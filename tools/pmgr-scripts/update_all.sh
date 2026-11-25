#!/bin/bash
# Aggiorna i template di tutte le release abilitate e poi sincronizza gli snippet sugli altri nodi.
set -uo pipefail
BASE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TAG="UPDATE-CLOUD-INIT-DATACLOUD"
echo "[$(date -Iseconds)] Avvio aggiornamento templates"
logger -t "$TAG" 'Avvio aggiornamento templates'
"$BASE/bin/pmgr-os.sh" --all "$@"
rc=$?
if [ -x "$BASE/bin/pmgr-snippets-sync" ]; then
  "$BASE/bin/pmgr-snippets-sync" run || rc=1
else
  echo "[$(date -Iseconds)] ATTENZIONE: sincronizzazione degli snippet NON eseguita (bin/pmgr-snippets-sync non installato)" >&2
  logger -t "$TAG" 'ATTENZIONE: sincronizzazione degli snippet non eseguita'
fi
echo "[$(date -Iseconds)] Fine aggiornamento templates (uscita $rc)"
logger -t "$TAG" "Fine aggiornamento templates (uscita $rc)"
exit "$rc"
