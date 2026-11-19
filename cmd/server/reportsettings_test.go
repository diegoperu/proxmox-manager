package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestReportSettingsClientValidation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	const begin = "// pmgr:report-settings:begin"
	const end = "// pmgr:report-settings:end"
	if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 {
		t.Fatal("missing report settings markers")
	}
	start, finish := strings.Index(text, begin)+len(begin), strings.Index(text, end)
	driver := `
const assert=require('node:assert/strict');
const ui=ReportSettingsUI;
const empty=()=>({internal_prices:{gpu_tiers:[]},market_prices:[],usd_eur:0,storage_classes:{},defaults:{},interruptions:[]});
assert.doesNotThrow(()=>ui.validate(empty()));
for(const group of [ui.internal,ui.thresholds,ui.market]) for(const f of group){
 const defaults=group===ui.thresholds;
 for(const v of [f[2],f[3]])assert.equal(ui.number(String(v),f,defaults),v,f[0]);
 for(const v of [-1,f[3]+1,NaN,Infinity,'hello',' '])assert.throws(()=>ui.number(v,f,defaults),undefined,f[0]);
 if(f[2]>0&&!defaults)assert.throws(()=>ui.number('0',f));
 if(defaults)assert.equal(ui.number('0',f,true),0);
 if(f[4]==='int')assert.throws(()=>ui.number('1.5',f,defaults));
}
for(const s of ['2024-02-29','2026-11-19'])assert(ui.date(s));
for(const s of ['2025-02-29','2026-02-30','2026-1-01','x'])assert(!ui.date(s));
for(const s of ['2026-11-19T12:00','2026-11-19T12:00:00Z','2026-11-19T12:00:00.123456789+01:00'])assert(Number.isFinite(ui.time(s)),s);
for(const s of ['2026-02-30T12:00','2026-11-19T24:00','2026-11-19T12:60','2026-11-19T12:00:60Z','2026-11-19T12:00:00+24:00','2026-11-19T12:00:00,1Z','2026-11-19'])assert(Number.isNaN(ui.time(s)),s);
for(const s of ['a\nb','a\tb','a\x1bb','a\u202eb','a\u2066b'])assert(!ui.printable(s,300));
assert(ui.printable('Elettricità <testo> https://example.org/?a=b',300));
assert(ui.printable('è'.repeat(200),200));assert(!ui.printable('è'.repeat(201),200));
let r=empty();r.internal_prices.gpu_tiers=[{min_gib:16,tokens:1},{min_gib:89,tokens:11}];assert.doesNotThrow(()=>ui.validate(r));
r.internal_prices.gpu_tiers.reverse();assert.throws(()=>ui.validate(r));
r.internal_prices.gpu_tiers=[{min_gib:16,tokens:1},{min_gib:16,tokens:2}];assert.throws(()=>ui.validate(r));
r=empty();r.market_prices=[{name:'Provider',level:'low',currency:'EUR',vcpu_hour:.1,ram_gb_hour:0,storage_gb_month:0,gpu_hour:[{min_gib:16,price:.6}],source:'https://example.org/<text>',date:'2026-11-19'}];assert.doesNotThrow(()=>ui.validate(r));
for(const [key,bad] of [['name','\n'],['name','x'.repeat(65)],['currency','eur'],['level','medium'],['source','\u202e'],['date','2026-02-30'],['vcpu_hour',1001],['ram_gb_hour',-1],['storage_gb_month',Infinity]]){const c=structuredClone(r);c.market_prices[0][key]=bad;assert.throws(()=>ui.validate(c),undefined,key);}
r.market_prices[0].gpu_hour=[{min_gib:20,price:0},{min_gib:16,price:0}];assert.throws(()=>ui.validate(r));
for(const [key,val] of [['../disk','ssd'],['disk','SSD'],['x'.repeat(65),'hdd']]){r=empty();r.storage_classes[key]=val;assert.throws(()=>ui.validate(r));}
r=empty();r.interruptions=[{from:'2026-11-19T10:00:00Z',to:'2026-12-20T10:00:00Z',cause:'external',note:'Corrente'}];assert.doesNotThrow(()=>ui.validate(r));
for(const [key,bad] of [['to','2026-12-20T10:01:00Z'],['to','2026-11-19T10:00:00Z'],['cause','power'],['note','x'.repeat(201)],['note','\n']]){const c=structuredClone(r);c.interruptions[0][key]=bad;assert.throws(()=>ui.validate(c),undefined,key);}
for(const [key,max,val] of [['interruptions',500,r.interruptions[0]],['market_prices',6,{name:'p',level:'low',currency:'EUR',date:'2026-11-19',source:'',gpu_hour:[]}]]){const c=empty();c[key]=Array(max+1).fill(val);assert.throws(()=>ui.validate(c));}
r=empty();r.internal_prices.gpu_tiers=Array(9).fill({min_gib:0,tokens:0});assert.throws(()=>ui.validate(r));
`
	cmd := exec.Command(node, "-e", text[start:finish]+driver)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("report settings JavaScript: %v\n%s", err, out)
	}
}

func TestReportSettingsAPIGuard(t *testing.T) {
	for _, path := range []string{"/api/clusters/0/report-settings", "/api/clusters/1/report-settings"} {
		for _, method := range []string{"GET", "PUT"} {
			for _, tc := range []struct {
				origin, site, contentType string
				status                    int
			}{
				{"http://pm.lan:8080", "same-origin", "application/json", 204},
				{"http://evil.example", "cross-site", "application/json", 403},
				{"http://pm.lan:8080", "same-site", "application/json", 403},
				{"http://pm.lan:8080", "same-origin", "text/plain", 415},
			} {
				if method == "GET" && tc.status == 415 {
					continue
				}
				req := httptest.NewRequest(method, "http://pm.lan:8080"+path, nil)
				req.Header.Set("Origin", tc.origin)
				req.Header.Set("Sec-Fetch-Site", tc.site)
				req.Header.Set("Content-Type", tc.contentType)
				w := httptest.NewRecorder()
				apiGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, req)
				if w.Code != tc.status {
					t.Errorf("%s %s: got %d want %d", method, path, w.Code, tc.status)
				}
			}
		}
	}
}
