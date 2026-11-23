package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The command line creates, lists, resets, disables and re-roles accounts in the files of the working
// directory, never accepts a password as an argument and protects the last administrator.
func TestUserCommandLine(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	os.WriteFile("config.json", []byte(`{"clusters":[],"db_path":"t.db"}`), 0o600)

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runUserCommand(args, &out)
		return out.String(), err
	}
	out, err := run("add", "admin1", "--role", "admin")
	if err != nil || !strings.Contains(out, "password: ") || !strings.Contains(out, "otpauth://totp/ProxmoxManager:admin1?") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if info, err := os.Stat(filepath.Join(dir, "auth.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("auth.key: %v", err)
	}
	if _, err := run("add", "admin1", "--role", "admin"); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, bad := range [][]string{{"add", "x", "--role", "boss"}, {"add", "Bad Name", "--role", "reader"}, {"add", "u", "--role"}, {"nope"}, {}, {"add", "pippo", "--password", "segreta"}} {
		if _, err := run(bad...); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if _, err := run("disable", "admin1"); err == nil {
		t.Fatal("last admin disabled")
	}
	if _, err := run("add", "op1", "--role", "operator"); err != nil {
		t.Fatal(err)
	}
	if out, err := run("role", "op1", "reader"); err != nil || !strings.Contains(out, "reader") {
		t.Fatalf("role: %v %s", err, out)
	}
	if _, err := run("disable", "op1"); err != nil {
		t.Fatal(err)
	}
	out, err = run("list")
	if err != nil || !strings.Contains(out, "admin1") || !strings.Contains(out, "op1") || !strings.Contains(out, "disattivato") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, err := run("reset", "op1"); err != nil || !strings.Contains(out, "password: ") {
		t.Fatalf("reset: %v", err)
	}
	if _, err := run("reset", "ghost"); err == nil {
		t.Fatal("reset of unknown user")
	}
}
