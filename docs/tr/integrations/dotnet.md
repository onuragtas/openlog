# openlog .NET ajanı {#openlog-net-agent}

Apache-2.0 · NuGet paketi `OpenLog.Agent` · .NET 8 ve 9 (`net8.0`), .NET Framework 4.6.2+ / diğer runtime'lar için `netstandard2.0`

Bu, [OpenTelemetry .NET](https://github.com/open-telemetry/opentelemetry-dotnet) için ince bir dağıtımdır (D-074) ve
[Go](/tr/integrations/go) ile [Node.js](/tr/integrations/node) ajanlarıyla aynı mantıkla kurulmuştur. Tek satırla trace'leri,
metrikleri (.NET runtime ve süreç metrikleri dahil) ve logları (`ILogger`) OTLP üzerinden openlog'a gönderirsin. Resource,
openlog altyapı ajanıyla aynı `host.id` değerini taşır, böylece servis çalıştığı host'a bağlanır. Örnekleme kararları da W3C
`tracestate` üzerinden Go, Node.js ve Java ajanlarıyla uyumlu çalışır.

## Hızlı başlangıç {#quick-start}

```sh
dotnet add package OpenLog.Agent
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 OPENLOG_SERVICE_NAME=checkout
```

Bir sürüm nuget.org'da yoksa (openlog sürümlerinde registry'ye yayımlamak isteğe bağlıdır), her GitHub sürümüne eklenen
paketi yerel NuGet kaynağı olarak kullan. "Add data" sayfası sunucunun sürümünde hangisi çalışıyorsa onu gösterir:

```sh
mkdir -p "$HOME/.openlog/nuget"
(cd "$HOME/.openlog/nuget" && curl -fsSLO https://github.com/onuragtas/openlog/releases/download/vX.Y.Z/OpenLog.Agent.X.Y.Z.nupkg \
  && curl -fsSLO https://github.com/onuragtas/openlog/releases/download/vX.Y.Z/OpenLog.Agent.X.Y.Z.nupkg.sha256 \
  && sha256sum -c OpenLog.Agent.X.Y.Z.nupkg.sha256)
dotnet nuget add source "$HOME/.openlog/nuget" -n openlog-local   # kullanıcı başına bir kez
dotnet add package OpenLog.Agent --version X.Y.Z
```

`dotnet add package` komutuna `--source` verme: restore işlemini o kaynakla sınırlar, oysa OpenTelemetry bağımlılıkları
nuget.org'dan gelir. `.nupkg`, sürümün imzalı `manifest.json` dosyasında listelenir.

ASP.NET Core ve generic host uygulamaları (worker servisleri):

```csharp
var builder = WebApplication.CreateBuilder(args);
builder.Services.AddOpenLog(o =>
{
    o.ServiceVersion = "1.4.0";                          // her seçenek OPENLOG_* değişkenlerinden de gelebilir
    o.ActivitySources = new[] { "MyCompany.*" };         // kendi ActivitySource'ların
});
var app = builder.Build();
```

`AddOpenLog()` OpenTelemetry tracing, metrik ve loglamayı kaydeder (`ILogger` kayıtları aktif span ile ilişkilendirilmiş
OTLP logları olarak export edilir) ve `IOpenTelemetryBuilder` döndürür, yani başka OpenTelemetry ayarlarını zincirleyebilirsin
(`.WithTracing(b => b.AddSource("…"))`). Provider'lar host durunca flush edilir.

Konsol uygulamaları, host'suz arka plan servisleri ve .NET Framework:

```csharp
using OpenLog.Agent;

using var agent = OpenLogAgent.Start(o => o.ServiceName = "invoice-job");   // Dispose'da ve süreç çıkışında flush eder
var logger = agent.LoggerFactory.CreateLogger("job");
using var source = new ActivitySource("InvoiceJob");                        // + OPENLOG_ACTIVITY_SOURCES=InvoiceJob
```

`OpenLogAgent.Start` tracer, meter ve logger provider'larını, W3C `tracecontext` (Level 2 random bayrağıyla) + `baggage`
propagator'larını ve aşağıdaki instrumentation'ları kurar. Geçersiz yapılandırmada `OpenLogConfigException`, zaten çalışan bir
ajan varsa `InvalidOperationException` fırlatır. `AddOpenLog` yalnızca geçersiz yapılandırmada exception fırlatır. Export
sorunları (ingest kapalı, `401`, `503`) stderr'e hız sınırıyla loglanır, uygulamaya asla exception olarak yansımaz ve onu
bloklamaz.

> .NET 8+ üzerinde paket ASP.NET Core shared framework'üne (`Microsoft.AspNetCore.App`, ASP.NET Core instrumentation'ı için
> gerekli) başvurur, bu yüzden konsol ve worker uygulamalarının ASP.NET Core runtime'ına ihtiyacı vardır (container'larda
> `runtime` yerine `mcr.microsoft.com/dotnet/aspnet` base image'ı).
>
> Giriş noktası `OpenLog.Start()` değil `OpenLogAgent.Start()`: `OpenLog` adlı statik bir sınıf, `OpenLog.Agent` namespace'i
> içinde onu import eden her dosyada namespace'i gölgelemeden yaşayamaz.

### Sıfır kod: OpenTelemetry .NET automatic instrumentation + openlog eklentisi {#zero-code-opentelemetry-net-automatic-instrumentation-openlog-plugin}

Yeniden derleyemediğin uygulamalar için
[OpenTelemetry .NET automatic instrumentation](https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation)
kur (**1.16.0** ile test edildi) ve bu paketle gelen openlog eklentisini ekle: `lib/net8.0/OpenLog.Agent.dll` dosyasını
`OpenLog.Agent` paketinden automatic instrumentation'ın `net/` dizinine kopyala. (Uygulamanın yanına kopyalamak işe yaramaz:
uygulamanın `deps.json` dosyası onu listelemediği için eklenti tipi yüklenemez.)

```sh
cp OpenLog.Agent.dll $HOME/.otel-dotnet-auto/net/
. $HOME/.otel-dotnet-auto/instrument.sh
export OTEL_DOTNET_AUTO_PLUGINS="OpenLog.Agent.AutoInstrumentation.OpenLogPlugin, OpenLog.Agent"
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_SERVICE_NAME=checkout
dotnet MyApp.dll
```

Provider'lar, instrumentation'lar ve exporter'lar automatic instrumentation'a aittir (kendi `OTEL_*` değişkenleriyle
yapılandırılır). Eklenti ise openlog resource'unu (host.id zinciri, container.id, `OPENLOG_SERVICE_*`), openlog sampler'ını,
random bayrağı propagator'ını, route ve DB sorgusu işlemcilerini, süreç metriklerini, lisans anahtarı header'ını, exception
event'lerini ve ASP.NET Core istekleri için `OPENLOG_HTTP_IGNORE_PATHS` desteğini ekler. `test/autoinstrumentation.sh` (CI,
.NET 8 ve 9) sabitlenmiş automatic instrumentation sürümünü indirir (sha256 doğrulamalı), eklentiyi kurar, hiçbir
OpenTelemetry referansı olmayan bir ASP.NET Core uygulamasını profiler ve startup hook altında çalıştırır ve şunları doğrular:
resource (`host.id`, `service.*`, `telemetry.distro.*`), lisans header'ı, sampler (`ot=th:c` parent'lar için `sampling.ratio`),
giden çağrılarda random bayrağının yayılması, hatalar, ilişkilendirilmiş loglar, yok sayılan path'ler ve openlog süreç metrikleri.
Eklenti OpenTelemetry 1.18'e karşı derlenir, automatic instrumentation 1.16 ise kendi 1.16 assembly'lerini yükler. Bu çalışır,
çünkü eklentinin public metotları yalnızca `OpenTelemetry`/`OpenTelemetry.Api` tiplerini kullanır (assembly sürümü 1.0.0.0) ve
instrumentation seçeneklerini `object` olarak alır. Automatic instrumentation yükseltildiğinde testi yeniden çalıştır.

## Yapılandırma {#configuration}

Öncelik sırası: **seçenekler > `OPENLOG_*` > `OTEL_*` > varsayılanlar**. İsimler Go ve Node.js ajanlarındakilerle aynıdır.

| Değişken | Seçenek | Varsayılan | Anlamı |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `LicenseKey` | — | Ingest lisans anahtarı, `openlog-license-key` header'ı olarak gönderilir. Bu olmadan openlog ingest `401` döner (bir uyarı loglanır) |
| `OPENLOG_ENDPOINT` (`OTEL_EXPORTER_OTLP_ENDPOINT`) | `Endpoint` | `http://localhost:4318` (grpc: `http://localhost:4317`) | OTLP temel URL'i. `http/protobuf` sonuna `/v1/traces`, `/v1/metrics`, `/v1/logs` ekler. `http://` = şifresiz, `https://` veya şemasız = TLS |
| `OPENLOG_PROTOCOL` (`OTEL_EXPORTER_OTLP_PROTOCOL`) | `Protocol` | `http/protobuf` | veya `grpc` (`netstandard2.0`/.NET Framework üzerinde OpenTelemetry exporter'ı gRPC için `HttpClientFactory` ister, orada `http/protobuf` kullan) |
| `OPENLOG_COMPRESSION` (`OTEL_EXPORTER_OTLP_COMPRESSION`) | `Compression` | `gzip` | veya `none` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `Headers` | — | Ek header'lar (lisans anahtarı header'ı önceliklidir) |
| `OPENLOG_SERVICE_NAME` (`OTEL_SERVICE_NAME`) | `ServiceName` | `unknown_service:<entry assembly>` | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `ServiceVersion` | — | `service.version` |
| `OPENLOG_SERVICE_NAMESPACE` | `ServiceNamespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `Environment` | — | `deployment.environment.name` |
| `OPENLOG_SAMPLING_RATIO` (`*traceidratio` sampler ile `OTEL_TRACES_SAMPLER_ARG`) | `SamplingRatio` | `1` | Yeni trace'lerin parent tabanlı head sampling oranı (0..1), bkz. [Örnekleme](#sampling) |
| `OPENLOG_SAMPLING_RV` | `SamplingRV` | `false` | Yeni trace'ler açıkça `ot=rv` rastgeleliği yazar |
| `OPENLOG_RESOURCE_ATTRIBUTES` (`OTEL_RESOURCE_ATTRIBUTES` üzerine birleştirilir) | `ResourceAttributes` | — | `k=v,k2=v2` (percent-encoded değerlere izin verilir) |
| `OPENLOG_HOST_ID` | `HostId` | algılanır | Açıkça verilen `host.id` |
| `OPENLOG_HOST_ROOT` | `HostRoot` | `/` | Host'un kök dizini mount edildiğinde host dosyaları için önek (örn. `/host`) |
| `OPENLOG_INFRA_RUNTIME_DIR` | `InfraRuntimeDir` | `/run/openlog-infra-agent` | Çalışan altyapı ajanının host id'sini yayımladığı yer (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | `InfraStateDir` | `/var/lib/openlog-infra-agent` | Altyapı ajanının ürettiği host id'yi sakladığı yer |
| `OPENLOG_STATE_DIR` | `StateDir` | kullanıcı önbellek dizini `/openlog` | Bu ajanın ürettiği host id'yi sakladığı yer (son çare) |
| `OPENLOG_RUNTIME_METRICS` | `RuntimeMetrics` | `true` | .NET runtime ve süreç metrikleri |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (Go süresi; `OTEL_METRIC_EXPORT_INTERVAL` ms cinsinden) | `MetricExportInterval` | `60s` | Metrik export aralığı |
| `OPENLOG_SHUTDOWN_TIMEOUT` (Go süresi) | `ShutdownTimeout` | `5s` | Son flush'ın üst sınırı (`OpenLogAgent.Dispose`) |
| — | `ExportTimeout` | `10s` | Tek bir export isteğinin zaman aşımı |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | `LogLevel` | `warn` | stderr'de ajan tanılamaları (ve OpenTelemetry öz tanılamaları): `debug`, `info`, `warn`, `error`, `off` |
| `OPENLOG_ENABLED` (`OTEL_SDK_DISABLED`) | `Enabled` | `true` | `false`: hiçbir şey kurulmaz |
| `OPENLOG_DB_QUERY_TEXT` | `DbQueryText` | `sanitized` | `db.query.text` / `db.statement` için `sanitized`, `raw` veya `off` |
| `OPENLOG_LOGS_EXPORT` | `LogsExport` | `true` | `ILogger` kayıtlarını OTLP logları olarak export et |
| `OPENLOG_INSTRUMENTATIONS_DISABLED` | `DisabledInstrumentations` | — | Virgülle ayrılmış kısa adlar: `aspnetcore`, `httpclient`, `sqlclient`, `npgsql`, `mysqlconnector`, `redis`, `masstransit`, `grpc` |
| `OPENLOG_HTTP_IGNORE_PATHS` | `HttpIgnorePaths` | — | Span üretilmeyecek gelen istek path'leri (tam eşleşme), örn. `/healthz,/readyz` |
| `OPENLOG_ACTIVITY_SOURCES` | `ActivitySources` | — | Ek `ActivitySource` adları (wildcard kullanılabilir) |
| `OPENLOG_METERS` | `Meters` | — | Ek `Meter` adları |
| `OPENLOG_SHUTDOWN_ON_SIGNAL` | `ShutdownOnExit` | `true` | `OpenLogAgent.Start` süreç çıkışında flush eder (SIGTERM, Ctrl+C) |
| — | `ConfigureTracing`, `ConfigureMetrics`, `ConfigureLogging` | — | OpenTelemetry builder'larını alan callback'ler, örn. `b => b.AddAspNetInstrumentation()` |

**Export davranışı.** Span'ler batch'lenir (kuyruk 4096, batch 512, 5 sn'de bir), loglar da aynı şekilde (2 sn'de bir). İstekler
10 sn sonra zaman aşımına uğrar. OpenTelemetry OTLP exporter'ı geçici hataları (`429`, `502`, `503`, `504`, gRPC
`UNAVAILABLE`/`RESOURCE_EXHAUSTED`) backoff ile yeniden dener ve `Retry-After` değerine uyar (D-014). Kuyruk dolduğunda
uygulamayı bloklamak yerine yeni veri atılır. Metrikler kümülatiftir.

## Framework ve kütüphane desteği {#framework-and-library-support}

OpenTelemetry paketleri tam sürümle sabitlenmiştir (`Directory.Packages.props`: OpenTelemetry 1.18.0). Desteklenen aralığın
dışındaki bir kütüphane sadece instrument edilmez.

| Ad (`OPENLOG_INSTRUMENTATIONS_DISABLED`) | Kütüphane | Span'ler / sinyaller | Test paketinin doğruladığı |
|---|---|---|---|
| `aspnetcore` | ASP.NET Core 8/9: minimal API'ler, MVC/Web API, Razor Pages | `<METHOD> <route>` adlı SERVER span'leri, endpoint'ten `http.route` (`/users/{id:int}`, `orders/{id:int}`), exception'lar, W3C extraction, `http.server.request.duration` | minimal API + MVC attribute route'ları, yakalanmamış exception |
| `grpc` | ASP.NET Core için gRPC (`Grpc.AspNetCore`) | `rpc.system.name`, `rpc.method`, `rpc.response.status_code`, `http.route` ile SERVER span'leri `greet.Greeter/SayHello` (`OTEL_DOTNET_EXPERIMENTAL_ASPNETCORE_ENABLE_GRPC_INSTRUMENTATION=true` gerekir, host oluşturulurken okunur) | sunucu + `Grpc.Net.Client` çağrısı |
| `httpclient` | `HttpClient` / `IHttpClientFactory` (`Grpc.Net.Client` de) | CLIENT span'leri, propagation, `http.client.request.duration` | giden çağrı, örneklenen ve örneklenmeyen propagation |
| `sqlclient` | `Microsoft.Data.SqlClient`, `System.Data.SqlClient` | CLIENT span'leri, sanitize edilmiş `db.query.text` | SQL Server 2022 (CI, amd64) ve Azure SQL Edge (arm64, yerel) üzerinde Microsoft.Data.SqlClient 7.0 |
| `npgsql` | Npgsql 6+ (yerleşik `ActivitySource`) | CLIENT span'leri, sanitize edilmiş `db.query.text`, Npgsql metrikleri | PostgreSQL 16 üzerinde Npgsql 8/9 |
| — | Entity Framework Core | EF Core sorguları ADO.NET provider'ının (Npgsql, SqlClient, MySqlConnector) span'leri olarak, sanitize edilmiş SQL ile görünür. Ayrı EF span'i yoktur | EF Core 8/9 + Npgsql provider |
| `mysqlconnector` | MySqlConnector 2.x (yerleşik `ActivitySource`) | CLIENT span'leri, sorgu MySQL kurallarıyla sanitize edilir (`"…"` string'dir, `#` yorumdur) | MySQL 8.4 üzerinde MySqlConnector 2.6 |
| `redis` | `OpenTelemetry.Instrumentation.StackExchangeRedis` üzerinden StackExchange.Redis | CLIENT span'leri, `db.query.text` = `SET ? ?`. Uygulama bu (prerelease) pakete başvurduğunda otomatik açılır, multiplexer DI'dan alınır | Redis 7 üzerinde StackExchange.Redis 3.2 |
| `masstransit` | MassTransit 8+ (yerleşik `ActivitySource`) | İstek trace'inde PRODUCER span'i `OrderPlaced publish` (`messaging.system=rabbitmq`, `messaging.destination.name`), aynı trace'te CONSUMER span'i `OrderPlaced process` (`messaging.operation=process`, `messaging.masstransit.*`), ilişkilendirilmiş consumer logları | RabbitMQ 4.1 üzerinde RabbitMQ transport'lu MassTransit 8.5 |
| — | `OpenTelemetry.Instrumentation.ConfluentKafka` üzerinden Confluent.Kafka (prerelease; `InstrumentedProducerBuilder`/`InstrumentedConsumerBuilder` + `ConfigureTracing` içinde `AddKafkaProducerInstrumentation`/`AddKafkaConsumerInstrumentation`) | İstek span'inin altında PRODUCER `send <topic>`, producer'ın trace'ini sürdüren veya ona link veren CLIENT `poll <topic>`, `messaging.system=kafka`, `messaging.destination.name` | Kafka 4.1 üzerinde Confluent.Kafka 2.15 + instrumentation 0.3.0-alpha.1 |
| (`OPENLOG_LOGS_EXPORT`) | `Microsoft.Extensions.Logging` (`ILogger`) | Trace/span id'leri, biçimlenmiş mesaj, scope'lar ve state değerleriyle OTLP logları. `TraceId`/`SpanId` uygulamanın kendi log scope'larına da eklenir (`ActivityTrackingOptions`) | minimal API, MVC ve gRPC handler logları |

MassTransit 9 ticari lisanslıdır, testler MassTransit 8 (Apache-2.0) kullanır. `ActivitySource` veya `Meter` sunan diğer
kütüphaneler (Azure SDK, kendi kodun) `OPENLOG_ACTIVITY_SOURCES` / `OPENLOG_METERS` veya `ConfigureTracing` ile eklenir.

**.NET Framework (ASP.NET 4.x).** `netstandard2.0` derlemesi .NET Framework 4.6.2+ üzerinde `OpenLogAgent.Start` ile çalışır
(HttpClient, SqlClient, runtime metrikleri). Gelen ASP.NET istekleri için OpenTelemetry HTTP modülü gerekir:
`OpenTelemetry.Instrumentation.AspNet` ekle (`TelemetryHttpModule` modülünü `web.config` içinde kaydeder) ve ajanı
`Application_Start` içinde `ConfigureTracing = b => b.AddAspNetInstrumentation()` ile başlat
([samples/OpenLog.AspNetFramework.Sample](https://github.com/onuragtas/openlog/blob/master/agents/dotnet/samples/OpenLog.AspNetFramework.Sample): `Global` + `Web.config.sample`). CI bu
örneği `netstandard2.0` ajan derlemesine karşı `net48` için derler (`make build-netfx`, referans assembly'ler
`Microsoft.NETFramework.ReferenceAssemblies` paketinden), Windows job'ı `dotnet-agent-netfx` de çalıştırır:
[test/netfx/run-iis.ps1](https://github.com/onuragtas/openlog/blob/master/agents/dotnet/test/netfx/run-iis.ps1) örneği bağımlılıkları ve üretilen binding
redirect'leriyle derler, bir site düzeni hazırlar (`Global.asax`, `TelemetryHttpModule` ve `OPENLOG_*` appSettings içeren,
`test/netfx/Web.config.template` dosyasından üretilen bir `web.config`; `Global.Application_Start` bunları ortam değişkenlerine
kopyalar), IIS Express altında barındırır (IIS Express yoksa DISM ile açılan tam IIS altında) ve
[test/OpenLog.NetFx.SmokeTest](https://github.com/onuragtas/openlog/blob/master/agents/dotnet/test/OpenLog.NetFx.SmokeTest) projesini çalıştırır. Bu proje, entegrasyon testlerinin OTLP yakalama sunucusunun konsol uygulaması
halidir: istek gönderir, SERVER span'ini (sürdürülen `traceparent`, `url.path`, durum, ASP.NET scope'u, `service.name`,
`telemetry.distro.name`, `.NET Framework` runtime'ı) ve lisans header'ını doğrular. Hata olursa `web.config`, `bin/`, sunucu
loglarını ve Windows event log kayıtlarını yazdırır. `pwsh test/netfx/run-iis.ps1 -BinDir <build output> -LayoutOnly` düzeni
Linux/macOS üzerinde kontrol eder. IIS Express / IIS kısmı yalnızca Windows'ta çalışabildiği için onu job'ın ilk CI çalışması
doğrular.

**Transaction adları.** APM backend'i bir web transaction'ını `<METHOD> <http.route>` olarak adlandırır
([apm.md §2.1](/tr/contracts/apm)). ASP.NET Core endpoint'in route şablonunu server span'ine koyar. `http.route` yalnızca alt
span'lerden birinde varsa (özel routing middleware'i), ajan bu route'ların en uzununu giriş span'ine kopyalar ve export'tan önce
span'i yeniden adlandırır.

**Veritabanı sorguları.** `OPENLOG_DB_QUERY_TEXT=sanitized` (varsayılan) ile SQL, Go ve Node.js ajanlarıyla birebir aynı şekilde
normalize edilir (ortak test senaryoları): string, sayısal, hex ve dollar-quoted literal'ler → `?`, `IN (…)` ve çok satırlı
`VALUES` → `(?)`, yorumlar silinir, placeholder'lar (`@p0`, `$1`, `:name`, `?`) korunur. MySQL'de `"…"` string'dir, diğerlerinde
identifier. Redis/Memcached komutları `CMD ? ?` olur. Sorgular 4096 karakterle sınırlanır. `raw` metni korur, `off` kaldırır.

## Örnekleme {#sampling}

`OPENLOG_SAMPLING_RATIO=p` yeni trace'leri `p` olasılığıyla örnekler, downstream servisler bu karara uyar (parent tabanlı). APM
sayıları doğru kalır, çünkü saklanan her span `1/p` ile ağırlıklandırılır ([apm.md §4](/tr/contracts/apm)). Anlam Go ajanıyla
aynıdır ([kılavuz](/tr/integrations/go#sampling)) ve Go ajanının sampler'ından üretilen fixture'lara karşı doğrulanmıştır
(`agents/node/test/interop/go-sampler-fixtures.json`):

- **Kök span'ler.** `T = (1 − p) · 2^56`. Rastgelelik varsa `ot=rv`, yoksa trace id'nin alt 56 biti. `R ≥ T` olduğunda
  örneklenir. Örneklenen kök span `sampling.ratio = p` ve `tracestate` `ot=th:<T>` alır (`p = 0.25` → `ot=th:c`).
- **Downstream.** Örneklenmiş uzak parent'ı `p < 1` ile `ot=th` (veya eski `ot=p`) taşıyan yerel giriş span'i
  `sampling.ratio = p` alır.
- **Random bayrağı.** Kök span'ler W3C Level 2 random bayrağını taşır (`traceparent` bayrakları `03`/`02`), çocuklar onu miras
  alır. OpenTelemetry .NET yalnızca sampled bitini modeller, bu yüzden ajan bayrağı her yeni `Activity` üzerinde ayarlar ve
  kendi W3C propagator'ını kullanır (SDK için ve runtime'ın `DistributedContextPropagator` bileşeni için).
- **Örneklenmeyen çocuklar.** OpenTelemetry .NET örneklenmemiş *yerel* parent'ın altında `Activity` oluşturmaz (Go ve Node.js
  kayıt yapmayan bir span oluşturur). Giden çağrılar bu durumda parent'ın span id'sini, örneklenmemiş bayrakları ve `tracestate`
  değerini yayar. Downstream'deki karar aynıdır, yalnızca hiç export edilmeyen bir span id farklıdır.
- **Sınırlar.** `ot` değeri 256 karakterle (önce bilinmeyen alt anahtarlar atılır), `tracestate` 32 üyeyle sınırlıdır.

## Resource {#resource}

| Attribute | Kaynak |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | yapılandırma |
| `host.id` | bkz. [Host bağlantısı](#host-linking) |
| `host.name`, `host.arch` | Linux: `/proc/sys/kernel/hostname` → `/etc/hostname`; `/proc/sys/kernel/arch` (OTel değerleri); diğerlerinde `Environment.MachineName`, işletim sistemi mimarisi |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | platform; `/etc/os-release`; `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.{name,path}`, `process.command` (giriş path'i, argümansız), `process.owner`, `process.runtime.{name,version,description}` | süreç (komut satırı argümanları gönderilmez, secret içerebilirler) |
| `container.id` | `/proc/self/cgroup` içindeki 64 hex id; özel cgroup namespace'inde `/proc/self/mountinfo` içindeki Docker/Podman container path'lerinden |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward API ortam değişkenleri `K8S_POD_NAME`, … (pod içinde namespace için service account dosyasına, pod adı için `HOSTNAME` değerine düşülür) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version`, `telemetry.sdk.*` | ajan |

Öncelik sırası: algılanan < ortamdan veya seçeneklerden gelen resource attribute'ları < açıkça verilen servis, environment ve host id ayarları.

## Host bağlantısı {#host-linking}

Go ajanıyla aynıdır ([ayrıntılar](/tr/integrations/go#host-linking)): çalışan altyapı ajanının
`/run/openlog-infra-agent/host-id` içinde yayımladığı id → `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid`
→ altyapı ajanının `/var/lib/openlog-infra-agent/host-id` içinde ürettiği id → (macOS/Windows) platformun makine id'si →
`OPENLOG_STATE_DIR` içinde üretilip saklanan bir UUID. Container'larda altyapı ajanının runtime dizinini salt okunur mount et:
`-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro` (Kubernetes: `Directory` tipinde `hostPath`), veya `OPENLOG_HOST_ID` ayarla.

## Runtime metrikleri {#runtime-metrics}

| Metrik | Kaynak |
|---|---|
| .NET 9+: `dotnet.gc.collections`, `dotnet.gc.heap.total_allocated`, `dotnet.gc.last_collection.heap.size`, `dotnet.gc.last_collection.memory.committed_size`, `dotnet.gc.pause.time`, `dotnet.jit.compilation.time`, `dotnet.jit.compiled_methods`, `dotnet.thread_pool.{thread.count,queue.length,work_item.count}`, `dotnet.monitor.lock_contentions`, `dotnet.assembly.count`, `dotnet.timer.count`, `dotnet.process.{cpu.time,memory.working_set}` | Runtime'ın yerleşik `System.Runtime` meter'ı, `OpenTelemetry.Instrumentation.Runtime` ile açılır |
| .NET 8 ve `netstandard2.0`: `process.runtime.dotnet.gc.{collections.count,allocations.size,heap.size,committed_memory.size,duration}`, `process.runtime.dotnet.jit.*`, `process.runtime.dotnet.thread_pool.*`, `process.runtime.dotnet.monitor.lock_contention.count`, `process.runtime.dotnet.assemblies.count`, `process.runtime.dotnet.timer.count` | `OpenTelemetry.Instrumentation.Runtime` 1.18 (eski adlar; uygulama .NET 9+'a geçince `dotnet.*` olur) |
| `process.cpu.time` {`cpu.mode`=`user`/`system`} (s), `process.memory.usage` (By, working set), `process.memory.virtual` (By), `process.thread.count` | ajan, meter `OpenLog.Agent.Process` |
| `http.server.request.duration`, `http.client.request.duration`, `kestrel.*`, Npgsql/MySqlConnector havuz metrikleri | ASP.NET Core, HttpClient ve veritabanı kütüphaneleri |

## Ek yük {#overhead}

`make bench` (`bench/OpenLog.Agent.Bench`): örnek uygulamanın minimal API route'u `GET /users/{id:int}` (bir `ILogger` çağrısı,
JSON yanıt) kendi sürecinde, keep-alive yük üreticisiyle doyuma ulaştırılır (16 bağlantı, 8 sn × 3 tur, medyan, 3 sn ısınma).
Gerçek ajan yerel bir OTLP sink'e export eder (ASP.NET Core + HttpClient instrumentation, HTTP ve runtime metrikleri, ILogger →
OTLP logları). Ölçüm Docker Desktop üzerinde (Apple M1 Pro, 10 CPU) `mcr.microsoft.com/dotnet/sdk:9.0` içinde (.NET 9.0.20, server
GC) yapıldı, yük üreticisi aynı makinede çalışır. İki çalıştırma:

| Senaryo | 1. çalıştırma req/s | ajansıza göre | 2. çalıştırma req/s | ajansıza göre | p50 ms | p99 ms | RSS MB |
|---|---|---|---|---|---|---|---|
| ajansız | 157 850 | — | 201 596 | — | 0.06–0.08 | 0.44–0.60 | 74–98 |
| ajan, örneklenen (oran 1) | 120 288 | −23.8 % | 112 945 | −44.0 % | 0.10 | 0.84–0.88 | 117–148 |
| ajan, örneklenmeyen (oran 0) | 110 373 | −30.1 % | 127 718 | −36.6 % | 0.09–0.10 | 0.86–0.97 | 128–136 |
| ajan, örneklenen, `OPENLOG_LOGS_EXPORT=false` | — | — | 124 578 | −38.2 % | 0.09 | 0.74 | 126 |

Bunu dürüstçe nasıl okumalı:

- Handler neredeyse hiçbir şey yapmaz, bu yüzden sunucu istek başına yalnızca birkaç mikrosaniye harcar ve istek başına sabit
  her iş büyük bir göreli düşüş gibi görünür. Burada ajan medyanda yaklaşık **20–40 µs, p99'da 0.3–0.4 ms gecikme** ekler, üstüne
  20–50 MB RSS. Uygulama kodunda veya I/O'da milisaniyeler harcayan bir istek için göreli maliyet küçüktür.
- Çalıştırmalar arasında 20 puana varan fark var (baz çizginin kendisi 158k'dan 202k req/s'ye çıktı), çünkü aynı Docker VM'inde
  başka container'lar çalışıyordu. Bunlar yol gösterici mikro benchmark sayılarıdır, ayrılmış bir benchmark host'u değil. `make bench`
  komutunu kendi donanımında çalıştır.
- Örneklememek Go ve Node.js ajanlarındakinden daha az kazandırır. ASP.NET Core propagation için istek `Activity` nesnesini yine
  oluşturur, HTTP metrik histogramı her istek için kaydedilir ve her `ILogger` çağrısı yine export edilir (çalıştırma yaklaşık
  800 MB OTLP üretti). Log export'unu kapatmak birkaç yüzde kazandırdı.
- Ek yükü azaltmak için: örnekle (`OPENLOG_SAMPLING_RATIO`), sık çalışan yollarda log seviyelerini `Information` üstünde tut veya
  `OPENLOG_LOGS_EXPORT` ayarını kapat, health check'leri hariç tut (`OPENLOG_HTTP_IGNORE_PATHS`) ve kullanmadığın
  instrumentation'ları kapat.

## Sürümleme ve güncelleme {#versioning-and-upgrading}

Paket openlog ürünüyle birlikte sürümlenir (D-025): her ürün sürümü `vX.Y.Z`, NuGet'e `OpenLog.Agent X.Y.Z` yayımlar
(`vX.Y.Z-beta.N` NuGet prerelease olarak). Backend, son üç minor sürümünün ajanlarını kabul eder. OpenTelemetry bağımlılıkları
sürüm başına sabitlenir. Uygulaman OpenTelemetry paketlerine kendisi de başvuruyorsa onları aynı minor sürümde (1.18) tut.

## Geliştirme {#development}

Her şey SDK container'larında çalışır (yerelde .NET gerekmez):

```sh
make test                         # veritabansız birim + uçtan uca testler (.NET 9; .NET 8 için DOTNET_VERSION=8.0)
make test-integration             # + PostgreSQL, MySQL, Redis, RabbitMQ, Kafka, SQL Server (compose projesi openlog-m4-dotnet-test, sonra silinir)
make test-autoinstrumentation     # sabitlenmiş OpenTelemetry .NET automatic instrumentation altında eklenti
make build-netfx                  # .NET Framework 4.8 / ASP.NET 4.x örneği, yalnızca derleme
pwsh test/netfx/run-iis.ps1       # yalnızca Windows: IIS Express / IIS altında örnek + smoke test (-Server iis, -LayoutOnly)
make bench                        # ek yük benchmark'ı
make pack VERSION=0.1.9           # artifacts/nupkg/OpenLog.Agent.0.1.9.nupkg
```

Dizin yapısı: `src/OpenLog.Agent` (paket), `test/OpenLog.Agent.Tests` (xUnit: Go sampler fixture'ları, Go test senaryolarıyla
sanitizer, yapılandırma, resource algılama, işlemciler, propagator), `test/OpenLog.Agent.IntegrationTests` (süreç içi bir OTLP
yakalama sunucusuna karşı gerçek ajanla örnek uygulama: span'ler, metrikler, loglar, resource, header'lar, propagation, ingest
kesintisi, `OpenLogAgent.Start`, `MessagingTests` içinde MassTransit/Kafka/SQL Server, `AutoInstrumentationTests` içinde
automatic instrumentation eklentisi), `test/OpenLog.AutoInstrumentation.TestApp` (OpenTelemetry referansı yok),
`samples/OpenLog.SampleApp` (ASP.NET Core minimal API + MVC + gRPC + HttpClient + Npgsql/EF Core + MySqlConnector +
StackExchange.Redis + SqlClient + MassTransit + Confluent.Kafka), `samples/OpenLog.AspNetFramework.Sample` (net48;
`test/netfx/run-iis.ps1` ile `test/OpenLog.NetFx.SmokeTest` kullanılarak IIS altında çalışır), `bench/OpenLog.Agent.Bench`. SQL
Server'ın arm64 `mssql/server` imajı yoktur: Makefile arm64'te `mcr.microsoft.com/azure-sql-edge` seçer (Microsoft emekliye
ayırdı ama hâlâ çekilebiliyor, SqlClient testi ona karşı geçiyor), CI (amd64) ise `mcr.microsoft.com/mssql/server:2022-latest`
kullanır. Sürüm akışı [releasing.md](/tr/operations/releasing#net-agent-package) dosyasında anlatılıyor.
