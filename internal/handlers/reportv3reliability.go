package handlers

import (
	"math"
	"sort"
	"strings"
	"time"

	"proxmox-manager/internal/config"
)

// Manager outputs deliberately contain only fixed codes, months and numbers.
type v3Availability struct {
	Months        []v3AvailabilityMonth `json:"months"`
	TotalPct      *float64              `json:"total_pct"`
	NetPct        *float64              `json:"net_pct"`
	Interruptions []v3Interruption      `json:"interruptions"`
}

type v3AvailabilityMonth struct {
	Month    string   `json:"month"`
	TotalPct *float64 `json:"total_pct"`
	NetPct   *float64 `json:"net_pct"`
}

type v3Interruption struct {
	Month string  `json:"month"`
	Cause string  `json:"cause"`
	Hours float64 `json:"hours"`
}

type v3Interval struct{ from, to int64 }

type v3AvailabilityCount struct {
	total, valid, netTotal, netValid int64
}

func v3Percentage(numerator, denominator int64) *float64 {
	if denominator <= 0 {
		return nil
	}
	v := round2(float64(numerator) / float64(denominator) * 100)
	return &v
}

func v3InterruptionTime(s string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", s, loc)
}

func v3MergedIntervals(in []v3Interval) []v3Interval {
	sort.Slice(in, func(i, j int) bool { return in[i].from < in[j].from })
	out := []v3Interval{}
	for _, iv := range in {
		if len(out) > 0 && iv.from <= out[len(out)-1].to {
			out[len(out)-1].to = max(out[len(out)-1].to, iv.to)
		} else {
			out = append(out, iv)
		}
	}
	return out
}

func v3ExcludedSeconds(from, to int64, excluded []v3Interval) int64 {
	var seconds int64
	for _, iv := range excluded {
		seconds += max(int64(0), min(to, iv.to)-max(from, iv.from))
	}
	return seconds
}

// calculateV3Availability estimates availability from node RRD, never from task failures.
// The system cannot see power events unless administrators log them as interruptions.
// Samples represent the interval until the next timestamp (the last uses its local step).
// Each node's observation starts at its first valid CPU sample, even when before the report.
func calculateV3Availability(parts []rrdSeries, interruptions []config.Interruption, from, to time.Time) v3Availability {
	rep := v3Availability{Months: []v3AvailabilityMonth{}, Interruptions: []v3Interruption{}}
	months := trendMonths(from, to)
	counts := make(map[string]*v3AvailabilityCount, len(months))
	for _, m := range months {
		counts[m] = &v3AvailabilityCount{}
	}
	start, end := from.Unix(), to.Unix()+1 // reports accept inclusive final seconds
	var excluded []v3Interval
	for _, interruption := range interruptions {
		if interruption.Cause != "external" && interruption.Cause != "maintenance" && interruption.Cause != "fault" {
			continue
		}
		a, errA := v3InterruptionTime(interruption.From, from.Location())
		b, errB := v3InterruptionTime(interruption.To, from.Location())
		if errA != nil || errB != nil || !b.After(a) {
			continue
		}
		lo, hi := max(start, a.Unix()), min(end, b.Unix())
		if hi <= lo {
			continue
		}
		if interruption.Cause != "fault" {
			excluded = append(excluded, v3Interval{lo, hi})
		}
		for cursor := lo; cursor < hi; {
			t := time.Unix(cursor, 0).In(from.Location())
			next := min(hi, trendMonthStart(t).AddDate(0, 1, 0).Unix())
			rep.Interruptions = append(rep.Interruptions, v3Interruption{
				Month: t.Format("2006-01"), Cause: interruption.Cause, Hours: round2(float64(next-cursor) / 3600),
			})
			cursor = next
		}
	}
	excluded = v3MergedIntervals(excluded)
	// Canonical annotation order does not depend on configuration entry order.
	sort.Slice(rep.Interruptions, func(i, j int) bool {
		a, b := rep.Interruptions[i], rep.Interruptions[j]
		if a.Month != b.Month {
			return a.Month < b.Month
		}
		return a.Cause < b.Cause
	})
	for _, s := range parts {
		indices := make([]int, 0, len(s.T))
		last := int64(-1)
		for i, ts := range s.T {
			if ts <= last {
				continue
			}
			indices = append(indices, i)
			last = ts
		}
		seen := false
		for k, i := range indices {
			v := trendValue(s, "cpu", i)
			valid := !math.IsNaN(v)
			seen = seen || valid
			if !seen {
				continue
			}
			step := s.Step
			if k > 0 {
				step = s.T[i] - s.T[indices[k-1]]
			}
			if k+1 < len(indices) {
				step = s.T[indices[k+1]] - s.T[i]
			}
			if step <= 0 {
				continue // one timestamp without a known step has no measurable duration
			}
			pointEnd := s.T[i] + min(step, int64(604800))
			lo, hi := max(start, s.T[i]), min(end, pointEnd)
			for cursor := lo; cursor < hi; {
				t := time.Unix(cursor, 0).In(from.Location())
				next := min(hi, trendMonthStart(t).AddDate(0, 1, 0).Unix())
				if a := counts[t.Format("2006-01")]; a != nil {
					seconds := next - cursor
					netSeconds := seconds - v3ExcludedSeconds(cursor, next, excluded)
					a.total += seconds
					a.netTotal += netSeconds
					if valid {
						a.valid += seconds
						a.netValid += netSeconds
					}
				}
				cursor = next
			}
		}
	}
	var total v3AvailabilityCount
	for _, m := range months {
		a := counts[m]
		rep.Months = append(rep.Months, v3AvailabilityMonth{Month: m, TotalPct: v3Percentage(a.valid, a.total), NetPct: v3Percentage(a.netValid, a.netTotal)})
		total.total += a.total
		total.valid += a.valid
		total.netTotal += a.netTotal
		total.netValid += a.netValid
	}
	rep.TotalPct, rep.NetPct = v3Percentage(total.valid, total.total), v3Percentage(total.netValid, total.netTotal)
	return rep
}

// These input records stay inside the calculation boundary, never in manager output.
type v3BackupTask struct {
	Time   int64
	Status string
}

type v3BackupCopy struct {
	VMID    int
	Created int64
}

type v3Backups struct {
	Nights         []v3BackupMonth `json:"nights"`
	Causes         *v3BackupCauses `json:"causes"`
	CoveragePct    *float64        `json:"coverage_pct"`
	CoverageReason string          `json:"coverage_reason,omitempty"`
}

type v3BackupMonth struct {
	Month  string `json:"month"`
	Jobs   int    `json:"jobs"`
	OK     int    `json:"ok"`
	Failed int    `json:"failed"`
}

type v3BackupCauses struct {
	StorageUnreachable int `json:"storage_unreachable"`
	VMErrors           int `json:"vm_errors"`
	Interrupted        int `json:"interrupted"`
	Other              int `json:"other"`
}

func calculateV3Backups(tasks []v3BackupTask, guests []int, backups []v3BackupCopy, readable bool, from, to, now time.Time) v3Backups {
	rep := v3Backups{Nights: []v3BackupMonth{}, Causes: &v3BackupCauses{}}
	months := trendMonths(from, to)
	counts := make(map[string]*v3BackupMonth, len(months))
	for _, m := range months {
		counts[m] = &v3BackupMonth{Month: m}
	}
	for _, task := range tasks {
		if task.Time < from.Unix() || task.Time > to.Unix() || task.Status == "" || task.Status == "running" {
			continue
		}
		m := time.Unix(task.Time, 0).In(from.Location()).Format("2006-01")
		a := counts[m]
		if a == nil {
			continue
		}
		a.Jobs++
		if !taskFailed(task.Status) {
			a.OK++
			continue
		}
		a.Failed++
		status := strings.ToLower(task.Status)
		switch {
		case strings.Contains(status, "could not activate storage"):
			rep.Causes.StorageUnreachable++
		case strings.Contains(status, "job errors"):
			rep.Causes.VMErrors++
		case strings.Contains(status, "interrupted by signal"):
			rep.Causes.Interrupted++
		default:
			rep.Causes.Other++
		}
	}
	for _, m := range months {
		rep.Nights = append(rep.Nights, *counts[m])
	}
	if !readable {
		rep.CoverageReason = "unreadable"
		return rep
	}
	current, covered := map[int]bool{}, map[int]bool{}
	for _, id := range guests {
		if id > 0 {
			current[id] = true
		}
	}
	cutoff := now.Add(-7 * 24 * time.Hour).Unix()
	for _, backup := range backups {
		if current[backup.VMID] && backup.Created > cutoff && backup.Created <= now.Unix() {
			covered[backup.VMID] = true
		}
	}
	rep.CoveragePct = v3Percentage(int64(len(covered)), int64(len(current)))
	return rep
}
