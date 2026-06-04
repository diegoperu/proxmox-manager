#!/bin/bash
set -e
cd "$(dirname "$0")"
echo "=== ProxmoxManager Build ==="
go mod tidy
CGO_ENABLED=1 go build -ldflags="-s -w" -o proxmox-manager ./cmd/server/
echo ""
echo "✓ Build OK: ./proxmox-manager"
echo "  Avvia: ./proxmox-manager"
echo "  Apri:  http://localhost:8080"
