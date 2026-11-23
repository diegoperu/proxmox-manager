#!/usr/bin/env bash
# Debian 13 installer. Run from a trusted, root-owned copy of this repository's deploy/ directory.
set -euo pipefail
umask 077

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source "$SCRIPT_DIR/release-verify.sh"
readonly APP_DIR='/home/pmgr/proxmox-manager'
readonly BIN_DIR="$APP_DIR/bin"
readonly SERVICE='proxmox-manager'
readonly REPO='diegoperu/proxmox-manager'
readonly ASSET='proxmox-manager-linux-amd64'
# Raw ed25519 public key, base64. A tag cannot rotate this trust anchor.
readonly RELEASE_KEY='pW13Ux563gjoeLL8JZUYokxjhUrdp1iVTJw7QPxBpIY='

die() { printf 'install: %s\n' "$*" >&2; exit 1; }
usage() {
    printf '%s\n' \
        'Usage: deploy/install.sh install --release vYYYY.MM.DD[.N] --host NAME [--import-config FILE --import-db FILE]' \
        '       deploy/install.sh upgrade --release vYYYY.MM.DD[.N]' \
        '       deploy/install.sh rollback' \
        '       deploy/install.sh status'
}

valid_host() {
    # ASCII DNS names only: no scheme, port, whitespace or header injection.
    python3 -c '
import re, sys
name = sys.argv[1]
ok = 1 <= len(name) <= 253 and all(re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?", x) for x in name.split("."))
sys.exit(0 if ok else 1)
' "$1"
}

regular_file() { [[ -f "$1" && ! -L "$1" ]] || die 'expected a regular file, not a symlink'; }
absent() { [[ ! -e "$1" && ! -L "$1" ]] || die 'destination already exists; refusing to overwrite data'; }

check_platform() {
    [[ $(id -u) == 0 ]] || die 'run as root on Debian 13'
    # Do not source OS metadata as shell code.
    [[ -f /etc/os-release ]] || die 'missing /etc/os-release'
    python3 -c '
import sys
from pathlib import Path
fields = dict(line.split("=", 1) for line in Path("/etc/os-release").read_text().splitlines() if "=" in line and not line.startswith("#"))
sys.exit(0 if fields.get("ID", "").strip("\"") == "debian" and fields.get("VERSION_ID", "").strip("\"") == "13" else 1)
' || die 'only Debian 13 is supported'
    [[ $(uname -m) == x86_64 ]] || die 'the release binary requires amd64 (x86_64)'
}

check_account() {
    python3 -c '
import grp, pwd, sys
try:
    p = pwd.getpwnam("pmgr")
    g = grp.getgrnam("pmgr")
except KeyError:
    sys.exit(1)
sys.exit(0 if 0 < p.pw_uid < 1000 and p.pw_gid == g.gr_gid and p.pw_dir == "/home/pmgr" and p.pw_shell == "/usr/sbin/nologin" else 1)
' || die 'existing pmgr account is not the expected dedicated system user'
    local password_state
    password_state=$(passwd -S pmgr) || die 'cannot check pmgr password lock'
    [[ "$password_state" == 'pmgr L '* ]] || die 'pmgr must have a locked password'
}

check_directory() {
    [[ -d "$1" && ! -L "$1" && $(realpath -e -- "$1") == "$1" ]] \
        || die 'managed directories must exist at their literal paths, without symlinks'
}

check_layout() {
    check_account
    check_directory /home
    check_directory /home/pmgr
    check_directory "$APP_DIR"
    check_directory "$BIN_DIR"
    [[ $(stat -c '%u:%g:%a' -- "$BIN_DIR") == "0:$(id -g pmgr):755" ]] \
        || die 'bin must be root:pmgr 0755'
    regular_file "$BIN_DIR/proxmox-manager"
    [[ $(stat -c '%u:%g:%a' -- "$BIN_DIR/proxmox-manager") == '0:0:755' ]] \
        || die 'binary must be root:root 0755'
}

fetch_https() {
    # Disable user curlrc; redirects stay HTTPS and all requests have finite deadlines and size limits.
    curl --disable --fail --silent --show-error --location \
        --proto '=https' --proto-redir '=https' --tlsv1.2 --max-redirs 5 \
        --connect-timeout 10 --max-time 120 --max-filesize "$3" --output "$2" "$1"
}

tag_commit() {
    # Resolve the tag explicitly, including annotated tags. No git checkout or GitHub token is needed.
    local url="https://api.github.com/repos/$REPO/git/ref/tags/$release" object kind sha depth=0
    while [[ "$depth" -lt 5 ]]; do
        fetch_https "$url" "$work/tag.json" 1048576 || die 'cannot resolve release tag commit'
        object=$(python3 -c '
import json, re, sys
try:
    data = json.load(open(sys.argv[1]))
    obj = data["object"]
    kind, sha = obj["type"], obj["sha"]
    if kind not in ("commit", "tag") or not re.fullmatch(r"[a-f0-9]{40}", sha):
        raise ValueError()
    print(kind, sha)
except (OSError, ValueError, KeyError, TypeError):
    sys.exit(1)
' "$work/tag.json") || die 'invalid release tag metadata'
        read -r kind sha <<< "$object"
        if [[ "$kind" == commit ]]; then
            expected_commit="$sha"
            return
        fi
        url="https://api.github.com/repos/$REPO/git/tags/$sha"
        depth=$((depth + 1))
    done
    die 'release tag nesting exceeds the limit'
}

download_release() {
    local asset limit base="https://github.com/$REPO/releases/download/$release"
    mkdir -- "$work/release"
    for asset in "$ASSET" SHA256SUMS SHA256SUMS.sig; do
        case "$asset" in "$ASSET") limit=134217728 ;; SHA256SUMS) limit=1048576 ;; *) limit=4096 ;; esac
        fetch_https "$base/$asset" "$work/release/$asset" "$limit" || die 'release asset download failed'
    done
    fetch_https "https://raw.githubusercontent.com/$REPO/$release/release-signing-key.pub" \
        "$work/release/release-signing-key.pub" 4096 || die 'tag public key download failed'
    verify_release "$work/release" "$RELEASE_KEY" || die 'release signature, pinned public key or checksum verification failed'
    tag_commit
    printf 'Verified release %s (%s).\n' "$release" "$expected_commit"
}

config_host() {
    regular_file "$APP_DIR/config.json"
    [[ $(wc -c < "$APP_DIR/config.json") -le 1048576 ]] || die 'config exceeds size limit'
    local name
    name=$(python3 -c '
import json, sys
try:
    c = json.load(open(sys.argv[1]))
    hosts = c["allowed_hosts"]
    if c["listen_addr"] != "127.0.0.1:8080" or not isinstance(hosts, list) or not hosts or not isinstance(hosts[0], str):
        raise ValueError()
    if c.get("db_path", "proxmox.db") != "proxmox.db" or c.get("auth_key_path", "auth.key") != "auth.key":
        raise ValueError()
    print(hosts[0])
except (OSError, ValueError, KeyError, TypeError, RecursionError):
    sys.exit(1)
' "$APP_DIR/config.json") || die 'config must use loopback, an allowed host, proxmox.db and auth.key'
    valid_host "$name" || die 'config allowed_hosts must contain a valid DNS name'
    printf '%s' "$name"
}

read_version() {
    # A local health probe supplies the configured Host for the DNS rebinding guard.
    curl --disable --noproxy '*' --fail --silent --connect-timeout 1 --max-time 2 \
        --max-filesize 4096 --header "Host: $host" http://127.0.0.1:8080/api/version
}

wait_version() {
    local deadline=$((SECONDS + 30)) remaining response
    while [[ "$SECONDS" -lt "$deadline" ]]; do
        remaining=$((deadline - SECONDS))
        # Bound even the last attempt by the same overall 30-second window.
        if response=$(curl --disable --noproxy '*' --fail --silent --connect-timeout 1 \
            --max-time "$remaining" --max-filesize 4096 --header "Host: $host" http://127.0.0.1:8080/api/version); then
            if printf '%s' "$response" | python3 -c '
import json, sys
try:
    data = json.load(sys.stdin)
    sys.exit(0 if data.get("commit") == sys.argv[1] and data.get("dirty") is False and data.get("known") is True else 1)
except (ValueError, AttributeError, TypeError, RecursionError):
    sys.exit(1)
' "$expected_commit"; then
                return 0
            fi
        fi
        [[ "$SECONDS" -ge "$deadline" ]] || sleep 1
    done
    return 1
}

stage_binary() {
    next_binary=$(mktemp "$BIN_DIR/.proxmox-manager.next.XXXXXX")
    install -o root -g root -m 0755 -- "$work/release/$ASSET" "$next_binary"
}

save_previous() {
    [[ ! -L "$BIN_DIR/proxmox-manager.prev" ]] || die 'previous binary must not be a symlink'
    previous_stage=$(mktemp "$BIN_DIR/.proxmox-manager.prev.XXXXXX")
    install -o root -g root -m 0755 -- "$BIN_DIR/proxmox-manager" "$previous_stage"
    mv -fT -- "$previous_stage" "$BIN_DIR/proxmox-manager.prev"
}

restore_previous() {
    regular_file "$BIN_DIR/proxmox-manager.prev"
    [[ $(stat -c '%u:%g:%a' -- "$BIN_DIR/proxmox-manager.prev") == '0:0:755' ]] \
        || die 'previous binary must be root:root 0755'
    # Copy first so both named binaries remain available until each atomic rename.
    rollback_stage=$(mktemp "$BIN_DIR/.proxmox-manager.rollback.XXXXXX")
    install -o root -g root -m 0755 -- "$BIN_DIR/proxmox-manager" "$rollback_stage"
    mv -fT -- "$BIN_DIR/proxmox-manager.prev" "$BIN_DIR/proxmox-manager"
    mv -fT -- "$rollback_stage" "$BIN_DIR/proxmox-manager.prev"
}

cleanup() {
    local path
    for path in "${next_binary:-}" "${previous_stage:-}" "${rollback_stage:-}"; do
        if [[ -n "$path" ]]; then rm -f -- "$path"; fi
    done
    rm -rf -- "$work"
}

prepare_config() {
    # Stage JSON privately before touching the installation; never print contents or parser errors.
    python3 -c '
import json, os, sys
source, target, host = sys.argv[1:]
try:
    if source:
        if os.path.islink(source) or not os.path.isfile(source) or os.path.getsize(source) > 1048576:
            raise ValueError()
        with open(source) as f:
            c = json.load(f)
        if not isinstance(c, dict) or "proxmox_url" in c:
            raise ValueError()
        if not isinstance(c.get("clusters"), list) or not all(isinstance(x, dict) for x in c["clusters"]):
            raise ValueError()
    else:
        c = {"clusters": [], "db_path": "proxmox.db", "auth_key_path": "auth.key", "update_check": True}
    c.update(listen_addr="127.0.0.1:8080", trusted_proxies=["127.0.0.1", "::1"], allowed_hosts=[host], db_path="proxmox.db", auth_key_path="auth.key")
    with open(target, "x") as f:
        json.dump(c, f, indent=2)
        f.write("\n")
except (OSError, ValueError, TypeError, RecursionError):
    sys.exit(1)
' "$import_config" "$work/config.json" "$host" || die 'invalid import config (must be current clusters-based JSON)'
}

prepare_import() {
    # Work from a private staged DB. Never modify or run a migration on the source copy.
    # A nonempty WAL means a bare-file import could lose committed data; require a clean stop/backup.
    python3 -c '
import json, os, pathlib, shutil, sqlite3, stat, sys, urllib.parse
config_path, db_path, target_dir = sys.argv[1:]
try:
    for suffix in ("-wal", "-journal"):
        sidecar = db_path + suffix
        if os.path.islink(sidecar) or (os.path.exists(sidecar) and os.path.getsize(sidecar)):
            raise ValueError()
    fd = os.open(db_path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as source:
        if not stat.S_ISREG(os.fstat(source.fileno()).st_mode) or source.read(16) != b"SQLite format 3\x00":
            raise ValueError()
        source.seek(0)
        with open(os.path.join(target_dir, "proxmox.db"), "xb") as target:
            shutil.copyfileobj(source, target)
    staged = os.path.join(target_dir, "proxmox.db")
    uri = "file:" + urllib.parse.quote(staged, safe="/") + "?mode=ro&immutable=1"
    with sqlite3.connect(uri, uri=True, timeout=5) as db:
        # Fixed queries only; input cannot become SQL identifiers or expressions.
        table = db.execute("SELECT 1 FROM sqlite_master WHERE type=? AND name=?", ("table", "auth_users")).fetchone()
        users = table and db.execute("SELECT 1 FROM auth_users LIMIT 1").fetchone()
    if users:
        with open(config_path) as source:
            config = json.load(source)
        key_name = config.get("auth_key_path") or "auth.key"
        if not isinstance(key_name, str) or not key_name or len(key_name) > 4096:
            raise ValueError()
        base = pathlib.Path(config_path).parent.resolve()
        candidate = pathlib.Path(key_name)
        if not candidate.is_absolute():
            candidate = base / candidate
        if candidate.parent.resolve() != base or candidate.is_symlink():
            raise ValueError()
        fd = os.open(candidate, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as source:
            info = os.fstat(source.fileno())
            if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) & 0o077:
                raise ValueError()
            key = source.read(33)
        if len(key) != 32:
            raise ValueError()
        with open(os.path.join(target_dir, "auth.key"), "xb") as target:
            target.write(key)
except (OSError, ValueError, TypeError, sqlite3.Error, RecursionError):
    sys.exit(1)
' "$import_config" "$import_db" "$work" \
        || die 'invalid DB/key import: stop the source, use a clean SQLite copy and keep its private auth.key beside the config when users exist'
}

install_service() {
    local file
    if [[ -n "$import_config" ]]; then
        absent "$APP_DIR/config.json"
    fi
    if [[ -n "$import_db" ]]; then
        absent "$APP_DIR/proxmox.db"
        absent "$APP_DIR/proxmox.db-wal"
        absent "$APP_DIR/proxmox.db-shm"
        absent "$APP_DIR/auth.key"
        regular_file "$import_db"
    fi
    [[ ! -L "$APP_DIR/config.json" ]] || die 'config must not be a symlink'
    if [[ ! -e "$APP_DIR/config.json" ]]; then
        prepare_config
    else
        regular_file "$APP_DIR/config.json"
        [[ -z "$import_config" && -z "$import_db" ]] || die 'imports require a fresh data directory'
        host=$(config_host)
    fi
    if [[ -n "$import_db" ]]; then prepare_import; fi
    for file in config.json proxmox.db auth.key proxmox.db-wal proxmox.db-shm; do
        if [[ -e "$APP_DIR/$file" || -L "$APP_DIR/$file" ]]; then regular_file "$APP_DIR/$file"; fi
    done
    download_release
    regular_file "$SCRIPT_DIR/systemd/proxmox-manager.service"
    install -o root -g root -m 0644 -- "$SCRIPT_DIR/systemd/proxmox-manager.service" "/etc/systemd/system/$SERVICE.service"
    systemctl daemon-reload
    # Kill the whole service cgroup before copying data. Caddy remains configured separately.
    systemctl stop "$SERVICE.service"
    if [[ -e "$work/config.json" ]]; then
        install -o pmgr -g pmgr -m 0600 -- "$work/config.json" "$APP_DIR/config.json"
    fi
    if [[ -n "$import_db" ]]; then
        install -o pmgr -g pmgr -m 0600 -- "$work/proxmox.db" "$APP_DIR/proxmox.db"
        if [[ -f "$work/auth.key" ]]; then
            install -o pmgr -g pmgr -m 0600 -- "$work/auth.key" "$APP_DIR/auth.key"
        fi
    fi
    for file in config.json proxmox.db auth.key proxmox.db-wal proxmox.db-shm; do
        if [[ -e "$APP_DIR/$file" ]]; then
            regular_file "$APP_DIR/$file"
            chown pmgr:pmgr "$APP_DIR/$file"
            chmod 0600 "$APP_DIR/$file"
        fi
    done
    [[ ! -L "$BIN_DIR/proxmox-manager" ]] || die 'binary must not be a symlink'
    if [[ -f "$BIN_DIR/proxmox-manager" ]]; then save_previous; fi
    stage_binary
    mv -fT -- "$next_binary" "$BIN_DIR/proxmox-manager"
    systemctl enable --now "$SERVICE.service"
    # "active" right after start proves nothing (a crash loop looks active for a moment): wait until the
    # service answers with the commit of the verified release.
    wait_version || die "the service does not answer with commit $expected_commit: see journalctl -u $SERVICE"
    printf 'Installed %s (%s) and verified.\n' "$release" "$expected_commit"
    printf '%s\n' \
        'Setup code (sensitive, view locally): journalctl -u proxmox-manager | grep CONFIGURAZIONE' \
        "Or create the first administrator: sudo -u pmgr -H sh -c 'cd ~/proxmox-manager && ./bin/proxmox-manager user add NAME --role admin'" \
        'Configure Caddy and test HTTPS login before switching users to this machine.'
}

main() {
    local action="${1:-}" release='' host='' import_config='' import_db='' expected_commit=''
    case "$action" in install|upgrade|rollback|status) shift ;; *) usage; exit 1 ;; esac
    while [[ $# -gt 0 ]]; do
        [[ $# -ge 2 && -n "$2" ]] || die 'missing option value'
        case "$1" in
            --release) [[ -z "$release" && ( "$action" == install || "$action" == upgrade ) ]] || die 'invalid or repeated --release'; release="$2" ;;
            --host) [[ -z "$host" && "$action" == install ]] || die 'invalid or repeated --host'; host="$2" ;;
            --import-config) [[ -z "$import_config" && "$action" == install ]] || die 'invalid or repeated --import-config'; import_config="$2" ;;
            --import-db) [[ -z "$import_db" && "$action" == install ]] || die 'invalid or repeated --import-db'; import_db="$2" ;;
            *) die 'unknown option' ;;
        esac
        shift 2
    done
    for tool in python3 curl openssl sha256sum systemctl flock realpath stat install getent passwd useradd; do
        command -v "$tool" >/dev/null || die "required tool missing: $tool"
    done
    check_platform
    if [[ "$action" == install || "$action" == upgrade ]]; then
        [[ "$release" =~ ^v[0-9]{4}\.[0-9]{2}\.[0-9]{2}(\.[0-9]+)?$ && ${#release} -le 64 ]] || die 'invalid release tag'
    fi
    if [[ "$action" == install ]]; then
        valid_host "$host" || die 'invalid DNS host name'
        if [[ -n "$import_config" || -n "$import_db" ]]; then
            [[ -n "$import_config" && -n "$import_db" ]] || die 'supply both --import-config and --import-db'
            regular_file "$import_config"
            regular_file "$import_db"
            import_config=$(realpath -e -- "$import_config")
            import_db=$(realpath -e -- "$import_db")
        fi
    fi
    # Serialize all operations; /run is root-owned, unlike the service's writable data directory.
    exec 9> /run/proxmox-manager-installer.lock
    flock -n 9 || die 'another installer operation is running'
    work=$(mktemp -d)
    trap cleanup EXIT
    if [[ "$action" == install ]]; then
        check_directory /home
        if ! getent passwd pmgr >/dev/null; then
            [[ ! -e /home/pmgr && ! -L /home/pmgr ]] || die 'unowned /home/pmgr already exists'
            useradd --system --user-group --create-home --home-dir /home/pmgr --shell /usr/sbin/nologin pmgr
        fi
        check_account
        check_directory /home/pmgr
        # A root-owned home prevents the service user from renaming the deployment directory itself.
        chown root:pmgr /home/pmgr
        chmod 0750 /home/pmgr
        if [[ -e "$APP_DIR" || -L "$APP_DIR" ]]; then check_directory "$APP_DIR"; fi
        install -d -o pmgr -g pmgr -m 0750 -- "$APP_DIR"
        if [[ -e "$BIN_DIR" || -L "$BIN_DIR" ]]; then check_directory "$BIN_DIR"; fi
        install -d -o root -g pmgr -m 0755 -- "$BIN_DIR"
        install_service
        return
    fi
    check_layout
    case "$action" in
        upgrade)
            host=$(config_host)
            download_release
            stage_binary
            save_previous
            mv -fT -- "$next_binary" "$BIN_DIR/proxmox-manager"
            if systemctl restart "$SERVICE.service" && wait_version; then
                printf 'Upgrade to %s verified (%s).\n' "$release" "$expected_commit"
            else
                printf 'Upgrade failed; restoring previous binary.\n' >&2
                restore_previous
                systemctl restart "$SERVICE.service" || die 'rollback restart failed; inspect systemctl status'
                die 'upgrade failed; previous binary restored'
            fi
            ;;
        rollback)
            restore_previous
            systemctl restart "$SERVICE.service"
            printf 'Previous binary restored.\n'
            ;;
        status)
            local state response
            if state=$(systemctl is-active "$SERVICE.service"); then :; else
                printf 'Service: %s\n' "$state"
                stat -c 'Config: %U:%G %a' -- "$APP_DIR/config.json"
                return 1
            fi
            printf 'Service: %s\n' "$state"
            stat -c 'Config: %U:%G %a' -- "$APP_DIR/config.json"
            [[ $(stat -c '%u:%g:%a' -- "$APP_DIR/config.json") == "$(id -u pmgr):$(id -g pmgr):600" ]] \
                || die 'config must be pmgr:pmgr 0600'
            host=$(config_host)
            response=$(read_version) || die 'version probe failed'
            printf '%s' "$response" | python3 -c '
import json, re, sys
try:
    c = json.load(sys.stdin).get("commit", "")
    if not re.fullmatch(r"[a-f0-9]{40}", c): raise ValueError()
    print("Commit:", c)
except (ValueError, AttributeError, TypeError, RecursionError): sys.exit(1)
' || die 'invalid version response'
            ;;
    esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi
