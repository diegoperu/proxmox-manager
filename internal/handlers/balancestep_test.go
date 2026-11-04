package handlers

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeStep struct {
	fakeBalance
	wmu       sync.Mutex
	migrated  []string
	taskState string // running | stopped
	migErr    error
	// guest state for the cold procedure
	status     map[int]string // vmid -> running|stopped (absent = running)
	loc        map[int]string // vmid -> node after a migration
	noShutdown bool           // the guest ignores the ordered shutdown
	startFails int            // the next N start requests are refused
	actions    []string
}

func (f *fakeStep) VMAction(node string, vmid int, action string, params url.Values) (json.RawMessage, error) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.actions = append(f.actions, action+"@"+node)
	if f.status == nil {
		f.status = map[int]string{}
	}
	switch action {
	case "shutdown":
		if params.Get("timeout") == "" {
			return nil, errors.New("shutdown without timeout")
		}
		if !f.noShutdown {
			f.status[vmid] = "stopped"
		}
	case "stop":
		f.status[vmid] = "stopped"
	case "start":
		if f.startFails > 0 {
			f.startFails--
			return nil, errors.New(`proxmox 500 on /x: {"message":"start failed: no memory"}`)
		}
		f.status[vmid] = "running"
	}
	return json.RawMessage(`"UPID:` + node + `:0000ABCD:00123456:65F0A1B2:qm` + action + `:1:root@pam!tok:"`), nil
}
func (f *fakeStep) GetVMStatus(node string, vmid int) (json.RawMessage, error) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if l, ok := f.loc[vmid]; ok && l != node {
		return nil, errors.New("proxmox 500: VM does not exist on this node")
	}
	st := "running"
	if v, ok := f.status[vmid]; ok {
		st = v
	}
	return json.RawMessage(`{"status":"` + st + `"}`), nil
}

const testUPID = `UPID:n1:0000ABCD:00123456:65F0A1B2:qmigrate:1:root@pam!tok:`

func (f *fakeStep) VMMigrate(node string, vmid int, target string, online bool) (json.RawMessage, error) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if f.migErr != nil {
		return nil, f.migErr
	}
	f.migrated = append(f.migrated, "qemu:"+node+">"+target+":"+itoaT(vmid)+":"+map[bool]string{true: "online", false: "offline"}[online])
	if f.loc == nil {
		f.loc = map[int]string{}
	}
	f.loc[vmid] = target
	return json.RawMessage(`"` + testUPID + `"`), nil
}
func (f *fakeStep) LXCMigrate(node string, vmid int, target string, restart bool) (json.RawMessage, error) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.migrated = append(f.migrated, "lxc:"+node+">"+target+":"+itoaT(vmid)+":"+map[bool]string{true: "restart", false: "stopped"}[restart])
	return json.RawMessage(`"` + testUPID + `"`), nil
}
func (f *fakeStep) GetTaskStatus(node, upid string) (json.RawMessage, error) {
	if f.taskState == "unreadable" {
		return nil, errors.New("proxmox 500: no such task")
	}
	if f.taskState == "stopped" {
		return json.RawMessage(`{"status":"stopped","exitstatus":"OK"}`), nil
	}
	return json.RawMessage(`{"status":"running"}`), nil
}

func stepFixture() (*fakeStep, balanceQuery) {
	factsReset()
	balanceRunning.Lock()
	balanceRunning.m = map[int]balanceRun{}
	balanceRunning.Unlock()
	f := &fakeStep{fakeBalance: fakeBalance{resources: resJSON(), rrdCPU: 0.1}, taskState: "stopped"}
	q, _ := parseBalanceQuery(query("cold", "1"))
	return f, q
}

func firstStep(t *testing.T, f *fakeStep, q balanceQuery) balanceStepReq {
	t.Helper()
	res, err := buildBalance(f, "C", 7, q, time.Now())
	if err != nil || len(res.Steps) == 0 {
		t.Fatalf("no step: %v %#v", err, res.Steps)
	}
	s := res.Steps[0]
	return balanceStepReq{Node: s.From, Kind: s.Kind, VMID: s.VMID, Target: s.To}
}

func TestStepRunsOnlyTheFirstStepOfTheFreshPlan(t *testing.T) {
	f, q := stepFixture()
	req := firstStep(t, f, q)
	st, err := runBalanceStep(f, "C", 7, q, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if node, id, err := parseUPID(st.UPID); err != nil || node != "n1" || id != testUPID {
		t.Fatalf("%s %s %v", node, id, err)
	}
	if len(f.migrated) != 1 || !strings.HasPrefix(f.migrated[0], "qemu:n1>n2:") || !strings.HasSuffix(f.migrated[0], ":online") {
		t.Fatalf("%v", f.migrated)
	}
}

func TestStepRejectsWhatThePlanDoesNotSay(t *testing.T) {
	f, q := stepFixture()
	good := firstStep(t, f, q)
	for name, mut := range map[string]func(*balanceStepReq){
		"other vm":       func(r *balanceStepReq) { r.VMID = 2 }, // GPU VM: blocked
		"locked vm":      func(r *balanceStepReq) { r.VMID = 3 },
		"ha vm":          func(r *balanceStepReq) { r.VMID = 4 },
		"offline target": func(r *balanceStepReq) { r.Target = "n3" },
		"wrong source":   func(r *balanceStepReq) { r.Node = "n2" },
		"wrong kind":     func(r *balanceStepReq) { r.Kind = "lxc" },
	} {
		r := good
		mut(&r)
		if _, err := runBalanceStep(f, "C", 7, q, r, time.Now()); !errors.Is(err, errStepChanged) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.migrated) != 0 {
		t.Fatalf("something was migrated: %v", f.migrated)
	}
}

func TestStepRejectsInvalidInputBeforeAnyCall(t *testing.T) {
	f, q := stepFixture()
	for _, r := range []balanceStepReq{
		{Node: "bad name", Kind: "qemu", VMID: 1, Target: "n2"}, {Node: "n1", Kind: "qemu", VMID: 1, Target: "n2;x"},
		{Node: "n1", Kind: "disk", VMID: 1, Target: "n2"}, {Node: "n1", Kind: "qemu", VMID: 0, Target: "n2"}, {Node: "n1", Kind: "qemu", VMID: -5, Target: "n2"},
	} {
		if _, err := runBalanceStep(f, "C", 7, q, r, time.Now()); err == nil || errors.Is(err, errStepChanged) {
			t.Errorf("%#v: %v", r, err)
		}
	}
	if len(f.calls) != 0 || len(f.migrated) != 0 {
		t.Fatalf("calls on invalid input: %v %v", f.calls, f.migrated)
	}
}

func TestStepOneMigrationAtATime(t *testing.T) {
	f, q := stepFixture()
	req := firstStep(t, f, q)
	f.taskState = "running"
	if _, err := runBalanceStep(f, "C", 7, q, req, time.Now()); err != nil {
		t.Fatal(err)
	}
	// the first task is still running: a second step is refused and nothing else is migrated
	if _, err := runBalanceStep(f, "C", 7, q, req, time.Now()); !errors.Is(err, errStepBusy) {
		t.Fatalf("%v", err)
	}
	if len(f.migrated) != 1 {
		t.Fatalf("%v", f.migrated)
	}
	f.taskState = "stopped"
	if _, err := runBalanceStep(f, "C", 7, q, req, time.Now()); err != nil {
		t.Fatalf("after the task ended: %v", err)
	}
	// another cluster is not blocked
	f.taskState = "running"
	if _, err := runBalanceStep(f, "C", 8, q, req, time.Now()); err != nil {
		t.Fatalf("other cluster: %v", err)
	}
}

func TestStepProxmoxErrorDoesNotRecordARun(t *testing.T) {
	f, q := stepFixture()
	req := firstStep(t, f, q)
	f.migErr = errors.New("proxmox 500: secret internal detail")
	if _, err := runBalanceStep(f, "C", 7, q, req, time.Now()); err == nil {
		t.Fatal("no error")
	}
	f.migErr = nil
	f.taskState = "running"
	if _, err := runBalanceStep(f, "C", 7, q, req, time.Now()); err != nil {
		t.Fatalf("a failed start must not block the next one: %v", err)
	}
}

func TestParseUPID(t *testing.T) {
	if n, _, err := parseUPID(json.RawMessage(`"` + testUPID + `"`)); err != nil || n != "n1" {
		t.Fatalf("%v %v", n, err)
	}
	for _, bad := range []string{``, `UPID:n1:1:2:3:t:i:u:/../x`, `UPID:bad name:1:2:3:t:i:root@pam:`, `UPID:n1:zz:2:3:t:i:root@pam:`, `x`, testUPID + `../../etc`, strings.Repeat("A", 500)} {
		if _, _, err := parseUPID(json.RawMessage(`"` + bad + `"`)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestReadTask(t *testing.T) {
	f := &fakeStep{taskState: "running"}
	if ti, err := readTask(f, "n1", testUPID); err != nil || ti.Status != "running" || ti.OK {
		t.Fatalf("%#v %v", ti, err)
	}
	f.taskState = "stopped"
	if ti, err := readTask(f, "n1", testUPID); err != nil || ti.Status != "stopped" || !ti.OK {
		t.Fatalf("%#v %v", ti, err)
	}
}

func TestStepUnreadablePreviousTaskBlocksForAnHourOnly(t *testing.T) {
	f, q := stepFixture()
	req := firstStep(t, f, q)
	start := time.Now()
	if _, err := runBalanceStep(f, "C", 7, q, req, start); err != nil {
		t.Fatal(err)
	}
	f.taskState = "unreadable"
	if _, err := runBalanceStep(f, "C", 7, q, req, start.Add(10*time.Minute)); !errors.Is(err, errStepBusy) {
		t.Fatalf("unreadable task just started must block: %v", err)
	}
	if _, err := runBalanceStep(f, "C", 7, q, req, start.Add(61*time.Minute)); err != nil {
		t.Fatalf("an unreadable task older than an hour must not block forever: %v", err)
	}
}

func TestStepRefusesReservesUnderTheExecutionMinimum(t *testing.T) {
	f, q := stepFixture()
	req := firstStep(t, f, q)
	for name, args := range map[string][]string{
		"host 0":       {"cold", "1", "host_reserve_gb", "0"},
		"host 7":       {"cold", "1", "host_reserve_gb", "7"},
		"node reserve": {"cold", "1", "reserve", "n2:4"},
	} {
		low, err := parseBalanceQuery(query(args...))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f.calls = nil
		if _, err := runBalanceStep(f, "C", 7, low, req, time.Now()); !errors.Is(err, errStepReserve) {
			t.Errorf("%s: %v", name, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: Proxmox called before refusing: %v", name, f.calls)
		}
		res, err := buildBalance(f, "C", 7, low, time.Now())
		if err != nil || res.Meta.ExecAllowed || res.Meta.ExecMinReserveGB != 8 {
			t.Errorf("%s: simulation must stay readable but not executable: %v %+v", name, err, res.Meta)
		}
	}
	if len(f.migrated) != 0 {
		t.Fatalf("migrated with a low reserve: %v", f.migrated)
	}
	// exactly the minimum, and per-OSD 0, are executable
	ok, _ := parseBalanceQuery(query("cold", "1", "host_reserve_gb", "8", "per_osd_gb", "0", "reserve", "n2:8"))
	if !ok.execReserveOK() {
		t.Fatal("8 GB must be executable")
	}
}
