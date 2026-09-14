-- openlog:phase expand
-- 0070_data_subject_requests: data portability exports, organization soft deletion with a grace period, the hard
-- deletion job's bookkeeping and deletion certificates (docs/contracts/postgres.md "Data subject requests", D-107).
--
-- Mixed versions: older api pods ignore organizations.deleted_at, so an organization scheduled for deletion stays
-- reachable through them until the rollout finishes (its license and SCIM keys are already revoked, so ingest and
-- provisioning stop at once). Older binaries never read the new tables.

-- Set when an owner or an operator schedules the organization's deletion; cleared again when it is cancelled. Members
-- lose access, ingest and API keys stop working. The row is removed by the hard deletion job.
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
CREATE INDEX IF NOT EXISTS organizations_deleted_idx ON organizations (deleted_at) WHERE deleted_at IS NOT NULL;

-- Export jobs: an organization export (owner; PostgreSQL data + telemetry of a time range) or a personal export of one
-- user. The api leader runs them one at a time and writes one ZIP archive to object storage or a local directory.
CREATE TABLE IF NOT EXISTS data_exports (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind                 text NOT NULL CHECK (kind IN ('organization', 'user')),
    org_id               uuid REFERENCES organizations (id) ON DELETE CASCADE,
    -- requester (organization exports) or subject (personal exports)
    user_id              uuid REFERENCES users (id) ON DELETE SET NULL,
    status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed', 'expired')),
    -- telemetry signals of an organization export: logs, traces, metrics
    signals              text[] NOT NULL DEFAULT '{}',
    range_from           timestamptz,
    range_to             timestamptz,
    locale               text NOT NULL DEFAULT '' CHECK (length(locale) <= 16),
    storage              text NOT NULL DEFAULT '' CHECK (storage IN ('', 'local', 's3')),
    object_key           text NOT NULL DEFAULT '' CHECK (length(object_key) <= 512),
    size_bytes           bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    telemetry_rows       bigint NOT NULL DEFAULT 0 CHECK (telemetry_rows >= 0),
    -- a size or row limit stopped the telemetry part early (the manifest says where)
    truncated            boolean NOT NULL DEFAULT false,
    manifest             jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- sha256 of the download link token (shown only in the e-mail)
    download_token_hash  bytea UNIQUE CHECK (download_token_hash IS NULL OR length(download_token_hash) = 32),
    attempts             integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    error                text NOT NULL DEFAULT '' CHECK (length(error) <= 1000),
    created_at           timestamptz NOT NULL DEFAULT now(),
    started_at           timestamptz,
    heartbeat_at         timestamptz,
    completed_at         timestamptz,
    expires_at           timestamptz,
    CHECK (kind <> 'organization' OR org_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS data_exports_org_idx ON data_exports (org_id, created_at DESC) WHERE org_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS data_exports_user_idx ON data_exports (user_id, created_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS data_exports_queue_idx ON data_exports (created_at) WHERE status IN ('pending', 'running');
CREATE INDEX IF NOT EXISTS data_exports_expires_idx ON data_exports (expires_at) WHERE status = 'completed';

-- Organization deletions. scheduled → (cancelled | deleting → completed). The row outlives the organization
-- (org_id becomes NULL); personal fields are cleared when the deletion completes.
CREATE TABLE IF NOT EXISTS org_deletions (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                   uuid REFERENCES organizations (id) ON DELETE SET NULL,
    tenant_id                text NOT NULL,
    status                   text NOT NULL CHECK (status IN ('scheduled', 'cancelled', 'deleting', 'completed')),
    initiator                text NOT NULL CHECK (initiator IN ('owner', 'operator')),
    reason                   text NOT NULL DEFAULT '' CHECK (length(reason) <= 1000),
    requested_by             uuid REFERENCES users (id) ON DELETE SET NULL,
    requested_by_email       text NOT NULL DEFAULT '',
    -- {"org_name", "owners": [{"email", "locale"}]} for the completion e-mail; cleared once it is sent
    notify                   jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- keys revoked by the scheduling, restored by a cancellation
    revoked_license_key_ids  uuid[] NOT NULL DEFAULT '{}',
    revoked_scim_token_ids   uuid[] NOT NULL DEFAULT '{}',
    requested_at             timestamptz NOT NULL DEFAULT now(),
    purge_after              timestamptz NOT NULL,
    cancelled_at             timestamptz,
    started_at               timestamptz,
    completed_at             timestamptz,
    -- ClickHouse progress: {"counts_before": {table: rows}, "submitted": {table: {partition: time}}}
    progress                 jsonb NOT NULL DEFAULT '{}'::jsonb,
    attempts                 integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error               text NOT NULL DEFAULT '' CHECK (length(last_error) <= 1000),
    certificate_id           uuid
);
CREATE UNIQUE INDEX IF NOT EXISTS org_deletions_active_uniq ON org_deletions (org_id) WHERE status IN ('scheduled', 'deleting');
CREATE INDEX IF NOT EXISTS org_deletions_due_idx ON org_deletions (purge_after) WHERE status IN ('scheduled', 'deleting');

-- Proof of completed hard deletions, kept without personal data (docs/operations/saas.md "Data subject requests").
CREATE TABLE IF NOT EXISTS deletion_certificates (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type     text NOT NULL CHECK (subject_type IN ('organization', 'user')),
    -- hex sha256 of the tenant id (organization) or of the user id (user): the data subject can verify it, it
    -- identifies no one by itself
    subject_hash     text NOT NULL CHECK (length(subject_hash) = 64),
    initiator        text NOT NULL CHECK (initiator IN ('owner', 'operator', 'self')),
    requested_at     timestamptz NOT NULL,
    grace_ended_at   timestamptz,
    started_at       timestamptz NOT NULL,
    completed_at     timestamptz NOT NULL DEFAULT now(),
    -- rows deleted (or anonymized) per PostgreSQL table and per ClickHouse table
    postgres_rows    jsonb NOT NULL DEFAULT '{}'::jsonb,
    clickhouse_rows  jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- every ClickHouse table was re-counted with zero rows of the tenant after the mutations finished
    verified         boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS deletion_certificates_subject_idx ON deletion_certificates (subject_hash);
CREATE INDEX IF NOT EXISTS deletion_certificates_completed_idx ON deletion_certificates (completed_at DESC);
