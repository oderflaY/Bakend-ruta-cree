package httpx

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// panelCSP loosens SecurityHeaders' API policy just enough for the control panel: its own scripts,
// Google Fonts, map tiles (Mapbox, or Esri without a token) and API/WebSocket calls to this same
// origin. MapLibre downloads tiles with fetch (connect-src) and decodes them as blob: images.
const panelCSP = "default-src 'self'; script-src 'self'; " +
	"connect-src 'self' https://api.mapbox.com https://server.arcgisonline.com; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; " +
	"img-src 'self' data: blob: https://api.mapbox.com https://server.arcgisonline.com; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Static serves the files of dir (no directory listings, no dotfiles). Revalidated on every load so
// a deploy reaches browsers at once.
func Static(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		for _, part := range strings.Split(p, "/") {
			if strings.HasPrefix(part, ".") {
				WriteError(w, r, ErrNotFound)
				return
			}
		}
		// A folder is served only through its index.html: never as a file listing.
		file := p
		if file == "" || strings.HasSuffix(file, "/") {
			file += "index.html"
		}
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(file))); err != nil || st.IsDir() {
			WriteError(w, r, ErrNotFound)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", panelCSP)
		h.Set("Cache-Control", "no-cache")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		files.ServeHTTP(w, r)
	})
}
