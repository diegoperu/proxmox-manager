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

ProxmoxManager is a self-hosted web interface for managing one or more Proxmox VE clusters. It runs as a Go binary that acts as a local HTTP proxy: the browser talks to `localhost:8080`, and the proxy forwards requests to the Proxmox API over HTTPS — eliminating all CORS issues without any browser extensions or configuration. The entire frontend is a single vanilla JS + Chart.js file embedded in the binary; no Node.js, no npm, no build pipeline. Multi-cluster support lets you switch between Proxmox environments from a single tab. Ships as a single self-contained binary (~10 MB).

---

## Features

- 🖥 **Dashboard** — cluster KPIs, CPU/RAM history charts (Proxmox RRD), live VM table with disk usage
- 🗂 **VM & LXC management** — start, stop, reboot, shutdown, suspend, resume, reset, snapshot, migrate, delete
- ⚡ **Batch operations** — multi-node VM/container selector, 7 actions, real-time log output
- 🚀 **VM provisioning** — clone CloudInit templates, resize disk, configure IP/gateway/DNS/SSH keys via cloud-init
- 📊 **Reports** — per-VM CPU/RAM/network history with one-click Excel export (.xlsx)
- 🌐 **Multi-cluster** — manage multiple independent Proxmox clusters from a single interface, switch with a dropdown
- 👤 **Add VM users** — create OS users inside running VMs via QEMU Guest Agent, with optional SSH public key and sudoers setup
- 🖥 **Serial console** — xterm.js terminal for VMs with `serial0` configured via termproxy WebSocket bridge (requires dedicated credentials — see [Security — Console credentials](#security--console-credentials))
- 🎨 **Themes** — Dark, Light, Eva-01, Catppuccin Mocha

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
| Proxmox VE     | 7.x or 8.x                    |
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

**Manual build:**
```bash
CGO_ENABLED=1 go build -ldflags="-s -w" -o proxmox-manager ./cmd/server/
```

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
  "cache_seconds": 30,
  "db_path": "proxmox.db",
  "listen_addr": ":8080"
}
```

> `username` and `password` are optional. If left empty, all features remain available except the Serial console, whose button is automatically hidden in the UI. For secure setup of these credentials, see [Security — Console credentials](#security--console-credentials).

| Field | Default | Description |
|---|---|---|
| `clusters[].label` | — | Display name for the cluster selector |
| `clusters[].url` | — | Proxmox node URL (or cluster VIP) |
| `clusters[].api_token` | — | Proxmox API token (`USER@REALM!TOKENID=UUID`) |
| `clusters[].default` | — | `true` = loaded on startup |
| `theme` | `""` | `""` follow OS, `"dark"`, `"light"`, `"eva01"`, `"catppuccino"` |
| `cache_seconds` | `30` | SQLite API cache TTL |
| `listen_addr` | `:8080` | HTTP listen address |

Configuration can also be edited from the **Settings** page in the web UI.

---

## Creating a Proxmox API Token

1. Open Proxmox web UI → **Datacenter** → **Permissions** → **API Tokens** → **Add**
2. Select the user (e.g. `root@pam` or a dedicated service account)
3. Enter a Token ID (e.g. `proxmox-manager`)
4. **Privilege Separation**: leave unchecked to inherit the user's full permissions
5. Click **Add** and copy the token secret immediately — it is shown only once
6. The token format used in `config.json` is: `root@pam!proxmox-manager=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`

**Minimum privileges** (if using privilege separation):
- `VM.Audit`, `VM.PowerMgmt`, `VM.Snapshot`, `VM.Clone`, `VM.Config.CPU`, `VM.Config.Memory`, `VM.Config.Disk`, `VM.Config.Network`, `VM.Migrate`, `VM.Allocate`
- `Sys.Audit`, `Sys.PowerMgmt`
- `Datastore.Audit`

---

## Security

- TLS certificate verification is **intentionally disabled** for Proxmox connections — Proxmox VE ships with self-signed certificates. See [`SECURITY.md`](SECURITY.md).
- Restrict the listen address to loopback if running on a shared host: `"listen_addr": "127.0.0.1:8080"`.
- `config.json` contains API tokens in plaintext — restrict file permissions: `chmod 600 config.json`.
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
