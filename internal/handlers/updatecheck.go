package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"proxmox-manager/internal/buildinfo"
	"proxmox-manager/internal/config"
)

const (
	updateRepo      = "diegoperu/proxmox-manager"
	updateBaseURL   = "https://api.github.com"
	updateBodyLimit = 1 << 20
	// Compare includes file patches even with per_page=1; bound the fixed-host response.
	updateCompareLimit = 8 << 20
	updateInterval     = 6 * time.Hour
	updateCooldown     = 5 * time.Minute
)

// Only tests substitute the base. Neither configuration nor requests supply URLs.
var updateAPIBase = updateBaseURL
var updateCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type updateLocal struct {
	Short string `json:"short"`
	Date  string `json:"date"`
	Dirty bool   `json:"dirty"`
}

type updateResult struct {
	CheckedAt        string      `json:"checked_at"`
	State            string      `json:"state"`
	BehindBy         int         `json:"behind_by"`
	Titles           []string    `json:"titles"`
	Local            updateLocal `json:"local"`
	LatestCommitDate string      `json:"latest_commit_date,omitempty"`
	Error            string      `json:"error,omitempty"`
}

type updateChecker struct {
	mu      sync.Mutex
	running bool
	last    time.Time
	cached  updateResult
	start   sync.Once
}

var updateChecks updateChecker

func localUpdateResult() updateResult {
	info := buildinfo.Info()
	r := updateResult{State: "pending", Titles: []string{}, Local: updateLocal{info.Short, info.Date, info.Dirty}}
	if !config.Get().UpdateCheckEnabled() {
		r.State = "disabled"
	} else if !info.Known {
		r.State = "no_build_info"
	}
	return r
}

func (c *updateChecker) status() updateResult {
	r := localUpdateResult()
	if r.State != "pending" {
		return r
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached.State != "" {
		r = c.cached
		r.Titles = append([]string{}, r.Titles...)
	}
	return r
}

func updateTitle(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	runes := make([]rune, 0, 120)
	for _, r := range line {
		// Format characters include invisible bidi controls; do not display them.
		if !unicode.IsPrint(r) {
			continue
		}
		if len(runes) == 120 {
			break
		}
		runes = append(runes, r)
	}
	return strings.TrimSpace(string(runes))
}

// GitHub compares base (our build) to head (main): its ahead_by is how many
// commits our build is behind, not how many commits our build is ahead.
func parseUpdateCompare(body []byte, result *updateResult) error {
	var data struct {
		Status   string `json:"status"`
		AheadBy  *int   `json:"ahead_by"`
		BehindBy *int   `json:"behind_by"`
	}
	if json.Unmarshal(body, &data) != nil || data.AheadBy == nil || data.BehindBy == nil || *data.AheadBy < 0 || *data.BehindBy < 0 {
		return errors.New("confronto non valido")
	}
	ahead, behind := *data.AheadBy, *data.BehindBy
	switch {
	case data.Status == "identical" && ahead == 0 && behind == 0:
		result.State = "up_to_date"
	case data.Status == "ahead" && ahead > 0 && behind == 0:
		result.State = "behind"
	case data.Status == "behind" && ahead == 0 && behind > 0:
		result.State = "ahead_of_main"
	case data.Status == "diverged" && ahead > 0 && behind > 0:
		result.State = "diverged"
	default:
		return errors.New("stato del confronto non valido")
	}
	result.BehindBy = ahead
	result.Titles = []string{}

	return nil
}

func parseUpdateCommits(body []byte, result *updateResult, limit int) error {
	var data []struct {
		SHA    string `json:"sha"`
		Commit *struct {
			Message   *string `json:"message"`
			Committer *struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if json.Unmarshal(body, &data) != nil || len(data) == 0 || len(data) > limit {
		return errors.New("elenco commit non valido")
	}
	titles := make([]string, 0, len(data))
	var latest string
	for i, item := range data {
		if !updateCommitPattern.MatchString(item.SHA) || item.Commit == nil || item.Commit.Message == nil || item.Commit.Committer == nil {
			return errors.New("commit non valido")
		}
		date, err := time.Parse(time.RFC3339, item.Commit.Committer.Date)
		if err != nil {
			return errors.New("data commit non valida")
		}
		if i == 0 {
			latest = date.Format(time.RFC3339)
		}
		titles = append(titles, updateTitle(*item.Commit.Message))
	}
	result.Titles, result.LatestCommitDate = titles, latest
	return nil
}

func fetchUpdate(ctx context.Context, commit string, result *updateResult, count *int64) {
	if !updateCommitPattern.MatchString(commit) {
		result.State = "no_build_info"
		return
	}
	client := externalClient()
	client.Timeout = 15 * time.Second
	defer client.CloseIdleConnections()
	body, status, err := externalRead(ctx, client, updateAPIBase+"/repos/"+updateRepo+"/compare/"+commit+"...main?per_page=1", updateCompareLimit, count)
	if status == http.StatusNotFound {
		result.State = "unknown_commit"
		return
	}
	if err != nil {
		result.State, result.Error = "unreachable", "GitHub non raggiungibile"
		return
	}
	if parseUpdateCompare(body, result) != nil {
		result.State, result.Error = "unreachable", "Risposta GitHub non valida"
		result.BehindBy, result.Titles, result.LatestCommitDate = 0, []string{}, ""
		return
	}
	if result.State == "behind" || result.State == "diverged" {
		limit := min(20, result.BehindBy)
		body, _, err = externalRead(ctx, client, fmt.Sprintf("%s/repos/%s/commits?sha=main&per_page=%d", updateAPIBase, updateRepo, limit), updateBodyLimit, count)
		if err != nil {
			return
		}
		if err := parseUpdateCommits(body, result, limit); err != nil {
			return
		}
	}
}

// check reserves both the manual and background slot before making any request.
// Every admitted round releases the slot, publishes its result and logs once,
// including a recovered panic. Upstream errors never enter responses or logs.
func (c *updateChecker) check(ctx context.Context) (result updateResult, retry int) {
	result = localUpdateResult()
	if result.State != "pending" {
		return result, 0
	}
	now := time.Now()
	c.mu.Lock()
	if c.running || now.Sub(c.last) < updateCooldown {
		retry = max(1, int(math.Ceil((updateCooldown - now.Sub(c.last)).Seconds())))
		c.mu.Unlock()
		return c.status(), retry
	}
	c.running, c.last = true, now
	c.mu.Unlock()
	result.CheckedAt = now.UTC().Format(time.RFC3339)
	var count int64
	defer func() {
		if recover() != nil {
			result.State, result.Error = "unreachable", "Controllo aggiornamenti non riuscito"
		}
		c.mu.Lock()
		c.cached = result
		c.running = false
		c.mu.Unlock()
		log.Printf("update check state=%s duration=%s bytes=%d", result.State, time.Since(now).Round(time.Millisecond), count)
	}()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fetchUpdate(ctx, buildinfo.Info().Commit, &result, &count)
	return result, 0
}

func (c *updateChecker) loop(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if ctx.Err() != nil {
				return
			}
			c.check(ctx)
			timer.Reset(updateInterval)
		}
	}
}

// StartUpdateCheck waits one minute before the first check, then six hours.
func StartUpdateCheck(ctx context.Context) {
	updateChecks.start.Do(func() { go updateChecks.loop(ctx) })
}

func (h *Handler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, updateChecks.status())
}
func (h *Handler) Version(w http.ResponseWriter, r *http.Request) { writeJSON(w, buildinfo.Info()) }
func (h *Handler) UpdateCheck(w http.ResponseWriter, r *http.Request) {
	result, retry := updateChecks.check(r.Context())
	if retry > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(retry))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]any{"error": "Controllo già in corso o troppo recente", "retry_after": retry})
		return
	}
	writeJSON(w, result)
}
