package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxCachedBodySize = 4096

// IdempotencyMiddleware enforces Stripe-style request idempotency for mutating
// methods. Two concurrent POSTs with the same Idempotency-Key must not both
// run; the first wins via an atomic INSERT-with-reservation, the second
// either replays the cached response or gets 409 if the first hasn't
// completed yet. Same key with a different body → 409. GETs/HEADs pass
// through unchanged.
func IdempotencyMiddleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !methodNeedsIdempotency(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			handleIdempotentRequest(w, r, pool, next, key)
		})
	}
}

func methodNeedsIdempotency(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

func handleIdempotentRequest(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, next http.Handler, key string) {
	tokenID := TokenIDFromContext(r.Context())
	if tokenID == "" {
		next.ServeHTTP(w, r)
		return
	}

	hashStr, err := requestHashAndRestoreBody(r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to read request body")
		return
	}

	// Atomically reserve (token_id, key). If the row already exists this is a
	// no-op and we fall through to the SELECT branch to figure out whether to
	// replay, conflict, or wait.
	tag, err := pool.Exec(r.Context(),
		`INSERT INTO idempotency_keys (token_id, key, request_hash)
		 VALUES ($1, $2, $3) ON CONFLICT (token_id, key) DO NOTHING`,
		tokenID, key, hashStr,
	)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "idempotency reserve failed")
		return
	}

	if tag.RowsAffected() == 0 {
		// Existing row — replay, conflict, or in-flight.
		serveExistingIdempotent(w, r, pool, tokenID, key, hashStr)
		return
	}

	// We hold the reservation. Run the handler and persist its result.
	rec := captureResponse(next, w, r)
	persistCompletion(r.Context(), pool, tokenID, key, rec)
}

func serveExistingIdempotent(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, tokenID, key, hashStr string) {
	var (
		storedHash  string
		respStatus  *int
		respBody    []byte
		completedAt *time.Time
	)
	err := pool.QueryRow(r.Context(),
		`SELECT request_hash, response_status, response_body, completed_at
		   FROM idempotency_keys WHERE token_id = $1 AND key = $2`,
		tokenID, key,
	).Scan(&storedHash, &respStatus, &respBody, &completedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Race: the row was deleted (e.g. handler returned non-2xx and we
			// rolled back) between our INSERT failing and this SELECT. The
			// caller can safely retry.
			WriteError(w, http.StatusConflict, ErrInvalidRequest, "idempotency record vanished, retry")
			return
		}
		WriteError(w, http.StatusInternalServerError, ErrInternal, "idempotency lookup failed")
		return
	}

	if storedHash != hashStr {
		WriteError(w, http.StatusConflict, ErrInvalidRequest, "idempotency key already used with a different request body")
		return
	}
	if completedAt == nil {
		// First request still running. Stripe-style: 409 and let the client
		// retry; alternative would be to wait, but the handler bound is
		// already capped by HTTP server write timeout.
		WriteError(w, http.StatusConflict, ErrInvalidRequest, "request with this idempotency key is in flight")
		return
	}
	if respStatus == nil || respBody == nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "original response was too large to cache; retry without Idempotency-Key")
		return
	}
	replayCachedResponse(w, *respStatus, respBody)
}

func persistCompletion(ctx context.Context, pool *pgxpool.Pool, tokenID, key string, rec *responseRecorder) {
	// Cache only 2xx and only if the body fit. Non-2xx clears the reservation
	// so retries can succeed; an oversize 2xx is persisted with NULL body so
	// future replays return a clear error.
	if rec.status < 200 || rec.status >= 300 {
		if _, err := pool.Exec(ctx,
			`DELETE FROM idempotency_keys WHERE token_id = $1 AND key = $2`,
			tokenID, key,
		); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("idempotency reservation release failed", "token_id", tokenID, "error", err)
		}
		return
	}

	if rec.tooLarge {
		if _, err := pool.Exec(ctx,
			`UPDATE idempotency_keys SET response_status = $1, completed_at = now()
			  WHERE token_id = $2 AND key = $3`,
			rec.status, tokenID, key,
		); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("idempotency completion (oversize) failed", "token_id", tokenID, "error", err)
		}
		return
	}

	if _, err := pool.Exec(ctx,
		`UPDATE idempotency_keys SET response_status = $1, response_body = $2, completed_at = now()
		  WHERE token_id = $3 AND key = $4`,
		rec.status, rec.body.Bytes(), tokenID, key,
	); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("idempotency cache write failed", "token_id", tokenID, "error", err)
	}
}

func requestHashAndRestoreBody(r *http.Request) (string, error) {
	bodyBytes, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	bodyHash := sha256.Sum256(bodyBytes)
	return hex.EncodeToString(bodyHash[:]), nil
}

func replayCachedResponse(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func captureResponse(next http.Handler, w http.ResponseWriter, r *http.Request) *responseRecorder {
	rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
	next.ServeHTTP(rec, r)
	return rec
}

// responseRecorder mirrors the handler's writes into a buffer up to
// maxCachedBodySize. A single overflowing Write flips tooLarge and stops
// further buffer writes, so a partial prefix is never persisted as the
// cached body.
type responseRecorder struct {
	http.ResponseWriter
	status   int
	body     *bytes.Buffer
	tooLarge bool
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if !r.tooLarge {
		if r.body.Len()+len(b) > maxCachedBodySize {
			r.tooLarge = true
			r.body.Reset()
		} else {
			r.body.Write(b)
		}
	}
	return r.ResponseWriter.Write(b)
}
