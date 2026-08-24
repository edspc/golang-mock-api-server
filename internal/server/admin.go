package server

import (
	"net/http"
)

// adminMux builds the control API. It is registered under AdminPrefix, which
// callback traffic can never reach, so the control endpoints stay available no
// matter what a user configures.
func (s *Server) adminMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET "+AdminPrefix+"health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"endpoints": s.endpoints.Len(),
		})
	})

	s.registerEndpointAdmin(mux)

	mux.HandleFunc(AdminPrefix, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "unknown control endpoint",
			"endpoints": []string{
				"GET " + AdminPrefix + "health",
				"POST " + AdminPrefix + "endpoints", "GET " + AdminPrefix + "endpoints",
				"GET " + AdminPrefix + "endpoints/{id}", "DELETE " + AdminPrefix + "endpoints/{id}",
				"PUT " + AdminPrefix + "endpoints/{id}/spec",
				"GET " + AdminPrefix + "endpoints/{id}/requests",
				"POST " + AdminPrefix + "endpoints/{id}/reset",
			},
		})
	})

	return mux
}
