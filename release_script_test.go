package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseScriptGuardsAndRecovery(t *testing.T) {
	if os.Getenv("DISCOVERY_RELEASE_TESTS") != "1" {
		t.Skip("set DISCOVERY_RELEASE_TESTS=1 with git-cliff and jq installed")
	}
	for _, tool := range []string{"git-cliff", "jq", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("scripts/release.sh")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile("cliff.toml")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	runGit := func(directory string, args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = directory
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runGit(root, "init", "--bare", remote)
	runGit(repo, "init", "-b", "main")
	runGit(repo, "config", "user.name", "Offline Fixture")
	runGit(repo, "config", "user.email", "fixture@example.invalid")
	for path, contents := range map[string][]byte{
		"scripts/release.sh": script, "cliff.toml": config,
		"CHANGELOG.md":            []byte("# Changelog\n\n## 0.1.0\n\nExisting notes.\n"),
		"extension.yaml":          []byte("version: 0.1.0\n"),
		"internal/cmd/version.go": []byte("package cmd\nvar Version = \"0.1.0\"\n"),
		".gitignore":              []byte("artifacts/\n"),
		// Publication tests stub only the expensive build, never release logic.
		"scripts/prepare-release.sh": []byte(`#!/usr/bin/env bash
set -euo pipefail
version="$1"
printf 'version: %s\n' "$version" > extension.yaml
printf 'package cmd\nvar Version = "%s"\n' "$version" > internal/cmd/version.go
mkdir -p artifacts
printf 'offline-test-bundle' > "artifacts/ikhemissi-azd-discovery_${version}.zip"
(cd artifacts && sha256sum "ikhemissi-azd-discovery_${version}.zip" > SHA256SUMS)
`),
	} {
		target := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(repo, "add", ".")
	runGit(repo, "commit", "-m", "feat: initial release")
	runGit(repo, "tag", "-a", "v0.1.0", "-m", "Release 0.1.0")
	runGit(repo, "remote", "add", "origin", remote)
	runGit(repo, "push", "-u", "origin", "main", "--tags")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	fakeGH := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GH_TEST_LOG"
case "$1 $2" in
  "release list") printf '%s\n' "${GH_TEST_RELEASES:-[]}" ;;
  "release create")
    if [[ "${GH_TEST_FAIL:-0}" == 1 ]]; then exit 1; fi ;;
  "release upload"|"release edit") ;;
  *) echo 'Unexpected GitHub operation' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "gh.log")
	execute := func(fail, releases string, args ...string) (string, error) {
		t.Helper()
		command := exec.Command("bash", append([]string{"scripts/release.sh"}, args...)...)
		command.Dir = repo
		command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GH_REPO=fixture/example", "GH_TEST_LOG="+log, "GH_TEST_FAIL="+fail, "GH_TEST_RELEASES="+releases)
		output, err := command.CombinedOutput()
		return string(output), err
	}
	if output, err := execute("0", "[]", "--dry-run"); err != nil || !strings.Contains(output, "No releasable changes") {
		t.Fatalf("no-change release: %v %s", err, output)
	}
	runGit(repo, "commit", "--allow-empty", "-m", "perf: reduce requests")
	runGit(repo, "push", "origin", "main")
	head := runGit(repo, "rev-parse", "HEAD")
	if output, err := execute("0", "[]", "--dry-run"); err != nil || !strings.Contains(output, "v0.1.1") {
		t.Fatalf("release candidate: %v %s", err, output)
	}
	if runGit(repo, "rev-parse", "HEAD") != head || runGit(repo, "tag", "--list") != "v0.1.0" {
		t.Fatal("dry run mutated release state")
	}
	if output, err := execute("1", "[]"); err == nil {
		t.Fatalf("publication failure must be surfaced: %s", output)
	}
	if runGit(repo, "log", "-1", "--format=%s") != "chore(release): 0.1.1 [skip ci]" ||
		runGit(repo, "cat-file", "-t", "v0.1.1") != "tag" ||
		runGit(repo, "rev-parse", "HEAD") != runGit(root, "--git-dir", remote, "rev-parse", "main") {
		t.Fatal("atomic release metadata/tag push did not precede publication")
	}
	const draft = `[{"tagName":"v0.1.1","isDraft":true}]`
	if output, err := execute("0", draft); err != nil {
		t.Fatalf("draft recovery: %v %s", err, output)
	}
	const published = `[{"tagName":"v0.1.1","isDraft":false}]`
	head = runGit(repo, "rev-parse", "HEAD")
	if output, err := execute("0", published); err != nil || !strings.Contains(output, "already published") {
		t.Fatalf("published rerun: %v %s", err, output)
	}
	if runGit(repo, "rev-parse", "HEAD") != head {
		t.Fatal("rerun created another release commit")
	}
	calls, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(calls), "release upload v0.1.1") ||
		!strings.Contains(string(calls), "release edit v0.1.1") {
		t.Fatalf("draft recovery did not finish publication: %v %s", err, calls)
	}
	runGit(repo, "commit", "--allow-empty", "-m", "fix: unpushed change")
	if output, err := execute("0", published, "--dry-run"); err == nil || !strings.Contains(output, "stale") {
		t.Fatalf("stale checkout accepted: %v %s", err, output)
	}
}
