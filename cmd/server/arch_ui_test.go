package main

import (
	"io/fs"
	"strings"
	"testing"
)

func archUISource(t *testing.T) string {
	t.Helper()
	return settingsJS(t, "// pmgr:arch:begin", "// pmgr:arch:end")
}

func TestArchUIHelpers(t *testing.T) {
	runSettingsJS(t, archUISource(t)+`
const assert=require('node:assert/strict');
for(const [arch,label] of [['amd64','x86-64'],['arm64','ARM64'],['mixed','misto'],['','']])assert.equal(ArchUI.label(arch),label);
for(const unknown of [null,undefined,0,{},[],true,'x86_64','aarch64','constructor','__proto__','<script>'])assert.equal(ArchUI.label(unknown),'');
for(const [list,show] of [
 [[],false],[[''],false],[['',''],false],[['amd64'],false],[['arm64','arm64'],false],
 [['amd64','amd64'],false],[['amd64','arm64'],true],[['arm64','amd64',''],true],
 [['mixed'],true],[['','mixed'],true],[['','amd64'],false],[['arm64',''],false]
]){const before=[...list];assert.equal(ArchUI.shouldShow(list),show);assert.deepEqual(list,before);}
const label='<CALCOLO & "test">';
assert.equal(ArchUI.optionText(label,'amd64',true),label+' · x86-64');
assert.equal(ArchUI.optionText(label,'arm64',true),label+' · ARM64');
assert.equal(ArchUI.optionText(label,'mixed',true),label+' · misto');
for(const arch of ['amd64','arm64','mixed','','other'])assert.equal(ArchUI.optionText(label,arch,false),label);
for(const arch of ['','other'])assert.equal(ArchUI.optionText(label,arch,true),label);
`)
}

func TestArchUIMarkupAndHooks(t *testing.T) {
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(page)
	for _, marker := range []string{"// pmgr:arch:begin", "// pmgr:arch:end", `id="arch-chip"`, `id="pv-arch"`, `id="bal-arch"`, `id="na-arch"`} {
		if strings.Count(source, marker) != 1 {
			t.Errorf("missing or duplicated %s", marker)
		}
	}
	if strings.Index(source, "// pmgr:arch:begin") >= strings.Index(source, "// pmgr:arch:end") {
		t.Fatal("architecture markers out of order")
	}
	if !strings.Contains(source, `<span id="arch-chip" class="arch-chip" hidden></span>`) {
		t.Error("architecture chip must start hidden")
	}
	selectEnd := strings.Index(source, `style="display:none;min-width:120px"></select>`)
	chip := strings.Index(source, `<span id="arch-chip"`)
	if selectEnd < 0 || chip < selectEnd || strings.TrimSpace(source[selectEnd+len(`style="display:none;min-width:120px"></select>`):chip]) != "" {
		t.Error("architecture chip must immediately follow cluster selector")
	}
	if !strings.Contains(source, "${esc(ArchUI.optionText(c.label,ArchUI.of(c.idx),show))}") {
		t.Error("cluster option architecture text must be escaped by caller")
	}
}

const archUIDOM = `
const assert=require('node:assert/strict');
const controls=new Map();
function get(id){
 if(!controls.has(id))controls.set(id,{textContent:'',hidden:true,title:'',attrs:{},options:[],
  classList:{values:new Set(),toggle(name,on){if(on)this.values.add(name);else this.values.delete(name);},contains(name){return this.values.has(name);}},
  setAttribute(k,v){this.attrs[k]=String(v);},set innerHTML(value){assert.fail('architecture text must use textContent');}});
 return controls.get(id);
}
global.document={getElementById:get};
global.ClusterMgr={current:0,clusters:[{idx:0,label:'<CALCOLO & "test">'},{idx:3,label:'ARM'},{idx:7,label:'Non raggiungibile'}]};
global.Auth={epoch:1,locked:false};
get('cluster-select').options=ClusterMgr.clusters.map(c=>({value:String(c.idx),textContent:c.label}));
const pending=[];
global.api=(path,options)=>new Promise((resolve,reject)=>{
 assert.equal(path,'/clusters/arch');assert.equal(options.quiet,true);assert.equal(options.timeoutMs,15000);
 pending.push({resolve,reject});
});
const report=(a='amd64',b='arm64')=>[{index:7,arch:''},{index:3,arch:b},{index:0,arch:a}];
const chip=get('arch-chip');
const hints=()=>['pv-arch','bal-arch','na-arch'].map(get);
`

func TestArchUILoadPaintAndStaleResponses(t *testing.T) {
	runSettingsJS(t, archUISource(t)+archUIDOM+`
(async()=>{
 const first=ArchUI.load();assert.equal(pending.length,1);assert.equal(ArchUI.of(0),'');assert(chip.hidden);
 pending[0].resolve(report());await first;
 assert.equal(ArchUI.of(3),'arm64');assert.equal(ArchUI.of(1),'');assert(!chip.hidden);assert.equal(chip.textContent,'x86-64');
 assert.equal(chip.title,'Architettura del cluster: x86-64');assert.equal(chip.attrs['aria-label'],chip.title);assert(!chip.classList.contains('warn'));
 assert.deepEqual(get('cluster-select').options.map(o=>o.textContent),['<CALCOLO & "test"> · x86-64','ARM · ARM64','Non raggiungibile']);
 for(const hint of hints()){assert(!hint.hidden);assert.equal(hint.textContent,'Architettura: x86-64');}
 ClusterMgr.current=3;ArchUI.paint();assert.equal(chip.textContent,'ARM64');
 ClusterMgr.current=7;ArchUI.paint();assert(chip.hidden);assert(hints().every(h=>h.hidden));
 ClusterMgr.current=0;ArchUI.by.set(0,'mixed');ArchUI.paint();assert(!chip.hidden);assert.equal(chip.textContent,'misto');assert(chip.classList.contains('warn'));
 assert.equal(chip.title,'Il cluster ha nodi di architetture diverse: le VM non si spostano fra architetture');assert.equal(chip.attrs['aria-label'],chip.title);
 const same=ArchUI.load();pending.at(-1).resolve(report('arm64','arm64'));await same;
 assert(chip.hidden);assert(!chip.classList.contains('warn'));assert(hints().every(h=>h.hidden));
 assert.deepEqual(get('cluster-select').options.map(o=>o.textContent),ClusterMgr.clusters.map(c=>c.label));
 const older=ArchUI.load(),newer=ArchUI.load();
 pending.at(-1).resolve(report('arm64','amd64'));await newer;
 pending.at(-2).resolve(report());await older;assert.equal(ArchUI.of(0),'arm64');
 const obsolete=ArchUI.load();ArchUI.clear();assert(chip.hidden);assert.equal(ArchUI.of(0),'');
 pending.at(-1).resolve(report());await obsolete;assert.equal(ArchUI.of(0),'');
 const expired=ArchUI.load();Auth.epoch++;pending.at(-1).resolve(report());await expired;assert.equal(ArchUI.of(0),'');
 const locked=ArchUI.load();Auth.locked=true;pending.at(-1).resolve(report());await locked;assert.equal(ArchUI.of(0),'');Auth.locked=false;
 for(const bad of [null,{},[],report('bad'),[{index:0,arch:'amd64'},{index:0,arch:'arm64'},{index:7,arch:''}],
  [{index:-1,arch:'amd64'},...report().slice(1)],[{index:'7',arch:''},...report().slice(1)]]){
  const invalid=ArchUI.load();pending.at(-1).resolve(bad);await invalid;assert.equal(ArchUI.of(0),'');assert(chip.hidden);
 }
 const failed=ArchUI.load();pending.at(-1).reject(new Error('unreachable'));await failed;
 assert(chip.hidden);assert(hints().every(h=>h.hidden));
 ClusterMgr.clusters=[{idx:0,label:'Solo ARM'}];get('cluster-select').options=[{value:'0'}];
 const mixed=ArchUI.load();pending.at(-1).resolve([{index:0,arch:'mixed'}]);await mixed;assert(!chip.hidden);assert.equal(chip.textContent,'misto');
})().catch(error=>{console.error(error);process.exitCode=1;});
`)
}

func TestArchUINodeAddRequirement(t *testing.T) {
	runSettingsJS(t, nodeStepsSource(t)+settingsJS(t, "const NodePrep={", "\n};")+nodeStepsDOM+`
for(const [arch,message] of [
 ['amd64',"Il nuovo nodo deve essere x86-64: un nodo di un'altra architettura va in un cluster separato."],
 ['arm64',"Il nuovo nodo deve essere ARM64: un nodo di un'altra architettura va in un cluster separato."],
 ['mixed','Il cluster ha nodi di architetture diverse.'],['',''],['<img>','']
]){
 const report=addition();report.facts.arch=arch;NodeAdd.paint(report);
 const notices=walk(get('na-body')).filter(n=>n.tag==='span'&&(n.textContent.startsWith('Il nuovo nodo')||n.textContent.startsWith('Il cluster ha nodi')));
 assert.equal(notices.length,message?1:0);
 if(message){assert.equal(notices[0].textContent,message);assert.equal(notices[0].className,arch==='mixed'?'w':'');}
 assert(walk(get('na-body')).every(n=>n.innerHTML===''));
}
`)
}

func TestArchUINodePreparation(t *testing.T) {
	source := archUISource(t) + settingsJS(t, "/* pmgr:nodeprep:begin */", "/* pmgr:nodeprep:end */") +
		settingsJS(t, "const NodePrep={", "\n};")
	runSettingsJS(t, source+nodeStepsDOM+`
global.esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
document.createDocumentFragment=()=>new Element('fragment');
global.Dashboard={ICONS:{ok:'',warn:'',bad:''}};
const check={id:'arch',status:'warn',title:'Architettura del cluster',detail:'Il cluster ha nodi di architetture diverse.',commands:[]};
NodePrep.d={cluster:'Mock',generated_at:1700000000,summary:{ok:3,warn:1,fail:0,unknown:0},cluster_checks:[check],partial:[],
 nodes:['amd64','arm64','','<img>'].map((arch,i)=>({node:'node-'+i,arch,version:'9.2',online:true,checks:[]}))};
NodePrep.paint();
assert(walk(get('np-cluster')).some(n=>n.innerHTML.includes(check.title)&&n.innerHTML.includes(check.detail)));
const panels=get('np-nodes').children;
assert(panels[0].innerHTML.includes('9.2 · x86-64 · in linea'));
assert(panels[1].innerHTML.includes('9.2 · ARM64 · in linea'));
for(const panel of panels.slice(2)){assert(panel.innerHTML.includes('9.2 · in linea'));assert(!panel.innerHTML.includes('<img>'));}
`)
}

func TestArchUIStartupDoesNotWait(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "const App={", "\n};")+`
const assert=require('node:assert/strict'),events=[];
const _IS_CONSOLE=false;
global.Auth={locked:false,epoch:1,bootError:e=>assert.fail(e.message)};
global.ClusterMgr={async load(){events.push('clusters');}};
global.ArchUI={load(){events.push('arch');return new Promise(()=>{});}};
global.UpdateUI={load(){}};
global.Settings={async load(){events.push('settings');}};
global.Reports={init(){}};global.Route={apply(){events.push('route');}};
global.AutoRefreshUI={init(){}};
global.setInterval=()=>0;
App.bound=true;App.checkConn=()=>{};
(async()=>{
 const boot=App.boot();await new Promise(setImmediate);
 assert.deepEqual(events,['clusters','arch','settings','route']);await boot;
})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}

func TestArchUIAPIHasNoClusterQuery(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "async function api(", "// Librerie incorporate")+`
const assert=require('node:assert/strict');
global.Auth={locked:false,credentialsPending:false,epoch:1,requests:new Set()};
global.ClusterMgr={current:3};global.classifyApiError=()=>'';
global.fetch=async url=>{
 assert.equal(url,'/api/clusters/arch');
 return {status:200,ok:true,json:async()=>[]};
};
(async()=>{assert.deepEqual(await api('/clusters/arch',{quiet:true}),[]);})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}
