package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"proxmox-manager/internal/buildinfo"
	pmgrscripts "proxmox-manager/tools/pmgr-scripts"
)

type nodeAddAPI interface {
	readinessAPI
	clusterStatusAPI
	GetNodeAPTVersions(string) (json.RawMessage, error)
	GetNodePCI(string) (json.RawMessage, error)
	GetPCIMappings() (json.RawMessage, error)
}
type nodeAddVersion struct {
	Node    string `json:"node"`
	Version string `json:"version"`
	Address string `json:"address"`
	Arch    string `json:"arch"` // amd64 | arm64 | "" (D168)
}
type nodeAddPending struct {
	Node   string `json:"node"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}
type nodeAddFacts struct {
	Versions      []nodeAddVersion `json:"versions"`
	Version       string           `json:"version"`
	VersionsMatch bool             `json:"versions_match"`
	CephRelease   string           `json:"ceph_release"`
	Arch          string           `json:"arch"` // architecture of the cluster: a new node must have the same (D167)
	Addresses     []string         `json:"addresses"`
	Issues        []string         `json:"issues"`
}
type nodeAddResult struct {
	Cluster      string           `json:"cluster"`
	Facts        nodeAddFacts     `json:"facts"`
	ScriptSHA256 string           `json:"script_sha256"`
	Steps        []nodeStep       `json:"steps"`
	NewNodes     []nodeAddPending `json:"new_nodes"`
}

const nodePreparePath = "/root/pmgr-node-prepare.sh"

var nodePrepareSHA256 = func() string {
	data, err := pmgrscripts.FS.ReadFile("bin/pmgr-node-prepare.sh")
	if err != nil {
		panic("embedded node preparation script missing")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}()
var nodeBuildCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var cephPackageVersion = regexp.MustCompile(`^(?:[0-9]+:)?([0-9]+)\.[0-9]`)
var nodeMajorMinor = regexp.MustCompile(`^(?:pve-manager/)?([0-9]{1,2}\.[0-9]{1,2})(?:\.[0-9]+)?(?:/[^\s]+)?$`)

func cephReleaseFromVersions(raw json.RawMessage, err error) (string, bool) {
	rows, ok := readNodeList[struct {
		Package string `json:"Package"`
		Version string `json:"Version"`
	}](raw, err)
	if !ok {
		return "unknown", false
	}
	release := "none"
	for _, r := range rows {
		if r.Package == "" {
			return "unknown", false
		}
		if r.Package != "ceph" && r.Package != "ceph-common" {
			continue
		}
		// An installed package has a nonempty version; apt may also list uninstalled packages.
		if r.Version == "" {
			continue
		}
		m := cephPackageVersion.FindStringSubmatch(r.Version)
		if m == nil {
			return "unknown", false
		}
		next := map[string]string{"18": "reef", "19": "squid", "20": "tentacle"}[m[1]]
		if next == "" || (release != "none" && release != next) {
			return "unknown", false
		}
		release = next
	}
	return release, true
}
func nodePrepareCommands(release string, build buildinfo.BuildInfo) []string {
	commands := []string{}
	if !build.Known || build.Dirty || !nodeBuildCommit.MatchString(build.Commit) {
		return commands
	}
	switch release {
	case "none", "reef", "squid", "tentacle":
	default:
		return commands
	}
	commands = append(commands, "curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 15 --max-time 120 -o "+nodePreparePath+" https://raw.githubusercontent.com/"+updateRepo+"/"+build.Commit+"/tools/pmgr-scripts/bin/pmgr-node-prepare.sh")
	for _, flags := range []string{"", " --apply", " --apply --upgrade"} {
		commands = append(commands, "echo '"+nodePrepareSHA256+"  "+nodePreparePath+"' | sha256sum --check --strict - && bash "+nodePreparePath+" --ceph "+release+flags)
	}
	return commands
}
func nodeAddGPU(c nodeAddAPI, nodes []syncNode) nodeStep {
	s := newNodeStep("gpu", "Mapping delle GPU", "Crea un mapping per scheda in Datacenter > Resource Mappings.")
	raw, err := c.GetPCIMappings()
	maps, ok := readStepMappings(raw, err)
	if !ok {
		unknownNodeStep(&s, "Mapping PCI non leggibili; verifica le schede in Datacenter > Resource Mappings.")
		return s
	}
	inputs := []GPUNodeInput{}
	readable := true
	for _, n := range nodes {
		if !n.Online {
			s.Items = append(s.Items, n.Name+": inventario GPU non verificabile (offline)")
			readable = false
			continue
		}
		raw, err = c.GetNodePCI(n.Name)
		rows, ok := readNodeList[map[string]any](raw, err)
		if !ok || len(rows) > maxPCIDevices {
			s.Items = append(s.Items, n.Name+": inventario GPU non leggibile")
			readable = false
			continue
		}
		valid := true
		for _, r := range rows {
			id, _ := r["id"].(string)
			class, _ := r["class"].(string)
			vendor, _ := r["vendor"].(string)
			if !pciAddrPat.MatchString(id) || class == "" || vendor == "" {
				valid = false
			}
		}
		if !valid {
			s.Items = append(s.Items, n.Name+": inventario GPU incompleto")
			readable = false
			continue
		}
		inputs = append(inputs, GPUNodeInput{Node: n.Name, Online: true, PCI: ParsePCIList(raw)})
	}
	inv := BuildGPUInventory(inputs, maps, time.Time{})
	for _, n := range inv.Nodes {
		missing := 0
		for _, card := range n.Cards {
			if len(card.Mappings) == 0 {
				missing++
			}
		}
		s.Items = append(s.Items, fmt.Sprintf("%s: %d schede Nvidia senza mapping su %d", n.Node, missing, len(n.Cards)))
		if missing > 0 {
			s.Status = "todo"
		}
	}
	if !readable {
		unknownNodeStep(&s, "Inventario GPU non completamente leggibile. Crea un mapping per scheda in Datacenter > Resource Mappings.")
	}
	return s
}
func nodeAddPendingNodes(c nodeAddAPI, nodes []syncNode, label, token string) []nodeAddPending {
	pending := []nodeAddPending{}
	report, err := BuildReadiness(c, 0, label, token, time.Now())
	byName := map[string]readinessNode{}
	if err == nil {
		for _, n := range report.Nodes {
			byName[n.Node] = n
		}
	}
	for _, n := range nodes {
		reasons := []string{}
		state := "todo"
		data, ok := byName[n.Name]
		if err != nil || !ok || len(data.Checks) == 0 {
			reasons = append(reasons, "Preparazione non verificabile")
			state = "unknown"
		} else {
			for _, ch := range append(append([]readinessCheck{}, report.ClusterChecks...), data.Checks...) {
				if ch.Status != "ok" {
					reasons = append(reasons, ch.Title+": "+ch.Detail)
					if ch.Status == "unknown" {
						state = "unknown"
					}
				}
			}
		}
		if len(reasons) > 0 {
			pending = append(pending, nodeAddPending{Node: n.Name, State: state, Detail: strings.Join(reasons, "; ")})
		}
	}
	return pending
}

// BuildNodeAdd reads cluster facts and reuses readiness and GPU inventory checks.
func BuildNodeAdd(c nodeAddAPI, label, token string, build buildinfo.BuildInfo) (nodeAddResult, error) {
	res := nodeAddResult{Cluster: label, ScriptSHA256: nodePrepareSHA256, Steps: []nodeStep{}, NewNodes: []nodeAddPending{}, Facts: nodeAddFacts{Versions: []nodeAddVersion{}, Addresses: []string{}, Issues: []string{}, CephRelease: "unknown"}}
	nodes, err := stepClusterNodes(c)
	if err != nil {
		return res, errors.New("impossibile leggere i nodi del cluster")
	}
	source := ""
	allVersions := true
	versions := map[string]bool{}
	addresses := map[string]bool{}
	for _, n := range nodes {
		if !n.Online {
			continue
		}
		if source == "" {
			source = n.Name
		}
		v := nodeAddVersion{Node: n.Name}
		if ip, e := netip.ParseAddr(n.Address); e == nil && ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() {
			v.Address = ip.String()
			if !addresses[v.Address] {
				res.Facts.Addresses = append(res.Facts.Addresses, v.Address)
				addresses[v.Address] = true
			}
		} else {
			res.Facts.Issues = append(res.Facts.Issues, n.Name+": indirizzo corosync non valido o assente")
		}
		raw, err := c.GetNodeStatus(n.Name)
		var st struct {
			Version string `json:"pveversion"`
		}
		if readinessRead(raw, err, &st) {
			if m := nodeMajorMinor.FindStringSubmatch(st.Version); m != nil {
				v.Version = m[1]
				versions[v.Version] = true
			}
			v.Arch = nodeStatusArch(raw)
		}
		if v.Version == "" {
			allVersions = false
			res.Facts.Issues = append(res.Facts.Issues, n.Name+": versione Proxmox non leggibile")
		}
		res.Facts.Versions = append(res.Facts.Versions, v)
	}
	archs := map[string]string{}
	for _, v := range res.Facts.Versions {
		archs[v.Node] = v.Arch
	}
	if arch, complete := clusterArchOf(archs); arch == archMixed {
		res.Facts.Arch = archMixed
		res.Facts.Issues = append(res.Facts.Issues, "Il cluster ha nodi di architetture diverse: "+strings.Join(sortedArchNodes(archs), ", "))
	} else if complete && arch != "" {
		res.Facts.Arch = arch
	} else if len(archs) > 0 {
		res.Facts.Issues = append(res.Facts.Issues, "Architettura non leggibile su tutti i nodi")
	}
	res.Facts.VersionsMatch = allVersions && len(versions) == 1
	if res.Facts.VersionsMatch {
		for v := range versions {
			res.Facts.Version = v
		}
	}
	if len(versions) > 1 {
		res.Facts.Issues = append(res.Facts.Issues, "I nodi online non hanno tutti la stessa versione maggiore/minore di Proxmox")
	}
	if source != "" {
		raw, err := c.GetNodeAPTVersions(source)
		release, ok := cephReleaseFromVersions(raw, err)
		res.Facts.CephRelease = release
		if !ok {
			res.Facts.Issues = append(res.Facts.Issues, "Release Ceph non verificabile dai pacchetti installati")
		}
	} else {
		res.Facts.Issues = append(res.Facts.Issues, "Nessun nodo online: versione e release Ceph non verificabili")
	}
	sort.Strings(res.Facts.Issues)
	prerequisites := newNodeStep("prerequisites", "Prerequisiti", "Sul nuovo nodo: stessa versione maggiore di Proxmox del cluster, ora sincronizzata, nome DNS risolvibile e reti management, cluster e Ceph raggiungibili. Il nodo deve essere privo di VM e container.")
	prerequisites.Status = "todo"
	if !res.Facts.VersionsMatch {
		unknownNodeStep(&prerequisites, "Versione comune del cluster non verificabile. Controlla le versioni prima di preparare il nuovo nodo. "+prerequisites.Detail)
	}
	prepare := newNodeStep("prepare", "Preparazione del nuovo nodo", "Come root SUL NUOVO NODO: scarica lo script, verifica SHA-256 prima di ogni esecuzione, controlla senza modifiche, poi --apply. Infine --apply --upgrade; riavvia se necessario. HA e corosync restano invariati.")
	prepare.Status = "todo"
	prepare.Commands = nodePrepareCommands(res.Facts.CephRelease, build)
	if len(prepare.Commands) == 0 {
		unknownNodeStep(&prepare, "Comandi non disponibili: occorrono una build nota e pulita e una release Ceph verificabile. Usa una release firmata del manager.")
	}
	join := newNodeStep("join", "Ingresso nel cluster", "Esegui UNO dei comandi come root SUL NUOVO NODO, dopo preparazione e riavvio, senza guest presenti.")
	join.Status = "todo"
	for _, ip := range res.Facts.Addresses {
		join.Commands = append(join.Commands, "pvecm add "+ip)
	}
	if len(join.Commands) == 0 {
		unknownNodeStep(&join, "Nessun indirizzo valido di un nodo online per pvecm add.")
	}
	res.Steps = append(res.Steps, prerequisites, prepare, join)
	if res.Facts.CephRelease != "none" {
		ceph := newNodeStep("ceph", "Installazione di Ceph", "Sul nuovo nodo, come root. Poi crea gli OSD dalla GUI di Proxmox o con pveceph osd create. Pianifica MON e MGR secondo il cluster.")
		ceph.Status = "todo"
		if res.Facts.CephRelease == "unknown" {
			unknownNodeStep(&ceph, "Release Ceph non verificabile: non installare una release scelta automaticamente.")
		} else {
			ceph.Commands = append(ceph.Commands, "pveceph install --repository no-subscription --version "+res.Facts.CephRelease)
		}
		res.Steps = append(res.Steps, ceph)
	}
	storage := newNodeStep("storage_sync", "Storage, snippet e sincronizzazione", "Dopo l'ingresso, il nodo compare in Preparazione nodi: verifica storage, contenuto Snippets e bridge; prepara l'accesso per la sincronizzazione, conferma l'impronta SSH e sincronizza script e snippet.")
	storage.Status = "todo"
	storage.Link = "#/nodeprep"
	res.Steps = append(res.Steps, storage, nodeAddGPU(c, nodes))
	for i := range res.Steps {
		res.Steps[i].CheckStatus = res.Steps[i].Status
	}
	res.NewNodes = nodeAddPendingNodes(c, nodes, label, token)
	return res, nil
}
func (h *Handler) GetNodeAdd(w http.ResponseWriter, r *http.Request) {
	idx, cl, c, ok := h.nodeStepsSetup(w, r)
	if !ok {
		return
	}
	lock, ok := beginNodeSteps(w, idx)
	if !ok {
		return
	}
	defer lock.Unlock()
	res, err := BuildNodeAdd(c, cl.Label, cl.APIToken, buildinfo.Info())
	if err != nil {
		writeError(w, errors.New("impossibile leggere i nodi del cluster"), http.StatusBadGateway)
		return
	}
	writeJSON(w, res)
}
