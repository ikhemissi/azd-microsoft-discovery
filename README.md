# Microsoft Discovery Community Extension for azd

Build container images, register Microsoft Discovery tools, and deploy prompt
agents with the Azure Developer CLI.

**Community-maintained, not officially supported by Microsoft.** Releases use
normal `0.x` versions; configuration and behavior may change between releases.

| Service | Purpose |
| --- | --- |
| `discovery.tool` | Build/publish a container or reference an immutable image, then register the tool |
| `discovery.agent` | Verify an existing model deployment, resolve tool bindings, and deploy the agent |

```text
Terraform provision → container build/package → ACR publish
                    → tool registration → agent deployment
```

Terraform owns infrastructure, identities, permissions, networking, and capacity.
Core azd handles dependencies through `uses`; no postdeploy hook is required.
Deployment does not invoke agents, run simulations, index data, grant roles, or
scale compute.

## Install or update

Prebuilt release bundles support **Linux amd64**. You need **azd >=1.35.0**,
but not Go, Node, Rust, developer tooling, or a clone of this repository.

Choose a version from [GitHub Releases](https://github.com/ikhemissi/azd-microsoft-discovery/releases):

```sh
VERSION=0.2.0
azd extension install \
  "https://github.com/ikhemissi/azd-microsoft-discovery/releases/download/v${VERSION}/ikhemissi-azd-discovery_${VERSION}.zip"
azd discovery version
```

- **Update:** install a newer version's bundle URL. Bundle installs do not use
  `azd extension update` because they do not track an extension registry.
- **Reinstall:** add `--force` when deliberately replacing an installed build.
- Use the same `AZD_CONFIG_DIR` for installation and your application commands.
- Each release includes `SHA256SUMS`; download only from the trusted repository.
  Checksums detect corruption, not publisher identity.

## Quick start: GROMACS demo

Use the [complete build-mode configuration](examples/gromacs-build/azure.yaml)
with [microsoft-discovery-gromacs-sample](https://github.com/ikhemissi/microsoft-discovery-gromacs-sample):

1. Copy the example to the demo root as `azure.yaml`; retain its infrastructure
   and experiment files.
2. Remove the old Python deployment hook. Keep the example's sequential
   `workflows.up`: provision first, then `deploy --all`.
3. Remove the unsupported `displayName` field from the agent manifest.
4. Add this model-name output to the demo's Terraform configuration:

```hcl
output "DISCOVERY_CHAT_MODEL_DEPLOYMENT_NAME" {
  description = "Existing Discovery chat model deployment name for agents."
  value       = azapi_resource.chat_model.name
}
```

The example also uses Terraform outputs for the workspace, project, node pool,
and ACR endpoint. The model remains Terraform-owned; the extension does not
create model deployments.

**Review and authorize Azure costs, capacity, and access before running:**

```sh
cd /path/to/microsoft-discovery-gromacs-sample
azd auth login
azd env new discovery-demo
azd env set AZURE_SUBSCRIPTION_ID <authorized-subscription-id>
azd env set AZURE_LOCATION <authorized-discovery-region>

# If using Podman instead of Docker:
export AZD_CONTAINER_RUNTIME=podman

azd provision --environment discovery-demo
azd deploy --all --environment discovery-demo
```

Alternatively, use `azd up` with the example's sequential workflow—do not run both
alternatives unintentionally. Build mode needs a working Docker/Podman runtime.
Authentication uses azd and the selected subscription's tenant; no separate
tenant configuration or Bicep infrastructure is required.

## Configuration

Full validation rules are in the [configuration schema](schemas/discovery.schema.json).
`${VARIABLE}` references use the azd environment before process variables.
Build/package defer missing provisioning outputs; Publish/Deploy require them.

### Application scope

| Top-level `discovery` setting | Requirement |
| --- | --- |
| `schemaVersion` | `"1.0"` |
| `workspaceResourceId` | Workspace ARM ID |
| `projectResourceId` | Project ARM ID within that workspace |
| `nodePoolResourceId` | Required for tools |
| `operationTimeoutSeconds` | Optional; default `1800`, range `1–7200` |

### Tools

| Setting | Default or requirement |
| --- | --- |
| `project` | Required in build mode; use `.` for the root |
| `language` | `docker` in build mode; omit in reference mode |
| `docker.registry` | Explicit public-cloud ACR hostname, not an HTTPS URL |
| `docker.path` / `docker.context` | `./Dockerfile` / `.`, relative to the service project |
| `docker.target` / `docker.platform` | Optional stage / core default `linux/amd64` |
| `config.type` | `container` |
| `config.definition` | `tool.yaml`, relative to the service project |
| `config.image.mode` | `build` or `reference`; default `build` |
| `config.image.infraNode` | Inferred for exactly one eligible named image entry; otherwise required |
| `config.image.reference` | Required in reference mode; OCI SHA-256 digest without a tag |

`infraNode` selects the tool definition's `infra[].image.acr` entry—not an Azure
node pool. Build mode publishes and verifies an immutable digest before
registration. Reference mode preserves the supplied digest without build/push
or registry lookup. Omit `language` and `docker` in reference mode:

```yaml
config:
  definition: ./tool.cpu.yaml
  image:
    mode: reference
    infraNode: worker
    reference: ${DISCOVERY_TOOL_IMAGE_DIGEST}
```

There is no assumed Discovery registry. If `AZURE_CONTAINER_REGISTRY_ENDPOINT`
is set, it must agree with `docker.registry` because core azd prioritizes it.

### Agents

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

- The definition defaults to `agent.yaml`, relative to the service project.
- Supply a **deployment name**, not a model ARM ID or catalog model name.
  Without `config.model.deploymentName`, use a literal manifest `model.id`.
- Bindings replace whole-field tool placeholders; declare their services in `uses`.
  Resolution occurs during Deploy, not Package.
- Source definitions, prompts, and runtime placeholders remain unchanged.
- All resolved model/tool references are checked remotely. Missing or mismatched
  managed fields block deployment; an omitted optional description does not.

## Commands and recovery

| Command | Behavior |
| --- | --- |
| `azd discovery plan` / `doctor` | Local configuration checks, not Azure readiness |
| `azd package simulation` | Local build/package; no push or Azure deployment |
| `azd deploy --all` | Build/package/publish and deploy in dependency order |
| `azd deploy researcher` | Requires verified deployed dependencies; does not rebuild them |
| `azd publish simulation` | Push to ACR only; still a cloud mutation |
| `azd down` | IaC teardown; no custom application-deletion hook |

Packaging may download dependencies. A separate package command can cause
another build on a later deploy because caches are process-local.

Verified tool outputs are `SERVICE_<NAME>_RESOURCE_ID` and `_IMAGE`; agent outputs
are `SERVICE_<NAME>_NAME` and `_VERSION`. Recovery records live under ignored
`.azure/<environment>/discovery/`. Reruns restore failed exports and allow
correction of definitive failures. Ambiguous submissions remain blocked to
avoid duplicate immutable versions.

**Diagnostic caveat:** azd 1.35.0 can repair environment metadata before extension
startup and append text after failed JSON output. For strict local read-only
diagnostics, see [standalone checks](CONTRIBUTING.md#standalone-diagnostics).

## Scope and limitations

- Container tools and prompt agents only. KBs, indexing, uploads, MCP/OpenAPI
  lifecycles, remote builds, `imagePassthrough`, and `publish --to` are unsupported.
- Build publication supports public-cloud ACR and single-image manifests, not
  multi-platform indexes. Immutable reference indexes are accepted.
- Linux CI and local tests cover azd 1.35.0 and Podman 4.9.3. A working live
  deployment has been reported, but this is not comprehensive cloud, GPU,
  platform, permission, or recovery certification.
- Discovery adapters use tool ARM API `2026-06-01` and agent workspace API
  `2026-02-01-preview`; the latter is an API version, not the extension's release status.

For local builds, tests, release automation, and development artifacts, see
[CONTRIBUTING.md](CONTRIBUTING.md). Changes are recorded in [CHANGELOG.md](CHANGELOG.md).

## Roadmap

| Next step | Status or scope |
| --- | --- |
| Linux CI and automatic Releases | Working; versions, annotated tags, notes, bundles, and checksums are automated |
| Broader live validation | Reference mode, Podman publication, redeployment, permissions, and recovery |
| Newer azd compatibility | Test alongside the pinned minimum |
| KB support | Resolve storage sources, GA attachment format, and optional identity behavior before implementation |
| Later features | Explicit/automatic indexing, MCP/OpenAPI tools, uploads, remote diagnostics, GPU certification |

KB infrastructure remains Terraform-owned. Future indexing is planned with an
explicit command and `autoindex: true` by default; none of this is implemented
yet. Cross-platform checks remain deferred; CI stays Linux-only.
