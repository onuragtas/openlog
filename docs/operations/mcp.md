# MCP server (`openlog-mcp`)

`openlog-mcp` lets AI tools — Claude Code, Cursor, and anything else that speaks the
[Model Context Protocol](https://modelcontextprotocol.io) — query an openlog installation: run OQL,
look at APM services, read log records, check alert incidents and SLO error budgets. Every tool is
**read-only** (D-126).

The server is a front end of the query API, not of the databases. Each tool call is one or two
requests to `/api/v1` carrying **your API key**, so everything the API enforces applies unchanged:
the key authenticates as a `viewer` of its organization, the tenant of every ClickHouse query comes
from that key alone, and the queries run as the read-only ClickHouse user with the organization's
limits ([api.md](../contracts/api.md#authentication), [config.md](../contracts/config.md)). The
process itself needs no ClickHouse, PostgreSQL or Kafka access, and it stores nothing.

## 1. An API key

Create one in the web UI under **Settings → API keys**, or with `openlog-admin bootstrap`
(`OPENLOG_BOOTSTRAP_API_KEY`). The key looks like `ola_…` and is shown only once.

An API key is a read-only viewer of one organization: telemetry endpoints answer, every management
endpoint answers `403`. Ingest license keys (`olk_…`) are **not** API keys and do not work here.

## 2. Transports

| Transport | For | Key |
|---|---|---|
| `stdio` (default) | a local CLI or editor that starts the binary itself | `OPENLOG_MCP_API_KEY` |
| `http` (streamable HTTP at `/mcp`) | a shared or remote server | each request's `Authorization: Bearer ola_…`, else `OPENLOG_MCP_API_KEY` |

In `http` the caller's own key is used for that session, so one deployment serves several
organizations without mixing them; a request with no key and no configured fallback is refused with
`401` before a session starts. Put TLS in front of it as for the API (the binary serves plain HTTP).

Variables: [config.md → `openlog-mcp`](../contracts/config.md#openlog-mcp).

```sh
# local, against a stack on this machine
OPENLOG_MCP_API_KEY=ola_… openlog-mcp

# remote, listening on :8092, keys per request
OPENLOG_MCP_TRANSPORT=http OPENLOG_MCP_API_URL=https://openlog.example.com openlog-mcp
```

With `-transport http` the admin server of every openlog binary is served too
(`OPENLOG_ADMIN_ADDR`, `:9464`): `/healthz`, `/readyz` (checks that the openlog API answers and that
the MCP port listens) and `/metrics` with `openlog_mcp_tool_calls_total{tool,result}` and
`openlog_mcp_tool_duration_seconds{tool}`. The `stdio` transport starts no listener, and logs go to
stderr in both (stdout carries the protocol).

## 3. Tools

All read-only, all scoped to the key's organization.

| Tool | Arguments | Answers |
|---|---|---|
| `run_oql` | `query`, `from`/`to`, `variables` | an OQL aggregate: single row, facets, timeseries or histogram ([oql.md](../contracts/oql.md)) |
| `oql_schema` | `event_type` | event types, attributes, functions, keywords; with `event_type` also the live attribute keys of the last hour |
| `list_services` | `from`/`to`, `namespace`, `environment`, `q` | APM services with their RED metrics (requests, throughput, errors, error rate, latency percentiles, Apdex) |
| `service_summary` | `service`, `from`/`to`, `namespace`, `environment`, `transaction`, `limit` | one service's RED totals and series plus its transactions by time consumed |
| `list_incidents` | `state`, `rule_id`, `severity`, `limit`, `cursor` | alert incidents, newest first, with counts by state |
| `get_incident` | `id` | one incident with its timeline and notification deliveries |
| `search_logs` | `from`/`to`, `q`, `filters`, `order`, `limit`, `columns`, `cursor` | individual log records ([api.md “Fields”](../contracts/api.md#fields) filter conditions) |
| `list_slos` | `status` | SLOs with their error budget over the rolling window |
| `slo_status` | `id`, `step` | one SLO's budget, multi-window burn rates and burndown series ([slo.md](../contracts/slo.md)) |

Ranges default to the last hour. `OPENLOG_MCP_MAX_ROWS` (100) caps every `limit`, so a model cannot
pull an unbounded result into its context. The organization's query limits still apply: a query that
is too expensive comes back as `resource_exhausted`, a slow one as `timeout` — narrow the range or add
filters.

`list_slos` and `slo_status` need an openlog running with PostgreSQL authentication
(`OPENLOG_AUTH_MODE=postgres`); with `static` they answer `404`, as the endpoints do.

## 4. Claude Code

Per project, in `.mcp.json` of the repository (or `claude mcp add`):

```json
{
  "mcpServers": {
    "openlog": {
      "command": "openlog-mcp",
      "env": {
        "OPENLOG_MCP_API_URL": "https://openlog.example.com",
        "OPENLOG_MCP_API_KEY": "ola_…"
      }
    }
  }
}
```

A remote server instead:

```json
{
  "mcpServers": {
    "openlog": {
      "type": "http",
      "url": "https://openlog-mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ola_…" }
    }
  }
}
```

`/mcp` in Claude Code lists the server and its tools once it connects.

## 5. Cursor

`.cursor/mcp.json` in the project (or `~/.cursor/mcp.json` for every project):

```json
{
  "mcpServers": {
    "openlog": {
      "command": "openlog-mcp",
      "env": {
        "OPENLOG_MCP_API_URL": "https://openlog.example.com",
        "OPENLOG_MCP_API_KEY": "ola_…"
      }
    }
  }
}
```

```json
{
  "mcpServers": {
    "openlog": {
      "url": "https://openlog-mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ola_…" }
    }
  }
}
```

Settings → MCP shows the server and lets you enable single tools.

## 6. Users in several organizations

An API key belongs to one organization, so nothing has to be selected. A key of a user who is a member
of several organizations can be pinned with `OPENLOG_MCP_ORG_ID` (stdio) or the header
`X-Openlog-Org-Id` (http); it must name the key's organization, otherwise the API answers `403`.

## 7. Troubleshooting

| Symptom | Cause |
|---|---|
| `openlog rejected the API key` | wrong, revoked or expired key, or an ingest license key (`olk_…`) |
| `openlog refused the request` | the endpoint is not open to API keys, or the organization does not have the feature (SLOs need postgres auth mode) |
| `cannot reach the openlog API at …` | `OPENLOG_MCP_API_URL` wrong or unreachable; it is the **base** URL, without `/api/v1` |
| `resource_exhausted` / `timeout` | the query hit the organization's ClickHouse limits — shorter range, more filters |
| the client shows no tools | check stderr of the process; with `http`, `curl -sS -H 'Authorization: Bearer ola_…' https://…/mcp` must not answer `401` |
