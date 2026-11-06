package handlers

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/cache"
	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type Handler struct {
	store *cache.Store
	hist  *history.Store // daily inventory snapshots (nil in tests)
}

// SetHistory gives the reports access to the daily inventory snapshots.
func (h *Handler) SetHistory(s *history.Store) { h.hist = s }

func chiParam(r *http.Request, k string) string { return chi.URLParam(r, k) }

func New(store *cache.Store) *Handler { return &Handler{store: store} }

func (h *Handler) getClientFor(idx int) (*api.Client, error) {
	cl, err := config.GetCluster(idx)
	if err != nil || cl.URL == "" {
		return nil, fmt.Errorf("cluster non configurato")
	}
	return api.NewClient(cl.URL, cl.APIToken), nil
}

func (h *Handler) getClient() (*api.Client, error) {
	cl, err := config.GetDefaultCluster()
	if err != nil || cl.URL == "" {
		return nil, fmt.Errorf("nessun cluster configurato")
	}
	return api.NewClient(cl.URL, cl.APIToken), nil
}

func clusterIdx(r *http.Request) int {
	s := r.URL.Query().Get("cluster")
	if s == "" {
		s = r.Header.Get("X-Cluster-Index")
	}
	idx, err := strconv.Atoi(s)
	if err != nil || idx < 0 {
		return 0
	}
	return idx
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// writeErrorLog is like writeError but attaches an accumulated step log, so the
// caller can see exactly how far a multi-step operation got before failing.
func writeErrorLog(w http.ResponseWriter, err error, code int, log []string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error(), "log": log})
}

// isForbidden returns true when Proxmox responded with 403 (missing privilege).
// Used to silently return empty data for non-critical read endpoints instead of propagating 502.
func isForbidden(err error) bool {
	return err != nil && strings.Contains(err.Error(), "proxmox 403")
}

func bodyMap(r *http.Request) map[string]string {
	var m map[string]string
	json.NewDecoder(r.Body).Decode(&m)
	if m == nil {
		m = map[string]string{}
	}
	return m
}

// ── Config ────────────────────────────────────────────────────────────────────

func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	for i := range cfg.Clusters {
		tok := cfg.Clusters[i].APIToken
		if len(tok) >= 8 {
			cfg.Clusters[i].APIToken = "..." + tok[len(tok)-8:]
		} else if tok != "" {
			cfg.Clusters[i].APIToken = "..."
		}
	}
	writeJSON(w, cfg)
}

func (h *Handler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	// Pointers: a missing field leaves the saved value untouched, an empty string resets it to the default.
	var body struct {
		Theme   *string `json:"theme"`
		Density *string `json:"density"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, errors.New("richiesta non valida"), 400)
		return
	}
	if body.Theme != nil && !config.ValidTheme(*body.Theme) {
		writeError(w, errors.New("tema non valido"), 400)
		return
	}
	if body.Density != nil && !config.ValidDensity(*body.Density) {
		writeError(w, errors.New("densita non valida"), 400)
		return
	}
	cfg := config.Get()
	if body.Theme != nil {
		cfg.Theme = *body.Theme
	}
	if body.Density != nil {
		cfg.Density = *body.Density
	}
	if err := config.Update(cfg); err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) TestConnection(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	nodes, err := client.GetNodes()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, map[string]interface{}{"status": "ok", "nodes": nodes})
}

// ── Dashboard ─────────────────────────────────────────────────────────────────

func (h *Handler) GetDashboard(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}

	// Fast path: /cluster/resources returns everything in one call.
	// Falls back to individual endpoints when token lacks cluster-level read privilege
	// (symptom: nodes appear but maxmem=0).
	if raw, err := client.GetClusterResources(""); err == nil {
		var items []map[string]interface{}
		if json.Unmarshal(raw, &items) == nil {
			nc, gc := 0, 0
			for _, it := range items {
				if it["type"] == "node" {
					nc++
					if mm, _ := it["maxmem"].(float64); mm > 0 {
						gc++
					}
				}
			}
			if nc > 0 && nc == gc {
				cs, _ := client.GetClusterStatus()
				writeJSON(w, map[string]interface{}{"resources": raw, "cluster_status": cs})
				return
			}
		}
	}

	// Fallback: build resources from individual node endpoints.
	nodesRaw, err := client.GetNodes()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	var nodeNames []struct {
		Node string `json:"node"`
	}
	json.Unmarshal(nodesRaw, &nodeNames)
	var nodeItems []map[string]interface{}
	json.Unmarshal(nodesRaw, &nodeItems)

	result := make([]map[string]interface{}, 0, len(nodeItems))
	for _, n := range nodeItems {
		n["type"] = "node"
		result = append(result, n)
	}
	for _, n := range nodeNames {
		if vms, _ := client.GetVMs(n.Node); vms != nil {
			var list []map[string]interface{}
			if json.Unmarshal(vms, &list) == nil {
				for i := range list {
					list[i]["type"] = "qemu"
					list[i]["node"] = n.Node
				}
				result = append(result, list...)
			}
		}
		if lxcs, _ := client.GetContainers(n.Node); lxcs != nil {
			var list []map[string]interface{}
			if json.Unmarshal(lxcs, &list) == nil {
				for i := range list {
					list[i]["type"] = "lxc"
					list[i]["node"] = n.Node
				}
				result = append(result, list...)
			}
		}
	}

	cs, _ := client.GetClusterStatus()
	writeJSON(w, map[string]interface{}{"resources": result, "cluster_status": cs})
}

// ── Nodes ─────────────────────────────────────────────────────────────────────

func (h *Handler) GetNodes(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetNodes()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) GetNodeStatus(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	status, err := client.GetNodeStatus(node)
	if err != nil {
		if isForbidden(err) {
			writeJSON(w, map[string]interface{}{"status": nil, "storage": nil, "networks": nil})
			return
		}
		writeError(w, err, 502)
		return
	}
	storage, _ := client.GetNodeStorage(node)
	networks, _ := client.GetNodeNetworks(node)
	writeJSON(w, map[string]interface{}{"status": status, "storage": storage, "networks": networks})
}

func (h *Handler) GetNodeRRD(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	tf := r.URL.Query().Get("timeframe")
	if tf == "" {
		tf = "hour"
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetNodeRRD(node, tf)
	if err != nil {
		if isForbidden(err) {
			writeJSON(w, []interface{}{})
			return
		}
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) NodeCommand(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	cmd := chi.URLParam(r, "cmd")
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.NodeCommand(node, cmd)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

// ── VMs ───────────────────────────────────────────────────────────────────────

func (h *Handler) GetAllVMs(w http.ResponseWriter, r *http.Request) {
	cIdx := clusterIdx(r)
	client, err := h.getClientFor(cIdx)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	nodes, err := client.GetNodes()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	var nodeList []struct {
		Node string `json:"node"`
	}
	json.Unmarshal(nodes, &nodeList)

	type NodeResult struct {
		Node string                   `json:"node"`
		VMs  []map[string]interface{} `json:"vms"`
		LXCs []map[string]interface{} `json:"lxcs"`
	}
	// Resource mappings are cluster-wide: one call, only when some VM can use them.
	var mapsRaw json.RawMessage
	if raw, err := client.GetPCIMappings(); err == nil {
		mapsRaw = raw
	}
	results := make([]NodeResult, len(nodeList))
	var wg sync.WaitGroup
	for i, n := range nodeList {
		i, n := i, n
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].Node = n.Node
			// PCI table and mappings: best effort (a token without the permission just shows no GPU badge).
			var pciTab PCITable
			var mapTab MappingTable
			if raw, err := client.GetNodePCI(n.Node); err == nil {
				pciTab = ParsePCIList(raw)
			}
			if mapsRaw != nil {
				mapTab = ParsePCIMappings(mapsRaw, n.Node)
			}
			if raw, err := client.GetVMs(n.Node); err == nil {
				var vms []map[string]interface{}
				if json.Unmarshal(raw, &vms) == nil {
					var wg2 sync.WaitGroup
					for j := range vms {
						vmid, _ := vms[j]["vmid"].(float64)
						if vmid == 0 {
							continue
						}
						wg2.Add(1)
						go func(j int, vmid float64) {
							defer wg2.Done()
							cfg, err := client.GetVMConfig(n.Node, int(vmid))
							if err != nil {
								return
							}
							var config map[string]interface{}
							if json.Unmarshal(cfg, &config) != nil {
								return
							}
							if ipcfg, ok := config["ipconfig0"].(string); ok {
								if ip := extractVMIP(ipcfg); ip != "" {
									vms[j]["ip"] = ip
								}
							}
							vms[j]["gpus"] = VMGPUs(config, pciTab, mapTab)
							_, hasSerial := config["serial0"]
							vms[j]["has_serial"] = hasSerial
							if ot, ok := config["ostype"].(string); ok {
								vms[j]["ostype"] = ot
							}

							// Il campo `mem` della lista /nodes/{node}/qemu è l'RSS del
							// processo KVM lato host (≈ maxmem appena il guest tocca tutte
							// le pagine). status/current riporta invece l'uso reale del
							// guest quando il balloon/guest-agent fornisce free_mem
							// (mem = actual - free_mem). Sovrascriviamo con quel valore.
							if st, _ := vms[j]["status"].(string); st == "running" {
								if raw, err := client.GetVMStatus(n.Node, int(vmid)); err == nil {
									var cur map[string]interface{}
									if json.Unmarshal(raw, &cur) == nil {
										if m, ok := cur["mem"].(float64); ok && m > 0 {
											vms[j]["mem"] = m
										}
										if mm, ok := cur["maxmem"].(float64); ok && mm > 0 {
											vms[j]["maxmem"] = mm
										}
									}
								}
							}
						}(j, vmid)
					}
					wg2.Wait()
					results[i].VMs = vms
				}
			}
			if raw, err := client.GetContainers(n.Node); err == nil {
				var lxcs []map[string]interface{}
				if json.Unmarshal(raw, &lxcs) == nil {
					var wg2 sync.WaitGroup
					for j := range lxcs {
						vmid, _ := lxcs[j]["vmid"].(float64)
						if vmid == 0 {
							continue
						}
						wg2.Add(1)
						go func(j int, vmid float64) {
							defer wg2.Done()
							cfg, err := client.GetContainerConfig(n.Node, int(vmid))
							if err != nil {
								return
							}
							var config map[string]interface{}
							if json.Unmarshal(cfg, &config) != nil {
								return
							}
							if ot, ok := config["ostype"].(string); ok {
								lxcs[j]["ostype"] = ot
							}
							// net0: "name=eth0,bridge=vmbr0,ip=10.x.x.x/24,gw=..."
							if net0, ok := config["net0"].(string); ok {
								if ip := extractVMIP(net0); ip != "" {
									lxcs[j]["ip"] = ip
								}
							}
						}(j, vmid)
					}
					wg2.Wait()
					for j := range lxcs {
						lxcs[j]["has_serial"] = false
					}
					results[i].LXCs = lxcs
				}
			}
		}()
	}
	wg.Wait()
	clusterCfg, _ := config.GetCluster(cIdx)
	for i := range results {
		for j := range results[i].VMs {
			results[i].VMs[j]["cluster_label"] = clusterCfg.Label
			results[i].VMs[j]["cluster_idx"] = cIdx
		}
		for j := range results[i].LXCs {
			results[i].LXCs[j]["cluster_label"] = clusterCfg.Label
			results[i].LXCs[j]["cluster_idx"] = cIdx
		}
	}
	writeJSON(w, results)
}

// extractVMIP parses "ip=10.x.x.x/24,gw=..." or "name=eth0,...,ip=10.x.x.x/24,..."
func extractVMIP(s string) string {
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == "ip" && kv[1] != "dhcp" {
			ip := kv[1]
			if idx := strings.IndexByte(ip, '/'); idx != -1 {
				ip = ip[:idx]
			}
			return ip
		}
	}
	return ""
}

func (h *Handler) GetVMStatus(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	status, err := client.GetVMStatus(node, vmid)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	cfg, _ := client.GetVMConfig(node, vmid)
	snaps, _ := client.GetVMSnapshots(node, vmid)
	writeJSON(w, map[string]interface{}{"status": status, "config": cfg, "snapshots": snaps})
}

func (h *Handler) GetVMRRD(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	tf := r.URL.Query().Get("timeframe")
	if tf == "" {
		tf = "hour"
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetVMRRD(node, vmid, tf)
	if err != nil {
		if isForbidden(err) {
			writeJSON(w, []interface{}{})
			return
		}
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) GetVMFSInfo(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetVMFSInfo(node, vmid)
	if err != nil {
		log.Printf("fsinfo %s/%d error: %v", node, vmid, err)
		writeJSON(w, []interface{}{})
		return
	}
	// agent GET endpoints wrap response: {"result":[...],"errobj":null}
	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &wrapper) == nil && len(wrapper.Result) > 0 && wrapper.Result[0] != 'n' {
		writeJSON(w, wrapper.Result)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) VMAction(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	action := chi.URLParam(r, "action")
	body := bodyMap(r)
	params := url.Values{}
	for k, v := range body {
		params.Set(k, v)
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	if action == "start" {
		data, ref, err := startGuarded(client, clusterIdx(r), node, "qemu", vmid, time.Now(), func() (json.RawMessage, error) {
			return client.VMAction(node, vmid, action, params)
		})
		writeStartResult(w, data, ref, err)
		return
	}
	data, err := client.VMAction(node, vmid, action, params)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

// writeStartResult answers a guarded start: 409 not_enough_ram with the numbers, or the Proxmox answer.
func writeStartResult(w http.ResponseWriter, data json.RawMessage, ref *startRefusal, err error) {
	switch {
	case ref != nil:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": ref.Message(), "reason": "not_enough_ram", "node": ref.Node, "missing_bytes": ref.MissingBytes()})
	case err != nil:
		writeError(w, err, 502)
	default:
		writeJSON(w, data)
	}
}

func (h *Handler) VMSnapshot(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	var body struct {
		Name string `json:"name"`
		Desc string `json:"description"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		body.Name = fmt.Sprintf("snap-%d", time.Now().Unix())
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.VMSnapshot(node, vmid, body.Name, body.Desc)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

// VMMigrate: POST /api/nodes/{node}/qemu/{vmid}/migrate  body {"target": "<node>", "online": bool}
// Everything is validated before any Proxmox call (it used to pass target and decode errors through unchecked).
// It does not apply the planner's rules (RAM limit, CPU vendor for cpu: host): that is what /api/balance/step is for.
func (h *Handler) VMMigrate(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, err := strconv.Atoi(chi.URLParam(r, "vmid"))
	if !nodeNamePattern.MatchString(node) || err != nil || vmid <= 0 || vmid > 999999999 {
		writeError(w, errors.New("nodo o VMID non validi"), http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var body struct {
		Target string `json:"target"`
		Online bool   `json:"online"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return
	}
	if !nodeNamePattern.MatchString(body.Target) || body.Target == node {
		writeError(w, errors.New("nodo di destinazione non valido"), http.StatusBadRequest)
		return
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	data, err := client.VMMigrate(node, vmid, body.Target, body.Online)
	if err != nil {
		log.Printf("migrate qemu=%d %s -> %s: %v", vmid, node, body.Target, err)
		if isForbidden(err) {
			writeError(w, errors.New("permessi Proxmox insufficienti per migrare (serve VM.Migrate)"), http.StatusForbidden)
		} else {
			writeError(w, errors.New("migrazione non avviata"), http.StatusBadGateway)
		}
		return
	}
	log.Printf("migrate qemu=%d %s -> %s (online=%v)", vmid, node, body.Target, body.Online)
	writeJSON(w, data)
}

func (h *Handler) DeleteVM(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.DeleteVM(node, vmid)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

// ── VM Provisioning ───────────────────────────────────────────────────────────

// GetTemplates returns all QEMU VMs that are templates (template=1) or whose
// name matches *-cloudinit-template pattern.
func (h *Handler) GetTemplates(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	nodes, err := client.GetNodes()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	var nodeList []struct {
		Node string `json:"node"`
	}
	json.Unmarshal(nodes, &nodeList)

	type Template struct {
		Node   string          `json:"node"`
		VMID   int             `json:"vmid"`
		Name   string          `json:"name"`
		Config json.RawMessage `json:"config"`
	}

	var templates []Template
	for _, n := range nodeList {
		vms, err := client.GetVMs(n.Node)
		if err != nil {
			continue
		}
		var vmList []struct {
			VMID     int    `json:"vmid"`
			Name     string `json:"name"`
			Template int    `json:"template"`
			Status   string `json:"status"`
		}
		json.Unmarshal(vms, &vmList)
		for _, vm := range vmList {
			isTemplate := vm.Template == 1 ||
				strings.Contains(strings.ToLower(vm.Name), "template") ||
				strings.Contains(strings.ToLower(vm.Name), "cloudinit")
			if isTemplate {
				cfg, _ := client.GetVMConfig(n.Node, vm.VMID)
				templates = append(templates, Template{
					Node:   n.Node,
					VMID:   vm.VMID,
					Name:   vm.Name,
					Config: cfg,
				})
			}
		}
	}
	writeJSON(w, templates)
}

// GetNextVMID returns the next available VMID from the cluster
func (h *Handler) GetNextVMID(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetNextVMID()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

// ── Batch ─────────────────────────────────────────────────────────────────────

type BatchRequest struct {
	VMs    []BatchTarget     `json:"vms"`
	Action string            `json:"action"`
	Params map[string]string `json:"params"`
}
type BatchTarget struct {
	Node string `json:"node"`
	VMID int    `json:"vmid"`
	Type string `json:"type"`
}
type BatchResult struct {
	Node   string          `json:"node"`
	VMID   int             `json:"vmid"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Reason string          `json:"reason,omitempty"` // e.g. not_enough_ram
}

func (h *Handler) BatchAction(w http.ResponseWriter, r *http.Request) {
	var req BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, err, 400)
		return
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	params := url.Values{}
	for k, v := range req.Params {
		params.Set(k, v)
	}
	results := make([]BatchResult, 0, len(req.VMs))
	for _, vm := range req.VMs {
		var data json.RawMessage
		var opErr error
		var ref *startRefusal
		kind := "qemu"
		if vm.Type == "lxc" {
			kind = "lxc"
		}
		run := func() (json.RawMessage, error) {
			if kind == "lxc" {
				return client.ContainerAction(vm.Node, vm.VMID, req.Action, params)
			}
			return client.VMAction(vm.Node, vm.VMID, req.Action, params)
		}
		if req.Action == "start" && nodeNamePattern.MatchString(vm.Node) {
			data, ref, opErr = startGuarded(client, clusterIdx(r), vm.Node, kind, vm.VMID, time.Now(), run)
		} else {
			data, opErr = run()
		}
		br := BatchResult{Node: vm.Node, VMID: vm.VMID}
		if ref != nil {
			br.Error, br.Reason = ref.Message(), "not_enough_ram"
		} else if opErr != nil {
			br.Error = opErr.Error()
		} else {
			br.Result = data
		}
		results = append(results, br)
	}
	writeJSON(w, results)
}

// ── LXC ──────────────────────────────────────────────────────────────────────

func (h *Handler) ContainerAction(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	action := chi.URLParam(r, "action")
	body := bodyMap(r)
	params := url.Values{}
	for k, v := range body {
		params.Set(k, v)
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	if action == "start" {
		data, ref, err := startGuarded(client, clusterIdx(r), node, "lxc", vmid, time.Now(), func() (json.RawMessage, error) {
			return client.ContainerAction(node, vmid, action, params)
		})
		writeStartResult(w, data, ref, err)
		return
	}
	data, err := client.ContainerAction(node, vmid, action, params)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) ContainerSnapshot(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	var body struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		body.Name = fmt.Sprintf("snap-%d", time.Now().Unix())
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.ContainerSnapshot(node, vmid, body.Name)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) DeleteContainer(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.DeleteContainer(node, vmid)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

// ── Metrics ───────────────────────────────────────────────────────────────────

// rrdF is a nullable float64 from Proxmox RRD JSON
type rrdF struct {
	v  float64
	ok bool
}

func (f *rrdF) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		f.ok = false
		return nil
	}
	f.ok = true
	return json.Unmarshal(b, &f.v)
}

func avgSlice(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += v
	}
	return sum / float64(len(s))
}

func maxSlice(s []float64) float64 {
	var m float64
	for _, v := range s {
		if v > m {
			m = v
		}
	}
	return m
}

func pickTF(tf string) string {
	switch tf {
	case "hour", "day", "week", "month", "year":
		return tf
	}
	return "day"
}

// GetMetrics returns cluster-wide CPU/RAM time series from node RRD data.
// Used by dashboard charts. ?timeframe=hour|day|week|month|year
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	tf := pickTF(r.URL.Query().Get("timeframe"))
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	nodesRaw, err := client.GetNodes()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	var nodeList []struct {
		Node string `json:"node"`
	}
	if err := json.Unmarshal(nodesRaw, &nodeList); err != nil {
		writeError(w, err, 502)
		return
	}

	type nodeRRD struct {
		Time     rrdF `json:"time"`
		CPU      rrdF `json:"cpu"`
		MemUsed  rrdF `json:"memused"`
		MemTotal rrdF `json:"memtotal"`
	}
	type bucket struct {
		cpu []float64
		mem []float64
	}
	byTs := map[int64]*bucket{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, n := range nodeList {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			rrd, err := client.GetNodeRRD(node, tf)
			if err != nil {
				return
			}
			var pts []nodeRRD
			if json.Unmarshal(rrd, &pts) != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, p := range pts {
				if !p.Time.ok || !p.CPU.ok {
					continue
				}
				b := (int64(p.Time.v) / 300) * 300
				if byTs[b] == nil {
					byTs[b] = &bucket{}
				}
				byTs[b].cpu = append(byTs[b].cpu, p.CPU.v)
				if p.MemUsed.ok && p.MemTotal.ok && p.MemTotal.v > 0 {
					byTs[b].mem = append(byTs[b].mem, p.MemUsed.v/p.MemTotal.v)
				}
			}
		}(n.Node)
	}
	wg.Wait()

	type Metric struct {
		TS     int64   `json:"ts"`
		Node   string  `json:"node"`
		AvgCPU float64 `json:"avg_cpu"`
		AvgMem float64 `json:"avg_mem"`
	}
	result := make([]Metric, 0, len(byTs))
	for ts, b := range byTs {
		result = append(result, Metric{
			TS:     ts,
			Node:   "cluster",
			AvgCPU: avgSlice(b.cpu),
			AvgMem: avgSlice(b.mem),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TS < result[j].TS })
	writeJSON(w, result)
}

// ── Storage / Pools / Tasks ───────────────────────────────────────────────────

func (h *Handler) GetStorage(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetStorage()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) GetNodeStorage(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetNodeStorage(node)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) GetPools(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetPools()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) GetClusterTasks(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.GetClusterTasks()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

var validUsername = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ── VM disk add + guest setup ────────────────────────────────────────────────

var (
	storagePattern    = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	busPattern        = regexp.MustCompile(`^(scsi|virtio|sata)$`)
	mountPointPattern = regexp.MustCompile(`^/[a-zA-Z0-9_/-]{1,64}$`)
)

// nextDiskSlot trova il primo slot libero per il bus specificato, leggendo la config VM già fetchata.
func nextDiskSlot(cfgMap map[string]interface{}, bus string) (string, error) {
	max, ok := diskSlotLimits[bus]
	if !ok {
		return "", fmt.Errorf("bus non supportato: %s (usa scsi, virtio o sata)", bus)
	}
	for i := 0; i <= max; i++ {
		key := fmt.Sprintf("%s%d", bus, i)
		if _, exists := cfgMap[key]; !exists {
			return key, nil
		}
	}
	return "", fmt.Errorf("nessuno slot libero per bus %s", bus)
}

func (h *Handler) AddVMDisk(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, err := strconv.Atoi(chi.URLParam(r, "vmid"))
	if err != nil {
		writeError(w, fmt.Errorf("vmid non valido"), 400)
		return
	}

	var body struct {
		Storage string `json:"storage"`
		SizeGB  int    `json:"size_gb"`
		Bus     string `json:"bus"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	if body.Bus == "" {
		body.Bus = "virtio" // same default as the page (and as the new-VM page)
	}
	if !storagePattern.MatchString(body.Storage) {
		writeError(w, fmt.Errorf("nome storage non valido"), 400)
		return
	}
	if !busPattern.MatchString(body.Bus) {
		writeError(w, fmt.Errorf("bus non valido (usa scsi, virtio o sata)"), 400)
		return
	}
	if body.SizeGB < 1 || body.SizeGB > 16384 {
		writeError(w, fmt.Errorf("dimensione non valida (1-16384 GB)"), 400)
		return
	}

	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}

	cfgRaw, err := client.GetVMConfig(node, vmid)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	var cfgMap map[string]interface{}
	json.Unmarshal(cfgRaw, &cfgMap)

	slot, err := nextDiskSlot(cfgMap, body.Bus)
	if err != nil {
		writeError(w, err, 400)
		return
	}

	params := url.Values{}
	params.Set(slot, fmt.Sprintf("%s:%d", body.Storage, body.SizeGB))

	if _, err := client.VMSetConfig(node, vmid, params); err != nil {
		writeError(w, fmt.Errorf("aggiunta disco fallita: %w", err), 502)
		return
	}

	writeJSON(w, map[string]interface{}{
		"disk":    slot,
		"storage": body.Storage,
		"size_gb": body.SizeGB,
	})
}

func (h *Handler) SetupVMDisk(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, err := strconv.Atoi(chi.URLParam(r, "vmid"))
	if err != nil {
		writeError(w, fmt.Errorf("vmid non valido"), 400)
		return
	}

	var body struct {
		Disk         string `json:"disk"`
		MountPoint   string `json:"mount_point"`
		StickyBit    *bool  `json:"sticky_bit"`
		AllowRestart bool   `json:"allow_restart"` // the user confirmed that the VM may be stopped and started
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	if body.MountPoint == "" {
		body.MountPoint = "/data"
	}
	sticky := true
	if body.StickyBit != nil {
		sticky = *body.StickyBit
	}
	if err := validateDiskSlot(body.Disk); err != nil {
		writeError(w, err, 400)
		return
	}
	if err := validateMountPoint(body.MountPoint); err != nil {
		writeError(w, err, 400)
		return
	}
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	res, e := runSetupDisk(client, node, vmid, setupDiskReq{Disk: body.Disk, MountPoint: body.MountPoint, Sticky: sticky, AllowRestart: body.AllowRestart})
	if e != nil {
		writeSetupDiskErr(w, e)
		return
	}
	writeJSON(w, res)
}

func (h *Handler) AddVMUser(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmidStr := chi.URLParam(r, "vmid")
	vmid, err := strconv.Atoi(vmidStr)
	if err != nil {
		writeError(w, fmt.Errorf("vmid non valido"), 400)
		return
	}

	var body struct {
		Username string `json:"username"`
		SSHKey   string `json:"ssh_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	if !validUsername.MatchString(body.Username) {
		writeError(w, fmt.Errorf("username non valido (solo a-z, 0-9, _ -, inizia con lettera o _, max 32)"), 400)
		return
	}

	// La chiave è validata e poi passata come ARGOMENTO ($2) dello script, mai
	// interpolata nel testo della shell: una chiave con apici o metacaratteri non
	// può eseguire comandi nel guest.
	sshKey, err := normalizeSSHKey(body.SSHKey)
	if err != nil {
		writeError(w, err, 400)
		return
	}

	c, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 503)
		return
	}

	// Helper etichettato se presente nel guest (SELinux enforcing), altrimenti script inline.
	useHelper, err := addUserTool.probeHelper(c, node, vmid)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	log.Printf("adduser %s/%d: esecuzione %s", node, vmid, guestMode(useHelper))
	res, err := addUserTool.run(c, node, vmid, useHelper, []string{body.Username, sshKey}, 30*time.Second)
	if err != nil {
		writeError(w, err, 502)
		return
	}

	// L'utente già esistente non è un errore del server: 409, nessuna modifica fatta.
	if res.ExitCode == 3 {
		writeError(w, fmt.Errorf("l'utente %s esiste già nella VM: nessuna modifica effettuata", body.Username), 409)
		return
	}

	// Senza sudo nella VM l'utente non verrebbe creato (exit 4): nessuna modifica, spiegato in parole.
	if res.ExitCode == 4 {
		writeError(w, errors.New("sudo non e' installato nella VM: l'utente non e' stato creato"), 409)
		return
	}

	output := res.Out
	if res.Err != "" {
		output += res.Err
	}
	log.Printf("adduser %s@%s/%d exitcode=%d", body.Username, node, vmid, res.ExitCode)
	writeJSON(w, map[string]interface{}{"output": output, "exitcode": res.ExitCode})
}

// ── Cluster management ────────────────────────────────────────────────────────

func maskToken(tok string) string {
	if len(tok) >= 8 {
		return "..." + tok[len(tok)-8:]
	}
	if tok != "" {
		return "..."
	}
	return ""
}

func (h *Handler) GetClusters(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	type SafeCluster struct {
		Label                 string `json:"label"`
		URL                   string `json:"url"`
		APITokenHint          string `json:"api_token_hint"`
		Default               bool   `json:"default"`
		Idx                   int    `json:"idx"`
		HasConsoleCredentials bool   `json:"has_console_credentials"`
	}
	result := make([]SafeCluster, len(cfg.Clusters))
	for i, c := range cfg.Clusters {
		result[i] = SafeCluster{
			Label:                 c.Label,
			URL:                   c.URL,
			APITokenHint:          maskToken(c.APIToken),
			Default:               c.Default,
			Idx:                   i,
			HasConsoleCredentials: c.Username != "" && c.Password != "",
		}
	}
	writeJSON(w, result)
}

func (h *Handler) AddCluster(w http.ResponseWriter, r *http.Request) {
	var body config.ClusterConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	if body.Label == "" || body.URL == "" || body.APIToken == "" {
		writeError(w, fmt.Errorf("label, url e api_token richiesti"), 400)
		return
	}
	cfg := config.Get()
	// Primo cluster → forzare default. Altrimenti ignorare il flag dal frontend.
	if len(cfg.Clusters) == 0 {
		body.Default = true
	} else {
		body.Default = false
	}
	cfg.Clusters = append(cfg.Clusters, body)
	if err := config.Update(cfg); err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]interface{}{"status": "ok", "idx": len(cfg.Clusters) - 1})
}

func (h *Handler) UpdateCluster(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	var body config.ClusterConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	cfg := config.Get()
	if idx < 0 || idx >= len(cfg.Clusters) {
		writeError(w, fmt.Errorf("cluster index out of range"), 404)
		return
	}
	existing := cfg.Clusters[idx]
	if body.Label != "" {
		existing.Label = body.Label
	}
	if body.URL != "" {
		existing.URL = body.URL
	}
	if body.APIToken != "" {
		existing.APIToken = body.APIToken
	}
	if body.Username == "" {
		// Empty username → clear both console credentials
		existing.Username = ""
		existing.Password = ""
	} else {
		existing.Username = body.Username
		if body.Password != "" {
			existing.Password = body.Password
		}
	}
	// Default preservato: non modificato da questo endpoint
	// Creare un nuovo slice per evitare aliasing
	newClusters := make([]config.ClusterConfig, len(cfg.Clusters))
	copy(newClusters, cfg.Clusters)
	newClusters[idx] = existing
	cfg.Clusters = newClusters
	if err := config.Update(cfg); err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) DeleteCluster(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	cfg := config.Get()
	if len(cfg.Clusters) <= 1 {
		writeError(w, fmt.Errorf("impossibile eliminare l'ultimo cluster"), 400)
		return
	}
	if idx < 0 || idx >= len(cfg.Clusters) {
		writeError(w, fmt.Errorf("cluster index out of range"), 404)
		return
	}
	newClusters := make([]config.ClusterConfig, 0, len(cfg.Clusters)-1)
	newClusters = append(newClusters, cfg.Clusters[:idx]...)
	newClusters = append(newClusters, cfg.Clusters[idx+1:]...)
	// Se non resta nessun default, promuovi il primo
	hasDefault := false
	for _, c := range newClusters {
		if c.Default {
			hasDefault = true
			break
		}
	}
	if !hasDefault && len(newClusters) > 0 {
		newClusters[0].Default = true
	}
	cfg.Clusters = newClusters
	if err := config.Update(cfg); err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) TestCluster(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	nodes, err := client.GetNodes()
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, map[string]interface{}{"status": "ok", "nodes": nodes})
}

func (h *Handler) SetDefaultCluster(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	cfg := config.Get()
	if idx < 0 || idx >= len(cfg.Clusters) {
		writeError(w, fmt.Errorf("cluster index out of range"), 404)
		return
	}
	newClusters := make([]config.ClusterConfig, len(cfg.Clusters))
	copy(newClusters, cfg.Clusters)
	for i := range newClusters {
		newClusters[i].Default = (i == idx)
	}
	cfg.Clusters = newClusters
	if err := config.Update(cfg); err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── Termproxy / serial console ────────────────────────────────────────────────

func (h *Handler) TermproxyCreate(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))

	cfg, err := config.GetCluster(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	if cfg.Username == "" || cfg.Password == "" {
		writeError(w, fmt.Errorf("console non disponibile: configura username e password per questo cluster in Impostazioni"), 400)
		return
	}

	// Use username+password client — the PVEVNC ticket must belong to the same
	// user that presents the PVEAuthCookie on vncwebsocket, otherwise Proxmox returns 401.
	consoleClient, err := api.NewClientWithCredentials(cfg.URL, cfg.Username, cfg.Password)
	if err != nil {
		writeError(w, fmt.Errorf("autenticazione console fallita: %w", err), 502)
		return
	}
	log.Printf("[termproxy-create] node=%s vmid=%d user=%s", node, vmid, cfg.Username)

	data, err := consoleClient.TermproxyCreate(node, vmid, cfg.URL)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
}

func (h *Handler) TermproxyWS(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid := chi.URLParam(r, "vmid")
	ticket := r.URL.Query().Get("ticket")
	port := r.URL.Query().Get("port")

	if ticket == "" || port == "" {
		writeError(w, fmt.Errorf("ticket e port richiesti"), 400)
		return
	}

	clCfg, _ := config.GetCluster(clusterIdx(r))
	if clCfg.URL == "" {
		writeError(w, fmt.Errorf("cluster non configurato"), 400)
		return
	}

	// Auth check before WS upgrade — vncwebsocket requires PVEAuthCookie, not API token
	if clCfg.Username == "" || clCfg.Password == "" {
		writeError(w, fmt.Errorf("console non disponibile: configura username e password per questo cluster in Impostazioni"), 400)
		return
	}
	log.Printf("[termproxy-ws] authenticating as %s for vncwebsocket", clCfg.Username)
	pveTicket, _, err := api.GetAccessTicket(clCfg.URL, clCfg.Username, clCfg.Password)
	if err != nil {
		log.Printf("[termproxy-ws] GetAccessTicket failed: %v", err)
		writeError(w, fmt.Errorf("autenticazione console fallita: %w", err), 502)
		return
	}

	log.Printf("[termproxy-ws] node=%s vmid=%s port=%s proxmox=%s", node, vmid, port, clCfg.URL)

	upgrader := websocket.Upgrader{
		CheckOrigin:  func(r *http.Request) bool { return true },
		Subprotocols: []string{"binary"},
	}
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[termproxy-ws] upgrade browser: %v", err)
		return
	}
	defer browserConn.Close()

	proxmoxHost := extractHost(clCfg.URL)
	proxmoxWS := fmt.Sprintf(
		"wss://%s/api2/json/nodes/%s/qemu/%s/vncwebsocket?port=%s&vncticket=%s",
		proxmoxHost, node, vmid, port, url.QueryEscape(ticket),
	)
	log.Printf("[termproxy-ws] dialing proxmox: %s", proxmoxWS)

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		Subprotocols:     []string{"binary"},
		HandshakeTimeout: 10 * time.Second,
	}
	reqHeader := http.Header{}
	reqHeader.Set("Cookie", "PVEAuthCookie="+pveTicket)

	proxmoxConn, resp, err := dialer.Dial(proxmoxWS, reqHeader)
	if err != nil {
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		log.Printf("[termproxy-ws] dial proxmox FAILED: %v (HTTP %d)", err, statusCode)
		browserConn.WriteMessage(websocket.TextMessage,
			[]byte(fmt.Sprintf(
				"\r\n\x1b[31mErrore connessione Proxmox (HTTP %d): %v\x1b[0m\r\n",
				statusCode, err,
			)))
		return
	}
	defer proxmoxConn.Close()
	log.Printf("[termproxy-ws] connected to proxmox OK")

	// Send initial auth message — termproxy expects "USERNAME:PVEVNC_TICKET\n" as
	// the very first WebSocket message or it closes the connection immediately (1006).
	authMsg := fmt.Sprintf("%s:%s\n", clCfg.Username, ticket)
	if err := proxmoxConn.WriteMessage(websocket.TextMessage, []byte(authMsg)); err != nil {
		log.Printf("[termproxy-ws] send auth message failed: %v", err)
		browserConn.WriteMessage(websocket.TextMessage,
			[]byte("\r\n\x1b[31mErrore invio autenticazione console\x1b[0m\r\n"))
		return
	}
	log.Printf("[termproxy-ws] auth message sent for user %s", clCfg.Username)

	errc := make(chan error, 2)

	// Proxmox → Browser: raw data, no wrapping
	go func() {
		for {
			mt, msg, err := proxmoxConn.ReadMessage()
			if err != nil {
				errc <- fmt.Errorf("proxmox→browser: %w", err)
				return
			}
			if err := browserConn.WriteMessage(mt, msg); err != nil {
				errc <- fmt.Errorf("write browser: %w", err)
				return
			}
		}
	}()

	// Browser → Proxmox: wrap input in pve-xtermjs protocol
	// "0:LENGTH:DATA" for normal input, "1:COLS:ROWS:" for resize, "2" for ping
	go func() {
		for {
			mt, msg, err := browserConn.ReadMessage()
			if err != nil {
				errc <- fmt.Errorf("browser→proxmox: %w", err)
				return
			}
			s := string(msg)
			var wrapped string
			if strings.HasPrefix(s, "1:") || s == "2" {
				wrapped = s
			} else {
				wrapped = fmt.Sprintf("0:%d:%s", len(msg), s)
			}
			if err := proxmoxConn.WriteMessage(mt, []byte(wrapped)); err != nil {
				errc <- fmt.Errorf("write proxmox: %w", err)
				return
			}
		}
	}()

	err = <-errc
	log.Printf("[termproxy-ws] pipe closed: %v", err)
}

func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.Port() == "" {
		return u.Hostname() + ":8006"
	}
	return u.Host
}
