#!/bin/bash
# pmgr-fail2ban-update.sh - porta una VM GIA' ESISTENTE alla configurazione fail2ban degli snippet
# cloud-init (ProxmoxManager): sshd, 5 tentativi falliti in 15 minuti = ban di 1 ora, ban progressivo
# alla recidiva (raddoppia fino a 64 ore), memoria delle recidive per 4 settimane, avvio al boot.
# Va eseguito DENTRO la VM, come root. Idempotente. Solo ASCII.
#
# Uso:  pmgr-fail2ban-update.sh [--check] [--print jail|global|override]
#   --check          non scrive niente: dice cosa cambierebbe (uscita 3 se qualcosa)
#   --print jail|global   stampa il contenuto di uno dei due file di configurazione ed esce
#
# Cosa fa:
#  1. scrive /etc/fail2ban/jail.d/50-pmgr.local e /etc/fail2ban/fail2ban.d/50-pmgr.local;
#  2. se esiste /etc/fail2ban/jail.local ed e' la vecchia copia di jail.conf fatta dai vecchi snippet
#     (uguale a jail.conf tranne le righe "bantime"), la mette da parte: annullava le impostazioni
#     della distribuzione (azione di ban, backend). Un jail.local con altre personalizzazioni NON si tocca;
#  3. controlla con fail2ban-client -d che la configurazione caricata sia quella attesa;
#     se non lo e', rimette tutto com'era e esce con errore;
#  4. abilita e riavvia fail2ban, e controlla che la jail sshd risponda (altrimenti torna indietro).
# Le copie di sicurezza stanno in /var/backups/pmgr-fail2ban/<data>/.
#
# Uscite: 0 ok / niente da fare, 1 errore (config ripristinata), 2 uso errato, 3 --check con modifiche.
set -euo pipefail
umask 022
export LC_ALL=C

readonly JAIL_FILE="/etc/fail2ban/jail.d/50-pmgr.local"
readonly GLOBAL_FILE="/etc/fail2ban/fail2ban.d/50-pmgr.local"
readonly OLD_JAIL_LOCAL="/etc/fail2ban/jail.local"
readonly OVR_FILE="/etc/fail2ban/jail.d/51-pmgr-sshd.local"
readonly UNIT_FILE="/etc/systemd/system/fail2ban.service.d/50-pmgr.conf"
readonly BACKUP_ROOT="/var/backups/pmgr-fail2ban"

# fail2ban e' un processo Python: un "sudo pkill -f python" di un utente lo uccide e systemd lo da per fermo. Con Restart=always
# (e nessun limite di riavvii, se qualcuno ripete il comando in ciclo) torna su da solo. Un "systemctl stop" voluto resta fermo.
print_unit() {
  cat <<'EOF'
# Gestito da ProxmoxManager: fail2ban deve ripartire da solo se qualcuno lo uccide (es. "pkill -f python").
# Un arresto voluto con "systemctl stop fail2ban" resta fermo.
[Unit]
StartLimitIntervalSec=0

[Service]
Restart=always
RestartSec=10
# l'unit di Debian ha RestartPreventExitStatus=0 255 e fail2ban ucciso con TERM esce con 255: senza azzerarla non riparte mai
RestartPreventExitStatus=
EOF
}

# Porte su cui ascolta sshd (da "sshd -T"), separate da virgola; 22 se non si riesce a leggerle (es. senza root).
# Una VM con SSH su un'altra porta (es. 24 o 2022, con la 22 occupata da un contenitore) deve far bannare QUELLA porta.
ssh_ports() {
  local p
  p="$(sshd -T 2>/dev/null | awk '$1 == "port" && $2 ~ /^[0-9]+$/ { print $2 }' | sort -un | paste -sd, -)"
  [ -n "$p" ] || p=22
  printf '%s' "$p"
}

print_jail() {
  : "${SSH_PORTS:=$(ssh_ports)}"
  cat <<'EOF'
# Gestito da ProxmoxManager (snippet cloud-init): vale sopra le impostazioni della distribuzione.
# sshd: 5 tentativi falliti in 15 minuti = ban di 1 ora; a ogni recidiva il ban raddoppia (1h 2h 4h ... 64h).
[DEFAULT]
bantime = 1h
findtime = 15m
maxretry = 5
bantime.increment = true
bantime.multipliers = 1 2 4 8 16 32 64
bantime.maxtime = 1w

[sshd]
enabled = true
backend = systemd
EOF
  # porta diversa dalla 22: la jail deve bannare quella, non la porta "ssh" (22) di un eventuale contenitore
  [ "$SSH_PORTS" = 22 ] || printf 'port = %s\n' "$SSH_PORTS"
}

# Valori NOSTRI per la sezione [sshd]; la sezione di un altro file (es. un jail.local copiato da un jail.conf di una versione piu'
# vecchia e modificato dal gruppo) prevale su [DEFAULT], quindi 50-pmgr.local da solo non basta. Valori PIU' SEVERI dei nostri
# scelti dal gruppo vanno rispettati: nel file di correzione finiscono solo le chiavi PIU' DEBOLI. Il jail.local non si tocca.
OUR_MAXRETRY=5
OUR_FINDTIME=900    # 15m
OUR_BANTIME=3600    # 1h

# secondi di un valore di fail2ban ("600", "10m", "1h", "-1" = permanente); vuoto se non e' leggibile (es. %(...)s)
to_seconds() {
  local v="${1//[[:space:]]/}"
  if [[ "$v" =~ ^-?[0-9]+$ ]]; then echo "$v"; return; fi
  if [[ "$v" =~ ^([0-9]+)([smhdwy])$ ]]; then
    case "${BASH_REMATCH[2]}" in s) echo $((BASH_REMATCH[1])) ;; m) echo $((BASH_REMATCH[1] * 60)) ;; h) echo $((BASH_REMATCH[1] * 3600)) ;;
      d) echo $((BASH_REMATCH[1] * 86400)) ;; w) echo $((BASH_REMATCH[1] * 604800)) ;; y) echo $((BASH_REMATCH[1] * 31536000)) ;; esac
  fi
}
# vero se il valore NON e' piu' severo di quello nostro (o non e' leggibile: nel dubbio si impone il nostro)
is_weaker() { # chiave valore
  local s
  case "$1" in
    maxretry) [[ "${2//[[:space:]]/}" =~ ^[0-9]+$ ]] && [ "${2//[[:space:]]/}" -le "$OUR_MAXRETRY" ] && return 1; return 0 ;;
    findtime) s="$(to_seconds "$2")"; [ -n "$s" ] && [ "$s" -ge "$OUR_FINDTIME" ] && return 1; return 0 ;;
    bantime) s="$(to_seconds "$2")"; { [ -n "$s" ] && { [ "$s" -ge "$OUR_BANTIME" ] || [ "$s" = -1 ]; }; } && return 1; return 0 ;;
  esac
  return 0
}

# "chiave valore" per ogni maxretry/findtime/bantime impostato dentro [sshd] da file diversi dai nostri, in ordine di caricamento (l'ultimo vince)
sshd_section_values() {
  local f
  for f in /etc/fail2ban/jail.d/*.conf /etc/fail2ban/jail.local /etc/fail2ban/jail.d/*.local; do
    [ -f "$f" ] || continue
    [ "$f" != "$JAIL_FILE" ] && [ "$f" != "$OVR_FILE" ] || continue
    awk '/^[[:space:]]*\[/ { s = ($0 ~ /^[[:space:]]*\[sshd\][[:space:]]*$/); next }
         s && /^[[:space:]]*(maxretry|findtime|bantime)[[:space:]]*=/ { k = $0; sub(/[[:space:]]*=.*/, "", k); gsub(/[[:space:]]/, "", k); v = $0; sub(/^[^=]*=[[:space:]]*/, "", v); sub(/[[:space:]]*(#|;).*$/, "", v); print k, v }' "$f"
  done | awk '{ k = $1; $1 = ""; sub(/^ /, ""); val[k] = $0 } END { for (k in val) print k, val[k] }'
}
# chiavi di [sshd] da correggere (piu' deboli delle nostre), una per riga
weak_sshd_keys() {
  local k v
  while read -r k v; do [ -n "$k" ] || continue; if is_weaker "$k" "$v"; then echo "$k"; fi; done < <(sshd_section_values)
}
print_override() { # chiavi deboli ... -> file di correzione con i soli valori nostri per quelle chiavi
  local k
  cat <<'EOF'
# Gestito da ProxmoxManager: un altro file di fail2ban (es. jail.local) imposta questi valori nella sezione [sshd] con valori PIU' DEBOLI
# dei nostri e prevale su [DEFAULT]. Qui solo le chiavi da correggere (5 tentativi in 15 minuti = ban di 1 ora); valori piu' severi non si toccano.
[sshd]
EOF
  for k in "$@"; do
    case "$k" in maxretry) echo "maxretry = $OUR_MAXRETRY" ;; findtime) echo "findtime = 15m" ;; bantime) echo "bantime = 1h" ;; esac
  done
}

print_global() {
  cat <<'EOF'
# Il ban progressivo conta le recidive nel database: deve ricordarle almeno quanto dura il ban massimo.
[Definition]
dbpurgeage = 4w
EOF
}

CHECK=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check) CHECK=1; shift ;;
    --print)
      case "${2:-}" in
        jail) print_jail; exit 0 ;;
        global) print_global; exit 0 ;;
        override) print_override maxretry findtime bantime; exit 0 ;;
        *) echo "uso: --print jail|global|override" >&2; exit 2 ;;
      esac
      ;;
    -h | --help) sed -n '2,23p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "opzione sconosciuta: $1" >&2; exit 2 ;;
  esac
done

die() { printf 'ERRORE: %s\n' "$*" >&2; exit 1; }
say() { printf '  %s\n' "$*"; }

[ "$(id -u)" = 0 ] || die "va eseguito come root (sudo)"
# Solo strumenti sempre presenti (coreutils, grep, sed): su Rocky minimale mancano per esempio cmp e diff.
for c in sha256sum cut grep sed install mktemp date seq tr cp rm mkdir head; do
  command -v "$c" >/dev/null 2>&1 || die "comando mancante: $c"
done
same_file() { # due file hanno lo stesso contenuto? (senza cmp/diff)
  [ -f "$1" ] && [ -f "$2" ] && [ "$(sha256sum <"$1" | cut -d' ' -f1)" = "$(sha256sum <"$2" | cut -d' ' -f1)" ]
}
if ! command -v fail2ban-client >/dev/null 2>&1; then
  echo "fail2ban non e' installato su questa VM: niente da fare."
  exit 0
fi
python3 -c 'import systemd.journal' >/dev/null 2>&1 \
  || die "manca il modulo python di systemd (pacchetto python3-systemd): serve al backend systemd. Installalo e rilancia."

CHANGES=0
STAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP="$BACKUP_ROOT/$STAMP"
HAVE_SYSTEMD=0
[ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1 && HAVE_SYSTEMD=1
# Il ban vero lo fa un programma esterno scelto dall'azione della jail (nft, iptables, firewall-cmd): se manca, fail2ban
# registra il ban ma non blocca niente ("nft: not found" nel log). Capitato su Debian 13 (immagine cloud senza nftables).
# Stampa "binario pacchetto" se manca, niente se c'e'.
ban_binary_missing() {
  local dump act bin pkg
  dump="${1:-$(fail2ban-client -d 2>/dev/null || true)}"
  act="$(grep -o "\['set', 'sshd', 'addaction', '[^']*'\]" <<<"$dump" | head -n 1 | sed "s/.*'addaction', '//; s/'\]//")"
  case "$act" in
    nftables*) bin=nft; pkg=nftables ;;
    iptables*) bin=iptables; pkg=iptables ;;
    firewallcmd*) bin=firewall-cmd; pkg=firewalld ;;
    *) return 0 ;;
  esac
  command -v "$bin" >/dev/null 2>&1 || [ -x "/usr/sbin/$bin" ] || [ -x "/sbin/$bin" ] || echo "$bin $pkg"
  return 0
}

TMP="$(mktemp -d)"
trap 'rm -rf -- "$TMP"' EXIT

echo "pmgr-fail2ban-update: $(fail2ban-client --version | head -1)$([ "$CHECK" = 1 ] && echo ' (solo controllo)')"

# ---------------------------------------------------------------- 1. piano
SSH_PORTS="$(ssh_ports)"
print_jail >"$TMP/jail.new"
print_global >"$TMP/global.new"
WRITE_JAIL=0
WRITE_GLOBAL=0
MOVE_OLD=0
same_file "$TMP/jail.new" "$JAIL_FILE" || WRITE_JAIL=1
same_file "$TMP/global.new" "$GLOBAL_FILE" || WRITE_GLOBAL=1
WRITE_OVR=0
WRITE_UNIT=0
if [ "$HAVE_SYSTEMD" = 1 ]; then
  print_unit >"$TMP/unit.new"
  same_file "$TMP/unit.new" "$UNIT_FILE" || WRITE_UNIT=1
fi

if [ -e "$OLD_JAIL_LOCAL" ]; then
  [ ! -L "$OLD_JAIL_LOCAL" ] || die "$OLD_JAIL_LOCAL e' un link simbolico: non lo tocco"
  # grep esce con 1 se non resta nessuna riga: non e' un errore
  grep -v '^bantime' "$OLD_JAIL_LOCAL" >"$TMP/local.nobt" || true
  grep -v '^bantime' /etc/fail2ban/jail.conf >"$TMP/conf.nobt" 2>/dev/null || true
  if [ -f /etc/fail2ban/jail.conf ] && same_file "$TMP/local.nobt" "$TMP/conf.nobt"; then
    MOVE_OLD=1
  else
    say "[avviso]  $OLD_JAIL_LOCAL contiene personalizzazioni (o e' una copia di un jail.conf piu' vecchio): non lo tocco."
    say "          I valori scritti in jail.d/50-pmgr.local hanno la precedenza su quelli di [DEFAULT]; se li ridefinisce nella sezione [sshd] controllalo a mano."
  fi
fi

# il file di correzione per [sshd] serve solo se un altro file ridefinisce i valori (e non e' la vecchia copia che sta per essere messa da parte)
if [ "$MOVE_OLD" = 0 ]; then
  mapfile -t WEAK < <(weak_sshd_keys | sort)
  if [ "${#WEAK[@]}" -gt 0 ]; then
    print_override "${WEAK[@]}" >"$TMP/ovr.new"
    same_file "$TMP/ovr.new" "$OVR_FILE" || WRITE_OVR=1
    [ "$WRITE_OVR" = 1 ] || say "[ok]      $OVR_FILE (${WEAK[*]})"
  fi
fi

SERVICE_ENABLED=1
SERVICE_ACTIVE=1
if [ "$HAVE_SYSTEMD" = 1 ]; then
  systemctl is-enabled --quiet fail2ban 2>/dev/null || SERVICE_ENABLED=0
  systemctl is-active --quiet fail2ban 2>/dev/null || SERVICE_ACTIVE=0
fi

[ "$WRITE_JAIL" = 0 ] && say "[ok]      $JAIL_FILE" || { CHANGES=$((CHANGES + 1)); say "[da fare] scrivere $JAIL_FILE"; }
[ "$WRITE_GLOBAL" = 0 ] && say "[ok]      $GLOBAL_FILE" || { CHANGES=$((CHANGES + 1)); say "[da fare] scrivere $GLOBAL_FILE"; }
if [ "$HAVE_SYSTEMD" = 1 ]; then
  [ "$WRITE_UNIT" = 0 ] && say "[ok]      $UNIT_FILE (riavvio automatico)" || { CHANGES=$((CHANGES + 1)); say "[da fare] scrivere $UNIT_FILE (fail2ban riparte da solo se ucciso)"; }
fi
if [ "$MOVE_OLD" = 1 ]; then CHANGES=$((CHANGES + 1)); say "[da fare] mettere da parte la vecchia copia di jail.conf ($OLD_JAIL_LOCAL)"; fi
if [ "$WRITE_OVR" = 1 ]; then CHANGES=$((CHANGES + 1)); say "[da fare] scrivere $OVR_FILE (un altro file imposta in [sshd] valori piu' deboli dei nostri: ${WEAK[*]})"; fi
if [ "$HAVE_SYSTEMD" = 1 ]; then
  [ "$SERVICE_ENABLED" = 1 ] && say "[ok]      fail2ban abilitato all'avvio" || { CHANGES=$((CHANGES + 1)); say "[da fare] abilitare fail2ban all'avvio"; }
  [ "$SERVICE_ACTIVE" = 1 ] && say "[ok]      fail2ban in esecuzione" || { CHANGES=$((CHANGES + 1)); say "[da fare] avviare fail2ban"; }
else
  say "[avviso]  systemd non attivo qui: abilitazione e riavvio del servizio non gestiti"
fi

BANMISS="$(ban_binary_missing)"
if [ -n "$BANMISS" ]; then CHANGES=$((CHANGES + 1)); say "[da fare] installare ${BANMISS#* } (manca ${BANMISS%% *}: senza, i ban non bloccano niente)"; fi

if [ "$CHECK" = 1 ]; then
  if [ "$CHANGES" -gt 0 ]; then echo "Modifiche da fare: $CHANGES"; exit 3; fi
  echo "Niente da fare."
  exit 0
fi
if [ "$CHANGES" = 0 ]; then
  echo "Niente da fare."
  exit 0
fi

# ---------------------------------------------------------------- 2. applicazione, con ritorno indietro
mkdir -p -m 700 -- "$BACKUP"
UNDO=()   # comandi per tornare indietro, eseguiti in ordine inverso

rollback() {
  local i
  echo "Ripristino della configurazione precedente..." >&2
  for ((i = ${#UNDO[@]} - 1; i >= 0; i--)); do
    eval "${UNDO[$i]}" || echo "  (ripristino non riuscito: ${UNDO[$i]})" >&2
  done
  if [ "$HAVE_SYSTEMD" = 1 ] && [ "$SERVICE_ACTIVE" = 1 ]; then
    systemctl restart fail2ban >/dev/null 2>&1 || true
  fi
}

install_file() { # nuovo destinazione
  local new="$1" dst="$2" name
  name="$(echo "$dst" | tr '/' '_')"
  if [ -e "$dst" ]; then
    cp -a -- "$dst" "$BACKUP/$name"
    UNDO+=("cp -a -- '$BACKUP/$name' '$dst'")
  else
    UNDO+=("rm -f -- '$dst'")
  fi
  install -m 644 -o root -g root -- "$new" "$dst"
}

[ "$WRITE_JAIL" = 0 ] || { install_file "$TMP/jail.new" "$JAIL_FILE"; say "[fatto]   $JAIL_FILE"; }
[ "$WRITE_GLOBAL" = 0 ] || { install_file "$TMP/global.new" "$GLOBAL_FILE"; say "[fatto]   $GLOBAL_FILE"; }
[ "$WRITE_OVR" = 0 ] || { install_file "$TMP/ovr.new" "$OVR_FILE"; say "[fatto]   $OVR_FILE"; }
if [ "$WRITE_UNIT" = 1 ]; then
  mkdir -p -- "$(dirname "$UNIT_FILE")"
  # nel ritorno indietro (ordine inverso) il daemon-reload deve venire DOPO la rimozione del file
  UNDO+=("systemctl daemon-reload >/dev/null 2>&1")
  install_file "$TMP/unit.new" "$UNIT_FILE"
  systemctl daemon-reload || { rollback; die "systemctl daemon-reload non riuscito: configurazione ripristinata."; }
  say "[fatto]   $UNIT_FILE"
fi
if [ "$MOVE_OLD" = 1 ]; then
  cp -a -- "$OLD_JAIL_LOCAL" "$BACKUP/jail.local"
  UNDO+=("cp -a -- '$BACKUP/jail.local' '$OLD_JAIL_LOCAL'")
  rm -f -- "$OLD_JAIL_LOCAL"
  say "[fatto]   vecchio jail.local messo da parte in $BACKUP/jail.local"
fi

# ---------------------------------------------------------------- 3. controllo della configurazione caricata
DUMP="$(fail2ban-client -d 2>&1)" || { echo "$DUMP" | tail -5 >&2; rollback; die "fail2ban non riesce a leggere la nuova configurazione: tutto ripristinato."; }
missing=""
for want in "['start', 'sshd']" "['add', 'sshd', 'systemd']" "['set', 'sshd', 'bantime.increment', True]" \
  "['set', 'sshd', 'bantime.multipliers', '1 2 4 8 16 32 64']" "['set', 'sshd', 'bantime.maxtime', '1w']" \
  "['set', 'dbpurgeage', '4w']"; do
  grep -qF -- "$want" <<<"$DUMP" || missing="$missing
    $want"
done
# maxretry/findtime/bantime: devono essere i nostri OPPURE piu' severi (scelta del gruppo, da rispettare)
for key in maxretry findtime bantime; do
  val="$(sed -n "s/^\['set', 'sshd', '$key', \(.*\)\]$/\1/p" <<<"$DUMP" | tail -n 1 | tr -d "'")"
  if [ -z "$val" ] || is_weaker "$key" "$val"; then missing="$missing
    $key (valore in uso: ${val:-nessuno}, minimo richiesto: nostro o piu' severo)"; fi
done
if [ -n "$missing" ]; then
  rollback
  die "la configurazione caricata non contiene:$missing
tutto ripristinato. Probabile causa: valori ridefiniti in un altro file di /etc/fail2ban (vedi 'fail2ban-client -d')."
fi
if [ "$SSH_PORTS" != 22 ]; then
  act="$(grep -o "\['set', 'sshd', 'addaction', '[^']*'\]" <<<"$DUMP" | head -n 1 | sed "s/.*'addaction', '//; s/'\]//")"
  # nel dump la porta dell'azione e' una coppia ['port', '<valore>'] dentro la riga multi-set dell'azione della jail sshd
  if ! { grep -F "['multi-set', 'sshd', 'action', '$act'," <<<"$DUMP" | grep -qF "['port', '$SSH_PORTS']"; }; then
    rollback
    die "la jail sshd non banna la porta $SSH_PORTS (azione ${act:-sconosciuta}): tutto ripristinato. Un altro file di /etc/fail2ban ridefinisce 'port'? (vedi 'fail2ban-client -d | grep port')."
  fi
  say "[ok]      la jail sshd banna la porta $SSH_PORTS (sshd non e' sulla 22)"
fi
BANMISS="$(ban_binary_missing "$DUMP")"
if [ -n "$BANMISS" ]; then
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${BANMISS#* }" >/dev/null 2>&1 || { rollback; die "installazione di ${BANMISS#* } non riuscita (apt-get update?): tutto ripristinato."; }
  elif command -v dnf >/dev/null 2>&1; then
    dnf -y -q install "${BANMISS#* }" >/dev/null 2>&1 || { rollback; die "installazione di ${BANMISS#* } non riuscita: tutto ripristinato."; }
  else
    rollback; die "manca ${BANMISS%% *} e non so come installarlo qui: tutto ripristinato."
  fi
  say "[fatto]   installato ${BANMISS#* } (serve all'azione di ban)"
fi
say "[ok]      configurazione caricata: sshd, 5 tentativi / 15 min, ban 1 h, ban progressivo, backend systemd"

# ---------------------------------------------------------------- 4. servizio
if [ "$HAVE_SYSTEMD" = 1 ]; then
  if [ "$SERVICE_ENABLED" = 0 ]; then
    systemctl enable fail2ban >/dev/null 2>&1 || { rollback; die "systemctl enable fail2ban non riuscito"; }
    UNDO+=("systemctl disable fail2ban >/dev/null 2>&1")
    say "[fatto]   fail2ban abilitato all'avvio"
  fi
  if [ "$(systemctl show fail2ban -p Restart --value 2>/dev/null)" != always ]; then
    rollback; die "systemd non applica Restart=always a fail2ban (un altro drop-in lo annulla?): tutto ripristinato."
  fi
  systemctl restart fail2ban || { rollback; die "il riavvio di fail2ban non e' riuscito: configurazione ripristinata."; }
  ok=0
  for _ in $(seq 1 15); do
    if fail2ban-client status sshd >/dev/null 2>&1; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" = 0 ]; then
    rollback
    die "dopo il riavvio la jail sshd non risponde: configurazione ripristinata (vedi: journalctl -u fail2ban)."
  fi
  say "[fatto]   fail2ban riavviato, jail sshd attiva"
fi
echo "Fatto. Copie di sicurezza in $BACKUP"
