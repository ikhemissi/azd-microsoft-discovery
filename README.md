# Microsoft Discovery extension for azd

[![CI](https://github.com/ikhemissi/azd-microsoft-discovery/actions/workflows/ci.yml/badge.svg)](https://github.com/ikhemissi/azd-microsoft-discovery/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ikhemissi/azd-microsoft-discovery)](https://github.com/ikhemissi/azd-microsoft-discovery/releases)
[![azd](https://img.shields.io/badge/azd-%E2%89%A5%201.35.0-0078D4)](https://learn.microsoft.com/azure/developer/azure-developer-cli/)
[![Platform](https://img.shields.io/badge/platform-linux%20%7C%20macOS%20%7C%20windows-lightgrey)](#install)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Deploy Microsoft Discovery tools, prompt agents and Bookshelf knowledge bases
from one `azure.yaml`, with the same `azd provision` / `azd deploy` workflow you
use for the rest of Azure.

> **Community-maintained, not officially supported by Microsoft.** Versions are
> `0.x`; configuration and behavior may change between releases.

| Service host | What it does |
| --- | --- |
| `discovery.tool` | Builds and publishes a container image (or references an immutable one), then registers the tool with its verified digest |
| `discovery.agent` | Resolves tool bindings and knowledge-base attachments, then deploys the prompt agent |
| `discovery.knowledgebase` | Creates or updates a knowledge base on an existing Bookshelf and indexes it |

**Why use it**

- **One declarative project.** Tools, agents, knowledge bases and their wiring
  live in `azure.yaml`; `azd deploy --all` deploys them in dependency order.
- **Clear ownership.** Terraform or Bicep owns infrastructure (workspace,
  project, node pools, Bookshelf, storage, identities, roles, models). The
  extension deploys what runs on top and never grants roles, scales compute,
  runs simulations or invokes agents.
- **Verified and resumable.** Images are pinned by digest, deployments are read
  back and checked, and an interrupted command resumes without duplicate agent
  versions or indexing runs.
- **Reusable experiment inputs.** Versioned, checksummed input bundles are
  uploaded explicitly, so one tool and agent can run many experiments without
  rebuilding images.

## Install

Requires **azd ≥ 1.35.0**. Releases include Linux, macOS and Windows binaries
for amd64 and arm64; CI tests Linux amd64. No Go or repository clone needed.

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
  includes the bundle, the per-platform archives and `SHA256SUMS`.

## Quick start

The [GROMACS sample](https://github.com/ikhemissi/microsoft-discovery-gromacs-sample)
is a complete project: Terraform infrastructure, a GROMACS tool, an agent and a
literature knowledge base. Ready-to-adapt configurations:

| Example | Shows |
| --- | --- |
| [gromacs-build](examples/gromacs-build/azure.yaml) | Minimal tool + agent, image built from source |
| [knowledgebase](examples/knowledgebase/azure.yaml) | Knowledge base attached to an agent ([agent definition](examples/knowledgebase/agent.yaml)) |
| [reusable-data](examples/reusable-data/azure.yaml) | Tool, agent, knowledge base and two versioned input bundles |

Review Azure costs, quota and access first, then:

```sh
azd auth login
azd env new dev
azd env set AZURE_SUBSCRIPTION_ID <subscription-id>
azd env set AZURE_LOCATION <discovery-region>
export AZD_CONTAINER_RUNTIME=podman   # only if using Podman instead of Docker

azd provision
azd discovery plan    # optional: validate the configuration locally
azd deploy --all
```

Authentication uses azd and the selected subscription's tenant.

## Configuration

`${VARIABLE}` values come from the azd environment first, then the process
environment. The [schema](schemas/discovery.schema.json) has the full rules.

### Project settings

| `discovery` setting | Value |
| --- | --- |
| `schemaVersion` | `"1.0"` |
| `workspaceResourceId` | Workspace ARM ID |
| `projectResourceId` | Project ARM ID in that workspace |
| `nodePoolResourceId` | Default node pool (required for tools) |
| `operationTimeoutSeconds` | Optional, default `1800` (1–7200) |
| `data` | Optional [input bundles](#experiment-inputs) |

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
  The rest of the definition (code environments, actions, mounts) is kept as is.
- `infraNode` names the tool definition's `infra[]` entry whose image is set;
  it can be omitted when there is exactly one.
- In reference mode, omit `language` and `docker`; `reference` must be a
  SHA-256 digest without a tag.

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

- Knowledge bases are Bookshelf data-plane objects, not ARM resources, so the
  extension manages them while IaC owns the Bookshelf and storage assets.
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

### Agents

```yaml
researcher:
  host: discovery.agent
  project: ./experiments/lysozyme-water
  uses: [simulation, literature]
  config:
    model:
      deploymentName: ${DISCOVERY_CHAT_MODEL_DEPLOYMENT_NAME}
    bindings:
      gromacsToolId:
        service: simulation
        output: resourceId
    knowledgeBases:
      - service: literature
```

- `definition` defaults to `agent.yaml`, relative to the service `project`.
- `model.deploymentName` is an existing Discovery chat model deployment name.
- `bindings` fill tool placeholders with outputs of services listed in `uses`.
- `knowledgeBases` attach up to 32 knowledge-base services, also listed in
  `uses`. Each must be indexed first. Agents reference a knowledge base as
  `/bookshelves/<bookshelf>/knowledgeBases/<name>`, as Discovery Studio does;
  removing all entries detaches them.
- Model, tools and attachments are verified remotely after deployment.

## Experiment inputs

`discovery.data` declares versioned input bundles, such as a GROMACS
experiment's `.mdp` and structure files. Each bundle is uploaded to an existing
Blob container behind an IaC-owned storage asset, so the same tool and agent run
new experiments without an image rebuild or redeployment:

```sh
azd discovery data prepare experiment-a                    # write checksum manifest
azd discovery data upload experiment-a --dry-run           # check the destination
azd discovery data upload experiment-a --confirm           # upload, never overwrite
azd discovery data show experiment-a --agent researcher    # asset ID and resource URI
```

Then, as in the official Discovery samples, attach the storage asset to a
conversation in Discovery and ask the agent to use it: the agent resolves the resource and
mounts it as the tool's input. Configuration, manifests and the full workflow
are in [docs/data-bundles.md](docs/data-bundles.md).

## Commands

| Command | Purpose |
| --- | --- |
| `azd deploy --all` | Build, publish and deploy every service in dependency order |
| `azd deploy <service>` | Deploy one service; its dependencies must already be deployed |
| `azd package` / `azd publish <tool>` | Build locally / push the image to ACR only |
| `azd discovery index <kb>` | Index a deployed knowledge base and export the run |
| `azd discovery data prepare\|validate <dataset> [--local]` | Write / check a bundle's checksum manifest |
| `azd discovery data upload <dataset> --dry-run\|--confirm` | Preview or perform a no-overwrite, checksum-verified upload |
| `azd discovery data show <dataset> [--agent <service>]` | Verify the storage asset mapping and print its Discovery resource URI |
| `azd discovery plan` / `doctor` `[--local]` | Validate configuration locally, without Azure calls |
| `azd discovery version` | Show the extension version |

Add `--output json` for machine-readable reports.

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

- Supports container tools, prompt agents and GA knowledge bases.
- Image builds are local and single-platform; publication targets public-cloud ACR.
- Input assets are attached to conversations in Discovery, not by the extension.
- `plan` and `doctor` validate local configuration only.
- Uses Discovery API version `2026-06-01`, the same as Discovery Studio.
- Tested in Linux CI with azd 1.35.0 and Podman 4.9.3, and live on a single
  Discovery deployment. Not certified for every region, GPU or permission setup.

## Roadmap

Not implemented yet; candidates for future releases:

- MCP and OpenAPI tools.
- Remote and multi-platform image builds.
- Azure readiness checks in `doctor` (authentication, RBAC, registry, networking).
- Programmatic attachment of input assets to conversations, and stronger
  runtime input bindings, guided by results from real experiments.
- Testing on platforms beyond Linux amd64.

Development, testing and releases: [CONTRIBUTING.md](CONTRIBUTING.md).
Changes: [CHANGELOG.md](CHANGELOG.md).
