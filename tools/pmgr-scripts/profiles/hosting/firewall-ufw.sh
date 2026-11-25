#!/bin/sh
# Hosting (Debian, Ubuntu): firewall ufw con tutto chiuso in ingresso tranne SSH, HTTP e HTTPS.
# Gestito da ProxmoxManager. Idempotente. Solo ASCII.
# "--force": cloud-init non ha un terminale e ufw altrimenti chiede conferma.
set -u
ufw default deny incoming
ufw default allow outgoing
# porte SSH: gli argomenti (es. "24 2022"), altrimenti la 22. Una VM con sshd su un'altra porta deve aprire QUELLA.
[ "$#" -gt 0 ] || set -- 22
for p in "$@"; do
  case "$p" in "" | *[!0-9]*) continue ;; esac
  ufw allow "$p/tcp"
done
ufw allow 80/tcp
ufw allow 443/tcp
ufw logging low
ufw --force enable
exit 0
