// Package buildinfo exposes validated, local build provenance.
package buildinfo

import (
	"regexp"
	"runtime/debug"
	"time"
)

// Set by build.sh with -ldflags -X. Empty fields fall back to Go's VCS metadata.
var Commit, Date, Dirty string

var readBuildInfo = debug.ReadBuildInfo
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type BuildInfo struct {
	Commit string `json:"commit"`
	Short  string `json:"short"`
	Date   string `json:"date"`
	Dirty  bool   `json:"dirty"`
	Known  bool   `json:"known"`
}

// Info never exposes an unvalidated revision or date.
func Info() BuildInfo {
	commit, date, dirty := Commit, Date, Dirty
	if b, ok := readBuildInfo(); ok {
		for _, setting := range b.Settings {
			switch setting.Key {
			case "vcs.revision":
				if Commit == "" {
					commit = setting.Value
				}
			case "vcs.time":
				if Date == "" {
					date = setting.Value
				}
			case "vcs.modified":
				if Dirty == "" {
					dirty = setting.Value
				}
			}
		}
	}
	info := BuildInfo{Dirty: dirty == "true"}
	if commitPattern.MatchString(commit) {
		info.Commit, info.Short, info.Known = commit, commit[:7], true
	}
	if t, err := time.Parse(time.RFC3339, date); err == nil {
		info.Date = t.Format(time.RFC3339)
	}
	return info
}
