package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cold migration of a running QEMU VM (D50-D53, 2026-11-04): shut it down, migrate it offline, start it on the
// target. The server drives the whole procedure (D53 A) so it does not depend on a browser tab; the page only reads
// its state and answers the one question it may ask: "the VM did not shut down, force stop?" (D51 B).
// Errors halfway (D52 A): a failed migration starts the VM again on its source node; a failed start on the target is
// retried once, then the VM is left stopped there and the state says so. Nothing is ever retried silently beyond that.
// The procedure lives in memory: if the server restarts halfway it stops, and the VM may stay stopped (logged).

type coldAPI interface {
	VMAction(node string, vmid int, action string, params url.Values) (json.RawMessage, error)
	GetVMStatus(node string, vmid int) (json.RawMessage, error)
	VMMigrate(node string, vmid int, target string, online bool) (json.RawMessage, error)
	GetTaskStatus(node, upid string) (json.RawMessage, error)
}

// coldTiming: how long each phase may take. Variables only so tests can shorten them.
var coldTiming = struct {
	Poll     time.Duration // between two status reads
	Shutdown time.Duration // ordered shutdown (ACPI / guest agent)
	Decide   time.Duration // the page must answer "force stop?" within this, otherwise the procedure is cancelled
	Stop     time.Duration // forced stop
	Migrate  time.Duration // offline migration task
	Start    time.Duration // start task
	Keep     time.Duration // a finished procedure stays readable this long
}{3 * time.Second, 3 * time.Minute, 5 * time.Minute, time.Minute, 2 * time.Hour, 5 * time.Minute, time.Hour}

const coldMaxJobs = 64

// phases and results, as codes: the page writes the sentences
const (
	coldShutdown  = "shutdown"   // ordered shutdown requested, waiting for the VM to stop
	coldNeedForce = "need_force" // did not stop in time: waiting for the user's decision
	coldStopping  = "stopping"   // forced stop requested
	coldMigrating = "migrating"
	coldStarting  = "starting"
	coldDone      = "done"

	coldOK              = "ok"
	coldCancelled       = "cancelled"        // no force stop: the VM stays on its node, running
	coldCancelRestarted = "cancel_restarted" // no force stop, but the VM had stopped meanwhile: started again on its node
	coldShutdownFailed  = "shutdown_failed"  // shutdown/stop refused or the VM never stopped: nothing moved
	coldMigrateRestart  = "migrate_failed_restarted"
	coldMigrateFailed   = "migrate_failed" // migration and restart on the source both failed: VM stopped on the source
	coldStartFailed     = "start_failed"   // migrated, but it does not start on the target: VM stopped on the target
)

type coldJob struct {
	ID      string
	Cluster int
	Node    string // source
	Target  string
	VMID    int
	Name    string
	Mem     int64

	mu       sync.Mutex
	phase    string
	result   string
	detail   string // short Proxmox error text for the page (already clamped)
	started  time.Time
	ended    time.Time
	deadline time.Time // need_force: when the question expires
	decide   chan bool
}

type coldView struct {
	ID            string `json:"id"`
	Phase         string `json:"phase"`
	Result        string `json:"result,omitempty"`
	Detail        string `json:"detail,omitempty"`
	VMID          int    `json:"vmid"`
	Name          string `json:"name"`
	From          string `json:"from"`
	To            string `json:"to"`
	StartedAt     int64  `json:"started_at"`
	DecideByEpoch int64  `json:"decide_by,omitempty"`
}

func (j *coldJob) view() coldView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := coldView{ID: j.ID, Phase: j.phase, Result: j.result, Detail: j.detail, VMID: j.VMID, Name: j.Name,
		From: j.Node, To: j.Target, StartedAt: j.started.Unix()}
	if j.phase == coldNeedForce {
		v.DecideByEpoch = j.deadline.Unix()
	}
	return v
}

func (j *coldJob) set(phase string) {
	j.mu.Lock()
	j.phase = phase
	j.mu.Unlock()
	log.Printf("balance cold: cluster=%d qemu=%d %s -> %s: %s", j.Cluster, j.VMID, j.Node, j.Target, phase)
}

func (j *coldJob) finish(result string, err error) {
	j.mu.Lock()
	j.phase, j.result, j.ended = coldDone, result, time.Now()
	if err != nil {
		j.detail = clampText(proxmoxMessage(err), 200)
	}
	j.mu.Unlock()
	if err != nil {
		log.Printf("balance cold: cluster=%d qemu=%d %s -> %s: %s (%v)", j.Cluster, j.VMID, j.Node, j.Target, result, err)
	} else {
		log.Printf("balance cold: cluster=%d qemu=%d %s -> %s: %s", j.Cluster, j.VMID, j.Node, j.Target, result)
	}
}

func (j *coldJob) finished() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.phase == coldDone
}

// coldMsg is an error whose text is ours (or the exit status of a task) and may be shown as is.
type coldMsg string

func (m coldMsg) Error() string { return string(m) }

// proxmoxMessage: our own messages as they are; of a Proxmox error only its JSON "message" (no URLs, no tokens).
func proxmoxMessage(err error) string {
	var m coldMsg
	if errors.As(err, &m) {
		return string(m)
	}
	s := err.Error()
	var body struct {
		Message string `json:"message"`
	}
	if k := strings.IndexByte(s, '{'); k >= 0 && json.Unmarshal([]byte(s[k:]), &body) == nil && body.Message != "" {
		return body.Message
	}
	return "errore di Proxmox"
}

var coldJobs = struct {
	sync.Mutex
	m map[string]*coldJob
}{m: map[string]*coldJob{}}

var coldIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func newColdJob(cluster int, node, target string, vmid int, name string, mem int64, now time.Time) (*coldJob, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, errors.New("identificativo non generabile")
	}
	j := &coldJob{ID: hex.EncodeToString(b), Cluster: cluster, Node: node, Target: target, VMID: vmid, Name: name, Mem: mem,
		phase: coldShutdown, started: now, decide: make(chan bool, 1)}
	coldJobs.Lock()
	defer coldJobs.Unlock()
	for id, o := range coldJobs.m { // forget finished procedures after a while; never forget a running one
		if o.finished() && now.Sub(o.ended) > coldTiming.Keep {
			delete(coldJobs.m, id)
		}
	}
	if len(coldJobs.m) >= coldMaxJobs {
		return nil, errStepBusy
	}
	coldJobs.m[j.ID] = j
	return j, nil
}

func findColdJob(id string, cluster int) *coldJob {
	if !coldIDPattern.MatchString(id) {
		return nil
	}
	coldJobs.Lock()
	defer coldJobs.Unlock()
	j := coldJobs.m[id]
	if j == nil || j.Cluster != cluster {
		return nil
	}
	return j
}

// ── the procedure ────────────────────────────────────────────────────────────

func coldVMState(c coldAPI, node string, vmid int) (string, error) {
	raw, err := c.GetVMStatus(node, vmid)
	if err != nil {
		return "", err
	}
	var s struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Status == "" {
		return "", coldMsg("stato della VM non valido")
	}
	return s.Status, nil
}

// waitStopped polls the VM until it is stopped or the time is over. Read errors are tolerated until the end.
func waitStopped(c coldAPI, node string, vmid int, d time.Duration) bool {
	end := time.Now().Add(d)
	for {
		if st, err := coldVMState(c, node, vmid); err == nil && st == "stopped" {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(coldTiming.Poll)
	}
}

// waitTask waits for a Proxmox task started by an action; an unrecognised task id is an error (we cannot follow it).
func waitTask(c coldAPI, raw json.RawMessage, d time.Duration) error {
	node, upid, err := parseUPID(raw)
	if err != nil {
		return coldMsg("identificativo del task non riconosciuto")
	}
	end := time.Now().Add(d)
	for {
		t, err := readTask(c, node, upid)
		if err == nil && t.Status == "stopped" {
			if t.OK {
				return nil
			}
			return coldMsg(t.ExitStatus)
		}
		if time.Now().After(end) {
			return coldMsg("tempo scaduto in attesa del task")
		}
		time.Sleep(coldTiming.Poll)
	}
}

func startVM(c coldAPI, node string, vmid int) error {
	raw, err := c.VMAction(node, vmid, "start", nil)
	if err != nil {
		return err
	}
	return waitTask(c, raw, coldTiming.Start)
}

// runCold is the whole procedure; it runs in its own goroutine and always ends in coldDone.
func runCold(c coldAPI, j *coldJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("balance cold: cluster=%d qemu=%d panic: %v", j.Cluster, j.VMID, r)
			j.finish(coldShutdownFailed, coldMsg("errore interno"))
		}
	}()
	// 1. ordered shutdown (Proxmox's own timeout matches ours; it never escalates to a forced stop by itself)
	if _, err := c.VMAction(j.Node, j.VMID, "shutdown", url.Values{"timeout": {strconv.Itoa(int(coldTiming.Shutdown / time.Second))}}); err != nil {
		j.finish(coldShutdownFailed, err)
		return
	}
	if !waitStopped(c, j.Node, j.VMID, coldTiming.Shutdown) {
		// 2. ask the page (D51 B)
		j.mu.Lock()
		j.phase, j.deadline = coldNeedForce, time.Now().Add(coldTiming.Decide)
		j.mu.Unlock()
		log.Printf("balance cold: cluster=%d qemu=%d did not shut down: waiting for the user", j.Cluster, j.VMID)
		force := false
		select {
		case force = <-j.decide:
		case <-time.After(coldTiming.Decide):
		}
		if !force {
			cancelCold(c, j)
			return
		}
		j.set(coldStopping)
		if _, err := c.VMAction(j.Node, j.VMID, "stop", nil); err != nil {
			j.finish(coldShutdownFailed, err)
			return
		}
		if !waitStopped(c, j.Node, j.VMID, coldTiming.Stop) {
			j.finish(coldShutdownFailed, coldMsg("la VM non si e' fermata"))
			return
		}
	}
	// 3. offline migration
	j.set(coldMigrating)
	raw, err := c.VMMigrate(j.Node, j.VMID, j.Target, false)
	if err == nil {
		err = waitTask(c, raw, coldTiming.Migrate)
	}
	if err != nil {
		// where is the VM now? a failed migration leaves it on the source; only if the source no longer has it
		// and the target does (migration done, task status lost) it is started on the target
		node := j.Node
		if _, serr := coldVMState(c, j.Node, j.VMID); serr != nil {
			if _, terr := coldVMState(c, j.Target, j.VMID); terr == nil {
				node = j.Target
			}
		}
		if rerr := startVM(c, node, j.VMID); rerr != nil {
			log.Printf("balance cold: cluster=%d qemu=%d restart on %s failed: %v", j.Cluster, j.VMID, node, rerr)
			j.finish(coldMigrateFailed, err)
			return
		}
		j.finish(coldMigrateRestart, err)
		return
	}
	// 4. start on the target, one retry (D52 A)
	j.set(coldStarting)
	if err := startVM(c, j.Target, j.VMID); err != nil {
		log.Printf("balance cold: cluster=%d qemu=%d first start on %s failed, retrying: %v", j.Cluster, j.VMID, j.Target, err)
		time.Sleep(coldTiming.Poll)
		if err2 := startVM(c, j.Target, j.VMID); err2 != nil {
			j.finish(coldStartFailed, err2)
			return
		}
	}
	j.finish(coldOK, nil)
}

// cancelCold: no forced stop. The shutdown request was already sent: if the guest stopped meanwhile, start it again.
func cancelCold(c coldAPI, j *coldJob) {
	if st, err := coldVMState(c, j.Node, j.VMID); err == nil && st == "stopped" {
		if err := startVM(c, j.Node, j.VMID); err != nil {
			j.finish(coldMigrateFailed, err)
			return
		}
		j.finish(coldCancelRestarted, nil)
		return
	}
	j.finish(coldCancelled, nil)
}

// ── HTTP ────────────────────────────────────────────────────────────────────

// GetBalanceJob: GET /api/balance/job?id=<32 hex>  state of a cold migration of this cluster (read only).
func (h *Handler) GetBalanceJob(w http.ResponseWriter, r *http.Request) {
	j := findColdJob(r.URL.Query().Get("id"), clusterIdx(r))
	if j == nil {
		writeError(w, errors.New("procedura non trovata"), http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, j.view())
}

// PostBalanceJobDecide: POST /api/balance/job/decide?id=<32 hex>  body {"force": true|false}
// Only while the procedure waits for it (need_force); the answer is taken once.
func (h *Handler) PostBalanceJobDecide(w http.ResponseWriter, r *http.Request) {
	j := findColdJob(r.URL.Query().Get("id"), clusterIdx(r))
	if j == nil {
		writeError(w, errors.New("procedura non trovata"), http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256)
	var body struct {
		Force *bool `json:"force"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Force == nil {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return
	}
	j.mu.Lock()
	waiting := j.phase == coldNeedForce
	if waiting {
		j.phase = coldStopping // the next answer finds the question closed
		if !*body.Force {
			j.phase = coldShutdown
		}
	}
	j.mu.Unlock()
	if !waiting {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "la procedura non sta aspettando una decisione", "reason": "not_waiting"})
		return
	}
	select {
	case j.decide <- *body.Force:
	default:
	}
	log.Printf("balance cold: cluster=%d qemu=%d user decision force=%v", j.Cluster, j.VMID, *body.Force)
	writeJSON(w, j.view())
}
