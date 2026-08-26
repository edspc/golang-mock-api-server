package server

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/edspc/golang-mock-api-server/internal/mock"
)

// requestFilter narrows a captured request listing. Every field is optional
// and the zero value matches everything, so an unfiltered listing costs
// nothing.
type requestFilter struct {
	invalid   bool
	method    string // uppercased; empty means any
	status    int    // an exact code; 0 means any
	class     int    // a status class from "4xx"; 0 means any
	requestID string // one captured request, by the id it was answered with
}

// filterParams are the query keys the listing understands. Anything else is
// rejected rather than ignored: a mistyped filter that silently returns
// everything is the same trap as a mistyped spec key that never matches.
var filterParams = map[string]bool{"invalid": true, "method": true, "status": true, "requestId": true}

func parseRequestFilter(q url.Values) (requestFilter, error) {
	var f requestFilter

	var unknown []string
	for key := range q {
		if !filterParams[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return f, fmt.Errorf("unknown filter %s; supported: invalid, method, requestId, status",
			strings.Join(unknown, ", "))
	}

	if raw := q.Get("invalid"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return f, fmt.Errorf("invalid must be true or false, got %q", raw)
		}
		f.invalid = v
	}

	// Methods are compared against what was captured, which config already
	// uppercases everywhere else.
	f.method = strings.ToUpper(strings.TrimSpace(q.Get("method")))

	// An id is quoted back from a response header or a log line, so it is
	// matched whole and without caring about case. An id that matches nothing
	// is an empty listing, not an error: it may simply have aged out of the
	// endpoint's bounded history.
	f.requestID = strings.TrimSpace(q.Get("requestId"))

	raw := strings.TrimSpace(q.Get("status"))
	switch lower := strings.ToLower(raw); {
	case lower == "":
	case len(lower) == 3 && strings.HasSuffix(lower, "xx") && lower[0] >= '1' && lower[0] <= '5':
		f.class = int(lower[0] - '0')
	default:
		code, err := strconv.Atoi(raw)
		if err != nil || code < 100 || code > 599 {
			return f, fmt.Errorf("status must be a code like 404 or a class like 4xx, got %q", raw)
		}
		f.status = code
	}

	return f, nil
}

// any reports whether the filter constrains anything.
func (f requestFilter) any() bool {
	return f.invalid || f.method != "" || f.status != 0 || f.class != 0 || f.requestID != ""
}

func (f requestFilter) match(e mock.Entry) bool {
	switch {
	case f.invalid && len(e.ValidationErrors) == 0:
		return false
	case f.method != "" && e.Method != f.method:
		return false
	case f.status != 0 && e.Status != f.status:
		return false
	case f.class != 0 && e.Status/100 != f.class:
		return false
	case f.requestID != "" && !strings.EqualFold(e.ID, f.requestID):
		return false
	}
	return true
}

// apply returns the entries the filter keeps, oldest first like the input.
func (f requestFilter) apply(entries []mock.Entry) []mock.Entry {
	if !f.any() {
		return entries
	}
	kept := make([]mock.Entry, 0, len(entries))
	for _, e := range entries {
		if f.match(e) {
			kept = append(kept, e)
		}
	}
	return kept
}
