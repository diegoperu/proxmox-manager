package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/nodessh"
)

type fakeCleanupAPI struct {
	guests              string
	listErr             error
	configs             map[int]string
	failed              map[int]bool
	mu                  sync.Mutex
	reads, active, peak int
}

func (f *fakeCleanupAPI) GetClusterStatus() (json.RawMessage, error) {
	return json.RawMessage(testClusterStatus), nil
}

func (f *fakeCleanupAPI) GetClusterResources(kind string) (json.RawMessage, error) {
	if kind != "vm" {
		return nil, errors.New("unexpected resource type")
	}
	return json.RawMessage(f.guests), f.listErr
}

// Containers share the same configs map: hookscripts are the only snippet references they can have.
func (f *fakeCleanupAPI) GetContainerConfig(node string, id int) (json.RawMessage, error) {
	return f.GetVMConfig(node, id)
}

func (f *fakeCleanupAPI) GetVMConfig(_ string, id int) (json.RawMessage, error) {
	f.mu.Lock()
	f.reads++
	f.active++
	f.peak = max(f.peak, f.active)
	f.mu.Unlock()
	time.Sleep(time.Microsecond)
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	if f.failed[id] {
		return nil, errors.New("secret API error")
	}
	return json.RawMessage(f.configs[id]), nil
}

func TestNodeCleanupClassification(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			c := &fakeCleanupAPI{guests: `[{"type":"qemu","vmid":100,"name":"vm-one","node":"n1"},{"type":"qemu","vmid":101,"name":"template-one","node":"n2","template":1},{"type":"lxc","vmid":102,"node":"n1"}]`,
				configs: map[int]string{100: `{"cicustom":"user=other-store:snippets/vm.yaml,vendor=local:snippets/vm.yaml"}`, 101: `{"cicustom":"meta=local:snippets/template.yaml,network=local:snippets/net.yaml"}`, 102: `{"hostname":"ct"}`}, failed: map[int]bool{101: broken}}
			refs := readCleanupReferences(context.Background(), c)
			if refs.Known == broken || refs.GuestsRead != 3-btoi(broken) || refs.ConfigsFailed != btoi(broken) || c.reads != 3 {
				t.Fatalf("references %+v reads %d", refs, c.reads)
			}
			n := cleanupNodePlan(syncNodePlan{Node: "n1", State: "ok", Ready: true, extra: []string{"snippets/vm.yaml", "snippets/template.yaml", "snippets/free.yaml", "pmgr-scripts/old.sh"}}, refs, "20261125-123456")
			if broken {
				if n.Unknown != 3 || n.Removable != 1 || strings.Contains(n.Command, "snippets/") {
					t.Fatalf("unsafe incomplete refs: %+v", n)
				}
			} else {
				if n.InUse != 2 || n.Removable != 2 || n.Files[0].UsedByCount != 1 || !reflect.DeepEqual(n.Files[0].UsedBy, []string{"100 vm-one"}) || !reflect.DeepEqual(n.Files[1].UsedBy, []string{"101 template-one"}) {
					t.Fatalf("classification %+v", n)
				}
				if strings.Contains(n.Command, "vm.yaml") || strings.Contains(n.Command, "template.yaml") {
					t.Fatal(n.Command)
				}
			}
			if !strings.Contains(n.Command, "'/var/lib/vz/pmgr-scripts/old.sh'") {
				t.Fatal(n.Command)
			}
		})
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestNodeCleanupCommandAndValidation(t *testing.T) {
	stamp := "20261125-123456"
	refs := cleanupReferences{Known: true}
	n := cleanupNodePlan(syncNodePlan{Node: "n1", State: "ok", Ready: true, extra: []string{"snippets/old.yaml", "pmgr-scripts/bin/a@b+-_1.sh", "snippets/../escape", "snippets/a;id", "snippets/a'evil", "/root/no", "snippets/" + strings.Repeat("a", 241)}}, refs, stamp)
	want := "umask 077 && mkdir -p /root/pmgr-backup && tar -C /var/lib/vz -czf /root/pmgr-backup/cleanup-20261125-123456.tar.gz -- 'snippets/old.yaml' 'pmgr-scripts/bin/a@b+-_1.sh' && rm -f -- '/var/lib/vz/snippets/old.yaml' '/var/lib/vz/pmgr-scripts/bin/a@b+-_1.sh'"
	if n.Command != want || n.Undo != "tar -C /var/lib/vz -xzf /root/pmgr-backup/cleanup-20261125-123456.tar.gz" || len(n.Files) != 2 || !strings.Contains(n.Message, "5 percorsi non validi") {
		t.Fatalf("%+v", n)
	}
	if !validCleanupPath("snippets/" + strings.Repeat("a", 240)) {
		t.Fatal("boundary path refused")
	}
	for _, state := range []string{"offline", "not_pinned", "error"} {
		n = cleanupNodePlan(syncNodePlan{Node: "n1", State: state, Message: "Not ready", extra: []string{"snippets/old.yaml"}}, refs, stamp)
		if len(n.Files) != 0 || n.Command != "" || n.Undo != "" || n.Message != "Not ready" {
			t.Fatalf("%+v", n)
		}
	}
}

func TestNodeCleanupFullExtrasAndCap(t *testing.T) {
	paths := []string{}
	for i := range 205 {
		paths = append(paths, fmt.Sprintf("pmgr-scripts/old-%03d.sh", i))
	}
	paths = append(paths, "snippets/evil';id", "snippets/../../root/file")
	ssh := &fakeSSH{pinned: map[string]string{"192.0.2.1": fpA}, lists: map[string]nodessh.Listing{"192.0.2.1": listingOf(paths...)}}
	plan, err := buildSyncPlan(context.Background(), fakeStatus{testClusterStatus}, ssh, testBundleBuilder(t), config.ClusterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Nodes[0].Extra) != 100 {
		t.Fatal("sync preview limit changed")
	}
	n := cleanupNodePlan(plan.Nodes[0], cleanupReferences{Known: true}, "20261125-123456")
	if len(n.Files) != 205 || n.Removable != 205 || strings.Count(n.Command, "'/var/lib/vz/pmgr-scripts/") != 200 || strings.Contains(n.Command, "old-200.sh") || !strings.Contains(n.Message, "altri 5") || strings.Contains(n.Command, "evil") || !strings.Contains(n.Message, "2 percorsi non validi") {
		t.Fatalf("cap files=%d removable=%d command=%s", len(n.Files), n.Removable, n.Command)
	}
	if len(ssh.pushed) != 0 {
		t.Fatal("cleanup wrote to a node")
	}
}

func TestNodeCleanupUnknownReferences(t *testing.T) {
	for _, raw := range []string{"null", "{}", "[", `[{"type":"qemu","vmid":1,"node":"../bad"}]`, `[{"type":"unexpected"}]`} {
		refs := readCleanupReferences(context.Background(), &fakeCleanupAPI{guests: raw})
		if refs.Known {
			t.Fatalf("accepted guest list %s", raw)
		}
	}
	refs := readCleanupReferences(context.Background(), &fakeCleanupAPI{guests: "[]", listErr: errors.New("secret")})
	if refs.Known {
		t.Fatal("list error treated as empty")
	}
	for _, raw := range []string{"null", "[]", `{"cicustom":null}`, `{"cicustom":12}`, `{"cicustom":"user=local:snippets/../bad"}`, `{"cicustom":"unexpected"}`} {
		c := &fakeCleanupAPI{guests: `[{"type":"qemu","vmid":1,"node":"n1"}]`, configs: map[int]string{1: raw}}
		refs = readCleanupReferences(context.Background(), c)
		if refs.Known || refs.ConfigsFailed != 1 {
			t.Fatalf("accepted config %s: %+v", raw, refs)
		}
	}
	for _, raw := range []string{`{}`, `{"cicustom":""}`, `{"cicustom":"user=local:snippets/subdir/a.yaml"}`} {
		if _, ok := cleanupSnippetNames(json.RawMessage(raw), nil); !ok {
			t.Fatalf("refused %s", raw)
		}
	}
}

func TestNodeCleanupGuestAndReferenceLimits(t *testing.T) {
	guests := []string{}
	c := &fakeCleanupAPI{configs: map[int]string{}}
	for i := range 2001 {
		guests = append(guests, fmt.Sprintf(`{"type":"qemu","vmid":%d,"node":"n1","name":"guest"}`, i+1))
		c.configs[i+1] = `{"cicustom":"user=local:snippets/shared.yaml"}`
	}
	c.guests = "[" + strings.Join(guests, ",") + "]"
	refs := readCleanupReferences(context.Background(), c)
	if refs.Known || refs.GuestsRead != 2000 || refs.ConfigsFailed != 1 || c.reads != 2000 || c.peak > 8 {
		t.Fatalf("limits %+v reads=%d peak=%d", refs, c.reads, c.peak)
	}
	c.guests = "[" + strings.Join(guests[:21], ",") + "]"
	refs = readCleanupReferences(context.Background(), c)
	n := cleanupNodePlan(syncNodePlan{Ready: true, State: "ok", extra: []string{"snippets/shared.yaml"}}, refs, "20261125-123456")
	if !refs.Known || len(n.Files[0].UsedBy) != 20 || n.Files[0].UsedByCount != 21 || n.Command != "" {
		t.Fatalf("reference cap %+v", n)
	}
	if len(refs.byName["shared.yaml"].guests) != 20 {
		t.Fatal("reference list must be bounded before serialization")
	}
}

func TestNodeCleanupEndpoint(t *testing.T) {
	reportSettingsTestRouter(t)
	cfg := config.Get()
	cfg.Clusters[0].URL = "https://pve.example.invalid"
	if err := config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	ssh := &fakeSSH{pinned: map[string]string{"192.0.2.1": fpA}, lists: map[string]nodessh.Listing{"192.0.2.1": listingOf("snippets/used.yaml", "pmgr-scripts/old.sh")}}
	h := &Handler{}
	r := chi.NewRouter()
	r.Get("/api/clusters/{idx}/node-cleanup", h.GetNodeCleanup)
	for _, setup := range []func(){func() {}, func() { h.SetNodeSSH(ssh) }, func() { h.SetNodeSSH(nil); h.SetSnippetBuilder(testBundleBuilder(t)) }} {
		setup()
		if w := reportRequest(r, "GET", "/api/clusters/0/node-cleanup", ""); w.Code != 503 {
			t.Fatalf("missing setup: %d", w.Code)
		}
	}
	h.SetNodeSSH(ssh)
	lock := syncOpLock(0)
	lock.Lock()
	w := reportRequest(r, "GET", "/api/clusters/0/node-cleanup", "")
	lock.Unlock()
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("busy %d", w.Code)
	}
	c := &fakeCleanupAPI{guests: `[{"type":"qemu","vmid":100,"name":"template","node":"n1","template":1}]`, configs: map[int]string{100: `{"cicustom":"user=local:snippets/used.yaml"}`}}
	r = chi.NewRouter()
	r.Get("/api/clusters/{idx}/node-cleanup", func(w http.ResponseWriter, r *http.Request) {
		h.serveNodeCleanup(w, r, 0, cfg.Clusters[0], c)
	})
	for range 2 {
		w = reportRequest(r, "GET", "/api/clusters/0/node-cleanup", "")
		var res nodeCleanupResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "no-store" || res.GeneratedAt <= 0 || !res.References.Known || res.References.GuestsRead != 1 || res.Nodes[0].InUse != 1 || len(res.Manual) != 5 {
			t.Fatalf("%+v", res)
		}
	}
	if len(ssh.listed) != 2 || len(ssh.pushed) != 0 {
		t.Fatalf("not a fresh read-only check: %+v", ssh)
	}
	h.SetSnippetBuilder(failingBuilder{})
	w = reportRequest(r, "GET", "/api/clusters/0/node-cleanup", "")
	if w.Code != 502 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// Hookscripts live in snippets/ too and run at every start: a VM or a container that uses one keeps it in use.
func TestNodeCleanupHookscriptsAreReferences(t *testing.T) {
	c := &fakeCleanupAPI{
		guests: `[{"type":"qemu","vmid":100,"name":"vm","node":"n1"},{"type":"lxc","vmid":200,"name":"ct","node":"n1"}]`,
		configs: map[int]string{
			100: `{"hookscript":"local:snippets/vm-hook.sh","cicustom":"vendor=local:snippets/vendor.yaml"}`,
			200: `{"hookscript":"local:snippets/ct-hook.pl"}`,
		},
	}
	refs := readCleanupReferences(context.Background(), c)
	if !refs.Known || refs.GuestsRead != 2 {
		t.Fatalf("refs %+v", refs)
	}
	for _, name := range []string{"vm-hook.sh", "ct-hook.pl", "vendor.yaml"} {
		if refs.byName[name].count != 1 {
			t.Fatalf("%s not counted as used: %+v", name, refs.byName)
		}
	}
	node := cleanupNodePlan(syncNodePlan{Node: "n1", Ready: true, extra: []string{"snippets/ct-hook.pl", "snippets/vm-hook.sh", "snippets/old.yaml"}}, refs, "20261125-000000")
	if node.InUse != 2 || node.Removable != 1 || strings.Contains(node.Command, "hook") || !strings.Contains(node.Command, "'snippets/old.yaml'") {
		t.Fatalf("%+v", node)
	}
	for _, raw := range []string{`{"hookscript":12}`, `{"hookscript":"local:snippets/../x"}`, `{"hookscript":"/var/lib/vz/snippets/x"}`} {
		if _, ok := cleanupSnippetNames(json.RawMessage(raw), nil); ok {
			t.Fatalf("accepted %s", raw)
		}
	}
}
