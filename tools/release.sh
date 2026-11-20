#!/usr/bin/env bash
# Publishes a release of the static binary (ROADMAP 24, D134). Run it only when a release has been decided.
#
#   tools/release.sh vYYYY.MM.DD[.N] [--dry-run]
#
# Steps: checks -> tag on main and push (the GitHub workflow builds, tests, attests and creates the release)
# -> wait for the workflow -> download the assets -> verify checksum and attestation -> sign SHA256SUMS with
# the offline ed25519 key of this workstation and upload the signature -> smoke test of the downloaded
# binary on a clean Rocky 8 container.
#
# The private key is read from $PMGR_RELEASE_KEY (default ~/.config/pmgr-release/ed25519.key) and is never
# copied anywhere. The public key is release-signing-key.pub in the repository.
set -euo pipefail

REPO="diegoperu/proxmox-manager"
ASSET="proxmox-manager-linux-amd64"
KEY="${PMGR_RELEASE_KEY:-$HOME/.config/pmgr-release/ed25519.key}"

die() { echo "release: $*" >&2; exit 1; }
step() { echo; echo "== $*"; }

tag="${1:-}"
dry=false
[[ "${2:-}" == "--dry-run" ]] && dry=true
[[ "$tag" =~ ^v[0-9]{4}\.[0-9]{2}\.[0-9]{2}(\.[0-9]+)?$ ]] || die "usage: tools/release.sh vYYYY.MM.DD[.N] [--dry-run]"

cd "$(dirname "$0")/.."
for tool in git gh go sha256sum podman; do command -v "$tool" >/dev/null || die "$tool is required"; done

step "Checks"
[[ "$(git rev-parse --abbrev-ref HEAD)" == "main" ]] || die "not on main"
[[ -z "$(git status --porcelain)" ]] || die "the working tree is not clean"
git fetch --quiet origin main --tags
[[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] || die "local main differs from origin/main"
! git rev-parse -q --verify "refs/tags/$tag" >/dev/null || die "tag $tag already exists locally"
[[ -z "$(git ls-remote --tags origin "refs/tags/$tag")" ]] || die "tag $tag already exists on origin"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated"
[[ -f "$KEY" ]] || die "signing key not found: $KEY"
[[ "$(stat -c '%a' "$KEY")" == "600" ]] || die "signing key must have mode 600"
[[ -f release-signing-key.pub ]] || die "release-signing-key.pub missing"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
go build -o "$work/release-sign" ./tools/release-sign
# The key on this workstation must be the one the repository publishes: sign and verify a probe.
echo "probe" > "$work/probe"
"$work/release-sign" sign -key "$KEY" -in "$work/probe" -out "$work/probe.sig" >/dev/null
"$work/release-sign" verify -pub release-signing-key.pub -in "$work/probe" -sig "$work/probe.sig" >/dev/null \
  || die "the signing key does not match release-signing-key.pub"

step "Tests"
go vet ./...
go test ./... >/dev/null

commit="$(git rev-parse HEAD)"
echo "tag $tag -> commit $commit"
if $dry; then
  echo "dry run: stopping before creating the tag"
  exit 0
fi

step "Tag and push (the workflow starts)"
started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
git tag -a "$tag" -m "Release $tag"
git push --quiet origin "$tag"

step "Waiting for the workflow"
run_id=""
for _ in $(seq 1 30); do
  # Only a run created after the push and on this commit: a tag that is created again after a failed
  # attempt must not pick up the old failed run.
  run_id="$(gh run list --repo "$REPO" --workflow release.yml --limit 10 --json databaseId,headSha,createdAt \
    --jq "[.[] | select(.headSha == \"$commit\" and .createdAt >= \"$started\")] | .[0].databaseId // empty")"
  [[ -n "$run_id" ]] && break
  sleep 5
done
[[ -n "$run_id" ]] || die "the workflow did not start (undo: git push origin :refs/tags/$tag; git tag -d $tag)"
if ! gh run watch "$run_id" --repo "$REPO" --exit-status; then
  die "the workflow failed (see gh run view $run_id --log-failed; undo: git push origin :refs/tags/$tag; git tag -d $tag; gh release delete $tag --yes)"
fi

step "Downloading and verifying the assets"
gh release download "$tag" --repo "$REPO" --dir "$work/dl" --pattern "$ASSET" --pattern SHA256SUMS
(cd "$work/dl" && sha256sum -c SHA256SUMS)
gh attestation verify "$work/dl/$ASSET" --repo "$REPO"

step "Signing SHA256SUMS with the offline key"
"$work/release-sign" sign -key "$KEY" -in "$work/dl/SHA256SUMS" -out "$work/dl/SHA256SUMS.sig"
"$work/release-sign" verify -pub release-signing-key.pub -in "$work/dl/SHA256SUMS" -sig "$work/dl/SHA256SUMS.sig"
gh release upload "$tag" "$work/dl/SHA256SUMS.sig" --repo "$REPO"

step "Smoke test on a clean Rocky 8 container"
cat > "$work/smoke.sh" <<'EOS'
set -eu
mkdir /w && cd /w && cp /app/proxmox-manager-linux-amd64 pm && chmod +x pm
printf '{"clusters":[{"label":"x","url":"https://127.0.0.1:1","api_token":"a@b!c=00000000-0000-0000-0000-000000000000","default":true}],"db_path":"t.db","listen_addr":"127.0.0.1:8099"}' > config.json
./pm > log 2>&1 &
sleep 3
curl -fs http://127.0.0.1:8099/api/version
EOS
version="$(podman run --rm -v "$work/dl":/app:ro,Z -v "$work/smoke.sh":/smoke.sh:ro,Z docker.io/library/rockylinux:8 sh /smoke.sh)"
echo "$version"
[[ "$version" == *"\"commit\":\"$commit\""* ]] || die "the released binary does not report commit $commit"

step "Done"
echo "https://github.com/$REPO/releases/tag/$tag"
