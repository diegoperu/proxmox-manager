#!/bin/bash
# Gira DENTRO il container "sorgente": esegue pmgr-snippets-sync.sh come utente pmgr-sync contro due
# destinazioni vere (porte 2222 standard e 2223 hosting) e controlla gli esiti.
set -euo pipefail
exec </dev/null
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq rsync openssh-client util-linux python3 procps >/dev/null 2>&1
mkdir -p /var/lib/vz
bash /t/pmgr-sync-setup.sh --role source >/tmp/setup.out 2>&1 || { cat /tmp/setup.out; exit 1; }
# la chiave di prova al posto di quella generata (le destinazioni autorizzano questa)
install -o pmgr-sync -g pmgr-sync -m 600 /w/key /var/lib/pmgr-sync/.ssh/id_ed25519
install -o pmgr-sync -g pmgr-sync -m 644 /w/key.pub /var/lib/pmgr-sync/.ssh/id_ed25519.pub
# copia scrivibile degli script veri (la sorgente "di verita'")
cp -a /scripts /srv/scripts && chown -R pmgr-sync:pmgr-sync /srv/scripts
mkdir -p /srv/snippets && chown pmgr-sync:pmgr-sync /srv/snippets
printf '#cloud-config\n# snippet manuale\n' >/srv/snippets/manuale.yaml && chown pmgr-sync:pmgr-sync /srv/snippets/manuale.yaml
mkdir -p /etc/pmgr
cat >/etc/pmgr/sync.conf <<CONF
SCRIPTS_DIR=/srv/scripts
SNIPPETS_DIR=/srv/snippets
dest alfa 127.0.0.1 profile=standard port=2222
dest beta 127.0.0.1 profile=hosting port=2223
CONF
chmod 644 /etc/pmgr/sync.conf

PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "FALLITO: $1"; }
check() { if "${@:2}" >/dev/null 2>&1; then ok; else ko "$1"; fi; }
S() { runuser -u pmgr-sync -- bash /srv/scripts/bin/pmgr-snippets-sync.sh "$@"; }
# esegue e conserva uscita e output
R() { set +e; S "$@" >/tmp/out 2>&1; RC=$?; set -e; }
SSHO="-i /var/lib/pmgr-sync/.ssh/id_ed25519 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o IdentitiesOnly=yes"
rlist() { ssh $SSHO -p "$1" pmgr-sync@127.0.0.1 pmgr-list; }

# ---- 1. prima di sincronizzare: differenze
R status
check "status prima: esce 3" [ "$RC" = 3 ]
check "status: alfa da allineare" grep -q 'alfa .*DA-ALLINEARE' /tmp/out
check "status: beta da allineare" grep -q 'beta .*DA-ALLINEARE' /tmp/out
R diff --dest alfa
check "diff: lo snippet vecchio e' 'diverso'" grep -qE '^ +diverso +snippets/vendor_ubuntu_26.04.yaml' /tmp/out
check "diff: lo snippet in piu' e' 'in_piu'" grep -qE '^ +in_piu +snippets/extra.yaml' /tmp/out
check "diff: gli script sono 'mancante'" grep -qE '^ +mancante +pmgr-scripts/bin/pmgr-os.sh' /tmp/out
rlist 2222 >/tmp/before_a; rlist 2223 >/tmp/before_b

# ---- 2. anteprima: nessuna modifica
R run
check "anteprima esce 3" [ "$RC" = 3 ]
check "anteprima dice che non modifica" grep -q 'anteprima: nessuna modifica' /tmp/out
rlist 2222 >/tmp/after_a; rlist 2223 >/tmp/after_b
check "anteprima: alfa invariata" cmp -s /tmp/before_a /tmp/after_a
check "anteprima: beta invariata" cmp -s /tmp/before_b /tmp/after_b

# ---- 3. copia
R run --apply
cat /tmp/out | tail -n 12
check "run --apply esce 0" [ "$RC" = 0 ]
check "hash verificati su alfa" grep -q 'hash verificati' /tmp/out
R status
check "status dopo la copia: esce 0" [ "$RC" = 0 ]
check "status: alfa allineata" grep -q 'alfa .* allineata' /tmp/out
check "status: beta allineata" grep -q 'beta .* allineata' /tmp/out
check "status: l'extra e' contato 'in piu'" grep -q 'alfa .*in piu 1' /tmp/out
rlist 2222 >/tmp/a2; rlist 2223 >/tmp/b2
check "l'extra non e' stato cancellato" grep -q 'snippets/extra.yaml' /tmp/a2
check "lo snippet manuale e' arrivato" grep -q 'snippets/manuale.yaml' /tmp/a2
check "il MANIFEST e' arrivato (snippets)" grep -q 'snippets/MANIFEST.sha256' /tmp/a2
check "il MANIFEST e' arrivato (script)" grep -q 'pmgr-scripts/MANIFEST.sha256' /tmp/a2
# profili: alfa riceve gli snippet standard, beta quelli hosting; il manifest dice quali
S manifest --profile standard >/tmp/man_std; S manifest --profile hosting >/tmp/man_host
h_std=$(grep ' snippets/vendor_ubuntu_26.04.yaml$' /tmp/man_std | cut -d' ' -f1)
h_host=$(grep ' snippets/vendor_ubuntu_26.04.yaml$' /tmp/man_host | cut -d' ' -f1)
check "i due profili hanno snippet diversi" [ -n "$h_std" ] && [ -n "$h_host" ] && [ "$h_std" != "$h_host" ]
check "alfa ha lo snippet standard" grep -q "^$h_std"$'\t'"[0-9]*"$'\t'"[0-9]*"$'\t'"snippets/vendor_ubuntu_26.04.yaml" /tmp/a2
check "beta ha lo snippet hosting" grep -q "^$h_host"$'\t'"[0-9]*"$'\t'"[0-9]*"$'\t'"snippets/vendor_ubuntu_26.04.yaml" /tmp/b2
mkdir -p /tmp/rh /tmp/rs
bash /srv/scripts/bin/pmgr-os.sh --render /tmp/rh --profile hosting ubuntu-26.04 >/dev/null 2>&1; bash /srv/scripts/bin/pmgr-os.sh --render /tmp/rs --profile standard ubuntu-26.04 >/dev/null 2>&1
check "lo snippet hosting generato non abilita la password SSH" bash -c "! grep -q 'PasswordAuthentication yes' /tmp/rh/vendor_ubuntu_26.04.yaml"
check "lo snippet standard generato la abilita" grep -q 'PasswordAuthentication yes' /tmp/rs/vendor_ubuntu_26.04.yaml
check "l'hash di beta e' quello dello snippet hosting generato" bash -c "[ \"\$(sha256sum /tmp/rh/vendor_ubuntu_26.04.yaml | cut -d' ' -f1)\" = '$h_host' ]"
check "il registro ha le righe della copia" grep -q ' run alfa copiati ' /var/lib/pmgr-sync/sync.log

# ---- 4. file che non si devono mai consegnare
cd /srv/scripts
printf 'x\n' >cluster.conf; printf 'ssh-ed25519 AAAA\n' >ssh_keys.pub; printf 'segreto\n' >template-password
printf 'x\n' >immagine.img; printf 'x\n' >disco.iso; printf 'x\n' >nascosto.sh.part; printf 'x\n' >.nascosto.sh
chown -R pmgr-sync:pmgr-sync /srv/scripts
S manifest --profile standard >/tmp/man2 2>&1 || true
check "cluster.conf escluso" bash -c '! grep -q "cluster.conf$" /tmp/man2'
check "chiavi pubbliche escluse" bash -c '! grep -q "ssh_keys.pub" /tmp/man2'
check "password escluse" bash -c '! grep -q "template-password" /tmp/man2'
check "immagini e ISO escluse" bash -c '! grep -qE "\.(img|iso|part)$" /tmp/man2'
rm -f /srv/scripts/{cluster.conf,ssh_keys.pub,template-password,immagine.img,disco.iso,nascosto.sh.part,.nascosto.sh}
cd /

# ---- 4b. estensioni: le nuove (.service .timer .local .sed) arrivano, le altre sono dichiarate e non consegnate
rlist 2222 >/tmp/a_ext
check ".timer consegnato" grep -q 'pmgr-scripts/profiles/hosting/pmgr-ssh-enforce.timer' /tmp/a_ext
check ".service consegnato" grep -q 'pmgr-scripts/profiles/hosting/pmgr-ssh-enforce.service' /tmp/a_ext
check ".local consegnato" grep -q 'pmgr-scripts/profiles/hosting/60-pmgr-recidive.local' /tmp/a_ext
check ".sed consegnato" grep -q 'pmgr-scripts/profiles/updates/dnf-automatic.sed' /tmp/a_ext
printf 'x\n' >/srv/scripts/strano.xyz; printf 'x\n' >/srv/scripts/altro.bin; chown pmgr-sync:pmgr-sync /srv/scripts/strano.xyz /srv/scripts/altro.bin
R status
check "estensioni non ammesse: avviso con i nomi" grep -q 'ATTENZIONE: 2 file NON consegnati.*strano.xyz' /tmp/out
check "estensioni non ammesse: nessun effetto sull'esito" [ "$RC" = 0 ]
rm -f /srv/scripts/strano.xyz /srv/scripts/altro.bin

# ---- 5. modifica sulla sorgente: si ricopia solo quello che cambia
printf 'riga nuova\n' >>/srv/scripts/README.md
R diff
check "diff: README diverso su entrambe" [ "$(grep -c 'diverso  *pmgr-scripts/README.md' /tmp/out)" = 2 ]
check "diff: esce 3" [ "$RC" = 3 ]
R run --dest alfa --apply
check "run solo su alfa: esce 0" [ "$RC" = 0 ]
check "run solo su alfa: copiati 2 file (il file e il MANIFEST)" grep -q 'copiati 2 file' /tmp/out
R status
check "beta resta da allineare (non toccata)" grep -q 'beta .*DA-ALLINEARE' /tmp/out
check "alfa allineata" grep -q 'alfa .* allineata' /tmp/out
R run --apply
check "beta allineata dopo il secondo giro" [ "$RC" = 0 ]

# ---- 6. --missing-only non sovrascrive
printf 'cambiato ancora\n' >>/srv/scripts/README.md
printf '#!/bin/bash\necho nuovo\n' >/srv/scripts/bin/nuovo.sh; chown pmgr-sync:pmgr-sync /srv/scripts/bin/nuovo.sh; chmod 755 /srv/scripts/bin/nuovo.sh
R run --dest alfa --apply --missing-only
check "missing-only: esce 0" [ "$RC" = 0 ]
rlist 2222 >/tmp/a3
check "missing-only: il file nuovo e' arrivato" grep -q 'pmgr-scripts/bin/nuovo.sh' /tmp/a3
R diff --dest alfa
check "missing-only: README resta diverso" grep -qE '^ +diverso +pmgr-scripts/README.md' /tmp/out
R run --apply
check "run completo allinea" [ "$RC" = 0 ]

# ---- 7. rifiuti sulla sorgente: link simbolici e file troppo grandi
ln -s /etc/passwd /srv/scripts/link.sh
R run --apply
check "link simbolico: errore (1)" [ "$RC" = 1 ]
check "link simbolico: messaggio" grep -qi 'link simbolici' /tmp/out
rm -f /srv/scripts/link.sh
head -c 1100000 /dev/zero | tr '\0' 'a' >/srv/scripts/enorme.txt; chown pmgr-sync:pmgr-sync /srv/scripts/enorme.txt
R run --apply
check "file troppo grande: errore (1)" [ "$RC" = 1 ]
check "file troppo grande: messaggio" grep -q 'troppo grande' /tmp/out
rm -f /srv/scripts/enorme.txt
rlist 2222 >/tmp/a4
check "nulla di 'enorme' sulla destinazione" bash -c '! grep -q enorme /tmp/a4'

# ---- 8. JSON
R status --json
python3 -c 'import json,sys; d=json.load(open("/tmp/out")); assert d["mode"]=="status"; assert [x["name"] for x in d["dests"]]==["alfa","beta"]; assert all(x["state"]=="ok" for x in d["dests"]); assert d["dests"][0]["extra"]==1' 2>/tmp/jerr
check "status --json valido e coerente" [ "$?" = 0 ] && [ ! -s /tmp/jerr ]
R diff --json --all
python3 -c 'import json; d=json.load(open("/tmp/out")); f=d["dests"][0]["files"]; assert any(x["path"]=="snippets/extra.yaml" and x["state"]=="in_piu" for x in f); assert any(x["state"]=="uguale" for x in f)' 2>/tmp/jerr
check "diff --json --all valido" [ ! -s /tmp/jerr ]
printf 'ancora\n' >>/srv/scripts/README.md
R run --json
python3 -c 'import json; d=json.load(open("/tmp/out")); assert d["apply"] is False; assert all(x["result"]=="preview" for x in d["dests"]); assert "da copiare" in d["dests"][0]["detail"]' 2>/tmp/jerr
check "run --json (anteprima) valido, con dettaglio" [ ! -s /tmp/jerr ]
R run --json --apply
python3 -c 'import json; d=json.load(open("/tmp/out")); assert d["apply"] is True; assert all(x["result"]=="ok" for x in d["dests"]), d' 2>/tmp/jerr
check "run --json --apply valido" [ ! -s /tmp/jerr ]

# ---- 9. una destinazione irraggiungibile non ferma le altre
cat >/etc/pmgr/sync2.conf <<CONF
SCRIPTS_DIR=/srv/scripts
SNIPPETS_DIR=/srv/snippets
dest giu 127.0.0.1 port=2299
dest alfa 127.0.0.1 profile=standard port=2222
CONF
R --config /etc/pmgr/sync2.conf status
check "irraggiungibile: esce 1" [ "$RC" = 1 ]
check "irraggiungibile: errore per giu" grep -q 'giu .*ERRORE' /tmp/out
check "irraggiungibile: alfa comunque controllata" grep -q 'alfa .* allineata' /tmp/out

# ---- 10. blocco: una sola copia alla volta
( flock 9; sleep 8 ) 9>/var/lib/pmgr-sync/sync.lock &
sleep 1
printf 'x\n' >>/srv/scripts/README.md
R run --apply
check "con il blocco preso: errore" [ "$RC" = 1 ]
check "con il blocco preso: messaggio" grep -q "un'altra sincronizzazione" /tmp/out
wait
chown pmgr-sync:pmgr-sync /var/lib/pmgr-sync/sync.lock
R run --apply
check "a blocco libero la copia riesce" [ "$RC" = 0 ]

# ---- 11. configurazioni e argomenti ostili: errore prima di toccare la rete
bad() { # DESCRIZIONE RIGA(E)
  printf 'SCRIPTS_DIR=/srv/scripts\n%s\n' "$2" >/etc/pmgr/bad.conf
  R --config /etc/pmgr/bad.conf status
  check "config ostile rifiutata: $1" [ "$RC" = 1 ] && ! grep -q 'alfa' /tmp/out
}
bad "nome con maiuscole" 'dest ALFA 127.0.0.1'
bad "host con spazio/comando" 'dest a 127.0.0.1;id'
bad "host che inizia con trattino" 'dest a -oProxyCommand=id'
bad "porta fuori range" 'dest a 127.0.0.1 port=70000'
bad "profilo sconosciuto" 'dest a 127.0.0.1 profile=altro'
bad "opzione sconosciuta" 'dest a 127.0.0.1 x=1'
bad "destinazione ripetuta" $'dest a 127.0.0.1\ndest a 127.0.0.2'
bad "chiave sconosciuta" $'FOO=/x\ndest a 127.0.0.1'
bad "percorso con .." $'STATE_DIR=/tmp/../etc\ndest a 127.0.0.1'
bad "percorso relativo" $'SSH_KEY=chiave\ndest a 127.0.0.1'
bad "nessuna destinazione" '# vuoto'
set +e; S status --bogus >/dev/null 2>&1; rc=$?; S status --apply >/dev/null 2>&1; rc2=$?; S diff --missing-only >/dev/null 2>&1; rc3=$?
S run --dest 'a;b' >/dev/null 2>&1; rc4=$?; S run --dest sconosciuta >/dev/null 2>&1; rc5=$?; S >/dev/null 2>&1; rc6=$?; S run status >/dev/null 2>&1; rc7=$?
S --config 'rel.conf' status >/dev/null 2>&1; rc8=$?
set -e
check "opzione sconosciuta: 2" [ "$rc" = 2 ]
check "--apply con status: 2" [ "$rc2" = 2 ]
check "--missing-only con diff: 2" [ "$rc3" = 2 ]
check "--dest con caratteri ostili: 2" [ "$rc4" = 2 ]
check "--dest sconosciuta: 2" [ "$rc5" = 2 ]
check "senza comando: 2" [ "$rc6" = 2 ]
check "due comandi: 2" [ "$rc7" = 2 ]
check "--config relativo: 2" [ "$rc8" = 2 ]

# ---- 12. discover
check "discover fuori da Proxmox: errore" bash -c '! runuser -u pmgr-sync -- bash /srv/scripts/bin/pmgr-snippets-sync.sh discover >/dev/null 2>&1'
mkdir -p /etc/pve
H=$(hostname -s)
cat >/etc/pve/.members <<CONF
{
"nodename": "$H",
"version": 4,
"cluster": { "name": "prova", "version": 3, "nodes": 3, "quorate": 1 },
"nodelist": {
  "$H": { "id": 1, "online": 1, "ip": "10.0.0.1"},
  "Altro-Nodo.x": { "id": 2, "online": 1, "ip": "10.0.0.2"},
  "nodo3": { "id": 3, "online": 1, "ip": "10.0.0.3"}
}
}
CONF
S discover >/tmp/disc 2>&1 || true
check "discover: propone il nodo 2 (nome normalizzato)" grep -q '^dest altro-nodo-x 10.0.0.2 profile=standard$' /tmp/disc
check "discover: propone il nodo 3" grep -q '^dest nodo3 10.0.0.3 profile=standard$' /tmp/disc
check "discover: esclude il nodo locale" bash -c "! grep -q '10.0.0.1' /tmp/disc"

echo "Sync (sorgente -> 2 destinazioni): $PASS riusciti, $FAIL falliti"
[ "$FAIL" = 0 ]
