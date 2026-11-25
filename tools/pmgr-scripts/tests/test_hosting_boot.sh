#!/bin/bash
# Profilo hosting con systemd VERO (Debian 13 in un container usa e getta): esegue i comandi SSH dello
# snippet di hosting cosi' come sono scritti nello YAML, poi prova il timer e un RIAVVIO con la
# configurazione manomessa; verifica sul demone sshd in esecuzione con un tentativo di login a password.
# Richiede podman e l'immagine docker.io/library/debian:13.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
IMG=localhost/hosting-boot:test
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
cleanup() { podman rm -f hbase hboot >/dev/null 2>&1; podman rmi -f "$IMG" >/dev/null 2>&1; rm -rf -- "$WORK"; }
trap cleanup EXIT
command -v podman >/dev/null 2>&1 || { echo "serve podman"; exit 1; }

# 1. snippet di hosting di Debian 13 e comandi SSH estratti dallo YAML
printf 'ssh-ed25519 AAAA t\n' >"$WORK/keys"; printf 'PasswordDiProva1\n' >"$WORK/pw"; chmod 600 "$WORK/pw"
printf 'VMID_BASE=1000\nSTORAGE=pool\nBRIDGE=lo\nNAMESERVER=10.0.0.1\nSEARCH_DOMAIN=t.local\nCI_USER=tecnico\nSSH_KEYS_FILE=%s\nPASSWORD_FILE=%s\nPROFILE=hosting\n' "$WORK/keys" "$WORK/pw" >"$WORK/cluster.conf"
chmod 600 "$WORK/cluster.conf"; mkdir -p "$WORK/out"
PMGR_CLUSTER_CONF="$WORK/cluster.conf" "$SRC/bin/pmgr-os.sh" --render "$WORK/out" debian-13 >/dev/null 2>&1 || { echo "render fallito"; exit 1; }
python3 - "$WORK/out/vendor_debian_13.yaml" "$WORK" <<'PYEND' || exit 1
import sys, yaml
d = yaml.safe_load(open(sys.argv[1]))
items = [c for c in d["runcmd"] if isinstance(c, str)]
# esattamente quelli aggiunti dal profilo: dallo script fino a enable (come li eseguirebbe cloud-init, uno per volta)
start = next(i for i, c in enumerate(items) if "cat > /usr/local/sbin/pmgr-ssh-enforce <<" in c)
end = next(i for i, c in enumerate(items) if c.startswith("systemctl enable pmgr-ssh-enforce"))
with open(sys.argv[2] + "/cmds.bin", "wb") as f:
    for c in items[start:end + 1]:
        f.write(c.encode() + b"\x01")
print("comandi del profilo estratti:", end - start + 1)
PYEND

# 2. immagine con systemd e openssh-server
podman rm -f hbase hboot >/dev/null 2>&1
podman run --name hbase --network=host docker.io/library/debian:13 bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq systemd systemd-sysv openssh-server openssh-client dbus passwd >/dev/null 2>&1; useradd -m -s /bin/bash prova; echo "prova:PasswordDiProva1" | chpasswd; sed -i "s/^#\?PasswordAuthentication.*/PasswordAuthentication yes/" /etc/ssh/sshd_config' >/dev/null 2>&1
podman commit -q hbase "$IMG" >/dev/null && podman rm -f hbase >/dev/null
podman run -d --name hboot --systemd=always -v "$WORK:/w:ro,Z" "$IMG" /sbin/init >/dev/null 2>&1 || { echo "container con systemd non avviabile"; exit 1; }
x() { podman exec hboot bash -c "$1" 2>&1; }
wait_boot() { for _ in $(seq 1 30); do podman exec hboot systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done; sleep 2; }
# tentativo di login a password contro il demone IN ESECUZIONE: i metodi offerti sono nel messaggio di rifiuto
offered() { x "ssh -o BatchMode=yes -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 prova@127.0.0.1 true 2>&1 | grep -oE 'Permission denied \\([a-z,-]+\\)' | head -1"; }
wait_boot

# 3. prima: password SSH aperta, come una VM standard
x 'systemctl enable --now ssh >/dev/null 2>&1; sleep 1'
[ "$(offered)" = "Permission denied (publickey,password)." ] || [ "$(offered)" = "Permission denied (publickey,password)" ] && ok || ko "la VM di partenza dovrebbe offrire anche la password: '$(offered)'"

# 4. i comandi del profilo, uno per volta (come cloud-init)
python3 - "$WORK/cmds.bin" <<'PYEND' >"$WORK/cmds.sh"
import sys
for i, c in enumerate(open(sys.argv[1], "rb").read().split(b"\x01")[:-1]):
    open("/dev/stdout", "a").write("echo '--- comando %d'\n" % i)
PYEND
x 'i=0; while IFS= read -r -d $'"'"'\x01'"'"' item; do sh -c "$item" || echo "COMANDO FALLITO: ${item%%$'"'"'\n'"'"'*}"; done < /w/cmds.bin' >"$WORK/run.out"
grep -q "COMANDO FALLITO" "$WORK/run.out" && ko "un comando del profilo e' fallito: $(grep 'COMANDO FALLITO' "$WORK/run.out" | head -2)" || ok
[ "$(x 'systemctl is-enabled pmgr-ssh-enforce.service')" = enabled ] && ok || ko "servizio non abilitato"
[ "$(x 'systemctl is-enabled pmgr-ssh-enforce.timer')" = enabled ] && ok || ko "timer non abilitato"
x 'systemctl start pmgr-ssh-enforce.timer' >/dev/null
x 'systemctl list-timers pmgr-ssh-enforce.timer --no-pager' | grep -q 'pmgr-ssh-enforce.timer' && ok || ko "il timer non risulta pianificato"
x 'systemctl cat pmgr-ssh-enforce.timer' | grep -q 'OnUnitActiveSec=15min' && ok || ko "il timer non e' di 15 minuti"
x 'systemctl cat pmgr-ssh-enforce.service' | grep -q 'Before=ssh.service sshd.service' && ok || ko "il servizio non precede sshd"
x 'sshd -T -h /etc/ssh/ssh_host_ed25519_key 2>/dev/null | grep -E "^(passwordauthentication|authenticationmethods) "' | tr '\n' ' ' | grep -q 'passwordauthentication no authenticationmethods publickey' && ok || ko "valore effettivo dopo i comandi non e' a sola chiave"
x "systemctl reload ssh"; sleep 1
[ "$(offered)" = "Permission denied (publickey)." ] || [ "$(offered)" = "Permission denied (publickey)" ] && ok || ko "dopo l'applicazione il demone offre ancora la password: '$(offered)'"

# 5. un utente riapre la password a VM accesa; il timer (qui avviato a mano) la richiude
x 'sed -i "1i PasswordAuthentication yes" /etc/ssh/sshd_config; printf "PasswordAuthentication yes\nAuthenticationMethods any\n" > /etc/ssh/sshd_config.d/00-aaa.conf; systemctl reload ssh'; sleep 1
offered | grep -q password && ok || ko "la manomissione non ha riaperto la password (il test non prova niente)"
x 'systemctl start pmgr-ssh-enforce.service'; sleep 1
[ "$(offered)" = "Permission denied (publickey)." ] || [ "$(offered)" = "Permission denied (publickey)" ] && ok || ko "dopo il servizio la password e' ancora offerta: '$(offered)'"

# 6. RIAVVIO con la configurazione manomessa e sshd disposto a offrire la password al suo avvio
x 'sed -i "1i PasswordAuthentication yes" /etc/ssh/sshd_config; printf "PasswordAuthentication yes\nAuthenticationMethods any\n" > /etc/ssh/sshd_config.d/00-aaa.conf; systemctl reload ssh; sleep 1' >/dev/null
offered | grep -q password && ok || ko "prima del riavvio la password dovrebbe essere offerta"
podman restart hboot >/dev/null 2>&1; wait_boot
x 'systemctl is-active ssh' | grep -qx active && ok || ko "ssh non e' attivo dopo il riavvio"
[ "$(offered)" = "Permission denied (publickey)." ] || [ "$(offered)" = "Permission denied (publickey)" ] && ok || ko "DOPO IL RIAVVIO il demone offre la password: '$(offered)'"
x 'systemctl is-active pmgr-ssh-enforce.timer' | grep -qx active && ok || ko "il timer non e' ripartito dopo il riavvio"
# l'ordine: il servizio e' partito prima di sshd
t_enf="$(x 'systemctl show pmgr-ssh-enforce.service -p ExecMainStartTimestampMonotonic --value')"
t_ssh="$(x 'systemctl show ssh.service -p ExecMainStartTimestampMonotonic --value')"
if [ -n "$t_enf" ] && [ -n "$t_ssh" ] && [ "$t_enf" -gt 0 ] && [ "$t_enf" -lt "$t_ssh" ]; then ok; else ko "pmgr-ssh-enforce non e' partito prima di sshd (enforce=$t_enf ssh=$t_ssh)"; fi
x 'journalctl -t pmgr-ssh-enforce --no-pager 2>/dev/null | tail -3' | grep -q 'pmgr-ssh-enforce' && ok || ko "nessuna traccia nel journal"

printf 'Test hosting con riavvio (systemd vero): %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
