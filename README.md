# openlog

Open-source observability platform — infrastructure monitoring, APM, logs and alerts — built from scratch.
Runs as a hosted service or self-hosted, from a single machine up to a horizontally scaled cluster.

> Status: **M0 (contracts and skeleton)**. Not production ready.

## Components

| Path | What |
|---|---|
| `cmd/openlog-ingest` | OTLP gateway (HTTP :4318, gRPC :4317) → Kafka |
| `cmd/openlog-processor` | Kafka → ClickHouse |
| `cmd/openlog-api` | Query API (:8080) |
| `cmd/openlog-migrate` | ClickHouse schema + Kafka topics |
| `cmd/openlog-allinone` | ingest + processor + api in one process (`single` profile) |
| `cmd/openlog-loadgen` | OTLP load generator |
| `schema/clickhouse` | ClickHouse schema |
| `deploy/compose` | `single` profile (Docker Compose) |
| `deploy/helm/openlog` | `cluster` profile (Kubernetes) |
| [`agents/infra`](agents/infra) | Linux host agent: metrics, inventory, auto-discovery (Apache-2.0) |

## Documentation

- Plan (Turkish): [docs/plan](docs/plan/README.md) — vision, decisions, architecture, cluster design, infra agent, roadmap, teams, risks
- Contracts: [docs/contracts](docs/contracts) — semantic conventions, Kafka, configuration, API
- Operations: [docs/operations](docs/operations)

## License

Backend and UI: [AGPL-3.0](LICENSE). Agents and SDKs: Apache-2.0.
