# Sözleşme: Servis yapılandırması (v1) {#contract-service-configuration-v1}

Tüm backend servisleri **yalnızca ortam değişkenleriyle** yapılandırılır (12-factor), böylece aynı image Compose'da da Kubernetes'te de çalışır.
Süreler Go söz dizimini kullanır (`2s`, `500ms`). Listeler virgülle ayrılır.

Aşağıdaki her değişken iki kurulumda da ayarlanabilir: Compose her birini `.env` üzerinden geçirir
(`deploy/compose/.env.example` hepsini gruplu ve yorum satırı olarak listeler; boş = buradaki varsayılan), Helm chart'ında
(`deploy/helm/openlog/values.yaml`) ise her biri için yapılandırılmış bir değer, bir bileşen `config` kaydı veya üst düzey `config`
map'i vardır; secret'lar (SMTP parolası, anahtar hash secret'ları, SSO secret anahtarları, CAPTCHA secret'ı, ClickHouse okuma parolası,
S3 anahtarları, bootstrap anahtarları) chart Secret'ında veya `auth.existingSecret` içinde durur. `internal/config` tarafından okunan bir değişken
bu belgede, Compose'da veya chart'ta yoksa `internal/config/envcoverage_test.go` başarısız olur; bilerek dışarı açılmayan birkaç değişken
(Compose: port eşlemeleriyle sabitlenen dinleme adresleri, paketle gelen container'ların bağımlılık TLS/SASL ayarları;
Helm: yalnızca `openlog-allinone`'a ait anahtarlar) gerekçesiyle birlikte orada listelenir.

## Ortak (tüm servisler) {#common-all-services}

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (stdout'a JSON log) |
| `OPENLOG_ADMIN_ADDR` | `:9464` | Admin HTTP: `/healthz`, `/readyz`, `/metrics` (Prometheus). `/readyz` yalnızca kayıtlı her kontrol geçerse 200 döner: bağımlılıklar (`kafka`, `kafka_topics`, `clickhouse`, `postgres`, …), servisin kendi portu (`ingest_listener`, `api_listener`) ve `openlog-allinone`'da `OPENLOG_MIGRATE_ON_START` ile, başlangıç migration'ları bitene kadar `migrations` (`"applying migrations"`) |
| `OPENLOG_KAFKA_BROKERS` | `localhost:9092` | Kafka bootstrap broker'ları |
| `OPENLOG_KAFKA_TOPIC_PREFIX` | `openlog` | Bkz. [kafka.md](/tr/contracts/kafka) |
| `OPENLOG_CLICKHOUSE_ADDR` | `localhost:9000` | ClickHouse native protokol adresleri |
| `OPENLOG_CLICKHOUSE_DATABASE` | `openlog` | `openlog` olmak zorunda (şema bunu sabit kodlar; başka her değer reddedilir) |
| `OPENLOG_CLICKHOUSE_USER` | `default` | |
| `OPENLOG_CLICKHOUSE_PASSWORD` | `` | |
| `OPENLOG_CLICKHOUSE_CLUSTER` | `openlog` | `ON CLUSTER` DDL'lerinde kullanılan cluster adı |
| `OPENLOG_AUTH_MODE` | `postgres` | `postgres`: tenant'lar, kullanıcılar, oturumlar ve anahtarlar PostgreSQL'de ([postgres.md](/tr/contracts/postgres)). `static`: **yalnızca geliştirme ve testler için**: lisans anahtarları `OPENLOG_LICENSE_KEYS`'ten gelir, kullanıcı yoktur; API aynı lisans anahtarlarını (viewer olarak) kabul eder ve yönetim endpoint'i sunmaz, bu yüzden web arayüzüne giriş yapılamaz |
| `OPENLOG_LICENSE_KEYS` | `` | Yalnızca `static` modda: `key1=tenant1,key2=tenant2`, ingest ve api tarafından kullanılır. `postgres` modda yok sayılır |
| `OPENLOG_POSTGRES_DSN` | `` | `postgres` modu (ingest, api, migrate, allinone, admin): örneğin `postgres://openlog@postgres:5432/openlog?sslmode=require` (libpq URL'i veya key=value biçimi; `sslmode`, `connect_timeout`, … dikkate alınır) |
| `OPENLOG_POSTGRES_PASSWORD` | `` | DSN'deki parolayı ezer (parola ayrı bir Secret'tan gelebilsin diye) |
| `OPENLOG_POSTGRES_MAX_CONNS` | `10` | Süreç başına bağlantı havuzu boyutu |
| `OPENLOG_KEY_HASH_SECRET` | `` | `postgres` modu (ingest, api, allinone, admin; **her yerde aynı değer**): ingest lisans anahtarları ve API anahtarları `SHA-256(key)` yerine `HMAC-SHA256(secret, key)` olarak saklanır (D-044). En az 32 byte (`openssl rand -base64 48`); yedeklerinle birlikte sakla. Secret olmadan yazılmış satırlar çalışmaya devam eder ve ilk başarılı kullanımlarında HMAC'e çevrilir (aynı sorguda bir `UPDATE` daha, yalnızca cache miss olduğunda). Her pod bunu bilen bir sürüme geçtikten **sonra** ayarla (eski pod'lar yeniden yazılmış anahtarları çözemez). **SaaS için önerilir** |
| `OPENLOG_KEY_HASH_SECRET_PREVIOUS` | `` | Rotasyon: önceki secret. Onunla hash'lenmiş anahtarlar çözülmeye devam eder ve kullanıldıkça güncel secret'a çevrilir. Her aktif anahtar rotasyondan sonra en az bir kez kullanılana kadar tut (`last_used_at`); daha önce kaldırırsan kullanılmamış anahtarlar geçersiz olur. Hâlâ satırı olan bir secret'ı asla kaldırma (yeniden hash'lenecek düz metin yok) |

### TLS ve SASL (bağımlılığı kullanan tüm servisler) {#tls-and-sasl-all-services-that-use-the-dependency}

Kafka ayarları her Kafka istemcisine (ingest producer, processor consumer group, migrate topic oluşturma);
ClickHouse ayarları her native bağlantıya (processor bootstrap **ve doğrudan shard/replica bağlantıları**, api,
migrate); PostgreSQL ayarları ingest, api, migrate, allinone ve admin'e uygulanır. Geçersiz kombinasyonlar veya okunamayan dosyalar
servisi başlangıçta yapılandırma hatasıyla durdurur.

**Yeniden başlatmadan sertifika rotasyonu (D-048).** CA, sertifika ve anahtar dosyaları içerikleri değiştiğinde yeniden okunur
(en fazla 10 sn'de bir, bir sonraki yeni bağlantıda kontrol edilir): yeni Kafka broker, ClickHouse (bootstrap ve replica
havuzları) ve PostgreSQL havuz bağlantıları yenilenen dosyaları kullanır; açık bağlantılar kapanana kadar kendi sertifikalarını korur
(ClickHouse havuzları bağlantıları 1 saat sonra, PostgreSQL 5 dk boşta kaldıktan sonra yeniler; Kafka bağlantıları uzun ömürlüdür ve
yalnızca broker bağlantısı koptuğunda yeniden kurulur). Dosyaları atomik olarak değiştir (Kubernetes Secret volume'leri ve cert-manager
böyle yapar). Yüklenemeyen bir dosya kümesi (örneğin rotasyon sürerken yeni sertifika ve eski anahtar) bir kez loglanır
(`certificate reload failed; keeping the previous certificates`) ve yüklenene kadar önceki sertifikalar kullanılmaya devam eder;
başarılı olunca `certificates reloaded` loglanır. PostgreSQL: yalnızca `OPENLOG_POSTGRES_TLS_*` dosyaları izlenir
(DSN'e yazılan `sslrootcert=` vb. izlenmez). ingest ve api düz HTTP/gRPC sunar; istemcilere doğru TLS
ingress / Envoy'da sonlandırılır.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_KAFKA_TLS_ENABLED` | `false` | Broker'lara TLS (listener `SSL` veya `SASL_SSL`). Diğer `OPENLOG_KAFKA_TLS_*` değişkenleri bunu gerektirir |
| `OPENLOG_KAFKA_TLS_CA_FILE` | `` | Broker sertifikalarını imzalayan PEM CA paketi; boş = sistem kökleri |
| `OPENLOG_KAFKA_TLS_CERT_FILE` / `OPENLOG_KAFKA_TLS_KEY_FILE` | `` | PEM istemci sertifikası ve anahtarı (mutual TLS, `ssl.client.auth=required`); ya ikisi ya hiçbiri |
| `OPENLOG_KAFKA_TLS_SERVER_NAME` | `` | Broker sertifikalarında doğrulanan ad; boş = her broker adresinin host'u (bootstrap ve advertised listener'lar) |
| `OPENLOG_KAFKA_TLS_INSECURE_SKIP_VERIFY` | `false` | Broker sertifikası doğrulamasını atlar. **Yalnızca test için**: ortadaki adam saldırısına izin verir |
| `OPENLOG_KAFKA_SASL_MECHANISM` | `` | `PLAIN`, `SCRAM-SHA-256` veya `SCRAM-SHA-512` (büyük/küçük harf duyarsız); boş = SASL yok. TLS olmadan bu `SASL_PLAINTEXT` olur (PLAIN bu durumda parolayı düz metin gönderir) |
| `OPENLOG_KAFKA_SASL_USERNAME` / `OPENLOG_KAFKA_SASL_PASSWORD` | `` | Bir mekanizma ayarlıysa zorunlu |
| `OPENLOG_CLICKHOUSE_TLS_ENABLED` | `false` | TLS üzerinden native protokol. `OPENLOG_CLICKHOUSE_ADDR` güvenli native portu kullanmalı (`tcp_port_secure`, genellikle `9440`) |
| `OPENLOG_CLICKHOUSE_TLS_CA_FILE` | `` | PEM CA paketi; boş = sistem kökleri |
| `OPENLOG_CLICKHOUSE_TLS_CERT_FILE` / `OPENLOG_CLICKHOUSE_TLS_KEY_FILE` | `` | İstemci sertifikası ve anahtarı (sunucuda `verificationMode` `strict`); ya ikisi ya hiçbiri |
| `OPENLOG_CLICKHOUSE_TLS_SERVER_NAME` | `` | Sunucu sertifikasında doğrulanan ad; boş = bağlanılan host. Ayarlanırsa her replica bağlantısına da uygulanır, bu yüzden tüm replica'lar tek bir sertifika adını paylaşmıyorsa çok shard'lı cluster'larda boş bırak |
| `OPENLOG_CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY` | `false` | **Yalnızca test için** |
| `OPENLOG_POSTGRES_TLS_CA_FILE` | `` | `sslrootcert` değerini ayarlar (DSN'dekini ezer). DSN'de `sslmode=verify-full` ile kullan |
| `OPENLOG_POSTGRES_TLS_CERT_FILE` / `OPENLOG_POSTGRES_TLS_KEY_FILE` | `` | `sslcert` / `sslkey` ayarlar (istemci sertifikasıyla kimlik doğrulama); ya ikisi ya hiçbiri |

TLS istemcileri TLS 1.2 veya üstünü ister. Hata mesajları değişkeni adıyla söyler (`OPENLOG_KAFKA_TLS_CA_FILE: open …: no such
file or directory`); doğrulanamayan bir sunucu sertifikası loglarda ve readiness kontrollerinde
`x509: certificate signed by unknown authority` (yanlış CA) veya `x509: certificate is valid for …, not …` (ad) olarak görünür.

**PostgreSQL.** `sslmode` her zaman `OPENLOG_POSTGRES_DSN`'den alınır. Production: `OPENLOG_POSTGRES_TLS_CA_FILE` veya DSN'de `sslrootcert=` ile
`sslmode=verify-full` (şifreler, CA'yı ve host adını doğrular). `require` yalnızca
şifreler (kök sertifika verilmedikçe sunucu doğrulaması yok, libpq semantiği); `prefer`/`allow` düz metne dönebilir,
`disable` ise hiç şifrelemez. Bunları yalnızca güvenilir ağlarda kullan.

**Kafka.** Cluster profilinde `SCRAM-SHA-512` ile bir `SASL_SSL` listener (Strimzi: listener `tls: true` +
`authentication: {type: scram-sha-512}` ve bir `KafkaUser`) veya mutual TLS (`authentication: {type: tls}`) kullan, Helm
chart README'sine bak.

### ClickHouse salt okunur kullanıcı ve tenant başına sorgu limitleri (api, alert, allinone; D-047) {#clickhouse-read-only-user-and-per-tenant-query-limits-api-alert-allinone-d-047}

`openlog-api` ve `openlog-alert`'in tenant sorguları (her `/api/v1` telemetri okuması, alert değerlendirmeleri ve önizlemeler)
salt okunur bir kullanıcıyla ayrı bir bağlantı kullanır. Yazan kullanıcı (`OPENLOG_CLICKHOUSE_USER`) openlog-migrate,
processor, APM edge bağlama ve alert değerlendirme geçmişi insert'leri için kullanımda kalır, yani api/alert pod'larının yine ikisine de ihtiyacı vardır.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_CLICKHOUSE_READ_USER` | `` | Salt okunur kullanıcı; yazan kullanıcıyla aynı adresler, veritabanı ve TLS. Boş = sorgular `OPENLOG_CLICKHOUSE_USER` kullanır ve servis uyarı loglar. Kullanıcının `readonly` ayarı `0` ise servis başlangıçta uyarır |
| `OPENLOG_CLICKHOUSE_READ_PASSWORD` | `` | Parolası (kullanıcıyı gerektirir) |
| `OPENLOG_QUERY_MAX_MEMORY_USAGE` | `2147483648` | Her tenant sorgusunun `max_memory_usage` değeri (byte); `0` = openlog ayarlamaz (kullanıcının profili geçerli olur) |
| `OPENLOG_QUERY_MAX_ROWS_TO_READ` | `2000000000` | `max_rows_to_read` (her shard'da tablolardan okunan satır); `0` = ayarlanmaz |
| `OPENLOG_QUERY_MAX_BYTES_TO_READ` | `0` | `max_bytes_to_read` (sıkıştırılmamış); `0` = ayarlanmaz |
| `OPENLOG_QUERY_TENANT_LIMITS` | `` | Tenant başına ezmeler: `tenant:key=value[;key=value],tenant2:…`, anahtarlar `max_memory_usage`, `max_rows_to_read`, `max_bytes_to_read` (ayarlanmayanlar varsayılanları devralır), örneğin `acme:max_memory_usage=8589934592;max_rows_to_read=0` |

Her tenant sorgusu ayrıca `max_execution_time` (`OPENLOG_API_QUERY_TIMEOUT` / `OPENLOG_ALERT_QUERY_TIMEOUT`),
`log_comment` = `{"component":"api"|"alert","tenant_id":"<tenant>"}` (`system.query_log` içinde görünür) ve native
protokol `quota_key` = tenant id ayarlar, böylece okuma kullanıcısının profilindeki `<keyed/>`'lı bir ClickHouse kotası her
tenant'ı ayrı sayar. Ezmeler yalnızca ortam değişkeniyle yapılır (PostgreSQL'de henüz organizasyon ayarları deposu yok).

Hatalar: `max_memory_usage`, `max_rows_to_read` veya `max_bytes_to_read` tarafından durdurulan bir sorgu (ClickHouse kodları 241, 158,
307, 396) limiti adıyla söyleyen `422` `{"error":{"code":"resource_exhausted"}}` döner; kota (201), çok fazla eşzamanlı
sorgu (202) veya sunucunun toplam bellek limiti `Retry-After` ile `429 resource_exhausted` döner; `max_execution_time`
`504 timeout` olarak kalır. Alert değerlendirmeleri değerlendirme hatası olarak `query limit exceeded (<limit>): …` kaydeder.
Depolamadan veri okuyamayan bir sorgu (genellikle tiered storage'ın S3'e taşıdığı part'lar: kod 499
`S3_ERROR`, 86 `RECEIVED_ERROR_FROM_REMOTE_IO_SERVER` veya mesajında S3 geçen socket timeout / ağ / Poco istisnaları)
`Retry-After` ile `503` `{"error":{"code":"storage_unavailable","retryable":true}}` döner ve
`clickhouse storage error` olarak loglanır; alert değerlendirmeleri `cold storage unavailable: …` kaydeder.

Okuma kullanıcısının `readonly = 2` olan bir profile (yalnızca okuma; openlog yukarıdaki limitleri ayarlayabilmeli) ve
yalnızca `openlog.*` üzerinde `SELECT` iznine ihtiyacı vardır. Compose, `openlog_reader` kullanıcısını `deploy/compose/clickhouse/openlog-reader.xml`
dosyasından oluşturur (parola: clickhouse container'ının `OPENLOG_CLICKHOUSE_READ_PASSWORD` değeri); Helm chart'ı onu
ClickHouseInstallation içinde oluşturur (`clickhouse.readUser`). Harici ClickHouse, SQL ile yönetilen kullanıcılar:

```sql
CREATE SETTINGS PROFILE IF NOT EXISTS openlog_readonly ON CLUSTER openlog SETTINGS readonly = 2, allow_ddl = 0;
CREATE USER IF NOT EXISTS openlog_reader ON CLUSTER openlog IDENTIFIED WITH sha256_password BY '<password>'
    SETTINGS PROFILE 'openlog_readonly';
GRANT ON CLUSTER openlog SELECT ON openlog.* TO openlog_reader;
```

## `openlog-ingest` {#openlog-ingest}

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_INGEST_HTTP_ADDR` | `:4318` | OTLP/HTTP (`/v1/metrics`, `/v1/logs`, `/v1/traces`; protobuf ve JSON; gzip) |
| `OPENLOG_INGEST_GRPC_ADDR` | `:4317` | OTLP/gRPC |
| `OPENLOG_INGEST_MAX_BODY_BYTES` | `10485760` | **Açılmış** en büyük istek boyutu; daha büyüğü → `413` / `RESOURCE_EXHAUSTED` |
| `OPENLOG_INGEST_PRODUCE_TIMEOUT` | `10s` | Kafka produce ack timeout'u; timeout veya Kafka erişilemez → `Retry-After` ile `503` / `UNAVAILABLE` (D-014) |
| `OPENLOG_INGEST_CORS_ALLOWED_ORIGINS` | `` | Tarayıcıdan OTLP/HTTP gönderenler için CORS: virgülle ayrılmış `*`, tam origin'ler (`https://app.example.com`) veya subdomain joker karakterleri (`https://*.example.com`). Boş = kapalı. İzin verilen origin'lerden gelen preflight'lar (`OPTIONS`) `Access-Control-Allow-Origin`, istenen header'lar ve `Max-Age 7200` ile `204` alır; diğer origin'ler `403` alır. Credentials yok (kimlik doğrulama anahtar header'ıyla yapılır). |
| `OPENLOG_RUM_ENABLED` | `true` | `POST /v1/rum` (tarayıcı SDK'sı, [rum.md](/tr/contracts/rum), D-136) ve `/api/v1/rum/*` okumalarını sunar. Yalnızca `postgres` modu: tarayıcı anahtarları PostgreSQL'de durur. RUM'u sınırlayan diğer her şey (origin'ler, rate limit, örnekleme oranı) burada değil, tarayıcı anahtarı başına bir admin tarafından ayarlanır. Bunu kapatmak var olan anahtarları korur ama verilerini kabul etmeyi bırakır |
| `OPENLOG_RUM_GEO_HEADER` | `` | Güvenilir bir proxy'nin veya CDN'in ziyaretçinin ISO 3166-1 alpha-2 ülkesini yazdığı header (örneğin Cloudflare arkasında `CF-IPCountry`), RUM span'lerinde `geo.country.iso_code` olarak kaydedilir ([rum.md](/tr/contracts/rum) §3.7). Boş = ülke kaydedilmez. openlog kendisi adres çözümlemez ve **ziyaretçi adresini asla saklamaz**; header'ı yazan bir proxy yoksa alan boş kalır. İki harf olmayan değerler ve CDN yer tutucuları `XX` ile `T1` atılır |
| `OPENLOG_RUM_GEOIP_DB` | `` | MMDB biçiminde bir ülke veritabanının yolu (DB-IP Country Lite veya GeoLite2-Country); hiçbir header ülke bildirmediğinde kullanılır. Dosyayı sen sağlarsın, değiştirilen dosya bir dakika içinde alınır. **Yalnızca ülke tutulur, adres saklanmaz.** Okunamayan bir dosya loglanır ve veriye değil yalnızca ülkeye mal olur. Boş: hiçbir şey çözümlenmez ([rum.md](/tr/contracts/rum) §3.7) |
| `OPENLOG_RUM_TRUSTED_PROXIES` | `` | Ingest'in önündeki proxy'ler, virgülle ayrılmış CIDR aralıkları veya adresler olarak. `X-Forwarded-For` konusunda yalnızca bunlara inanılır, header sağdan okunur. Boş: header hiç okunmaz ve proxy arkasında her ziyaretçi proxy'nin adresine sahip olur |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_PROFILES` | `3` | Profiling örnekleri cold volume'a taşınmadan önceki gün sayısı. Profiller openlog'un sakladığı en geniş satırlardır, bu yüzden diğer sınıflardan daha erken taşınırlar |
| `OPENLOG_STORAGE_WARM_AFTER_DAYS_PROFILES` | – | Profiling örnekleri warm volume'a taşınmadan önceki gün sayısı (ayarlı değilse warm adımı yok) |
| `OPENLOG_SOURCE_MAPS_ENABLED` | `OPENLOG_RUM_ENABLED` | `/api/v1/source-maps` sunar ve tarayıcı stack'lerini yüklenen map'lerle minify'dan çıkarır ([rum.md](/tr/contracts/rum) §8). Kapalı: stack'ler minify'lı kalır, başka bir şey değişmez |
| `OPENLOG_SOURCE_MAPS_STORAGE` | `auto` | Map'lerin tutulduğu yer: `auto` (S3 URL'i biliniyorsa s3, yoksa local), `local` veya `s3` |
| `OPENLOG_SOURCE_MAPS_LOCAL_PATH` | `/tmp/openlog-sourcemaps` | Yerelde saklanan map'lerin dizini. Compose bunu `source-maps` volume'unda `/var/lib/openlog/sourcemaps` olarak ezer; başka yerlerde yeniden başlatmadan etkilenmeyen bir depolamaya koy, yoksa map'ler de gider |
| `OPENLOG_SOURCE_MAPS_S3_URL` | – | Bucket ve prefix dahil nesne base URL'i. Tiered storage açıkken boşsa `OPENLOG_S3_ENDPOINT`'ten türetilir (aynı bucket, prefix `openlog-sourcemaps/`) |
| `OPENLOG_SOURCE_MAPS_S3_REGION` | `OPENLOG_S3_REGION`, else `us-east-1` | O bucket'ın region'ı |
| `OPENLOG_SOURCE_MAPS_S3_ACCESS_KEY_ID` | `OPENLOG_S3_ACCESS_KEY_ID` | Statik key id; `_S3_CREDENTIALS=auto` ile ayarlanmaz |
| `OPENLOG_SOURCE_MAPS_S3_SECRET_ACCESS_KEY` | `OPENLOG_S3_SECRET_ACCESS_KEY` | Statik secret; `_S3_CREDENTIALS=auto` ile ayarlanmaz |
| `OPENLOG_SOURCE_MAPS_S3_CREDENTIALS` | `static` with keys, else `auto` | `static` (yukarıdaki anahtarlar) veya `auto` (AWS credential chain: env, web identity, ECS, IMDSv2; D-116) |
| `OPENLOG_SOURCE_MAPS_RETENTION_DAYS` | `90` | Bu süre boyunca kimsenin yüklemediği map'leri belge ve index satırıyla birlikte siler (`0` sonsuza kadar tutar). Dosya adlarını hash'leyen bir bundler her deploy'u yeni bir script yapar, yani bu olmadan bucket kurulumun ömrü boyunca büyür. Aynı adla yeniden yüklenen map asla bayatlamaz |
| `OPENLOG_AUTH_CACHE_TTL` | `60s` | `postgres` modu: çözülmüş bir lisans anahtarı bu süreden sonra PostgreSQL'e karşı yeniden kontrol edilir. **İptal edilen bir anahtar bir ingest pod'unda bu süre boyunca kabul edilmeye devam edebilir** (tarayıcı anahtarları da, [rum.md](/tr/contracts/rum) §3.5) |
| `OPENLOG_AUTH_NEGATIVE_CACHE_TTL` | `10s` | Bilinmeyen anahtarlar bu süreden sonra yeniden kontrol edilir (yeni oluşturulan bir anahtar, onu daha önce reddetmiş pod'larda bu gecikme içinde çalışmaya başlar) |
| `OPENLOG_AUTH_CACHE_MAX_STALE` | `15m` | PostgreSQL'e erişilemediği sürece, bu pencere içinde başarıyla çözülmüş anahtarlar kabul edilmeye devam eder (`0` = bayat kayıtlar asla kullanılmaz) |

`postgres` modu: ingest ayrıca agent sync için entegrasyon ayarı parolalarını çözmek üzere `OPENLOG_SECRETS_KEY` ve `OPENLOG_SECRETS_KEY_PREVIOUS` değerlerini de (bkz. `openlog-alert`)
okur ([releases-updates.md](/tr/contracts/releases-updates) §3). Api pod'larındakiyle aynı
değeri kullan; o olmadan parolalı ayarlar iletilmez (diğer sync'ler etkilenmez), geçersiz
bir değer ise servisi başlangıçta durdurur.

Lisans anahtarı önce `openlog-license-key` header'ından, sonra `x-api-key`'den (başka OTLP backend'leri için yapılandırılmış gönderenler için takma ad), sonra `Authorization: Bearer <key>`'den okunur (gRPC: aynı adlarla metadata). Bilinmeyen/eksik/iptal edilmiş anahtar → `401` / `UNAUTHENTICATED`.

**Lisans anahtarı cache'i (`postgres` modu).** Her ingest pod'u `sha256(key)` anahtarlı bir bellek içi cache tutar
(100 000 kayıtla sınırlı; bir anahtar için eşzamanlı miss'ler tek sorguyu paylaşır). Ingest başlangıçta PostgreSQL'i
beklemez ve PostgreSQL readiness kontrolü yoktur, bu yüzden bir PostgreSQL kesintisi ingest'i load balancer'dan
çıkarmaz: cache'teki anahtarlar `OPENLOG_AUTH_CACHE_MAX_STALE` boyunca çalışmaya devam eder (en fazla 5 sn'de bir yeniden denenir),
cache'te olmayan anahtarlar ise `401` yerine `Retry-After` ile `503` / `UNAVAILABLE` alır (ajanlar buffer'lar
ve yeniden dener, D-014). `last_used_at` asenkron yazılır, pod başına anahtar başına dakikada en fazla bir kez.
Metrik: `openlog_license_key_resolutions_total{result="hit|miss|negative|stale|unavailable"}`.

## `openlog-processor` {#openlog-processor}

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_PROCESSOR_GROUP` | `openlog-processor` | Kafka consumer group |
| `OPENLOG_PROCESSOR_BATCH_ROWS` | `50000` | Insert bloğu başına en fazla satır. Bir chunk'ın tek tablo için bunu aşan satırları deterministik bloklara bölünür (token son eki `#<i>/<batch_rows>`, bkz. [kafka.md](/tr/contracts/kafka)) |
| `OPENLOG_PROCESSOR_FLUSH_INTERVAL` | `2s` | Partition başına bir chunk'ı sınırlayan kayıt zaman damgası penceresinin uzunluğu. Yetişmiş bir partition'ın chunk'ı, penceresi başladıktan yaklaşık bunun `1.5 ×` katı sonra insert edilir (pencere + yarım pencere tolerans), yani processor'ın eklediği ingest'ten sorgulanabilir olmaya kadarki gecikme de budur |
| `OPENLOG_PROCESSOR_INSERT_TIMEOUT` | `30s` | Insert denemesi başına; kapanıştaki son flush'ı da sınırlar |
| `OPENLOG_PROCESSOR_BATCH_BYTES` | `8388608` | Partition başına bir chunk'ın kayıtları bu kadar (sıkıştırılmamış) byte'a ulaşınca chunk kapatılır |
| `OPENLOG_PROCESSOR_MAX_BUFFERED_BYTES` | `134217728` | Chunk'lar erken flush edilmeden önce tüm partition'larda buffer'lanan kayıt byte'ı (zamanlamaya bağlı kesim, bkz. kafka.md); `OPENLOG_PROCESSOR_BATCH_BYTES` değerinden ≥ olmalı. Processor belleği kabaca bu, artı `OPENLOG_PROCESSOR_INSERT_CONCURRENCY × BATCH_BYTES ×` ~10 (decode edilmiş satırlar), artı Kafka fetch buffer'ı (~26 MiB) kadardır |
| `OPENLOG_PROCESSOR_INSERT_CONCURRENCY` | `4` | Paralel decode edilip insert edilen chunk sayısı |
| `OPENLOG_PROCESSOR_INSERT_MODE` | `direct` | `direct`: her satırın shard'ındaki `*_local` tabloya insert (D-018; aşağıya bak). `distributed`: `distributed_foreground_insert=1` ile Distributed tablolara insert |
| `OPENLOG_PROCESSOR_TOPOLOGY_REFRESH` | `1m` | `direct` modu: `system.clusters` ne sıklıkla yeniden okunur; yeni shard/replica'lar bir sonraki yenilemeden sonra kullanılır |
| `OPENLOG_PROCESSOR_INSERT_QUORUM` | `0` | `direct` modu: yerel insert'ler için `insert_quorum` (`0` = kapalı). `2`, bir insert'in ancak shard'ın 2 replica'sında part varken başarılı olmasını sağlar; daha az replica erişilebilirken başarısız olur |

**`direct` insert modu.** Processor başlangıçta her Distributed tablonun kendi hesapladığı sharding key'i kullandığını kontrol eder (`cityHash64(tenant_id, host_id)`, span'ler için `cityHash64(tenant_id, trace_id)`; uyuşmazlık processor'ı durdurur, eksik tablolar beklenir), sonra `OPENLOG_CLICKHOUSE_CLUSTER`'ın shard, ağırlık ve replica'larını `OPENLOG_CLICKHOUSE_ADDR` üzerinden `system.clusters`'tan okur. Satırlar, tıpkı Distributed engine gibi `slot[cityHash64(...) % total_weight]` shard'ına, o shard'ın bir replica'sına gider (sırayla; başarısız replica 10 sn boyunca en son denenir ve sıradaki replica kullanılır). `system.clusters`'taki replica `host_name:port` değerine processor'dan erişilebilmelidir (Helm chart'ında ve compose stack'inde bu geçerlidir); 1 shard × 1 replica'lık bir cluster için bunun yerine `OPENLOG_CLICKHOUSE_ADDR` bağlantısı kullanılır. `OPENLOG_CLICKHOUSE_TLS_ENABLED` ile replica bağlantıları da TLS kullanır: `remote_servers` güvenli portu listelemeli (`<port>9440</port><secure>1</secure>`) ve her replica sertifikası kendi `host_name`'i için geçerli olmalıdır. Shard içi replikasyonu ReplicatedMergeTree yapar (`internal_replication` flag'i yalnızca Distributed insert'leri etkiler; `false` ise processor uyarı loglar). `OPENLOG_PROCESSOR_INSERT_QUORUM` olmadan bir insert'i tek replica onaylar, bu da `internal_replication=true` ile yapılan Distributed insert'le aynı dayanıklılıktır.

## `openlog-api` {#openlog-api}

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_API_HTTP_ADDR` | `:8080` | Bkz. [api.md](/tr/contracts/api) |
| `OPENLOG_API_QUERY_TIMEOUT` | `30s` | ClickHouse `max_execution_time` değerini ayarlar (tenant başına limitler: bkz. "ClickHouse salt okunur kullanıcı ve tenant başına sorgu limitleri") |
| `OPENLOG_API_MAX_ROWS` | `10000` | Liste endpoint'leri için üst sınır |
| `OPENLOG_API_UI_ENABLED` | `true` | Gömülü web arayüzünü `/` altında sunar (`/api` dışındaki yollar için SPA fallback) |
| `OPENLOG_COST_ENABLED` | `true` | Altyapı maliyeti endpoint'leri `/api/v1/costs/*` ile Costs görünümünü sunar ([cost.md](/tr/contracts/cost), D-134). Rakamlar statik bir fiyat tablosundan çıkan tahminlerdir, asla faturalama verisi değildir |
| `OPENLOG_COST_PRICES_FILE` | `` | Yerleşik fiyat tablosunun üzerine birleştirilen JSON dosyası (instance fiyatları, vCPU/GB başına yedek oranlar, region çarpanları), böylece bir fiyatı sürüm beklemeden düzeltebilirsin. Başlangıçta bir kez okunur; eksik, okunamayan veya geçersiz bir dosya, operatörün değiştirmek istediği rakamlarla sessizce fiyatlamak yerine api'yi durdurur. Biçim ve örnek: [cost.md](/tr/contracts/cost) §2 |
| `OPENLOG_INGEST_PUBLIC_URL` | `` | Arayüzün **Add data** kurulum komutlarında (`GET /api/v1/onboarding`) gösterilen ingest'in public OTLP/HTTP base URL'i, örneğin `https://ingest.openlog.example.com:4318`; path prefix'e izin verilir, credentials/query/fragment/tırnak/boşluk verilmez. Boş = `OPENLOG_PUBLIC_URL`'in şeması ve host'u, port `4318`; o da yoksa tarayıcının kullandığı host. Ingest'in kendi adı veya portu varsa ayarla (ters proxy, load balancer). Yalnızca gösterilen komutları etkiler |
| `OPENLOG_INGEST_PUBLIC_GRPC_URL` | `` | Aynı komutlar için public OTLP/gRPC endpoint'i, örneğin `https://ingest.openlog.example.com:4317`. Boş = `OPENLOG_INGEST_PUBLIC_URL`'in (veya onun yedeğinin) host'u, port `4317` |
| `OPENLOG_SESSION_TTL` | `168h` | Mutlak oturum ömrü (`postgres` modu) |
| `OPENLOG_SESSION_IDLE_TIMEOUT` | `24h` | Bu süre kullanılmayan oturum biter (`0` kapatır); aktivite dakikada en fazla bir kez kaydedilir |
| `OPENLOG_DEVICE_SESSION_TTL` | `2160h` | Bir cihazın bearer token olarak tuttuğu oturumun (mobil konsol) mutlak ömrü; 90 gün |
| `OPENLOG_DEVICE_SESSION_IDLE_TIMEOUT` | `720h` | Bu süre kullanılmayan cihaz oturumu biter (`0` kapatır); `OPENLOG_DEVICE_SESSION_TTL` değerini aşmamalı. Tarayıcı timeout'undan bilerek daha uzun: telefon alert geldiğinde açılır, bu da uzun bir sessiz dönemin ardından olur |
| `OPENLOG_COOKIE_SECURE` | `true` | Oturum cookie'sinin `Secure` özelliği. `false` değerini yalnızca düz HTTP geliştirme için ayarla (API uyarı loglar) |
| `OPENLOG_COOKIE_DOMAIN` | `` | Cookie `Domain`; boş = yalnızca host cookie'si (önerilen) |
| `OPENLOG_SIGNUP_ENABLED` | `false` | `POST /api/v1/auth/signup` endpoint'ini açar (self-service organizasyon oluşturma, SaaS) |
| `OPENLOG_LOGIN_MAX_FAILURES` | `10` | `429`'dan önce pencere içinde (e-posta, istemci IP'si) başına başarısız parola kontrolü |
| `OPENLOG_LOGIN_WINDOW` | `15m` | `OPENLOG_LOGIN_MAX_FAILURES` için kayan pencere (tüm api pod'ları PostgreSQL üzerinden paylaşır) |
| `OPENLOG_INVITATION_TTL` | `168h` | Davetin geçerlilik süresi |
| `OPENLOG_API_TRUSTED_PROXIES` | `` | İstemci IP'si için `X-Forwarded-For`'una güvenilen ters proxy'lerin CIDR/adresleri (rate limiting, oturumlar, audit log). Boş = TCP peer adresi kullanılır |
| `OPENLOG_SIGNUP_REQUIRE_VERIFICATION` | automatic | Kayıt olanlar lisans/API anahtarı oluşturmadan veya davet göndermeden önce e-posta adreslerini onaylamalıdır (D-045). Ayarlı değilse e-posta yapılandırıldığında (`OPENLOG_SMTP_HOST` ve `OPENLOG_PUBLIC_URL`) `true`, aksi halde `false`. Bunlar olmadan `true` bir yapılandırma hatasıdır |
| `OPENLOG_EMAIL_VERIFICATION_TTL` | `48h` | Doğrulama bağlantılarının geçerlilik süresi |
| `OPENLOG_SIGNUP_MAX_PER_IP` | `10` | `429`'dan önce pencere içinde istemci IP'si başına kayıt denemesi (girdi doğrulamasını geçen her deneme sayılır, başarılı olsun olmasın) |
| `OPENLOG_SIGNUP_MAX_PER_EMAIL` | `5` | Pencere içinde e-posta adresi başına kayıt denemesi |
| `OPENLOG_SIGNUP_RATE_WINDOW` | `1h` | İki kayıt limitinin penceresi (1s–24h; sayaçlar `login_failures` içinde durur, tüm api pod'ları paylaşır) |
| `OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS` | `` | Kayıtta reddedilen, virgülle ayrılmış e-posta domain'leri (subdomain'ler dahil), örneğin tek kullanımlık adres sağlayıcıları |
| `OPENLOG_SIGNUP_BLOCKED_EMAIL_DOMAINS_FILE` | `` | Satır başına bir engelli domain içeren dosya (`#` yorumları), yukarıdaki listeyle birleştirilir; başlangıçta okunur (örneğin mount edilmiş bir tek kullanımlık domain listesi) |
| `OPENLOG_SIGNUP_CAPTCHA_PROVIDER` | `` | `turnstile` (Cloudflare) veya `hcaptcha`: kayıt, sunucu tarafında doğrulanan bir CAPTCHA token'ı ister (`siteverify`, 10 sn timeout; sağlayıcı hataları → `503`) |
| `OPENLOG_SIGNUP_CAPTCHA_SECRET` | `` | Sağlayıcı secret key'i (sağlayıcı ayarlıysa zorunlu) |
| `OPENLOG_SIGNUP_CAPTCHA_SITE_KEY` | `` | Public site key, arayüz widget'ı için `GET /api/v1/auth/config` tarafından döndürülür (sağlayıcı ayarlıysa zorunlu) |
| `OPENLOG_SSO_ENABLED` | `true` | Single sign-on endpoint'leri, oturum politikası ve SCIM (postgres modu; D-077). SSO ayrıca `OPENLOG_PUBLIC_URL` ister (redirect URI, SAML entity ID/ACS URL, SCIM base URL); o olmadan ayarlar sayfası SSO'yu kullanılamaz olarak gösterir |
| `OPENLOG_SSO_SECRET_KEY` | `` | ≥ 32 byte. OIDC client secret'larını ve SAML SP private key'lerini şifreler (AES-256-GCM). Boş = anahtar `OPENLOG_KEY_HASH_SECRET`'tan türetilir; ikisi de yoksa şifrelenmeden saklanır (uyarı). Her api pod'unda aynı değer |
| `OPENLOG_SSO_SECRET_KEY_PREVIOUS` | `` | Önceki anahtar, rotasyon sırasında şifre çözmek için hâlâ kabul edilir (`OPENLOG_SSO_SECRET_KEY` gerektirir) |
| `OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS` | automatic | Identity provider URL'leri (OIDC issuer, SAML metadata) `http` kullanabilir ve loopback/private/link-local adreslere çözümlenebilir. Ayarlı değilse `OPENLOG_SIGNUP_ENABLED=true` olmadıkça `true` (o durumda organizasyon admin'leri operatör değildir: yalnızca https ve public adresler, bağlanma anında kontrol edilir) |
| `OPENLOG_SSO_LOGIN_TTL` | `10m` | "Sign in with SSO"dan callback'e kadarki süre (1m–1h) |
| `OPENLOG_SSO_CLOCK_SKEW` | `2m` | ID token `exp`/`iat` ve SAML zaman koşulları için tolere edilen IdP saat farkı (0–10m; süreç genelinde) |
| `OPENLOG_SSO_HTTP_TIMEOUT` | `10s` | Identity provider isteklerinin timeout'u: discovery, JWKS, token, UserInfo, SAML metadata (1s–1m; gövdeler ≤ 2 MiB) |
| `OPENLOG_SCIM_ENABLED` | `true` | `/api/scim/v2` altında SCIM 2.0 provisioning ve SCIM token yönetimi (D-078) |

**E-posta (davetler, adres doğrulama).** Api işlemsel e-postaları `openlog-alert`'in global SMTP sunucusu üzerinden gönderir
(`OPENLOG_SMTP_HOST`, `_PORT`, `_USERNAME`, `_PASSWORD`, `_FROM`, `_TLS`, `_INSECURE_SKIP_VERIFY`;
aşağıya bak), bağlantılar `OPENLOG_PUBLIC_URL`'e gider. İkisi de ayarlı olmalı; yoksa davetler kopyalanabilir bağlantı olarak paylaşılır
(arayüz bağlantıyı gösterir) ve kayıtlar doğrulanmaz. Gönderim senkrondur (15 sn timeout) ve saatte organizasyon başına 100,
davet edilen adres başına 5 davet e-postasıyla, kullanıcı başına 5 doğrulama e-postasıyla sınırlıdır.
E-postalar düz metin + HTML olarak İngilizce veya Türkçe gönderilir, dil isteğin `Accept-Language` değerinden seçilir (api.md
"Invitations" → "E-mail language"; şablonlar `internal/mail/templates` içinde).

**APM** ([apm.md](/tr/contracts/apm)):

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_APM_DEFAULT_APDEX_T` | `500ms` | Ayarı olmayan servislerin Apdex T değeri (1ms–10m) |
| `OPENLOG_APM_LINK_ENABLED` | `true` | Edge bağlama job'unu çalıştırır (trace'e bağlı servis haritası edge'leri). Api leader'ında çalışır (PostgreSQL advisory lock); `OPENLOG_AUTH_MODE=static` ile her api sürecinde |
| `OPENLOG_APM_LINK_INTERVAL` | `1m` | Çalıştırmalar arası süre (≥ 10s) |
| `OPENLOG_APM_LINK_LOOKBACK` | `10m` | Her çalıştırma `[now − lookback, now − delay)` aralığının tam dakikalarını yeniden hesaplar; daha geç gelen span'leri catch-up ve late-span re-link bağlar (1m–24h) |
| `OPENLOG_APM_LINK_DELAY` | `1m` | Bundan daha genç dakikalar bir sonraki çalıştırmaya bırakılır (0–1h); processor'ın ingest'ten sorgulanabilir olmaya kadarki gecikmesinin üstünde tut |
| `OPENLOG_APM_LINK_CATCHUP_ENABLED` | `true` | Önceki UTC gününü yeniden bağlayan günlük catch-up geçişi (lookback'ten daha geç gelen span'ler), [apm.md](/tr/contracts/apm) §6 |
| `OPENLOG_APM_LINK_CATCHUP_AT` | `03:00` | Geçişin başladığı `HH:MM` UTC (6 saate kadar geç başlayabilir, örneğin leader değişiminden sonra) |
| `OPENLOG_APM_LINK_CATCHUP_BATCH` | `1h` | Bir catch-up adımının penceresi (5m–6h); adımlar 10 sn araklarla tek tek çalışır. Aynı zamanda bir re-link geçişinin en uzun penceresi |
| `OPENLOG_APM_RELINK_ENABLED` | `true` | `openlog-processor`: geç gelen span'lerin istemci dakikalarını `apm_relink_queue` kuyruğuna ekler; api leader'ı: onları yeniden bağlar ([apm.md](/tr/contracts/apm) §6 "Late-span re-link", şema 0020) |
| `OPENLOG_APM_RELINK_AFTER` | `OPENLOG_APM_LINK_LOOKBACK` | `openlog-processor`: span'leri işlendiğinde bundan daha eski olan dakikalar kuyruğa eklenir (1m – `OPENLOG_APM_LINK_LOOKBACK`; api'nin lookback'inden ≤ tut) |
| `OPENLOG_APM_RELINK_MAX_AGE` | `168h` | Processor: bundan eski geç span'ler yalnızca sayılır; api: kuyruktaki bundan eski dakikalar yok sayılır (1h – 30 gün, ≤ `OPENLOG_APM_RETENTION_DAYS`) |
| `OPENLOG_APM_RELINK_INTERVAL` | `5m` | Api leader'ı: re-link geçişleri arası süre (10s–1h) |
| `OPENLOG_APM_RELINK_MAX_MINUTES` | `120` | Api leader'ı: geçiş başına yeniden bağlanan kuyruk dakikası, en eskiden başlayarak; gerisi bir sonraki geçişi bekler (1–1440) |
| `OPENLOG_APM_RETENTION_DAYS` | `30` | `openlog-migrate` / `openlog-allinone` (migration'lar): APM tablolarının TTL'i (1–3650). Değişen bir değer migration'lardan sonra `ALTER TABLE … ON CLUSTER … MODIFY TTL` ile uygulanır ([apm.md](/tr/contracts/apm) §8 "Retention"), APM tablolarının tiered storage taşımalarıyla birlikte (bkz. "Tiered storage") |

Job, `system.clusters`'tan shard başına bir replica'ya bağlanır (doğrudan processor insert'lerinde olduğu gibi, replica
`host_name:port` değerine api'den erişilebilmeli; TLS ayarları uygulanır). Metrikler: `openlog_apm_link_runs_total{result}`,
`openlog_apm_link_rows_total`, `openlog_apm_link_duration_seconds`, `openlog_apm_link_lag_seconds`; late-span re-link:
`openlog_apm_relink_runs_total{result}`, `openlog_apm_relink_minutes_total`, `openlog_apm_relink_backlog_minutes`,
`openlog_apm_relink_late_calls_total`, `openlog_apm_relink_duration_seconds` (api), `openlog_apm_relink_enqueued_total`,
`openlog_apm_relink_too_old_total` (processor).

Oturum cookie'si: `openlog_session`, `HttpOnly`, `SameSite=Strict`, `Path=/api`, `Secure` değeri
`OPENLOG_COOKIE_SECURE`'a göre. Api başlangıçta PostgreSQL'i bekler ve bir `postgres` readiness kontrolü ekler.

## `openlog-migrate` {#openlog-migrate}

Sırayla `migrations/postgres/*.sql` (`OPENLOG_AUTH_MODE=postgres` veya `OPENLOG_POSTGRES_DSN` ayarlıysa; PostgreSQL'i
bekler), sonra `schema/clickhouse/*.sql` (ikisi de binary'ye gömülü) uygular, ardından Kafka topic'lerini oluşturur.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_KAFKA_PARTITIONS` | `6` | Topic oluşturma için |
| `OPENLOG_KAFKA_REPLICATION_FACTOR` | `1` | Topic oluşturma için |
| `OPENLOG_KAFKA_MIN_INSYNC_REPLICAS` | `1` | Topic ayarı `min.insync.replicas`; 1 ile replication factor arasında olmalı |
| `OPENLOG_KAFKA_RETENTION_MS` | `86400000` | Topic ayarı `retention.ms`; `> 0` veya sınırsız için `-1` |
| `OPENLOG_KAFKA_MAX_MESSAGE_BYTES` | `12582912` | Topic ayarı `max.message.bytes`; `OPENLOG_INGEST_MAX_BODY_BYTES` + 2 MiB'den ≥ kalmalı. Ingest'in producer'ı ve processor'ın fetch boyutu M0'da 12 MiB'de sabittir, bu yüzden bunu onun altına indirmek ingest'in büyük isteklerde `503` dönmesine yol açabilir. |
| `OPENLOG_MIGRATE_SKIP_KAFKA` | `false` | Topic oluşturmayı atlar (topic'leri Strimzi yönetir) |

Her migration dosyası ilk satırında `-- openlog:phase expand|contract` bildirir (yoksa yükleme başarısız olur).
Expand migration'ları her zaman çalışır; bir contract migration'ı (`-- openlog:requires-all-at-least <version>`) yalnızca
bu binary ve son 5 dakikada görülen `component_heartbeats` içindeki her instance en az o sürümdeyse çalışır, aksi halde
atlanır (bir sonraki çalıştırma yeniden dener). Flag'ler: `-plan` canlı instance'ları ve neyin çalışacağını yazdırır, hiçbir şeyi
değiştirmez (topic'leri de); `-force-contract` engellenen contract migration'larını yine de uygular; `-version`.

Zaten var olan topic'lere dokunulmaz (partition sayısı asla otomatik değişmez; bkz. `docs/operations/scaling.md`).

### Tiered storage (openlog-migrate, openlog-allinone migration'ları; D-066, D-067) {#tiered-storage-openlog-migrate-openlog-allinone-migrations-d-066-d-067}

Telemetri tablolarının eski part'larını yerel diskten isteğe bağlı bir warm diske ve S3'e taşır
([tiered-storage.md](/tr/operations/tiered-storage)). Şema migration'larından sonra yönetilen tabloların tüm TTL'inin
sahibi migrate'tir (retention + taşımalar): her replica'nın storage policy'yi tanımladığını kontrol eder, taşıma alacak tablolar için
`ALTER TABLE … ON CLUSTER … MODIFY SETTING storage_policy = '<policy>', materialize_ttl_recalculate_only = 1` ve
değişen TTL'ler için `MODIFY TTL … TO VOLUME 'warm'|'cold', … <retention>` çalıştırır, uygulanan her TTL'i
`openlog.table_settings` içine kaydeder (`ttl:<table>`). Idempotent'tir; tiering hiç açılmadıysa ve
`OPENLOG_APM_RETENTION_DAYS` değişmediyse hiçbir şey çalışmaz. `-plan` bekleyen ALTER'ları yazdırır.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_STORAGE_TIERING_ENABLED` | `false` | Taşımaları ekler. `true`'dan sonra `false` taşıma cümlelerini yine kaldırır; tablolar policy'yi korur ve S3'teki part'lar orada okunabilir kalır. Bir replica'da policy veya volume'leri eksikse migrate `storage policy "<policy>" is not usable … <host>: …` ile başarısız olur |
| `OPENLOG_STORAGE_POLICY` | `openlog_tiered` | ClickHouse storage policy: `default` diskli `default` adında ilk volume (var olan tabloları geçirmek için zorunlu), `cold`, isteğe bağlı `warm` |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS` | `7` | Part'ların `cold` volume'una taşındığı gün sayısı: `metrics_local` (retention 30 g). `0` = taşıma yok; retention'dan ≥ değer yok sayılır (0–3650) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS_1M` | `30` | `metrics_1m_local` (retention 395 g) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_LOGS` | `3` | `logs_local` (14 g) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_TRACES` | `3` | `spans_local`, `trace_index_local` (7 g) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_APM` | `7` | `OPENLOG_APM_RETENTION_DAYS`'in 8 `apm_*` rollup tablosu |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_ALERTS` | `7` | `alert_evaluations_local` (30 g) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_RUM` | `7` | `rum_page_views_1m_local`, `rum_vitals_1m_local`, `rum_sessions_local` (30 g, D-136) |
| `OPENLOG_STORAGE_COLD_AFTER_DAYS_ANALYTICS` | `30` | `analytics_events_local` (90 g), `analytics_actors_local`, `analytics_actor_days_local` (395 g) ([analytics.md](/tr/contracts/analytics)) |
| `OPENLOG_STORAGE_WARM_AFTER_DAYS_<CLASS>` | `0` | Aynı sınıflar: part'ların `warm` volume'una taşındığı gün sayısı (ikisi de ayarlıysa cold yaşından küçük olmalı; her replica'da bir `warm` volume gerektirir) |

**ClickHouse sunucu değişkenleri** (openlog okumaz; ClickHouse container'ında `from_env` ile `deploy/compose/clickhouse/storage-tiered.xml` tarafından
kullanılır ve operators modunda Helm chart'ı tarafından `clickhouse.tieredStorage.s3` üzerinden ayarlanır):

| Değişken | Compose varsayılanı | Açıklama |
|---|---|---|
| `OPENLOG_CLICKHOUSE_STORAGE_CONFIG` | `./clickhouse/storage-none.xml` | Yalnızca Compose: `config.d/openlog-storage.xml` olarak mount edilen dosya; policy'yi `./clickhouse/storage-tiered.xml` tanımlar |
| `OPENLOG_CLICKHOUSE_STORAGE_WARM_CONFIG` | `./clickhouse/storage-none.xml` | Yalnızca Compose: `./clickhouse/storage-tiered-warm.xml` warm volume'u ekler (Docker volume `clickhouse-warm`) |
| `OPENLOG_CLICKHOUSE_S3_CREDENTIALS_FILE` | `./clickhouse/storage-none.xml` | Yalnızca Compose: anahtarları içeren XML (`storage-tiered-credentials.xml.example`), iki anahtar değişkenini ezer |
| `OPENLOG_S3_ENDPOINT` | `http://minio:9000/openlog-cold/ch-1/` | Sonunda prefix olan bucket URL'i, replica başına bir prefix (`{shard}`/`{replica}` makroları açılır) |
| `OPENLOG_S3_REGION` | `` | Region; boş = endpoint'ten |
| `OPENLOG_S3_ACCESS_KEY_ID` / `OPENLOG_S3_SECRET_ACCESS_KEY` | MinIO profile keys | Statik kimlik bilgileri; IAM rolleri için ikisini de boş bırak |
| `OPENLOG_S3_USE_ENVIRONMENT_CREDENTIALS` | `false` | `true`: AWS varsayılan credential chain (`AWS_*`, IRSA/web identity, instance profile) |
| `OPENLOG_S3_CACHE_MAX_SIZE` | `10Gi` | Cold okumalar için dosya sistemi cache'i (yerel disk) |
| `OPENLOG_S3_CACHE_PATH` / `OPENLOG_WARM_PATH` | `/var/lib/clickhouse/openlog_s3_cache/` / `/var/lib/clickhouse-warm/` | Cache dizini ve warm disk yolu (var olan bir dizinin bir seviye altında: image entrypoint'i eksik üst dizinleri root olarak oluşturur) |

`COMPOSE_PROFILES=tiered` test için `openlog-cold` bucket'lı yerel bir MinIO başlatır.

## `openlog-allinone` {#openlog-allinone}

Yukarıdaki tüm değişkenlerle ingest + processor + api'yi tek süreçte çalıştırır (tek `OPENLOG_SECRETS_KEY` api, ingest sync
ve alerting'e hizmet eder); tek bir ortak admin sunucusu vardır.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_MIGRATE_ON_START` | `true` | Başlamadan önce migration'ları çalıştırır (PostgreSQL + ClickHouse + topic'ler) |

## `openlog-admin` {#openlog-admin}

PostgreSQL tabanlı tenancy için komut satırı aracı; `OPENLOG_POSTGRES_*` değişkenlerini kullanır ve her komuttan önce bekleyen
PostgreSQL migration'larını uygular. Admin sunucusu başlatmaz.

| Komut | Ne yapar |
|---|---|
| `migrate` | Yalnızca `migrations/postgres` uygular |
| `bootstrap` | `OPENLOG_BOOTSTRAP_*` ile tarif edilen organizasyonu, owner'ı ve anahtarları idempotent olarak garantiler (`OPENLOG_BOOTSTRAP_OWNER_EMAIL` ve iki anahtar da boşsa değişiklik yapmadan 0 ile çıkar) |
| `create-owner --email E --org NAME [--tenant-id ID] [--name N] [--password-stdin] [--no-license-key]` | Self-hosted ilk çalıştırma: organizasyonu (tenant id verilmezse üretilir), owner'ı ve bir ingest lisans anahtarı oluşturur; üretilen parolayı ve anahtarı bir kez yazdırır |
| `reset-password --email E [--password-stdin]` | Yeni (üretilmiş veya stdin'den) bir parola ayarlar ve kullanıcının tüm oturumlarını iptal eder |
| `storage status [--json]` | Yalnızca ClickHouse (PostgreSQL yok, `OPENLOG_CLICKHOUSE_*` yazma kimlik bilgilerini ve `OPENLOG_STORAGE_*` kullanır): storage policy volume'leri, replica başına diskler, replica'lar üzerinden toplanmış volume başına yönetilen tablo byte/part/satır sayıları, move TTL'ini geçip hâlâ hot volume'da olan part'lar, çalışan taşımalar, son 24 saatin başarısız taşımaları (`system.part_log`), nedene göre detached part'lar ve openlog-migrate'in daha uygulayacağı TTL/policy ALTER'ları ([tiered-storage.md](/tr/operations/tiered-storage)) |

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_BOOTSTRAP_TENANT_ID` | `default` | Garantilenecek organizasyonun tenant id'si |
| `OPENLOG_BOOTSTRAP_ORG_NAME` | tenant id | Organizasyon oluşturulurken kullanılan ad |
| `OPENLOG_BOOTSTRAP_OWNER_EMAIL` | `` | Garantilenecek owner (yoksa oluşturulur, üye değilse owner olarak eklenir) |
| `OPENLOG_BOOTSTRAP_OWNER_PASSWORD` | `` | Yalnızca owner kullanıcı henüz yoksa zorunlu (≥ 12 karakter); var olan bir kullanıcının parolası asla değişmez |
| `OPENLOG_BOOTSTRAP_OWNER_NAME` | `` | Yeni owner için görünen ad |
| `OPENLOG_BOOTSTRAP_LICENSE_KEY` | `` | Garantilenecek düz metin ingest anahtarı: boşluk, tırnak veya ters bölü içermeyen 8–256 yazdırılabilir ASCII karakter (arayüzde/API'de özel değerle oluşturulan anahtarlar ≥ 16 ister). Üretilmiş anahtarları tercih et. Başka bir organizasyona aitse veya iptal edilmişse başarısız olur |
| `OPENLOG_BOOTSTRAP_API_KEY` | `` | Garantilenecek düz metin salt okunur API anahtarı (aynı kurallar) |

## `openlog-mcp` {#openlog-mcp}

AI araçları için salt okunur Model Context Protocol sunucusu ([mcp.md](/tr/operations/mcp), D-126). Sorgu API'sinin
bir istemcisidir, her araç çağrısı bir API anahtarıyla `/api/v1`'e yapılan bir istektir, bu yüzden ortak
ClickHouse, Kafka veya PostgreSQL değişkenlerinin hiçbirini okumaz ve onlara erişmesi gerekmez. `OPENLOG_ADMIN_ADDR` yalnızca
`http` transport'unda kullanılır. Bu değişkenleri `internal/config` değil `internal/mcp` okur ve sunucu
Compose stack'inin veya Helm chart'ının parçası değildir: AI aracının yanında (stdio) veya kendi
deployment'ı olarak (http) çalışır.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_MCP_TRANSPORT` | `stdio` | `stdio` (stdin/stdout üzerinden tek yerel istemci) veya `http` (`/mcp` altında streamable HTTP). `-transport` bunu ezer |
| `OPENLOG_MCP_HTTP_ADDR` | `:8092` | `http` transport'unun dinleme adresi (`-addr`). Düz HTTP sunar; TLS'i önünde sonlandır |
| `OPENLOG_MCP_API_URL` | `http://localhost:8080` | `openlog-api` base URL'i, örneğin `https://openlog.example.com`, `/api/v1` **olmadan**. Host'lu http(s), credentials, query veya fragment yok |
| `OPENLOG_MCP_API_KEY` | `` | Araç çağrılarının kimliğiyle çalıştığı API anahtarı (`ola_…`). `stdio` ile zorunlu; `http` ile yalnızca kendi `Authorization: Bearer`'ı olmayan istekler için yedektir (ikisi de olmayan istek → `401`). Ingest lisans anahtarları (`olk_…`) API anahtarı değildir |
| `OPENLOG_MCP_ORG_ID` | `` | Her çağrıyla gönderilen `X-Openlog-Org-Id`, birden çok organizasyondaki kullanıcıların anahtarları için; boş = anahtarın organizasyonu |
| `OPENLOG_MCP_TIMEOUT` | `60s` | Tek bir API isteğini sınırlar (bir araç çağrısı iki istek yapabilir) |
| `OPENLOG_MCP_MAX_ROWS` | `100` | Liste araçlarının `limit` argümanı için üst sınır (1–1000), böylece bir model bağlamına sınırsız sonuç çekemez |

Organizasyonun sorgu limitlerini, tenant kapsamını ve salt okunur ClickHouse kullanıcısını burası değil
`openlog-api` uygular: MCP sunucusu, API anahtarının erişemeyeceği hiçbir veriye erişemez.

## Fleet güncellemeleri (`openlog-ingest`, `openlog-api`) {#fleet-updates-openlog-ingest-openlog-api}

Agent sync, sürüm kataloğu ve rollout'lar ([releases-updates.md](/tr/contracts/releases-updates) §3–§4). Ingest,
`POST /v1/openlog/agent/sync` ve `GET /v1/openlog/releases/<version>/<name>` endpoint'lerini OTLP/HTTP portunda sunar (lisans anahtarıyla
kimlik doğrulama); api `/api/v1/fleet/*` sunar ve rollout controller'ı leader'da çalıştırır.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_RELEASE_INDEX_URL` | `https://github.com/onuragtas/openlog/releases/latest/download/index.json` | İmzalı sürüm index'i (güncelleme kontrolüyle ortak). Manifest'ler index kayıtlarının `manifest_url` adresinden (+`.sig`) alınır ve cache'lenir (değiştirilemez); index `If-None-Match` ile yeniden istenir |
| `OPENLOG_RELEASE_TRUSTED_KEYS_FILE` | `` | Ek sürüm public key'leri, satır başına bir base64 anahtar (güncelleme kontrolüyle ortak). **Derlenmiş anahtarlar ve bu dosya olmadan katalog kapalıdır**: sync asla güncelleme önermez ve fleet özeti `catalog.status = disabled` bildirir |
| `OPENLOG_RELEASE_MIRROR_DIR` | `` | Index URL'i **yerine** kullanılan yerel sürüm mirror'ı (air-gapped): `<dir>/index.json`, `<dir>/index.json.sig`, `<dir>/v<version>/manifest.json(.sig)` ve artifact'ler. Ingest listelenen artifact'leri buradan `/v1/openlog/releases/<version>/<name>` altında sunar (GET/HEAD, range istekleri; yalnızca doğrulanmış bir manifest'te listelenen dosya adları, boyut ve sha256 kontrollü; yollar dizinin dışına çıkamaz) |
| `OPENLOG_RELEASE_SERVE_MIRROR` | `false` | Sync yanıtları `download_url`'i sürüm URL'i yerine ingest'in mirror endpoint'ine yöneltir. `OPENLOG_RELEASE_MIRROR_DIR` gerektirir |
| `OPENLOG_RELEASE_MIRROR_BASE_URL` | `` | Mirror bağlantıları için ingest'in harici base URL'i (örneğin `https://ingest.example.com`); boş = sync isteğinin şeması (`X-Forwarded-Proto`/TLS) ve `Host`'u |
| `OPENLOG_RELEASE_CATALOG_REFRESH` | `15m` | Katalog yenileme periyodu (≥ 10s); başarısız olunca dakikada bir yeniden dener. Son doğrulanan katalog kullanımda kalır; daha eski `generated_at`'lı index reddedilir |
| `OPENLOG_FLEET_SYNC_INTERVAL` | `300s` | Ajanlara döndürülen `poll_interval_seconds` (1m–1h) |
| `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` | `60s` | Aktif bir rollout'un sonraki dalgasını bekleyen ajanlar için `poll_interval_seconds` (1m–1h, en fazla `OPENLOG_FLEET_SYNC_INTERVAL`), böylece "Deploy now" onlara hızlı ulaşır |
| `OPENLOG_FLEET_POLICY_CACHE_TTL` | `15s` | Ingest her organizasyonun politikasını, ezmelerini ve güncel rollout'unu bu kadar süre cache'ler (> 0, ≤ 30s) |
| `OPENLOG_FLEET_CONTROLLER_INTERVAL` | `30s` | Api leader'ında rollout controller periyodu (dalga ilerletme, durdurma, tamamlama, otomatik oluşturulan rollout'lar) |
| `OPENLOG_FLEET_HOST_STALE_AFTER` | `24h` | Bu süre sync yapmamış ajanlar rollout'larda ve özet sayılarında yok sayılır |
| `OPENLOG_FLEET_REPORT_QUEUE_SIZE` | `10000` | Ingest pod'u başına PostgreSQL için kuyruğa alınan sync raporları; dolunca en eskisi atılır |

Metrikler: ingest `openlog_agent_sync_requests_total{code}`, `openlog_agent_sync_decisions_total{reason}`,
`openlog_release_mirror_requests_total{code}`, `openlog_agent_sync_integrations_config_total{result}`, `openlog_fleet_policy_cache_total{result}`,
`openlog_fleet_host_reports_written_total{result}`, `openlog_fleet_host_reports_dropped_total`,
`openlog_fleet_host_reports_pending`; api (leader) `openlog_fleet_rollout_transitions_total{transition}`,
`openlog_agent_updates_total{result}`, `openlog_fleet_hosts{version}`; ikisi de
`openlog_release_catalog_refreshes_total{result}`, `openlog_release_catalog_releases`.
`OPENLOG_AUTH_MODE=static` ile sync güncellemesiz yanıt verir ve hiçbir şey saklanmaz.

## Sürümler, sürüm kontrolü ve güncellemeler {#versions-release-check-and-updates}

Her binary ürün sürümünü bildirir (docs/contracts/releases-updates.md §1, §5): `-version`, `/readyz` gövdesinde `"version"`,
her ingest/api/admin HTTP yanıtında `X-Openlog-Version` ve `x-openlog-version` gRPC header
metadata'sı. `OPENLOG_POSTGRES_DSN` ayarlı uzun süre çalışan servisler (ingest, processor, api, allinone)
`component_heartbeats`'i her 30 sn'de bir upsert eder (tek bağlantılı kendi havuzu; PostgreSQL kesintileri tolere edilir ve bir kez loglanır) ve
düzgün kapanışta kendi satırını siler; `openlog-migrate` contract migration'larını kapılamak için bu satırları kullanır.

`openlog-api` (yalnızca leader pod, PostgreSQL advisory lock):

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_UPDATE_CHECK` | `enabled` | `enabled` veya `disabled`. İmzalı sürüm index'ini her `OPENLOG_UPDATE_CHECK_INTERVAL`'da bir kontrol eder (başarısızlıktan sonra: 6 saat veya interval, hangisi kısaysa) ve en yeni sürümü `GET /api/v1/version` için PostgreSQL'e (`system_state`) yazar. `static` auth modunda etkisi yok |
| `OPENLOG_UPDATE_CHECK_INTERVAL` | `24h` | Başarılı sürüm kontrolleri arası süre (≥ 1m). Yayın yaptığın yerel bir mirror için (testler, demolar) kısa değerler işe yarar |
| `OPENLOG_RELEASE_INDEX_URL` | `https://github.com/onuragtas/openlog/releases/latest/download/index.json` | Index (veya mirror); imza `<url>.sig` içinde |
| `OPENLOG_UPDATE_CHANNEL` | `stable` | `stable` veya `beta` (beta stable sürümleri de görür) |
| `OPENLOG_RELEASE_TRUSTED_KEYS_FILE` | `` | Ek güvenilen Ed25519 public key'leri (satır başına bir base64 anahtar, `#` yorumları). Derlenmiş anahtarı ve bu dosyası olmayan build'ler `update_check: failed` bildirir |

`openlog-updater` (`updater` profilinde Compose servisi veya `-k8s -once` ile Helm CronJob) ayrıca şunları okur:
`OPENLOG_UPDATER_MODE` (`off`, `notify` (varsayılan), `auto`), `OPENLOG_UPDATER_INTERVAL` (`1h`),
`OPENLOG_UPDATER_REQUEST_POLL` (`10s`, `update_requests` içindeki arayüz güncelleme istekleri; yalnızca Compose),
`OPENLOG_UPDATER_MAINTENANCE_WINDOW` (UTC, örneğin `sat,sun 02:00-05:00; mon-fri 03:00-04:00`; boş = her zaman),
`OPENLOG_UPDATER_HEALTH_TIMEOUT` (`5m`), `OPENLOG_UPDATER_IMAGE_REPOSITORY` (mirror repository, digest korunur),
Compose: `OPENLOG_UPDATER_SERVICES` (`openlog`), `OPENLOG_UPDATER_HEALTH_URLS` (`http://openlog:9464/readyz`),
`OPENLOG_UPDATER_POSTGRES_SERVICE` (`postgres`), `OPENLOG_UPDATER_PGDUMP_USER` / `_DATABASE` (`openlog`),
`OPENLOG_UPDATER_BACKUP_DIR` (`/backups`), `OPENLOG_UPDATER_BACKUP_KEEP` (`5`), `OPENLOG_UPDATER_ENV_FILE`,
`OPENLOG_UPDATER_COMPOSE_DIR` (env dosyasının dizini), `OPENLOG_UPDATER_COMPOSE_SYNC` (`auto` | `off`),
`OPENLOG_UPDATER_SELF_UPDATE` (`auto`: güncellemeden sonra install-server.sh kurulumları updater container'ını
kurulu image ile değiştirir; `on`: her Compose kurulumu; `off`: yalnızca bildirim; D-120),
`OPENLOG_UPDATER_COMPOSE_PROJECT` (algılanır), `DOCKER_HOST` (`unix:///var/run/docker.sock`);
Kubernetes: `OPENLOG_UPDATER_K8S_DEPLOYMENTS`, `OPENLOG_UPDATER_K8S_MIGRATE_TEMPLATE`, `OPENLOG_UPDATER_VERSION_URL`,
`OPENLOG_UPDATER_ROLLOUT_TIMEOUT` (`15m`), `OPENLOG_UPDATER_MIGRATE_TIMEOUT` (`30m`), artı yukarıdaki sürüm değişkenleri
ve durum ile audit olayları için `OPENLOG_POSTGRES_*`. Ayrıntılar: [upgrading.md](/tr/operations/upgrading).

## `openlog-alert` {#openlog-alert}

Alert kuralı değerlendirme ve bildirim teslimi ([alerting.md](/tr/contracts/alerting)). İstediğin kadar replica çalıştır (kurallar
PostgreSQL lease'leriyle paylaşılır) veya `openlog-allinone`'a çalıştırt (`OPENLOG_ALERT_ENABLED`).
`OPENLOG_AUTH_MODE=postgres` gerektirir; ortak ClickHouse ve PostgreSQL değişkenlerini kullanır. `openlog-api` secret, public URL, SMTP ve limit
değişkenlerini de okur (kanal şifreleme, test gönderimleri, önizlemeler); `openlog-ingest` secret değişkenlerini okur
(agent sync'teki entegrasyon ayarı parolaları).

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_ALERT_ENABLED` | `true` | Yalnızca `openlog-allinone`: evaluator ve dispatcher'ı süreç içinde çalıştırır |
| `OPENLOG_SECRETS_KEY` | `` | Kanal secret'ları ve entegrasyon ayarı parolaları için Base64 32 byte AES-256-GCM anahtarı (`openssl rand -base64 32`). O olmadan kanallar kaydedilemez, test edilemez, teslim edilemez ve entegrasyon ayarları parola saklayamaz. Her api, alert ve ingest pod'unda aynı değer |
| `OPENLOG_SECRETS_KEY_PREVIOUS` | `` | Rotasyon sırasında şifre çözmek için hâlâ kabul edilen, virgülle ayrılmış eski anahtarlar (sonra `openlog-alert rotate-secrets` çalıştır) |
| `OPENLOG_PUBLIC_URL` | `` | Bildirimlerdeki bağlantılar için web arayüzü base URL'i (`https://openlog.example.com`); boş = bağlantı yok |
| `OPENLOG_ALERT_LEASE_TTL` | `30s` | Kural lease ömrü (≥ 5s): ölü bir pod'un kuralları bu süreden sonra taşınır |
| `OPENLOG_ALERT_LEASE_RENEW_INTERVAL` | `10s` | Yenileme/yeniden dengeleme periyodu (≤ TTL/2) |
| `OPENLOG_ALERT_EVALUATION_DELAY` | `15s` | Pencereler şu andan bu kadar önce biter (ingest'ten sorgulanabilir olmaya kadarki gecikme) |
| `OPENLOG_ALERT_MAX_CONCURRENT_EVALUATIONS` | `16` | Pod başına |
| `OPENLOG_ALERT_TENANT_MAX_CONCURRENT` | `4` | Pod başına organizasyon başına |
| `OPENLOG_ALERT_TENANT_EVALUATIONS_PER_MINUTE` | `600` | Pod başına organizasyon başına token bucket; bütçe aşılırsa = `throttled`, yeniden denenir |
| `OPENLOG_ALERT_MAX_SERIES_PER_RULE` | `1000` | Değerlendirme başına seri (fazlası → değerlendirme hatası) |
| `OPENLOG_ALERT_QUERY_TIMEOUT` | `20s` | Değerlendirme ve önizleme sorgularının ClickHouse `max_execution_time` değeri |
| `OPENLOG_ALERT_MAX_RULES_PER_ORG` | `1000` | api: organizasyon başına kural |
| `OPENLOG_ALERT_EVALUATION_HISTORY` | `true` | Değerlendirme özetlerini ClickHouse `alert_evaluations`'a yazar (doğrudan shard insert'leri, 5 sn'de bir toplu; [alerting.md](/tr/contracts/alerting) §3.6). Doğrudan processor insert'lerinde olduğu gibi `system.clusters`'taki replica `host_name:port` değerine erişilebilmeli |
| `OPENLOG_ALERT_DISPATCH_WORKERS` | `4` | Pod başına eşzamanlı teslim |
| `OPENLOG_ALERT_DELIVERY_TIMEOUT` | `10s` | HTTP/SMTP teslim denemesi başına |
| `OPENLOG_ALERT_DELIVERY_MAX_ATTEMPTS` | `10` | Sonra bildirim `failed` olur (24 saatten sonra da) |
| `OPENLOG_ALERT_BLOCK_PRIVATE_DESTINATIONS` | `false` | Loopback, private, link-local ve CGNAT adreslere webhook/Slack/Teams/SMTP bağlantılarını reddeder (DNS çözümlemesinden sonra kontrol edilir). **SaaS için `true` ayarla** |
| `OPENLOG_SMTP_HOST` / `OPENLOG_SMTP_PORT` | `` / `587` | Kendi sunucusu olmayan e-posta kanalları için global SMTP sunucusu |
| `OPENLOG_SMTP_USERNAME` / `OPENLOG_SMTP_PASSWORD` | `` | PLAIN auth, yalnızca TLS üzerinden |
| `OPENLOG_SMTP_FROM` | `` | Host ayarlıysa zorunlu, örneğin `openlog <alerts@example.com>` |
| `OPENLOG_SMTP_TLS` | `starttls` | `starttls` (zorunlu), `tls` (implicit, port 465) veya `none` (kimlik bilgisine izin yok) |
| `OPENLOG_SMTP_INSECURE_SKIP_VERIFY` | `false` | **Yalnızca test için** |

Komutlar: `openlog-alert rotate-secrets [-check]` (kanal secret'larını güncel anahtarla yeniden şifreler; kanallar hâlâ başka bir anahtar kullanıyorsa
`-check` 3 ile çıkar). Readiness kontrolleri: `postgres`, `clickhouse`. Metrikler: alerting.md §8. SIGTERM'de
çalışan değerlendirmeler ve teslimler biter, sonra pod lease'lerini bırakır (grace period ≥ sorgu timeout'u + teslim
timeout'u). `openlog-alert` kendini `component_heartbeats`'e kaydeder.

## Tail sampling (`openlog-sampler`; flag ingest, processor, api, allinone'da da; D-075) {#tail-sampling-openlog-sampler-flag-also-on-ingest-processor-api-allinone-d-075}

Varsayılan olarak kapalıdır; kapalıyken davranış değişmez. `OPENLOG_TAILSAMPLING_ENABLED` ingest (trace id başına bir traces kaydı), processor (`<prefix>.otlp.traces.sampled.v1` okur), `openlog-sampler` ve allinone'da (sampler'ı süreç içinde çalıştırır) aynı değere sahip olmalı. Api yalnızca bildirir. İşletim: docs/operations/tail-sampling.md.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `OPENLOG_TAILSAMPLING_ENABLED` | `false` | Tail sampling aşamasını açar |
| `OPENLOG_TAILSAMPLING_GROUP` | `openlog-sampler` | Sampler'ın consumer group'u |
| `OPENLOG_TAILSAMPLING_DECISION_WAIT` | `30s` | Bir trace'in ilk span'inden kararına kadarki süre (1s–10m) |
| `OPENLOG_TAILSAMPLING_MAX_TRACES` | `100000` | Instance başına buffer'lanan trace; dolunca en eskisi erken karara bağlanır |
| `OPENLOG_TAILSAMPLING_MAX_SPANS_PER_TRACE` | `2000` | Buna ulaşan trace erken karara bağlanır; sonraki span'ler kararı izler |
| `OPENLOG_TAILSAMPLING_MAX_BUFFERED_BYTES` | `536870912` | Instance başına buffer'lanan protobuf span byte'ı (≥ 1 MiB); aşılınca en eski trace'ler erken karara bağlanır |
| `OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL` | `10m` | Kararların geç span'ler için ne kadar hatırlanacağı (≥ decision wait, ≤ 24h) |
| `OPENLOG_TAILSAMPLING_DECISION_CACHE_SIZE` | `500000` | Instance başına hatırlanan karar |
| `OPENLOG_TAILSAMPLING_POLICY_REFRESH` | `30s` | `tail_sampling_policies` yeniden yükleme aralığı (postgres auth modu; 1s–1h) |
| `OPENLOG_TAILSAMPLING_PRODUCE_TIMEOUT` | `10s` | Sampled topic'e deneme başına produce timeout'u (yeniden denenir) |
| `OPENLOG_TAILSAMPLING_DEFAULT_POLICY` | empty | Saklı politikası olmayan tenant'lar için JSON politika (apm.md §4.2); boş her şeyi tutar. Static auth modu yalnızca bunu kullanır |

## Kullanım, planlar ve faturalama {#usage-plans-and-billing}

Kullanım ölçümü her zaman açıktır (şema `0050_usage`, processor muhasebesi). Planlar, kotalar ve faturalama: [usage.md](/tr/contracts/usage),
operatör kılavuzu [docs/operations/saas.md](/tr/operations/saas), D-079–D-081. Self-hosted varsayılanları hiçbir şeyi zorlamaz.

| Değişken | Varsayılan | Servisler | Açıklama |
|---|---|---|---|
| `OPENLOG_SAAS_MODE` | `false` | ingest, api, migrate, allinone | Sert uygulama: aylık kotayı ve tenant başına rate limit'leri aşınca ingest `429`; tenant başına retention varsayılanı. `OPENLOG_AUTH_MODE=postgres` gerektirir |
| `OPENLOG_PLANS` | empty | api, migrate, allinone | Plan kataloğu JSON'u (usage.md §4.1). Boş (ve dosya yoksa): tek `unlimited` plan |
| `OPENLOG_PLANS_FILE` | empty | api, migrate, allinone | Plan kataloğunu içeren dosya; `OPENLOG_PLANS` ile birlikte kullanılamaz |
| `OPENLOG_DEFAULT_PLAN` | catalog `default`, else first plan | api, migrate | Ataması olmayan organizasyonların planı |
| `OPENLOG_SUPERADMIN_EMAILS` | empty | api | Herhangi bir organizasyona plan atayabilen SaaS operatörlerinin virgülle ayrılmış e-posta adresleri |
| `OPENLOG_USAGE_EVALUATION_INTERVAL` | `1m` | api | Leader'ın kota değerlendirme aralığı (10s–1h) |
| `OPENLOG_USAGE_QUERY_COLLECTION_ENABLED` | `true` | api | Sorgu hesaplamasını `system.query_log`'dan `usage_queries_1h` içine toplar (leader, 15 dakikada bir) |
| `OPENLOG_USAGE_NOTIFY_THRESHOLDS` | `80,100` | api | Owner'lara e-posta gönderen yüzdeler (fatura dönemi başına bir kez); 100'ün altındaki en düşük değer uyarı seviyesidir |
| `OPENLOG_QUOTA_REFRESH_INTERVAL` | `30s` | ingest | `tenant_quota_status` yeniden yükleme aralığı (1s–10m) |
| `OPENLOG_QUOTA_INGEST_PODS` | `0` | ingest | Tenant rate limit'lerini pod'lara böler; `0` = `component_heartbeats`'ten canlı ingest/allinone instance'ları |
| `OPENLOG_QUOTA_BLOCKED_RETRY_AFTER` | `5m` | ingest | Aylık kotayı aşan isteklerin `Retry-After` değeri (1s–24h) |
| `OPENLOG_QUOTA_RETENTION_ENABLED` | `OPENLOG_SAAS_MODE` | api, migrate, allinone | Tenant başına retention: migrate ham tablo TTL'lerini en uzun plan retention'ına genişletir, api leader'ı daha kısa retention'lı tenant'ların eski verisini siler (D-081) |
| `OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS` | `20` | api | Saatlik çalıştırma başına gönderilen tenant retention mutation'ı (1–1000) |
| `OPENLOG_BILLING_PROVIDER` | `none` | api | `none` veya `noop` (henüz gerçek sağlayıcı yok; saas.md §6) |
| `OPENLOG_BILLING_PUSH_AT` | `02:00` | api | Günlük kullanım gönderim saati (HH:MM UTC) |

Owner e-postaları `OPENLOG_SMTP_*` kullanır ve `OPENLOG_PUBLIC_URL`'e bağlantı verir.

### SaaS işletimi (operatör konsolu, yaşam döngüsü, sert limitler, kötüye kullanım tespiti; D-105, D-106) {#saas-operations-operator-console-lifecycle-hard-limits-abuse-detection-d-105-d-106}

Operatör konsolu (`/operator`, `OPENLOG_SUPERADMIN_EMAILS`) her postgres modu kurulumunda çalışır. Askıya alma
uygulaması, sert host/kullanıcı limitleri, deneme süreleri ve kötüye kullanım tespiti yalnızca `OPENLOG_SAAS_MODE=true` ile çalışır
([saas.md](/tr/operations/saas) §8–§11). Ingest askıya alma ve host limitleri için `OPENLOG_QUOTA_REFRESH_INTERVAL` değerini yeniden kullanır.

| Değişken | Varsayılan | Servisler | Açıklama |
|---|---|---|---|
| `OPENLOG_SAAS_HOST_SYNC_INTERVAL` | `1m` | api | Leader'ın ingest için host limitlerini ve aktif host id'lerini ne sıklıkla yazdığı (10s–1h) |
| `OPENLOG_SAAS_SIGNUP_TRIAL_PLAN` | empty | api | Yeni kayıt organizasyonlarının deneme süresini aldığı plan (`trial_days` ile); boş = otomatik deneme yok |
| `OPENLOG_SAAS_TRIAL_NOTIFY_DAYS` | `7,3,1` | api | Deneme bitmeden owner'lara e-posta gönderilen günler (her biri 1–90) |
| `OPENLOG_SAAS_LIFECYCLE_INTERVAL` | `5m` | api | Deneme job'u aralığı: kayıt denemeleri, hatırlatma e-postaları, sonunda yedek plan (10s–1h) |
| `OPENLOG_SAAS_SUPPORT_SESSION_TTL` | `2h` | api | Tek bir operatör destek görünümünün en uzun süresi (5m–24h; owner'ın verdiği iznin ötesine asla geçmez) |
| `OPENLOG_SAAS_AUTO_SUSPEND` | `false` | api | Kötüye kullanım dedektörü yeni bir işaret çıkardığında organizasyonu otomatik askıya alır |
| `OPENLOG_SAAS_ABUSE_INTERVAL` | `10m` | api | Kötüye kullanım dedektörü aralığı (1m–24h) |
| `OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER` | `10` | api | Planın saatlik payının (`ingest_gb_month / 730`) bu katını aşan bir saatlik ingest'i işaretler; `0` kapatır |
| `OPENLOG_SAAS_ABUSE_NEW_ORG_DAYS` | `7` | api | Organizasyonun yeni sayıldığı yaş sınırı (1–365) |
| `OPENLOG_SAAS_ABUSE_NEW_ORG_HOSTS` | `50` | api | Bundan fazla aktif host'u olan yeni organizasyonları işaretler; `0` kapatır |
| `OPENLOG_SAAS_ABUSE_SOURCE_IPS` | `200` | api | Lisans anahtarları bir saat içinde bundan fazla farklı istemci adresinden kullanılan tenant'ları işaretler (ingest instance'ı başına); `0` kapatır |

### Veri sahibi talepleri ve durum sayfası (api, allinone; D-107, D-108) {#data-subject-requests-and-status-page-api-allinone-d-107-d-108}

Dışa aktarmalar, hesap silme ve organizasyon silme ([saas.md](/tr/operations/saas) §12) her postgres modu
kurulumunda vardır; dışa aktarma kuyruğunu ve kalıcı silme job'unu api leader'ı çalıştırır. E-postalar `OPENLOG_SMTP_*`, bağlantılar
`OPENLOG_PUBLIC_URL` kullanır. Tiered storage açıkken (`OPENLOG_STORAGE_TIERING_ENABLED=true`) ve
`OPENLOG_DATA_EXPORT_S3_URL` yoksa dışa aktarma `OPENLOG_S3_ENDPOINT` bucket'ını (prefix `openlog-exports/`) ve,
kendi anahtarları yoksa, `OPENLOG_S3_REGION`, `OPENLOG_S3_ACCESS_KEY_ID` ve
`OPENLOG_S3_SECRET_ACCESS_KEY` değerlerini yeniden kullanır. Statik anahtar yoksa dışa aktarma istemcisi IAM kimlik bilgilerini kullanır (D-116): sürecin
ortamındaki standart `AWS_*` değişkenleri, EKS IRSA (`AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN`), ECS task rolleri
ve EKS Pod Identity (`AWS_CONTAINER_CREDENTIALS_*`) ve EC2 instance profile (IMDSv2).

| Değişken | Varsayılan | Servisler | Açıklama |
|---|---|---|---|
| `OPENLOG_ORG_DELETION_GRACE` | `168h` | api | Organizasyon silmenin, owner'ın iptal edebileceği bekleme süresi (0–2160h); operatörler hemen silebilir |
| `OPENLOG_DATA_EXPORT_ENABLED` | `true` | api | Organizasyon ve kişisel veri dışa aktarmalarını sunar |
| `OPENLOG_DATA_EXPORT_STORAGE` | `auto` | api | `auto` (S3 URL'i biliniyorsa S3, yoksa local), `local` veya `s3`. Birden fazla api pod'u varsa S3 kullan: arşivi leader yazar, indirmeyi herhangi bir pod sunar |
| `OPENLOG_DATA_EXPORT_LOCAL_PATH` | `/tmp/openlog-exports` | api | Yerel arşivlerin ve bir dışa aktarma oluşturulurken geçici arşiv dosyasının mutlak dizini (S3 ile de). Compose: `/var/lib/openlog/exports`, `data-exports` volume'u (image onu uid 10001'e ait olarak oluşturur); Helm: pod'un `emptyDir`'inde `/tmp/openlog-exports` (birden fazla api replica'sı varsa S3 kullan) |
| `OPENLOG_DATA_EXPORT_S3_URL` | empty | api | Bucket ve prefix içeren, `/` ile biten nesne base URL'i (path-style `https://s3.example.com/bucket/exports/` veya virtual-hosted `https://bucket.s3.eu-west-1.amazonaws.com/exports/`) |
| `OPENLOG_DATA_EXPORT_S3_REGION` | `OPENLOG_S3_REGION`, else `us-east-1` | api | İmzalama region'ı (AWS Signature Version 4) |
| `OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID` / `OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY` | `OPENLOG_S3_ACCESS_KEY_ID` / `OPENLOG_S3_SECRET_ACCESS_KEY` | api | `OPENLOG_DATA_EXPORT_S3_CREDENTIALS=static` için statik kimlik bilgileri (birlikte ayarla) |
| `OPENLOG_DATA_EXPORT_S3_CREDENTIALS` | `static` when static keys are set (own or `OPENLOG_S3_*`), else `auto` | api | `static`: yukarıdaki anahtarlar (zorunlu). `auto`: SDK'sız AWS credential chain, ilk bulunan kazanır: ortam (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`) → web identity (`AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_ROLE_ARN`, `AWS_ROLE_SESSION_NAME`; STS endpoint'i `AWS_REGION`/`AWS_DEFAULT_REGION`'dan, yoksa imzalama region'ından ve `AWS_STS_REGIONAL_ENDPOINTS`'ten) → container (`AWS_CONTAINER_CREDENTIALS_RELATIVE_URI` veya https ya da loopback/ECS/EKS link-local host'lu `AWS_CONTAINER_CREDENTIALS_FULL_URI`, `AWS_CONTAINER_AUTHORIZATION_TOKEN(_FILE)` ile) → IMDSv2 üzerinden EC2 instance profile (`AWS_EC2_METADATA_DISABLED=true` atlar). Geçici kimlik bilgileri süreleri dolmadan 5 dakika önce yenilenir ve `X-Amz-Security-Token` ile gönderilir; hiçbir kaynakta kimlik bilgisi yoksa istekler imzasız gider (eskisi gibi). Açıkça `auto`, `OPENLOG_S3_*` anahtarlarını ödünç almaz ve `OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID`/`_SECRET_ACCESS_KEY` değerlerini reddeder. Ortak config/credentials dosyaları, SSO ve process credentials desteklenmez (D-116) |
| `OPENLOG_DATA_EXPORT_TTL` | `168h` | api | Bir arşivin ve e-postayla gönderilen indirme bağlantısının ne kadar kullanılabilir kalacağı (1h–720h); süresi dolan arşivler silinir |
| `OPENLOG_DATA_EXPORT_MAX_BYTES` | `4294967296` | api | Arşiv boyutu limiti (1 MiB–5 GiB, tek S3 PUT); telemetri ulaşılmadan önce durur (`truncated`) |
| `OPENLOG_DATA_EXPORT_MAX_ROWS` | `50000000` | api | Dışa aktarma başına telemetri satırı (≥ 1000) |
| `OPENLOG_DATA_EXPORT_MAX_RANGE` | `744h` | api | Bir dışa aktarmanın en uzun telemetri zaman aralığı (1h–9600h) |
| `OPENLOG_DATA_EXPORT_ROWS_PER_SECOND` | `200000` | api | ClickHouse'tan telemetri okumalarının kısıtı; `0` = kısıtsız (sorgular ayrıca `max_threads=2` ile çalışır) |
| `OPENLOG_STATUS_PAGE_ENABLED` | `OPENLOG_SAAS_MODE` | api | Public durum sayfası: `/status`, `GET /api/v1/status` ve leader'ın dakikalık öz kontrolleri |

### Sentetik izleme (api, allinone; D-132) {#synthetic-monitoring-api-allinone-d-132}

Zamanlanmış, dışarıdan içeriye HTTP kontrolleri ([api.md](/tr/contracts/api#synthetic-monitoring)). Tanımlar organizasyon başına
PostgreSQL'de durur (`0090_synthetics`); zamanı gelenleri api **leader**'ı çalıştırır, yani aşağıdaki limitler cluster'ı değil tek
süreci sınırlar. Her çalıştırma ClickHouse'ta saklanır (`synthetic_runs`, 30 gün) ve metrik alert kuralları ile dashboard'ların diğer metrikler gibi
kullandığı `synthetics.check.success` ve `synthetics.check.duration` gauge'ları olarak yansıtılır. Yalnızca postgres auth modu.

Bir kontrol, sunucunun bir organizasyon **üyesi** adına yaptığı istektir, bu yüzden çözümlendiği adres
her bağlantıda kontrol edilir (DNS çözümlemesinden sonra, yani rebinding yanıtı da yakalanır) ve yönlendirmeler sınırlanıp
yeniden doğrulanır: `OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS` olmadan yalnızca public adreslere erişilebilir, bu da
loopback, private aralıklar, CGNAT ve link-local bulut metadata endpoint'lerini erişim dışında tutar. Varsayılan
`OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS`'ü izler: self-hosted bir kurulum kendi iç servislerini kontrol edebilir, kayda açık bir
kurulum edemez.

| Değişken | Varsayılan | Servisler | Açıklama |
|---|---|---|---|
| `OPENLOG_SYNTHETICS_ENABLED` | `true` | api | `/api/v1/synthetics/*` sunar ve zamanlayıcıyı leader'da çalıştırır |
| `OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS` | `true` unless `OPENLOG_SIGNUP_ENABLED=true` | api | Kontrollerin private, loopback, CGNAT ve link-local adreslere erişmesine izin verir (ayarlı değilse SSRF koruması) |
| `OPENLOG_SYNTHETICS_MAX_CONCURRENT` | `20` | api | Leader'da tüm organizasyonların toplam eşzamanlı çalıştırması (1–1000) |
| `OPENLOG_SYNTHETICS_TENANT_MAX_CONCURRENT` | `5` | api | Bir organizasyonun eşzamanlı çalıştırması (1–`OPENLOG_SYNTHETICS_MAX_CONCURRENT`), böylece tek tenant tüm havuzu kullanamaz |
| `OPENLOG_SYNTHETICS_MAX_RESPONSE_BYTES` | `1048576` | api | Bir çalıştırmanın okuduğu yanıt gövdesi (1024–67108864); daha büyük yanıt kesilmek yerine çalıştırmayı başarısız yapar |
| `OPENLOG_SYNTHETICS_MAX_REDIRECTS` | `5` | api | Bir çalıştırmanın izlediği yönlendirme (0–10); her adım yeniden doğrulanır |
| `OPENLOG_SYNTHETICS_CA_FILE` | — | api | Sistem köklerine ek olarak güvenilen PEM paketi, sertifikaları kurulumun kendi CA'sından gelen iç endpoint'ler için. Kontrol başına "doğrulamayı atla" yoktur: doğrulamayan bir tls kontrolü hiçbir şeyi kontrol etmez (D-140) |

### Job izleme (api, allinone; D-141) {#job-monitoring-api-allinone-d-141}

Cron ve heartbeat monitörleri ([api.md](/tr/contracts/api#job-monitoring)). Tanımlar ve güncel durum
PostgreSQL'de (`0096_job_monitors`), sonuçlanan çalıştırmalar ClickHouse'ta (`job_runs`, 90 gün) durur. Ping endpoint'ini
**her** api pod'u sunar, çünkü bir crontab kilidin hangi pod'da olduğuna bağlı kalmamalı; hiçbir şeyin bildirmediği çalıştırmaları
sonuçlandıran sweeper bir leader görevidir.

| Değişken | Varsayılan | Servis | Anlamı |
|---|---|---|---|
| `OPENLOG_JOBS_ENABLED` | `true` | api | `/api/v1/jobs/*` sunar ve gecikmiş monitörleri tarar (yalnızca postgres auth modu) |
| `OPENLOG_JOBS_SWEEP_INTERVAL` | `30s` | api | Gecikmiş monitörlerin ne sıklıkla arandığı (5s–10m); bir alert'in monitörün kendi grace period'unun ötesinde en fazla ne kadar gecikebileceğini sınırlar |
| `OPENLOG_JOBS_MAX_PER_SWEEP` | `200` | api | Bir geçişte sonuçlandırılan monitör (1–10000); dolu bir geçişi hemen bir başkası izler, yani bu tavan değil batch boyutudur |

### Zafiyetler (api, allinone; D-142) {#vulnerabilities-api-allinone-d-142}

Altyapı ajanının zaten bildirdiği paketleri bir zafiyet feed'iyle eşleştirir
([api.md](/tr/contracts/api#vulnerabilities)). Katalog kurulum geneli (OSV advisory'leri public veridir,
`0097_vulnerabilities`), bulgular host başınadır (ClickHouse `host_vulnerabilities`, 90 gün). Feed
sync'i ve eşleştirici leader görevleridir.

| Değişken | Varsayılan | Servis | Anlamı |
|---|---|---|---|
| `OPENLOG_VULN_ENABLED` | `true` | api | `/api/v1/vulnerabilities/*` sunar ve eşleştiriciyi çalıştırır (yalnızca postgres auth modu) |
| `OPENLOG_VULN_FEED_URL` | `https://osv-vulnerabilities.storage.googleapis.com` | api | Ekosistem başına OSV export'larının indirildiği yer. İndirmeyi ağ içinde tutmak için bir mirror'a ayarla; indirmeyi tamamen kapatmak için **boş** bırak: air-gapped bir kurulum kataloğu kendisi doldurur ve eşleştirme saklanan veriyle çalışmaya devam eder |
| `OPENLOG_VULN_FEED_TIMEOUT` | `10m` | api | Tek bir ekosistem indirmesini sınırlar (1m–1h) |
| `OPENLOG_VULN_SYNC_INTERVAL` | `12h` | api | Feed'in ne sıklıkla indirildiği (1h–168h) |
| `OPENLOG_VULN_MATCH_INTERVAL` | `1h` | api | Host'ların ne sıklıkla yeniden eşleştirildiği (5m–24h); envanter dakikada bir değil, bir paket kurulunca değişir |

Yalnızca kurulumun gerçekten çalıştırdığı ekosistemler indirilir: bir host'un ekosistemi
işletim sistemi kaydından ve paket yöneticisinden türetilir (`Debian:12`, `Ubuntu:22.04`, `Alpine:v3.19`, …), yani
yalnızca Debian olan bir kurulum Alpine export'unu asla çekmez.

## Bulut bağlantıları (api, allinone; D-135) {#cloud-connections-api-allinone-d-135}

Yönetilen bulut servislerinin metrikleri ([api.md](/tr/contracts/api#cloud-connections)). Bağlantılar organizasyon başına
PostgreSQL'de saklanır (`0092_cloud_connections`); zamanı gelen scope'ları api **leader**'ı yoklar, yani aşağıdaki limitler
cluster'ı değil tek süreci sınırlar. Toplanan her veri noktası bir ajanınki gibi ClickHouse `metrics`'e yazılır, yani alerting
veya dashboard'lar için başka bir şey yapılandırman gerekmez. Yalnızca postgres auth modu.

Kimlik bilgileri **`OPENLOG_SECRETS_KEY`** ile şifrelenir (alert kanal secret'ları ve
entegrasyon ayarlarıyla aynı anahtar). O olmadan bir bağlantı kimlik bilgileriyle kaydedilemez ve hiçbir şey yoklanmaz; arayüz
sessizce başarısız olmak yerine bunu söyler.

Bu sağlayıcı API'leri istek başına faturalanır ve rate limit'leri serttir, bu yüzden maliyet kontrolleri global değil
bağlantı başınadır: `max_metrics_per_poll` ve `max_api_calls_per_poll` bağlantının alanlarıdır
(varsayılanlar 5000 ve 200) ve bunlardan birine ulaşan yoklama `partial` olarak bildirilir. Aşağıdaki değişkenler leader'ın aynı anda ne kadar
iş yaptığını sınırlar.

| Değişken | Varsayılan | Servisler | Açıklama |
|---|---|---|---|
| `OPENLOG_CLOUD_ENABLED` | `true` | api | `/api/v1/cloud/*` sunar ve yoklayıcıyı leader'da çalıştırır |
| `OPENLOG_CLOUD_MAX_CONCURRENT` | `10` | api | Leader'da tüm organizasyonların toplam eşzamanlı scope yoklaması (1–1000) |
| `OPENLOG_CLOUD_TENANT_MAX_CONCURRENT` | `4` | api | Bir organizasyonun eşzamanlı yoklaması (1–`OPENLOG_CLOUD_MAX_CONCURRENT`), böylece tek tenant tüm havuzu kullanamaz |
| `OPENLOG_CLOUD_CONNECTION_MAX_CONCURRENT` | `2` | api | Bir bağlantının eşzamanlı yoklaması (1–`OPENLOG_CLOUD_TENANT_MAX_CONCURRENT`), böylece çok region kapsayan bir bağlantı tek bulut hesabına ani istek yağdırıp rate limit'lerini tetiklemez |
| `OPENLOG_CLOUD_REQUEST_TIMEOUT` | `30s` | api | Tek bir sağlayıcı HTTP isteğinin sınırı (1s–5m) |

## Rapor grafik görselleri (`openlog-renderer`, api, allinone; D-097) {#report-chart-images-openlog-renderer-api-allinone-d-097}

Zamanlanmış rapor e-postalarında isteğe bağlı PNG widget görselleri ([operations/reports.md](/tr/operations/reports)).
`OPENLOG_RENDERER_URL` olmadan raporlar HTML tablolarını korur ve render endpoint'i olmaz. `openlog-renderer` kendi
image'ı olarak gelir (`ghcr.io/onuragtas/openlog-renderer`, headless Chromium); Kafka, ClickHouse veya PostgreSQL gerektirmez.

| Değişken | Varsayılan | Servis | Açıklama |
|---|---|---|---|
| `OPENLOG_RENDERER_URL` | `` | api | Render API base URL'i, örneğin `http://openlog-renderer:8090` (TLS ile `https://`). `OPENLOG_RENDERER_TOKEN` ile `OPENLOG_KEY_HASH_SECRET` veya `OPENLOG_SECRETS_KEY` gerektirir (render token'ları bunlardan türetilen bir anahtarla imzalanır; her api pod'unda aynı değer) |
| `OPENLOG_RENDERER_TOKEN` | `` | api, renderer | Render API'nin paylaşılan secret'ı (≥ 32 karakter, `openssl rand -hex 32`); `openlog-renderer` için zorunlu |
| `OPENLOG_RENDERER_TIMEOUT` | `2m` | api | Bir raporun görsellerini render etmenin sınırı (≥ 5s); timeout'ta veya herhangi bir renderer hatasında e-posta tablolarla gönderilir |
| `OPENLOG_RENDERER_TLS_CERT_FILE` / `OPENLOG_RENDERER_TLS_KEY_FILE` | `` | api, renderer | İsteğe bağlı PEM dosyaları: `openlog-renderer`'da renderer'ın sunucu sertifikası, api'de api'nin istemci sertifikası (birlikte ayarla) |
| `OPENLOG_RENDERER_TLS_CA_FILE` | `` | api, renderer | `openlog-renderer`'da: kabul edilen istemci sertifikalarının CA'sı (istemci sertifikaları zorunlu olur: mTLS; sunucu sertifikası gerekir). Api'de: renderer'ı doğrulayan CA |
| `OPENLOG_RENDERER_ADDR` | `:8090` | renderer | Render API'nin dinleme adresi |
| `OPENLOG_RENDERER_UI_ORIGIN` | `` | renderer | Zorunlu. Tarayıcının print görünümünü yüklediği web arayüzü origin'i, örneğin `http://openlog-api:8080` (yalnızca şema, host, port). Sayfanın başka bir origin'e her isteği reddedilir, DNS yalnızca bu host'u çözümler |
| `OPENLOG_RENDERER_MAX_CONCURRENCY` | `2` | renderer | Eşzamanlı render (1–64); her biri kendi Chromium sürecini çalıştırır (≈ 200–400 MiB) |
| `OPENLOG_RENDERER_QUEUE_TIMEOUT` | `30s` | renderer | Bir isteğin `429`'dan önce boş slot için ne kadar bekleyeceği |
| `OPENLOG_RENDERER_RENDER_TIMEOUT` | `60s` | renderer | Tek bir render'ın sınırı: tarayıcı başlatma, sayfa yükleme, yakalamalar (≥ 5s); sonra `502` ve tarayıcı öldürülür |
| `OPENLOG_RENDERER_MAX_IMAGE_BYTES` | `1048576` | renderer | Widget başına en büyük PNG (16 KiB–16 MiB); daha büyük yakalamalar element hatası olarak bildirilir. Api ayrıca görsel başına ≤ 1 MiB ve e-posta başına ≤ 10 MiB gömer |
| `OPENLOG_RENDERER_CHROMIUM_PATH` | `` | renderer | Chromium binary'si; boş = `PATH`'ten `chromium`, `chromium-browser`, `google-chrome` veya `headless-shell` (image `/usr/bin/chromium` ayarlar) |
| `OPENLOG_RENDERER_CHROMIUM_NO_SANDBOX` | `false` | renderer | Chromium'u `--no-sandbox` ile çalıştırır. Yalnızca kilitlenmiş bir container'da (root olmayan, salt okunur kök dosya sistemi, capability yok, yeni ayrıcalık yok, çıkış yalnızca UI origin'ine); Compose profili ve Helm chart'ı bu kısıtlarla `true` ayarlar, çünkü varsayılan seccomp profilleri sandbox'ın user namespace'lerini reddeder |

`openlog-renderer` endpoint'leri: `POST /v1/render` (render API, [operations/reports.md](/tr/operations/reports)) ve
admin portu (`OPENLOG_ADMIN_ADDR`: `/healthz`, `listener` ve `chromium` kontrolleriyle `/readyz`,
`openlog_renderer_renders_total{result}` ve `openlog_renderer_render_duration_seconds` ile `/metrics`).

## Port özeti {#ports-summary}

| Port | Servis |
|---|---|
| 4317 | ingest OTLP/gRPC |
| 4318 | ingest OTLP/HTTP |
| 8080 | api |
| 8090 | openlog-renderer render API (yalnızca dahili) |
| 9464 | admin (sağlık, readiness, metrikler) |

## Henüz belirlenmedi {#not-yet-specified}

- Kafka `OAUTHBEARER`/`GSSAPI` SASL mekanizmaları ve ClickHouse HTTPS (servisler yalnızca native protokolü kullanır).
