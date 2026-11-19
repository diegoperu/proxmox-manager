package main

import (
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestInternalPricePreview(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "// pmgr:internal-price:begin", "// pmgr:internal-price:end"
	text := string(page)
	if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 {
		t.Fatal("internal price markers missing or duplicated")
	}
	start, finish := strings.Index(text, begin)+len(begin), strings.Index(text, end)
	if start >= finish {
		t.Fatal("internal price markers out of order")
	}
	// Both languages consume this fixture, including the expected amounts.
	fixture, err := os.ReadFile("../../internal/handlers/testdata/internal_price_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	driver := `
const assert=require('node:assert/strict');
const fixture=JSON.parse(require('node:fs').readFileSync(0,'utf8'));
for(const c of fixture.cases){
 const prices={...fixture.prices,...c.prices}, before=JSON.stringify(prices);
 const result=internalPrice(prices,c.guest);
 for(const [actual,expected] of [['vmTokens','vm_tokens'],['gpuTokens','gpu_tokens'],['tokens','tokens'],['eur','eur'],['gpuEUR','gpu_eur']]){
  assert.equal(result[actual],c[expected],c.name+': '+actual);
 }
 assert.equal(!!result.reason,c.eur===null,c.name+': unknown needs a reason');
 assert.equal(JSON.stringify(prices),before,'pure calculation must not mutate prices');
}
`
	cmd := exec.Command(node, "-e", text[start:finish]+driver)
	cmd.Stdin = strings.NewReader(string(fixture))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("internal price JavaScript: %v\n%s", err, out)
	}
}
