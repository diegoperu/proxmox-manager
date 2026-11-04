package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

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

func withTempConfig(t *testing.T) {
	t.Helper()
	old, oldCur := cfgPath, current
	cfgPath = t.TempDir() + "/config.json"
	current = Config{Clusters: []ClusterConfig{{Label: "X", URL: "https://x", APIToken: "u@pam!t=secret", Default: true}}}
	t.Cleanup(func() { cfgPath, current = old, oldCur })
}

func TestGPUModelSetReplaceRemoveAndPersist(t *testing.T) {
	withTempConfig(t)
	key := "NVIDIA Corporation GA107GL [A2 / A16]"
	if err := SetGPUModel(key, GPUModel{Name: "  NVIDIA A2  ", MemoryGB: 16}); err != nil {
		t.Fatal(err)
	}
	if got := GPUModels()[key]; got.Name != "NVIDIA A2" || got.MemoryGB != 16 {
		t.Fatalf("%+v", got)
	}
	// persisted in the file, token untouched
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), `"gpu_models"`) || !strings.Contains(string(data), "u@pam!t=secret") {
		t.Fatalf("file: %s", data)
	}
	if err := SetGPUModel(key, GPUModel{MemoryGB: 24}); err != nil || GPUModels()[key].Name != "" || GPUModels()[key].MemoryGB != 24 {
		t.Fatalf("replace: %v %+v", err, GPUModels())
	}
	// empty name and 0 GB = back to the default: the entry disappears
	if err := SetGPUModel(key, GPUModel{}); err != nil || len(GPUModels()) != 0 {
		t.Fatalf("remove: %v %+v", err, GPUModels())
	}
	data, _ = os.ReadFile(cfgPath)
	if strings.Contains(string(data), "gpu_models") {
		t.Fatalf("empty map still written: %s", data)
	}
}

func TestGPUModelValidation(t *testing.T) {
	withTempConfig(t)
	long := strings.Repeat("a", 161)
	for name, c := range map[string]struct {
		key string
		m   GPUModel
	}{
		"empty key":      {"", GPUModel{Name: "x"}},
		"long key":       {long, GPUModel{Name: "x"}},
		"control in key": {"a\x00b", GPUModel{Name: "x"}},
		"newline key":    {"a\nb", GPUModel{Name: "x"}},
		"long name":      {"k", GPUModel{Name: strings.Repeat("n", 65)}},
		"control name":   {"k", GPUModel{Name: "a\x1b[31m"}},
		"bidi name":      {"k", GPUModel{Name: "a‮b"}},
		"negative GB":    {"k", GPUModel{MemoryGB: -1}},
		"huge GB":        {"k", GPUModel{MemoryGB: 513}},
		"bad utf8":       {"k", GPUModel{Name: "a\xffb"}},
	} {
		if err := SetGPUModel(c.key, c.m); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(GPUModels()) != 0 {
		t.Fatalf("rejected values were stored: %+v", GPUModels())
	}
	// accents, brackets and slashes are fine
	if err := SetGPUModel("NVIDIA [A2 / A16]", GPUModel{Name: "Scheda perche' e' cosi' — A2", MemoryGB: 512}); err != nil {
		t.Fatal(err)
	}
}

func TestGPUModelLimitAndSaveFailureRollsBack(t *testing.T) {
	withTempConfig(t)
	for i := 0; i < gpuModelsMax; i++ {
		if err := SetGPUModel(fmt.Sprintf("m%d", i), GPUModel{MemoryGB: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetGPUModel("one-too-many", GPUModel{MemoryGB: 1}); err == nil {
		t.Fatal("limit not enforced")
	}
	// an existing model can still be edited at the limit
	if err := SetGPUModel("m0", GPUModel{MemoryGB: 2}); err != nil {
		t.Fatal(err)
	}
	// the file cannot be written: the in-memory value is restored
	cfgPath = t.TempDir() + "/no/such/dir/config.json"
	if err := SetGPUModel("m0", GPUModel{MemoryGB: 99}); err == nil {
		t.Fatal("write error ignored")
	}
	if GPUModels()["m0"].MemoryGB != 2 {
		t.Fatalf("not restored: %+v", GPUModels()["m0"])
	}
	if err := SetGPUModel("m1", GPUModel{}); err == nil || GPUModels()["m1"].MemoryGB != 1 {
		t.Fatalf("removal not rolled back: %v %+v", err, GPUModels()["m1"])
	}
}

func TestGetReturnsACopyOfGPUModels(t *testing.T) {
	withTempConfig(t)
	_ = SetGPUModel("k", GPUModel{MemoryGB: 8})
	c := Get()
	c.GPUModels["k"] = GPUModel{MemoryGB: 999}
	delete(c.GPUModels, "k")
	if GPUModels()["k"].MemoryGB != 8 {
		t.Fatal("Get() shares the map with the live config")
	}
}
