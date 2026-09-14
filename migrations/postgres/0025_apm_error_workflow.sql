-- openlog:phase expand
-- 0025_apm_error_workflow: error inbox workflow (docs/contracts/apm.md §3.4, postgres.md "APM error workflow"):
-- per error group status (unresolved / resolved / ignored), assignee, regression bookkeeping and comments,
-- and the alert rule type apm_error (alerting.md §2.9).
--
-- A group without a row is unresolved and unassigned. group_id is the 16 hex digit ClickHouse error group id
-- (apm.md §3.1); it already encodes the service, the service columns are kept for filtering.
--
-- Mixed versions: an older api ignores these tables (every group shows as before); an older alert binary loads an
-- apm_error rule without a condition ("rule type is not available") and never creates one.

CREATE TABLE apm_error_group_states (
    org_id                  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    group_id                text NOT NULL CHECK (group_id ~ '^[0-9a-f]{16}$'),
    service_name            text NOT NULL CHECK (length(service_name) BETWEEN 1 AND 512),
    service_namespace       text NOT NULL DEFAULT '' CHECK (length(service_namespace) <= 512),
    deployment_environment  text NOT NULL DEFAULT '' CHECK (length(deployment_environment) <= 512),
    status                  text NOT NULL DEFAULT 'unresolved' CHECK (status IN ('unresolved', 'resolved', 'ignored')),
    assignee_user_id        uuid REFERENCES users (id) ON DELETE SET NULL,
    resolved_at             timestamptz,
    resolved_in_version     text NOT NULL DEFAULT '' CHECK (length(resolved_in_version) <= 256),
    resolved_by             uuid REFERENCES users (id) ON DELETE SET NULL,
    regressed_at            timestamptz,
    regression_count        integer NOT NULL DEFAULT 0 CHECK (regression_count >= 0),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    updated_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (org_id, group_id),
    CHECK ((status = 'resolved') = (resolved_at IS NOT NULL)),
    CHECK (status = 'resolved' OR resolved_in_version = '')
);
CREATE INDEX apm_error_group_states_status_idx ON apm_error_group_states (org_id, status, updated_at DESC);
CREATE INDEX apm_error_group_states_assignee_idx ON apm_error_group_states (org_id, assignee_user_id)
    WHERE assignee_user_id IS NOT NULL;
CREATE INDEX apm_error_group_states_service_idx ON apm_error_group_states
    (org_id, service_name, service_namespace, deployment_environment);

CREATE TABLE apm_error_group_comments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    group_id        text NOT NULL CHECK (group_id ~ '^[0-9a-f]{16}$'),
    author_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    author_email    text NOT NULL DEFAULT '',
    body            text NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX apm_error_group_comments_group_idx ON apm_error_group_comments (org_id, group_id, created_at);

-- Activity of a group is read from audit_log (target_type apm_error_group, target_id = group id).
CREATE INDEX IF NOT EXISTS audit_log_target_idx ON audit_log (org_id, target_type, target_id, created_at DESC);

-- Rule type apm_error. 'oql' (query language rules, built in parallel) is accepted too, so the order in which the
-- two expand migrations run does not matter.
ALTER TABLE alert_rules DROP CONSTRAINT IF EXISTS alert_rules_type_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_type_check
    CHECK (type IN ('metric_threshold', 'log_match', 'no_data', 'discovery', 'apm', 'apm_no_data', 'apm_error', 'oql'));
