#!/bin/bash
# pmgr-node-install.sh - installazione "leggera" del nuovo tooling su un nodo Proxmox (ProxmoxManager). Solo ASCII.
# Da eseguire come root SUL NODO che tiene gli snippet (il nodo sorgente degli snippet del cluster), dopo aver estratto il pacchetto
# degli script in una cartella di root (es. /root/pmgr-scripts).
#
# Uso:  pmgr-node-install.sh            SOLA LETTURA: dice cosa farebbe (predefinito)
#       pmgr-node-install.sh --apply    scrive
#
# Cosa fa (con --apply):
#   1. /etc/pmgr/cluster.conf  : lo crea (root, 0600) SOLO se manca, con i valori di CALCOLO letti dai vecchi script.
#                                Se esiste non lo tocca.
#   2. /etc/pmgr/ssh_keys.pub  : lo crea SOLO se manca, dalle chiavi gia' usate dai vecchi script
#                                (/root/cloud-images/ssh_pub_auth_keys/auth), ogni riga validata con ssh-keygen.
#   3. snippet cloud-init      : genera gli otto snippet "standard" in una cartella temporanea, li confronta con quelli
#                                gia' presenti in /var/lib/vz/snippets, fa una COPIA DI SICUREZZA (tar) dei file che
#                                sostituisce in /root/pmgr-backup/ (0700) e li sostituisce. I file nuovi si aggiungono.
# Cosa NON fa: non crea ne' cancella template o VM, non crea la password dei template, non copia sugli altri nodi
# (si usa il vecchio rsync_snippets.sh o, dopo, pmgr-snippets-sync), non tocca nessun altro file.
# Variabili solo per i test: PMGR_TEST=1 (niente controllo root/Proxmox), PMGR_ETC, PMGR_SNIPPETS_DIR, PMGR_BACKUP_DIR, PMGR_OLD_KEYS.
set -euo pipefail
export LC_ALL=C
umask 077

APPLY=0
case "${1:-}" in
  "") ;;
  --apply) APPLY=1 ;;
  -h | --help) sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *) echo "argomento sconosciuto: $1" >&2; exit 2 ;;
esac
[ "$#" -le 1 ] || { echo "troppi argomenti" >&2; exit 2; }

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ETC="${PMGR_ETC:-/etc/pmgr}"
SNIP="${PMGR_SNIPPETS_DIR:-/var/lib/vz/snippets}"
BACKUP_ROOT="${PMGR_BACKUP_DIR:-/root/pmgr-backup}"
OLD_KEYS="${PMGR_OLD_KEYS:-}"
RENDER="$HERE/bin/pmgr-os.sh"
OWN=()
[ "$(id -u)" = 0 ] && OWN=(-o root -g root)

say() { printf '%s\n' "$*"; }
die() { printf 'ERRORE: %s\n' "$*" >&2; exit 1; }

if [ "${PMGR_TEST:-0}" != 1 ]; then
  [ "$(id -u)" = 0 ] || die "va eseguito come root sul nodo Proxmox"
  command -v pvesm >/dev/null 2>&1 || die "pvesm non trovato: questo non sembra un nodo Proxmox"
fi
[ -x "$RENDER" ] || die "manca $RENDER: estrai tutto il pacchetto degli script, non un file solo"
[ -d "$SNIP" ] && [ ! -L "$SNIP" ] || die "la cartella degli snippet non esiste (o e' un link simbolico): $SNIP"
for c in sha256sum tar install mktemp ssh-keygen; do command -v "$c" >/dev/null 2>&1 || die "manca il comando $c"; done

say "Modo: $([ "$APPLY" = 1 ] && echo SCRITTURA || echo 'sola lettura (aggiungi --apply per scrivere)')"
say "Pacchetto script: $HERE"
say "Snippet:          $SNIP"
CHANGES=0

# ---- 1. cluster.conf
say ""
say "[1] $ETC/cluster.conf"
if [ -e "$ETC/cluster.conf" ]; then
  say "    esiste gia': non lo tocco"
else
  die "manca $ETC/cluster.conf: crealo a mano (root, 0600) partendo da $HERE/cluster.conf.example con i valori di QUESTO cluster"
fi

# ---- 2. ssh_keys.pub
say ""
say "[2] $ETC/ssh_keys.pub"
if [ -e "$ETC/ssh_keys.pub" ]; then
  say "    esiste gia': non lo tocco"
elif [ -z "$OLD_KEYS" ] || [ ! -f "$OLD_KEYS" ] || [ -L "$OLD_KEYS" ]; then
  say "    manca: lo crei tu (una chiave pubblica per riga) quando serve per i template; non serve per gli snippet"
else
  n=0
  tmpk="$(mktemp)"
  while IFS= read -r line || [ -n "$line" ]; do
    [ -n "$line" ] || continue
    case "$line" in \#*) continue ;; esac
    printf '%s\n' "$line" | ssh-keygen -lf - >/dev/null 2>&1 || { rm -f "$tmpk"; die "riga non valida in $OLD_KEYS: non la copio"; }
    printf '%s\n' "$line" >>"$tmpk"
    n=$((n + 1))
  done <"$OLD_KEYS"
  [ "$n" -ge 1 ] || { rm -f "$tmpk"; die "nessuna chiave valida in $OLD_KEYS"; }
  CHANGES=$((CHANGES + 1))
  say "    manca: lo creo con le $n chiavi del file indicato (tutte valide)"
  if [ "$APPLY" = 1 ]; then
    install -d -m 755 "${OWN[@]}" -- "$ETC"
    install -m 600 "${OWN[@]}" -- "$tmpk" "$ETC/ssh_keys.pub"
    say "    [fatto]"
  fi
  rm -f "$tmpk"
fi

# ---- 3. snippet
say ""
say "[3] snippet cloud-init (profilo standard)"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
mkdir -p -- "$WORK/new"
"$RENDER" --render "$WORK/new" --profile standard --all >"$WORK/render.log" 2>&1 || { cat "$WORK/render.log" >&2; die "generazione degli snippet fallita: non scrivo niente"; }
mapfile -t NEWFILES < <(find "$WORK/new" -maxdepth 1 -type f -name 'vendor_*.yaml' -printf '%f\n' | sort)
[ "${#NEWFILES[@]}" -ge 8 ] || die "attesi almeno 8 snippet generati, trovati ${#NEWFILES[@]}: non scrivo niente"
TOREPLACE=()
for f in "${NEWFILES[@]}"; do
  [[ "$f" =~ ^vendor_[A-Za-z0-9._]+\.yaml$ ]] || die "nome di file inatteso: $f"
  LC_ALL=C grep -qP '[^\x09\x0a\x20-\x7e]' "$WORK/new/$f" && die "lo snippet $f contiene caratteri non ASCII: non scrivo niente"
  if [ ! -e "$SNIP/$f" ]; then
    say "    NUOVO      $f"
    TOREPLACE+=("$f")
  elif [ "$(sha256sum <"$SNIP/$f")" = "$(sha256sum <"$WORK/new/$f")" ]; then
    say "    uguale     $f"
  else
    say "    DIVERSO    $f   (attuale $(stat -c %s "$SNIP/$f") byte, nuovo $(stat -c %s "$WORK/new/$f") byte)"
    TOREPLACE+=("$f")
  fi
done
CHANGES=$((CHANGES + ${#TOREPLACE[@]}))
if [ "${#TOREPLACE[@]}" -gt 0 ] && [ "$APPLY" = 1 ]; then
  stamp="$(date +%Y%m%d-%H%M%S)"
  install -d -m 700 "${OWN[@]}" -- "$BACKUP_ROOT"
  bk="$BACKUP_ROOT/snippets-$stamp.tar.gz"
  existing=()
  for f in "${TOREPLACE[@]}"; do [ -e "$SNIP/$f" ] && existing+=("$f"); done
  if [ "${#existing[@]}" -gt 0 ]; then
    tar -C "$SNIP" -czf "$bk" -- "${existing[@]}"
    chmod 600 "$bk"
    tar -tzf "$bk" >/dev/null || die "la copia di sicurezza non si legge: non sostituisco niente"
    say "    copia di sicurezza: $bk (${#existing[@]} file)"
  fi
  for f in "${TOREPLACE[@]}"; do
    install -m 644 "${OWN[@]}" -- "$WORK/new/$f" "$SNIP/.$f.pmgr-new"
    mv -f -- "$SNIP/.$f.pmgr-new" "$SNIP/$f"
    say "    [fatto]    $f"
  done
fi

say ""
if [ "$CHANGES" = 0 ]; then
  say "Niente da fare: tutto gia' a posto."
elif [ "$APPLY" = 1 ]; then
  say "Fatto: $CHANGES modifiche."
  say "Prossimi passi: copiare gli snippet sugli altri nodi (pmgr-snippets-sync o, a mano, rsync -au)."
  say "Per tornare indietro: tar -xzf <copia di sicurezza> -C $SNIP"
else
  say "Sola lettura: $CHANGES modifiche da fare. Per scrivere: $0 --apply"
fi
