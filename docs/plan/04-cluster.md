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
| **ClickHouse** | Shard = yazma kapasitesi, replica = okuma + dayanıklılık | `ReplicatedMergeTree` + `Distributed`. Shard anahtarı `cityHash64(tenant_id, host_id)` (metrik/log), `cityHash64(tenant_id, trace_id)` (span). Koordinasyon: ClickHouse Keeper ×3. Soğuk veri S3'e (M3). |
| **API** | HPA | Tenant başına `max_execution_time`, `max_memory_usage`. Sonuç cache'i Valkey'de (M1). |
| **Alert** | Kopya sayısı | Kural sahipliği Postgres'te `SELECT … FOR UPDATE SKIP LOCKED` ile alınan süreli lease; pod ölürse lease süresi dolar ve kural başka pod'a geçer. Bildirimlerde idempotency key. |
| **PostgreSQL** | Primary + replica (CloudNativePG) | Düşük yazma hacmi, yalnızca metadata. |

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
