package handlers

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
)

// Pure calculations for the reports (no Proxmox, no HTTP): RRD parsing, period -> timeframe, statistics,
// trend and exhaustion forecast, VM classification. Proxmox RRD resolutions (measured on PVE 9.2): day 60 s,
// week and month 30 min; the year timeframe (488 days, the longest history) is NOT uniform: 6-hour points for the last
// ~90 days, then weekly points (checked on the real series: 359 gaps of 21600 s, 57 of 604800 s).

const (
	maxRRDPoints   = 5000
	maxHistoryDays = 488
	fineStepMax    = 1800 // up to this step a percentile is meaningful
)

type rrdSeries struct {
	T    []int64
	Cols map[string][]float64 // NaN = missing
	Step int64
}

// parseRRD reads a `/rrddata` answer keeping only the wanted fields.
func parseRRD(raw json.RawMessage, fields ...string) (rrdSeries, error) {
	var pts []map[string]interface{}
	if err := json.Unmarshal(raw, &pts); err != nil {
		return rrdSeries{}, err
	}
	if len(pts) > maxRRDPoints {
		return rrdSeries{}, errors.New("troppi punti RRD")
	}
	s := rrdSeries{Cols: map[string][]float64{}}
	for _, f := range fields {
		s.Cols[f] = make([]float64, 0, len(pts))
	}
	for _, p := range pts {
		t, ok := p["time"].(float64)
		if !ok {
			continue
		}
		s.T = append(s.T, int64(t))
		for _, f := range fields {
			v, ok := p[f].(float64)
			if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
				v = math.NaN()
			}
			s.Cols[f] = append(s.Cols[f], v)
		}
	}
	s.Step = medianStep(s.T)
	return s, nil
}

func medianStep(t []int64) int64 {
	if len(t) < 2 {
		return 0
	}
	d := make([]int64, 0, len(t)-1)
	for i := 1; i < len(t); i++ {
		if t[i] > t[i-1] {
			d = append(d, t[i]-t[i-1])
		}
	}
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[len(d)/2]
}

// clip keeps the points with from <= t <= to.
func (s rrdSeries) clip(from, to int64) rrdSeries {
	out := rrdSeries{Cols: map[string][]float64{}, Step: s.Step}
	for k := range s.Cols {
		out.Cols[k] = nil
	}
	for i, t := range s.T {
		if t < from || t > to {
			continue
		}
		out.T = append(out.T, t)
		for k, c := range s.Cols {
			out.Cols[k] = append(out.Cols[k], c[i])
		}
	}
	return out
}

// pickTimeframe returns the smallest RRD timeframe that still covers `from`, and its step in seconds.
func pickTimeframe(now, from time.Time) (string, int64, error) {
	age := now.Sub(from)
	switch {
	case age < 0:
		return "", 0, errors.New("la data iniziale e' nel futuro")
	case age > maxHistoryDays*24*time.Hour:
		return "", 0, errors.New("oltre lo storico di Proxmox (488 giorni)")
	case age <= 24*time.Hour:
		return "day", 60, nil
	case age <= 7*24*time.Hour:
		return "week", 1800, nil
	case age <= 30*24*time.Hour:
		return "month", 1800, nil
	}
	return "year", 604800, nil
}

// coverageDays is the time the valid samples stand for: each valid sample counts for the gap from the previous point
// (the median step for the first one), so mixed resolutions are handled correctly.
func coverageDays(t []int64, v []float64, medianStep int64) float64 {
	var sec int64
	for i := range t {
		if i >= len(v) || math.IsNaN(v[i]) {
			continue
		}
		if i == 0 {
			sec += medianStep
		} else if g := t[i] - t[i-1]; g > 0 {
			sec += g
		}
	}
	return float64(sec) / 86400
}

// validFraction is the share of the series' points that have a value, counted from the first valid one (a node
// younger than the period must not look like downtime for the time before it existed).
func validFraction(v []float64) float64 {
	first := -1
	for i, x := range v {
		if !math.IsNaN(x) {
			first = i
			break
		}
	}
	if first < 0 {
		return 0
	}
	n, ok := 0, 0
	for _, x := range v[first:] {
		n++
		if !math.IsNaN(x) {
			ok++
		}
	}
	return float64(ok) / float64(n)
}

// weeklyBuckets averages the series into 7-day buckets, so a trend is not dominated by the dense recent part of
// the year RRD.
func weeklyBuckets(t []int64, v []float64) ([]int64, []float64) {
	type acc struct {
		sum float64
		n   int
		t   int64
	}
	m := map[int64]*acc{}
	for i := range t {
		if i >= len(v) || math.IsNaN(v[i]) {
			continue
		}
		k := t[i] / (7 * 86400)
		a := m[k]
		if a == nil {
			a = &acc{}
			m[k] = a
		}
		a.sum += v[i]
		a.n++
		if t[i] > a.t {
			a.t = t[i]
		}
	}
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	ts := make([]int64, 0, len(keys))
	vs := make([]float64, 0, len(keys))
	for _, k := range keys {
		ts = append(ts, m[k].t)
		vs = append(vs, m[k].sum/float64(m[k].n))
	}
	return ts, vs
}

// valid returns the non-missing values.
func valid(v []float64) []float64 {
	out := make([]float64, 0, len(v))
	for _, x := range v {
		if !math.IsNaN(x) {
			out = append(out, x)
		}
	}
	return out
}

// Stat summarises a series. P95 is nil when the resolution is too coarse for a percentile.
type Stat struct {
	N       int      `json:"n"`
	Avg     float64  `json:"avg"`
	Max     float64  `json:"max"`
	P95     *float64 `json:"p95"`
	OverPct float64  `json:"over_pct"` // % of samples at or above the threshold
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// statOf: v are the average values; maxv (optional, same length) are the MAX consolidation used for Max.
func statOf(v, maxv []float64, step int64, threshold float64) Stat {
	vv := valid(v)
	st := Stat{N: len(vv)}
	if len(vv) == 0 {
		return st
	}
	var sum float64
	over := 0
	for _, x := range vv {
		sum += x
		if x >= threshold {
			over++
		}
		if x > st.Max {
			st.Max = x
		}
	}
	for _, x := range valid(maxv) {
		if x > st.Max {
			st.Max = x
		}
	}
	st.Avg = sum / float64(len(vv))
	st.OverPct = float64(over) / float64(len(vv)) * 100
	if step > 0 && step <= fineStepMax {
		p := percentile(vv, 95)
		st.P95 = &p
	}
	st.Avg, st.Max, st.OverPct = round2(st.Avg), round2(st.Max), round2(st.OverPct)
	if st.P95 != nil {
		*st.P95 = round2(*st.P95)
	}
	return st
}

func percentile(vv []float64, p float64) float64 {
	c := append([]float64(nil), vv...)
	sort.Float64s(c)
	if len(c) == 1 {
		return c[0]
	}
	rank := p / 100 * float64(len(c)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	return c[lo] + (c[hi]-c[lo])*(rank-float64(lo))
}

// Trend is a least-squares line over the valid samples.
type Trend struct {
	SlopePerDay float64 `json:"slope_per_day"`
	EndValue    float64 `json:"end_value"` // fitted value at the last timestamp
	OK          bool    `json:"ok"`
}

// trendOf fits weekly averages: at least 8 weeks with data spanning at least 7 days.
func trendOf(t []int64, v []float64) Trend {
	t, v = weeklyBuckets(t, v)
	var xs, ys []float64
	for i := range t {
		if i < len(v) && !math.IsNaN(v[i]) {
			xs = append(xs, float64(t[i]))
			ys = append(ys, v[i])
		}
	}
	if len(xs) < 8 || xs[len(xs)-1]-xs[0] < 7*86400 {
		return Trend{}
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(xs))
	var num, den float64
	for i := range xs {
		num += (xs[i] - mx) * (ys[i] - my)
		den += (xs[i] - mx) * (xs[i] - mx)
	}
	if den == 0 {
		return Trend{}
	}
	slope := num / den // per second
	end := my + slope*(xs[len(xs)-1]-mx)
	return Trend{SlopePerDay: math.Round(slope*86400*1e4) / 1e4, EndValue: round2(end), OK: true}
}

// daysToReach: days until the fitted line reaches limit; nil when flat/decreasing or beyond ten years.
func (t Trend) daysToReach(limit float64) *int {
	if !t.OK {
		return nil
	}
	if t.EndValue >= limit {
		z := 0
		return &z
	}
	if t.SlopePerDay <= 0.0001 {
		return nil
	}
	d := (limit - t.EndValue) / t.SlopePerDay
	if d > 3650 {
		return nil
	}
	n := int(math.Ceil(d))
	return &n
}

type Point struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

// downsample averages the valid samples into at most max buckets.
func downsample(t []int64, v []float64, max int) []Point {
	out := []Point{}
	if len(t) == 0 || max <= 0 {
		return out
	}
	n := len(t)
	per := (n + max - 1) / max
	for i := 0; i < n; i += per {
		end := i + per
		if end > n {
			end = n
		}
		var sum float64
		c := 0
		for j := i; j < end; j++ {
			if j < len(v) && !math.IsNaN(v[j]) {
				sum += v[j]
				c++
			}
		}
		if c > 0 {
			out = append(out, Point{T: t[end-1], V: round2(sum / float64(c))})
		}
	}
	return out
}

// VMThresholds are the report-level, non-persistent knobs (D32).
type VMThresholds struct {
	InactiveCPU      float64 // fraction of allocated vCPU, default 0.02
	InactiveNetBps   float64 // bytes/s (in+out), default 1024
	InactiveMinDays  float64
	OversizedMem     float64 // memory p95 / assigned, default 0.25
	OversizedCPU     float64 // cpu p95 (fraction of vCPU), default 0.10
	OversizedMinDays float64
}

func defaultVMThresholds() VMThresholds {
	return VMThresholds{InactiveCPU: 0.02, InactiveNetBps: 1024, InactiveMinDays: 7, OversizedMem: 0.25, OversizedCPU: 0.10, OversizedMinDays: 14}
}

type vmEvidence struct {
	Running  bool
	DataDays float64
	CPUAvg   float64  // fraction
	NetAvg   float64  // bytes/s
	CPUP95   *float64 // fraction
	MemP95F  *float64 // memory used p95 / maxmem
}

// classifyVM: "inactive" wins over "oversized"; stopped VMs and VMs without enough data are never flagged.
func classifyVM(e vmEvidence, th VMThresholds) string {
	if !e.Running {
		return ""
	}
	if e.DataDays >= th.InactiveMinDays && e.CPUAvg < th.InactiveCPU && e.NetAvg < th.InactiveNetBps {
		return "inactive"
	}
	if e.DataDays >= th.OversizedMinDays && e.CPUP95 != nil && e.MemP95F != nil &&
		(*e.MemP95F < th.OversizedMem || *e.CPUP95 < th.OversizedCPU) {
		return "oversized"
	}
	return ""
}
