package server

import "net/http"

type APIServer struct {
	Router *http.ServeMux
}

func NewAPIServer() *APIServer {
	router := http.NewServeMux()
	return &APIServer{
		Router: router,
	}
}

// RegisterAPI registers the handlers
func (as *APIServer) RegisterAPI() {
	as.Router.HandleFunc("GET "+v1+"/databases", as.listDatabases)
	as.Router.HandleFunc("POST "+v1+"/databases", as.createDatabase)
	as.Router.HandleFunc("GET /health", as.healthHandler)
	as.Router.HandleFunc("GET /ping", as.pingHandler)
}
