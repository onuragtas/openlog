# CI: ne nerede çalışır {#ci-what-runs-where}

Üç GitHub Actions workflow'u var. Bir commit'in release edilip edilmeyeceğine yalnızca `ci.yml` karar verir.

| Workflow | Tetikleyici | Release'i engeller mi | İçerik |
|---|---|---|---|
| `.github/workflows/ci.yml` | master'a push, pull request'ler | evet (`release-tag` her job'ı bekler) | Her modülün Go vet/test'i (kök, `libs/release`, `agents/infra`, `terraform`), ajanlar (infra macOS/Windows, Go, PHP, Node, Python, Java, .NET), web typecheck/lint/Vitest/build, Helm, shellcheck/actionlint, release dry run (paketler, `install.sh`). `make e2e` master push'larında `continue-on-error` ile çalışır |
| `.github/workflows/long-tests.yml` | master'a push (yalnızca doküman değişikliği değilse), her gece 02:17 UTC, elle | hayır | aşağıdaki uzun Docker test takımları |
| `.github/workflows/release.yml` | yeni bir tag için `release-tag` tarafından veya elle başlatılır | — | release'i derler, imzalar ve yayınlar ([releasing.md](/tr/operations/releasing)) |

## long-tests.yml {#long-tests-yml}

Job'lar `ubuntu-24.04` üzerinde paralel çalışır. Daha yeni bir push, çalışan push run'ını iptal eder; zamanlanmış ve elle
başlatılan run'lar iptal edilmez.

| Job | Çalıştırdığı | Gereken | Yaklaşık süre |
|---|---|---|---|
| `go-integration` | `test/integration/run.sh shared`, `self`, `renderer` | Docker; aşağıya bak | 25–40 dk |
| `web-playwright` | `npx playwright test` (`web/playwright.config.ts`: MSW mock'lu Vite dev sunucusu, backend yok) | Node 22, `npx playwright install` ile gelen Chromium (`package-lock.json` içindeki `@playwright/test` sürümü) | 5–10 dk |
| `updater-acceptance` | `make updater-acceptance` (`test/autoupdate`: 0.9.0 → 0.9.1, bozuk 0.9.2 geri alınır, updater kendini günceller) | Docker; imajları, yerel registry'yi ve geçici imza anahtarını test, checkout edilen commit'ten kendisi üretir | 25–35 dk |
| `install-server-test` | `sh packaging/test/install-server.sh` (docker:dind: 0.1.6 kurulur, yeniden çalıştırılır, en son stable release'e yükseltilir, sürüm düşürme reddedilir) | GitHub release'lerine ve ghcr.io'ya internet erişimi | 15–25 dk |
| `tls-e2e` | `make tlstest` (compose `openlog-tlstest`; sertifikalar test tarafından `/tmp` altında üretilir) | Docker; `openlog:tlstest` imajını derler | 15–25 dk |
| `tiered-e2e` | `make tieredtest` (compose `openlog-tiered`: MinIO + 2×2 ClickHouse) | Docker | 15–20 dk |
| `sso-e2e` | `make ssotest` (compose `openlog-ssotest`: PostgreSQL + `test/integration/sso/realm-openlog.json` ile Keycloak; `host.docker.internal` üzerinden back-channel logout) | Docker | 5–10 dk |
| `report` | job sonuçlarının özet tablosu; zamanlanmış run'larda aşağıdaki issue | — | < 1 dk |

Her job compose projelerini `if: always()` adımında `down -v` ile kaldırır ve hata durumunda container loglarını yazdırır.
Playwright başarısız olursa `playwright-report/` ve `test-results/` (trace'ler) klasörlerini `playwright-report` artifact'i
olarak yükler.

### Entegrasyon paketleri {#integration-packages}

`test/integration/run.sh` (`make integration`, tek bir bölüm için `INTTEST_PHASE=shared|self|renderer`):

| Aşama | Paketler | Bağımlılıklar |
|---|---|---|
| `shared` | `internal/deletion`, `dataexport`, `usage`, `quota`, `oql`, `api`, `dashboard`, `savedview`, `operator`, `updatereq` | `internal/usage/testdata/docker-compose.yml` dosyasından `openlog-inttest` compose projesi: PostgreSQL 16 (`127.0.0.1:27441`, paket başına bir veritabanı) ve `clickhouse` hostname'li, `deploy/compose/clickhouse/openlog-cluster.xml` kullanan ClickHouse 25.8 (`127.0.0.1:19010`, `ON CLUSTER` DDL için). Script `OPENLOG_TEST_POSTGRES_DSN` ve `OPENLOG_TEST_CLICKHOUSE_ADDR` değişkenlerini ayarlar |
| `self` | `internal/store/postgres`, `alert`, `fleet`, `processor`, `apm` | her test kendi projesini başlatır ve kaldırır (`test/integration/*` altından `openlog-pgtest`, `-alerttest`, `-fleettest`, `testdata/shardtest` altından `openlog-shardtest` / `openlog-apmshard`) |
| `renderer` | `internal/renderer/chrome` | `openlog-renderer:inttest` imajı (`docker build --target renderer`) |

Paketler teker teker çalışır. `INTTEST_KEEP=1`, `openlog-inttest` projesini ayakta tutar.

## Hata bildirimi {#failure-reporting}

- Push ve elle başlatılan run'lar: başarısız job'lar run'da ve `report` özetinde kırmızı görünür, başka bir şey olmaz.
- Zamanlanmış run'lar: `report`, run bağlantısı ve başarısız job'larla birlikte **long-tests failing** issue'sunu açar (veya
  açık olana yorum yazar) ve sonraki bir zamanlanmış run geçince issue'yu kapatır.
- long-tests hatası `release-tag`'i asla engellemez. Master'da düzelt, sonraki push veya gece run'ı yeniden kontrol eder.

## Yerelde çalıştırmak {#running-locally}

Compose v2.17+ ile Docker, Go (`go.mod` sürümü) ve web test takımı için Node ≥ 20.19 gerekir.

```sh
make integration                 # veya INTTEST_PHASE=shared make integration
make web-e2e                     # MSW mock'larıyla Playwright
make updater-acceptance
sh packaging/test/install-server.sh [VERSION]
make tlstest
make tieredtest
make ssotest                     # 18080 doluysa SSOTEST_KEYCLOAK_PORT=27480 (e2e stack'i)
make e2e                         # E2E_UI=1, stack'e karşı Playwright aşamasını ekler (e2e.md)
```

e2e, TLS ve tiered testlerinin neyi doğruladığı: [e2e.md](/tr/operations/e2e).
