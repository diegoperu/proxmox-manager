#!/bin/bash
# Prova comportamentale di adduser.sh in container: l'utente aggiunto DEVE avere sudo, in tutti i casi,
# oppure non deve esistere. Uso: adduser_sudo_test.sh [immagine ...]   (richiede podman e rete)
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
IMAGES=("$@")
[ "${#IMAGES[@]}" -gt 0 ] || IMAGES=(docker.io/library/debian:13 docker.io/library/ubuntu:24.04 docker.io/library/ubuntu:26.04 docker.io/library/rockylinux:9 docker.io/rockylinux/rockylinux:10)
KEY='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA t'
for img in "${IMAGES[@]}"; do
  out="$(timeout 600 podman run --rm -v "$HERE/adduser.sh:/a.sh:ro,z" "$img" bash -c '
    if command -v apt-get >/dev/null; then apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq sudo passwd >/dev/null 2>&1
    else sed -i "s|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|" /etc/yum.repos.d/*.repo 2>/dev/null; dnf -y -q install sudo shadow-utils >/dev/null 2>&1; fi
    can() { sudo -l -U "$1" 2>/dev/null | grep -q "(ALL"; }
    # 1. ramo password e ramo chiave: sudo vero
    sh /a.sh pw1 "" >/dev/null 2>&1; echo "pw1 rc=$? sudo=$(can pw1 && echo si || echo no)"
    sh /a.sh key1 "'"$KEY"'" >/dev/null 2>&1; echo "key1 rc=$? sudo=$(can key1 && echo si || echo no)"
    # 2. regole di gruppo di /etc/sudoers tolte: sudo deve funzionare lo stesso (file dedicato)
    sed -i -E "s/^%(sudo|wheel)/#&/" /etc/sudoers
    sh /a.sh pw2 "" >/dev/null 2>&1; echo "pw2-senza-regola-gruppo rc=$? sudo=$(can pw2 && echo si || echo no)"
    # 3. utente gia esistente: nessuna modifica (rc 3)
    sh /a.sh pw2 "" >/dev/null 2>&1; echo "esistente rc=$?"
    # 4. sudo assente: rc 4 e utente NON creato
    # (togliere il pacchetto dentro un container fallisce: si cancellano i programmi, stesso effetto per lo script)
    rm -f /usr/bin/sudo /usr/sbin/visudo /bin/sudo /sbin/visudo
    sh /a.sh nosudo "" >/dev/null 2>&1; echo "senza-sudo rc=$? esiste=$(id nosudo >/dev/null 2>&1 && echo si || echo no)"
  ' 2>&1)"
  echo "### $img"; echo "$out" | sed 's/^/   /'
  grep -q 'pw1 rc=0 sudo=si' <<<"$out" && ok || ko "$img: ramo password senza sudo"
  grep -q 'key1 rc=0 sudo=si' <<<"$out" && ok || ko "$img: ramo chiave senza sudo"
  grep -q 'pw2-senza-regola-gruppo rc=0 sudo=si' <<<"$out" && ok || ko "$img: senza la regola di gruppo l'utente non ha sudo"
  grep -q 'esistente rc=3' <<<"$out" && ok || ko "$img: utente esistente non rifiutato"
  grep -q 'senza-sudo rc=4 esiste=no' <<<"$out" && ok || ko "$img: senza sudo l'utente e' stato creato o l'uscita non e' 4"
done
printf 'Test adduser sudo: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
