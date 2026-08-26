package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func enabledAuth(t *testing.T, baseURL string) *Auth {
	t.Helper()
	a, err := New(Config{
		ClientID:      "id",
		ClientSecret:  "secret",
		BaseURL:       baseURL,
		AllowedEmails: []string{"me@edspc.dev"},
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a.Routes(http.NewServeMux(), "/api/")
	return a
}

// The app can be mounted under a sub-path, where the browser asks for
// /mockapi/api/auth/callback while the server only ever sees
// /api/auth/callback. An absolute "/" would drop the user on the domain root;
// the relative form lands on the console from either mount.
func TestSignInRedirectIsRelativeToTheCallback(t *testing.T) {
	a := enabledAuth(t, "https://mock.example.com")

	if got, want := a.consolePath(), "../../"; got != want {
		t.Fatalf("consolePath() = %q, want %q", got, want)
	}

	ref, err := url.Parse(a.consolePath())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		browserPath string
		want        string
	}{
		{"/api/auth/callback", "/"},
		{"/mockapi/api/auth/callback", "/mockapi/"},
		{"/deep/mount/api/auth/callback", "/deep/mount/"},
	}
	for _, tt := range tests {
		t.Run(tt.browserPath, func(t *testing.T) {
			base, err := url.Parse("https://mock.example.com" + tt.browserPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := base.ResolveReference(ref).Path; got != tt.want {
				t.Errorf("got = %s, want %s", got, tt.want)
			}
		})
	}
}

// The redirect URI is the one thing that cannot be relative — Google needs an
// absolute URL — so it is built from the configured base, sub-path and all.
func TestRedirectURIKeepsTheConfiguredSubPath(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{"https://mock.example.com", "https://mock.example.com/api/auth/callback"},
		{"https://mock.example.com/", "https://mock.example.com/api/auth/callback"},
		{"https://mock.example.com/mockapi", "https://mock.example.com/mockapi/api/auth/callback"},
		{"https://mock.example.com/mockapi/", "https://mock.example.com/mockapi/api/auth/callback"},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			if got := enabledAuth(t, tt.base).redirectURI(); got != tt.want {
				t.Errorf("got = %s, want %s", got, tt.want)
			}
		})
	}
}

// Guard points an unauthenticated caller at the login route. It is a
// server-root path, which the console rebases onto its own mount.
func TestUnauthorizedResponseNamesTheLoginRoute(t *testing.T) {
	a := enabledAuth(t, "https://mock.example.com")
	w := httptest.NewRecorder()
	a.Guard("/api/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("Guard let an unauthenticated request through")
	})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/endpoints", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/api/auth/login") {
		t.Errorf("body = %s, want the login route", w.Body)
	}
}
