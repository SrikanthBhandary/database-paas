package server

import (
	"db-paas/pkg/database"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"go.uber.org/zap"
)

const maxBodyBytes = 1 << 20 // 1 MiB

type Problem struct {
	Type   string            `json:"type"`
	Title  string            `json:"title"`
	Status int               `json:"status"`
	Detail string            `json:"detail,omitempty"`
	Errors map[string]string `json:"errors,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, detail string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
		Errors: fields,
	})
}

func (as *APIServer) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		as.log.Error("write response", zap.Error(err))
	}
}

// decodeJSON reads exactly one JSON object, rejecting unknown fields,
// trailing data, and bodies over maxBodyBytes.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain a single JSON object")
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var (
		tooBig  *http.MaxBytesError
		typeErr *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &tooBig):
		writeProblem(w, http.StatusRequestEntityTooLarge, "request body too large", nil)
	case errors.As(err, &typeErr):
		writeProblem(w, http.StatusBadRequest, "invalid request body",
			map[string]string{typeErr.Field: "must be of type " + typeErr.Type.String()})
	case errors.Is(err, io.EOF):
		writeProblem(w, http.StatusBadRequest, "request body is empty", nil)
	default:
		writeProblem(w, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
	}
}

// writeError maps domain errors to HTTP. Anything unexpected is logged in
// full and returned as a generic 500, so internals never reach the client.
func (as *APIServer) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *database.ValidationError
	switch {
	case errors.As(err, &ve): // check before ErrInvalidInput, which it unwraps to
		writeProblem(w, http.StatusBadRequest, "validation failed", ve.Fields)
	case errors.Is(err, database.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "invalid input", nil)
	case errors.Is(err, database.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "database not found", nil)
	case errors.Is(err, database.ErrAlreadyExists):
		writeProblem(w, http.StatusConflict, "a database with this name already exists", nil)
	case errors.Is(err, database.ErrInvalidState):
		writeProblem(w, http.StatusConflict, "operation not allowed in the database's current state", nil)
	default:
		as.log.Error("request failed",
			zap.String("method", r.Method), zap.String("path", r.URL.Path), zap.Error(err))
		writeProblem(w, http.StatusInternalServerError, "internal server error", nil)
	}
}
