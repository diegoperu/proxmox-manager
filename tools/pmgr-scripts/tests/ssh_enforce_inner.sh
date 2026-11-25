#!/bin/bash
# Gira DENTRO un container: openssh-server vero, configurazioni ostili, pmgr-ssh-enforce.sh da /w.
set -u
. /etc/os-release
export DEBIAN_FRONTEND=noninteractive
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "   FALLITO: $1"; }
sum_all() { cat /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf 2>/dev/null | sha256sum | cut -d' ' -f1; }
case "${ID_LIKE:-}$ID" in
  *debian* | *ubuntu*) apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq openssh-server >/dev/null 2>&1 ;;
  *rhel* | *rocky*) bash /w/rocky_repo.sh; dnf -y -q install openssh-server >/dev/null 2>&1 ;;
esac
command -v sshd >/dev/null || { echo "sshd non installato"; exit 1; }
echo "### $WANT | $PRETTY_NAME | $(sshd -V 2>&1 | head -1)"
E=/w/enforce.sh
MAIN=/etc/ssh/sshd_config
mkdir -p /etc/ssh/sshd_config.d
run() { sh "$E" >/tmp/out 2>&1; RC=$?; }
eff() { # valore effettivo per un utente: eff chiave [utente]
  local K; mkdir -p /run/sshd; K=$(mktemp -d); ssh-keygen -q -t ed25519 -N "" -f $K/k
  sshd -T -f $MAIN -h $K/k -C "user=${2:-nobody},host=h,addr=203.0.113.9" 2>/dev/null | tr 'A-Z' 'a-z' | grep -E "^$1 " | head -1
}
want_eff() { local v; v="$(eff "$1" "${3:-}")"; [ "$v" = "$1 $2" ] && ok || ko "valore effettivo '$1' = '${v:-assente}' (atteso '$2')${3:+ per utente $3}"; }

echo "-- 1. configurazione ostile"
# valori deboli PRIMA dell'Include (vincerebbero), e un blocco Match in fondo con valori deboli
grep -vE '^\s*Include' $MAIN > /tmp/sc
{
  cat <<'EOF'
PasswordAuthentication yes
PermitRootLogin yes
PubkeyAuthentication no
KbdInteractiveAuthentication yes
PermitEmptyPasswords yes
AuthenticationMethods password keyboard-interactive
Include /etc/ssh/sshd_config.d/*.conf
EOF
  cat /tmp/sc
  cat <<'EOF'
Match User foo
    PasswordAuthentication yes
    AuthenticationMethods password
    PermitRootLogin yes
EOF
} > $MAIN
printf 'PasswordAuthentication yes\n' > /etc/ssh/sshd_config.d/50-cloud-init.conf
printf 'PasswordAuthentication yes\nKbdInteractiveAuthentication yes\n' > /etc/ssh/sshd_config.d/60-cloudimg-settings.conf
want_eff passwordauthentication yes   # la VM di partenza e' davvero debole
run
[ "$RC" = 0 ] && ok || ko "esecuzione: uscita $RC: $(tail -3 /tmp/out | tr '\n' ' ')"
for kv in "passwordauthentication no" "pubkeyauthentication yes" "authenticationmethods publickey" "permitrootlogin no" "permitemptypasswords no" "logingracetime 30" "x11forwarding no" "clientaliveinterval 300" "clientalivecountmax 2"; do want_eff "${kv% *}" "${kv#* }"; done
want_eff passwordauthentication no foo
want_eff authenticationmethods publickey foo
want_eff permitrootlogin no foo
grep -q '^# pmgr-disabled: PasswordAuthentication yes' $MAIN && ok || ko "la riga debole nel file principale non e' stata commentata"
grep -q 'pmgr-disabled' /etc/ssh/sshd_config.d/50-cloud-init.conf && ok || ko "il drop-in di cloud-init non e' stato neutralizzato"
grep -q 'Match User foo' $MAIN && ok || ko "il blocco Match non deve sparire (solo le righe deboli)"
[ "$(grep -vE '^\s*(#|$)' $MAIN | head -1)" = "Include /etc/ssh/sshd_config.d/*.conf" ] && ok || ko "Include non e' la prima riga attiva di sshd_config"

echo "-- 2. idempotenza"
S1="$(sum_all)"; run
[ "$RC" = 0 ] && [ "$(sum_all)" = "$S1" ] && [ ! -s /tmp/out ] && ok || ko "seconda esecuzione: cambia file o scrive output (uscita $RC): $(head -c 200 /tmp/out)"

echo "-- 3. qualcuno riabilita la password"
sed -i '1i PasswordAuthentication yes' $MAIN
printf 'PasswordAuthentication yes\nAuthenticationMethods any\n' > /etc/ssh/sshd_config.d/00-aaa.conf
want_eff passwordauthentication yes
run
[ "$RC" = 0 ] && ok || ko "ripristino: uscita $RC"
want_eff passwordauthentication no; want_eff authenticationmethods publickey

echo "-- 4. il file nostro modificato a mano viene riscritto"
printf 'PasswordAuthentication yes\n' > /etc/ssh/sshd_config.d/00-pmgr-hosting.conf
run; want_eff passwordauthentication no
grep -q '^AuthenticationMethods publickey' /etc/ssh/sshd_config.d/00-pmgr-hosting.conf && ok || ko "00-pmgr-hosting.conf non riscritto"
[ "$(stat -c %a /etc/ssh/sshd_config.d/00-pmgr-hosting.conf)" = 644 ] && ok || ko "permessi del file nostro"

echo "-- 5. configurazione GIA' rotta: non si tocca niente"
printf 'OpzioneInesistente yes\n' > /etc/ssh/sshd_config.d/99-rotto.conf
S2="$(sum_all)"; run
[ "$RC" = 1 ] && [ "$(sum_all)" = "$S2" ] && grep -q "non e' valida" /tmp/out && ok || ko "config rotta: uscita $RC, file cambiati? $(tail -2 /tmp/out | tr '\n' ' ')"
rm -f /etc/ssh/sshd_config.d/99-rotto.conf

echo "-- 6. nessun file di chiavi host (come al primo avvio) e /run/sshd mancante"
rm -rf /run/sshd /etc/ssh/ssh_host_*
run; [ "$RC" = 0 ] && ok || ko "senza chiavi host: uscita $RC: $(tail -2 /tmp/out | tr '\n' ' ')"
ls /etc/ssh/ssh_host_* >/dev/null 2>&1 && ko "lo script non deve creare le chiavi host" || ok

echo "-- 7. Rocky 8: manca la riga Include"
if [ "$WANT" = rocky-8 ]; then
  cp /tmp/sc /tmp/sc2; cat >> /tmp/sc2 <<'EOF'
PasswordAuthentication yes
EOF
  cp /tmp/sc2 $MAIN; rm -f /etc/ssh/sshd_config.d/*.conf
  run; grep -qE '^Include /etc/ssh/sshd_config.d/' $MAIN && ok || ko "la riga Include non e' stata aggiunta"
  want_eff passwordauthentication no
fi
echo "   RISULTATO $WANT: $PASS ok, $FAIL falliti"
[ "$FAIL" = 0 ]
