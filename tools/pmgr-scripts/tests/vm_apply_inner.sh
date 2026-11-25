#!/bin/bash
# Gira DENTRO un container: esegue il payload prodotto da pmgr-vm-rollout.sh (bash -s -- ARGS < payload),
# cioe' la stessa catena che vede una VM vera, su una VM "vecchia" simulata.
set -u
. /etc/os-release
export DEBIAN_FRONTEND=noninteractive
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "   FALLITO: $1"; }
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then ok; else ko "$d"; fi; }
FAM=deb
case "${ID_LIKE:-}$ID" in
  *debian* | *ubuntu*) apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq openssh-server openssh-client python3 systemd fail2ban >/dev/null 2>&1 ;;
  *rhel* | *rocky*) FAM=rpm; bash /w/rocky_repo.sh; dnf -y -q install epel-release >/dev/null 2>&1; dnf -y -q install openssh-server python3 systemd fail2ban >/dev/null 2>&1 ;;
esac
echo "### $WANT | $PRETTY_NAME"
P=/w/payload.sh
apply() { bash -s -- "$@" <$P >/tmp/out 2>&1; RC=$?; }
mkdir -p /run/sshd /etc/ssh/sshd_config.d

echo "-- payload"
bash -n $P && ok || ko "il payload non e' uno script valido"
grep -q 'PMGR_B64' $P && ok || ko "il payload non contiene l'archivio"

# la VM "vecchia": fail2ban come lo lasciavano i vecchi snippet
cp /etc/fail2ban/jail.conf /etc/fail2ban/jail.local; sed -i -e '/^bantime/s/^.*$/bantime = 1d/' /etc/fail2ban/jail.local
# VM vecchia: sshd come lo lasciava l'immagine (Rocky 8: PermitRootLogin yes, nessun Include)
if [ "$FAM" = rpm ]; then mkdir -p /usr/local/libexec/pmgr; for h in adduser disk dmesg; do printf '#!/bin/sh\n' >/usr/local/libexec/pmgr/$h.sh; chmod 750 /usr/local/libexec/pmgr/$h.sh; done; fi

echo "-- --verify su una VM vecchia"
apply --verify
[ "$RC" = 1 ] && ok || ko "--verify su VM vecchia: uscita $RC (attesa 1)"
grep -q 'FALLITO.*modulo fail2ban' /tmp/out && grep -q 'FALLITO.*modulo updates' /tmp/out && ok || ko "--verify non segnala fail2ban e updates: $(grep FALLITO /tmp/out | head -3 | tr '\n' ' ')"
check "--verify non scrive niente" test ! -e /usr/local/sbin/pmgr-reboot-check

echo "-- modulo fail2ban"
apply --module fail2ban --check
[ "$RC" = 3 ] && ok || ko "fail2ban --check su VM vecchia: uscita $RC (attesa 3)"
apply --module fail2ban
[ "$RC" = 0 ] && ok || ko "fail2ban: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
check "fail2ban: file di configurazione scritto" test -f /etc/fail2ban/jail.d/50-pmgr.local
check "fail2ban: vecchia copia di jail.conf messa da parte" test ! -e /etc/fail2ban/jail.local
apply --module fail2ban --check
[ "$RC" = 0 ] && ok || ko "fail2ban --check dopo l'applicazione: uscita $RC"

echo "-- modulo updates"
apply --module updates --check
[ "$RC" = 3 ] && ok || ko "--check su VM vecchia: uscita $RC (attesa 3): $(tail -3 /tmp/out | tr '\n' ' ')"
check "--check non scrive niente" test ! -e /usr/local/sbin/pmgr-reboot-check
apply --module updates
[ "$RC" = 0 ] && ok || ko "updates: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
check "pmgr-reboot-check installato 755" bash -c '[ "$(stat -c %a /usr/local/sbin/pmgr-reboot-check)" = 755 ]'
check "avviso di login installato" test -f /etc/profile.d/pmgr-reboot-required.sh
if [ "$FAM" = deb ]; then
  check "unattended-upgrades installato" dpkg -s unattended-upgrades
  A="$(apt-config dump 2>/dev/null)"
  grep -qF 'Unattended-Upgrade::Automatic-Reboot "false";' <<<"$A" && grep -qF 'APT::Periodic::Unattended-Upgrade "always";' <<<"$A" && ok || ko "configurazione apt effettiva"
  check "timer di installazione due volte al giorno" bash -c '[ "$(grep -c "^OnCalendar=." /etc/systemd/system/apt-daily-upgrade.timer.d/50-pmgr.conf)" = 2 ]'
else
  check "dnf-automatic e dnf-utils (needs-restarting) installati" bash -c 'rpm -q dnf-automatic && command -v needs-restarting'
  check "automatic.conf: sicurezza + installa" bash -c 'grep -qE "^upgrade_type = security$" /etc/dnf/automatic.conf && grep -qE "^apply_updates = yes$" /etc/dnf/automatic.conf'
  check "timer di dnf-automatic due volte al giorno" bash -c '[ "$(grep -c "^OnCalendar=." /etc/systemd/system/dnf-automatic.timer.d/50-pmgr.conf)" = 2 ]'
fi
apply --module updates
[ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "updates seconda esecuzione non idempotente (uscita $RC)"
apply --module updates --check
[ "$RC" = 0 ] && ok || ko "updates --check dopo l'applicazione: uscita $RC"

echo "-- modulo updates su una VM con scheda NVIDIA (Debian/Ubuntu)"
if [ "$FAM" = deb ]; then
  mkdir -p /tmp/fakepci/0000:01:00.0 /tmp/fakepci/0000:00:03.0
  echo 0x10de >/tmp/fakepci/0000:01:00.0/vendor; echo 0x030200 >/tmp/fakepci/0000:01:00.0/class
  echo 0x1af4 >/tmp/fakepci/0000:00:03.0/vendor; echo 0x020000 >/tmp/fakepci/0000:00:03.0/class
  export PMGR_SYSFS_PCI=/tmp/fakepci
  apply --module updates --check
  [ "$RC" = 3 ] && grep -q '53pmgr-gpu-hold' /tmp/out && ok || ko "updates --check su VM con GPU: dovrebbe annunciare le esclusioni (uscita $RC)"
  apply --module updates
  [ "$RC" = 0 ] && grep -q 'scheda NVIDIA rilevata' /tmp/out && ok || ko "updates su VM con GPU: uscita $RC: $(tail -3 /tmp/out | tr '\n' ' ')"
  A="$(apt-config dump 2>/dev/null)"
  for pat in 'linux-image-.*' 'linux-headers-.*' 'linux-modules-.*' 'nvidia-.*' 'libnvidia-.*' 'cuda-.*' 'docker.*' 'containerd.*' 'slurm.*' 'munge$'; do
    grep -qF "Unattended-Upgrade::Package-Blacklist:: \"$pat\";" <<<"$A" && ok || ko "esclusione mancante: $pat"
  done
  grep -qF 'Unattended-Upgrade::Automatic-Reboot "false";' <<<"$A" && grep -qF 'APT::Periodic::Unattended-Upgrade "always";' <<<"$A" && ok || ko "con la GPU gli aggiornamenti di sicurezza non sono piu' attivi"
  [ "$(grep -cE '^Unattended-Upgrade::(Allowed-Origins|Origins-Pattern)::' <<<"$A")" -ge 1 ] && ok || ko "origini di sicurezza perse"
  check "pmgr-gpu-note installato 755" bash -c '[ "$(stat -c %a /usr/local/sbin/pmgr-gpu-note)" = 755 ]'
  check "nota di login installata" test -f /etc/profile.d/pmgr-gpu-note.sh
  # senza kernel esclusi non si nomina niente di troppo largo: pacchetti normali NON esclusi
  for pat in 'openssl' 'libc6' 'sudo' 'openssh-server' 'libcurl4' 'linux-firmware' 'linux-libc-dev'; do
    grep -qF "Package-Blacklist:: \"$pat" <<<"$A" && ko "esclusione troppo larga: colpisce $pat" || ok
  done
  apply --module updates
  [ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "updates su VM con GPU non idempotente (uscita $RC)"
  apply --verify --profile standard
  grep -q 'INFO.*scheda NVIDIA rilevata' /tmp/out && ok || ko "--verify non segnala la scheda NVIDIA"
  # nota GPU: kernel nuovo con e senza modulo, e aggiornamenti in attesa (apt finto)
  mkdir -p /tmp/gpu/boot /tmp/gpu/mod/6.8.0-141-generic/updates/dkms /tmp/gpu/mod/6.8.0-142-generic /tmp/gpustub
  touch /tmp/gpu/boot/vmlinuz-6.8.0-139-generic /tmp/gpu/boot/vmlinuz-6.8.0-141-generic
  touch /tmp/gpu/mod/6.8.0-141-generic/updates/dkms/nvidia.ko.zst
  printf '#!/bin/sh\nprintf "Listing...\\nlinux-image-virtual/noble-security 6.8.0.142 amd64 [upgradable from: 6.8.0.141]\\nlibnvidia-compute-535/noble-security 535.1 amd64 [upgradable from: 535.0]\\ndocker-ce/noble 5:28 amd64 [upgradable from: 5:27]\\nopenssl/noble-security 3.0.13 amd64 [upgradable from: 3.0.12]\\n"\n' >/tmp/gpustub/apt; chmod 755 /tmp/gpustub/apt
  G="env PMGR_UNAME=6.8.0-139-generic PMGR_BOOT=/tmp/gpu/boot PMGR_MODULES=/tmp/gpu/mod PATH=/tmp/gpustub:$PATH"
  rm -f /run/pmgr-gpu-note; $G /usr/local/sbin/pmgr-gpu-note --update-flag
  N="$(/usr/local/sbin/pmgr-gpu-note)"
  grep -q 'kernel 6.8.0-141-generic installato con il modulo NVIDIA' <<<"$N" && ok || ko "kernel nuovo con modulo: nota errata: $N"
  grep -q '3 aggiornamenti NON automatici' <<<"$N" && ok || ko "conteggio degli aggiornamenti trattenuti errato (attesi 3, openssl escluso): $N"
  touch /tmp/gpu/boot/vmlinuz-6.8.0-142-generic
  $G /usr/local/sbin/pmgr-gpu-note --update-flag
  N="$(/usr/local/sbin/pmgr-gpu-note)"
  grep -q 'ATTENZIONE.*6.8.0-142-generic manca il modulo NVIDIA.*NON riavviare' <<<"$N" && ok || ko "kernel senza modulo: nota errata: $N"
  rm -f /tmp/gpu/boot/vmlinuz-6.8.0-141-generic /tmp/gpu/boot/vmlinuz-6.8.0-142-generic
  $G /usr/local/sbin/pmgr-gpu-note --update-flag
  N="$(/usr/local/sbin/pmgr-gpu-note)"
  grep -q 'kernel' <<<"$N" | grep -q ATTENZIONE; grep -q 'ATTENZIONE' <<<"$N" && ko "nessun kernel nuovo ma la nota avvisa: $N" || ok
  # il login mostra la nota (shell interattiva con terminale) e tace negli script
  printf 'GPU: prova\n' >/run/pmgr-gpu-note
  [ -z "$(sh -c '. /etc/profile.d/pmgr-gpu-note.sh' </dev/null 2>&1)" ] && ok || ko "la nota di login non deve scrivere negli script"
  # la scheda sparisce (VM senza GPU): le esclusioni e la nota vengono tolte, gli aggiornamenti di sicurezza restano
  rm -rf /tmp/fakepci/0000:01:00.0
  apply --module updates
  [ "$RC" = 0 ] && ok || ko "updates dopo la rimozione della GPU: uscita $RC"
  check "esclusioni tolte senza GPU" bash -c "test ! -e /etc/apt/apt.conf.d/53pmgr-gpu-hold && test ! -e /usr/local/sbin/pmgr-gpu-note && test ! -e /etc/profile.d/pmgr-gpu-note.sh"
  apply --module updates --check
  [ "$RC" = 0 ] && ok || ko "updates --check senza GPU: uscita $RC"
  unset PMGR_SYSFS_PCI
  rm -f /run/pmgr-gpu-note
fi

echo "-- modulo updates su una VM con Docker (senza GPU): docker, containerd e runc esclusi, il resto si aggiorna"
if [ "$FAM" = deb ]; then
  printf '#!/bin/sh\nexit 0\n' >/usr/local/bin/docker; chmod 755 /usr/local/bin/docker
  apply --module updates --check
  [ "$RC" = 3 ] && grep -q '53pmgr-docker-hold' /tmp/out && ok || ko "updates --check su VM con Docker: dovrebbe annunciare le esclusioni (uscita $RC)"
  apply --module updates
  [ "$RC" = 0 ] && grep -q 'Docker rilevato' /tmp/out && ok || ko "updates su VM con Docker: uscita $RC: $(tail -3 /tmp/out | tr '\n' ' ')"
  A="$(apt-config dump 2>/dev/null)"
  for pat in 'docker.*' 'containerd.*' 'runc$'; do
    grep -qF "Unattended-Upgrade::Package-Blacklist:: \"$pat\";" <<<"$A" && ok || ko "esclusione Docker mancante: $pat"
  done
  for pat in 'linux-image' 'openssl' 'libc6' 'sudo' 'openssh-server' 'nvidia'; do
    grep -qF "Package-Blacklist:: \"$pat" <<<"$A" && ko "con solo Docker si esclude troppo: $pat" || ok
  done
  grep -qF 'APT::Periodic::Unattended-Upgrade "always";' <<<"$A" && grep -qF 'Unattended-Upgrade::Automatic-Reboot "false";' <<<"$A" && ok || ko "con Docker gli aggiornamenti di sicurezza non sono piu' attivi"
  apply --module updates
  [ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "updates su VM con Docker non idempotente (uscita $RC)"
  apply --verify --profile hosting
  grep -q 'INFO.*Docker rilevato' /tmp/out && ok || ko "--verify non segnala Docker"
  rm -f /usr/local/bin/docker
  apply --module updates
  [ "$RC" = 0 ] && ok || ko "updates dopo la rimozione di Docker: uscita $RC"
  check "esclusione Docker tolta senza Docker" test ! -e /etc/apt/apt.conf.d/53pmgr-docker-hold
fi

echo "-- modulo ssh-standard (VM esistente: root no, chiavi si', la password NON si tocca mai)"
useradd -m -s /bin/bash tecnico 2>/dev/null; install -d -m 700 -o tecnico /home/tecnico/.ssh
ssh-keygen -q -t ed25519 -N '' -C admin@test -f /tmp/adminkey; install -m 600 -o tecnico /tmp/adminkey.pub /home/tecnico/.ssh/authorized_keys
export SUDO_USER=tecnico
K=$(mktemp -d); ssh-keygen -q -t ed25519 -N '' -f $K/k
effv() { sshd -T -f /etc/ssh/sshd_config -h $K/k 2>/dev/null | tr 'A-Z' 'a-z'; }
PW_BEFORE="$(effv | grep '^passwordauthentication ')"
apply --module ssh-standard --check
[ "$RC" = 3 ] && ok || ko "ssh-standard --check su VM vecchia: uscita $RC (attesa 3): $(tail -3 /tmp/out | tr '\n' ' ')"
check "--check non scrive niente" test ! -e /etc/ssh/sshd_config.d/00-pmgr-admin.conf
apply --module ssh-standard
[ "$RC" = 0 ] && ok || ko "ssh-standard: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
EFF="$(effv)"
grep -qx 'permitrootlogin no' <<<"$EFF" && grep -qx 'pubkeyauthentication yes' <<<"$EFF" && ok || ko "ssh-standard: valori effettivi: $(grep -E '^(pubkey|permitroot)' <<<"$EFF" | tr '\n' ' ')"
[ "$(grep '^passwordauthentication ' <<<"$EFF")" = "$PW_BEFORE" ] && ok || ko "ssh-standard ha CAMBIATO la password SSH: era '$PW_BEFORE', ora '$(grep '^passwordauthentication ' <<<"$EFF")'"
check "il file scritto non nomina PasswordAuthentication" bash -c "test -f /etc/ssh/sshd_config.d/00-pmgr-admin.conf && ! grep -qi 'PasswordAuthentication' <(grep -v '^#' /etc/ssh/sshd_config.d/00-pmgr-admin.conf)"
[ "$(grep -vE '^\s*(#|$)' /etc/ssh/sshd_config | head -1)" = "Include /etc/ssh/sshd_config.d/*.conf" ] && ok || ko "Include non e' la prima riga attiva"
apply --module ssh-standard
[ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "ssh-standard seconda esecuzione non idempotente (uscita $RC)"
apply --module ssh-standard --check
[ "$RC" = 0 ] && ok || ko "ssh-standard --check dopo l'applicazione: uscita $RC"

echo "-- ssh-standard: password disattivata dal proprietario -> resta disattivata (e root no vince su un altro file)"
rm -f /etc/ssh/sshd_config.d/00-pmgr-admin.conf
printf 'PasswordAuthentication no\nPermitRootLogin yes\n' >/etc/ssh/sshd_config.d/10-proprietario.conf
apply --module ssh-standard
[ "$RC" = 0 ] && ok || ko "ssh-standard con password disattivata: uscita $RC: $(tail -3 /tmp/out | tr '\n' ' ')"
EFF="$(effv)"
grep -qx 'passwordauthentication no' <<<"$EFF" && grep -qx 'permitrootlogin no' <<<"$EFF" && ok || ko "password riabilitata o root non chiuso: $(grep -E '^(passwordauth|permitroot)' <<<"$EFF" | tr '\n' ' ')"
echo "-- ssh-standard: chiavi pubbliche spente da un altro file -> riaccese"
rm -f /etc/ssh/sshd_config.d/00-pmgr-admin.conf
printf 'PubkeyAuthentication no\n' >/etc/ssh/sshd_config.d/10-proprietario.conf
apply --module ssh-standard
[ "$RC" = 0 ] && grep -qx 'pubkeyauthentication yes' <<<"$(effv)" && ok || ko "PubkeyAuthentication non riabilitata (uscita $RC)"

echo "-- ssh-standard: se l'amministratore non puo' entrare lo dice"
rm -f /etc/ssh/sshd_config.d/00-pmgr-admin.conf
printf 'AllowUsers alice\n' >/etc/ssh/sshd_config.d/10-proprietario.conf
apply --module ssh-standard
[ "$RC" = 0 ] && grep -q 'accesso amministrativo: AllowUsers di sshd non include tecnico' /tmp/out && ok || ko "AllowUsers che esclude tecnico non segnalato (uscita $RC): $(tail -3 /tmp/out | tr '\n' ' ')"
apply --verify --profile standard
grep -q 'FALLITO.*accesso amministrativo: AllowUsers' /tmp/out && ok || ko "--verify non segnala AllowUsers"
printf 'DenyUsers tecnico\n' >/etc/ssh/sshd_config.d/10-proprietario.conf
apply --module ssh-standard
grep -q 'DenyUsers di sshd include tecnico' /tmp/out && ok || ko "DenyUsers che include tecnico non segnalato"
rm -f /etc/ssh/sshd_config.d/10-proprietario.conf
mv /home/tecnico/.ssh/authorized_keys /tmp/ak.bak
apply --module ssh-standard
grep -q 'nessuna chiave pubblica valida' /tmp/out && ok || ko "assenza di chiavi non segnalata"
apply --verify --profile standard
grep -q 'FALLITO.*nessuna chiave pubblica valida' /tmp/out && ok || ko "--verify non segnala l'assenza di chiavi"
printf 'questo non e una chiave\n' >/home/tecnico/.ssh/authorized_keys
apply --module ssh-standard
grep -q 'nessuna chiave pubblica valida' /tmp/out && ok || ko "un authorized_keys senza chiavi valide non e' segnalato"
install -m 600 -o tecnico /tmp/ak.bak /home/tecnico/.ssh/authorized_keys
apply --module ssh-standard
grep -q 'accesso amministrativo' /tmp/out && ko "avviso spurio con chiave e sshd a posto: $(grep 'accesso amm' /tmp/out)" || ok

echo "-- ssh-standard: account scaduto = problema; password bloccata con UsePAM attivo = normale"
chage -E 2000-01-01 tecnico 2>/dev/null
apply --module ssh-standard
grep -q "e' scaduto" /tmp/out && ok || ko "account scaduto non segnalato: $(grep -i 'accesso amm' /tmp/out | head -1)"
chage -E -1 tecnico 2>/dev/null
usermod -L tecnico 2>/dev/null
apply --module ssh-standard
grep -q 'accesso amministrativo' /tmp/out && ko "password bloccata con UsePAM attivo segnalata come problema: $(grep 'accesso amm' /tmp/out)" || ok
usermod -U tecnico 2>/dev/null

echo "-- modulo agent-exec"
if [ "$FAM" = deb ]; then
  apply --module agent-exec
  [ "$RC" = 0 ] && grep -q 'niente da fare' /tmp/out && ok || ko "agent-exec su Debian/Ubuntu deve essere un no-op (uscita $RC): $(tail -2 /tmp/out | tr '\n' ' ')"
else
  CFG=/etc/sysconfig/qemu-ga; mkdir -p /etc/sysconfig
  printf 'BLACKLIST_RPC=guest-file-open,guest-exec,guest-file-close,guest-exec-status\nFSFREEZE_HOOK_PATHNAME=/etc/qemu-ga/fsfreeze-hook\n' >$CFG
  apply --module agent-exec --check
  [ "$RC" = 3 ] && ok || ko "agent-exec --check su Rocky vecchia: uscita $RC (attesa 3): $(tail -3 /tmp/out | tr '\n' ' ')"
  check "--check non cambia il filtro" grep -q 'guest-exec' $CFG
  apply --module agent-exec
  [ "$RC" = 0 ] && ok || ko "agent-exec: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
  [ "$(sed -n 1p $CFG)" = 'BLACKLIST_RPC=guest-file-open,guest-file-close' ] && ok || ko "filtro Rocky 8 non corretto: $(sed -n 1p $CFG)"
  check "le altre righe del file restano" grep -qx 'FSFREEZE_HOOK_PATHNAME=/etc/qemu-ga/fsfreeze-hook' $CFG
  check "helper installati 750 root" bash -c '[ "$(stat -c "%a %U" /usr/local/libexec/pmgr/adduser.sh)" = "750 root" ] && [ "$(stat -c "%a" /usr/local/libexec/pmgr)" = 750 ]'
  check "gli helper vecchi sono stati sostituiti da quelli attuali (sudo garantito)" grep -q 'visudo -cf' /usr/local/libexec/pmgr/adduser.sh
  check "disk.sh e dmesg.sh presenti" bash -c 'test -x /usr/local/libexec/pmgr/disk.sh && test -x /usr/local/libexec/pmgr/dmesg.sh'
  apply --module agent-exec
  [ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "agent-exec seconda esecuzione non idempotente (uscita $RC)"
  apply --module agent-exec --check
  [ "$RC" = 0 ] && ok || ko "agent-exec --check dopo l'applicazione: uscita $RC"
  # Rocky 9/10: elenco dei consentiti senza guest-exec
  printf 'FILTER_RPC_ARGS="--allow-rpcs=guest-ping,guest-info"\n' >$CFG
  apply --module agent-exec
  grep -qx 'FILTER_RPC_ARGS="--allow-rpcs=guest-ping,guest-info,guest-exec,guest-exec-status"' $CFG && ok || ko "consentiti Rocky 9/10 non corretti: $(cat $CFG)"
  printf 'FILTER_RPC_ARGS="--allow-rpcs=guest-ping,guest-exec,guest-exec-status"\n' >$CFG; cp $CFG /tmp/cfg.before
  apply --module agent-exec
  [ "$(sha256sum <$CFG)" = "$(sha256sum </tmp/cfg.before)" ] && ok || ko "consentiti gia' a posto ma il file e' cambiato"
  # elenco dei vietati
  printf 'FILTER_RPC_ARGS="--block-rpcs=guest-exec,guest-exec-status,guest-shutdown"\n' >$CFG
  apply --module agent-exec
  grep -qx 'FILTER_RPC_ARGS="--block-rpcs=guest-shutdown"' $CFG && ok || ko "vietati Rocky 9/10 non corretti: $(cat $CFG)"
  # il file permessi non si allarga e il file mancante e' un errore chiaro
  chmod 600 $CFG; printf 'BLACKLIST_RPC=guest-exec\n' >$CFG; chmod 600 $CFG
  apply --module agent-exec
  [ "$(stat -c %a $CFG)" = 600 ] && ok || ko "i permessi del file dell'agent sono cambiati"
  mv $CFG /tmp/qemu-ga.moved
  apply --module agent-exec
  [ "$RC" = 1 ] && grep -q 'non trovato' /tmp/out && ok || ko "file dell'agent mancante: uscita $RC"
  mv /tmp/qemu-ga.moved $CFG
  # le righe vietate negli helper non devono mai essere di un altro tipo: un helper non ASCII sarebbe un problema per l'agent
  check "gli helper installati sono solo ASCII" bash -c "! LC_ALL=C grep -qP '[^\x09\x0a\x20-\x7e]' /usr/local/libexec/pmgr/*.sh"
fi

echo "-- --verify profilo standard dopo baseline + ssh-standard"
apply --verify --profile standard
[ "$RC" = 0 ] && ok || ko "--verify standard dopo l'applicazione: uscita $RC: $(grep FALLITO /tmp/out | head -3 | tr '\n' ' ')"
grep -q 'OK.*modulo fail2ban' /tmp/out && grep -q 'OK.*modulo updates' /tmp/out && grep -q "OK.*accesso amministrativo: l'utente tecnico ha chiavi" /tmp/out && grep -q 'INFO.*passwordauthentication' /tmp/out && ok || ko "--verify standard: mancano righe OK/INFO: $(grep -E 'accesso|password' /tmp/out | tr '\n' ' ')"
apply --verify --profile hosting
[ "$RC" = 1 ] && ok || ko "--verify hosting su VM standard: uscita $RC (attesa 1: manca l'imposizione della sola chiave)"
rm -rf /home/tecnico/.ssh /etc/ssh/sshd_config.d/00-pmgr-admin.conf; unset SUDO_USER

echo "-- modulo ssh-hosting: nessuna chiave"
cat >>/etc/ssh/sshd_config <<'EOF'
PasswordAuthentication yes
PermitRootLogin yes
EOF
rm -rf /root/.ssh
apply --module ssh-hosting
[ "$RC" = 1 ] && grep -q "nessun utente ha una chiave" /tmp/out && ok || ko "senza chiavi dovrebbe rifiutare (uscita $RC): $(tail -2 /tmp/out | tr '\n' ' ')"
check "senza chiavi non scrive niente" bash -c 'test ! -e /usr/local/sbin/pmgr-ssh-enforce && test ! -e /etc/ssh/sshd_config.d/00-pmgr-hosting.conf'
# una chiave "finta" non basta: righe senza un vero blob base64
mkdir -p /root/.ssh; printf '# commento\nssh-ed25519 corta\nnon e una chiave\n' >/root/.ssh/authorized_keys
apply --module ssh-hosting
[ "$RC" = 1 ] && ok || ko "chiavi non valide: dovrebbe rifiutare (uscita $RC)"
# nologin non conta
useradd -m -s /usr/sbin/nologin senzashell 2>/dev/null || useradd -m -s /sbin/nologin senzashell
mkdir -p /home/senzashell/.ssh
ssh-keygen -q -t ed25519 -N '' -f /tmp/k1; cp /tmp/k1.pub /home/senzashell/.ssh/authorized_keys
apply --module ssh-hosting
[ "$RC" = 1 ] && ok || ko "una chiave di un utente senza shell non deve contare (uscita $RC)"
apply --module ssh-hosting --allow-no-keys --check
[ "$RC" = 3 ] && ok || ko "--allow-no-keys --check: uscita $RC (attesa 3)"

echo "-- modulo ssh-hosting: con una chiave"
cp /tmp/k1.pub /root/.ssh/authorized_keys
apply --module ssh-hosting --check
[ "$RC" = 3 ] && ok || ko "--check con chiave: uscita $RC (attesa 3)"
apply --module ssh-hosting
[ "$RC" = 0 ] && ok || ko "ssh-hosting con chiave: uscita $RC: $(tail -5 /tmp/out | tr '\n' ' ')"
grep -q '\[chiavi\]  root' /tmp/out && ok || ko "non dice di quali chiavi si e' accorto"
K=$(mktemp -d); ssh-keygen -q -t ed25519 -N '' -f $K/k
EFF="$(sshd -T -f /etc/ssh/sshd_config -h $K/k 2>/dev/null | tr 'A-Z' 'a-z')"
for kv in "passwordauthentication no" "authenticationmethods publickey" "permitrootlogin no" "pubkeyauthentication yes"; do
  grep -qx "$kv" <<<"$EFF" && ok || ko "valore effettivo dopo l'applicazione: manca '$kv'"
done
check "servizio e timer installati" bash -c 'test -f /etc/systemd/system/pmgr-ssh-enforce.service && test -f /etc/systemd/system/pmgr-ssh-enforce.timer && test -x /usr/local/sbin/pmgr-ssh-enforce'
apply --module ssh-hosting
[ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "ssh-hosting seconda esecuzione non idempotente (uscita $RC): $(tail -3 /tmp/out | tr '\n' ' ')"
apply --module ssh-hosting --check
[ "$RC" = 0 ] && ok || ko "ssh-hosting --check dopo l'applicazione: uscita $RC"

echo "-- modulo ssh-hosting: la configurazione di sshd e' gia' rotta -> ripristino"
rm -f /usr/local/sbin/pmgr-ssh-enforce /etc/systemd/system/pmgr-ssh-enforce.* /etc/ssh/sshd_config.d/00-pmgr-hosting.conf
printf 'OpzioneInesistente yes\n' >/etc/ssh/sshd_config.d/99-rotto.conf
S1="$(cat /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf | sha256sum)"
apply --module ssh-hosting
[ "$RC" = 1 ] && ok || ko "con sshd gia' rotto dovrebbe fallire (uscita $RC)"
check "ripristinati i file nuovi" bash -c 'test ! -e /usr/local/sbin/pmgr-ssh-enforce && test ! -e /etc/systemd/system/pmgr-ssh-enforce.service'
[ "$S1" = "$(cat /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf | sha256sum)" ] && ok || ko "la configurazione di sshd non e' tornata identica"
rm -f /etc/ssh/sshd_config.d/99-rotto.conf

echo "-- modulo hardening-hosting"
apply --module hardening-hosting --check
[ "$RC" = 3 ] && ok || ko "hardening-hosting --check su VM senza: uscita $RC (attesa 3)"
check "hardening-hosting --check non scrive niente" test ! -e /etc/sysctl.d/90-pmgr-hosting.conf
apply --module hardening-hosting
[ "$RC" = 0 ] && ok || ko "hardening-hosting: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
check "sysctl scritto 644 e identico al profilo" bash -c '[ "$(stat -c %a /etc/sysctl.d/90-pmgr-hosting.conf)" = 644 ] && [ "$(sha256sum </etc/sysctl.d/90-pmgr-hosting.conf)" = "$(sha256sum </w/hosting-sysctl.conf)" ]'
check "recidive scritto e identico al profilo" bash -c '[ "$(sha256sum </etc/fail2ban/jail.d/60-pmgr-recidive.local)" = "$(sha256sum </w/hosting-recidive.local)" ]'
check "fail2ban carica la jail recidive" bash -c "fail2ban-client -d 2>&1 | grep -q \"'recidive'\""
check "recidive: 3 ban in un giorno, ban di una settimana" bash -c "fail2ban-client -d 2>&1 | grep -qF \"['set', 'recidive', 'maxretry', 3]\" && fail2ban-client -d 2>&1 | grep -qF \"['set', 'recidive', 'bantime', '1w']\""
check "il registro di fail2ban esiste (recidive lo legge)" test -e /var/log/fail2ban.log
apply --module hardening-hosting
[ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "hardening-hosting seconda esecuzione non idempotente (uscita $RC): $(tail -3 /tmp/out | tr '\n' ' ')"
apply --module hardening-hosting --check
[ "$RC" = 0 ] && ok || ko "hardening-hosting --check dopo l'applicazione: uscita $RC"
echo "-- modulo hardening-hosting: sshd su una porta diversa dalla 22 -> recidive banna quella porta"
mkdir -p /run/sshd /etc/ssh/sshd_config.d; ssh-keygen -A >/dev/null 2>&1
grep -q '^Include' /etc/ssh/sshd_config || sed -i '1i Include /etc/ssh/sshd_config.d/*.conf' /etc/ssh/sshd_config
printf 'Port 2222\n' >/etc/ssh/sshd_config.d/00-port.conf
apply --module hardening-hosting --check
[ "$RC" = 3 ] && ok || ko "recidive: --check con sshd su 2222: uscita $RC (attesa 3, il file ha ancora port = ssh)"
apply --module hardening-hosting
[ "$RC" = 0 ] && ok || ko "recidive con sshd su 2222: uscita $RC: $(tail -4 /tmp/out | tr '\n' ' ')"
check "recidive: port = 2222" grep -qx 'port = 2222' /etc/fail2ban/jail.d/60-pmgr-recidive.local
apply --module hardening-hosting
[ "$RC" = 0 ] && grep -q "Niente da fare" /tmp/out && ok || ko "recidive con sshd su 2222: seconda esecuzione non idempotente (uscita $RC)"
rm -f /etc/ssh/sshd_config.d/00-port.conf
apply --module hardening-hosting
[ "$RC" = 0 ] && ok || ko "recidive: ritorno alla 22: uscita $RC"
check "recidive: tornata la porta ssh" grep -qx 'port = ssh' /etc/fail2ban/jail.d/60-pmgr-recidive.local
echo "-- modulo hardening-hosting: configurazione di fail2ban rotta -> ripristino"
rm -f /etc/sysctl.d/90-pmgr-hosting.conf /etc/fail2ban/jail.d/60-pmgr-recidive.local
printf '[sshd\n' >/etc/fail2ban/jail.d/99-rotto.local
apply --module hardening-hosting
[ "$RC" = 1 ] && ok || ko "hardening-hosting con fail2ban rotto: uscita $RC (attesa 1)"
check "ripristino: nessun file nostro rimasto" bash -c '[ ! -e /etc/sysctl.d/90-pmgr-hosting.conf ] && [ ! -e /etc/fail2ban/jail.d/60-pmgr-recidive.local ]'
rm -f /etc/fail2ban/jail.d/99-rotto.local
apply --module hardening-hosting
[ "$RC" = 0 ] && ok || ko "hardening-hosting dopo la riparazione: uscita $RC"
echo "-- --verify profilo hosting dopo ssh-hosting"
apply --module ssh-hosting
apply --verify --profile hosting
# in questo container non c'e' firewall (e su Rocky nemmeno SELinux): --verify deve fallire SOLO su quelle righe,
# mentre SSH, fail2ban e aggiornamenti devono risultare conformi (il firewall ha i suoi test con traffico vero)
[ "$RC" = 1 ] && ok || ko "--verify hosting senza firewall: uscita $RC (attesa 1)"
if grep FALLITO /tmp/out | grep -vqE 'firewall|SELinux'; then ko "--verify hosting: falliscono anche righe che non riguardano il firewall: $(grep FALLITO /tmp/out | grep -vE 'firewall|SELinux' | head -3 | tr '\n' ' ')"; else ok; fi
grep -q 'FALLITO.*firewall' /tmp/out && ok || ko "--verify hosting non segnala la mancanza del firewall"
grep -q 'OK.*sshd effettivo: clientaliveinterval 300' /tmp/out && grep -q 'OK.*modulo hardening-hosting' /tmp/out && ok || ko "--verify hosting: manca ClientAlive o hardening-hosting conforme"
grep -q 'OK.*sshd effettivo: passwordauthentication no' /tmp/out && ok || ko "--verify hosting: manca la riga sulla password"
apply --verify --profile standard
[ "$RC" = 1 ] && grep -q 'configurazione di hosting\|VM di hosting' /tmp/out && ok || ko "--verify standard su VM di hosting: dovrebbe segnalare la configurazione di hosting (uscita $RC): $(grep FALLITO /tmp/out | head -2 | tr '\n' ' ')"
apply --module ssh-standard
[ "$RC" = 1 ] && grep -q "VM di hosting" /tmp/out && ok || ko "ssh-standard su una VM di hosting deve rifiutare (uscita $RC)"
check "ssh-standard rifiutato: la password resta disattivata" bash -c "sshd -T -h $K/k 2>/dev/null | grep -qx 'passwordauthentication no'"
echo "-- baseline e argomenti"
apply --module baseline --check
[ "$RC" = 0 ] && ok || ko "--module baseline --check: uscita $RC"
apply --module all; [ "$RC" = 2 ] && ok || ko "'all' accettato: uscita $RC"
apply --module bogus; [ "$RC" = 2 ] && ok || ko "modulo sconosciuto: uscita $RC (attesa 2)"
echo "   RISULTATO $WANT: $PASS ok, $FAIL falliti"
[ "$FAIL" = 0 ]
