package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Kernel log (dmesg) of a guest, read through the QEMU guest agent on demand.
// Read-only. Request data is limited to two whitelisted values (line count, level) that
// reach the guest only as positional arguments of a FIXED script (guestscripts/dmesg.sh).

const (
	dmesgTimeout    = 25 * time.Second
	dmesgMaxBytes   = 512 * 1024 // cap on what is kept from the guest output
	dmesgMaxLineLen = 2000
	dmesgDefLines   = 300
)

var (
	dmesgLinesAllowed = map[int]bool{100: true, 300: true, 1000: true}
	dmesgLevelAllowed = map[string]bool{"all": true, "warn": true}
	// Proxmox node names are DNS-style host names.
	nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)
	ansiPattern     = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
)

// validVMTarget validates the node and vmid taken from the URL.
func validVMTarget(node, vmidStr string) (int, error) {
	if !nodeNamePattern.MatchString(node) {
		return 0, errors.New("nodo non valido")
	}
	vmid, err := strconv.Atoi(vmidStr)
	if err != nil || vmid < 100 || vmid > 999999999 {
		return 0, errors.New("vmid non valido")
	}
	return vmid, nil
}

// sanitizeDmesg turns guest output into safe display lines: invalid UTF-8 replaced, ANSI
// sequences and control characters removed (the browser also renders it as plain text, this
// is defence in depth), very long lines cut, and at most maxBytes kept (the END of the log,
// which is the most recent part). It reports whether anything was cut off.
func sanitizeDmesg(raw string, maxBytes int) ([]string, bool) {
	truncated := false
	if len(raw) > maxBytes {
		raw = raw[len(raw)-maxBytes:]
		if i := strings.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:] // drop the partial first line
		}
		truncated = true
	}
	raw = strings.ToValidUTF8(raw, "�")
	raw = ansiPattern.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			// dropped: control characters
		default:
			b.WriteRune(r)
		}
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, truncated
	}
	for i, l := range lines {
		if utf8.RuneCountInString(l) > dmesgMaxLineLen {
			lines[i] = string([]rune(l)[:dmesgMaxLineLen]) + "..."
			truncated = true
		}
	}
	return lines, truncated
}

// dmesgAPI is the part of the Proxmox client the dmesg read needs.
type dmesgAPI interface {
	agentExecer
	GetVMStatus(node string, vmid int) (json.RawMessage, error)
	GetVMOSInfo(node string, vmid int) (json.RawMessage, error)
}

type dmesgResponse struct {
	Lines          []string `json:"lines"`
	Count          int      `json:"count"`
	LinesRequested int      `json:"lines_requested"`
	Level          string   `json:"level"`
	Mode           string   `json:"mode"` // helper | inline
	Truncated      bool     `json:"truncated"`
	FetchedAt      int64    `json:"fetched_at"`
}

// readDmesg returns the response, or an HTTP status and a message that is safe to show.
func readDmesg(c dmesgAPI, node string, vmid, lines int, level string) (dmesgResponse, int, error) {
	raw, err := c.GetVMStatus(node, vmid)
	if err != nil {
		log.Printf("dmesg %s/%d: stato VM non letto: %v", node, vmid, err)
		return dmesgResponse{}, http.StatusBadGateway, errors.New("stato della VM non disponibile")
	}
	var st struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &st) != nil || st.Status != "running" {
		return dmesgResponse{}, http.StatusConflict, errors.New("la VM non è in esecuzione")
	}

	// The OS info call doubles as a reachability probe of the guest agent.
	osRaw, err := c.GetVMOSInfo(node, vmid)
	if err != nil {
		return dmesgResponse{}, http.StatusBadGateway, agentError(err)
	}
	var osinfo struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if json.Unmarshal(osRaw, &osinfo) == nil && osinfo.Result.ID == "mswindows" {
		return dmesgResponse{}, http.StatusConflict, errors.New("il dmesg non è disponibile per le VM Windows")
	}

	useHelper, err := dmesgTool.probeHelper(c, node, vmid)
	if err != nil {
		return dmesgResponse{}, http.StatusBadGateway, err
	}
	res, err := dmesgTool.run(c, node, vmid, useHelper, []string{strconv.Itoa(lines), level}, dmesgTimeout)
	if err != nil {
		return dmesgResponse{}, http.StatusBadGateway, err
	}
	switch res.ExitCode {
	case 0:
	case 3:
		log.Printf("dmesg %s/%d: non leggibile nel guest (%s): %s", node, vmid, guestMode(useHelper), strings.TrimSpace(res.Err))
		msg := "il guest non permette di leggere il dmesg"
		if !useHelper {
			msg += fmt.Sprintf(": se la VM usa SELinux enforcing serve l'helper dmesg.sh in %s (vedi documentazione)", guestHelperDir)
		}
		return dmesgResponse{}, http.StatusBadGateway, errors.New(msg)
	default:
		log.Printf("dmesg %s/%d: exit %d (%s): %s", node, vmid, res.ExitCode, guestMode(useHelper), strings.TrimSpace(res.Err))
		return dmesgResponse{}, http.StatusBadGateway, fmt.Errorf("lettura del dmesg non riuscita (codice %d)", res.ExitCode)
	}

	out, truncated := sanitizeDmesg(res.Out, dmesgMaxBytes)
	// Only counts and the mode are logged, never the content of the guest's kernel log.
	log.Printf("dmesg %s/%d: %d righe (%s)", node, vmid, len(out), guestMode(useHelper))
	return dmesgResponse{Lines: out, Count: len(out), LinesRequested: lines, Level: level,
		Mode: guestMode(useHelper), Truncated: truncated, FetchedAt: time.Now().Unix()}, http.StatusOK, nil
}

// One read per VM at a time: a second click while the first is running is refused.
var dmesgInFlight = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

func dmesgAcquire(key string) bool {
	dmesgInFlight.Lock()
	defer dmesgInFlight.Unlock()
	if dmesgInFlight.m[key] {
		return false
	}
	dmesgInFlight.m[key] = true
	return true
}

func dmesgRelease(key string) {
	dmesgInFlight.Lock()
	delete(dmesgInFlight.m, key)
	dmesgInFlight.Unlock()
}

// VMDmesg: POST /api/nodes/{node}/qemu/{vmid}/dmesg  {"lines":300,"level":"all"}
// POST (not GET) because it runs a command in the guest: no caching, no link-triggered runs.
func (h *Handler) VMDmesg(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, err := validVMTarget(node, chi.URLParam(r, "vmid"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	body := struct {
		Lines int    `json:"lines"`
		Level string `json:"level"`
	}{Lines: dmesgDefLines, Level: "all"}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return
	}
	if !dmesgLinesAllowed[body.Lines] {
		writeError(w, errors.New("numero di righe non valido (100, 300 o 1000)"), http.StatusBadRequest)
		return
	}
	if !dmesgLevelAllowed[body.Level] {
		writeError(w, errors.New("livello non valido (all o warn)"), http.StatusBadRequest)
		return
	}
	c, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, http.StatusServiceUnavailable)
		return
	}
	key := fmt.Sprintf("%d|%s|%d", clusterIdx(r), node, vmid)
	if !dmesgAcquire(key) {
		writeError(w, errors.New("lettura già in corso per questa VM"), http.StatusTooManyRequests)
		return
	}
	defer dmesgRelease(key)

	resp, status, err := readDmesg(c, node, vmid, body.Lines, body.Level)
	if err != nil {
		writeError(w, err, status)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, resp)
}

// normalizeOSInfo keeps the few string fields of guest-get-osinfo the UI uses.
func normalizeOSInfo(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	var wrapper struct {
		Result map[string]interface{} `json:"result"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return out
	}
	keys := map[string]string{"id": "id", "name": "name", "pretty-name": "pretty_name", "version": "version",
		"version-id": "version_id", "kernel-release": "kernel_release", "machine": "machine"}
	for src, dst := range keys {
		if v, ok := wrapper.Result[src].(string); ok && v != "" {
			if utf8.RuneCountInString(v) > 100 {
				v = string([]rune(v)[:100])
			}
			out[dst] = strings.ToValidUTF8(v, "")
		}
	}
	return out
}

// GetVMOSInfo: GET /api/nodes/{node}/qemu/{vmid}/osinfo -> {} when the agent does not answer.
func (h *Handler) GetVMOSInfo(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, err := validVMTarget(node, chi.URLParam(r, "vmid"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	data, err := client.GetVMOSInfo(node, vmid)
	if err != nil { // stopped VM or no agent: not an error for the list
		writeJSON(w, map[string]string{})
		return
	}
	writeJSON(w, normalizeOSInfo(data))
}
