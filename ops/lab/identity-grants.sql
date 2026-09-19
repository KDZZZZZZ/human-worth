GRANT USAGE ON SCHEMA identity TO identity_runtime, identity_operator;
GRANT SELECT ON ALL TABLES IN SCHEMA identity TO identity_runtime;
GRANT INSERT(id,display_name), UPDATE(display_name,updated_at) ON identity.accounts TO identity_runtime;
GRANT INSERT,UPDATE,DELETE ON identity.external_identities,identity.credentials,identity.login_families,identity.login_flows TO identity_runtime;
GRANT INSERT ON identity.audit_events TO identity_runtime;
GRANT SELECT, UPDATE(role,state,auth_version,updated_at) ON identity.accounts TO identity_operator;
GRANT INSERT ON identity.audit_events TO identity_operator;
