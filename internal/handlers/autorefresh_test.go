package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"proxmox-manager/internal/config"
)

func TestSaveConfigRefresh(t *testing.T) {
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
	h := &Handler{}
	for _, tc := range []struct {
		body         string
		status, want int
	}{
		{`{"refresh_seconds":15}`, http.StatusOK, 15},
		{`{"theme":"dark","refresh_seconds":42}`, http.StatusBadRequest, 15},
		{`{"density":"compact","unknown":true}`, http.StatusOK, 15},
		{`{"refresh_seconds":0}`, http.StatusOK, 0},
		{`{"refresh_seconds":300}`, http.StatusOK, 300},
	} {
		before := config.Get()
		w := httptest.NewRecorder()
		h.SaveConfig(w, httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("%s: status %d: %s", tc.body, w.Code, w.Body.String())
		}
		if got := config.Get(); got.RefreshSeconds != tc.want || (tc.status == http.StatusBadRequest && got.Theme != before.Theme) {
			t.Fatalf("%s: %+v", tc.body, got)
		}
		if err := config.Load(); err != nil {
			t.Fatal(err)
		}
		if got := config.Get().RefreshSeconds; got != tc.want {
			t.Fatalf("%s persisted %d", tc.body, got)
		}
	}
	w := httptest.NewRecorder()
	h.GetConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if !strings.Contains(w.Body.String(), `"refresh_seconds":300`) {
		t.Fatal(w.Body.String())
	}
}
