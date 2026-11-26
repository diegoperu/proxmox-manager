package handlers

// CPU architecture of the nodes (D168): a guest runs only on the architecture it was created for, so the
// balance, the new-VM advice and the manual migration never cross it, and the page shows which architecture the
// selected cluster has when the manager holds clusters of different architectures.
//
// The architecture comes from /nodes/{n}/status, current-kernel.machine (x86_64 or aarch64), the same call the
// other node checks already make. Unknown values are kept unknown (""): never guessed.

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/config"
)

const (
	archAMD64 = "amd64"
	archARM64 = "arm64"
	archMixed = "mixed"
)

// archFromMachine maps the kernel machine name to the Debian architecture name; anything else is unknown.
func archFromMachine(machine string) string {
	switch machine {
	case "x86_64":
		return archAMD64
	case "aarch64":
		return archARM64
	}
	return ""
}

// nodeStatusArch reads the architecture from a /nodes/{n}/status answer ("" when absent or unknown).
func nodeStatusArch(raw json.RawMessage) string {
	var st struct {
		Kernel struct {
			Machine string `json:"machine"`
		} `json:"current-kernel"`
	}
	if json.Unmarshal(raw, &st) != nil {
		return ""
	}
	return archFromMachine(st.Kernel.Machine)
}

type nodeStatusReader interface {
	GetNodeStatus(node string) (json.RawMessage, error)
}

// readNodeArch reads one node's architecture ("" on any error).
func readNodeArch(c nodeStatusReader, node string) string {
	if !nodeNamePattern.MatchString(node) {
		return ""
	}
	raw, err := c.GetNodeStatus(node)
	if err != nil {
		return ""
	}
	return nodeStatusArch(raw)
}

// clusterArchOf summarises the architectures of the online nodes: one value, "mixed", or "" when none is known.
// A node whose architecture is unknown does not make the cluster mixed: it is reported apart (Complete=false).
func clusterArchOf(nodes map[string]string) (arch string, complete bool) {
	complete = true
	seen := map[string]bool{}
	for _, a := range nodes {
		if a == "" {
			complete = false
			continue
		}
		seen[a] = true
	}
	switch len(seen) {
	case 0:
		return "", complete
	case 1:
		for a := range seen {
			return a, complete
		}
	}
	return archMixed, complete
}

// ── GET /api/clusters/arch: architecture of every configured cluster, for the label in the page ──

type clusterArchInfo struct {
	Index    int               `json:"index"`
	Arch     string            `json:"arch"`     // amd64 | arm64 | mixed | "" (not read)
	Complete bool              `json:"complete"` // every online node read
	Nodes    map[string]string `json:"nodes"`
}

type clusterArchEntry struct {
	key  string // URL of the cluster: a changed URL is another cluster
	info clusterArchInfo
	at   time.Time
}

var (
	clusterArchMu    sync.Mutex
	clusterArchCache = map[int]clusterArchEntry{}
)

// The architecture of a node does not change while it runs: a long cache, only for complete answers.
const clusterArchTTL = 30 * time.Minute

var clusterArchClient = func(cl config.ClusterConfig) clusterNodesAPI {
	return api.NewClientCA(cl.URL, cl.APIToken, cl.TLSCA)
}

type clusterNodesAPI interface {
	nodeStatusReader
	Get(path string) (json.RawMessage, error)
}

func readClusterArch(c clusterNodesAPI, idx int) clusterArchInfo {
	info := clusterArchInfo{Index: idx, Nodes: map[string]string{}}
	raw, err := c.Get("/nodes")
	if err != nil {
		return info
	}
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &nodes) != nil {
		return info
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	started := 0
	for _, n := range nodes {
		if n.Status != "online" || !nodeNamePattern.MatchString(n.Node) || started >= maxNodesArch {
			continue
		}
		started++
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			a := readNodeArch(c, name)
			mu.Lock()
			info.Nodes[name] = a
			mu.Unlock()
		}(n.Node)
	}
	wg.Wait()
	info.Arch, info.Complete = clusterArchOf(info.Nodes)
	if len(info.Nodes) == 0 {
		info.Complete = false
	}
	return info
}

const maxNodesArch = 64

func (h *Handler) GetClustersArch(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	out := make([]clusterArchInfo, len(cfg.Clusters))
	var wg sync.WaitGroup
	now := time.Now()
	for i, cl := range cfg.Clusters {
		clusterArchMu.Lock()
		e, ok := clusterArchCache[i]
		clusterArchMu.Unlock()
		if ok && e.key == cl.URL && now.Sub(e.at) < clusterArchTTL {
			out[i] = e.info
			continue
		}
		wg.Add(1)
		go func(i int, cl config.ClusterConfig) {
			defer wg.Done()
			done := make(chan clusterArchInfo, 1)
			go func() {
				defer func() {
					if v := recover(); v != nil {
						log.Printf("arch: cluster %d: %v", i, v)
						done <- clusterArchInfo{Index: i, Nodes: map[string]string{}}
					}
				}()
				done <- readClusterArch(clusterArchClient(cl), i)
			}()
			select {
			case info := <-done:
				out[i] = info
				if info.Complete {
					clusterArchMu.Lock()
					clusterArchCache[i] = clusterArchEntry{key: cl.URL, info: info, at: time.Now()}
					clusterArchMu.Unlock()
				}
			case <-time.After(8 * time.Second):
				// an unreachable cluster must not hold the page: unknown this time, read again next time
				out[i] = clusterArchInfo{Index: i, Nodes: map[string]string{}}
			}
		}(i, cl)
	}
	wg.Wait()
	for i := range out {
		out[i].Index = i
		if out[i].Nodes == nil {
			out[i].Nodes = map[string]string{}
		}
	}
	writeJSON(w, out)
}

// sortedArchNodes is used in messages: "node (arch)" in a stable order.
func sortedArchNodes(nodes map[string]string) []string {
	out := make([]string, 0, len(nodes))
	for n, a := range nodes {
		if a == "" {
			a = "sconosciuta"
		}
		out = append(out, n+" ("+archLabel(a)+")")
	}
	sort.Strings(out)
	return out
}

// archLabel is the text shown to people.
func archLabel(a string) string {
	switch a {
	case archAMD64:
		return "x86-64"
	case archARM64:
		return "ARM64"
	case archMixed:
		return "misto"
	}
	return a
}

func archLabelOrUnknown(a string) string {
	if a == "" {
		return "sconosciuta"
	}
	return archLabel(a)
}
