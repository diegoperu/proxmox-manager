package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"proxmox-manager/internal/config"
)

// Corrections of the name and video memory of a card model (D62 A). They live in config.json (`gpu_models`),
// keyed by the model string Proxmox reports, valid for every cluster. They are applied on a COPY of the
// inventory after the cache, so the cached data stays as Proxmox gave it and a correction shows at once.

// withGPUModels returns inv with the corrections applied; inv itself (possibly the cached one) is not changed.
func withGPUModels(inv GPUInventory, models map[string]config.GPUModel) GPUInventory {
	out := inv
	out.Nodes = make([]GPUNode, len(inv.Nodes))
	for i, n := range inv.Nodes {
		cards := make([]GPUCard, len(n.Cards)) // never nil: the page iterates "cards" and an empty node must stay []
		copy(cards, n.Cards)
		n.Cards = cards
		for j := range n.Cards {
			c := &n.Cards[j]
			c.Model = c.Name
			c.DisplayName, c.MemoryCustom = "", false
			if m, ok := models[c.Name]; ok {
				if m.Name != "" {
					c.DisplayName = m.Name
				}
				if m.MemoryGB > 0 {
					c.MemoryGB, c.MemoryCustom = m.MemoryGB, true
				}
			}
		}
		out.Nodes[i] = n
	}
	return out
}

// gpuModelsKey identifies the current corrections, so that a cached answer that includes names is not reused after one.
func gpuModelsKey() string {
	m := config.GPUModels()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k+"="+m[k].Name+"/"+strconv.Itoa(m[k].MemoryGB))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x1f")
}

// PutGPUModel: PUT /api/gpu-models  body {"model": "<as Proxmox writes it>", "name": "", "memory_gb": 0}
// An empty name with memory 0 removes the correction. The model must be one of this cluster's cards or one
// that already has a correction (so a typo cannot create an entry nobody can see or remove).
func (h *Handler) PutGPUModel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	var body struct {
		Model    string `json:"model"`
		Name     string `json:"name"`
		MemoryGB *int   `json:"memory_gb"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() || body.MemoryGB == nil {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return
	}
	entry := config.GPUModel{Name: strings.TrimSpace(body.Name), MemoryGB: *body.MemoryGB}
	if err := config.ValidGPUModel(body.Model, entry); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	idx := clusterIdx(r)
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	_, existing := config.GPUModels()[body.Model]
	if !existing {
		v, err := cephCached("gpus|"+strconv.Itoa(idx), gpuCacheTTL, func() (interface{}, error) {
			return fetchGPUInventory(client, time.Now())
		})
		inv, ok := v.(GPUInventory)
		if err != nil || !ok {
			writeError(w, errors.New("dati GPU non disponibili"), http.StatusBadGateway)
			return
		}
		found := false
		for _, n := range inv.Nodes {
			for _, c := range n.Cards {
				found = found || c.Name == body.Model
			}
		}
		if !found {
			writeError(w, errors.New("modello sconosciuto in questo cluster"), http.StatusNotFound)
			return
		}
	}
	if err := config.SetGPUModel(body.Model, entry); err != nil {
		if strings.Contains(err.Error(), "non valid") || strings.Contains(err.Error(), "troppe") {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		log.Printf("gpu model %q: %v", body.Model, err)
		writeError(w, errors.New("salvataggio non riuscito"), http.StatusInternalServerError)
		return
	}
	log.Printf("gpu model: %s", fmt.Sprintf("model=%q name=%q memory_gb=%d", body.Model, entry.Name, entry.MemoryGB))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]interface{}{"model": body.Model, "name": entry.Name, "memory_gb": entry.MemoryGB})
}
