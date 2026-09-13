# 07 — Ekipler

| Ekip | Sorumluluk | Sahip olduğu yerler |
|---|---|---|
| **Infra Agent** | Metrik toplayıcılar, envanter, keşif motoru ve katalog, entegrasyonlar, paketleme | `agents/infra` |
| **Ingest & Pipeline** | OTLP gateway, auth, rate limit, Kafka, processor, cardinality kontrolü | `cmd/openlog-ingest`, `cmd/openlog-processor`, `internal/ingest`, `internal/processor`, `internal/queue` |
| **Storage & Query** | ClickHouse şeması ve migration'lar, sorgu katmanı, sorgu dili, Query API | `schema/`, `cmd/openlog-migrate`, `cmd/openlog-api`, `internal/store`, `internal/api` |
| **Frontend** | Tasarım sistemi, host/APM/log/alarm ekranları, dashboard'lar | `web/` (M1) |
| **Alerting** | Kural motoru, incident, bildirim kanalları | `cmd/openlog-alert` (M2) |
| **APM Agent ekipleri** | Dil başına ekip: Go, PHP, Node.js, Java, Python, .NET | `agents/<dil>` |
| **Platform / DevOps** | Compose ve Helm, SaaS altyapısı, CI, release, yük ve chaos testleri | `deploy/`, `cmd/openlog-loadgen`, CI |
| **Identity & Tenancy** | Tenant, kullanıcı, rol, SSO, kota, faturalama | `internal/tenant` (M1+) |

## Repo yapısı

Tek repo: **github.com/onuragtas/openlog**.

```
openlog/                     AGPL-3.0 — backend, UI, şema, deploy, belgeler
  cmd/openlog-*              servis binary'leri
  internal/                  backend paketleri
  schema/clickhouse/         ClickHouse şeması
  deploy/compose, deploy/helm/openlog
  docs/plan, docs/contracts, docs/operations
  web/                       UI (M1)
  agents/                    Apache-2.0 — her agent kendi Go modülü ve LICENSE dosyasıyla
    infra/                   github.com/onuragtas/openlog/agents/infra
    php/, node/, java/ …     (M2+)
```

## Çalışma kuralları

- **Sözleşme önce:** Ekipler arası her arayüz `docs/contracts` altında tanımlanır. Sözleşme değişikliği ilgili ekiplerin onayını gerektirir.
- **Karar günlüğü:** Mimari etkisi olan her karar [02-decisions.md](02-decisions.md)'ye eklenir.
- **Açık kaynak:** Yol haritası ve tasarım tartışmaları herkese açık; katkılar DCO (Developer Certificate of Origin) ile kabul edilir.
- **Tanım "bitti":** Test + belge + `single` ve `cluster` profilde çalıştığının doğrulanması.
