#!/bin/bash
# Profilo hosting degli snippet: rendering (--render), YAML, fedelta' del contenuto ai file di
# profiles/hosting, ordine dei comandi, assenza di righe che abilitano la password SSH.
# Non richiede root, ne' Proxmox, ne' podman.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
OS="$SRC/bin/pmgr-os.sh"
mkdir -p "$WORK/std" "$WORK/host" "$WORK/cfgprof"
printf 'ssh-ed25519 AAAA t\n' >"$WORK/keys"; printf 'PasswordDiProva1\n' >"$WORK/pw"; chmod 600 "$WORK/pw"
conf() { # [righe extra]
  printf 'VMID_BASE=1000\nSTORAGE=pool\nBRIDGE=lo\nNAMESERVER=10.0.0.1\nSEARCH_DOMAIN=t.local\nCI_USER=tecnico\nSSH_KEYS_FILE=%s\nPASSWORD_FILE=%s\n%s' "$WORK/keys" "$WORK/pw" "${1:-}" >"$WORK/cluster.conf"
  chmod 600 "$WORK/cluster.conf"
}
export PMGR_CLUSTER_CONF="$WORK/cluster.conf"

conf
"$OS" --render "$WORK/std" --all >"$WORK/out" 2>&1 && ok || ko "render standard: $(tail -2 "$WORK/out")"
"$OS" --render "$WORK/host" --profile hosting --all >"$WORK/out" 2>&1 && ok || ko "render hosting: $(tail -2 "$WORK/out")"
[ "$(ls "$WORK/std" | wc -l)" = 8 ] && [ "$(ls "$WORK/host" | wc -l)" = 8 ] && ok || ko "attesi 8 snippet per profilo"

# PROFILE in cluster.conf
conf 'PROFILE=hosting
'
"$OS" --render "$WORK/cfgprof" ubuntu-24.04 >"$WORK/out" 2>&1 && cmp -s "$WORK/cfgprof/vendor_ubuntu_24.04.yaml" "$WORK/host/vendor_ubuntu_24.04.yaml" && ok || ko "PROFILE=hosting in cluster.conf non produce lo snippet di hosting"
conf 'PROFILE=hosting
'
"$OS" --render "$WORK/cfgprof" --profile standard ubuntu-24.04 >"$WORK/out" 2>&1 && cmp -s "$WORK/cfgprof/vendor_ubuntu_24.04.yaml" "$WORK/std/vendor_ubuntu_24.04.yaml" && ok || ko "--profile standard deve prevalere su cluster.conf"
for bad in 'PROFILE=hostng' 'PROFILE=' 'PROFILE=hosting;id' 'PROFILE=Hosting'; do
  conf "$bad
"; "$OS" --render "$WORK/cfgprof" ubuntu-24.04 >"$WORK/out" 2>&1; [ "$?" != 0 ] && ok || ko "PROFILE non valido accettato: $bad"
done
conf
"$OS" --render "$WORK/cfgprof" --profile root ubuntu-24.04 >"$WORK/out" 2>&1; [ "$?" = 2 ] && ok || ko "--profile non valido accettato"
for d in 'relativo' '/non/esiste' '/tmp/../etc' "$WORK/std/vendor_ubuntu_24.04.yaml"; do
  "$OS" --render "$d" ubuntu-24.04 >"$WORK/out" 2>&1; [ "$?" = 2 ] && ok || ko "--render con cartella non valida accettata: $d"
done
ln -s "$WORK/std" "$WORK/linkdir"; "$OS" --render "$WORK/linkdir" ubuntu-24.04 >"$WORK/out" 2>&1; [ "$?" = 2 ] && ok || ko "--render su link simbolico accettato"

python3 - "$SRC" "$WORK" <<'PYEND' || FAIL=$((FAIL + 1))
import difflib, glob, os, re, sys, yaml
src, work = sys.argv[1], sys.argv[2]
fails = 0
def bad(msg):
    global fails
    fails += 1
    print("FALLITO:", msg)
class Loader(yaml.SafeLoader): pass
def no_dup(loader, node, deep=False):
    keys = set()
    for k, _ in node.value:
        key = loader.construct_object(k, deep=deep)
        if key in keys: raise yaml.constructor.ConstructorError(None, None, "chiave duplicata %r" % key, k.start_mark)
        keys.add(key)
    return yaml.SafeLoader.construct_mapping(loader, node, deep)
Loader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, no_dup)
prof = src + "/profiles/hosting/"
srcs = {"/usr/local/sbin/pmgr-ssh-enforce": open(prof + "pmgr-ssh-enforce.sh").read(),
        "/etc/systemd/system/pmgr-ssh-enforce.service": open(prof + "pmgr-ssh-enforce.service").read(),
        "/etc/systemd/system/pmgr-ssh-enforce.timer": open(prof + "pmgr-ssh-enforce.timer").read(),
        "/etc/sysctl.d/90-pmgr-hosting.conf": open(prof + "90-pmgr-hosting-sysctl.conf").read(),
        "/etc/fail2ban/jail.d/60-pmgr-recidive.local": open(prof + "60-pmgr-recidive.local").read()}
n_ok = 0
def kind_is_host(name):
    return True  # in questo ciclo si confronta lo snippet di hosting con quello standard della stessa release
for f in sorted(glob.glob(work + "/std/*.yaml")):
    name = os.path.basename(f)
    std_txt = open(f).read(); host_txt = open(work + "/host/" + name).read()
    try:
        std = yaml.load(std_txt, Loader=Loader); host = yaml.load(host_txt, Loader=Loader)
    except Exception as e:
        bad("%s: YAML non valido: %s" % (name, e)); continue
    sr, hr = std["runcmd"], host["runcmd"]
    # standard: password SSH abilitata (Ubuntu/Debian con sed, Rocky col file nostro), nessun elemento dell'hosting
    s_all = "\n".join(str(x) for x in sr)
    if "PasswordAuthentication yes" not in s_all and "PasswordAuthentication/s/^.*$/PasswordAuthentication yes" not in s_all:
        bad("%s: lo snippet standard non abilita la password SSH" % name)
    if "pmgr-ssh-enforce" in s_all: bad("%s: lo snippet standard contiene l'hosting" % name)
    if "pmgr:ssh-password" in std_txt: bad("%s: restano i marcatori nello standard" % name)
    if name.startswith("vendor_rocky"):
        if "00-pmgr-ssh.conf" not in s_all or "PermitRootLogin no" not in s_all: bad("%s: Rocky standard senza il file sshd con password" % name)
    # hosting: nessuna riga che abilita la password, ne' marcatori
    # lo script pmgr-ssh-enforce nomina quelle parole nelle sue espressioni regolari: va escluso dal controllo
    h_all = "\n".join(str(x) for x in hr if "cat > /usr/local/sbin/pmgr-ssh-enforce <<" not in str(x))
    if re.search(r"(PasswordAuthentication|KbdInteractiveAuthentication|ChallengeResponseAuthentication)[^\n]*\byes\b", h_all):
        bad("%s: l'hosting contiene ancora una riga che abilita la password/tastiera" % name)
    if "pmgr:ssh-password" in host_txt: bad("%s: restano i marcatori nell'hosting" % name)
    if "00-pmgr-ssh.conf" in h_all: bad("%s: l'hosting contiene il file sshd con password" % name)
    # contenuto fedele ai file sorgente
    for dest, text in srcs.items():
        item = next((x for x in hr if isinstance(x, str) and ("cat > %s <<" % dest) in x), None)
        if item is None: bad("%s: manca la scrittura di %s" % (name, dest)); continue
        m = re.search(r"<<'PMGR_EOF'\n(.*)\nPMGR_EOF\s*$", item, re.S)
        if not m or m.group(1) + "\n" != text: bad("%s: il contenuto di %s non coincide con il file sorgente" % (name, dest))
    # D93: strumenti di diagnostica solo nello standard, sysctl e recidive solo nell'hosting
    EXTRA = ("tcpdump", "iptraf", "nethogs", "iftop", "arpwatch", "nfs-common", "cifs-utils", "acct", "nmon")
    if not any(e in s_all for e in EXTRA): bad("%s: lo snippet standard non installa piu' gli strumenti di diagnostica" % name)
    for e in EXTRA:
        if re.search(r"\b" + re.escape(e) + r"\b", h_all): bad("%s: l'hosting installa ancora %s" % (name, e))
    if "pmgr:extra-tools" in std_txt or "pmgr:extra-tools" in host_txt: bad("%s: restano i marcatori extra-tools" % name)
    for must in ("fail2ban", "ufw" if "rocky" not in name else "firewalld", "openssh-server", "qemu-guest-agent"):
        if not re.search(r"\b" + must + r"\b", h_all): bad("%s: nell'hosting manca il pacchetto essenziale %s" % (name, must))
    if "90-pmgr-hosting.conf" in s_all or "60-pmgr-recidive" in s_all: bad("%s: lo standard contiene sysctl o recidive dell'hosting" % name)
    # firewall dell'hosting: solo nell'hosting, 22/80/443, testo identico ai file sorgente, prima di pmgr-ssh-enforce
    fwfile = "firewall-firewalld.sh" if "rocky" in name else "firewall-ufw.sh"
    # il firewall e' incorporato in una sottoshell: un "exit" dello script non deve fermare il resto del runcmd
    fwtext = "(\n" + open(prof + fwfile).read() + ")\n"
    def block(x):
        m = re.match(r"^(.*)\n?$", x, re.S)
        return m.group(1) + "\n"
    fw_h = [x for x in hr if isinstance(x, str) and ("ufw allow 80/tcp" in x or "--add-service=http" in x)]
    fw_s = [x for x in sr if isinstance(x, str) and ("ufw allow 80/tcp" in x or "--add-service=http" in x)]
    if kind_is_host(name):
        if len(fw_h) != 1: bad("%s: serve esattamente un blocco di firewall dell'hosting (trovati %d)" % (name, len(fw_h)))
        else:
            if fw_h[0] != fwtext: bad("%s: il firewall dello snippet non coincide con profiles/hosting/%s" % (name, fwfile))
            for must in (("set -- 22", 'ufw allow "$p/tcp"', "ufw allow 80/tcp", "ufw allow 443/tcp", "ufw --force enable", "ufw default deny incoming") if "rocky" not in name else ("set -- 22", "--add-service=ssh", "--add-service=http", "--add-service=https", "--remove-service=cockpit", "--remove-service=dhcpv6-client")):
                if must not in fw_h[0]: bad("%s: nel firewall manca '%s'" % (name, must))
            if "rocky" in name and re.search(r"firewall-offline-cmd[^\n]*--zone=public[^\n]*--remove-service", fw_h[0]): bad("%s: firewall-offline-cmd non accetta --remove-service con --zone" % name)
            i_fw = next((i for i, x in enumerate(hr) if x is fw_h[0]), -1)
            i_en = next((i for i, x in enumerate(hr) if isinstance(x, str) and "cat > /usr/local/sbin/pmgr-ssh-enforce <<" in x), -1)
            if not (-1 < i_fw < i_en): bad("%s: il firewall dovrebbe precedere pmgr-ssh-enforce" % name)
    else:
        if fw_s: bad("%s: lo snippet standard (calcolo) non deve aprire 80/443 con il firewall di hosting" % name)
    # ordine: fail2ban, poi enforce, poi daemon-reload, esecuzione, enable, e reboot per ultimo
    def idx(pred):
        for i, x in enumerate(hr):
            if isinstance(x, str) and pred(x): return i
        return -1
    i_f2b = idx(lambda x: "systemctl restart fail2ban" in x)
    i_scr = idx(lambda x: "cat > /usr/local/sbin/pmgr-ssh-enforce <<" in x)
    # ci sono due daemon-reload (aggiornamenti e hosting): conta quello che segue lo script dell'hosting
    i_dr = next((i for i, x in enumerate(hr) if i > i_scr and isinstance(x, str) and x.strip() == "systemctl daemon-reload"), -1)
    i_run = idx(lambda x: x.strip() == "/usr/local/sbin/pmgr-ssh-enforce")
    i_en = idx(lambda x: x.startswith("systemctl enable pmgr-ssh-enforce.service pmgr-ssh-enforce.timer"))
    if not (-1 < i_f2b < i_scr < i_dr < i_run < i_en < len(hr) - 1) or str(hr[-1]).strip() != "reboot":
        bad("%s: ordine dei comandi dell'hosting non corretto (%s)" % (name, [i_f2b, i_scr, i_dr, i_run, i_en, len(hr)]))
    # le prime due ore: l'hosting e' lo standard senza le righe password e con il blocco in piu'
    sm = difflib.SequenceMatcher(None, std_txt.splitlines(), host_txt.splitlines(), autojunk=False)
    kinds = {t for t, *_ in sm.get_opcodes() if t != "equal"}
    if not kinds <= {"insert", "delete"}: bad("%s: differenze inattese tra standard e hosting: %s" % (name, kinds))
    n_ok += 1
print("python: release controllate: %d" % n_ok)
sys.exit(1 if fails else 0)
PYEND
[ "$?" = 0 ] && ok || ko "controlli YAML/contenuto del profilo"

printf 'Test profilo hosting: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
