package api

import "context"

type contextKey string

const (
	tokenIDKey contextKey = "token_id"
	userIDKey  contextKey = "user_id"
	isAdminKey contextKey = "is_admin"
)

// authInfo is a request-scoped container the auth middleware writes into so
// outer middleware (request logging, response wrappers) can observe the
// authenticated principal even though `r.WithContext` only propagates the new
// ctx downward.
type authInfo struct {
	tokenID string
	userID  string
	isAdmin bool
}

type authInfoCtxKey struct{}

// withAuthInfoContainer attaches an empty authInfo to the request context.
// Outer middleware (RequestLogMiddleware) calls this at the top of the chain.
func withAuthInfoContainer(ctx context.Context) (context.Context, *authInfo) {
	info := &authInfo{}
	return context.WithValue(ctx, authInfoCtxKey{}, info), info
}

// ContextWithAuth attaches the authenticated token and user IDs.
func ContextWithAuth(ctx context.Context, tokenID, userID string) context.Context {
	ctx = context.WithValue(ctx, tokenIDKey, tokenID)
	ctx = context.WithValue(ctx, userIDKey, userID)
	return ctx
}

// ContextWithAdmin attaches authentication IDs and administrator status.
func ContextWithAdmin(ctx context.Context, tokenID, userID string, isAdmin bool) context.Context {
	ctx = ContextWithAuth(ctx, tokenID, userID)
	ctx = context.WithValue(ctx, isAdminKey, isAdmin)
	return ctx
}

// TokenIDFromContext returns the authenticated API token ID, if present.
func TokenIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(tokenIDKey).(string)
	return v
}

// UserIDFromContext returns the authenticated user ID, if present.
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// IsAdminFromContext reports whether the authenticated user is an administrator.
func IsAdminFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(isAdminKey).(bool)
	return v
}
