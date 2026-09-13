-- openlog:phase expand
-- 0009_update_requests: "Check now" / "Update now" requests from the UI to openlog-updater
-- (docs/contracts/releases-updates.md §5.1, docs/contracts/postgres.md "update_requests", D-041).
-- The api inserts a row; the updater claims pending rows every OPENLOG_UPDATER_REQUEST_POLL (10 s) with
-- FOR UPDATE SKIP LOCKED and records the result.
--
-- Mixed versions (releases-updates.md §6): older api binaries do not offer the buttons, older updaters never
-- read the table (pending rows expire after 15 minutes and are shown as such).

CREATE TABLE update_requests (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    action                    text NOT NULL CHECK (action IN ('check', 'apply')),
    -- apply: the version the admin confirmed; the updater installs it only if it is still the selected target.
    target_version            text NOT NULL DEFAULT '' CHECK (length(target_version) <= 100),
    ignore_maintenance_window boolean NOT NULL DEFAULT false,
    state                     text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'done', 'failed', 'expired')),
    message                   text NOT NULL DEFAULT '' CHECK (length(message) <= 2000),
    org_id                    uuid REFERENCES organizations (id) ON DELETE SET NULL,
    requested_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    requested_by_email        text NOT NULL DEFAULT '',
    requested_at              timestamptz NOT NULL DEFAULT now(),
    picked_at                 timestamptz,
    finished_at               timestamptz
);
CREATE INDEX update_requests_requested_at_idx ON update_requests (requested_at DESC);
CREATE INDEX update_requests_pending_idx ON update_requests (requested_at) WHERE state = 'pending';
