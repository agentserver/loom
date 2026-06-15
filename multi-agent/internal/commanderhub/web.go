package commanderhub

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/index.html assets/app.js assets/style.css
var assetsFS embed.FS

// MountWeb registers GET /commander (the page) and its static assets.
func MountWeb(mux *http.ServeMux) {
	mux.HandleFunc("/commander", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/commander" {
			http.NotFound(w, r)
			return
		}
		data, err := assetsFS.ReadFile("assets/index.html")
		if err != nil {
			http.Error(w, "index unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	sub, _ := fs.Sub(assetsFS, "assets")
	fileServer := http.StripPrefix("/commander/", http.FileServer(http.FS(sub)))
	mux.Handle("/commander/app.js", fileServer)
	mux.Handle("/commander/style.css", fileServer)
}
