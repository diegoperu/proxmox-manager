package handlers

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

// Daily inventory snapshot (D28). One snapshot per cluster per day, taken by the first check of the day.
// It stores counts and assignments only: no IP, no VM names, no guest data, no metrics.

const (
	snapshotEvery   = time.Hour
	snapshotStartIn = 30 * time.Second
	otherGroup      = "altro"
)

var groupPattern = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// vmGroup: the research group of a VM, by internal naming convention (D27): the prefix of the name before the first
// "-" (`dinitto-vm-gpu-1` -> `dinitto`). Anything that does not look like a prefix is "altro".
func vmGroup(name string) string {
	prefix, _, found := strings.Cut(strings.ToLower(strings.TrimSpace(name)), "-")
	if !found || !groupPattern.MatchString(prefix) {
		return otherGroup
	}
	return prefix
}

type snapshotAPI interface {
	gpuAPI
	GetClusterResources(resType string) (json.RawMessage, error)
}

var _ snapshotAPI = (*api.Client)(nil)

func isTemplateRes(r map[string]interface{}) bool {
	if parseBoolish(r["template"]) {
		return true
	}
	name, _ := r["name"].(string)
	n := strings.ToLower(name)
	return strings.Contains(n, "template") || strings.Contains(n, "cloudinit")
}

func intField(r map[string]interface{}, k string) int64 {
	v, _ := r[k].(float64)
	return i64(v)
}

// BuildSnapshot is pure. `gpu` may be nil (GPU inventory unreadable): no GPU rows and GPUKnown=false.
func BuildSnapshot(resources json.RawMessage, gpu *GPUInventory, cluster string, now time.Time) (history.Snapshot, error) {
	snap := history.Snapshot{Day: now.Format("2006-01-02"), Cluster: cluster, TakenAt: now, GPUKnown: gpu != nil}
	var res []map[string]interface{}
	if err := json.Unmarshal(resources, &res); err != nil {
		return snap, err
	}
	type key struct{ node, grp, kind string }
	groups := map[key]*history.GroupRow{}
	for _, r := range res {
		typ, _ := r["type"].(string)
		node, _ := r["node"].(string)
		if !nodeNamePattern.MatchString(node) {
			continue
		}
		switch typ {
		case "node":
			status, _ := r["status"].(string)
			snap.Nodes = append(snap.Nodes, history.NodeRow{Node: node, Online: status == "online", Cores: int(intField(r, "maxcpu")), MemBytes: intField(r, "maxmem")})
		case "qemu", "lxc":
			if isTemplateRes(r) {
				continue
			}
			name, _ := r["name"].(string)
			k := key{node, vmGroup(name), typ}
			g := groups[k]
			if g == nil {
				g = &history.GroupRow{Node: node, Group: k.grp, Kind: typ}
				groups[k] = g
			}
			g.VMs++
			if st, _ := r["status"].(string); st == "running" {
				g.Running++
			}
			g.Cores += int(intField(r, "maxcpu"))
			g.MemBytes += intField(r, "maxmem")
			g.DiskBytes += intField(r, "maxdisk")
		}
	}
	for _, g := range groups {
		snap.Groups = append(snap.Groups, *g)
	}
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Node < snap.Nodes[j].Node })
	sort.Slice(snap.Groups, func(i, j int) bool {
		a, b := snap.Groups[i], snap.Groups[j]
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Kind < b.Kind
	})
	if gpu != nil {
		type gk struct{ node, model, grp string }
		cards := map[gk]int{}
		for _, n := range gpu.Nodes {
			for _, c := range n.Cards {
				grp := "" // free
				if len(c.Uses) > 0 {
					grp = vmGroup(c.Uses[0].Name) // uses are sorted by VMID: stable
				}
				cards[gk{n.Node, clampText(c.Name, 80), grp}]++
			}
		}
		for k, v := range cards {
			snap.GPUs = append(snap.GPUs, history.GPURow{Node: k.node, Model: k.model, Group: k.grp, Cards: v})
		}
		sort.Slice(snap.GPUs, func(i, j int) bool {
			a, b := snap.GPUs[i], snap.GPUs[j]
			if a.Node != b.Node {
				return a.Node < b.Node
			}
			if a.Model != b.Model {
				return a.Model < b.Model
			}
			return a.Group < b.Group
		})
	}
	return snap, nil
}

// collectSnapshot reads one cluster and builds its snapshot. A GPU inventory failure does not lose the day.
func collectSnapshot(c snapshotAPI, cluster string, now time.Time) (history.Snapshot, error) {
	raw, err := c.GetClusterResources("")
	if err != nil {
		return history.Snapshot{}, err
	}
	var gpu *GPUInventory
	if inv, err := fetchGPUInventory(c, now); err != nil {
		log.Printf("snapshot %q: inventario GPU non letto: %v", cluster, err)
	} else if len(inv.Partial) == 0 {
		gpu = &inv // partial reads would undercount: treated as unknown
	} else {
		log.Printf("snapshot %q: inventario GPU parziale (%d parti), GPU non registrate", cluster, len(inv.Partial))
	}
	return BuildSnapshot(raw, gpu, cluster, now)
}

// takeMissingSnapshots takes today's snapshot for every cluster that lacks one.
func takeMissingSnapshots(store *history.Store, clusters []config.ClusterConfig, now time.Time, newClient func(config.ClusterConfig) snapshotAPI) {
	day := now.Format("2006-01-02")
	for _, cl := range clusters {
		if cl.URL == "" || cl.Label == "" {
			continue
		}
		if has, err := store.Has(day, cl.Label); err != nil {
			log.Printf("snapshot %q: %v", cl.Label, err)
			continue
		} else if has {
			continue
		}
		snap, err := collectSnapshot(newClient(cl), cl.Label, now)
		if err != nil {
			log.Printf("snapshot %q: lettura non riuscita, riprovo tra un'ora: %v", cl.Label, err)
			continue
		}
		if err := store.Save(snap); err != nil {
			log.Printf("snapshot %q: salvataggio non riuscito: %v", cl.Label, err)
			continue
		}
		log.Printf("snapshot %q %s: %d nodi, %d righe di gruppo, GPU %v (%d righe)", cl.Label, day, len(snap.Nodes), len(snap.Groups), snap.GPUKnown, len(snap.GPUs))
	}
	if err := store.Prune(now); err != nil {
		log.Printf("snapshot: pulizia non riuscita: %v", err)
	}
}

// StartSnapshots runs the daily snapshot loop until ctx is cancelled: first check shortly after start, then hourly.
func StartSnapshots(ctx context.Context, store *history.Store) {
	newClient := func(cl config.ClusterConfig) snapshotAPI { return api.NewClient(cl.URL, cl.APIToken) }
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("snapshot: interrotto da un errore interno: %v", r)
			}
		}()
		timer := time.NewTimer(snapshotStartIn)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				takeMissingSnapshots(store, config.Get().Clusters, time.Now(), newClient)
				timer.Reset(snapshotEvery)
			}
		}
	}()
}
