#!/bin/sh
# pmgr-ssh-enforce - rimette SSH a sola chiave (ProxmoxManager, cluster di hosting).
# Installato dallo snippet cloud-init di hosting. Gira a ogni avvio (prima di sshd) e ogni 15 minuti.
# Non va modificato a mano: le modifiche vengono annullate. Solo ASCII.
#
# Cosa fa:
#  1. controlla che la configurazione di sshd sia valida (altrimenti non tocca niente);
#  2. porta la riga "Include /etc/ssh/sshd_config.d/*.conf" in testa a sshd_config (Rocky 8 non ce l'ha;
#     altrove puo' essere stata spostata): in sshd vince il primo valore e un Include dopo un blocco Match
#     varrebbe solo per quell'utente;
#  3. scrive /etc/ssh/sshd_config.d/00-pmgr-hosting.conf (in sshd vince il PRIMO valore trovato);
#  4. commenta, in tutti gli altri file di configurazione (anche dentro i blocchi Match), le righe che
#     indebolirebbero la regola (password o tastiera abilitate, root con password, metodi diversi da publickey);
#  5. controlla con "sshd -T" il valore EFFETTIVO; se non e' quello voluto esce con errore;
#  6. ricarica sshd solo se ha cambiato qualcosa. Se il risultato non e' valido ripristina i file originali.
set -u
DIR=/etc/ssh/sshd_config.d
MAIN=/etc/ssh/sshd_config
CONF=$DIR/00-pmgr-hosting.conf
TAG=pmgr-ssh-enforce
CHANGED=0

log() { logger -t "$TAG" -- "$*" 2>/dev/null; echo "$TAG: $*"; }
fail() { log "ERRORE: $*"; exit 1; }
sum() { if [ -f "$1" ]; then sha256sum <"$1" | cut -d' ' -f1; else echo none; fi; }

SSHD=$(command -v sshd 2>/dev/null || true)
[ -n "$SSHD" ] || SSHD=/usr/sbin/sshd
[ -x "$SSHD" ] || fail "sshd non trovato"
[ -f "$MAIN" ] || fail "$MAIN non trovato"
mkdir -p "$DIR" /run/sshd

WORK=$(mktemp -d) || fail "mktemp non riuscito"
trap 'rm -rf "$WORK"' EXIT
# Chiave host temporanea: sshd -T non parte senza chiavi, e all'avvio possono non esistere ancora.
HK=""
if command -v ssh-keygen >/dev/null 2>&1 && ssh-keygen -q -t ed25519 -N '' -f "$WORK/k" >/dev/null 2>&1; then
  HK="-h $WORK/k"
fi
# shellcheck disable=SC2086
sshd_T() { "$SSHD" -T -f "$MAIN" $HK 2>/dev/null | tr 'A-Z' 'a-z'; }

sshd_T >"$WORK/eff0" && [ -s "$WORK/eff0" ] || fail "la configurazione di sshd non e' valida: non tocco niente"

# copie per tornare indietro
mkdir -p "$WORK/orig"
cp -p "$MAIN" "$WORK/orig/sshd_config"
for f in "$DIR"/*.conf; do [ -f "$f" ] && cp -p "$f" "$WORK/orig/$(basename "$f")"; done
restore() {
  cp -p "$WORK/orig/sshd_config" "$MAIN"
  for f in "$DIR"/*.conf; do [ -f "$f" ] || continue; [ -f "$WORK/orig/$(basename "$f")" ] || rm -f "$f"; done
  for f in "$WORK"/orig/*.conf; do [ -f "$f" ] && cp -p "$f" "$DIR/$(basename "$f")"; done
}

# 2. Include in testa (Rocky 8: il sshd la supporta ma la configurazione di serie non la ha)
first_active=$(grep -vE '^[[:space:]]*(#|$)' "$MAIN" | head -n 1)
if ! printf '%s\n' "$first_active" | grep -qiE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/'; then
  { echo 'Include /etc/ssh/sshd_config.d/*.conf'; grep -viE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/' "$MAIN"; } >"$WORK/main.new"
  cat "$WORK/main.new" >"$MAIN"
  CHANGED=1
  log "riga Include di sshd_config.d portata in testa a $MAIN"
fi

# 3. il nostro file
cat >"$WORK/conf" <<'CONF_END'
# Gestito da ProxmoxManager (cluster di hosting): SSH solo con chiave.
# Viene riscritto a ogni avvio e ogni 15 minuti da pmgr-ssh-enforce: le modifiche a mano non restano.
PubkeyAuthentication yes
PasswordAuthentication no
AuthenticationMethods publickey
PermitEmptyPasswords no
PermitRootLogin no
LoginGraceTime 30
X11Forwarding no
ClientAliveInterval 300
ClientAliveCountMax 2
CONF_END
if [ "$(sum "$WORK/conf")" != "$(sum "$CONF")" ]; then
  install -m 644 -o root -g root "$WORK/conf" "$CONF"
  CHANGED=1
  log "scritto $CONF"
fi

# 4. righe che indebolirebbero la regola, in tutti gli altri file
for f in "$MAIN" "$DIR"/*.conf; do
  [ -f "$f" ] || continue
  [ "$f" = "$CONF" ] && continue
  before=$(sum "$f")
  sed -i -E \
    -e '/^[[:space:]]*(PasswordAuthentication|KbdInteractiveAuthentication|ChallengeResponseAuthentication|PermitEmptyPasswords)[[:space:]]+yes[[:space:]]*$/I s/^(.*)$/# pmgr-disabled: \1/' \
    -e '/^[[:space:]]*PubkeyAuthentication[[:space:]]+no[[:space:]]*$/I s/^(.*)$/# pmgr-disabled: \1/' \
    -e '/^[[:space:]]*PermitRootLogin[[:space:]]+(yes|without-password|prohibit-password|forced-commands-only)[[:space:]]*$/I s/^(.*)$/# pmgr-disabled: \1/' \
    -e '/^[[:space:]]*AuthenticationMethods[[:space:]]/I { /^[[:space:]]*AuthenticationMethods[[:space:]]+publickey[[:space:]]*$/I !s/^(.*)$/# pmgr-disabled: \1/ }' \
    "$f"
  if [ "$(sum "$f")" != "$before" ]; then
    CHANGED=1
    log "neutralizzate righe che abilitavano la password in $f"
  fi
done

# 5. valore effettivo
if ! sshd_T >"$WORK/eff" || [ ! -s "$WORK/eff" ]; then
  restore
  fail "la nuova configurazione non e' valida: ripristinati i file originali"
fi
for want in 'passwordauthentication no' 'pubkeyauthentication yes' 'authenticationmethods publickey' 'permitrootlogin no' 'permitemptypasswords no' 'logingracetime 30' 'x11forwarding no' 'clientaliveinterval 300' 'clientalivecountmax 2'; do
  grep -qx "$want" "$WORK/eff" || fail "valore effettivo diverso da quello voluto: manca '$want' (controlla: sshd -T)"
done

# 6. ricarica solo se e' cambiato qualcosa
if [ "$CHANGED" = 1 ] && [ -d /run/systemd/system ]; then
  for u in ssh.service sshd.service; do
    # --no-block e' indispensabile: questo servizio e' "Before=ssh.service", quindi un reload che aspetta
    # finirebbe in deadlock (il reload attende la fine di questo servizio, che attende il reload).
    if systemctl is-active --quiet "$u" 2>/dev/null; then systemctl reload --no-block "$u" 2>/dev/null && log "ricarico $u"; fi
  done
fi
exit 0
