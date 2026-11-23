package handlers

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"proxmox-manager/internal/config"
)

// GET /api/config never returns the console password or a full API token.
func TestGetConfigHidesSecrets(t *testing.T) {
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
	os.WriteFile("config.json", []byte(`{"clusters":[{"label":"c","url":"https://x:8006","api_token":"pmgr@pve!manager=11111111-2222-3333-4444-555555555555","username":"console@pve","password":"segreto-console-123"}]}`), 0o600)
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	New().GetConfig(w, httptest.NewRequest("GET", "/api/config", nil))
	body := w.Body.String()
	if strings.Contains(body, "segreto-console-123") || strings.Contains(body, "11111111-2222-3333-4444") {
		t.Fatalf("secret in GET /api/config: %s", body)
	}
	if config.Get().Clusters[0].Password != "segreto-console-123" {
		t.Fatal("masking the response changed the stored configuration")
	}
}
