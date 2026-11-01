# Contract: Product analytics (D-150)

**Status: partly built.** Done: the key setting, ingest rules and tables (§1, §3 to §5), the browser and mobile SDKs and
the endpoints of §6, the OQL event type and erasure (§7). User guide:
[Product analytics](../guide/product-analytics.md). Related: [rum.md](rum.md),
[mobile-agent.md](mobile-agent.md), [mobile-crash.md](mobile-crash.md).

Who uses the application, where they came from, what they do and whether they come back — from web and mobile
applications, over longer than the 7-day trace retention.

**Analytics is not a parallel stack either.** The SDKs keep sending the OTLP spans of [rum.md](rum.md); there is
no second endpoint and no second SDK. What is new is a persistent identity (§1), a little more attribution on the
landing page view (§3), and three tables that outlive the spans (§5).

**Not here, deliberately:** push messaging, remote configuration, A/B testing, session replay, region or city.

## 1. Identity

### 1.1 On by default, like Google Analytics and Firebase

DAU, MAU, new users and retention all need someone to be recognised across visits, so **collection is on by default
for a new key and a new SDK**, the way it is in Google Analytics and Firebase Analytics.

**This gives up the argument of `rum.md` §1.1 for those applications.** A session being a visit and not a person is
what made RUM collectable without a consent banner. With a persistent visitor id that no longer holds, and an
application that needs consent has to ask for it. **Whether it does stays the application's question**, as with
`identify()` in rum.md §3.7, and it has what it needs to answer it: see `analyticsCollection` below.

| Switch | Who | Default | Why |
|---|---|---|---|
| `browser_keys.analytics_mode` | the operator | `visitors` on a **new** key; `off` on a key that existed before this feature | An upgrade must not start collecting personal data for an application whose owner never decided to |
| `analyticsCollection` at `init` | the application | `true` | `false` starts with no visitor id, for an application that must ask first |
| `setAnalyticsCollection(enabled)` | the application, at runtime | | The counterpart of Firebase's `setAnalyticsCollectionEnabled`, called from a consent dialog |

| `analytics_mode` | Stored | Answers |
|---|---|---|
| `off` | nothing new; today's behaviour | RUM as it is |
| `sessions` | §5.1 only, with no actor | event volume, attribution and session funnels past 7 days |
| `visitors` | all of §5 | DAU, WAU, MAU, new vs returning, retention, user funnels |

With collection disabled in the application, a key in `visitors` mode behaves like `sessions` for that visitor.

### 1.2 The visitor id

`openlog.rum.visitor.id`: 32 random hex characters, never derived from the device or the person.

- **Web:** `localStorage`, shared by the tabs of one origin. No cookie: a cookie travels to the application's own
  servers, and nothing here needs that. Unavailable storage degrades to no visitor id.
- **Mobile:** `UserDefaults` on Apple platforms; on Kotlin and Dart a file in `visitorDirectory`, which the application
  passes because these SDKs have no platform API to find one. **Without a store no id is sent**, rather than a new one per
  launch, which would count every launch as a new person. `device.id` stays refused (mobile-agent.md §3.2).
- **Lifetime:** rotated 13 months after creation.
- `setAnalyticsCollection(false)` deletes the stored id and stops sending it at once. Enabling it again creates a new
  id, so a withdrawn visitor is not recognised on returning.

The server **drops the attribute unless the key is in `visitors` mode**, so it cannot arrive simply by being sent.
It is validated like `session.id`: anything but 32 hex characters is removed, and the span is kept.

**The id reaches the device only after the server says so.** `GET /v1/rum/config` returns `analytics_mode`. Until it
answers `visitors` the id lives in memory, so a key whose operator left analytics off leaves nothing behind in a
visitor's browser, and an id stored earlier is deleted when the answer changes. A page view sent before the answer
still carries the in-memory id, so a new visitor's first page view is not lost.

**A withdrawal is remembered.** `setAnalyticsCollection(false)` stores that fact, because otherwise the next load would
give someone who just said no a new id. Only an explicit `setAnalyticsCollection(true)` undoes it, never the default of
`analyticsCollection`.

### 1.3 The actor

Tables count an **actor**: the visitor id when there is one, otherwise `user.id`.

**One device is one actor when a visitor id exists**, even after sign-in, so a person on a phone and a laptop
counts twice. That is the stated limit of v1. Merging identities retroactively is a much larger design, and a
wrong merge is worse than a double count.

Only a hash reaches the long-lived tables (§5):

```
actor_hash = sipHash64(tenant_id, actor)
```

This is **pseudonymisation, not anonymisation**; the tables remain personal data and §7 applies to them.

## 2. Sampling changes when there is a visitor

Today a session is sampled independently (rum.md §1.1). For retention that is wrong: at a 10% rate, a loyal user
would look as if they had vanished on nine visits out of ten. So once a visitor id exists:

```
sampled = fnv1a32(visitor_id) / 2^32 < sample_rate
```

Sampling is decided **once per visitor**. All clients must implement the same function, which is why it is
specified here: FNV-1a over the ASCII bytes of the id, with `fedcba9876543210fedcba9876543210` hashing to `777805509`.
Without a visitor id, the rule stays once per session.

Lowering a key's rate mid-cohort still drops some visitors from that moment; the retention response reports the
rate changing in its range (§6) rather than hiding it.

## 3. Attribution

Sent on the **first page view of a session only**, by the browser SDK:

| Attribute | Value |
|---|---|
| `openlog.rum.referrer.host` | Host of `document.referrer`, empty for direct or same-site. **Host only**: paths carry personal data |
| `openlog.rum.utm.source`, `.medium`, `.campaign` | From the landing URL, up to 128 bytes each |

These are the only query parameters the SDK ever reads. `url.full` still drops the query string (rum.md §2.4).
`utm_term` and `utm_content` are not collected: they are the two that free text ends up in.

Mobile v1 sends none of these. Install attribution depends on the stores and is a separate design.

## 4. Sanitize rules

All in `internal/rum.Sanitize`, following rum.md §3.3:

| Rule | Why |
|---|---|
| `openlog.analytics.mode` is **forced** from the key on every span | The views of §5 filter on it, so a page cannot opt itself in |
| Visitor id handled as in §1.2 | Identity is the key's decision |
| Referrer and UTM accepted on `page_view` only, bounded | Not a free attribute store |
| **At most 500 distinct event names per key; beyond that the name becomes `(other)`** | See below |

**The event name cap is new, and required.** `rum.md` §2.5 could leave names unbounded because they had no
rollup. They now reach a 90-day table from a public key, so a copied key could otherwise mint names without limit
("a page cannot mint rollup keys", §3.3). It applies to keys with analytics on. The set is held per ingest pod, like
the rate limit, so the ceiling with *p* pods is at most *p* × 500, stated rather than hidden. An `(other)` count above
zero is shown in the UI.

## 5. Storage

ClickHouse `0102_analytics.sql`, PostgreSQL `0104_analytics_mode.sql`. All three tables are written by materialized
views on `spans_local`, filtered on the forced mode attribute, so a key in `off` mode costs nothing.

**The views must be created new.** `rum_sessions` cannot be altered (rum.md §3.7), and nothing here touches it.

### 5.1 `analytics_events` — 90 days

One row per `page_view` or `custom` span, with its weight:

`tenant_id, app, deployment_environment, timestamp, kind, name, route, session_id, actor_hash, weight, version,
country, device_type, os_name, browser_name, referrer_host, utm_source, utm_medium, utm_campaign, params`

`name` is the event name, or the route for a page view. `params` is `Map(LowCardinality(String), String)` holding
the existing `openlog.rum.custom.param.*`. Partitioned by day, `ORDER BY (tenant_id, app, deployment_environment,
timestamp, name)`, with a bloom filter on `actor_hash`.

### 5.2 `analytics_actors` — 13 months after last seen

One row per actor: `first_seen` (min), `last_seen` (max), `first_version`, `first_country`, `first_referrer_host`,
`first_utm_source`. This is what makes "new" mean new: `first_seen` must survive the 90 days of §5.1, or anyone
returning after three months would count as a new user.

### 5.3 `analytics_actor_days` — 13 months

`tenant_id, app, deployment_environment, day, actor_hash, weight (max), events (sum)`. Retention reads this and
never the events, so a 12-month cohort chart does not scan 12 months of events.

### 5.4 `analytics_device_models_1d` — 90 days

`tenant_id, app, deployment_environment, day, model, sessions, actors, weight`, from the resource's `device.model.identifier`, so mobile SDKs only.
§5.1 has no model, and its view cannot be altered. `sessions` and `actors` are `uniqCombined64` sketches and hold no personal data, so erasure
(§7) has nothing to find here. Past a few thousand they are estimates.

### 5.5 Retention class

A new storage class `analytics` in `internal/migrate.TTLTables` with **fixed** retention, for the reason `rum` has
its own class (rum.md §5): borrowing another would let a per-tenant setting silently widen how long personal data
is kept. `OPENLOG_STORAGE_COLD_AFTER_DAYS_ANALYTICS` defaults to 30.

Rows on one actor are spread over shards, because the views write locally and spans are sharded by trace.
Reads therefore re-aggregate through the Distributed table (apm.md §8). Retention needs no join between tables: an
actor's cohort is its first row in §5.3, which holds the same 13 months.

**Measured at 2 million actors and 10 million actor-days:** retention answers in 0.16 s. The activity read first spread
each actor-day over 30 days, which was exact and ran out of memory, so it now merges one `uniqCombined64` sketch per day.
DAU, WAU and MAU past a few thousand people are therefore **estimates, within about 1%**, and exact below that.

## 6. API

Base `/api/v1/analytics`, tenant-scoped, viewer role, API keys allowed. All take `app=&environment=&from=&to=`.
OQL gains the event type `AnalyticsEvent`, which makes every question below alertable with the `oql` rule type.

| Endpoint | Content | Needs |
|---|---|---|
| `GET /overview` | sessions, events, users, new users, series | users: `visitors` |
| `GET /events` | event names with count, users and trend; `?name=` adds a breakdown by one param | |
| `GET /acquisition` | sessions and users by referrer host, UTM source, medium, campaign | |
| `GET /audience?by=country\|device\|model\|os\|browser\|version` | sessions and users by what they ran on and where; a session counts once, under its first event | |
| `GET /retention?grain=day\|week\|month` | cohorts by `first_seen`, percentage returning per period | `visitors` |
| `GET /funnel?step=&step=&window=&by=session\|user` | as rum.md §2.8, over §5.1, up to 90 days | `by=user`: `visitors` |
| `GET /activity` | DAU, WAU, MAU, new users and stickiness per day | `visitors` |

An endpoint that needs `visitors` answers `collecting: false` on an application with no actors in the range, so the UI
can say what to turn on: an empty retention chart reads as "nobody came back". It is decided from the data, because a
read here depends on ClickHouse alone. Retention includes `sampling_changed` when the key's rate changed in the range.

**Status:** all of these are built.

`/rum/funnel` stays as it is. The UI adds an **Analytics** section per application; it does not replace the RUM tabs.

## 7. Erasure

`POST /api/v1/analytics/erase` with `{"app", "visitor_id" | "user_id"}`, admin role (API keys allowed, since the caller is
usually a backend), written to the audit log without the id. It answers `202 {"id", "status": "pending", …}`, and
`GET /api/v1/analytics/erase/{id}` reports the status:

1. The request is queued in PostgreSQL, because the API reads ClickHouse as a read-only user.
2. A leader task hashes the id as in §1.3 and deletes from the §5 tables, by `ALTER TABLE … DELETE` scoped to that app. The
   statement carries only the hash, since its text stays in `system.mutations` and the query log.
3. The queued id is cleared as the request completes.
4. The raw id on `spans` expires with the 7-day trace retention, and this is stated in the response.

Deletions are mutations, so they are **asynchronous**: `submitted` means handed to ClickHouse, and the response says so. Data subject requests for an
application's visitors are a new case for [saas.md](../operations/saas.md) §12, which today covers accounts and
organizations, and that document gains a section.

**A limit that matters:** `setAnalyticsCollection(false)` stops collection but erases nothing, because a client cannot
be trusted with a delete on a public key. The application's backend has to call the endpoint above.

## 8. Changes to existing contracts

- **rum.md §1.1:** points to §1 and §2 here.
- **rum.md §2.5:** "there is no rollup" stays true of `off`, and so does the absence of a name cap: a key in `off`
  mode behaves as it did before this feature existed.
- **rum.md §2.8:** the paragraph saying retention is not here is replaced with a link to this document.
- **rum.md §3.3 and semantic-conventions.md §10:** the new attributes.
- **mobile-agent.md §3.1:** the visitor id. **§4:** visitor-level sampling.

## 9. Open questions

1. **Is `first_seen` backfilled?** Proposed: no. Everyone is a new user on the day a key is switched on, and the UI
   marks that date, because the spans available for a backfill reach back only 7 days anyway.

## 10. Decided

- **Billing.** The §5 rows are free: they are a copy of data already counted as spans, and are not a metered signal.
- **Retention.** 90 days for §5.1 and 13 months for §5.2 and §5.3, fixed as §5.4 describes.
- **Identity.** On by default for new keys and new SDKs, off on existing keys until the operator turns it on (§1.1).
