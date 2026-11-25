package main

import (
	"io/fs"
	"strings"
	"testing"
)

func nodeStepsSource(t *testing.T) string {
	t.Helper()
	return settingsJS(t, "/* pmgr:nodesteps:begin */", "/* pmgr:nodesteps:end */") +
		settingsJS(t, "const NodeStepsView={", "\n};") +
		settingsJS(t, "const NodeAdd={", "\n};") +
		settingsJS(t, "const NodeRemove={", "\n};")
}

const nodeStepsDOM = `
const assert=require('node:assert/strict');
class Element{
 constructor(tag){this.tag=tag;this.children=[];this.attrs={};this.events={};this._text='';this._html='';this.disabled=false;this.hidden=false;this.value='';}
 set textContent(v){this._text=String(v);this.children=[];this._html='';}get textContent(){return this._text;}
 set innerHTML(v){this._html=String(v);this.children=[];}get innerHTML(){return this._html;}
 append(...nodes){this.children.push(...nodes);}replaceChildren(...nodes){this.children=[...nodes];this._text='';this._html='';}
 setAttribute(k,v){this.attrs[k]=String(v);}addEventListener(k,fn){this.events[k]=fn;}
}
const elements=new Map();
const get=id=>{if(!elements.has(id))elements.set(id,new Element('div'));return elements.get(id);};
global.document={createElement:tag=>new Element(tag),getElementById:get};
const walk=node=>[node,...node.children.flatMap(walk)];
global.ClusterMgr={current:0};global.App={cur:'nodeadd',nav:p=>{App.cur=p;}};global.Auth={locked:false,epoch:1,role:'admin'};
global.canDo=(role,min)=>role==='admin'&&min==='admin';
const messages=[];global.toast=(...args)=>messages.push(args);
const ids=['guests','ha','replication','storage','pci_mapping','ceph_osd','ceph_services','power_off','quorum','delnode','cleanup'];
const step=(id,status='ok')=>({id,title:id,detail:'detail',status,check_status:status,items:[],commands:[]});
const removal=()=>({cluster:'C',node:'n2',address:'192.0.2.2',pinned:true,pin_known:true,steps:ids.map((id,i)=>({...step(id,i<9?'ok':i===9?'todo':'blocked'),check_status:i<9?'ok':'todo',commands:i===9?['pvecm delnode n2']:i===10?['rm -rf /etc/pve/nodes/n2','pvecm updatecerts --force']:[]}))});
const addition=()=>({cluster:'C',script_sha256:'a'.repeat(64),facts:{version:'9.2',versions_match:true,ceph_release:'squid',addresses:['192.0.2.2'],versions:[{node:'n2',version:'9.2',address:'192.0.2.2'}],issues:[]},steps:[step('prepare','todo')],new_nodes:[]});
`

func TestNodeStepsHelpers(t *testing.T) {
	source := settingsJS(t, "/* pmgr:nodesteps:begin */", "/* pmgr:nodesteps:end */")
	runSettingsJS(t, source+`
const assert=require('node:assert/strict');
for(const [status,word] of Object.entries({ok:'fatto',todo:'da fare',blocked:'in attesa del passo precedente',unknown:'non verificabile'}))assert.equal(NodeSteps.stateWord(status),word);
for(const hostile of ['constructor','__proto__','<img>',null,undefined])assert.equal(NodeSteps.stateWord(hostile),'non verificabile');
for(const status of ['todo','unknown','blocked']){
 const steps=[{status:'ok',commands:['done']},{status,commands:['first']},{status:'blocked',commands:['later']}];
 assert.equal(NodeSteps.firstOpenStep(steps),1);assert.deepEqual(NodeSteps.visibleCommands(steps,0),[]);assert.deepEqual(NodeSteps.visibleCommands(steps,1),['first']);assert.deepEqual(NodeSteps.visibleCommands(steps,2),[]);
}
assert.equal(NodeSteps.firstOpenStep([]),-1);assert.equal(NodeSteps.firstOpenStep([{status:'ok'}]),-1);assert.equal(NodeSteps.firstOpenStep(null),-1);
assert.deepEqual(NodeSteps.visibleCommands([],0),[]);assert.deepEqual(NodeSteps.visibleCommands(null,0),[]);
`)
}
func TestNodeStepsRenderAndValidation(t *testing.T) {
	source := nodeStepsSource(t) + settingsJS(t, "const NodePrep={", "\n};")
	runSettingsJS(t, source+nodeStepsDOM+`
const hostile='<img src=x onerror="alert(1)"> & <script>bad</script>';
const first={...step('first','todo'),title:hostile,detail:hostile,items:[hostile],commands:[hostile]};
const later={...step('later','blocked'),check_status:'todo',commands:['never visible']};
const list=NodeStepsView.render([first,later],true),all=walk(list);
assert(all.every(n=>n.innerHTML===''));assert(all.some(n=>n.tag==='h2'&&n.textContent==='1. '+hostile));assert(all.some(n=>n.tag==='li'&&n.textContent===hostile));
assert(all.some(n=>n.tag==='code'&&n.textContent===hostile));assert(!all.some(n=>n.tag==='code'&&n.textContent==='never visible'));
assert(all.some(n=>n.tag==='summary'&&n.textContent==='Comandi disponibili dopo i passi precedenti'));
assert(all.some(n=>n.textContent==='! da fare'));assert(all.some(n=>n.textContent==='◷ in attesa del passo precedente'));
const report=addition();report.cluster=hostile;report.facts.versions[0].node=hostile;report.facts.issues=[hostile];report.steps=[first];report.new_nodes=[{node:hostile,state:'unknown',detail:hostile}];
NodeAdd.validate(report);NodeAdd.paint(report);
assert(walk(get('na-body')).every(n=>n.innerHTML===''));assert(walk(get('na-body')).some(n=>n.textContent==='Cluster '+hostile));
const d=removal();NodeRemove.validate(d,'n2');NodeRemove.d=d;NodeRemove.idx=0;NodeRemove.gone=true;NodeRemove.paint();
let cleanup=walk(get('nr-body'));assert(cleanup.some(n=>n.textContent==='Nodo rimosso dal cluster'));assert(cleanup.some(n=>n.textContent==='11. cleanup'));
assert(cleanup.some(n=>n.tag==='code'&&n.textContent==='rm -rf /etc/pve/nodes/n2'));
assert(cleanup.some(n=>n.tag==='button'&&n.attrs['data-min-role']==='admin'));
Auth.role='reader';NodeRemove.paint();assert(!walk(get('nr-body')).some(n=>n.tag==='button'&&n.textContent==='Dimentica impronta SSH'));
NodeRemove.d.pinned=false;NodeRemove.paint();assert(walk(get('nr-body')).some(n=>n.textContent==='Nessuna impronta SSH da dimenticare.'));
for(const bad of [null,{}, {...d,node:'bad name'},{...d,steps:[...d.steps].reverse()},{...d,steps:d.steps.map(s=>({...s,status:'ok'}))}])assert.throws(()=>NodeRemove.validate(bad,'n2'));
for(const bad of [null,{}, {...addition(),script_sha256:'oops'},{...addition(),facts:{...addition().facts,ceph_release:'squid;id'}},{...addition(),steps:[{...first,link:'javascript:bad'}]}])assert.throws(()=>NodeAdd.validate(bad));
`)
}
func TestNodeStepsRequestsAndRemovedCleanup(t *testing.T) {
	source := nodeStepsSource(t) + settingsJS(t, "const NodePrep={", "\n};")
	runSettingsJS(t, source+nodeStepsDOM+`
const calls=[];global.api=(path,opts)=>new Promise((resolve,reject)=>calls.push({path,opts,resolve,reject}));
(async()=>{
 let first=NodeAdd.load();assert.equal(calls.at(-1).path,'/clusters/0/node-add');
 ClusterMgr.current=1;const second=NodeAdd.load();calls[1].resolve(addition());await second;
 const accepted=get('na-body').children[0];calls[0].resolve({...addition(),cluster:'stale'});await first;assert.equal(get('na-body').children[0],accepted);
 assert.equal(get('na-recheck').disabled,false);
 App.cur='noderemove';let loading=NodeRemove.load();assert.equal(calls.at(-1).path,'/nodes?cluster=1');calls.at(-1).resolve([{node:'n10'},{node:'n2'}]);await loading;
 assert.deepEqual(get('nr-select').children.map(n=>n.value),['','n2','n10']);get('nr-select').value='n2';get('nr-select').events.change();
 const checked=NodeRemove.check();assert.equal(calls.at(-1).path,'/clusters/1/node-removal?node=n2');calls.at(-1).resolve(removal());await checked;assert.equal(NodeRemove.d.node,'n2');assert(!get('nr-recheck').hidden);
 const gone=NodeRemove.check();calls.at(-1).reject({status:404,message:'missing',body:{reason:'node_not_member',cluster:'C'}});await gone;
 assert(NodeRemove.gone);assert(get('nr-recheck').hidden);assert(get('nr-select').disabled);assert(!walk(get('nr-body')).find(n=>n.textContent==='Dimentica impronta SSH').disabled);
 let confirmation;global.askConfirm=(options,ok)=>{confirmation={options,ok};};
 NodeRemove.forget();assert.equal(confirmation.options.minRole,'admin');assert.equal(confirmation.options.level,'danger');
 const forgetting=confirmation.ok();assert.equal(calls.at(-1).path,'/clusters/1/sync-access/pin/n2?address=192.0.2.2');assert.equal(calls.at(-1).opts.method,'DELETE');calls.at(-1).resolve({ok:true});await forgetting;assert(!NodeRemove.d.pinned);
 const before=calls.length;Auth.role='reader';NodeRemove.forget();assert.equal(calls.length,before);Auth.role='admin';
 // A configuration/route 404 with a prior report is not proof of node removal.
 NodeRemove.d=removal();NodeRemove.gone=false;get('nr-select').value='n2';const wrong404=NodeRemove.check();calls.at(-1).reject({status:404,message:'cluster missing'});await wrong404;assert(!NodeRemove.gone);assert.equal(NodeRemove.d,null);
 // A 404 without a prior successful safety report is not success.
 loading=NodeRemove.load();calls.at(-1).resolve([{node:'n2'}]);await loading;get('nr-select').value='n2';
 const missing=NodeRemove.check();calls.at(-1).reject({status:404,message:'nodo non trovato'});await missing;assert(!NodeRemove.gone);assert.equal(get('nr-body').textContent,'nodo non trovato');
 // Leaving the page invalidates all pending responses.
 loading=NodeRemove.check();NodeRemove.leave();App.cur='dashboard';calls.at(-1).resolve(removal());await loading;assert.equal(NodeRemove.d,null);
 App.cur='nodeadd';const obsolete=NodeAdd.load();Auth.epoch++;Auth.locked=true;calls.at(-1).resolve(addition());await obsolete;assert.equal(get('na-body').textContent,'Lettura del cluster e dei controlli…');
})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}
func TestNodeStepsNavigation(t *testing.T) {
	source := settingsJS(t, "const PMETA=", "// pmgr:autorefresh:begin")
	runSettingsJS(t, `global.window={addEventListener(){}};`+source+`
const assert=require('node:assert/strict');
assert.deepEqual(Route.parse('#/nodeadd'),{page:'nodeadd'});assert.deepEqual(Route.parse('#/noderemove'),{page:'noderemove'});
assert.equal(PMETA.nodeadd,'Aggiungi nodo');assert.equal(PMETA.noderemove,'Rimuovi nodo');
`)
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, needle := range []string{`data-page="nodeadd"`, `data-page="noderemove"`, `href="#/nodeadd"`, `href="#/noderemove"`, `nuovo nodo onboarding`, `togliere nodo delnode`} {
		if !strings.Contains(html, needle) {
			t.Errorf("missing navigation: %s", needle)
		}
	}
	auto := settingsJS(t, "// pmgr:autorefresh:begin", "// pmgr:autorefresh:end")
	runSettingsJS(t, auto+`const assert=require('node:assert/strict');for(const page of ['nodeadd','noderemove'])assert(!AutoRefresh.PAGES.includes(page));`)
}
