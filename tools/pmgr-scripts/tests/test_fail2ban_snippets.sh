#!/bin/bash
# Verifica la configurazione di fail2ban degli snippet:
#  1. (sempre) lo YAML e' valido, senza chiavi duplicate, e contiene la configurazione attesa;
#  2. (con podman e le immagini) esegue i comandi dello snippet con il fail2ban VERO di ogni
#     distribuzione e controlla cosa viene caricato (jail sshd avviata, 5 tentativi / 15 minuti,
#     ban 1 ora, ban progressivo, memoria delle recidive).
# Uso: test_fail2ban_snippets.sh [--no-containers]
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }

declare -A IMG=(
  [debian-12]=docker.io/library/debian:12
  [debian-13]=docker.io/library/debian:13
  [ubuntu-22.04]=docker.io/library/ubuntu:22.04
  [ubuntu-24.04]=docker.io/library/ubuntu:24.04
  [rocky-8]=docker.io/library/rockylinux:8
  [rocky-9]=docker.io/library/rockylinux:9 [ubuntu-26.04]=docker.io/library/ubuntu:26.04 [rocky-10]=docker.io/rockylinux/rockylinux:10
)
IDS=(debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10)

# ---- parte 1: YAML e contenuto, per tutte le release
for id in "${IDS[@]}"; do
  python3 - "$SRC/snippets.d/$id.yaml" "$WORK/$id.cmds.bin" <<'PYEND' || { ko "$id: YAML o contenuto non valido"; continue; }
import re, sys, yaml

class Loader(yaml.SafeLoader):
    pass

def no_dup(loader, node, deep=False):
    keys = set()
    for k, _ in node.value:
        key = loader.construct_object(k, deep=deep)
        if key in keys:
            raise yaml.constructor.ConstructorError(None, None, "chiave duplicata: %r" % key, k.start_mark)
        keys.add(key)
    return yaml.SafeLoader.construct_mapping(loader, node, deep)

Loader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, no_dup)
doc = yaml.load(open(sys.argv[1]), Loader=Loader)
cmds = [c for c in doc["runcmd"] if isinstance(c, str)]
f2b = [c for c in cmds if ("/etc/fail2ban" in c or re.search(r"systemctl \w+ fail2ban", c))]
txt = "\n".join(f2b)
need = ["bantime = 1h", "findtime = 15m", "maxretry = 5", "bantime.increment = true",
        "bantime.multipliers = 1 2 4 8 16 32 64", "bantime.maxtime = 1w", "[sshd]", "enabled = true", "backend = systemd",
        "dbpurgeage = 4w", "systemctl enable fail2ban", "systemctl restart fail2ban"]
missing = [n for n in need if n not in txt]
assert not missing, "manca: %s" % missing
assert "jail.conf" not in txt and "jail.local" not in txt and "1d" not in re.findall(r"bantime = (\S+)", txt), "resti della vecchia configurazione"
# ordine: i file prima di enable/restart; restart prima del reboot finale
idx = {k: next(i for i, c in enumerate(cmds) if k in c) for k in ("jail.d/50-pmgr.local", "fail2ban.d/50-pmgr.local", "systemctl enable fail2ban", "systemctl restart fail2ban")}
assert idx["jail.d/50-pmgr.local"] < idx["systemctl restart fail2ban"] and idx["fail2ban.d/50-pmgr.local"] < idx["systemctl restart fail2ban"] and idx["systemctl enable fail2ban"] < idx["systemctl restart fail2ban"]
assert cmds[-1].strip() == "reboot"
with open(sys.argv[2], "wb") as out:
    for c in f2b:
        out.write(c.encode() + b"\x01")
PYEND
  ok
done
for id in "${IDS[@]}"; do
  grep -q 'backend = systemd' "$SRC/snippets.d/$id.yaml" && ok || ko "$id: manca backend = systemd (senza rsyslog il file di log non esiste e fail2ban non parte)"
done

# ---- parte 2: fail2ban vero in un container per distribuzione
if [ "${1:-}" = "--no-containers" ] || ! command -v podman >/dev/null 2>&1; then
  echo "(parte container saltata)"
else
  cp "$HERE/f2b_inner.sh" "$WORK/"
  cat >"$WORK/rocky_repo.sh" <<'EOF'
# i mirror di Rocky a volte sono incoerenti: si usa il sito ufficiale
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF
  pids=()
  for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
    mkdir -p "$WORK/c-$id"
    cp "$WORK/f2b_inner.sh" "$WORK/rocky_repo.sh" "$WORK/$id.cmds.bin" "$WORK/c-$id/"
    mv "$WORK/c-$id/$id.cmds.bin" "$WORK/c-$id/cmds.bin"
    ( timeout 600 podman run --rm --network=host -e WANT="$id" -v "$WORK/c-$id:/w:ro,Z" "${IMG[$id]}" bash /w/f2b_inner.sh >"$WORK/c-$id.out" 2>&1; echo $? >"$WORK/c-$id.rc" ) &
    pids+=($!)
  done
  wait "${pids[@]}"
  for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
    cat "$WORK/c-$id.out"
    if [ "$(cat "$WORK/c-$id.rc" 2>/dev/null)" = 0 ]; then ok; else ko "$id: la configurazione caricata da fail2ban non e' quella attesa"; fi
  done
  echo "(ubuntu-26.04: nessuna immagine di prova, stesso corpo di ubuntu-24.04)"
fi
printf 'Test fail2ban: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
