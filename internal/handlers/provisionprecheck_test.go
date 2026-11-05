package handlers

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// template 9000 (on n1, 32 GB disk on ceph) with a vendor snippet that lives on the local storage of each node
func tplWithSnippet(f *fakeProv) {
	f.vms[9000].cfg["cicustom"] = "vendor=local:snippets/v.yaml"
	f.files = map[string][]string{"n1": {"local:snippets/v.yaml"}}
	f.localFiles = true
}

func reasonsOf(r adviceResult, node string) []string {
	for _, n := range r.Nodes {
		if n.Name == node {
			out := []string{}
			for _, re := range n.Reasons {
				out = append(out, re.Code+"|"+re.Detail)
			}
			return out
		}
	}
	return nil
}

func adviceT(t *testing.T, f *fakeProv, cluster int, mod func(*adviceQuery)) adviceResult {
	t.Helper()
	q := adviceQuery{Cluster: cluster, MemMB: 1024, Cores: 1, TemplateNode: "n1", TemplateVMID: 9000}
	if mod != nil {
		mod(&q)
	}
	r, err := buildAdvice(f, nil, "C", q, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAdviceSnippetMustBeOnTheNode(t *testing.T) {
	pending(t)
	f := newFakeProv()
	tplWithSnippet(f)
	r := adviceT(t, f, 51, nil)
	// n1 is the template's node: not checked. n2 lacks the file: not eligible, with the name of the file
	if got := reasonsOf(r, "n1"); len(got) != 0 {
		t.Fatalf("n1: %v", got)
	}
	if got := reasonsOf(r, "n2"); len(got) != 1 || got[0] != "file_missing|local:snippets/v.yaml" {
		t.Fatalf("n2: %v", got)
	}
	if r.Recommended != "n1" {
		t.Fatalf("recommended %q", r.Recommended)
	}
	// the file exists on n2 as well (another cluster index: the listing is cached per cluster, node and storage)
	f.files["n2"] = []string{"local:snippets/v.yaml"}
	if got := reasonsOf(adviceT(t, f, 52, nil), "n2"); len(got) != 0 {
		t.Fatalf("n2 with the file: %v", got)
	}
}

func TestAdviceSharedStorageNeedsNoFileCheck(t *testing.T) {
	pending(t)
	f := newFakeProv()
	f.vms[9000].cfg["cicustom"] = "vendor=ceph:snippets/v.yaml" // ceph is shared: the file is the same everywhere
	if got := reasonsOf(adviceT(t, f, 53, nil), "n2"); len(got) != 0 {
		t.Fatalf("a shared storage was checked: %v", got)
	}
}

func TestAdviceUnreadableListingBlocksNothingAndSaysSo(t *testing.T) {
	pending(t)
	f := newFakeProv()
	tplWithSnippet(f)
	f.filesErr = true
	r := adviceT(t, f, 54, nil)
	if got := reasonsOf(r, "n2"); len(got) != 0 {
		t.Fatalf("an unreadable listing blocked the node: %v", got)
	}
	if !containsStr(r.Meta.Partial, "files") {
		t.Fatalf("partial %v", r.Meta.Partial)
	}
}

func TestAdviceIsoOfTheTemplateIsAFileToo(t *testing.T) {
	pending(t)
	f := newFakeProv()
	f.vms[9000].cfg["ide0"] = "local:iso/win.iso,media=cdrom,size=4G"
	f.files = map[string][]string{"n1": {"local:iso/win.iso"}}
	f.localFiles = true
	r := adviceT(t, f, 55, nil)
	if got := reasonsOf(r, "n2"); len(got) != 1 || got[0] != "file_missing|local:iso/win.iso" {
		t.Fatalf("%v", got)
	}
	// and it is NOT a disk: no "storage_not_shared" for the local storage that holds the ISO
	for _, g := range reasonsOf(r, "n2") {
		if strings.HasPrefix(g, "storage_not_shared") {
			t.Fatalf("an ISO was treated as a disk: %v", g)
		}
	}
}

func TestAdviceCloneNeedsSpaceOnItsStorage(t *testing.T) {
	pending(t)
	f := newFakeProv()
	// ceph: 1000 GB, 100 used = 900 free. A 32 GB disk fits; asking for 950 GB does not (the clone is the whole disk)
	if got := reasonsOf(adviceT(t, f, 56, nil), "n2"); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	r := adviceT(t, f, 57, func(q *adviceQuery) { q.DiskGB = 950 })
	var missing int64
	for _, re := range r.Nodes[0].Reasons {
		if re.Code == adviceCloneNoSpace {
			missing = re.Missing
		}
	}
	if got := reasonsOf(r, "n1"); len(got) == 0 || !strings.HasPrefix(got[0], "clone_no_space|ceph") {
		t.Fatalf("n1: %v", got)
	}
	// 950 GB + the 1 MB EFI disk + the 4 MB cloud-init drive - 900 GB free
	want := int64(950)<<30 + 1<<20 + cloudInitDriveBytes - int64(900)<<30
	for _, n := range r.Nodes {
		if n.Name == "n1" {
			for _, re := range n.Reasons {
				missing = re.Missing
			}
		}
	}
	if missing != want {
		t.Fatalf("missing %d, want %d", missing, want)
	}
	// the extra disk on the SAME storage adds to the clone (900 GB free): 860 + 32 fits, 880 + 32 does not
	if got := reasonsOf(adviceT(t, f, 58, func(q *adviceQuery) { q.DiskGB = 32; q.Disk2GB = 860 }), "n1"); len(got) != 0 {
		t.Fatalf("860 GB next to the 32 GB clone fits in 900 GB: %v", got)
	}
	r = adviceT(t, f, 59, func(q *adviceQuery) { q.DiskGB = 32; q.Disk2GB = 880 })
	if got := reasonsOf(r, "n1"); len(got) == 0 || !strings.HasPrefix(got[0], "clone_no_space|ceph") {
		t.Fatalf("clone + extra disk on the same storage: %v", got)
	}
}

func TestAdviceExtraDiskOnAnotherStorageDoesNotChargeTheClone(t *testing.T) {
	pending(t)
	f := newFakeProv()
	// 'fast' on n2 has 5 GB free: a 10 GB extra disk there misses 5 GB, with disk2_no_space (not clone_no_space)
	r := adviceT(t, f, 60, func(q *adviceQuery) { q.Disk2GB = 10; q.Disk2Storage = "fast" })
	got := reasonsOf(r, "n2")
	if len(got) != 1 || got[0] != "disk2_no_space|fast" {
		t.Fatalf("%v", got)
	}
}

func TestProvisionRefusesBeforeTheCloneWhenTheNodeLacksTheSnippetOrTheSpace(t *testing.T) {
	provFast(t)
	pending(t)
	f := newFakeProv()
	tplWithSnippet(f)
	_, err := startProv(f, 61, "C", mustPlanProv(t, baseReq()), noOpts, time.Now()) // target n2: no snippet there
	ref := asRefusal(t, err)
	if ref.Reason != "node_not_eligible" || !containsStr(ref.Reasons, adviceFileMissing) {
		t.Fatalf("%+v", ref)
	}
	if w := f.log(); w != "" {
		t.Fatalf("written:\n%s", w)
	}
	// no space: 2000 GB of disk on a 900 GB free storage
	f = newFakeProv()
	r := baseReq()
	r.DiskGB = 2000
	_, err = startProv(f, 62, "C", mustPlanProv(t, r), noOpts, time.Now())
	ref = asRefusal(t, err)
	if ref.Reason != "node_not_eligible" || !containsStr(ref.Reasons, adviceCloneNoSpace) {
		t.Fatalf("%+v", ref)
	}
	if w := f.log(); w != "" {
		t.Fatalf("written:\n%s", w)
	}
	// with the file on the node the creation goes through
	f = newFakeProv()
	tplWithSnippet(f)
	f.files["n2"] = []string{"local:snippets/v.yaml"}
	j, err := startProv(f, 63, "C", mustPlanProv(t, baseReq()), noOpts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v := waitProv(t, j); v.Result != provOK {
		t.Fatalf("%+v", v)
	}
}

func TestParseAdviceTemplateParams(t *testing.T) {
	good := []string{"memory_mb", "4096", "cores", "2"}
	q, err := parseAdviceQuery(query(append(good, "template_vmid", "1003", "disk_device", "virtio0", "disk_gb", "100")...))
	if err != nil || q.TemplateVMID != 1003 || q.DiskDevice != "virtio0" || q.DiskGB != 100 {
		t.Fatalf("%v %+v", err, q)
	}
	for name, kv := range map[string][]string{
		"vmid zero":   append(good, "template_vmid", "0"),
		"vmid text":   append(good, "template_vmid", "1003x"),
		"device":      append(good, "disk_device", "nvme0"),
		"device path": append(good, "disk_device", "../x"),
		"disk huge":   append(good, "disk_gb", "70000"),
		"disk zero":   append(good, "disk_gb", "0"),
	} {
		if _, err := parseAdviceQuery(query(kv...)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	a, _ := parseAdviceQuery(query(append(good, "template_vmid", "1")...))
	b, _ := parseAdviceQuery(query(append(good, "template_vmid", "2")...))
	if a.key() == b.key() {
		t.Fatal("the cache key ignores the template")
	}
}

func TestAdviceWithANonTemplateOrUnreadableTemplateSaysSo(t *testing.T) {
	pending(t)
	f := newFakeProv()
	r := adviceT(t, f, 64, func(q *adviceQuery) { q.TemplateVMID = 51 }) // an ordinary VM, not a template
	if !containsStr(r.Meta.Partial, "template") {
		t.Fatalf("partial %v", r.Meta.Partial)
	}
	r = adviceT(t, f, 65, func(q *adviceQuery) { q.TemplateVMID = 777777 })
	if !containsStr(r.Meta.Partial, "template") || r.Recommended == "" {
		t.Fatalf("%v %q", r.Meta.Partial, r.Recommended)
	}
}

func asRefusal(t *testing.T, err error) *provRefusal {
	t.Helper()
	var ref *provRefusal
	if !errorsAs(err, &ref) {
		t.Fatalf("not a refusal: %v", err)
	}
	return ref
}

func errorsAs(err error, target interface{}) bool { return errors.As(err, target) }

// the real cluster: "local" has iso,backup,vztmpl and NOT snippets, so its listing of snippets is empty everywhere even
// where the files exist. Nothing may be blocked, and the answer says the check was not possible.
func TestAdviceSnippetsOnAStorageWithoutThatContentTypeAreNotVerifiable(t *testing.T) {
	pending(t)
	f := newFakeProv()
	f.vms[9000].cfg["cicustom"] = "vendor=local:snippets/v.yaml"
	f.localFiles = true
	f.files = map[string][]string{} // empty listing
	// the storage declares only iso: take snippets away from its content
	r0 := adviceT(t, f, 66, nil)
	if got := reasonsOf(r0, "n2"); len(got) != 1 {
		t.Fatalf("with snippets enabled and an empty listing the file is missing: %v", got)
	}
	f = newFakeProv()
	f.vms[9000].cfg["cicustom"] = "vendor=nosnip:snippets/v.yaml"
	f.noSnippetKind = true
	r := adviceT(t, f, 67, nil)
	if got := reasonsOf(r, "n2"); len(got) != 0 {
		t.Fatalf("an unverifiable file blocked the node: %v", got)
	}
	if !containsStr(r.Meta.Partial, "files_unverified") {
		t.Fatalf("partial %v", r.Meta.Partial)
	}
}

func TestAdviceFileOnAStorageThatIsNotOnTheNodeIsMissing(t *testing.T) {
	pending(t)
	f := newFakeProv()
	f.vms[9000].cfg["cicustom"] = "vendor=onlyn1:snippets/v.yaml"
	f.onlyN1Storage = true // storage "onlyn1" exists on n1 only
	r := adviceT(t, f, 68, nil)
	if got := reasonsOf(r, "n2"); len(got) != 1 || got[0] != "file_missing|onlyn1:snippets/v.yaml" {
		t.Fatalf("%v", got)
	}
}
