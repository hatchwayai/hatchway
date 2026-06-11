package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdempotencyMiddleware_NoKey(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	handler := IdempotencyMiddleware(nil)(inner)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(`{}`))
	// No Idempotency-Key header — should pass through directly
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != `{"ok":true}` {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestIdempotencyMiddleware_NoTokenID(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	handler := IdempotencyMiddleware(nil)(inner)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(`{}`))
	r.Header.Set("Idempotency-Key", "test-key-123")
	// No token_id in context — should pass through
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRequestHashAndRestoreBody(t *testing.T) {
	body := `{"type":"http","local_port":3000}`
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(body))

	hash1, err := requestHashAndRestoreBody(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash1 == "" {
		t.Error("expected non-empty hash")
	}

	// Body should be restored and readable
	restored, _ := io.ReadAll(r.Body)
	if string(restored) != body {
		t.Errorf("body not restored: got %q", string(restored))
	}

	// Same body should produce same hash
	r2 := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(body))
	hash2, _ := requestHashAndRestoreBody(r2)
	if hash1 != hash2 {
		t.Errorf("hashes differ for same body: %s vs %s", hash1, hash2)
	}
}

func TestRequestHashAndRestoreBody_Empty(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(""))
	hash, err := requestHashAndRestoreBody(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash == "" {
		t.Error("expected hash for empty body")
	}
}

func TestReplayCachedResponse(t *testing.T) {
	w := httptest.NewRecorder()
	body := []byte(`{"tunnel_id":"t-abc"}`)
	replayCachedResponse(w, http.StatusCreated, body)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}
	if w.Body.String() != string(body) {
		t.Errorf("body mismatch: %s", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type: %s", ct)
	}
}

func TestCaptureResponse(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"tunnel_id":"t-abc"}`))
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(`{}`))
	rec := captureResponse(inner, w, r)

	if rec.status != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.status)
	}
	if rec.body.String() != `{"tunnel_id":"t-abc"}` {
		t.Errorf("captured body: %s", rec.body.String())
	}
}

func TestCaptureResponse_DefaultStatus(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`hello`))
		// No WriteHeader call — should default to 200
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	rec := captureResponse(inner, w, r)

	if rec.status != http.StatusOK {
		t.Errorf("expected default status 200, got %d", rec.status)
	}
}

func TestResponseRecorder_OversizeSetsFlag(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}

	// A single oversized Write must flip tooLarge and clear the buffer so we
	// never persist a partial prefix as the cached body.
	large := make([]byte, maxCachedBodySize+100)
	for i := range large {
		large[i] = 'x'
	}

	n, err := rec.Write(large)
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if n != len(large) {
		t.Errorf("short write: %d vs %d", n, len(large))
	}
	if !rec.tooLarge {
		t.Error("expected tooLarge=true after oversized write")
	}
	if rec.body.Len() != 0 {
		t.Errorf("expected body cleared on overflow, got len=%d", rec.body.Len())
	}
}

func TestResponseRecorder_OversizeAcrossChunksSetsFlag(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}

	// Two writes that individually fit but together overflow: the second one
	// must trip tooLarge so we don't replay only the first chunk.
	half := make([]byte, maxCachedBodySize/2+1)
	for i := range half {
		half[i] = 'a'
	}
	_, _ = rec.Write(half)
	_, _ = rec.Write(half)
	if !rec.tooLarge {
		t.Error("expected tooLarge=true after two writes overflowing the cap")
	}
}

func TestIdempotencyMiddleware_WithTokenID_NilPool(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	handler := IdempotencyMiddleware(nil)(inner)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(`{}`))
	r.Header.Set("Idempotency-Key", "key-123")
	r = r.WithContext(ContextWithAuth(r.Context(), "tok-1", "usr-1"))

	// nil pool will panic in fetchCachedResponse — recover and verify
	defer func() {
		if r := recover(); r == nil {
			// If no panic, the handler should have returned an error response
			if w.Code != http.StatusInternalServerError {
				t.Errorf("expected 500, got %d", w.Code)
			}
		}
	}()
	handler.ServeHTTP(w, r)
}

func TestIdempotencyMiddleware_BodyTooLarge(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run for an oversized idempotent request")
	})

	handler := IdempotencyMiddleware(nil)(inner)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(`{"too":"large"}`))
	r.Header.Set("Idempotency-Key", "key-123")
	r = r.WithContext(ContextWithAuth(r.Context(), "tok-1", "usr-1"))
	r.Body = http.MaxBytesReader(w, r.Body, 5)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", w.Code)
	}
}

func TestIdempotencyMiddleware_IgnoresEmptyKey(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := IdempotencyMiddleware(nil)(inner)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(`{}`))
	// Empty key should pass through
	r.Header.Set("Idempotency-Key", "")
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestMethodNeedsIdempotency(t *testing.T) {
	tests := []struct {
		method string
		want   bool
	}{
		{http.MethodGet, false},
		{http.MethodHead, false},
		{http.MethodOptions, false},
		{http.MethodPost, true},
		{http.MethodPut, true},
		{http.MethodPatch, true},
		{http.MethodDelete, true},
	}
	for _, tt := range tests {
		if got := methodNeedsIdempotency(tt.method); got != tt.want {
			t.Errorf("methodNeedsIdempotency(%q) = %v, want %v", tt.method, got, tt.want)
		}
	}
}

func TestIdempotencyMiddleware_GetSkipsMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Pass nil pool — middleware must not touch it for GET, even with a key.
	handler := IdempotencyMiddleware(nil)(inner)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/tunnels", nil)
	r.Header.Set("Idempotency-Key", "k1")
	r = r.WithContext(ContextWithAuth(r.Context(), "tok-1", "usr-1"))
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET should pass through, got %d", w.Code)
	}
}

func TestResponseRecorder_DefaultStatusOnWrite(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
	_, _ = rec.Write([]byte("hi"))
	if rec.status != http.StatusOK {
		t.Errorf("expected default status 200, got %d", rec.status)
	}
}

func TestResponseRecorder_HeaderThenWrite(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
	rec.WriteHeader(http.StatusAccepted)
	_, _ = rec.Write([]byte("body"))
	if rec.status != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.status)
	}
	if rec.body.String() != "body" {
		t.Errorf("body captured wrong: %q", rec.body.String())
	}
}

func TestReplayCachedResponse_Bytes(t *testing.T) {
	w := httptest.NewRecorder()
	replayCachedResponse(w, http.StatusTeapot, []byte("x"))
	if w.Code != http.StatusTeapot {
		t.Errorf("status = %d", w.Code)
	}
	if w.Body.String() != "x" {
		t.Errorf("body = %q", w.Body.String())
	}
}
