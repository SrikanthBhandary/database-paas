package server

import (
	"db-paas/pkg/service"
	"net/http"

	"go.uber.org/zap"
)

type APIServer struct {
	Router *http.ServeMux
	svc    *service.Service
	log    *zap.Logger
}

func NewAPIServer(svc *service.Service, log *zap.Logger) *APIServer {
	return &APIServer{Router: http.NewServeMux(), svc: svc, log: log}
}

// RegisterAPI registers the handlers
func (as *APIServer) RegisterAPI() {
	// infrastructure endpoints: unversioned, no owner required
	as.Router.HandleFunc("GET /health", as.healthHandler)
	as.Router.HandleFunc("GET /ping", as.pingHandler)

	owner := withOwner

	// v1 API: every route is wrapped in withOwner
	as.Router.Handle("POST "+v1+"/databases", owner(http.HandlerFunc(as.createDatabase)))
	as.Router.Handle("GET "+v1+"/databases", owner(http.HandlerFunc(as.listDatabases)))
	as.Router.Handle("GET "+v1+"/databases/{id}", owner(http.HandlerFunc(as.getDatabase)))
	as.Router.Handle("PATCH "+v1+"/databases/{id}", owner(http.HandlerFunc(as.updateDatabase)))
}
