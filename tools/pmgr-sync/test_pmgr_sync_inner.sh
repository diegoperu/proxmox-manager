#!/bin/bash
# Corpo del test di integrazione: gira DENTRO il container (lanciato da test_pmgr_sync_container.sh).
set -euo pipefail
exec </dev/null
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq rsync openssh-server openssh-client python3 util-linux procps >/dev/null 2>&1
mkdir -p /var/lib/vz/snippets
printf 'vecchio\n' >/var/lib/vz/snippets/vendor_esistente.yaml     # file gia' presente, di root

PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "FALLITO: $1"; }
check() { if "${@:2}" >/dev/null 2>&1; then ok; else ko "$1"; fi; }

# chiave del "nodo sorgente"
ssh-keygen -q -t ed25519 -N '' -f /tmp/src_key
S=/t/pmgr-sync-setup.sh

# --check su un nodo vergine: nessuna scrittura, uscita 3
set +e; $S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1 --check >/tmp/c1.out 2>&1; rc=$?; set -e
check "check su nodo vergine esce 3" [ "$rc" = 3 ]
check "check non crea l'utente" bash -c '! getent passwd pmgr-sync >/dev/null'
check "check non crea il gate" [ ! -e /usr/local/libexec/pmgr/pmgr-sync-gate ]

# prima esecuzione
$S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1 >/tmp/r1.out 2>&1 || { cat /tmp/r1.out; ko "prima esecuzione"; }
check "utente creato" getent passwd pmgr-sync
check "password disabilitata" [ "$(getent shadow pmgr-sync | cut -d: -f2)" = '*' ]
check "home 750" [ "$(stat -c %a /var/lib/pmgr-sync)" = 750 ]
check "snippets root:pmgr-sync 2775" [ "$(stat -c %U:%G:%a /var/lib/vz/snippets)" = root:pmgr-sync:2775 ]
check "pmgr-scripts root:pmgr-sync 2775" [ "$(stat -c %U:%G:%a /var/lib/vz/pmgr-scripts)" = root:pmgr-sync:2775 ]
check "gate root 755" [ "$(stat -c %U:%G:%a /usr/local/libexec/pmgr/pmgr-sync-gate)" = root:root:755 ]
check "authorized_keys 600 di pmgr-sync" [ "$(stat -c %U:%a /var/lib/pmgr-sync/.ssh/authorized_keys)" = pmgr-sync:600 ]
check "authorized_keys restringe comando e indirizzo" grep -q '^restrict,from="127.0.0.1",command="/usr/local/libexec/pmgr/pmgr-sync-gate" ssh-ed25519 ' /var/lib/pmgr-sync/.ssh/authorized_keys
check "authorized_keys non ha altre righe" [ "$(wc -l </var/lib/pmgr-sync/.ssh/authorized_keys)" = 1 ]
check "il file esistente non e' stato toccato" [ "$(cat /var/lib/vz/snippets/vendor_esistente.yaml)" = vecchio ]
check "pmgr-sync scrive in snippets" runuser -u pmgr-sync -- touch /var/lib/vz/snippets/prova
check "pmgr-sync non scrive altrove in vz" bash -c '! runuser -u pmgr-sync -- touch /var/lib/vz/altro 2>/dev/null'
check "pmgr-sync non scrive in /etc" bash -c '! runuser -u pmgr-sync -- touch /etc/prova 2>/dev/null'
rm -f /var/lib/vz/snippets/prova

# seconda esecuzione: nessuna modifica
$S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1 >/tmp/r2.out 2>&1 || { cat /tmp/r2.out; ko "seconda esecuzione"; }
check "idempotente (nessun [fatto])" bash -c '! grep -q "\[fatto\]" /tmp/r2.out'
set +e; $S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1 --check >/dev/null 2>&1; rc=$?; set -e
check "check dopo il setup esce 0" [ "$rc" = 0 ]

# cambiare la lista degli indirizzi aggiorna authorized_keys e nient'altro
$S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1,10.0.0.0/24 >/tmp/r3.out 2>&1
check "from aggiornato" grep -q 'from="127.0.0.1,10.0.0.0/24"' /var/lib/pmgr-sync/.ssh/authorized_keys
check "solo authorized_keys cambiato" bash -c '[ "$(grep -c "\[fatto\]" /tmp/r3.out)" = 1 ]'

# permessi rovinati a mano vengono ripristinati
chmod 755 /var/lib/vz/snippets; chown root:root /var/lib/vz/snippets
$S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1,10.0.0.0/24 >/dev/null 2>&1
check "permessi ripristinati" [ "$(stat -c %U:%G:%a /var/lib/vz/snippets)" = root:pmgr-sync:2775 ]

# utente esistente con home diversa: si ferma senza modificare
usermod -d /home/altro pmgr-sync 2>/dev/null
set +e; $S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1 >/tmp/r4.out 2>&1; rc=$?; set -e
check "utente con home diversa: errore" [ "$rc" = 1 ]
check "l'home diversa non e' stata corretta di nascosto" [ "$(getent passwd pmgr-sync | cut -d: -f6)" = /home/altro ]
usermod -d /var/lib/pmgr-sync pmgr-sync

# ruolo sorgente: genera la chiave una volta sola
$S --role source >/tmp/r5.out 2>&1 || { cat /tmp/r5.out; ko "ruolo source"; }
check "chiave generata" [ -f /var/lib/pmgr-sync/.ssh/id_ed25519 ]
check "chiave privata 600 di pmgr-sync" [ "$(stat -c %U:%a /var/lib/pmgr-sync/.ssh/id_ed25519)" = pmgr-sync:600 ]
check "la chiave pubblica e' stampata" grep -q '^ssh-ed25519 ' /tmp/r5.out
fp1=$(ssh-keygen -lf /var/lib/pmgr-sync/.ssh/id_ed25519.pub)
$S --role source >/dev/null 2>&1
check "la chiave non cambia al secondo giro" [ "$fp1" = "$(ssh-keygen -lf /var/lib/pmgr-sync/.ssh/id_ed25519.pub)" ]

# ruolo source con solo chiave dev: authorized_keys ha una riga con shell, vincolata all'indirizzo
ssh-keygen -q -t ed25519 -N '' -f /tmp/dev_key
$S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1 --dev-pubkey /tmp/dev_key.pub --dev-from 127.0.0.1 >/dev/null 2>&1
check "riga dev presente e vincolata" grep -q '^from="127.0.0.1" ssh-ed25519 .* pmgr-dev$' /var/lib/pmgr-sync/.ssh/authorized_keys
$S --role dest --source-pubkey /tmp/src_key.pub --source-from 127.0.0.1 >/dev/null 2>&1
check "senza --dev-pubkey la riga dev sparisce" bash -c '! grep -q pmgr-dev /var/lib/pmgr-sync/.ssh/authorized_keys'

# --- prova con sshd e rsync veri ----------------------------------------------------------
mkdir -p /run/sshd
ssh-keygen -A >/dev/null 2>&1
cat >/etc/ssh/sshd_config.d/test.conf <<'EOF'
ListenAddress 127.0.0.1
Port 2222
PasswordAuthentication no
PermitRootLogin no
EOF
/usr/sbin/sshd
sleep 1
SSHO="-i /tmp/src_key -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o IdentitiesOnly=yes"

# la chiave sorgente puo' fare solo il suo lavoro
set +e
ssh $SSHO pmgr-sync@127.0.0.1 id >/tmp/s1.out 2>&1; rc=$?
set -e
check "shell rifiutata alla chiave sorgente" [ "$rc" != 0 ]
check "nessun output di id" bash -c '! grep -q uid= /tmp/s1.out'

# push reale con rsync
mkdir -p /tmp/src/snippets /tmp/src/scripts
printf 'nuovo\n' >/tmp/src/snippets/vendor_nuovo.yaml
printf 'echo ciao\n' >/tmp/src/scripts/update.sh
set +e
rsync -rltO --no-owner --no-group --chmod=F644,D775 -e "ssh $SSHO" /tmp/src/snippets/ pmgr-sync@127.0.0.1:snippets/ >/tmp/p1.out 2>&1; rc=$?
rsync -rltO --no-owner --no-group --chmod=F644,D775 -e "ssh $SSHO" /tmp/src/scripts/ pmgr-sync@127.0.0.1:pmgr-scripts/ >/tmp/p2.out 2>&1; rc2=$?
set -e
[ "$rc" = 0 ] || cat /tmp/p1.out
[ "$rc2" = 0 ] || cat /tmp/p2.out
check "push snippets riuscito" [ "$rc" = 0 ]
check "push pmgr-scripts riuscito" [ "$rc2" = 0 ]
check "file arrivato in snippets" [ "$(cat /var/lib/vz/snippets/vendor_nuovo.yaml)" = nuovo ]
check "file arrivato in pmgr-scripts" [ "$(cat /var/lib/vz/pmgr-scripts/update.sh)" = 'echo ciao' ]
check "file di proprieta' di pmgr-sync, leggibile da tutti" [ "$(stat -c %U:%a /var/lib/vz/snippets/vendor_nuovo.yaml)" = pmgr-sync:644 ]

# nessuna cancellazione, nemmeno chiedendola
rm -f /tmp/src/snippets/vendor_nuovo.yaml
printf 'altro\n' >/tmp/src/snippets/vendor_b.yaml
set +e
rsync -rltO --delete --no-owner --no-group -e "ssh $SSHO" /tmp/src/snippets/ pmgr-sync@127.0.0.1:snippets/ >/tmp/p3.out 2>&1; rc=$?
set -e
check "--delete rifiutato dal server" [ "$rc" != 0 ]
check "il file della destinazione c'e' ancora" [ -f /var/lib/vz/snippets/vendor_nuovo.yaml ]
check "il file di root c'e' ancora" [ -f /var/lib/vz/snippets/vendor_esistente.yaml ]
set +e
rsync -rltO --no-owner --no-group -e "ssh $SSHO" /tmp/src/snippets/ pmgr-sync@127.0.0.1:snippets/ >/tmp/p4.out 2>&1; rc=$?
set -e
check "push senza --delete riuscito" [ "$rc" = 0 ]
check "i file vecchi sono rimasti" [ -f /var/lib/vz/snippets/vendor_nuovo.yaml ]

# un file di root piu' nuovo sulla destinazione: rsync con file diverso lo sostituisce (rename nella cartella di gruppo)
printf 'sorgente\n' >/tmp/src/snippets/vendor_esistente.yaml
set +e
rsync -rltO --no-owner --no-group -e "ssh $SSHO" /tmp/src/snippets/ pmgr-sync@127.0.0.1:snippets/ >/tmp/p5.out 2>&1; rc=$?
set -e
check "file di root sostituito dalla cartella di gruppo" [ "$(cat /var/lib/vz/snippets/vendor_esistente.yaml)" = sorgente ]

# percorsi fuori dalle due cartelle
set +e
rsync -rltO -e "ssh $SSHO" /tmp/src/snippets/ pmgr-sync@127.0.0.1:/etc/ >/tmp/p6.out 2>&1; rc=$?
rsync -rltO -e "ssh $SSHO" /tmp/src/snippets/ pmgr-sync@127.0.0.1:images/ >/tmp/p7.out 2>&1; rc2=$?
rsync -rltO -e "ssh $SSHO" pmgr-sync@127.0.0.1:snippets/ /tmp/pull/ >/tmp/p8.out 2>&1; rc3=$?
set -e
check "scrittura in /etc rifiutata" [ "$rc" != 0 ]
check "scrittura in images rifiutata" [ "$rc2" != 0 ]
check "lettura (pull) rifiutata" [ "$rc3" != 0 ]
check "/etc non toccato" [ ! -e /etc/vendor_b.yaml ]

# elenco con hash
ssh $SSHO pmgr-sync@127.0.0.1 pmgr-list >/tmp/l1.out 2>&1
check "pmgr-list elenca vendor_b" grep -qP '^[0-9a-f]{64}\t6\t[0-9]+\tsnippets/vendor_b.yaml$' /tmp/l1.out
check "pmgr-list elenca update.sh" grep -qP '^[0-9a-f]{64}\t10\t[0-9]+\tpmgr-scripts/update.sh$' /tmp/l1.out

echo "Integrazione: $PASS riusciti, $FAIL falliti"
[ "$FAIL" = 0 ]
