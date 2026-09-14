# openlog Helm chart (`cluster` profile)

Deploys openlog as a horizontally scalable cluster on Kubernetes:

| Component | Workload | Ports | Scaling |
|---|---|---|---|
| `openlog-ingest` | Deployment + HPA (CPU) + PDB | 4317 OTLP/gRPC, 4318 OTLP/HTTP, 9464 admin | stateless; add replicas |
| `openlog-processor` | Deployment + HPA (CPU) + PDB | 9464 admin | consumer group; replicas **<= Kafka partitions** |
| `openlog-api` | Deployment + HPA (CPU) + PDB | 8080 HTTP, 9464 admin | stateless; add replicas |
| `openlog-migrate` | Helm hook Job | - | - |
| `openlog-admin bootstrap` (optional) | Helm hook Job | - | - |
| PostgreSQL (`postgres.mode: operator`) | CloudNativePG `Cluster` | 5432 | 1 (dev) / 3 (prod) instances |
| Envoy (optional) | Deployment + PDB | 4317, 4318, 9902 metrics | L7 LB for ingest |

Contracts: [config.md](../../../docs/contracts/config.md), [kafka.md](../../../docs/contracts/kafka.md),
[schema](../../../schema/clickhouse). Runbooks: [k3d dev cluster](../../../docs/operations/k3d-dev-cluster.md),
[scaling](../../../docs/operations/scaling.md).

Requirements: Kubernetes >= 1.30, Helm 3. Image `ghcr.io/onuragtas/openlog:<tag>` with
`/usr/local/bin/openlog-{ingest,processor,api,migrate,allinone,loadgen}`, runnable as a non-root
user with a read-only root filesystem (`/tmp` is an emptyDir).

Connections to Kafka, ClickHouse and PostgreSQL are plaintext unless configured as described in
[TLS and SASL](#tls-and-sasl). Without TLS, run them only on a trusted network (e.g. restrict with
NetworkPolicies).

## TLS and SASL

Variables and semantics: [config.md, "TLS and SASL"](../../../docs/contracts/config.md). The chart
mounts the referenced Secrets read-only under `/etc/openlog/tls/<kafka|clickhouse|postgres>-<ca|client>/`
(files `ca.crt`, `tls.crt`, `tls.key`) in every openlog pod (ingest, processor, api, migrate, bootstrap)
and sets the `OPENLOG_*_TLS_*` / `OPENLOG_KAFKA_SASL_*` env. Certificates are read at start-up: roll
the Deployments after rotating a Secret (`kubectl rollout restart deploy -l app.kubernetes.io/part-of=openlog`).

External Kafka with SASL_SSL (SCRAM) and a private CA, ClickHouse native TLS with mutual TLS,
PostgreSQL `verify-full`:

```yaml
kafka:
  external: {brokers: [kafka-0.example:9093, kafka-1.example:9093]}
  tls:
    enabled: true
    caSecret: {name: kafka-ca, key: ca.crt}
  sasl:
    mechanism: SCRAM-SHA-512
    username: openlog
    passwordSecret: {name: kafka-openlog, key: password}
clickhouse:
  external: {addrs: [clickhouse-0.example:9440]}   # tcp_port_secure
  tls:
    enabled: true
    caSecret: {name: clickhouse-ca, key: ca.crt}
    clientSecret: {name: openlog-clickhouse-client, certKey: tls.crt, keyKey: tls.key}   # e.g. cert-manager
postgres:
  external: {dsn: "postgres://openlog@pg.example:5432/openlog?sslmode=verify-full"}
  tls:
    caSecret: {name: pg-ca, key: ca.crt}
```

For multi-shard ClickHouse leave `clickhouse.tls.serverName` empty: the processor's direct shard
connections verify each replica's `host_name` from `system.clusters`, so every replica certificate must
carry that name, and `remote_servers` must use the secure port with `<secure>1</secure>`.

**Strimzi (operators mode).** `kafka.strimzi.listener.tls: true` turns the internal listener into a
TLS listener (use `listenerPort: 9093`); openlog then trusts the cluster CA Secret
`<kafka name>-cluster-ca-cert`. `kafka.strimzi.listener.authentication` adds client authentication and
renders a `KafkaUser` (`<kafka name>-openlog`, User Operator enabled):

| `listener.tls` | `listener.authentication` | Kafka protocol | openlog credentials |
|---|---|---|---|
| `false` | `""` | PLAINTEXT | — |
| `true` | `""` | SSL | CA only |
| `true` | `scram-sha-512` | SASL_SSL | `OPENLOG_KAFKA_SASL_*` from the KafkaUser Secret (`password`) |
| `true` | `tls` | SSL + mutual TLS | client certificate from the KafkaUser Secret (`user.crt`/`user.key`) |
| `false` | `scram-sha-512` | SASL_PLAINTEXT (trusted networks only) | as above |

Changing the listener on an existing cluster rolls the brokers; openlog pods pick up the new settings
on the next `helm upgrade` (env and mounts change, so they roll too). Authorization (ACLs) is not
configured by the chart. For Altinity ClickHouse in operators mode, `clickhouse.tls.server.secretName` makes the chart
configure server TLS (`tcp_port_secure`, `https_port`, secure `remote_servers`), Keeper TLS (client port 9281 + Raft,
`server.keeper`) and replication over HTTPS (`server.interserverHTTPS`, port 9010); the certificate must cover the
ClickHouse pod hosts, the service host and the Keeper hosts (`keeper-<chk>.<ns>.svc`, `chk-<chk>-keeper-0-<n>`) with
server and client usage. On an existing release enable it in two upgrades: first `clickhouse.tls.server.*`, and after
the CHI/CHK show `Completed` `clickhouse.tls.enabled` + `clickhouse.tls.clientSecret` (in one upgrade the pre-upgrade
migrate hook dials 9440 before ClickHouse serves it). Between the two, processor shard inserts fail the handshake and
data waits in Kafka. Validated on kind with `verificationMode: strict` ([kind runbook](../../../docs/operations/kind-dev-cluster.md)).

## Dependency modes

### `dependencies.mode: external` (default)

You provide Kafka and ClickHouse:

```yaml
dependencies: {mode: external}
kafka:
  external: {brokers: [kafka-0.example:9092, kafka-1.example:9092]}
clickhouse:
  user: openlog
  external: {addrs: [clickhouse.example:9000]}
```

ClickHouse requirements: a cluster named `openlog` (`clickhouse.cluster`), the `{cluster}`,
`{shard}`, `{replica}` macros, Keeper/ZooKeeper, and `distributed_ddl` enabled
(the schema uses `ON CLUSTER` and `Replicated*` engines). The database is always `openlog`
(fixed by contract, D-015; not a chart value).
`openlog-migrate` creates missing topics from `kafka.topics` (partitions, replicationFactor,
minInsyncReplicas, retentionMs, maxMessageBytes, passed as `OPENLOG_KAFKA_*`). Topics that already
exist are left unchanged. Set `kafka.topics.create=false` to manage topics yourself.

### `dependencies.mode: operators`

The chart renders custom resources; the operators themselves are **not** vendored:

- Strimzi: `Kafka` (KRaft, `strimzi.io/node-pools: enabled`), `KafkaNodePool`s, and a
  `KafkaTopic` per signal (`<prefix>.otlp.{metrics,logs,traces}.v1`) with partitions, replicas,
  `min.insync.replicas`, `retention.ms` and `max.message.bytes` from `kafka.topics`.
  `OPENLOG_MIGRATE_SKIP_KAFKA=true` is set on the migrate Job.
- Altinity: `ClickHouseKeeperInstallation` (3 replicas) and `ClickHouseInstallation` with cluster
  `openlog`, `shards` x `replicas`. The operator generates the `{cluster}/{shard}/{replica}` macros
  and `remote_servers`. The CHI also creates the ClickHouse user `openlog`, whose password is read
  from the chart Secret (`k8s_secret_password`).

Install the operators first (they can live in any namespace, but must watch the release namespace):

```bash
# Strimzi (KRaft only). Tested: chart 1.2.0 (Kafka 4.3.1).
helm install strimzi oci://quay.io/strimzi-helm/strimzi-kafka-operator --version 1.2.0 \
  -n strimzi --create-namespace --set watchAnyNamespace=true

# Altinity clickhouse-operator (includes ClickHouseKeeperInstallation). Tested: chart 0.27.3.
# Chart >= 0.27 watches only its own namespace unless watchNamespaces is set.
helm repo add altinity https://docs.altinity.com/clickhouse-operator/
helm install clickhouse-operator altinity/altinity-clickhouse-operator --version 0.27.3 \
  -n clickhouse-operator --create-namespace --set 'watchNamespaces={openlog}'
```

- Strimzi >= 1.0 serves only `kafka.strimzi.io/v1` (the chart default `kafka.strimzi.apiVersion`).
  For Strimzi 0.46 - 0.48 set `kafka.strimzi.apiVersion=kafka.strimzi.io/v1beta2`.
- If the ClickHouseInstallation/ClickHouseKeeperInstallation never get a `STATUS`, the Altinity
  operator is not watching the release namespace.

Endpoints derived in this mode:
`OPENLOG_KAFKA_BROKERS=<kafka name>-kafka-bootstrap.<ns>.svc:9092`,
`OPENLOG_CLICKHOUSE_ADDR=clickhouse-<chi name>.<ns>.svc:9000`, Keeper `keeper-<chk name>.<ns>.svc:2181`
(override with `clickhouse.altinity.host` / `clickhouse.altinity.keeper.host` if your operator
version names services differently).

#### Tiered storage (S3)

`clickhouse.tieredStorage.enabled=true` moves old parts to S3 (and optionally a warm PVC) while retention stays the
same ([docs/operations/tiered-storage.md](../../../docs/operations/tiered-storage.md), D-066). In operators mode the
chart adds the storage policy `openlog_tiered` (`config.d/openlog-storage.xml`), the S3 disk with a cache on the data
volume (`s3.cacheMaxSize`; size `clickhouse.altinity.storage.size` for it), credentials from `s3.credentialsSecret`
(or IRSA: empty secret name, `s3.useEnvironmentCredentials=true`, `s3.serviceAccountName`), extra disk settings such as
SSE-KMS (`s3.diskSettings`) and, with `warm.enabled`, a second PVC per replica. Use `{shard}/{replica}` in
`s3.endpoint` so every replica has its own prefix. The migrate Job applies the moves per `coldAfterDays` /
`warmAfterDays`; `kubectl exec deploy/<release>-api -- openlog-admin storage status` shows bytes per volume and pending
moves. External mode: configure `storage_configuration` on every server yourself (same XML as
`deploy/compose/clickhouse/storage-tiered.xml`), then enable. On an existing operators-mode release, run `helm upgrade` twice (see tiered-storage.md). Not yet validated end to end.

### PostgreSQL (`auth.mode: postgres`, default)

Organizations (tenants), users, sessions, ingest license keys and API keys live in PostgreSQL
([postgres.md](../../../docs/contracts/postgres.md)). ingest, api and migrate get
`OPENLOG_AUTH_MODE=postgres` and `OPENLOG_POSTGRES_DSN`; processor does not use PostgreSQL.

- `postgres.mode: external` (default): set `postgres.external.dsn` (stored in the chart Secret) or put
  the DSN into `auth.existingSecret` under `auth.postgresDsnKey`. A password-only Secret can be given with
  `postgres.external.passwordSecret`. Use `sslmode=verify-full` on untrusted networks.
- `postgres.mode: operator`: the chart renders a CloudNativePG `Cluster` (`postgres.operator.instances`:
  1 in `values-dev.yaml`, 3 in production; soft anti-affinity; `backup` passthrough). Install the operator
  first. `OPENLOG_POSTGRES_DSN` is read from the Secret `<cluster>-app` (key `uri`) that CNPG creates; pods
  wait (CreateContainerConfigError, retried by Kubernetes) until it exists.

  ```bash
  # CloudNativePG. Tested with chart 0.26 (operator 1.27).
  helm repo add cnpg https://cloudnative-pg.github.io/charts
  helm install cnpg cnpg/cloudnative-pg -n cnpg-system --create-namespace
  ```

`auth.mode: static` keeps the M0 behaviour for development/tests: `auth.licenseKeys` for ingest and api,
no users, no management API, no UI sign-in.

Ingest caches license keys (`auth.cache`): a revoked key is accepted for up to `auth.cache.ttl` (60 s),
and while PostgreSQL is unreachable cached keys keep working for `auth.cache.maxStale`. Ingest has no
PostgreSQL readiness check, so a database outage does not remove ingest pods from the load balancer.
The api waits for PostgreSQL at start-up and reports it in `/readyz`.

### First organization and owner

Either enable the `bootstrap` hook Job (`bootstrap.enabled`, `ownerEmail`, `ownerPassword`, optional
`licenseKey`; idempotent, runs after every install/upgrade), or run once:

```bash
kubectl -n openlog exec deploy/openlog-api -- openlog-admin create-owner --email you@example.com --org "Acme"
# prints a generated password and an ingest license key once
```

Then sign in to the UI, change the password (Settings → Security) and create ingest license keys and
read-only API keys under Settings.

## Install

```bash
helm install openlog deploy/helm/openlog -n openlog --create-namespace -f my-values.yaml --timeout 30m
```

Released charts (chart `version` = `appVersion` = openlog version, sha256 in the signed release manifest,
[releasing.md](../../../docs/operations/releasing.md#helm-charts)) instead of the repository checkout:

```bash
# GitHub release asset
helm install openlog https://github.com/onuragtas/openlog/releases/download/v0.4.0/openlog-0.4.0.tgz \
  -n openlog --create-namespace -f my-values.yaml --timeout 30m
# OCI registry (when the release was pushed to GHCR)
helm install openlog oci://ghcr.io/onuragtas/charts/openlog --version 0.4.0 \
  -n openlog --create-namespace -f my-values.yaml --timeout 30m
```

Starting points: `values-dev.yaml` (3-agent k3d cluster) and `values-production.example.yaml`.

### Migrations (hook ordering)

`openlog-migrate` runs as a Job hook with `backoffLimit` retries (exponential backoff):

`openlog-migrate` applies the PostgreSQL migrations first (auth.mode=postgres), then the ClickHouse schema.

| Mode | Hook | Why |
|---|---|---|
| external (and `postgres.mode: external`) | `pre-install,pre-upgrade` | Schema exists before new pods start. |
| `dependencies.mode: operators` or `postgres.mode: operator` | `post-install,pre-upgrade` | On first install ClickHouse/Kafka/PostgreSQL do not exist until the chart's CRs are created. |

The optional bootstrap Job runs `post-install,post-upgrade` with hook weight 5 (after migrate); it
applies the PostgreSQL migrations itself, so it also works when migrate runs later.
PostgreSQL migrations take an advisory lock, so concurrent migrators are safe.

In operators mode, **do not use `--wait` on the first install**: Helm would wait for the openlog
Deployments to become ready before running post-install hooks, while the services may not
become ready until the schema exists. Use a long `--timeout` (the hook waits for the
operators to bring up Keeper, ClickHouse and Kafka). Schema migrations are idempotent
(`IF NOT EXISTS`, `ON CLUSTER`), so re-running them on every upgrade is safe and also creates
tables on newly added shards/replicas. Override with `migrate.hookEvents`.

### Secrets

- `auth.existingSecret`: a Secret with the keys that apply: `auth.clickhousePasswordKey` (default
  `clickhouse-password`), `auth.postgresDsnKey` (`postgres-dsn`, postgres external mode),
  `bootstrap.ownerPasswordKey` / `bootstrap.licenseKeyKey` (bootstrap Job), `auth.licenseKeysKey`
  (`license-keys`, static mode only).
- Otherwise the chart creates `<fullname>-credentials` from `clickhouse.password`,
  `postgres.external.dsn`, `bootstrap.ownerPassword`, `bootstrap.licenseKey` and `auth.licenseKeys`.
  In operators mode an empty ClickHouse password is replaced by a random one that is preserved across
  upgrades (`lookup`). The Secret is a pre-install hook (so the migrate Job can use it) with
  `helm.sh/resource-policy: keep`; it is **not** deleted by `helm uninstall`.
- With `postgres.mode: operator` the PostgreSQL password is generated by CloudNativePG (`<cluster>-app`).

Exposure: `OPENLOG_POSTGRES_DSN` is injected into ingest, api, migrate and bootstrap;
`OPENLOG_CLICKHOUSE_PASSWORD` into processor, api and migrate; `OPENLOG_LICENSE_KEYS` into ingest and
api only in static mode. PostgreSQL stores only hashes of keys, session tokens and invitation tokens.

## Load balancing OTLP/gRPC

OTLP/gRPC clients keep long-lived HTTP/2 connections. A Kubernetes Service (kube-proxy, L4)
balances **connections**, so ingest replicas added by the HPA get no traffic from clients that
are already connected. Use one of:

1. `envoy.enabled=true` - Envoy Deployment in front of ingest. It resolves the headless
   Service `<fullname>-ingest-headless` (STRICT_DNS, 5s refresh), balances per request
   (`LEAST_REQUEST`), health-checks `/readyz` on 9464, and sets `max_connection_duration` so
   clients reconnect and spread across Envoy replicas behind an L4 `LoadBalancer`.
2. `ingest.ingress` with an HTTP/2-aware controller (e.g. ingress-nginx with
   `nginx.ingress.kubernetes.io/backend-protocol: "GRPC"`; separate hosts for gRPC and HTTP).
3. `gateway.enabled` - `GRPCRoute` (4317) and `HTTPRoute` (4318) for a Gateway API implementation.

OTLP/HTTP (4318) is short request/response traffic and balances acceptably at L4.

## Probes, shutdown, security

- Startup and liveness: `GET /healthz :9464`; readiness: `GET /readyz :9464`.
- `terminationGracePeriodSeconds`: ingest 30 (produce timeout 10s), api 45 (query timeout 30s),
  processor 90 (flush interval + insert timeout with a retry + offset commit). Raise the
  processor value if you raise `OPENLOG_PROCESSOR_INSERT_TIMEOUT`.
- ingest/api use a `preStop` `sleep` so endpoints are removed before SIGTERM. Ingest sleeps 15 s:
  longer than Envoy needs to drop the endpoint (2 failed health checks x 5 s, DNS refresh 5 s).
  Envoy additionally retries requests that never reached ingest (`connect-failure`,
  `refused-stream`, `reset-before-request`) on another pod.
- Pods run as UID 65532, non-root, read-only root FS, all capabilities dropped, seccomp RuntimeDefault,
  no service account token.
- Scheduling: soft pod anti-affinity + topology spread on `kubernetes.io/hostname` (optionally zones).
  Keeper pods get a soft hostname anti-affinity unless `clickhouse.altinity.keeper.affinity` is set
  (node-local volumes still pin a pod to the node where its PV was created).
- `GOMEMLIMIT` is set from the container memory limit (`resourceFieldRef`), so the Go GC reacts
  before the cgroup OOM killer. Always set a memory limit on openlog containers.

## Monitoring

`metrics.podAnnotations` adds `prometheus.io/*` annotations; `metrics.serviceMonitor.enabled`
creates a ServiceMonitor for all services labelled `openlog.io/metrics: "true"` (port `admin`
`/metrics`, and Envoy `/stats/prometheus`). Enable `kafka.strimzi.kafkaExporter` for consumer lag.

## Main values

| Key | Default | Description |
|---|---|---|
| `image.repository` / `image.tag` | `ghcr.io/onuragtas/openlog` / appVersion | |
| `dependencies.mode` | `external` | `external` or `operators` |
| `auth.mode` | `postgres` | `postgres` or `static` (dev/tests) |
| `auth.existingSecret` | `""` | Existing credentials Secret |
| `auth.session.*`, `auth.signupEnabled`, `auth.login.*`, `auth.invitationTTL`, `auth.trustedProxies` | see values | api user auth (config.md) |
| `auth.cache.ttl` / `negativeTTL` / `maxStale` | `60s` / `10s` / `15m` | ingest license key cache |
| `auth.licenseKeys` | `""` | static mode license keys (chart-generated Secret) |
| `postgres.mode` | `external` | `external` or `operator` (CloudNativePG) |
| `postgres.external.dsn` | placeholder | external mode |
| `postgres.operator.instances` | `3` | operator mode (`values-dev.yaml`: 1) |
| `bootstrap.enabled` | `false` | first organization/owner hook Job |
| `kafka.topicPrefix` | `openlog` | |
| `kafka.topics.partitions` / `replicationFactor` / `minInsyncReplicas` | `48` / `3` / `2` | kafka.md cluster profile |
| `kafka.topics.retentionMs` / `maxMessageBytes` | `86400000` / `12582912` | kafka.md |
| `kafka.topics.create` | `true` | KafkaTopic CRs (operators) or openlog-migrate (external) |
| `kafka.external.brokers` | placeholder | external mode |
| `kafka.strimzi.nodePools` | 3 x controller+broker | operators mode |
| `clickhouse.cluster` | `openlog` | `OPENLOG_CLICKHOUSE_CLUSTER`, CHI cluster name |
| `clickhouse.user` | `openlog` (operators) / `default` (external) | |
| `clickhouse.external.addrs` | placeholder | external mode |
| `clickhouse.altinity.shards` / `replicas` | `2` / `2` | operators mode |
| `clickhouse.altinity.keeper.replicas` | `3` | |
| `<component>.replicaCount` | 2 / 3 / 2 | used when HPA disabled |
| `<component>.autoscaling.*` | enabled | processor `maxReplicas` must be <= partitions (enforced) |
| `<component>.config` | config.md defaults | service env vars |
| `<component>.pdb.maxUnavailable` | `1` | |
| `envoy.enabled` | `false` | L7 LB for ingest |
| `ingest.ingress.*`, `api.ingress.*`, `gateway.*` | disabled | |
| `migrate.hookEvents` | auto | see above |
| `metrics.serviceMonitor.enabled` | `false` | |

Render-time checks fail the install when: the mode is unknown; external addresses are missing;
processor replicas / HPA max exceed partitions; `minInsyncReplicas > replicationFactor`;
a stale `clickhouse.database` other than `openlog` is set;
replication factor exceeds the Strimzi broker count; Keeper replicas are even;
`auth.mode`/`postgres.mode` are unknown; external PostgreSQL has no DSN; the bootstrap Job lacks
`ownerEmail` or runs in static mode.

## Upgrades and uninstall

- Partitions can only grow; changing `kafka.topics.partitions` updates the KafkaTopic (operators
  mode) but not existing topics in external mode. Read [scaling.md](../../../docs/operations/scaling.md)
  first: key-to-partition mapping changes.
- Changing `clickhouse.altinity.shards` adds shards but does **not** move existing data.
- `helm uninstall` keeps the credentials Secret, Kafka PVCs (`deleteClaim: false`) and ClickHouse
  PVs (`reclaimPolicy: Retain`). Delete them explicitly for a full teardown.

## Upgrades and the optional updater

`helm upgrade --set image.tag=<version>` runs the migrate hook (`pre-upgrade`) with the new image before the
Deployments roll; migrations are `expand` (compatible with the running pods) and `contract` migrations wait
until no older pod heartbeats. Recommended for automation: GitOps (Argo CD / Flux) pinning the chart version.

Alternatively enable the updater CronJob (`updater.enabled=true`, `updater.mode=notify|auto`, `schedule`,
`maintenanceWindow`, `channel`, `trustedKeys`, `imageRepository`). In `auto` mode `openlog-updater -k8s -once`
verifies the signed release manifest, runs `openlog-migrate` as a Job with the new image digest, patches the
images of the ingest/processor/api Deployments, waits for the rollouts and the new version, and patches the
previous images back if anything fails. Its Role only allows get/patch on these Deployments, create/get Jobs
and reading pods/logs. The processor and updater also receive the PostgreSQL env (heartbeats, status).
A later `helm upgrade` resets images to `image.tag`, so keep it in sync. Details:
[docs/operations/upgrading.md](../../../docs/operations/upgrading.md).
