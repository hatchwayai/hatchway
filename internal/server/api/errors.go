package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// ErrorCode is a stable, machine-readable API error identifier.
type ErrorCode string

// Stable API error codes.
const (
	ErrUnauthenticated ErrorCode = "UNAUTHENTICATED"
	ErrForbidden       ErrorCode = "FORBIDDEN"
	ErrNotFound        ErrorCode = "NOT_FOUND"
	ErrRateLimited     ErrorCode = "RATE_LIMITED"
	ErrQuotaExceeded   ErrorCode = "QUOTA_EXCEEDED"
	ErrInvalidRequest  ErrorCode = "INVALID_REQUEST"
	ErrInternal        ErrorCode = "INTERNAL"
)

// ErrorResponse is the JSON envelope returned for API failures.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody contains the stable code and human-readable failure message.
type ErrorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// WriteError writes a JSON error response with the supplied HTTP status.
func WriteError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(ErrorResponse{Error: ErrorBody{Code: code, Message: message}}); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("error response encode failed", "status", status, "code", string(code), "error", err)
	}
}
