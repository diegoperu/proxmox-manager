package main

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/handlers"
)

func TestReportBuiltinDefaultsMatchJavaScript(t *testing.T) {
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	const begin = "// pmgr:report-defaults:begin"
	const end = "// pmgr:report-defaults:end"
	if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 {
		t.Fatal("expected exactly one pair of report defaults markers")
	}
	start, finish := strings.Index(text, begin)+len(begin), strings.Index(text, end)
	if start >= finish {
		t.Fatal("report defaults markers out of order")
	}
	declaration := strings.TrimSpace(text[start:finish])
	const prefix = "const REPORT_DEFAULTS = "
	if !strings.HasPrefix(declaration, prefix) || !strings.HasSuffix(declaration, ";") {
		t.Fatal("expected a single REPORT_DEFAULTS JSON object between markers")
	}
	var actual map[string]float64
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(declaration, prefix), ";")), &actual); err != nil {
		t.Fatal(err)
	}
	expected := handlers.BuiltinReportDefaults()
	fields := reflect.TypeOf(config.ReportDefaults{})
	if len(expected) != fields.NumField() {
		t.Fatalf("helper has %d keys; config.ReportDefaults has %d fields", len(expected), fields.NumField())
	}
	for i := 0; i < fields.NumField(); i++ {
		key := fields.Field(i).Tag.Get("json")
		if _, ok := expected[key]; !ok {
			t.Errorf("helper missing config.ReportDefaults key %q", key)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("JavaScript defaults = %v; handler defaults = %v (keys and values must match exactly)", actual, expected)
	}
}
