ALTER TABLE tunnel_events ADD COLUMN remote_addr TEXT;
ALTER TABLE tunnel_events ADD COLUMN user_id UUID;

ALTER TABLE idempotency_keys
  DROP COLUMN completed_at,
  ALTER COLUMN response_body SET NOT NULL,
  ALTER COLUMN response_status SET NOT NULL;
