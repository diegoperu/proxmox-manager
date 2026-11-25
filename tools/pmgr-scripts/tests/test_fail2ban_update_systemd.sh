#!/bin/bash
# Prova del percorso del SERVIZIO di pmgr-fail2ban-update.sh con un systemd vero (Debian 13 in un
# container usa e getta, rootless): servizio fermo e disabilitato (come su Rocky), aggiornamento,
# valori caricati dal demone, idempotenza, e ritorno indietro quando il riavvio fallisce.
# Richiede podman e l'immagine docker.io/library/debian:13 (installa systemd e fail2ban nel container).
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPD="$(dirname "$HERE")/bin/pmgr-fail2ban-update.sh"
IMG=localhost/f2b-systemd:test
PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
cleanup() { podman rm -f f2bbase f2bsys >/dev/null 2>&1; podman rmi -f "$IMG" >/dev/null 2>&1; }
trap cleanup EXIT
command -v podman >/dev/null 2>&1 || { echo "serve podman"; exit 1; }
cleanup

podman run --name f2bbase --network=host docker.io/library/debian:13 bash -c \
  'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq systemd systemd-sysv fail2ban ufw python3 dbus procps >/dev/null 2>&1' >/dev/null 2>&1
podman commit -q f2bbase "$IMG" >/dev/null && podman rm -f f2bbase >/dev/null
podman run -d --name f2bsys --systemd=always --network=host -v "$UPD:/u.sh:ro,Z" "$IMG" /sbin/init >/dev/null 2>&1 || { echo "container con systemd non avviabile"; exit 1; }
for _ in $(seq 1 20); do podman exec f2bsys systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
x() { podman exec f2bsys bash -c "$1" 2>&1; }

old_vm='J=/etc/fail2ban/jail.local; rm -f /etc/fail2ban/jail.d/50-pmgr.local /etc/fail2ban/fail2ban.d/50-pmgr.local /etc/fail2ban/jail.d/zz-fail.local; rm -rf /var/backups/pmgr-fail2ban /etc/systemd/system/fail2ban.service.d; systemctl daemon-reload
cp /etc/fail2ban/jail.conf $J; sed -i -e "/^bantime/s/^.*\$/bantime = 1d/" $J; cp $J /tmp/orig.local'

# --- 1. VM vecchia, servizio fermo e disabilitato
x "$old_vm; systemctl restart fail2ban; sleep 2" >/dev/null
[ "$(x 'fail2ban-client get sshd bantime')" = 86400 ] && ok || ko "la VM vecchia non ha bantime 86400"
x 'systemctl disable --now fail2ban' >/dev/null
x 'bash /u.sh --check >/dev/null; echo $?' | grep -qx 3 && ok || ko "--check con servizio fermo: uscita attesa 3"
x 'bash /u.sh' >"${TMPDIR:-/tmp}/f2b_sys.out"; x 'echo' >/dev/null
rc="$(x 'bash /u.sh >/dev/null; echo $?')"
[ "$rc" = 0 ] && ok || ko "seconda esecuzione: uscita $rc (attesa 0)"
[ "$(x 'systemctl is-enabled fail2ban')" = enabled ] && ok || ko "fail2ban non abilitato all'avvio"
[ "$(x 'systemctl is-active fail2ban')" = active ] && ok || ko "fail2ban non in esecuzione"
for kv in "bantime:3600" "findtime:900" "maxretry:5" "bantime.increment:True" "bantime.multipliers:1 2 4 8 16 32 64" "bantime.maxtime:604800"; do
  k="${kv%%:*}"; v="${kv#*:}"
  [ "$(x "fail2ban-client get sshd $k")" = "$v" ] && ok || ko "il demone ha $k = '$(x "fail2ban-client get sshd $k")' (atteso '$v')"
done
x 'fail2ban-client get dbpurgeage' | grep -q 2419200 && ok || ko "dbpurgeage non e' 4 settimane"
x 'fail2ban-client get sshd actions' | grep -q nftables && ok || ko "l'azione della distribuzione (nftables) non e' tornata"
x 'test ! -e /etc/fail2ban/jail.local && echo si' | grep -qx si && ok || ko "vecchio jail.local ancora presente"
x 'bash /u.sh | tail -1' | grep -q "Niente da fare" && ok || ko "terza esecuzione non idempotente"

# --- 1b. fail2ban ripartira' da solo se ucciso (caso reale: un utente lancia "sudo pkill -f python")
[ "$(x 'systemctl show fail2ban -p Restart --value')" = always ] && ok || ko "Restart=always non applicato"
x 'test -f /etc/systemd/system/fail2ban.service.d/50-pmgr.conf && echo si' | grep -qx si && ok || ko "drop-in systemd mancante"
x 'p=pyt; p=${p}hon; pkill -f "$p"; sleep 1; pkill -f "$p"; sleep 1; pkill -f "$p"; echo fatto' >/dev/null
back=no
for _ in $(seq 1 40); do [ "$(x 'systemctl is-active fail2ban')" = active ] && x 'fail2ban-client status sshd >/dev/null 2>&1 && echo si' | grep -qx si && { back=si; break; }; sleep 2; done
[ "$back" = si ] && ok || ko "fail2ban non e' ripartito dopo pkill -f python (3 volte di fila)"
st="$(x 'systemctl stop fail2ban; sleep 25; systemctl is-active fail2ban' | tail -1)"
[ "$st" = inactive ] && ok || ko "un arresto voluto non deve ripartire (stato dopo 25 s: $st)"
x 'systemctl start fail2ban; sleep 3; systemctl is-active fail2ban' | tail -1 | grep -qx active && ok || ko "avvio manuale dopo lo stop non riuscito"

# --- 2. il riavvio fallisce: ritorno indietro
x "$old_vm; systemctl restart fail2ban; sleep 2" >/dev/null
x 'printf "[zzfail]\nenabled = true\nfilter = sshd\nbackend = polling\nlogpath = /nonexistent/zz.log\n" > /etc/fail2ban/jail.d/zz-fail.local' >/dev/null
rc="$(x 'bash /u.sh >/tmp/rb.out 2>&1; echo $?')"
[ "$rc" = 1 ] && ok || ko "riavvio fallito: uscita $rc (attesa 1)"
x 'grep -c "configurazione ripristinata" /tmp/rb.out' | grep -qx 1 && ok || ko "il messaggio non dice che e' tutto ripristinato"
x 'ls /etc/fail2ban/jail.d/50-pmgr.local /etc/fail2ban/fail2ban.d/50-pmgr.local 2>/dev/null | wc -l' | grep -qx 0 && ok || ko "dopo il ritorno indietro restano file nostri"
x 'test ! -e /etc/systemd/system/fail2ban.service.d/50-pmgr.conf && echo si' | grep -qx si && ok || ko "dopo il ritorno indietro resta il drop-in systemd"
[ "$(x 'systemctl show fail2ban -p Restart --value')" != always ] && ok || ko "dopo il ritorno indietro systemd tiene ancora Restart=always in memoria"
x '[ "$(sha256sum < /etc/fail2ban/jail.local)" = "$(sha256sum < /tmp/orig.local)" ] && echo si' | grep -qx si && ok || ko "jail.local non ripristinato identico"
x 'rm -f /etc/fail2ban/jail.d/zz-fail.local; systemctl restart fail2ban; sleep 2; systemctl is-active fail2ban' | grep -qx active && ok || ko "senza il guasto fail2ban dovrebbe ripartire"

printf 'Test servizio fail2ban (systemd vero): %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
