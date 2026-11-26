#!/bin/bash
# pmgr-lib.sh - funzioni comuni degli script dei template cloud-init (ProxmoxManager, ROADMAP 13).
# Si include con "source", non si esegue. Solo ASCII.
#
# Principi:
#  - gli script sono IDENTICI su ogni cluster; quello che cambia (storage, bridge, DNS, VMID,
#    utente, percorsi dei segreti) sta in /etc/pmgr/cluster.conf, locale al cluster e NON sincronizzato;
#  - nessun file di configurazione viene "sourced": si legge riga per riga, con chiavi e valori
#    validati con una whitelist (un file manomesso non puo' eseguire codice);
#  - se la configurazione manca o e' incompleta ci si ferma prima di toccare qualunque cosa;
#  - i segreti (password dei template) stanno solo in un file 0600 fuori dalla cartella sincronizzata
#    e non compaiono mai in log, in output o in dry-run.
# shellcheck shell=bash

[ -n "${PMGR_LIB_LOADED:-}" ] && return 0
PMGR_LIB_LOADED=1

# Le regole di validazione usano intervalli di caratteri ([A-Za-z0-9], [ -~]): il loro significato
# dipende dalla lingua del sistema, quindi si lavora sempre in "C".
export LC_ALL=C

PMGR_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PMGR_BASE="$(dirname "$PMGR_LIB_DIR")"
PMGR_CLUSTER_CONF="${PMGR_CLUSTER_CONF:-/etc/pmgr/cluster.conf}"
PMGR_LOCK_FILE="${PMGR_LOCK_FILE:-/run/lock/pmgr-templates.lock}"
PMGR_LOG_TAG="UPDATE-CLOUD-INIT-DATACLOUD"
PMGR_DRY=0

# ------------------------------------------------------------------ log e uscite
pmgr_log() {
  printf '[%s] %s\n' "$(date -Iseconds)" "$*"
  logger -t "$PMGR_LOG_TAG" -- "$*" 2>/dev/null || true
}
pmgr_warn() { pmgr_log "ATTENZIONE: $*"; }
pmgr_die() {
  printf '[%s] ERRORE: %s\n' "$(date -Iseconds)" "$*" >&2
  logger -t "$PMGR_LOG_TAG" -- "ERRORE: $*" 2>/dev/null || true
  exit 1
}

# pmgr_run COMANDO...: in dry-run descrive soltanto, altrimenti esegue.
pmgr_run() {
  if [ "$PMGR_DRY" = 1 ]; then
    printf '  [dry-run]'
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

# ------------------------------------------------------------------ regole di validazione
readonly PMGR_ABS='^/[A-Za-z0-9._/-]{1,200}$'
readonly PMGR_STORAGE_RE='^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'
readonly PMGR_URL_RE='^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/[A-Za-z0-9._/%+~-]+$'

declare -A PMGR_CLUSTER_RULES=(
  [VMID_BASE]='^[0-9]{3,7}$'
  [STORAGE]="$PMGR_STORAGE_RE"
  [CI_STORAGE]="$PMGR_STORAGE_RE"
  [SNIPPETS_STORAGE]="$PMGR_STORAGE_RE"
  [SNIPPETS_DIR]="$PMGR_ABS"
  [IMAGES_DIR]="$PMGR_ABS"
  [BRIDGE]='^[A-Za-z0-9][A-Za-z0-9._-]{0,14}$'
  [NAMESERVER]='^[0-9]{1,3}(\.[0-9]{1,3}){3}( [0-9]{1,3}(\.[0-9]{1,3}){3}){0,2}$'
  [SEARCH_DOMAIN]='^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$'
  [CI_USER]='^[a-z_][a-z0-9_-]{0,31}$'
  [SSH_KEYS_FILE]="$PMGR_ABS"
  [PASSWORD_FILE]="$PMGR_ABS"
  [CPU_TYPE]='^[A-Za-z0-9_.-]{1,64}$'
  [DISK_SIZE]='^[0-9]{1,5}G$'
  [MEMORY_MB]='^[0-9]{3,6}$'
  [PROFILE]='^(standard|hosting)$'
  [TEMPLATE_IPCONFIG]='^(ip=dhcp)?$'
)
PMGR_CLUSTER_REQUIRED=(VMID_BASE STORAGE BRIDGE NAMESERVER SEARCH_DOMAIN CI_USER SSH_KEYS_FILE PASSWORD_FILE)

declare -A PMGR_OS_RULES=(
  [FAMILY]='^(ubuntu|debian|rocky)$'
  [LABEL]='^[A-Za-z0-9][A-Za-z0-9 ._()+-]{0,63}$'
  [SLOT]='^[1-9][0-9]{0,2}$'
  [ENABLED]='^[01]$'
  [ARCHES]='^(amd64|arm64|amd64 arm64|arm64 amd64)$'
  [IMAGE_URL]="$PMGR_URL_RE"
  [IMAGE_FILE]='^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
  [IMAGE_CHECKSUM_URL]="$PMGR_URL_RE"
  [IMAGE_CHECKSUM_ALGO]='^(sha256|sha512)$'
  [TEMPLATE_NAME]='^[a-z0-9][a-z0-9-]{0,62}-cloudinit-template$'
  [TAGS]='^[a-z0-9][a-z0-9._-]{0,30}(,[a-z0-9][a-z0-9._-]{0,30}){0,7}$'
  [SNIPPET]='^vendor_[A-Za-z0-9._-]{1,100}\.yaml$'
  [HELPERS]='^([a-z0-9_]+\.sh( [a-z0-9_]+\.sh){0,7})?$'
)
PMGR_OS_REQUIRED=(FAMILY LABEL SLOT IMAGE_URL IMAGE_FILE TEMPLATE_NAME TAGS SNIPPET)
readonly PMGR_OS_ID_RE='^(ubuntu|debian|rocky)-[0-9][0-9.]{0,10}$'

declare -A PMGR_ARCH_RULES=(
  [MACHINE]='^(q35|virt)$'
  [CLOUDINIT_DRIVE]='^(ide|scsi|sata)[0-9]$'
)

# ------------------------------------------------------------------ lettura delle configurazioni
# pmgr_parse_conf FILE NOME_ARRAY_REGOLE: riempie l'array globale CONF con CHIAVE=VALORE validati.
pmgr_parse_conf() {
  local file="$1"
  local -n rules="$2"
  local line n=0 key val
  declare -gA CONF=()
  { [ -f "$file" ] && [ ! -L "$file" ]; } || pmgr_die "file di configurazione mancante (o link simbolico): $file"
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    [[ "$line" =~ ^[[:space:]]*(#.*)?$ ]] && continue
    [[ "$line" =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]] || pmgr_die "$file riga $n: formato non valido (atteso CHIAVE=valore)"
    key="${BASH_REMATCH[1]}"
    val="${BASH_REMATCH[2]}"
    [[ -n "${rules[$key]+x}" ]] || pmgr_die "$file riga $n: chiave sconosciuta: $key"
    [[ -z "${CONF[$key]+x}" ]] || pmgr_die "$file riga $n: chiave ripetuta: $key"
    # I soli segnaposto ammessi si espandono prima della whitelist originale: mai codice o valori liberi.
    if [ "$2" = PMGR_OS_RULES ]; then
      case "$key" in
        IMAGE_URL | IMAGE_FILE | IMAGE_CHECKSUM_URL)
          if [[ "$val" == *'{arch}'* || "$val" == *'{march}'* ]]; then
            [[ "${PMGR_ARCH:-}" =~ ^(amd64|arm64)$ ]] && [[ "${PMGR_MARCH:-}" =~ ^(x86_64|aarch64)$ ]] \
              || pmgr_die "caricare l'architettura prima dei segnaposto in $file"
            val="${val//\{arch\}/$PMGR_ARCH}"
            val="${val//\{march\}/$PMGR_MARCH}"
          fi
          ;;
      esac
    fi
    [[ "$val" =~ ${rules[$key]} ]] || pmgr_die "$file riga $n: valore non valido per $key"
    case "$key" in
      SNIPPETS_DIR | IMAGES_DIR | SSH_KEYS_FILE | PASSWORD_FILE)
        [[ "$val" != *..* ]] || pmgr_die "$file riga $n: $key contiene '..'"
        ;;
    esac
    CONF[$key]="$val"
  done <"$file"
}

# pmgr_load_arch [ARCH]: rileva sul nodo, oppure usa l'override esplicito; mai esegue arch.d/*.conf.
pmgr_load_arch() {
  local arch="${1:-}" k
  if [ -z "$arch" ]; then
    pmgr_need_cmds dpkg
    arch="$(dpkg --print-architecture)" || pmgr_die "impossibile rilevare l'architettura con dpkg --print-architecture"
  fi
  case "$arch" in
    amd64) PMGR_MARCH=x86_64 ;;
    arm64) PMGR_MARCH=aarch64 ;;
    *) pmgr_die "architettura non supportata: sono ammesse solo amd64 e arm64" ;;
  esac
  PMGR_ARCH="$arch"
  pmgr_parse_conf "$PMGR_BASE/arch.d/$arch.conf" PMGR_ARCH_RULES
  declare -gA ARCH=()
  for k in MACHINE CLOUDINIT_DRIVE; do
    [ -n "${CONF[$k]:-}" ] || pmgr_die "arch.d/$arch.conf: manca $k"
    ARCH[$k]="${CONF[$k]}"
  done
}

pmgr_valid_ipv4_list() {
  local ip oct a b c d
  for ip in $1; do
    IFS=. read -r a b c d <<<"$ip"
    for oct in "$a" "$b" "$c" "$d"; do
      [ "$((10#$oct))" -le 255 ] || return 1
    done
  done
}

# pmgr_load_cluster: carica /etc/pmgr/cluster.conf in CL[]. Si ferma se manca o e' incompleto.
pmgr_load_cluster() {
  local k
  pmgr_parse_conf "$PMGR_CLUSTER_CONF" PMGR_CLUSTER_RULES
  declare -gA CL=()
  for k in "${!CONF[@]}"; do CL[$k]="${CONF[$k]}"; done
  for k in "${PMGR_CLUSTER_REQUIRED[@]}"; do
    [ -n "${CL[$k]:-}" ] || pmgr_die "$PMGR_CLUSTER_CONF: manca $k (configurazione incompleta, non tocco niente; vedi cluster.conf.example)"
  done
  pmgr_valid_ipv4_list "${CL[NAMESERVER]}" || pmgr_die "$PMGR_CLUSTER_CONF: NAMESERVER non e' un elenco di indirizzi IPv4 validi"
  : "${CL[CI_STORAGE]:=${CL[STORAGE]}}"
  : "${CL[SNIPPETS_STORAGE]:=local}"
  : "${CL[SNIPPETS_DIR]:=/var/lib/vz/snippets}"
  : "${CL[IMAGES_DIR]:=/var/lib/vz/pmgr-images}"
  : "${CL[CPU_TYPE]:=host}"
  : "${CL[DISK_SIZE]:=32G}"
  : "${CL[MEMORY_MB]:=1024}"
  : "${CL[PROFILE]:=standard}"
  : "${CL[TEMPLATE_IPCONFIG]:=}"
}

# pmgr_load_os ID: carica os.d/ID.conf in OS[] e calcola VMID. Richiede pmgr_load_cluster.
pmgr_load_os() {
  local id="$1" k
  [[ "$id" =~ $PMGR_OS_ID_RE ]] || pmgr_die "identificativo di sistema operativo non valido: $id"
  pmgr_parse_conf "$PMGR_BASE/os.d/$id.conf" PMGR_OS_RULES
  declare -gA OS=()
  for k in "${!CONF[@]}"; do OS[$k]="${CONF[$k]}"; done
  for k in "${PMGR_OS_REQUIRED[@]}"; do
    [ -n "${OS[$k]:-}" ] || pmgr_die "os.d/$id.conf: manca $k"
  done
  : "${OS[ENABLED]:=1}"
  : "${OS[HELPERS]:=}"
  : "${OS[ARCHES]:=amd64 arm64}"
  if [ -n "${OS[IMAGE_CHECKSUM_URL]:-}" ] || [ -n "${OS[IMAGE_CHECKSUM_ALGO]:-}" ]; then
    { [ -n "${OS[IMAGE_CHECKSUM_URL]:-}" ] && [ -n "${OS[IMAGE_CHECKSUM_ALGO]:-}" ]; } \
      || pmgr_die "os.d/$id.conf: IMAGE_CHECKSUM_URL e IMAGE_CHECKSUM_ALGO vanno insieme"
  fi
  [[ "$id" == "${OS[FAMILY]}-"* ]] || pmgr_die "os.d/$id.conf: FAMILY non coincide con il nome del file"
  OS[ID]="$id"
  VMID=$((10#${CL[VMID_BASE]} + 10#${OS[SLOT]}))
  [ -f "$PMGR_BASE/snippets.d/$id.yaml" ] || pmgr_die "manca il corpo dello snippet: snippets.d/$id.yaml"
}

pmgr_os_available() {
  [[ " ${OS[ARCHES]} " == *" $PMGR_ARCH "* ]]
}

# pmgr_all_os_ids [FAMIGLIA]: riempie PMGR_IDS con gli identificativi abilitati, in ordine di SLOT.
# Controlla anche che due release non usino lo stesso SLOT o lo stesso snippet.
pmgr_all_os_ids() {
  local family="${1:-}" f id lines=() s
  PMGR_IDS=()
  declare -A slots=() snips=()
  for f in "$PMGR_BASE"/os.d/*.conf; do
    [ -e "$f" ] || pmgr_die "nessun file in $PMGR_BASE/os.d"
    id="$(basename "$f" .conf)"
    pmgr_load_os "$id"
    [ -z "${slots[${OS[SLOT]}]+x}" ] || pmgr_die "SLOT ${OS[SLOT]} usato da ${slots[${OS[SLOT]}]} e da $id"
    slots[${OS[SLOT]}]="$id"
    [ -z "${snips[${OS[SNIPPET]}]+x}" ] || pmgr_die "lo snippet ${OS[SNIPPET]} e' usato da ${snips[${OS[SNIPPET]}]} e da $id"
    snips[${OS[SNIPPET]}]="$id"
    [ "${OS[ENABLED]}" = 1 ] || continue
    [ -z "$family" ] || [ "${OS[FAMILY]}" = "$family" ] || continue
    lines+=("$(printf '%05d %s' "${OS[SLOT]}" "$id")")
  done
  if [ "${#lines[@]}" -gt 0 ]; then
    while read -r s id; do PMGR_IDS+=("$id"); done < <(printf '%s\n' "${lines[@]}" | sort)
  fi
}

# ------------------------------------------------------------------ segreti
# pmgr_read_password: legge la password del template in PMGR_PASSWORD (mai stampata).
pmgr_read_password() {
  local f="${CL[PASSWORD_FILE]}" mode owner
  { [ -f "$f" ] && [ ! -L "$f" ]; } || pmgr_die "file della password non trovato: $f (vedi cluster.conf.example)"
  mode="$(stat -c '%a' -- "$f")"
  owner="$(stat -c '%u' -- "$f")"
  if [ "$(id -u)" = 0 ]; then
    [ "$owner" = 0 ] || pmgr_die "$f deve essere di root"
  fi
  [ $((8#$mode & 8#077)) -eq 0 ] || pmgr_die "$f deve essere leggibile solo dal proprietario (chmod 600)"
  [ "$(wc -l <"$f")" -le 1 ] || pmgr_die "$f deve contenere una sola riga"
  PMGR_PASSWORD=""
  IFS= read -r PMGR_PASSWORD <"$f" || true
  [[ "$PMGR_PASSWORD" =~ ^[\ -~]{8,128}$ ]] || pmgr_die "$f: la password deve essere di 8-128 caratteri ASCII stampabili"
}

# ------------------------------------------------------------------ immagini
# pmgr_checksum_for URL ALGO FILE: stampa l'hash atteso di FILE letto da un file di checksum
# ("hash  nome", "hash *nome" oppure "ALGO (nome) = hash"). Esce con errore se non lo trova.
pmgr_checksum_for() {
  local url="$1" algo="$2" name="$3" len tmp hash
  case "$algo" in sha256) len=64 ;; sha512) len=128 ;; *) return 1 ;; esac
  tmp="$(mktemp)"
  if ! curl -fsSL --retry 3 --connect-timeout 30 --max-time 120 -o "$tmp" -- "$url"; then
    rm -f -- "$tmp"
    return 1
  fi
  hash="$(awk -v n="$name" -v len="$len" '
    { for (i = 1; i <= NF; i++) { t = $i; gsub(/^\*|^\(|\)$/, "", t); if (t == n) found = 1 } }
    found { for (i = 1; i <= NF; i++) if ($i ~ "^[0-9a-fA-F]{" len "}$") { print tolower($i); exit } }
    { found = 0 }' "$tmp")"
  rm -f -- "$tmp"
  [ -n "$hash" ] || return 1
  printf '%s\n' "$hash"
}

# pmgr_validate_image FILE: dimensione minima e formato qcow2. Esce con errore se non va.
pmgr_validate_image() {
  local f="$1" size fmt
  size="$(stat -c %s -- "$f")"
  [ "$size" -ge 52428800 ] || { echo "file troppo piccolo ($size byte)" >&2; return 1; }
  fmt="$(qemu-img info -- "$f" 2>/dev/null | awk '/^file format:/ {print $3}')"
  [ "$fmt" = qcow2 ] || { echo "formato non riconosciuto: ${fmt:-sconosciuto}" >&2; return 1; }
}

# ------------------------------------------------------------------ lock e controlli di ambiente
pmgr_lock() {
  mkdir -p -- "$(dirname "$PMGR_LOCK_FILE")" 2>/dev/null || true
  exec 9>"$PMGR_LOCK_FILE" || pmgr_die "non riesco ad aprire il lock $PMGR_LOCK_FILE"
  flock -n 9 || pmgr_die "un altro aggiornamento dei template e' in corso (lock $PMGR_LOCK_FILE)"
}

pmgr_need_cmds() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || pmgr_die "comando mancante: $c"
  done
}

# pmgr_storage_ok STORAGE CONTENUTO: lo storage e' attivo e accetta quel contenuto (images, snippets)?
pmgr_storage_ok() {
  local st="$1" content="$2"
  pvesm status --content "$content" 2>/dev/null | awk -v s="$st" 'NR > 1 && $1 == s && $3 == "active" {ok = 1} END {exit !ok}'
}

# pmgr_preflight_cluster: controlli in sola lettura sul cluster/nodo, prima di toccare qualunque cosa.
pmgr_preflight_cluster() {
  pmgr_need_cmds qm pvesm qemu-img curl flock openssl sha256sum sha512sum cmp mktemp
  pmgr_storage_ok "${CL[STORAGE]}" images \
    || pmgr_die "lo storage ${CL[STORAGE]} non esiste, non e' attivo o non accetta dischi (images)"
  pmgr_storage_ok "${CL[CI_STORAGE]}" images \
    || pmgr_die "lo storage ${CL[CI_STORAGE]} non esiste, non e' attivo o non accetta dischi (images)"
  pmgr_storage_ok "${CL[SNIPPETS_STORAGE]}" snippets \
    || pmgr_die "lo storage ${CL[SNIPPETS_STORAGE]} non accetta snippets: in Proxmox (Datacenter > Storage) aggiungi 'Snippets' ai suoi contenuti. Non lo faccio io."
  [ -d "/sys/class/net/${CL[BRIDGE]}" ] || pmgr_die "il bridge ${CL[BRIDGE]} non esiste su questo nodo"
  [ -d "${CL[SNIPPETS_DIR]}" ] || pmgr_die "la cartella degli snippet non esiste: ${CL[SNIPPETS_DIR]}"
  { [ -f "${CL[SSH_KEYS_FILE]}" ] && [ -r "${CL[SSH_KEYS_FILE]}" ]; } || pmgr_die "file delle chiavi ssh non leggibile: ${CL[SSH_KEYS_FILE]}"
  pmgr_read_password
}
