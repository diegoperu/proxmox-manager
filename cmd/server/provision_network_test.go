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

func TestProvisionDHCPUI(t *testing.T) {
	source := settingsJS(t, "// pmgr:network:begin", "// pmgr:network:end") +
		settingsJS(t, "// pmgr:vmnet:begin", "// pmgr:vmnet:end") +
		settingsJS(t, "const Provision=", "\n})();")
	runSettingsJS(t, `
const assert=require('node:assert/strict'),nodes=new Map();
function element(){
 const classes=new Set();
 return {value:'',textContent:'',innerHTML:'',hidden:false,checked:false,disabled:false,style:{},children:[],options:[],attrs:{},
  focus(){this.focused=true;},scrollIntoView(){},add(option){this.options.push(option);},append(child){this.children.push(child);},
  replaceChildren(...children){this.children=children;},classList:{toggle(name,on){if(on)classes.add(name);else classes.delete(name);},contains:name=>classes.has(name)},
  setAttribute(key,value){this.attrs[key]=value;},removeAttribute(key){delete this.attrs[key];},querySelector:()=>null};
}
const get=id=>{if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);};
const steps=Array.from({length:4},element);
global.document={getElementById:get,querySelectorAll:s=>s==='#pv-steps .pv-step'?steps:[],createElement:()=>element()};
global.window={matchMedia:()=>({matches:true})};
global.Option=function(text,value){return {text,value};};
global.ClusterMgr={current:0,currentLabel:()=>'<cluster>'};
global.Groups={fetch:async()=>{},parse:()=>({ok:true,groups:[]}),existing:()=>new Set()};
global.toast=()=>{};global.esc=value=>String(value??'').replaceAll('<','&lt;');global.errBox=message=>message;
global._tc={clear(){}};
`+source+`
const flush=()=>new Promise(setImmediate);
const template={vmid:9000,node:'n1',name:'Template',config:{ipconfig0:'ip=10.75.4.20/24,gw=10.75.4.254',nameserver:'9.9.9.9',searchdomain:'template.example'}};
const text=id=>get(id).children.map(child=>child.textContent).join('\n');
Provision.target=()=> 'n1';Provision.advice=()=>{};
async function load(mode,dns=[]){
 Provision.resetNetwork();get('tpl-select').value='';
 global.api=async path=>path.includes('network-settings')?{ip_mode:mode,networks:[{cidr:'10.75.4.0/24',gateway:'10.75.4.1'}],dns,search_domain:''}:[structuredClone(template)];
 await Provision.load();await flush();get('tpl-select').value='0';Provision.onSelectTemplate();
 get('pv-name').value='new-vm';get('pv-cpu').value='2';get('pv-ram').value='4';get('pv-start').checked=true;
 Provision.summary();
}
let payload;
async function submit(view={}){
 global.api=async(path,options)=>{
  if(path==='/provision/job'){
   payload=JSON.parse(options.body);return {job:'job',vmid:200};
  }
  assert.equal(path,'/provision/job?id=job');
  return {vmid:200,name:'new-vm',node:'n1',phase:'done',result:'ok',vm_created:true,steps:[],warnings:[],...view};
 };
 await Provision.run();await flush();await flush();return payload;
}
(async()=>{
 // Old clusters retain static fields, template defaults and the same payload.
 await load('');assert(get('pv-address-choice').hidden);assert(!get('pv-static-fields').hidden);assert(get('pv-dhcp-note').hidden);
 assert.equal(get('pv-ip').value,'10.75.4.20/24');assert.equal(get('pv-dns').value,'9.9.9.9');
 let p=await submit({ip:'10.75.4.20'});assert.equal(p.ip,'10.75.4.20/24');assert.equal(p.gateway,'10.75.4.1');assert(!Object.hasOwn(p,'dhcp'));
 assert(text('pv-log').includes('IP: 10.75.4.20'));assert(!text('pv-log').includes('(dal DHCP)'));

 // DHCP hides static fields and suppresses advice for stale static values. Configured DNS still applies.
 await load('dhcp',['1.1.1.1']);assert(get('pv-address-choice').hidden);assert(get('pv-static-fields').hidden);assert(!get('pv-dhcp-note').hidden);
 assert.equal(get('pv-ip').value,'','static template IP must not be copied into DHCP');assert.equal(get('pv-dns').value,'1.1.1.1');
 get('pv-ip').value='198.51.100.20/24';get('pv-gw').value='198.51.100.1';Provision.calcGw();
 assert(get('pv-net-warning').hidden);assert(get('pv-net-hint').hidden);assert(text('pv-summary').includes('IP: DHCP'));assert(!text('pv-summary').includes('198.51.100'));
 assert(steps[2].classList.contains('done'),'DHCP completes network step without a static IP');
 Provision.goto(3);assert(get('pv-dns').focused);
 p=await submit({ip:'192.0.2.42'});assert.equal(p.dhcp,true);assert(!Object.hasOwn(p,'ip'));assert(!Object.hasOwn(p,'gateway'));assert.equal(p.dns,'1.1.1.1');
 assert(text('pv-log').includes('IP: 192.0.2.42 (dal DHCP)'));

 // Template DNS is omitted for DHCP; an explicit value or an explicit clearing is respected.
 await load('dhcp');assert.equal(get('pv-dns').value,'');
 get('pv-dns').value='8.8.8.8';Provision.onDns();p=await submit();assert.equal(p.dns,'8.8.8.8');
 get('pv-dns').value='';Provision.onDns();p=await submit({result:'warnings',warnings:['dhcp_ip_unknown']});assert.equal(p.dns,'');
 assert(text('pv-log').includes('IP non letto dal guest agent: controllalo nella pagina della VM'));

 // Both starts static. DHCP can be selected explicitly, then static values return unchanged.
 await load('both');assert(!get('pv-address-choice').hidden);assert(!get('pv-static-fields').hidden);assert(get('pv-address-static').classList.contains('on'));
 get('pv-ip').value='192.0.2.9/24';Provision.calcGw();get('pv-gw').value='192.0.2.1';Provision.onGw(get('pv-gw'));
 assert(!get('pv-net-warning').hidden);
 Provision.setAddressMode('unexpected');assert(!get('pv-static-fields').hidden);
 Provision.setAddressMode('dhcp');assert(get('pv-static-fields').hidden);assert(get('pv-net-warning').hidden);assert.equal(get('pv-address-dhcp').attrs['aria-pressed'],true);
 p=await submit({ip:'192.0.2.42'});assert.equal(p.dhcp,true);assert(!Object.hasOwn(p,'ip'));assert(!Object.hasOwn(p,'gateway'));assert.equal(p.dns,'');
 Provision.setAddressMode('static');assert(!get('pv-static-fields').hidden);assert.equal(get('pv-ip').value,'192.0.2.9/24');assert.equal(get('pv-gw').value,'192.0.2.1');
 assert(!get('pv-net-warning').hidden);assert.equal(get('pv-dns').value,'9.9.9.9');
 p=await submit();assert.equal(p.ip,'192.0.2.9/24');assert.equal(p.gateway,'192.0.2.1');assert(!Object.hasOwn(p,'dhcp'));
 get('pv-ip').value='';Provision.calcGw();assert(text('pv-summary').includes('IP: DHCP'));
 p=await submit();assert.equal(p.dhcp,true);assert(!Object.hasOwn(p,'ip'));assert(!Object.hasOwn(p,'gateway'));

 // Cluster changes reset the choice, and a late DHCP settings response overrides template defaults.
 Provision.setAddressMode('dhcp');await load('both');assert(!get('pv-static-fields').hidden);assert(get('pv-address-static').classList.contains('on'));
 Provision.resetNetwork();get('tpl-select').value='';let resolve;
 global.api=async path=>path.includes('network-settings')?new Promise(done=>resolve=done):[structuredClone(template)];
 await Provision.load();get('tpl-select').value='0';Provision.onSelectTemplate();assert.equal(get('pv-dns').value,'9.9.9.9');
 resolve({ip_mode:'dhcp',networks:[],dns:[],search_domain:''});await flush();
 assert(get('pv-static-fields').hidden);assert.equal(get('pv-dns').value,'');assert(text('pv-summary').includes('IP: DHCP'));
 p=await submit({ip:'<img src=x onerror=alert(1)>'});assert.equal(p.dhcp,true);
 const ipLine=get('pv-log').children.find(child=>child.textContent.startsWith('IP: '));
 assert.equal(ipLine.textContent,'IP: <img src=x onerror=alert(1)> (dal DHCP)');assert.equal(ipLine.innerHTML,'','external IP uses textContent');
})().catch(err=>{console.error(err);process.exitCode=1;});
`)
}
