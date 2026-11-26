package main

import "testing"

func TestNetworkSettingsUnsavedChanges(t *testing.T) {
	source := archUISource(t) + authUISource(t, "// pmgr:auth-ui:begin", "// pmgr:auth-ui:end") +
		settingsJS(t, "// pmgr:network:begin", "// pmgr:network:end") +
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
function element(){return {value:'',textContent:'',innerHTML:'',hidden:false,disabled:false,style:{},events:{},options:[],classList:{contains:()=>false,toggle(){}},setAttribute(){},removeAttribute(){},focus(){},addEventListener(event,fn){this.events[event]=fn;},querySelector:()=>null,querySelectorAll:()=>[]};}
const get=id=>{if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);};
global.Auth={locked:false,role:'admin'};global.RoleUI={readonly(){},canPage:()=>true};global._IS_CONSOLE=false;
global.document={hidden:false,activeElement:null,getElementById:get,querySelectorAll:selector=>selector==='#ns-networks [data-network]'?get('ns-networks').children:selector==='#ns-manager-from [data-manager-from]'?get('ns-manager-from').children:selector==='#ns-profile-seg button'?profilePicks:selector==='#ns-ip-mode-seg button'?modePicks:[],querySelector:()=>null};
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
global.UpdateUI={load(){}};
`+source+`
const ui=NetworkSettingsUI;
const profilePicks=['standard','hosting'].map(profile=>({...element(),dataset:{profile}}));
const profileButton=profile=>({target:{closest:()=>({hasAttribute:a=>a==='data-profile',dataset:{profile}})}});
const modePicks=['static','dhcp','both'].map(ipMode=>({...element(),dataset:{ipMode}}));
const modeButton=ipMode=>({target:{closest:()=>({hasAttribute:a=>a==='data-ip-mode',dataset:{ipMode}})}});
const empty=()=>({networks:[],dns:[],search_domain:''});
function row(value){
 const controls={cidr:{value:value.cidr,focus(){}},gateway:{value:value.gateway}};
 return {controls,querySelector:selector=>selector==='input'?controls.cidr:controls[/data-key="([^"]+)"/.exec(selector)[1]],remove(){const root=get('ns-networks');root.children=root.children.filter(child=>child!==this);}};
}
const render=ui.render.bind(ui);
function managerRow(value){const input={value,focus(){}};return {querySelector:()=>input,remove(){const root=get('ns-manager-from');root.children=root.children.filter(child=>child!==this);}};}
ui.render=settings=>{
 const root={...element(),children:settings.networks.map(row),querySelectorAll(){return this.children;},insertAdjacentHTML(position,markup){this.children.push(row({cidr:'',gateway:''}));this.lastElementChild=this.children.at(-1);}};
 nodes.set('ns-networks',root);get('ns-dns').value=settings.dns.join(' ');get('ns-search').value=settings.search_domain;get('ns-save-error').textContent='';
 nodes.set('ns-manager-from',{...element(),children:(settings.manager_from??[]).map(managerRow),querySelectorAll(){return this.children;},insertAdjacentHTML(){this.children.push(managerRow(''));this.lastElementChild=this.children.at(-1);}});
 get('ns-profile').value=settings.snippet_profile||'standard';
 get('ns-ip-mode').value=settings.ip_mode||'static';
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
 assert(get('ns-editor').innerHTML.includes('Indirizzi delle VM nuove'));
 assert(get('ns-editor').innerHTML.includes('data-ip-mode="static" class="on" aria-pressed="true"'));
 assert.equal(ui.read().ip_mode,'static');
 for(const role of ['reader','operator']){Auth.role=role;ui.click(modeButton('dhcp'));assert.equal(ui.read().ip_mode,'static');assert(!ui.isDirty());}
 Auth.role='admin';ui.click(modeButton('unexpected'));assert(!ui.isDirty());
 for(const mode of ['dhcp','both']){
  ui.click(modeButton(mode));assert(ui.isDirty());assert.equal(ui.read().ip_mode,mode);
  assert(ui.guardLeave(()=>{}));assert.equal(confirmation.options.level,'warn');
  global.api=async(path,options)=>{saved=JSON.parse(options.body);assert.equal(saved.ip_mode,mode);return {status:'ok'};};
  await ui.save();assert(!ui.isDirty());
  ui.click(modeButton('static'));assert(ui.isDirty());global.api=async()=>structuredClone(saved);await ui.cancel();
  assert.equal(ui.read().ip_mode,mode);assert(!ui.isDirty());
  assert(get('ns-editor').innerHTML.includes('data-ip-mode="'+mode+'" class="on" aria-pressed="true"'));
 }
 ui.render(empty());
 assert(get('ns-editor').innerHTML.includes('Profilo degli snippet'));
 assert(get('ns-editor').innerHTML.includes('data-profile="standard" class="on" aria-pressed="true"'));
 assert(get('ns-editor').innerHTML.includes('<p class="role-readonly">Profilo: Standard</p>'));
 assert(get('ns-editor').innerHTML.includes('Hosting: SSH solo con chiave, firewall e irrigidimento nelle VM nuove. Vale per tutti i nodi del cluster.'));
 for(const role of ['reader','operator']){Auth.role=role;ui.click(profileButton('hosting'));assert.equal(get('ns-profile').value,'standard');assert(!ui.isDirty());}
 Auth.role='admin';ui.click(profileButton('unexpected'));assert(!ui.isDirty());
 ui.click(profileButton('hosting'));assert(ui.isDirty());assert.equal(ui.read().snippet_profile,'hosting');
 assert(ui.guardLeave(()=>{}));assert.equal(confirmation.options.level,'warn');
 ui.click(profileButton('standard'));assert(!ui.isDirty());
 ui.click(profileButton('hosting'));let profileWrites=0;
 global.api=async(path,options)=>{profileWrites++;assert.equal(options.method,'PUT');saved=JSON.parse(options.body);return {status:'ok'};};
 await ui.save();assert.equal(profileWrites,1);assert.equal(saved.snippet_profile,'hosting');assert(!ui.isDirty());
 ui.click(profileButton('standard'));assert(ui.isDirty());global.api=async()=>structuredClone(saved);await ui.cancel();
 assert.equal(get('ns-profile').value,'hosting');assert(!ui.isDirty());
 assert(get('ns-editor').innerHTML.includes('data-profile="hosting" class="on" aria-pressed="true"'));
 assert(get('ns-editor').innerHTML.includes('<p class="role-readonly">Profilo: Hosting</p>'));
 ui.render(empty());
 assert(!ui.row({cidr:'<script>',gateway:'" onfocus="bad'}).includes('<script>'));
 assert(ui.row({gateway:'" onfocus="bad'}).includes('&quot; onfocus=&quot;bad'));
 assert(ui.managerRow('" onfocus="bad').includes('&quot; onfocus=&quot;bad'));
 ui.click(button('data-manager-add'));assert(ui.isDirty(),'empty manager row is dirty');
 let managerWrites=0;global.api=async(path,options)=>{managerWrites++;saved=JSON.parse(options.body);return {status:'ok'};};
 await ui.save();assert.equal(managerWrites,0);assert.equal(get('ns-save-error').textContent,'indirizzo del manager 1: indirizzo IPv4 non valido');
 get('ns-manager-from').children[0].querySelector('input').value='192.0.2.10';get('ns-form').events.input();await ui.save();
 assert.equal(managerWrites,1);assert.deepEqual(saved.manager_from,['192.0.2.10']);assert(!ui.isDirty());
 get('ns-manager-from').children[0].querySelector('input').value='192.0.2.20';assert(ui.isDirty());
 assert(ui.guardLeave(()=>{}));assert.equal(confirmation.options.level,'warn');
 ui.render({...empty(),manager_from:['192.0.2.10']});assert(!ui.isDirty());
 ui.click(button('data-manager-remove',get('ns-manager-from').children[0]));assert(ui.isDirty(),'removing an address is dirty');
 ui.render(empty());for(let i=0;i<4;i++)ui.click(button('data-manager-add'));
 assert(get('ns-manager-add').disabled);ui.click(button('data-manager-add'));assert.equal(get('ns-manager-from').children.length,4);
 for(const child of get('ns-manager-from').children)child.querySelector('input').value='192.0.2.10';
 await ui.save();assert.equal(get('ns-save-error').textContent,'indirizzo del manager 2: indirizzo ripetuto');
 get('ns-manager-from').children[1].querySelector('input').value='2001:db8::1';
 await ui.save();assert.equal(get('ns-save-error').textContent,'indirizzo del manager 2: indirizzo IPv4 non valido');
 ui.render(empty());
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
