package main

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"proxmox-manager/internal/auth"
)

func TestRoleVisibility(t *testing.T) {
	source := authUISource(t, "// pmgr:auth-ui:begin", "const Auth={")
	runAuthUI(t, source+`
const assert=require('node:assert/strict');
const classes=new Set();
global.document={documentElement:{classList:{toggle(name,on){on?classes.add(name):classes.delete(name);}}}};
global.Auth={role:'reader'};
const roles=['reader','operator','admin'];
const table=[[true,false,false],[true,true,false],[true,true,true]];
for(let user=0;user<3;user++){
 Auth.role=roles[user];RoleUI.sync(Auth.role);
 assert.deepEqual([...classes],['role-'+Auth.role]);
 for(let control=0;control<3;control++){
  assert.equal(canDo(Auth.role,roles[control]),table[user][control],Auth.role+'/'+roles[control]);
  assert.equal(roleAttrs(roles[control]),'data-min-role="'+roles[control]+'"');
 }
 for(const page of ['dashboard','nodes','vms','gpu','groups','ceph','balance','cluster','reports','settings'])assert(RoleUI.canPage(page));
 for(const page of ['provision','batch'])assert.equal(RoleUI.canPage(page),user>=1);
 assert.equal(RoleUI.canPage('users'),user===2);
 const fields={disabled:false};RoleUI.readonly({querySelector:()=>fields});assert.equal(fields.disabled,user!==2);
}
for(const role of ['',null,'owner','toString']){
 assert(!canDo(role,'operator'));assert(!canDo('admin',role));assert.throws(()=>roleAttrs(role));
}
RoleUI.sync('');assert.equal(classes.size,0);
const control={setAttribute(k,v){this[k]=v;}};RoleUI.mark(control,'operator');assert.equal(control['data-min-role'],'operator');
`)
}

// Best effort over the known UI entry points. The minimum comes from routeAccess, not a second role table.
// Match quoted attributes too: > can occur inside a template expression in an attribute.
func TestWriteControlsDeclareRole(t *testing.T) {
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(page)
	tags := regexp.MustCompile(`(?s)<(?:button|input|select|form)\b(?:[^>"']|"[^"]*"|'[^']*')*>`).FindAllString(source, -1)
	cases := []struct {
		route   string
		entries []string
	}{
		{"POST /api/nodes/{node}/qemu/{vmid}/action/{action}", []string{"VMs.action"}},
		{"POST /api/nodes/{node}/lxc/{vmid}/action/{action}", []string{"VMs.action"}},
		{"POST /api/nodes/{node}/qemu/{vmid}/snapshot", []string{"VMs.snapshot"}},
		{"POST /api/nodes/{node}/qemu/{vmid}/dmesg", []string{"Dmesg.load", "Dmesg.setLines", "Dmesg.setLevel"}},
		{"POST /api/nodes/{node}/qemu/{vmid}/adduser", []string{"VMs.addUser", "VMs.submitAddUser"}},
		{"POST /api/nodes/{node}/qemu/{vmid}/adddisk", []string{"DiskMgr.open", "DiskMgr.addDisk"}},
		{"POST /api/nodes/{node}/qemu/{vmid}/setup-disk", []string{"DiskMgr.setupDisk"}},
		{"POST /api/nodes/{node}/qemu/{vmid}/termproxy", []string{"Console.open"}},
		{"POST /api/batch", []string{"Batch.run", "VMs.toBatch"}},
		{"PUT /api/nodes/{node}/{kind}/{vmid}/groups", []string{"Groups.apply", "Groups.save", "Groups.saveInline", "Groups.inlEdit"}},
		{"POST /api/provision/job", []string{"Provision.run"}},
		{"POST /api/update-check", []string{"UpdateUI.check"}},
		{"POST /api/balance/step", []string{"Balance.exec"}},
		{"POST /api/balance/job/decide", []string{"Balance.decide", "Balance.askForce"}},
		{"POST /api/nodes/{node}/qemu/{vmid}/gpu", []string{"Gpu.assign", "Gpu.next"}},
		{"DELETE /api/nodes/{node}/qemu/{vmid}/gpu/{slot}", []string{"Gpu.unassign"}},
		{"PUT /api/gpu-models", []string{"Gpu.modelEdit", "Gpu.modelSave"}},
		{"POST /api/nodes/{node}/cmd/{cmd}", []string{"Cluster.run"}},
		{"POST /api/config", []string{"Settings.pickTheme", "Settings.pickDensity", "AutoRefreshUI.save"}},
		{"POST /api/clusters", []string{"ClusterMgr.showAdd"}},
		{"PUT /api/clusters/{idx}", []string{"ClusterMgr.showEdit", "ClusterMgr.saveForm"}},
		{"DELETE /api/clusters/{idx}", []string{"ClusterMgr.deleteCluster"}},
		{"POST /api/clusters/{idx}/test", []string{"ClusterMgr.testCluster"}},
		{"PUT /api/clusters/{idx}/default", []string{"ClusterMgr.setDefault"}},
		{"POST /api/users", []string{"Users.newUser", "Users.create"}},
		{"PUT /api/users/{name}", []string{"Users.changeRole", "Users.setDisabled"}},
		{"POST /api/users/{name}/reset", []string{"Users.reset"}},
	}
	for _, tc := range cases {
		rule, ok := routeAccess[tc.route]
		if !ok || rule.role < auth.RoleOperator {
			t.Fatalf("missing write rule %q", tc.route)
		}
		role := rule.role.String()
		for _, entry := range tc.entries {
			found := false
			for _, tag := range tags {
				if !strings.Contains(tag, entry+"(") {
					continue
				}
				found = true
				if !strings.Contains(tag, `data-min-role="`+role+`"`) && !strings.Contains(tag, `${roleAttrs('`+role+`')}`) {
					t.Errorf("%s (%s): role %s missing on %s", entry, tc.route, role, tag)
				}
			}
			if !found {
				t.Errorf("write entry no longer found: %s", entry)
			}
		}
	}
	for _, control := range []struct{ route, selector string }{
		{"PUT /api/clusters/{idx}/network-settings", `id="ns-save"`},
		{"PUT /api/clusters/{idx}/report-settings", `id="rs-save"`},
		{"POST /api/clusters/{idx}/report-settings/price-check", "data-price-check"},
	} {
		rule, ok := routeAccess[control.route]
		if !ok || rule.role < auth.RoleOperator {
			t.Fatalf("missing settings rule %s", control.route)
		}
		found := false
		for _, tag := range tags {
			if strings.Contains(tag, control.selector) {
				found = true
				if !strings.Contains(tag, `data-min-role="`+rule.role.String()+`"`) {
					t.Errorf("%s: missing role on %s", control.route, tag)
				}
			}
		}
		if !found {
			t.Errorf("missing settings control %s", control.selector)
		}
	}
	for _, fragment := range []string{
		`html:not(.role-operator):not(.role-admin) [data-min-role="operator"]`,
		`html:not(.role-admin) [data-min-role="admin"]`,
		`data-page="users" data-min-role="admin"`,
		`RoleUI.mark(b,'operator');b.onclick=()=>Dmesg.load()`,
		`RoleUI.mark(b,'admin');b.onclick=action.run`,
		`RoleUI.mark(btn,minRole)`,
		`data-min-role="admin" type="button" class="btn ba sm" data-price-check`,
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("missing role visibility contract: %s", fragment)
		}
	}
	// The direct delete/migrate API routes have no UI entry in the baseline. If one is added,
	// require it to carry admin even before it is included in the explicit entry table above.
	for _, tag := range tags {
		if regexp.MustCompile(`VMs\.(?:delete|remove|destroy|migrate)\(`).MatchString(tag) && !strings.Contains(tag, `data-min-role="admin"`) && !strings.Contains(tag, `${roleAttrs('admin')}`) {
			t.Errorf("admin guest control without role: %s", tag)
		}
	}
}

func TestRoleNavigation(t *testing.T) {
	source := authUISource(t, "// pmgr:auth-ui:begin", "const Auth={") +
		authUISource(t, "const PMETA=", "// pmgr:autorefresh:begin") +
		authUISource(t, "const App={", "// DMESG:") +
		authUISource(t, "const Palette={", "// DASHBOARD")
	runAuthUI(t, `
const assert=require('node:assert/strict');
global.Auth={role:'reader',locked:false};global._IS_CONSOLE=false;
global.window={addEventListener(){}};
global.location={hash:'#/users'};
global.history={replaceState(a,b,h){location.hash=h;},pushState(a,b,h){location.hash=h;}};
global.SettingsGuard={dirtyForm:()=>null};global.Settings={tab:'general',showTab(){},hash:()=>"#/settings"};
global.Ceph={stop(){},isAvail:()=>true};global.Gpu={isAvail:()=>true};
global.ClusterMgr={clusters:[]};global.AutoRefreshUI={reset(){}};
const controls=new Map();
const get=id=>{if(!controls.has(id))controls.set(id,{textContent:'',classList:{toggle(){},contains:()=>false},setAttribute(){},removeAttribute(){}});return controls.get(id);};
global.document={getElementById:get,querySelectorAll:()=>[]};
global.closeModal=()=>{};
`+source+`
App.refresh=()=>{};
assert.deepEqual(Route.parse('#/users'),{page:'users'});assert.equal(Route.parse('#/unknown'),null);
for(const role of ['reader','operator','admin']){
 Auth.role=role;location.hash='#/users';Route.apply();
 assert.equal(App.cur,role==='admin'?'users':'dashboard');assert.equal(location.hash,'#/'+App.cur);
 // Read pages remain accessible, including settings tabs and simulations.
 for(const page of ['vms','groups','balance','cluster','settings','gpu','reports']){App.nav(page);assert.equal(App.cur,page);}
 // Direct read access is retained for the two workflows whose navigation is hidden from readers.
 for(const page of ['provision','batch']){App.nav(page);assert.equal(App.cur,page);}
 const pages=Palette.base().filter(it=>it.group==='Pagine');
 for(const label of ['Nuova VM','Batch'])assert.equal(pages.some(it=>it.label===label),role!=='reader');
 assert.equal(pages.some(it=>it.label==='Utenti'),role==='admin');
}
let ran=false;Auth.role='reader';Palette.shown=[{minRole:'admin',run(){ran=true;}}];Palette.run(0);assert(!ran);
Auth.role='admin';Palette.run(0);assert(ran);
`)
}
