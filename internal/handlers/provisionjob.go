package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/config"
)

// POST /api/provision/job: create a VM from a template, driven by the server (ROADMAP 3b, D56-D61).
//
// Before anything is written the server recomputes the advice with fresh data: the target node must be eligible
// (RAM within the assignable limit, storage of the template reachable, D58 A) and, with a GPU, the card behind the
// mapping must be on that node, free and not reserved by another creation. Then, in its own goroutine:
//
//	clone (directly on the target node, D56) -> cores/RAM -> disk size -> cloud-init -> group tag
//	-> GPU (hostpci=mapping, same plan/lock/digest code as the GPU page) -> start -> wait for the guest agent
//	(5 min, then a second wait of 5 min, D61 C) -> additional user (adduser.sh, as the "Aggiungi utente" button)
//
// Nothing is ever deleted automatically. A failure in clone/config/disk/cloud-init/GPU/start ends the procedure
// with the VM left STOPPED (if it was created) and the phase that failed; a failed group tag or additional user
// is only a warning (the VM is created and running, the user adds them from the VM panel).
// The card is reserved in memory for the whole procedure (soft lock): the advice shows it as "in creation". The
// real check is still the GPU page's one, under its lock, right before writing.

const (
	provBodyMax   = 64 << 10
	provMaxJobs   = 64
	provMaxSSHKey = 8
)

var provTiming = struct {
	Clone     time.Duration // clone task
	Task      time.Duration // other tasks (start)
	Agent     time.Duration // one wait for the guest agent (D61: two of them)
	AgentPoll time.Duration
	// DiskScanWait: pause between two searches for the new disk in the guest
	DiskScanWait time.Duration
	Keep         time.Duration // a finished procedure stays readable this long
}{15 * time.Minute, 5 * time.Minute, 10 * time.Minute, 10 * time.Second, 2 * time.Second, time.Hour}

// phases and results (codes: the page writes the sentences)
const (
	provPhClone     = "clone"
	provPhConfig    = "config"
	provPhDisk      = "disk"
	provPhDisk2     = "disk2" // the extra disk is attached to the VM
	provPhMount     = "mount" // ... and formatted and mounted in the guest
	provPhCloudInit = "cloudinit"
	provPhGroup     = "group"
	provPhGPU       = "gpu"
	provPhStart     = "start"
	provPhAgent     = "agent"
	provPhUser      = "user"
	provPhDone      = "done"

	provOK       = "ok"
	provWarnings = "warnings" // created and running, something optional failed (see warnings)
	provFailed   = "failed"   // stopped at failed_phase; vm_created says whether a VM exists (then it is stopped)
)

var (
	vmNamePattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,61}[A-Za-z0-9])?$`)
	diskDevPattern  = regexp.MustCompile(`^(scsi|virtio|sata|ide)[0-9]{1,2}$`)
	diskSizePattern = regexp.MustCompile(`(?:^|,)size=([0-9]+(?:\.[0-9]+)?)([KMGT]?)(?:,|$)`)
)

type provUser struct {
	Username string `json:"username"`
	SSHKey   string `json:"ssh_key"`
}

type provReq struct {
	TemplateNode string    `json:"template_node"`
	TemplateVMID int       `json:"template_vmid"`
	TargetNode   string    `json:"target_node"`
	NewVMID      int       `json:"new_vmid"` // 0 = next free
	Name         string    `json:"name"`
	Cores        int       `json:"cores"`
	Memory       int       `json:"memory"`  // MB
	DiskGB       int       `json:"disk_gb"` // desired total size of disk_device; 0 = as the template
	DiskDevice   string    `json:"disk_device"`
	IP           string    `json:"ip"` // a.b.c.d[/nn]; "" = leave the template's
	Gateway      string    `json:"gateway"`
	DNS          string    `json:"dns"` // up to 3 addresses, space or comma separated
	SSHKeys      string    `json:"ssh_keys"`
	CIUser       string    `json:"ciuser"`
	CIPassword   string    `json:"cipassword"`
	Group        string    `json:"group"`
	GroupNew     bool      `json:"group_new"`
	GPUMapping   string    `json:"gpu_mapping"` // "" = no GPU
	Start        *bool     `json:"start"`       // default true (decision 2026-11-03)
	ExtraUser    *provUser `json:"extra_user"`
	ExtraDisk    *provDisk `json:"extra_disk"`
}

// provDisk is the extra disk (D64 B, D65 A, D66 A, D67 A): a new volume of SizeGB on Storage (default: the
// template's storage), attached with the bus of the main disk, then formatted and mounted on MountPoint.
type provDisk struct {
	SizeGB     int    `json:"size_gb"`
	Storage    string `json:"storage"`
	MountPoint string `json:"mount_point"` // default /data
	Sticky     *bool  `json:"sticky_bit"`  // default true: permissions 1777
}

// provPlan is the validated request, ready to be executed.
type provPlan struct {
	provReq
	start       bool
	groups      []string
	ipcfg       string // ipconfig0 value or ""
	dns         string
	sshKeys     string // normalised, one per line
	extraKey    string
	disk2       *provDisk // validated copy, defaults filled
	disk2Sticky bool
	disk2Store  string // resolved storage of the extra disk
	disk2Bus    string // bus of the main disk (virtio, scsi, sata)
	storage     string // storage of the template's disk
	curDisk     int    // GB of the template's disk
}

func validateProvReq(r provReq) (provPlan, error) {
	p := provPlan{provReq: r, start: true}
	if r.Start != nil {
		p.start = *r.Start
	}
	bad := func(what string) (provPlan, error) { return provPlan{}, errors.New(what + " non valido") }
	switch {
	case !nodeNamePattern.MatchString(r.TemplateNode):
		return bad("nodo del template")
	case !nodeNamePattern.MatchString(r.TargetNode):
		return bad("nodo di destinazione")
	case r.TemplateVMID < 1 || r.TemplateVMID > 999999999:
		return bad("template")
	case r.NewVMID != 0 && (r.NewVMID < 100 || r.NewVMID > 999999999):
		return bad("VMID (100 - 999999999, 0 = il primo libero)")
	case !vmNamePattern.MatchString(r.Name):
		return bad("nome (lettere, cifre, punto e trattino, max 63)")
	case r.Cores < 1 || r.Cores > adviceMaxCores:
		return bad("numero di core")
	case r.Memory < adviceMemMinMB || r.Memory > adviceMemMaxMB:
		return bad("RAM")
	case r.DiskGB < 0 || r.DiskGB > 65536:
		return bad("disco (GB)")
	}
	if p.DiskDevice == "" {
		p.DiskDevice = "virtio0"
	}
	if !diskDevPattern.MatchString(p.DiskDevice) {
		return bad("dispositivo del disco")
	}
	if r.IP != "" {
		ip := r.IP
		if !strings.Contains(ip, "/") {
			ip += "/24"
		}
		addr, nw, err := net.ParseCIDR(ip)
		if err != nil || addr.To4() == nil {
			return bad("IP (IPv4, es. 10.75.4.20/24)")
		}
		ones, bits := nw.Mask.Size()
		if bits != 32 || ones < 1 { // "::ffff:a.b.c.d/120" parses as IPv4 with a 128-bit mask
			return bad("IP (IPv4 con prefisso 1-32, es. 10.75.4.20/24)")
		}
		p.ipcfg = "ip=" + addr.String() + "/" + strconv.Itoa(ones)
		if r.Gateway != "" {
			gw := net.ParseIP(r.Gateway)
			if gw == nil || gw.To4() == nil {
				return bad("gateway")
			}
			p.ipcfg += ",gw=" + gw.String()
		}
	} else if r.Gateway != "" {
		return provPlan{}, errors.New("il gateway richiede un IP")
	}
	if r.DNS != "" {
		var out []string
		for _, d := range strings.FieldsFunc(r.DNS, func(c rune) bool { return c == ' ' || c == ',' }) {
			ip := net.ParseIP(d)
			if ip == nil || len(out) >= 3 {
				return bad("DNS (fino a 3 indirizzi)")
			}
			out = append(out, ip.String())
		}
		p.dns = strings.Join(out, " ")
	}
	if r.SSHKeys != "" {
		var keys []string
		keysText := strings.ReplaceAll(strings.ReplaceAll(r.SSHKeys, "\r\n", "\n"), "\r", "\n")
		for _, line := range strings.Split(keysText, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			k, err := normalizeSSHKey(line)
			if err != nil || k == "" || len(keys) >= provMaxSSHKey {
				return bad("chiavi SSH (una per riga, al massimo 8)")
			}
			keys = append(keys, k)
		}
		p.sshKeys = strings.Join(keys, "\n")
	}
	if r.CIUser != "" && !validUsername.MatchString(r.CIUser) {
		return bad("utente cloud-init")
	}
	if r.CIPassword != "" {
		if len(r.CIPassword) > 128 {
			return bad("password cloud-init")
		}
		for _, c := range r.CIPassword {
			if c < 0x20 || c > 0x7e {
				return bad("password cloud-init (solo caratteri ASCII stampabili)")
			}
		}
	}
	if strings.TrimSpace(r.Group) != "" {
		g, err := normalizeGroups([]string{r.Group})
		if err != nil {
			return provPlan{}, err
		}
		p.groups = g
	}
	if r.GPUMapping != "" && !mappingPat.MatchString(r.GPUMapping) {
		return bad("mapping della GPU")
	}
	if r.ExtraDisk != nil {
		d := *r.ExtraDisk
		if d.SizeGB < 1 || d.SizeGB > 16384 {
			return bad("dimensione del disco aggiuntivo (1 - 16384 GB)")
		}
		if d.Storage != "" && !storageNamePattern.MatchString(d.Storage) {
			return bad("storage del disco aggiuntivo")
		}
		if d.MountPoint == "" {
			d.MountPoint = "/data"
		}
		if err := validateMountPoint(d.MountPoint); err != nil {
			return provPlan{}, err
		}
		p.disk2, p.disk2Sticky, p.disk2Store = &d, d.Sticky == nil || *d.Sticky, d.Storage
		bus := strings.TrimRight(p.DiskDevice, "0123456789")
		if _, ok := diskSlotLimits[bus]; !ok {
			bus = "virtio" // an ide main disk: the extra disk still gets a bus with enough slots
		}
		p.disk2Bus = bus
	}
	if r.ExtraUser != nil {
		if !validUsername.MatchString(r.ExtraUser.Username) {
			return bad("utente aggiuntivo")
		}
		if r.CIUser != "" && r.ExtraUser.Username == r.CIUser {
			return provPlan{}, errors.New("l'utente aggiuntivo deve essere diverso dall'utente cloud-init")
		}
		k, err := normalizeSSHKey(r.ExtraUser.SSHKey)
		if err != nil {
			return provPlan{}, err
		}
		p.extraKey = k
		if !p.start {
			return provPlan{}, errors.New("l'utente aggiuntivo si crea solo se la VM viene avviata")
		}
	}
	return p, nil
}

// diskSizeGB reads "size=32G" (K/M/G/T) from a disk line; rounds up to whole GB.
func diskSizeGB(line string) (int, bool) {
	m := diskSizePattern.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "K":
		v /= 1024 * 1024
	case "M":
		v /= 1024
	case "T":
		v *= 1024
	}
	gb := int(v)
	if float64(gb) < v {
		gb++
	}
	return gb, true
}

// ── reservations (soft lock of the cards) ─────────────────────────────────────

var provReserved = struct {
	sync.Mutex
	m map[string]string // cluster|node|addr -> job id
}{m: map[string]string{}}

func cardKey(cluster int, node, addr string) string {
	return strconv.Itoa(cluster) + "|" + node + "|" + addr
}

func reserveCard(key, job string) bool {
	provReserved.Lock()
	defer provReserved.Unlock()
	if _, taken := provReserved.m[key]; taken {
		return false
	}
	provReserved.m[key] = job
	return true
}

func releaseCard(key, job string) {
	provReserved.Lock()
	defer provReserved.Unlock()
	if provReserved.m[key] == job {
		delete(provReserved.m, key)
	}
}

// reservedCards returns node|addr -> true for one cluster (for the advice).
func reservedCards(cluster int) map[string]bool {
	prefix := strconv.Itoa(cluster) + "|"
	out := map[string]bool{}
	provReserved.Lock()
	defer provReserved.Unlock()
	for k := range provReserved.m {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = true
		}
	}
	return out
}

// ── creations in flight: RAM promised to a node by a VM that is cloned but not started yet ──

type pendingVM struct {
	Cluster int
	Node    string
	VMID    int
	Mem     int64
	VCPU    int
}

var provPending = struct {
	sync.Mutex
	m map[string]pendingVM // job id -> promise
}{m: map[string]pendingVM{}}

func addPending(job string, p pendingVM) {
	provPending.Lock()
	defer provPending.Unlock()
	provPending.m[job] = p
}

func releasePending(job string) {
	provPending.Lock()
	defer provPending.Unlock()
	delete(provPending.m, job)
}

func pendingCreations(cluster int) []pendingVM {
	provPending.Lock()
	defer provPending.Unlock()
	out := []pendingVM{}
	for _, p := range provPending.m {
		if p.Cluster == cluster {
			out = append(out, p)
		}
	}
	return out
}

// provCreate serialises "check -> clone -> RAM set" per cluster, so two creations cannot both pass the RAM check
// for the same node (the second one sees the first VM with its final RAM).
var provCreate sync.Map // cluster -> *sync.Mutex

func provCreateLock(cluster int) *sync.Mutex {
	m, _ := provCreate.LoadOrStore(cluster, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// ── job ───────────────────────────────────────────────────────────────────────

type provStep struct {
	Phase string `json:"phase"`
	OK    bool   `json:"ok"`
	Note  string `json:"note,omitempty"` // our text or the "message" of a Proxmox error, never secrets
}

type provJob struct {
	ID      string
	Cluster int

	mu        sync.Mutex
	phase     string
	result    string
	failedAt  string
	vmCreated bool
	vmid      int
	node      string
	name      string
	card      string // reserved card key ("" = none)
	steps     []provStep
	warnings  []string
	started   time.Time
	ended     time.Time
}

type provView struct {
	ID        string     `json:"id"`
	Phase     string     `json:"phase"`
	Result    string     `json:"result,omitempty"`
	FailedAt  string     `json:"failed_phase,omitempty"`
	VMCreated bool       `json:"vm_created"`
	VMID      int        `json:"vmid"`
	Node      string     `json:"node"`
	Name      string     `json:"name"`
	Steps     []provStep `json:"steps"`
	Warnings  []string   `json:"warnings"`
	StartedAt int64      `json:"started_at"`
}

func (j *provJob) view() provView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return provView{ID: j.ID, Phase: j.phase, Result: j.result, FailedAt: j.failedAt, VMCreated: j.vmCreated, VMID: j.vmid,
		Node: j.node, Name: j.name, Steps: append([]provStep{}, j.steps...), Warnings: append([]string{}, j.warnings...), StartedAt: j.started.Unix()}
}

func (j *provJob) enter(phase string) {
	j.mu.Lock()
	j.phase = phase
	j.mu.Unlock()
	log.Printf("provision: cluster=%d vmid=%d %s: %s", j.Cluster, j.vmid, j.node, phase)
}

func (j *provJob) step(phase string, ok bool, note string) {
	j.mu.Lock()
	j.steps = append(j.steps, provStep{Phase: phase, OK: ok, Note: clampText(note, 200)})
	j.mu.Unlock()
}

func (j *provJob) warn(code, phase string, err error) {
	note := ""
	if err != nil {
		note = proxmoxMessage(err)
		log.Printf("provision: cluster=%d vmid=%d warning %s: %v", j.Cluster, j.vmid, code, err)
	}
	j.step(phase, false, note)
	j.mu.Lock()
	j.warnings = append(j.warnings, code)
	j.mu.Unlock()
}

// fail ends the procedure at phase; the VM (if any) is left as it is: stopped, never deleted.
func (j *provJob) fail(phase string, err error) {
	log.Printf("provision: cluster=%d vmid=%d failed at %s: %v", j.Cluster, j.vmid, phase, err)
	j.step(phase, false, proxmoxMessage(err))
	j.mu.Lock()
	j.phase, j.result, j.failedAt, j.ended = provPhDone, provFailed, phase, time.Now()
	j.mu.Unlock()
}

func (j *provJob) finish() {
	j.mu.Lock()
	j.phase, j.ended = provPhDone, time.Now()
	j.result = provOK
	if len(j.warnings) > 0 {
		j.result = provWarnings
	}
	j.mu.Unlock()
	log.Printf("provision: cluster=%d vmid=%d %s: %s", j.Cluster, j.vmid, j.node, j.result)
}

func (j *provJob) finished() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.phase == provPhDone
}

var provJobs = struct {
	sync.Mutex
	m map[string]*provJob
}{m: map[string]*provJob{}}

func newProvJob(cluster int, now time.Time) (*provJob, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, errors.New("identificativo non generabile")
	}
	j := &provJob{ID: hex.EncodeToString(b), Cluster: cluster, phase: provPhClone, started: now, steps: []provStep{}, warnings: []string{}}
	provJobs.Lock()
	defer provJobs.Unlock()
	for id, o := range provJobs.m {
		if o.finished() && now.Sub(o.ended) > provTiming.Keep {
			delete(provJobs.m, id)
		}
	}
	if len(provJobs.m) >= provMaxJobs {
		return nil, errors.New("troppe creazioni in corso")
	}
	provJobs.m[j.ID] = j
	return j, nil
}

func findProvJob(id string, cluster int) *provJob {
	if !coldIDPattern.MatchString(id) {
		return nil
	}
	provJobs.Lock()
	defer provJobs.Unlock()
	j := provJobs.m[id]
	if j == nil || j.Cluster != cluster {
		return nil
	}
	return j
}

// ── Proxmox ───────────────────────────────────────────────────────────────────

type provAPI interface {
	balanceAPI
	gpuWriteAPI
	groupWriteAPI
	agentExecer
	coldAPI
	GetNextVMID() (json.RawMessage, error)
	VMClone(node string, vmid, newid int, params url.Values) (json.RawMessage, error)
	VMResizeDisk(node string, vmid int, disk, size string) (json.RawMessage, error)
	VMSetCloudInit(node string, vmid int, params url.Values) (json.RawMessage, error)
}

var _ provAPI = (*api.Client)(nil)

// provRefusal is a refusal before anything was written (409 with a reason code for the page).
type provRefusal struct {
	Reason  string
	Msg     string
	Reasons []string // node reasons, when the node is not eligible
}

func (e *provRefusal) Error() string { return e.Msg }

// precheck reads the template and recomputes the advice with fresh data. Nothing is written.
func provPrecheck(c provAPI, cluster int, label string, p *provPlan, now time.Time) (cardKeyOut string, err error) {
	raw, err := c.GetVMConfig(p.TemplateNode, p.TemplateVMID)
	if err != nil {
		return "", &provRefusal{Reason: "template", Msg: "template non leggibile"}
	}
	var tcfg map[string]interface{}
	if json.Unmarshal(raw, &tcfg) != nil {
		return "", &provRefusal{Reason: "template", Msg: "template non leggibile"}
	}
	// only templates are cloned (same rule as the template list of the page): a normal VM would be copied
	// with its data into a VM with new users and keys
	tname, _ := tcfg["name"].(string)
	if !isTemplateRes(map[string]interface{}{"template": tcfg["template"], "name": tname}) {
		return "", &provRefusal{Reason: "not_template", Msg: "la sorgente non e' un template"}
	}
	disk, _ := tcfg[p.DiskDevice].(string)
	i := strings.IndexByte(disk, ':')
	if i <= 0 || !storageNamePattern.MatchString(disk[:i]) || strings.Contains(disk, "media=cdrom") {
		return "", &provRefusal{Reason: "template_disk", Msg: "il template non ha il disco " + p.DiskDevice}
	}
	p.storage = disk[:i]
	// every volume of the template (disks, EFI, TPM, cloud-init drive, ISO) must be reachable from the target:
	// a full clone with "target" copies all of them
	storages := templateStorages(tcfg)
	if len(storages) > 8 {
		return "", &provRefusal{Reason: "template_disk", Msg: "il template usa troppi storage diversi"}
	}
	if gb, ok := diskSizeGB(disk); ok {
		p.curDisk = gb
	}
	if p.DiskGB > 0 && p.curDisk > 0 && p.DiskGB < p.curDisk {
		return "", &provRefusal{Reason: "disk_smaller", Msg: fmt.Sprintf("il disco non si puo' ridurre: il template ha %d GB", p.curDisk)}
	}
	if len(p.groups) > 0 && !p.GroupNew {
		known, err := knownGroups(c)
		if err != nil {
			return "", errors.New("elenco dei gruppi non leggibile")
		}
		if known[p.groups[0]] == 0 {
			return "", &provRefusal{Reason: "new_group", Msg: "Gruppo nuovo, va confermato: " + p.groups[0]}
		}
	}
	var inv *GPUInventory
	if p.GPUMapping != "" {
		fresh, err := fetchGPUInventory(c, now) // never the cache: this decides a write
		if err != nil || len(fresh.Partial) > 0 {
			return "", errors.New("inventario delle schede non leggibile")
		}
		inv = &fresh
	}
	if p.disk2 != nil && p.disk2Store == "" {
		p.disk2Store = p.storage // D65 A: by default the template's storage
	}
	q := adviceQuery{Cluster: cluster, MemMB: int64(p.Memory), Cores: p.Cores, GPU: p.GPUMapping != "", Storages: storages, TemplateNode: p.TemplateNode}
	if p.disk2 != nil {
		q.Disk2GB, q.Disk2Storage = p.disk2.SizeGB, p.disk2Store
	}
	if len(p.groups) > 0 {
		q.Group = p.groups[0]
	}
	adv, err := buildAdvice(c, inv, label, q, reservedCards(cluster), now)
	if err != nil {
		return "", err
	}
	for _, n := range adv.Nodes {
		if n.Name != p.TargetNode {
			continue
		}
		if !n.Eligible {
			codes := []string{}
			for _, r := range n.Reasons {
				codes = append(codes, r.Code)
			}
			return "", &provRefusal{Reason: "node_not_eligible", Msg: "il nodo scelto non puo' ricevere la VM", Reasons: codes}
		}
		if p.GPUMapping == "" {
			return "", nil
		}
		for _, cd := range adv.Cards {
			if cd.Node == p.TargetNode && containsStr(cd.Mappings, p.GPUMapping) {
				if !cd.Selectable {
					return "", &provRefusal{Reason: "card_unavailable", Msg: "la scheda scelta non e' disponibile", Reasons: cd.Reasons}
				}
				return cardKey(cluster, cd.Node, cd.Addr), nil
			}
		}
		return "", &provRefusal{Reason: "card_unavailable", Msg: "il mapping non punta a una scheda di questo nodo"}
	}
	return "", &provRefusal{Reason: "node_not_eligible", Msg: "nodo sconosciuto"}
}

var templateVolumeKey = regexp.MustCompile(`^((virtio|scsi|sata|ide)[0-9]{1,2}|efidisk[0-9]|tpmstate[0-9])$`)

// templateStorages lists the storages of every volume of a template config ("storage:volume,..."), sorted.
func templateStorages(cfg map[string]interface{}) []string {
	set := map[string]bool{}
	for k, v := range cfg {
		s, _ := v.(string)
		if !templateVolumeKey.MatchString(k) || s == "" || strings.HasPrefix(s, "none") {
			continue
		}
		if i := strings.IndexByte(s, ':'); i > 0 && storageNamePattern.MatchString(s[:i]) {
			set[s[:i]] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nextVMID(c provAPI) (int, error) {
	raw, err := c.GetNextVMID()
	if err != nil {
		return 0, err
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var n int
		if json.Unmarshal(raw, &n) != nil {
			return 0, errors.New("VMID non leggibile")
		}
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 100 {
		return 0, errors.New("VMID non leggibile")
	}
	return n, nil
}

// vmidInUse looks for the VMID on the whole cluster (VMs, containers and templates).
func vmidInUse(c provAPI, vmid int) (bool, error) {
	raw, err := c.GetClusterResources("vm")
	if err != nil {
		return false, errors.New("elenco delle VM non leggibile")
	}
	var list []map[string]interface{}
	if json.Unmarshal(raw, &list) != nil {
		return false, errors.New("elenco delle VM non leggibile")
	}
	for _, r := range list {
		if int(intField(r, "vmid")) == vmid {
			return true, nil
		}
	}
	return false, nil
}

// vmNamed reports whether VMID exists in the cluster with exactly this name.
func vmNamed(c provAPI, vmid int, name string) (bool, error) {
	raw, err := c.GetClusterResources("vm")
	if err != nil {
		return false, err
	}
	var list []map[string]interface{}
	if json.Unmarshal(raw, &list) != nil {
		return false, errors.New("elenco delle VM non leggibile")
	}
	for _, r := range list {
		if n, _ := r["name"].(string); int(intField(r, "vmid")) == vmid && n == name {
			return true, nil
		}
	}
	return false, nil
}

// runProv executes the procedure. createLock is held by the caller and released here once the RAM is set.
func runProv(c provAPI, j *provJob, p provPlan, createLock *sync.Mutex, optionsFor func(string) string) {
	var once sync.Once
	unlock := func() { once.Do(createLock.Unlock) }
	defer func() {
		if r := recover(); r != nil {
			log.Printf("provision: cluster=%d vmid=%d panic: %v", j.Cluster, j.vmid, r)
			j.fail(j.view().Phase, coldMsg("errore interno"))
		}
		unlock()
		releasePending(j.ID) // on every exit: a started VM is counted from its running state, a failed one uses no RAM
		if j.card != "" {
			releaseCard(j.card, j.ID)
		}
	}()
	node, vmid := p.TargetNode, p.NewVMID

	// 1. clone, directly on the target node (D56)
	j.enter(provPhClone)
	params := url.Values{"full": {"1"}, "name": {p.Name}}
	if node != p.TemplateNode {
		params.Set("target", node)
	}
	raw, err := c.VMClone(p.TemplateNode, p.TemplateVMID, vmid, params)
	if err != nil {
		// refused before starting (e.g. the VMID was just taken by someone else): any VM with this id is not ours
		j.fail(provPhClone, err)
		return
	}
	if err = waitTask(c, raw, provTiming.Clone); err != nil {
		// the clone started and failed: a leftover VM is ours only if it carries the name we gave it
		if ours, _ := vmNamed(c, vmid, p.Name); ours {
			j.mu.Lock()
			j.vmCreated = true
			j.mu.Unlock()
		}
		j.fail(provPhClone, err)
		return
	}
	j.mu.Lock()
	j.vmCreated = true
	j.mu.Unlock()
	j.step(provPhClone, true, "")

	// 2. cores and RAM: from now on the cluster resources show the real RAM, the next creation may check
	j.enter(provPhConfig)
	if _, err := c.VMSetConfig(node, vmid, url.Values{"cores": {strconv.Itoa(p.Cores)}, "sockets": {"1"}, "memory": {strconv.Itoa(p.Memory)}}); err != nil {
		unlock()
		j.fail(provPhConfig, err)
		return
	}
	unlock()
	j.step(provPhConfig, true, "")

	// 3. disk (only growing; the precheck refused a smaller size)
	if p.DiskGB > 0 && p.DiskGB != p.curDisk {
		j.enter(provPhDisk)
		size := strconv.Itoa(p.DiskGB) + "G"
		if p.curDisk > 0 {
			size = "+" + strconv.Itoa(p.DiskGB-p.curDisk) + "G"
		}
		if _, err := c.VMResizeDisk(node, vmid, p.DiskDevice, size); err != nil {
			j.fail(provPhDisk, err)
			return
		}
		j.step(provPhDisk, true, "")
	}

	// 3b. extra disk: a new volume attached with the main disk's bus, first free slot
	if p.disk2 != nil {
		j.enter(provPhDisk2)
		raw, err := c.GetVMConfig(node, vmid)
		var cfg map[string]interface{}
		if err != nil || json.Unmarshal(raw, &cfg) != nil {
			j.fail(provPhDisk2, coldMsg("configurazione della VM non leggibile"))
			return
		}
		slot, err := nextDiskSlot(cfg, p.disk2Bus)
		if err != nil {
			j.fail(provPhDisk2, coldMsg(err.Error()))
			return
		}
		// the digest makes Proxmox refuse if anyone changed the VM since we read it
		params := url.Values{slot: {fmt.Sprintf("%s:%d", p.disk2Store, p.disk2.SizeGB)}}
		if d, _ := cfg["digest"].(string); d != "" {
			params.Set("digest", d)
		}
		if _, err := c.VMSetConfig(node, vmid, params); err != nil {
			j.fail(provPhDisk2, err)
			return
		}
		j.step(provPhDisk2, true, fmt.Sprintf("%s = %s:%d GB", slot, p.disk2Store, p.disk2.SizeGB))
	}

	// 4. cloud-init (values validated; the password and the keys are never logged)
	ci := url.Values{}
	if p.ipcfg != "" {
		ci.Set("ipconfig0", p.ipcfg)
	}
	if p.dns != "" {
		ci.Set("nameserver", p.dns)
	}
	if p.sshKeys != "" {
		ci.Set("sshkeys", strings.ReplaceAll(url.QueryEscape(p.sshKeys), "+", "%20")) // Proxmox wants RFC 3986
	}
	if p.CIUser != "" {
		ci.Set("ciuser", p.CIUser)
	}
	if p.CIPassword != "" {
		ci.Set("cipassword", p.CIPassword)
	}
	if len(ci) > 0 {
		j.enter(provPhCloudInit)
		if _, err := c.VMSetCloudInit(node, vmid, ci); err != nil {
			j.fail(provPhCloudInit, err)
			return
		}
		j.step(provPhCloudInit, true, "")
	}

	// 5. group tag: optional
	if len(p.groups) > 0 {
		j.enter(provPhGroup)
		var confirm []string
		if p.GroupNew {
			confirm = p.groups
		}
		if _, e := setVMGroups(c, j.Cluster, node, "qemu", vmid, p.groups, confirm); e != nil {
			j.warn("group_failed", provPhGroup, coldMsg(e.Msg))
		} else {
			j.step(provPhGroup, true, "")
		}
	}

	// 6. GPU: the GPU page's plan and write, under its node lock and with the config digest
	if p.GPUMapping != "" {
		j.enter(provPhGPU)
		l := gpuLock(j.Cluster, node)
		l.Lock()
		plan, e := planGPUAssign(c, node, vmid, p.GPUMapping, optionsFor, time.Now())
		l.Unlock()
		if e == nil {
			_, e = assignGPU(c, j.Cluster, node, vmid, p.GPUMapping, plan.Slot, plan.Options, optionsFor)
		}
		if e != nil {
			j.fail(provPhGPU, coldMsg(e.Msg)) // the VM stays stopped (decision 2026-11-03)
			return
		}
		j.step(provPhGPU, true, plan.Slot+" = mapping "+p.GPUMapping)
	}
	if j.card != "" {
		releaseCard(j.card, j.ID)
		j.card = ""
	}
	if !p.start {
		if p.disk2 != nil { // formatting needs the guest: the disk stays attached and empty
			j.warn("mount_skipped", provPhMount, coldMsg("la VM non e' stata avviata: il disco e' collegato ma non formattato"))
		}
		j.finish()
		return
	}

	// 7. start
	j.enter(provPhStart)
	if err := startVM(c, node, vmid); err != nil {
		j.fail(provPhStart, err)
		return
	}
	j.step(provPhStart, true, "")
	// from here the VM counts as running (even if Proxmox still shows it stopped for a moment); the promise ends
	markStarted(j.Cluster, node, "qemu", vmid, time.Now())
	releasePending(j.ID)
	mountWanted := p.disk2 != nil
	if p.ExtraUser == nil && !mountWanted {
		j.finish()
		return
	}

	// 8. guest agent: up to 10 minutes, then a second wait of 10 minutes (D61 C; the first boot of a template
	// that updates itself can take longer than 5 minutes before the agent answers)
	j.enter(provPhAgent)
	useHelper, ready := false, false
	for attempt := 0; attempt < 2 && !ready; attempt++ {
		end := time.Now().Add(provTiming.Agent)
		for time.Now().Before(end) {
			if h, err := addUserTool.probeHelper(c, node, vmid); err == nil {
				useHelper, ready = h, true
				break
			}
			time.Sleep(provTiming.AgentPoll)
		}
	}
	if !ready {
		what := "i passi dentro la VM"
		switch {
		case p.ExtraUser != nil && mountWanted:
			what = "l'utente aggiuntivo e il montaggio del disco"
		case p.ExtraUser != nil:
			what = "l'utente aggiuntivo"
		case mountWanted:
			what = "il montaggio del disco"
		}
		j.warn("agent_timeout", provPhAgent, coldMsg("il guest agent non ha risposto in 20 minuti: "+what+" si fanno dal pannello della VM"))
		j.finish()
		return
	}
	j.step(provPhAgent, true, "")

	// 9. additional user: same script and rules as the "Aggiungi utente" button
	if p.ExtraUser != nil {
		j.enter(provPhUser)
		res, err := addUserTool.run(c, node, vmid, useHelper, []string{p.ExtraUser.Username, p.extraKey}, 30*time.Second)
		switch {
		case err != nil:
			j.warn("user_failed", provPhUser, agentError(err))
		case res.ExitCode == 3:
			j.warn("user_exists", provPhUser, coldMsg("l'utente esiste gia' nella VM: nessuna modifica"))
		case res.ExitCode != 0:
			j.warn("user_failed", provPhUser, coldMsg(fmt.Sprintf("lo script e' uscito con codice %d", res.ExitCode)))
		default:
			j.step(provPhUser, true, p.ExtraUser.Username)
		}
	}

	// 10. extra disk: format and mount (D64 B). Independent of the user: one failing does not skip the other.
	if mountWanted {
		j.enter(provPhMount)
		if err := formatAndMountBlank(c, node, vmid, p.disk2.MountPoint, p.disk2Sticky); err != nil {
			var ours coldMsg // our own explanation (no disk, several disks...) is shown as it is
			if !errors.As(err, &ours) {
				err = agentError(err)
			}
			j.warn("mount_failed", provPhMount, err)
		} else {
			j.step(provPhMount, true, p.disk2.MountPoint)
		}
	}
	j.finish()
}

// ── HTTP ──────────────────────────────────────────────────────────────────────

// PostProvisionJob: POST /api/provision/job  body: provReq. 202 {job} or 400 / 409 {error, reason, reasons}.
func (h *Handler) PostProvisionJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, provBodyMax)
	var req provReq
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return
	}
	plan, err := validateProvReq(req)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	idx := clusterIdx(r)
	cl, err := config.GetCluster(idx)
	if err != nil || cl.URL == "" {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	st, err := startProv(client, idx, cl.Label, plan, h.optionsFromCache(client, idx), time.Now())
	if err != nil {
		var ref *provRefusal
		if errors.As(err, &ref) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			reasons := ref.Reasons
			if reasons == nil {
				reasons = []string{}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": ref.Msg, "reason": ref.Reason, "reasons": reasons})
			return
		}
		log.Printf("provision (cluster %d): %v", idx, err)
		writeError(w, errors.New(clampText(proxmoxMessage(err), 200)), http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json") // before WriteHeader: headers set later are dropped
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"job": st.ID, "vmid": st.vmid})
}

// startProv runs the checks, reserves the card and starts the procedure. Testable without HTTP.
func startProv(c provAPI, idx int, label string, plan provPlan, optionsFor func(string) string, now time.Time) (*provJob, error) {
	lock := provCreateLock(idx)
	if !lock.TryLock() {
		// held from the check to the RAM setting, clone included (seconds, minutes for a big template):
		// refuse rather than queue HTTP requests behind it
		return nil, &provRefusal{Reason: "busy", Msg: "un'altra creazione sta partendo su questo cluster: riprova tra poco"}
	}
	release := true
	defer func() {
		if release {
			lock.Unlock()
		}
	}()
	card, err := provPrecheck(c, idx, label, &plan, now)
	if err != nil {
		return nil, err
	}
	if plan.NewVMID == 0 {
		if plan.NewVMID, err = nextVMID(c); err != nil {
			return nil, err
		}
	} else if used, err := vmidInUse(c, plan.NewVMID); err != nil {
		return nil, err
	} else if used {
		return nil, &provRefusal{Reason: "vmid_taken", Msg: "VMID gia' in uso"}
	}
	j, err := newProvJob(idx, now)
	if err != nil {
		return nil, err
	}
	j.vmid, j.node, j.name = plan.NewVMID, plan.TargetNode, plan.Name
	// promise the RAM of the new VM to its node from now on, still inside the locked check -> clone section
	addPending(j.ID, pendingVM{Cluster: idx, Node: plan.TargetNode, VMID: plan.NewVMID, Mem: int64(plan.Memory) << 20, VCPU: plan.Cores})
	if card != "" {
		if !reserveCard(card, j.ID) {
			j.fail(provPhClone, coldMsg("la scheda e' stata appena prenotata da un'altra creazione"))
			return nil, &provRefusal{Reason: "card_reserved", Msg: "la scheda e' in uso da un'altra creazione in corso"}
		}
		j.card = card
	}
	log.Printf("provision: cluster=%d start vmid=%d name=%s on %s (template %d, %d core, %d MB, gpu=%t, start=%t, extra user=%t)",
		idx, plan.NewVMID, plan.Name, plan.TargetNode, plan.TemplateVMID, plan.Cores, plan.Memory, plan.GPUMapping != "", plan.start, plan.ExtraUser != nil)
	release = false // the procedure releases it once the RAM is set
	go runProv(c, j, plan, lock, optionsFor)
	return j, nil
}

// GetProvisionJob: GET /api/provision/job?id=<32 hex>
func (h *Handler) GetProvisionJob(w http.ResponseWriter, r *http.Request) {
	j := findProvJob(r.URL.Query().Get("id"), clusterIdx(r))
	if j == nil {
		writeError(w, errors.New("creazione non trovata"), http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, j.view())
}
