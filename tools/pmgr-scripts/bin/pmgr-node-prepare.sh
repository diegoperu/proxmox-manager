#!/bin/bash
# pmgr-node-prepare.sh - preparazione non interattiva di un nodo PVE 9 / Debian trixie.
# Senza --apply: sola lettura. Uscite: 0 ok, 1 errore, 2 uso errato, 3 modifiche necessarie.
# Ogni file sostituito ha una copia in /root/pmgr-backup/node-prepare-YYYYmmdd-HHMMSS/.
# Non tocca pve-ha-lrm, pve-ha-crm, corosync, /etc/pve, rete, utenti o SSH.
# Non riavvia mai. La release Ceph arriva dal cluster, mai dalla versione di PVE.
# Hook di test SOLO via env: PMGR_ROOT prefisso di tutti i path dati (default vuoto);
# PMGR_TEST=1 salta SOLO il controllo root. pveversion/apt-get si simulano tramite PATH.
set -Eeuo pipefail
export LC_ALL=C
umask 022

die() { printf 'ERRORE: %s\n' "$*" >&2; exit 1; }
usage() {
  printf '%s\n' 'Uso: pmgr-node-prepare.sh [--apply] [--ceph squid|tentacle|none] [--keep-nag] [--upgrade]' >&2
}
bad_usage() { usage; exit 2; }

APPLY=0 CEPH=none KEEP_NAG=0 UPGRADE=0
declare -A SEEN=()
while [ "$#" -gt 0 ]; do
  case "$1" in --apply|--ceph|--keep-nag|--upgrade) ;; *) bad_usage ;; esac
  [ -z "${SEEN[$1]:-}" ] || bad_usage
  SEEN["$1"]=1
  case "$1" in
    --apply) APPLY=1; shift ;;
    --keep-nag) KEEP_NAG=1; shift ;;
    --upgrade) UPGRADE=1; shift ;;
    --ceph)
      [ "$#" -ge 2 ] || bad_usage
      case "$2" in squid|tentacle|none) CEPH="$2" ;; *) bad_usage ;; esac
      shift 2 ;;
  esac
done
[ "$UPGRADE" = 0 ] || [ "$APPLY" = 1 ] || bad_usage

# Queste funzioni vengono incluse anche nell'helper autonomo dell'hook apt.
init_root() {
  [ "${PMGR_TEST:-0}" = 1 ] || [ "$(id -u)" = 0 ] || die 'va eseguito come root'
  ROOT="${PMGR_ROOT:-}"
  if [ -n "$ROOT" ]; then
    [[ "$ROOT" == /* && ! "$ROOT" =~ [[:cntrl:]] && ${#ROOT} -lt 4096 ]] || die 'PMGR_ROOT non valido'
    ROOT=$(realpath -e -- "$ROOT") || die 'PMGR_ROOT non esiste'
    [ -d "$ROOT" ] || die "PMGR_ROOT non e' una cartella"
    [ "$ROOT" != / ] || ROOT=''
  fi
  BACKUP='' TEMPFILES=()
}

# Rifiuta link anche nei genitori: una scrittura non deve uscire dal prefisso di test.
safe_path() {
  local path="$1" part current="$ROOT" relative
  [[ "$path" == "$ROOT/"* && ! "$path" =~ [[:cntrl:]] ]] || die 'percorso non consentito'
  relative="${path#"$ROOT/"}"
  local parts=()
  IFS=/ read -r -a parts <<<"$relative"
  for part in "${parts[@]}"; do
    [[ -n "$part" && "$part" != . && "$part" != .. ]] || die 'percorso non consentito'
    current="$current/$part"
    [ ! -L "$current" ] || die "link simbolico non consentito: $current"
    if [ "$current" != "$path" ] && [ -e "$current" ]; then
      [ -d "$current" ] || die "non e' una cartella: $current"
    fi
  done
}

ensure_dir() {
  local path="$1" mode="${2:-755}"
  if [ "$path" = "${ROOT:-/}" ]; then
    [ -d "$path" ] || die 'cartella radice assente'
    return
  fi
  safe_path "$path"
  if [ ! -d "$path" ]; then
    ensure_dir "${path%/*}" "$mode"
    mkdir -m "$mode" -- "$path"
    chown 0:0 -- "$path"
  fi
}

init_backup() {
  [ -z "$BACKUP" ] || return 0
  local base="$ROOT/root/pmgr-backup" candidate
  ensure_dir "$base" 700
  chmod 700 -- "$base"
  chown 0:0 -- "$base"
  while :; do
    candidate="$base/node-prepare-$(date +%Y%m%d-%H%M%S)"
    safe_path "$candidate"
    if [ -e "$candidate" ]; then sleep 1; continue; fi
    mkdir -m 700 -- "$candidate"
    chown 0:0 -- "$candidate"
    BACKUP="$candidate"
    break
  done
}

log_change() { logger -t PMGR-NODE-PREPARE -- "$1" 2>/dev/null || true; }

atomic_write() {
  local path="$1" mode="$2" content="$3" temporary backup_path backup_tmp
  safe_path "$path"
  ensure_dir "${path%/*}"
  init_backup
  if [ -e "$path" ]; then
    [ -f "$path" ] || die "non e' un file normale: $path"
    backup_path="$BACKUP/${path#"$ROOT/"}"
    ensure_dir "${backup_path%/*}" 700
    backup_tmp=$(mktemp "${backup_path%/*}/.pmgr-backup.XXXXXX")
    TEMPFILES+=("$backup_tmp")
    cp -p -- "$path" "$backup_tmp"
    chown 0:0 -- "$backup_tmp"
    mv -fT -- "$backup_tmp" "$backup_path"
  fi
  temporary=$(mktemp "${path%/*}/.pmgr-node-prepare.XXXXXX")
  TEMPFILES+=("$temporary")
  printf '%s' "$content" >"$temporary"
  chown 0:0 -- "$temporary"
  chmod "$mode" -- "$temporary"
  mv -fT -- "$temporary" "$path"
  log_change "aggiornato ${path#"$ROOT"}"
}

cleanup() {
  local path
  for path in "${TEMPFILES[@]}"; do rm -f -- "$path"; done
}

nag_content() {
  # Stesse due sostituzioni di community-scripts/ProxmoxVE post-pve-install.sh (MIT).
  # Niente sed -i: atomic_write conserva prima l'originale.
  sed -e '/data\.status/ s/!//' -e '/data\.status/ s/active/NoMoreNagging/' -- "$1"
}

print_nag_helper() {
  cat <<'HEADER'
#!/bin/bash
# pmgr-remove-nag.sh - generato da pmgr-node-prepare.sh; hook apt autonomo, solo Web UI.
# PMGR_ROOT prefissa tutti i path dati; PMGR_TEST=1 salta solo il controllo root.
set -Eeuo pipefail
export LC_ALL=C
umask 022
HEADER
  declare -f die init_root safe_path ensure_dir init_backup log_change atomic_write cleanup nag_content
  cat <<'BODY'
trap 'printf "ERRORE: rimozione avviso fallita\n" >&2; exit 1' ERR
init_root
trap cleanup EXIT
web_js="$ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js"
safe_path "$web_js"
if [ -s "$web_js" ] && ! grep -qF NoMoreNagging -- "$web_js"; then
  [ -f "$web_js" ] || die "proxmoxlib.js non e' un file normale"
  [ "$(stat -c %s -- "$web_js")" -le 16777216 ] || die 'proxmoxlib.js troppo grande'
  exec {nag_lock}<"${web_js%/*}"
  flock -n "$nag_lock" || die "patch nag gia' in corso"
  content=$(nag_content "$web_js" && printf '.')
  content="${content%.}"
  [[ "$content" == *NoMoreNagging* ]] || die 'formato proxmoxlib.js non riconosciuto'
  atomic_write "$web_js" 644 "$content"
fi
BODY
}

# Parser a righe: mantiene commenti, separatori e stanzas estranee. Le continuazioni
# appartengono al campo precedente, anche per URIs, Components ed Enabled.
deb822() {
  local mode="$1" file="$2"
  awk -v mode="$mode" -v release="$CEPH" '
    function flush(    i,key,value,field,enterprise,disabled,found,skip,uri,count,urls) {
      if (!n) return
      delete fields
      field=""
      for (i=1;i<=n;i++) {
        if (lines[i] ~ /^[#]/) continue
        if (lines[i] ~ /^[ \t]/) {
          value=lines[i]; sub(/^[ \t]+/, "", value)
          if (field != "") fields[field]=fields[field] " " value
        } else if (index(lines[i], ":")) {
          key=lines[i]; sub(/:.*/, "", key); field=tolower(key)
          value=substr(lines[i], index(lines[i], ":")+1)
          sub(/^[ \t]+/, "", value); sub(/[ \t\r]+$/, "", value)
          fields[field]=value
        }
      }
      for (key in fields) {
        gsub(/[ \t]+/, " ", fields[key])
        sub(/^ /, "", fields[key]); sub(/ $/, "", fields[key])
      }
      enterprise=(fields["uris"] ~ /enterprise[.]proxmox[.]com/)
      disabled=(tolower(fields["enabled"]) ~ /^(no|false|0)$/)
      if (mode == "enterprise") {
        if (enterprise) print "yes"
      } else if (mode == "conflict") {
        if (!disabled && (" " fields["components"] " ") ~ / no-subscription /) {
          count=split(fields["uris"], urls, /[ \t]+/)
          for (i=1;i<=count;i++) {
            uri=urls[i]
            if (uri ~ /\/ceph-[a-z0-9-]+\/?$/) {
              sub(/^.*\/ceph-/, "", uri); sub(/\/$/, "", uri)
              if (uri != release) print uri
            }
          }
        }
      } else if (mode != "ceph" || enterprise || disabled || fields["types"] == "") {
        found=0; skip=0
        for (i=1;i<=n;i++) {
          if (enterprise && tolower(lines[i]) ~ /^enabled:/) {
            if (!found) print "Enabled: no"
            found=1; skip=1
          } else if (skip && lines[i] ~ /^[ \t]/) {
            continue
          } else {
            skip=0; print lines[i]
          }
        }
        if (enterprise && !found) print "Enabled: no"
      }
      n=0
    }
    /^[ \t\r]*$/ { flush(); if (mode != "conflict" && mode != "enterprise") print; next }
    { lines[++n]=$0 }
    END { flush() }
  ' "$file"
}

preflight() {
  local command_name codename version resolved file
  init_root
  for command_name in awk sed grep stat cmp mktemp realpath flock cp mv chmod chown mkdir date sort uname cat tail; do
    command -v "$command_name" >/dev/null 2>&1 || die "comando mancante: $command_name"
  done
  resolved=$(realpath -e -- "$ROOT/etc/os-release") || die 'manca /etc/os-release'
  [[ "$resolved" == "$ROOT/"* ]] || die 'os-release fuori da PMGR_ROOT'
  [ -f "$resolved" ] && [ "$(stat -c %s -- "$resolved")" -le 65536 ] || die 'os-release non valido'
  codename=$(awk -F= '$1 == "VERSION_CODENAME" { gsub(/["\047]/, "", $2); print $2 }' "$resolved")
  [ "$codename" = trixie ] || die 'serve Debian trixie (VERSION_CODENAME)'
  command -v pveversion >/dev/null 2>&1 || die 'pveversion non trovato'
  version=$(pveversion) || die 'pveversion fallito'
  [[ "$version" =~ ^pve-manager/9[./] ]] || die 'serve pve-manager major 9'
  safe_path "$ROOT/etc/apt/sources.list.d"
  [ -d "$ROOT/etc/apt/sources.list.d" ] || die 'manca sources.list.d'
  safe_path "$ROOT/root/pmgr-backup"
  if [ "$UPGRADE" = 1 ]; then command -v apt-get >/dev/null 2>&1 || die 'apt-get non trovato'; fi
  for file in "$ROOT/etc/apt/sources.list" "$ROOT/etc/apt/sources.list.d/"*.list \
    "$ROOT/etc/apt/sources.list.d/"*.sources; do
    safe_path "$file"
    [ -e "$file" ] || continue
    [ -f "$file" ] || die "non e' un file normale: $file"
    [ "$(stat -c %s -- "$file")" -le 1048576 ] || die "file troppo grande: $file"
  done
  if [ "$CEPH" != none ] && [ -f "$ROOT/etc/apt/sources.list.d/ceph.sources" ]; then
    local conflict
    conflict=$(deb822 conflict "$ROOT/etc/apt/sources.list.d/ceph.sources")
    [ -z "$conflict" ] || die "il nodo usa gia' ceph-${conflict%%$'\n'*}: non cambio release di Ceph"
  fi
}

plan_file() {
  local file="$1" mode="$2" content="$3"
  safe_path "$file"
  if [ -e "$file" ]; then
    [ -f "$file" ] || die "non e' un file normale: $file"
    if [ "$(stat -c '%u:%g:%a' -- "$file")" = "0:0:$mode" ] && cmp -s -- "$file" <(printf '%s' "$content"); then
      printf 'ok: %s\n' "${file#"$ROOT"}"
      return
    fi
  fi
  printf 'da cambiare: %s (contenuto o permessi %s root)\n' "${file#"$ROOT"}" "$mode"
  FILES+=("$file"); MODES+=("$mode"); CONTENTS+=("$content")
}

ceph_source() {
  printf 'Types: deb\nURIs: http://download.proxmox.com/debian/ceph-%s\nSuites: trixie\nComponents: no-subscription\nSigned-By: /usr/share/keyrings/proxmox-archive-keyring.gpg\n' "$CEPH"
}

build_plan() {
  FILES=() MODES=() CONTENTS=()
  local file content retained nag="$ROOT/usr/local/libexec/pmgr/pmgr-remove-nag.sh"
  local hook="$ROOT/etc/apt/apt.conf.d/90pmgr-no-nag"
  content=$(cat <<'DEBIAN'
Types: deb
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: http://security.debian.org/debian-security
Suites: trixie-security
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
DEBIAN
)
  plan_file "$ROOT/etc/apt/sources.list.d/debian.sources" 644 "$content"$'\n'
  content=$(cat <<'PROXMOX'
Types: deb
URIs: http://download.proxmox.com/debian/pve
Suites: trixie
Components: pve-no-subscription
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
PROXMOX
)
  plan_file "$ROOT/etc/apt/sources.list.d/proxmox.sources" 644 "$content"$'\n'
  for file in "$ROOT/etc/apt/sources.list" "$ROOT/etc/apt/sources.list.d/"*.list; do
    [ -f "$file" ] || continue
    content=$(sed '/^[[:space:]]*deb\(-src\)\?[[:space:]]/s/^/# pmgr-disabled: /' -- "$file" && printf '.')
    content="${content%.}"
    plan_file "$file" 644 "$content"
  done
  for file in "$ROOT/etc/apt/sources.list.d/"*.sources; do
    [ -f "$file" ] || continue
    case "${file##*/}" in debian.sources|proxmox.sources) continue ;; esac
    if [ "${file##*/}" = ceph.sources ] && [ "$CEPH" != none ]; then continue; fi
    if [ -n "$(deb822 enterprise "$file")" ]; then
      content=$(deb822 disable "$file" && printf '.')
    else
      content=$(cat -- "$file" && printf '.')
    fi
    content="${content%.}"
    plan_file "$file" 644 "$content"
  done
  if [ "$CEPH" != none ]; then
    file="$ROOT/etc/apt/sources.list.d/ceph.sources"
    retained=''
    if [ -f "$file" ]; then retained=$(deb822 ceph "$file"); fi
    content=$(ceph_source)
    if [ -n "$retained" ]; then content="$retained"$'\n\n'"$content"; fi
    plan_file "$file" 644 "$content"$'\n'
  else
    printf 'ok: Ceph none, nessun repository aggiunto\n'
  fi
  if [ "$KEEP_NAG" = 1 ]; then
    for file in "$nag" "$hook"; do
      if [ -e "$file" ] || [ -L "$file" ]; then
        printf 'ok: --keep-nag, mantengo %s (rimozione manuale)\n' "${file#"$ROOT"}"
      fi
    done
    return
  fi
  content=$(print_nag_helper)
  plan_file "$nag" 755 "$content"$'\n'
  plan_file "$hook" 644 'DPkg::Post-Invoke { "/usr/local/libexec/pmgr/pmgr-remove-nag.sh || true"; };'$'\n'
  file="$ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js"
  safe_path "$file"
  if [ -s "$file" ]; then
    [ -f "$file" ] || die "proxmoxlib.js non e' un file normale"
    [ "$(stat -c %s -- "$file")" -le 16777216 ] || die 'proxmoxlib.js troppo grande'
    if grep -qF NoMoreNagging -- "$file"; then
      content=$(cat -- "$file" && printf '.')
      content="${content%.}"
    else
      content=$(nag_content "$file" && printf '.')
      content="${content%.}"
      [[ "$content" == *NoMoreNagging* ]] || die 'formato proxmoxlib.js non riconosciuto'
    fi
    plan_file "$file" 644 "$content"
  else
    printf 'ok: proxmoxlib.js assente o vuoto, patch rinviata al prossimo aggiornamento del toolkit\n'
  fi
}

reboot_status() {
  local kernel newest='' file
  kernel=$(uname -r)
  for file in "$ROOT/boot/"vmlinuz-*-pve; do
    [ -f "$file" ] || continue
    newest+="${file##*/vmlinuz-}"$'\n'
  done
  if [ -n "$newest" ]; then newest=$(printf '%s' "$newest" | sort -V | tail -n 1); fi
  if [ -e "$ROOT/run/reboot-required" ] || { [ -n "$newest" ] && [ "$kernel" != "$newest" ]; }; then
    printf 'Riavvio necessario: si (nessun riavvio automatico)\n'
  else
    printf 'Riavvio necessario: no\n'
  fi
}

trap 'printf "ERRORE: preparazione nodo fallita\n" >&2; exit 1' ERR
preflight
trap cleanup EXIT
if [ "$APPLY" = 1 ]; then
  # Il lock sulla cartella esistente non crea file, neppure prima della pianificazione.
  exec {prepare_lock}<"$ROOT/etc/apt/sources.list.d"
  flock -n "$prepare_lock" || die "preparazione nodo gia' in corso"
  preflight
fi
build_plan
if [ "$APPLY" = 0 ]; then
  [ "${#FILES[@]}" = 0 ] || exit 3
  exit 0
fi
for index in "${!FILES[@]}"; do
  atomic_write "${FILES[$index]}" "${MODES[$index]}" "${CONTENTS[$index]}"
done
if [ "$UPGRADE" = 1 ]; then
  apt-get update || die 'apt-get update fallito'
  DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::=--force-confdef \
    -o Dpkg::Options::=--force-confold dist-upgrade || die 'apt-get dist-upgrade fallito'
  log_change 'aggiornamento pacchetti completato'
fi
if [ "$KEEP_NAG" = 0 ]; then
  "$ROOT/usr/local/libexec/pmgr/pmgr-remove-nag.sh" || die 'patch nag fallita'
fi
build_plan
[ "${#FILES[@]}" = 0 ] || die 'verifica finale fallita'
reboot_status
