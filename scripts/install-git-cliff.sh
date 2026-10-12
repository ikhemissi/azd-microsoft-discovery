#!/usr/bin/env bash
set -euo pipefail

destination="${1:?Supply an installation directory}"
version=2.14.2
archive="git-cliff-${version}-x86_64-unknown-linux-musl.tar.gz"
mkdir -p "$destination"
url="https://github.com/orhun/git-cliff/releases/download/v${version}/${archive}"
curl --fail --location --retry 3 "$url" --output "$destination/$archive"
curl --fail --location --retry 3 "$url.sha512" --output "$destination/$archive.sha512"
(cd "$destination" && sha512sum --check "$archive.sha512")
tar -xzf "$destination/$archive" -C "$destination" "git-cliff-${version}/git-cliff"
install "$destination/git-cliff-${version}/git-cliff" "$destination/git-cliff"
"$destination/git-cliff" --version
if [[ -n "${GITHUB_PATH:-}" ]]; then
  echo "$destination" >> "$GITHUB_PATH"
fi
