#!/bin/sh
# Hosting (Rocky): firewalld con la zona "public" e solo SSH, HTTP e HTTPS.
# Toglie anche cockpit (porta 9090) e dhcpv6-client, aperti di serie nella zona public.
# Gestito da ProxmoxManager. Idempotente. Solo ASCII.
# Con firewalld spento (primo avvio) si usa firewall-offline-cmd; con firewalld acceso, firewall-cmd.
set -u
# porte SSH: gli argomenti (es. "24 2022"), altrimenti la 22 (servizio ssh). Una VM con sshd su un'altra porta deve aprire QUELLA.
[ "$#" -gt 0 ] || set -- 22
systemctl enable firewalld >/dev/null 2>&1
if firewall-cmd --state >/dev/null 2>&1; then
  firewall-cmd --set-default-zone=public
  firewall-cmd --permanent --zone=public --add-service=http --add-service=https
  for p in "$@"; do
    case "$p" in "" | *[!0-9]*) continue ;; 22) firewall-cmd --permanent --zone=public --add-service=ssh ;; *) firewall-cmd --permanent --zone=public --add-port="$p/tcp" ;; esac
  done
  firewall-cmd --permanent --zone=public --remove-service=cockpit --remove-service=dhcpv6-client
  firewall-cmd --reload
else
  firewall-offline-cmd --set-default-zone=public
  firewall-offline-cmd --zone=public --add-service=http --add-service=https
  for p in "$@"; do
    case "$p" in "" | *[!0-9]*) continue ;; 22) firewall-offline-cmd --zone=public --add-service=ssh ;; *) firewall-offline-cmd --zone=public --add-port="$p/tcp" ;; esac
  done
  # firewall-offline-cmd non accetta --remove-service insieme a --zone ne' piu' servizi nello stesso comando:
  # agisce sulla zona predefinita (public, impostata sopra), un servizio per volta
  firewall-offline-cmd --remove-service=cockpit
  firewall-offline-cmd --remove-service=dhcpv6-client
fi
exit 0
