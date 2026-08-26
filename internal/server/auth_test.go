package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/edspc/golang-mock-api-server/internal/auth"
	"github.com/edspc/golang-mock-api-server/internal/endpoint"
)

// guardedServer returns a server whose control API needs a session, plus one
// endpoint created before the guard went up.
func guardedServer(t *testing.T) (*Server, endpointView) {
	t.Helper()

	open := newTestServer(t)
	view := createEndpoint(t, open, `{"spec":{"rules":[{"name":"ack","response":{"status":202}}]}}`)

	guard, err := auth.New(auth.Config{
		ClientID:      "id",
		ClientSecret:  "secret",
		BaseURL:       "https://mock.example.com",
		AllowedEmails: []string{"me@edspc.dev"},
	}, nil)
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	if !guard.Enabled() {
		t.Fatal("guard is not enabled with a full configuration")
	}

	srv, err := New(Options{Endpoints: open.endpoints, Auth: guard})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return srv, view
}

// The whole point of the split: a third party posting to a callback URL cannot
// sign in, so callbacks must never be guarded.
func TestCallbacksStayOpenWhenSignInIsRequired(t *testing.T) {
	srv, view := guardedServer(t)

	w := do(t, srv, http.MethodPost, view.URL, `{"event":"paid"}`)
	if w.Code != 202 {
		t.Errorf("callback status = %d, want the endpoint's own 202", w.Code)
	}

	// And it was really captured, not just answered.
	sub := do(t, srv, http.MethodPost, view.URL+"/anything", "")
	if sub.Code != 202 {
		t.Errorf("sub-path callback status = %d, want 202", sub.Code)
	}
}

func TestControlAPIRequiresSignIn(t *testing.T) {
	srv, view := guardedServer(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/endpoints"},
		{http.MethodPost, "/api/endpoints"},
		{http.MethodGet, "/api/endpoints/" + view.ID},
		{http.MethodGet, "/api/endpoints/" + view.ID + "/requests"},
		{http.MethodPut, "/api/endpoints/" + view.ID + "/spec"},
		{http.MethodPut, "/api/endpoints/" + view.ID + "/name"},
		{http.MethodDelete, "/api/endpoints/" + view.ID},
		{http.MethodGet, "/api/health"},
		// The stream carries captured traffic like any other control route.
		{http.MethodGet, "/api/events"},
	} {
		w := do(t, srv, tc.method, tc.path, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", tc.method, tc.path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "/api/auth/login") {
			t.Errorf("%s %s body = %s, want it to point at the login route", tc.method, tc.path, w.Body)
		}
	}
}

// The console has to be able to ask whether anyone is signed in, and to start
// the flow, without already being signed in.
func TestAuthRoutesAreReachableWithoutASession(t *testing.T) {
	srv, _ := guardedServer(t)

	status := do(t, srv, http.MethodGet, "/api/auth/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", status.Code)
	}
	if !strings.Contains(status.Body.String(), `"enabled": true`) ||
		!strings.Contains(status.Body.String(), `"authenticated": false`) {
		t.Errorf("body = %s, want enabled and not authenticated", status.Body)
	}

	login := do(t, srv, http.MethodGet, "/api/auth/login", "")
	if login.Code != http.StatusFound {
		t.Fatalf("login status = %d, want a redirect to Google", login.Code)
	}
	location := login.Header().Get("Location")
	for _, want := range []string{
		"accounts.google.com",
		"client_id=id",
		"redirect_uri=https%3A%2F%2Fmock.example.com%2Fapi%2Fauth%2Fcallback",
		"response_type=code",
		"state=",
	} {
		if !strings.Contains(location, want) {
			t.Errorf("redirect %q is missing %q", location, want)
		}
	}
	if cookies := login.Result().Cookies(); len(cookies) == 0 || cookies[0].Name != "mockapi_oauth_state" {
		t.Error("login did not set the state cookie that guards the callback against forgery")
	}
}

// A callback without the matching state cookie must not sign anyone in.
func TestCallbackRejectsForgedState(t *testing.T) {
	srv, _ := guardedServer(t)

	w := do(t, srv, http.MethodGet, "/api/auth/callback?code=stolen&state=guessed", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a callback with no state cookie", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "mockapi_session" && c.Value != "" {
			t.Error("a forged callback set a session cookie")
		}
	}
}

// With no client id and secret the guard stays dormant, which is the default
// the service ships with.
func TestSignInDisabledByDefault(t *testing.T) {
	guard, err := auth.New(auth.Config{}, nil)
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	if guard.Enabled() {
		t.Fatal("guard is enabled without any configuration")
	}

	srv, err := New(Options{Endpoints: endpoint.NewRegistry(10), Auth: guard})
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, srv, http.MethodGet, "/api/endpoints", ""); w.Code != http.StatusOK {
		t.Errorf("status = %d, want the control API open when sign-in is unconfigured", w.Code)
	}
	if w := do(t, srv, http.MethodGet, "/api/auth/status", ""); !strings.Contains(w.Body.String(), `"enabled": false`) {
		t.Errorf("status body = %s, want enabled:false so the console skips the login screen", w.Body)
	}
}

func TestAuthConfigRefusesUnsafeSetups(t *testing.T) {
	tests := []struct {
		name    string
		cfg     auth.Config
		wantSub string
	}{
		{
			name:    "no allowlist would let in any google account",
			cfg:     auth.Config{ClientID: "id", ClientSecret: "s", BaseURL: "https://x"},
			wantSub: "allowed email list or domain",
		},
		{
			name:    "secret without id",
			cfg:     auth.Config{ClientSecret: "s"},
			wantSub: "client id and a client secret",
		},
		{
			name:    "no base URL to build the redirect",
			cfg:     auth.Config{ClientID: "id", ClientSecret: "s", AllowedDomain: "edspc.dev"},
			wantSub: "base URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := auth.New(tt.cfg, nil)
			if err == nil {
				t.Fatalf("auth.New() error = nil, want one containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("auth.New() error = %v, want it to contain %q", err, tt.wantSub)
			}
		})
	}
}

func TestAllowlist(t *testing.T) {
	guard, err := auth.New(auth.Config{
		ClientID: "id", ClientSecret: "s", BaseURL: "https://x",
		AllowedEmails: []string{"Me@Edspc.dev"},
		AllowedDomain: "@example.com",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		email string
		want  bool
	}{
		{"me@edspc.dev", true},       // listed, case-insensitively
		{"ME@EDSPC.DEV", true},       //
		{"anyone@example.com", true}, // allowed domain
		{"someone@else.com", false},  // neither
		{"me@edspc.dev.evil.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := guard.Allows(tt.email); got != tt.want {
			t.Errorf("Allows(%q) = %v, want %v", tt.email, got, tt.want)
		}
	}
}
