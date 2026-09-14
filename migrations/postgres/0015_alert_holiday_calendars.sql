-- openlog:phase expand
-- 0015_alert_holiday_calendars: named holiday date sets used as exceptions by recurring mutes (docs/contracts/alerting.md §5.2).
--
-- Mixed versions (releases-updates.md §6): monthly rules, exdates and holiday_calendar_ids live inside
-- alert_mutes.schedule (jsonb). Dispatchers of older versions ignore them: they do not mute monthly schedules
-- (their stored weekday list is empty) and they mute on exception dates, until they are upgraded.

CREATE TABLE IF NOT EXISTS alert_holiday_calendars (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    -- 'YYYY-MM-DD' (one date) or 'MM-DD' (every year), sorted and deduplicated by the API
    dates       text[] NOT NULL DEFAULT '{}',
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);
