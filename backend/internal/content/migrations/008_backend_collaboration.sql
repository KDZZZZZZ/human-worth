CREATE TABLE content.asset_descriptors (
 asset_id text PRIMARY KEY,
 metadata jsonb NOT NULL CHECK(jsonb_typeof(metadata)='object')
);

CREATE TABLE content.comments (
 id text PRIMARY KEY,
 task_id text NOT NULL REFERENCES content.reviewed_tasks(task_id),
 author_id text NOT NULL,
 body text NOT NULL CHECK(length(btrim(body))>0 AND octet_length(body)<=8000),
 create_key text NOT NULL,
 state text NOT NULL DEFAULT 'visible' CHECK(state IN ('visible','taken_down')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(author_id,task_id,create_key)
);
CREATE INDEX comments_task_order ON content.comments(task_id,created_at,id) WHERE state='visible';
CREATE TABLE content.takedown_receipts (
 decision_id text PRIMARY KEY,
 target_kind text NOT NULL,
 target_id text NOT NULL,
 task_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE content.entry_submissions DROP CONSTRAINT entry_submissions_origin_check;
ALTER TABLE content.entry_submissions ADD CONSTRAINT entry_submissions_origin_check CHECK(origin IN ('additional','cloud'));
ALTER TABLE content.reviewed_entries DROP CONSTRAINT reviewed_entries_origin_check;
ALTER TABLE content.reviewed_entries ADD CONSTRAINT reviewed_entries_origin_check CHECK(origin IN ('initial','additional','cloud'));
ALTER TABLE content.reviewed_entries DROP CONSTRAINT reviewed_entries_projection_check;
ALTER TABLE content.reviewed_entries ADD CONSTRAINT reviewed_entries_projection_check CHECK (
 (origin='initial' AND author_id IS NULL AND content IS NULL) OR
 (origin IN ('additional','cloud') AND author_id IS NOT NULL AND content IS NOT NULL AND jsonb_typeof(content)='object')
);
CREATE TABLE content.run_registrations (
 run_id text PRIMARY KEY,
 task_id text NOT NULL,
 state text NOT NULL CHECK(state IN ('open','closed')),
 administrator_id text,
 executor_model text
);
CREATE TABLE content.challenge_registrations (
 run_id text NOT NULL REFERENCES content.run_registrations(run_id),
 candidate_id text NOT NULL,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 entry_id text NOT NULL UNIQUE REFERENCES content.entry_submissions(id),
 PRIMARY KEY(run_id,candidate_id)
);

ALTER TABLE content.voting_catalog_changes DROP CONSTRAINT voting_catalog_changes_method_check;
ALTER TABLE content.voting_catalog_changes ADD CONSTRAINT voting_catalog_changes_method_check CHECK(method IN ('task','entry','legacy','bootstrap','takedown'));
