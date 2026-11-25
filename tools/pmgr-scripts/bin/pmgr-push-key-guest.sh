#!/bin/bash
# pmgr-push-key-guest.sh - rimette una chiave SSH pubblica tra le authorized_keys di un utente DENTRO delle VM, tramite il
# guest agent (qm guest exec). Per l'amministratore del cluster, quando l'accesso SSH all'utente di servizio (tecnico) e' stato
# perso o rovinato. Da eseguire come root SUL NODO PROXMOX che ospita le VM. Solo ASCII.
#
# Uso:  pmgr-push-key-guest.sh --key-file FILE.pub [--user tecnico] [--apply] VMID [VMID...]
#   senza --apply: SOLA LETTURA. Per ogni VM controlla che sia accesa e che l'agent risponda, e stampa lo stato dell'utente,
#                  di .ssh, di authorized_keys (righe, ultimo carattere, permessi) e la configurazione di sshd: non scrive niente.
#   con --apply:   scrive. Dentro la VM, come root:
#                    - se .ssh o authorized_keys sono link simbolici si ferma (non si seguono);
#                    - crea ~/.ssh (700, dell'utente) se manca;
#                    - fa una copia di authorized_keys accanto all'originale (authorized_keys.pmgr-bak.<data>);
#                    - se l'ultimo carattere non e' un ritorno a capo ne aggiunge uno (un file senza a-capo finale fa
#                      "incollare" la nuova chiave all'ultima riga: e' la causa piu' comune di una chiave che "non funziona");
#                    - aggiunge la chiave solo se non c'e' gia' (confronto su tipo e corpo), mai altre righe toccate;
#                    - 600 e proprietario giusti, restorecon dove c'e' SELinux.
#
# Cosa NON fa: non cambia password, non tocca sshd_config, non riavvia niente, non sblocca account. Se l'utente e' bloccato o
# scaduto, o sshd non accetta le chiavi (AllowUsers, PubkeyAuthentication no), lo dice nella diagnostica e basta.
# I dati arrivano alla VM solo come argomenti posizionali dello script (mai nel suo testo), come per gli helper del manager.
set -uo pipefail
export LC_ALL=C

KEYFILE="" USER_G="tecnico" APPLY=0 VMIDS=()
bad() { printf 'ERRORE: %s\n' "$*" >&2; exit 2; }
while [ "$#" -gt 0 ]; do
  case "$1" in
    --key-file) [ "$#" -ge 2 ] || bad "--key-file vuole un valore"; KEYFILE="$2"; shift 2 ;;
    --user) [ "$#" -ge 2 ] || bad "--user vuole un valore"; USER_G="$2"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    -h | --help) sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) bad "opzione sconosciuta: $1" ;;
    *) [[ "$1" =~ ^[1-9][0-9]{2,8}$ ]] || bad "VMID non valido: $1"; VMIDS+=("$1"); shift ;;
  esac
done
[ -n "$KEYFILE" ] && [ -f "$KEYFILE" ] && [ ! -L "$KEYFILE" ] || bad "--key-file: file non trovato (o link simbolico)"
[[ "$USER_G" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || bad "--user: nome utente non valido"
[ "${#VMIDS[@]}" -gt 0 ] || bad "manca almeno un VMID"
[ "${#VMIDS[@]}" -le 50 ] || bad "al massimo 50 VM per volta"
[ "$(id -u)" = 0 ] || bad "va eseguito come root su un nodo Proxmox"
command -v qm >/dev/null 2>&1 || bad "qm non trovato: va eseguito su un nodo Proxmox"
command -v python3 >/dev/null 2>&1 || bad "manca python3"

# la chiave: una sola riga "tipo base64 [commento]", solo tipi noti, ASCII, lunghezza ragionevole
[ "$(grep -c . "$KEYFILE")" = 1 ] || bad "il file deve contenere una sola chiave (una riga)"
[ "$(stat -c %s "$KEYFILE")" -le 16384 ] || bad "chiave troppo grande"
KEY="$(grep . "$KEYFILE" | head -n 1)"
KEY_RE='^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521) [A-Za-z0-9+/]+=*( [A-Za-z0-9._@:+=,-]{1,100})?$'
[[ "$KEY" =~ $KEY_RE ]] || bad "il file non contiene una chiave pubblica valida (solo ssh-rsa, ssh-ed25519, ecdsa; commento semplice)"
ssh-keygen -l -f "$KEYFILE" >/dev/null 2>&1 || bad "ssh-keygen non riconosce la chiave"
echo "Chiave: $(ssh-keygen -l -f "$KEYFILE" | cut -d' ' -f1-2,4-)   utente nella VM: $USER_G   modo: $([ "$APPLY" = 1 ] && echo SCRITTURA || echo 'sola lettura')"

# Script eseguito DENTRO la VM (come root, dall'agent). Argomenti: $1 modo (check|apply), $2 utente, $3 chiave.
read -r -d '' GUEST <<'GUEST_EOF'
set -u
MODE="$1"; U="$2"; KEY="$3"
if ! id -u "$U" >/dev/null 2>&1; then echo "RISULTATO: ERRORE l utente $U non esiste in questa VM"; exit 3; fi
H="$(getent passwd "$U" | cut -d: -f6)"
SH="$(getent passwd "$U" | cut -d: -f7)"
D="$H/.ssh"; F="$D/authorized_keys"
echo "utente: $U  home: $H  shell: $SH"
echo "account: $(passwd -S "$U" 2>/dev/null | cut -d' ' -f2)  (P = ok, L = bloccato, NP = senza password)"
if command -v chage >/dev/null 2>&1; then echo "scadenza account: $(chage -l "$U" 2>/dev/null | sed -n 's/^Account expires[^:]*: *//p')"; fi
if [ -L "$D" ] || [ -L "$F" ]; then echo "RISULTATO: ERRORE .ssh o authorized_keys sono link simbolici: non li seguo"; exit 5; fi
if [ -e "$F" ]; then
  echo "authorized_keys: $(wc -l <"$F") righe, $(stat -c '%a %U:%G' "$F"), ultimo carattere: $(tail -c1 "$F" | od -An -c | tr -d ' ')"
  echo "contiene gia' la chiave: $(if grep -qF -- "$(printf '%s' "$KEY" | cut -d' ' -f1,2)" "$F"; then echo si; else echo no; fi)"
else
  echo "authorized_keys: non esiste"
fi
echo "~/.ssh: $( [ -d "$D" ] && stat -c '%a %U:%G' "$D" || echo 'non esiste')"
if command -v sshd >/dev/null 2>&1; then
  sshd -T 2>/dev/null | grep -E '^(pubkeyauthentication|authorizedkeysfile|allowusers|denyusers|allowgroups|passwordauthentication|permitrootlogin) ' | sed 's/^/sshd: /'
fi
if [ "$MODE" != apply ]; then echo "RISULTATO: solo lettura"; exit 0; fi
[ -d "$H" ] || { echo "RISULTATO: ERRORE la home $H non esiste"; exit 4; }
[ -d "$D" ] || install -d -m 700 -o "$U" -- "$D"
chmod 700 "$D"; chown "$U" "$D"
if [ -e "$F" ]; then
  cp -p -- "$F" "$F.pmgr-bak.$(date +%Y%m%d%H%M%S)"
  if [ -s "$F" ] && [ "$(tail -c1 "$F" | wc -l)" = 0 ]; then echo "(manca il ritorno a capo finale: lo aggiungo)"; printf '\n' >>"$F"; fi
fi
if [ -f "$F" ] && grep -qF -- "$(printf '%s' "$KEY" | cut -d' ' -f1,2)" "$F"; then
  echo "chiave gia' presente: niente da aggiungere"
else
  printf '%s\n' "$KEY" >>"$F"
  echo "chiave aggiunta"
fi
chown "$U" "$F"; chmod 600 "$F"
if command -v restorecon >/dev/null 2>&1; then restorecon -R "$D" >/dev/null 2>&1 || true; fi
echo "authorized_keys ora: $(wc -l <"$F") righe, $(stat -c '%a %U:%G' "$F")"
echo "RISULTATO: ok"
GUEST_EOF

fail=0
for vmid in "${VMIDS[@]}"; do
  echo "=== VM $vmid"
  st="$(qm status "$vmid" 2>/dev/null | sed -n 's/^status: //p')"
  if [ "$st" != running ]; then echo "  saltata: la VM non esiste o non e' accesa (stato: ${st:-sconosciuto})"; fail=$((fail + 1)); continue; fi
  if ! qm agent "$vmid" ping >/dev/null 2>&1; then echo "  saltata: il guest agent non risponde"; fail=$((fail + 1)); continue; fi
  mode=check; [ "$APPLY" = 1 ] && mode=apply
  out="$(qm guest exec "$vmid" --timeout 40 -- /bin/sh -c "$GUEST" pmgr "$mode" "$USER_G" "$KEY" 2>&1)"; rc=$?
  if [ "$rc" != 0 ]; then echo "  ERRORE da qm guest exec (uscita $rc): $(printf '%s' "$out" | head -c 300)"; fail=$((fail + 1)); continue; fi
  printf '%s' "$out" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("  risposta non leggibile"); sys.exit(1)
txt = (d.get("out-data") or "") + (("\n[stderr] " + d["err-data"]) if d.get("err-data") else "")
for line in txt.splitlines():
    print("  " + "".join(c if 32 <= ord(c) < 127 else "?" for c in line)[:200])
sys.exit(0 if d.get("exitcode") == 0 else 1)
'
  [ "${PIPESTATUS[1]}" = 0 ] || fail=$((fail + 1))
done
echo
[ "$APPLY" = 1 ] && echo "Fatto: $((${#VMIDS[@]} - fail)) VM su ${#VMIDS[@]}. Poi, dalla postazione, riprova il preflight." || echo "Sola lettura: nessuna modifica. Per scrivere aggiungi --apply."
[ "$fail" = 0 ]
