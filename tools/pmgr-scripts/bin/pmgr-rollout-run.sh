#!/bin/bash
# pmgr-rollout-run.sh - porta MOLTE VM esistenti alla configurazione degli snippet, a ondate, con prove e stato ripetibile.
# E' il "direttore d'orchestra" sopra pmgr-vm-rollout.sh (che a sua volta manda pmgr-vm-apply.sh dentro ogni VM via ssh).
# Una VM alla volta, mai in parallelo. Solo ASCII.
#
# Uso:
#   pmgr-rollout-run.sh --hosts FILE --user UTENTE --state DIR [opzioni] FASE
#
# FASE:
#   preflight   SOLA LETTURA. Per ogni VM: una connessione ssh, sudo senza password, sistema operativo. Si ferma da sola se
#               3 VM di fila rifiutano la chiave (utente o chiave sbagliati: continuare farebbe scattare fail2ban sulle VM).
#   rootcheck   SOLA LETTURA. Conta gli accessi SSH come root negli ultimi 30 giorni su ogni VM: `ssh-standard` imposta
#               PermitRootLogin no, chi entra ancora come root resterebbe fuori. Da fare PRIMA di applicare l'SSH.
#   verify      SOLA LETTURA. pmgr-vm-apply --verify (stato atteso del profilo) su ogni VM.
#   check       SOLA LETTURA. I moduli con --check: dice cosa cambierebbe, senza scrivere.
#   canary      SCRIVE (serve --yes). Applica i moduli alla prima VM di ogni sistema operativo (da preflight) e le verifica.
#   apply       SCRIVE (serve --yes). Applica i moduli a tutte le VM, a ondate; dopo ogni VM fa --verify; si ferma al primo
#               problema (salvo --continue-on-fail). Le VM gia' riuscite non si rifanno (stato in DIR).
#   report      Riepilogo da DIR/results.tsv.
#
# Opzioni:
#   --hosts FILE            un indirizzo IPv4 per riga (# e righe vuote ignorati), per esempio target.txt di pmgr-rollout-inventory.sh
#   --user U                utente ssh sulle VM (deve avere sudo senza password)
#   --state DIR             cartella dello stato e dei registri (resta tra una fase e l'altra)
#   --profile P             standard (calcolo, predefinito) oppure hosting
#   --modules "M1 M2 ..."   moduli da applicare, in ordine. Predefiniti: standard = "baseline ssh-standard agent-exec";
#                           hosting = "baseline ssh-hosting firewall-hosting hardening-hosting agent-exec"
#   --ssh-key FILE          chiave privata da usare (la carica in un ssh-agent temporaneo; chiede la passphrase se c'e')
#   --ssh-port N            porta SSH delle VM (predefinita 22): vale per tutte le VM dell'elenco, quindi una VM con la
#                           porta diversa si tratta con un'esecuzione a parte
#   --learn-hostkeys        solo con preflight: memorizza le chiavi host sconosciute al primo contatto (accept-new)
#   --wave N                VM per ondata (predefinito 10); alla fine di ogni ondata si chiede conferma (se c'e' un terminale)
#   --max-waves N           si ferma da solo dopo N ondate (rilanciando lo stesso comando si riprende): per procedere un'ondata alla volta
#   --pause S               senza terminale: secondi di pausa tra ondate (predefinito 60)
#   --limit N               solo le prime N VM dell'elenco
#   --exclude FILE          indirizzi da saltare
#   --continue-on-fail      non fermarsi alla prima VM che non supera la verifica
#   --yes                   conferma esplicita per le fasi che scrivono (canary, apply)
#
# Con --modules scelto a mano, dopo l'applicazione si controllano solo quei moduli (--check), non l'intero profilo.
# Regola SSH (2026-11-10): il modulo ssh-standard NON tocca PasswordAuthentication; garantisce solo root no, chiavi accettate e che
# l'utente amministrativo (--user) possa entrare. Una password gia' disattivata dal proprietario della VM resta disattivata.
# Non riavvia mai le VM: dove serve un riavvio (kernel nuovo) lo scrive nel registro e nel riepilogo.
set -uo pipefail
export LC_ALL=C

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROLL="${PMGR_ROLLOUT_BIN:-$HERE/pmgr-vm-rollout.sh}"   # PMGR_ROLLOUT_BIN: solo per i test

MODS_EXPLICIT=0 MAXW=0 HOSTS="" USER_SSH="" STATE="" PROFILE="standard" MODULES="" SSH_KEY="" LEARN=0 WAVE=10 PAUSE=60 LIMIT=0 EXCLUDE="" CONT=0 YES=0 PHASE=""
bad() { printf 'ERRORE: %s\n' "$*" >&2; exit 2; }
while [ "$#" -gt 0 ]; do
  case "$1" in
    --hosts) [ "$#" -ge 2 ] || bad "--hosts vuole un valore"; HOSTS="$2"; shift 2 ;;
    --user) [ "$#" -ge 2 ] || bad "--user vuole un valore"; USER_SSH="$2"; shift 2 ;;
    --state) [ "$#" -ge 2 ] || bad "--state vuole un valore"; STATE="$2"; shift 2 ;;
    --profile) [ "$#" -ge 2 ] || bad "--profile vuole un valore"; PROFILE="$2"; shift 2 ;;
    --modules) [ "$#" -ge 2 ] || bad "--modules vuole un valore"; MODULES="$2"; MODS_EXPLICIT=1; shift 2 ;;
    --ssh-key) [ "$#" -ge 2 ] || bad "--ssh-key vuole un valore"; SSH_KEY="$2"; shift 2 ;;
    --learn-hostkeys) LEARN=1; shift ;;
    --ssh-port) [ "$#" -ge 2 ] || bad "--ssh-port vuole un valore"; [[ "$2" =~ ^[0-9]{1,5}$ ]] && [ "$2" -ge 1 ] && [ "$2" -le 65535 ] || bad "--ssh-port: numero di porta non valido"; export PMGR_SSH_PORT="$2"; shift 2 ;;
    --wave) [ "$#" -ge 2 ] || bad "--wave vuole un valore"; WAVE="$2"; shift 2 ;;
    --max-waves) [ "$#" -ge 2 ] || bad "--max-waves vuole un valore"; MAXW="$2"; shift 2 ;;
    --pause) [ "$#" -ge 2 ] || bad "--pause vuole un valore"; PAUSE="$2"; shift 2 ;;
    --limit) [ "$#" -ge 2 ] || bad "--limit vuole un valore"; LIMIT="$2"; shift 2 ;;
    --exclude) [ "$#" -ge 2 ] || bad "--exclude vuole un valore"; EXCLUDE="$2"; shift 2 ;;
    --continue-on-fail) CONT=1; shift ;;
    --yes) YES=1; shift ;;
    -h | --help) sed -n '2,38p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    preflight | rootcheck | verify | check | canary | apply | report) [ -z "$PHASE" ] || bad "una sola fase alla volta"; PHASE="$1"; shift ;;
    *) bad "argomento sconosciuto: $1" ;;
  esac
done
[ -n "$PHASE" ] || bad "manca la fase (preflight, rootcheck, verify, check, canary, apply, report)"
[ -n "$STATE" ] || bad "manca --state"
[[ "$STATE" =~ ^[A-Za-z0-9._/~-]+$ ]] || bad "--state: percorso non valido"
case "$PROFILE" in standard | hosting) ;; *) bad "--profile: standard o hosting" ;; esac
[[ "$WAVE" =~ ^[1-9][0-9]{0,2}$ ]] || bad "--wave: numero da 1 a 999"
[[ "$PAUSE" =~ ^[0-9]{1,4}$ ]] || bad "--pause: secondi"
[[ "$MAXW" =~ ^[0-9]{1,3}$ ]] || bad "--max-waves: numero"
[[ "$LIMIT" =~ ^[0-9]{1,4}$ ]] || bad "--limit: numero"
if [ -z "$MODULES" ]; then
  if [ "$PROFILE" = hosting ]; then MODULES="baseline ssh-hosting firewall-hosting hardening-hosting agent-exec"; else MODULES="baseline ssh-standard agent-exec"; fi
fi
for m in $MODULES; do
  case "$m" in fail2ban | updates | baseline | ssh-standard | ssh-hosting | firewall-hosting | hardening-hosting | agent-exec) ;; *) bad "modulo sconosciuto: $m" ;; esac
done
if [ "$PROFILE" = standard ]; then for m in $MODULES; do case "$m" in ssh-hosting | firewall-hosting | hardening-hosting) bad "il modulo $m e' solo per l'hosting (usa --profile hosting)" ;; esac; done; fi
if [ "$PROFILE" = hosting ]; then for m in $MODULES; do [ "$m" != ssh-standard ] || bad "il modulo ssh-standard e' solo per il calcolo"; done; fi
if [ "$PHASE" != report ]; then
  [ -n "$HOSTS" ] && [ -f "$HOSTS" ] || bad "--hosts: file non trovato"
  [[ "${USER_SSH:-}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || bad "--user: nome utente non valido"
  [ -f "$ROLL" ] || bad "manca $ROLL"
fi
case "$PHASE" in canary | apply) [ "$YES" = 1 ] || bad "la fase '$PHASE' SCRIVE sulle VM: aggiungi --yes (prima fai preflight, verify e check)" ;; esac
[ -z "$SSH_KEY" ] || { [ -f "$SSH_KEY" ] || bad "--ssh-key: file non trovato"; }

mkdir -p -- "$STATE/logs"
chmod 700 "$STATE"
RES="$STATE/results.tsv"
OSF="$STATE/os.tsv"
touch "$RES" "$OSF"

# ------------------------------------------------------------------ report
cmd_report() {
  echo "Registro: $RES"
  awk -F'\t' '{ last[$2 "\t" $3] = $4 } END { for (k in last) { split(k, a, "\t"); c[a[1] "\t" last[k]]++ } for (k in c) { split(k, a, "\t"); printf "  %-10s %-26s %d\n", a[1], a[2], c[k] } }' "$RES" | sort
  echo
  echo "Ultimo esito per le VM NON riuscite:"
  awk -F'\t' '{ last[$2 "\t" $3] = $4 "\t" $5 } END { for (k in last) { split(last[k], b, "\t"); if (b[1] != "ok") { split(k, a, "\t"); printf "  %-10s %-16s %s %s\n", a[1], a[2], b[1], b[2] } } }' "$RES" | sort
}
if [ "$PHASE" = report ]; then cmd_report; exit 0; fi

# ------------------------------------------------------------------ elenco VM
mapfile -t ALLHOSTS < <(grep -vE '^[[:space:]]*(#|$)' "$HOSTS" | sed 's/[[:space:]]*#.*$//; s/^[[:space:]]*//; s/[[:space:]]*$//')
IPV4='^((25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])$'
declare -A EXC=()
if [ -n "$EXCLUDE" ]; then
  [ -f "$EXCLUDE" ] || bad "--exclude: file non trovato"
  while IFS= read -r l; do l="${l%%#*}"; l="${l//[[:space:]]/}"; [ -z "$l" ] || EXC[$l]=1; done <"$EXCLUDE"
fi
HLIST=()
declare -A SEEN=()
for h in "${ALLHOSTS[@]}"; do
  [[ "$h" =~ $IPV4 ]] || bad "indirizzo non valido nell'elenco: '$h'"
  [ -z "${SEEN[$h]:-}" ] || continue
  SEEN[$h]=1
  [ -z "${EXC[$h]:-}" ] || continue
  HLIST+=("$h")
  [ "$LIMIT" = 0 ] || [ "${#HLIST[@]}" -lt "$LIMIT" ] || break
done
[ "${#HLIST[@]}" -gt 0 ] || bad "nessuna VM da trattare"

# ------------------------------------------------------------------ blocco e agente ssh
exec 9>"$STATE/lock"
flock -n 9 || { echo "ERRORE: un'altra esecuzione usa gia' $STATE" >&2; exit 1; }
AGENT_PID=""
cleanup() { [ -z "$AGENT_PID" ] || kill "$AGENT_PID" 2>/dev/null || true; }
trap cleanup EXIT
if [ -n "$SSH_KEY" ]; then
  eval "$(ssh-agent -s)" >/dev/null
  AGENT_PID="$SSH_AGENT_PID"
  ssh-add -- "$SSH_KEY" >/dev/null || { echo "ERRORE: ssh-add non riuscito" >&2; exit 1; }
fi

now() { date +%s; }
rec() { printf '%s\t%s\t%s\t%s\t%s\n' "$(now)" "$1" "$2" "$3" "${4:-}" >>"$RES"; }
done_ok() { awk -F'\t' -v p="$1" -v h="$2" '$2 == p && $3 == h { last = $4 } END { exit (last == "ok") ? 0 : 1 }' "$RES"; }
os_of() { awk -F'\t' -v h="$1" '$1 == h { print $2 " " $3 }' "$OSF" | tail -n 1; }

SSHO=(-o BatchMode=yes -o ConnectTimeout=8 -o IdentitiesOnly=no)
[ -z "${PMGR_SSH_PORT:-}" ] || SSHO+=(-o "Port=$PMGR_SSH_PORT")
[ "$LEARN" = 1 ] && SSHO+=(-o StrictHostKeyChecking=accept-new)

# ------------------------------------------------------------------ preflight
cmd_preflight() {
  local h out rc denied=0 n=0 ok=0 ko=0
  echo "Preflight su ${#HLIST[@]} VM (utente $USER_SSH). Sola lettura."
  for h in "${HLIST[@]}"; do
    n=$((n + 1))
    out="$(timeout 40 ssh "${SSHO[@]}" -- "$USER_SSH@$h" 'sudo -n true 2>/dev/null && . /etc/os-release && echo "PMGR_OS ${ID:-?} ${VERSION_ID:-?}" || echo PMGR_NOSUDO' 2>&1)"
    rc=$?
    if grep -q '^PMGR_OS ' <<<"$out"; then
      read -r _ id ver <<<"$(grep '^PMGR_OS ' <<<"$out" | tail -n 1)"
      printf '%s\t%s\t%s\n' "$h" "$id" "$ver" >>"$OSF"
      rec preflight "$h" ok "$id $ver"
      printf '  [ok]       %-15s %s %s\n' "$h" "$id" "$ver"
      ok=$((ok + 1)); denied=0
    elif grep -q 'PMGR_NOSUDO' <<<"$out"; then
      rec preflight "$h" sudo_mancante "$USER_SSH senza sudo -n"
      printf '  [SUDO]     %-15s l utente %s non ha sudo senza password\n' "$h" "$USER_SSH"
      ko=$((ko + 1)); denied=0
    elif grep -qi 'Host key verification failed\|REMOTE HOST IDENTIFICATION' <<<"$out"; then
      rec preflight "$h" chiave_host "chiave host sconosciuta o cambiata"
      printf '  [HOSTKEY]  %-15s chiave host sconosciuta o cambiata (--learn-hostkeys solo se sei sicuro che sia la VM giusta)\n' "$h"
      ko=$((ko + 1))
    elif grep -qi 'Permission denied' <<<"$out"; then
      rec preflight "$h" accesso_negato "chiave rifiutata"
      printf '  [NEGATO]   %-15s chiave rifiutata\n' "$h"
      ko=$((ko + 1)); denied=$((denied + 1))
      if [ "$denied" -ge 3 ]; then
        echo "STOP: 3 VM di fila rifiutano la chiave. Utente o chiave sbagliati: continuare farebbe scattare fail2ban sulle altre VM." >&2
        echo "      Controlla --user e --ssh-key (o ssh-add), poi riparti: le VM gia' controllate restano nel registro." >&2
        return 1
      fi
    else
      rec preflight "$h" irraggiungibile "$(printf '%s' "$out" | tail -n 1 | tr -c '[:print:]' '?' | cut -c1-100)"
      printf '  [NO RETE]  %-15s %s\n' "$h" "$(printf '%s' "$out" | tail -n 1 | cut -c1-80)"
      ko=$((ko + 1)); denied=0
    fi
  done
  echo "Preflight: $ok pronte, $ko con problemi su $n."
  # una riga per VM (vale l'ultima): il file si accumula tra piu' esecuzioni
  echo "Sistemi operativi trovati:"; awk -F'\t' '{ o[$1] = $2 " " $3 } END { for (i in o) c[o[i]]++; for (k in c) printf "  %-18s %d\n", k, c[k] }' "$OSF" | sort
  [ "$ko" = 0 ]
}

# ------------------------------------------------------------------ rootcheck
cmd_rootcheck() {
  local h out n tot=0 withroot=0 err=0
  echo "Accessi SSH come root negli ultimi 30 giorni (sola lettura) su ${#HLIST[@]} VM."
  echo "Dove ce ne sono, 'ssh-standard' (PermitRootLogin no) li bloccherebbe: avvisa chi li usa prima di applicare."
  for h in "${HLIST[@]}"; do
    # lo script remoto arriva da stdin (nessun problema di apici) e ha limiti di tempo PROPRI: un journalctl su 30 giorni di una VM
    # con un journal da GB resta in esecuzione anche se la connessione cade (era successo), quindi si filtra su sshd e si limita
    out="$(timeout 150 ssh "${SSHO[@]}" -- "$USER_SSH@$h" 'bash -s' 2>&1 <<'REMOTE_EOF'
j=$(sudo -n timeout 60 journalctl _COMM=sshd --since '-30 days' --no-pager -o cat -g 'Accepted [a-z-]* for root from' 2>/dev/null); rc=$?
n=$(printf '%s\n' "$j" | grep -c 'Accepted')
f=$(sudo -n timeout 30 grep -h 'Accepted [a-z-]* for root from' /var/log/secure /var/log/auth.log 2>/dev/null | wc -l)
echo "PMGR_ROOT $((n + f))"
if [ "$rc" != 0 ] && [ "$rc" != 1 ]; then echo "PMGR_PARTIAL $rc"; fi
REMOTE_EOF
)"
    n="$(grep '^PMGR_ROOT ' <<<"$out" | tail -n 1 | cut -d' ' -f2)"
    tot=$((tot + 1))
    if grep -q '^PMGR_PARTIAL ' <<<"$out" && [ "${n:-0}" = 0 ]; then
      rec rootcheck "$h" parziale "il registro non si e' letto per intero (tempo scaduto)"
      printf '  [PARZIALE] %-15s registro troppo grande: nessun accesso trovato nella parte letta, controlla a mano\n' "$h"; err=$((err + 1))
    elif [[ "${n:-}" =~ ^[0-9]+$ ]]; then
      if [ "$n" -gt 0 ]; then
        rec rootcheck "$h" root_usato "$n accessi come root in 30 giorni"
        printf '  [ROOT]     %-15s %s accessi come root negli ultimi 30 giorni\n' "$h" "$n"; withroot=$((withroot + 1))
      else
        rec rootcheck "$h" ok "nessun accesso come root"
        printf '  [ok]       %-15s nessun accesso come root\n' "$h"
      fi
    else
      rec rootcheck "$h" errore "$(printf '%s' "$out" | tail -n 1 | tr -c '[:print:]' '?' | cut -c1-100)"
      printf '  [ERRORE]   %-15s %s\n' "$h" "$(printf '%s' "$out" | tail -n 1 | cut -c1-80)"; err=$((err + 1))
    fi
  done
  echo "Rootcheck: $withroot VM con accessi come root, $err in errore, su $tot. (Il registro di sistema puo' coprire meno di 30 giorni.)"
  [ "$withroot" = 0 ] && [ "$err" = 0 ]
}

# ------------------------------------------------------------------ verify / check
cmd_verify() {
  local h rc log ok=0 ko=0
  echo "Verifica (sola lettura, profilo $PROFILE) su ${#HLIST[@]} VM."
  for h in "${HLIST[@]}"; do
    log="$STATE/logs/$h.verify.log"
    bash "$ROLL" --verify --profile "$PROFILE" --user "$USER_SSH" "$h" >"$log" 2>&1; rc=$?
    if grep -q 'Verifica: tutto conforme' "$log"; then
      rec verify "$h" ok "$(grep -c 'riavvio necessario: SI' "$log" | sed 's/^1$/riavvio necessario/; s/^0$//')$(grep -q 'AVVISO.*guest-exec BLOCCATO' "$log" && echo ' guest_exec_bloccato')"
      printf '  [conforme] %s\n' "$h"; ok=$((ok + 1))
    elif grep -q 'FALLITO' "$log"; then
      rec verify "$h" non_conforme "$(grep 'FALLITO' "$log" | head -n 3 | sed 's/^ *\[FALLITO\] *//' | tr '\n' ';' | cut -c1-160)"
      printf '  [DA FARE]  %-15s %s\n' "$h" "$(grep 'FALLITO' "$log" | wc -l | tr -d ' ') controlli falliti (vedi $log)"; ko=$((ko + 1))
    else
      rec verify "$h" errore "uscita $rc"
      printf '  [ERRORE]   %-15s uscita %s (vedi %s)\n' "$h" "$rc" "$log"; ko=$((ko + 1))
    fi
  done
  echo "Verifica: $ok conformi, $ko da sistemare o in errore."
}

cmd_check() {
  local h m rc log todo=0 uptodate=0 err=0
  echo "Controllo dei moduli [$MODULES] (sola lettura, nessuna scrittura) su ${#HLIST[@]} VM."
  for h in "${HLIST[@]}"; do
    log="$STATE/logs/$h.check.log"
    : >"$log"
    rc=0
    bash "$ROLL" $(for m in $MODULES; do printf -- '--module %s ' "$m"; done) --check --user "$USER_SSH" "$h" >>"$log" 2>&1 || rc=$?
    case "$rc" in
      0) rec check "$h" ok "niente da fare"; printf '  [ok]        %s niente da fare\n' "$h"; uptodate=$((uptodate + 1)) ;;
      3) rec check "$h" da_fare "$(grep -c 'da fare' "$log" | tr -d '\n') modifiche"; printf '  [DA FARE]   %-15s %s modifiche\n' "$h" "$(grep -c '\[da fare\]' "$log")"; todo=$((todo + 1)) ;;
      *) rec check "$h" errore "uscita $rc"; printf '  [ERRORE]    %-15s uscita %s (vedi %s)\n' "$h" "$rc" "$log"; err=$((err + 1)) ;;
    esac
  done
  echo "Controllo: $uptodate gia' a posto, $todo da aggiornare, $err in errore."
}

# ------------------------------------------------------------------ applicazione
apply_one() { # IP -> 0 ok, 1 modulo fallito, 2 verifica fallita
  local h="$1" m rc log="$STATE/logs/$h.apply.log"
  { printf '== %s  %s  moduli: %s\n' "$(date -Iseconds)" "$h" "$MODULES"; } >>"$log"
  for m in $MODULES; do
    rc=0
    bash "$ROLL" --module "$m" --user "$USER_SSH" "$h" >>"$log" 2>&1 || rc=$?
    if [ "$rc" != 0 ]; then
      rec apply "$h" modulo_fallito "$m (uscita $rc)"
      printf '  [FALLITO]  %-15s modulo %s (uscita %s) - vedi %s\n' "$h" "$m" "$rc" "$log"
      return 1
    fi
  done
  rc=0
  if [ "$MODS_EXPLICIT" = 1 ]; then
    # con --modules scelto a mano si applica solo una parte del profilo: la verifica completa fallirebbe per cio' che non si e' ancora
    # applicato. Si controlla che i moduli appena applicati siano a posto (--check: uscita 0 = niente da fare).
    : >"$STATE/logs/$h.verify.log"
    local margs=() mm
    for mm in $MODULES; do margs+=(--module "$mm"); done
    bash "$ROLL" "${margs[@]}" --check --user "$USER_SSH" "$h" >"$STATE/logs/$h.verify.log" 2>&1 || rc=$?
    if [ "$rc" = 0 ]; then printf 'Verifica: tutto conforme (solo i moduli applicati: %s)\n' "$MODULES" >>"$STATE/logs/$h.verify.log"; fi
  else
    bash "$ROLL" --verify --profile "$PROFILE" --user "$USER_SSH" "$h" >"$STATE/logs/$h.verify.log" 2>&1 || rc=$?
  fi
  if grep -q 'Verifica: tutto conforme' "$STATE/logs/$h.verify.log"; then
    local note=""
    grep -q 'riavvio necessario: SI' "$STATE/logs/$h.verify.log" && note="riavvio necessario"
    grep -q 'AVVISO.*guest-exec BLOCCATO' "$STATE/logs/$h.verify.log" && note="$note guest_exec_bloccato"
    rec apply "$h" ok "$note"
    printf '  [fatto]    %-15s verificata%s\n' "$h" "${note:+ ($note)}"
    return 0
  fi
  rec apply "$h" verifica_fallita "$(grep 'FALLITO' "$STATE/logs/$h.verify.log" | head -n 2 | sed 's/^ *\[FALLITO\] *//' | tr '\n' ';' | cut -c1-160)"
  printf '  [VERIFICA] %-15s applicato ma NON conforme - vedi %s\n' "$h" "$STATE/logs/$h.verify.log"
  return 2
}

run_list() { # elenco di IP (argomenti)
  local h i=0 wave=0 fails=0 ok=0 skipped=0 inwave=0 r
  for h in "$@"; do
    if done_ok apply "$h"; then skipped=$((skipped + 1)); continue; fi
    r=0
    apply_one "$h" || r=$?
    if [ "$r" = 0 ]; then ok=$((ok + 1)); else
      fails=$((fails + 1))
      if [ "$CONT" = 0 ]; then
        echo "STOP: $h non e' andata a buon fine. Guarda il registro, correggi, poi rilancia lo stesso comando (le VM riuscite non si rifanno)."
        echo "      Per proseguire comunque con le altre: --continue-on-fail."
        return 1
      fi
    fi
    inwave=$((inwave + 1))
    if [ "$inwave" -ge "$WAVE" ]; then
      wave=$((wave + 1)); inwave=0
      echo "--- ondata $wave completata ($ok riuscite, $fails problemi, $skipped gia' fatte) ---"
      if [ "$MAXW" != 0 ] && [ "$wave" -ge "$MAXW" ]; then echo "Fermo dopo $wave ondate (--max-waves). Rilancia lo stesso comando per continuare."; return 0; fi
      if [ -t 0 ]; then
        local ans=""
        read -r -p "Continuo con la prossima ondata? [s/N] " ans || ans=n
        case "$ans" in s | S | si | SI | y | Y) ;; *) echo "Interrotto su richiesta. Rilancia lo stesso comando per riprendere."; return 1 ;; esac
      else
        echo "(nessun terminale: pausa di $PAUSE s; Ctrl-C per fermare)"; sleep "$PAUSE"
      fi
    fi
  done
  echo "Applicazione: $ok riuscite, $fails con problemi, $skipped gia' fatte in precedenza."
  [ "$fails" = 0 ]
}

cmd_canary() {
  local h key chosen=()
  declare -A S=()
  for h in "${HLIST[@]}"; do
    key="$(os_of "$h")"
    [ -n "$key" ] || continue
    [ -z "${S[$key]:-}" ] || continue
    S[$key]=1
    chosen+=("$h")
    printf '  canarino per %-16s -> %s\n' "$key" "$h"
  done
  [ "${#chosen[@]}" -gt 0 ] || { echo "Nessun sistema operativo noto: fai prima 'preflight'." >&2; return 1; }
  echo "Canarini: ${#chosen[@]} VM (una per sistema operativo). Moduli: $MODULES"
  run_list "${chosen[@]}"
}

case "$PHASE" in
  preflight) cmd_preflight ;;
  rootcheck) cmd_rootcheck ;;
  verify) cmd_verify ;;
  check) cmd_check ;;
  canary) cmd_canary ;;
  apply) echo "Applicazione dei moduli [$MODULES] (profilo $PROFILE) a ${#HLIST[@]} VM, ondate da $WAVE."; run_list "${HLIST[@]}" ;;
esac
