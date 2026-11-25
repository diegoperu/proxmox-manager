package handlers

// Manager SSH access to the nodes (onboarding step 3, D150-D153, D158-D159).
//
// The manager reaches each node as pmgr-sync, restricted on the node by pmgr-sync-gate (it can list two folders and
// write into them, nothing else). Here: the status of that access per node (pmgr-list, only towards nodes whose host
// key an administrator confirmed), reading a node's host key fingerprint for comparison, confirming it (TOTP, see
// routeAccess) and forgetting it. Nothing in this file writes to a node or to Proxmox.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/nodessh"

	"github.com/go-chi/chi/v5"
)

// nodeSSH is what the handlers need from internal/nodessh (an interface so tests use a fake).
type nodeSSH interface {
	EnsureKey(ctx context.Context) (nodessh.PublicKey, error)
	Pinned(host string) (string, bool, error)
	Scan(ctx context.Context, host string) (b64, fingerprint string, err error)
	Pin(ctx context.Context, host, confirmed string) error
	Unpin(host string) (bool, error)
	List(ctx context.Context, host string) (nodessh.Listing, error)
}

// SetNodeSSH installs the SSH store (nil = the endpoints answer 503).
func (h *Handler) SetNodeSSH(s nodeSSH) { h.ssh = s }

type clusterStatusAPI interface {
	GetClusterStatus() (json.RawMessage, error)
}

type syncNode struct {
	Name, Address string
	Online        bool
}

// clusterNodes reads name, address and state of the nodes from /cluster/status (the corosync addresses).
func clusterNodes(c clusterStatusAPI) ([]syncNode, error) {
	raw, err := c.GetClusterStatus()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Type   string          `json:"type"`
		Name   string          `json:"name"`
		IP     string          `json:"ip"`
		Online json.RawMessage `json:"online"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil, errors.New("stato del cluster non leggibile")
	}
	out := []syncNode{}
	for _, r := range rows {
		if r.Type != "node" || !nodeNamePattern.MatchString(r.Name) {
			continue
		}
		n := syncNode{Name: r.Name, Online: readinessFlag(r.Online, false)}
		if nodessh.ValidAddress(r.IP) {
			n.Address = r.IP
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Name, out[j].Name) })
	return out, nil
}

type syncNodeState struct {
	Node         string `json:"node"`
	Address      string `json:"address"`
	Online       bool   `json:"online"`
	Pinned       bool   `json:"pinned"`
	Fingerprint  string `json:"fingerprint"`
	State        string `json:"state"`
	Message      string `json:"message"`
	Files        int    `json:"files"`
	Truncated    bool   `json:"truncated"`
	Invalid      int    `json:"invalid"`
	Digest       string `json:"digest"`
	SameAsOthers *bool  `json:"same_as_others"`
}

type syncSetup struct {
	Ready    bool     `json:"ready"`
	Reason   string   `json:"reason"`
	Commands []string `json:"commands"`
	Note     string   `json:"note"`
}

type syncSummary struct {
	OK      int `json:"ok"`
	Todo    int `json:"todo"`
	Problem int `json:"problem"`
}

type syncAccessResult struct {
	Cluster            string          `json:"cluster"`
	GeneratedAt        int64           `json:"generated_at"`
	ManagerKey         string          `json:"manager_key"`
	ManagerFingerprint string          `json:"manager_fingerprint"`
	ManagerFrom        []string        `json:"manager_from"`
	VerifyCommand      string          `json:"verify_command"`
	Setup              syncSetup       `json:"setup"`
	Nodes              []syncNodeState `json:"nodes"`
	Summary            syncSummary     `json:"summary"`
}

// syncStateOf turns a nodessh error into a state code and a sentence; the raw ssh text never reaches the page.
func syncStateOf(err error) (state, message string) {
	switch {
	case errors.Is(err, nodessh.ErrNotPinned):
		return "not_pinned", "Impronta della chiave del nodo da confermare."
	case errors.Is(err, nodessh.ErrAuthRefused):
		return "auth_refused", "Il nodo non accetta ancora la chiave del manager: esegui il comando di preparazione su questo nodo."
	case errors.Is(err, nodessh.ErrGateDenied):
		return "gate_denied", "Il comando ristretto del nodo ha rifiutato la richiesta: rilancia il comando di preparazione."
	case errors.Is(err, nodessh.ErrHostKeyChanged):
		return "host_key_changed", "ATTENZIONE: la chiave del nodo non e' piu' quella confermata. Verifica sul nodo prima di confermarne una nuova."
	case errors.Is(err, nodessh.ErrUnreachable):
		return "unreachable", "Nodo non raggiungibile via SSH dal manager (porta 22)."
	}
	return "error", "Errore SSH (dettagli nel log del server)."
}

// setupCommands compiles the root commands that authorise the manager key on a node. Every value is validated:
// the key by nodessh (fixed format), the addresses by config.ValidNetworkSettings (IPv4).
func setupCommands(key nodessh.PublicKey, from []string) syncSetup {
	s := syncSetup{Commands: []string{},
		Note: "Da eseguire come root su OGNI nodo, con pmgr-sync-setup.sh di tools/pmgr-sync (dal passo 5 arrivera' come allegato firmato della release). " +
			"Il comando riscrive le chiavi ammesse per pmgr-sync: dopo, solo il manager puo' scrivere su quel nodo (D159)."}
	if key.Line == "" {
		s.Reason = "Chiave del manager non disponibile."
		return s
	}
	if len(from) == 0 {
		s.Reason = "Manca l'indirizzo del manager visto dai nodi: impostalo in Impostazioni, Infrastruttura."
		return s
	}
	s.Ready = true
	s.Commands = append(s.Commands,
		"printf '%s\\n' '"+key.Line+"' > /root/pmgr-manager.pub",
		"bash pmgr-sync-setup.sh --role dest --source-pubkey /root/pmgr-manager.pub --source-from "+strings.Join(from, ","))
	return s
}

// buildSyncAccess checks every node: pinned ones are asked pmgr-list (4 at a time), the others only reported.
func buildSyncAccess(ctx context.Context, c clusterStatusAPI, ssh nodeSSH, cl config.ClusterConfig, now time.Time) (syncAccessResult, error) {
	res := syncAccessResult{Cluster: cl.Label, GeneratedAt: now.Unix(), ManagerFrom: []string{}, VerifyCommand: nodessh.VerifyCommand, Nodes: []syncNodeState{}}
	key, err := ssh.EnsureKey(ctx)
	if err != nil {
		log.Printf("nodessh: manager key: %v", err)
	}
	res.ManagerKey, res.ManagerFingerprint = key.Line, key.Fingerprint
	if cl.Network != nil {
		res.ManagerFrom = append(res.ManagerFrom, cl.Network.ManagerFrom...)
	}
	res.Setup = setupCommands(key, res.ManagerFrom)
	nodes, err := clusterNodes(c)
	if err != nil {
		return res, err
	}
	res.Nodes = make([]syncNodeState, len(nodes))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, n syncNode) {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					res.Nodes[i] = syncNodeState{Node: n.Name, Address: n.Address, State: "error", Message: "Errore interno."}
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			res.Nodes[i] = checkSyncNode(ctx, ssh, n, key.Line != "")
		}(i, n)
	}
	wg.Wait()
	markSameAsOthers(res.Nodes)
	for _, n := range res.Nodes {
		switch n.State {
		case "ok":
			res.Summary.OK++
		case "not_pinned", "auth_refused", "no_address", "offline":
			res.Summary.Todo++
		default:
			res.Summary.Problem++
		}
	}
	return res, nil
}

func checkSyncNode(ctx context.Context, ssh nodeSSH, n syncNode, haveKey bool) syncNodeState {
	st := syncNodeState{Node: n.Name, Address: n.Address, Online: n.Online}
	if n.Address == "" {
		st.State, st.Message = "no_address", "Proxmox non riporta l'indirizzo del nodo."
		return st
	}
	fp, ok, err := ssh.Pinned(n.Address)
	if err != nil {
		st.State, st.Message = "error", "File delle impronte non leggibile (dettagli nel log del server)."
		log.Printf("nodessh: pinned keys: %v", err)
		return st
	}
	st.Pinned, st.Fingerprint = ok, fp
	switch {
	case !n.Online:
		st.State, st.Message = "offline", "Nodo non in linea."
		return st
	case !ok:
		st.State, st.Message = syncStateOf(nodessh.ErrNotPinned)
		return st
	case !haveKey:
		st.State, st.Message = "error", "Chiave del manager non disponibile."
		return st
	}
	listing, err := ssh.List(ctx, n.Address)
	if err != nil {
		st.State, st.Message = syncStateOf(err)
		log.Printf("nodessh: pmgr-list on node %s (%s): %s", n.Name, n.Address, st.State)
		return st
	}
	st.State = "ok"
	st.Files, st.Truncated, st.Invalid, st.Digest = len(listing.Entries), listing.Truncated, listing.Invalid, listing.Digest()
	st.Message = fmt.Sprintf("Il comando ristretto risponde: %d file in snippets e pmgr-scripts.", st.Files)
	if st.Invalid > 0 || st.Truncated {
		st.Message += " Alcune righe dell'elenco non sono valide o l'elenco e' troncato."
	}
	return st
}

// markSameAsOthers compares the file lists of the reachable nodes with the most common one.
func markSameAsOthers(nodes []syncNodeState) {
	count := map[string]int{}
	ok := 0
	for _, n := range nodes {
		if n.State == "ok" {
			count[n.Digest]++
			ok++
		}
	}
	if ok < 2 {
		return
	}
	best, bestN := "", 0
	for d, c := range count {
		if c > bestN || (c == bestN && d < best) {
			best, bestN = d, c
		}
	}
	for i := range nodes {
		if nodes[i].State == "ok" {
			same := nodes[i].Digest == best
			nodes[i].SameAsOthers = &same
		}
	}
}

// ── cache and one computation at a time per cluster ──

type syncAccessEntry struct {
	sync.Mutex
	at    time.Time
	value syncAccessResult
	valid bool
}

var syncAccessEntries sync.Map

func forgetSyncAccess() {
	syncAccessEntries.Range(func(k, _ any) bool { syncAccessEntries.Delete(k); return true })
}

func (h *Handler) GetSyncAccess(w http.ResponseWriter, r *http.Request) {
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return
	}
	if h.ssh == nil {
		writeError(w, errors.New("accesso SSH non configurato"), http.StatusServiceUnavailable)
		return
	}
	cl, err := config.GetCluster(idx)
	client, cerr := h.getClientFor(idx)
	if err != nil || cerr != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	v, _ := syncAccessEntries.LoadOrStore(idx, &syncAccessEntry{})
	e := v.(*syncAccessEntry)
	if !e.TryLock() {
		w.Header().Set("Retry-After", "10")
		writeError(w, errors.New("controllo gia' in corso"), http.StatusTooManyRequests)
		return
	}
	defer e.Unlock()
	if r.URL.Query().Get("fresh") != "1" && e.valid && time.Since(e.at) < 30*time.Second && e.value.Cluster == cl.Label {
		writeJSON(w, e.value)
		return
	}
	res, err := buildSyncAccess(r.Context(), client, h.ssh, cl, time.Now())
	if err != nil {
		writeError(w, errors.New("impossibile leggere i nodi del cluster"), http.StatusBadGateway)
		return
	}
	e.value, e.at, e.valid = res, time.Now(), true
	writeJSON(w, res)
}

var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

type syncNodeRequest struct {
	Node        string `json:"node"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func decodeSyncRequest(w http.ResponseWriter, r *http.Request, needFingerprint bool) (syncNodeRequest, bool) {
	var req syncNodeRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&req) != nil || dec.More() || !nodeNamePattern.MatchString(req.Node) ||
		(needFingerprint && !fingerprintPattern.MatchString(req.Fingerprint)) || (!needFingerprint && req.Fingerprint != "") {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// syncTarget finds the address of a node of the cluster (taken from Proxmox, never from the request).
func (h *Handler) syncTarget(w http.ResponseWriter, r *http.Request, node string) (string, bool) {
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return "", false
	}
	if h.ssh == nil {
		writeError(w, errors.New("accesso SSH non configurato"), http.StatusServiceUnavailable)
		return "", false
	}
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return "", false
	}
	nodes, err := clusterNodes(client)
	if err != nil {
		writeError(w, errors.New("impossibile leggere i nodi del cluster"), http.StatusBadGateway)
		return "", false
	}
	for _, n := range nodes {
		if n.Name == node {
			if n.Address == "" {
				writeError(w, errors.New("Proxmox non riporta l'indirizzo del nodo"), http.StatusConflict)
				return "", false
			}
			return n.Address, true
		}
	}
	writeError(w, errors.New("nodo non trovato nel cluster"), http.StatusNotFound)
	return "", false
}

// PostSyncScan reads the host key a node presents now. Nothing is stored: the administrator compares the
// fingerprint with the one printed on the node by VerifyCommand.
func (h *Handler) PostSyncScan(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeSyncRequest(w, r, false)
	if !ok {
		return
	}
	addr, ok := h.syncTarget(w, r, req.Node)
	if !ok {
		return
	}
	_, fp, err := h.ssh.Scan(r.Context(), addr)
	if err != nil {
		_, msg := syncStateOf(err)
		if errors.Is(err, nodessh.ErrNoHostKey) {
			msg = "Il nodo non ha presentato una chiave ed25519."
		}
		writeError(w, errors.New(msg), http.StatusBadGateway)
		return
	}
	pinned, _, _ := h.ssh.Pinned(addr)
	writeJSON(w, map[string]any{"node": req.Node, "address": addr, "fingerprint": fp, "pinned_fingerprint": pinned,
		"verify_command": nodessh.VerifyCommand})
}

// PostSyncPin stores the node key, but only if the node still presents the key whose fingerprint was confirmed.
func (h *Handler) PostSyncPin(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeSyncRequest(w, r, true)
	if !ok {
		return
	}
	addr, ok := h.syncTarget(w, r, req.Node)
	if !ok {
		return
	}
	if err := h.ssh.Pin(r.Context(), addr, req.Fingerprint); err != nil {
		if errors.Is(err, nodessh.ErrHostKeyChanged) {
			writeError(w, errors.New("il nodo presenta ora una chiave diversa da quella confermata: rileggi l'impronta e verificala di nuovo"), http.StatusConflict)
			return
		}
		_, msg := syncStateOf(err)
		log.Printf("nodessh: pin node %s (%s): %v", req.Node, addr, err)
		writeError(w, errors.New(msg), http.StatusBadGateway)
		return
	}
	forgetSyncAccess()
	log.Printf("nodessh: host key of node %s (%s) confirmed: %s", req.Node, addr, req.Fingerprint)
	writeJSON(w, map[string]any{"ok": true, "node": req.Node, "fingerprint": req.Fingerprint})
}

// DeleteSyncPin forgets the confirmed key of a node.
func (h *Handler) DeleteSyncPin(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	if !nodeNamePattern.MatchString(node) {
		writeError(w, errors.New("nodo non valido"), http.StatusBadRequest)
		return
	}
	addr, ok := h.syncTarget(w, r, node)
	if !ok {
		return
	}
	found, err := h.ssh.Unpin(addr)
	if err != nil {
		log.Printf("nodessh: unpin node %s (%s): %v", node, addr, err)
		writeError(w, errors.New("impossibile aggiornare il file delle impronte"), http.StatusInternalServerError)
		return
	}
	forgetSyncAccess()
	log.Printf("nodessh: host key of node %s (%s) forgotten (was pinned: %v)", node, addr, found)
	writeJSON(w, map[string]any{"ok": true, "removed": found})
}
