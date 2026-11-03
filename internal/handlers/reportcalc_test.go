package handlers

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestPickTimeframe(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		days float64
		tf   string
		step int64
		err  bool
	}{{0.5, "day", 60, false}, {1, "day", 60, false}, {3, "week", 1800, false}, {7, "week", 1800, false}, {20, "month", 1800, false},
		{30, "month", 1800, false}, {31, "year", 604800, false}, {488, "year", 604800, false}, {489, "", 0, true}, {-1, "", 0, true}}
	for _, c := range cases {
		tf, step, err := pickTimeframe(now, now.Add(-time.Duration(c.days*24*float64(time.Hour))))
		if (err != nil) != c.err || tf != c.tf || step != c.step {
			t.Fatalf("%v days: %s %d %v", c.days, tf, step, err)
		}
	}
}

func TestParseRRDAndStats(t *testing.T) {
	raw := json.RawMessage(`[{"time":0,"cpu":0.1},{"time":1800,"cpu":0.2},{"time":3600,"cpu":null},{"time":5400,"cpu":0.9},{"time":7200}]`)
	s, err := parseRRD(raw, "cpu", "mem")
	if err != nil || len(s.T) != 5 || s.Step != 1800 || !math.IsNaN(s.Cols["cpu"][2]) || !math.IsNaN(s.Cols["mem"][0]) {
		t.Fatalf("%+v %v", s, err)
	}
	st := statOf(s.Cols["cpu"], []float64{0.5, 0.95, math.NaN(), 0.9, math.NaN()}, s.Step, 0.85)
	if st.N != 3 || st.Avg != 0.4 || st.Max != 0.95 || st.P95 == nil || st.OverPct != 33.33 {
		t.Fatalf("%+v", st)
	}
	coarse := statOf([]float64{1, 2, 3}, nil, 604800, 10)
	if coarse.P95 != nil || coarse.Avg != 2 {
		t.Fatalf("weekly resolution must not give a p95: %+v", coarse)
	}
	if e := statOf(nil, nil, 60, 1); e.N != 0 || e.P95 != nil {
		t.Fatal("empty")
	}
	if _, err := parseRRD(json.RawMessage(`nope`), "cpu"); err == nil {
		t.Fatal("bad json")
	}
	c := s.clip(1800, 5400)
	if len(c.T) != 3 || c.T[0] != 1800 {
		t.Fatalf("%v", c.T)
	}
}

func TestPercentile(t *testing.T) {
	v := []float64{}
	for i := 1; i <= 100; i++ {
		v = append(v, float64(i))
	}
	if p := percentile(v, 95); math.Abs(p-95.05) > 0.01 {
		t.Fatal(p)
	}
	if percentile([]float64{7}, 95) != 7 {
		t.Fatal("single")
	}
}

func TestTrendAndExhaustion(t *testing.T) {
	var ts []int64
	var v []float64
	for i := 0; i < 52; i++ { // weekly points, +1 point/week from 60
		ts = append(ts, int64(i)*604800)
		v = append(v, 60+float64(i))
	}
	tr := trendOf(ts, v)
	if !tr.OK || math.Abs(tr.SlopePerDay-1.0/7) > 0.01 || math.Abs(tr.EndValue-111) > 0.1 {
		t.Fatalf("%+v", tr)
	}
	ts2, v2 := ts[:20], make([]float64, 20)
	for i := range v2 {
		v2[i] = 60 + 0.5*float64(i)
	}
	tr2 := trendOf(ts2, v2) // ends at 69.5, +0.0714/day
	d := tr2.daysToReach(85)
	if d == nil || *d < 200 || *d > 230 {
		t.Fatalf("%+v %v", tr2, d)
	}
	if d := tr2.daysToReach(60); d == nil || *d != 0 {
		t.Fatal("already above the limit must give 0")
	}
	flat := trendOf(ts, make([]float64, 52))
	if flat.daysToReach(85) != nil {
		t.Fatal("flat trend never exhausts")
	}
	if trendOf(ts[:5], v[:5]).OK || trendOf([]int64{0, 60, 120, 180, 240, 300, 360, 420, 480}, make([]float64, 9)).OK {
		t.Fatal("too few points or too short a span must not give a trend")
	}
}

func TestDownsample(t *testing.T) {
	ts := make([]int64, 1000)
	v := make([]float64, 1000)
	for i := range ts {
		ts[i], v[i] = int64(i), float64(i%10)
	}
	v[5] = math.NaN()
	p := downsample(ts, v, 100)
	if len(p) != 100 || p[0].T != 9 {
		t.Fatalf("%d %+v", len(p), p[0])
	}
	if len(downsample(nil, nil, 10)) != 0 {
		t.Fatal("empty")
	}
}

func TestClassifyVM(t *testing.T) {
	th := defaultVMThresholds()
	f := func(x float64) *float64 { return &x }
	cases := []struct {
		name string
		e    vmEvidence
		want string
	}{
		{"idle", vmEvidence{Running: true, DataDays: 20, CPUAvg: 0.004, NetAvg: 200, CPUP95: f(0.01), MemP95F: f(0.1)}, "inactive"},
		{"idle but short data", vmEvidence{Running: true, DataDays: 3, CPUAvg: 0.004, NetAvg: 200}, ""},
		{"busy network keeps it active", vmEvidence{Running: true, DataDays: 20, CPUAvg: 0.004, NetAvg: 50000, CPUP95: f(0.02), MemP95F: f(0.6)}, "oversized"},
		{"oversized by memory", vmEvidence{Running: true, DataDays: 28, CPUAvg: 0.3, NetAvg: 9000, CPUP95: f(0.6), MemP95F: f(0.2)}, "oversized"},
		{"oversized by cpu", vmEvidence{Running: true, DataDays: 28, CPUAvg: 0.05, NetAvg: 9000, CPUP95: f(0.08), MemP95F: f(0.7)}, "oversized"},
		{"healthy", vmEvidence{Running: true, DataDays: 28, CPUAvg: 0.4, NetAvg: 9000, CPUP95: f(0.7), MemP95F: f(0.8)}, ""},
		{"stopped never flagged", vmEvidence{Running: false, DataDays: 28, CPUAvg: 0, NetAvg: 0, CPUP95: f(0), MemP95F: f(0)}, ""},
		{"no percentile data", vmEvidence{Running: true, DataDays: 28, CPUAvg: 0.3, NetAvg: 9000}, ""},
		{"borderline not flagged", vmEvidence{Running: true, DataDays: 28, CPUAvg: 0.3, NetAvg: 9000, CPUP95: f(0.10), MemP95F: f(0.25)}, ""},
	}
	for _, c := range cases {
		if got := classifyVM(c.e, th); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// The year RRD mixes resolutions: 6-hour points for the recent part, weekly points before.
func mixedYear() ([]int64, []float64) {
	var ts []int64
	var v []float64
	t := int64(1_700_000_000)
	for i := 0; i < 57; i++ { // 57 weekly points
		ts = append(ts, t)
		v = append(v, 40+float64(i)*0.2)
		t += 604800
	}
	for i := 0; i < 359; i++ { // then 359 six-hour points
		ts = append(ts, t)
		v = append(v, 52+float64(i)*0.02)
		t += 21600
	}
	return ts, v
}

func TestCoverageDaysAndValidFractionOnMixedResolution(t *testing.T) {
	ts, v := mixedYear()
	full := coverageDays(ts, v, medianStep(ts))
	span := float64(ts[len(ts)-1]-ts[0]) / 86400
	if math.Abs(full-span) > 6.5 { // the first point counts one median step
		t.Fatalf("coverage %.1f vs span %.1f", full, span)
	}
	for i := 100; i < 140; i++ { // a gap of 40 six-hour samples = 10 days
		v[i] = math.NaN()
	}
	if got := coverageDays(ts, v, medianStep(ts)); math.Abs((full-got)-10) > 0.6 {
		t.Fatalf("a 10-day hole must remove ~10 days: %.1f -> %.1f", full, got)
	}
	if f := validFraction(v); f < 0.9 || f > 0.95 {
		t.Fatalf("valid fraction %.3f", f)
	}
	lead := append([]float64{math.NaN(), math.NaN(), math.NaN()}, 1, 2, 3, 4)
	if validFraction(lead) != 1 {
		t.Fatal("a node younger than the period must not look like downtime")
	}
	if validFraction([]float64{math.NaN()}) != 0 || validFraction(nil) != 0 {
		t.Fatal("no data")
	}
}

func TestTrendOnMixedResolutionIsNotDominatedByRecentDensity(t *testing.T) {
	ts, v := mixedYear()
	tr := trendOf(ts, v)
	// weekly part: +0.2 per week; six-hour part: +0.02 per 6 h = +0.56 per week; the weekly buckets weigh each
	// week equally (57 + 90/7 = ~70 weeks), so the fit is between the two, closer to the long weekly part.
	perWeek := tr.SlopePerDay * 7
	if !tr.OK || perWeek < 0.2 || perWeek > 0.45 {
		t.Fatalf("slope per week %.3f", perWeek)
	}
}
