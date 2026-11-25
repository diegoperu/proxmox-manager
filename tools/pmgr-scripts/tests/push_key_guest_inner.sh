#!/bin/bash
# Dentro un container che fa da nodo Proxmox E da "VM": un qm finto esegue lo script nel container stesso.
set -u
. /etc/os-release
export DEBIAN_FRONTEND=noninteractive
case "${ID_LIKE:-}$ID" in
  *debian* | *ubuntu*) apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq python3 openssh-client passwd >/dev/null 2>&1 ;;
  *) bash /w/rocky_repo.sh; dnf -y -q install python3 openssh-clients shadow-utils >/dev/null 2>&1 ;;
esac
echo "### $WANT | $PRETTY_NAME"
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); echo "   FALLITO: $1"; }
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then ok; else ko "$d"; fi; }
mkdir -p /stub
cat >/stub/qm <<'STUB'
#!/bin/bash
case "$1 $2" in
  "status "*) echo "status: ${QM_STATE:-running}" ;;
  "agent "*) [ "${QM_AGENT:-up}" = up ] ;;
  "guest exec")
    shift 3; while [ "$1" != "--" ]; do shift; done; shift     # salta: <vmid> --timeout N --
    out="$("$@" 2>&1)"; rc=$?
    python3 -c 'import json,sys; print(json.dumps({"exitcode": int(sys.argv[1]), "out-data": sys.argv[2]}))' "$rc" "$out" ;;
esac
STUB
chmod 755 /stub/qm; export PATH=/stub:$PATH
useradd -m -s /bin/bash tecnico 2>/dev/null
ssh-keygen -q -t ed25519 -N '' -C 'prova@test' -f /tmp/k; ssh-keygen -q -t ed25519 -N '' -C 'altro@test' -f /tmp/k2
S=/w/pmgr-push-key-guest.sh
F=/home/tecnico/.ssh/authorized_keys
run() { bash $S "$@" >/tmp/out 2>&1; RC=$?; }

echo "-- sola lettura"
run --key-file /tmp/k.pub 101
[ "$RC" = 0 ] && ok || ko "sola lettura: uscita $RC"
check "sola lettura: non crea .ssh" test ! -e /home/tecnico/.ssh
check "sola lettura: lo dice" grep -q 'Sola lettura: nessuna modifica' /tmp/out
check "sola lettura: stato dell'utente" grep -q 'authorized_keys: non esiste' /tmp/out

echo "-- apply: .ssh e file mancanti"
run --key-file /tmp/k.pub --apply 101
[ "$RC" = 0 ] && ok || ko "apply: uscita $RC: $(tail -3 /tmp/out | tr '\n' ' ')"
check "chiave aggiunta" grep -qF "$(cut -d' ' -f2 /tmp/k.pub)" $F
check "authorized_keys 600 di tecnico" bash -c "[ \"\$(stat -c '%a %U' $F)\" = '600 tecnico' ]"
check ".ssh 700 di tecnico" bash -c "[ \"\$(stat -c '%a %U' /home/tecnico/.ssh)\" = '700 tecnico' ]"
check "una riga sola" bash -c "[ \$(wc -l <$F) = 1 ]"

echo "-- apply di nuovo: idempotente"
cp $F /tmp/before
run --key-file /tmp/k.pub --apply 101
check "seconda volta: gia' presente" grep -q "gia' presente" /tmp/out
check "file invariato" bash -c "[ \"\$(sha256sum <$F)\" = \"\$(sha256sum </tmp/before)\" ]"

echo "-- file senza ritorno a capo finale e altra chiave"
printf '%s' "$(cat /tmp/k2.pub)" >$F      # niente \n finale
run --key-file /tmp/k.pub --apply 101
[ "$RC" = 0 ] && ok || ko "senza a-capo: uscita $RC"
check "le due chiavi su righe separate" bash -c "[ \$(wc -l <$F) = 2 ] && grep -qxF \"\$(cat /tmp/k2.pub)\" $F && grep -qxF \"\$(cat /tmp/k.pub)\" $F"
check "copia di sicurezza accanto al file" bash -c "ls /home/tecnico/.ssh/authorized_keys.pmgr-bak.* >/dev/null"
check "la copia ha il contenuto originale" bash -c "grep -qxF \"\$(cat /tmp/k2.pub)\" \$(ls /home/tecnico/.ssh/authorized_keys.pmgr-bak.* | tail -1)"
check "lo dice" grep -q 'manca il ritorno a capo' /tmp/out

echo "-- permessi rovinati e proprietario sbagliato"
chmod 666 $F; chown root:root $F; chmod 777 /home/tecnico/.ssh
run --key-file /tmp/k.pub --apply 101
check "permessi e proprietario ripristinati" bash -c "[ \"\$(stat -c '%a %U' $F)\" = '600 tecnico' ] && [ \"\$(stat -c '%a %U' /home/tecnico/.ssh)\" = '700 tecnico' ]"

echo "-- link simbolici: non si seguono"
rm -rf /home/tecnico/.ssh; mkdir /tmp/altrove; ln -s /tmp/altrove /home/tecnico/.ssh
run --key-file /tmp/k.pub --apply 101
[ "$RC" = 1 ] && ok || ko ".ssh link simbolico: uscita $RC (attesa 1)"
check ".ssh link simbolico: niente scritto altrove" bash -c "[ -z \"\$(ls -A /tmp/altrove)\" ]"
check ".ssh link simbolico: messaggio" grep -q 'link simbolici' /tmp/out
rm -f /home/tecnico/.ssh; mkdir -m 700 /home/tecnico/.ssh; chown tecnico /home/tecnico/.ssh; ln -s /tmp/altro_file /home/tecnico/.ssh/authorized_keys
run --key-file /tmp/k.pub --apply 101
[ "$RC" = 1 ] && check "authorized_keys link simbolico: niente scritto" test ! -e /tmp/altro_file || ko "authorized_keys link simbolico: uscita $RC"
rm -rf /home/tecnico/.ssh

echo "-- utente inesistente e VM non pronte"
run --key-file /tmp/k.pub --user nessuno --apply 101
[ "$RC" = 1 ] && grep -q 'non esiste' /tmp/out && ok || ko "utente inesistente: uscita $RC"
QM_STATE=stopped run --key-file /tmp/k.pub --apply 101
[ "$RC" = 1 ] && grep -q "non e' accesa" /tmp/out && ok || ko "VM spenta: uscita $RC"
QM_AGENT=down run --key-file /tmp/k.pub --apply 101
[ "$RC" = 1 ] && grep -q 'agent non risponde' /tmp/out && ok || ko "agent giu': uscita $RC"
run --key-file /tmp/k.pub --apply 101 102
check "due VM: entrambe trattate" bash -c "[ \$(grep -c '^=== VM' /tmp/out) = 2 ]"

echo "-- argomenti ostili: uscita 2, niente eseguito"
rm -rf /home/tecnico/.ssh
bad() { run "$@"; [ "$RC" = 2 ] && ok || ko "argomenti ostili accettati: $* (uscita $RC)"; }
printf 'ssh-ed25519 AAAA;rm -rf /\n' >/tmp/ko1; bad --key-file /tmp/ko1 101
printf 'ssh-ed25519 AAAAC3 commento con spazi e "apici"\n' >/tmp/ko2; bad --key-file /tmp/ko2 101
printf 'command="id" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA\n' >/tmp/ko3; bad --key-file /tmp/ko3 101
cat /tmp/k.pub /tmp/k2.pub >/tmp/ko4; bad --key-file /tmp/ko4 101
printf 'tipo-strano AAAA\n' >/tmp/ko5; bad --key-file /tmp/ko5 101
ln -s /tmp/k.pub /tmp/ko6; bad --key-file /tmp/ko6 101
bad --key-file /tmp/k.pub 'x;id'
bad --key-file /tmp/k.pub -1
bad --key-file /tmp/k.pub 12
bad --key-file /tmp/k.pub --user 'a;b' 101
bad --key-file /tmp/k.pub --user '-oX' 101
bad --key-file /tmp/nonesiste 101
bad --key-file /tmp/k.pub
bad --bogus 101
check "argomenti ostili: nessun file creato" test ! -e /home/tecnico/.ssh
echo "   RISULTATO $WANT: $PASS ok, $FAIL falliti"
[ "$FAIL" = 0 ]
