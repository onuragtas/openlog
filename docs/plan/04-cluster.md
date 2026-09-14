# 04 — Cluster ve Ölçekleme

## Temel kural

**Hiçbir serviste yerel state yoktur.** State yalnızca Kafka, ClickHouse ve PostgreSQL'dedir.
Bu yüzden her servis kopya sayısı artırılarak ölçeklenir ve herhangi bir pod kaybı veri kaybına yol açmaz.

## Bileşen bazında ölçekleme

| Bileşen | Ölçekleme | Dikkat |
|---|---|---|
| **Ingest** | HPA (CPU ve RPS) | OTLP/gRPC uzun ömürlü HTTP/2 bağlantıları kullanır; L4 LB yükü dengelemez → **L7 LB (Envoy)**. Kafka'ya yazılamazsa `503` / `UNAVAILABLE` + `Retry-After` döner; agent disk buffer'dan tekrar dener. `429` / `RESOURCE_EXHAUSTED` tenant limit aşımına ayrılmıştır (M1, D-014). Gövde limiti: sıkıştırılmamış 10 MiB. |
| **Kafka** | Partition ve broker ekleme | Sinyal başına topic. RF=3, `min.insync.replicas=2`, producer `acks=all`. Partition anahtarları [kafka.md](../contracts/kafka.md)'de. |
| **Processor** | Consumer group; kopya ≤ partition sayısı | At-least-once. Offset yalnızca ClickHouse insert başarılı olduktan sonra commit edilir. Aynı batch'in tekrar denemesi `insert_deduplication_token` ile elenir. |
| **Sampler** (tail sampling açıkken, D-075/D-076) | Consumer group `openlog-sampler`; kopya ≤ ham traces partition sayısı; HPA consumer lag'e göre | Ingest trace'i tek partition'a anahtarlar, böylece trace tek kopyada tamponlanır. Revoke edilen partition'ların trace'leri callback içinde hemen karara bağlanır, üretilir, commit edilir; kapanışta tümü. Çökme/lost partition'da tutulan span'ler çoğalabilir (at-least-once, transaction yok). Oran sınırı kopya başına. Bellek ≈ `MAX_BUFFERED_BYTES` + karar önbelleği. |
| **ClickHouse** | Shard = yazma kapasitesi, replica = okuma + dayanıklılık | `ReplicatedMergeTree` + `Distributed`. Shard anahtarı `cityHash64(tenant_id, host_id)` (metrik/log), `cityHash64(tenant_id, trace_id)` (span). Koordinasyon: ClickHouse Keeper ×3. Eski veri S3'e (katmanlı saklama, aşağıda). |
| **API** | HPA | Salt okunur ClickHouse kullanıcısıyla sorgular; tenant başına `max_execution_time`, `max_memory_usage`, `max_rows_to_read`, `max_bytes_to_read` (D-047). Sonuç cache'i Valkey'de (M1). |
| **Alert** | Kopya sayısı | Kural sahipliği Postgres'te `SELECT … FOR UPDATE SKIP LOCKED` ile alınan süreli lease; pod ölürse lease süresi dolar ve kural başka pod'a geçer. Bildirimlerde idempotency key. |
| **PostgreSQL** | Primary + replica (CloudNativePG) | Düşük yazma hacmi, yalnızca metadata. |

## Katmanlı saklama (D-066, D-067)

Ayrıntı ve işletim: [tiered-storage.md](../operations/tiered-storage.md).

- **Politika** `openlog_tiered` her ClickHouse sunucusunda tanımlıdır: volume `default` (yerel veri diski; tüm insert'ler buraya), isteğe bağlı `warm` (ikinci yerel disk), `cold` (S3 `object_storage` disk, metadata yerel diskte, okuma için sınırlı filesystem cache). Hot volume'un adı `default` olmak zorundadır (mevcut tabloların politikası değiştirilebilsin diye).
- **Taşıma** yalnızca TTL ile ve tablo sınıfı başına: metrics 7 g, metrics_1m 30 g, logs 3 g, traces 3 g, APM 7 g, alarm geçmişi 7 g (varsayılanlar, `OPENLOG_STORAGE_COLD_AFTER_DAYS_*`); silme süreleri değişmez. openlog-migrate politikayı ve TTL'i `ON CLUSTER` uygular, her replikada politikanın varlığını önce denetler, adım idempotenttir.
- **Replikasyon:** zero-copy kapalı; her replika kendi kopyasını kendi önekine (`{shard}/{replica}`) yükler. Replika kaybı diğer replikanın S3 verisini etkilemez; S3 maliyeti replika sayısıyla çarpılır. Replika eklemek/yeniden kurmak parçaları sağlıklı replikadan çekip yeniden yükler.
- **Kesinti:** S3 erişilemezken insert'ler ve yalnızca sıcak part'lara dokunan sorgular çalışır; soğuk part okuyan sorgular `S3_ERROR` ile (veya API sorgu zaman aşımıyla) düşer, taşımalar ve soğuk part merge/silme işlemleri tekrar denenir, sıcak disk dolmaya devam eder. Sunucu S3 kapalıyken de açılır.
- **Yedek:** disk snapshot'ı soğuk part'ların yalnızca referanslarını içerir; tiering açıkken `BACKUP … TO S3` (veya object disk destekli clickhouse-backup) kullanılır.
- **Kurulum profilleri:** `single` (Compose) `storage-tiered.xml` + `OPENLOG_S3_*` (yerel deneme için MinIO profili `tiered`); `cluster` (Helm) operator modunda `clickhouse.tieredStorage` CHI'ye politika, Secret/IRSA kimlik bilgisi, cache ve warm PVC ekler; external modda sunucu yapılandırması kullanıcıdadır.

## Güvenlik

- **Bağımlılık bağlantıları:** Kafka (TLS/mTLS + SASL), ClickHouse (native TLS, doğrudan shard bağlantıları dahil), PostgreSQL (TLS). Sertifika dosyaları değişince yeni bağlantılar yeni sertifikayı kullanır, pod yeniden başlatılmaz (D-048; en geç 10 sn sonra, açık bağlantılar eski sertifikayla kapanana kadar sürer). ingest/api sunucuları TLS sonlandırmaz; TLS ingress/Envoy'dadır.
- **ClickHouse kullanıcıları (D-047):** yazıcı (`clickhouse.user`: migrate, processor, APM edge linking, alarm değerlendirme geçmişi) ve salt okunur okuyucu (`clickhouse.readUser`, varsayılan `openlog_reader`: api ve alert tenant sorguları; profil `readonly=2`, yalnızca `GRANT SELECT ON openlog.*`). Okuyucu tanımlı değilse servisler yazıcıya düşer ve uyarı loglar.
- **Tenant sorgu limitleri (D-047):** her sorguda `max_execution_time`, `max_memory_usage`, `max_rows_to_read`, `max_bytes_to_read`, `log_comment` (`{"component","tenant_id"}`) ve `quota_key` = tenant. Varsayılanlar ve tenant bazlı istisnalar env ile (`OPENLOG_QUERY_*`); aşım `422 resource_exhausted`, kota/eşzamanlılık `429`.
- **Operator modunda ClickHouse sunucu TLS'i (D-049):** `clickhouse.tls.server.secretName` (cert-manager Secret'ı) verilince chart CHI'ye `tcp_port_secure`/`https_port`, openSSL server+client ayarlarını, sertifika volume'unu, cluster `secure: "yes"` (remote_servers güvenli portta) ve Service portlarını ekler; istemciler `clickhouse.tls.enabled` ile güvenli porta bağlanır. Varsayılan olarak Keeper istemci bağlantısı (9281) ve Raft TLS'e, replikasyon HTTPS'e (9010) geçer (D-093); aynı Secret Keeper hostlarını (`keeper-<chk>.<ns>.svc`, `chk-<chk>-keeper-0-<n>`) da kapsamalı. Mevcut kurulumda iki upgrade: önce `server.secretName`, CHI/CHK tamamlanınca `tls.enabled` + `clientSecret` (tek upgrade'de pre-upgrade migrate hook'u henüz açılmamış 9440'a bağlanmaya çalışıp takılır). kind'de doğrulandı (2026-09-14, `docs/operations/kind-dev-cluster.md`). Plaintext Keeper 2181 (operator probe'u) ve `disableInsecure` + strict (operator istemci sertifikası sunamaz) açık konu; bu portlar NetworkPolicy ile sınırlandırılmalı.

## Bilinen açık konular (M1 yük testinde karara bağlanacak)

İlk ölçümler: [benchmarks/2026-09-13-kind.md](../operations/benchmarks/2026-09-13-kind.md) (kind, 3 worker, pod başına 300m CPU).

1. **Distributed'a mı, doğrudan shard'a mı insert?** → **Karar: doğrudan shard (D-018).** Ölçüm: processor 1→2 kopyada ×1,42, 4 kopyada artış yok. Darboğaz processor değil, Distributed insert'in koordinasyonu (koordinasyon insert'i p50 ~580 ms, shard insert'leri ~80 ms; koordinasyonun %88–100'ü tek ClickHouse pod'una düştü). M1'de processor shard'ı kendisi hesaplayıp her shard'ın local tablolarına shard başına dedup token ile yazar. Ara çözüm (yalnızca config): `OPENLOG_CLICKHOUSE_ADDR` içine Service yerine tüm ClickHouse pod adresleri yazılır.
2. **Rebalance sonrası tekrar:** Ölçüm: normal çalışmada ve tekil pod kayıplarında %0; processor'lar uzun insert retry'ları sırasında art arda öldürüldüğünde %0,67–1,36. M0 için kabul. Sıfırlamak için: batch sınırlarını poll zamanlamasından bağımsız yapmak (sabit offset katlarında kesmek) ve partition revoke anında flush.
3. **Büyük tenant'lar:** Tek tenant bir shard'ı doldurursa shard anahtarına ek bileşen eklenir.

## Kurulum profilleri

| Profil | İçerik | Kimin için |
|---|---|---|
| `single` | Docker Compose: `openlog-allinone`, tek node Kafka (KRaft), tek node ClickHouse (gömülü Keeper, 1 shard × 1 replica cluster tanımı) | Deneme, küçük ekipler |
| `cluster` | Helm chart: ayrı ingest/processor/api deployment'ları + HPA; ClickHouse (Altinity operator), Kafka (Strimzi), PostgreSQL (CloudNativePG) | Büyük self-hosted ve SaaS |

`single` profilde bile ClickHouse bir cluster tanımıyla (`openlog`, 1 shard, 1 replica) ve Replicated tablolarla çalışır.
Böylece şema ve kod iki profilde **birebir aynıdır**; küçük kurulumdan büyüğe geçiş shard/replica eklemekten ibarettir.

## Test stratejisi

- **Yük:** `openlog-loadgen` ile sabit hızda host/log/span üretimi; ingest ve processor kopya sayısı 1→2→4 iken işlenen veri miktarı ölçülür.
- **Chaos:** Yük altında bir Kafka broker'ı, bir ClickHouse replica'sı ve bir processor pod'u öldürülür; üretilen ve saklanan satır sayıları karşılaştırılır.
- **Ortam:** Yerelde k3d/kind ile 3 node'luk cluster; CI'da her gece.

## Başlangıç hedef rakamları (M0'da doğrulanacak)

| Metrik | Hedef |
|---|---|
| Ingest pod başına kabul | ≥ 50k span veya log / saniye |
| Uçtan uca gecikme (ingest → sorgulanabilir) | p95 < 10 sn |
| Host metrik sorgusu (24 saat, 1 host) | p95 < 500 ms |
| 1M aktif seri üzerinde aggregate sorgu | p95 < 2 sn |
