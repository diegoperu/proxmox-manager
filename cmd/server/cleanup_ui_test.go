package main

import "testing"

func cleanupUISource(t *testing.T) string {
	t.Helper()
	return syncAccessUISource(t) +
		settingsJS(t, "/* pmgr:cleanup:begin */", "/* pmgr:cleanup:end */") +
		settingsJS(t, "const NodeCleanup={", "\n};")
}

const cleanupHarness = syncAccessHarness + `
const free={path:'snippets/free.yaml',class:'removable',used_by:[],used_by_count:0};
const used={path:'snippets/used.yaml',class:'in_use',used_by:['100 template'],used_by_count:1};
const uncertain={path:'snippets/maybe.yaml',class:'unknown',used_by:[],used_by_count:0};
const cleanupNode={node:'node-1',state:'ok',message:'Controllato.',files:[free,used,uncertain],removable:1,in_use:1,unknown:1,
 command:'umask 077 && backup && rm',undo:'tar undo'};
const cleanup={cluster:'Mock',generated_at:1700000000,references:{known:true,guests_read:1,configs_failed:0},
 nodes:[cleanupNode],manual:['Controlla /root','CONSERVA /etc/pmgr/cluster.conf']};
const ui=NodeCleanup;
`

func TestCleanupHelpers(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "/* pmgr:cleanup:begin */", "/* pmgr:cleanup:end */")+`
const assert=require('node:assert/strict'),{classWord,nodeSummary}=CleanupHelpers;
assert.equal(classWord('removable'),'rimovibile');assert.equal(classWord('in_use'),'IN USO: non cancellare');
for(const value of ['unknown','constructor','__proto__','<script>',null])assert.equal(classWord(value),'non verificabile: non cancellare');
assert.equal(nodeSummary({state:'ok',files:[]}),"nessun file in piu'");
assert.equal(nodeSummary({state:'ok',files:[{}],removable:2,in_use:3,unknown:4}),'2 rimovibili · 3 in uso · 4 non verificabili');
assert.equal(nodeSummary({state:'offline',message:'Spento'}),'Spento');assert.equal(nodeSummary(null),'Nodo non pronto.');
`)
}

func TestCleanupValidationAndRequests(t *testing.T) {
	runSettingsJS(t, cleanupUISource(t)+cleanupHarness+`
assert.equal(ui.validate(cleanup),cleanup);
for(const bad of [null,{}, {...cleanup,generated_at:NaN},{...cleanup,references:{known:'yes',guests_read:1,configs_failed:0}},
 {...cleanup,references:{...cleanup.references,guests_read:2001}}, {...cleanup,manual:[{}]},
 {...cleanup,nodes:[cleanupNode,cleanupNode]}, {...cleanup,nodes:[{...cleanupNode,node:'../bad'}]},
 {...cleanup,nodes:[{...cleanupNode,state:'constructor'}]}, {...cleanup,nodes:[{...cleanupNode,removable:3}]},
 {...cleanup,nodes:[{...cleanupNode,command:{}}]}, {...cleanup,nodes:[{...cleanupNode,command:''}]},
 {...cleanup,nodes:[{...cleanupNode,state:'offline'}]},
 {...cleanup,nodes:[{...cleanupNode,files:[{...free,class:'constructor'},used,uncertain]}]},
 {...cleanup,nodes:[{...cleanupNode,files:[free,{...used,used_by_count:21},uncertain]}]}])assert.throws(()=>ui.validate(bad),/non valide|non valida/);
const pending=[];global.api=(path,options)=>new Promise((resolve,reject)=>{
 assert.equal(options.quiet,true);assert.equal(options.timeoutMs,180000);assert(!options.method);pending.push({path,resolve,reject});
});
(async()=>{
 let first=ui.load(),second=ui.load();assert.equal(pending[0].path,'/clusters/0/node-cleanup');
 pending[0].resolve(cleanup);await first;assert.equal(ui.d,null);assert.equal(get('np-cleanup').attrs['aria-busy'],'true');
 pending[1].resolve(cleanup);await second;assert.equal(ui.d,cleanup);assert.equal(get('np-cleanup').attrs['aria-busy'],'false');
 first=ui.load();ClusterMgr.current++;second=ui.load();pending[3].resolve({...cleanup,cluster:'Next'});await second;
 pending[2].reject(new Error('old failure'));await first;assert.equal(ui.d.cluster,'Next');
 first=ui.load();ui.leave();App.cur='nodes';pending[4].resolve(cleanup);await first;assert.equal(ui.d,null);
 App.cur='nodeprep';first=ui.load();pending[5].reject({status:429,message:'busy'});await first;
 assert(walk(get('np-cleanup')).some(n=>n.textContent.includes("Operazione gia' in corso")));
 assert.equal(get('np-cleanup').attrs['aria-busy'],'false');
 const retry=walk(get('np-cleanup')).find(n=>n.tag==='button');retry.events.click();
 pending[6].reject(new Error('<img src=x onerror=alert(1)>'));await new Promise(setImmediate);
 assert(walk(get('np-cleanup')).some(n=>n.textContent==='<img src=x onerror=alert(1)>'));
 assert(!walk(get('np-cleanup')).some(n=>n.innerHTML.includes('<img')));
 first=ui.load();Auth.epoch++;pending[7].resolve(cleanup);await first;assert.equal(ui.d,null);
 first=ui.load();Auth.locked=true;pending[8].resolve(cleanup);await first;assert.equal(ui.d,null);
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestCleanupSafeRendering(t *testing.T) {
	runSettingsJS(t, cleanupUISource(t)+cleanupHarness+`
const hostile='<img src=x onerror="alert(1)"> & \'quoted\'';
ui.d={...cleanup,references:{known:false,guests_read:3,configs_failed:1},manual:[hostile],nodes:[
 {...cleanupNode,node:hostile,message:hostile,command:hostile,undo:hostile,
 files:[{...free,path:hostile},{...used,path:hostile,used_by:[hostile],used_by_count:21},uncertain]},
 {...cleanupNode,node:'node-2',files:[],removable:0,in_use:0,unknown:0,command:'',undo:''},
 {...cleanupNode,node:'node-3',state:'offline',message:hostile,files:[],removable:0,in_use:0,unknown:0,command:'',undo:''}
]};
for(const role of ['reader','operator','admin']){
 Auth.role=role;ui.paint();const all=walk(get('np-cleanup'));
 assert(!all.some(n=>n.innerHTML.includes('<img')));
 assert.equal(all.filter(n=>n.tag==='code'&&n.textContent===hostile).length,4);
 assert(all.some(n=>n.tag==='strong'&&n.textContent===hostile));assert(all.some(n=>n.textContent==='☐ '+hostile));
 assert(all.some(n=>n.textContent==='VM e template: '+hostile+' · e altri 20'));
 assert(all.some(n=>n.textContent==='3 configurazioni di VM e template lette · 1 non verificate'));
 assert(all.some(n=>n.attrs.role==='alert'&&n.textContent.includes('gli snippet non possono essere rimossi')));
 assert(all.some(n=>n.textContent==="nessun file in piu'"));
 for(const word of ['✓ rimovibile','✕ IN USO: non cancellare','? non verificabile: non cancellare'])assert(all.some(n=>n.textContent.trim()===word));
 assert(all.some(n=>n.textContent==='Da eseguire come root su '+hostile+': copia di sicurezza, poi cancellazione dei soli file rimovibili'));
 assert(all.some(n=>n.tag==='details'&&!n.open));
 assert.equal(all.filter(n=>n.tag==='button'&&n.textContent==='Copia').length,2);
}
ui.d={...cleanup,nodes:[]};ui.paint();assert(walk(get('np-cleanup')).some(n=>n.textContent==='Nessun nodo disponibile.'));
`)
}

func TestCleanupLoadsAfterSyncPlan(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "/* pmgr:nodeprep:begin */", "/* pmgr:nodeprep:end */")+settingsJS(t, "const NodePrep={", "\n};")+syncAccessHarness+`
let finishPlan,cleanupLoads=0,cleanupLeaves=0;
global.SyncAccess={load:async()=>{},leave(){}};
global.SyncPlan={load:()=>new Promise(resolve=>finishPlan=resolve),leave(){}};
global.NodeCleanup={load:async()=>cleanupLoads++,leave(){cleanupLeaves++;}};
const readiness={cluster:'Mock',generated_at:1,summary:{ok:0,warn:0,fail:0,unknown:0},cluster_checks:[],nodes:[],partial:[]};
global.api=async()=>readiness;NodePrep.paint=()=>{};
(async()=>{
 const first=NodePrep.load(true);assert.equal(cleanupLoads,0);assert.equal(cleanupLeaves,1);
 finishPlan();await first;assert.equal(cleanupLoads,1);
 const second=NodePrep.load();NodePrep.leave();finishPlan();await second;assert.equal(cleanupLoads,1);
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}
