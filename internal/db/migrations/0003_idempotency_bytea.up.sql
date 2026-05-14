-- response_body stored as JSONB gets re-serialized on read, so byte-for-byte
-- replay isn't preserved (key order, whitespace differ). Switch to BYTEA so
-- the cached body is the literal handler output.
ALTER TABLE idempotency_keys
  ALTER COLUMN response_body TYPE BYTEA USING response_body::text::bytea;
