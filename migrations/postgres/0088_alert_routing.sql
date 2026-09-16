-- openlog:phase expand
-- 0088_alert_routing: PagerDuty and Opsgenie notification channels, the `acknowledged` notification kind and
-- per-organization routing rules (docs/contracts/alerting.md §5.3, §5.6, api.md "Alerting",
-- postgres.md "Alerting", D-127).
--
-- Mixed versions (releases-updates.md §6): older binaries never create pagerduty/opsgenie channels and ignore
-- alert_routing_rules, so their evaluations notify the channels of the rule itself (the behaviour before this
-- migration). An older dispatcher that claims an `acknowledged` row delivers it like any other notification (its
-- renderers fall back to the firing wording); only new dispatchers map it to the provider's acknowledge action.

ALTER TABLE alert_channels DROP CONSTRAINT IF EXISTS alert_channels_type_check;
ALTER TABLE alert_channels ADD CONSTRAINT alert_channels_type_check
    CHECK (type IN ('slack', 'email', 'webhook', 'teams', 'pagerduty', 'opsgenie'));

ALTER TABLE alert_notifications DROP CONSTRAINT IF EXISTS alert_notifications_kind_check;
ALTER TABLE alert_notifications ADD CONSTRAINT alert_notifications_kind_check
    CHECK (kind IN ('opened', 'acknowledged', 'resolved', 'renotify', 'test'));

-- Ordered list per organization: the first enabled rule whose match applies decides the channels of an opening
-- incident. The default rule matches every incident and is evaluated last.
CREATE TABLE IF NOT EXISTS alert_routing_rules (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    "position"  integer NOT NULL DEFAULT 0,
    enabled     boolean NOT NULL DEFAULT true,
    is_default  boolean NOT NULL DEFAULT false,
    -- {"severities": ["critical"], "services": ["checkout"], "rule_types": ["apm"],
    --  "labels": [{"label": "env", "op": "eq", "value": "prod"}],
    --  "time_window": {"timezone": "Europe/Istanbul", "days": ["mon"], "start_time": "09:00", "end_time": "18:00"}}
    match       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS alert_routing_rules_org_idx ON alert_routing_rules (org_id, "position", id);
-- At most one default route per organization (the API checks it first, for a better error message).
CREATE UNIQUE INDEX IF NOT EXISTS alert_routing_rules_default_uniq ON alert_routing_rules (org_id) WHERE is_default;

CREATE TABLE IF NOT EXISTS alert_routing_rule_channels (
    route_id    uuid NOT NULL REFERENCES alert_routing_rules (id) ON DELETE CASCADE,
    channel_id  uuid NOT NULL REFERENCES alert_channels (id) ON DELETE CASCADE,
    PRIMARY KEY (route_id, channel_id)
);
CREATE INDEX IF NOT EXISTS alert_routing_rule_channels_channel_idx ON alert_routing_rule_channels (channel_id);
