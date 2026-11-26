package config

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type ClusterConfig struct {
	Report   *ReportSettings  `json:"report,omitempty"`
	Network  *NetworkSettings `json:"network,omitempty"`
	Label    string           `json:"label"`
	URL      string           `json:"url"`
	APIToken string           `json:"api_token"`
	Username string           `json:"username"`
	Password string           `json:"password"`
	Default  bool             `json:"default"`
	// TLSCA: PEM of the cluster CA (/etc/pve/pve-root-ca.pem); when set, the certificate of the cluster must be
	// signed by it (D163). Empty = not verified. Changed only through SetClusterTLSCA (admin + TOTP).
	TLSCA string `json:"tls_ca,omitempty"`
}

type Config struct {
	Clusters       []ClusterConfig `json:"clusters"`
	Theme          string          `json:"theme"`
	Density        string          `json:"density"`
	RefreshSeconds int             `json:"refresh_seconds"`
	DBPath         string          `json:"db_path"`
	ListenAddr     string          `json:"listen_addr"`
	// GPUUsage turns on the periodic nvidia-smi sampling inside the VMs that have a GPU (ROADMAP 4).
	// Off by default: it runs a command in production guests every 5 minutes.
	GPUUsage bool `json:"gpu_usage"`
	// VMUsage controls the daily VM usage history (ROADMAP 21): nil/absent = on (it only reads the RRD),
	// false = off. A pointer so that existing config files without the field keep the default.
	VMUsage *bool `json:"vm_usage,omitempty"`
	// UpdateCheck reads public GitHub build comparisons unless explicitly false.
	UpdateCheck *bool `json:"update_check,omitempty"`
	// AllowedHosts, if not empty, restricts the Host header accepted on /api (defence against DNS rebinding).
	AllowedHosts []string `json:"allowed_hosts"`
	// AuthKeyPath is the 0600 file with the key that encrypts the TOTP secrets of the accounts (voce 20).
	// Created at the first start when missing; relative paths are relative to the working directory.
	AuthKeyPath string `json:"auth_key_path,omitempty"`
	// TrustedProxies: addresses or CIDR prefixes of the reverse proxy (D147). X-Forwarded-For and
	// X-Forwarded-Host are honoured only from these; empty = none (direct access).
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	// GPUModels: corrections of the name shown and of the video memory of a card model (D62 A), keyed by the
	// model string Proxmox reports. Valid for every cluster; the built-in table is the default.
	GPUModels map[string]GPUModel `json:"gpu_models,omitempty"`
	// SSHDir holds the manager's SSH key for the nodes (pmgr-sync) and the pinned node host keys (onboarding
	// step 3). Created with mode 0700 on first use; relative paths are relative to the working directory.
	SSHDir string `json:"ssh_dir,omitempty"`
}

// GPUModel is one correction: an empty Name keeps the model's own name, MemoryGB 0 keeps the default size.
type GPUModel struct {
	Name     string `json:"name"`
	MemoryGB int    `json:"memory_gb"`
}

const (
	gpuModelKeyMax  = 160
	gpuModelNameMax = 64
	gpuModelMaxGB   = 512
	gpuModelsMax    = 128
)

var (
	current Config
	mu      sync.RWMutex
)

// Allowed values for the appearance preferences. The empty string means "default":
// theme follows the OS, density is comfortable. Whitelists live here so that the
// HTTP handler (on save) and the page renderer (on injection) cannot drift apart.
var (
	themes    = map[string]bool{"dark": true, "light": true, "eva01": true, "catppuccino": true, "spiderman": true}
	densities = map[string]bool{"compact": true}
)

// ValidTheme reports whether t is "" or a known theme.
func ValidTheme(t string) bool { return t == "" || themes[t] }

// ValidDensity reports whether d is "" or a known density.
func ValidDensity(d string) bool { return d == "" || densities[d] }

// VMUsageEnabled: the daily VM usage history runs unless vm_usage is explicitly false.
func (c Config) VMUsageEnabled() bool { return c.VMUsage == nil || *c.VMUsage }

func (c Config) UpdateCheckEnabled() bool { return c.UpdateCheck == nil || *c.UpdateCheck }

// ValidRefresh reports whether the automatic refresh interval is allowed.
func ValidRefresh(n int) bool {
	switch n {
	case 0, 15, 30, 60, 120, 300:
		return true
	}
	return false
}

var cfgPath = "config.json" // a variable only so that tests can point it to a temporary directory

func Load() error {
	mu.Lock()
	defer mu.Unlock()
	current = Config{
		RefreshSeconds: 60,
		DBPath:         "proxmox.db",
		ListenAddr:     ":8080",
		AuthKeyPath:    "auth.key",
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return save()
		}
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Migrate from flat format (proxmox_url + api_token)
	if _, hasURL := raw["proxmox_url"]; hasURL {
		var oldURL, oldToken string
		json.Unmarshal(raw["proxmox_url"], &oldURL)
		if v, ok := raw["api_token"]; ok {
			json.Unmarshal(v, &oldToken)
		}
		if v, ok := raw["theme"]; ok {
			json.Unmarshal(v, &current.Theme)
		}
		if v, ok := raw["refresh_seconds"]; ok {
			if err := json.Unmarshal(v, &current.RefreshSeconds); err != nil {
				return err
			}
		}
		if v, ok := raw["db_path"]; ok {
			json.Unmarshal(v, &current.DBPath)
		}
		if v, ok := raw["listen_addr"]; ok {
			json.Unmarshal(v, &current.ListenAddr)
		}
		current.Clusters = []ClusterConfig{{
			Label:    "Default",
			URL:      oldURL,
			APIToken: oldToken,
			Default:  true,
		}}
		return save()
	}

	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}
	for _, c := range current.Clusters {
		if err := ValidReportSettings(c.Report); err != nil {
			return err
		}
		if err := ValidNetworkSettings(c.Network); err != nil {
			return err
		}
	}
	return nil
}

// save requires mu to be held. Rename keeps readers from observing a partial file.
func save() error {
	f, err := os.CreateTemp(filepath.Dir(cfgPath), ".config-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(current); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, cfgPath)
}

func Get() Config {
	mu.RLock()
	defer mu.RUnlock()
	c := current
	c.Clusters = make([]ClusterConfig, len(current.Clusters))
	copy(c.Clusters, current.Clusters)
	for i := range c.Clusters {
		c.Clusters[i].Report = copyReport(current.Clusters[i].Report)
		c.Clusters[i].Network = copyNetwork(current.Clusters[i].Network)
	}
	c.AllowedHosts = append([]string(nil), current.AllowedHosts...)
	c.TrustedProxies = append([]string(nil), current.TrustedProxies...)
	if current.VMUsage != nil {
		v := *current.VMUsage
		c.VMUsage = &v
	}
	if current.UpdateCheck != nil {
		v := *current.UpdateCheck
		c.UpdateCheck = &v
	}
	c.GPUModels = copyGPUModels(current.GPUModels)
	return c
}

func Update(c Config) error {
	for _, cl := range c.Clusters {
		if err := ValidReportSettings(cl.Report); err != nil {
			return err
		}
		if err := ValidNetworkSettings(copyNetwork(cl.Network)); err != nil {
			return err
		}
	}
	// Keep caller-owned report pointers out of the live configuration.
	c.Clusters = append([]ClusterConfig(nil), c.Clusters...)
	for i := range c.Clusters {
		c.Clusters[i].Report = copyReport(c.Clusters[i].Report)
		c.Clusters[i].Network = copyNetwork(c.Clusters[i].Network)
		if err := ValidNetworkSettings(c.Clusters[i].Network); err != nil {
			return err
		}
	}
	mu.Lock()
	defer mu.Unlock()
	old := current
	current = c
	if err := save(); err != nil {
		current = old
		return err
	}
	return nil
}

// GetCluster ritorna il cluster all'indice idx.
// Se idx è fuori range, cade back sul cluster default — non ritorna mai errore
// se esiste almeno un cluster.
func GetCluster(idx int) (ClusterConfig, error) {
	mu.RLock()
	defer mu.RUnlock()
	if len(current.Clusters) == 0 {
		return ClusterConfig{}, nil
	}
	if idx >= 0 && idx < len(current.Clusters) {
		c := current.Clusters[idx]
		c.Report = copyReport(c.Report)
		c.Network = copyNetwork(c.Network)
		return c, nil
	}
	// idx fuori range: fallback al default
	for _, c := range current.Clusters {
		if c.Default {
			c.Report = copyReport(c.Report)
			c.Network = copyNetwork(c.Network)
			return c, nil
		}
	}
	c := current.Clusters[0]
	c.Report = copyReport(c.Report)
	c.Network = copyNetwork(c.Network)
	return c, nil
}

// GetDefaultCluster ritorna il primo cluster con Default==true.
// Se nessuno è default, ritorna Clusters[0] con un warning.
func GetDefaultCluster() (ClusterConfig, error) {
	mu.RLock()
	defer mu.RUnlock()
	if len(current.Clusters) == 0 {
		return ClusterConfig{}, nil
	}
	for _, c := range current.Clusters {
		if c.Default {
			c.Report = copyReport(c.Report)
			c.Network = copyNetwork(c.Network)
			return c, nil
		}
	}
	log.Printf("warning: nessun cluster con default=true, usando Clusters[0]")
	c := current.Clusters[0]
	c.Report = copyReport(c.Report)
	c.Network = copyNetwork(c.Network)
	return c, nil
}

// AllowedHosts returns a copy of the configured Host allowlist (empty = no restriction).
func AllowedHosts() []string {
	mu.RLock()
	defer mu.RUnlock()
	return append([]string(nil), current.AllowedHosts...)
}

func copyGPUModels(m map[string]GPUModel) map[string]GPUModel {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]GPUModel, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// GPUModels returns a copy of the card model corrections.
func GPUModels() map[string]GPUModel {
	mu.RLock()
	defer mu.RUnlock()
	return copyGPUModels(current.GPUModels)
}

func printableText(s string, max int) bool {
	if !utf8.ValidString(s) || len(s) > max*4 || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

// ValidGPUModel checks one correction: the model key as Proxmox writes it, the shown name and the size.
func ValidGPUModel(key string, m GPUModel) error {
	switch {
	case key == "" || !printableText(key, gpuModelKeyMax):
		return errors.New("modello non valido")
	case !printableText(m.Name, gpuModelNameMax):
		return errors.New("nome non valido (testo semplice, massimo 64 caratteri)")
	case m.MemoryGB < 0 || m.MemoryGB > gpuModelMaxGB:
		return errors.New("memoria non valida (0 = predefinita, massimo 512 GB)")
	}
	return nil
}

// SetGPUModel stores a correction and saves; an empty name with memory 0 removes it. The file is rewritten
// whole, so on a write error the in-memory value goes back to what it was.
func SetGPUModel(key string, m GPUModel) error {
	m.Name = strings.TrimSpace(m.Name)
	if err := ValidGPUModel(key, m); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	prev, had := current.GPUModels[key]
	if m.Name == "" && m.MemoryGB == 0 {
		delete(current.GPUModels, key)
	} else {
		if _, exists := current.GPUModels[key]; !exists && len(current.GPUModels) >= gpuModelsMax {
			return errors.New("troppe correzioni salvate")
		}
		if current.GPUModels == nil {
			current.GPUModels = map[string]GPUModel{}
		}
		current.GPUModels[key] = m
	}
	if err := save(); err != nil {
		if had {
			current.GPUModels[key] = prev
		} else {
			delete(current.GPUModels, key)
		}
		return err
	}
	return nil
}

// SSHDirectory returns the directory of the manager's SSH key and pinned host keys (default "ssh").
func (c Config) SSHDirectory() string {
	if c.SSHDir == "" {
		return "ssh"
	}
	return c.SSHDir
}

// AuthKeyFile returns the path of the TOTP encryption key (default auth.key).
func (c Config) AuthKeyFile() string {
	if c.AuthKeyPath == "" {
		return "auth.key"
	}
	return c.AuthKeyPath
}
