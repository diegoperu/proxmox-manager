#!/bin/bash
# Esegue il runcmd COMPLETO di ogni snippet (8 release, standard e hosting) in un container della release, con stub al posto
# dei comandi di sistema, e controlla che arrivi in fondo (reboot). Regressione di un difetto reale: l'"exit 0" dello script
# del firewall, incorporato nello snippet, terminava tutto il runcmd (niente pmgr-ssh-enforce, sysctl, recidive, reboot).
# Richiede podman e python3 (yaml). Uso: test_runcmd_end.sh
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
command -v podman >/dev/null 2>&1 || { echo "podman non disponibile: test saltato"; exit 0; }
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
declare -A IMG=(
  [debian-12]=docker.io/library/debian:12 [debian-13]=docker.io/library/debian:13
  [ubuntu-22.04]=docker.io/library/ubuntu:22.04 [ubuntu-24.04]=docker.io/library/ubuntu:24.04 [ubuntu-26.04]=docker.io/library/ubuntu:26.04
  [rocky-8]=docker.io/library/rockylinux:8 [rocky-9]=docker.io/library/rockylinux:9 [rocky-10]=docker.io/rockylinux/rockylinux:10
)
pids=()
for id in "${!IMG[@]}"; do
  mkdir -p "$WORK/c-$id/std" "$WORK/c-$id/host"
  for prof in std host; do
    p=standard; [ "$prof" = host ] && p=hosting
    bash "$SRC/bin/pmgr-os.sh" --render "$WORK/c-$id/$prof" --profile "$p" "$id" >"$WORK/render.out" 2>&1 || { ko "$id/$prof: render non riuscito: $(tail -2 "$WORK/render.out")"; continue; }
    python3 - "$WORK/c-$id/$prof"/*.yaml "$WORK/c-$id/run-$prof.sh" <<'PYEND' || ko "$id/$prof: estrazione del runcmd non riuscita"
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
out = open(sys.argv[2], "w")
for item in doc["runcmd"]:
    assert isinstance(item, str), "elemento runcmd non testuale"
    out.write(item.rstrip("\n") + "\n")
PYEND
  done
  cp "$HERE/runcmd_end_inner.sh" "$WORK/c-$id/"
  ( timeout 300 podman run --rm -e WANT="$id" -v "$WORK/c-$id:/w:ro,Z" "${IMG[$id]}" bash /w/runcmd_end_inner.sh >"$WORK/c-$id.out" 2>&1; echo $? >"$WORK/c-$id.rc" ) &
  pids+=($!)
done
wait "${pids[@]}"
for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
  cat "$WORK/c-$id.out"
  [ "$(cat "$WORK/c-$id.rc" 2>/dev/null)" = 0 ] && ok || ko "$id: il runcmd non supera i controlli"
done
printf 'Test runcmd completo: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
