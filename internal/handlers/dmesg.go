package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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
	dmesgDeadline   = 60 * time.Second // tetto complessivo della richiesta (le singole chiamate hanno i loro timeout)
	dmesgMaxPar     = 4                // letture dmesg contemporanee in tutto il proxy
)

var (
	dmesgLinesAllowed = map[int]bool{100: true, 300: true, 1000: true}
	dmesgLevelAllowed = map[string]bool{"all": true, "warn": true}
	// Proxmox node names are DNS-style host names.
	nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)
	ansiPattern     = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
	dmesgSlots      = make(chan struct{}, dmesgMaxPar)
)

// dropUnsafeRune: control characters and bidirectional overrides (they can reorder text to spoof what is shown).
func dropUnsafeRune(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069):
		return true
	}
	return false
}

// cleanText removes unsafe runes and invalid UTF-8 from a short guest-provided string.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if dropUnsafeRune(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
}

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
		case dropUnsafeRune(r):
			// dropped: control characters and bidi overrides
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
	Level          string   `json:"level"`         // livello richiesto
	LevelApplied   string   `json:"level_applied"` // livello davvero applicato ("all" se il dmesg del guest non supporta il filtro)
	Mode           string   `json:"mode"`          // helper | inline
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
		log.Printf("dmesg %s/%d: non leggibile nel guest (%s): stderr %d byte: %.200q", node, vmid, guestMode(useHelper), len(res.Err), strings.TrimSpace(res.Err))
		msg := "il guest non permette di leggere il dmesg"
		if !useHelper {
			msg += fmt.Sprintf(": se la VM usa SELinux enforcing serve l'helper dmesg.sh in %s (vedi documentazione)", guestHelperDir)
		}
		return dmesgResponse{}, http.StatusBadGateway, errors.New(msg)
	default:
		log.Printf("dmesg %s/%d: exit %d (%s): stderr %d byte: %.200q", node, vmid, res.ExitCode, guestMode(useHelper), len(res.Err), strings.TrimSpace(res.Err))
		return dmesgResponse{}, http.StatusBadGateway, fmt.Errorf("lettura del dmesg non riuscita (codice %d)", res.ExitCode)
	}

	out, truncated := sanitizeDmesg(res.Out, dmesgMaxBytes)
	if len(out) > lines { // un guest ostile puo' ignorare `tail`: si tengono comunque le ultime `lines` righe
		out = out[len(out)-lines:]
		truncated = true
	}
	applied := level
	if strings.Contains(res.Err, "level filter not supported") {
		applied = "all"
	}
	// Only counts and the mode are logged, never the content of the guest's kernel log.
	log.Printf("dmesg %s/%d: %d righe (%s)", node, vmid, len(out), guestMode(useHelper))
	return dmesgResponse{Lines: out, Count: len(out), LinesRequested: lines, Level: level, LevelApplied: applied,
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
	w.Header().Set("Cache-Control", "no-store") // il log va riletto a ogni clic: mai dalla cache
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
	key := fmt.Sprintf("%d|%s|%d", clusterIdx(r), node, vmid)
	if !dmesgAcquire(key) {
		writeError(w, errors.New("lettura già in corso per questa VM"), http.StatusTooManyRequests)
		return
	}
	// Tetto globale alle letture contemporanee: ognuna tiene occupati il guest agent e la memoria del proxy.
	select {
	case dmesgSlots <- struct{}{}:
	default:
		dmesgRelease(key)
		writeError(w, errors.New("troppe letture dmesg in corso: riprova tra poco"), http.StatusTooManyRequests)
		return
	}
	c, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		dmesgRelease(key)
		<-dmesgSlots
		writeError(w, err, http.StatusServiceUnavailable)
		return
	}

	type outcome struct {
		resp   dmesgResponse
		status int
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		// La chiave e lo slot si liberano quando il lavoro finisce davvero, anche se la richiesta e' gia' tornata.
		defer func() { dmesgRelease(key); <-dmesgSlots }()
		defer func() { // il Recoverer di chi non protegge le goroutine: un panico qui fermerebbe il processo
			if rec := recover(); rec != nil {
				log.Printf("dmesg %s/%d: panico: %v", node, vmid, rec)
				done <- outcome{status: http.StatusInternalServerError, err: errors.New("errore interno")}
			}
		}()
		var o outcome
		o.resp, o.status, o.err = readDmesg(c, node, vmid, body.Lines, body.Level)
		done <- o
	}()
	timer := time.NewTimer(dmesgDeadline)
	defer timer.Stop()
	select {
	case o := <-done:
		if o.err != nil {
			writeError(w, o.err, o.status)
			return
		}
		writeJSON(w, o.resp)
	case <-timer.C:
		writeError(w, errors.New("tempo scaduto: la VM non ha risposto in tempo"), http.StatusGatewayTimeout)
	case <-r.Context().Done(): // il browser ha chiuso la richiesta: niente da scrivere
	}
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
			v = cleanText(v)
			if utf8.RuneCountInString(v) > 100 {
				v = string([]rune(v)[:100])
			}
			if v != "" {
				out[dst] = v
			}
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

// ── Network addresses reported by the guest agent ─────────────────────────────
// Used when a VM has no static address in its Proxmox cloud-init config (DHCP, manual setup).

type netIface struct {
	Name string   `json:"name"`
	IPv4 []string `json:"ipv4"`
	IPv6 []string `json:"ipv6"`
}

type netInfo struct {
	Interfaces  []netIface `json:"interfaces"`
	PrimaryIPv4 string     `json:"primary_ipv4"`
}

const (
	netMaxIfaces  = 32
	netMaxAddrs   = 16
	netMaxNameLen = 32
)

// virtualIfacePattern: interfaces created by containers, bridges and tunnels, never "the" address of a VM.
var virtualIfacePattern = regexp.MustCompile(`^(lo|docker|veth|br-|virbr|cni|flannel|cali|tun|tap|kube|vxlan|podman|cilium|lxc|weave)`)

// normalizeNetInfo keeps interface names and valid addresses from guest-network-get-interfaces.
// Loopback and link-local addresses are dropped; the primary address is the first IPv4 of a
// non-virtual interface. Everything is bounded: the data comes from the guest.
func normalizeNetInfo(raw json.RawMessage) netInfo {
	out := netInfo{Interfaces: []netIface{}}
	var wrapper struct {
		Result []struct {
			Name string `json:"name"`
			IPs  []struct {
				Addr string `json:"ip-address"`
				Type string `json:"ip-address-type"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return out
	}
	for _, it := range wrapper.Result {
		if len(out.Interfaces) >= netMaxIfaces {
			break
		}
		name := cleanText(it.Name)
		if utf8.RuneCountInString(name) > netMaxNameLen {
			name = string([]rune(name)[:netMaxNameLen])
		}
		ni := netIface{Name: name, IPv4: []string{}, IPv6: []string{}}
		for _, a := range it.IPs {
			ip := net.ParseIP(strings.TrimSpace(a.Addr))
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				if len(ni.IPv4) < netMaxAddrs {
					ni.IPv4 = append(ni.IPv4, v4.String())
				}
			} else if len(ni.IPv6) < netMaxAddrs {
				ni.IPv6 = append(ni.IPv6, ip.String())
			}
		}
		if len(ni.IPv4)+len(ni.IPv6) == 0 {
			continue
		}
		out.Interfaces = append(out.Interfaces, ni)
		if out.PrimaryIPv4 == "" && len(ni.IPv4) > 0 && !virtualIfacePattern.MatchString(strings.ToLower(name)) {
			out.PrimaryIPv4 = ni.IPv4[0]
		}
	}
	return out
}

// GetVMNetInfo: GET /api/nodes/{node}/qemu/{vmid}/netinfo -> addresses from the guest agent, empty if it does not answer.
func (h *Handler) GetVMNetInfo(w http.ResponseWriter, r *http.Request) {
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
	data, err := client.GetVMNetworkInterfaces(node, vmid)
	if err != nil { // stopped VM or no agent
		writeJSON(w, netInfo{Interfaces: []netIface{}})
		return
	}
	writeJSON(w, normalizeNetInfo(data))
}
