# Contract: Infrastructure cost (v1)

How openlog turns "this host is an `m5.large` in `eu-central-1`" into "this service costs you $420 a month, and 60% of
that CPU is idle". Implemented by `internal/cost` (price table and attribution), `internal/api/cost.go` (endpoints) and
the agent's instance-fact collection (`agents/infra/internal/resource/cloud.go`).

> **These numbers are estimates, not billing data.** openlog never talks to a cloud billing API. It knows what machine
> a host is and multiplies by a list price from a table in this repository. §2 names everything the estimate ignores.
> Use it to compare services and find idle capacity — never to reconcile an invoice.

## 1. Where the instance facts come from

The infra agent asks the instance metadata service **once at start-up** and attaches the answer to every payload as
resource attributes (semantic-conventions.md §1 "Cloud instance facts"): `cloud.provider`, `cloud.platform`,
`cloud.region`, `cloud.availability_zone`, `cloud.account.id`, `host.type` (the instance type) and
`openlog.host.lifecycle` (`on-demand`, `spot`, `preemptible`).

| Provider | Endpoint | Fields |
|---|---|---|
| AWS | `169.254.169.254/latest/dynamic/instance-identity/document` (IMDSv2 token first, IMDSv1 fallback), `/latest/meta-data/instance-life-cycle` | `instanceType`, `region`, `availabilityZone`, `accountId`, spot/on-demand |
| GCP | `169.254.169.254/computeMetadata/v1/instance/{machine-type,zone,scheduling/*}` (`Metadata-Flavor: Google`) | machine type, zone (region derived), project id, `provisioning-model` / `preemptible` |
| Azure | `169.254.169.254/metadata/instance?api-version=2021-02-01` (`Metadata: true`) | `vmSize`, `location`, `zone`, `subscriptionId`, `priority` |

The whole probe is bounded to **1.5 s**, one request to **500 ms**. It always uses the link-local IP (never DNS, so a
host where `metadata.google.internal` does not resolve waits for nothing) and never an HTTP proxy from the environment,
so a metadata request cannot leave the machine. The DMI system vendor (`/sys/class/dmi/id/sys_vendor`) only decides
which provider is asked first; a wrong or absent hint costs one extra probe and never suppresses detection.

**A host that is not in a cloud** gets a refused connection on the link-local address, or at worst hits the deadline
behind a firewall that drops the packets. It then carries none of these attributes, and start-up is otherwise
unaffected — no error, no retry loop, no repeated probing. Such a host is priced per vCPU/GB (§2) or reported as
unpriced; it is never reported as free. `host.cloud_metadata: off` skips the probe entirely.

## 2. The price table

`internal/cost/prices.json` is compiled into the binary. It holds, per provider, the hourly on-demand and spot price of
the common instance families, a per-vCPU/per-GB fallback rate, and a per-region multiplier relative to a reference
region (`us-east-1`, `us-central1`, `eastus`). It carries its own `version`, `updated` date, `currency`, the `sources`
the numbers were read from, and a `note` that the API returns with every response.

**What the prices are:** approximate public list prices for Linux instances, collected by hand on the `updated` date.

**What they ignore** — every one of these makes a real bill differ, often by a lot:

- committed-use discounts, reserved instances, savings plans and enterprise agreements;
- credits, free tiers, promotional and negotiated pricing;
- taxes and currency conversion;
- OS and software licences (Windows, RHEL, marketplace images);
- **storage, snapshots, network egress, load balancers, managed services** — only the compute instance is priced;
- per-second billing granularity, minimum billing periods and stop/start behaviour;
- regions not in the multiplier list (they are priced at the reference region's rate);
- spot prices, which move continuously — the table's spot column is a typical value, not the price you paid.

A price therefore says *"a machine like this one lists at about this much"*, and the comparison between two services on
the same fleet is far more trustworthy than the absolute number.

### Resolution order

1. exact instance type for the provider, from the operator's override if it defines one (`source: "override"`),
   otherwise the built-in table (`source: "table"`);
2. otherwise the provider's per-vCPU/per-GB fallback rate, or the `default` rate for an unknown provider, applied to
   the host's measured capacity (`source: "fallback"`);
3. otherwise **no price** (`source: "none"`): the host is reported with `"priced": false` and counted in
   `unpriced_hosts`, never as costing nothing.

The region multiplier is applied in cases 1 and 2. For a spot or preemptible machine the spot column is used; when the
entry has no spot price, the on-demand rate is used and the result's `note` says the estimate is too high. The fallback
rate is an on-demand rate and says so for spot machines.

### Operator override

`OPENLOG_COST_PRICES_FILE` points at a JSON file merged over the built-in table at start-up, so a price can be
corrected without waiting for a release. Only what the file names is replaced — one instance type, one provider's
fallback rate, one provider's region multipliers — so a correction for a single machine does not drop the rest of the
table:

```json
{
  "updated": "2026-10-01",
  "instances": {"aws": {"m5.large": {"on_demand": 0.0712, "spot": 0.0301}}},
  "fallback": {"aws": {"vcpu_hour": 0.028, "gb_hour": 0.004}},
  "region_multipliers": {"aws": {"eu-central-1": 1.09}}
}
```

An unknown key, invalid JSON or an unreadable file **stops the api**: silently pricing with the numbers the operator
meant to replace would be worse than a failed start-up. `GET /api/v1/costs/prices` returns the effective table with
`override_file` and `overridden_keys`, so a correction can be confirmed from the UI.

## 3. Attribution

All of it is pure arithmetic over data openlog already stores (`internal/cost/attribute.go`); nothing is inferred from
a bill. Over the requested range, for host *h*:

```
host cost      = price per hour × hours h reported
workload share = 0.5 × (workload core-hours / host core-hours)
               + 0.5 × (workload byte-hours / host byte-hours)
host usage     = 0.5 × (used cores / vCPUs) + 0.5 × (used bytes / total bytes)
```

and the host's cost decomposes into four buckets that always add up to it exactly:

| Bucket | What it is |
|---|---|
| `services` | Σ shares of containers linked to an APM service |
| `unallocated` | Σ shares of containers with no linked service |
| `unattributed` | `host usage − Σ container shares`: real work no container explains |
| `idle` | `1 − host usage`: capacity nobody used |

### Inputs

| Quantity | Source |
|---|---|
| host vCPUs, memory | `system.cpu.logical.count`, `system.memory.limit` (peak over the range) |
| host usage | `system.cpu.utilization` (`1 − idle`, falling back to the sum of the non-idle modes) and `system.memory.usage{used}` |
| hours reported | distinct one-minute buckets of the host's per-host series, capped at the range |
| container CPU, memory | `container.cpu.utilization` (a fraction of the whole host → cores) and `container.memory.usage` |
| container → service | `apm_service_containers` (apm.md §1), newest link wins |

All of it is read from the 1-minute rollup `metrics_1m`, which keeps 395 days — so a cost range is not limited to the
30-day raw metric retention.

### Assumptions, stated honestly

- **CPU and memory weigh the same (0.5 / 0.5).** Any split is arbitrary. Weighting CPU alone makes memory-heavy
  services look free and vice versa; equal weights are the least surprising choice and the formula above lets anyone
  reproduce the arithmetic by hand.
- **A host costs money for the hours it reported, not for the hours it existed.** A host that reported for half the
  range is charged half. openlog prices what it observed; a machine that was up but silent is invisible to it, and a
  machine that was deleted stops costing at its last data point.
- **Usage is a mean over the range.** A service that is idle for 23 hours and saturates one CPU for one hour is
  attributed the same as one that uses 1/24 of a CPU all day. Shorter ranges show the difference.
- **`container.cpu.utilization` is a fraction of the whole host**, so a container's cores are that fraction × the
  host's vCPUs. On a host whose vCPU count is unknown the container contributes memory only.
- **Requests and limits are not used**, only actual usage. A service that reserves 8 GB and uses 1 GB is charged for
  what it used; the reserved-but-unused capacity shows up as idle on its host, which is where an operator can act on it.
- **Only containers are attributed.** Work outside a container (a database on the machine, a cron job, the kernel)
  cannot be linked to a service, so it lands in `unattributed` — visibly, rather than being silently shared out or
  mislabelled as idle.
- **Over-subscription is scaled, not hidden.** If the container shares sum above the host's usage (double counting,
  sampling noise, a container moving between hosts inside the range) they are scaled down to fit and the host is marked
  `oversubscribed`. The host's total stays exact and idle never goes negative.
- **Idle is never redistributed.** It is reported as its own number at host level (`idle`, `idle_share`) and fleet
  level. Spreading idle cost over the services would make every service look more expensive and hide the one finding
  that is directly actionable.
- **A host with no instance facts and no capacity has no price.** It is counted in `unpriced_hosts` rather than
  contributing 0, so a fleet total is never quietly too low.

## 4. API and UI

Endpoints, response shapes and status codes: [api.md](api.md) "Costs". The UI's Costs view (`/costs`) shows the total,
the run rate per hour, the idle share, the trend over the range and the breakdown by service and by host; the host
detail page shows one host's cost and its split. Both render the price table's `note` next to the numbers.

## 5. What is deliberately not here

- **No ClickHouse migration.** Every input already exists in `metrics_1m`, `hosts`, `containers` and
  `apm_service_containers`; a cost-specific rollup would be a performance optimisation, not new information, and is not
  worth a schema change until a large fleet shows the query is too slow.
- **No cloud billing integration.** Reading a real bill (Cost and Usage Report, Cloud Billing export) is a different
  feature with different credentials and a different trust model. When it exists it will *replace* the price table as
  the source, and this contract's split (§3) will still be how a bill is attributed to services.
- **No per-tenant price tables.** The table is an operator setting, not an organization one.
