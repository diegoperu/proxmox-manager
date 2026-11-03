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
