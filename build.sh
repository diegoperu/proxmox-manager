#!/bin/bash
set -e
cd "$(dirname "$0")"
echo "=== ProxmoxManager Build ==="
go mod tidy
commit=$(git rev-parse HEAD)
date=$(git log -1 --format=%cI)
dirty=false
if [[ -n "$(git status --porcelain)" ]]; then
  dirty=true
fi
CGO_ENABLED=1 go build -ldflags="-s -w -X proxmox-manager/internal/buildinfo.Commit=$commit -X proxmox-manager/internal/buildinfo.Date=$date -X proxmox-manager/internal/buildinfo.Dirty=$dirty" -o proxmox-manager ./cmd/server/
echo ""
echo "✓ Build OK: ./proxmox-manager"
echo "  Avvia: ./proxmox-manager"
echo "  Apri:  http://localhost:8080"
