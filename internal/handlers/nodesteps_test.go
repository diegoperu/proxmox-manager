package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"proxmox-manager/internal/buildinfo"
	pmgrscripts "proxmox-manager/tools/pmgr-scripts"
)

type nodeTaskFake struct {
	readinessFake
	data     map[string]string
	failures map[string]bool
	calls    map[string]int
	mu       sync.Mutex
}

func taskFake() *nodeTaskFake {
	return &nodeTaskFake{data: map[string]string{
		"status": `[{"type":"node","name":"n1","ip":"192.0.2.1","online":0},{"type":"node","name":"n2","ip":"192.0.2.2","online":1},{"type":"node","name":"n3","ip":"192.0.2.3","online":1}]`,
		"guests": `[]`, "ha": `[]`, "rules": `[]`, "groups": `[]`, "replication": `[]`, "storage": `[]`, "mappings": `[]`,
		"ceph": `{"health":{"status":"HEALTH_OK"}}`, "osds": `{"root":{"type":"root","children":[]}}`, "mon": `[]`, "mgr": `[]`, "mds": `[]`,
		"apt": `[]`, "pci": `[]`, "version": `{"pveversion":"pve-manager/9.2.1/abc","current-kernel":{"machine":"x86_64"}}`,
	}, failures: map[string]bool{}, calls: map[string]int{}}
}
func (f *nodeTaskFake) get(key string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[key]++
	if f.failures[key] {
		return nil, errors.New("upstream SECRET error")
	}
	value, ok := f.data[key]
	if !ok {
		return nil, errors.New("missing fake response")
	}
	return json.RawMessage(value), nil
}
func (f *nodeTaskFake) GetClusterStatus() (json.RawMessage, error)          { return f.get("status") }
func (f *nodeTaskFake) GetClusterResources(string) (json.RawMessage, error) { return f.get("guests") }
func (f *nodeTaskFake) GetHAResources() (json.RawMessage, error)            { return f.get("ha") }
func (f *nodeTaskFake) GetHARules() (json.RawMessage, error)                { return f.get("rules") }
func (f *nodeTaskFake) GetHAGroups() (json.RawMessage, error)               { return f.get("groups") }
func (f *nodeTaskFake) GetReplication() (json.RawMessage, error)            { return f.get("replication") }
func (f *nodeTaskFake) GetStorage() (json.RawMessage, error)                { return f.get("storage") }
func (f *nodeTaskFake) GetPCIMappings() (json.RawMessage, error)            { return f.get("mappings") }
func (f *nodeTaskFake) GetCephStatus() (json.RawMessage, error)             { return f.get("ceph") }
func (f *nodeTaskFake) GetCephOSD(string) (json.RawMessage, error)          { return f.get("osds") }
func (f *nodeTaskFake) GetCephServices(_ string, kind string) (json.RawMessage, error) {
	return f.get(kind)
}
func (f *nodeTaskFake) GetNodeAPTVersions(string) (json.RawMessage, error) { return f.get("apt") }
func (f *nodeTaskFake) GetNodePCI(string) (json.RawMessage, error)         { return f.get("pci") }
func (f *nodeTaskFake) GetNodeStatus(n string) (json.RawMessage, error) {
	if _, ok := f.data["version-"+n]; ok {
		return f.get("version-" + n)
	}
	return f.get("version")
}
func (f *nodeTaskFake) GetNodes() (json.RawMessage, error) {
	return json.RawMessage(`[{"node":"n1","status":"offline"},{"node":"n2","status":"online"},{"node":"n3","status":"online"}]`), nil
}

func removalReport(t *testing.T, f *nodeTaskFake) nodeRemovalResult {
	t.Helper()
	res, err := BuildNodeRemoval(f, "Cluster", "n1")
	if err != nil {
		t.Fatal(err)
	}
	return res
}
func TestNodeRemovalOrderAndUnlock(t *testing.T) {
	f := taskFake()
	res := removalReport(t, f)
	ids := []string{"guests", "ha", "replication", "storage", "pci_mapping", "ceph_osd", "ceph_services", "power_off", "quorum", "delnode", "cleanup"}
	for i, s := range res.Steps {
		if s.ID != ids[i] {
			t.Fatalf("step %d: %+v", i, s)
		}
		want := "ok"
		if i == 9 {
			want = "todo"
		}
		if i == 10 {
			want = "blocked"
		}
		if s.Status != want {
			t.Fatalf("step %s status %s want %s", s.ID, s.Status, want)
		}
	}
	if !reflect.DeepEqual(res.Steps[9].Commands, []string{"pvecm delnode n1"}) {
		t.Fatal(res.Steps[9])
	}
	f.data["guests"] = `[{"vmid":100,"name":"template <script>","type":"qemu","status":"stopped","template":1,"node":"n1"},{"vmid":101,"name":"container","type":"lxc","status":"stopped","node":"n1"}]`
	res = removalReport(t, f)
	if res.Steps[0].Status != "todo" || len(res.Steps[0].Items) != 2 || len(res.Steps[0].Commands) != 0 || res.Steps[0].Link != "#/balance" {
		t.Fatal(res.Steps[0])
	}
	for _, s := range res.Steps[1:] {
		if s.Status != "blocked" {
			t.Fatal(s)
		}
	}
	// A new computation must reread data, not reuse an earlier safe report.
	f.data["guests"] = `[]`
	res = removalReport(t, f)
	if res.Steps[0].Status != "ok" || f.calls["guests"] != 3 {
		t.Fatal("cached removal check")
	}
}
func TestNodeRemovalUnreadableNeverOK(t *testing.T) {
	for key, index := range map[string]int{"guests": 0, "ha": 1, "rules": 1, "groups": 1, "replication": 2, "storage": 3, "mappings": 4, "ceph": 5, "osds": 5, "mon": 6, "mgr": 6, "mds": 6} {
		for _, value := range []string{"error", "null", "{}", "[null]", "[{}]"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				f := taskFake()
				if value == "error" {
					f.failures[key] = true
				} else {
					f.data[key] = value
				}
				res := removalReport(t, f)
				s := res.Steps[index]
				if s.CheckStatus != "unknown" || len(s.Commands) != 0 || strings.Contains(s.Detail, "SECRET") {
					t.Fatalf("unsafe unreadable step: %+v", s)
				}
			})
		}
	}
}
func TestNodeRemovalHAStorageReplicationMappings(t *testing.T) {
	f := taskFake()
	f.data["guests"] = `[{"vmid":100,"name":"vm","type":"qemu","status":"running","node":"n1"}]`
	f.data["ha"] = `[{"sid":"vm:100"}]`
	f.data["rules"] = `[{"rule":"r1","type":"node-affinity","nodes":"n1:2,n2"}]`
	f.data["groups"] = `[{"group":"g1","nodes":"n3,n1:1"}]`
	f.data["replication"] = `[{"id":"100-0","target":"n3"},{"id":"100-1","target":"n1"}]`
	f.data["storage"] = `[{"storage":"s1","nodes":"n1,n2,n3"},{"storage":"s2","nodes":"n1"},{"storage":"s3"}]`
	f.data["mappings"] = `[{"id":"gpu1","map":["node=n1,path=0000:01:00.0"]}]`
	res := removalReport(t, f)
	if len(res.Steps[1].Items) != 3 || len(res.Steps[1].Commands) != 0 {
		t.Fatal(res.Steps[1])
	}
	if !reflect.DeepEqual(res.Steps[2].Commands, []string{"pvesr delete 100-0", "pvesr delete 100-1"}) {
		t.Fatal(res.Steps[2])
	}
	if !reflect.DeepEqual(res.Steps[3].Commands, []string{"pvesm set s1 --nodes n2,n3"}) || !strings.Contains(strings.Join(res.Steps[3].Items, " "), "togli lo storage o assegnalo ad altri nodi") {
		t.Fatal(res.Steps[3])
	}
	if !reflect.DeepEqual(res.Steps[4].Items, []string{"gpu1"}) || len(res.Steps[4].Commands) != 0 {
		t.Fatal(res.Steps[4])
	}
	for _, bad := range []string{"100-0;id", "100-0 x", "100-'0", "1-1234", "1234567890-1"} {
		f.data["replication"] = fmt.Sprintf(`[{"id":%q,"target":"n1"}]`, bad)
		if s := removalReport(t, f).Steps[2]; len(s.Commands) != 0 || s.CheckStatus != "unknown" {
			t.Fatal(s)
		}
	}
	for _, bad := range []string{"name x", "name'", "name;id", "../n1"} {
		for _, value := range []string{fmt.Sprintf(`[{"storage":%q,"nodes":"n1,n2"}]`, bad), fmt.Sprintf(`[{"storage":"safe","nodes":%q}]`, "n1,"+bad)} {
			f.data["storage"] = value
			if s := removalReport(t, f).Steps[3]; len(s.Commands) != 0 || s.CheckStatus != "unknown" {
				t.Fatal(s)
			}
		}
		if _, err := BuildNodeRemoval(f, "C", bad); err == nil {
			t.Fatal("accepted hostile node", bad)
		}
		if len(removalCleanup(bad).Commands) != 0 {
			t.Fatal("hostile cleanup")
		}
	}
}
func TestNodeRemovalCephSafety(t *testing.T) {
	f := taskFake()
	f.data["osds"] = `{"root":{"type":"root","children":[{"type":"host","name":"n1","children":[{"type":"osd","id":"1","in":1,"status":"up"},{"type":"osd","id":2,"host":"n1","in":0,"status":"down"}]}]}}`
	f.data["mon"] = `[{"name":"n1"},{"name":"n2"},{"name":"n3"}]`
	f.data["mgr"] = `[{"name":"n1"}]`
	f.data["mds"] = `[{"name":"n1"}]`
	res := removalReport(t, f)
	// one OSD at a time: the first one (lowest id) is still in, so only "out" is offered, nothing is destroyed
	if !reflect.DeepEqual(res.Steps[5].Commands, []string{"ceph osd out 1"}) || !strings.Contains(res.Steps[5].Detail, "Un OSD alla volta") ||
		!strings.Contains(res.Steps[5].Detail, "altri 1 OSD") {
		t.Fatal(res.Steps[5])
	}
	// once out, destroy only behind Ceph's own safe-to-destroy check, chained so it cannot run alone
	f.data["osds"] = `{"root":{"type":"root","children":[{"type":"host","name":"n1","children":[{"type":"osd","id":2,"in":0,"status":"down"}]}]}}`
	if c := removalReport(t, f).Steps[5].Commands; !reflect.DeepEqual(c, []string{"ceph osd safe-to-destroy osd.2 && systemctl stop ceph-osd@2 && pveceph osd destroy 2 --cleanup"}) {
		t.Fatal(c)
	}
	for _, c := range removalReport(t, f).Steps[5].Commands {
		if strings.Contains(c, "destroy") && !strings.HasPrefix(c, "ceph osd safe-to-destroy ") {
			t.Fatalf("destroy without safe-to-destroy: %q", c)
		}
	}
	f.data["osds"] = `{"root":{"type":"root","children":[{"type":"host","name":"n1","children":[{"type":"osd","id":"1","in":1,"status":"up"},{"type":"osd","id":2,"host":"n1","in":0,"status":"down"}]}]}}`
	svc := res.Steps[6]
	if !reflect.DeepEqual(svc.Commands, []string{"pveceph mds destroy n1"}) || !strings.Contains(svc.Detail, "meno di 3 monitor") {
		t.Fatal(svc)
	}
	f.data["mon"] = `[{"name":"n1"},{"name":"n2"},{"name":"n3"},{"name":"n4"}]`
	f.data["mgr"] = `[{"name":"n1"},{"name":"n2"}]`
	svc = removalReport(t, f).Steps[6]
	if !reflect.DeepEqual(svc.Commands, []string{"pveceph mon destroy n1", "pveceph mgr destroy n1", "pveceph mds destroy n1"}) {
		t.Fatal(svc)
	}
	f.data["ceph"] = `{"health":{"status":"HEALTH_WARN"}}`
	f.data["osds"] = `{"root":{"type":"root","children":[]}}`
	// a permanent HEALTH_WARN (e.g. AUTH warnings) must not block a node that has no OSD
	osd := removalReport(t, f).Steps[5]
	if osd.CheckStatus != "ok" || len(osd.Commands) != 0 {
		t.Fatal(osd)
	}
	// the real Proxmox shape: a root without "type", and a host with no OSD has no "children"
	f.data["osds"] = `{"root":{"leaf":0,"children":[{"type":"host","name":"n2"},{"type":"host","name":"n1","children":[{"type":"osd","id":7,"in":1,"status":"up"}]}]}}`
	if s := removalReport(t, f).Steps[5]; s.CheckStatus != "todo" || !reflect.DeepEqual(s.Commands, []string{"ceph osd out 7"}) {
		t.Fatal(s)
	}
	for _, bad := range []string{`{"root":{}}`, `{"root":{"type":"root"}}`, `{"root":{"type":"root","children":[{"type":"osd","id":"1;id","host":"n1","in":1,"status":"up"}]}}`, `{"root":{"type":"root","children":[{"type":"osd","id":1,"host":"n1","status":"up"}]}}`} {
		f.data["osds"] = bad
		if s := removalReport(t, f).Steps[5]; s.CheckStatus != "unknown" || len(s.Commands) != 0 {
			t.Fatal(s)
		}
	}
}

type noCephFake struct{ *nodeTaskFake }

func (f noCephFake) GetCephStatus() (json.RawMessage, error) {
	return nil, errors.New("proxmox 500 on /cluster/ceph/status: ceph is not initialized")
}
func TestNodeRemovalNoCephAndQuorum(t *testing.T) {
	f := taskFake()
	r, err := BuildNodeRemoval(noCephFake{f}, "C", "n1")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Steps[5:7] {
		if s.Status != "ok" || s.Detail != "Ceph non configurato" {
			t.Fatal(s)
		}
	}
	f.data["status"] = `[{"type":"node","name":"n1","online":1},{"type":"node","name":"n2","online":1},{"type":"node","name":"n3","online":0}]`
	r = removalReport(t, f)
	if r.Steps[7].Status != "todo" || r.Steps[8].Status != "blocked" || r.Steps[8].CheckStatus != "todo" || !strings.Contains(r.Steps[8].Detail, "1 nodi online rimasti su 2; quorum richiesto: 2") {
		t.Fatal(r.Steps[7:9])
	}
}
func TestNodeRemovalHTTPMemberAndBusy(t *testing.T) {
	h := New()
	f := taskFake()
	for _, tc := range []struct {
		node string
		code int
	}{{"missing", 404}, {"n1", 200}, {"n1;id", 400}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/node-removal?node="+url.QueryEscape(tc.node), nil)
		h.serveNodeRemoval(w, r, 314159, "C", f)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.node, w.Code, w.Body)
		}
	}
	lock := nodeStepLock(314159)
	lock.Lock()
	defer lock.Unlock()
	w := httptest.NewRecorder()
	h.serveNodeRemoval(w, httptest.NewRequest("GET", "/node-removal?node=n1", nil), 314159, "C", f)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, w.Body)
	}
}
func TestNodeAddCephAndVerifiedPrepare(t *testing.T) {
	for version, release := range map[string]string{"18.2.0": "reef", "19.2.1": "squid", "20.0.0": "tentacle", "1:19.2.1-pve1": "squid", "": "none"} {
		raw := `[]`
		if version != "" {
			raw = fmt.Sprintf(`[{"Package":"ceph-common","Version":%q}]`, version)
		}
		got, ok := cephReleaseFromVersions(json.RawMessage(raw), nil)
		if !ok || got != release {
			t.Fatalf("%s: %s %v", version, got, ok)
		}
	}
	for _, raw := range []string{`null`, `{}`, `[{}]`, `[{"Package":"ceph","Version":"17.2.1"}]`, `[{"Package":"ceph","Version":"19.2.1"},{"Package":"ceph-common","Version":"20.0.1"}]`} {
		if release, ok := cephReleaseFromVersions(json.RawMessage(raw), nil); ok || release != "unknown" {
			t.Fatal(raw, release)
		}
	}
	data, err := pmgrscripts.FS.ReadFile("bin/pmgr-node-prepare.sh")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != nodePrepareSHA256 {
		t.Fatal("wrong embedded hash")
	}
	clean := buildinfo.BuildInfo{Known: true, Commit: strings.Repeat("a", 40)}
	commands := nodePrepareCommands("squid", clean)
	if len(commands) != 4 || !strings.Contains(commands[0], "/"+clean.Commit+"/") {
		t.Fatal(commands)
	}
	for i, c := range commands[1:] {
		if !strings.Contains(c, "echo '"+nodePrepareSHA256+"  "+nodePreparePath+"' | sha256sum --check --strict - && bash ") || !strings.Contains(c, " --ceph squid") {
			t.Fatal(c)
		}
		if i == 0 && strings.Contains(c, "--apply") {
			t.Fatal("first check writes")
		}
	}
	if !strings.HasSuffix(commands[2], "--apply") || !strings.HasSuffix(commands[3], "--apply --upgrade") {
		t.Fatal(commands)
	}
	for _, build := range []buildinfo.BuildInfo{{}, {Known: true, Dirty: true, Commit: clean.Commit}, {Known: true, Commit: "a;id"}} {
		if len(nodePrepareCommands("squid", build)) != 0 {
			t.Fatal(build)
		}
	}
	if len(nodePrepareCommands("squid;id", clean)) != 0 {
		t.Fatal("hostile release")
	}
}
func TestNodeAddFactsJoinGPUAndPending(t *testing.T) {
	f := taskFake()
	f.data["apt"] = `[{"Package":"ceph","Version":"19.2.1"}]`
	f.data["pci"] = `[{"id":"0000:01:00.0","class":"030200","vendor":"10de","device_name":"NVIDIA A100"}]`
	f.data["mappings"] = `[{"id":"gpu","map":["node=n2,path=0000:01:00.0"]}]`
	res, err := BuildNodeAdd(f, "C", "", buildinfo.BuildInfo{Known: true, Commit: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts.Version != "9.2" || !res.Facts.VersionsMatch || res.Facts.CephRelease != "squid" {
		t.Fatal(res.Facts)
	}
	if res.Facts.Arch != "amd64" || res.Facts.Versions[0].Arch != "amd64" {
		t.Fatal(res.Facts)
	}
	if !reflect.DeepEqual(res.Steps[2].Commands, []string{"pvecm add 192.0.2.2", "pvecm add 192.0.2.3"}) {
		t.Fatal(res.Steps[2])
	}
	if len(res.NewNodes) != 3 || len(res.Steps) != 6 || !strings.Contains(strings.Join(res.Steps[5].Items, " "), "n3: 1 schede Nvidia senza mapping") {
		t.Fatal(res)
	}
	f.data["version-n3"] = `{"pveversion":"8.4.1","current-kernel":{"machine":"aarch64"}}`
	f.data["status"] = `[{"type":"node","name":"n2","ip":"192.0.2.2;id","online":1},{"type":"node","name":"n3","ip":"2001:db8::3","online":1}]`
	res, err = BuildNodeAdd(f, "C", "", buildinfo.BuildInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts.VersionsMatch || len(res.Steps[1].Commands) != 0 || !reflect.DeepEqual(res.Steps[2].Commands, []string{"pvecm add 2001:db8::3"}) {
		t.Fatal(res)
	}
	if res.Facts.Arch != "mixed" || !strings.Contains(strings.Join(res.Facts.Issues, " "), "n3 (ARM64)") {
		t.Fatal(res.Facts)
	}
	f.failures["apt"] = true
	res, err = BuildNodeAdd(f, "C", "", buildinfo.BuildInfo{Known: true, Commit: strings.Repeat("a", 40)})
	if err != nil || res.Facts.CephRelease != "unknown" || len(res.Steps[1].Commands) != 0 || len(res.Steps[3].Commands) != 0 {
		t.Fatal(res, err)
	}
	f.failures["apt"] = false
	f.data["apt"] = `[]`
	res, err = BuildNodeAdd(f, "C", "", buildinfo.BuildInfo{})
	if err != nil || len(res.Steps) != 5 {
		t.Fatal(res, err)
	}
}

func TestNodeRemovalForgetAddressAfterDelnode(t *testing.T) {
	f := taskFake()
	addr, err := syncForgetAddress(f, "n1", "192.0.2.1")
	if err != nil || addr != "192.0.2.1" {
		t.Fatal(addr, err)
	}
	if _, err := syncForgetAddress(f, "n1", "192.0.2.9"); !errors.Is(err, errSyncForgetChanged) {
		t.Fatal(err)
	}
	f.data["status"] = `[{"type":"node","name":"n2","ip":"192.0.2.2","online":1}]`
	addr, err = syncForgetAddress(f, "n1", "192.0.2.1")
	if err != nil || addr != "192.0.2.1" {
		t.Fatal(addr, err)
	}
	for _, address := range []string{"192.0.2.1;id", "https://192.0.2.1", "", "host.example", "0.0.0.0"} {
		if _, err := syncForgetAddress(f, "n1", address); err == nil {
			t.Fatal("accepted address", address)
		}
	}
	for _, raw := range []string{"null", "{}", "[]broken", `[{"type":"node","name":"n2"}]`} {
		f.data["status"] = raw
		if _, err := syncForgetAddress(f, "n1", "192.0.2.1"); err == nil {
			t.Fatal("unreadable membership allowed pin fallback", raw)
		}
	}
	f.failures["status"] = true
	if _, err := syncForgetAddress(f, "n1", "192.0.2.1"); err == nil {
		t.Fatal("upstream failure allowed pin fallback")
	}
	if len(f.calls) != 1 {
		t.Fatal("pin fallback made unexpected API calls", f.calls)
	}
}

func TestNodeAddRejectsHostileJoinAddresses(t *testing.T) {
	for _, address := range []string{"192.0.2.1;id", "192.0.2.1 x", "2001:db8::1%eth0;id", "2001:db8::1%$(id)", "2001:db8::1%eth0", "fe80::1", "::", "224.0.0.1"} {
		t.Run(address, func(t *testing.T) {
			f := taskFake()
			f.data["status"] = fmt.Sprintf(`[{"type":"node","name":"n2","ip":%q,"online":1}]`, address)
			res, err := BuildNodeAdd(f, "C", "", buildinfo.BuildInfo{})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Steps[2].Commands) != 0 || res.Steps[2].Status != "unknown" {
				t.Fatal("unsafe join address", address, res.Steps[2])
			}
		})
	}
}

func TestNodeRemovalOSDIDValidation(t *testing.T) {
	for _, value := range []any{float64(0), float64(999999999), "123", "00001"} {
		if _, ok := removalOSDID(value); !ok {
			t.Fatal("valid OSD id rejected", value)
		}
	}
	for _, value := range []any{nil, float64(-1), 1.2, float64(1000000000), "1;id", "1 2", "1'", "", "0000000000"} {
		if _, ok := removalOSDID(value); ok {
			t.Fatal("invalid OSD id accepted", value)
		}
	}
}
