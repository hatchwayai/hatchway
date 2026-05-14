package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zydo/hatchway/internal/tokens"
)

// TokenCandidate is a single row matching a token prefix lookup. Multiple
// candidates may share a prefix (low probability but possible), so the auth
// middleware verifies the hash against each one in turn.
type TokenCandidate struct {
	TokenID string
	UserID  string
	Hash    string
	IsAdmin bool
}

// TokenLookup returns all DB rows whose token_prefix matches. pgx.ErrNoRows
// is treated by callers as "no candidates" (equivalent to an empty slice);
// other errors are surfaced as 401 without leaking the internal reason.
type TokenLookup func(ctx context.Context, prefix string) ([]TokenCandidate, error)

var (
	errMissingAuthHeader       = errors.New("missing Authorization header")
	errInvalidAuthHeaderFormat = errors.New("invalid Authorization header format")
	errInvalidToken            = errors.New("invalid token")
	errTokenLookupFailed       = errors.New("token lookup failed")
)

func AuthMiddleware(lookup TokenLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, errMsg := authenticateRequest(r, lookup)
			if errMsg != nil {
				WriteError(w, http.StatusUnauthorized, ErrUnauthenticated, errMsg.Error())
				return
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func authenticateRequest(r *http.Request, lookup TokenLookup) (context.Context, error) {
	raw, err := extractBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return nil, err
	}

	if tokens.KindOf(raw) != tokens.KindAPI {
		return nil, errInvalidToken
	}

	prefix, err := tokens.ParsePrefix(raw)
	if err != nil {
		return nil, errInvalidToken
	}

	candidates, err := lookupCandidates(r.Context(), lookup, prefix)
	if err != nil {
		return nil, err
	}

	for _, c := range candidates {
		if tokens.VerifyToken(raw, c.Hash) {
			// Propagate the request's auth info up through the outer
			// RequestLogMiddleware via the shared container, so request logs
			// can include token_id even though the new ctx is only visible
			// to handlers below this middleware.
			if container, ok := r.Context().Value(authInfoCtxKey{}).(*authInfo); ok {
				container.tokenID = c.TokenID
				container.userID = c.UserID
				container.isAdmin = c.IsAdmin
			}
			return ContextWithAdmin(r.Context(), c.TokenID, c.UserID, c.IsAdmin), nil
		}
	}
	return nil, errInvalidToken
}

// AdminOnly wraps a handler so it only runs when the authenticated principal has is_admin=true.
// Returns 403 FORBIDDEN otherwise. AuthMiddleware must be applied before this in the chain.
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAdminFromContext(r.Context()) {
			WriteError(w, http.StatusForbidden, ErrForbidden, "admin privileges required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearerToken(header string) (string, error) {
	if header == "" {
		return "", errMissingAuthHeader
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", errInvalidAuthHeaderFormat
	}

	return parts[1], nil
}

func lookupCandidates(ctx context.Context, lookup TokenLookup, prefix string) ([]TokenCandidate, error) {
	candidates, err := lookup(ctx, prefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errInvalidToken
	}
	if err != nil {
		return nil, errTokenLookupFailed
	}
	if len(candidates) == 0 {
		return nil, errInvalidToken
	}
	return candidates, nil
}
