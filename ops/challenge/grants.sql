-- Run as challenge_owner after the migration. No cross-schema access.
\set ON_ERROR_STOP on
BEGIN;
GRANT USAGE ON SCHEMA challenge TO challenge_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA challenge TO challenge_runtime;
GRANT INSERT, UPDATE ON challenge.runs, challenge.model_calls TO challenge_runtime;
GRANT INSERT ON challenge.claims, challenge.receipts, challenge.audit_events TO challenge_runtime;
GRANT USAGE ON SEQUENCE challenge.audit_events_id_seq TO challenge_runtime;
COMMIT;
