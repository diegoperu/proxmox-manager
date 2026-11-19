package handlers

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

type gpuCardUse struct {
	BusID       string   `json:"bus_id"`
	Name        string   `json:"name"`
	Util        *float64 `json:"util"`
	MemUsedMiB  *float64 `json:"mem_used_mib"`
	MemTotalMiB *float64 `json:"mem_total_mib"`
	TempC       *float64 `json:"temp_c"`
	PowerW      *float64 `json:"power_w"`
}

var gpuUseBusPattern = regexp.MustCompile(`^[0-9A-Fa-f]{4,8}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}\.[0-7]$`)
var gpuUseNumberPattern = regexp.MustCompile(`^[+]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func parseGPUUse(out string) (cards []gpuCardUse, rejected int) {
	truncated := len(out) > 8*1024
	if truncated {
		out = out[:8*1024]
	}
	lines := strings.Split(out, "\n")
	seen := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		seen++
		if seen > 8 || (truncated && i == len(lines)-1) {
			rejected++
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 7 {
			rejected++
			continue
		}
		for j := range fields {
			fields[j] = strings.TrimSpace(fields[j])
		}
		if !gpuUseBusPattern.MatchString(fields[0]) {
			rejected++
			continue
		}
		name := strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r > 0x7e {
				return -1
			}
			return r
		}, fields[1]))
		if len(name) > 64 {
			name = name[:64]
		}
		if name == "" {
			name = "GPU"
		}
		values := [5]*float64{}
		valid := true
		for j, max := range [...]float64{100, 1048576, 1048576, 150, 2000} {
			var ok bool
			values[j], ok = parseGPUUseNumber(fields[j+2], max)
			if !ok {
				valid = false
				break
			}
		}
		if !valid || (values[1] != nil && values[2] != nil && *values[1] > *values[2]) {
			rejected++
			continue
		}
		cards = append(cards, gpuCardUse{fields[0], name, values[0], values[1], values[2], values[3], values[4]})
	}
	return cards, rejected
}

func parseGPUUseNumber(s string, max float64) (*float64, bool) {
	switch s {
	case "", "[N/A]", "[Not Supported]", "N/A":
		return nil, true
	}
	if !gpuUseNumberPattern.MatchString(s) {
		return nil, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > max {
		return nil, false
	}
	return &n, true
}

func gpuUseExitState(exitcode int) string {
	switch exitcode {
	case 0:
		return "ok"
	case 3:
		return "no_driver"
	case 4:
		return "driver_error"
	default:
		return "error"
	}
}
