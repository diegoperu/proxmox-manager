package main

import (
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

func TestSettingsClusterMenu(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	start := strings.Index(text, "const ClusterMgr={")
	if start < 0 {
		t.Fatal("ClusterMgr missing")
	}
	end := strings.Index(text[start:], "\n};")
	if end < 0 {
		t.Fatal("ClusterMgr end missing")
	}
	driver := `
const assert=require('node:assert/strict');
global.ArchUI={clear(){},load(){}};
const esc=v=>String(v??'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const listeners=new Map(),nodes=new Map();
global.document={activeElement:null,getElementById:id=>nodes.get(id),
 addEventListener(type,fn,capture){listeners.set(type,fn);},
 removeEventListener(type,fn){assert.equal(listeners.get(type),fn);listeners.delete(type);}};
function control(attrs={}){return {attrs,focus(){document.activeElement=this;},
 setAttribute(k,v){this.attrs[k]=v;},getAttribute(k){return this.attrs[k];}};}
const first=control(),last=control(),popup=control();popup.hidden=true;
popup.querySelectorAll=()=>[first,last];nodes.set('menu',popup);
const button=control({'aria-controls':'menu','aria-haspopup':'menu','aria-expanded':'false'});
button.parentElement={contains:t=>[button,popup,first,last].includes(t)};
const event=(key,currentTarget=popup)=>({key,currentTarget,preventDefault(){this.prevented=true;},stopPropagation(){}});
const ui=ClusterMgr;
ui.toggleMenu(button);assert(!popup.hidden);assert.equal(button.attrs['aria-expanded'],'true');assert.equal(document.activeElement,first);
ui.menuKey(event('ArrowDown'));assert.equal(document.activeElement,last);
ui.menuKey(event('ArrowDown'));assert.equal(document.activeElement,first);
ui.menuKey(event('ArrowUp'));assert.equal(document.activeElement,last);
ui.menuKey(event('Home'));assert.equal(document.activeElement,first);
ui.menuKey(event('End'));assert.equal(document.activeElement,last);
ui.menuKey(event('Escape'));assert(popup.hidden);assert.equal(button.attrs['aria-expanded'],'false');assert.equal(document.activeElement,button);assert.equal(listeners.size,0);
ui.menuKey(event('ArrowUp',button));assert.equal(document.activeElement,last);
listeners.get('click')({target:first});assert(!popup.hidden,'inside click keeps menu open');
listeners.get('click')({target:{}});assert(popup.hidden);assert.equal(document.activeElement,button);assert.equal(listeners.size,0);
ui.toggleMenu(button);listeners.get('keydown')(event('Escape'));assert(popup.hidden);assert.equal(document.activeElement,button);
ui.toggleMenu(button);ui.menuKey(event('Tab'));assert(popup.hidden);assert.equal(document.activeElement,button);
ui.toggleMenu(button);ui.toggleMenu(button);assert(popup.hidden);
ui.menuKey(event('ArrowDown',button));assert.equal(document.activeElement,first);ui.closeMenu();

(async()=>{
 const list={innerHTML:''};nodes.set('cluster-list',list);ui.load=async()=>{};
 ui.clusters=[{idx:0,label:'<Default & "A">',default:true,url:'https://host.invalid/<script>',api_token_hint:'…masked'},
 {idx:1,label:'Other',default:false,url:'https://other.invalid',api_token_hint:'…masked'}];
 await ui.renderList();
 const rows=list.innerHTML.split('<div class="cl-row">').slice(1);
 assert.equal(rows.length,2);assert(rows[0].includes('&lt;Default &amp; &quot;A&quot;&gt;'));
 assert(!list.innerHTML.includes('<script>'));
 for(const row of rows){
  assert(row.includes('>Test</button>'));assert(row.includes('>Modifica</button>'));
  assert(row.includes('aria-haspopup="menu" aria-expanded="false"'));
  assert(row.includes('role="menu"'));assert(row.includes('hidden onkeydown='));
  const menu=row.slice(row.indexOf('<div class="cl-menu"'));
  assert(menu.includes('class="cl-delete"'));assert(!row.slice(0,row.indexOf('<div class="cl-menu"')).includes('cl-delete'));
 }
 assert(!rows[0].includes('Imposta predefinito'));assert(rows[1].includes('Imposta predefinito'));
 let confirmation;global.askConfirm=options=>confirmation=options;
 await ui.deleteCluster(1);assert.equal(confirmation.typed,'Other');assert.equal(confirmation.level,'danger');
 ui.clusters=ui.clusters.slice(0,1);await ui.renderList();assert(!list.innerHTML.includes('aria-haspopup'),'no empty menu for the sole default cluster');
})().catch(err=>{console.error(err);process.exitCode=1;});
`
	if out, err := exec.Command(node, "-e", text[start:start+end+4]+driver).CombinedOutput(); err != nil {
		t.Fatalf("cluster menu JavaScript: %v\n%s", err, out)
	}
}
