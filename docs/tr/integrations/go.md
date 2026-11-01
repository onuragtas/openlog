# openlog Go ajanı {#openlog-go-agent}

Apache-2.0 · modül `github.com/onuragtas/openlog/agents/go` · Go 1.26+

Bu, OpenTelemetry Go SDK'sının ince bir dağıtımıdır (D-033). Tek bir çağrı trace'leri, metrikleri (Go runtime metrikleri dahil) ve
logları OTLP üzerinden openlog'a gönderir, sürekli CPU profillerini de gönderir (varsayılan olarak açık, aşağıya bak). Resource,
openlog altyapı ajanıyla aynı `host.id` değerini taşır, böylece bir servis üzerinde çalıştığı makineye bağlanır.

## Hızlı başlangıç {#quick-start}

```sh
go get github.com/onuragtas/openlog/agents/go
```

```go
import openlog "github.com/onuragtas/openlog/agents/go"

func main() {
	shutdown, err := openlog.Start(context.Background(), openlog.WithServiceName("checkout"))
	if err != nil {
		log.Fatal(err)
	}
	defer shutdown(context.Background()) // tampondaki telemetriyi gönderir (varsayılan olarak en fazla 5 sn)
	// ...
}
```

```sh
OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 go run .
```

`Start` genel TracerProvider, MeterProvider ve LoggerProvider'ı, ayrıca W3C `tracecontext` ve `baggage` propagator'larını kurar.
Bu yüzden herhangi bir OpenTelemetry enstrümantasyon kütüphanesi değişiklik gerektirmeden çalışır. `Start` yalnızca geçersiz
yapılandırmada hata döndürür. Gönderim sorunları (ingest kapalı, `401`, `503`) hız sınırıyla stderr'e yazılır, uygulamayı asla
panic'e düşürmez veya bloklamaz.

Çalıştırılabilir örnekler `examples` modülünde (`cd examples`):

| Örnek | Ne yapar |
|---|---|
| `go run ./basic` | span + bağlı log + özel counter |
| `go run ./http-sql -load 2m` | ServeMux route'ları, `openlogsql` üzerinden SQLite, slog köprüsü, enstrümante edilmiş istemci yük üreticisi |
| `go run ./grpc -n 50` | Bağlı span'lerle gRPC sunucu ve istemci (health servisi) |

## Sürekli profil alma {#continuous-profiling}

Varsayılan olarak açık: `Start` süreci profiller, böylece yavaş bir span kimse bir şey açmak zorunda kalmadan, içinde zamanı harcayan
fonksiyon olarak açılabilir.

```go
// Açılacak bir şey yok, bu zaten profil alır.
shutdown, err := openlog.Start(ctx, openlog.WithServiceName("checkout"))

// Süreç net/http/pprof'a ihtiyaç duyuyorsa kapat (aşağıya bak):
shutdown, err := openlog.Start(ctx, openlog.WithServiceName("checkout"), openlog.WithProfiling(false))
```

```sh
OPENLOG_PROFILING=false go run .
```

Ajan her `OPENLOG_PROFILE_INTERVAL` süresi boyunca (varsayılan 60 sn, en az 10 sn) sürecin CPU profilini alır ve OTLP profili
olarak `/v1/profiles` adresine gönderir ([profiles.md](/contracts/profiles)). Flame graph arayüzde Profiling altında görünür.
`shutdown` açık pencereyi kapatıp içindekileri gönderir, böylece bir deploy öncesindeki son dakikalar kaybolan dakikalar olmaz.

Açmadan önce bilmeye değer iki şey var:

- **Sürecinde CPU harcar.** Runtime, stack kaydetmek için çalışan her thread'i saniyede yaklaşık yüz kez keser. Runtime metrikleri
  Bu senin için önemliyse `OPENLOG_PROFILING=false` ile kapat.
- **Yalnızca HTTP.** OTLP profil sinyalinin henüz gRPC istemcisi yok, bu yüzden `OPENLOG_PROTOCOL=grpc` ile profiler, gRPC konuşan
  bir porta göndermek yerine kapalı kalır ve bunu söyleyen tek satırlık bir log yazar.

Bir süreçte aynı anda tek bir CPU profili çalışabilir. Başka bir şey zaten profil alıyorsa (`net/http/pprof`, bir benchmark), ajan
onunla kapışmak yerine tek bir uyarı yazar ve bir sonraki aralıkta yeniden dener.

## Yapılandırma {#configuration}

Öncelik sırası: **seçenekler > `OPENLOG_*` > `OTEL_*` > varsayılanlar**.

| Değişken | Seçenek | Varsayılan | Anlamı |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `WithLicenseKey` | — | Ingest lisans anahtarı, `openlog-license-key` header'ı olarak gönderilir. Yoksa openlog ingest `401` döner (uyarı yazılır) |
| `OPENLOG_ENDPOINT` (`OTEL_EXPORTER_OTLP_ENDPOINT`) | `WithEndpoint` | `http://localhost:4318` (grpc: `http://localhost:4317`) | OTLP taban URL'i. `http/protobuf`, `/v1/traces`, `/v1/metrics`, `/v1/logs` ekler. `http://` = şifresiz, `https://` veya şemasız = TLS |
| `OPENLOG_PROTOCOL` (`OTEL_EXPORTER_OTLP_PROTOCOL`) | `WithProtocol` | `http/protobuf` | veya `grpc` |
| `OPENLOG_COMPRESSION` (`OTEL_EXPORTER_OTLP_COMPRESSION`) | `WithCompression` | `gzip` | veya `none` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `WithHeaders` | — | Ek header'lar (lisans anahtarı header'ı önceliklidir) |
| `OPENLOG_SERVICE_NAME` (`OTEL_SERVICE_NAME`) | `WithServiceName` | `unknown_service:<executable>` | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `WithServiceVersion` | — | `service.version` |
| `OPENLOG_SERVICE_NAMESPACE` | `WithServiceNamespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `WithEnvironment` | — | `deployment.environment.name` |
| `OPENLOG_SAMPLING_RATIO` (`*traceidratio` sampler ile `OTEL_TRACES_SAMPLER_ARG`) | `WithSamplingRatio` | `1` | Yeni trace'lerin parent tabanlı head sampling'i (0..1), bkz. [Örnekleme](#sampling) |
| `OPENLOG_RESOURCE_ATTRIBUTES` (`OTEL_RESOURCE_ATTRIBUTES` üzerine birleştirilir) | `WithResourceAttributes` | — | `k=v,k2=v2` (percent-encoded değerlere izin verilir) |
| `OPENLOG_HOST_ID` | `WithHostID` | algılanır | Açıkça verilen `host.id` |
| `OPENLOG_HOST_ROOT` | — | `/` | Makine kökü mount edildiğinde (örn. `/host`) makine dosyaları için önek |
| `OPENLOG_SAMPLING_RV` | `WithSamplingRV` | `false` | Yeni trace'ler açık rastgelelik `ot=rv` yazar, bkz. [Örnekleme](#sampling) |
| `OPENLOG_INFRA_RUNTIME_DIR` | — | `/run/openlog-infra-agent` | Çalışan altyapı ajanının host id'sini yayımladığı yer (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | — | `/var/lib/openlog-infra-agent` | Altyapı ajanının ürettiği host id'yi sakladığı yer |
| `OPENLOG_STATE_DIR` | — | kullanıcı önbellek dizini `/openlog` | Bu ajanın ürettiği host id'yi sakladığı yer (son çare) |
| `OPENLOG_RUNTIME_METRICS` | `WithRuntimeMetrics` | `true` | Go runtime metrikleri |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (`OTEL_METRIC_EXPORT_INTERVAL`, ms) | `WithMetricInterval` | `60s` | Metrik gönderim aralığı |
| `OPENLOG_PROFILING` | `WithProfiling` | `true` | Sürekli CPU profili (yalnızca HTTP protokolü). Süreç `net/http/pprof`'a ihtiyaç duyuyorsa `false` yap |
| `OPENLOG_PROFILE_INTERVAL` | `WithProfileInterval` | `60s` | Her CPU profilinin ne kadar süreyi kapsadığı ve ne sıklıkla gönderildiği (en az 10s) |
| `OPENLOG_SHUTDOWN_TIMEOUT` | `WithShutdownTimeout` | `5s` | Shutdown context'inin deadline'ı yoksa son gönderimin sınırı |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | `WithLogLevel` | `warn` | stderr'deki ajan tanılama çıktısı: `debug`, `info`, `warn`, `error`, `off` |
| `OPENLOG_ENABLED` (`OTEL_SDK_DISABLED`) | `WithEnabled` | `true` | `false`: `Start` hiçbir şey kurmaz |
| `OPENLOG_DB_QUERY_TEXT` | `openlogsql.WithQueryText` | `sanitized` | `sanitized`, `raw` veya `off` |

**Gönderim davranışı.** Span'ler toplu gönderilir: kuyruk 4096, batch 512, 5 sn'de bir gönderilir. Loglar da aynı şekilde toplanır,
2 sn'de bir gönderilir. Gönderimler 10 sn sonra zaman aşımına uğrar. HTTP `429/502/503/504` ve gRPC
`UNAVAILABLE`/`RESOURCE_EXHAUSTED` üstel backoff ile yeniden denenir (1 sn → 30 sn, en fazla 1 dk boyunca), ingest'ten gelen
`Retry-After` / `RetryInfo` dikkate alınır (D-014). Kuyruk dolunca yeni span'ler, uygulamayı bloklamak yerine atılır.

## Örnekleme {#sampling}

`OPENLOG_SAMPLING_RATIO=p` yeni trace'leri `p` olasılığıyla örnekler. Karar kökte bir kez verilir ve aşağı akıştaki her servis ona
uyar (parent tabanlı). APM sayıları doğru kalır, çünkü saklanan her span `1/p` ile ağırlıklandırılır
([apm.md §4](/contracts/apm)).

Ajan OpenTelemetry'nin
[tutarlı olasılık örneklemesini](https://opentelemetry.io/docs/specs/otel/trace/tracestate-probability-sampling/) kullanır:

- **Kök span'ler.** Reddetme eşiği `T = (1 − p) · 2^56`. Rastgelelik `R`, varsa tracestate değeri `ot=rv:<14 hex>`, yoksa trace
  id'nin alt 56 bitidir. Bir trace `R ≥ T` olduğunda örneklenir. Örneklenen kök `sampling.ratio = p` attribute'unu alır ve
  `tracestate`'e `ot=th:<T>` yazılır: 14 hex basamak, sondaki sıfırlar atılır. Örnekler: `p = 0.25` için `ot=th:c`, `p = 0.5` için
  `ot=th:8`, `p = 0.1` için `ot=th:e6666666666666`. Diğer `ot` alt anahtarları (`rv` gibi) ve başka vendor'ların üyeleri korunur.
  Bir kök örneklenmediğinde eski kalmış `th` silinir.
- **Aşağı akış servisleri.** W3C `tracestate`, `traceparent` ile birlikte taşınır, böylece her dildeki servis `ot=th` değerinden
  `1/p`'yi hesaplayabilir. Bir Go servisinin giriş span'inin, tracestate'i `p < 1` ile `ot=th` (veya eski `ot=p:<n>`) taşıyan
  örneklenmiş bir uzak parent'ı varsa, ajan o yerel giriş span'ine de `sampling.ratio = p` yazar. Yerel `OPENLOG_SAMPLING_RATIO`,
  yukarı akışta verilmiş bir kararı değiştirmez.
- **W3C sınırları.** `ot` değeri 256 karakterle sınırlıdır: bilinmeyen alt anahtarlar atılır, `th`/`rv` korunur. Liste 32 üyeyle
  sınırlıdır ve `ot` eklenirken en sağdaki üye atılır.

- **Rastgelelik.** SDK'nın ürettiği trace id'ler rastgeledir, bu yüzden kök span'ler W3C Trace Context Level 2 random bayrağını
  taşır (örneklendiyse `traceparent` bayrakları `03`, örneklenmediyse `02`) ve rastgeleliği trace id sağlar. Çocuklar bayrağı
  parent'larından devralır: bayrağı olmayan bir uzak W3C Level 1 parent olduğu gibi sürdürülür. `OPENLOG_SAMPLING_RV=true`
  (`WithSamplingRV(true)`) ile yeni bir trace ayrıca açık rastgelelik `ot=rv:<14 hex>` alır; kök kararı bunu kullanır, aşağı
  akıştaki her servis de korur. Gelen bir `rv` asla değiştirilmez. Trace id'ler yolda yeniden yazılabiliyorsa kullan (örneğin
  yeni trace başlatan bir proxy varsa).

## Resource {#resource}

| Attribute | Kaynak |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | yapılandırma |
| `host.id` | bkz. [Makine bağlama](#host-linking) |
| `host.name`, `host.arch` | `/proc/sys/kernel/hostname` → `/etc/hostname` → `os.Hostname`; `/proc/sys/kernel/arch` (OTel değerleri, altyapı ajanıyla aynı) |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | `GOOS`; `/etc/os-release` `ID`/`VERSION_ID`/`PRETTY_NAME`; `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.name`, `process.executable.path`, `process.owner`, `process.runtime.{name,version,description}` | süreç (komut satırı argümanları gönderilmez, secret içerebilirler) |
| `container.id` | `/proc/self/cgroup` içindeki 64 hex id; özel cgroup namespace'inde (`0::/`) `/proc/self/mountinfo` içindeki Docker/Podman container yollarından |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward API ortam değişkenleri `K8S_POD_NAME`, `K8S_POD_UID`, `K8S_NAMESPACE_NAME`, `K8S_NODE_NAME`, `K8S_CONTAINER_NAME`, `K8S_DEPLOYMENT_NAME`, `K8S_CLUSTER_NAME` (pod içinde namespace için service account dosyasına, pod adı için `HOSTNAME`'e düşer) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version` = ürün sürümü, `telemetry.sdk.*` | ajan |

Öncelik sırası: algılanan < ortamdan veya seçeneklerden gelen resource attribute'ları < açıkça verilen servis, ortam ve host id
ayarları.

## Makine bağlama {#host-linking}

APM arayüzü, ikisi de aynı `host.id` değerini raporladığında bir servisi bir makineye bağlar. Bu ajan altyapı ajanının çözümleme
zincirini kullanır ([semantic-conventions.md §1](/contracts/semantic-conventions)), böylece aynı makinede iki ajan da aynı id'yi
üretir:

1. Çalışan altyapı ajanının `/run/openlog-infra-agent/host-id` içinde yayımladığı id (`OPENLOG_INFRA_RUNTIME_DIR`; önce
   `OPENLOG_HOST_ROOT` altında, sonra düz yolda okunur). Hangi kaynağı kullanmış olursa olsun altyapı ajanının `host.id` değerinin
   tam kendisidir.
2. `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid`. Bir değer ancak `[0-9A-Za-z-]` karakterlerinden
   en az 8 tane içeriyor ve tamamı sıfır değilse kullanılır; küçük harfe çevrilir.
3. Altyapı ajanının üretip `<state_dir>/host-id` içinde sakladığı UUID (`/var/lib/openlog-infra-agent/host-id`).
4. Yalnızca Linux dışında: platformun makine id'si (macOS `IOPlatformUUID`, Windows `MachineGuid`).
5. Bu ajanın üretip `OPENLOG_STATE_DIR` içinde sakladığı UUID. Bu bir altyapı ajanıyla eşleşemez ve `OPENLOG_LOG_LEVEL=info` bunu
   başlangıçta söyler.

**Container'lar ve Kubernetes.** Bir container'ın `/etc/machine-id` dosyası genelde ya yoktur ya da makineye değil imaja aittir.
Container'ları makineye şu yollardan biriyle bağla:

- makinede altyapı ajanı varsa önerilen yol: runtime dizinini salt okunur mount et,
  `-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro` (Kubernetes: `Directory` türünde bir `hostPath` volume). Id her zaman
  altyapı ajanıyla eşleşir, `product_uuid` veya üretilmiş bir id kullandığında da. Altyapı ajanı dosyayı, dizin varsa başlangıçta
  yazar (systemd oluşturur; container'da çalışan bir altyapı ajanı için oraya bir makine dizini mount et)
- makinenin id'sini salt okunur mount et: `-v /etc/machine-id:/etc/machine-id:ro` (Kubernetes: `File` türünde bir `hostPath` volume)
- `OPENLOG_HOST_ID` değerini açıkça ayarla
- altyapı ajanının `host.root_path` ayarındaki gibi makine kökünü mount et ve `OPENLOG_HOST_ROOT=/host` ayarla

Container root olarak çalışıyorsa `/sys/class/dmi/id/product_uuid` genelde makineninkidir ve mount gerektirmeden aynı id'yi verir,
çünkü sysfs namespace'lere ayrılmaz.

## Enstrümantasyon {#instrumentation}

| Paket | Modül | Kapsam |
|---|---|---|
| `openloghttp` | core | `Middleware(mux)` (ServeMux pattern'leri → `http.route`, span `GET /users/{id}`), `Handler(h, route)`, `RouteMiddleware(fn)`, `SetRoute(r, route)`, `Transport(rt)`, `Client()` |
| `openlogsql` | core | `Open(driver, dsn)`, `Register(driver)`, `OpenDB(connector, driver)`, `WrapDriver`. `db.system.name` (+ eski `db.system`), `db.namespace`, `db.operation.name`, `db.query.text` (sanitize edilmiş), `server.address`, `server.port` ile client span'leri; hatalar kaydedilir |
| `openlogslog` | core | `Wrap(handler)`: kayıtları trace/span id'leriyle OTLP log olarak gönderir ve kendi handler'ının çıktısına `trace_id`/`span_id` ekler. `NewHandler()`: yalnızca OTLP |
| `openloggrpc` | `instrumentation/grpc` | `ServerOption()`, `DialOption()` (otelgrpc stats handler'ları) |
| `openlogchi` | `instrumentation/chi` | `r.Use(openlogchi.Middleware())`, route = chi pattern'i |
| `openloggin` | `instrumentation/gin` | `r.Use(openloggin.Middleware())` (otelgin), route = `c.FullPath()` |
| `openlogecho` | `instrumentation/echo` | `e.Use(openlogecho.Middleware())` (otelecho), route = `c.Path()` |
| `openlogpgx` | `instrumentation/pgx` | `cfg.ConnConfig.Tracer = openlogpgx.NewTracer()`: `pgxpool` ve `pgx.Conn`, `openlogsql` ile aynı span'ler |

Router ve gRPC yardımcıları ayrı modüllerdir, böylece gin, echo, chi veya grpc yalnızca bunları kullananların `go.sum` dosyasına girer.

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /users/{id}", getUser)
db, _ := openlogsql.Open("pgx", os.Getenv("DATABASE_URL"))
slog.SetDefault(slog.New(openlogslog.Wrap(slog.NewJSONHandler(os.Stdout, nil))))
http.ListenAndServe(":8080", openloghttp.Middleware(mux))
```

**SQL ayrıntıları.** Statement'lar yalnızca var olan bir span içinde (istek veya job gibi) span alır; bunu değiştirmek için
`WithRootSpans()` geçir. Bir sorgu span'i, satırlar okunduktan sonra değil `Query` döndüğünde biter. Sanitizer string, sayısal, hex
ve dollar-quoted literal'leri `?` ile değiştirir, `IN (…)` ve çok satırlı `VALUES` ifadelerini `(?)` olarak daraltır, yorumları siler
ve placeholder'ları korur. MySQL'de `"…"` string sayılır; diğer veritabanlarında `"…"` bir identifier'dır. Sorgu metni 4096 baytla
sınırlıdır.

**pgx ve pgxpool.** `openlogsql` yalnızca `database/sql`'i görür, bu yüzden `pgxpool` kullanan bir sunucu şunu kullanmalı:

```go
cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
if err != nil {
	return err
}
cfg.ConnConfig.Tracer = openlogpgx.NewTracer()
pool, err := pgxpool.NewWithConfig(ctx, cfg)
```

Yukarıdaki kurallar burada da geçerli, `OPENLOG_DB_QUERY_TEXT`, `WithQueryText` ve `WithRootSpans` da. Her çağrıya isteğin context'ini ver, yoksa statement'ın bağlanacağı bir span olmaz. Batch, statement'larını içeren tek bir span'dir; `CopyFrom` da tek span'dir. `openlogsql`'den farklı olarak `BEGIN`, `COMMIT` ve `ROLLBACK` de span alır, çünkü pgx için bunlar birer statement'tır. Sorgu span'i satırlar kapatılınca biter, yani okumayı da kapsar. Zaten bir tracer ayarlıysan `github.com/jackc/pgx/v5/multitracer` ile birleştir.

**Çalıştırma planları.** openlog'dan veritabanına bağlantı açmadan PostgreSQL'in yavaş statement'larını nasıl çalıştırdığını görmek için bunu aç:

```go
tracer := openlogpgx.NewTracer(openlogpgx.WithExplain(500 * time.Millisecond))
cfg.ConnConfig.Tracer = tracer
pool, err := pgxpool.NewWithConfig(ctx, cfg)
if err != nil {
	return err
}
tracer.ExplainWith(pool)
```

Varsayılan olarak kapalıdır, çünkü uygulamanın istemediği bir sorgu çalıştırır. Eşikten yavaş bir `SELECT`, arka planda ve havuzundaki bir bağlantıyla saatte bir kez `EXPLAIN` edilir. `ANALYZE` asla kullanılmaz, yani statement yeniden çalışmaz. Parametre değerleri gönderilmez, literal değerler plandan çıkarılır. Parametreli statement'lar PostgreSQL 16 veya üstünü ister ve planları geneldir, bu yüzden yavaş çağrının kullandığı plandan farklı olabilir. Planlar **Veritabanları** ekranında görünür; `OPENLOG_DB_QUERY_TEXT` değeri `off` olmamalı.

### Runtime metrikleri {#runtime-metrics}

Metrikler her gönderimde (varsayılan olarak 60 sn'de bir) `runtime/metrics` üzerinden okunur. Ek goroutine ve `ReadMemStats`
stop-the-world'ü yoktur.

| Metrik | Tür | Birim |
|---|---|---|
| `go.memory.used` (`go.memory.type` = `stack`/`other`) | Sum, monoton olmayan | `By` |
| `go.memory.limit` (yalnızca `GOMEMLIMIT` ayarlıysa) | Sum, monoton olmayan | `By` |
| `go.memory.allocated` | Sum, monoton | `By` |
| `go.memory.allocations` | Sum, monoton | `{allocation}` |
| `go.memory.gc.goal` | Sum, monoton olmayan | `By` |
| `go.goroutine.count` | Sum, monoton olmayan | `{goroutine}` |
| `go.processor.limit` | Sum, monoton olmayan | `{thread}` |
| `go.config.gogc` | Sum, monoton olmayan | `%` |
| `go.schedule.duration` | Histogram | `s` |
| `openlog.go.gc.cycles` | Sum, monoton | `{gc_cycle}` |
| `openlog.go.gc.pause.duration` | Histogram | `s` |

İki histogram, runtime'ın ince taneli bucket'larını 10 µs ile 1 sn arasında 15 sınıra toplar. `sum` değerleri bucket orta
noktalarından tahmin edilir.

## Ek yük {#overhead}

`make bench` (Apple M1 Pro, Go 1.26, batch processor'lı ve çıktıyı atan exporter'lı gerçek SDK):

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| HTTP handler, düz ServeMux | 955 | 1056 | 12 |
| HTTP handler, enstrümante, örneklenmiş | 5257 | 6900 | 50 |
| HTTP handler, enstrümante, örneklenmemiş | 2427 | 3897 | 42 |
| SQL sorgusu (sahte driver), düz | 337 | 241 | 7 |
| SQL sorgusu, enstrümante, sanitize edilmiş metin | 3267 | 2776 | 19 |
| SQL sorgusu, enstrümante, ham metin | 2505 | 2616 | 17 |
| Yalnızca `Sanitize` | 436 | 160 | 2 |

Kısacası: örneklenen her HTTP isteği için yaklaşık 4 µs, trace edilen her SQL statement'ı için yaklaşık 3 µs. HTTP rakamına sunucu
metrikleri histogramı dahildir. İkisi de gerçek ağ ve veritabanı gecikmesinin yanında önemsizdir. Yüksek hızlarda span başına
maliyeti düşürmek için `OPENLOG_SAMPLING_RATIO` kullan.

## Sürümleme ve yükseltme {#versioning-and-upgrading}

Ajan openlog ürünüyle birlikte sürümlenir (D-025). Backend, arayüz ve tüm ajanlar tek bir `X.Y.Z` paylaşır. Her `vX.Y.Z` ürün sürümü
aynı commit üzerinde Go modül tag'lerini de yayımlar: `agents/go/vX.Y.Z` ve
`agents/go/instrumentation/{grpc,chi,gin,echo,pgx}/vX.Y.Z`. Ön sürümler (`vX.Y.Z-beta.N`) de aynı şekilde tag'lenir. `go get …@latest`
bunları atlar, o yüzden açıkça iste. Backend, son üç minor sürümünün ajanlarını kabul eder, bu yüzden ajanı o aralıkta yükselt:

```sh
go get github.com/onuragtas/openlog/agents/go@vX.Y.Z
go get github.com/onuragtas/openlog/agents/go/instrumentation/grpc@vX.Y.Z   # kullanıyorsan (chi, gin, echo, pgx için de aynı şekilde)
```

Core ve enstrümantasyon modüllerini aynı sürümde tut. `telemetry.distro.version` kullanımdaki sürümü raporlar. OpenTelemetry
bağımlılıkları her sürüm için sabitlenir (bkz. `go.mod`). Uygulaman da OTel paketleri gerektiriyorsa Go'nun minimal version
selection'ı yüksek olan sürümü seçer; sürpriz yaşamamak için aynı OTel minor'ında kal.

## Geliştirme {#development}

```sh
make -C agents/go test        # her modül
make -C agents/go vet fmt-check bench
```

`go.mod` içeren her dizin kendi başına bir modüldür. `instrumentation/chi` ve `examples`, repodaki modülleri sürüm numarasıyla
gerektirir ve yerel dizinleri gösteren `replace` direktifleri kullanır. `replace` yalnızca ana modüle uygulanır, yani bu repodaki
geliştirmeyi etkiler, tüketiciler için yok sayılır. `make go-agent-verify` (repo kökünde, CI da çalıştırır) bu modülleri `replace`
direktifleri olmadan, ağacın yerel bir proxy'sine karşı derler. Bu, tüketiciler için bozulacak require'ları yakalar. Sürüm akışı
[releasing.md](/tr/operations/releasing#go-agent-modules) içinde anlatılıyor.
