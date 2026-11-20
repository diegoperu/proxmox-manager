package main

import "testing"

func TestUpdateStatusText(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "// pmgr:update-status:begin", "// pmgr:update-status:end")+`
const assert=require('node:assert/strict');
for(const [state,text] of Object.entries({
 up_to_date:'Aggiornato',behind:'6 commit indietro rispetto a main',
 ahead_of_main:"Versione piu' recente di main",diverged:'Divergente da main',
 unknown_commit:'Versione non confrontabile (compilazione locale)',
 no_build_info:'Versione non confrontabile (compilazione locale)',
 disabled:'Controllo spento',pending:'In attesa del primo controllo',
 unreachable:'GitHub non raggiungibile'
}))assert.equal(updateStatusText({state,behind_by:6}),text);
const checked_at='2026-11-20T09:35:00Z';
assert.equal(updateStatusText({state:'unreachable',checked_at}),'GitHub non raggiungibile · ultimo controllo '+new Date(checked_at).toLocaleTimeString('it-IT',{hour:'2-digit',minute:'2-digit'}));
assert.equal(updateStatusText({state:'unreachable',checked_at:'<img onerror=alert(1)>'}),'GitHub non raggiungibile');
assert.equal(updateStatusText({state:'behind',behind_by:'<script>'}),'0 commit indietro rispetto a main');
assert.equal(updateStatusText({state:'<script>'}),'Stato aggiornamenti non disponibile');
`)
}

func TestUpdateUIRequestsAndUntrustedText(t *testing.T) {
	source := settingsJS(t, "// pmgr:update-status:begin", "// pmgr:update-status:end") + settingsJS(t, "const UpdateUI={", "\n};")
	runSettingsJS(t, source+`
const assert=require('node:assert/strict');
const nodes=new Map();
function element(){return {textContent:'',disabled:false,hidden:false,children:[],attributes:{},get childElementCount(){return this.children.length;},set innerHTML(v){throw Error('untrusted HTML');},replaceChildren(){this.children=[];},append(v){this.children.push(v);},setAttribute(k,v){this.attributes[k]=v;},removeAttribute(k){delete this.attributes[k];}};}
global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);},createElement(){return element();}};
const get=id=>document.getElementById(id);
const hostile='<img src=x onerror=alert(1)>';
const behind={state:'behind',behind_by:6,titles:[hostile],local:{}};
const calls=[];
global.api=async(path,options)=>{calls.push(path);return path==='/version'?{short:hostile,date:'2026-11-20T09:00:00Z',dirty:true}:behind;};
(async()=>{
 await UpdateUI.load();assert.deepEqual(calls,['/version','/update-status']);
 assert.equal(get('update-version').textContent,hostile+' · 2026-11-20T09:00:00Z · con modifiche locali');
 assert.equal(get('update-titles').children[0].textContent,hostile);
 assert.equal(get('update-dot').hidden,false);assert.equal(get('update-dot').attributes['aria-label'],'Aggiornamento disponibile: 6 commit');
 assert.equal(get('update-dot').title,'Aggiornamento disponibile: 6 commit');
 for(const state of ['up_to_date','ahead_of_main','diverged','unknown_commit','unreachable','disabled','no_build_info','pending']){UpdateUI.render({state});assert(get('update-dot').hidden);assert(get('update-titles').hidden);}
 global.api=async()=>{throw Error('endpoint unavailable');};await UpdateUI.load();assert.equal(get('update-message').textContent,'Stato aggiornamenti non disponibile');
 assert.equal(get('update-version').textContent,'Versione non disponibile');
 global.api=async(path)=>path==='/version'?{short:'0123456'}:behind;await UpdateUI.load();assert.equal(get('update-message').textContent,'');
 global.api=async()=>{const e=Error('rate limited');e.status=429;e.body={retry_after:121};throw e;};
 await UpdateUI.check();assert.equal(get('update-message').textContent,'riprova tra 3 minuti');assert(!get('update-check').disabled);
 let resolve;global.api=async()=>new Promise(done=>resolve=done);
 const running=UpdateUI.check();assert(get('update-check').disabled);await UpdateUI.load();
 const previousResolve=resolve;await UpdateUI.check();assert.equal(resolve,previousResolve);
 resolve(behind);await running;assert(!get('update-check').disabled);assert.equal(get('update-state').textContent,'6 commit indietro rispetto a main');
 let help;global.askConfirm=(options)=>help=options;UpdateUI.help();assert(help.lines.join(' ').includes('ultima release')&&help.lines.join(' ').includes('SHA256SUMS.sig')&&help.lines.join(' ').includes('release-sign verify'));assert(help.lines.join(' ').includes('sha256sum -c SHA256SUMS'));assert(help.lines.join(' ').includes('gh attestation verify'));
})().catch(e=>{console.error(e);process.exitCode=1;});
`)
}
