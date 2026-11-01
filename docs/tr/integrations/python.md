# openlog Python ajanı {#openlog-python-agent}

Apache-2.0 · PyPI paketi `openlog-agent` · import `openlog_agent` · Python 3.9 – 3.13

Bu, OpenTelemetry Python SDK ve contrib instrumentation'ları için ince bir dağıtımdır (D-073) ve [Go](/tr/integrations/go) ile
[Node.js](/tr/integrations/node) ajanlarıyla aynı mantıkla kurulmuştur. Tek komutla trace'leri, metrikleri (Python runtime metrikleri
dahil) ve logları OTLP üzerinden openlog'a gönderirsin. Resource, openlog altyapı ajanıyla aynı `host.id` değerini taşır, böylece servis
çalıştığı host'a bağlanır. Örnekleme kararları W3C `tracestate` üzerinden diğer ajanlarla uyumlu çalışır.

(PyPI'daki `openlog` adı ilgisiz bir projeye ait, `openlog-agent` kur.)

## Hızlı başlangıç {#quick-start}

```sh
pip install openlog-agent
```

Bir sürüm PyPI'da yoksa (openlog sürümlerinde registry'ye yayımlamak isteğe bağlıdır), her GitHub sürümüne eklenen wheel dosyasını kur.
"Add data" sayfası sunucunun sürümünde hangisi çalışıyorsa onu gösterir. Ön sürümlerin dosya adında PEP 440 sürümü kullanılır
(`X.Y.Z-beta.N` → `X.Y.ZbN`):

```sh
pip install https://github.com/onuragtas/openlog/releases/download/vX.Y.Z/openlog_agent-X.Y.Z-py3-none-any.whl
```

Sürüm ayrıca sdist (`openlog_agent-X.Y.Z.tar.gz`) ve her biri için bir `sha256sum` dosyası içerir. Wheel, sürümün imzalı `manifest.json`
dosyasında listelenir. Requirements dosyasında URL'i `--hash` kontrolüyle veya `openlog-agent @ <wheel URL>` ile sabitle.

Sıfır kod değişikliği: uygulamayı başlatan komutun başına ekle.

```sh
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 OPENLOG_SERVICE_NAME=checkout
openlog-instrument python app.py
openlog-instrument gunicorn -w 4 -b 0.0.0.0:8000 shop.wsgi:application
openlog-instrument uvicorn main:app --host 0.0.0.0 --workers 4
openlog-instrument celery -A proj worker --concurrency 8
openlog-instrument uwsgi --master --processes 4 --threads 2 --die-on-term --http :8000 --wsgi-file app.py
DJANGO_SETTINGS_MODULE=shop.settings openlog-instrument python manage.py runserver --noreload
```

Ya da koddan, uygulama framework'ünü import etmeden ve app, engine veya client'ları oluşturmadan önce:

```python
# giriş noktasının ilk satırları (wsgi.py, main.py, manage.py, celery app modülü)
import openlog_agent

openlog_agent.start(service_name="checkout", service_version="1.4.0", environment="production")
```

`start()` global TracerProvider, MeterProvider ve LoggerProvider'ı ve kurulu olan her desteklenen kütüphanenin OpenTelemetry
instrumentation'ını kurar, böylece OpenTelemetry API kullanımı (`opentelemetry.trace.get_tracer(...)`) olduğu gibi çalışır. Yalnızca geçersiz
yapılandırmada (`ConfigError`) veya koddan başlatılmış bir ajan zaten çalışıyorsa exception fırlatır. `openlog-instrument` altında çalışan
ajanı döndürür. `openlog-instrument` uygulamaya asla exception yansıtmaz: geçersiz yapılandırma stderr'de raporlanır ve program ajansız
çalışır. Export sorunları (ingest kapalı, `401`, `503`) stderr'e hız sınırıyla loglanır ve uygulamayı asla bloklamaz.

Tamponlanmış telemetri interpreter çıkışında (`OPENLOG_SHUTDOWN_TIMEOUT` ile sınırlı) ve programın kendi işleyicisi yoksa `SIGTERM` anında flush
edilir. Sinyal sonra yeniden tetiklenir, yani süreç ajan yokmuş gibi sonlanır (`OPENLOG_SHUTDOWN_ON_SIGNAL=false` bunu kapatır). `SIGTERM` sinyalini
kendisi işleyen sunucular (gunicorn, uvicorn, Celery) normal çıkar ve çıkışta flush eder. Diğer durumlar için (örn. `os._exit` ile biten kısa ömürlü
işler) `openlog_agent.shutdown()` ve `get_agent().force_flush()` kullanılabilir.

`openlog-instrument`, `opentelemetry-instrument` için bir sarmalayıcıdır: OpenTelemetry'nin `sitecustomize` modülünü `PYTHONPATH` üzerine koyar ve
openlog distro/configurator'ını seçer (`OTEL_PYTHON_DISTRO=openlog`, `OTEL_PYTHON_CONFIGURATOR=openlog`), yani `opentelemetry-distro` kuruluyken de
çalışır. Alt Python süreçleri bunu miras alır. Kendi `sitecustomize` modülü olan bir uygulama bunun yerine `openlog_agent.start()` çağırmalı.

## Yapılandırma {#configuration}

Öncelik sırası: **`start()` seçenekleri > `OPENLOG_*` > `OTEL_*` > varsayılanlar**. İsimler Go ve Node.js ajanlarındakilerle aynıdır.

| Değişken | Seçenek | Varsayılan | Anlamı |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `license_key` | — | Ingest lisans anahtarı, `openlog-license-key` header'ı olarak gönderilir. Bu olmadan openlog ingest `401` döner (bir uyarı loglanır) |
| `OPENLOG_ENDPOINT` (`OTEL_EXPORTER_OTLP_ENDPOINT`) | `endpoint` | `http://localhost:4318` (grpc: `http://localhost:4317`) | OTLP temel URL'i. `http/protobuf` sonuna `/v1/traces`, `/v1/metrics`, `/v1/logs` ekler. `http://` = şifresiz, `https://` veya şemasız = TLS |
| `OPENLOG_PROTOCOL` (`OTEL_EXPORTER_OTLP_PROTOCOL`) | `protocol` | `http/protobuf` | veya `grpc` (`pip install 'openlog-agent[grpc]'`) |
| `OPENLOG_COMPRESSION` (`OTEL_EXPORTER_OTLP_COMPRESSION`) | `compression` | `gzip` | veya `none` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `headers` | — | Ek header'lar (lisans anahtarı header'ı önceliklidir) |
| `OPENLOG_SERVICE_NAME` (`OTEL_SERVICE_NAME`) | `service_name` | `unknown_service:<script>` | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `service_version` | — | `service.version` |
| `OPENLOG_SERVICE_NAMESPACE` | `service_namespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `environment` | — | `deployment.environment.name` |
| `OPENLOG_SAMPLING_RATIO` (`*traceidratio` sampler ile `OTEL_TRACES_SAMPLER_ARG`) | `sampling_ratio` | `1` | Yeni trace'lerin parent tabanlı head sampling oranı (0..1), bkz. [Örnekleme](#sampling) |
| `OPENLOG_SAMPLING_RV` | `sampling_rv` | `false` | Yeni trace'ler açıkça `ot=rv` rastgeleliği yazar |
| `OPENLOG_RESOURCE_ATTRIBUTES` (`OTEL_RESOURCE_ATTRIBUTES` üzerine birleştirilir) | `resource_attributes` | — | `k=v,k2=v2` (percent-encoded değerlere izin verilir) |
| `OPENLOG_HOST_ID` | `host_id` | algılanır | Açıkça verilen `host.id` |
| `OPENLOG_HOST_ROOT` | `host_root` | `/` | Host'un kök dizini mount edildiğinde host dosyaları için önek (örn. `/host`) |
| `OPENLOG_INFRA_RUNTIME_DIR` | `infra_runtime_dir` | `/run/openlog-infra-agent` | Çalışan altyapı ajanının host id'sini yayımladığı yer (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | `infra_state_dir` | `/var/lib/openlog-infra-agent` | Altyapı ajanının ürettiği host id'yi sakladığı yer |
| `OPENLOG_STATE_DIR` | `state_dir` | kullanıcı önbellek dizini `/openlog` | Bu ajanın ürettiği host id'yi sakladığı yer (son çare) |
| `OPENLOG_RUNTIME_METRICS` | `runtime_metrics` | `true` | Python runtime ve süreç metrikleri |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (Go süresi; `OTEL_METRIC_EXPORT_INTERVAL` ms cinsinden) | `metric_interval` (saniye) | `60s` | Metrik export aralığı |
| `OPENLOG_SHUTDOWN_TIMEOUT` (Go süresi) | `shutdown_timeout` (saniye) | `5s` | Son flush'ın üst sınırı |
| — | `export_timeout` (saniye) | `10` | Tek bir export isteğinin zaman aşımı |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | `log_level` | `warn` | stderr'de ajan ve OpenTelemetry SDK tanılamaları: `debug`, `info`, `warn`, `error`, `off` |
| `OPENLOG_ENABLED` (`OTEL_SDK_DISABLED`) | `enabled` | `true` | `false`: hiçbir şey kurulmaz, hiçbir kütüphane instrument edilmez |
| `OPENLOG_DB_QUERY_TEXT` | `db_query_text` | `sanitized` | `db.query.text` / `db.statement` için `sanitized`, `raw` veya `off` |
| `OPENLOG_LOGS_EXPORT` | `logs_export` | `true` | stdlib `logging` kayıtlarını OTLP logları olarak export et |
| `OPENLOG_LOGS_CORRELATION` | `logs_correlation` | `true` | Her `LogRecord` kaydına `trace_id`, `span_id`, `trace_flags` ekle |
| `OPENLOG_INSTRUMENTATIONS_DISABLED` (`OTEL_PYTHON_DISABLED_INSTRUMENTATIONS`) | `disabled_instrumentations` | — | Virgülle ayrılmış adlar (`celery,grpc`), takma adlar veya paket adları |
| — | `instrumentation_config` | — | Ajan varsayılanlarının üzerine birleştirilen, instrumentation başına keyword argümanları, örn. `{"flask": {"excluded_urls": "static/.*"}, "requests": {"request_hook": hook}}` (yalnızca koddan başlatmada) |
| `OPENLOG_HTTP_IGNORE_PATHS` | `http_ignore_paths` | — | Span üretilmeyecek gelen istek path'leri (tam eşleşme), örn. `/healthz,/readyz` |
| `OPENLOG_SHUTDOWN_ON_SIGNAL` | `shutdown_on_signal` | `true` | Programın işleyicisi yoksa `SIGTERM` anında flush et |

Ajan ayrıca şu upstream değişkenlerini, zaten ayarlı değillerse ayarlar: `OTEL_SEMCONV_STABILITY_OPT_IN=http,database` (kararlı HTTP ve veritabanı semantic
convention'ları: `http.request.method`, saniye cinsinden `http.server.request.duration`, `db.query.text`, `db.system.name`, yani Go ve Node.js ajanlarının
gönderdiği adlar), `OPENLOG_HTTP_IGNORE_PATHS` değerinden `OTEL_PYTHON_<FRAMEWORK>_EXCLUDED_URLS` ve `OPENLOG_LOGS_EXPORT=false` olduğunda
`OTEL_PYTHON_LOG_AUTO_INSTRUMENTATION=false`. Diğer `OTEL_PYTHON_*` instrumentation değişkenleri (yakalanan header'lar, hariç tutulan URL'ler, …) upstream'de
belgelendiği gibi çalışır.

**Export davranışı.** Span'ler batch'lenir: kuyruk 4096, batch 512, 5 sn'de bir flush. Loglar da aynı şekilde batch'lenir, 2 sn'de bir flush edilir. Export
istekleri 10 sn sonra zaman aşımına uğrar. OpenTelemetry exporter'ları geçici hataları (`429/502/503/504`, gRPC `UNAVAILABLE`) exponential backoff ile yeniden
dener. Kuyruk dolduğunda uygulamayı bloklamak yerine yeni span'ler atılır. Metrikler kümülatiftir. Ajanın kendi export istekleri asla trace edilmez.

## Framework ve kütüphane desteği {#framework-and-library-support}

Instrumentation'lar ajanla birlikte kurulan, sabitlenmiş OpenTelemetry contrib paketleridir (`pyproject.toml` dosyasına bak). Bir instrumentation yalnızca
kütüphanesi desteklenen bir sürümde kuruluysa etkinleşir.

| Ad (`OPENLOG_INSTRUMENTATIONS_DISABLED`) | Kütüphane | Span'ler / sinyaller |
|---|---|---|
| `django` | Django | `<METHOD> /<route>` adlı SERVER span'leri (ajan, resolver route'larının başına `/` ekler), exception'lar, `http.server.request.duration`. `DJANGO_SETTINGS_MODULE` gerekir |
| `flask` | Flask | `<METHOD> <url rule>` SERVER span'leri, `http.route` ajan tarafından ayarlanır, exception'lar, HTTP metrikleri |
| `fastapi`, `starlette` | FastAPI, Starlette (ASGI) | `<METHOD> <route>` SERVER span'leri, exception'lar, HTTP metrikleri; mesaj başına `http send`/`http receive` span'leri kapalıdır |
| `tornado` | Tornado ≥ 5.1 | Eşleşen kuraldan `<METHOD> <route>` SERVER span'leri (ajan, upstream'in yalnızca metriklere koyduğu `http.route` değerini ayarlar), exception'lar, HTTP metrikleri; `AsyncHTTPClient` CLIENT span'leri |
| `falcon` | Falcon 1.4 – 4.x | `http.route` ile `<METHOD> <uri template>` SERVER span'leri; ajan responder'ın exception'ını `exception` event'i olarak kaydeder (upstream yalnızca durumu ayarlar; `HTTPError`/`HTTPStatus` kaydedilmez); `http.route` içermeyen HTTP metrikleri |
| `pyramid` | Pyramid ≥ 1.7 | `http.route` ile `<METHOD> <route pattern>` SERVER span'leri (upstream yalnızca pattern ile adlandırır, ajan metodu ekler), exception'lar, `http.route` içermeyen HTTP metrikleri |
| `requests`, `httpx`, `aiohttp-client`, `urllib`, `urllib3` | HTTP istemcileri | CLIENT span'leri, W3C propagation, `http.client.request.duration` |
| `psycopg2`, `psycopg`, `asyncpg` | PostgreSQL | CLIENT span'leri, sanitize edilmiş `db.query.text` |
| `pymysql`, `mysqlclient` | MySQL / MariaDB | CLIENT span'leri, sanitize edilmiş `db.query.text` (`"…"` string'dir) |
| `sqlalchemy` | SQLAlchemy 1.x, 2.0 | Sorgu başına CLIENT span'leri (artı sürücünün span'leri) |
| `redis` | redis-py (sync ve asyncio) | CLIENT span'leri, `db.query.text` = `SET ? ?` |
| `celery` | Celery | Broker üzerinden propagation ile PRODUCER `apply_async/<task>` ve CONSUMER `run/<task>` span'leri |
| `aio-pika` (takma ad `rabbitmq`) | aio-pika 7.2 – 9.x (RabbitMQ) | `Exchange.publish` için PRODUCER span'leri, `Queue.consume` callback'leri etrafında producer span'inin çocuğu olarak CONSUMER span'leri (W3C context mesaj header'larında), `messaging.system=rabbitmq` |
| `confluent_kafka` | confluent-kafka 1.8 – 2.x | PRODUCER `<topic> send` span'leri; `poll`/`consume` için producer span'lerine **link veren** CONSUMER span'leri (tek bir poll birkaç trace'in mesajlarını döndürebilir), `messaging.system=kafka` |
| `kafka` (`kafka-python`) | kafka-python 2.x (3.x henüz upstream'de desteklenmiyor) | PRODUCER `<topic> send`, kayıt başına (iteration) producer span'inin çocuğu olarak CONSUMER `<topic> receive` |
| `aiokafka` | aiokafka 0.8 – 0.x | PRODUCER `<topic> send`, producer span'inin çocuğu olarak CONSUMER `<topic> receive` (`getone`/`getmany`) |
| `grpc` (`grpc_client`, `grpc_server`, `grpc_aio_client`, `grpc_aio_server`) | grpcio | SERVER/CLIENT span'leri (`rpc.system`, `rpc.service`, `rpc.method`, `rpc.grpc.status_code`) |
| `logging` | stdlib `logging` | Logger seviyelerinden geçen kayıtların trace context'iyle OTLP log export'u |

`kafka-all` üç Kafka instrumentation'ını birden kapatır.

CI paketi Python 3.9, 3.12 ve 3.13 üzerinde çalışır: Flask 3.1, Django 4.2 (Python 3.9) ve 6.1 (3.12+), uvicorn ile FastAPI 0.128/0.141, Tornado 6.5, Falcon 4.3,
Pyramid 2.0/2.1, gunicorn 23/26, uWSGI 2.0.31, gevent 25.9/26.8, eventlet 0.40/0.41, psycopg2 2.9, psycopg 3.3, asyncpg 0.31, PyMySQL 1.2, mysqlclient 2.2,
SQLAlchemy 2.0, redis 8.1, Celery 5.6, grpcio 1.83, aiohttp 3.14, aio-pika 9.6 (RabbitMQ 4.1), confluent-kafka 2.15, kafka-python 2.3 ve aiokafka 0.14 (Kafka 4.1)
(veritabanı, broker, Celery ve gRPC paketi Python 3.12 üzerinde).

**Transaction adları.** APM backend'i bir web transaction'ını `<METHOD> <http.route>` olarak adlandırır ([apm.md §2.1](/contracts/apm)). Route'u olmayan istekler
(404'ler) backend'in path normalizasyonuyla gruplanır. gRPC transaction'ları `<rpc.service>/<rpc.method>` biçimindedir. Celery görevleri ve RabbitMQ/Kafka consumer'ları
mesajlaşma (`consumer`) giriş span'leridir.

**Veritabanı sorguları.** `OPENLOG_DB_QUERY_TEXT=sanitized` (varsayılan) ile SQL, Go ve Node.js ajanlarıyla birebir aynı şekilde normalize edilir (ortak test senaryoları):
string, sayısal, hex ve dollar-quoted literal'ler → `?`, `IN (…)` ve çok satırlı `VALUES` → `(?)`, yorumlar silinir, placeholder'lar (`%s`, `%(name)s`, `$1`, `?`) korunur.
MySQL'de `"…"` string'dir, diğerlerinde identifier. Redis komutları `CMD ? ?` olur. Sorgular 4096 UTF-8 bayt ile sınırlanır. `raw` metni korur, `off` kaldırır. Sanitize
işlemi istekte değil, exporter thread'inde çalışır. Sorgu parametreleri asla yakalanmaz.

## Loglar {#logs}

Logger seviyesinden geçen stdlib `logging` kayıtları, aktif span'in trace ve span id'siyle OTLP logları olarak export edilir (root logger üzerindeki OpenTelemetry
logging handler'ı). Uygulamanın kendi çıktısı da id'leri alır: her `LogRecord` kaydında `trace_id`, `span_id` ve `trace_flags` bulunur (span dışında boştur), yani şöyle bir format

```python
logging.basicConfig(format="%(asctime)s %(levelname)s %(message)s trace_id=%(trace_id)s span_id=%(span_id)s")
```

veya bir JSON formatter bunları gösterir. structlog:

```python
import structlog, openlog_agent

structlog.configure(processors=[openlog_agent.structlog_processor, structlog.processors.JSONRenderer()])
```

`structlog.stdlib.LoggerFactory()` ile kayıtlar stdlib logging'den de geçer ve OTLP logları olarak export edilir.

## Pre-fork sunucular ve worker'lar {#pre-fork-servers-and-workers}

Ajanı sunucu fork etmeden önce başlatmak güvenlidir:

- OpenTelemetry SDK her child'da export thread'lerini yeniden başlatır (`os.register_at_fork`) ve ajan resource içindeki `process.pid` değerini günceller, böylece
  her worker kendi span ve metriklerini kendi pid'i altında export eder. Uçtan uca test paketi bunu gunicorn (`openlog-instrument gunicorn -w 3` ve app modülünde `start()`
  ile `--preload`) ve Celery'nin prefork havuzuyla kontrol eder: hiçbir span kaybolmaz ve her worker pid'i görünür.
- gunicorn ve Celery worker'ları düzgün kapanışta çıkarken flush eder. Öldürülen veya yenilenen (`--max-tasks-per-child`) Celery prefork child'ları tamponlanmış
  span'lerin en fazla son 5 sn'sini kaybeder.
- `uvicorn --workers N` ve `spawn` başlatma yöntemiyle `multiprocessing` yeni interpreter'lar başlatır. `openlog-instrument` altında her biri ajanı kendisi başlatır
  (miras alınan `PYTHONPATH`). Koddan `start()` kullanıyorsan worker'ın import ettiği modülde çağır.

Ajan yalnızca worker'larda başlamalıysa (örneğin uygulama import sırasında fork boyunca paylaşılmaması gereken thread veya bağlantılar oluşturuyorsa), onu gunicorn'un
`post_fork` hook'undan başlat ve `--preload` kullanma:

```python
# gunicorn.conf.py
from openlog_agent import post_fork  # ajanı her worker'da başlatır
```

### uWSGI {#uwsgi}

uWSGI worker'larını C içinde, Python'ın fork hook'ları olmadan fork eder, yani master'da başlatılan export thread'leri worker'larda çalışmaz (istekler, olmayan thread'lerin
tuttuğu kilitlerde takılır). Bu yüzden ajan, `openlog-instrument` altında ve master'ın import ettiği bir modülde `start()` ile uWSGI master'ını algılar ve orada yalnızca
instrumentation'ları kurar. Provider'lar ve export thread'leri her worker'da `uwsgi.post_fork_hook` üzerinden başlar (uygulama `uwsgidecorators` import ediyorsa onun `@postfork`
zincirinde ilk sırada tutulur). Python < 3.12'de `uwsgi` modülü `openlog-instrument` başlarken henüz yoktur. Ajan, uWSGI modülü oluşturduktan sonra çalışan ilk `import` ifadesinde
(uygulama yüklenirken) hook'u ayarlar, sonra kendi import hook'unu kaldırır. `get_agent()` master'da bekleyen bir ajan döndürür.

```ini
[uwsgi]
master = true
processes = 4
threads = 2          ; isteğe bağlı
lazy-apps = false    ; ikisi de çalışır
die-on-term = true   ; aksi halde uWSGI 2.0 SIGTERM'de yeniden yüklenir
; py-call-osafterfork yok: gerekli değil ve Python 3.13 ile worker'ları abort eder (Fatal Python error: PyMutex_Unlock)
```

```sh
openlog-instrument uwsgi --ini uwsgi.ini --http :8000 --wsgi-file app.py
```

Uçtan uca test paketi `--master --processes 3` kombinasyonunu `lazy-apps` ve `threads` ile ve onlarsız, `uwsgidecorators` ile ve koddan `start()` ile kontrol eder: her worker kendi
`process.pid` değeri altında export eder, master hiçbir şey export etmez, W3C propagation worker'lar arasında çalışır ve düzgün durdurma (`die-on-term` ile `SIGINT`/`SIGTERM`) hiç span
kaybettirmez, çünkü worker'lar çıkarken flush eder. uWSGI `Python threads support is disabled` loglarsa (eski sürümler) `enable-threads = true` ekle, yoksa export thread'leri istekler
arasında çalışmaz. `max-requests` ile yenilenen worker'lar da diğerleri gibi çıkarken flush eder.

### gevent ve eventlet {#gevent-and-eventlet}

Monkey-patch'li sunucular `openlog-instrument` altında (ajan patch'ten önce başlar) ve `gevent.monkey.patch_all()` / `eventlet.monkey_patch()` çağrısının hemen ardından çağrılan
`openlog_agent.start()` ile çalışır. Önce patch'lemek gevent ve eventlet'in önerdiği sıradır. Uçtan uca test paketi `gevent.pywsgi`, `eventlet.wsgi` ve `gunicorn -k gevent` worker'larını
eşzamanlı isteklerle kontrol eder: hiçbir span kaybolmaz, aktif span her greenlet'i izler (eşzamanlı isteklerin span'leri asla karışmaz) ve `SIGTERM` flush eder (flush bir greenlet'te çalışır,
çünkü event loop sinyal işleyicilerini bloklamanın yasak olduğu yerde çalıştırır).

İstek içinde başlatılan bir greenlet, yeni bir thread gibi boş context ile başlar: span'leri yeni trace'ler başlatır. Trace içinde kalması için onu isteğin context'inin bir kopyasında
çalıştır, greenlet başına bir kopya:

```python
import contextvars, gevent

gevent.spawn(contextvars.copy_context().run, fetch_price, item_id)   # eventlet.spawn(...) için de aynısı
```

`openlog-instrument` altında gevent, süreç çıkarken `threading` kaynaklı zararsız `KeyError` traceback'leri yazdırabilir (patch'ten önce başlatılan thread'ler). gunicorn 26'da artık eventlet
worker'ı yok.

## Örnekleme {#sampling}

`OPENLOG_SAMPLING_RATIO=p` yeni trace'leri `p` olasılığıyla örnekler, downstream servisler bu karara uyar (parent tabanlı). APM sayıları doğru kalır, çünkü saklanan her span `1/p` ile
ağırlıklandırılır ([apm.md §4](/contracts/apm)). Anlam Go ajanıyla aynıdır ([kılavuz](/tr/integrations/go#sampling)) ve onun sampler'ından üretilen fixture'lara karşı doğrulanmıştır
(`agents/node/test/interop/go-sampler-fixtures.json`):

- **Kök span'ler.** `T = (1 − p) · 2^56`. Rastgelelik varsa `ot=rv`, yoksa trace id'nin alt 56 biti. `R ≥ T` olduğunda örneklenir. Örneklenen kök span `sampling.ratio = p` ve `tracestate`
  `ot=th:<T>` alır (örn. `p = 0.25` → `ot=th:c`).
- **Downstream.** Örneklenmiş uzak parent'ı `p < 1` ile `ot=th` (veya eski `ot=p`) taşıyan yerel giriş span'i `sampling.ratio = p` alır. Yerel oran upstream'deki kararı değiştirmez.
- **Random bayrağı.** Kök span'ler W3C Level 2 random bayrağını taşır (`traceparent` bayrakları `03`/`02`), çocuklar onu miras alır. `OPENLOG_SAMPLING_RV=true` yeni trace'lere açıkça
  `ot=rv:<14 hex>` yazar.
- **Sınırlar.** `ot` değeri 256 karakterle (önce bilinmeyen alt anahtarlar atılır), `tracestate` 32 üyeyle sınırlıdır.

## Resource {#resource}

| Attribute | Kaynak |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | yapılandırma |
| `host.id` | bkz. [Host bağlantısı](#host-linking) |
| `host.name`, `host.arch` | Linux: `/proc/sys/kernel/hostname` → `/etc/hostname`; `/proc/sys/kernel/arch` (OTel değerleri, altyapı ajanıyla aynı); diğerlerinde `socket.gethostname()`, `platform.machine()` |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | platform; `/etc/os-release`; `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.{name,path}`, `process.command` (script path'i, argümansız), `process.owner`, `process.runtime.{name,version,description}` | süreç (komut satırı argümanları gönderilmez, secret içerebilirler) |
| `container.id` | `/proc/self/cgroup` içindeki 64 hex id; özel cgroup namespace'inde `/proc/self/mountinfo` içindeki Docker/Podman container path'lerinden |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward API ortam değişkenleri `K8S_POD_NAME`, … (pod içinde namespace için service account dosyasına, pod adı için `HOSTNAME` değerine düşülür) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version`, `telemetry.sdk.*` | ajan |

Öncelik sırası: algılanan < ortamdan veya seçeneklerden gelen resource attribute'ları < açıkça verilen servis, environment ve host id ayarları.

## Host bağlantısı {#host-linking}

Go ajanıyla aynıdır ([ayrıntılar](/tr/integrations/go#host-linking)): çalışan altyapı ajanının `/run/openlog-infra-agent/host-id` içinde yayımladığı id → `/etc/machine-id` →
`/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid` → altyapı ajanının `/var/lib/openlog-infra-agent/host-id` içinde ürettiği id → (macOS/Windows) platformun makine id'si →
`OPENLOG_STATE_DIR` içinde üretilip saklanan bir UUID. Container'larda altyapı ajanının runtime dizinini salt okunur mount et: `-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro`
(Kubernetes: `Directory` tipinde `hostPath`), veya `OPENLOG_HOST_ID` ayarla.

## Runtime metrikleri {#runtime-metrics}

Her metrik toplamasında okunur (varsayılan olarak 60 sn'de bir); scope `openlog_agent.runtime`. Adlar OpenTelemetry semantic convention'larını izler (Go ajanının politikası: varsa semconv
adları, yoksa `openlog.` öneki).

| Metrik | Tip | Birim | Attribute'lar |
|---|---|---|---|
| `cpython.gc.collections` | Sum, monotonic | `{collection}` | `cpython.gc.generation` = 0/1/2 |
| `cpython.gc.collected_objects`, `cpython.gc.uncollectable_objects` | Sum, monotonic | `{object}` | `cpython.gc.generation` |
| `openlog.cpython.gc.time` | Sum, monotonic | `s` | `cpython.gc.generation` (toplama işlemlerindeki toplam süre) |
| `openlog.cpython.gc.pause.max` | Gauge | `s` | `cpython.gc.generation` (önceki export'tan beri en uzun toplama) |
| `process.cpu.time` | Sum, monotonic | `s` | `cpu.mode` = `user`/`system` |
| `process.memory.usage`, `process.memory.virtual` | Sum, non-monotonic | `By` | — (RSS / virtual; Linux, diğer platformlarda `psutil` kuruluysa) |
| `process.thread.count` | Sum, non-monotonic | `{thread}` | — |
| `process.open_file_descriptor.count` | Sum, non-monotonic | `{file_descriptor}` | — (Linux) |
| `process.context_switches` | Sum, monotonic | `{context_switch}` | `process.context_switch.type` = `voluntary`/`involuntary` (Linux) |

GC süresi histogram yerine bir sayaç artı bir maks gauge'dur: `gc.callbacks` hook'u yalnızca düz sayıları günceller, çünkü bir garbage collection içinden SDK instrument'ına kayıt yapmak
SDK kilitlerine yeniden girebilir. Web framework instrumentation'ları ayrıca `http.server.request.duration`, HTTP istemcileri ise `http.client.request.duration` kaydeder.

## Ek yük {#overhead}

`make bench` (`tests/bench/overhead.py`): tek süreçli sunucular, kalıcı bağlantılarla 8 yük üretici süreç tarafından doyuma ulaştırılır (8 sn × 3 çalıştırma, medyan), gerçek ajan yerel bir
OTLP yakalama sunucusuna export eder (tüm instrumentation'lar, logging handler ve runtime metrikleri açık; route log yazmaz). Ölçüm Docker Desktop içinde (arm64, 10 CPU, yük üreticisi aynı
makinede) Python 3.12 ile yapıldı:

Flask 3.1 / gunicorn (1 `gthread` worker, 1 thread):

| Senaryo | req/s | ajansıza göre | p50 ms | p99 ms | RSS MB | istek başına eklenen |
|---|---|---|---|---|---|---|
| ajansız | 2615 | — | 2.30 | 9.34 | 37 | — |
| ajan, örneklenen (oran 1) | 1651 | −36.9 % | 3.99 | 26.24 | 70 | 0.22 ms |
| ajan, örneklenmeyen (oran 0) | 2638 | +0.9 % | 2.40 | 6.26 | 65 | ≈ 0 (gürültü içinde) |

FastAPI 0.141 / uvicorn (1 süreç):

| Senaryo | req/s | ajansıza göre | p50 ms | p99 ms | RSS MB | istek başına eklenen |
|---|---|---|---|---|---|---|
| ajansız | 7726 | — | 0.88 | 3.57 | 47 | — |
| ajan, örneklenen (oran 1) | 1923 | −75.1 % | 2.39 | 28.04 | 84 | 0.39 ms |
| ajan, örneklenmeyen (oran 0) | 3826 | −50.5 % | 1.63 | 8.81 | 78 | 0.13 ms |

Sunucu doyuma ulaştığı için istek başına servis süresi `1/rps` olur: ajan **örneklenen istek başına yaklaşık 0.2 ms (WSGI) ile 0.4 ms (ASGI)** (server span'i, HTTP metrikleri, GIL'i paylaşan
bir thread'de batch'leme ve export), üstüne ~30–40 MB RSS ekler. Örneklenmeyen istekler Flask ile neredeyse bedavadır. FastAPI ile upstream ASGI middleware'i istek başına yine ~0.13 ms tutar.
Göreli düşüş yalnızca bu handler'lar neredeyse hiçbir şey yapmadığı için (0.1–0.4 ms) büyüktür. Uygulama kodunda ve I/O'da 20 ms harcayan bir istek için örneklenen ek yük %1–2'dir. Bu, ajanın
üzerine kurulduğu upstream OpenTelemetry Python instrumentation'larıyla uyumludur ve Go (~4 µs) ile Node.js (~0.2 ms) ajanlarından yüksektir. Azaltmanın yolları: örnekleme
(`OPENLOG_SAMPLING_RATIO`), gerekmeyen kütüphaneler için `OPENLOG_INSTRUMENTATIONS_DISABLED` ve health check'ler için `OPENLOG_HTTP_IGNORE_PATHS`. Bunlar ayrılmış bir benchmark host'undan değil,
paylaşılan bir geliştirme makinesinden alınmış mikro benchmark sayılarıdır.

## Sürümleme ve güncelleme {#versioning-and-upgrading}

Paket openlog ürünüyle birlikte sürümlenir (D-025): her ürün sürümü `vX.Y.Z`, PyPI'a `openlog-agent==X.Y.Z` yayımlar (ön sürümler `vX.Y.Z-beta.N`, `X.Y.ZbN` olarak, yalnızca `pip install --pre`
ile kurulur). Backend, son üç minor sürümünün ajanlarını kabul eder. OpenTelemetry bağımlılıkları sürüm başına tam olarak sabitlenir: Python ≥ 3.10 SDK 1.44.0 / contrib 0.65b0 alır.
OpenTelemetry'nin SDK 1.42'de bıraktığı Python 3.9 ise destekleyen son hattı (1.41.1 / 0.62b1) alır ve daha yeni upstream düzeltmeleri almaz. Uygulaman OpenTelemetry paketlerini kendisi
sabitliyorsa aynı sürümleri kullan.

```sh
pip install openlog-agent==X.Y.Z
```

## Geliştirme {#development}

Her şey container'larda çalışır (iCloud Drive üzerindeki yerel Python araçları takılabilir):

```sh
make install test                     # Python 3.12: venv volume'u, birim + uçtan uca testler (uWSGI için gcc kurar)
make install test PYTHON_VERSION=3.13
make install test PYTHON_VERSION=3.9  # Python 3.9 hattı
make lint
make test-integration                 # gerçek PostgreSQL/MySQL/Redis/RabbitMQ/Kafka, Celery, gRPC (compose projesi openlog-m4-python-test)
make bench                            # ek yük mikro benchmark'ı
make build                            # dist/ içinde wheel + sdist
```

Birim testler yapılandırmayı, resource algılamayı, sampler'ı (Go fixture'ları), SQL sanitizer'ı (Go ve Node.js ajanlarıyla ortak senaryolar), export anında span yeniden yazmayı, runtime metriklerini
ve instrumentation varsayılanlarını kapsar. Uçtan uca testler Flask, Django, FastAPI, Tornado, Falcon ve Pyramid uygulamalarını `openlog-instrument` altında (ve `start()` ile) bir OTLP yakalama
sunucusuna karşı başlatır: route adları, hatalar, propagation, HTTP ve runtime metrikleri, ilişkilendirilmiş loglar, resource, lisans header'ı, yok sayılan path'ler, SIGTERM flush'ı, kapalı ajan, geçersiz
yapılandırma, ingest kesintisi, gunicorn ve uWSGI pre-fork worker'ları ve gevent/eventlet sunucuları. Entegrasyon paketi gerçek RabbitMQ ve Kafka broker'ları üzerinden üretir ve tüketir (producer ve
consumer tek trace'te, confluent-kafka için link'li). Sürüm akışı [releasing.md](/tr/operations/releasing#python-agent-package) dosyasında anlatılıyor.
