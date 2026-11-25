package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"proxmox-manager/internal/nodessh"
)

type nodeRemovalAPI interface {
	clusterStatusAPI
	GetClusterResources(string) (json.RawMessage, error)
	GetHAResources() (json.RawMessage, error)
	GetHARules() (json.RawMessage, error)
	GetHAGroups() (json.RawMessage, error)
	GetReplication() (json.RawMessage, error)
	GetStorage() (json.RawMessage, error)
	GetPCIMappings() (json.RawMessage, error)
	GetCephStatus() (json.RawMessage, error)
	GetCephOSD(string) (json.RawMessage, error)
	GetCephServices(string, string) (json.RawMessage, error)
}
type nodeRemovalResult struct {
	Cluster  string     `json:"cluster"`
	Node     string     `json:"node"`
	Address  string     `json:"address"`
	Pinned   bool       `json:"pinned"`
	PinKnown bool       `json:"pin_known"`
	Steps    []nodeStep `json:"steps"`
}
type removalGuest struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Node   string `json:"node"`
}

var errNodeNotMember = errors.New("nodo non trovato nel cluster")
var replicationJobPattern = regexp.MustCompile(`^[0-9]{1,9}-[0-9]{1,3}$`)

func removalGuests(c nodeRemovalAPI, node string) (nodeStep, map[int]string, bool) {
	s := newNodeStep("guests", "VM e container", "Il nodo non ospita VM, container o template.")
	s.Link = "#/balance"
	raw, err := c.GetClusterResources("vm")
	rows, ok := readNodeList[removalGuest](raw, err)
	locations := map[int]string{}
	if ok {
		for _, g := range rows {
			if g.VMID <= 0 || g.VMID > 999999999 || (g.Type != "qemu" && g.Type != "lxc") || !nodeNamePattern.MatchString(g.Node) || g.Status == "" {
				ok = false
				continue
			}
			locations[g.VMID] = g.Node
			if g.Node == node {
				s.Items = append(s.Items, fmt.Sprintf("%d · %s · %s · %s", g.VMID, g.Name, g.Type, g.Status))
			}
		}
	}
	sort.Strings(s.Items)
	if !ok {
		unknownNodeStep(&s, "Elenco VM, container e template non completamente leggibile.")
	} else if len(s.Items) > 0 {
		s.Status = "todo"
		s.Detail = "Svuota il nodo dal Bilanciamento (Simula lo spegnimento) o spostali a mano; i template vanno spostati o cancellati"
	}
	return s, locations, ok
}
func nodeInList(list, node string) bool {
	for _, part := range strings.Split(list, ",") {
		name, _, _ := strings.Cut(part, ":")
		if strings.TrimSpace(name) == node {
			return true
		}
	}
	return false
}
func removalHA(c nodeRemovalAPI, node string, guests map[int]string, guestsOK bool) nodeStep {
	s := newNodeStep("ha", "Alta disponibilita'", "Nessun riferimento al nodo in HA.")
	unknown := !guestsOK
	raw, err := c.GetHAResources()
	rows, ok := readNodeList[struct {
		SID string `json:"sid"`
	}](raw, err)
	if !ok {
		unknown = true
	}
	for _, r := range rows {
		kind, id, valid := strings.Cut(r.SID, ":")
		vmid, e := strconv.Atoi(id)
		if !valid || (kind != "vm" && kind != "ct") || e != nil || vmid <= 0 || vmid > 999999999 {
			unknown = true
			continue
		}
		location, exists := guests[vmid]
		if !exists {
			unknown = true
			continue
		}
		if location == node {
			s.Items = append(s.Items, "Risorsa HA: "+r.SID)
		}
	}
	raw, err = c.GetHARules()
	rules, ok := readNodeList[struct {
		ID    string `json:"id"`
		Rule  string `json:"rule"`
		Type  string `json:"type"`
		Nodes string `json:"nodes"`
	}](raw, err)
	if !ok {
		unknown = true
	}
	for _, r := range rules {
		if r.ID == "" {
			r.ID = r.Rule
		}
		if r.ID == "" || r.Type == "" {
			unknown = true
			continue
		}
		if r.Type == "node-affinity" {
			if r.Nodes == "" {
				unknown = true
			}
			if nodeInList(r.Nodes, node) {
				s.Items = append(s.Items, "Regola HA: "+r.ID)
			}
		}
	}
	raw, err = c.GetHAGroups()
	groups, ok := readNodeList[struct {
		Group string `json:"group"`
		Nodes string `json:"nodes"`
	}](raw, err)
	if !ok {
		unknown = true
	}
	for _, g := range groups {
		if g.Group == "" || g.Nodes == "" {
			unknown = true
			continue
		}
		if nodeInList(g.Nodes, node) {
			s.Items = append(s.Items, "Gruppo HA: "+g.Group)
		}
	}
	sort.Strings(s.Items)
	if len(s.Items) > 0 {
		s.Status = "todo"
		s.Detail = "Sposta le risorse HA e modifica regole e gruppi in Datacenter > HA."
	}
	if unknown {
		unknownNodeStep(&s, "Risorse, regole o gruppi HA non completamente leggibili. Verifica in Datacenter > HA.")
	}
	return s
}
func removalReplication(c nodeRemovalAPI, node string, guests map[int]string, guestsOK bool) nodeStep {
	s := newNodeStep("replication", "Replica", "Nessun job di replica coinvolge il nodo.")
	raw, err := c.GetReplication()
	rows, ok := readNodeList[struct {
		ID     string          `json:"id"`
		Target string          `json:"target"`
		Guest  json.RawMessage `json:"guest"`
	}](raw, err)
	if !guestsOK {
		ok = false
	}
	for _, r := range rows {
		if !replicationJobPattern.MatchString(r.ID) || !nodeNamePattern.MatchString(r.Target) {
			ok = false
			continue
		}
		guestID, _, _ := strings.Cut(r.ID, "-")
		vmid, _ := strconv.Atoi(guestID) // the pattern guarantees a bounded integer
		if len(r.Guest) > 0 {
			id := strings.Trim(string(r.Guest), `"`)
			n, e := strconv.Atoi(id)
			if e != nil || n <= 0 || n > 999999999 {
				ok = false
				continue
			}
			vmid = n
		}
		location, exists := guests[vmid]
		if !exists {
			ok = false
		}
		if r.Target == node || location == node {
			s.Items = append(s.Items, r.ID+" · destinazione "+r.Target)
			s.Commands = append(s.Commands, "pvesr delete "+r.ID)
		}
	}
	if len(s.Items) > 0 {
		s.Status = "todo"
		s.Detail = "Elimina i job di replica che coinvolgono il nodo."
	}
	if !ok {
		unknownNodeStep(&s, "Job di replica o collocazione dei guest non completamente leggibili.")
	}
	return s
}
func removalStorage(c nodeRemovalAPI, node string) nodeStep {
	s := newNodeStep("storage", "Restrizioni degli storage", "Nessuno storage e' limitato a questo nodo.")
	raw, err := c.GetStorage()
	rows, ok := readNodeList[struct {
		Storage string `json:"storage"`
		Nodes   string `json:"nodes"`
	}](raw, err)
	for _, r := range rows {
		if !storageNamePattern.MatchString(r.Storage) {
			ok = false
			continue
		}
		if r.Nodes == "" {
			continue
		}
		keep := []string{}
		found := false
		valid := true
		for _, name := range strings.Split(r.Nodes, ",") {
			if !nodeNamePattern.MatchString(name) {
				valid = false
			}
			if name == node {
				found = true
			} else {
				keep = append(keep, name)
			}
		}
		if !valid {
			ok = false
			continue
		}
		if !found {
			continue
		}
		s.Items = append(s.Items, r.Storage)
		s.Status = "todo"
		s.Detail = "Togli il nodo dalle restrizioni degli storage."
		if len(keep) == 0 {
			s.Items = append(s.Items, r.Storage+": togli lo storage o assegnalo ad altri nodi")
		} else {
			s.Commands = append(s.Commands, "pvesm set "+r.Storage+" --nodes "+strings.Join(keep, ","))
		}
	}
	if !ok {
		unknownNodeStep(&s, "Restrizioni degli storage non completamente leggibili o nomi non validi.")
	}
	return s
}
func readStepMappings(raw json.RawMessage, err error) (MappingSet, bool) {
	rows, ok := readNodeList[struct {
		ID  string   `json:"id"`
		Map []string `json:"map"`
	}](raw, err)
	if !ok {
		return nil, false
	}
	for _, r := range rows {
		if !mappingPat.MatchString(r.ID) || len(r.Map) == 0 || len(r.Map) > 64 {
			return nil, false
		}
		for _, entry := range r.Map {
			node, path := "", ""
			for _, kv := range strings.Split(entry, ",") {
				k, v, _ := strings.Cut(kv, "=")
				switch strings.TrimSpace(k) {
				case "node":
					node = strings.TrimSpace(v)
				case "path":
					path = strings.TrimSpace(v)
				}
			}
			if !nodeNamePattern.MatchString(node) || normalizePCIAddr(path) == "" {
				return nil, false
			}
		}
	}
	return ParsePCIMappingEntries(raw), true
}
func removalMappings(c nodeRemovalAPI, node string) nodeStep {
	s := newNodeStep("pci_mapping", "Mapping PCI", "Nessun mapping PCI cita il nodo.")
	raw, err := c.GetPCIMappings()
	maps, ok := readStepMappings(raw, err)
	if !ok {
		unknownNodeStep(&s, "Mapping PCI non completamente leggibili.")
		return s
	}
	for id, entries := range maps {
		for _, e := range entries {
			if e.Node == node {
				s.Items = append(s.Items, id)
				break
			}
		}
	}
	sort.Strings(s.Items)
	if len(s.Items) > 0 {
		s.Status = "todo"
		s.Detail = "Modifica le voci del nodo in Datacenter > Resource Mappings."
	}
	return s
}

func removalOSDID(value any) (int, bool) {
	switch id := value.(type) {
	case float64:
		if id >= 0 && id <= 999999999 && math.Trunc(id) == id {
			return int(id), true
		}
	case string:
		if len(id) == 0 || len(id) > 9 || strings.Trim(id, "0123456789") != "" {
			return 0, false
		}
		n, err := strconv.Atoi(id)
		return n, err == nil
	}
	return 0, false
}

// Validate the complete tree before using the inventory normalizer: its permissive
// defaults are suitable for display, but cannot prove that removal is safe.
func removalOSDs(raw json.RawMessage, err error) ([]cephOSD, bool) {
	var tree struct {
		Root map[string]any `json:"root"`
	}
	if !readinessRead(raw, err, &tree) || tree.Root == nil {
		return nil, false
	}
	count := 0
	seen := map[int]bool{}
	var walk func(map[string]any, int, string) bool
	walk = func(row map[string]any, depth int, host string) bool {
		count++
		if depth > 8 || count > cephMaxOSDs {
			return false
		}
		kind, _ := row["type"].(string)
		if kind == "osd" {
			id, valid := removalOSDID(row["id"])
			if !valid || seen[id] {
				return false
			}
			seen[id] = true
			if h, ok := row["host"].(string); ok && h != "" {
				host = h
			}
			if !nodeNamePattern.MatchString(host) {
				return false
			}
			row["host"] = host
			status, _ := row["status"].(string)
			in := fmt.Sprint(row["in"])
			return (status == "up" || status == "down") && (in == "0" || in == "1" || in == "true" || in == "false")
		}
		if kind == "host" {
			host, _ = row["name"].(string)
			if !nodeNamePattern.MatchString(host) {
				return false
			}
		}
		// Proxmox's synthetic root has no type; every other bucket must have one.
		if kind == "" && depth > 0 {
			return false
		}
		raw, present := row["children"]
		if !present {
			// a bucket without children (e.g. a host with no OSD) is valid and empty; an empty root is not an answer
			return depth > 0
		}
		children, ok := raw.([]any)
		if !ok {
			return false
		}
		for _, child := range children {
			r, ok := child.(map[string]any)
			if !ok || !walk(r, depth+1, host) {
				return false
			}
		}
		return true
	}
	if !walk(tree.Root, 0, "") {
		return nil, false
	}
	validated, e := json.Marshal(tree)
	if e != nil {
		return nil, false
	}
	return normalizeCephOSDs(validated), true
}
func removalCeph(c nodeRemovalAPI, node string, nodes []syncNode) (nodeStep, nodeStep) {
	osd := newNodeStep("ceph_osd", "OSD Ceph", "Nessun OSD del nodo nella mappa.")
	svc := newNodeStep("ceph_services", "Servizi Ceph", "Nessun MON, MGR o MDS sul nodo.")
	raw, err := c.GetCephStatus()
	if classifyCephError(err) == cephNotConfigured {
		osd.Detail = "Ceph non configurato"
		svc.Detail = osd.Detail
		return osd, svc
	}
	var status struct {
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
	}
	if !readinessRead(raw, err, &status) || (status.Health.Status != "HEALTH_OK" && status.Health.Status != "HEALTH_WARN" && status.Health.Status != "HEALTH_ERR") {
		unknownNodeStep(&osd, "Stato Ceph non leggibile.")
		unknownNodeStep(&svc, "Servizi Ceph non verificabili senza lo stato del cluster.")
		return osd, svc
	}
	source := ""
	for _, n := range nodes {
		if n.Online && n.Name != node {
			source = n.Name
			break
		}
	}
	if source == "" {
		for _, n := range nodes {
			if n.Online {
				source = n.Name
				break
			}
		}
	}
	if source == "" {
		unknownNodeStep(&osd, "Nessun nodo online per leggere Ceph.")
		unknownNodeStep(&svc, osd.Detail)
		return osd, svc
	}
	raw, err = c.GetCephOSD(source)
	osds, ok := removalOSDs(raw, err)
	if !ok {
		unknownNodeStep(&osd, "Albero OSD non completamente leggibile.")
	} else {
		mine := []cephOSD{}
		for _, o := range osds {
			if o.Host == node {
				mine = append(mine, o)
			}
		}
		sort.Slice(mine, func(i, j int) bool { return mine[i].ID < mine[j].ID })
		for _, o := range mine {
			in := "out"
			if o.In {
				in = "in"
			}
			osd.Items = append(osd.Items, fmt.Sprintf("OSD %d · %s · %s", o.ID, in, o.Status))
		}
		if len(mine) > 0 {
			osd.Status = "todo"
			// Commands for ONE OSD at a time, so nobody can paste the destruction of several OSDs at once.
			// The destroy runs only if Ceph itself says the OSD can go without losing data (safe-to-destroy):
			// a permanent HEALTH_WARN (e.g. AUTH warnings) does not block the removal, missing data copies do.
			first := mine[0]
			if first.In {
				osd.Commands = append(osd.Commands, fmt.Sprintf("ceph osd out %d", first.ID))
				osd.Detail = fmt.Sprintf("Un OSD alla volta. Ora: metti fuori l'OSD %d e aspetta che Ceph ridistribuisca i dati (recupero finito nella pagina Ceph), poi ricontrolla.", first.ID)
			} else {
				osd.Commands = append(osd.Commands, fmt.Sprintf("ceph osd safe-to-destroy osd.%d && systemctl stop ceph-osd@%d && pveceph osd destroy %d --cleanup", first.ID, first.ID, first.ID))
				osd.Detail = fmt.Sprintf("Un OSD alla volta. L'OSD %d e' fuori: il comando lo distrugge solo se Ceph conferma che i dati sono al sicuro (safe-to-destroy); se risponde di no, aspetta la fine del recupero e riprova. Da eseguire sul nodo da rimuovere.", first.ID)
			}
			if len(mine) > 1 {
				osd.Detail += fmt.Sprintf(" Restano altri %d OSD su questo nodo: i loro comandi compaiono dopo.", len(mine)-1)
			}
			osd.Detail += " Stato di Ceph: " + status.Health.Status + "."
		}
	}
	readable := true
	for _, kind := range []string{"mon", "mgr", "mds"} {
		raw, err = c.GetCephServices(source, kind)
		rows, ok := readNodeList[struct {
			Name string `json:"name"`
			Host string `json:"host"`
		}](raw, err)
		if !ok {
			readable = false
			continue
		}
		unique := map[string]bool{}
		here := false
		others := 0
		for _, r := range rows {
			if !nodeNamePattern.MatchString(r.Name) || (r.Host != "" && !nodeNamePattern.MatchString(r.Host)) || unique[r.Name] {
				ok = false
				continue
			}
			unique[r.Name] = true
			host := r.Host
			if host == "" {
				host = r.Name
			}
			if host == node {
				here = true
			} else {
				others++
			}
		}
		if !ok {
			readable = false
			continue
		}
		if !here {
			continue
		}
		svc.Status = "todo"
		svc.Items = append(svc.Items, strings.ToUpper(kind)+" · "+node)
		svc.Detail = "Rimuovi i servizi dal nodo, dopo gli OSD."
		switch {
		case kind == "mon" && others < 3:
			svc.Items = append(svc.Items, "prima aggiungi un monitor su un altro nodo: con meno di 3 monitor il quorum di Ceph e' fragile")
		case kind == "mgr" && others < 1:
			svc.Items = append(svc.Items, "Prima aggiungi un MGR su un altro nodo: questo e' l'ultimo MGR.")
		default:
			svc.Commands = append(svc.Commands, "pveceph "+kind+" destroy "+node)
		}
	}
	if len(svc.Items) > 0 {
		svc.Detail += " " + strings.Join(svc.Items, "; ")
	}
	if !readable {
		unknownNodeStep(&svc, "MON, MGR o MDS non completamente leggibili.")
	}
	return osd, svc
}
func removalCleanup(node string) nodeStep {
	s := newNodeStep("cleanup", "Pulizia finale", "Solo dopo delnode: rimuovi la cartella residua su un ALTRO nodo; aggiorna i certificati su ogni nodo rimasto. Nel manager dimentica l'impronta SSH, se presente.")
	s.Status = "todo"
	if nodeNamePattern.MatchString(node) {
		s.Commands = []string{"rm -rf /etc/pve/nodes/" + node, "pvecm updatecerts --force"}
	}
	return s
}

// BuildNodeRemoval only reads current Proxmox data. It never caches a safety check.
func BuildNodeRemoval(c nodeRemovalAPI, label, node string) (nodeRemovalResult, error) {
	res := nodeRemovalResult{Cluster: label, Node: node, Steps: []nodeStep{}}
	if !nodeNamePattern.MatchString(node) {
		return res, errors.New("nodo non valido")
	}
	nodes, err := stepClusterNodes(c)
	if err != nil {
		return res, errors.New("impossibile leggere i nodi del cluster")
	}
	member := false
	online := false
	remainingOnline := 0
	for _, n := range nodes {
		if n.Name == node {
			member = true
			online = n.Online
			if nodessh.ValidAddress(n.Address) {
				res.Address = n.Address
			}
		} else if n.Online {
			remainingOnline++
		}
	}
	if !member {
		return res, errNodeNotMember
	}
	guests, locations, guestsOK := removalGuests(c, node)
	res.Steps = append(res.Steps, guests, removalHA(c, node, locations, guestsOK), removalReplication(c, node, locations, guestsOK), removalStorage(c, node), removalMappings(c, node))
	osd, services := removalCeph(c, node, nodes)
	res.Steps = append(res.Steps, osd, services)
	power := newNodeStep("power_off", "Spegnimento del nodo", "Nodo offline.")
	if online {
		power.Status = "todo"
		power.Detail = "Spegni il nodo (dalla pagina Cluster o da console). Non deve piu' tornare in rete con la stessa identita' senza reinstallazione."
	}
	power.Link = "#/cluster"
	quorum := newNodeStep("quorum", "Quorum dopo la rimozione", "")
	total := len(nodes) - 1
	required := total/2 + 1
	quorum.Detail = fmt.Sprintf("%d nodi online rimasti su %d; quorum richiesto: %d.", remainingOnline, total, required)
	if remainingOnline < required {
		quorum.Status = "todo"
		quorum.Detail += " Ripristina il quorum prima di proseguire."
	}
	del := newNodeStep("delnode", "Rimozione dal cluster", "Da eseguire come root su un ALTRO nodo del cluster.")
	del.Status = "todo"
	del.Commands = append(del.Commands, "pvecm delnode "+node)
	res.Steps = append(res.Steps, power, quorum, del, removalCleanup(node))
	lockNodeSteps(res.Steps)
	return res, nil
}
func (h *Handler) GetNodeRemoval(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("node")
	if !nodeNamePattern.MatchString(node) {
		writeError(w, errors.New("nodo non valido"), http.StatusBadRequest)
		return
	}
	idx, cl, c, ok := h.nodeStepsSetup(w, r)
	if !ok {
		return
	}
	h.serveNodeRemoval(w, r, idx, cl.Label, c)
}

func (h *Handler) serveNodeRemoval(w http.ResponseWriter, r *http.Request, idx int, label string, c nodeRemovalAPI) {
	w.Header().Set("Cache-Control", "no-store")
	node := r.URL.Query().Get("node")
	if !nodeNamePattern.MatchString(node) {
		writeError(w, errors.New("nodo non valido"), http.StatusBadRequest)
		return
	}
	lock, ok := beginNodeSteps(w, idx)
	if !ok {
		return
	}
	defer lock.Unlock()
	res, err := BuildNodeRemoval(c, label, node)
	if errors.Is(err, errNodeNotMember) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": errNodeNotMember.Error(), "reason": "node_not_member", "cluster": label})
		return
	}
	if err != nil {
		writeError(w, errors.New("impossibile leggere i nodi del cluster"), http.StatusBadGateway)
		return
	}
	if h.ssh != nil && res.Address != "" {
		_, res.Pinned, err = h.ssh.Pinned(res.Address)
		res.PinKnown = err == nil
	}
	writeJSON(w, res)
}
