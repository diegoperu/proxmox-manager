// Package snippetsync builds what every node must hold (onboarding step 4, D150): the cloud-init snippets rendered by
// the embedded pmgr-os.sh for the cluster profile, and the tooling itself, and compares it with what a node holds.
//
// The content comes only from the files embedded in the signed binary plus a profile chosen from a closed list: no
// text from a page or a request ever ends up in a snippet.
package snippetsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/nodessh"
)

// Profiles a cluster can use (D90): "hosting" adds key-only SSH, firewall and hardening to the snippets.
var Profiles = map[string]bool{"standard": true, "hosting": true}

// File is one file a node must hold, under snippets/ or pmgr-scripts/.
type File struct {
	Dir  string `json:"dir"`
	Path string `json:"path"` // relative to Dir
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Exec bool   `json:"-"`
	data []byte
}

// Key is the path as the node gate reports it ("snippets/vendor_x.yaml").
func (f File) Key() string { return f.Dir + "/" + f.Path }

// Bundle is the full expected content for one profile.
type Bundle struct {
	Profile string
	Files   []File
	Digest  string
}

var (
	snippetName = regexp.MustCompile(`^vendor_[A-Za-z0-9._-]{1,80}\.yaml$`)
	scriptPath  = regexp.MustCompile(`^[A-Za-z0-9._/@+-]{1,240}$`)
)

const maxFileSize = 1 << 20

// Renderer runs the embedded pmgr-os.sh (bash, argument list, minimal environment).
type Renderer func(ctx context.Context, script string, args, env []string) error

func bashRenderer(ctx context.Context, script string, args, env []string) error {
	bash, err := exec.LookPath("bash")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bash, append([]string{script}, args...)...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		msg := out.String()
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return fmt.Errorf("generazione degli snippet non riuscita: %v: %s", err, strings.TrimSpace(msg))
	}
	return nil
}

// Builder renders bundles; one per profile is kept for the life of the process (the embedded files never change).
type Builder struct {
	src    fs.FS
	render Renderer
	mu     sync.Mutex
	cache  map[string]*Bundle
}

// NewBuilder uses bash to render.
func NewBuilder(src fs.FS) *Builder {
	return &Builder{src: src, render: bashRenderer, cache: map[string]*Bundle{}}
}

// NewBuilderWithRenderer is for tests.
func NewBuilderWithRenderer(src fs.FS, r Renderer) *Builder {
	return &Builder{src: src, render: r, cache: map[string]*Bundle{}}
}

// Build returns the bundle of a profile, rendering it the first time.
func (b *Builder) Build(ctx context.Context, profile string) (*Bundle, error) {
	if !Profiles[profile] {
		return nil, errors.New("profilo non valido")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.cache[profile]; c != nil {
		return c, nil
	}
	bundle, err := b.build(ctx, profile)
	if err != nil {
		return nil, err
	}
	b.cache[profile] = bundle
	return bundle, nil
}

func (b *Builder) build(ctx context.Context, profile string) (*Bundle, error) {
	work, err := os.MkdirTemp("", "pmgr-render-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	tree, out := filepath.Join(work, "tree"), filepath.Join(work, "out")
	scripts, err := extract(b.src, tree)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + work, "PMGR_CLUSTER_CONF=" + filepath.Join(work, "none")}
	if err := b.render(ctx, filepath.Join(tree, "bin", "pmgr-os.sh"), []string{"--render", out, "--profile", profile, "--all"}, env); err != nil {
		return nil, err
	}
	snippets, err := readSnippets(out)
	if err != nil {
		return nil, err
	}
	files := append(snippets, scripts...)
	sort.Slice(files, func(i, j int) bool { return files[i].Key() < files[j].Key() })
	return &Bundle{Profile: profile, Files: files, Digest: digest(files)}, nil
}

func newFile(dir, p string, data []byte, exec bool) File {
	sum := sha256.Sum256(data)
	return File{Dir: dir, Path: p, Hash: hex.EncodeToString(sum[:]), Size: int64(len(data)), Exec: exec, data: data}
}

// extract writes the embedded tree to dir (scripts 0755, the rest 0644) and returns it as pmgr-scripts files.
func extract(src fs.FS, dir string) ([]File, error) {
	var files []File
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if strings.HasSuffix(p, ".go") {
			return nil
		}
		if !scriptPath.MatchString(p) || strings.Contains(p, "..") {
			return fmt.Errorf("nome di file non ammesso negli strumenti: %q", p)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		if len(data) > maxFileSize {
			return fmt.Errorf("file troppo grande negli strumenti: %q", p)
		}
		exec := strings.HasSuffix(p, ".sh")
		mode := os.FileMode(0o644)
		if exec {
			mode = 0o755
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
		files = append(files, newFile("pmgr-scripts", p, data, exec))
		return nil
	})
	return files, err
}

// readSnippets collects the rendered vendor_*.yaml files; anything else in the output is an error.
func readSnippets(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []File
	for _, e := range entries {
		if !e.Type().IsRegular() || !snippetName.MatchString(e.Name()) {
			return nil, fmt.Errorf("file inatteso nella generazione degli snippet: %q", e.Name())
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if len(data) == 0 || len(data) > maxFileSize {
			return nil, fmt.Errorf("snippet vuoto o troppo grande: %q", e.Name())
		}
		for _, c := range data {
			if c != '\n' && c != '\t' && (c < 0x20 || c > 0x7e) {
				return nil, fmt.Errorf("lo snippet %q contiene caratteri non ASCII", e.Name())
			}
		}
		files = append(files, newFile("snippets", e.Name(), data, false))
	}
	if len(files) == 0 {
		return nil, errors.New("nessuno snippet generato")
	}
	return files, nil
}

func digest(files []File) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s %s\n", f.Hash, f.Key())
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// NodePlan is what differs between a bundle and a node. Extra files are only reported, never deleted.
type NodePlan struct {
	Missing   []string `json:"missing"`
	Different []string `json:"different"`
	Extra     []string `json:"extra"`
	Same      int      `json:"same"`
}

// Aligned: every expected file is on the node with the expected content.
func (p NodePlan) Aligned() bool { return len(p.Missing) == 0 && len(p.Different) == 0 }

// Compare matches a bundle with a node listing.
func Compare(b *Bundle, l nodessh.Listing) NodePlan {
	plan := NodePlan{Missing: []string{}, Different: []string{}, Extra: []string{}}
	have := map[string]string{}
	for _, e := range l.Entries {
		have[e.Path] = e.Hash
	}
	want := map[string]bool{}
	for _, f := range b.Files {
		want[f.Key()] = true
		switch h, ok := have[f.Key()]; {
		case !ok:
			plan.Missing = append(plan.Missing, f.Key())
		case h != f.Hash:
			plan.Different = append(plan.Different, f.Key())
		default:
			plan.Same++
		}
	}
	for p := range have {
		if !want[p] {
			plan.Extra = append(plan.Extra, p)
		}
	}
	sort.Strings(plan.Extra)
	return plan
}

// Stage writes the files of one folder (only those the plan needs) into a fresh directory for rsync.
// The caller removes the returned directory.
func Stage(b *Bundle, dir string, keys []string) (string, error) {
	if !nodessh.RemoteDirs[dir] {
		return "", errors.New("cartella non ammessa")
	}
	need := map[string]bool{}
	for _, k := range keys {
		need[k] = true
	}
	stage, err := os.MkdirTemp("", "pmgr-stage-")
	if err != nil {
		return "", err
	}
	n := 0
	for _, f := range b.Files {
		if f.Dir != dir || !need[f.Key()] {
			continue
		}
		target := filepath.Join(stage, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			os.RemoveAll(stage)
			return "", err
		}
		mode := os.FileMode(0o644)
		if f.Exec {
			mode = 0o755
		}
		if err := os.WriteFile(target, f.data, mode); err != nil {
			os.RemoveAll(stage)
			return "", err
		}
		n++
	}
	if n == 0 {
		os.RemoveAll(stage)
		return "", nil
	}
	return stage, nil
}

// DirsOf returns the folders ("snippets", "pmgr-scripts") that contain at least one of keys.
func DirsOf(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		d := strings.SplitN(path.Clean(k), "/", 2)[0]
		if nodessh.RemoteDirs[d] && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
