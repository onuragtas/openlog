-- openlog:phase expand
-- 0087_slo: service level objectives and their error budgets (docs/contracts/slo.md, api.md "Service level
-- objectives", postgres.md "SLOs"). An SLO names one service (apm.md §1; NULL namespace/environment = every
-- namespace/environment), an SLI (availability or latency), the objective in percent and a rolling window in days.
-- Budgets and burn rates are computed at query time from apm_transactions_1m; nothing is precomputed.
--
-- Mixed versions (releases-updates.md §6): older binaries do not use the table. Rule type slo_burn is added to
-- alert_rules_type_check here; evaluators of older versions cannot parse such rules and report them as evaluation
-- errors until they are upgraded.

CREATE TABLE IF NOT EXISTS slos (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name                    text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description             text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    service_name            text NOT NULL CHECK (length(service_name) BETWEEN 1 AND 512),
    -- NULL = every namespace/environment of the service; '' = exactly the services without the attribute.
    service_namespace       text CHECK (service_namespace IS NULL OR length(service_namespace) <= 512),
    deployment_environment  text CHECK (deployment_environment IS NULL OR length(deployment_environment) <= 512),
    sli_type                text NOT NULL CHECK (sli_type IN ('availability', 'latency')),
    -- Latency SLI only: requests faster than this many milliseconds are good (histogram buckets, apm.md §4.1).
    latency_threshold_ms    integer CHECK (latency_threshold_ms IS NULL OR latency_threshold_ms BETWEEN 1 AND 600000),
    CONSTRAINT slos_latency_threshold_check CHECK (
        (sli_type = 'latency' AND latency_threshold_ms IS NOT NULL) OR
        (sli_type <> 'latency' AND latency_threshold_ms IS NULL)),
    objective               double precision NOT NULL CHECK (objective >= 50 AND objective < 100),
    window_days             integer NOT NULL CHECK (window_days IN (7, 28, 30)),
    created_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS slos_org_name_idx ON slos (org_id, lower(name));
CREATE INDEX IF NOT EXISTS slos_org_service_idx ON slos (org_id, service_name);

-- Rule type slo_burn (multi-window burn rate, alerting.md §2.11). The list repeats the types of the earlier
-- migrations so the result does not depend on the order in which expand migrations run.
ALTER TABLE alert_rules DROP CONSTRAINT IF EXISTS alert_rules_type_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_type_check
    CHECK (type IN ('metric_threshold', 'log_match', 'no_data', 'discovery', 'apm', 'apm_no_data', 'apm_error', 'oql', 'slo_burn'));
