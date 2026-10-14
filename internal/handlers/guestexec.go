package handlers

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// agentExecer is the part of the Proxmox client used to run commands through the
// QEMU guest agent. An interface so the polling logic can be tested without Proxmox.
type agentExecer interface {
	AgentExec(node string, vmid int, args []string) (json.RawMessage, error)
	AgentExecStatus(node string, vmid, pid int) (json.RawMessage, error)
}

type guestResult struct {
	Out      string
	Err      string
	ExitCode int
}

// errGuestTimeout: the command did not finish in time. It may still be running inside
// the guest, so it must never be reported as a success.
var errGuestTimeout = errors.New("timeout: il comando non è terminato nel tempo previsto e potrebbe essere ancora in esecuzione nella VM")

// guestPollInterval is a variable only so tests can shorten it.
var guestPollInterval = time.Second

// runGuestScript runs a FIXED shell script through the guest agent. Every value that
// comes from a request is passed as a positional parameter ($1, $2, ...) of `sh -c`,
// so it is never parsed as shell code. The script text must not contain request data.
func runGuestScript(c agentExecer, node string, vmid int, script string, args []string, timeout time.Duration) (guestResult, error) {
	return runGuestArgv(c, node, vmid, append([]string{"/bin/sh", "-c", script, "pmgr"}, args...), timeout)
}

// runGuestArgv runs argv through the guest agent and waits for it to exit. No shell is
// involved unless argv[0] is one: arguments are never reinterpreted by the guest.
func runGuestArgv(c agentExecer, node string, vmid int, argv []string, timeout time.Duration) (guestResult, error) {
	raw, err := c.AgentExec(node, vmid, argv)
	if err != nil {
		return guestResult{}, agentError(err)
	}
	var started struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.PID == 0 {
		log.Printf("guest agent: risposta inattesa a exec su %s/%d: %s", node, vmid, string(raw))
		return guestResult{}, fmt.Errorf("risposta inattesa del guest agent")
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(guestPollInterval)
		statusRaw, err := c.AgentExecStatus(node, vmid, started.PID)
		if err != nil {
			continue // l'agent può non rispondere per un attimo: si riprova fino alla scadenza
		}
		var s struct {
			Exited   int    `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  string `json:"out-data"`
			ErrData  string `json:"err-data"`
		}
		if err := json.Unmarshal(statusRaw, &s); err != nil {
			continue
		}
		if s.Exited == 1 {
			return guestResult{Out: s.OutData, Err: s.ErrData, ExitCode: s.ExitCode}, nil
		}
	}
	return guestResult{}, errGuestTimeout
}

// agentError turns guest-agent failures into messages an operator can act on.
// The raw Proxmox message goes to the log only.
func agentError(err error) error {
	msg := err.Error()
	log.Printf("guest agent exec: %s", msg)
	switch {
	case strings.Contains(msg, "frozen state"):
		return fmt.Errorf("il guest agent è in stato 'frozen' (snapshot o backup non concluso): riprova tra poco; se persiste, riavvia qemu-guest-agent nella VM")
	case strings.Contains(msg, "has been disabled"):
		return fmt.Errorf("guest-exec è disabilitato nella VM (BLACKLIST_RPC di qemu-guest-agent)")
	case strings.Contains(msg, "not running") || strings.Contains(msg, "No QEMU guest agent"):
		return fmt.Errorf("guest agent non attivo nella VM")
	}
	return fmt.Errorf("guest agent non raggiungibile")
}

// ── SSH key ──────────────────────────────────────────────────────────────────

const maxSSHKeyLen = 16384

// One public key on one line: "<type> <base64> [comment]". The key is passed to the
// guest as an argument (never inside shell code); the strict format is defence in depth
// and keeps authorized_keys to exactly one well-formed line per request.
var sshKeyPattern = regexp.MustCompile(`^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+={0,3}( [\x20-\x7e]{1,256})?$`)

// normalizeSSHKey returns "" for no key, the trimmed key if valid, or an error.
func normalizeSSHKey(raw string) (string, error) {
	k := strings.TrimSpace(raw)
	if k == "" {
		return "", nil
	}
	if len(k) > maxSSHKeyLen || strings.ContainsAny(k, "\r\n") || !sshKeyPattern.MatchString(k) {
		return "", fmt.Errorf("chiave pubblica SSH non valida: serve una sola riga nel formato \"tipo base64 [commento]\"")
	}
	return k, nil
}

// ── Disk slot / mount point ──────────────────────────────────────────────────

// diskSlotLimits: highest index accepted for each bus (Proxmox limits). Shared by
// adddisk (next free slot) and setup-disk (validation) so they cannot diverge.
var diskSlotLimits = map[string]int{"scsi": 30, "virtio": 15, "sata": 5}

var diskSlotParse = regexp.MustCompile(`^(scsi|virtio|sata)(0|[1-9][0-9]?)$`)

func validateDiskSlot(slot string) error {
	m := diskSlotParse.FindStringSubmatch(slot)
	if m == nil {
		return fmt.Errorf("slot disco non valido")
	}
	n, _ := strconv.Atoi(m[2])
	if n > diskSlotLimits[m[1]] {
		return fmt.Errorf("slot disco non valido: %s accetta indici da 0 a %d", m[1], diskSlotLimits[m[1]])
	}
	return nil
}

// protectedMountRoots: system directories that must never be mounted over, together
// with everything below them (a mount on /etc/ssh is as harmful as one on /etc).
var protectedMountRoots = []string{
	"/etc", "/boot", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/var", "/proc",
	"/sys", "/dev", "/root", "/home", "/opt", "/run", "/tmp",
}

func validateMountPoint(p string) error {
	if !mountPointPattern.MatchString(p) {
		return fmt.Errorf("mount point non valido")
	}
	if path.Clean(p) != p {
		return fmt.Errorf("mount point non valido: usa un percorso assoluto senza slash finale né doppi slash")
	}
	for _, root := range protectedMountRoots {
		if p == root || strings.HasPrefix(p, root+"/") {
			return fmt.Errorf("mount point non permesso: %s è (o sta dentro) una directory di sistema", p)
		}
	}
	return nil
}

// ── Guest scripts ────────────────────────────────────────────────────────────
//
// Single source of truth: guestscripts/*.sh. The same text is used as
//   - labelled helper installed in the guest (/usr/local/libexec/pmgr/<name>): on SELinux
//     enforcing guests it runs in virt_qemu_ga_unconfined_t, where useradd/parted/mount
//     work (the agent's own confined shell cannot run them), or
//   - inline fallback (`sh -c <text>`) on guests that have no helper (e.g. Debian/Ubuntu).
// Request data reaches either form only as arguments, never inside script text.

//go:embed guestscripts/adduser.sh
var addUserScriptSrc string

//go:embed guestscripts/disk.sh
var diskScriptSrc string

const guestHelperDir = "/usr/local/libexec/pmgr"

// guestTool describes one script: helper file name and the embedded inline fallback.
type guestTool struct {
	name   string
	inline string
}

var (
	addUserTool = guestTool{name: "adduser.sh", inline: addUserScriptSrc}
	diskTool    = guestTool{name: "disk.sh", inline: diskScriptSrc}
)

func (t guestTool) helperPath() string { return guestHelperDir + "/" + t.name }

// probeHelper asks the guest whether the labelled helper is installed and executable.
// `[ -x ]` is a shell builtin: it runs even in the confined agent domain, so the probe
// itself works on any guest. An agent error is returned (the real command would fail too).
func (t guestTool) probeHelper(c agentExecer, node string, vmid int) (bool, error) {
	res, err := runGuestScript(c, node, vmid, `[ -x "$1" ] && echo HELPER || echo INLINE`, []string{t.helperPath()}, 15*time.Second)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(res.Out) == "HELPER", nil
}

// run executes the tool with args using the helper (useHelper) or the inline fallback.
func (t guestTool) run(c agentExecer, node string, vmid int, useHelper bool, args []string, timeout time.Duration) (guestResult, error) {
	if useHelper {
		return runGuestArgv(c, node, vmid, append([]string{t.helperPath()}, args...), timeout)
	}
	return runGuestScript(c, node, vmid, t.inline, args, timeout)
}

func guestMode(useHelper bool) string {
	if useHelper {
		return "helper"
	}
	return "inline"
}
