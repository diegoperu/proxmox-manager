#!/bin/bash
# pmgr-os.sh - crea o aggiorna il template cloud-init di una o piu' release di sistema operativo.
# Sostituisce, per ogni release, i quattro script update_image / create_template /
# create_cloud_snippet / finalize_image: i dati di ogni release stanno in os.d/<id>.conf, quelli del
# cluster in /etc/pmgr/cluster.conf. Solo ASCII.
#
# Uso:
#   pmgr-os.sh <id> [--step image|snippet|template|finalize|all] [--dry-run]
#   pmgr-os.sh --family ubuntu|debian|rocky | --all   [--dry-run]
#   pmgr-os.sh --list
#   --arch amd64|arm64: override di dpkg --print-architecture, valido per ogni modo
#   pmgr-os.sh --render DIR [--profile standard|hosting] <id>|--family F|--all
#       scrive gli snippet nella cartella DIR (niente cluster, niente root): serve a preparare le
#       varianti da sincronizzare verso un cluster di hosting
#
# Profilo (PROFILE in cluster.conf, oppure --profile): "standard" = snippet come sono; "hosting" = in piu'
# SSH solo con chiave, rimesso a ogni avvio e ogni 15 minuti (profiles/hosting/).
#
# Ordine dei passi (--step all): image -> snippet -> template -> finalize.
# Tutto cio' che puo' fallire senza danni (configurazione, storage, bridge, password, chiavi, download e
# controllo dell'immagine, generazione dello snippet) avviene PRIMA di cancellare il template esistente.
# --dry-run esegue solo i controlli in sola lettura e descrive cosa farebbe.
set -euo pipefail

# shellcheck source=../lib/pmgr-lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/pmgr-lib.sh"

usage() {
  sed -n '2,23p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
}

# ------------------------------------------------------------------ file temporanei
PMGR_TMP=()
pmgr_cleanup() {
  local f
  for f in "${PMGR_TMP[@]+"${PMGR_TMP[@]}"}"; do rm -f -- "$f"; done
}

# ------------------------------------------------------------------ passi
step_image() {
  local id="${OS[ID]}" dir final part expected actual
  dir="${CL[IMAGES_DIR]}/$id"
  final="$dir/${OS[IMAGE_FILE]}"
  part="$final.part"
  if [ "$PMGR_DRY" = 1 ]; then
    pmgr_log "[dry-run] scaricherei ${OS[IMAGE_URL]} in $final, controllo dimensione/formato${OS[IMAGE_CHECKSUM_URL]:+/checksum}, ridimensiono a ${CL[DISK_SIZE]}"
    return 0
  fi
  mkdir -p -- "$dir"
  chmod 755 -- "$dir"
  PMGR_TMP+=("$part")
  rm -f -- "$part"
  pmgr_log "scarico ${OS[IMAGE_URL]}"
  curl -fsSL --retry 3 --connect-timeout 30 --max-time 7200 -o "$part" -- "${OS[IMAGE_URL]}" \
    || pmgr_die "download fallito (${OS[ID]}): l'immagine e il template esistenti non sono stati toccati"
  pmgr_validate_image "$part" || pmgr_die "immagine scaricata non valida (${OS[ID]}): il template esistente non e' stato toccato"
  if [ -n "${OS[IMAGE_CHECKSUM_URL]:-}" ]; then
    expected="$(pmgr_checksum_for "${OS[IMAGE_CHECKSUM_URL]}" "${OS[IMAGE_CHECKSUM_ALGO]}" "${OS[IMAGE_FILE]}")" \
      || pmgr_die "non riesco a leggere il checksum di ${OS[IMAGE_FILE]} da ${OS[IMAGE_CHECKSUM_URL]} (se il mirror non lo pubblica, togli IMAGE_CHECKSUM_* da os.d/${OS[ID]}.conf)"
    actual="$("${OS[IMAGE_CHECKSUM_ALGO]}sum" -- "$part" | cut -d' ' -f1)"
    [ "$actual" = "$expected" ] || pmgr_die "checksum dell'immagine ${OS[ID]} diverso da quello pubblicato: scarto il file"
    pmgr_log "checksum ${OS[IMAGE_CHECKSUM_ALGO]} verificato"
  else
    pmgr_warn "${OS[ID]}: nessun checksum configurato, controllati solo dimensione e formato"
  fi
  qemu-img resize -f qcow2 -- "$part" "${CL[DISK_SIZE]}" >/dev/null \
    || pmgr_die "ridimensionamento a ${CL[DISK_SIZE]} fallito (${OS[ID]})"
  mv -f -- "$part" "$final"
  pmgr_log "immagine pronta: $final"
}

# Un file di profiles/hosting diventa un elemento runcmd che lo scrive con un heredoc quotato.
emit_heredoc_item() { # file destinazione
  printf '    - |\n      cat > %s <<'"'"'PMGR_EOF'"'"'\n' "$2"
  sed 's/^\(.\)/      \1/' -- "$1"
  printf '      PMGR_EOF\n'
}

# Blocco runcmd del profilo hosting: installa pmgr-ssh-enforce, il servizio (a ogni avvio, prima di sshd)
# e il timer (ogni 15 minuti), lo esegue subito e li abilita.
# Un file di script diventa un elemento runcmd a blocco ("- |") con il testo cosi' com'e', ma dentro una SOTTOSHELL:
# cloud-init concatena tutti gli elementi in un unico script, quindi un "exit" o un "set -u" dello script incorporato
# terminerebbe (o cambierebbe) tutto il resto, reboot finale compreso. Era successo con il firewall dell'hosting: dopo
# "ufw --force enable" non girava piu' niente (pmgr-ssh-enforce, sysctl, recidive, reboot).
emit_script_item() { # file
  printf '    - |\n      (\n'
  sed 's/^\(.\)/      \1/' -- "$1"
  printf '      )\n'
}

# Firewall dell'hosting: tutto chiuso in ingresso tranne 22, 80 e 443 (ufw su Debian/Ubuntu, firewalld su Rocky).
firewall_block() {
  local d="$PMGR_BASE/profiles/hosting"
  printf '#\n# Hosting: firewall con solo SSH (22), HTTP (80) e HTTPS (443) aperti in ingresso\n#\n'
  case "${OS[FAMILY]}" in
    debian | ubuntu) check_profile_files "$d" firewall-ufw.sh; emit_script_item "$d/firewall-ufw.sh" ;;
    rocky) check_profile_files "$d" firewall-firewalld.sh; emit_script_item "$d/firewall-firewalld.sh" ;;
  esac
}

hosting_block() {
  local d="$PMGR_BASE/profiles/hosting"
  check_profile_files "$d" pmgr-ssh-enforce.sh pmgr-ssh-enforce.service pmgr-ssh-enforce.timer 90-pmgr-hosting-sysctl.conf 60-pmgr-recidive.local
  firewall_block
  printf '#\n# Hosting: hardening di rete e kernel (sysctl) e jail recidive di fail2ban\n#\n'
  emit_heredoc_item "$d/90-pmgr-hosting-sysctl.conf" /etc/sysctl.d/90-pmgr-hosting.conf
  printf '    - sysctl --system >/dev/null 2>&1 || true\n'
  emit_heredoc_item "$d/60-pmgr-recidive.local" /etc/fail2ban/jail.d/60-pmgr-recidive.local
  # L'azione di ban di recidive deve essere quella PER PORTA (come sshd), non quella "allports" della jail.conf: si copia dalla jail sshd.
  # Su Rocky (firewalld) la regola di recidive ha "drop" al posto di "reject": con regole identiche a quelle di sshd, lo sblocco
  # di sshd toglierebbe anche la regola di recidive (firewalld le riconosce dal testo) pur restando il ban registrato.
  # Se non si riesce a leggere l'azione resta quella di fail2ban (tutte le porte): piu' severa, ma mai senza recidive.
  cat <<'PMGR_LINE'
    - act=$(fail2ban-client get sshd actions 2>/dev/null | tail -n 1); case "$act" in firewallcmd-rich-rules) b='firewallcmd-rich-rules[rich-blocktype=drop]' ;; iptables-multiport|nftables) b="$act" ;; *) b= ;; esac; if [ -n "$b" ]; then printf '[recidive]\nbanaction = %s\n' "$b" >/etc/fail2ban/jail.d/61-pmgr-recidive-action.local; fi
PMGR_LINE
  # se la configurazione di fail2ban non si carica si toglie la jail: meglio senza recidive che senza fail2ban
  printf '    - if command -v fail2ban-client >/dev/null 2>&1; then [ -e /var/log/fail2ban.log ] || install -m 640 /dev/null /var/log/fail2ban.log; if ! fail2ban-client -d >/dev/null 2>&1; then rm -f /etc/fail2ban/jail.d/60-pmgr-recidive.local /etc/fail2ban/jail.d/61-pmgr-recidive-action.local; fi; systemctl restart fail2ban || true; fi\n'
  printf '#\n# Hosting: SSH solo con chiave, rimesso a ogni avvio e ogni 15 minuti (pmgr-ssh-enforce)\n#\n'
  emit_heredoc_item "$d/pmgr-ssh-enforce.sh" /usr/local/sbin/pmgr-ssh-enforce
  printf '    - chmod 0750 /usr/local/sbin/pmgr-ssh-enforce\n'
  emit_heredoc_item "$d/pmgr-ssh-enforce.service" /etc/systemd/system/pmgr-ssh-enforce.service
  emit_heredoc_item "$d/pmgr-ssh-enforce.timer" /etc/systemd/system/pmgr-ssh-enforce.timer
  printf '    - if command -v restorecon >/dev/null 2>&1; then restorecon -R /usr/local/sbin/pmgr-ssh-enforce /etc/systemd/system/pmgr-ssh-enforce.service /etc/systemd/system/pmgr-ssh-enforce.timer; fi\n'
  printf '    - systemctl daemon-reload\n'
  printf '    - /usr/local/sbin/pmgr-ssh-enforce\n'
  printf '    - systemctl enable pmgr-ssh-enforce.service pmgr-ssh-enforce.timer\n'
}

# Controlla che i file di un profilo esistano, siano ASCII e non contengano la riga che chiude l'heredoc.
check_profile_files() { # cartella file...
  local d="$1" f
  shift
  for f in "$@"; do
    [ -f "$d/$f" ] || pmgr_die "manca ${d#"$PMGR_BASE"/}/$f"
    if LC_ALL=C grep -qP '[^\x09\x0a\x20-\x7e]' -- "$d/$f"; then pmgr_die "${d#"$PMGR_BASE"/}/$f contiene caratteri non ASCII"; fi
    if grep -qx 'PMGR_EOF' -- "$d/$f"; then pmgr_die "${d#"$PMGR_BASE"/}/$f contiene la riga PMGR_EOF: chiuderebbe il blocco"; fi
  done
}

# Blocco runcmd degli aggiornamenti di sicurezza automatici (tutti i cluster, tutti i profili):
# due volte al giorno controllo e installazione, mai riavvio automatico, avviso di riavvio al login.
updates_block() {
  local d="$PMGR_BASE/profiles/updates"
  check_profile_files "$d" pmgr-reboot-check.sh pmgr-reboot-required.sh
  printf '#\n# Aggiornamenti di sicurezza automatici: 2 volte al giorno, nessun riavvio automatico, avviso al login\n#\n'
  emit_heredoc_item "$d/pmgr-reboot-check.sh" /usr/local/sbin/pmgr-reboot-check
  printf '    - chmod 0755 /usr/local/sbin/pmgr-reboot-check\n'
  emit_heredoc_item "$d/pmgr-reboot-required.sh" /etc/profile.d/pmgr-reboot-required.sh
  case "${OS[FAMILY]}" in
    debian | ubuntu)
      check_profile_files "$d" "apt-${OS[FAMILY]}.conf" apt-daily.timer.conf apt-daily-upgrade.timer.conf
      emit_heredoc_item "$d/apt-${OS[FAMILY]}.conf" /etc/apt/apt.conf.d/52pmgr-unattended-upgrades
      printf '    - mkdir -p /etc/systemd/system/apt-daily.timer.d /etc/systemd/system/apt-daily-upgrade.timer.d\n'
      emit_heredoc_item "$d/apt-daily.timer.conf" /etc/systemd/system/apt-daily.timer.d/50-pmgr.conf
      emit_heredoc_item "$d/apt-daily-upgrade.timer.conf" /etc/systemd/system/apt-daily-upgrade.timer.d/50-pmgr.conf
      printf '    - systemctl daemon-reload\n'
      printf '    - systemctl enable apt-daily.timer apt-daily-upgrade.timer\n'
      ;;
    rocky)
      check_profile_files "$d" dnf-automatic.sed dnf-automatic.timer.conf dnf-automatic.service.conf
      printf '    - |\n      sed -i -E -f - /etc/dnf/automatic.conf <<'"'"'PMGR_EOF'"'"'\n'
      sed 's/^\(.\)/      \1/' -- "$d/dnf-automatic.sed"
      printf '      PMGR_EOF\n'
      printf '    - mkdir -p /etc/systemd/system/dnf-automatic.timer.d /etc/systemd/system/dnf-automatic.service.d\n'
      emit_heredoc_item "$d/dnf-automatic.timer.conf" /etc/systemd/system/dnf-automatic.timer.d/50-pmgr.conf
      emit_heredoc_item "$d/dnf-automatic.service.conf" /etc/systemd/system/dnf-automatic.service.d/50-pmgr.conf
      printf '    - systemctl daemon-reload\n'
      printf '    - systemctl enable dnf-automatic.timer\n'
      ;;
  esac
}

# Corpo dello snippet secondo il profilo. Le righe che abilitano la password SSH sono racchiuse tra i
# commenti "# pmgr:ssh-password-begin/end" (e "# pmgr:extra-tools-begin/end": strumenti di diagnostica che i clienti
# dell'hosting installano se servono): lo standard li tiene, l'hosting li toglie. Gli aggiornamenti
# automatici e, nell'hosting, pmgr-ssh-enforce vanno prima del "reboot" finale.
render_body() {
  local body="$PMGR_BASE/snippets.d/${OS[ID]}.yaml" tmp
  tmp="$(mktemp)"
  PMGR_TMP+=("$tmp")
  if [ "$PROFILE_USE" = hosting ]; then
    awk '/^# pmgr:(ssh-password|extra-tools)-begin$/ {skip = 1; next} /^# pmgr:(ssh-password|extra-tools)-end$/ {skip = 0; next} !skip' "$body" >"$tmp"
  else
    grep -v '^# pmgr:\(ssh-password\|extra-tools\)-\(begin\|end\)$' -- "$body" >"$tmp" || true
  fi
  [ "$(tail -n 1 -- "$tmp")" = "    - reboot" ] || pmgr_die "snippets.d/${OS[ID]}.yaml non finisce con '    - reboot': non so dove inserire i blocchi dei profili"
  head -n -1 -- "$tmp"
  updates_block
  if [ "$PROFILE_USE" = hosting ]; then hosting_block; fi
  printf '    - reboot\n'
}

# Scrive lo snippet completo su stdout.
render_snippet() {
  local body="$PMGR_BASE/snippets.d/${OS[ID]}.yaml" h f
  if [ -n "${OS[HELPERS]}" ]; then
    printf '#cloud-config\n\nwrite_files:\n'
    for h in ${OS[HELPERS]}; do
      f="$PMGR_BASE/helpers/$h"
      [ -f "$f" ] || pmgr_die "helper mancante: helpers/$h (copialo da internal/handlers/guestscripts/ del repository)"
      if LC_ALL=C grep -qP '[^\x09\x0a\x20-\x7e]' -- "$f"; then
        pmgr_die "helpers/$h contiene caratteri non ASCII: l'agent del guest si blocca (vedi CLAUDE.md, 'Solo ASCII')"
      fi
      printf '  - path: /usr/local/libexec/pmgr/%s\n    owner: root:root\n    permissions: "0750"\n    encoding: b64\n    content: %s\n' \
        "$h" "$(base64 -w0 -- "$f")"
    done
    printf '\n'
  fi
  render_body
}

step_snippet() {
  local out="${CL[SNIPPETS_DIR]}/${OS[SNIPPET]}" tmp
  if [ "$PMGR_DRY" = 1 ]; then
    render_snippet >/dev/null
    pmgr_log "[dry-run] scriverei lo snippet $out (profilo $PROFILE_USE${OS[HELPERS]:+, con gli helper ${OS[HELPERS]}}, corpo snippets.d/${OS[ID]}.yaml)"
    return 0
  fi
  tmp="$(mktemp "${CL[SNIPPETS_DIR]}/.${OS[SNIPPET]}.XXXXXX")"
  PMGR_TMP+=("$tmp")
  render_snippet >"$tmp"
  [ -s "$tmp" ] || pmgr_die "snippet generato vuoto (${OS[ID]})"
  chmod 644 -- "$tmp"
  if [ -f "$out" ] && cmp -s -- "$tmp" "$out"; then
    pmgr_log "snippet invariato: $out"
  else
    mv -f -- "$tmp" "$out"
    pmgr_log "snippet scritto: $out"
  fi
}

# Stato del VMID: stampa "none", "template" o "vm"; esce con errore se non e' una nostra VM.
vm_state() {
  local cfg name
  if ! cfg="$(qm config "$VMID" 2>/dev/null)"; then
    echo none
    return 0
  fi
  name="$(sed -n 's/^name: //p' <<<"$cfg")"
  [[ "$name" == *-cloudinit-template ]] \
    || pmgr_die "il VMID $VMID e' occupato da una VM che non e' un nostro template ('$name'): non la tocco. Cambia VMID_BASE in cluster.conf o spostala."
  if grep -q '^template: 1' <<<"$cfg"; then echo template; else echo vm; fi
}

step_template() {
  local id="${OS[ID]}" img state disk mem="${CL[MEMORY_MB]}"
  img="${CL[IMAGES_DIR]}/$id/${OS[IMAGE_FILE]}"
  state="$(vm_state)"
  if [ "$PMGR_DRY" != 1 ]; then
    [ -f "$img" ] || pmgr_die "immagine mancante: $img (esegui il passo image)"
    pmgr_validate_image "$img" || pmgr_die "immagine non valida: $img"
    [ -s "${CL[SNIPPETS_DIR]}/${OS[SNIPPET]}" ] || pmgr_die "snippet mancante: ${CL[SNIPPETS_DIR]}/${OS[SNIPPET]} (esegui il passo snippet)"
  fi
  if [ "$state" != none ]; then
    pmgr_log "il VMID $VMID contiene il template precedente ($state): lo ricreo"
    pmgr_run qm destroy "$VMID"
  fi
  pmgr_log "creo il template $VMID ${OS[TEMPLATE_NAME]}"
  pmgr_run qm create "$VMID" --name "${OS[TEMPLATE_NAME]}" --ostype l26 \
    --memory "$mem" --agent 1 \
    --bios ovmf --machine "${ARCH[MACHINE]}" --efidisk0 "${CL[STORAGE]}:0,pre-enrolled-keys=0" \
    --cpu "${CL[CPU_TYPE]}" --sockets 1 --cores 1 \
    --vga serial0 --serial0 socket \
    --net0 "virtio,bridge=${CL[BRIDGE]}"
  pmgr_run qm importdisk "$VMID" "$img" "${CL[STORAGE]}"
  if [ "$PMGR_DRY" = 1 ]; then
    disk="${CL[STORAGE]}:vm-$VMID-disk-1"
  else
    # il nome del volume dipende dal tipo di storage: si legge da Proxmox invece di indovinarlo
    disk="$(qm config "$VMID" | sed -n 's/^unused0: //p')"
    disk="${disk%%,*}"
    [[ "$disk" =~ ^[A-Za-z0-9._-]+:[A-Za-z0-9._/-]+$ ]] \
      || pmgr_die "dopo l'importazione non trovo il disco (unused0) nella VM $VMID: la VM esiste ma e' incompleta"
  fi
  pmgr_run qm set "$VMID" --scsihw virtio-scsi-pci --virtio0 "$disk,discard=on,cache=writeback"
  pmgr_run qm set "$VMID" --boot order=virtio0
  pmgr_run qm set "$VMID" "--${ARCH[CLOUDINIT_DRIVE]}" "${CL[CI_STORAGE]}:cloudinit"
  # autostart = riavvio dopo un crash; onboot = avvio con il nodo (le VM clonate lo ereditano: senza, non ripartono dopo il riavvio di un nodo)
  pmgr_run qm set "$VMID" --autostart 1 --onboot 1
  pmgr_run qm set "$VMID" --numa 1
  pmgr_run qm set "$VMID" --searchdomain "${CL[SEARCH_DOMAIN]}"
}

step_finalize() {
  local state hash
  state="$(vm_state)"
  if [ "$PMGR_DRY" != 1 ]; then
    [ "$state" = vm ] || pmgr_die "la VM $VMID non e' pronta per la finalizzazione (stato: $state): esegui prima il passo template"
    # una VM rimasta a meta' (disco o cloud-init mancanti) non deve diventare un template
    local cfg
    cfg="$(qm config "$VMID")"
    local k
    for k in efidisk0 virtio0 "${ARCH[CLOUDINIT_DRIVE]}"; do
      grep -q "^$k:" <<<"$cfg" || pmgr_die "la VM $VMID e' incompleta (manca $k): rifai il passo template, non la trasformo in template"
    done
    [ -s "${CL[SNIPPETS_DIR]}/${OS[SNIPPET]}" ] || pmgr_die "snippet mancante: ${CL[SNIPPETS_DIR]}/${OS[SNIPPET]}"
  fi
  pmgr_run qm set "$VMID" --cicustom "vendor=${CL[SNIPPETS_STORAGE]}:snippets/${OS[SNIPPET]}"
  pmgr_run qm set "$VMID" --tags "${OS[TAGS]}"
  pmgr_run qm set "$VMID" --ciuser "${CL[CI_USER]}"
  if [ "$PMGR_DRY" = 1 ]; then
    printf '  [dry-run] qm set %s --cipassword <nascosta>\n' "$VMID"
  else
    hash="$(printf '%s' "$PMGR_PASSWORD" | openssl passwd -6 -stdin)"
    qm set "$VMID" --cipassword "$hash"
  fi
  pmgr_run qm set "$VMID" --sshkeys "${CL[SSH_KEYS_FILE]}"
  pmgr_run qm set "$VMID" --nameserver "${CL[NAMESERVER]}"
  if [ -n "${CL[TEMPLATE_IPCONFIG]}" ]; then
    pmgr_run qm set "$VMID" --ipconfig0 "${CL[TEMPLATE_IPCONFIG]}"
  fi
  pmgr_run qm template "$VMID"
  pmgr_log "template $VMID ${OS[TEMPLATE_NAME]} pronto"
}

run_os() {
  local id="$1"
  trap pmgr_cleanup EXIT
  pmgr_load_os "$id"
  pmgr_log "== ${OS[LABEL]} (VMID $VMID, snippet ${OS[SNIPPET]})"
  case "$STEP" in
    image) step_image ;;
    snippet) step_snippet ;;
    template) step_template ;;
    finalize) step_finalize ;;
    all)
      step_image
      step_snippet
      step_template
      step_finalize
      ;;
  esac
}

# ------------------------------------------------------------------ main
STEP=all
MODE=""
TARGET=""
FAMILY=""
PROFILE_OPT=""
ARCH_OPT=""
RENDER_DIR=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) PMGR_DRY=1; shift ;;
    --step)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      STEP="$2"
      shift 2
      ;;
    --family)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      MODE=family
      FAMILY="$2"
      shift 2
      ;;
    --profile)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      PROFILE_OPT="$2"
      shift 2
      ;;
    --arch)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      [[ "$2" =~ ^(amd64|arm64)$ ]] || pmgr_die "--arch deve essere amd64 o arm64"
      ARCH_OPT="$2"
      shift 2
      ;;
    --render)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      RENDER_DIR="$2"
      shift 2
      ;;
    --all) MODE=all; shift ;;
    --list) MODE=list; shift ;;
    -h | --help) usage; exit 0 ;;
    -*) echo "opzione sconosciuta: $1" >&2; usage; exit 2 ;;
    *)
      [ -z "$TARGET" ] || { echo "una sola release alla volta (usa --family o --all)" >&2; exit 2; }
      MODE=one
      TARGET="$1"
      shift
      ;;
  esac
done
case "$STEP" in image | snippet | template | finalize | all) ;; *) echo "--step non valido: $STEP" >&2; exit 2 ;; esac
[ -n "$MODE" ] || { usage; exit 2; }
[ -z "$FAMILY" ] || [[ "$FAMILY" =~ ^(ubuntu|debian|rocky)$ ]] || { echo "famiglia non valida: $FAMILY" >&2; exit 2; }
[ "$STEP" = all ] || [ "$MODE" = one ] || { echo "--step diverso da all vale per una sola release" >&2; exit 2; }

[ -z "$PROFILE_OPT" ] || [[ "$PROFILE_OPT" =~ ^(standard|hosting)$ ]] || { echo "--profile deve essere standard o hosting" >&2; exit 2; }
if [ -n "$RENDER_DIR" ]; then
  [[ "$RENDER_DIR" =~ ^/[A-Za-z0-9._/-]{1,200}$ ]] && [[ "$RENDER_DIR" != *..* ]] || { echo "--render vuole un percorso assoluto semplice" >&2; exit 2; }
  { [ -d "$RENDER_DIR" ] && [ ! -L "$RENDER_DIR" ]; } || { echo "--render: la cartella non esiste (o e' un link simbolico): $RENDER_DIR" >&2; exit 2; }
  [ "$MODE" != list ] || { echo "--render e --list non vanno insieme" >&2; exit 2; }
fi

if [ -n "$RENDER_DIR" ]; then
  # Il manager genera gli snippet anche senza dpkg: nessun hardware dipende dal render.
  # Senza override, amd64 serve solo a validare i campi immagine inutilizzati.
  pmgr_load_arch "${ARCH_OPT:-amd64}"
else
  pmgr_load_arch "$ARCH_OPT"
fi

if [ -n "$RENDER_DIR" ] && ! [ -r "$PMGR_CLUSTER_CONF" ]; then
  # --render genera solo testo e non ha bisogno del cluster: se cluster.conf non e' leggibile (sulla sorgente della
  # sincronizzazione e' di root 0600 e pmgr-sync non lo legge) si usa "standard"; chi vuole un profilo preciso
  # passa --profile (lo fa pmgr-snippets-sync). Se e' leggibile vale come prima (PROFILE di cluster.conf).
  declare -gA CL=([VMID_BASE]=1000 [PROFILE]=standard)
else
  pmgr_load_cluster
fi
PROFILE_USE="${PROFILE_OPT:-${CL[PROFILE]}}"

if [ "$MODE" = list ]; then
  pmgr_all_os_ids
  for id in "${PMGR_IDS[@]}"; do
    pmgr_load_os "$id"
    available=disponibile
    if ! pmgr_os_available; then available='non disponibile'; fi
    printf '%-14s VMID %-6s %-8s %s (%s: %s)\n' "$id" "$VMID" "${OS[FAMILY]}" "${OS[LABEL]}" "$PMGR_ARCH" "$available"
  done
  exit 0
fi

case "$MODE" in
  one)
    pmgr_all_os_ids
    IDS=("$TARGET")
    ;;
  family)
    pmgr_all_os_ids "$FAMILY"
    IDS=("${PMGR_IDS[@]+"${PMGR_IDS[@]}"}")
    ;;
  all)
    pmgr_all_os_ids
    IDS=("${PMGR_IDS[@]+"${PMGR_IDS[@]}"}")
    ;;
esac
[ "${#IDS[@]}" -gt 0 ] || pmgr_die "nessuna release da aggiornare"

if [ -n "$RENDER_DIR" ]; then
  # solo generazione degli snippet in una cartella: nessun controllo del cluster, nessun root, nessun lock
  for id in "${IDS[@]}"; do
    pmgr_load_os "$id"
    tmp="$(mktemp "$RENDER_DIR/.${OS[SNIPPET]}.XXXXXX")"
    PMGR_TMP+=("$tmp")
    render_snippet >"$tmp"
    [ -s "$tmp" ] || pmgr_die "snippet generato vuoto ($id)"
    chmod 644 -- "$tmp"
    mv -f -- "$tmp" "$RENDER_DIR/${OS[SNIPPET]}"
    pmgr_log "snippet $id (profilo $PROFILE_USE) scritto in $RENDER_DIR/${OS[SNIPPET]}"
  done
  pmgr_cleanup
  exit 0
fi

AVAILABLE_IDS=()
for id in "${IDS[@]}"; do
  pmgr_load_os "$id"
  if pmgr_os_available; then
    AVAILABLE_IDS+=("$id")
  elif [ "$MODE" = one ]; then
    pmgr_die "$id non disponibile per l'architettura $PMGR_ARCH (ARCHES=${OS[ARCHES]})"
  else
    pmgr_log "$id saltata: non disponibile per l'architettura $PMGR_ARCH (ARCHES=${OS[ARCHES]})"
  fi
done
IDS=("${AVAILABLE_IDS[@]}")
# Un gruppo senza release compatibili non richiede alcuna operazione sul nodo.
[ "${#IDS[@]}" -gt 0 ] || exit 0

if [ "$PMGR_DRY" != 1 ]; then
  [ "$(id -u)" = 0 ] || pmgr_die "va lanciato come root (oppure usa --dry-run)"
fi
pmgr_preflight_cluster
[ "$PMGR_DRY" = 1 ] || pmgr_lock

FAILED=()
for id in "${IDS[@]}"; do
  # Non in un "if": dentro una condizione bash ignora set -e e un comando fallito non fermerebbe la release.
  set +e
  ( set -e; run_os "$id" )
  rc=$?
  set -e
  if [ "$rc" != 0 ]; then
    FAILED+=("$id")
    pmgr_log "RELEASE FALLITA: $id (passo $STEP); si prosegue con le altre"
  fi
done

if [ "${#FAILED[@]}" -gt 0 ]; then
  pmgr_log "riepilogo: falliti: ${FAILED[*]}"
  exit 1
fi
pmgr_log "riepilogo: tutto a posto (${IDS[*]})"
