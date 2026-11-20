package main

import "testing"

func TestProvisionNetworkDefaults(t *testing.T) {
	source := settingsJS(t, "// pmgr:network:begin", "// pmgr:network:end") +
		settingsJS(t, "// pmgr:vmnet:begin", "// pmgr:vmnet:end") +
		settingsJS(t, "const Provision=", "\n})();")
	runSettingsJS(t, `
const assert=require('node:assert/strict'),nodes=new Map();
function element(){return {value:'',textContent:'',innerHTML:'',hidden:false,checked:false,disabled:false,style:{},children:[],options:[],focus(){this.focused=true;},add(option){this.options.push(option);},append(child){this.children.push(child);},replaceChildren(...children){this.children=children;},classList:{toggle(){}},setAttribute(){},removeAttribute(){}};}
const get=id=>{if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);};
global.document={getElementById:get,querySelectorAll:()=>[],createElement:()=>element()};
global.Option=function(text,value){return {text,value};};
global.ClusterMgr={current:0,currentLabel:()=>'<Cluster & "test">'};
global.Groups={fetch:async()=>{},parse:()=>({ok:true,groups:[]}),existing:()=>new Set()};
let toasts=[];global.toast=message=>toasts.push(message);global.esc=value=>String(value??'').replaceAll('<','&lt;');global.errBox=message=>message;
global._tc={clear(){}};
`+source+`
const settings={networks:[{cidr:'10.75.4.0/24',gateway:'10.75.4.1'}],dns:['1.1.1.1','::1'],search_domain:'cluster.example'};
const empty={networks:[],dns:[],search_domain:''};
const template={vmid:900,node:'n1',name:'Template',config:{ciuser:'ubuntu',nameserver:'9.9.9.9',searchdomain:'template.example',ipconfig0:'ip=10.75.4.20/24,gw=10.75.4.250'}};
const flush=()=>new Promise(setImmediate);
const select=()=>{get('tpl-select').value='0';Provision.onSelectTemplate();};
Provision.target=()=> 'n1';
let payload;
(async()=>{
 global.api=async(path,options)=>{
  if(path==='/clusters/0/network-settings'){assert(options.quiet);return structuredClone(settings);}
  if(path==='/templates?cluster=0')return [structuredClone(template)];
  throw new Error('unexpected API '+path);
 };
 await Provision.load();await flush();
 assert.equal(get('pv-dns').value,'1.1.1.1 ::1');assert.equal(get('pv-search').value,'cluster.example');
 assert.equal(get('pv-gw').value,'');select();
 assert.equal(get('pv-user').value,'ubuntu');assert.equal(get('pv-ip').value,'10.75.4.20/24');assert.equal(get('pv-gw').value,'10.75.4.1');assert.equal(get('pv-dns').value,'1.1.1.1 ::1');assert.equal(get('pv-search').value,'cluster.example');
 const summary=get('pv-summary').children.map(child=>child.textContent).join('\n');
 assert(summary.includes('gateway: 10.75.4.1 · DNS: 1.1.1.1 ::1 · dominio: cluster.example'));
 get('pv-gw').value='192.0.2.99';Provision.onGw(get('pv-gw'));
 get('pv-dns').value='';Provision.onDns();get('pv-search').value='MANUAL.Example';Provision.onSearch();
 get('pv-ip').value='192.0.2.20/24';Provision.calcGw();select();
 assert.equal(get('pv-ip').value,'192.0.2.20/24');assert.equal(get('pv-gw').value,'192.0.2.99');assert.equal(get('pv-dns').value,'');assert.equal(get('pv-search').value,'MANUAL.Example');
 const warning='IP fuori dalle reti note di <Cluster & "test">: controlla IP e gateway';
 assert.equal(get('pv-net-warning').textContent,warning);assert(!get('pv-net-warning').hidden);assert(get('pv-summary').children.some(child=>child.textContent===warning));
 get('pv-gw').value='';Provision.onGw(get('pv-gw'));assert.equal(get('pv-gw').value,'','cleared gateway stays empty outside known networks');
 get('pv-ip').value='10.75.4.30/24';Provision.calcGw();assert.equal(get('pv-gw').value,'10.75.4.1');assert(get('pv-net-warning').hidden);

 // Domain uses the same validator as settings, and the normalized value reaches POST /provision/job.
 get('pv-name').value='vm-new';get('pv-search').value='bad_domain';Provision.run();assert(get('pv-search').focused);assert(toasts.at(-1).includes('dominio di ricerca non valido'));
 get('pv-search').value='MANUAL.Example';
 global.api=async(path,options)=>{assert.equal(path,'/provision/job');payload=JSON.parse(options.body);return {job:'test',vmid:101};};
 Provision.pollJob=()=>{};Provision.run();await flush();assert.equal(payload.search_domain,'manual.example');

 // A cluster change resets every manual network flag and removes values from the old cluster.
 ClusterMgr.current=1;Provision.resetNetwork();get('tpl-select').value='';
 global.api=async path=>path==='/clusters/1/network-settings'?structuredClone(empty):[structuredClone(template)];
 await Provision.load();await flush();assert.equal(get('pv-dns').value,'');assert.equal(get('pv-search').value,'');assert.equal(get('pv-ip').value,'');
 get('pv-ip').value='192.0.2.1/30';Provision.calcGw();assert.equal(get('pv-gw').value,'192.0.2.2');assert.equal(get('pv-net-hint').textContent,'gateway indovinato: configura le reti in Impostazioni → Infrastruttura');
 select();assert.equal(get('pv-ip').value,'192.0.2.1/30');assert.equal(get('pv-gw').value,'10.75.4.250');assert.equal(get('pv-dns').value,'9.9.9.9');assert.equal(get('pv-search').value,'template.example');

 // Late cluster defaults override template defaults, preserving edits made while GET was pending.
 Provision.resetNetwork();get('tpl-select').value='';let resolve;
 global.api=async path=>path.includes('network-settings')?new Promise(done=>resolve=done):[structuredClone(template)];
 await Provision.load();select();get('pv-dns').value='8.8.8.8';Provision.onDns();resolve(structuredClone(settings));await flush();
 assert.equal(get('pv-gw').value,'10.75.4.1');assert.equal(get('pv-dns').value,'8.8.8.8');assert.equal(get('pv-search').value,'cluster.example');

 // Quiet read failures show no warning or hint and retain the legacy gateway fallback.
 Provision.resetNetwork();get('tpl-select').value='';toasts=[];
 global.api=async path=>{if(path.includes('network-settings'))throw new Error('offline');return [structuredClone(template)];};
 await Provision.load();await flush();get('pv-ip').value='192.0.2.1/30';Provision.calcGw();assert.equal(get('pv-gw').value,'192.0.2.2');assert.equal(get('pv-dns').value,'');assert(get('pv-net-warning').hidden);assert(get('pv-net-hint').hidden);assert.equal(toasts.length,0);

 // Resolve network reads from two clusters in reverse order.
 Provision.resetNetwork();get('tpl-select').value='';const pending=[];
 global.api=async path=>path.includes('network-settings')?new Promise(done=>pending.push(done)):[structuredClone(template)];
 ClusterMgr.current=0;await Provision.load();ClusterMgr.current=1;await Provision.load();
 pending[1]({...empty,dns:['4.4.4.4'],search_domain:'new.example'});await flush();pending[0](structuredClone(settings));await flush();
 assert.equal(get('pv-dns').value,'4.4.4.4');assert.equal(get('pv-search').value,'new.example');

 // Stale template reads cannot replace the selector for the current cluster either.
 Provision.resetNetwork();const reads=[];global.api=async path=>path.includes('network-settings')?structuredClone(empty):new Promise(done=>reads.push(done));
 ClusterMgr.current=0;const first=Provision.load();ClusterMgr.current=1;const second=Provision.load();
 reads[1]([{...template,name:'New cluster'}]);await second;reads[0]([{...template,name:'Old cluster'}]);await first;
 select();assert(get('tpl-info').innerHTML.includes('New cluster'));assert(!get('tpl-info').innerHTML.includes('Old cluster'));
})().catch(err=>{console.error(err);process.exitCode=1;});
`)
}
