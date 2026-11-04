package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/config"
)

// POST /api/balance/step runs ONE step of the balance plan: the first step of a plan recomputed right now with the
// same parameters. The client does not choose what to migrate: it says what it saw (VM, from, to) and the server
// executes it only if the fresh plan still starts with exactly that step (otherwise 409 plan_changed and nothing
// happens). So every rule of the planner (RAM limit of the destination, blocked VMs, allowed nodes) is enforced
// again at write time. One migration at a time per cluster: a new step is refused while the previous task runs.

const balanceStepBodyMax = 4 << 10

type balanceStepAPI interface {
	balanceAPI
	coldAPI
	VMMigrate(node string, vmid int, target string, online bool) (json.RawMessage, error)
	LXCMigrate(node string, vmid int, target string, restart bool) (json.RawMessage, error)
	GetTaskStatus(node, upid string) (json.RawMessage, error)
}

type balanceStepReq struct {
	Node   string `json:"node"`
	Kind   string `json:"kind"`
	VMID   int    `json:"vmid"`
	Target string `json:"target"`
	Mode   string `json:"mode"` // "" or "live" = as Proxmox does it; "cold" = running QEMU VM: shutdown, migrate, start (D50)
}

var errStepChanged = errors.New("il piano e' cambiato: ricontrolla i passi")
var errStepBusy = errors.New("c'e' ancora una migrazione in corso su questo cluster")
var errStepReserve = errors.New("riserva sotto il minimo per eseguire (8 GB per nodo): questo piano e' solo una simulazione")

// last migration started from the page, per cluster: guards "one at a time".
var balanceRunning = struct {
	sync.Mutex
	m map[int]balanceRun
}{m: map[int]balanceRun{}}

type balanceRun struct {
	Node, UPID string
	At         time.Time
	Job        *coldJob // cold migration driven by the server (then Node/UPID are empty)
}

// balanceRunStale: a previous task whose status cannot be read for this long no longer blocks new steps
// (node down, task log rotated). Proxmox still refuses to migrate a guest that is locked by a running migration.
const balanceRunStale = time.Hour

// upidPattern: UPID:node:pid:pstart:starttime:type:id:user:  (user may contain @ and !)
var upidPattern = regexp.MustCompile(`^UPID:([A-Za-z0-9.-]{1,64}):[0-9A-Fa-f]{1,16}:[0-9A-Fa-f]{1,16}:[0-9A-Fa-f]{1,16}:[A-Za-z0-9]{1,32}:[A-Za-z0-9._-]{0,64}:[A-Za-z0-9._@!-]{1,128}:$`)

func parseUPID(raw json.RawMessage) (node, upid string, err error) {
	if json.Unmarshal(raw, &upid) != nil {
		upid = strings.Trim(string(raw), `"`)
	}
	m := upidPattern.FindStringSubmatch(upid)
	if m == nil || !nodeNamePattern.MatchString(m[1]) {
		return "", "", errors.New("identificativo del task non valido")
	}
	return m[1], upid, nil
}

type taskInfo struct {
	Status     string `json:"status"`     // running | stopped
	ExitStatus string `json:"exitstatus"` // OK or the error text when stopped
	OK         bool   `json:"ok"`
}

func readTask(c interface {
	GetTaskStatus(node, upid string) (json.RawMessage, error)
}, node, upid string) (taskInfo, error) {
	raw, err := c.GetTaskStatus(node, upid)
	if err != nil {
		return taskInfo{}, err
	}
	var t struct {
		Status     string `json:"status"`
		ExitStatus string `json:"exitstatus"`
	}
	if json.Unmarshal(raw, &t) != nil {
		return taskInfo{}, errors.New("stato del task non valido")
	}
	out := taskInfo{Status: "running"}
	if t.Status == "stopped" {
		out.Status = "stopped"
		out.ExitStatus = clampText(t.ExitStatus, 200)
		out.OK = t.ExitStatus == "OK"
	}
	return out, nil
}

// stepStart: a started step is either a Proxmox task (UPID) or a cold procedure driven by the server (Job).
type stepStart struct {
	UPID json.RawMessage
	Job  *coldJob
}

// runBalanceStep rechecks the plan and starts the migration.
func runBalanceStep(c balanceStepAPI, label string, idx int, q balanceQuery, req balanceStepReq, now time.Time) (stepStart, error) {
	if !nodeNamePattern.MatchString(req.Node) || !nodeNamePattern.MatchString(req.Target) || req.VMID <= 0 || (req.Kind != "qemu" && req.Kind != "lxc") ||
		(req.Mode != "" && req.Mode != "live" && req.Mode != "cold") || (req.Mode == "cold" && req.Kind != "qemu") {
		return stepStart{}, errors.New("richiesta non valida")
	}
	if !q.execReserveOK() {
		return stepStart{}, errStepReserve
	}
	// the previous migration must be over (or unreadable for good: then we do not start a second one blindly)
	balanceRunning.Lock()
	prev, had := balanceRunning.m[idx]
	balanceRunning.Unlock()
	if had && prev.Job != nil {
		if !prev.Job.finished() {
			return stepStart{}, errStepBusy
		}
	} else if had {
		t, err := readTask(c, prev.Node, prev.UPID)
		switch {
		case err == nil && t.Status == "running":
			return stepStart{}, errStepBusy
		case err != nil && now.Sub(prev.At) < balanceRunStale:
			return stepStart{}, errStepBusy
		}
	}
	if had {
		balanceRunning.Lock()
		delete(balanceRunning.m, idx)
		balanceRunning.Unlock()
	}
	q.Fresh = true
	res, err := buildBalance(c, label, idx, q, now)
	if err != nil {
		return stepStart{}, err
	}
	if len(res.Steps) == 0 {
		return stepStart{}, errStepChanged
	}
	s := res.Steps[0]
	if s.From != req.Node || s.To != req.Target || s.VMID != req.VMID || s.Kind != req.Kind {
		return stepStart{}, errStepChanged
	}
	// the mode the user confirmed must be the plan's: cold_vms is part of the plan (D55: a cold move may cross CPU
	// models, a live one may not), so a "live" confirmation of a cold step, or the reverse, is a changed plan
	if (req.Mode == "cold") != s.Cold {
		return stepStart{}, errStepChanged
	}
	if s.Cold {
		j, err := newColdJob(idx, s.From, s.To, s.VMID, s.Name, s.Mem, now)
		if err != nil {
			return stepStart{}, err
		}
		balanceRunning.Lock()
		balanceRunning.m[idx] = balanceRun{At: now, Job: j}
		balanceRunning.Unlock()
		log.Printf("balance step: cluster=%d qemu=%d %s -> %s cold (%d GB)", idx, s.VMID, s.From, s.To, s.Mem>>30)
		go runCold(c, j)
		return stepStart{Job: j}, nil
	}
	var upid json.RawMessage
	if req.Kind == "qemu" {
		upid, err = c.VMMigrate(req.Node, req.VMID, req.Target, s.Running) // a stopped VM migrates offline
	} else {
		upid, err = c.LXCMigrate(req.Node, req.VMID, req.Target, s.Running) // a running container: restart mode
	}
	if err != nil {
		return stepStart{}, err
	}
	if node, id, perr := parseUPID(upid); perr != nil {
		log.Printf("balance step: cluster=%d migration started but the task id is not recognised", idx)
	} else {
		balanceRunning.Lock()
		balanceRunning.m[idx] = balanceRun{Node: node, UPID: id, At: now}
		balanceRunning.Unlock()
	}
	log.Printf("balance step: cluster=%d %s=%d %s -> %s (running=%v, %d GB)", idx, req.Kind, req.VMID, req.Node, req.Target, s.Running, s.Mem>>30)
	return stepStart{UPID: upid}, nil
}

// PostBalanceStep: POST /api/balance/step?<same parameters as GET /api/balance>  body {node, kind, vmid, target}
func (h *Handler) PostBalanceStep(w http.ResponseWriter, r *http.Request) {
	q, err := parseBalanceQuery(r.URL.Query().Get)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, balanceStepBodyMax)
	var req balanceStepReq
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return
	}
	idx := clusterIdx(r)
	cl, err := config.GetCluster(idx)
	if err != nil || cl.URL == "" {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	g := reportGate(idx)
	if !g.TryLock() {
		w.Header().Set("Retry-After", "10")
		writeError(w, errors.New("un altro calcolo e' in corso su questo cluster: riprova tra qualche secondo"), http.StatusTooManyRequests)
		return
	}
	defer g.Unlock()
	st, err := runBalanceStep(client, cl.Label, idx, q, req, time.Now())
	if err != nil {
		switch {
		case errors.Is(err, errStepChanged):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "reason": "plan_changed"})
		case errors.Is(err, errStepReserve):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "reason": "reserve_too_low"})
		case errors.Is(err, errStepBusy):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "reason": "busy"})
		case err.Error() == "richiesta non valida" || strings.Contains(err.Error(), "sconosciuto"):
			writeError(w, err, http.StatusBadRequest)
		default:
			log.Printf("balance step (cluster %d): %v", idx, err)
			if isForbidden(err) {
				writeError(w, errors.New("permessi Proxmox insufficienti per migrare (serve VM.Migrate)"), http.StatusForbidden)
			} else {
				writeError(w, errors.New("migrazione non avviata"), http.StatusBadGateway)
			}
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if st.Job != nil {
		writeJSON(w, map[string]string{"job": st.Job.ID})
		return
	}
	// an unrecognised task id is not passed on: the page then says the migration started and points to the tasks
	node, id, perr := parseUPID(st.UPID)
	if perr != nil {
		node, id = "", ""
	}
	writeJSON(w, map[string]string{"upid": id, "node": node})
}

// GetBalanceTask: GET /api/balance/task?upid=...  status of a migration started by the page (read only).
func (h *Handler) GetBalanceTask(w http.ResponseWriter, r *http.Request) {
	node, upid, err := parseUPID(json.RawMessage(`"` + strings.ReplaceAll(r.URL.Query().Get("upid"), `"`, "") + `"`))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	t, err := readTask(client, node, upid)
	if err != nil {
		log.Printf("balance task: %v", err)
		writeError(w, errors.New("stato della migrazione non disponibile"), http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, t)
}

var _ balanceStepAPI = (*api.Client)(nil)
