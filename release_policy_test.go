package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCliffReleasePolicy(t *testing.T) {
	if os.Getenv("DISCOVERY_RELEASE_TESTS") != "1" {
		t.Skip("set DISCOVERY_RELEASE_TESTS=1 with pinned git-cliff installed")
	}
	binary, err := exec.LookPath("git-cliff")
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := os.ReadFile("cliff.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, version, group string
		commits              []string
	}{
		{name: "no commits", version: "v0.1.0"},
		{name: "fix", version: "v0.1.1", group: "Bug Fixes", commits: []string{"fix: repair auth"}},
		{name: "scoped perf", version: "v0.1.1", group: "Performance", commits: []string{"perf(registry): reduce requests"}},
		{name: "feature and perf", version: "v0.2.0", group: "Features", commits: []string{"perf: speed up checks", "feat: support tools"}},
		{name: "breaking perf", version: "v0.2.0", commits: []string{"perf!: change caching"}},
		{name: "breaking footer", version: "v0.2.0", commits: []string{"fix: change format\n\nBREAKING CHANGE: changed config"}},
		{name: "breaking ignored type", version: "v0.2.0", commits: []string{"ci!: change requirements"}},
		{name: "maintenance only", version: "v0.1.0", commits: []string{"docs: clarify setup", "ci: improve runner", "test: add case", "chore: update tools"}},
		{name: "release commit", version: "v0.1.0", commits: []string{"chore(release): 0.1.1 [skip ci]"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "cliff.toml"), configuration, 0600); err != nil {
				t.Fatal(err)
			}
			git := func(args ...string) string {
				t.Helper()
				command := exec.Command("git", args...)
				command.Dir = root
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v %s", args, err, output)
				}
				return strings.TrimSpace(string(output))
			}
			git("init", "-b", "main")
			git("config", "user.name", "Offline Fixture")
			git("config", "user.email", "fixture@example.invalid")
			git("add", "cliff.toml")
			git("commit", "-m", "feat: initial version")
			git("tag", "-a", "v0.1.0", "-m", "Release 0.1.0")
			for _, commit := range test.commits {
				git("commit", "--allow-empty", "-m", commit)
			}
			head := git("rev-parse", "HEAD")
			command := exec.Command(binary, "--unreleased", "--bumped-version")
			command.Dir = root
			output, err := command.Output()
			if err != nil || strings.TrimSpace(string(output)) != test.version {
				t.Fatalf("expected %s, got %s: %v", test.version, output, err)
			}
			if test.version != "v0.1.0" {
				command = exec.Command(binary, "--unreleased", "--tag", test.version, "--strip", "header")
				command.Dir = root
				notes, err := command.Output()
				if err != nil || len(notes) == 0 {
					t.Fatalf("release notes: %v %s", err, notes)
				}
				if test.group != "" && !strings.Contains(string(notes), test.group) {
					t.Fatalf("release notes missing group %s: %s", test.group, notes)
				}
				if strings.HasPrefix(test.name, "breaking") && !strings.Contains(string(notes), "Breaking:") {
					t.Fatalf("breaking commit was hidden from notes: %s", notes)
				}
			}
			if git("rev-parse", "HEAD") != head || git("tag", "--list") != "v0.1.0" {
				t.Fatal("git-cliff calculation or notes generation changed Git state")
			}
		})
	}
}
