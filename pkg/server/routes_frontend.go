package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// registerFrontendRoutes serves the embedded frontend SPA.
func registerFrontendRoutes(r chi.Router) error {
	// Serve embedded frontend
	sub, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		return fmt.Errorf("frontend fs: %w", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		f, err := sub.Open(r.URL.Path[1:])
		if err != nil {
			r.URL.Path = "/"
		} else {
			f.Close()
		}
		// Content-hashed assets are immutable; everything else (index.html, SPA
		// fallback) must revalidate so a rebuilt binary's new asset hashes are
		// picked up instead of a stale cached index.html 404ing the old ones.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
	return nil
}
