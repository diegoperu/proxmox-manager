package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"proxmox-manager/internal/nodessh"
	"proxmox-manager/internal/snippetsync"
)

func testBundleBuilder(t *testing.T) *snippetsync.Builder {
	t.Helper()
	tree := fstest.MapFS{
		"bin/pmgr-os.sh": {Data: []byte("#!/bin/bash\n")},
		"README.md":      {Data: []byte("readme\n")},
	}
	render := func(_ context.Context, _ string, args, _ []string) error {
		return os.WriteFile(filepath.Join(args[1], "vendor_debian_13.yaml"), []byte("#cloud-config "+args[3]+"\n"), 0o644)
	}
	return snippetsync.NewBuilderWithRenderer(tree, render)
}

func listingOfBundle(b *snippetsync.Bundle) nodessh.Listing {
	l := nodessh.Listing{Entries: []nodessh.Entry{}}
	for _, f := range b.Files {
		l.Entries = append(l.Entries, nodessh.Entry{Path: f.Key(), Hash: f.Hash, Size: f.Size})
	}
	return l
}

type failingBuilder struct{}

func (failingBuilder) Build(context.Context, string) (*snippetsync.Bundle, error) {
	return nil, errors.New("bash: secret detail")
}

func syncPlanRouter(t *testing.T, ssh *fakeSSH, b bundleBuilder) http.Handler {
	t.Helper()
	syncTestRouter(t, nil) // config with the fake Proxmox cluster status, restored after the test
	h := &Handler{}
	h.SetNodeSSH(ssh)
	h.SetSnippetBuilder(b)
	r := chi.NewRouter()
	r.Get("/api/clusters/{idx}/sync-plan", h.GetSyncPlan)
	r.Post("/api/clusters/{idx}/sync-run", h.PostSyncRun)
	return r
}

func TestSyncPlanAndRun(t *testing.T) {
	builder := testBundleBuilder(t)
	bundle, err := builder.Build(context.Background(), "standard")
	if err != nil {
		t.Fatal(err)
	}
	full := listingOfBundle(bundle)
	f := &fakeSSH{
		pinned: map[string]string{"192.0.2.1": fpA, "192.0.2.2": fpA, "192.0.2.5": fpA},
		lists: map[string]nodessh.Listing{
			"192.0.2.1": full,
			"192.0.2.2": {Entries: []nodessh.Entry{{Path: "snippets/old.yaml", Hash: strings.Repeat("9", 64)}}},
		},
	}
	f.onPush = func(host, dir, remote string) { f.lists[host] = full }
	r := syncPlanRouter(t, f, builder)

	w := reportRequest(r, "GET", "/api/clusters/0/sync-plan", "")
	if w.Code != 200 {
		t.Fatalf("plan %d %s", w.Code, w.Body)
	}
	var plan struct {
		Profile string `json:"profile"`
		Digest  string `json:"digest"`
		Summary struct {
			Aligned  int `json:"aligned"`
			ToSync   int `json:"to_sync"`
			NotReady int `json:"not_ready"`
		} `json:"summary"`
		Nodes []struct {
			Node                     string
			Ready, Aligned           bool
			MissingCount, ExtraCount int      `json:"-"`
			Extra                    []string `json:"extra"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Profile != "standard" || plan.Summary.Aligned != 1 || plan.Summary.ToSync != 1 || plan.Summary.NotReady != 6 {
		t.Fatalf("plan %+v", plan)
	}

	// a plan that is not the current one copies nothing
	w = reportRequest(r, "POST", "/api/clusters/0/sync-run", `{"nodes":["n2"],"digest":"000000000000000000000000"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "plan_changed") || len(f.pushed) != 0 {
		t.Fatalf("stale plan %d %s pushed %v", w.Code, w.Body, f.pushed)
	}

	w = reportRequest(r, "POST", "/api/clusters/0/sync-run", `{"nodes":["n1","n2","n4"],"digest":"`+plan.Digest+`"}`)
	if w.Code != 200 {
		t.Fatalf("run %d %s", w.Code, w.Body)
	}
	var run struct {
		OK      bool `json:"ok"`
		Results []struct {
			Node    string `json:"node"`
			OK      bool   `json:"ok"`
			Copied  int    `json:"copied"`
			Message string `json:"message"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.OK || len(run.Results) != 3 {
		t.Fatalf("run %+v", run)
	}
	if !run.Results[0].OK || run.Results[0].Copied != 0 {
		t.Fatalf("aligned node: %+v", run.Results[0])
	}
	if !run.Results[1].OK || run.Results[1].Copied != len(bundle.Files) || !strings.Contains(run.Results[1].Message, "verificati") {
		t.Fatalf("synced node: %+v", run.Results[1])
	}
	if run.Results[2].OK || !strings.Contains(run.Results[2].Message, "Non pronto") {
		t.Fatalf("not ready node: %+v", run.Results[2])
	}
	if strings.Join(f.pushed, ",") != "192.0.2.2 pmgr-scripts,192.0.2.2 snippets" {
		t.Fatalf("pushed %v", f.pushed)
	}
	if _, ok := f.lists["192.0.2.1"]; !ok {
		t.Fatal("aligned node touched")
	}
}

func TestSyncRunReportsCopyFailures(t *testing.T) {
	builder := testBundleBuilder(t)
	f := &fakeSSH{pinned: map[string]string{"192.0.2.2": fpA}, lists: map[string]nodessh.Listing{"192.0.2.2": {}}, pushErr: nodessh.ErrGateDenied}
	r := syncPlanRouter(t, f, builder)
	w := reportRequest(r, "GET", "/api/clusters/0/sync-plan", "")
	var plan struct{ Digest string }
	json.Unmarshal(w.Body.Bytes(), &plan)
	w = reportRequest(r, "POST", "/api/clusters/0/sync-run", `{"nodes":["n2"],"digest":"`+plan.Digest+`"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "comando ristretto") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestSyncRunRejectsBadRequests(t *testing.T) {
	f := &fakeSSH{pinned: map[string]string{}, lists: map[string]nodessh.Listing{}}
	r := syncPlanRouter(t, f, testBundleBuilder(t))
	d := strings.Repeat("a", 24)
	many := make([]string, 65)
	for i := range many {
		many[i] = `"n` + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + `"`
	}
	for _, body := range []string{
		`{"nodes":["n1"],"digest":"` + d + `","force":true}`,
		`{"nodes":["n1","n1"],"digest":"` + d + `"}`,
		`{"nodes":["n1;id"],"digest":"` + d + `"}`,
		`{"nodes":[],"digest":"` + d + `"}`,
		`{"nodes":["n1"],"digest":"short"}`,
		`{"nodes":[` + strings.Join(many, ",") + `],"digest":"` + d + `"}`,
		`{"nodes":["n1"],"digest":"` + d + `"} {}`,
	} {
		if w := reportRequest(r, "POST", "/api/clusters/0/sync-run", body); w.Code != 400 {
			t.Fatalf("%q: %d", body, w.Code)
		}
	}
	if len(f.pushed) != 0 {
		t.Fatal("a refused request copied files")
	}
	lock := syncOpLock(0)
	lock.Lock()
	if w := reportRequest(r, "GET", "/api/clusters/0/sync-plan", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("busy %d", w.Code)
	}
	lock.Unlock()
}

func TestSyncPlanHidesBuilderErrors(t *testing.T) {
	r := syncPlanRouter(t, &fakeSSH{pinned: map[string]string{}}, failingBuilder{})
	w := reportRequest(r, "GET", "/api/clusters/0/sync-plan", "")
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
