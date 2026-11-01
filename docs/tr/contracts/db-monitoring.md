# Sözleşme: Veritabanı sorgu performansı izleme (D-138) {#contract-database-query-performance-monitoring-d-138}

Altyapı ajanının veritabanı entegrasyonlarının statement'lar, session'lar ve plan'lar hakkında ne gönderdiği, backend'in bunları nasıl
sakladığı ve onları okuyan API. Entegrasyonların sunucu metriklerini (semantic-conventions §6.4–6.7) ve APM'in istemci görünümünü
(apm.md §7) tamamlar: APM "servisimin hangi veritabanı çağrıları yavaş" sorusunu cevaplar, bu ise "bu veritabanı sunucusunda en çok ne
maliyetli, session'ları neyi bekliyor, planner onu nasıl çalıştırıyor ve onu hangi servisler gönderiyor" sorusunu.

**Databases** ekranı ikisini de gösterir: **From applications** (APM, sunucuya bağlantı yok) ve **From servers** (bu sözleşme). Hiçbir sunucu raporlamadığı sürece ilkiyle açılır.

## 1. Genel bakış {#_1-overview}

```
PostgreSQL ── pg_stat_statements, pg_stat_activity, EXPLAIN ─┐
MySQL      ── performance_schema digests/threads, EXPLAIN ────┼─ infra agent ─ OTLP logs ─ processor ─ db_query_stats
SQL Server ── dm_exec_query_stats/requests, dm_exec_query_plan┘   (redacts)                (normalizes)  db_session_samples
                                                                                                        db_query_plans
```

Entegrasyon başına opt-in: `integrations.<postgresql|mysql|mssql>.query_stats.enabled: true` (config.yaml; instance başına da olur).
Açıkken ajan, instance başına:

| Tür | Ne zaman | Event (`event.name`) | Tablo |
|---|---|---|---|
| Statement istatistikleri | her toplamada (`integrations.interval`, 30 sn) | `openlog.db.query_stats` | `db_query_stats` |
| Session örneği | her `query_stats.sample_interval` (10 sn) | `openlog.db.session_sample` | `db_session_samples` |
| Execution plan | en üstteki bir statement en fazla her `explain_interval` (1 sa); şekli değiştiğinde veya günde bir kez gönderilir | `openlog.db.query_plan` | `db_query_plans` |

| Anahtar (`query_stats.…`) | Varsayılan | Anlamı |
|---|---|---|
| `enabled` | `false` | açma kapama |
| `top_n` | 20 (en fazla 100) | toplama başına gönderilen statement sayısı, aralıkta harcanan süreye göre |
| `min_calls` | 1 | toplamda bundan az çalışmış statement'ları atla |
| `sessions` | `true` | boşta olmayan session'ları örnekle |
| `sample_interval` | `10s` (en az 1 sn) | session örnekleme aralığı |
| `explain` | `true` | plan yakala |
| `explain_interval` | `1h` (en az 5 dk) | bir statement'ın ne sıklıkla yeniden explain edileceği |

## 2. Kayıtlar (OTLP log kayıtları) {#_2-records-otlp-log-records}

Resource: entegrasyon instance resource'u (semantic-conventions §6.1): host attribute'ları, `service.instance.id`
(aşağıda her yerde bir veritabanı instance'ının anahtarı), `server.address`, `server.port`, `openlog.integration.id`.
Her kayıt `event.name` ve `db.system.name` (`postgresql`, `mysql`, `mssql`) taşır.

| Attribute | Stats | Session | Plan | Anlamı |
|---|---|---|---|---|
| `db.namespace` | ✓ | ✓ | ✓ | veritabanı / şema |
| `db.query.text` | ✓ (zorunlu) | ✓ | ✓ (zorunlu) | **literal'leri redakte edilmiş** statement metni (§3.4) |
| `openlog.db.query.id` | ✓ | ✓ | – | sunucu id'si: `queryid`, `DIGEST`, `query_hash` |
| `openlog.db.user` | ✓ (PG) | ✓ | – | rol / login |
| `openlog.db.interval_seconds` | ✓ (zorunlu) | – | – | delta'ların kapsadığı aralığın uzunluğu |
| `openlog.db.calls`, `.total_time_ms`, `.rows` | ✓ | – | – | **aralıktaki** çalıştırma sayısı, süre (ms), dönen/etkilenen satır |
| `openlog.db.rows_examined`, `.errors`, `.no_index_used` | MySQL | – | – | |
| `openlog.db.blocks_hit`, `.blocks_read` | PG, SQL Server | – | – | shared block'lar / logical − physical ve physical okumalar |
| `openlog.db.session.id`, `.session.state` | – | ✓ | – | pid / processlist id / session_id; `active`, `idle in transaction`, `query`, `suspended`, … |
| `openlog.db.wait.type`, `.wait.event` | – | ✓ | – | PG `wait_event_type`/`wait_event`; MySQL `wait/<type>/<event>`; SQL Server Query Store kategorisi / `wait_type`; yoksa = CPU'da |
| `openlog.db.duration_ms` | – | ✓ | – | çalışan statement (veya idle in transaction ise son statement'ından beri geçen süre) |
| `openlog.db.blocking_session_ids` | – | ✓ | – | string dizisi: bu session'ın beklediği kilitleri tutan session'lar |
| `openlog.db.application`, `client.address` | – | ✓ | – | |
| `openlog.db.plan.format`, `.plan.hash`, `.plan.cost` | – | – | ✓ | `json` veya `xml`; tahminler hariç plan'ın hash'i; kök maliyet |
| gövde | – | – | ✓ | plan dokümanı (≤ 512 KiB) |

İstatistikler **delta**'dır: ajan alınan her statement'ın önceki kümülatif sayaçlarını tutar
(`5 × top_n`, en az 200, kümülatif süreye göre en fazla 1000), (yeniden) başlatmadan sonraki ilk toplamayı ve call sayısı düşen her
statement'ı atar (reset, eviction, plan cache flush) ve aralığın en ağır `top_n` tanesini gönderir.

## 3. Ajan {#_3-agent}

### 3.1 PostgreSQL {#_3-1-postgresql}

`pg_stat_statements` (`shared_preload_libraries`, entegrasyonun veritabanında `CREATE EXTENSION`) ve `pg_monitor` yetkili bir rol gerekir.
Session'lar: `idle` olmayan `pg_stat_activity` client backend'leri, `pg_blocking_pids()`, PostgreSQL 14'ten itibaren `query_id`. Plan'lar:
PostgreSQL 16+ üzerinde `EXPLAIN (GENERIC_PLAN, FORMAT JSON)`; daha eski sunucular yalnızca `$n` parametresi olmayan statement'ları explain eder.
Yalnızca `;` içermeyen `SELECT`/`WITH`/`VALUES`/`TABLE`/`INSERT`/`UPDATE`/`DELETE`/`MERGE` statement'ları explain edilir, her biri
`BEGIN TRANSACTION READ ONLY` … `ROLLBACK` içinde ve en fazla 5 sn'lik `statement_timeout` ile. ANALYZE'sız EXPLAIN statement'ı asla çalıştırmaz.
Not: planner sabitler üzerindeki immutable fonksiyonları gerçekten değerlendirir; böyle fonksiyon oluşturabilen bir veritabanı kullanıcısı
bunları izleme rolüyle çalıştırabilir, bunun önemli olduğu yerde `explain`'i kapat.

### 3.2 MySQL / MariaDB {#_3-2-mysql-mariadb}

`performance_schema.events_statements_summary_by_digest` (statement digest'leri açık, MySQL 8 varsayılanı). Metin, kırpılmadıysa
`QUERY_SAMPLE_TEXT`'tir (MySQL 8.0.3+, gerçek bir çalıştırma: APM'in sakladığı şeye normalize olur), aksi halde `DIGEST_TEXT` (MariaDB).
Session'lar: `events_waits_current` ile `performance_schema.threads` (foreground, `Sleep` olmayan); blocking `data_lock_waits`'ten (MySQL 8).
Plan'lar: örneğin kendi şemasında `EXPLAIN FORMAT=JSON`; plan'daki her `*condition` string'i redakte edilir (MySQL literal değerleri orada tekrarlar).

### 3.3 SQL Server {#_3-3-sql-server}

`query_hash` ve veritabanı başına toplanmış `sys.dm_exec_query_stats`; `sys.dm_exec_requests` (+ session'lar, bağlantılar) ve başkalarını
bloke eden uyuyan session'lar (açık transaction'lar); `sys.dm_exec_query_plan`'dan en pahalı plan handle'ının cache'lenmiş showplan'ı,
`ParameterCompiledValue`/`ParameterRuntimeValue` çıkarılmış ve `ScalarString`/`StatementText`/`ConstValue` redakte edilmiş olarak. Yetki:
`VIEW SERVER STATE` (2022: `VIEW SERVER PERFORMANCE STATE`).

### 3.4 Redaksiyon {#_3-4-redaction}

Activity view'lerinden okunan statement metni uygulamaların gönderdiği değerleri taşır. Host'tan bir şey çıkmadan önce ajan
(`internal/integrations/sqlredact`) string, sayı, hex/bit ve dollar-quoted literal'leri `?` ile değiştirir, yorumları atar ve metni
4096 byte ile sınırlar; identifier'lar, keyword'ler ve bind placeholder'ları kalır. Ajan kanonik biçimi üretmez (§4.1).

## 4. Backend {#_4-backend}

### 4.1 Normalizasyon ve korelasyon {#_4-1-normalization-and-correlation}

Processor `db.query.text`'i `apm.NormalizeStatement` ile normalize eder (APM istemci span'lerinin `db.statement` değerine uygulanan
fonksiyon) ve sonucu `query_text` olarak, `fingerprint` = onun FNV-1a 64'ü ile saklar. Yani bir statement'ı çalıştıran servisler,
`db_statement_normalized = query_text` ve aynı `db_system` olan `apm_db_queries_1m` satırlarıdır: ayrı bir ikinci normalizer sapma yaratamaz.
Sınırlar: normalizasyondan sonra 2048 byte'tan uzun statement'lar kırpılmış önekleri üzerinden karşılaştırılır; MariaDB (örnek metin yok) yalnızca
digest'leri gibi backtick ile yazılmış statement'larla eşleşir.

### 4.2 Tablolar (şema `0096_db_monitoring`) {#_4-2-tables-schema-0096-db-monitoring}

`cityHash64(tenant_id, instance)` ile shard'lanır, güne göre partition'lanır. `db_query_stats` ve `db_query_plans` metrics depolama sınıfını
(30 gün), `db_session_samples` traces sınıfını (7 gün) izler; üçü de tenant başına retention ile kırpılır (D-081). Instance, system veya (stats,
plan'lar için) metni olmayan kayıtlar atılır (`invalid_db_*`), 512 KiB'ı aşan plan'lar da (`db_query_plan_too_large`).

## 5. API {#_5-api}

Hepsi `GET`, tenant kapsamlı, `from`/`to` her okuma gibi (varsayılan: son bir saat). `instance`,
`service.instance.id` değeridir.

| Endpoint | Cevap |
|---|---|
| `/api/v1/db/instances?sort=&dir=&limit=&offset=` | aralıkta statement istatistiği veya örneği olan instance'lar: system, host, çağrılar, throughput, toplam ve ortalama süre, statement'lar, hatalar, ortalama aktif session, en üstteki wait tipi |
| `/api/v1/db/queries?instance=&sort=time\|calls\|avg\|rows\|rows_per_call\|errors\|reads\|statement&dir=&db=&q=&limit=&offset=` | statement'lar (bir sayfa ≤ 1000, `has_more` ile): fingerprint, metin, çağrılar, throughput, toplam/ortalama süre, instance'ın süresindeki pay, satırlar (çağrı başına), incelenen satırlar, hatalar, index'siz çalıştırmalar, block'lar, cache hit oranı |
| `/api/v1/db/queries/{fingerprint}?instance=&step=` | tek statement: toplamlar, seriler (adım başına çağrılar, throughput, ortalama ve toplam süre, satırlar), plan'lar (en yenisi başta; `is_current`, `plan_change`), örneklerinin wait event'leri, çağıranlar (APM servisleri) |
| `/api/v1/db/activity?instance=&step=` | adım başına wait tipi başına ortalama aktif session (örnekler ÷ örnekleme anları), en üstteki wait event'ler, en çok örneği olan statement'lar |
| `/api/v1/db/activity/waits?instance=&sort=&dir=&limit=&offset=` | aralığın her wait event'i, sayfalı, instance'ın tüm örneklerindeki payıyla |
| `/api/v1/db/activity/queries?instance=&sort=&dir=&limit=&offset=` | örneklerin yakaladığı her statement, sayfalı |
| `/api/v1/db/sessions?instance=&at=` | `at` anında veya öncesindeki son örnekleme anı (ms, varsayılan şimdi, 5 dakika içinde): durum, wait, statement, `blocking_session_ids` ve `blocks` (geçişli olarak onu bekleyen session'lar) ile session'lar. Sayfalı değil, çünkü bir zincir tüm örneği ister: en fazla en uzun çalışan 1000 tanesi ve daha fazlası olduğunda `has_more` söyler |
| `/api/v1/db/lookup?db_system=&statement=` | istatistikleri normalize bir statement içeren instance'lar: bir APM veritabanı çağrısından bu görünüme bağlantı |
