#!/bin/bash
# Modulo firewall-hosting per le VM di hosting GIA' ESISTENTI, con traffico vero (come test_hosting_firewall.sh):
# VM "vecchia" con firewall spento e un servizio dei clienti su altre porte -> controllo, rifiuto, applicazione con
# --open, idempotenza, regole del cliente conservate. Uso: test_firewall_module.sh [ID...]  (predefinito: 3 release)
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
cleanup() { for c in $(podman ps -a --format '{{.Names}}' | grep '^hfm-'); do podman rm -f "$c" >/dev/null 2>&1; done; podman network rm -f hfm-net >/dev/null 2>&1; podman rmi -f localhost/hfm-rocky:test >/dev/null 2>&1; rm -rf -- "$WORK"; }
trap cleanup EXIT
command -v podman >/dev/null 2>&1 || { echo "serve podman"; exit 1; }
declare -A IMG=( [debian-12]=docker.io/library/debian:12 [ubuntu-24.04]=docker.io/library/ubuntu:24.04 [ubuntu-26.04]=docker.io/library/ubuntu:26.04
  [rocky-9]=docker.io/library/rockylinux:9 [rocky-10]=docker.io/rockylinux/rockylinux:10 )
IDS=("$@"); [ "${#IDS[@]}" -gt 0 ] || IDS=(debian-12 ubuntu-26.04 rocky-9)

# payload del rollout (ssh finto che lo salva)
mkdir -p "$WORK/bin"; printf '#!/bin/bash\ncat >"%s/payload.sh"\nexit 0\n' "$WORK" >"$WORK/bin/ssh"; chmod 755 "$WORK/bin/ssh"
PATH="$WORK/bin:$PATH" "$SRC/bin/pmgr-vm-rollout.sh" --module firewall-hosting dummy >/dev/null 2>&1
[ -s "$WORK/payload.sh" ] || { echo "payload non creato"; exit 1; }
cat >"$WORK/rocky_repo.sh" <<'EOF'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF
podman network rm -f hfm-net >/dev/null 2>&1; podman network create hfm-net >/dev/null 2>&1 || { echo "rete non creata"; exit 1; }
podman run -d --name hfm-client --network hfm-net docker.io/library/debian:13 sleep 3000 >/dev/null 2>&1 || { echo "client non avviato"; exit 1; }
probe() { if podman exec hfm-client timeout 4 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; then echo open; else echo filtered; fi; }
state() { local out="" p; for p in "${@:2}"; do out="$out $(probe "$1" "$p")"; done; echo $out; }

for id in "${IDS[@]}"; do
  c="hfm-$id"; echo "### $id"
  cp "$WORK/rocky_repo.sh" "$WORK/payload.sh" "$WORK/"  2>/dev/null
  case "$id" in
    rocky-*)
      podman run --name "hfmbase" --network=host -v "$WORK:/w:ro,z" "${IMG[$id]}" bash -c 'bash /w/rocky_repo.sh; dnf -y -q install epel-release >/dev/null 2>&1; dnf -y -q install systemd firewalld python3 iproute procps-ng >/dev/null 2>&1' >/dev/null 2>&1
      podman commit -q hfmbase localhost/hfm-rocky:test >/dev/null; podman rm -f hfmbase >/dev/null
      podman run -d --name "$c" --network hfm-net --systemd=always --cap-add=NET_ADMIN --cap-add=NET_RAW -v "$WORK:/w:ro,z" localhost/hfm-rocky:test /sbin/init >/dev/null 2>&1 || { ko "$id: container"; continue; }
      for _ in $(seq 1 40); do podman exec "$c" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
      podman exec "$c" systemctl stop firewalld >/dev/null 2>&1
      podman exec "$c" systemctl disable firewalld >/dev/null 2>&1
      ;;
    *)
      podman run -d --name "$c" --network hfm-net --cap-add=NET_ADMIN --cap-add=NET_RAW -v "$WORK:/w:ro,z" "${IMG[$id]}" sleep 900 >/dev/null 2>&1 || { ko "$id: container"; continue; }
      podman exec "$c" bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq ufw python3 iproute2 procps >/dev/null 2>&1' >/dev/null 2>&1
      ;;
  esac
  x() { podman exec "$c" bash -c "$1" 2>&1; }
  apply() { x "bash -s -- $* < /w/payload.sh" >"$WORK/out"; RC=$?; }
  ip="$(podman inspect -f '{{(index .NetworkSettings.Networks "hfm-net").IPAddress}}' "$c")"
  # VM vecchia: firewall spento, servizi sulle porte 22, 80, 443 e sulle porte dei clienti 3306 e 8080
  x 'for p in 22 80 443 3306 8080; do (cd /tmp && nohup python3 -m http.server $p --bind 0.0.0.0 >/dev/null 2>&1 &); done; sleep 2; ss -tln | grep -cE ":(22|80|443|3306|8080) "' | grep -qx 5 && ok || ko "$id: ascoltatori"
  [ "$(state "$ip" 22 80 443 3306 8080)" = "open open open open open" ] && ok || ko "$id: controllo iniziale: $(state "$ip" 22 80 443 3306 8080)"

  echo "-- --check"
  x "bash -s -- --module firewall-hosting --check < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 3 ] && ok || ko "$id: --check su VM vecchia: uscita $RC: $(tail -3 "$WORK/out" | tr '\n' ' ')"
  grep -q '3306/tcp' "$WORK/out" && grep -q '8080/tcp' "$WORK/out" && ok || ko "$id: --check non elenca le porte che verrebbero chiuse: $(grep avviso "$WORK/out" | tr '\n' ' ')"
  [ "$(state "$ip" 22 80 443 3306 8080)" = "open open open open open" ] && ok || ko "$id: --check ha cambiato il firewall"

  echo "-- applicazione senza --open: si rifiuta"
  x "bash -s -- --module firewall-hosting < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 1 ] && grep -q 'queste porte sono in ascolto' "$WORK/out" && ok || ko "$id: doveva rifiutare (uscita $RC): $(tail -2 "$WORK/out" | tr '\n' ' ')"
  [ "$(state "$ip" 22 80 443 3306 8080)" = "open open open open open" ] && ok || ko "$id: il rifiuto ha comunque cambiato il firewall"

  echo "-- applicazione con --open 3306/tcp --allow-close-ports"
  x "bash -s -- --module firewall-hosting --open 3306/tcp --allow-close-ports < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 0 ] && ok || ko "$id: applicazione: uscita $RC: $(tail -4 "$WORK/out" | tr '\n' ' ')"
  case "$id" in rocky-*) x 'systemctl is-active firewalld' | grep -qx active || x 'systemctl start firewalld; sleep 3' >/dev/null ;; esac
  st="$(state "$ip" 22 80 443 3306 8080)"
  [ "$st" = "open open open open filtered" ] && ok || ko "$id: porte 22 80 443 3306 8080 = [$st] (attese: aperte le prime quattro, 8080 filtrata)"
  echo "   porte 22 80 443 3306 8080 prima [open open open open open] -> dopo [$st]"

  echo "-- idempotenza e --verify"
  x "bash -s -- --module firewall-hosting < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 0 ] && grep -q 'firewall conforme' "$WORK/out" && ok || ko "$id: seconda esecuzione (uscita $RC): $(tail -2 "$WORK/out" | tr '\n' ' ')"
  x "bash -s -- --module firewall-hosting --check < /w/payload.sh" >/dev/null; [ "$?" = 0 ] && ok || ko "$id: --check dopo l'applicazione"
  x "bash -s -- --verify --profile hosting < /w/payload.sh" >"$WORK/out"
  grep -q 'OK.*firewall: 22, 80 e 443 aperte' "$WORK/out" && ok || ko "$id: --verify hosting non conferma il firewall: $(grep -i 'firewall' "$WORK/out" | head -3 | tr '\n' ' ')"

  echo "-- regole aggiunte dal cliente: conservate, non contano come non conformita'"
  case "$id" in
    rocky-*) x 'firewall-cmd --permanent --zone=public --add-port=5432/tcp; firewall-cmd --reload' >/dev/null ;;
    *) x 'ufw allow 5432/tcp' >/dev/null ;;
  esac
  x '(cd /tmp && nohup python3 -m http.server 5432 --bind 0.0.0.0 >/dev/null 2>&1 &); sleep 1' >/dev/null
  x "bash -s -- --module firewall-hosting < /w/payload.sh" >"$WORK/out"; RC=$?
  [ "$RC" = 0 ] && ok || ko "$id: con una regola del cliente: uscita $RC"
  [ "$(state "$ip" 5432 8080)" = "open filtered" ] && ok || ko "$id: la regola del cliente (5432) non e' stata conservata: $(state "$ip" 5432 8080)"
  podman rm -f "$c" >/dev/null 2>&1
done
printf 'Test modulo firewall-hosting con traffico vero: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
