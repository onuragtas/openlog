# Runbook: openlog kümesini ölçeklemek {#runbook-scaling-the-openlog-cluster}

İlke ([04-cluster.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/04-cluster.md)): servisler stateless'tir, durum
Kafka ve ClickHouse'ta tutulur. Bir servisi replica ekleyerek, Kafka'yı partition ve broker ile, ClickHouse'u ise shard (yazma
kapasitesi) ve replica (okuma kapasitesi ve dayanıklılık) ile ölçeklersin.

Veri yolu ve her sınırın nerede kendini gösterdiği:

```
clients --> Envoy --> openlog-ingest xN --> Kafka (P partitions/topic) --> openlog-processor xM (M <= P) --> ClickHouse S shards x R replicas
             L7        CPU, produce latency     disk, network, ISR          CPU, lag                      parts, merges, insert latency
```

## 1. Temel metrikler {#_1-key-metrics}

openlog'un kendi metriklerinden `openlog_processor_records_rejected_total{reason}` dışındakilerin adları henüz bir kontratla
sabitlenmedi. İstek hızı, gecikme ve durum kodları için `:9464/metrics` üzerinde yayınlananları kullan. Aşağıdaki Kafka ve
ClickHouse metrikleri standarttır.

| Alan | Sinyal | Sağlıklı | Değilse ne yapmalı |
|---|---|---|---|
| Ingest | Pod başına CPU (HPA hedefi %70) | < %70 | daha fazla ingest replica'sı |
| Ingest | 503/`UNAVAILABLE` oranı (`Retry-After` ile) | ~0 | Kafka produce yavaş veya erişilemiyor: ingest'e değil broker'lara bak |
| Ingest | 429 oranı | yalnızca limitini aşan tenant'larda beklenir | kapasite değil, tenant başına limit meselesi (D-014) |
| Ingest | İstek gecikmesi p99 | produce timeout'unun (10 sn) payla altında | broker'lar / ISR |
| Ingest | Pod başına istek (host başına Envoy `envoy_cluster_upstream_rq_total`) | dengeli | L7 dengelemeye bak |
| Kafka | `kafka_consumergroup_lag` (`openlog-processor` grubu), toplam ve partition başına en yüksek | düz, düşük | daha fazla processor, ya da ClickHouse yavaştır |
| Kafka | Under-replicated partition'lar, offline partition'lar | 0 | broker sağlığı, disk |
| Kafka | Broker disk kullanımı | 24 saat retention'da < %70 | daha fazla broker / disk, ya da rebalance |
| Kafka | Request handler boşta kalma oranı, produce p99 | > %30, düşük | daha fazla broker |
| Processor | Pod başına CPU | < %70 | daha fazla processor (M = P olana kadar) |
| Processor | `openlog_processor_records_rejected_total` | 0 | producer/şema hatası |
| Processor | Saat başına consumer group rebalance'ı | düşük | HPA scale-down'ı yavaşlat |
| ClickHouse | `ClickHouseProfileEvents_InsertedRows`, insert sorgusu p99 | kararlı | daha fazla shard |
| ClickHouse | Partition başına aktif part (`system.parts`), `DelayedInserts`, `RejectedInserts` | < 300 part, 0 | daha büyük batch'ler, daha fazla shard |
| ClickHouse | `ReplicasMaxQueueSize`, `ReplicasMaxAbsoluteDelay` | düşük | replica / Keeper sağlığı |
| ClickHouse | Arka plan merge havuzu kullanımı, disk | < %80 | daha fazla shard, disk |
| ClickHouse | Sorgu p95 (API) | 04-cluster.md'deki hedefler | daha fazla replica (okuma) |
| Keeper | Gecikme (`mntr`: `zk_avg_latency`), bekleyen istekler | < 10 ms, ~0 | daha hızlı disk, ayrılmış node'lar |

İşe yarar ClickHouse sorguları (herhangi bir node'da çalıştır):

```sql
-- shard host'u ve tablo başına part sayısı
SELECT hostName(), table, count() AS parts
FROM clusterAllReplicas('openlog', system.parts)
WHERE database = 'openlog' AND active GROUP BY 1, 2 ORDER BY parts DESC;

-- son 5 dakikadaki insert hızı
SELECT hostName(), sum(written_rows) / 300 AS rows_per_s
FROM clusterAllReplicas('openlog', system.query_log)
WHERE type = 'QueryFinish' AND query_kind = 'Insert' AND event_time > now() - 300
GROUP BY 1;

-- verinin shard'lara dağılımı
SELECT hostName(), table, formatReadableSize(sum(bytes_on_disk))
FROM clusterAllReplicas('openlog', system.parts)
WHERE database = 'openlog' AND active GROUP BY 1, 2 ORDER BY 2, 1;
```

### Processor metrikleri ve consumer lag alarmları {#processor-metrics-and-consumer-lag-alerts}

| Metrik | Anlamı |
|---|---|
| `openlog_processor_consumer_lag_records{topic,partition}` | Bu processor pod'una atanan partition'larda log end offset eksi commit edilmiş offset (group coordinator üzerinden 15 sn'de bir yenilenir). Sahibi olmayan partition'ların serisi olmaz. |
| `openlog_processor_consumer_lag_seconds{topic}` | Bu pod'un çektiği veya geride kaldığı, henüz insert ve commit edilmemiş en eski kaydın yaşı (kayıt zaman damgası = ingest'in produce zamanı), partition'ları üzerinden en yüksek değer. Scrape anında hesaplanır, bu yüzden insert'ler takılı kaldıkça artmaya devam eder. Normali: yaklaşık 1.5 × `OPENLOG_PROCESSOR_FLUSH_INTERVAL`. |
| `openlog_processor_insert_failures_total` | Başarısız insert denemeleri (aynı token'larla yeniden denenir). |
| `openlog_processor_chunk_cuts_total{reason}` | Kurala göre chunk sınırları. `window`/`bytes` deterministiktir, `memory` payının yüksek olması `OPENLOG_PROCESSOR_MAX_BUFFERED_BYTES` değerinin fazla küçük olduğunu gösterir (bkz. kafka.md). |
| `openlog_processor_shard_insert_duration_seconds{shard}`, `openlog_processor_shard_insert_failures_total{shard,replica}`, `openlog_processor_clickhouse_shards` | `direct` insert modu: shard başına gecikme, replica failover'ları, kullanılan topolojideki shard'lar. |

**Kaybın sınırı retention'dır.** Kayıtlar commit edilmeden önce topic'in `retention.ms` değerinden eski hale gelirse Kafka onları
siler ve grup saklanan en eski offset'ten devam eder (2026-09-13 benchmark'ı: 17 dakikalık bir ClickHouse kesintisi, 30 dakikalık
retention ile bu sınıra ~10 dakika kadar yaklaştı). Alarmı kayıt sayısına göre değil, retention'a karşı saniye cinsinden lag'e göre
kur. Varsayılan `retention.ms` = 86400000 (24 saat) için örnek Prometheus kuralları aşağıda, eşikleri kendi retention'ına göre
ölçekle:

```yaml
groups:
  - name: openlog-processor
    rules:
      - alert: OpenlogProcessorLagHigh
        # retention.ms'in %25'i (86400 sn): veri kaybına daha saatler var.
        expr: max by (topic) (openlog_processor_consumer_lag_seconds) > 0.25 * 86400
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "openlog processor is {{ $value | humanizeDuration }} behind on {{ $labels.topic }} (retention 24h)"
      - alert: OpenlogProcessorLagCritical
        # retention.ms'in %50'si: bu sürerse tüketilmemiş telemetriyi Kafka silecek.
        expr: max by (topic) (openlog_processor_consumer_lag_seconds) > 0.5 * 86400
        for: 1m
        labels: { severity: critical }
      - alert: OpenlogProcessorLagGrowing
        expr: sum by (topic) (openlog_processor_consumer_lag_records) > 100000
          and deriv(sum by (topic) (openlog_processor_consumer_lag_records)[15m:1m]) > 0
        for: 15m
        labels: { severity: warning }
      - alert: OpenlogProcessorInsertsFailing
        expr: sum(rate(openlog_processor_insert_failures_total[5m])) > 0
        for: 10m
        labels: { severity: warning }
      - alert: OpenlogProcessorAbsent
        # Lag yayınlayan processor yok: kimse tüketmiyor. Processor'ın ayakta olmasına bağlı olmayan
        # bir Kafka exporter'ın kafka_consumergroup_lag metriğiyle karşılaştır.
        expr: absent(openlog_processor_consumer_lag_seconds)
        for: 10m
        labels: { severity: critical }
```

## 2. Ingest {#_2-ingest}

**Replica ekle:** CPU hedefin üstündeyse veya Kafka produce gecikmesi normalken istek gecikmesi artıyorsa. Ingest kendi başına batch
yapmaz, Kafka yetiştiği sürece throughput replica sayısıyla ölçeklenir. 04-cluster.md'deki hedef: pod başına saniyede >= 50k span
veya log. Kendininkini ölç.

- Normal büyümeyi HPA (CPU) karşılar: `ingest.autoscaling.{minReplicas,maxReplicas}`.
- PDB'ye takılmadan node boşaltabilmek için `minReplicas` değerini >= 2 tut (production'da 3).
- **503 hataları için ingest replica'sı ekleme.** Kafka zamanında onay vermediğinde veya erişilemediğinde ingest
  `Retry-After` ile 503/`UNAVAILABLE` döner, daha fazla ingest pod'u Kafka'ya daha fazla yük bindirir. 429 tenant başına limitlere
  ayrılmıştır (D-014) ve o da kapasite sinyali değildir.
- Yeni replica'lar yalnızca dengeleme L7 ise (Envoy, gRPC'yi anlayan Ingress, GRPCRoute) gRPC trafiği alır. Scale-up sonrasında
  Envoy istatistiklerinde upstream host başına istek sayısına bak. L4 LoadBalancer arkasındaysan Envoy'u da ölçekle,
  `envoy.maxConnectionDuration` istemcileri yeniden bağlanmaya zorlar, böylece yük Envoy pod'larına yayılır.
- İstek gövdesi sınırı sıkıştırma açıldıktan sonra 10 MiB'dir. Ajanlardan gelen çok büyük batch'ler istek başına belleği artırır.
  `OPENLOG_INGEST_MAX_BODY_BYTES` değerini artırmadan önce `ingest.resources.limits.memory` değerini artır.

## 3. Processor ve partition'lar {#_3-processor-vs-partitions}

Consumer group her partition'ı tam olarak bir üyeye atar, bu yüzden **işe yarayan processor replica sayısı <= topic başına partition
sayısı** (her processor üç topic'in hepsini tüketir). Fazla replica'lar boşta durur. `processor.autoscaling.maxReplicas` veya
`processor.replicaCount`, `kafka.topics.partitions` değerini aşarsa chart render edilmez.

**Processor replica'sı ekle:** consumer lag sürekli büyüyorsa ve ClickHouse insert gecikmesi normalken processor CPU'su yüksekse.

**Processor ekleme:** lag büyüyor ama processor CPU'su düşükse. Darboğaz ClickHouse'tur (insert gecikmesine, part'lara,
`DelayedInserts`'e bak). Bu durumda daha fazla processor yalnızca daha fazla eşzamanlı insert ve daha fazla part demektir. Bunun
yerine ClickHouse'u ölçekle veya daha az ama daha büyük batch'ler yazmak için `OPENLOG_PROCESSOR_BATCH_ROWS` /
`OPENLOG_PROCESSOR_FLUSH_INTERVAL` değerlerini artır.

Her üyelik değişikliği bir rebalance tetikler: tüketim kısa süreliğine durur. Geri alınan partition'ların buffer'daki, commit
edilmemiş kayıtları atılır ve yeni sahip tarafından yeniden okunur. Yeni sahip aynı chunk'ları ve dedup token'larını ürettiği için
rebalance normalde hiçbir şeyi iki kez yazmaz (kalan durumlar: kafka.md, "Consumption semantics"). Her processor, flush aralığı
başına aktif partition, tablo ve shard başına bir blok yazar, yani düşük yükte ClickHouse tablo ve shard başına yaklaşık
`partitions / OPENLOG_PROCESSOR_FLUSH_INTERVAL` insert görür (48 partition, 2 sn: 24/sn). Part'lar birikiyorsa flush aralığını
artır. Chart'taki yavaş scale-down politikasının (`processor.autoscaling.behavior`) nedeni de budur. Lag'e bağlı autoscaling için
CPU yerine KEDA'nın Kafka scaler'ını (`lagThreshold`) düşünebilirsin, `maxReplicaCount` değerini <= partition sayısında tut.

Boyutlandırma için pratik kural: `partitions = 2-4 x expected steady-state processor replicas`, böylece yeniden partition'lamadan
ölçeklenecek payın olur. `cluster` profilinin varsayılanı 48'dir.

### Partition sayısını güvenle artırmak {#increasing-partitions-safely}

Partition sayısı yalnızca artırılabilir, asla azaltılamaz. Artırmak, **yeni** kayıtlar için key -> partition eşlemesini
(`hash(key) % partitions`) değiştirir:

- Bir key'in (metrik ve loglar için `<tenant>/<host.id>`, trace'ler için `<tenant>/<trace_id>`) değişiklikten önce üretilmiş kayıtları
  eski partition'da kalır, yenileri başka bir partition'a gidebilir. Boşaltma penceresi boyunca host başına sıralama garanti değildir.
  kafka.md, envanter öğelerinin "snapshot complete" kaydından önce gelmesi için bu sıralamaya dayanır. Değişikliğin ortasına denk
  gelen bir snapshot, tüm öğeleri saklanmadan tamamlandı olarak işaretlenebilir. Bir sonraki envanter snapshot'ı bunu düzeltir. Veri
  kaybı olmaz.
- Ingest producer'ları ve processor'lar yeni partition'ları metadata yenilemesiyle otomatik görür (varsayılan
  `metadata.max.age.ms` 5 dk). Yeniden başlatma gerekmez, ama bir rebalance olur.

Prosedür:

1. Trafiğin düşük olduğu bir zaman seç. Consumer lag'in 0'a yakın olduğunu doğrula (böylece eski eşlemeli az veri kalır).
2. Üç topic'in hepsinde partition sayısını aynı değere çıkar (eşit tut, chart tek değer kullanır).
   - operators modu: `helm upgrade ... --set kafka.topics.partitions=96` (Topic Operator uygular).
   - external modu: her topic için `kafka-topics.sh --alter --topic openlog.otlp.metrics.v1 --partitions 96`.
     (`openlog-migrate` mevcut topic'leri değiştirmez.)
3. Gerekiyorsa aynı upgrade'de `processor.autoscaling.maxReplicas` değerini de artır.
4. Kafka broker sayısı değiştiyse veya partition boyutları dengesizse replica'ları rebalance et:
   Strimzi Cruise Control (`kafka.strimzi.cruiseControl.enabled=true` + bir `KafkaRebalance` kaynağı) veya external modda
   `kafka-reassign-partitions.sh`.
5. Durum oturana kadar under-replicated partition'ları ve consumer lag'i izle.

Partition sayısının broker belleği, file handle ve leader seçimi süresi açısından da bir maliyeti vardır. Broker başına birkaç bin
partition replica'sının altında kal.

## 4. Kafka broker'ları {#_4-kafka-brokers}

**Broker ekle:** ayarlı retention'da broker diski ~%70'i geçtiyse, ağ veya request handler thread'leri doyduysa ya da ISR sağlıklıyken
produce p99 yükseliyorsa. Yeni broker'lar sen rebalance edene kadar mevcut partition'lardan hiçbirini almaz (`mode: add-brokers` ile
Cruise Control `KafkaRebalance`). Values'ta broker node pool'unun `replicas` değerini artır. Partition'ları kaldırılacak broker'lardan
taşımadan (`mode: remove-brokers`) bu değeri asla düşürme.

RF=3 / `min.insync.replicas=2` olarak tut, "bir broker'ı öldürmek"i güvenli yapan budur. RF=3 ile partition başına bir broker'ı, produce
başarısız olmadan kaybedebilirsin. İkisini kaybetmek onaylanmış veriyi kaybetmek yerine produce'ları başarısız yapar (ingest 503 döner).

## 5. ClickHouse: shard'lar ve replica'lar {#_5-clickhouse-shards-vs-replicas}

| İhtiyaç | Ekle | Neden |
|---|---|---|
| Daha fazla insert throughput'u, merge'lerin yetişmesi, daha fazla depolama | **Shard** | Her shard verinin ayrık bir bölümünü tutar ve kendi merge'lerini yapar. |
| Daha fazla eşzamanlı sorgu, hata toleransı | **Replica** | Her replica kendi shard'ının tam bir kopyasını tutar. Distributed okumalar shard başına bir replica seçer. |

**Daha fazla shard** sinyalleri: insert gecikmesinin artması, partition başına part sayısının büyümesi, `DelayedInserts` /
"Too many parts", merge'lerin doyması, disk > %70. **Daha fazla replica** sinyalleri: insert'ler iyiyken sorgu CPU'sunun doyması veya
bir dayanıklılık gereksinimi (production >= 2 replica ile çalışmalı).

### Replica eklemek {#adding-replicas}

`helm upgrade ... --set clickhouse.altinity.replicas=3`. Altinity operator pod'ları oluşturur, şemayı onlarda kurar ve
ReplicatedMergeTree veriyi Keeper ile koordine olan eşlerinden çeker. `system.replication_queue` boşalana kadar izle. Replica'lar
yetişme sırasında Keeper yükü ve ağ trafiği ekler.

### Shard eklemek (ve resharding uyarıları) {#adding-shards-and-resharding-caveats}

`helm upgrade ... --set clickhouse.altinity.shards=4`. Operator host'ları `remote_servers`'a ekler ve tabloları yeni shard'larda
oluşturur. `pre-upgrade` migrate hook'u da idempotent şemayı yeniden uygular (şununla doğrula: `SELECT hostName(), count() FROM clusterAllReplicas('openlog', system.tables)
WHERE database='openlog' GROUP BY 1`).

Uyarılar: shard eklemek mevcut veriyi yeniden **dağıtmaz**:

1. **Mevcut veri olduğu yerde kalır.** Distributed tablolar yeni satırları toplam shard ağırlığına göre mod alarak
   `cityHash64(tenant_id, host_id)` (metrics, logs, hosts, inventory) veya `cityHash64(tenant_id, trace_id)` (spans) ile yönlendirir. Yeni
   shard'lar boş başlar ve yalnızca yeni veriyle dolar, bu yüzden TTL eski veriyi temizleyene kadar disk kullanımı dengesiz kalır (spans 7g,
   logs 14g, metrics 30g, rollup'lar 395g). Distributed tüm shard'ları okuduğu için sorgular doğru kalır.
2. **Aynı key başka bir shard'a geçer.** Değişiklikten sonra bir host'un satırları (veya bir trace'in span'leri) iki shard'da olabilir:
   eski satırlar eski shard'da, yenileri yeni shard'da.
   - `ReplacingMergeTree` tabloları (`hosts`, `inventory_snapshots`) yalnızca shard içinde dedup yapar. Bu durumda Distributed tablodan
     `FINAL` ile okuma, eski satırın süresi dolana kadar bir host için iki satır dönebilir. Distributed tablo üzerinden `argMax` kullanan
     okumalar doğru kalır. API'de argMax'i tercih et.
   - `AggregatingMergeTree` rollup'ları (`metrics_1m`, `trace_index`) shard'lar arasında son bir `GROUP BY` ister (Distributed sorgular
     bunu zaten yapar), sorun yok.
   - Yerleşim değiştiği sırada devam eden trace'ler shard'lara bölünebilir. `trace_index` + Distributed üzerinden trace arama yine çalışır.
3. **Veri taşımadan shard key'ini değiştirme veya shard sayısını geri düşürme.** Bir shard'ı kaldırmak onun verisini sorgulardan siler.
4. **Eski veriyi dengelemek için** (TTL'ler düşünülünce nadiren değer): yeni bir küme yerleşimi veya yeni tablolar oluştur, partition
   boyutunda parçalarla `INSERT INTO openlog.<table> SELECT * FROM remote(...)` çalıştır, sayıları doğrula, sonra yer değiştir.
   Alternatif olarak geçici bir süre yeni veriyi daha çok yeni shard'lara yönlendirmek için `remote_servers` içindeki shard `weight`
   değerini kullan (özel operator yapılandırması gerekir).
5. Tek bir çok büyük tenant/host yine de bir shard'ı aşırı yükleyebilir. Bkz. 04-cluster.md açık madde 3.

### Keeper {#keeper}

3 node tut (çok büyük kümelerde 5). Keeper gecikmeye duyarlıdır: hızlı disk ve mümkünse ayrılmış node'lar. Keeper sorunları salt okunur
replica'lar ve takılı kalan `ON CLUSTER` DDL olarak görünür, bu da `openlog-migrate`'i engeller.

## 6. API {#_6-api}

CPU'ya göre ölçekle (HPA). Sorgu maliyetini `OPENLOG_API_QUERY_TIMEOUT` (ClickHouse `max_execution_time`) ve `OPENLOG_API_MAX_ROWS`
sınırlar. API pod'ları boştayken sorgular yavaşsa darboğaz ClickHouse'tur: replica (eşzamanlılık) veya shard (tarama paralelliği) ekle.

## 7. Hızlı karar tablosu {#_7-quick-decision-table}

| Belirti | Olası neden | Yapılacak |
|---|---|---|
| Ingest CPU'su yüksek, 503 yok | ingest kapasitesi | + ingest replica'sı |
| Ingest 503 + Retry-After, ingest CPU'su düşük | Kafka yavaş / ISR küçülmüş | broker'lara bak; + broker |
| Tek tenant için ingest 429 | tenant limitini aşmış | tenant limitleri, ölçekleme değil |
| Yeni ingest pod'ları boşta | gRPC'nin L4 dengelenmesi | Envoy'u / gRPC'yi anlayan ingress'i aç |
| Lag büyüyor, processor CPU'su yüksek, CH insert'leri hızlı | processor kapasitesi | + processor (<= partition) |
| Lag büyüyor, processor CPU'su düşük, CH insert'leri yavaş | ClickHouse yazma kapasitesi | + shard; daha büyük batch'ler |
| Processor sayısı == partition sayısı ve hâlâ geride | partition tavanı | partition sayısını artır (bölüm 3) |
| Too many parts / DelayedInserts | küçük veya fazla eşzamanlı insert'ler | daha büyük batch'ler; + shard |
| API yavaş, CH sorgu CPU'su yüksek | okuma kapasitesi | + replica |
| Replica'lar salt okunur, DDL takılı | Keeper | Keeper sağlığı, disk gecikmesi |
