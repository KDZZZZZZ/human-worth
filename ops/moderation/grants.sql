\set ON_ERROR_STOP on
BEGIN;
GRANT USAGE ON SCHEMA moderation TO moderation_runtime;
GRANT SELECT ON moderation.schema_migrations,moderation.reviews,moderation.decision_outbox TO moderation_runtime;
GRANT INSERT ON moderation.reviews,moderation.decision_outbox TO moderation_runtime;
GRANT UPDATE(status,decision,decision_id,reason,reviewer_id,decided_at,delivery_state,receipt) ON moderation.reviews TO moderation_runtime;
GRANT UPDATE(attempts,lease_token,available_at,delivered_at,blocked) ON moderation.decision_outbox TO moderation_runtime;
-- object_kind is supplied only at INSERT; object_id is generated from immutable
-- task/snapshot identity. No UPDATE grants for either column are needed.
-- No DDL/DELETE, snapshot/source/created_at rewrite or cross-schema access.
GRANT SELECT,INSERT ON moderation.actions,moderation.reports TO moderation_runtime;
GRANT UPDATE(state,applied_at,lease_token,available_at) ON moderation.actions TO moderation_runtime;
GRANT UPDATE(resolution_action,resolution_reason,resolver_id,action_id,status,resolved_at) ON moderation.reports TO moderation_runtime;
COMMIT;
