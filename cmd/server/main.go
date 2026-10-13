package main

import (
	"bytes"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"

	"proxmox-manager/internal/cache"
	"proxmox-manager/internal/config"
	"proxmox-manager/internal/handlers"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// web/ lives in the same directory as main.go — Go embed requires this.
// web/static holds vendored third-party libraries and fonts (see web/static/THIRD_PARTY.md);
// the application frontend itself stays the single file web/templates/index.html.
//
//go:embed web/templates/index.html web/static
var staticFiles embed.FS

func main() {
	if err := config.Load(); err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg := config.Get()

	store, err := cache.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()

	h := handlers.New(store)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"http://localhost:*", "http://127.0.0.1:*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"*"},
	}))

	// Vendored libraries and fonts (read-only, embedded)
	r.Handle("/static/*", staticHandler())

	// SPA pages
	for _, path := range []string{"/", "/nodes", "/vms", "/batch", "/cluster", "/provision", "/reports", "/settings", "/console"} {
		r.Get(path, serveIndex)
	}

	// API
	r.Route("/api", func(r chi.Router) {
		r.Get("/config", h.GetConfig)
		r.Post("/config", h.SaveConfig)
		r.Post("/config/test", h.TestConnection)

		r.Get("/clusters", h.GetClusters)
		r.Post("/clusters", h.AddCluster)
		r.Put("/clusters/{idx}", h.UpdateCluster)
		r.Delete("/clusters/{idx}", h.DeleteCluster)
		r.Post("/clusters/{idx}/test", h.TestCluster)
		r.Put("/clusters/{idx}/default", h.SetDefaultCluster)

		r.Get("/dashboard", h.GetDashboard)

		r.Get("/nodes", h.GetNodes)
		r.Get("/nodes/{node}/status", h.GetNodeStatus)
		r.Get("/nodes/{node}/rrd", h.GetNodeRRD)
		r.Post("/nodes/{node}/cmd/{cmd}", h.NodeCommand)
		r.Get("/nodes/{node}/storage", h.GetNodeStorage)

		r.Get("/vms", h.GetAllVMs)
		r.Get("/nodes/{node}/qemu/{vmid}/status", h.GetVMStatus)
		r.Get("/nodes/{node}/qemu/{vmid}/rrd", h.GetVMRRD)
		r.Get("/nodes/{node}/qemu/{vmid}/fsinfo", h.GetVMFSInfo)
		r.Post("/nodes/{node}/qemu/{vmid}/action/{action}", h.VMAction)
		r.Post("/nodes/{node}/qemu/{vmid}/snapshot", h.VMSnapshot)
		r.Post("/nodes/{node}/qemu/{vmid}/migrate", h.VMMigrate)
		r.Post("/nodes/{node}/qemu/{vmid}/adduser", h.AddVMUser)
		r.Post("/nodes/{node}/qemu/{vmid}/adddisk", h.AddVMDisk)
		r.Post("/nodes/{node}/qemu/{vmid}/setup-disk", h.SetupVMDisk)
		r.Post("/nodes/{node}/qemu/{vmid}/termproxy", h.TermproxyCreate)
		r.Get("/nodes/{node}/qemu/{vmid}/termproxy-ws", h.TermproxyWS)
		r.Delete("/nodes/{node}/qemu/{vmid}", h.DeleteVM)

		r.Post("/batch", h.BatchAction)

		r.Post("/nodes/{node}/lxc/{vmid}/action/{action}", h.ContainerAction)
		r.Post("/nodes/{node}/lxc/{vmid}/snapshot", h.ContainerSnapshot)
		r.Delete("/nodes/{node}/lxc/{vmid}", h.DeleteContainer)

		// Provisioning
		r.Get("/templates", h.GetTemplates)
		r.Get("/nextid", h.GetNextVMID)
		r.Post("/provision", h.ProvisionVM)

		r.Get("/storage", h.GetStorage)
		r.Get("/pools", h.GetPools)
		r.Get("/tasks", h.GetClusterTasks)

		r.Get("/metrics", h.GetMetrics)
		r.Get("/report", h.GetReportData)
	})

	log.Printf("ProxmoxManager → http://localhost%s", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, r))
}

// staticTypes is the allowlist of served extensions: anything else under /static/ is a 404.
// Types are explicit because Go's built-in MIME table has no woff2.
var staticTypes = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".woff2": "font/woff2",
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "web/static")
	if err != nil {
		log.Fatalf("static: %v", err)
	}
	files := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct, ok := staticTypes[strings.ToLower(path.Ext(r.URL.Path))]
		if !ok || strings.HasSuffix(r.URL.Path, "/") { // no directory listings, no unknown types
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}

// validThemes mirrors the html.<theme> classes in index.html. The empty theme means
// "follow the OS" and adds no class.
var validThemes = map[string]bool{"dark": true, "light": true, "eva01": true, "catppuccino": true}

// injectTheme puts the saved theme on <html> so the first paint already uses it
// (no localStorage: the preference lives in config.json). Only whitelisted values
// are ever written into the page.
func injectTheme(page []byte, theme string) []byte {
	if !validThemes[theme] {
		return page
	}
	return bytes.Replace(page, []byte(`<html lang="it">`), []byte(`<html lang="it" class="`+theme+`">`), 1)
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		http.Error(w, "not found", 500)
		return
	}
	b = injectTheme(b, config.Get().Theme)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache") // the page varies with the saved theme
	w.Write(b)
}
