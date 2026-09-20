-- Run with content_owner (or administrator) AFTER `content migrate`.
\set ON_ERROR_STOP on
BEGIN;
GRANT USAGE ON SCHEMA content TO content_runtime;
GRANT SELECT ON content.schema_migrations, content.task_drafts TO content_runtime;
GRANT INSERT ON content.task_drafts TO content_runtime;
GRANT UPDATE(content, revision, updated_at) ON content.task_drafts TO content_runtime;
-- No DELETE, DDL, author/state/key updates or cross-schema privileges.
COMMIT;
