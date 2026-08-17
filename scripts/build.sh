#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist="$project_root/dist"
version="${VERSION:-2.3.5}"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
commit="$(git -C "$project_root" rev-parse --short HEAD 2>/dev/null || printf source)"
if git -C "$project_root" rev-parse --is-inside-work-tree >/dev/null 2>&1 &&
   ! git -C "$project_root" diff --quiet --ignore-submodules HEAD --; then
  commit="${commit}-dirty"
fi
go_bin="${GO:-go}"
mkdir -p "$dist"

cd "$project_root"
"$go_bin" test ./...

for target in windows/amd64 windows/arm64 linux/amd64 linux/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  suffix=""
  if [[ "$os" == windows ]]; then suffix=".exe"; fi
  output="$dist/codex-usage-$os-$arch$suffix"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$go_bin" build \
    -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/zJay26/codex-usage/internal/app.Version=$version -X github.com/zJay26/codex-usage/internal/app.Commit=$commit -X github.com/zJay26/codex-usage/internal/app.BuildDate=$build_date" \
    -o "$output" ./cmd/codex-usage
done

(
  cd "$dist"
  sha256sum \
    codex-usage-linux-amd64 \
    codex-usage-linux-arm64 \
    codex-usage-windows-amd64.exe \
    codex-usage-windows-arm64.exe > SHA256SUMS
)
printf 'Built artifacts in %s\n' "$dist"
