package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"proxmox-manager/internal/api"
)

// Group tags: write side (D35-D42, 2026-11-04).
//
// The app writes only the `tags` field of a VM/container config, and inside it only the `gruppo-<name>` tags:
// every other tag (cloudinit, 24.04, LTSC...) is kept exactly as it is, in its order. The PUT carries the config
// digest (Proxmox refuses if someone changed the VM in between). Templates are refused: a clone copies the tags
// of its source, so a group on a template would end up on every VM made from it. A group that does not exist
// yet as a tag on any VM is written only if the client confirms it by name (D38 A: stops typos).

const (
	maxGroupsPerVM  = 4    // more than this is a mistake, not a shared VM
	maxTagsLen      = 1024 // the whole tags string after the change
	groupBodyLimit  = 4096
	groupLogMaxText = 160
)

type groupWriteAPI interface {
	GetClusterResources(resType string) (json.RawMessage, error)
	GetGuestConfig(node, kind string, vmid int) (json.RawMessage, error)
	SetGuestConfig(node, kind string, vmid int, params url.Values) (json.RawMessage, error)
}

var _ groupWriteAPI = (*api.Client)(nil)

// normalizeGroups validates a requested group list: [a-z0-9]{1,32} each (lower-cased), not "altro", at most
// maxGroupsPerVM, duplicates removed, sorted. An empty list means "no group".
func normalizeGroups(in []string) ([]string, error) {
	if len(in) > maxGroupsPerVM {
		return nil, fmt.Errorf("al massimo %d gruppi per VM", maxGroupsPerVM)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, g := range in {
		g = strings.ToLower(strings.TrimSpace(g))
		if !groupPattern.MatchString(g) || g == otherGroup {
			return nil, errors.New("nome di gruppo non valido: solo lettere minuscole e cifre, da 1 a 32 caratteri")
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out, nil
}

// withGroupTags returns the tag string with the group tags replaced by `groups` (already normalized). Non-group
// tags keep their text and order; group tags are appended in alphabetical order.
func withGroupTags(tags string, groups []string) string {
	var out []string
	for _, t := range parseTagsAll(tags) {
		if _, ok := groupFromTag(t); ok {
			continue
		}
		out = append(out, t)
	}
	for _, g := range groups {
		out = append(out, tagGroupPrefix+g)
	}
	return strings.Join(out, ";")
}

// parseTagsAll splits like parseTags but without the read limit: a write must never drop tags it did not read.
func parseTagsAll(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '\t' })
}

func sameGroups(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// knownGroups reads, fresh, the groups that exist as tags on non-template VMs of the cluster.
func knownGroups(c groupWriteAPI) (map[string]int, error) {
	raw, err := c.GetClusterResources("vm")
	if err != nil {
		return nil, err
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range list {
		if isTemplateRes(r) {
			continue
		}
		ts, _ := r["tags"].(string)
		for _, g := range tagGroups(ts) {
			out[g]++
		}
	}
	return out, nil
}

// GroupChange is the result of a write.
type GroupChange struct {
	Node    string   `json:"node"`
	Kind    string   `json:"kind"`
	VMID    int      `json:"vmid"`
	Before  []string `json:"before"`
	Groups  []string `json:"groups"`
	Changed bool     `json:"changed"`
}

// setVMGroups replaces the group tags of one VM/container. groups must be normalized; confirmNew lists the new
// groups the user confirmed by name.
func setVMGroups(c groupWriteAPI, cluster int, node, kind string, vmid int, groups, confirmNew []string) (GroupChange, *gpuErr) {
	res := GroupChange{Node: node, Kind: kind, VMID: vmid, Groups: groups, Before: []string{}}
	raw, err := c.GetGuestConfig(node, kind, vmid)
	if err != nil {
		return res, groupRefusal(err, "configurazione della VM non leggibile")
	}
	var cfg map[string]interface{}
	if json.Unmarshal(raw, &cfg) != nil {
		return res, refuse(http.StatusBadGateway, "unknown", "configurazione della VM non leggibile")
	}
	digest, _ := cfg["digest"].(string)
	if digest == "" || len(digest) > 128 {
		return res, refuse(http.StatusBadGateway, "unknown", "configurazione della VM senza digest")
	}
	if parseBoolish(cfg["template"]) {
		return res, refuse(http.StatusConflict, "template", "È un template: il gruppo finirebbe in ogni VM clonata da lì")
	}
	if lock, _ := cfg["lock"].(string); lock != "" {
		return res, refuse(http.StatusConflict, "locked", fmt.Sprintf("La VM è bloccata (%s): riprova a operazione finita", clampText(lock, 32)))
	}
	oldTags, _ := cfg["tags"].(string)
	if before := tagGroups(oldTags); before != nil {
		res.Before = before
	}
	if sameGroups(res.Before, groups) {
		return res, nil
	}
	if len(groups) > 0 {
		known, err := knownGroups(c)
		if err != nil {
			return res, groupRefusal(err, "elenco dei gruppi non leggibile")
		}
		confirmed := map[string]bool{}
		for _, g := range confirmNew {
			confirmed[strings.ToLower(strings.TrimSpace(g))] = true
		}
		var missing []string
		for _, g := range groups {
			if known[g] == 0 && !confirmed[g] {
				missing = append(missing, g)
			}
		}
		if len(missing) > 0 {
			return res, refuse(http.StatusConflict, "new_group", "Gruppo nuovo, va confermato: "+strings.Join(missing, ", "))
		}
	}
	newTags := withGroupTags(oldTags, groups)
	if len(newTags) > maxTagsLen {
		return res, refuse(http.StatusBadRequest, "invalid", "troppi tag sulla VM")
	}
	params := url.Values{}
	if newTags == "" {
		params.Set("delete", "tags")
	} else {
		params.Set("tags", newTags)
	}
	params.Set("digest", digest)
	if _, err := c.SetGuestConfig(node, kind, vmid, params); err != nil {
		return res, groupRefusal(err, "scrittura dei tag non riuscita")
	}
	res.Changed = true
	log.Printf("group set: cluster=%d node=%s %s=%d groups %s -> %s", cluster, node, kind, vmid,
		clampText(strings.Join(res.Before, ","), groupLogMaxText), clampText(strings.Join(groups, ","), groupLogMaxText))
	return res, nil
}

// groupRefusal maps a Proxmox error without leaking its body to the browser.
func groupRefusal(err error, what string) *gpuErr {
	msg := err.Error()
	switch {
	case isForbidden(err):
		return refuse(http.StatusForbidden, "forbidden", "Permessi Proxmox insufficienti (serve VM.Config.Options sulla VM)")
	case strings.Contains(msg, "does not exist"):
		return refuse(http.StatusNotFound, "not_found", "VM non trovata su questo nodo")
	case strings.Contains(msg, "digest") || strings.Contains(msg, "modified"):
		return refuse(http.StatusConflict, "changed", "La VM è stata modificata nel frattempo: ricarica e riprova")
	}
	log.Printf("group: %s: %v", what, err)
	return refuse(http.StatusBadGateway, "proxmox", what)
}

// ── read: page "Gruppi" ─────────────────────────────────────────────────────

type groupVM struct {
	Node     string   `json:"node"`
	VMID     int      `json:"vmid"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Groups   []string `json:"groups"`
	Source   string   `json:"source"`   // tag | name | none
	Proposal string   `json:"proposal"` // name prefix (D39 B), "" when the VM already has a group tag or no prefix
}

type groupCount struct {
	Name string `json:"name"`
	VMs  int    `json:"vms"`
}

type groupsOverview struct {
	Groups []groupCount `json:"groups"` // existing group tags
	VMs    []groupVM    `json:"vms"`
	// Templates with a group tag: every clone inherits it (to be fixed in Proxmox or with the app's "remove").
	Templates []groupVM `json:"templates_with_group"`
}

// buildGroupsOverview is pure.
func buildGroupsOverview(raw json.RawMessage) (groupsOverview, error) {
	out := groupsOverview{Groups: []groupCount{}, VMs: []groupVM{}, Templates: []groupVM{}}
	var list []map[string]interface{}
	if err := json.Unmarshal(raw, &list); err != nil {
		return out, err
	}
	var tagStrs []string
	for _, r := range list {
		if typ, _ := r["type"].(string); (typ == "qemu" || typ == "lxc") && !isTemplateRes(r) {
			ts, _ := r["tags"].(string)
			tagStrs = append(tagStrs, ts)
		}
	}
	rs := newGroupResolver(tagStrs)
	counts := map[string]int{}
	for _, r := range list {
		typ, _ := r["type"].(string)
		node, _ := r["node"].(string)
		vmid := int(intField(r, "vmid"))
		if (typ != "qemu" && typ != "lxc") || !nodeNamePattern.MatchString(node) || vmid <= 0 {
			continue
		}
		name, _ := r["name"].(string)
		ts, _ := r["tags"].(string)
		status, _ := r["status"].(string)
		row := groupVM{Node: node, VMID: vmid, Kind: typ, Name: clampText(name, 64), Status: clampText(status, 16)}
		if isTemplateRes(r) {
			if g := tagGroups(ts); len(g) > 0 {
				row.Groups, row.Source = g, groupSrcTag
				out.Templates = append(out.Templates, row)
			}
			continue
		}
		as := rs.resolve(name, ts)
		row.Groups, row.Source = as.Groups, as.Source
		if as.Source == groupSrcTag {
			for _, g := range as.Groups {
				counts[g]++
			}
		} else if p := vmGroup(name); p != otherGroup {
			row.Proposal = p
		}
		out.VMs = append(out.VMs, row)
	}
	for g, n := range counts {
		out.Groups = append(out.Groups, groupCount{Name: g, VMs: n})
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Name < out.Groups[j].Name })
	sort.Slice(out.VMs, func(i, j int) bool { return out.VMs[i].VMID < out.VMs[j].VMID })
	sort.Slice(out.Templates, func(i, j int) bool { return out.Templates[i].VMID < out.Templates[j].VMID })
	return out, nil
}

// GetGroups: GET /api/groups — existing groups, every VM with its group and a proposal from the name. Fresh read.
func (h *Handler) GetGroups(w http.ResponseWriter, r *http.Request) {
	client, err := h.getClientFor(clusterIdx(r))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	raw, err := client.GetClusterResources("vm")
	if err != nil {
		writeGPUErr(w, groupRefusal(err, "elenco delle VM non leggibile"))
		return
	}
	ov, err := buildGroupsOverview(raw)
	if err != nil {
		writeGPUErr(w, refuse(http.StatusBadGateway, "proxmox", "elenco delle VM non leggibile"))
		return
	}
	writeJSON(w, ov)
}

// SetGroups: PUT /api/nodes/{node}/{kind}/{vmid}/groups {"groups":["rossi"],"confirm_new":["rossi"]}.
// An empty list removes the group tags. One VM per request: the page applies a list one VM at a time and shows
// the outcome of each.
func (h *Handler) SetGroups(w http.ResponseWriter, r *http.Request) {
	node, kind, vmidS := chi.URLParam(r, "node"), chi.URLParam(r, "kind"), chi.URLParam(r, "vmid")
	if !nodeNamePattern.MatchString(node) || (kind != "qemu" && kind != "lxc") || !vmidPattern.MatchString(vmidS) {
		writeGPUErr(w, refuse(http.StatusBadRequest, "invalid", "nodo, tipo o VMID non validi"))
		return
	}
	vmid, _ := strconv.Atoi(vmidS)
	var body struct {
		Groups     []string `json:"groups"`
		ConfirmNew []string `json:"confirm_new"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, groupBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Groups == nil || len(body.ConfirmNew) > maxGroupsPerVM {
		writeGPUErr(w, refuse(http.StatusBadRequest, "invalid", "richiesta non valida: serve {\"groups\":[...]}"))
		return
	}
	groups, err := normalizeGroups(body.Groups)
	if err != nil {
		writeGPUErr(w, refuse(http.StatusBadRequest, "invalid", err.Error()))
		return
	}
	idx := clusterIdx(r)
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	res, e := setVMGroups(client, idx, node, kind, vmid, groups, body.ConfirmNew)
	if e != nil {
		writeGPUErr(w, e)
		return
	}
	writeJSON(w, res)
}
