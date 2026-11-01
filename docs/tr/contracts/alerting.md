# Sözleşme: Alerting (M2, D-030) {#contract-alerting-m2-d-030}

Kurallar tek bir organizasyonun telemetrisini değerlendirir, **incident** açar ve **bildirimleri** Slack'e, e-postaya
(SMTP), genel webhook'lara (HMAC imzalı), Microsoft Teams'e, PagerDuty'ye ve Opsgenie'ye iletir; bir incident'ın hangi
kanallara ulaşacağına **routing kuralları** (§5.6) karar verir. Plan: [10-m2.md §1](https://github.com/onuragtas/openlog/blob/master/docs/plan/10-m2.md), cluster kuralı:
[04-cluster.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/04-cluster.md) (Alert satırı). HTTP API: [api.md](/tr/contracts/api#alerting) /
[openapi.yaml](https://github.com/onuragtas/openlog/blob/master/docs/contracts/openapi.yaml) (`alerts` tag'i). Tablolar: [postgres.md](/tr/contracts/postgres#alerting-0004-alerting-0088-alert-routing).
Yapılandırma: [config.md](/tr/contracts/config#openlog-alert). Kod: `internal/alert`, `cmd/openlog-alert`, `internal/api/alerts.go`.

## 1. Bileşenler {#_1-components}

| Bileşen | Nerede çalışır | Ne yapar |
|---|---|---|
| Rule/channel/mute/incident API | `openlog-api` (her pod) | CRUD, önizleme, test gönderimi, incident aksiyonları. Kanal secret'larını şifreler |
| Evaluator | `openlog-alert` (N replika) veya `openlog-allinone` (`OPENLOG_ALERT_ENABLED=true`) | Kuralları lease'ler, takvime göre değerlendirir, durum geçişlerini, incident'ları ve outbox satırlarını **tek bir transaction**'da yazar |
| Dispatcher | evaluator ile aynı süreçler | Outbox satırlarını üstlenir (`FOR UPDATE SKIP LOCKED`), iletir, yeniden dener, teslimat logunu yazar |

Tüm durum PostgreSQL'dedir (D-006): bir `openlog-alert` pod'u her an öldürülebilir. ClickHouse'daki telemetri yalnızca
okunur, o da yalnızca tenant kapsamlı sorgu katmanından (`internal/api/query`): evaluator bir `query.Scope`'u, kuralın
organizasyonunun `organizations.tenant_id` değerinden kurar (asla kural içeriğinden); her filtre değeri ve attribute
anahtarı bind edilmiş bir sorgu parametresidir. ClickHouse'a yapılan tek yazma değerlendirme geçmişidir (§3.6), o da değerlendirme için gerekli değildir.

## 2. Kurallar {#_2-rules}

### 2.1 Ortak alanlar {#_2-1-common-fields}

| Alan | Tür | Varsayılan | Notlar |
|---|---|---|---|
| `name` | string | zorunlu | 1–200 karakter |
| `description` | string | `""` | ≤ 2000 karakter |
| `type` | enum | zorunlu | `metric_threshold`, `log_match`, `no_data`, `discovery`, `apm` (§2.6), `apm_no_data` (§2.7), `apm_error` (§2.9), `oql` (§2.10), `slo_burn` (§2.11), `anomaly` (§2.12) |
| `severity` | enum | `warning` | `critical`, `warning`, `info` |
| `enabled` | bool | `true` | |
| `interval_seconds` | int | `60` | 10–3600, değerlendirme periyodu |
| `for_seconds` | int | `0` | 0–86400, koşul tetiklenmeden önce bu kadar süre sağlanmalıdır (`pending`). `discovery` için yok sayılır |
| `recovery_for_seconds` | int | `0` | 0–86400, incident çözülmeden önce toparlanma koşulu bu kadar süre sağlanmalıdır |
| `condition` | object | zorunlu | türe göre, §2.2–2.6 |
| `channel_ids` | uuid[] | `[]` | aynı organizasyondan ≤ 20 kanal |
| `renotify_interval_seconds` | int | `0` | 0 = kapalı, aksi halde 300–604800: incident açık ve onaylanmamışken bildirimi tekrarla |
| `flapping` | object | `{"enabled": true, "transitions": 4, "window_seconds": 3600, "hold_seconds": 600}` | §3.4 |
| `runbook_url` | string | `""` | `http(s)` URL'i, ≤ 2000 karakter |
| `labels` | map | `{}` | ≤ 20 çift; anahtarlar `^[a-zA-Z0-9_.-]{1,64}$`, değerler ≤ 256 karakter; incident label'larına eklenir |

`version` (salt okunur) her değişiklikte artar. Doğrulama hataları → alan yoluyla birlikte `400 invalid_argument`
(`condition.threshold: required`).

**Filtreler** (`filters`, ≤ 20, AND ile bağlanır): `{"field": F, "op": OP, "values": ["…"]}`.
`F` ∈ `host.id`, `host.name`, `service.name`, `attr.<key>` (data point / log kaydı attribute'u), `resource.<key>`
(resource attribute'u, örneğin `resource.env`). `OP` ∈ `eq`, `neq` (tam olarak bir değer), `in`, `not_in` (1–100 değer),
`contains` (tek değer, büyük/küçük harf duyarsız alt dizgi). Değerler ≤ 1024 bayt, anahtarlar `^[A-Za-z0-9_.\-/]{1,128}$`.

**Group by** (`group_by`, ≤ 5): `host` (label'lar `host.id`, `host.name`), `service` (`service.name`), `attr.<key>`,
`resource.<key>`. Her farklı grup ayrı bir **seridir** ("çok boyutlu"). Boş = eşleşen her şey için tek seri.
Değerlendirme başına en fazla `OPENLOG_ALERT_MAX_SERIES_PER_RULE` (1000) seri; daha fazlası → değerlendirme `error`
(`too many series`), durum değişmez.

**Hedef seçimi** filtrelerle ifade edilir: tek bir host (`host.id eq …`), bir tag (`resource.env eq prod`) veya tüm host'lar
(host filtresi yok); host başına bir seri için `group_by: ["host"]` ile birleştirilir.

### 2.2 `metric_threshold` {#_2-2-metric-threshold}

```json
{"metric": "system.cpu.utilization", "aggregation": "avg", "series_aggregation": "sum",
 "window_seconds": 60, "filters": [{"field": "attr.cpu.mode", "op": "not_in", "values": ["idle"]}],
 "group_by": ["host"], "operator": "gt", "threshold": 0.9, "recovery_threshold": 0.8, "missing_data": "keep"}
```

| Alan | Varsayılan | Notlar |
|---|---|---|
| `metric` | zorunlu | metrik adı (gauge'lar ve sum'lar; histogram'lar `value` sütunlarının yalnızca `count`/`sum` değerini kullanır) |
| `aggregation` | `avg` | pencere boyunca alttaki her zaman serisi için: `avg`, `min`, `max`, `sum`, `last`, `count`, `rate`, `p50`, `p95`, `p99` |
| `series_aggregation` | `avg` (`rate`, `count` için `sum`) | tek bir grubun zaman serileri arasında: `avg`, `sum`, `min`, `max`. Persentiller için yok sayılır |
| `window_seconds` | `300` | 10–21600 |
| `operator` | zorunlu | `gt`, `gte`, `lt`, `lte` |
| `threshold` | zorunlu | sonlu sayı |
| `recovery_threshold` | `null` (= `threshold`) | histerezis; ihlal tarafında olmamalıdır (`gt/gte`: ≤ threshold, `lt/lte`: ≥ threshold) |
| `missing_data` | `keep` | §3.3 |

`[end − window, end)` penceresi üzerinde toplama matematiği (ham `openlog.metrics`):
- `avg` = Σvalue / count, `min`, `max`, `sum`, `count`, `last` = en büyük timestamp'e sahip değer, zaman serisi başına
  (`series_id`), ardından grubun serileri arasında `series_aggregation`.
- `rate` (saniye başına): kümülatif monoton sum'lar: ardışık pozitif farkların toplamı (counter sıfırlanması 0 sayılır)
  bölü pencere; delta sum'lar: Σvalue / pencere. Ardından `series_aggregation` (varsayılan `sum`).
- `p50`/`p95`/`p99`: grubun tüm ham noktaları üzerinde `quantile` (seri başına değil).

### 2.3 `log_match` {#_2-3-log-match}

```json
{"query": "timeout", "severity_min": "ERROR", "filters": [], "group_by": ["host"],
 "window_seconds": 300, "operator": "gte", "threshold": 5, "recovery_threshold": null}
```
Değer = pencerede gövdesi `query`'yi içeren (büyük/küçük harf duyarsız; boş = herhangi biri) ve
`severity_number ≥ severity_min` olan (OTel adları veya bir sayı; boş = herhangi biri) log kayıtlarının sayısı. Envanter olayları,
logs API'deki gibi hariç tutulur. Eşleşen kaydı olmayan bir grubun değeri `0`'dır (böylece `lt` koşulları çalışır ve tetiklenmiş gruplar toparlanır).

### 2.4 `no_data` {#_2-4-no-data}

```json
{"signal": "host", "metric": "", "filters": [{"field": "resource.env", "op": "eq", "values": ["prod"]}],
 "group_by": ["host"], "window_seconds": 300, "lookback_seconds": 86400}
```
- `signal`: `host` (herhangi bir telemetri: `openlog.hosts.last_seen`), `metric` (`metric` gerektirir), `log`.
- `group_by` zorunludur (`host` veya `service`; `signal=host` için yalnızca `host`).
- Beklenen seriler = `lookback_seconds` (600–604800, > pencere) içinde verisi olan gruplar. Değer = son data point'ten beri geçen
  saniye. Değer ≥ `window_seconds` (60–86400) olduğunda ihlal sayılır. Veri yeniden geldiğinde toparlanır. Lookback'ten eski bir seri
  ortadan kalkar ve incident'ı `expired` sebebiyle çözülür.

### 2.5 `discovery` {#_2-5-discovery}

```json
{"event": "service_disappeared", "filters": [{"field": "host.name", "op": "eq", "values": ["web-1"]}],
 "match": "redis", "window_seconds": 900, "lookback_seconds": 86400}
```
Envanter öğelerini kullanır (semantic-conventions §3). `event`:
- `service_disappeared`: bir host'ta `(end − lookback, end − window]` aralığında var olan bir `discovered_service` anahtarı
  (`<rule_id>:<instance>`), o host'un `(end − window, end]` aralığındaki her snapshot'ında yoktur ve host bu aralıkta
  gerçekten bir snapshot raporlamıştır (sessiz bir host `no_data` vakasıdır). Servis yeniden görüldüğünde veya anahtar lookback'ten eski
  olduğunda (`expired`) çözülür.
- `port_opened`: `(end − window, end]` aralığında var olan bir `listening_port` anahtarı, host o sırada snapshot raporlamış olmasına rağmen
  `(end − lookback, end − window]` aralığında yoktu (yeni host'lar tetiklemez). Anahtar bir pencereden eski olduğunda çözülür
  (değişiklik artık yeni değildir).

`match` (isteğe bağlı), öğe anahtarında büyük/küçük harf duyarsız bir alt dizgidir. `window_seconds` 300–86400 aralığındadır ve ajanın
envanter aralığından ≥ olmalıdır; `lookback_seconds` > pencere, ≤ 604800. Seri label'ları: `host.id`, `host.name`, `service` veya `port`.
Olay koşulu sağlandığı sürece değer = 1. `for_seconds` yok sayılır; bu tür için `interval_seconds` varsayılanı 300'dür.

### 2.6 `apm` {#_2-6-apm}

[apm.md](/tr/contracts/apm) §4/§10'daki APM rollup'ı üzerinde koşullar (`apm_transactions_1m`, sorgu katmanından okunur):
```json
{"service_name": "checkout", "service_namespace": null, "environment": "prod", "transaction_type": "",
 "transaction_name": "", "metric": "p95_ms", "group_by": ["transaction"], "window_seconds": 300,
 "min_requests": 10, "operator": "gt", "threshold": 800, "recovery_threshold": 600, "missing_data": "keep"}
```
| Alan | Varsayılan | Notlar |
|---|---|---|
| `service_name` | zorunlu | birebir `service.name` |
| `service_namespace`, `environment` | `null` | `null` = tümü (toplanmış), string (`""` dahil) = birebir eşleşme (apm.md §1) |
| `transaction_type`, `transaction_name` | `""` | `""` = tüm transaction'lar |
| `metric` | zorunlu | `throughput` (istek/dk), `error_rate` (0..1), `errors`, `avg_ms`, `p50_ms`, `p95_ms`, `p99_ms`, `apdex` (apm.md §4 tanımları, ağırlıklı, histogram quantile'ları) |
| `group_by` | `[]` | `environment`, `transaction` (label'lar `environment`, `transaction.type`, `transaction.name`) |
| `window_seconds` | `300` | 60–21600, tam dakikaya yukarı yuvarlanır; pencereler son tamamlanmış dakikada biter |
| `min_requests` | `0` | penceredeki (ağırlıklı) isteği bundan az olan serinin değeri olmaz (eksik veri), bir avuç istek üzerine alarm çıkmasını önler |
| `operator`, `threshold`, `recovery_threshold`, `missing_data` | | §2.2'deki gibi |

Apdex, servisin yapılandırılmış T değerini kullanır (`apm_service_settings`, önce birebir namespace/environment, sonra servis wildcard'ı,
yoksa `OPENLOG_APM_DEFAULT_APDEX_T`); bu değer değerlendirme anında seri başına çözülür. İstek yoksa `error_rate`, gecikme ve
Apdex'in değeri olmaz; `throughput` ve `errors` 0 olur. Seri label'ları her zaman `service.name` içerir.

### 2.7 `apm_no_data` {#_2-7-apm-no-data}

Bir servis transaction raporlamayı bırakmıştır (§2.4'ün no-data semantiği, APM rollup'ı `apm_transactions_1m` üzerinde):
```json
{"service_name": "checkout", "service_namespace": null, "environment": "prod", "group_by": [],
 "window_seconds": 600, "lookback_seconds": 86400}
```
| Alan | Varsayılan | Notlar |
|---|---|---|
| `service_name` | `""` | birebir `service.name`; `""` = her servis (servis başına bir seri) |
| `service_namespace`, `environment` | `null` | `null` = tümü, string (`""` dahil) = birebir eşleşme (apm.md §1) |
| `group_by` | `[]` | `namespace`, `environment`: ek seri boyutları (label'lar `service.namespace`, `environment`) |
| `window_seconds` | `600` | 60–86400, tam dakikaya yukarı yuvarlanır |
| `lookback_seconds` | `86400` | 600–604800, > pencere, tam dakikaya yukarı yuvarlanır |

- Beklenen seriler = lookback içinde en az bir transaction dakikası olan servisler (grup başına). Değer = transaction'lı son
  dakikanın sonundan pencere bitişinden önceki son tamamlanmış dakikanın sonuna kadar geçen saniye
  (son tamamlanmış dakikada raporlamış bir servisin değeri 0'dır). Değer ≥ `window_seconds` olduğunda ihlal sayılır;
  transaction'lar yeniden geldiğinde toparlanır; lookback'ten eski bir seri `expired` olarak çözülür.
- Seri label'ları: `service.name`, ayrıca gruplandığında veya koşulla sabitlendiğinde `service.namespace`/`environment`. Seri
  anahtarı: `service.name=…[|service.namespace=…][|environment=…]`.
- `for_seconds` geçerlidir; `interval_seconds` varsayılanı 60. `sample_weight = 0` olan entry span'ler sayılmaz (apm.md §4).

### 2.9 `apm_error` {#_2-9-apm-error}

Hata gelen kutusu üzerinde olay kuralı ([apm.md](/tr/contracts/apm) §3.4): **yeni bir hata grubu** belirdi veya çözülmüş bir grup **regresyona girdi**.
```json
{"event": "new_group", "service_name": "checkout", "service_namespace": null, "environment": "prod",
 "match": "timeout", "window_seconds": 300, "min_count": 5}
```
| Alan | Varsayılan | Notlar |
|---|---|---|
| `event` | zorunlu | `new_group` · `regressed` |
| `service_name` | `""` | birebir `service.name`; `""` = her servis |
| `service_namespace`, `environment` | `null` | `null` = tümü, string (`""` dahil) = birebir eşleşme |
| `match` | `""` | hata türünde veya normalize edilmiş mesajda büyük/küçük harf duyarsız alt dizgi |
| `window_seconds` | `300` | 60–86400, tam dakikaya yukarı yuvarlanır |
| `min_count` | `0` | yalnızca `new_group`: pencere içinde en az bu kadar ağırlıklı oluşum (tek bir oluşumda alarm çıkmasını önler); `regressed` ile 400 |

- `new_group`: ilk oluşumu `[end − window, end)` içinde olan gruplar (`apm_error_groups`), ignored gruplar hariç.
  `regressed`: önce kapsamdaki çözülmüş gruplar üzerinde regresyon tespiti çalıştırır (onları yeniden açar, apm.md §3.4),
  sonra `regressed_at` değeri pencere içinde olan grupları raporlar. PostgreSQL gerektirir (yoksa değerlendirme hatası).
- Grup başına bir seri: anahtar `error.group_id=<16 hex>`; label'lar `service.name`, `service.namespace`, `environment`,
  `error.group_id`, `error.type`, `error.message`. Olay penceredeyken değer 1; sonra seri
  ortadan kalkar ve `expired` olarak çözülür (`discovery` gibi). `for_seconds` yok sayılır; `interval_seconds` varsayılanı 60.
- Önizleme (`Range`) yaklaşıktır: `new_group`, `min_count` olmadan ilk görülme zamanlarını kullanır; `regressed` kayıtlı
  `regressed_at` değerini kullanır (tespit yapılmaz).
- Özet: `new error group in checkout (prod): TimeoutError: upstream timed out after <n>ms` /
  `error group regressed in …`.

### 2.10 `oql` {#_2-10-oql}

Bir OQL sorgusu üzerinde eşik ([oql.md](/tr/contracts/oql), D-065; `0021_alert_oql`):
```json
{"query": "SELECT percentile(duration.ms, 95) FROM Transaction WHERE service.name = 'checkout' FACET transaction.name",
 "window_seconds": 300, "operator": "gt", "threshold": 800, "recovery_threshold": 600, "missing_data": "keep"}
```
| Alan | Varsayılan | Notlar |
|---|---|---|
| `query` | zorunlu | tam olarak bir sayı sütunu; `TIMESERIES`, `SINCE`/`UNTIL`, `COMPARE WITH`, `histogram` veya `{{variables}}` olamaz (konumla birlikte `400`) |
| `window_seconds` | `300` | 60–21600; sorgu aralığı `[end − window, end)` |
| `operator`, `threshold`, `recovery_threshold`, `missing_data` | | §2.2'deki gibi |

- `FACET` olmadan tek seri (anahtar `*`); `FACET` ile grup başına bir seri, label'lar = facet attribute adları (yazıldığı gibi) →
  değerler, anahtar `name=value|…`. `LIMIT` olmadan en fazla `OPENLOG_ALERT_MAX_SERIES_PER_RULE` grup okunur (daha fazlası →
  değerlendirme hatası `too many series`); `LIMIT` değere göre en üstteki grupları tutar.
- Her zaman ham veri okunur (Metric rollup'ı yok). Olayı olmayan bir grubun değeri olmaz (eksik veri); istisnası `FACET`'siz `count`,
  `sum`, `uniqueCount` ve `rate`'tir, onlar `0` olur.
- Önizleme (`Range`) kesindir: tek sorgu her olayı onu içeren her pencereye atar (`arrayJoin`), böylece pencereler
  üst üste binebilir; `window_seconds / step` en fazla 720 olmalıdır.
- Özet: `<column> over 5m is 912 (> 800) (transaction.name=GET /cart)`. `interval_seconds` varsayılanı 60.

### 2.11 `slo_burn` {#_2-11-slo-burn}

Tek bir SLO'nun error budget'ı üzerinde çok pencereli, çok burn rate'li alarm ([slo.md](/tr/contracts/slo) §3, D-125;
`0087_slo`):
```json
{"slo_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
 "windows": [{"name": "fast", "factor": 14.4, "long_seconds": 3600, "short_seconds": 300},
             {"name": "slow", "factor": 6, "long_seconds": 21600, "short_seconds": 1800}],
 "min_requests": 0}
```
| Alan | Varsayılan | Notlar |
|---|---|---|
| `slo_id` | zorunlu | organizasyona ait bir SLO (api.md [Service level objectives](/tr/contracts/api#service-level-objectives)) |
| `windows` | yukarıdaki ikisi | 1–4 çift; `name` `^[a-z0-9_-]{1,32}$` (benzersiz), `factor` > 0 ve ≤ 1000, `long_seconds` 300–86400, `short_seconds` 60–`long_seconds` (varsayılan `long/12`), ikisi de tam dakikaya yukarı yuvarlanır |
| `min_requests` | `0` | uzun penceresinde (ağırlıklı) isteği bundan az olan pencerenin değeri olmaz |

- Değer = `max over the windows of min(burn rate over long, burn rate over short) / factor`, yani karşılaştırma
  `gte 1` olarak sabittir: yapılandırmayı faktörler taşır ve 1 "yapılandırılan hızda yakıyor" demektir.
  Burn rate, pencere boyunca `(bad / requests) / (1 − objective)` değeridir (slo.md §2); `apm_transactions_1m`'den,
  SLO'nun SLI'ı ile hesaplanır (availability veya süre histogramından latency).
- **İki** pencerenin de istenmesi, çoktan durmuş bir patlamanın tetiklememesi ve düzeltilmiş bir incident'ın kısa pencere içinde
  toparlanması demektir. Pencereler son tamamlanmış dakikada biter.
- Kural başına bir seri, anahtar `slo.id=<uuid>`; label'lar `slo.id`, `slo.name`, `slo.window` (değeri üreten
  pencere), `service.name` ve SLO onları sabitliyorsa `service.namespace`/`environment`.
- Değer yoksa (hiçbir pencerede istek yok veya hepsi `min_requests`'in altında) eksik veri davranışı
  `keep`'tir. `for_seconds` geçerlidir; `interval_seconds` varsayılanı 60.
- Tanım için PostgreSQL gerekir: o olmadan (veya SLO silindikten sonra) değerlendirme hatadır,
  durum değişmez. Bir SLO'yu silmek kurallarını silmez.
- Özet: `error budget of Checkout availability burns 21.6× too fast over 1h and 5m (≥ 14.4×)`.

### 2.12 `anomaly` {#_2-12-anomaly}

Bir **baseline** koşulu (D-129, `0089_alert_anomaly`): sinyal sabit bir sayıyla değil, kendi yakın geçmişinin dağılımıyla
karşılaştırılır; yüzlerce servisin her birinin kendi normal seviyesi olduğunda sabit bir eşiğin yapamadığı da budur.

```json
{"signal": "metric", "metric": "system.cpu.utilization", "aggregation": "avg", "series_aggregation": "avg",
 "filters": [], "group_by": ["host"], "window_seconds": 300, "seasonality": "daily", "lookback_days": 7,
 "direction": "upper", "sensitivity": 3, "min_samples": 3, "min_deviation": 0}
```

| Alan | Varsayılan | Notlar |
|---|---|---|
| `signal` | `metric` | `metric` (§2.2 seçicisi) veya `apm` (§2.6 seçicisi) |
| `metric` | zorunlu | metrik adı (`signal=metric`) veya §2.6'daki bir APM metriği (`signal=apm`) |
| `aggregation` | `avg` | `signal=metric`: `avg`, `min`, `max`, `sum`, `count`, `last`; `rate` ve persentiller rollup'ın tutmadığı ham noktalara ihtiyaç duyar |
| `series_aggregation` | `avg` (`sum`/`count` için `sum`) | tek bir grubun zaman serileri arasında, §2.2'deki gibi |
| `filters`, `group_by` | `[]` | `signal=metric`: `host.id`, `service.name`, `attr.<key>` (rollup'ta host adı veya resource attribute'ları yoktur, bu yüzden seriler yalnızca `host.id` taşır); `signal=apm`: `group_by` `environment`/`transaction`, filtre yok |
| `service_name`, `service_namespace`, `environment`, `transaction_type`, `transaction_name`, `min_requests` | | yalnızca `signal=apm`, §2.6'daki gibi |
| `window_seconds` | `300` | 60–21600, tam dakikaya yukarı yuvarlanır; baseline pencereleri aynı dilimi kapsasın diye sezonu tam bölmelidir |
| `seasonality` | `daily` | `none` (önceki pencereler), `hourly`, `daily`, `weekly` |
| `lookback_days` | `7` | 1–28; `lookback / season` baseline örneği verir, en fazla 200 (en yeniler) |
| `direction` | `upper` | bandın hangi tarafı alarm verir: `upper`, `lower`, `both` |
| `sensitivity` | `3` | standart sapma cinsinden bant genişliğinin yarısı, 0.5–20 |
| `min_samples` | `3` | 2–50; bundan az baseline örneği olan serinin değeri olmaz (eksik veri). `lookback_days` en az bu kadar örnek vermelidir |
| `min_deviation` | `0` | altında hiçbir şeyin alarm vermediği mutlak sapma (sabit bir değer civarında duran sinyaller için gürültü tabanı) |
- **Baseline.** Geçerli `[end − window, end)` penceresi, aynı sezonsal dilimin pencereleriyle karşılaştırılır:
  `m = 1 … samples` için `[end − m·season − window, end − m·season)`. Merkez ve ölçek, ortalama ve standart sapma değil **medyan** ve
  `1.4826 × MAD`'dir (median absolute deviation, normal veride σ'yı kestirecek şekilde ölçeklenmiş): alarm veren bir geçmiş önceki
  incident'ları içerir ve geçmişteki tek bir sıçrama, tüm lookback boyunca ortalamayı yükseltip standart sapmayı şişirir,
  böylece bant bir sonraki gerçek anomaliyi yutar. Ölçek hiçbir zaman baseline seviyesinin %1'inin altına inmez.
- **Değer.** `deviation ÷ (sensitivity × σ)`, yani karşılaştırma `gte 1` olarak sabittir: 0 = bandın içinde, 1 =
  tam kenarında, 2 = iki kat dışarıda; `direction`'ın izlemediği tarafta veya `min_deviation`'ın altında sapma 0'dır.
  Hiç değişim göstermeyen bir geçmiş büyük ve sonlu bir oran raporlar (her hareket anomalidir);
  böyle bir sinyali sessiz tutmak için `min_deviation` kullan. `for_seconds` geçerlidir; `interval_seconds`
  varsayılanı 60; eksik veri davranışı `keep`'tir.
- **Veri.** Yalnızca 1 dakikalık rollup'lar okunur (`metrics_1m`, `apm_transactions_1m`, apm.md §8) ve yalnızca
  sezonsal dilimlerin ihtiyaç duyduğu dakikalar: günlük 7 günlük bir baseline seri başına 7 gün değil, 8 × pencere dakika okur.
- Seri anahtarları ve label'ları alttaki seçicininkilerdir, artı `anomaly.baseline` (o değerlendirmedeki medyan) ve
  `anomaly.direction` (`above`/`below`).
- **Önizleme** (`Range`) her adım sonunu aynı şekilde simüle eder. Aralığı ve lookback'i birlikte seri başına 8000 dakikadan
  fazla okuyacak bir önizleme daha az (en yeni) baseline örneği kullanır ve
  `approximate` olarak raporlanır.
- Özet: `system.cpu.utilization avg over 5m is 6σ above the daily baseline 0.41 (≥ 3σ) (host.id=…)`.

### 2.8 Önerilen şablonlar {#_2-8-recommended-templates}

Hazır kural kataloğu (kod: `internal/alert/templates.go`): host'lar (`host_cpu_high`, `host_memory_high`,
`host_disk_full`, `host_not_reporting`, `service_disappeared`), container'lar (`container_restarts`, `container_cpu_high`,
`container_unhealthy`), APM servisleri (`apm_error_rate`, `apm_apdex_low`, `apm_latency_p95`, `apm_service_silent`),
Kubernetes (`k8s_pod_crashloop`, `k8s_pod_not_ready`, `k8s_node_not_ready`, `k8s_workload_replicas_unavailable`; kod
`internal/alert/templates_k8s.go`, semantic-conventions §7.4'ün metrikleri), synthetic check'ler (`synthetic_check_failing`,
`synthetic_certificate_expiring`; kod `internal/alert/templates_synthetics.go`) ve entegrasyonlar (nginx, Redis, MySQL, PostgreSQL; semantic-conventions §6'nın metrik adları) için. `GET /alerts/templates` bunları
`en`/`tr` adları, açıklamaları ve parametreleriyle listeler (`kind` number/duration/host/instance/service/environment/synthetic_check/text,
`unit`, `required`, `default`, `min`/`max`). `POST /alerts/templates/{id}/render` `{"params", "language", "name",
"channel_ids"}` doğrulanmış bir `RuleInput` döner (label `openlog.template=<id>`); bu, normal kural endpoint'leriyle önizlenir ve
oluşturulur; render ederken hiçbir şey saklanmaz.

- Hedefler: host şablonları `host_id` verilirse `host.id`'ye göre filtreler (yoksa tüm host'lar, host başına bir seri). Entegrasyon
  şablonları tek bir instance seçer (`host_id` + `discovery_id` + `instance` → `resource.openlog.discovery.id/instance`
  filtreleri, host başına bir seri) veya instance verilmezse entegrasyonun her instance'ını seçer
  (`resource.openlog.integration.id eq <id>`, `host` ve `resource.openlog.discovery.instance` ile gruplanır). APM
  şablonları `service_name` ve isteğe bağlı `environment` alır.
- Oran eşikleri (`redis_memory_high`: `ratio` × `redis.maxmemory`, `postgresql_connections_high`: `ratio` ×
  `postgresql.connection.max`) tek bir instance gerektirir ve referansın son bir saatteki en güncel değerini render anında çözer
  (tenant kapsamlı sorgu katmanından); eksik veya `0` → `409 failed_precondition`. Kural çözülen sayıyı saklar
  (sunucu ayarını değiştirdikten sonra yeniden render et).
- `container_restarts`, Docker yeniden başlatma sayısı pencere içinde en az `restarts` kadar arttığında alarm verir
  (`rate` > (`restarts` − 0.5) / pencere).
- Synthetics şablonları (`synthetics` kategorisi) isteğe bağlı bir `check_id` (`attr.check.id eq`; boş = her check,
  böylece bir kez yazılan kural sonradan eklenen check'leri de kapsar) ve kural adı için `check_name` alır,
  `attr.check.name` ve `attr.location` ile gruplanır; çalıştırmalar host veya servis taşımaz (D-132). `synthetic_check_failing`
  (`synthetics.check.success`, `avg`/`min` < `threshold` oranı, varsayılan 1 = herhangi bir başarısız çalıştırma) **eksik veriyi ihlal
  sayar**: çalışmayı bırakmış bir check başarılı bir check değildir. `synthetic_certificate_expiring`
  (`synthetics.certificate.expiry`, `last`/`min` < gün, varsayılan 14) eksik veriyi olduğu gibi bırakır (çalıştırma olmayan bir
  pencere sertifika hakkında bir şey söylemez) ve zaten yaptıkları handshake'te sertifikayı okuyan her `https` ve `tls` check'ini
  kapsar (D-140).
- Kubernetes şablonları (`kubernetes` kategorisi) isteğe bağlı `cluster_name` (`resource.k8s.cluster.name eq`) ve
  `namespace` (`attr.k8s.namespace.name eq`; `k8s_node_not_ready`'de yok) alır ve her zaman
  `resource.k8s.cluster.name` artı nesneye göre gruplanır: `k8s_pod_crashloop` (`attr.openlog.k8s.pod.reason eq CrashLoopBackOff` ile
  `openlog.k8s.pod.status`, `last`/`sum` > 0, namespace ve pod başına, eksik veri ok),
  `k8s_pod_not_ready` (`attr.openlog.k8s.pod.ready eq false`, phase `Succeeded`/`Failed` değil, 5 dk boyunca),
  `k8s_node_not_ready` (`attr.condition eq Ready` ile `k8s.node.condition`, `attr.k8s.node.name` başına `last`/`min` < 1,
  2 dk boyunca: false ve unknown alarm verir), `k8s_workload_replicas_unavailable` (`openlog.k8s.workload.unavailable`, pencere boyunca `min`
  > 0, namespace, kind ve name başına; isteğe bağlı `kind` filtresi).
- Bilinmeyen veya aralık dışı parametreler → `params.<key>` ile `400`; bilinmeyen şablon → `404`.

## 3. Değerlendirme semantiği {#_3-evaluation-semantics}

### 3.1 Takvim ve hizalama {#_3-1-schedule-and-alignment}
- Her kuralın sabit bir fazı vardır: `φ = fnv32a(rule_id) mod interval` (saniye), böylece kurallar aralığa yayılır (jitter).
- `now` duvar saatindeki bir değerlendirme, şu noktada biten pencereyi kapsar:
  `end = floor((now − delay − φ) / interval) · interval + φ`; burada `delay = OPENLOG_ALERT_EVALUATION_DELAY` (15 sn, processor'ın
  yetişmesine izin verir). Pencereler hizalıdır: her pod aynı `end` değerini hesaplar.
- `next_eval_at = end + interval + delay`. `end` değeri kayıtlı `last_eval_end` değerinden yeni olmayan bir değerlendirme
  atlanır (pod'lar arasında idempotent). Kaçırılan pencereler sonradan doldurulmaz; önceki değerlendirme
  `2 × interval + delay` süresinden eskiyse `pending` bir seri `for` zamanlayıcısını yeniden başlatır.

### 3.2 Durum makinesi (seri başına) {#_3-2-state-machine-per-series}

```
          breach               breach for ≥ for_seconds
   ok ───────────▶ pending ───────────────────────────▶ firing ──▶ (incident open)
   ▲                 │ no breach                          │ recovered for ≥ recovery hold
   └─────────────────┴───────────────────────────────────┘──▶ resolved (incident) → ok
```
- *breach* (ihlal): `value OP threshold`. *recovered* (toparlandı): `NOT (value OP recovery_threshold)`. İkisinin arasında (histerezis bandı)
  firing bir seri firing kalır, pending bir seri ok'a döner.
- `for_seconds = 0`: doğrudan ok → firing. Firing'e girmek **bir incident** açar ve kuralın etkin her kanalı için bir `opened`
  bildirimi kuyruğa alır.
- Toparlanma beklemesi = `recovery_for_seconds`; seri flapping durumundayken `flapping.hold_seconds` değerine yükseltilir. Bekleme sırasındaki
  bir ihlal, yeni incident veya bildirim olmadan beklemeyi iptal eder.
- Incident durumları: `open` → `acknowledged` (kullanıcı) → `resolved` (otomatik veya kullanıcı). Çözülme sebepleri: `recovered`, `manual`,
  `no_data` (missing_data=`ok`), `expired`, `rule_disabled`, `rule_deleted`, `rule_changed`.
- Hâlâ ihlalde olan bir serinin elle çözülmesi seriyi `ok`'a döndürür: seri yeniden `pending`'den geçer ve
  yeni bir incident açabilir.
- Bir kuralın `type` veya `condition` değerini değiştirmek, kuralı devre dışı bırakmak veya silmek açık incident'larını çözer (yukarıdaki sebep) ve
  seri durumunu temizler. Diğer düzenlemeler (ad, severity, kanallar, `for`) durumu korur.

### 3.3 Eksik veri (`metric_threshold`) {#_3-3-missing-data-metric-threshold}
Durum tablosunda bilinen (pending/firing/recovering) ama geçerli pencerede bulunmayan bir seri için:
`keep` (varsayılan) durumu değiştirmez; `max(1h, 10 × window)` süresinden uzun yoksa `expired` olarak çözülür.
`ok` onu toparlanmış sayar (incident `no_data` sebebiyle çözülür). `breach` onu ihlal sayar (değer `null`).
`log_match` gruplarında kayıt yoksa değer 0'dır; `no_data`/`discovery` yokluğu kendileri tanımlar.

### 3.4 Flapping koruması {#_3-4-flapping-protection}
Seri başına firing'e son 10 geçiş tutulur. `flapping.enabled` açıkken `flapping.window_seconds` (300–86400) içinde en az
`flapping.transitions` (2–20) geçiş olduysa seri **flapping** durumundadır: seri `flapping.hold_seconds` (60–86400) boyunca
kesintisiz toparlanana kadar mevcut incident'ı açık tutulur, böylece flapping yapan bir sinyal
tek incident ve tek çift bildirim üretir. `flapping` zaman çizelgesi olayı başlangıcı işaretler.

### 3.5 Bütçeler ve limitler {#_3-5-budgets-and-limits}
- Pod başına: aynı anda en fazla `OPENLOG_ALERT_MAX_CONCURRENT_EVALUATIONS` (16) değerlendirme, tenant başına en fazla
  `OPENLOG_ALERT_TENANT_MAX_CONCURRENT` (4) ve `OPENLOG_ALERT_TENANT_EVALUATIONS_PER_MINUTE` (600, token bucket). Bütçeyi aşan
  bir değerlendirme `throttled` olur ve bir saniye sonra yeniden denenir (penceresini korur).
- Her ClickHouse sorgusunun `max_execution_time` değeri `OPENLOG_ALERT_QUERY_TIMEOUT`'tur (20 sn).
- Organizasyon başına `OPENLOG_ALERT_MAX_RULES_PER_ORG` (1000) kural (API, `409 failed_precondition`).

### 3.6 Değerlendirme geçmişi (ClickHouse `alert_evaluations`) {#_3-6-evaluation-history-clickhouse-alert-evaluations}

`OPENLOG_ALERT_EVALUATION_HISTORY=true` (varsayılan) ile **commit edilen** her değerlendirme (§4 fencing: kural penceresi başına en fazla bir
satır kümesi) `openlog.alert_evaluations` tablosunda özetlenir (`schema/clickhouse/0007_alert_evaluations.sql`):

| Sütun | Kural satırı (`series_key = ''`) | Seri satırı |
|---|---|---|
| `tenant_id`, `rule_id`, `rule_type` | kural | kural |
| `evaluated_at` | değerlendirilen pencerenin sonu (§3.1) | aynı |
| `labels` | `{}` | seri label'ları |
| `value` | firing serilerin sayısı | seri değeri (`NULL` = değer yok) |
| `state` | herhangi bir seri firing ise `firing`, değilse herhangi biri pending ise `pending`, değilse `ok` | değerlendirme sonrası durum (`ok`, `pending`, `firing`) |
| `result` | `ok` veya `error` (hatalı bir değerlendirmede yalnızca kural satırı vardır) | `ok` |
| `duration_ms` | değerlendirme süresi | aynı |

- **Yazma yolu:** processor'dan bağımsızdır. Her evaluator satırları buffer'a alır ve her 5 sn'de bir (veya 10 000
  satırda bir) doğrudan `cityHash64(tenant_id, rule_id)`'nin seçtiği shard'ın `alert_evaluations_local` tablosuna insert eder (D-018,
  processor'ın doğrudan insert yazıcısı; bir kuralın tüm satırları tek shard'dadır). Başarısız bir batch aynı insert
  deduplication token'ıyla yeniden denenir (asla çift yazılmaz); ClickHouse erişilemezken pod başına en fazla 100 000 satır
  buffer'lanır ve en eskiler atılır (`openlog_alert_evaluation_rows_total{result="dropped"}`). Ölen bir pod'da buffer'lanmış
  satırlar kaybolur (geçmişte bir boşluk; durum ve incident'lar etkilenmez). SIGTERM flush eder.
- TTL 30 gün (`ttl_only_drop_parts`, günlük partition'lar).
- **API:** `GET /api/v1/alerts/rules/{id}/evaluations?from&to` (viewer ve API key'ler; varsayılan son 24 saat, en fazla
  30 gün; başka organizasyonun kuralı için `404`). Satırlar en fazla 500 noktaya bucket'lanır (`step_seconds` ≥ 10):
  seri başına bucket'taki en güncel değer ve en kötü durum (`firing` > `pending` > `ok`); kural satırlarının bucket'ı başına
  `firing_series` (maksimum), `evaluations`, `errors`, `duration_ms` (en güncel) ve `max_duration_ms`. En fazla 50 seri
  (ok olmayan bucket'ı en çok olanlar önce); seriler veya satırlar (50 000) kesildiğinde `truncated`. Tenant kapsamlı sorgu
  katmanından okunur (`query.AlertEvaluations`).

## 4. Kural sahipliği (lease'ler) {#_4-rule-ownership-leases}

`alert_rule_leases` tablosu (kural başına bir satır, kuralla birlikte oluşturulur) ve `alert_evaluators` (heartbeat'ler).
Her `OPENLOG_ALERT_LEASE_RENEW_INTERVAL` (10 sn) aralığında her evaluator:
1. heartbeat'ini upsert eder; `live` = `OPENLOG_ALERT_LEASE_TTL` (30 sn) içinde görülmüş evaluator'lar;
2. lease'lerini yeniler: `UPDATE … SET lease_until = now() + ttl WHERE owner = me` (devre dışı kurallar bırakılır);
3. adil payını hesaplar: `ceil(enabled_rules / live)`;
4. payın üstündeyse: fazlasını bırakır (en geç `next_eval_at` önce; `owner = ''`, `lease_until = -infinity`);
5. payın altındaysa: süresi dolmuş lease'leri üstlenir, en eski `next_eval_at` önce:
   `SELECT … WHERE lease_until < now() ORDER BY next_eval_at LIMIT n FOR UPDATE SKIP LOCKED` → owner ve lease ayarlanır.

Timestamp'ler veritabanı saatini kullanır. Yeni bir pod payını yaklaşık iki yenileme aralığında alır; ölü bir pod'un kuralları
TTL'den sonra taşınır; duran bir pod (SIGTERM) lease'lerini ve heartbeat'ini hemen bırakır.

**Fencing.** Bir değerlendirme ancak transaction'ı içinde lease satırı (`FOR UPDATE` ile kilitli) hâlâ
`owner = me AND lease_until > now() AND (last_eval_end IS NULL OR last_eval_end < end)` koşulunu sağlıyorsa ve kural satırı hâlâ
değerlendirilen `version` ve `enabled = true` değerlerini taşıyorsa commit edilir. Aksi halde tüm transaction (seri durumu, incident'lar, olaylar, outbox satırları)
atılır (`openlog_alert_evaluations_total{result="lease_lost"|"stale"}`). API de bir kuralı veya incident'ı değiştirmeden önce aynı lease
satırını kilitler, yani ikisi sıraya girer. Bu yüzden iki pod aynı seriye asla birlikte geçiş yaptıramaz; kısmi
unique index `alert_incidents_open_uniq (rule_id, series_key) WHERE state <> 'resolved'` ikinci bir korumadır.

## 5. Bildirimler {#_5-notifications}

### 5.1 Outbox ve teslimat {#_5-1-outbox-and-delivery}
`alert_notifications` satırları, onlara yol açan değerlendirme (veya API) transaction'ında insert edilir:

| `kind` | Ne zaman | Idempotency anahtarı |
|---|---|---|
| `opened` | seri firing'e girer | `<incident_id>:opened:<channel_id>` |
| `acknowledged` | bir kullanıcı incident'ı onaylar; yalnızca yaşam döngüsünü izleyen kanallar için (`pagerduty`, `opsgenie`) | `<incident_id>:acknowledged:<channel_id>` |
| `resolved` | incident çözülür (herhangi bir sebeple) | `<incident_id>:resolved:<channel_id>` |
| `renotify` | açık, onaylanmamış incident, son bildirimden `renotify_interval_seconds` sonra | `<incident_id>:renotify:<n>:<channel_id>` |
| `test` | `POST /alerts/channels/{id}/test` (API tarafından eşzamanlı gönderilir) | `test:<notification_id>` |

Anahtar `UNIQUE`'tir (`INSERT … ON CONFLICT DO NOTHING`) ve alıcılara gönderilir (§5.3). `opened` kanalları = incident'la eşleşen
routing kuralının kanalları, yoksa incident açıldığı andaki kuralın etkin kanalları
(§5.6; incident üzerinde saklanır); `acknowledged`/`resolved`/`renotify` aynı kanallara gider.

Dispatcher'lar (her `openlog-alert` pod'u, `OPENLOG_ALERT_DISPATCH_WORKERS` worker) vadesi gelen satırları üstlenir
(`status = pending AND next_attempt_at <= now()`, en eski önce, `FOR UPDATE SKIP LOCKED`), `status = sending` ve
`claimed_until = now() + delivery timeout + 30 s` ayarlar, iletir, sonra tek transaction'da denemeyi
`alert_delivery_attempts`'e, sonucu satıra ve bir incident zaman çizelgesi olayını yazar. Kurallar:
- **Sıralama:** aynı incident ve kanalın daha eski bir satırı `pending`/`sending` iken bir satır üstlenilmez.
  `acknowledged`/`resolved`/`renotify` yalnızca o kanalın `opened` satırı `delivered` ise gönderilir; aksi halde
  `suppressed` olurlar (bir alıcı, açılış mesajı olmadan asla resolve almaz).
- **Yeniden denemeler:** ağ hataları, timeout'lar, `408`, `429` (`Retry-After`'a uyar, ≤ 1 sa), `5xx` ve SMTP `4xx`,
  üstel backoff ve ±%20 jitter ile yeniden denenir: 10 sn, 30 sn, 1 dk, 2 dk, 5 dk, 10 dk, 20 dk, 30 dk, sonra her 30 dk. Diğer `4xx`
  ve SMTP `5xx` hemen başarısız olur. `OPENLOG_ALERT_DELIVERY_MAX_ATTEMPTS` (10) deneme veya 24 saat sonra satır `failed` olur
  (vazgeçilir), `notification_failed` zaman çizelgesi olayıyla.
- **Pod ölümü:** `claimed_until` değeri geçmiş bir `sending` satırı herhangi bir dispatcher tarafından `pending`'e döndürülür. Yani teslimat
  at-least-once'tır: çift gönderim yalnızca bir süreç alıcı mesajı kabul ettikten sonra ve sonuç commit'inden önce
  (milisaniyeler) ölürse (SIGKILL/OOM/elektrik) mümkündür. SIGTERM önce yoldaki teslimatları bitirir. Webhook
  alıcıları `X-Openlog-Idempotency-Key` ile tekilleştirir; e-postalar, posta istemcilerinin tekilleştirdiği deterministik bir `Message-ID`
  taşır. Değerlendirmeler fencing sayesinde exactly-once'tır (§4), yani pod ölümü asla ikinci bir incident veya outbox satırı oluşturmaz.
- Silinmiş veya devre dışı kanal → denemesiz `failed` satır (`channel deleted` / `channel disabled`).
- Satırlar ve denemeler bittikten sonra 30 gün tutulur (evaluator döngüsü temizler).

### 5.2 Mute pencereleri {#_5-2-mute-windows}
`alert_mutes`: `starts_at`, `ends_at` (arası ≤ 90 gün), isteğe bağlı `rule_ids`, `matchers`
(incident label'ları üzerinde `{"label", "op": "eq|neq|contains", "value"}`; AND ile bağlanır). Teslimat anında dispatcher tarafından kontrol edilir:
- eşleşen bir `opened` satırı mute'un sonuna ertelenir (`notification_muted` zaman çizelgesi olayı); vadesi geldiğinde
  incident zaten çözülmüşse `suppressed` olur (`resolved` satırı da);
- `resolved` satırları `opened` satırları gibi ertelenir; mute sırasındaki `renotify` ve `acknowledged` satırları `suppressed` olur
  (bunlar durum güncellemeleridir, ertelemenin anlamı yoktur).
Değerlendirme, incident'lar ve zaman çizelgesi mute'lardan etkilenmez. Bir mute eşleştiği sürece incident'lar `muted: true` gösterir.

**Tekrarlayan mute'lar.** `schedule` içeren bir mute tekrarlar; girdideki `starts_at`/`ends_at` bu durumda yok sayılır:
```json
{"name": "nightly batch", "rule_ids": [], "matchers": [],
 "schedule": {"timezone": "Europe/Istanbul", "days": ["mon", "tue", "wed", "thu", "fri"], "start_time": "22:00",
              "end_time": "06:00", "from": null, "until": null}}
```
| Alan | Notlar |
|---|---|
| `timezone` | IANA adı (varsayılan `UTC`) |
| `days` | `mon` … `sun` (tekilleştirilmiş, Pazartesi başta saklanır) veya bunun yerine `rrule`: RFC 5545 alt kümesi `FREQ=WEEKLY;BYDAY=MO,TU,…` (ordinal yok) ya da `FREQ=DAILY`, isteğe bağlı `RRULE:` öneki ve `INTERVAL=1`; türetilen `days` ile normalize edilmiş halde saklanır. Diğer parçalar (`COUNT`, `UNTIL`, `BYHOUR`, …) → `400` |
| `start_time`, `end_time` | yerel `HH:MM`; `end_time` ≤ `start_time` sonraki gün demektir (en fazla 23 sa 59 dk); eşit → `400` |
| `from`, `until` | isteğe bağlı RFC3339 / unix ms sınırları; `from` varsayılan olarak şimdidir; oluşumlar `[from, until)` aralığına kırpılır |
| `exdates` | isteğe bağlı, hiçbir oluşumun **başlamadığı** ≤ 366 yerel tarih (`YYYY-MM-DD` veya RFC 5545 `YYYYMMDD`) (EXDATE alt kümesi); sıralı ve tekilleştirilmiş saklanır |
| `holiday_calendar_ids` | isteğe bağlı, tarihleri de istisna sayılan, organizasyona ait ≤ 10 tatil takvimi (bilinmeyen id için `400`) |

**Aylık kurallar.** `rrule` ayrıca `FREQ=MONTHLY`'yi şunlarla kabul eder: `BYMONTHDAY=1,15,-1` (±1..31; `-1` = son gün; ayda bulunmayan
gün atlanır), `BYDAY=MO,TU` (böyle her hafta günü), ordinal'lar `BYDAY=1MO,-1FR` (ilk Pazartesi, son
Cuma; ±1..5) ve `BYSETPOS=±1..31` (ayın `BYMONTHDAY`/`BYDAY` ile eşleşen günler kümesindeki konumlar, örneğin
`FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1` = son iş günü). `BYMONTHDAY` ve `BYDAY` birlikte birbirini kısıtlar
(`BYDAY=FR;BYMONTHDAY=13`). Yinelenen parçalar, `MONTHLY` dışındaki ordinal'lar, `BYDAY`/`BYMONTHDAY` olmadan `BYSETPOS` → `400`.
Aylık takvimler `days: []` ve normalize edilmiş kuralı saklar.

**Tatil takvimleri** (`alert_holiday_calendars`, `/alerts/holiday-calendars`): adlandırılmış tarih kümeleri (≤ 1000 tarih:
tek bir tarih için `YYYY-MM-DD`, her yıl için `MM-DD` veya `--MM-DD`); admin ve owner'lar yönetir, her rol okur. Tarihler
her mute'un saat dilimindeki yerel tarihlerdir. Takvim değişiklikleri, ona referans veren mute'lara bir sonraki kontrollerinden itibaren uygulanır; bir mute'un
referans verdiği takvim silinemez (`409 failed_precondition`).

- **Oluşumlar** seçilen her yerel tarihte `start_time`'da başlar ve `end_time`'da biter (aynı veya sonraki tarih). DST
  değişikliklerinde yerel duvar saati değerlerini korurlar (saatlerin ileri alındığı gece 01:00–04:00 oluşumu 2 sa, geri alındığı gece
  4 sa sürer). Var olmayan bir yerel saat (DST boşluğu) boşluk kadar ileri kayar (02:30 → 03:30); belirsiz bir
  yerel saat sonraki andır (saatler geri alındıktan sonraki). Dönüştürülmüş bitişi başlangıcından sonra olmayan oluşum
  atlanır.
- **İstisnalar** bir istisna tarihinde (exdate veya tatil) başlayan oluşumları atlar; önceki akşam başlamış bir gece oluşumu
  istisna tarihine yine de sarkar. Sonraki oluşum 800 güne kadar ileride aranır.
- Yanıtlar `upcoming` taşır (istisnalar uygulanmış, en fazla 5 geçerli/sonraki oluşum); `POST /alerts/mutes/preview`
  `{"schedule"}` kaydedilmemiş bir takvim için aynısını döner.
- Bir mute bir oluşumun içinde aktiftir (bitiş hariç); eşleşen `opened`/`resolved` satırları **geçerli oluşumun**
  sonuna ertelenir. Şimdiden sonra oluşumu olmayan takvimli bir mute oluşturmak → `400`.
- Yanıtlar: `starts_at`/`ends_at` = geçerli veya sonraki oluşum (`until`'den sonra sonuncusu), `schedule`
  saklandığı gibi (tek seferlik mute'lar için `null`), `active`. `GET /mutes` tekrarlayan mute'ları `until`'den 7 gün sonrasına kadar tutar.
- Depolama ve karışık sürümler: `alert_mutes.schedule` (jsonb). `starts_at`/`ends_at` geçerli veya sonraki oluşumu tutar;
  dispatcher'lar bir oluşum bittikten sonra dakikada bir onları sonraki oluşuma taşır, böylece eski sürüm
  dispatcher'lar (`schedule`'ı yok sayarlar) aynı oluşumlarda mute uygular. Aylık kurallar ve istisnalar
  `0015_alert_holiday_calendars`'tan eski dispatcher'lar tarafından yok sayılır: rolling upgrade sırasında aylık takvimlerde mute uygulamazlar
  (boş `days`) ve istisna tarihlerinde mute uygularlar.

### 5.3 Payload'lar {#_5-3-payloads}

Ortak olay alanları (webhook gövdesi; diğer formatlar aynı veriyi render eder):
```json
{"version": "1", "event": "incident.opened", "idempotency_key": "…:opened:…", "notification_id": "…",
 "sent_at": "2026-09-13T10:00:05.000000000Z",
 "organization": {"id": "…", "name": "Default"},
 "rule": {"id": "…", "name": "High CPU", "type": "metric_threshold", "severity": "critical",
          "runbook_url": "", "url": "https://openlog.example.com/alerts/rules/…"},
 "incident": {"id": "…", "state": "open", "url": "https://openlog.example.com/alerts/incidents/…",
              "summary": "system.cpu.utilization avg over 1m is 0.93 (> 0.9) on web-1", "value": 0.93,
              "threshold": 0.9, "labels": {"host.id": "…", "host.name": "web-1"},
              "opened_at": "…", "acknowledged_at": null, "resolved_at": null, "resolve_reason": null}}
```
`event` ∈ `incident.opened`, `incident.acknowledged`, `incident.resolved`, `incident.renotify`, `test`. Bağlantılar
`OPENLOG_PUBLIC_URL` kullanır (ayarlı değilse atlanır). Özetlerdeki değerler 4 anlamlı basamağa yuvarlanır.

**Genel webhook** — kanal URL'ine `POST`, `Content-Type: application/json`, `User-Agent: openlog-alert/<version>`,
`X-Openlog-Event`, `X-Openlog-Delivery: <notification_id>`, `X-Openlog-Idempotency-Key`,
`X-Openlog-Timestamp: <unix seconds>` ve kanalın HMAC secret'ı varsa
`X-Openlog-Signature: sha256=<hex(HMAC-SHA256(secret, timestamp + "." + body))>`. Alıcılar HMAC'i ham gövde üzerinden yeniden
hesaplamalı, sabit zamanda karşılaştırmalı ve 5 dakikadan eski timestamp'leri reddetmelidir. 2xx = iletildi. Yönlendirmeler
izlenmez.

**Slack** (incoming webhook) — `{"text": "<fallback>", "blocks": [header ("🔴 FIRING: High CPU" / "✅ RESOLVED: …"),
section with summary, section fields (Severity, Rule, Labels, Opened/Resolved), context (value vs threshold),
actions button "Open incident" (when a public URL is set)]}`. Başarı = HTTP 200.

**Microsoft Teams** (Workflows "When a Teams webhook request is received" veya eski incoming webhook) —
`{"type": "message", "attachments": [{"contentType": "application/vnd.microsoft.card.adaptive", "content":
{"type": "AdaptiveCard", "version": "1.4", "body": [TextBlock title (attention/good colour), TextBlock summary,
FactSet (Severity, Rule, State, Value, Threshold, labels…)], "actions": [Action.OpenUrl "Open incident"]}}]}`.
Başarı = HTTP 200/202.

**PagerDuty** (Events API v2) — `https://events.pagerduty.com/v2/enqueue` adresine `POST` (`region: eu` →
`https://events.eu.pagerduty.com/v2/enqueue`), `routing_key` olarak integration key:
```json
{"routing_key": "…", "event_action": "trigger", "dedup_key": "openlog-<incident id>",
 "client": "openlog", "client_url": "https://openlog.example.com/alerts/incidents/…",
 "payload": {"summary": "FIRING: High CPU — system.cpu.utilization avg over 1m is 0.93 (> 0.9) on web-1",
             "severity": "critical", "source": "web-1", "component": "checkout", "group": "Default",
             "class": "metric_threshold", "timestamp": "2026-09-13T10:00:00.000000000Z",
             "custom_details": {"rule": "High CPU", "rule_type": "metric_threshold", "severity": "critical",
                                "state": "open", "value": "0.93", "threshold": "0.9", "organization": "Default",
                                "host.name": "web-1", "incident_id": "…", "incident_url": "…", "runbook_url": "…"}},
 "links": [{"href": "…", "text": "Open incident"}, {"href": "…", "text": "Alert rule"}]}
```
- **Yaşam döngüsü:** `incident.opened` ve `incident.renotify` → `trigger`, `incident.acknowledged` → `acknowledge`,
  `incident.resolved` → `resolve`. Bir incident'ın her bildirimi için `dedup_key` `openlog-<incident id>` olur,
  böylece güncellemeler trigger'ın açtığı PagerDuty alert'ine düşer (yeniden bildirim ikincisini açmak yerine onu
  günceller). `acknowledge` ve `resolve` yalnızca `routing_key`, `event_action` ve `dedup_key` taşır.
- **Severity:** `critical` → `critical`, `warning` → `warning`, `info` → `info` (başka her şey `warning`).
- `source` incident'ın `host.name` değeridir, yoksa `service.name`, yoksa `host.id`, yoksa organizasyon; `component`,
  incident'ta varsa `service.name` olur; dahili `alert.*` label'ları detay olarak gönderilmez.
- **Test** bildirimi hemen tetikler ve çözer (`dedup_key` `openlog-test-<notification id>`), yani bir kanalı test etmek
  açık PagerDuty incident'ı bırakmaz. Başarı = 2xx (`202 Accepted`).

**Opsgenie** (Alerts API) — `https://api.opsgenie.com/v2/alerts` adresine `POST` (`region: eu` →
`https://api.eu.opsgenie.com/v2/alerts`), header `Authorization: GenieKey <api key>`:
```json
{"message": "FIRING: High CPU", "alias": "openlog-<incident id>", "description": "<summary + details>",
 "priority": "P1", "source": "openlog", "entity": "web-1", "tags": ["openlog", "severity:critical", "payments"],
 "responders": [{"type": "team", "name": "ops"}],
 "details": {"rule": "High CPU", "value": "0.93", "threshold": "0.9", "host.name": "web-1", "incident_url": "…"}}
```
- **Yaşam döngüsü:** `incident.opened`/`incident.renotify`'da oluştur, sonra
  `{"user", "source", "note"}` gövdesiyle `POST /v2/alerts/<alias>/acknowledge?identifierType=alias` ve
  `POST /v2/alerts/<alias>/close?identifierType=alias`. `alias` `openlog-<incident id>` olur, böylece Opsgenie
  yeniden bildirimleri oluşturmanın açtığı alert'e tekilleştirir.
- **Priority:** `critical` → `P1`, `warning` → `P3`, `info` → `P5`; `config.opsgenie.priority` ile ezilir
  (`P1` … `P5`). `message` 130 karaktere, `description` 15 000 karaktere kısaltılır.
- `tags`: `openlog`, `severity:<severity>` ve yapılandırılan tag'ler (≤ 18, Opsgenie toplamda 20'ye izin verir);
  `responders`: yapılandırılan takımlar, kullanıcılar, escalation'lar veya schedule'lar (`type` + `name` ya da `id`).
- **Test** bildirimi alert'i hemen oluşturup kapatır. Başarı = 2xx (`202 Accepted`).

**E-posta** — `multipart/alternative` (düz metin + HTML), `Subject: [openlog] FIRING critical: High CPU (web-1)` /
`RESOLVED …`, `Message-ID: <sha256(idempotency_key)[:32]@openlog>`; resolve ve renotify e-postaları açılış e-postasının `In-Reply-To` ve
`References` değerlerini taşır (threading). Kanal yapılandırması: `to` (1–50 adres) ve isteğe bağlı SMTP override'ı
(`smtp.host`, `port`, `username`, `from`, `tls`), parola secret olarak; override yoksa global
`OPENLOG_SMTP_*` sunucusu kullanılır. `tls`: `starttls` (varsayılan, zorunlu), `tls` (implicit TLS, port 465), `none`
(düz metin, kimlik doğrulama reddedilir). TLS üzerinden yalnızca `PLAIN` auth.

### 5.4 Kanal secret'ları {#_5-4-channel-secrets}
Secret alanlar: `url` (slack, teams, webhook), `hmac_secret` (webhook), `smtp_password` (email),
`routing_key` (pagerduty, Events API v2 integration key), `api_key` (opsgenie). PostgreSQL'e ulaşmadan önce
AES-256-GCM ile şifrelenirler:
- anahtar: `OPENLOG_SECRETS_KEY` = 32 rastgele baytın base64'ü (`openssl rand -base64 32`); okuma için önceki anahtarlar
  `OPENLOG_SECRETS_KEY_PREVIOUS` içinde (virgülle ayrılmış).
- saklanan değer: `ol1:<key id>:<base64(12-byte nonce ‖ ciphertext ‖ tag)>`, key id = `sha256(key)`'in ilk 8 hex karakteri,
  ek kimliği doğrulanmış veri = `org_id + "/" + channel_id` (başka bir satıra kopyalanan ciphertext
  çözülmez).
- API oluşturmadan sonra secret'ları asla döndürmez: yanıtlar `secret_hints` taşır (`https://hooks.slack.com/…/•••f3a9`,
  `•••••• (set)`). `PUT` gönderilmeyen secret alanlarını korur. `hmac_secret` olmadan oluşturulan webhook üretilmiş bir secret alır,
  bir kez `generated_secrets.hmac_secret` olarak döner.
- `OPENLOG_SECRETS_KEY` yoksa API kanal yazmalarına ve test gönderimlerine `409 failed_precondition` ile yanıt verir;
  dispatcher, şifresini çözemediği kanalların satırlarını yeniden denemelerden sonra başarısız yapar (`cannot decrypt channel secrets`).

**Rotasyon:** (1) yeni anahtar `OPENLOG_SECRETS_KEY`'de, eskisi `OPENLOG_SECRETS_KEY_PREVIOUS`'ta olacak şekilde deploy et
(API ve alert pod'ları); (2) `openlog-alert rotate-secrets`'ı bir kez çalıştır (her kanalı geçerli anahtarla yeniden şifreler,
idempotent, sayıları yazdırır); (3) eski anahtarı kaldır. `openlog-alert rotate-secrets -check` hâlâ başka anahtar kullanan
satırları raporlar.

### 5.5 Egress {#_5-5-egress}
Webhook, Slack ve Teams URL'leri `https://` olmalıdır (webhook'lar `http://` kullanabilir). PagerDuty ve Opsgenie kanallarının
URL'si yoktur: her zaman sağlayıcının kendi `region`'ının (`us`/`eu`) public `https://` endpoint'ine ulaşırlar.
`OPENLOG_ALERT_BLOCK_PRIVATE_DESTINATIONS=true` ile (SaaS için önerilir) loopback, private, link-local ve
belirtilmemiş adreslere bağlantılar dial anında reddedilir (DNS rebinding'e karşı güvenli), SMTP override'ları için de.

### 5.6 Routing kuralları {#_5-6-routing-rules}

`alert_routing_rules` (`0088_alert_routing`, D-127), bir incident açıldığında hangi kanallara ulaşacağına karar veren,
**organizasyon başına sıralı bir listedir**. Routing kuralı yoksa hiçbir şey değişmez: incident kuralının
etkin kanallarına iletilir (`channel_ids`, §2.1).

```json
{"name": "critical payments to on-call", "position": 0, "enabled": true, "is_default": false,
 "channel_ids": ["…pagerduty…"],
 "match": {"severities": ["critical"], "services": ["checkout"], "rule_types": ["apm", "metric_threshold"],
           "labels": [{"label": "env", "op": "eq", "value": "prod"}],
           "time_window": {"timezone": "Europe/Istanbul", "days": ["mon", "tue", "wed", "thu", "fri"],
                           "start_time": "09:00", "end_time": "18:00"}}}
```

| Alan | Notlar |
|---|---|
| `name` | 1–200 karakter |
| `position` | değerlendirme sırası, 0 ilk (0–100); eşitlikte kayıtlı sıra korunur. `POST /alerts/routing-rules/reorder` tüm sırayı tek seferde ayarlar |
| `enabled` | varsayılan `true`; devre dışı route atlanır |
| `is_default` | **varsayılan route**: her incident'la eşleşir ve diğer tüm route'lardan sonra değerlendirilir. Organizasyon başına en fazla bir tane (`409 failed_precondition`) ve `match`'i boş olmalıdır |
| `channel_ids` | organizasyonun 1–20 kanalı (bilinmeyen id → `400`); eşleşen route'un devre dışı kanalları atılır |
| `match.severities` | `critical`, `warning`, `info`; boş = herhangi biri |
| `match.services` | incident label'larının birebir `service.name` değeri; boş = herhangi biri |
| `match.rule_types` | §2.1'in kural türleri; boş = herhangi biri |
| `match.labels` | incident label'ları üzerinde ≤ 20 eşleştirici `{"label", "op": "eq\|neq\|contains", "value"}` (§5.2'nin operatörleri), AND ile bağlanır |
| `match.time_window` | yerel pencere: IANA `timezone` (varsayılan `UTC`), `days` (boş = her gün), `start_time`/`end_time` `HH:MM`; `end_time` ≤ `start_time` sonraki gün demektir |

**Eşleşme** bir kez, incident açıldığında yapılır (pencere bitişinde, §3.1); incident'ın severity'si, kural
türü, `service.name` label'ı ve tüm label'larıyla (kural label'ları dahil, §6). Bir `match`'in parçaları AND ile bağlanır;
boş parça her şeyle eşleşir.

**Sıra:** eşleşmesi uyan ilk **etkin** route kazanır (en spesifik olan değil), sonra varsayılan
route gelir. Eşleşme ve varsayılan route yoksa incident kuralının kanallarına gider, yani bir routing
hatası bir alarmı asla sessizce düşürmez. Seçilen kanallar incident üzerinde saklanır (`channel_ids`), yani
`acknowledged`, `resolved` ve `renotify` routing kurallarını değil incident'ı izler (bir route'u düzenlemek açık
incident'ları taşımaz).

Organizasyon başına en fazla `100` routing kuralı (`409 failed_precondition`). Evaluator'lar bir organizasyonun
routing kurallarını 5 saniye cache'ler; başarısız bir yükleme loglanır ve o değerlendirme için kuralın kendi kanalları kullanılır.

## 6. Incident'lar {#_6-incidents}

`alert_incidents` satırları `rule_name`, `rule_type`, `severity`, `labels`, `summary`, `threshold`, `value` (açılıştaki),
`last_value`, `opened_at`, `acknowledged_at/by`, `resolved_at/by`, `resolve_reason`, `channel_ids` tutar. Zaman çizelgesi
(`alert_incident_events`, sıralı): `opened`, `flapping`, `acknowledged`, `note`, `renotified`, `resolved`,
`notification_delivered`, `notification_failed`, `notification_suppressed`, `notification_muted`. Incident'lar
kuralları silindiğinde korunur (`rule_id` null olur). 400 günden eski çözülmüş incident'lar temizlenir.

Incident label'ları = seri label'ları + kural `labels` + `alert.severity`, `alert.rule_name`.

## 7. API rolleri {#_7-api-roles}

| İşlem | viewer | member | admin, owner |
|---|:-:|:-:|:-:|
| Kuralları, kural türlerini, incident'ları, kanalları (maskeli), mute'ları, routing kurallarını, teslimat logunu okumak; kural önizleme | ✓ | ✓ | ✓ |
| Kural oluşturmak; **kendi** kurallarını güncellemek, etkinleştirmek/devre dışı bırakmak, silmek | | ✓ | ✓ |
| Incident'ları onaylamak, çözmek, not eklemek | | ✓ | ✓ |
| Mute oluşturmak; **kendi** mute'larını güncellemek/silmek | | ✓ | ✓ |
| Şablon render etmek (`/alerts/templates/{id}/render`), mute takvim önizlemesi | ✓ | ✓ | ✓ |
| Herhangi bir kuralı veya mute'u güncellemek/silmek; kanal oluşturmak/güncellemek/silmek/test etmek; routing kuralı oluşturmak/güncellemek/silmek/sıralamak; tatil takvimi oluşturmak/güncellemek/silmek | | | ✓ |

Aynı satırlar, kendi rolü olan bir API key için de geçerlidir (D-133): `member` key kural ve
mute oluşturur, incident'lar üzerinde çalışır; `admin` key herhangi bir kuralı veya mute'u değiştirir ve kanalları yönetir. Key hiçbir şeye sahip olmaz,
yani key ile oluşturulan kural veya mute sonradan yalnızca admin kuralıyla değiştirilebilir. Session'lar her zamanki gibi CSRF
gönderir; bearer istekleri göndermez. Her yazma audit log'dadır
(`alert.rule.{create,update,delete,enable,disable}`, `alert.channel.{create,update,delete,test}`,
`alert.mute.{create,update,delete}`, `alert.routing_rule.{create,update,delete,reorder}`,
`alert.holiday_calendar.{create,update,delete}`, `alert.incident.{acknowledge,resolve}`). `OPENLOG_AUTH_MODE=static` ile
kullanılamaz.

## 8. Metrikler (`openlog-alert` admin `/metrics`) {#_8-metrics-openlog-alert-admin-metrics}

| Metrik | Label'lar |
|---|---|
| `openlog_alert_evaluations_total` | `result` = `ok`, `error`, `throttled`, `lease_lost`, `stale`, `skipped` |
| `openlog_alert_evaluation_duration_seconds` (histogram) | `type` |
| `openlog_alert_evaluation_lag_seconds` (histogram) | — (başlangıç zamanı − `next_eval_at`) |
| `openlog_alert_rules_owned` (gauge) | — |
| `openlog_alert_lease_changes_total` | `change` = `claimed`, `released`, `lost` |
| `openlog_alert_evaluators_live` (gauge) | — |
| `openlog_alert_transitions_total` | `to` = `pending`, `firing`, `ok`, `resolved` |
| `openlog_alert_notifications_total` | `channel_type`, `result` = `delivered`, `retry`, `failed`, `suppressed`, `muted` |
| `openlog_alert_delivery_duration_seconds` (histogram) | `channel_type` |
| `openlog_alert_outbox_pending` (gauge) | — |
| `openlog_alert_evaluation_rows_total` | `result` = `written`, `dropped` (§3.6) |
| `openlog_alert_evaluation_write_errors_total` | — (başarısız insert'ler, yeniden denenir) |

## 9. M2'de olmayanlar {#_9-not-in-m2}
§5.2 alt kümesinin ötesindeki mute takvimleri (yıllık kurallar, `INTERVAL` > 1, RRULE `COUNT`/`UNTIL`, `RDATE`); önizlemeler için değerlendirme
geçmişi. PagerDuty ve Opsgenie kanalları ile routing kuralları M3'te geldi (§5.3, §5.6, D-127).
