package main

import (
	"encoding/json"
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

func authUISource(t *testing.T, begin, end string) string {
	t.Helper()
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 {
		t.Fatalf("auth UI markers missing or duplicated: %q, %q", begin, end)
	}
	start, finish := strings.Index(text, begin), strings.Index(text, end)
	if start >= finish {
		t.Fatal("auth UI markers out of order")
	}
	return text[start:finish]
}

func runAuthUI(t *testing.T, source string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	cmd := exec.Command(node)
	cmd.Stdin = strings.NewReader(source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("auth UI JavaScript: %v\n%s", err, out)
	}
}

func TestAuthUIHelpers(t *testing.T) {
	runAuthUI(t, authUISource(t, "// pmgr:auth-ui:begin", "// pmgr:auth-ui:end")+`
const assert=require('node:assert/strict');
for(const [name,rank] of [['reader',1],['operator',2],['admin',3],['administrator',0],['',0],['toString',0],[null,0],[1,0]])assert.equal(roleRank(name),rank);
const roles=['reader','operator','admin'];
for(let i=0;i<3;i++)for(let j=0;j<3;j++)assert.equal(canDo(roles[i],roles[j]),i>=j);
for(const [user,required] of [['unknown','reader'],['admin','unknown'],['',''],[null,'reader'],['admin',null]])assert(!canDo(user,required));
for(const [status,body,want] of [
 [401,{setup:true},'setup'],[401,{login:true},'login'],[401,{setup:true,login:true},'setup'],
 [401,{},'other'],[401,null,'other'],[401,{setup:'true'},'other'],
 [403,{reason:'step_up'},'step_up'],[403,{required:'admin'},'role'],[403,{required:'operator'},'role'],
 [403,{required:'unknown'},'other'],[403,{reason:'csrf'},'csrf'],[403,{error:'richiesta non valida (token CSRF)'},'csrf'],
 [403,{error:'password attuale o codice non validi'},'other'],[403,null,'other'],
 [429,{reason:'step_up'},'other'],[200,{login:true},'other'],[500,{required:'admin'},'other']
])assert.equal(classifyApiError(status,body),want,JSON.stringify([status,body]));
for(const [a,b,valid] of [
 ['a'.repeat(12),'a'.repeat(12),true],['a'.repeat(256),'a'.repeat(256),true],
 ['a'.repeat(11),'a'.repeat(11),false],['a'.repeat(257),'a'.repeat(257),false],
 ['valid-password','different-password',false],['','',false],[null,null,false],
 ['🔐'.repeat(12),'🔐'.repeat(12),true],['🔐'.repeat(6),'🔐'.repeat(6),false],
 [' spaces count ',' spaces count ',true],['valid-password\n','valid-password\n',false],
 ['valid-password\u0000','valid-password\u0000',false],['valid-password\u0085','valid-password\u0085',false]
])assert.equal(validateNewPassword(a,b)==='',valid,JSON.stringify([a,b]));
`)
}

func TestAuthUINoBrowserStorage(t *testing.T) {
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"localStorage", "sessionStorage"} {
		if strings.Contains(string(page), name) {
			t.Errorf("page references forbidden browser storage: %s", name)
		}
	}
}

// Run the actual request wrapper against a fake same-origin server. No browser library required.
func TestAuthUIRequests(t *testing.T) {
	source := authUISource(t, "// pmgr:auth-ui:begin", "// Librerie incorporate")
	runAuthUI(t, source+`
const assert=require('node:assert/strict');
const nodes=new Map();
global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{textContent:''});return nodes.get(id);}};
const toasts=[];global.toast=(...args)=>toasts.push(args);
global.ClusterMgr={current:2};
const me={username:'dev',role:'admin',csrf:'csrf-in-memory',expires:1900000000};
const calls=[],responses=[];
global.fetch=async(url,opts)=>{calls.push({url,...opts,headers:new Headers(opts.headers)});return responses.shift()();};
function response(status,body,headers={}){responses.push(()=>({status,ok:status>=200&&status<300,statusText:'Failure',headers:new Headers(headers),json:async()=>body}));}
(async()=>{
 await assert.rejects(api('/nodes'),/Accesso richiesto/);assert.equal(calls.length,0);
 response(200,me);assert.deepEqual(await api('/auth/me'),me);assert.equal(calls[0].url,'/api/auth/me');
 Auth.accept(me);
 for(const method of ['POST','PUT','DELETE','PATCH','HEAD']){
  response(200,{ok:true});await api('/test',{method,headers:{'X-Other':'preserved'},body:method==='HEAD'?undefined:'{}'});
  const c=calls.at(-1);assert.equal(c.headers.get('X-CSRF-Token'),me.csrf);assert.equal(c.headers.get('X-Other'),'preserved');
  assert.equal(c.credentials,'same-origin');assert.equal(c.url,'/api/test?cluster=2');
 }
 response(200,[]);await api('/clusters');assert.equal(calls.at(-1).url,'/api/clusters');assert(!calls.at(-1).headers.has('X-CSRF-Token'));
 let prompts=0;Auth.promptStep=async()=>{prompts++;return '123456';};
 const body=JSON.stringify({some:'original payload'});
 response(403,{reason:'step_up'});response(200,{ok:true});await api('/clusters/3',{method:'PUT',body,headers:{'X-Other':'keep'}});
 const first=calls.at(-2),retry=calls.at(-1);
 assert.equal(prompts,1);assert.equal(first.url,retry.url);assert.equal(first.method,retry.method);assert.equal(first.body,retry.body);
 assert.equal(retry.headers.get('X-Other'),'keep');assert.equal(retry.headers.get('X-TOTP-Code'),'123456');assert.equal(retry.headers.get('X-CSRF-Token'),me.csrf);
 response(403,{reason:'step_up'});response(403,{reason:'step_up',error:'codice non valido'});
 const before=calls.length;await assert.rejects(api('/delete',{method:'DELETE',quiet:true}),/codice non valido/);assert.equal(calls.length-before,2);assert.equal(prompts,2);
 Auth.promptStep=async()=>{throw new Error('Azione annullata: codice di autenticazione non fornito.');};
 response(403,{reason:'step_up'});const cancelled=calls.length;
 await assert.rejects(api('/delete',{method:'DELETE',quiet:true}),/Azione annullata/);assert.equal(calls.length-cancelled,1);
 response(403,{required:'admin'});Auth.role='reader';
 await assert.rejects(api('/users',{quiet:true}),/Il tuo ruolo \(Lettore\) non permette questa azione/);
 assert(toasts.some(([message])=>message==='Il tuo ruolo (Lettore) non permette questa azione'));
 response(403,{error:'richiesta non valida (token CSRF)'});
 await assert.rejects(api('/nodes',{method:'POST',quiet:true}),e=>e.status===403);assert(!Auth.locked);
 response(429,{error:'troppi tentativi'},{'Retry-After':'45'});
 await assert.rejects(api('/auth/login',{method:'POST',quiet:true}),e=>e.status===429&&Auth.errorText(e).includes('45 secondi'));
 response(401,{error:'credenziali non valide'});await assert.rejects(api('/auth/login',{method:'POST',quiet:true}));assert(!Auth.locked,'bad login never expires an existing session');
 // A pending request cannot populate caches or continue a mutation after reauthentication.
 let release;responses.push(()=>new Promise(resolve=>release=()=>resolve({status:200,ok:true,json:async()=>({stale:true})})));
 const pending=api('/nodes',{quiet:true});await Promise.resolve();Auth.clear();Auth.accept(me);release();
 await assert.rejects(pending,/Sessione/);
 Auth.promptStep=async()=>{Auth.clear();Auth.accept(me);return '123456';};
 response(403,{reason:'step_up'});const changed=calls.length;
 await assert.rejects(api('/delete',{method:'DELETE',quiet:true}),/Sessione cambiata/);assert.equal(calls.length-changed,1);
 let expired=0;Auth.expired=()=>{expired++;Auth.clear();};
 for(const flag of ['login','setup']){
  Auth.accept(me);response(401,{[flag]:true});const count=calls.length;
  await assert.rejects(api('/nodes',{quiet:true}),e=>e.status===401);assert.equal(calls.length-count,1);assert(Auth.locked);
 }
 assert.equal(expired,2);assert.equal(responses.length,0);
})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}

func TestAuthUILifecycle(t *testing.T) {
	source := authUISource(t, "// pmgr:auth-ui:begin", "// Librerie incorporate") +
		authUISource(t, "const FOCUSABLE=", "// Riga di errore") +
		authUISource(t, "const App={", "// DMESG:")
	runAuthUI(t, source+`
const assert=require('node:assert/strict');
const nodes=new Map(),toasts=[],requests=[],replies=[];let reloads=0,stops=0,boots=0;
const _IS_CONSOLE=false;
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function element(id=''){
 const classes=new Set();return {id,textContent:'',innerHTML:'',value:'',hidden:false,inert:false,attrs:{},dataset:{},
  contains(el){return el===this;},
  classList:{contains:v=>classes.has(v),add:v=>classes.add(v),remove:v=>classes.delete(v),toggle(v,on){if(on)classes.add(v);else classes.delete(v);}},
  focus(){document.activeElement=this;},setAttribute(k,v){this.attrs[k]=v;},removeAttribute(k){delete this.attrs[k];},
  querySelector(){return get('focus-target');},replaceChildren(){this.innerHTML='';},reset(){this.value='';}};
}
const get=id=>{if(!nodes.has(id))nodes.set(id,element(id));return nodes.get(id);};
global.document={activeElement:null,body:element('body'),getElementById:get,querySelectorAll:selector=>selector==='.ov'?[get('mauth-step'),get('mauth-account')]:[],querySelector:()=>null};
global.location={reload(){reloads++;}};global.toast=(...args)=>toasts.push(args);
global.ClusterMgr={current:0,closeMenu(){},async load(){}};
global.AutoRefreshUI={started:true,stop(){stops++;this.started=false;},init(){this.started=true;},reset(){},mark(){}};
global.Ceph={stop(){}};global.Balance={timer:null};global.Palette={data:{old:true},init(){}};
global.Provision={bind(){}};global.Drawer={init(){}};global.UpdateUI={load(){}};
global.Settings={load:async()=>{}};global.Reports={init(){}};global.Route={apply(){}};
const _tc=new Map([['old',{}]]);
const me={username:'dev',role:'admin',csrf:'memory-token',expires:1900000000};
global.fetch=async(url,opts)=>{requests.push({url,...opts});assert(replies.length,'unexpected network call: '+url);return replies.shift()(url,opts);};
const reply=(status,body,headers={})=>replies.push(()=>({status,ok:status<300,statusText:'Failure',headers:new Headers(headers),json:async()=>body}));
const actualBoot=App.boot;App.boot=async()=>{assert(!Auth.locked);boots++;};
function form(values){
 const button={disabled:false},error={textContent:''},elements=Object.fromEntries(Object.entries(values).map(([k,value])=>[k,{value}]));
 return {dataset:{},elements,button,error,reportValidity:()=>true,querySelector:s=>s==='[type=submit]'?button:error,reset(){for(const field of Object.values(elements))field.value='';}};
}
(async()=>{
 // Before /me resolves no data request, route change or refresh can run.
 let release;replies.push(()=>new Promise(resolve=>release=()=>resolve({status:200,ok:true,json:async()=>me})));
 const startup=App.init();assert.equal(requests.length,1);assert.equal(requests[0].url,'/api/auth/me');
 App.refresh();App.nav('nodes');assert.equal(requests.length,1);assert.equal(App.cur,'dashboard');
 release();await startup;assert.equal(boots,1);assert.equal(Auth.username,'dev');assert(get('auth-screen').hidden);assert(!get('main').inert);
 // Public /me currently returns a generic 401; setup status is authoritative.
 Auth.lock();reply(401,{error:'sessione assente o scaduta'});reply(200,{open:true});await App.init();
 assert.equal(requests.at(-2).url,'/api/auth/me');assert.equal(requests.at(-1).url,'/api/auth/setup');assert(get('auth-body').innerHTML.includes('journalctl -u proxmox-manager'));assert(get('main').inert);
 reply(401,{setup:true});reply(200,{open:false});await App.init();assert.equal(get('auth-title').textContent,'Accedi');
 reply(500,{error:'errore interno'});await App.init();assert.equal(get('auth-title').textContent,'Accesso non disponibile');assert(get('auth-body').innerHTML.includes('Riprova'));
 // Failed login uses server text as textContent and never sends credentials in a URL.
 Auth.showLogin();const bad=form({username:'dev',password:'private-password',code:'123456'});
 reply(401,{error:'<generic server error>'});await Auth.login(bad);assert.equal(bad.error.textContent,'<generic server error>');assert(!bad.button.disabled);assert.equal(bad.elements.password.value,'');assert.equal(bad.elements.code.value,'');
 assert(!requests.at(-1).url.includes('private-password'));assert(!requests.at(-1).url.includes('123456'));
 reply(429,{error:'troppi tentativi'},{'Retry-After':'30'});await Auth.login(form({username:'dev',password:'private-password',code:'123456'}));
 // Fresh login runs the normal boot.
 reply(200,me);await Auth.login(form({username:'dev',password:'private-password',code:'123456'}));assert.equal(boots,2);
 // Session expiration keeps DOM data underneath, stops refresh, rejects the request, and never replays it.
 get('main').innerHTML='current page';reply(401,{login:true});const before=requests.length;
 await assert.rejects(api('/nodes',{method:'POST',body:'{}',quiet:true}),e=>e.status===401);
 assert(Auth.locked);assert(!AutoRefreshUI.started);assert.equal(get('main').innerHTML,'current page');assert(get('main').inert);assert.equal(get('auth-screen').attrs.role,'dialog');assert.equal(get('auth-title').textContent,'Sessione scaduta');
 reply(200,me);await Auth.login(form({username:'dev',password:'private-password',code:'123456'}));
 assert.equal(requests.length-before,2);assert.equal(boots,2);assert(!AutoRefreshUI.started);assert.equal(get('main').innerHTML,'current page');assert(!get('main').inert);assert.equal(_tc.size,0);
 // Modal cancellation and submission settle the original challenge and clear the input.
 const cancel=Auth.promptStep();get('auth-step-code').value='123456';closeModal('mauth-step');await assert.rejects(cancel,/Azione annullata/);assert.equal(get('auth-step-code').value,'');
 const challenge=Auth.promptStep();Auth.submitStep(form({code:'654321'}));assert.equal(await challenge,'654321');assert.equal(Auth.step,null);
 const interrupted=Auth.promptStep();Auth.expired();await assert.rejects(interrupted,/Azione annullata/);assert.equal(_modalStack.length,0);
 // A changed account must reload instead of inheriting the previous user's page and private caches.
 reply(200,{...me,username:'other'});await Auth.login(form({username:'other',password:'private-password',code:'123456'}));assert.equal(reloads,1);
 // Setup errors: wrong code, rate limit, closed; successful setup clears the one-time material.
 Auth.lock();Auth.showSetup();const setup=form({code:'one-time-code'});
 reply(403,{error:'codice non valido'});await Auth.setupKey(setup);assert.equal(setup.error.textContent,'codice non valido');assert.equal(Auth.setup,null);
 reply(429,{error:'troppi tentativi'},{'Retry-After':'60'});setup.elements.code.value='one-time-code';await Auth.setupKey(setup);assert(setup.error.textContent.includes('60 secondi'));
 reply(200,{secret:'ABCDEFGHIJKLMNOP',uri:'otpauth://totp/ProxmoxManager?secret=ABCDEFGHIJKLMNOP'});setup.elements.code.value='one-time-code';await Auth.setupKey(setup);
 assert(get('auth-body').innerHTML.includes('ABCD EFGH IJKL MNOP'));assert(get('auth-body').innerHTML.includes('QR code'));assert.equal(Auth.setup.code,'one-time-code');
 Auth.setupAccount();const complete=form({username:'dev',password:'long-password',repeat:'mismatch',totp:'123456'});const count=requests.length;
 await Auth.completeSetup(complete);assert.equal(requests.length,count);assert(complete.error.textContent.includes('non coincidono'));
 complete.elements.repeat.value='long-password';reply(403,{error:'codice non valido'});await Auth.completeSetup(complete);assert.equal(complete.error.textContent,'codice non valido');assert.equal(Auth.setup.code,'one-time-code');
 Object.assign(complete.elements.password,{value:'long-password'});Object.assign(complete.elements.repeat,{value:'long-password'});complete.elements.totp.value='654321';reply(200,{ok:true});await Auth.completeSetup(complete);
 assert.equal(Auth.setup,null);assert(get('auth-body').innerHTML.includes('Account creato'));
 Auth.showSetup();reply(410,{error:'configurazione chiusa'});await Auth.setupKey(form({code:'closed'}));assert.equal(Auth.setup,null);assert(get('auth-body').innerHTML.includes('configurazione chiusa'));
 // Password errors stay in the account form. Success retains this session and closes the modal.
 Auth.accept(me);const password=form({old:'old-password',new:'new-password',repeat:'new-password',code:'654321'});
 reply(403,{error:'password attuale o codice non validi'});await Auth.password(password);assert.equal(password.error.textContent,'password attuale o codice non validi');assert.equal(password.elements.old.value,'');assert.equal(password.elements.code.value,'');
 // Logout is CSRF-protected; reload clears every closure cache and pending task.
 _tc.set('old',{});Palette.data={old:true};reply(200,{ok:true});await Auth.logout();
 assert.equal(requests.at(-1).url,'/api/auth/logout');assert.equal(requests.at(-1).headers.get('X-CSRF-Token'),me.csrf);assert.equal(Auth.csrf,'');assert.equal(Auth.username,'');assert.equal(_tc.size,0);assert.equal(Palette.data,null);assert.equal(reloads,2);assert.equal(replies.length,0);
})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}

// Execute the whole script (including DOMContentLoaded and global event handlers), for both pages.
func TestAuthUIBootGatesEntireScript(t *testing.T) {
	source := strings.TrimPrefix(authUISource(t, "<script>", "</script>"), "<script>")
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	runAuthUI(t, "const source="+string(encoded)+`; 
const assert=require('node:assert/strict'),vm=require('node:vm');
(async()=>{
 for(const pathname of ['/','/console']){
  const events={},requests=[],nodes=new Map();let release;
  const node=id=>{if(!nodes.has(id))nodes.set(id,{id,value:'',textContent:'',innerHTML:'',style:{},classList:{contains:()=>false,add(){},remove(){},toggle(){}},setAttribute(){},removeAttribute(){},querySelector:()=>null,querySelectorAll:()=>[],replaceChildren(){this.innerHTML="";},focus(){}});return nodes.get(id);};
  const document={body:node('body'),documentElement:node('html'),activeElement:null,hidden:false,querySelectorAll:()=>[],querySelector:()=>null,getElementById:node,
   addEventListener(event,fn){(events[event]??=[]).push(fn);}};
  const context=vm.createContext({document,location:{pathname,hash:''},navigator:{platform:'Linux'},console,
   setTimeout:()=>1,clearTimeout(){},setInterval:()=>1,clearInterval(){},URLSearchParams,Headers,AbortController,
   fetch(url,opts){requests.push({url,opts});return new Promise(resolve=>release=resolve);}});
  context.window={location:context.location,matchMedia:()=>({matches:false,addEventListener(){}}),addEventListener(event,fn){(events[event]??=[]).push(fn);}};
  vm.runInContext(source,context);assert.equal(requests.length,0,pathname+': no API before DOM is ready');
  vm.runInContext('globalThis.boots=0;App.boot=async()=>{globalThis.boots++;};',context);
  for(const callback of events.DOMContentLoaded)callback();
  assert.equal(requests.length,1);assert.equal(requests[0].url,'/api/auth/me');
  // Global navigation, palette shortcuts, refresh and polling remain gated while /me is pending.
  vm.runInContext("Route.apply();App.nav('vms');App.refresh();Palette.open();AutoRefreshUI.tick();App.checkConn();",context);
  for(const callback of events.visibilitychange||[])callback();
  assert.equal(requests.length,1,pathname+': only /me before session known');assert.equal(context.boots,0);
  release({status:200,ok:true,json:async()=>({username:'dev',role:'admin',csrf:'in-memory',expires:1900000000})});
  await new Promise(setImmediate);assert.equal(context.boots,1);assert.equal(requests.length,1);
  assert.equal(vm.runInContext('Auth.username',context),'dev');assert(node('auth-screen').hidden);
 }
})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}
