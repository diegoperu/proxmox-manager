# ProxmoxManager — CLAUDE.md

Interfaccia web di amministrazione per cluster Proxmox VE.
Browser → Go proxy (localhost:8080) → Proxmox API. Nessun CORS.

## Stack

| Layer | Tecnologia |
|-------|-----------|
| Backend | Go 1.21, modulo `proxmox-manager` |
| Router | `github.com/go-chi/chi/v5` |
| CORS | `github.com/go-chi/cors` |
| DB | SQLite via `github.com/mattn/go-sqlite3` (CGO_ENABLED=1) — solo cache API |
| Frontend | Vanilla JS + Chart.js 4.4 + SheetJS (xlsx) — **nessun framework** |
| Config | `config.json` nella working dir del binario |
| Storico | Proxmox RRD nativo (no collector locale) |

## Struttura

```
proxmox-manager/
├── go.mod
├── build.sh                        # CGO_ENABLED=1 go build ./cmd/server/
├── cmd/server/
│   ├── main.go                     # entrypoint, router, embed
│   └── web/
│       └── templates/
│           └── index.html          # SPA intera (1 file)
└── internal/
    ├── api/
    │   └── client.go               # client HTTP verso Proxmox (TLS skip verify)
    ├── cache/
    │   └── store.go                # SQLite: solo api_cache (TTL)
    ├── config/
    │   └── config.go               # config.json, singleton con RWMutex
    └── handlers/
        └── handlers.go             # tutti gli HTTP handler (proxy → Proxmox)
```

## Build & run

```bash
# Dipendenze: go 1.21+ e gcc (per go-sqlite3)
chmod +x build.sh && ./build.sh
./proxmox-manager          # ascolta su :8080
```

## Regola critica: embed Go

`//go:embed web/templates/index.html` è in `cmd/server/main.go`.
La cartella `web/` **deve stare nella stessa directory di `main.go`** (`cmd/server/web/`).
Go non supporta embed con path che escono dallo scope del file (`../../`).

## Config (config.json)

```json
{
  "clusters": [
    {
      "label": "Default",
      "url": "https://192.168.x.x:8006",
      "api_token": "root@pam!tokenid=UUID",
      "default": true
    }
  ],
  "theme": "",
  "cache_seconds": 30,
  "db_path": "proxmox.db",
  "listen_addr": ":8080"
}
```

**Migrazione automatica**: se `config.json` ha ancora i campi flat `proxmox_url` + `api_token` (formato vecchio), `Load()` migra automaticamente creando `clusters[0]` con `label:"Default"` e salva nel nuovo formato.

Autenticazione: **solo API Token**. Username/password rimossi.
Token format Proxmox: `USER@REALM!TOKENID=UUID`.
`theme`: `""` (segui OS), `"dark"`, `"light"`, `"eva01"`, `"catppuccino"`.

### Strutture Go

```go
type ClusterConfig struct {
    Label    string `json:"label"`
    URL      string `json:"url"`
    APIToken string `json:"api_token"`
    Default  bool   `json:"default"`
}

type Config struct {
    Clusters     []ClusterConfig `json:"clusters"`
    Theme        string          `json:"theme"`
    CacheSeconds int             `json:"cache_seconds"`
    DBPath       string          `json:"db_path"`
    ListenAddr   string          `json:"listen_addr"`
}
```

Helper config: `GetCluster(idx int)` e `GetDefaultCluster()` leggono con RLock. `Get()` ritorna deep copy della slice `Clusters` — i caller possono mutare liberamente il valore ritornato senza corrompere `current`.

## API Routes (tutte su /api/*)

```
GET  /api/config                                    GetConfig (token mascherati)
POST /api/config                                    SaveConfig (solo theme)
POST /api/config/test                               TestConnection → GetNodes

GET  /api/clusters                                  GetClusters (token mascherati: ...last8chars)
POST /api/clusters                                  AddCluster {label, url, api_token, default}
PUT  /api/clusters/{idx}                            UpdateCluster (api_token vuoto = non cambia)
DELETE /api/clusters/{idx}                          DeleteCluster (rifiuta se ultimo)
POST /api/clusters/{idx}/test                       TestCluster → GetNodes
PUT  /api/clusters/{idx}/default                    SetDefaultCluster (toglie default agli altri)

GET  /api/dashboard                                 GetClusterResources + GetClusterStatus
GET  /api/nodes                                     GetNodes
GET  /api/nodes/{node}/status                       GetNodeStatus + storage + networks
GET  /api/nodes/{node}/rrd?timeframe=hour|day|...   GetNodeRRD (proxy raw RRD Proxmox)
POST /api/nodes/{node}/cmd/{cmd}                    NodeCommand (reboot|shutdown)
GET  /api/nodes/{node}/storage                      GetNodeStorage

GET  /api/vms                                       GetAllVMs: nodi in parallelo → qemu+lxc+IP da config
GET  /api/nodes/{node}/qemu/{vmid}/status           GetVMStatus + config + snapshots
GET  /api/nodes/{node}/qemu/{vmid}/rrd              GetVMRRD (proxy raw RRD Proxmox)
GET  /api/nodes/{node}/qemu/{vmid}/fsinfo           GetVMFSInfo (agent get-fsinfo, GET)
POST /api/nodes/{node}/qemu/{vmid}/action/{action}  VMAction (start|stop|shutdown|reboot|suspend|resume|reset)
POST /api/nodes/{node}/qemu/{vmid}/snapshot         VMSnapshot
POST /api/nodes/{node}/qemu/{vmid}/migrate          VMMigrate {target, online}
POST /api/nodes/{node}/qemu/{vmid}/adduser          AddVMUser {username} → guest agent exec
DELETE /api/nodes/{node}/qemu/{vmid}                DeleteVM

POST /api/batch                                     BatchAction {vms:[{type,node,vmid}], action, params}

POST /api/nodes/{node}/lxc/{vmid}/action/{action}   ContainerAction
POST /api/nodes/{node}/lxc/{vmid}/snapshot          ContainerSnapshot
DELETE /api/nodes/{node}/lxc/{vmid}                 DeleteContainer

GET  /api/templates                                 GetTemplates: VM con template=1 o nome contiene "template"/"cloudinit"
GET  /api/nextid                                    GetNextVMID
POST /api/provision                                 ProvisionVM (vedi sotto)

GET  /api/storage                                   GetStorage
GET  /api/pools                                     GetPools
GET  /api/tasks                                     GetClusterTasks
GET  /api/metrics?timeframe=hour|day|week|month|year  GetMetrics: RRD nodi aggregato cluster (avg cpu/mem per 5min bucket)
GET  /api/report?timeframe=...&type=qemu|lxc|all&node=  GetReportData: RRD per VM aggregato orario (parallel goroutine per VM)
```

## Multi-cluster — routing e propagazione

Ogni handler usa `h.getClientFor(clusterIdx(r))` per costruire il client Proxmox.

`clusterIdx(r)` legge `?cluster=N` (query param) o `X-Cluster-Index` (header). Restituisce `0` se assente.

`getClientFor(idx)` → `config.GetCluster(idx)`. `getClient()` → `config.GetDefaultCluster()` (solo per backward compat, non usato nei handler).

**Frontend**: `api(path, opts)` aggiunge automaticamente `?cluster=N` a tutte le route tranne `/clusters*` e `/config*` (NO_CLUSTER_ROUTES). `ClusterMgr.current` tiene l'indice attivo.

`ClusterMgr.load()` popola `ClusterMgr.clusters` da `GET /api/clusters`, aggiorna `<select id="cluster-select">`, nasconde il select se c'è un solo cluster. Chiamato in `App.init()` prima di `App.nav()`.

## GetAllVMs — arricchimento IP e cluster

`GetAllVMs` aggiunge `cluster_label` e `cluster_idx` a ogni VM/LXC (dopo `wg.Wait()`). Flusso completo:
1. Fetch lista nodi (parallelo via `sync.WaitGroup`)
2. Per ogni nodo: fetch QEMU list + LXC list
3. Per ogni VM/LXC: fetch config (`/qemu/{id}/config` o `/lxc/{id}/config`) in goroutine parallele
4. Estrae IP da `ipconfig0` (QEMU) o `net0` (LXC) via `extractVMIP(s string) string`
5. Aggiunge campo `"ip"` a ogni oggetto VM prima di rispondere

`extractVMIP`: parsea `"ip=10.x.x.x/24,gw=..."` o `"name=eth0,...,ip=10.x.x.x/24,..."` → restituisce solo host IP (senza CIDR). Ignora `"dhcp"`.

Client methods: `GetVMConfig`, `GetContainerConfig` (aggiunto).

## Proxmox RRD — campi reali (Proxmox 8)

**Nodi** (`/nodes/{node}/rrddata`):
- `time`, `cpu` (0-1), `memused` (bytes), `memtotal` (bytes)
- `netin`, `netout` (bytes/s), `loadavg`, `iowait`, `roottotal`, `rootused`
- ⚠️ NON `mem`/`maxmem` — sono `memused`/`memtotal`

**VM QEMU** (`/nodes/{node}/qemu/{vmid}/rrddata`):
- `time`, `cpu` (0-1), `mem` (bytes used), `maxmem` (bytes total)
- `netin`, `netout`, `disk` (I/O counter, NON uso filesystem), `maxdisk`

**LXC** (`/nodes/{node}/lxc/{vmid}/rrddata`):
- Stesso schema VM QEMU

**Parametro `ds` rimosso**: Proxmox 8 restituisce 400 se si passa `ds=cpu,mem,...`. Usare solo `?timeframe=`.

## Guest Agent

### Disk usage QEMU

`GET /nodes/{node}/qemu/{vmid}/agent/get-fsinfo` ritorna struttura:
```json
{"data": {"result": [{"mountpoint": "/", "used-bytes": N, "total-bytes": N, "disk":[...], "type":"ext4", "name":"vda1"}], "errobj": null}}
```
- Campo: `mountpoint` (singolare, non `mountpoints`)
- Handler `GetVMFSInfo` usa `c.Get()` (non POST) e unwrappa `result` prima di rispondere
- Frontend filtra per `f.mountpoint` e usa `f['used-bytes']`/`f['total-bytes']`
- Lista VM carica fsinfo in background post-render (`loadFsinfo()`, `loadDashFsinfo()`)

### AddVMUser — aggiunta utente via guest agent exec

`POST /api/nodes/{node}/qemu/{vmid}/adduser` body: `{"username":"nomeutente"}`

Flusso handler `AddVMUser`:
1. Valida username con regex `^[a-z_][a-z0-9_-]{0,31}$`
2. Se `ssh_key` presente: valida prefisso (`ssh-rsa`, `ssh-ed25519`, `ecdsa-*`, `sk-`), sanitizza newline
3. Branch **solo password**: genera password random 12 char con `genTempPassword()` (`crypto/rand`, charset `[a-zA-Z0-9!@#$%]`)
4. `AgentExec(node, vmid, ["/bin/sh", "-c", script])` → POST `/agent/exec` con body `command=/bin/sh&command=-c&command=<script>` (repeated keys, **non** `command[N]`)
5. Estrae PID dalla risposta
6. Polling `AgentExecStatus(node, vmid, pid)` ogni 1s fino a `exited==1` (timeout 30s)
7. Risponde `{"output":"...", "exitcode":N}` — aggiunge `"temp_password":"..."` se branch password

**Branch solo password** (SSHKey assente):
```sh
useradd -m -s /bin/bash <user> 2>/dev/null || true
echo '<user>:<RANDOM_12>' | chpasswd
usermod -aG sudo <user> 2>/dev/null || true
usermod -aG wheel <user> 2>/dev/null || true
chage -d 0 <user>                              # password scade al primo login
```
La password è generata con `crypto/rand` — mai stringa fissa. Inclusa nel JSON di risposta come `temp_password` e mostrata nel frontend nel log output del modal.

**Branch SSH key** (SSHKey presente):
```sh
useradd -m -s /bin/bash <user> 2>/dev/null || true
usermod -aG sudo <user> 2>/dev/null || true
usermod -aG wheel <user> 2>/dev/null || true
mkdir -p /home/<user>/.ssh
echo '<ssh_key>' >> /home/<user>/.ssh/authorized_keys
chmod 700 /home/<user>/.ssh
chmod 600 /home/<user>/.ssh/authorized_keys
chown -R <user>:<user> /home/<user>/.ssh
echo '<user> ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/<user>
chmod 440 /etc/sudoers.d/<user>
```
Nessuna password impostata; accesso solo via chiave SSH; sudo senza password via file dedicato in `/etc/sudoers.d/`.

Client methods: `AgentExec`, `AgentExecStatus`.
⚠️ Proxmox agent exec vuole repeated `command` keys, **non** `command[0]`/`command[1]`.

## Provisioning VM (POST /api/provision)

Flusso sequenziale nel handler `ProvisionVM`:
1. `GetNextVMID` se `new_vmid == 0`
2. Legge config template → estrae dimensione disco attuale dal campo `{disk_device}` (parse `size=NNg`)
3. `VMClone(full=1)` sul nodo template
4. `WaitForTask(upid, 10min)` polling ogni 3s
5. `VMSetConfig` → cores + memory
6. `VMResizeDisk` → calcola delta: se `desired > current` manda `+deltaG`, altrimenti `NNg` assoluto
7. `VMSetCloudInit` → ipconfig0 (CIDR obbligatorio, auto-aggiunto `/24` se mancante), nameserver, sshkeys, ciuser, cipassword

Body JSON richiesto:
```json
{
  "template_node": "pve1", "template_vmid": 9000, "target_node": "pve2",
  "new_vmid": 0, "name": "mia-vm", "cpu": 4, "memory": 4096,
  "disk": "50", "disk_device": "virtio0",
  "ip": "10.75.4.X/24", "gateway": "10.75.4.254", "dns": "8.8.8.8",
  "ciuser": "ubuntu", "cipassword": "", "ssh_keys": ""
}
```

Note provisioning:
- `disk_device` rilevato automaticamente dal frontend scansionando virtio0-7, scsi0-7, sata0-7, ide0-7
- SSH keys: encoding con `strings.ReplaceAll(url.QueryEscape(keys), "+", "%20")` — Proxmox richiede RFC 3986
- Cloud-init pre-compilato dal frontend leggendo config template
- Default gateway: `10.75.4.254`, default IP prefix: `10.75.4.`

## SQLite schema (solo cache)

```sql
api_cache(key TEXT PK, value TEXT, expires_at INTEGER)
```

Nessuna tabella metrics — storico completamente delegato a Proxmox RRD.

## Frontend (index.html — SPA vanilla)

**Pagine** (routing client-side):
- `dashboard` — KPI cluster + grafici CPU/RAM 6h (da `/api/metrics?timeframe=day`, slice last 6h) + tabella VM con disk% da fsinfo
- `nodes` — card per nodo (ordinati natural sort) + RRD grafici (memused/memtotal) + VM per nodo
- `vms` — tabella QEMU+LXC con colonna IP; multi-nodo selector (natural sort, modello esplicito); sort; disk% da fsinfo async
- `provision` — form clone template cloudinit con calcolo delta disco
- `batch` — chip selector nodi (natural sort, modello esplicito), filtri tipo/stato/testo, tabella full-height con IP, 7 azioni, log real-time
- `cluster` — gestione nodi (natural sort), dettaglio RRD, reboot/shutdown batch
- `reports` — selector periodo + grafici CPU/RAM/Net + tabella + export Excel
- `settings` — config connessione (URL + API Token) + selettore tema + test

**Colonna IP nelle tabelle VM**:
- Popolata dal backend in `GetAllVMs` (fetch config parallelo per ogni VM)
- QEMU: da `ipconfig0` → `extractVMIP`
- LXC: da `net0` → `extractVMIP`
- Mostra `—` se assente (VM senza cloud-init o agent)

**Disk usage nelle tabelle VM**:
- QEMU: caricato async tramite fsinfo (mountpoint `/`), mostrato come `bar(diskPct)`, placeholder `···` durante fetch
- LXC: usa `pct(v.disk, v.maxdisk)` da cluster resources
- Cache `v.diskPct` su ogni oggetto VM per re-render senza re-fetch

**Selettore nodi (VMs e Batch)**:
- Modello esplicito: `selectedNodes: Set<string>` sempre popolato (no "empty=all" shortcut)
- Inizializzato con tutti i nodi al primo caricamento
- `nodeAll()` → aggiunge tutti; `nodeNone()` → clear
- `doFilter()` usa `selectedNodes.has(v.node)` senza controllo size
- Nodi ordinati con `localeCompare({numeric:true})` in tutte le pagine

**Temi** (applicati via classe su `<html>`):
- `""` (nessuna classe) → segue `prefers-color-scheme`
- `html.dark` → scuro forzato
- `html.light` → chiaro (`#f8fafc` bg, `#0f172a` text)
- `html.eva01` → Evangelion Unit 01 (viola scuro, verde lime `#7cc443`)
- `html.catppuccino` → Catppuccin Mocha (Base `#1e1e2e`, Peach `#fab387`)
- `applyTheme(t)` aggiorna classi HTML + colori Chart.js `CD` object
- Preferenza salvata in `config.json` campo `theme`
- Media query `@media(prefers-color-scheme:light)` usa `:not(.dark):not(.eva01):not(.catppuccino)` per non sovrascrivere temi scuri

**Pattern sort tabelle**: `mkSorter(tbodyId, headId)` → `.sort(data, key)` con classi CSS `asc`/`desc`.

**Charts**: `mkChart(id, cfg)` distrugge il precedente se esiste. Defaults `CD` (oggetto mutabile). Palette `COLS`.

## Convenzioni codice Go

- Ogni handler chiama `h.getClientFor(clusterIdx(r))` per istanziare il client Proxmox. `clusterIdx(r)` legge `?cluster=N`. Solo API Token supportato.
- `writeJSON(w, data)` e `writeError(w, err, code)` per tutte le risposte.
- `bodyMap(r)` deserializza body JSON in `map[string]string`.
- `isForbidden(err)` → true se Proxmox risponde 403; endpoint non critici restituiscono array vuoto invece di 502.
- `rrdF` — tipo custom `json.Unmarshaler` per float64 nullable in RRD (`null` → `ok:false`).
- `avgSlice`/`maxSlice` — helper aggregazione slice float64.
- `pickTF(tf)` — valida/normalizza timeframe string.
- `extractVMIP(s)` — parsea `ip=X/CIDR` da config Proxmox (QEMU `ipconfig0` e LXC `net0`).
- `GetMetrics`, `GetReportData`, `GetAllVMs` usano `sync.WaitGroup` + goroutine per fetch parallelo.
- SQLite: `MaxOpenConns(1)` + WAL mode.

## Cosa NON fare

- Non aggiungere framework JS (React, Vue, ecc.) — il frontend deve restare un singolo file HTML.
- Non spostare `web/` fuori da `cmd/server/` — rompe l'embed.
- Non fare chiamate dirette da JS verso Proxmox — tutto passa per il proxy Go.
- Non disabilitare CGO — go-sqlite3 ne ha bisogno.
- Non usare `localStorage`/`sessionStorage` — preferenze salvate in `config.json` via backend.
- Non aggiungere `?ds=...` alle chiamate RRD — Proxmox 8 risponde 400.
- Non usare `c.Post()` per endpoint agent read-only (`get-fsinfo`) — usare `c.Get()`.
- Non reintrodurre collector SQLite — storico ora da RRD Proxmox.
- Non reintrodurre username/password — autenticazione solo via API Token.
- Non usare "empty=all" per `selectedNodes` nei selettori nodi — modello esplicito, inizializzare con tutti i nodi al load.
- Non mischiare selettori CSS con `@media` in liste comma — CSS non supporta quella sintassi (regole separate obbligatorie).
- Non usare `command[N]` per agent exec — Proxmox vuole repeated `command` keys (`url.Values.Add`).
- Non rimuovere il `copy()` in `config.Get()` — senza, handler che mutano `cfg.Clusters` (es. `GetConfig` che maschera i token) corrompono `current.Clusters` per aliasing Go; poi qualsiasi `SaveConfig` scrive i token mascherati su disco.
- Non inviare `api_token` completo al frontend in nessuna risposta — mascherare sempre con `...last8chars`.
