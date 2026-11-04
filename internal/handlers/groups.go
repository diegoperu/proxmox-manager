package handlers

import (
	"encoding/json"
	"sort"
	"strings"
)

// Group of a VM from Proxmox tags (D35-D42, 2026-11-04).
//
//   - tag `gruppo-<name>` (case-insensitive on read), <name> = [a-z0-9]{1,32}: that is the group (source "tag");
//   - several such tags: the VM belongs to all of them and its resources are split equally (D37 C, D42 B);
//   - no tag: the prefix of the VM name (D27), but only if that group already exists as a tag on some other VM
//     (D36 C), so `ubuntu-test` is not a group (source "name");
//   - otherwise "altro" (shown as "senza gruppo", source "none").
const (
	tagGroupPrefix = "gruppo-"
	maxTagsRead    = 32

	groupSrcTag  = "tag"
	groupSrcName = "name"
	groupSrcNone = "none"
)

// parseTags splits a Proxmox tag string (`;` normally, `,` and spaces in old configs). Bounded.
func parseTags(s string) []string {
	if len(s) > 1024 {
		s = s[:1024]
	}
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '\t' })
	if len(f) > maxTagsRead {
		f = f[:maxTagsRead]
	}
	return f
}

// groupFromTag returns the group named by a tag, if the tag is a valid group tag. "altro" is reserved.
func groupFromTag(tag string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(tag))
	if !strings.HasPrefix(t, tagGroupPrefix) {
		return "", false
	}
	name := strings.TrimPrefix(t, tagGroupPrefix)
	if !groupPattern.MatchString(name) || name == otherGroup {
		return "", false
	}
	return name, true
}

// tagGroups returns the distinct valid groups in a tag string, sorted.
func tagGroups(tags string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range parseTags(tags) {
		if g, ok := groupFromTag(t); ok && !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

// groupAssign is the result for one VM: Groups is never empty and sorted.
type groupAssign struct {
	Groups []string
	Source string
}

func (a groupAssign) Shared() bool { return len(a.Groups) > 1 }

// groupResolver knows which groups exist as tags in the cluster (D36 C).
type groupResolver struct{ known map[string]bool }

// newGroupResolver is built from the tag strings of every non-template VM of the cluster.
func newGroupResolver(tagStrings []string) groupResolver {
	known := map[string]bool{}
	for _, ts := range tagStrings {
		for _, g := range tagGroups(ts) {
			known[g] = true
		}
	}
	return groupResolver{known: known}
}

func (r groupResolver) resolve(name, tags string) groupAssign {
	if g := tagGroups(tags); len(g) > 0 {
		return groupAssign{Groups: g, Source: groupSrcTag}
	}
	if p := vmGroup(name); p != otherGroup && r.known[p] {
		return groupAssign{Groups: []string{p}, Source: groupSrcName}
	}
	return groupAssign{Groups: []string{otherGroup}, Source: groupSrcNone}
}

// has reports whether the VM belongs to group g.
func (a groupAssign) has(g string) bool {
	for _, x := range a.Groups {
		if x == g {
			return true
		}
	}
	return false
}

// splitEven splits total into n parts that sum exactly to total; the remainder goes to the first part (the first
// group in alphabetical order, D42). n <= 0 yields nil.
func splitEven(total int64, n int) []int64 {
	if n <= 0 {
		return nil
	}
	out := make([]int64, n)
	base, rem := total/int64(n), total%int64(n)
	for i := range out {
		out[i] = base
	}
	out[0] += rem
	return out
}

// groupsByVMID resolves the groups of every non-template VM in a /cluster/resources answer.
func groupsByVMID(raw json.RawMessage) (map[int]groupAssign, error) {
	var list []map[string]interface{}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	var tagStrs []string
	for _, r := range list {
		if typ, _ := r["type"].(string); (typ == "qemu" || typ == "lxc") && !isTemplateRes(r) {
			ts, _ := r["tags"].(string)
			tagStrs = append(tagStrs, ts)
		}
	}
	rs := newGroupResolver(tagStrs)
	out := map[int]groupAssign{}
	for _, r := range list {
		if typ, _ := r["type"].(string); (typ != "qemu" && typ != "lxc") || isTemplateRes(r) {
			continue
		}
		if vmid := int(intField(r, "vmid")); vmid > 0 {
			name, _ := r["name"].(string)
			ts, _ := r["tags"].(string)
			out[vmid] = rs.resolve(name, ts)
		}
	}
	return out, nil
}
