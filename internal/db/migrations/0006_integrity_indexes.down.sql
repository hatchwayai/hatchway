ALTER TABLE idempotency_keys
  DROP CONSTRAINT idempotency_keys_completion_valid;

ALTER TABLE tunnel_runtime_tokens
  DROP CONSTRAINT tunnel_runtime_tokens_use_count_valid;

ALTER TABLE tunnels
  DROP CONSTRAINT tunnels_status_valid,
  DROP CONSTRAINT tunnels_local_port_valid;

DROP INDEX idx_tunnel_runtime_tokens_live_expires_at;
DROP INDEX idx_tunnel_runtime_tokens_revoked_at;
DROP INDEX idx_tunnels_user_created_id;
