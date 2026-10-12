#!/usr/bin/env bash
set -euo pipefail

version="${1:?Supply the computed normal version}"
go run ./scripts/release-version "$version"
gofmt -w internal/cmd/version.go
bash scripts/check-go.sh
GITHUB_REF_TYPE=tag GITHUB_REF_NAME="v$version" \
  go test -count=1 -v -run '^Test(ManifestVersionMatchesBinaryDefault|ReleaseVersionMatchesTag)$' .
bash scripts/build-test-bundle.sh
mapfile -t bundles < <(find artifacts -maxdepth 1 -type f -name '*.zip' -printf '%f\n')
test "${#bundles[@]}" -eq 1
test "${bundles[0]}" = "ikhemissi-azd-discovery_${version}.zip"
