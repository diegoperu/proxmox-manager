package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"proxmox-manager/internal/config"
)

func reportSettingsIndex(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "idx")
	idx, err := strconv.Atoi(raw)
	if err != nil || idx < 0 || len(raw) > 10 || strings.Trim(raw, "0123456789") != "" {
		writeError(w, errors.New("indice cluster non valido"), 400)
		return 0, false
	}
	if idx >= len(config.Get().Clusters) {
		writeError(w, config.ErrReportCluster, 404)
		return 0, false
	}
	return idx, true
}

func (h *Handler) GetReportSettings(w http.ResponseWriter, r *http.Request) {
	idx, ok := reportSettingsIndex(w, r)
	if !ok {
		return
	}
	cfg := config.Get()
	if idx >= len(cfg.Clusters) {
		writeError(w, config.ErrReportCluster, 404)
		return
	}
	settings := cfg.Clusters[idx].Report
	if settings == nil {
		settings = &config.ReportSettings{}
	}
	writeJSON(w, settings)
}

func (h *Handler) PutReportSettings(w http.ResponseWriter, r *http.Request) {
	idx, ok := reportSettingsIndex(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || !utf8.Valid(data) {
		writeError(w, errors.New("richiesta non valida (JSON UTF-8, massimo 64 KB)"), 400)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var settings *config.ReportSettings
	if err := dec.Decode(&settings); err != nil || settings == nil {
		writeError(w, errors.New("impostazioni non valide: oggetto JSON con soli campi previsti richiesto"), 400)
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeError(w, errors.New("impostazioni non valide: un solo oggetto JSON richiesto"), 400)
		return
	}
	if err := config.ValidReportSettings(settings); err != nil {
		writeError(w, err, 400)
		return
	}
	if err := config.SetReportSettings(idx, settings); err != nil {
		if errors.Is(err, config.ErrReportCluster) {
			writeError(w, err, 404)
		} else {
			writeError(w, errors.New("impossibile salvare le impostazioni dei report"), 500)
		}
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}
