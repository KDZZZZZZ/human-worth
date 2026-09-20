-- Initial entry drafts belong to the task aggregate and are replaced atomically.
-- No FK to Identity: account identity is supplied by VerifyActor over mTLS.
CREATE TABLE content.task_drafts (
    id text PRIMARY KEY,
    author_id text NOT NULL,
    create_key text NOT NULL CHECK (length(create_key) BETWEEN 16 AND 128),
    create_hash bytea NOT NULL CHECK (octet_length(create_hash) = 32),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    state text NOT NULL DEFAULT 'draft' CHECK (state = 'draft'),
    content jsonb NOT NULL CHECK (jsonb_typeof(content) = 'object'
        AND content ? 'entries' AND jsonb_typeof(content->'entries') = 'array'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (author_id, create_key)
);
-- No public projection or publication transition exists in this migration.
