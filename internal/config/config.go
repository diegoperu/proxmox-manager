package config

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type ClusterConfig struct {
	Label    string `json:"label"`
	URL      string `json:"url"`
	APIToken string `json:"api_token"`
	Username string `json:"username"`
	Password string `json:"password"`
	Default  bool   `json:"default"`
}

type Config struct {
	Clusters     []ClusterConfig `json:"clusters"`
	Theme        string          `json:"theme"`
	Density      string          `json:"density"`
	CacheSeconds int             `json:"cache_seconds"`
	DBPath       string          `json:"db_path"`
	ListenAddr   string          `json:"listen_addr"`
	// AllowedHosts, if not empty, restricts the Host header accepted on /api (defence against DNS rebinding).
	AllowedHosts []string `json:"allowed_hosts"`
	// GPUModels: corrections of the name shown and of the video memory of a card model (D62 A), keyed by the
	// model string Proxmox reports. Valid for every cluster; the built-in table is the default.
	GPUModels map[string]GPUModel `json:"gpu_models,omitempty"`
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

var cfgPath = "config.json" // a variable only so that tests can point it to a temporary directory

func Load() error {
	mu.Lock()
	defer mu.Unlock()
	current = Config{
		CacheSeconds: 30,
		DBPath:       "proxmox.db",
		ListenAddr:   ":8080",
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
		if v, ok := raw["cache_seconds"]; ok {
			json.Unmarshal(v, &current.CacheSeconds)
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

	return json.Unmarshal(data, &current)
}

func save() error {
	f, err := os.Create(cfgPath)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(current)
}

func Get() Config {
	mu.RLock()
	defer mu.RUnlock()
	c := current
	c.Clusters = make([]ClusterConfig, len(current.Clusters))
	copy(c.Clusters, current.Clusters)
	c.GPUModels = copyGPUModels(current.GPUModels)
	return c
}

func Update(c Config) error {
	mu.Lock()
	current = c
	mu.Unlock()
	return save()
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
		return current.Clusters[idx], nil
	}
	// idx fuori range: fallback al default
	for _, c := range current.Clusters {
		if c.Default {
			return c, nil
		}
	}
	return current.Clusters[0], nil
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
			return c, nil
		}
	}
	log.Printf("warning: nessun cluster con default=true, usando Clusters[0]")
	return current.Clusters[0], nil
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
