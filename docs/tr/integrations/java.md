# openlog Java ajanı {#openlog-java-agent}

Apache-2.0 · `openlog-javaagent-<version>.jar` · Java 8+ uygulamalar (17 ve 21 üzerinde test edildi)

[OpenTelemetry Java agent](https://github.com/open-telemetry/opentelemetry-java-instrumentation) için bir dağıtımdır
(D-072) ve [Go](/tr/integrations/go) ile [Node.js](/tr/integrations/node) ajanlarıyla aynı mantıkla kurulmuştur. Tek bir JVM
flag'iyle trace'leri, metrikleri (JVM runtime metrikleri dahil) ve logları OTLP üzerinden openlog'a gönderirsin, kod değişikliği
gerekmez. Jar, değiştirilmemiş upstream ajan (sabitlenmiş, şu an **2.31.1**, SDK 1.65.0) artı `extensions/` dizinindeki openlog
eklentisinden oluşur. Eklenti şunları ekler:

- tüm openlog ajanlarının paylaştığı `OPENLOG_*` yapılandırması;
- altyapı ajanının `host.id` ve `container.id` değerleri, böylece servisler host'larına ve container'larına bağlanır;
- Go ajanının tutarlı olasılık sampler'ı (`ot=th`, `sampling.ratio`, Go fixture'larına karşı doğrulanmış);
- Go ajanının DB sorgusu sanitizer'ı;
- lisans anahtarı header'ı ve openlog varsayılanları.

## Hızlı başlangıç {#quick-start}

`openlog-javaagent-<version>.jar` dosyasını ve `.sha256` dosyasını [GitHub sürümünden](https://github.com/onuragtas/openlog/releases) indir, sonra:

```sh
sha256sum -c openlog-javaagent-X.Y.Z.jar.sha256
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 OPENLOG_SERVICE_NAME=checkout
java -javaagent:/opt/openlog/openlog-javaagent-X.Y.Z.jar -jar app.jar
# veya: JAVA_TOOL_OPTIONS="-javaagent:/opt/openlog/openlog-javaagent.jar"
```

**Altyapı ajanı olan host'larda otomatik güncelleme:** `/opt/openlog/openlog-javaagent.jar` dosyasını altyapı ajanına bırak ve
`config.yaml` içinde (veya Fleet sayfasında) `java_agent.mode: auto` ayarla. Ajan her sürümün imzalı jar'ını
`/opt/openlog/java-agent/versions/<v>/` altına kurar ve link'i değiştirir. Çalışan uygulamalar yeniden başlatılana kadar kendi
sürümlerinde kalır ve host sayfasında "restart pending" olarak listelenir. O path'e kendin kopyaladığın bir jar'a dokunulmaz
(onu sahiplendirmek için [java-agent.md](/tr/contracts/java-agent) §2.2'ye bak).

Her ayar system property de olabilir: `OPENLOG_SERVICE_NAME` ⇔ `-Dopenlog.service.name=checkout`. OpenTelemetry Java'da olduğu
gibi system property'ler ortam değişkenlerine üstün gelir. Container imajları:

```dockerfile
ADD --checksum=sha256:<sha256> https://github.com/onuragtas/openlog/releases/download/vX.Y.Z/openlog-javaagent-X.Y.Z.jar /opt/openlog/openlog-javaagent.jar
ENV JAVA_TOOL_OPTIONS="-javaagent:/opt/openlog/openlog-javaagent.jar"
```

Export sorunları (ingest kapalı, `401`, `503`) ajan tarafından loglanır, uygulamaya asla exception olarak yansımaz ve onu
bloklamaz. Geçersiz `OPENLOG_*` değerleri stderr'de raporlanır (`[openlog] WARN …`) ve yok sayılır.

## Yapılandırma {#configuration}

Öncelik sırası: **`openlog.*` system property'leri > `OPENLOG_*` > `otel.*` system property'leri / `OTEL_*` > openlog varsayılanları**.
İsimler Go ve Node.js ajanlarındakilerle aynıdır. Her [OpenTelemetry Java agent seçeneği](https://opentelemetry.io/docs/zero-code/java/agent/configuration/)
çalışmaya devam eder.

| Değişken (system property `openlog.…`) | OpenTelemetry karşılığı | Varsayılan | Anlamı |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `otel.exporter.otlp.headers` içinde header | — | Ingest lisans anahtarı, `openlog-license-key` header'ı olarak gönderilir (`OTEL_EXPORTER_OTLP_HEADERS` ile birleştirilir, lisans anahtarı önceliklidir). Bu olmadan openlog ingest `401` döner (bir uyarı loglanır) |
| `OPENLOG_ENDPOINT` | `otel.exporter.otlp.endpoint` | `http://localhost:4318` (grpc: `:4317`) | OTLP temel URL'i. http/protobuf için `/v1/traces`, `/v1/metrics`, `/v1/logs` eklenir. Şemasız = `https://` |
| `OPENLOG_PROTOCOL` | `otel.exporter.otlp.protocol` | `http/protobuf` | veya `grpc` |
| `OPENLOG_COMPRESSION` | `otel.exporter.otlp.compression` | **`gzip`** (upstream: yok) | veya `none` |
| `OPENLOG_SERVICE_NAME` | `otel.service.name` | upstream algılama (Spring Boot adı, jar adı, `unknown_service:java`) | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `otel.resource.attributes` içinde `service.version` | — | `service.version` (deployment'lar, apm.md §12) |
| `OPENLOG_SERVICE_NAMESPACE` | `service.namespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `deployment.environment.name` | — | `deployment.environment.name` |
| `OPENLOG_RESOURCE_ATTRIBUTES` | `otel.resource.attributes` üzerine birleştirilir | — | `k=v,k2=v2` (percent-encoded değerler) |
| `OPENLOG_HOST_ID` | `host.id` | algılanır | Açıkça verilen `host.id` |
| `OPENLOG_HOST_ROOT` | — | `/` | Host'un kök dizini mount edildiğinde host dosyaları için önek (örn. `/host`) |
| `OPENLOG_INFRA_RUNTIME_DIR` | — | `/run/openlog-infra-agent` | Çalışan altyapı ajanının host id'sini yayımladığı yer (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | — | `/var/lib/openlog-infra-agent` | Altyapı ajanının ürettiği host id'yi sakladığı yer |
| `OPENLOG_STATE_DIR` | — | kullanıcı önbellek dizini `/openlog` | Bu ajanın ürettiği host id'yi sakladığı yer (son çare) |
| `OPENLOG_SAMPLING_RATIO` | `(parentbased_)traceidratio` için `otel.traces.sampler.arg` | `1` | Yeni trace'lerin parent tabanlı head sampling oranı (0..1), bkz. [Örnekleme](#sampling) |
| `OPENLOG_SAMPLING_RV` | — | `false` | Yeni trace'ler açıkça `ot=rv` rastgeleliği yazar |
| `OPENLOG_HTTP_IGNORE_PATHS` | — | — | Span üretilmeyecek gelen istek path'leri (tam eşleşme), örn. `/healthz,/actuator/health` |
| `OPENLOG_DB_QUERY_TEXT` | — | `sanitized` | `db.query.text` / `db.statement` için `sanitized`, `raw` veya `off` |
| `OPENLOG_RUNTIME_METRICS` | `otel.instrumentation.runtime-telemetry.enabled` | `true` | JVM runtime metrikleri |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (Go süresi) | `otel.metric.export.interval` (ms) | `60s` | Metrik export aralığı |
| `OPENLOG_LOGS_EXPORT` | `false` olduğunda `otel.logs.exporter=none` | `true` | Logback/Log4j2/JUL kayıtlarını OTLP logları olarak export et (trace id'ler her durumda MDC'de kalır) |
| `OPENLOG_INSTRUMENTATIONS_DISABLED` | `otel.instrumentation.<name>.enabled=false` | — | Virgülle ayrılmış upstream instrumentation adları, örn. `jdbc,kafka,spring-webmvc` |
| `OPENLOG_ENABLED` | `false` olduğunda `otel.sdk.disabled=true` | `true` | `false`: telemetri yok (instrumentation no-op olarak kurulu kalır; `OTEL_JAVAAGENT_ENABLED=false` ajanı tamamen atlar) |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | — | `warn` | stderr'de openlog tanılamaları: `debug`, `info`, `warn`, `error`, `off` (ajanın iç detayları: `OTEL_JAVAAGENT_DEBUG=true`) |

Java için geçerli olmayanlar: `OPENLOG_SHUTDOWN_TIMEOUT` (ajan bir JVM shutdown hook'unda flush eder), `OPENLOG_SHUTDOWN_ON_SIGNAL`,
`OPENLOG_LOGS_CONSOLE` (`System.out` bir loglama API'si değildir).

**Diğer varsayılanlar.** openlog varsayılanları şunlardır: trace, metrik ve loglar için OTLP/HTTP protobuf exporter'ı, `gzip`, W3C
`tracecontext` + `baggage` propagation, runtime metrikleri ve kapalı upstream DB sanitizer'ı (bkz.
[Veritabanı sorguları](#database-statements)). Export davranışı upstream'inkidir: span'ler batch'lenir (kuyruk 2048, batch 512,
5 sn), log kayıtları da aynı şekilde (1 sn), export'lar 10 sn sonra zaman aşımına uğrar ve yeniden denenebilir HTTP/gRPC yanıtları
exponential backoff ile yeniden denenir. Kuyruk dolduğunda uygulamayı bloklamak yerine span'ler atılır. Metrikler kümülatiftir.

## Framework ve kütüphane desteği {#framework-and-library-support}

[Upstream ajan 2.31.1'in desteklediği](https://github.com/open-telemetry/opentelemetry-java-instrumentation/blob/v2.31.1/docs/supported-libraries.md)
her kütüphane instrument edilir: 100'den fazla framework, sunucu, istemci, veritabanı, mesajlaşma sistemi ve loglama kütüphanesi.
Entegrasyon testleri (`integration-tests/`) şunları openlog jar'ıyla gerçek servislere karşı çalıştırır:

| Alan | Doğrulandığı ortam | Neler doğrulanıyor |
|---|---|---|
| Spring Boot 3 Web MVC (Tomcat) | Spring Boot 3.5 | `http.route` ile SERVER span'i `GET /users/{id}`; `POST /orders`; 500 → status ERROR + Java stack trace'li `exception` event'i |
| Spring Boot 3 WebFlux (Reactor Netty) | Spring Boot 3.5 | `http.route` ile `GET /items/{id}`; hatalar; 0.5 oranında head sampling |
| JDBC + Hibernate/JPA | PostgreSQL 16 (pgJDBC), MySQL 8.4 (Connector/J), Hibernate 6.6 | Go ajanıyla birebir aynı sanitize edilmiş sorgulu CLIENT span'leri, Hibernate span'leri, istek trace'i içinde JDBC span'leri, hiçbir attribute'ta literal değer yok |
| Redis | Lettuce (Spring Data Redis), Jedis | İki istemci için de `SET ? ?`, `GET ?`, değerler asla export edilmez |
| Kafka | kafka-clients 3.9 | İstek trace'inde PRODUCER span'i, ona link veren CONSUMER span'i, ilişkilendirilmiş consumer log kayıtları |
| gRPC | grpc-java 1.84 (Netty) | CLIENT ve SERVER span'leri `demo.Greeter/SayHello`, çağrı boyunca parent/child |
| Logback | Spring Boot varsayılanı | MDC'de `trace_id`/`span_id` (uygulama çıktısı) ve trace/span id'li OTLP log kayıtları |
| Log4j2 | spring-boot-starter-log4j2 | aynısı, Log4j2 context data provider üzerinden |
| JVM runtime | JDK 17, 21 | `jvm.memory.used`, `jvm.thread.count`, `jvm.class.loaded`, `jvm.cpu.time`; `http.server.request.duration`; runtime metriklerini kapatma |
| JAX-RS | Gömülü Jetty 12 (ee10) üzerinde Jersey 3.1 `ServletContainer` | `http.route` ile SERVER span'i `GET /users/{id}`, W3C parent, istek trace'inde sanitize edilmiş sorgulu JDBC CLIENT span'i, 500 → ERROR, yok sayılan path'ler, openlog resource'u |
| Quarkus (JVM modu) | Quarkus 3.39, Quarkus REST, `quarkus-run.jar` | JAX-RS ile aynı (`@Blocking` resource metodu) |
| Vert.x Web | Vert.x 4.5 router, `executeBlocking` içinde JDBC | aynısı, `http.route` `/users/:id` (Vert.x söz dizimi) |
| Micronaut | Micronaut 4.10 (Netty), `@ExecuteOn(BLOCKING)` | İstek trace'inde JDBC span'i, hatalar, resource; **`http.route` yok** (aşağıya bak) |

Servlet container'ları, JMS, RabbitMQ, MongoDB, Cassandra, Elasticsearch, AWS SDK, OkHttp, Apache HttpClient ve upstream listesinin
geri kalanı aynı ajanla instrument edilir ama openlog test paketinin parçası değildir.

**Transaction adları.** APM backend'i bir web transaction'ını `<METHOD> <http.route>` olarak adlandırır ([apm.md §2.1](/tr/contracts/apm)).
Upstream instrumentation'lar Spring MVC, WebFlux, servlet container üzerinde JAX-RS, Quarkus REST, Vert.x Web, Ktor, Play, Grails,
Struts ve servlet mapping'leri için HTTP server span'ine `http.route` koyar ve span'i `GET /users/{id}` olarak yeniden adlandırır.
Route'u olmayan istekler backend'in `url.path` üzerindeki path normalizasyonuyla gruplanır. Upstream ajan 2.31.1'in doğrulanmış
eksikleri: **Micronaut** için instrumentation yok (SERVER span'i Netty'den gelir ve adı `GET` olur), **Grizzly üzerinde Jersey veya
Jersey'nin Jetty handler container'ı** (`jersey-container-grizzly2-http`, `jersey-container-jetty-http`) `http.route` içermeyen bir
SERVER span'i alır. Route adları için Jersey'yi servlet olarak deploy et (`jersey-container-servlet`).

## Veritabanı sorguları {#database-statements}

`OPENLOG_DB_QUERY_TEXT=sanitized` (varsayılan) ile `db.query.text` ve `db.statement`, export yolunda Go ajanının algoritmasıyla
yeniden yazılır (`openlogsql.Sanitize`, Go ve Node.js ajanlarıyla aynı test senaryoları):
- string, sayısal, hex ve dollar-quoted literal'ler → `?`;
- `IN (…)` ve çok satırlı `VALUES` → `(?)`;
- yorumlar silinir, boşluklar sadeleştirilir, placeholder'lar korunur;
- MySQL/MariaDB'de `"…"` string'dir, diğerlerinde identifier.

`redis`, `valkey` ve `memcached` sorguları büyük harfli komut ve kelime başına bir `?` olur (`SET ? ?`). Tüm sorgular 4096
karakterle sınırlanır.

Upstream'den farklar, net olarak:

- Upstream sanitizer (`otel.instrumentation.common.db.query-sanitization.enabled`, eski adıyla
  `…db-statement-sanitizer.enabled`) openlog sanitizer'ı orijinal sorguyu görsün diye **varsayılan olarak kapalıdır**. Sorgu asla
  sanitize edilmeden export edilmez: yeniden yazma, yapılandırılmış her span exporter'ının içinden geçtiği span exporter zincirinde
  çalışır. Upstream sanitizer'ı yeniden açmak serbesttir ve zararsızdır (ikisi de çalışır). Ama çıktısı farklıdır: `IN (?, ?)`
  listelerini ve yorumları korur.
- `mongodb`, `elasticsearch` ve `opensearch` sorguları kendi upstream sanitizer'larına bırakılır (MongoDB:
  `otel.instrumentation.mongo.statement-sanitizer.enabled`, varsayılan olarak açık).
- Boşluk içeren bir Redis argümanı birden fazla `?` sayılır.
- `raw` sorguları değiştirmeden export eder (sınırlı olarak), `off` iki attribute'u da kaldırır. Span adları (`SELECT app_users`)
  upstream'den gelir ve asla değer içermez.

## Örnekleme {#sampling}

`OPENLOG_SAMPLING_RATIO=p` yeni trace'leri `p` olasılığıyla örnekler ve downstream servisler bu karara uyar (parent tabanlı). APM
sayıları doğru kalır, çünkü saklanan her span `1/p` ile ağırlıklandırılır ([apm.md §4](/tr/contracts/apm)). Sampler, Go ajanınınkinin
port'udur. Go sampler'ından üretilen fixture'lara karşı doğrulanmıştır (`agents/node/test/interop/go-sampler-fixtures.json`,
`SamplerFixturesTest` çalıştırır: bir span ve çocuğunun `traceparent`, `tracestate`, sampled bayrağı ve `sampling.ratio` değerleri):

- **Kök span'ler.** `T = (1 − p) · 2^56`. Rastgelelik varsa `ot=rv`, yoksa trace id'nin alt 56 biti. `R ≥ T` olduğunda span
  örneklenir. Örneklenen kök span `sampling.ratio = p` ve `tracestate` `ot=th:<T>` alır (örn. `p = 0.25` → `ot=th:c`).
- **Downstream.** Örneklenmiş uzak parent'ı `p < 1` ile `ot=th` (veya eski `ot=p`) taşıyan yerel giriş span'i
  `sampling.ratio = p` alır.
- **Random bayrağı.** Kök span'ler W3C Level 2 random bayrağını taşır (`traceparent` bayrakları `03`/`02`, export edilen span
  bayrakları da), bunu OpenTelemetry Java SDK'sının kendisi ayarlar. Çocuklar onu miras alır, yani Level 1 parent değişmeden
  sürdürülür. `OPENLOG_SAMPLING_RV=true` yeni trace'lere açıkça `ot=rv:<14 hex>` yazar.
- **Sınırlar.** `ot` değeri 256 karakterle (önce bilinmeyen alt anahtarlar atılır), `tracestate` 32 üyeyle sınırlıdır.
- **Açıkça seçilen sampler'lar.** `OTEL_TRACES_SAMPLER` = `always_on`, `parentbased_always_on`, `traceidratio`,
  `parentbased_traceidratio` veya `openlog` ise openlog sampler'ıyla değiştirilir (oran sampler'ının argümanı oran olur). Diğer
  sampler'lar (`always_off`, `jaeger_remote`, `xray`, …) yapılandırıldığı gibi kalır.

## Resource {#resource}

| Attribute | Kaynak |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | yapılandırma (yedek olarak upstream servis adı algılama) |
| `host.id` | bkz. [Host bağlantısı](#host-linking); upstream'in machine-id algılamasının yerine geçer |
| `host.name`, `host.arch` | Linux: `/proc/sys/kernel/hostname` → `/etc/hostname`, `/proc/sys/kernel/arch` (OTel değerleri `amd64`/`arm64`, altyapı ajanıyla aynı); diğerlerinde upstream |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | upstream; Linux `/etc/os-release`, `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.path`, `process.runtime.{name,version,description}` | upstream; **`process.command_line`/`process.command_args` kaldırılır** (JVM argümanları secret içerebilir) |
| `container.id` | `/proc/self/cgroup` içindeki 64 hex id; özel cgroup namespace'inde `/proc/self/mountinfo` içindeki Docker/Podman container path'lerinden |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward API ortam değişkenleri `K8S_POD_NAME`, … (pod içinde namespace için service account dosyasına, pod adı için `HOSTNAME` değerine düşülür) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version`, `telemetry.sdk.*` | ajan |

`OTEL_RESOURCE_ATTRIBUTES` / `OPENLOG_RESOURCE_ATTRIBUTES` içinde ayarlanan attribute'lar algılama tarafından asla değiştirilmez.

## Host bağlantısı {#host-linking}

Go ve Node.js ajanlarıyla aynıdır ([ayrıntılar](/tr/integrations/go#host-linking)), şu sırayla:

1. çalışan altyapı ajanının `/run/openlog-infra-agent/host-id` içinde yayımladığı id;
2. `/etc/machine-id`, `/var/lib/dbus/machine-id`, `/sys/class/dmi/id/product_uuid`;
3. altyapı ajanının `/var/lib/openlog-infra-agent/host-id` içinde ürettiği id;
4. (macOS/Windows) platformun makine id'si;
5. `OPENLOG_STATE_DIR` içinde üretilip saklanan bir UUID.

Container'larda altyapı ajanının runtime dizinini salt okunur mount et
(`-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro`; Kubernetes: `Directory` tipinde `hostPath`), veya
`OPENLOG_HOST_ID` ayarla.

## Loglar ve runtime metrikleri {#logs-and-runtime-metrics}

**Log ↔ trace.** Logback ve Log4j2 için (ayrıca Log4j 1, JBoss LogManager) ajan `trace_id`, `span_id` ve `trace_flags` değerlerini
MDC'ye / context data'ya koyar, böylece `[trace_id=%X{trace_id} span_id=%X{span_id}]` gibi bir pattern bunları uygulamanın kendi
çıktısında gösterir. Logback, Log4j2 ve `java.util.logging` kayıtları da trace ve span id'lerini taşıyan OTLP log kayıtları olarak
export edilir, bu da [log ↔ trace geçişini](/tr/contracts/apm) sağlar (§11).

**Runtime metrikleri** (upstream `runtime-telemetry`, OpenTelemetry semantic convention adları; scope
`io.opentelemetry.runtime-telemetry-java8`/`-java17`):
- bellek: `jvm.memory.used`, `jvm.memory.committed`, `jvm.memory.limit`, `jvm.memory.used_after_last_gc`;
- GC: `jvm.gc.duration`;
- thread'ler ve sınıflar: `jvm.thread.count`, `jvm.class.loaded`, `jvm.class.unloaded`, `jvm.class.count`;
- CPU: `jvm.cpu.time`, `jvm.cpu.count`, `jvm.cpu.recent_utilization`;
- HTTP instrumentation'ları `http.server.request.duration` ve `http.client.request.duration` ekler.

## Ek yük {#overhead}

`agents/java/test/run.sh bench` (`integration-tests/…/OverheadBench.java`), Spring Boot MVC örneğinin `/hello` route'unu (Tomcat, tek
JSON route) keep-alive yük üreticisiyle doyuma ulaştırır (16 bağlantı). Uygulamanın üç kopyası yan yana çalışır: ajansız, her şeyi
örnekleyen ajan, hiçbir şeyi örneklemeyen ajan. Her biri 45 sn ısıtılır, sonra iç içe geçmiş 5 turda 10 sn yüklenir, böylece host
gürültüsü üçünü de aynı şekilde etkiler. Ajan, veriyi kabul edip atan yerel bir OTLP alıcısına export eder. Benchmark min/medyan/maks
değerlerini raporlar ve çalıştırmalar arası fark 1.5 katı aşarsa ya da bir ajan senaryosu ajansızı geçerse sonucu `INCONCLUSIVE`
olarak işaretler.

**Güncel sonuç: belirsiz. Henüz bir ek yük rakamı iddia edilmiyor.** Ölçüm Docker Desktop üzerinde (Apple M1 Pro VM, 10 CPU)
`eclipse-temurin:21-jdk` içinde yapıldı, aynı VM'de başka projelerin container'ları (~%77 CPU'da bir ClickHouse, bir kind kümesi)
meşguldü:

| Senaryo | req/s medyan (5 turda min–maks) | p50 ms | p99 ms | RSS MiB |
|---|---|---|---|---|
| ajansız | 23 355 (10 907–26 978) | 0.43 | 4.84 | 732 |
| ajan, örneklenen (oran 1) | 17 931 (9 519–24 992) | 0.62 | 4.67 | 855 |
| ajan, örneklenmeyen (oran 0) | 17 041 (14 218–25 723) | 0.53 | 6.25 | 830 |

Üç aralık neredeyse tamamen çakışıyor. Tek bir senaryo içinde throughput 2.5 kat değişirken medyanlar bundan daha az farklı. Medyanlarda
"örneklenmeyen" senaryosu "örneklenen" senaryosundan da yavaş, bunu ancak gürültü üretebilir. Yani bu sayılar ajanı ölçmüyor. İki
benchmark çalıştırmasında da geçerli olanlar:
- ajan yaklaşık **100–125 MiB RSS** ekler, bu class metadata, bytecode instrumentation ve SDK'dır;
- sakin turlarda, ~0.4 ms'lik bu isteğin medyan gecikmesi ajansız çalıştırmanın 0.05–0.2 ms yakınındadır.

Benchmark'ın daha önceki sıralı sürümü (senaryolar art arda) daha da gürültülüydü: bir ajan senaryosu ajansızdan +%191 hızlı çıkmıştı.

Güvenilir bir istek başı maliyet için ayrılmış, boşta bir host gerekir. Orada `BENCH_RUNS=10 agents/java/test/run.sh bench` çalıştır
(CPU'lara başka yük bindirmeden; PostgreSQL başlatılmışken `NO_SERVICES=1` Kafka ve MySQL'i boşta tutar). O zamana kadar fikir vermesi
için, upstream OpenTelemetry Java ajanı [kendi ek yükünü](https://opentelemetry.io/docs/zero-code/java/agent/performance/) belgeliyor.
openlog eklentisi yalnızca başlangıçta (yapılandırma, resource algılama) ve export edilen span batch'i başına (sorgu taşıyan span'lerin
DB sorgusunu yeniden yazma) iş ekler. Maliyeti azaltmak için:
- örnekleme (`OPENLOG_SAMPLING_RATIO`);
- health check'ler için `OPENLOG_HTTP_IGNORE_PATHS`;
- gerekmeyen instrumentation'ları kapatma (`OPENLOG_INSTRUMENTATIONS_DISABLED`).

## Sürümleme ve sürümler {#versioning-and-releases}

Jar, openlog ürünüyle birlikte sürümlenir (D-025). Her `vX.Y.Z` sürümü GitHub sürümüyle birlikte `openlog-javaagent-X.Y.Z.jar` ve
`openlog-javaagent-X.Y.Z.jar.sha256` yayımlar (`release.yml`, job `java-agent-jar`; bkz.
[releasing.md](/tr/operations/releasing#java-agent-jar)). Jar, imzalı sürüm manifest'inde listelenir (`manifest.json`, bileşen
`java-agent`, biçim `jar`): jar'ın olduğu bir dizinde `openlog-release verify --keys <key> --check-artifacts manifest.json` imza ve
sha256'yı kontrol eder, ya da `.sha256` dosyasıyla karşılaştır. Manifest'teki `Openlog-Javaagent-Version` ve
`Openlog-Upstream-Javaagent-Version` attribute'ları ile `telemetry.distro.version` bir jar'ı tanımlar. Upstream ajan sürümü
`gradle.properties` içinde sabitlenir ve openlog sürümleriyle birlikte ilerler. **Maven Central** yayımı
(`io.github.onuragtas.openlog:openlog-javaagent`, ajanları indiren build araçları için) planlanıyor, henüz yapılmadı.

Altyapı ajanı, host'lardaki jar'ı güncellemek için manifest kaydını kullanır (`java_agent`, D-123,
[java-agent.md](/tr/contracts/java-agent) §2): altyapı ajanına derlenmiş sürüm anahtarlarıyla doğrulama, root'a ait `versions/<v>/`,
atomik link değişimi, yüklü sürümle JVM envanteri, fleet dalgaları.

## Geliştirme {#development}

Build araçları container'larda çalışır (`eclipse-temurin` + Gradle wrapper). Runner kaynakları bir Docker volume'una kopyalar, yani
checkout içine hiçbir şey yazılmaz:

```sh
agents/java/test/run.sh                                  # ./gradlew check integrationTest (PostgreSQL, MySQL, Redis, Kafka başlatır)
NO_SERVICES=1 agents/java/test/run.sh :extension:test :agentJar
JAVA_TEST_JDK=17 agents/java/test/run.sh check
agents/java/test/run.sh :integration-tests:integrationTest --tests '*QuarkusIT'   # tek framework
JAVA_TEST_PROJECT=my-prefix JAVA_TEST_KAFKA_PORT=23192 agents/java/test/run.sh   # başka compose/volume adları, Kafka host portu
agents/java/test/run.sh bench                            # ek yük mikro benchmark'ı
agents/java/test/run.sh down                             # container ve volume'ları sil
```

| Path | İçerik |
|---|---|
| `extension/` | openlog eklentisi (Java 8 bytecode, runtime bağımlılığı yok): `OpenlogCustomizerProvider` (SPI), yapılandırma eşlemesi, `sampling/`, `db/`, `resource/`; Go sampler fixture'ları ve SDK autoconfiguration dahil birim testler |
| `build.gradle.kts` | `agentJar`: upstream ajan + `extensions/openlog-extension.jar` → `build/libs/openlog-javaagent-<version>.jar`, `agentJarChecksum` |
| `testapps/mvc`, `testapps/webflux` | Spring Boot örnek uygulamaları |
| `testapps/jaxrs`, `testapps/quarkus`, `testapps/micronaut`, `testapps/vertx` | framework başına aynı küçük uygulama (JDBC'li `/users/{id}`, `/fail`); Quarkus, Quarkus Gradle eklentisiyle derlenir (`quarkusBuild`) |
| `integration-tests/` | OTLP yakalama sunucusu, uygulama başlatıcı, `SpringMvcIT`, `SpringWebfluxIT`, `FrameworkIT` (`JaxrsJerseyIT`, `QuarkusIT`, `MicronautIT`, `VertxWebIT`), `OverheadBench` |
| `scripts/release-jar.sh` | jar + `.sha256` dosyasının bir sürüm dizinine release derlemesi (`make release-java-agent`, `release.yml`) |
| `test/docker-compose.yml` | geçici servisler (proje `openlog-m4-java-test`) ve Gradle runner |

Upstream ajanı yükseltmek için: `gradle.properties` içinde `otelJavaagentVersion`/`otelSdkVersion`/`otelProtoVersion` değerlerini
değiştir, tüm test paketini çalıştır ve eklentinin ayarladığı property'lerin (DB sanitizer, runtime telemetry) adı değişmiş mi diye
upstream sürüm notlarına bak.
