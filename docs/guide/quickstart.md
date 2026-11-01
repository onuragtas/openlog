# Quickstart

This takes you from nothing to data on screen: a server running openlog, one host reporting to it, and an application
sending traces. It uses the `single` profile, where everything runs on one machine under Docker Compose. For Kubernetes,
see [the Helm chart](../../deploy/helm/openlog/README.md).

## 1. Install the server

You need a Linux server with Docker Engine and the Compose v2 plugin. Add `--install-docker` if it has no Docker yet.

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install-server.sh |
  sudo sh -s -- --email you@example.com
```

When it finishes, the installer prints the address of the UI, your **license key** (`olk_…`), and the command for
installing an agent. Keep the license key: agents use it to send data.

Open `http://<server>:8080` and sign in with the email you gave. If you did not set a password, the installer generated
one and printed it once.

::: tip Everything else is optional
[The README](../../README.md) lists every installer flag, including a public domain, a release channel and manual
installation.
:::

## 2. Open the ports

| Port | What it is | Who needs it |
|---|---|---|
| `8080` | The UI and the API | Your users |
| `4318` | OTLP/HTTP, where agents send data | Monitored hosts and applications |
| `4317` | OTLP/gRPC | Monitored hosts and applications that use gRPC |
| `9464` | Prometheus metrics and health checks | Keep this one internal |

For anything beyond a test, put a reverse proxy such as Caddy, nginx or Traefik in front of `8080` and `4318`, set
`OPENLOG_COOKIE_SECURE=true`, and give agents `https://` endpoints.

## 3. Monitor a host

Run this on the machine you want to monitor:

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
  sudo sh -s -- --license-key <your olk_ key> --endpoint http://<server>:4318
```

On the openlog server itself, use `http://127.0.0.1:4318` as the endpoint.

The host appears under **Hosts** within about a minute, with its metrics, inventory and discovered services.

## 4. Trace an application

Pick your language from [the integrations](./send-data.md). As an example, the Node.js agent needs no code changes:

```sh
npm install openlog-node
```

Then follow [its quick start](/integrations/node#quick-start) to start your application with the agent.

If your application already uses OpenTelemetry, you do not need an openlog agent. Point it at the server instead:

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://<server>:4318
export OTEL_EXPORTER_OTLP_HEADERS=openlog-license-key=<your olk_ key>
```

Once your application receives traffic, it appears under **APM** as a service.

## Let the UI do it

Open **Add data** from the top bar, or go to `http://<server>:8080/add-data`. It builds the commands for hosts, Docker,
Kubernetes, every APM agent, logs and OpenTelemetry with your server's address already filled in. It can create a license
key for the install, and it waits until the data arrives.

## Which key goes where

There are two kinds of key, and mixing them up is the most common mistake.

| | License key `olk_…` | Browser or mobile key `olb_…` |
|---|---|---|
| **Used by** | Infra agent, APM agents, OpenTelemetry | Browser and mobile SDKs |
| **Secret?** | **Yes.** Never put it in a web page or an app | No. It ships in your page or app, and is limited by design |
| **Create it under** | Add data, or Settings | Settings → Browser keys |

## Next

- [Monitor a website or a mobile app](/integrations/browser)
- [Measure DAU, retention and funnels](./product-analytics.md)
- [See what the product does, screen by screen](/features)
- [Keep openlog up to date](/operations/upgrading)
