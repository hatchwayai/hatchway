ALTER TABLE tunnel_events ADD COLUMN remote_addr TEXT;
ALTER TABLE tunnel_events ADD COLUMN user_id UUID;

-- In-flight reservations and completed-but-uncacheable responses cannot be
-- represented by the pre-0004 NOT NULL schema. They are safe to discard:
-- callers can retry after a rollback.
DELETE FROM idempotency_keys
WHERE response_status IS NULL OR response_body IS NULL;

ALTER TABLE idempotency_keys
  DROP COLUMN completed_at,
  ALTER COLUMN response_body SET NOT NULL,
  ALTER COLUMN response_status SET NOT NULL;
