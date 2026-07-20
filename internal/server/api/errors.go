package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

type ErrorCode string

const (
	ErrUnauthenticated ErrorCode = "UNAUTHENTICATED"
	ErrForbidden       ErrorCode = "FORBIDDEN"
	ErrNotFound        ErrorCode = "NOT_FOUND"
	ErrRateLimited     ErrorCode = "RATE_LIMITED"
	ErrQuotaExceeded   ErrorCode = "QUOTA_EXCEEDED"
	ErrInvalidRequest  ErrorCode = "INVALID_REQUEST"
	ErrInternal        ErrorCode = "INTERNAL"
)

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func WriteError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(ErrorResponse{Error: ErrorBody{Code: code, Message: message}}); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("error response encode failed", "status", status, "code", string(code), "error", err)
	}
}
