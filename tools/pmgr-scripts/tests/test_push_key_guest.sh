#!/bin/bash
# Test di pmgr-push-key-guest.sh con un qm finto, in un container per distribuzione. Richiede podman.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
command -v podman >/dev/null 2>&1 || { echo "podman non disponibile: test saltato"; exit 0; }
WORK="$(mktemp -d)"; trap 'rm -rf -- "$WORK"' EXIT
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
bash -n "$SRC/bin/pmgr-push-key-guest.sh" && ok || ko "sintassi"
cat >"$WORK/rocky_repo.sh" <<'REPO'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
REPO
declare -A IMG=([debian-13]=docker.io/library/debian:13 [ubuntu-24.04]=docker.io/library/ubuntu:24.04 [ubuntu-22.04]=docker.io/library/ubuntu:22.04 [rocky-9]=docker.io/library/rockylinux:9)
pids=()
for id in debian-13 ubuntu-24.04 ubuntu-22.04 rocky-9; do
  mkdir -p "$WORK/c-$id"; cp "$HERE/push_key_guest_inner.sh" "$WORK/rocky_repo.sh" "$SRC/bin/pmgr-push-key-guest.sh" "$WORK/c-$id/"
  ( timeout 600 podman run --rm -e WANT="$id" -v "$WORK/c-$id:/w:ro,Z" "${IMG[$id]}" bash /w/push_key_guest_inner.sh >"$WORK/c-$id.out" 2>&1; echo $? >"$WORK/c-$id.rc" ) &
  pids+=($!)
done
wait "${pids[@]}"
for id in debian-13 ubuntu-24.04 ubuntu-22.04 rocky-9; do
  cat "$WORK/c-$id.out"
  [ "$(cat "$WORK/c-$id.rc" 2>/dev/null)" = 0 ] && ok || ko "$id: scenari non superati"
done
printf 'Test push chiave via guest agent: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
