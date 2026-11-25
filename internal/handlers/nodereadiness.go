package handlers

import (
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

type readinessAPI interface {
	GetNodes() (json.RawMessage, error)
	GetNodeStatus(string) (json.RawMessage, error)
	GetNodeTime(string) (json.RawMessage, error)
	GetNodeStorage(string) (json.RawMessage, error)
	GetNodeNetworks(string) (json.RawMessage, error)
	GetStorageContent(string, string, string) (json.RawMessage, error)
	GetClusterResources(string) (json.RawMessage, error)
	GetVMConfig(string, int) (json.RawMessage, error)
	GetPermissions(string) (json.RawMessage, error)
}

type readinessCheck struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Commands []string `json:"commands"`
}
type readinessNode struct {
	Node    string           `json:"node"`
	Online  bool             `json:"online"`
	Version string           `json:"version"`
	Checks  []readinessCheck `json:"checks"`
}
type readinessSummary struct {
	OK      int `json:"ok"`
	Warn    int `json:"warn"`
	Fail    int `json:"fail"`
	Unknown int `json:"unknown"`
}
type readinessResult struct {
	Cluster       string           `json:"cluster"`
	GeneratedAt   int64            `json:"generated_at"`
	TokenID       string           `json:"token_id"`
	Summary       readinessSummary `json:"summary"`
	ClusterChecks []readinessCheck `json:"cluster_checks"`
	Nodes         []readinessNode  `json:"nodes"`
	Partial       []string         `json:"partial"`
}
type readinessTemplate struct {
	id      int
	info    tplInfo
	bridges []string
}
type readinessStorage struct {
	Storage string          `json:"storage"`
	Content string          `json:"content"`
	Active  json.RawMessage `json:"active"`
	Enabled json.RawMessage `json:"enabled"`
}
type readinessNodeData struct {
	node    readinessNode
	epoch   int64
	timeOK  bool
	partial []string
}

var readinessToken = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}@[A-Za-z0-9._-]{1,32}![A-Za-z0-9._-]{1,64}$`)
var readinessContent = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
var readinessBridge = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)
var readinessNetKey = regexp.MustCompile(`^net[0-9]{1,2}$`)
var readinessVersion = regexp.MustCompile(`(?:pve-manager/)?([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)
var readinessPrivileges = []string{"Sys.Audit", "Sys.PowerMgmt", "VM.Audit", "VM.PowerMgmt", "VM.Snapshot", "VM.Migrate", "VM.Clone", "VM.Allocate", "VM.Config.Disk", "VM.Config.CPU", "VM.Config.Memory", "VM.Config.Network", "VM.Config.Cloudinit", "VM.Config.Options", "VM.Config.HWType", "VM.GuestAgent.Audit", "VM.GuestAgent.Unrestricted", "Datastore.Audit", "Datastore.AllocateSpace", "Pool.Audit", "Mapping.Audit", "Mapping.Use", "SDN.Audit", "SDN.Use"}

func readinessCheckOf(id, status, title, detail string) readinessCheck {
	return readinessCheck{id, status, title, detail, []string{}}
}
func readinessRead(raw json.RawMessage, err error, dest any) bool {
	return err == nil && len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, dest) == nil
}
func readinessList(xs []string) string {
	sort.Strings(xs)
	if len(xs) > 8 {
		return strings.Join(xs[:8], ", ") + fmt.Sprintf(" e altri %d", len(xs)-8)
	}
	return strings.Join(xs, ", ")
}
func readinessFlag(raw json.RawMessage, defaultValue bool) bool {
	if len(raw) == 0 {
		return defaultValue
	}
	var x any
	if json.Unmarshal(raw, &x) != nil {
		return false
	}
	return parseBoolish(x)
}
func readinessTokenID(token string) string {
	id, _, ok := strings.Cut(token, "=")
	if !ok || !readinessToken.MatchString(id) {
		return ""
	}
	return id
}

func readinessTemplates(c readinessAPI) ([]readinessTemplate, int, bool) {
	raw, err := c.GetClusterResources("vm")
	var rows []map[string]interface{}
	if !readinessRead(raw, err, &rows) {
		return nil, 0, false
	}
	out := []readinessTemplate{}
	count := 0
	complete := true
	for _, row := range rows {
		if !parseBoolish(row["template"]) || row["type"] != "qemu" {
			continue
		}
		count++
		node, _ := row["node"].(string)
		id := int(intField(row, "vmid"))
		if count > 64 || !nodeNamePattern.MatchString(node) || id <= 0 {
			complete = false
			continue
		}
		raw, err := c.GetVMConfig(node, id)
		var cfg map[string]interface{}
		if !readinessRead(raw, err, &cfg) || cfg == nil {
			complete = false
			continue
		}
		t := readinessTemplate{id: id, info: parseTemplateInfo(cfg, "", 0)}
		for key, v := range cfg {
			if !readinessNetKey.MatchString(key) {
				continue
			}
			line, _ := v.(string)
			for _, item := range strings.Split(line, ",") {
				k, val, ok := strings.Cut(strings.TrimSpace(item), "=")
				if ok && k == "bridge" && readinessBridge.MatchString(val) {
					t.bridges = append(t.bridges, val)
				}
			}
		}
		out = append(out, t)
	}
	return out, count, complete
}
func readinessPermissions(c readinessAPI, storages []string, token string) (readinessCheck, bool) {
	check := readinessCheckOf("permissions", "ok", "Permessi", "Tutti i privilegi richiesti sono disponibili.")
	paths := append([]string{"/"}, storages...)
	missing := []string{}
	for _, path := range paths {
		actual := path
		if path != "/" {
			actual = "/storage/" + path
		}
		raw, err := c.GetPermissions(actual)
		var data map[string]map[string]int
		if !readinessRead(raw, err, &data) || data[actual] == nil {
			check.Status = "unknown"
			check.Detail = "Permessi non leggibili."
			return check, false
		}
		privileges := readinessPrivileges
		if path != "/" {
			privileges = []string{"Datastore.Audit", "Datastore.AllocateSpace"}
		}
		for _, p := range privileges {
			if data[actual][p] != 1 && !(strings.HasPrefix(p, "VM.GuestAgent.") && data[actual]["VM.Monitor"] == 1) {
				item := p + " (" + actual + ")"
				missing = append(missing, item)
			}
		}
	}
	if len(missing) > 0 {
		check.Status = "fail"
		check.Detail = "Privilegi mancanti: " + strings.Join(missing, ", ") + "."
		for _, m := range missing {
			if strings.HasPrefix(m, "Datastore.AllocateSpace") {
				check.Detail += " Senza questo privilegio Proxmox nasconde snippet e ISO negli elenchi dello storage."
				break
			}
		}
		check.Commands = append(check.Commands, `pveum role modify PMGRManager --privs "`+strings.Join(readinessPrivileges, ",")+`"`)
		if token != "" {
			check.Commands = append(check.Commands, "pveum acl modify / --tokens '"+token+"' --roles PMGRManager")
		}
	}
	return check, true
}
func readinessStorageChecks(c readinessAPI, node string, templates []readinessTemplate, stores []string, complete bool) ([]readinessCheck, []string) {
	raw, err := c.GetNodeStorage(node)
	var list []readinessStorage
	if !readinessRead(raw, err, &list) {
		return []readinessCheck{readinessCheckOf("snippets_storage", "unknown", "Storage snippet", "Elenco storage illeggibile."), readinessCheckOf("snippet_files", "unknown", "File snippet", "Dipende da snippets_storage."), readinessCheckOf("template_storage", "unknown", "Storage template", "Elenco storage illeggibile.")}, []string{"snippets_storage", "snippet_files", "template_storage"}
	}
	byName := map[string]readinessStorage{}
	for _, s := range list {
		byName[s.Storage] = s
	}
	sc := readinessCheckOf("snippets_storage", "ok", "Storage snippet", "Storage snippet disponibili.")
	bad := map[string]bool{}
	issues := []string{}
	for _, name := range stores {
		s, ok := byName[name]
		if !ok {
			issues = append(issues, "lo storage "+name+" non esiste su questo nodo")
			bad[name] = true
			continue
		}
		kinds := strings.Split(s.Content, ",")
		has := false
		valid := len(kinds) > 0
		for _, k := range kinds {
			if k == "snippets" {
				has = true
			}
			if !readinessContent.MatchString(k) {
				valid = false
			}
		}
		if !has {
			issues = append(issues, "lo storage "+name+" non contiene snippets")
			bad[name] = true
			if storageNamePattern.MatchString(name) && valid {
				sc.Commands = append(sc.Commands, "pvesm set "+name+" --content "+s.Content+",snippets")
			}
		}
		if !readinessFlag(s.Active, false) || !readinessFlag(s.Enabled, true) {
			issues = append(issues, "lo storage "+name+" non e' attivo o abilitato")
			bad[name] = true
		}
	}
	if len(issues) > 0 {
		sc.Status = "fail"
		sc.Detail = strings.Join(issues, "; ") + "."
	} else if !complete {
		sc.Status = "unknown"
		sc.Detail = "Configurazioni template non leggibili."
	}
	fc := readinessCheckOf("snippet_files", "ok", "File snippet", "Tutti i file snippet sono presenti.")
	missing := []string{}
	unknown := false
	dependent := false
	seen := map[string]bool{}
	listed := map[string]map[string]bool{} // one snippet listing per storage and node
	count := 0
	for _, t := range templates {
		for _, f := range t.info.Files {
			if f.Kind != "snippets" || seen[f.volid()] {
				continue
			}
			seen[f.volid()] = true
			if bad[f.Storage] {
				dependent = true
				continue
			}
			present, readable := listed[f.Storage], true
			if present == nil {
				raw, err := c.GetStorageContent(node, f.Storage, "snippets")
				var files []struct {
					VolID string `json:"volid"`
				}
				if readinessRead(raw, err, &files) {
					present = map[string]bool{}
					for _, v := range files {
						present[v.VolID] = true
					}
					listed[f.Storage] = present
				} else {
					readable = false
				}
			}
			if !readable {
				unknown = true
				continue
			}
			if present[f.volid()] {
				count++
			} else {
				missing = append(missing, f.volid())
			}
		}
	}
	if len(missing) > 0 {
		fc.Status = "fail"
		fc.Detail = "File mancanti: " + readinessList(missing) + ". Eseguire la sincronizzazione (docs/ONBOARDING.md, sezione C)."
	} else if dependent {
		fc.Status = "unknown"
		fc.Detail = "Dipende da snippets_storage."
	} else if unknown || !complete {
		fc.Status = "unknown"
		fc.Detail = "Elenco illeggibile, di solito perche' lo storage non ha il contenuto snippets o manca Datastore.AllocateSpace."
	} else {
		fc.Detail = fmt.Sprintf("%d file snippet presenti.", count)
	}
	tc := readinessCheckOf("template_storage", "ok", "Storage template", "Storage dei template disponibili.")
	absent := []string{}
	for _, t := range templates {
		for _, s := range t.info.Storages {
			st, ok := byName[s]
			if !ok || !readinessFlag(st.Active, false) || !readinessFlag(st.Enabled, true) {
				absent = append(absent, fmt.Sprintf("storage %s (template VMID %d)", s, t.id))
			}
		}
	}
	if len(absent) > 0 {
		tc.Status = "warn"
		tc.Detail = readinessList(absent) + " non disponibili."
	} else if len(templates) == 0 || !complete {
		tc.Status = "unknown"
		tc.Detail = "Storage template non determinabili."
	}
	partial := []string{}
	if unknown || !complete {
		partial = append(partial, "snippet_files")
	}
	if !complete {
		partial = append(partial, "snippets_storage", "template_storage")
	}
	return []readinessCheck{sc, fc, tc}, partial
}
func readinessNodeCheck(c readinessAPI, name string, online bool, templates []readinessTemplate, stores []string, complete bool) (out readinessNodeData) {
	out.node = readinessNode{Node: name, Online: online, Checks: []readinessCheck{}}
	if !online {
		out.node.Checks = append(out.node.Checks, readinessCheckOf("online", "fail", "Nodo", "Nodo non in linea."))
		return
	}
	out.node.Checks = append(out.node.Checks, readinessCheckOf("online", "ok", "Nodo", "Nodo in linea."))
	defer func() {
		if recover() != nil {
			out.partial = append(out.partial, "node")
			out.node.Checks = append(out.node.Checks, readinessCheckOf("node", "unknown", "Controlli nodo", "Controlli non leggibili."))
		}
	}()
	raw, err := c.GetNodeStatus(name)
	var status struct {
		Version string `json:"pveversion"`
	}
	if readinessRead(raw, err, &status) {
		if m := readinessVersion.FindStringSubmatch(status.Version); m != nil {
			out.node.Version = m[1]
		}
	}
	if out.node.Version == "" {
		out.partial = append(out.partial, "versions")
	}
	raw, err = c.GetNodeTime(name)
	var tm struct {
		Time *int64 `json:"time"`
	}
	if readinessRead(raw, err, &tm) && tm.Time != nil {
		out.epoch = *tm.Time
		out.timeOK = true
	} else {
		out.partial = append(out.partial, "clock")
	}
	checks, partial := readinessStorageChecks(c, name, templates, stores, complete)
	out.node.Checks = append(out.node.Checks, checks...)
	out.partial = append(out.partial, partial...)
	bc := readinessCheckOf("bridges", "ok", "Bridge", "Bridge disponibili.")
	wanted := map[string]bool{}
	for _, t := range templates {
		for _, b := range t.bridges {
			wanted[b] = true
		}
	}
	if len(wanted) == 0 || !complete {
		bc.Status = "unknown"
		bc.Detail = "Bridge dei template non determinabili."
	} else {
		raw, err := c.GetNodeNetworks(name)
		var nets []struct {
			Iface string `json:"iface"`
			Type  string `json:"type"`
		}
		if !readinessRead(raw, err, &nets) {
			bc.Status = "unknown"
			bc.Detail = "Elenco bridge illeggibile."
			out.partial = append(out.partial, "bridges")
		} else {
			for _, n := range nets {
				if n.Type == "bridge" || n.Type == "OVSBridge" {
					delete(wanted, n.Iface)
				}
			}
			missing := []string{}
			for b := range wanted {
				missing = append(missing, "il bridge "+b+" non esiste su questo nodo")
			}
			if len(missing) > 0 {
				bc.Status = "fail"
				bc.Detail = readinessList(missing) + "."
			}
		}
	}
	out.node.Checks = append(out.node.Checks, bc)
	return
}

// BuildReadiness computes a read-only report from the current Proxmox data.
func BuildReadiness(c readinessAPI, cluster int, label, token string, now time.Time) (readinessResult, error) {
	result := readinessResult{Cluster: label, GeneratedAt: now.Unix(), TokenID: readinessTokenID(token), ClusterChecks: []readinessCheck{}, Nodes: []readinessNode{}, Partial: []string{}}
	raw, err := c.GetNodes()
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if !readinessRead(raw, err, &nodes) {
		return result, errors.New("elenco nodi non leggibile")
	}
	templates, count, complete := readinessTemplates(c)
	partial := map[string]bool{}
	if !complete {
		partial["templates"] = true
	}
	storesSet := map[string]bool{}
	for _, t := range templates {
		for _, f := range t.info.Files {
			if f.Kind == "snippets" {
				storesSet[f.Storage] = true
			}
		}
	}
	if len(storesSet) == 0 {
		storesSet["local"] = true
	}
	stores := []string{}
	for s := range storesSet {
		stores = append(stores, s)
	}
	sort.Strings(stores)
	perm, ok := readinessPermissions(c, stores, result.TokenID)
	if !ok {
		partial["permissions"] = true
	}
	result.ClusterChecks = append(result.ClusterChecks, perm)
	sort.Slice(nodes, func(i, j int) bool { return naturalLess(nodes[i].Node, nodes[j].Node) })
	data := make([]readinessNodeData, len(nodes))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, n := range nodes {
		if !nodeNamePattern.MatchString(n.Node) {
			partial["nodes"] = true
			continue
		}
		wg.Add(1)
		go func(i int, name string, online bool) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data[i] = readinessNodeCheck(c, name, online, templates, stores, complete)
		}(i, n.Node, n.Status == "online")
	}
	wg.Wait()
	versions := []string{}
	majors := map[string]bool{}
	times := []int64{}
	onlineCount := 0
	for _, d := range data {
		if d.node.Node == "" {
			continue
		}
		result.Nodes = append(result.Nodes, d.node)
		for _, p := range d.partial {
			partial[p] = true
		}
		if d.node.Online {
			onlineCount++
			if d.node.Version != "" {
				versions = append(versions, d.node.Node+" "+d.node.Version)
				majors[strings.Split(d.node.Version, ".")[0]] = true
			}
			if d.timeOK {
				times = append(times, d.epoch)
			}
		}
	}
	vc := readinessCheckOf("versions", "ok", "Versioni", strings.Join(versions, ", "))
	if len(majors) > 1 {
		vc.Status = "warn"
		vc.Detail += "; un nodo nuovo deve avere la stessa versione maggiore del cluster"
	} else if len(versions) != onlineCount || onlineCount == 0 {
		vc.Status = "unknown"
		vc.Detail = "Versioni non completamente leggibili: " + strings.Join(versions, ", ")
	}
	result.ClusterChecks = append(result.ClusterChecks, vc)
	clock := readinessCheckOf("clock", "unknown", "Orologi", "Meno di due orologi leggibili.")
	if len(times) >= 2 {
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		median := float64(times[len(times)/2])
		if len(times)%2 == 0 {
			median = (float64(times[len(times)/2-1]) + median) / 2
		}
		drift := float64(0)
		for _, v := range times {
			d := float64(v) - median
			if d < 0 {
				d = -d
			}
			if d > drift {
				drift = d
			}
		}
		clock.Status = "ok"
		if drift > 30 {
			clock.Status = "fail"
		} else if drift > 2 {
			clock.Status = "warn"
		} else if len(times) != onlineCount {
			clock.Status = "unknown"
		}
		clock.Detail = fmt.Sprintf("Scarto massimo dalla mediana: %g secondi.", drift)
	}
	result.ClusterChecks = append(result.ClusterChecks, clock)
	tc := readinessCheckOf("templates", "ok", "Template", fmt.Sprintf("%d template disponibili.", count))
	if count == 0 {
		tc.Status = "unknown"
		tc.Detail = "nessun template: la Nuova VM non e' utilizzabile"
	}
	if !complete {
		tc.Status = "unknown"
		tc.Detail = "Elenco o configurazioni template non completamente leggibili."
	}
	result.ClusterChecks = append(result.ClusterChecks, tc)
	for _, c := range result.ClusterChecks {
		readinessCount(&result.Summary, c.Status)
	}
	for _, n := range result.Nodes {
		for _, c := range n.Checks {
			readinessCount(&result.Summary, c.Status)
		}
	}
	for p := range partial {
		result.Partial = append(result.Partial, p)
	}
	sort.Strings(result.Partial)
	return result, nil
}
func readinessCount(s *readinessSummary, status string) {
	switch status {
	case "ok":
		s.OK++
	case "warn":
		s.Warn++
	case "fail":
		s.Fail++
	default:
		s.Unknown++
	}
}

type readinessEntry struct {
	sync.Mutex
	at       time.Time
	value    readinessResult
	valid    bool
	identity string
}

var readinessEntries sync.Map
var errReadinessBusy = errors.New("controllo gia' in corso")

func cachedReadiness(entry *readinessEntry, c readinessAPI, idx int, label, token, identity string, fresh bool) (readinessResult, error) {
	if !entry.TryLock() {
		return readinessResult{}, errReadinessBusy
	}
	defer entry.Unlock()
	if !fresh && entry.valid && entry.identity == identity && time.Since(entry.at) < 15*time.Second {
		return entry.value, nil
	}
	result, err := BuildReadiness(c, idx, label, token, time.Now())
	if err != nil {
		return readinessResult{}, err
	}
	entry.value, entry.at, entry.identity, entry.valid = result, time.Now(), identity, true
	return result, nil
}

func (h *Handler) GetNodeReadiness(w http.ResponseWriter, r *http.Request) {
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return
	}
	cl, err := config.GetCluster(idx)
	if err != nil || cl.URL == "" {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	value, _ := readinessEntries.LoadOrStore(idx, &readinessEntry{})
	entry := value.(*readinessEntry)
	identity := cl.URL + "\x00" + cl.APIToken + "\x00" + cl.Label
	result, err := cachedReadiness(entry, client, idx, cl.Label, cl.APIToken, identity, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		if errors.Is(err, errReadinessBusy) {
			w.Header().Set("Retry-After", "10")
			writeError(w, errReadinessBusy, http.StatusTooManyRequests)
			return
		}
		writeError(w, errors.New("impossibile leggere i nodi del cluster"), http.StatusBadGateway)
		return
	}
	writeJSON(w, result)
}
