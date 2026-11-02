package handlers

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Behavioural test of guestscripts/dmesg.sh: the real script runs under sh with a fake `dmesg` in
// front of PATH that records every argument it receives. What is checked is what the script DOES
// (which flags reach dmesg, exit codes, what reaches stderr), not what its text looks like.
const fakeDmesg = `#!/bin/sh
echo "$*" >> "$STUB_LOG"
if [ "${STUB_FAIL:-}" = 1 ]; then echo "first failure line" >&2; echo "second line with the buffer content" >&2; exit 1; fi
if [ "${STUB_NOLEVEL:-}" = 1 ]; then case " $* " in *" --level="*) echo unsupported >&2; exit 1;; esac; fi
if [ "${STUB_NOISO:-}" = 1 ]; then case " $* " in *" --time-format=iso "*) echo unsupported >&2; exit 1;; esac; fi
i=1; while [ $i -le 1500 ]; do echo "line $i"; i=$((i+1)); done
`

type scriptRun struct {
	out, err string
	code     int
	flags    []string // every argument the fake dmesg received
}

func runDmesgScript(t *testing.T, env []string, args ...string) scriptRun {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "dmesg.sh")
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(dmesgScriptSrc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "dmesg"), []byte(fakeDmesg), 0o700); err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(dir, "calls.log")
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "STUB_LOG=" + logf}, env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("sh non eseguibile: %v", err)
	}
	var flags []string
	if b, err := os.ReadFile(logf); err == nil {
		flags = strings.Fields(string(b))
	}
	return scriptRun{out: so.String(), err: se.String(), code: code, flags: flags}
}

func TestDmesgScriptBehaviour(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh non disponibile")
	}
	allowed := map[string]bool{"--time-format=iso": true, "-T": true, "--level=emerg,alert,crit,err,warn": true}
	onlyAllowed := func(t *testing.T, r scriptRun) {
		t.Helper()
		for _, f := range r.flags {
			if !allowed[f] {
				t.Errorf("il dmesg del guest ha ricevuto un'opzione non prevista %q (tutte: %v)", f, r.flags)
			}
		}
	}

	t.Run("300 all: tiene le ultime 300 righe, nessun filtro", func(t *testing.T) {
		r := runDmesgScript(t, nil, "300", "all")
		lines := strings.Split(strings.TrimRight(r.out, "\n"), "\n")
		if r.code != 0 || len(lines) != 300 || lines[299] != "line 1500" || lines[0] != "line 1201" {
			t.Errorf("code=%d righe=%d prima=%q ultima=%q", r.code, len(lines), lines[0], lines[len(lines)-1])
		}
		onlyAllowed(t, r)
		for _, f := range r.flags {
			if strings.HasPrefix(f, "--level") {
				t.Errorf("livello 'all' non deve passare --level: %v", r.flags)
			}
		}
	})
	t.Run("100 warn: filtro di livello, 100 righe", func(t *testing.T) {
		r := runDmesgScript(t, nil, "100", "warn")
		if r.code != 0 || len(strings.Split(strings.TrimRight(r.out, "\n"), "\n")) != 100 {
			t.Errorf("code=%d", r.code)
		}
		onlyAllowed(t, r)
		if !strings.Contains(strings.Join(r.flags, " "), "--level=emerg,alert,crit,err,warn") {
			t.Errorf("filtro non applicato: %v", r.flags)
		}
	})
	t.Run("argomenti non ammessi: exit 2 e dmesg mai chiamato con dati della richiesta", func(t *testing.T) {
		for _, a := range [][]string{{}, {"300"}, {"50", "all"}, {"300", "err"}, {"300;ls", "all"}, {"300", "all;ls"}, {"-c", "all"}, {"300", "--clear"}, {"3000", "all"}, {"", ""}} {
			r := runDmesgScript(t, nil, a...)
			if r.code != 2 {
				t.Errorf("args %q: exit %d, atteso 2", a, r.code)
			}
			if len(r.flags) != 0 {
				t.Errorf("args %q: dmesg e' stato chiamato (%v)", a, r.flags)
			}
		}
	})
	t.Run("dmesg non leggibile: exit 3 e il buffer non passa dal canale di errore", func(t *testing.T) {
		r := runDmesgScript(t, []string{"STUB_FAIL=1"}, "100", "all")
		if r.code != 3 || !strings.Contains(r.err, "first failure line") || strings.Contains(r.err, "buffer content") {
			t.Errorf("code=%d stderr=%q", r.code, r.err)
		}
	})
	t.Run("filtro di livello non supportato: nessun errore, avviso su stderr, log non filtrato", func(t *testing.T) {
		r := runDmesgScript(t, []string{"STUB_NOLEVEL=1"}, "100", "warn")
		if r.code != 0 || !strings.Contains(r.err, "level filter not supported") || !strings.HasPrefix(r.out, "line ") {
			t.Errorf("code=%d stderr=%q", r.code, r.err)
		}
	})
	t.Run("senza --time-format=iso ripiega su -T", func(t *testing.T) {
		r := runDmesgScript(t, []string{"STUB_NOISO=1"}, "100", "all")
		if r.code != 0 || !strings.Contains(strings.Join(r.flags, " "), "-T") {
			t.Errorf("code=%d flags=%v", r.code, r.flags)
		}
		onlyAllowed(t, r)
	})
}
