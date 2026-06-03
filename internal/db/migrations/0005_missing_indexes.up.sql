-- lookupRuntimeToken queries tunnel_runtime_tokens by token_prefix alone
-- (frps Login/NewProxy callbacks, the hottest read path in the plugin
-- handler). The existing idx_tunnel_runtime_tokens_tunnel_prefix index has
-- tunnel_id as its leading column, so it doesn't support this lookup.
--
-- The old composite index is kept, not dropped: revokeRuntimeTokens
-- (handlers.go) queries `WHERE tunnel_id = $1`, which still needs an
-- index leading with tunnel_id.
CREATE INDEX idx_tunnel_runtime_tokens_token_prefix ON tunnel_runtime_tokens(token_prefix);

-- sweepIdempotencyKeys deletes by created_at every hour; without an index on
-- that column the sweep is a full table scan each run.
CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys(created_at);
