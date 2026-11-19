package handlers

import (
	"math"
	"testing"
	"time"

	"proxmox-manager/internal/config"
)

func v3ReliabilityTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func v3AssertPct(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > .001 {
		t.Fatalf("percentage = %v, want %.2f", got, want)
	}
}

func TestV3AvailabilityFirstDataAndPeriodClipping(t *testing.T) {
	from := v3ReliabilityTime(t, "2026-01-01T00:00:00Z")
	to := from.Add(6*time.Hour - time.Second)
	series := rrdSeries{T: []int64{from.Unix(), from.Add(time.Hour).Unix(), from.Add(2 * time.Hour).Unix(), from.Add(3 * time.Hour).Unix(), from.Add(4 * time.Hour).Unix(), from.Add(5 * time.Hour).Unix()}, Step: 3600,
		Cols: map[string][]float64{"cpu": {math.NaN(), math.NaN(), .2, math.NaN(), .3, .4}}}
	got := calculateV3Availability([]rrdSeries{series}, nil, from, to)
	v3AssertPct(t, got.TotalPct, 75)
	v3AssertPct(t, got.NetPct, 75)
	if len(got.Months) != 1 || got.Months[0].Month != "2026-01" {
		t.Fatalf("months = %+v", got.Months)
	}
	// A node with earlier valid data remains observed during this entirely missing subperiod.
	got = calculateV3Availability([]rrdSeries{series}, nil, from.Add(3*time.Hour), from.Add(4*time.Hour-time.Second))
	v3AssertPct(t, got.TotalPct, 0)
	unknown := calculateV3Availability([]rrdSeries{{T: series.T, Step: 3600, Cols: map[string][]float64{}}}, nil, from, to)
	if unknown.TotalPct != nil || unknown.NetPct != nil {
		t.Fatalf("missing node data = %+v", unknown)
	}
}

func TestV3AvailabilityExcludedOverlapAcrossMonth(t *testing.T) {
	from := v3ReliabilityTime(t, "2026-01-31T22:00:00Z")
	to := from.Add(4*time.Hour - time.Second)
	s := rrdSeries{T: []int64{from.Unix(), from.Add(time.Hour).Unix(), from.Add(2 * time.Hour).Unix(), from.Add(3 * time.Hour).Unix()}, Step: 3600,
		Cols: map[string][]float64{"cpu": {.1, math.NaN(), math.NaN(), .2}}}
	interruptions := []config.Interruption{
		{From: "2026-01-31T23:00:00Z", To: "2026-02-01T00:30:00Z", Cause: "external", Note: "private annotation"},
		{From: "2026-02-01T00:00:00Z", To: "2026-02-01T01:00:00Z", Cause: "maintenance"},
		{From: "2026-01-31T22:00:00Z", To: "2026-01-31T23:00:00Z", Cause: "fault"},
	}
	got := calculateV3Availability([]rrdSeries{s}, interruptions, from, to)
	v3AssertPct(t, got.TotalPct, 50)
	v3AssertPct(t, got.NetPct, 100)
	if len(got.Months) != 2 || len(got.Interruptions) != 4 {
		t.Fatalf("month split = %+v", got)
	}
	for _, m := range got.Months {
		v3AssertPct(t, m.TotalPct, 50)
		v3AssertPct(t, m.NetPct, 100)
	}
	var hours float64
	for _, annotation := range got.Interruptions {
		if annotation.Cause == "external" {
			hours += annotation.Hours
		}
	}
	if hours != 1.5 {
		t.Fatalf("external hours = %v", hours)
	}
}

func TestV3AvailabilityMixedStepsAndDuplicateTimes(t *testing.T) {
	from := v3ReliabilityTime(t, "2026-01-01T00:00:00Z")
	s := rrdSeries{T: []int64{from.Unix(), from.Add(6 * time.Hour).Unix(), from.Add(6 * time.Hour).Unix(), from.Add(3 * time.Hour).Unix(), from.Add(12 * time.Hour).Unix(), from.Add(36 * time.Hour).Unix()}, Step: 21600,
		Cols: map[string][]float64{"cpu": {.1, math.NaN(), .5, .5, .2, math.NaN()}}}
	got := calculateV3Availability([]rrdSeries{s}, nil, from, from.Add(60*time.Hour-time.Second))
	// 6 valid hours, 6 missing, 24 valid, 24 missing. Duplicate/backwards values add nothing.
	v3AssertPct(t, got.TotalPct, 50)
	// Period aggregation weights durations rather than averaging monthly percentages.
	from = v3ReliabilityTime(t, "2026-01-31T23:00:00Z")
	s = rrdSeries{T: []int64{from.Unix(), from.Add(time.Hour).Unix(), from.Add(4 * time.Hour).Unix()}, Step: 3600,
		Cols: map[string][]float64{"cpu": {.2, math.NaN(), .3}}}
	got = calculateV3Availability([]rrdSeries{s}, nil, from, from.Add(7*time.Hour-time.Second))
	v3AssertPct(t, got.TotalPct, 57.14)
	v3AssertPct(t, got.Months[0].TotalPct, 100)
	v3AssertPct(t, got.Months[1].TotalPct, 50)
}

func TestV3AvailabilityFullyExcludedIsUnknown(t *testing.T) {
	from := v3ReliabilityTime(t, "2026-01-01T00:00:00Z")
	s := rrdSeries{T: []int64{from.Unix()}, Step: 3600, Cols: map[string][]float64{"cpu": {.1}}}
	got := calculateV3Availability([]rrdSeries{s}, []config.Interruption{{From: "2026-01-01T00:00:00Z", To: "2026-01-01T01:00:00Z", Cause: "maintenance"}}, from, from.Add(time.Hour-time.Second))
	v3AssertPct(t, got.TotalPct, 100)
	if got.NetPct != nil {
		t.Fatalf("fully excluded net = %v", *got.NetPct)
	}
}

func TestV3BackupsMonthlyJobsAndCauses(t *testing.T) {
	from := v3ReliabilityTime(t, "2026-01-31T00:00:00Z")
	to := v3ReliabilityTime(t, "2026-02-28T23:59:59Z")
	tasks := []v3BackupTask{
		{Time: from.Unix() - 1, Status: "OK"},
		{Time: from.Unix(), Status: "OK"},
		{Time: from.Unix(), Status: "WARNINGS: 1"},
		{Time: from.Unix(), Status: "could not activate storage 'private-storage'"},
		{Time: from.AddDate(0, 0, 1).Unix(), Status: "job errors"},
		{Time: from.AddDate(0, 0, 1).Unix(), Status: "interrupted by signal"},
		{Time: from.AddDate(0, 0, 1).Unix(), Status: "unexpected state"},
		{Time: from.Unix(), Status: ""},
		{Time: from.Unix(), Status: "running"},
	}
	got := calculateV3Backups(tasks, nil, nil, false, from, to, to)
	if len(got.Nights) != 2 || got.Nights[0] != (v3BackupMonth{Month: "2026-01", Jobs: 3, OK: 2, Failed: 1}) || got.Nights[1] != (v3BackupMonth{Month: "2026-02", Jobs: 3, Failed: 3}) {
		t.Fatalf("monthly jobs = %+v", got.Nights)
	}
	if got.Causes == nil || *got.Causes != (v3BackupCauses{StorageUnreachable: 1, VMErrors: 1, Interrupted: 1, Other: 1}) {
		t.Fatalf("causes = %+v", got.Causes)
	}
	if got.CoveragePct != nil || got.CoverageReason != "unreadable" {
		t.Fatalf("unreadable coverage = %+v", got)
	}
}

func TestV3BackupCoverageCurrentGuestsFreshCopies(t *testing.T) {
	now := v3ReliabilityTime(t, "2026-11-19T12:00:00Z")
	backups := []v3BackupCopy{
		{VMID: 101, Created: now.Add(-time.Hour).Unix()},
		{VMID: 101, Created: now.Add(-2 * time.Hour).Unix()},
		{VMID: 102, Created: now.Add(-7 * 24 * time.Hour).Unix()}, // strictly newer than 7 days
		{VMID: 103, Created: now.Add(time.Hour).Unix()},
		{VMID: 999, Created: now.Add(-time.Hour).Unix()},
	}
	got := calculateV3Backups(nil, []int{101, 102, 103, 101}, backups, true, now.AddDate(0, -1, 0), now, now)
	v3AssertPct(t, got.CoveragePct, 33.33)
	if got.CoverageReason != "" {
		t.Fatalf("readable coverage reason = %q", got.CoverageReason)
	}
	got = calculateV3Backups(nil, []int{101}, nil, true, now.AddDate(0, -1, 0), now, now)
	v3AssertPct(t, got.CoveragePct, 0)
	got = calculateV3Backups(nil, nil, backups, true, now.AddDate(0, -1, 0), now, now)
	if got.CoveragePct != nil {
		t.Fatalf("no guests coverage = %v", *got.CoveragePct)
	}
}
