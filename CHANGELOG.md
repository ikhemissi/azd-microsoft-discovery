# Changelog

## Unreleased

- Add `config.knowledgeBases` for agent attachment to declared/indexed KB services.
  Verify dependency state, source configuration and successful indexing with
  subscription-scoped credentials, then use the service-returned KB ID unchanged.
- Adapt native agent `knowledgeBases`, verify readback, explicitly clear removed
  attachments and preserve recovery without duplicate agent versions. Keep
  unchanged configuration-only KB deployments' verified indexing provenance.
  Offline tests pass; live GA attachment/retrieval remains pending Bookshelf quota.
- Add optional domain-neutral `discovery.data` sources/datasets and strict
  versioned file/checksum manifests without changing tool/agent authoring.
- Add local bundle validation and explicit, no-overwrite Blob uploads with
  no-write dry-runs, tenant-aware azd authentication, project/storage scope
  verification, full content readback, completion markers and resumable ownership.
- Preserve/test runtime-mount descriptions and native output settings. Document
  verified mount contracts, separate Discovery attachment/registration gates,
  read-only sample integration guidance and an authorized two-bundle test plan.
- Follow the official attached-asset/agent-mediated runtime workflow, preserving
  code-environment flexibility; defer deterministic binding and additional
  execution restrictions pending experience from authorized experiments.
- Add local `data prepare` checksum generation and read-only `data show`
  handoff reports. Verify per-dataset IaC-owned storage assets against their
  exact Blob directory paths and include asset IDs/Discovery URIs in upload
  results without creating assets or attaching/invoking conversations.

## 0.3.0 (2026-10-12)

### Features

- add GA knowledge-base lifecycle and indexing (7ff2bbe)



## 0.2.0 (2026-10-12)

### Features

- automate releases with git-cliff (ff049b0)



## 0.1.0 (2026-10-12)

- Add Linux-only GitHub Release publishing on matching version tags. Reuse the
  CI-tested bundle, validate version/platform/artifact checksums, publish bundle
  and SHA256SUMS.
- Upload tested extension bundles as development Actions artifacts; document
  release URL installation without local build tooling.
- Verify optional agent descriptions at the Foundry latest-version readback
  location; accept omitted descriptions without creating duplicate immutable
  versions, while keeping strict managed-field checks and field-specific errors.
- Resolve the azd credential tenant through the effective environment's selected
  subscription and public account API. Pass an explicit validated tenant for
  Discovery and ACR verification, avoiding Bicep deployment-context lookup in
  Terraform projects.
- Recover from definitive rejected submissions and terminal failed operations
  while retaining ambiguous-submission protection; repair failed app resources.
- Verify literal tool references and reject duplicate managed targets and
  unsupported/deferred agent fields at preflight.
- Publish SDK-cache-compatible `$defs` schemas and test metadata roundtrips,
  provider registration, SDK environment refresh, and documentation examples.
- Add Linux-only lint, coverage, modernization, fresh-bundle and native
  minimum-host CI gates; bound host exceptions and always clean fixture images.
- Add sanitized API diagnostic identifiers and lifecycle progress reporting.
- Resolve and verify agent models directly by `config.model.deploymentName`;
  defer provisioning outputs during Package, preserve source manifests, and
  verify literal deployment names when configured in the manifest.
- Add a container tool-type default distinct from build language; reject
  unimplemented MCP/OpenAPI modes and defer knowledge-base support.
- Default tool/agent definition paths, build image mode, and empty agent bindings;
  infer a single eligible tool infra image entry and reject ambiguity.
- Require explicit build publication registry and reject conflicting core azd
  registry overrides; simplify the Terraform-first workflow to avoid double builds.
- Scaffold the community extension using the public azd SDK v1.35.0.
- Add read-only plan/doctor commands with local-only readiness reporting.
- Add typed configuration, definition/path/YAML validation, binding declaration
  validation, immutable image-reference checks, and configuration metadata.
- Add offline tests and a current Terraform-first container/agent example.
- Verify compatibility with the real demo's CPU tool/agent definitions and add
  opt-in native/standalone smoke tests.
- Record azd 1.35.0 host-side environment normalization and failed-JSON output
  limitations; strict read-only automation currently uses standalone local mode.
- Add native tool/agent targets using public azd container RPCs, immutable
  ACR manifest verification, bounded Discovery polling/readback, scoped recovery
  records, verified output exports, and Deploy-time dependency binding resolution.
- Reuse framework package artifacts instead of rebuilding; preserve reference
  mode without build/push, and reject unsupported `publish --to` overrides.
- Verify local packaging with azd 1.35.0 and Podman 4.9.3; add focused offline
  lifecycle, registry, readiness, state, and reconciliation tests.
- Live ACR/Discovery certification, bookshelf/indexing contracts, and public
  release publication remain pending explicit authorization and independent evidence.
