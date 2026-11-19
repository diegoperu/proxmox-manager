package handlers

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGPUUseScript(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	const fake = `#!/bin/sh
printf '%s\n' "$@" > "$STUB_LOG"
if [ "${STUB_FAIL:-}" = 1 ]; then printf '%s\n' 'Failed to initialize NVML: Driver/library version mismatch' 'second line' >&2; exit 1; fi
printf 'first line\nsecond line\n\n'
`
	for _, tc := range []struct {
		name                        string
		present, fail, arg, timeout bool
		code                        int
	}{
		{name: "success", present: true, code: 0},
		{name: "timeout wrapper", present: true, timeout: true, code: 0},
		{name: "timeout failure", present: true, timeout: true, fail: true, code: 4},
		{name: "absent", code: 3},
		{name: "failure", present: true, fail: true, code: 4},
		{name: "argument", present: true, arg: true, code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			work := filepath.Join(dir, "work")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.present {
				if err := os.WriteFile(filepath.Join(bin, "nvidia-smi"), []byte(fake), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Failure formatting uses head and cut; expose only these commands.
			for _, name := range []string{"head", "cut"} {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Skipf("%s unavailable", name)
				}
				if err = os.Symlink(path, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.timeout {
				timeoutScript := "#!/bin/sh\n[ \"$1\" = 15 ] || exit 99\nshift\nexec \"$@\"\n"
				if tc.name == "timeout failure" {
					timeoutScript = "#!/bin/sh\nprintf 'timed out\\n' >&2\nexit 124\n"
				}
				if err := os.WriteFile(filepath.Join(bin, "timeout"), []byte(timeoutScript), 0700); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"-c", gpuUseScriptSrc, "gpuuse"}
			if tc.arg {
				args = append(args, "bad")
			}
			cmd := exec.Command("/bin/sh", args...)
			cmd.Dir = work
			log := filepath.Join(dir, "calls")
			cmd.Env = []string{"PATH=" + bin, "HOME=" + work, "STUB_LOG=" + log}
			if tc.fail {
				cmd.Env = append(cmd.Env, "STUB_FAIL=1")
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			code := 0
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if tc.code == 0 && stdout.String() != "first line\nsecond line\n" {
				t.Errorf("stdout=%q", stdout.String())
			}
			if tc.name == "failure" && (!strings.Contains(stderr.String(), "Failed to initialize NVML: Driver/library version mismatch") || strings.Count(stderr.String(), "\n") != 1) {
				t.Errorf("stderr=%q", stderr.String())
			}
			if tc.code == 3 && !strings.Contains(stderr.String(), "nvidia-smi") {
				t.Errorf("stderr=%q", stderr.String())
			}
			if tc.present && !tc.arg && tc.name != "timeout failure" {
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
				want := []string{"--query-gpu=pci.bus_id,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw", "--format=csv,noheader,nounits"}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("args=%q", got)
				}
			}
			entries, err := os.ReadDir(work)
			if err != nil || len(entries) != 0 {
				t.Errorf("work dir=%v err=%v", entries, err)
			}
		})
	}
}
