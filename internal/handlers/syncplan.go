package handlers

// Snippet and script delivery to the nodes (onboarding step 4, D150).
//
// GET  sync-plan: what every reachable node lacks or holds differently, compared with the bundle built from the
//      embedded tooling for the cluster profile. Read only (pmgr-list).
// POST sync-run:  copies the differences to the chosen nodes through the pmgr-sync gate (rsync, never --delete) and
//      re-reads each node to verify. Only if the plan is still the one the administrator looked at (digest), one
//      operation per cluster at a time; administrators with the TOTP step-up (routeAccess), every call in the action log.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/nodessh"
	"proxmox-manager/internal/snippetsync"
)

type bundleBuilder interface {
	Build(ctx context.Context, profile string) (*snippetsync.Bundle, error)
}

// SetSnippetBuilder installs the builder of the expected content (nil = the endpoints answer 503).
func (h *Handler) SetSnippetBuilder(b bundleBuilder) { h.snippets = b }

const maxPlanList = 100

type syncNodePlan struct {
	Node       string   `json:"node"`
	Address    string   `json:"address"`
	State      string   `json:"state"`
	Message    string   `json:"message"`
	Ready      bool     `json:"ready"`
	Aligned    bool     `json:"aligned"`
	Same       int      `json:"same"`
	Missing    []string `json:"missing"`
	Different  []string `json:"different"`
	Extra      []string `json:"extra"`
	NMissing   int      `json:"missing_count"`
	NDifferent int      `json:"different_count"`
	NExtra     int      `json:"extra_count"`
	todo       []string // missing + different, not truncated
}

type syncPlanSummary struct {
	Aligned  int `json:"aligned"`
	ToSync   int `json:"to_sync"`
	NotReady int `json:"not_ready"`
}

type syncPlanResult struct {
	Cluster      string          `json:"cluster"`
	Profile      string          `json:"profile"`
	BundleDigest string          `json:"bundle_digest"`
	Snippets     int             `json:"snippets"`
	Scripts      int             `json:"scripts"`
	Digest       string          `json:"digest"`
	Nodes        []syncNodePlan  `json:"nodes"`
	Summary      syncPlanSummary `json:"summary"`
	bundle       *snippetsync.Bundle
}

func clip(list []string) []string {
	if len(list) > maxPlanList {
		return append([]string{}, list[:maxPlanList]...)
	}
	return list
}

func profileOf(cl config.ClusterConfig) string {
	if cl.Network != nil && snippetsync.Profiles[cl.Network.SnippetProfile] {
		return cl.Network.SnippetProfile
	}
	return "standard"
}

// buildSyncPlan compares the bundle with every node whose host key is confirmed and that answers pmgr-list.
func buildSyncPlan(ctx context.Context, c clusterStatusAPI, ssh nodeSSH, b bundleBuilder, cl config.ClusterConfig) (syncPlanResult, error) {
	res := syncPlanResult{Cluster: cl.Label, Profile: profileOf(cl), Nodes: []syncNodePlan{}}
	bundle, err := b.Build(ctx, res.Profile)
	if err != nil {
		return res, err
	}
	res.bundle, res.BundleDigest = bundle, bundle.Digest
	for _, f := range bundle.Files {
		if f.Dir == "snippets" {
			res.Snippets++
		} else {
			res.Scripts++
		}
	}
	nodes, err := clusterNodes(c)
	if err != nil {
		return res, err
	}
	res.Nodes = make([]syncNodePlan, len(nodes))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, n syncNode) {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					res.Nodes[i] = syncNodePlan{Node: n.Name, Address: n.Address, State: "error", Message: "Errore interno."}
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			res.Nodes[i] = planNode(ctx, ssh, bundle, n)
		}(i, n)
	}
	wg.Wait()
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s\n", res.Profile, res.BundleDigest)
	for _, n := range res.Nodes {
		switch {
		case !n.Ready:
			res.Summary.NotReady++
		case n.Aligned:
			res.Summary.Aligned++
		default:
			res.Summary.ToSync++
		}
		todo := append([]string{}, n.todo...)
		sort.Strings(todo)
		fmt.Fprintf(h, "%s %v %s\n", n.Node, n.Ready, strings.Join(todo, ","))
	}
	res.Digest = hex.EncodeToString(h.Sum(nil))[:24]
	return res, nil
}

func planNode(ctx context.Context, ssh nodeSSH, bundle *snippetsync.Bundle, n syncNode) syncNodePlan {
	p := syncNodePlan{Node: n.Name, Address: n.Address, Missing: []string{}, Different: []string{}, Extra: []string{}}
	st := checkSyncNodeListing(ctx, ssh, n)
	p.State, p.Message = st.state, st.message
	if st.state != "ok" {
		return p
	}
	plan := snippetsync.Compare(bundle, st.listing)
	p.Ready, p.Aligned, p.Same = true, plan.Aligned(), plan.Same
	p.NMissing, p.NDifferent, p.NExtra = len(plan.Missing), len(plan.Different), len(plan.Extra)
	p.Missing, p.Different, p.Extra = clip(plan.Missing), clip(plan.Different), clip(plan.Extra)
	p.todo = append(append([]string{}, plan.Missing...), plan.Different...)
	if p.Aligned {
		p.Message = "Allineato."
	} else {
		p.Message = fmt.Sprintf("%d file da copiare, %d da aggiornare.", p.NMissing, p.NDifferent)
	}
	if p.NExtra > 0 {
		p.Message += fmt.Sprintf(" %d file in piu' sul nodo (non vengono cancellati).", p.NExtra)
	}
	return p
}

// ── one delivery operation per cluster at a time (the plan read shares the same lock) ──

var syncOps sync.Map

func syncOpLock(idx int) *sync.Mutex {
	v, _ := syncOps.LoadOrStore(idx, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (h *Handler) syncPlanSetup(w http.ResponseWriter, r *http.Request) (int, config.ClusterConfig, clusterStatusAPI, bool) {
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return 0, config.ClusterConfig{}, nil, false
	}
	if h.ssh == nil || h.snippets == nil {
		writeError(w, errors.New("sincronizzazione non configurata"), http.StatusServiceUnavailable)
		return 0, config.ClusterConfig{}, nil, false
	}
	cl, err := config.GetCluster(idx)
	client, cerr := h.getClientFor(idx)
	if err != nil || cerr != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return 0, config.ClusterConfig{}, nil, false
	}
	return idx, cl, client, true
}

func (h *Handler) GetSyncPlan(w http.ResponseWriter, r *http.Request) {
	idx, cl, client, ok := h.syncPlanSetup(w, r)
	if !ok {
		return
	}
	lock := syncOpLock(idx)
	if !lock.TryLock() {
		w.Header().Set("Retry-After", "10")
		writeError(w, errors.New("un'altra operazione di sincronizzazione e' in corso"), http.StatusTooManyRequests)
		return
	}
	defer lock.Unlock()
	res, err := buildSyncPlan(r.Context(), client, h.ssh, h.snippets, cl)
	if err != nil {
		log.Printf("snippetsync: plan cluster %d: %v", idx, err)
		writeError(w, errors.New("impossibile preparare l'anteprima (dettagli nel log del server)"), http.StatusBadGateway)
		return
	}
	writeJSON(w, res)
}

var planDigestPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

type syncRunRequest struct {
	Nodes  []string `json:"nodes"`
	Digest string   `json:"digest"`
}

type syncRunResult struct {
	Node    string `json:"node"`
	OK      bool   `json:"ok"`
	Copied  int    `json:"copied"`
	Message string `json:"message"`
}

func decodeSyncRun(w http.ResponseWriter, r *http.Request) (syncRunRequest, bool) {
	var req syncRunRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	bad := err != nil || dec.Decode(&req) != nil || dec.More() || !planDigestPattern.MatchString(req.Digest) ||
		len(req.Nodes) == 0 || len(req.Nodes) > 64
	seen := map[string]bool{}
	for _, n := range req.Nodes {
		if !nodeNamePattern.MatchString(n) || seen[n] {
			bad = true
		}
		seen[n] = true
	}
	if bad {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// PostSyncRun copies to the chosen nodes what the plan says, then verifies every node again.
func (h *Handler) PostSyncRun(w http.ResponseWriter, r *http.Request) {
	idx, cl, client, ok := h.syncPlanSetup(w, r)
	if !ok {
		return
	}
	req, ok := decodeSyncRun(w, r)
	if !ok {
		return
	}
	lock := syncOpLock(idx)
	if !lock.TryLock() {
		w.Header().Set("Retry-After", "10")
		writeError(w, errors.New("un'altra operazione di sincronizzazione e' in corso"), http.StatusTooManyRequests)
		return
	}
	defer lock.Unlock()
	plan, err := buildSyncPlan(r.Context(), client, h.ssh, h.snippets, cl)
	if err != nil {
		log.Printf("snippetsync: plan cluster %d: %v", idx, err)
		writeError(w, errors.New("impossibile preparare la sincronizzazione (dettagli nel log del server)"), http.StatusBadGateway)
		return
	}
	if plan.Digest != req.Digest {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"reason": "plan_changed",
			"error": "lo stato dei nodi e' cambiato dall'anteprima: nessun file copiato, ricarica l'anteprima"})
		return
	}
	byName := map[string]syncNodePlan{}
	for _, n := range plan.Nodes {
		byName[n.Node] = n
	}
	results := []syncRunResult{}
	allOK := true
	for _, name := range req.Nodes {
		n, found := byName[name]
		var res syncRunResult
		if !found {
			res = syncRunResult{Node: name, Message: "Nodo non trovato nel cluster."}
		} else {
			res = h.syncOneNode(r.Context(), plan.bundle, n)
		}
		allOK = allOK && res.OK
		results = append(results, res)
		log.Printf("snippetsync: cluster %d node %s: ok=%v copied=%d %s", idx, name, res.OK, res.Copied, res.Message)
	}
	forgetSyncAccess()
	writeJSON(w, map[string]any{"ok": allOK, "results": results})
}

func (h *Handler) syncOneNode(ctx context.Context, bundle *snippetsync.Bundle, n syncNodePlan) syncRunResult {
	res := syncRunResult{Node: n.Node}
	switch {
	case !n.Ready:
		res.Message = "Non pronto: " + n.Message
		return res
	case n.Aligned:
		res.OK, res.Message = true, "Gia' allineato: nessun file copiato."
		return res
	}
	for _, dir := range snippetsync.DirsOf(n.todo) {
		stage, err := snippetsync.Stage(bundle, dir, n.todo)
		if err != nil {
			log.Printf("snippetsync: stage %s for %s: %v", dir, n.Node, err)
			res.Message = "Preparazione dei file non riuscita."
			return res
		}
		if stage == "" {
			continue
		}
		err = h.ssh.Push(ctx, n.Address, stage, dir)
		os.RemoveAll(stage)
		if err != nil {
			_, msg := syncStateOf(err)
			log.Printf("snippetsync: push %s to %s (%s): %v", dir, n.Node, n.Address, err)
			res.Message = "Copia di " + dir + " non riuscita: " + msg
			return res
		}
	}
	res.Copied = len(n.todo)
	after := checkSyncNodeListing(ctx, h.ssh, syncNode{Name: n.Node, Address: n.Address, Online: true})
	if after.state != "ok" {
		res.Message = "Copiato, ma la verifica non e' riuscita: " + after.message
		return res
	}
	check := snippetsync.Compare(bundle, after.listing)
	if !check.Aligned() {
		res.Message = fmt.Sprintf("Copiato, ma %d file risultano ancora diversi o mancanti.", len(check.Missing)+len(check.Different))
		return res
	}
	res.OK, res.Message = true, fmt.Sprintf("Allineato: %d file copiati e verificati.", res.Copied)
	return res
}

// ── shared with sync-access: pmgr-list on one node, with the same states ──

type nodeListing struct {
	state, message string
	listing        nodessh.Listing
}

func checkSyncNodeListing(ctx context.Context, ssh nodeSSH, n syncNode) nodeListing {
	st, l := checkSyncNode(ctx, ssh, n, true)
	return nodeListing{state: st.State, message: st.Message, listing: l}
}
