CREATE TABLE identity.accounts (
    id text PRIMARY KEY,
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 100),
    role text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'disabled')),
    auth_version bigint NOT NULL DEFAULT 1 CHECK (auth_version > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE identity.external_identities (
    issuer text NOT NULL,
    subject text NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),
    account_id text NOT NULL REFERENCES identity.accounts(id),
    email text NOT NULL DEFAULT '',
    email_verified boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (issuer, subject)
);
-- Serialize replacement even if two starts carry the same older flow cookie.
CREATE TABLE identity.login_families (
    id text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE identity.login_flows (
    id text PRIMARY KEY,
    family_id text NOT NULL REFERENCES identity.login_families(id) ON DELETE CASCADE,
    cookie_hash bytea NOT NULL UNIQUE CHECK (octet_length(cookie_hash) = 32),
    state_hash bytea NOT NULL CHECK (octet_length(state_hash) = 32),
    nonce_hash bytea NOT NULL CHECK (octet_length(nonce_hash) = 32),
    verifier_cipher text,
    config_version text NOT NULL,
    redirect_uri text NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'exchanging', 'succeeded', 'failed', 'expired', 'cancelled')),
    attempt_id text,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (status IN ('pending', 'exchanging') OR verifier_cipher IS NULL)
);
CREATE INDEX login_flows_family ON identity.login_flows(family_id);
CREATE INDEX login_flows_expiry ON identity.login_flows(expires_at);
CREATE TABLE identity.credentials (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES identity.accounts(id),
    kind text NOT NULL CHECK (kind IN ('web', 'mcp')),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    auth_version bigint NOT NULL,
    csrf_cipher text,
    name text,
    create_request_id text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    UNIQUE (account_id, create_request_id),
    CHECK ((kind = 'web' AND csrf_cipher IS NOT NULL AND name IS NULL AND create_request_id IS NULL)
        OR (kind = 'mcp' AND csrf_cipher IS NULL AND name IS NOT NULL AND create_request_id IS NOT NULL))
);
CREATE INDEX credentials_account ON identity.credentials(account_id, id);
CREATE TABLE identity.audit_events (
    id text PRIMARY KEY,
    actor_id text NOT NULL,
    action text NOT NULL,
    target_id text NOT NULL,
    reason text NOT NULL DEFAULT '',
    auth_version bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz
);
-- This table is the local audit/outbox. Moderation delivery is added with that service.
CREATE INDEX audit_outbox ON identity.audit_events(created_at) WHERE delivered_at IS NULL;
