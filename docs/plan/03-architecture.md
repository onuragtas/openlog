# 03 — Mimari

## Veri akışı

```
 [Uygulama + APM agent / OTel SDK] ─┐
 [openlog-infra-agent]  ───────────────┼─ OTLP (HTTP :4318 / gRPC :4317)
                                    ▼
                           L7 Load Balancer (Envoy)
                                    ▼
                         openlog-ingest ×N  (stateless)
     license key (Postgres + pod içi TTL cache) · limit · decompress · JSON→protobuf
                                    ▼
                  Kafka  openlog.otlp.{metrics,logs,traces}
                                    ▼  consumer group
                        openlog-processor ×N (stateless)
       OTLP → satır · envanter/keşif yönlendirme · chunk · dedup token
                                    ▼  doğrudan shard *_local insert (D-018)
                ClickHouse cluster (shard × replica, Keeper ×3)
                                    ▲
 UI (React, API binary'sine gömülü) ──> openlog-api ×N ──> tenant filtresi zorunlu sorgu katmanı
                     │                  (oturum cookie + CSRF / API key)
               PostgreSQL (organizasyon, kullanıcı, rol, license/API key, oturum, audit; M2+: dashboard, alarm kuralı)
                     ▲
            openlog-alert ×N (lease tabanlı kural paylaşımı)                [M2+]
```

## Bileşenler

| Bileşen | Binary | Görev | State |
|---|---|---|---|
| Ingest | `openlog-ingest` | OTLP alır, license key → tenant çözer, limit uygular, Kafka'ya yazar | Yok |
| Processor | `openlog-processor` | Kafka'dan okur, OTLP'yi tablo satırlarına çevirir, ClickHouse'a batch yazar | Yok (offset Kafka'da) |
| Sampler (isteğe bağlı) | `openlog-sampler` | Tail-based sampling (D-075): ham traces topic'ini okur, trace başına tamponlar, tenant politikasıyla karar verip tutulan span'leri `traces.sampled` topic'ine yazar; açıkken processor bu topic'i okur, ingest trace id'ye göre anahtarlar | Karar süresi kadar bellek içi tampon (kaybında karar hemen verilir; offset Kafka'da) |
| API | `openlog-api` | UI ve dış kullanıcılar için sorgu API'si | Yok |
| Alert | `openlog-alert` | Kuralları periyodik değerlendirir, bildirim gönderir | Yok (lease Postgres'te) |
| Migrate | `openlog-migrate` | PostgreSQL, ardından ClickHouse şemasını uygular, Kafka topic'lerini oluşturur | Yok |
| Admin CLI | `openlog-admin` | `migrate`, `bootstrap`, `create-owner`, `reset-password` | Yok |
| All-in-one | `openlog-allinone` | `single` profil için ingest + processor + api tek süreçte | Yok |
| Load generator | `openlog-loadgen` | Gerçekçi OTLP yükü üretir | — |
| Infra agent | `openlog-infra-agent` | Host metrikleri, envanter, keşif, log | Disk buffer |

## Multi-tenancy

- Her telemetri satırında `tenant_id` vardır ve tüm tabloların `ORDER BY` anahtarının **ilk kolonudur**.
- Organizasyon = tenant. `tenant_id` organizasyon oluşturulurken atanır ve değişmez; ClickHouse anahtarıdır.
- Tenant bilgisi **yalnızca** ingest'te license key'den çözülür; agent'ın gönderdiği hiçbir attribute tenant belirleyemez. Key'ler PostgreSQL'de yalnızca hash olarak durur; ingest pod'u sonuçları kısa süre cache'ler (D-021).
- API'de tenant yalnızca kimliği doğrulanmış çağırandan (oturum veya API key) gelir ve sorgu katmanı tarafından zorunlu eklenir; handler'lar ham SQL'e tenant koyamaz/çıkaramaz. Birden fazla organizasyona üye kullanıcı `X-Openlog-Org-Id` header'ı ile seçer.
- Self-hosted kurulum tek tenant ile çalışır; ayrı kod yolu yoktur.

## Neden bu teknolojiler

- **OTLP:** Standart; tüm dillerde hazır SDK. Kendi agent'larımız da OTLP konuşur.
- **Kafka:** Ingest ile yazma arasındaki tampon; ClickHouse yavaşladığında veri kaybolmaz, processor'lar partition bazında ölçeklenir.
- **ClickHouse:** Kolon bazlı, yüksek sıkıştırma, trace/log/metrik için tek motor, sharding + replikasyon yerleşik.
- **Go:** Statik binary, düşük bellek, hem agent hem backend için aynı dil.

## Sürümleme ve uyumluluk

- Agent ↔ backend arasında sözleşme OTLP + [semantic-conventions](../contracts/semantic-conventions.md) belgesidir.
- ClickHouse şema değişiklikleri yalnızca **ileriye dönük uyumlu** migration'larla yapılır (kolon ekleme, yeni tablo).
- Kafka mesaj formatı değişirse `openlog-schema-version` header'ı artırılır; processor iki sürümü bir süre birlikte destekler.
