#!/bin/bash
# pmgr-vm-rollout.sh - porta piu' VM GIA' ESISTENTI alla configurazione degli snippet (via ssh, una alla volta).
# Manda sul canale ssh uno script autosufficiente (pmgr-vm-apply.sh + i file dei profili, in un archivio) ed
# esegue "sudo -n" sulla VM: non si copia niente prima, servono un utente con sudo senza password e le
# chiavi degli host gia' note (nessuna conferma automatica). Solo ASCII.
#
# Uso:
#   pmgr-vm-rollout.sh --module fail2ban|updates|ssh-hosting|baseline [--check] [--allow-no-keys] [--user U] [--hosts-file F] HOST...
#   pmgr-vm-rollout.sh --verify [--profile standard|hosting] [--user U] [--hosts-file F] HOST...
#   --module M        agent-exec (Rocky: sblocca guest-exec e aggiorna gli helper del manager; altrove nulla) | fail2ban | updates (aggiornamenti di sicurezza) | hardening-hosting (SOLO hosting: sysctl + jail recidive) | ssh-hosting (SOLO hosting: SSH solo con chiave)
#                     | ssh-standard (SOLO calcolo: root no, chiavi accettate, accesso di tecnico verificato; la password NON si tocca) | baseline (= fail2ban + updates, tutti i cluster;
#                     NON include i moduli ssh-*). Calcolo = baseline + ssh-standard; hosting = baseline + ssh-hosting + firewall-hosting + hardening-hosting
#   --verify          sola lettura: controlla lo stato atteso di ogni VM, senza modificare niente
#   --check           solo controllo: non cambia nessuna VM
#   --allow-no-keys   ssh-hosting: procede anche senza chiavi in authorized_keys (rischio di blocco)
#   --open LISTA / --allow-close-ports   firewall-hosting: porte da tenere aperte (3306/tcp,...) / accettare la chiusura delle altre
#   --user U          utente ssh;  --hosts-file F  un host per riga (# e righe vuote ignorate)
#
# Sequenza consigliata: prima --check su tutte, poi una VM di prova, poi il resto.
# Uscita: 0 tutto ok, 1 errori o VM irraggiungibili, 3 con --check e modifiche da fare.
set -uo pipefail

BASE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK=0
VERIFY=0
PROFILE=""
ALLOW=0
ALLOW_CLOSE=0
OPEN=""
SSH_USER=""
MODS=()
HOSTS=()
bad() { printf 'ERRORE: %s\n' "$*" >&2; exit 2; }
add_host() {
  [[ "$1" =~ ^[A-Za-z0-9]([A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$ ]] || bad "nome host o indirizzo non valido: $1"
  HOSTS+=("$1")
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --module)
      [ "$#" -ge 2 ] || bad "--module vuole un valore"
      case "$2" in fail2ban | updates | ssh-hosting | ssh-standard | firewall-hosting | hardening-hosting | agent-exec | baseline) MODS+=("$2") ;; *) bad "modulo sconosciuto: $2" ;; esac
      shift 2
      ;;
    --check) CHECK=1; shift ;;
    --verify) VERIFY=1; shift ;;
    --profile)
      [ "$#" -ge 2 ] || bad "--profile vuole un valore"
      case "$2" in standard | hosting) PROFILE="$2" ;; *) bad "profilo sconosciuto: $2" ;; esac
      shift 2
      ;;
    --allow-no-keys) ALLOW=1; shift ;;
    --allow-close-ports) ALLOW_CLOSE=1; shift ;;
    --open)
      [ "$#" -ge 2 ] || bad "--open vuole un elenco"
      [[ "$2" =~ ^[0-9]{1,5}/(tcp|udp)(,[0-9]{1,5}/(tcp|udp))*$ ]] || bad "--open: formato 3306/tcp,53/udp"
      OPEN="$2"
      shift 2
      ;;
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
    -h | --help) sed -n '2,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) bad "opzione sconosciuta: $1" ;;
    *) add_host "$1"; shift ;;
  esac
done
if [ "$VERIFY" = 1 ]; then
  [ "${#MODS[@]}" = 0 ] || bad "--verify non si combina con --module"
else
  [ "${#MODS[@]}" -gt 0 ] || bad "manca --module (o --verify)"
  [ -z "$PROFILE" ] || bad "--profile vale solo con --verify"
fi
[ "${#HOSTS[@]}" -gt 0 ] || bad "nessun host indicato"
for f in bin/pmgr-vm-apply.sh bin/pmgr-fail2ban-update.sh profiles/updates profiles/hosting profiles/ssh-standard helpers; do [ -e "$BASE/$f" ] || bad "manca $BASE/$f"; done

ARGS=()
if [ "$VERIFY" = 1 ]; then ARGS+=(--verify --profile "${PROFILE:-standard}"); fi
for m in "${MODS[@]}"; do ARGS+=(--module "$m"); done
[ "$CHECK" = 0 ] || ARGS+=(--check)
[ "$ALLOW" = 0 ] || ARGS+=(--allow-no-keys)
[ "$ALLOW_CLOSE" = 0 ] || ARGS+=(--allow-close-ports)
[ -z "$OPEN" ] || ARGS+=(--open "$OPEN")

# Script autosufficiente: estrae l'archivio in una cartella temporanea, lo esegue, la cancella.
PAYLOAD="$(mktemp)"
trap 'rm -f -- "$PAYLOAD"' EXIT
{
  cat <<'HEAD'
#!/bin/bash
set -eu
D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT
base64 -d <<'PMGR_B64' | tar -xzf - -C "$D"
HEAD
  tar -C "$BASE" -czf - bin/pmgr-vm-apply.sh bin/pmgr-fail2ban-update.sh profiles/updates profiles/hosting profiles/ssh-standard helpers | base64 -w 76
  printf 'PMGR_B64\nbash "$D/bin/pmgr-vm-apply.sh" "$@"\n'
} >"$PAYLOAD"

PORT_OPT=()
if [ -n "${PMGR_SSH_PORT:-}" ]; then
  [[ "$PMGR_SSH_PORT" =~ ^[0-9]{1,5}$ ]] && [ "$PMGR_SSH_PORT" -ge 1 ] && [ "$PMGR_SSH_PORT" -le 65535 ] || bad "PMGR_SSH_PORT non valida"
  PORT_OPT=(-o "Port=$PMGR_SSH_PORT")
fi
declare -A RESULT=()
worst=0
for h in "${HOSTS[@]}"; do
  target="$h"
  [ -z "$SSH_USER" ] || target="$SSH_USER@$h"
  echo "=== $h"
  # 255 = ssh non ha potuto collegarsi: succede per qualche secondo dopo che un modulo precedente ha ricaricato sshd
  # (visto su Debian 12 con ssh-hosting seguito da firewall-hosting). I moduli sono idempotenti: si riprova fino a 3 volte.
  for attempt in 1 2 3; do
    rc=0
    timeout 600 ssh -o BatchMode=yes -o ConnectTimeout=10 "${PORT_OPT[@]}" -- "$target" "sudo -n bash -s -- ${ARGS[*]}" <"$PAYLOAD" || rc=$?
    [ "$rc" = 255 ] && [ "$attempt" -lt 3 ] || break
    echo "(ssh non raggiungibile, riprovo tra 10 s: tentativo $attempt di 3)" >&2
    sleep 10
  done
  case "$rc" in
    0) RESULT[$h]="ok" ;;
    3) RESULT[$h]="da aggiornare"; [ "$worst" = 1 ] || worst=3 ;;
    255) RESULT[$h]="IRRAGGIUNGIBILE (ssh)"; worst=1 ;;
    124) RESULT[$h]="TEMPO SCADUTO"; worst=1 ;;
    *) RESULT[$h]="ERRORE (uscita $rc)"; worst=1 ;;
  esac
done

echo
echo "=== Riepilogo ($([ "$VERIFY" = 1 ] && echo "verifica, profilo ${PROFILE:-standard}" || (IFS=,; echo "${MODS[*]}"))$([ "$CHECK" = 1 ] && echo ', solo controllo'))"
for h in "${HOSTS[@]}"; do printf '  %-30s %s\n' "$h" "${RESULT[$h]}"; done
exit "$worst"
