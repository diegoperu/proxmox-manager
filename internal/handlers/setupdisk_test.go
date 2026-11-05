package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func setupFast(t *testing.T) {
	t.Helper()
	provFast(t)
	old := setupDiskTiming
	setupDiskTiming.StopWait, setupDiskTiming.StartWait, setupDiskTiming.AgentPoll, setupDiskTiming.AgentWait = time.Second, time.Second, 2*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { setupDiskTiming = old })
}

// a running VM 60 with a disk virtio1 and a CD-ROM ide2
func setupVM(f *fakeProv) {
	f.vms[60] = &fvm{node: "n1", name: "vm60", status: "running", cfg: map[string]string{"virtio0": "ceph:vm-60-disk-0,size=32G", "virtio1": "ceph:vm-60-disk-1,size=20G", "ide2": "ceph:vm-60-cloudinit,media=cdrom", "memory": "1024", "cores": "1"}}
}

func req(disk string) setupDiskReq {
	return setupDiskReq{Disk: disk, MountPoint: "/data", Sticky: true}
}

func (f *fakeProv) vmActions() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, w := range f.writes {
		if strings.HasPrefix(w, "shutdown") || strings.HasPrefix(w, "start") || strings.HasPrefix(w, "stop") {
			out = append(out, w)
		}
	}
	return strings.Join(out, ",")
}

func TestSetupDiskFormatsTheBlankDiskWithoutRestart(t *testing.T) {
	setupFast(t)
	f := newFakeProv()
	setupVM(f)
	res, e := runSetupDisk(f, "n1", 60, req("virtio1"))
	if e != nil || res.Device != "/dev/vdb" || res.MountPoint != "/data" {
		t.Fatalf("%v %+v", e, res)
	}
	if f.vmActions() != "" {
		t.Fatalf("the VM was touched: %s", f.vmActions())
	}
	// the page is told that the disk was picked in the guest, not by slot
	if !strings.Contains(strings.Join(res.Log, "\n"), "scelto nel guest, non dallo slot virtio1") {
		t.Fatalf("log: %v", res.Log)
	}
}

func TestSetupDiskRefusesASlotThatIsNotInTheConfig(t *testing.T) {
	setupFast(t)
	for _, slot := range []string{"virtio7", "scsi1", "ide2", "sata0"} { // ide2 is a CD-ROM; the others do not exist
		f := newFakeProv()
		setupVM(f)
		_, e := runSetupDisk(f, "n1", 60, req(slot))
		if e == nil || e.Code != http.StatusBadRequest || e.Reason != "slot_missing" {
			t.Fatalf("%s: %+v", slot, e)
		}
		if len(f.execs) != 0 || f.vmActions() != "" {
			t.Fatalf("%s: something ran in the guest or on the VM", slot)
		}
	}
	// a stopped VM is refused before anything else
	f := newFakeProv()
	setupVM(f)
	f.vms[60].status = "stopped"
	if _, e := runSetupDisk(f, "n1", 60, req("virtio1")); e == nil || e.Reason != "not_running" || len(f.execs) != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestSetupDiskNeverRestartsTheVMWithoutPermission(t *testing.T) {
	setupFast(t)
	f := newFakeProv()
	setupVM(f)
	f.blank = []string{} // the disk does not show up in the guest
	_, e := runSetupDisk(f, "n1", 60, req("virtio1"))
	if e == nil || e.Code != http.StatusConflict || e.Reason != "restart_needed" {
		t.Fatalf("%+v", e)
	}
	if f.vmActions() != "" {
		t.Fatalf("the VM was stopped or started without permission: %s", f.vmActions())
	}
	if len(e.Log) == 0 {
		t.Fatal("the page gets no log with the refusal")
	}
	// with the permission: stop, start, search again, format
	f.blankAfterStart = []string{"vdb"}
	r := req("virtio1")
	r.AllowRestart = true
	res, e := runSetupDisk(f, "n1", 60, r)
	if e != nil || res.Device != "/dev/vdb" {
		t.Fatalf("%v %+v", e, res)
	}
	if got := f.vmActions(); got != "shutdown 60,start 60" {
		t.Fatalf("actions %q", got)
	}
}

func TestSetupDiskPermissionIsNotEnoughWhenTheDiskStaysInvisible(t *testing.T) {
	setupFast(t)
	f := newFakeProv()
	setupVM(f)
	f.blank = []string{}
	r := req("virtio1")
	r.AllowRestart = true
	_, e := runSetupDisk(f, "n1", 60, r)
	if e == nil || e.Code != http.StatusBadGateway || !strings.Contains(e.Error(), "nemmeno dopo il riavvio") {
		t.Fatalf("%+v", e)
	}
}

func TestSetupDiskSeveralBlankDisksWriteNothing(t *testing.T) {
	setupFast(t)
	f := newFakeProv()
	setupVM(f)
	f.blank = []string{"vdb", "vdc"}
	_, e := runSetupDisk(f, "n1", 60, req("virtio1"))
	if e == nil || e.Code != http.StatusConflict || e.Reason != "ambiguous" {
		t.Fatalf("%+v", e)
	}
	for _, x := range f.execs {
		for _, a := range x {
			if a == "setup" {
				t.Fatal("formatted with several candidates")
			}
		}
	}
}

func TestSetupDiskHandlerValidatesBeforeAnyCall(t *testing.T) {
	h := &Handler{} // no client: anything that reached Proxmox would panic
	for name, body := range map[string]string{
		"bad slot":      `{"disk":"virtio99","mount_point":"/data"}`,
		"no slot":       `{"mount_point":"/data"}`,
		"system dir":    `{"disk":"virtio1","mount_point":"/etc"}`,
		"traversal":     `{"disk":"virtio1","mount_point":"/data/../etc"}`,
		"not json":      `x`,
		"restart typed": `{"disk":"virtio1","mount_point":"/etc","allow_restart":true}`,
	} {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		rec := httptest.NewRecorder()
		rc := routeCtx("node", "n1", "vmid", "60")
		h.SetupVMDisk(rec, r.WithContext(rc(r)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// routeCtx builds a request context with chi URL parameters.
func routeCtx(kv ...string) func(*http.Request) context.Context {
	return func(r *http.Request) context.Context {
		rc := chi.NewRouteContext()
		for i := 0; i+1 < len(kv); i += 2 {
			rc.URLParams.Add(kv[i], kv[i+1])
		}
		return context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	}
}
