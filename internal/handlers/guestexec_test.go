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
		res, err := runGuestScript(f, "n", 1, addUserScriptSrc, []string{"tosi", "LAKEY"}, time.Second)
		if err != nil {
			t.Fatalf("errore inatteso: %v", err)
		}
		if res.ExitCode != 0 || res.Out != "OK\n" {
			t.Errorf("risultato inatteso: %+v", res)
		}
		want := []string{"/bin/sh", "-c", addUserScriptSrc, "pmgr", "tosi", "LAKEY"}
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

// scriptedAgent risponde a ogni exec con la prossima risposta in coda e registra gli argv.
type scriptedAgent struct {
	replies []guestResult
	execErr error
	argvs   [][]string
	next    int
}

func (s *scriptedAgent) AgentExec(node string, vmid int, args []string) (json.RawMessage, error) {
	if s.execErr != nil {
		return nil, s.execErr
	}
	s.argvs = append(s.argvs, args)
	return json.Marshal(map[string]int{"pid": 100 + len(s.argvs)})
}

func (s *scriptedAgent) AgentExecStatus(node string, vmid, pid int) (json.RawMessage, error) {
	idx := pid - 101
	var r guestResult
	if idx < len(s.replies) {
		r = s.replies[idx]
	}
	return json.Marshal(map[string]interface{}{"exited": 1, "exitcode": r.ExitCode, "out-data": r.Out, "err-data": r.Err})
}

func TestGuestToolProbeAndRun(t *testing.T) {
	old := guestPollInterval
	guestPollInterval = time.Millisecond
	defer func() { guestPollInterval = old }()

	tool := guestTool{name: "adduser.sh", inline: "INLINE-TEXT"}

	t.Run("helper presente", func(t *testing.T) {
		a := &scriptedAgent{replies: []guestResult{{Out: "HELPER\n"}, {Out: "OK\n"}}}
		use, err := tool.probeHelper(a, "n", 1)
		if err != nil || !use {
			t.Fatalf("probe: use=%v err=%v, atteso true", use, err)
		}
		// il percorso del probe è una costante, passata come argomento $1 (mai nel testo)
		if got := a.argvs[0][len(a.argvs[0])-1]; got != guestHelperDir+"/adduser.sh" {
			t.Errorf("probe argomento = %q", got)
		}
		if _, err := tool.run(a, "n", 1, use, []string{"tosi", "KEY"}, time.Second); err != nil {
			t.Fatal(err)
		}
		// helper: exec diretto del file con gli argomenti, NESSUNA shell in mezzo
		want := []string{guestHelperDir + "/adduser.sh", "tosi", "KEY"}
		if fmt.Sprint(a.argvs[1]) != fmt.Sprint(want) {
			t.Errorf("argv helper = %v, atteso %v", a.argvs[1], want)
		}
	})

	t.Run("helper assente → inline", func(t *testing.T) {
		a := &scriptedAgent{replies: []guestResult{{Out: "INLINE\n"}, {Out: "OK\n"}}}
		use, err := tool.probeHelper(a, "n", 1)
		if err != nil || use {
			t.Fatalf("probe: use=%v err=%v, atteso false", use, err)
		}
		if _, err := tool.run(a, "n", 1, use, []string{"tosi", ""}, time.Second); err != nil {
			t.Fatal(err)
		}
		want := []string{"/bin/sh", "-c", "INLINE-TEXT", "pmgr", "tosi", ""}
		if fmt.Sprint(a.argvs[1]) != fmt.Sprint(want) {
			t.Errorf("argv inline = %v, atteso %v", a.argvs[1], want)
		}
	})

	t.Run("probe con output inatteso → inline (mai helper per sbaglio)", func(t *testing.T) {
		for _, out := range []string{"", "helper", "HELPER extra", "xx"} {
			a := &scriptedAgent{replies: []guestResult{{Out: out}}}
			if use, err := tool.probeHelper(a, "n", 1); err != nil || use {
				t.Errorf("out=%q: use=%v err=%v, atteso inline", out, use, err)
			}
		}
	})

	t.Run("errore dell'agent nel probe viene propagato", func(t *testing.T) {
		a := &scriptedAgent{execErr: errors.New("proxmox 500: QEMU guest agent is in frozen state")}
		if _, err := tool.probeHelper(a, "n", 1); err == nil || !strings.Contains(err.Error(), "frozen") {
			t.Errorf("atteso errore frozen, got %v", err)
		}
	})
}

// Gli script embedded sono la sorgente unica di helper e fallback inline: devono
// esistere, essere script veri e non contenere segnaposto né dati di richiesta.
func TestEmbeddedGuestScripts(t *testing.T) {
	for name, src := range map[string]string{"adduser.sh": addUserScriptSrc, "disk.sh": diskScriptSrc} {
		if !strings.HasPrefix(src, "#!/bin/sh\n") {
			t.Errorf("%s: manca lo shebang #!/bin/sh", name)
		}
		if strings.Contains(src, "TODO") || strings.Contains(src, "FIXME") || strings.Contains(src, "%s\n\"") {
			t.Errorf("%s: contiene un segnaposto", name)
		}
		if !strings.Contains(src, "set -eu") {
			t.Errorf("%s: manca `set -eu`", name)
		}
	}
	// adduser.sh: nessuna interpolazione di dati nel testo, solo parametri posizionali
	for _, must := range []string{`U="${1:?`, `KEY="${2:-}"`, "exit 3", "usermod -L"} {
		if !strings.Contains(addUserScriptSrc, must) {
			t.Errorf("adduser.sh: manca %q", must)
		}
	}
	// nessuna riga che mandi una variabile della richiesta a una shell/eval
	for _, bad := range []string{"eval ", "sh -c \"$", "bash -c"} {
		if strings.Contains(addUserScriptSrc, bad) || strings.Contains(diskScriptSrc, bad) {
			t.Errorf("script embedded contiene il pattern pericoloso %q", bad)
		}
	}
	for _, cmd := range []string{"find-blank)", "rescan)", "setup)"} {
		if !strings.Contains(diskScriptSrc, cmd) {
			t.Errorf("disk.sh: manca il subcommand %q", cmd)
		}
	}
}

func TestAgentErrorMessages(t *testing.T) {
	cases := []struct{ in, want string }{
		{"proxmox 500: QEMU guest agent is in frozen state", "frozen"},
		{"proxmox 500: command is disabled / has been disabled", "disabilitato"},
		{"proxmox 500: QEMU guest agent is not running", "non attivo"},
		{`proxmox 500: {"message":"VM 134 qga command 'guest-exec' failed - got timeout\n"}`, "SELinux"},
		{"dial tcp: connection refused", "non raggiungibile"},
	}
	for _, c := range cases {
		got := agentError(errors.New(c.in)).Error()
		if !strings.Contains(got, c.want) {
			t.Errorf("agentError(%q) = %q, atteso che contenga %q", c.in, got, c.want)
		}
		// il dettaglio grezzo di Proxmox non deve arrivare all'utente
		if strings.Contains(got, "proxmox 500") || strings.Contains(got, "qga command") {
			t.Errorf("agentError(%q) espone il dettaglio interno: %q", c.in, got)
		}
	}
}

// Proxmox tronca il messaggio guest-exec di (byte - caratteri) e l'agent resta bloccato:
// verificato su Ubuntu 26.04 (1 byte extra passa, 2 o più no). Gli script embedded e
// ogni argomento verso il guest devono quindi essere ASCII.
func TestGuestScriptsAreASCII(t *testing.T) {
	for name, src := range map[string]string{"adduser.sh": addUserScriptSrc, "disk.sh": diskScriptSrc} {
		if !isASCII(src) {
			t.Errorf("%s contiene caratteri non ASCII o di controllo: l'esecuzione inline blocca il guest agent", name)
		}
	}
}

func TestRunGuestArgvRejectsNonASCII(t *testing.T) {
	for _, arg := range []string{"già", "chiave \u2014 commento", "a\x00b", "a\x7fb"} {
		f := &fakeAgent{exitedAfter: 1}
		_, err := runGuestArgv(f, "n", 1, []string{"/bin/sh", "-c", "echo ok", "pmgr", arg}, time.Second)
		if !errors.Is(err, errNonASCIIArg) {
			t.Errorf("arg %q: atteso errNonASCIIArg, got %v", arg, err)
		}
		if f.gotArgs != nil {
			t.Errorf("arg %q: il comando non doveva essere inviato al guest", arg)
		}
	}
	f := &fakeAgent{exitedAfter: 1}
	if _, err := runGuestArgv(f, "n", 1, []string{"/bin/sh", "-c", "echo ok\n\t", "pmgr", "tosi"}, time.Second); err != nil {
		t.Errorf("argomenti ASCII rifiutati: %v", err)
	}
}
