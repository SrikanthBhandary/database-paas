package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

type ctxKey int

const ownerKey ctxKey = iota

type StatusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *StatusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *StatusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *StatusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func LoggingMiddleware(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &StatusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			log.Info("http request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", rec.status),
				zap.Int("bytes", rec.bytes),
				zap.Duration("duration", time.Since(start)),
				zap.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}

// withOwner is a stand-in for real authentication: it reads the caller's
// identity from X-Owner-ID. Anyone can claim any owner, so replace this with
// real auth (JWT/OIDC/API keys) before exposing the API. Handlers only call
// ownerFrom, so swapping it in touches this file alone.
func withOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := strings.TrimSpace(r.Header.Get("X-Owner-ID"))
		if owner == "" {
			writeProblem(w, http.StatusUnauthorized, "X-Owner-ID header is required", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerKey, owner)))
	})
}

func ownerFrom(ctx context.Context) string {
	owner, _ := ctx.Value(ownerKey).(string)
	return owner
}
