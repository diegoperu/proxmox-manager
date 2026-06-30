package handlers

import (
	"crypto/tls"
	"encoding/json"
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

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type Handler struct {
	store *cache.Store
}

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
	var body struct {
		Theme string `json:"theme"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	cfg := config.Get()
	cfg.Theme = body.Theme
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
	var nodeNames []struct{ Node string `json:"node"` }
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
	var nodeList []struct{ Node string `json:"node"` }
	json.Unmarshal(nodes, &nodeList)

	type NodeResult struct {
		Node string                   `json:"node"`
		VMs  []map[string]interface{} `json:"vms"`
		LXCs []map[string]interface{} `json:"lxcs"`
	}
	results := make([]NodeResult, len(nodeList))
	var wg sync.WaitGroup
	for i, n := range nodeList {
		i, n := i, n
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].Node = n.Node
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
							_, hasSerial := config["serial0"]
							vms[j]["has_serial"] = hasSerial
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
	data, err := client.VMAction(node, vmid, action, params)
	if err != nil {
		writeError(w, err, 502)
		return
	}
	writeJSON(w, data)
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

func (h *Handler) VMMigrate(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	vmid, _ := strconv.Atoi(chi.URLParam(r, "vmid"))
	var body struct {
		Target string `json:"target"`
		Online bool   `json:"online"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	data, err := client.VMMigrate(node, vmid, body.Target, body.Online)
	if err != nil {
		writeError(w, err, 502)
		return
	}
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
	var nodeList []struct{ Node string `json:"node"` }
	json.Unmarshal(nodes, &nodeList)

	type Template struct {
		Node    string          `json:"node"`
		VMID    int             `json:"vmid"`
		Name    string          `json:"name"`
		Config  json.RawMessage `json:"config"`
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

// ProvisionVM clones a template and configures the new VM in one call.
// Body JSON:
//   {
//     "template_node": "pve1", "template_vmid": 9000,
//     "target_node":   "pve2",  // optional, defaults to template_node
//     "new_vmid": 101,           // 0 = auto
//     "name": "myvm",
//     "cpu": 4,
//     "memory": 4096,            // MB
//     "disk": "50G",             // desired TOTAL disk size e.g. "50G"
//     "disk_device": "scsi0",    // optional, defaults to scsi0
//     "ip": "192.168.1.50/24",
//     "gateway": "192.168.1.1",
//     "dns": "8.8.8.8",
//     "ssh_keys": "...",         // optional
//     "ciuser": "ubuntu",        // optional cloud-init user
//     "cipassword": "..."        // optional
//   }

func (h *Handler) ProvisionVM(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TemplateNode string `json:"template_node"`
		TemplateVMID int    `json:"template_vmid"`
		TargetNode   string `json:"target_node"`
		NewVMID      int    `json:"new_vmid"`
		Name         string `json:"name"`
		CPU          int    `json:"cpu"`
		Memory       int    `json:"memory"`
		Disk         string `json:"disk"`
		DiskDevice   string `json:"disk_device"`
		IP           string `json:"ip"`
		Gateway      string `json:"gateway"`
		DNS          string `json:"dns"`
		SSHKeys      string `json:"ssh_keys"`
		CIUser       string `json:"ciuser"`
		CIPassword   string `json:"cipassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	if body.TemplateNode == "" || body.TemplateVMID == 0 {
		writeError(w, fmt.Errorf("template_node e template_vmid richiesti"), 400)
		return
	}
	if body.DiskDevice == "" {
		body.DiskDevice = "scsi0"
	}
	if body.TargetNode == "" {
		body.TargetNode = body.TemplateNode
	}

	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}

	// Step 1: get next VMID if not provided
	if body.NewVMID == 0 {
		raw, err := client.GetNextVMID()
		if err != nil {
			writeError(w, fmt.Errorf("GetNextVMID: %w", err), 502)
			return
		}
		// raw is a quoted string like "101" or a number
		var idStr string
		if err := json.Unmarshal(raw, &idStr); err != nil {
			// might be a bare number
			json.Unmarshal(raw, &body.NewVMID)
		} else {
			body.NewVMID, _ = strconv.Atoi(idStr)
		}
	}
	if body.NewVMID == 0 {
		writeError(w, fmt.Errorf("impossibile determinare VMID"), 500)
		return
	}

	// Step 2: get current disk size from template config so we can calculate delta
	var currentDiskGB int
	if body.Disk != "" {
		templateCfg, err := client.GetVMConfig(body.TemplateNode, body.TemplateVMID)
		if err == nil {
			var cfgMap map[string]interface{}
			if json.Unmarshal(templateCfg, &cfgMap) == nil {
				if diskVal, ok := cfgMap[body.DiskDevice].(string); ok {
					// Format: "local-lvm:vm-9000-disk-0,size=20G"
					for _, part := range strings.Split(diskVal, ",") {
						if strings.HasPrefix(part, "size=") {
							sizeStr := strings.TrimPrefix(part, "size=")
							sizeStr = strings.TrimSuffix(sizeStr, "G")
							sizeStr = strings.TrimSuffix(sizeStr, "M")
							currentDiskGB, _ = strconv.Atoi(strings.TrimSpace(sizeStr))
						}
					}
				}
			}
		}
	}

	// Step 3: full clone
	cloneParams := url.Values{}
	cloneParams.Set("full", "1")
	if body.Name != "" {
		cloneParams.Set("name", body.Name)
	}
	if body.TargetNode != body.TemplateNode {
		cloneParams.Set("target", body.TargetNode)
	}

	log := []string{}
	log = append(log, fmt.Sprintf("Clonazione template %d → VMID %d su %s", body.TemplateVMID, body.NewVMID, body.TargetNode))

	taskID, err := client.VMClone(body.TemplateNode, body.TemplateVMID, body.NewVMID, cloneParams)
	if err != nil {
		writeError(w, fmt.Errorf("clone: %w", err), 502)
		return
	}
	log = append(log, fmt.Sprintf("Clone avviato (task: %s)", string(taskID)))

	// Step 4: wait for clone task to complete (cross-node clones can take minutes)
	if err := client.WaitForTask(taskID, 10*time.Minute); err != nil {
		log = append(log, fmt.Sprintf("⚠ Attesa clone: %v", err))
	} else {
		log = append(log, "✓ Clone completato")
	}

	// Step 5: set CPU and memory
	if body.CPU > 0 || body.Memory > 0 {
		cfgParams := url.Values{}
		if body.CPU > 0 {
			cfgParams.Set("cores", strconv.Itoa(body.CPU))
			cfgParams.Set("sockets", "1")
		}
		if body.Memory > 0 {
			cfgParams.Set("memory", strconv.Itoa(body.Memory))
		}
		if _, err := client.VMSetConfig(body.TargetNode, body.NewVMID, cfgParams); err != nil {
			log = append(log, fmt.Sprintf("⚠ Set CPU/RAM: %v", err))
		} else {
			log = append(log, fmt.Sprintf("✓ CPU: %d cores, RAM: %d MB", body.CPU, body.Memory))
		}
	}

	// Step 6: resize disk
	if body.Disk != "" {
		desiredStr := strings.ToUpper(strings.TrimSpace(body.Disk))
		desiredGB := 0
		if strings.HasSuffix(desiredStr, "G") {
			desiredGB, _ = strconv.Atoi(strings.TrimSuffix(desiredStr, "G"))
		} else if strings.HasSuffix(desiredStr, "GB") {
			desiredGB, _ = strconv.Atoi(strings.TrimSuffix(desiredStr, "GB"))
		} else {
			desiredGB, _ = strconv.Atoi(desiredStr)
		}

		var sizeParam string
		if currentDiskGB > 0 && desiredGB > currentDiskGB {
			// Use relative increment: +deltaG
			delta := desiredGB - currentDiskGB
			sizeParam = fmt.Sprintf("+%dG", delta)
			log = append(log, fmt.Sprintf("Resize disco %s: %dG → %dG (delta: +%dG)", body.DiskDevice, currentDiskGB, desiredGB, delta))
		} else if desiredGB > 0 {
			// Fallback: absolute size (works if we couldn't detect current)
			sizeParam = fmt.Sprintf("%dG", desiredGB)
			log = append(log, fmt.Sprintf("Resize disco %s: %s", body.DiskDevice, sizeParam))
		}

		if sizeParam != "" {
			if _, err := client.VMResizeDisk(body.TargetNode, body.NewVMID, body.DiskDevice, sizeParam); err != nil {
				log = append(log, fmt.Sprintf("⚠ Resize disco: %v", err))
			} else {
				log = append(log, fmt.Sprintf("✓ Disco resizato a %dG", desiredGB))
			}
		}
	}

	// Step 7: cloud-init
	ciParams := url.Values{}
	hasCI := false
	if body.IP != "" {
		ip := body.IP
		if !strings.Contains(ip, "/") {
			ip += "/24"
		}
		ipCfg := "ip=" + ip
		if body.Gateway != "" {
			ipCfg += ",gw=" + body.Gateway
		}
		ciParams.Set("ipconfig0", ipCfg)
		hasCI = true
	}
	if body.DNS != "" {
		ciParams.Set("nameserver", body.DNS)
		hasCI = true
	}
	if body.SSHKeys != "" {
		// Proxmox expects RFC 3986 URL encoding (%20 for spaces, not +).
		// url.QueryEscape produces + for spaces which Proxmox rejects.
		ciParams.Set("sshkeys", strings.ReplaceAll(url.QueryEscape(body.SSHKeys), "+", "%20"))
		hasCI = true
	}
	if body.CIUser != "" {
		ciParams.Set("ciuser", body.CIUser)
		hasCI = true
	}
	if body.CIPassword != "" {
		ciParams.Set("cipassword", body.CIPassword)
		hasCI = true
	}
	if hasCI {
		if _, err := client.VMSetCloudInit(body.TargetNode, body.NewVMID, ciParams); err != nil {
			log = append(log, fmt.Sprintf("⚠ Cloud-init: %v", err))
		} else {
			log = append(log, fmt.Sprintf("✓ Cloud-init: IP=%s GW=%s DNS=%s", body.IP, body.Gateway, body.DNS))
		}
	}

	log = append(log, fmt.Sprintf("✓ VM %d pronta su nodo %s", body.NewVMID, body.TargetNode))

	writeJSON(w, map[string]interface{}{
		"vmid":   body.NewVMID,
		"node":   body.TargetNode,
		"name":   body.Name,
		"log":    log,
		"status": "ok",
	})
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
		if vm.Type == "lxc" {
			data, opErr = client.ContainerAction(vm.Node, vm.VMID, req.Action, params)
		} else {
			data, opErr = client.VMAction(vm.Node, vm.VMID, req.Action, params)
		}
		br := BatchResult{Node: vm.Node, VMID: vm.VMID}
		if opErr != nil {
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
	var body struct{ Name string `json:"name"` }
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
type rrdF struct{ v float64; ok bool }

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
		Time    rrdF `json:"time"`
		CPU     rrdF `json:"cpu"`
		MemUsed rrdF `json:"memused"`
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

// GetReportData returns per-VM aggregated metrics from RRD data.
// ?timeframe=hour|day|week|month|year  &type=qemu|lxc|all  &node=nodename
func (h *Handler) GetReportData(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tf := pickTF(q.Get("timeframe"))
	resType := q.Get("type")
	filterNode := q.Get("node")

	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	resRaw, err := client.GetClusterResources("vm")
	if err != nil {
		writeError(w, err, 502)
		return
	}
	var vms []struct {
		Type string `json:"type"`
		Node string `json:"node"`
		VMID int    `json:"vmid"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resRaw, &vms); err != nil {
		writeError(w, err, 502)
		return
	}

	type vmRRD struct {
		Time   rrdF `json:"time"`
		CPU    rrdF `json:"cpu"`
		Mem    rrdF `json:"mem"`
		MaxMem rrdF `json:"maxmem"`
		NetIn  rrdF `json:"netin"`
		NetOut rrdF `json:"netout"`
	}
	type AggRow struct {
		TS        int64   `json:"ts"`
		Node      string  `json:"node"`
		VMID      *int    `json:"vmid,omitempty"`
		Name      string  `json:"name"`
		VMType    string  `json:"vmtype"`
		AvgCPU    float64 `json:"avg_cpu"`
		MaxCPU    float64 `json:"max_cpu"`
		AvgMem    float64 `json:"avg_mem"`
		AvgNetIn  float64 `json:"avg_netin"`
		AvgNetOut float64 `json:"avg_netout"`
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		result []AggRow
	)

	for _, vm := range vms {
		if resType != "" && resType != "all" && vm.Type != resType {
			continue
		}
		if filterNode != "" && vm.Node != filterNode {
			continue
		}
		wg.Add(1)
		go func(vmType, vmNode, vmName string, vmid int) {
			defer wg.Done()
			var rrdRaw json.RawMessage
			var err error
			if vmType == "qemu" {
				rrdRaw, err = client.GetVMRRD(vmNode, vmid, tf)
			} else {
				rrdRaw, err = client.GetContainerRRD(vmNode, vmid, tf)
			}
			if err != nil {
				return
			}
			var pts []vmRRD
			if json.Unmarshal(rrdRaw, &pts) != nil {
				return
			}

			type hb struct{ cpu, mem, ni, no []float64 }
			hMap := map[int64]*hb{}
			for _, p := range pts {
				if !p.Time.ok {
					continue
				}
				ts := (int64(p.Time.v) / 3600) * 3600
				if hMap[ts] == nil {
					hMap[ts] = &hb{}
				}
				b := hMap[ts]
				if p.CPU.ok {
					b.cpu = append(b.cpu, p.CPU.v)
				}
				if p.Mem.ok && p.MaxMem.ok && p.MaxMem.v > 0 {
					b.mem = append(b.mem, p.Mem.v/p.MaxMem.v)
				}
				if p.NetIn.ok {
					b.ni = append(b.ni, p.NetIn.v)
				}
				if p.NetOut.ok {
					b.no = append(b.no, p.NetOut.v)
				}
			}

			id := vmid
			mu.Lock()
			for ts, b := range hMap {
				result = append(result, AggRow{
					TS: ts, Node: vmNode, VMID: &id,
					Name: vmName, VMType: vmType,
					AvgCPU:    avgSlice(b.cpu),
					MaxCPU:    maxSlice(b.cpu),
					AvgMem:    avgSlice(b.mem),
					AvgNetIn:  avgSlice(b.ni),
					AvgNetOut: avgSlice(b.no),
				})
			}
			mu.Unlock()
		}(vm.Type, vm.Node, vm.Name, vm.VMID)
	}
	wg.Wait()

	sort.Slice(result, func(i, j int) bool {
		if result[i].Node != result[j].Node {
			return result[i].Node < result[j].Node
		}
		if result[i].VMID != nil && result[j].VMID != nil && *result[i].VMID != *result[j].VMID {
			return *result[i].VMID < *result[j].VMID
		}
		return result[i].TS < result[j].TS
	})
	if result == nil {
		result = []AggRow{}
	}
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
		writeError(w, fmt.Errorf("username non valido (solo a-z, 0-9, _ -)"), 400)
		return
	}

	sshKey := strings.ReplaceAll(strings.ReplaceAll(body.SSHKey, "\n", ""), "\r", "")
	if sshKey != "" {
		validKeyPrefixes := []string{"ssh-rsa ", "ssh-ed25519 ", "ecdsa-sha2-nistp256 ", "ecdsa-sha2-nistp384 ", "ecdsa-sha2-nistp521 ", "sk-"}
		valid := false
		for _, p := range validKeyPrefixes {
			if strings.HasPrefix(sshKey, p) {
				valid = true
				break
			}
		}
		if !valid {
			writeError(w, fmt.Errorf("chiave pubblica SSH non valida"), 400)
			return
		}
	}

	c, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, 503)
		return
	}

	u := body.Username
	var script string
	if sshKey == "" {
		script = fmt.Sprintf(
			"useradd -m -s /bin/bash %s 2>/dev/null || true; "+
				"echo '%s:p4ssw0rd' | chpasswd; "+
				"usermod -aG sudo %s 2>/dev/null || true; "+
				"usermod -aG wheel %s 2>/dev/null || true; "+
				"chage -d 0 %s",
			u, u, u, u, u,
		)
	} else {
		script = fmt.Sprintf(
			"useradd -m -s /bin/bash %s 2>/dev/null || true; "+
				"usermod -aG sudo %s 2>/dev/null || true; "+
				"usermod -aG wheel %s 2>/dev/null || true; "+
				"mkdir -p /home/%s/.ssh; "+
				"echo '%s' >> /home/%s/.ssh/authorized_keys; "+
				"chmod 700 /home/%s/.ssh; "+
				"chmod 600 /home/%s/.ssh/authorized_keys; "+
				"chown -R %s:%s /home/%s/.ssh; "+
				"echo '%s ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/%s; "+
				"chmod 440 /etc/sudoers.d/%s",
			u, u, u, u, sshKey, u, u, u, u, u, u, u, u, u,
		)
	}

	execRaw, err := c.AgentExec(node, vmid, []string{"/bin/sh", "-c", script})
	if err != nil {
		writeError(w, fmt.Errorf("agent exec: %w", err), 502)
		return
	}
	var execResp struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(execRaw, &execResp); err != nil || execResp.PID == 0 {
		writeError(w, fmt.Errorf("agent exec risposta inattesa: %s", string(execRaw)), 502)
		return
	}

	deadline := time.Now().Add(30 * time.Second)
	var output string
	var exitCode int
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		statusRaw, err := c.AgentExecStatus(node, vmid, execResp.PID)
		if err != nil {
			continue
		}
		var s struct {
			Exited   int    `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  string `json:"out-data"`
			ErrData  string `json:"err-data"`
		}
		if err := json.Unmarshal(statusRaw, &s); err != nil {
			continue
		}
		if s.Exited == 1 {
			output = s.OutData
			if s.ErrData != "" {
				output += s.ErrData
			}
			exitCode = s.ExitCode
			break
		}
	}

	log.Printf("adduser %s@%s/%d exitcode=%d", u, node, vmid, exitCode)
	writeJSON(w, map[string]interface{}{"output": output, "exitcode": exitCode})
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
	node   := chi.URLParam(r, "node")
	vmid   := chi.URLParam(r, "vmid")
	ticket := r.URL.Query().Get("ticket")
	port   := r.URL.Query().Get("port")

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
