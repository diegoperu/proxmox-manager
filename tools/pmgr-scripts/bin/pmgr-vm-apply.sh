#!/bin/bash
# pmgr-vm-apply.sh - porta una VM GIA' ESISTENTE alla configurazione degli snippet cloud-init.
# Va eseguito DENTRO la VM, come root (di solito lo lancia pmgr-vm-rollout.sh via ssh). Idempotente.
# Usa gli stessi file dei profili degli snippet (profiles/updates, profiles/hosting): una sola sorgente. Solo ASCII.
#
# Uso:  pmgr-vm-apply.sh --module fail2ban|updates|ssh-hosting|hardening-hosting|baseline [--check] [--allow-no-keys] [--from DIR]
#       pmgr-vm-apply.sh --verify [--profile standard|hosting]
#   (moduli per cluster: calcolo = baseline + ssh-standard;  hosting = baseline + ssh-hosting + firewall-hosting)
#   --module M        agent-exec  = Rocky: sblocca guest-exec nell'agent QEMU, aggiorna gli helper del manager in /usr/local/libexec/pmgr
#                                   (etichetta SELinux virt_qemu_ga_unconfined_exec_t, boolean virt_qemu_ga_run_unconfined) e riavvia il solo
#                                   qemu-guest-agent. Su Debian/Ubuntu niente da fare. Serve perche' il manager aggiunga utenti e dischi
#                     fail2ban    = sshd: 5 tentativi in 15 minuti = ban di 1 ora, ban progressivo (pmgr-fail2ban-update.sh)
#                     updates     = aggiornamenti di SICUREZZA automatici due volte al giorno, nessun riavvio
#                                   automatico, avviso di riavvio al login (console e SSH)
#                     ssh-hosting = SSH solo con chiave, rimesso a ogni avvio e ogni 15 minuti (pmgr-ssh-enforce):
#                                   SOLO per le VM di un cluster di hosting, mai nel calcolo
#                     ssh-standard = accesso SSH amministrativo garantito nel CALCOLO: root non entra, le chiavi pubbliche
#                                   sono accettate, Include di sshd_config.d in testa; controlla che l'utente con cui si
#                                   lancia (SUDO_USER, di solito tecnico) possa davvero entrare. NON tocca PasswordAuthentication:
#                                   chi alza la sicurezza (password disattivata) non viene riportato indietro. Si rifiuta su
#                                   una VM di hosting
#                     firewall-hosting = firewall chiuso in ingresso tranne 22, 80 e 443 (ufw su Debian/Ubuntu, firewalld su
#                                   Rocky, senza cockpit). Su una VM con il firewall spento si RIFIUTA se qualche servizio ascolta su
#                                   altre porte: indicale con --open (restano aperte) o accetta la chiusura con --allow-close-ports
#                     hardening-hosting = sysctl di rete e kernel (/etc/sysctl.d/90-pmgr-hosting.conf) e jail recidive di
#                                   fail2ban (3 ban in un giorno = ban di una settimana); richiede fail2ban (modulo baseline).
#                                   Solo VM di hosting. NON toglie i pacchetti gia' installati (i clienti potrebbero usarli)
#                     baseline    = fail2ban + updates (le VM di tutti i cluster; NON comprende i moduli ssh-*)
#   --check           non scrive niente: dice cosa cambierebbe (uscita 3 se qualcosa)
#   --verify          sola lettura: controlla che la VM sia nello stato atteso (agent, fail2ban, aggiornamenti, SSH,
#                     helper e SELinux su Rocky, cloud-init) e stampa [OK]/[FALLITO]; uscita 1 se qualcosa non va
#   --profile P       con --verify: standard (calcolo: root no, chiavi accettate, password a scelta della VM) o hosting (solo chiave)
#   --open LISTA      firewall-hosting: altre porte da lasciare aperte, es. 3306/tcp,53/udp (separate da virgola)
#   --allow-close-ports firewall-hosting: accetta di chiudere le altre porte in ascolto
#   --allow-no-keys   ssh-hosting: procede anche se non trova nessuna chiave in authorized_keys (rischio di blocco!)
#   --from DIR        cartella con updates/ e hosting/ (predefinita: ../profiles rispetto a questo script)
#
# Prima di modificare salva i file toccati in /var/backups/pmgr-vm/<data>/; se il controllo finale non
# passa rimette tutto com'era. ssh-hosting si rifiuta di procedere se nessun utente ha una chiave
# in authorized_keys: dopo l'applicazione chi entra con la password non entra piu' (resta la console).
#
# Uscite: 0 ok / niente da fare, 1 errore (file ripristinati), 2 uso errato, 3 --check con modifiche.
set -euo pipefail
umask 022
export LC_ALL=C

MODULES=()
CHECK=0
ALLOW_NO_KEYS=0
FROM=""
VERIFY=0
PROFILE=standard
OPEN_PORTS=()
ALLOW_CLOSE=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --module)
      [ "$#" -ge 2 ] || { echo "--module vuole un valore" >&2; exit 2; }
      case "$2" in
        fail2ban | updates | ssh-hosting | ssh-standard | firewall-hosting | hardening-hosting | agent-exec) MODULES+=("$2") ;;
        baseline) MODULES+=(fail2ban updates) ;;
        *) echo "modulo sconosciuto: $2" >&2; exit 2 ;;
      esac
      shift 2
      ;;
    --check) CHECK=1; shift ;;
    --verify) VERIFY=1; shift ;;
    --allow-close-ports) ALLOW_CLOSE=1; shift ;;
    --open)
      [ "$#" -ge 2 ] || { echo "--open vuole un elenco" >&2; exit 2; }
      IFS=',' read -r -a _ports <<<"$2"
      for _p in "${_ports[@]}"; do
        [[ "$_p" =~ ^[0-9]{1,5}/(tcp|udp)$ ]] && [ "${_p%%/*}" -ge 1 ] && [ "${_p%%/*}" -le 65535 ] || { echo "porta non valida: '$_p' (formato 3306/tcp)" >&2; exit 2; }
        OPEN_PORTS+=("$_p")
      done
      shift 2
      ;;
    --profile)
      [ "$#" -ge 2 ] || { echo "--profile vuole un valore" >&2; exit 2; }
      case "$2" in standard | hosting) PROFILE="$2" ;; *) echo "profilo sconosciuto: $2" >&2; exit 2 ;; esac
      shift 2
      ;;
    --allow-no-keys) ALLOW_NO_KEYS=1; shift ;;
    --from)
      [ "$#" -ge 2 ] || { echo "--from vuole una cartella" >&2; exit 2; }
      FROM="$2"
      shift 2
      ;;
    -h | --help) sed -n '2,36p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "opzione sconosciuta: $1" >&2; exit 2 ;;
  esac
done
[ "$VERIFY" = 1 ] || [ "${#MODULES[@]}" -gt 0 ] || { echo "manca --module (o --verify)" >&2; exit 2; }
[ "$VERIFY" = 0 ] || [ "${#MODULES[@]}" = 0 ] || { echo "--verify non si combina con --module" >&2; exit 2; }
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
SELF_DIR="$(dirname "$SELF")"
[ -n "$FROM" ] || FROM="$(cd "$(dirname "${BASH_SOURCE[0]}")/../profiles" 2>/dev/null && pwd)" || FROM=""
[[ "$FROM" =~ ^/[A-Za-z0-9._/-]+$ ]] && [ -d "$FROM/updates" ] && [ -d "$FROM/hosting" ] || { echo "cartella dei profili non valida: ${FROM:-non trovata}" >&2; exit 2; }

die() { printf 'ERRORE: %s\n' "$*" >&2; exit 1; }
say() { printf '  %s\n' "$*"; }
[ "$(id -u)" = 0 ] || die "va eseguito come root (sudo)"
for c in sha256sum cut grep sed install mktemp date tr cp rm mkdir stat awk head tail; do
  command -v "$c" >/dev/null 2>&1 || die "comando mancante: $c"
done

. /etc/os-release
case "${ID:-}" in
  ubuntu) FAM=ubuntu ;;
  debian) FAM=debian ;;
  rocky | rhel | almalinux | centos) FAM=rocky ;;
  *) die "sistema operativo non gestito: ${ID:-sconosciuto}" ;;
esac
HAVE_SYSTEMD=0
{ [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; } && HAVE_SYSTEMD=1

CHANGES=0
STAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP="/var/backups/pmgr-vm/$STAMP"
UNDO=()
TMP="$(mktemp -d)"
trap 'rm -rf -- "$TMP"' EXIT

same_file() { [ -f "$1" ] && [ -f "$2" ] && [ "$(sha256sum <"$1" | cut -d' ' -f1)" = "$(sha256sum <"$2" | cut -d' ' -f1)" ]; }

rollback() {
  local i
  echo "Ripristino dei file originali..." >&2
  for ((i = ${#UNDO[@]} - 1; i >= 0; i--)); do
    eval "${UNDO[$i]}" || echo "  (ripristino non riuscito: ${UNDO[$i]})" >&2
  done
  [ "$HAVE_SYSTEMD" = 1 ] && systemctl daemon-reload >/dev/null 2>&1 || true
}

# ensure_file SORGENTE DESTINAZIONE MODO
ensure_file() {
  local src="$1" dst="$2" mode="$3" cur key
  cur="$(stat -c %a -- "$dst" 2>/dev/null || echo none)"
  if same_file "$src" "$dst" && [ "$cur" = "$mode" ]; then
    say "[ok]      $dst"
    return 0
  fi
  CHANGES=$((CHANGES + 1))
  if [ "$CHECK" = 1 ]; then
    say "[da fare] scrivere $dst"
    return 0
  fi
  mkdir -p -m 700 -- "$BACKUP"
  mkdir -p -- "$(dirname "$dst")"
  key="$(printf '%s' "$dst" | tr '/' '_')"
  if [ -e "$dst" ]; then
    cp -a -- "$dst" "$BACKUP/$key"
    UNDO+=("cp -a -- '$BACKUP/$key' '$dst'")
  else
    UNDO+=("rm -f -- '$dst'")
  fi
  install -m "$mode" -o root -g root -- "$src" "$dst"
  say "[fatto]   $dst"
}

# vero se la VM ha una scheda NVIDIA di tipo display/3D (vendor 0x10de, classe 0x03xx) vista dal bus PCI. PMGR_SYSFS_PCI serve ai test.
gpu_present() {
  local root="${PMGR_SYSFS_PCI:-/sys/bus/pci/devices}" d v c
  for d in "$root"/*; do
    [ -r "$d/vendor" ] || continue
    v="$(cat "$d/vendor" 2>/dev/null || true)"; c="$(cat "$d/class" 2>/dev/null || true)"
    if [ "$v" = 0x10de ]; then case "$c" in 0x03*) return 0 ;; esac; fi
  done
  return 1
}

# vero se la VM usa Docker: i suoi pacchetti non si aggiornano da soli (l'aggiornamento riavvia i contenitori)
docker_present() { command -v docker >/dev/null 2>&1 || [ -S /var/run/docker.sock ] || [ -S /run/docker.sock ]; }

# ensure_absent FILE: toglie il file se c'e' (con copia di sicurezza); silenzioso se non c'e'
ensure_absent() {
  local f="$1" key
  [ -e "$f" ] || return 0
  CHANGES=$((CHANGES + 1))
  if [ "$CHECK" = 1 ]; then say "[da fare] togliere $f"; return 0; fi
  mkdir -p -m 700 -- "$BACKUP"
  key="$(printf '%s' "$f" | tr '/' '_')"
  cp -a -- "$f" "$BACKUP/$key"
  UNDO+=("cp -a -- '$BACKUP/$key' '$f'")
  rm -f -- "$f"
  say "[fatto]   tolto $f"
}

pkg_installed() {
  # rpm: --whatprovides perche' "dnf-utils" e' un nome virtuale (il pacchetto vero e' yum-utils)
  if [ "$FAM" = rocky ]; then rpm -q --whatprovides "$1" >/dev/null 2>&1; else dpkg -s "$1" 2>/dev/null | grep -q '^Status: install ok installed'; fi
}
ensure_packages() { # pacchetti...
  local missing=() p
  for p in "$@"; do pkg_installed "$p" || missing+=("$p"); done
  if [ "${#missing[@]}" = 0 ]; then
    say "[ok]      pacchetti: $*"
    return 0
  fi
  CHANGES=$((CHANGES + 1))
  if [ "$CHECK" = 1 ]; then
    say "[da fare] installare: ${missing[*]}"
    return 0
  fi
  if [ "$FAM" = rocky ]; then
    dnf -y -q install "${missing[@]}" >/dev/null 2>&1 || { rollback; die "installazione di ${missing[*]} non riuscita"; }
  else
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${missing[@]}" >/dev/null 2>&1 || { rollback; die "installazione di ${missing[*]} non riuscita (apt-get update?)"; }
  fi
  say "[fatto]   installati: ${missing[*]}"
}

sysd() { # comando systemctl non critico
  [ "$HAVE_SYSTEMD" = 1 ] || return 0
  systemctl "$@" >/dev/null 2>&1 || say "[avviso]  systemctl $* non riuscito"
}

# ---------------------------------------------------------------- modulo updates
mod_updates() {
  local d="$FROM/updates"
  echo "modulo updates ($FAM)"
  if [ "$FAM" = rocky ]; then ensure_packages dnf-automatic dnf-utils; else ensure_packages unattended-upgrades; fi
  ensure_file "$d/pmgr-reboot-check.sh" /usr/local/sbin/pmgr-reboot-check 755
  ensure_file "$d/pmgr-reboot-required.sh" /etc/profile.d/pmgr-reboot-required.sh 644
  if [ "$FAM" = rocky ]; then
    if [ -f /etc/dnf/automatic.conf ]; then
      sed -E -f "$d/dnf-automatic.sed" /etc/dnf/automatic.conf >"$TMP/automatic.conf"
      local mode
      mode="$(stat -c %a /etc/dnf/automatic.conf)"
      ensure_file "$TMP/automatic.conf" /etc/dnf/automatic.conf "$mode"
    else
      CHANGES=$((CHANGES + 1))
      say "[da fare] configurare /etc/dnf/automatic.conf (dopo l'installazione di dnf-automatic)"
    fi
    ensure_file "$d/dnf-automatic.timer.conf" /etc/systemd/system/dnf-automatic.timer.d/50-pmgr.conf 644
    ensure_file "$d/dnf-automatic.service.conf" /etc/systemd/system/dnf-automatic.service.d/50-pmgr.conf 644
  else
    ensure_file "$d/apt-$FAM.conf" /etc/apt/apt.conf.d/52pmgr-unattended-upgrades 644
    ensure_file "$d/apt-daily.timer.conf" /etc/systemd/system/apt-daily.timer.d/50-pmgr.conf 644
    ensure_file "$d/apt-daily-upgrade.timer.conf" /etc/systemd/system/apt-daily-upgrade.timer.d/50-pmgr.conf 644
    # VM con scheda NVIDIA: restano gli aggiornamenti di sicurezza di tutto il sistema, ma kernel, stack NVIDIA/CUDA, docker, containerd e
    # slurm non si aggiornano da soli (vedi apt-gpu-hold.conf) e il login avvisa quando c'e' qualcosa in attesa
    if gpu_present; then
      say "[info]    scheda NVIDIA rilevata: aggiornamenti automatici con esclusioni (kernel, driver NVIDIA/CUDA, docker, slurm)"
      GPU=1
      ensure_file "$d/apt-gpu-hold.conf" /etc/apt/apt.conf.d/53pmgr-gpu-hold 644
      ensure_file "$d/pmgr-gpu-note.sh" /usr/local/sbin/pmgr-gpu-note 755
      ensure_file "$d/pmgr-gpu-note-login.sh" /etc/profile.d/pmgr-gpu-note.sh 644
    else
      ensure_absent /etc/apt/apt.conf.d/53pmgr-gpu-hold
      ensure_absent /usr/local/sbin/pmgr-gpu-note
      ensure_absent /etc/profile.d/pmgr-gpu-note.sh
    fi
    # VM con Docker (senza GPU: li' docker e' gia' nelle esclusioni della GPU): docker, containerd e runc non si aggiornano da soli
    if docker_present && ! gpu_present; then
      say "[info]    Docker rilevato: docker, containerd e runc non si aggiornano da soli (il resto si)"
      DOCKERHOLD=1
      ensure_file "$d/apt-docker-hold.conf" /etc/apt/apt.conf.d/53pmgr-docker-hold 644
    else
      ensure_absent /etc/apt/apt.conf.d/53pmgr-docker-hold
    fi
  fi
  if [ "$FAM" = rocky ] && docker_present; then
    say "[avviso]  Docker su Rocky: nessuna esclusione dei pacchetti configurata per dnf-automatic (dnf-automatic aggiorna anche docker e containerd)"
  fi
  if [ "$FAM" = rocky ] && gpu_present; then
    say "[avviso]  scheda NVIDIA su Rocky: nessuna esclusione dei pacchetti configurata per dnf-automatic (dnf-automatic aggiorna anche kernel e driver)"
  fi
  [ "$CHECK" = 1 ] && return 0
  # verifica della configurazione effettiva
  local bad=""
  if [ "$FAM" = rocky ]; then
    grep -qE '^upgrade_type = security$' /etc/dnf/automatic.conf || bad="upgrade_type"
    grep -qE '^apply_updates = yes$' /etc/dnf/automatic.conf || bad="$bad apply_updates"
  else
    local a
    a="$(apt-config dump 2>/dev/null)"
    grep -qF 'Unattended-Upgrade::Automatic-Reboot "false";' <<<"$a" || bad="Automatic-Reboot"
    grep -qF 'APT::Periodic::Unattended-Upgrade "always";' <<<"$a" || bad="$bad Periodic"
    if [ "$FAM" = ubuntu ]; then
      [ "$(grep -cE '^Unattended-Upgrade::Allowed-Origins::' <<<"$a")" = 3 ] || bad="$bad Allowed-Origins"
    else
      [ "$(grep -cE '^Unattended-Upgrade::Origins-Pattern::' <<<"$a")" = 2 ] || bad="$bad Origins-Pattern"
    fi
    if [ "${DOCKERHOLD:-0}" = 1 ]; then
      grep -qF 'Unattended-Upgrade::Package-Blacklist:: "docker.*";' <<<"$a" && grep -qF 'Unattended-Upgrade::Package-Blacklist:: "containerd.*";' <<<"$a" || bad="$bad Package-Blacklist-docker"
    fi
    if [ "${GPU:-0}" = 1 ]; then
      grep -qF 'Unattended-Upgrade::Package-Blacklist:: "linux-image-.*";' <<<"$a" && grep -qF 'Unattended-Upgrade::Package-Blacklist:: "nvidia-.*";' <<<"$a" \
        && grep -qF 'Unattended-Upgrade::Package-Blacklist:: "docker.*";' <<<"$a" || bad="$bad Package-Blacklist"
    fi
  fi
  if [ -n "${bad// /}" ]; then
    rollback
    die "la configurazione effettiva degli aggiornamenti non e' quella attesa ($bad): file ripristinati. Un altro file in /etc/apt/apt.conf.d o /etc/dnf la sta annullando?"
  fi
  say "[ok]      configurazione effettiva: solo sicurezza, nessun riavvio automatico, due volte al giorno"
  sysd daemon-reload
  if [ "$FAM" = rocky ]; then
    sysd enable dnf-automatic.timer
    sysd restart dnf-automatic.timer
  else
    sysd enable apt-daily.timer apt-daily-upgrade.timer
    sysd restart apt-daily.timer apt-daily-upgrade.timer
  fi
  /usr/local/sbin/pmgr-reboot-check --update-flag >/dev/null 2>&1 || true
  if [ "${GPU:-0}" = 1 ]; then /usr/local/sbin/pmgr-gpu-note --update-flag >/dev/null 2>&1 || true; fi
}

# ---------------------------------------------------------------- modulo ssh-hosting
count_keys() { # numero di chiavi (righe valide) in authorized_keys di root e degli utenti con shell di login
  local n=0 f c
  while IFS=: read -r user _ uid _ _ home shell; do
    { [ "$uid" = 0 ] || [ "$uid" -ge 1000 ]; } 2>/dev/null || continue
    case "$shell" in */nologin | */false | "") continue ;; esac
    f="$home/.ssh/authorized_keys"
    [ -f "$f" ] || continue
    c="$(grep -cE '^[[:space:]]*(([a-z-]+=[^[:space:]]+,?)+[[:space:]]+)?(ssh-(rsa|ed25519|dss)|ecdsa-sha2-[a-z0-9]+|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com)[[:space:]]+[A-Za-z0-9+/=]{20,}' "$f" || true)"
    if [ "${c:-0}" -gt 0 ]; then
      say "[chiavi]  $user: $c in $f" >&2
      n=$((n + c))
    fi
  done </etc/passwd
  echo "$n"
}

mod_ssh_hosting() {
  local d="$FROM/hosting" keys
  echo "modulo ssh-hosting ($FAM)"
  command -v sshd >/dev/null 2>&1 || [ -x /usr/sbin/sshd ] || die "sshd non trovato: niente da imporre"
  keys="$(count_keys | tail -n 1)"
  if [ "${keys:-0}" -eq 0 ]; then
    if [ "$ALLOW_NO_KEYS" = 1 ]; then
      say "[avviso]  nessuna chiave in nessun authorized_keys, ma --allow-no-keys: si procede (accesso solo da console)"
    else
      die "nessun utente ha una chiave in authorized_keys: dopo l'applicazione non si entrerebbe piu' via SSH. Aggiungi una chiave o usa --allow-no-keys (resta la console)."
    fi
  fi
  ensure_file "$d/pmgr-ssh-enforce.sh" /usr/local/sbin/pmgr-ssh-enforce 750
  ensure_file "$d/pmgr-ssh-enforce.service" /etc/systemd/system/pmgr-ssh-enforce.service 644
  ensure_file "$d/pmgr-ssh-enforce.timer" /etc/systemd/system/pmgr-ssh-enforce.timer 644
  if [ "$CHECK" = 1 ]; then
    # stato attuale: la password SSH e' abilitata?
    if [ -x /usr/sbin/sshd ] && [ ! -e /etc/ssh/sshd_config.d/00-pmgr-hosting.conf ]; then
      CHANGES=$((CHANGES + 1))
      say "[da fare] imporre SSH a sola chiave (scrivere 00-pmgr-hosting.conf, neutralizzare le righe che abilitano la password)"
    fi
    return 0
  fi
  # copie di sicurezza di tutta la configurazione di sshd, per tornare indietro se l'imposizione fallisce
  mkdir -p -m 700 -- "$BACKUP/sshd"
  cp -a -- /etc/ssh/sshd_config "$BACKUP/sshd/sshd_config"
  UNDO+=("cp -a -- '$BACKUP/sshd/sshd_config' /etc/ssh/sshd_config")
  if [ -d /etc/ssh/sshd_config.d ]; then
    local f
    for f in /etc/ssh/sshd_config.d/*; do
      [ -f "$f" ] || continue
      cp -a -- "$f" "$BACKUP/sshd/$(basename "$f")"
      UNDO+=("cp -a -- '$BACKUP/sshd/$(basename "$f")' '$f'")
    done
    UNDO+=("[ -e '$BACKUP/sshd/00-pmgr-hosting.conf' ] || rm -f /etc/ssh/sshd_config.d/00-pmgr-hosting.conf")
  else
    UNDO+=("rm -f /etc/ssh/sshd_config.d/00-pmgr-hosting.conf")
  fi
  command -v restorecon >/dev/null 2>&1 && restorecon -R /usr/local/sbin/pmgr-ssh-enforce /etc/systemd/system/pmgr-ssh-enforce.service /etc/systemd/system/pmgr-ssh-enforce.timer >/dev/null 2>&1 || true
  sysd daemon-reload
  if ! /usr/local/sbin/pmgr-ssh-enforce >"$TMP/enforce.out" 2>&1; then
    tail -3 "$TMP/enforce.out" >&2
    rollback
    die "pmgr-ssh-enforce non e' riuscito: configurazione di sshd e file ripristinati."
  fi
  say "[fatto]   SSH a sola chiave imposto ($(grep -c . "$TMP/enforce.out") messaggi di pmgr-ssh-enforce)"
  sysd enable pmgr-ssh-enforce.service pmgr-ssh-enforce.timer
  sysd restart pmgr-ssh-enforce.timer
}

# ---------------------------------------------------------------- modulo ssh-standard
# La riga Include di sshd_config.d deve essere la prima attiva: in sshd vince il primo valore, e un Include dopo
# un blocco Match varrebbe solo per quell'utente. Rocky 8 di serie non ce l'ha.
ensure_include_first() {
  local first
  first="$(grep -vE '^[[:space:]]*(#|$)' /etc/ssh/sshd_config | head -n 1)"
  if printf '%s\n' "$first" | grep -qiE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/'; then
    say "[ok]      Include di sshd_config.d in testa a sshd_config"
    return 0
  fi
  CHANGES=$((CHANGES + 1))
  if [ "$CHECK" = 1 ]; then
    say "[da fare] portare la riga Include di sshd_config.d in testa a sshd_config"
    return 0
  fi
  mkdir -p -m 700 -- "$BACKUP"
  cp -a -- /etc/ssh/sshd_config "$BACKUP/sshd_config"
  UNDO+=("cp -a -- '$BACKUP/sshd_config' /etc/ssh/sshd_config")
  { echo 'Include /etc/ssh/sshd_config.d/*.conf'; grep -viE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/' /etc/ssh/sshd_config; } >"$TMP/main.new"
  cat "$TMP/main.new" >/etc/ssh/sshd_config
  say "[fatto]   riga Include portata in testa a sshd_config"
}

# Perche' l'utente amministrativo (quello con cui il rollout entra, SUDO_USER) potrebbe NON riuscire a entrare con la chiave.
# Una riga per problema, niente se tutto torna. $1 = sshd -T in minuscolo.
admin_access_problems() {
  local eff="$1" u="${SUDO_USER:-}" h f pat n
  [ -n "$u" ] && [ "$u" != root ] || return 0
  if ! id -u "$u" >/dev/null 2>&1; then echo "l'utente amministrativo $u non esiste"; return 0; fi
  h="$(getent passwd "$u" | cut -d: -f6)"
  # una password bloccata ("!") NON impedisce il login con la chiave quando UsePAM e' attivo (e' lo stato normale di tecnico nelle immagini
  # cloud); lo impedisce solo con UsePAM no. Un account scaduto lo impedisce sempre.
  if [ "$(passwd -S "$u" 2>/dev/null | cut -d' ' -f2)" = L ] && grep -qx 'usepam no' <<<"$eff"; then echo "l'account $u ha la password bloccata e sshd ha UsePAM no: sshd rifiuta anche la chiave"; fi
  if command -v chage >/dev/null 2>&1; then
    n="$(chage -l "$u" 2>/dev/null | sed -n 's/^Account expires[^:]*: *//p')"
    case "$n" in ""|never) ;; *) if [ "$(date -d "$n" +%s 2>/dev/null || echo 9999999999)" -lt "$(date +%s)" ]; then echo "l'account $u e' scaduto ($n)"; fi ;; esac
  fi
  f="$h/.ssh/authorized_keys"
  if [ -L "$h/.ssh" ] || [ -L "$f" ]; then echo "$f: .ssh o authorized_keys sono link simbolici"; fi
  n=0
  [ -f "$f" ] && n="$(grep -cE '^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp(256|384|521)|sk-[a-z0-9@.-]+) [A-Za-z0-9+/]+=*' "$f" 2>/dev/null || true)"
  [ "${n:-0}" -gt 0 ] || echo "$u non ha nessuna chiave pubblica valida in $f (pmgr-push-key-guest.sh dal nodo Proxmox la rimette)"
  if grep -q '^authorizedkeysfile ' <<<"$eff" && ! grep -qE '^authorizedkeysfile (\.ssh/authorized_keys|%h/\.ssh/authorized_keys)( |$)' <<<"$eff"; then
    echo "sshd legge le chiavi da un file non standard ($(grep '^authorizedkeysfile ' <<<"$eff" | cut -c1-80)): controlla che $u vi compaia"
  fi
  if grep -q '^allowusers ' <<<"$eff"; then
    local ok=0
    for pat in $(sed 's/^allowusers //' <<<"$(grep '^allowusers ' <<<"$eff")"); do case "$pat" in *@*) ok=1 ;; *) [[ "$u" == $pat ]] && ok=1 ;; esac; done
    [ "$ok" = 1 ] || echo "AllowUsers di sshd non include $u"
  fi
  if grep -q '^denyusers ' <<<"$eff"; then
    for pat in $(sed 's/^denyusers //' <<<"$(grep '^denyusers ' <<<"$eff")"); do case "$pat" in *@*) ;; *) [[ "$u" == $pat ]] && echo "DenyUsers di sshd include $u" ;; esac; done
  fi
  return 0
}

mod_ssh_standard() {
  local eff eff2 pw need="" line
  echo "modulo ssh-standard ($FAM)"
  if [ -e /etc/ssh/sshd_config.d/00-pmgr-hosting.conf ] || [ -e /usr/local/sbin/pmgr-ssh-enforce ]; then
    die "questa e' una VM di hosting (SSH a sola chiave imposto): il modulo ssh-standard non si applica."
  fi
  command -v sshd >/dev/null 2>&1 || [ -x /usr/sbin/sshd ] || die "sshd non trovato"
  mkdir -p /etc/ssh/sshd_config.d
  ensure_include_first
  eff="$(sshd_effective)"
  [ -n "$eff" ] || die "sshd -T non risponde: configurazione non valida? Non tocco niente."
  pw="$(grep '^passwordauthentication ' <<<"$eff" || true)"
  # Si aggiunge SOLO cio' che manca all'accesso amministrativo e alla sicurezza di base. La password NON si tocca mai:
  # se il gruppo l'ha disattivata resta disattivata (regola del 2026-11-10: gli admin entrano con la chiave di tecnico).
  grep -qx 'permitrootlogin no' <<<"$eff" || need="${need}PermitRootLogin no\n"
  grep -qx 'pubkeyauthentication yes' <<<"$eff" || need="${need}PubkeyAuthentication yes\n"
  if [ -n "$need" ]; then
    { printf '# Gestito da ProxmoxManager (rollout): root non entra e le chiavi pubbliche sono accettate (accesso amministrativo).\n'
      printf '# Non imposta PasswordAuthentication: la scelta sulla password resta del proprietario della VM.\n'
      printf '# In sshd vince il primo valore trovato: questo file e'"'"' letto prima degli altri di sshd_config.d.\n'
      printf '%b' "$need"; } >"$TMP/ssh-admin.conf"
    ensure_file "$TMP/ssh-admin.conf" /etc/ssh/sshd_config.d/00-pmgr-admin.conf 644
  else
    say "[ok]      sshd: root non entra e le chiavi pubbliche sono accettate (${pw:-passwordauthentication ?}: non imposta dal rollout)"
  fi
  if [ "$CHECK" != 1 ] && [ -n "$need" ]; then
    eff2="$(sshd_effective)"
    if [ -z "$eff2" ] || ! grep -qx 'permitrootlogin no' <<<"$eff2" || ! grep -qx 'pubkeyauthentication yes' <<<"$eff2"; then
      rollback
      die "la configurazione effettiva di sshd non e' quella attesa (root no, chiavi si'): file ripristinati. Un altro file la annulla?"
    fi
    if [ "$(grep '^passwordauthentication ' <<<"$eff2" || true)" != "$pw" ]; then
      rollback
      die "il modulo avrebbe cambiato PasswordAuthentication: file ripristinati (non deve succedere)."
    fi
    say "[ok]      configurazione effettiva: root no, chiavi si' (${pw:-password ?} invariata)"
    if [ "$HAVE_SYSTEMD" = 1 ]; then
      systemctl reload ssh.service >/dev/null 2>&1 || systemctl reload sshd.service >/dev/null 2>&1 || true
    fi
  fi
  # l'utente amministrativo deve poter entrare: se qualcosa lo impedisce lo si dice (non si indovina come correggerlo)
  while IFS= read -r line; do [ -z "$line" ] || say "[avviso]  accesso amministrativo: $line"; done < <(admin_access_problems "$eff")
}

# ---------------------------------------------------------------- modulo firewall-hosting
# Porte su cui ascolta sshd (da "sshd -T"), una per riga; 22 se non si riesce a leggerle. Una VM con SSH su un'altra porta
# (es. 24 o 2022, con la 22 occupata da un contenitore) deve avere QUELLA aperta nel firewall e protetta da fail2ban.
ssh_ports() {
  local p
  p="$(sshd -T 2>/dev/null | awk '$1 == "port" && $2 ~ /^[0-9]+$/ { print $2 }' | sort -un)"
  [ -n "$p" ] || p=22
  printf '%s\n' "$p"
}
# fw_problems: stampa una riga per ogni scostamento dalla configurazione attesa (22, 80, 443 aperte, il resto chiuso).
# Controlla la configurazione scritta su disco (funziona anche senza diritti di rete) e, se possibile, lo stato attivo.
fw_problems() {
  local p svc zone f
  if [ "$FAM" = rocky ]; then
    command -v firewall-cmd >/dev/null 2>&1 || { echo "firewalld non installato"; return 0; }
    [ "$HAVE_SYSTEMD" = 1 ] && ! systemctl is-enabled --quiet firewalld 2>/dev/null && echo "firewalld non abilitato all'avvio"
    f=/etc/firewalld/zones/public.xml
    [ -f "$f" ] || f=/usr/lib/firewalld/zones/public.xml
    svc="$(grep -o 'service name="[^"]*"' "$f" 2>/dev/null | cut -d'"' -f2 | tr '\n' ' ')"
    for p in http https; do grep -qw "$p" <<<"$svc" || echo "zona public: manca il servizio $p"; done
    for p in $(ssh_ports); do
      if [ "$p" = 22 ]; then grep -qw ssh <<<"$svc" || echo "zona public: manca il servizio ssh"
      else grep -qE "port=\"$p\"" "$f" 2>/dev/null || echo "zona public: manca la porta $p/tcp (SSH)"; fi
    done
    for p in cockpit dhcpv6-client; do grep -qw "$p" <<<"$svc" && echo "zona public: il servizio $p e' aperto (non serve)"; done
    zone="$(sed -n 's/^DefaultZone=//p' /etc/firewalld/firewalld.conf 2>/dev/null | head -n 1)"
    [ -z "$zone" ] || [ "$zone" = public ] || echo "zona predefinita: $zone (attesa public)"
    if firewall-cmd --state >/dev/null 2>&1; then
      svc="$(firewall-cmd --zone=public --list-services 2>/dev/null)"
      for p in http https; do grep -qw "$p" <<<"$svc" || echo "attivo: manca il servizio $p"; done
      for p in $(ssh_ports); do
        if [ "$p" = 22 ]; then grep -qw ssh <<<"$svc" || echo "attivo: manca il servizio ssh"
        else firewall-cmd --zone=public --list-ports 2>/dev/null | grep -qw "$p/tcp" || echo "attivo: manca la porta $p/tcp (SSH)"; fi
      done
      for p in cockpit dhcpv6-client; do grep -qw "$p" <<<"$svc" && echo "attivo: il servizio $p e' aperto"; done
    else
      [ "$HAVE_SYSTEMD" = 1 ] && echo "firewalld non in esecuzione"
    fi
  else
    command -v ufw >/dev/null 2>&1 || { echo "ufw non installato"; return 0; }
    grep -qx 'ENABLED=yes' /etc/ufw/ufw.conf 2>/dev/null || echo "ufw non abilitato all'avvio"
    grep -q '^DEFAULT_INPUT_POLICY="DROP"' /etc/default/ufw 2>/dev/null || echo "politica in ingresso di ufw non e' DROP"
    for p in $(ssh_ports) 80 443; do grep -qE "^### tuple ### allow tcp $p " /etc/ufw/user.rules 2>/dev/null || echo "manca la regola per $p/tcp"; done
    # solo se ufw non e' ancora attivo: su una VM dove ufw gira gia' (e i contenitori funzionano) non si allarga niente
    if [ -n "$(docker_ifaces)" ] && ! { ufw status 2>/dev/null | grep -q '^Status: active'; }; then
      grep -qE '^### tuple ### allow any any 0\.0\.0\.0/0 any 0\.0\.0\.0/0 in_docker0' /etc/ufw/user.rules 2>/dev/null || echo "manca la regola per l'interfaccia docker0 (i contenitori non raggiungerebbero l'host)"
    fi
    if ufw status >"$TMP/ufw.st" 2>&1; then
      grep -q '^Status: active' "$TMP/ufw.st" || echo "ufw non attivo"
    fi
  fi
  # esplicito: l'ultima istruzione puo' essere un "grep && echo" che da' 1 quando va tutto bene, e con set -e
  # l'assegnazione problems="$(fw_problems)" farebbe uscire lo script in silenzio
  return 0
}

# fw_listening: porte in ascolto raggiungibili dall'esterno (non solo loopback) che non sono 22, 80, 443 e non sono tra --open
fw_listening() {
  command -v ss >/dev/null 2>&1 || return 0
  ss -H -tuln 2>/dev/null | awk '{print $1, $5}' | while read -r proto addr; do
    case "$addr" in 127.* | "[::1]"* | "::1"* | "localhost"*) continue ;; esac
    local port="${addr##*:}" pr="${proto%6}" ok=0 o
    [[ "$port" =~ ^[0-9]+$ ]] || continue
    case " 80 443 $(ssh_ports | tr '\n' ' ')" in *" $port "*) [ "$pr" = tcp ] && continue ;; esac
    for o in "${OPEN_PORTS[@]+"${OPEN_PORTS[@]}"}"; do [ "$o" = "$port/$pr" ] && ok=1; done
    [ "$ok" = 1 ] || echo "$port/$pr"
  done | sort -u
}

# docker_ifaces: interfacce di Docker presenti (docker0, docker_gwbridge e i bridge br-*): con ufw attivo, senza una regola
# per loro i contenitori non raggiungono piu' i servizi dell'host (es. un DNS o un proxy su 172.17.0.1), quindi si aprono.
docker_ifaces() {
  command -v docker >/dev/null 2>&1 || ip link show docker0 >/dev/null 2>&1 || return 0
  echo docker0; echo docker_gwbridge; echo 'br-+'
}

mod_firewall_hosting() {
  local d="$FROM/hosting" problems extra o
  echo "modulo firewall-hosting ($FAM)"
  [ -f "$d/firewall-ufw.sh" ] && [ -f "$d/firewall-firewalld.sh" ] || die "mancano i file di firewall in $d"
  problems="$(fw_problems)" || true
  if [ -z "$problems" ]; then
    say "[ok]      firewall conforme: $(ssh_ports | paste -sd, - | sed 's/,/, /g'), 80 e 443 aperte, il resto chiuso"
    return 0
  fi
  CHANGES=$((CHANGES + 1))
  while IFS= read -r o; do say "[da fare] $o"; done <<<"$problems"
  extra="$(fw_listening | tr '\n' ' ')"
  if [ -n "${extra// /}" ]; then
    say "[avviso]  porte in ascolto che il firewall chiuderebbe: $extra"
    # chiudere il firewall su una VM dove era spento puo' tagliare un servizio dei clienti
    if [ "$ALLOW_CLOSE" = 0 ] && ! { ufw status 2>/dev/null | grep -q '^Status: active'; } && ! firewall-cmd --state >/dev/null 2>&1; then
      if [ "$CHECK" = 1 ]; then
        say "[avviso]  senza --open o --allow-close-ports l'applicazione si rifiuterebbe"
      else
        die "il firewall e' spento e queste porte sono in ascolto: $extra. Si chiuderebbero. Indica quelle da tenere aperte con --open (es. --open 3306/tcp) oppure accetta con --allow-close-ports."
      fi
    fi
  fi
  [ "$CHECK" = 1 ] && return 0
  # installazione se manca, copie di sicurezza, applicazione degli stessi script dello snippet
  mkdir -p -m 700 -- "$BACKUP/fw"
  if [ "$FAM" = rocky ]; then
    ensure_packages firewalld
    cp -a /etc/firewalld "$BACKUP/fw/firewalld" 2>/dev/null || true
    UNDO+=("rm -rf /etc/firewalld && cp -a '$BACKUP/fw/firewalld' /etc/firewalld && { firewall-cmd --reload >/dev/null 2>&1 || true; }")
    # shellcheck disable=SC2046
    sh "$d/firewall-firewalld.sh" $(ssh_ports) >"$TMP/fw.out" 2>&1 || true
    for o in "${OPEN_PORTS[@]+"${OPEN_PORTS[@]}"}"; do
      if firewall-cmd --state >/dev/null 2>&1; then firewall-cmd --permanent --zone=public --add-port="$o" >/dev/null 2>&1; else firewall-offline-cmd --zone=public --add-port="$o" >/dev/null 2>&1; fi
    done
    if [ "$HAVE_SYSTEMD" = 1 ]; then
      systemctl enable firewalld >/dev/null 2>&1 || true
      if firewall-cmd --state >/dev/null 2>&1; then
        firewall-cmd --reload >/dev/null 2>&1
      else
        systemctl start firewalld >/dev/null 2>&1 || true
        # firewalld impiega qualche secondo a partire: senza attesa il controllo qui sotto lo darebbe per spento
        for _ in $(seq 1 30); do firewall-cmd --state >/dev/null 2>&1 && break; sleep 1; done
      fi
    fi
  else
    ensure_packages ufw
    UFW_WAS_ACTIVE=0; ufw status 2>/dev/null | grep -q '^Status: active' && UFW_WAS_ACTIVE=1
    cp -a /etc/ufw "$BACKUP/fw/ufw" 2>/dev/null || true
    cp -a /etc/default/ufw "$BACKUP/fw/default-ufw" 2>/dev/null || true
    UNDO+=("rm -rf /etc/ufw && cp -a '$BACKUP/fw/ufw' /etc/ufw && cp -a '$BACKUP/fw/default-ufw' /etc/default/ufw && { ufw reload >/dev/null 2>&1 || true; }")
    # ufw "abilitato all'avvio" ma non caricato (caso reale: VM 118, caricamento fallito al boot): in questo stato ogni
    # comando ufw fallisce con "problem running" perche' prova a svuotare catene che non esistono. Si riporta prima a spento.
    if grep -qx 'ENABLED=yes' /etc/ufw/ufw.conf 2>/dev/null && ufw status 2>/dev/null | grep -q '^Status: inactive'; then
      ufw disable >/dev/null 2>&1 || true
    fi
    for o in "${OPEN_PORTS[@]+"${OPEN_PORTS[@]}"}"; do ufw allow "$o" >/dev/null 2>&1; done
    # shellcheck disable=SC2046
    sh "$d/firewall-ufw.sh" $(ssh_ports) >"$TMP/fw.out" 2>&1 || true
    [ "$UFW_WAS_ACTIVE" = 1 ] || for o in $(docker_ifaces); do ufw allow in on "$o" >>"$TMP/fw.out" 2>&1 || true; done
  fi
  problems="$(fw_problems)" || true
  if [ -n "$problems" ]; then
    echo "$problems" | sed 's/^/    /' >&2
    tail -3 "$TMP/fw.out" >&2
    rollback
    die "il firewall non risulta nello stato atteso dopo l'applicazione: configurazione ripristinata."
  fi
  say "[fatto]   firewall: $(ssh_ports | paste -sd, - | sed 's/,/, /g'), 80 e 443 aperte${OPEN_PORTS[*]:+, in piu ${OPEN_PORTS[*]}}, il resto chiuso"
}

# ---------------------------------------------------------------- modulo fail2ban (delega allo script dedicato)
mod_fail2ban() {
  local f="$SELF_DIR/pmgr-fail2ban-update.sh" rc=0
  echo "modulo fail2ban ($FAM)"
  [ -f "$f" ] || die "manca $f"
  if [ "$CHECK" = 1 ]; then
    bash "$f" --check || rc=$?
    case "$rc" in
      0) ;;
      3) CHANGES=$((CHANGES + 1)) ;;
      *) die "pmgr-fail2ban-update.sh --check: uscita $rc" ;;
    esac
  else
    bash "$f" || die "pmgr-fail2ban-update.sh non e' riuscito (ha ripristinato la configurazione precedente)"
  fi
}

# ---------------------------------------------------------------- modulo agent-exec
# Le VM Rocky create con il vecchio snippet hanno guest-exec nel filtro dell'agent (BLACKLIST_RPC, Rocky 8) o fuori dai consentiti
# (FILTER_RPC_ARGS, Rocky 9/10) e helper vecchi o assenti: il manager non puo' aggiungere utenti o dischi. Stessa correzione dello
# snippet: il filtro si modifica con lo stesso sed, gli helper sono quelli del repository del manager (payload: ../helpers).
HELPERS_LIST="adduser.sh disk.sh dmesg.sh"
agent_filter_sed() { # stampa lo script sed (una espressione per riga) che sblocca guest-exec e guest-exec-status
  cat <<'SED_EOF'
/^BLACKLIST_RPC=/{s/,guest-exec-status//g;s/,guest-exec//g;s/=guest-exec-status,/=/;s/=guest-exec,/=/;s/=guest-exec-status$/=/;s/=guest-exec$/=/}
/^FILTER_RPC_ARGS="--allow-rpcs=/{/guest-exec([,"])/!s/"$/,guest-exec"/;/guest-exec-status([,"])/!s/"$/,guest-exec-status"/}
/^FILTER_RPC_ARGS="--block-rpcs=/{s/,guest-exec-status//g;s/,guest-exec//g;s/=guest-exec-status,/=/;s/=guest-exec,/=/;s/=guest-exec-status"$/="/;s/=guest-exec"$/="/}
SED_EOF
}
# vero se l'agent in esecuzione ha guest-exec consentito (nessun filtro, o fuori dai vietati, o tra i consentiti)
agent_exec_allowed() {
  local pid cl
  pid="$(systemctl show -p MainPID --value qemu-guest-agent 2>/dev/null || true)"
  [ -n "$pid" ] && [ "$pid" != 0 ] || return 1
  cl="$(tr '\0' ' ' </proc/"$pid"/cmdline 2>/dev/null || true)"
  [ -n "$cl" ] || return 1
  if grep -q -- '--allow-rpcs=' <<<"$cl"; then grep -qE -- '--allow-rpcs=[^ ]*guest-exec(,| |$)' <<<"$cl"; return; fi
  if grep -qE -- '--(blacklist|block-rpcs)=' <<<"$cl"; then ! grep -qE -- '--(blacklist|block-rpcs)=[^ ]*guest-exec(,| |$)' <<<"$cl"; return; fi
  return 0
}

mod_agent_exec() {
  local hd="$FROM/../helpers" cfg=/etc/sysconfig/qemu-ga h c0="$CHANGES" mode selinux=0 cfg_changed=0 labeled=0
  echo "modulo agent-exec ($FAM)"
  if [ "$FAM" != rocky ]; then say "[ok]      Debian/Ubuntu: l'agent non ha filtri da togliere, niente da fare"; return 0; fi
  for h in $HELPERS_LIST; do [ -f "$hd/$h" ] || die "manca l'helper $h nel payload ($hd): rilancia con la versione attuale di pmgr-vm-rollout.sh"; done
  [ -f "$cfg" ] || die "$cfg non trovato: l'agent QEMU non e' installato?"
  if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce)" != Disabled ]; then selinux=1; ensure_packages policycoreutils-python-utils; fi
  # 1. helper (specifici e validati: il manager li lancia con argomenti posizionali)
  if [ ! -d /usr/local/libexec/pmgr ]; then
    CHANGES=$((CHANGES + 1))
    if [ "$CHECK" = 1 ]; then say "[da fare] creare /usr/local/libexec/pmgr"
    else install -d -m 750 -o root -g root /usr/local/libexec/pmgr; UNDO+=("rmdir /usr/local/libexec/pmgr 2>/dev/null || true"); say "[fatto]   /usr/local/libexec/pmgr"; fi
  fi
  if [ -d /usr/local/libexec/pmgr ] && [ "$(stat -c '%a %U' /usr/local/libexec/pmgr)" != "750 root" ]; then
    CHANGES=$((CHANGES + 1))
    if [ "$CHECK" = 1 ]; then say "[da fare] /usr/local/libexec/pmgr deve essere 750 di root (oggi $(stat -c '%a %U' /usr/local/libexec/pmgr))"
    else
      UNDO+=("chmod $(stat -c %a /usr/local/libexec/pmgr) /usr/local/libexec/pmgr; chown $(stat -c %U:%G /usr/local/libexec/pmgr) /usr/local/libexec/pmgr")
      chown root:root /usr/local/libexec/pmgr; chmod 750 /usr/local/libexec/pmgr; say "[fatto]   /usr/local/libexec/pmgr 750 root"
    fi
  fi
  for h in $HELPERS_LIST; do ensure_file "$hd/$h" "/usr/local/libexec/pmgr/$h" 750; done
  # 2. filtro dell'agent
  agent_filter_sed >"$TMP/qemu-ga.sed"
  sed -E -f "$TMP/qemu-ga.sed" "$cfg" >"$TMP/qemu-ga.new"
  mode="$(stat -c %a -- "$cfg")"
  same_file "$TMP/qemu-ga.new" "$cfg" || cfg_changed=1
  ensure_file "$TMP/qemu-ga.new" "$cfg" "$mode"
  # 3. SELinux: boolean ed etichetta degli helper
  if [ "$selinux" = 1 ]; then
    if [ "$(getsebool virt_qemu_ga_run_unconfined 2>/dev/null | awk '{print $NF}')" = on ]; then
      say "[ok]      boolean virt_qemu_ga_run_unconfined attivo"
    else
      CHANGES=$((CHANGES + 1))
      if [ "$CHECK" = 1 ]; then say "[da fare] attivare il boolean virt_qemu_ga_run_unconfined"
      elif setsebool -P virt_qemu_ga_run_unconfined on; then say "[fatto]   boolean virt_qemu_ga_run_unconfined attivo"
      else rollback; die "setsebool non riuscito: file ripristinati."; fi
    fi
    if [ -d /usr/local/libexec/pmgr ] && ls -Z /usr/local/libexec/pmgr/*.sh >/dev/null 2>&1 \
       && [ "$(ls -Z /usr/local/libexec/pmgr/*.sh 2>/dev/null | grep -vc virt_qemu_ga_unconfined_exec_t)" = 0 ]; then labeled=1; fi
    if [ "$labeled" = 1 ] && [ "$CHANGES" = "$c0" ]; then say "[ok]      helper con l'etichetta virt_qemu_ga_unconfined_exec_t"
    elif [ "$CHECK" = 1 ]; then
      [ "$labeled" = 1 ] || { CHANGES=$((CHANGES + 1)); say "[da fare] etichettare gli helper virt_qemu_ga_unconfined_exec_t"; }
    else
      { semanage fcontext -a -t virt_qemu_ga_unconfined_exec_t '/usr/local/libexec/pmgr(/.*)?' >/dev/null 2>&1 \
          || semanage fcontext -m -t virt_qemu_ga_unconfined_exec_t '/usr/local/libexec/pmgr(/.*)?'; } \
        && restorecon -R /usr/local/libexec/pmgr \
        && say "[fatto]   helper etichettati virt_qemu_ga_unconfined_exec_t" \
        || { rollback; die "etichettatura SELinux non riuscita: file ripristinati."; }
    fi
  fi
  [ "$CHECK" = 1 ] && return 0
  # 4. riavvio del solo agent, e controllo che guest-exec risulti davvero consentito
  if [ "$cfg_changed" = 1 ]; then
    if [ "$HAVE_SYSTEMD" = 1 ]; then
      systemctl restart qemu-guest-agent >/dev/null 2>&1 || { rollback; systemctl restart qemu-guest-agent >/dev/null 2>&1 || true; die "il riavvio dell'agent non e' riuscito: file ripristinati."; }
      local ok=0 _
      for _ in $(seq 1 20); do systemctl is-active --quiet qemu-guest-agent && agent_exec_allowed && { ok=1; break; }; sleep 1; done
      if [ "$ok" = 0 ]; then
        rollback
        systemctl restart qemu-guest-agent >/dev/null 2>&1 || true
        die "dopo il riavvio guest-exec non risulta consentito (o l'agent non e' ripartito): file ripristinati e agent riavviato."
      fi
      say "[fatto]   agent riavviato: guest-exec consentito"
    else
      say "[avviso]  systemd non attivo qui: agent non riavviato"
    fi
  elif [ "$HAVE_SYSTEMD" = 1 ] && ! agent_exec_allowed; then
    say "[avviso]  il file di configurazione e' a posto ma l'agent in esecuzione ha ancora il filtro: riavvialo (systemctl restart qemu-guest-agent)"
  fi
  [ "$CHANGES" -gt "$c0" ] || say "[ok]      niente da applicare"
}

# ---------------------------------------------------------------- modulo hardening-hosting
# Valori di sysctl del file che non coincidono con quelli in uso (solo per le voci che esistono su questo kernel).
sysctl_mismatches() { # FILE
  local line key val path cur
  while IFS= read -r line; do
    line="${line%%#*}"
    [[ "$line" == *=* ]] || continue
    key="$(printf '%s' "${line%%=*}" | tr -d '[:space:]')"
    val="$(printf '%s' "${line#*=}" | tr -d '[:space:]')"
    path="/proc/sys/${key//.//}"
    [ -r "$path" ] || continue
    cur="$(tr -d '[:space:]' <"$path" 2>/dev/null || true)"
    [ "$cur" = "$val" ] || printf '%s (voluto %s, in uso %s)\n' "$key" "$val" "$cur"
  done <"$1"
}

mod_hardening_hosting() {
  local d="$FROM/hosting" c0="$CHANGES" mism
  echo "modulo hardening-hosting ($FAM)"
  [ -f "$d/90-pmgr-hosting-sysctl.conf" ] && [ -f "$d/60-pmgr-recidive.local" ] || die "mancano i file del profilo hosting in $d (90-pmgr-hosting-sysctl.conf, 60-pmgr-recidive.local)"
  command -v fail2ban-client >/dev/null 2>&1 || die "fail2ban non e' installato: applica prima il modulo fail2ban (baseline)"
  ensure_file "$d/90-pmgr-hosting-sysctl.conf" /etc/sysctl.d/90-pmgr-hosting.conf 644
  local sp
  sp="$(ssh_ports | paste -sd, -)"
  if [ "$sp" = 22 ]; then
    ensure_file "$d/60-pmgr-recidive.local" /etc/fail2ban/jail.d/60-pmgr-recidive.local 644
  else
    # sshd non e' sulla 22: la jail recidive deve bannare quella porta, non "ssh" (22)
    sed "s/^port = ssh\$/port = $sp/" "$d/60-pmgr-recidive.local" >"$TMP/recidive-port.local"
    ensure_file "$TMP/recidive-port.local" /etc/fail2ban/jail.d/60-pmgr-recidive.local 644
  fi
  # azione di ban di recidive = quella per porta della jail sshd (la jail.conf usa "allports": bloccherebbe 80 e 443)
  local act=""
  act="$(fail2ban-client get sshd actions 2>/dev/null | tail -n 1 || true)"
  case "$act" in
    iptables-multiport | nftables | firewallcmd-rich-rules)
      # firewalld riconosce le regole dal testo: con quella di sshd identica, lo sblocco di sshd toglierebbe anche
      # la regola di recidive. Con "drop" invece di "reject" le due regole sono diverse.
      [ "$act" != firewallcmd-rich-rules ] || act='firewallcmd-rich-rules[rich-blocktype=drop]'
      printf '[recidive]\nbanaction = %s\n' "$act" >"$TMP/recidive-action.local"
      ensure_file "$TMP/recidive-action.local" /etc/fail2ban/jail.d/61-pmgr-recidive-action.local 644
      ;;
    *) say "[avviso]  azione di ban di sshd non leggibile ('${act:-nessuna}': fail2ban in esecuzione?): recidive resta con l'azione di fail2ban, che blocca TUTTE le porte" ;;
  esac
  [ "$CHECK" = 1 ] && return 0
  [ "$CHANGES" -gt "$c0" ] || { say "[ok]      niente da applicare"; return 0; }
  # fail2ban: la configurazione nuova deve caricarsi, altrimenti si torna indietro (meglio senza recidive che senza fail2ban)
  [ -e /var/log/fail2ban.log ] || install -m 640 /dev/null /var/log/fail2ban.log
  if ! fail2ban-client -d >"$TMP/f2b.out" 2>&1; then
    tail -3 "$TMP/f2b.out" >&2
    rollback
    die "fail2ban non riesce a leggere la jail recidive: file ripristinati."
  fi
  if [ "$HAVE_SYSTEMD" = 1 ]; then
    systemctl restart fail2ban >/dev/null 2>&1 || { rollback; systemctl restart fail2ban >/dev/null 2>&1 || true; die "il riavvio di fail2ban non e' riuscito: file ripristinati."; }
    local ok=0 _
    for _ in $(seq 1 15); do
      if fail2ban-client status recidive >/dev/null 2>&1; then ok=1; break; fi
      sleep 1
    done
    if [ "$ok" = 0 ]; then
      rollback
      systemctl restart fail2ban >/dev/null 2>&1 || true
      die "dopo il riavvio la jail recidive non risponde: file ripristinati (vedi: journalctl -u fail2ban)."
    fi
    say "[fatto]   fail2ban riavviato, jail recidive attiva"
  else
    say "[avviso]  systemd non attivo qui: fail2ban non riavviato"
  fi
  # sysctl per ultimo: un valore applicato non torna indietro con i file (unprivileged_bpf_disabled=1 non si puo'
  # nemmeno riabbassare senza riavvio), quindi solo quando fail2ban non puo' piu' far scattare il ripristino.
  # Le voci che il kernel non conosce o non lascia scrivere (container) non sono un errore
  sysctl -q -p /etc/sysctl.d/90-pmgr-hosting.conf >"$TMP/sysctl.out" 2>&1 || true
  mism="$(sysctl_mismatches /etc/sysctl.d/90-pmgr-hosting.conf)"
  if [ -z "$mism" ]; then say "[fatto]   sysctl applicato"; else say "[avviso]  sysctl: alcuni valori non sono quelli voluti (un altro file di sysctl.d li ridefinisce, o il kernel non li lascia scrivere):"; printf '           %s\n' "$mism"; fi
}

# ---------------------------------------------------------------- --verify: sola lettura
sshd_effective() { # stampa i valori effettivi di sshd in minuscolo (chiave host temporanea: puo' non esistere ancora)
  local k hk=""
  mkdir -p /run/sshd 2>/dev/null || true
  k="$(mktemp -d)"
  ssh-keygen -q -t ed25519 -N '' -f "$k/k" >/dev/null 2>&1 && hk="-h $k/k"
  # shellcheck disable=SC2086
  "$(command -v sshd || echo /usr/sbin/sshd)" -T $hk 2>/dev/null | tr 'A-Z' 'a-z'
  rm -rf -- "$k"
}

do_verify() {
  local FAILS=0 rc eff
  v_ok() { printf '  [OK]       %s\n' "$*"; }
  v_ko() { printf '  [FALLITO]  %s\n' "$*"; FAILS=$((FAILS + 1)); }
  v_info() { printf '  [INFO]     %s\n' "$*"; }
  v_warn() { printf '  [AVVISO]   %s\n' "$*"; }   # non fa fallire la verifica: cosa che il rollout non corregge
  echo "pmgr-vm-apply --verify (profilo $PROFILE): $PRETTY_NAME, kernel $(uname -r)"

  # cloud-init
  if command -v cloud-init >/dev/null 2>&1; then
    local st
    # cloud-init status esce con 2 quando e' "degraded done" (avvisi recuperabili, es. DEPRECATED): non e' un errore
    # e con set -e/pipefail fermerebbe il controllo in silenzio
    st="$( { cloud-init status 2>&1 || true; } | head -1)"
    case "$st" in *done*) v_ok "cloud-init: $st" ;; *) v_ko "cloud-init: ${st:-nessuna risposta}" ;; esac
    if cloud-init status --long 2>/dev/null | grep -q 'extended_status:.*degraded'; then v_info "cloud-init: stato degraded (avvisi recuperabili, non errori): cloud-init status --long"; fi
    v_info "instance-id: $(cat /var/lib/cloud/data/instance-id 2>/dev/null || echo sconosciuto)"
  else
    v_info "cloud-init non installato"
  fi
  # guest agent
  if [ "$HAVE_SYSTEMD" = 1 ]; then
    if systemctl is-active --quiet qemu-guest-agent 2>/dev/null; then v_ok "qemu-guest-agent attivo"; else v_ko "qemu-guest-agent non attivo"; fi
  else
    v_info "systemd non attivo qui: agent e servizi non controllati"
  fi
  # moduli (ognuno in un processo a parte: una die non interrompe il resto)
  local m
  for m in fail2ban updates; do
    rc=0
    bash "$SELF" --module "$m" --check --from "$FROM" >"$TMP/v.$m" 2>&1 || rc=$?
    case "$rc" in
      0) v_ok "modulo $m: configurazione conforme" ;;
      3) v_ko "modulo $m: NON conforme ($(grep -c '\[da fare\]' "$TMP/v.$m") modifiche da fare)"; grep '\[da fare\]' "$TMP/v.$m" | head -5 | sed 's/^/                /' ;;
      *) v_ko "modulo $m: errore (uscita $rc): $(tail -1 "$TMP/v.$m")" ;;
    esac
  done
  if docker_present && ! gpu_present && [ "$FAM" != rocky ]; then v_info "Docker rilevato: aggiornamenti automatici con esclusioni (docker, containerd, runc)"; fi
  if gpu_present; then v_info "scheda NVIDIA rilevata: aggiornamenti automatici con esclusioni (kernel, driver NVIDIA/CUDA, docker, slurm)"; fi
  # SSH secondo il profilo
  eff="$(sshd_effective)"
  if [ -z "$eff" ]; then
    v_ko "sshd -T non risponde: configurazione non valida?"
  elif [ "$PROFILE" = hosting ]; then
    rc=0
    bash "$SELF" --module ssh-hosting --check --allow-no-keys --from "$FROM" >"$TMP/v.ssh" 2>&1 || rc=$?
    [ "$rc" = 0 ] && v_ok "modulo ssh-hosting: configurazione conforme" || v_ko "modulo ssh-hosting: NON conforme (uscita $rc)"
    local want
    for want in 'passwordauthentication no' 'pubkeyauthentication yes' 'authenticationmethods publickey' 'permitrootlogin no' 'logingracetime 30' 'x11forwarding no' 'clientaliveinterval 300' 'clientalivecountmax 2'; do
      grep -qx "$want" <<<"$eff" && v_ok "sshd effettivo: $want" || v_ko "sshd effettivo: manca '$want'"
    done
    if [ "$HAVE_SYSTEMD" = 1 ]; then
      systemctl is-enabled --quiet pmgr-ssh-enforce.service 2>/dev/null && v_ok "pmgr-ssh-enforce.service abilitato (a ogni avvio)" || v_ko "pmgr-ssh-enforce.service non abilitato"
      systemctl is-enabled --quiet pmgr-ssh-enforce.timer 2>/dev/null && v_ok "pmgr-ssh-enforce.timer abilitato (ogni 15 minuti)" || v_ko "pmgr-ssh-enforce.timer non abilitato"
    fi
  else
    rc=0
    bash "$SELF" --module ssh-standard --check --from "$FROM" >"$TMP/v.sshs" 2>&1 || rc=$?
    case "$rc" in
      0) v_ok "modulo ssh-standard: configurazione conforme" ;;
      3) v_ko "modulo ssh-standard: NON conforme ($(grep '\[da fare\]' "$TMP/v.sshs" | head -2 | sed 's/^ *//' | tr '\n' ';'))" ;;
      *) v_ko "modulo ssh-standard: $(tail -1 "$TMP/v.sshs")" ;;
    esac
    v_info "sshd effettivo: $(grep '^passwordauthentication ' <<<"$eff" || echo 'passwordauthentication ?') (scelta del proprietario della VM: il rollout non la cambia)"
    grep -qx 'permitrootlogin no' <<<"$eff" && v_ok "sshd effettivo: root non entra" || v_ko "sshd effettivo: PermitRootLogin non e' no ($(grep '^permitrootlogin' <<<"$eff"))"
    grep -qx 'pubkeyauthentication yes' <<<"$eff" && v_ok "sshd effettivo: chiavi pubbliche accettate" || v_ko "sshd effettivo: PubkeyAuthentication non e' yes (gli amministratori non possono entrare)"
    if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER:-}" != root ]; then
      local adm
      adm="$(admin_access_problems "$eff")"
      if [ -z "$adm" ]; then v_ok "accesso amministrativo: l'utente ${SUDO_USER} ha chiavi e sshd lo accetta"; else while IFS= read -r want; do v_ko "accesso amministrativo: $want"; done <<<"$adm"; fi
    else
      v_info "accesso amministrativo non controllato (utente non noto: lancia con sudo da tecnico)"
    fi
    if [ -e /etc/ssh/sshd_config.d/00-pmgr-hosting.conf ] || [ -e /usr/local/sbin/pmgr-ssh-enforce ]; then v_ko "su una VM standard c'e' la configurazione di hosting (00-pmgr-hosting.conf / pmgr-ssh-enforce)"; fi
  fi
  # firewall e controllo degli accessi obbligatorio (solo hosting): indipendenti da sshd
  if [ "$PROFILE" = hosting ]; then
    # hardening-hosting: sysctl e jail recidive
    rc=0
    bash "$SELF" --module hardening-hosting --check --from "$FROM" >"$TMP/v.hard" 2>&1 || rc=$?
    case "$rc" in
      0) v_ok "modulo hardening-hosting: configurazione conforme" ;;
      3) v_ko "modulo hardening-hosting: NON conforme ($(grep '\[da fare\]' "$TMP/v.hard" | head -2 | sed 's/^ *//' | tr '\n' ';'))" ;;
      *) v_ko "modulo hardening-hosting: $(tail -1 "$TMP/v.hard")" ;;
    esac
    if [ -r /etc/sysctl.d/90-pmgr-hosting.conf ]; then
      local sm
      sm="$(sysctl_mismatches /etc/sysctl.d/90-pmgr-hosting.conf)"
      if [ ! -w /proc/sys/kernel/kptr_restrict ]; then v_info "sysctl: /proc/sys non e' scrivibile qui (container): valori in uso non verificati"
      elif [ -z "$sm" ]; then v_ok "sysctl effettivi come voluto"
      else while IFS= read -r want; do v_ko "sysctl: $want"; done <<<"$sm"; fi
    fi
    if [ "$HAVE_SYSTEMD" = 1 ] && command -v fail2ban-client >/dev/null 2>&1; then
      fail2ban-client status recidive >/dev/null 2>&1 && v_ok "fail2ban: jail recidive attiva" || v_ko "fail2ban: jail recidive non attiva"
      # un ban "registrato" puo' non bloccare niente (programma mancante, regola rifiutata): lo dice solo il registro
      local fb
      fb="$(grep -c 'Failed to execute ban' /var/log/fail2ban.log 2>/dev/null || true)"
      [ "${fb:-0}" = 0 ] || v_info "fail2ban: $fb ban NON eseguiti nel registro (ultimo: $(grep 'Failed to execute ban' /var/log/fail2ban.log | tail -n 1 | cut -c1-19)): controlla /var/log/fail2ban.log"
    fi
    local fwp lst
    fwp="$(fw_problems)" || true
    if [ -z "$fwp" ]; then v_ok "firewall: $(ssh_ports | paste -sd, - | sed 's/,/, /g'), 80 e 443 aperte, il resto chiuso"; else while IFS= read -r want; do v_ko "firewall: $want"; done <<<"$fwp"; fi
    lst="$(fw_listening | tr '\n' ' ')"
    if [ -n "${lst// /}" ]; then
      # "filtrate" vale solo se un firewall e' davvero attivo: con ufw/firewalld spenti quelle porte sono raggiungibili
      if { ufw status 2>/dev/null | grep -q '^Status: active'; } || firewall-cmd --state >/dev/null 2>&1; then
        v_info "porte in ascolto filtrate dal firewall (non raggiungibili dall'esterno): $lst"
      else
        v_info "ATTENZIONE: nessun firewall attivo, queste porte in ascolto sono raggiungibili dall'esterno: $lst"
      fi
    fi
    if [ "$FAM" = rocky ]; then
      if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce)" = Enforcing ]; then v_ok "SELinux Enforcing"; else v_ko "SELinux non e' Enforcing ($(getenforce 2>/dev/null || echo assente))"; fi
    else
      if command -v aa-status >/dev/null 2>&1 && aa-status --enabled >/dev/null 2>&1; then v_ok "AppArmor attivo"; else v_info "AppArmor non attivo o non verificabile"; fi
    fi
  fi
  # Rocky: helper del manager e SELinux
  if [ "$FAM" = rocky ]; then
    local h
    for h in adduser.sh disk.sh dmesg.sh; do
      if [ ! -x "/usr/local/libexec/pmgr/$h" ]; then v_ko "helper /usr/local/libexec/pmgr/$h mancante (modulo agent-exec)"
      elif [ -f "$FROM/../helpers/$h" ] && ! same_file "$FROM/../helpers/$h" "/usr/local/libexec/pmgr/$h"; then v_ko "helper $h diverso dalla versione attuale (modulo agent-exec)"
      else v_ok "helper $h presente e aggiornato"; fi
    done
    if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce)" != Disabled ]; then
      v_info "SELinux: $(getenforce)"
      ls -Z /usr/local/libexec/pmgr/*.sh 2>/dev/null | grep -q virt_qemu_ga_unconfined_exec_t && v_ok "helper con l'etichetta virt_qemu_ga_unconfined_exec_t" || v_ko "helper senza l'etichetta virt_qemu_ga_unconfined_exec_t"
      [ "$(getsebool virt_qemu_ga_run_unconfined 2>/dev/null | awk '{print $NF}')" = on ] && v_ok "booleano virt_qemu_ga_run_unconfined attivo" || v_ko "booleano virt_qemu_ga_run_unconfined non attivo"
    else
      v_info "SELinux assente o disattivo"
    fi
    if [ "$HAVE_SYSTEMD" = 1 ]; then
      local pid
      pid="$(systemctl show -p MainPID --value qemu-guest-agent 2>/dev/null)"
      # il filtro dell'agent e' una lista di consentiti (--allow-rpcs, Rocky 9/10) oppure di vietati (--blacklist, Rocky 8 e
      # agent vecchi): guest-exec e' attivo se e' nei consentiti, o se NON e' nei vietati, o se non c'e' nessun filtro
      local cl exec_on=0
      cl="$(tr '\0' ' ' </proc/"${pid:-0}"/cmdline 2>/dev/null || true)"
      if [ -z "$cl" ] || [ "$pid" = 0 ]; then exec_on=0
      elif grep -q -- '--allow-rpcs=' <<<"$cl"; then grep -qE -- '--allow-rpcs=[^ ]*guest-exec(,| |$)' <<<"$cl" && exec_on=1
      elif grep -qE -- '--(blacklist|block-rpcs)=' <<<"$cl"; then grep -qE -- '--(blacklist|block-rpcs)=[^ ]*guest-exec(,| |$)' <<<"$cl" || exec_on=1
      else exec_on=1; fi
      if [ "$exec_on" = 1 ]; then v_ok "guest-exec abilitato nell'agent"; else v_ko "guest-exec BLOCCATO nell'agent: il manager non puo' aggiungere utenti o dischi (modulo agent-exec)"; fi
    fi
  fi
  # informazioni
  if [ ! -x /usr/local/sbin/pmgr-reboot-check ]; then v_info "controllo del riavvio non ancora installato (lo porta il modulo updates)"
  elif /usr/local/sbin/pmgr-reboot-check >"$TMP/rb" 2>&1; then v_info "riavvio necessario: no"; else v_info "riavvio necessario: SI ($(head -1 "$TMP/rb"))"; fi
  echo
  if [ "$FAILS" = 0 ]; then echo "Verifica: tutto conforme."; exit 0; fi
  echo "Verifica: $FAILS controlli falliti."
  exit 1
}

if [ "$VERIFY" = 1 ]; then do_verify; fi

echo "pmgr-vm-apply: $PRETTY_NAME$([ "$CHECK" = 1 ] && echo ' (solo controllo)')"
for m in "${MODULES[@]}"; do
  case "$m" in
    fail2ban) mod_fail2ban ;;
    updates) mod_updates ;;
    ssh-hosting) mod_ssh_hosting ;;
    ssh-standard) mod_ssh_standard ;;
    firewall-hosting) mod_firewall_hosting ;;
    hardening-hosting) mod_hardening_hosting ;;
    agent-exec) mod_agent_exec ;;
  esac
done

if [ "$CHECK" = 1 ]; then
  if [ "$CHANGES" -gt 0 ]; then echo "Modifiche da fare: $CHANGES"; exit 3; fi
  echo "Niente da fare."
  exit 0
fi
[ "$CHANGES" -gt 0 ] && echo "Fatto (modifiche: $CHANGES). Copie di sicurezza in $BACKUP" || echo "Niente da fare."
exit 0
