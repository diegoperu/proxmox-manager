#!/usr/bin/env python3
"""Finto Proxmox VE per l'anteprima grafica (solo dati sintetici, solo localhost, sola lettura)."""
import hashlib, json, math, os, random, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 18006
rnd = random.Random(42)
NOW = int(time.time())
GB = 1024 ** 3

NODES = {
    "datacloud-node-1": dict(maxmem=256 * GB, cores=32, status="online", mem=0.58, cpu=0.31, disk=0.46),
    "datacloud-node-2": dict(maxmem=384 * GB, cores=48, status="online", mem=0.63, cpu=0.44, disk=0.52),
    "datacloud-node-3": dict(maxmem=256 * GB, cores=32, status="online", mem=0.92, cpu=0.37, disk=0.71),
    "datacloud-node-4": dict(maxmem=256 * GB, cores=32, status="offline", mem=0, cpu=0, disk=0),
}
# processori diversi come nel cluster reale: le VM con cpu=host si spostano a caldo solo tra node-1 e node-3
# flag CPU (accorciati): una VM con cpu=host va a caldo solo verso un nodo che li ha tutti
CPU_FLAGS = {"AMD EPYC 7313": "fpu sse2 avx2 svm vaes pku", "Intel Xeon E5-2640 v4": "fpu sse2 avx2 vmx"}
CPU_MODELS = {"datacloud-node-1": "AMD EPYC 7313", "datacloud-node-2": "Intel Xeon E5-2640 v4", "datacloud-node-3": "AMD EPYC 7313"}
OSES = [
    {"id": "ubuntu", "name": "Ubuntu", "pretty-name": "Ubuntu 24.04.1 LTS", "version": "24.04.1 LTS (Noble Numbat)", "version-id": "24.04", "kernel-release": "6.8.0-45-generic", "machine": "x86_64"},
    {"id": "rocky", "name": "Rocky Linux", "pretty-name": "Rocky Linux 8.10 (Green Obsidian)", "version": "8.10 (Green Obsidian)", "version-id": "8.10", "kernel-release": "4.18.0-553.el8_10.x86_64", "machine": "x86_64"},
    {"id": "debian", "name": "Debian GNU/Linux", "pretty-name": "Debian GNU/Linux 12 (bookworm)", "version": "12 (bookworm)", "version-id": "12", "kernel-release": "6.1.0-25-amd64", "machine": "x86_64"},
    {"id": "rhel", "name": "Red Hat Enterprise Linux", "pretty-name": "Red Hat Enterprise Linux 8.10 (Ootpa)", "version": "8.10 (Ootpa)", "version-id": "8.10", "kernel-release": "4.18.0-553.el8_10.x86_64", "machine": "x86_64"},
    {"id": "mswindows", "name": "Microsoft Windows", "pretty-name": "Microsoft Windows Server 2022 Standard", "version": "Server 2022", "version-id": "10.0", "kernel-release": "20348", "machine": "x86_64"},
]
EXEC = {}  # pid -> risultato finto (solo per l'anteprima: nessun comando viene eseguito)


def fake_dmesg(lines, level):
    rnd2 = random.Random(7)
    out = []
    for i in range(1500):
        t = time.strftime("%Y-%m-%dT%H:%M:%S+0000", time.gmtime(NOW - 86400 + i * 57))
        kind = rnd2.random()
        if kind < 0.03:
            out.append((f"{t} EXT4-fs error (device vda1): ext4_find_entry:1455: inode #{rnd2.randint(100,9999)}: comm cron: reading directory lblock 0", "err"))
        elif kind < 0.08:
            out.append((f"{t} nf_conntrack: table full, dropping packet (warning {i})", "warn"))
        elif kind < 0.5:
            out.append((f"{t} audit: type=1400 audit({NOW - 86400 + i * 57}.123:{i}): avc: denied {{ read }} for pid={rnd2.randint(100,9999)}", "info"))
        else:
            out.append((f"{t} systemd[1]: Started Session {i} of User root.", "info"))
    if level == "warn":
        out = [x for x in out if x[1] != "info"]
    return "\n".join(x[0] for x in out[-lines:]) + "\n"


NAMES = ["web-lab", "db-test", "ci-runner", "gitea", "nextcloud", "grafana", "prometheus", "mail-relay",
         "dns-int", "vpn-gw", "backup-srv", "k8s-ctl", "k8s-w1", "k8s-w2", "jupyter", "gpu-train",
         "wiki", "monitor", "proxy", "ldap", "redis-cache", "minio", "registry", "build-arm"]

VMS = []
CFG_EDITS = {}  # vmid -> {chiave: valore|None}: le uniche scritture accettate (config QEMU e spegnimento), solo in memoria
vmid = 100
for node in NODES:
    n = {"datacloud-node-1": 12, "datacloud-node-2": 14, "datacloud-node-3": 12, "datacloud-node-4": 6}[node]
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
VMS[40].update(vmid=151, name="web-lab-01", node="datacloud-node-3", status="stopped", mem=0, cpu=0, type="qemu")
VMS.append(dict(type="qemu", node="datacloud-node-1", vmid=9000, name="rocky-9-template", status="stopped", maxmem=4 * GB,
                mem=0, cpu=0, maxdisk=20 * GB, template=1, ip=""))
for v in VMS:
    if v["vmid"] % 7 == 0: v["ip"] = ""  # DHCP: nessun IP in Proxmox
seen = set()
for v in VMS:  # VMID univoci: la VM di prova 151 non deve avere omonimi
    while v["vmid"] in seen:
        v["vmid"] += 1000
    seen.add(v["vmid"])
for v in VMS:
    v["disk"] = int(v["maxdisk"] * rnd.uniform(.15, .8)) if v["status"] == "running" else 0
# tag come sul cluster reale: di sistema per tutte; `gruppo-<prefisso>` su una VM ogni tre (le altre restano senza gruppo e
# la pagina Gruppi propone il prefisso del nome); una VM con due gruppi (condivisa), un tag maiuscolo e un template con un
# tag di gruppo per provare l'avviso
for v in VMS:
    t = ["cloudinit"] if v["vmid"] % 2 else ["ubuntu-template", "24.04"]
    if v["vmid"] % 3 == 0 and not v.get("template"): t.append("gruppo-" + v["name"].split("-")[0].lower())
    if v["vmid"] % 31 == 0: t.append("LTSC")
    v["tags"] = ";".join(t) if not v.get("template") else "ubuntu-template;gruppo-ghost"
shared = next(v for v in VMS if v["type"] == "qemu" and not v.get("template") and v["vmid"] % 3 != 0)
shared["tags"] += ";gruppo-condivisa;gruppo-secondo"


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
        up = 1 if (node == "datacloud-node-3" and i > 70) else 0
        mem = min(.99, base["mem"] - (.22 if node == "datacloud-node-3" else .06) + .05 * wave + (i - 70) * .012 * up + seed.uniform(-.01, .01))
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
        r = {k: v[k] for k in ("type", "node", "vmid", "name", "status", "maxmem", "mem", "cpu", "maxdisk", "disk", "template", "tags")}
        r["id"] = f"{v['type']}/{v['vmid']}"
        r["maxcpu"] = v.get("maxcpu", 2)
        res.append(r)
    for n, b in NODES.items():  # storage condiviso (come Ceph) e uno locale, per il consiglio della creazione VM
        if b["status"] == "online":
            res.append(dict(id=f"storage/{n}/ceph-vm", type="storage", node=n, storage="ceph-vm", status="available", shared=1, content="images,rootdir", maxdisk=5000 * GB, disk=int(1000 * GB)))
            res.append(dict(id=f"storage/{n}/local-lvm", type="storage", node=n, storage="local-lvm", status="available", shared=0, content="images,rootdir", maxdisk=900 * GB, disk=int(900 * GB * b["disk"])))
    return res


def tasks():
    out = []
    spec = [("qmigrate", "datacloud-node-1", 140, "diego@pve", None, 90), ("vzdump", "datacloud-node-3", 151, "root@pam", "ERROR: storage 'nas01' non raggiungibile", 3000),
            ("qmclone", "datacloud-node-1", 213, "diego@pve", "OK", 5400), ("qmsnapshot", "datacloud-node-2", 118, "diego@pve", "OK", 7200),
            ("qmstart", "datacloud-node-3", 118, "diego@pve", "OK", 9000), ("qmresize", "datacloud-node-2", 207, "diego@pve", "OK", 80000)]
    for i, (t, n, vid, user, st, ago) in enumerate(spec):
        s = NOW - ago
        e = None if st is None else s + 40
        d = dict(upid=f"UPID:{n}:0000{i}:{s:x}:{t}:{vid}:{user}:", node=n, pid=1000 + i, pstart=s, starttime=s,
                 type=t, id=str(vid), user=user)
        if e:
            d.update(endtime=e, status=st)
        out.append(d)
    return out


# ── Ceph finto: CEPH_SCENARIO = healthy | warn | error | recovery | down (default recovery); "none" = nessun Ceph ──
SCEN = os.environ.get("CEPH_SCENARIO", "recovery")
CEPH_NODES = ["datacloud-node-%d" % i for i in range(1, 7)]


def ceph_osds():
    r = random.Random(7)
    out, oid = [], 0
    for n in CEPH_NODES:
        for k in range(6):
            st = "up"
            if SCEN in ("down", "recovery", "error") and n == CEPH_NODES[4] and oid in (24, 25): st = "down"
            used = r.uniform(.55, .88) * 7680873725952
            out.append(dict(id=str(oid), name="osd.%d" % oid, type="osd", leaf=1, host=n, status=st, **{"in": 0 if st == "down" and SCEN == "error" else 1},
                            device_class="hdd" if k >= 4 else "ssd", percent_used=used / 7680873725952 * 100, bytes_used=int(used), total_space=7680873725952,
                            pgs=34, apply_latency_ms=1))
            oid += 1
    return out


def ceph_status():
    osds = ceph_osds()
    down = sum(1 for o in osds if o["status"] == "down")
    total = 3.36e14
    used = total * (.97 if SCEN == "error" else .78)
    health, checks = "HEALTH_OK", {}
    if SCEN in ("warn", "down", "recovery"):
        health = "HEALTH_WARN"
        checks["POOL_APP_NOT_ENABLED"] = dict(severity="HEALTH_WARN", muted=False, summary=dict(message="1 pool(s) do not have an application enabled", count=1), detail=[dict(message="application not enabled on pool 'test'")])
    if down:
        health = "HEALTH_ERR" if SCEN == "error" else "HEALTH_WARN"
        checks["OSD_DOWN"] = dict(severity="HEALTH_WARN", muted=False, summary=dict(message="%d osds down" % down, count=down), detail=[dict(message="osd.%s (root=default,host=%s) is down" % (o["id"], o["host"])) for o in osds if o["status"] == "down"])
    if SCEN == "error":
        health = "HEALTH_ERR"
        checks["OSD_FULL"] = dict(severity="HEALTH_ERR", muted=False, summary=dict(message="1 full osd(s)", count=1), detail=[])
    states = [dict(state_name="active+clean", count=257)]
    pm = dict(bytes_total=total, bytes_used=used, bytes_avail=total - used, data_bytes=used / 3, num_pgs=257, num_objects=12888875, num_pools=3,
              read_bytes_sec=4.1e7 * (1 + .3 * math.sin(time.time() / 7)), write_bytes_sec=3.0e7, read_op_per_sec=1218, write_op_per_sec=310)
    ev = {}
    if SCEN == "recovery":
        states = [dict(state_name="active+clean", count=200), dict(state_name="active+undersized+degraded", count=30), dict(state_name="active+remapped+backfill_wait", count=22), dict(state_name="active+recovering", count=5)]
        pm.update(degraded_objects=900000, degraded_ratio=.07, misplaced_objects=400000, misplaced_ratio=.03, recovering_objects_per_sec=2500, recovering_bytes_per_sec=8.5e8)
        ev = {"a": dict(message="Rebalancing after osd.30 marked out", progress=.63)}
    pm["pgs_by_state"] = states
    return dict(health=dict(status=health, checks=checks), osdmap=dict(num_osds=len(osds), num_up_osds=len(osds) - down, num_in_osds=len(osds) - (down if SCEN == "error" else 0)),
                pgmap=pm, quorum_names=CEPH_NODES[:3], monmap=dict(mons=[dict(name=n) for n in CEPH_NODES[:3]]), progress_events=ev)


def ceph_route(rest):
    """rest = path dopo /cluster/ceph o /nodes/{n}/ceph; None = 404"""
    if SCEN == "none": return "ERR500"
    if rest == "status": return ceph_status()
    if rest == "flags":
        return [dict(name="noout", value=1 if SCEN in ("down", "recovery") else 0, description="OSDs will not be automatically marked out"), dict(name="noup", value=0, description="OSDs are not allowed to start")]
    if rest == "osd":
        hosts = {}
        for o in ceph_osds(): hosts.setdefault(o["host"], []).append(o)
        return dict(flags="sortbitwise", root=dict(type="root", name="default", children=[dict(type="host", name=h, children=v) for h, v in hosts.items()]))
    if rest == "pool":
        return [dict(pool_name=n, type="replicated", size=3, min_size=2, pg_num=128, bytes_used=int(2e13 * f), percent_used=f * .4, crush_rule_name=r, application_metadata=dict(rbd={}), pg_autoscale_mode="on")
                for n, f, r in (("datacloud-ssd", 1.0, "replicated_ssd"), ("datacloud-hdd", .6, "replicated_hdd"), (".mgr", .01, "replicated_ssd"), ("archivio", .2, "replicated_rule"))]
    if rest == "crush":
        rules = "".join("rule %s {\n\tid %d\n\ttype replicated\n\tstep take default%s\n\tstep chooseleaf firstn 0 type host\n\tstep emit\n}\n" % (n, i, c)
                        for i, (n, c) in enumerate((("replicated_rule", ""), ("replicated_ssd", " class ssd"), ("replicated_hdd", " class hdd"))))
        return "# begin crush map\ntunable choose_total_tries 50\n\n# rules\n" + rules + "\n# end crush map\n"
    if rest in ("mon", "mgr", "mds"):
        if rest == "mds": return []
        return [dict(name=n, host=n, state="running" if rest == "mon" else ("active" if i == 0 else "standby"), quorum=1, rank=i, ceph_version_short="20.2.4") for i, n in enumerate(CEPH_NODES[:3])]
    return None


def route(path):
    p = path.rstrip("/")
    segs = p.split("/")
    if p == "/cluster/resources": return resources()
    if p == "/cluster/status":
        return [dict(type="cluster", name="polimi-deib", nodes=4, quorate=1, version=7)] + \
               [dict(type="node", name=n, online=1 if b["status"] == "online" else 0, id=f"node/{n}") for n, b in NODES.items()]
    if p.startswith("/cluster/ceph/"): return ceph_route(p[len("/cluster/ceph/"):])
    if p == "/cluster/mapping/pci": return [dict(id="gpu-a100", map=[f"node={n},path=0000:02:00,id=10de:20f1" for n in NODES]), dict(id="gpu-3090", map=["node=datacloud-node-1,path=0000:01:00"])]
    if p == "/cluster/tasks": return tasks()
    if p == "/cluster/nextid": return str(max(v["vmid"] for v in VMS) + 1)
    if p == "/cluster/ha/resources": return []
    if p == "/nodes":
        return [dict(node=n, status=b["status"], maxmem=b["maxmem"], mem=int(b["maxmem"] * b["mem"]), cpu=b["cpu"],
                     maxcpu=b["cores"], uptime=86400 * 12 if b["mem"] else 0, maxdisk=1000 * GB, disk=int(1000 * GB * b["disk"]))
                for n, b in NODES.items()]
    if len(segs) >= 3 and segs[1] == "nodes":
        node = segs[2]
        if node not in NODES: return None
        rest = "/".join(segs[3:])
        if rest.startswith("ceph/"): return ceph_route(rest[len("ceph/"):])
        b = NODES[node]
        if rest == "rrddata": return rrd(node, "day")
        if rest == "status":
            return dict(cpu=b["cpu"], memory=dict(used=int(b["maxmem"] * b["mem"]), total=b["maxmem"]), uptime=86400 * 12,
                        loadavg=["1.2", "1.1", "0.9"], pveversion="pve-manager/8.2.4", kversion="Linux 6.8.8-2-pve",
                        cpuinfo=dict(cpus=b["cores"], model=CPU_MODELS.get(node, "AMD EPYC 7313"), flags=CPU_FLAGS.get(CPU_MODELS.get(node, "AMD EPYC 7313"), "")))
        if rest == "hardware/pci":
            return [dict(id="0000:01:00.0", **{"class": "0x030000"}, vendor="0x10de", iommugroup=12, vendor_name="NVIDIA Corporation", device_name="GA102 [GeForce RTX 3090]"),
                    dict(id="0000:01:00.1", **{"class": "0x040300"}, vendor_name="NVIDIA Corporation", device_name="GA102 High Definition Audio"),
                    dict(id="0000:02:00.0", **{"class": "0x030200"}, vendor="0x10de", iommugroup=13, vendor_name="NVIDIA Corporation", device_name="A100 PCIe 80GB"),
                    dict(id="0000:03:00.0", **{"class": "0x020000"}, vendor_name="Intel Corporation", device_name="Ethernet X710")]
        if rest.startswith("tasks/") and rest.endswith("/status"):  # migrazione finta: "running" per le prime 2 letture, poi OK
            from urllib.parse import unquote
            up = unquote(rest[len("tasks/"):-len("/status")]); MIG_TASKS[up] = MIG_TASKS.get(up, 0) + 1
            return dict(status="running") if MIG_TASKS[up] <= 2 else dict(status="stopped", exitstatus="OK")
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
                return dict(status=v["status"], mem=v["mem"], maxmem=v["maxmem"], cpu=v["cpu"], uptime=3600 * 30, cpus=v.get("maxcpu", 4), maxdisk=v.get("maxdisk", 0), name=v["name"], vmid=v["vmid"])
            if seg[2] == "migrate":  # controllo prima della migrazione (sola lettura)
                lr = ["hostpci0"] if (seg[0] == "qemu" and v["vmid"] % 11 == 0 or v["vmid"] % 13 == 0) else []
                return dict(allowed_nodes=[n for n, b in NODES.items() if b["status"] == "online" and n != v["node"]], not_allowed_nodes={}, local_disks=[], local_resources=lr, running=1 if v["status"] == "running" else 0)
            if seg[2] == "config":
                key = "ipconfig0" if seg[0] == "qemu" else "net0"
                val = f"ip={v['ip']}/24,gw=10.75.4.254" if v["ip"] else ""
                cfg = {"name": v["name"], "cores": 2, "memory": v["maxmem"] // (1024 * 1024)}
                if v.get("tags"): cfg["tags"] = v["tags"]
                if v.get("template"):
                    cfg["template"] = 1
                    cfg["virtio0"] = f"ceph-vm:base-{v['vmid']}-disk-0,size={v['maxdisk'] // GB}G"
                if seg[0] == "qemu" and not v.get("template"): cfg["virtio0"] = f"ceph-vm:vm-{v['vmid']}-disk-0,size={v['maxdisk'] // GB}G"; cfg["virtio1"] = f"ceph-vm:vm-{v['vmid']}-disk-1,size=20G"; cfg["ide2"] = f"ceph-vm:vm-{v['vmid']}-cloudinit,media=cdrom"
                if v.get("mem_mb"): cfg["memory"] = v["mem_mb"]
                if v.get("maxcpu"): cfg["cores"] = v["maxcpu"]
                if val: cfg[key] = val
                if seg[0] == "qemu" and v["vmid"] % 3 == 0: cfg["serial0"] = "socket"  # alcune VM con console seriale
                if seg[0] == "qemu" and v["vmid"] % 2 == 0: cfg["cpu"] = "host"  # meta' delle VM con la CPU del nodo
                if seg[0] == "qemu" and v["vmid"] % 11 == 0: cfg["hostpci0"] = "0000:01:00,pcie=1,x-vga=1"  # GPU diretta
                if seg[0] == "qemu" and v["vmid"] % 13 == 0: cfg["hostpci0"] = "mapping=gpu-a100,pcie=1"      # GPU da mapping
                if seg[0] == "qemu" and v["vmid"] % 19 == 0: cfg["hostpci0"] = "0000:02:00"                       # diretta sulla stessa A100 del mapping: doppione
                if seg[0] == "qemu" and v["vmid"] % 23 == 0: cfg["hostpci0"] = "0000:7a:00"                       # scheda inesistente sul nodo
                if seg[0] == "qemu" and v["vmid"] % 17 == 0: cfg["hostpci0"] = "0000:03:00.0"                 # NIC, non e' una GPU
                for k, val in CFG_EDITS.get(v["vmid"], {}).items():
                    if val is None: cfg.pop(k, None)
                    else: cfg[k] = val
                cfg["digest"] = hashlib.sha256(json.dumps(cfg, sort_keys=True).encode()).hexdigest()[:40]  # come Proxmox: cambia se cambia il config
                return cfg
            if seg[2] == "agent" and seg[3] == "network-get-interfaces":
                if v["status"] != "running" or v["type"] != "qemu" or v["vmid"] % 14 == 0: return None  # agent muto
                return dict(result=[
                    {"name": "lo", "ip-addresses": [{"ip-address": "127.0.0.1", "ip-address-type": "ipv4", "prefix": 8}]},
                    {"name": "docker0", "ip-addresses": [{"ip-address": "172.17.0.1", "ip-address-type": "ipv4", "prefix": 16}]},
                    {"name": "ens18", "ip-addresses": [{"ip-address": "10.99.%d.5" % (v["vmid"] % 250), "ip-address-type": "ipv4", "prefix": 24}]}])
            if seg[2] == "agent" and seg[3] == "get-osinfo":
                if v["status"] != "running" or v["type"] != "qemu": return None
                return dict(result=OSES[v["vmid"] % len(OSES)])
            if seg[2] == "agent":
                root = dict(mountpoint="/", **{"used-bytes": v["disk"], "total-bytes": v["maxdisk"]}, type="ext4", name="vda1", disk=[{"bus-type": "virtio", "dev": "/dev/vda1"}])
                if v["vmid"] % 4 != 0: return dict(result=[root])
                # alcune VM: disco secondario, snap, partizione EFI e un montaggio NFS (da non mostrare)
                return dict(result=[root,
                    dict(mountpoint="/boot/efi", **{"used-bytes": 6 * 1024 * 1024, "total-bytes": 100 * 1024 * 1024}, type="vfat", name="vda15", disk=[{"bus-type": "virtio", "dev": "/dev/vda15"}]),
                    dict(mountpoint="/data", **{"used-bytes": 38 * GB, "total-bytes": 50 * GB}, type="xfs", name="vdb1", disk=[{"bus-type": "virtio", "dev": "/dev/vdb1"}]),
                    dict(mountpoint="/snap/core/1", **{"used-bytes": 100 * 1024 * 1024, "total-bytes": 100 * 1024 * 1024}, type="squashfs", name="loop0", disk=[]),
                    dict(mountpoint="/mnt/nas", **{"used-bytes": 900 * GB, "total-bytes": 1000 * GB}, type="nfs4", name="nas:/export", disk=[])])
            if seg[2] == "rrddata":
                pts = rrd(node, "day")
                for pt in pts:  # campi per-VM: mem/maxmem (non memused/memtotal)
                    pt["mem"] = int(v["maxmem"] * min(.95, max(.05, v["mem"] / max(v["maxmem"], 1) + (pt["cpu"] - .3) * .1)))
                    pt["maxmem"] = v["maxmem"]
                    pt["cpu"] = round(min(.95, v["cpu"] * (0.6 + pt["cpu"])), 4)
                return pts
    return None


MIG_TASKS = {}
RESTARTED = set()  # VM riaccese: il disco "invisibile" compare solo dopo
NOBLANK = set(x for x in os.environ.get("NOBLANK_VMIDS", "").split(",") if x.strip().isdigit())  # il guest non vede il disco nuovo finche' la VM non viene riavviata
NOSHUTDOWN = set(x for x in os.environ.get("NOSHUTDOWN_VMIDS", "").split(",") if x.strip().isdigit())


class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def do_GET(self):
        path = urlparse(self.path).path
        if not path.startswith("/api2/json"):
            return self.send_error(404)
        if path.endswith("/agent/exec-status"):
            pid = int(parse_qs(urlparse(self.path).query).get("pid", ["0"])[0])
            code, out = EXEC.get(pid, (0, ""))
            body = json.dumps({"data": {"exited": 1, "exitcode": code, "out-data": out}}).encode()
            self.send_response(200); self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
            return
        data = route(path[len("/api2/json"):])
        if data == "ERR500":  # cluster senza Ceph, come HOSTING
            body = json.dumps({"data": None, "message": "binary not installed: /usr/bin/ceph-mon\n"}).encode()
            self.send_response(500); self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
            return
        if data is None:
            return self.send_error(404)
        body = json.dumps({"data": data}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

    def do_POST(self):
        path = urlparse(self.path).path
        if path.endswith("/agent/exec"):  # l'unica "scrittura" accettata: esecuzione finta, nessun effetto
            n = int(self.headers.get("Content-Length", "0") or 0)
            cmd = parse_qs(self.rfile.read(min(n, 65536)).decode()).get("command", [])
            pid = 1000 + len(EXEC)
            if "pmgr" in cmd and cmd[cmd.index("pmgr") + 1:] == ["find-blank"]:  # script del disco: un solo disco vuoto
                vm_id = path.split("/")[-3] if path.count("/") >= 3 else ""
                EXEC[pid] = (0, "" if (vm_id in NOBLANK and vm_id not in RESTARTED) else "vdb\n")
            elif any("-x" in c and "HELPER" in c for c in cmd):
                EXEC[pid] = (0, "INLINE\n")
            elif "pmgr" in cmd and len(cmd) >= 6 and cmd[-2].isdigit():  # solo dmesg (righe, livello)
                a_lines, a_level = int(cmd[-2]), cmd[-1]
                EXEC[pid] = (0, fake_dmesg(a_lines, a_level))
            else:
                EXEC[pid] = (0, "")
            body = json.dumps({"data": {"pid": pid}}).encode()
            self.send_response(200); self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
            return
        parts = path[len("/api2/json"):].strip("/").split("/")
        n = int(self.headers.get("Content-Length", "0") or 0)
        form = parse_qs(self.rfile.read(min(n, 65536)).decode()) if n else {}
        # config QEMU: PUT /nodes/{n}/qemu/{id}/config (con digest, come Proxmox)
        if self.command == "PUT" and len(parts) == 5 and parts[0] == "nodes" and parts[2] in ("qemu", "lxc") and parts[4] == "config" and parts[3].isdigit():
            vmid = int(parts[3]); cur = route("/" + "/".join(parts))
            if cur is None: return self.send_error(404)
            if form.get("digest", [cur["digest"]])[0] != cur["digest"]:  # come Proxmox: il digest e' facoltativo, se c'e' deve coincidere
                return self._json({"data": None, "message": "detected modified configuration - file changed by other user? Try again"}, 500)
            ed = CFG_EDITS.setdefault(vmid, {})
            vm = next(x for x in VMS if x["vmid"] == vmid)
            for k, val in form.items():
                if k == "digest": continue
                if k == "delete":
                    for d in val[0].split(","):
                        if d == "tags": vm["tags"] = ""
                        else: ed[d] = None
                elif k == "tags": vm["tags"] = val[0]
                elif k == "memory" and val[0].isdigit(): vm["maxmem"] = int(val[0]) * 1024 * 1024; vm["mem_mb"] = int(val[0])
                elif k == "cores" and val[0].isdigit(): vm["maxcpu"] = int(val[0])
                else: ed[k] = val[0]
            return self._json({"data": None}, 200)
        # spegnimento/accensione: POST /nodes/{n}/qemu/{id}/status/{shutdown|stop|start}
        # le VM in NOSHUTDOWN_VMIDS (env, es. "105,110") ignorano lo spegnimento ordinato: serve a provare lo stop forzato
        if self.command == "POST" and len(parts) == 6 and parts[2] == "qemu" and parts[4] == "status" and parts[5] in ("shutdown", "stop", "start") and parts[3].isdigit():
            vmid = int(parts[3])
            for v in VMS:
                if v["vmid"] != vmid: continue
                if parts[5] == "start": v["status"] = "running"; RESTARTED.add(str(vmid))
                elif parts[5] == "stop" or str(vmid) not in NOSHUTDOWN: v["status"] = "stopped"
            up = "UPID:%s:0000ABCD:00123456:65F0A1B2:qm%s:%d:root@pam!mock:" % (parts[1], parts[5], vmid)
            MIG_TASKS[up] = 2  # gia' "finito" alla prossima lettura
            return self._json({"data": up}, 200)
        # clone: POST /nodes/{n}/qemu/{id}/clone newid,name,target (crea la VM solo in memoria, sul nodo "target")
        if self.command == "POST" and len(parts) == 5 and parts[0] == "nodes" and parts[2] == "qemu" and parts[4] == "clone" and parts[3].isdigit():
            src = next((x for x in VMS if x["vmid"] == int(parts[3])), None)
            newid = form.get("newid", [""])[0]; tgt = form.get("target", [parts[1]])[0]
            if src is None or not newid.isdigit() or any(x["vmid"] == int(newid) for x in VMS) or tgt not in NODES or NODES[tgt]["status"] != "online":
                return self._json({"data": None, "message": "clone refused"}, 500)
            VMS.append(dict(src, vmid=int(newid), name=form.get("name", ["clone"])[0], node=tgt, status="stopped", mem=0, cpu=0, template=0, tags="", ip=""))
            up = "UPID:%s:0000ABCD:00123456:65F0A1B2:qmclone:%s:root@pam!mock:" % (parts[1], parts[3])
            MIG_TASKS.pop(up, None)
            return self._json({"data": up}, 200)
        # resize disco: PUT /nodes/{n}/qemu/{id}/resize (solo conferma)
        if self.command == "PUT" and len(parts) == 5 and parts[0] == "nodes" and parts[2] == "qemu" and parts[4] == "resize" and parts[3].isdigit():
            return self._json({"data": None}, 200)
        # migrazione: POST /nodes/{n}/{qemu|lxc}/{id}/migrate target=<nodo> (sposta la VM solo in memoria)
        if self.command == "POST" and len(parts) == 5 and parts[0] == "nodes" and parts[2] in ("qemu", "lxc") and parts[4] == "migrate" and parts[3].isdigit():
            vm = next((x for x in VMS if x["vmid"] == int(parts[3])), None)
            tgt = form.get("target", [""])[0]
            if vm is None or tgt not in NODES or NODES[tgt]["status"] != "online" or tgt == vm["node"]:
                return self._json({"data": None, "message": "invalid target"}, 500)
            src = vm["node"]; vm["node"] = tgt
            up = "UPID:%s:0000ABCD:00123456:65F0A1B2:%s:%s:root@pam!mock:" % (src, "qmigrate" if parts[2] == "qemu" else "vzmigrate", parts[3])
            MIG_TASKS.pop(up, None)
            return self._json({"data": up}, 200)
        self.send_error(405, "anteprima in sola lettura (tranne config QEMU/LXC, spegnimento/accensione e migrazione, solo in memoria)")
    do_PUT = do_DELETE = do_POST

    def _json(self, obj, code):
        body = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)


ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
