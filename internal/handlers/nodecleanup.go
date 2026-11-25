package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/config"
)

const (
	maxCleanupGuests = 2000
	maxCleanupFiles  = 200
)

type nodeCleanupAPI interface {
	clusterStatusAPI
	GetClusterResources(string) (json.RawMessage, error)
	GetVMConfig(string, int) (json.RawMessage, error)
	GetContainerConfig(string, int) (json.RawMessage, error)
}

type cleanupReferences struct {
	Known         bool `json:"known"`
	GuestsRead    int  `json:"guests_read"`
	ConfigsFailed int  `json:"configs_failed"`
	byName        map[string]cleanupUsage
}

type cleanupUsage struct {
	guests []string
	count  int
}

type cleanupFile struct {
	Path        string   `json:"path"`
	Class       string   `json:"class"`
	UsedBy      []string `json:"used_by"`
	UsedByCount int      `json:"used_by_count"`
}

type cleanupNode struct {
	Node      string        `json:"node"`
	State     string        `json:"state"`
	Message   string        `json:"message"`
	Files     []cleanupFile `json:"files"`
	Removable int           `json:"removable"`
	InUse     int           `json:"in_use"`
	Unknown   int           `json:"unknown"`
	Command   string        `json:"command"`
	Undo      string        `json:"undo"`
}

type nodeCleanupResult struct {
	Cluster     string            `json:"cluster"`
	GeneratedAt int64             `json:"generated_at"`
	References  cleanupReferences `json:"references"`
	Nodes       []cleanupNode     `json:"nodes"`
	Manual      []string          `json:"manual"`
}

// This is the nodessh entry allowlist. Check it again before compiling root commands,
// even when a test double or another listing provider bypasses ParseListing.
var cleanupPathPattern = regexp.MustCompile(`^(snippets|pmgr-scripts)/[A-Za-z0-9._/@+-]{1,240}$`)

func validCleanupPath(path string) bool {
	return cleanupPathPattern.MatchString(path) && !strings.Contains(path, "..")
}

// A malformed config cannot prove absence of a reference. Accept longer and nested
// names too: the delivery listing permits them, unlike the template creation parser.
func cleanupSnippetNames(raw json.RawMessage, err error) ([]string, bool) {
	var cfg map[string]json.RawMessage
	if !readinessRead(raw, err, &cfg) || cfg == nil {
		return nil, false
	}
	names := []string{}
	seen := map[string]bool{}
	// hookscript (QEMU and LXC): "<storage>:snippets/<name>", run by Proxmox at every start.
	if value, present := cfg["hookscript"]; present {
		var hook string
		if string(value) == "null" || json.Unmarshal(value, &hook) != nil || len(hook) > 4096 {
			return nil, false
		}
		if hook != "" {
			storage, path, ok := strings.Cut(hook, ":")
			if !ok || !storageNamePattern.MatchString(storage) || !validCleanupPath(path) || !strings.HasPrefix(path, "snippets/") {
				return nil, false
			}
			name := strings.TrimPrefix(path, "snippets/")
			seen[name] = true
			names = append(names, name)
		}
	}
	value, present := cfg["cicustom"]
	if !present {
		return names, true
	}
	var custom string
	if string(value) == "null" || json.Unmarshal(value, &custom) != nil || len(custom) > 65536 {
		return nil, false
	}
	if custom == "" {
		return names, true
	}
	for _, item := range strings.Split(custom, ",") {
		kind, volume, ok := strings.Cut(strings.TrimSpace(item), "=")
		storage, path, hasStorage := strings.Cut(volume, ":")
		if !ok || !hasStorage || !storageNamePattern.MatchString(storage) ||
			(kind != "meta" && kind != "network" && kind != "user" && kind != "vendor") || !validCleanupPath(path) || !strings.HasPrefix(path, "snippets/") {
			return nil, false
		}
		name := strings.TrimPrefix(path, "snippets/")
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, true
}

func readCleanupReferences(ctx context.Context, c nodeCleanupAPI) cleanupReferences {
	refs := cleanupReferences{Known: true, byName: map[string]cleanupUsage{}}
	raw, err := c.GetClusterResources("vm")
	rows, ok := readNodeList[removalGuest](raw, err)
	if !ok {
		refs.Known = false
		return refs
	}
	guests := []removalGuest{}
	seen := map[int]bool{}
	for _, g := range rows {
		if (g.Type != "qemu" && g.Type != "lxc") || g.VMID <= 0 || g.VMID > 999999999 || !nodeNamePattern.MatchString(g.Node) || len(g.Name) > 256 || seen[g.VMID] {
			refs.Known = false
			refs.ConfigsFailed++
			continue
		}
		seen[g.VMID] = true
		guests = append(guests, g)
	}
	sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
	if len(guests) > maxCleanupGuests {
		refs.ConfigsFailed += len(guests) - maxCleanupGuests
		guests = guests[:maxCleanupGuests]
		refs.Known = false
	}
	type result struct {
		names []string
		ok    bool
	}
	results := make([]result, len(guests))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				g := guests[i]
				var raw json.RawMessage
				var err error
				if g.Type == "lxc" {
					raw, err = c.GetContainerConfig(g.Node, g.VMID)
				} else {
					raw, err = c.GetVMConfig(g.Node, g.VMID)
				}
				results[i].names, results[i].ok = cleanupSnippetNames(raw, err)
			}
		})
	}
	for i := range guests {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for i, r := range results {
		if !r.ok {
			refs.ConfigsFailed++
			refs.Known = false
			continue
		}
		refs.GuestsRead++
		for _, name := range r.names {
			usage := refs.byName[name]
			usage.count++
			if len(usage.guests) < 20 {
				usage.guests = append(usage.guests, fmt.Sprintf("%d %s", guests[i].VMID, guests[i].Name))
			}
			refs.byName[name] = usage
		}
	}
	return refs
}

func cleanupNodePlan(n syncNodePlan, refs cleanupReferences, stamp string) cleanupNode {
	out := cleanupNode{Node: n.Node, State: n.State, Message: n.Message, Files: []cleanupFile{}}
	if !n.Ready {
		return out
	}
	paths := []string{}
	ignored := 0
	for _, path := range n.extra {
		if !validCleanupPath(path) {
			ignored++
			continue
		}
		f := cleanupFile{Path: path, Class: "removable", UsedBy: []string{}}
		if strings.HasPrefix(path, "snippets/") {
			users := refs.byName[strings.TrimPrefix(path, "snippets/")]
			switch {
			case !refs.Known:
				f.Class = "unknown"
			case users.count > 0:
				f.Class, f.UsedByCount = "in_use", users.count
				f.UsedBy = append(f.UsedBy, users.guests...)
			}
		}
		switch f.Class {
		case "removable":
			out.Removable++
			if len(paths) < maxCleanupFiles {
				paths = append(paths, path)
			}
		case "in_use":
			out.InUse++
		case "unknown":
			out.Unknown++
		}
		out.Files = append(out.Files, f)
	}
	if ignored > 0 {
		out.Message += fmt.Sprintf(" %d percorsi non validi esclusi dalla pulizia.", ignored)
	}
	if out.Removable > maxCleanupFiles {
		out.Message += fmt.Sprintf(" Il comando include solo %d file; gli altri %d compariranno dopo un ricontrollo.", maxCleanupFiles, out.Removable-maxCleanupFiles)
	}
	if len(paths) > 0 {
		archive := "/root/pmgr-backup/cleanup-" + stamp + ".tar.gz"
		relative, absolute := []string{}, []string{}
		for _, path := range paths {
			// The allowlist excludes quotes and all shell syntax; every operand stays quoted.
			relative = append(relative, "'"+path+"'")
			absolute = append(absolute, "'/var/lib/vz/"+path+"'")
		}
		out.Command = "umask 077 && mkdir -p /root/pmgr-backup && tar -C /var/lib/vz -czf " + archive + " -- " + strings.Join(relative, " ") + " && rm -f -- " + strings.Join(absolute, " ")
		out.Undo = "tar -C /var/lib/vz -xzf " + archive
	}
	return out
}

func cleanupManual() []string {
	return []string{
		"Verifica e pulisci a mano le vecchie copie degli script in /root: /root/cloud-images, /root/old-scripts-backup-*, rsync_snippets.sh e /root/pmgr-scripts.",
		"Verifica e dismetti la vecchia sincronizzazione nodo-nodo: /var/lib/pmgr-sync/sync.conf e la chiave sorgente pmgr-sync sul precedente nodo sorgente.",
		"Verifica e rimuovi le chiavi di sviluppo non piu' necessarie dagli accessi autorizzati.",
		"CONSERVA /etc/pmgr/cluster.conf, /etc/pmgr/ssh_keys.pub e /etc/pmgr/template-password: servono alla creazione dei template.",
		"CONSERVA /var/lib/pmgr-sync: e' la home dell'utente pmgr-sync.",
	}
}

// GetNodeCleanup reads only. Root commands are text for a person to run on the named node.
func (h *Handler) GetNodeCleanup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	idx, cl, client, ok := h.syncPlanSetup(w, r)
	if !ok {
		return
	}
	c, supported := client.(nodeCleanupAPI)
	if !supported {
		writeError(w, errors.New("impossibile preparare il controllo di pulizia"), http.StatusBadGateway)
		return
	}
	h.serveNodeCleanup(w, r, idx, cl, c)
}

func (h *Handler) serveNodeCleanup(w http.ResponseWriter, r *http.Request, idx int, cl config.ClusterConfig, c nodeCleanupAPI) {
	w.Header().Set("Cache-Control", "no-store")
	lock := syncOpLock(idx)
	if !lock.TryLock() {
		w.Header().Set("Retry-After", "10")
		writeError(w, errors.New("un'altra operazione di sincronizzazione e' in corso"), http.StatusTooManyRequests)
		return
	}
	defer lock.Unlock()
	plan, err := buildSyncPlan(r.Context(), c, h.ssh, h.snippets, cl)
	if err != nil {
		writeError(w, errors.New("impossibile preparare il controllo di pulizia"), http.StatusBadGateway)
		return
	}
	refs := readCleanupReferences(r.Context(), c)
	now := time.Now()
	res := nodeCleanupResult{Cluster: cl.Label, GeneratedAt: now.Unix(), References: refs, Nodes: []cleanupNode{}, Manual: cleanupManual()}
	for _, n := range plan.Nodes {
		res.Nodes = append(res.Nodes, cleanupNodePlan(n, refs, now.Format("20060102-150405")))
	}
	writeJSON(w, res)
}
