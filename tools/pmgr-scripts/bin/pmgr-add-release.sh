#!/bin/bash
# pmgr-add-release.sh - aggiunge una nuova release di sistema operativo ai template.
# Crea os.d/<id>.conf e snippets.d/<id>.yaml (copiato da una release simile); non tocca il cluster e
# non serve root. Da lanciare dove si tiene la copia "vera" degli script, poi si sincronizza.
# Solo ASCII.
#
# Uso:
#   pmgr-add-release.sh --family ubuntu --version 26.10 --codename <nome>   [opzioni]
#   pmgr-add-release.sh --family debian --version 14    --codename <nome>   [opzioni]
#   pmgr-add-release.sh --family rocky  --version 10                        [opzioni]
#
# Opzioni:
#   --like ID        release da cui copiare corpo dello snippet e helper
#                    (predefinito: la piu' recente della stessa famiglia)
#   --slot N         SLOT (VMID = VMID_BASE + SLOT); predefinito: il primo numero oltre quelli usati
#   --url URL        indirizzo dell'immagine, se non segue lo schema solito del fornitore
#   --no-check-url   non verifica che l'immagine esista sul mirror (utile senza rete)
#   --dry-run        mostra cosa scriverebbe
set -euo pipefail

# shellcheck source=../lib/pmgr-lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/pmgr-lib.sh"

usage() { sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; }
bad() { printf 'ERRORE: %s\n' "$*" >&2; exit 2; }

FAMILY="" VERSION="" CODENAME="" LIKE="" SLOT="" URL="" CHECK_URL=1 DRY=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --family) [ "$#" -ge 2 ] || bad "--family vuole un valore"; FAMILY="$2"; shift 2 ;;
    --version) [ "$#" -ge 2 ] || bad "--version vuole un valore"; VERSION="$2"; shift 2 ;;
    --codename) [ "$#" -ge 2 ] || bad "--codename vuole un valore"; CODENAME="$2"; shift 2 ;;
    --like) [ "$#" -ge 2 ] || bad "--like vuole un valore"; LIKE="$2"; shift 2 ;;
    --slot) [ "$#" -ge 2 ] || bad "--slot vuole un valore"; SLOT="$2"; shift 2 ;;
    --url) [ "$#" -ge 2 ] || bad "--url vuole un valore"; URL="$2"; shift 2 ;;
    --no-check-url) CHECK_URL=0; shift ;;
    --dry-run) DRY=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) bad "opzione sconosciuta: $1" ;;
  esac
done

[[ "$FAMILY" =~ ^(ubuntu|debian|rocky)$ ]] || bad "--family deve essere ubuntu, debian o rocky"
case "$FAMILY" in
  ubuntu)
    [[ "$VERSION" =~ ^[0-9]{2}\.[0-9]{2}$ ]] || bad "--version Ubuntu nel formato AA.MM (es. 26.10)"
    [[ "$CODENAME" =~ ^[a-z]{3,20}$ ]] || bad "--codename Ubuntu obbligatorio (es. noble)"
    ;;
  debian)
    [[ "$VERSION" =~ ^[0-9]{1,2}$ ]] || bad "--version Debian numerica (es. 14)"
    [[ "$CODENAME" =~ ^[a-z]{3,20}$ ]] || bad "--codename Debian obbligatorio (es. trixie)"
    ;;
  rocky)
    [[ "$VERSION" =~ ^[0-9]{1,2}$ ]] || bad "--version Rocky numerica (es. 10)"
    [ -z "$CODENAME" ] || bad "--codename non serve per Rocky"
    ;;
esac
[ -z "$SLOT" ] || [[ "$SLOT" =~ ^[1-9][0-9]{0,2}$ ]] || bad "--slot deve essere un numero da 1 a 999"
[ -z "$LIKE" ] || [[ "$LIKE" =~ $PMGR_OS_ID_RE ]] || bad "--like: identificativo non valido"
[ -z "$URL" ] || [[ "$URL" =~ $PMGR_URL_RE ]] || bad "--url deve essere un indirizzo https valido"

ID="$FAMILY-$VERSION"
CONF="$PMGR_BASE/os.d/$ID.conf"
BODY="$PMGR_BASE/snippets.d/$ID.yaml"
[ ! -e "$CONF" ] || bad "$ID esiste gia' ($CONF): non lo sovrascrivo"
[ ! -e "$BODY" ] || bad "$BODY esiste gia': non lo sovrascrivo"

# SLOT: il primo oltre quelli usati (anche dalle release disabilitate)
if [ -z "$SLOT" ]; then
  max=0
  for f in "$PMGR_BASE"/os.d/*.conf; do
    [ -e "$f" ] || continue
    s="$(sed -n 's/^SLOT=//p' "$f")"
    [[ "$s" =~ ^[0-9]+$ ]] && [ "$((10#$s))" -gt "$max" ] && max=$((10#$s))
  done
  SLOT=$((max + 1))
fi
[ "$SLOT" -le 999 ] || bad "SLOT oltre 999"
for f in "$PMGR_BASE"/os.d/*.conf; do
  [ -e "$f" ] || continue
  [ "$(sed -n 's/^SLOT=//p' "$f")" != "$SLOT" ] || bad "lo SLOT $SLOT e' gia' usato da $(basename "$f" .conf)"
done

# release da cui copiare: la piu' RECENTE (numero di versione piu' alto) della stessa famiglia, non quella con lo SLOT
# piu' alto: gli SLOT seguono il VMID storico (1001-1007) e non l'ordine delle release
if [ -z "$LIKE" ]; then
  LIKE="$(for f in "$PMGR_BASE"/os.d/"$FAMILY"-*.conf; do [ -e "$f" ] && basename "$f" .conf; done | sed "s/^$FAMILY-//" | sort -V | tail -n 1)"
  [ -z "$LIKE" ] || LIKE="$FAMILY-$LIKE"
fi
[ -n "$LIKE" ] || bad "non c'e' nessuna release $FAMILY da cui copiare: indica --like"
[ -f "$PMGR_BASE/os.d/$LIKE.conf" ] && [ -f "$PMGR_BASE/snippets.d/$LIKE.yaml" ] || bad "--like $LIKE: release inesistente"
[[ "$LIKE" == "$FAMILY-"* ]] || bad "--like deve essere della stessa famiglia ($FAMILY)"

# valori secondo lo schema di ogni fornitore
case "$FAMILY" in
  ubuntu)
    LABEL="Ubuntu $VERSION LTS"
    IMAGE_FILE="$CODENAME-server-cloudimg-amd64.img"
    : "${URL:=https://cloud-images.ubuntu.com/$CODENAME/current/$IMAGE_FILE}"
    TNAME="ubuntu-${VERSION//./}-cloudinit-template"
    CK_URL="https://cloud-images.ubuntu.com/$CODENAME/current/SHA256SUMS"
    CK_ALGO=sha256
    ;;
  debian)
    LABEL="Debian $VERSION ($CODENAME)"
    IMAGE_FILE="debian-$VERSION-generic-amd64.qcow2"
    : "${URL:=https://cloud.debian.org/images/cloud/$CODENAME/latest/$IMAGE_FILE}"
    TNAME="debian-$VERSION-cloudinit-template"
    CK_URL="https://cloud.debian.org/images/cloud/$CODENAME/latest/SHA512SUMS"
    CK_ALGO=sha512
    ;;
  rocky)
    LABEL="Rocky Linux $VERSION"
    IMAGE_FILE="Rocky-$VERSION-GenericCloud-Base.latest.x86_64.qcow2"
    : "${URL:=https://dl.rockylinux.org/pub/rocky/$VERSION/images/x86_64/$IMAGE_FILE}"
    TNAME="rocky-$VERSION-cloudinit-template"
    CK_URL=""
    CK_ALGO=""
    ;;
esac
# se l'indirizzo dell'immagine e' stato dato a mano il nome del file segue l'indirizzo
IMAGE_FILE="${URL##*/}"
SNIPPET="vendor_${FAMILY}_${VERSION}.yaml"
TAGS="$FAMILY-template,$VERSION,cloudinit"
HELPERS="$(sed -n 's/^HELPERS=//p' "$PMGR_BASE/os.d/$LIKE.conf")"

TMP="$(mktemp)"
trap 'rm -f -- "$TMP"' EXIT
{
  printf '# %s - template cloud-init (ProxmoxManager). Il VMID e'"'"' VMID_BASE (cluster.conf) + SLOT.\n' "$LABEL"
  printf '# Creato con pmgr-add-release.sh (copiato da %s): controlla snippets.d/%s.yaml prima di usarlo.\n' "$LIKE" "$ID"
  printf 'FAMILY=%s\nLABEL=%s\nSLOT=%s\nENABLED=1\nIMAGE_URL=%s\nIMAGE_FILE=%s\nTEMPLATE_NAME=%s\nTAGS=%s\nSNIPPET=%s\n' \
    "$FAMILY" "$LABEL" "$SLOT" "$URL" "$IMAGE_FILE" "$TNAME" "$TAGS" "$SNIPPET"
  [ -z "$HELPERS" ] || printf 'HELPERS=%s\n' "$HELPERS"
  [ -z "$CK_URL" ] || printf 'IMAGE_CHECKSUM_URL=%s\nIMAGE_CHECKSUM_ALGO=%s\n' "$CK_URL" "$CK_ALGO"
} >"$TMP"

# il file generato deve superare le stesse regole che usano gli script
( pmgr_parse_conf "$TMP" PMGR_OS_RULES ) || bad "il file generato non supera la validazione (vedi errore sopra): non scrivo niente"

if [ "$CHECK_URL" = 1 ]; then
  if ! curl -fsI --max-time 30 -o /dev/null -- "$URL"; then
    bad "l'immagine non risponde: $URL (la release e' gia' uscita? usa --url oppure --no-check-url)"
  fi
fi

if [ "$DRY" = 1 ]; then
  echo "--- os.d/$ID.conf (non scritto, --dry-run)"
  cat -- "$TMP"
  echo "--- snippets.d/$ID.yaml: copia di snippets.d/$LIKE.yaml"
  exit 0
fi

install -m 600 -- "$TMP" "$CONF"
install -m 644 -- "$PMGR_BASE/snippets.d/$LIKE.yaml" "$BODY"
cat <<EOF
Aggiunta la release $ID (SLOT $SLOT).
  $CONF
  $BODY   (copiato da $LIKE)

Prossimi passi:
  1. apri snippets.d/$ID.yaml e controlla che i pacchetti e i comandi valgano per $LABEL
     (i nomi dei pacchetti e la sintassi di sshd cambiano tra le release);
  2. prova senza cambiare niente:   bin/pmgr-os.sh $ID --dry-run
  3. crea il template:              bin/pmgr-os.sh $ID
  4. sincronizza gli snippet sugli altri nodi/cluster.
Per escludere una release vecchia dagli aggiornamenti: ENABLED=0 nel suo file in os.d/.
EOF
