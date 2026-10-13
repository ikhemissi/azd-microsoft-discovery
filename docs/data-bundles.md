# Reusable tools and explicit input bundles

**Data commands are unreleased.** Install a [development bundle](../CONTRIBUTING.md#build-and-install-a-development-bundle);
published version 0.3.0 includes KB support, but not these commands.

## Supported behavior and boundaries

The existing `discovery.tool` provider already preserves tool-definition fields,
including code-environment descriptions and `output_mount_configurations`. It
changes only the selected image reference. Agent instructions remain unchanged.
Runtime inputs therefore need neither a new service host nor Docker-style
`volumes` in `azure.yaml`.

### Current approach: follow the official examples

Use an existing code environment, action-based tool, or hybrid without requiring
a new runner or execution gateway. The initial workflow is:

1. Deploy the tool and its agent once.
2. Prepare experiment files, including the tool's MDPs/settings where applicable.
3. Make the files available as a Discovery asset and attach it to the intended
   conversation/task through a supported platform workflow.
4. Select that attached experiment in the request to the agent.
5. Let the agent resolve its resource URI and supply the runtime mounts when
   calling the tool. Inspect the actual call and captured outputs.

This is agent-mediated selection, not deterministic input-binding enforcement.
Instructions guide calls; they do not enforce them. Keep existing tool-specific
validation and execution approval, but defer additional binding restrictions,
mandatory typed actions, or runner redesign until real experiments show the need.
Upload checksums protect transfer integrity; chemists need not manually supply a
bundle fingerprint to the agent.

The extension prepares/validates generic checksummed bundles and explicitly
uploads them to existing Azure Blob containers. It verifies the selected
subscription, Discovery project association, backing storage account, endpoint
and uploaded bytes. A dataset can also reference an existing IaC-owned directory
asset: its parent and path must exactly match the upload destination. It does
not provision storage, create Discovery assets, attach resources to conversations,
index inputs, invoke agents, simulate, or scale compute.

**Blob upload is not Discovery resource attachment.** Successful upload returns
an unsigned Blob prefix locator with `blobVerified: true`,
`conversationAttached: false`, and `discoveryAttachment: required`. If an asset
is configured, successful verification also returns its ARM ID and directory
`resourceUri` using the documented Discovery addressing format. This verifies
the resource locator, not membership in any conversation's data context.

Reviewed references:

- [Discovery runtime mounts/resource tools](https://learn.microsoft.com/azure/microsoft-discovery/how-to-data-handling-with-tools-agents).
- [Storage assets and per-conversation data context](https://learn.microsoft.com/azure/microsoft-discovery/concept-files-storage-assets).
- [Azure Blob Storage requirements](https://learn.microsoft.com/azure/microsoft-discovery/concept-storage-account).
- [Storage-container/asset addressing](https://learn.microsoft.com/azure/microsoft-discovery/concept-storage-containers-assets).
- [Official Blob asset path model](https://github.com/microsoft/discovery/blob/10a7418ee2813767a9fce73dfc72002c7380d8a5/utilities/supercomputer-cli/discovery/src/discovery/poll/models/dataasset.py):
  first path component is the physical Blob container; subsequent components
  identify the path within it.
- [Upstream GROMACS](https://github.com/microsoft/discovery/tree/10a7418ee2813767a9fce73dfc72002c7380d8a5/agents/gromacs),
  revision `10a7418ee2813767a9fce73dfc72002c7380d8a5`: agent-generated
  Python, runtime input files, and an attach-asset-and-prompt workflow.
- [GA management contracts](https://github.com/Azure/azure-rest-api-specs/tree/2f40414843c3098af00f7a2773c139d67363ce75/specification/discovery/resource-manager/Microsoft.Discovery/stable/2026-06-01),
  revision `2f40414843c3098af00f7a2773c139d67363ce75`.

The upstream pattern combines mounted scientific files with agent-generated
calls to packaged helpers; it is not a generic agent-YAML parameters object.
Do not copy its disabled confirmation, warning allowances, adaptive retries,
inserted recovery stages, or heuristic scientific acceptance criteria.

## Configuration

Add `data` beneath the existing `discovery` settings. The
[complete configuration](../examples/reusable-data/azure.yaml) includes an agent,
its tool binding, and two bundles targeting one immutable tool image. Supply your
existing `tool.cpu.yaml` and `agent.yaml` under `experiments/lysozyme-water`,
or adjust the service paths to your application's definitions. Its text payloads
are validation fixtures, not runnable scientific protocols; replace them with
reviewed experiment bundles and their manifests before scientific use.

The agent's `uses`/`bindings` connect it to the deployed tool, and
`config.knowledgeBases` attaches its indexed literature service. Neither binds
an experiment dataset to a conversation. Dataset declarations describe local
validation/upload. At runtime the user selects an attached asset through the
conversation, as in the official GROMACS sample; there is no new agent-YAML
dataset or mount field. Literature assets and experiment-input assets remain
separate; uploading simulation inputs does not index them.

```yaml
data:
  sources:
    experiment-inputs:
      storageContainerResourceId: ${DISCOVERY_STORAGE_CONTAINER_ID}
      blobContainerResourceId: ${DISCOVERY_INPUT_BLOB_CONTAINER_ID}
      prefix: approved-experiments
  datasets:
    experiment-a:
      source: experiment-inputs
      directory: ./bundles/experiment-a
      manifest: bundle.yaml
      namespace: reusable-tool
      version: v1
      overwrite: false
      storageAssetResourceId: ${DISCOVERY_EXPERIMENT_A_ASSET_ID}
    experiment-b:
      source: experiment-inputs
      directory: ./bundles/experiment-b
      namespace: reusable-tool
      version: v2
      storageAssetResourceId: ${DISCOVERY_EXPERIMENT_B_ASSET_ID}
```

| Field | Meaning |
| --- | --- |
| `storageContainerResourceId` | Existing `Microsoft.Discovery/storageContainers` registration, associated with the configured project |
| `blobContainerResourceId` | Existing `Microsoft.Storage/storageAccounts/blobServices/containers` ARM ID, under the registered account's `default` Blob service |
| `prefix` | Extension-owned, explicitly authorized Blob prefix, not a native Discovery asset property |
| `directory` | Local bundle directory within the azd project |
| `manifest` | Relative manifest path; defaults to `bundle.yaml` |
| `namespace` / `version` | Immutable destination suffix |
| `overwrite` | Defaults to `false`; `true` is unsupported |
| `storageAssetResourceId` | Optional existing `Microsoft.Discovery/storageContainers/storageAssets` directory asset; required for `data show` |

The destination is `<prefix>/<namespace>/<version>/` within the explicitly
declared Blob container. Its endpoint comes from verified storage-account
readback; no account keys, SAS URLs, endpoint overrides or credentials belong
in configuration. An existing Discovery asset is a separate identity; neither
the Blob container ID nor this prefix substitutes for a storage-asset ID.
Its path must be exactly `<blob-container>/<prefix>/<namespace>/<version>/`,
including the trailing directory slash. The extension reads this asset but
never creates or updates it.

For example, Terraform can own an asset for A with:

```hcl
resource "azapi_resource" "experiment_a" {
  type      = "Microsoft.Discovery/storageContainers/storageAssets@2026-06-01"
  name      = "experiment-a-v1"
  parent_id = azapi_resource.discovery_storage.id
  location  = var.location
  body = {
    properties = {
      description = "Reviewed experiment A input directory"
      path        = "inputs/approved-experiments/reusable-tool/v1/"
    }
  }
}

output "DISCOVERY_EXPERIMENT_A_ASSET_ID" {
  value = azapi_resource.experiment_a.id
}
```

Here `inputs` must be the name of the actual input Blob container, not the
Discovery registration name. Change the path to match your declared destination;
B needs its own version/asset. Bicep can own the equivalent ARM resource.

Environment substitution follows existing azd-before-process precedence.
Deployment validates declarations but does not read/upload bundle files, so
directories can be prepared after provisioning and tool deployment. Existing
tool/model/agent authoring remains compatible; there are no automatic data hooks.
Public-cloud Blob storage in the selected subscription is supported. Other
backends, cross-subscription destinations and speculative URI mappings fail
clearly rather than falling back.

## Manifest and local validation

Generate a manifest from the files already prepared by the experiment owner:

```sh
azd discovery data prepare experiment-a --local --output json
```

This local-only operation computes file sizes and SHA-256 hashes automatically
and creates `bundle.yaml` (JSON syntax is valid YAML). It refuses to overwrite
an existing manifest; use `validate` for bundles that already include one.
It does not generate MDPs, scientific parameters or runner checksums such as
`SHA256SUMS`; those remain tool/sample-owned. It never executes payloads.

The manifest is data, not executable instructions:

```yaml
schemaVersion: "1.0"
files:
  - path: input.txt
    size: 22
    sha256: c1ac6b5a0339732150ec5782ca60ace7159f9b8e0dae7ee113b0caa061241882
```

Every listed file is required. Each size is its byte count, and checksums are
lowercase SHA-256. The manifest itself is uploaded and included in the content
fingerprint; do not list it recursively. Reject unknown fields/versions, YAML
aliases/anchors, traversal, absolute paths, symlinks, duplicate/case-colliding
paths, file/directory collisions, undeclared files and special files.

Limits: manifest 1 MiB; 1,000 declared files; 1 GiB per declared file; 2 GiB
total including the manifest. Paths use alphanumeric components with `.`, `_`,
and `-`, separated by `/`; no spaces, dot-prefixed components, unsafe segments
or reserved `.azd-*` paths. Combined Blob names must fit 1,024 characters and
254 path segments.

```sh
azd discovery data validate experiment-a --local --output json
azd discovery data validate experiment-b --environment demo --output json
```

`--local` uses only local configuration/process variables, not azd or Azure.
Default validation can read azd environment values but never obtains Azure
credentials or executes a domain validator. Run the sample-owned scientific
validator separately. File integrity is not scientific validation or approval.

## Explicit upload

After reviewing destination, RBAC, network access and costs:

```sh
azd discovery data upload experiment-a --environment demo --dry-run --output json
# Explicitly authorize Blob writes only:
azd discovery data upload experiment-a --environment demo --confirm --output json
```

`--confirm` is required for writes, including noninteractive use. Dry-run reads
ARM/Blob prerequisites and inspects prefix ownership, but does not upload,
create assets, export environment values or write recovery records. Read access
does not prove write RBAC; the actual authorized write propagates denied-access
errors. Core azd can maintain its own environment metadata before extension
startup, as documented in [standalone diagnostics](../CONTRIBUTING.md#standalone-diagnostics).
When a dataset declares an asset, upload and dry-run verify its parent/path
before Blob writes. For raw uploads before asset provisioning, omit the optional
asset field; add the IaC output after the asset exists. Missing/denied/mismatched
declared assets are explicit errors, not silent raw-upload fallbacks.

Uploads are sequential, using one 4 MiB SDK transfer buffer. Requests allow
three exponential retries, 1-10 second delays and 120-second per-attempt timeouts.
The existing operation timeout bounds the whole command. The SDK uses azd
credentials and the selected subscription's tenant; no Bicep deployment-context
lookup, role grant or storage/container creation occurs.

Conditional `If-None-Match: *` writes prevent overwrites. Before each new
payload write, a private temporary snapshot is checksummed again and removed
after transfer. Every uploaded/existing payload is downloaded and SHA-256
verified, not accepted by length, ETag or client-supplied metadata alone.

Ignored, bounded recovery records under `.azure/<environment>/discovery/`
bind destination, dataset, environment, fingerprint and upload ownership.
`.azd-upload.json` claims a previously empty immutable prefix.
`.azd-complete.json` is published only after all payloads and their manifest
are verified and undeclared objects are rejected. No partial upload is returned
as verified. Rerun the same confirmed command to resume; changed content needs
a new version. Foreign prefixes or missing ownership records are not adopted.
There is no deletion or forced overwrite command.

The marker is point-in-time verification, not a substitute for storage
immutability or later consumer checksum checks. Terraform/IAM must prevent
other principals from replacing approved data. A corrupted completed upload
fails verification on a rerun rather than being silently repaired.

## Asset handoff and conversation context

After provisioning the asset, inspect its mapping with:

```sh
azd discovery data show experiment-a --agent researcher --environment demo --output json
```

This read-only command verifies the project, registration, storage endpoint and
asset, then returns `storageAssetResourceId`, `resourceUri`, and the configured
agent/tool-service association. It does not need local bundle files or write
recovery state. `assetVerified: true` means ARM identity/path checks passed;
`blobVerified: false` means this command did not checksum Blob payloads.
Upload performs that separate content check.

For the example above with a Discovery registration named `research-data`:

```text
Asset path: inputs/approved-experiments/reusable-tool/v1/
Resource URI: discovery://resources/research-data/paths/inputs/approved-experiments/reusable-tool/v1/
```

This URI is derived from verified ARM readback and the documented addressing
contract; it is not a claim that `GetResourceContext` has returned it in a
conversation. `--agent` selects a configured service for the handoff report:
it does not change prompts, inject mounts, verify agent deployment or invoke it.

Link the correct existing Discovery storage asset to the intended task/session
through the platform's supported workflow. The official storage-asset guide
describes explicit task `storageAssetIds`, parent/dependency inheritance, and
per-conversation isolation. An ARM asset existing in a project is not automatic
attachment. A raw Blob upload is not asset registration.

Programmatic task/conversation attachment remains unimplemented. Use the
supported platform attachment flow with the reported asset ID. Studio
documentation also differs on local-file upload availability: the
Studio overview describes project uploads, while the storage-asset guide lists
local upload as unsupported. Do not rely on either as proof of an automatic
upload-to-conversation path.

For example, after attaching experiment A to the chosen agent's conversation:

> Use the attached experiment A directory with the configured tool. Resolve its
> actual resource URI, mount the directory contents at the tool's expected input
> path, and capture its output directory. State the selected input, requested
> operation and compute requirements before asking for execution approval.

For the sample, those paths are `/input` and `/output`. Use the same agent/tool
for B with its attached asset and a fresh output location; neither image rebuild
nor redeployment is needed. Names in the request refer to attached assets, not
automatically to extension dataset aliases. If the selected resource is missing
or ambiguous, resolve that with the user before execution.

After explicit attachment, call `GetResourceContext` and use its actual URI.
The following is a **mount fragment**, not a fabricated complete invocation API:

```json
{
  "inputMounts": [
    {
      "uri": "discovery://resources/example-container/paths/example-resource/",
      "mountPath": "/input"
    }
  ],
  "outputMounts": ["/output"]
}
```

Replace the illustrative URI with the platform-returned directory URI. Its
trailing `/` mounts contents directly under `/input`. A file URI has no trailing
`/` and mounts directly at the specified file path, for example
`/input/experiment.json`. All mount paths must be absolute. Output mounts capture
directories, not individual files. Supply mounts on **each invocation**; tool
descriptions and agent instructions do not cause mounts on their own.

For small agent-generated text configs, `WriteResource` creates a file in the
conversation context. Retrieve its URI and explicitly mount the file on the
next tool call. Apply the tool's input validation and obtain execution approval.
Do not invent agent-YAML mount fields or treat uploaded file contents as
instructions or authorization.

After verified execution, use `GetResourceContext`, `PreviewResource`,
`UpdateResourceDetails` and `ShareResource` to inspect/describe/share results.
Keep auto-promotion disabled when failed/intermediate outputs require review.

## Exact sample integration changes (not applied)

Reviewed the sample at `42b9155da8cabb25073ca5c7c39da316d7343c53`.
No sample files were changed or sample commands executed.

For literature grounding, declare a `discovery.knowledgebase` service using the
existing papers asset, then add its service name to the agent's `uses` and
`config.knowledgeBases`. The [agent + KB example](../examples/knowledgebase/azure.yaml)
is self-contained for that configuration. Indexing must succeed before agent
attachment. The service-returned KB ID is used unchanged; live retrieval and
GA identifier compatibility remain pending validation due Bookshelf quota.

| Sample file | Required integration |
| --- | --- |
| `infra/storage.tf` | Add/reuse a dedicated existing input Blob container under the registered data account. Do not confuse it with `discoveryoutputs` or Terraform state storage. |
| `infra/outputs.tf` | Export `DISCOVERY_INPUT_BLOB_CONTAINER_ID` from that container's ARM ID; retain `DISCOVERY_STORAGE_CONTAINER_ID`. Create/reuse IaC-owned directory assets with the exact container/prefix/version path and export their IDs as `DISCOVERY_EXPERIMENT_A_ASSET_ID` / `DISCOVERY_EXPERIMENT_B_ASSET_ID`. |
| `infra/identity.tf` | Review uploader read/write access on the input container. Current optional provisioner data role targets the output container only. Platform identities already have account-level roles, but input access/networking still require validation. Roles remain Terraform-owned. |
| `azure.yaml` | Install a development release containing data commands, add the declarations above, and retain existing tool/agent services. Data directories must not become image build inputs or deployment hooks. |
| `tools/gromacs/run.sh` | No redesign required for initial supported baseline experiments: it already reads mounted MDPs/structures. Keep its existing protocol restrictions; generalization to other science is separate sample work, not required by the extension. |
| `tools/gromacs/Dockerfile` and GPU variant | Reuse the existing image and packaged runner for supported experiments. Changing mounted inputs does not require a rebuild; changing packaged runner/software does. Do not change compute mode implicitly. |
| `tools/gromacs/test.sh` | Retain existing checks. Add observations from the two-bundle exercise without putting cloud simulations in default tests. |
| `tools/gromacs/tool.cpu.yaml` and GPU variant | Keep the existing code environment and describe explicit `/input` and `/output` mounts, approval and actual compute mode. Typed actions or hybrids are optional, not an extension requirement. |
| `agents/lysozyme-water-gromacs/agent.yaml` | Select an approved attached bundle through `GetResourceContext`; explicitly pass both mount arrays, use checked calls to the packaged runner, retain confirmation and share only verified outputs. Do not import upstream `gromacs_utils` unless deliberately packaged/reviewed. |
| Sample documentation/presets | Record input hashes, provenance, submitted/effective parameters, image digest, software version, actual compute mode and status. Source inputs remain immutable; use working copies. |

The current runner already reads mounted MDPs/structures and never executes
`/input/run.sh`, but hardcodes OPLS-AA/SPC/E, preparation and stage order. Initial
experiments must stay within that supported scope. Its Python code environment
can execute agent-generated code: instructions are **not a runner-only sandbox**.
Preserve this flexibility and document what happened in each experiment.
Evaluate optional typed actions, stronger runtime bindings and additional
restrictions later; none is a prerequisite for the initial official-style flow.

## Separately authorized two-bundle end-to-end plan

Nothing below has been run against Azure:

1. Authorize Terraform/Bicep changes, identities/networking, input/output storage
   and asset resources separately. Provision assets at the documented Blob paths
   and establish the supported platform attach workflow.
2. Review two inputs within the existing runner's supported scope. Prefer a
   reviewed seed/config difference initially, not an unreviewed chemistry change.
   Keep generated-code execution; no typed-action migration is required.
3. Build/publish/deploy the reusable tool **once**. Record its full ACR SHA-256
   digest and verified Discovery tool identity/version.
4. Prepare bundles A/B and run sample scientific validators separately. Use
   `data prepare` to generate generic manifests if missing, then `data validate`.
   Confirm distinct content fingerprints.
5. Dry-run both uploads. Explicitly authorize each confirmed upload; verify
   completion markers and downloaded file checksums. No tool redeploy occurs.
6. Run `data show A --agent researcher` and the equivalent for B to verify the
   existing asset IDs/URIs. Attach A/B through separately authorized platform
   operations to their intended isolated tasks/conversations.
   Require `GetResourceContext` to return each real directory URI.
7. Obtain explicit user approval for each billable simulation and its compute
   bounds. Ask the **same agent** to use attached A, then B with the **same tool**.
   Inspect the actual input URI and `/input` directory mount and require fresh
   output directories under `/output`. Do not resize or switch pools.
8. Verify checked process success, runner `completed` status, full inputs/effective
   parameters, outputs/checksums, software version and actual CPU/GPU mode.
   Verify the tool still references the original image digest.
9. Inspect/share verified outputs through resource tools. Keep failures and
   inconclusive scientific results; do not infer equilibrium or efficacy from
   a smoke test. Confirm no rebuild/redeploy, input mutation, hidden indexing,
   warning bypass, automatic retries or CPU/GPU fallback occurred.
10. Record selection/mount mistakes, missing files, generated scripts, effective
    settings, user corrections and validation failures across experiments.
    Use that evidence to decide later which verifications/restrictions belong
    in the extension, tool implementation or platform. Successful agent behavior
    is not evidence of deterministic binding enforcement.
