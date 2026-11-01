# Product analytics

See how many people use your application, whether they come back, what they do and where they came from: DAU, WAU, MAU,
new users, retention, funnels, events and traffic sources. If you have used Google Analytics or Firebase Analytics, this
covers the same ground, on your own server.

It runs on top of the [browser](/integrations/browser), [iOS](/integrations/ios), [Android](/integrations/android) and
[Flutter](/integrations/flutter) and [Unity](/integrations/unity) SDKs, so there is nothing else to install.

Results appear per application, in the **Analytics** tab under **Browser**.

::: tip Which endpoint?
These examples use the hosted service, `https://apm.resoft.org`. If you run openlog yourself, use your own server instead.
:::

## 1. Check the key

Open **Settings → Browser keys**. Each key has an **Analytics** setting:

| Mode | What it stores | What you get |
|---|---|---|
| **Visitors** | A persistent, random id per device | Everything on this page |
| **Sessions** | Events and attribution, nobody recognised across visits | Events, sources and session funnels. No DAU or retention |
| **Off** | Nothing beyond normal monitoring | None of this |

New keys start in **Visitors**. **A key created before this feature stays Off until you change it**, so upgrading openlog
never starts collecting by itself.

## 2. Start the SDK

### Browser

Nothing else is needed:

```js
import { init } from 'openlog-browser';

const openlog = init({ key: 'olb_…', endpoint: 'https://apm.resoft.org' });
```

### iOS

Nothing else is needed. The id is kept in `UserDefaults`.

```swift
let openlog = try OpenLog.start(Options(
    key: "olb_…",
    endpoint: "https://apm.resoft.org",
    appID: Bundle.main.bundleIdentifier ?? ""
))
```

### Android

Give the SDK a private directory to keep the id in:

```kotlin
val openlog = OpenLog.start(
    Options(
        key = "olb_…",
        endpoint = "https://apm.resoft.org",
        appId = BuildConfig.APPLICATION_ID,
        visitorDirectory = context.filesDir.path,
    )
)
```

### Flutter

Give the SDK a private directory, for example from the `path_provider` package:

```dart
final dir = await getApplicationSupportDirectory();

final openlog = Openlog.init(OpenlogOptions(
  key: 'olb_…',
  endpoint: 'https://apm.resoft.org',
  appId: 'com.example.shop',
  visitorDirectory: dir.path,
));
```

::: warning Android and Flutter need `visitorDirectory`
Without it the SDK sends no visitor id, and DAU, MAU and retention stay empty. It does not fall back to a new id on each
launch, because that would count every launch as a new person.
:::

## 3. Record what matters

Page views and screens are recorded for you on the web. On mobile, call `recordScreen` as people navigate. Name the
actions you care about:

```js
openlog.recordEvent('checkout_started', { plan: 'pro' });
```

Use a small, fixed set of names, and put what varies into parameters. Each key stores at most 500 distinct event names on
each ingest server, and anything beyond that is counted as `(other)`.

## 4. Decide about consent

::: danger This is your decision to make
In **Visitors** mode the SDK stores a persistent id on the visitor's device and recognises them across visits. Depending
on where you and your visitors are, that may need their consent, under the GDPR, KVKK or the ePrivacy rules. openlog cannot
make this call for you. Take advice if you are unsure.
:::

If you need to ask first, start with collection off and turn it on when they agree:

```js
const openlog = init({
  key: 'olb_…',
  endpoint: 'https://apm.resoft.org',
  analyticsCollection: false,
});

// In your consent dialog:
openlog.setAnalyticsCollection(true);
```

The same option and call exist on iOS, Android and Flutter.

- Turning collection off **deletes the id from the device and is remembered**, so a reload does not give the person a new
  one.
- Turning it off stops collection. **It does not delete what was already sent.**
- `identify()` is separate from all of this. Send an opaque account id, never an email address or a name.

### Deleting a person's data

To answer an erasure request, call this from your backend with an admin API key. Get the id from `visitorId()` in the SDK,
or use your own account id if you call `identify()`:

```sh
curl -X POST https://openlog.example.com/api/v1/analytics/erase \
  -H "Authorization: Bearer <api key>" -H "Content-Type: application/json" \
  -d '{"app": "shop-web", "visitor_id": "fedcba9876543210fedcba9876543210"}'
```

It answers `202` with an id, and `GET /api/v1/analytics/erase/{id}` reports the status. Deletion runs in the background
shortly after. The raw id on spans is not deleted by this: it expires with the 7-day trace retention.

## Compare with the previous period

The **Analytics** tab has a period of 7, 28 or 90 days, and every total and every row of its lists shows how far it moved from the same number of days before that, as a count and a percentage: `+200 (+20.0%)`.

- **Complete days, ending yesterday, in UTC.** Today has only just started in the morning, so counting it would show every number falling, every morning.
- **`(new)`** means the previous number was zero, and a rise from nothing has no percentage. `–` means the previous number can't be known: with 90 days, the 90 before that are past how long events are kept, though people are still compared.
- **A row with `0` now and a fall** is something that stopped altogether.
- If the app started reporting during the previous period, the tab says so, because the rise may only mean that.

The same numbers are on the API, under `GET /api/v1/analytics/compare?app=&days=`.

## See your audience

The **Audience** table on the **Analytics** tab shows sessions and people by country, device type, device model, operating system, browser and
app version. Device type, OS, browser and version need no setup, and the device model comes from the mobile SDKs.

**Country needs one setting on your server,** because openlog ships no location data. If a CDN such as Cloudflare is in front of it, name the header it writes:

```sh
OPENLOG_RUM_GEO_HEADER=CF-IPCountry
```

Otherwise, give it a country database. [DB-IP's free country database](https://db-ip.com/db/download/ip-to-country-lite) works and asks for a link back to DB-IP
wherever you show the data. GeoLite2-Country works as well.

```sh
# In your .env. The Compose file keeps this folder in the volume `geoip`.
OPENLOG_RUM_GEOIP_DB=/var/lib/openlog/geoip/country.mmdb
# If nginx or a load balancer is in front of ingest, or every visitor looks like it:
OPENLOG_RUM_TRUSTED_PROXIES=10.0.0.0/8
```

Restart once after adding the setting. After that, replace the file whenever a new one comes out, with no restart. **Only the country is kept.** The address is used while the request is handled and isn't stored,
logged or put on a span. It is still personal data for that moment, so mention it in your privacy policy if yours lists such things. Countries start appearing from
the moment you set this, and earlier sessions stay unknown.

## How the numbers work

- **A user is a device.** Someone on a phone and a laptop counts twice. Accounts are not merged across devices.
- **WAU and MAU are rolling.** They count the people active in the 7 and 30 days ending on that date.
- **Large counts are estimates.** Past a few thousand people, DAU, WAU and MAU are within about 1%. Below that they are exact.
- **Stickiness** is DAU divided by MAU.
- **Retention groups people by when they were first seen,** and shows the share active in each later day, week or month.
- **Everyone is new on the first day.** Nothing is backfilled, so a key's first days show only new users.
- **Sampling is by person, not by session,** so a person stays in or out of the sample. Counts are extrapolated from the
  sample rate. If you change the rate, retention says so, because part of any drop is then people leaving the sample.

## How long data is kept

| Data | Kept for |
|---|---|
| Events, page views and parameters | 90 days |
| First-seen dates and daily activity per person | 13 months |
| Raw visitor and user ids, on spans | 7 days |

These periods are fixed. The analytics tables hold a hash of the id rather than the id itself. That is pseudonymous data,
which still counts as personal data.

## Use the API

| Endpoint | Answers |
|---|---|
| `GET /api/v1/analytics/activity` | DAU, WAU, MAU, new users and stickiness per day |
| `GET /api/v1/analytics/retention` | Cohorts by day, week or month |
| `GET /api/v1/analytics/overview` | Page views, events and sessions per day |
| `GET /api/v1/analytics/events` | Event names, or one parameter's values |
| `GET /api/v1/analytics/acquisition` | Sessions by referrer or UTM value |
| `GET /api/v1/analytics/funnel` | Ordered steps, by session or by person |

[The API reference](/contracts/api) has the details, and [the contract](/contracts/analytics) has the design and its
reasoning.

## Not covered

- Push notifications, remote configuration and A/B testing
- Session replay
- Region or city. Only the country is resolved
- Device names: the model appears as the device reports it, such as `iPhone15,2`
- Install attribution on mobile. UTM and referrer are web only
