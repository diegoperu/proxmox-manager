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
global.RoleUI={readonly(){}};
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
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

// Percent inputs retain the persisted fractions, including decimal boundaries.
const threshold=key=>ui.thresholds.find(f=>f[0]===key);
for(const key of ['inactive_cpu','oversized_mem','oversized_cpu']){
 const f=threshold(key);
 for(const stored of [0,.007,.029,.29,f[3]]){
  const display=ui.decimalShift(stored,2);
  assert.equal(ui.thresholdNumber(String(display),f),stored,key);
  if(stored)assert(ui.field(f,stored,true).includes('value="'+display+'"'),key);
 }
 assert.equal(ui.thresholdNumber('',f),0);
 assert.throws(()=>ui.thresholdNumber(String(ui.decimalShift(f[3],2)+.001),f));
 assert.throws(()=>ui.thresholdNumber('-0.001',f));
}
assert.equal(ui.thresholdNumber('25',threshold('over_mem')),25);
assert.throws(()=>ui.thresholdNumber('6',threshold('idle_days')));
assert.throws(()=>ui.thresholdNumber('7.5',threshold('idle_days')));
assert(ui.field(threshold('inactive_cpu'),0,true).includes('placeholder="Predefinito: 2 %"'));
assert(ui.field(threshold('oversized_mem'),.25,true).includes('value="25"'));
assert(ui.field(threshold('oversized_cpu'),null,true).includes('placeholder="Predefinito: 10 %"'));
assert(ui.field(threshold('inactive_net'),0,true).includes('placeholder="Predefinito: 1024"'));
for(const f of ui.thresholds)assert(ui.field(f,0,true).includes('aria-describedby="rs-hint-'+f[0]+'"'));

// Exercise read() through the same validation and serialization path as Save.
const inputs={inactive_cpu:'2.9',oversized_mem:'29',oversized_cpu:'0.7',over_mem:'35'};
const controls={querySelector:selector=>({value:inputs[/data-key="([^"]+)"/.exec(selector)[1]]??''}),querySelectorAll:()=>[]};
global.document={getElementById:()=>controls,querySelector:()=>({value:'0'}),querySelectorAll:()=>[]};
const saved=JSON.parse(JSON.stringify(ui.read()));
assert.equal(saved.defaults.inactive_cpu,.029);
assert.equal(saved.defaults.oversized_mem,.29);
assert.equal(saved.defaults.oversized_cpu,.007);
assert.equal(saved.defaults.over_mem,35);
assert.equal(saved.defaults.inactive_days,0);
inputs.inactive_cpu='50.001';assert.throws(()=>ui.read());

// EconomicEnabled parity: quantities alone never activate the economic part.
r=empty();assert(ui.status(r).includes('Parte economica: disabilitata'));
r.internal_prices={base_cores:16,base_mem_gib:32,base_ssd_gb:100,gpu_tiers:[]};
assert(ui.status(r).includes('Parte economica: disabilitata'));
for(const key of ['token_eur','gpu_token_eur','base_tokens','cores_block_tokens','mem_block_tokens','ssd_block_tokens','hdd_block_tokens']){
 const c=empty();c.internal_prices[key]=1;assert(ui.status(c).includes('Parte economica: attiva'),key);
}
r=empty();r.internal_prices.gpu_tiers=[{min_gib:80,tokens:1}];assert(ui.status(r).includes('Parte economica: attiva'));
for(const key of ['vcpu_hour','ram_gb_hour','storage_gb_month','gpu_hour']){
 r=empty();r.market_prices=[{level:'low',[key]:key==='gpu_hour'?[{min_gib:80,price:1}]:1}];
 assert(ui.status(r).includes('Parte economica: attiva'),key);
}
r=empty();r.market_prices=[{date:'2026-02-30'},{date:'2026-11-19'},{date:'2026-09-01'}];r.interruptions=[{},{}];
const strip=ui.status(r);assert(strip.includes('<span>2026-11-19</span>'));assert(!strip.includes('2026-02-30'));assert(strip.includes('<span>2</span>'));
assert(strip.includes('parti con prezzi mancanti')&&strip.includes('<li>listino interno: gettone VM, gettone GPU'));
assert(strip.includes('mercato basso:'));

// Rendered structure, escaped cluster heading, and stale report response rejection.
(async()=>{
 const nodes=new Map();global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{innerHTML:'',textContent:'',classList:{toggle(){}},addEventListener(){},querySelector:()=>({value:''}),querySelectorAll:()=>[]});return nodes.get(id);}};
 const read=ui.read;ui.read=()=>empty();ui.render(empty());ui.read=read;const markup=document.getElementById('rs-editor').innerHTML;
 assert.deepEqual([...markup.matchAll(/<details class="rs-section"( open)?><summary>([^<]+)<\/summary>/g)].map(m=>[m[2],!!m[1]]),[
  ['Listino interno',true],['Prezzi di mercato',true],['Classi degli storage',false],['Registro delle interruzioni',false],['Soglie avanzate',false]
 ]);
 assert(markup.includes('class="btn ba sm" data-price-check'));
 assert(markup.includes('id="rs-save"'));
 global.ClusterMgr={current:0,clusters:[{idx:0},{idx:1}],currentLabel:()=>'<CALCOLO & "test">'};
 const pending=[];global.api=path=>path.startsWith('/storage?')?Promise.resolve([]):new Promise(resolve=>pending.push(resolve));
 const rendered=[];ui.render=data=>rendered.push(data.cluster);
 const first=ui.load();ClusterMgr.current=1;const second=ui.load();
 pending[1]({cluster:1});await second;pending[0]({cluster:0});await first;
 assert.deepEqual(rendered,[1]);assert.equal(ui.idx,1);
 assert.equal(document.getElementById('rs-heading').innerHTML,'Report e listini · &lt;CALCOLO &amp; &quot;test&quot;&gt;');
 ClusterMgr.clusters=[];await ui.load();assert.equal(ui.idx,null);assert.equal(document.getElementById('rs-editor').textContent,'Nessun cluster configurato');
})().catch(err=>{console.error(err);process.exitCode=1;});
`
	cmd := exec.Command(node, "-e", text[start:finish]+driver)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("report settings JavaScript: %v\n%s", err, out)
	}
}

func TestReportSettingsAPIGuard(t *testing.T) {
	for _, path := range []string{"/api/clusters/0/report-settings", "/api/clusters/1/report-settings", "/api/clusters/0/network-settings"} {
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

func TestSettingsTabsAndClusterBinding(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	section := func(begin, end string) string {
		t.Helper()
		start := strings.Index(text, begin)
		if start < 0 {
			t.Fatalf("missing %q", begin)
		}
		finish := strings.Index(text[start:], end)
		if finish < 0 {
			t.Fatalf("missing %q after %q", end, begin)
		}
		return text[start : start+finish+len(end)]
	}
	source := section("const ClusterMgr={", "\n};") + "\n" + section("const PMETA=", "// pmgr:autorefresh:begin") + "\n" + section("const Settings={", "\n};") + "\n" + section("// pmgr:settings-guard:begin", "// pmgr:settings-guard:end")
	driver := `
const assert=require('node:assert/strict');
const elements=new Map(),listeners={};
global.Auth={locked:false};global._IS_CONSOLE=false;
global.document={getElementById(id){if(!elements.has(id))elements.set(id,{innerHTML:'',value:'',style:{},classList:{contains:()=>false,toggle(){}},setAttribute(k,v){this[k]=v;}});return elements.get(id);},querySelectorAll:()=>[]};
global.window={addEventListener:(event,fn)=>listeners[event]=fn};
global.location={hash:'#/settings'};
global.history={pushState(a,b,h){location.hash=h;},replaceState(a,b,h){location.hash=h;}};
global.esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const clusters=[{idx:0,label:'Default',default:true},{idx:1,label:'Other'}];
const config={theme:'',density:'',refresh_seconds:0};
global.api=async path=>path==='/clusters'?clusters:config;
global.applyTheme=()=>{};global.applyDensity=()=>{};
global.AutoRefreshUI={reset(){},configure(){}};
global.VMs={clearCache(){},detail(){}};global.Nodes={};global.Batch={};
let loads=[],networkLoads=[];
global.NetworkSettingsUI={isDirty:()=>false,guardLeave:()=>false,clear(){},heading(){},load(){networkLoads.push(ClusterMgr.current);}};
global.Provision={resetNetwork(){}};
global.ReportSettingsUI={isDirty:()=>false,guardLeave:()=>false,clear(){},heading(){},load(){loads.push(ClusterMgr.current);}};
global.closeModal=()=>{};
global.App={cur:'settings',nav(page,opts){this.cur=page;if(page==='settings')Settings.showTab(opts.settingsTab??'general');},refresh(){return Settings.load();}};
`
	checks := `
(async()=>{
 assert.deepEqual(Route.parse('#/settings'),{page:'settings'});
 assert.deepEqual(Route.parse('#/settings/report'),{page:'settings',tab:'report'});
 assert.deepEqual(Route.parse('#/settings/infra'),{page:'settings',tab:'infra'});
 for(const bad of ['#/settings/infra/','#/settings/infra?cluster=1','#/settings/%69nfra','#/settings/infra<script>','#/settings/other','#/settings/report/','#/settings/report?cluster=1','#/settings/%72eport','#/settings/report<script>'])assert.equal(Route.parse(bad),null);
 Settings.selectTab('report');assert.equal(location.hash,'#/settings/report');assert.equal(Settings.tab,'report');
 assert.equal(document.getElementById('settings-tab-report')['aria-selected'],'true');
 assert.equal(document.getElementById('settings-tab-report').tabIndex,0);
 assert.equal(document.getElementById('settings-tab-general').tabIndex,-1);
 assert.equal(document.getElementById('settings-general').hidden,true);
 assert.equal(document.getElementById('settings-report').hidden,false);
 location.hash='#/settings';listeners.popstate();assert.equal(Settings.tab,'general');
 location.hash='#/settings/report';listeners.popstate();assert.equal(Settings.tab,'report');assert.equal(location.hash,'#/settings/report');
 location.hash='#/settings';listeners.hashchange();assert.equal(Settings.tab,'general');
 location.hash='#/settings/report';listeners.hashchange();assert.equal(Settings.tab,'report');
 location.hash='#/settings/other';listeners.hashchange();assert.equal(App.cur,'dashboard');assert.equal(location.hash,'#/dashboard');
 Settings.selectTab('other');assert.equal(Settings.tab,'report');
 Settings.selectTab('infra');assert.equal(location.hash,'#/settings/infra');assert.equal(Settings.tab,'infra');
 assert.equal(document.getElementById('settings-tab-infra')['aria-selected'],'true');
 assert.equal(document.getElementById('settings-tab-infra').tabIndex,0);
 assert.equal(document.getElementById('settings-tab-report').tabIndex,-1);
 assert.equal(document.getElementById('settings-infra').hidden,false);
 assert.equal(document.getElementById('settings-report').hidden,true);
 location.hash='#/settings/report';listeners.popstate();assert.equal(Settings.tab,'report');
 location.hash='#/settings/infra';listeners.hashchange();assert.equal(Settings.tab,'infra');
 ClusterMgr.current=1;await Settings.load();assert.equal(networkLoads.at(-1),1);
 Settings.showTab('report');
 App.cur='settings';ClusterMgr.current=1;loads=[];
 await Settings.load();assert.equal(ClusterMgr.current,1);assert.deepEqual(loads,[1]);
 assert.equal(document.getElementById('cluster-select').value,1);
 ClusterMgr.switchTo(0);await new Promise(setImmediate);assert.equal(ClusterMgr.current,0);assert.deepEqual(loads,[1,0]);
 ClusterMgr.switchTo(999);ClusterMgr.switchTo('1bad');assert.equal(ClusterMgr.current,0);
 // Resolve overlapping cluster-list refreshes in reverse order.
 const pending=[];global.api=path=>path==='/clusters'?new Promise(resolve=>pending.push(resolve)):Promise.resolve(config);
 ClusterMgr.current=0;const first=Settings.load();ClusterMgr.current=1;const second=Settings.load();
 pending[1](clusters);await second;
 pending[0](clusters);await first;
 assert.equal(ClusterMgr.current,1);assert.equal(loads.at(-1),1);
})().catch(err=>{console.error(err);process.exitCode=1;});
`
	if out, err := exec.Command(node, "-e", driver+source+checks).CombinedOutput(); err != nil {
		t.Fatalf("settings tabs JavaScript: %v\n%s", err, out)
	}
}
