package config

import "testing"

func TestValidTheme(t *testing.T) {
	for _, ok := range []string{"", "dark", "light", "eva01", "catppuccino", "spiderman"} {
		if !ValidTheme(ok) {
			t.Errorf("theme %q should be valid", ok)
		}
	}
	for _, bad := range []string{"DARK", "dark light", `x" onload="1`, "../x", "catpuccino", " "} {
		if ValidTheme(bad) {
			t.Errorf("theme %q should be rejected", bad)
		}
	}
}

func TestValidDensity(t *testing.T) {
	for _, ok := range []string{"", "compact"} {
		if !ValidDensity(ok) {
			t.Errorf("density %q should be valid", ok)
		}
	}
	for _, bad := range []string{"Compact", "comfortable", "compact dark", `"`, "../x"} {
		if ValidDensity(bad) {
			t.Errorf("density %q should be rejected", bad)
		}
	}
}
