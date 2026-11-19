package main

import (
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

func TestSettingsStorageClasses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "// pmgr:report-settings:begin", "// pmgr:report-settings:end"
	text := string(page)
	start, finish := strings.Index(text, begin), strings.Index(text, end)
	if start < 0 || finish < start {
		t.Fatal("report settings markers missing")
	}
	driver := `
const assert=require('node:assert/strict');
const esc=v=>String(v??'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const ui=ReportSettingsUI;
assert.equal(ui.storageProposal('fast-SSD'),'ssd');assert.equal(ui.storageProposal('backup-HDD'),'hdd');assert.equal(ui.storageProposal('local'),'');
ui.detectedStorage=['pool-ssd','pool-hdd','local'];
const markup=ui.storageTable({'pool-ssd':'hdd','manual-only':'other'});
assert.equal((markup.match(/<tr data-storage/g)||[]).length,4);
const byName=name=>markup.match(new RegExp('<tr[^>]*>(?:(?!</tr>)[\\s\\S])*value="'+name+'"(?:(?!</tr>)[\\s\\S])*</tr>'))?.[0];
assert(byName('pool-ssd').includes('value="hdd" selected'),'saved class wins over name');
assert(!byName('pool-ssd').includes('proposta dal nome'));
assert(byName('pool-hdd').includes('value="hdd" selected'));assert(byName('pool-hdd').includes('proposta dal nome'));
assert(byName('local').includes('value="" selected'));assert(!byName('local').includes('proposta dal nome'));
assert(byName('manual-only').includes('Nome storage manuale'));assert(byName('manual-only').includes('data-remove'));
assert(!byName('pool-ssd').includes('data-remove'));
assert(!ui.storageTable({},false).includes('proposta dal nome'),'cancel reloads saved classes without reapplying proposals');
assert(ui.storage('<script>"','other').includes('&lt;script&gt;&quot;'));assert(!ui.storage('<script>"','other').includes('<script>'));

// Controls stand in for DOM nodes; production detection, read(), save() and note updates run unchanged.
const nodes=new Map();
function row(name,value,{detected=false,proposed=false}={}){
 const attrs=new Set([...(detected?['data-detected']:[]),...(proposed?['data-proposed']:[])]);
 const controls={name:{value:name},class:{value}},note={textContent:''};
 const item={controls,note,attrs,removed:false,setAttribute(k){attrs.add(k);},hasAttribute:k=>attrs.has(k),
  querySelector(selector){if(selector==='[data-storage-note]')return note;if(selector==='[data-remove]')return {remove(){item.removed=true;}};
   const key=/data-key="([^"]+)"/.exec(selector)?.[1];return controls[key];}};
 return item;
}
const storage={rows:[row('saved-ssd','other'),row('pending-manual','hdd')],
 querySelectorAll(){return this.rows;},
 insertAdjacentHTML(position,html){
  const name=/data-key="name"[^>]*value="([^"]*)"/.exec(html)[1];
  const value=/<option value="([^"]*)" selected>/.exec(html)?.[1]??'';
  this.rows.push(row(name,value,{detected:html.includes('data-detected'),proposed:html.includes('data-proposed')}));
 }};
function element(){return {innerHTML:'',textContent:'',hidden:false,disabled:false,classList:{toggle(){}},querySelector:()=>null,querySelectorAll:()=>[]};}
function fields(values){const controls=Object.fromEntries(Object.entries(values).map(([k,v])=>[k,{value:String(v??''),validity:{badInput:false}}]));return {
 ...element(),controls,querySelector:s=>controls[/data-key="([^"]+)"/.exec(s)?.[1]],querySelectorAll:()=>[]};}
nodes.set('rs-storage',storage);
nodes.set('rs-internal',fields(Object.fromEntries(ui.internal.map(f=>[f[0],f[5]==='optional'?'':0]))));
nodes.set('rs-defaults',fields(Object.fromEntries(ui.thresholds.map(f=>[f[0],'']))));
global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);},
 querySelectorAll:s=>s.startsWith('#rs-storage ')?storage.rows.filter(r=>!r.removedRow):[],querySelector:s=>s==='#rs-exchange input'?{value:'0'}:null};
ui.idx=0;ui.savedStorage={'saved-ssd':'other'};ui.baseline=ui.serialize(ui.read(false));
ui.detectStorage([{storage:'pool-ssd'},{storage:'pool-ssd'},{storage:'pool-hdd'},{storage:'local'},
 {storage:'saved-ssd'},{storage:'pending-manual'},{storage:'../invalid'},{storage:42}]);
assert.equal(storage.rows.length,5,'detected names deduplicated and existing manual rows reused');
assert.equal(storage.rows[0].controls.class.value,'other','saved value preserved');
assert.equal(storage.rows[1].controls.class.value,'hdd','pending manual edit preserved');
assert(storage.rows[1].controls.name.readOnly);assert(storage.rows[1].removed);
const ssd=storage.rows.find(r=>r.controls.name.value==='pool-ssd');
assert.equal(ssd.controls.class.value,'','name proposals never fill the form on load');
assert(!ui.isDirty(),'opening the tab must not create unsaved changes');
assert.equal(ui.proposable().length,2,'pool-ssd and pool-hdd can be proposed');
assert(document.getElementById('rs-storage-propose').innerHTML.includes('Applica le proposte dal nome (2 storage)'));
ui.applyStorageProposals();
assert.equal(ssd.controls.class.value,'ssd');assert.equal(ssd.note.textContent,'proposta dal nome');
assert.equal(storage.rows.find(r=>r.controls.name.value==='local').controls.class.value,'');
assert.equal(document.getElementById('rs-storage-propose').innerHTML,'','button gone once applied');
assert(ui.isDirty(),'applied proposals remain unsaved until PUT');
let draft=ui.read();assert.deepEqual({...draft.storage_classes},{'saved-ssd':'other','pending-manual':'hdd','pool-hdd':'hdd','pool-ssd':'ssd'});
assert(!Object.hasOwn(draft.storage_classes,'local'),'unclassified rows never reach PUT');
storage.rows.push(row('',''));assert.doesNotThrow(()=>ui.read(),'empty unclassified manual row omitted');
storage.rows.push(row('pool-ssd','hdd'));assert.throws(()=>ui.read(),/duplicato/);storage.rows.pop();
storage.rows.push(row('../invalid','ssd'));assert.throws(()=>ui.read(),/nome o classe/);storage.rows.pop();
ssd.controls.class.value='other';ui.updateStorageNotes();assert.equal(ssd.note.textContent,'rilevato nel cluster');
ssd.controls.class.value='ssd';ui.updateStorageNotes();assert.equal(ssd.note.textContent,'proposta dal nome');
global.toast=()=>{};
(async()=>{
 let sent;
 global.api=async(path,options)=>{assert.equal(path,'/clusters/0/report-settings');sent=JSON.parse(options.body);return {};};
 await ui.save();assert(sent);assert(!Object.hasOwn(sent.storage_classes,'local'));
 assert(!ui.isDirty());assert.equal(ssd.note.textContent,'rilevato nel cluster','proposal mark clears only after successful save');
 ssd.controls.class.value='hdd';await ui.save();assert.equal(sent.storage_classes['pool-ssd'],'hdd');
 ssd.controls.class.value='ssd';ui.updateStorageNotes();assert.equal(ssd.note.textContent,'proposta dal nome');
 global.api=async()=>{throw new Error('Rejected');};await ui.save();
 assert(ui.isDirty());assert.equal(ssd.note.textContent,'proposta dal nome','failed save keeps proposal pending');
 assert.equal(document.getElementById('rs-save-error').textContent,'Rejected');
})().catch(err=>{console.error(err);process.exitCode=1;});
`
	if out, err := exec.Command(node, "-e", text[start:finish]+driver).CombinedOutput(); err != nil {
		t.Fatalf("storage classes JavaScript: %v\n%s", err, out)
	}
}
