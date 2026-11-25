package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The repository is public: the template tooling must not carry internal addresses, host names or domains.
var internalRef = regexp.MustCompile(`10\.75\.|10\.248\.|131\.175\.|polimi|datacloud-|inside\.dei|tecnico_strong`)

func TestPmgrScriptsHaveNoInternalReferences(t *testing.T) {
	root := filepath.Join("..", "..", "tools", "pmgr-scripts")
	checked := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		checked++
		if m := internalRef.Find(b); m != nil {
			t.Errorf("%s contains an internal reference %q", p, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Fatalf("only %d files checked: tools/pmgr-scripts missing?", checked)
	}
}

// The helpers the manager runs inline and the ones installed in the templates must be the same text.
func TestPmgrScriptsHelpersMatchGuestScripts(t *testing.T) {
	for _, name := range []string{"adduser.sh", "disk.sh", "dmesg.sh"} {
		a, err := os.ReadFile(filepath.Join("..", "..", "tools", "pmgr-scripts", "helpers", name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "handlers", "guestscripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("helpers/%s differs from internal/handlers/guestscripts/%s", name, name)
		}
	}
}
