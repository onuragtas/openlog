-- openlog:phase expand
-- 0063_sso_sessions_index: OIDC back-channel and front-channel logout (D-098) end the SSO sessions of an IdP session
-- by its OIDC `sid` (sso_sessions.session_index) without scanning every session of the connection
-- (docs/contracts/postgres.md "Single sign-on and SCIM").
--
-- Mixed versions: older api binaries do not use the index; it only costs the index maintenance on their inserts.

CREATE INDEX IF NOT EXISTS sso_sessions_session_index_idx ON sso_sessions (connection_id, session_index) WHERE session_index <> '';
