-- openlog:phase expand
-- 0002_fleet: agent fleet updates — per-organization update policy, host overrides,
-- rollouts and the agents that sync with ingest (POST /v1/openlog/agent/sync).
-- See docs/contracts/postgres.md "Fleet" and docs/contracts/releases-updates.md §3–§4.

CREATE TABLE agent_update_policies (
    org_id               uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    mode                 text NOT NULL DEFAULT 'auto' CHECK (mode IN ('off', 'notify', 'auto')),
    channel              text NOT NULL DEFAULT 'stable' CHECK (channel IN ('stable', 'beta')),
    target               text NOT NULL DEFAULT 'latest' CHECK (target IN ('latest', 'patch', 'pinned')),
    pinned_version       text,
    -- Strictly increasing percentages, last = 100 (validated by the API).
    waves                integer[] NOT NULL DEFAULT '{10,50,100}',
    wave_soak_minutes    integer NOT NULL DEFAULT 60 CHECK (wave_soak_minutes BETWEEN 0 AND 43200),
    halt_failure_rate    double precision NOT NULL DEFAULT 0.05 CHECK (halt_failure_rate BETWEEN 0 AND 1),
    -- [{"days": ["mon", …], "start": "02:00", "end": "05:00"}] in UTC; [] = always.
    maintenance_windows  jsonb NOT NULL DEFAULT '[]'::jsonb,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    updated_by           uuid REFERENCES users (id) ON DELETE SET NULL,
    CHECK (target <> 'pinned' OR pinned_version IS NOT NULL)
);

CREATE TABLE agent_update_host_overrides (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    host_id     text NOT NULL CHECK (length(host_id) BETWEEN 1 AND 256),
    action      text NOT NULL CHECK (action IN ('hold', 'pin')),
    version     text,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (org_id, host_id),
    CHECK (action <> 'pin' OR version IS NOT NULL)
);

CREATE TABLE agent_rollouts (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    action             text NOT NULL CHECK (action IN ('upgrade', 'rollback')),
    from_version       text,
    -- NULL for target=patch rollouts: per-minor targets are in "targets" ({"0.3": "0.3.4"}).
    to_version         text,
    targets            jsonb NOT NULL DEFAULT '{}'::jsonb,
    waves              integer[] NOT NULL,
    current_wave       integer NOT NULL DEFAULT 0 CHECK (current_wave >= 0),
    wave_started_at    timestamptz NOT NULL DEFAULT now(),
    wave_soak_minutes  integer NOT NULL,
    halt_failure_rate  double precision NOT NULL,
    state              text NOT NULL DEFAULT 'active'
                       CHECK (state IN ('active', 'paused', 'halted', 'completed', 'superseded')),
    state_reason       text NOT NULL DEFAULT '',
    -- Counters refreshed by the rollout controller.
    hosts_pending      integer NOT NULL DEFAULT 0,
    hosts_attempted    integer NOT NULL DEFAULT 0,
    hosts_succeeded    integer NOT NULL DEFAULT 0,
    hosts_failed       integer NOT NULL DEFAULT 0,
    hosts_rolled_back  integer NOT NULL DEFAULT 0,
    -- Attempts/failures already acknowledged by an admin resume (excluded from the halt rate).
    ack_attempted      integer NOT NULL DEFAULT 0,
    ack_failed         integer NOT NULL DEFAULT 0,
    created_by         uuid REFERENCES users (id) ON DELETE SET NULL,  -- NULL = rollout controller
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    ended_at           timestamptz
);
-- At most one open rollout per organization.
CREATE UNIQUE INDEX agent_rollouts_open_uniq ON agent_rollouts (org_id) WHERE state IN ('active', 'paused', 'halted');
CREATE INDEX agent_rollouts_org_idx ON agent_rollouts (org_id, created_at DESC);

-- Agents as last reported through sync. Written asynchronously by ingest in batches.
CREATE TABLE agent_hosts (
    org_id             uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    host_id            text NOT NULL CHECK (length(host_id) BETWEEN 1 AND 256),
    host_name          text NOT NULL DEFAULT '',
    agent_name         text NOT NULL DEFAULT '',
    agent_version      text NOT NULL DEFAULT '',
    agent_commit       text NOT NULL DEFAULT '',
    agent_os           text NOT NULL DEFAULT '',
    agent_arch         text NOT NULL DEFAULT '',
    install_method     text NOT NULL DEFAULT '',
    update_capable     boolean NOT NULL DEFAULT false,
    update_state       text NOT NULL DEFAULT 'idle',
    update_from        text NOT NULL DEFAULT '',
    update_to          text NOT NULL DEFAULT '',
    update_error       text NOT NULL DEFAULT '',
    update_changed_at  timestamptz,
    config_hash        text NOT NULL DEFAULT '',
    first_seen_at      timestamptz NOT NULL DEFAULT now(),
    last_sync_at       timestamptz NOT NULL DEFAULT now(),
    -- Last rollout that offered this host an update (soft reference, no FK: written by ingest in batches).
    rollout_id         uuid,
    PRIMARY KEY (org_id, host_id)
);
CREATE INDEX agent_hosts_name_idx ON agent_hosts (org_id, host_name, host_id);
CREATE INDEX agent_hosts_version_idx ON agent_hosts (org_id, agent_version);
CREATE INDEX agent_hosts_rollout_idx ON agent_hosts (rollout_id) WHERE rollout_id IS NOT NULL;
