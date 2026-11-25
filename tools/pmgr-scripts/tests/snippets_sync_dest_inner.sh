#!/bin/bash
# Gira DENTRO un container "destinazione": nodo con pmgr-sync (ruolo dest), gate e sshd veri.
# Variabili: PORT (porta di sshd). Segnala "pronto" creando /w/ready-$PORT.
set -euo pipefail
exec </dev/null
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq rsync openssh-server openssh-client util-linux procps python3 >/dev/null 2>&1
mkdir -p /var/lib/vz/snippets /run/sshd
# stato preesistente: un vecchio snippet diverso e uno in piu' (mai cancellato), di root
printf 'vecchio\n' >/var/lib/vz/snippets/vendor_ubuntu_26.04.yaml
printf 'solo sulla destinazione\n' >/var/lib/vz/snippets/extra.yaml
bash /t/pmgr-sync-setup.sh --role dest --source-pubkey /w/key.pub --source-from 127.0.0.1 >/tmp/setup.out 2>&1 || { cat /tmp/setup.out; exit 1; }
ssh-keygen -A >/dev/null 2>&1
cat >/etc/ssh/sshd_config.d/test.conf <<CONF
ListenAddress 127.0.0.1
Port ${PORT}
PasswordAuthentication no
PermitRootLogin no
CONF
touch "/w/ready-${PORT}"
exec /usr/sbin/sshd -D -e
