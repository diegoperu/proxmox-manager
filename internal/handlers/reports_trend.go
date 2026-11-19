package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"proxmox-manager/internal/history"
)

func reportIntParam(q func(string) string, name string, def, lo, hi int) (int, error) {
	s := q(name)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s: valore intero non valido (%d-%d)", name, lo, hi)
	}
	return n, nil
}

func parseTrendThresholds(q func(string) string, p *reportParams) error {
	var err error
	p.Months, err = reportIntParam(q, "months", 6, 1, 12)
	if err != nil {
		return err
	}
	p.Trend.AbandonMonths, err = reportIntParam(q, "abandon_months", 3, 1, 12)
	if err != nil {
		return err
	}
	for _, f := range []struct {
		name   string
		dst    *float64
		lo, hi float64
	}{
		{"abandon_cpu", &p.Trend.AbandonCPU, 0, 50}, {"abandon_net", &p.Trend.AbandonNet, 0, 1048576},
		{"over_mem", &p.Trend.OverMem, 1, 90}, {"over_cpu", &p.Trend.OverCPU, 1, 90}, {"margin", &p.Trend.Margin, 0, 200},
	} {
		v, err := floatParam(q(f.name), *f.dst, f.lo, f.hi)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		*f.dst = v
	}
	return nil
}

// months overrides from/to, including invalid date strings, only for the trend model.
// Common filters use the same whitelist as parseReportParams; no minimum-day rule applies.
func parseTrendParams(q func(string) string, now time.Time) (reportParams, error) {
	p, err := parseReportFilters(q, defaultReportParams())
	if err != nil {
		return p, err
	}
	p.From, p.To, p.TF, p.Step = trendMonthStart(now).AddDate(0, 1-p.Months, 0), now, "year", 21600
	return p, nil
}

type trendMeta struct {
	reportMeta
	Months         []string `json:"months"`
	CurrentPartial bool     `json:"current_partial"`
	StoredFrom     *string  `json:"stored_from"`
}

type trendReport struct {
	Meta       trendMeta       `json:"meta"`
	Thresholds trendThresholds `json:"thresholds"`
	Totals     trendTotals     `json:"totals"`
	Rows       []trendRow      `json:"rows"`
}

type trendInput struct {
	row     trendRow
	stored  []history.VMUseDay
	current *resGuest
	avg, mx rrdSeries
}

func trendMatches(g resGuest, p reportParams) bool {
	if p.Nodes != nil && !p.Nodes[g.Node] {
		return false
	}
	if p.Kind != "all" && p.Kind != "" && p.Kind != g.Kind {
		return false
	}
	if p.Group != "" {
		found := false
		for _, group := range g.Groups {
			found = found || group == p.Group
		}
		if !found {
			return false
		}
	}
	return p.Query == "" || strings.Contains(strings.ToLower(g.Name), p.Query)
}

func trendStoredGuest(r history.VMUseDay) resGuest {
	return resGuest{VMID: r.VMID, Name: r.Name, Kind: r.Kind, Node: r.Node, Groups: []string{r.Group}, Status: "deleted", Cores: int64(r.Cores), Mem: r.MemBytes}
}

func trendGuestRow(g resGuest) trendRow {
	status := g.Status
	if status != "running" && status != "deleted" {
		status = "stopped"
	}
	cores, mem := g.Cores, g.Mem
	if cores <= 0 || cores > 4096 {
		cores = -1
	}
	if mem <= 0 {
		mem = -1
	}
	return trendRow{VMID: g.VMID, Name: g.Name, Kind: g.Kind, Node: g.Node, Group: firstGroup(g.Groups), Status: status, Cores: cores, MemBytes: mem}
}

func buildTrendReport(c reportAPI, hist *history.Store, cluster string, p reportParams, now time.Time) (trendReport, error) {
	rep := trendReport{Thresholds: p.Trend, Rows: []trendRow{}}
	rep.Meta = trendMeta{reportMeta: metaFor(cluster, p, now), Months: trendMonths(p.From, p.To), CurrentPartial: true}
	rep.Meta.P95Window = ""
	rep.Meta.Notes = []string{"calendar_months", "months_overrides_from_to", "rrd_coarse", "no_monthly_p95"}
	var stored []history.VMUseDay
	first := ""
	if hist != nil {
		var err error
		first, err = hist.VMUseFirstDay(cluster)
		if err != nil {
			return rep, err
		}
		if first != "" {
			if _, err := time.ParseInLocation("2006-01-02", first, p.From.Location()); err != nil {
				return rep, errors.New("trend: data dello storico non valida")
			}
		}
		stored, err = hist.VMUseDays(cluster, p.From.Format("2006-01-02"), p.To.Format("2006-01-02"))
		if err != nil {
			return rep, err
		}
	}
	if first != "" {
		rep.Meta.StoredFrom = &first
		rep.Meta.Notes = append(rep.Meta.Notes, "rrd_before_"+first)
	} else {
		rep.Meta.Notes = append(rep.Meta.Notes, "rrd_only")
	}
	// Keep the unfiltered inventory for identity: moving a VM to another node/group must not
	// make its selected historical instance look deleted.
	res, err := loadResources(c, reportParams{Kind: "all"})
	if err != nil {
		return rep, errors.New("trend: inventario non disponibile")
	}
	byInstance := map[vmInstKey]*trendInput{}
	latest := map[vmInstKey]string{}
	relevant := map[int]bool{}
	for _, r := range stored {
		if err := history.ValidVMUse(r); err != nil {
			return rep, err
		}
		key := vmInstKey{r.VMID, r.Inst}
		in := byInstance[key]
		if in == nil {
			in = &trendInput{}
			byInstance[key] = in
		}
		in.stored = append(in.stored, r)
		if r.Day >= latest[key] {
			in.row = trendGuestRow(trendStoredGuest(r))
			latest[key] = r.Day
		}
	}
	for _, in := range byInstance {
		g := resGuest{VMID: in.row.VMID, Name: in.row.Name, Kind: in.row.Kind, Node: in.row.Node, Groups: []string{in.row.Group}}
		if trendMatches(g, p) {
			relevant[g.VMID] = true
		}
	}
	var guests []resGuest
	for _, g := range res.Guests {
		if g.VMID > 999999999 {
			return rep, errors.New("trend: VMID non valido")
		}
		if relevant[g.VMID] || trendMatches(g, p) {
			guests = append(guests, g)
		}
	}
	if len(guests) > maxReportGuests {
		return rep, errors.New(errTooManyGuests)
	}
	insts := make([]string, len(guests))
	errs := make([]error, len(guests))
	jobs := []func(){}
	for i, g := range guests {
		i, g := i, g
		jobs = append(jobs, func() {
			if g.Kind == "qemu" {
				raw, err := c.GetVMConfig(g.Node, g.VMID)
				if err != nil {
					errs[i] = errors.New("trend: configurazione VM non disponibile")
					return
				}
				var cfg map[string]interface{}
				if err := json.Unmarshal(raw, &cfg); err != nil {
					errs[i] = errors.New("trend: configurazione VM non valida")
					return
				}
				if cfg == nil {
					errs[i] = errors.New("trend: configurazione VM non valida")
					return
				}
				insts[i] = vmInstance(cfg)
			}
			if insts[i] == "" && hist != nil {
				last, found, err := hist.VMUseLastBefore(cluster, g.VMID, g.Kind, p.To.AddDate(0, 0, 1).Format("2006-01-02"))
				if err != nil {
					errs[i] = err
					return
				}
				if found && (g.Kind == "lxc" || last.Name == g.Name) {
					if !history.InstPattern.MatchString(last.Inst) {
						errs[i] = errors.New("trend: istanza dello storico non valida")
						return
					}
					insts[i] = last.Inst
				}
			}
			// An unseen current guest remains separate from every stored instance.
			if insts[i] == "" {
				insts[i] = "current"
			}
		})
	}
	runLimited(jobs)
	for i, g := range guests {
		if errs[i] != nil {
			return rep, errs[i]
		}
		key := vmInstKey{g.VMID, insts[i]}
		in := byInstance[key]
		if in == nil {
			in = &trendInput{}
			byInstance[key] = in
		}
		in.row = trendGuestRow(g)
		g := g
		in.current = &g
	}
	inputs := []*trendInput{}
	for _, in := range byInstance {
		g := resGuest{Name: in.row.Name, Kind: in.row.Kind, Node: in.row.Node, Groups: []string{in.row.Group}}
		if in.current != nil {
			g = *in.current
		}
		if trendMatches(g, p) {
			inputs = append(inputs, in)
		}
	}
	if len(inputs) > maxReportGuests {
		return rep, errors.New(errTooManyGuests)
	}
	jobs = nil
	rrdOK := make([]bool, len(inputs))
	for i, in := range inputs {
		if in.current == nil || (first != "" && first <= p.From.Format("2006-01-02")) {
			rrdOK[i] = true
			continue
		}
		i, in := i, in
		jobs = append(jobs, func() {
			var okAvg, okMax bool
			in.avg, okAvg = guestRRD(c, *in.current, "year", "AVERAGE", 0, p.To.Unix())
			in.mx, okMax = guestRRD(c, *in.current, "year", "MAX", 0, p.To.Unix())
			rrdOK[i] = okAvg && okMax
		})
	}
	runLimited(jobs)
	for i, in := range inputs {
		// Failed reads are not proof that a guest has been off for months.
		if !rrdOK[i] {
			return rep, errors.New("trend: RRD non disponibile")
		}
		in.row.Months = mergeTrendMonths(in.stored, in.avg, in.mx, first, p)
		classifyTrend(&in.row, p.Trend)
		rep.Rows = append(rep.Rows, in.row)
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		a, b := rep.Rows[i], rep.Rows[j]
		if a.VMID != b.VMID {
			return a.VMID < b.VMID
		}
		if a.Status != b.Status {
			return a.Status == "deleted"
		}
		return a.Name < b.Name
	})
	rep.Totals = trendSum(rep.Rows)
	return rep, nil
}

// The service period counts inclusive calendar months, capped at 12, ending at its own To.
func serviceTrendParams(p reportParams) reportParams {
	months := (p.To.Year()-p.From.Year())*12 + int(p.To.Month()-p.From.Month()) + 1
	p.Months = min(12, max(1, months))
	p.From = trendMonthStart(p.To).AddDate(0, 1-p.Months, 0)
	p.TF, p.Step = "year", 21600
	return p
}
