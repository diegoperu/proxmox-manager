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

## Reporting vulnerabilities
Open a GitHub issue marked **[Security]** or contact the maintainer directly.
