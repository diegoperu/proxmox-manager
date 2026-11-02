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
