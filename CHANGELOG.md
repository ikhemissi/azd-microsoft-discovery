# Changelog

## 0.1.0-preview.1 (unreleased)

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
