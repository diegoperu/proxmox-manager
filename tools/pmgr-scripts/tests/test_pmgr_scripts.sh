#!/bin/bash
# Test degli script dei template con qm/pvesm/curl/qemu-img FINTI: nessun contatto con Proxmox ne' con la rete.
# Gira in un user namespace (unshare -r) per apparire "root" senza esserlo. Solo ASCII.
set -uo pipefail

if [ "$(id -u)" != 0 ]; then
  command -v unshare >/dev/null 2>&1 || { echo "serve unshare (util-linux) o root"; exit 1; }
  exec unshare -r bash "${BASH_SOURCE[0]}" "$@"
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
check() { # descrizione comando...
  local d="$1"
  shift
  if "$@" >/dev/null 2>&1; then ok; else ko "$d"; fi
}

# ------------------------------------------------------------------ ambiente finto
TREE="$WORK/tree"
mkdir -p "$TREE" "$WORK/bin" "$WORK/state" "$WORK/snippets" "$WORK/images"
cp -r "$SRC/bin" "$SRC/lib" "$SRC/os.d" "$SRC/snippets.d" "$SRC/helpers" "$SRC/profiles" "$TREE/"
cp "$SRC"/update*.sh "$TREE/"

cat >"$WORK/bin/qm" <<'STUB'
#!/bin/bash
echo "qm $*" >>"$STUB_LOG"
cmd="$1"; shift
[ "${STUB_FAIL:-}" = "$cmd" ] && { echo "qm: errore simulato in $cmd" >&2; exit 1; }
cfg() { echo "$STUB_STATE/vm-$1.cfg"; }
case "$cmd" in
  config) [ -f "$(cfg "$1")" ] && cat "$(cfg "$1")" || { echo "Configuration file does not exist" >&2; exit 2; } ;;
  destroy) rm -f "$(cfg "$1")" ;;
  create)
    v="$1"; shift; name=""; efi=""
    while [ "$#" -gt 0 ]; do
      [ "$1" = --name ] && name="$2"
      [ "$1" = --efidisk0 ] && efi="$2"
      shift
    done
    printf 'name: %s\nefidisk0: %s\n' "$name" "$efi" >"$(cfg "$v")" ;;
  importdisk) echo "unused0: ${STUB_POOL:-pool}:vm-$1-disk-1" >>"$(cfg "$1")" ;;
  set)
    v="$1"; shift; echo "set: $*" >>"$(cfg "$v")"
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --virtio0 | --ide2 | --tags | --ciuser | --cicustom | --nameserver | --searchdomain) echo "${1#--}: $2" >>"$(cfg "$v")"; shift ;;
      esac
      shift
    done ;;
  template) echo "template: 1" >>"$(cfg "$1")" ;;
esac
STUB
cat >"$WORK/bin/pvesm" <<'STUB'
#!/bin/bash
# pvesm status --content X : elenco da STUB_PVESM_<X> ("nome:stato nome:stato")
echo "pvesm $*" >>"$STUB_LOG"
content=""
while [ "$#" -gt 0 ]; do [ "$1" = --content ] && content="$2"; shift; done
var="STUB_PVESM_$content"
echo "Name Type Status Total Used Available %"
for e in ${!var}; do echo "${e%%:*} dir ${e##*:} 100 10 90 10%"; done
STUB
cat >"$WORK/bin/qemu-img" <<'STUB'
#!/bin/bash
echo "qemu-img $*" >>"$STUB_LOG"
case "$1" in
  info) f="${@: -1}"; if head -c 4 "$f" | grep -q QCOW; then echo "file format: qcow2"; else echo "file format: raw"; fi ;;
  resize) echo "(resized)" ;;
esac
STUB
cat >"$WORK/bin/curl" <<'STUB'
#!/bin/bash
# curl finto: -o FILE -- URL ; con -I solo "esiste?"
echo "curl $*" >>"$STUB_LOG"
out=""; head=0; url="${@: -1}"
prev=""
for a in "$@"; do
  [ "$prev" = -o ] && out="$a"
  case "$a" in -*I*) [[ "$a" == -* && "$a" != --* ]] && head=1 ;; esac
  prev="$a"
done
[ "$head" = 1 ] && { [[ "$url" == *missing* ]] && exit 22 || exit 0; }
[ -n "${STUB_CURL_FAIL_MATCH:-}" ] && [[ "$url" == *"$STUB_CURL_FAIL_MATCH"* ]] && exit 22
[ "${STUB_CURL_FAIL:-0}" = 1 ] && exit 22
case "$url" in
  *SHA256SUMS | *SHA512SUMS) cp "$STUB_CKFILE" "$out" ;;
  *) truncate -s 60M "$out"; printf '%s' "${STUB_IMG_MAGIC:-QCOW}" | dd of="$out" conv=notrunc 2>/dev/null ;;
esac
STUB
printf '#!/bin/bash\nexit 0\n' >"$WORK/bin/logger"
chmod 755 "$WORK"/bin/*
export PATH="$WORK/bin:$PATH"
export STUB_LOG="$WORK/stub.log" STUB_STATE="$WORK/state"
export STUB_PVESM_images="pool:active cipool:active" STUB_PVESM_snippets="local:active"
export PMGR_LOCK_FILE="$WORK/lock"
export PMGR_CLUSTER_CONF="$WORK/cluster.conf"

PW='S3cretPassw0rd!'
printf '%s\n' "$PW" >"$WORK/pw"; chmod 600 "$WORK/pw"
printf 'ssh-ed25519 AAAA test\n' >"$WORK/keys.pub"
write_cluster() { # write_cluster [CHIAVE=valore ...]: scrive la base, sostituendo (o aggiungendo) le chiavi indicate
  cat >"$PMGR_CLUSTER_CONF" <<EOF
VMID_BASE=1000
STORAGE=pool
BRIDGE=lo
NAMESERVER=10.0.0.1 10.0.0.2
SEARCH_DOMAIN=test.local
CI_USER=tecnico
SSH_KEYS_FILE=$WORK/keys.pub
PASSWORD_FILE=$WORK/pw
SNIPPETS_DIR=$WORK/snippets
IMAGES_DIR=$WORK/images
EOF
  local kv key
  for kv in "$@"; do
    key="${kv%%=*}"
    if [[ "$kv" == *=* ]] && grep -q "^${key}=" "$PMGR_CLUSTER_CONF"; then
      grep -v "^${key}=" "$PMGR_CLUSTER_CONF" >"$PMGR_CLUSTER_CONF.new"
      printf '%s\n' "$kv" >>"$PMGR_CLUSTER_CONF.new"
      mv "$PMGR_CLUSTER_CONF.new" "$PMGR_CLUSTER_CONF"
    else
      printf '%s\n' "$kv" >>"$PMGR_CLUSTER_CONF"
    fi
  done
  chmod 600 "$PMGR_CLUSTER_CONF"
}
write_cluster

# checksum atteso dell'immagine finta (stessa ricetta dello stub)
truncate -s 60M "$WORK/ref.img"; printf 'QCOW' | dd of="$WORK/ref.img" conv=notrunc 2>/dev/null
HASH256="$(sha256sum "$WORK/ref.img" | cut -d' ' -f1)"
HASH512="$(sha512sum "$WORK/ref.img" | cut -d' ' -f1)"
export STUB_CKFILE="$WORK/ck"

OS="$TREE/bin/pmgr-os.sh"
run() { # esegue pmgr-os.sh; out in $WORK/out, codice in RC
  : >"$STUB_LOG"
  "$OS" "$@" >"$WORK/out" 2>&1
  RC=$?
}
logged() { grep -q -- "$1" "$STUB_LOG"; }
no_dangerous_calls() { ! grep -qE '^qm (destroy|create|importdisk|set|template)' "$STUB_LOG"; }
reset_state() { rm -rf "$WORK/state" "$WORK/images" "$WORK/snippets"; mkdir -p "$WORK/state" "$WORK/images" "$WORK/snippets"; }
expect_die() { # descrizione, testo atteso nell'errore
  if [ "$RC" != 0 ] && grep -q -- "$2" "$WORK/out"; then ok; else ko "$1 (uscita $RC): $(head -c 300 "$WORK/out" | tr '\n' ' ')"; fi
}

# ------------------------------------------------------------------ 1. lettura della configurazione
printf 'VMID_BASE=1000\n' >"$WORK/bad.conf"
for t in \
  'chiave sconosciuta|ALTRO=1' \
  'valore con codice|STORAGE=$(id)' \
  'valore con punto e virgola|STORAGE=a;b' \
  'percorso con ..|SNIPPETS_DIR=/var/lib/../etc' \
  'percorso relativo|IMAGES_DIR=relativo/dir' \
  'DNS non valido|NAMESERVER=10.0.0.999' \
  'DNS non IPv4|NAMESERVER=dns.example.org' \
  'bridge troppo lungo|BRIDGE=abcdefghijklmnopq' \
  'utente con maiuscole|CI_USER=Tecnico' \
  'dimensione disco senza G|DISK_SIZE=32' \
  'riga senza uguale|STORAGE pool'; do
  write_cluster "${t#*|}"
  run --list
  expect_die "configurazione rifiutata: ${t%%|*}" "non valido\|validi\|sconosciuta\|contiene"
done
write_cluster
printf 'STORAGE=altro\n' >>"$PMGR_CLUSTER_CONF"
run --list
expect_die "chiave ripetuta" "ripetuta"
printf 'VMID_BASE=1000\r\nSTORAGE=pool\r\n' >"$PMGR_CLUSTER_CONF"
run --list
expect_die "fine riga Windows rifiutata" "non valido"
printf 'VMID_BASE=1000\nSTORAGE=pool\n' >"$PMGR_CLUSTER_CONF"
run --list
expect_die "configurazione incompleta: si ferma" "manca"
rm -f "$PMGR_CLUSTER_CONF"
run --list
expect_die "configurazione assente" "mancante"
ln -s "$WORK/bad.conf" "$PMGR_CLUSTER_CONF"
run --list
expect_die "configurazione come link simbolico" "mancante"
rm -f "$PMGR_CLUSTER_CONF"
write_cluster

# l'esempio fornito deve essere valido (sostituendo solo i percorsi dei segreti)
sed -e "s|/etc/pmgr/ssh_keys.pub|$WORK/keys.pub|" -e "s|/etc/pmgr/template-password|$WORK/pw|" "$SRC/cluster.conf.example" >"$WORK/example.conf"
PMGR_CLUSTER_CONF="$WORK/example.conf" run --list
if [ "$RC" = 0 ]; then ok; else ko "cluster.conf.example non valido: $(head -c 200 "$WORK/out")"; fi

run --list
if [ "$RC" = 0 ] && grep -q '^ubuntu-22.04 *VMID 1001' "$WORK/out" && grep -q '^ubuntu-26.04 *VMID 1007' "$WORK/out" && grep -q '^rocky-10 *VMID 1008' "$WORK/out" && [ "$(wc -l <"$WORK/out")" = 8 ]; then ok; else ko "--list: $(cat "$WORK/out")"; fi

# ------------------------------------------------------------------ 2. coerenza di os.d
for f in "$SRC"/os.d/*.conf; do
  id="$(basename "$f" .conf)"
  check "$id: manca il corpo dello snippet" test -s "$SRC/snippets.d/$id.yaml"
done
check "snippet con nomi unici" bash -c "[ \"\$(sed -n 's/^SNIPPET=//p' $SRC/os.d/*.conf | sort | uniq -d | wc -l)\" = 0 ]"
check "ubuntu-26.04 usa il proprio snippet" grep -q '^SNIPPET=vendor_ubuntu_26.04.yaml$' "$SRC/os.d/ubuntu-26.04.conf"
for f in "$SRC"/helpers/*.sh "$SRC"/snippets.d/*.yaml; do
  check "ASCII: $(basename "$f")" bash -c "! LC_ALL=C grep -qP '[^\\x09\\x0a\\x20-\\x7e]' '$f'"
done

# ------------------------------------------------------------------ 3. dry-run: non scrive niente
reset_state
run --all --dry-run
if [ "$RC" = 0 ]; then ok; else ko "dry-run --all (uscita $RC): $(tail -5 "$WORK/out" | tr '\n' ' ')"; fi
check "dry-run: nessuna chiamata distruttiva" no_dangerous_calls
check "dry-run: nessun download" bash -c "! grep -q '^curl.* -o ' '$STUB_LOG'"
check "dry-run: nessun file scritto" bash -c "[ -z \"\$(find $WORK/snippets $WORK/images -type f)\" ]"
check "dry-run: password nascosta" grep -q 'cipassword <nascosta>' "$WORK/out"
check "dry-run: la password non compare" bash -c "! grep -qF -- '$PW' '$WORK/out' '$STUB_LOG'"

# ------------------------------------------------------------------ 4. percorso completo (Ubuntu 24.04, con checksum)
printf '%s  %s\n' "$HASH256" "noble-server-cloudimg-amd64.img" >"$STUB_CKFILE"
reset_state
run ubuntu-24.04
if [ "$RC" = 0 ]; then ok; else ko "ubuntu-24.04 completo (uscita $RC): $(tail -6 "$WORK/out" | tr '\n' ' ')"; fi
check "immagine al suo posto" test -f "$WORK/images/ubuntu-24.04/noble-server-cloudimg-amd64.img"
check "nessun file .part rimasto" bash -c "[ -z \"\$(find $WORK/images -name '*.part')\" ]"
check "ridimensionata a 32G" logged "qemu-img resize -f qcow2 -- $WORK/images/ubuntu-24.04/noble-server-cloudimg-amd64.img.part 32G"
check "checksum verificato" grep -q 'checksum sha256 verificato' "$WORK/out"
check "snippet scritto 644" bash -c "[ \"\$(stat -c %a $WORK/snippets/vendor_ubuntu_24.04.yaml)\" = 644 ]"
grep -v '^# pmgr:\(ssh-password\|extra-tools\)-' "$SRC/snippets.d/ubuntu-24.04.yaml" >"$WORK/ubuntu-24.04.body"
# lo snippet = corpo senza marcatori e senza il "reboot" finale, poi i blocchi dei profili, poi "reboot"
head -n -1 "$WORK/ubuntu-24.04.body" >"$WORK/ubuntu-24.04.prefix"
check "snippet inizia con il corpo senza i marcatori (nessun helper per Ubuntu)" bash -c "head -n \$(wc -l < '$WORK/ubuntu-24.04.prefix') '$WORK/snippets/vendor_ubuntu_24.04.yaml' | cmp -s - '$WORK/ubuntu-24.04.prefix'"
check "snippet finisce con reboot" bash -c "[ \"\$(tail -n 1 '$WORK/snippets/vendor_ubuntu_24.04.yaml')\" = '    - reboot' ]"
check "VM creata con VMID 1003" logged "qm create 1003 --name ubuntu-2404-cloudinit-template"
check "efidisk sullo storage del cluster" logged "--efidisk0 pool:0,pre-enrolled-keys=0"
check "bridge del cluster" logged "--net0 virtio,bridge=lo"
check "disco letto da unused0" logged "qm set 1003 --scsihw virtio-scsi-pci --virtio0 pool:vm-1003-disk-1,discard=on,cache=writeback"
check "cloud-init sullo storage del cluster" logged "qm set 1003 --ide2 pool:cloudinit"
check "avvio con il nodo e riavvio dopo crash" logged "qm set 1003 --autostart 1 --onboot 1"
check "snippet referenziato" logged "qm set 1003 --cicustom vendor=local:snippets/vendor_ubuntu_24.04.yaml"
check "DNS del cluster" logged 'qm set 1003 --nameserver 10.0.0.1 10.0.0.2'
check "dominio di ricerca del cluster" logged "qm set 1003 --searchdomain test.local"
check "tag" logged "qm set 1003 --tags ubuntu-template,24.04,cloudinit"
check "diventa template" logged "qm template 1003"
check "password impostata come hash" grep -qF 'qm set 1003 --cipassword $6$' "$STUB_LOG"
check "la password non compare ne' nel log ne' nell'output" bash -c "! grep -qF -- '$PW' '$STUB_LOG' '$WORK/out'"
# seconda esecuzione: template esistente ricreato, snippet invariato
run ubuntu-24.04
if [ "$RC" = 0 ]; then ok; else ko "seconda esecuzione (uscita $RC)"; fi
check "template precedente distrutto e ricreato" bash -c "grep -n '^qm destroy 1003' '$STUB_LOG' | head -1 | grep -q . && grep -q '^qm create 1003' '$STUB_LOG'"
check "snippet invariato alla seconda esecuzione" grep -q 'snippet invariato' "$WORK/out"

# ------------------------------------------------------------------ 5. i guasti NON distruggono il template
failcase() { # descrizione, testo atteso, [env...]  (il template 1003 esiste e deve restare)
  local d="$1" want="$2"
  shift 2
  reset_state
  printf 'name: ubuntu-2404-cloudinit-template\ntemplate: 1\n' >"$WORK/state/vm-1003.cfg"
  printf 'immagine-vecchia' >/dev/null
  mkdir -p "$WORK/images/ubuntu-24.04"
  printf 'vecchia' >"$WORK/images/ubuntu-24.04/noble-server-cloudimg-amd64.img"
  env "$@" "$OS" ubuntu-24.04 >"$WORK/out" 2>&1
  RC=$?
  expect_die "$d" "$want"
  check "$d: template intatto" test -f "$WORK/state/vm-1003.cfg"
  check "$d: nessun qm destroy/create" bash -c "! grep -qE '^qm (destroy|create)' '$STUB_LOG'"
  check "$d: immagine precedente intatta" bash -c "[ \"\$(cat $WORK/images/ubuntu-24.04/noble-server-cloudimg-amd64.img)\" = vecchia ]"
  check "$d: nessun .part" bash -c "[ -z \"\$(find $WORK/images -name '*.part')\" ]"
}
: >"$STUB_LOG"
failcase "download fallito" "download fallito" STUB_CURL_FAIL=1
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "noble-server-cloudimg-amd64.img" >"$STUB_CKFILE"
failcase "checksum sbagliato" "checksum dell'immagine" STUB_CURL_FAIL=0
printf 'riga senza il nostro file\n' >"$STUB_CKFILE"
failcase "checksum non trovato" "non riesco a leggere il checksum" STUB_CURL_FAIL=0
printf '%s  %s\n' "$HASH256" "noble-server-cloudimg-amd64.img" >"$STUB_CKFILE"
failcase "immagine non qcow2" "immagine scaricata non valida" STUB_IMG_MAGIC=XXXX
printf '%s  %s\n' "$HASH256" "noble-server-cloudimg-amd64.img" >"$STUB_CKFILE"

# VMID occupato da una VM che non e' un nostro template
reset_state
printf 'name: database-di-produzione\n' >"$WORK/state/vm-1003.cfg"
run ubuntu-24.04
expect_die "VMID occupato da VM non nostra" "non la tocco"
check "VM non nostra intatta" test -f "$WORK/state/vm-1003.cfg"
check "nessun qm destroy" bash -c "! grep -q '^qm destroy' '$STUB_LOG'"

# guasto dopo la creazione: qm importdisk fallisce -> errore chiaro, nessun template finto
reset_state
run ubuntu-24.04 --step image
run ubuntu-24.04 --step snippet
STUB_FAIL=importdisk run ubuntu-24.04 --step template
if [ "$RC" != 0 ]; then ok; else ko "importdisk fallito deve dare errore"; fi
run ubuntu-24.04 --step finalize
expect_die "finalize rifiutato su VM incompleta" "incompleta"
check "VM incompleta non diventa template" bash -c "! grep -q '^qm template' '$STUB_LOG'"

# ------------------------------------------------------------------ 6. controlli prima di toccare qualunque cosa
pre() { # descrizione, testo atteso, comando di preparazione
  reset_state
  printf '%s  %s\n' "$HASH256" "noble-server-cloudimg-amd64.img" >"$STUB_CKFILE"
  write_cluster
  printf '%s\n' "$PW" >"$WORK/pw"; chmod 600 "$WORK/pw"
  eval "$3"
  run ubuntu-24.04
  expect_die "$1" "$2"
  check "$1: nessuna chiamata distruttiva" no_dangerous_calls
  check "$1: nessun download" bash -c "! grep -q '^curl.* -o ' '$STUB_LOG'"
}
pre "storage dei dischi inesistente" "non esiste, non e' attivo" "write_cluster STORAGE=nope"
pre "storage dei dischi non attivo" "non esiste, non e' attivo" "export STUB_PVESM_images='pool:inactive cipool:active'"
export STUB_PVESM_images="pool:active cipool:active"
pre "storage cloud-init inesistente" "non esiste, non e' attivo" "write_cluster CI_STORAGE=nope"
pre "storage degli snippet senza il contenuto snippets" "non accetta snippets" "export STUB_PVESM_snippets='altro:active'"
export STUB_PVESM_snippets="local:active"
pre "bridge inesistente" "bridge nope0 non esiste" "write_cluster; sed -i 's/^BRIDGE=.*/BRIDGE=nope0/' $PMGR_CLUSTER_CONF"
pre "password con permessi larghi" "chmod 600" "chmod 644 $WORK/pw"
pre "file della password mancante" "file della password non trovato" "rm -f $WORK/pw"
pre "password troppo corta" "8-128" "printf 'corta\n' >$WORK/pw"
pre "password su due righe" "una sola riga" "printf 'unaPasswordLunga1\naltra\n' >$WORK/pw"
pre "chiavi ssh mancanti" "file delle chiavi ssh non leggibile" "rm -f $WORK/keys.pub"
printf 'ssh-ed25519 AAAA test\n' >"$WORK/keys.pub"
pre "cartella degli snippet mancante" "cartella degli snippet non esiste" "rmdir $WORK/snippets"
write_cluster; printf '%s\n' "$PW" >"$WORK/pw"; chmod 600 "$WORK/pw"

# helper non ASCII (rocky): si ferma, nessun file scritto
reset_state
printf '%s\n' "# prova àè" >>"$TREE/helpers/disk.sh"
run rocky-9 --step snippet
expect_die "helper non ASCII rifiutato" "non ASCII"
check "helper non ASCII: nessuno snippet scritto" bash -c "[ -z \"\$(ls -A $WORK/snippets)\" ]"
cp "$SRC/helpers/disk.sh" "$TREE/helpers/disk.sh"

# lock: un secondo aggiornamento insieme al primo e' rifiutato
reset_state
( flock -n 9 && sleep 3 ) 9>"$PMGR_LOCK_FILE" &
sleep 0.5
run ubuntu-24.04 --step snippet
expect_die "aggiornamento gia' in corso" "un altro aggiornamento"
wait

# ------------------------------------------------------------------ 7. snippet di Rocky con gli helper
reset_state
run rocky-9 --step snippet
if [ "$RC" = 0 ]; then ok; else ko "rocky-9 snippet (uscita $RC): $(cat "$WORK/out")"; fi
S="$WORK/snippets/vendor_rocky_9.yaml"
check "Rocky: inizia con #cloud-config e write_files" bash -c "head -3 '$S' | tr '\n' '|' | grep -q '^#cloud-config||write_files:|'"
for h in adduser.sh disk.sh dmesg.sh; do
  want="$(base64 -w0 "$SRC/helpers/$h")"
  check "Rocky: helper $h incorporato in base64" grep -qF "content: $want" "$S"
  check "Rocky: $h in /usr/local/libexec/pmgr con permessi 0750" grep -q "path: /usr/local/libexec/pmgr/$h" "$S"
done
check "Rocky: segue il corpo con runcmd" grep -q '^runcmd:$' "$S"
grep -v '^# pmgr:\(ssh-password\|extra-tools\)-' "$SRC/snippets.d/rocky-9.yaml" >"$WORK/rocky-9.body"
head -n -1 "$WORK/rocky-9.body" >"$WORK/rocky-9.prefix"
check "Rocky: il corpo (senza marcatori) segue gli helper, identico" bash -c "tail -n +\$(grep -n '^runcmd:\$' '$S' | head -1 | cut -d: -f1) '$S' | head -n \$(wc -l < '$WORK/rocky-9.prefix') | cmp -s - '$WORK/rocky-9.prefix'"

# ------------------------------------------------------------------ 8. famiglie: una release che fallisce non ferma le altre
reset_state
printf '%s  %s\n' "$HASH512" "debian-12-generic-amd64.qcow2" >"$STUB_CKFILE"
printf '%s  %s\n' "$HASH512" "debian-13-generic-amd64.qcow2" >>"$STUB_CKFILE"
STUB_CURL_FAIL_MATCH="debian-12-generic" run --family debian
if [ "$RC" = 1 ] && grep -q 'falliti: debian-12' "$WORK/out"; then ok; else ko "famiglia con una release fallita (uscita $RC): $(tail -4 "$WORK/out" | tr '\n' ' ')"; fi
check "debian-13 completato nonostante debian-12" logged "qm template 1006"
check "debian-12 non ha toccato il template" bash -c "! grep -q 'qm .* 1002' '$STUB_LOG'"
# release disabilitata: saltata
sed -i 's/^ENABLED=1/ENABLED=0/' "$TREE/os.d/debian-12.conf"
STUB_CURL_FAIL_MATCH="" run --family debian --dry-run
if [ "$RC" = 0 ] && ! grep -q 'Debian 12' "$WORK/out" && grep -q 'Debian 13' "$WORK/out"; then ok; else ko "release disabilitata non saltata"; fi
sed -i 's/^ENABLED=0/ENABLED=1/' "$TREE/os.d/debian-12.conf"

# ------------------------------------------------------------------ 9. argomenti
for t in '--step nonvalido ubuntu-24.04' '--family windows' 'ubuntu-24.04 debian-13' '--step image --all' 'ubuntu-99.99'; do
  # shellcheck disable=SC2086
  run $t
  if [ "$RC" != 0 ]; then ok; else ko "argomenti accettati per errore: $t"; fi
done
run
if [ "$RC" = 2 ]; then ok; else ko "senza argomenti: uscita $RC"; fi

# ------------------------------------------------------------------ 10. pmgr-add-release.sh (su una copia dell'albero)
ADD="$TREE/bin/pmgr-add-release.sh"
add() { "$ADD" "$@" >"$WORK/out" 2>&1; RC=$?; }
add --family ubuntu --version 26.10 --codename questing --no-check-url
if [ "$RC" = 0 ]; then ok; else ko "add-release ubuntu (uscita $RC): $(cat "$WORK/out")"; fi
C="$TREE/os.d/ubuntu-26.10.conf"
check "add: SLOT 9 (l'8 e' rocky-10)" grep -q '^SLOT=9$' "$C"
check "add: immagine secondo lo schema" grep -q '^IMAGE_URL=https://cloud-images.ubuntu.com/questing/current/questing-server-cloudimg-amd64.img$' "$C"
check "add: nome del template" grep -q '^TEMPLATE_NAME=ubuntu-2610-cloudinit-template$' "$C"
check "add: snippet con il suo nome" grep -q '^SNIPPET=vendor_ubuntu_26.10.yaml$' "$C"
check "add: tag" grep -q '^TAGS=ubuntu-template,26.10,cloudinit$' "$C"
check "add: checksum Ubuntu" grep -q '^IMAGE_CHECKSUM_URL=https://cloud-images.ubuntu.com/questing/current/SHA256SUMS$' "$C"
check "add: corpo copiato dalla release piu' recente" cmp -s "$TREE/snippets.d/ubuntu-26.10.yaml" "$TREE/snippets.d/ubuntu-26.04.yaml"
# la piu' recente e' per VERSIONE, non per SLOT: rocky-9 ha SLOT 4 e rocky-8 SLOT 5
add --family rocky --version 11 --no-check-url
check "add: rocky-11 copiato da rocky-10 (versione piu' alta) e non da rocky-8 (SLOT piu' alto)" bash -c "cmp -s '$TREE/snippets.d/rocky-11.yaml' '$TREE/snippets.d/rocky-10.yaml' && ! cmp -s '$TREE/snippets.d/rocky-11.yaml' '$TREE/snippets.d/rocky-8.yaml'"
run --list
if [ "$RC" = 0 ] && grep -q '^ubuntu-26.10 *VMID 1009' "$WORK/out"; then ok; else ko "la nuova release non compare in --list: $(cat "$WORK/out")"; fi
add --family ubuntu --version 26.10 --codename questing --no-check-url
if [ "$RC" != 0 ] && grep -q "esiste gia'" "$WORK/out"; then ok; else ko "add-release non deve sovrascrivere"; fi
add --family rocky --version 12 --no-check-url
if [ "$RC" = 0 ] && grep -q '^HELPERS=adduser.sh disk.sh dmesg.sh$' "$TREE/os.d/rocky-12.conf" && grep -q '^SLOT=11$' "$TREE/os.d/rocky-12.conf" && ! grep -q CHECKSUM "$TREE/os.d/rocky-12.conf"; then ok; else ko "add-release rocky: $(cat "$WORK/out")"; fi
add --family debian --version 14 --codename forky --like debian-13 --slot 20 --no-check-url
if [ "$RC" = 0 ] && grep -q '^SLOT=20$' "$TREE/os.d/debian-14.conf"; then ok; else ko "add-release debian con --slot"; fi
add --family debian --version 15 --codename x --no-check-url
if [ "$RC" != 0 ]; then ok; else ko "codename troppo corto accettato"; fi
add --family debian --version 15 --no-check-url
if [ "$RC" != 0 ]; then ok; else ko "debian senza codename accettato"; fi
add --family rocky --version 11 --codename x --no-check-url
if [ "$RC" != 0 ]; then ok; else ko "rocky con codename accettato"; fi
add --family ubuntu --version 27.04 --codename missing
if [ "$RC" != 0 ] && grep -q "non risponde" "$WORK/out"; then ok; else ko "URL inesistente accettato (uscita $RC): $(cat "$WORK/out")"; fi
check "add: URL inesistente non scrive niente" test ! -e "$TREE/os.d/ubuntu-27.04.conf"
add --family ubuntu --version 27.04 --codename resolute --slot 3 --no-check-url
if [ "$RC" != 0 ] && grep -q "gia' usato" "$WORK/out"; then ok; else ko "SLOT duplicato accettato"; fi
add --family ubuntu --version '26.10; rm -rf /' --codename x --no-check-url
if [ "$RC" != 0 ]; then ok; else ko "versione ostile accettata"; fi
add --family ubuntu --version 27.04 --codename resolute --url 'http://example.org/x.img' --no-check-url
if [ "$RC" != 0 ]; then ok; else ko "URL http accettato"; fi
add --family ubuntu --version 27.04 --codename resolute --like rocky-9 --no-check-url
if [ "$RC" != 0 ]; then ok; else ko "--like di un'altra famiglia accettato"; fi
add --family ubuntu --version 27.04 --codename resolute --dry-run --no-check-url
if [ "$RC" = 0 ] && grep -q 'non scritto' "$WORK/out" && [ ! -e "$TREE/os.d/ubuntu-27.04.conf" ]; then ok; else ko "dry-run di add-release scrive"; fi
# con la nuova release il controllo di coerenza resta valido (SLOT e snippet unici)
reset_state; write_cluster
run --all --dry-run
if [ "$RC" = 0 ]; then ok; else ko "dry-run --all con le release aggiunte: $(tail -3 "$WORK/out" | tr '\n' ' ')"; fi
cp "$TREE/os.d/ubuntu-26.10.conf" "$WORK/saved.conf"
sed -i 's/^SLOT=9$/SLOT=3/' "$TREE/os.d/ubuntu-26.10.conf"
run --list
expect_die "SLOT duplicato tra due release" "SLOT 3 usato"
cp "$WORK/saved.conf" "$TREE/os.d/ubuntu-26.10.conf"

# ------------------------------------------------------------------ 11. wrapper
for w in update_ubuntu_template.sh update_debian_template.sh update_rocky_template.sh; do
  : >"$STUB_LOG"
  "$TREE/$w" --dry-run >"$WORK/out" 2>&1
  rc=$?
  fam="${w#update_}"
  fam="${fam%_template.sh}"
  if [ "$rc" = 0 ] && grep -q "VMID" "$WORK/out"; then ok; else ko "wrapper $w (uscita $rc)"; fi
done
"$TREE/update_all.sh" --dry-run >"$WORK/out" 2>&1
if grep -q 'sincronizzazione degli snippet NON eseguita' "$WORK/out"; then ok; else ko "update_all.sh deve dichiarare che la sincronizzazione non e' stata eseguita"; fi

printf 'Test: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
