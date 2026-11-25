#!/bin/bash
# Modulo firewall-hosting su una VM il cui sshd NON e' sulla 22 (caso reale: sshd sulla 24 o sulla 2022, con la 22
# occupata da un contenitore del cliente), con traffico vero tra due container. Controlla che il firewall apra la porta
# di sshd (da "sshd -T"), non apra la 22 se non e' di sshd, lasci 80 e 443, e conservi una regola del cliente sulla 22.
# Uso: test_firewall_sshport.sh [ID...]  (predefinito: debian-12 ubuntu-26.04 rocky-9)
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
cleanup() { for c in $(podman ps -a --format '{{.Names}}' | grep '^hfp-'); do podman rm -f "$c" >/dev/null 2>&1; done; podman network rm -f hfp-net >/dev/null 2>&1; podman rmi -f localhost/hfp-rocky:test >/dev/null 2>&1; rm -rf "$WORK"; }
trap cleanup EXIT
command -v podman >/dev/null 2>&1 || { echo "serve podman"; exit 1; }
declare -A IMG=( [debian-12]=docker.io/library/debian:12 [ubuntu-26.04]=docker.io/library/ubuntu:26.04 [rocky-9]=docker.io/library/rockylinux:9 )
IDS=("$@"); [ "${#IDS[@]}" -gt 0 ] || IDS=(debian-12 ubuntu-26.04 rocky-9)
SSHP=2222

mkdir -p "$WORK/bin"; printf '#!/bin/bash\ncat >"%s/payload.sh"\nexit 0\n' "$WORK" >"$WORK/bin/ssh"; chmod 755 "$WORK/bin/ssh"
PATH="$WORK/bin:$PATH" "$SRC/bin/pmgr-vm-rollout.sh" --module firewall-hosting dummy >/dev/null 2>&1
[ -s "$WORK/payload.sh" ] || { echo "payload non creato"; exit 1; }
cat >"$WORK/rocky_repo.sh" <<'EOF'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF
podman network rm -f hfp-net >/dev/null 2>&1; podman network create hfp-net >/dev/null 2>&1 || { echo "rete non creata"; exit 1; }
podman run -d --name hfp-client --network hfp-net docker.io/library/debian:13 sleep 3000 >/dev/null 2>&1 || { echo "client non avviato"; exit 1; }
probe() { if podman exec hfp-client timeout 4 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; then echo open; else echo filtered; fi; }
state() { local out="" p; for p in "${@:2}"; do out="$out $(probe "$1" "$p")"; done; echo $out; }

for id in "${IDS[@]}"; do
  c="hfp-$id"; echo "### $id"
  case "$id" in
    rocky-*)
      podman run --name hfpbase --network=host -v "$WORK:/w:ro,z" "${IMG[$id]}" bash -c 'bash /w/rocky_repo.sh; dnf -y -q install epel-release >/dev/null 2>&1; dnf -y -q install systemd firewalld openssh-server python3 iproute procps-ng >/dev/null 2>&1' >/dev/null 2>&1
      podman commit -q hfpbase localhost/hfp-rocky:test >/dev/null; podman rm -f hfpbase >/dev/null
      podman run -d --name "$c" --network hfp-net --systemd=always --cap-add=NET_ADMIN --cap-add=NET_RAW -v "$WORK:/w:ro,z" localhost/hfp-rocky:test /sbin/init >/dev/null 2>&1 || { ko "$id: container"; continue; }
      for _ in $(seq 1 40); do podman exec "$c" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
      podman exec "$c" systemctl stop firewalld >/dev/null 2>&1; podman exec "$c" systemctl disable firewalld >/dev/null 2>&1
      ;;
    *)
      podman run -d --name "$c" --network hfp-net --cap-add=NET_ADMIN --cap-add=NET_RAW -v "$WORK:/w:ro,z" "${IMG[$id]}" sleep 900 >/dev/null 2>&1 || { ko "$id: container"; continue; }
      podman exec "$c" bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq ufw openssh-server python3 iproute2 procps >/dev/null 2>&1' >/dev/null 2>&1
      ;;
  esac
  x() { podman exec "$c" bash -c "$1" 2>&1; }
  ip="$(podman inspect -f '{{(index .NetworkSettings.Networks "hfp-net").IPAddress}}' "$c")"
  # sshd vero sulla $SSHP; un servizio del cliente sulla 22 (come il contenitore git di chef), uno sulle 80, 443 e uno su 8080
  x "mkdir -p /run/sshd /etc/ssh/sshd_config.d; ssh-keygen -A >/dev/null 2>&1; printf 'Port $SSHP\n' >/etc/ssh/sshd_config.d/00-port.conf; grep -q '^Include' /etc/ssh/sshd_config || sed -i '1i Include /etc/ssh/sshd_config.d/*.conf' /etc/ssh/sshd_config; sed -i 's/^Port 22\$/#Port 22/' /etc/ssh/sshd_config; /usr/sbin/sshd" >/dev/null
  x 'for p in 22 80 443 8080; do (cd /tmp && nohup python3 -m http.server $p --bind 0.0.0.0 >/dev/null 2>&1 &); done; sleep 2' >/dev/null
  [ "$(x 'sshd -T | awk "\$1==\"port\"{print \$2}"')" = "$SSHP" ] && ok || ko "$id: sshd -T non dice la porta $SSHP"
  [ "$(state "$ip" "$SSHP" 22 80 443 8080)" = "open open open open open" ] && ok || ko "$id: controllo iniziale: $(state "$ip" "$SSHP" 22 80 443 8080)"

  echo "-- --check: dice che manca la regola per $SSHP (e non per la 22)"
  x "bash -s -- --module firewall-hosting --check < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 3 ] && ok || ko "$id: --check: uscita $RC"
  grep -qE "(regola per|porta) $SSHP/tcp" "$WORK/out" && ok || ko "$id: --check non chiede la porta $SSHP: $(tail -4 "$WORK/out" | tr '\n' ' ')"
  grep -qE "(regola per|porta) 22/tcp|servizio ssh" "$WORK/out" && ko "$id: --check chiede ancora la 22 pur con sshd su $SSHP" || ok

  echo "-- applicazione (la 22 e 8080 si chiudono: --allow-close-ports)"
  x "bash -s -- --module firewall-hosting --allow-close-ports < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 0 ] && ok || ko "$id: applicazione: uscita $RC: $(tail -4 "$WORK/out" | tr '\n' ' ')"
  case "$id" in rocky-*) x 'systemctl is-active firewalld' | grep -qx active || x 'systemctl start firewalld; sleep 3' >/dev/null ;; esac
  st="$(state "$ip" "$SSHP" 80 443 22 8080)"
  # su Rocky la zona public di firewalld ha il servizio ssh di serie: il modulo non toglie regole gia' presenti (come
  # per ufw: si conserva cio' che c'e'), quindi la 22 resta aperta; su Debian/Ubuntu non c'era nessuna regola e resta filtrata
  want="open open open filtered filtered"; case "$id" in rocky-*) want="open open open open filtered" ;; esac
  [ "$st" = "$want" ] && ok || ko "$id: porte $SSHP 80 443 22 8080 = [$st] (attese [$want])"
  echo "   porte $SSHP 80 443 22 8080: [$st]"

  echo "-- idempotenza e --verify"
  x "bash -s -- --module firewall-hosting < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 0 ] && grep -q 'firewall conforme' "$WORK/out" && ok || ko "$id: seconda esecuzione (uscita $RC): $(tail -2 "$WORK/out" | tr '\n' ' ')"
  x "bash -s -- --verify --profile hosting < /w/payload.sh" >"$WORK/out"
  grep -q "OK.*firewall: $SSHP, 80 e 443 aperte" "$WORK/out" && ok || ko "$id: --verify non conferma il firewall con la porta $SSHP: $(grep -i 'firewall' "$WORK/out" | head -3 | tr '\n' ' ')"

  echo "-- regola del cliente sulla 22: conservata"
  case "$id" in
    rocky-*) x 'firewall-cmd --permanent --zone=public --add-port=22/tcp; firewall-cmd --reload' >/dev/null ;;
    *) x 'ufw allow 22/tcp' >/dev/null ;;
  esac
  x "bash -s -- --module firewall-hosting < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 0 ] && ok || ko "$id: con la regola del cliente: uscita $RC"
  [ "$(state "$ip" 22 "$SSHP" 8080)" = "open open filtered" ] && ok || ko "$id: la regola del cliente sulla 22 non e' stata conservata: $(state "$ip" 22 "$SSHP" 8080)"
  podman rm -f "$c" >/dev/null 2>&1
done
printf 'Test firewall-hosting con sshd su una porta diversa dalla 22: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
