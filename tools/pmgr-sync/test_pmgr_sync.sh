#!/bin/bash
# Test di pmgr-sync-setup.sh senza root: validazione degli argomenti e comportamento di
# pmgr-sync-gate con un rrsync finto. Il test di integrazione (utente, cartelle, ssh) e'
# in test_pmgr_sync_container.sh.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP="$HERE/pmgr-sync-setup.sh"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0
FAIL=0

ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }

# expect_exit DESCRIZIONE CODICE comando...
expect_exit() {
  local desc="$1" want="$2" got
  shift 2
  "$@" >"$WORK/out" 2>&1
  got=$?
  if [ "$got" = "$want" ]; then ok; else ko "$desc (uscita $got, attesa $want): $(head -c 200 "$WORK/out" | tr '\n' ' ')"; fi
}

ssh-keygen -q -t ed25519 -N '' -C test -f "$WORK/k1"
ssh-keygen -q -t ed25519 -N '' -C test2 -f "$WORK/k2"
ssh-keygen -q -t rsa -b 2048 -N '' -C rsa -f "$WORK/rsa"
cat "$WORK/k1.pub" "$WORK/k2.pub" >"$WORK/due.pub"
: >"$WORK/vuota.pub"
printf '%s\n' 'ssh-ed25519 AAAA command="rm -rf /" x' >"$WORK/strana.pub"
printf '%s\n' "$(cut -d' ' -f1,2 "$WORK/k1.pub") comment;with;chars" >"$WORK/commento.pub"
ln -s "$WORK/k1.pub" "$WORK/link.pub"

# --- uso ----------------------------------------------------------------------------------
expect_exit "senza argomenti" 2 "$SETUP"
expect_exit "--help" 0 "$SETUP" --help
expect_exit "ruolo sconosciuto" 2 "$SETUP" --role admin --check
expect_exit "opzione sconosciuta" 2 "$SETUP" --role source --boh
expect_exit "dest senza chiave" 2 "$SETUP" --role dest
expect_exit "both senza chiave" 2 "$SETUP" --role both
expect_exit "chiave senza --source-from" 2 "$SETUP" --role dest --source-pubkey "$WORK/k1.pub"
expect_exit "--source-from senza chiave" 2 "$SETUP" --role source --source-from 10.0.0.1
expect_exit "source con --source-pubkey" 2 "$SETUP" --role source --source-pubkey "$WORK/k1.pub" --source-from 10.0.0.1
expect_exit "chiave RSA rifiutata" 2 "$SETUP" --role dest --source-pubkey "$WORK/rsa.pub" --source-from 10.0.0.1
expect_exit "file con due chiavi" 2 "$SETUP" --role dest --source-pubkey "$WORK/due.pub" --source-from 10.0.0.1
expect_exit "file vuoto" 2 "$SETUP" --role dest --source-pubkey "$WORK/vuota.pub" --source-from 10.0.0.1
expect_exit "riga con opzioni infilate" 2 "$SETUP" --role dest --source-pubkey "$WORK/strana.pub" --source-from 10.0.0.1
expect_exit "chiave inesistente" 2 "$SETUP" --role dest --source-pubkey "$WORK/non-c-e.pub" --source-from 10.0.0.1
expect_exit "chiave tramite link simbolico" 2 "$SETUP" --role dest --source-pubkey "$WORK/link.pub" --source-from 10.0.0.1
expect_exit "from con codice dentro" 2 "$SETUP" --role dest --source-pubkey "$WORK/k1.pub" --source-from '10.0.0.1";rm -rf /;"'
expect_exit "from con ottetto 256" 2 "$SETUP" --role dest --source-pubkey "$WORK/k1.pub" --source-from 10.0.0.256
expect_exit "from con maschera 33" 2 "$SETUP" --role dest --source-pubkey "$WORK/k1.pub" --source-from 10.0.0.0/33
expect_exit "from con nome host" 2 "$SETUP" --role dest --source-pubkey "$WORK/k1.pub" --source-from nodo1.example.org
expect_exit "from vuoto" 2 "$SETUP" --role dest --source-pubkey "$WORK/k1.pub" --source-from ''
expect_exit "from con elemento vuoto" 2 "$SETUP" --role dest --source-pubkey "$WORK/k1.pub" --source-from '10.0.0.1,,10.0.0.2'
expect_exit "dev senza --dev-from" 2 "$SETUP" --role source --dev-pubkey "$WORK/k1.pub"
expect_exit "--dev-from senza chiave" 2 "$SETUP" --role source --dev-from 10.0.0.1
expect_exit "dev con piu' indirizzi" 2 "$SETUP" --role source --dev-pubkey "$WORK/k1.pub" --dev-from 10.0.0.1,10.0.0.2
# argomenti validi: arrivano al controllo "root" (uscita 1 se non si e' root), non a un errore d'uso
if [ "$(id -u)" != 0 ]; then
  expect_exit "argomenti validi, non root" 1 "$SETUP" --role dest --source-pubkey "$WORK/k1.pub" --source-from 10.0.0.1,10.1.0.0/24
  expect_exit "commento strano ignorato, non root" 1 "$SETUP" --role dest --source-pubkey "$WORK/commento.pub" --source-from 10.0.0.1
fi

# --- pmgr-sync-gate -----------------------------------------------------------------------
VZ="$WORK/vz"
mkdir -p "$VZ/snippets" "$VZ/pmgr-scripts" "$VZ/images" "$WORK/bin"
printf 'uno\n' >"$VZ/snippets/vendor_a.yaml"
printf 'due\n' >"$VZ/pmgr-scripts/update.sh"
printf 'segreto\n' >"$VZ/images/disco.raw"
printf 'x\n' >"$VZ/snippets/nome strano.yaml"
ln -s "$VZ/images/disco.raw" "$VZ/snippets/link.yaml"

cat >"$WORK/bin/rrsync" <<'STUB'
#!/bin/bash
printf 'ARGS=%s\nCMD=%s\n' "$*" "${SSH_ORIGINAL_COMMAND:-}" >"$STUB_OUT"
STUB
chmod 755 "$WORK/bin/rrsync"
"$SETUP" --print-gate | sed -e "s|@RRSYNC@|$WORK/bin/rrsync|" -e "s|@VZDIR@|$VZ|" >"$WORK/gate"
chmod 755 "$WORK/gate"
export STUB_OUT="$WORK/stub_out"

run_gate() { # comando -> esegue il gate come farebbe sshd
  rm -f "$STUB_OUT"
  SSH_ORIGINAL_COMMAND="$1" "$WORK/gate" >"$WORK/gout" 2>"$WORK/gerr"
}

gate_allows() { # descrizione comando percorso-atteso
  if run_gate "$2"; then
    if grep -q "^ARGS=-wo -no-del $VZ/$3\$" "$STUB_OUT" && grep -q '^CMD=rsync --server .* \. \.$' "$STUB_OUT"; then ok
    else ko "$1: rrsync chiamato in modo inatteso: $(tr '\n' ' ' <"$STUB_OUT")"; fi
  else ko "$1: rifiutato ($(cat "$WORK/gerr"))"; fi
}
gate_denies() { # descrizione comando
  if run_gate "$2"; then ko "$1: accettato"; elif [ "$?" = 126 ] && [ ! -e "$STUB_OUT" ]; then ok
  else ko "$1: uscita inattesa o rrsync chiamato"; fi
}

gate_allows "push su snippets" "rsync --server -vlogDtpre.iLsfxCIvu . snippets/" snippets
gate_allows "push su snippets senza barra" "rsync --server -vlt . snippets" snippets
gate_allows "push su pmgr-scripts" "rsync --server -vlogDtpre.iLsfxCIvu . pmgr-scripts/" pmgr-scripts
gate_allows "push con opzioni lunghe" "rsync --server -vlt --chmod=F644,D775 --no-owner . snippets/" snippets

gate_denies "comando vuoto" ""
gate_denies "shell qualsiasi" "bash -i"
gate_denies "cat di un file" "cat /etc/passwd"
gate_denies "rsync su altra cartella" "rsync --server -vlt . images/"
gate_denies "rsync fuori cartella" "rsync --server -vlt . snippets/../images"
gate_denies "rsync percorso assoluto" "rsync --server -vlt . /etc/"
gate_denies "rsync in lettura (--sender)" "rsync --server --sender -vlt . snippets/"
gate_denies "rsync con punto e virgola" "rsync --server -vlt . snippets/; id"
gate_denies "rsync con a-capo" $'rsync --server -vlt . snippets/\nid'
gate_denies "rsync con apice" "rsync --server -vlt . 'snippets/'"
gate_denies "rsync con dollaro" 'rsync --server -vlt . $HOME'
gate_denies "rsync senza punto" "rsync --server -vlt snippets/"
gate_denies "rsync senza argomenti" "rsync --server "
gate_denies "prefisso simile" "rsync --serverx -vlt . snippets/"
gate_denies "pmgr-list con argomenti" "pmgr-list --all"
gate_denies "pmgr-list maiuscolo" "PMGR-LIST"

if run_gate "pmgr-list"; then
  out="$(cat "$WORK/gout")"
  if grep -qP "^[0-9a-f]{64}\t4\t[0-9]+\tsnippets/vendor_a.yaml$" <<<"$out"; then ok; else ko "pmgr-list: manca vendor_a.yaml: $out"; fi
  if grep -qP "^[0-9a-f]{64}\t4\t[0-9]+\tpmgr-scripts/update.sh$" <<<"$out"; then ok; else ko "pmgr-list: manca update.sh"; fi
  if grep -q '^INVALID$' <<<"$out"; then ok; else ko "pmgr-list: il nome con spazio non e' segnalato INVALID"; fi
  if grep -q 'disco.raw\|link.yaml\|images' <<<"$out"; then ko "pmgr-list: mostra file fuori dalle due cartelle o un link simbolico"; else ok; fi
  expected="$(printf 'uno\n' | sha256sum | cut -d' ' -f1)"
  if grep -q "^${expected}	" <<<"$out"; then ok; else ko "pmgr-list: hash sbagliato"; fi
else
  ko "pmgr-list rifiutato: $(cat "$WORK/gerr")"
fi

printf 'Test: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
