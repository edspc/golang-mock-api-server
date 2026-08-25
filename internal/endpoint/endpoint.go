// Package endpoint implements callback endpoints: throwaway URLs created on
// demand, identified by a UUIDv8, that capture whatever a third party sends
// them and answer according to rules the user can rewrite at any time.
package endpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edspc/golang-mock-api-server/internal/config"
	"github.com/edspc/golang-mock-api-server/internal/mock"
	"github.com/edspc/golang-mock-api-server/internal/uuid"
)

// AnyPath is the rule path used when a rule declares none: match any sub-path
// below the endpoint URL.
const AnyPath = "/*"

// Spec is the user-editable behaviour of an endpoint. It is replaced wholesale
// rather than patched, so what the user PUTs is exactly what the endpoint does
// — there is no accumulated state to reason about.
type Spec struct {
	// Validation is checked before any rule matches. A failing request is
	// still recorded, with the reasons attached.
	Validation *Validation `json:"validation,omitempty"`
	// Rules are tried in order; the first full match wins.
	Rules []config.Rule `json:"rules,omitempty"`
	// Response answers requests that match no rule. Nil means 200 with an
	// acknowledgement body.
	Response *config.Response `json:"response,omitempty"`
}

// Validation describes what an incoming callback must look like.
type Validation struct {
	// RequireHeaders lists headers that must be present. "Name: value" pins
	// the value; a bare name only requires presence.
	RequireHeaders []string `json:"requireHeaders,omitempty"`
	// RequireQuery lists query parameters that must be present.
	RequireQuery []string `json:"requireQuery,omitempty"`
	// JSONBody requires the body to parse as JSON.
	JSONBody bool `json:"jsonBody,omitempty"`
	// RequireFields lists dotted paths that must exist in the JSON body,
	// e.g. "data.id". Implies JSONBody.
	RequireFields []string `json:"requireFields,omitempty"`
	// BodyContains requires a substring in the raw body.
	BodyContains string `json:"bodyContains,omitempty"`
	// OnFailure is returned when validation fails. Nil means 400 with the
	// reasons in the body.
	OnFailure *config.Response `json:"onFailure,omitempty"`
}

// History records and returns the traffic captured on one endpoint. The
// in-memory ring and the SQLite-backed log both satisfy it, so Endpoint never
// learns which one it has.
type History interface {
	Record(mock.Entry)
	Entries() []mock.Entry
	Reset()
}

// Endpoint is one callback URL and everything captured on it.
type Endpoint struct {
	ID        uuid.UUID `json:"-"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`

	// mu guards spec and rules, which are replaced together on every update.
	mu    sync.RWMutex
	spec  Spec
	rules []*mock.Rule

	history  History
	received atomic.Int64

	// onSpec and onRecv let the Registry persist changes. They are separate
	// because a callback must not rewrite the spec on every request.
	onSpec func(*Endpoint)
	onRecv func(*Endpoint)
}

// New creates an endpoint with a fresh UUIDv8, an empty spec and an in-memory
// history of the given size.
func New(name string, history int) (*Endpoint, error) {
	id, err := uuid.NewV8()
	if err != nil {
		return nil, err
	}
	return newEndpoint(id, name, id.Time(), mock.NewRecorder(history)), nil
}

func newEndpoint(id uuid.UUID, name string, created time.Time, h History) *Endpoint {
	return &Endpoint{ID: id, Name: name, CreatedAt: created, history: h}
}

// Spec returns the endpoint's current behaviour.
func (e *Endpoint) Spec() Spec {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.spec
}

// SetSpec validates spec, compiles its rules, and installs both atomically.
// On error the endpoint keeps serving its previous spec.
func (e *Endpoint) SetSpec(spec Spec) error {
	if spec.Validation != nil {
		if err := spec.Validation.normalize(); err != nil {
			return err
		}
	}
	if spec.Response != nil {
		if err := spec.Response.Normalize("response"); err != nil {
			return err
		}
	}
	rules := make([]*mock.Rule, 0, len(spec.Rules))
	for i := range spec.Rules {
		if err := spec.Rules[i].Normalize(fmt.Sprintf("rule[%d]", i), AnyPath); err != nil {
			return err
		}
		s, err := mock.NewRule(spec.Rules[i])
		if err != nil {
			return err
		}
		rules = append(rules, s)
	}

	e.mu.Lock()
	e.spec = spec
	e.rules = rules
	e.mu.Unlock()

	if e.onSpec != nil {
		e.onSpec(e)
	}
	return nil
}

func (v *Validation) normalize() error {
	if v.OnFailure != nil {
		if err := v.OnFailure.Normalize("validation.onFailure"); err != nil {
			return err
		}
	}
	for _, h := range v.RequireHeaders {
		if strings.TrimSpace(h) == "" {
			return errors.New("validation.requireHeaders: empty header name")
		}
	}
	for _, f := range v.RequireFields {
		if strings.TrimSpace(f) == "" {
			return errors.New("validation.requireFields: empty field path")
		}
	}
	return nil
}

// Received is the total number of callbacks this endpoint has seen, including
// those evicted from its bounded history.
func (e *Endpoint) Received() int64 { return e.received.Load() }

// Requests returns the captured request history, oldest first.
func (e *Endpoint) Requests() []mock.Entry { return e.history.Entries() }

// ResetRequests clears the captured history. The received counter is left
// alone: it reports lifetime traffic, not history size.
func (e *Endpoint) ResetRequests() { e.history.Reset() }

// Outcome is what an endpoint decided to do with one callback.
type Outcome struct {
	Response config.Response
	// Rule is the name of the rule that matched, empty when none did.
	Rule string
	// ValidationErrors is non-empty when the request failed validation, in
	// which case Response is the failure response.
	ValidationErrors []string
	// Params are the rule's path captures.
	Params map[string]string
}

// Handle decides how to answer one callback. subPath is the request path below
// the endpoint's URL ("/" when the callback hit the endpoint URL itself). It
// records the request and returns the outcome; it does not write anything.
func (e *Endpoint) Handle(r *http.Request, subPath string, body []byte) Outcome {
	e.mu.RLock()
	spec, rules := e.spec, e.rules
	e.mu.RUnlock()

	e.received.Add(1)
	out := Outcome{}

	if spec.Validation != nil {
		if errs := spec.Validation.check(r, body); len(errs) > 0 {
			out.ValidationErrors = errs
			out.Response = validationFailureResponse(spec.Validation, errs)
			e.record(r, body, out)
			return out
		}
	}

	for i, rule := range rules {
		if params, ok := rule.MatchPath(r, subPath, body); ok {
			out.Rule = spec.Rules[i].Name
			out.Params = params
			out.Response = spec.Rules[i].Response
			e.record(r, body, out)
			return out
		}
	}

	out.Response = defaultResponse(spec.Response)
	e.record(r, body, out)
	return out
}

func defaultResponse(r *config.Response) config.Response {
	if r != nil {
		return *r
	}
	return config.Response{
		Status: http.StatusOK,
		Body:   json.RawMessage(`{"received":true}`),
	}
}

func validationFailureResponse(v *Validation, errs []string) config.Response {
	if v.OnFailure != nil {
		return *v.OnFailure
	}
	body, err := json.Marshal(struct {
		Error  string   `json:"error"`
		Errors []string `json:"errors"`
	}{Error: "validation_failed", Errors: errs})
	if err != nil {
		body = []byte(`{"error":"validation_failed"}`)
	}
	return config.Response{Status: http.StatusBadRequest, Body: body}
}

func (e *Endpoint) record(r *http.Request, body []byte, out Outcome) {
	e.history.Record(mock.Entry{
		Time:             time.Now().UTC(),
		Method:           r.Method,
		Path:             r.URL.Path,
		Query:            r.URL.Query(),
		Headers:          r.Header.Clone(),
		Body:             string(body),
		Rule:             out.Rule,
		Status:           out.Response.Status,
		ValidationErrors: out.ValidationErrors,
	})
	if e.onRecv != nil {
		e.onRecv(e)
	}
}

// check returns every reason the request is invalid, so the user fixing their
// caller sees all the problems at once rather than one per attempt.
func (v *Validation) check(r *http.Request, body []byte) []string {
	var errs []string

	for _, want := range v.RequireHeaders {
		name, value, pinned := strings.Cut(want, ":")
		name = strings.TrimSpace(name)
		got := r.Header.Values(name)
		if len(got) == 0 {
			errs = append(errs, "missing header "+name)
			continue
		}
		if pinned {
			value = strings.TrimSpace(value)
			if !containsValue(got, value) {
				errs = append(errs, fmt.Sprintf("header %s = %q, want %q", name, got[0], value))
			}
		}
	}

	query := r.URL.Query()
	for _, name := range v.RequireQuery {
		if _, ok := query[name]; !ok {
			errs = append(errs, "missing query parameter "+name)
		}
	}

	if v.BodyContains != "" && !bytes.Contains(body, []byte(v.BodyContains)) {
		errs = append(errs, fmt.Sprintf("body does not contain %q", v.BodyContains))
	}

	if !v.JSONBody && len(v.RequireFields) == 0 {
		return errs
	}

	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return append(errs, "body is not valid JSON")
	}
	for _, path := range v.RequireFields {
		if _, ok := lookupField(decoded, path); !ok {
			errs = append(errs, "missing field "+path)
		}
	}
	return errs
}

// lookupField walks a dotted path through decoded JSON objects.
func lookupField(v any, path string) (any, bool) {
	cur := v
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func containsValue(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
