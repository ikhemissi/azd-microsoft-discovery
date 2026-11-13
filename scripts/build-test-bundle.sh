#!/usr/bin/env bash
set -euo pipefail

export AZD_COLLECT_TELEMETRY=no
: "${AZD_CONFIG_DIR:?Use an isolated AZD_CONFIG_DIR for building and testing}"

azd extension install microsoft.azd.extensions --version 0.13.0
azd x build --all
version="$(./bin/ikhemissi-azd-discovery-linux-amd64 version --output json | jq -er '.version')"
azd x pack --bundle --output ./artifacts
bundle="ikhemissi-azd-discovery_${version}.zip"
azd extension install "./artifacts/$bundle" --force
azd discovery version
AZD_DISCOVERY_CONTAINER_TESTS=1 AZD_DISCOVERY_NATIVE_TESTS=1 \
  go test -count=1 -v \
    -run 'Test(NativeContainerPackage|NativeReferencePackage|NativeKnowledgeBasePackage|NativeDataValidation|(Native|Standalone)ReadOnlyCommands)' .
DISCOVERY_RELEASE_BUNDLE="$PWD/artifacts/$bundle" \
  go test -count=1 -v -run '^TestReleaseBundle$' .
# Platform archives are also published on their own so registries can reference them.
mapfile -t platform_archives < <(unzip -Z1 "artifacts/$bundle" 'artifacts/*' | sed 's#^artifacts/##')
unzip -j -o -q "artifacts/$bundle" 'artifacts/*' -d artifacts
(cd artifacts && sha256sum "$bundle" "${platform_archives[@]}" > SHA256SUMS && sha256sum --check SHA256SUMS)
