package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edspc/golang-mock-api-server/internal/config"
)

// mustRule builds a compiled rule matcher the way an endpoint spec would.
func mustRule(t *testing.T, raw string) *Rule {
	t.Helper()
	var def config.Rule
	if err := json.Unmarshal([]byte(raw), &def); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := def.Normalize("rule[0]", "/*"); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	s, err := NewRule(def)
	if err != nil {
		t.Fatalf("NewRule() error = %v", err)
	}
	return s
}

func TestMatchPathConstraints(t *testing.T) {
	rule := mustRule(t, `{
	  "name":"guarded",
	  "request":{"method":"POST","path":"/users/{id}","query":{"force":"true","trace":"*"},
	             "headers":{"X-Api-Key":"secret"},"bodyContains":"\"role\":\"admin\""}
	}`)

	newReq := func(target string, headers map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, target, nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}
	okHeaders := map[string]string{"X-Api-Key": "secret"}
	okTarget := "/cb/x/users/7?force=true&trace=abc"
	okSub := "/users/7"
	okBody := []byte(`{"role":"admin"}`)

	t.Run("all constraints satisfied", func(t *testing.T) {
		params, ok := rule.MatchPath(newReq(okTarget, okHeaders), okSub, okBody)
		if !ok {
			t.Fatal("MatchPath() ok = false, want a match")
		}
		if params["id"] != "7" {
			t.Errorf("params = %v, want id=7", params)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, okTarget, nil)
		r.Header.Set("X-Api-Key", "secret")
		if _, ok := rule.MatchPath(r, okSub, okBody); ok {
			t.Error("MatchPath() ok = true, want no match on a different method")
		}
	})

	t.Run("missing wildcard query param", func(t *testing.T) {
		if _, ok := rule.MatchPath(newReq("/cb/x/users/7?force=true", okHeaders), okSub, okBody); ok {
			t.Error("MatchPath() ok = true, want no match when a \"*\" query param is absent")
		}
	})

	t.Run("wrong header value", func(t *testing.T) {
		bad := map[string]string{"X-Api-Key": "nope"}
		if _, ok := rule.MatchPath(newReq(okTarget, bad), okSub, okBody); ok {
			t.Error("MatchPath() ok = true, want no match on a wrong header value")
		}
	})

	t.Run("body substring absent", func(t *testing.T) {
		if _, ok := rule.MatchPath(newReq(okTarget, okHeaders), okSub, []byte(`{"role":"user"}`)); ok {
			t.Error("MatchPath() ok = true, want no match when bodyContains is absent")
		}
	})
}

// A rule matches the path below the endpoint URL, not the whole request path.
func TestMatchPathUsesSubPath(t *testing.T) {
	rule := mustRule(t, `{"request":{"path":"/success"}}`)

	r := httptest.NewRequest(http.MethodPost, "/cb/0192f0c8-3f4e-8a1b-9c2d-4e5f60718293/success", nil)
	if _, ok := rule.MatchPath(r, "/success", nil); !ok {
		t.Error("MatchPath() ok = false, want the rule to match its sub-path")
	}
	if _, ok := rule.MatchPath(r, r.URL.Path, nil); ok {
		t.Error("MatchPath() ok = true for the full path, want rules scoped to the sub-path")
	}
}

func TestEmptyMethodMatchesAnything(t *testing.T) {
	rule := mustRule(t, `{"request":{"path":"/ping"}}`)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		r := httptest.NewRequest(method, "/cb/x/ping", nil)
		if _, ok := rule.MatchPath(r, "/ping", nil); !ok {
			t.Errorf("MatchPath() ok = false for %s, want a match when method is unset", method)
		}
	}
}

func TestNewRuleRejectsBadPattern(t *testing.T) {
	var def config.Rule
	if err := json.Unmarshal([]byte(`{"name":"bad","request":{"path":"/a/*/b"}}`), &def); err != nil {
		t.Fatal(err)
	}
	_, err := NewRule(def)
	if err == nil {
		t.Fatal("NewRule() error = nil, want a malformed pattern to be rejected")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("NewRule() error = %v, want it to name the rule", err)
	}
}
