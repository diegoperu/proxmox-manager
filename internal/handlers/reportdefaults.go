package handlers

import "time"

// BuiltinReportDefaults returns the report thresholds in their persisted units.
// Keys match the JSON fields of config.ReportDefaults.
func BuiltinReportDefaults() map[string]float64 {
	empty := func(string) string { return "" }
	now := time.Now().UTC()
	report, err := parseReportParams(empty, now)
	if err != nil {
		// An empty query lacks the required start date. Its thresholds are already
		// initialized; run the empty filters to finish parsing without a period.
		report, err = parseReportFilters(empty, report)
		if err != nil {
			panic("invalid built-in report defaults: " + err.Error())
		}
	}
	trend, err := parseTrendParams(empty, now)
	if err != nil {
		panic("invalid built-in trend defaults: " + err.Error())
	}
	return map[string]float64{
		"inactive_cpu":   report.TH.InactiveCPU,
		"inactive_net":   report.TH.InactiveNetBps,
		"inactive_days":  report.TH.InactiveMinDays,
		"oversized_mem":  report.TH.OversizedMem,
		"oversized_cpu":  report.TH.OversizedCPU,
		"oversized_days": report.TH.OversizedMinDays,
		"idle_util":      report.GPUIdle.Util,
		"idle_mem":       report.GPUIdle.Mem,
		"idle_days":      float64(report.GPUIdle.Days),
		"abandon_months": float64(trend.Trend.AbandonMonths),
		"abandon_peak":   trend.Trend.AbandonPeak,
		"abandon_cpu":    trend.Trend.AbandonCPU,
		"abandon_net":    trend.Trend.AbandonNet,
		"over_mem":       trend.Trend.OverMem,
		"over_cpu":       trend.Trend.OverCPU,
		"margin":         trend.Trend.Margin,
	}
}
