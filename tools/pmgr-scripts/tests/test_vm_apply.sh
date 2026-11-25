#!/bin/bash
# pmgr-vm-apply.sh e pmgr-vm-rollout.sh: argomenti, payload, e catena completa in container (6 distribuzioni).
# Uso: test_vm_apply.sh [--no-containers]
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
ROLL="$SRC/bin/pmgr-vm-rollout.sh"; APPLY="$SRC/bin/pmgr-vm-apply.sh"

# ---- ssh finto: salva argomenti e payload, l'esito dipende dal nome host
mkdir -p "$WORK/bin"
cat >"$WORK/bin/ssh" <<'STUB'
#!/bin/bash
echo "ssh $*" >>"$STUB_LOG"
cat >"$STUB_PAYLOAD"
target="${@: -2:1}"
case "$target" in *down*) exit 255 ;; *bad*) exit 1 ;; *todo*) exit 3 ;; *) exit 0 ;; esac
STUB
chmod 755 "$WORK/bin/ssh"
export PATH="$WORK/bin:$PATH" STUB_LOG="$WORK/ssh.log" STUB_PAYLOAD="$WORK/payload.sh"
r() { : >"$STUB_LOG"; rm -f "$STUB_PAYLOAD"; "$ROLL" "$@" >"$WORK/out" 2>&1; RC=$?; }
r --module updates a.example.org b.example.org
[ "$RC" = 0 ] && [ "$(grep -c '^ssh' "$STUB_LOG")" = 2 ] && grep -q 'sudo -n bash -s -- --module updates' "$STUB_LOG" && ok || ko "rollout semplice (uscita $RC)"
bash -n "$STUB_PAYLOAD" && ok || ko "il payload non e' uno script valido"
r --module baseline --check --allow-no-keys host-todo host-ok
[ "$RC" = 3 ] && grep -q 'host-todo *da aggiornare' "$WORK/out" && grep -q -- '--module baseline --check --allow-no-keys' "$STUB_LOG" && ok || ko "rollout --check (uscita $RC)"
r --verify --profile hosting host-ok
[ "$RC" = 0 ] && grep -q -- '--verify --profile hosting' "$STUB_LOG" && grep -q 'verifica, profilo hosting' "$WORK/out" && ok || ko "rollout --verify (uscita $RC)"
r --verify host-ok
grep -q -- '--verify --profile standard' "$STUB_LOG" && ok || ko "--verify senza profilo deve usare standard"
r --module ssh-hosting host-down host-bad host-ok
[ "$RC" = 1 ] && grep -q 'host-down *IRRAGGIUNGIBILE' "$WORK/out" && grep -q 'host-bad *ERRORE' "$WORK/out" && grep -q 'host-ok *ok' "$WORK/out" && ok || ko "rollout con errori (uscita $RC)"
# il payload contiene davvero gli script e i profili
mkdir -p "$WORK/x"; sed -n "/^base64 -d <<'PMGR_B64'/,/^PMGR_B64$/p" "$STUB_PAYLOAD" | sed '1d;$d' | base64 -d | tar -xzf - -C "$WORK/x" 2>/dev/null
for f in bin/pmgr-vm-apply.sh bin/pmgr-fail2ban-update.sh profiles/updates/pmgr-reboot-check.sh profiles/hosting/pmgr-ssh-enforce.sh profiles/updates/dnf-automatic.sed; do
  cmp -s "$WORK/x/$f" "$SRC/$f" && ok || ko "payload: $f assente o diverso dall'originale"
done
# il file SSH standard dello snippet Rocky e' identico a quello del profilo usato per le VM esistenti
for rk in rocky-8 rocky-9; do
  python3 - "$SRC/snippets.d/$rk.yaml" "$SRC/profiles/ssh-standard/00-pmgr-ssh.conf" <<'PYEND2' && ok || ko "$rk: il file SSH dello snippet non coincide con profiles/ssh-standard"
import re, sys, yaml
body = open(sys.argv[1]).read()
items = [c for c in yaml.safe_load(body)["runcmd"] if isinstance(c, str)]
it = next(x for x in items if "cat > /etc/ssh/sshd_config.d/00-pmgr-ssh.conf <<" in x)
m = re.search(r"<<'EOF'\n(.*)\nEOF\s*$", it, re.S)
sys.exit(0 if m and m.group(1) + "\n" == open(sys.argv[2]).read() else 1)
PYEND2
done
for bad in 'x;rm -rf /' '-oProxyCommand=id' 'a b' '$(id)' '../x'; do
  : >"$STUB_LOG"; "$ROLL" --module updates "$bad" >"$WORK/out" 2>&1; rc=$?
  [ "$rc" = 2 ] && [ ! -s "$STUB_LOG" ] && ok || ko "host ostile accettato: '$bad'"
done
"$ROLL" --module updates --user 'a;b' h >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "utente ostile accettato"
"$ROLL" h >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "senza --module"
"$ROLL" --module nope h >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "modulo sconosciuto"
"$ROLL" --module all h >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "'all' non esiste piu': ssh-hosting non deve poter finire in un gruppo per errore"
"$ROLL" --verify --module updates h >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "--verify con --module accettato"
"$ROLL" --module updates --profile hosting h >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "--profile senza --verify accettato"
"$ROLL" --verify --profile bogus h >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "profilo sconosciuto accettato"
"$ROLL" --module updates >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "senza host"
# non root / opzioni
"$APPLY" --module updates >"$WORK/out" 2>&1; [ "$?" = 1 ] && grep -q 'come root' "$WORK/out" && ok || ko "apply senza root"
"$APPLY" --bogus >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "apply opzione sconosciuta"
"$APPLY" --module all >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "apply: 'all' non deve esistere"
"$APPLY" --verify --module updates >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "apply: --verify con --module"
"$APPLY" --module updates --from /non/esiste >/dev/null 2>&1; [ "$?" = 2 ] && ok || ko "apply --from non valida"
bash -n "$APPLY" && bash -n "$ROLL" && ok || ko "sintassi"

# ---- container: la catena completa con il payload vero
if [ "${1:-}" = "--no-containers" ] || ! command -v podman >/dev/null 2>&1; then
  echo "(parte container saltata)"
else
  r --module updates dummy-ok; cp "$STUB_PAYLOAD" "$WORK/payload.sh"
  declare -A IMG=( [debian-12]=docker.io/library/debian:12 [debian-13]=docker.io/library/debian:13
    [ubuntu-22.04]=docker.io/library/ubuntu:22.04 [ubuntu-24.04]=docker.io/library/ubuntu:24.04
    [rocky-8]=docker.io/library/rockylinux:8 [rocky-9]=docker.io/library/rockylinux:9 [ubuntu-26.04]=docker.io/library/ubuntu:26.04 [rocky-10]=docker.io/rockylinux/rockylinux:10 )
  cat >"$WORK/rocky_repo.sh" <<'EOF'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF
  pids=()
  for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
    [ -z "${PMGR_TEST_ONLY:-}" ] || [ "$PMGR_TEST_ONLY" = "$id" ] || continue
    mkdir -p "$WORK/c-$id"; cp "$HERE/vm_apply_inner.sh" "$WORK/rocky_repo.sh" "$WORK/payload.sh" "$WORK/c-$id/"; cp "$SRC/profiles/hosting/90-pmgr-hosting-sysctl.conf" "$WORK/c-$id/hosting-sysctl.conf"; cp "$SRC/profiles/hosting/60-pmgr-recidive.local" "$WORK/c-$id/hosting-recidive.local"
    ( timeout 900 podman run --rm --network=host -e WANT="$id" -v "$WORK/c-$id:/w:ro,Z" "${IMG[$id]}" bash /w/vm_apply_inner.sh >"$WORK/c-$id.out" 2>&1; echo $? >"$WORK/c-$id.rc" ) &
    pids+=($!)
  done
  wait "${pids[@]}"
  for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
    [ -z "${PMGR_TEST_ONLY:-}" ] || [ "$PMGR_TEST_ONLY" = "$id" ] || continue
    cat "$WORK/c-$id.out"
    [ "$(cat "$WORK/c-$id.rc" 2>/dev/null)" = 0 ] && ok || ko "$id: pmgr-vm-apply non supera gli scenari"
  done
fi
printf 'Test pmgr-vm-apply/rollout: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
