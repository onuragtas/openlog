-- openlog:phase expand
-- 0103_synthetic_interval_floor: the shortest synthetic check interval goes from 30 s to 5 s (D-132, D-140).
--
-- Widening a CHECK is an expand change: an older instance keeps refusing anything under 30 s in its own
-- validation (internal/synthetics MinIntervalSecs), so a mixed deployment simply means the shorter intervals
-- can only be created through the new instances. Nothing already stored becomes invalid.
--
-- The floor is the scheduler's resolution, not an opinion: the leader claims due rows once per tick
-- (internal/synthetics DefaultTick), so an interval shorter than a tick could not be honoured. The tick drops
-- to 1 s with this change, which leaves a 5 s check running every 5-6 s rather than every 5-10 s.
--
-- timeout_ms still has to fit inside the interval (synthetic_checks_timeout_fits, unchanged): a 5 s check
-- cannot keep the 10 s default timeout, and the API says so rather than silently shortening it.

ALTER TABLE synthetic_checks DROP CONSTRAINT IF EXISTS synthetic_checks_interval_seconds_check;

ALTER TABLE synthetic_checks
    ADD CONSTRAINT synthetic_checks_interval_seconds_check
    CHECK (interval_seconds BETWEEN 5 AND 86400);
