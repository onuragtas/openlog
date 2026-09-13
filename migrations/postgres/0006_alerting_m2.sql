-- openlog:phase expand
-- 0006_alerting_m2: rule type apm_no_data and recurring mute schedules (docs/contracts/alerting.md §2.7, §5.2).
--
-- Mixed versions (releases-updates.md §6): a binary without apm_no_data support loads such a rule without a
-- condition (its evaluation fails with "rule type is not available"); it never creates one. For recurring mutes
-- starts_at/ends_at hold the current or next occurrence, rolled forward by new dispatchers, so an old dispatcher
-- (which ignores schedule) mutes during the same occurrence.

ALTER TABLE alert_rules DROP CONSTRAINT IF EXISTS alert_rules_type_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_type_check
    CHECK (type IN ('metric_threshold', 'log_match', 'no_data', 'discovery', 'apm', 'apm_no_data'));

-- {"timezone": "Europe/Istanbul", "days": ["mon", "fri"], "rrule": "FREQ=WEEKLY;BYDAY=MO,FR",
--  "start_time": "22:00", "end_time": "06:00", "from": "…", "until": "…"}; NULL = one-off mute.
ALTER TABLE alert_mutes ADD COLUMN IF NOT EXISTS schedule jsonb;
CREATE INDEX IF NOT EXISTS alert_mutes_recurring_idx ON alert_mutes (ends_at) WHERE schedule IS NOT NULL;
