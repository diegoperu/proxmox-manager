#!/bin/bash
# Firewall dell'hosting con TRAFFICO VERO: i comandi dello snippet di hosting (estratti dallo YAML cosi' come sono)
# in container con diritti di rete e porte pubblicate; dalla postazione si prova a connettersi a 22, 80, 443 (devono
# passare) e a 8080, 9090 (devono essere filtrate), prima e dopo. Il traffico arriva da un SECONDO container sulla stessa rete,
# dall'interfaccia di rete: le porte pubblicate di Podman entrano dal loopback, che il firewall lascia sempre passare. Debian/Ubuntu: ufw (container normali). Rocky:
# firewalld (container con systemd). Poi un ban di fail2ban verifica che la regola finisca nel firewall.
# Uso: test_hosting_firewall.sh [ID...]   (predefinito: tutte le release con immagine)
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
cleanup() { for c in $(podman ps -a --format '{{.Names}}' | grep '^hfw'); do podman rm -f "$c" >/dev/null 2>&1; done; podman network rm -f hfw-net >/dev/null 2>&1; podman rmi -f localhost/hfw-rocky:test >/dev/null 2>&1; rm -rf -- "$WORK"; }
trap cleanup EXIT
command -v podman >/dev/null 2>&1 || { echo "serve podman"; exit 1; }

declare -A IMG=( [debian-12]=docker.io/library/debian:12 [debian-13]=docker.io/library/debian:13
  [ubuntu-22.04]=docker.io/library/ubuntu:22.04 [ubuntu-24.04]=docker.io/library/ubuntu:24.04 [ubuntu-26.04]=docker.io/library/ubuntu:26.04
  [rocky-8]=docker.io/library/rockylinux:8 [rocky-9]=docker.io/library/rockylinux:9 [rocky-10]=docker.io/rockylinux/rockylinux:10 )
IDS=("$@"); [ "${#IDS[@]}" -gt 0 ] || IDS=(debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10)
declare -A SNIP=( [debian-12]=vendor_debian [debian-13]=vendor_debian_13 [ubuntu-22.04]=vendor_ubuntu [ubuntu-24.04]=vendor_ubuntu_24.04
  [ubuntu-26.04]=vendor_ubuntu_26.04 [rocky-8]=vendor_rocky_8 [rocky-9]=vendor_rocky_9 [rocky-10]=vendor_rocky_10 )

printf 'ssh-ed25519 AAAA t\n' >"$WORK/keys"; printf 'PasswordDiProva1\n' >"$WORK/pw"; chmod 600 "$WORK/pw"
printf 'VMID_BASE=1000\nSTORAGE=pool\nBRIDGE=lo\nNAMESERVER=10.0.0.1\nSEARCH_DOMAIN=t.local\nCI_USER=tecnico\nSSH_KEYS_FILE=%s\nPASSWORD_FILE=%s\n' "$WORK/keys" "$WORK/pw" >"$WORK/cluster.conf"
chmod 600 "$WORK/cluster.conf"; mkdir -p "$WORK/host"
PMGR_CLUSTER_CONF="$WORK/cluster.conf" "$SRC/bin/pmgr-os.sh" --render "$WORK/host" --profile hosting --all >/dev/null 2>&1 || { echo "render fallito"; exit 1; }
cat >"$WORK/rocky_repo.sh" <<'EOF'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF

# comandi del firewall estratti dallo YAML dell'hosting
extract() { # id file-uscita
  python3 - "$WORK/host/${SNIP[$1]}.yaml" "$2" "$1" <<'PYEND'
import re, sys, yaml
items = [c for c in yaml.safe_load(open(sys.argv[1]))["runcmd"] if isinstance(c, str)]
if sys.argv[3].startswith("rocky"):
    sel = [c for c in items if "firewall-offline-cmd" in c and "--add-service=http" in c]
else:
    sel = [c for c in items if re.search(r"\bufw\b", c) and not c.startswith(("apt install", "sudo dnf"))]
assert sel, "nessun comando di firewall nello snippet"
open(sys.argv[2], "wb").write(b"".join(c.encode() + b"\x01" for c in sel))
print(len(sel))
PYEND
}
wait_rule() { # contenitore comando-lista-regole ip presente|assente -> 0 se lo stato atteso arriva entro 15 s
  # L'output si raccoglie PRIMA di cercarvi dentro: con "pipefail" un "grep -q" che esce al primo riscontro fa
  # ricevere SIGPIPE a chi scrive a monte e la pipeline risulta fallita proprio quando la regola c'e'.
  local c=$1 cmd=$2 ip=$3 want=$4 i out
  for i in $(seq 1 "${WAIT_RULE_SECONDS:-15}"); do
    out="$(podman exec "$c" bash -c "$cmd" 2>/dev/null)"
    if grep -q "$ip" <<<"$out"; then [ "$want" = presente ] && return 0; else [ "$want" = assente ] && return 0; fi
    sleep 1
  done
  return 1
}
probe() { # ip porta -> open|filtered, dal container client (traffico in ingresso dall'interfaccia di rete)
  if podman exec hfw-client timeout 4 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; then echo open; else echo filtered; fi
}
ports_state() { # ip -> "open open filtered ..." per 22 80 443 8080 9090
  local out="" p
  for p in 22 80 443 8080 9090; do out="$out $(probe "$1" "$p")"; done
  echo $out
}
podman network rm -f hfw-net >/dev/null 2>&1; podman network create hfw-net >/dev/null 2>&1 || { echo "rete di prova non creata"; exit 1; }
podman run -d --name hfw-client --network hfw-net docker.io/library/debian:13 sleep 3000 >/dev/null 2>&1 || { echo "client non avviato"; exit 1; }
EXPECT_CTRL="open open open open open"
EXPECT_FW="open open open filtered filtered"

idx=0
for id in "${IDS[@]}"; do
  idx=$((idx + 1))
  c="hfw-$id"
  mkdir -p "$WORK/c-$id"
  n="$(extract "$id" "$WORK/c-$id/cmds.bin")" || { ko "$id: estrazione dei comandi"; continue; }
  echo "### $id ($n comandi di firewall dallo snippet di hosting)"
  cp "$WORK/rocky_repo.sh" "$WORK/c-$id/"
  case "$id" in
    rocky-*)
      img="localhost/hfw-rocky:test"
      podman run --name "hfwbase-$id" --network=host -v "$WORK/c-$id:/w:ro,z" "${IMG[$id]}" bash -c 'bash /w/rocky_repo.sh; dnf -y -q install epel-release >/dev/null 2>&1; dnf -y -q install systemd firewalld python3 fail2ban iproute procps-ng >/dev/null 2>&1; echo base' >/dev/null 2>&1
      podman commit -q "hfwbase-$id" "$img" >/dev/null; podman rm -f "hfwbase-$id" >/dev/null
            podman run -d --name "$c" --network hfw-net --systemd=always --cap-add=NET_ADMIN --cap-add=NET_RAW -v "$WORK/c-$id:/w:ro,z" "$img" /sbin/init >/dev/null 2>&1 || { ko "$id: container non avviabile"; continue; }
      for _ in $(seq 1 40); do podman exec "$c" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
      podman exec "$c" systemctl stop firewalld >/dev/null 2>&1
      ;;
    *)
            podman run -d --name "$c" --network hfw-net --cap-add=NET_ADMIN --cap-add=NET_RAW -v "$WORK/c-$id:/w:ro,z" "${IMG[$id]}" sleep 900 >/dev/null 2>&1 || { ko "$id: container non avviabile"; continue; }
      podman exec "$c" bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq ufw sudo python3 iproute2 fail2ban procps >/dev/null 2>&1; echo ok' >/dev/null 2>&1
      ;;
  esac
  x() { podman exec "$c" bash -c "$1" 2>&1; }
  ip="$(podman inspect -f '{{(index .NetworkSettings.Networks "hfw-net").IPAddress}}' "$c" 2>/dev/null)"
  [ -n "$ip" ] && ok || { ko "$id: indirizzo del container non trovato"; continue; }
  # ascoltatori su tutte e cinque le porte
  x 'for p in 22 80 443 8080 9090; do (cd /tmp && nohup python3 -m http.server $p --bind 0.0.0.0 >/dev/null 2>&1 &); done; sleep 2; ss -tln | grep -cE ":(22|80|443|8080|9090) "' | grep -qx 5 && ok || ko "$id: gli ascoltatori di prova non sono partiti"
  # controllo: senza firewall passa tutto (altrimenti il test non dimostra niente)
  [ "$(ports_state "$ip")" = "$EXPECT_CTRL" ] && ok || ko "$id: controllo senza firewall: $(ports_state "$ip") (atteso: tutte aperte)"
  # i comandi dello snippet, uno per volta, senza terminale
  x 'while IFS= read -r -d $'"'"'\x01'"'"' item; do sh -c "$item" </dev/null >/tmp/fw.out 2>&1 || echo "COMANDO FALLITO: ${item%%$'"'"'\n'"'"'*} -> $(tail -2 /tmp/fw.out | tr "\n" " ")"; done < /w/cmds.bin' >"$WORK/run-$id.out"
  grep -q "COMANDO FALLITO" "$WORK/run-$id.out" && ko "$id: $(grep 'COMANDO FALLITO' "$WORK/run-$id.out" | head -2)" || ok
  case "$id" in
    rocky-*)
      # come dopo il riavvio dello snippet: firewalld parte con la configurazione scritta a freddo
      x 'systemctl start firewalld; sleep 3; firewall-cmd --state' | grep -qx running && ok || ko "$id: firewalld non parte"
      svc="$(x 'firewall-cmd --zone=public --list-services')"
      for s in ssh http https; do grep -qw "$s" <<<"$svc" && ok || ko "$id: manca il servizio $s (servizi: $svc)"; done
      grep -qwE 'cockpit|dhcpv6-client' <<<"$svc" && ko "$id: cockpit/dhcpv6-client ancora aperti ($svc)" || ok
      [ "$(x 'firewall-cmd --get-default-zone')" = public ] && ok || ko "$id: zona predefinita non public"
      # seconda esecuzione, ora con firewalld ACCESO (ramo --permanent + reload): idempotente e senza chiudere nulla
      x 'while IFS= read -r -d $'"'"'\x01'"'"' item; do sh -c "$item" </dev/null >/tmp/fw.out 2>&1 || echo "FALLITO: $(tail -2 /tmp/fw.out)"; done < /w/cmds.bin' | grep -q FALLITO && ko "$id: la seconda esecuzione (firewalld acceso) fallisce" || ok
      [ "$(x 'firewall-cmd --zone=public --list-services' | tr ' ' '\n' | sort | tr '\n' ' ')" = "$(tr ' ' '\n' <<<"$svc" | sort | tr '\n' ' ')" ] && ok || ko "$id: la seconda esecuzione ha cambiato i servizi"
      ;;
    *)
      st="$(x 'ufw status verbose')"
      grep -q '^Status: active' <<<"$st" && ok || ko "$id: ufw non e' attivo: $(head -2 <<<"$st" | tr '\n' ' ')"
      grep -q 'Default: deny (incoming)' <<<"$st" && ok || ko "$id: la politica in ingresso non e' deny"
      for p in 22 80 443; do grep -qE "^$p/tcp +ALLOW IN" <<<"$st" && ok || ko "$id: manca la regola per $p/tcp"; done
      x 'grep -c . /etc/ufw/user.rules >/dev/null; grep -E "^ENABLED=" /etc/ufw/ufw.conf' | grep -qx 'ENABLED=yes' && ok || ko "$id: ufw non e' abilitato all'avvio"
      ;;
  esac
  state="$(ports_state "$ip")"
  [ "$state" = "$EXPECT_FW" ] && ok || ko "$id: con il firewall: porte 22 80 443 8080 9090 = [$state] (atteso: $EXPECT_FW)"
  echo "   porte 22 80 443 8080 9090 dall'esterno: senza firewall [$EXPECT_CTRL] -> con firewall [$state]"
  # fail2ban + firewall: un ban deve finire nelle regole
  case "$id" in
    rocky-*)
      x 'systemctl start fail2ban; sleep 3; fail2ban-client ping' | grep -q pong || { echo "   (fail2ban non parte in questo container: ban non provato)"; }
      if x 'fail2ban-client ping' | grep -q pong; then
        x 'fail2ban-client status' | grep -q sshd || x 'printf "[sshd]\nenabled = true\nbackend = polling\nlogpath = /tmp/x.log\n" > /etc/fail2ban/jail.d/zz-test.local; touch /tmp/x.log; fail2ban-client reload' >/dev/null
        x 'fail2ban-client set sshd banip 198.51.100.7' >/dev/null
        wait_rule "$c" 'firewall-cmd --list-all --zone=public; nft list ruleset 2>/dev/null' 198.51.100.7 presente && ok || ko "$id: il ban 198.51.100.7 non compare nel firewall"
        x 'fail2ban-client set sshd unbanip 198.51.100.7' >/dev/null
        wait_rule "$c" 'firewall-cmd --list-all --zone=public; nft list ruleset 2>/dev/null' 198.51.100.7 assente && ok || ko "$id: dopo lo sblocco la regola e' rimasta"
      fi
      ;;
    *)
      x 'mkdir -p /run/fail2ban; printf "[sshd]\nenabled = true\nbackend = polling\nlogpath = /tmp/x.log\n" > /etc/fail2ban/jail.d/zz-test.local; touch /tmp/x.log; fail2ban-server -b -s /run/fail2ban/f.sock -p /run/fail2ban/f.pid --logtarget /tmp/f2b.log >/dev/null 2>&1; sleep 4; fail2ban-client -s /run/fail2ban/f.sock ping' | grep -q pong && {
        x 'fail2ban-client -s /run/fail2ban/f.sock set sshd banip 198.51.100.7' >/dev/null
        act="$(x 'fail2ban-client -s /run/fail2ban/f.sock get sshd actions' | tail -1)"
        if wait_rule "$c" '(iptables-save; ip6tables-save; nft list ruleset) 2>/dev/null' 198.51.100.7 presente; then ok; echo "   ban di fail2ban con ufw attivo: azione $act, regola presente"; else
          ko "$id: il ban 198.51.100.7 (azione $act) non compare nel firewall con ufw attivo"
          echo "   --- diagnostica:"; x 'fail2ban-client -s /run/fail2ban/f.sock status sshd 2>&1 | tail -4; echo "- f2b-sshd:"; iptables -S f2b-sshd 2>&1; echo "- INPUT:"; iptables -S INPUT 2>&1 | head -8; echo "- iptables-save conta IP: $(iptables-save 2>&1 | grep -c 198.51.100.7)"; echo "- errori nel log:"; grep -iE "error|fail|warn" /tmp/f2b.log | tail -6' | sed 's/^/   | /' | cut -c1-200
        fi
        x 'fail2ban-client -s /run/fail2ban/f.sock set sshd unbanip 198.51.100.7' >/dev/null
        wait_rule "$c" '(iptables-save; ip6tables-save; nft list ruleset) 2>/dev/null' 198.51.100.7 assente && ok || ko "$id: dopo lo sblocco la regola e' rimasta"
      } || echo "   (fail2ban-server non parte in questo container: ban non provato)"
      ;;
  esac
  podman rm -f "$c" >/dev/null 2>&1
done
printf 'Test firewall hosting con traffico vero: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
