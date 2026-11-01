# Sözleşme: APM (v1, M2) {#contract-apm-v1-m2}

OTLP trace'lerinden türetilen uygulama performans izleme (D-032, [10-m2.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/10-m2.md) §3). Bu dosya
processor, ClickHouse şeması (`schema/clickhouse/0006_apm.sql`), edge bağlama job'u
(`internal/apm`), API (`/api/v1/apm/*`, [api.md](/tr/contracts/api#apm)) ve **alert kuralı koşulları** (§10) için bağlayıcıdır. §1–§8'deki tanımlayıcılar ve
metrik tanımları kararlıdır: değişiklikler yalnızca ekleme şeklinde olur.

## 1. Servis kimliği {#_1-service-identity}

Bir servis, span'in **resource** attribute'larından alınan üçlüdür; eksik değerler `''` olur.

| Alan | Attribute | Sütun |
|---|---|---|
| `service_name` | `service.name` | `service_name` |
| `service_namespace` | `service.namespace` | `service_namespace` |
| `environment` | `deployment.environment.name`, yoksa `deployment.environment` | `deployment_environment` |

`service.name` olmayan span'ler saklanır ama APM'in parçası olmaz (transaction, edge, hata veya servis satırı üretmez).
API yolları servisi yalnızca `service_name` ile adlandırır (`/apm/services/{service_name}`); isteğe bağlı `namespace` ve
`environment` sorgu parametreleri tek bir namespace/environment ile sınırlar. **Verilmemesi "tümü" demektir** (toplanmış), verilmesi
(boş olsa bile) tam eşleşme demektir.

Arayüzün gösterdiği diğer resource attribute'ları: `service.version`, `telemetry.sdk.language`, `telemetry.sdk.name`,
`host.id`, `host.name`, `container.id`, `k8s.pod.name`.

**Host ↔ servis bağı:** hem `service.name` hem `host.id` taşıyan bir resource, servisi o host'a bağlar
(`apm_service_hosts`). Altyapı ajanı aynı `host.id`'yi gönderir (semantic-conventions §1), böylece host sayfası üzerinde çalışan
servisleri listeler. Processor ayrıca `host.id` taşıyan trace resource'larından `hosts` tablosunu upsert eder
(M1 davranışı değişmedi).

**Container ↔ servis bağı:** hem `service.name` hem `container.id` taşıyan bir span resource'u, servisi o
container'a bağlar (`apm_service_containers`, küçük harfli id; resource'un `host.id` ve `container.name` değerlerini de kaydeder). Altyapı
ajanı container'ın metrikleri, durumu ve logları için aynı `container.id`'yi bildirir (semantic-conventions §2, §4.1), böylece
servis sayfası container'larını durum, CPU ve bellekle, container sayfası da servislerini RED metrikleriyle listeler.
OTel SDK'ları `container.id`'yi container resource detector'larıyla (Go ajanı: `agents/go`, `/proc/self/cgroup`'tan, private cgroup namespace altında
`/proc/self/mountinfo`'dan) veya `OTEL_RESOURCE_ATTRIBUTES` ile ayarlar. `container.id` olmayan servisler
bağlanmaz: `host.id` + süreç ile eşleştirme yapılmaz (bir host çok sayıda container çalıştırır; PID'ler namespace'ler arasında farklıdır).
Yalnızca span'ler bağ kurar; uygulamaların log ve metrikleri kurmaz.

## 2. Transaction'lar {#_2-transactions}

**Transaction**, bir servisin giriş span'idir. Processor bir span için şu durumda `is_entry = true` ayarlar:

1. `kind` değeri `server` veya `consumer` ise ve
2. parent yoksa veya uzaktaysa:
   - `parent_span_id` boşsa veya
   - span flag'leri `SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE` ve `SPAN_FLAGS_CONTEXT_IS_REMOTE` taşıyorsa (parent uzakta) veya
   - flag'ler parent'ın yerel olduğunu söylemiyorsa ve parent span aynı OTLP export isteğinde aynı
     `service.name` ile yoksa.

Parent araması export isteğiyle sınırlıdır, böylece dönüşüm Kafka kaydının bir fonksiyonu olarak kalır (deduplication
token'ları, [kafka.md](/tr/contracts/kafka)). Aynı servisin *başka* bir istekteki yerel span'i altında `is_remote` flag'leri olmadan
duran bir server span, giriş span'i sayılır (SDK'lar pratikte bir servisin yerel span'lerini tek batch'te gönderir).

### 2.1 Transaction türü ve adı {#_2-1-transaction-type-and-name}

| `transaction_type` | Koşul (ilk eşleşme) | `transaction_name` |
|---|---|---|
| `web` | `http.request.method` veya `http.method` var | `<METHOD> <route>`; `route` = `http.route`, yoksa `url.path`, `http.target` veya `url.full`/`http.url` yolunun normalize hâli (§2.2); yoksa span adı |
| `rpc` | `rpc.system` var | `<rpc.service>/<rpc.method>` (eksik parçalar span adından) |
| `messaging` | `messaging.system` var | `<messaging.operation.name \| messaging.operation \| "process"> <destination>`; `destination` = `messaging.destination.name` (yol segmenti gibi normalize edilir) |
| `other` | diğer durumlar | span adı |

`METHOD` büyük harfe çevrilir; bilinmeyen metotlar `_OTHER` olur (OTel). Adlar 256 byte'a kırpılır (UTF-8 güvenli).

### 2.2 Yol normalizasyonu {#_2-2-path-normalization}

Query string ve fragment kaldırılır; boş segmentler birleştirilir; her segment şu durumlarda değiştirilir:

| Segment | Yerine |
|---|---|
| UUID (8-4-4-4-12 hex, her harf biçimi) | `{uuid}` |
| ondalık sayı (isteğe bağlı işaretli) | `{id}` |
| ≥ 16 karakterlik hex string veya rakam ve yalnızca `a-f` harfi içeren ≥ 8 karakter | `{hex}` |
| rakam içeren ≥ 20 karakter `[A-Za-z0-9_-]` (token'lar, base64 id'ler) | `{token}` |
| bir e-posta adresi | `{email}` |

8'den fazla segment: gerisi `/…` ile değiştirilir. Örnek: `/orders/42/items/5f0e…?x=1` → `/orders/{id}/items/{hex}`.

## 3. Hatalar {#_3-errors}

Bir span için `status.code = ERROR` ise veya `kind = server` iken `http.response.status_code`
(`http.status_code`) ≥ 500 ise `is_error = true` olur. Client span'ler yalnızca status code ile hata sayılır (OTel SDK'ları client 4xx/5xx için ERROR ayarlar).

**Exception'lar**, `exception.type`, `exception.message`,
`exception.stacktrace` taşıyan `exception` adlı span event'leridir. Bir span'in **son** exception event'i o span'in hatasıdır.

### 3.1 Hata grubu fingerprint'i {#_3-1-error-group-fingerprint}

Bir servisin her hata span'i için (yalnızca giriş span'leri değil):

```
error_type    = exception.type                     | "HTTP <status>" (HTTP status ≥ 400) | "error"
message       = normalize(exception.message         | status.message | "")
frame         = top in-app frame of exception.stacktrace (§3.2), "" if none
error_group_id = xxhash64(service_name \0 service_namespace \0 environment \0 error_type \0 message \0 frame)
```

`normalize`: tırnaklı string'ler (`'…'`, `"…"`, `` `…` ``, kelime içinde başlamayan, yani `can't` kalır) → `'?'`; UUID'ler →
`<uuid>`; e-posta adresleri → `<email>`; IPv4 (portlu) → `<ip>`; rakam içeren ≥ 8 hex dizisi → `<hex>`;
kelime dışı karakterlerle ayrılmış sayılar → `<n>` (`shard4` kalır); boşluklar birleştirilir; en fazla 512 byte.
**Fingerprint v2** (APM GA, D-068) yukarıdaki kurallardan önce şunları ekler: ISO 8601 tarih-saatleri (`2026-09-14T10:11:12.345Z`,
`2026-09-14 10:11:12+03:00`) → `<ts>`; bir `word_` önekinin id'si (`req_8f3a9c2b1d` → `req_<id>`, `order_123456` →
`order_<id>`), yalnızca ≥ 4 rakamsa veya rakam ile harf karışık ≥ 6 karakterse (`shard_4`, `user_not_found`
kalır). Bu kalıpları içermeyen mesajlar v1 grup id'lerini korur; mesajı veya üst frame'i değişen gruplar yükseltmeden sonra bir kez yeni
id alır (eski grup yeni oluşum almayı bırakır ve retention ile sona erer). Grup
id'si bir `UInt64`'tür, 16 küçük harf hex rakam olarak sunulur (`group_id`). `error_message` (saklanan) normalize edilmiş
mesajdır; örneklerin ham mesajı ve stack'i span'lerden gelir.

### 3.2 Üst in-app frame {#_3-2-top-in-app-frame}

Runtime veya kütüphane kodu **olmayan** ilk stack frame (stack'in tepesi). Bir frame, dosya
yolu `/vendor/`, `node_modules`, `/usr/local/go/`, `/usr/lib/go`, `/go/pkg/mod/`, `GOROOT`, `node:` önekli `internal/`
(Node), `<anonymous>`, `/usr/share/php`, `/usr/lib/python`, `site-packages` içeriyorsa veya fonksiyon
`runtime.`, `net/http.`, `panic` ile başlıyorsa kütüphane kodudur. Desteklenen biçimler: Go (`pkg.Func(...)` + `\t/path/file.go:12 +0x1f`), Node/JS
(`    at fn (/app/x.js:10:5)`), PHP (`#0 /app/src/X.php(12): Cls->fn()`), Java (`\tat a.b.C.fn(C.java:12)`), Python
(`  File "/app/x.py", line 12, in fn`). Frame anahtarı **satır numarası olmadan** `function@file`'dır (deploy'lar arasında
kararlı). Frame bulunamazsa → herhangi türden ilk frame; stack yoksa → `""`.

Fingerprint v2 frame anahtarını daha da normalize eder (build'e ve deploy'a özgü parçalar): Go func literal'leri
`pkg.fn.func1.2` → `pkg.fn.func` ve generic örneklemeler `Fn[go.shape.int]` → `Fn[...]`; Java gizli lambda
sınıfları `Foo$$Lambda$123/0x…` → `Foo$$Lambda`, `lambda$handle$0` → `lambda$handle`, `$Proxy12`,
`GeneratedMethodAccessor12`, `$$EnhancerBySpringCGLIB$$1a2b` numarasız; PHP `class@anonymous…` →
`class@anonymous`; dosyada: `file://` öneki, query string ve fragment kaldırılır, ≥ 8 rakam, ≥ 12
hex karakter veya UUID olan dizinler (release dizinleri) → `<id>`, paketlenmiş JS/TS dosyalarının içerik hash'leri (`main.3f2a1b9c.js` →
`main.js`).

### 3.3 Hata grubu boyutları {#_3-3-error-group-dimensions}

`apm_error_group_dims` (şema `0035_apm_ga`) bir grubun her hata span'ini boyut başına, retention boyunca sayar:
`version` (resource `service.version`, boş değerler atlanır), `host` (`host_id`), `container` (küçük harfli
`container.id`) ve `transaction` (`transaction_name`, yoksa span adı): `first_seen`, `last_seen`, ağırlıklı
`count`, `samples`. Hata grubu detayı boyut başına en üstteki 20 değeri gösterir ("etkilenen sürümler, host'lar,
container'lar, transaction'lar"); regresyon tespiti (§3.4) sürüm başına en yeni oluşumu okur.

### 3.4 Hata gelen kutusu iş akışı {#_3-4-error-inbox-workflow}

Her hata grubunun PostgreSQL'de bir iş akışı durumu vardır (`apm_error_group_states`, [postgres.md](/tr/contracts/postgres#apm-error-workflow-0025_apm_error_workflow)),
anahtarı organizasyon ve `group_id`'dir; satırı olmayan bir grup **unresolved** ve atanmamıştır.

| Alan | Anlamı |
|---|---|
| `status` | `unresolved` · `resolved` · `ignored` |
| `assignee` | organizasyonun bir üyesi veya kimse |
| `resolved_at`, `resolved_in_version` | resolved iken ayarlanır; sürüm isteğe bağlıdır |
| `regressed_at`, `regression_count` | son otomatik yeniden açılma ve kaç kez olduğu |

Geçişler (herhangi biri, member, admin veya owner tarafından, o role sahip bir API anahtarı da dahil; değişen grup başına from/to `details.status`/`assignee_user_id` ile
audit olayı `apm.error_group.update`): resolve etmek `resolved_at = now` ayarlar (zaten resolved bir grup aynı sürümle
yeniden resolve edilirse korunur); unresolve veya ignore etmek `resolved_at` ve sürümü temizler. Değişiklik yapmayan bir
işlem hiçbir şey yazmaz. Ignored gruplar ne olursa olsun ignored kalır (`apm_error` yeni grup
alert'lerinden hariç tutulurlar).

**Regresyon (otomatik yeniden açılma).** Resolved bir grup, `resolved_at`'ten sonra oluşursa yeniden açılır: status `unresolved`,
`regressed_at` = koşulu sağlayan en yeni oluşum, `regression_count + 1`, oluşum zamanı ve sürümüyle `openlog-apm` tarafından
audit olayı `apm.error_group.regressed`.

- `resolved_in_version` yoksa: herhangi bir oluşum (grubun veya herhangi bir sürümün `last_seen`'i);
- `resolved_in_version` V ile: V'de veya V'nin ilk span'inde ya da sonrasında ilk görülen (`apm_service_versions_1m` içinde, §12) bir
  sürümde bir oluşum, yani daha yeni deployment'lar; V henüz bildirim yapmadıysa `resolved_at`'ten sonra ilk görülen bir sürüm.
  Zaten çalışmakta olan sürümlerin oluşumları, boşalmakta olan eski instance'lardır ve grubu yeniden açmaz.
  Sürümler ada göre değil ilk görünüşe göre karşılaştırılır (semver varsayımı yok); `service.version` olmayan span'ler
  bir sürümde resolve edilmiş grubu regrese edemez.

Tespit bir arka plan job'u değildir: API bir gelen kutusu veya grup detayı her okunduğunda resolved grupları kontrol eder ve `event: regressed` olan her
`apm_error` alert kuralı her değerlendirmede kapsamındaki resolved grupları kontrol eder. İlk
dedektör geçişi koşullu bir update ile kalıcı yapar (`status = 'resolved' AND resolved_at < occurrence`), böylece
eşzamanlı dedektörler bir grubu bir kez yeniden açar.

**Gelen kutusu okumaları.** Adaylar, aralıkta oluşumu olan gruplardır (en fazla 2000, en sık olan başta); filtre
resolved veya ignored grupları ya da bir assignee'yi seçtiğinde, durum satırı eşleşen gruplar (en fazla 500)
`count` 0 ile eklenir. Filtreler: `status` (liste), `assignee` (`any`, `none`, `me`, kullanıcı id'si), `q` (tür, mesaj, servis,
span adı içinde alt string), `sort` (`count`, `last_seen`, `first_seen`, `type`, `service`, `status`, `assignee`, `comments`), `dir`, `limit` ve `offset`. `counts`, assignee ve `q` filtrelerinden sonra status başınadır.
Yorumlar (1–4000 byte, `apm_error_group_comments`) ve grubun audit olayları ("activity") detayda gösterilir.

## 4. Gecikme, throughput, Apdex {#_4-latency-throughput-apdex}

**Örnekleme düzeltmesi.** Her span'in `sample_weight = 1 / p` değeri vardır; `p` örnekleme olasılığıdır ve şunlardan ilk bulunandır:

1. tracestate `ot=th:<hex>` (OTel olasılık örnekleme eşiği): `p = 1 − T / 2^56`, `T` = sağdan sıfırlarla
   14 rakama tamamlanmış hex; `th:0` → `p = 1`;
2. tracestate `ot=p:<n>` (eski ikinin kuvveti): `p = 2^−n` (`n` ≤ 62; `p:63` → ağırlık 0, sayılmaz);
3. span attribute'u, sonra resource attribute'u `sampling.ratio` ((0, 1] aralığında float);
4. aksi halde `p = 1`.

`p`, [1e-6, 1] aralığına sıkıştırılır. Aşağıdaki tüm sayımlar **ağırlıklıdır** (`sample_weight` toplamları); `samples` saklanan span'leri sayar.

### 4.2 Tail sampling (D-075) {#_4-2-tail-sampling-d-075}

`OPENLOG_TAILSAMPLING_ENABLED=true` olduğunda, `openlog-sampler` processor span'leri görmeden önce trace başına karar verir
(işletim: docs/operations/tail-sampling.md; topic'ler: kafka.md). Çıktısı yukarıdaki (1) kuralını geçerli tutar:

- **Politika** (organizasyon başına, PostgreSQL `tail_sampling_policies`, API `GET/PUT /api/v1/apm/sampling`; yoksa
  `OPENLOG_TAILSAMPLING_DEFAULT_POLICY`; o da yoksa hepsini tut): `enabled`, `baseline_ratio`, `max_spans_per_second`, sıralı
  `error`, `latency`, `service`, `route`, `attribute` türünde `rules`, her biri bir tutma `ratio` değeriyle (varsayılan 1). Eşleşen
  **ilk** kural tail olasılığı `q`'yu verir; eşleşme yoksa → `baseline_ratio`; `enabled: false` → `q = 1`.
- **Rate limit:** tenant'ın instance üzerindeki beklenen tutulan span hızı `max_spans_per_second`'ı aştığında,
  `q ← q × limit / rate`.
- **Karar:** trace'in head olasılığı `h`, span'lerinin en büyük `ot=th`/`ot=p` olasılığıdır. Varsa,
  trace ancak ve ancak randomness `R` (tracestate `ot=rv`, yoksa trace id'nin son 7 byte'ı) ≥ `T(h·q)` ise tutulur,
  yani head kararı verildiğinde `q` olasılığıyla (OTel consistent probability sampling). Yoksa,
  ancak ve ancak `xxhash64("openlog-tail-sampling/v1:" ‖ trace id) >> 8 < q · 2^56` ise tutulur. `q`, her span için
  `h_span · q ≥ 1e-6` olacak şekilde yükseltilir (yukarıdaki sıkıştırma).
- **Ağırlıklar:** `q < 1` ile her tutulan span tracestate `ot=th:<T(h_span · q)>` alır (`th`/`p` alt anahtarları değiştirilir, diğer
  `ot` alt anahtarları ve vendor'lar korunur, `ot` başa taşınır); burada `h_span`, span'in (1)–(4) kurallarına göre kendi olasılığıdır.
  Yani `sample_weight = 1 / (h_span · q)` ve her ağırlıklı sayım örneklenmemiş trafiğin yansız (Horvitz–Thompson) tahmini
  olarak kalır. `q = 1` ile span'ler değişmez.
- Karara bağlanmış bir trace'in **geç span'leri** (karar cache'i, `OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL`) aynı `q` ile
  kararı izler.
- **Önizleme** (`POST /api/v1/apm/sampling/preview`): politika pencerenin en fazla 20 000 saklı trace'ine uygulanır;
  her trace span'lerinin `max(sample_weight)` değeriyle sayılır; tutulma oranları `Σ w·q / Σ w` (trace'ler) ve
  `Σ w·spans·q / Σ w·spans` (span'ler) olur.

| Metrik | Tanım (bir zaman aralığında, servis veya transaction başına) |
|---|---|
| `requests` | Giriş span'lerinin Σ ağırlığı |
| `throughput` (rpm) | `requests / minutes in range` |
| `errors` | `is_error` olan giriş span'lerinin Σ ağırlığı |
| `error_rate` | `errors / requests` (istek yoksa 0), 0..1 |
| `avg_ms` | Σ(weight × duration) / requests |
| `p50_ms` / `p95_ms` / `p99_ms` | gecikme histogramından (§4.1) giriş span süresinin ağırlıklı quantile'ı |
| `time_consumed_ms` | Σ(weight × duration) ("en çok zaman harcayan" sıralama anahtarı) |
| `apdex` | `(satisfied + tolerating / 2) / requests`; satisfied: süre ≤ T **ve hata değil**, tolerating: T < süre ≤ 4T ve hata değil, frustrated: gerisi (hatalar New Relic'teki gibi frustrated sayılır); istek yoksa `null` |

**Apdex T** servis başına ayarlanır (§7, PostgreSQL `apm_service_settings`), varsayılan **500 ms**
(`OPENLOG_APM_DEFAULT_APDEX_T`). Arama: tam (`service_name`, `service_namespace`, `environment`), yoksa
(`service_name`, `''`, `''`), yoksa varsayılan. T **sorgu anında** uygulanır, yani değiştirmek geçmişi yeniden hesaplar.

### 4.1 Gecikme histogramı {#_4-1-latency-histogram}

Süreler ikinin her kuvveti için 8 bucket'lı log-lineer bucket'larda sayılır (oran 2^(1/8) ≈ 1.0905):

```
bucket(d) = clamp(ceil(8 · log2(d_ms)), −80, 200)        d_ms = duration_ns / 1e6;  d_ns ≤ 1000 → −80
bucket b covers (2^((b−1)/8), 2^(b/8)] ms                (b = −80: ≤ ~1 µs, b = 200: ≥ 2^24.875 ms ≈ 8.3 h)
```

`sumMap` ile birleştirilen `duration_hist Tuple(Array(Int16), Array(Float64))` = (bucket, Σ weight) olarak saklanır.

- **Quantile q:** bucket'ları sırayla dolaş; kümülatif ağırlığın `q · total`'a ulaştığı bucket'ta sınırları arasında
  geometrik interpolasyon yap. Sonuç, ham span'ler üzerinde `quantileExactWeighted(q)` ile aynı bucket'ta kalır,
  yani göreli fark **%9,1'in altındadır** (bir bucket genişliği) ve yüzlerce örnekle genellikle %1'in altındadır (ölçülen:
  %0,1–0,5, `internal/apm/integration_test.go`).
- **Apdex:** satisfied/tolerating "hata değil" gerektirdiğinden MV hata olmayan span'ler için ikinci bir histogram tutar
  (`ok_hist`); ≤ T sayımları tamamen T'nin altındaki bucket'ları artı T'yi içeren bucket'ın lineer payını kullanır (eşik üzerinde aynı ±%4,4
  sınırı).
- Referans uygulamalar: Go `apm.HistQuantile`, `apm.HistApdex` (`internal/apm/hist.go`); ClickHouse SQL tarifi
  §10'da.

## 5. Servis haritası {#_5-service-map}

Düğümler: servisler (`type=service`) ve bağımlılıklar: `db` (`<db.system>` veya `<db.system>/<db.namespace|db.name>`),
`messaging` (`<messaging.system>/<messaging.destination.name>`), `external` (`<server.address>[:<server.port>]`).

**Attribute edge'leri** (processor + MV, bir servisin her `client` veya `producer` span'i), ilk eşleşme:

| Hedef türü | Koşul | Hedef adı |
|---|---|---|
| `service` | `peer.service` | değeri |
| `db` | `db.system` (`db.system.name`) | yukarıdaki gibi |
| `messaging` | `messaging.system` | yukarıdaki gibi |
| `external` | `server.address`, `net.peer.name` veya `url.full`/`http.url` host'u | `host[:port]` (80/443 için port yazılmaz) |

**Trace'e bağlı edge'ler** (edge bağlama job'u, §6): A servisinin bir `client`/`producer` span'i, çocuğu olan `server`/`consumer`
span'i B ≠ A servisine ait olan. Job, client span'in attribute hedefini `via` olarak kaydeder (örneğin `orders:8080`).

(source, target) başına API birleştirme kuralları: trace'e bağlı `service` edge'leri attribute `service` edge'lerine üstün gelir (ikisi de aynı
client span'leri sayar); A'dan bir attribute `external` edge'i, A'dan trace'e bağlı bir edge'in `via` değeri onun hedef adına eşitse
gizlenir (external adres o servistir).

Edge RED: `calls` = client span'lerin Σ ağırlığı, `errors` (`is_error`), client tarafı süresinden `avg_ms`, `p95_ms`.

### 5.1 Harita filtreleri, sayılar ve transaction yolları (GA) {#_5-1-map-filters-counts-and-transaction-paths-ga}

- `service` olmadan, `namespace` ve `environment` tüm haritayı filtreler: o namespace/environment'ın transaction'ları, servisleri, attribute edge'leri ve
  trace'e bağlı edge'leri (kaynak ve hedef).
- Servis düğümleri `host_count` ve `container_count` taşır: servise bağlı (§1), `last_seen` değeri `from`'dan beri olan farklı `host_id` / `container.id`
  sayısı. Bağımlılık düğümlerinde 0'dır.
- **Bir transaction'ın yolu** (`GET /apm/map/path`): aralıkta transaction'ın giriş span'ine sahip en fazla 50 trace;
  span'leri (±10 dakika) dokundukları her servis ve bağımlılığın düğüm id'lerini ve
  client/producer attribute edge'lerinin edge id'lerini (`source->type:name`, `peer.service` aynı trace'in bir servisine çözümlenir) ve
  servisler arası client/producer → server/consumer parent bağlarını verir. Id'ler `GET /apm/map`'inkilerdir; arayüz
  kesişimi vurgular (birleştirme kurallarıyla gizlenen bir external edge'in karşılığı yoktur).
- Arayüz sürüklenen düğüm konumlarını kullanıcı başına tarayıcıda (`localStorage`) tutar, sunucuda değil.

## 6. Edge bağlama job'u {#_6-edge-linking-job}

- **Api leader**'ında çalışır (`postgres.Leader`; `OPENLOG_AUTH_MODE=static` ile her api sürecinde), her
  `OPENLOG_APM_LINK_INTERVAL` (1m) bir, ve **her shard'da** çalışır: shard başına bir replica'ya bağlanır (`system.clusters`'tan)
  ve orada `INSERT INTO apm_service_links_1m_local SELECT … FROM spans_local c JOIN spans_local s` çalıştırır.
  Span'ler `(tenant_id, trace_id)` ile shard'lanır, yani bir client span ile çocuğu her zaman aynı shard'dadır:
  join tam ve yereldir.
- Pencere: `[now − OPENLOG_APM_LINK_LOOKBACK (10m, 1m–24h), now − OPENLOG_APM_LINK_DELAY (1m))` içindeki tam dakikalar. Her
  çalıştırma pencerenin her dakikasını yeniden hesaplar (lookback'e kadar geç gelen span'ler alınır).
- **Günlük catch-up** (`OPENLOG_APM_LINK_CATCHUP_ENABLED`, varsayılan açık): UTC gününde bir kez,
  `OPENLOG_APM_LINK_CATCHUP_AT` (varsayılan `03:00` UTC, düşük trafik) saatinde başlayarak leader önceki UTC gününün tamamını
  `OPENLOG_APM_LINK_CATCHUP_BATCH` (1h) pencerelerinde yeniden bağlar; tüm shard'lar üzerinde bir seferde bir pencere, `max_threads=2` ile ve pencereler arasında 10 sn
  duraklamayla, normal çalıştırmaların yanında (onlar aynı dakikaları asla kapsamaz). Lookback'ten sonra ama geçişten önce
  gelen span'ler bağlanır. Geçiş, zamanından 6 saate kadar sonra başlayabilir (o pencerede yeni bir leader veya yeniden başlatma
  onu tekrar çalıştırır; zararsızdır, sonuç aynıdır); başarısız geçiş en fazla 30 dakikada bir yeniden denenir.
  Geç çağrılar = pencere başına geçişten sonraki ağırlıklı bağlı çağrılar eksi öncekiler (kullanılan replica'da `FINAL`; replica'lar geride
  kaldığında yaklaşık): `openlog_apm_link_late_calls_total`. Daha geç span'leri late-span re-link halleder.
- **Late-span re-link** (`OPENLOG_APM_RELINK_ENABLED`, varsayılan açık; şema `0020_apm_relink_queue`). *Processor:* `now` zamanında bir
  chunk'ı dönüştürdükten sonra, bağlamada rol alan her span (`sample_weight > 0` olan bir servisin `client`/`producer` span'i, onun client dakikası `m`;
  veya parent'ı olan bir servisin `server`/`consumer` span'i, child aralığı `[m, m + 6m)` span'i içeren her client dakikası
  `m`, yani en fazla 6 dakika) `m < now − OPENLOG_APM_RELINK_AFTER` dakikalarına katkıda bulunur (varsayılan `OPENLOG_APM_LINK_LOOKBACK`; daha yeni dakikaları normal
  çalıştırmalar kapsar). Dakikalar chunk içinde (tenant, minute) başına tekilleştirilir ve chunk'ın span'lerinden sonra, chunk'ın
  deduplication token'ıyla, satır olarak (`tenant_id`, `minute`, `trace_id` = ilk geç trace, `spans`,
  `enqueued_at = now`) `apm_relink_queue` Distributed tablosuna yazılır (sharding key span'ler gibi `cityHash64(tenant_id, trace_id)`,
  MergeTree, `enqueued_at` üzerinde 8 gün TTL). `OPENLOG_APM_RELINK_MAX_AGE`'den (`168h`) eski span'ler yalnızca
  sayılır. *Leader:* her `OPENLOG_APM_RELINK_INTERVAL` (5m) bir arka plan geçişi, Distributed tablo üzerinden,
  `(watermark − 10m, now − 2m]` içinde kuyruğa alınmış satırı olan, normal pencerenin başlangıcından eski ve
  `OPENLOG_APM_RELINK_MAX_AGE`'den eski olmayan farklı dakikaları okur (2 dakikalık oturma processor saat kayması
  ve replikasyonu karşılar; 10 dakikalık örtüşme geç görünür olan satırları yeniden okur). Bir dakika, onu en son yeniden bağlayan geçişin okuma sınırından
  daha yeni bir satırı varsa zamanı gelmiş sayılır. Geçiş başına en fazla `OPENLOG_APM_RELINK_MAX_MINUTES` (120) zamanı gelmiş dakika, en eskiden başlayarak,
  bağlama ifadesi ve `max_threads=2` ile bitişik dakika pencerelerinde (en fazla `OPENLOG_APM_LINK_CATCHUP_BATCH`) **her** shard'da yeniden bağlanır
  (kuyruk shard başına değildir; tek bir geç batch genellikle çok sayıda trace'e yayılır); geri
  kalanlar ve başarısız pencerelerin dakikaları zamanı gelmiş kalır ve watermark'ı tutar. Watermark ve dakika başına sınırlar
  bellekte durur: yeni bir leader (veya yeniden başlatma) kuyruğun son saatini yeniden okur (yeniden bağlama idempotent'tir). Catch-up ve
  re-link geçişi asla aynı anda çalışmaz (sonraki olan bir sonraki döngü yinelemesini bekler).
  `OPENLOG_APM_RELINK_AFTER` değerini api'nin `OPENLOG_APM_LINK_LOOKBACK` değerine ≤ ayarla (processor ve api'de aynı ortam); daha büyük
  bir değer normal pencere ile kuyruktaki dakikalar arasında boşluk bırakır. Normal çalıştırmalarda olduğu gibi, client span'inin dakikasından önce
  başlayan bir child span (saat kayması) yalnızca ikisi aynı penceredeyse bağlanır. Processor'ın tabloya
  ihtiyacı vardır: processor'ı yaymadan önce migration 0020'yi uygula (her zaman olduğu gibi önce expand).
- Idempotent: `apm_service_links_1m_local` bir `ReplicatedReplacingMergeTree(computed_at)`'tir; yeniden çalıştırma o shard'da aynı (tenant, source, target, minute) için
  önceki sonucu değiştirir. Okumalar `FINAL` kullanır (Distributed tablo tarafından shard başına uygulanır, sonra
  shard'lar arasında toplanır).
- Metrikler: `openlog_apm_link_runs_total{result}`, `openlog_apm_link_rows_total`, `openlog_apm_link_duration_seconds`,
  `openlog_apm_link_lag_seconds` (now − son başarıyla bağlanan pencerenin sonu),
  `openlog_apm_link_catchup_runs_total{result}`, `openlog_apm_link_catchup_duration_seconds`,
  `openlog_apm_link_late_calls_total`. Late-span re-link: processor `openlog_apm_relink_enqueued_total` (oluşturulan (tenant, minute)
  satırları), `openlog_apm_relink_too_old_total` (en büyük yaştan eski geç span'ler); api
  `openlog_apm_relink_runs_total{result=ok|error|skipped}`, `openlog_apm_relink_minutes_total`,
  `openlog_apm_relink_backlog_minutes` (son geçişten sonra kalan zamanı gelmiş dakikalar), `openlog_apm_relink_late_calls_total`,
  `openlog_apm_relink_duration_seconds`; kuyruk insert'leri ayrıca
  `openlog_processor_rows_inserted_total{table="apm_relink_queue"}` içinde görünür.

## 7. Veritabanı sorguları {#_7-database-queries}

`db.system` (`db.system.name`) taşıyan `client` türündeki her span bir DB çağrısıdır (DB çağrılarını
`internal` span olarak gönderen instrumentasyonlar sayılmaz: bir ORM'in client çocuğu olan internal span'i iki kez sayılırdı), **bağlantı yönetimi
span'leri hariç**: statement yok (`db.query.text`, `db.statement` eksik veya boş) ve operasyon
(`db.operation.name` | `db.operation` | span adı, küçük harfli) `connector` içeriyor veya son kelimesi (`.`, ` `, `/`, `:` sonrası)
`connect`, `connection`, `reconnect`, `disconnect`, `close`, `ping`, `reset_session`,
`resetsession`, `reset`, `acquire`, `release` veya `handshake`; örneğin otelsql `sql.connector.connect`,
`sql.conn.reset_session`, `sql.conn.ping`, `db.connect`, `pg.connect`. Bunlar DB sütunu almaz (`db_system` … `db_operation`
boş kalır, yani `apm_db_queries_1m` içinde olmazlar); veritabanına attribute edge'leri (§5) korunur. Bu kuraldan önce
toplanmış satırlar (kuralı olmayan processor sürümleri) TTL'lerine kadar kalır.
`db_statement_normalized` = normalize(`db.query.text` | `db.statement` | span adı):

- yorumlar kaldırılır; string literal'ler (`''` kaçışlı `'…'`, `E'…'`, `$$…$$`) → `?`; sayılar, hex (`0x…`), karşılaştırmalardaki
  boolean'lar → `?`; `$1`, `:name`, `@p1`, `?` yer tutucuları → `?`;
- `IN (?, ?, …)` → `IN (?)`; tekrarlanan `VALUES (…), (…)` → `VALUES (…)`;
- anahtar/değer depoları (`redis`, `memcached`): komut kelimesi büyük harfe çevrilir, her argüman → `?` (`GET products:42` →
  `GET ?`);
- boşluklar tek boşluğa indirilir, sondaki statement sonlandırıcılar (`;`) kaldırılır, en fazla 2048 byte (sonra `…`).

Aynı fonksiyon veritabanı sorgu izlemenin statement'larını da normalize eder (db-monitoring.md §4.1), yani
`db_statement_normalized`, bir servisin çağrıları ile sunucunun statement istatistikleri arasındaki join anahtarıdır.

`db_operation` = `db.operation.name` | `db.operation` | statement'ın ilk anahtar kelimesi (büyük harfli).


### 7.1 Servisler arası görünüm ve execution plan'ları {#_7-1-across-services-and-execution-plans}

`GET /apm/db-queries`, `apm_db_queries_1m` tablosunu servisler arası okur (api.md "Lists"te anlatıldığı gibi sıralı ve sayfalı; `time_share` sayfaya göredir), böylece statement'lar veritabanı sunucusuna bağlantı olmadan da **Databases** ekranında görünür. Bunlar
uygulamanın süreleridir, ağı ve havuzdan bağlantı beklemeyi içerir, bu yüzden sunucunun görünümünün (db-monitoring.md) yanında gösterilir ve asla onunla birleştirilmez.
Wait event'leri ve oturumlar sunucu gerektirir: bunları tek bir uygulama göremez.

**Plan'lar.** Bir ajan `EXPLAIN`'i uygulamanın kendi bağlantısı üzerinden çalıştırabilir, openlog'a hiçbir kimlik bilgisi vermeden plan elde edilebilmesinin yolu budur. `openlogpgx` bunu `WithExplain`
ayarlıysa ve yalnızca o zaman yapar, çünkü bu uygulamanın istemediği bir sorgudur.

- Yalnızca `SELECT`, bir seferde bir statement ve asla `ANALYZE` değil, çünkü o statement'ı çalıştırır.
- Eşikten yavaş bir statement saatte en fazla bir kez, teker teker, isteğin yolundan ayrı, 2 saniye limitle explain edilir.
- Parametreliyse `EXPLAIN (GENERIC_PLAN)` kullanır, yani PostgreSQL 16 veya üstü: parametre değerleri `EXPLAIN`'e asla gönderilmez, yani plan'da yer almaz. Daha eski bir sunucu yalnızca
  parametresiz statement'lar için plan alır. Generic plan, yavaş çağrının gerçekte kullandığından farklı olabilir.
- Plan'daki literal'ler süreçten çıkmadan önce `?` ile değiştirilir ve plan 16 KiB'de kesilir. Geçici tablolar ve yavaş çağrının bağlantısına veya transaction'ına özel diğer her şey plan'lanamaz,
  böyle bir statement'ın sessizce plan'ı olmaz.
- Plan, yavaş çağrının trace'inde `db.explain` adlı bir `internal` span üzerinde, `openlog.db.plan*` attribute'larıyla ve `db.*` olmadan taşınır, yani veritabanı çağrısı sayılmaz. Dolayısıyla
  trace retention ve örneklemesini izler ve `openlog.db.plan.statement` normalize edilerek geri okunur (aralıktaki en yeni 200 böyle span ile sınırlı).

Diğer ajanlar henüz plan göndermiyor.

## 8. Depolama (ClickHouse) {#_8-storage-clickhouse}

Tüm tablolar, `_local` olmadan aynı adı taşıyan bir `Distributed` tabloya sahip `Replicated*` `_local` tablolardır ve
`spans_local` üzerindeki materialized view'larla (veya link job'uyla) beslenir. Doğrudan shard insert'leriyle çalışırlar (D-018), çünkü MV
span bloğunu alan shard'da tetiklenir.

| Tablo | Engine | Anahtar (ORDER BY) | İçerik | TTL |
|---|---|---|---|---|
| `apm_transactions_1m` | AggregatingMergeTree | tenant, service triple, minute, type, name | requests, samples, errors, duration_sum_ms, duration_max_ms, duration_hist, ok_hist | 30 g |
| `apm_service_edges_1m` | AggregatingMergeTree | tenant, source triple, minute, target_type, target | calls, errors, duration_sum_ms, duration_hist | 30 g |
| `apm_service_links_1m` | ReplacingMergeTree(computed_at) | tenant, source triple, minute, target triple, via | calls, errors, duration_sum_ms, duration_hist | 30 g |
| `apm_db_queries_1m` | AggregatingMergeTree | tenant, service triple, minute, db_system, statement | calls, errors, duration_sum_ms, duration_max_ms, duration_hist, operation, db_name | 30 g |
| `apm_errors_1m` | AggregatingMergeTree | tenant, service triple, minute, error_group_id | count (ağırlıklı), samples | 30 g |
| `apm_error_groups` | AggregatingMergeTree | tenant, service triple, error_group_id | first_seen, last_seen, count, error_type, message, stacktrace, span_name, örnek trace id'leri (≤ 10) | last_seen'den 30 g sonra |
| `apm_services` | AggregatingMergeTree | tenant, service triple | first_seen, last_seen, version, language, sdk, resource attribute'ları | last_seen'den 30 g sonra |
| `apm_service_hosts` | AggregatingMergeTree | tenant, host_id, service triple | first_seen, last_seen, host_name | last_seen'den 30 g sonra |
| `apm_service_containers` | AggregatingMergeTree | tenant, container_id, service triple | first_seen, last_seen, host_id, container_name | last_seen'den 30 g sonra (sabit, `OPENLOG_APM_RETENTION_DAYS` değil; şema 0009) |
| `apm_relink_queue` | MergeTree (processor yazar, sharding key `cityHash64(tenant_id, trace_id)`) | enqueued_at, tenant, minute | geç span'lerin client dakikaları (§6 "Late-span re-link"): trace_id, spans | enqueued_at'ten 8 g sonra (sabit; şema 0020) |
| `apm_error_group_dims` | AggregatingMergeTree | tenant, service triple, error_group_id, dim, value | version/host/container/transaction başına first_seen, last_seen, count (ağırlıklı), samples (§3.3) | last_seen'den 30 g sonra (sabit; şema 0035) |
| `apm_service_versions_1m` | AggregatingMergeTree | tenant, service triple, minute, service_version | span_count, first_seen, last_seen (§12) | 30 g (sabit; şema 0035) |

**Retention.** Yukarıdaki TTL'ler (30 g), `OPENLOG_APM_RETENTION_DAYS` (1–3650) varsayılanıdır. `openlog-migrate` (ve
`OPENLOG_MIGRATE_ON_START` ile `openlog-allinone`) migration dosyalarını uyguladıktan sonra bunu
`openlog.table_settings` içinde kayıtlı değerle (`apm_retention_days`; expand migration `0008_table_settings` oluşturur; satır yoksa
= 30) karşılaştırır ve farklıysa sekiz `_local` tablonun her biri için
`ALTER TABLE openlog.<table> ON CLUSTER '<cluster>' MODIFY TTL <timestamp | toDateTime(last_seen)> + INTERVAL <days> DAY` çalıştırır,
sonra yeni değeri kaydeder (yarıda kalan bir hatayı sonraki çalıştırma onarır; her ALTER idempotent'tir). `-plan`
bekleyen değişikliği yazdırır. **ON CLUSTER:** ifade distributed DDL kuyruğu üzerinden cluster'ın her host'una gider
ve migrate onları bekler (`distributed_ddl_task_timeout`, varsayılan 180 sn; kapalı bir host geri geldiğinde
uygular). Tablolar `Replicated*`'dır: metadata değişikliği shard başına Keeper üzerinden koordine edilir, yani her
replica aynı TTL ile biter. ClickHouse sonra TTL'i replica başına asenkron bir mutation ile uygular
(`system.mutations`): `ttl_only_drop_parts` ve günlük partition'larla retention'ı kısaltmak süresi dolmuş tüm
part'ları düşürür (disk alanı dakikalar ile saatler içinde boşalır), uzatmak henüz silinmemiş part'ları tutar
(zaten kaldırılmış veri geri gelmez). Migration çalıştıran her süreçte aynı değeri ayarla.

**Sharding doğruluğu.** Span'ler `cityHash64(tenant_id, trace_id)` ile shard'lanır, yani bir aggregate anahtarının satırları
(örneğin bir servisin bir dakikadaki transaction'ı) **tüm** shard'lara ve her shard'da birkaç part'a yayılır.
Her sütun birleştirilebilir bir aggregate'tir (`sum`, `min`, `max`, `sumMap`, `groupUniqArray`, `anyLast`) ve **her okuma
Distributed tablo üzerinden `GROUP BY` ile yeniden toplar** (arka plan merge'lerine asla güvenmez, `_local`
tabloları okumaz). Ayrık span kümelerinin kısmi aggregate'lerini birleştirmek tüm span'leri toplamakla aynı sonucu verir, yani
yanıt yerleşime bağlı değildir. `apm_service_links_1m` "birleştirilebilir"in istisnasıdır: her shard'ın job'u
yalnızca kendi satırlarını değiştirir (shard başına `FINAL`) ve shard başına sonuçlar ayrık trace'ler üzerindendir, yani toplamları tamdır.
Yeniden denenen bloklar iki kez sayılmaz: insert'ler `deduplicate_blocks_in_dependent_materialized_views=1` kullanır.
Distributed tabloların sharding key'i `cityHash64(tenant_id, service_name)` kullanılmaz (hiçbir şey onlar üzerinden insert etmez).

Türetilmiş span sütunları (`spans` üzerinde expand migration): `service_namespace`, `deployment_environment`, `is_entry`,
`transaction_type`, `transaction_name`, `is_error`, `http_status_code`, `sample_weight`, `peer_type`, `peer_name`,
`db_system`, `db_name`, `db_operation`, `db_statement_normalized`, `error_group_id`, `error_type`, `error_message`.
Eski bir processor'ın yazdığı satırlar varsayılanlara sahiptir ve APM view'ları tarafından yok sayılır.

## 9. API {#_9-api}

Base `/api/v1/apm`, her telemetri endpoint'i gibi tenant kapsamlıdır (viewer rolü, API anahtarlarına izin verilir); Apdex ayarları
`PUT` admin veya owner rolü ister, o role sahip bir API anahtarı da dahil (D-133). `from`/`to` diğer yerlerdeki gibi (varsayılan son saat); `step` (Go duration, ≥ 60s, varsayılan
≈ 60 nokta, tam dakikalara yukarı yuvarlanır). Her `services/{service_name}` endpoint'inde servis kapsam parametreleri:
`namespace`, `environment` (§1). Süreler milisaniye (float), oranlar 0..1, `throughput` dakika başına istek.
Endpoint'ler ve şekiller: [api.md § APM](/tr/contracts/api#apm), [openapi.yaml](https://github.com/onuragtas/openlog/blob/master/docs/contracts/openapi.yaml) (tag `apm`).

| Endpoint | İçerik |
|---|---|
| `GET /apm/services` | RED, Apdex, sparkline ile servisler |
| `GET /apm/services/{s}` | kimlik, attribute'lar, host'lar, Apdex T |
| `GET /apm/services/{s}/overview` | zaman serileri (throughput, hata oranı, p50/p95/p99, Apdex) + toplamlar + en üstteki transaction'lar |
| `GET /apm/services/{s}/transactions` | transaction listesi (sıralı ve sayfalı, api.md "Lists") |
| `GET /apm/services/{s}/transaction?name=&type=` | tek transaction: toplamlar, histogram, zaman serileri, en yavaş trace'ler |
| `GET /apm/services/{s}/errors` · `GET /apm/errors` | iş akışı durumu ve filtrelerle hata gelen kutusu (§3.4) |
| `GET /apm/services/{s}/errors/{group_id}` | grup detayı: stack, seriler, örnek trace'ler, etkilenen boyutlar (§3.3), durum, yorumlar, aktivite |
| `PATCH /apm/errors/groups` | toplu status / assignee / resolved-in-version (member+) |
| `GET/POST /apm/errors/groups/{group_id}/comments`, `DELETE …/{comment_id}` | yorumlar |
| `GET /apm/services/{s}/deployments`, `…/deployments/compare` | deployment'lar ve önce/sonra karşılaştırması (§12) |
| `GET /apm/map/path` | bir transaction'ın kullandığı düğümler ve edge'ler (§5.1) |
| `GET /apm/services/{s}/databases` | normalize edilmiş DB sorguları |
| `GET /apm/services/{s}/hosts` | servisin host'ları |
| `GET/PUT /apm/services/{s}/settings` | Apdex T |
| `GET /apm/hosts/{host_id}/services` | bir host üzerindeki servisler |
| `GET /apm/map` | servis haritası (düğümler + RED'li edge'ler) |
| `GET /apm/traces` | giriş span'leri üzerinde trace araması → `GET /traces/{trace_id}`; loglar `GET /logs?trace_id=` ile (`&span_id=` tek span için, `transaction` + `transaction_service` bir transaction'ın trace'leri için; cursor sayfalama) |

## 10. Alert kuralı koşulları (`openlog-alert` için) {#_10-alert-rule-conditions-for-openlog-alert}

Bir koşul `service_name` (+ isteğe bağlı `service_namespace`, `environment`, `transaction_type`,
`transaction_name`), §4'ten bir metrik (`throughput`, `error_rate`, `errors`, `avg_ms`, `p50_ms`, `p95_ms`, `p99_ms`,
`apdex`) ve bir pencere belirtir. `apm_transactions_1m` üzerinde tenant kapsamlı sorgu katmanıyla değerlendir (tablo
`query.ApmTransactions1m`), örneğin bir servisin son 5 dakikadaki hata oranı ve p95'i:

```sql
SELECT sum(requests) AS req, sum(errors) AS err,
       sumMap(duration_hist) AS h,          -- Go: apm.HistQuantile(h, 0.95); apm.HistApdex(sumMap(ok_hist), total, T)
       sumMap(ok_hist) AS ok
FROM openlog.apm_transactions_1m
WHERE tenant_id = {tenant_id:String} AND service_name = {service:String}
  AND timestamp >= toStartOfMinute(now() - INTERVAL 5 MINUTE) AND timestamp < toStartOfMinute(now())
```

Saf SQL karşılıkları: `err / req`; `ok = (keys, vals)` ile T ms'de satisfied:
`arraySum(arrayMap((k, v) -> if(pow(2, k / 8) <= T, v, if(pow(2, (k - 1) / 8) < T, v * (T - pow(2, (k - 1) / 8)) / (pow(2, k / 8) - pow(2, (k - 1) / 8)), 0)), ok.1, ok.2))`.
Güncel (tamamlanmamış) dakika ve verinin son `OPENLOG_PROCESSOR_FLUSH_INTERVAL × 1.5` kısmı kesin değildir; tamamlanmış
dakikaları değerlendir. Bir servis için "veri yok": `apm_services` içinde pencerede `last_seen`'i olan satır yok.
Service level objective'ler aynı tabloyu okur: SLI, error budget ve burn rate'leri (kural türü `slo_burn`)
§4'ün ağırlıklarıyla `requests`, `errors` ve `duration_hist`'ten hesaplanır ([slo.md](/tr/contracts/slo)).

## 11. Log ↔ trace gezintisi {#_11-log-↔-trace-navigation}

Log kayıtları OTel log kaydından (veya SDK'nın log bridge'inden) `trace_id`/`span_id` taşır. Arayüz iki yönde de bağlantı verir:
log satırları (log explorer, host, container ve ajan logları) trace'e ve span'e bağlanır (`/traces/{id}?span=`);
trace sayfası trace'in loglarını listeler (trace'in başlangıcı − 1 dk … sonu + 1 dk içinde `GET /logs?trace_id=`, isteğe bağlı olarak
seçili span'in `span_id=` değeri); bir transaction trace'lerinin loglarına bağlanır (`GET /logs?transaction=&transaction_service=`,
aralıktaki giriş span'leri, en fazla 10000 trace). Loglar `next_cursor` ile sayfalanır ([api.md](/tr/contracts/api#logs)).

**Metrik → trace (exemplar'lar, D-130).** OTLP veri noktaları exemplar taşır, yani bir ölçümün geldiği trace'i. Processor
bunları `metric_exemplars` içinde saklar (şema 0092) ve `POST /api/v1/metrics/exemplars` bir
metrik, aralık ve Metrics Explorer filtreleri için döndürür, böylece grafikteki bir sıçrama trace olarak açılır (`/traces/{id}?span=`).
Tablonun TTL'i metrik retention'ını değil **trace** retention'ını izler: bir exemplar trace'e işaret eden bir göstericidir ve
30 günlük metrik retention'ında trace'inden 23 gün fazla yaşardı. Trace → metrik yönü yok: bir trace
onu hangi metrik enstrümanlarının gözlemlediğini kaydetmez.

## 12. Deployment'lar {#_12-deployments}

**Deployment**, bir servis için (namespace/environment başına) başka bir sürümden sonra span bildirmeye başlayan bir `service.version`'dır.
Materialized view `apm_service_versions_1m`, dakika başına sürüm başına span sayar (resource
`service.version`, eksikse `''`); tespit sorgu anında çalışır (`apm.DetectDeployments`, `GET /apm/services/{s}/deployments`):

- aralık başlangıcı − 24 saatten (önceki sürüm için lookback) sona kadar satırlar, dakika sırasıyla;
- m dakikasındaki V sürümü, V önceki `gap` içinde (varsayılan 30 dk; 5 dk–24 sa) bildirim yapmadıysa ve
  en son görülen diğer sürüm (*önceki sürüm*) V'nin son span'inden sonra görüldüyse bir deployment'tır; aynı sürümün duraklayıp
  yeniden başlaması deployment değildir; rolling update'ler (iki sürüm de gap içinde) V'nin ilk dakikasında
  tek deployment verir;
- daha önce başka sürüm görülmediyse: V'nin retention içindeki ilk span'i o dakikadaysa *initial*, aksi halde hiçbir şey (lookback'ten
  eski); V, m'den `gap`'ten daha önce bildirim yapmışsa *rollback*;
- `service.version` olmayan span'ler asla deployment oluşturmaz.

Arayüz deployment'ları APM grafiklerinde işaretler. **Compare** (`…/deployments/compare?at=&window=`), `[at − window, at)` ve `[at, at + window)` için
RED metriklerini ve Apdex'i (§4) döndürür (now ile sınırlı; `at` dakikaya kırpılır; window
5 dk–24 sa, varsayılan 30 dk) ve ikinci dönemde ilk görülen hata gruplarını döndürür.

## 13. openlog APM ajanları {#_13-openlog-apm-agents}

Yukarıdaki kurallar herhangi bir OpenTelemetry SDK'sından gelen span'lere uygulanır. openlog ajanları, §1–§7 ve §11'in ihtiyaç duyduğunu
yapılandırma olmadan üreten OpenTelemetry dağıtımlarıdır; hepsi aynı ajan tarafı sözleşmeyi izler:

| Kural | Go ajanı (`agents/go`, D-033) | Node.js ajanı (`agents/node`, `openlog-node`, D-060) |
|---|---|---|
| Servis kimliği (§1) | `OPENLOG_*`'tan `service.name/version/namespace`, `deployment.environment.name` | aynı değişkenler |
| Host ve container bağı (§1) | `host.id`: `/run/openlog-infra-agent/host-id` → machine-id dosyaları → altyapı ajanı durumu → platform id → üretilen (semantic-conventions §1); `container.id` `/proc/self/cgroup`'tan, yoksa `/proc/self/mountinfo`'dan | aynı zincir ve kaynaklar |
| Giriş span'leri ve transaction adları (§2) | `openloghttp`, ServeMux pattern'lerinden `http.route` ayarlar; chi/gin/echo modülleri | express, koa ve `@fastify/otel` HTTP server span'inde `http.route` ayarlar; aksi halde (NestJS, özel router'lar) ajan yerel bir alt span'in en uzun `http.route` değerini giriş server span'ine kopyalar ve onu `<METHOD> <route>` olarak yeniden adlandırır. NestJS 4–11: OpenTelemetry Nest instrumentation (request context ve handler span'leri); NestJS 12 (yalnızca ESM, upstream kapsamında değil): uygulama `--import openlog-node/register` ile başladığında giriş span'ini express/fastify instrumentation adlandırır, isteğe bağlı `OpenLogNestInterceptor` (`openlog-node/nest`) eşleşen route'tan `http.route` ayarlar ve `nestjs.controller`/`nestjs.callback` ekler (12'de Nest handler span'i yok) |
| Hatalar (§3) | instrumentasyonlardan `exception` event'leri | instrumentasyonlardan `exception` event'leri (Node stack biçimi) |
| Örnekleme ağırlığı (§4) | parent-based consistent probability sampling: örneklenen kökler tracestate `ot=th:<T>` ve `sampling.ratio` yazar; örneklenen uzak parent'ların yerel giriş span'leri `ot=th`/`ot=p`'den `sampling.ratio` alır; W3C random flag; isteğe bağlı `ot=rv` (`OPENLOG_SAMPLING_RV`) | aynısı; Go sampler'ından üretilen fixture'lara karşı doğrulanır (D-061) |
| DB sorguları (§7) | `db.query.text` temizlenir (`openlogsql.Sanitize`; `OPENLOG_DB_QUERY_TEXT` = `sanitized`, `raw` veya `off`) | `db.query.text`/`db.statement` için aynı algoritma ve değişken; Redis/Memcached `CMD ? ?`; 4096 karakterle sınırlı |
| Log ↔ trace (§11) | slog bridge: trace/span id'li OTLP loglar | pino ve winston (console isteğe bağlı): trace/span id'li OTLP loglar, `trace_id`/`span_id` uygulamanın kendi çıktısında da |
| Deployment'lar (§12) | `OPENLOG_SERVICE_VERSION`'dan `service.version` | aynı |

**Java ajanı** (`agents/java`, `openlog-javaagent.jar`, D-072), upstream OpenTelemetry Java ajanı artı bir openlog
eklentisidir ve aynı sözleşmeyi izler. Aynı `OPENLOG_*` değişkenlerini (`openlog.*` system
property'leri olarak da) ve aynı `host.id` zincirini ve `container.id` kaynaklarını kullanır. Upstream instrumentasyonlar (Spring MVC/WebFlux,
servlet container üzerinde JAX-RS, Quarkus REST, Vert.x Web, servlet, …) `http.route` ayarlar ve server span'i
`<METHOD> <route>` olarak adlandırır; Micronaut ile Grizzly/Jersey'in Jetty handler'ı üzerindeki Jersey, `http.route` olmadan
`<METHOD>` adlı bir server span alır, backend bunu normalize `url.path` ile gruplar (§2.1). Exception'lar Java stack biçiminde (§3.2) `exception`
event'i olarak kaydedilir. Sampler Go ajanınınkiyle eşleşir ve Go fixture'larına karşı doğrulanır;
W3C random flag'ini OTel Java SDK ayarlar. `db.query.text`/`db.statement` export yolunda Go algoritmasıyla temizlenir;
upstream sanitizer varsayılan olarak kapalıdır, MongoDB/Elasticsearch/OpenSearch upstream
temizlemeyi korur. Redis/Memcached statement'ları `CMD ? ?` olur, 4096 karakterle sınırlıdır. Logback/Log4j2 kayıtları
MDC'de `trace_id`/`span_id` alır ve OTLP log olarak export edilir. `process.command_line`/`process.command_args`
gönderilmez.

**.NET ajanı** (`agents/dotnet`, NuGet `OpenLog.Agent`, D-074), aynı sözleşmeye sahip bir OpenTelemetry .NET dağıtımıdır:
aynı `OPENLOG_*` değişkenleri, `host.id` zinciri ve `container.id` kaynakları. ASP.NET Core `http.route` değerini
endpoint'in route şablonundan ayarlar (minimal API'ler, MVC, gRPC) ve server span'i `<METHOD> <route>` olarak adlandırır; aksi halde
Node.js ajanının en uzun alt route yedeği uygulanır. Exception'lar .NET stack biçiminde
`exception` event'leridir. Sampler, Go sampler'ının port'udur ve Go fixture'larına karşı doğrulanır; W3C random flag'i
her yeni `Activity`'ye eklenir ve ajanın kendi W3C propagator'ıyla taşınır, çünkü OpenTelemetry .NET yalnızca sampled
bitini modeller. OpenTelemetry .NET örneklenmemiş yerel parent altında `Activity` oluşturmaz, bu yüzden alt servisler
parent'ın span id'sini aynı karar ve `tracestate` ile alır. `db.query.text`/`db.statement` Go
algoritmasıyla temizlenir (Npgsql, SqlClient, MySqlConnector; sağlayıcısı üzerinden EF Core), Redis statement'ları `CMD ? ?` olur,
4096 karakterle sınırlıdır. `ILogger` kayıtları trace/span id'leriyle OTLP log olarak export edilir, bu id'ler uygulamanın
log scope'larına da eklenir. MassTransit ve Confluent.Kafka (OpenTelemetry instrumentation paketi üzerinden) istek trace'ini
`messaging.*` attribute'lu PRODUCER ve consumer tarafı span'lerine taşır. Yeniden
derlenemeyen uygulamalar, ajanın plugin'iyle OpenTelemetry .NET otomatik instrumentation kullanır; bu plugin aynı resource,
sampler, propagator, processor'lar ve lisans header'ını uygular (sabitlenmiş bir otomatik instrumentation sürümüne karşı test edilir).
.NET Framework 4.8'de (ASP.NET 4.x) `netstandard2.0` build'i, her yönetilen isteğin SERVER span'ini oluşturan OpenTelemetry `TelemetryHttpModule` ile
çalışır; CI job'u `dotnet-agent-netfx` (Windows, IIS Express veya IIS) örneği çalıştırır ve
export edilen span'i kontrol eder (trace devamı, `url.path`, status, resource, lisans header'ı).

**Python ajanı** (`agents/python`, PyPI `openlog-agent`, D-073), OpenTelemetry Python SDK ve
contrib instrumentasyonlarının bir dağıtımıdır (`openlog-instrument <command>` veya `openlog_agent.start()`), aynı `OPENLOG_*`
değişkenleri, `host.id` zinciri ve `container.id` kaynaklarıyla; kararlı HTTP ve veritabanı semantic convention'ları
varsayılan olarak açıktır. Django, Flask, FastAPI/Starlette, Tornado, Falcon ve Pyramid server span'leri `http.route` taşır ve
`<METHOD> <route>` olarak adlandırılır (ajan route'u Flask için URL kuralından, Tornado için span adından ayarlar,
Django resolver route'larının başına `/` ekler, Pyramid span adlarına metodu ekler ve Falcon responder exception'larını kaydeder); gRPC
server span'leri `rpc.system`/`rpc.service`/`rpc.method` taşır. Celery görevleri ve aio-pika (RabbitMQ), kafka-python ve
aiokafka consumer'ları, producer'ın trace'ini sürdüren `consumer` span'leridir; confluent-kafka consumer span'leri ona link verir. Exception'lar
Python traceback'li `exception` event'leridir. Sampler, Go sampler'ının port'udur ve Go fixture'larına karşı doğrulanır (W3C random flag'ini
1.42'den itibaren SDK, Python 3.9 hattında ajan ayarlar). `db.query.text`/`db.statement` export yolunda Go
algoritmasıyla temizlenir, Redis statement'ları `CMD ? ?` olur, 4096 UTF-8 byte ile sınırlıdır. stdlib `logging` kayıtları
OTLP log olarak export edilir ve uygulamanın kendi çıktısı için `trace_id`/`span_id` alır (structlog processor'ı
mevcut). Pre-fork sunucuların worker'ları (gunicorn, uWSGI, Celery prefork) kendi `process.pid` değerlerini bildirir; Python'un fork hook'ları olmadan
fork eden uWSGI altında provider'lar fork'tan sonra her worker'da başlar. gevent/eventlet greenlet'leri
kendi aktif span'lerini korur. Python 3.9 – 3.13.

Runtime metrikleri normal OTLP metrikleridir (APM tabloları değil): Java `jvm.*` (bellek, GC, thread'ler, sınıflar, CPU); Python
`cpython.gc.*`, `openlog.cpython.gc.time`/`openlog.cpython.gc.pause.max` artı `process.*` (CPU, bellek, thread'ler, file
descriptor'lar, context switch'ler); .NET
.NET 9+'da `dotnet.*` (.NET 8'de `process.runtime.dotnet.*`) artı `process.cpu.time`, `process.memory.usage`; Go `go.*` ve `openlog.go.gc.*`; Node.js
`nodejs.eventloop.*`, `v8js.*` (heap space'ler, `v8js.gc.duration`, `v8js.resource.active`), `process.cpu.time`,
`process.memory.usage`. Tüm openlog ajanları `openlog-license-key` header'ıyla kimlik doğrular ve varsayılan olarak OTLP/HTTP protobuf
(gzip) export eder.
