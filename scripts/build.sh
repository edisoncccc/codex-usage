#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
dist="$project_root/dist"
version="${VERSION:-2.3.5}"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
commit="dev"
dirty="true"
if git_root="$(git -C "$project_root" rev-parse --show-toplevel 2>/dev/null)"; then
  git_root="$(cd "$git_root" 2>/dev/null && pwd -P)" || git_root=""
  if [[ "$git_root" == "$project_root" ]]; then
    git_commit=""
    git_status=""
    if git_commit="$(git -C "$project_root" rev-parse HEAD 2>/dev/null)" &&
       git_status="$(git -C "$project_root" status --porcelain 2>/dev/null)" &&
       [[ "$git_commit" =~ ^[0-9a-f]{40}$ ]]; then
      commit="$git_commit"
      if [[ -z "$git_status" ]]; then
        dirty="false"
      fi
    fi
  fi
fi
go_bin="${GO:-go}"
mkdir -p "$dist"

cd "$project_root"
if [[ "${SKIP_TESTS:-0}" != "1" ]]; then
  "$go_bin" test ./...
fi

for target in windows/amd64 windows/arm64 linux/amd64 linux/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  suffix=""
  if [[ "$os" == windows ]]; then suffix=".exe"; fi
  output="$dist/codex-usage-$os-$arch$suffix"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$go_bin" build \
    -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/zJay26/codex-usage/internal/app.Version=$version -X github.com/zJay26/codex-usage/internal/app.Commit=$commit -X github.com/zJay26/codex-usage/internal/app.BuildDirty=$dirty -X github.com/zJay26/codex-usage/internal/app.BuildDate=$build_date" \
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
