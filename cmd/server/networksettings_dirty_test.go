package main

import "testing"

func TestNetworkSettingsUnsavedChanges(t *testing.T) {
	source := settingsJS(t, "// pmgr:network:begin", "// pmgr:network:end") +
		settingsJS(t, "// pmgr:network-settings:begin", "// pmgr:network-settings:end") +
		settingsJS(t, "// pmgr:settings-guard:begin", "// pmgr:settings-guard:end") +
		settingsJS(t, "const ClusterMgr={", "\n};") +
		settingsJS(t, "const PMETA=", "// pmgr:autorefresh:begin") +
		settingsJS(t, "const AutoRefresh={", "\n};") +
		settingsJS(t, "const AutoRefreshUI={", "\n};") +
		settingsJS(t, "const App={", "\n};") +
		settingsJS(t, "const Settings={", "\n};") +
		settingsJS(t, "window.addEventListener('beforeunload'", "\n")
	runSettingsJS(t, `
const assert=require('node:assert/strict'),nodes=new Map(),listeners={};
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function element(){return {value:'',textContent:'',innerHTML:'',hidden:false,disabled:false,style:{},events:{},classList:{contains:()=>false,toggle(){}},setAttribute(){},removeAttribute(){},focus(){},addEventListener(event,fn){this.events[event]=fn;},querySelector:()=>null,querySelectorAll:()=>[]};}
const get=id=>{if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);};
global.document={hidden:false,activeElement:null,getElementById:get,querySelectorAll:selector=>selector==='#ns-networks [data-network]'?get('ns-networks').children:[],querySelector:()=>null};
global.window={addEventListener:(event,fn)=>listeners[event]=fn};
global.location={hash:'#/settings/infra'};
global.history={pushState(a,b,h){location.hash=h;},replaceState(a,b,h){location.hash=h;}};
let confirmation,confirmations=0;
global.askConfirm=(options,ok)=>{confirmation={options,ok};confirmations++;};
global.toast=()=>{};global.closeModal=()=>{};global.errBox=message=>esc(message);
global.Ceph={stop(){},probe(){}};global.Gpu={probe(){}};
global.VMs={clearCache(){},detail(){}};global.Nodes={};global.Batch={};
global.Groups={busy:false};global._modalStack=[];
global.ReportSettingsUI={isDirty:()=>false,clear(){},heading(){},load(){}};
global.Provision={resetNetwork(){}};
`+source+`
const ui=NetworkSettingsUI;
const empty=()=>({networks:[],dns:[],search_domain:''});
function row(value){
 const controls={cidr:{value:value.cidr,focus(){}},gateway:{value:value.gateway}};
 return {controls,querySelector:selector=>selector==='input'?controls.cidr:controls[/data-key="([^"]+)"/.exec(selector)[1]],remove(){const root=get('ns-networks');root.children=root.children.filter(child=>child!==this);}};
}
const render=ui.render.bind(ui);
ui.render=settings=>{
 const root={...element(),children:settings.networks.map(row),querySelectorAll(){return this.children;},insertAdjacentHTML(position,markup){this.children.push(row({cidr:'',gateway:''}));this.lastElementChild=this.children.at(-1);}};
 nodes.set('ns-networks',root);get('ns-dns').value=settings.dns.join(' ');get('ns-search').value=settings.search_domain;get('ns-save-error').textContent='';
 render(settings);
};
const edit=(id,value)=>{get(id).value=value;get('ns-form').events.input();};
const flush=()=>new Promise(setImmediate);
const button=(attribute,network=null)=>({target:{closest:()=>({hasAttribute:a=>a===attribute,closest:()=>network})}});
ClusterMgr.current=0;ClusterMgr.clusters=[{idx:0,label:'<Cluster & "test">'},{idx:1,label:'Other'}];
Settings.tab='infra';App.cur='settings';
let refreshes=0;App.refresh=()=>{refreshes++;};AutoRefreshUI.reset=()=>{};
let saved=empty();global.api=async()=>structuredClone(saved);
(async()=>{
 await ui.load();assert(!ui.isDirty());assert(get('ns-savebar').hidden);
 assert.equal(get('ns-heading').innerHTML,'Infrastruttura · &lt;Cluster &amp; &quot;test&quot;&gt;');
 assert(get('ns-editor').innerHTML.includes('<details class="rs-section" open>'));
 assert(get('ns-editor').innerHTML.includes('role="alert"'));
 assert(!ui.row({cidr:'<script>',gateway:'" onfocus="bad'}).includes('<script>'));
 assert(ui.row({gateway:'" onfocus="bad'}).includes('&quot; onfocus=&quot;bad'));
 edit('ns-dns','1.1.1.1');assert(ui.isDirty());assert(!get('ns-savebar').hidden);
 edit('ns-dns','');assert(!ui.isDirty());assert(get('ns-savebar').hidden);
 ui.render({...empty(),dns:['2001:db8::1'],search_domain:'example.org'});
 edit('ns-dns',' 2001:0DB8:0:0:0:0:0:1 ');edit('ns-search','EXAMPLE.ORG');assert(!ui.isDirty(),'normalized equivalent values are clean');
 edit('ns-dns','256.0.0.1');assert(ui.isDirty());let calls=0;global.api=async()=>{calls++;};await ui.save();assert.equal(calls,0);assert.equal(get('ns-save-error').textContent,'DNS 1: indirizzo IP non valido');
 edit('ns-dns','1.1.1.1 1.1.1.1');await ui.save();assert.equal(get('ns-save-error').textContent,'DNS 2: indirizzo ripetuto');
 ui.render(empty());ui.click(button('data-add'));assert(ui.isDirty(),'incomplete added row is dirty');await ui.save();assert.equal(calls,0);assert.equal(get('ns-save-error').textContent,'rete 1: indirizzo di rete IPv4 non valido (es. 10.75.4.0/24)');
 ui.click(button('data-remove',get('ns-networks').children[0]));assert(!ui.isDirty());
 for(let i=0;i<16;i++)ui.click(button('data-add'));assert(get('ns-add').disabled);ui.click(button('data-add'));assert.equal(get('ns-networks').children.length,16);
 ui.render(empty());edit('ns-dns','1.1.1.1');
 const baseline=ui.baseline;
 global.api=async(path,options)=>{assert.equal(path,'/clusters/0/network-settings');assert.equal(options.method,'PUT');assert.equal(options.quiet,true);throw new Error('Errore 400 <test>');};
 await ui.save();assert.equal(ui.baseline,baseline);assert(ui.isDirty());assert.equal(get('ns-dns').value,'1.1.1.1');assert.equal(get('ns-save-error').textContent,'Errore 400 <test>');assert(!get('ns-save').disabled);
 global.api=async(path,options)=>{saved=JSON.parse(options.body);return {status:'ok'};};
 edit('ns-search','EXAMPLE.ORG');await ui.save();assert(!ui.isDirty());assert(get('ns-savebar').hidden);assert.equal(saved.search_domain,'example.org');
 edit('ns-dns','9.9.9.9');let resolve;
 global.api=async(path,options)=>{saved=JSON.parse(options.body);return new Promise(done=>resolve=done);};
 const pending=ui.save();assert(get('ns-save').disabled);assert(get('ns-cancel').disabled);edit('ns-dns','8.8.8.8');resolve({status:'ok'});await pending;
 assert(ui.isDirty());assert.equal(get('ns-dns').value,'8.8.8.8');edit('ns-dns','9.9.9.9');assert(!ui.isDirty());
 edit('ns-dns','4.4.4.4');global.api=async()=>{throw new Error('Lettura fallita');};await ui.cancel();assert(ui.isDirty());assert.equal(get('ns-dns').value,'4.4.4.4');assert.equal(get('ns-save-error').textContent,'Lettura fallita');
 global.api=async()=>structuredClone(saved);await ui.cancel();assert(!ui.isDirty());assert(get('ns-savebar').hidden);assert.equal(get('ns-dns').value,'9.9.9.9');
 edit('ns-dns','1.1.1.1');
 const before=refreshes;App.nav('nodes');assert.equal(App.cur,'settings');assert.equal(refreshes,before);assert.equal(location.hash,'#/settings/infra');
 assert.equal(confirmation.options.title,'Hai modifiche non salvate alle impostazioni di rete di <Cluster & "test">. Uscire senza salvare?');assert.equal(confirmation.options.level,'warn');
 for(const tab of ['general','report']){Settings.selectTab(tab);assert.equal(Settings.tab,'infra');assert(ui.isDirty());}
 for(const hash of ['#/settings','#/settings/report','#/nodes','#/settings/invalid']){
  location.hash=hash;listeners.hashchange();assert.equal(location.hash,'#/settings/infra');assert.equal(App.cur,'settings');assert(ui.isDirty());
 }
 location.hash='#/settings/report';listeners.popstate();const count=confirmations;listeners.hashchange();assert.equal(confirmations,count);
 get('cluster-select').value=1;ClusterMgr.switchTo(1);assert.equal(ClusterMgr.current,0);assert.equal(get('cluster-select').value,0);assert(ui.isDirty());
 let prevented=false;const unload={preventDefault(){prevented=true;}};listeners.beforeunload(unload);assert(prevented);assert.equal(unload.returnValue,'');
 const state=AutoRefreshUI.state('nodes');assert(state.unsaved);assert(!AutoRefresh.shouldRun(state));
 await Settings.load();await ui.load();assert(ui.isDirty(),'refresh cannot replace the draft');
 confirmation.ok();assert.equal(ClusterMgr.current,1);assert(!ui.isDirty());
 ui.idx=1;ui.render(empty());edit('ns-dns','1.1.1.1');Settings.selectTab('general');confirmation.ok();assert.equal(Settings.tab,'general');assert.equal(location.hash,'#/settings');assert(!ui.isDirty());
 Settings.showTab('infra');ui.idx=1;ui.render(empty());edit('ns-dns','1.1.1.1');App.nav('nodes');confirmation.ok();assert.equal(App.cur,'nodes');assert.equal(location.hash,'#/nodes');assert(!ui.isDirty());
 App.cur='settings';Settings.showTab('infra');ui.idx=1;ui.render(empty());edit('ns-dns','1.1.1.1');location.hash='#/groups';listeners.hashchange();confirmation.ok();assert.equal(App.cur,'groups');assert.equal(location.hash,'#/groups');assert(!ui.isDirty());
 prevented=false;listeners.beforeunload({preventDefault(){prevented=true;}});assert(!prevented);
 App.cur='settings';Settings.showTab('infra');ui.idx=1;ui.render(empty());edit('ns-dns','1.1.1.1');
 global.api=async()=>new Promise(done=>resolve=done);const stale=ui.save();ui.clear();ClusterMgr.current=0;ui.idx=0;ui.render(empty());edit('ns-dns','8.8.8.8');resolve({status:'ok'});await stale;
 assert(ui.isDirty());assert.equal(get('ns-dns').value,'8.8.8.8');assert(!get('ns-save').disabled);
 ui.clear();const reads=[];global.api=()=>new Promise(done=>reads.push(done));const first=ui.load();ClusterMgr.current=1;const second=ui.load();reads[1]({...empty(),dns:['9.9.9.9']});await second;reads[0]({...empty(),dns:['1.1.1.1']});await first;assert.equal(ui.idx,1);assert.equal(get('ns-dns').value,'9.9.9.9');
 ClusterMgr.clusters=[];await ui.load();assert.equal(ui.idx,null);assert.equal(get('ns-editor').textContent,'Nessun cluster configurato');
 await flush();
})().catch(err=>{console.error(err);process.exitCode=1;});
`)
}
