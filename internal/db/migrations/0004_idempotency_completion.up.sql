-- Idempotency: track in-flight requests so two concurrent POSTs with the same
-- key are serialized. response_status/response_body become nullable so a row
-- can be inserted as a reservation before the handler finishes; completed_at
-- distinguishes reserved-but-running from finished.
ALTER TABLE idempotency_keys
  ALTER COLUMN response_status DROP NOT NULL,
  ALTER COLUMN response_body DROP NOT NULL,
  ADD COLUMN completed_at TIMESTAMPTZ;

-- Drop unused columns. tunnel_events stores user-relevant metadata in payload
-- JSONB; the dedicated columns were never written.
ALTER TABLE tunnel_events DROP COLUMN user_id;
ALTER TABLE tunnel_events DROP COLUMN remote_addr;
