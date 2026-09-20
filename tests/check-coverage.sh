#!/usr/bin/env bash

set -Eeuo pipefail

profile="${1:?coverage profile path is required}"
threshold="${2:-35}"

[ -r "$profile" ] || {
  printf 'coverage profile not found: %s\n' "$profile" >&2
  exit 1
}

actual="$(go tool cover -func="$profile" | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')"
[ -n "$actual" ] || {
  printf 'could not read total coverage from %s\n' "$profile" >&2
  exit 1
}

if ! awk -v actual="$actual" -v threshold="$threshold" 'BEGIN { exit !(actual + 0 >= threshold + 0) }'; then
  printf 'coverage %.1f%% is below the required %.1f%%\n' "$actual" "$threshold" >&2
  exit 1
fi

printf 'coverage %.1f%% meets the required %.1f%%\n' "$actual" "$threshold"
