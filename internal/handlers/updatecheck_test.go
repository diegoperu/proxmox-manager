package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"proxmox-manager/internal/buildinfo"
	"proxmox-manager/internal/config"
)

const updateTestCommit = "0123456789abcdef0123456789abcdef01234567"

func setupUpdateTest(t *testing.T) *Handler {
	t.Helper()
	reportSettingsTestRouter(t)
	commit, date, dirty, base := buildinfo.Commit, buildinfo.Date, buildinfo.Dirty, updateAPIBase
	buildinfo.Commit, buildinfo.Date, buildinfo.Dirty = updateTestCommit, "2026-11-20T08:00:00Z", "false"
	updateChecks.mu.Lock()
	updateChecks.cached, updateChecks.last, updateChecks.running = updateResult{}, time.Time{}, false
	updateChecks.mu.Unlock()
	t.Cleanup(func() {
		buildinfo.Commit, buildinfo.Date, buildinfo.Dirty, updateAPIBase = commit, date, dirty, base
		updateChecks.mu.Lock()
		updateChecks.cached, updateChecks.last, updateChecks.running = updateResult{}, time.Time{}, false
		updateChecks.mu.Unlock()
	})
	return New()
}

func updateFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/github_compare_" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUpdateCompareFixtures(t *testing.T) {
	wantTitles := []string{
		"docs: design of the update check and GitHub release binaries (ROADMAP 24, D134) (#54)",
		"feat(settings): per-cluster network values for new VMs (ROADMAP 25) (#53)",
		"docs(roadmap): update check from the repository (24) and per-cluster network values (25) (#52)",
		"docs: refresh README, CLAUDE.md, ROADMAP, SECURITY, DESIGN and mock README (#51)",
		"fix(settings): name-based storage proposals are opt-in (#50)",
	}
	for _, tc := range []struct {
		name, state string
		behind      int
		titles      []string
	}{
		{"ahead", "behind", 6, []string{}}, {"identical", "up_to_date", 0, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got updateResult
			if err := parseUpdateCompare(updateFixture(t, tc.name), &got); err != nil {
				t.Fatal(err)
			}
			if got.State != tc.state || got.BehindBy != tc.behind || !reflect.DeepEqual(got.Titles, tc.titles) {
				t.Fatalf("%+v", got)
			}

		})
	}
	commits, err := os.ReadFile("testdata/github_commits_main.json")
	if err != nil {
		t.Fatal(err)
	}
	var parsed updateResult
	if err := parseUpdateCommits(commits, &parsed, 6); err != nil || !reflect.DeepEqual(parsed.Titles[:5], wantTitles) || parsed.LatestCommitDate != "2026-11-20T08:17:19Z" {
		t.Fatalf("%+v %v", parsed, err)
	}

	for _, tc := range []struct {
		status string
		a, b   int
		state  string
	}{{"behind", 0, 2, "ahead_of_main"}, {"diverged", 3, 2, "diverged"}} {
		var got updateResult
		if err := parseUpdateCompare([]byte(fmt.Sprintf(`{"status":%q,"ahead_by":%d,"behind_by":%d}`, tc.status, tc.a, tc.b)), &got); err != nil || got.State != tc.state || got.BehindBy != tc.a {
			t.Fatalf("%+v %v", got, err)
		}
	}
}

func TestUpdateTitlesAndInvalidResponse(t *testing.T) {

	hostile := "<img src=x onerror=alert(1)>\x00\x1b\t\r\u202e" + strings.Repeat("界", 500) + "\nignored"
	body, err := json.Marshal([]any{map[string]any{"sha": updateTestCommit, "commit": map[string]any{"message": hostile, "committer": map[string]any{"date": "2026-11-20T08:17:19Z"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var got updateResult
	if err := parseUpdateCommits(body, &got, 1); err != nil {
		t.Fatal(err)
	}
	want := "<img src=x onerror=alert(1)>" + strings.Repeat("界", 92)
	if len(got.Titles) != 1 || got.Titles[0] != want || len([]rune(got.Titles[0])) != 120 {
		t.Fatalf("titles: %#v", got.Titles)
	}
	for _, bad := range []string{`null`, `[]`, `[{}]`, `[{"sha":"bad"}]`, `{}`} {
		if parseUpdateCommits([]byte(bad), &updateResult{}, 1) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}

	for _, bad := range []string{`{}`, `{"status":"ahead","ahead_by":-1,"behind_by":0}`, `{"status":"identical","ahead_by":1,"behind_by":0}`, `{"status":"other","ahead_by":1,"behind_by":0}`, `{"status":"ahead","ahead_by":1.5,"behind_by":0}`, `{"status":"ahead","ahead_by":1,"behind_by":0} {}`} {
		if parseUpdateCompare([]byte(bad), &updateResult{}) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestUpdateHTTPFailuresAndLimits(t *testing.T) {
	ahead, unknown := updateFixture(t, "ahead"), updateFixture(t, "unknown")
	setupUpdateTest(t)
	newServer := updateTestServers(t)
	var destinationCalls atomic.Int32
	destination := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	for _, tc := range []struct {
		name, body, state string
		status            int
		redirect          string
	}{
		{"ok", string(ahead), "behind", 200, ""},
		{"unknown", string(unknown), "unknown_commit", 404, ""},
		{"oversized", strings.Repeat("x", 9<<20), "unreachable", 200, ""},
		{"invalid", "upstream sensitive detail", "unreachable", 200, ""},
		{"rate limited", "upstream sensitive detail", "unreachable", 429, ""},
		{"redirect", "", "unreachable", 302, strings.Replace(destination.URL, "127.0.0.1", "localhost", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/"+updateRepo+"/commits" {
					if r.URL.RawQuery != "sha=main&per_page=6" {
						t.Errorf("query: %s", r.URL.RawQuery)
					}
					http.Error(w, "failed", 500)
					return
				}
				if r.URL.Path != "/repos/"+updateRepo+"/compare/"+updateTestCommit+"...main" || r.URL.RawQuery != "per_page=1" || r.UserAgent() == "" {
					t.Errorf("request: %s", r.URL)
				}
				if tc.redirect != "" {
					w.Header().Set("Location", tc.redirect)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			updateAPIBase = server.URL
			result := updateResult{Titles: []string{}}
			var count int64
			fetchUpdate(context.Background(), updateTestCommit, &result, &count)
			if result.State != tc.state || strings.Contains(result.Error, "sensitive") {
				t.Fatalf("%+v", result)
			}
			if tc.name == "ok" && (result.BehindBy != 6 || len(result.Titles) != 0) {
				t.Fatalf("commits failure: %+v", result)
			}
			if tc.name == "oversized" && count != updateCompareLimit+1 {
				t.Fatalf("bytes: %d", count)
			}
		})
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("redirect followed to another host")
	}
	closed := newServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	updateAPIBase = closed.URL
	var result updateResult
	var count int64
	fetchUpdate(context.Background(), updateTestCommit, &result, &count)
	if result.State != "unreachable" || result.Error != "GitHub non raggiungibile" {
		t.Fatalf("closed: %+v", result)
	}
}

func TestUpdateEndpointsCacheGateAndSingleFlight(t *testing.T) {
	fixture := updateFixture(t, "ahead")
	commits, err := os.ReadFile("testdata/github_commits_main.json")
	if err != nil {
		t.Fatal(err)
	}
	h := setupUpdateTest(t)
	newServer := updateTestServers(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	server := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/repos/"+updateRepo+"/commits" {
			if r.URL.RawQuery != "sha=main&per_page=6" {
				t.Errorf("query: %s", r.URL.RawQuery)
			}
			w.Write(commits)
			return
		}
		if r.URL.Path != "/repos/"+updateRepo+"/compare/"+updateTestCommit+"...main" || r.URL.RawQuery != "per_page=1" {
			t.Errorf("request: %s", r.URL)
		}
		started <- struct{}{}
		<-release
		w.Write(fixture)
	}))
	defer server.Close()
	defer close(release)
	updateAPIBase = server.URL
	get := func() updateResult {
		w := httptest.NewRecorder()
		h.UpdateStatus(w, httptest.NewRequest("GET", "/api/update-status", nil))
		var got updateResult
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if get().State != "pending" || calls.Load() != 0 {
		t.Fatal("GET performed a check")
	}
	version := httptest.NewRecorder()
	h.Version(version, httptest.NewRequest("GET", "/api/version", nil))
	var info buildinfo.BuildInfo
	if json.Unmarshal(version.Body.Bytes(), &info) != nil || info.Commit != updateTestCommit || calls.Load() != 0 {
		t.Fatal("version must be local")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		h.UpdateCheck(w, httptest.NewRequest("POST", "/api/update-check", nil))
		done <- w
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not start")
	}
	// Even after the cooldown, an in-flight check cannot overlap.
	updateChecks.mu.Lock()
	updateChecks.last = time.Now().Add(-10 * time.Minute)
	updateChecks.mu.Unlock()
	w := httptest.NewRecorder()
	h.UpdateCheck(w, httptest.NewRequest("POST", "/api/update-check", nil))
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || calls.Load() != 1 {
		t.Fatal("single flight failed")
	}
	updateChecks.mu.Lock()
	updateChecks.last = time.Now()
	updateChecks.mu.Unlock()
	release <- struct{}{}
	select {
	case w = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not finish")
	}
	if w.Code != 200 {
		t.Fatalf("check status %d", w.Code)
	}
	if got := get(); got.State != "behind" || got.BehindBy != 6 || got.CheckedAt == "" || got.Local.Short != "0123456" || calls.Load() != 2 {
		t.Fatalf("cache %+v, requests %d", got, calls.Load())
	}
	w = httptest.NewRecorder()
	h.UpdateCheck(w, httptest.NewRequest("POST", "/api/update-check", nil))
	if w.Code != 429 || w.Header().Get("Retry-After") != "300" || !strings.Contains(w.Body.String(), `"retry_after":300`) || calls.Load() != 2 {
		t.Fatalf("rate limit %d %s", w.Code, w.Body)
	}
	cfg := config.Get()
	off := false
	cfg.UpdateCheck = &off
	if err := config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		w = httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/update-status", nil)
		if method == "GET" {
			h.UpdateStatus(w, req)
		} else {
			h.UpdateCheck(w, req)
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"disabled"`) {
			t.Fatalf("disabled: %s", w.Body)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("disabled made a request")
	}
	cfg.UpdateCheck = nil
	if err := config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	buildinfo.Commit = "invalid"
	if get().State != "no_build_info" {
		t.Fatal("missing build info")
	}
	w = httptest.NewRecorder()
	h.UpdateCheck(w, httptest.NewRequest("POST", "/api/update-check", nil))
	if !strings.Contains(w.Body.String(), `"state":"no_build_info"`) || calls.Load() != 2 {
		t.Fatal("missing build info made a request")
	}
}

func TestUpdatePanicRecoveryAndCancelledLoop(t *testing.T) {
	setupUpdateTest(t)
	old := http.DefaultTransport
	http.DefaultTransport = priceRoundTrip(func(*http.Request) (*http.Response, error) { panic("upstream sensitive detail") })
	defer func() { http.DefaultTransport = old }()
	var checker updateChecker
	result, retry := checker.check(context.Background())
	if result.State != "unreachable" || result.Error != "Controllo aggiornamenti non riuscito" || retry != 0 || checker.running {
		t.Fatalf("panic result: %+v", result)
	}
	if checker.status().State != "unreachable" {
		t.Fatal("panic result not cached")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { checker.loop(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop")
	}
}

func TestUpdateSeparateRequests(t *testing.T) {
	setupUpdateTest(t)
	newServer := updateTestServers(t)
	for _, tc := range []struct {
		name, compare, state, query, commits string
		status, calls, behind                int
	}{
		{"large", `{"status":"ahead","ahead_by":60,"behind_by":0,"files":"` + strings.Repeat("x", 3<<20) + `"}`, "behind", "sha=main&per_page=20", `[]`, 200, 2, 60},
		{"one", `{"status":"ahead","ahead_by":1,"behind_by":0}`, "behind", "sha=main&per_page=1", `invalid`, 200, 2, 1},
		{"diverged", `{"status":"diverged","ahead_by":2,"behind_by":1}`, "diverged", "sha=main&per_page=2", `invalid`, 200, 2, 2},
		{"identical", `{"status":"identical","ahead_by":0,"behind_by":0}`, "up_to_date", "", "", 200, 1, 0},
		{"ahead", `{"status":"behind","ahead_by":0,"behind_by":1}`, "ahead_of_main", "", "", 200, 1, 0},
		{"unknown", `{}`, "unknown_commit", "", "", 404, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/repos/" + updateRepo + "/compare/" + updateTestCommit + "...main":
					if r.URL.RawQuery != "per_page=1" {
						t.Errorf("query %s", r.URL.RawQuery)
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.compare)
				case "/repos/" + updateRepo + "/commits":
					if r.URL.RawQuery != tc.query {
						t.Errorf("query %s", r.URL.RawQuery)
					}
					fmt.Fprint(w, tc.commits)
				default:
					t.Errorf("path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			updateAPIBase = server.URL
			result := updateResult{Titles: []string{}}
			var count int64
			fetchUpdate(context.Background(), updateTestCommit, &result, &count)
			if result.State != tc.state || result.BehindBy != tc.behind || len(result.Titles) != 0 || calls != tc.calls {
				t.Fatalf("result %+v calls %d", result, calls)
			}
		})
	}
}
