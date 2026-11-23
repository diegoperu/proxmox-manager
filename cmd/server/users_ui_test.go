package main

import "testing"

func TestUsersUILifecycle(t *testing.T) {
	source := authUISource(t, "// pmgr:auth-ui:begin", "// Librerie incorporate") +
		authUISource(t, "const FOCUSABLE=", "// Riga di errore") +
		authUISource(t, "// pmgr:users-ui:begin", "// pmgr:users-ui:end")
	runAuthUI(t, source+`
const assert=require('node:assert/strict');
const nodes=new Map(),calls=[],replies=[],toasts=[];
function element(tag='div'){
 const classes=new Set();let id='';
 return {tag,children:[],textContent:'',innerHTML:'',value:'',disabled:false,dataset:{},attrs:{},isConnected:true,
  get id(){return id;},set id(value){id=value;nodes.set(id,this);},
  classList:{contains:v=>classes.has(v),add:v=>classes.add(v),remove:v=>classes.delete(v),toggle(v,on){on?classes.add(v):classes.delete(v);}},
  setAttribute(k,v){this.attrs[k]=v;},removeAttribute(k){delete this.attrs[k];},
  append(...children){this.children.push(...children);},
  replaceChildren(){const remove=el=>{for(const child of el.children)remove(child);if(el.id)nodes.delete(el.id);el.isConnected=false;};for(const child of this.children)remove(child);this.children=[];this.textContent='';this.innerHTML='';},
  querySelector(selector){if(selector==='fieldset')return this.fieldset;return this.children.find(c=>c.tag==='button')||null;},
  querySelectorAll(){return [];},contains(el){return el===this;},focus(){document.activeElement=this;},reset(){this.value='';}};
}
const get=id=>{if(!nodes.has(id)){const el=element();el.id=id;}return nodes.get(id);};
const newForm={reset(){},querySelector:()=>({textContent:''})};
global.document={documentElement:element('html'),body:element('body'),activeElement:null,getElementById:get,createElement:element,
 querySelector:s=>s==='#musers-new form'?newForm:null,querySelectorAll:s=>s==='.ov'?[get('musers-credentials'),get('musers-new')]:[]};
global.toast=(...args)=>toasts.push(args);
global.errRow=(n,message)=>'<error>'+esc(message)+'</error>';
const esc=v=>String(v??'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const js=v=>esc(JSON.stringify(String(v)));
global.ClusterMgr={current:0,closeMenu(){}};global.location={hash:'#/users'};
const copied=[];Object.defineProperty(global,'navigator',{value:{clipboard:{async writeText(value){copied.push(value);}}}});
global.fetch=async(url,opts)=>{assert(replies.length,'unexpected request '+url);calls.push({url,...opts,headers:new Headers(opts.headers)});const [status,body]=replies.shift();return {status,ok:status<300,statusText:'Failure',headers:new Headers(),json:async()=>body};};
const response=(status,body)=>replies.push([status,body]);
const me={username:'admin',role:'admin',csrf:'test-csrf',expires:1900000000};
const account=(username,role='reader',disabled=false)=>({username,role,disabled,created:1780000000});
const credentials=()=>({username:'new-user',password:'A-new-random-password',totp_secret:'ABCDEFGHIJKLMNOP',totp_uri:'otpauth://not-retained'});
let confirmation;global.askConfirm=(options,ok)=>confirmation={options,ok};
let stops=0;Auth.stop=()=>{stops++;};
const actualExpired=Auth.expired;let expirations=0;Auth.expired=()=>{expirations++;Auth.clear();};
let prompts=0;Auth.promptStep=async()=>{prompts++;return '123456';};
function form(username,role){
 const button={disabled:false},error={textContent:''};
 return {dataset:{},elements:{username:{value:username},role:{value:role}},reportValidity:()=>true,
 querySelector:s=>s==='[type=submit]'?button:error,error};
}
(async()=>{
 Auth.accept(me);
 response(200,[account('admin','admin'),account('dev','operator'),account('off','reader',true)]);await Users.load();
 const markup=get('users-body').innerHTML;
 assert(markup.includes('Amministratore'));assert(markup.includes('Operatore'));assert(markup.includes('Lettore'));
 assert(markup.includes('Disabilitato'));assert(markup.includes('Attivo'));assert(markup.includes('Abilita'));
 const own=markup.split('</tr>')[0];assert(/<select[^>]+disabled/.test(own));assert(/<button[^>]+disabled[^>]+Users.setDisabled/.test(own));
 assert(!/<button[^>]+disabled[^>]+Users.reset/.test(own),'own reset is permitted');
 let before=calls.length;Users.changeRole('admin','reader');Users.setDisabled('admin',true);Users.changeRole('dev','owner');Users.reset('../bad');assert.equal(calls.length,before);
 Auth.role='reader';await Users.load();await Users.update('dev',{disabled:true});assert.equal(calls.length,before);Auth.role='admin';
 const bad=form('../bad','admin');await Users.create(bad);assert.equal(calls.length,before);assert(bad.error.textContent.includes('non valido'));
 response(200,[account('<img src=x onerror=alert(1)>')]);await Users.load();assert(!get('users-body').innerHTML.includes('<img'));assert(get('users-body').innerHTML.includes('Elenco utenti non valido'));
 // Creating an account uses the existing CSRF/step-up wrapper. Only the dialog retains its credentials.
 const d=credentials();response(403,{reason:'step_up'});response(200,d);response(200,[account('admin','admin'),account('new-user')]);
 await Users.create(form('new-user','operator'));
 const writes=calls.filter(c=>c.method==='POST');assert.equal(writes.length,2);assert.equal(writes[0].url,'/api/users');
 assert.deepEqual(JSON.parse(writes[0].body),{username:'new-user',role:'operator'});assert.equal(writes[1].body,writes[0].body);
 assert.equal(writes[1].headers.get('X-CSRF-Token'),'test-csrf');assert.equal(writes[1].headers.get('X-TOTP-Code'),'123456');assert.equal(prompts,1);
 assert.deepEqual(d,{});assert(get('musers-credentials').classList.contains('open'));assert.equal(get('users-credential-password').textContent,'A-new-random-password');
 assert(!get('users-body').innerHTML.includes('A-new-random-password'));
 assert(!get('users-credentials-body').children.some(el=>el.innerHTML.includes('A-new-random-password')));
 await Users.copy('totp');assert.equal(copied.at(-1),'ABCDEFGHIJKLMNOP');
 closeModal('musers-credentials');assert(get('musers-credentials').classList.contains('open'));assert(nodes.has('users-credential-password'),'Esc cannot discard one-time material');
 Users.acknowledge();assert(!get('musers-credentials').classList.contains('open'));assert(!nodes.has('users-credential-password'));assert.equal(get('users-credentials-body').children.length,0);
 before=copied.length;await Users.copy('password');assert.equal(copied.length,before);
 // A rejected update restores the server's role, instead of leaving an optimistic value in the row.
 response(409,{error:'ultimo amministratore'});response(200,[account('admin','admin'),account('dev','operator')]);await Users.update('dev',{role:'reader'});
 assert(toasts.some(([s])=>s==='ultimo amministratore'));assert(get('users-body').innerHTML.includes('value="operator" selected'));
 response(200,{ok:true});response(200,[account('admin','admin'),account('dev','reader',true)]);await Users.update('dev',{disabled:true});
 assert.equal(calls.at(-2).method,'PUT');assert.deepEqual(JSON.parse(calls.at(-2).body),{disabled:true});
 // Reset is a danger confirmation; no request is sent before confirmation.
 before=calls.length;Users.reset('dev');assert.equal(calls.length,before);assert.equal(confirmation.options.level,'danger');assert.equal(confirmation.options.minRole,'admin');
 const reset=credentials();reset.username='dev';response(200,reset);response(200,[account('dev')]);await confirmation.ok();
 assert.equal(calls.at(-2).url,'/api/users/dev/reset');assert.equal(calls.at(-2).method,'POST');assert.deepEqual(reset,{});Users.acknowledge();
 // Resetting the current account pauses requests until the credentials have been acknowledged.
 const ownCredentials=credentials();ownCredentials.username='admin';response(200,ownCredentials);await Users.resetNow('admin');
 assert.equal(stops,1);assert(Auth.credentialsPending);assert.equal(expirations,0);assert(nodes.has('users-credential-totp'));
 before=calls.length;await assert.rejects(api('/nodes'),/Accesso richiesto/);assert.equal(calls.length,before);
 Users.acknowledge();assert.equal(expirations,1);assert(!nodes.has('users-credential-totp'));assert(!Auth.credentialsPending);
 // Expiration/logout can clear the material even though ordinary modal dismissal is blocked.
 Auth.accept(me);Users.showCredentials(credentials());Auth.lock();assert(!nodes.has('users-credential-password'));assert(!get('musers-credentials').classList.contains('open'));
 // Invalid one-time responses are cleared too, and never injected as markup.
 const invalid={username:'dev',password:'<script>danger</script>',totp_secret:'<bad>'};
 assert.throws(()=>Users.showCredentials(invalid),/non valide/);assert.deepEqual(invalid,{});assert.equal(get('users-credentials-body').children.length,0);
 assert.equal(replies.length,0);assert(!Object.values(Users).includes('A-new-random-password'));
})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}
