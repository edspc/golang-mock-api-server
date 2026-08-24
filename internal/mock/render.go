package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// RenderData is what a response body template can reference:
//
//	{{.Path.id}}      a {id} capture from the request path
//	{{.Query.limit}}  the first value of a query parameter
//	{{.Header.Accept}} the first value of a request header
//	{{now}}           RFC3339 timestamp
//
// Bodies written inline in the config file are JSON, so a template there
// cannot contain a double quote: json.RawMessage keeps the "\"" escape and the
// backslash reaches the template parser. That rules out {{index .Map "key"}},
// so every key that is not a valid Go identifier also gets an alias that is:
//
//	{{.Header.XApiKey}}  alias of the X-Api-Key header
//	{{.Path.rest}}       alias of the "*" wildcard capture
//
// Missing keys render as the empty string rather than failing the request,
// because a mock that 500s on a typo is harder to debug than one that returns
// a visibly empty field.
type RenderData struct {
	Path   map[string]string
	Query  map[string]string
	Header map[string]string
	// Body is the request body decoded as JSON, or nil when it is absent or
	// not JSON. Callback responses use it to echo fields back:
	// {{.Body.event}}, {{.Body.data.id}}.
	Body any
}

// WithBody returns a copy of d carrying the decoded request body.
func (d RenderData) WithBody(body []byte) RenderData {
	d.Body = DecodeJSON(body)
	return d
}

// DecodeJSON decodes body as JSON, returning nil if it is empty or invalid.
// Template access to a nil body renders empty rather than failing, which keeps
// a malformed callback payload debuggable instead of turning it into a 500.
func DecodeJSON(body []byte) any {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	return v
}

// WildcardAlias is the identifier-safe name for the "*" path capture.
const WildcardAlias = "rest"

// NewRenderData collects the template inputs for a request.
func NewRenderData(r *http.Request, params map[string]string) RenderData {
	d := RenderData{
		Path:   make(map[string]string, len(params)+1),
		Query:  make(map[string]string),
		Header: make(map[string]string),
	}
	for k, v := range params {
		d.Path[k] = v
	}
	if v, ok := d.Path["*"]; ok {
		if _, taken := d.Path[WildcardAlias]; !taken {
			d.Path[WildcardAlias] = v
		}
	}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			d.Query[k] = v[0]
			addAlias(d.Query, k, v[0])
		}
	}
	for k, v := range r.Header {
		if len(v) > 0 {
			d.Header[k] = v[0]
			addAlias(d.Header, k, v[0])
		}
	}
	return d
}

// addAlias adds a dash-free alias for key so templates can use field syntax
// ({{.Header.XApiKey}}) instead of an index expression that would need quotes.
func addAlias(m map[string]string, key, value string) {
	alias := strings.ReplaceAll(key, "-", "")
	if alias == key {
		return
	}
	if _, taken := m[alias]; !taken {
		m[alias] = value
	}
}

var funcs = template.FuncMap{
	"now": func() string { return time.Now().UTC().Format(time.RFC3339) },
}

// Render expands the templates in body. Bodies without "{{" skip parsing
// entirely, which is the common case.
func Render(body []byte, data RenderData) ([]byte, error) {
	if !bytes.Contains(body, []byte("{{")) {
		return body, nil
	}
	tmpl, err := template.New("body").Funcs(funcs).Option("missingkey=zero").Parse(string(body))
	if err != nil {
		return nil, fmt.Errorf("parse body template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render body template: %w", err)
	}
	return buf.Bytes(), nil
}
