#!/usr/bin/env bash
set -euo pipefail

dry_run=false
if [[ "${1:-}" == --dry-run ]]; then
  dry_run=true
elif [[ "$#" -ne 0 ]]; then
  echo "Usage: bash scripts/release.sh [--dry-run]" >&2
  exit 1
fi
test "$(git branch --show-current)" = main
test -z "$(git status --porcelain)"
git fetch origin main --tags
source_sha="$(git rev-parse HEAD)"
if [[ "$source_sha" != "$(git rev-parse origin/main)" ]]; then
  echo "Release checkout is stale; rerun from the latest main commit." >&2
  exit 1
fi
previous="$(git describe --tags --match 'v[0-9]*' --exclude '*-*' --exclude '*+*' --abbrev=0)"
tag="$(git-cliff --unreleased --bumped-version)"
version="${tag#v}"
resume=false
if [[ "$tag" == "$previous" ]]; then
  if [[ "$(git rev-parse "$tag^{commit}")" == "$source_sha" &&
        "$(git log -1 --format=%s)" == "chore(release): $version [skip ci]" ]]; then
    resume=true
  else
    echo "No releasable changes since $previous."
    exit 0
  fi
fi
if $dry_run; then
  echo "Release candidate: $tag (resume=$resume); no versions, tags or releases changed."
  exit 0
fi
: "${GH_REPO:?Supply owner/repository for publication}"
releases="$(gh release list --repo "$GH_REPO" --limit 100 --json tagName,isDraft)"
published="$(jq -r --arg tag "$tag" '.[] | select(.tagName == $tag and .isDraft == false) | .tagName' <<< "$releases")"
if [[ -n "$published" ]]; then
  if $resume; then
    echo "$tag is already published."
    exit 0
  fi
  echo "Refusing to overwrite published release $tag." >&2
  exit 1
fi
mkdir -p artifacts
notes="$PWD/artifacts/RELEASE_NOTES.md"
if $resume; then
  git-cliff --latest --strip header --output "$notes"
else
  if git show-ref --verify --quiet "refs/tags/$tag"; then
    echo "Refusing to reuse an existing tag $tag." >&2
    exit 1
  fi
  git-cliff --unreleased --tag "$tag" --strip header --output "$notes"
  test -s "$notes"
  { printf '# Changelog\n\n'; cat "$notes"; printf '\n'
    awk 'BEGIN { skip=0 } /^## Unreleased/ { skip=1; next } /^## / { skip=0 } NR>1 && !skip { print }' CHANGELOG.md
  } > artifacts/changelog.next.md
  mv artifacts/changelog.next.md CHANGELOG.md
fi
bash scripts/prepare-release.sh "$version"
git fetch origin main --tags
if [[ "$(git rev-parse origin/main)" != "$source_sha" ]]; then
  echo "Main changed during release validation; refusing to publish stale code." >&2
  exit 1
fi
if ! $resume; then
  git add extension.yaml internal/cmd/version.go CHANGELOG.md
  git commit -m "chore(release): $version [skip ci]" \
    --trailer "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
  git tag -a "$tag" -m "Release $version"
  git push --atomic origin HEAD:main "$tag"
fi
(cd artifacts && sha256sum --check SHA256SUMS)
bundle="$PWD/artifacts/ikhemissi-azd-discovery_${version}.zip"
draft="$(jq -r --arg tag "$tag" '.[] | select(.tagName == $tag and .isDraft == true) | .tagName' <<< "$releases")"
if [[ -n "$draft" ]]; then
  if ! $resume; then
    echo "Existing draft is not associated with a resumable release commit." >&2
    exit 1
  fi
  gh release upload "$tag" "$bundle" artifacts/SHA256SUMS --repo "$GH_REPO" --clobber
  gh release edit "$tag" --repo "$GH_REPO" --draft=false --notes-file "$notes" --latest
else
  gh release create "$tag" "$bundle" artifacts/SHA256SUMS --repo "$GH_REPO" \
    --verify-tag --title "$tag" --notes-file "$notes" --latest
fi
