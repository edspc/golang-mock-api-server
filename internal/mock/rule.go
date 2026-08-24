// Package mock holds the request-matching, recording, and response-rendering
// pieces an endpoint uses to answer the traffic it captures.
package mock

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/edspc/golang-mock-api-server/internal/config"
)

// AnyValue in a query or header matcher requires the key to be present with
// any value.
const AnyValue = "*"

// Rule is a request matcher with its path pattern compiled.
type Rule struct {
	Def     config.Rule
	pattern *Pattern
}

// NewRule compiles def's path pattern.
func NewRule(def config.Rule) (*Rule, error) {
	p, err := Compile(def.Request.Path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", def.Name, err)
	}
	return &Rule{Def: def, pattern: p}, nil
}

// MatchPath reports whether r (whose body has already been read into body)
// satisfies every constraint the rule declares, and returns the path captures.
// path is the request path below the endpoint's own URL, so a rule's pattern
// describes the callback sub-path rather than the whole request.
func (s *Rule) MatchPath(r *http.Request, path string, body []byte) (map[string]string, bool) {
	if m := s.Def.Request.Method; m != "" && !strings.EqualFold(m, r.Method) {
		return nil, false
	}
	params, ok := s.pattern.Match(path)
	if !ok {
		return nil, false
	}
	query := r.URL.Query()
	for k, want := range s.Def.Request.Query {
		got, present := query[k]
		if !present {
			return nil, false
		}
		if want != AnyValue && (len(got) == 0 || got[0] != want) {
			return nil, false
		}
	}
	for k, want := range s.Def.Request.Headers {
		got := r.Header.Values(k)
		if len(got) == 0 {
			return nil, false
		}
		if want != AnyValue && !containsValue(got, want) {
			return nil, false
		}
	}
	if sub := s.Def.Request.BodyContains; sub != "" && !strings.Contains(string(body), sub) {
		return nil, false
	}
	return params, true
}

func containsValue(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
