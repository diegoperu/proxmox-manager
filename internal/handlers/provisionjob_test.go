package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProv is an in-memory cluster for the creation procedure: two nodes of 100 GB / 16 threads, a shared
// "ceph" storage, a template 9000 on n1, two NVIDIA cards on n2 (g1 free, g2 used by VM 50 directly).
// Cluster resources are derived from the VMs, so the RAM check sees what the procedure creates.
type fakeProv struct {
	mu           sync.Mutex
	vms          map[int]*fvm
	ver          map[int]int
	writes       []string // "op vmid key=value..." (no secret values: they are recorded as ***)
	cloudInit    url.Values
	execs        [][]string
	agentUp      bool
	agentFrom    time.Time // agent answers only after this time
	probes       int
	userExit     int
	failClone    bool
	failStart    int // the next N start requests fail
	onClone      func()
	nextID       int
	extraStorage bool // a non shared "local" storage on n1
}

type fvm struct {
	node, name, status string
	template           bool
	cfg                map[string]string
}

func newFakeProv() *fakeProv {
	return &fakeProv{
		vms: map[int]*fvm{
			9000: {node: "n1", name: "tpl", status: "stopped", template: true, cfg: map[string]string{"virtio0": "ceph:base-9000-disk-1,cache=writeback,size=32G", "efidisk0": "ceph:base-9000-disk-0,size=1M", "ide2": "ceph:vm-9000-cloudinit,media=cdrom", "memory": "1024", "cores": "1", "cpu": "host"}},
			50:   {node: "n2", name: "gpu-user", status: "running", cfg: map[string]string{"memory": "20480", "cores": "4", "hostpci0": "0000:02:00"}},
			51:   {node: "n1", name: "app", status: "running", cfg: map[string]string{"memory": "10240", "cores": "2", "tags": "gruppo-rossi"}},
		},
		ver: map[int]int{}, agentUp: true, nextID: 200,
	}
}

func (f *fakeProv) rec(s string) { f.writes = append(f.writes, s) }

func (f *fakeProv) digestOf(id int) string {
	return "d" + strconv.Itoa(id) + "v" + strconv.Itoa(f.ver[id])
}

func upidFor(node, typ string, id int) json.RawMessage {
	return json.RawMessage(`"UPID:` + node + `:0000ABCD:00123456:65F0A1B2:` + typ + `:` + strconv.Itoa(id) + `:root@pam!tok:"`)
}

func (f *fakeProv) GetClusterResources(t string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := func(n int64) int64 { return n * gibB }
	list := []map[string]interface{}{}
	if t == "" {
		list = append(list,
			map[string]interface{}{"type": "node", "node": "n1", "status": "online", "maxmem": g(100), "maxcpu": 16},
			map[string]interface{}{"type": "node", "node": "n2", "status": "online", "maxmem": g(100), "maxcpu": 16},
			map[string]interface{}{"type": "storage", "node": "n1", "storage": "ceph", "shared": 1, "status": "available", "content": "images"},
			map[string]interface{}{"type": "storage", "node": "n2", "storage": "ceph", "shared": 1, "status": "available", "content": "images"})
		if f.extraStorage {
			list = append(list, map[string]interface{}{"type": "storage", "node": "n1", "storage": "local", "shared": 0, "status": "available", "content": "images"})
		}
	}
	for id, v := range f.vms {
		mem, _ := strconv.Atoi(v.cfg["memory"])
		cores, _ := strconv.Atoi(v.cfg["cores"])
		r := map[string]interface{}{"type": "qemu", "vmid": id, "node": v.node, "name": v.name, "status": v.status,
			"maxmem": int64(mem) << 20, "maxcpu": cores, "tags": v.cfg["tags"]}
		if v.template {
			r["template"] = 1
		}
		list = append(list, r)
	}
	b, _ := json.Marshal(list)
	return b, nil
}
func (f *fakeProv) GetNodeRRDcf(n, tf, cf string) (json.RawMessage, error) {
	return json.RawMessage(`[]`), nil
}
func (f *fakeProv) GetGuestRRDcf(n, k string, id int, tf, cf string) (json.RawMessage, error) {
	return json.RawMessage(`[]`), nil
}
func (f *fakeProv) GetContainerConfig(n string, id int) (json.RawMessage, error) {
	return nil, errors.New("no")
}
func (f *fakeProv) GetMigratePrecheck(n string, id int) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (f *fakeProv) GetHAResources() (json.RawMessage, error) { return json.RawMessage(`[]`), nil }
func (f *fakeProv) GetNodes() (json.RawMessage, error) {
	return json.RawMessage(`[{"node":"n1","status":"online"},{"node":"n2","status":"online"}]`), nil
}
func (f *fakeProv) GetCephOSD(n string) (json.RawMessage, error) {
	return nil, errors.New("proxmox 500: binary not installed")
}
func (f *fakeProv) GetNodeStatus(n string) (json.RawMessage, error) {
	return json.RawMessage(`{"cpuinfo":{"model":"Intel Xeon"}}`), nil
}
func (f *fakeProv) GetNodePCI(node string) (json.RawMessage, error) {
	if node != "n2" {
		return json.RawMessage(`[]`), nil
	}
	return json.RawMessage(`[{"id":"0000:01:00.0","class":"0x030200","vendor":"0x10de","vendor_name":"NVIDIA Corporation","device_name":"GA100 [A100 SXM4 40GB]"},
	 {"id":"0000:02:00.0","class":"0x030200","vendor":"0x10de","vendor_name":"NVIDIA Corporation","device_name":"GA100 [A100 SXM4 40GB]"}]`), nil
}
func (f *fakeProv) GetPCIMappings() (json.RawMessage, error) {
	return json.RawMessage(`[{"id":"g1","map":["node=n2,path=0000:01:00"]},{"id":"g2","map":["node=n2,path=0000:02:00"]},{"id":"far","map":["node=n1,path=0000:09:00"]}]`), nil
}
func (f *fakeProv) GetVMs(node string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := []map[string]interface{}{}
	for id, v := range f.vms {
		if v.node == node {
			list = append(list, map[string]interface{}{"vmid": id, "name": v.name, "status": v.status})
		}
	}
	b, _ := json.Marshal(list)
	return b, nil
}
func (f *fakeProv) cfgOf(node string, id int) (map[string]interface{}, error) {
	v := f.vms[id]
	if v == nil || v.node != node {
		return nil, fmt.Errorf("proxmox 500 on /nodes/%s/qemu/%d/config: Configuration file does not exist", node, id)
	}
	c := map[string]interface{}{"digest": f.digestOf(id), "name": v.name}
	for k, x := range v.cfg {
		c[k] = x
	}
	if v.template {
		c["template"] = 1
	}
	return c, nil
}
func (f *fakeProv) GetVMConfig(node string, id int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.cfgOf(node, id)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(c)
	return b, nil
}
func (f *fakeProv) GetGuestConfig(node, kind string, id int) (json.RawMessage, error) {
	return f.GetVMConfig(node, id)
}
func (f *fakeProv) SetGuestConfig(node, kind string, id int, p url.Values) (json.RawMessage, error) {
	return f.VMSetConfig(node, id, p)
}
func (f *fakeProv) GetVMStatus(node string, id int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.vms[id]
	if v == nil || v.node != node {
		return nil, errors.New("proxmox 500: VM does not exist")
	}
	return json.RawMessage(`{"status":"` + v.status + `"}`), nil
}
func (f *fakeProv) VMSetConfig(node string, id int, p url.Values) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.vms[id]
	if v == nil || v.node != node {
		return nil, errors.New("proxmox 500: does not exist")
	}
	if d := p.Get("digest"); d != "" && d != f.digestOf(id) {
		return nil, errors.New("proxmox 500: detected modified configuration - file changed by other user? Try again")
	}
	keys := []string{}
	for k, x := range p {
		if k == "digest" {
			continue
		}
		if k == "delete" {
			for _, d := range strings.Split(x[0], ",") {
				delete(v.cfg, d)
			}
			continue
		}
		v.cfg[k] = x[0]
		keys = append(keys, k+"="+x[0])
	}
	f.ver[id]++
	f.rec("config " + strconv.Itoa(id) + " " + strings.Join(sortedStrings(keys), " "))
	return json.RawMessage(`null`), nil
}
func sortedStrings(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}
func (f *fakeProv) GetNextVMID() (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.RawMessage(`"` + strconv.Itoa(f.nextID) + `"`), nil
}
func (f *fakeProv) VMClone(node string, id, newid int, p url.Values) (json.RawMessage, error) {
	if f.onClone != nil {
		f.onClone()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.vms[id]
	if src == nil || src.node != node {
		return nil, errors.New("proxmox 500: template not found")
	}
	if f.failClone {
		return nil, errors.New(`proxmox 500 on /x?token=SECRET: {"message":"clone failed: storage full"}`)
	}
	if f.vms[newid] != nil {
		return nil, errors.New("proxmox 500: VM exists")
	}
	target := node
	if t := p.Get("target"); t != "" {
		target = t
	}
	cfg := map[string]string{}
	for k, x := range src.cfg {
		cfg[k] = strings.ReplaceAll(x, "base-9000", "vm-"+strconv.Itoa(newid))
	}
	f.vms[newid] = &fvm{node: target, name: p.Get("name"), status: "stopped", cfg: cfg}
	f.nextID = newid + 1
	f.rec(fmt.Sprintf("clone %d->%d on %s full=%s", id, newid, target, p.Get("full")))
	return upidFor(node, "qmclone", id), nil
}
func (f *fakeProv) VMResizeDisk(node string, id int, disk, size string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec(fmt.Sprintf("resize %d %s %s", id, disk, size))
	return json.RawMessage(`null`), nil
}
func (f *fakeProv) VMSetCloudInit(node string, id int, p url.Values) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cloudInit = p
	keys := []string{}
	for k := range p {
		keys = append(keys, k)
	}
	f.rec("cloudinit " + strconv.Itoa(id) + " " + strings.Join(sortedStrings(keys), " "))
	return json.RawMessage(`null`), nil
}
func (f *fakeProv) VMAction(node string, id int, action string, p url.Values) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if action == "start" && f.failStart > 0 {
		f.failStart--
		return nil, errors.New(`proxmox 500: {"message":"start failed: no IOMMU"}`)
	}
	if v := f.vms[id]; v != nil && action == "start" {
		v.status = "running"
	}
	f.rec(fmt.Sprintf("%s %d", action, id))
	return upidFor(node, "qm"+action, id), nil
}
func (f *fakeProv) VMMigrate(node string, id int, target string, online bool) (json.RawMessage, error) {
	return nil, errors.New("not used")
}
func (f *fakeProv) GetTaskStatus(node, upid string) (json.RawMessage, error) {
	return json.RawMessage(`{"status":"stopped","exitstatus":"OK"}`), nil
}
func (f *fakeProv) AgentExec(node string, id int, args []string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.agentUp || time.Now().Before(f.agentFrom) {
		f.probes++
		return nil, errors.New("proxmox 500: QEMU guest agent is not running")
	}
	f.execs = append(f.execs, args)
	return json.RawMessage(`{"pid":` + strconv.Itoa(len(f.execs)) + `}`), nil
}
func (f *fakeProv) AgentExecStatus(node string, id, pid int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	args := f.execs[pid-1]
	if strings.Contains(strings.Join(args, " "), "[ -x") {
		return json.RawMessage(`{"exited":1,"exitcode":0,"out-data":"INLINE\n"}`), nil
	}
	return json.RawMessage(`{"exited":1,"exitcode":` + strconv.Itoa(f.userExit) + `}`), nil
}

// ── helpers ──

func provFast(t *testing.T) {
	t.Helper()
	coldFast(t)
	old := provTiming
	provTiming.Clone, provTiming.Task, provTiming.Agent, provTiming.AgentPoll = time.Second, time.Second, 40*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { provTiming = old })
	provReserved.Lock()
	provReserved.m = map[string]string{}
	provReserved.Unlock()
}

func baseReq() provReq {
	return provReq{TemplateNode: "n1", TemplateVMID: 9000, TargetNode: "n2", Name: "new-vm", Cores: 4, Memory: 8192, DiskGB: 50,
		DiskDevice: "virtio0", IP: "10.75.4.20/24", Gateway: "10.75.4.254", DNS: "8.8.8.8 1.1.1.1", CIUser: "ubuntu",
		CIPassword: "S3cret!pw", SSHKeys: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl me@host"}
}

func mustPlanProv(t *testing.T, r provReq) provPlan {
	t.Helper()
	p, err := validateProvReq(r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func waitProv(t *testing.T, j *provJob) provView {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		if v := j.view(); v.Phase == provPhDone {
			return v
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("procedure never ended: %+v", j.view())
	return provView{}
}

func noOpts(string) string { return "" }

func (f *fakeProv) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.writes, "\n")
}

// ── tests ──

func TestProvisionHappyPathWithGPUAndExtraUser(t *testing.T) {
	provFast(t)
	var buf lockedBuf
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	f := newFakeProv()
	r := baseReq()
	r.GPUMapping = "g1"
	r.Group = "rossi"
	r.ExtraUser = &provUser{Username: "mario", SSHKey: ""}
	j, err := startProv(f, 3, "C", mustPlanProv(t, r), noOpts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v := waitProv(t, j)
	if v.Result != provOK || !v.VMCreated || v.VMID != 200 || v.Node != "n2" || len(v.Warnings) != 0 {
		t.Fatalf("%+v", v)
	}
	vm := f.vms[200]
	if vm.node != "n2" || vm.status != "running" || vm.cfg["memory"] != "8192" || vm.cfg["cores"] != "4" ||
		vm.cfg["hostpci0"] != "mapping=g1" || vm.cfg["tags"] != "gruppo-rossi" {
		t.Fatalf("vm %+v", vm)
	}
	w := f.log()
	for _, want := range []string{"clone 9000->200 on n2 full=1", "resize 200 virtio0 +18G", "cloudinit 200 cipassword ciuser ipconfig0 nameserver sshkeys", "start 200"} {
		if !strings.Contains(w, want) {
			t.Errorf("missing %q in\n%s", want, w)
		}
	}
	if f.cloudInit.Get("ipconfig0") != "ip=10.75.4.20/24,gw=10.75.4.254" || f.cloudInit.Get("nameserver") != "8.8.8.8 1.1.1.1" ||
		strings.Contains(f.cloudInit.Get("sshkeys"), "+") || strings.Contains(f.cloudInit.Get("sshkeys"), " ") {
		t.Fatalf("cloud-init %v", f.cloudInit)
	}
	// the additional user went to the guest as an ARGUMENT of the script, never inside its text
	last := f.execs[len(f.execs)-1]
	if last[len(last)-2] != "mario" || strings.Contains(last[2], "mario") {
		t.Fatalf("exec %q", last)
	}
	// order: GPU before the start, the user after the agent
	if strings.Index(w, "config 200 hostpci0") > strings.Index(w, "start 200") {
		t.Fatalf("GPU after start:\n%s", w)
	}
	if len(reservedCards(3)) != 0 {
		t.Fatal("card still reserved after the end")
	}
	// no secret in the server log
	for _, secret := range []string{"S3cret!pw", "AAAAC3NzaC1lZDI1NTE5", "token=SECRET"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("secret %q in the log:\n%s", secret, buf.String())
		}
	}
}

func TestProvisionRefusalsWriteNothing(t *testing.T) {
	provFast(t)
	cases := map[string]struct {
		mut    func(*provReq)
		reason string
	}{
		"node over the RAM limit":  {func(r *provReq) { r.Memory = 80 * 1024 }, "node_not_eligible"},
		"card in use":              {func(r *provReq) { r.GPUMapping = "g2" }, "card_unavailable"},
		"mapping of another node":  {func(r *provReq) { r.GPUMapping = "far" }, "card_unavailable"},
		"template without disk":    {func(r *provReq) { r.DiskDevice = "scsi0" }, "template_disk"},
		"smaller disk":             {func(r *provReq) { r.DiskGB = 10 }, "disk_smaller"},
		"new group not confirmed":  {func(r *provReq) { r.Group = "nuovo" }, "new_group"},
		"VMID already used":        {func(r *provReq) { r.NewVMID = 9000 }, "vmid_taken"},
		"template that is missing": {func(r *provReq) { r.TemplateVMID = 9999 }, "template"},
	}
	for name, c := range cases {
		f := newFakeProv()
		r := baseReq()
		c.mut(&r)
		_, err := startProv(f, 4, "C", mustPlanProv(t, r), noOpts, time.Now())
		var ref *provRefusal
		if !errors.As(err, &ref) || ref.Reason != c.reason {
			t.Errorf("%s: %v", name, err)
		}
		if w := f.log(); w != "" {
			t.Errorf("%s: something was written:\n%s", name, w)
		}
	}
	// the busy card says why
	f := newFakeProv()
	r := baseReq()
	r.GPUMapping = "g2"
	_, err := startProv(f, 4, "C", mustPlanProv(t, r), noOpts, time.Now())
	var ref *provRefusal
	if !errors.As(err, &ref) || len(ref.Reasons) == 0 || ref.Reasons[0] != adviceCardBusy {
		t.Fatalf("%+v", err)
	}
}

func TestProvisionCardReservedByAnotherCreation(t *testing.T) {
	provFast(t)
	f := newFakeProv()
	if !reserveCard(cardKey(5, "n2", "0000:01:00"), "other-job") {
		t.Fatal("reserve")
	}
	defer releaseCard(cardKey(5, "n2", "0000:01:00"), "other-job")
	r := baseReq()
	r.GPUMapping = "g1"
	_, err := startProv(f, 5, "C", mustPlanProv(t, r), noOpts, time.Now())
	var ref *provRefusal
	if !errors.As(err, &ref) || f.log() != "" {
		t.Fatalf("%v\n%s", err, f.log())
	}
	// the advice shows it as reserved, not selectable
	adv, err := buildAdvice(f, mustInv(t, f), "C", adviceQuery{MemMB: 1024, Cores: 1, GPU: true}, reservedCards(5), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range adv.Cards {
		if c.Addr == "0000:01:00" && (!c.Reserved || c.Selectable || c.Reasons[0] != adviceCardReserved) {
			t.Fatalf("%+v", c)
		}
	}
}

func mustInv(t *testing.T, f *fakeProv) *GPUInventory {
	t.Helper()
	inv, err := fetchGPUInventory(f, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &inv
}

func TestProvisionFailuresLeaveTheVMStoppedAndNeverDelete(t *testing.T) {
	provFast(t)
	// GPU taken between the check and the write: no start, VM stopped, failed at gpu
	f := newFakeProv()
	f.onClone = func() {
		f.mu.Lock()
		f.vms[50].cfg["hostpci1"] = "mapping=g1"
		f.mu.Unlock()
	}
	r := baseReq()
	r.GPUMapping = "g1"
	j, err := startProv(f, 6, "C", mustPlanProv(t, r), noOpts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v := waitProv(t, j)
	if v.Result != provFailed || v.FailedAt != provPhGPU || !v.VMCreated || f.vms[200].status != "stopped" || strings.Contains(f.log(), "start 200") {
		t.Fatalf("%+v\n%s", v, f.log())
	}
	if f.vms[200] == nil {
		t.Fatal("VM deleted")
	}
	// start refused: failed at start, VM exists and is stopped
	f = newFakeProv()
	f.failStart = 1
	j, _ = startProv(f, 6, "C", mustPlanProv(t, baseReq()), noOpts, time.Now())
	v = waitProv(t, j)
	if v.Result != provFailed || v.FailedAt != provPhStart || f.vms[200].status != "stopped" {
		t.Fatalf("%+v", v)
	}
	if last := v.Steps[len(v.Steps)-1]; last.Note != "start failed: no IOMMU" {
		t.Fatalf("note %+v", last)
	}
	// clone refused: failed at clone, no VM, Proxmox URL/token not in the note
	f = newFakeProv()
	f.failClone = true
	j, _ = startProv(f, 6, "C", mustPlanProv(t, baseReq()), noOpts, time.Now())
	v = waitProv(t, j)
	if v.Result != provFailed || v.FailedAt != provPhClone || v.VMCreated || strings.Contains(fmt.Sprint(v.Steps), "SECRET") {
		t.Fatalf("%+v", v)
	}
}

func TestProvisionAgentWaitsTwiceThenWarns(t *testing.T) {
	provFast(t)
	f := newFakeProv()
	f.agentUp = false
	r := baseReq()
	r.ExtraUser = &provUser{Username: "mario"}
	start := time.Now()
	j, _ := startProv(f, 7, "C", mustPlanProv(t, r), noOpts, time.Now())
	v := waitProv(t, j)
	if v.Result != provWarnings || len(v.Warnings) != 1 || v.Warnings[0] != "agent_timeout" || f.vms[200].status != "running" {
		t.Fatalf("%+v", v)
	}
	if time.Since(start) < 2*provTiming.Agent {
		t.Fatal("did not wait twice")
	}
	// agent that comes up during the second wait: the user is created
	f = newFakeProv()
	f.agentFrom = time.Now().Add(provTiming.Agent + 15*time.Millisecond)
	j, _ = startProv(f, 7, "C", mustPlanProv(t, r), noOpts, time.Now())
	if v := waitProv(t, j); v.Result != provOK {
		t.Fatalf("second wait: %+v", v)
	}
	// existing user: warning, no change
	f = newFakeProv()
	f.userExit = 3
	j, _ = startProv(f, 7, "C", mustPlanProv(t, r), noOpts, time.Now())
	if v := waitProv(t, j); v.Result != provWarnings || v.Warnings[0] != "user_exists" {
		t.Fatalf("%+v", v)
	}
}

func TestProvisionWithoutStart(t *testing.T) {
	provFast(t)
	f := newFakeProv()
	r := baseReq()
	no := false
	r.Start = &no
	j, _ := startProv(f, 8, "C", mustPlanProv(t, r), noOpts, time.Now())
	if v := waitProv(t, j); v.Result != provOK || f.vms[200].status != "stopped" || strings.Contains(f.log(), "start") {
		t.Fatalf("%+v %s", v, f.log())
	}
}

// Two creations at the same time on a node with room for only one: exactly one passes the RAM check.
func TestProvisionConcurrentCreationsDoNotOverbook(t *testing.T) {
	provFast(t)
	for round := 0; round < 10; round++ {
		f := newFakeProv()
		r := baseReq()
		r.Memory = 40 * 1024 // n2: 20 GB used, 92 assignable (reserve 8): one fits (60), two do not (100)
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok, refused := 0, 0
		var jobs []*provJob
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				j, err := startProv(f, 9, "C", mustPlanProv(t, r), noOpts, time.Now())
				mu.Lock()
				defer mu.Unlock()
				var ref *provRefusal
				switch {
				case err == nil:
					ok++
					jobs = append(jobs, j)
				case errors.As(err, &ref) && (ref.Reason == "node_not_eligible" || ref.Reason == "busy"):
					refused++
				default:
					t.Errorf("round %d: %v", round, err)
				}
			}()
		}
		wg.Wait()
		for _, j := range jobs {
			waitProv(t, j)
		}
		if ok != 1 || refused != 1 {
			t.Fatalf("round %d: ok=%d refused=%d", round, ok, refused)
		}
		// once the first one has set its RAM, a new request is refused for RAM, not for being busy
		_, err := startProv(f, 9, "C", mustPlanProv(t, r), noOpts, time.Now())
		var ref *provRefusal
		if !errors.As(err, &ref) || ref.Reason != "node_not_eligible" {
			t.Fatalf("round %d: after the first: %v", round, err)
		}
	}
}

func TestValidateProvReq(t *testing.T) {
	if _, err := validateProvReq(baseReq()); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*provReq){
		"bad target":      func(r *provReq) { r.TargetNode = "n2;x" },
		"bad template":    func(r *provReq) { r.TemplateVMID = 0 },
		"small vmid":      func(r *provReq) { r.NewVMID = 5 },
		"bad name":        func(r *provReq) { r.Name = "-bad" },
		"long name":       func(r *provReq) { r.Name = strings.Repeat("a", 64) },
		"name with space": func(r *provReq) { r.Name = "a b" },
		"no cores":        func(r *provReq) { r.Cores = 0 },
		"little ram":      func(r *provReq) { r.Memory = 64 },
		"negative disk":   func(r *provReq) { r.DiskGB = -1 },
		"bad device":      func(r *provReq) { r.DiskDevice = "nvme0" },
		"bad ip":          func(r *provReq) { r.IP = "10.0.0.300" },
		"ipv6":            func(r *provReq) { r.IP = "fd00::1/64" },
		"ip injection":    func(r *provReq) { r.IP = "10.0.0.1/24,gw=1.1.1.1" },
		"bad gateway":     func(r *provReq) { r.Gateway = "x" },
		"gateway alone":   func(r *provReq) { r.IP = "" },
		"bad dns":         func(r *provReq) { r.DNS = "8.8.8.8 evil" },
		"four dns":        func(r *provReq) { r.DNS = "1.1.1.1 2.2.2.2 3.3.3.3 4.4.4.4" },
		"bad key":         func(r *provReq) { r.SSHKeys = "ssh-ed25519 AAAA'; rm -rf /" },
		"bad ciuser":      func(r *provReq) { r.CIUser = "Root!" },
		"control in pw":   func(r *provReq) { r.CIPassword = "a\nb" },
		"long pw":         func(r *provReq) { r.CIPassword = strings.Repeat("a", 129) },
		"bad group":       func(r *provReq) { r.Group = "Bad Group" },
		"bad mapping":     func(r *provReq) { r.GPUMapping = "g1;x" },
		"bad extra user":  func(r *provReq) { r.ExtraUser = &provUser{Username: "Bad"} },
		"same user":       func(r *provReq) { r.ExtraUser = &provUser{Username: "ubuntu"} },
		"extra user key":  func(r *provReq) { r.ExtraUser = &provUser{Username: "mario", SSHKey: "nope"} },
		"user, no start":  func(r *provReq) { no := false; r.Start = &no; r.ExtraUser = &provUser{Username: "mario"} },
		"nine keys":       func(r *provReq) { r.SSHKeys = strings.Repeat(baseReq().SSHKeys+"\n", 9) },
	} {
		r := baseReq()
		mut(&r)
		if _, err := validateProvReq(r); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// normalisation: bare IP gets /24, DNS separators, CRLF keys
	r := baseReq()
	r.IP, r.DNS = "10.75.4.21", "8.8.8.8,1.1.1.1"
	r.SSHKeys = baseReq().SSHKeys + "\r\n\r\n" + baseReq().SSHKeys + "\r" + baseReq().SSHKeys // CRLF and CR-only
	p, err := validateProvReq(r)
	if err != nil || p.ipcfg != "ip=10.75.4.21/24,gw=10.75.4.254" || p.dns != "8.8.8.8 1.1.1.1" || strings.Count(p.sshKeys, "\n") != 2 || p.DiskDevice != "virtio0" || !p.start {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestDiskSizeGB(t *testing.T) {
	for line, want := range map[string]int{
		"ceph:vm-1-disk-0,size=32G": 32, "ceph:x,size=1T,cache=none": 1024, "local:x,size=512M": 1, "x,size=10.5G": 11, "x,size=2048K": 1,
	} {
		if got, ok := diskSizeGB(line); !ok || got != want {
			t.Errorf("%q: %d %v", line, got, ok)
		}
	}
	if _, ok := diskSizeGB("ceph:x,cache=none"); ok {
		t.Error("no size accepted")
	}
}

func TestProvisionHTTP(t *testing.T) {
	h := &Handler{}
	for _, body := range []string{``, `{}`, `{"name":"x","bogus":1}`, `not json`, `{"template_node":"n1"}{"a":1}`, strings.Repeat("a", 70<<10)} {
		rec := httptest.NewRecorder()
		h.PostProvisionJob(rec, httptest.NewRequest("POST", "/api/provision/job", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%.40q: %d", body, rec.Code)
		}
	}
	provFast(t)
	f := newFakeProv()
	j, err := startProv(f, 2, "C", mustPlanProv(t, baseReq()), noOpts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	waitProv(t, j)
	get := func(u string) int {
		rec := httptest.NewRecorder()
		h.GetProvisionJob(rec, httptest.NewRequest("GET", u, nil))
		return rec.Code
	}
	if c := get("/api/provision/job?cluster=2&id=" + j.ID); c != 200 {
		t.Fatalf("own job %d", c)
	}
	for _, u := range []string{"/api/provision/job?cluster=3&id=" + j.ID, "/api/provision/job?cluster=2&id=x", "/api/provision/job?cluster=2"} {
		if c := get(u); c != http.StatusNotFound {
			t.Errorf("%s: %d", u, c)
		}
	}
}

// lockedBuf: the log written by the procedure's goroutine while the test reads it.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestProvisionReviewRefusals(t *testing.T) {
	provFast(t)
	cases := map[string]struct {
		mut    func(*fakeProv, *provReq)
		reason string
	}{
		// a normal VM is not cloned (its data would end up in a VM with new users and keys)
		"not a template": {func(f *fakeProv, r *provReq) {
			r.TemplateNode, r.TemplateVMID = "n1", 51
			f.vms[51].cfg["virtio0"] = "ceph:vm-51-disk-0,size=10G"
		}, "not_template"},
		// the cloud-init drive is not a disk to resize
		"cdrom as disk": {func(f *fakeProv, r *provReq) { r.DiskDevice = "ide2" }, "template_disk"},
		// an EFI disk on a storage the target cannot reach
		"local EFI disk": {func(f *fakeProv, r *provReq) { f.vms[9000].cfg["efidisk0"] = "local:base-9000-disk-0,size=1M" }, "node_not_eligible"},
	}
	for name, c := range cases {
		f := newFakeProv()
		r := baseReq()
		c.mut(f, &r)
		_, err := startProv(f, 11, "C", mustPlanProv(t, r), noOpts, time.Now())
		var ref *provRefusal
		if !errors.As(err, &ref) || ref.Reason != c.reason {
			t.Errorf("%s: %v", name, err)
		}
		if w := f.log(); w != "" {
			t.Errorf("%s: written:\n%s", name, w)
		}
	}
	// the local EFI disk is fine when the target IS the template's node
	f := newFakeProv()
	f.vms[9000].cfg["efidisk0"] = "local:base-9000-disk-0,size=1M"
	f.mu.Lock()
	f.extraStorage = true
	f.mu.Unlock()
	r := baseReq()
	r.TargetNode = "n1"
	j, err := startProv(f, 11, "C", mustPlanProv(t, r), noOpts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v := waitProv(t, j); v.Result != provOK {
		t.Fatalf("%+v", v)
	}
}

func TestProvisionCloneRefusedIsNeverOurs(t *testing.T) {
	provFast(t)
	f := newFakeProv()
	f.failClone = true // refused before starting, e.g. the VMID was just taken
	f.vms[200] = &fvm{node: "n2", name: "someone-else", status: "running", cfg: map[string]string{"memory": "1024", "cores": "1"}}
	j, err := startProv(f, 12, "C", mustPlanProv(t, baseReq()), noOpts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v := waitProv(t, j); v.Result != provFailed || v.VMCreated {
		t.Fatalf("someone else's VM reported as ours: %+v", v)
	}
}

func TestValidateProvIPv4Mapped(t *testing.T) {
	for _, ip := range []string{"::ffff:10.0.0.1/120", "10.0.0.1/0", "10.0.0.1/33"} {
		r := baseReq()
		r.IP = ip
		if _, err := validateProvReq(r); err == nil {
			t.Errorf("%s accepted", ip)
		}
	}
}

func TestTemplateStorages(t *testing.T) {
	got := templateStorages(map[string]interface{}{
		"virtio0": "ceph:base-1-disk-1,size=32G", "efidisk0": "ceph:base-1-disk-0", "ide2": "local:iso/x.iso,media=cdrom",
		"tpmstate0": "fast:vm-1-tpm", "net0": "virtio=AA,bridge=vmbr0", "scsi1": "none,media=cdrom", "unused0": "other:vm-1-disk-9", "name": "x",
	})
	if strings.Join(got, ",") != "ceph,fast,local" {
		t.Fatalf("%v", got)
	}
}
