CREATE TABLE moderation.actions (
 id text PRIMARY KEY,
 actor_id text NOT NULL,
 target_kind text NOT NULL CHECK(target_kind IN ('task','entry','comment')),
 target_id text NOT NULL,
 reason text NOT NULL,
 create_key text NOT NULL,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','applied','blocked')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 applied_at timestamptz,
 lease_token text,
 available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(actor_id,create_key)
);
CREATE INDEX actions_pending ON moderation.actions(available_at,id) WHERE state='pending';
CREATE TABLE moderation.reports (
 id text PRIMARY KEY,
 reporter_id text NOT NULL,
 target_kind text NOT NULL CHECK(target_kind IN ('task','entry','comment')),
 target_id text NOT NULL,
 reason text NOT NULL,
 create_key text NOT NULL,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','dismissed','actioned')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 resolution_action text CHECK(resolution_action IN ('dismiss','take_down')),
 resolution_reason text,
 resolver_id text,
 resolved_at timestamptz,
 action_id text REFERENCES moderation.actions(id),
 UNIQUE(reporter_id,create_key)
);
CREATE INDEX reports_status_order ON moderation.reports(status,created_at DESC,id DESC);

ALTER TABLE moderation.reviews DROP CONSTRAINT reviews_entry_identity_check;
ALTER TABLE moderation.reviews ADD CONSTRAINT reviews_entry_identity_check CHECK (
 object_kind='task' OR (
 object_id ~ '^ent_[a-f0-9]{32}$'
 AND (snapshot->'submission'->>'taskId') IS NOT DISTINCT FROM task_id
 AND (snapshot->'submission'->>'origin') IN ('additional','cloud')
 AND (snapshot->>'reviewId') IS NOT DISTINCT FROM id
 AND (snapshot->'submission'->>'reviewId') IS NOT DISTINCT FROM id
 AND (snapshot->'submission'->>'revision') IS NOT DISTINCT FROM submission_revision::text
 ));
