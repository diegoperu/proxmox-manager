#!/bin/sh
# pmgr-reboot-check - dice se serve un riavvio dopo gli aggiornamenti (ProxmoxManager). Solo ASCII.
#
# Uso:
#   pmgr-reboot-check                 esce 0 se NON serve il riavvio, 1 se serve (stampa il motivo)
#   pmgr-reboot-check --update-flag   come sopra, con controlli piu' lenti (needs-restarting), e scrive o toglie
#                                     /run/pmgr-reboot-required (in /run: sparisce da solo al riavvio)
#
# Cosa guarda: il file /var/run/reboot-required dei pacchetti Debian/Ubuntu; un kernel installato piu' nuovo
# di quello in esecuzione (rpm su Rocky, /boot su Debian/Ubuntu, stesso "sapore" del kernel); il flag scritto
# da un controllo precedente.
FLAG=/run/pmgr-reboot-required
reasons=""
add() { reasons="${reasons:+$reasons; }$1"; }

if [ -e /var/run/reboot-required ]; then
  pk=""
  [ -r /var/run/reboot-required.pkgs ] && pk=$(tr '\n' ' ' </var/run/reboot-required.pkgs | cut -c1-100)
  add "pacchetti: ${pk:-segnalato dal sistema}"
fi

running=$(uname -r)
newest=""
if command -v rpm >/dev/null 2>&1 && rpm -q kernel-core >/dev/null 2>&1; then
  # l'ultimo installato (non "il piu' alto con sort -V": per sort 553.el8_10 viene dopo 553.184.1 e darebbe un riavvio inesistente)
  newest=$(rpm -q kernel-core --last 2>/dev/null | head -n 1 | sed -E 's/^kernel-core-([^ ]+) .*/\1/')
elif [ -d /boot ]; then
  flavor=$(printf '%s' "$running" | sed -E 's/^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+//')
  newest=$(ls /boot 2>/dev/null | sed -n 's/^vmlinuz-//p' | grep -E -- "${flavor}\$" | sort -V | tail -n 1)
fi
if [ -n "$newest" ] && [ "$newest" != "$running" ]; then
  if command -v rpm >/dev/null 2>&1 && rpm -q kernel-core >/dev/null 2>&1; then
    # rpm: l'ultimo installato e' per definizione quello da avviare
    add "kernel $newest installato, in uso $running"
  else
    higher=$(printf '%s\n%s\n' "$running" "$newest" | sort -V | tail -n 1)
    [ "$higher" = "$newest" ] && add "kernel $newest installato, in uso $running"
  fi
fi

if [ "${1:-}" = "--update-flag" ]; then
  if command -v needs-restarting >/dev/null 2>&1; then
    needs-restarting -r >/dev/null 2>&1
    [ "$?" = 1 ] && add "servizi o librerie aggiornati (needs-restarting)"
  fi
  if [ -n "$reasons" ]; then printf '%s\n' "$reasons" >"$FLAG"; else rm -f "$FLAG"; fi
elif [ -s "$FLAG" ]; then
  add "$(head -n 1 "$FLAG")"
fi

if [ -n "$reasons" ]; then
  printf '%s\n' "$reasons"
  exit 1
fi
exit 0
