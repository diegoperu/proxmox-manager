package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoRequestCapsResponseSize(t *testing.T) {
	big := strings.Repeat("x", maxResponseBytes+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"` + big + `"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "u@pam!t=00000000-0000-0000-0000-000000000000")
	if _, err := c.Get("/anything"); err == nil || !strings.Contains(err.Error(), "troppo grande") {
		t.Errorf("risposta oltre il tetto accettata, err=%v", err)
	}
}

func TestDoRequestAcceptsNormalResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "u@pam!t=00000000-0000-0000-0000-000000000000")
	got, err := c.Get("/x")
	if err != nil || string(got) != `{"ok":true}` {
		t.Errorf("got %s err=%v", got, err)
	}
}
