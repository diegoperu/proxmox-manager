package main

import (
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

func TestAutoRefreshDecision(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	const begin = "// pmgr:autorefresh:begin"
	const end = "// pmgr:autorefresh:end"
	text := string(page)
	if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 {
		t.Fatal("autorefresh markers missing or duplicated")
	}
	start, finish := strings.Index(text, begin)+len(begin), strings.Index(text, end)
	if start >= finish {
		t.Fatal("autorefresh markers out of order")
	}
	driver := `
const assert=require('assert');
const base={page:'nodes',hidden:false,modalOpen:false,busy:false,typing:false,now:200000,busySince:100000};
for(const page of AutoRefresh.PAGES) assert(AutoRefresh.shouldRun({...base,page}));
assert.deepEqual([...AutoRefresh.PAGES].sort(),['cluster','dashboard','gpu','groups','nodes','vms']);
for(const page of ['provision','batch','balance','settings','ceph','reports','other']) assert(!AutoRefresh.shouldRun({...base,page}));
for(const field of ['hidden','modalOpen','typing','blocked','unsaved']) assert(!AutoRefresh.shouldRun({...base,[field]:true}));
assert(!AutoRefresh.shouldRun({...base,busy:true}));
assert(AutoRefresh.shouldRun({...base,busy:true,busySince:80000}));
for(const hidden of [false,true]) for(const modalOpen of [false,true]) for(const typing of [false,true]) for(const blocked of [false,true]) for(const unsaved of [false,true]) for(const busy of [false,true]) for(const expired of [false,true]) {
 const state={...base,hidden,modalOpen,typing,blocked,unsaved,busy,busySince:expired?80000:100000};
 assert.equal(AutoRefresh.shouldRun(state),!hidden&&!modalOpen&&!typing&&!blocked&&!unsaved&&(!busy||expired));
}

assert.deepEqual(AutoRefresh.CHOICES,[0,15,30,60,120,300]);
`
	cmd := exec.Command(node, "-e", text[start:finish]+driver)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("autorefresh JavaScript: %v\n%s", err, out)
	}
}

func TestAutoRefreshUnsavedSettingsForms(t *testing.T) {
	source := settingsJS(t, "// pmgr:settings-guard:begin", "// pmgr:settings-guard:end") +
		settingsJS(t, "// pmgr:autorefresh:begin", "// pmgr:autorefresh:end") +
		settingsJS(t, "const AutoRefreshUI={", "\n};")
	runSettingsJS(t, source+`
const assert=require('node:assert/strict');
let networkDirty=false,reportDirty=false;
global.NetworkSettingsUI={isDirty:()=>networkDirty};global.ReportSettingsUI={isDirty:()=>reportDirty};
global.ClusterMgr={current:0};global._modalStack=[];global.Groups={busy:false};
global.document={hidden:false,activeElement:null,getElementById:()=>null,querySelector:()=>null};
for(const network of [false,true])for(const report of [false,true]){
 networkDirty=network;reportDirty=report;
 const state=AutoRefreshUI.state('nodes');
 assert.equal(state.unsaved,network||report);
 assert.equal(AutoRefresh.shouldRun(state),!network&&!report);
}
`)
}
