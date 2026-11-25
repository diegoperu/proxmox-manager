#!/bin/bash
# pmgr-fail2ban-rollout.sh - lancia pmgr-fail2ban-update.sh su piu' VM via ssh, una alla volta.
# Lo script viene passato sul canale ssh (non si copia niente sulla VM) ed eseguito con "sudo -n":
# serve un utente con sudo senza password (come l'utente creato da cloud-init). Solo ASCII.
#
# Uso:
#   pmgr-fail2ban-rollout.sh [--check] [--user UTENTE] [--hosts-file FILE] [HOST...]
#   --check        solo controllo: non cambia nessuna VM
#   --user U       utente ssh (predefinito: quello di ~/.ssh/config o il tuo)
#   --hosts-file F un host per riga (righe vuote e # ignorate)
#
# Si usa la tua configurazione ssh: le chiavi degli host devono essere gia' note (nessuna conferma
# automatica). Consiglio: prima con --check su tutte, poi senza su una VM di prova, poi sul resto.
# Uscita: 0 se tutto ok, 1 se qualche VM e' in errore o irraggiungibile, 3 con --check e modifiche da fare.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPDATE="$HERE/pmgr-fail2ban-update.sh"
CHECK=0
SSH_USER=""
HOSTS=()

bad() { printf 'ERRORE: %s\n' "$*" >&2; exit 2; }
add_host() {
  [[ "$1" =~ ^[A-Za-z0-9]([A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$ ]] || bad "nome host o indirizzo non valido: $1"
  HOSTS+=("$1")
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --check) CHECK=1; shift ;;
    --user)
      [ "$#" -ge 2 ] || bad "--user vuole un valore"
      [[ "$2" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || bad "utente non valido: $2"
      SSH_USER="$2"
      shift 2
      ;;
    --hosts-file)
      [ "$#" -ge 2 ] || bad "--hosts-file vuole un file"
      [ -f "$2" ] || bad "file non trovato: $2"
      while IFS= read -r line || [ -n "$line" ]; do
        line="${line%%#*}"
        line="${line//[[:space:]]/}"
        [ -z "$line" ] || add_host "$line"
      done <"$2"
      shift 2
      ;;
    -h | --help) sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) bad "opzione sconosciuta: $1" ;;
    *) add_host "$1"; shift ;;
  esac
done
[ "${#HOSTS[@]}" -gt 0 ] || bad "nessun host indicato"
[ -f "$UPDATE" ] || bad "manca $UPDATE"

ARGS=()
[ "$CHECK" = 0 ] || ARGS+=(--check)

declare -A RESULT=()
worst=0
for h in "${HOSTS[@]}"; do
  target="$h"
  [ -z "$SSH_USER" ] || target="$SSH_USER@$h"
  echo "=== $h"
  timeout 300 ssh -o BatchMode=yes -o ConnectTimeout=10 -- "$target" "sudo -n bash -s -- ${ARGS[*]:-}" <"$UPDATE"
  rc=$?
  case "$rc" in
    0) RESULT[$h]="ok" ;;
    3) RESULT[$h]="da aggiornare"; [ "$worst" = 1 ] || worst=3 ;;
    255) RESULT[$h]="IRRAGGIUNGIBILE (ssh)"; worst=1 ;;
    124) RESULT[$h]="TEMPO SCADUTO"; worst=1 ;;
    *) RESULT[$h]="ERRORE (uscita $rc)"; worst=1 ;;
  esac
done

echo
echo "=== Riepilogo$([ "$CHECK" = 1 ] && echo ' (solo controllo)')"
for h in "${HOSTS[@]}"; do printf '  %-30s %s\n' "$h" "${RESULT[$h]}"; done
exit "$worst"
