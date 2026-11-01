# Choose an integration

Start from what you want to see. Each guide below has its own quick start.

## What are you monitoring?

| You want to see | Use | Key |
|---|---|---|
| CPU, memory, disks, logs and services of a machine | [Infra agent](/integrations/infra) | License key |
| Why a process uses CPU, with no change to it | [Whole-host profiler](/integrations/ebpf), installed with the infra agent | License key |
| Slow requests and errors in a backend | An [APM agent](#backend-apm) | License key |
| Page speed, errors and sessions in a website | [Browser SDK](/integrations/browser) | Browser key |
| Screens, errors and events in a mobile app | [iOS](/integrations/ios), [Android](/integrations/android) or [Flutter](/integrations/flutter) | Mobile key |
| Crashes in a mobile app, by version | [Crash reporting](./crash-reporting.md), on top of the mobile SDK | Mobile key |
| Daily users, retention and funnels | [Product analytics](./product-analytics.md), on top of the browser or mobile SDK | Browser or mobile key |
| A Kubernetes cluster | [Kubernetes](/operations/kubernetes) | License key |

## Backend APM

| Language | Guide | Code changes | Install |
|---|---|---|---|
| Go | [Go agent](/integrations/go) | A few lines at start-up | `go get github.com/onuragtas/openlog/agents/go` |
| Node.js | [Node.js agent](/integrations/node) | None | `npm install openlog-node` |
| Python | [Python agent](/integrations/python) | None | `pip install openlog-agent` |
| Java | [Java agent](/integrations/java) | None, runs as `-javaagent` | Download the jar from a release |
| .NET | [.NET agent](/integrations/dotnet) | See the guide | `dotnet add package OpenLog.Agent` |
| PHP | [PHP extension](/integrations/php) | None, a C extension | See the guide |

### Already using OpenTelemetry?

Any OpenTelemetry SDK or Collector can send to openlog without an openlog agent:

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://<server>:4318
export OTEL_EXPORTER_OTLP_HEADERS=openlog-license-key=<your olk_ key>
```

The openlog agents add automatic framework instrumentation and host linking on top of this.
[Semantic conventions](/contracts/semantic-conventions) lists the attributes openlog understands.

## Connect the pieces

The integrations work together, and these are the links worth setting up:

- **Browser to backend.** The browser SDK adds a W3C `traceparent` header to same-origin requests, so one trace runs from
  the click to the database. Requests to another origin need `propagateTraceHeaders`, as
  [the browser guide](/integrations/browser#options) describes.
- **Application to host.** Running an APM agent on a host that also runs the infra agent links the service to the machine.
  Each agent's guide has a "Host linking" section.

## Not seeing data?

1. **Check the endpoint.** Agents send to port `4318` (`http://<server>:4318`), not the UI on `8080`.
2. **Check the key type.** An `olk_…` key belongs on servers, an `olb_…` key in pages and apps. See
   [which key goes where](./quickstart.md#which-key-goes-where).
3. **Browser: check the origin.** The page's origin must be on the key's allowlist, or requests are refused.
4. **Mobile: check the application id.** It must be on the key's allowlist. Requests otherwise answer `403` with
   `app_not_allowed`.
5. **Wait a minute.** Hosts and services take about that long to appear.
