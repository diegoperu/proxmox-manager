package handlers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"proxmox-manager/internal/config"
)

func TestNetworkSettingsAPI(t *testing.T) {
	base := reportSettingsTestRouter(t)
	h := &Handler{}
	r := chi.NewRouter()
	r.Mount("/legacy", base)
	r.Get("/api/clusters/{idx}/network-settings", h.GetNetworkSettings)
	r.Put("/api/clusters/{idx}/network-settings", h.PutNetworkSettings)
	r.Put("/api/clusters/{idx}", h.UpdateCluster)
	path := "/api/clusters/0/network-settings"
	w := reportRequest(r, "GET", path, "")
	if w.Code != 200 || w.Body.String() != `{"networks":[],"dns":[],"search_domain":"","manager_from":[],"snippet_profile":"standard"}`+"\n" {
		t.Fatalf("empty %d %s", w.Code, w.Body)
	}
	body := `{"networks":[{"cidr":"10.75.4.0/24","gateway":"10.75.4.254"}],"dns":["2001:0DB8::1"],"search_domain":"EXAMPLE.COM"}`
	if w = reportRequest(r, "PUT", path, body); w.Code != 200 {
		t.Fatalf("put %d %s", w.Code, w.Body)
	}
	want := config.Get().Clusters[0].Network
	if want.SearchDomain != "example.com" || want.DNS[0] != "2001:db8::1" {
		t.Fatal(want)
	}
	copy := config.Get()
	copy.Clusters[0].Network.Networks[0].Gateway = "changed"
	copy.Clusters[0].Network.DNS[0] = "changed"
	if !reflect.DeepEqual(config.Get().Clusters[0].Network, want) {
		t.Fatal("Get alias")
	}
	if err := config.Load(); err != nil || !reflect.DeepEqual(config.Get().Clusters[0].Network, want) {
		t.Fatalf("reload %v", err)
	}
	if w = reportRequest(r, "PUT", "/api/clusters/0", `{"label":"Changed"}`); w.Code != 200 || !reflect.DeepEqual(config.Get().Clusters[0].Network, want) {
		t.Fatalf("cluster update %d %s", w.Code, w.Body)
	}
	if w = reportRequest(r, "PUT", "/api/clusters/0", `{"network":{}}`); w.Code != 400 {
		t.Fatal("network injection accepted")
	}
	for _, invalid := range []string{`null`, `{} {}`, `{"unknown":1}`, `{"networks":[{"cidr":"10.0.0.1/24","gateway":"10.0.0.2"}]}`, `{"networks":[{"cidr":"10.0.0.0/24","gateway":"10.0.1.1"}]}`, `{"dns":["bad"]}`, `{"dns":["1.1.1.1","1.1.1.1"]}`, `{"dns":["1.1.1.1","2.2.2.2","3.3.3.3","4.4.4.4"]}`, `{"search_domain":"bad_domain"}`, strings.Repeat(" ", 16<<10) + `{}`} {
		if w = reportRequest(r, "PUT", path, invalid); w.Code != 400 {
			t.Errorf("invalid accepted: %d", w.Code)
		}
	}
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/clusters/x/network-settings", 400}, {"/api/clusters/99/network-settings", 404}} {
		if w = reportRequest(r, "GET", tc.path, ""); w.Code != tc.status {
			t.Errorf("index: %d", w.Code)
		}
	}
	w = reportRequest(r, "GET", path, "")
	var got config.NetworkSettings
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(&got, want) {
		t.Fatalf("GET %v %+v", err, got)
	}
}
