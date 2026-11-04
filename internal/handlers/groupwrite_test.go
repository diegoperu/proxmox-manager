package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
)

// fakeTags is an in-memory Proxmox for the tag writes: configs with digest, cluster resources built from them.
type fakeTags struct {
	mu      sync.Mutex
	cfg     map[int]map[string]interface{}
	kind    map[int]string
	writes  []url.Values
	putErr  error
	readErr error
	n       int
}

func newFakeTags() *fakeTags {
	f := &fakeTags{cfg: map[int]map[string]interface{}{}, kind: map[int]string{}}
	f.add(100, "qemu", map[string]interface{}{"name": "rossi-a", "tags": "cloudinit;gruppo-rossi;LTSC"})
	f.add(101, "qemu", map[string]interface{}{"name": "bianchi-x", "tags": "24.04;ubuntu-template"})
	f.add(102, "lxc", map[string]interface{}{"hostname": "ct", "tags": "Gruppo-Verdi"})
	f.add(103, "qemu", map[string]interface{}{"name": "tpl", "template": 1, "tags": "gruppo-solo"})
	f.add(104, "qemu", map[string]interface{}{"name": "busy", "lock": "backup"})
	return f
}

func (f *fakeTags) add(vmid int, kind string, c map[string]interface{}) {
	f.n++
	c["digest"] = "d" + strings.Repeat("0", f.n)
	f.cfg[vmid], f.kind[vmid] = c, kind
}

func (f *fakeTags) GetClusterResources(string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]interface{}
	for id, c := range f.cfg {
		r := map[string]interface{}{"type": f.kind[id], "node": "n1", "vmid": id, "name": c["name"], "tags": c["tags"], "template": c["template"]}
		out = append(out, r)
	}
	return json.Marshal(out)
}

func (f *fakeTags) GetGuestConfig(node, kind string, vmid int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	c, ok := f.cfg[vmid]
	if !ok || f.kind[vmid] != kind || node != "n1" {
		return nil, errors.New("proxmox 500: Configuration file 'x' does not exist")
	}
	return json.Marshal(c)
}

func (f *fakeTags) SetGuestConfig(node, kind string, vmid int, p url.Values) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return nil, f.putErr
	}
	c := f.cfg[vmid]
	if p.Get("digest") != c["digest"] {
		return nil, errors.New("proxmox 500: detected modified configuration - file changed by other user? Try again.")
	}
	f.writes = append(f.writes, p)
	if p.Get("delete") == "tags" {
		delete(c, "tags")
	} else {
		c["tags"] = p.Get("tags")
	}
	c["digest"] = c["digest"].(string) + "x"
	return json.RawMessage(`null`), nil
}

func TestWithGroupTagsKeepsOtherTags(t *testing.T) {
	cases := []struct {
		in     string
		groups []string
		want   string
	}{
		{"cloudinit;gruppo-rossi;LTSC", []string{"bianchi"}, "cloudinit;LTSC;gruppo-bianchi"},
		{"cloudinit;Gruppo-Rossi;GRUPPO-verdi", nil, "cloudinit"},
		{"", []string{"a", "b"}, "gruppo-a;gruppo-b"},
		{"gruppo-rossi", nil, ""},
		{"gruppo-altro;gruppo-ro.ssi;x", []string{"a"}, "gruppo-altro;gruppo-ro.ssi;x;gruppo-a"}, // not group tags: kept
		{"a,b c", []string{"g"}, "a;b;c;gruppo-g"},
	}
	for _, c := range cases {
		if got := withGroupTags(c.in, c.groups); got != c.want {
			t.Errorf("withGroupTags(%q,%v) = %q, want %q", c.in, c.groups, got, c.want)
		}
	}
	// a write must keep tags beyond the read limit
	many := strings.TrimSuffix(strings.Repeat("t;", 40), ";")
	if got := withGroupTags(many, []string{"g"}); strings.Count(got, ";") != 40 {
		t.Fatalf("tags dropped: %q", got)
	}
}

func TestNormalizeGroups(t *testing.T) {
	if g, err := normalizeGroups([]string{" Rossi ", "bianchi", "rossi"}); err != nil || !reflect.DeepEqual(g, []string{"bianchi", "rossi"}) {
		t.Fatal(g, err)
	}
	if g, err := normalizeGroups([]string{}); err != nil || len(g) != 0 {
		t.Fatal(g, err)
	}
	for _, bad := range [][]string{{"altro"}, {""}, {"ro ssi"}, {"a-b"}, {"gruppo-a"}, {"a;b"}, {strings.Repeat("a", 33)}, {"à"}, {"a", "b", "c", "d", "e"}} {
		if _, err := normalizeGroups(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSetVMGroupsRules(t *testing.T) {
	f := newFakeTags()
	// known group: written, other tags kept, digest sent
	res, e := setVMGroups(f, 0, "n1", "qemu", 101, []string{"rossi"}, nil)
	if e != nil || !res.Changed || f.cfg[101]["tags"] != "24.04;ubuntu-template;gruppo-rossi" || len(f.writes) != 1 || f.writes[0].Get("digest") == "" {
		t.Fatalf("%+v %v %v", res, e, f.cfg[101])
	}
	// same groups: no write
	if res, e := setVMGroups(f, 0, "n1", "qemu", 100, []string{"rossi"}, nil); e != nil || res.Changed || len(f.writes) != 1 {
		t.Fatalf("unchanged wrote: %+v %v", res, e)
	}
	// new group without confirmation: 409, no write; the template's tag does not make a group "known"
	for _, g := range []string{"nuovo", "solo"} {
		if _, e := setVMGroups(f, 0, "n1", "qemu", 100, []string{g}, nil); e == nil || e.Reason != "new_group" || len(f.writes) != 1 {
			t.Fatalf("%s: %+v", g, e)
		}
	}
	// confirmed by name: written; confirmation of another name does not count
	if _, e := setVMGroups(f, 0, "n1", "qemu", 100, []string{"nuovo"}, []string{"altronome"}); e == nil || e.Reason != "new_group" {
		t.Fatalf("%+v", e)
	}
	if res, e := setVMGroups(f, 0, "n1", "qemu", 100, []string{"nuovo"}, []string{"Nuovo"}); e != nil || !res.Changed || !reflect.DeepEqual(res.Before, []string{"rossi"}) || f.cfg[100]["tags"] != "cloudinit;LTSC;gruppo-nuovo" {
		t.Fatalf("%+v %v %v", res, e, f.cfg[100])
	}
	// container, mixed-case tag read; removing the only tag deletes the field
	if res, e := setVMGroups(f, 0, "n1", "lxc", 102, []string{}, nil); e != nil || !res.Changed || f.cfg[102]["tags"] != nil || f.writes[len(f.writes)-1].Get("delete") != "tags" {
		t.Fatalf("%+v %v %v", res, e, f.cfg[102])
	}
	w := len(f.writes)
	// template and locked VM: refused, no write
	if _, e := setVMGroups(f, 0, "n1", "qemu", 103, []string{"rossi"}, nil); e == nil || e.Reason != "template" {
		t.Fatalf("%+v", e)
	}
	if _, e := setVMGroups(f, 0, "n1", "qemu", 104, []string{"rossi"}, nil); e == nil || e.Reason != "locked" {
		t.Fatalf("%+v", e)
	}
	// wrong node / wrong kind: not found
	if _, e := setVMGroups(f, 0, "n2", "qemu", 101, []string{"rossi"}, nil); e == nil || e.Code != http.StatusNotFound {
		t.Fatalf("%+v", e)
	}
	if _, e := setVMGroups(f, 0, "n1", "lxc", 101, []string{"rossi"}, nil); e == nil || e.Code != http.StatusNotFound {
		t.Fatalf("%+v", e)
	}
	if len(f.writes) != w {
		t.Fatal("a refusal wrote")
	}
	// Proxmox errors are mapped, bodies not leaked
	f.putErr = errors.New("proxmox 403: Permission check failed (/vms/101, VM.Config.Options)")
	if _, e := setVMGroups(f, 0, "n1", "qemu", 101, []string{"bianchi"}, []string{"bianchi"}); e == nil || e.Code != http.StatusForbidden || strings.Contains(e.Msg, "/vms/101") {
		t.Fatalf("%+v", e)
	}
	f.putErr = errors.New("proxmox 500: secret internal detail")
	if _, e := setVMGroups(f, 0, "n1", "qemu", 101, []string{"bianchi"}, []string{"bianchi"}); e == nil || e.Code != http.StatusBadGateway || strings.Contains(e.Msg, "secret") {
		t.Fatalf("%+v", e)
	}
}

func TestSetVMGroupsDigestRace(t *testing.T) {
	f := newFakeTags()
	// someone changes the VM between our read and our write: the digest no longer matches
	wrapped := &raceTags{fakeTags: f}
	if _, e := setVMGroups(wrapped, 0, "n1", "qemu", 101, []string{"rossi"}, nil); e == nil || e.Reason != "changed" {
		t.Fatalf("%+v", e)
	}
	if f.cfg[101]["tags"] != "other;change" {
		t.Fatalf("the other change was overwritten: %v", f.cfg[101]["tags"])
	}
}

type raceTags struct{ *fakeTags }

func (r *raceTags) GetGuestConfig(node, kind string, vmid int) (json.RawMessage, error) {
	raw, err := r.fakeTags.GetGuestConfig(node, kind, vmid)
	r.mu.Lock()
	r.cfg[vmid]["tags"], r.cfg[vmid]["digest"] = "other;change", "changed-by-someone"
	r.mu.Unlock()
	return raw, err
}

func TestGroupsOverview(t *testing.T) {
	raw := json.RawMessage(`[
	 {"type":"qemu","node":"n1","vmid":3,"name":"rossi-b","tags":"cloudinit","status":"running"},
	 {"type":"qemu","node":"n1","vmid":1,"name":"rossi-a","tags":"gruppo-rossi","status":"running"},
	 {"type":"qemu","node":"n1","vmid":2,"name":"ubuntu-test","status":"stopped"},
	 {"type":"lxc","node":"n1","vmid":4,"name":"nodash","status":"running"},
	 {"type":"qemu","node":"n1","vmid":5,"name":"base","template":1,"tags":"gruppo-x"},
	 {"type":"qemu","node":"bad node","vmid":6,"name":"z-1"},
	 {"type":"storage","node":"n1","storage":"local"}]`)
	ov, err := buildGroupsOverview(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ov.Groups, []groupCount{{Name: "rossi", VMs: 1}}) || len(ov.VMs) != 4 || len(ov.Templates) != 1 || ov.Templates[0].VMID != 5 {
		t.Fatalf("%+v", ov)
	}
	by := map[int]groupVM{}
	for _, v := range ov.VMs {
		by[v.VMID] = v
	}
	if by[1].Source != "tag" || by[1].Proposal != "" ||
		by[3].Source != "name" || by[3].Proposal != "rossi" ||
		by[2].Source != "none" || by[2].Proposal != "ubuntu" ||
		by[4].Proposal != "" || by[4].Groups[0] != otherGroup {
		t.Fatalf("%+v", by)
	}
}

func TestSetGroupsHTTPValidation(t *testing.T) {
	r := chi.NewRouter()
	h := &Handler{}
	r.Put("/api/nodes/{node}/{kind}/{vmid}/groups", h.SetGroups)
	cases := []struct{ path, body string }{
		{"/api/nodes/n1/vm/100/groups", `{"groups":["a"]}`},
		{"/api/nodes/n1/qemu/0100/groups", `{"groups":["a"]}`},
		{"/api/nodes/n%3B1/qemu/100/groups", `{"groups":["a"]}`},
		{"/api/nodes/n1/qemu/100/groups", `{}`},
		{"/api/nodes/n1/qemu/100/groups", `{"groups":["a"],"tags":"x"}`},
		{"/api/nodes/n1/qemu/100/groups", `{"groups":["a;b"]}`},
		{"/api/nodes/n1/qemu/100/groups", `{"groups":["altro"]}`},
		{"/api/nodes/n1/qemu/100/groups", `{"groups":["a","b","c","d","e"]}`},
		{"/api/nodes/n1/qemu/100/groups", `{"groups":["a"],"confirm_new":["a","b","c","d","e"]}`},
		{"/api/nodes/n1/qemu/100/groups", `{"groups":["` + strings.Repeat("a", 5000) + `"]}`},
		{"/api/nodes/n1/qemu/100/groups", `not json`},
	}
	for _, c := range cases {
		req := httptest.NewRequest("PUT", c.path, strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %s: got %d %s", c.path, c.body, rec.Code, rec.Body.String())
		}
	}
}
