-- openlog:phase expand
-- Hosts, inventory items and snapshot pointers. All sharded by (tenant_id, host_id) so that
-- ReplacingMergeTree deduplication happens on a single shard. Read with FINAL or argMax.

-- Written by the processor once per (tenant, host) per batch, from any signal carrying host.id.
CREATE TABLE IF NOT EXISTS openlog.hosts_local ON CLUSTER '{cluster}'
(
    tenant_id           LowCardinality(String),
    host_id             String,
    host_name           String,
    os_type             LowCardinality(String),
    os_description      String,
    arch                LowCardinality(String),
    agent_name          LowCardinality(String),
    agent_version       LowCardinality(String),
    resource_attributes Map(LowCardinality(String), String),
    last_seen           DateTime64(3, 'UTC')
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/openlog/hosts_local', '{replica}', last_seen)
ORDER BY (tenant_id, host_id)
TTL toDateTime(last_seen) + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS openlog.hosts ON CLUSTER '{cluster}'
AS openlog.hosts_local
ENGINE = Distributed('{cluster}', openlog, hosts_local, cityHash64(tenant_id, host_id));

-- One row per inventory item per snapshot (event.name = openlog.inventory.item).
CREATE TABLE IF NOT EXISTS openlog.inventory_items_local ON CLUSTER '{cluster}'
(
    tenant_id     LowCardinality(String),
    host_id       String,
    snapshot_id   String,
    snapshot_time DateTime64(9, 'UTC'),
    category      LowCardinality(String),
    item_key      String,
    data          String CODEC(ZSTD(3)),
    INDEX idx_item_key lower(item_key) TYPE tokenbf_v1(8192, 3, 0) GRANULARITY 4
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/inventory_items_local', '{replica}')
PARTITION BY toDate(snapshot_time)
ORDER BY (tenant_id, host_id, snapshot_id, category, item_key)
TTL toDateTime(snapshot_time) + INTERVAL 7 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.inventory_items ON CLUSTER '{cluster}'
AS openlog.inventory_items_local
ENGINE = Distributed('{cluster}', openlog, inventory_items_local, cityHash64(tenant_id, host_id));

-- Latest complete snapshot per host (event.name = openlog.inventory.snapshot).
CREATE TABLE IF NOT EXISTS openlog.inventory_snapshots_local ON CLUSTER '{cluster}'
(
    tenant_id     LowCardinality(String),
    host_id       String,
    snapshot_id   String,
    snapshot_time DateTime64(9, 'UTC'),
    item_count    UInt32
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/openlog/inventory_snapshots_local', '{replica}', snapshot_time)
ORDER BY (tenant_id, host_id)
TTL toDateTime(snapshot_time) + INTERVAL 7 DAY;

CREATE TABLE IF NOT EXISTS openlog.inventory_snapshots ON CLUSTER '{cluster}'
AS openlog.inventory_snapshots_local
ENGINE = Distributed('{cluster}', openlog, inventory_snapshots_local, cityHash64(tenant_id, host_id));
