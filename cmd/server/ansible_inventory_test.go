package main

import (
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

func TestAnsibleInventory(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	const begin = "// pmgr:ansible-inventory:begin"
	const end = "// pmgr:ansible-inventory:end"
	text := string(page)
	if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 {
		t.Fatal("inventory markers missing or duplicated")
	}
	start, finish := strings.Index(text, begin)+len(begin), strings.Index(text, end)
	if start >= finish {
		t.Fatal("inventory markers out of order")
	}
	driver := `
const assert = require('assert');
const labels=[{idx:0,label:'Calcolo'},{idx:1,label:'calcolo!'}];
assert.equal(AnsibleInv.slug(' -- Hello World! '),'hello-world');
assert.equal(AnsibleInv.slug('x'.repeat(45)),'x'.repeat(40));
assert.equal(AnsibleInv.fileName('Calcolo',0,labels),'inventory-calcolo-0.yml');
assert.equal(AnsibleInv.fileName('calcolo!',1,labels),'inventory-calcolo-1.yml');
assert.equal(AnsibleInv.fileName('!!!',2,[{idx:2,label:'!!!'}]),'inventory-cluster2.yml');
const vm=(vmid,name,extra={})=>({vmid,name,node:'node-1',type:'qemu',status:'running',ip:'10.0.0.1',grp:{source:'tag',groups:['z','a']},...extra});
const data=[vm(2,'dup'),vm(1,'dup',{type:'lxc',grp:{source:'name',groups:['a','bad-group','UPPER','x'.repeat(33)]}}),vm(3,'a\nb: c'),vm(4,'x"y'),vm(5,"{{ lookup('pipe','id') }}"),vm(6,'é'),vm(7,''),vm(8,'missing',{ip:''}),vm(9,'invalid',{ip:'10.0.0.256'}),vm(10,'space',{ip:' 10.0.0.1'}),vm(11,'cidr',{ip:'10.0.0.1/24'}),vm(12,'badnode',{node:'node\nfoo'}),vm(13,'badid',{vmid:0}),vm(14,'off',{status:'stopped'}),vm(15,'template',{template:1}),vm(16,'other-template')];
const opts={label:'Safe\nInjected: evil\u0000',idx:0,slug:'calcolo-0',now:new Date('2026-01-02T03:04:05Z')};
const result=AnsibleInv.build(data,opts);
assert.equal(result.exported,7); assert.equal(result.skipped,6);
assert.equal(result.yaml,AnsibleInv.build([...data].reverse(),opts).yaml);
const lines=result.yaml.split('\n');
assert.deepEqual(lines.slice(0,5),['# Inventory Ansible generato da ProxmoxManager','# Cluster: SafeInjected: evil','# Solo VM e container in esecuzione, generato il 2026-01-02T03:04:05.000Z','all:','  hosts:']);
assert.deepEqual(lines.filter(s=>/^    ".*":$/.test(s)),['    "_":','    "___lookup__pipe___id_____":','    "a_b__c":','    "dup-1":','    "dup-2":','    "vm7":','    "x_y":']);
assert(lines.includes('      pmgr_type: lxc'));
assert(lines.includes('      pmgr_node: "node-1"'));
assert(lines.includes('      pmgr_cluster: "calcolo-0"'));
assert(lines.includes('    cluster_calcolo_0:'));
assert(lines.includes('    gruppo_a:')); assert(lines.includes('    gruppo_z:'));
assert(!lines.some(s=>s.includes('bad-group')||s.includes('UPPER')||s.includes('Injected: evil:')));
assert(lines.indexOf('    gruppo_a:')<lines.indexOf('    gruppo_z:'));
const empty=AnsibleInv.build([vm(1,'x',{ip:'1.2.3'})],opts);
assert.equal(empty.exported,0); assert.equal(empty.skipped,1); assert(!empty.yaml.includes('  children:'));
const collision=AnsibleInv.build([vm(1,'a'),vm(2,'a'),vm(3,'a-1')],opts);
assert.equal(new Set(collision.yaml.split('\n').filter(s=>/^    ".*":$/.test(s))).size,3);
`
	cmd := exec.Command(node, "-e", text[start:finish]+driver)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inventory JavaScript: %v\n%s", err, out)
	}
}
