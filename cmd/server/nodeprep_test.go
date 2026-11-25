package main

import "testing"

func TestNodePrepHelpers(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "/* pmgr:nodeprep:begin */", "/* pmgr:nodeprep:end */")+`
const assert=require('node:assert/strict');
const {condition,visibleNodes,partialWords}=NodePrepHelpers;
for(const [summary,expected] of [
 [{ok:24,warn:0,fail:0,unknown:0},{level:'ok',fails:0,others:0,text:'Tutti i nodi sono pronti'}],
 [{warn:2,fail:0,unknown:3},{level:'warn',fails:0,others:5,text:'5 controlli da verificare'}],
 [{warn:2,fail:1,unknown:3},{level:'bad',fails:1,others:5,text:'1 controllo da sistemare'}],
 [{warn:0,fail:1,unknown:0},{level:'bad',fails:1,others:0,text:'1 controllo da sistemare'}],
 [{unknown:1},{level:'warn',fails:0,others:1,text:'1 controllo da verificare'}]
])assert.deepEqual(condition({summary}),expected);
const nodes=[
 {node:'node-1',online:true,checks:[{status:'ok'}]},
 {node:'node-2',online:true,checks:[{status:'warn'},{status:'ok'}]},
 {node:'node-10',online:true,checks:[{status:'unknown'}]},
 {node:'node-11',online:true,checks:[{status:'fail'}]},
 {node:'offline',online:false,checks:[]},
 {node:'empty',online:true,checks:[]}
];
const before=JSON.stringify(nodes);
assert.deepEqual(visibleNodes({nodes},true),nodes.slice(1));
assert.deepEqual(visibleNodes({nodes},false),nodes);
assert.deepEqual(visibleNodes({},true),[]);
assert.equal(JSON.stringify(nodes),before,'filter preserves server order and data');
assert.equal(partialWords(['permissions','clock','versions','snippets_storage','snippet_files','bridges','templates','template_storage','nodes','node']),
 'permessi del token, orologi dei nodi, versioni di Proxmox, storage degli snippet, file snippet, bridge di rete, template, storage dei template, elenco dei nodi, controlli dei nodi');
assert.equal(partialWords(['<script>','constructor','__proto__']),'<script>, constructor, __proto__');
assert.equal(partialWords(null),'');
`)
}

func TestNodePrepRequests(t *testing.T) {
	source := settingsJS(t, "/* pmgr:nodeprep:begin */", "/* pmgr:nodeprep:end */") +
		settingsJS(t, "const NodePrep={", "\n};")
	runSettingsJS(t, source+`
const assert=require('node:assert/strict');
const controls=new Map();
global.document={getElementById(id){if(!controls.has(id))controls.set(id,{disabled:false,hidden:false,textContent:'',innerHTML:'',replaceChildren(){this.innerHTML='';}});return controls.get(id);}};
global.ClusterMgr={current:0};global.App={cur:'nodeprep'};global.Auth={locked:false,epoch:1};
const check={id:'online',status:'ok',title:'Nodo',detail:'In linea',commands:[]};
const report={cluster:'Mock',generated_at:1700000000,summary:{ok:1,warn:0,fail:0,unknown:0},cluster_checks:[],nodes:[{node:'node-1',online:true,version:'8.2.4',checks:[check]}],partial:[]};
for(const bad of [null,{}, {...report,generated_at:NaN},{...report,summary:{...report.summary,fail:-1}},
 {...report,nodes:[{...report.nodes[0],checks:[{...check,status:'<script>'}]}]},
 {...report,nodes:[{...report.nodes[0],checks:[{...check,commands:[{}]}]}]},
 {...report,partial:[{}]}])assert.throws(()=>NodePrep.validate(bad),/non valida/);
assert.equal(NodePrep.validate(report),report);
const painted=[];NodePrep.paint=function(){painted.push(this.d);};
global.errBox=(message,retry)=>{assert.equal(retry,'NodePrep.load(true)');return message;};
global.toast=()=>assert.fail('load errors must not generate a toast');
let pending=[];
global.api=(path,options)=>new Promise((resolve,reject)=>{assert.equal(options.quiet,true);assert.equal(options.timeoutMs,120000);pending.push({path,resolve,reject});});
(async()=>{
 let first=NodePrep.load();assert.equal(pending[0].path,'/clusters/0/node-readiness');
 let second=NodePrep.load(true);assert.equal(pending[1].path,'/clusters/0/node-readiness?fresh=1');
 pending[0].resolve(report);await first;assert.equal(painted.length,0);assert(document.getElementById('np-recheck').disabled);
 pending[1].resolve(report);await second;assert.equal(painted.length,1);assert(!NodePrep.onlyProblems);assert(!document.getElementById('np-recheck').disabled);
 first=NodePrep.load();ClusterMgr.current=1;second=NodePrep.load();
 pending[3].resolve({...report,cluster:'Second',summary:{ok:0,fail:1,warn:0,unknown:0}});await second;
 assert.equal(pending[3].path,'/clusters/1/node-readiness');assert(NodePrep.onlyProblems);
 pending[2].reject(new Error('old error'));await first;assert.equal(NodePrep.d.cluster,'Second');assert.equal(painted.length,2);
 first=NodePrep.load();NodePrep.leave();App.cur='nodes';pending[4].resolve(report);await first;assert.equal(painted.length,2);
 App.cur='nodeprep';first=NodePrep.load();pending[5].reject({status:429,message:'busy'});await first;
 assert.equal(document.getElementById('np-body').innerHTML,"Un controllo e' gia' in corso, riprova tra qualche secondo");
 assert(!document.getElementById('np-recheck').disabled);assert(document.getElementById('np-only').disabled);
 first=NodePrep.load(true);pending[6].reject(new Error('Unavailable'));await first;
 assert.equal(document.getElementById('np-body').innerHTML,'Unavailable');
 first=NodePrep.load();Auth.epoch++;pending[7].resolve(report);await first;assert.equal(painted.length,2);
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestNodePrepRoutingAndScope(t *testing.T) {
	source := authUISource(t, "// pmgr:auth-ui:begin", "const Auth={") +
		authUISource(t, "const PMETA=", "window.addEventListener('popstate'") +
		settingsJS(t, "const Palette={", "\n};") +
		settingsJS(t, "// pmgr:autorefresh:begin", "// pmgr:autorefresh:end") +
		settingsJS(t, "async function api(", "// Librerie incorporate")
	runSettingsJS(t, source+`
const assert=require('node:assert/strict');
assert.deepEqual(Route.parse('#/nodeprep'),{page:'nodeprep'});assert.equal(PMETA.nodeprep,'Preparazione nodi');
assert(!AutoRefresh.PAGES.includes('nodeprep'));
global.Auth={locked:false,credentialsPending:false,epoch:1,requests:new Set()};global.ClusterMgr={current:2,clusters:[]};
global.Ceph={isAvail:()=>true};global.Gpu={isAvail:()=>true};
for(const role of ['reader','operator','admin']){
 Auth.role=role;assert(RoleUI.canPage('nodeprep'));
 const page=Palette.base().find(item=>item.label==='Preparazione nodi');assert(page);
 for(const term of ['nodo nuovo','snippet','onboarding','preparazione'])assert(page.kw.includes(term));
}
global.fetch=async(path)=>{assert.equal(path,'/api/clusters/2/node-readiness?fresh=1');return {ok:true,status:200,json:async()=>({})};};
api('/clusters/2/node-readiness?fresh=1',{quiet:true}).catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestNodePrepSafeCommandsAndCopy(t *testing.T) {
	source := settingsJS(t, "/* pmgr:nodeprep:begin */", "/* pmgr:nodeprep:end */") +
		settingsJS(t, "const NodePrep={", "\n};")
	runSettingsJS(t, source+`
const assert=require('node:assert/strict');
const esc=v=>String(v??'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
function element(tag){return {tag,children:[],attrs:{},innerHTML:'',textContent:'',disabled:false,
 append(...children){this.children.push(...children);},setAttribute(k,v){this.attrs[k]=v;},
 addEventListener(k,v){this[k]=v;},select(){this.selected=true;},focus(){this.focused=true;},remove(){this.removed=true;}};}
const body=element('body'),focus=element('button');
global.document={body,activeElement:focus,createElement:element,createDocumentFragment:()=>element('fragment'),execCommand:()=>true};
const messages=[];global.toast=(...args)=>messages.push(args);
const command='pvesm set local --content iso,snippets\n<svg onload="alert(1)"> & \'quoted\'';
const title='<script>Title</script>',detail='<img src=x onerror=alert(1)>';
const fragment=NodePrep.checks([{title,detail,status:'fail',commands:[command]}]);
const row=fragment.children[0];
assert(row.innerHTML.includes(esc(title)));assert(row.innerHTML.includes(esc(detail)));
assert(!row.innerHTML.includes('<script>'));assert(!row.innerHTML.includes('<img'));
assert.equal(row.children[0].textContent,'Da eseguire come root su un nodo del cluster:');
const block=row.children[1],code=block.children[0].children[0],button=block.children[1];
assert.equal(code.textContent,command);assert.equal(code.innerHTML,'');assert.equal(button.tag,'button');
assert.equal(button.attrs['aria-label'],'Copia comando: '+title);
Object.defineProperty(globalThis,'navigator',{value:{clipboard:{writeText:async text=>{assert.equal(text,command);}}},configurable:true});
(async()=>{
 await button.click();assert.equal(messages.pop()[0],'Comando copiato');assert.equal(body.children.length,0);
 navigator.clipboard.writeText=async()=>{throw new Error('Permission denied');};
 let copied=false;
 document.execCommand=action=>{assert.equal(action,'copy');const area=body.children.at(-1);assert(area.selected);assert.equal(area.value,command);copied=true;return true;};
 await NodePrep.copy(command,button);assert(copied);assert(body.children.at(-1).removed);assert(focus.focused);assert.equal(messages.pop()[1],'ok');
 navigator.clipboard=null;document.execCommand=()=>false;
 await NodePrep.copy(command,button);assert.equal(messages.pop()[1],'err');assert(body.children.at(-1).removed);assert(!button.disabled);
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}
