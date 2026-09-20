-- Run ONCE with a database administrator, on the chosen application database.
-- psql reads passwords from the environment; never commit real credentials.
\set ON_ERROR_STOP on
\getenv content_owner_password CONTENT_OWNER_PASSWORD
\getenv content_runtime_password CONTENT_RUNTIME_PASSWORD
BEGIN;
CREATE ROLE content_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD :'content_owner_password';
CREATE ROLE content_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD :'content_runtime_password';
CREATE SCHEMA content AUTHORIZATION content_owner;
REVOKE ALL ON SCHEMA content FROM PUBLIC;
DO $$
BEGIN
    IF has_database_privilege('content_owner', current_database(), 'CREATE')
       OR has_database_privilege('content_runtime', current_database(), 'CREATE')
       OR has_schema_privilege('content_runtime', 'public', 'CREATE') THEN
        RAISE EXCEPTION 'Database/public schema grants CREATE to these roles; review PUBLIC privileges before provisioning Content';
    END IF;
END
$$;
COMMIT;
-- No grants on Identity or other schemas. The database must not grant CREATE
-- to PUBLIC and public schema must not grant CREATE to PUBLIC (PG18 defaults).
