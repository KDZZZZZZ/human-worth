-- Challenge 独占此 schema。当前提示随状态覆盖，结果回执不保存历史输入正文。
CREATE TABLE challenge.runs (
    id text PRIMARY KEY,
    task_id text NOT NULL,
    administrator_id text NOT NULL,
    parent_run_id text NOT NULL DEFAULT '',
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    status text NOT NULL CHECK (status IN ('queued','active','cancelling','finalizing','completed','failed','cancelled')),
    pending text NOT NULL,
    kind integer NOT NULL DEFAULT 0,
    leased boolean NOT NULL DEFAULT false,
    worker_id text NOT NULL DEFAULT '',
    lease_until timestamptz,
    deadline timestamptz NOT NULL,
    state bytea NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (administrator_id, operation, idempotency_key)
);
CREATE INDEX runs_ready ON challenge.runs (created_at, id) WHERE status = 'active' AND NOT leased;
CREATE INDEX runs_reconcile ON challenge.runs (updated_at) WHERE status IN ('queued','active','cancelling','finalizing');
-- 同一次领取（包括空结果）可恢复，响应丢失不会偷偷领取另一件工作。
CREATE TABLE challenge.claims (
    worker_id text NOT NULL,
    request_id text NOT NULL,
    capabilities_hash text NOT NULL,
    run_id text NOT NULL DEFAULT '',
    attempt_id text NOT NULL DEFAULT '',
    PRIMARY KEY (worker_id, request_id)
);
CREATE TABLE challenge.receipts (
    attempt_id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES challenge.runs(id),
    worker_id text NOT NULL,
    lease_epoch bigint NOT NULL,
    work_item_id text NOT NULL,
    result_digest text NOT NULL
);
-- 每次发送权有唯一请求键；未知结果保留预留，首版按模型调用次数精确计量。
CREATE TABLE challenge.model_calls (
    id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES challenge.runs(id),
    attempt_id text NOT NULL,
    request_id text NOT NULL,
    request_digest text NOT NULL,
    worker_id text NOT NULL,
    lease_epoch bigint NOT NULL,
    state text NOT NULL CHECK (state IN ('reserved','succeeded','failed','unknown')),
    UNIQUE (attempt_id, request_id)
);
CREATE TABLE challenge.audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
