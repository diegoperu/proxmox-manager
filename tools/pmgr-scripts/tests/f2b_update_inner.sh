#!/bin/bash
# Gira DENTRO un container (lanciato da test_fail2ban_update.sh): simula una VM creata con i VECCHI snippet
# (copia di jail.conf in jail.local, bantime 1d) con il fail2ban vero della distribuzione e prova
# pmgr-fail2ban-update.sh. Variabili: WANT (es. ubuntu-24.04), SEDMODE (a|b come nel vecchio snippet).
set -u
. /etc/os-release
export DEBIAN_FRONTEND=noninteractive
PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "   FALLITO: $1"; }
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then ok; else ko "$d"; fi; }
# il test non usa cmp/diff (vengono nascosti piu' sotto, come su Rocky minimale)
same() { [ "$(sha256sum <"$1" | cut -d' ' -f1)" = "$(sha256sum <"$2" | cut -d' ' -f1)" ]; }
export -f same

case "${ID_LIKE:-}$ID" in
  *debian* | *ubuntu*)
    apt-get update -qq >/dev/null 2>&1
    apt-get install -y -qq fail2ban ufw python3 openssh-server >/dev/null 2>&1
    ;;
  *rhel* | *rocky*)
    bash /w/rocky_repo.sh
    dnf -y -q install epel-release >/dev/null 2>&1
    dnf -y -q install fail2ban firewalld openssh-server >/dev/null 2>&1
    ;;
esac
command -v fail2ban-client >/dev/null || { echo "fail2ban non installato"; exit 1; }
echo "### $WANT | $PRETTY_NAME | $(fail2ban-client --version | head -1)"
U=/w/pmgr-fail2ban-update.sh
J=/etc/fail2ban/jail.local

old_vm() { # ricrea lo stato lasciato dai vecchi snippet
  rm -f /etc/fail2ban/jail.d/50-pmgr.local /etc/fail2ban/fail2ban.d/50-pmgr.local /etc/fail2ban/jail.d/99-broken.local
  cp /etc/fail2ban/jail.conf $J
  if [ "$SEDMODE" = b ]; then sed -i -e 's/^bantime .*$/bantime = 1d/' $J; else sed -i -e '/^bantime/s/^.*$/bantime = 1d/' $J; fi
}
dump() { fail2ban-client -d 2>&1; }
run() { "$U" "$@" >/tmp/out 2>&1; RC=$?; }

# ---- 0. la VM "vecchia" e' davvero sbagliata
old_vm
cp $J /tmp/jail.local.orig
case "$ID" in
  rocky)
    # sui Rocky la VM "vecchia" non ha NESSUNA jail sshd (il pacchetto EPEL non la abilita)
    dump | grep -qF "['start', 'sshd']" && ko "su Rocky la VM vecchia non dovrebbe avere la jail sshd" || ok ;;
  *)
    dump | grep -qF "['set', 'sshd', 'bantime', '1d']" && ok || ko "la VM simulata non ha il vecchio bantime di 1 giorno"
    dump | grep -qF "['set', 'sshd', 'findtime', '10m']" && ok || ko "la VM simulata non ha findtime 10m" ;;
esac

# ---- 0b. lo script non deve dipendere da cmp/diff (assenti su Rocky minimale)
command -v cmp >/dev/null 2>&1 && mv "$(command -v cmp)" /tmp/cmp.moved
command -v diff >/dev/null 2>&1 && mv "$(command -v diff)" /tmp/diff.moved
command -v cmp >/dev/null 2>&1 && ko "cmp ancora presente" || ok

# ---- 1. --check non scrive
run --check
[ "$RC" = 3 ] && ok || ko "--check su VM vecchia: uscita $RC (attesa 3): $(tail -3 /tmp/out | tr '\n' ' ')"
check "--check non cambia jail.local" same $J /tmp/jail.local.orig
check "--check non crea file" test ! -e /etc/fail2ban/jail.d/50-pmgr.local

# ---- 2. aggiornamento
run
[ "$RC" = 0 ] && ok || { ko "aggiornamento: uscita $RC: $(tail -5 /tmp/out | tr '\n' ' ')"; }
D="$(dump)"
for want in "['start', 'sshd']" "['add', 'sshd', 'systemd']" "['set', 'sshd', 'maxretry', 5]" "['set', 'sshd', 'findtime', '15m']" \
  "['set', 'sshd', 'bantime', '1h']" "['set', 'sshd', 'bantime.increment', True]" \
  "['set', 'sshd', 'bantime.multipliers', '1 2 4 8 16 32 64']" "['set', 'sshd', 'bantime.maxtime', '1w']" "['set', 'dbpurgeage', '4w']"; do
  grep -qF -- "$want" <<<"$D" && ok || ko "dopo l'aggiornamento manca: $want"
done
grep -qF "'bantime', '1d'" <<<"$D" && ko "bantime di 1 giorno ancora presente" || ok
check "vecchia copia messa da parte" test ! -e $J
check "copia di sicurezza identica all'originale" bash -c "same /tmp/jail.local.orig \$(ls -d /var/backups/pmgr-fail2ban/*/jail.local | head -1)"
check "file 644 di root" bash -c "[ \"\$(stat -c %U:%a /etc/fail2ban/jail.d/50-pmgr.local /etc/fail2ban/fail2ban.d/50-pmgr.local | sort -u)\" = root:644 ]"
$U --print jail >/tmp/pj; $U --print global >/tmp/pg
check "contenuto = quello dello snippet" bash -c "same /tmp/pj /etc/fail2ban/jail.d/50-pmgr.local && same /tmp/pg /etc/fail2ban/fail2ban.d/50-pmgr.local"
echo "   azione di ban dopo l'aggiornamento: $(grep -oE "\['set', 'sshd', 'addaction', '[^']+'\]" <<<"$D" | sed -E "s/.*'([^']+)'\]/\1/")"
case "$WANT" in
  debian-13 | ubuntu-24.04) grep -qF "'addaction', 'nftables'" <<<"$D" && ok || ko "l'azione della distribuzione (nftables) non e' tornata" ;;
  rocky-*) grep -qF "'addaction', 'firewallcmd-rich-rules'" <<<"$D" && ok || ko "l'azione di Rocky non e' tornata" ;;
esac

# ---- 3. idempotenza
run
if [ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out; then ok; else ko "seconda esecuzione: uscita $RC"; fi
run --check
[ "$RC" = 0 ] && ok || ko "--check dopo l'aggiornamento: uscita $RC"
check "nessun nuovo backup alla seconda esecuzione" bash -c "[ \"\$(ls /var/backups/pmgr-fail2ban | wc -l)\" = 1 ]"

# ---- 4. jail.local personalizzato: non si tocca
rm -f /etc/fail2ban/jail.d/50-pmgr.local /etc/fail2ban/fail2ban.d/50-pmgr.local
printf '[DEFAULT]\nignoreip = 127.0.0.1/8 ::1 10.0.0.0/8\n' >$J
cp $J /tmp/custom.local
run
if [ "$RC" = 0 ] && grep -q "contiene personalizzazioni" /tmp/out; then ok; else ko "jail.local personalizzato: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"; fi
check "jail.local personalizzato intatto" same $J /tmp/custom.local
dump | grep -qF "['set', 'sshd', 'bantime', '1h']" && ok || ko "con jail.local personalizzato il bantime non e' 1h"

# ---- 5. configurazione che fail2ban non riesce a leggere: ritorno indietro
rm -f /etc/fail2ban/jail.d/50-pmgr.local /etc/fail2ban/fail2ban.d/50-pmgr.local
old_vm
cp $J /tmp/jail.local.orig2
printf '[sshd\nrotto\n' >/etc/fail2ban/jail.d/99-broken.local
run
[ "$RC" = 1 ] && ok || ko "configurazione rotta: uscita $RC (attesa 1)"
check "ripristino: nessun file nostro" bash -c "[ ! -e /etc/fail2ban/jail.d/50-pmgr.local ] && [ ! -e /etc/fail2ban/fail2ban.d/50-pmgr.local ]"
check "ripristino: jail.local com'era" same $J /tmp/jail.local.orig2
grep -q "ripristinato" /tmp/out && ok || ko "il messaggio non dice che e' stato tutto ripristinato"
rm -f /etc/fail2ban/jail.d/99-broken.local

# ---- 6. sezione [sshd] ridefinita da un altro file (jail.local del gruppo): si corregge solo cio' che e' piu' debole
OVR=/etc/fail2ban/jail.d/51-pmgr-sshd.local
clean() { rm -f /etc/fail2ban/jail.d/50-pmgr.local /etc/fail2ban/fail2ban.d/50-pmgr.local $OVR /etc/fail2ban/jail.d/99-late.local; }
effective() { dump | sed -n "s/^\['set', 'sshd', '$1', \(.*\)\]$/\1/p" | tail -n 1 | tr -d "'"; }
# 6a. valori piu' DEBOLI dei nostri
clean; printf '[sshd]\nmaxretry = 10\nfindtime = 10m\nbantime = 5m\n' >$J; cp $J /tmp/conflict.local
run --check
[ "$RC" = 3 ] && grep -q "51-pmgr-sshd.local" /tmp/out && ok || ko "6a --check: dovrebbe annunciare il file di correzione (uscita $RC)"
check "6a --check non scrive niente" test ! -e $OVR
run
[ "$RC" = 0 ] && ok || ko "6a [sshd] piu' debole: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
check "6a jail.local del gruppo intatto" same $J /tmp/conflict.local
check "6a file di correzione scritto con le tre chiavi" bash -c "grep -q '^maxretry = 5' $OVR && grep -q '^findtime = 15m' $OVR && grep -q '^bantime = 1h' $OVR"
[ "$(effective maxretry)" = 5 ] && [ "$(effective findtime)" = 15m ] && [ "$(effective bantime)" = 1h ] && ok || ko "6a valori caricati: $(effective maxretry) $(effective findtime) $(effective bantime)"
run
[ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "6a seconda esecuzione non idempotente (uscita $RC)"
# 6b. valori piu' SEVERI: si rispettano, nessun file di correzione
clean; printf '[sshd]\nmaxretry = 3\nfindtime = 30m\nbantime = 1d\n' >$J; cp $J /tmp/strict.local
run
[ "$RC" = 0 ] && ok || ko "6b [sshd] piu' severo: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
check "6b nessun file di correzione" test ! -e $OVR
[ "$(effective maxretry)" = 3 ] && [ "$(effective findtime)" = 30m ] && [ "$(effective bantime)" = 1d ] && ok || ko "6b i valori piu' severi del gruppo sono stati cambiati: $(effective maxretry) $(effective findtime) $(effective bantime)"
check "6b jail.local intatto" same $J /tmp/strict.local
# 6c. misto: maxretry piu' severo, bantime piu' debole -> si corregge solo bantime
clean; printf '[sshd]\nmaxretry = 3\nbantime = 10m\n' >$J
run
[ "$RC" = 0 ] && ok || ko "6c misto: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
check "6c il file di correzione ha solo bantime" bash -c "grep -q '^bantime = 1h' $OVR && ! grep -q '^maxretry' $OVR && ! grep -q '^findtime' $OVR"
[ "$(effective maxretry)" = 3 ] && [ "$(effective bantime)" = 1h ] && ok || ko "6c valori caricati: maxretry $(effective maxretry), bantime $(effective bantime)"
# 6d. ban permanente (-1) = piu' severo di 1 ora
clean; printf '[sshd]\nbantime = -1\n' >$J
run
[ "$RC" = 0 ] && [ ! -e $OVR ] && [ "$(effective bantime)" = -1 ] && ok || ko "6d ban permanente: uscita $RC, correzione presente? $([ -e $OVR ] && echo si || echo no)"
# 6e. valore non leggibile (riferimento a un'altra variabile): nel dubbio si impone il nostro
clean; printf '[sshd]\nmaxretry = %%(foo)s\n' >$J
run
[ "$RC" = 0 ] || [ "$RC" = 1 ] && ok || ko "6e valore non leggibile: uscita $RC"
# 6f. conflitto che la correzione NON puo' risolvere (file caricato DOPO i nostri): tutto ripristinato
clean; rm -f $J; printf '[sshd]\nbantime = 5m\n' >/etc/fail2ban/jail.d/99-late.local
run
[ "$RC" = 1 ] && ok || ko "6f conflitto non risolvibile: uscita $RC (attesa 1): $(tail -4 /tmp/out | tr '\n' ' ')"
grep -q "bantime" /tmp/out && ok || ko "6f il conflitto non indica il valore mancante"
check "6f file nostri rimossi (compreso il file di correzione)" bash -c "[ ! -e /etc/fail2ban/jail.d/50-pmgr.local ] && [ ! -e /etc/fail2ban/fail2ban.d/50-pmgr.local ] && [ ! -e $OVR ]"
rm -f /etc/fail2ban/jail.d/99-late.local; clean

# ---- 6g. sshd su una porta diversa dalla 22 (caso reale: sshd sulla 24, la 22 e' di un contenitore del cliente)
clean
mkdir -p /run/sshd /etc/ssh/sshd_config.d; ssh-keygen -A >/dev/null 2>&1
grep -q '^Include' /etc/ssh/sshd_config || sed -i '1i Include /etc/ssh/sshd_config.d/*.conf' /etc/ssh/sshd_config
sed -i 's/^Port 22$/#Port 22/' /etc/ssh/sshd_config
printf 'Port 2222\n' >/etc/ssh/sshd_config.d/00-port.conf
[ "$(sshd -T | awk '$1=="port"{print $2}' | tr '\n' ' ')" = "2222 " ] && ok || ko "6g sshd -T non dice 2222"
run --check
[ "$RC" = 3 ] && ok || ko "6g --check con sshd su 2222: uscita $RC (attesa 3)"
run
[ "$RC" = 0 ] && ok || ko "6g applicazione con sshd su 2222: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
grep -qx 'port = 2222' /etc/fail2ban/jail.d/50-pmgr.local && ok || ko "6g la jail sshd non ha port = 2222"
dump | grep -F "['multi-set', 'sshd', 'action'," | grep -qF "['port', '2222']" && ok || ko "6g la configurazione caricata non banna la 2222"
dump | grep -F "['multi-set', 'sshd', 'action'," | grep -qF "['port', 'ssh']" && ko "6g la jail banna ancora la porta ssh (22)" || ok
run
[ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "6g seconda esecuzione non idempotente (uscita $RC)"
# due porte
printf 'Port 22\nPort 2222\n' >/etc/ssh/sshd_config.d/00-port.conf
run
[ "$RC" = 0 ] && grep -qx 'port = 22,2222' /etc/fail2ban/jail.d/50-pmgr.local && ok || ko "6g con due porte: uscita $RC, jail: $(grep '^port' /etc/fail2ban/jail.d/50-pmgr.local)"
# di nuovo la 22: la riga port sparisce
rm -f /etc/ssh/sshd_config.d/00-port.conf; sed -i 's/^#Port 22$/Port 22/' /etc/ssh/sshd_config
run
[ "$RC" = 0 ] && ! grep -q '^port' /etc/fail2ban/jail.d/50-pmgr.local && ok || ko "6g tornata la 22 la riga port dovrebbe sparire (uscita $RC)"
clean

# ---- 7. fail2ban assente
mv "$(command -v fail2ban-client)" /tmp/fail2ban-client.moved
run
[ "$RC" = 0 ] && grep -q "non e' installato" /tmp/out && ok || ko "fail2ban assente: uscita $RC"
mv /tmp/fail2ban-client.moved /usr/bin/fail2ban-client 2>/dev/null || true

# ---- 8. non root
run --bogus
[ "$RC" = 2 ] && ok || ko "opzione sconosciuta: uscita $RC (attesa 2)"

echo "   RISULTATO $WANT: $PASS ok, $FAIL falliti"
[ "$FAIL" = 0 ]
