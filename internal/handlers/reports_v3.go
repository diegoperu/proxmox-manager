package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

type serviceV3Settings struct {
	Report *config.ReportSettings
	Models map[string]config.GPUModel
}

func serviceV3SettingsKey(s serviceV3Settings) string {
	// Settings are validated before persistence. JSON sorts map keys, giving one cache
	// identity for prices, thresholds, outages and GPU corrections without retaining text.
	b, err := json.Marshal(s)
	if err != nil {
		return "invalid"
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

// All Proxmox calls in this adapter are GETs. Phases run sequentially, each with at
// most reportWorkers calls in flight; nested inventory loading uses the same limit.
func buildServiceV3(c reportAPI, hist *history.Store, cluster string, p reportParams, now time.Time, options serviceV3Settings) (serviceV3, error) {
	var out serviceV3
	// v3 describes the cluster, independent of technical guest/node filters.
	p.Kind, p.Group, p.Query, p.Nodes = "all", "", "", nil
	p.Trend = v3TrendThresholds(options.Report)
	res, err := loadResources(c, p)
	if err != nil {
		return out, err
	}
	if len(res.Guests) > maxReportGuests || len(res.Nodes) > 64 {
		return out, errors.New(errTooManyGuests)
	}
	// Unlike serviceTrendParams this keeps the exact period, including its partial
	// first month and periods over twelve calendar months. The pure sizing pass trims to six.
	trend, err := buildTrendReport(c, hist, cluster, p, now)
	if err != nil {
		return out, err
	}
	nodes, tasks, nodesKnown, tasksKnown := loadV3NodeData(c, res.Nodes, p, now)
	out.Growth, out.VMGrowthPct, out.MemGrowthPct = calculateV3Growth(trend.Rows, nodes, p.From, p.To)
	out.Concentration = calculateV3Concentration(res.Guests)
	out.Sizing = calculateV3Sizing(trend.Rows, p.From, p.To, p.Trend)
	var interruptions []config.Interruption
	if options.Report != nil {
		interruptions = options.Report.Interruptions
	}
	out.Availability = calculateV3Availability(nodes, interruptions, p.From, p.To)
	if !nodesKnown {
		// A failed read is different from an observed missing RRD sample. Never
		// advertise partial node data as the whole cluster's availability or RAM use.
		out.Availability.TotalPct, out.Availability.NetPct = nil, nil
		for i := range out.Availability.Months {
			out.Availability.Months[i].TotalPct, out.Availability.Months[i].NetPct = nil, nil
		}
		for i := range out.Growth {
			out.Growth[i].NodeMemUsedPct = nil
		}
	}

	inv := loadV3GPUInventory(c, res.Nodes, now, options.Models)
	var gpuRows []history.GPUUseDay
	if hist != nil {
		gpuRows, err = hist.GPUUseDays(cluster, p.From.Format("2006-01-02"), p.To.Format("2006-01-02"))
		if err != nil {
			return out, err
		}
		for _, row := range gpuRows {
			if err := history.ValidGPUUse(row); err != nil {
				return out, err
			}
		}
	}
	out.GPUClasses = calculateV3GPUClasses(inv, gpuRows, hist != nil, options.Models, p.From, p.To)
	guestIDs := make([]int, 0, len(res.Guests))
	for _, guest := range res.Guests {
		guestIDs = append(guestIDs, guest.VMID)
	}
	backups, readable := loadV3BackupCopies(c, res.Nodes)
	out.Backups = calculateV3Backups(tasks, guestIDs, backups, readable, p.From, p.To, now)
	if !tasksKnown {
		out.Backups.Nights, out.Backups.Causes = nil, nil
	}
	if options.Report.EconomicEnabled() {
		guests, days, err := loadV3EconomicGuests(c, hist, cluster, res.Guests, inv, options, now)
		if err != nil {
			return out, err
		}
		out.Economic = calculateV3Economic(options.Report, guests, days)
	}
	return out, nil
}

func loadV3NodeData(c reportAPI, nodes []resNode, p reportParams, now time.Time) ([]rrdSeries, []v3BackupTask, bool, bool) {
	series := make([]rrdSeries, len(nodes))
	lists := make([][]v3BackupTask, len(nodes))
	known := make([]bool, len(nodes))
	nodeKnown := make([]bool, len(nodes))
	tf, _, err := pickTimeframe(now, p.From)
	if err != nil {
		return series, nil, false, false
	}
	jobs := []func(){}
	for i, node := range nodes {
		i, node := i, node
		jobs = append(jobs, func() {
			// Read even currently offline nodes: they may have history during the period.
			if raw, err := c.GetNodeRRDcf(node.Name, tf, "AVERAGE"); err == nil {
				if s, err := parseRRD(raw, "cpu", "memused", "memtotal"); err == nil && strings.TrimSpace(string(raw)) != "null" {
					series[i] = s // retain samples before From to find the node's real start
					nodeKnown[i] = true
				}
			}
			raw, err := c.GetNodeTasks(node.Name, maxTaskLimit)
			if err != nil {
				return
			}
			var tasks []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Start  int64  `json:"starttime"`
			}
			if json.Unmarshal(raw, &tasks) != nil || len(tasks) > maxTaskLimit || string(raw) == "null" {
				return
			}
			known[i] = true
			for _, task := range tasks {
				if task.Type == "vzdump" && task.Start > 0 && len(task.Status) <= 4096 {
					lists[i] = append(lists[i], v3BackupTask{Time: task.Start, Status: task.Status})
				}
			}
		})
	}
	runLimited(jobs)
	allKnown := true
	allNodesKnown := true
	tasks := []v3BackupTask{}
	for i := range nodes {
		allKnown = allKnown && known[i]
		allNodesKnown = allNodesKnown && nodeKnown[i]
		tasks = append(tasks, lists[i]...)
	}
	return series, tasks, allNodesKnown, allKnown
}

func loadV3GPUInventory(c reportAPI, nodes []resNode, now time.Time, models map[string]config.GPUModel) *GPUInventory {
	raw, err := c.GetPCIMappings()
	if err != nil {
		return nil // missing mappings cannot prove that a physical card is free
	}
	var check []json.RawMessage
	if json.Unmarshal(raw, &check) != nil || len(check) > maxGPUVMs || string(raw) == "null" {
		return nil
	}
	mappings := ParsePCIMappingEntries(raw)
	inputs := make([]GPUNodeInput, len(nodes))
	partial := []string{}
	sem := make(chan struct{}, reportWorkers)
	// Node phases stay sequential: loadGPUNodeVMs already parallelizes config reads.
	for i, node := range nodes {
		inputs[i] = GPUNodeInput{Node: node.Name, Online: node.Online}
		if !node.Online {
			partial = append(partial, node.Name)
			continue
		}
		raw, err := c.GetNodePCI(node.Name)
		if err != nil {
			partial = append(partial, node.Name)
			continue
		}
		var pciList []json.RawMessage
		if json.Unmarshal(raw, &pciList) != nil || len(pciList) > maxGPUVMs || string(raw) == "null" {
			partial = append(partial, node.Name)
			continue
		}
		inputs[i].PCI = ParsePCIList(raw)
		vms, failed, err := loadGPUNodeVMs(v3GPUReadAPI{c}, node.Name, sem)
		if err != nil || len(failed) > 0 {
			partial = append(partial, node.Name)
		}
		inputs[i].VMs = vms
	}
	inv := BuildGPUInventory(inputs, mappings, now)
	inv.Partial = partial
	inv = expandV3GPUAssignments(inv, mappings, models)
	return &inv
}

// The existing inventory loader tolerates null for legacy consumers. v3 cannot use
// that tolerance as proof of zero assignments or free cards, so validate its read boundary.
type v3GPUReadAPI struct{ gpuAPI }

func (c v3GPUReadAPI) GetVMs(node string) (json.RawMessage, error) {
	raw, err := c.gpuAPI.GetVMs(node)
	if err != nil {
		return nil, err
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil || list == nil || len(list) > maxGPUVMs {
		return nil, errors.New("report v3: elenco VM GPU non valido")
	}
	return raw, nil
}

func (c v3GPUReadAPI) GetVMConfig(node string, vmid int) (json.RawMessage, error) {
	raw, err := c.gpuAPI.GetVMConfig(node, vmid)
	if err != nil {
		return nil, err
	}
	var cfg map[string]interface{}
	if json.Unmarshal(raw, &cfg) != nil || cfg == nil {
		return nil, errors.New("report v3: configurazione GPU non valida")
	}
	for key, value := range cfg {
		if hostpciKey.MatchString(key) {
			text, ok := value.(string)
			if !ok || text == "" || len(text) > 4096 {
				return nil, errors.New("report v3: dispositivo GPU non valido")
			}
		}
	}
	return raw, nil
}

// A multi-card resource mapping is a pool of alternatives, one card per VM slot.
// Proxmox does not identify which alternative was selected. Homogeneous-memory pools
// can be counted/priced exactly; heterogeneous or overbooked pools stay unknown.
// The technical inventory's representation is preserved by working on a private copy.
func expandV3GPUAssignments(inv GPUInventory, mappings MappingSet, models map[string]config.GPUModel) GPUInventory {
	out := inv
	out.Nodes = append([]GPUNode(nil), inv.Nodes...)
	out.Partial = append([]string(nil), inv.Partial...)
	for i, node := range out.Nodes {
		out.Nodes[i].Cards = append([]GPUCard(nil), node.Cards...)
		for _, group := range node.Groups {
			if len(group.Uses) == 0 {
				continue
			}
			candidates := []int{}
			memory := -1
			for j, card := range node.Cards {
				for _, entry := range mappings[group.ID] {
					if entry.Node == node.Node && normalizePCIAddr(entry.Addr) == normalizePCIAddr(card.Addr) {
						m := v3GPUMemory(card.Name, models)
						if memory == -1 {
							memory = m
						}
						if memory != m {
							out.Partial = append(out.Partial, node.Node)
							memory = -2
						}
						if len(out.Nodes[i].Cards[j].Uses) == 0 {
							candidates = append(candidates, j)
						}
						break
					}
				}
			}
			seen := map[struct {
				vmid int
				slot string
			}]bool{}
			for _, use := range group.Uses {
				key := struct {
					vmid int
					slot string
				}{use.VMID, use.Slot}
				if seen[key] {
					continue
				}
				seen[key] = true
				if len(candidates) == 0 || memory == -2 {
					out.Partial = append(out.Partial, node.Node)
					continue
				}
				j := candidates[0]
				candidates = candidates[1:]
				out.Nodes[i].Cards[j].Uses = []GPUUse{use}
			}
		}
	}
	return out
}

type v3StorageAPI interface {
	GetStorage() (json.RawMessage, error)
	GetStorageContent(node, storage, content string) (json.RawMessage, error)
}

func loadV3BackupCopies(c reportAPI, nodes []resNode) ([]v3BackupCopy, bool) {
	storageAPI, ok := c.(v3StorageAPI)
	if !ok {
		return nil, false
	}
	raw, err := storageAPI.GetStorage()
	if err != nil {
		return nil, false
	}
	var stores []struct {
		Storage string `json:"storage"`
		Type    string `json:"type"`
		Nodes   string `json:"nodes"`
		Disable int    `json:"disable"`
	}
	if json.Unmarshal(raw, &stores) != nil || len(stores) > maxReportStores {
		return nil, false
	}
	lists := make([][]v3BackupCopy, len(stores))
	readable := make([]bool, len(stores))
	jobs := []func(){}
	for i, store := range stores {
		if store.Type != "pbs" || store.Disable != 0 || !storageNamePattern.MatchString(store.Storage) {
			continue
		}
		node := ""
		for _, n := range nodes {
			allowed := store.Nodes == ""
			for _, name := range strings.Split(store.Nodes, ",") {
				allowed = allowed || name == n.Name
			}
			if n.Online && allowed {
				node = n.Name
				break
			}
		}
		if node == "" {
			continue
		}
		i, store, node := i, store, node
		jobs = append(jobs, func() {
			// This client's method has no content whitelist; the argument is a fixed literal.
			raw, err := storageAPI.GetStorageContent(node, store.Storage, "backup")
			if err != nil {
				return
			}
			var copies []struct {
				VMID    int   `json:"vmid"`
				Created int64 `json:"ctime"`
			}
			if json.Unmarshal(raw, &copies) != nil || len(copies) > 100000 || string(raw) == "null" {
				return
			}
			readable[i] = true
			for _, copy := range copies {
				if copy.VMID > 0 && copy.VMID <= 999999999 && copy.Created > 0 {
					lists[i] = append(lists[i], v3BackupCopy{VMID: copy.VMID, Created: copy.Created})
				}
			}
		})
	}
	runLimited(jobs)
	out := []v3BackupCopy{}
	anyReadable := false
	for i := range stores {
		out = append(out, lists[i]...)
		anyReadable = anyReadable || readable[i]
	}
	return out, anyReadable
}

func loadV3EconomicGuests(c reportAPI, hist *history.Store, cluster string, guests []resGuest, inv *GPUInventory, options serviceV3Settings, now time.Time) ([]v3EconomicGuest, int, error) {
	// vm_use_day records completed days. Use the last 30 completed calendar days,
	// independent of the report's older selected period: this is a current monthly value.
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(-time.Second)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -30)
	var rows []history.VMUseDay
	if hist != nil {
		var err error
		rows, err = hist.VMUseDays(cluster, start.Format("2006-01-02"), end.Format("2006-01-02"))
		if err != nil {
			return nil, 0, err
		}
	}
	for _, row := range rows {
		if err := history.ValidVMUse(row); err != nil {
			return nil, 0, err
		}
	}
	days := calculateV3DaysUsed(rows, start, end)
	out := make([]v3EconomicGuest, len(guests))
	jobs := []func(){}
	for i, guest := range guests {
		i, guest := i, guest
		jobs = append(jobs, func() {
			e := v3EconomicGuest{Cores: guest.Cores, MemBytes: guest.Mem, SSDGB: -1, HDDGB: -1, OtherGB: -1}
			if e.Cores <= 0 || e.Cores > 4096 {
				e.Cores = -1
			}
			if e.MemBytes <= 0 {
				e.MemBytes = -1
			}
			inst := ""
			configKnown := guest.Kind != "qemu"
			if guest.Kind == "qemu" {
				if raw, err := c.GetVMConfig(guest.Node, guest.VMID); err == nil {
					var cfg map[string]interface{}
					if json.Unmarshal(raw, &cfg) == nil && cfg != nil {
						configKnown = true
						inst = vmInstance(cfg)
						ssd, hdd, other, known := parseV3Disks(cfg, options.Report.StorageClasses)
						if known {
							e.SSDGB, e.HDDGB, e.OtherGB = ssd, hdd, other
						}
					}
				}
			} else {
				// Only QEMU disk configurations have a storage classification here.
				// Container capacity from resources belongs to "other": market storage
				// includes it, while the internal SSD/HDD list cannot price it.
				e.SSDGB, e.HDDGB = 0, 0
				if guest.Disk > 0 {
					e.OtherGB = float64(guest.Disk) / (1 << 30)
				}
			}
			if inst == "" && configKnown {
				for _, row := range rows { // ordered by day, latest matching identity wins
					if row.VMID == guest.VMID && row.Kind == guest.Kind && row.Name == guest.Name {
						inst = row.Inst
					}
				}
			}
			e.Hours = calculateV3Hours(rows, guest.VMID, inst, guest.Kind, start, end, days)
			if guest.Kind == "qemu" && !configKnown {
				e.Hours = nil // instance unknown: the guest may have run under its real instance
			}
			if inv == nil || len(inv.Partial) > 0 {
				e.GPUMemoryGiB = []float64{-1} // unknown inventory must not imply no GPU cost
			} else {
				for _, node := range inv.Nodes {
					for _, card := range node.Cards {
						// Charge an assigned physical card once, to its first current guest.
						if len(card.Uses) > 0 && card.Uses[0].VMID == guest.VMID && node.Node == guest.Node {
							memory := v3GPUMemory(card.Name, options.Models)
							if memory <= 0 {
								memory = -1
							}
							e.GPUMemoryGiB = append(e.GPUMemoryGiB, float64(memory))
						}
					}
				}
			}
			out[i] = e
		})
	}
	runLimited(jobs)
	return out, days, nil
}
