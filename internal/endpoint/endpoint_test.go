package endpoint

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mustSpec(t *testing.T, raw string) Spec {
	t.Helper()
	var spec Spec
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	return spec
}

func testEndpoint(t *testing.T, rawSpec string) *Endpoint {
	t.Helper()
	e, err := New("", "test", 10)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if rawSpec != "" {
		if err := e.SetSpec(mustSpec(t, rawSpec)); err != nil {
			t.Fatalf("SetSpec() error = %v", err)
		}
	}
	return e
}

func post(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestNewEndpointHasV8ID(t *testing.T) {
	e := testEndpoint(t, "")
	if !e.ID.IsV8() {
		t.Errorf("ID %s is not a v8 UUID", e.ID)
	}
	if e.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want the timestamp encoded in the ID")
	}
}

func TestDefaultResponseAcknowledges(t *testing.T) {
	e := testEndpoint(t, "")

	out := e.Handle(post("/cb/x", `{"a":1}`), "/", []byte(`{"a":1}`))

	if out.Response.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", out.Response.Status)
	}
	if out.Rule != "" {
		t.Errorf("rule = %q, want no rule matched", out.Rule)
	}
	if got := string(out.Response.Body); !strings.Contains(got, "received") {
		t.Errorf("body = %s, want the default acknowledgement", got)
	}
	if e.Received() != 1 {
		t.Errorf("Received() = %d, want 1", e.Received())
	}
	if got := e.Requests(); len(got) != 1 || got[0].Body != `{"a":1}` {
		t.Errorf("Requests() = %+v, want the captured callback", got)
	}
}

func TestRulesMatchInOrder(t *testing.T) {
	e := testEndpoint(t, `{
	  "rules": [
	    {"name":"paid","request":{"method":"POST","bodyContains":"\"type\":\"paid\""},
	     "response":{"status":202,"body":{"handled":"paid"}}},
	    {"name":"catch-all","response":{"status":200,"body":{"handled":"other"}}}
	  ]
	}`)

	paid := e.Handle(post("/cb/x", ""), "/", []byte(`{"type":"paid"}`))
	if paid.Rule != "paid" || paid.Response.Status != 202 {
		t.Errorf("rule = %q status = %d, want paid/202", paid.Rule, paid.Response.Status)
	}

	other := e.Handle(post("/cb/x", ""), "/", []byte(`{"type":"refund"}`))
	if other.Rule != "catch-all" {
		t.Errorf("rule = %q, want the catch-all", other.Rule)
	}
}

func TestRulePathMatchesSubPath(t *testing.T) {
	e := testEndpoint(t, `{
	  "rules": [
	    {"name":"success","request":{"path":"/success"},"response":{"status":200}},
	    {"name":"failure","request":{"path":"/failure"},"response":{"status":500}}
	  ]
	}`)

	if out := e.Handle(post("/cb/x/success", ""), "/success", nil); out.Rule != "success" {
		t.Errorf("rule = %q, want success", out.Rule)
	}
	if out := e.Handle(post("/cb/x/failure", ""), "/failure", nil); out.Rule != "failure" {
		t.Errorf("rule = %q, want failure", out.Rule)
	}
	// No rule matches the endpoint root, so the default response answers.
	if out := e.Handle(post("/cb/x", ""), "/", nil); out.Rule != "" {
		t.Errorf("rule = %q, want no match at the endpoint root", out.Rule)
	}
}

func TestRuleWithoutPathMatchesAnySubPath(t *testing.T) {
	e := testEndpoint(t, `{"rules":[{"name":"any","response":{"status":204}}]}`)

	for _, sub := range []string{"/", "/a", "/a/b/c"} {
		out := e.Handle(post("/cb/x"+sub, ""), sub, nil)
		if out.Rule != "any" {
			t.Errorf("sub-path %q matched %q, want the path-less rule", sub, out.Rule)
		}
	}
}

func TestValidationCollectsAllErrors(t *testing.T) {
	e := testEndpoint(t, `{
	  "validation": {
	    "requireHeaders": ["X-Signature", "X-Env: prod"],
	    "requireQuery": ["source"],
	    "requireFields": ["event", "data.id"]
	  }
	}`)

	r := post("/cb/x", "")
	r.Header.Set("X-Env", "staging")
	out := e.Handle(r, "/", []byte(`{"data":{}}`))

	if out.Response.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", out.Response.Status)
	}
	want := []string{
		"missing header X-Signature",
		`header X-Env = "staging", want "prod"`,
		"missing query parameter source",
		"missing field event",
		"missing field data.id",
	}
	if len(out.ValidationErrors) != len(want) {
		t.Fatalf("errors = %q, want %d entries", out.ValidationErrors, len(want))
	}
	for i, w := range want {
		if out.ValidationErrors[i] != w {
			t.Errorf("error[%d] = %q, want %q", i, out.ValidationErrors[i], w)
		}
	}
	if got := e.Requests(); len(got) != 1 || len(got[0].ValidationErrors) != len(want) {
		t.Error("an invalid request must still be recorded, with its errors attached")
	}
}

func TestValidationPasses(t *testing.T) {
	e := testEndpoint(t, `{
	  "validation": {"requireHeaders":["X-Signature"],"requireFields":["event"]},
	  "rules": [{"name":"ok","response":{"status":200}}]
	}`)

	r := post("/cb/x?source=stripe", "")
	r.Header.Set("X-Signature", "abc")
	out := e.Handle(r, "/", []byte(`{"event":"charge.succeeded"}`))

	if len(out.ValidationErrors) != 0 {
		t.Fatalf("errors = %q, want none", out.ValidationErrors)
	}
	if out.Rule != "ok" {
		t.Errorf("rule = %q, want the rule to run after validation passes", out.Rule)
	}
}

func TestValidationOnFailureResponse(t *testing.T) {
	e := testEndpoint(t, `{
	  "validation": {
	    "jsonBody": true,
	    "onFailure": {"status": 422, "body": {"error":"bad payload"}}
	  }
	}`)

	out := e.Handle(post("/cb/x", ""), "/", []byte(`not json`))

	if out.Response.Status != 422 {
		t.Errorf("status = %d, want the configured 422", out.Response.Status)
	}
	if got := string(out.Response.Body); !strings.Contains(got, "bad payload") {
		t.Errorf("body = %s, want the configured failure body", got)
	}
	if len(out.ValidationErrors) != 1 || !strings.Contains(out.ValidationErrors[0], "not valid JSON") {
		t.Errorf("errors = %q, want a JSON parse error", out.ValidationErrors)
	}
}

func TestValidationSkipsRules(t *testing.T) {
	e := testEndpoint(t, `{
	  "validation": {"requireHeaders":["X-Signature"]},
	  "rules": [{"name":"never","response":{"status":200}}]
	}`)

	out := e.Handle(post("/cb/x", ""), "/", nil)
	if out.Rule != "" {
		t.Errorf("rule = %q, want rules skipped when validation fails", out.Rule)
	}
}

func TestSetSpecRejectsInvalidAndKeepsPrevious(t *testing.T) {
	e := testEndpoint(t, `{"rules":[{"name":"v1","response":{"status":201}}]}`)

	err := e.SetSpec(mustSpec(t, `{"rules":[{"name":"bad","request":{"path":"/a/*/b"}}]}`))
	if err == nil {
		t.Fatal("SetSpec() error = nil, want a rejection for a malformed path pattern")
	}
	if out := e.Handle(post("/cb/x", ""), "/", nil); out.Rule != "v1" || out.Response.Status != 201 {
		t.Errorf("rule = %q status = %d, want the previous spec still serving", out.Rule, out.Response.Status)
	}
}

// A response body is data the user supplies, never a path the server reads.
// There is no bodyFile field, so asking for one fails at decode.
func TestSpecCannotReadServerFiles(t *testing.T) {
	var spec Spec
	dec := json.NewDecoder(strings.NewReader(`{"rules":[{"response":{"bodyFile":"/etc/passwd"}}]}`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err == nil {
		t.Fatal("decode error = nil, want a spec cannot name a file on the server")
	}
}

func TestSetSpecReplacesWholesale(t *testing.T) {
	e := testEndpoint(t, `{"rules":[{"name":"old","response":{"status":201}}]}`)
	if err := e.SetSpec(Spec{}); err != nil {
		t.Fatalf("SetSpec() error = %v", err)
	}
	if out := e.Handle(post("/cb/x", ""), "/", nil); out.Rule != "" {
		t.Errorf("rule = %q, want an empty spec to drop the old rules", out.Rule)
	}
}

func TestResetRequestsKeepsCounter(t *testing.T) {
	e := testEndpoint(t, "")
	e.Handle(post("/cb/x", ""), "/", nil)
	e.Handle(post("/cb/x", ""), "/", nil)
	e.ResetRequests()

	if got := e.Requests(); len(got) != 0 {
		t.Errorf("Requests() = %+v, want empty after reset", got)
	}
	if e.Received() != 2 {
		t.Errorf("Received() = %d, want the lifetime count preserved", e.Received())
	}
}

func TestLookupField(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"a":{"b":{"c":1}},"n":null}`), &v); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{"a", true},
		{"a.b", true},
		{"a.b.c", true},
		{"n", true},
		{"a.b.c.d", false},
		{"a.x", false},
		{"missing", false},
	}
	for _, tt := range tests {
		if _, ok := lookupField(v, tt.path); ok != tt.want {
			t.Errorf("lookupField(%q) ok = %v, want %v", tt.path, ok, tt.want)
		}
	}
}

func TestResponseNormalizationAppliesToRules(t *testing.T) {
	e := testEndpoint(t, `{"rules":[{"name":"defaulted","response":{"body":{"ok":true}}}]}`)
	out := e.Handle(post("/cb/x", ""), "/", nil)
	if out.Response.Status != http.StatusOK {
		t.Errorf("status = %d, want the 200 default applied to rule responses", out.Response.Status)
	}
}
