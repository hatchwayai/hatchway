package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxCachedBodySize    = 4096
	maxIdempotencyKeyLen = 255
	completionTimeout    = 3 * time.Second
	retryAfterSeconds    = "1"
)

// IdempotencyMiddleware enforces Stripe-style request idempotency for mutating
// methods. Two concurrent POSTs with the same Idempotency-Key must not both
// run; the first wins via an atomic INSERT-with-reservation, the second
// either replays the cached response or gets 409 if the first hasn't
// completed yet. The request fingerprint covers the method, target, and body,
// so a key cannot replay a response from another endpoint. Cached response
// bodies are encrypted with a key derived from the plugin secret.
func IdempotencyMiddleware(pool *pgxpool.Pool, secret string) func(http.Handler) http.Handler {
	codec := newIdempotencyCodec(secret)
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
			if len(key) > maxIdempotencyKeyLen {
				WriteError(w, http.StatusBadRequest, ErrInvalidRequest, "Idempotency-Key must be at most 255 bytes")
				return
			}

			handleIdempotentRequest(w, r, pool, codec, next, key)
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

func handleIdempotentRequest(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, codec *idempotencyCodec, next http.Handler, key string) {
	tokenID := TokenIDFromContext(r.Context())
	if tokenID == "" {
		next.ServeHTTP(w, r)
		return
	}

	hashStr, err := requestHashAndRestoreBody(r)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			WriteError(w, http.StatusRequestEntityTooLarge, ErrInvalidRequest, "request body too large")
			return
		}
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
		serveExistingIdempotent(w, r, pool, codec, tokenID, key, hashStr)
		return
	}

	// Domain handlers that commit one-time response material can complete this
	// reservation in their own transaction. That prevents a process exit after
	// the domain commit from losing the only recoverable copy of the response.
	reservation := &idempotencyReservation{codec: codec, tokenID: tokenID, key: key}
	r = r.WithContext(context.WithValue(r.Context(), idempotencyReservationContextKey{}, reservation))

	// We hold the reservation. Run the handler and persist its result.
	var rec *responseRecorder
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), completionTimeout)
				defer cancel()
				if _, err := pool.Exec(releaseCtx,
					"DELETE FROM idempotency_keys WHERE token_id = $1 AND key = $2 AND completed_at IS NULL",
					tokenID, key,
				); err != nil {
					slog.Error("idempotency reservation release after panic failed", "token_id", tokenID, "error", err)
				}
				panic(recovered)
			}
		}()
		rec = captureResponse(next, w, r)
	}()
	// The client can disconnect after the handler commits but before the
	// response finishes. Complete the reservation with a short detached
	// context so that cancellation does not strand it as permanently in-flight.
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), completionTimeout)
	defer cancel()
	persistCompletion(completionCtx, pool, codec, tokenID, key, rec)
}

// CompleteIdempotentResponseInTransaction stores a successful response in the
// idempotency reservation associated with ctx using tx. It returns false when
// the request has no active reservation. A domain handler should call this
// before committing any transaction that creates one-time response material,
// such as a runtime token, so the state change and replay response are atomic.
func CompleteIdempotentResponseInTransaction(
	ctx context.Context,
	tx pgx.Tx,
	status int,
	body []byte,
) (bool, error) {
	reservation, ok := ctx.Value(idempotencyReservationContextKey{}).(*idempotencyReservation)
	if !ok {
		return false, nil
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("idempotency: transaction completion requires a 2xx status, got %d", status)
	}
	if len(body) > maxCachedBodySize {
		return false, fmt.Errorf("idempotency: transaction response exceeds %d bytes", maxCachedBodySize)
	}

	encryptedBody, err := reservation.codec.seal(reservation.tokenID, reservation.key, status, body)
	if err != nil {
		return false, fmt.Errorf("idempotency: encrypt transaction response: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE idempotency_keys
		    SET response_status = $1, response_body = $2, completed_at = now()
		  WHERE token_id = $3 AND key = $4 AND completed_at IS NULL`,
		status, encryptedBody, reservation.tokenID, reservation.key,
	)
	if err != nil {
		return false, fmt.Errorf("idempotency: complete transaction response: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, errors.New("idempotency: active reservation disappeared before transaction completion")
	}
	return true, nil
}

func serveExistingIdempotent(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, codec *idempotencyCodec, tokenID, key, hashStr string) {
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
			w.Header().Set("Retry-After", retryAfterSeconds)
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
		w.Header().Set("Retry-After", retryAfterSeconds)
		WriteError(w, http.StatusConflict, ErrInvalidRequest, "request with this idempotency key is in flight")
		return
	}
	if respStatus == nil || respBody == nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "original response was too large to cache; retry without Idempotency-Key")
		return
	}
	plaintext, err := codec.open(tokenID, key, *respStatus, respBody)
	if err != nil {
		slog.Error("idempotency cache decrypt failed", "token_id", tokenID, "error", err)
		WriteError(w, http.StatusInternalServerError, ErrInternal, "cached response is unavailable; retry without Idempotency-Key")
		return
	}
	replayCachedResponse(w, *respStatus, plaintext)
}

func persistCompletion(ctx context.Context, pool *pgxpool.Pool, codec *idempotencyCodec, tokenID, key string, rec *responseRecorder) {
	// Cache only 2xx and only if the body fit. Non-2xx clears the reservation
	// so retries can succeed; an oversize 2xx is persisted with NULL body so
	// future replays return a clear error.
	if rec.status < 200 || rec.status >= 300 {
		if _, err := pool.Exec(ctx,
			`DELETE FROM idempotency_keys
			  WHERE token_id = $1 AND key = $2 AND completed_at IS NULL`,
			tokenID, key,
		); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("idempotency reservation release failed", "token_id", tokenID, "error", err)
		}
		return
	}

	if rec.tooLarge {
		markResponseUncacheable(ctx, pool, tokenID, key, rec.status, "oversize")
		return
	}

	encryptedBody, err := codec.seal(tokenID, key, rec.status, rec.body.Bytes())
	if err != nil {
		slog.Error("idempotency cache encrypt failed", "token_id", tokenID, "error", err)
		markResponseUncacheable(ctx, pool, tokenID, key, rec.status, "encryption failure")
		return
	}

	if _, err := pool.Exec(ctx,
		`UPDATE idempotency_keys SET response_status = $1, response_body = $2, completed_at = now()
		  WHERE token_id = $3 AND key = $4 AND completed_at IS NULL`,
		rec.status, encryptedBody, tokenID, key,
	); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("idempotency cache write failed", "token_id", tokenID, "error", err)
	}
}

func markResponseUncacheable(ctx context.Context, pool *pgxpool.Pool, tokenID, key string, status int, reason string) {
	if _, err := pool.Exec(ctx,
		`UPDATE idempotency_keys SET response_status = $1, response_body = NULL, completed_at = now()
		  WHERE token_id = $2 AND key = $3 AND completed_at IS NULL`,
		status, tokenID, key,
	); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("idempotency completion failed", "token_id", tokenID, "reason", reason, "error", err)
	}
}

func requestHashAndRestoreBody(r *http.Request) (string, error) {
	bodyBytes, err := io.ReadAll(r.Body)
	closeErr := r.Body.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	h := sha256.New()
	_, _ = io.WriteString(h, r.Method)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, r.URL.EscapedPath())
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, r.URL.RawQuery)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(bodyBytes)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func replayCachedResponse(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// #nosec G705 -- only previously generated JSON API responses are cached,
	// and the content type is forced to application/json above.
	_, _ = w.Write(body)
}

func captureResponse(next http.Handler, w http.ResponseWriter, r *http.Request) *responseRecorder {
	rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
	next.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
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
	if r.status != 0 {
		return
	}
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

// Unwrap lets net/http's ResponseController reach optional capabilities on the
// underlying writer.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

const encryptedResponsePrefix = "HWID1"

type idempotencyCodec struct {
	aead cipher.AEAD
}

type idempotencyReservation struct {
	codec   *idempotencyCodec
	tokenID string
	key     string
}

type idempotencyReservationContextKey struct{}

func newIdempotencyCodec(secret string) *idempotencyCodec {
	key := sha256.Sum256([]byte("hatchway/idempotency/v1\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic("idempotency: initialize AES: " + err.Error())
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("idempotency: initialize GCM: " + err.Error())
	}
	return &idempotencyCodec{aead: aead}
}

func (c *idempotencyCodec) seal(tokenID, key string, status int, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	out := make([]byte, 0, len(encryptedResponsePrefix)+len(nonce)+len(plaintext)+c.aead.Overhead())
	out = append(out, encryptedResponsePrefix...)
	out = append(out, nonce...)
	out = c.aead.Seal(out, nonce, plaintext, idempotencyAAD(tokenID, key, status))
	return out, nil
}

func (c *idempotencyCodec) open(tokenID, key string, status int, stored []byte) ([]byte, error) {
	if !bytes.HasPrefix(stored, []byte(encryptedResponsePrefix)) {
		// Compatibility for rows written before encrypted caching was added.
		// The retention sweeper removes these within the configured replay
		// window.
		return stored, nil
	}
	payload := stored[len(encryptedResponsePrefix):]
	if len(payload) < c.aead.NonceSize() {
		return nil, errors.New("encrypted response is truncated")
	}
	nonce := payload[:c.aead.NonceSize()]
	ciphertext := payload[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, idempotencyAAD(tokenID, key, status))
	if err != nil {
		return nil, fmt.Errorf("decrypt response: %w", err)
	}
	return plaintext, nil
}

func idempotencyAAD(tokenID, key string, status int) []byte {
	return fmt.Appendf(nil, "%s\x00%s\x00%d", tokenID, key, status)
}
