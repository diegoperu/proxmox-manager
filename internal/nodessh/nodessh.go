// Package nodessh runs the manager's only SSH operations towards Proxmox nodes (onboarding step 3).
//
// The manager logs in as pmgr-sync, an unprivileged account whose key is restricted on the node by the
// pmgr-sync-gate forced command (tools/pmgr-sync): it can list two folders (pmgr-list) and write into them with
// rsync, nothing else. This package never trusts a host key on first use: a key is used only after an
// administrator compared its fingerprint with the one printed on the node and confirmed it (D152).
//
// Every external program is started with an argument list (no shell), a minimal environment (no agent, no user
// ssh_config), a deadline and a cap on the output it keeps.
package nodessh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	keyFile    = "id_ed25519"
	knownFile  = "known_hosts"
	keyComment = "proxmox-manager"
	syncUser   = "pmgr-sync"

	// VerifyCommand prints the fingerprint on the node, to compare with the one the manager received.
	VerifyCommand = "ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub"

	maxListOutput = 4 << 20 // pmgr-list prints at most 2000 lines of ~150 bytes
	maxListLines  = 2000
)

var (
	ErrNotPinned       = errors.New("impronta del nodo non ancora confermata")
	ErrHostKeyChanged  = errors.New("la chiave del nodo non corrisponde all'impronta confermata")
	ErrAuthRefused     = errors.New("il nodo rifiuta la chiave del manager")
	ErrGateDenied      = errors.New("il comando ristretto del nodo ha rifiutato la richiesta")
	ErrUnreachable     = errors.New("nodo non raggiungibile via SSH")
	ErrBadAddress      = errors.New("indirizzo del nodo non valido")
	ErrNoHostKey       = errors.New("il nodo non ha presentato una chiave ed25519")
	ErrUnsafeDirectory = errors.New("cartella delle chiavi SSH non sicura")
	ErrSSH             = errors.New("errore SSH")
)

// Result of an external program.
type Result struct {
	Stdout, Stderr []byte
	Exit           int
}

// Runner starts name with args (never through a shell) and env as its whole environment.
type Runner func(ctx context.Context, name string, args, env []string, maxOut int) (Result, error)

// Store owns the manager key and the pinned host keys, both in one 0700 directory.
type Store struct {
	dir string
	run Runner
	mu  sync.Mutex
}

// New returns a store using the real ssh tools.
func New(dir string) *Store { return &Store{dir: dir, run: execRunner} }

// NewWithRunner is for tests.
func NewWithRunner(dir string, run Runner) *Store { return &Store{dir: dir, run: run} }

// PublicKey is the manager key as written in authorized_keys, and its SHA256 fingerprint.
type PublicKey struct {
	Line        string `json:"line"`
	Fingerprint string `json:"fingerprint"`
}

var keyLinePattern = regexp.MustCompile(`^ssh-ed25519 ([A-Za-z0-9+/]{68}) ` + keyComment + `$`)

// ValidAddress accepts a literal IPv4 or IPv6 address (no names: nothing to resolve, nothing to spoof via DNS).
func ValidAddress(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && ip.String() == s
}

// ed25519Blob decodes the base64 of an ssh-ed25519 public key and checks its exact wire format.
func ed25519Blob(b64 string) ([]byte, bool) {
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(blob) != 51 {
		return nil, false
	}
	if binary.BigEndian.Uint32(blob[0:4]) != 11 || string(blob[4:15]) != "ssh-ed25519" || binary.BigEndian.Uint32(blob[15:19]) != 32 {
		return nil, false
	}
	return blob, true
}

// Fingerprint returns the OpenSSH SHA256 fingerprint of a key blob.
func Fingerprint(blob []byte) string {
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// prepareDir creates the directory 0700, refuses a symlink or a non-directory and tightens a wider mode.
func (s *Store) prepareDir() error {
	fi, err := os.Lstat(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(s.dir, 0o700)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return ErrUnsafeDirectory
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(s.dir, 0o700)
	}
	return nil
}

// privateKeyOK refuses a private key that is a symlink, not a regular file or readable by others.
func privateKeyOK(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return ErrUnsafeDirectory
	}
	return nil
}

// EnsureKey returns the manager public key, generating the key pair with ssh-keygen the first time.
func (s *Store) EnsureKey(ctx context.Context) (PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepareDir(); err != nil {
		return PublicKey{}, err
	}
	priv := filepath.Join(s.dir, keyFile)
	if _, err := os.Lstat(priv); errors.Is(err, os.ErrNotExist) {
		res, err := s.run(ctx, "ssh-keygen", []string{"-q", "-t", "ed25519", "-N", "", "-C", keyComment, "-f", priv}, s.env(), 64<<10)
		if err != nil || res.Exit != 0 {
			return PublicKey{}, fmt.Errorf("creazione della chiave SSH non riuscita")
		}
		if err := os.Chmod(priv, 0o600); err != nil {
			return PublicKey{}, err
		}
	}
	if err := privateKeyOK(priv); err != nil {
		return PublicKey{}, err
	}
	raw, err := os.ReadFile(priv + ".pub")
	if err != nil {
		return PublicKey{}, err
	}
	line := strings.TrimSpace(string(raw))
	m := keyLinePattern.FindStringSubmatch(line)
	if m == nil {
		return PublicKey{}, fmt.Errorf("chiave pubblica del manager non riconosciuta")
	}
	blob, ok := ed25519Blob(m[1])
	if !ok {
		return PublicKey{}, fmt.Errorf("chiave pubblica del manager non riconosciuta")
	}
	return PublicKey{Line: line, Fingerprint: Fingerprint(blob)}, nil
}

func (s *Store) env() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + s.dir, "LC_ALL=C"}
}

// ── pinned host keys (an OpenSSH known_hosts file written only by this package) ──

type knownEntry struct{ host, b64 string }

func (s *Store) readKnown() ([]knownEntry, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, knownFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []knownEntry
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 || !ValidAddress(f[0]) || f[1] != "ssh-ed25519" {
			return nil, fmt.Errorf("file delle impronte non valido")
		}
		if _, ok := ed25519Blob(f[2]); !ok {
			return nil, fmt.Errorf("file delle impronte non valido")
		}
		out = append(out, knownEntry{f[0], f[2]})
	}
	return out, nil
}

func (s *Store) writeKnown(list []knownEntry) error {
	sort.Slice(list, func(i, j int) bool { return list[i].host < list[j].host })
	var b strings.Builder
	for _, e := range list {
		fmt.Fprintf(&b, "%s ssh-ed25519 %s\n", e.host, e.b64)
	}
	f, err := os.CreateTemp(s.dir, ".known-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.dir, knownFile))
}

// Pinned returns the confirmed fingerprint of a node address.
func (s *Store) Pinned(host string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readKnown()
	if err != nil {
		return "", false, err
	}
	for _, e := range list {
		if e.host == host {
			blob, _ := ed25519Blob(e.b64)
			return Fingerprint(blob), true, nil
		}
	}
	return "", false, nil
}

// Scan asks the node for its ed25519 host key. The result is NOT trusted: it is only shown to an administrator.
func (s *Store) Scan(ctx context.Context, host string) (b64, fingerprint string, err error) {
	if !ValidAddress(host) {
		return "", "", ErrBadAddress
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := s.run(ctx, "ssh-keyscan", []string{"-T", "5", "-t", "ed25519", "--", host}, s.env(), 64<<10)
	if err != nil {
		return "", "", ErrUnreachable
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != host || f[1] != "ssh-ed25519" {
			continue
		}
		if blob, ok := ed25519Blob(f[2]); ok {
			return f[2], Fingerprint(blob), nil
		}
	}
	// ssh-keyscan prints "# <host>:22 SSH-2.0-..." on stderr when the SSH server answered; without it nothing answered.
	if !strings.Contains(string(res.Stderr), "# "+host+":") && !strings.Contains(string(res.Stdout), "# "+host+":") {
		return "", "", ErrUnreachable
	}
	return "", "", ErrNoHostKey
}

// Pin re-reads the node key and stores it only if its fingerprint is the one the administrator confirmed.
// It returns ErrHostKeyChanged when the node now presents a different key.
func (s *Store) Pin(ctx context.Context, host, confirmed string) error {
	b64, fp, err := s.Scan(ctx, host)
	if err != nil {
		return err
	}
	if fp != confirmed {
		return ErrHostKeyChanged
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepareDir(); err != nil {
		return err
	}
	list, err := s.readKnown()
	if err != nil {
		return err
	}
	out := list[:0]
	for _, e := range list {
		if e.host != host {
			out = append(out, e)
		}
	}
	return s.writeKnown(append(out, knownEntry{host, b64}))
}

// Unpin forgets the key of a node address; it reports whether there was one.
func (s *Store) Unpin(host string) (bool, error) {
	if !ValidAddress(host) {
		return false, ErrBadAddress
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readKnown()
	if err != nil {
		return false, err
	}
	out := list[:0]
	found := false
	for _, e := range list {
		if e.host == host {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return false, nil
	}
	return true, s.writeKnown(out)
}

// ── pmgr-list ──

// Entry is one file reported by the node gate.
type Entry struct {
	Hash  string `json:"hash"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Path  string `json:"path"`
}

// Listing is the parsed output of pmgr-list. The node is not trusted: malformed lines are only counted.
type Listing struct {
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
	Invalid   int     `json:"invalid"`
}

var (
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pathPattern = regexp.MustCompile(`^(snippets|pmgr-scripts)/[A-Za-z0-9._/@+-]{1,240}$`)
)

// ParseListing reads the gate output (hash TAB size TAB mtime TAB path, or TRUNCATED / INVALID lines).
func ParseListing(out []byte) Listing {
	l := Listing{Entries: []Entry{}}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 4096), 4096)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			continue
		case line == "TRUNCATED":
			l.Truncated = true
			continue
		case len(l.Entries) >= maxListLines:
			l.Truncated = true
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 || !hashPattern.MatchString(f[0]) || !pathPattern.MatchString(f[3]) || strings.Contains(f[3], "..") {
			l.Invalid++
			continue
		}
		size, err1 := strconv.ParseInt(f[1], 10, 64)
		mtime, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || size < 0 || mtime < 0 {
			l.Invalid++
			continue
		}
		l.Entries = append(l.Entries, Entry{f[0], size, mtime, f[3]})
	}
	if sc.Err() != nil { // a line longer than the buffer: the node sent something that is not a listing
		l.Invalid++
	}
	return l
}

// Digest identifies the content of a listing (hashes and paths, not dates), to compare nodes.
func (l Listing) Digest() string {
	lines := make([]string, 0, len(l.Entries))
	for _, e := range l.Entries {
		lines = append(lines, e.Hash+" "+e.Path)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return base64.RawStdEncoding.EncodeToString(sum[:12])
}

// sshOptions pins every option that matters, so neither a user ssh_config nor an agent can change the behaviour.
func (s *Store) sshOptions() []string {
	return []string{
		"-F", "/dev/null",
		"-i", filepath.Join(s.dir, keyFile),
		"-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
		"-o", "BatchMode=yes", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(s.dir, knownFile),
		"-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no", "-o", "CheckHostIP=no",
		"-o", "HostKeyAlgorithms=ssh-ed25519",
		"-o", "ConnectTimeout=8", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2",
		"-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes",
		"-o", "PermitLocalCommand=no", "-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "LogLevel=ERROR", "-o", "Port=22", "-T",
	}
}

func (s *Store) sshArgs(host string, remote ...string) []string {
	args := append(s.sshOptions(), "--", syncUser+"@"+host)
	return append(args, remote...)
}

// classify maps the ssh exit code and message to an error the page can explain; the raw text is never returned.
func classify(res Result) error {
	if res.Exit == 126 {
		return ErrGateDenied
	}
	msg := string(res.Stderr)
	switch {
	case strings.Contains(msg, "REMOTE HOST IDENTIFICATION HAS CHANGED"), strings.Contains(msg, "Host key verification failed"):
		return ErrHostKeyChanged
	case strings.Contains(msg, "Permission denied"):
		return ErrAuthRefused
	case strings.Contains(msg, "Connection refused"), strings.Contains(msg, "timed out"), strings.Contains(msg, "No route to host"),
		strings.Contains(msg, "Network is unreachable"), strings.Contains(msg, "Connection closed"), strings.Contains(msg, "Connection reset"):
		return ErrUnreachable
	}
	return ErrSSH
}

// List runs pmgr-list on a node whose key has been confirmed.
func (s *Store) List(ctx context.Context, host string) (Listing, error) {
	if !ValidAddress(host) {
		return Listing{}, ErrBadAddress
	}
	if _, ok, err := s.Pinned(host); err != nil {
		return Listing{}, err
	} else if !ok {
		return Listing{}, ErrNotPinned
	}
	if err := privateKeyOK(filepath.Join(s.dir, keyFile)); err != nil {
		return Listing{}, ErrUnsafeDirectory
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := s.run(ctx, "ssh", s.sshArgs(host, "pmgr-list"), s.env(), maxListOutput)
	if err != nil {
		return Listing{}, ErrUnreachable
	}
	if res.Exit != 0 {
		return Listing{}, classify(res)
	}
	return ParseListing(res.Stdout), nil
}

// ── rsync push through the gate ──

// plainPath: rsync splits its -e command on spaces (no shell), so every path in it must be free of spaces and quotes.
var plainPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,400}$`)

// RemoteDirs: the only two folders the node gate accepts.
var RemoteDirs = map[string]bool{"snippets": true, "pmgr-scripts": true}

// Push copies the content of localDir into remote ("snippets" or "pmgr-scripts") on a node whose key has been
// confirmed. Never --delete; checksums decide what is copied; the gate on the node refuses anything else.
func (s *Store) Push(ctx context.Context, host, localDir, remote string) error {
	if !ValidAddress(host) {
		return ErrBadAddress
	}
	if !RemoteDirs[remote] {
		return errors.New("cartella di destinazione non ammessa")
	}
	absDir, err := filepath.Abs(s.dir)
	if err != nil || !plainPath.MatchString(absDir) || !plainPath.MatchString(localDir) || strings.Contains(absDir+localDir, "..") {
		return ErrUnsafeDirectory
	}
	if _, ok, err := s.Pinned(host); err != nil {
		return err
	} else if !ok {
		return ErrNotPinned
	}
	if err := privateKeyOK(filepath.Join(s.dir, keyFile)); err != nil {
		return ErrUnsafeDirectory
	}
	opts := s.sshOptions()
	for i, o := range opts { // absolute paths: rsync starts ssh from its own working directory
		if strings.HasPrefix(o, "UserKnownHostsFile=") {
			opts[i] = "UserKnownHostsFile=" + filepath.Join(absDir, knownFile)
		}
		if i > 0 && opts[i-1] == "-i" {
			opts[i] = filepath.Join(absDir, keyFile)
		}
	}
	dest := host
	if strings.Contains(host, ":") {
		dest = "[" + host + "]"
	}
	args := []string{"-rltOEc", "--no-owner", "--no-group", "--timeout=60", "-e", "ssh " + strings.Join(opts, " "),
		"--", strings.TrimSuffix(localDir, "/") + "/", syncUser + "@" + dest + ":" + remote + "/"}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := s.run(ctx, "rsync", args, s.env(), 256<<10)
	if err != nil {
		return ErrUnreachable
	}
	if res.Exit != 0 {
		if e := classify(res); !errors.Is(e, ErrSSH) {
			return e
		}
		return fmt.Errorf("%w: rsync uscito con %d", ErrSSH, res.Exit)
	}
	return nil
}
