package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// UIPath is where the browser console is served: the site root. /api/ and /cb/
// are matched before it, so the console cannot shadow them or be shadowed.
const UIPath = "/"

//go:embed web
var uiFiles embed.FS

// apiBasePlaceholder is substituted in index.html at startup so the console
// learns the control prefix instead of hardcoding it. The console is no longer
// served underneath AdminPrefix, so it cannot derive the base from its own URL
// — and a literal in the JS would silently break if AdminPrefix moved.
const apiBasePlaceholder = "__API_BASE__"

// uiFS is the embedded console, rooted at the web directory. The assets are
// compiled into the binary, so there is no build step and no runtime
// dependency on the working directory.
func uiFS() fs.FS {
	sub, err := fs.Sub(uiFiles, "web")
	if err != nil {
		// Unreachable: the directory is embedded at compile time.
		panic("server: embedded UI is missing: " + err.Error())
	}
	return sub
}

// indexPage is index.html with the control prefix substituted in, rendered
// once at startup.
func indexPage() []byte {
	raw, err := fs.ReadFile(uiFS(), "index.html")
	if err != nil {
		panic("server: embedded console index is missing: " + err.Error())
	}
	if !strings.Contains(string(raw), apiBasePlaceholder) {
		panic("server: console index has no " + apiBasePlaceholder + " to substitute")
	}
	return []byte(strings.ReplaceAll(string(raw), apiBasePlaceholder, adminRoot))
}

// serveUI serves the console and its assets. Anything that is not an asset is
// a 404: the service answers only on the console, the control API, and the
// callback URLs it handed out.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if name == "." || name == "index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodHead {
			if _, err := w.Write(s.index); err != nil {
				s.log.Warn("write console", "error", err)
			}
		}
		return
	}
	if _, err := fs.Stat(s.assetFS, name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":   "no such path",
			"console": UIPath,
		})
		return
	}
	s.assets.ServeHTTP(w, r)
}
