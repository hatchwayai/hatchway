CREATE TABLE users (
  id UUID PRIMARY KEY,
  email TEXT UNIQUE,
  name TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE api_tokens (
  id UUID PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES users(id),
  name TEXT NOT NULL,
  token_prefix TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE tunnels (
  id TEXT PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES users(id),
  type TEXT NOT NULL,
  public_host TEXT,
  public_port INTEGER,
  local_host TEXT NOT NULL,
  local_port INTEGER NOT NULL,
  status TEXT NOT NULL,
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE tunnel_runtime_tokens (
  id UUID PRIMARY KEY,
  tunnel_id TEXT NOT NULL REFERENCES tunnels(id),
  token_prefix TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  last_used_at TIMESTAMPTZ,
  use_count BIGINT NOT NULL DEFAULT 0,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE tunnel_events (
  id BIGSERIAL PRIMARY KEY,
  tunnel_id TEXT NOT NULL,
  user_id UUID,
  event_type TEXT NOT NULL,
  remote_addr TEXT,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE idempotency_keys (
  token_id UUID NOT NULL REFERENCES api_tokens(id),
  key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  response_status INTEGER NOT NULL,
  response_body JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (token_id, key)
);
-- Indexes
CREATE INDEX idx_api_tokens_token_prefix ON api_tokens(token_prefix);
CREATE INDEX idx_tunnel_runtime_tokens_tunnel_prefix ON tunnel_runtime_tokens(tunnel_id, token_prefix);
CREATE INDEX idx_tunnels_user_status ON tunnels(user_id, status);
CREATE INDEX idx_tunnels_expires ON tunnels(expires_at)
WHERE status IN ('reserved', 'active', 'closed');
CREATE INDEX idx_tunnel_events_tunnel_created ON tunnel_events(tunnel_id, created_at);
CREATE INDEX idx_tunnel_events_created ON tunnel_events(created_at);