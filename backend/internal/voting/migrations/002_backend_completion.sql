-- Append-only upgrade: preserve all votes, immutable closures and protocol receipts.
ALTER TABLE voting.catalog_gates ADD COLUMN activated_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE voting.current_votes ADD COLUMN generation bigint NOT NULL DEFAULT 0;
CREATE TABLE voting.conclusions (
 task_id text PRIMARY KEY REFERENCES voting.catalog_gates(task_id),
 conclusion text NOT NULL CHECK(conclusion IN ('undetermined','human_leading','agent_leading')),
 last_leading text NOT NULL DEFAULT '',
 flip_count bigint NOT NULL DEFAULT 0 CHECK(flip_count>=0),
 last_flip_at timestamptz,
 changed_at timestamptz NOT NULL
);
CREATE TABLE voting.conclusion_events (
 id bigserial PRIMARY KEY,
 task_id text NOT NULL REFERENCES voting.catalog_gates(task_id),
 catalog_revision bigint NOT NULL,
 previous_conclusion text NOT NULL,
 conclusion text NOT NULL,
 is_flip boolean NOT NULL,
 recorded_at timestamptz NOT NULL
);
CREATE INDEX conclusion_events_task_time ON voting.conclusion_events(task_id,recorded_at,id);
CREATE TABLE voting.preference_snapshots (
 id text PRIMARY KEY,
 run_id text NOT NULL,
 purpose text NOT NULL CHECK(purpose IN ('training','validation')),
 batch_index integer NOT NULL CHECK(batch_index>=0),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 payload bytea NOT NULL,
 generations jsonb NOT NULL,
 UNIQUE(run_id,purpose,batch_index)
);
CREATE TABLE voting.preference_assignments (
 run_id text NOT NULL,
 task_id text NOT NULL REFERENCES voting.catalog_gates(task_id),
 snapshot_id text NOT NULL REFERENCES voting.preference_snapshots(id),
 PRIMARY KEY(run_id,task_id)
);
