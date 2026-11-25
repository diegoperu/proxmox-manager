#!/bin/bash
# Punto d'ingresso dell'aggiornamento completo: come update_all.sh, con un file di log per giorno.
set -uo pipefail
BASE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="${PMGR_LOG_DIR:-/var/log/pmgr}"
mkdir -p -- "$LOG_DIR"
LOG="$LOG_DIR/$(date +%Y_%m_%d)_update.log"
echo "Aggiornamento immagini CLOUD INIT: $(date -Iseconds)" | tee -a "$LOG"
"$BASE/update_all.sh" "$@" 2>&1 | tee -a "$LOG"
exit "${PIPESTATUS[0]}"
