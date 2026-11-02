#!/usr/bin/env python3
"""Finto Proxmox VE per l'anteprima grafica (solo dati sintetici, solo localhost, sola lettura)."""
import json, math, random, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 18006
rnd = random.Random(42)
NOW = int(time.time())
GB = 1024 ** 3

NODES = {
    "pve1": dict(maxmem=64 * GB, cores=32, status="online", mem=0.58, cpu=0.31, disk=0.46),
    "pve2": dict(maxmem=128 * GB, cores=48, status="online", mem=0.63, cpu=0.44, disk=0.52),
    "pve3": dict(maxmem=64 * GB, cores=32, status="online", mem=0.92, cpu=0.37, disk=0.71),
    "pve4": dict(maxmem=64 * GB, cores=32, status="offline", mem=0, cpu=0, disk=0),
}
NAMES = ["web-lab", "db-test", "ci-runner", "gitea", "nextcloud", "grafana", "prometheus", "mail-relay",
         "dns-int", "vpn-gw", "backup-srv", "k8s-ctl", "k8s-w1", "k8s-w2", "jupyter", "gpu-train",
         "wiki", "monitor", "proxy", "ldap", "redis-cache", "minio", "registry", "build-arm"]

VMS = []
vmid = 100
for node in NODES:
    n = {"pve1": 12, "pve2": 14, "pve3": 12, "pve4": 6}[node]
    for i in range(n):
        vmid += 1 if rnd.random() < .8 else 7
        typ = "lxc" if rnd.random() < .22 else "qemu"
        offline = NODES[node]["status"] != "online"
        run = (not offline) and rnd.random() < .78
        maxmem = rnd.choice([2, 4, 4, 8, 8, 16, 32]) * GB
        VMS.append(dict(type=typ, node=node, vmid=vmid, name=f"{NAMES[(vmid + i) % len(NAMES)]}-{vmid % 100:02d}",
                        status="running" if run else "stopped", maxmem=maxmem,
                        mem=int(maxmem * rnd.uniform(.2, .85)) if run else 0,
                        cpu=round(rnd.uniform(.01, .55), 3) if run else 0,
                        maxdisk=rnd.choice([20, 32, 50, 100, 200]) * GB, template=0,
                        ip=f"10.75.4.{(vmid % 200) + 10}"))
# un caso di prova: la VM 151 e' ferma su pve3
VMS[40].update(vmid=151, name="web-lab-01", node="pve3", status="stopped", mem=0, cpu=0, type="qemu")
VMS.append(dict(type="qemu", node="pve1", vmid=9000, name="rocky-9-template", status="stopped", maxmem=4 * GB,
                mem=0, cpu=0, maxdisk=20 * GB, template=1, ip=""))
seen = set()
for v in VMS:  # VMID univoci: la VM di prova 151 non deve avere omonimi
    while v["vmid"] in seen:
        v["vmid"] += 1000
    seen.add(v["vmid"])
for v in VMS:
    v["disk"] = int(v["maxdisk"] * rnd.uniform(.15, .8)) if v["status"] == "running" else 0


def rrd(node, tf):
    base = NODES[node]
    pts = 96
    out = []
    if base["status"] != "online":
        for i in range(pts // 3):
            t = NOW - (pts - i) * 900
            out.append(dict(time=t, cpu=base["cpu"] or .2 + .05 * math.sin(i / 3), memused=base["maxmem"] * .5,
                            memtotal=base["maxmem"], netin=1e5, netout=8e4, loadavg=.8, iowait=.01))
        return out
    seed = random.Random(node)
    for i in range(pts):
        t = NOW - (pts - i) * 900
        wave = math.sin(i / 9.0 + len(node))
        cpu = max(.02, min(.98, base["cpu"] + .18 * wave + seed.uniform(-.06, .06)))
        up = 1 if (node == "pve3" and i > 70) else 0
        mem = min(.99, base["mem"] - (.22 if node == "pve3" else .06) + .05 * wave + (i - 70) * .012 * up + seed.uniform(-.01, .01))
        out.append(dict(time=t, cpu=round(cpu, 4), memused=int(base["maxmem"] * mem), memtotal=base["maxmem"],
                        netin=seed.uniform(1e5, 9e6), netout=seed.uniform(1e5, 6e6),
                        loadavg=round(cpu * 4, 2), iowait=round(seed.uniform(0, .05), 4),
                        roottotal=100 * GB, rootused=int(100 * GB * base["disk"])))
    return out


def resources():
    res = []
    for n, b in NODES.items():
        res.append(dict(id=f"node/{n}", type="node", node=n, status=b["status"], maxmem=b["maxmem"],
                        mem=int(b["maxmem"] * b["mem"]), maxcpu=b["cores"], cpu=b["cpu"],
                        maxdisk=1000 * GB, disk=int(1000 * GB * b["disk"]), uptime=86400 * 12 if b["mem"] else 0))
    for v in VMS:
        r = {k: v[k] for k in ("type", "node", "vmid", "name", "status", "maxmem", "mem", "cpu", "maxdisk", "disk", "template")}
        r["id"] = f"{v['type']}/{v['vmid']}"
        r["maxcpu"] = 2
        res.append(r)
    return res


def tasks():
    out = []
    spec = [("qmigrate", "pve1", 140, "diego@pve", None, 90), ("vzdump", "pve3", 151, "root@pam", "ERROR: storage 'nas01' non raggiungibile", 3000),
            ("qmclone", "pve1", 213, "diego@pve", "OK", 5400), ("qmsnapshot", "pve2", 118, "diego@pve", "OK", 7200),
            ("qmstart", "pve3", 118, "diego@pve", "OK", 9000), ("qmresize", "pve2", 207, "diego@pve", "OK", 80000)]
    for i, (t, n, vid, user, st, ago) in enumerate(spec):
        s = NOW - ago
        e = None if st is None else s + 40
        d = dict(upid=f"UPID:{n}:0000{i}:{s:x}:{t}:{vid}:{user}:", node=n, pid=1000 + i, pstart=s, starttime=s,
                 type=t, id=str(vid), user=user)
        if e:
            d.update(endtime=e, status=st)
        out.append(d)
    return out


def route(path):
    p = path.rstrip("/")
    segs = p.split("/")
    if p == "/cluster/resources": return resources()
    if p == "/cluster/status":
        return [dict(type="cluster", name="polimi-deib", nodes=4, quorate=1, version=7)] + \
               [dict(type="node", name=n, online=1 if b["status"] == "online" else 0, id=f"node/{n}") for n, b in NODES.items()]
    if p == "/cluster/tasks": return tasks()
    if p == "/cluster/nextid": return "214"
    if p == "/nodes":
        return [dict(node=n, status=b["status"], maxmem=b["maxmem"], mem=int(b["maxmem"] * b["mem"]), cpu=b["cpu"],
                     maxcpu=b["cores"], uptime=86400 * 12 if b["mem"] else 0, maxdisk=1000 * GB, disk=int(1000 * GB * b["disk"]))
                for n, b in NODES.items()]
    if len(segs) >= 3 and segs[1] == "nodes":
        node = segs[2]
        if node not in NODES: return None
        rest = "/".join(segs[3:])
        b = NODES[node]
        if rest == "rrddata": return rrd(node, "day")
        if rest == "status":
            return dict(cpu=b["cpu"], memory=dict(used=int(b["maxmem"] * b["mem"]), total=b["maxmem"]), uptime=86400 * 12,
                        loadavg=["1.2", "1.1", "0.9"], pveversion="pve-manager/8.2.4", kversion="Linux 6.8.8-2-pve",
                        cpuinfo=dict(cpus=b["cores"], model="AMD EPYC 7313"))
        if rest == "qemu": return [x for x in VMS if x["node"] == node and x["type"] == "qemu"]
        if rest == "lxc": return [x for x in VMS if x["node"] == node and x["type"] == "lxc"]
        if rest == "storage":
            return [dict(storage="local-lvm", type="lvmthin", total=900 * GB, used=int(900 * GB * b["disk"]), avail=900 * GB, active=1, content="images,rootdir")]
        if rest == "network": return [dict(iface="vmbr0", type="bridge", active=1, address="10.75.4.%d" % (10 + len(node)))]
        seg = segs[3:]
        if len(seg) >= 3 and seg[0] in ("qemu", "lxc"):
            v = next((x for x in VMS if x["vmid"] == int(seg[1])), None)
            if not v: return None
            if seg[2] == "status" and len(seg) > 3 and seg[3] == "current":
                return dict(status=v["status"], mem=v["mem"], maxmem=v["maxmem"], cpu=v["cpu"], uptime=3600 * 30, name=v["name"], vmid=v["vmid"])
            if seg[2] == "config":
                key = "ipconfig0" if seg[0] == "qemu" else "net0"
                val = f"ip={v['ip']}/24,gw=10.75.4.254" if v["ip"] else ""
                return {"name": v["name"], "cores": 2, "memory": v["maxmem"] // (1024 * 1024), key: val} if val else {"name": v["name"]}
            if seg[2] == "agent": return dict(result=[dict(mountpoint="/", **{"used-bytes": v["disk"], "total-bytes": v["maxdisk"]}, type="ext4", name="vda1")])
            if seg[2] == "rrddata":
                pts = rrd(node, "day")
                for pt in pts:  # campi per-VM: mem/maxmem (non memused/memtotal)
                    pt["mem"] = int(v["maxmem"] * min(.95, max(.05, v["mem"] / max(v["maxmem"], 1) + (pt["cpu"] - .3) * .1)))
                    pt["maxmem"] = v["maxmem"]
                    pt["cpu"] = round(min(.95, v["cpu"] * (0.6 + pt["cpu"])), 4)
                return pts
    return None


class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def do_GET(self):
        path = urlparse(self.path).path
        if not path.startswith("/api2/json"):
            return self.send_error(404)
        data = route(path[len("/api2/json"):])
        if data is None:
            return self.send_error(404)
        body = json.dumps({"data": data}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

    def do_POST(self): self.send_error(405, "anteprima in sola lettura")
    do_PUT = do_DELETE = do_POST


ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
