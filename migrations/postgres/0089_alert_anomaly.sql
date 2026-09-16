-- openlog:phase expand
-- 0089_alert_anomaly: rule type `anomaly` (baseline conditions, docs/contracts/alerting.md §2.12). The condition
-- lives in alert_rules.condition like every other type; only the type check has to allow the new name. Nothing
-- else is stored: baselines are computed at evaluation time from the 1-minute rollups (metrics_1m,
-- apm_transactions_1m).
--
-- Mixed versions (releases-updates.md §6): evaluators of older versions cannot parse an anomaly condition and
-- report such rules as evaluation errors until they are upgraded; their state and incidents are unaffected.

-- The list repeats the types of the earlier migrations so the result does not depend on the order in which
-- expand migrations run.
ALTER TABLE alert_rules DROP CONSTRAINT IF EXISTS alert_rules_type_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_type_check
    CHECK (type IN ('metric_threshold', 'log_match', 'no_data', 'discovery', 'apm', 'apm_no_data', 'apm_error', 'oql', 'slo_burn', 'anomaly'));
