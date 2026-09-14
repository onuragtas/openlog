-- openlog:phase expand
-- 0062_report_recipients_cleanup: removing a member from an organization (API or SCIM deactivation) also removes their
-- address from the organization's dashboard report recipient lists in the same transaction (D-096). A report whose
-- last recipient was removed is disabled and keeps an empty list, so recipients may now be empty; the API still
-- requires 1–20 recipients when a report is saved.
--
-- Mixed versions: older binaries never write empty lists; they read a disabled report with no recipients unchanged.
ALTER TABLE dashboard_reports DROP CONSTRAINT IF EXISTS dashboard_reports_recipients_check;
ALTER TABLE dashboard_reports ADD CONSTRAINT dashboard_reports_recipients_check CHECK (cardinality(recipients) <= 20);
