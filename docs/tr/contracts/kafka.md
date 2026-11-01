# Sözleşme: Kafka (v1) {#contract-kafka-v1}

Producer: `openlog-ingest`. Consumer: `openlog-processor` (varsayılan consumer group `openlog-processor`).

## Topic'ler {#topics}

`<prefix>` varsayılan olarak `openlog`'dur (`OPENLOG_KAFKA_TOPIC_PREFIX`).

| Topic | Değer | Partition anahtarı |
|---|---|---|
| `<prefix>.otlp.metrics.v1` | protobuf `ExportMetricsServiceRequest` | `host.id` taşıyan ilk resource'un `<tenant_id>/<host.id>` değeri, yoksa `<tenant_id>/<service.name>`, o da yoksa `<tenant_id>/` |
| `<prefix>.otlp.logs.v1` | protobuf `ExportLogsServiceRequest` | metriklerle aynı kural |
| `<prefix>.otlp.traces.v1` | protobuf `ExportTraceServiceRequest` | `<tenant_id>/<hex trace_id of the first span>`; tail sampling açıkken **trace id başına** bir kayıt (`<tenant_id>/<hex trace_id>`) |
| `<prefix>.otlp.traces.sampled.v1` | protobuf `ExportTraceServiceRequest`: bir trace'in tutulan span'leri (veya geç gelen span'leri), tracestate `ot=th` yeniden yazılmış olarak (apm.md §4.2) | `<tenant_id>/<hex trace_id>` |
| `<prefix>.otlp.profiles.v1` | protobuf `ExportProfilesServiceRequest` (OTLP profiles, upstream'de hâlâ `v1development`; geldiği gibi byte byte üretilir, openlog asla yeniden encode etmez) | sahip olan ilk resource'un `<tenant_id>/<service.name>` değeri, yoksa `<tenant_id>/` |

**Tail sampling (D-075, D-076).** Sampled topic, tail sampling açık olsun olmasın diğerleriyle aynı ayarlarla oluşturulur. `OPENLOG_TAILSAMPLING_ENABLED=true` olduğunda:
- ingest, trace export isteklerini trace id'ye göre böler, böylece bir trace'in tüm span'leri tek partition'a gider;
- `openlog-sampler` (consumer group `openlog-sampler`, `OPENLOG_TAILSAMPLING_GROUP`) `<prefix>.otlp.traces.v1` topic'ini tüketir ve `<prefix>.otlp.traces.sampled.v1` topic'ine aynı dört header ile (tenant, şema sürümü, ilk kaydın alınma zamanı ve request id) üretir. Ham bir partition'ı yalnızca hâlâ buffer'da span'i veya üretilmemiş tutulan span'i olan en eski kayda kadar commit eder. Rebalance'ta bıraktığı partition'lar revoke callback'inin içinde karara bağlanır, üretilir ve commit edilir. Teslimat at-least-once'tır; sampler'ın çökmesi, commit hatası veya kaybedilen partition'lar tutulan span'leri çoğaltabilir ve processor'ın dedup token'ları bu kopyaları kapsamaz, çünkü token'lar sampled topic'in offset aralığı başınadır. Bkz. docs/operations/tail-sampling.md;
- processor, `<prefix>.otlp.traces.v1` yerine `<prefix>.otlp.traces.sampled.v1` topic'ini tüketir (aşağıdaki chunk, token ve commit kuralları ona da aynen uygulanır).

Metrik ve logları host'a göre anahtarlamak, envanter snapshot kalemlerini ve snapshot-complete kayıtlarını tek partition içinde sıralı tutar.

| Ayar | `single` profili | `cluster` profili |
|---|---|---|
| Partition sayısı | 6 | 48 (processor replica sayısıyla ölçekle) |
| Replication factor | 1 | 3 |
| `min.insync.replicas` | 1 | 2 |
| `retention.ms` | 86400000 (24 sa) | 86400000 |
| `max.message.bytes` | 12582912 (12 MiB) | 12582912 |

Topic'leri `openlog-migrate` (veya Helm chart'ının Strimzi `KafkaTopic` kaynakları) oluşturur; servisler asla kendiliğinden topic açmaz. `openlog-allinone`, `OPENLOG_MIGRATE_ON_START=true` olduğunda `OPENLOG_MIGRATE_SKIP_KAFKA=true` olmadığı sürece aynı migration'ı (topic'ler dahil) çalıştırır. Topic ayarları [config.md](/tr/contracts/config) içindeki `OPENLOG_KAFKA_*` değişkenlerinden gelir.

## Mesaj {#message}

- **Değer:** protobuf olarak serialize edilmiş, **sıkıştırılmamış** OTLP export isteği (`zstd` batch sıkıştırmasını Kafka producer'ı uygular). Ingest, OTLP/JSON'u üretmeden önce protobuf'a çevirir.
- **Header'lar:**

| Header | Değer |
|---|---|
| `openlog-schema-version` | `1` |
| `openlog-tenant-id` | lisans anahtarından çözülen tenant id |
| `openlog-received-at` | ingest'in alma zamanı, unix nanosaniye, ondalık string |
| `openlog-request-id` | ingest isteği başına benzersiz id |

- Producer ayarları: `acks=all`, idempotent producer açık, sıkıştırma `zstd`.
- Ingest, istemciye başarıyı **ancak** üretim onaylandıktan sonra döner.

## Tüketim semantiği {#consumption-semantics}

- At-least-once. Offset'ler, ancak o kayıtlardan türeyen tüm satırlar ClickHouse'a yazıldıktan sonra commit edilir.
- **Chunk'lar.** Processor kayıtları partition başına *chunk*'lar halinde gruplar: kesintisiz offset aralıkları. Bir chunk şu durumlarda kapanır:
  1. `floor(record timestamp / OPENLOG_PROCESSOR_FLUSH_INTERVAL)` zaman penceresi chunk'ın penceresinden **sonra** olan bir kayıttan önce (penceresi daha erken olan bir kayıt, örneğin saati geride kalan bir ingest pod'undan gelen, açık chunk'a katılır): `window`;
  2. chunk'ın sıkıştırılmamış kayıt byte'larını `OPENLOG_PROCESSOR_BATCH_BYTES` değerine getiren kayıttan sonra: `bytes`;
  3. partition high watermark'ına yetiştiğinde ve pencere sonunun üstüne yarım flush aralığı geçtiğinde (veya chunk 2,5 flush aralığı açık kaldığında; bu, saati processor'ın ilerisinde olan producer'lar içindir): `idle`;
  4. processor'ın buffer'daki tüm kayıt byte'ları `OPENLOG_PROCESSOR_MAX_BUFFERED_BYTES` değerine ulaştığında: `memory`;
  5. kapanışta: `shutdown`.

  Kaydın zaman damgası producer'ın CreateTime'ıdır, ingest üretirken ayarlar. Kural 1 ve 2 yalnızca kayıtlara bağlıdır, yani yeniden teslim edilen bir aralık (aynı veya başka bir grup üyesine) aynı chunk'lara kesilir. `openlog_processor_chunk_cuts_total{reason}` kesimleri kural başına sayar.
- Kapanan her chunk tablo tablo yazılır (host'lar en sonda); her partition için, tüm tabloları yazılmış son chunk'tan sonraki offset commit edilir. Chunk'lar partition'lar arasında asla birleştirilmez, yani her insert bloğunun tam olarak bir sahip partition'ı vardır.
- **Deduplication token'ı** blok başına: `<topic>:<partition>:<first_offset>-<last_offset>:<table>`.
  - Bir tablonun tek chunk'taki satırları `OPENLOG_PROCESSOR_BATCH_ROWS` değerini aşarsa, sırayla bu boyutta bloklara bölünür ve token `#<block_index>/<batch_rows>` alır.
  - `OPENLOG_PROCESSOR_INSERT_MODE=direct`, `<table>_local` içine shard başına bir blok yazar ve `:<shard_num>/<layout>` ekler; `<layout>`, shard numaraları ve ağırlıklarının 8 hex basamağıdır (FNV-1a 32). Deduplication blok id'leri Keeper'da shard başına tutulur, yani aynı shard'ın başka bir replica'sındaki yeniden deneme de deduplicate edilir.
  - Insert'ler `insert_deduplicate=1` ve `deduplicate_blocks_in_dependent_materialized_views=1` ayarlar. İkincisi olmadan, kaynak tablonun attığı yeniden denenmiş blok `metrics_1m` ve `trace_index` tablolarına yine eklenir (ClickHouse 25.8 üzerinde, iki insert modunda da doğrulandı).
- **Rebalance'lar.** Yazılmayı bekleyen kapalı chunk olmadığı sürece rebalance'a izin verilir. Revoke edilen veya kaybedilen bir partition için buffer'daki kayıtlar yazılmadan ve commit edilmeden atılır; yeni sahip commit edilmiş offset'ten okur ve aynı chunk'ları keser.
- **Kalan kopya durumları.** Kopya oluşması için, satırları yazılmış ama offset'leri commit edilmemiş bir aralık (insert ile commit arasında crash veya kill, başarısız bir commit, yeniden denerken partition'larını kaybeden bir üye) **ve** şunlardan biri gerekir:
  1. yazılan chunk kural 3, 4 veya 5 ile kesilmiş ve yeniden teslim edilen aralık başka yerden kesilmiştir (`idle`: aynı pencerenin bir kaydı partition'a pencere bittikten yarım flush aralığından fazla sonra ulaşmıştır; `memory`: buffer sınırı; `shutdown`: son flush yazılmış ama commit'i kaybolmuştur);
  2. yeniden teslim, ClickHouse token'ı unuttuktan sonra olur: bir tablo shard başına son `replicated_deduplication_window` blok id'sini tutar (25.8'de varsayılan 1000 ve en fazla `replicated_deduplication_window_seconds` = 7 gün). Düşük yükte flush aralığı başına, aktif partition başına, tablo başına, shard başına bir blok yazılır; örneğin 48 partition / 2 sn = 24 blok/sn, yani ~40 sn'lik bir ufuk. Daha uzun kesintilerden sonraki yeniden teslimlerin de deduplicate edilmesi gerekiyorsa `*_local` tablolarında `replicated_deduplication_window` değerini artır;
  3. insert ile yeniden teslim arasında shard düzeni değişmiştir (shard eklenmiş, ağırlıklar değişmiş; `direct` modu) veya `OPENLOG_PROCESSOR_INSERT_MODE`, `OPENLOG_PROCESSOR_FLUSH_INTERVAL` ya da `OPENLOG_PROCESSOR_BATCH_BYTES` değişmiştir veya satır dönüşümü farklı bir processor sürümü aralığı yeniden teslim eder.

  Bunların hiçbiri satır kaybettiremez: iki blok ancak aynı kayıtlardan, aynı bölünmeden ve aynı shard düzeninden geliyorlarsa aynı token'ı paylaşır.
- Bilinmeyen `openlog-schema-version` taşıyan veya değeri decode edilemeyen kayıtlar loglanır, `openlog_processor_records_rejected_total{reason}` içinde sayılır ve atlanır (M0'da dead-letter topic yok).
