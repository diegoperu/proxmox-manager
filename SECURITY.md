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

## GPU usage monitoring (nvidia-smi in the guests)

When `gpu_usage` is true in `config.json` (off by default), the server runs `guestscripts/gpuuse.sh` every 5 minutes in each running QEMU VM that has a GPU, through the guest agent (labelled helper if present, inline otherwise). The script takes no arguments, only reads (`nvidia-smi --query-gpu=...`, never `-pm`, `-r` or anything that changes the card), is ASCII only and stops `nvidia-smi` after 15 s. No process names are read.

- **Hostile output:** the VM owner controls what `nvidia-smi` prints. The parser keeps at most 8 KB and 8 lines, needs 7 fields, accepts only numbers within ranges and a PCI bus id pattern, cleans the name to 64 printable ASCII characters and drops anything else (counted, logged as a number). A guest can only falsify its own figures; they feed reports, never an automatic action.
- **Availability:** 4 guests at a time per cluster, 25 s per guest, one of our commands per VM at a time (lock shared with the dmesg reader), the helper probe cached 1 hour, a VM that fails 3 times in a row is left alone for 1 hour, a cycle still running makes the next one a no-op, a panic ends only that cycle. HTTP requests never run anything in a guest: `GET /api/gpu-usage` and the pages read memory only.
- **Stored data:** table `gpu_use_day`, one row per cluster, day and VMID with daily aggregates (samples, utilization, memory, minutes at work, temperature, power); no names, no processes, no IP; rows validated before writing, parameterised SQL, kept 396 days. The manager report only receives per-group aggregates through a type that cannot carry names, VMIDs or nodes.
- **Measured on CALCOLO (2026-11-19, VM 202 A100 and VM 170 L4):** `nvidia-smi` query 30-55 ms with persistence mode off; an LLM generation ran at 74.8 tokens/s without reads and 74.7 with a read every second (300 times the collector's rate); during a guest reboot the VM went no_data, then paused, and was read again after it came back.

## Daily VM usage history (ROADMAP 21)

Unless `vm_usage` is false in `config.json`, the server stores one row per cluster, day, VMID and instance with the day's usage computed from Proxmox's RRD (CPU and RAM average, p95 of the 30-minute averages and peak; network and disk byte totals; allocated resources). It only reads the RRD and the VM config (for the `smbios1` uuid): nothing runs in the guests. Stored: VM name, VMID, instance id, group, node; never IPs or guest data. VMIDs and IPs are reused after a VM is deleted, so history is keyed by the instance id and a new VM never inherits the history of a deleted one; the current name is shown only for the matching instance. Rows are validated before writing (patterns for every key, ranges for every number), statements are parameterised, a day is written in one transaction, rows are kept 396 days. The data describes the activity of research groups: the technical report stays "internal use"; the manager document receives aggregates only.

## Release binaries (GitHub Actions)

`.github/workflows/release.yml` runs only on a pushed tag `vYYYY.MM.DD[.N]` and refuses a tag whose commit is not an ancestor of `main`. It uses no secret: permissions are empty by default and the single job gets `contents: write`, `id-token: write` and `attestations: write`; the third-party actions and the build image are pinned by commit SHA / digest. The binary is built in a pinned Alpine (musl) container after `go vet` and `go test`, fully static, with the commit embedded; the job fails if the binary is dynamic. Outputs: the binary, `SHA256SUMS` and a build-provenance attestation (Sigstore, verifiable with `gh attestation verify --repo diegoperu/proxmox-manager`). Anyone who can push a tag on the repository can publish a release, which is why every release is also **signed offline**: `tools/release.sh` (run on the maintainer's workstation) verifies the checksum and the attestation, signs `SHA256SUMS` with an ed25519 key that never leaves that workstation (`tools/release-sign`, standard library only; the signed message carries a fixed domain prefix; the key file must be 0600 and not a link; keygen never overwrites) and uploads `SHA256SUMS.sig`. The public key is `release-signing-key.pub` in the repository (fingerprint `004254617f4e7fad`); a repository or workflow compromise cannot produce a valid signature. The private key is a plain 0600 file without a passphrase (the release tool runs unattended): protect and back up the workstation, or move the key to hardware later. Any future in-app update button must verify this signature, with the public key built into the binary.

## Accounts (voce 20, step 2: authentication core)

`internal/auth`: accounts in the SQLite file (own connection). Passwords: argon2id (64 MiB, 3 passes, 16-byte salt, PHC format; parameters read back within bounds so a tampered row cannot force huge allocations), policy 12-256 characters without control characters, generated by the command line (never typed as arguments). Second factor: TOTP RFC 6238 (HMAC-SHA1, 6 digits, 30 s, +-1 step), each step usable once (atomic update, also for the setup code), secrets encrypted at rest with AES-256-GCM bound to the username, key in a separate 0600 file (`auth_key_path`, refused if group/other readable or a link). Sessions: 256-bit random token in the `__Host-pmgr` cookie (`Secure; HttpOnly; SameSite=Strict; Path=/`), only its SHA-256 stored, 30 minutes idle and 12 hours absolute, role read fresh on every request, ended by logout, password change (other sessions), reset or disable; per-session CSRF token sent in `X-CSRF-Token`. Login answers one generic error for unknown user, wrong password, wrong code, locked or disabled account, and verifies a dummy hash for unknown users (same timing); 5 failures lock the account for 15 minutes; 20 failures per client address in 15 minutes give 429 (bounded memory). The client address comes from `X-Forwarded-For` only when the peer is in `trusted_proxies`, taking the rightmost untrusted entry. First start (D145): with no account a 128-bit one-time code is written only to the log/journal, valid 1 hour; the web setup requires it, verifies a first TOTP code before creating the administrator and closes for good (also when an account is created from the command line meanwhile). The last active administrator cannot be disabled or demoted. Not yet enforced on the other routes: that is step 3, and this version must not be deployed alone.

## Authorization (voce 20, step 3)

One table, `routeAccess` in `cmd/server/authz.go`, maps every "METHOD pattern" to the minimum role (D136: reader, operator, administrator) and to whether the TOTP code must be typed again (`X-TOTP-Code`: deletions, node power, cluster tokens, users). The `authorize` middleware runs on every request: it finds the route pattern with the router itself, refuses a route without a rule (fail closed; `TestEveryRouteHasAnAccessRule` fails if a route is added without one, or a rule outlives its route), answers 401 "setup" while no account exists, then checks session, role (read fresh from the account), the CSRF header on every write and the step-up code. Pages and `/static` are public, and so are login and first-start setup. Every write is logged (D139) with user, client address, route, target (route parameters and cluster, never the body) and status, plus the Proxmox UPID found in the response, which links the row to the Proxmox task (D144); login, logout, setup and password changes are logged by the auth handlers. Users API: list, create (random password and TOTP returned once), role/enable changes, reset; an administrator cannot disable or demote their own account from the page; the last active administrator is protected. **Found while defining the roles (existing weakness):** single and Batch power actions and node commands forwarded the action string and any body key to Proxmox unchecked; now actions are whitelists (VM: start, stop, shutdown, reboot, suspend, resume, reset; container: same without reset; node: reboot, shutdown), node and VMID are validated and any parameter in the body is refused. **Lock-out safety:** first start via the one-time code in the log; `proxmox-manager user add|reset|enable` on the server always works (it does not go through the web); the previous binary is kept for rollback; the local copy (D146) is an emergency access.

## Review findings (voce 20, step 7, 2026-11-23)

- **Authorization bypass with encoded slashes (fixed, affected release v2026.11.23 only, never deployed):** the middleware looked up the route on the decoded `Path` while chi routes on the encoded `RawPath`; `GET /api/nodes/a%2Fb/status` found no rule (one segment more once decoded) and was passed through as "the router will answer 404", but the router matched it with `node="a/b"` and ran the handler without session, role or CSRF checks. Now the lookup uses the router's path, an `/api` path without a rule is answered 404 by the middleware itself, and API paths with an encoded slash or a backslash are refused (400). Regression test `TestEncodedPathsCannotSkipAuthorization`.
- **Console password returned in clear (fixed, older than voce 20):** `GET /api/config` masked the API tokens but returned `clusters[].password` of the serial console; it is now removed from the response (the page only uses `has_console_credentials`).
- Checked without findings: login and setup paths (generic errors, limits, single-use TOTP steps), session cookie and rotation, fresh role on every request, step-up list, CSRF on every write, action log content (no body, no secrets) and its page (all request-controlled text escaped), user management protections, response headers, installer signature chain, systemd unit.

## Service packaging (voce 20, step 6)

`deploy/`: Debian 13, Caddy on the same machine terminates HTTPS and forwards to `127.0.0.1:8080` (`trusted_proxies` 127.0.0.1/::1, `allowed_hosts` = the public name). Layout in the home of the system user `pmgr` (no password, nologin): `bin/` and the binary are owned by root and read-only for the service, so a compromised process cannot replace its own code; `config.json`, `auth.key` and `proxmox.db` are 0600 `pmgr`. systemd unit: dedicated user, no capabilities (empty bounding set), `NoNewPrivileges`, `ProtectSystem=strict`, home hidden except the service directory, `bin/` read-only, private /tmp and devices, kernel and cgroup protection, `MemoryDenyWriteExecute`, seccomp `@system-service` minus `@privileged`, address families limited; `systemd-analyze security` exposure 1.6 (OK). `@resources` is not excluded: the Go runtime raises its file limit at start and the first version of the unit was killed with SIGSYS. `deploy/install.sh install|upgrade|rollback|status` downloads a release only over HTTPS, verifies `SHA256SUMS` and the ed25519 signature with OpenSSL against a public key embedded in the script and equal to `release-signing-key.pub` of the tag, keeps the previous binary, and both install and upgrade wait until the service answers with the commit of the verified release (upgrade rolls back automatically otherwise). Response headers on every page: CSP (`default-src 'self'`; scripts and styles also `'unsafe-inline'`, needed by the single-page app; no external origin, `object-src 'none'`, `frame-ancestors 'none'`, `base-uri 'none'`), `X-Frame-Options DENY`, `nosniff`, `Referrer-Policy no-referrer`, HSTS only for HTTPS requests (direct TLS or `X-Forwarded-Proto: https` from a trusted proxy). Startup warnings when the service listens outside loopback without trusted proxies or without `allowed_hosts`. One exception to the fail-closed table: `GET /api/version` answers without a session only to a loopback peer that sent no `Forwarded`/`X-Forwarded-*` header (Caddy always adds them, so requests from the network never qualify); it exposes the build commit only, used by the upgrade check.

## Update check (second outbound Internet call)

The server asks the public GitHub API of this repository how far the running build is behind `main`: `GET https://api.github.com/repos/diegoperu/proxmox-manager/compare/<commit>...main?per_page=1` (state and count) and, only when behind, `.../commits?sha=main&per_page=N` (titles, at most 20). The repository and host are constants in the code (neither configuration nor requests can change them); the commit comes from the binary and must be 40 lowercase hex characters. Dedicated external client: TLS verified, redirects only to the same host, timeouts of 15 s, bodies read through a limit (8 MB for the compare, which also carries a file list; 1 MB for the commit list), generic User-Agent, proxy from the environment. Nothing about the organisation is sent (only the public commit hash and the usual request metadata, including the server's IP address, which GitHub sees). The response is untrusted: strict parsing, titles reduced to the first line, 120 printable characters, rendered only as text, never as links; upstream errors never reach the page or the log (fixed messages). One check at a time, at most one every 5 minutes (429), background round every 6 hours, `update_check: false` turns everything off. `GET /api/update-status` and `GET /api/version` only read memory and never touch the network. The check never downloads or executes anything; the future "update" button (ROADMAP 24, part 3) is a separate decision that needs login and signed releases.

## Network settings (new VMs)

`GET/PUT /api/clusters/{idx}/network-settings` store per-cluster networks, DNS and search domain in `config.json` (atomic write, 0600): body capped at 16 KB, unknown fields refused, IPv4 networks written as network addresses (prefix 8–30, gateway inside the network, never network or broadcast, no overlaps, at most 16), at most 3 DNS, RFC 1123 search domain; error messages name the field and row and never echo the submitted text. The values only prefill the New VM form and are validated again by the provisioning job, which also validates the optional `search_domain` before any write. The cluster edit endpoint cannot overwrite them.

## Report settings (price lists, outage log)

`GET/PUT /api/clusters/{idx}/report-settings` read and replace only the `report` object of one cluster in `config.json` (the file that also holds the API tokens): body capped at 64 KB, unknown fields refused, every number within explicit bounds, text fields printable and length-limited, dates validated; the cluster edit endpoint cannot write `report`, and the token is never returned. `config.json` is written atomically (temporary file + rename) with permissions 0600. The values are shown in the page only through `esc()`. The internal price preview in the page is display only: the report computes on the server with the saved values.

## External price check (the only outbound Internet call)

`POST /api/clusters/{idx}/report-settings/price-check` reads public price lists to PROPOSE market prices; it never saves anything (the administrator copies the values into the form and saves with the normal, validated `PUT report-settings`). Sources and URLs are fixed in the code: Azure Retail Prices API (filters built only from a whitelist of SKUs and regions) and the OVHcloud public cloud catalog; AWS, Scaleway and Aruba are manual (no public list without credentials). The HTTP client is dedicated: TLS verified with the system CAs (never the Proxmox client's skip-verify), redirects only to the same host and scheme, at most 3 Azure pages on the same host, bodies capped at 16 MB, 20 s per source, generic User-Agent, proxy from the environment, nothing about the organisation is sent. Responses are untrusted: strict parsing, expected currency (EUR), prices finite and within the settings bounds, missing items reported as errors (never zero); logs carry only result, duration and size. One check at a time and at most one every 5 minutes (429). Residual risks: the manager must be able to reach the Internet; a provider can change format or SKUs (the check fails explicitly); a human confirmation remains the last barrier against wrong values.

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

`extra_disk` adds a new volume to the VM being created and formats it inside the guest. Inputs are the ones of the existing "Aggiungi disco" button and use the same checks (size 1-16384 GB, storage name pattern, `validateMountPoint` refusing system directories and traversal, bus taken from the main disk, never from the request). The storage must be active on the chosen node, accept VM disks and have the space; this is checked before the clone, with fresh data, in the same advice code the page uses. The attach is a config write with the digest read just before, so Proxmox refuses it if anyone changed the VM. Formatting happens only through `disk.sh` on a disk with no partitions and no filesystem, only when exactly one such disk exists in the guest (several or none: nothing is written, a warning says so), the device, the mount point and the permissions travel as positional arguments of the script, and the VM is never stopped or restarted to make the disk appear. A failed mount never fails the creation and never skips the extra user (and the reverse). The guest agent wait is 10 minutes twice. Edge case accepted: if a template carried a data disk already formatted ext4 and not mounted, it could be the only candidate and would be mounted on the chosen directory, but not reformatted (the script skips partitioning and mkfs when they are already there).

### Add disk (VM panel): restart only with permission

`POST /nodes/{n}/qemu/{id}/setup-disk` formats the single blank disk found in the guest. The slot sent by the page is checked against the VM configuration (it must exist and not be a CD-ROM); several blank disks mean no write. A forced stop + start of the VM, which used to happen on its own when the disk did not show up, now needs `allow_restart` in the request: without it the server answers 409 `restart_needed` before touching the VM and the page asks the user in a separate danger confirmation.

### Start check (RAM)

Starting a stopped VM or container (single action, container action, Batch `start`) first checks, under a per-node lock, that the RAM of the running guests of the node plus the guest's own fits the assignable RAM; otherwise 409 `not_enough_ram` and nothing is started. The check reads cluster resources only and uses no request text in the answer except Proxmox names clamped to 64 characters. It fails open (the start goes on, logged) when the cluster cannot be read: it is a safeguard against overbooking, not an access control.

The advice for a new VM counts the RAM of the guests that use it now (running, started a moment ago, or being created and promised to the node), not stopped ones; creations in progress are tracked in memory per cluster and released on every exit of the procedure, so two creations in a row cannot both be given the same room.

### Template prechecks (files and space)

The advice reads the template config itself (`template_vmid`) and refuses to treat anything else than a template as one. Values taken from that config are checked before they are used: file references only match `storage:snippets/<name>` or `storage:iso/<name>` with a storage name pattern and a name without control characters, `/` or `,`; storage and node names are validated before the listing call (`/nodes/{n}/storage/{s}/content?content=snippets|iso`, a fixed set of content values), and the result is cached 60 s per cluster, node, storage and kind. Sizes are parsed with a bound. A listing that cannot be read, or a storage that does not enable the content type, blocks nothing and is reported in `meta.partial` (`files`, `files_unverified`): the check is a safeguard, never a condition for creating.

### Waiting for the resize task and for cloud-init (VM creation)

On Proxmox 9 a disk resize answers with a task and rewrites the VM configuration when it ends; the job now waits for that task before reading the configuration (and its `digest`) for the extra disk, so the write is never based on a stale digest. After the guest agent answers, the job waits until cloud-init has finished in the current boot (read-only probe using shell builtins, same result twice on the same boot id, 15 minutes at most) before it adds the extra user or touches the disk: cloud-init restarts the agent while it runs, which used to kill `adduser` half way. On timeout nothing is run in the guest and a warning is returned. `adduser.sh` guarantees sudo (checks `sudo` and `visudo` first, writes and validates `/etc/sudoers.d/<user>`, verifies with `sudo -l -U`) and rolls the account back on any failure or on SIGTERM/SIGHUP/SIGINT.

### Node access for script and snippet sync (`tools/pmgr-sync`)

`tools/pmgr-sync/pmgr-sync-setup.sh` creates a non-root `pmgr-sync` user on Proxmox nodes. On destinations its key is bound by `authorized_keys` to a forced command (`pmgr-sync-gate`) that accepts only `pmgr-list` and `rsync --server` writes into `snippets/` and `pmgr-scripts/` (via `rrsync -wo -no-del`: no reads, no deletes, no other paths); no shell. Snippets are executed as root in new VMs (cloud-init vendor data), so whoever can write them controls the future VMs: the source node is the trust anchor, copies are verified by `MANIFEST.sha256`, and nothing is deleted automatically. The manager integration (forced command `status|diff|run`, `os/exec` of `ssh` with a fixed argument list) is not written yet; see ROADMAP 13.

### Manager SSH access to the nodes (onboarding step 3)

The manager reaches a node only as `pmgr-sync`, the account that `pmgr-sync-gate` restricts on the node to `pmgr-list` and rsync writes into two folders (above). Its own ed25519 key is generated with `ssh-keygen` in `ssh_dir` (default `ssh/` next to `config.json`): directory `0700`, private key `0600`; a symlinked directory or a private key readable by others is refused. Node host keys are **never trusted on first use**: an administrator reads the fingerprint the node presents (`ssh-keyscan`), compares it with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` printed on the node and confirms it (TOTP step-up, action log); the server re-reads the key and stores it only if it still has the confirmed fingerprint (D152). After that `StrictHostKeyChecking=yes` against that file only: a changed key stops the access and the page says so, it is never re-pinned automatically. Node addresses come from Proxmox (`/cluster/status`), never from the request, and must be literal IP addresses. `ssh`, `ssh-keyscan` and `ssh-keygen` are the only programs the package may start, always with an argument list (no shell), an empty stdin, a minimal environment (no agent, `HOME` = `ssh_dir`), `-F /dev/null` (no user ssh_config), `BatchMode`, no forwarding, no local command, no control socket, a deadline and a cap on the output kept. The node's answer is not trusted: `pmgr-list` lines are parsed with strict patterns (hash, size, date, path under `snippets/` or `pmgr-scripts/` without `..`), malformed lines are only counted, the raw ssh text never reaches the page. The root commands that authorise the manager key on a node are displayed, never executed: the key line has a fixed format and the `from=` addresses (`manager_from`, D158) are validated IPv4 addresses. Running them replaces the other source keys of `pmgr-sync` on that node (D159).

### Hardening of existing VMs (operational tooling, not part of the binary)

The scripts used to bring existing VMs to the current baseline (fail2ban, automatic security updates, SSH, firewall) live outside the repository and are documented in `CLAUDE.md` and `ROADMAP.md`. Operating rules: always the `tecnico` account, snapshot before every write, one VM at a time with a verification afterwards, a safety timer that disables the firewall if the checks fail, and automatic security updates that never reboot and never touch Docker packages (or kernel/NVIDIA/Slurm on GPU VMs). Known residual exposure: ports published by Docker bypass `ufw`.

