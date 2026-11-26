package handlers

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"proxmox-manager/internal/config"
)

func TestArchFromNodeStatus(t *testing.T) {
	for raw, want := range map[string]string{
		`{"current-kernel":{"machine":"x86_64"}}`:  "amd64",
		`{"current-kernel":{"machine":"aarch64"}}`: "arm64",
		`{"current-kernel":{"machine":"riscv64"}}`: "",
		`{"current-kernel":{}}`:                    "",
		`{}`:                                       "",
		`not json`:                                 "",
	} {
		if got := nodeStatusArch(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

func TestClusterArchOf(t *testing.T) {
	for _, c := range []struct {
		nodes    map[string]string
		arch     string
		complete bool
	}{
		{map[string]string{"a": "amd64", "b": "amd64"}, "amd64", true},
		{map[string]string{"a": "arm64"}, "arm64", true},
		{map[string]string{"a": "amd64", "b": "arm64"}, "mixed", true},
		{map[string]string{"a": "amd64", "b": ""}, "amd64", false},
		{map[string]string{"a": ""}, "", false},
		{map[string]string{}, "", true},
	} {
		if a, ok := clusterArchOf(c.nodes); a != c.arch || ok != c.complete {
			t.Errorf("%v: %q %v", c.nodes, a, ok)
		}
	}
}

func TestReadinessArchCheck(t *testing.T) {
	n := func(name, arch string, online bool) readinessNode { return readinessNode{Node: name, Arch: arch, Online: online} }
	if c := readinessArchCheck([]readinessNode{n("a", "amd64", true), n("b", "amd64", true), n("c", "arm64", false)}); c.Status != "ok" || !strings.Contains(c.Detail, "x86-64") {
		t.Fatalf("%+v", c) // an offline node does not count
	}
	if c := readinessArchCheck([]readinessNode{n("a", "amd64", true), n("b", "arm64", true)}); c.Status != "warn" || !strings.Contains(c.Detail, "b (ARM64)") {
		t.Fatalf("%+v", c)
	}
	if c := readinessArchCheck([]readinessNode{n("a", "amd64", true), n("b", "", true)}); c.Status != "unknown" || !strings.Contains(c.Detail, "b (sconosciuta)") {
		t.Fatalf("%+v", c)
	}
}

type fakeArchCluster struct {
	machines map[string]string
	nodesErr bool
}

func (f fakeArchCluster) Get(path string) (json.RawMessage, error) {
	if f.nodesErr || path != "/nodes" {
		return nil, errors.New("down")
	}
	out := []map[string]string{}
	for n := range f.machines {
		out = append(out, map[string]string{"node": n, "status": "online"})
	}
	out = append(out, map[string]string{"node": "off", "status": "offline"}, map[string]string{"node": "bad name", "status": "online"})
	b, _ := json.Marshal(out)
	return b, nil
}

func (f fakeArchCluster) GetNodeStatus(n string) (json.RawMessage, error) {
	m, ok := f.machines[n]
	if !ok {
		return nil, errors.New("no node")
	}
	return json.RawMessage(`{"current-kernel":{"machine":"` + m + `"}}`), nil
}

// GET /api/clusters/arch: one entry per configured cluster; an unreachable one is unknown and not cached.
func TestGetClustersArch(t *testing.T) {
	oldDir, _ := os.Getwd()
	old := config.Get()
	oldClient := clusterArchClient
	os.Chdir(t.TempDir())
	t.Cleanup(func() { config.Update(old); os.Chdir(oldDir); clusterArchClient = oldClient })
	clusterArchMu.Lock()
	clusterArchCache = map[int]clusterArchEntry{}
	clusterArchMu.Unlock()
	os.WriteFile("config.json", []byte(`{"clusters":[{"label":"x","url":"https://x:8006","api_token":"a@pve!b=1"},{"label":"arm","url":"https://arm:8006","api_token":"a@pve!b=1"},{"label":"mix","url":"https://mix:8006","api_token":"a@pve!b=1"},{"label":"down","url":"https://down:8006","api_token":"a@pve!b=1"}]}`), 0o600)
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	clusterArchClient = func(cl config.ClusterConfig) clusterNodesAPI {
		switch cl.Label {
		case "x":
			return fakeArchCluster{machines: map[string]string{"n1": "x86_64", "n2": "x86_64"}}
		case "arm":
			return fakeArchCluster{machines: map[string]string{"proxmoxarm": "aarch64"}}
		case "mix":
			return fakeArchCluster{machines: map[string]string{"n1": "x86_64", "n2": "aarch64"}}
		}
		return fakeArchCluster{nodesErr: true}
	}
	w := httptest.NewRecorder()
	New().GetClustersArch(w, httptest.NewRequest("GET", "/api/clusters/arch", nil))
	var got []clusterArchInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got) != 4 {
		t.Fatalf("%v %s", err, w.Body)
	}
	want := []string{"amd64", "arm64", "mixed", ""}
	for i, g := range got {
		if g.Index != i || g.Arch != want[i] || g.Nodes == nil {
			t.Errorf("%d: %+v", i, g)
		}
	}
	if got[3].Complete {
		t.Error("unreachable cluster marked complete")
	}
	if _, ok := got[0].Nodes["off"]; ok {
		t.Error("offline node read")
	}
	clusterArchMu.Lock()
	_, downCached := clusterArchCache[3]
	_, xCached := clusterArchCache[0]
	clusterArchMu.Unlock()
	if downCached || !xCached {
		t.Errorf("cache: down %v, x %v", downCached, xCached)
	}
}
