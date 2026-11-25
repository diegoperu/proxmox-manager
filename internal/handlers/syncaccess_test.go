package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/nodessh"

	"github.com/go-chi/chi/v5"
)

const (
	testKeyLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOdgCfDNtrRZu7tP4kD8WE1PN2xD5bGpW8YuVzPvZx6h proxmox-manager"
	fpA         = "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fpB         = "SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeSSH: pinned addresses, the key each address presents now, and a pmgr-list result per address.
type fakeSSH struct {
	mu      sync.Mutex
	pinned  map[string]string
	present map[string]string
	lists   map[string]nodessh.Listing
	errs    map[string]error
	listed  []string
	noKey   bool
}

func (f *fakeSSH) EnsureKey(context.Context) (nodessh.PublicKey, error) {
	if f.noKey {
		return nodessh.PublicKey{}, errors.New("no key")
	}
	return nodessh.PublicKey{Line: testKeyLine, Fingerprint: "SHA256:manager"}, nil
}
func (f *fakeSSH) Pinned(h string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fp, ok := f.pinned[h]
	return fp, ok, nil
}
func (f *fakeSSH) Scan(_ context.Context, h string) (string, string, error) {
	fp, ok := f.present[h]
	if !ok {
		return "", "", nodessh.ErrUnreachable
	}
	return "b64", fp, nil
}
func (f *fakeSSH) Pin(ctx context.Context, h, confirmed string) error {
	_, fp, err := f.Scan(ctx, h)
	if err != nil {
		return err
	}
	if fp != confirmed {
		return nodessh.ErrHostKeyChanged
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinned[h] = fp
	return nil
}
func (f *fakeSSH) Unpin(h string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.pinned[h]
	delete(f.pinned, h)
	return ok, nil
}
func (f *fakeSSH) List(_ context.Context, h string) (nodessh.Listing, error) {
	f.mu.Lock()
	f.listed = append(f.listed, h)
	f.mu.Unlock()
	if err := f.errs[h]; err != nil {
		return nodessh.Listing{}, err
	}
	return f.lists[h], nil
}

type fakeStatus struct{ raw string }

func (f fakeStatus) GetClusterStatus() (json.RawMessage, error) { return json.RawMessage(f.raw), nil }

func listingOf(paths ...string) nodessh.Listing {
	l := nodessh.Listing{Entries: []nodessh.Entry{}}
	for _, p := range paths {
		l.Entries = append(l.Entries, nodessh.Entry{Hash: strings.Repeat("c", 64), Size: 1, Path: p})
	}
	return l
}

const testClusterStatus = `[{"type":"cluster","name":"c"},
 {"type":"node","name":"n1","ip":"192.0.2.1","online":1},
 {"type":"node","name":"n2","ip":"192.0.2.2","online":1},
 {"type":"node","name":"n3","ip":"192.0.2.3","online":1},
 {"type":"node","name":"n4","ip":"192.0.2.4","online":1},
 {"type":"node","name":"n5","ip":"192.0.2.5","online":0},
 {"type":"node","name":"n6","ip":"","online":1},
 {"type":"node","name":"n7","ip":"192.0.2.7","online":1},
 {"type":"node","name":"n8","ip":"192.0.2.8;id","online":1},
 {"type":"node","name":"bad name","ip":"192.0.2.9","online":1}]`

func TestBuildSyncAccessStates(t *testing.T) {
	f := &fakeSSH{
		pinned: map[string]string{"192.0.2.1": fpA, "192.0.2.2": fpA, "192.0.2.3": fpA, "192.0.2.5": fpA, "192.0.2.7": fpA},
		lists: map[string]nodessh.Listing{
			"192.0.2.1": listingOf("snippets/a.yaml"), "192.0.2.2": listingOf("snippets/a.yaml"), "192.0.2.3": listingOf("snippets/b.yaml"),
		},
		errs: map[string]error{"192.0.2.7": nodessh.ErrAuthRefused},
	}
	cl := config.ClusterConfig{Label: "C", Network: &config.NetworkSettings{ManagerFrom: []string{"192.0.2.254"}}}
	res, err := buildSyncAccess(context.Background(), fakeStatus{testClusterStatus}, f, cl, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"n1": "ok", "n2": "ok", "n3": "ok", "n4": "not_pinned", "n5": "offline", "n6": "no_address", "n7": "auth_refused", "n8": "no_address"}
	if len(res.Nodes) != len(want) {
		t.Fatalf("nodes %+v", res.Nodes)
	}
	for _, n := range res.Nodes {
		if want[n.Node] != n.State {
			t.Fatalf("node %s: state %s, want %s", n.Node, n.State, want[n.Node])
		}
	}
	byName := map[string]syncNodeState{}
	for _, n := range res.Nodes {
		byName[n.Node] = n
	}
	if *byName["n1"].SameAsOthers != true || *byName["n2"].SameAsOthers != true || *byName["n3"].SameAsOthers != false {
		t.Fatal("same_as_others wrong")
	}
	if byName["n4"].SameAsOthers != nil {
		t.Fatal("same_as_others set for a node that was not listed")
	}
	// only pinned, online nodes with an address were contacted
	for _, h := range f.listed {
		if h == "192.0.2.4" || h == "192.0.2.5" || strings.Contains(h, ";") {
			t.Fatalf("contacted %s", h)
		}
	}
	if res.Summary.OK != 3 || res.Summary.Todo != 5 || res.Summary.Problem != 0 {
		t.Fatalf("summary %+v", res.Summary)
	}
	if !res.Setup.Ready || len(res.Setup.Commands) != 2 || !strings.Contains(res.Setup.Commands[0], testKeyLine) ||
		!strings.HasSuffix(res.Setup.Commands[1], "--source-from 192.0.2.254") {
		t.Fatalf("setup %+v", res.Setup)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "Permission denied") {
		t.Fatal("raw ssh text in the response")
	}
}

func TestSetupCommandsNeedAddressAndKey(t *testing.T) {
	if s := setupCommands(nodessh.PublicKey{Line: testKeyLine}, nil); s.Ready || len(s.Commands) != 0 || !strings.Contains(s.Reason, "Infrastruttura") {
		t.Fatalf("%+v", s)
	}
	if s := setupCommands(nodessh.PublicKey{}, []string{"192.0.2.1"}); s.Ready || len(s.Commands) != 0 {
		t.Fatalf("%+v", s)
	}
	s := setupCommands(nodessh.PublicKey{Line: testKeyLine}, []string{"192.0.2.1", "192.0.2.2"})
	if !strings.HasSuffix(s.Commands[1], "--source-from 192.0.2.1,192.0.2.2") {
		t.Fatalf("%+v", s)
	}
}

func syncTestRouter(t *testing.T, ssh nodeSSH) http.Handler {
	t.Helper()
	pve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/cluster/status" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":` + testClusterStatus + `}`))
	}))
	t.Cleanup(pve.Close)
	reportSettingsTestRouter(t) // temporary config directory, restored after the test
	cfg := config.Get()
	cfg.Clusters = []config.ClusterConfig{{Label: "C", URL: pve.URL, APIToken: "u@pve!t=secret", Default: true}}
	if err := config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	forgetSyncAccess()
	h := &Handler{}
	if ssh != nil {
		h.SetNodeSSH(ssh)
	}
	r := chi.NewRouter()
	r.Get("/api/clusters/{idx}/sync-access", h.GetSyncAccess)
	r.Post("/api/clusters/{idx}/sync-access/scan", h.PostSyncScan)
	r.Post("/api/clusters/{idx}/sync-access/pin", h.PostSyncPin)
	r.Delete("/api/clusters/{idx}/sync-access/pin/{node}", h.DeleteSyncPin)
	return r
}

func TestSyncAccessEndpoints(t *testing.T) {
	f := &fakeSSH{pinned: map[string]string{}, present: map[string]string{"192.0.2.1": fpA, "192.0.2.2": fpB}, lists: map[string]nodessh.Listing{}}
	r := syncTestRouter(t, f)

	w := reportRequest(r, "POST", "/api/clusters/0/sync-access/scan", `{"node":"n1"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), fpA) || !strings.Contains(w.Body.String(), nodessh.VerifyCommand) {
		t.Fatalf("scan %d %s", w.Code, w.Body)
	}
	if len(f.pinned) != 0 {
		t.Fatal("scan stored a key")
	}
	// confirming a fingerprint the node does not present is refused
	if w = reportRequest(r, "POST", "/api/clusters/0/sync-access/pin", `{"node":"n1","fingerprint":"`+fpB+`"}`); w.Code != 409 || len(f.pinned) != 0 {
		t.Fatalf("mismatch %d %s", w.Code, w.Body)
	}
	if w = reportRequest(r, "POST", "/api/clusters/0/sync-access/pin", `{"node":"n1","fingerprint":"`+fpA+`"}`); w.Code != 200 || f.pinned["192.0.2.1"] != fpA {
		t.Fatalf("pin %d %s", w.Code, w.Body)
	}
	if w = reportRequest(r, "GET", "/api/clusters/0/sync-access", ""); w.Code != 200 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("get %d %s", w.Code, w.Body)
	}
	if w = reportRequest(r, "DELETE", "/api/clusters/0/sync-access/pin/n1", ""); w.Code != 200 || len(f.pinned) != 0 {
		t.Fatalf("unpin %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/clusters/0/sync-access/scan", `{"node":"n1","address":"203.0.113.9"}`}, // the address never comes from the request
		{"POST", "/api/clusters/0/sync-access/scan", `{"node":"n1;id"}`},
		{"POST", "/api/clusters/0/sync-access/scan", `{"node":"n1","fingerprint":"` + fpA + `"}`},
		{"POST", "/api/clusters/0/sync-access/pin", `{"node":"n1"}`},
		{"POST", "/api/clusters/0/sync-access/pin", `{"node":"n1","fingerprint":"SHA256:short"}`},
		{"POST", "/api/clusters/0/sync-access/pin", `{"node":"n1","fingerprint":"` + fpA + `"} {}`},
		{"POST", "/api/clusters/0/sync-access/pin", strings.Repeat(" ", 2000) + `{"node":"n1","fingerprint":"` + fpA + `"}`},
		{"DELETE", "/api/clusters/0/sync-access/pin/n1%3Bid", ""},
	} {
		if w = reportRequest(r, c.method, c.path, c.body); w.Code != 400 {
			t.Fatalf("%s %s %q: %d", c.method, c.path, c.body, w.Code)
		}
	}
	if w = reportRequest(r, "POST", "/api/clusters/0/sync-access/scan", `{"node":"zz"}`); w.Code != 404 {
		t.Fatalf("unknown node %d", w.Code)
	}
	if w = reportRequest(r, "POST", "/api/clusters/0/sync-access/scan", `{"node":"n6"}`); w.Code != 409 {
		t.Fatalf("node without address %d", w.Code)
	}
	if w = reportRequest(r, "GET", "/api/clusters/7/sync-access", ""); w.Code != 404 {
		t.Fatalf("bad cluster %d", w.Code)
	}
	if len(f.pinned) != 0 {
		t.Fatal("a refused request changed the pinned keys")
	}
}

func TestSyncAccessWithoutStore(t *testing.T) {
	r := syncTestRouter(t, nil)
	if w := reportRequest(r, "GET", "/api/clusters/0/sync-access", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d", w.Code)
	}
	if w := reportRequest(r, "POST", "/api/clusters/0/sync-access/scan", `{"node":"n1"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d", w.Code)
	}
}
