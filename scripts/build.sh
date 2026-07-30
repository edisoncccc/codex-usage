#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist="$project_root/dist"
version="${VERSION:-0.1.0}"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
commit="$(git -C "$project_root" rev-parse --short HEAD 2>/dev/null || printf source)"
go_bin="${GO:-go}"
mkdir -p "$dist"

cd "$project_root"
"$go_bin" test ./...

for target in windows/amd64 windows/arm64 linux/amd64 linux/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  suffix=""
  if [[ "$os" == windows ]]; then suffix=".exe"; fi
  output="$dist/codex-meter-$os-$arch$suffix"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$go_bin" build \
    -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/local-first/codex-meter/internal/app.Version=$version -X github.com/local-first/codex-meter/internal/app.Commit=$commit -X github.com/local-first/codex-meter/internal/app.BuildDate=$build_date" \
    -o "$output" ./cmd/codex-meter
done

(
  cd "$dist"
  sha256sum codex-meter-* > SHA256SUMS
)
printf 'Built artifacts in %s\n' "$dist"
