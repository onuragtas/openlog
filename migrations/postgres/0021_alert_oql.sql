-- openlog:phase expand
-- 0021_alert_oql: alert rules of type oql (conditions written in OQL; docs/contracts/alerting.md §2.10, D-065).
--
-- Mixed versions (releases-updates.md §6): evaluators of older versions do not know the type; they fail to parse
-- such rules and report them as evaluation errors until they are upgraded.
-- The list also contains apm_error (0025_apm_error_workflow, which sets the same list) so that the result does not
-- depend on the order in which the two files are applied.

ALTER TABLE alert_rules DROP CONSTRAINT IF EXISTS alert_rules_type_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_type_check
    CHECK (type IN ('metric_threshold', 'log_match', 'no_data', 'discovery', 'apm', 'apm_no_data', 'apm_error', 'oql'));
