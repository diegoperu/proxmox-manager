#!/usr/bin/env bash
# Sourceable, offline release verification. The caller pins the expected raw ed25519 public key.
set -euo pipefail

verify_release() (
    # A subshell keeps temporary files, traps and umask local to this verification.
    set -euo pipefail
    umask 077
    local release_dir="${1:-}" public_key="${2:-}" scratch key_text signature checksum
    local asset='proxmox-manager-linux-amd64'
    [[ $# == 2 && -d "$release_dir" && ! -L "$release_dir" ]] || return 1
    for file in "$asset" SHA256SUMS SHA256SUMS.sig release-signing-key.pub; do
        [[ -f "$release_dir/$file" && ! -L "$release_dir/$file" ]] || return 1
    done
    [[ $(wc -c < "$release_dir/SHA256SUMS") -le 1048576 ]] || return 1
    [[ $(wc -c < "$release_dir/SHA256SUMS.sig") -le 4096 ]] || return 1
    [[ $(wc -c < "$release_dir/release-signing-key.pub") -le 4096 ]] || return 1
    [[ "$public_key" =~ ^[A-Za-z0-9+/]{43}=$ ]] || return 1
    key_text=$(cat -- "$release_dir/release-signing-key.pub") || return 1
    [[ "$key_text" == "$public_key" ]] || return 1
    signature=$(cat -- "$release_dir/SHA256SUMS.sig") || return 1
    [[ "$signature" =~ ^[A-Za-z0-9+/]{86}==$ ]] || return 1

    scratch=$(mktemp -d) || return 1
    trap 'rm -rf -- "$scratch"' EXIT
    printf '%s' "$public_key" | openssl base64 -d -A > "$scratch/key.raw" || return 1
    [[ $(wc -c < "$scratch/key.raw") == 32 ]] || return 1
    [[ $(openssl base64 -A -in "$scratch/key.raw") == "$public_key" ]] || return 1
    # SubjectPublicKeyInfo DER: 302a300506032b6570032100 followed by the 32 raw key bytes.
    printf '\060\052\060\005\006\003\053\145\160\003\041\000' > "$scratch/pub.der" || return 1
    cat -- "$scratch/key.raw" >> "$scratch/pub.der" || return 1
    openssl pkey -pubin -inform DER -in "$scratch/pub.der" -out "$scratch/pub.pem" 2>/dev/null || return 1
    printf '%s' "$signature" | openssl base64 -d -A > "$scratch/sig.bin" || return 1
    [[ $(wc -c < "$scratch/sig.bin") == 64 ]] || return 1
    [[ $(openssl base64 -A -in "$scratch/sig.bin") == "$signature" ]] || return 1
    printf 'proxmox-manager release signature v1\n' > "$scratch/msg" || return 1
    cat -- "$release_dir/SHA256SUMS" >> "$scratch/msg" || return 1
    openssl pkeyutl -verify -pubin -inkey "$scratch/pub.pem" -rawin \
        -in "$scratch/msg" -sigfile "$scratch/sig.bin" >/dev/null 2>&1 || return 1

    # The current release has exactly one asset. Reject filenames, paths or extra checksum rows
    # before invoking sha256sum; a signed manifest must never make it read outside this directory.
    [[ $(wc -l < "$release_dir/SHA256SUMS") == 1 ]] || return 1
    checksum=$(cat -- "$release_dir/SHA256SUMS") || return 1
    [[ "$checksum" =~ ^[a-f0-9]{64}\ \ proxmox-manager-linux-amd64$ ]] || return 1
    (cd -- "$release_dir" && sha256sum --status --strict -c SHA256SUMS) || return 1
)
