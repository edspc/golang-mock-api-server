// Package server exposes the endpoint registry over HTTP: the control API
// under /api/, the callback endpoints under /cb/, and the console at the root.
package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/edspc/golang-mock-api-server/internal/auth"
	"github.com/edspc/golang-mock-api-server/internal/endpoint"
)

// AdminPrefix is reserved for the control API, so a callback endpoint's own
// traffic can never collide with it.
//
// Every control route is built from this constant rather than spelling the
// prefix out, so moving it stays a one-line change.
const AdminPrefix = "/api/"

// adminRoot is AdminPrefix without its trailing slash. The bare path belongs
// to the control API too, so that every path in the reservation behaves the
// same way.
var adminRoot = strings.TrimSuffix(AdminPrefix, "/")

// isAdminPath reports whether p is handled by the control API.
func isAdminPath(p string) bool {
	return p == adminRoot || strings.HasPrefix(p, AdminPrefix)
}

// MaxBodyBytes caps how much of a request body is read for matching and
// recording.
const MaxBodyBytes = 1 << 20

// Options configures a Server.
type Options struct {
	// Endpoints is the callback-endpoint registry. Nil creates an empty one.
	Endpoints *endpoint.Registry
	Logger    *slog.Logger
	// Auth guards the control API. Nil leaves it open, which is the default.
	Auth *auth.Auth
}

// Server implements http.Handler. It serves three disjoint path spaces: /api/
// is the control API, /cb/ is the callback endpoints created through it, and
// everything else is the console. Nothing else is served — this is not a mock
// host.
type Server struct {
	endpoints *endpoint.Registry
	log       *slog.Logger
	auth      *auth.Auth
	admin     http.Handler
	assets    http.Handler
	assetFS   fs.FS
	index     []byte
}

// New builds a Server.
func New(opts Options) (*Server, error) {
	s := &Server{
		endpoints: opts.Endpoints,
		log:       opts.Logger,
		auth:      opts.Auth,
	}
	if s.endpoints == nil {
		s.endpoints = endpoint.NewRegistry(endpoint.DefaultHistory)
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s.assetFS = uiFS()
	s.assets = http.FileServerFS(s.assetFS)
	s.index = indexPage()

	// Only the control API is guarded. Callback traffic is dispatched in
	// ServeHTTP before this handler and never passes through the guard: a
	// third party posting to /cb/{id} cannot sign in, and must not have to.
	s.admin = s.auth.Guard(AdminPrefix, s.adminMux())
	return s, nil
}

// ServeHTTP routes control traffic to the admin mux and callback traffic to
// the endpoint registry, both matched before the console at the root, so
// neither can be shadowed by it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case isAdminPath(r.URL.Path):
		// ServeMux redirects a bare /api to /api/ for us.
		s.admin.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, CallbackPrefix):
		s.serveCallback(w, r)
	default:
		s.serveUI(w, r)
	}
}

// sleep waits for d, reporting false if the client disconnected first.
func sleep(r *http.Request, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
