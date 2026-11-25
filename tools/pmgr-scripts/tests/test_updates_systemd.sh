#!/bin/bash
# Aggiornamenti automatici con systemd VERO (Ubuntu 24.04 e Rocky 9 in container usa e getta): orari reali dei timer
# (due al giorno, quelli di serie azzerati), due esecuzioni ravvicinate dell'installazione (la seconda non deve essere
# saltata), avviso di riavvio su un vero login SSH. Richiede podman. Applica pmgr-vm-apply.sh via il payload del rollout.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
cleanup() { podman rm -f usb usd >/dev/null 2>&1; podman rmi -f localhost/upd-ubuntu:test localhost/upd-rocky:test >/dev/null 2>&1; rm -rf -- "$WORK"; }
trap cleanup EXIT
command -v podman >/dev/null 2>&1 || { echo "serve podman"; exit 1; }
cleanup 2>/dev/null; WORK="$(mktemp -d)"

# payload del rollout (con ssh finto che lo salva)
mkdir -p "$WORK/bin"; printf '#!/bin/bash\ncat >"%s/payload.sh"\nexit 0\n' "$WORK" >"$WORK/bin/ssh"; chmod 755 "$WORK/bin/ssh"
PATH="$WORK/bin:$PATH" "$SRC/bin/pmgr-vm-rollout.sh" --module updates dummy >/dev/null 2>&1
[ -s "$WORK/payload.sh" ] || { echo "payload non creato"; exit 1; }
cat >"$WORK/rocky_repo.sh" <<'EOF'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF
ssh-keygen -q -t ed25519 -N '' -f "$WORK/k"

run_case() { # nome immagine famiglia
  local name="$1" image="$2" fam="$3" c="u$4" cname
  cname="$c"
  echo "### $name"
  podman rm -f "$cname" >/dev/null 2>&1
  local img="localhost/upd-$fam:test"
  if [ "$fam" = ubuntu ]; then
    podman run --name "${cname}base" --network=host -v "$WORK:/w:ro,Z" "$image" bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq systemd systemd-sysv openssh-server openssh-client dbus python3 >/dev/null 2>&1; rm -f /etc/apt/apt.conf.d/docker-*; useradd -m -s /bin/bash prova; mkdir -p -m 700 /home/prova/.ssh; cp /w/k.pub /home/prova/.ssh/authorized_keys; chown -R prova:prova /home/prova/.ssh; mkdir -p /run/sshd' >/dev/null 2>&1
  else
    podman run --name "${cname}base" --network=host -v "$WORK:/w:ro,Z" "$image" bash -c 'bash /w/rocky_repo.sh; dnf -y -q install systemd openssh-server openssh-clients python3 passwd >/dev/null 2>&1; useradd -m -s /bin/bash prova; mkdir -p -m 700 /home/prova/.ssh; cp /w/k.pub /home/prova/.ssh/authorized_keys; chown -R prova:prova /home/prova/.ssh; chmod 600 /home/prova/.ssh/authorized_keys' >/dev/null 2>&1
  fi
  podman commit -q "${cname}base" "$img" >/dev/null; podman rm -f "${cname}base" >/dev/null
  podman run -d --name "$cname" --systemd=always -v "$WORK:/w:ro,Z" "$img" /sbin/init >/dev/null 2>&1 || { ko "$name: container con systemd non avviabile"; return; }
  for _ in $(seq 1 40); do podman exec "$cname" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
  x() { podman exec "$cname" bash -c "$1" 2>&1; }

  x 'bash -s -- --module updates < /w/payload.sh' >"$WORK/apply.out"
  grep -q 'Fatto' "$WORK/apply.out" && ok || ko "$name: pmgr-vm-apply: $(tail -3 "$WORK/apply.out" | tr '\n' ' ')"
  if [ "$fam" = ubuntu ]; then T1=apt-daily; T2=apt-daily-upgrade; else T1=dnf-automatic; T2=dnf-automatic; fi
  for t in $T1 $T2; do
    cal="$(x "systemctl show $t.timer -p TimersCalendar --value")"
    n=$(grep -oE 'OnCalendar=[^;]+' <<<"$cal" | wc -l)
    [ "$n" = 2 ] && ok || ko "$name: $t.timer ha $n orari (attesi 2): $cal"
    grep -q '06:00\|04:30\|16:30\|18:00' <<<"$cal" && ! grep -q '6,18' <<<"$cal" && ok || ko "$name: $t.timer ha ancora gli orari di serie: $cal"
    x "systemctl is-enabled $t.timer" | grep -qx enabled && ok || ko "$name: $t.timer non abilitato"
    x "systemctl list-timers $t.timer --no-pager" | grep -q "$t.timer" && ok || ko "$name: $t.timer non pianificato"
  done
  echo "   $(x "systemctl show $T2.timer -p NextElapseUSecRealtime --value") (prossima esecuzione di $T2.timer)"
  if [ "$fam" = ubuntu ]; then
    # due esecuzioni ravvicinate dell'installazione: con "always" la seconda NON deve essere saltata
    x 'systemctl start apt-daily.service; systemctl start apt-daily-upgrade.service' >/dev/null
    x 'systemctl start apt-daily.service; systemctl start apt-daily-upgrade.service' >/dev/null
    runs=$(x 'grep -c "Starting unattended upgrades script" /var/log/unattended-upgrades/unattended-upgrades.log')
    [ "${runs:-0}" -ge 2 ] && ok || ko "$name: l'installazione e' partita ${runs:-0} volte su 2 (la seconda e' stata saltata?)"
    x 'systemctl show apt-daily-upgrade.service -p Result --value' | grep -qx success && ok || ko "$name: apt-daily-upgrade.service non e' terminato con successo"
    x 'grep "Allowed origins" /var/log/unattended-upgrades/unattended-upgrades.log | tail -1' | grep -q 'noble-security' && ok || ko "$name: il giro non usa le origini di sicurezza"
  fi
  # avviso di riavvio su un vero login SSH (chiave), con un terminale
  x 'systemctl enable --now ssh >/dev/null 2>&1 || systemctl enable --now sshd >/dev/null 2>&1; sleep 2; printf "motivo di prova\n" > /run/pmgr-reboot-required'
  podman exec -i "$cname" bash -c 'cp /w/k /tmp/k; chmod 600 /tmp/k' >/dev/null 2>&1
  out="$(podman exec "$cname" bash -c "printf 'exit\\n' | ssh -tt -i /tmp/k -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes prova@127.0.0.1 2>&1" | tr -d '\r')"
  grep -q 'riavvio necessario' <<<"$out" && ok || ko "$name: il login SSH non mostra l'avviso: [$(head -c 300 <<<"$out" | tr '\n' ' ')]"
  grep -q 'sudo reboot' <<<"$out" && ok || ko "$name: l'avviso non dice cosa fare"
  x 'rm -f /run/pmgr-reboot-required'
  out="$(podman exec "$cname" bash -c "printf 'exit\\n' | ssh -tt -i /tmp/k -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes prova@127.0.0.1 2>&1" | tr -d '\r')"
  grep -q 'riavvio necessario' <<<"$out" && ko "$name: l'avviso compare anche senza motivo" || ok
  # un comando via SSH (non interattivo) non deve essere sporcato dall'avviso
  x 'printf "motivo\n" > /run/pmgr-reboot-required'
  res="$(podman exec "$cname" bash -c 'ssh -i /tmp/k -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes prova@127.0.0.1 "echo OK" 2>/dev/null')"
  [ "$res" = OK ] && ok || ko "$name: l'output di un comando via SSH e' sporco: [$res]"
  podman rm -f "$cname" >/dev/null 2>&1
}
run_case "Ubuntu 24.04" docker.io/library/ubuntu:24.04 ubuntu usb
run_case "Rocky 9" docker.io/library/rockylinux:9 rocky usd
printf 'Test aggiornamenti con systemd vero: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
