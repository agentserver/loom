package commanderhub

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/dist/*
//go:embed assets/dist/assets/*
var assetsFS embed.FS

// MountWeb registers the commander page routes and static assets.
func MountWeb(mux *http.ServeMux) {
	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		data, err := assetsFS.ReadFile("assets/dist/index.html")
		if err != nil {
			http.Error(w, "index unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	}
	mux.HandleFunc("/commander", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/commander" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r)
	})
	mux.HandleFunc("/commander/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/commander/" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r)
	})
	sub, _ := fs.Sub(assetsFS, "assets/dist")
	fileServer := http.StripPrefix("/commander/", http.FileServer(http.FS(sub)))
	mux.Handle("/commander/assets/", fileServer)
}
