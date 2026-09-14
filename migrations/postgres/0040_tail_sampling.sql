-- openlog:phase expand
-- 0040_tail_sampling: per-organization tail sampling policy (D-075, docs/contracts/apm.md §4.2,
-- postgres.md "tail_sampling_policies"). The policy is a JSON document validated by the api
-- (internal/tailsampling.ParsePolicy); openlog-sampler reloads all rows periodically. Organizations
-- without a row use OPENLOG_TAILSAMPLING_DEFAULT_POLICY (keep everything unless configured).
--
-- Mixed versions: older binaries ignore the table.

CREATE TABLE tail_sampling_policies (
    org_id      uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    policy      jsonb NOT NULL CHECK (jsonb_typeof(policy) = 'object'),
    version     integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL
);
