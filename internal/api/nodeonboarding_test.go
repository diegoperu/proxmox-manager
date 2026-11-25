package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type onboardingTransport func(*http.Request) (*http.Response, error)

func (f onboardingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestNodeOnboardingGETs(t *testing.T) {
	c := NewClient("https://pve.example.invalid", "test-token")
	var path string
	status := 200
	c.httpClient.Transport = onboardingTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.Body != nil {
			t.Fatalf("onboarding wrote to Proxmox: %s", r.Method)
		}
		path = r.URL.EscapedPath()
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
	})
	tests := []struct {
		name, path string
		get        func() (json.RawMessage, error)
	}{
		{"replication", "/api2/json/cluster/replication", c.GetReplication},
		{"rules", "/api2/json/cluster/ha/rules", c.GetHARules},
		{"groups", "/api2/json/cluster/ha/groups", c.GetHAGroups},
		{"apt", "/api2/json/nodes/n1/apt/versions", func() (json.RawMessage, error) { return c.GetNodeAPTVersions("n1") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := tc.get()
			if err != nil || string(raw) != "[]" || path != tc.path {
				t.Fatalf("%s %s %v", path, raw, err)
			}
		})
	}
	for _, code := range []int{404, 500} {
		status = code
		raw, err := c.GetHAGroups()
		if err != nil || string(raw) != "[]" {
			t.Fatalf("removed groups endpoint %d: %s %v", code, raw, err)
		}
	}
	for _, code := range []int{401, 403, 502} {
		status = code
		if _, err := c.GetHAGroups(); err == nil {
			t.Fatalf("masked groups error %d", code)
		}
	}
	status = 500
	if _, err := c.GetHARules(); err == nil {
		t.Fatal("masked rules error")
	}
}
