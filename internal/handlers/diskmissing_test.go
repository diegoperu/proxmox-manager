package handlers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiskMissingMessage(t *testing.T) {
	tests := []struct {
		input string
		want  []string
		ok    bool
	}{
		{"MISSING parted\n", []string{"comando parted", "apt install parted / dnf install parted"}, true},
		{"MISSING mount\nMISSING blkid\nMISSING awk\n", []string{"apt install mount", "dnf install util-linux", "comando blkid", "apt install gawk"}, true},
		{"MISSING bogus\nMISSING wc\n", []string{"comando wc"}, true},
		{"MISSING parted; touch /tmp/x\nMISSING blkid extra\n", nil, false},
		{"", nil, false},
	}
	for _, tt := range tests {
		got, ok := diskMissingMessage(tt.input)
		if ok != tt.ok {
			t.Fatalf("%q: %q %v", tt.input, got, ok)
		}
		for _, part := range tt.want {
			if !strings.Contains(got, part) {
				t.Errorf("%q missing %q", got, part)
			}
		}
		if strings.Contains(got, "bogus") || strings.Contains(got, "touch") {
			t.Errorf("unsafe output: %q", got)
		}
	}
}

func TestDiskScriptChecksCommandsBeforeRunning(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	dir := t.TempDir()
	if err := os.Symlink(sh, filepath.Join(dir, "sh")); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "marker")
	fake := "#!/bin/sh\nprintf ran > \"$MARKER\"\n"
	if err := os.WriteFile(filepath.Join(dir, "lsblk"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args    []string
		missing string
	}{
		{[]string{"setup", "/dev/null-ish", "/unused", "0777"}, "MISSING parted"},
		{[]string{"find-blank"}, "MISSING blkid"},
	} {
		cmd := exec.Command(sh, append([]string{"guestscripts/disk.sh"}, tc.args...)...)
		cmd.Env = []string{"PATH=" + dir, "MARKER=" + marker}
		output, err := cmd.CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 4 || !strings.Contains(string(output), tc.missing) {
			t.Fatalf("%v: %q %v", tc.args, output, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("command ran: %v", err)
		}
	}
}

func TestProvisionMissingDiskCommandNote(t *testing.T) {
	provFast(t)
	for _, phase := range []string{"find", "setup"} {
		f := newFakeProv()
		f.diskErr = "MISSING parted\n"
		if phase == "find" {
			f.findExit = 4
		} else {
			f.setupExit = 4
		}
		r := withDisk(baseReq(), 10)
		j, _ := startProv(f, 20, "C", mustPlanProv(t, r), noOpts, time.Now())
		v := waitProv(t, j)
		found := false
		for _, step := range v.Steps {
			if step.Phase == provPhMount && strings.Contains(step.Note, "apt install parted") {
				found = true
			}
		}
		if !found || len(v.Warnings) != 1 || v.Warnings[0] != "mount_failed" {
			t.Fatalf("%s: %+v", phase, v)
		}
	}
}
