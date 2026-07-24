package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// HealthzHandler returns a process-liveness handler.
func HealthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

// ReadyzHandler returns a readiness handler backed by the supplied check.
func ReadyzHandler(pingFn func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pingFn(); err != nil {
			slog.Error("readiness check failed", "error", err)
			WriteError(w, http.StatusServiceUnavailable, ErrInternal, "service not ready")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

// RequestLogMiddleware logs every request after it completes. Seeds the
// auth-info container on the request context so AuthMiddleware can write the
// authenticated principal back even though `r.WithContext` only propagates
// downward; otherwise log entries for /v1 routes would always have an empty
// token_id.
func RequestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, info := withAuthInfoContainer(r.Context())
		r = r.WithContext(ctx)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		requestID := middleware.GetReqID(ctx)
		if requestID != "" {
			w.Header().Set("X-Request-ID", requestID)
		}

		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		slog.Info("request",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"latency_ms", time.Since(start).Milliseconds(),
			"token_id", info.tokenID,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets net/http's ResponseController reach optional capabilities on the
// underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
