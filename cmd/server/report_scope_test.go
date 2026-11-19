package main

import (
	"io/fs"
	"strings"
	"testing"
)

// dateFmt is a local helper of Reports.docHTML: used anywhere else it is a ReferenceError at run time
// (the manager report failed with "dateFmt is not defined" whenever the group-rule note was present).
func TestReportDateFmtStaysLocal(t *testing.T) {
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	start := strings.Index(text, "  docHTML(L,d,eff){")
	if start < 0 {
		t.Fatal("docHTML not found")
	}
	end := start + strings.Index(text[start:], "\n  },\n")
	for i := 0; ; {
		j := strings.Index(text[i:], "dateFmt")
		if j < 0 {
			break
		}
		if pos := i + j; pos < start || pos > end {
			t.Fatalf("dateFmt used outside Reports.docHTML at byte %d: use rptDate(s, loc)", pos)
		}
		i += j + len("dateFmt")
	}
}
