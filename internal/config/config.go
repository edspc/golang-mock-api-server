// Package config defines the shapes a user PUTs to configure an endpoint: the
// request matchers and responses that make up its spec. It owns validation
// and normalization for those, so nothing downstream re-derives them.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Rule is one request matcher plus the response to serve on a match.
type Rule struct {
	Name     string   `json:"name"`
	Request  Request  `json:"request"`
	Response Response `json:"response"`
}

// Request describes what an incoming request must look like to match. Every
// declared field must match; omitted fields are not constrained.
//
// The omitempty tags matter beyond tidiness: a spec is read back into the
// console's editor after every save, and emitting zero values there would grow
// the user's document with fields they never wrote.
type Request struct {
	// Method is matched case-insensitively. Empty matches any method.
	Method string `json:"method,omitempty"`
	// Path is a pattern: literal segments, {name} captures, and a trailing
	// * that captures the remainder.
	Path string `json:"path,omitempty"`
	// Query entries must all be present with the given value. A value of "*"
	// requires the parameter to be present with any value.
	Query map[string]string `json:"query,omitempty"`
	// Headers entries must all be present with the given value (header names
	// are canonicalized, values compared case-sensitively). "*" matches any.
	Headers map[string]string `json:"headers,omitempty"`
	// BodyContains requires the raw request body to contain this substring.
	BodyContains string `json:"bodyContains,omitempty"`
}

// Response is what the endpoint writes when a rule matches.
type Response struct {
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Body is embedded JSON, written verbatim after template rendering.
	Body json.RawMessage `json:"body,omitempty"`
	// Delay is how long to wait before writing the response.
	Delay Duration `json:"delay,omitempty"`
}

// Duration accepts either a duration string ("250ms") or a number of
// milliseconds in JSON.
type Duration time.Duration

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var ms float64
	if err := json.Unmarshal(b, &ms); err != nil {
		return errors.New("duration must be a string like \"250ms\" or a number of milliseconds")
	}
	*d = Duration(time.Duration(ms) * time.Millisecond)
	return nil
}

// MarshalJSON implements json.Marshaler so a spec round-trips readably.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Normalize validates and canonicalizes a rule in place. defaultName is used
// when the rule is unnamed; defaultPath is used when it declares no path.
func (s *Rule) Normalize(defaultName, defaultPath string) error {
	if s.Name == "" {
		s.Name = defaultName
	}
	if s.Request.Path == "" {
		s.Request.Path = defaultPath
	}
	if !strings.HasPrefix(s.Request.Path, "/") {
		return fmt.Errorf("%s: request.path %q must start with /", s.Name, s.Request.Path)
	}
	s.Request.Method = strings.ToUpper(s.Request.Method)
	s.Request.Headers = canonicalizeHeaders(s.Request.Headers)
	return s.Response.Normalize(s.Name)
}

// Normalize defaults and validates a response in place.
func (r *Response) Normalize(name string) error {
	if r.Status == 0 {
		r.Status = http.StatusOK
	}
	if r.Status < 100 || r.Status > 599 {
		return fmt.Errorf("%s: response.status %d is out of range", name, r.Status)
	}
	return nil
}

func canonicalizeHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[http.CanonicalHeaderKey(k)] = v
	}
	return out
}
