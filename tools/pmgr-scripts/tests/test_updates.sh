#!/bin/bash
# Aggiornamenti di sicurezza automatici degli snippet: contenuto, e prova con i pacchetti VERI in container.
# Uso: test_updates.sh [--no-containers] [--functional]
#   --functional  su debian-13, ubuntu-24.04 e rocky-9 esegue un giro REALE di aggiornamenti (scarica pacchetti)
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
NOCONT=0; FUNC=0
for a in "$@"; do case "$a" in --no-containers) NOCONT=1 ;; --functional) FUNC=1 ;; esac; done

printf 'ssh-ed25519 AAAA t\n' >"$WORK/keys"; printf 'PasswordDiProva1\n' >"$WORK/pw"; chmod 600 "$WORK/pw"
printf 'VMID_BASE=1000\nSTORAGE=pool\nBRIDGE=lo\nNAMESERVER=10.0.0.1\nSEARCH_DOMAIN=t.local\nCI_USER=tecnico\nSSH_KEYS_FILE=%s\nPASSWORD_FILE=%s\n' "$WORK/keys" "$WORK/pw" >"$WORK/cluster.conf"
chmod 600 "$WORK/cluster.conf"
export PMGR_CLUSTER_CONF="$WORK/cluster.conf"
mkdir -p "$WORK/std" "$WORK/host"
"$SRC/bin/pmgr-os.sh" --render "$WORK/std" --all >/dev/null 2>&1 && "$SRC/bin/pmgr-os.sh" --render "$WORK/host" --profile hosting --all >/dev/null 2>&1 && ok || { ko "render"; exit 1; }

# ---- 1. contenuto, per tutte le release e per entrambi i profili
python3 - "$SRC" "$WORK" <<'PYEND' || FAIL=$((FAIL + 1))
import glob, os, re, sys, yaml
src, work = sys.argv[1:3]
prof = src + "/profiles/updates/"
read = lambda n: open(prof + n).read()
bad_n = 0
def bad(m):
    global bad_n; bad_n += 1; print("FALLITO:", m)
files = {"/usr/local/sbin/pmgr-reboot-check": read("pmgr-reboot-check.sh"), "/etc/profile.d/pmgr-reboot-required.sh": read("pmgr-reboot-required.sh")}
extra = {"ubuntu": {"/etc/apt/apt.conf.d/52pmgr-unattended-upgrades": "apt-ubuntu.conf", "/etc/systemd/system/apt-daily.timer.d/50-pmgr.conf": "apt-daily.timer.conf", "/etc/systemd/system/apt-daily-upgrade.timer.d/50-pmgr.conf": "apt-daily-upgrade.timer.conf"},
         "debian": {"/etc/apt/apt.conf.d/52pmgr-unattended-upgrades": "apt-debian.conf", "/etc/systemd/system/apt-daily.timer.d/50-pmgr.conf": "apt-daily.timer.conf", "/etc/systemd/system/apt-daily-upgrade.timer.d/50-pmgr.conf": "apt-daily-upgrade.timer.conf"},
         "rocky": {"/etc/systemd/system/dnf-automatic.timer.d/50-pmgr.conf": "dnf-automatic.timer.conf", "/etc/systemd/system/dnf-automatic.service.d/50-pmgr.conf": "dnf-automatic.service.conf"}}
class L(yaml.SafeLoader): pass
def nd(l, n, deep=False):
    k = set()
    for a, _ in n.value:
        key = l.construct_object(a, deep=deep)
        if key in k: raise yaml.constructor.ConstructorError(None, None, "chiave duplicata %r" % key, a.start_mark)
        k.add(key)
    return yaml.SafeLoader.construct_mapping(l, n, deep)
L.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, nd)
for kind in ("std", "host"):
    for f in sorted(glob.glob("%s/%s/*.yaml" % (work, kind))):
        name = kind + "/" + os.path.basename(f)
        try: items = yaml.load(open(f), Loader=L)["runcmd"]
        except Exception as e: bad("%s: YAML non valido: %s" % (name, e)); continue
        items = [x for x in items if isinstance(x, str)]
        txt = "\n".join(items)
        fam = "ubuntu" if "ubuntu" in f else "debian" if "debian" in f else "rocky"
        if "auto_updates" in txt or "tecnico /usr/bin/unattended-upgrade" in txt: bad("%s: resta il vecchio cron come utente non root" % name)
        wanted = dict(files)
        wanted.update({d: read(s) for d, s in extra[fam].items()})
        for dest, text in wanted.items():
            it = next((x for x in items if ("cat > %s <<" % dest) in x), None)
            if it is None: bad("%s: manca la scrittura di %s" % (name, dest)); continue
            m = re.search(r"<<'PMGR_EOF'\n(.*)\nPMGR_EOF\s*$", it, re.S)
            if not m or m.group(1) + "\n" != text: bad("%s: contenuto di %s diverso dal file sorgente" % (name, dest))
        if fam == "rocky":
            it = next((x for x in items if "sed -i -E -f - /etc/dnf/automatic.conf" in x), None)
            m = it and re.search(r"<<'PMGR_EOF'\n(.*)\nPMGR_EOF\s*$", it, re.S)
            if not m or m.group(1) + "\n" != read("dnf-automatic.sed"): bad("%s: lo script sed di dnf-automatic non coincide" % name)
        # pacchetti
        inst = " ".join(x for x in items if x.startswith(("apt install", "sudo dnf install")) or "install -y" in x)
        need = {"debian": "unattended-upgrades", "ubuntu": "unattended-upgrades", "rocky": "dnf-automatic"}[fam]
        if need not in inst: bad("%s: il pacchetto %s non e' installato" % (name, need))
        if fam == "rocky" and "dnf-utils" not in inst: bad("%s: manca dnf-utils (needs-restarting)" % name)
        # ordine: scrittura prima di daemon-reload e enable, tutto prima del reboot finale
        def idx(p):
            for i, x in enumerate(items):
                if p(x): return i
            return -1
        i_w = idx(lambda x: "cat > /usr/local/sbin/pmgr-reboot-check <<" in x)
        i_dr = idx(lambda x: x.strip() == "systemctl daemon-reload")
        i_en = idx(lambda x: x.startswith("systemctl enable apt-daily.timer") or x.startswith("systemctl enable dnf-automatic.timer"))
        if not (0 < i_w < i_dr < i_en < len(items) - 1) or items[-1].strip() != "reboot": bad("%s: ordine dei comandi non corretto %s" % (name, [i_w, i_dr, i_en, len(items)]))
        if kind == "host" and idx(lambda x: "pmgr-ssh-enforce" in x) < i_en: bad("%s: nell'hosting pmgr-ssh-enforce dovrebbe seguire gli aggiornamenti" % name)
        # mai riavvio automatico
        if re.search(r"Automatic-Reboot\s+\"true\"|reboot\s*=\s*when-needed|reboot\s*=\s*when-changed", txt + "".join(files.values()) + "".join(read(s) for d in extra.values() for s in d.values())): bad("%s: riavvio automatico abilitato" % name)
print("python: %d snippet controllati" % len(glob.glob(work + "/*/*.yaml")))
sys.exit(1 if bad_n else 0)
PYEND
[ "$?" = 0 ] && ok || ko "controlli di contenuto"

# ---- 2. container con i pacchetti veri
if [ "$NOCONT" = 1 ] || ! command -v podman >/dev/null 2>&1; then
  echo "(parte container saltata)"
else
  declare -A IMG=( [debian-12]=docker.io/library/debian:12 [debian-13]=docker.io/library/debian:13
    [ubuntu-22.04]=docker.io/library/ubuntu:22.04 [ubuntu-24.04]=docker.io/library/ubuntu:24.04
    [rocky-8]=docker.io/library/rockylinux:8 [rocky-9]=docker.io/library/rockylinux:9 [ubuntu-26.04]=docker.io/library/ubuntu:26.04 [rocky-10]=docker.io/rockylinux/rockylinux:10 )
  cat >"$WORK/rocky_repo.sh" <<'EOF'
sed -i 's|^mirrorlist=|#mirrorlist=|; s|^#baseurl=http://dl.rockylinux.org/\$contentdir|baseurl=https://dl.rockylinux.org/pub/rocky|' /etc/yum.repos.d/*.repo
dnf clean all >/dev/null 2>&1
EOF
  pids=()
  for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
    mkdir -p "$WORK/c-$id"
    snip="$(ls "$WORK/std" | grep -E "$(sed -E 's/^debian-12$/^vendor_debian\\.yaml$/; s/^debian-13$/vendor_debian_13/; s/^ubuntu-22.04$/^vendor_ubuntu\\.yaml$/; s/^ubuntu-24.04$/vendor_ubuntu_24/; s/^rocky-8$/vendor_rocky_8/; s/^rocky-9$/vendor_rocky_9/; s/^ubuntu-26.04$/vendor_ubuntu_26/; s/^rocky-10$/vendor_rocky_10/' <<<"$id")" | head -1)"
    python3 - "$WORK/std/$snip" "$WORK/c-$id/cmds.bin" <<'PYEND' || { ko "$id: estrazione"; continue; }
import sys, yaml
items = [c for c in yaml.safe_load(open(sys.argv[1]))["runcmd"] if isinstance(c, str)]
start = next(i for i, c in enumerate(items) if "cat > /usr/local/sbin/pmgr-reboot-check <<" in c)
end = next(i for i, c in enumerate(items) if c.startswith(("systemctl enable apt-daily.timer", "systemctl enable dnf-automatic.timer")))
open(sys.argv[2], "wb").write(b"".join(c.encode() + b"\x01" for c in items[start:end + 1]))
PYEND
    cp "$HERE/updates_inner.sh" "$WORK/rocky_repo.sh" "$WORK/c-$id/"
    func=0; if [ "$FUNC" = 1 ]; then case "$id" in debian-13 | ubuntu-24.04 | rocky-9) func=1 ;; esac; fi
    ( timeout 1700 podman run --rm --network=host -e WANT="$id" -e FUNCTIONAL="$func" -v "$WORK/c-$id:/w:ro,Z" "${IMG[$id]}" bash /w/updates_inner.sh >"$WORK/c-$id.out" 2>&1; echo $? >"$WORK/c-$id.rc" ) &
    pids+=($!)
  done
  wait "${pids[@]}"
  for id in debian-12 debian-13 ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 rocky-8 rocky-9 rocky-10; do
    cat "$WORK/c-$id.out"
    [ "$(cat "$WORK/c-$id.rc" 2>/dev/null)" = 0 ] && ok || ko "$id: aggiornamenti automatici non conformi"
  done
fi
printf 'Test aggiornamenti automatici: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
