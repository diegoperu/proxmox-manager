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
| Frontend | Vanilla JS + Chart.js 4.4 + SheetJS (xlsx 0.20.3) + xterm.js 5.3 — **nessun framework**. Librerie e font **incorporati nel binario** (`web/static/`), nessun CDN |
| Config | `config.json` nella working dir del binario |
| Storico | Proxmox RRD nativo (no collector locale) |

## Struttura

```
proxmox-manager/
├── go.mod
├── build.sh                        # CGO_ENABLED=1 go build ./cmd/server/
├── cmd/server/
│   ├── main.go                     # entrypoint, router, embed, /static, tema in <html>
│   ├── main_test.go                # test: whitelist tema, /static (MIME, traversal), asset locali
│   └── web/
│       ├── templates/
│       │   └── index.html          # codice dell'app: SPA intera (1 file)
│       └── static/                 # librerie di terze parti + font vendorizzati (vedi THIRD_PARTY.md)
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

## Versione UI (auto-bump)

`<div id="ver">` nella `.sb-bottom` della sidebar (sotto l'orario) mostra `v0.<N>.0 · <data>`.
Aggiornata dal hook `.githooks/pre-commit` ad ogni commit: `N = git rev-list --count HEAD + 1`
(coincide col numero di commit dopo il commit). Attivazione: `git config core.hooksPath .githooks`
(già impostato nel repo locale; ri-eseguire dopo un clone fresco).
Serve a capire a colpo d'occhio se il binario servito è aggiornato o no.

## Regola critica: embed Go

`//go:embed web/templates/index.html web/static` è in `cmd/server/main.go`.
La cartella `web/` **deve stare nella stessa directory di `main.go`** (`cmd/server/web/`).
Go non supporta embed con path che escono dallo scope del file (`../../`).

### Asset statici (decisione 2026-10-13)

Chart.js, SheetJS, xterm.js, xterm-addon-fit e i font (Inter, JetBrains Mono, subset latin) sono **vendorizzati** in `cmd/server/web/static/` e serviti da `GET /static/*` (`staticHandler`): allowlist di estensioni (`.js .css .woff2`), `nosniff`, nessun listing, niente traversal. Motivo: la UI deve funzionare su VPN isolata e non eseguire codice da CDN. `index.html` resta l'**unico file di codice dell'app**; in `<head>` solo `/static/...` con `defer`.
Per aggiornare una libreria: scaricarla, confrontare l'hash con una seconda fonte, mettere la versione nel nome file, aggiornare `index.html` e `web/static/THIRD_PARTY.md` (versioni, licenze, SHA-256). `TestIndexUsesOnlyEmbeddedAssets` fallisce se compare una risorsa esterna o un file non incorporato.
SheetJS: usare >= 0.20.2 (le 0.18.x hanno CVE note); l'app lo usa solo per **scrivere** l'export Excel.

### Tema nel primo render

`serveIndex` inserisce `class="<tema> <densita>"` nel tag `<html lang="it">` leggendo `theme` e `density` da `config.json` (`injectRootClasses`; whitelist in `config.ValidTheme`/`config.ValidDensity`: tema `dark|light|eva01|catppuccino`, densita' `compact`; vuoto = nessuna classe); niente lampeggio e nessun `localStorage`. La pagina ha `Cache-Control: no-cache` perché varia con tema e densita'. `POST /api/config` accetta `{theme, density}` (campo assente = invariato) e risponde 400 se un valore non e' in whitelist.

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
  "density": "",
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
    Density      string          `json:"density"`
    CacheSeconds int             `json:"cache_seconds"`
    DBPath       string          `json:"db_path"`
    ListenAddr   string          `json:"listen_addr"`
}
```

Helper config: `GetCluster(idx int)` e `GetDefaultCluster()` leggono con RLock. `Get()` ritorna deep copy della slice `Clusters` — i caller possono mutare liberamente il valore ritornato senza corrompere `current`.

## API Routes (tutte su /api/*)

```
GET  /api/config                                    GetConfig (token mascherati)
POST /api/config                                    SaveConfig (theme, density; validati a whitelist)
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
GET  /static/*                                      asset incorporati (librerie, font); non è API
POST /api/nodes/{node}/qemu/{vmid}/dmesg             VMDmesg {lines:100|300|1000, level:all|warn} → ultime righe del dmesg del guest via guest agent (helper dmesg.sh o inline), solo VM QEMU Linux in esecuzione; 409 se ferma/Windows, 429 se gia' in lettura, mai in cache
GET  /api/nodes/{node}/qemu/{vmid}/osinfo            GetVMOSInfo: guest-get-osinfo normalizzato (id, name, pretty_name, version_id, kernel_release), {} se l'agent non risponde
GET  /api/nodes/{node}/qemu/{vmid}/netinfo           GetVMNetInfo: indirizzi dal guest agent (network-get-interfaces) normalizzati e limitati; `primary_ipv4` = primo IPv4 di un'interfaccia non virtuale (no lo/docker/veth/br-...), {} se l'agent non risponde. Serve alle VM senza IP in Proxmox (DHCP): lo usa `install-dmesg-helper.sh`
GET  /api/ceph/summary                              GetCephSummary: stato Ceph per la dashboard (salute, check, capacita', conteggi OSD/PG, I/O, recupero, quorum MON). Sola lettura, cache 10 s; `available:false` + `reason` (`not_configured` = 500 "binary not installed", `forbidden` = 403) se il cluster non ha Ceph o il token non puo' leggerlo
GET  /api/ceph                                      GetCeph: riepilogo + OSD (albero `/nodes/{n}/ceph/osd`), pool, MON/MGR/MDS, flag attivi; cache 4 s; parti non leggibili elencate in `partial`. Il nodo per le chiamate per-nodo e' il primo online
GET  /api/top?hours=6|12|24                          GetTopVMs: classifica delle 10 VM in esecuzione con piu' lavoro (CPU media x core assegnati, RRD `day`), IP dei soli 10 risultati, cache 60s per cluster+finestra, concorrenza 8
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

**Schede video (PCI passthrough, 2026-11-02):** `GetAllVMs` aggiunge `gpus:[{name}]` alle VM QEMU con una scheda video in `hostpciN`. Il config ha solo l'indirizzo (`0000:01:00`, `host=01:00.0;...`) o `mapping=nome`: modello e classe vengono da `/nodes/{n}/hardware/pci` (una chiamata per nodo, classe `0x03xxxx` = VGA/3D/display, quindi anche le A100 "3D controller"; la funzione audio della stessa scheda non conta) e da `/cluster/mapping/pci` (una chiamata, voci del nodo). Best effort: senza permesso (`Sys.Audit`, `Mapping.Audit`) niente badge, nessun errore. Solo dominio PCI 0000; indirizzi non riconosciuti non si indovinano. Frontend: badge verde "GPU" (×N) accanto al nome nella lista VM con modello in tooltip/aria-label e "· N con GPU" nel riepilogo (`gpuBadge`). Codice in `internal/handlers/gpu.go` (+test).

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
- Lista VM carica fsinfo in background post-render (`loadFsinfo()`); la dashboard non lo fa piu' (niente tabella VM)

### Esecuzione nel guest: helper etichettato o script inline (2026-10-14)

Tutto ciò che gira nel guest (add-user, operazioni disco) ha **una sola sorgente**: `internal/handlers/guestscripts/{adduser.sh,disk.sh}`, incorporata con `//go:embed` (`guestexec.go`). Lo stesso testo viene eseguito in due modi:

- **helper etichettato** `/usr/local/libexec/pmgr/<nome>.sh`, se presente nel guest: `AgentExec(["/usr/local/libexec/pmgr/adduser.sh", user, key])` — exec diretto, nessuna shell;
- **fallback inline** se manca: `AgentExec(["/bin/sh","-c", <testo>, "pmgr", user, key])`.

`probeHelper` decide una volta per richiesta con `[ -x "$1" ] && echo HELPER || echo INLINE` (builtin di shell: gira anche nel dominio confinato). Output diverso da `HELPER` → inline.

**Perché esiste l'helper (SELinux enforcing, verificato su Rocky 8.10):** la shell di `guest-exec` gira in `virt_qemu_ga_t`, che non può eseguire `useradd`/`usermod`/`lsblk`/`mount`/scrivere `fstab`; il boolean `virt_qemu_ga_run_unconfined` NON basta, perché abilita la transizione a `virt_qemu_ga_unconfined_t` solo per file etichettati `virt_qemu_ga_unconfined_exec_t`. L'helper è etichettato così e gira in un dominio completamente non confinato (`unconfined_domain_type`): nessun modulo SELinux custom. Sui guest senza SELinux (Debian/Ubuntu) basta l'inline.

**Preparazione del guest** (template/cloud-init, vedi `create_cloud_snippet.sh` fuori repo): `BLACKLIST_RPC` senza `guest-exec`/`guest-exec-status` in `/etc/sysconfig/qemu-ga`; `setsebool -P virt_qemu_ga_run_unconfined on`; helper in `/usr/local/libexec/pmgr` (root, 0750); `semanage fcontext -a -t virt_qemu_ga_unconfined_exec_t '/usr/local/libexec/pmgr(/.*)?'` + `restorecon -R`.

**Regole sugli script** (non negoziabili):
- i dati della richiesta arrivano **solo come argomenti posizionali** (`$1`, `$2`), mai nel testo dello script: niente `fmt.Sprintf` di dati dentro uno script;
- gli helper girano non confinati: devono restare **specifici e validati** (add-user, disco), mai un trampolino generico tipo `sh -c "$@"`; un nuovo subcommand va aggiunto con la sua validazione;
- `adduser.sh` esporta `SYSTEMD_NSS_DYNAMIC_BYPASS=1`: senza, `useradd` nel dominio dell'agent attende il timeout D-Bus da 25 s a ogni lookup di un nome inesistente (>2 minuti);
- **Solo ASCII (2026-10-14, verificato su Ubuntu 26.04, VM 151):** nessun argomento di `guest-exec` — quindi nemmeno il testo degli script, commenti e messaggi compresi — deve contenere caratteri non ASCII. Proxmox tronca il messaggio verso l'agent di (byte - caratteri): con 1 byte extra passa (di fortuna), con 2 o più la richiesta va in `got timeout`, il comando non arriva mai all'agent e **l'agent resta bloccato** (anche `agent/ping` risponde "not running") finché non si riavvia `qemu-guest-agent` nella VM. Misurato: 6000 byte ASCII passano, `à`x1 passa, `à`x2 e `—`x1 no. Difese: `runGuestArgv` rifiuta argomenti non ASCII (`errNonASCIIArg`), `TestGuestScriptsAreASCII` blocca gli script. Gli accenti nei messaggi degli script si scrivono senza (`gia'`, `e'`).
- modificare uno script significa cambiare **anche** l'helper installato nei template: tenere stabile il contratto degli argomenti.

**Verificato end to end il 2026-10-14** (Rocky 8.10, SELinux `Enforcing`, nessun dominio permissive, binario v0.51.0 → VM di test): add-user ramo password e ramo chiave (login SSH reale + sudo, etichette `ssh_home_t`), 409 su utente esistente, 400 su chiave con apice/comando (nessun effetto nel guest); `setup-disk` su disco vergine (GPT + `mkfs.ext4` + `fstab` per UUID + mount `1777`, ~6 s, nessun riavvio forzato) e su disco lasciato a metà; 409 con più candidati (nessuna modifica). `useradd` risulta eseguito in `virt_qemu_ga_unconfined_t` (audit), 0 AVC `virt_qemu_ga`.
**Fallback inline su guest enforcing senza helper:** esito su Rocky 8.10 = Proxmox 500 `guest-exec failed - got timeout` prima che il comando parta. Attribuito a SELinux il 2026-10-14, ma **la causa è probabilmente un'altra** (vedi "Solo ASCII" sopra: gli script inline contenevano caratteri non ASCII); il test su Rocky va **rifatto** con gli script ASCII prima di concludere che l'inline non funzioni con SELinux enforcing. Gli helper restano necessari solo per i guest con SELinux (la shell dell'agent è confinata in `virt_qemu_ga_t`); Ubuntu/Debian usano l'inline.

### AddVMUser — aggiunta utente via guest agent exec

`POST /api/nodes/{node}/qemu/{vmid}/adduser` body: `{"username":"nomeutente","ssh_key":"..."}`

Flusso handler `AddVMUser`:
1. Valida username con regex `^[a-z_][a-z0-9_-]{0,31}$` (400 se non valido)
2. `normalizeSSHKey`: una sola riga `tipo base64 [commento]`, tipi ssh-rsa/ssh-ed25519/ecdsa-nistp*/sk-*, niente newline, max 16 KB (400 altrimenti). Vuoto = ramo password
3. `probeHelper` + `addUserTool.run(..., [username, key], 30s)`; agent non raggiungibile/frozen/guest-exec disabilitato → 502 con messaggio tradotto (`agentError`, dettaglio solo nei log)
4. exit 3 dallo script → **409**: l'utente esiste già, nessuna modifica (una richiesta non può resettare password/chiavi/sudo di un account esistente, root compreso)
5. Risponde `{"output":"...", "exitcode":N}` (nessun campo password)

Comportamento di `adduser.sh`:
- `set -eu`: nessun errore nascosto (niente `2>/dev/null || true`), stderr nel risultato;
- gruppo admin: `wheel` e/o `sudo`, solo quelli che esistono (`getent group`);
- **ramo password** (nessuna chiave): password fissa `p4ssw0rd` **voluta** (decisione utente 2026-10-13, rischio accettato: VM interne, rete fidata), `chage -d 0`. Se `chage` fallisce l'account viene **bloccato** (`usermod -L`) ed esce 1: la password fissa non resta mai usabile. **Non introdurre password casuali**. Il frontend mostra l'hint solo con campo chiave vuoto (`#adduser-pwhint`);
- **ramo chiave**: `~/.ssh` 700, `authorized_keys` 600 con **una sola riga** (`printf >`, idempotente), `restorecon -R` (senza `ssh_home_t` sshd rifiuta la chiave su SELinux), sudoers `NOPASSWD` 440. Nessuna password impostata.

Client methods: `AgentExec`, `AgentExecStatus`.
⚠️ Proxmox agent exec vuole repeated `command` keys, **non** `command[0]`/`command[1]`.

## Aggiunta disco VM (POST /api/nodes/{node}/qemu/{vmid}/adddisk + /setup-disk)

Solo VM QEMU. Setup lato guest via QEMU Guest Agent exec con `guestscripts/disk.sh` (helper etichettato o fallback inline, vedi sopra), non Ansible reale. `disk.sh` ha 3 subcommand: `find-blank`, `rescan`, `setup <device> <mount> <perm>`; l'orchestrazione (quale disco, retry, 409, riavvio) resta in Go.

**`POST /nodes/{node}/qemu/{vmid}/adddisk`** — `AddVMDisk`
```json
{"storage": "local-lvm", "size_gb": 50, "bus": "scsi"}
```
Legge config VM, trova primo slot libero per il bus (`nextDiskSlot`, limiti in `diskSlotLimits`: scsi 0-30, virtio 0-15, sata 0-5, **condivisi** con la validazione di `setup-disk`), chiama `VMSetConfig` con `<slot>=<storage>:<size_gb>`. Funziona indipendentemente dallo stato power della VM. Risponde `{"disk":"scsi1","storage":"...","size_gb":50}`.

**`POST /nodes/{node}/qemu/{vmid}/setup-disk`** — `SetupVMDisk`
```json
{"disk": "scsi1", "mount_point": "/data", "sticky_bit": true}
```
Richiede VM `running` con guest agent attivo. Filesystem ext4, partizionamento GPT singola partizione 100%. Permessi: `1777` (sticky, default) o `0777` se `sticky_bit:false`. `validateMountPoint` (`mountPointPattern` + `path.Clean` + blocco per **prefisso** di `/etc`, `/boot`, `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/var`, `/proc`, `/sys`, `/dev`, `/root`, `/home`, `/opt`, `/run`, `/tmp`) impedisce mount point pericolosi: `/etc/`, `/etc/ssh`, `/var/log` sono rifiutati (400). `validateDiskSlot` rifiuta slot oltre il limite del bus (`sata6`, `virtio16`).

Rilevamento nuovo device — **per proprietà, non per diff temporale**:
1. `disk.sh find-blank`: tra tutti i `TYPE=disk` di `lsblk`, candidato è o (a) un disco vergine — nessuna partizione figlia (`lsblk -ln /dev/$d | wc -l` == 1) e senza filesystem (`blkid` vuoto) — o (b) un disco con una sola partizione ext4 non montata (`findmnt -S`) — quest'ultimo caso copre il retry dopo un tentativo di setup fallito a metà (partizionato/formattato ma non montato/fstab)
2. Se nessun candidato: rescan bus (`partprobe` + `echo "- - -" > /sys/class/scsi_host/host*/scan` + `udevadm settle`) e retry (5 tentativi, 2s apart)
3. **Fallback**: se ancora nessun candidato, forza un ciclo `VMAction(shutdown, forceStop=1)` + `WaitForTask` + `VMAction(start)` + `WaitForTask`, poi attende il guest agent tornare raggiungibile (poll `echo ready`, timeout 2 minuti), infine ripete `findBlank` — autorizzato esplicitamente, nessuna conferma richiesta
4. Se `findBlank` trova **più di un candidato**: errore esplicito (409, disambiguazione richiesta all'utente) invece di sceglierne uno a caso — evita di formattare il disco sbagliato

⚠️ **Perché non un diff prima/dopo `lsblk`** (bug reale osservato e corretto): su Proxmox/QEMU con machine type q35+PCIe il disco può venire collegato a caldo **immediatamente** alla `PUT /config` (prima ancora della chiamata a `setup-disk`). Una baseline "prima" presa a inizio richiesta lo troverebbe già presente, quindi nessun diff lo segnalerebbe mai come "nuovo" — risultato: `lsblk` manuale mostrava il disco (es. `vdb`), ma il diff falliva sempre, anche dopo un vero stop+start. La causa non era il tipo di riavvio (era già corretto: stop+start con `WaitForTask`, non `reboot` ACPI) ma il metodo di identificazione stesso.

Setup (`disk.sh setup`): `parted mklabel gpt` + `mkpart primary 0% 100%` (idempotente, skip se `blkid` già trova la partizione) → `mkfs.ext4` + `udevadm settle` (idempotente, skip se già formattata) → `mkdir -p <mount>` → riga UUID in `/etc/fstab` (idempotente) → `mount` (idempotente) → `chmod <perm> <mount>`.

⚠️ **Bug reale corretto**: `UUID=$(blkid -o value -s UUID "${DEV}1")` subito dopo `mkfs.ext4`, senza `udevadm settle`, poteva restituire stringa vuota (race blkid/cache). Con `UUID=""`, `grep -q "$UUID" /etc/fstab` (pattern vuoto) matcha qualunque riga non vuota → il check "già presente" risultava vero per errore, la riga fstab non veniva mai scritta, e il successivo `mount /data` falliva con `can't find in /etc/fstab`. Fix: `udevadm settle` dopo `mkfs.ext4`, probe non cache (`blkid -c /dev/null`), e `exit 1` esplicito se `UUID` risulta vuoto invece di proseguire silenziosamente.

Risponde `{"log":[...righe...], "device":"/dev/sdX", "mount_point":"/data"}`.

⚠️ **Limite noto (da correggere):** `setup-disk` valida `disk` ma **non lo usa** per scegliere il device (sceglie l'unico disco vuoto); se non ne trova forza uno stop+start della VM e risponde 502 "disco non rilevato nemmeno dopo il riavvio", anche quando il disco è già configurato. Un `lsblk` che fallisce nel guest ora è un errore esplicito (exit 3), non "nessun disco".

Client methods riusati: `AgentExec`, `AgentExecStatus`, `VMSetConfig`, `VMAction`. Nessun nuovo metodo client aggiunto.

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
- Frontend (2026-10-13): il gateway digitato a mano non viene sovrascritto dall'auto-calcolo (`gwManual`; svuotarlo o scegliere un template lo riattiva); il nome VM è validato (`^[A-Za-z0-9][A-Za-z0-9.-]*$`, max 253) prima della chiamata; `#pv-summary` mostra cosa verrà creato e si aggiorna mentre si compila (solo `textContent`).

## SQLite schema (solo cache)

```sql
api_cache(key TEXT PK, value TEXT, expires_at INTEGER)
```

Nessuna tabella metrics — storico completamente delegato a Proxmox RRD.

## Frontend (index.html — SPA vanilla)

**Pagine** (routing client-side):
- `dashboard` — **"Bollettino"** (branch `experiment/ui-redesign`): frase di condizione (sereno / quasi sereno / attenzione, calcolata da nodi offline, RAM/CPU >=85%, task falliti 24h) + striscia di 4 KPI con storico (sparkline da `/api/metrics?timeframe=day`, barre task da `/api/tasks`) + un riga per nodo con barre min-max 24h CPU/RAM (RRD `day` per nodo, punto = valore attuale, tratto = soglia 85%, aggiornate sul posto) e un punto per VM (pieno = in esecuzione, vuoto = ferma, diametro = RAM) + colonna Attivita' (`/api/tasks`) + **Top 10 VM & Container** (le VM che hanno lavorato di piu' nelle ultime 6/12/24 h, da `/api/top`: andamento CPU come previsione oraria con punto sul picco, CPU media e core usati, picco, RAM, condizione sereno/variabile/temporale con glifo meteo (temporale = media >= 75% o >= 10% del tempo oltre l'85%, `hot_pct`; un picco isolato non basta); sotto 1600px tabella a colonne fisse, sotto 1320px senza RAM (verificato con nomi lunghi: 4K al 100% non ha problemi); il nome apre il pannello VM); ordine iniziale: in esecuzione, poi spente, template in fondo (poi per VMID), intestazioni ordinabili come prima
- `ceph` (2026-11-02, solo dove `/api/ceph/summary` risponde `available:true`: voce di menu `hidden` altrimenti, `Ceph.probe()`/`Ceph.availBy`): "osservatorio meteo", modulo `Ceph` in `index.html`. Titolo di condizione (`Ceph.stateOf` e' l'unica definizione: HEALTH_ERR o capacita' >= 95% = Attenzione; HEALTH_WARN/ignota o capacita' >= 85% = Quasi sereno; OSD giu' e recupero sono voci della sottoriga), tre strumenti (pluviometro = capacita' con tacche 85/90/95, anemometro = I/O + IOPS con curva degli ultimi 10 min tenuta solo in memoria, fronte = recupero con % da `progress_events`, fine stimata solo se `eta_seconds`), avvisi dai health check (gravita' = parola + forma della linea; silenziati in grigio), stazioni OSD (riga per nodo, tessera per OSD: riempimento = spazio usato, giu' = bordo tratteggiato rosso, fuori = punteggiato), barra dei PG per stato, pool, servizi e flag attivi (con spiegazione di noout & co.). Aggiornamento ogni 5 s solo con pagina aperta e scheda visibile (`Ceph.stop()` quando si cambia pagina, risposte superate scartate con `seq`); se il polling fallisce restano i dati vecchi con "non raggiungibile · dati delle HH:MM:SS". Dashboard: riquadro `#dash-ceph` sotto i KPI (cliccabile) e la frase di condizione include lo stato Ceph. Ogni testo esterno via `esc()`.
- `nodes` — card per nodo (ordinati natural sort) + RRD grafici (memused/memtotal) + VM per nodo
- `vms` — tabella QEMU+LXC con colonna IP; multi-nodo selector (natural sort, modello esplicito); sort; disk% da fsinfo async; **selezione a checkbox con barra flottante** (ripristinata il 2026-11-02, D6 opzione B: la barra non esegue, apre Batch con VM e azione preselezionate via `VMs.toBatch` → `Batch.preselect`; le operazioni di massa si confermano e si loggano solo in `batch`)
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
- (default, `dark` o OS scuro) → "cielo notturno" `#0b1424`, accento azzurro `#7fb2ff`
- `html.light` → chiaro "alba" (`#eef2f8` bg, `#0d1b33` text, accento `#1d5fd1`)
- `html.eva01` → Evangelion Unit 01 (viola scuro, verde lime `#7cc443`)
- `html.catppuccino` → Catppuccin Mocha (Base `#1e1e2e`, Peach `#fab387`)
- `applyTheme(t)` aggiorna classi HTML + colori Chart.js `CD` object
- Preferenza salvata in `config.json` campi `theme` e `density` (`html.compact` = densita' compatta: `--card-pad`, `--cell-y`, `--row-h`)
- Token: `--accent*` e' il colore di marca; `--amber*` restano **alias** (JS usa `tk('--amber')`). Nuovi: `--on-accent`, `--on-danger`, `--hover`, `--scrim`, `--sky-glow`, `--shadow-pop`
- Media query `@media(prefers-color-scheme:light)` usa `:not(.dark):not(.eva01):not(.catppuccino)` per non sovrascrivere temi scuri

**Pattern sort tabelle**: `mkSorter(tbodyId, headId)` → `.sort(data, key)` con classi CSS `asc`/`desc`.

**Charts**: `mkChart(id, cfg)` (asincrona) distrugge il precedente se esiste. Defaults `CD` (oggetto mutabile; assi dai token del tema). Palette serie: `seriesCols()` (token del tema, non più una costante `COLS`). Le serie si dichiarano con `...ser('--amber', alpha)`: il colore viene dal token e la serie lo ricorda (`_tok`, `_a`; le barre per-VM usano `_cols:true`). `applyTheme` chiama `recolorCharts()`, che ricolora serie, assi e legenda dei grafici già disegnati **senza rifare le richieste**: un grafico nuovo deve usare `ser()` o `_cols`, mai colori letterali. Ogni grafico ha un riepilogo testuale (`describeChart`: ultimo/min/max, o i 3 valori più alti per le barre) in un `div.sr` collegato con `aria-describedby`.

**Sicurezza output (2026-10-13)**: ogni dato esterno (API, config, input) interpolato in `innerHTML` passa da `esc(v)`. Argomenti JS dentro attributi `onclick="..."` passano da `js(v)` (JSON + escape attributo), mai `'${v}'`. Log e toast sono solo testo: `logLine(el, text, cls)` e `toast()` usano `textContent`.

**Accessibilità (2026-10-13)**: nav sidebar e tab sono `<button>` (`aria-current`, `role=tab`/`aria-selected`, frecce); `th.s` ordinabili hanno `tabindex=0` + `aria-sort` (impostato in `mkSorter`), Invio/Spazio attivano `th.s` e `[role=button][tabindex]` (delegato in `document keydown`). Modal: `openModal`/`closeModal` gestiscono una pila (`_modalStack`): `role=dialog`, sfondo e modal sottostanti `inert`, i modal chiusi sono `inert`, Esc chiude quello in cima, il focus torna a chi l'ha aperto (`A11y.init()` all'avvio). Ogni `<label class="fl">` ha `for=`, ogni controllo ha etichetta o `aria-label`. `:focus-visible` globale con `--amber`; `prefers-reduced-motion` toglie animazioni di toast, modal e spinner (resta "…") e l'animazione dei grafici.

**Struttura semantica (2026-10-13)**: `<header class="topbar">`, `<aside id="sidebar" aria-label="Menu">` con `<nav>`, `<main id="content" tabindex="-1">`; il titolo di pagina è l'unico `<h1 id="ptitle">`, le intestazioni di sezione `.sec` sono `<h2>` (stile invariato). Skip link `#skip-link` come primo elemento del body (inert con i modal). Gli spinner sono `role="status"` con testo `.sr` ("Caricamento in corso"); ogni `<canvas>` è `role="img"` con `aria-labelledby` sul titolo `.ct` del suo riquadro (assegnato in `A11y.init`).

**Librerie a richiesta**: nessuno script di libreria nell'`<head>`. `lib('chart'|'xlsx'|'xterm')` carica da `/static/` al primo uso e memorizza la promessa (se fallisce si può riprovare). `mkChart` è asincrona; `Reports.xlsx` fa `await lib('xlsx')`; la console in popup carica solo xterm + fit (non Chart.js né SheetJS).

**Sistema operativo e dmesg (2026-11-02)**:
- Lista VM: colonna **Sistema** (`osLabel`/`osFromType`). Per le VM QEMU in esecuzione viene dall'agent (`/osinfo`, `loadOsinfo()` in background, pool 6, cache 10 min) come "Nome versione" (es. "Ubuntu 24.04", "Rocky 8.10"); per le altre dal tipo OS di Proxmox (`ostype` aggiunto da `GetAllVMs`), in grigio e con tooltip. Il pannello VM mostra anche kernel.
- **IP dal guest agent**: per le VM QEMU in esecuzione senza IP in Proxmox (DHCP, configurazione manuale) la lista VM (`loadAgentIps`, pool 6, cache 10 min), il pannello VM e la Top 10 (`vmIPFromConfig`, attesa massima 5 s) usano `/netinfo`; nella lista l'indirizzo e' grigio con tooltip, nel pannello c'e' "· dal guest agent".
- **dmesg** (`Dmesg` in `index.html`, `internal/handlers/dmesg.go`, `guestscripts/dmesg.sh`): nel pannello VM, sezione "Log del kernel" che legge solo al clic (nessun precaricamento, mai in cache); "Espandi" apre la vista larga `#mdm` con ricerca, copia e download. Righe 100/300/1000, livello tutto/avvisi+errori, evidenziazione ERR/WARN (parola + colore). Disabilitato con il motivo per LXC, VM ferma, Windows.
- Sicurezza: stessi principi delle altre operazioni nel guest. Solo due valori da whitelist (righe, livello) arrivano allo script come argomenti posizionali; `dmesg.sh` e' a sola lettura (testato: nessuna scrittura, nessun `dmesg -c`), ASCII-only, `set -eu`, exit 2 argomento non valido, exit 3 dmesg non leggibile. Output ripulito (UTF-8, ANSI, caratteri di controllo, tetto 512 KB e 2000 caratteri per riga) e mostrato solo con `textContent`. POST (esegue un comando nel guest), una lettura alla volta per VM, nel log solo conteggi e modo, mai il contenuto.
- **SELinux:** su Rocky/RHEL enforcing serve l'helper `dmesg.sh` in `/usr/local/libexec/pmgr/` (etichettato `virt_qemu_ga_unconfined_exec_t` come gli altri): vanno rigenerati e risincronizzati gli snippet cloud-init (fuori dal repo). Senza helper l'esecuzione inline puo' fallire con exit 3 e il messaggio lo dice. Su Debian/Ubuntu basta l'inline (verificato su Ubuntu 24.04 reale: 300 righe in ~3 s).
- Difese emerse dalla revisione di sicurezza (Opus): tetto di 32 MiB alle risposte di Proxmox (`api.maxResponseBytes`), solo le ultime N righe tenute anche se il guest ignora `tail`, bidi e controlli rimossi, log del server solo con conteggi (stderr del guest troncato a 200 caratteri), una lettura per VM + massimo 4 in tutto, tetto di 60 s con la goroutine protetta da `recover`, `level_applied` (se il dmesg del guest non supporta `--level` la UI dice che il log non e' filtrato), `Object.hasOwn` per i nomi OS, ricerca ritardata di 150 ms, `api({timeoutMs})` con AbortController.
- **Guardia `/api` (`cmd/server/guard.go`)**: `Sec-Fetch-Site` same-origin/none, `Origin` uguale a `Host` (o `X-Forwarded-Host`), Content-Type JSON per POST/PUT/DELETE, `allowed_hosts` opzionale in config.json (anti DNS rebinding, vuoto = nessun controllo). Copre anche il WebSocket della console (il suo `CheckOrigin` accetta tutto). Dietro reverse proxy il nome host va preservato. Gli script esterni devono mandare `Content-Type: application/json`.
- Se il guest agent e' in stato `frozen` (fsfreeze non rilasciato) guest-exec e' bloccato: l'errore lo dice, non e' un bug dell'app.

**Pagine secondarie (2026-11-02)**: componenti condivisi `.toolbar`, `.seg` (controllo segmentato), `.panel` (+ `.panel-h`/`.panel-b`), `.nc` (scheda nodo con `meter()`), `.tiles` (tessere tema), `.cl-row`, `.dl` (lista definizioni), `.empty`. `nodeState(o)` e' l'unica definizione dello stato di un nodo (Panoramica, Nodi, Cluster); soglie in `Dashboard.WARN/CRIT`. Nodi: schede + pannello VM/grafici visibili solo dopo la scelta di un nodo. VM: toolbar con riepilogo (`#vm-sum`), il nome apre il pannello, nessun rosso a riposo; sotto 650px restano VMID, Nome, Stato, RAM. Batch: solo restyle; il bottone diventa rosso pieno solo per azioni `danger` (`batchActionChanged`). Cluster: zona distruttiva isolata (`.sh-danger`) con conferma scritta. Report: contenuto nascosto finche' non si genera, striscia KPI, grafici a tinta unica (rosso/ambra solo oltre soglia). Impostazioni: colonna singola, tessere tema con anteprime colore (gli esadecimali delle anteprime sono voluti), densita' segmentata con anteprima immediata e salvataggio esplicito.

**Navigazione e pannelli (2026-11-02, step 3)**:
- **Indirizzi nell'hash** (`Route`): `#/<pagina>` e `#/vm/<nodo>/<vmid>/<qemu|lxc>` (pannello VM); Indietro/Avanti funzionano (`popstate`); ogni valore letto dall'hash e' validato a whitelist/regex. `App.nav(p,{silent:true})` non scrive nella cronologia.
- **Dettaglio VM = pannello laterale** (`#mov.ov.sheet`, `VMs.detail`): sostituisce il modal; l'azione distruttiva (stop forzato) sta isolata in fondo e a contorno. Si apre anche dal nome VM nella tabella della dashboard e dalla ricerca.
- **Ricerca rapida** `Ctrl/⌘ K` (`Palette`, `#mpal`): pagine (con sinonimi), azioni, nodi, VM/LXC da `/api/vms` (cache 60s). Tutto il testo via `textContent`; si apre solo se non c'e' un altro dialogo. Clic sullo sfondo chiude pannello e ricerca.
- **Provisioning**: barra di avanzamento a 4 passi (`Provision.steps`, `Provision.goto`) calcolata dagli stessi campi del riepilogo; le sezioni restano tutte visibili.
- Scala inline ripulita: niente font da 9/10px, niente maiuscolo spaziato, raggi 8/10px negli stili inline.

**Colore "warn"**: `--warn` (default = `--amber`) è il colore di attenzione per soglie e barre (`.warn2`, `.twn`, `.card-v.w`); `--amber` resta il colore di marca (selezione, azione primaria). In eva01 il marchio è lime e si confondeva col verde "ok", quindi `--warn:#ffa24a`; sempre in eva01 `--cyan` (badge QEMU) è `#2dd4bf`. Verificare ≥4.5:1 per ogni nuovo ruolo.

**Nomi degli helper globali**: `tk`, `tka`, `esc`, `js`, `lib`, `pool`, `apiCached`, `logLine`, `errRow`, `errBox` sono funzioni globali. **Non** dichiarare variabili locali con questi nomi: una `const tk=[...]` in `Reports.load` oscurava `tk()` e rompeva i grafici dei Report (TDZ) senza errori di sintassi.

**Tipografia (2026-11-02, Bollettino)**: Inter variabile (UI e cifre tabellari) + JetBrains Mono solo per VMID, IP, log. Scala: 30 condizione, 26 KPI, 17 titoli modal, 15 titoli sezione/pagina, 13 body, 12 tabelle/controlli/etichette, 11 solo badge (16 solo input con `pointer:coarse`). Niente maiuscolo spaziato: etichette in frase. Barlow Condensed rimosso (`--cond` e' un alias di `--sans`). DESIGN.md e `.impeccable/design.json` descrivono il look Bollettino (riscritti il 2026-11-02 dal codice). `.card-v`/`.mv` usano `tabular-nums`. Nomi di VM nelle tabelle: `<span class="nm" title="…">` con ellissi a 34ch (il nome completo è nel `title`).

**Adattamento (2026-10-13)**: desktop-first; il mobile è per consultazione. Sotto 850px la sidebar diventa un drawer (`#menu-btn` in topbar, `Drawer` in JS: Esc/scrim/click su una voce lo chiudono; chiuso è `visibility:hidden`, aperto rende `#main` `inert`); sotto 650px niente badge in topbar, padding ridotto, toast a tutta larghezza. Batch impila le colonne sotto 850px (`.batch-cols`/`.batch-side`). I modal non superano mai `100vw - 24px` (`min()` nel CSS e negli stili inline). Bersaglio minimo 24px per `.sm` (WCAG 2.5.8); con `pointer:coarse` 44px, campi a 16px (niente zoom su iOS) e checkbox da 22px. Altezze con `dvh`. **Ordine nel CSS**: le regole di base di un componente vanno *prima* delle sue media query (a pari specificità vince la più bassa: una regola base in fondo al file annullava il responsive del Batch). Il comportamento `pointer:coarse` non è verificato su un dispositivo touch.

**Richieste per-VM e colori dei grafici**: le richieste per ogni VM (fsinfo, status) passano da `pool(items, 6, fn)` (concorrenza limitata) e da `apiCached(path, ttl)` (cache 30s per cluster, errori non memorizzati; `dropVmCache`/`_tc.clear()` dopo azioni). `checkConn` salta il sondaggio se la scheda è nascosta. Le serie dei grafici e il popup console leggono i colori dai token (`tk('--amber')`, `tka('--amber',.1)`, `seriesCols()`): niente colori letterali.

**Colori e contrasto**: ogni colore usato come testo deve essere ≥4.5:1 su `--bg/--bg2/--bg3` in tutti i temi (badge sul loro velo compresi). `--text3` ridefinito per tema. Il tema chiaro ridefinisce gli accenti come scuri (`--amber`, `--green`, `--red`, `--blue`, `--cyan`, `--purple`); il riempimento del bottone primario usa `--amber-fill`/`--amber2-fill` (non `--amber`). I colori degli assi dei grafici si leggono dai token (`applyTheme` → `getComputedStyle`), niente copie letterali.

**Conferme**: `askConfirm({title, lines, consequence, items, level, typed, okLabel}, onOk)` sostituisce la vecchia `confirm()` (che oscurava `window.confirm`). Livelli: `safe` (start, resume, snapshot), `warn` (shutdown, reboot, suspend), `danger` (stop forzato, reset, formattazione disco, reboot/shutdown nodo, elimina cluster). `typed` (testo da digitare) solo per formattazione disco (VMID) e reboot/shutdown nodo (nome nodo, o numero di nodi se più d'uno). Mappa azioni VM in `VM_ACTIONS`.

**Errori**: `api(path, {quiet:true})` non mostra toast; il chiamante gestisce l'errore in pagina. Liste che non caricano mostrano `errRow()`/`errBox()` con "Riprova", mai "Nessuna VM". fsinfo/status/rrd/checkConn sono `quiet`. Toast identici entro 5s deduplicati; errori visibili 8s, chiusura al click.

**Batch**: template nascosti di default (`isTemplate()`: `template==1` o nome contiene "template", stesso criterio di `GetTemplates`), checkbox "Mostra template". La selezione sopravvive ai filtri (scelta utente): `#binfo` e la conferma dichiarano le VM selezionate non visibili. Il risultato riporta i fallimenti reali.

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

- Non aggiungere framework JS (React, Vue, ecc.) — il codice dell'app resta in un solo file HTML (`index.html`). Le librerie di terze parti stanno **solo** in `web/static/`.
- Non caricare script, stili o font da CDN o da host esterni: vendorizzare in `web/static/` e aggiornare `THIRD_PARTY.md`.
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

## Branch sperimentale `experiment/ui-redesign`

Creato il 2026-11-02 da `main` (d5fa513) per sperimentare grafica e UX nuove ispirate ai servizi cloud moderni. Decisioni 2026-11-02 (`DOMANDE.md` D1-D6): ambito completo (pelle + flussi + dashboard) a fasi; nomi tema invariati, auto/dark/light col nuovo look "Horizon", eva01/catppuccino come palette sulla stessa struttura; due densita' (comoda/compatta) in `config.json`; Inter vendorizzato + JetBrains Mono per dati; anteprima su `:8081` con dati finti; barra azioni flottante = assunzione provvisoria (D6, l'utente valuta alla fine). **Direzione scelta 2026-11-02: "Bollettino"** (cluster come bollettino meteo: frase di condizione, barre min-max 24h per nodo, centro attivita'); contratto in `.impeccable/surfaces/cmd-server-web-templates-index-html.md`, schizzo `.impeccable/mocks/decision/assigned.html`. Nota: "Horizon" era il nome provvisorio, sostituito da "Bollettino". `main` e `proxmox-manager-use/` non vengono toccati. Vincoli invariati: un solo `index.html`, vanilla JS, asset vendorizzati, nessun CDN, temi di brand (vedi `PRODUCT.md`).

## Registro modifiche

- 2026-11-02 — creato branch `experiment/ui-redesign`, aggiunto `DOMANDE.md` (D1-D6) — avvio sperimentazione redesign UI/UX, discussione prima del codice — `CLAUDE.md`, `DOMANDE.md`
- 2026-11-02 — risposte D1-D6 registrate, avvio step 1 (direzione visiva) — `CLAUDE.md`, `DOMANDE.md`
- 2026-11-02 — scelta direzione "Bollettino" tra 5 (scartate: Riepilogo Salute, Console Grafite, Linee, standard di categoria) — fine step 1, prossimo step 2 implementazione — `.impeccable/surfaces/…index-html.md`, `.impeccable/mocks/decision/`
- 2026-11-02 — step 2: token "Bollettino" (5 temi + 2 densita'), Inter vendorizzato (Barlow rimosso), nuova shell (barra superiore + menu + briciole), dashboard Bollettino; backend: `density` in config, `config.ValidTheme/ValidDensity`, `SaveConfig` valida a whitelist con campi opzionali, `injectRootClasses` — richiesto da D2/D3/D4 — `cmd/server/web/templates/index.html`, `cmd/server/main.go`, `internal/config`, `internal/handlers/handlers.go`, `web/static/`
- 2026-11-02 — anteprima su `:8090` e non `:8081`: la 8081 e' occupata da un altro servizio (podman/pasta) — dati finti da `devtools/mockpve/mockpve.py` (D7: nel repo, sola lettura, solo 127.0.0.1; uso nel suo README)
- 2026-11-02 — D7 risolta (opzione B): `mockpve.py` aggiunto al repo con README; D8 presa d'atto — `devtools/mockpve/`, `DOMANDE.md`, `CLAUDE.md`
- 2026-11-02 — step 3: hash routing con deep link VM, pannello laterale VM, ricerca Ctrl/⌘ K, avanzamento provisioning, pulizia stili inline; mock con VMID univoci — D1 (tutto) — `cmd/server/web/templates/index.html`, `devtools/mockpve/mockpve.py`
- 2026-11-02 — revisione finale (2 giri) e fix: pannello VM sensibile allo stato, glifi sostituiti da SVG/parole, attivita' sopra la tabella sotto 1180px, tabella dashboard leggibile su mobile, 'da HH:MM' sotto lo stato del nodo (scartata la tacca sulla barra: l'asse e' il valore, non il tempo); DESIGN.md riscritto — non costruiti: tacche 'dato vecchio/spento' a mezza altezza e View Transitions (promessi nel contratto), barra azioni flottante (D6 aperta) — `DESIGN.md`, `.impeccable/design.json`, `cmd/server/web/templates/index.html`
- 2026-11-02 — ridisegno pagine Nodi, VM, Batch (restyle), Cluster, Report, Impostazioni con i componenti della dashboard; `nodeState` unico; revisione e fix (stato coerente tra pagine, grafici Report senza colori di stato, tabelle mobile con Stato visibile) — richiesta utente — `cmd/server/web/templates/index.html`, `devtools/mockpve/mockpve.py`
- 2026-11-02 — D6 risolta (opzione B): colonna checkbox e barra di selezione nella lista VM (sticky in basso, Esc deseleziona, dichiara le VM nascoste dai filtri, handoff a Batch); pulsante Console ripristinato nelle righe VM (era stato tolto dal ridisegno senza richiesta: appare solo con `has_serial` e credenziali console) — richiesta utente — `cmd/server/web/templates/index.html`, `devtools/mockpve/mockpve.py`
- 2026-11-02 — dashboard: la tabella VM & Container (ridondante) e' sostituita da Top 10 VM & Container; nuovo endpoint `/api/top` (ranking testato in `internal/handlers/top_test.go`), rimossi DashTbl/loadDashMem/loadDashFsinfo (meno richieste per-VM al caricamento) — richiesta utente — `internal/handlers/top.go`, `cmd/server/main.go`, `cmd/server/web/templates/index.html`
- 2026-11-02 — Top 10: condizione basata sulla durata (`hot_pct` dal backend, test), tabella e righe nodo robuste a risoluzioni <= 1600px con nomi reali lunghi (il nome nodo andava a capo a ogni risoluzione: colonna a 150px senza a-capo); scartata la riduzione dei punti della curva (l'utente la preferisce frastagliata) — dati reali del cluster CALCOLO, richiesta utente — `internal/handlers/top.go`, `cmd/server/web/templates/index.html`, `devtools/mockpve/mockpve.py`
- 2026-11-02 — branch `feat/vm-dmesg`: colonna Sistema operativo nella lista VM (agent + ripiego su ostype), dmesg on-demand nel pannello VM con vista larga (D9-D13), helper `guestscripts/dmesg.sh`, endpoint `/dmesg` e `/osinfo`, mock con exec finto — richiesta utente — `internal/handlers/dmesg.go` (+test), `internal/handlers/guestscripts/dmesg.sh`, `cmd/server/web/templates/index.html`, `devtools/mockpve/`
- 2026-11-02 — revisione di sicurezza di dmesg/Sistema (Opus) e fix: guardia CSRF/DNS-rebinding su /api (anche WebSocket), tetti su risposte e righe, log senza contenuto, `level_applied`, bidi, test comportamentale di `dmesg.sh` con dmesg finto, test del handler (validazione, 429, slot) e del tetto del client — `cmd/server/guard.go`, `internal/handlers/dmesg*.go`, `internal/api/client.go`, `SECURITY.md`
- 2026-11-02 — endpoint `/netinfo` (indirizzi dal guest agent) perche' lo script `install-dmesg-helper.sh` non vedeva le VM senza IP configurato in Proxmox (es. una RHEL 8.10 in DHCP); lo script ora usa l'IP dell'agent, segnala le VM senza alcun IP e installa tutti e tre gli helper (riconosce Rocky e RHEL) — richiesta utente — `internal/handlers/dmesg.go` (+test), `cmd/server/main.go`, `devtools/mockpve/mockpve.py`
- 2026-11-02 — step 1 Ceph (Opus): direzione "osservatorio meteo" approvata in D15-D19 (sola lettura; voce di menu e riquadro dashboard solo dove Ceph c'e'; aggiornamento ogni 5 s con 10 min in memoria del browser; HEALTH_ERR = Attenzione, HEALTH_WARN = Quasi sereno, capacita' >= 85% ambra e >= 95% rosso). Schizzo di riferimento (non nel repo): `.impeccable/mocks/ceph/ceph-sketch.html` — titolo di condizione; tre strumenti (pluviometro = capacita' con tacche 85/90/95, anemometro = I/O + IOPS con curva 10 min, fronte = recupero con simbolo del fronte freddo e fine stimata); avvisi meteo dai health check (gravita' = parola + forma); stazioni OSD (riga per nodo, tessera per OSD riempita per spazio usato, giu' tratteggiata rossa, fuori punteggiata); pressione dei PG (barra per stato con parole); pool in tabella; servizi MON/MGR/MDS e flag (es. noout spiegato) — `DOMANDE.md`
- 2026-11-02 — step 2 Ceph (Sonnet): backend sola lettura `/api/ceph` e `/api/ceph/summary` (normalizzazione con tetti, ETA solo se gli oggetti si muovono davvero, `percent_used` dei pool e' 0-1 mentre quello degli OSD e' gia' in %, cluster senza Ceph = `available:false`), metodi client `GetCeph*`, test `ceph_test.go`, mock `CEPH_SCENARIO` (healthy|warn|error|recovery|down|none). Verificato in sola lettura su CALCOLO (31 OSD su 8 nodi, 3 pool, 11 MON, HEALTH_WARN per 4 check AUTH/BLUESTORE) e HOSTING (not_configured). Soglie 85/90/95 fisse: Proxmox non espone i ratio del cluster — `internal/handlers/ceph.go`, `internal/api/client.go`, `cmd/server/main.go`, `devtools/mockpve/`
- 2026-11-02 — step 3 Ceph (Sonnet): pagina Ceph "osservatorio meteo", voce di menu e palette solo dove Ceph esiste, riquadro in panoramica, stato Ceph nella frase di condizione (`Ceph.stateOf`), polling 5 s con curva I/O in memoria; mock con MGR `active`. Nota: CALCOLO e' in HEALTH_WARN permanente (check AUTH_INSECURE_*), quindi la panoramica mostrera' "Quasi sereno" finche' non vengono risolti o silenziati in Ceph (`ceph health mute`) — `cmd/server/web/templates/index.html`, `devtools/mockpve/mockpve.py`, `CLAUDE.md`
- 2026-11-02 — traduzione italiana dei controlli di salute Ceph: tabella `Ceph.CHECK_IT` (codice -> frase dal conteggio, ~70 codici) e `Ceph.DETAIL_IT` (righe di dettaglio ricorrenti) in `index.html`; i codici non in tabella restano col testo originale di Ceph, quello originale e' nel tooltip dove la frase e' tradotta; un codice nuovo si aggiunge nella tabella — `cmd/server/web/templates/index.html`, `CLAUDE.md`
- 2026-11-02 — step 4 Ceph, revisione (Opus) e fix: chiamate per-nodo prima sui nodi monitor e ripiego su altri nodi in linea (max 3) se un nodo non ha i pacchetti Ceph; test con client finto (ripiego, tutto parziale, not_configured/forbidden/errore di rete, liste mai null) e `-race`; pagina che riscrive solo le sezioni cambiate (niente perdita di selezione/tooltip, condizione annunciata solo quando cambia; tolto `role=status` dall'orario che si aggiornava ogni 5 s); salute ignota = "stato di Ceph sconosciuto"; barra del riquadro con le soglie del server; fine stimata con data se non e' oggi; annullata la riformattazione gofmt di parti di `client.go` estranee a Ceph. Documentati SECURITY.md (sezione Ceph) e DESIGN.md — `internal/handlers/ceph*.go`, `internal/api/client.go`, `cmd/server/web/templates/index.html`, `SECURITY.md`, `DESIGN.md`
- 2026-11-02 — badge GPU nella lista VM (PCI passthrough di schede video, anche via resource mapping): `gpus` da `GetAllVMs`, mock con GPU diretta, GPU da mapping e una NIC (non conta) — richiesta utente — `internal/handlers/gpu.go`, `gpu_test.go`, `handlers.go`, `internal/api/client.go`, `cmd/server/web/templates/index.html`, `devtools/mockpve/mockpve.py`
