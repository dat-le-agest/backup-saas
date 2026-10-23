// Package web serves the admin dashboard: the React app in admin-dashboard/
// (repo root), built into dist/ by `make admin-web` and embedded in the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var files embed.FS

// Handler serves the built dashboard; unknown paths get index.html (client-side
// routes). ok is false when the dashboard wasn't built (dist/index.html missing).
func Handler() (h http.Handler, ok bool) {
	dist, _ := fs.Sub(files, "dist")
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, false
	}
	static := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if f, err := fs.Stat(dist, name); name != "" && err == nil && !f.IsDir() {
			if strings.HasPrefix(name, "assets/") { // hashed file names
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			static.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK) // the router may have preset 404 for unknown routes
		_, _ = w.Write(index)
	}), true
}
