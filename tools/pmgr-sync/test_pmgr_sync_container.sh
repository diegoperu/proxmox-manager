#!/bin/bash
# Test di integrazione di pmgr-sync-setup.sh in un container Debian 13 usa e getta (come root
# nel container, senza toccare il sistema ospite). Richiede podman e l'immagine
# docker.io/library/debian:13. Dentro il container installa rsync, openssh-server e
# util-linux, poi controlla utente, cartelle, chiavi, idempotenza, --check e il gate con un
# vero rsync su ssh.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${PMGR_TEST_IMAGE:-docker.io/library/debian:13}"

podman run --rm --network=host \
  -v "$HERE:/t:ro,Z" \
  "$IMAGE" bash /t/test_pmgr_sync_inner.sh
