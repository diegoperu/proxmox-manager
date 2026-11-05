package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxmox-manager/internal/config"
)

func modelInv() GPUInventory {
	return GPUInventory{Nodes: []GPUNode{{Node: "n1", Online: true, Cards: []GPUCard{
		{Addr: "0000:01:00", Name: "NVIDIA Corporation GA107GL [A2 / A16]", MemoryGB: 16, DefaultMemory: 16, Status: "free", Mappings: []string{"M1"}, Uses: []GPUUse{}},
		{Addr: "0000:02:00", Name: "NVIDIA Corporation Mystery", Status: "free", Mappings: []string{"M2"}, Uses: []GPUUse{}},
	}}}}
}

func TestWithGPUModelsAppliesOnACopy(t *testing.T) {
	inv := modelInv()
	out := withGPUModels(inv, map[string]config.GPUModel{
		"NVIDIA Corporation GA107GL [A2 / A16]": {Name: "NVIDIA A2", MemoryGB: 24},
		"NVIDIA Corporation Mystery":            {MemoryGB: 48}, // size only: the name stays
	})
	a, m := out.Nodes[0].Cards[0], out.Nodes[0].Cards[1]
	if a.DisplayName != "NVIDIA A2" || a.MemoryGB != 24 || !a.MemoryCustom || a.DefaultMemory != 16 || a.Model != a.Name {
		t.Fatalf("%+v", a)
	}
	if m.DisplayName != "" || m.MemoryGB != 48 || !m.MemoryCustom || m.DefaultMemory != 0 {
		t.Fatalf("%+v", m)
	}
	// the input (the cached inventory) was not touched
	o := inv.Nodes[0].Cards[0]
	if o.DisplayName != "" || o.MemoryGB != 16 || o.MemoryCustom {
		t.Fatalf("the cached inventory was modified: %+v", o)
	}
	// no corrections: values as they were, Model filled
	plain := withGPUModels(inv, nil)
	if p := plain.Nodes[0].Cards[1]; p.MemoryGB != 0 || p.MemoryCustom || p.Model != "NVIDIA Corporation Mystery" {
		t.Fatalf("%+v", p)
	}
	// a correction that is removed later no longer shows
	again := withGPUModels(out, nil)
	if c := again.Nodes[0].Cards[0]; c.DisplayName != "" || c.MemoryCustom {
		t.Fatalf("stale correction: %+v", c)
	}
}

func TestWithGPUModelsKeepsEmptyListsAsLists(t *testing.T) {
	inv := GPUInventory{Nodes: []GPUNode{{Node: "empty", Online: false, Cards: []GPUCard{}, Groups: []GPUGroupMapping{}}}, Problems: []GPUProblem{}, Mappings: []string{}, Partial: []string{}}
	b, err := json.Marshal(withGPUModels(inv, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cards", "groups", "problems", "mappings", "partial"} {
		if strings.Contains(string(b), `"`+k+`":null`) {
			t.Fatalf("%s is null: %s", k, b)
		}
	}
}

func TestAdviceUsesTheCorrectedNameAndMemory(t *testing.T) {
	inv := withGPUModels(modelInv(), map[string]config.GPUModel{"NVIDIA Corporation Mystery": {Name: "Scheda nuova", MemoryGB: 48}})
	cards := adviceCards(&inv, nil)
	for _, c := range cards {
		if c.Addr == "0000:02:00" && (c.Name != "Scheda nuova" || c.MemoryGB != 48) {
			t.Fatalf("%+v", c)
		}
		if c.Addr == "0000:01:00" && (c.Name != "NVIDIA Corporation GA107GL [A2 / A16]" || c.MemoryGB != 16) {
			t.Fatalf("%+v", c)
		}
	}
}

func TestPutGPUModelRejectsBadInputBeforeAnyCall(t *testing.T) {
	h := &Handler{} // no client: anything that reached Proxmox would panic
	for name, body := range map[string]string{
		"empty":           ``,
		"not json":        `x`,
		"unknown field":   `{"model":"m","name":"a","memory_gb":1,"x":1}`,
		"missing memory":  `{"model":"m","name":"a"}`,
		"no model":        `{"model":"","name":"a","memory_gb":1}`,
		"long name":       `{"model":"m","name":"` + strings.Repeat("a", 65) + `","memory_gb":1}`,
		"control in name": `{"model":"m","name":"a\u001bb","memory_gb":1}`,
		"negative memory": `{"model":"m","name":"a","memory_gb":-1}`,
		"huge memory":     `{"model":"m","name":"a","memory_gb":9999}`,
		"two documents":   `{"model":"m","name":"a","memory_gb":1}{"x":1}`,
		"huge body":       `{"model":"m","name":"` + strings.Repeat("a", 3000) + `","memory_gb":1}`,
	} {
		rec := httptest.NewRecorder()
		h.PutGPUModel(rec, httptest.NewRequest("PUT", "/api/gpu-models", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
