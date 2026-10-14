package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNormalizeSSHKey(t *testing.T) {
	ed := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBgRc00K614Aa4ttUvcyjOowy06cdpoTErYh9a79FxiG utente@host"
	valid := []string{
		ed,
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQAB",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYQ== commento con spazi",
		"sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5",
		"  " + ed + "  ", // spazi esterni tollerati
	}
	for _, k := range valid {
		got, err := normalizeSSHKey(k)
		if err != nil {
			t.Errorf("chiave valida rifiutata %q: %v", k, err)
		}
		if strings.ContainsAny(got, "\r\n") || got != strings.TrimSpace(k) {
			t.Errorf("normalizeSSHKey(%q) = %q, atteso trim senza newline", k, got)
		}
	}

	if got, err := normalizeSSHKey(""); err != nil || got != "" {
		t.Errorf("chiave vuota: got %q err %v, atteso \"\",nil", got, err)
	}
	if got, err := normalizeSSHKey("   "); err != nil || got != "" {
		t.Errorf("solo spazi: got %q err %v, atteso \"\",nil", got, err)
	}

	// Ogni caso qui NON deve passare: sono i vettori di iniezione e i formati sbagliati.
	bad := map[string]string{
		"iniezione apice singolo": "ssh-ed25519 AAAA'; touch /tmp/x; echo '",
		"iniezione con newline":   "ssh-ed25519 AAAA\ntouch /tmp/x",
		"iniezione con CR":        "ssh-ed25519 AAAA\rtouch /tmp/x",
		"tipo non ammesso (dss)":  "ssh-dss AAAAB3Nza",
		"prefisso sk generico":    "sk-qualcosa AAAA",
		"testo libero":            "foo bar",
		"solo tipo":               "ssh-ed25519",
		"caratteri non base64":    "ssh-ed25519 AAAA$()",
		"commento con newline":    "ssh-ed25519 AAAA commento\naltro",
	}
	for name, k := range bad {
		if _, err := normalizeSSHKey(k); err == nil {
			t.Errorf("%s: chiave %q accettata, doveva essere rifiutata", name, k)
		}
	}

	// Chiave oltre il limite di lunghezza.
	huge := "ssh-rsa " + strings.Repeat("A", maxSSHKeyLen)
	if _, err := normalizeSSHKey(huge); err == nil {
		t.Error("chiave oltre maxSSHKeyLen accettata")
	}
}

func TestValidateDiskSlot(t *testing.T) {
	ok := []string{"scsi0", "scsi30", "virtio0", "virtio15", "sata0", "sata5", "scsi9", "scsi10"}
	for _, s := range ok {
		if err := validateDiskSlot(s); err != nil {
			t.Errorf("slot valido rifiutato %q: %v", s, err)
		}
	}
	// sata6/virtio16 ora rifiutati (prima passavano e innescavano un riavvio VM); più injection.
	bad := []string{"sata6", "virtio16", "scsi31", "foo", "scsi99", "scsi1;ls", "scsi-1", "SCSI1", "", "scsi1\n", "scsi01", "scsi 1"}
	for _, s := range bad {
		if err := validateDiskSlot(s); err == nil {
			t.Errorf("slot non valido accettato %q", s)
		}
	}
}

func TestValidateMountPoint(t *testing.T) {
	ok := []string{"/data", "/mnt/dati", "/srv/export", "/data2"}
	for _, m := range ok {
		if err := validateMountPoint(m); err != nil {
			t.Errorf("mount valido rifiutato %q: %v", m, err)
		}
	}
	// /etc/ (slash finale) e i sottopercorsi di sistema ora bloccati; più injection/traversal.
	bad := []string{
		"/", "/etc", "/etc/", "/etc/ssh", "/var/log", "/home/tizio", "/boot/efi",
		"/usr/local", "/root", "/proc", "/sys", "/dev", "/run/x", "/tmp/x",
		"/data/", "/data//x", "/data/../etc", "..", "relativo", "/a b", "/x;y", "",
		"/" + strings.Repeat("a", 65),
	}
	for _, m := range bad {
		if err := validateMountPoint(m); err == nil {
			t.Errorf("mount non permesso accettato %q", m)
		}
	}
}

// fakeAgent simula il guest agent: AgentExec restituisce un pid, AgentExecStatus
// restituisce lo stato dopo `exitedAfter` chiamate. Registra gli argomenti dell'exec.
type fakeAgent struct {
	execErr      error
	gotArgs      []string
	pid          int
	exitedAfter  int
	calls        int
	exitCode     int
	out, errData string
	statusErr    error
	neverExits   bool
}

func (f *fakeAgent) AgentExec(node string, vmid int, args []string) (json.RawMessage, error) {
	if f.execErr != nil {
		return nil, f.execErr
	}
	f.gotArgs = args
	if f.pid == 0 {
		f.pid = 4242
	}
	return json.Marshal(map[string]int{"pid": f.pid})
}

func (f *fakeAgent) AgentExecStatus(node string, vmid, pid int) (json.RawMessage, error) {
	f.calls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	exited := 0
	if !f.neverExits && f.calls >= f.exitedAfter {
		exited = 1
	}
	return json.Marshal(map[string]interface{}{
		"exited": exited, "exitcode": f.exitCode, "out-data": f.out, "err-data": f.errData,
	})
}

func TestRunGuestScript(t *testing.T) {
	old := guestPollInterval
	guestPollInterval = time.Millisecond
	defer func() { guestPollInterval = old }()

	t.Run("successo e passaggio argomenti", func(t *testing.T) {
		f := &fakeAgent{exitedAfter: 2, exitCode: 0, out: "OK\n"}
		res, err := runGuestScript(f, "n", 1, addUserScript, []string{"tosi", "LAKEY"}, time.Second)
		if err != nil {
			t.Fatalf("errore inatteso: %v", err)
		}
		if res.ExitCode != 0 || res.Out != "OK\n" {
			t.Errorf("risultato inatteso: %+v", res)
		}
		want := []string{"/bin/sh", "-c", addUserScript, "pmgr", "tosi", "LAKEY"}
		if fmt.Sprint(f.gotArgs) != fmt.Sprint(want) {
			t.Errorf("args = %v, atteso %v", f.gotArgs, want)
		}
	})

	t.Run("exit code propagato", func(t *testing.T) {
		f := &fakeAgent{exitedAfter: 1, exitCode: 3, errData: "esiste già"}
		res, err := runGuestScript(f, "n", 1, "x", nil, time.Second)
		if err != nil || res.ExitCode != 3 || res.Err != "esiste già" {
			t.Errorf("got %+v err %v", res, err)
		}
	})

	t.Run("timeout non è un successo", func(t *testing.T) {
		f := &fakeAgent{neverExits: true}
		_, err := runGuestScript(f, "n", 1, "x", nil, 5*time.Millisecond)
		if !errors.Is(err, errGuestTimeout) {
			t.Errorf("atteso errGuestTimeout, got %v", err)
		}
	})

	t.Run("errore frozen tradotto", func(t *testing.T) {
		f := &fakeAgent{execErr: errors.New("proxmox 500: QEMU guest agent is in frozen state")}
		_, err := runGuestScript(f, "n", 1, "x", nil, time.Second)
		if err == nil || !strings.Contains(err.Error(), "frozen") {
			t.Errorf("atteso messaggio frozen, got %v", err)
		}
	})

	t.Run("pid mancante è errore", func(t *testing.T) {
		f := &fakeAgent{pid: -1} // Marshal darà pid 0 → trattato come risposta inattesa
		f.pid = 0
		// forziamo pid 0 nella risposta
		fe := &fakeAgentZeroPID{}
		_, err := runGuestScript(fe, "n", 1, "x", nil, time.Second)
		if err == nil || !strings.Contains(err.Error(), "inattesa") {
			t.Errorf("atteso errore risposta inattesa, got %v", err)
		}
		_ = f
	})
}

type fakeAgentZeroPID struct{}

func (fakeAgentZeroPID) AgentExec(string, int, []string) (json.RawMessage, error) {
	return json.Marshal(map[string]int{"pid": 0})
}
func (fakeAgentZeroPID) AgentExecStatus(string, int, int) (json.RawMessage, error) {
	return json.Marshal(map[string]int{})
}
