#!/usr/bin/env bash
# Offline tests only: no root, network, real signing key, account or service changes.
set -euo pipefail
umask 077
script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source "$script_dir/install.sh"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
[[ $(cat -- "$script_dir/../release-signing-key.pub") == "$RELEASE_KEY" ]] || fail 'embedded release public key differs from repository key'
reject() {
    if verify_release "$work/release" "$public_key"; then
        fail "$1 was accepted"
    fi
    printf 'PASS: rejected %s\n' "$1"
}
sign_manifest() {
    printf '%s\n' "$1" > "$work/message"
    cat -- "$work/release/SHA256SUMS" >> "$work/message"
    openssl pkeyutl -sign -inkey "$work/private.pem" -rawin \
        -in "$work/message" -out "$work/signature"
    openssl base64 -A -in "$work/signature" > "$work/release/SHA256SUMS.sig"
    printf '\n' >> "$work/release/SHA256SUMS.sig"
}

mkdir -- "$work/release"
openssl genpkey -algorithm ED25519 -out "$work/private.pem" 2>/dev/null
openssl pkey -in "$work/private.pem" -pubout -outform DER -out "$work/public.der"
tail -c 32 -- "$work/public.der" | openssl base64 -A > "$work/release/release-signing-key.pub"
public_key=$(cat -- "$work/release/release-signing-key.pub")
printf 'fake executable\n' > "$work/release/proxmox-manager-linux-amd64"
(cd -- "$work/release" && sha256sum proxmox-manager-linux-amd64 > SHA256SUMS)
cp -- "$work/release/SHA256SUMS" "$work/original-sums"
sign_manifest 'proxmox-manager release signature v1'
verify_release "$work/release" "$public_key" || fail 'valid release was rejected'
printf 'PASS: valid signature and checksum\n'

printf '\n' >> "$work/release/SHA256SUMS"
reject 'tampered SHA256SUMS'
cp -- "$work/original-sums" "$work/release/SHA256SUMS"

openssl genpkey -algorithm ED25519 -out "$work/wrong-private.pem" 2>/dev/null
openssl pkey -in "$work/wrong-private.pem" -pubout -outform DER -out "$work/wrong-public.der"
tail -c 32 -- "$work/wrong-public.der" | openssl base64 -A > "$work/release/release-signing-key.pub"
reject 'tag public key mismatch'
wrong_key=$(cat -- "$work/release/release-signing-key.pub")
if verify_release "$work/release" "$wrong_key"; then fail 'signature with wrong key was accepted'; fi
printf 'PASS: rejected signature with wrong key\n'
printf '%s\n' "$public_key" > "$work/release/release-signing-key.pub"

sign_manifest 'wrong release domain'
reject 'wrong domain'
sign_manifest 'proxmox-manager release signature v1'
printf 'tampered executable\n' >> "$work/release/proxmox-manager-linux-amd64"
reject 'tampered binary'
printf 'All offline release verification tests passed.\n'

for host in pmgr.example.edu localhost PMGR-01.internal; do
    valid_host "$host" || fail 'valid DNS host was rejected'
done
for host in '' '-invalid.test' 'https://pmgr.example.edu' 'pmgr:443' '../outside' $'pmgr\r\nInjected: yes'; do
    if valid_host "$host"; then fail 'invalid DNS host was accepted'; fi
done
printf 'PASS: DNS host validation\n'

mkdir -- "$work/source" "$work/config-test"
python3 - "$work/source" <<'PY'
import json, pathlib, sqlite3, sys
base = pathlib.Path(sys.argv[1])
(base / "config.json").write_text(json.dumps({"clusters": [{"label": "test", "api_token": "fake-test-token"}], "db_path": "/old/path/data.db", "auth_key_path": "custom.key"}))
with sqlite3.connect(base / "proxmox.db") as db:
    db.execute("CREATE TABLE history (value TEXT)")
    db.execute("INSERT INTO history VALUES (?)", ("preserved",))
PY
import_config="$work/source/config.json"
import_db="$work/source/proxmox.db"
host=pmgr.example.edu
original_work="$work"
(
    work="$original_work/config-test"
    prepare_config
    prepare_import
    python3 - "$work" <<'PY'
import json, pathlib, sqlite3, sys
base = pathlib.Path(sys.argv[1])
c = json.loads((base / "config.json").read_text())
assert c["listen_addr"] == "127.0.0.1:8080"
assert c["trusted_proxies"] == ["127.0.0.1", "::1"]
assert c["allowed_hosts"] == ["pmgr.example.edu"]
assert c["db_path"] == "proxmox.db" and c["auth_key_path"] == "auth.key"
assert c["clusters"][0]["api_token"] == "fake-test-token"
assert (base / "config.json").stat().st_mode & 0o777 == 0o600
with sqlite3.connect(base / "proxmox.db") as db:
    assert db.execute("SELECT value FROM history").fetchone() == ("preserved",)
PY
)
printf 'PASS: import preserves configuration/history and confines deployment paths\n'

reject_import() {
    mkdir -- "$original_work/$1"
    if (work="$original_work/$1"; prepare_import) > "$original_work/import-output" 2>&1; then
        fail "$1 was accepted"
    fi
    if grep -q 'fake-test-token' "$original_work/import-output"; then fail 'import leaked config'; fi
    printf 'PASS: rejected %s\n' "$1"
}
python3 - "$import_db" <<'PY'
import sqlite3, sys
with sqlite3.connect(sys.argv[1]) as db:
    db.execute("CREATE TABLE auth_users (username TEXT)")
    db.execute("INSERT INTO auth_users VALUES (?)", ("testadmin",))
PY
reject_import missing-auth-key
python3 - "$original_work/source/custom.key" <<'PY'
import pathlib, sys
pathlib.Path(sys.argv[1]).write_bytes(bytes(range(32)))
PY
mkdir -- "$original_work/with-key"
(work="$original_work/with-key"; prepare_import)
cmp -- "$original_work/source/custom.key" "$original_work/with-key/auth.key" || fail 'key import changed bytes'
printf 'PASS: imports the existing private TOTP key with existing users\n'
chmod 0644 "$original_work/source/custom.key"
reject_import readable-auth-key
chmod 0600 "$original_work/source/custom.key"
mv -- "$original_work/source/custom.key" "$original_work/key-outside"
ln -s -- "$original_work/key-outside" "$original_work/source/custom.key"
reject_import symlink-auth-key
rm -- "$original_work/source/custom.key"
python3 - "$import_config" "$original_work/key-outside" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
c = json.loads(p.read_text())
c["auth_key_path"] = sys.argv[2]
p.write_text(json.dumps(c))
PY
reject_import outside-auth-key
printf 'pending WAL bytes' > "$import_db-wal"
reject_import nonempty-source-WAL
printf 'All offline installer tests passed.\n'
