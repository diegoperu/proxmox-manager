package nodessh

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func keyB64(seed byte) string {
	blob := make([]byte, 0, 51)
	blob = binary.BigEndian.AppendUint32(blob, 11)
	blob = append(blob, "ssh-ed25519"...)
	blob = binary.BigEndian.AppendUint32(blob, 32)
	for i := 0; i < 32; i++ {
		blob = append(blob, seed+byte(i))
	}
	return base64.StdEncoding.EncodeToString(blob)
}

func fpOf(b64 string) string { blob, _ := ed25519Blob(b64); return Fingerprint(blob) }

type call struct {
	name string
	args []string
	env  []string
}

// fake plays ssh-keygen, ssh-keyscan and ssh.
type fake struct {
	mu      sync.Mutex
	calls   []call
	hostKey map[string]string // address -> base64 key presented
	ssh     Result
	sshErr  error
}

func (f *fake) run(ctx context.Context, name string, args, env []string, maxOut int) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{name, append([]string{}, args...), append([]string{}, env...)})
	f.mu.Unlock()
	switch name {
	case "ssh-keygen":
		path := args[len(args)-1]
		if err := os.WriteFile(path, []byte("PRIVATE"), 0o644); err != nil {
			return Result{}, err
		}
		pub := "ssh-ed25519 " + keyB64(7) + " " + keyComment + "\n"
		return Result{}, os.WriteFile(path+".pub", []byte(pub), 0o644)
	case "ssh-keyscan":
		host := args[len(args)-1]
		k, ok := f.hostKey[host]
		if !ok {
			return Result{Exit: 1}, nil
		}
		return Result{Stdout: []byte("# " + host + ":22 SSH-2.0-OpenSSH\n" + host + " ssh-rsa AAAAB3Nza\n" + host + " ssh-ed25519 " + k + "\n")}, nil
	case "ssh":
		return f.ssh, f.sshErr
	}
	return Result{}, errors.New("unexpected program " + name)
}

func (f *fake) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.name == name {
			n++
		}
	}
	return n
}

func newStore(t *testing.T) (*Store, *fake, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ssh")
	f := &fake{hostKey: map[string]string{"192.0.2.10": keyB64(1), "2001:db8::10": keyB64(2)}}
	return NewWithRunner(dir, f.run), f, dir
}

func TestEnsureKeyCreatesOnceWithSafeModes(t *testing.T) {
	s, f, dir := newStore(t)
	k, err := s.EnsureKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Line, "ssh-ed25519 ") || k.Fingerprint != fpOf(keyB64(7)) {
		t.Fatalf("key %+v", k)
	}
	got := f.calls[0].args
	want := []string{"-q", "-t", "ed25519", "-N", "", "-C", keyComment, "-f", filepath.Join(dir, keyFile)}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("ssh-keygen args %q", got)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode().Perm())
	}
	fi, _ = os.Stat(filepath.Join(dir, keyFile))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", fi.Mode().Perm())
	}
	if _, err := s.EnsureKey(context.Background()); err != nil || f.count("ssh-keygen") != 1 {
		t.Fatalf("second call: err %v, keygen runs %d", err, f.count("ssh-keygen"))
	}
}

func TestEnsureKeyRefusesUnsafeFiles(t *testing.T) {
	s, _, dir := newStore(t)
	if _, err := s.EnsureKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, keyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureKey(context.Background()); !errors.Is(err, ErrUnsafeDirectory) {
		t.Fatalf("readable private key accepted: %v", err)
	}

	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	if _, err := NewWithRunner(link, f.run).EnsureKey(context.Background()); !errors.Is(err, ErrUnsafeDirectory) || f.count("ssh-keygen") != 0 {
		t.Fatalf("symlinked directory accepted: %v", err)
	}
}

func TestEnsureKeyTightensDirectoryMode(t *testing.T) {
	s, _, dir := newStore(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestScanParsesOnlyTheEd25519KeyOfThatHost(t *testing.T) {
	s, f, _ := newStore(t)
	b64, fp, err := s.Scan(context.Background(), "192.0.2.10")
	if err != nil || b64 != keyB64(1) || fp != fpOf(keyB64(1)) {
		t.Fatalf("scan %q %q %v", b64, fp, err)
	}
	if args := f.calls[0].args; strings.Join(args, " ") != "-T 5 -t ed25519 -- 192.0.2.10" {
		t.Fatalf("keyscan args %q", args)
	}
	if _, _, err := s.Scan(context.Background(), "2001:db8::10"); err != nil {
		t.Fatalf("ipv6: %v", err)
	}
	f.hostKey["192.0.2.11"] = "!!notbase64"
	if _, _, err := s.Scan(context.Background(), "192.0.2.11"); !errors.Is(err, ErrNoHostKey) {
		t.Fatalf("bad key accepted: %v", err)
	}
	for _, bad := range []string{"", "node-1", "192.0.2.1;id", "-oProxyCommand=x", "0.0.0.0", "192.0.2.010", "::ffff:192.0.2.1"} {
		before := len(f.calls)
		if _, _, err := s.Scan(context.Background(), bad); !errors.Is(err, ErrBadAddress) || len(f.calls) != before {
			t.Fatalf("address %q: err %v, ran a program %v", bad, err, len(f.calls) != before)
		}
	}
}

func TestPinStoresOnlyTheConfirmedKey(t *testing.T) {
	s, f, dir := newStore(t)
	ctx := context.Background()
	if err := s.Pin(ctx, "192.0.2.10", fpOf(keyB64(9))); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("wrong fingerprint pinned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, knownFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("known_hosts written after a refused pin")
	}
	if err := s.Pin(ctx, "192.0.2.10", fpOf(keyB64(1))); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(ctx, "2001:db8::10", fpOf(keyB64(2))); err != nil {
		t.Fatal(err)
	}
	fp, ok, err := s.Pinned("192.0.2.10")
	if err != nil || !ok || fp != fpOf(keyB64(1)) {
		t.Fatalf("pinned %q %v %v", fp, ok, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, knownFile))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode %v", fi.Mode().Perm())
	}
	// the node changes key and the administrator confirms the new one: one line per host
	f.hostKey["192.0.2.10"] = keyB64(3)
	if err := s.Pin(ctx, "192.0.2.10", fpOf(keyB64(3))); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, knownFile))
	if strings.Count(string(raw), "192.0.2.10 ") != 1 || !strings.Contains(string(raw), keyB64(3)) {
		t.Fatalf("known_hosts %q", raw)
	}
	if found, err := s.Unpin("192.0.2.10"); !found || err != nil {
		t.Fatalf("unpin %v %v", found, err)
	}
	if _, ok, _ := s.Pinned("192.0.2.10"); ok {
		t.Fatal("still pinned")
	}
	if _, ok, _ := s.Pinned("2001:db8::10"); !ok {
		t.Fatal("other host lost")
	}
	if found, _ := s.Unpin("192.0.2.99"); found {
		t.Fatal("unknown host reported as removed")
	}
}

func TestCorruptKnownHostsIsAnError(t *testing.T) {
	s, _, dir := newStore(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"192.0.2.10 ssh-rsa AAAA\n", "*.example ssh-ed25519 " + keyB64(1) + "\n", "192.0.2.10 ssh-ed25519 short\n"} {
		if err := os.WriteFile(filepath.Join(dir, knownFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Pinned("192.0.2.10"); err == nil {
			t.Fatalf("accepted %q", content)
		}
	}
}

func TestListNeedsAPinnedHostAndHardensSSH(t *testing.T) {
	s, f, dir := newStore(t)
	ctx := context.Background()
	if _, err := s.EnsureKey(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(ctx, "192.0.2.10"); !errors.Is(err, ErrNotPinned) || f.count("ssh") != 0 {
		t.Fatalf("unpinned host contacted: %v", err)
	}
	if err := s.Pin(ctx, "192.0.2.10", fpOf(keyB64(1))); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	f.ssh = Result{Stdout: []byte(hash + "\t12\t1700000000\tsnippets/vendor.yaml\n")}
	l, err := s.List(ctx, "192.0.2.10")
	if err != nil || len(l.Entries) != 1 || l.Entries[0].Path != "snippets/vendor.yaml" {
		t.Fatalf("list %+v %v", l, err)
	}
	var c call
	for _, x := range f.calls {
		if x.name == "ssh" {
			c = x
		}
	}
	args := strings.Join(c.args, " ")
	for _, want := range []string{"-F /dev/null", "StrictHostKeyChecking=yes", "UserKnownHostsFile=" + filepath.Join(dir, knownFile),
		"GlobalKnownHostsFile=/dev/null", "IdentityAgent=none", "BatchMode=yes", "ForwardAgent=no", "PermitLocalCommand=no",
		"-- pmgr-sync@192.0.2.10 pmgr-list"} {
		if !strings.Contains(args, want) {
			t.Fatalf("ssh args lack %q: %s", want, args)
		}
	}
	if !strings.HasSuffix(args, "-- pmgr-sync@192.0.2.10 pmgr-list") {
		t.Fatalf("destination or remote command not last: %s", args)
	}
	for _, e := range c.env {
		if strings.HasPrefix(e, "SSH_AUTH_SOCK") || strings.HasPrefix(e, "SSH_ASKPASS") {
			t.Fatalf("agent leaked into the environment: %q", e)
		}
	}
}

func TestListClassifiesFailures(t *testing.T) {
	s, f, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.EnsureKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(ctx, "192.0.2.10", fpOf(keyB64(1))); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		res  Result
		err  error
		want error
	}{
		{Result{Exit: 126, Stderr: []byte("pmgr-sync-gate: comando non consentito")}, nil, ErrGateDenied},
		{Result{Exit: 255, Stderr: []byte("@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@")}, nil, ErrHostKeyChanged},
		{Result{Exit: 255, Stderr: []byte("Host key verification failed.")}, nil, ErrHostKeyChanged},
		{Result{Exit: 255, Stderr: []byte("pmgr-sync@192.0.2.10: Permission denied (publickey).")}, nil, ErrAuthRefused},
		{Result{Exit: 255, Stderr: []byte("ssh: connect to host 192.0.2.10 port 22: Connection refused")}, nil, ErrUnreachable},
		{Result{Exit: 255, Stderr: []byte("something else")}, nil, ErrSSH},
		{Result{}, context.DeadlineExceeded, ErrUnreachable},
	}
	for i, c := range cases {
		f.ssh, f.sshErr = c.res, c.err
		if _, err := s.List(ctx, "192.0.2.10"); !errors.Is(err, c.want) {
			t.Fatalf("case %d: %v, want %v", i, err, c.want)
		}
		if err := errors.Unwrap(c.want); err != nil {
			t.Fatal("sentinel errors must not wrap the raw ssh text")
		}
	}
}

func TestParseListingDistrustsTheNode(t *testing.T) {
	h := strings.Repeat("b", 64)
	out := strings.Join([]string{
		h + "\t10\t1\tsnippets/a.yaml",
		h + "\t10\t1\tpmgr-scripts/bin/x.sh",
		"INVALID",
		h + "\t10\t1\t../etc/passwd",
		h + "\t10\t1\tsnippets/../../etc/shadow",
		h + "\t10\t1\t/etc/passwd",
		h + "\t10\t1\tother/a",
		"ZZZ\t10\t1\tsnippets/b",
		h + "\t-1\t1\tsnippets/c",
		h + "\t10\t1\tsnippets/d\textra",
		h + "\t10\t1\tsnippets/<script>",
		"TRUNCATED",
	}, "\n")
	l := ParseListing([]byte(out))
	if len(l.Entries) != 2 || !l.Truncated || l.Invalid != 9 {
		t.Fatalf("entries %d truncated %v invalid %d", len(l.Entries), l.Truncated, l.Invalid)
	}
	long := ParseListing([]byte(strings.Repeat("x", 10000)))
	if len(long.Entries) != 0 || long.Invalid == 0 {
		t.Fatalf("over-long line: %+v", long)
	}
	var b strings.Builder
	for i := 0; i < maxListLines+5; i++ {
		b.WriteString(h + "\t1\t1\tsnippets/f" + strings.Repeat("x", i%3) + "\n")
	}
	if capd := ParseListing([]byte(b.String())); len(capd.Entries) != maxListLines || !capd.Truncated {
		t.Fatalf("cap: %d %v", len(capd.Entries), capd.Truncated)
	}
}

func TestDigestIgnoresOrderAndDates(t *testing.T) {
	h1, h2 := strings.Repeat("1", 64), strings.Repeat("2", 64)
	a := ParseListing([]byte(h1 + "\t1\t100\tsnippets/a\n" + h2 + "\t1\t100\tsnippets/b\n"))
	b := ParseListing([]byte(h2 + "\t1\t999\tsnippets/b\n" + h1 + "\t1\t5\tsnippets/a\n"))
	c := ParseListing([]byte(h1 + "\t1\t100\tsnippets/a\n" + h1 + "\t1\t100\tsnippets/b\n"))
	if a.Digest() != b.Digest() || a.Digest() == c.Digest() {
		t.Fatal("digest must depend on content only")
	}
}

func TestCappedWriterDrains(t *testing.T) {
	c := &capped{max: 4}
	n, err := c.Write([]byte("abcdef"))
	if n != 6 || err != nil || c.buf.String() != "abcd" {
		t.Fatalf("%d %v %q", n, err, c.buf.String())
	}
	if _, err := execRunner(context.Background(), "sh", []string{"-c", "id"}, nil, 10); err == nil {
		t.Fatal("execRunner started a program outside the allowlist")
	}
}

// With the real ssh-keygen: the key it writes matches what EnsureKey expects.
func TestEnsureKeyWithRealSSHKeygen(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	s := New(filepath.Join(t.TempDir(), "ssh"))
	k, err := s.EnsureKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !keyLinePattern.MatchString(k.Line) || !strings.HasPrefix(k.Fingerprint, "SHA256:") {
		t.Fatalf("%+v", k)
	}
	out, err := exec.Command("ssh-keygen", "-lf", filepath.Join(s.dir, keyFile+".pub")).Output()
	if err != nil || !strings.Contains(string(out), k.Fingerprint) {
		t.Fatalf("fingerprint differs from ssh-keygen: %q vs %s", out, k.Fingerprint)
	}
}
