package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// setupDiskAPI is what the "Aggiungi disco" button needs from Proxmox.
type setupDiskAPI interface {
	agentExecer
	GetVMStatus(node string, vmid int) (json.RawMessage, error)
	GetVMConfig(node string, vmid int) (json.RawMessage, error)
	VMAction(node string, vmid int, action string, params url.Values) (json.RawMessage, error)
	WaitForTask(upid json.RawMessage, timeout time.Duration) error
}

// setupDiskTiming: only variables so that tests can shorten them.
var setupDiskTiming = struct {
	StopWait, StartWait time.Duration // shutdown / start tasks of the restart
	AgentPoll           time.Duration // between two "is the agent back" probes
	AgentWait           time.Duration // how long to wait for the agent after the restart
}{90 * time.Second, 60 * time.Second, 5 * time.Second, 120 * time.Second}

type setupDiskReq struct {
	Disk         string // slot in the VM config (virtio1, scsi2...): must exist there
	MountPoint   string
	Sticky       bool
	AllowRestart bool // the user confirmed that the VM may be stopped and started to make the disk appear
}

// setupDiskErr: Code is the HTTP status, Reason a code for the page ("restart_needed", "slot_missing"), Log the steps done.
type setupDiskErr struct {
	Code   int
	Reason string
	Err    error
	Log    []string
}

func (e *setupDiskErr) Error() string { return e.Err.Error() }

type setupDiskResult struct {
	Log        []string `json:"log"`
	Device     string   `json:"device"`
	MountPoint string   `json:"mount_point"`
}

// runSetupDisk formats and mounts the blank disk of a running VM through the guest agent.
//
// The slot the user picked is only checked against the VM configuration (a slot that is not there, or that is a
// CD-ROM, is refused: the disk that gets formatted is the single blank disk found in the guest, and the page says
// so). The VM is stopped and started only when the caller allows it (AllowRestart): without that permission the
// function stops with reason "restart_needed" before touching the VM, so the page can ask first.
func runSetupDisk(c setupDiskAPI, node string, vmid int, req setupDiskReq) (setupDiskResult, *setupDiskErr) {
	fail := func(code int, reason string, err error, opLog []string) (setupDiskResult, *setupDiskErr) {
		return setupDiskResult{}, &setupDiskErr{Code: code, Reason: reason, Err: err, Log: opLog}
	}
	statusRaw, err := c.GetVMStatus(node, vmid)
	if err != nil {
		return fail(http.StatusBadGateway, "", err, nil)
	}
	var st struct {
		Status string `json:"status"`
	}
	json.Unmarshal(statusRaw, &st)
	if st.Status != "running" {
		return fail(http.StatusBadRequest, "not_running", fmt.Errorf("la VM deve essere in esecuzione per configurare il disco lato guest"), nil)
	}
	cfgRaw, err := c.GetVMConfig(node, vmid)
	var cfg map[string]interface{}
	if err != nil || json.Unmarshal(cfgRaw, &cfg) != nil {
		return fail(http.StatusBadGateway, "", fmt.Errorf("configurazione della VM non leggibile"), nil)
	}
	slotVal, ok := cfg[req.Disk].(string)
	if !ok || strings.Contains(slotVal, "media=cdrom") {
		return fail(http.StatusBadRequest, "slot_missing", fmt.Errorf("il disco %s non esiste nella configurazione della VM", req.Disk), nil)
	}

	var opLog []string
	useHelper, err := diskTool.probeHelper(c, node, vmid)
	if err != nil {
		return fail(http.StatusBadGateway, "", err, nil)
	}
	log.Printf("setup-disk %s/%d: esecuzione %s", node, vmid, guestMode(useHelper))
	opLog = append(opLog, "Esecuzione guest: "+guestMode(useHelper))

	disk := func(timeout time.Duration, args ...string) (guestResult, error) {
		return diskTool.run(c, node, vmid, useHelper, args, timeout)
	}
	// findBlank: candidate disks (blank, or left half-done by a previous attempt). A failing lsblk in the guest is
	// an error (exit 3), not "no disk".
	findBlank := func() ([]string, error) {
		res, err := disk(30*time.Second, "find-blank")
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("ricerca dischi fallita nel guest (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Err))
		}
		return strings.Fields(res.Out), nil
	}

	opLog = append(opLog, "Ricerca disco vuoto (senza partizioni/filesystem)...")
	candidates, err := findBlank()
	if err != nil {
		return fail(http.StatusBadGateway, "", fmt.Errorf("guest agent: %w", err), opLog)
	}
	if len(candidates) == 0 {
		opLog = append(opLog, "Nessun disco vuoto trovato — rescan bus...")
		disk(30*time.Second, "rescan") // best effort
		for attempt := 0; attempt < provDiskScans && len(candidates) == 0; attempt++ {
			time.Sleep(provTiming.DiskScanWait)
			candidates, _ = findBlank()
		}
	}

	if len(candidates) == 0 {
		if !req.AllowRestart {
			// the disk did not show up: the only remedy left is a real stop + start of the VM, which needs the
			// user's explicit permission (a restart can be pointless, e.g. when the disk is already set up)
			return fail(http.StatusConflict, "restart_needed", fmt.Errorf("il disco non e' stato rilevato nel guest: servirebbe spegnere e riaccendere la VM"), opLog)
		}
		// A stop + start that waits for the tasks: a plain "reboot" without waiting races with the agent probe, which
		// could answer from the session BEFORE the restart.
		opLog = append(opLog, "Disco non rilevato — arresto VM per applicare la configurazione (autorizzato dall'utente)...")
		shutdownUPID, err := c.VMAction(node, vmid, "shutdown", url.Values{"forceStop": {"1"}, "timeout": {"30"}})
		if err != nil {
			return fail(http.StatusBadGateway, "", fmt.Errorf("arresto VM fallito: %w", err), opLog)
		}
		if err := c.WaitForTask(shutdownUPID, setupDiskTiming.StopWait); err != nil {
			return fail(http.StatusBadGateway, "", fmt.Errorf("arresto VM non completato: %w", err), opLog)
		}
		opLog = append(opLog, "VM arrestata, riavvio...")
		startUPID, err := c.VMAction(node, vmid, "start", url.Values{})
		if err != nil {
			return fail(http.StatusBadGateway, "", fmt.Errorf("avvio VM fallito: %w", err), opLog)
		}
		if err := c.WaitForTask(startUPID, setupDiskTiming.StartWait); err != nil {
			return fail(http.StatusBadGateway, "", fmt.Errorf("avvio VM non completato: %w", err), opLog)
		}
		opLog = append(opLog, "Attesa guest agent pronto...")
		agentReady := false
		deadline := time.Now().Add(setupDiskTiming.AgentWait)
		for time.Now().Before(deadline) {
			time.Sleep(setupDiskTiming.AgentPoll)
			if _, err := runGuestScript(c, node, vmid, `echo ready`, nil, 15*time.Second); err == nil {
				agentReady = true
				break
			}
		}
		if !agentReady {
			return fail(http.StatusBadGateway, "", fmt.Errorf("la VM non ha completato il riavvio entro %d minuti — riprova più tardi", int(setupDiskTiming.AgentWait/time.Minute)), opLog)
		}
		opLog = append(opLog, "Guest agent di nuovo raggiungibile, nuova ricerca disco...")
		candidates, _ = findBlank()
		if len(candidates) == 0 {
			return fail(http.StatusBadGateway, "", fmt.Errorf("disco non rilevato nemmeno dopo il riavvio — verifica la configurazione del disco in Proxmox (bus, storage, VMID)"), opLog)
		}
		opLog = append(opLog, fmt.Sprintf("Dischi vuoti trovati dopo riavvio: %s", strings.Join(candidates, ", ")))
	} else {
		opLog = append(opLog, fmt.Sprintf("Dischi vuoti trovati: %s", strings.Join(candidates, ", ")))
	}

	if len(candidates) > 1 {
		return fail(http.StatusConflict, "ambiguous", fmt.Errorf(
			"trovati %d dischi vuoti candidati (%s) — impossibile determinare automaticamente quale sia quello appena aggiunto; rimuovi/configura gli altri dischi vuoti e riprova",
			len(candidates), strings.Join(candidates, ", ")), opLog)
	}
	dev := "/dev/" + candidates[0]
	opLog = append(opLog, fmt.Sprintf("Disco selezionato: %s (scelto nel guest, non dallo slot %s)", dev, req.Disk))

	perm := "1777"
	if !req.Sticky {
		perm = "0777"
	}
	opLog = append(opLog, fmt.Sprintf("Partizionamento GPT + ext4 su %s...", dev))
	res, err := disk(120*time.Second, "setup", dev, req.MountPoint, perm) // mkfs on a big disk can exceed 30 s
	if err != nil {
		return fail(http.StatusBadGateway, "", fmt.Errorf("setup disco fallito: %w", err), opLog)
	}
	if res.ExitCode != 0 {
		return fail(http.StatusBadGateway, "", fmt.Errorf("setup disco fallito (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Err)), opLog)
	}
	opLog = append(opLog, "Output: "+strings.TrimSpace(res.Out))
	opLog = append(opLog, fmt.Sprintf("✓ Disco montato su %s (permessi %s)", req.MountPoint, perm))
	return setupDiskResult{Log: opLog, Device: dev, MountPoint: req.MountPoint}, nil
}

// writeSetupDiskErr answers with {error, reason, log}.
func writeSetupDiskErr(w http.ResponseWriter, e *setupDiskErr) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": e.Err.Error(), "reason": e.Reason, "log": e.Log})
}
