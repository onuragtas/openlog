# Sözleşme: OQL sorgu dili (M3) {#contract-oql-query-language-m3}

OQL, openlog'un grafikler, özel dashboard'lar ([api.md](/tr/contracts/api#query-language-oql),
[api.md](/tr/contracts/api#dashboards)) ve `oql` alert kuralları ([alerting.md](/tr/contracts/alerting) §2.10) için NRQL benzeri sorgu dilidir. Kod: `internal/oql`
(lexer → parser → AST → doğrulanmış plan → `internal/api/query` üzerinden parametreli ClickHouse SQL). Karar: D-063.

```sql
SELECT count(*), percentile(duration.ms, 95) FROM Transaction
WHERE service.name = 'checkout' AND http.status_code >= 500
FACET transaction.name SINCE 3 hours ago TIMESERIES 5 minutes LIMIT 5 COMPARE WITH 1 day ago
```

## 1. Gramer {#_1-grammar}

```
query      := SELECT item (',' item)* FROM EventType clause*
clause     := WHERE cond | FACET attr (',' attr)* | SINCE time | UNTIL time
            | TIMESERIES [duration | AUTO] | LIMIT integer | COMPARE WITH duration AGO
item       := agg [AS (identifier | string)]
agg        := count '(' ('*' | attr) ')' | (sum | average | avg | min | max | uniqueCount | median
            | latest | earliest) '(' attr ')' | percentile '(' attr (',' number)+ ')'
            | rate '(' agg ',' duration ')' | filter '(' agg ',' WHERE cond ')'
            | histogram '(' attr ',' number [',' integer] ')'
cond       := and (OR and)*      and := not (AND not)*      not := NOT not | '(' cond ')' | predicate
predicate  := attr ('=' | '!=' | '<>' | '<' | '<=' | '>' | '>=') value
            | attr [NOT] IN '(' value (',' value)* ')' | attr [NOT] LIKE value | attr [NOT] CONTAINS value
            | attr IS [NOT] NULL
value      := string | number | true | false | variable
attr       := identifier | `backtick quoted` | attributes '[' string ']' | resource '[' string ']'
duration   := number unit            unit := second(s) | sec | minute(s) | min | hour(s) | day(s) | week(s)
time       := duration AGO | string (RFC3339 or 'YYYY-MM-DD HH:MM:SS', UTC) | integer (unix ms) | NOW
variable   := '{{' name '}}'
```

- Keyword'ler ve fonksiyon adları büyük/küçük harfe duyarsızdır; attribute adları duyarlıdır. `FROM`'dan sonraki clause'lar her sırada gelebilir,
  her biri en fazla bir kez. String'ler `\\`, `\'`, `\"`, `\n`, `\t` kaçışlarıyla `'…'` veya `"…"` kullanır (tek tırnaklı string içinde `''` bir
  tırnaktır). `--` ve `//` satır sonuna kadar yorum başlatır.
- Identifier'lar: `[A-Za-z_][A-Za-z0-9_.]*` veya backtick içermeyen, backtick ile tırnaklanmış herhangi bir metin.
- Varsayılanlar: `SINCE 1 hour ago`, `UNTIL now`, `LIMIT 10` (facet'ler), `TIMESERIES` = `TIMESERIES AUTO`.
- Her sonuç sütunu bir aggregate'tir (ham event listelemesi yok); `percentile(x, 50, 95)` düzey başına bir sütun verir.

## 2. Event tipleri ve attribute'lar {#_2-event-types-and-attributes}

| Event tipi | Tablo | Zaman sütunu | Notlar |
|---|---|---|---|
| `Log` | `logs` | `timestamp` | 14 gün retention |
| `Span` | `spans` | `timestamp` | 7 gün retention |
| `Transaction` | `spans` | `timestamp` | yalnızca entry span'ler (`is_entry` her zaman eklenir, kaldırılamaz) |
| `Metric` | `metrics` veya `metrics_1m` | `timestamp` | 1 dakikalık rollup otomatik seçilir (§4) |
| `Host` | `hosts FINAL` | `last_seen` | aralıkta görülen host'lar |
| `Container` | `containers` (container başına birleştirilmiş) | `last_seen` | aralıkta görülen container'lar |
| `Profile` | `profiles` | `timestamp` | 7 gün retention; profiling örneği başına bir satır, yani her sorgu aggregate yapar |
| `RumPageView` | `rum_page_views_1m` | `timestamp` | 30 gün retention; dakikalık rollup, her zaman aggregate |
| `RumVital` | `rum_vitals_1m` | `timestamp` | 30 gün retention; dakikalık rollup, her zaman aggregate |
| `RumSession` | `rum_sessions` | `last_seen` | 30 gün retention; aralıkta görülen oturumlar |
| `AnalyticsEvent` | `analytics_events` | `timestamp` | 90 gün retention; analytics'i açık anahtarların sayfa görüntülemeleri ve custom event'leri ([analytics.md](/tr/contracts/analytics)). `sum(weight)` tahmini (ekstrapole) sayıdır |
| `DbQuery` | `db_query_stats` | `timestamp` | toplama aralığı başına statement istatistikleri |

Attribute'lar (`GET /api/v1/query/schema` tipleriyle listeler):

| Event tipi | Attribute'lar (aksi belirtilmedikçe tip string) |
|---|---|
| `Log` | `service.name`, `host.id`, `host.name`, `severity` (= `severity.text`), `severity.number` (sayı), `message` (= `body`), `trace.id`, `span.id`, `event.name`, `scope.name`, `pattern.id`, `pattern.template` (= `pattern`) |
| `Span`, `Transaction` | `name` (Transaction: transaction adı; `span.name` = span adı), `kind`, `status.code`, `status.message`, `trace.id`, `span.id`, `parent.id`, `service.name` (= `appName`), `service.namespace`, `deployment.environment`, `host.id`, `duration` (sayı, saniye), `duration.ms` (sayı), `transaction.name`, `transaction.type`, `entry` (bool), `error` (bool), `http.status_code` (sayı), `db.system`, `db.name`, `db.operation`, `db.statement`, `peer.type`, `peer.name`, `error.type`, `error.message`, `sample.weight` (sayı), `scope.name` |
| `Metric` | `metricName` (= `metric.name`), `metric.type`, `unit`, `service.name`, `host.id`, `host.name`\*, `value` (sayı), `count`\* (sayı), `sum`\* (sayı), `scope.name`\* |
| `Host` | `host.id`, `host.name`, `os.type`, `os.description`, `arch`, `agent.name`, `agent.version` |
| `Container` | `container.id`, `container.name`, `host.id`, `host.name`, `image.name`, `image.tags`, `runtime`, `compose.project`, `compose.service`, `k8s.pod.name`, `k8s.namespace.name`, `k8s.container.name`, `state`, `health`, `restarts` (sayı) |
| `Profile` | `service.name`, `service.namespace`, `deployment.environment`, `host.id`, `profile.type`, `unit`, `function` (= `leaf`, self time'ın yazıldığı en içteki frame), `stack` (`main;handleRequest;db.Query` biçiminde katlanmış tüm stack), `stack.depth` (sayı), `value` (sayı, `unit` cinsinden), `profile.duration` (sayı, saniye) |
| `RumPageView` | `app`, `deployment.environment`, `route`, `page.kind` (`load` veya `route_change`), `device.type`, `browser.name` ve sayılar `views`, `samples`, `duration.sum.ms`, `duration.max.ms`, `ttfb.sum.ms`, `dns.sum.ms`, `connect.sum.ms`, `tls.sum.ms`, `response.sum.ms`, `dom.interactive.sum.ms`, `dom.content_loaded.sum.ms`, `load.event.sum.ms` |
| `RumVital` | `app`, `deployment.environment`, `vital` (`lcp`, `inp`, `cls`, `fcp`, `ttfb`), `route`, `device.type` ve sayılar `count`, `samples`, `value.sum`, `value.max`, `good`, `needs_improvement`, `poor` |
| `RumSession` | `app`, `deployment.environment`, `session.id`, `device.type`, `browser.name`, `browser.version`, `os.name` ve sayılar `page.views`, `errors`, `spans` |
| `AnalyticsEvent` | `app`, `deployment.environment`, `kind` (`page_view` veya `custom`), `name`, `route`, `session.id`, `version`, `country`, `device.type`, `os.name`, `browser.name`, `referrer.host`, `utm.source`, `utm.medium`, `utm.campaign` ve sayı `weight` |
| `DbQuery` | `db.system`, `instance`, `db.name`, `db.user`, `host.id`, `host.name`, `server.address`, `query.id`, `query.text`, `fingerprint` ve sayılar `server.port`, `calls`, `total_time.ms`, `rows`, `rows_examined`, `errors`, `no_index_used`, `blocks.hit`, `blocks.read`, `interval.seconds` |

Rollup tipleri (`RumPageView`, `RumVital`, `RumSession`, `DbQuery`) ortalama değil ağırlıklı toplam saklar: ortalama
`sum(duration.sum.ms) / sum(views)` olur. Saklanan bir "ortalama" sütunu, facet'lendiği anda yanlış olurdu. Histogram sütunları dizi
tuple'larıdır ve bilerek açılmamıştır: OQL'de dizi tipi yok ve içerdikleri percentile'ları, bucket ölçeğini bilen RUM API'si sunar.

\* yalnızca ham veri noktaları (rollup'ı engeller).

Map'ler: `attributes['k']` (Log, Span, Transaction, Metric, Container, Profile) ve `resource['k']` veya `resource.k` (Log, Span,
Transaction, Metric\*, Host, Profile). Rollup tipleri ikisini de taşımaz. `attributes` olan bir event tipinde başka herhangi bir identifier `attributes['<identifier>']` olarak okunur
(örneğin `http.route`); doğrulama bunun için uyarı verir. `tenant_id` ve `_` ile başlayan isimler reddedilir.
Map değerleri string'dir: bir sayıyla karşılaştırıldığında veya `sum`/`average`/`min`/`max`/`percentile`/`histogram` içinde kullanıldığında
`toFloat64OrNull` ile çevrilir (sayısal olmayan değerler yok sayılır).

**Metrik exemplar'ları OQL attribute'u değildir** (D-130). Exemplar'lar (bir veri noktasının geldiği trace) kendi tablolarında
(`metric_exemplars`, şema 0092) traces retention'ıyla yaşar ve bir event tipi tam olarak tek tablo okur: OQL'in onlara ulaşacak join'i yok ve
onları metrik satırına geri koymak, o tablonun kaçınmak için var olduğu tasarımdır. Ulaşılabilir olsalar bile burada kullanılamazlardı, çünkü
her OQL sonuç sütunu bir aggregate'tir (ham event listelemesi yok), yani trace id ancak `latest(...)` veya `uniqueCount(...)` olarak dönebilir;
tıklayıp geçmenin ihtiyaç duyduğu link listesi olarak asla. Metrik → trace gezinmesi için `POST /api/v1/metrics/exemplars` ([api.md](/tr/contracts/api#metrics)) kullan.

## 3. Semantik {#_3-semantics}

**Predicate'ler.** String attribute'lar string değer alır (sayılar metin olarak karşılaştırılır); sayı attribute'ları sayı alır (sayısal bir
string kabul edilir); bool attribute'lar `true`/`false` alır. `LIKE` string'lerde SQL joker karakterlerini kullanır (`%`, `_`, büyük/küçük harfe
duyarlı). `CONTAINS` string'lerde büyük/küçük harfe duyarsız alt metin eşleşmesidir ve içinde `%`, `_` ile `\` sıradan karakterdir; Logs/Metrics/Traces
explorer'larındaki `contains` ile aynı koşuldur (D-122; `CONTAINS` rezerve değildir, yani `contains` adlı attribute yine çalışır). `IS NULL`: map anahtarı yok, string attribute boş; sayı/bool attribute'lar için asla doğru değildir.

**Fonksiyonlar.**

| Fonksiyon | Sonuç |
|---|---|
| `count(*)`, `count(attr)` | event'ler; map attribute'uyla: anahtara sahip event'ler |
| `sum`, `average`/`avg`, `min`, `max` | sayı attribute'ları üzerinde (map değerleri çevrilir) |
| `uniqueCount(attr)` | birebir farklı değerler |
| `percentile(attr, p…)`, `median(attr)` | düzey başına quantile (0 < p < 100); grup başına ~8k değerin üzerinde yaklaşık |
| `latest(attr)`, `earliest(attr)` | en yeni/en eski event'in değeri (string'lere izin var) |
| `rate(agg, duration)` | `duration` başına ölçeklenmiş `agg` (`TIMESERIES` ile bucket genişliği başına, aksi halde sorgu aralığı başına) |
| `filter(agg, WHERE cond)` | `cond` ile eşleşen event'ler üzerinde `agg` (`rate`, `filter`, `histogram` hariç her aggregate) |
| `histogram(attr, ceiling, buckets=40)` | `[0, ceiling)` üzerinde `buckets` eşit bucket'taki event sayıları (1–200 bucket); tek item olmalı; `FACET`, `TIMESERIES` veya `COMPARE WITH` yok |

**FACET** en fazla 5 attribute'a göre gruplar (değerler string olarak verilir). Gruplar ilk sütuna göre azalan sıralanır;
`LIMIT` ilk *n* tanesini tutar (varsayılan 10, en fazla 2000; `TIMESERIES` ile en fazla 50). `TIMESERIES` ile en üst gruplar tüm aralık üzerinden
seçilir, sonra bucket'lanır.

**TIMESERIES** bucket'ları unix epoch'a hizalıdır. `AUTO`, 10s, 30s, 1m, 5m, 10m, 15m, 30m, 1h, 3h, 6h, 12h, 1d, 7d içinden en fazla 300 bucket
veren en küçüğü seçer (rollup'ta en az 1m). Açıkça verilen bucket en fazla 1000 bucket vermeli ve ≥ 10s olmalı (rollup: ≥ 1m tam dakika). Eksik bucket'lar
`count`, `sum`, `uniqueCount` ve `rate` için `0`, aksi halde `null` olur.

**COMPARE WITH** sorguyu, süre kadar geriye kaydırılmış aralık üzerinde yeniden çalıştırır (1 dakika – 400 gün); sonuç bunu `compare` içinde taşır,
timeseries noktaları ikisi üst üste binsin diye ileri kaydırılır.

**Variable'lar.** `{{name}}` bir değerin yerini tutar; istek `variables: {"name": "v" | ["v1", "v2"]}` verir. Değerler literal gibi bind edilir (asla
SQL'e yapıştırılmaz). Birden fazla değerle `=`, `IN` olur ve `!=`, `NOT IN` olur. Variable'ı eksik, boş veya `*` olan predicate `true` olur (filtre yok).
İsimler: `[A-Za-z_][A-Za-z0-9_]{0,63}`.

## 4. Metrik rollup'ı {#_4-metric-rollup}

`FROM Metric`, şunların **hepsi** sağlandığında `metrics_1m` okur: aralık 6 saatten uzun; her attribute şunlardan biri:
`metricName`, `metric.type`, `unit`, `service.name`, `host.id`, `attributes[…]`; her aggregate şunlardan biri: `count(*)` (`value_count` toplamı),
`sum(value)`, `average(value)` (`sum(value_sum)/sum(value_count)`), `min(value)`, `max(value)`,
`latest(value)` (`argMaxMerge(value_last)`), izin verilen bir attribute'un `uniqueCount`'u ve bunların `rate`/`filter`'ı
(`latest` `filter` olmadan); bucket ≥ 1 dakika. Rollup satırları dakikalıktır, yani aralık kenarları dakikaya hizalanır.
Aksi halde ham `metrics` okunur; ham aralıklar 31 günle sınırlıdır (hangi tablonun kullanıldığını `metadata.rollup` söyler).
`metrics_1m` yalnızca gauge ve sum içerir (histogram/summary yok). Rollup satırları seri ve dakika başına bir attribute kümesi tutar;
`attributes[…]` üzerindeki filtre ve facet'ler birebirdir, çünkü bir seri (`series_id`) attribute'larıyla tanımlanır.

## 5. Sınırlar ve güvenlik {#_5-limits-and-security}

- Sorgu metni ≤ 8 KiB; ≤ 20 select item (percentile açıldıktan sonra ≤ 30 sütun); ≤ 5 facet; `IN` başına ≤ 500 değer;
  ≤ 100 predicate; iç içelik derinliği ≤ 20; string'ler ≤ 4096 byte; map anahtarları kontrol karakteri olmadan 1–256 byte.
- Aralık: `SINCE` < `UNTIL`, en fazla 31 gün (rollup üzerinde Metric: 400 gün), 400 günden eski değil; seri ≤ 500
  (facet × sütun), nokta ≤ 100 000.
- Parse işlemi kullanıcı girdisinden asla SQL metni üretmez: identifier'lar yukarıdaki event tipi başına whitelist'ten gelir, her literal, map
  anahtarı, variable değeri ve zaman bind edilmiş bir sorgu parametresidir (`{pN:Type}`) ve SQL, çağıranın organizasyonu için her zaman
  `tenant_id = {tenant_id:String}` ekleyen (sorgu tablo, `tenant_id` veya alt sorgu belirtemez) ve her parçada yasak token'ları reddeden
  `internal/api/query` ile oluşturulur.
- Sorgular salt okunur ClickHouse kullanıcısıyla, organizasyonun sınırlarıyla çalışır (`max_memory_usage`, `max_rows_to_read`,
  `max_execution_time`, `quota_key`, `log_comment`; D-047). Sınır hataları `422`/`429`'a, zaman aşımları `504`'e eşlenir.

## 6. Hatalar {#_6-errors}

Söz dizimi ve doğrulama hataları, sorunlu token'ın byte offset'ini, uzunluğunu, 1 tabanlı satır ve sütununu taşır
(`POST /api/v1/query/validate`). `POST /api/v1/query`, mesajın başında
`line L, column C: ` olacak şekilde `400 invalid_argument` döner.

## 7. Dashboard filtreleri {#_7-dashboard-filters}

`POST /api/v1/query` ve `/query/validate`, dashboard'ların widget'lar arası filtreleri olan (api.md "Dashboards" › "Cross-widget filters")
`"filters": [{"attribute", "value", "event_type"?}]` alanını kabul eder (≤ 10). `attribute`, sorgudaki bir attribute gibi parse edilir
(`host.name`, `` `quoted` ``, `attributes['k']`, `resource['k']`, `resource.k`; parse edilemezse `400`); `value` kontrol karakteri olmadan
≤ 4096 byte'tır. Uygulanabilir her filtre `attribute = value` predicate'i olur ve planlamadan önce sorgunun `WHERE`'i ile AND'lenir: değer, her literal ve
variable değeri gibi bind edilmiş bir parametredir; rollup uygunluğu, facet sınırları ve zaman aralığı sınırları predicate sorguda yazılmış gibi uygulanır. Bir filtre şu durumlarda uygulanır:
- attribute, sorgunun event tipinin bilinen bir attribute'uysa (sayı attribute'ları sayısal değer, bool'lar
  `true`/`false`/`1`/`0` ister) veya
- açık bir `attributes[...]` / `resource[...]` araması ise ve event tipinde o map varsa veya
- bilinmeyen bir isimse (örtük `attributes['name']` araması) ve `event_type` sorgunun event tipiyse.

Aksi halde filtre atlanır ve attribute'u `metadata.ignored_filters` içinde listelenir (boşsa çıkarılır). `tenant_id`
ve `_` ile başlayan isimler asla uygulanmaz.
