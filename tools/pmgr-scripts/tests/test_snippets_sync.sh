#!/bin/bash
# Test di pmgr-snippets-sync.sh con tre container Debian 13: due DESTINAZIONI vere (sshd, rsync e il gate di
# pmgr-sync-setup.sh; alfa = profilo standard su 2222, beta = hosting su 2223) e una SORGENTE che esegue
# lo strumento come utente pmgr-sync. Richiede podman. Uso: test_snippets_sync.sh
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
REPO="$(cd "$SRC/../.." && pwd)"
IMAGE="${PMGR_TEST_IMAGE:-docker.io/library/debian:13}"
command -v podman >/dev/null 2>&1 || { echo "podman non disponibile: test saltato"; exit 0; }
WORK="$(mktemp -d)"
cleanup() { podman rm -f pmgr-sync-t-alfa pmgr-sync-t-beta >/dev/null 2>&1 || true; rm -rf -- "$WORK"; }
trap cleanup EXIT
ssh-keygen -q -t ed25519 -N '' -f "$WORK/key"
cp "$HERE/snippets_sync_dest_inner.sh" "$HERE/snippets_sync_src_inner.sh" "$WORK/"
for spec in "alfa 2222" "beta 2223"; do
  set -- $spec
  podman run -d --name "pmgr-sync-t-$1" --network=host -e PORT="$2" \
    -v "$REPO/tools/pmgr-sync:/t:ro,z" -v "$WORK:/w:z" "$IMAGE" bash /w/snippets_sync_dest_inner.sh >/dev/null || { echo "avvio destinazione $1 non riuscito"; exit 1; }
done
for i in $(seq 1 120); do [ -e "$WORK/ready-2222" ] && [ -e "$WORK/ready-2223" ] && break; sleep 2; done
[ -e "$WORK/ready-2222" ] && [ -e "$WORK/ready-2223" ] || { echo "le destinazioni non sono pronte"; for c in alfa beta; do echo "-- $c:"; podman logs pmgr-sync-t-$c 2>&1 | tail -n 6; done; exit 1; }
timeout 900 podman run --rm --network=host \
  -v "$REPO/tools/pmgr-sync:/t:ro,z" -v "$SRC:/scripts:ro,z" -v "$WORK:/w:z" \
  "$IMAGE" bash /w/snippets_sync_src_inner.sh
RC=$?
# controlli dal lato delle destinazioni (proprietario e permessi dei file consegnati)
PASS=0; FAIL=0
chk() { if [ "$2" = "$3" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo "FALLITO: $1 (atteso '$3', trovato '$2')"; fi; }
for c in alfa beta; do
  X() { podman exec "pmgr-sync-t-$c" "$@" 2>&1; }
  chk "$c: script eseguibile, di pmgr-sync" "$(X stat -c '%U:%a' /var/lib/vz/pmgr-scripts/bin/pmgr-os.sh)" "pmgr-sync:755"
  chk "$c: file normale 644, di pmgr-sync" "$(X stat -c '%U:%a' /var/lib/vz/pmgr-scripts/README.md)" "pmgr-sync:644"
  chk "$c: snippet 644" "$(X stat -c '%a' /var/lib/vz/snippets/vendor_ubuntu_26.04.yaml)" "644"
  chk "$c: cartelle 775 root:pmgr-sync" "$(X stat -c '%U:%G:%a' /var/lib/vz/snippets /var/lib/vz/pmgr-scripts | tr '\n' ' ')" "root:pmgr-sync:2775 root:pmgr-sync:2775 "
  chk "$c: l'extra di root e' ancora li" "$(X cat /var/lib/vz/snippets/extra.yaml)" "solo sulla destinazione"
  chk "$c: nessun file segreto o immagine arrivato" "$(X find /var/lib/vz -name 'cluster.conf' -o -name '*.img' -o -name '*.iso' -o -name 'template-password' -o -name 'ssh_keys.pub' | wc -l)" "0"
  chk "$c: nessun file fuori da snippets e pmgr-scripts" "$(X find /var/lib/vz -mindepth 1 -maxdepth 1 | sort | tr '\n' ' ')" "/var/lib/vz/pmgr-scripts /var/lib/vz/snippets "
done
printf 'Destinazioni (permessi e proprieta\x27): %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$RC" = 0 ] && [ "$FAIL" = 0 ]
