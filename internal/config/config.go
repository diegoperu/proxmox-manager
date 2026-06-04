package config

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

type ClusterConfig struct {
	Label    string `json:"label"`
	URL      string `json:"url"`
	APIToken string `json:"api_token"`
	Default  bool   `json:"default"`
}

type Config struct {
	Clusters     []ClusterConfig `json:"clusters"`
	Theme        string          `json:"theme"`
	CacheSeconds int             `json:"cache_seconds"`
	DBPath       string          `json:"db_path"`
	ListenAddr   string          `json:"listen_addr"`
}

var (
	current Config
	mu      sync.RWMutex
)

const cfgPath = "config.json"

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
