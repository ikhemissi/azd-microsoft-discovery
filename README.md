# Microsoft Discovery Community Extension for azd

Build container images, register Microsoft Discovery tools, and deploy prompt
agents through the Azure Developer CLI (`azd`).

**Community-maintained:** this is not an official Microsoft-supported extension.
Local packaging and offline lifecycle tests pass; live Azure deployment has not
yet been verified.

## At a glance

| Service target | Role | Infrastructure ownership |
| --- | --- | --- |
| `discovery.tool` | Build and publish a container, or use an existing immutable image; register the tool | The extension manages tool registration |
| `discovery.agent` | Verify the model deployment, resolve tool bindings, and deploy a prompt agent | The extension manages the agent |

The extension has two service targets: **tools and agents**. Agents select an
existing Terraform-owned model deployment by name.

The intended flow is:

```text
Terraform provision → container build/package → ACR publish
                    → tool registration → agent deployment
```

- Core azd handles service ordering through `uses`.
- No global postdeploy hook, custom scheduler, or Docker/az CLI wrapper is needed.
- Terraform owns infrastructure, identities, RBAC, networking, capacity, and teardown.
- Deployment does not invoke agents, run simulations, upload documents, index
  knowledge bases, grant roles, or scale compute.

## Requirements and installation

| Requirement | Version or purpose |
| --- | --- |
| azd | `>=1.35.0`; SDK and compatibility tests are pinned to `1.35.0` |
| Container runtime | Docker or working local Podman for build mode |
| Terraform | For the demo's infrastructure provisioning |
| Azure permissions | Provision deployment access and Discovery image access through IaC |

### Install a published release

GitHub Release bundles support **Linux amd64**. Users do not need Go, a clone of
this repository, or extension developer tooling to install them.

Choose a published version from [GitHub Releases](https://github.com/ikhemissi/azd-microsoft-discovery/releases):

```sh
# Select an existing published version.
VERSION=0.1.0
azd extension install \
  "https://github.com/ikhemissi/azd-microsoft-discovery/releases/download/v${VERSION}/ikhemissi-azd-discovery_${VERSION}.zip"
azd discovery version
```

- Upgrade by installing the bundle URL for a newer version. Bundle installations
  do not track a registry for automatic `azd extension update`.
- Use `--force` only when deliberately reinstalling or replacing an installed build.
- Install into the same `AZD_CONFIG_DIR` used for your application.
- Each release includes `SHA256SUMS`. A checksum detects corruption but is not
  a publisher signature; download only from a trusted repository.

Authentication stays with azd. For both Discovery requests and ACR digest
verification, the extension reads `AZURE_SUBSCRIPTION_ID` from the effective azd
environment and resolves its tenant through the public account API. It validates
the IDs and supplies the tenant explicitly to the SDK token provider, without
deployment-context lookup or Bicep compilation. No separate tenant setting is
needed; a missing or invalid subscription selection blocks credential creation.

### Build and install locally

Developers also need Go `>=1.26.4` and `microsoft.azd.extensions` `0.13.0`.
From this extension's repository root:

```sh
export AZD_CONFIG_DIR=/absolute/path/to/isolated-azd-config
export AZD_COLLECT_TELEMETRY=no

azd extension install microsoft.azd.extensions --version 0.13.0
azd x build
azd x pack --bundle --output ./artifacts
azd extension install ./artifacts/ikhemissi-azd-discovery_0.1.0.zip --force
azd discovery version
```

Use an **isolated azd configuration** for development. Do not overwrite your
normal extension installation or container credentials.

> Install the bundle after building. On this tooling version, `azd x build`
> alone can retain stale provider metadata and cause an “unsupported host” error.
> A custom or official extension-registry listing is not required for release
> URL installation.

## Set up the GROMACS demo

Use the [complete Terraform + build-mode example](examples/gromacs-build/azure.yaml)
with [ikhemissi/microsoft-discovery-gromacs-sample](https://github.com/ikhemissi/microsoft-discovery-gromacs-sample).

1. Copy the example to the demo's root as `azure.yaml`.
2. Keep the demo's `infra/` and `experiments/lysozyme-water/` files.
3. Remove the old Python postdeploy hook and use the example's native services.
4. Add the model-name Terraform output below before provisioning.
5. Remove `displayName` from the demo's agent manifest. This authoring label has
   no verified request adapter and is rejected; keep `name`, description, and prompts.

| Provisioning output | Used for |
| --- | --- |
| `DISCOVERY_WORKSPACE_ID` | Workspace ARM ID |
| `DISCOVERY_PROJECT_ID` | Project ARM ID |
| `DISCOVERY_NODE_POOL_ID` | Tool compute prerequisite |
| `AZURE_CONTAINER_REGISTRY_ENDPOINT` | Explicit tool publication registry |
| `DISCOVERY_CHAT_MODEL_DEPLOYMENT_NAME` | Agent model selection; add this output |

```hcl
output "DISCOVERY_CHAT_MODEL_DEPLOYMENT_NAME" {
  description = "Existing Discovery chat model deployment name for agents."
  value       = azapi_resource.chat_model.name
}
```

The inspected demo baseline is `9d811fc54ecc358f7ab2e161ddb323e0f696b4af`.
The demo repository itself has not been modified.

**Provision before building:** the example's sequential `workflows.up` runs
`provision`, then `deploy --all`. Core azd 1.35.0's default up workflow can
package concurrently with provisioning, so retain this override.

## Configuration

### Project settings

These settings belong under the top-level `discovery` block.

| Setting | Requirement |
| --- | --- |
| `schemaVersion` | Required; `"1.0"` |
| `workspaceResourceId` | Required; workspace ARM ID |
| `projectResourceId` | Required; project ARM ID within that workspace |
| `nodePoolResourceId` | Required when a tool is declared |
| `operationTimeoutSeconds` | Optional; default `1800`, range `1–7200` |

Provisioning outputs can be referenced with `${VARIABLE}`. Build/package defer
missing infrastructure outputs; Publish/Deploy require them to resolve.
Configuration and environment values are refreshed at lifecycle boundaries,
with azd environment values taking precedence over process variables.

### Container tools

Core azd settings select the build framework; extension settings describe the
Discovery integration.

| Setting | Default / behavior |
| --- | --- |
| `project` | Required for build mode; use `.` for the application root |
| `language` | Set to `docker` for build mode; omit for reference mode |
| `docker.path` | `./Dockerfile`, relative to the service project |
| `docker.context` | `.`, relative to the service project |
| `docker.target` | Optional build stage; the demo uses `discovery` |
| `docker.platform` | Core default `linux/amd64` |
| `docker.registry` | Required for build mode; explicit public-cloud ACR hostname |
| `docker.image` / `docker.tag` | Optional; core azd owns image naming and tagging |
| `config.type` | `container`; MCP and OpenAPI lifecycles are not implemented |
| `config.definition` | `tool.yaml`, relative to the service project; the demo overrides it with `tool.cpu.yaml` |
| `config.image.mode` | `build`; alternatively, `reference` |
| `config.image.infraNode` | Inferred only when exactly one eligible named image entry exists |
| `config.image.reference` | Required in reference mode; fully qualified OCI SHA-256 digest without a tag |

**What is `infraNode`?** It selects the named `infra` entry in the tool
definition whose `image.acr` is replaced. Code environments refer to that name
through `infra_node`. It is **not** an Azure node pool. If multiple eligible
entries exist, select one explicitly.

| Image mode | Build/push behavior | Registered image |
| --- | --- | --- |
| `build` | Native azd build, package, and ACR publication | Verified immutable digest |
| `reference` | No build, tag, login, push, or registry lookup | Exact supplied digest |

To use an existing image, keep the service project and omit `language` and
`docker`. Use this configuration:

```yaml
config:
  definition: ./tool.cpu.yaml
  image:
    mode: reference
    infraNode: worker
    reference: ${DISCOVERY_TOOL_IMAGE_DIGEST}
```

Build-mode boundaries:

- Use a registry hostname such as `example.azurecr.io`, not an HTTPS URL.
- Discovery has no verified default publication registry. The registry output
  above is a demo convention.
- Core prioritizes `AZURE_CONTAINER_REGISTRY_ENDPOINT` over `docker.registry`;
  conflicting values are rejected.
- `remoteBuild` and `imagePassthrough` are not supported. Existing images use
  reference mode instead.
- `azd publish --to` is rejected because the pinned Publish RPC cannot forward it.
- Build publication supports public-cloud ACR only, not sovereign endpoints or
  external registries.
- Build-produced multi-platform indexes are rejected. Immutable reference
  indexes are accepted; Discovery's access to the image remains your responsibility.

### Agents and model selection

```yaml
researcher:
  host: discovery.agent
  project: ./experiments/lysozyme-water
  uses: [simulation]
  config:
    model:
      deploymentName: ${DISCOVERY_CHAT_MODEL_DEPLOYMENT_NAME}
    bindings:
      gromacsToolId:
        service: simulation
        output: resourceId
```

| Setting | Default / behavior |
| --- | --- |
| `config.definition` | `agent.yaml`, relative to the service project |
| `config.model.deploymentName` | Optional; existing workspace model deployment name |
| `config.bindings` | Empty object; maps whole-field placeholders to dependency outputs |
| `uses` | Declare services referenced by bindings; core azd controls ordering |

- **Use the deployment name**, not a model ARM ID or necessarily the catalog model name.
- A configured deployment name overrides `model.id` in the deployment copy only.
  The manifest's model field may be omitted or contain an old placeholder.
- Without that setting, the manifest must supply a literal deployment name in
  `model.id`. Model service bindings are not supported.
- Keep tool placeholders and prompts in the manifest. Instructions and runtime
  placeholders are preserved; source definitions are never rewritten.
- Missing, failed, or mismatched model/tool readback blocks agent submission.
- Missing model deployments are not created; model provisioning stays in Terraform.
- Unsupported fields, including `displayName`, fail preflight rather than being discarded.
- KB attachments and Bookshelf service configuration are outside this iteration
  and fail preflight.
- Duplicate managed tool/agent targets are rejected. Agents may share the same
  existing model deployment.

## Commands

### Inspect and package locally

| Command | Purpose |
| --- | --- |
| `azd discovery plan` | Inspect configuration and intended operations |
| `azd discovery doctor` | Validate local configuration, not Azure readiness |
| `azd package <service>` | Build and package locally; no image push or Azure deployment |

From the demo directory:

```sh
azd env new discovery-local
export AZD_CONTAINER_RUNTIME=podman
export REGISTRY_AUTH_FILE=/absolute/path/to/isolated-container-auth.json

azd package simulation --environment discovery-local --no-prompt
azd package researcher --environment discovery-local --no-prompt
```

Packaging may download base images/packages and consume local resources.
There is no separate extension build command for service containers.

For strictly local diagnostics, build the standalone executable from the
extension repository and supply the demo's required variables in your process
environment:

```sh
go build -o bin/discovery .
./bin/discovery plan --local --cwd /path/to/demo --output json
./bin/discovery doctor --local --cwd /path/to/demo --output json

# Native diagnostics for an existing azd environment:
azd discovery doctor --cwd /path/to/demo --environment demo --output json
```

Diagnostics validate schemas, scopes, paths, definitions, bindings, and output
aliases. Missing required environment values block them.
`deploymentSupported: true` means providers exist—not that Azure is ready.

> **Read-only caveat:** azd 1.35.0 can maintain `.env.lock`, repair
> `AZURE_ENV_NAME`, create an empty environment `config.json`, or hydrate remote
> state before the extension starts. Failed native JSON commands can also contain
> a trailing human-readable error. Use standalone `--local` for strict read-only
> behavior and parseable failures; it uses only process environment values.

### Provision and deploy to Azure

**Run these commands only after explicit authorization for provisioning, image
publication, and application deployment.** Review Terraform capacity/costs and
configure the authorized subscription, location, and IaC-owned access first.

```sh
cd /absolute/path/to/microsoft-discovery-gromacs-sample
azd auth login
azd env new discovery-demo
azd env set AZURE_SUBSCRIPTION_ID <authorized-subscription-id>
azd env set AZURE_LOCATION <authorized-discovery-region>
export AZD_CONTAINER_RUNTIME=podman

azd provision --environment discovery-demo
azd deploy --all --environment discovery-demo

# Alternative: the example's sequential workflow performs both stages.
azd up --environment discovery-demo
```

Choose **either** the separate stages or `azd up`; do not run both unintentionally.

| Command | Important behavior |
| --- | --- |
| `azd deploy --all` | Includes build/package/publish, tool registration, and agent deployment |
| `azd deploy researcher` | Requires previously deployed, verified tool records; does not rebuild dependencies |
| `azd publish simulation` | Pushes to ACR without registering the tool; still a cloud mutation |
| `azd down` | IaC teardown owns its scope; no custom application-deletion hook |

A separate `azd package --all` is optional for inspecting local artifacts.
It can cause another build on a later deploy because lifecycle caches are
process-local.

## Verification, outputs, and recovery

### Deployment checks

- Reuse core build/package artifacts; use public container RPCs when absent.
- Resolve the published tag to a manifest digest, hash its bytes, verify the
  digest header, and fetch by digest again.
- Compare the manifest's config digest with the packaged image identity to
  guard against a concurrently overwritten tag.
- Replace only the selected tool image, wait for readiness, and verify the
  registered definition and version by GET.
- Resolve agent bindings during Deploy—not Package—and verify the latest
  agent version before exporting results.
- Description is sent in `foundryDetails.description`. The
  [Foundry response contract](https://github.com/Azure/azure-rest-api-specs/blob/2927d4ca395b75c4d4b879755dabfb32cb981687/specification/ai-foundry/data-plane/Foundry/src/agents/models.tsp)
  returns it optionally at `foundryDetails.versions.latest.description`.
  A returned mismatch fails verification; omission does not imply failure or
  trigger another immutable version when all other managed fields match.
  Name, model, instructions, tools, confirmation policies, extension flags,
  provisioning state, and a nonempty version remain strictly verified.

### Exported environment values

| Service | Outputs |
| --- | --- |
| Tool | `SERVICE_<NAME>_RESOURCE_ID`, `SERVICE_<NAME>_IMAGE` |
| Agent | `SERVICE_<NAME>_NAME`, `SERVICE_<NAME>_VERSION` |

Recovery records use atomic writes and per-service locks under
`.azure/<environment>/discovery/`. They do not copy definitions, prompts, tokens,
or Terraform state.

| Situation | Recovery behavior |
| --- | --- |
| Export failure | Rerun to restore exports without re-upserting unchanged resources |
| Definitive rejection or failed/canceled operation | Record failure; allow a later corrected deployment |
| Previously pending operation has failed | Report and record failure first; the following rerun can submit the correction |
| Lost agent POST response | Reconcile with GET; never blindly retry |
| Ambiguous submission or polling transport/access failure | Retain pending state and block unsafe duplicate submission |
| Failed application resource | Allow repair; failed infrastructure prerequisites still block deployment |

## Testing and quality checks

CI is **Linux-only** and covers:

- Formatting, build, vet, `unused`/`errorlint`, and Go modernization.
- Uncached race tests with coverage.
- Metadata SDK-cache roundtrips, provider/manifest matching, and environment refresh.
- Documentation examples, independent readback fixtures, and failure/recovery regressions.
- A fresh bundle installed on azd 1.35.0, followed by native offline smoke tests.

Successful CI runs upload `discovery-bundle-linux-amd64`, containing the tested
bundle and its checksum, with 30-day retention. Download a specific development
build using authenticated GitHub CLI:

```sh
gh run download <run-id> --repo ikhemissi/azd-microsoft-discovery \
  --name discovery-bundle-linux-amd64 --dir ./downloaded-extension
cd downloaded-extension
sha256sum --check SHA256SUMS
azd extension install ./ikhemissi-azd-discovery_<version>.zip --force
```

The artifact contains the extension ZIP directly after `gh run download` extracts
GitHub's outer archive. Actions artifacts are temporary; use Releases for persistent
versioned distribution.

Run quality checks from the extension repository:

```sh
golangci-lint run --timeout 10m
go fix -diff ./...
go vet ./...
mkdir -p coverage
go test -race -count=1 -coverprofile=coverage/offline.out ./...
go tool cover -func=coverage/offline.out
```

Use golangci-lint **v2.11.4** to match CI. Coverage is informational, not a
percentage gate or cloud certification.

After installing the bundle in an isolated `AZD_CONFIG_DIR`, run native tests:

```sh
AZD_DISCOVERY_CONTAINER_TESTS=1 \
  go test -count=1 -v -run TestNativeContainerPackage .
AZD_DISCOVERY_NATIVE_TESTS=1 \
  go test -count=1 -v -run 'Test(NativeReferencePackage|(Native|Standalone)ReadOnlyCommands)' .
```

These use a scratch image without base-image pulls, provisioning, publication,
or simulations. Fixture images are cleaned up even on failure. Native failure
tests allow only the precisely documented host metadata/error exceptions.

Build and validation notes:

- Canonical schemas use draft 2020-12 `$defs` to survive azd metadata caching.
- Definitions stay inside the project after symlink resolution. YAML accepts
  one bounded object document, without anchors, aliases, duplicate keys,
  non-string keys, or unsupported tags.
- `bin/`, `artifacts/`, and `coverage/` are disposable generated output, ignored
  by Git. Build/package/test commands recreate them; they are not source assets.
- Build scripts use `-trimpath`, disable CGO, and support a repository with no
  commits by setting commit metadata to `none`.

### Automatic GitHub Releases

The first normal release is [v0.1.0](https://github.com/ikhemissi/azd-microsoft-discovery/releases/tag/v0.1.0).
Subsequent releases use pinned **git-cliff 2.14.2** and Conventional Commits;
there is no manual version bump, release PR, or tag step.

| Commit | Release effect during `0.x` |
| --- | --- |
| `fix: ...`, `perf: ...` | Patch, for example `0.1.0` to `0.1.1` |
| `feat: ...` | Minor, for example `0.1.0` to `0.2.0` |
| `feat!: ...` or a `BREAKING CHANGE:` footer | Minor while the public API is `0.x` |
| `docs: ...`, `chore: ...`, `ci: ...`, `test: ...` without breaking changes | No release |

The highest applicable bump across commits since the last release wins. Use
Conventional Commit subjects for commits or squash-merge titles. `perf:` is a
patch trigger, including scoped performance changes. Breaking changes on normally
ignored types remain releasable and visible in notes. Git-cliff preserves `0.x`
for breaking changes and uses major increments once a deliberate `1.0` baseline
exists.

For each push to `main`, the Linux-only release workflow:

1. Requires the regular quality and native checks to pass.
2. Reads the complete Git history and release tags to compute the next version.
3. Updates the manifest and binary version before running tests and building.
4. Tests the new-version bundle and writes its checksum.
5. Commits only version metadata and generated changelog, atomically pushes the
   corresponding annotated Git tag and release commit, and publishes the exact
   tested bundle plus `SHA256SUMS` and git-cliff-generated release notes using `gh`.

Normal versions have no preview suffix and are not GitHub prereleases. Releases
are serialized and published tags/assets are not intentionally overwritten.
The generated release commit uses `[skip ci]`; the built-in GitHub token also
prevents recursive workflow triggers. Publishing is performed in the same workflow,
not by relying on another workflow triggered by the generated tag.

Only the release job has `contents: write`; PR jobs cannot publish. It uses the
built-in `GITHUB_TOKEN`, with no personal token or Azure credentials required.
Repository branch rules must permit the Actions bot to push the three release
metadata files; protected branches may require an approved GitHub App/bypass policy.
The workflow does not grant itself permissions or modify branch protection.

Release jobs check that the checkout is current before work starts and again
before pushing, skip unchanged versions, and refuse to reuse existing tags or
overwrite published releases. If publication fails after the release commit/tag
was pushed, rerun the workflow at that release commit: it can rebuild/test and
finish publication, including an unpublished draft. Only draft assets may be
replaced during recovery; published releases are left unchanged.

There is no Node/npm or SVU dependency. Git-cliff is a standalone Rust binary,
installed from its pinned Linux release with SHA-512 verification; no Rust
compiler is required. A small Go helper synchronizes native version files.
The same Go/native checks and bundle script are shared between CI and releases.
Offline tooling tests and dry-run:

```sh
bash scripts/install-git-cliff.sh /absolute/path/to/git-cliff-tools
export PATH=/absolute/path/to/git-cliff-tools:$PATH
DISCOVERY_RELEASE_TESTS=1 go test -count=1 -run '^TestGitCliffReleasePolicy$' .
go test ./scripts/release-version
bash scripts/release.sh --dry-run
```

The dry-run requires a clean, current `main` checkout and fetches Git history,
but does not edit version files, create tags, build or publish releases.
CI tests commit classification, categorized notes, `perf` handling, zero-major
breaking rules, version synchronization and unchanged Git state.

## Known limits and remaining verification

| Area | Current boundary |
| --- | --- |
| Local compatibility | Tested with azd 1.35.0 and Podman 4.9.3 |
| Container publication | Offline-tested; live ACR credentials/RBAC and Podman push remain unverified |
| Discovery API | Offline-tested; endpoint, payload, readiness, and recovery behavior need authorized tenant testing |
| API versions | Tool ARM API: `2026-06-01`; agent workspace API: `2026-02-01-preview` |
| Endpoint and authentication | Tool IDs use workspace subscription/RG; workspace endpoint derives from verified name; SDK credentials use the selected subscription's resolved tenant and ARM/Discovery audiences |
| GPU workloads | Not certified |
| Knowledge bases | No KB target, upload, indexing, attachment, or status/index command |
| KB follow-up questions | Storage-location resolution, GA agent attachment, and whether optional source identity can be omitted |

No live Azure provisioning, publication, or Discovery mutation has been
authorized or performed during development. Offline fixtures are not proof of
the live API contract.

Further detail: [configuration schema](schemas/discovery.schema.json) ·
[changelog](CHANGELOG.md).

## Roadmap

The tool/agent lifecycle is implemented and tested locally and on GitHub's Linux
runner. Both quality and native smoke-test jobs passed for `c9189fe` in
[the first hosted CI run](https://github.com/ikhemissi/azd-microsoft-discovery/actions/runs/38085870149).
The next priority is live validation, not additional service types. Cloud tests
require explicit authorization; CI remains Linux-only.

### 1. Validate the first iteration

| Task | Completion criteria |
| --- | --- |
| Run hosted CI | Complete: Linux quality and native smoke-test jobs passed for `c9189fe`; keep these checks passing |
| Test a newer azd host | Existing packaging and command tests pass alongside the pinned minimum host |
| Migrate the GROMACS demo | Add the model-name Terraform output and replace the old hook with native services |
| Validate build mode in Azure | Provision, publish to ACR, verify the digest, register the tool, and deploy the agent with correct bindings |
| Validate reference mode in Azure | No build/push; tool readback contains the exact supplied digest |
| Exercise redeployment and recovery | Verify unchanged agent reconciliation, interrupted operations, failed exports, and corrected failures against the live service |

Resolve any endpoint, authentication, API-contract, or Podman-publication
differences exposed by these tests before calling the release cloud-verified.

### 2. Publish and validate releases

- First normal release `v0.1.0` is published with its Git tag and checksum.
- Enable git-cliff release automation by committing/pushing the workflow changes;
  future versions and releases are generated from Conventional Commits.
- Verify installation from the published release in a clean, isolated configuration.
- Pin the extension version in demo instructions.
- Record tested host/runtime versions and remaining limitations.
- Optionally add a custom azd registry later for install-by-ID and automatic updates.

### 3. Extend functionality after the first iteration

| Feature | Planned scope |
| --- | --- |
| `discovery.knowledgebase` | Reconcile named KBs under Terraform-owned Bookshelves; start with existing storage-backed sources |
| Agent KB attachment | Verify the GA reference format and attachment before first indexing; do not invent version suffixes |
| Indexing | Add an explicit index command and optional deployment-triggered indexing with `autoindex: true` by default |
| MCP/OpenAPI tools | Verify Discovery contracts and implement their complete lifecycles |
| Local-folder ingestion | Upload support after storage-backed KB ingestion works |
| Remote diagnostics | Extend doctor beyond local configuration validation |
| GPU workloads | Separate compatibility and workload certification |
| Upstream contribution | Optional community registry listing or source contribution |

For KB support, preserve these design boundaries:

- Resolve the parent Bookshelf by name in the azd subscription/resource group,
  with explicit scope overrides available.
- Verify source-location resolution, GA agent attachment, and whether the
  optional source identity can be omitted before finalizing configuration.
- Prefer omitting the optional identity and surfacing API errors unless testing
  establishes that an identity must be supplied.
- With `autoindex: false`, reconcile configuration without indexing; verify that
  agent deployment remains possible before relying on this behavior.
- Keep infrastructure, permissions, and compute capacity under Terraform ownership.
  Do not introduce implicit role grants or scaling.

Cross-platform checks remain deferred and are not first-iteration release gates.
