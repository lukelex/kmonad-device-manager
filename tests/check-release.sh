#!/usr/bin/env bash

set -Eeuo pipefail

tag="${1:-${VERSION:-}}"
if [[ ! "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  printf '%s\n' 'usage: tests/check-release.sh vMAJOR.MINOR.PATCH' >&2
  exit 2
fi
version="${tag#v}"

pkgver="$(awk -F= '$1 == "pkgver" { print $2; exit }' packaging/arch/PKGBUILD)"
if [[ "$pkgver" != "$version" ]]; then
  printf 'release version mismatch: tag %s, packaging/arch/PKGBUILD pkgver %s\n' "$tag" "${pkgver:-missing}" >&2
  exit 1
fi

notes="docs/releases/${tag}.md"
if [[ ! -s "$notes" ]]; then
  printf 'release notes are missing or empty: %s\n' "$notes" >&2
  exit 1
fi

if ! grep -Eq "^## \[${version}\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$" CHANGELOG.md; then
  printf 'CHANGELOG.md has no dated [%s] section\n' "$version" >&2
  exit 1
fi

printf 'release metadata is consistent for %s\n' "$tag"
