package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestUIServesFromRoot(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		path        string
		wantType    string
		wantContent string
	}{
		{"/", "text/html", "<title>mockapi</title>"},
		{"/index.html", "text/html", "<title>mockapi</title>"},
		{"/app.css", "text/css", "--accent"},
		{"/app.js", "javascript", "textContent"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			w := do(t, srv, http.MethodGet, tt.path, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if got := w.Header().Get("Content-Type"); !strings.Contains(got, tt.wantType) {
				t.Errorf("Content-Type = %q, want it to contain %q", got, tt.wantType)
			}
			if !strings.Contains(w.Body.String(), tt.wantContent) {
				t.Errorf("body does not contain %q", tt.wantContent)
			}
		})
	}
}

// The console cannot derive the control prefix from its own URL now that it
// lives at the root, so the server renders it into the page. A literal in the
// JS would survive a move of AdminPrefix and break the console at runtime,
// where no Go test would catch it.
func TestConsoleLearnsApiBaseFromServer(t *testing.T) {
	srv := newTestServer(t)

	index := do(t, srv, http.MethodGet, "/", "").Body.String()
	if strings.Contains(index, apiBasePlaceholder) {
		t.Errorf("served index still contains %s, want it substituted", apiBasePlaceholder)
	}
	want := `content="` + adminRoot + `"`
	if !strings.Contains(index, want) {
		t.Errorf("served index does not carry %s", want)
	}

	js := do(t, srv, http.MethodGet, "/app.js", "").Body.String()
	if !strings.Contains(js, `meta[name="api-base"]`) {
		t.Error("app.js does not read the api-base meta tag")
	}
	for _, literal := range []string{`'` + adminRoot + `'`, `"` + adminRoot + `"`} {
		if strings.Contains(js, literal) {
			t.Errorf("app.js contains the hardcoded control prefix %s; read the meta tag instead", literal)
		}
	}
}

func TestUnknownAssetIs404(t *testing.T) {
	srv := newTestServer(t)

	for _, path := range []string{"/nope.js", "/users/42", "/deep/nested/thing"} {
		w := do(t, srv, http.MethodGet, path, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "no such path") {
			t.Errorf("GET %s body = %s, want the JSON 404", path, w.Body)
		}
	}
}

// The console sits at the root, but /api/ and /cb/ are matched first.
func TestConsoleDoesNotShadowApiOrCallbacks(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, "")

	if w := do(t, srv, http.MethodGet, "/api/health", ""); w.Code != http.StatusOK {
		t.Errorf("control API status = %d, want the API to win over the console", w.Code)
	}
	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != http.StatusOK {
		t.Errorf("callback status = %d, want the endpoint to win over the console", w.Code)
	}
}

// A callback endpoint's rules live below /cb/, so nothing a user creates can
// reach the console.
func TestCallbackRulesCannotShadowConsole(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, `{"spec":{"rules":[
	  {"name":"greedy","request":{"path":"/*"},"response":{"status":500}}
	]}}`)

	if w := do(t, srv, http.MethodGet, view.URL+"/anything", ""); w.Code != 500 {
		t.Errorf("status = %d, want the rule to serve under its endpoint", w.Code)
	}
	if w := do(t, srv, http.MethodGet, "/", ""); w.Code != http.StatusOK {
		t.Errorf("status = %d, want the console unaffected", w.Code)
	}
}

// The console is embedded in the binary: it needs no files on disk beside it
// and no configuration to serve.
func TestUIServesFromABareServer(t *testing.T) {
	srv, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, srv, http.MethodGet, UIPath, ""); w.Code != http.StatusOK {
		t.Errorf("status = %d, want the console served by a freshly built server", w.Code)
	}
}
