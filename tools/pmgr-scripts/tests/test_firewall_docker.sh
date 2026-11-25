#!/bin/bash
# Modulo firewall-hosting su una VM con Docker: ufw deve avere le regole per le interfacce di Docker (docker0, docker_gwbridge,
# br-+), altrimenti i contenitori non raggiungono piu' i servizi dell'host. Senza Docker quelle regole non ci sono.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; SRC="$(dirname "$HERE")"; WORK="$(mktemp -d)"
PASS=0; FAIL=0; ok() { PASS=$((PASS + 1)); }; ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
trap 'podman rm -f pfd-test >/dev/null 2>&1; rm -rf "$WORK"' EXIT
command -v podman >/dev/null 2>&1 || { echo "serve podman"; exit 1; }
mkdir -p "$WORK/bin"; printf '#!/bin/bash\ncat >"%s/payload.sh"\nexit 0\n' "$WORK" >"$WORK/bin/ssh"; chmod 755 "$WORK/bin/ssh"
PATH="$WORK/bin:$PATH" "$SRC/bin/pmgr-vm-rollout.sh" --module firewall-hosting --open 5432/tcp dummy >/dev/null 2>&1
[ -s "$WORK/payload.sh" ] || { echo "payload non creato"; exit 1; }
podman run -d --name pfd-test --cap-add=NET_ADMIN --cap-add=NET_RAW -v "$WORK:/w:ro,z" docker.io/library/ubuntu:24.04 sleep 600 >/dev/null 2>&1 || { echo "container"; exit 1; }
x() { podman exec pfd-test bash -c "$1" 2>&1; }
x 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq ufw openssh-server python3 iproute2 procps >/dev/null 2>&1; mkdir -p /run/sshd; ssh-keygen -A >/dev/null 2>&1; /usr/sbin/sshd' >/dev/null
echo "-- ufw abilitato all'avvio ma non caricato (stato reale della VM 118): ogni comando ufw fallirebbe"
x 'sed -i "s/^ENABLED=.*/ENABLED=yes/" /etc/ufw/ufw.conf; ufw status | head -1; ufw allow 1234/tcp 2>&1 | tail -1' | grep -q 'problem running' && ok || ko "lo stato di prova non riproduce l'errore"
x 'ufw delete allow 1234/tcp >/dev/null 2>&1; true' >/dev/null
echo "-- senza Docker: nessuna regola per le interfacce"
x 'bash -s -- --module firewall-hosting --check < /w/payload.sh' | grep -q docker0 && ko "senza Docker chiede la regola docker0" || ok
x 'bash -s -- --module firewall-hosting --open 5432/tcp --allow-close-ports < /w/payload.sh' >"$WORK/o"; [ "$?" = 0 ] && ok || ko "applicazione senza Docker: $(tail -3 "$WORK/o")"
[ "$(x "grep -c in_docker0 /etc/ufw/user.rules; true" | tail -1)" = 0 ] && ok || ko "regola docker0 presente senza Docker"
x "ufw --force disable >/dev/null 2>&1; true" >/dev/null
echo "-- con Docker (comando finto): --check la chiede, l'applicazione la scrive, poi e' conforme"
x 'printf "#!/bin/sh\nexit 0\n" >/usr/local/bin/docker; chmod 755 /usr/local/bin/docker' >/dev/null
x 'bash -s -- --module firewall-hosting --open 5432/tcp --check < /w/payload.sh' >"$WORK/o"; grep -q 'interfaccia docker0' "$WORK/o" && ok || ko "--check non chiede docker0: $(tail -3 "$WORK/o")"
x 'bash -s -- --module firewall-hosting --open 5432/tcp < /w/payload.sh' >"$WORK/o"; [ "$?" = 0 ] && ok || ko "applicazione con Docker: $(tail -3 "$WORK/o")"
for r in in_docker0 in_docker_gwbridge in_br-+; do x "grep -c '$r' /etc/ufw/user.rules" | grep -qx 1 && ok || ko "manca la regola $r"; done
x 'bash -s -- --module firewall-hosting --open 5432/tcp < /w/payload.sh' >"$WORK/o"; grep -q 'firewall conforme' "$WORK/o" && ok || ko "non conforme alla seconda esecuzione: $(tail -2 "$WORK/o")"
x 'ufw status | grep -E "5432"' | grep -q ALLOW && ok || ko "5432 non aperta"
printf 'Test firewall-hosting con Docker: %d riusciti, %d falliti\n' "$PASS" "$FAIL"; [ "$FAIL" = 0 ]
