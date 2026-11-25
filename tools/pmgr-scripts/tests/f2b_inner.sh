#!/bin/bash
# Gira DENTRO un container (lanciato da test_fail2ban_snippets.sh): installa il fail2ban vero della
# distribuzione, esegue i comandi fail2ban estratti dallo snippet e controlla cosa carica davvero.
# Variabili: ID (es. ubuntu-24.04), con /w/cmds.sh = comandi estratti dallo snippet.
set -u
. /etc/os-release
export DEBIAN_FRONTEND=noninteractive
PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "   FALLITO: $1"; }

case "${ID_LIKE:-}$ID" in
  *debian* | *ubuntu*)
    apt-get update -qq >/dev/null 2>&1
    apt-get install -y -qq fail2ban ufw python3 >/dev/null 2>&1
    ;;
  *rhel* | *rocky*)
    bash /w/rocky_repo.sh
    dnf -y -q install epel-release >/dev/null 2>&1
    dnf -y -q install fail2ban firewalld >/dev/null 2>&1
    ;;
esac
command -v fail2ban-client >/dev/null || { echo "fail2ban non installato"; exit 1; }
echo "### $WANT | $PRETTY_NAME | $(fail2ban-client --version | head -1)"

# i comandi dello snippet, nell'ordine; systemctl non c'e' nel container e viene ignorato
while IFS= read -r -d $'\x01' item; do
  case "$item" in
    *systemctl*) echo "   (ignorato nel container: ${item%%$'\n'*})" ;;
    *) sh -c "$item" || ko "comando dello snippet fallito: ${item%%$'\n'*}" ;;
  esac
done </w/cmds.bin

[ -f /etc/fail2ban/jail.d/50-pmgr.local ] && ok || ko "manca jail.d/50-pmgr.local"
[ -f /etc/fail2ban/fail2ban.d/50-pmgr.local ] && ok || ko "manca fail2ban.d/50-pmgr.local"
[ ! -f /etc/fail2ban/jail.local ] && ok || ko "jail.local esiste ancora (la copia di jail.conf sovrascrive le impostazioni della distribuzione)"

DUMP="$(fail2ban-client -d 2>&1)"
has() { grep -qF -- "$1" <<<"$DUMP"; }
want() { if has "$1"; then ok; else ko "atteso nella configurazione caricata: $1"; fi; }
want "['start', 'sshd']"
want "['set', 'sshd', 'maxretry', 5]"
want "['set', 'sshd', 'findtime', '15m']"
want "['set', 'sshd', 'bantime', '1h']"
want "['set', 'sshd', 'bantime.increment', True]"
want "['set', 'sshd', 'bantime.multipliers', '1 2 4 8 16 32 64']"
want "['set', 'sshd', 'bantime.maxtime', '1w']"
want "['set', 'dbpurgeage', '4w']"
if has "'bantime', '1d'"; then ko "bantime di 1 giorno ancora presente"; else ok; fi
if [ "$(grep -c "\['start'," <<<"$DUMP")" = 1 ]; then ok; else ko "devono partire solo le jail attese (solo sshd)"; fi
want "['add', 'sshd', 'systemd']"
echo "   backend: $(grep -oE "\['add', 'sshd', '[a-z]+'\]" <<<"$DUMP" | head -1) | azione: $(grep -oE "\['set', 'sshd', 'addaction', '[^']+'\]" <<<"$DUMP" | head -1 | sed -E "s/.*'([^']+)'\]/\1/") | $(grep -c "WARNING Have not found any log file" <<<"$DUMP") avvisi 'nessun file di log'"
echo "   RISULTATO $WANT: $PASS ok, $FAIL falliti"
[ "$FAIL" = 0 ]
