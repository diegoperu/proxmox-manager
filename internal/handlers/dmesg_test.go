package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeDmesgAPI: guest agent scripted by scriptedAgent plus canned status / osinfo answers.
type fakeDmesgAPI struct {
	*scriptedAgent
	status    string
	statusErr error
	osJSON    string
	osErr     error
}

func (f *fakeDmesgAPI) GetVMStatus(string, int) (json.RawMessage, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return json.RawMessage(fmt.Sprintf(`{"status":%q}`, f.status)), nil
}

func (f *fakeDmesgAPI) GetVMOSInfo(string, int) (json.RawMessage, error) {
	if f.osErr != nil {
		return nil, f.osErr
	}
	if f.osJSON == "" {
		return json.RawMessage(`{"result":{"id":"ubuntu"}}`), nil
	}
	return json.RawMessage(f.osJSON), nil
}

func newFake(replies ...guestResult) *fakeDmesgAPI {
	return &fakeDmesgAPI{scriptedAgent: &scriptedAgent{replies: replies}, status: "running"}
}

func fastPoll(t *testing.T) {
	t.Helper()
	old := guestPollInterval
	guestPollInterval = time.Millisecond
	t.Cleanup(func() { guestPollInterval = old })
}

func TestValidVMTarget(t *testing.T) {
	for _, ok := range [][2]string{{"pve1", "100"}, {"datacloud-node-10", "219"}, {"a.b-c", "999999999"}} {
		if _, err := validVMTarget(ok[0], ok[1]); err != nil {
			t.Errorf("%v dovrebbe essere valido: %v", ok, err)
		}
	}
	for _, bad := range [][2]string{{"", "100"}, {"-x", "100"}, {"a b", "100"}, {"a/b", "100"}, {"../x", "100"}, {"a;b", "100"},
		{"pve1", "99"}, {"pve1", "abc"}, {"pve1", "1000000000"}, {"pve1", "-5"}, {"pve1", ""}, {strings.Repeat("a", 64), "100"}} {
		if _, err := validVMTarget(bad[0], bad[1]); err == nil {
			t.Errorf("%v dovrebbe essere rifiutato", bad)
		}
	}
}

func TestSanitizeDmesg(t *testing.T) {
	t.Run("righe e controllo", func(t *testing.T) {
		got, trunc := sanitizeDmesg("a\r\nb\x00c\x07d\x1b[31mred\x1b[0m\te\n\n", 1<<20)
		want := []string{"a", "bcdred\te"}
		if trunc || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("got %q trunc=%v, atteso %q", got, trunc, want)
		}
	})
	t.Run("utf8 non valido", func(t *testing.T) {
		got, _ := sanitizeDmesg("ok \xff\xfe fine", 1<<20)
		if len(got) != 1 || !strings.Contains(got[0], "�") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("vuoto", func(t *testing.T) {
		got, _ := sanitizeDmesg("", 100)
		if got == nil || len(got) != 0 {
			t.Errorf("got %#v, atteso slice vuota non nil (JSON [])", got)
		}
	})
	t.Run("tiene la coda e dichiara il taglio", func(t *testing.T) {
		raw := "riga-vecchia-1\nriga-vecchia-2\nriga-recente-3\nriga-recente-4\n"
		got, trunc := sanitizeDmesg(raw, 35)
		if !trunc || got[len(got)-1] != "riga-recente-4" || strings.Contains(strings.Join(got, ""), "vecchia-1") {
			t.Errorf("got %q trunc=%v", got, trunc)
		}
	})
	t.Run("riga troppo lunga", func(t *testing.T) {
		got, trunc := sanitizeDmesg(strings.Repeat("x", dmesgMaxLineLen+50), 1<<20)
		if !trunc || len(got[0]) != dmesgMaxLineLen+3 || !strings.HasSuffix(got[0], "...") {
			t.Errorf("len=%d trunc=%v", len(got[0]), trunc)
		}
	})
}

func TestReadDmesgHappyPathInlineAndHelper(t *testing.T) {
	fastPoll(t)
	for _, c := range []struct {
		name  string
		probe string
		mode  string
	}{{"inline", "INLINE\n", "inline"}, {"helper", "HELPER\n", "helper"}} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(guestResult{Out: c.probe}, guestResult{Out: "[ 0.1] boot\n[ 0.2] eth0 up\n"})
			resp, status, err := readDmesg(f, "n1", 150, 300, "warn")
			if err != nil || status != http.StatusOK {
				t.Fatalf("status=%d err=%v", status, err)
			}
			if resp.Count != 2 || resp.Mode != c.mode || resp.Level != "warn" || resp.LinesRequested != 300 || resp.FetchedAt == 0 {
				t.Errorf("risposta inattesa: %+v", resp)
			}
			// i due valori della richiesta arrivano come argomenti posizionali finali, mai dentro il testo dello script
			last := f.argvs[1]
			if got := last[len(last)-2:]; fmt.Sprint(got) != "[300 warn]" {
				t.Errorf("argomenti = %v", got)
			}
			if c.mode == "helper" && last[0] != guestHelperDir+"/dmesg.sh" {
				t.Errorf("helper non usato: %v", last)
			}
		})
	}
}

func TestReadDmesgRefusals(t *testing.T) {
	fastPoll(t)
	t.Run("VM ferma", func(t *testing.T) {
		f := newFake()
		f.status = "stopped"
		_, status, err := readDmesg(f, "n", 150, 100, "all")
		if status != http.StatusConflict || err == nil || len(f.argvs) != 0 {
			t.Errorf("status=%d err=%v exec=%d", status, err, len(f.argvs))
		}
	})
	t.Run("stato non leggibile", func(t *testing.T) {
		f := newFake()
		f.statusErr = errors.New("proxmox 500: dettaglio interno")
		_, status, err := readDmesg(f, "n", 150, 100, "all")
		if status != http.StatusBadGateway || err == nil || strings.Contains(err.Error(), "dettaglio interno") {
			t.Errorf("status=%d err=%v (non deve esporre il dettaglio)", status, err)
		}
	})
	t.Run("Windows", func(t *testing.T) {
		f := newFake()
		f.osJSON = `{"result":{"id":"mswindows"}}`
		_, status, err := readDmesg(f, "n", 150, 100, "all")
		if status != http.StatusConflict || err == nil || len(f.argvs) != 0 {
			t.Errorf("status=%d err=%v exec=%d", status, err, len(f.argvs))
		}
	})
	t.Run("agent non attivo", func(t *testing.T) {
		f := newFake()
		f.osErr = errors.New("QEMU guest agent is not running")
		_, status, err := readDmesg(f, "n", 150, 100, "all")
		if status != http.StatusBadGateway || err == nil || !strings.Contains(err.Error(), "agent") {
			t.Errorf("status=%d err=%v", status, err)
		}
	})
	t.Run("dmesg non leggibile: suggerisce l'helper solo se inline", func(t *testing.T) {
		inline := newFake(guestResult{Out: "INLINE\n"}, guestResult{ExitCode: 3, Err: "dmesg not readable"})
		_, status, err := readDmesg(inline, "n", 150, 100, "all")
		if status != http.StatusBadGateway || err == nil || !strings.Contains(err.Error(), "dmesg.sh") {
			t.Errorf("inline: status=%d err=%v", status, err)
		}
		helper := newFake(guestResult{Out: "HELPER\n"}, guestResult{ExitCode: 3, Err: "dmesg not readable"})
		_, _, err = readDmesg(helper, "n", 150, 100, "all")
		if err == nil || strings.Contains(err.Error(), "dmesg.sh") {
			t.Errorf("helper: err=%v", err)
		}
	})
	t.Run("codice di uscita inatteso", func(t *testing.T) {
		f := newFake(guestResult{Out: "INLINE\n"}, guestResult{ExitCode: 7, Err: "boom"})
		_, status, err := readDmesg(f, "n", 150, 100, "all")
		if status != http.StatusBadGateway || err == nil || strings.Contains(err.Error(), "boom") {
			t.Errorf("status=%d err=%v", status, err)
		}
	})
}

func TestDmesgInFlightGuard(t *testing.T) {
	if !dmesgAcquire("k") {
		t.Fatal("prima acquisizione rifiutata")
	}
	if dmesgAcquire("k") {
		t.Error("seconda lettura sulla stessa VM accettata")
	}
	if !dmesgAcquire("altro") {
		t.Error("una VM diversa non deve essere bloccata")
	}
	dmesgRelease("k")
	if !dmesgAcquire("k") {
		t.Error("dopo il rilascio deve essere di nuovo possibile")
	}
	dmesgRelease("k")
	dmesgRelease("altro")
}

func TestDmesgWhitelists(t *testing.T) {
	for _, n := range []int{100, 300, 1000} {
		if !dmesgLinesAllowed[n] {
			t.Errorf("%d dovrebbe essere ammesso", n)
		}
	}
	for _, n := range []int{0, 1, 99, 301, 5000, -1} {
		if dmesgLinesAllowed[n] {
			t.Errorf("%d non dovrebbe essere ammesso", n)
		}
	}
	for _, l := range []string{"all", "warn"} {
		if !dmesgLevelAllowed[l] {
			t.Errorf("%q dovrebbe essere ammesso", l)
		}
	}
	for _, l := range []string{"", "ALL", "err", "all;rm -rf /", "warn "} {
		if dmesgLevelAllowed[l] {
			t.Errorf("%q non dovrebbe essere ammesso", l)
		}
	}
}

func TestNormalizeOSInfo(t *testing.T) {
	got := normalizeOSInfo(json.RawMessage(`{"result":{"id":"rocky","name":"Rocky Linux","pretty-name":"Rocky Linux 8.10 (Green Obsidian)","version":"8.10 (Green Obsidian)","version-id":"8.10","kernel-release":"4.18.0","machine":"x86_64","extra":"ignorato","bad":5}}`))
	want := map[string]string{"id": "rocky", "name": "Rocky Linux", "pretty_name": "Rocky Linux 8.10 (Green Obsidian)",
		"version": "8.10 (Green Obsidian)", "version_id": "8.10", "kernel_release": "4.18.0", "machine": "x86_64"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v", got)
	}
	for _, bad := range []string{`not json`, `{}`, `{"result":null}`, `{"result":{"id":5}}`} {
		if g := normalizeOSInfo(json.RawMessage(bad)); len(g) != 0 {
			t.Errorf("%q -> %v, atteso vuoto", bad, g)
		}
	}
}
