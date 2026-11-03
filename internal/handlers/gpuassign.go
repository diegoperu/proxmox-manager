package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"proxmox-manager/internal/api"
)

// GPU assignment (write). Decisions D20-D26: only through PCI resource mappings with one card per node,
// direct hostpci lines are never written or removed, the VM must be stopped, options are copied from the
// assignments already in use for the same card model.
//
// Safety, in order: input whitelists; one change at a time per cluster+node (app lock); a fresh read of the
// VM (stopped, no lock, not a template) and of every VM config on the node (the card must be free, a single
// unreadable config aborts); the client must confirm the exact slot and options it was shown; the PUT carries
// the config digest, so Proxmox rejects it if anyone changed the VM in between.

const maxHostPCISlots = 16 // hostpci0..hostpci15

var (
	vmidPattern       = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	hostpciSlotPat    = regexp.MustCompile(`^hostpci(\d|1[0-5])$`)
	allowedPCIOpts    = map[string]bool{"pcie": true, "rombar": true, "x-vga": true}
	gpuLocks          [64]sync.Mutex // striped by cluster+node: bounded memory whatever node names are requested
	errGPUCannotCheck = errors.New("impossibile verificare tutte le VM del nodo")
)

// normalizeHostpciOptions keeps only whitelisted on/off options of a hostpci value, sorted ("pcie=1,rombar=0").
func normalizeHostpciOptions(val string) string {
	var out []string
	seen := map[string]bool{}
	for i, f := range strings.Split(val, ",") {
		if i == 0 {
			continue // the device itself (address or mapping=...)
		}
		k, v, ok := strings.Cut(strings.TrimSpace(f), "=")
		if !ok || !allowedPCIOpts[k] || (v != "0" && v != "1") || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// pickGPUOptions: the most frequent option set among the assignments of the same card model (D22 A);
// ties go to the shorter set, then alphabetical. No assignment of that model = no options.
func pickGPUOptions(inv GPUInventory, cardName string) string {
	count := map[string]int{}
	for _, n := range inv.Nodes {
		for _, c := range n.Cards {
			if c.Name != cardName {
				continue
			}
			for _, u := range c.Uses {
				count[u.Options]++
			}
		}
	}
	best, bestN := "", 0
	for o, n := range count {
		if n > bestN || (n == bestN && (len(o) < len(best) || (len(o) == len(best) && o < best))) {
			best, bestN = o, n
		}
	}
	return best
}

// gpuLock serialises changes on one node of one cluster. Two nodes may share a stripe (they then wait for
// each other): harmless, changes are rare and short.
func gpuLock(cluster int, node string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.Itoa(cluster) + "|" + node))
	return &gpuLocks[h.Sum32()%uint32(len(gpuLocks))]
}

// gpuErr is a refusal the UI can act on: Reason is machine-readable (running, locked, busy...).
type gpuErr struct {
	Code   int
	Reason string
	Msg    string
}

func (e *gpuErr) Error() string { return e.Msg }

func refuse(code int, reason, msg string) *gpuErr {
	return &gpuErr{Code: code, Reason: reason, Msg: msg}
}

type gpuWriteAPI interface {
	gpuAPI
	GetVMStatus(node string, vmid int) (json.RawMessage, error)
	VMSetConfig(node string, vmid int, params url.Values) (json.RawMessage, error)
}

var _ gpuWriteAPI = (*api.Client)(nil)

// vmState is the fresh state of the target VM.
type vmState struct {
	Digest  string
	HostPCI map[string]string
}

// readTargetVM: stopped, not locked, not a template. Any doubt is a refusal.
func readTargetVM(c gpuWriteAPI, node string, vmid int) (vmState, *gpuErr) {
	sraw, err := c.GetVMStatus(node, vmid)
	if err != nil {
		return vmState{}, proxmoxRefusal(err, "stato della VM non leggibile")
	}
	var st struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(sraw, &st) != nil || st.Status == "" {
		return vmState{}, refuse(http.StatusBadGateway, "unknown", "stato della VM non leggibile")
	}
	if st.Status != "stopped" {
		return vmState{}, refuse(http.StatusConflict, "running", "La VM è accesa: spegnila per cambiare la scheda video")
	}
	craw, err := c.GetVMConfig(node, vmid)
	if err != nil {
		return vmState{}, proxmoxRefusal(err, "configurazione della VM non leggibile")
	}
	var cfg map[string]interface{}
	if json.Unmarshal(craw, &cfg) != nil {
		return vmState{}, refuse(http.StatusBadGateway, "unknown", "configurazione della VM non leggibile")
	}
	digest, _ := cfg["digest"].(string)
	if digest == "" || len(digest) > 128 {
		return vmState{}, refuse(http.StatusBadGateway, "unknown", "configurazione della VM senza digest")
	}
	if lock, _ := cfg["lock"].(string); lock != "" {
		return vmState{}, refuse(http.StatusConflict, "locked", fmt.Sprintf("La VM è bloccata (%s): riprova a operazione finita", clampText(lock, 32)))
	}
	if parseBoolish(cfg["template"]) {
		return vmState{}, refuse(http.StatusConflict, "template", "È un template: una GPU nel template finirebbe in ogni clone")
	}
	hp := map[string]string{}
	for k, v := range cfg {
		if s, ok := v.(string); ok && hostpciKey.MatchString(k) {
			hp[k] = s
		}
	}
	return vmState{Digest: digest, HostPCI: hp}, nil
}

// proxmoxRefusal maps a Proxmox error to a refusal without leaking its body to the browser.
func proxmoxRefusal(err error, what string) *gpuErr {
	msg := err.Error()
	switch {
	case isForbidden(err):
		return refuse(http.StatusForbidden, "forbidden", "Permessi Proxmox insufficienti (servono VM.Config.HWType sulla VM e Mapping.Use sul mapping)")
	case strings.Contains(msg, "does not exist"):
		return refuse(http.StatusNotFound, "not_found", "VM non trovata su questo nodo")
	case strings.Contains(msg, "digest") || strings.Contains(msg, "modified"):
		return refuse(http.StatusConflict, "changed", "La VM è stata modificata nel frattempo: ricarica e riprova")
	}
	log.Printf("gpu: %s: %v", what, err)
	return refuse(http.StatusBadGateway, "proxmox", what)
}

// GPUPlan is what an assignment will write; the client must send Slot and Options back to confirm.
type GPUPlan struct {
	Node    string `json:"node"`
	VMID    int    `json:"vmid"`
	Mapping string `json:"mapping"`
	Card    string `json:"card"`
	Addr    string `json:"addr"`
	Slot    string `json:"slot"`
	Options string `json:"options"`
	digest  string
}

func (p GPUPlan) value() string {
	if p.Options == "" {
		return "mapping=" + p.Mapping
	}
	return "mapping=" + p.Mapping + "," + p.Options
}

// planGPUAssign checks everything with fresh data and returns what would be written. Callers hold gpuLock.
// optionsFor may be nil (no options).
func planGPUAssign(c gpuWriteAPI, node string, vmid int, mapping string, optionsFor func(card string) string, now time.Time) (GPUPlan, *gpuErr) {
	vm, e := readTargetVM(c, node, vmid)
	if e != nil {
		return GPUPlan{}, e
	}
	praw, err := c.GetNodePCI(node)
	if err != nil {
		return GPUPlan{}, proxmoxRefusal(err, "elenco PCI del nodo non leggibile")
	}
	mraw, err := c.GetPCIMappings()
	if err != nil {
		return GPUPlan{}, proxmoxRefusal(err, "mapping PCI non leggibili")
	}
	maps := ParsePCIMappingEntries(mraw)
	sem := make(chan struct{}, gpuConfigWorkers)
	vms, failed, err := loadGPUNodeVMs(c, node, sem)
	if err != nil || len(failed) > 0 {
		if err != nil {
			log.Printf("gpu plan %s/%d: %v", node, vmid, err)
		} else {
			log.Printf("gpu plan %s/%d: config non leggibili: %v", node, vmid, failed)
		}
		return GPUPlan{}, refuse(http.StatusBadGateway, "cannot_check", errGPUCannotCheck.Error())
	}
	// The target's own entry comes from the fresh read above, not from the list (it may have changed).
	others := vms[:0:0]
	for _, v := range vms {
		if v.VMID != vmid {
			others = append(others, v)
		}
	}
	others = append(others, GPUVM{VMID: vmid, Status: "stopped", HostPCI: vm.HostPCI})
	inv := BuildGPUInventory([]GPUNodeInput{{Node: node, Online: true, PCI: ParsePCIList(praw), VMs: others}}, maps, now)

	if _, ok := maps[mapping]; !ok {
		return GPUPlan{}, refuse(http.StatusNotFound, "no_mapping", "Mapping non trovato")
	}
	var entries []MappingEntry
	for _, e := range maps[mapping] {
		if e.Node == node {
			entries = append(entries, e)
		}
	}
	switch {
	case len(entries) == 0:
		return GPUPlan{}, refuse(http.StatusConflict, "other_node", "Il mapping non ha schede su questo nodo: una GPU si assegna solo a VM dello stesso nodo")
	case len(entries) > 1:
		return GPUPlan{}, refuse(http.StatusConflict, "group", "Il mapping contiene più schede su questo nodo: serve un mapping per scheda")
	}
	var card *GPUCard
	for i := range inv.Nodes[0].Cards {
		if inv.Nodes[0].Cards[i].Addr == fullAddr(entries[0].Addr) {
			card = &inv.Nodes[0].Cards[i]
		}
	}
	if card == nil {
		return GPUPlan{}, refuse(http.StatusConflict, "not_gpu", "Il mapping non punta a una scheda video Nvidia di questo nodo")
	}
	if len(card.Uses) > 0 {
		for _, u := range card.Uses {
			if u.VMID == vmid {
				return GPUPlan{}, refuse(http.StatusConflict, "already", "La scheda è già assegnata a questa VM")
			}
		}
		names := make([]string, 0, len(card.Uses))
		for _, u := range card.Uses {
			names = append(names, fmt.Sprintf("%d %s", u.VMID, u.Name))
		}
		return GPUPlan{}, refuse(http.StatusConflict, "busy", "Scheda già in uso: "+strings.Join(names, ", "))
	}
	slot := ""
	for i := 0; i < maxHostPCISlots; i++ {
		if _, used := vm.HostPCI["hostpci"+strconv.Itoa(i)]; !used {
			slot = "hostpci" + strconv.Itoa(i)
			break
		}
	}
	if slot == "" {
		return GPUPlan{}, refuse(http.StatusConflict, "no_slot", "La VM non ha slot PCI liberi")
	}
	opts := ""
	if optionsFor != nil {
		opts = optionsFor(card.Name)
	}
	return GPUPlan{Node: node, VMID: vmid, Mapping: mapping, Card: card.Name, Addr: card.Addr, Slot: slot, Options: opts, digest: vm.Digest}, nil
}

// assignGPU re-plans under the lock and writes only if the plan equals what the user confirmed.
func assignGPU(c gpuWriteAPI, cluster int, node string, vmid int, mapping, wantSlot, wantOpts string, optionsFor func(string) string) (GPUPlan, *gpuErr) {
	l := gpuLock(cluster, node)
	l.Lock()
	defer l.Unlock()
	plan, e := planGPUAssign(c, node, vmid, mapping, optionsFor, time.Now())
	if e != nil {
		return GPUPlan{}, e
	}
	if plan.Slot != wantSlot || plan.Options != wantOpts {
		return GPUPlan{}, refuse(http.StatusConflict, "plan_changed", "La situazione è cambiata dopo la conferma: ricontrolla e riprova")
	}
	params := url.Values{}
	params.Set(plan.Slot, plan.value())
	params.Set("digest", plan.digest)
	if _, err := c.VMSetConfig(node, vmid, params); err != nil {
		return GPUPlan{}, proxmoxRefusal(err, "scrittura della configurazione non riuscita")
	}
	log.Printf("gpu assign: cluster=%d node=%s vmid=%d %s=%s card=%s", cluster, node, vmid, plan.Slot, plan.value(), plan.Addr)
	return plan, nil
}

// unassignGPU removes a mapping-based hostpci line; direct assignments are refused (D25).
func unassignGPU(c gpuWriteAPI, cluster int, node string, vmid int, slot string) *gpuErr {
	l := gpuLock(cluster, node)
	l.Lock()
	defer l.Unlock()
	vm, e := readTargetVM(c, node, vmid)
	if e != nil {
		return e
	}
	val, ok := vm.HostPCI[slot]
	if !ok {
		return refuse(http.StatusNotFound, "no_slot", "Assegnazione non trovata (forse già tolta)")
	}
	k, mapping, _ := strings.Cut(strings.SplitN(val, ",", 2)[0], "=")
	if k != "mapping" {
		return refuse(http.StatusConflict, "direct", "Assegnazione diretta: l'app non la modifica, correggila in Proxmox")
	}
	// Only GPU mappings: a mapping of a NIC or an NVMe on the same VM is not this feature's to remove.
	if e := checkGPUMapping(c, node, mapping); e != nil {
		return e
	}
	params := url.Values{}
	params.Set("delete", slot)
	params.Set("digest", vm.Digest)
	if _, err := c.VMSetConfig(node, vmid, params); err != nil {
		return proxmoxRefusal(err, "scrittura della configurazione non riuscita")
	}
	log.Printf("gpu unassign: cluster=%d node=%s vmid=%d %s (was %s)", cluster, node, vmid, slot, clampText(val, 120))
	return nil
}

// checkGPUMapping: the mapping is ours to remove if, on the VM's node, it points to an NVIDIA display card;
// or, when the card or the entry is missing there (the "to fix" cases), if every entry is an NVIDIA device.
func checkGPUMapping(c gpuWriteAPI, node, mapping string) *gpuErr {
	if !mappingPat.MatchString(mapping) {
		return refuse(http.StatusConflict, "not_gpu", "Mapping non riconosciuto: correggilo in Proxmox")
	}
	praw, err := c.GetNodePCI(node)
	if err != nil {
		return proxmoxRefusal(err, "elenco PCI del nodo non leggibile")
	}
	mraw, err := c.GetPCIMappings()
	if err != nil {
		return proxmoxRefusal(err, "mapping PCI non leggibili")
	}
	pci, entries := ParsePCIList(praw), ParsePCIMappingEntries(mraw)[mapping]
	allNvidia := len(entries) > 0
	for _, e := range entries {
		if e.Node == node {
			if info, ok := pci[e.Addr]; ok {
				if info.IsNvidiaDisplay() {
					return nil
				}
				return refuse(http.StatusConflict, "not_gpu", "Il mapping non è di una scheda video: l'app non lo modifica")
			}
		}
		allNvidia = allNvidia && e.VendorID == "10de"
	}
	if allNvidia {
		return nil
	}
	return refuse(http.StatusConflict, "not_gpu", "Non riesco a stabilire che il mapping sia di una scheda video: correggilo in Proxmox")
}

// ── HTTP ────────────────────────────────────────────────────────────────────

func gpuTarget(r *http.Request) (string, int, *gpuErr) {
	node, vmidS := chi.URLParam(r, "node"), chi.URLParam(r, "vmid")
	if !nodeNamePattern.MatchString(node) || !vmidPattern.MatchString(vmidS) {
		return "", 0, refuse(http.StatusBadRequest, "invalid", "nodo o VMID non validi")
	}
	vmid, _ := strconv.Atoi(vmidS)
	return node, vmid, nil
}

func writeGPUErr(w http.ResponseWriter, e *gpuErr) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": e.Msg, "reason": e.Reason})
}

// optionsFromCache uses the (possibly 15 s old) cluster inventory: options are shown before confirmation,
// they are not a safety check.
func (h *Handler) optionsFromCache(c gpuAPI, idx int) func(string) string {
	return func(card string) string {
		v, err := cephCached("gpus|"+strconv.Itoa(idx), gpuCacheTTL, func() (interface{}, error) { return fetchGPUInventory(c, time.Now()) })
		if err != nil {
			return ""
		}
		return pickGPUOptions(v.(GPUInventory), card)
	}
}

// PlanGPU: GET /api/nodes/{node}/qemu/{vmid}/gpu/plan?mapping=ID — what an assignment would write (no write).
func (h *Handler) PlanGPU(w http.ResponseWriter, r *http.Request) {
	node, vmid, e := gpuTarget(r)
	if e != nil {
		writeGPUErr(w, e)
		return
	}
	mapping := r.URL.Query().Get("mapping")
	if !mappingPat.MatchString(mapping) {
		writeGPUErr(w, refuse(http.StatusBadRequest, "invalid", "mapping non valido"))
		return
	}
	idx := clusterIdx(r)
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	l := gpuLock(idx, node)
	l.Lock()
	plan, e := planGPUAssign(client, node, vmid, mapping, h.optionsFromCache(client, idx), time.Now())
	l.Unlock()
	if e != nil {
		writeGPUErr(w, e)
		return
	}
	writeJSON(w, plan)
}

// AssignGPU: POST /api/nodes/{node}/qemu/{vmid}/gpu {"mapping","slot","options"} (slot/options from the plan).
func (h *Handler) AssignGPU(w http.ResponseWriter, r *http.Request) {
	node, vmid, e := gpuTarget(r)
	if e != nil {
		writeGPUErr(w, e)
		return
	}
	var body struct {
		Mapping string `json:"mapping"`
		Slot    string `json:"slot"`
		Options string `json:"options"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil ||
		!mappingPat.MatchString(body.Mapping) || !hostpciSlotPat.MatchString(body.Slot) || normalizeHostpciOptions("x,"+body.Options) != body.Options {
		writeGPUErr(w, refuse(http.StatusBadRequest, "invalid", "richiesta non valida"))
		return
	}
	idx := clusterIdx(r)
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	plan, e := assignGPU(client, idx, node, vmid, body.Mapping, body.Slot, body.Options, h.optionsFromCache(client, idx))
	cephForget("gpus|" + strconv.Itoa(idx))
	if e != nil {
		writeGPUErr(w, e)
		return
	}
	writeJSON(w, plan)
}

// UnassignGPU: DELETE /api/nodes/{node}/qemu/{vmid}/gpu/{slot}
func (h *Handler) UnassignGPU(w http.ResponseWriter, r *http.Request) {
	node, vmid, e := gpuTarget(r)
	if e != nil {
		writeGPUErr(w, e)
		return
	}
	slot := chi.URLParam(r, "slot")
	if !hostpciSlotPat.MatchString(slot) {
		writeGPUErr(w, refuse(http.StatusBadRequest, "invalid", "slot non valido"))
		return
	}
	idx := clusterIdx(r)
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	e = unassignGPU(client, idx, node, vmid, slot)
	cephForget("gpus|" + strconv.Itoa(idx))
	if e != nil {
		writeGPUErr(w, e)
		return
	}
	writeJSON(w, map[string]string{"slot": slot})
}
