package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"proxmox-manager/internal/config"
)

func reportSettingsTestRouter(t *testing.T) http.Handler {
	t.Helper()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	old := config.Get()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := config.Update(old); err != nil {
			t.Error(err)
		}
		if err := os.Chdir(oldDir); err != nil {
			t.Error(err)
		}
	})
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Get()
	cfg.Clusters = []config.ClusterConfig{
		{Label: "Calcolo", APIToken: "sensitive-token-first", Default: true},
		{Label: "Hosting", APIToken: "sensitive-token-second", Report: &config.ReportSettings{UsdEur: .9}},
	}
	if err := config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	h := &Handler{}
	r := chi.NewRouter()
	r.Post("/api/clusters", h.AddCluster)
	r.Put("/api/clusters/{idx}", h.UpdateCluster)
	r.Get("/api/clusters/{idx}/report-settings", h.GetReportSettings)
	r.Put("/api/clusters/{idx}/report-settings", h.PutReportSettings)
	return r
}

func reportRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)
	return w
}

func TestReportSettingsRoundTrip(t *testing.T) {
	router := reportSettingsTestRouter(t)
	w := reportRequest(router, "GET", "/api/clusters/0/report-settings", "")
	if w.Code != 200 {
		t.Fatalf("GET: %d %s", w.Code, w.Body)
	}
	var zero config.ReportSettings
	if err := json.Unmarshal(w.Body.Bytes(), &zero); err != nil || !reflect.DeepEqual(zero, config.ReportSettings{}) {
		t.Fatalf("zero object: %v %+v", err, zero)
	}
	const body = `{"internal_prices":{"token_eur":1,"gpu_token_eur":35,"base_tokens":8,"base_cores":16,"base_mem_gib":32,"base_ssd_gb":100,"cores_block":16,"cores_block_tokens":1,"mem_block_gib":32,"mem_block_tokens":4,"ssd_block_gb":100,"ssd_block_tokens":3,"hdd_block_gb":1000,"hdd_block_tokens":2,"gpu_tiers":[{"min_gib":16,"tokens":1},{"min_gib":24,"tokens":3},{"min_gib":40,"tokens":10},{"min_gib":89,"tokens":11}]},"market_prices":[{"name":"Provider","level":"high","currency":"USD","vcpu_hour":0.038,"ram_gb_hour":0.0051,"storage_gb_month":0.094,"gpu_hour":[{"min_gib":16,"price":0.616}],"source":"https://example.org/<text>","date":"2026-11-19"}],"usd_eur":0.9,"storage_classes":{"datacloud-ssd":"ssd"},"defaults":{"inactive_cpu":0.05,"idle_days":30,"abandon_peak":10,"over_cpu":30,"margin":20},"interruptions":[{"from":"2026-11-19T10:00:00+01:00","to":"2026-11-19T11:00:00+01:00","cause":"external","note":"Elettricità"}]}`
	before := config.Get()
	w = reportRequest(router, "PUT", "/api/clusters/0/report-settings", body)
	if w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	w = reportRequest(router, "GET", "/api/clusters/0/report-settings", "")
	var want, got config.ReportSettings
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: %v %+v", err, got)
	}
	if !reflect.DeepEqual(config.Get().Clusters[1], before.Clusters[1]) {
		t.Fatal("other cluster changed")
	}
	// Dedicated responses must never serialize credentials or even their keys.
	for _, idx := range []string{"0", "1"} {
		w := reportRequest(router, "GET", "/api/clusters/"+idx+"/report-settings", "")
		for _, secret := range []string{"api_token", "sensitive-token", "password", "username"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("GET leaked %s", secret)
			}
		}
	}
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.Get().Clusters[0].Report, &want) {
		t.Fatal("settings not persisted")
	}
	w = reportRequest(router, "PUT", "/api/clusters/0/report-settings", `{}`)
	if w.Code != 200 || !reflect.DeepEqual(config.Get().Clusters[0].Report, &config.ReportSettings{}) {
		t.Fatalf("full replacement failed: %s", w.Body)
	}
	cfg := config.Get()
	if cfg.Clusters[0].APIToken != before.Clusters[0].APIToken || !reflect.DeepEqual(cfg.Clusters[1], before.Clusters[1]) {
		t.Fatal("credentials or other cluster changed after replace")
	}
}

func TestReportSettingsRejectsInvalidRequests(t *testing.T) {
	router := reportSettingsTestRouter(t)
	before := config.Get()
	disk, err := os.ReadFile("config.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"", `null`, `[]`, `true`, `{`, `{} {}`, `{} trailing`,
		`{"api_token":"sensitive-token"}`, `{"unknown":"sensitive-token"}`, `{"internal_prices":{"unknown":1}}`,
		`{"defaults":{"unknown":1}}`, `{"internal_prices":{"gpu_tiers":[{"min_gib":0,"tokens":0,"unknown":1}]}}`,
		`{"market_prices":[{"name":"p","level":"low","currency":"EUR","date":"2026-11-19","unknown":1}]}`,
		`{"market_prices":[{"gpu_hour":[{"min_gib":0,"price":0,"unknown":1}]}]}`,
		`{"interruptions":[{"unknown":1}]}`, `{"usd_eur":11}`, `{"usd_eur":"1"}`, `{"usd_eur":NaN}`, `{"usd_eur":1e999}`,
		`{"internal_prices":{"cores_block":0}}`, `{"internal_prices":{"gpu_tiers":[{"min_gib":24,"tokens":1},{"min_gib":16,"tokens":1}]}}`,
		`{"defaults":{"idle_days":6}}`, `{"defaults":{"abandon_months":1.1}}`, `{"defaults":{"inactive_cpu":0.51}}`,
		`{"storage_classes":{"../s":"ssd"}}`, `{"market_prices":[{"name":"\n","level":"low","currency":"EUR","date":"2026-11-19"}]}`,
		`{"interruptions":[{"from":"2026-11-01T00:00","to":"2026-12-02T00:01","cause":"fault"}]}`,
		`{"market_prices":[{"name":"p","level":"low","currency":"EUR","date":"2026-02-30"}]}`,
		`{"interruptions":[{"from":"2026-11-19T10:00","to":"2026-11-19T11:00","cause":"fault","note":"\u202e"}]}`,
		"{\"storage_classes\":{\"\xff\":\"ssd\"}}", strings.Repeat(" ", 65536) + "{}",
	} {
		w := reportRequest(router, "PUT", "/api/clusters/0/report-settings", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for %.100s; got %d: %s", body, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "sensitive-token") {
			t.Fatal("invalid input echoed")
		}
		var response map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response["error"] == "" {
			t.Errorf("missing safe error: %v %s", err, w.Body)
		}
		if !reflect.DeepEqual(config.Get(), before) {
			t.Fatal("invalid request changed config")
		}
	}
	after, err := os.ReadFile("config.json")
	if err != nil || !bytes.Equal(disk, after) {
		t.Fatal("invalid request modified disk")
	}
	for _, method := range []string{"GET", "PUT"} {
		for _, tc := range []struct {
			idx    string
			status int
		}{{"-1", 400}, {"+1", 400}, {"x", 400}, {"999999999999999999999999", 400}, {"2", 404}} {
			w := reportRequest(router, method, "/api/clusters/"+tc.idx+"/report-settings", `{}`)
			if w.Code != tc.status {
				t.Errorf("%s %s: %d", method, tc.idx, w.Code)
			}
		}
	}
	// Exactly 64 KiB is accepted; the next byte is rejected, even trailing whitespace.
	body := "{}" + strings.Repeat(" ", 65534)
	if w := reportRequest(router, "PUT", "/api/clusters/0/report-settings", body); w.Code != 200 {
		t.Fatalf("64KB body rejected: %s", w.Body)
	}
	if w := reportRequest(router, "PUT", "/api/clusters/0/report-settings", body+" "); w.Code != 400 {
		t.Fatal("oversized body accepted")
	}
}

func TestReportSettingsSaveFailure(t *testing.T) {
	router := reportSettingsTestRouter(t)
	before := config.Get()
	if err := os.Remove("config.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("config.json", 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove("config.json"); err != nil {
			t.Error(err)
		}
	})
	w := reportRequest(router, "PUT", "/api/clusters/0/report-settings", `{"usd_eur":1}`)
	if w.Code != 500 || strings.Contains(w.Body.String(), "rename") || strings.Contains(w.Body.String(), "config.json") {
		t.Fatalf("unsafe error: %d %s", w.Code, w.Body)
	}
	if !reflect.DeepEqual(config.Get(), before) {
		t.Fatal("failed save changed live settings")
	}
}

func TestClusterEditsPreserveReportAndRejectReportInjection(t *testing.T) {
	router := reportSettingsTestRouter(t)
	before := config.Get()
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/clusters", `{"label":"new","url":"https://pve.example","api_token":"new-secret","report":{"usd_eur":1}}`},
		{"PUT", "/api/clusters/1", `{"label":"edit","report":{"usd_eur":1}}`},
		{"POST", "/api/clusters", `{"label":"new","url":"https://pve.example","api_token":"new-secret","report":{"unknown":true}}`},
		{"PUT", "/api/clusters/1", `{"label":"edit"} {}`},
	} {
		w := reportRequest(router, tc.method, tc.path, tc.body)
		if w.Code != 400 || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if !reflect.DeepEqual(config.Get(), before) {
			t.Fatal("cluster request changed report settings")
		}
	}
	w := reportRequest(router, "PUT", "/api/clusters/1", `{"label":"Hosting aggiornato","api_token":""}`)
	if w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	after := config.Get()
	if !reflect.DeepEqual(after.Clusters[1].Report, before.Clusters[1].Report) || after.Clusters[1].APIToken != before.Clusters[1].APIToken {
		t.Fatal("cluster edit changed report or token")
	}
	w = reportRequest(router, "POST", "/api/clusters", `{"label":"new","url":"https://pve.example","api_token":"new-secret"}`)
	if w.Code != 200 || len(config.Get().Clusters) != 3 || config.Get().Clusters[2].Report != nil {
		t.Fatalf("new cluster: %d %s", w.Code, w.Body)
	}
}
