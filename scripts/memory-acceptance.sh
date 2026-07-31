#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
binary="${1:-$project_root/codex-usage}"
gib="${2:-10}"
fixture_root="$(mktemp -d)"
trap 'rm -rf -- "$fixture_root"' EXIT

cd "$project_root"
go run ./cmd/fixturegen -root "$fixture_root" -gib "$gib"
export CODEX_HOME="$fixture_root/.codex"
export CODEX_USAGE_HOME="$fixture_root/meter-state"

if command -v /usr/bin/time >/dev/null 2>&1; then
  /usr/bin/time -v "$binary" scan --rebuild --json 2>"$fixture_root/time.txt"
  peak_kib="$(awk -F: '/Maximum resident set size/{gsub(/ /,"",$2); print $2}' "$fixture_root/time.txt")"
  printf 'Peak RSS: %s KiB\n' "$peak_kib"
  if [[ -n "$peak_kib" && "$peak_kib" -ge 153600 ]]; then
    printf 'FAIL: peak RSS is not below 150 MiB\n' >&2
    exit 1
  fi
else
  "$binary" scan --rebuild --json
  printf 'Warning: /usr/bin/time is unavailable; RSS threshold was not measured.\n' >&2
fi
