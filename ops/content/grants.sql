-- Run with content_owner (or administrator) AFTER `content migrate`.
\set ON_ERROR_STOP on
BEGIN;
GRANT USAGE ON SCHEMA content TO content_runtime;
GRANT SELECT ON content.schema_migrations, content.task_drafts TO content_runtime;
GRANT INSERT ON content.task_drafts TO content_runtime;
GRANT UPDATE(content, revision, state, review_id, rejection_reason, updated_at) ON content.task_drafts TO content_runtime;
GRANT SELECT, INSERT ON content.reviewed_tasks, content.reviewed_entries, content.asset_review_receipts TO content_runtime;
GRANT UPDATE(state, revision, updated_at, next_entry_position, catalog_revision) ON content.reviewed_tasks TO content_runtime;
GRANT UPDATE(state) ON content.reviewed_entries TO content_runtime;
GRANT SELECT, INSERT ON content.review_requests, content.review_outbox, content.review_decision_receipts TO content_runtime;
GRANT UPDATE(state, reason) ON content.review_requests TO content_runtime;
GRANT UPDATE(attempts, lease_token, available_at, delivered_at) ON content.review_outbox TO content_runtime;
GRANT SELECT, INSERT ON content.entry_submissions, content.entry_review_requests, content.entry_review_outbox, content.entry_decision_receipts TO content_runtime;
GRANT UPDATE(content,revision,state,review_id,rejection_reason,updated_at,approved_at) ON content.entry_submissions TO content_runtime;
GRANT UPDATE(state,reason) ON content.entry_review_requests TO content_runtime;
GRANT UPDATE(attempts,lease_token,available_at,delivered_at,blocked) ON content.entry_review_outbox TO content_runtime;
-- No snapshot/source/entry-identity updates or receipt rewrites.
-- No DELETE, DDL, author/key updates or cross-schema privileges.
GRANT SELECT,INSERT ON content.voting_catalog_heads,content.voting_catalog_changes TO content_runtime;
GRANT UPDATE(generation,active_change) ON content.voting_catalog_heads TO content_runtime;
GRANT UPDATE(stage,result,catalog,attempts,lease_token,available_at,completed_at) ON content.voting_catalog_changes TO content_runtime;
GRANT SELECT,INSERT ON content.asset_descriptors,content.comments,content.takedown_receipts,content.run_registrations,content.challenge_registrations TO content_runtime;
GRANT UPDATE(state) ON content.comments,content.run_registrations TO content_runtime;
COMMIT;
