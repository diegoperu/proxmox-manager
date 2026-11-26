package main

import "testing"

func TestSyncAccessHelpers(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "/* pmgr:syncaccess:begin */", "/* pmgr:syncaccess:end */")+`
const assert=require('node:assert/strict');
const {stateWord,shortFingerprint,summaryText}=SyncAccessHelpers;
for(const [state,word,level] of [
 ['ok','accesso attivo','ok'],['not_pinned','impronta da confermare','warn'],
 ['auth_refused','chiave del manager non ancora autorizzata','warn'],['gate_denied','comando ristretto rifiuta','fail'],
 ['host_key_changed','CHIAVE DEL NODO CAMBIATA','fail'],['unreachable','non raggiungibile','fail'],
 ['offline','nodo spento','warn'],['no_address','indirizzo sconosciuto','warn'],['error','errore','fail']
])assert.deepEqual(stateWord(state),{word,level});
for(const unknown of ['constructor','__proto__','<script>',null])assert.deepEqual(stateWord(unknown),{word:'errore',level:'fail'});
const fp='SHA256:'+'a'.repeat(43);assert.equal(shortFingerprint(fp),'SHA256:aaaaaaaaaaaa…aaaa');
assert.equal(shortFingerprint('short'),'short');assert.equal(shortFingerprint(null),'—');assert.equal(shortFingerprint(''),'—');
assert.equal(summaryText({summary:{ok:3,todo:2,problem:1}}),'3 accessi attivi · 2 da preparare · 1 con problemi');
assert.equal(summaryText(null),'0 accessi attivi · 0 da preparare · 0 con problemi');
assert.equal(summaryText({summary:{ok:-1,todo:'2',problem:NaN}}),'0 accessi attivi · 0 da preparare · 0 con problemi');
`)
}

func syncAccessUISource(t *testing.T) string {
	t.Helper()
	return authUISource(t, "// pmgr:auth-ui:begin", "// pmgr:auth-ui:end") +
		settingsJS(t, "const NodePrep={", "\n};") +
		settingsJS(t, "/* pmgr:syncaccess:begin */", "/* pmgr:syncaccess:end */") +
		settingsJS(t, "const SyncAccess={", "\n};")
}

const syncAccessHarness = `
const assert=require('node:assert/strict'),controls=new Map();
global.planReloads=0;global.SyncPlan={load(){planReloads++;return Promise.resolve();}};
const esc=v=>String(v??'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
function element(tag='div'){
 return {tag,children:[],attrs:{},events:{},textContent:'',innerHTML:'',disabled:false,
  append(...children){this.children.push(...children);},replaceChildren(){this.children=[];this.innerHTML='';this.textContent='';},
  setAttribute(k,v){this.attrs[k]=v;},addEventListener(k,fn){this.events[k]=fn;},focus(){this.focused=true;}};
}
const get=id=>{if(!controls.has(id))controls.set(id,element());return controls.get(id);};
const walk=root=>[root,...root.children.flatMap(walk)];
global.document={getElementById:get,createElement:element};
global.ClusterMgr={current:0};global.App={cur:'nodeprep'};global.Auth={locked:false,epoch:1,role:'admin'};
global.errBox=(message,retry)=>{assert.equal(retry,'SyncAccess.load(true)');return esc(message);};
const fp='SHA256:'+'a'.repeat(43),changedFP='SHA256:'+'b'.repeat(43);
const node={node:'node-1',address:'192.0.2.11',online:true,pinned:false,fingerprint:'',state:'not_pinned',message:'Da confermare',files:0,same_as_others:null};
const report={cluster:'Mock',generated_at:1700000000,manager_key:'ssh-ed25519 public-example',manager_fingerprint:fp,
 manager_from:['192.0.2.10'],verify_command:'ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub',
 setup:{ready:true,reason:'',commands:['command-one','command-two'],note:'Solo il manager potrà scrivere.'},
 nodes:[node],summary:{ok:0,todo:1,problem:0}};
const scan={node:node.node,address:node.address,fingerprint:fp,pinned_fingerprint:'',verify_command:report.verify_command};
let confirmation;global.askConfirm=(options,ok)=>confirmation={options,ok};
`

func TestSyncAccessValidationAndRequests(t *testing.T) {
	runSettingsJS(t, syncAccessUISource(t)+syncAccessHarness+`
const ui=SyncAccess,painted=[];ui.paint=function(){painted.push(this.d);};
for(const bad of [null,{}, {...report,manager_fingerprint:'invalid'}, {...report,manager_from:'bad'},
 {...report,summary:{ok:-1,todo:0,problem:0}}, {...report,setup:{...report.setup,commands:[{}]}},
 {...report,nodes:[{...node,node:'../bad'}]}, {...report,nodes:[{...node,state:'<script>'}]},
 {...report,nodes:[{...node,same_as_others:'false'}]}, {...report,nodes:[{...node,files:-1}]},
 {...report,nodes:[{...node,fingerprint:'bad'}]}, {...report,nodes:[node,node]},
 {...report,nodes:Array(257).fill(node)}])assert.throws(()=>ui.validate(bad),/non valida/);
assert.equal(ui.validate(report),report);
for(const bad of [{...scan,node:'other'},{...scan,address:'192.0.2.12'},{...scan,fingerprint:'bad'},
 {...scan,pinned_fingerprint:'bad'},{...scan,verify_command:[]},{...scan,verify_command:''}])
 assert.throws(()=>ui.validateScan(bad,node.node,node.address),/non valida/);
assert.equal(ui.validateScan(scan,node.node,node.address),scan);
const pending=[];global.api=(path,options)=>new Promise((resolve,reject)=>{assert.equal(options.quiet,true);assert.equal(options.timeoutMs,120000);pending.push({path,resolve,reject});});
(async()=>{
 let first=ui.load(),second=ui.load(true);
 assert.equal(pending[0].path,'/clusters/0/sync-access');assert.equal(pending[1].path,'/clusters/0/sync-access?fresh=1');
 pending[0].resolve(report);await first;assert.equal(painted.length,0);assert.equal(get('np-sync').attrs['aria-busy'],'true');
 pending[1].resolve(report);await second;assert.equal(painted.length,1);assert.equal(get('np-sync').attrs['aria-busy'],'false');
 first=ui.load();ClusterMgr.current=1;second=ui.load();pending[3].resolve({...report,cluster:'Second'});await second;
 pending[2].reject(new Error('old failure'));await first;assert.equal(ui.d.cluster,'Second');assert.equal(painted.length,2);
 first=ui.load();ui.leave();App.cur='nodes';pending[4].resolve(report);await first;assert.equal(painted.length,2);
 App.cur='nodeprep';first=ui.load();pending[5].reject({status:429,message:'busy'});await first;
 assert(get('np-sync').innerHTML.includes(esc("Controllo gia' in corso")));assert.equal(get('np-sync').attrs['aria-busy'],'false');
 first=ui.load();pending[6].reject(new Error('<server unavailable>'));await first;assert.equal(get('np-sync').innerHTML,'&lt;server unavailable&gt;');
 first=ui.load();Auth.epoch++;pending[7].resolve(report);await first;assert.equal(painted.length,2);
 first=ui.load();Auth.locked=true;pending[8].resolve(report);await first;assert.equal(painted.length,2);Auth.locked=false;
 ui.scans.set(node.node,scan);first=ui.load(true,true);pending[9].resolve({...report,nodes:[{...node,address:'192.0.2.99'}]});await first;
 assert.equal(ui.scans.size,0,'a scan cannot follow a changed node address');
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestSyncAccessActions(t *testing.T) {
	runSettingsJS(t, syncAccessUISource(t)+syncAccessHarness+`
const ui=SyncAccess,calls=[],replies=[];
global.api=async(path,options)=>{calls.push({path,...options});assert(replies.length,'unexpected request '+path);const reply=replies.shift();if(reply instanceof Error)throw reply;return reply;};
const reset=()=>{ui.idx=ClusterMgr.current;ui.d=structuredClone(report);ui.busy=false;};
(async()=>{
 reset();for(const role of ['reader','operator']){Auth.role=role;await ui.scan(node.node);ui.pin(node.node);ui.forget(node.node);}
 assert.equal(calls.length,0);Auth.role='admin';
 for(const name of ['../bad','unknown'])await ui.scan(name);assert.equal(calls.length,0);
 replies.push(scan,report);await ui.scan(node.node);
 assert.equal(calls[0].path,'/clusters/0/sync-access/scan');assert.equal(calls[0].method,'POST');assert.deepEqual(JSON.parse(calls[0].body),{node:node.node});
 assert.equal(calls[1].path,'/clusters/0/sync-access?fresh=1');assert.equal(ui.scans.get(node.node),scan);
 let before=calls.length;ui.pin(node.node);assert.equal(calls.length,before);assert.equal(confirmation.options.title,"Confermare l'impronta di node-1?");
 assert.equal(confirmation.options.level,'warn');assert.equal(confirmation.options.minRole,'admin');assert(confirmation.options.lines.includes(fp));
 const conflict=new Error('il nodo presenta ora una chiave diversa da quella confermata');conflict.status=409;
 replies.push(conflict,report);await confirmation.ok();assert.equal(ui.messages.get(node.node),conflict.message);assert(!ui.scans.has(node.node));
 assert.equal(calls[before].path,'/clusters/0/sync-access/pin');assert.deepEqual(JSON.parse(calls[before].body),{node:node.node,fingerprint:fp});
 assert.equal(calls[before+1].path,'/clusters/0/sync-access?fresh=1');
 replies.push(scan,report);await ui.scan(node.node);ui.pin(node.node);
 const pinned={...report,nodes:[{...node,pinned:true,fingerprint:fp,state:'ok'}]};replies.push({ok:true},pinned);await confirmation.ok();
 assert(!ui.scans.has(node.node));assert(!ui.messages.has(node.node));assert(ui.target(node.node).pinned);
 assert(planReloads>=1,'the snippet plan is read again after a confirmed key');
 before=calls.length;ui.forget(node.node);assert.equal(calls.length,before);assert.equal(confirmation.options.level,'danger');assert.equal(confirmation.options.minRole,'admin');
 replies.push({ok:true},report);await confirmation.ok();assert.equal(calls[before].path,'/clusters/0/sync-access/pin/node-1');assert.equal(calls[before].method,'DELETE');
 assert.equal(calls[before+1].path,'/clusters/0/sync-access?fresh=1');
 assert(planReloads>=2,'and after a forgotten key');
 // Confirmation callbacks cannot write after a cluster, page, session or role change, or a refresh.
 for(const change of [()=>ClusterMgr.current++,()=>ui.leave(),()=>Auth.epoch++,()=>Auth.role='reader',()=>ui.seq++]){
  reset();ui.scans.set(node.node,scan);ui.pin(node.node);before=calls.length;change();await confirmation.ok();assert.equal(calls.length,before);Auth.role='admin';
 }
 reset();ui.d.nodes[0].address='';before=calls.length;await ui.scan(node.node);assert.equal(calls.length,before);
 reset();replies.push(new Error('Nodo non raggiungibile via SSH dal manager (porta 22).'),report);await ui.scan(node.node);
 assert(ui.messages.get(node.node).includes('non raggiungibile'));assert.equal(replies.length,0);
 reset();replies.push(scan,new Error('stato non disponibile'));await ui.scan(node.node);
 assert.equal(ui.scans.get(node.node),scan,'the received fingerprint survives a failed status reload');
 assert.equal(ui.d.nodes[0].node,node.node);assert(walk(get('np-sync')).some(n=>n.innerHTML.includes('Stato non aggiornato')));
 ui.pin(node.node);replies.push(conflict,new Error('stato non disponibile'));await confirmation.ok();
 assert.equal(ui.messages.get(node.node),conflict.message,'the conflict survives a failed status reload');
 assert(walk(get('np-sync')).some(n=>n.textContent===conflict.message));
 // A late scan cannot add a confirmation or reload a different cluster.
 reset();let finish;global.api=()=>new Promise(resolve=>finish=resolve);const pending=ui.scan(node.node);ui.leave();ClusterMgr.current++;finish(scan);await pending;assert.equal(ui.scans.size,0);
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestSyncAccessSafeRendering(t *testing.T) {
	runSettingsJS(t, syncAccessUISource(t)+syncAccessHarness+`
const ui=SyncAccess,unsafe='<img src=x onerror="alert(1)"> & \'quoted\'';
ui.idx=0;ui.d={...report,manager_key:unsafe,setup:{...report.setup,commands:[unsafe],note:unsafe}};
ui.paint();const all=walk(get('np-sync'));
assert(all.some(n=>n.tag==='details'&&!n.open),'public key is collapsed');
assert.equal(all.filter(n=>n.tag==='code'&&n.textContent===unsafe).length,2);
assert(all.some(n=>n.tag==='p'&&n.textContent===unsafe));assert(!all.some(n=>n.innerHTML.includes('<img')));
const copied=[];NodePrep.copy=(value,button)=>{copied.push(value);};
all.find(n=>n.tag==='button'&&n.textContent==='Copia').events.click();assert.equal(copied[0],unsafe);
ui.d={...report,manager_from:[],setup:{...report.setup,ready:false,reason:unsafe,commands:[]}};ui.paint();
assert(walk(get('np-sync')).some(n=>n.tag==='a'&&n.href==='#/settings/infra'));
assert(walk(get('np-sync')).some(n=>n.textContent===unsafe));
for(const [same,words] of [[true,'uguale agli altri'],[false,'diverso dagli altri'],[null,null]]){
 const row=ui.nodeRow({...node,message:unsafe,pinned:true,fingerprint:fp,files:7,same_as_others:same,state:'host_key_changed'});
 assert(row.innerHTML.includes('CHIAVE DEL NODO CAMBIATA'));assert(row.innerHTML.includes('✕'));assert(!row.innerHTML.includes('7 file'));assert(ui.nodeRow({...node,state:'ok',pinned:true,fingerprint:fp,files:7,same_as_others:true}).innerHTML.includes('7 file'));
 assert(row.innerHTML.includes('title="'+fp+'"'));assert(row.innerHTML.includes(SyncAccessHelpers.shortFingerprint(fp)));
 const okRow=ui.nodeRow({...node,state:'ok',pinned:true,fingerprint:fp,files:7,same_as_others:same});assert.equal(okRow.innerHTML.includes('agli altri'),same!==null);if(words)assert(okRow.innerHTML.includes(words));assert(!row.innerHTML.includes('agli altri'));
 const message=row.children.find(n=>n.textContent===unsafe);assert.equal(message.className,'sa-danger');
 const buttons=walk(row).filter(n=>n.tag==='button');assert.deepEqual(buttons.map(n=>n.textContent),['Leggi impronta','Dimentica impronta']);
 assert(buttons.every(n=>n.attrs['data-min-role']==='admin'));
}
ui.scans.set(node.node,{...scan,fingerprint:changedFP,pinned_fingerprint:fp,verify_command:unsafe});
const row=ui.nodeRow(node),elements=walk(row);
assert(elements.some(n=>n.className==='sa-danger'&&n.textContent.includes('diversa da quella confermata in precedenza')&&n.textContent.includes(fp)));
assert(elements.some(n=>n.tag==='code'&&n.textContent===unsafe));
assert(elements.some(n=>n.textContent==='Confronta questa impronta con quella stampata dal comando sul nodo: confermala solo se sono identiche.'));
assert(elements.some(n=>n.tag==='button'&&n.textContent==='Conferma impronta'&&n.attrs['data-min-role']==='admin'));
assert(!elements.some(n=>n.innerHTML.includes('<img')));
ui.busy=true;assert(walk(ui.nodeRow(node)).filter(n=>n.tag==='button'&&n.attrs['data-min-role']).every(n=>n.disabled));
`)
}

func TestSyncAccessUsesCSRFAndStepUp(t *testing.T) {
	source := syncAccessUISource(t) + syncAccessHarness +
		settingsJS(t, "async function api(", "// Librerie incorporate")
	runSettingsJS(t, source+`
Object.assign(Auth,{csrf:'test-csrf',requests:new Set(),credentialsPending:false,promptStep:async()=>{prompts++;return '123456';}});
let prompts=0;const calls=[],replies=[];
global.fetch=async(url,options)=>{calls.push({url,...options,headers:new Headers(options.headers)});assert(replies.length);const [status,body]=replies.shift();return {status,ok:status<300,headers:new Headers(),json:async()=>body};};
const ui=SyncAccess;
(async()=>{
 for(const [method,path,body] of [['POST','/pin',{node:node.node,fingerprint:fp}],['DELETE','/pin/node-1',undefined]]){
  ui.idx=2;ClusterMgr.current=2;ui.d={...report,nodes:[{...node,pinned:true,fingerprint:fp}]};ui.scans.set(node.node,scan);
  replies.push([403,{reason:'step_up'}],[200,{ok:true}],[200,report]);
  method==='POST'?ui.pin(node.node):ui.forget(node.node);await confirmation.ok();
  const [first,retry,refresh]=calls.slice(-3);assert.equal(first.url,'/api/clusters/2/sync-access'+path);assert.equal(retry.url,first.url);
  assert.equal(first.method,method);assert.equal(retry.method,method);assert.equal(retry.body,first.body);
  if(body)assert.deepEqual(JSON.parse(first.body),body);
  assert.equal(retry.headers.get('X-CSRF-Token'),'test-csrf');assert.equal(retry.headers.get('X-TOTP-Code'),'123456');
  assert.equal(refresh.url,'/api/clusters/2/sync-access?fresh=1');
 }
 assert.equal(prompts,2);assert.equal(replies.length,0);
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestSyncAccessAndReadinessLoadIndependently(t *testing.T) {
	source := settingsJS(t, "/* pmgr:nodeprep:begin */", "/* pmgr:nodeprep:end */") + syncAccessUISource(t)
	runSettingsJS(t, source+syncAccessHarness+`
const planLoads=[];global.SyncPlan={load:()=>new Promise(resolve=>planLoads.push(resolve)),leave(){}};
global.NodeCleanup={load:async()=>{},leave(){}};
const pending=[];global.api=(path,options)=>new Promise((resolve,reject)=>pending.push({path,resolve,reject}));
global.errBox=(message,retry)=>{assert(['NodePrep.load(true)','SyncAccess.load(true)'].includes(retry));return esc(message);};
NodePrep.paint=function(){get('np-cluster').textContent='Controlli pronti';};
const ready={cluster:'Mock',generated_at:1700000000,summary:{ok:1,warn:0,fail:0,unknown:0},cluster_checks:[],nodes:[],partial:[]};
const flush=()=>new Promise(setImmediate);
(async()=>{
 const first=NodePrep.load();assert.deepEqual(pending.map(p=>p.path),['/clusters/0/sync-access','/clusters/0/node-readiness']);
 pending[0].resolve(report);await flush();assert.equal(SyncAccess.d,report);assert(get('np-sync').children.length);
 const content=get('np-sync').children;pending[1].reject(new Error('readiness unavailable'));await flush();
 assert(get('np-recheck').disabled,'recheck also waits for the sync plan');planLoads[0]();await first;
 assert.equal(get('np-cluster').innerHTML,'readiness unavailable');assert.equal(get('np-sync').children,content);
 assert(!get('np-recheck').disabled);assert(get('np-only').disabled);
 const second=NodePrep.load(true);assert.deepEqual(pending.slice(2).map(p=>p.path),['/clusters/0/sync-access?fresh=1','/clusters/0/node-readiness?fresh=1']);
 pending[3].resolve(ready);await flush();assert.equal(get('np-cluster').textContent,'Controlli pronti');
 assert(get('np-recheck').disabled,'recheck waits for both reports');
 pending[2].reject({status:429,message:'busy'});planLoads[1]();await second;
 assert(get('np-sync').innerHTML.includes(esc("Controllo gia' in corso")));assert.equal(get('np-cluster').textContent,'Controlli pronti');
 assert(!get('np-recheck').disabled);assert(!get('np-only').disabled);
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}
