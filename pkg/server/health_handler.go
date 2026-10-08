package server

import "net/http"

// healthHandler handles the health endpoint
func (as *APIServer) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

// pingHandler handles the ping endpoint
func (as *APIServer) pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Pong"))
}
