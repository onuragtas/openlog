-- openlog:phase expand
-- 0064_php_access: PHP-FPM pools allowed to send spans to the infra agent's php.sock (docs/contracts/php-agent.md §1,
-- releases-updates.md §3 php_access, postgres.md "Fleet", D-103). The host Services tab shows pools without access and
-- the fix.
--
-- Mixed versions: an older ingest updates agent_hosts without touching php_access (the last report stays).

-- The php_access section of the agent's last sync request (socket group, pools, access); NULL when the agent does not
-- report one (no PHP-FPM pools, older agent).
ALTER TABLE agent_hosts ADD COLUMN php_access jsonb;
