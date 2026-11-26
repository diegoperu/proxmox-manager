#!/bin/bash
# Test degli script dei template con qm/pvesm/curl/qemu-img FINTI: nessun contatto con Proxmox ne' con la rete.
# Gira in un user namespace (unshare -r) per apparire "root" senza esserlo. Solo ASCII.
set -uo pipefail
export LC_ALL=C

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
cp -r "$SRC/bin" "$SRC/lib" "$SRC/arch.d" "$SRC/os.d" "$SRC/snippets.d" "$SRC/helpers" "$SRC/profiles" "$TREE/"
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
        --virtio0 | --ide2 | --scsi2 | --tags | --ciuser | --cicustom | --nameserver | --searchdomain) echo "${1#--}: $2" >>"$(cfg "$v")"; shift ;;
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
cat >"$WORK/bin/dpkg" <<'STUB'
#!/bin/bash
[ "$#" = 1 ] && [ "$1" = --print-architecture ] || exit 2
[ "${STUB_DPKG_FAIL:-0}" = 0 ] || exit 1
printf '%s\n' "${STUB_ARCH:-amd64}"
STUB
chmod 755 "$WORK"/bin/*
export PATH="$WORK/bin:$PATH"
export STUB_LOG="$WORK/stub.log" STUB_STATE="$WORK/state"
export STUB_PVESM_images="pool:active cipool:active" STUB_PVESM_snippets="local:active"
export PMGR_LOCK_FILE="$WORK/lock"
export PMGR_CLUSTER_CONF="$WORK/cluster.conf"
export STUB_ARCH=amd64

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
# Confronto completo dei comandi amd64 preesistenti (l'hash della password ha un sale casuale).
cat >"$WORK/expected-qm" <<EOF
qm create 1003 --name ubuntu-2404-cloudinit-template --ostype l26 --memory 1024 --agent 1 --bios ovmf --machine q35 --efidisk0 pool:0,pre-enrolled-keys=0 --cpu host --sockets 1 --cores 1 --vga serial0 --serial0 socket --net0 virtio,bridge=lo
qm importdisk 1003 $WORK/images/ubuntu-24.04/noble-server-cloudimg-amd64.img pool
qm set 1003 --scsihw virtio-scsi-pci --virtio0 pool:vm-1003-disk-1,discard=on,cache=writeback
qm set 1003 --boot order=virtio0
qm set 1003 --ide2 pool:cloudinit
qm set 1003 --autostart 1 --onboot 1
qm set 1003 --numa 1
qm set 1003 --searchdomain test.local
qm set 1003 --cicustom vendor=local:snippets/vendor_ubuntu_24.04.yaml
qm set 1003 --tags ubuntu-template,24.04,cloudinit
qm set 1003 --ciuser tecnico
qm set 1003 --cipassword <hash>
qm set 1003 --sshkeys $WORK/keys.pub
qm set 1003 --nameserver 10.0.0.1 10.0.0.2
qm template 1003
EOF
grep -E '^qm (create|importdisk|set|template)' "$STUB_LOG" | sed 's/--cipassword .*/--cipassword <hash>/' >"$WORK/actual-qm"
check "amd64: comandi qm identici al comportamento precedente" cmp -s "$WORK/expected-qm" "$WORK/actual-qm"
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
pre() { # descrizione, testo atteso, comando di preparazione e argomenti separati
  local description="$1" expected="$2"
  shift 2
  reset_state
  printf '%s  %s\n' "$HASH256" "noble-server-cloudimg-amd64.img" >"$STUB_CKFILE"
  write_cluster
  printf '%s\n' "$PW" >"$WORK/pw"; chmod 600 "$WORK/pw"
  "$@"
  run ubuntu-24.04
  expect_die "$description" "$expected"
  check "$description: nessuna chiamata distruttiva" no_dangerous_calls
  check "$description: nessun download" bash -c "! grep -q '^curl.* -o ' '$STUB_LOG'"
}
write_test_password() { printf '%s\n' "$@" >"$WORK/pw"; }
pre "storage dei dischi inesistente" "non esiste, non e' attivo" write_cluster STORAGE=nope
pre "storage dei dischi non attivo" "non esiste, non e' attivo" export 'STUB_PVESM_images=pool:inactive cipool:active'
export STUB_PVESM_images="pool:active cipool:active"
pre "storage cloud-init inesistente" "non esiste, non e' attivo" write_cluster CI_STORAGE=nope
pre "storage degli snippet senza il contenuto snippets" "non accetta snippets" export STUB_PVESM_snippets=altro:active
export STUB_PVESM_snippets="local:active"
pre "bridge inesistente" "bridge nope0 non esiste" write_cluster BRIDGE=nope0
pre "password con permessi larghi" "chmod 600" chmod 644 "$WORK/pw"
pre "file della password mancante" "file della password non trovato" rm -f "$WORK/pw"
pre "password troppo corta" "8-128" write_test_password corta
pre "password su due righe" "una sola riga" write_test_password unaPasswordLunga1 altra
pre "chiavi ssh mancanti" "file delle chiavi ssh non leggibile" rm -f "$WORK/keys.pub"
printf 'ssh-ed25519 AAAA test\n' >"$WORK/keys.pub"
pre "cartella degli snippet mancante" "cartella degli snippet non esiste" rmdir "$WORK/snippets"
write_cluster; printf '%s\n' "$PW" >"$WORK/pw"; chmod 600 "$WORK/pw"

# helper non ASCII (rocky): si ferma, nessun file scritto
reset_state
printf '# prova \303\240\303\250\n' >>"$TREE/helpers/disk.sh"
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

# ------------------------------------------------------------------ architetture e ipconfig facoltativo
# URL attesi espliciti: amd64 conserva gli originali, arm64 coincide con la copia ARM dell'utente.
while IFS='|' read -r release url64 file64 urlarm filearm checksum; do
  for arch in amd64 arm64; do
    url="$url64"; image="$file64"
    if [ "$arch" = arm64 ]; then url="$urlarm"; image="$filearm"; fi
    run "$release" --arch "$arch" --step image --dry-run
    if [ "$RC" = 0 ]; then ok; else ko "$release/$arch: dry-run immagine"; fi
    check "$release/$arch: URL esatto" grep -qF "scaricherei $url in $WORK/images/$release/$image," "$WORK/out"
    # Verifica anche i valori caricati (checksum compreso), senza download.
    bash -euo pipefail -c 'source "$1/lib/pmgr-lib.sh"; pmgr_load_arch "$2"; pmgr_load_cluster; pmgr_load_os "$3"; printf "%s|%s|%s\n" "${OS[IMAGE_URL]}" "${OS[IMAGE_FILE]}" "${OS[IMAGE_CHECKSUM_URL]:-}"' bash "$TREE" "$arch" "$release" >"$WORK/values" 2>"$WORK/values.err"
    printf '%s|%s|%s\n' "$url" "$image" "$checksum" >"$WORK/expected-values"
    check "$release/$arch: valori espansi esatti" cmp -s "$WORK/expected-values" "$WORK/values"
  done
done <<'URLS'
ubuntu-22.04|https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img|jammy-server-cloudimg-amd64.img|https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-arm64.img|jammy-server-cloudimg-arm64.img|https://cloud-images.ubuntu.com/jammy/current/SHA256SUMS
ubuntu-24.04|https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img|noble-server-cloudimg-amd64.img|https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-arm64.img|noble-server-cloudimg-arm64.img|https://cloud-images.ubuntu.com/noble/current/SHA256SUMS
ubuntu-26.04|https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img|resolute-server-cloudimg-amd64.img|https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-arm64.img|resolute-server-cloudimg-arm64.img|https://cloud-images.ubuntu.com/resolute/current/SHA256SUMS
debian-12|https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2|debian-12-generic-amd64.qcow2|https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-arm64.qcow2|debian-12-generic-arm64.qcow2|https://cloud.debian.org/images/cloud/bookworm/latest/SHA512SUMS
debian-13|https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64.qcow2|debian-13-generic-amd64.qcow2|https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-arm64.qcow2|debian-13-generic-arm64.qcow2|https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS
rocky-8|https://dl.rockylinux.org/pub/rocky/8.10/images/x86_64/Rocky-8-GenericCloud-Base.latest.x86_64.qcow2|Rocky-8-GenericCloud-Base.latest.x86_64.qcow2|https://dl.rockylinux.org/pub/rocky/8.10/images/aarch64/Rocky-8-GenericCloud-Base.latest.aarch64.qcow2|Rocky-8-GenericCloud-Base.latest.aarch64.qcow2|
rocky-9|https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2|Rocky-9-GenericCloud-Base.latest.x86_64.qcow2|https://dl.rockylinux.org/pub/rocky/9/images/aarch64/Rocky-9-GenericCloud-Base.latest.aarch64.qcow2|Rocky-9-GenericCloud-Base.latest.aarch64.qcow2|
rocky-10|https://dl.rockylinux.org/pub/rocky/10/images/x86_64/Rocky-10-GenericCloud-Base.latest.x86_64.qcow2|Rocky-10-GenericCloud-Base.latest.x86_64.qcow2|https://dl.rockylinux.org/pub/rocky/10/images/aarch64/Rocky-10-GenericCloud-Base.latest.aarch64.qcow2|Rocky-10-GenericCloud-Base.latest.aarch64.qcow2|
URLS

# Percorso completo ARM sia con override sia con rilevamento dal nodo.
# Un hash errato per amd64 prova che il lookup cerca il nome arm64.
printf '%064d  noble-server-cloudimg-amd64.img\n' 0 >"$STUB_CKFILE"
printf '%s  noble-server-cloudimg-arm64.img\n' "$HASH256" >>"$STUB_CKFILE"
for detection in override dpkg; do
  reset_state
  if [ "$detection" = override ]; then
    STUB_DPKG_FAIL=1 run ubuntu-24.04 --arch arm64
  else
    STUB_ARCH=arm64 run ubuntu-24.04
  fi
  if [ "$RC" = 0 ]; then ok; else ko "arm64/$detection completo: $(tail -4 "$WORK/out")"; fi
  check "arm64/$detection: macchina virt" logged '--bios ovmf --machine virt --efidisk0'
  check "arm64/$detection: cloud-init scsi2" logged '^qm set 1003 --scsi2 pool:cloudinit$'
  check "arm64/$detection: nessun ide2" bash -c '! grep -q -- --ide2 "$1"' bash "$STUB_LOG"
  check "arm64/$detection: checksum verificato per nome arm64" grep -q 'checksum sha256 verificato' "$WORK/out"
  check "arm64/$detection: immagine arm64 importata" logged "qm importdisk 1003 $WORK/images/ubuntu-24.04/noble-server-cloudimg-arm64.img pool"
  check "arm64/$detection: finalize controlla scsi2" logged '^qm template 1003$'
  sed -e 's/--machine q35/--machine virt/' -e 's/--ide2 /--scsi2 /' -e 's/noble-server-cloudimg-amd64.img/noble-server-cloudimg-arm64.img/' "$WORK/expected-qm" >"$WORK/expected-arm-qm"
  grep -E '^qm (create|importdisk|set|template)' "$STUB_LOG" | sed 's/--cipassword .*/--cipassword <hash>/' >"$WORK/actual-qm"
  check "arm64/$detection: solo le differenze hardware e immagine attese" cmp -s "$WORK/expected-arm-qm" "$WORK/actual-qm"
done
# Un ide2 residuo non deve mascherare un cloud-init scsi2 mancante.
sed -i '/^template:/d; /^scsi2:/d' "$WORK/state/vm-1003.cfg"
printf 'ide2: pool:cloudinit\n' >>"$WORK/state/vm-1003.cfg"
run ubuntu-24.04 --arch arm64 --step finalize
expect_die "arm64: finalize senza scsi2 rifiutato" 'manca scsi2'
check "arm64 incompleta: nessuna scrittura qm" no_dangerous_calls

reset_state
printf '%0128d  debian-13-generic-amd64.qcow2\n' 0 >"$STUB_CKFILE"
printf '%s  debian-13-generic-arm64.qcow2\n' "$HASH512" >>"$STUB_CKFILE"
STUB_ARCH=arm64 run debian-13
if [ "$RC" = 0 ]; then ok; else ko "Debian arm64 completo: $(tail -4 "$WORK/out")"; fi
check "Debian arm64: checksum SHA512 per nome arm64" grep -q 'checksum sha512 verificato' "$WORK/out"

# ARCHES limita le operazioni sui template, non i manifest degli snippet.
printf '\nARCHES=amd64\n' >>"$TREE/os.d/ubuntu-24.04.conf"
for selection in all family; do
  if [ "$selection" = all ]; then
    run --all --arch arm64 --dry-run
  else
    run --family ubuntu --arch arm64 --dry-run
  fi
  if [ "$RC" = 0 ]; then ok; else ko "ARCHES/$selection: uscita $RC"; fi
  check "ARCHES/$selection: messaggio di skip" grep -q 'ubuntu-24.04 saltata: non disponibile' "$WORK/out"
  check "ARCHES/$selection: nessun passo della release incompatibile" bash -c '! grep -qE "scaricherei .*noble|qm .* 1003" "$1"' bash "$WORK/out"
  check "ARCHES/$selection: altra release completata" grep -q 'template 1001 .* pronto' "$WORK/out"
done
run ubuntu-24.04 --arch arm64
expect_die "ARCHES: ID incompatibile rifiutato" 'ubuntu-24.04 non disponibile'
check "ARCHES: rifiuto prima di qualsiasi comando del cluster" test ! -s "$STUB_LOG"
run --list --arch arm64
check "list: release incompatibile visibile" grep -q '^ubuntu-24.04 .*arm64: non disponibile' "$WORK/out"
check "list: release compatibile visibile" grep -q '^ubuntu-22.04 .*arm64: disponibile' "$WORK/out"

for profile in standard hosting; do
  mkdir -p "$WORK/render-amd64" "$WORK/render-arm64" "$WORK/render-nodpkg"
  run --render "$WORK/render-amd64" --profile "$profile" --arch amd64 --all
  if [ "$RC" = 0 ]; then ok; else ko "render amd64/$profile"; fi
  run --render "$WORK/render-arm64" --profile "$profile" --arch arm64 --all
  if [ "$RC" = 0 ]; then ok; else ko "render arm64/$profile"; fi
  check "render $profile: otto file identici anche con ARCHES limitato" diff -r "$WORK/render-amd64" "$WORK/render-arm64"
  check "render $profile: release esclusa presente" test -s "$WORK/render-arm64/vendor_ubuntu_24.04.yaml"
  STUB_DPKG_FAIL=1 run --render "$WORK/render-nodpkg" --profile "$profile" --all
  if [ "$RC" = 0 ]; then ok; else ko "render $profile senza dpkg"; fi
  check "render $profile indipendente da dpkg" diff -r "$WORK/render-amd64" "$WORK/render-nodpkg"
done
cp "$SRC/os.d/ubuntu-24.04.conf" "$TREE/os.d/ubuntu-24.04.conf"

for bad in '' 'riscv64' 'amd64 riscv64' 'amd64;id' 'amd64 amd64' 'amd64  arm64'; do
  printf '\nARCHES=%s\n' "$bad" >>"$TREE/os.d/ubuntu-24.04.conf"
  run --all --arch arm64
  expect_die "ARCHES non valido: $bad" 'valore non valido per ARCHES'
  check "ARCHES non valido: nessun comando" test ! -s "$STUB_LOG"
  cp "$SRC/os.d/ubuntu-24.04.conf" "$TREE/os.d/ubuntu-24.04.conf"
done
for arches in 'arm64' 'amd64 arm64' 'arm64 amd64'; do
  printf '\nARCHES=%s\n' "$arches" >>"$TREE/os.d/ubuntu-24.04.conf"
  run ubuntu-24.04 --arch arm64 --dry-run
  if [ "$RC" = 0 ]; then ok; else ko "ARCHES valido rifiutato: $arches"; fi
  cp "$SRC/os.d/ubuntu-24.04.conf" "$TREE/os.d/ubuntu-24.04.conf"
done

# Valori di architettura ostili non diventano mai percorsi o opzioni qm.
for bad in '' riscv64 '../amd64' 'amd64;id'; do
  run ubuntu-24.04 --arch "$bad"
  expect_die "--arch non valido: $bad" '--arch deve essere'
  check "--arch non valido: nessun comando" test ! -s "$STUB_LOG"
done
STUB_ARCH=riscv64 run ubuntu-24.04
expect_die "architettura dpkg sconosciuta" 'architettura non supportata'
check "architettura sconosciuta: nessun comando" test ! -s "$STUB_LOG"
STUB_DPKG_FAIL=1 run ubuntu-24.04
expect_die "dpkg fallito" "impossibile rilevare l'architettura"
check "dpkg fallito: nessun comando" test ! -s "$STUB_LOG"

for bad in 'MACHINE=pc' 'CLOUDINIT_DRIVE=ide22' 'CLOUDINIT_DRIVE=scsi2;id' 'EXTRA=1'; do
  printf '%s\n' "$bad" >"$TREE/arch.d/arm64.conf"
  run ubuntu-24.04 --arch arm64
  expect_die "arch.d non valido: $bad" 'non valido\|sconosciuta'
  check "arch.d non valido: nessun comando" test ! -s "$STUB_LOG"
done
printf 'MACHINE=virt\n' >"$TREE/arch.d/arm64.conf"
run ubuntu-24.04 --arch arm64
expect_die "arch.d incompleto" 'manca CLOUDINIT_DRIVE'
printf 'MACHINE=virt\nMACHINE=virt\nCLOUDINIT_DRIVE=scsi2\n' >"$TREE/arch.d/arm64.conf"
run ubuntu-24.04 --arch arm64
expect_die "arch.d chiave ripetuta" 'chiave ripetuta'
rm "$TREE/arch.d/arm64.conf"
ln -s "$SRC/arch.d/arm64.conf" "$TREE/arch.d/arm64.conf"
run ubuntu-24.04 --arch arm64
expect_die "arch.d link simbolico" 'link simbolico'
rm "$TREE/arch.d/arm64.conf"
cp "$SRC/arch.d/arm64.conf" "$TREE/arch.d/arm64.conf"

# Tutti e tre i campi immagine supportano i segnaposto, poi applicano le regex originali.
printf '%s  noble-server-cloudimg-arm64.img\n' "$HASH256" >"$STUB_CKFILE"
sed -i 's|^IMAGE_CHECKSUM_URL=.*|IMAGE_CHECKSUM_URL=https://example.org/{arch}/{march}/SHA256SUMS|' "$TREE/os.d/ubuntu-24.04.conf"
run ubuntu-24.04 --arch arm64 --step image
if [ "$RC" = 0 ]; then ok; else ko "segnaposto nel checksum URL: $(tail -3 "$WORK/out")"; fi
check "checksum URL espanso prima di curl" logged 'https://example.org/arm64/aarch64/SHA256SUMS'
cp "$SRC/os.d/ubuntu-24.04.conf" "$TREE/os.d/ubuntu-24.04.conf"
for bad in 'IMAGE_FILE=bad-{unknown}.img' 'IMAGE_FILE=../{arch}.img' 'IMAGE_FILE=$(id)-{arch}.img' 'IMAGE_URL=http://example.org/{arch}.img' 'IMAGE_CHECKSUM_URL=https://example.org/{unknown}/SHA256SUMS'; do
  key="${bad%%=*}"
  sed -i "/^${key}=/d" "$TREE/os.d/ubuntu-24.04.conf"
  printf '%s\n' "$bad" >>"$TREE/os.d/ubuntu-24.04.conf"
  run ubuntu-24.04 --arch arm64
  expect_die "campo immagine ostile: $key" "valore non valido per $key"
  check "campo immagine ostile: nessun comando" test ! -s "$STUB_LOG"
  cp "$SRC/os.d/ubuntu-24.04.conf" "$TREE/os.d/ubuntu-24.04.conf"
done

for arch in amd64 arm64; do
  reset_state
  run rocky-9 --arch "$arch"
  if [ "$RC" = 0 ]; then ok; else ko "Rocky/$arch completo"; fi
  check "ipconfig assente: nessuna impostazione" bash -c '! grep -q -- --ipconfig0 "$1"' bash "$STUB_LOG"
  write_cluster TEMPLATE_IPCONFIG=
  run rocky-9 --arch "$arch"
  if [ "$RC" = 0 ]; then ok; else ko "TEMPLATE_IPCONFIG vuoto/$arch"; fi
  check "ipconfig vuoto: nessuna impostazione" bash -c '! grep -q -- --ipconfig0 "$1"' bash "$STUB_LOG"
  write_cluster TEMPLATE_IPCONFIG=ip=dhcp
  run rocky-9 --arch "$arch"
  if [ "$RC" = 0 ]; then ok; else ko "TEMPLATE_IPCONFIG DHCP/$arch"; fi
  check "DHCP prima di qm template/$arch" awk '/^qm set 1004 --ipconfig0 ip=dhcp$/ {ip=NR} /^qm template 1004$/ {ok=(ip>0 && ip<NR)} END {exit !ok}' "$STUB_LOG"
  write_cluster
done
for bad in ip=auto ip=dhcp6 'ip=dhcp,ip6=dhcp' 'ip=dhcp;id' ' ip=dhcp'; do
  write_cluster "TEMPLATE_IPCONFIG=$bad"
  run ubuntu-24.04 --arch arm64
  expect_die "TEMPLATE_IPCONFIG non valido: $bad" 'valore non valido per TEMPLATE_IPCONFIG'
  check "TEMPLATE_IPCONFIG non valido: nessun comando" test ! -s "$STUB_LOG"
done
write_cluster

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
