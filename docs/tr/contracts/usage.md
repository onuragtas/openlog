# Sözleşme: Kullanım ölçümü, planlar, kotalar ve faturalama (v1) {#contract-usage-metering-plans-quotas-and-billing-v1}

Kararlar: D-079 (ölçüm), D-080 (planlar ve kotalar), D-081 (tenant başına retention). Operatör kılavuzu:
[docs/operations/saas.md](/tr/operations/saas). API: [api.md "Usage and plans"](/tr/contracts/api#usage-and-plans), yapılandırma:
[config.md "Usage, plans and billing"](/tr/contracts/config#usage-plans-and-billing), PostgreSQL tabloları:
[postgres.md "Usage, plans and billing"](/tr/contracts/postgres#usage-plans-and-billing-0035-usage-plans).

## 1. Modlar {#_1-modes}

| | Self-hosted (varsayılan) | SaaS (`OPENLOG_SAAS_MODE=true`) |
|---|---|---|
| Ölçüm (§2) | açık | açık |
| Planlar, değerlendirme, banner'lar, owner e-postaları (§4) | açık (katalog varsayılan olarak tek bir `unlimited` plan) | açık |
| Aylık kota üstünde ingest 429, ingest rate limit'leri (§4.4) | **kapalı** | açık (`hard_ingest_limit`, `ingest_bytes_per_second` olan planlar) |
| Tenant başına retention (§5) | kapalı (`OPENLOG_QUOTA_RETENTION_ENABLED` açabilir) | varsayılan olarak açık |
| Faturalama sağlayıcısı push/webhook'ları (§8) | `OPENLOG_BILLING_PROVIDER` | `OPENLOG_BILLING_PROVIDER` |
| Sert host sınırı (`limits.hosts` üstündeki yeni host'lar reddedilir, §4.6) | **kapalı** (yalnızca uyarı) | açık |
| Davetlerde, SSO JIT ve SCIM'de kullanıcı sınırı (§4.6) | **kapalı** (yalnızca uyarı) | açık |
| Askıya alma, deneme süreleri, kötüye kullanım işaretleri (§4.7) | **kapalı** (operatör konsolunun salt okunur görünümleri çalışır) | açık |

### 4.6 Sert host ve kullanıcı sınırları (D-105) {#_4-6-hard-host-and-user-limits-d-105}

SaaS modunda §4'ün hosts ve users metrikleri yalnızca uyarılmaz, uygulanır:

- **Host'lar**: pencere hosts metriğininkidir, yani bugün veya dün raporlamış host'lar (`usage_entities_1d`).
  Bu penceredeki host'lar sınırın üstünde de (plan düşürmeden sonra) raporlamaya devam eder. Henüz görülmemiş bir `host.id`, bilinen host'lar artı ingest
  pod'unun yeni kabul ettiği host'lar sınırın altındayken kabul edilir, aksi halde resource'ları reddedilir
  (`429 quota_exceeded` veya istek kabul edilmiş host'lar da taşıyorsa OTLP partial success; api.md "Ingest:
  suspended organization and host limit"). `host.id` olmayan veri (host'suz APM servisleri) sınırlanmaz. Bilinen host listesini api leader'ı her
  `OPENLOG_SAAS_HOST_SYNC_INTERVAL`'de yeniler; yenilemeler arasında birkaç ingest pod'u kalan yere kadar kabul edebilir, yani sınır bir aralık boyunca en fazla
  (pod − 1) × kalan yer kadar aşılabilir. PostgreSQL 15 dakikadan uzun süre erişilemezse §4.4'teki gibi fail open olur.
- **Kullanıcılar**: davet oluşturulurken üyeler (etkin kullanıcılar) artı bekleyen, süresi dolmamış davetler `limits.users`'ı aşamaz; daveti kabul etmek, SSO
  just-in-time provisioning ve SCIM için üyeler < sınır olmalı. Retler `403 quota_exceeded` olur.

### 4.7 Yaşam döngüsü (D-106) {#_4-7-lifecycle-d-106}

Planlar `trial_days` ve `trial_fallback_plan` tanımlayabilir (§4.1 katalog alanları; fallback varsayılan olarak katalog varsayılanıdır ve plandan farklı olmalı).
Deneme süreleri, askıya alma, destek erişimi ve kötüye kullanım işaretleri
[saas.md](/tr/operations/saas) §8–§11'de anlatılır.

SaaS modu `OPENLOG_AUTH_MODE=postgres` ister. Static auth modunda yalnızca kullanım okuma endpoint'leri çalışır (PostgreSQL'de plan yok, sorgu toplama dışında leader job yok).

## 2. Ölçüm (D-079) {#_2-metering-d-079}

Faturalanabilir her şey ingest tarafındaki sayaçlardan değil **saklanan satırlardan** türetilir, yani sayılar tam olarak tenant'ın sorgulayabildiği şeydir ve Kafka
yeniden teslimi ve insert yeniden denemeleri altında idempotent'tir.

ClickHouse şeması `0050_usage` (hepsi `ReplicatedMergeTree` ailesi, `TTL 400 DAY`, openlog-migrate'in TTL sahipliği tarafından yönetilmez):

| Tablo | Engine / anahtar | Dolduran | İçerik |
|---|---|---|---|
| `usage_signals_1h` | SummingMergeTree `(tenant_id, hour, signal, service_name, host_id)` | `spans_local`, `logs_local`, `metrics_local`, `profiles_local` üzerindeki MV'ler | `items` (span'ler, log kayıtları, metrik veri noktaları, profiling örnekleri), `bytes` (tahmini sıkıştırılmamış satır boyutu: değişken boyutlu sütunların `byteSize`'ı + sabit boyutlu sütunlar) |
| `usage_entities_1d` | ReplacingMergeTree `(tenant_id, day, kind, entity)` | aynı tablolar üzerindeki MV'ler | gün başına farklı `host` (host_id), `container` (metrik veri noktası veya resource attribute'larının küçük harfli `container.id` değeri), `service` (span `service_name`) başına bir satır |
| `usage_ingest_1h` | SummingMergeTree `(tenant_id, hour, signal)` | openlog-processor (`internal/processor/usage.go`) | `requests`, `bytes` = ingest'in aldığı saatte her Kafka kaydının sıkıştırılmamış OTLP protobuf byte'ları (ingest'in ürettiği payload; OTLP/JSON ingest'te protobuf'a çevrilir) |
| `usage_queries_1h` | ReplacingMergeTree(`collected_at`) `(tenant_id, hour, component)` | api leader job'u `usage-query-collection` | `system.query_log`'dan `queries`, `failed`, `read_rows`, `read_bytes`, `cpu_microseconds` (User+System süresi), `memory_bytes` (sorgu başına tepelerin toplamı) |

**Idempotency.**

- Processor bir chunk'ın her tablosunu `insert_deduplication_token = <topic>:<partition>:<first>-<last>:<table>`
  ve `deduplicate_blocks_in_dependent_materialized_views = 1` ile yazar (kafka.md "Consumption semantics"). Yeniden teslim edilen chunk aynı token'ları üretir,
  kaynak tablo bloğu atar ve MV'ler onu görmez. `usage_ingest_1h` satırları chunk başına toplanır ve aynı token şemasını kullanır. Zamana bağlı chunk kesimleri (idle, memory, shutdown) yeniden
  teslimde farklı token üretebilir; bunlar telemetri satırlarını da iki kez saklar, yani kullanım saklanan şeye eşit kalır.
- Sorgu hesaplaması her `15m`'de son `3h` için yeniden hesaplanır; en yeni `collected_at` kazanır, yani yeniden çalıştırmalar ve paralel toplayıcılar asla çift saymaz.
- Entegrasyon testi: `internal/usage/integration_test.go` chunk'lar yazar, aynı token'larla yeniden dener ve `usage_*`'ı ham tablolardaki `count()` / `uniqExact()` ile karşılaştırır.

**Sharding.** MV'ler her shard'a kısmi aggregate yazar; her okuma Distributed tablolar üzerinden `sum` / `uniqExact` ile yeniden toplar.
`usage_queries_1h` yalnızca `tenant_id` ile shard'lanır, yani `FINAL` shard başına çalışır.

**Tanımlar.**

| Metrik | Tanım |
|---|---|
| ingest byte'ları | dönemde `sum(usage_ingest_1h.bytes)`. Tail sampling ile (D-075) traces değeri örneklenmiş traces kaydıdır (saklanan). |
| sinyal başına item'lar | `sum(usage_signals_1h.items)` |
| saklanan byte'lar (tahmin) | sinyalin geçerli retention'ı içindeki saatlerin `sum(usage_signals_1h.bytes)` değeri; sıkıştırılmış tahmin bunu ham tablonun aktif part'larının `data_compressed_bytes / data_uncompressed_bytes` oranıyla çarpar |
| aktif host'lar | `max(uniqExact(host) of the day of "to", of the day before)`; `hosts` sınırının karşılaştırıldığı değer |
| aralıktaki host / container / servisler | aralığın günleri üzerinde `uniqExact(entity)` |
| kullanıcılar | değerlendirme anında organizasyonun etkin üyeleri (PostgreSQL) |
| sorgu hesaplaması | `log_comment.tenant_id` taşıyan başlangıç sorguları (`is_initial_query`); her api/alert sorgusu `{"component","tenant_id"}` taşır (D-047). Distributed alt sorgular başlangıç sorgusunun sayaçlarına dahildir. Kullanım endpoint'lerinin kendi sorgularının `log_comment`'i yoktur ve sayılmaz. |

## 3. Faturalama dönemi ve projeksiyon {#_3-billing-period-and-projection}

Faturalama dönemi bir UTC takvim ayıdır (`YYYY-MM`). Kümülatif metriklerin dönem sonu projeksiyonu
`used + recent_daily_average × remaining_days` olur; ortalama dönemin son 7 tam gününü kapsar; tam gün yoksa `used × period_length / elapsed` olur (bir saat
geçmeden olmaz). Geçmiş dönemler için projeksiyon yapılmaz.

## 4. Planlar ve kotalar (D-080) {#_4-plans-and-quotas-d-080}

### 4.1 Katalog {#_4-1-catalog}

Planlar yapılandırmadır (`OPENLOG_PLANS` satır içi JSON veya `OPENLOG_PLANS_FILE`), asla kod değil; fiyatlar faturalama sağlayıcısında yaşar. Bilinmeyen alanlar reddedilir.
`0` veya olmayan bir sınır sınırsız demektir; olmayan bir retention tablo varsayılanını korur.

```json
{
  "default": "free",
  "plans": [
    {
      "id": "free", "name": "Free", "description": "Evaluation",
      "limits": {
        "ingest_gb_month": 100, "hosts": 5, "users": 3,
        "retention_days": { "logs": 7, "traces": 7, "metrics": 30 },
        "query": { "max_memory_usage": 2147483648, "max_rows_to_read": 500000000 },
        "ingest_bytes_per_second": 2097152, "ingest_burst_bytes": 20971520
      },
      "enforcement": { "hard_ingest_limit": true, "grace_percent": 10 },
      "billing": { "plan_ref": "price_free" }
    },
    { "id": "pro", "name": "Pro", "limits": { "ingest_gb_month": 1000, "retention_days": { "logs": 30 } }, "billing": { "plan_ref": "price_pro" } },
    { "id": "enterprise", "name": "Enterprise", "limits": {} }
  ]
}
```

- `ingest_gb_month`, faturalama dönemi başına ingest byte'larının (§2) GiB'ıdır (2^30 byte).
- `billing`, sağlayıcı aboneliklerini planlara eşleyen `plan_ref` (§8) dışında openlog için opaktır.
- Katalog yoksa tek plan `unlimited`'dır. `OPENLOG_DEFAULT_PLAN` `default`'u ezer; ikisi de yoksa ilk plan varsayılandır. `org_plans` satırı olmayan bir organizasyon varsayılan plandadır.

### 4.2 Organizasyon başına ezmeler {#_4-2-per-organization-overrides}

`org_plans.overrides` (superadmin'ler ayarlar, §7) tek tek alanların yerine geçer: `ingest_gb_month`, `hosts`, `users`,
`retention_days.<signal>`, `query`, `ingest_bytes_per_second`, `ingest_burst_bytes`, `hard_ingest_limit`,
`grace_percent`. Bir retention ezmesi §5'in tablo retention'ını aşamaz (API reddeder).

### 4.3 Değerlendirme {#_4-3-evaluation}

api leader job'u `usage-quota-evaluation` (her `OPENLOG_USAGE_EVALUATION_INTERVAL`, varsayılan 1m) her organizasyonu değerlendirir ve `tenant_quota_status`'a upsert eder:

| Metrik | Kullanılan | Sınır |
|---|---|---|
| `ingest_bytes` | dönem ingest byte'ları | `ingest_gb_month × 2^30` |
| `hosts` | aktif host'lar | `hosts` |
| `users` | üyeler | `users` |

`percent = used / limit × 100`; ≥ %100'de düzey `exceeded`, 100'ün altındaki en düşük yapılandırılmış eşikte veya üstünde `warning`
(`OPENLOG_USAGE_NOTIFY_THRESHOLDS`, varsayılan `80,100`), aksi halde `ok`. Organizasyonun düzeyi en kötü metriğininkidir.
`ingest_blocked` yalnızca SaaS modunda, `hard_ingest_limit` olan planlar için,
`used ≥ limit + round(limit × grace_percent / 100)` olduğunda true olur.

**Yumuşak uygulama** (tüm modlar): arayüz banner'ı (`GET /api/v1/usage/status`), Settings → Usage & plan ve bir eşik aşıldığında organizasyonun owner'larına e-posta (§6).

### 4.4 Ingest'te sert uygulama (SaaS modu) {#_4-4-hard-enforcement-in-ingest-saas-mode}

Her ingest pod'u her `OPENLOG_QUOTA_REFRESH_INTERVAL`'de (30s) `tenant_quota_status` satırlarını (son bir saatte değerlendirilmiş, bloke veya rate limit'li) yükler ve kimliği doğrulanmış
her export isteğini decode ettikten sonra kontrol eder (boyut = sıkıştırılmamış protobuf byte'ları; OTLP/JSON: protobuf boyutu):

| Durum | HTTP | gRPC | Yeniden deneme ipucu | Gövde mesajı |
|---|---|---|---|---|
| aylık kota bitti (`ingest_blocked`) | `429` | `RESOURCE_EXHAUSTED` | `Retry-After` / `RetryInfo` = `OPENLOG_QUOTA_BLOCKED_RETRY_AFTER` (5m) | `monthly ingest quota of plan "free" is used up (110.00 of 100.00 GiB); upgrade the plan or wait for the next billing period` |
| rate limit | `429` | `RESOURCE_EXHAUSTED` | bucket bir isteği kabul edene kadarki süre (≥ 1 sn) | `ingest rate limit of plan "free" exceeded (2097152 bytes/s); retry after 3 s` |

Bu, D-014'ün tenant sınırı durumudur (`503`/`UNAVAILABLE` backend kesintileri için ayrılmış kalır). HTTP gövdesi isteğin encoding'inde bir `google.rpc.Status`'tur. Kimliği doğrulanmamış
istekler limiter'dan önce reddedilir.

**Yeni bağımlılık olmadan rate limit.** Her pod, `rate = ingest_bytes_per_second / pods` ve `burst = ingest_burst_bytes / pods` ile tenant başına bir token bucket çalıştırır (burst varsayılan olarak bir saniyelik rate).
`pods`, `component_heartbeats` içindeki canlı `openlog-ingest` ve `openlog-allinone` instance sayısıdır (son 90 sn'de görülmüş) veya ayarlıysa `OPENLOG_QUOTA_INGEST_PODS`. Bir istek, bucket herhangi bir token tuttuğu sürece kabul edilir
ve tüm boyutunu alır (bucket borca girebilir), yani burst'ten büyük istekler asla aç kalmaz ve uzun vadeli rate sınırlı kalır. Pod başına bölüşüm yaklaşıktır: dengesiz load balancing ile bir tenant yoğun pod'larda rate'inin altında
sınırlanır (paylaşılan bir Redis sayacına karşı belgelenmiş bir ödünleşim).

**Hata davranışı.** Ingest fail open olur: ilk başarılı yüklemeden önce veya PostgreSQL 15 dakikadan uzun süre erişilemediğinde her istek kabul edilir. ClickHouse kullanılamadığında değerlendirme durur (durum satırları bir saat sonra
eskir ve uygulanmayı bırakır).

### 4.5 Sorgu sınırları {#_4-5-query-limits}

Bir organizasyonun api sorguları ve alert değerlendirmelerinin ClickHouse `max_memory_usage`, `max_rows_to_read` ve `max_bytes_to_read` değerleri, ayar başına en düşükten en yükseğe dört katmandan çözülür:

| # | Katman | Ayarlayan | Değer semantiği |
|---|---|---|---|
| 1 | `OPENLOG_QUERY_MAX_*` varsayılanları | ortam | `0` = openlog ayarlamaz |
| 2 | plan `limits.query` (`org_plans.overrides.query` ile) | plan kataloğu / superadmin | yalnızca `> 0` değerler katman 1'in yerine geçer |
| 3 | organizasyon ayarı (`org_query_limits`, Settings → Usage & plan → Query limits, `PUT /api/v1/usage/query-limits`) | owner (self-hosted) veya superadmin (SaaS modu) | ayarlanmış değer (`0` dahil) katman 1–2'nin yerine geçer; ayarlanmamışsa miras alır |
| 4 | tenant'ın `OPENLOG_QUERY_TENANT_LIMITS` kaydı | ortam | üç ayarın hepsinin yerine geçer (operatörün son sözü) |

Katman 3'ü kim değiştirebilir: superadmin'ler her zaman; organizasyon owner'ları (session kullanıcıları) yalnızca `OPENLOG_SAAS_MODE` olmadan, çünkü SaaS modunda sınırlar operatörün sattığı planın parçasıdır (D-080). Her değişiklik
audit eylemi `query_limits.update` / `query_limits.delete` yazar. Her api ve alert pod'u katman 2–3'ü tüm tenant'lar için cache'ler ve her 30 sn'de yeniden yükler (`quota.QueryLimitsResolver`; değişikliği işleyen pod hemen yeniden yükler), yani sorgular PostgreSQL'i asla okumaz;
PostgreSQL erişilemezken son yüklenen katmanlar kullanımda kalır (ilk yüklemeden önce: katman 1 ve 4).

## 5. Tenant başına retention (D-081) {#_5-per-tenant-retention-d-081}

Değerlendirilen seçenekler:

1. **(Retention katmanı, gün) ile partition:** yeni partition anahtarı her ham tabloyu yeniden oluşturmak demektir; plan değişiklikleri tenant'ları katmanlar arasında yalnızca yeni veri için taşır.
2. **Tenant'a bağlı sütunla row TTL** (`TTL ts + toIntervalDay(retention_days)`): retention insert anında damgalanır (plan düşürme/yükseltme saklanan veriye uygulanmaz) ve günlük partition başına karışık tenant'larla part'lar yalnızca part'ları yeniden yazan TTL merge'leriyle dolar
   (`ttl_only_drop_parts` kullanılamaz).
3. **Tablo TTL'i = en uzun retention + günlük partition kapsamlı silmeler:** seçilen.

Mekanik:

- `OPENLOG_QUOTA_RETENTION_ENABLED` ile openlog-migrate, `logs`, `traces` (`spans_local`, `trace_index_local`) ve `metrics` (`metrics_local`) sınıflarının delete TTL'ini en uzun geçerli plan retention'ına ayarlar; retention'ı olmayan bir plan şema varsayılanını korur
  (logs 14, traces 7, metrics 30, profiles 7 gün). Bu, D-067'nin TTL sahipliğinden geçer (`migrate.TTLOptions.ClassRetentionDays`); tiered storage taşımaları etkilenmez.
- api leader job'u `usage-tenant-retention` (saatlik) tenant'ları sinyal başına geçerli retention `N`'ye göre gruplar (yalnızca tablo TTL'inden kısa `N`). Bir tablonun günlük partition'ı `D`, bir grup için `D + N + 1 days ≤ today` olduğunda
  ve tablo TTL'i onu düşürmediği sürece vadesi gelmiştir. Vadesi gelmiş ve o tenant'ların satırlarını içeren her (tablo, partition, grup) için
  `ALTER TABLE openlog.<table>_local ON CLUSTER '<cluster>' DELETE IN PARTITION ID '<YYYYMMDD>' WHERE tenant_id IN (…)` çalıştırır
  ve bunu `usage_retention_mutations` içine kaydeder (aynı tenant kümesi için 24 saat içinde yeniden gönderilmez), çalıştırma başına en fazla
  `OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS`, en eski partition önce.
- Kararlı durumda gün başına (tablo, farklı retention) başına bir günlük partition yeniden yazılır. Plan düşürme birkaç çalıştırmada yetişir.
- Aggregate'ler (`metrics_1m`, APM tabloları) global retention'larını korur. Partition'ı işlendikten sonra gelen, zaman damgası çok geçmişte olan satırlar tablo TTL'ine kadar kalır.

## 6. Bildirimler {#_6-notifications}

Değerlendirici, her organizasyon ve metrik için etkin owner'lara (faturalama dönemi, metrik, eşik) başına bir kez e-posta gönderir:
sahiplenme `usage_notifications` insert'idir, hiç e-posta gönderilemezse yeniden bırakılır. Daha yüksek eşik önce aşılırsa düşük olanlar sessizce sahiplenilir (%100 postasından sonra %80 postası gelmez). E-posta `OPENLOG_SMTP_*` kullanır;
SMTP olmadan yalnızca banner'lar görünür. Metin `<OPENLOG_PUBLIC_URL>/settings/usage` adresine link verir.

## 7. Superadmin'ler {#_7-superadmins}

`OPENLOG_SUPERADMIN_EMAILS`, SaaS operatörlerini listeler. Superadmin, listede doğrulanmış e-posta adresi olan bir session kullanıcısıdır; organizasyona üyelik gerekmez. Herhangi bir organizasyonun plan atamalarını
yalnızca superadmin'ler okuyabilir veya değiştirebilir (`GET/PUT /api/v1/admin/orgs/{org}/plan`, audit eylemi `plan.update`). API anahtarları asla bu kapsama girmez.

## 8. Faturalama entegrasyonu {#_8-billing-integration}

`internal/billing` sağlayıcıdan bağımsızdır; openlog kendisi ölçer ve uygular, müşteriler, abonelikler, faturalar ve ödemeler sağlayıcıya aittir.

```go
type Provider interface {
    Name() string
    EnsureCustomer(ctx, Customer) (customerID string, err error)
    GetSubscription(ctx, customerID string) (Subscription, error)
    PushUsage(ctx, UsageRecord) error            // UsageRecord.IdempotencyKey üzerinde idempotent
    ParseWebhook(payload []byte, header http.Header) (Event, error) // imzayı doğrular
}
```

- `OPENLOG_BILLING_PROVIDER=none` (varsayılan) push'ları ve webhook'ları kapatır; `noop` her şeyi kabul eder (job'ı ve kayıt tutmayı çalıştırır). Henüz gerçek bir sağlayıcı uygulanmadı (sağlayıcı seçilmedi).
- **Günlük push** (api leader job'u `billing-usage-push`, UTC `OPENLOG_BILLING_PUSH_AT` zamanında, artı başlangıçta ve her çalıştırmada önceki 3 gün): `billing_provider = <provider>` ve müşteri id'si olan organizasyonlar için gün başına metrik başına bir kayıt:
  `ingest_gb` (o gün alınan GiB), `hosts` (o gün farklı host'lar), `users` (push anındaki üyeler). Idempotency anahtarı `openlog:<org_id>:<YYYY-MM-DD>:<metric>`; `billing_usage_pushes` her anahtarı sahiplenir (pending → pushed/failed, en fazla 5
  deneme, bayat pending sahiplenmeleri 15 dakika sonra devralınır).
- **Webhook'lar** `POST /api/v1/billing/webhooks/{provider}` (public; imzayı sağlayıcı doğrular): normalize edilmiş `subscription.updated`, `billing.plan_ref` değeri aboneliğin plan referansına eşit olan planı atar;
  `subscription.canceled` organizasyonu varsayılan plana döndürür; diğer event'ler kabul edilir ve yok sayılır.
  Ezmeler korunur; değişiklikler `billing:<provider>` aktörüyle audit edilir.

## 9. Dışa aktarma {#_9-export}

`GET /api/v1/usage/export?period=YYYY-MM&format=csv|json` (admin, owner). CSV long formattadır:

```
tenant_id,period,day,metric,value
acme,2026-09,2026-09-01,traces.items,120034
acme,2026-09,2026-09-01,traces.bytes,98123456
acme,2026-09,2026-09-01,traces.ingest_bytes,51234567
acme,2026-09,2026-09-01,traces.ingest_requests,4521
…
acme,2026-09,2026-09-01,ingest_bytes,151234567
acme,2026-09,2026-09-01,hosts,12
acme,2026-09,2026-09-01,containers,40
acme,2026-09,2026-09-01,services,7
acme,2026-09,2026-09-01,queries,321
acme,2026-09,2026-09-01,query_failed,2
acme,2026-09,2026-09-01,query_read_rows,123456789
acme,2026-09,2026-09-01,query_read_bytes,4567890123
acme,2026-09,2026-09-01,query_cpu_seconds,12.5
```

JSON organizasyonu, plan id'sini, dönemi, toplamları ve `GET /api/v1/usage/daily`'nin günlerini içerir.

## 10. Metrikler {#_10-metrics}

| Metrik | Bileşen |
|---|---|
| `openlog_quota_ingest_rejected_total{reason=quota_exceeded\|rate_limited}` | ingest |
| `openlog_quota_ingest_pods` | ingest |
| `openlog_quota_evaluations_total{result}` | api (leader) |
| `openlog_processor_rows_inserted_total{table="usage_ingest_1h"}` | processor |
