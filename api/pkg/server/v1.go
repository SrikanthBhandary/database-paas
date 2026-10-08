package server

import "net/http"

const v1 = "/v1"

// POST /v1/databases: validates, stores a pending row, returns 202.
func (as *APIServer) createDatabase(w http.ResponseWriter, r *http.Request) {
	var req createDatabaseRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}

	db, err := as.svc.CreateDatabase(r.Context(), req.toInput(ownerFrom(r.Context())))
	if err != nil {
		as.writeError(w, r, err)
		return
	}

	w.Header().Set("Location", v1+"/databases/"+db.ID)
	as.writeJSON(w, http.StatusAccepted, toResponse(db))
}

// GET /v1/databases/{id}
func (as *APIServer) getDatabase(w http.ResponseWriter, r *http.Request) {
	db, err := as.svc.GetDatabase(r.Context(), ownerFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		as.writeError(w, r, err)
		return
	}
	as.writeJSON(w, http.StatusOK, toResponse(db))
}

// GET /v1/databases
func (as *APIServer) listDatabases(w http.ResponseWriter, r *http.Request) {
	dbs, err := as.svc.ListDatabases(r.Context(), ownerFrom(r.Context()))
	if err != nil {
		as.writeError(w, r, err)
		return
	}
	as.writeJSON(w, http.StatusOK, toListResponse(dbs))
}
