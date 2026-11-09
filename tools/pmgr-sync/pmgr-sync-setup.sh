#!/bin/bash
# pmgr-sync-setup.sh - prepara un nodo Proxmox per la sincronizzazione di script e snippet
# (ProxmoxManager, ROADMAP 13). Va lanciato come root, una volta per nodo; si puo' rilanciare:
# non cambia nulla se e' tutto gia' a posto. Con --check non scrive niente.
#
# Cosa crea (e solo questo):
#   - utente di sistema "pmgr-sync" (senza password, senza sudo), home /var/lib/pmgr-sync
#   - cartelle /var/lib/vz/snippets e /var/lib/vz/pmgr-scripts di gruppo pmgr-sync (2775),
#     cosi' la sincronizzazione scrive senza root
#   - ruolo "dest": /usr/local/libexec/pmgr/pmgr-sync-gate (root, 0755) e authorized_keys con la
#     chiave del nodo sorgente, ristretta a quel solo comando e al suo indirizzo
#   - ruolo "source": chiave ssh ed25519 di pmgr-sync (stampata a fine corsa)
#   - facoltativo --dev-pubkey: accesso con shell di pmgr-sync, SOLO per il cluster di prova
#
# Cosa NON fa: non cancella niente, non tocca sshd_config, non tocca file di /root, non
# modifica la configurazione di Proxmox (storage, utenti, permessi).
#
# Uscite: 0 ok (o niente da fare con --check), 1 errore, 2 uso errato, 3 --check: ci sono modifiche da fare.
set -euo pipefail
umask 022

readonly SYNC_USER="pmgr-sync"
readonly SYNC_HOME="/var/lib/pmgr-sync"
readonly VZ_DIR="/var/lib/vz"
readonly SNIPPETS_DIR="${VZ_DIR}/snippets"
readonly SCRIPTS_DIR="${VZ_DIR}/pmgr-scripts"
readonly GATE_DIR="/usr/local/libexec/pmgr"
readonly GATE="${GATE_DIR}/pmgr-sync-gate"
readonly SSH_DIR="${SYNC_HOME}/.ssh"
readonly AKEYS="${SSH_DIR}/authorized_keys"
readonly KEY="${SSH_DIR}/id_ed25519"

ROLE=""
CHECK=0
SRC_FROM=""
DEV_FROM=""
DEV_KEY=""
SRC_KEYS=()
CHANGES=0

usage() {
  cat >&2 <<'EOF'
Uso: pmgr-sync-setup.sh --role dest|source|both [opzioni]

  --role R              dest   = il nodo riceve i file (serve --source-pubkey)
                        source = il nodo li invia (genera la chiave di pmgr-sync)
                        both   = tutte e due le cose
  --source-pubkey FILE  chiave pubblica (ed25519 o ecdsa, una riga) di un nodo sorgente;
                        si puo' ripetere (sorgente e copia di riserva)
  --source-from LISTA   indirizzi IPv4 dei nodi sorgente, separati da virgola (obbligatorio con
                        --source-pubkey); la chiave funziona solo da questi indirizzi
  --dev-pubkey FILE     chiave con SHELL completa di pmgr-sync (solo cluster di prova)
  --dev-from IP         indirizzo da cui e' consentita (obbligatorio con --dev-pubkey)
  --check               non scrive niente: elenca cosa cambierebbe (uscita 3 se qualcosa)
  --print-gate          stampa il testo di pmgr-sync-gate (per revisione e test) ed esce
  -h, --help            questo aiuto
EOF
}

die() { printf 'ERRORE: %s\n' "$*" >&2; exit 1; }
bad_usage() { printf 'ERRORE: %s\n\n' "$*" >&2; usage; exit 2; }

# ---------------------------------------------------------------- pmgr-sync-gate (testo)
# Comando forzato in authorized_keys. Accetta SOLO "pmgr-list" e un rsync in scrittura verso
# snippets/ o pmgr-scripts/, mai cancellazioni, mai altri percorsi.
print_gate() {
  cat <<'GATE_EOF'
#!/bin/bash
# pmgr-sync-gate - comando forzato per la chiave di pmgr-sync (installato da pmgr-sync-setup.sh,
# non modificare a mano: viene riscritto a ogni esecuzione dello setup).
# Accetta SOLO:
#   pmgr-list                           elenco dei file sincronizzati (hash, dimensione, data, percorso)
#   rsync --server ... . snippets/      scrittura in snippets (nessuna cancellazione)
#   rsync --server ... . pmgr-scripts/  scrittura in pmgr-scripts (nessuna cancellazione)
set -euo pipefail
readonly VZ_DIR="@VZDIR@"
readonly RRSYNC="@RRSYNC@"
readonly MAX_FILES=2000

deny() {
  echo "pmgr-sync-gate: comando non consentito" >&2
  logger -t PMGR-SYNC-GATE "comando rifiutato" 2>/dev/null || true
  exit 126
}

list_files() {
  local d f sum size mtime n=0
  cd "$VZ_DIR"
  for d in snippets pmgr-scripts; do
    [ -d "$d" ] || continue
    while IFS= read -r -d '' f; do
      n=$((n + 1))
      if [ "$n" -gt "$MAX_FILES" ]; then
        echo "TRUNCATED"
        return 0
      fi
      if [[ ! "$f" =~ ^[A-Za-z0-9._/@+-]+$ ]] || [[ "$f" == *..* ]]; then
        echo "INVALID"
        continue
      fi
      sum=$(sha256sum -- "$f" | cut -d' ' -f1)
      size=$(stat -c %s -- "$f")
      mtime=$(stat -c %Y -- "$f")
      printf '%s\t%s\t%s\t%s\n' "$sum" "$size" "$mtime" "$f"
    done < <(find "$d" -type f -print0 | sort -z)
  done
}

cmd="${SSH_ORIGINAL_COMMAND:-}"
case "$cmd" in
  pmgr-list)
    list_files
    ;;
  "rsync --server "*)
    allowed='^[A-Za-z0-9 ._=,+/-]+$'
    [[ "$cmd" =~ $allowed ]] || deny
    read -r -a tok <<<"$cmd"
    n=${#tok[@]}
    [ "$n" -ge 4 ] || deny
    for t in "${tok[@]}"; do
      [ "$t" = "--sender" ] && deny
    done
    [ "${tok[$((n - 2))]}" = "." ] || deny
    case "${tok[$((n - 1))]}" in
      snippets | snippets/) sub=snippets ;;
      pmgr-scripts | pmgr-scripts/) sub=pmgr-scripts ;;
      *) deny ;;
    esac
    tok[$((n - 1))]="."
    export SSH_ORIGINAL_COMMAND="${tok[*]}"
    exec "$RRSYNC" -wo -no-del "${VZ_DIR}/${sub}"
    ;;
  *)
    deny
    ;;
esac
GATE_EOF
}

# ---------------------------------------------------------------- validazione degli input
# Stampa "tipo base64" di una chiave pubblica in un file con una sola riga. Rifiuta tutto il resto.
read_pubkey() {
  local f="$1" n line
  { [ -f "$f" ] && [ ! -L "$f" ]; } || bad_usage "chiave pubblica non trovata (o non e' un file normale): $f"
  [ "$(stat -c %s -- "$f")" -le 4096 ] || bad_usage "chiave pubblica troppo grande: $f"
  n=$(grep -cve '^[[:space:]]*$' -- "$f" || true)
  [ "$n" = 1 ] || bad_usage "$f deve contenere una sola chiave (una riga)"
  line=$(grep -ve '^[[:space:]]*$' -- "$f")
  local re='^(ssh-ed25519|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521) ([A-Za-z0-9+/]+=*)( [^[:cntrl:]]*)?$'
  [[ "$line" =~ $re ]] || bad_usage "$f non e' una chiave ed25519/ecdsa valida"
  command -v ssh-keygen >/dev/null 2>&1 || die "manca ssh-keygen: non posso validare la chiave $f"
  ssh-keygen -l -f "$f" >/dev/null 2>&1 || bad_usage "$f: ssh-keygen non riconosce la chiave"
  printf '%s %s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
}

# Valida una lista di indirizzi IPv4 (con maschera facoltativa) separati da virgola.
check_from_list() {
  local what="$1" list="$2" item ip mask a b c d
  [ -n "$list" ] || bad_usage "$what: elenco vuoto"
  local re='^[0-9]{1,3}(\.[0-9]{1,3}){3}(/[0-9]{1,2})?$'
  local items
  IFS=',' read -r -a items <<<"$list"
  for item in "${items[@]}"; do
    [[ "$item" =~ $re ]] || bad_usage "$what: indirizzo non valido: $item"
    ip="${item%%/*}"
    IFS='.' read -r a b c d <<<"$ip"
    for oct in "$a" "$b" "$c" "$d"; do
      [ "$((10#$oct))" -le 255 ] || bad_usage "$what: indirizzo non valido: $item"
    done
    if [[ "$item" == */* ]]; then
      mask="${item##*/}"
      [ "$((10#$mask))" -le 32 ] || bad_usage "$what: maschera non valida: $item"
    fi
  done
}

parse_args() {
  [ "$#" -gt 0 ] || bad_usage "manca --role"
  local srcfiles=() devfile=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --role) [ "$#" -ge 2 ] || bad_usage "--role vuole un valore"; ROLE="$2"; shift 2 ;;
      --source-pubkey) [ "$#" -ge 2 ] || bad_usage "--source-pubkey vuole un file"; srcfiles+=("$2"); shift 2 ;;
      --source-from) [ "$#" -ge 2 ] || bad_usage "--source-from vuole un elenco"; SRC_FROM="$2"; shift 2 ;;
      --dev-pubkey) [ "$#" -ge 2 ] || bad_usage "--dev-pubkey vuole un file"; devfile="$2"; shift 2 ;;
      --dev-from) [ "$#" -ge 2 ] || bad_usage "--dev-from vuole un indirizzo"; DEV_FROM="$2"; shift 2 ;;
      --check) CHECK=1; shift ;;
      --print-gate) print_gate; exit 0 ;;
      -h | --help) usage; exit 0 ;;
      *) bad_usage "opzione sconosciuta: $1" ;;
    esac
  done
  case "$ROLE" in dest | source | both) ;; *) bad_usage "--role deve essere dest, source o both" ;; esac

  local f
  local k
  for f in "${srcfiles[@]+"${srcfiles[@]}"}"; do
    k=$(read_pubkey "$f") || exit 2
    SRC_KEYS+=("$k")
  done
  if [ "${#SRC_KEYS[@]}" -gt 0 ]; then
    [ "$ROLE" != "source" ] || bad_usage "--source-pubkey non ha senso con --role source"
    check_from_list "--source-from" "$SRC_FROM"
  elif [ -n "$SRC_FROM" ]; then
    bad_usage "--source-from senza --source-pubkey"
  fi
  if [ "$ROLE" != "source" ] && [ "${#SRC_KEYS[@]}" -eq 0 ]; then
    bad_usage "il ruolo $ROLE richiede almeno una --source-pubkey"
  fi
  if [ -n "$devfile" ]; then
    check_from_list "--dev-from" "$DEV_FROM"
    [[ "$DEV_FROM" != *,* ]] || bad_usage "--dev-from vuole un solo indirizzo"
    DEV_KEY=$(read_pubkey "$devfile") || exit 2
  elif [ -n "$DEV_FROM" ]; then
    bad_usage "--dev-from senza --dev-pubkey"
  fi
}

# ---------------------------------------------------------------- passi
# act "descrizione" comando...: con --check descrive soltanto, altrimenti esegue.
act() {
  local desc="$1"
  shift
  CHANGES=$((CHANGES + 1))
  if [ "$CHECK" = 1 ]; then
    printf '  [da fare] %s\n' "$desc"
    return 0
  fi
  printf '  [fatto]   %s\n' "$desc"
  "$@"
}
same() { printf '  [ok]      %s\n' "$1"; }

preflight() {
  [ "$(id -u)" = 0 ] || die "va lanciato come root"
  local c
  for c in useradd usermod getent install stat find sort sha256sum cmp mktemp runuser hostname; do
    command -v "$c" >/dev/null 2>&1 || die "comando mancante: $c"
  done
  [ -d "$VZ_DIR" ] || die "$VZ_DIR non esiste: questo non sembra un nodo Proxmox con lo storage 'local' predefinito"
  case "$ROLE" in
    dest | both)
      command -v python3 >/dev/null 2>&1 || die "manca python3 (serve a rrsync)"
      RRSYNC=""
      local p
      for p in /usr/bin/rrsync /usr/local/bin/rrsync /usr/share/doc/rsync/scripts/rrsync; do
        if [ -f "$p" ] && [ -x "$p" ]; then RRSYNC="$p"; break; fi
      done
      [ -n "$RRSYNC" ] || die "rrsync non trovato (pacchetto rsync): senza non si puo' limitare la scrittura"
      grep -q -- '-no-del' "$RRSYNC" || die "$RRSYNC non supporta -no-del: aggiorna rsync"
      grep -q -- '-wo' "$RRSYNC" || die "$RRSYNC non supporta -wo: aggiorna rsync"
      ;;
  esac
  case "$ROLE" in
    source | both)
      command -v ssh-keygen >/dev/null 2>&1 || die "comando mancante: ssh-keygen"
      ;;
  esac
}

ensure_user() {
  if getent passwd "$SYNC_USER" >/dev/null; then
    local home shell uid
    home=$(getent passwd "$SYNC_USER" | cut -d: -f6)
    shell=$(getent passwd "$SYNC_USER" | cut -d: -f7)
    uid=$(getent passwd "$SYNC_USER" | cut -d: -f3)
    [ "$home" = "$SYNC_HOME" ] || die "l'utente $SYNC_USER esiste con home $home (atteso $SYNC_HOME): non lo modifico"
    [ "$uid" != 0 ] || die "l'utente $SYNC_USER ha uid 0: non lo modifico"
    [ "$shell" = /bin/bash ] || die "l'utente $SYNC_USER esiste con shell $shell (attesa /bin/bash): non lo modifico"
    same "utente $SYNC_USER"
    local pw
    pw=$(getent shadow "$SYNC_USER" | cut -d: -f2)
    if [ "$pw" != '*' ]; then
      act "password di $SYNC_USER disabilitata" usermod -p '*' "$SYNC_USER"
    else
      same "password di $SYNC_USER disabilitata"
    fi
  else
    act "creo l'utente $SYNC_USER" useradd --system --user-group --create-home \
      --home-dir "$SYNC_HOME" --shell /bin/bash --comment "ProxmoxManager sync" "$SYNC_USER"
    act "password di $SYNC_USER disabilitata" usermod -p '*' "$SYNC_USER"
  fi
}

# ensure_dir PERCORSO PROPRIETARIO GRUPPO MODO
ensure_dir() {
  local dir="$1" owner="$2" group="$3" mode="$4" cur
  cur=$(stat -c '%U:%G:%a' -- "$dir" 2>/dev/null || echo missing)
  if [ "$cur" = "${owner}:${group}:${mode}" ]; then
    same "cartella $dir ($owner:$group $mode)"
  elif [ "$cur" = missing ]; then
    act "creo la cartella $dir ($owner:$group $mode)" bash -c 'mkdir -p -- "$3" && chown -- "$1:$2" "$3" && chmod -- "$4" "$3"' _ "$owner" "$group" "$dir" "$mode"
  else
    act "imposto $dir a $owner:$group $mode (era $cur)" bash -c 'chown -- "$1:$2" "$3" && chmod -- "$4" "$3"' _ "$owner" "$group" "$dir" "$mode"
  fi
}

# install_file ORIGINE DESTINAZIONE PROPRIETARIO GRUPPO MODO
install_file() {
  local src="$1" dst="$2" owner="$3" group="$4" mode="$5" cur
  cur=$(stat -c '%U:%G:%a' -- "$dst" 2>/dev/null || echo missing)
  if [ "$cur" = "${owner}:${group}:${mode}" ] && cmp -s -- "$src" "$dst"; then
    same "file $dst"
  else
    act "scrivo $dst ($owner:$group $mode)" install -o "$owner" -g "$group" -m "$mode" -- "$src" "$dst"
  fi
}

build_authorized_keys() {
  local k
  for k in "${SRC_KEYS[@]+"${SRC_KEYS[@]}"}"; do
    printf 'restrict,from="%s",command="%s" %s pmgr-source\n' "$SRC_FROM" "$GATE" "$k"
  done
  if [ -n "$DEV_KEY" ]; then
    printf 'from="%s" %s pmgr-dev\n' "$DEV_FROM" "$DEV_KEY"
  fi
}

ensure_gate() {
  local tmp
  tmp=$(mktemp)
  TMPFILES+=("$tmp")
  print_gate | sed -e "s|@RRSYNC@|${RRSYNC}|" -e "s|@VZDIR@|${VZ_DIR}|" >"$tmp"
  ensure_dir "$GATE_DIR" root root 755
  install_file "$tmp" "$GATE" root root 755
}

ensure_authorized_keys() {
  local tmp
  tmp=$(mktemp)
  TMPFILES+=("$tmp")
  build_authorized_keys >"$tmp"
  ensure_dir "$SSH_DIR" "$SYNC_USER" "$SYNC_USER" 700
  install_file "$tmp" "$AKEYS" "$SYNC_USER" "$SYNC_USER" 600
}

ensure_source_key() {
  if [ -f "$KEY" ]; then
    same "chiave ssh $KEY"
  else
    ensure_dir "$SSH_DIR" "$SYNC_USER" "$SYNC_USER" 700
    act "genero la chiave ssh ed25519 di $SYNC_USER" \
      runuser -u "$SYNC_USER" -- ssh-keygen -q -t ed25519 -N '' -C "pmgr-sync@$(hostname -s)" -f "$KEY"
  fi
}

# Avvisa se sshd potrebbe rifiutare l'utente (AllowUsers/DenyUsers); non modifica nulla.
check_sshd() {
  command -v sshd >/dev/null 2>&1 || { echo "  [avviso]  sshd non trovato: non controllo AllowUsers"; return 0; }
  local out
  out=$(sshd -T -C "user=${SYNC_USER},host=localhost,addr=127.0.0.1" 2>/dev/null || true)
  [ -n "$out" ] || { echo "  [avviso]  non riesco a leggere la configurazione di sshd"; return 0; }
  if grep -qi '^allowusers ' <<<"$out" && ! grep -i '^allowusers ' <<<"$out" | grep -qw "$SYNC_USER"; then
    echo "  [avviso]  sshd ha AllowUsers senza $SYNC_USER: l'accesso verra' rifiutato finche' non lo aggiungi"
  fi
  if grep -i '^denyusers ' <<<"$out" | grep -qw "$SYNC_USER"; then
    echo "  [avviso]  sshd ha $SYNC_USER in DenyUsers"
  fi
  if grep -qi '^pubkeyauthentication no' <<<"$out"; then
    echo "  [avviso]  sshd ha PubkeyAuthentication disattivata"
  fi
}

verify() {
  local d
  for d in "$SNIPPETS_DIR" "$SCRIPTS_DIR"; do
    runuser -u "$SYNC_USER" -- test -w "$d" || die "$SYNC_USER non riesce a scrivere in $d"
    runuser -u "$SYNC_USER" -- test -x "$VZ_DIR" || die "$SYNC_USER non attraversa $VZ_DIR"
  done
  case "$ROLE" in
    dest | both) runuser -u "$SYNC_USER" -- test -x "$GATE" || die "$SYNC_USER non puo' eseguire $GATE" ;;
  esac
  echo "  [ok]      verifica: $SYNC_USER scrive in snippets e pmgr-scripts"
}

main() {
  parse_args "$@"
  preflight
  echo "pmgr-sync-setup: ruolo=$ROLE$([ "$CHECK" = 1 ] && echo ' (solo controllo)')"

  ensure_user
  ensure_dir "$SYNC_HOME" "$SYNC_USER" "$SYNC_USER" 750
  ensure_dir "$SNIPPETS_DIR" root "$SYNC_USER" 2775
  ensure_dir "$SCRIPTS_DIR" root "$SYNC_USER" 2775

  case "$ROLE" in
    dest | both) ensure_gate ;;
  esac
  case "$ROLE" in
    source | both) ensure_source_key ;;
  esac
  if [ "$ROLE" != source ] || [ -n "$DEV_KEY" ]; then
    ensure_authorized_keys
  fi
  check_sshd

  if [ "$CHECK" = 1 ]; then
    if [ "$CHANGES" -gt 0 ]; then
      echo "Modifiche da fare: $CHANGES"
      exit 3
    fi
    echo "Niente da fare."
    exit 0
  fi
  verify
  logger -t PMGR-SYNC-SETUP "setup eseguito (ruolo $ROLE, modifiche $CHANGES)" 2>/dev/null || true
  if [ -f "${KEY}.pub" ]; then
    echo
    echo "Chiave pubblica di $SYNC_USER su questo nodo (da dare ai nodi che devono ricevere):"
    cat "${KEY}.pub"
  fi
  echo "Fatto (modifiche: $CHANGES)."
}

RRSYNC=""
TMPFILES=()
cleanup() {
  local f
  for f in "${TMPFILES[@]+"${TMPFILES[@]}"}"; do rm -f -- "$f"; done
}
trap cleanup EXIT
main "$@"
