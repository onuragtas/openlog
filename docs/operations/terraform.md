# Terraform provider (`terraform-provider-openlog`)

The provider manages an openlog organization's **configuration** as code: alert rules, notification channels,
routing rules, service level objectives and dashboards (D-131). It is a client of the documented HTTP API
(`/api/v1`, [api.md](../contracts/api.md)) and needs nothing else — no database access, no file on the
server.

Source: `terraform/` in this repository. It is its own Go module (`github.com/onuragtas/openlog/terraform`)
under Apache-2.0 like the agents and SDKs (D-004), so the plugin framework's dependency tree never reaches
the AGPL-3.0 server binaries.

What it does **not** manage: telemetry, incidents, mute windows, holiday calendars, share links, scheduled
reports, API keys, members and invitations. Incidents and mutes are operational state rather than desired
state; the rest is a deliberate first scope (§7).

## 1. A credential that may write

**This is the prerequisite to check first.** An openlog API key (`ola_…`) carries a role of its own —
`viewer` (the default), `member` or `admin` ([api.md "Roles"](../contracts/api.md#roles), D-133) — and the
provider needs one whose role may write. Use **`admin`**: a key owns nothing, so a rule, SLO or dashboard
created with a `member` key has no creator and that same key cannot change it afterwards; an `admin` key
manages everything in the organization, which is what a provider that owns its resources does.

The key is created by a signed-in admin or owner (a key never creates keys). Settings → API keys in the web
UI creates a `viewer` key; to choose the role, call the API with an admin or owner session:

```sh
curl -sS -X POST https://openlog.example.com/api/v1/api-keys \
  -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" -b "openlog_session=$SESSION" \
  -d '{"name": "terraform", "role": "admin"}'
```

`role` above `viewer` needs an admin or owner and never exceeds the caller's own role (`403`); an unknown role
is a `400`. The response shows the key once — it is `OPENLOG_API_KEY` for the provider (§3).

A **read-only (`viewer`) key still fails**: `plan` works, because reads are allowed, and the first `apply`
stops with

```
Error: Could not create the alert rule: the API key may not write

  this API key's role (viewer) does not allow this operation

  The openlog API accepts writes from a credential with write permission; a read-only API key is
  refused here. See docs/operations/terraform.md.
```

Nothing in the provider assumes a session cookie: it authenticates exactly as [mcp.md](mcp.md) does, with
`Authorization: Bearer` and `X-Openlog-Org-Id`.

Channels additionally need `OPENLOG_SECRETS_KEY` on the server; without it the API refuses channel writes with
`409 failed_precondition` (alerting.md §5.4).

## 2. Building and using it locally

The provider is not published to a registry. Build it and point Terraform at the binary with a dev override:

```sh
cd terraform
go build -o ~/.terraform.d/plugins/terraform-provider-openlog ./cmd/terraform-provider-openlog
```

`~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "onuragtas/openlog" = "/Users/you/.terraform.d/plugins"
  }
  direct {}
}
```

With a dev override Terraform skips `terraform init` for this provider and prints a warning on every command;
that is expected.

## 3. The provider block

```hcl
terraform {
  required_providers {
    openlog = {
      source = "onuragtas/openlog"
    }
  }
}

provider "openlog" {
  endpoint = "https://openlog.example.com" # or OPENLOG_ENDPOINT
  # api_key is read from OPENLOG_API_KEY; keep it out of the configuration
  # org_id  = "…"                          # or OPENLOG_ORG_ID, only with several organizations
  # timeout_seconds = 30
}
```

| Attribute | Environment variable | Notes |
|---|---|---|
| `endpoint` | `OPENLOG_ENDPOINT` | Base URL **without** `/api/v1`; a trailing `/api/v1` is trimmed |
| `api_key` | `OPENLOG_API_KEY` | Sensitive. Prefer the environment variable: a key written in the configuration ends up in the state file |
| `org_id` | `OPENLOG_ORG_ID` | `X-Openlog-Org-Id`; only needed when the credential belongs to several organizations, and it must match the key's organization |
| `timeout_seconds` | — | One API request, 1–3600, default 30 |

## 4. A worked example

A Slack channel, a metric threshold rule that notifies it, and an availability SLO for the checkout service:

```hcl
resource "openlog_notification_channel" "oncall_slack" {
  name = "on-call Slack"
  type = "slack"

  secrets = {
    url = var.slack_webhook_url # sensitive; never printed in a plan
  }
}

resource "openlog_alert_rule" "host_cpu_high" {
  name        = "Host CPU high"
  description = "CPU above 90 % for five minutes, one incident per host."
  type        = "metric_threshold"
  severity    = "critical"

  interval_seconds = 60
  for_seconds      = 300

  condition = jsonencode({
    metric             = "system.cpu.utilization"
    aggregation        = "avg"
    window_seconds     = 300
    group_by           = ["host"]
    operator           = "gt"
    threshold          = 0.9
    recovery_threshold = 0.8
    filters = [
      { field = "attr.cpu.mode", op = "not_in", values = ["idle"] },
    ]
  })

  channel_ids = [openlog_notification_channel.oncall_slack.id]
  labels      = { team = "infra" }
}

resource "openlog_slo" "checkout_availability" {
  name         = "Checkout availability"
  service_name = "checkout"
  environment  = "prod"
  sli_type     = "availability"
  objective    = 99.9
  window_days  = 28
}

# Burn-rate alerting on that SLO is an ordinary alert rule of type slo_burn.
resource "openlog_alert_rule" "checkout_budget_burn" {
  name     = "Checkout error budget burning"
  type     = "slo_burn"
  severity = "critical"

  condition = jsonencode({
    slo_id = openlog_slo.checkout_availability.id
  })

  channel_ids = [openlog_notification_channel.oncall_slack.id]
}
```

`terraform apply`, then `terraform plan` again: the second plan is empty. That is worth checking after any
change to a `condition`, because it is what §6 is about.

## 5. Resources and data sources

| Resource | Manages | API |
|---|---|---|
| `openlog_alert_rule` | one alert rule of any of the ten types | `/api/v1/alerts/rules` |
| `openlog_notification_channel` | Slack, e-mail, webhook, Teams, PagerDuty, Opsgenie | `/api/v1/alerts/channels` |
| `openlog_alert_routing_rule` | which channels an incident reaches when it opens | `/api/v1/alerts/routing-rules` |
| `openlog_slo` | a service level objective | `/api/v1/slos` |
| `openlog_dashboard` | a whole dashboard document (pages, widgets, variables) | `/api/v1/dashboards` |

| Data source | Looks up |
|---|---|
| `openlog_alert_channel` | a channel by `id` or `name` (masked `secret_hints`, never secrets) |
| `openlog_dashboard` | a dashboard by `id` or `name`; its `document` is the stored `{variables, pages}` JSON |

Every resource supports `terraform import <address> <id>`, where the id is the one the API and the web UI URL
show. A channel's secrets cannot be imported — openlog never returns them — so after importing a channel the
configuration must carry its `secrets` again and the next apply re-sends them.

### `condition` is JSON

Each of the ten rule types has its own condition shape (alerting.md §2.2–§2.12), so `condition` is a JSON
document written with `jsonencode({…})` rather than ten typed blocks. The API validates it field by field and
reports the offending path, which the provider attaches to the `condition` attribute:

```
Error: Invalid value

  with openlog_alert_rule.host_cpu_high,
  on main.tf line 21, in resource "openlog_alert_rule" "host_cpu_high":
  21:   condition = jsonencode({

condition.threshold: required
```

The complete document openlog stores — every default it filled in — is the computed attribute
`condition_effective`, which is the quickest way to see what a three-line condition actually means.

## 6. Drift, defaults and concurrent changes

**Reads decide.** After every create and update the provider stores the API's answer, not the plan. Server-side
normalization therefore shows up as a diff on the next plan instead of drifting quietly.

**Deleted outside Terraform.** A read that gets `404` removes the resource from state, and the next plan
recreates it. A delete of something already gone is not an error.

**Defaulted conditions do not cause perpetual diffs.** openlog parses a condition and echoes it back complete:
a three-field condition comes back with ten. Storing that answer verbatim would make every plan show a diff;
storing the configuration would hide real drift. The provider keeps the configured document as long as openlog
agrees about **every field the configuration sets**, and replaces it with openlog's document as soon as one of
them differs — which is exactly when a diff should appear. Adding a field to the configuration is a normal
diff; the rest of the stored document stays visible in `condition_effective`.

**Optimistic concurrency.** Alert rules and dashboards carry a `version` that openlog raises on every change.
The provider sends the version it last read with the update, so a rule someone edited in the web UI in the
meantime is refused instead of silently overwritten:

```
Error: Could not update the alert rule: the object changed in openlog

  conflicting change; reload and try again

  Someone changed this object (or a limit was reached) since Terraform last read it. Run
  'terraform refresh' (or plan again) and re-apply; this provider never overwrites a newer version blindly.
```

`terraform apply -refresh-only` (or an ordinary plan, which refreshes) picks the new version up; then the
change applies. The same message covers the API's other `409`s — a second default routing rule, the 1000th
rule of an organization, the 201st SLO, a channel write without `OPENLOG_SECRETS_KEY` — because they have the
same fix: look at what is there before writing again.

**Routing rules have no version.** They are a small ordered list, and openlog replaces them whole; a
concurrent edit to a routing rule is overwritten by the next apply. Order comes from `position`, so give each
route its own number when the order matters.

**Page and widget ids.** openlog assigns them and keeps them when a write carries them, so the provider sends
back the ids it last read: editing a widget keeps its identity and the dashboard's version history. Inserting
a page in the middle of the list shifts the ids of the pages after it, which is harmless but shows up in the
plan.

**What openlog rewrites, the provider does not hide.** Two cases are worth knowing, because both surface as an
apply-time error rather than a silent change: `for_seconds` on a `discovery` or `apm_error` rule (those types
ignore it and openlog stores 0), and the three numbers of a `flapping` block next to `enabled = false` (openlog
stores its own defaults). The second is caught at validation time with a message that says so.

## 7. Secrets

Channel secrets — a Slack or webhook URL, an HMAC secret, an SMTP password, a PagerDuty routing key, an
Opsgenie API key — are write-only. openlog encrypts them with AES-256-GCM before they reach PostgreSQL and
never returns them; a read answers with masked `secret_hints` (`••••••  (set)`) instead.

In the provider:

- `secrets` is marked **sensitive**, so a plan prints `(sensitive value)` and never the value itself;
- a read never touches `secrets`: there is nothing to compare against, so a secret rotated in the web UI is
  invisible to Terraform — rotate it here instead;
- `generated_secrets` (a webhook's `hmac_secret` when none was given) is sensitive too and is returned only by
  the create response;
- the values are nonetheless written to **state**, like every Terraform secret, so the state belongs in an
  encrypted backend with restricted access.

## 8. Tests

`cd terraform && go test ./...` runs the unit tests: the API client against an `httptest` fake (auth headers,
the error shape, the field path, 404 and 409 handling) and the schema mapping in both directions. The
acceptance tests use the same fake API instead of a live openlog and are skipped unless `TF_ACC=1` and a
`terraform` binary are present, so the normal test run needs neither.

CI runs the module in the `go` job of `.github/workflows/ci.yml` ([ci.md](ci.md)): `gofmt`, `go vet` and
`go test`, next to `libs/release` and `agents/infra`.
