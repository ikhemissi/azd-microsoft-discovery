# Contributing

The [README](README.md) covers installing published bundles and using the extension.
This guide covers development, validation, and release maintenance.

## Development requirements

| Tool | Version or role |
| --- | --- |
| Go | `>=1.26.4`; the public azd SDK is pinned to `1.35.0` |
| azd | `>=1.35.0`; keep minimum-host compatibility tested |
| Extension developer tooling | `microsoft.azd.extensions` `0.13.0` |
| Podman | Native container tests; local baseline `4.9.3` |
| golangci-lint | `2.11.4`, matching CI |
| git-cliff | `2.14.2`, for release-policy tests and publishing |
| Git, Bash, jq, gh | Linux build/release orchestration |

No Node/npm, SVU, or Rust compiler is required. Git-cliff is installed as a
checksum-verified standalone binary.

Start from the latest `main`; release automation pushes version/changelog commits:

```sh
git pull --ff-only
export AZD_CONFIG_DIR=/absolute/path/to/isolated-azd-config
export AZD_COLLECT_TELEMETRY=no
```

Do not overwrite your normal azd configuration or container authentication.
Infrastructure, networking, identities, roles, and capacity belong to Terraform.
Do not use private SDK packages, read Terraform state directly, add dummy Bicep,
or introduce Docker/az CLI wrappers.

## Build and install a development bundle

From the extension repository root:

```sh
azd extension install microsoft.azd.extensions --version 0.13.0
azd x build
VERSION="$(./bin/ikhemissi-azd-discovery-linux-amd64 version --output json | jq -er '.version')"
azd x pack --bundle --output ./artifacts
azd extension install "./artifacts/ikhemissi-azd-discovery_${VERSION}.zip" --force
azd discovery version
```

Install the bundle after building: `azd x build` alone can retain stale provider
metadata on this tooling version. Use the isolated `AZD_CONFIG_DIR` for native
tests. `bin/`, `artifacts/`, and `coverage/` are disposable, Git-ignored outputs.
Build scripts use `-trimpath` and disable CGO.

To download a tested CI build instead:

```sh
gh run download <run-id> --repo ikhemissi/azd-microsoft-discovery \
  --name discovery-bundle-linux-amd64 --dir ./downloaded-extension
cd downloaded-extension
sha256sum --check SHA256SUMS
azd extension install ./ikhemissi-azd-discovery_<version>.zip --force
```

Actions artifacts expire after 30 days. Releases provide persistent distribution.
`gh run download` extracts GitHub's outer archive; install the extension ZIP inside.

## Validate changes

```sh
bash scripts/install-git-cliff.sh /absolute/path/to/git-cliff-tools
export PATH=/absolute/path/to/git-cliff-tools:$PATH
export DISCOVERY_RELEASE_TESTS=1

golangci-lint run --timeout 10m
bash scripts/check-go.sh
```

The shared script checks formatting, compilation, vet, uncached race tests,
coverage, and modernization. Coverage is informational, not a certification
claim or arbitrary percentage gate. Linux CI also validates:

- Provider registration against the manifest.
- Schema roundtrips through azd metadata caching.
- Terraform-safe credential construction and fresh environment values.
- Documentation examples and independently authored API fixtures.
- Digest verification, strict agent readback, reconciliation, and recovery.
- GA KB definition/index operation correlation, automatic/manual indexing,
  subscription overrides, ambiguous submissions, and export restoration without duplicate jobs.
- Commit-based version rules, annotated tags, stale-checkout guards, and draft recovery.

Native offline tests build a scratch image without pulling a base image:

```sh
bash scripts/build-test-bundle.sh
```

This builds/installs a fresh bundle in your isolated configuration, runs native
container/reference/agent/KB packaging and diagnostic tests, validates bundle
metadata, and writes `SHA256SUMS`. Fixture images are cleaned on failure too.
No ACR publication, provisioning, agent invocation, or indexing is performed.

### Standalone diagnostics

For strict read-only inspection, supply required provisioning variables through
the process environment rather than an azd environment:

```sh
go build -o bin/discovery .
./bin/discovery plan --local --cwd /path/to/demo --output json
./bin/discovery doctor --local --cwd /path/to/demo --output json
```

Core azd 1.35.0 can maintain `.env.lock`, repair `AZURE_ENV_NAME`, create an empty
environment `config.json`, or hydrate remote state before the extension starts.
Native failures can append a human-readable JSON error trailer. Tests permit
only the exact documented host exceptions; other mutations/output drift fail.

## Contracts and safety boundaries

- Keep schema, metadata, examples, documentation, and runtime behavior aligned.
- Preserve source definitions and prompts; resolve deployment bindings late.
- Use draft 2020-12 `$defs` so azd's typed metadata cache preserves definitions.
- Definitions must remain inside the project after symlink resolution. Bounded
  YAML documents reject anchors, aliases, duplicate/non-string keys, and custom tags.
- Send agent description in `foundryDetails.description`; verify it only when
  returned at the optional `foundryDetails.versions.latest.description`. See
  [the Foundry contract](https://github.com/Azure/azure-rest-api-specs/blob/2927d4ca395b75c4d4b879755dabfb32cb981687/specification/ai-foundry/data-plane/Foundry/src/agents/models.tsp).
  All other managed fields remain strictly verified.
- Fixtures and successful individual deployments do not establish every live
  API, authentication, permission, endpoint, or failure/recovery contract.
- Cloud provisioning/publication/deployment requires explicit authorization.
  Agent invocation, simulations, indexing, uploads, role grants, and scaling
  require separate consideration; do not hide them in deployment tests.
- KB deployments index by default. Offline tests must never submit actual jobs.
  Bookshelves and storage registrations are GET-only prerequisites; source
  identities are omitted rather than inferred. GA KBs have no versions; keep
  agent attachment blocked until its released contract is verified.

## Conventional Commits and automatic releases

Use Conventional Commit subjects, including squash-merge titles:

| Commit | Release effect |
| --- | --- |
| `fix:` / `perf:` (including scopes) | Patch |
| `feat:` | Minor |
| Breaking marker or `BREAKING CHANGE:` footer | Minor during `0.x`, major after `1.0` |
| Maintenance-only `docs:`, `chore:`, `ci:`, `test:`, etc. | No release unless breaking |

[cliff.toml](cliff.toml) is the policy and categorized-note template. The highest
applicable bump wins; generated release commits do not trigger another release.
Do not manually bump the version or push a release tag for ordinary changes.

On pushes to `main`, [the release workflow](.github/workflows/release.yml):

1. Requires the standard Linux checks to pass.
2. Checks current Git history and determines the next version.
3. Generates notes, preserves older changelog entries, and synchronizes manifest/binary versions.
4. Runs shared Go checks and native tests on the new-version bundle.
5. Atomically pushes the release metadata commit and annotated Git tag.
6. Publishes the exact tested bundle, checksum, and commit-based notes using `gh`.

PR jobs have read-only permissions. Only the release job has `contents: write`,
using the built-in GitHub token; it needs no Azure credentials or personal token.
Branch protection must allow the Actions bot to push release metadata, or use an
approved GitHub App/bypass policy. The workflow never changes branch rules itself.
`[skip ci]` and GitHub token behavior prevent recursive workflow runs; publication
does not depend on a second workflow being triggered by the generated tag.

For a read-only candidate check:

```sh
bash scripts/release.sh --dry-run
```

It requires a clean, current `main` checkout and fetches Git history, but does not
edit files, build, create tags, or publish. Policy/orchestration tests use local
Git repositories and a fake `gh`, without creating real releases.

### Publication recovery

Releases are serialized. Stale checkouts, existing candidate tags, and published
releases are not overwritten. If publication fails after pushing the metadata
commit/tag, rerun at that release commit while it is still current `main`.
The workflow can rebuild/test and finish an unpublished draft; only draft assets
may be replaced. Published releases remain unchanged.

CI and release support remain Linux-only. Cross-platform certification and a
custom azd registry are separate follow-up work, not prerequisites for this flow.
