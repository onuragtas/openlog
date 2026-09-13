-- openlog:phase expand
-- 0003_component_heartbeats: running openlog processes (for contract migrations, see
-- docs/contracts/releases-updates.md §6) and small cluster-wide system state documents
-- (update check result, updater status). See docs/contracts/postgres.md.

-- One row per running process (ingest, processor, api, allinone). Upserted every 30 s; a
-- gracefully stopping process deletes its row. Rows older than one day are pruned.
CREATE TABLE IF NOT EXISTS component_heartbeats (
    component    text NOT NULL CHECK (length(component) BETWEEN 1 AND 100),
    instance_id  text NOT NULL CHECK (length(instance_id) BETWEEN 1 AND 200),
    version      text NOT NULL CHECK (length(version) <= 100),
    started_at   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (component, instance_id)
);
CREATE INDEX IF NOT EXISTS component_heartbeats_last_seen_idx ON component_heartbeats (last_seen);

-- Singleton JSON documents keyed by name: `update_check` (written by the api leader),
-- `updater` (written by openlog-updater).
CREATE TABLE IF NOT EXISTS system_state (
    key         text PRIMARY KEY CHECK (length(key) BETWEEN 1 AND 100),
    value       jsonb NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
