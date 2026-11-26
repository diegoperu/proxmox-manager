```text
 ██████╗ ██████╗  ██████╗ ██╗  ██╗███╗   ███╗ ██████╗ ██╗  ██╗
 ██╔══██╗██╔══██╗██╔═══██╗╚██╗██╔╝████╗ ████║██╔═══██╗╚██╗██╔╝
 ██████╔╝██████╔╝██║   ██║ ╚███╔╝ ██╔████╔██║██║   ██║ ╚███╔╝
 ██╔═══╝ ██╔══██╗██║   ██║ ██╔██╗ ██║╚██╔╝██║██║   ██║ ██╔██╗
 ██║     ██║  ██║╚██████╔╝██╔╝ ██╗██║ ╚═╝ ██║╚██████╔╝██╔╝ ██╗
 ╚═╝     ╚═╝  ╚═╝ ╚═════╝ ╚═╝  ╚═╝╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═╝

            ███╗   ███╗ █████╗ ███╗   ██╗ █████╗  ██████╗ ███████╗██████╗
            ████╗ ████║██╔══██╗████╗  ██║██╔══██╗██╔════╝ ██╔════╝██╔══██╗
            ██╔████╔██║███████║██╔██╗ ██║███████║██║  ███╗█████╗  ██████╔╝
            ██║╚██╔╝██║██╔══██║██║╚██╗██║██╔══██║██║   ██║██╔══╝  ██╔══██╗
            ██║ ╚═╝ ██║██║  ██║██║ ╚████║██║  ██║╚██████╔╝███████╗██║  ██║
            ╚═╝     ╚═╝╚═╝  ╚═╝╚═╝  ╚═══╝╚═╝  ╚═╝ ╚═════╝ ╚══════╝╚═╝  ╚═╝
```

Web management interface for Proxmox VE clusters — no CORS, no frameworks

![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat-square&logo=go&logoColor=white) ![SQLite](https://img.shields.io/badge/SQLite-3-003B57?style=flat-square&logo=sqlite&logoColor=white) ![Vanilla JS](https://img.shields.io/badge/JS-Vanilla-F7DF1E?style=flat-square&logo=javascript&logoColor=black) ![Chart.js](https://img.shields.io/badge/Chart.js-4.4-FF6384?style=flat-square&logo=chartdotjs&logoColor=white) ![License](https://img.shields.io/badge/License-CC%20BY--NC%204.0-lightgrey?style=flat-square) ![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS-lightgrey?style=flat-square)

---

## Overview

ProxmoxManager is a self-hosted web interface for managing one or more Proxmox VE clusters. It runs as a Go binary that acts as a local HTTP proxy: the browser talks to `localhost:8080`, and the proxy forwards requests to the Proxmox API over HTTPS — eliminating all CORS issues without any browser extensions or configuration. The application frontend is a single vanilla JS file embedded in the binary, together with the vendored third-party libraries it uses (Chart.js, SheetJS, xterm.js) and its fonts — no CDN, so it also works on an isolated network; no Node.js, no npm, no build pipeline. Multi-cluster support lets you switch between Proxmox environments from a single tab. Ships as a single self-contained binary (~12 MB).

---

## Features

- 🖥 **Dashboard** — a one-sentence cluster condition, KPIs with history, per-node 24h min–max bars (Proxmox RRD), recent activity, and the Top 10 VMs/containers by work done in the last 6/12/24 hours
- 🗂 **VM & LXC management** — start, stop, reboot, shutdown, suspend, resume, reset, snapshot, migrate, delete
- ⚡ **Batch operations** — multi-node VM/container selector, 7 actions, real-time log output
- 🚀 **Guided VM creation** — the server runs the whole job: recommends the node (RAM never overbooked, CPU only as a warning) and explains why a node is not eligible, clones a CloudInit template straight onto the chosen node, resizes the disk (waiting for the Proxmox task), optionally assigns a GPU, adds an extra disk (formatted and mounted in the guest) and a second user (sudo guaranteed, applied once cloud-init has finished), and picks the group with one click or a new name
- 🏷 **Groups from Proxmox tags** — `gruppo-<name>` tags, bulk labelling page with a proposal from the VM name, per-VM change, group in reports
- 🎮 **GPU page** — inventory of NVIDIA cards per node, assign/remove through PCI resource mappings, conflicts and problems, card model names and memory you can correct
- ⚖ **Cluster balance** — a plan of migrations that evens out assigned RAM (live or cold), "what if I switch off node X", one step at a time with recalculation; CPU compatibility rules for live migration
- 🗄 **Ceph page** — read-only "weather station" (capacity, I/O, recovery, OSDs per pool, health checks translated)
- 📊 **Reports** — technical reports (nodes, VMs and containers, GPU and GPU usage, storage and Ceph, reliability) and a **VM trend** report (1–12 calendar months per VM: average and peak use, abandoned / long-off / oversized VMs, a sizing proposal); CSV and Excel export with formula neutralisation
- 🏛 **Manager report** — an anonymous document for management (no VM, group or node names): one executive page plus annexes, Italian then English, A4 portrait or landscape, printed to PDF. Growth over time, how users size their VMs, GPUs by memory size, availability (also net of recorded external outages and maintenance), backups, economic value (internal price list vs. a low–high market range)
- 🔔 **Update check** — the manager compares the commit it was built from with `main` on GitHub (every 6 hours, or on demand) and says in Settings → General → Information whether it is up to date, how many commits behind it is and what they are, with instructions to update. It never downloads or runs anything. Can be switched off with `update_check: false`
- 🌐 **Network of new VMs per cluster** — Settings → Infrastructure: networks (CIDR + gateway), DNS and search domain; the New VM page picks the gateway from the network that contains the IP and warns when the IP is in none of them
- 💶 **Price lists and report parameters per cluster** — internal price list (tokens, base VM, blocks, GPU tiers by memory) with a live preview, market prices, storage classes, default report thresholds, outage log. A **"Check price lists"** button reads the public Azure and OVHcloud price lists and *proposes* values; nothing is saved without the administrator. All prices at zero = no economic section for that cluster
- 📈 **GPU usage** — optional sampling of `nvidia-smi` inside VMs with a GPU every 5 minutes (utilisation, memory, temperature, power; never processes): working / reserved (model loaded, no compute) / idle / VM off, on the GPU page, in the VM panel and in reports
- 🧾 **Ansible inventory** — export the running VMs of the current cluster as `inventory-<cluster>.yml` (one file per cluster, groups from tags)
- 🔄 **Automatic refresh** — read-only pages refresh every 60 s by default (off, 15 s … 5 min), paused while a dialog is open, while typing or with unsaved changes
- 🌐 **Multi-cluster** — manage multiple independent Proxmox clusters from a single interface, switch with a dropdown
- 👤 **Add VM users** — create OS users inside running VMs via QEMU Guest Agent, with optional SSH public key and sudoers setup
- 🖥 **Serial console** — xterm.js terminal for VMs with `serial0` configured via termproxy WebSocket bridge (requires dedicated credentials — see [Security — Console credentials](#security--console-credentials))
- 🎨 **Themes and density** — auto (follows the OS), Night, Light ("Alba"), Eva-01, Catppuccin Mocha, Spider-Man; comfortable or compact density
- ⌨ **Quick search** — `Ctrl`/`⌘` + `K` jumps to any VM, node, page or action; every VM has a shareable link (`#/vm/<node>/<vmid>/<qemu|lxc>`)
- 🧾 **Kernel log (dmesg)** — read on demand from a running Linux VM through the guest agent (last 100/300/1000 lines, errors-and-warnings filter, search, copy, download); needs the labelled `dmesg.sh` helper on SELinux-enforcing guests
- 🧩 **Operating system column** — name and version from the guest agent (e.g. Ubuntu 24.04, Rocky 8.10), with the Proxmox OS type as fallback
- ☑ **Selection bar** — tick VMs in the VM list and hand them to Batch with the action already chosen

---

## Architecture

```
Browser  ──────►  Go proxy (localhost:8080)  ──────►  Proxmox VE API
              (serves SPA + proxies all API calls)    (HTTPS, self-signed OK)
```

All API calls go through the Go proxy, which adds the `Authorization` header with the Proxmox API token server-side. The browser never contacts Proxmox directly, so there are no CORS preflight issues and no need to configure CORS headers on the Proxmox side.

---

## Requirements

|                | Minimum                        |
|----------------|-------------------------------|
| Go             | 1.21+ with CGO enabled        |
| C compiler     | gcc or clang (for go-sqlite3) |
| Proxmox VE     | 8.x or 9.x (in use on 9.2)    |
| OS             | Linux, macOS                  |

**Debian/Ubuntu:** `apt install golang gcc`  
**Fedora/RHEL:** `dnf install golang gcc`

---

## Quick Start

```bash
git clone https://github.com/diegoperu/proxmox-manager
cd proxmox-manager
chmod +x build.sh && ./build.sh
./proxmox-manager
# Open http://localhost:8080 → Settings → add your Proxmox cluster
```

On first run, `config.json` and `proxmox.db` are created in the working directory.

**From a release (no compiler needed):** every release carries a fully static `linux/amd64` binary (runs on any Linux x86-64, no glibc or Go needed), the `SHA256SUMS` file and a signed build-provenance attestation.
```bash
# download proxmox-manager-linux-amd64 and SHA256SUMS from the latest release, then:
sha256sum -c SHA256SUMS
# the maintainer signs SHA256SUMS offline: SHA256SUMS.sig is verified against release-signing-key.pub (in this repository)
go run ./tools/release-sign verify -pub release-signing-key.pub -in SHA256SUMS -sig SHA256SUMS.sig
gh attestation verify proxmox-manager-linux-amd64 --repo diegoperu/proxmox-manager   # optional: proves it was built by this repository's workflow
chmod +x proxmox-manager-linux-amd64 && ./proxmox-manager-linux-amd64
```
Releases are tagged `vYYYY.MM.DD[.N]` and can only be created from a commit that is on `main`.

**Manual build:**
```bash
CGO_ENABLED=1 go build -ldflags="-s -w" -o proxmox-manager ./cmd/server/
```
`./build.sh` additionally embeds the commit, its date and whether the tree was modified, which the update check needs to compare with `main` (a plain `go build` inside the git checkout is also recognised).

**Systemd service:**
```ini
[Unit]
Description=ProxmoxManager
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/proxmox-manager
ExecStart=/opt/proxmox-manager/proxmox-manager
Restart=on-failure
User=nobody

[Install]
WantedBy=multi-user.target
```

---

## Configuration

Settings are stored in `config.json` in the binary's working directory. The file is created automatically on first run and is excluded from git (see `.gitignore`).

**Multi-cluster format:**
```json
{
  "clusters": [
    {
      "label": "Production",
      "url": "https://10.0.0.1:8006",
      "api_token": "user@pam!tokenid=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
      "username": "",
      "password": "",
      "default": true
    },
    {
      "label": "Dev",
      "url": "https://YOUR-SECOND-PROXMOX-IP:8006",
      "api_token": "USER@REALM!TOKENID=YOUR-UUID-HERE",
      "username": "",
      "password": "",
      "default": false
    }
  ],
  "theme": "",
  "density": "",
  "refresh_seconds": 60,
  "db_path": "proxmox.db",
  "listen_addr": ":8080",
  "gpu_usage": false
}
```

Per-cluster report settings (`clusters[].report`: price lists, storage classes, default thresholds, outage log) and the GPU model corrections (`gpu_models`) are written by the Settings and GPU pages; there is no need to edit them by hand.

> `username` and `password` are optional. If left empty, all features remain available except the Serial console, whose button is automatically hidden in the UI. For secure setup of these credentials, see [Security — Console credentials](#security--console-credentials).

| Field | Default | Description |
|---|---|---|
| `clusters[].label` | — | Display name for the cluster selector |
| `clusters[].url` | — | Proxmox node URL (or cluster VIP) |
| `clusters[].api_token` | — | Proxmox API token (`USER@REALM!TOKENID=UUID`) |
| `clusters[].default` | — | `true` = loaded on startup |
| `theme` | `""` | `""` follow OS, `"dark"`, `"light"`, `"eva01"`, `"catppuccino"`, `"spiderman"` (unknown values are rejected) |
| `density` | `""` | `""` comfortable, `"compact"` (tighter tables and rows) |
| `refresh_seconds` | `60` | Automatic data refresh interval in seconds: `0` (off), `15`, `30`, `60`, `120` or `300`. Also adjustable from the top bar and Settings |
| `allowed_hosts` | `[]` | Optional list of host names accepted in the `Host` header (anti DNS-rebinding), e.g. `["localhost","pmgr.example.org"]`; empty = no restriction |
| `listen_addr` | `:8080` | HTTP listen address |
| `db_path` | `proxmox.db` | SQLite file for the stored history (see [Stored data](#stored-data)) |
| `gpu_usage` | `false` | `true` = sample `nvidia-smi` every 5 minutes inside running VMs that have a GPU (runs a fixed read-only script through the guest agent) |
| `update_check` | on | `false` = never contact GitHub to look for a newer version (see [Security](#security)) |
| `vm_usage` | on | `false` = do not keep the daily VM usage history (it only reads the Proxmox RRD) |

The server rewrites `config.json` atomically with permissions `0600` whenever something is saved from the UI.

Configuration can also be edited from the **Settings** page in the web UI (tabs *General* and *Reports and price lists*).

---

## Stored data

Live data always comes from Proxmox. The SQLite file (`db_path`) keeps only:

| Table | Content | Kept |
|---|---|---|
| `inv_*` | daily inventory snapshot per cluster: counts and assigned resources per node and group, GPU cards | 5 years |
| `vm_use_day` | daily usage per VM and instance (CPU/RAM average, p95 and peak, network, disk), **VM name**, VMID, group, assigned resources | 13 months |
| `gpu_use_day` | daily GPU usage per VM and instance (when `gpu_usage` is on) | 13 months |

No IP addresses, no guest content, no processes. VMIDs are reused by Proxmox, so rows carry an instance id (the SMBIOS UUID of the VM).

---

## Manager SSH access to the nodes

To copy scripts and cloud-init snippets to the nodes (onboarding, see `docs/ONBOARDING.md`) the manager logs in to each node as `pmgr-sync`, an account restricted by a forced command to listing and writing two folders. The manager key and the node host keys it trusts are in `ssh_dir` (default `ssh/` next to `config.json`, mode 0700; back it up with the configuration). In **Operazioni → Preparazione nodi**:

1. set **Impostazioni → Infrastruttura → Indirizzo del manager visto dai nodi** (the address the nodes see: a router's if there is one in between);
2. run, as root on every node, the two commands the page compiles (`pmgr-sync-setup.sh --role dest` with the manager key and that address);
3. for each node, an administrator reads the host key fingerprint, compares it with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` run on the node, and confirms it (TOTP).

The access then shows "accesso attivo" with the files present on the node. Needs `openssh-client` and `rsync` on the manager machine.

**Delivering snippets and scripts.** The section "Snippet e script sui nodi" compares every node with what the manager would deliver: the cloud-init snippets rendered for the cluster profile (**Impostazioni → Infrastruttura → Profilo degli snippet**: `standard`, or `hosting` for key-only SSH, firewall and hardening) and the template tooling, both taken from the files built into the release binary. An administrator chooses the nodes and confirms with the TOTP code; files are copied (never deleted) and each node is checked again.

## Creating the Proxmox service account and API tokens

The manager talks to every cluster with **one dedicated service account per cluster**, `pmgr@pve`, through API tokens; who did what is recorded by the manager itself (action log with the Proxmox task UPID). Do **not** use `root@pam` or the built-in `Administrator` role: it also grants `Permissions.Modify`, `User.Modify`, `Realm.Allocate`, `Group.Allocate`, `Sys.Modify` and `Sys.Incoming`, so a stolen token could create users, grant itself rights and change the cluster configuration. No other built-in role fits either (`PVEVMAdmin` lacks node and storage access, `PVEAdmin` includes `User.Modify`, only `Administrator` has `Sys.PowerMgmt`).

### 1. Custom role `PMGRManager` (24 privileges)

| Area | Privileges | Used for |
|---|---|---|
| Nodes and cluster | `Sys.Audit` `Sys.PowerMgmt` | node status, network, PCI devices, tasks, Ceph, HA; node reboot/shutdown |
| VMs | `VM.Audit` `VM.PowerMgmt` `VM.Snapshot` `VM.Migrate` `VM.Clone` `VM.Allocate` | lists, power actions, snapshots, migrations and balance, New VM, deletion |
| VM configuration | `VM.Config.Disk` `VM.Config.CPU` `VM.Config.Memory` `VM.Config.Network` `VM.Config.Cloudinit` `VM.Config.Options` `VM.Config.HWType` | disks, cores/RAM, cloud-init, group tags, GPU assignment |
| Guest agent | `VM.GuestAgent.Audit` `VM.GuestAgent.Unrestricted` | IP, OS and disk info; add user, disk setup, kernel log, GPU usage |
| Storage | `Datastore.Audit` `Datastore.AllocateSpace` | free space, snippets, new disks. **`Datastore.AllocateSpace` is also what lets the token list snippets and ISO files**: Proxmox hides non-disk volumes from a storage listing without it, and the New VM precheck then sees the cloud-init snippets only on some nodes (observed on the clusters, 2026-11-25) |
| Other | `Pool.Audit` `Mapping.Audit` `Mapping.Use` `SDN.Audit` `SDN.Use` | pools, PCI mappings of the GPUs, network bridges of new VMs |

Deliberately left out: everything that changes users, permissions, realms and the cluster configuration, `Sys.Console` (node shell), `VM.Console` (the serial console uses its own credentials, see [Security — Console credentials](#security--console-credentials)), `VM.Backup`, `VM.Snapshot.Rollback`, `VM.GuestAgent.File*`. Proxmox 8 has `VM.Monitor` instead of the `VM.GuestAgent.*` privileges.

### 2. Commands (as root on one node of each cluster)

```bash
pveum role add PMGRManager --privs "Sys.Audit,Sys.PowerMgmt,VM.Audit,VM.PowerMgmt,VM.Snapshot,VM.Migrate,VM.Clone,VM.Allocate,VM.Config.Disk,VM.Config.CPU,VM.Config.Memory,VM.Config.Network,VM.Config.Cloudinit,VM.Config.Options,VM.Config.HWType,VM.GuestAgent.Audit,VM.GuestAgent.Unrestricted,Datastore.Audit,Datastore.AllocateSpace,Pool.Audit,Mapping.Audit,Mapping.Use,SDN.Audit,SDN.Use"

# service account: no password, so it cannot log in to the Proxmox web UI
pveum user add pmgr@pve --comment "ProxmoxManager service account (no login)"
pveum acl modify / --users pmgr@pve --roles PMGRManager

# token of the production server
pveum user token add pmgr@pve manager --privsep 1 --comment "ProxmoxManager server"
pveum acl modify / --tokens 'pmgr@pve!manager' --roles PMGRManager

# separate token for the local copy (development and emergency access), revocable on its own
pveum user token add pmgr@pve local --privsep 1 --comment "ProxmoxManager local copy"
pveum acl modify / --tokens 'pmgr@pve!local' --roles PMGRManager
```

- `token add` prints the secret **once**: copy it immediately into the manager (Settings → General → cluster → Edit). Format: `pmgr@pve!manager=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`.
- With `--privsep 1` the effective rights of a token are the intersection of the user's and the token's ACLs: that is why both ACL lines are needed.
- Proxmox task logs show `pmgr@pve!manager` (or `!local`); the manager's action log names the person, linked by the task UPID.
- Check what a token can do: `pveum user token permissions pmgr@pve manager` (or `GET /access/permissions` with the token).

### 3. Rotation and revocation

A token secret that has been exposed (pasted in a chat, a ticket, a shared file) must be regenerated:

```bash
pveum user token remove pmgr@pve local
pveum user token add pmgr@pve local --privsep 1 --comment "ProxmoxManager local copy"
pveum acl modify / --tokens 'pmgr@pve!local' --roles PMGRManager
```

Then put the new secret in the manager that uses it. Removing a token stops that instance only; the other token keeps working.

### 4. Verified

On Proxmox VE 9.2 (test cluster, 2026-11-23) with the `pmgr@pve!local` token: all 34 read calls used by the manager, and the writes snapshot create/delete, tags, cores/RAM, guest agent exec, reboot, live migration both ways, clone from a template, cloud-init and network settings, disk resize and extra disk, VM deletion. Not exercised yet: node reboot/shutdown (`Sys.PowerMgmt`) and GPU assignment (`VM.Config.HWType`, `Mapping.Use`, no PCI mapping on the test cluster). A missing privilege shows up in the manager as an explained 403 on the feature that needs it.

---

## Deployment on a dedicated Debian 13 machine (Caddy)

Use a dedicated **Debian 13 amd64** machine on the internal network/VPN. Caddy
runs on that same machine and provides HTTPS using the university certificate.
The manager listens only on `127.0.0.1:8080`. Restrict inbound access to the
internal network/VPN; do not expose it to the Internet.

1. **Create the Proxmox service account first.** Follow
   [Creating the Proxmox service account and API tokens](#creating-the-proxmox-service-account-and-api-tokens)
   on every cluster. The dedicated machine uses `pmgr@pve!manager`; the workstation
   emergency copy uses its own `pmgr@pve!local` token. Never reuse either secret
   in the other instance.

2. **Install a signed release.** Copy this repository's `deploy/` directory to a
   trusted, root-owned location on the Debian machine. Keep `install.sh`,
   `release-verify.sh` and `systemd/proxmox-manager.service` together. Required OS
   tools: Bash, systemd, curl, CA certificates, OpenSSL 3 (ed25519), Python 3,
   coreutils, util-linux (`flock`) and passwd (`useradd`). Install Caddy separately
   using the Debian package. No Go compiler or private release-signing key belongs
   on this machine.

   Choose a **published, signed** release tag, replacing `vYYYY.MM.DD` below with
   that actual tag (an optional `.N` suffix is supported). The tag must include
   this deployment support; older binaries do not implement its health probe.

   ```bash
   sudo bash deploy/install.sh install --release vYYYY.MM.DD --host pmgr.example.edu
   ```

   The installer checks Debian 13/root/amd64, creates the locked system account
   `pmgr` with `/usr/sbin/nologin`, verifies the release signature **and** SHA-256
   checksum, installs the hardened unit and enables/starts it. The raw ed25519
   public key is pinned in the installer; the key published by the selected tag
   must match it. Downloads allow HTTPS only, verify certificates, and have size
   limits and timeouts. GitHub's API resolves the tag's commit without credentials.
   The installer never prints config contents or tokens. A release without its
   offline `SHA256SUMS.sig` is refused.

3. **Create and verify the first administrator.** The command line is always
   available, including when web login is broken:

   ```bash
   sudo -u pmgr -H sh -c 'cd ~/proxmox-manager && ./bin/proxmox-manager user add NAME --role admin'
   ```

   Replace `NAME` with the administrator's username. The command displays generated
   credentials once: enrol the TOTP key in an authenticator and store the password
   securely. For recovery use `user reset NAME` or `user enable NAME` from the same
   directory. Alternatively, after Caddy is ready, use the web setup wizard with
   the one-hour, one-use setup code from the journal:

   ```bash
   sudo journalctl -u proxmox-manager | grep CONFIGURAZIONE
   ```

   This command deliberately reveals the setup secret **locally**. Do not paste
   its output into tickets or chats. No default account/password exists. Creating
   an account from the command line permanently closes first-start web setup.

4. **Configure Caddy on the same host.** Start from
   [`deploy/caddy/Caddyfile.example`](deploy/caddy/Caddyfile.example). Replace
   `{$PMGR_HOST}` with the same DNS name supplied to `--host` (or set `PMGR_HOST`
   in the **Caddy systemd service environment**, not just the current shell).
   Replace the certificate paths with the university certificate chain and key.
   Keep the key `root:caddy 0640` under a `root:caddy 0750` directory outside the
   manager's home. Give Caddy a `0750` log directory at `/var/log/caddy`; its access
   log files use `0600`. Retain existing site blocks if Caddy already serves others.

   Validate as the Caddy user so certificate permissions are checked, then reload:

   ```bash
   sudo -u caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
   sudo systemctl reload caddy
   ```

   For a new Caddy service, enable/start it after successful validation instead.
   The example enforces a 1 MB request-body limit and HSTS. Caddy sets the
   forwarded client address, host and protocol; it ignores client-supplied
   forwarded values by default. Keep this single-proxy topology: the manager
   trusts only `127.0.0.1` and `::1`. Console WebSockets pass through the proxy
   without extra rewrite or upgrade directives. See
   [Caddy's reverse proxy documentation](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy).
   The example's `tls internal` alternative requires distributing Caddy's CA to
   clients before testing; use the university certificate for the real deployment.

   Open `https://pmgr.example.edu`, verify certificate trust, complete setup/login,
   and test a cluster read and the console. Verify admin recovery and a login on
   this isolated instance **before** directing the team to it.

5. **Upgrade, inspect or roll back.** Keep the trusted deployment scripts outside
   the service's writable directory:

   ```bash
   sudo bash deploy/install.sh status
   sudo bash deploy/install.sh upgrade --release vYYYY.MM.DD
   sudo bash deploy/install.sh rollback
   ```

   Upgrade repeats signature/checksum verification, saves the current binary as
   `bin/proxmox-manager.prev`, atomically replaces the executable and restarts.
   Within 30 seconds the loopback `/api/version` probe must report the selected
   tag's exact commit; a failed restart/probe restores the previous binary,
   restarts it and returns failure. Manual rollback swaps the two binaries and
   restarts. This is a **binary-only** rollback: keep a data backup before any
   future release that changes database/config formats. `status` shows service
   state, the running commit and config ownership/mode; it never prints config.
   Only a direct loopback `GET /api/version` without any `Forwarded` or
   `X-Forwarded-*` headers can skip login for this probe. Requests through Caddy
   still require a reader session; other API routes keep their existing roles.

Data lives under `/home/pmgr/proxmox-manager/`: `config.json`, `proxmox.db`
(including SQLite `-wal`/`-shm` sidecars while running), and `auth.key`, the key
encrypting account TOTP secrets. Back up config, database **and key together**
using the machine's existing backup system, with access limited to administrators.
Use a clean service stop or a SQLite-consistent backup, not a live copy of only
the main database file. The application directory is `pmgr:pmgr 0750`; config,
database and key are private (`0600`, including new files via `UMask=0077`). The
home's parent is root-owned. `bin/` is `root:pmgr 0755`, binaries are
`root:root 0755`, and systemd mounts `bin/` read-only for the service while
allowing atomic config rewrites in its parent. Other home directories are hidden.
Outbound networking remains available to Proxmox, GitHub and public price lists.
The unit uses systemd's filesystem, capability and syscall restrictions; see
[systemd.exec](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html).

**Migration from the workstation copy:** prepare the DNS/certificate, dedicated
cluster tokens and an isolated login test first. Stop the workstation instance
cleanly, then transfer its `config.json` and `proxmox.db` through a protected
channel into a private staging directory on Debian. Use the dedicated machine's
`!manager` tokens in the staged config, never the workstation's `!local` tokens.
Install into a **fresh data directory** with both import options:

```bash
sudo bash deploy/install.sh install --release vYYYY.MM.DD --host pmgr.example.edu \
  --import-config /root/pmgr-import/config.json --import-db /root/pmgr-import/proxmox.db
```

The installer preserves cluster configuration/history, copies imports with
`pmgr:pmgr 0600`, and sets `listen_addr`, `trusted_proxies` and `allowed_hosts`
for the local proxy. It normalizes `db_path` to `proxmox.db` and `auth_key_path`
to `auth.key`; keep these names and the loopback address for the installer's
upgrade/status checks. Imports require current JSON configuration with a
`clusters` array; convert any old flat `proxmox_url` configuration first.
If the imported database already contains accounts, transfer its **matching**
32-byte authentication key too, beside the staged config, with permissions
`0600`. Set the staged config's `auth_key_path` to that key's filename (for example
`auth.key`). The installer automatically imports that key, refuses symlinks or
keys outside the staging directory, and stores it as `auth.key`. A missing,
exposed or malformed key stops installation; a different 32-byte key cannot
decrypt existing TOTP secrets, so verify login before handover. A database from
the old version without accounts needs no key. A nonempty source WAL/journal is
refused: stop the source cleanly or create a consistent SQLite backup first.
Imports never overwrite existing configuration/data. Normal
installation creates config only if missing. Test HTTPS login, existing history,
cluster permissions and administrator recovery before the final handover. Keep
the previous binary and the workstation copy available for emergency access.
The workstation remains a separate local instance with its **own** config,
database, authentication key, accounts, port and `!local` tokens; its collections
remain enabled as agreed in D146. Production operations belong on the dedicated
machine. Protect or remove staging copies once the backup and login checks pass.

## Security

- **TLS to Proxmox**: paste the cluster CA (`cat /etc/pve/pve-root-ca.pem` on a node, public) in **Impostazioni → Infrastruttura → Verifica TLS del cluster**: from then on every connection must present a certificate signed by it. Without it the certificate is not verified (the Node preparation page warns). See [`SECURITY.md`](SECURITY.md).
- Restrict the listen address to loopback if running on a shared host: `"listen_addr": "127.0.0.1:8080"`.
- `config.json` contains API tokens in plaintext — the server writes it with `0600`; check the permissions of a file created by hand: `chmod 600 config.json`.
- The only outbound Internet traffic is the optional **"Check price lists"** button (fixed public Azure and OVHcloud URLs) and the **update check** (the public GitHub API of this repository; it sends only the public commit hash it was built from). Both use TLS with verification and send nothing about your organisation. Everything else talks only to Proxmox.
- For remote access, place a TLS-terminating reverse proxy (nginx, Caddy) in front, or use an SSH tunnel: `ssh -L 8080:localhost:8080 user@server`.
- **Console credentials**: if using the serial console feature, follow the dedicated user setup in [Security — Console credentials](#security--console-credentials) instead of storing `root@pam` credentials.

See [`SECURITY.md`](SECURITY.md) for full details.

---

## Security — Console credentials

The serial console feature requires a Proxmox username and password because
Proxmox VE does not accept API tokens for WebSocket authentication
(`vncwebsocket` endpoint). These credentials are stored in `config.json`
and used exclusively to obtain a short-lived `PVEAuthCookie` at the moment
a console session is opened.

### ⚠ Do not use root@pam

Storing `root@pam` credentials gives full administrative access to the entire
Proxmox cluster if `config.json` is ever compromised. The impact is
significantly larger than a scoped API token.

### Recommended: create a dedicated limited user

Create a dedicated Proxmox user with only the permissions required for
console access. This limits the blast radius of a credential leak to
read-only console access on specific VMs.

**Step 1 — Create the user in Proxmox UI:**

```
Datacenter → Permissions → Users → Add
  User name:  console-manager
  Realm:      Proxmox VE authentication server  (pve)
  Password:   <strong password>
  Enabled:    ✓
```

Or via CLI on a Proxmox node:

```bash
pveum user add console-manager@pve --password '<strong password>'
```

**Step 2 — Create a role with minimal permissions:**

```
Datacenter → Permissions → Roles → Create
  Name:        ConsoleOnly
  Privileges:  VM.Console
               VM.Audit      (needed to list VMs and read their status)
```

Or via CLI:

```bash
pveum role add ConsoleOnly --privs "VM.Console,VM.Audit"
```

**Step 3 — Assign the role to the user on the relevant scope:**

To grant access to all VMs on all nodes (datacenter level):

```
Datacenter → Permissions → Add → User Permission
  Path:   /
  User:   console-manager@pve
  Role:   ConsoleOnly
```

Or via CLI:

```bash
pveum acl modify / --user console-manager@pve --role ConsoleOnly
```

To restrict access to a specific node or VM only:

```bash
# Only VMs on a specific node:
pveum acl modify /nodes/pve1 --user console-manager@pve --role ConsoleOnly

# Only a specific VM (VMID 100):
pveum acl modify /vms/100 --user console-manager@pve --role ConsoleOnly
```

**Step 4 — Configure ProxmoxManager:**

```
Settings → Cluster → Edit
  Username:  console-manager@pve
  Password:  <the password you set>
```

### What this account can and cannot do

| Action | Allowed |
|--------|---------|
| Open serial console on VMs | ✓ |
| View VM status and config | ✓ |
| Start / stop / reboot VMs | ✗ |
| Modify VM configuration | ✗ |
| Access Proxmox node shell | ✗ |
| Create or delete VMs | ✗ |
| Access Proxmox web UI | ✓ (read-only console only) |

All other ProxmoxManager features (VM management, batch operations,
provisioning, reports) continue to use the API token, which can be
scoped independently.

### Additional hardening

- Set `"listen_addr": "127.0.0.1:8080"` in `config.json` if you only
  access ProxmoxManager from the same machine.
- Ensure `config.json` is readable only by the user running the binary:
  ```bash
  chmod 600 config.json
  ```
- Rotate the console password periodically:
  ```bash
  pveum passwd console-manager@pve
  ```

---

## License

Copyright © 2024 Diego Peruselli. Licensed under [CC BY-NC 4.0](LICENSE) — free for personal and internal use; commercial resale and SaaS are not permitted.
