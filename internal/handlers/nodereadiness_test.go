package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type readinessFake struct {
	missingPrivilege, monitor, noSnippets, badContent, missingFile, unreadable, offline, noDisk, noBridge bool
	drift                                                                                                 int
	versions                                                                                              bool
}

func rawReadiness(s string) (json.RawMessage, error) { return json.RawMessage(s), nil }
func (f *readinessFake) GetNodes() (json.RawMessage, error) {
	status := "online"
	if f.offline {
		status = "offline"
	}
	return rawReadiness(fmt.Sprintf(`[{"node":"node10","status":%q},{"node":"node2","status":"online"}]`, status))
}
func (f *readinessFake) GetNodeStatus(n string) (json.RawMessage, error) {
	v := "9.2.1"
	if f.versions && n == "node10" {
		v = "8.4.0"
	}
	return rawReadiness(fmt.Sprintf(`{"pveversion":"pve-manager/%s/hash","current-kernel":{"machine":"x86_64"}}`, v))
}
func (f *readinessFake) GetNodeTime(n string) (json.RawMessage, error) {
	v := 100
	if n == "node10" {
		v += f.drift
	}
	return rawReadiness(fmt.Sprintf(`{"time":%d}`, v))
}
func (f *readinessFake) GetNodeStorage(n string) (json.RawMessage, error) {
	content := "images,snippets"
	if f.noSnippets {
		content = "images,iso"
	}
	if f.badContent {
		content = "images,$bad"
	}
	disk := `,{"storage":"disk","content":"images","active":1,"enabled":1}`
	if f.noDisk && n == "node10" {
		disk = ""
	}
	return rawReadiness(fmt.Sprintf(`[{"storage":"local","content":%q,"active":1,"enabled":1}%s]`, content, disk))
}
func (f *readinessFake) GetNodeNetworks(n string) (json.RawMessage, error) {
	if f.noBridge && n == "node10" {
		return rawReadiness(`[]`)
	}
	return rawReadiness(`[{"iface":"vmbr0","type":"bridge"}]`)
}
func (f *readinessFake) GetStorageContent(n, s, k string) (json.RawMessage, error) {
	if f.unreadable {
		return nil, errors.New("denied")
	}
	if f.missingFile && n == "node10" {
		return rawReadiness(`[]`)
	}
	return rawReadiness(`[{"volid":"local:snippets/user.yml"}]`)
}
func (f *readinessFake) GetClusterResources(string) (json.RawMessage, error) {
	return rawReadiness(`[{"type":"qemu","template":1,"node":"node2","vmid":100}]`)
}
func (f *readinessFake) GetVMConfig(string, int) (json.RawMessage, error) {
	return rawReadiness(`{"scsi0":"disk:vm-100-disk-0,size=10G","cicustom":"user=local:snippets/user.yml","net0":"virtio=aa,bridge=vmbr0"}`)
}
func (f *readinessFake) GetPermissions(path string) (json.RawMessage, error) {
	p := map[string]int{}
	for _, v := range readinessPrivileges {
		p[v] = 1
	}
	if f.missingPrivilege {
		delete(p, "Datastore.AllocateSpace")
	}
	if f.monitor {
		delete(p, "VM.GuestAgent.Audit")
		delete(p, "VM.GuestAgent.Unrestricted")
		p["VM.Monitor"] = 1
	}
	data, _ := json.Marshal(map[string]map[string]int{path: p})
	return data, nil
}
func checkReadiness(t *testing.T, checks []readinessCheck, id, status string) readinessCheck {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			if c.Status != status {
				t.Fatalf("%s: got %s want %s: %s", id, c.Status, status, c.Detail)
			}
			return c
		}
	}
	t.Fatalf("missing %s", id)
	return readinessCheck{}
}
func TestReadiness(t *testing.T) {
	f := &readinessFake{monitor: true}
	result, err := BuildReadiness(f, 42, "test", "user@pve!token=supersecret", time.Unix(123, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.Nodes[0].Node != "node2" || result.Nodes[1].Node != "node10" {
		t.Fatal(result.Nodes)
	}
	checkReadiness(t, result.ClusterChecks, "permissions", "ok")
	for _, n := range result.Nodes {
		for _, c := range n.Checks {
			if c.Status != "ok" {
				t.Fatalf("%s: %+v", n.Node, c)
			}
		}
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), "supersecret") {
		t.Fatal("secret disclosed")
	}
	cases := []struct {
		name       string
		fake       readinessFake
		cluster    bool
		id, status string
	}{
		{"permissions", readinessFake{missingPrivilege: true}, true, "permissions", "fail"}, {"contents", readinessFake{noSnippets: true}, false, "snippets_storage", "fail"}, {"missing file", readinessFake{missingFile: true}, false, "snippet_files", "fail"}, {"unreadable", readinessFake{unreadable: true}, false, "snippet_files", "unknown"}, {"offline", readinessFake{offline: true}, false, "online", "fail"}, {"disk", readinessFake{noDisk: true}, false, "template_storage", "warn"}, {"bridge", readinessFake{noBridge: true}, false, "bridges", "fail"}, {"clock warning", readinessFake{drift: 6}, true, "clock", "warn"}, {"clock failure", readinessFake{drift: 62}, true, "clock", "fail"}, {"versions", readinessFake{versions: true}, true, "versions", "warn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := BuildReadiness(&tc.fake, 43, "test", "user@pve!token=secret", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			checks := r.Nodes[1].Checks
			if tc.cluster {
				checks = r.ClusterChecks
			}
			c := checkReadiness(t, checks, tc.id, tc.status)
			if tc.name == "permissions" && !strings.Contains(c.Detail, "nasconde snippet e ISO") {
				t.Fatal(c.Detail)
			}
			if tc.name == "contents" && (len(c.Commands) != 1 || c.Commands[0] != "pvesm set local --content images,iso,snippets") {
				t.Fatal(c.Commands)
			}
		})
	}
	f = &readinessFake{badContent: true}
	r, _ := BuildReadiness(f, 44, "test", "bad=secret", time.Now())
	c := checkReadiness(t, r.Nodes[0].Checks, "snippets_storage", "fail")
	if len(c.Commands) != 0 {
		t.Fatal(c.Commands)
	}
}

func TestReadinessCacheAndLock(t *testing.T) {
	entry := &readinessEntry{}
	f := &readinessFake{}
	first, err := cachedReadiness(entry, f, 0, "test", "u@pve!t=secret", "id", false)
	if err != nil {
		t.Fatal(err)
	}
	f.offline = true
	cached, err := cachedReadiness(entry, f, 0, "test", "u@pve!t=secret", "id", false)
	if err != nil || !cached.Nodes[1].Online || first.GeneratedAt != cached.GeneratedAt {
		t.Fatalf("cache: %+v %v", cached, err)
	}
	fresh, err := cachedReadiness(entry, f, 0, "test", "u@pve!t=secret", "id", true)
	if err != nil || fresh.Nodes[1].Online {
		t.Fatalf("fresh: %+v %v", fresh, err)
	}
	entry.Lock()
	defer entry.Unlock()
	_, err = cachedReadiness(entry, f, 0, "test", "u@pve!t=secret", "id", true)
	if !errors.Is(err, errReadinessBusy) {
		t.Fatalf("lock: %v", err)
	}
}

func TestReadinessRejectsInvalidClusterIndex(t *testing.T) {
	reportSettingsTestRouter(t)
	r := chi.NewRouter()
	r.Get("/api/clusters/{idx}/node-readiness", (&Handler{}).GetNodeReadiness)
	for _, path := range []string{"/api/clusters/-1/node-readiness", "/api/clusters/no/node-readiness", "/api/clusters/99999/node-readiness"} {
		w := reportRequest(r, "GET", path, "")
		if w.Code != 400 && w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}
