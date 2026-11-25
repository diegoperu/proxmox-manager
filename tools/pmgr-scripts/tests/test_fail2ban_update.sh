#!/bin/bash
# Test di pmgr-fail2ban-update.sh e pmgr-fail2ban-rollout.sh.
#  1. (sempre) il contenuto scritto dallo script e' IDENTICO a quello degli snippet; il rollout valida
#     gli argomenti e riassume gli esiti (con un ssh finto);
#  2. (con podman) in un container per distribuzione, con il fail2ban vero, simulando una VM creata con
#     i vecchi snippet: controllo, aggiornamento, idempotenza, jail.local personalizzato,
#     configurazione rotta e conflitti con ritorno indietro.
# Uso: test_fail2ban_update.sh [--no-containers]
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
UPD="$SRC/bin/pmgr-fail2ban-update.sh"
ROLL="$SRC/bin/pmgr-fail2ban-rollout.sh"

# ---- 1a. contenuto identico a quello degli snippet
for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9; do
  python3 - "$SRC/snippets.d/$id.yaml" "$WORK" <<'PYEND' || { ko "$id: impossibile estrarre la configurazione dallo snippet"; continue; }
import re, sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
items = [c for c in doc["runcmd"] if isinstance(c, str)]
def body(marker):
    it = next(c for c in items if marker in c)
    m = re.search(r"<<'EOF'\n(.*)\nEOF\s*$", it, re.S)
    return m.group(1) + "\n"
open(sys.argv[2] + "/jail.snippet", "w").write(body("jail.d/50-pmgr.local"))
open(sys.argv[2] + "/global.snippet", "w").write(body("fail2ban.d/50-pmgr.local"))
PYEND
  if cmp -s <("$UPD" --print jail) "$WORK/jail.snippet" && cmp -s <("$UPD" --print global) "$WORK/global.snippet"; then ok; else ko "$id: la configurazione dello script differisce da quella dello snippet"; fi
done

# ---- 1b. rollout con ssh finto
mkdir -p "$WORK/bin"
cat >"$WORK/bin/ssh" <<'STUB'
#!/bin/bash
echo "ssh $*" >>"$STUB_LOG"
# ultimo argomento = comando remoto; il nome (penultimo) decide l'esito
target="${@: -2:1}"
case "$target" in
  *down*) exit 255 ;;
  *bad*) cat >/dev/null; exit 1 ;;
  *todo*) cat >/dev/null; exit 3 ;;
  *) cat >/dev/null; exit 0 ;;
esac
STUB
chmod 755 "$WORK/bin/ssh"
export PATH="$WORK/bin:$PATH" STUB_LOG="$WORK/ssh.log"
r() { : >"$STUB_LOG"; "$ROLL" "$@" >"$WORK/out" 2>&1; RC=$?; }
r a.example.org b.example.org
if [ "$RC" = 0 ] && grep -q "a.example.org *ok" "$WORK/out" && [ "$(grep -c '^ssh' "$STUB_LOG")" = 2 ]; then ok; else ko "rollout semplice (uscita $RC)"; fi
r --check host-todo host-ok
if [ "$RC" = 3 ] && grep -q "host-todo *da aggiornare" "$WORK/out" && grep -q 'sudo -n bash -s -- --check' "$STUB_LOG"; then ok; else ko "rollout --check (uscita $RC)"; fi
r host-down host-bad host-ok
if [ "$RC" = 1 ] && grep -q "host-down *IRRAGGIUNGIBILE" "$WORK/out" && grep -q "host-bad *ERRORE" "$WORK/out" && grep -q "host-ok *ok" "$WORK/out"; then ok; else ko "rollout con errori: si deve proseguire e riassumere (uscita $RC)"; fi
r --user tecnico h1
grep -q 'tecnico@h1' "$STUB_LOG" && ok || ko "rollout --user"
printf '# commento\n\nh1\n  h2  # altro\n' >"$WORK/hosts"
r --hosts-file "$WORK/hosts"
[ "$RC" = 0 ] && [ "$(grep -c '^ssh' "$STUB_LOG")" = 2 ] && ok || ko "rollout --hosts-file"
for bad in 'x;rm -rf /' '-oProxyCommand=id' 'a b' '$(id)' '' '../x'; do
  : >"$STUB_LOG"
  "$ROLL" "$bad" >"$WORK/out" 2>&1
  rc=$?
  if [ "$rc" = 2 ] && [ ! -s "$STUB_LOG" ]; then ok; else ko "host ostile accettato: '$bad' (uscita $rc)"; fi
done
"$ROLL" --user 'a;b' h >"$WORK/out" 2>&1; [ "$?" = 2 ] && ok || ko "utente ostile accettato"
"$ROLL" >"$WORK/out" 2>&1; [ "$?" = 2 ] && ok || ko "senza host"
"$UPD" --bogus >"$WORK/out" 2>&1; [ "$?" = 2 ] && ok || ko "update: opzione sconosciuta"
bash -n "$UPD" && bash -n "$ROLL" && ok || ko "sintassi"

# ---- 2. container
if [ "${1:-}" = "--no-containers" ] || ! command -v podman >/dev/null 2>&1; then
  echo "(parte container saltata)"
else
  declare -A IMG=(
    [debian-12]=docker.io/library/debian:12 [debian-13]=docker.io/library/debian:13
    [ubuntu-22.04]=docker.io/library/ubuntu:22.04 [ubuntu-24.04]=docker.io/library/ubuntu:24.04
    [rocky-8]=docker.io/library/rockylinux:8 [rocky-9]=docker.io/library/rockylinux:9 [ubuntu-26.04]=docker.io/library/ubuntu:26.04 [rocky-10]=docker.io/rockylinux/rockylinux:10
  )
  cat >"$WORK/rocky_repo.sh" <<'EOF'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF
  pids=()
  for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
    mkdir -p "$WORK/c-$id"
    cp "$HERE/f2b_update_inner.sh" "$WORK/rocky_repo.sh" "$UPD" "$WORK/c-$id/"
    mode=a; case "$id" in ubuntu-*) mode=b ;; esac
    ( timeout 600 podman run --rm --network=host -e WANT="$id" -e SEDMODE="$mode" -v "$WORK/c-$id:/w:ro,Z" "${IMG[$id]}" bash /w/f2b_update_inner.sh >"$WORK/c-$id.out" 2>&1; echo $? >"$WORK/c-$id.rc" ) &
    pids+=($!)
  done
  wait "${pids[@]}"
  for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
    cat "$WORK/c-$id.out"
    if [ "$(cat "$WORK/c-$id.rc" 2>/dev/null)" = 0 ]; then ok; else ko "$id: scenari di aggiornamento non superati"; fi
  done
fi
printf 'Test aggiornamento fail2ban: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
