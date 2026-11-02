package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeCephStatusHealthyAndRecovery(t *testing.T) {
	raw := json.RawMessage(`{"health":{"status":"HEALTH_WARN","checks":{"OSD_DOWN":{"severity":"HEALTH_WARN","muted":false,"summary":{"message":"1 osds down","count":1},"detail":[{"message":"osd.3 is down"}]}}},
	"osdmap":{"num_osds":4,"num_up_osds":3,"num_in_osds":4},
	"pgmap":{"bytes_total":1000,"bytes_used":850,"bytes_avail":150,"num_pgs":10,"pgs_by_state":[{"state_name":"active+clean","count":6},{"state_name":"active+undersized+degraded","count":3},{"state_name":"active+remapped+backfill_wait","count":1}],
	"degraded_objects":100,"degraded_ratio":0.1,"recovering_objects_per_sec":10},
	"quorum_names":["a","b"],"monmap":{"mons":[{"name":"a"},{"name":"b"},{"name":"c"}]},
	"progress_events":{"x":{"message":"Rebalancing","progress":0.5}}}`)
	o, err := normalizeCephStatus(raw, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if o.Health != "HEALTH_WARN" || len(o.Checks) != 1 || o.Checks[0].Detail[0] != "osd.3 is down" {
		t.Fatalf("health/checks: %+v", o)
	}
	if o.Capacity.Percent != 85 || o.OSDs.Up != 3 || o.MonQuorum.In != 2 || o.MonQuorum.Total != 3 {
		t.Fatalf("capacity/osd/mon: %+v", o)
	}
	r := o.Recovery
	if !r.Active || r.BackfillPGs != 1 || r.DegradedPGs != 3 || r.ProgressPct == nil || *r.ProgressPct != 50 || r.ETASeconds == nil || *r.ETASeconds != 10 {
		t.Fatalf("recovery: %+v", r)
	}
}

func TestNormalizeCephStatusIdleAndUnknownHealth(t *testing.T) {
	o, err := normalizeCephStatus(json.RawMessage(`{"health":{"status":"weird"},"progress_events":{},"pgmap":{"read_bytes_sec":-5}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if o.Health != "HEALTH_UNKNOWN" || o.Recovery.Active || o.IO.ReadBytesSec != 0 {
		t.Fatalf("got %+v", o)
	}
	if _, err := normalizeCephStatus(json.RawMessage(`not json`), time.Now()); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeCephOSDsSortsAndDedups(t *testing.T) {
	raw := json.RawMessage(`{"root":{"type":"root","children":[
	{"type":"host","name":"node-10","children":[{"type":"osd","id":"5","name":"osd.5","host":"node-10","status":"up","in":1,"percent_used":50}]},
	{"type":"host","name":"node-2","children":[{"type":"osd","id":"1","name":"osd.1","host":"node-2","status":"down","in":0,"percent_used":10},
	{"type":"osd","id":"1","name":"osd.1","host":"node-2","status":"up"}]}]}}`)
	o := normalizeCephOSDs(raw)
	if len(o) != 2 || o[0].Host != "node-2" || o[0].Status != "down" || o[0].In || o[1].Host != "node-10" {
		t.Fatalf("got %+v", o)
	}
}

func TestNormalizeCephPoolsFlagsAndText(t *testing.T) {
	p := normalizeCephPools(json.RawMessage(`[{"pool_name":"b","percent_used":0.5,"size":3,"min_size":2},{"pool_name":"a","application_metadata":{"rbd":{}}}]`))
	if len(p) != 2 || p[0].Name != "a" || p[1].Percent != 50 || p[0].Apps[0] != "rbd" {
		t.Fatalf("pools %+v", p)
	}
	f := normalizeCephFlags(json.RawMessage(`[{"name":"noout","value":1,"description":"d"},{"name":"noup","value":0}]`))
	if len(f) != 1 || f[0].Name != "noout" {
		t.Fatalf("flags %+v", f)
	}
	if got := clampText("a\x1b[31mb", 10); got == "" {
		t.Fatal("clampText empty")
	}
}

func TestClassifyCephError(t *testing.T) {
	if classifyCephError(errors.New(`proxmox 500 on /cluster/ceph/status: {"message":"binary not installed: /usr/bin/ceph-mon\n"}`)) != cephNotConfigured {
		t.Fatal("not installed")
	}
	if classifyCephError(errors.New("connection refused")) != cephOther {
		t.Fatal("other")
	}
}

func TestNaturalLess(t *testing.T) {
	if !naturalLess("node-2", "node-10") || naturalLess("node-10", "node-2") {
		t.Fatal("natural order")
	}
}

// fakeCeph answers like Proxmox; nodes listed in noCeph fail the per-node calls as a node without Ceph packages does.
type fakeCeph struct {
	statusErr error
	noCeph    map[string]bool
	mu        sync.Mutex // per-node calls run in parallel
	calls     []string
}

func (f *fakeCeph) GetNodes() (json.RawMessage, error) {
	return json.RawMessage(`[{"node":"pve1","status":"online"},{"node":"pve2","status":"online"},{"node":"pve3","status":"offline"},{"node":"bad name","status":"online"}]`), nil
}
func (f *fakeCeph) GetCephStatus() (json.RawMessage, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return json.RawMessage(`{"health":{"status":"HEALTH_OK"},"quorum_names":["pve2","pve3","$(x)"],"monmap":{"mons":[{"name":"pve2"}]}}`), nil
}
func (f *fakeCeph) per(node, what string) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, what+"@"+node)
	f.mu.Unlock()
	if f.noCeph[node] {
		return nil, errors.New(`proxmox 500 on /nodes/x/ceph: {"message":"binary not installed: /usr/bin/ceph-mon"}`)
	}
	switch what {
	case "osd":
		return json.RawMessage(`{"root":{"type":"root","children":[{"type":"osd","id":"0","name":"osd.0","host":"` + node + `","status":"up","in":1}]}}`), nil
	case "pool":
		return json.RawMessage(`[{"pool_name":"p","percent_used":0.1}]`), nil
	}
	return json.RawMessage(`[]`), nil
}
func (f *fakeCeph) GetCephOSD(n string) (json.RawMessage, error)   { return f.per(n, "osd") }
func (f *fakeCeph) GetCephPools(n string) (json.RawMessage, error) { return f.per(n, "pool") }
func (f *fakeCeph) GetCephServices(n, k string) (json.RawMessage, error) {
	return f.per(n, k)
}
func (f *fakeCeph) GetCephFlags() (json.RawMessage, error) { return json.RawMessage(`[]`), nil }

func TestFetchCephFullPrefersMonitorNodeAndFallsBack(t *testing.T) {
	f := &fakeCeph{noCeph: map[string]bool{"pve2": true}}
	full, err := fetchCephFull(f, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// pve2 is the monitor (tried first) but has no Ceph packages: pve1 must serve every per-node call.
	if len(full.OSDs) != 1 || full.OSDs[0].Host != "pve1" || len(full.Pools) != 1 || len(full.Partial) != 0 {
		t.Fatalf("got osds=%+v pools=%+v partial=%v calls=%v", full.OSDs, full.Pools, full.Partial, f.calls)
	}
	if f.calls[0] != "osd@pve2" || f.calls[1] != "osd@pve1" {
		t.Fatalf("order %v", f.calls)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "bad name") || strings.Contains(c, "pve3") || strings.Contains(c, "$(x)") {
			t.Fatalf("invalid or offline node used: %v", f.calls)
		}
	}
}

func TestFetchCephFullAllNodesFailIsPartial(t *testing.T) {
	f := &fakeCeph{noCeph: map[string]bool{"pve1": true, "pve2": true}}
	full, err := fetchCephFull(f, time.Now())
	if err != nil || !full.Available {
		t.Fatalf("err=%v full=%+v", err, full)
	}
	if strings.Join(full.Partial, ",") != "mds,mgr,mon,osd,pools" || full.OSDs == nil || full.Pools == nil {
		t.Fatalf("partial=%v", full.Partial)
	}
}

func TestFetchCephNotConfiguredAndForbidden(t *testing.T) {
	f := &fakeCeph{statusErr: errors.New(`proxmox 500 on /cluster/ceph/status: {"message":"binary not installed: /usr/bin/ceph-mon\n"}`)}
	full, err := fetchCephFull(f, time.Now())
	if err != nil || full.Available || full.Reason != "not_configured" || full.PGs.States == nil || full.Recovery.Events == nil {
		t.Fatalf("err=%v full=%+v", err, full)
	}
	b, _ := json.Marshal(full)
	// lists must be [] (the browser iterates them); progress_pct and eta_seconds are null on purpose
	for _, k := range []string{"checks", "states", "events", "osds", "pools", "mons", "mgrs", "mdss", "flags", "partial", "thresholds"} {
		if strings.Contains(string(b), `"`+k+`":null`) {
			t.Fatalf("%s is null in unavailable answer: %s", k, b)
		}
	}
	f = &fakeCeph{statusErr: errors.New("proxmox 403 on /cluster/ceph/status: permission check failed")}
	if ov, err := fetchCephOverview(f, time.Now()); err != nil || ov.Reason != "forbidden" {
		t.Fatalf("forbidden: %v %+v", err, ov)
	}
	f = &fakeCeph{statusErr: errors.New("dial tcp: connection refused")}
	if _, err := fetchCephOverview(f, time.Now()); err == nil {
		t.Fatal("network errors must stay errors (502), not 'no Ceph'")
	}
}
