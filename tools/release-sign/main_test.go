package main

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) (dir, key, pub string) {
	t.Helper()
	dir = t.TempDir()
	key, pub = filepath.Join(dir, "k"), filepath.Join(dir, "k.pub")
	var out bytes.Buffer
	if err := run([]string{"keygen", "-key", key, "-pub", pub}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fingerprint") {
		t.Fatalf("no fingerprint: %q", out.String())
	}
	return
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKeygenModesAndNoOverwrite(t *testing.T) {
	dir, key, pub := setup(t)
	if info, _ := os.Stat(key); info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v", info.Mode().Perm())
	}
	if err := run([]string{"keygen", "-key", key, "-pub", filepath.Join(dir, "other.pub")}, &bytes.Buffer{}); err == nil {
		t.Fatal("overwrote an existing private key")
	}
	if _, err := os.Stat(filepath.Join(dir, "other.pub")); err == nil {
		t.Fatal("left a public key behind after a refused keygen")
	}
	if err := run([]string{"keygen", "-key", filepath.Join(dir, "k2"), "-pub", pub}, &bytes.Buffer{}); err == nil {
		t.Fatal("overwrote an existing public key")
	}
	if _, err := os.Stat(filepath.Join(dir, "k2")); err == nil {
		t.Fatal("left a private key behind after a refused keygen")
	}
}

func TestSignVerifyRoundTripAndTamper(t *testing.T) {
	dir, key, pub := setup(t)
	sums, sig := filepath.Join(dir, "SHA256SUMS"), filepath.Join(dir, "SHA256SUMS.sig")
	write(t, sums, "abc123  proxmox-manager-linux-amd64\n")
	if err := run([]string{"sign", "-key", key, "-in", sums, "-out", sig}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"verify", "-pub", pub, "-in", sums, "-sig", sig}, &out); err != nil || !strings.Contains(out.String(), "valid") {
		t.Fatalf("verify: %v %q", err, out.String())
	}
	write(t, sums, "abc124  proxmox-manager-linux-amd64\n")
	if err := run([]string{"verify", "-pub", pub, "-in", sums, "-sig", sig}, &bytes.Buffer{}); err == nil {
		t.Fatal("tampered content verified")
	}
	// A signature from another key must not verify.
	_, key2, pub2 := setup(t)
	write(t, sums, "abc123  proxmox-manager-linux-amd64\n")
	if err := run([]string{"verify", "-pub", pub2, "-in", sums, "-sig", sig}, &bytes.Buffer{}); err == nil {
		t.Fatal("verified with the wrong public key")
	}
	_ = key2
}

func TestDomainSeparation(t *testing.T) {
	// The raw signature of the content (without the domain prefix) must not be accepted.
	dir, key, pub := setup(t)
	priv, err := loadPrivate(key)
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := loadPublic(pub)
	content := []byte("same content\n")
	if sigOK := verifyRaw(pk, content, priv); sigOK {
		t.Fatal("a signature without the domain prefix verified")
	}
	_ = dir
}

func TestPrivateKeyChecks(t *testing.T) {
	dir, key, _ := setup(t)
	sums, sig := filepath.Join(dir, "S"), filepath.Join(dir, "S.sig")
	write(t, sums, "x\n")
	if err := os.Chmod(key, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", "-key", key, "-in", sums, "-out", sig}, &bytes.Buffer{}); err == nil {
		t.Fatal("signed with a group-readable key")
	}
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", "-key", link, "-in", sums, "-out", sig}, &bytes.Buffer{}); err == nil {
		t.Fatal("signed with a symlinked key")
	}
	write(t, filepath.Join(dir, "bad"), "not base64!!\n")
	if err := os.Chmod(filepath.Join(dir, "bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", "-key", filepath.Join(dir, "bad"), "-in", sums, "-out", sig}, &bytes.Buffer{}); err == nil {
		t.Fatal("signed with a malformed key")
	}
}

func TestInputLimitsAndArguments(t *testing.T) {
	dir, key, pub := setup(t)
	empty, big, sig := filepath.Join(dir, "empty"), filepath.Join(dir, "big"), filepath.Join(dir, "sig")
	write(t, empty, "")
	write(t, big, strings.Repeat("a", maxInput+1))
	for _, in := range []string{empty, big} {
		if err := run([]string{"sign", "-key", key, "-in", in, "-out", sig}, &bytes.Buffer{}); err == nil {
			t.Fatalf("signed %s", in)
		}
	}
	write(t, sig, "AAAA\n")
	if err := run([]string{"verify", "-pub", pub, "-in", empty, "-sig", sig}, &bytes.Buffer{}); err == nil {
		t.Fatal("malformed signature accepted")
	}
	for _, args := range [][]string{nil, {"nope"}, {"sign"}, {"verify"}, {"keygen"}, {"sign", "-bogus"}} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

// verifyRaw reports whether a signature over the bare content (no domain prefix) would verify.
func verifyRaw(pub ed25519.PublicKey, content []byte, priv ed25519.PrivateKey) bool {
	return ed25519.Verify(pub, message(content), ed25519.Sign(priv, content))
}
