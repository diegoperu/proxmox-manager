package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditFormatters(t *testing.T) {
	keys := []string{}
	for key := range routeAccess {
		// The authorization middleware records state-changing requests, not GETs.
		if strings.HasPrefix(key, "GET ") {
			continue
		}
		keys = append(keys, key)
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	source := authUISource(t, "// pmgr:audit-format:begin", "// pmgr:audit-format:end")
	runAuthUI(t, source+"\nconst routeKeys="+string(encoded)+`; 
const assert=require('node:assert/strict');
for(const key of routeKeys){assert(Object.hasOwn(AUDIT_LABELS,key),'missing label: '+key);assert.notEqual(auditLabel(key),key);}
assert.equal(auditLabel('POST /api/nodes/{node}/qemu/{vmid}/action/{action}'),'Azione sulla VM');
assert.equal(auditLabel('login'),'Accesso');assert.equal(auditLabel('logout'),'Uscita');
assert.equal(auditLabel('setup'),'Configurazione iniziale');assert.equal(auditLabel('password'),'Cambio password');
for(const unknown of ['toString','__proto__','POST /new/<img src=x>'])assert.equal(auditLabel(unknown),unknown);
const clusters=[{label:'Calcolo'},{label:'Hosting'}];
assert.equal(auditTarget('cluster=1 node=n1 vmid=101 action=stop',clusters),'Cluster Hosting · Nodo: n1 · VMID: 101 · Azione: stop');
assert.equal(auditTarget('idx=0',clusters),'Cluster Calcolo');
assert.equal(auditTarget('cluster=4 node=n2',clusters),'Cluster 4 · Nodo: n2');
assert.equal(auditTarget('cluster=-1 cluster=1x cluster=__proto__',clusters),'Cluster -1 · Cluster 1x · Cluster __proto__');
assert.equal(auditTarget('name=anna cmd=reboot kind=qemu slot=hostpci0',clusters),'Utente: anna · Comando: reboot · Tipo: qemu · Slot: hostpci0');
assert.equal(auditTarget('',clusters),'—');assert.equal(auditTarget(null,clusters),'—');
assert.equal(auditTarget('other=<script> raw',clusters),'other=<script> · raw');
assert.equal(taskUser({pmgr_user:'Anna',user:'pmgr@pve!manager'}),'Anna tramite il manager');
for(const suffix of ['manager','local'])assert.equal(taskUser({user:'pmgr@pve!'+suffix}),'manager (utente non registrato)');
assert.equal(taskUser({user:'root@pam'}),'root@pam');assert.equal(taskUser({}), '');
`)
}

func TestAuditUILoadPagingExportAndErrors(t *testing.T) {
	source := authUISource(t, "// pmgr:audit-format:begin", "// pmgr:audit-format:end") +
		authUISource(t, "// pmgr:audit-ui:begin", "// pmgr:audit-ui:end") +
		authUISource(t, "function rptSafeCell(", "function rptIso(")
	runAuthUI(t, source+`
const assert=require('node:assert/strict');
const esc=v=>String(v??'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const nodes=new Map();const get=id=>{if(!nodes.has(id))nodes.set(id,{value:'',innerHTML:'',textContent:'',disabled:false,hidden:false,append(){}});return nodes.get(id);};
global.document={getElementById:get,createElement:()=>({value:"",textContent:""})};
global.Auth={locked:false,role:'admin',epoch:1,errorText:e=>e.message};global.canDo=(role,required)=>role===required;
global.Users={validName:v=>/^[a-z][a-z0-9._-]{1,31}$/.test(v)};
global.ClusterMgr={clusters:[{label:'Calcolo'}]};
global.errRow=(n,message)=>'<tr><td>'+esc(message)+'</td></tr>';
global.toast=()=>{};
const copied=[];Object.defineProperty(global,'navigator',{value:{clipboard:{async writeText(value){copied.push(value);}}}});
let exported;global.Reports={download:(blob,name)=>exported={blob,name}};
const elements=Object.fromEntries(['user','q','since','until','outcome'].map(n=>[n,{value:''}]));
get('audit-filters').elements=elements;get('audit-filters').reportValidity=()=>true;
const calls=[],responses=[];global.api=async(path)=>{calls.push(path);if(!responses.length)throw new Error('unexpected API request');const reply=responses.shift();if(reply instanceof Error)throw reply;return reply;};
const row=(id,extra={})=>({id,time:1800000000,user:'anna',ip:'127.0.0.1',action:'login',target:'cluster=0',status:200,upid:'UPID:n1:long-task-identifier-that-is-truncated',...extra});
(async()=>{
 const initial=Array.from({length:100},(_,i)=>row(200-i));
 elements.q.value="'%_";elements.since.value='2026-11-01';elements.until.value='2026-11-23';elements.outcome.value='ok';elements.user.value='anna';get('audit-user').value='anna';
 responses.push(initial,[{username:'anna'}]);await Audit.load();
 const query=new URLSearchParams(calls[0].split('?')[1]);assert.equal(query.get('q'),"'%_");assert.equal(query.get('since'),'2026-11-01');assert.equal(query.get('until'),'2026-11-23');assert.equal(query.get('outcome'),'ok');assert.equal(query.get('user'),'anna');
 assert.equal(Audit.rows.length,100);assert(!get('audit-more').hidden);assert(get('audit-body').innerHTML.includes('Cluster Calcolo'));assert(get('audit-body').innerHTML.includes('Accesso'));assert(!get('audit-body').innerHTML.includes('href='));
 // Paging keeps the applied filters even if the unsubmitted form changes.
 elements.q.value='unsubmitted';responses.push([row(100,{action:'<img src=x>',target:'node=<img>',user:'=formula',upid:'@formula'})]);await Audit.load(true);
 const next=new URLSearchParams(calls.at(-1).split('?')[1]);assert.equal(next.get('before'),'101');assert.equal(next.get('q'),"'%_");assert.equal(Audit.rows.length,101);assert(get('audit-more').hidden);
 assert(!get('audit-body').innerHTML.includes('<img'));assert(get('audit-body').innerHTML.includes('&lt;img'));
 await Audit.copy(100);assert.equal(copied.at(-1),'@formula');await Audit.copy(-1);assert.equal(copied.length,1);
 const write=navigator.clipboard.writeText;navigator.clipboard.writeText=async()=>{throw new Error('clipboard unavailable');};await Audit.copy(0);assert.equal(get('audit-upid-0').textContent,Audit.rows[0].upid);navigator.clipboard.writeText=write;
 Audit.exportCSV();assert.equal(exported.name,'registro.csv');const csv=await exported.blob.text();assert(csv.includes("'=formula"));assert(csv.includes("'@formula"));assert(csv.includes('Accesso'));
 // A paging failure keeps loaded rows and permits a retry.
 Audit.more=true;responses.push(new Error('offline'));await Audit.load(true);assert.equal(Audit.rows.length,101);assert.equal(get('audit-status').textContent,'offline');assert(!get('audit-more').disabled);
 responses.push([],[]);await Audit.load();assert.equal(Audit.rows.length,0);assert(get('audit-body').innerHTML.includes('Nessuna azione'));assert(get('audit-export').disabled);
 responses.push(new Error('<script>offline'),[]);await Audit.load();assert(!get('audit-body').innerHTML.includes('<script>'));assert(get('audit-body').innerHTML.includes('&lt;script&gt;offline'));
 const before=calls.length;Auth.role='reader';await Audit.load();Audit.exportCSV();assert.equal(calls.length,before);
 // Ignore a pending administrator response after the session changes.
 Auth.role='admin';let finish;global.api=path=>path==='/users'?Promise.resolve([]):new Promise(resolve=>finish=resolve);
 const pending=Audit.load();Auth.epoch++;Auth.locked=true;finish([row(1)]);await pending;assert.equal(Audit.rows.length,0);
})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}
