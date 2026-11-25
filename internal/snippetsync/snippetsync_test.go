package snippetsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"proxmox-manager/internal/nodessh"
	pmgrscripts "proxmox-manager/tools/pmgr-scripts"
)

func fakeTree() fstest.MapFS {
	return fstest.MapFS{
		"bin/pmgr-os.sh":   {Data: []byte("#!/bin/bash\n")},
		"lib/pmgr-lib.sh":  {Data: []byte("x\n")},
		"os.d/debian.conf": {Data: []byte("ID=debian\n")},
		"README.md":        {Data: []byte("readme\n")},
	}
}

// renderWriting returns a renderer that writes the given files into the --render directory.
func renderWriting(files map[string]string) Renderer {
	return func(_ context.Context, script string, args, env []string) error {
		out := args[1]
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(out, name), []byte(body), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestBuildCollectsSnippetsAndScripts(t *testing.T) {
	var gotArgs, gotEnv []string
	r := func(ctx context.Context, script string, args, env []string) error {
		gotArgs, gotEnv = args, env
		if fi, err := os.Stat(script); err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("script not extracted executable: %v", err)
		}
		return renderWriting(map[string]string{"vendor_debian_13.yaml": "#cloud-config\n"})(ctx, script, args, env)
	}
	b := NewBuilderWithRenderer(fakeTree(), r)
	bundle, err := b.Build(context.Background(), "hosting")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotArgs[2:], " ") != "--profile hosting --all" {
		t.Fatalf("args %q", gotArgs)
	}
	for _, e := range gotEnv {
		if strings.HasPrefix(e, "PMGR_CLUSTER_CONF=") && !strings.HasSuffix(e, "/none") {
			t.Fatalf("cluster.conf of the machine could be read: %q", e)
		}
	}
	keys := []string{}
	for _, f := range bundle.Files {
		keys = append(keys, f.Key())
	}
	want := "pmgr-scripts/README.md pmgr-scripts/bin/pmgr-os.sh pmgr-scripts/lib/pmgr-lib.sh pmgr-scripts/os.d/debian.conf snippets/vendor_debian_13.yaml"
	if strings.Join(keys, " ") != want {
		t.Fatalf("files %q", keys)
	}
	again, _ := b.Build(context.Background(), "hosting")
	if again != bundle {
		t.Fatal("bundle not cached")
	}
	if _, err := b.Build(context.Background(), "nope"); err == nil {
		t.Fatal("unknown profile accepted")
	}
}

func TestBuildRefusesUnexpectedOutput(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"other file":  {"vendor_x.yaml": "a\n", "evil.sh": "x"},
		"non ascii":   {"vendor_x.yaml": "caff\xc3\xa8\n"},
		"empty":       {"vendor_x.yaml": ""},
		"no snippets": {},
	} {
		b := NewBuilderWithRenderer(fakeTree(), renderWriting(files))
		if _, err := b.Build(context.Background(), "standard"); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	bad := fakeTree()
	bad["bin/with space.sh"] = &fstest.MapFile{Data: []byte("x")}
	if _, err := NewBuilderWithRenderer(bad, renderWriting(map[string]string{"vendor_x.yaml": "a\n"})).Build(context.Background(), "standard"); err == nil {
		t.Fatal("file name with a space accepted")
	}
}

func TestCompareAndStage(t *testing.T) {
	b, err := NewBuilderWithRenderer(fakeTree(), renderWriting(map[string]string{"vendor_a.yaml": "A\n"})).Build(context.Background(), "standard")
	if err != nil {
		t.Fatal(err)
	}
	hash := map[string]string{}
	for _, f := range b.Files {
		hash[f.Key()] = f.Hash
	}
	l := nodessh.Listing{Entries: []nodessh.Entry{
		{Path: "snippets/vendor_a.yaml", Hash: strings.Repeat("0", 64)},
		{Path: "pmgr-scripts/README.md", Hash: hash["pmgr-scripts/README.md"]},
		{Path: "snippets/old.yaml", Hash: strings.Repeat("1", 64)},
	}}
	p := Compare(b, l)
	if p.Aligned() || strings.Join(p.Different, ",") != "snippets/vendor_a.yaml" || len(p.Missing) != 3 || p.Same != 1 ||
		strings.Join(p.Extra, ",") != "snippets/old.yaml" {
		t.Fatalf("plan %+v", p)
	}
	dirs := DirsOf(append(p.Missing, p.Different...))
	if strings.Join(dirs, ",") != "pmgr-scripts,snippets" {
		t.Fatalf("dirs %q", dirs)
	}
	stage, err := Stage(b, "pmgr-scripts", p.Missing)
	if err != nil || stage == "" {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	if fi, err := os.Stat(filepath.Join(stage, "bin", "pmgr-os.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("staged script: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "README.md")); err == nil {
		t.Fatal("staged a file that is already equal on the node")
	}
	if s, err := Stage(b, "snippets", nil); s != "" || err != nil {
		t.Fatalf("empty stage %q %v", s, err)
	}
	if _, err := Stage(b, "../etc", p.Missing); err == nil {
		t.Fatal("folder outside the gate accepted")
	}
}

// The real embedded tooling, rendered with bash: 8 releases, deterministic, profiles differ.
func TestBuildRealTooling(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	b := NewBuilder(pmgrscripts.FS)
	std, err := b.Build(context.Background(), "standard")
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewBuilder(pmgrscripts.FS).Build(context.Background(), "hosting")
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewBuilder(pmgrscripts.FS).Build(context.Background(), "standard")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range std.Files {
		if f.Dir == "snippets" {
			n++
		}
		if f.Dir == "pmgr-scripts" && strings.HasPrefix(f.Path, "tests/") {
			t.Fatalf("tests shipped to the nodes: %s", f.Path)
		}
	}
	if n != 8 {
		t.Fatalf("%d snippets, want 8", n)
	}
	if std.Digest != again.Digest {
		t.Fatal("rendering is not deterministic")
	}
	if std.Digest == host.Digest {
		t.Fatal("standard and hosting render the same")
	}
}
