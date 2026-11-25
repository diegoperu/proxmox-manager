#!/bin/bash
# pmgr-ssh-enforce.sh con openssh-server vero in un container per distribuzione e configurazioni ostili.
# Uso: test_ssh_enforce.sh   (richiede podman e le immagini; salta se manca podman)
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
command -v podman >/dev/null 2>&1 || { echo "(podman assente: saltato)"; exit 0; }
declare -A IMG=( [debian-12]=docker.io/library/debian:12 [debian-13]=docker.io/library/debian:13
  [ubuntu-22.04]=docker.io/library/ubuntu:22.04 [ubuntu-24.04]=docker.io/library/ubuntu:24.04
  [rocky-8]=docker.io/library/rockylinux:8 [rocky-9]=docker.io/library/rockylinux:9 [ubuntu-26.04]=docker.io/library/ubuntu:26.04 [rocky-10]=docker.io/rockylinux/rockylinux:10 )
cat >"$WORK/rocky_repo.sh" <<'EOF'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF
SCRIPT="${ENFORCE_SCRIPT:-$SRC/profiles/hosting/pmgr-ssh-enforce.sh}"
pids=()
for id in "${!IMG[@]}"; do
  mkdir -p "$WORK/c-$id"
  cp "$HERE/ssh_enforce_inner.sh" "$WORK/rocky_repo.sh" "$WORK/c-$id/"; cp "$SCRIPT" "$WORK/c-$id/enforce.sh"
  ( timeout 500 podman run --rm --network=host -e WANT="$id" -v "$WORK/c-$id:/w:ro,Z" "${IMG[$id]}" bash /w/ssh_enforce_inner.sh >"$WORK/c-$id.out" 2>&1; echo $? >"$WORK/c-$id.rc" ) &
  pids+=($!)
done
wait "${pids[@]}"
for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
  cat "$WORK/c-$id.out"
  [ "$(cat "$WORK/c-$id.rc" 2>/dev/null)" = 0 ] && ok || ko "$id: pmgr-ssh-enforce non supera gli scenari"
done
printf 'Test pmgr-ssh-enforce: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
