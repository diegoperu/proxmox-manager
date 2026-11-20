// Command release-sign creates, uses and checks the ed25519 key that signs the SHA256SUMS file of a
// release (ROADMAP 24, D134 C1). It only uses the standard library, so the same verification can later
// be built into the server for an update button.
//
//	release-sign keygen -key FILE -pub FILE
//	release-sign sign   -key FILE -in SHA256SUMS -out SHA256SUMS.sig
//	release-sign verify -pub FILE -in SHA256SUMS -sig SHA256SUMS.sig
//
// The private key never leaves the workstation of whoever publishes releases and is never committed.
// The signed message is a fixed domain prefix followed by the file content, so a signature made here
// cannot be replayed as a signature of anything else.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	domain      = "proxmox-manager release signature v1\n"
	maxInput    = 1 << 20 // SHA256SUMS is a few hundred bytes; anything larger is a mistake
	maxKeyFile  = 4096
	keyFileMode = 0o600
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release-sign:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: release-sign keygen|sign|verify (see the package comment)")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	key := fs.String("key", "", "private key file")
	pub := fs.String("pub", "", "public key file")
	in := fs.String("in", "", "file to sign or verify")
	sigPath := fs.String("sig", "", "signature file to verify")
	outPath := fs.String("out", "", "signature file to write")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "keygen":
		return keygen(*key, *pub, out)
	case "sign":
		return sign(*key, *in, *outPath, out)
	case "verify":
		return verify(*pub, *in, *sigPath, out)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

func keygen(keyPath, pubPath string, out io.Writer) error {
	if keyPath == "" || pubPath == "" {
		return errors.New("keygen needs -key and -pub")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	// O_EXCL: never overwrite an existing key, losing it would invalidate every release.
	kf, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyFileMode)
	if err != nil {
		return fmt.Errorf("cannot create the private key file (it must not exist): %w", err)
	}
	_, werr := fmt.Fprintln(kf, base64.StdEncoding.EncodeToString(privateKey.Seed()))
	if cerr := kf.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(keyPath)
		return werr
	}
	pf, err := os.OpenFile(pubPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		os.Remove(keyPath)
		return fmt.Errorf("cannot create the public key file (it must not exist): %w", err)
	}
	_, werr = fmt.Fprintf(pf, "%s\n", base64.StdEncoding.EncodeToString(publicKey))
	if cerr := pf.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(keyPath)
		os.Remove(pubPath)
		return werr
	}
	fmt.Fprintf(out, "public key fingerprint: %s\n", fingerprint(publicKey))
	return nil
}

func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

func loadPrivate(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the private key must be a regular file, not a link")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("the private key file is readable by group or others: chmod 600 it")
	}
	data, err := readSmall(path, maxKeyFile)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("the private key file is not valid")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func loadPublic(path string) (ed25519.PublicKey, error) {
	data, err := readSmall(path, maxKeyFile)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("the public key file is not valid")
	}
	return ed25519.PublicKey(raw), nil
}

func message(content []byte) []byte { return append([]byte(domain), content...) }

func sign(keyPath, inPath, outPath string, out io.Writer) error {
	if keyPath == "" || inPath == "" || outPath == "" {
		return errors.New("sign needs -key, -in and -out")
	}
	priv, err := loadPrivate(keyPath)
	if err != nil {
		return err
	}
	content, err := readSmall(inPath, maxInput)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return errors.New("refusing to sign an empty file")
	}
	sig := ed25519.Sign(priv, message(content))
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(sig))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	fmt.Fprintf(out, "signed %s with key %s\n", inPath, fingerprint(priv.Public().(ed25519.PublicKey)))
	return nil
}

func verify(pubPath, inPath, sigPath string, out io.Writer) error {
	if pubPath == "" || inPath == "" || sigPath == "" {
		return errors.New("verify needs -pub, -in and -sig")
	}
	pub, err := loadPublic(pubPath)
	if err != nil {
		return err
	}
	content, err := readSmall(inPath, maxInput)
	if err != nil {
		return err
	}
	raw, err := readSmall(sigPath, maxKeyFile)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("the signature file is not valid")
	}
	if !ed25519.Verify(pub, message(content), sig) {
		return errors.New("SIGNATURE NOT VALID")
	}
	fmt.Fprintf(out, "signature valid (key %s)\n", fingerprint(pub))
	return nil
}
