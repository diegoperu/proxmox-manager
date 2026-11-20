package config

import (
	"os"
	"testing"
)

func TestUpdateCheckDefaultAndCopy(t *testing.T) {
	withTempConfig(t)
	for _, tc := range []struct {
		body    string
		enabled bool
	}{
		{`{}`, true}, {`{"update_check":true}`, true}, {`{"update_check":false}`, false},
	} {
		if err := os.WriteFile(cfgPath, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Load(); err != nil {
			t.Fatal(err)
		}
		got := Get()
		if got.UpdateCheckEnabled() != tc.enabled {
			t.Fatalf("%s: %v", tc.body, got.UpdateCheckEnabled())
		}
		if got.UpdateCheck != nil {
			*got.UpdateCheck = !*got.UpdateCheck
			if Get().UpdateCheckEnabled() != tc.enabled {
				t.Fatal("Get aliases update_check")
			}
		}
	}
	if err := os.WriteFile(cfgPath, []byte(`{"update_check":"false"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Load(); err == nil {
		t.Fatal("string boolean accepted")
	}
}
