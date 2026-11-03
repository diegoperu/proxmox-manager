package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/api"
)

// Ceph status, read only. The cluster answers come from Proxmox (`/cluster/ceph/status` and, per node,
// `/nodes/{node}/ceph/*`); everything is normalised and bounded here because the browser draws it as is.

const (
	cephMaxOSDs     = 2000
	cephMaxPools    = 200
	cephMaxChecks   = 100
	cephMaxDetails  = 20
	cephMaxStates   = 50
	cephMaxServices = 100
	cephMaxFlags    = 50
	cephMaxText     = 300
	cephSummaryTTL  = 10 * time.Second
	cephFullTTL     = 4 * time.Second // the page polls every 5 s
)

// Ceph's default ratios. Proxmox does not expose the cluster's own values.
var cephThresholds = map[string]float64{"nearfull": 85, "backfillfull": 90, "full": 95}

type cephCheck struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"` // HEALTH_ERR | HEALTH_WARN
	Message  string   `json:"message"`
	Count    int      `json:"count"`
	Muted    bool     `json:"muted"`
	Detail   []string `json:"detail"`
}

type cephCapacity struct {
	Total   int64   `json:"total"`
	Used    int64   `json:"used"`
	Avail   int64   `json:"avail"`
	Data    int64   `json:"data"` // logical data stored (before replication)
	Percent float64 `json:"percent"`
}

type cephIO struct {
	ReadBytesSec  float64 `json:"read_bytes_sec"`
	WriteBytesSec float64 `json:"write_bytes_sec"`
	ReadOpsSec    float64 `json:"read_ops_sec"`
	WriteOpsSec   float64 `json:"write_ops_sec"`
}

type cephPGState struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type cephProgress struct {
	Message  string  `json:"message"`
	Progress float64 `json:"progress"` // 0-100
}

type cephRecovery struct {
	Active             bool           `json:"active"`
	ProgressPct        *float64       `json:"progress_pct"` // mean of the progress events, nil when Ceph reports none
	Events             []cephProgress `json:"events"`
	DegradedRatio      float64        `json:"degraded_ratio"`
	MisplacedRatio     float64        `json:"misplaced_ratio"`
	DegradedObjects    int64          `json:"degraded_objects"`
	MisplacedObjects   int64          `json:"misplaced_objects"`
	RecoveringBytesSec float64        `json:"recovering_bytes_sec"`
	RecoveringObjSec   float64        `json:"recovering_objects_sec"`
	ETASeconds         *int64         `json:"eta_seconds"` // only when objects are being moved right now
	BackfillPGs        int            `json:"backfill_pgs"`
	RecoveringPGs      int            `json:"recovering_pgs"`
	DegradedPGs        int            `json:"degraded_pgs"`
}

type cephOverview struct {
	Available  bool               `json:"available"`
	Reason     string             `json:"reason,omitempty"` // not_configured | forbidden
	Health     string             `json:"health"`
	Checks     []cephCheck        `json:"checks"`
	Capacity   cephCapacity       `json:"capacity"`
	Thresholds map[string]float64 `json:"thresholds"`
	OSDs       struct {
		Total int `json:"total"`
		Up    int `json:"up"`
		In    int `json:"in"`
	} `json:"osd_counts"`
	PGs struct {
		Total   int           `json:"total"`
		Objects int64         `json:"objects"`
		States  []cephPGState `json:"states"`
	} `json:"pgs"`
	IO        cephIO       `json:"io"`
	Recovery  cephRecovery `json:"recovery"`
	MonQuorum struct {
		In    int `json:"in"`
		Total int `json:"total"`
	} `json:"mon_quorum"`
	Updated int64    `json:"updated"`
	quorum  []string // monitor names in quorum: on Proxmox they are node names, tried first for per-node calls
}

type cephOSD struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	Host      string  `json:"host"`
	Class     string  `json:"class"`
	Status    string  `json:"status"` // up | down
	In        bool    `json:"in"`
	Percent   float64 `json:"percent"`
	Used      int64   `json:"used"`
	Total     int64   `json:"total"`
	PGs       int     `json:"pgs"`
	LatencyMs float64 `json:"latency_ms"`
}

type cephPool struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Size      int      `json:"size"`
	MinSize   int      `json:"min_size"`
	PGNum     int      `json:"pg_num"`
	Used      int64    `json:"used"`
	Percent   float64  `json:"percent"`
	Rule      string   `json:"rule"`
	Apps      []string `json:"apps"`
	Autoscale string   `json:"autoscale"`
}

type cephService struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	State   string `json:"state"`
	Quorum  bool   `json:"quorum,omitempty"`
	Rank    int    `json:"rank"`
	Version string `json:"version"`
}

type cephFlag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type cephFull struct {
	cephOverview
	OSDs    []cephOSD     `json:"osds"`
	Pools   []cephPool    `json:"pools"`
	Mons    []cephService `json:"mons"`
	Mgrs    []cephService `json:"mgrs"`
	Mdss    []cephService `json:"mdss"`
	Flags   []cephFlag    `json:"flags"`
	Partial []string      `json:"partial"` // parts that could not be read (the rest is still valid)
}

// ── helpers ─────────────────────────────────────────────────────────────────

func clampText(s string, n int) string {
	s = cleanText(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func num(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func i64(v float64) int64 {
	if v = num(v); v > 9e15 {
		return 9e15
	}
	return int64(math.Round(v))
}

func round1v(v float64) float64 { return math.Round(num(v)*10) / 10 }

// parseBoolish: Proxmox returns 0/1, true/false or "0"/"1" depending on the call.
func parseBoolish(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	}
	return false
}

func asFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		var f float64
		if _, err := fmt.Sscan(t, &f); err == nil {
			return f
		}
	}
	return 0
}

func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ── status ──────────────────────────────────────────────────────────────────

type rawCephStatus struct {
	Health struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Severity string `json:"severity"`
			Muted    bool   `json:"muted"`
			Summary  struct {
				Message string  `json:"message"`
				Count   float64 `json:"count"`
			} `json:"summary"`
			Detail []struct {
				Message string `json:"message"`
			} `json:"detail"`
		} `json:"checks"`
	} `json:"health"`
	OSDMap struct {
		NumOSDs float64 `json:"num_osds"`
		NumUp   float64 `json:"num_up_osds"`
		NumIn   float64 `json:"num_in_osds"`
	} `json:"osdmap"`
	PGMap struct {
		BytesTotal float64 `json:"bytes_total"`
		BytesUsed  float64 `json:"bytes_used"`
		BytesAvail float64 `json:"bytes_avail"`
		DataBytes  float64 `json:"data_bytes"`
		NumPGs     float64 `json:"num_pgs"`
		NumObjects float64 `json:"num_objects"`
		PGsByState []struct {
			StateName string  `json:"state_name"`
			Count     float64 `json:"count"`
		} `json:"pgs_by_state"`
		ReadBytesSec     float64 `json:"read_bytes_sec"`
		WriteBytesSec    float64 `json:"write_bytes_sec"`
		ReadOpPerSec     float64 `json:"read_op_per_sec"`
		WriteOpPerSec    float64 `json:"write_op_per_sec"`
		RecoveringBytes  float64 `json:"recovering_bytes_per_sec"`
		RecoveringObjs   float64 `json:"recovering_objects_per_sec"`
		DegradedObjects  float64 `json:"degraded_objects"`
		DegradedRatio    float64 `json:"degraded_ratio"`
		MisplacedObjects float64 `json:"misplaced_objects"`
		MisplacedRatio   float64 `json:"misplaced_ratio"`
	} `json:"pgmap"`
	QuorumNames []string `json:"quorum_names"`
	MonMap      struct {
		Mons []struct {
			Name string `json:"name"`
		} `json:"mons"`
	} `json:"monmap"`
	ProgressEvents json.RawMessage `json:"progress_events"`
}

var severityRank = map[string]int{"HEALTH_ERR": 0, "HEALTH_WARN": 1}

// normalizeCephStatus turns `/cluster/ceph/status` into the overview. `now` is injected for tests.
func normalizeCephStatus(raw json.RawMessage, now time.Time) (cephOverview, error) {
	var st rawCephStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return cephOverview{}, err
	}
	o := cephOverview{Available: true, Thresholds: cephThresholds, Updated: now.Unix(),
		Checks: []cephCheck{}}
	o.Health = st.Health.Status
	if _, ok := map[string]bool{"HEALTH_OK": true, "HEALTH_WARN": true, "HEALTH_ERR": true}[o.Health]; !ok {
		o.Health = "HEALTH_UNKNOWN"
	}
	for code, c := range st.Health.Checks {
		if len(o.Checks) >= cephMaxChecks {
			break
		}
		ck := cephCheck{Code: clampText(code, 64), Severity: c.Severity, Message: clampText(c.Summary.Message, cephMaxText),
			Count: int(num(c.Summary.Count)), Muted: c.Muted, Detail: []string{}}
		if ck.Severity != "HEALTH_ERR" {
			ck.Severity = "HEALTH_WARN"
		}
		for _, d := range c.Detail {
			if len(ck.Detail) >= cephMaxDetails {
				break
			}
			ck.Detail = append(ck.Detail, clampText(strings.TrimSpace(d.Message), cephMaxText))
		}
		o.Checks = append(o.Checks, ck)
	}
	sort.Slice(o.Checks, func(i, j int) bool {
		a, b := o.Checks[i], o.Checks[j]
		if a.Muted != b.Muted {
			return !a.Muted
		}
		if severityRank[a.Severity] != severityRank[b.Severity] {
			return severityRank[a.Severity] < severityRank[b.Severity]
		}
		return a.Code < b.Code
	})

	pm := st.PGMap
	o.Capacity = cephCapacity{Total: i64(pm.BytesTotal), Used: i64(pm.BytesUsed), Avail: i64(pm.BytesAvail), Data: i64(pm.DataBytes)}
	if o.Capacity.Total > 0 {
		o.Capacity.Percent = round1v(float64(o.Capacity.Used) / float64(o.Capacity.Total) * 100)
	}
	o.OSDs.Total, o.OSDs.Up, o.OSDs.In = int(num(st.OSDMap.NumOSDs)), int(num(st.OSDMap.NumUp)), int(num(st.OSDMap.NumIn))
	o.PGs.Total, o.PGs.Objects = int(num(pm.NumPGs)), i64(pm.NumObjects)
	o.PGs.States = []cephPGState{}
	var backfill, recovering, degraded int
	for _, s := range pm.PGsByState {
		n := int(num(s.Count))
		name := clampText(s.StateName, 100)
		if len(o.PGs.States) < cephMaxStates {
			o.PGs.States = append(o.PGs.States, cephPGState{State: name, Count: n})
		}
		if strings.Contains(name, "backfill") {
			backfill += n
		}
		if strings.Contains(name, "recover") {
			recovering += n
		}
		if strings.Contains(name, "degraded") || strings.Contains(name, "undersized") {
			degraded += n
		}
	}
	sort.SliceStable(o.PGs.States, func(i, j int) bool { return o.PGs.States[i].Count > o.PGs.States[j].Count })
	o.IO = cephIO{ReadBytesSec: num(pm.ReadBytesSec), WriteBytesSec: num(pm.WriteBytesSec), ReadOpsSec: num(pm.ReadOpPerSec), WriteOpsSec: num(pm.WriteOpPerSec)}

	rc := cephRecovery{Events: []cephProgress{}, DegradedRatio: num(pm.DegradedRatio), MisplacedRatio: num(pm.MisplacedRatio),
		DegradedObjects: i64(pm.DegradedObjects), MisplacedObjects: i64(pm.MisplacedObjects),
		RecoveringBytesSec: num(pm.RecoveringBytes), RecoveringObjSec: num(pm.RecoveringObjs),
		BackfillPGs: backfill, RecoveringPGs: recovering, DegradedPGs: degraded}
	// progress_events is {} when idle, a map id -> {message, progress} while Ceph tracks something
	var ev map[string]struct {
		Message  string  `json:"message"`
		Progress float64 `json:"progress"`
	}
	if len(st.ProgressEvents) > 0 && json.Unmarshal(st.ProgressEvents, &ev) == nil {
		ids := make([]string, 0, len(ev))
		for id := range ev {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var sum float64
		for _, id := range ids {
			if len(rc.Events) >= 20 {
				break
			}
			p := math.Min(100, math.Max(0, num(ev[id].Progress)*100))
			rc.Events = append(rc.Events, cephProgress{Message: clampText(ev[id].Message, cephMaxText), Progress: round1v(p)})
			sum += p
		}
		if len(rc.Events) > 0 {
			avg := round1v(sum / float64(len(rc.Events)))
			rc.ProgressPct = &avg
		}
	}
	rc.Active = rc.RecoveringObjSec > 0 || rc.RecoveringBytesSec > 0 || rc.DegradedRatio > 0 || rc.MisplacedRatio > 0 ||
		backfill+recovering+degraded > 0 || len(rc.Events) > 0
	if remaining := rc.DegradedObjects + rc.MisplacedObjects; remaining > 0 && rc.RecoveringObjSec > 0 {
		eta := int64(math.Ceil(float64(remaining) / rc.RecoveringObjSec))
		rc.ETASeconds = &eta
	}
	o.Recovery = rc

	o.MonQuorum.In, o.MonQuorum.Total = len(st.QuorumNames), len(st.MonMap.Mons)
	for _, n := range st.QuorumNames {
		if len(o.quorum) < 64 && nodeNamePattern.MatchString(n) {
			o.quorum = append(o.quorum, n)
		}
	}
	return o, nil
}

// ── OSD tree, pools, services, flags ─────────────────────────────────────────

// normalizeCephOSDs walks `/nodes/{n}/ceph/osd` (root > host > osd, any depth) and returns the OSD leaves.
func normalizeCephOSDs(raw json.RawMessage) []cephOSD {
	out := []cephOSD{}
	var tree struct {
		Root json.RawMessage `json:"root"`
	}
	if json.Unmarshal(raw, &tree) != nil {
		return out
	}
	seen := map[int]bool{}
	var walk func(n map[string]interface{}, depth int)
	walk = func(n map[string]interface{}, depth int) {
		if depth > 8 || len(out) >= cephMaxOSDs {
			return
		}
		if n["type"] == "osd" {
			id := int(asFloat(n["id"]))
			if seen[id] {
				return
			}
			seen[id] = true
			st := asString(n["status"])
			if st != "up" {
				st = "down"
			}
			out = append(out, cephOSD{ID: id, Name: clampText(asString(n["name"]), 32), Host: clampText(asString(n["host"]), 64),
				Class: clampText(asString(n["device_class"]), 16), Status: st, In: parseBoolish(n["in"]),
				Percent: round1v(asFloat(n["percent_used"])), Used: i64(asFloat(n["bytes_used"])), Total: i64(asFloat(n["total_space"])),
				PGs: int(num(asFloat(n["pgs"]))), LatencyMs: round1v(asFloat(n["apply_latency_ms"]))})
			return
		}
		if ch, ok := n["children"].([]interface{}); ok {
			for _, c := range ch {
				if m, ok := c.(map[string]interface{}); ok {
					walk(m, depth+1)
				}
			}
		}
	}
	var root map[string]interface{}
	if json.Unmarshal(tree.Root, &root) == nil {
		walk(root, 0)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return naturalLess(out[i].Host, out[j].Host)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// naturalLess orders "node-2" before "node-10".
func naturalLess(a, b string) bool {
	ia, ib := 0, 0
	for ia < len(a) && ib < len(b) {
		if isDigit(a[ia]) && isDigit(b[ib]) {
			ja, jb := ia, ib
			for ja < len(a) && isDigit(a[ja]) {
				ja++
			}
			for jb < len(b) && isDigit(b[jb]) {
				jb++
			}
			na, nb := strings.TrimLeft(a[ia:ja], "0"), strings.TrimLeft(b[ib:jb], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			ia, ib = ja, jb
			continue
		}
		if a[ia] != b[ib] {
			return a[ia] < b[ib]
		}
		ia++
		ib++
	}
	return len(a)-ia < len(b)-ib
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func normalizeCephPools(raw json.RawMessage) []cephPool {
	out := []cephPool{}
	var list []map[string]interface{}
	if json.Unmarshal(raw, &list) != nil {
		return out
	}
	for _, p := range list {
		if len(out) >= cephMaxPools {
			break
		}
		apps := []string{}
		if am, ok := p["application_metadata"].(map[string]interface{}); ok {
			for k := range am {
				apps = append(apps, clampText(k, 32))
			}
			sort.Strings(apps)
		}
		// percent_used is a 0-1 fraction in this answer (unlike the per-OSD one, already in percent)
		out = append(out, cephPool{Name: clampText(asString(p["pool_name"]), 64), Type: clampText(asString(p["type"]), 16),
			Size: int(num(asFloat(p["size"]))), MinSize: int(num(asFloat(p["min_size"]))), PGNum: int(num(asFloat(p["pg_num"]))),
			Used: i64(asFloat(p["bytes_used"])), Percent: round1v(asFloat(p["percent_used"]) * 100),
			Rule: clampText(asString(p["crush_rule_name"]), 64), Apps: apps, Autoscale: clampText(asString(p["pg_autoscale_mode"]), 16)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func normalizeCephServices(raw json.RawMessage) []cephService {
	out := []cephService{}
	var list []map[string]interface{}
	if json.Unmarshal(raw, &list) != nil {
		return out
	}
	for _, s := range list {
		if len(out) >= cephMaxServices {
			break
		}
		out = append(out, cephService{Name: clampText(asString(s["name"]), 64), Host: clampText(asString(s["host"]), 64),
			State: clampText(asString(s["state"]), 32), Quorum: parseBoolish(s["quorum"]), Rank: int(asFloat(s["rank"])),
			Version: clampText(asString(s["ceph_version_short"]), 32)})
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Name, out[j].Name) })
	return out
}

// normalizeCephFlags keeps the flags that are SET (value 1): they are what an operator needs to notice.
func normalizeCephFlags(raw json.RawMessage) []cephFlag {
	out := []cephFlag{}
	var list []map[string]interface{}
	if json.Unmarshal(raw, &list) != nil {
		return out
	}
	for _, f := range list {
		if len(out) >= cephMaxFlags {
			break
		}
		if parseBoolish(f["value"]) {
			out = append(out, cephFlag{Name: clampText(asString(f["name"]), 40), Description: clampText(asString(f["description"]), cephMaxText)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── fetching ────────────────────────────────────────────────────────────────

type cephErrorKind int

const (
	cephOther cephErrorKind = iota
	cephNotConfigured
	cephForbidden
)

// classifyCephError separates "this cluster has no Ceph" and "the token may not read it" from real failures.
func classifyCephError(err error) cephErrorKind {
	if err == nil {
		return cephOther
	}
	if isForbidden(err) {
		return cephForbidden
	}
	msg := err.Error()
	if strings.Contains(msg, "proxmox 5") || strings.Contains(msg, "proxmox 4") {
		for _, marker := range []string{"binary not installed", "not initialized", "not configured", "ceph is not"} {
			if strings.Contains(msg, marker) {
				return cephNotConfigured
			}
		}
	}
	return cephOther
}

type cephAPI interface {
	GetNodes() (json.RawMessage, error)
	GetCephStatus() (json.RawMessage, error)
	GetCephOSD(node string) (json.RawMessage, error)
	GetCephPools(node string) (json.RawMessage, error)
	GetCephServices(node, kind string) (json.RawMessage, error)
	GetCephFlags() (json.RawMessage, error)
}

var _ cephAPI = (*api.Client)(nil)

// unavailableCeph is the answer for a cluster without readable Ceph; slices stay empty arrays, never null.
func unavailableCeph(reason string) cephOverview {
	o := cephOverview{Available: false, Reason: reason, Checks: []cephCheck{}, Thresholds: cephThresholds}
	o.PGs.States = []cephPGState{}
	o.Recovery.Events = []cephProgress{}
	return o
}

func fetchCephOverview(c cephAPI, now time.Time) (cephOverview, error) {
	raw, err := c.GetCephStatus()
	if err != nil {
		switch classifyCephError(err) {
		case cephNotConfigured:
			return unavailableCeph("not_configured"), nil
		case cephForbidden:
			return unavailableCeph("forbidden"), nil
		}
		return cephOverview{}, err
	}
	return normalizeCephStatus(raw, now)
}

// cephNodeCandidates lists the online nodes the per-node Ceph calls may go to, monitor hosts first:
// a node of a Ceph cluster without the Ceph packages answers 500 to /nodes/{n}/ceph/*.
func cephNodeCandidates(c cephAPI, prefer []string) ([]string, error) {
	raw, err := c.GetNodes()
	if err != nil {
		return nil, err
	}
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &nodes) != nil {
		return nil, errors.New("elenco nodi non valido")
	}
	online := map[string]bool{}
	var rest []string
	for _, n := range nodes {
		if n.Status == "online" && nodeNamePattern.MatchString(n.Node) && !online[n.Node] {
			online[n.Node] = true
			rest = append(rest, n.Node)
		}
	}
	out := []string{}
	used := map[string]bool{}
	for _, n := range prefer {
		if online[n] && !used[n] {
			used[n] = true
			out = append(out, n)
		}
	}
	for _, n := range rest {
		if !used[n] {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("nessun nodo in linea")
	}
	return out, nil
}

const cephNodeAttempts = 3

func fetchCephFull(c cephAPI, now time.Time) (cephFull, error) {
	ov, err := fetchCephOverview(c, now)
	if err != nil || !ov.Available {
		return cephFull{cephOverview: ov, OSDs: []cephOSD{}, Pools: []cephPool{}, Mons: []cephService{}, Mgrs: []cephService{},
			Mdss: []cephService{}, Flags: []cephFlag{}, Partial: []string{}}, err
	}
	full := cephFull{cephOverview: ov, OSDs: []cephOSD{}, Pools: []cephPool{}, Mons: []cephService{}, Mgrs: []cephService{},
		Mdss: []cephService{}, Flags: []cephFlag{}, Partial: []string{}}
	// The OSD tree decides the node: the first candidate that answers is used for the other per-node calls.
	node := ""
	if cands, err := cephNodeCandidates(c, ov.quorum); err == nil {
		for i, n := range cands {
			if i >= cephNodeAttempts {
				break
			}
			if raw, err := c.GetCephOSD(n); err == nil {
				node = n
				full.OSDs = normalizeCephOSDs(raw)
				break
			}
		}
	}
	if node == "" {
		full.Partial = append(full.Partial, "osd", "pools", "mon", "mgr", "mds")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	part := func(name string, fn func() (json.RawMessage, error), apply func(json.RawMessage)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := fn()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				full.Partial = append(full.Partial, name)
				return
			}
			apply(raw)
		}()
	}
	if node != "" {
		part("pools", func() (json.RawMessage, error) { return c.GetCephPools(node) }, func(r json.RawMessage) { full.Pools = normalizeCephPools(r) })
		part("mon", func() (json.RawMessage, error) { return c.GetCephServices(node, "mon") }, func(r json.RawMessage) { full.Mons = normalizeCephServices(r) })
		part("mgr", func() (json.RawMessage, error) { return c.GetCephServices(node, "mgr") }, func(r json.RawMessage) { full.Mgrs = normalizeCephServices(r) })
		part("mds", func() (json.RawMessage, error) { return c.GetCephServices(node, "mds") }, func(r json.RawMessage) { full.Mdss = normalizeCephServices(r) })
	}
	part("flags", c.GetCephFlags, func(r json.RawMessage) { full.Flags = normalizeCephFlags(r) })
	wg.Wait()
	sort.Strings(full.Partial)
	return full, nil
}

// ── cache (per cluster, per view) with one fetch at a time per key ───────────

type cephEntry struct {
	mu  sync.Mutex
	at  time.Time
	val interface{}
}

var cephCache = struct {
	sync.Mutex
	m map[string]*cephEntry
}{m: map[string]*cephEntry{}}

func cephCached(key string, ttl time.Duration, fn func() (interface{}, error)) (interface{}, error) {
	cephCache.Lock()
	e := cephCache.m[key]
	if e == nil {
		e = &cephEntry{}
		cephCache.m[key] = e
	}
	cephCache.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.val != nil && time.Since(e.at) < ttl {
		return e.val, nil
	}
	v, err := fn()
	if err != nil {
		return nil, err
	}
	e.val, e.at = v, time.Now()
	return v, nil
}

// cephForget drops a cached entry (after a write that changes what it describes).
func cephForget(key string) {
	cephCache.Lock()
	delete(cephCache.m, key)
	cephCache.Unlock()
}

func (h *Handler) cephHandler(full bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx := clusterIdx(r)
		client, err := h.getClientFor(idx)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		view, ttl := "summary", cephSummaryTTL
		if full {
			view, ttl = "full", cephFullTTL
		}
		v, err := cephCached(view+"|"+strconv.Itoa(idx), ttl, func() (interface{}, error) {
			if full {
				return fetchCephFull(client, time.Now())
			}
			return fetchCephOverview(client, time.Now())
		})
		if err != nil {
			log.Printf("ceph %s (cluster %d): %v", view, idx, err) // detail in the log only
			writeError(w, errors.New("dati Ceph non disponibili"), http.StatusBadGateway)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, v)
	}
}

// GetCephSummary: GET /api/ceph/summary (dashboard). GetCeph: GET /api/ceph (page).
func (h *Handler) GetCephSummary() http.HandlerFunc { return h.cephHandler(false) }
func (h *Handler) GetCeph() http.HandlerFunc        { return h.cephHandler(true) }
