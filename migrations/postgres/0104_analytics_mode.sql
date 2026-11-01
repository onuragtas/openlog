-- openlog:phase expand
-- 0104_analytics_mode: how much product analytics a browser or mobile key collects (docs/contracts/analytics.md §1.1).
--
-- The column default is 'off' and the application's default for a new key is 'visitors', and the difference is the
-- whole point. Every key that exists when this runs gets 'off': upgrading openlog must not start collecting a
-- persistent visitor id for an application whose owner never decided to. A key created afterwards goes through
-- internal/auth, which writes 'visitors' explicitly, the way Google Analytics and Firebase Analytics start.

ALTER TABLE browser_keys
    ADD COLUMN IF NOT EXISTS analytics_mode text NOT NULL DEFAULT 'off'
        CHECK (analytics_mode IN ('off', 'sessions', 'visitors'));
