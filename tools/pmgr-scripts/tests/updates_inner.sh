#!/bin/bash
# Gira DENTRO un container: installa i pacchetti veri, applica i comandi degli aggiornamenti estratti dallo
# snippet e controlla la configurazione effettiva, i timer, l'avviso di riavvio e (FUNCTIONAL=1) un giro reale.
set -u
. /etc/os-release
export DEBIAN_FRONTEND=noninteractive
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "   FALLITO: $1"; }
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then ok; else ko "$d"; fi; }
FAM=deb
case "${ID_LIKE:-}$ID" in
  *debian* | *ubuntu*) apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq unattended-upgrades systemd python3 >/dev/null 2>&1; rm -f /etc/apt/apt.conf.d/docker-* ;;  # i file docker-* sono un artefatto dell'immagine, non delle VM
  *rhel* | *rocky*) FAM=rpm; bash /w/rocky_repo.sh; dnf -y -q install epel-release >/dev/null 2>&1; dnf -y -q install dnf-automatic dnf-utils systemd python3 >/dev/null 2>&1 ;;
esac
echo "### $WANT | $PRETTY_NAME"

# i comandi dello snippet, come li eseguirebbe cloud-init; systemctl non serve (si controllano i file)
n=0
while IFS= read -r -d $'\x01' item; do
  n=$((n + 1))
  case "$item" in
    systemctl*) ;;
    *) sh -c "$item" >/tmp/item.out 2>&1 || ko "comando $n dello snippet fallito: ${item%%$'\n'*} -> $(head -c 150 /tmp/item.out)" ;;
  esac
done </w/cmds.bin
[ "$n" -ge 8 ] && ok || ko "pochi comandi estratti ($n)"

echo "-- file"
check "pmgr-reboot-check eseguibile" bash -c '[ "$(stat -c %a /usr/local/sbin/pmgr-reboot-check)" = 755 ]'
check "profile.d presente" test -f /etc/profile.d/pmgr-reboot-required.sh
if [ "$FAM" = deb ]; then
  echo "-- apt: configurazione effettiva"
  A="$(apt-config dump 2>/dev/null)"
  has() { grep -qF -- "$1" <<<"$A"; }
  has 'Unattended-Upgrade::Automatic-Reboot "false";' && ok || ko "Automatic-Reboot non e' false"
  has 'APT::Periodic::Enable "1";' && ok || ko "APT::Periodic::Enable non e' 1 (con 0 i giri automatici sono spenti)"
  has 'APT::Periodic::Update-Package-Lists "always";' && ok || ko "Update-Package-Lists non e' always"
  has 'APT::Periodic::Unattended-Upgrade "always";' && ok || ko "Unattended-Upgrade non e' always"
  grep -q 'pmgr-reboot-check --update-flag' <<<"$A" && ok || ko "manca il hook DPkg::Post-Invoke"
  if [ "$ID" = ubuntu ]; then
    O="$(grep -E '^Unattended-Upgrade::Allowed-Origins::' <<<"$A" | grep -oE '"[^"]+"')"
    [ "$(wc -l <<<"$O")" = 3 ] && ok || ko "attese 3 origini Ubuntu, trovate: $(tr '\n' ' ' <<<"$O")"
    grep -qE '^"\$\{distro_id\}:\$\{distro_codename\}"$' <<<"$O" && ko "la sola origine del rilascio (non di sicurezza) e' ancora presente" || ok
    [ "$(grep -c -- '-security' <<<"$O")" = 3 ] && ok || ko "non tutte le origini sono di sicurezza"
  else
    O="$(grep -E '^Unattended-Upgrade::Origins-Pattern::' <<<"$A" | grep -oE '"[^"]+"')"
    [ "$(wc -l <<<"$O")" = 2 ] && ok || ko "attese 2 origini Debian, trovate: $(tr '\n' ' ' <<<"$O")"
    grep -q 'label=Debian-Security' <<<"$O" && ! grep -q 'label=Debian"' <<<"$O" && ok || ko "origini Debian non solo di sicurezza: $(tr '\n' ' ' <<<"$O")"
  fi
  UU="$(unattended-upgrade --dry-run -d 2>&1 | head -60)"
  grep -q 'Allowed origins are' <<<"$UU" && ok || ko "unattended-upgrade non stampa le origini permesse"
  grep -E 'Allowed origins are' <<<"$UU" | head -1 | sed 's/^/   /'
  echo "-- timer"
  grep -q '^OnCalendar=$' /etc/systemd/system/apt-daily-upgrade.timer.d/50-pmgr.conf && ok || ko "il drop-in non azzera OnCalendar"
  for t in apt-daily apt-daily-upgrade; do
    while IFS= read -r cal; do
      systemd-analyze calendar "$cal" 2>&1 | grep -q 'Normalized form' && ok || ko "OnCalendar non valido: $t $cal"
    done < <(grep '^OnCalendar=.' /etc/systemd/system/$t.timer.d/50-pmgr.conf | cut -d= -f2)
    [ "$(grep -c '^OnCalendar=.' /etc/systemd/system/$t.timer.d/50-pmgr.conf)" = 2 ] && ok || ko "$t: servono due orari al giorno"
  done
  systemd-analyze verify /usr/lib/systemd/system/apt-daily-upgrade.timer /etc/systemd/system/apt-daily-upgrade.timer.d/50-pmgr.conf >/dev/null 2>&1 || true
else
  echo "-- dnf-automatic: configurazione effettiva"
  C=/etc/dnf/automatic.conf
  grep -qE '^upgrade_type = security$' $C && ok || ko "upgrade_type non e' security"
  grep -qE '^apply_updates = yes$' $C && ok || ko "apply_updates non e' yes"
  grep -qE '^download_updates = yes$' $C && ok || ko "download_updates non e' yes"
  if grep -q '^reboot' $C; then grep -qE '^reboot = never$' $C && ok || ko "reboot non e' never"; else echo "   (questa versione di dnf-automatic non ha l'opzione reboot: non riavvia mai)"; fi
  echo "-- timer"
  while IFS= read -r cal; do
    systemd-analyze calendar "$cal" 2>&1 | grep -q 'Normalized form' && ok || ko "OnCalendar non valido: $cal"
  done < <(grep '^OnCalendar=.' /etc/systemd/system/dnf-automatic.timer.d/50-pmgr.conf | cut -d= -f2)
  [ "$(grep -c '^OnCalendar=.' /etc/systemd/system/dnf-automatic.timer.d/50-pmgr.conf)" = 2 ] && ok || ko "servono due orari al giorno"
  grep -q '^ExecStartPost=-/usr/local/sbin/pmgr-reboot-check --update-flag' /etc/systemd/system/dnf-automatic.service.d/50-pmgr.conf && ok || ko "manca l'aggiornamento del flag dopo il giro"
fi

echo "-- avviso di riavvio"
R=/usr/local/sbin/pmgr-reboot-check
rm -f /run/pmgr-reboot-required /var/run/reboot-required /var/run/reboot-required.pkgs
$R >/dev/null; [ "$?" = 0 ] && ok || ko "senza motivi dovrebbe uscire 0"
printf '*** System restart required ***\n' >/var/run/reboot-required; printf 'linux-image-x\nlibc6\n' >/var/run/reboot-required.pkgs
W="$($R)"; [ "$?" = 1 ] && ok || ko "con reboot-required dovrebbe uscire 1"
grep -q 'linux-image-x' <<<"$W" && ok || ko "il motivo non cita i pacchetti"
rm -f /var/run/reboot-required /var/run/reboot-required.pkgs
if [ "$FAM" = deb ]; then
  run="$(uname -r)"; flavor="$(printf '%s' "$run" | sed -E 's/^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+//')"
  mkdir -p /boot; : >"/boot/vmlinuz-99.0.0-1$flavor"; : >"/boot/vmlinuz-1.0.0-1$flavor"
  W="$($R)"; [ "$?" = 1 ] && ok || ko "kernel piu' nuovo in /boot: dovrebbe uscire 1"
  grep -q 'kernel 99.0.0' <<<"$W" && ok || ko "il motivo non cita il kernel: $W"
  : >"/boot/vmlinuz-99.0.0-1-altrosapore"; rm -f "/boot/vmlinuz-99.0.0-1$flavor"
  $R >/dev/null; [ "$?" = 0 ] && ok || ko "un kernel di un altro 'sapore' non deve contare"
  rm -f /boot/vmlinuz-*
fi
printf 'prova motivo\n' >/run/pmgr-reboot-required
$R >/dev/null; [ "$?" = 1 ] && ok || ko "il flag in /run dovrebbe far uscire 1"
$R --update-flag >/dev/null; [ ! -e /run/pmgr-reboot-required ] && ok || ko "--update-flag deve togliere il flag quando non serve piu'"
printf 'ancora\n' >/var/run/reboot-required; $R --update-flag >/dev/null
[ -s /run/pmgr-reboot-required ] && ok || ko "--update-flag deve scrivere il flag quando serve"
rm -f /var/run/reboot-required /run/pmgr-reboot-required

# il messaggio al login: vero terminale (pty) per una shell di login interattiva
cat >/tmp/ptyrun.py <<'EOF'
import os, pty, sys
out = []
def rd(fd):
    d = os.read(fd, 4096); out.append(d); return d
pty.spawn(sys.argv[1:], rd)
open("/tmp/pty.out", "wb").write(b"".join(out))
EOF
login_out() { rm -f /tmp/pty.out; python3 /tmp/ptyrun.py bash -l -i -c true >/dev/null 2>&1 </dev/null; tr -d '\r' </tmp/pty.out 2>/dev/null; }
printf 'prova\n' >/run/pmgr-reboot-required
L="$(login_out)"
grep -q 'riavvio necessario' <<<"$L" && ok || ko "il login interattivo non mostra l'avviso: [$L]"
grep -q 'restart required' <<<"$L" && ok || ko "manca la riga in inglese"
grep -q 'sudo reboot' <<<"$L" && ok || ko "l'avviso non dice cosa fare"
N="$(bash -lc true 2>&1)"; [ -z "$N" ] && ok || ko "una shell di login NON interattiva (script) non deve stampare: [$N]"
N="$(bash -l -i -c true 2>&1 </dev/null | cat)"; ! grep -q 'riavvio' <<<"$N" && ok || ko "con l'uscita su pipe non deve stampare"
rm -f /run/pmgr-reboot-required
L="$(login_out)"; grep -q 'riavvio' <<<"$L" && ko "senza motivi l'avviso non deve comparire" || ok

if [ "${FUNCTIONAL:-0}" = 1 ]; then
  echo "-- giro REALE di aggiornamenti (solo sicurezza)"
  if [ "$FAM" = deb ]; then
    apt-get update -qq >/dev/null 2>&1
    timeout 900 unattended-upgrade -d >/tmp/uu.out 2>&1; rc=$?
    [ "$rc" = 0 ] && ok || ko "unattended-upgrade reale: uscita $rc: $(tail -3 /tmp/uu.out | tr '\n' ' ')"
    grep -E 'Packages that will be upgraded|No packages found|All upgrades installed|Allowed origins' /tmp/uu.out | head -3 | cut -c1-160 | sed 's/^/   /'
    unattended-upgrade --dry-run -d 2>&1 | grep -q 'No packages found that can be upgraded unattended' && ok || ko "dopo il giro restano aggiornamenti di sicurezza non installati"
    echo "   aggiornamenti NON di sicurezza rimasti (attesi, non installati): $(apt list --upgradable 2>/dev/null | grep -c upgradable)"
  else
    timeout 1500 dnf-automatic /etc/dnf/automatic.conf >/tmp/da.out 2>&1; rc=$?
    [ "$rc" = 0 ] && ok || ko "dnf-automatic reale: uscita $rc: $(tail -3 /tmp/da.out | tr '\n' ' ')"
    left=$(dnf -q updateinfo list --updates security 2>/dev/null | grep -cE 'RL(SA|BA|EA)|Moderate|Important|Critical|Low')
    [ "$left" = 0 ] && ok || ko "restano $left avvisi di sicurezza non installati"
    dnf -q check-update >/dev/null 2>&1; [ "$?" = 100 ] && echo "   aggiornamenti NON di sicurezza rimasti (attesi, non installati): si'" && ok || ko "ci si aspettava che restassero aggiornamenti non di sicurezza (se non restano non si puo' provare che sono esclusi)"
    sed -n '1,6p' /tmp/da.out | cut -c1-140 | sed 's/^/   /'
  fi
fi
echo "   RISULTATO $WANT: $PASS ok, $FAIL falliti"
[ "$FAIL" = 0 ]
