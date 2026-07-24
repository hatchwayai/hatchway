-- BYTEA cache entries may contain encrypted response bytes, which cannot be
-- converted back to the original JSONB representation. Idempotency rows are
-- short-lived replay caches, so a downgrade discards them and lets callers
-- establish fresh keys.
DELETE FROM idempotency_keys;

ALTER TABLE idempotency_keys
  ALTER COLUMN response_body TYPE JSONB USING convert_from(response_body, 'UTF8')::jsonb;
