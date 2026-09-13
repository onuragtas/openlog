# openlog PHP agent — M2 spike (D-034)

Status: **spike / decision material, not a product.** License: Apache-2.0 (see [LICENSE](LICENSE)).

The decision document (Turkish, for the product owner) is [docs/decision.md](docs/decision.md).
Measured results are in [spike/results/](spike/results/) (`summary.md`, `verify.txt`, `crash-test.txt`, raw k6 JSON).

## Layout

| Path | What |
|---|---|
| `docs/decision.md` | Options A/B, measurements, recommendation, open questions |
| `prototype-a/ext/` | Option A: C extension on the PHP 8 Observer API (`openlog.so`): request span, Laravel route / Symfony route name, PDO + phpredis client spans, exceptions and fatal errors, W3C `traceparent` in; one non-blocking unix datagram per request, fail-open |
| `prototype-a/forwarder/` | Option A: Go forwarder — unix datagram socket → batched OTLP/HTTP protobuf to openlog |
| `prototype-b/bundle/` | Option B: OpenTelemetry PHP SDK + contrib auto-instrumentation (Laravel, Symfony, PDO) bundled with Composer and prefixed with php-scoper (`OpenlogVendor\`); `Openlog\Php\Bootstrap` configures the SDK from `OPENLOG_*` env vars |
| `prototype-b/prepend.php` | Option B entry point, loaded with `auto_prepend_file` (no change to the application's composer.json) |
| `apps/laravel/overlay` | Files copied over a fresh `laravel/laravel:^12` project (Laravel 11 cannot be installed: every 11.x release is blocked by Packagist security advisories) |
| `apps/symfony/app` | Minimal Symfony 7.4 app on `HttpKernel` + `RouterListener` without FrameworkBundle (the full skeleton's `symfony/cache`/`yaml`/`runtime` are advisory-blocked) |
| `spike/app.Dockerfile` | One PHP 8.3-FPM image with both apps and both agents installed; the variant is chosen at runtime with `PHP_INI_SCAN_DIR=:/opt/variants/{base,a,b}` |
| `spike/docker-compose.spike.yml` | MariaDB, Redis, 3 × (php-fpm + nginx) variants, forwarder, k6. Standalone (project `openlog-phpspike`), **no openlog backend of its own**: telemetry goes to the shared local openlog at `http://host.docker.internal:4318` |
| `bench/` | `verify.sh` (traces via `/api/v1/traces/{id}` + `/api/v1/apm/services`), `run.sh` (benchmark), `mem.sh` (per-worker memory), `crash-test.sh` (failure tests), `summarize.php` |

Service names in the shared openlog UI (APM): `php-laravel-proto-a`, `php-symfony-proto-a` (option A),
`php-laravel-proto-b`, `php-symfony-proto-b` (option B). `php-laravel-base` / `php-symfony-base` are the
uninstrumented baseline and send nothing.

Host ports (127.0.0.1): Laravel base/A/B `28801/28802/28803`, Symfony base/A/B `28811/28812/28813`.

## Rerun everything

Prerequisites: Docker with Compose v2; the **shared local openlog** already running (`deploy/compose`, project
`openlog`; `curl localhost:9464/readyz` all ok). The spike never starts, stops or changes it. Network access for
Composer/PECL during the image build.

Settings (environment, defaults in brackets): `SPIKE_OTLP_ENDPOINT` / `SPIKE_B_ENDPOINT`
[`http://host.docker.internal:4318`], `SPIKE_LICENSE_KEY` [`dev-license-key`, use `OPENLOG_BOOTSTRAP_LICENSE_KEY`
from `deploy/compose/.env`], `OPENLOG_API` [`http://127.0.0.1:8080`], `OPENLOG_EMAIL` / `OPENLOG_PASSWORD`
[bootstrap owner] for reading traces back.

**Project name safety:** every compose command in this spike passes `-p openlog-phpspike` explicitly (`Makefile`,
`bench/lib.sh`). Never use `deploy/compose/docker-compose.yml` from here: it declares `name: openlog`.

```sh
make -C agents/php spike        # images → up → verify → bench → mem → crash-test → down (≈ 25 min)
```

Individual steps: `images`, `up`, `verify`, `bench` (`RUNS=3 DURATION=30s VUS=16`), `mem`, `crash-test`, `down`,
`clean` (also removes the two spike images). Benchmarks send real telemetry to the shared openlog.

Benchmark method: Laravel `GET /bench/{id}` (one Eloquent query on MariaDB + two phpredis calls), PHP-FPM
`pm=static` 8 workers, container limited to 4 CPUs, opcache on, k6 closed loop with 16 VUs, 10 s warm-up + 30 s
measurement after an FPM restart, 3 runs per variant with rotated order. CPU = cgroup `usage_usec` delta of the
PHP container per request; memory = average RSS/PSS of the Laravel pool's workers after the run.
All services share one laptop VM (OrbStack, arm64), so absolute numbers are only comparable within one run set.
