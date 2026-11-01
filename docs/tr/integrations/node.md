# openlog Node.js ajanı {#openlog-node-js-agent}

Apache-2.0 · paket `openlog-node` · Node.js 18.19+, 20, 22, 24

Bu, OpenTelemetry JavaScript SDK için ince bir dağıtımdır (D-060) ve [Go ajanıyla](/tr/integrations/go) aynı mantıkla kurulmuştur.
Tek satırla trace'leri, metrikleri (Node.js runtime metrikleri dahil) ve logları OTLP üzerinden openlog'a gönderirsin. Resource,
openlog altyapı ajanıyla aynı `host.id` değerini taşır, böylece servis çalıştığı host'a bağlanır. Örnekleme kararları W3C
`tracestate` üzerinden Go ajanıyla uyumlu çalışır.

## Hızlı başlangıç {#quick-start}

```sh
npm install openlog-node
```

Bir sürüm npm'de yoksa (openlog sürümlerinde registry'ye yayımlamak isteğe bağlıdır), her GitHub sürümüne eklenen paket dosyasını
kur. "Add data" sayfası sunucunun sürümünde hangisi çalışıyorsa onu gösterir:

```sh
npm install https://github.com/onuragtas/openlog/releases/download/vX.Y.Z/openlog-node-X.Y.Z.tgz
```

npm, URL'i ve tarball'ın bütünlük değerini `package.json` ve `package-lock.json` içine kaydeder. Yanındaki
`openlog-node-X.Y.Z.tgz.sha256`, `sha256sum` checksum'ıdır ve tarball sürümün imzalı `manifest.json` dosyasında listelenir.

Sıfır kod değişikliği:

```sh
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 OPENLOG_SERVICE_NAME=checkout
node --require openlog-node/register server.js     # CommonJS uygulamaları
node --import openlog-node/register server.mjs     # ES module uygulamaları (CommonJS için de çalışır)
# veya: NODE_OPTIONS="--require openlog-node/register"
```

Ya da koddan, uygulama izlenecek kütüphaneleri yüklemeden önce:

```js
// instrumentation.js — giriş noktasının ilk satırı veya node --require ./instrumentation.js
const openlog = require('openlog-node');            // import * as openlog from 'openlog-node'

const agent = openlog.start({ serviceName: 'checkout', serviceVersion: '1.4.0', environment: 'production' });
process.on('SIGTERM', () => agent.shutdown().finally(() => process.exit(0)));  // tamponlanmış telemetriyi flush eder (en fazla 5 sn)
```

`start()` global TracerProvider, MeterProvider ve LoggerProvider'ı, AsyncLocalStorage context manager'ı, W3C `tracecontext` + `baggage`
propagator'larını ve aşağıdaki instrumentation'ları kurar, böylece OpenTelemetry API kullanımı (`@opentelemetry/api`) olduğu gibi çalışır.
Yalnızca geçersiz yapılandırmada (`ConfigError`) veya zaten çalışan bir ajan varsa exception fırlatır. Export sorunları (ingest kapalı,
`401`, `503`) stderr'e hız sınırıyla loglanır, uygulamaya asla exception olarak yansımaz ve onu bloklamaz.

`register` giriş noktası asla exception fırlatmaz: geçersiz yapılandırma stderr'de raporlanır ve uygulama ajansız çalışır. `beforeExit`,
`SIGTERM` ve `SIGINT` anında flush eder. Uygulamanın o sinyal için kendi işleyicisi yoksa sinyal flush'tan sonra yeniden tetiklenir,
yani süreç ajan yokmuş gibi sonlanır (`OPENLOG_SHUTDOWN_ON_SIGNAL=false` bunu kapatır).

ES module'ler: `import` edilen kütüphanelerin instrument edilmesi için `--import openlog-node/register` komutunun kaydettiği loader hook
gerekir (`import-in-the-middle`). `--require` veya `start()` ile yalnızca `require` edilen modüller instrument edilir.

## Yapılandırma {#configuration}

Öncelik sırası: **seçenekler > `OPENLOG_*` > `OTEL_*` > varsayılanlar**. İsimler Go ajanındakilerle aynıdır.

| Değişken | Seçenek | Varsayılan | Anlamı |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `licenseKey` | — | Ingest lisans anahtarı, `openlog-license-key` header'ı olarak gönderilir. Bu olmadan openlog ingest `401` döner (bir uyarı loglanır) |
| `OPENLOG_ENDPOINT` (`OTEL_EXPORTER_OTLP_ENDPOINT`) | `endpoint` | `http://localhost:4318` (grpc: `http://localhost:4317`) | OTLP temel URL'i. `http/protobuf` sonuna `/v1/traces`, `/v1/metrics`, `/v1/logs` ekler. `http://` = şifresiz, `https://` veya şemasız = TLS |
| `OPENLOG_PROTOCOL` (`OTEL_EXPORTER_OTLP_PROTOCOL`) | `protocol` | `http/protobuf` | veya `grpc` (isteğe bağlı peer'ları kur: `@opentelemetry/exporter-{trace,metrics,logs}-otlp-grpc@0.222.0`) |
| `OPENLOG_COMPRESSION` (`OTEL_EXPORTER_OTLP_COMPRESSION`) | `compression` | `gzip` | veya `none` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `headers` | — | Ek header'lar (lisans anahtarı header'ı önceliklidir) |
| `OPENLOG_SERVICE_NAME` (`OTEL_SERVICE_NAME`) | `serviceName` | `unknown_service:<npm package or script>` | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `serviceVersion` | — | `service.version` |
| `OPENLOG_SERVICE_NAMESPACE` | `serviceNamespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `environment` | — | `deployment.environment.name` |
| `OPENLOG_SAMPLING_RATIO` (`*traceidratio` sampler ile `OTEL_TRACES_SAMPLER_ARG`) | `samplingRatio` | `1` | Yeni trace'lerin parent tabanlı head sampling oranı (0..1), bkz. [Örnekleme](#sampling) |
| `OPENLOG_SAMPLING_RV` | `samplingRV` | `false` | Yeni trace'ler açıkça `ot=rv` rastgeleliği yazar |
| `OPENLOG_RESOURCE_ATTRIBUTES` (`OTEL_RESOURCE_ATTRIBUTES` üzerine birleştirilir) | `resourceAttributes` | — | `k=v,k2=v2` (percent-encoded değerlere izin verilir) |
| `OPENLOG_HOST_ID` | `hostId` | algılanır | Açıkça verilen `host.id` |
| `OPENLOG_HOST_ROOT` | `hostRoot` | `/` | Host'un kök dizini mount edildiğinde host dosyaları için önek (örn. `/host`) |
| `OPENLOG_INFRA_RUNTIME_DIR` | `infraRuntimeDir` | `/run/openlog-infra-agent` | Çalışan altyapı ajanının host id'sini yayımladığı yer (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | `infraStateDir` | `/var/lib/openlog-infra-agent` | Altyapı ajanının ürettiği host id'yi sakladığı yer |
| `OPENLOG_STATE_DIR` | `stateDir` | kullanıcı önbellek dizini `/openlog` | Bu ajanın ürettiği host id'yi sakladığı yer (son çare) |
| `OPENLOG_RUNTIME_METRICS` | `runtimeMetrics` | `true` | Node.js runtime metrikleri |
| `OPENLOG_PROFILING` | `profiling` | `false` | Sürekli CPU profilleme, bkz. [Profilleme](#profiling) |
| `OPENLOG_PROFILE_INTERVAL` (Go süresi) | `profileIntervalMs` | `60s` | Her CPU profilinin kapsadığı süre |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (Go süresi; `OTEL_METRIC_EXPORT_INTERVAL` ms cinsinden) | `metricIntervalMs` | `60s` | Metrik export aralığı |
| `OPENLOG_SHUTDOWN_TIMEOUT` (Go süresi) | `shutdownTimeoutMs` | `5s` | Son flush'ın üst sınırı |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | `logLevel` | `warn` | stderr'de ajan tanılamaları: `debug`, `info`, `warn`, `error`, `off` |
| `OPENLOG_ENABLED` (`OTEL_SDK_DISABLED`) | `enabled` | `true` | `false`: hiçbir şey kurulmaz |
| `OPENLOG_DB_QUERY_TEXT` | `dbQueryText` | `sanitized` | `db.query.text` / `db.statement` için `sanitized`, `raw` veya `off` |
| `OPENLOG_LOGS_EXPORT` | `logsExport` | `true` | pino/winston kayıtlarını OTLP logları olarak export et (trace id'ler her durumda çıktılarına eklenir) |
| `OPENLOG_LOGS_CONSOLE` | `logsConsole` | `false` | `console.*` çağrılarını da OTLP logları olarak export et |
| `OPENLOG_INSTRUMENTATIONS_DISABLED` (`OTEL_NODE_DISABLED_INSTRUMENTATIONS`) | `disabledInstrumentations` | — | Virgülle ayrılmış kısa adlar (`graphql,aws-sdk`) veya paket adları |
| — | `instrumentationConfig` | — | Ajan varsayılanlarının üzerine birleştirilen, instrumentation başına OTel seçenekleri, örn. `{ http: { headersToSpanAttributes: … } }` |
| `OPENLOG_HTTP_IGNORE_PATHS` | `httpIgnorePaths` | — | Span üretilmeyecek gelen istek path'leri (tam eşleşme), örn. `/healthz,/readyz` |
| `OPENLOG_SHUTDOWN_ON_SIGNAL` | `shutdownOnSignal` | `true` | `register` `SIGTERM`/`SIGINT`/`beforeExit` anında flush eder |

**Export davranışı.** Span'ler batch'lenir: kuyruk 4096, batch 512, 5 sn'de bir flush. Loglar da aynı şekilde batch'lenir, 2 sn'de bir
flush edilir. Export istekleri 10 sn sonra zaman aşımına uğrar. HTTP `429/502/503/504` ve gRPC `UNAVAILABLE`/`RESOURCE_EXHAUSTED`
yanıtları OpenTelemetry exporter'ları tarafından exponential backoff ile yeniden denenir ve `Retry-After` değerine uyulur (D-014).
Kuyruk dolduğunda uygulamayı bloklamak yerine yeni span'ler atılır. Metrikler kümülatiftir.

## Framework ve kütüphane desteği {#framework-and-library-support}

Instrumentation'lar sabitlenmiş OpenTelemetry JS paketleridir (`package.json` dosyasına bak). Desteklenen aralığın dışındaki bir
kütüphane sadece instrument edilmez.

| Ad (`OPENLOG_INSTRUMENTATIONS_DISABLED`) | Kütüphane | Sürümler | Span'ler / sinyaller |
|---|---|---|---|
| `http` | `http`, `https` | Node.js yerleşik | SERVER span'leri (giriş span'leri, `http.route` framework'ten gelir), CLIENT span'leri, W3C propagation, `http.server.request.duration` / `http.client.request.duration` |
| `undici` | `fetch`, `undici` | Node.js 18+ `fetch`, undici ≥ 5 | `server.address` ile CLIENT span'leri, propagation |
| `express` | `express` | 4, 5 | middleware/handler span'leri, route → server span adı `GET /users/:id` |
| `fastify` | `fastify` (`@fastify/otel`) | 4.x, 5.x | hook/handler span'leri, route |
| `koa` | `koa` + `@koa/router` | koa 2–3 | middleware/router span'leri, route |
| `nestjs` | `@nestjs/core` | 4 – 11 (OTel); 12 openlog üzerinden, bkz. [NestJS 12](#nestjs-12) | 4 – 11: istek context'i ve handler span'leri, route. 12: express/fastify route'uyla adlandırılan server span'i; isteğe bağlı `OpenLogNestInterceptor` `nestjs.controller`/`nestjs.callback` ekler ve platform instrumentation'ı olmayan route'ları adlandırır; Nest handler span'i yok |
| `pg` | `pg`, `pg-pool` | 8.x | CLIENT span'leri, sanitize edilmiş `db.query.text` |
| `mysql2` | `mysql2` | 1.4 – 3 | CLIENT span'leri, değerler maskelenir + sanitize edilir |
| `redis` | `redis`, `@redis/client` | 2 – 5 | CLIENT span'leri, `db.query.text` = `GET ?` |
| `ioredis` | `ioredis` | 2 – 5 | CLIENT span'leri, `HSET ? ? ?` |
| `mongodb` | `mongodb` | 3.3 – 7 | CLIENT span'leri, sorgu değerleri `?` ile değiştirilir |
| `graphql` | `graphql` | 14 – 16 | parse/validate/execute span'leri (önemsiz resolver'lar atlanır) |
| `grpc` | `@grpc/grpc-js` | 1.x | SERVER/CLIENT span'leri (`rpc.*`) |
| `aws-sdk` | AWS SDK v3 (`@aws-sdk/*`, `@smithy/*`) | v3 | CLIENT span'leri (`rpc.system=aws-api`, SQS/SNS mesajlaşma, DynamoDB) |
| `pino` | `pino` | 5 – 10 | Her kayıtta `trace_id`/`span_id`/`trace_flags` + OTLP log export'u |
| `winston` | `winston` | 1 – 3 | Her kayıtta `trace_id`/`span_id` + OTLP log export'u |
| (`OPENLOG_LOGS_CONSOLE`) | `console` | yerleşik | Aktif span ile ilişkilendirilmiş OTLP log export'u |

Kesin sürüm aralıkları sabitlenmiş instrumentation paketlerinin aralıklarıdır. CI paketi Node.js 20 ve 22 üzerinde express 5, fastify 5,
koa 3, NestJS 11 ve 12 (ESM, express ve fastify platformları), pg 8, mysql2 3, redis 6, ioredis 6, pino 10 ve winston 3 çalıştırır.

**Transaction adları.** APM backend'i bir web transaction'ını `<METHOD> <http.route>` olarak adlandırır ([apm.md §2.1](/contracts/apm)).
Express, Koa ve Fastify eşleşen route'u HTTP server span'ine geçirir. `http.route` değerini yalnızca kendi span'lerine koyan framework'ler
için (diğer platformlarda NestJS, özel router'lar) ajan, giriş span'inin altında gördüğü en uzun route'u span bitmeden ona kopyalar.
Route'u olmayan istekler (404'ler, düz `http` sunucuları) backend'in path normalizasyonuyla gruplanır.

### NestJS 12 {#nestjs-12}

NestJS 12 yalnızca ESM'dir ve `@opentelemetry/instrumentation-nestjs-core` aralığının (`< 12`) dışındadır, bu yüzden Nest request-context/handler
span'leri yoktur. Transaction'lar yine de kod değişikliği olmadan route ile adlandırılır: uygulamayı
`node --import openlog-node/register main.js` ile başlat. Express ve fastify instrumentation'ları Nest 12'nin yüklediği platformu patch'ler,
SERVER span'i `GET /users/:id` olur ve `http.route` kayıtlı tam path'e eşittir (URI sürümü dahil, örn. `/v2/orders/:orderId`). express 5 ve
fastify 5 üzerinde `@nestjs/*` 12.0.1 ile doğrulandı (`test/e2e/nest12.test.ts`). `--require` ile ES module import'ları patch'lenmez ve route'lar
adsız kalır.

İstersen openlog interceptor'ını kaydet (`@nestjs/*` bağımlılığı yok, NestJS 8–12 üzerinde çalışır):

```js
import { OpenLogNestInterceptor } from 'openlog-node/nest';   // require('openlog-node/nest')

app.useGlobalInterceptors(new OpenLogNestInterceptor());
// veya bir modülde: providers: [{ provide: APP_INTERCEPTOR, useClass: OpenLogNestInterceptor }]
```

Eşleşen route'u platform isteğinden okur (express'te `req.route.path`, fastify'da `request.routeOptions.url`), giriş SERVER span'ine `http.route`
koyar (framework instrumentation'larının yaptığı gibi HTTP instrumentation'ının RPC metadata'sı üzerinden), span'i `<METHOD> <route>` olarak yeniden
adlandırır ve `nestjs.controller` ile `nestjs.callback` ekler. Böylece route'lar `OPENLOG_INSTRUMENTATIONS_DISABLED=express` (veya `fastify`) ile
bile adlandırılır, bu ayar middleware başına span'leri de kaldırır. Kapsam dışı olanlar: interceptor'a hiç ulaşmayan istekler (guard reddi, 404'ler,
middleware'de fırlatılan hatalar) platform instrumentation'ının verdiği adı korur (veya adsız kalır); GraphQL, microservice ve WebSocket context'leri
yok sayılır; Nest handler/guard/pipe span'i yoktur.

**Veritabanı sorguları.** `OPENLOG_DB_QUERY_TEXT=sanitized` (varsayılan) ile SQL, Go ajanıyla birebir aynı şekilde normalize edilir
(`openlogsql.Sanitize`, ortak test senaryoları): string, sayısal, hex ve dollar-quoted literal'ler → `?`, `IN (…)` ve çok satırlı `VALUES` → `(?)`,
yorumlar silinir, placeholder'lar korunur. MySQL'de `"…"` string'dir, diğerlerinde identifier. Redis/Memcached komutları `CMD ? ?` olur. Sorgular
4096 karakterle sınırlanır. `raw` metni korur, `off` kaldırır.

## Örnekleme {#sampling}

`OPENLOG_SAMPLING_RATIO=p` yeni trace'leri `p` olasılığıyla örnekler, downstream servisler bu karara uyar (parent tabanlı). APM sayıları doğru
kalır, çünkü saklanan her span `1/p` ile ağırlıklandırılır ([apm.md §4](/contracts/apm)). Anlam Go ajanıyla aynıdır
([kılavuz](/tr/integrations/go#sampling)) ve Go ajanının sampler'ından üretilen fixture'lara karşı doğrulanmıştır
(`test/interop/go-sampler-fixtures.json`, `scripts/gen-go-fixtures.sh`):

- **Kök span'ler.** `T = (1 − p) · 2^56`. Rastgelelik varsa `ot=rv`, yoksa trace id'nin alt 56 biti. `R ≥ T` olduğunda örneklenir. Örneklenen kök
  span `sampling.ratio = p` ve `tracestate` `ot=th:<T>` alır (örn. `p = 0.25` → `ot=th:c`).
- **Downstream.** Örneklenmiş uzak parent'ı `p < 1` ile `ot=th` (veya eski `ot=p`) taşıyan yerel giriş span'i `sampling.ratio = p` alır. Yerel oran
  upstream'deki kararı değiştirmez.
- **Random bayrağı.** Kök span'ler W3C Level 2 random bayrağını taşır (`traceparent` bayrakları `03`/`02`), çocuklar onu miras alır, yani Level 1
  parent değişmeden sürdürülür. `OPENLOG_SAMPLING_RV=true` yeni trace'lere açıkça `ot=rv:<14 hex>` yazar.
- **Sınırlar.** `ot` değeri 256 karakterle (önce bilinmeyen alt anahtarlar atılır), `tracestate` 32 üyeyle sınırlıdır.

## Resource {#resource}

| Attribute | Kaynak |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | yapılandırma |
| `host.id` | bkz. [Host bağlantısı](#host-linking) |
| `host.name`, `host.arch` | Linux: `/proc/sys/kernel/hostname` → `/etc/hostname`; `/proc/sys/kernel/arch` (OTel değerleri, altyapı ajanıyla aynı); diğerlerinde `os.hostname()`, `process.arch` |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | platform; `/etc/os-release`; `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.{name,path}`, `process.command` (script path'i, argümansız), `process.owner`, `process.runtime.{name,version,description}` | süreç (komut satırı argümanları gönderilmez, secret içerebilirler) |
| `container.id` | `/proc/self/cgroup` içindeki 64 hex id; özel cgroup namespace'inde `/proc/self/mountinfo` içindeki Docker/Podman container path'lerinden |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward API ortam değişkenleri `K8S_POD_NAME`, … (pod içinde namespace için service account dosyasına, pod adı için `HOSTNAME` değerine düşülür) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version`, `telemetry.sdk.*` | ajan |

Öncelik sırası: algılanan < ortamdan veya seçeneklerden gelen resource attribute'ları < açıkça verilen servis, environment ve host id ayarları.

## Host bağlantısı {#host-linking}

Go ajanıyla aynıdır ([ayrıntılar](/tr/integrations/go#host-linking)): çalışan altyapı ajanının `/run/openlog-infra-agent/host-id` içinde yayımladığı id
→ `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid` → altyapı ajanının `/var/lib/openlog-infra-agent/host-id` içinde
ürettiği id → (macOS/Windows) platformun makine id'si → `OPENLOG_STATE_DIR` içinde üretilip saklanan bir UUID. Container'larda altyapı ajanının runtime
dizinini salt okunur mount et: `-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro` (Kubernetes: `Directory` tipinde `hostPath`), veya
`OPENLOG_HOST_ID` ayarla.

## Runtime metrikleri {#runtime-metrics}

Her metrik toplamasında okunur (varsayılan olarak 60 sn'de bir), ayrıca bir event loop gecikme monitörü ve bir GC performance observer çalışır. Adlar
OpenTelemetry semantic convention'larını izler (Go ajanının politikası: varsa semconv adları, yoksa `openlog.` öneki). Scope `@openlog/node/runtime`.

| Metrik | Tip | Birim | Attribute'lar |
|---|---|---|---|
| `nodejs.eventloop.delay.{min,max,mean,stddev,p50,p90,p99}` | Gauge (son toplamadan beri) | `s` | — |
| `nodejs.eventloop.utilization` | Gauge (son toplamadan beri) | `1` | — |
| `nodejs.eventloop.time` | Sum, monotonic | `s` | `nodejs.eventloop.state` = `active`/`idle` |
| `v8js.gc.duration` | Histogram (Go ajanı sınırları, 10 µs – 1 s) | `s` | `v8js.gc.type` = `major`/`minor`/`incremental`/`weakcb` |
| `v8js.memory.heap.used`, `v8js.memory.heap.space.size`, `v8js.memory.heap.space.available_size`, `v8js.memory.heap.space.physical_size` | Sum, non-monotonic | `By` | `v8js.heap.space.name` |
| `v8js.memory.heap.limit` | Sum, non-monotonic | `By` | — (isolate heap boyutu sınırı) |
| `v8js.resource.active` | Sum, non-monotonic | `{resource}` | `v8js.resource.type` (aktif handle/request'ler: `TCPSocketWrap`, `Timeout`, …) |
| `process.cpu.time` | Sum, monotonic | `s` | `cpu.mode` = `user`/`system` |
| `process.memory.usage` | Sum, non-monotonic | `By` | — (RSS) |

HTTP instrumentation'ı ayrıca `http.server.request.duration` ve `http.client.request.duration` kaydeder.

## Profilleme {#profiling}

Ajan her `OPENLOG_PROFILE_INTERVAL` süresinde o aralığı kapsayan bir V8 CPU profili alır ve OTLP/JSON olarak `<endpoint>/v1/profiles` adresine gönderir.
Orada yavaş bir span'in arkasındaki flame graph'a dönüşür. Kontrat: [profiles.md](/contracts/profiles).

**Varsayılan olarak kapalıdır,** Go ajanının aksine, ve fark maliyetten çok güncellemelerle ilgilidir. Go'da varsayılan açıktır ve kapatma ayarı gerekir,
çünkü Go süreç başına yalnızca bir CPU profiline izin verir. Node'da böyle bir kilit yok, ama mevcut kurulumlar var: güncelleyen herkes için yeni bir
sinyali açmak, kimse istemeden sakladıkları ve faturalandıkları veriyi katlardı:

```sh
OPENLOG_PROFILING=true
```

Boşta bir süreç hiçbir şey göndermez: örneği olmayan profil asla gönderilmez, yani hiç istek işlemeyen bir servis burada maliyet çıkarmaz.

Yalnızca CPU örneklenir, heap, allocation ve lock profilleri henüz toplanmıyor. Sampler'ın maliyeti aşağıdaki [Ek yük](#overhead) sayılarına dahil değildir,
onlar profilleme kapalıyken ölçüldü.

## Ek yük {#overhead}

`npm run bench` (`test/bench/overhead.ts`): bir Express 5 uygulaması (`express.Router`, tek JSON route) keep-alive yük üreticisiyle doyuma ulaştırılır
(16 eşzamanlı bağlantı, 8 sn × 3 çalıştırma, medyan), gerçek ajan yerel bir OTLP yakalama sunucusuna export eder (http, express, pino/winston hook'ları
ve runtime metrikleri açık). Ölçüm Docker Desktop üzerinde (Apple M1 Pro, 10 CPU, yük üreticisi aynı makinede) `node:22-alpine` içinde yapıldı:

| Senaryo | req/s | ajansıza göre | p50 ms | p99 ms | RSS MB |
|---|---|---|---|---|---|
| ajansız | 5214 | — | 2.39 | 11.62 | 128 |
| ajan, örneklenen (oran 1) | 2469 | −52.6 % | 5.47 | 23.58 | 178 |
| ajan, örneklenmeyen (oran 0) | 3578 | −31.4 % | 3.98 | 12.29 | 170 |

Sunucu doyuma ulaşmış tek bir event loop'tur, yani istek başına servis süresi `1/rps` olur: ajan **örneklenen istek başına yaklaşık 0.21 ms** (server span'i,
express router/handler span'leri, HTTP metrik histogramı, batch'leme ve export) ve **örneklenmeyen istek başına 0.09 ms** (context propagation, instrumentation
hook'ları, metrikler) ekler, üstüne ~50 MB RSS. Göreli düşüş burada yalnızca handler'ın kendisi neredeyse hiçbir şey yapmadığı için (0.19 ms) büyüktür. Uygulama
kodunda ve I/O'da 10 ms harcayan bir istek için yaklaşık %2'dir. Bu, ajanın üzerine kurulduğu upstream OpenTelemetry JS instrumentation'larıyla karşılaştırılabilir
düzeydedir ve Go ajanından (~4 µs) yüksektir. Azaltmanın yolları: örnekleme (`OPENLOG_SAMPLING_RATIO`), gerekmeyen instrumentation'ları kapatma
(`OPENLOG_INSTRUMENTATIONS_DISABLED`, örn. `express` route adlarını korur ama middleware başına span'leri en pahalı olanlardır) ve health check'ler için
`OPENLOG_HTTP_IGNORE_PATHS`. Bunlar ayrılmış bir benchmark host'undan değil, paylaşılan bir geliştirme makinesinden alınmış mikro benchmark sayılarıdır.

## Sürümleme ve güncelleme {#versioning-and-upgrading}

Paket openlog ürünüyle birlikte sürümlenir (D-025): her ürün sürümü `vX.Y.Z`, npm'e `openlog-node@X.Y.Z` yayımlar (ön sürümler `vX.Y.Z-beta.N`, `beta` dist-tag'i
altında). Backend, son üç minor sürümünün ajanlarını kabul eder. OpenTelemetry bağımlılıkları sürüm başına tam olarak sabitlenir. Uygulaman `@opentelemetry/api`
paketine de bağımlıysa onu `^1.9` üzerinde tut (tek bir global API örneği paylaşılır).

```sh
npm install openlog-node@X.Y.Z
```

## Geliştirme {#development}

iCloud Drive üzerindeki yerel Node araçları takılabilir, her şeyi container içinde çalıştır:

```sh
docker volume create openlog-node-agent-nm
docker run --rm -v "$PWD":/src -v openlog-node-agent-nm:/src/node_modules -w /src node:22-alpine \
  sh -c 'npm ci && npm run test:apps:install && npm run lint && npm run typecheck && npm test'
# gerçek PostgreSQL/MySQL/Redis
docker compose -p openlog-nodeagent-test -f test/integration/docker-compose.yml --profile runner run --rm runner
docker compose -p openlog-nodeagent-test -f test/integration/docker-compose.yml down -v
npm run bench                     # ek yük mikro benchmark'ı
scripts/gen-go-fixtures.sh        # Go sampler fixture'larını yeniden üret (Go gerekir)
```

`npm test` paketi derler (tsc ile `dist/cjs` ve aynı örneği yeniden export eden `dist/esm` içinde bir ES module cephesi), testleri derler ve birim testlerin yanı sıra
gerçek Express, Fastify, Koa, NestJS ve düz `http` uygulamalarını `--require`/`--import` ile bir OTLP yakalama sunucusuna karşı başlatan uçtan uca testleri çalıştırır
(span'ler, metrikler, loglar, resource, header'lar, SIGTERM flush'ı, ingest kesintisi). NestJS 12 paketi Node.js 20+ ve `test/apps/nest12` içinde ayrı sabitlenmiş
uygulamayı gerektirir (`npm run test:apps:install`, yani `npm ci --prefix test/apps/nest12`). Bu olmadan paket bu ipucuyla atlanır. Sürüm akışı
[releasing.md](/tr/operations/releasing#node-js-agent-package) dosyasında anlatılıyor.
