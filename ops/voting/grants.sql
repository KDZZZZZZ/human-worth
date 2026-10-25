\set ON_ERROR_STOP on
BEGIN;
GRANT USAGE ON SCHEMA voting TO voting_runtime;
GRANT SELECT ON voting.schema_migrations TO voting_runtime;
GRANT SELECT,INSERT ON voting.catalog_gates,voting.catalog_changes,voting.account_task_guards,voting.statistics_access,voting.ballots,voting.current_votes TO voting_runtime;
GRANT UPDATE(generation,state,change_id,sha256,snapshot,tombstone) ON voting.catalog_gates TO voting_runtime;
GRANT UPDATE(phase,sha256) ON voting.catalog_changes TO voting_runtime;
-- FOR UPDATE on guards needs a permitted UPDATE column; identity is never changed.
GRANT UPDATE(lock_marker) ON voting.account_task_guards TO voting_runtime;
GRANT UPDATE(entry_id,revision,request_hash,updated_at) ON voting.current_votes TO voting_runtime;
GRANT DELETE ON voting.ballots TO voting_runtime;
-- No UPDATE/DELETE of permanent closure facts, no DDL/cross-schema privileges.
GRANT UPDATE(activated_at) ON voting.catalog_gates TO voting_runtime;
GRANT UPDATE(generation) ON voting.current_votes TO voting_runtime;
GRANT SELECT,INSERT ON voting.conclusions,voting.conclusion_events,voting.preference_snapshots,voting.preference_assignments TO voting_runtime;
GRANT UPDATE(conclusion,last_leading,flip_count,last_flip_at,changed_at) ON voting.conclusions TO voting_runtime;
GRANT USAGE ON SEQUENCE voting.conclusion_events_id_seq TO voting_runtime;
COMMIT;
