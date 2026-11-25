#!/bin/bash
# Prove offline in Debian 13: repository montato in sola lettura, nodo interamente finto.
# --inner e' l'ingresso usato dal container; non esegue comandi sul nodo reale.
set -Eeuo pipefail
export LC_ALL=C
umask 022
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd -- "$HERE/../../.." && pwd)
if [ "$#" = 0 ]; then
  if podman run --rm --pull=never --network none --security-opt label=disable \
    --mount "type=bind,src=$REPO,dst=/repo,ro" docker.io/library/debian:13 \
    bash /repo/tools/pmgr-scripts/tests/test_node_prepare.sh --inner; then
    exit 0
  fi
  printf 'FALLITO: container Debian 13 non eseguibile o test falliti\n'
  printf 'Test node prepare: 0 riusciti, 1 falliti\n'
  exit 1
fi
[ "$#" = 1 ] && [ "$1" = --inner ] || exit 2
WORK=$(mktemp -d /tmp/pmgr-node-prepare-test.XXXXXX)
trap 'rm -rf -- "$WORK"' EXIT
SCRIPT="$REPO/tools/pmgr-scripts/bin/pmgr-node-prepare.sh"
PASS=0 FAIL=0 RC=0
trap 'printf "FALLITO: errore inatteso alla riga %s\n" "$LINENO"; printf "Test node prepare: %d riusciti, %d falliti\n" "$PASS" "$((FAIL + 1))"; exit 1' ERR
check() {
  local description="$1"
  shift
  if "$@"; then PASS=$((PASS + 1)); else
    FAIL=$((FAIL + 1))
    printf 'FALLITO: %s\n' "$description"
    cat "$WORK/out" 2>/dev/null || true
  fi
}
run() {
  RC=0
  bash "$SCRIPT" "$@" >"$WORK/out" 2>&1 || RC=$?
}
snapshot() {
  (
    cd -- "$PMGR_ROOT"
    find . -printf '%y %m %U:%G %i %T@ %p %l\n'
    find . -type f -exec sha256sum -- '{}' +
  ) | sort
}
unchanged() { snapshot >"$WORK/after"; cmp -s "$WORK/before" "$WORK/after"; }
fresh() {
  export PMGR_ROOT="$WORK/node-$1" PMGR_TEST=1
  mkdir -p "$PMGR_ROOT/etc/apt/sources.list.d" "$PMGR_ROOT/etc/apt/apt.conf.d" \
    "$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit" "$PMGR_ROOT/boot" "$PMGR_ROOT/run"
  printf 'VERSION_CODENAME=trixie\n' >"$PMGR_ROOT/etc/os-release"
  printf 'pve-manager/9.1.4/abcdef (running kernel: 6.17.2-1-pve)\n' >"$PMGR_ROOT/test-version"
  printf '6.17.2-1-pve\n' >"$PMGR_ROOT/test-kernel"
  cat >"$PMGR_ROOT/etc/apt/sources.list.d/debian.sources" <<'EOF'
Types: deb
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: http://security.debian.org/debian-security
Suites: trixie-security
Components: main non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
EOF
  cat >"$PMGR_ROOT/etc/apt/sources.list.d/pve-enterprise.sources" <<'EOF'
Types: deb
URIs: https://enterprise.proxmox.com/debian/pve
Suites: trixie
Components: pve-enterprise
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
EOF
  cat >"$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources" <<'EOF'
Types: deb
URIs: https://enterprise.proxmox.com/debian/ceph-squid
Suites: trixie
Components: enterprise
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
Enabled: yes
EOF
  printf '# sorgenti precedenti\n  deb http://deb.debian.org/debian trixie main\ndeb-src http://deb.debian.org/debian trixie main\n' >"$PMGR_ROOT/etc/apt/sources.list"
  printf '# commento\ndeb http://download.proxmox.com/debian/pve trixie pve-no-subscription\n' >"$PMGR_ROOT/etc/apt/sources.list.d/old.list"
  cat >"$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js" <<'EOF'
// fixture Web UI
if (res === null || res === undefined || !res || res.data.status.toLowerCase() !== 'active') {
    Ext.Msg.show({ title: 'Subscription' });
}
EOF
}

mkdir "$WORK/bin"
cat >"$WORK/bin/pveversion" <<'EOF'
#!/bin/bash
cat "$PMGR_ROOT/test-version"
EOF
cat >"$WORK/bin/apt-get" <<'EOF'
#!/bin/bash
printf 'frontend=%s' "${DEBIAN_FRONTEND:-}" >>"$PMGR_ROOT/test-apt.log"
printf ' <%s>' "$@" >>"$PMGR_ROOT/test-apt.log"
printf '\n' >>"$PMGR_ROOT/test-apt.log"
[ ! -f "$PMGR_ROOT/test-apt-fail" ] || exit 100
if [ "$1" = -y ] && [ -f "$PMGR_ROOT/test-upgrade-js" ]; then
  cp "$PMGR_ROOT/test-upgrade-js" "$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js"
fi
EOF
cat >"$WORK/bin/uname" <<'EOF'
#!/bin/bash
[ "$#" = 1 ] && [ "$1" = -r ] || exit 1
cat "$PMGR_ROOT/test-kernel"
EOF
cat >"$WORK/bin/logger" <<'EOF'
#!/bin/bash
printf '<%s> ' "$@" >>"$PMGR_ROOT/test-logger.log"
printf '\n' >>"$PMGR_ROOT/test-logger.log"
# Anche un logger non disponibile non deve impedire l'applicazione.
exit 1
EOF
cat >"$WORK/bin/reboot" <<'EOF'
#!/bin/bash
touch "$PMGR_ROOT/test-reboot-called"
exit 1
EOF
cp "$WORK/bin/reboot" "$WORK/bin/shutdown"
cp "$WORK/bin/reboot" "$WORK/bin/systemctl"
cat >"$WORK/bin/id" <<'EOF'
#!/bin/bash
if [ "$*" = -u ] && [ -f "$PMGR_ROOT/test-uid" ]; then cat "$PMGR_ROOT/test-uid"; else /usr/bin/id "$@"; fi
EOF
chmod 755 "$WORK"/bin/*
export PATH="$WORK/bin:$PATH"

fresh basic
snapshot >"$WORK/before"
run
check 'anteprima: uscita 3' test "$RC" = 3
check 'anteprima: nessun file o metadato modificato' unchanged
check 'anteprima: nessuna chiamata apt' test ! -e "$PMGR_ROOT/test-apt.log"
cp -a "$PMGR_ROOT/etc" "$WORK/original-etc"
cp "$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js" "$WORK/original.js"
run --apply
check 'apply: uscita 0' test "$RC" = 0
cat >"$WORK/expected-debian" <<'EOF'
Types: deb
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: http://security.debian.org/debian-security
Suites: trixie-security
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
EOF
cat >"$WORK/expected-proxmox" <<'EOF'
Types: deb
URIs: http://download.proxmox.com/debian/pve
Suites: trixie
Components: pve-no-subscription
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
EOF
check 'Debian esatto' cmp -s "$WORK/expected-debian" "$PMGR_ROOT/etc/apt/sources.list.d/debian.sources"
check 'Proxmox esatto' cmp -s "$WORK/expected-proxmox" "$PMGR_ROOT/etc/apt/sources.list.d/proxmox.sources"
for name in pve-enterprise ceph; do
  file="$PMGR_ROOT/etc/apt/sources.list.d/$name.sources"
  check "$name: file conservato ed enterprise disabilitato" grep -qx 'Enabled: no' "$file"
done
check 'nessun pve-test aggiunto' test ! -e "$PMGR_ROOT/etc/apt/sources.list.d/pve-test.sources"
check 'Ceph none: nessun no-subscription aggiunto' test "$(grep -c 'no-subscription' "$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources" || true)" = 0
for file in "$PMGR_ROOT/etc/apt/sources.list" "$PMGR_ROOT/etc/apt/sources.list.d/old.list"; do
  check 'legacy: tutte le righe attive commentate' test "$(grep -Ec '^[[:space:]]*deb(-src)?[[:space:]]' "$file" || true)" = 0
done
backup_dirs=("$PMGR_ROOT/root/pmgr-backup/"node-prepare-*)
backup="${backup_dirs[0]}"
check 'backup creato con nome previsto' test -d "$backup"
check 'backup privato root 0700' test "$(stat -c '%u:%g:%a' "$backup")" = 0:0:700
for relative in etc/apt/sources.list etc/apt/sources.list.d/debian.sources etc/apt/sources.list.d/pve-enterprise.sources etc/apt/sources.list.d/ceph.sources etc/apt/sources.list.d/old.list; do
  check "backup fedele: $relative" cmp -s "$backup/$relative" "$WORK/original-etc/${relative#etc/}"
done
check 'backup del JavaScript originale' cmp -s "$backup/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js" "$WORK/original.js"
nag="$PMGR_ROOT/usr/local/libexec/pmgr/pmgr-remove-nag.sh"
hook="$PMGR_ROOT/etc/apt/apt.conf.d/90pmgr-no-nag"
js="$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js"
check 'helper root 0755' test "$(stat -c '%u:%g:%a' "$nag")" = 0:0:755
check 'hook root 0644' test "$(stat -c '%u:%g:%a' "$hook")" = 0:0:644
printf '%s\n' 'DPkg::Post-Invoke { "/usr/local/libexec/pmgr/pmgr-remove-nag.sh || true"; };' >"$WORK/expected-hook"
check 'hook esatto' cmp -s "$WORK/expected-hook" "$hook"
sed -e '/data\.status/ s/!//' -e '/data\.status/ s/active/NoMoreNagging/' "$WORK/original.js" >"$WORK/expected-js"
check 'patch identica al community script' cmp -s "$WORK/expected-js" "$js"
check 'una sola patch' test "$(grep -c NoMoreNagging "$js")" = 1
check 'log per modifica' grep -q 'PMGR-NODE-PREPARE' "$PMGR_ROOT/test-logger.log"
snapshot >"$WORK/before"
run --apply
check 'secondo apply: uscita 0' test "$RC" = 0
check 'secondo apply: identico anche per inode e mtime' unchanged
run
check 'anteprima conforme: uscita 0' test "$RC" = 0
bash "$nag"
check 'helper idempotente' unchanged
run --apply --keep-nag
check 'keep-nag: uscita 0' test "$RC" = 0
check 'keep-nag: conserva tutti i file' unchanged
check 'keep-nag: segnala file presenti' grep -q 'mantengo /etc/apt/apt.conf.d/90pmgr-no-nag' "$WORK/out"

run --apply --ceph squid
check 'Ceph squid: uscita 0' test "$RC" = 0
cat >"$WORK/expected-ceph" <<'EOF'
Types: deb
URIs: http://download.proxmox.com/debian/ceph-squid
Suites: trixie
Components: enterprise
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
Enabled: no

Types: deb
URIs: http://download.proxmox.com/debian/ceph-squid
Suites: trixie
Components: no-subscription
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
EOF
# L'URI enterprise deve restare quella originale.
sed -i '0,/http:\/\/download.proxmox.com/s//https:\/\/enterprise.proxmox.com/' "$WORK/expected-ceph"
check 'Ceph: enterprise conservato, un solo stanza abilitato canonico' cmp -s "$WORK/expected-ceph" "$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources"
snapshot >"$WORK/before"
run --apply --ceph squid
check 'Ceph squid idempotente: uscita 0' test "$RC" = 0
check 'Ceph squid idempotente: nessuna scrittura' unchanged
run --apply --ceph tentacle
check 'rifiuto cambio Ceph: uscita 1' test "$RC" = 1
check 'rifiuto cambio Ceph: messaggio' grep -qF "il nodo usa gia' ceph-squid: non cambio release di Ceph" "$WORK/out"
check 'rifiuto cambio Ceph: nessuna scrittura' unchanged

fresh tentacle
run --apply --ceph tentacle --keep-nag
check 'tentacle richiesto esplicitamente: uscita 0' test "$RC" = 0
check 'tentacle: URI corretta' grep -qx 'URIs: http://download.proxmox.com/debian/ceph-tentacle' "$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources"
check 'keep-nag su nodo nuovo: helper assente' test ! -e "$PMGR_ROOT/usr/local/libexec/pmgr/pmgr-remove-nag.sh"
check 'keep-nag su nodo nuovo: hook assente' test ! -e "$PMGR_ROOT/etc/apt/apt.conf.d/90pmgr-no-nag"

fresh absent-ceph
rm "$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources"
run --apply --keep-nag
check 'Ceph none senza file: uscita 0' test "$RC" = 0
check 'Ceph none senza file: non crea ceph.sources' test ! -e "$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources"

fresh disabled-ceph
cat >>"$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources" <<'EOF'

Types: deb
URIs:
 http://download.proxmox.com/debian/ceph-squid
Suites: trixie
Components: no-subscription
Enabled: no
EOF
run --apply --ceph tentacle --keep-nag
check 'Ceph diverso ma disabilitato: consentito' test "$RC" = 0
check 'Ceph diverso disabilitato: conservato' grep -q '^ http://download.proxmox.com/debian/ceph-squid$' "$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources"

fresh diff
snapshot >"$WORK/before"
run
check 'anteprima: differenza mostrata' grep -qF -- '--- attuale /etc/apt/sources.list.d/debian.sources' "$WORK/out"
check 'anteprima: riga nuova visibile' grep -qF -- '+Components: main contrib non-free-firmware' "$WORK/out"
check 'anteprima con differenza: nessuna scrittura' unchanged

fresh nosub
cat >"$PMGR_ROOT/etc/apt/sources.list.d/pve-install-repo.sources" <<'EOF'
Types: deb
URIs: http://download.proxmox.com/debian/pve
Suites: trixie
Components: pve-no-subscription
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
EOF
run --apply --keep-nag
check 'no-subscription gia presente: uscita 0' test "$RC" = 0
check 'no-subscription gia presente: nessun doppione' test ! -e "$PMGR_ROOT/etc/apt/sources.list.d/proxmox.sources"
check 'no-subscription gia presente: segnalato' grep -qF 'pve-no-subscription gia attivo in /etc/apt/sources.list.d/pve-install-repo.sources' "$WORK/out"
snapshot >"$WORK/before"
run --apply --keep-nag
check 'no-subscription gia presente: idempotente' unchanged
sed -i 's/^Components: pve-no-subscription$/Components: pve-no-subscription\nEnabled: no/' "$PMGR_ROOT/etc/apt/sources.list.d/pve-install-repo.sources"
run --apply --keep-nag
check 'no-subscription disabilitato altrove: crea proxmox.sources' test -f "$PMGR_ROOT/etc/apt/sources.list.d/proxmox.sources"

fresh community-nag
printf 'DPkg::Post-Invoke { "/usr/local/bin/pve-remove-nag.sh"; };\n' >"$PMGR_ROOT/etc/apt/apt.conf.d/no-nag-script"
run --apply
check 'aggancio della community: uscita 0' test "$RC" = 0
check 'aggancio della community: il mio helper non installato' test ! -e "$PMGR_ROOT/usr/local/libexec/pmgr/pmgr-remove-nag.sh"
check 'aggancio della community: il mio hook non installato' test ! -e "$PMGR_ROOT/etc/apt/apt.conf.d/90pmgr-no-nag"
check 'aggancio della community: segnalato' grep -qF '(/etc/apt/apt.conf.d/no-nag-script)' "$WORK/out"
check 'aggancio della community: avviso comunque tolto' grep -q NoMoreNagging "$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js"
snapshot >"$WORK/before"
run --apply
check 'aggancio della community: idempotente' unchanged

fresh continued-ceph
cat >>"$PMGR_ROOT/etc/apt/sources.list.d/ceph.sources" <<'EOF'

Types: deb
URIs:
 http://download.proxmox.com/debian/ceph-squid
Suites: trixie
Components:
 no-subscription
EOF
snapshot >"$WORK/before"
run --apply --ceph tentacle
check 'Ceph con campi continuati: rifiuto' test "$RC" = 1
check 'Ceph con campi continuati: nessuna scrittura' unchanged

for scenario in codename version root symlink bad-js; do
  fresh "$scenario"
  case "$scenario" in
    codename) printf 'VERSION_CODENAME=bookworm\n' >"$PMGR_ROOT/etc/os-release" ;;
    version) printf 'pve-manager/8.4.1/abcdef\n' >"$PMGR_ROOT/test-version" ;;
    root) printf '1000\n' >"$PMGR_ROOT/test-uid"; export PMGR_TEST=0 ;;
    symlink) ln -s "$WORK" "$PMGR_ROOT/usr/local" ;;
    bad-js) printf 'format changed\n' >"$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js" ;;
  esac
  snapshot >"$WORK/before"
  run --apply
  check "$scenario: rifiuto con uscita 1" test "$RC" = 1
  check "$scenario: nessuna scrittura" unchanged
done
fresh arguments
bad_options() {
  run "$@"
  check "argomenti rifiutati: $*" test "$RC" = 2
  check "usage presente: $*" grep -q '^Uso:' "$WORK/out"
}
snapshot >"$WORK/before"
bad_options --ceph quincy
bad_options --ceph
bad_options --foo
bad_options --apply --apply
bad_options --ceph squid --ceph squid
bad_options --keep-nag --keep-nag
bad_options --apply --upgrade --upgrade
bad_options --upgrade
bad_options --ceph 'squid;reboot'
check 'argomenti errati: nessuna scrittura' unchanged

fresh stanzas
cat >"$PMGR_ROOT/etc/apt/sources.list.d/mixed.sources" <<'EOF'
# enterprise con URI continuata
Types: deb
URIs:
 https://enterprise.proxmox.com/debian/pve
Suites: trixie
Components: pve-enterprise
Enabled:
 yes

# stanza estranea intatta
Types: deb
URIs: https://example.org/debian
Suites: trixie
Components: main
Enabled: yes
EOF
sed -e '/^Enabled:$/c\Enabled: no' -e '/^ yes$/d' "$PMGR_ROOT/etc/apt/sources.list.d/mixed.sources" >"$WORK/expected-mixed"
printf '\n\n' >>"$PMGR_ROOT/etc/apt/sources.list.d/mixed.sources"
printf '\n\n' >>"$WORK/expected-mixed"
printf '# sorgente estranea senza newline finale\nTypes: deb\nURIs: https://example.org/debian\nSuites: trixie\nComponents: main' >"$PMGR_ROOT/etc/apt/sources.list.d/foreign.sources"
cp "$PMGR_ROOT/etc/apt/sources.list.d/foreign.sources" "$WORK/expected-foreign"
run --apply
check 'deb822: continuazioni e piu\047 stanzas' test "$RC" = 0
check 'deb822: cambia solo lo stanza enterprise' cmp -s "$WORK/expected-mixed" "$PMGR_ROOT/etc/apt/sources.list.d/mixed.sources"
check 'deb822: stanza estraneo conservato byte per byte' cmp -s "$WORK/expected-foreign" "$PMGR_ROOT/etc/apt/sources.list.d/foreign.sources"

fresh upgrade
cp "$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js" "$PMGR_ROOT/test-upgrade-js"
touch "$PMGR_ROOT/boot/vmlinuz-6.17.2-1-pve" "$PMGR_ROOT/boot/vmlinuz-6.17.10-1-pve" "$PMGR_ROOT/boot/vmlinuz-99-generic"
run --apply --upgrade
check 'upgrade: uscita 0' test "$RC" = 0
cat >"$WORK/expected-apt" <<'EOF'
frontend= <update>
frontend=noninteractive <-y> <-o> <Dpkg::Options::=--force-confdef> <-o> <Dpkg::Options::=--force-confold> <dist-upgrade>
EOF
check 'upgrade: argomenti esatti, ordine e frontend' cmp -s "$WORK/expected-apt" "$PMGR_ROOT/test-apt.log"
check 'upgrade: nessun reboot o systemctl' test ! -e "$PMGR_ROOT/test-reboot-called"
check 'upgrade: patch riapplicata dopo aggiornamento toolkit' grep -q NoMoreNagging "$PMGR_ROOT/usr/share/javascript/proxmox-widget-toolkit/proxmoxlib.js"
check 'upgrade: kernel piu\047 recente, ordinamento numerico' grep -q 'Riavvio necessario: si' "$WORK/out"
printf '6.17.10-1-pve\n' >"$PMGR_ROOT/test-kernel"
run --apply
check 'kernel corrente: nessun riavvio' grep -q 'Riavvio necessario: no' "$WORK/out"
touch "$PMGR_ROOT/run/reboot-required"
run --apply
check 'flag reboot-required: riavvio segnalato' grep -q 'Riavvio necessario: si' "$WORK/out"
touch "$PMGR_ROOT/test-apt-fail"
run --apply --upgrade
check 'apt fallito: uscita 1' test "$RC" = 1

check 'sintassi Bash' bash -n "$SCRIPT"
check 'sintassi helper' bash -n "$PMGR_ROOT/usr/local/libexec/pmgr/pmgr-remove-nag.sh"
non_ascii=0
LC_ALL=C grep -qP '[^\x00-\x7f]' "$SCRIPT" && non_ascii=1
check 'script solo ASCII' test "$non_ascii" = 0
printf 'Test node prepare: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
