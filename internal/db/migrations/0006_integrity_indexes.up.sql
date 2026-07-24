-- Support the owner-scoped keyset pagination query used by GET /v1/tunnels.
CREATE INDEX idx_tunnels_user_created_id
  ON tunnels(user_id, created_at DESC, id DESC);

-- Support the two branches of the dead runtime-token retention sweep.
CREATE INDEX idx_tunnel_runtime_tokens_revoked_at
  ON tunnel_runtime_tokens(revoked_at)
  WHERE revoked_at IS NOT NULL;
CREATE INDEX idx_tunnel_runtime_tokens_live_expires_at
  ON tunnel_runtime_tokens(expires_at)
  WHERE revoked_at IS NULL;

-- Preserve application invariants even when operators use administrative SQL.
ALTER TABLE tunnels
  ADD CONSTRAINT tunnels_local_port_valid CHECK (local_port BETWEEN 1 AND 65535),
  ADD CONSTRAINT tunnels_status_valid CHECK (status IN ('reserved', 'active', 'closed', 'expired', 'revoked'));

ALTER TABLE tunnel_runtime_tokens
  ADD CONSTRAINT tunnel_runtime_tokens_use_count_valid CHECK (use_count >= 0);

-- Releases through v5 could contain completed replay rows whose completed_at
-- remained NULL after migration 0004. Repair those historical rows before
-- enforcing the completion invariant. Rows with a missing status or body are
-- genuine in-flight/uncacheable states and must retain their existing shape.
UPDATE idempotency_keys
SET completed_at = created_at
WHERE completed_at IS NULL
  AND response_status IS NOT NULL
  AND response_body IS NOT NULL;

ALTER TABLE idempotency_keys
  ADD CONSTRAINT idempotency_keys_completion_valid CHECK (
    (completed_at IS NULL AND response_status IS NULL AND response_body IS NULL)
    OR
    (completed_at IS NOT NULL AND response_status IS NOT NULL AND response_status BETWEEN 100 AND 599)
  );
