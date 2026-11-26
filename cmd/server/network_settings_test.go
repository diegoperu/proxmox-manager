package main

import (
	"encoding/json"
	"io/fs"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"proxmox-manager/internal/config"
)

func settingsJS(t *testing.T, begin, end string) string {
	t.Helper()
	page, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(page)
	if strings.Count(source, begin) != 1 {
		t.Fatalf("missing or duplicated %q", begin)
	}
	start := strings.Index(source, begin)
	finish := strings.Index(source[start+len(begin):], end)
	if finish < 0 {
		t.Fatalf("missing %q after %q", end, begin)
	}
	return source[start:start+len(begin)+finish+len(end)] + "\n"
}

func runSettingsJS(t *testing.T, source string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not in PATH")
	}
	if out, err := exec.Command(node, "-e", source).CombinedOutput(); err != nil {
		t.Fatalf("settings JavaScript: %v\n%s", err, out)
	}
}

func TestNetworkSettingsValidationParity(t *testing.T) {
	network := func(cidr, gateway string) config.NetworkSettings {
		return config.NetworkSettings{Networks: []config.VMNetwork{{CIDR: cidr, Gateway: gateway}}}
	}
	cases := []config.NetworkSettings{
		{}, network("10.75.4.0/24", "10.75.4.1"), network("10.75.4.0/24", "10.75.4.254"),
		network("10.0.0.0/8", "10.0.0.1"), network("10.0.0.0/8", "10.255.255.254"),
		network("192.0.2.0/30", "192.0.2.1"), network("192.0.2.0/30", "192.0.2.2"),
		network("192.0.2.0/30", "192.0.2.0"), network("192.0.2.0/30", "192.0.2.3"),
		network("10.75.4.0/24", "10.75.4.0"), network("10.75.4.0/24", "10.75.4.255"),
		network("10.0.0.0/8", "10.0.0.0"), network("10.0.0.0/8", "10.255.255.255"),
		network("10.75.4.0/24", "10.75.5.1"), network("10.75.4.0/24", "256.75.4.1"),
		network("10.75.4.0/24", "10.075.4.1"), network("10.75.4.0/24", "::1"),
		network("256.75.4.0/24", "10.75.4.1"), network("10.075.4.0/24", "10.75.4.1"),
		network("10.75.4.1/24", "10.75.4.254"), network("192.0.2.2/30", "192.0.2.1"),
		network("10.1.0.0/8", "10.0.0.1"), network("10.0.0.0/7", "10.0.0.1"),
		network("10.0.0.0/31", "10.0.0.1"), network("10.0.0.0/32", "10.0.0.1"),
		network("10.0.0.0/33", "10.0.0.1"), network("::/24", "10.0.0.1"),
		network("10.75.4.0/024", "::ffff:10.75.4.1"), network("10.75.4.0", "10.75.4.1"),
		network("10.75.4.0/000024", "10.75.4.1"), network("::ffff:10.75.4.0/120", "10.75.4.1"),
		network("::ffff:10.75.4.0/129", "10.75.4.1"),
	}
	for _, pairs := range [][]config.VMNetwork{
		{{CIDR: "10.0.0.0/8", Gateway: "10.0.0.1"}, {CIDR: "10.75.4.0/24", Gateway: "10.75.4.1"}},
		{{CIDR: "10.75.4.0/24", Gateway: "10.75.4.1"}, {CIDR: "10.0.0.0/8", Gateway: "10.0.0.1"}},
		{{CIDR: "10.75.4.0/24", Gateway: "10.75.4.1"}, {CIDR: "10.75.4.0/24", Gateway: "10.75.4.2"}},
		{{CIDR: "192.0.2.0/30", Gateway: "192.0.2.1"}, {CIDR: "192.0.2.4/30", Gateway: "192.0.2.5"}},
	} {
		cases = append(cases, config.NetworkSettings{Networks: pairs})
	}
	for _, dns := range [][]string{
		{"1.1.1.1", "2001:0DB8:0:0:1:0:0:1", "::ffff:192.0.2.1"},
		{"::", "::1", "2001:db8::192.0.2.1"}, {"1.1.1.1", "1.1.1.1"},
		{"2001:db8::1", "2001:0DB8:0:0:0:0:0:1"}, {"192.0.2.1", "::ffff:c000:201"},
		{"256.1.1.1"}, {"01.1.1.1"}, {"1.1.1.1/32"}, {"fe80::1%eth0"},
		{"2001::db8::1"}, {"1:2:3:4:5:6:7:8:9"}, {"1:2:3:4:5:6:7::8"},
		{"1.1.1.1", "8.8.8.8", "9.9.9.9", "4.4.4.4"},
	} {
		cases = append(cases, config.NetworkSettings{DNS: dns})
	}
	for _, domain := range []string{"", "EXAMPLE.Org", "a", "node-1.example", "-bad.example", "bad-.example", "a..b", "bad.example.", "è.it", "a_b", "a\nb", strings.Repeat("a", 64), strings.Repeat("a.", 127)} {
		cases = append(cases, config.NetworkSettings{SearchDomain: domain})
	}
	for _, profile := range []string{"", "standard", "hosting", "Hosting", "x"} {
		cases = append(cases, config.NetworkSettings{SnippetProfile: profile})
	}
	for _, mode := range []string{"", "static", "dhcp", "both", "DHCP", " static", "dynamic", "both\n", "<script>"} {
		cases = append(cases, config.NetworkSettings{IPMode: mode})
	}
	for _, from := range [][]string{nil, {"192.0.2.254"}, {"::ffff:198.51.100.7"}, {"0.0.0.0"}, {"224.0.0.1"}, {"255.255.255.255"},
		{"2001:db8::1"}, {"010.0.0.1"}, {"192.0.2.1", "192.0.2.1"}, {"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4"},
		{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5"}, {"x"}, {""}} {
		cases = append(cases, config.NetworkSettings{ManagerFrom: from})
	}
	for _, count := range []int{16, 17} {
		value := config.NetworkSettings{}
		for i := 0; i < count; i++ {
			value.Networks = append(value.Networks, config.VMNetwork{CIDR: "192.0.2." + strconv.Itoa(i*4) + "/30", Gateway: "192.0.2." + strconv.Itoa(i*4+1)})
		}
		cases = append(cases, value)
	}
	type parityCase struct {
		Input  config.NetworkSettings `json:"input"`
		Output config.NetworkSettings `json:"output"`
		Error  string                 `json:"error"`
	}
	var parity []parityCase
	for _, input := range cases {
		// Round-trip so normalizing the backend's slice cannot modify the input fixture.
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var normalized config.NetworkSettings
		if err := json.Unmarshal(raw, &normalized); err != nil {
			t.Fatal(err)
		}
		row := parityCase{Input: input}
		if err := config.ValidNetworkSettings(&normalized); err != nil {
			row.Error = err.Error()
		} else {
			row.Output = normalized
		}
		parity = append(parity, row)
	}
	data, err := json.Marshal(parity)
	if err != nil {
		t.Fatal(err)
	}
	runSettingsJS(t, settingsJS(t, "// pmgr:network:begin", "// pmgr:network:end")+`
const assert=require('node:assert/strict');
const cases=`+string(data)+`;
for(const [i,test] of cases.entries()){
 const before=JSON.stringify(test.input);
 if(test.error)assert.throws(()=>validateNetwork(test.input),e=>e.message===test.error,'case '+i+': '+test.error);
 else assert.deepEqual(validateNetwork(test.input),test.output,'case '+i);
 assert.equal(JSON.stringify(test.input),before,'pure validator');
}
const settings={networks:[{cidr:'10.75.4.0/24',gateway:'10.75.4.254'},{cidr:'192.0.2.0/30',gateway:'192.0.2.1'},{cidr:'11.0.0.0/8',gateway:'11.0.0.1'}]};
for(const [ip,index] of [['10.75.4.0',0],['10.75.4.1',0],['10.75.4.254/24',0],['10.75.4.255',0],['192.0.2.0',1],['192.0.2.1/30',1],['192.0.2.2',1],['192.0.2.3',1],['11.0.0.0',2],['11.0.0.1/8',2],['11.255.255.254',2],['11.255.255.255',2]])assert.equal(findNetwork(settings,ip),settings.networks[index],ip);
for(const ip of ['10.75.3.255','10.75.5.0','192.0.2.4','12.0.0.0','256.75.4.1','10.075.4.1','10.75.4.1/33','10.75.4.1/','10.75.4.1/24/1','::1',''])assert.equal(findNetwork(settings,ip),null,ip);
assert.equal(findNetwork(null,'10.75.4.1'),null);
assert.equal(parseCIDR('10.75.4.7/24').cidr,'10.75.4.0/24');
assert.equal(parseCIDR('255.255.255.255/8').broadcast,4294967295);
`)
}

func TestVMNetworkPrecedence(t *testing.T) {
	runSettingsJS(t, settingsJS(t, "// pmgr:network:begin", "// pmgr:network:end")+settingsJS(t, "// pmgr:vmnet:begin", "// pmgr:vmnet:end")+`
const assert=require('node:assert/strict');
const settings={networks:[{cidr:'10.75.4.0/24',gateway:'10.75.4.1'}],dns:['1.1.1.1','::1'],search_domain:'cluster.example'};
const base={settings,ip:'10.75.4.20/24',templateDns:'9.9.9.9',templateGw:'10.75.4.250',templateSearch:'template.example',label:'<Cluster & "test">'};
assert.deepEqual(chooseNetwork(base),{gateway:'10.75.4.1',dns:'1.1.1.1 ::1',search_domain:'cluster.example',warning:'',hint:''});
for(const key of ['typedGateway','typedDns','typedSearch']){
 const field={typedGateway:'gateway',typedDns:'dns',typedSearch:'search_domain'}[key];
 for(const value of ['manual',''])assert.equal(chooseNetwork({...base,[key]:value})[field],value);
}
assert.equal(chooseNetwork({...base,ip:'192.0.2.1/24'}).gateway,'','unknown network never uses template gateway');
assert.equal(chooseNetwork({...base,ip:'192.0.2.1/24',typedGateway:'192.0.2.254'}).gateway,'192.0.2.254');
assert.equal(chooseNetwork({...base,ip:'192.0.2.1/24'}).warning,'IP fuori dalle reti note di <Cluster & "test">: controlla IP e gateway');
for(const ip of ['', '256.0.0.1/24', '10.075.4.1/24'])assert.equal(chooseNetwork({...base,ip}).warning,'');
for(const settings of [null,{}, {networks:[],dns:[],search_domain:''}]){
 const r=chooseNetwork({...base,settings});assert.equal(r.gateway,base.templateGw);assert.equal(r.dns,base.templateDns);assert.equal(r.search_domain,base.templateSearch);assert.equal(r.hint,'');
}
for(const [ip,gateway] of [['10.75.4.20/24','10.75.4.254'],['192.0.2.1/30','192.0.2.2'],['10.0.0.1/8','10.255.255.254']]){
 const r=chooseNetwork({ip});assert.equal(r.gateway,gateway);assert.equal(r.dns,'');assert.equal(r.search_domain,'');assert.equal(r.warning,'');assert.equal(r.hint,'gateway indovinato: configura le reti in Impostazioni → Infrastruttura');
 assert.equal(chooseNetwork({ip,typedGateway:'manual'}).hint,'');
}
assert.deepEqual(chooseNetwork({}),{gateway:'',dns:'',search_domain:'',warning:'',hint:''});
const dhcp=chooseNetwork({...base,dhcp:true,ip:'192.0.2.1/24',typedGateway:'192.0.2.254'});
assert.deepEqual(dhcp,{gateway:'',dns:'1.1.1.1 ::1',search_domain:'cluster.example',warning:'',hint:''});
assert.equal(chooseNetwork({...base,settings:null,dhcp:true}).dns,'','DHCP never sends template DNS');
assert.equal(chooseNetwork({...base,settings:null,dhcp:true,typedDns:'8.8.8.8'}).dns,'8.8.8.8');
assert.equal(chooseNetwork({...base,dhcp:true,typedDns:''}).dns,'','explicitly cleared DNS stays empty');
assert.equal(chooseNetwork({...base,settings:{networks:[],dns:['8.8.8.8']}}).gateway,base.templateGw);
assert.equal(chooseNetwork({...base,settings:{networks:settings.networks,dns:[]}}).dns,base.templateDns);
`)
}
