# Microsoft Discovery extension for azd

Deploy Microsoft Discovery tools, prompt agents and Bookshelf knowledge bases
from `azure.yaml` with the Azure Developer CLI.

> **Community-maintained, not officially supported by Microsoft.** Versions are
> `0.x`; configuration and behavior may change between releases.

| Service host | What it does |
| --- | --- |
| `discovery.tool` | Builds and publishes a container image (or references an immutable one), then registers the tool |
| `discovery.agent` | Resolves tool bindings and knowledge-base attachments, then deploys the prompt agent |
| `discovery.knowledgebase` | Creates or updates a knowledge base on an existing Bookshelf and indexes it |

Terraform (or other IaC) owns infrastructure: workspace, project, node pools,
Bookshelf, storage, identities, roles and model deployments. The extension
deploys what runs on top of it. It never grants roles, scales compute, runs
simulations or invokes agents.

## Install

Requires **azd ≥ 1.35.0** on **Linux amd64**. No Go or repository clone needed.

```sh
VERSION=0.4.0
azd extension install \
  "https://github.com/ikhemissi/azd-microsoft-discovery/releases/download/v${VERSION}/ikhemissi-azd-discovery_${VERSION}.zip"
azd discovery version
```

- **Update:** install a newer release's bundle URL (bundles don't support
  `azd extension update`). Add `--force` to replace an installed build.
- Use the same `AZD_CONFIG_DIR` for installation and your project commands.
- Each [release](https://github.com/ikhemissi/azd-microsoft-discovery/releases)
  includes `SHA256SUMS`.

## Quick start

The [GROMACS sample](https://github.com/ikhemissi/microsoft-discovery-gromacs-sample)
is a complete project using this extension: Terraform infrastructure, a
GROMACS tool, an agent and a literature knowledge base. Its `azure.yaml` is the
best starting point; [examples/gromacs-build](examples/gromacs-build/azure.yaml)
shows the minimal tool + agent configuration.

Review Azure costs, quota and access first, then:

```sh
azd auth login
azd env new dev
azd env set AZURE_SUBSCRIPTION_ID <subscription-id>
azd env set AZURE_LOCATION <discovery-region>
export AZD_CONTAINER_RUNTIME=podman   # only if using Podman instead of Docker

azd provision
azd deploy --all
```

Authentication uses azd and the selected subscription's tenant.

## Configuration

`${VARIABLE}` values come from the azd environment first, then the process
environment. The full rules are in the [schema](schemas/discovery.schema.json).

### Project settings

| `discovery` setting | Value |
| --- | --- |
| `schemaVersion` | `"1.0"` |
| `workspaceResourceId` | Workspace ARM ID |
| `projectResourceId` | Project ARM ID in that workspace |
| `nodePoolResourceId` | Default node pool (required for tools) |
| `operationTimeoutSeconds` | Optional, default `1800` (1–7200) |

### Tools

Build mode (default) builds the image with `language: docker` and the
`docker` settings (`registry` is an ACR hostname), publishes it, and registers
the tool with the verified digest. Reference mode registers an existing image
digest without building:

```yaml
config:
  definition: ./tool.cpu.yaml
  image:
    mode: reference
    infraNode: worker
    reference: ${DISCOVERY_TOOL_IMAGE_DIGEST}
```

- `definition` defaults to `tool.yaml`, relative to the service `project`.
- `infraNode` names the tool definition's `infra[]` entry whose image is set;
  it can be omitted when there is exactly one.
- In reference mode, omit `language` and `docker`; `reference` must be a
  SHA-256 digest without a tag.

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

- `definition` defaults to `agent.yaml`, relative to the service `project`.
- `model.deploymentName` is an existing Discovery chat model deployment name.
- `bindings` fill tool placeholders with outputs of services listed in `uses`.
- To attach knowledge bases, add `config.knowledgeBases: [{service: literature}]`
  and list `literature` in `uses`. The knowledge base must be indexed first.
  See the [agent + knowledge base example](examples/knowledgebase/azure.yaml).
- Model, tools and attachments are verified remotely after deployment.

### Knowledge bases

```yaml
literature:
  host: discovery.knowledgebase
  config:
    name: research-papers
    bookshelfName: ${DISCOVERY_BOOKSHELF_NAME}
    sources:
      - storageAssetResourceId: ${DISCOVERY_PAPERS_ASSET_ID}
        userAssignedIdentityResourceId: ${DISCOVERY_MANAGED_IDENTITY_ID}
    autoindex: true
```

- `sources` are existing Discovery storage assets. Each needs a user-assigned
  identity that is a Bookshelf workload identity with read access to the data.
- `name` defaults to the service name (1–24 letters, digits or hyphens).
  `description` and `copilotInstruction` are optional and shown to agents.
- `autoindex: true` (default) indexes on every deploy. With `false`, deploy
  only updates the definition; run `azd discovery index literature` to index.
- Indexing uses `config.nodePoolResourceId`, else `discovery.nodePoolResourceId`.
  Use a memory-optimized pool; indexing incurs compute costs.
- The Bookshelf is looked up in `AZURE_SUBSCRIPTION_ID` / `AZURE_RESOURCE_GROUP`
  unless `config.subscriptionId` / `resourceGroup` are set.

### Input data

`discovery.data` declares versioned input bundles (for example experiment
inputs or papers) that are checksummed locally and uploaded to an existing
Blob container behind an existing storage asset. Uploads never overwrite,
create assets or run uploaded code. See [docs/data-bundles.md](docs/data-bundles.md)
and the [two-bundle example](examples/reusable-data/azure.yaml).

## Commands

| Command | Purpose |
| --- | --- |
| `azd deploy --all` | Build, publish and deploy every service in dependency order |
| `azd deploy <service>` | Deploy one service; its dependencies must already be deployed |
| `azd package` / `azd publish <tool>` | Build locally / push the image to ACR only |
| `azd discovery index <kb>` | Index a deployed knowledge base and export the run |
| `azd discovery data prepare\|validate <dataset> --local` | Generate / check a bundle manifest and checksums |
| `azd discovery data upload <dataset> --dry-run\|--confirm` | Preview or perform a no-overwrite upload |
| `azd discovery data show <dataset> [--agent <service>]` | Verify the uploaded asset and print its Discovery URI |
| `azd discovery plan` / `doctor` / `version` | Local configuration checks and version |

## Outputs and recovery

Each deploy exports values to the azd environment:

| Service | Outputs (`SERVICE_<NAME>_...`) |
| --- | --- |
| Tool | `RESOURCE_ID`, `IMAGE` |
| Agent | `NAME`, `VERSION` |
| Knowledge base | `NAME`, `KNOWLEDGE_BASE_ID`, `BOOKSHELF_RESOURCE_ID`, `INDEXING_STATUS`, `RUN_ID` |

Progress is recorded under `.azure/<environment>/discovery/` (git-ignored).
If a command is interrupted or fails, **rerun the same command**: it resumes the
pending operation and restores exports without creating duplicate agent
versions or indexing runs. Don't delete these records to force a retry.

## Limitations

- Supports container tools, prompt agents and GA knowledge bases. MCP/OpenAPI
  tools, remote builds and multi-platform image builds are not supported.
- Image publication targets public-cloud ACR.
- Uses Discovery API version `2026-06-01`, the same as Discovery Studio.
- Tested in Linux CI with azd 1.35.0 and Podman 4.9.3, and live on a single
  Discovery deployment. Not certified for every region, GPU or permission setup.

Development, testing and releases: [CONTRIBUTING.md](CONTRIBUTING.md).
Changes: [CHANGELOG.md](CHANGELOG.md).
