# Security

## Scope
ProxmoxManager runs as a local proxy between the browser and Proxmox VE API.
It is designed for trusted local network / VPN use only.

## Known design decisions
- TLS certificate verification is disabled for Proxmox connections (`InsecureSkipVerify`).
  This is intentional to support the self-signed certificates used by default in
  Proxmox VE. Do not expose the proxy to untrusted networks.
- The application listens on `0.0.0.0` by default. Restrict with `listen_addr`
  in `config.json` (e.g. `"127.0.0.1:8080"`) if running on a shared host.
- `config.json` contains API tokens in plaintext. Ensure file permissions
  restrict access (`chmod 600 config.json`).

## Guest operations (add user, add disk)

Two features run commands **inside guest VMs** through the QEMU Guest Agent
(`guest-exec`): creating an OS user and preparing a new disk.

- The application has **no authentication of its own**. Anyone who can reach
  its HTTP port can use these features on every VM the configured API token can
  see. Keep it on a trusted network/VPN or bind it to loopback (`listen_addr`).
- Request data (username, SSH key, device, mount point) is validated with
  whitelists and reaches the guest **only as positional arguments** to fixed
  scripts (`internal/handlers/guestscripts/`), never inside script text. The
  scripts are embedded in the binary.
- On SELinux-enforcing guests (e.g. Rocky/RHEL) the agent's shell is confined
  and cannot do this work. To support them, the guest is prepared with two
  helpers in `/usr/local/libexec/pmgr/` labelled `virt_qemu_ga_unconfined_exec_t`
  (boolean `virt_qemu_ga_run_unconfined` on). They run in an **unconfined**
  domain, and `guest-exec` must be enabled in the agent (`BLACKLIST_RPC`).
  Consequences to accept knowingly:
  - for those VMs, access to the API is equivalent to the ability to run the
    helpers as unconfined root; the helpers must stay narrow, validated
    operations (never a generic "run this command" entry point);
  - the guest, not the host, is the trust boundary: an enabled `guest-exec`
    also lets anyone whose Proxmox role allows guest-agent execution on the VM
    run commands in it, with or without this application;
  - changing a helper means redeploying it in the templates; guests without
    helpers fall back to the same script run inline; on an enforcing guest
    that fails with an explicit error (in testing, before any command ran and
    without creating the user).
- The add-user feature refuses to touch an account that already exists, so a
  request cannot reset the password, keys or sudo rights of an existing user.
- The password-only branch sets a **fixed initial password that expires at
  first login** (a deliberate project decision, accepted risk for internal
  VMs). If the expiry cannot be forced, the account is locked instead.

## Kernel log (dmesg) and operating system information

A third guest feature reads the **kernel log** of a running Linux VM through the
same agent mechanism, only when the user asks (nothing is preloaded or cached),
and the VM's operating system through `guest-get-osinfo`.

- Read-only: the fixed script `guestscripts/dmesg.sh` only reads the ring buffer
  (a behavioural test checks which flags the guest's `dmesg` can receive; it can
  never clear it or change the console level). The only request data are two
  whitelisted values (100/300/1000 lines, level all/warn), passed as positional
  arguments.
- The kernel log can contain internal names, addresses and occasionally
  sensitive messages. It is shown to anyone who can reach the application, like
  the other guest operations. The server log records counts and the execution
  mode only, never the content; guest stderr is logged truncated.
- Hostile-guest limits: output capped (512 KB, 2000 characters per line, only the
  last N lines kept), control characters, ANSI sequences and bidirectional
  overrides removed, text rendered with `textContent` only, at most 4 reads at a
  time in the whole proxy and one per VM, a 60 s overall deadline, and the
  proxy refuses Proxmox responses over 32 MiB.
- On SELinux-enforcing guests this needs a third helper, `dmesg.sh`, in
  `/usr/local/libexec/pmgr/` with the same label as the others.

## Reports

`GET /api/reports/{model}` only reads (Proxmox RRD, tasks, GPU inventory, the inventory snapshot). Every parameter is whitelisted (dates `YYYY-MM-DD` within the 488 days Proxmox keeps, node names, type, group `[a-z0-9]{1,32}`, search text up to 64 characters without control characters, thresholds with bounds); a bad value is a 400 with a fixed message. Work against Proxmox is bounded: 8 parallel requests, at most 600 VMs per report, one report at a time per cluster (a second one gets 429). Results are cached for 60 s in a shared cache capped at 256 entries (least recently used dropped), so varying the filters cannot grow memory without limit.

The manager report (`service`) carries only aggregates and groups: no VM names, IPs, node-level detail or task identifiers (UPID, user); the technical reports are for internal use. In the browser every value goes through `esc()`. CSV exports neutralise cells that a spreadsheet would read as a formula (text starting with `=`, `+`, `-`, `@`, tab or CR gets a leading apostrophe); the Excel export writes strings as strings (SheetJS never turns them into formulas). The PDF is produced by the browser's print dialog from a page built with the same escaping; the only image is the vendored PNG logo (no SVG, so no script).

## Daily inventory snapshot

The server writes one small snapshot per cluster per day into the SQLite file (`inv_*` tables): counts and assignments per node and group (VMs, allocated cores/RAM/disk, GPU cards), never IPs, VM names, guest data or metrics; the group comes from the VM's `gruppo-<name>` tag (see below; before 2026-11-04 from the name prefix, recorded per day as `group_rule`), validated as `[a-z0-9]{1,32}`, otherwise `altro`. Rows older than 5 years are deleted. The database file is as sensitive as `config.json`'s neighbours: it lives next to the binary with the process's umask. Keys written to SQL are validated and all statements are parameterised. A failed read stores nothing and is retried; unreadable GPU data is stored as "unknown", not as zero.

## Group tags

`GET /api/groups` reads; `PUT /api/nodes/{node}/{qemu|lxc}/{vmid}/groups` writes the `tags` field of one VM/container config, and nothing else.

- Only `gruppo-<name>` tags are replaced; every other tag is re-sent as it was (text; separators normalised to `;`; Proxmox itself re-sorts the tags on save, verified 2026-11-04). A write never drops tags beyond the read limit.
- Input: whitelisted node, kind (`qemu|lxc`), VMID, at most 4 groups `[a-z0-9]{1,32}` (not `altro`), unknown JSON fields refused, body capped at 4 KB. Group names are the only request data that reach the config, so no other tag can be injected.
- Every write reads the config fresh, refuses templates (a clone copies tags, so a group on a template would spread) and locked VMs, and sends the config `digest`: a VM changed in between is refused, never overwritten.
- A group that does not exist yet on any (non-template) VM is written only if the client names it in `confirm_new` (stops typos turning into fake groups in the reports); the page asks the user first.
- Proxmox errors are mapped to short messages (403 = missing `VM.Config.Options`); bodies stay in the log. Each change is logged (node, VMID, old and new groups).
- The groups page writes one VM per request, in sequence, and stops at the first 403. Accepted: the app has no login (as for the other VM actions); VMs whose name contains "template"/"cloudinit" are treated as templates (same rule as everywhere in the app) and do not appear in the page, though they can still be tagged in Proxmox.

## GPU assignment (PCI passthrough)

The only hardware write the app performs. `GET /api/gpus` and `GET .../gpu/plan` only read; `POST .../gpu` and `DELETE .../gpu/{slot}` write the VM config.

- Writes go through PCI resource mappings only: the app writes `hostpciN=mapping=<id>[,pcie|rombar|x-vga=0|1]` and removes only lines that currently are `mapping=...` of an NVIDIA video card (on the VM's node, or, when the card or the entry is missing there, a mapping whose entries are all NVIDIA). Direct `hostpciN: 0000:xx:yy` lines are never written or removed; NIC/NVMe mappings are refused.
- Every write: whitelisted node, VMID, mapping id, slot (`hostpci0-15`) and options; a per-node lock (64 striped mutexes, bounded memory); a fresh read of the VM (must be stopped, unlocked, not a template) and of every VM config on the node (a single unreadable config aborts); the client must send back exactly the slot and options it was shown; the PUT carries the config `digest`, so Proxmox rejects it if the VM changed in between.
- Permissions are Proxmox's: the token needs `VM.Config.HWType` on the VM and `Mapping.Use` on the mapping; a 403 is reported as such.
- Residual risks, accepted: someone assigning the same card to another VM from the Proxmox UI between our check and our write (only the target VM's digest is checked; the next inventory shows the conflict); a VM started (by a person or HA) in the same instant (the change then waits for the next start). The app has no login: anyone who reaches it can do this, as for the other VM actions; the `/api` guard blocks other web pages.
- Each change is logged (node, VMID, slot, mapping, card address); no secrets.

## Ceph status (read only)

`GET /api/ceph` and `GET /api/ceph/summary` only read Proxmox (`/cluster/ceph/status`, `/cluster/ceph/flags`, `/nodes/{node}/ceph/{osd,pool,mon,mgr,mds}`); there is no Ceph write path. The token needs `Sys.Audit`; without it the page says so (`reason: forbidden`) instead of failing.

- Node names used in per-node paths come from `/nodes` (online only) and the monitor quorum, and must match `nodeNamePattern`; the service kind is a whitelist (`mon|mgr|mds`) in the client.
- Every answer is normalised with caps (2000 OSD, 200 pools, 100 checks, 20 detail lines per check, 300 characters per text, control and bidi characters removed) on top of the 32 MiB response cap.
- Server-side cache (4 s page, 10 s summary) with one Proxmox fetch at a time per cluster and view, so many open tabs polling every 5 s do not multiply the load on Proxmox. Failures are not cached, are logged with detail and answered with a generic 502.
- Pools to OSDs (2026-11-04): the decompiled CRUSH map (`/nodes/{node}/ceph/crush`, plain text) is read only for its `rule NAME {` blocks and their `step take BUCKET [class CLASS]` lines; text over 1 MiB is ignored, at most 256 rules and 8 takes per rule, rule/bucket/class names must match `[A-Za-z0-9._-]{1,64}` (anything else is skipped, never guessed). An unreadable map is reported as `partial: crush` and the pools stay `mapped:false`.
- A cluster without Ceph is recognised only by Proxmox's own 4xx/5xx "binary not installed / not initialized / not configured" answer; network errors stay errors.
- In the browser every Ceph text (check messages, detail lines, names) goes through `esc()`; the Italian translations replace text only by code or anchored regular expressions on lines already capped at 300 characters.

## Requests from other web pages (CSRF, DNS rebinding)

Because the application has no login, `/api` refuses requests that a browser
started on behalf of another web page:

- `Sec-Fetch-Site`, when present, must be `same-origin` or `none`;
- an `Origin` header, when present (any method, WebSocket handshakes included),
  must match the `Host` the request was sent to (or `X-Forwarded-Host` behind a
  reverse proxy, which must therefore preserve the host name);
- `POST`, `PUT`, `DELETE` must send `Content-Type: application/json` (scripts and
  `curl` need `-H 'Content-Type: application/json'`);
- optional **`allowed_hosts`** in `config.json` (list of host names, empty by
  default = no restriction) rejects any other `Host` header, which stops DNS
  rebinding. Set it to the names users type in the address bar, e.g.
  `["localhost","127.0.0.1","pmgr.example.org"]`.

## Reporting vulnerabilities
Open a GitHub issue marked **[Security]** or contact the maintainer directly.

## Balance plan (read only)

`GET /api/balance` only reads Proxmox (cluster resources, guest configs, the migration precheck `GET .../migrate`, `/cluster/ha/resources`, RRD, Ceph OSD tree); it never migrates or writes anything: the interface `balanceAPI` has no write method, and a test uses a fake that could not write. The plan is advice: the page will run each step with the existing migrate action, after a new plan and an explicit confirmation.

- Parameters are whitelisted (numbers with fixed ranges, booleans `0|1|true|false`, node names `[A-Za-z0-9.-]` and they must exist in the cluster), at most 32 nodes to empty and 64 per-node reserves; the planner validates the ranges again.
- A fact that cannot be read is never guessed: the guest is marked `unverified` and is not moved. A guest under HA, with a lock, a PCI card, USB, a non shared disk, or a node list from Proxmox that excludes the target is not moved.
- Work against Proxmox is bounded (8 workers, 600 guests at most, one calculation at a time per cluster with 429 otherwise, static facts cached for 5 minutes, the cache is capped at 2000 entries and never holds an unverified guest). Errors to the client are generic; details only in the server log.
- The response contains VM names (like the VM report), no IPs, UPIDs or users.

### Balance step (the only write of the balance feature)

`POST /api/balance/step` migrates one guest. The client does not choose what to migrate: it sends what it saw (node, kind, vmid, target) and the server recomputes the plan with fresh data and the same parameters, and executes only if the first step of that plan is exactly the requested one; otherwise `409 plan_changed` and nothing is written. So the planner's hard rules (target RAM limit, blocked guests: PCI/USB, non shared disk, lock, HA, `unverified`, Proxmox's allowed nodes, offline nodes) are enforced again at write time, not only in the page. One migration at a time per cluster (`409 busy` while the previous task, read from Proxmox, is still running), serialised with the plan calculation by the same per-cluster gate. Body capped at 4 KB, unknown fields rejected, node names/kind/vmid/target validated before any Proxmox call. Reserves are user parameters (0 is allowed to simulate), so a step is executed only if the host reserve and every per-node reserve are at least 8 GB (`400 reserve_too_low` before any Proxmox call): a simulation with a low reserve can never fill a node to its physical RAM. Verified on the real cluster (2026-11-04): non-first steps, a GPU guest, wrong source/target/kind, a container under a live plan, low reserves and guard violations were all refused with nothing migrated. The UPID returned to the page is validated by a strict regex and `GET /api/balance/task` only reads task status. Proxmox errors are mapped (403 = missing `VM.Migrate`), the raw message stays in the server log; every started migration logs cluster, guest, source, target, mode and size. Accepted risk: like the other write actions the app has no login (trusted network, `/api` guard against CSRF and DNS rebinding).

### Balance: CPU compatibility of live migrations (incident 2026-11-04)

A live migration of a QEMU VM with `cpu: host` (or `max`, `custom-*`) from an AMD EPYC to an Intel Xeon node transferred all the memory and then failed at resume (`Failed to set special registers: Invalid argument`): the VM was left stopped on the target (its RAM state lost). Proxmox neither prevents nor prechecks it. Since then the planner refuses, as a hard rule enforced again at write time, any live move of such a VM to a node that lacks any of the source's CPU flags (`cpuinfo.flags`, host-only flags such as virtualization extensions, SEV and `x2apic` left out); unknown flags mean no live destination. The rule was checked on 2026-11-05 against a real live migration of every ordered pair of the 8 CPU models of the cluster (56 migrations of a dedicated VM): it never allows a pair that failed, including 14 pairs where Proxmox reported success but the guest had hung. The VM can still move cold (`cold_vms`), and the mode confirmed by the page must equal the plan's. Named CPU models are not constrained.

### Balance cold migration (server-driven procedure)

`mode:"cold"` on a running QEMU step starts a procedure in the server: ordered shutdown, offline migration, start on the target. It is started only after the same plan recheck as a live step and holds the same per-cluster "one at a time" slot until it ends. The procedure id is 128 random bits (crypto/rand), bound to the cluster (another `?cluster=` gets 404), validated by regex. The only input it accepts while running is the answer to "force stop?" (`{"force": bool}`, unknown fields rejected, 256 bytes max), only in `need_force` and only once; without an answer it cancels after 5 minutes and leaves the VM running (restarting it on its node if the ordered shutdown completed late). A forced stop never happens without that explicit answer. Failures halfway never leave a VM silently stopped: a failed migration restarts the VM on its source (checked first), a failed start on the target is retried once and then reported as a persistent notice. Only the `message` field of Proxmox errors reaches the page (no URLs/tokens), clamped to 200 characters. The goroutine is protected by `recover`. Accepted risk: the procedure lives in memory, a server restart halfway stops it and may leave the VM stopped (each phase is logged).

### Provision advice (read only)

`GET /api/provision/advice` reads cluster resources, Ceph OSD counts, node CPU history and (with `gpu=1`) the GPU inventory, and writes or reserves nothing. Parameters are whitelisted (memory 128 MB - 4 TB, cores 1-1024, storage and template node by name pattern, group `[a-z0-9]{1,32}`); the ranking is computed in the pure `balance.Place`. Answers are cached 10 s per cluster and query (bounded cache) and serialised with the other calculations of the cluster (429). Card and VM names come from Proxmox and are truncated; the page must show them through `esc()`. The advice is never trusted by a later write: the creation step (next steps of ROADMAP 3b) rechecks node, RAM, storage and card itself.

### Provision job (server-driven VM creation)

`POST /api/provision/job` is a write with many inputs, all validated before any Proxmox call: names and nodes by pattern, numbers by range, IPv4 address/gateway/DNS parsed with `net` and rebuilt (no free text reaches `ipconfig0`), SSH keys through `normalizeSSHKey` (one per line, at most 8), cloud-init password printable ASCII only, body capped at 64 KB with unknown fields refused. Nothing is written until a fresh precheck passes: template and disk storage readable, advice recomputed with a non-cached GPU inventory (target node eligible: RAM within the assignable limit, storage reachable; the mapping's card on that node, free and not reserved), new group confirmed, VMID unused in the cluster. "Check -> clone -> RAM set" is serialised per cluster so two creations cannot overbook a node; the chosen card is reserved in memory for the whole procedure and the real check remains the GPU page's plan under its node lock with the config digest. The procedure never deletes anything: a failure leaves the VM stopped and names the phase. The additional user is passed to `adduser.sh` as a positional argument (same rules as the button), the cloud-init password and SSH keys are never logged, and only the `message` of Proxmox errors reaches the page. Only templates are cloned (template flag or name, the same rule as the page's list), never an ordinary VM with its data; every volume of the template (disks, EFI, TPM, cloud-init, ISO) must be reachable from the target node; the disk to grow cannot be a cdrom; a second creation that arrives while one is between its check and its RAM setting is refused (409 busy) instead of queueing. A VM left by a failed clone is reported as ours only if the clone started and the VM carries the requested name. The procedure id is 128 random bits bound to the cluster. Accepted risk: the procedure lives in memory (a server restart halfway stops it; each phase is logged).

### GPU model corrections (name and memory)

`PUT /api/gpu-models` writes `gpu_models` in `config.json`, the file that also holds the Proxmox API tokens, so it is kept narrow: body capped at 2 KB with unknown fields refused, the model key must be a card model of the requested cluster (or already corrected: a typo cannot create an entry nobody can see), name and key must be valid UTF-8 printable text without control or bidi characters (name at most 64 characters, key 160), memory 0-512, at most 128 entries; the write is rolled back in memory if the file cannot be written. The values are shown only through `esc()`/`textContent`. Accepted risk, existing before this change: `config.json` is rewritten in place (not atomically), so a crash during the write can truncate it; the app has no login like its other write actions (trusted network, `/api` guard).

### Extra disk at VM creation

`extra_disk` adds a new volume to the VM being created and formats it inside the guest. Inputs are the ones of the existing "Aggiungi disco" button and use the same checks (size 1-16384 GB, storage name pattern, `validateMountPoint` refusing system directories and traversal, bus taken from the main disk, never from the request). The storage must be active on the chosen node, accept VM disks and have the space; this is checked before the clone, with fresh data, in the same advice code the page uses. The attach is a config write with the digest read just before, so Proxmox refuses it if anyone changed the VM. Formatting happens only through `disk.sh` on a disk with no partitions and no filesystem, only when exactly one such disk exists in the guest (several or none: nothing is written, a warning says so), the device, the mount point and the permissions travel as positional arguments of the script, and the VM is never stopped or restarted to make the disk appear. A failed mount never fails the creation and never skips the extra user (and the reverse). The guest agent wait is 10 minutes twice.

