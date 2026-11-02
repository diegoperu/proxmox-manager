package handlers

import "testing"

func pts(from int64, step int64, cpus ...float64) []topPoint {
	out := make([]topPoint, len(cpus))
	for i, c := range cpus {
		out[i] = topPoint{T: from + int64(i)*step, CPU: c, Mem: 0.5}
	}
	return out
}

func TestPickTopHours(t *testing.T) {
	for in, want := range map[string]int{"6": 6, "12": 12, "24": 24, "": 6, "7": 6, "-1": 6, "abc": 6, "24 ": 6, "999999999999999999999": 6} {
		if got := pickTopHours(in); got != want {
			t.Errorf("pickTopHours(%q)=%d, want %d", in, got, want)
		}
	}
}

func TestRankTopOrdersByCoresUsedNotPercent(t *testing.T) {
	cands := []topCandidate{
		{Type: "qemu", Node: "n1", Name: "small", VMID: 101, Cores: 1, Points: pts(100, 60, 0.9, 0.9, 0.9)}, // 0.9 core
		{Type: "qemu", Node: "n1", Name: "big", VMID: 102, Cores: 16, Points: pts(100, 60, 0.3, 0.3, 0.3)},  // 4.8 core
	}
	got := rankTop(cands, 0, 10)
	if len(got) != 2 || got[0].Name != "big" || got[1].Name != "small" {
		t.Fatalf("unexpected order: %+v", got)
	}
	if got[0].Work != 4.8 || got[0].AvgCPU != 30 || got[0].PeakCPU != 30 {
		t.Errorf("wrong figures: %+v", got[0])
	}
}

func TestRankTopWindowAndFilters(t *testing.T) {
	cands := []topCandidate{
		{Name: "old-burst", VMID: 1, Cores: 2, Points: pts(0, 10, 1, 1, 1, 0.0, 0.0)},             // lavoro solo prima della finestra
		{Name: "idle", VMID: 2, Cores: 2, Points: pts(100, 10, 0, 0, 0)},                          // nessun lavoro
		{Name: "nocores", VMID: 3, Cores: 0, Points: pts(100, 10, 0.5)},                           // core ignoti
		{Name: "nodata", VMID: 4, Cores: 2},                                                       // nessun campione
		{Name: "ok", VMID: 5, Cores: 2, Points: append(pts(0, 10, 1), pts(100, 10, 0.2, 0.4)...)}, // il campione a t=0 e' fuori
	}
	got := rankTop(cands, 50, 10)
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("unexpected: %+v", got)
	}
	if len(got[0].Series) != 2 || got[0].AvgCPU != 30 || got[0].PeakCPU != 40 {
		t.Errorf("window not applied: %+v", got[0])
	}
}

func TestRankTopLimitAndTieBreak(t *testing.T) {
	var cands []topCandidate
	for i := 0; i < 15; i++ {
		cands = append(cands, topCandidate{Name: "vm", VMID: 200 - i, Cores: 1, Points: pts(100, 10, 0.5)})
	}
	cands = append(cands, topCandidate{Name: "spiky", VMID: 999, Cores: 1, Points: pts(100, 10, 0.5, 0.5)})
	got := rankTop(cands, 0, 10)
	if len(got) != 10 {
		t.Fatalf("limit not applied: %d", len(got))
	}
	// stesso lavoro: ordine stabile per VMID crescente
	for i := 1; i < len(got); i++ {
		if got[i-1].Work == got[i].Work && got[i-1].PeakCPU == got[i].PeakCPU && got[i-1].VMID > got[i].VMID {
			t.Errorf("tie-break not by VMID at %d: %+v %+v", i, got[i-1], got[i])
		}
	}
}

func TestRankTopMemUnknown(t *testing.T) {
	c := topCandidate{Name: "m", VMID: 1, Cores: 1, Points: []topPoint{{T: 10, CPU: 0.5, Mem: -1}}}
	got := rankTop([]topCandidate{c}, 0, 10)
	if len(got) != 1 || got[0].MemPct != -1 {
		t.Fatalf("mem should stay unknown: %+v", got)
	}
}

func TestRankTopHotPctSeparatesSpikesFromSustainedLoad(t *testing.T) {
	spike := make([]float64, 20)
	spike[7] = 1.0 // un solo campione su 20 oltre la soglia
	sustained := make([]float64, 20)
	for i := range sustained {
		sustained[i] = 0.9 // sempre oltre la soglia
	}
	got := rankTop([]topCandidate{
		{Name: "spike", VMID: 1, Cores: 1, Points: pts(100, 10, spike...)},
		{Name: "sustained", VMID: 2, Cores: 1, Points: pts(100, 10, sustained...)},
	}, 0, 10)
	by := map[string]TopVM{}
	for _, g := range got {
		by[g.Name] = g
	}
	if by["spike"].HotPct != 5 || by["spike"].PeakCPU != 100 {
		t.Errorf("spike: %+v", by["spike"])
	}
	if by["sustained"].HotPct != 100 {
		t.Errorf("sustained: %+v", by["sustained"])
	}
	// il limite e' incluso: esattamente 0.85 conta come oltre soglia
	edge := rankTop([]topCandidate{{Name: "edge", VMID: 3, Cores: 1, Points: pts(100, 10, 0.85, 0.5)}}, 0, 10)
	if len(edge) != 1 || edge[0].HotPct != 50 {
		t.Errorf("edge: %+v", edge)
	}
}
