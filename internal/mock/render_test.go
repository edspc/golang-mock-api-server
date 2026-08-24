package mock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/users/7?expand=posts", nil)
	r.Header.Set("X-Trace-Id", "abc123")
	data := NewRenderData(r, map[string]string{"id": "7"})

	tests := []struct {
		name string
		body string
		want string
	}{
		{"path capture", `{"id":"{{.Path.id}}"}`, `{"id":"7"}`},
		{"query value", `{"q":"{{.Query.expand}}"}`, `{"q":"posts"}`},
		{"header value", `{"t":"{{index .Header "X-Trace-Id"}}"}`, `{"t":"abc123"}`},
		{"dash-free header alias", `{"t":"{{.Header.XTraceId}}"}`, `{"t":"abc123"}`},
		{"no template", `{"static":true}`, `{"static":true}`},
		{"missing key renders empty", `{"x":"{{.Path.nope}}"}`, `{"x":""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render([]byte(tt.body), data)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Render() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRenderWildcardAlias(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/things", nil)
	data := NewRenderData(r, map[string]string{"*": "v1/things"})

	got, err := Render([]byte(`{"echo":"{{.Path.rest}}"}`), data)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if string(got) != `{"echo":"v1/things"}` {
		t.Errorf("Render() = %s, want the wildcard alias expanded", got)
	}
}

func TestNewRenderDataDoesNotMutateParams(t *testing.T) {
	params := map[string]string{"*": "a/b"}
	NewRenderData(httptest.NewRequest(http.MethodGet, "/a/b", nil), params)
	if len(params) != 1 {
		t.Errorf("params = %v, want the caller's map left alone", params)
	}
}

func TestRenderNowFunc(t *testing.T) {
	got, err := Render([]byte(`{"at":"{{now}}"}`), RenderData{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(string(got), "T") || strings.Contains(string(got), "{{") {
		t.Errorf("Render() = %s, want an expanded RFC3339 timestamp", got)
	}
}

func TestRenderBadTemplate(t *testing.T) {
	if _, err := Render([]byte(`{{ if }}`), RenderData{}); err == nil {
		t.Fatal("Render() error = nil, want a parse error")
	}
}
