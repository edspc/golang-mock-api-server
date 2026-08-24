package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// decode mirrors how a spec arrives over the control API.
func decode(t *testing.T, raw string) Rule {
	t.Helper()
	var s Rule
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return s
}

func TestNormalize(t *testing.T) {
	s := decode(t, `{"request":{"method":"get","path":"/users/{id}","headers":{"x-api-key":"k"}},
	                "response":{"body":{"ok":true}}}`)

	if err := s.Normalize("rule[0]", "/*"); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if s.Name != "rule[0]" {
		t.Errorf("Name = %q, want the default applied", s.Name)
	}
	if s.Request.Method != "GET" {
		t.Errorf("method = %q, want GET (uppercased)", s.Request.Method)
	}
	if _, ok := s.Request.Headers["X-Api-Key"]; !ok {
		t.Errorf("header keys = %v, want canonicalized X-Api-Key", s.Request.Headers)
	}
	if s.Response.Status != 200 {
		t.Errorf("status = %d, want the 200 default", s.Response.Status)
	}
}

func TestNormalizeDefaultsPath(t *testing.T) {
	s := decode(t, `{"response":{"status":204}}`)
	if err := s.Normalize("rule[0]", "/*"); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if s.Request.Path != "/*" {
		t.Errorf("path = %q, want the default path applied", s.Request.Path)
	}
}

func TestNormalizeErrors(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantSub string
	}{
		{"relative path", `{"request":{"path":"users"}}`, "must start with /"},
		{"bad status", `{"response":{"status":42}}`, "out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := decode(t, tt.raw)
			err := s.Normalize("rule[0]", "/*")
			if err == nil {
				t.Fatalf("Normalize() error = nil, want one containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("Normalize() error = %v, want it to contain %q", err, tt.wantSub)
			}
		})
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	var s Rule
	dec := json.NewDecoder(strings.NewReader(`{"reqeust":{"path":"/a"}}`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err == nil {
		t.Fatal("decode error = nil, want a typo to be rejected")
	}
}

func TestParseDurationForms(t *testing.T) {
	tests := []struct {
		name string
		json string
		want time.Duration
	}{
		{"string", `"1.5s"`, 1500 * time.Millisecond},
		{"milliseconds number", `250`, 250 * time.Millisecond},
		{"zero", `0`, 0},
		{"null", `null`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := decode(t, `{"response":{"delay":`+tt.json+`}}`)
			if got := s.Response.Delay.Duration(); got != tt.want {
				t.Errorf("delay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBadDuration(t *testing.T) {
	var s Rule
	err := json.Unmarshal([]byte(`{"response":{"delay":"soon"}}`), &s)
	if err == nil || !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("Unmarshal() error = %v, want an invalid duration error", err)
	}
}

// The console reads a spec back into its editor after each save, so zero
// values must not accumulate in the user's document.
func TestMarshalOmitsZeroValues(t *testing.T) {
	s := decode(t, `{"name":"ack","response":{"status":202}}`)
	if err := s.Normalize("rule[0]", "/*"); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, noise := range []string{`"delay"`, `"headers":null`, `"query"`, `"bodyContains"`, `"method"`} {
		if strings.Contains(string(out), noise) {
			t.Errorf("marshalled spec contains %s, want it omitted:\n%s", noise, out)
		}
	}
}
