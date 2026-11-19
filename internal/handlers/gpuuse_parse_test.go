package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseGPUUse(t *testing.T) {
	const a = "00000000:01:00.0, NVIDIA A100, 45, 4096, 81920, 55, 120.5"
	const b = "0000:02:00.1, NVIDIA A2, 0, 0, 16384, [Not Supported], [N/A]"
	cases := []struct {
		name, input     string
		count, rejected int
		firstName       string
	}{
		{"A100", a, 1, 0, "NVIDIA A100"},
		{"A2 unknown", b, 1, 0, "NVIDIA A2"},
		{"multiple CRLF", a + "\r\n" + b + "\r\n", 2, 0, "NVIDIA A100"},
		{"MIG spaces", " 00000000:0a:00.7 , MIG 1g.5gb , N/A , , 5000 , 0 , 1 ", 1, 0, "MIG 1g.5gb"},
		{"used over total", "0000:01:00.0, X, 0, 2, 1, 0, 0", 0, 1, ""},
		{"wrong fields", a + ", extra", 0, 1, ""},
		{"invalid bus", "0000:01:00.8, X, 0, 0, 1, 0, 0", 0, 1, ""},
		{"hostile name", "0000:01:00.0, \x01é<script>" + strings.Repeat("x", 500) + ", 0, 0, 1, 0, 0", 1, 0, "<script>" + strings.Repeat("x", 56)},
		{"empty name", "0000:01:00.0, é\x01, 0, 0, 1, 0, 0", 1, 0, "GPU"},
		{"20 lines", strings.Repeat(a+"\n", 20), 8, 12, "NVIDIA A100"},
		{"100 KB", strings.Repeat("x", 100*1024), 0, 1, ""},
		{"empty", "", 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cards, rejected := parseGPUUse(tc.input)
			if len(cards) != tc.count || rejected != tc.rejected {
				t.Fatalf("cards=%d rejected=%d", len(cards), rejected)
			}
			if len(cards) > 0 && cards[0].Name != tc.firstName {
				t.Errorf("name=%q", cards[0].Name)
			}
			if tc.name == "A2 unknown" && (cards[0].PowerW != nil || cards[0].TempC != nil) {
				t.Errorf("unknown values: %+v", cards[0])
			}
		})
	}
	for _, value := range []string{"101", "-1", "NaN", "Inf", "+Inf", "0x1p2", "1e999"} {
		t.Run("invalid "+value, func(t *testing.T) {
			cards, rejected := parseGPUUse("0000:01:00.0, X, " + value + ", 0, 1, 0, 0")
			if len(cards) != 0 || rejected != 1 {
				t.Fatalf("cards=%v rejected=%d", cards, rejected)
			}
		})
	}
	for _, line := range []string{"0, 1048577, 1048577, 0, 0", "0, 0, 1048577, 0, 0", "0, 0, 1, 151, 0", "0, 0, 1, 0, 2001"} {
		_, rejected := parseGPUUse("0000:01:00.0, X, " + line)
		if rejected != 1 {
			t.Errorf("accepted %q", line)
		}
	}
	cards, _ := parseGPUUse(b)
	data, err := json.Marshal(cards[0])
	if err != nil || !strings.Contains(string(data), `"power_w":null`) {
		t.Errorf("json=%s err=%v", data, err)
	}
}

func TestGPUUseExitState(t *testing.T) {
	for code, want := range map[int]string{0: "ok", 3: "no_driver", 4: "driver_error", 2: "error", 124: "error", -1: "error"} {
		if got := gpuUseExitState(code); got != want {
			t.Errorf("%d: %q", code, got)
		}
	}
}
