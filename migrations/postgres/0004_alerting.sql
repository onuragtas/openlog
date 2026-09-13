-- openlog:phase expand
-- 0004_alerting: alert rules, rule leases (evaluator sharding), per-series evaluation state,
-- incidents and their timeline, notification channels (encrypted secrets), mute windows,
-- the notification outbox and the delivery log.
-- See docs/contracts/alerting.md and docs/contracts/postgres.md "Alerting".

CREATE TABLE alert_channels (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    type             text NOT NULL CHECK (type IN ('slack', 'email', 'webhook', 'teams')),
    enabled          boolean NOT NULL DEFAULT true,
    -- Non-secret settings (e-mail recipients, SMTP override without password).
    config           jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- AES-256-GCM: "ol1:<key id>:<base64(nonce|ciphertext|tag)>" of a JSON object; '' = no secrets.
    secrets          text NOT NULL DEFAULT '',
    secrets_key_id   text NOT NULL DEFAULT '',
    -- Masked representations shown by the API ({"url": "https://hooks.slack.com/…/•••f3a9"}).
    secret_hints     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alert_channels_org_idx ON alert_channels (org_id, name);
CREATE INDEX alert_channels_key_idx ON alert_channels (secrets_key_id) WHERE secrets <> '';

CREATE TABLE alert_rules (
    id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                     uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name                       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description                text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    type                       text NOT NULL CHECK (type IN ('metric_threshold', 'log_match', 'no_data', 'discovery', 'apm')),
    severity                   text NOT NULL DEFAULT 'warning' CHECK (severity IN ('critical', 'warning', 'info')),
    enabled                    boolean NOT NULL DEFAULT true,
    interval_seconds           integer NOT NULL CHECK (interval_seconds BETWEEN 10 AND 3600),
    for_seconds                integer NOT NULL DEFAULT 0 CHECK (for_seconds BETWEEN 0 AND 86400),
    recovery_for_seconds       integer NOT NULL DEFAULT 0 CHECK (recovery_for_seconds BETWEEN 0 AND 86400),
    condition                  jsonb NOT NULL,
    renotify_interval_seconds  integer NOT NULL DEFAULT 0 CHECK (renotify_interval_seconds >= 0),
    flapping                   jsonb NOT NULL DEFAULT '{}'::jsonb,
    runbook_url                text NOT NULL DEFAULT '',
    labels                     jsonb NOT NULL DEFAULT '{}'::jsonb,
    version                    integer NOT NULL DEFAULT 1,
    created_by                 uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by                 uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alert_rules_org_idx ON alert_rules (org_id, name);

CREATE TABLE alert_rule_channels (
    rule_id     uuid NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    channel_id  uuid NOT NULL REFERENCES alert_channels (id) ON DELETE CASCADE,
    PRIMARY KEY (rule_id, channel_id)
);
CREATE INDEX alert_rule_channels_channel_idx ON alert_rule_channels (channel_id);

-- Running evaluators (openlog-alert / allinone), heartbeat every lease renew interval.
CREATE TABLE alert_evaluators (
    instance_id  text PRIMARY KEY CHECK (length(instance_id) BETWEEN 1 AND 200),
    version      text NOT NULL DEFAULT '',
    started_at   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now()
);

-- One row per rule: who evaluates it (lease) and its schedule. Created with the rule.
CREATE TABLE alert_rule_leases (
    rule_id            uuid PRIMARY KEY REFERENCES alert_rules (id) ON DELETE CASCADE,
    org_id             uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    owner              text NOT NULL DEFAULT '',
    lease_until        timestamptz NOT NULL DEFAULT '-infinity',
    claimed_at         timestamptz,
    next_eval_at       timestamptz NOT NULL DEFAULT now(),
    last_eval_end      timestamptz,
    last_evaluated_at  timestamptz,
    last_result        text NOT NULL DEFAULT '',
    last_error         text NOT NULL DEFAULT '',
    last_duration_ms   integer NOT NULL DEFAULT 0,
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alert_rule_leases_owner_idx ON alert_rule_leases (owner, lease_until);
CREATE INDEX alert_rule_leases_claim_idx ON alert_rule_leases (lease_until, next_eval_at);

CREATE TABLE alert_incidents (
    id                   uuid PRIMARY KEY,
    org_id               uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    rule_id              uuid REFERENCES alert_rules (id) ON DELETE SET NULL,
    rule_name            text NOT NULL,
    rule_type            text NOT NULL,
    severity             text NOT NULL,
    series_key           text NOT NULL,
    labels               jsonb NOT NULL DEFAULT '{}'::jsonb,
    summary              text NOT NULL DEFAULT '',
    state                text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'acknowledged', 'resolved')),
    value                double precision,
    last_value           double precision,
    threshold            double precision,
    channel_ids          uuid[] NOT NULL DEFAULT '{}',
    flapping             boolean NOT NULL DEFAULT false,
    opened_at            timestamptz NOT NULL,
    acknowledged_at      timestamptz,
    acknowledged_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    resolved_at          timestamptz,
    resolved_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    resolve_reason       text CHECK (resolve_reason IN ('recovered', 'manual', 'no_data', 'expired', 'rule_disabled', 'rule_deleted', 'rule_changed')),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
-- At most one unresolved incident per series (second guard besides lease fencing).
CREATE UNIQUE INDEX alert_incidents_open_uniq ON alert_incidents (rule_id, series_key) WHERE state <> 'resolved';
CREATE INDEX alert_incidents_org_idx ON alert_incidents (org_id, state, opened_at DESC);
CREATE INDEX alert_incidents_rule_idx ON alert_incidents (rule_id, opened_at DESC);

CREATE TABLE alert_incident_events (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    incident_id    uuid NOT NULL REFERENCES alert_incidents (id) ON DELETE CASCADE,
    org_id         uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    at             timestamptz NOT NULL DEFAULT now(),
    kind           text NOT NULL,
    actor_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    actor_email    text NOT NULL DEFAULT '',
    message        text NOT NULL DEFAULT '' CHECK (length(message) <= 4000),
    details        jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX alert_incident_events_incident_idx ON alert_incident_events (incident_id, id);

-- Evaluation state of non-ok (or recently flapping) series. Absent row = ok.
CREATE TABLE alert_series_state (
    rule_id            uuid NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    series_key         text NOT NULL,
    labels             jsonb NOT NULL DEFAULT '{}'::jsonb,
    state              text NOT NULL CHECK (state IN ('ok', 'pending', 'firing')),
    pending_since      timestamptz,
    firing_since       timestamptz,
    recovering_since   timestamptz,
    last_value         double precision,
    last_seen_at       timestamptz,
    incident_id        uuid REFERENCES alert_incidents (id) ON DELETE SET NULL,
    transitions        timestamptz[] NOT NULL DEFAULT '{}',
    last_notified_at   timestamptz,
    renotify_count     integer NOT NULL DEFAULT 0,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, series_key)
);

CREATE TABLE alert_mutes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    comment     text NOT NULL DEFAULT '' CHECK (length(comment) <= 2000),
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL,
    rule_ids    uuid[] NOT NULL DEFAULT '{}',
    -- [{"label": "host.name", "op": "eq", "value": "web-1"}]
    matchers    jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);
CREATE INDEX alert_mutes_org_idx ON alert_mutes (org_id, ends_at);

-- Notification outbox. Inserted in the transaction that caused it; claimed by dispatchers with SKIP LOCKED.
CREATE TABLE alert_notifications (
    id               uuid PRIMARY KEY,
    seq              bigint GENERATED ALWAYS AS IDENTITY,
    org_id           uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    incident_id      uuid REFERENCES alert_incidents (id) ON DELETE CASCADE,
    rule_id          uuid,
    channel_id       uuid REFERENCES alert_channels (id) ON DELETE SET NULL,
    channel_type     text NOT NULL,
    kind             text NOT NULL CHECK (kind IN ('opened', 'resolved', 'renotify', 'test')),
    idempotency_key  text NOT NULL UNIQUE,
    payload          jsonb NOT NULL,
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'delivered', 'failed', 'suppressed')),
    attempts         integer NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    claimed_by       text NOT NULL DEFAULT '',
    claimed_until    timestamptz,
    muted_logged     boolean NOT NULL DEFAULT false,
    last_error       text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alert_notifications_due_idx ON alert_notifications (next_attempt_at, seq) WHERE status = 'pending';
CREATE INDEX alert_notifications_sending_idx ON alert_notifications (claimed_until) WHERE status = 'sending';
CREATE INDEX alert_notifications_incident_idx ON alert_notifications (incident_id, channel_id, seq);
CREATE INDEX alert_notifications_org_idx ON alert_notifications (org_id, created_at DESC);
CREATE INDEX alert_notifications_channel_idx ON alert_notifications (channel_id, updated_at DESC);
CREATE INDEX alert_notifications_finished_idx ON alert_notifications (finished_at) WHERE finished_at IS NOT NULL;

-- Delivery log: one row per attempt.
CREATE TABLE alert_delivery_attempts (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    notification_id  uuid NOT NULL REFERENCES alert_notifications (id) ON DELETE CASCADE,
    org_id           uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    attempt          integer NOT NULL,
    instance_id      text NOT NULL DEFAULT '',
    started_at       timestamptz NOT NULL,
    duration_ms      integer NOT NULL DEFAULT 0,
    success          boolean NOT NULL,
    status_code      integer NOT NULL DEFAULT 0,
    error            text NOT NULL DEFAULT '' CHECK (length(error) <= 2000)
);
CREATE INDEX alert_delivery_attempts_notification_idx ON alert_delivery_attempts (notification_id, attempt);
