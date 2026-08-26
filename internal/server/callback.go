package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/edspc/golang-mock-api-server/internal/endpoint"
	"github.com/edspc/golang-mock-api-server/internal/mock"
)

// CallbackPrefix is where dynamically created callback endpoints are served:
// /cb/{uuid} plus any sub-path.
const CallbackPrefix = "/cb/"

// RequestIDHeader carries the id of the captured request back to whoever sent
// it, on every answer an endpoint gives.
const RequestIDHeader = "X-Mock-API-RequestID"

// canonicalRequestIDHeader is what Go's Header.Set would store it under.
var canonicalRequestIDHeader = textproto.CanonicalMIMEHeaderKey(RequestIDHeader)

// setRequestID writes the header with its documented spelling. Header.Set
// would canonicalise it to X-Mock-Api-Requestid; the name is documented,
// searched for and pasted into the console, so it goes out as written.
func setRequestID(w http.ResponseWriter, id string) {
	if id == "" {
		return
	}
	w.Header()[RequestIDHeader] = []string{id}
}

// serveCallback dispatches traffic aimed at a callback endpoint. Everything
// below /cb/{id} belongs to that endpoint, and the remainder of the path is
// what the endpoint's rules match against.
func (s *Server) serveCallback(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, CallbackPrefix)
	id, sub, _ := strings.Cut(rest, "/")

	ep, err := s.endpoints.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "unknown callback endpoint",
			"id":    id,
		})
		return
	}

	subPath := "/" + sub
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		s.log.Warn("read callback body", "endpoint", id, "error", err)
		body = nil
	}

	out := ep.Handle(r, subPath, body)
	// Before anything that can return: a validation failure and a broken
	// response template are answers too, and are exactly the ones worth
	// looking up afterwards.
	setRequestID(w, out.RequestID)

	if d := out.Response.Delay.Duration(); d > 0 && !sleep(r, d) {
		return
	}

	data := mock.NewRenderData(r, out.Params).WithBody(body)
	rendered, err := mock.Render(out.Response.Body, data)
	if err != nil {
		s.log.Error("render callback response", "endpoint", id, "rule", out.Rule, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "callback response template failed: " + err.Error(),
		})
		return
	}

	header := w.Header()
	for k, v := range out.Response.Headers {
		// A spec that sets the request-id header itself wins, and replaces
		// ours rather than joining it: Header.Set stores the canonical
		// spelling, which is a different map key from the one above.
		if textproto.CanonicalMIMEHeaderKey(k) == canonicalRequestIDHeader {
			delete(header, RequestIDHeader)
		}
		header.Set(k, v)
	}
	if len(rendered) > 0 && header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	w.WriteHeader(out.Response.Status)
	if len(rendered) > 0 && r.Method != http.MethodHead {
		if _, err := w.Write(rendered); err != nil {
			s.log.Warn("write callback response", "endpoint", id, "error", err)
		}
	}

	s.log.Info("callback",
		"endpoint", id, "method", r.Method, "path", r.URL.Path,
		"rule", out.Rule, "status", out.Response.Status,
		"validationErrors", len(out.ValidationErrors))
}

// endpointView is the control-API projection of an endpoint.
type endpointView struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url"`
	// Owner and Shared are omitted entirely when sign-in is off: with no
	// accounts there is nobody to own anything, and an empty "owner" in every
	// response would only invite the question.
	Owner     string        `json:"owner,omitempty"`
	Shared    []string      `json:"shared,omitempty"`
	CreatedAt string        `json:"createdAt"`
	Received  int64         `json:"received"`
	Spec      endpoint.Spec `json:"spec"`
}

func viewOf(e *endpoint.Endpoint) endpointView {
	return endpointView{
		ID:        e.ID.String(),
		Name:      e.Name(),
		URL:       CallbackPrefix + e.ID.String(),
		Owner:     e.Owner,
		Shared:    e.Shared(),
		CreatedAt: e.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		Received:  e.Received(),
		Spec:      e.Spec(),
	}
}

// registerEndpointAdmin adds the endpoint-management routes to the control mux.
func (s *Server) registerEndpointAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST "+AdminPrefix+"endpoints", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string         `json:"name"`
			Spec *endpoint.Spec `json:"spec"`
		}
		// An empty body is a valid "just give me a URL" request.
		if err := decodeOptionalJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		ep, err := s.endpoints.Create(s.auth.Caller(r), req.Name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if req.Spec != nil {
			if err := ep.SetSpec(*req.Spec); err != nil {
				// Roll back rather than leave an endpoint the caller never
				// successfully configured.
				_ = s.endpoints.Delete(ep.Owner, ep.ID.String())
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}
		s.log.Info("endpoint created", "id", ep.ID.String(), "name", ep.Name(), "owner", ep.Owner)
		writeJSON(w, http.StatusCreated, viewOf(ep))
	})

	mux.HandleFunc("GET "+AdminPrefix+"endpoints", func(w http.ResponseWriter, r *http.Request) {
		list := s.endpoints.List(s.auth.Caller(r))
		views := make([]endpointView, 0, len(list))
		for _, e := range list {
			views = append(views, viewOf(e))
		}
		writeJSON(w, http.StatusOK, views)
	})

	mux.HandleFunc("GET "+AdminPrefix+"endpoints/{id}", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := s.lookup(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, viewOf(ep))
	})

	mux.HandleFunc("DELETE "+AdminPrefix+"endpoints/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.endpoints.Delete(s.auth.Caller(r), r.PathValue("id")); err != nil {
			writeNotFound(w, r.PathValue("id"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})

	// Renaming touches only the label. The URL is the endpoint's identity and
	// never changes, so a rename cannot break a third party already calling it.
	mux.HandleFunc("PUT "+AdminPrefix+"endpoints/{id}/name", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := s.lookup(w, r)
		if !ok {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "parse name: " + err.Error()})
			return
		}
		was := ep.Name()
		if err := ep.SetName(req.Name); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.log.Info("endpoint renamed", "id", ep.ID.String(), "from", was, "to", ep.Name())
		writeJSON(w, http.StatusOK, viewOf(ep))
	})

	// Sharing gives another account the same access the owner has — reading
	// the traffic, editing the spec, deleting it. The one thing it does not
	// pass on is this route: only the owner decides who else is on the list,
	// so a shared account cannot widen its own reach.
	mux.HandleFunc("PUT "+AdminPrefix+"endpoints/{id}/share", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := s.lookup(w, r)
		if !ok {
			return
		}
		caller := s.auth.Caller(r)
		if !s.auth.Enabled() {
			// Without sign-in there are no accounts to share with, and every
			// caller is already the same anonymous one.
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "sharing needs sign-in; configure google oauth to use it",
			})
			return
		}
		if ep.Owner != caller {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "only the owner can share this endpoint",
				"owner": ep.Owner,
			})
			return
		}
		var req struct {
			Shared []string `json:"shared"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "parse share list: " + err.Error()})
			return
		}
		if err := ep.SetShared(req.Shared); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.log.Info("endpoint shared", "id", ep.ID.String(), "owner", ep.Owner, "with", len(ep.Shared()))
		writeJSON(w, http.StatusOK, viewOf(ep))
	})

	// PUT replaces the whole spec: rules, validation, and default response.
	mux.HandleFunc("PUT "+AdminPrefix+"endpoints/{id}/spec", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := s.lookup(w, r)
		if !ok {
			return
		}
		var spec endpoint.Spec
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&spec); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "parse spec: " + err.Error()})
			return
		}
		if err := ep.SetSpec(spec); err != nil {
			// The previous spec keeps serving: a bad edit must not break an
			// endpoint a third party is already calling.
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.log.Info("endpoint spec updated", "id", ep.ID.String(), "rules", len(spec.Rules))
		writeJSON(w, http.StatusOK, viewOf(ep))
	})

	mux.HandleFunc("GET "+AdminPrefix+"endpoints/{id}/requests", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := s.lookup(w, r)
		if !ok {
			return
		}
		filter, err := parseRequestFilter(r.URL.Query())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, filter.apply(ep.Requests()))
	})

	mux.HandleFunc("POST "+AdminPrefix+"endpoints/{id}/reset", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := s.lookup(w, r)
		if !ok {
			return
		}
		ep.ResetRequests()
		writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
	})
}

// lookup resolves the endpoint a control request names, scoped to whoever is
// asking. Callback traffic never comes through here — it is dispatched before
// the guard and has no identity to scope by.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (*endpoint.Endpoint, bool) {
	id := r.PathValue("id")
	ep, err := s.endpoints.GetFor(s.auth.Caller(r), id)
	if err != nil {
		writeNotFound(w, id)
		return nil, false
	}
	return ep, true
}

func writeNotFound(w http.ResponseWriter, id string) {
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": endpoint.ErrNotFound.Error(),
		"id":    id,
	})
}

// decodeOptionalJSON decodes a request body that is allowed to be empty.
func decodeOptionalJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}
