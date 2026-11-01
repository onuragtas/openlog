-- openlog:phase expand
-- 0107_synthetic_check_bounds: what internal/synthetics validates and the table refuses (docs/contracts/api.md).
--
-- 0095 added checks with no HTTP method, and left the constraint that requires one, so creating a tcp, dns or tls check
-- failed with an internal error. The interval was moved down to 5 seconds in code and not here, so anything from 5 to 29
-- failed the same way.

ALTER TABLE synthetic_checks
    DROP CONSTRAINT IF EXISTS synthetic_checks_method_check;

ALTER TABLE synthetic_checks
    ADD CONSTRAINT synthetic_checks_method_check
    CHECK (method IN ('GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS')
           OR (type <> 'http' AND method = ''));

ALTER TABLE synthetic_checks
    DROP CONSTRAINT IF EXISTS synthetic_checks_interval_seconds_check;

ALTER TABLE synthetic_checks
    ADD CONSTRAINT synthetic_checks_interval_seconds_check CHECK (interval_seconds BETWEEN 5 AND 86400);
