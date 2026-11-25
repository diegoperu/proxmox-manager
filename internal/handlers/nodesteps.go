package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/config"
)

// nodeStep keeps the observed result even when an earlier step locks the action.
// Only the first unfinished step is actionable. No command is executed by the manager.
type nodeStep struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	CheckStatus string   `json:"check_status"`
	Detail      string   `json:"detail"`
	Commands    []string `json:"commands"`
	Items       []string `json:"items"`
	Link        string   `json:"link,omitempty"`
}

func newNodeStep(id, title, detail string) nodeStep {
	return nodeStep{ID: id, Title: title, Status: "ok", CheckStatus: "ok", Detail: detail, Commands: []string{}, Items: []string{}}
}
func unknownNodeStep(s *nodeStep, detail string) {
	s.Status, s.Detail, s.Commands = "unknown", detail, []string{}
}
func lockNodeSteps(steps []nodeStep) {
	locked := false
	for i := range steps {
		steps[i].CheckStatus = steps[i].Status
		if locked {
			steps[i].Status = "blocked"
		}
		if steps[i].CheckStatus != "ok" {
			locked = true
		}
	}
}

// Lists must be arrays, never null, and bounded. A malformed answer cannot prove absence.
func readNodeList[T any](raw json.RawMessage, err error) ([]T, bool) {
	var list []T
	if !readinessRead(raw, err, &list) || list == nil || len(list) > 10000 {
		return nil, false
	}
	return list, true
}
func nodeFlag(raw json.RawMessage) (bool, bool) {
	switch string(raw) {
	case "1", "true", `"1"`:
		return true, true
	case "0", "false", `"0"`:
		return false, true
	}
	return false, false
}
func stepClusterNodes(c clusterStatusAPI) ([]syncNode, error) {
	raw, err := c.GetClusterStatus()
	rows, ok := readNodeList[struct {
		Type   string          `json:"type"`
		Name   string          `json:"name"`
		IP     string          `json:"ip"`
		Online json.RawMessage `json:"online"`
	}](raw, err)
	if !ok {
		return nil, errors.New("stato del cluster non leggibile")
	}
	nodes := []syncNode{}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Type == "cluster" {
			continue
		}
		if r.Type != "node" || !nodeNamePattern.MatchString(r.Name) || seen[r.Name] || len(nodes) >= 256 {
			return nil, errors.New("elenco nodi non valido")
		}
		online, valid := nodeFlag(r.Online)
		if !valid {
			return nil, errors.New("stato dei nodi non leggibile")
		}
		seen[r.Name] = true
		nodes = append(nodes, syncNode{Name: r.Name, Address: r.IP, Online: online})
	}
	sort.Slice(nodes, func(i, j int) bool { return naturalLess(nodes[i].Name, nodes[j].Name) })
	return nodes, nil
}

var nodeStepOps sync.Map

func nodeStepLock(idx int) *sync.Mutex {
	v, _ := nodeStepOps.LoadOrStore(idx, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func (h *Handler) nodeStepsSetup(w http.ResponseWriter, r *http.Request) (int, config.ClusterConfig, *api.Client, bool) {
	w.Header().Set("Cache-Control", "no-store")
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return 0, config.ClusterConfig{}, nil, false
	}
	cl, err := config.GetCluster(idx)
	client, cerr := h.getClientFor(idx)
	if err != nil || cerr != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return 0, cl, nil, false
	}
	return idx, cl, client, true
}
func beginNodeSteps(w http.ResponseWriter, idx int) (*sync.Mutex, bool) {
	lock := nodeStepLock(idx)
	if !lock.TryLock() {
		w.Header().Set("Retry-After", "10")
		writeError(w, errors.New("controllo gia' in corso"), http.StatusTooManyRequests)
		return nil, false
	}
	return lock, true
}
