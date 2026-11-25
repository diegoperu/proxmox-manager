package main

import "testing"

func syncPlanUISource(t *testing.T) string {
	t.Helper()
	return syncAccessUISource(t) +
		settingsJS(t, "/* pmgr:syncplan:begin */", "/* pmgr:syncplan:end */") +
		settingsJS(t, "const SyncPlan={", "\n};")
}

const syncPlanHarness = syncAccessHarness + `
document.createElement=tag=>{
 const el=element(tag);Object.defineProperty(el,'id',{get(){return this.elementID;},set(value){this.elementID=value;controls.set(value,this);}});return el;
};
const pendingNode={node:'node-1',address:'192.0.2.11',state:'ok',message:'Da copiare',ready:true,aligned:false,same:4,
 missing:['snippets/a.yaml'],different:['scripts/b.sh'],extra:['scripts/local.sh'],missing_count:1,different_count:1,extra_count:1};
const alignedNode={...pendingNode,node:'node-2',aligned:true,missing:[],different:[],missing_count:0,different_count:0};
const blockedNode={...pendingNode,node:'node-3',state:'not_pinned',message:'Impronta da confermare',ready:false,aligned:false,
 missing:[],different:[],extra:[],missing_count:0,different_count:0,extra_count:0};
const plan={cluster:'Mock',profile:'standard',bundle_digest:'b'.repeat(24),digest:'a'.repeat(24),snippets:4,scripts:3,
 nodes:[pendingNode,alignedNode,blockedNode],summary:{aligned:1,to_sync:1,not_ready:1}};
const done={ok:true,results:[{node:pendingNode.node,ok:true,copied:2,message:'Copiati e verificati.'}]};
const accessRefreshes=[];SyncAccess.load=async fresh=>accessRefreshes.push(fresh);
global.errBox=(message,retry)=>{assert(['SyncAccess.load(true)','SyncPlan.load()','NodePrep.load(true)'].includes(retry));return esc(message);};
const ui=SyncPlan;
const reset=()=>{ui.leave();ui.idx=ClusterMgr.current;ui.epoch=Auth.epoch;ui.d=structuredClone(plan);ui.selected=new Set(SyncPlanHelpers.defaultSelection(ui.d));};
const flush=()=>new Promise(setImmediate);
`

func TestSyncPlanHelpers(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "/* pmgr:syncplan:begin */", "/* pmgr:syncplan:end */")+`
const assert=require('node:assert/strict');
const {nodeLevel,defaultSelection,confirmLines}=SyncPlanHelpers;
assert.deepEqual(nodeLevel({ready:true,aligned:true}),{word:'allineato',level:'ok'});
assert.deepEqual(nodeLevel({ready:true,aligned:false}),{word:'da sincronizzare',level:'warn'});
assert.deepEqual(nodeLevel({ready:false,aligned:true,message:'<host error>'}),{word:'<host error>',level:'unknown'});
assert.deepEqual(nodeLevel(null),{word:'non pronto',level:'unknown'});
const plan={nodes:[{node:'a',ready:true,aligned:false,missing_count:2,different_count:3,extra_count:4},
 {node:'b',ready:false,aligned:false},{node:'c',ready:true,aligned:true}]};
const before=JSON.stringify(plan);
assert.deepEqual(defaultSelection(plan),['a']);assert.deepEqual(defaultSelection(null),[]);
assert.deepEqual(confirmLines(plan,['a','b','c','unknown']),['a: 2 mancanti · 3 diversi · 4 extra (non vengono cancellati)']);
assert.deepEqual(confirmLines(plan,new Set(['a'])),confirmLines(plan,['a']));
assert.deepEqual(confirmLines(null,[]),[]);assert.equal(JSON.stringify(plan),before);
`)
}

func TestSyncPlanValidationAndRequests(t *testing.T) {
	runSettingsJS(t, syncPlanUISource(t)+syncPlanHarness+`
for(const bad of [null,{}, {...plan,profile:'<script>'},{...plan,digest:'bad'},{...plan,bundle_digest:'bad'},
 {...plan,snippets:-1},{...plan,summary:{...plan.summary,to_sync:2}},
 {...plan,nodes:[{...pendingNode,node:'../bad'}]}, {...plan,nodes:[pendingNode,pendingNode]},
 {...plan,nodes:Array(257).fill(pendingNode)}, {...plan,nodes:[{...pendingNode,ready:'yes'}]},
 {...plan,nodes:[{...pendingNode,state:'constructor'}]}, {...plan,nodes:[{...pendingNode,message:{}}]},
 {...plan,nodes:[{...pendingNode,extra_count:NaN}]}, {...plan,nodes:[{...pendingNode,missing:['a','b']}]},
 {...plan,nodes:[{...pendingNode,different:[{}]}]}, {...plan,nodes:[{...pendingNode,extra:Array(101).fill('x'),extra_count:101}]}])
 assert.throws(()=>ui.validate(bad),/non valida/);
assert.equal(ui.validate(plan),plan);
assert.equal(ui.validate({...plan,nodes:[{...pendingNode,missing_count:103,missing:Array(100).fill('snippets/a.yaml')},alignedNode,blockedNode]}).nodes[0].missing_count,103);
assert.equal(ui.validate({...plan,nodes:[pendingNode,alignedNode,{...blockedNode,missing:null,different:null,extra:null}]}).nodes.length,3);
assert.deepEqual(ui.validateResults(done,['node-1']),done.results);
for(const bad of [{}, {...done,ok:false}, {...done,results:[]}, {...done,results:[{...done.results[0],node:'other'}]},
 {...done,results:[{...done.results[0],copied:-1}]}, {...done,results:[{...done.results[0],message:{}}]}])
 assert.throws(()=>ui.validateResults(bad,['node-1']),/non valida/);
const pending=[];global.api=(path,options)=>new Promise((resolve,reject)=>{
 assert.equal(options.quiet,true);assert.equal(options.timeoutMs,120000);pending.push({path,resolve,reject});
});
(async()=>{
 let first=ui.load(),second=ui.load();assert.equal(pending[0].path,'/clusters/0/sync-plan');
 pending[0].resolve(plan);await first;assert.equal(ui.d,null);assert.equal(get('np-delivery').attrs['aria-busy'],'true');
 pending[1].resolve(plan);await second;assert.equal(ui.d,plan);assert.deepEqual([...ui.selected],['node-1']);
 assert.equal(get('np-delivery').attrs['aria-busy'],'false');
 first=ui.load();ClusterMgr.current=1;second=ui.load();pending[3].resolve({...plan,cluster:'Other'});await second;
 pending[2].reject(new Error('old failure'));await first;assert.equal(ui.d.cluster,'Other');assert.equal(ui.error,'');
 first=ui.load();ui.leave();App.cur='nodes';pending[4].resolve(plan);await first;assert.equal(ui.d,null);
 App.cur='nodeprep';first=ui.load();pending[5].reject({status:429,message:'busy'});await first;
 assert(ui.error.includes("Operazione gia' in corso"));assert.equal(get('np-delivery').attrs['aria-busy'],'false');
 first=ui.load();pending[6].reject(new Error('<server unavailable>'));await first;
 assert(walk(get('np-delivery')).some(n=>n.innerHTML==='&lt;server unavailable&gt;'));
 first=ui.load();Auth.epoch++;pending[7].resolve(plan);await first;assert.equal(ui.d,null);
 first=ui.load();Auth.locked=true;pending[8].resolve(plan);await first;assert.equal(ui.d,null);Auth.locked=false;
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestSyncPlanActions(t *testing.T) {
	runSettingsJS(t, syncPlanUISource(t)+syncPlanHarness+`
const calls=[],replies=[];
global.api=async(path,options)=>{calls.push({path,...options});assert(replies.length,'unexpected request '+path);
 const reply=replies.shift();if(reply instanceof Error)throw reply;return reply;
};
(async()=>{
 for(const role of ['reader','operator']){reset();Auth.role=role;confirmation=null;ui.confirm();assert.equal(confirmation,null);assert.equal(calls.length,0);}
 Auth.role='admin';reset();ui.selected.clear();confirmation=null;ui.confirm();assert.equal(confirmation,null);
 reset();ui.paint();const input=walk(get('np-delivery')).find(n=>n.type==='checkbox');
 assert(input.checked);input.checked=false;input.events.change();assert.equal(ui.selected.size,0);assert(get('sp-run').disabled);
 input.checked=true;input.events.change();assert.equal(ui.selected.size,1);assert.equal(get('sp-run').textContent,'Sincronizza 1 nodi');
 ui.confirm();assert.equal(calls.length,0);assert.equal(confirmation.options.title,'Sincronizzare snippet e script?');
 assert.equal(confirmation.options.level,'danger');assert.equal(confirmation.options.minRole,'admin');
 assert.deepEqual(confirmation.options.lines,SyncPlanHelpers.confirmLines(plan,['node-1']));
 assert.equal(confirmation.options.consequence,'Gli snippet sono eseguiti come root nelle VM create da ora in poi su questi nodi. Nessun file viene cancellato.');
 let finish;global.api=(path,options)=>{calls.push({path,...options});return new Promise(resolve=>finish=resolve);};
 const pending=confirmation.ok();assert(ui.busy);assert(get('sp-run').disabled);
 assert(walk(get('np-delivery')).some(n=>n.attrs.role==='status'&&n.textContent==='Sincronizzazione in corso…'));
 assert(walk(get('np-delivery')).filter(n=>n.type==='checkbox').every(n=>n.disabled));
 const before=calls.length;ui.confirm();await ui.load();assert.equal(calls.length,before);
 assert.equal(calls[0].path,'/clusters/0/sync-run');assert.equal(calls[0].method,'POST');assert.equal(calls[0].timeoutMs,600000);assert.equal(calls[0].quiet,true);
 assert.deepEqual(JSON.parse(calls[0].body),{nodes:['node-1'],digest:plan.digest});
 global.api=async(path,options)=>{calls.push({path,...options});assert.equal(path,'/clusters/0/sync-plan');return {...plan,digest:'c'.repeat(24)};};
 finish(done);await pending;assert(!ui.busy);assert.equal(ui.d.digest,'c'.repeat(24));assert.deepEqual(accessRefreshes,[true]);
 assert(walk(get('np-delivery')).some(n=>n.textContent==='✓ ok · node-1: Copiati e verificati.'));
 // A changed plan must be re-read without retrying the write, preserving the server explanation.
 global.api=async(path,options)=>{calls.push({path,...options});assert(replies.length);const reply=replies.shift();if(reply instanceof Error)throw reply;return reply;};
 reset();ui.confirm();const conflict=new Error('<state changed>: nessun file copiato');conflict.status=409;conflict.body={reason:'plan_changed'};
 let count=calls.length;replies.push(conflict,{...plan,digest:'d'.repeat(24)});await confirmation.ok();
 assert.deepEqual(calls.slice(count).map(c=>c.path),['/clusters/0/sync-run','/clusters/0/sync-plan']);
 assert.equal(ui.message,conflict.message);assert.equal(ui.d.digest,'d'.repeat(24));assert.equal(ui.results.length,0);
 assert(walk(get('np-delivery')).some(n=>n.textContent===conflict.message));
 reset();ui.confirm();const failed={ok:false,results:[{node:'node-1',ok:false,copied:1,message:'Verifica fallita.'}]};
 replies.push(failed,new Error('Anteprima non disponibile'));await confirmation.ok();assert.equal(ui.d,null);
 assert(walk(get('np-delivery')).some(n=>n.textContent==='✕ non ok · node-1: Verifica fallita.'));
 assert(walk(get('np-delivery')).some(n=>n.innerHTML==='Anteprima non disponibile'));
 // The preview and session at confirmation time own the callback.
 for(const change of [()=>ClusterMgr.current++,()=>ui.leave(),()=>App.cur='nodes',()=>Auth.epoch++,()=>Auth.role='reader',()=>ui.seq++,()=>ui.d=structuredClone(plan)]){
  reset();App.cur='nodeprep';Auth.role='admin';ui.confirm();count=calls.length;change();await confirmation.ok();assert.equal(calls.length,count);
 }
 App.cur='nodeprep';Auth.role='admin';reset();ui.confirm();count=calls.length;
 global.api=(path,options)=>{calls.push({path,...options});return new Promise(resolve=>finish=resolve);};
 const stale=confirmation.ok();ui.leave();ClusterMgr.current++;finish(done);await stale;
 assert.equal(calls.length,count+1,'a late result cannot refresh another cluster');assert.equal(ui.results.length,0);
 // Switching clusters or signing in again must allow a new plan while the old write is still pending.
 for(const change of [()=>ClusterMgr.current++,()=>Auth.epoch++]){
  reset();ui.confirm();global.api=(path,options)=>{
   calls.push({path,...options});return path.endsWith('/sync-run')?new Promise(resolve=>finish=resolve):Promise.resolve({...plan,cluster:'New context'});
  };
  const old=confirmation.ok();change();count=calls.length;await ui.load();
  assert.equal(calls[count].path,'/clusters/'+ClusterMgr.current+'/sync-plan');assert(!ui.busy);
  finish(done);await old;assert.equal(ui.d.cluster,'New context');assert.equal(ui.results.length,0);
 }
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestSyncPlanSafeRendering(t *testing.T) {
	runSettingsJS(t, syncPlanUISource(t)+syncPlanHarness+`
const unsafe='<img src=x onerror="alert(1)"> & \'quoted\'';
reset();ui.d={...plan,nodes:[{...pendingNode,missing:Array(100).fill(unsafe),missing_count:103,different:[unsafe],extra:[unsafe]},
 alignedNode,{...blockedNode,message:unsafe}]};
ui.message=unsafe;ui.results=[{node:'node-1',ok:false,copied:0,message:unsafe}];ui.paint();
const all=walk(get('np-delivery'));
assert(all.some(n=>n.tag==='details'&&!n.open));
assert.equal(all.filter(n=>n.tag==='code'&&n.textContent===unsafe).length,102);
assert(all.some(n=>n.textContent==='e altri 3'));
assert(all.some(n=>n.textContent===unsafe));assert(all.some(n=>n.textContent===' '+unsafe));
assert(all.some(n=>n.textContent==='✕ non ok · node-1: '+unsafe));
assert(!all.some(n=>n.innerHTML.includes('<img')));
assert(all.some(n=>n.textContent==='Profilo standard · 4 snippet e 3 script attesi'));
assert(all.some(n=>n.textContent==='1 allineati · 1 da sincronizzare · 1 non pronti'));
assert(all.some(n=>n.textContent.includes('extra (non vengono cancellati)')));
const boxes=all.filter(n=>n.type==='checkbox');assert.equal(boxes.length,1);assert.equal(boxes[0].attrs['data-min-role'],'admin');
assert.equal(get('sp-run').attrs['data-min-role'],'admin');
for(const role of ['reader','operator']){Auth.role=role;ui.paint();assert(get('sp-run').disabled);assert(walk(get('np-delivery')).filter(n=>n.type==='checkbox').every(n=>n.disabled));}
ui.d={...plan,nodes:[],summary:{aligned:0,to_sync:0,not_ready:0}};ui.selected.clear();ui.paint();
assert(walk(get('np-delivery')).some(n=>n.textContent==='Nessun nodo disponibile.'));assert(get('sp-run').disabled);
`)
}

func TestSyncPlanUsesCSRFAndStepUp(t *testing.T) {
	runSettingsJS(t, syncPlanUISource(t)+syncPlanHarness+
		settingsJS(t, "async function api(", "// Librerie incorporate")+`
Object.assign(Auth,{csrf:'test-csrf',requests:new Set(),credentialsPending:false,promptStep:async()=>{prompts++;return '123456';}});
let prompts=0;const calls=[],replies=[[403,{reason:'step_up'}],[200,done],[200,plan]];
global.fetch=async(url,options)=>{calls.push({url,...options,headers:new Headers(options.headers)});assert(replies.length);
 const [status,body]=replies.shift();return {status,ok:status<300,headers:new Headers(),json:async()=>body};
};
(async()=>{
 reset();ui.confirm();await confirmation.ok();assert.equal(prompts,1);assert.equal(replies.length,0);
 const [first,retry,refresh]=calls;assert.equal(first.url,'/api/clusters/0/sync-run');assert.equal(retry.url,first.url);
 assert.equal(first.method,'POST');assert.equal(retry.method,'POST');assert.equal(retry.body,first.body);
 assert.deepEqual(JSON.parse(first.body),{nodes:['node-1'],digest:plan.digest});
 assert.equal(first.headers.get('X-CSRF-Token'),'test-csrf');assert.equal(retry.headers.get('X-CSRF-Token'),'test-csrf');
 assert.equal(retry.headers.get('X-TOTP-Code'),'123456');assert.equal(refresh.url,'/api/clusters/0/sync-plan');
 assert.deepEqual(accessRefreshes,[true]);
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}
