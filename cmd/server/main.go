package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"

	"proxmox-manager/internal/cache"
	"proxmox-manager/internal/config"
	"proxmox-manager/internal/handlers"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// web/ lives in the same directory as main.go — Go embed requires this.
//
//go:embed web/templates/index.html
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

func serveIndex(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		http.Error(w, "not found", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}
