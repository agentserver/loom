package userspace

import (
	"net/http"
	"strings"
)

// MountRoutes wires every /api/userspace/* path onto the given mux.
// Call once at observer-server startup.
func MountRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /api/userspace/packages", h.push)
	mux.HandleFunc("GET /api/userspace/search", h.search)
	mux.HandleFunc("GET /api/userspace/packages", h.listPackages)
	mux.HandleFunc("GET /api/userspace/packages/", h.getPackage)
	mux.HandleFunc("POST /api/userspace/packages/", h.routePackagePost)
	mux.HandleFunc("POST /api/userspace/workspaces/", h.installVersion)
	mux.HandleFunc("DELETE /api/userspace/workspaces/", h.installVersion)
}

// routePackagePost dispatches POST /api/userspace/packages/{slug}/yank/{ver}
// (no other POST endpoints under /packages/ in v1).
func (h *Handler) routePackagePost(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/yank/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	h.yankVersion(w, r)
}
