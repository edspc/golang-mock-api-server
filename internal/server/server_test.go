package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer builds a Server with an empty endpoint registry, which is
// exactly how the real command starts: endpoints are created at runtime.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv, err := New(Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return srv
}

func do(t *testing.T, srv *Server, method, target string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)

	w := do(t, srv, http.MethodGet, "/api/health", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status": "ok"`) {
		t.Errorf("body = %s, want an ok status", w.Body)
	}
}

// The root is the console; anything that is neither a console asset, a
// control route, nor a callback URL is a 404 (see TestUnknownAssetIs404).
func TestRootServesConsole(t *testing.T) {
	srv := newTestServer(t)

	w := do(t, srv, http.MethodGet, "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want the console at the root", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<title>mockapi</title>") {
		t.Errorf("body = %.80s…, want the console page", w.Body.String())
	}
}

func TestUnknownControlEndpoint(t *testing.T) {
	srv := newTestServer(t)

	w := do(t, srv, http.MethodGet, "/api/nope", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unknown control endpoint") {
		t.Errorf("body = %s, want the control endpoint listing", w.Body)
	}
}

// The bare control path belongs to the control API too, so every path in the
// reservation behaves the same way.
func TestBareAdminPathIsReserved(t *testing.T) {
	srv := newTestServer(t)

	w := do(t, srv, http.MethodGet, "/api", "")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want a redirect into the control API", w.Code)
	}
	if got := w.Header().Get("Location"); got != AdminPrefix {
		t.Errorf("Location = %q, want %q", got, AdminPrefix)
	}
}

// Only the exact prefix is reserved.
func TestAdjacentPrefixesAreNotReserved(t *testing.T) {
	srv := newTestServer(t)

	for _, path := range []string{"/apiv2/users", "/api-gateway/x"} {
		w := do(t, srv, http.MethodGet, path, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want the ordinary 404", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "unknown control endpoint") {
			t.Errorf("GET %s was handled by the control API, want it treated as an ordinary path", path)
		}
	}
}
