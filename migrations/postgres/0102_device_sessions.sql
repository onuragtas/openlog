-- openlog:phase expand
-- 0102_device_sessions: a session that a mobile application holds as a bearer token (docs/plan/11-mobile-console.md §3.2).
--
-- A column on sessions rather than a table of its own. A device session *is* a user session -- same user, same
-- membership role, same revocation -- and splitting it off would mean a second lookup path, a second revocation
-- path and a "sign out everywhere" that forgets half the places you are signed in. With a column, GET
-- /api/v1/sessions lists phones beside browsers and DELETE /api/v1/sessions/{id} signs them out, both unchanged.
--
-- kind is what the two differ by, and it is what auth.Allow reads: a device principal is refused every operation
-- marked UserOnly (members, invitations, keys, fleet, updates), so a stolen phone cannot take over the
-- installation. The browser default keeps every row written by an older binary exactly what it was.
--
-- device_name is what the person sees in that list ("Onur's iPhone"); it is chosen by the client and never
-- interpreted, so it is bounded rather than trusted.
--
-- Mixed versions: older binaries do not select these columns and keep issuing browser sessions.

ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'browser',
    ADD COLUMN IF NOT EXISTS device_name text NOT NULL DEFAULT '';

ALTER TABLE sessions
    ADD CONSTRAINT sessions_kind CHECK (kind IN ('browser', 'device')),
    -- A device name is a label, not a field to hide a payload in.
    ADD CONSTRAINT sessions_device_name_len CHECK (length(device_name) <= 100),
    -- Only a device session carries a name, so an empty name cannot be read as "unnamed phone".
    ADD CONSTRAINT sessions_device_name_kind CHECK (kind = 'device' OR device_name = '');
