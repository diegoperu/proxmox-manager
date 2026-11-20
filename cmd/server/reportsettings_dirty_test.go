package main

import (
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

func TestReportSettingsUnsavedChanges(t *testing.T) {
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
		if strings.Count(text, begin) != 1 {
			t.Fatalf("missing or duplicated %q", begin)
		}
		start := strings.Index(text, begin)
		finish := strings.Index(text[start:], end)
		if finish < 0 {
			t.Fatalf("missing %q after %q", end, begin)
		}
		return text[start : start+finish+len(end)]
	}
	source := section("// pmgr:report-settings:begin", "// pmgr:report-settings:end") + "\n" +
		section("const ClusterMgr={", "\n};") + "\n" +
		section("const PMETA=", "// pmgr:autorefresh:begin") + "\n" +
		section("const AutoRefresh={", "\n};") + "\n" +
		section("const AutoRefreshUI={", "\n};") + "\n" +
		section("const App={", "\n};") + "\n" +
		section("const Settings={", "\n};") + "\n" +
		section("// pmgr:settings-guard:begin", "// pmgr:settings-guard:end") + "\n" +
		section("window.addEventListener('beforeunload'", "\n")
	driver := `
const assert=require('node:assert/strict');
const nodes=new Map(),listeners={};
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function element(){return {value:'',textContent:'',innerHTML:'',hidden:false,disabled:false,style:{},events:{},
 classList:{contains:()=>false,toggle(){}},setAttribute(){},removeAttribute(){},focus(){},
 addEventListener(event,fn){this.events[event]=fn;},querySelector:()=>null,querySelectorAll:()=>[]};}
const get=id=>{if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);};
global.document={hidden:false,activeElement:null,getElementById:get,querySelectorAll:()=>[],querySelector:()=>null};
global.window={addEventListener:(event,fn)=>listeners[event]=fn};
global.location={hash:'#/settings/report'};
global.history={pushState(a,b,h){location.hash=h;},replaceState(a,b,h){location.hash=h;}};
let confirmation,confirmations=0;
global.askConfirm=(options,ok)=>{confirmation={options,ok};confirmations++;};
global.UpdateUI={load(){}};
global.toast=()=>{};global.closeModal=()=>{};
global.Ceph={stop(){},probe(){}};global.Gpu={probe(){}};
global.VMs={clearCache(){},detail(){}};global.Nodes={};global.Batch={};
global.Groups={busy:false};global._modalStack=[];
global.NetworkSettingsUI={isDirty:()=>false,clear(){},heading(){},load(){}};
global.Provision={resetNetwork(){}};
`
	checks := `
const ui=ReportSettingsUI;
const empty=()=>({internal_prices:{gpu_tiers:[]},market_prices:[],usd_eur:0,storage_classes:{},defaults:{},interruptions:[]});
// Model form controls only. Production render(), read(), validation and event handlers run unchanged.
function root(values={},children=[]){
 const controls=Object.fromEntries(Object.entries(values).map(([k,v])=>[k,{value:String(v??''),validity:{badInput:false},focus(){}}]));
 return {...element(),controls,children,
  querySelector(selector){const match=/data-key="([^"]+)"/.exec(selector);return match?controls[match[1]]:null;},
  querySelectorAll(selector){const match=/data-key="([^"]+)"/.exec(selector);return match?this.children.map(row=>row.controls[match[1]]):this.children;}};
}
const productionRender=ui.render.bind(ui);
ui.render=r=>{
 const p=r.internal_prices??{};
 nodes.set('rs-internal',root(Object.fromEntries(ui.internal.map(f=>[f[0],p[f[0]]??(f[5]==='optional'?'':0)]))));
 nodes.set('rs-defaults',root(Object.fromEntries(ui.thresholds.map(f=>[f[0],r.defaults?.[f[0]]?ui.decimalShift(r.defaults[f[0]],f[5]===100?2:0):'']))));
 nodes.set('rs-tiers',root({},(p.gpu_tiers??[]).map(t=>root(t))));
 nodes.set('rs-market',root({},(r.market_prices??[]).map(p=>root(p,(p.gpu_hour??[]).map(t=>root(t))))));
 nodes.set('rs-storage',root({},Object.entries(r.storage_classes??{}).map(([name,value])=>root({name,class:value}))));
 nodes.set('rs-interruptions',root({},(r.interruptions??[]).map(row=>root(row))));
 nodes.set('rs-exchange',root({usd_eur:r.usd_eur??0}));
 get('rs-savebar').hidden=true;get('rs-save-error').textContent='';
 document.querySelectorAll=selector=>{
  const [id,sub]=selector.split(' ');return get(id.slice(1)).querySelectorAll(sub);
 };
 document.querySelector=selector=>selector==='#rs-exchange input'?get('rs-exchange').controls.usd_eur:null;
 productionRender(r);
};
const field=(key,group='rs-internal')=>get(group).controls[key];
const edit=(key,value,group)=>{field(key,group).value=value;get('rs-form').events.input();};
const flush=()=>new Promise(setImmediate);
ClusterMgr.current=0;ClusterMgr.clusters=[{idx:0,label:'<CALCOLO & "test">'},{idx:1,label:'Other'}];
Settings.tab='report';App.cur='settings';
let refreshes=0;App.refresh=()=>{refreshes++;};AutoRefreshUI.reset=()=>{};
let saved=empty();global.api=async()=>structuredClone(saved);
(async()=>{
 await ui.load();
 assert(!ui.isDirty(),'load is clean');assert(get('rs-savebar').hidden);
 assert(get('rs-editor').innerHTML.includes('role="region" aria-label="Salvataggio dei parametri dei report" hidden'));
 edit('token_eur','2');assert(ui.isDirty());assert(!get('rs-savebar').hidden);
 edit('token_eur','0.00');assert(!ui.isDirty(),'reverting the serialized value is clean');assert(get('rs-savebar').hidden);
 edit('inactive_cpu','2.9','rs-defaults');assert(ui.isDirty());assert.equal(ui.read().defaults.inactive_cpu,.029);
 edit('inactive_cpu','','rs-defaults');assert(!ui.isDirty());
 edit('token_eur','1001');assert(ui.isDirty());
 let calls=0;global.api=async()=>{calls++;};await ui.save();assert.equal(calls,0);assert(get('rs-save-error').textContent.includes('non valido'));
 edit('token_eur','0');field('cores_block').validity.badInput=true;get('rs-form').events.input();assert(ui.isDirty(),'incomplete numeric input is dirty');assert.throws(()=>ui.read(),/numero non valido/);
 field('cores_block').validity.badInput=false;get('rs-form').events.input();assert(!ui.isDirty());
 get('rs-storage').children=[root({name:'disk',class:'ssd'})];get('rs-form').events.change();assert(ui.isDirty());
 get('rs-storage').children.push(root({name:'disk',class:'ssd'}));assert.doesNotThrow(()=>ui.isDirty());assert.throws(()=>ui.read(),/duplicato/);
 get('rs-storage').children=[];get('rs-form').events.change();assert(!ui.isDirty());
 assert.equal(ui.serialize({b:1,a:2}),ui.serialize({a:2,b:1}));

 // Applying a price proposal changes the serialized settings, even without a DOM input event.
 const provider={name:'Old',level:'low',currency:'EUR',vcpu_hour:0,ram_gb_hour:0,storage_gb_month:0,source:'',date:'2026-11-19',gpu_hour:[]};
 ui.render({...empty(),market_prices:[provider]});ui.proposals=[{provider_name:'New',level:'low',vcpu_hour:.1,ram_gb_hour:.2,source_text:'Public price list',date:'2026-11-19',gpu_hour:[]}];
 ui.showPrices=()=>{};ui.applyPrice(0);assert(ui.isDirty());assert(!get('rs-savebar').hidden);

 // The server error stays next to the bar and the draft survives a rejected PUT.
 const baseline=ui.baseline;
 global.api=async(path,options)=>{assert.equal(path,'/clusters/0/report-settings');assert.equal(options.method,'PUT');throw new Error('Errore server 400 <test>');};
 await ui.save();assert.equal(ui.baseline,baseline);assert(ui.isDirty());assert.equal(get('rs-save-error').textContent,'Errore server 400 <test>');assert(!get('rs-save').disabled);
 global.api=async(path,options)=>{saved=JSON.parse(options.body);return {status:'ok'};};
 await ui.save();assert(!ui.isDirty());assert(get('rs-savebar').hidden);assert.equal(get('rs-save-error').textContent,'');assert(get('rs-status').innerHTML.includes('Parte economica: attiva'));

 // Edits made while a PUT is pending remain unsaved after it succeeds.
 edit('token_eur','2');let resolve;
 global.api=async(path,options)=>{saved=JSON.parse(options.body);await new Promise(done=>resolve=done);return {status:'ok'};};
 const pending=ui.save();assert(get('rs-save').disabled);assert(get('rs-cancel').disabled);edit('token_eur','3');resolve();await pending;
 assert(ui.isDirty());assert.equal(field('token_eur').value,'3');edit('token_eur','2');assert(!ui.isDirty());

 // Annulla reloads saved values. A failed GET keeps edits and shows the error.
 edit('token_eur','4');global.api=async()=>{throw new Error('Lettura fallita');};await ui.cancel();assert(ui.isDirty());assert.equal(field('token_eur').value,'4');assert.equal(get('rs-save-error').textContent,'Lettura fallita');
 global.api=async()=>structuredClone(saved);await ui.cancel();assert(!ui.isDirty());assert(get('rs-savebar').hidden);assert.equal(field('token_eur').value,'2');

 // Every exit waits for confirmation; cancel leaves route, cluster and draft unchanged.
 edit('token_eur','5');
 const before=refreshes;App.nav('nodes');assert.equal(App.cur,'settings');assert.equal(refreshes,before);assert.equal(location.hash,'#/settings/report');
 assert.equal(confirmation.options.level,'warn');assert.equal(confirmation.options.title,'Hai modifiche non salvate alle impostazioni dei report di <CALCOLO & "test">. Uscire senza salvare?');
 assert(ui.isDirty());
 Settings.selectTab('general');assert.equal(Settings.tab,'report');assert.equal(location.hash,'#/settings/report');
 for(const hash of ['#/settings','#/settings/infra','#/nodes','#/settings/invalid']){
  location.hash=hash;listeners.hashchange();assert.equal(location.hash,'#/settings/report');assert.equal(App.cur,'settings');assert(ui.isDirty());
 }
 location.hash='#/settings';listeners.popstate();const count=confirmations;listeners.hashchange();assert.equal(confirmations,count,'popstate + hashchange do not open duplicate confirmations');
 get('cluster-select').value=1;ClusterMgr.switchTo(1);assert.equal(ClusterMgr.current,0);assert.equal(get('cluster-select').value,0);assert(ui.isDirty());
 let prevented=false;const unload={preventDefault(){prevented=true;}};listeners.beforeunload(unload);assert(prevented);assert.equal(unload.returnValue,'');
 const state=AutoRefreshUI.state('nodes');assert.equal(state.unsaved,true);assert(!AutoRefresh.shouldRun(state));
 await Settings.load();assert(ui.isDirty(),'refresh cannot overwrite unsaved edits');

 // Accept cluster switch, tab exit, direct navigation and history navigation.
 confirmation.ok();assert.equal(ClusterMgr.current,1);assert.equal(get('cluster-select').value,1);assert(!ui.isDirty());
 ui.idx=1;ui.render(empty());edit('token_eur','1');Settings.selectTab('general');confirmation.ok();assert.equal(Settings.tab,'general');assert.equal(location.hash,'#/settings');assert(!ui.isDirty());
 Settings.showTab('report');ui.idx=1;ui.render(empty());edit('token_eur','1');App.nav('nodes');confirmation.ok();assert.equal(App.cur,'nodes');assert.equal(location.hash,'#/nodes');assert(!ui.isDirty());
 App.cur='settings';Settings.showTab('report');ui.idx=1;ui.render(empty());edit('token_eur','1');location.hash='#/groups';listeners.hashchange();confirmation.ok();assert.equal(App.cur,'groups');assert.equal(location.hash,'#/groups');assert(!ui.isDirty());
 prevented=false;listeners.beforeunload({preventDefault(){prevented=true;}});assert(!prevented);

 // A PUT response from a discarded editor must not reset the newly loaded form.
 App.cur='settings';Settings.showTab('report');ui.idx=1;ui.render(empty());edit('token_eur','1');
 global.api=async()=>new Promise(done=>resolve=done);const stale=ui.save();ui.clear();ui.idx=0;ui.render(empty());edit('token_eur','7');resolve({status:'ok'});await stale;assert(ui.isDirty());assert.equal(field('token_eur').value,'7');assert(!get('rs-save').disabled);assert(!get('rs-cancel').disabled);
 await flush();
})().catch(err=>{console.error(err);process.exitCode=1;});
`
	if out, err := exec.Command(node, "-e", driver+source+checks).CombinedOutput(); err != nil {
		t.Fatalf("report settings unsaved changes JavaScript: %v\n%s", err, out)
	}
}
