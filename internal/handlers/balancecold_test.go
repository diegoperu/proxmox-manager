package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fast timings for the procedure; restored after each test
func coldFast(t *testing.T) {
	t.Helper()
	old := coldTiming
	coldTiming.Poll, coldTiming.Shutdown, coldTiming.Decide, coldTiming.Stop = time.Millisecond, 30*time.Millisecond, 200*time.Millisecond, 30*time.Millisecond
	coldTiming.Migrate, coldTiming.Start = time.Second, time.Second
	t.Cleanup(func() { coldTiming = old })
}

// coldPlan: the same query with the first step's VM chosen cold (cold_vms is part of the plan, D55)
func coldPlan(t *testing.T, f *fakeStep, q balanceQuery) (balanceQuery, balanceStepReq) {
	t.Helper()
	first := firstStep(t, f, q)
	cq, err := parseBalanceQuery(query("cold", "1", "cold_vms", itoaT(first.VMID)))
	if err != nil {
		t.Fatal(err)
	}
	req := firstStep(t, f, cq)
	if req.VMID != first.VMID {
		t.Fatalf("cold plan starts elsewhere: %+v", req)
	}
	req.Mode = "cold"
	return cq, req
}

func coldStart(t *testing.T, f *fakeStep, q balanceQuery) (*coldJob, balanceStepReq) {
	t.Helper()
	q, req := coldPlan(t, f, q)
	st, err := runBalanceStep(f, "C", 7, q, req, time.Now())
	if err != nil || st.Job == nil || st.UPID != nil {
		t.Fatalf("cold start: %v %+v", err, st)
	}
	return st.Job, req
}

func waitPhase(t *testing.T, j *coldJob, phase string) coldView {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		if v := j.view(); v.Phase == phase {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("phase %s never reached: %+v", phase, j.view())
	return coldView{}
}

func (f *fakeStep) acts() string {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	return strings.Join(f.actions, ",") + "|" + strings.Join(f.migrated, ",")
}

func TestColdHappyPath(t *testing.T) {
	coldFast(t)
	f, q := stepFixture()
	j, req := coldStart(t, f, q)
	v := waitPhase(t, j, coldDone)
	if v.Result != coldOK || v.Detail != "" {
		t.Fatalf("%+v", v)
	}
	want := "shutdown@" + req.Node + ",start@" + req.Target + "|qemu:" + req.Node + ">" + req.Target + ":" + itoaT(req.VMID) + ":offline"
	if got := f.acts(); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestColdOneProcedureAtATimeAndNotLiveWhileRunning(t *testing.T) {
	coldFast(t)
	f, q := stepFixture()
	f.noShutdown = true
	j, req := coldStart(t, f, q)
	waitPhase(t, j, coldNeedForce)
	for _, mode := range []string{"cold", "live", ""} {
		req.Mode = mode
		if _, err := runBalanceStep(f, "C", 7, q, req, time.Now()); !errors.Is(err, errStepBusy) {
			t.Errorf("mode %q while a procedure runs: %v", mode, err)
		}
	}
	waitPhase(t, j, coldDone) // nobody answers: cancelled
	if v := j.view(); v.Result != coldCancelled {
		t.Fatalf("%+v", v)
	}
	if strings.Contains(f.acts(), "stop@") || strings.Contains(f.acts(), "qemu:") {
		t.Fatalf("forced stop or migration without a decision: %s", f.acts())
	}
	// finished: the next step may start
	req.Mode = ""
	if _, err := runBalanceStep(f, "C", 7, q, req, time.Now()); err != nil {
		t.Fatalf("after the procedure: %v", err)
	}
}

func TestColdForceStopOnlyWithTheUsersYes(t *testing.T) {
	coldFast(t)
	coldTiming.Decide = 5 * time.Second
	f, q := stepFixture()
	f.noShutdown = true
	j, req := coldStart(t, f, q)
	waitPhase(t, j, coldNeedForce)
	rec := decide(j, `{"force":true}`, 7)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// the question is closed: a second answer is refused
	if rec := decide(j, `{"force":false}`, 7); rec.Code != http.StatusConflict {
		t.Fatalf("second answer: %d", rec.Code)
	}
	v := waitPhase(t, j, coldDone)
	if v.Result != coldOK || !strings.Contains(f.acts(), "stop@"+req.Node) {
		t.Fatalf("%+v %s", v, f.acts())
	}
}

func TestColdNoMeansTheVMStaysOn(t *testing.T) {
	coldFast(t)
	coldTiming.Decide = 5 * time.Second
	f, q := stepFixture()
	f.noShutdown = true
	j, _ := coldStart(t, f, q)
	waitPhase(t, j, coldNeedForce)
	if rec := decide(j, `{"force":false}`, 7); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	if v := waitPhase(t, j, coldDone); v.Result != coldCancelled {
		t.Fatalf("%+v", v)
	}
	if a := f.acts(); strings.Contains(a, "stop@") || strings.Contains(a, "qemu:") {
		t.Fatalf("%s", a)
	}
}

func TestColdCancelRestartsAVMThatStoppedLate(t *testing.T) {
	coldFast(t)
	coldTiming.Decide = 5 * time.Second
	f, q := stepFixture()
	f.noShutdown = true
	j, req := coldStart(t, f, q)
	waitPhase(t, j, coldNeedForce)
	f.wmu.Lock()
	f.status[req.VMID] = "stopped" // the ACPI request was honoured after the wait
	f.wmu.Unlock()
	decide(j, `{"force":false}`, 7)
	if v := waitPhase(t, j, coldDone); v.Result != coldCancelRestarted {
		t.Fatalf("%+v", v)
	}
	if !strings.Contains(f.acts(), "start@"+req.Node) {
		t.Fatalf("not restarted on its node: %s", f.acts())
	}
}

func TestColdMigrationFailureRestartsOnSource(t *testing.T) {
	coldFast(t)
	f, q := stepFixture()
	q, req := coldPlan(t, f, q)
	f.migErr = errors.New(`proxmox 500 on /nodes/n1/qemu/1/migrate?token=SECRET: {"message":"target storage not available"}`)
	st, err := runBalanceStep(f, "C", 7, q, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v := waitPhase(t, st.Job, coldDone)
	if v.Result != coldMigrateRestart || v.Detail != "target storage not available" {
		t.Fatalf("%+v", v)
	}
	if !strings.HasSuffix(strings.Split(f.acts(), "|")[0], "start@"+req.Node) {
		t.Fatalf("not restarted on the source: %s", f.acts())
	}
}

func TestColdStartRetriedOnceThenLeftStopped(t *testing.T) {
	coldFast(t)
	f, q := stepFixture()
	f.startFails = 1
	j, _ := coldStart(t, f, q)
	if v := waitPhase(t, j, coldDone); v.Result != coldOK {
		t.Fatalf("one failure must be retried: %+v", v)
	}

	f, q = stepFixture()
	f.startFails = 5
	j, req := coldStart(t, f, q)
	v := waitPhase(t, j, coldDone)
	if v.Result != coldStartFailed || v.Detail != "start failed: no memory" {
		t.Fatalf("%+v", v)
	}
	if n := strings.Count(f.acts(), "start@"+req.Target); n != 2 {
		t.Fatalf("start attempts on the target = %d (%s)", n, f.acts())
	}
	if strings.Count(f.acts(), "qemu:") != 1 {
		t.Fatalf("no automatic migration back: %s", f.acts())
	}
}

func TestColdRejectsBadModes(t *testing.T) {
	f, q := stepFixture()
	req := firstStep(t, f, q)
	f.calls = nil
	for _, r := range []balanceStepReq{
		{Node: req.Node, Kind: "lxc", VMID: req.VMID, Target: req.Target, Mode: "cold"},
		{Node: req.Node, Kind: req.Kind, VMID: req.VMID, Target: req.Target, Mode: "warm"},
		{Node: req.Node, Kind: req.Kind, VMID: req.VMID, Target: req.Target, Mode: "COLD"},
	} {
		if _, err := runBalanceStep(f, "C", 7, q, r, time.Now()); err == nil || errors.Is(err, errStepChanged) {
			t.Errorf("%+v: %v", r, err)
		}
	}
	if len(f.calls) != 0 || f.acts() != "|" {
		t.Fatalf("calls on invalid input: %v %s", f.calls, f.acts())
	}
}

func decide(j *coldJob, body string, cluster int) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/balance/job/decide?id="+j.ID+"&cluster="+itoaT(cluster), strings.NewReader(body))
	rec := httptest.NewRecorder()
	(&Handler{}).PostBalanceJobDecide(rec, r)
	return rec
}

func TestColdJobEndpointsValidate(t *testing.T) {
	coldFast(t)
	coldTiming.Decide = 5 * time.Second
	f, q := stepFixture()
	f.noShutdown = true
	j, _ := coldStart(t, f, q)
	waitPhase(t, j, coldNeedForce)
	h := &Handler{}
	get := func(url string) int {
		rec := httptest.NewRecorder()
		h.GetBalanceJob(rec, httptest.NewRequest("GET", url, nil))
		return rec.Code
	}
	if c := get("/api/balance/job?cluster=7&id=" + j.ID); c != 200 {
		t.Fatalf("own job: %d", c)
	}
	for _, u := range []string{"/api/balance/job?cluster=8&id=" + j.ID, "/api/balance/job?cluster=7&id=../x", "/api/balance/job?cluster=7&id=" + strings.ToUpper(j.ID), "/api/balance/job?cluster=7"} {
		if c := get(u); c != http.StatusNotFound {
			t.Errorf("%s: %d", u, c)
		}
	}
	for _, b := range []string{``, `{}`, `{"force":"yes"}`, `{"force":true,"x":1}`, strings.Repeat("a", 1000)} {
		if rec := decide(j, b, 7); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: %d", b, rec.Code)
		}
	}
	if rec := decide(j, `{"force":true}`, 8); rec.Code != http.StatusNotFound {
		t.Errorf("other cluster: %d", rec.Code)
	}
	if v := j.view(); v.Phase != coldNeedForce {
		t.Fatalf("invalid answers changed the state: %+v", v)
	}
	decide(j, `{"force":false}`, 7)
	waitPhase(t, j, coldDone)
}

func TestStepModeMustMatchThePlan(t *testing.T) {
	f, q := stepFixture()
	cq, req := coldPlan(t, f, q)
	live := req
	live.Mode = "live"
	if _, err := runBalanceStep(f, "C", 7, cq, live, time.Now()); !errors.Is(err, errStepChanged) {
		t.Fatalf("live confirmation of a cold step: %v", err)
	}
	if _, err := runBalanceStep(f, "C", 7, q, req, time.Now()); !errors.Is(err, errStepChanged) {
		t.Fatalf("cold confirmation of a live step: %v", err)
	}
	if f.acts() != "|" {
		t.Fatalf("something ran: %s", f.acts())
	}
}

func TestHostCPUVMsAndNodeModels(t *testing.T) {
	f, _ := stepFixture()
	f.hostCPU = map[int]bool{1: true}
	f.models = map[string]string{"n1": "AMD EPYC 7713 64-Core Processor", "n2": "Intel(R) Xeon(R) CPU E5-2640 v4 @ 2.40GHz"}
	q, _ := parseBalanceQuery(query("cold", "1"))
	res, err := buildBalance(f, "C", 7, q, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Steps {
		if s.VMID == 1 {
			t.Fatalf("host-CPU VM moved live across CPU vendors: %+v", s)
		}
	}
	found := false
	for _, b := range res.Blocked {
		found = found || (b.VMID == 1 && containsStr(b.Reasons, "cpu_host"))
	}
	if !found {
		t.Fatalf("VM 1 not blocked by cpu_host: %+v", res.Blocked)
	}
	// chosen cold, it may move; the step says cold
	cq, _ := parseBalanceQuery(query("cold", "1", "cold_vms", "1"))
	res, err = buildBalance(f, "C", 7, cq, time.Now())
	if err != nil || len(res.Steps) == 0 || res.Steps[0].VMID != 1 || !res.Steps[0].Cold {
		t.Fatalf("cold: %v %+v", err, res.Steps)
	}
	// two Intel models: the live move is allowed (vendor, not model)
	f.models["n1"] = "Intel(R) Xeon(R) Silver 4114 CPU @ 2.20GHz"
	res, _ = buildBalance(f, "C", 7, q, time.Now())
	if len(res.Steps) == 0 || res.Steps[0].VMID != 1 || res.Steps[0].Cold || !res.Steps[0].SameVendor {
		t.Fatalf("Intel -> Intel live: %+v", res.Steps)
	}
	// a node whose model cannot be read is reported
	f.models["n2"] = ""
	res, _ = buildBalance(f, "C", 7, q, time.Now())
	if !containsStr(res.Meta.Partial, "cpu_vendor") {
		t.Fatalf("partial %v", res.Meta.Partial)
	}
}

func TestVMIDListParams(t *testing.T) {
	q, err := parseBalanceQuery(query("only", "174,12,174", "cold_vms", "174"))
	if err != nil || len(q.P.Only) != 2 || q.P.Only[0] != 12 || len(q.P.ColdVMs) != 1 {
		t.Fatalf("%v %+v", err, q.P)
	}
	for _, bad := range []string{"0", "-1", "a", "1,,2", "1;2", "1234567890", strings.Repeat("1,", 65) + "1"} {
		if _, err := parseBalanceQuery(query("only", bad)); err == nil {
			t.Errorf("only=%q accepted", bad)
		}
		if _, err := parseBalanceQuery(query("cold_vms", bad)); err == nil {
			t.Errorf("cold_vms=%q accepted", bad)
		}
	}
	a, _ := parseBalanceQuery(query("only", "1,2"))
	b, _ := parseBalanceQuery(query("only", "2,1"))
	c, _ := parseBalanceQuery(query("cold_vms", "1,2"))
	if a.key() != b.key() || a.key() == c.key() {
		t.Fatal("cache key ignores or mixes the VM lists")
	}
}

func TestHostCPUType(t *testing.T) {
	for cpu, want := range map[string]bool{
		"host": true, "host,flags=+aes": true, "cputype=host": true, "max": true, "custom-mine": true, "cputype=custom-x,flags=+pcid": true,
		"x86-64-v2-AES": false, "kvm64": false, "EPYC-Rome": false, "": false, "flags=+aes,cputype=host": true,
	} {
		cfg := map[string]interface{}{}
		if cpu != "" {
			cfg["cpu"] = cpu
		}
		if got := hostCPUType(cfg); got != want {
			t.Errorf("%q: %v", cpu, got)
		}
	}
	if hostCPUType(map[string]interface{}{"cpu": 5}) {
		t.Error("non-string cpu")
	}
}

func TestCPUVendor(t *testing.T) {
	for m, want := range map[string]string{
		"AMD EPYC 7713 64-Core Processor": "amd", "Intel(R) Xeon(R) CPU E5-2640 v4 @ 2.40GHz": "intel", "Intel Xeon Test": "intel",
		"": "", "Common KVM processor": "", "AMD Intel mix": "",
	} {
		if got := cpuVendor(m); got != want {
			t.Errorf("%q: %q", m, got)
		}
	}
}
