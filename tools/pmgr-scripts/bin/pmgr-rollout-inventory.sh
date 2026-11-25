#!/bin/bash
# pmgr-rollout-inventory.sh - prepara gli elenchi di VM per il rollout, leggendo il manager (SOLA LETTURA).
# Non si collega alle VM, non scrive su Proxmox: fa solo GET verso il manager (/api/clusters e /api/vms).
# Solo ASCII.
#
# Uso:  pmgr-rollout-inventory.sh --cluster "CALCOLO" [--manager http://localhost:8080] [--out DIR]
#
# Scrive in DIR (predefinita: ./rollout-<cluster>):
#   inventario.tsv      tutte le VM (vmid, nome, nodo, stato, ip, gpu, ostype, famiglia indovinata dai tag, tag)
#   target.txt          IP delle VM Linux ACCESE, con IP, SENZA scheda GPU          -> rollout normale
#   gpu.txt             IP delle VM accese con scheda GPU                           -> da trattare a parte
#   senza_ip.txt        VM accese ma senza IP in Proxmox (DHCP o manuale): da vedere a mano
#   ferme.txt           VM spente (nome e IP): si fanno quando vengono accese
#   ostype_ignoto.txt   VM accese con ostype non impostato in Proxmox (potrebbero non essere Linux)
# Gli IP in target.txt/gpu.txt sono validati (solo IPv4 a quattro numeri) e senza duplicati.
set -euo pipefail
export LC_ALL=C

MGR="http://localhost:8080"
CLUSTER=""
OUT=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --cluster) [ "$#" -ge 2 ] || { echo "--cluster vuole un valore" >&2; exit 2; }; CLUSTER="$2"; shift 2 ;;
    --manager) [ "$#" -ge 2 ] || { echo "--manager vuole un valore" >&2; exit 2; }; MGR="$2"; shift 2 ;;
    --out) [ "$#" -ge 2 ] || { echo "--out vuole un valore" >&2; exit 2; }; OUT="$2"; shift 2 ;;
    -h | --help) sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "argomento sconosciuto: $1" >&2; exit 2 ;;
  esac
done
[ -n "$CLUSTER" ] || { echo "manca --cluster" >&2; exit 2; }
[[ "$MGR" =~ ^https?://[A-Za-z0-9.:-]+$ ]] || { echo "--manager non valido" >&2; exit 2; }
[[ "$CLUSTER" =~ ^[A-Za-z0-9\ ._-]{1,64}$ ]] || { echo "--cluster: nome non valido" >&2; exit 2; }
for c in curl python3; do command -v "$c" >/dev/null 2>&1 || { echo "manca $c" >&2; exit 1; }; done
[ -n "$OUT" ] || OUT="./rollout-$(printf '%s' "$CLUSTER" | tr -c 'A-Za-z0-9._-' '_')"
mkdir -p -- "$OUT"
chmod 700 "$OUT"

python3 - "$MGR" "$CLUSTER" "$OUT" <<'PYEND'
import json, os, re, sys, urllib.request

mgr, label, out = sys.argv[1:4]
IPV4 = re.compile(r"^(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)$")

def get(path):
    with urllib.request.urlopen(mgr + path, timeout=60) as r:
        return json.load(r)

clusters = get("/api/clusters")
idx = [i for i, c in enumerate(clusters) if c.get("label") == label]
if len(idx) != 1:
    sys.exit("cluster '%s' non trovato (o ambiguo). Disponibili: %s" % (label, ", ".join(c.get("label", "?") for c in clusters)))
nodes = get("/api/vms?cluster=%d" % idx[0])

def clean(s):
    return re.sub(r"[^A-Za-z0-9._ -]", "?", str(s or ""))[:64]

def family(tags):
    t = (tags or "").split(";")
    for f in ("ubuntu", "debian", "rocky"):
        if f + "-template" in t:
            return f
    return "?"

rows = []
for n in nodes:
    for v in n.get("vms", []):
        if v.get("template"):
            continue
        rows.append(dict(vmid=v.get("vmid"), name=clean(v.get("name")), node=clean(n.get("node")), status=v.get("status"),
                         ip=v.get("ip") or "", gpu=len(v.get("gpus") or []), ostype=v.get("ostype") or "", fam=family(v.get("tags")),
                         tags=clean(v.get("tags"))))
rows.sort(key=lambda r: (r["vmid"] if isinstance(r["vmid"], int) else 0))

target, gpu, noip, stopped, unknown, bad = [], [], [], [], [], []
seen = set()
for r in rows:
    if r["status"] != "running":
        stopped.append("%s\t%s\t%s" % (r["name"], r["ip"] or "-", r["vmid"]))
        continue
    if not r["ip"]:
        noip.append("%s\t%s" % (r["name"], r["vmid"]))
        continue
    if not IPV4.match(r["ip"]):
        bad.append("%s\t%s" % (r["name"], r["ip"]))
        continue
    if r["ip"] in seen:
        bad.append("%s\tIP duplicato %s" % (r["name"], r["ip"]))
        continue
    seen.add(r["ip"])
    if not r["ostype"]:
        unknown.append("%s\t%s\t%s" % (r["name"], r["ip"], r["vmid"]))
    (gpu if r["gpu"] else target).append(r["ip"])

def w(name, lines):
    with open(os.path.join(out, name), "w") as f:
        f.write("".join(l + "\n" for l in lines))

w("inventario.tsv", ["vmid\tnome\tnodo\tstato\tip\tgpu\tostype\tfamiglia\ttag"] +
  ["%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s" % (r["vmid"], r["name"], r["node"], r["status"], r["ip"] or "-", r["gpu"], r["ostype"] or "-", r["fam"], r["tags"]) for r in rows])
w("target.txt", target)
w("gpu.txt", gpu)
w("senza_ip.txt", noip + bad)
w("ferme.txt", stopped)
w("ostype_ignoto.txt", unknown)
print("Cluster %s (indice %d): %d VM non template" % (label, idx[0], len(rows)))
print("  accese, con IP, senza GPU -> target.txt   : %d" % len(target))
print("  accese con GPU            -> gpu.txt      : %d" % len(gpu))
print("  accese senza IP / IP non valido -> senza_ip.txt: %d" % len(noip + bad))
print("  spente                    -> ferme.txt    : %d" % len(stopped))
print("  ostype non impostato (da controllare)     : %d" % len(unknown))
PYEND
echo "File in: $OUT"
