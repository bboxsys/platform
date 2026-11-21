package server

import "net/http"

// ServeOpenAPI registers GET /api/v1/openapi.json returning the given spec.
// The route is unauthenticated so the API explorer can load it before or
// after login; specs document the API but contain no secrets.
func (server *Server) ServeOpenAPI(spec []byte) {
	server.Router.HandleFunc("/api/v1/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write(spec)
	}).Methods("GET")
}
