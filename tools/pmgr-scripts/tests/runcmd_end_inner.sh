#!/bin/bash
# Gira DENTRO un container della release: esegue il runcmd completo dello snippet (standard e hosting) con
# i comandi che toccano il sistema sostituiti da stub che registrano cosa e' stato chiamato, e controlla che
# lo script ARRIVI IN FONDO (reboot) e che nell'hosting giri tutto il blocco hosting.
set -u
. /etc/os-release
echo "### $WANT | $PRETTY_NAME"
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "   FALLITO: $1"; }
mkdir -p /stubs
cat >/stubs/stub <<'STUB'
#!/bin/sh
echo "$(basename "$0") $*" >>/tmp/stublog
exit 0
STUB
chmod 755 /stubs/stub
for c in apt apt-get dnf systemctl ufw firewall-cmd firewall-offline-cmd reboot sysctl fail2ban-client setsebool semanage restorecon getenforce service; do ln -sf /stub /stubs/$c 2>/dev/null; ln -sf stub /stubs/$c; done
# sudo: passa il comando (che e' uno stub o un comando innocuo)
printf '#!/bin/sh\nexec "$@"\n' >/stubs/sudo; chmod 755 /stubs/sudo
for prof in std host; do
  : >/tmp/stublog
  rm -f /etc/sysctl.d/90-pmgr-hosting.conf /etc/fail2ban/jail.d/60-pmgr-recidive.local
  mkdir -p /etc/sysctl.d /etc/fail2ban/jail.d /etc/systemd/system /usr/local/sbin /etc/ssh/sshd_config.d
  # come cloud-init: un unico script eseguito con sh
  PATH=/stubs:$PATH timeout 120 sh /w/run-$prof.sh >/tmp/run.out 2>&1
  last="$(tail -n 1 /tmp/stublog | sed "s/[[:space:]]*$//")"
  [ "$last" = "reboot" ] && ok || ko "$prof: lo script non arriva al reboot finale (ultima chiamata: '$last')"
  if [ "$prof" = host ]; then
    grep -q 'systemctl enable pmgr-ssh-enforce.service pmgr-ssh-enforce.timer' /tmp/stublog && ok || ko "host: non gira l'abilitazione di pmgr-ssh-enforce"
    grep -q 'sysctl --system' /tmp/stublog && ok || ko "host: non gira sysctl --system"
    grep -q 'fail2ban-client -d' /tmp/stublog && ok || ko "host: non si controlla la configurazione di fail2ban dopo recidive"
    [ -f /etc/sysctl.d/90-pmgr-hosting.conf ] && ok || ko "host: file sysctl non scritto"
    [ -f /etc/fail2ban/jail.d/60-pmgr-recidive.local ] && ok || ko "host: file recidive non scritto"
    [ -x /usr/local/sbin/pmgr-ssh-enforce ] && ok || ko "host: pmgr-ssh-enforce non scritto/eseguibile"
    [ -f /etc/systemd/system/pmgr-ssh-enforce.timer ] && ok || ko "host: timer non scritto"
    if [ "${ID_LIKE:-}$ID" != "${ID_LIKE:-}$ID" ]; then :; fi
    case "$ID" in rocky) grep -q 'firewall-offline-cmd\|firewall-cmd' /tmp/stublog && ok || ko "host: firewall di Rocky non eseguito" ;; *) grep -q 'ufw --force enable\|ufw .*enable' /tmp/stublog && ok || ko "host: ufw non abilitato" ;; esac
  else
    [ ! -f /etc/sysctl.d/90-pmgr-hosting.conf ] && ok || ko "std: il file sysctl dell'hosting non deve esserci"
    grep -q 'pmgr-ssh-enforce' /tmp/stublog && ko "std: pmgr-ssh-enforce non deve girare" || ok
  fi
  grep -qE 'syntax error|Syntax error|unexpected|Unterminated' /tmp/run.out && ko "$prof: errori di sintassi nello script: $(grep -E 'yntax|nexpected' /tmp/run.out | head -2 | tr '\n' ' ')" || ok
done
echo "   RISULTATO $WANT: $PASS ok, $FAIL falliti"
[ "$FAIL" = 0 ]
