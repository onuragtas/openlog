# Contract: Service level objectives (D-125)

An **SLO** states how reliable one APM service must be: an SLI (availability or latency), a target percentage
and a rolling window. From it follow the **error budget** (how many bad requests the target still allows) and
the **burn rate** (how fast that budget is being spent), which the `slo_burn` alert rule watches with
multi-window multi-burn-rate conditions.

This file is binding for the PostgreSQL table `slos` (`migrations/postgres/0087_slo.sql`,
[postgres.md](postgres.md#slos-0087_slo)), the budget math (`internal/slo`), the API (`/api/v1/slos/*`,
[api.md](api.md#service-level-objectives)) and the alert rule type `slo_burn`
([alerting.md](alerting.md) §2.11). Everything is computed **at query time** from the APM rollup
`apm_transactions_1m` ([apm.md](apm.md) §4, §8); nothing is precomputed and no new telemetry table is written.

## 1. Objects

| Field | Type | Notes |
|---|---|---|
| `name` | string | 1–200 characters |
| `description` | string | ≤ 2000 characters |
| `service_name` | string | required, exact `service.name` (apm.md §1) |
| `service_namespace`, `environment` | string \| null | `null` = every namespace/environment (aggregated), a string (also `""`) = exact match, as in APM alert conditions |
| `sli_type` | enum | `availability` · `latency` |
| `latency_threshold_ms` | integer | `latency` only: 1–600000, required there and rejected for `availability` |
| `objective` | number | target in percent, `50 ≤ objective < 100` (e.g. `99.9`) |
| `window_days` | enum | rolling window `7`, `28` or `30` days |

Audit metadata: `created_by`/`created_at` and `updated_by`/`updated_at` (the API returns the e-mail
addresses). At most **200 SLOs per organization** (`409 failed_precondition`). Validation errors are
`400 invalid_argument` with the field path (`objective: must be at least 50 and below 100 (percent)`).
Every write is in the audit log (`slo.create`, `slo.update`, `slo.delete`, target type `slo`), written in the
same transaction as the change.

Several SLOs may target the same service (for example availability and latency, or one per environment).
An SLO is a definition only: it fires nothing by itself, it is the input of the `slo_burn` rule (§3).

## 2. Budget math (`internal/slo`)

All counts come from `apm_transactions_1m` and are **weighted** by the sampling probability (apm.md §4):
`requests` is Σ weight of entry spans, `errors` Σ weight of the failed ones. Reads always re-aggregate with
`GROUP BY` (sharding correctness, apm.md §8), and the current incomplete minute is not read: ranges end at the
last complete minute.

| Quantity | Definition |
|---|---|
| `good` (availability) | `requests − errors`, clamped to `[0, requests]` |
| `good` (latency) | weight of requests with duration ≤ `latency_threshold_ms`, from the stored `duration_hist` buckets (apm.md §4.1, `apm.HistCountLE`) — **never** from `avg_ms`; errors are not subtracted (a fast error is good for a latency SLI, as it is for the request count) |
| `bad` | `requests − good` |
| `sli` | `good / requests` (no value without requests) |
| `budget_requests` | `(1 − objective) × requests` — the bad requests the target allows in the range |
| `budget_consumed` | `bad`; `budget_remaining` = `budget_requests − bad` (negative once the target is missed) |
| `remaining_ratio` | `1 − bad / budget_requests` (no value without requests); 1 = untouched, 0 = exhausted, < 0 = missed |
| `burn_rate` | `(bad / requests) / (1 − objective)` — 1 spends the budget exactly over the window, 14.4 spends 2 % of a 30-day budget in an hour |
| `met` | `sli ≥ objective` (a range without requests counts as met: nothing failed) |

**Threshold precision.** The latency SLI counts the histogram buckets fully below the threshold plus a linear
share of the bucket containing it, so the counted weight is within one bucket width (≤ 9.1 %, apm.md §4.1) of
an exact per-span count and independent of how the spans were sampled.

**Bucketing is exact.** Sums and `sumMap` histograms of adjacent buckets merge without loss, so the budget of
a range is the same whether it is read as one bucket or as 43 200 minutes. Reads therefore pick the step from
the range (the API's usual `step` rules), not from the math.

**Series (burndown).** The UI series has one point per step with the bucket's own `requests`, `good`, `bad`,
`sli` and `burn_rate`, plus `remaining_ratio`: the share of the **window's** budget left after that bucket
(cumulative bad requests against the budget of the whole window). The last point therefore equals the window's
`remaining_ratio`, and empty buckets keep the line flat.

**Window edges.** A window `[end − w, end)` contains exactly the buckets that lie completely inside it; the end
is exclusive. Burn windows use 1-minute buckets, so every window that is a whole number of minutes is exact.

**Retention.** The rollup keeps `OPENLOG_APM_RETENTION_DAYS` (30 by default, apm.md §8). A 30-day window is at
that edge: the oldest minutes of the window may already have expired, which makes the budget of the visible
data, not of the whole month.

## 3. Multi-window burn-rate alerting (`slo_burn`)

Rule type `slo_burn` ([alerting.md](alerting.md) §2.11) evaluates one SLO with the multi-window
multi-burn-rate method (Google SRE workbook): a window pair alerts when the budget burns at least `factor`
times too fast over its **long** window *and* still does over its trailing **short** window, so a burst that
has already stopped does not fire and a resolved incident closes quickly.

Defaults (configurable per rule, 1–4 windows):

| Window | Factor | Long | Short | Alerts on |
|---|---|---|---|---|
| `fast` | 14.4× | 1 h | 5 m | 2 % of a 30-day budget in an hour |
| `slow` | 6× | 6 h | 30 m | 5 % of a 30-day budget in six hours |

- The series value is `max over the windows of min(burn(long), burn(short)) / factor`, so it is 1 exactly at a
  window's factor and the rule's comparison is always `gte 1` (the factors carry the configuration). The
  window that produced the value is the label `slo.window`.
- A window whose long window has fewer than `min_requests` weighted requests, or no requests at all, has no
  value; if no window has one, the evaluation has **no value** (missing data `keep`: the state is unchanged).
- One series per rule (key `slo.id=<uuid>`), labels `slo.id`, `slo.name`, `slo.window`, `service.name` and,
  when the SLO fixes them, `service.namespace` and `environment`.
- Evaluation needs PostgreSQL (the definition); without it the rule reports an evaluation error, as do rules
  whose SLO was deleted. Deleting an SLO does not delete its rules.
- Incidents, notifications, mutes, flapping and the evaluation history are the normal alerting paths
  (alerting.md §3–§6); `slo_burn` adds no mechanism of its own.

## 4. API

`GET /api/v1/slos`, `POST /api/v1/slos`, `GET|PUT|DELETE /api/v1/slos/{id}` and
`GET /api/v1/slos/{id}/results` — shapes and roles in [api.md](api.md#service-level-objectives) and
[openapi.yaml](openapi.yaml) (tag `slos`). Reads are telemetry reads (any role, API keys too); writes need a
signed-in member or higher. Not available with `OPENLOG_AUTH_MODE=static` (`404`).

## 5. Not in this version

Per-SLO burn-window defaults (rules carry their own), calendar-aligned windows (only rolling ones), SLOs on
metrics or logs (only APM transactions), budget alerts on the remaining share (only burn rates), and
precomputed budgets (every read aggregates the rollup).
