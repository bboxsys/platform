package server

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// hasAssetExt reports whether name looks like a static bundle asset rather
// than a client-side route, so a missing one 404s instead of falling back
// to index.html.
func hasAssetExt(name string) bool {
	switch path.Ext(name) {
	case ".js", ".mjs", ".css", ".map", ".json",
		".woff", ".woff2", ".ttf", ".eot", ".otf",
		".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico":
		return true
	}
	return false
}

// SPAHandler serves a single-page application from fsys: static files with
// long-lived caching for hashed assets, index.html as the fallback for
// client-side routes, and precompressed variants (file.br, file.gz) when
// the build produced them and the client accepts the encoding.
func SPAHandler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		// Never serve compressed variants directly; they exist only as
		// encodings of their base file.
		if strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".br") {
			http.NotFound(w, r)
			return
		}
		if _, err := fs.Stat(fsys, name); err != nil {
			// A missing asset-like request (.js/.css/font/image) must NOT
			// fall back to index.html: a stale client asking for an old
			// chunk hash would receive HTML, try to execute it as a script,
			// and blank the page with a ChunkLoadError. 404 instead so the
			// browser raises a real load error and the app can reload.
			if hasAssetExt(name) {
				http.NotFound(w, r)
				return
			}
			// SPA fallback: unknown paths get index.html so client-side
			// routing resolves them.
			name = "index.html"
			if _, err := fs.Stat(fsys, name); err != nil {
				http.Error(w, "index.html not found", http.StatusInternalServerError)
				return
			}
		}

		ctype := mime.TypeByExtension(path.Ext(name))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ctype)
		// Vite emits content-hashed filenames under assets/; everything
		// else (index.html, favicons) must revalidate.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("Vary", "Accept-Encoding")

		accept := r.Header.Get("Accept-Encoding")
		for _, enc := range []struct{ coding, ext string }{{"br", ".br"}, {"gzip", ".gz"}} {
			if !strings.Contains(accept, enc.coding) {
				continue
			}
			if f, err := fsys.Open(name + enc.ext); err == nil {
				defer f.Close()
				w.Header().Set("Content-Encoding", enc.coding)
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					io.Copy(w, f)
				}
				return
			}
		}

		f, err := fsys.Open(name)
		if err != nil {
			http.Error(w, "error opening file", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			io.Copy(w, f)
		}
	})
}
