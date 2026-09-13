# PHP agent kararı (D-034) — spike raporu

Durum: **karar bekliyor.** Tarih: 2026-09-13. Kapsam: `docs/plan/10-m2.md` §5.
Bu belge ürün sahibine sunulmak üzere yazıldı. Ölçümler bu spike'ta Docker'da yapıldı; ham veriler
`agents/php/spike/results/` altında, tekrar üretmek için `make -C agents/php spike`.

## 0. Özet (TL;DR)

- İki prototip de çalışıyor ve ortak yerel openlog'a trace gönderiyor. Laravel 12 ve Symfony 7.4'te uygulama
  kodu ve composer.json değişmedi. Veriler UI'da APM altında görülebilir (§4).
- **Ölçülen overhead** (Laravel, doygunlukta, 3 koşu medyanı, §5):
  - **A (kendi C extension + Go forwarder):** base'den ayırt edilemiyor (RPS +1.7%, PHP CPU/istek −0.1%,
    +0.9 MB/worker).
  - **B (OTel extension + scoped bundle, ingest'e doğrudan export):** RPS **−61%**, p50 **+166%**, PHP
    CPU/istek **+92%**, +4.5 MB/worker.
- **Hata testi:** A'da forwarder öldürülünce uygulama %0 hata ile çalışmaya devam etti. B'de backend
  erişilemez olunca kapasite **%96 düştü** (her istek ~1 sn bekledi).
- **Öneri** (§7): B'yi M2'de "preview" olarak çıkarmak, ama **yalnızca yerel forwarder üzerinden** export ederek.
  Forwarder'ı iki yolun ortak parçası olarak infra agent'a eklemek. A'yı M3 başında 2 kişiyle, bayrak arkasında
  başlatmak ve ölçülebilir kriterlerle varsayılan yapmak.
- Karar için §8'deki sorular yanıtlanmalı.

## 1. Arka plan: başkaları nasıl yapıyor?

| Ürün | Mimari | Not |
|---|---|---|
| **New Relic** | `newrelic.so` (C extension) + `newrelic-daemon` (Go) | Extension her istekte veriyi yerel daemon'a unix socket ile yollar. Daemon uygulama başına toplar ve collector'a gönderir. PHP 8'de fonksiyon sarmalamayı Observer API ile yapar. Framework tespiti (Laravel, Symfony, WordPress, Drupal…) extension içinde C kodudur. Kod 2020'den beri Apache-2.0. |
| **Datadog** | `ddtrace` extension (C, yeni sürümlerde Rust'lı `libdatadog` / sidecar) | Entegrasyonların çoğu PHP ile yazılmış hook'lardır ve extension ile birlikte dağıtılır, Composer gerekmez. Gönderim arka plan thread'i ya da sidecar süreçle yapılır. Kurulum `datadog-setup.php` ile olur. |
| **OpenTelemetry** | `ext-opentelemetry` (C) + Composer paketleri (`open-telemetry/sdk`, `opentelemetry-auto-*`) | Extension yalnızca `OpenTelemetry\Instrumentation\hook()` sağlar (Observer API üzerinde pre/post closure). Span üretimi, örnekleme ve export PHP kodunda çalışır. Varsayılan kurulum uygulamanın Composer'ına paket eklemeyi gerektirir. PHP içinde arka plan thread'i yoktur; export istek sonunda senkron yapılır. |

Ortak ders: PHP-FPM'in süreç modelinde (kısa ömürlü istek, paylaşımsız worker'lar) ciddi agent'ların hepsi
veriyi **yerel bir sürece** devreder ya da en azından export'u yanıt gönderildikten sonraya bırakır.

## 2. Seçenek A — Kendi extension'ımız

### 2.1 Dil: C mi, Rust mı?

| | C | Rust (`ext-php-rs`) |
|---|---|---|
| Observer API erişimi | Doğrudan (`zend_observer_fcall_register`, `zend_observer_error_register`) | `ext-php-rs` üst seviye API'lerinde Observer desteği sınırlı; ham `bindgen` FFI gerekir ve bu `unsafe` kod demektir. Kullanılacak sürümle doğrulanmalı. |
| Bellek güvenliği | Tamamen bizde | Güvenli kısım (serileştirme, kuyruk, config) Rust'ta; Zend yapılarına dokunan sınır yine `unsafe` |
| Engine makroları | Birinci sınıf (`ZEND_CALL_ARG`, `Z_PDO_STMT_P`, ZTS makroları) | Makrolar FFI'da yok, elle yeniden yazılır. PHP minor sürümleri arasında kırılgan. |
| Derleme matrisi | `phpize` + `cc`, basit | Rust toolchain + bindgen + her PHP header seti. musl/ZTS kombinasyonları daha zahmetli. |
| Ekip | PHP çekirdek C bilgisi gerekir, bulması zor | Rust bilen bulmak daha kolay, ama Zend bilgisi yine şart |

Spike'ta **C** seçildi: Observer API ve PDO iç yapıları doğrudan erişilebildiği için prototip bir günde çalıştı.
Ürün için önerilen yol **hibrit**: engine sınırı ince bir C katmanı (hook'lar, span tablosu), serileştirme,
örnekleme ve konfigürasyon ise ayrı süreçte (Go forwarder / infra agent). Böylece extension içindeki kod
küçük kalır; güvenlik ve çökme yüzeyi azalır.

### 2.2 Hook noktaları (prototipte uygulanan)

- `MINIT`: `zend_observer_fcall_register(init)`. `init` her fonksiyon için **bir kez** çağrılır ve sonucu engine
  önbelleğe alır. İlgilenmediğimiz fonksiyonlarda handler `NULL` döner, sıcak yolda maliyet yok denecek kadar azdır.
- `RINIT` / `RSHUTDOWN`: istek span'ı; W3C `traceparent` okunur.
- `PDOStatement::execute`, `PDO::exec/query`: `pdo_stmt_t->query_string` ve `driver_name` doğrudan C yapısından okunur.
- phpredis `Redis::*`: komut adı fonksiyon adından alınır.
- Laravel: `Illuminate\Routing\Router::runRoute($request, $route)` → `Route::$uri` (route şablonu, `http.route`).
  `Illuminate\Foundation\Exceptions\Handler::report()` → exception.
- Symfony: `HttpKernel::handleRaw` bitişinde `Request::$attributes->parameters['_route']`.
- `zend_observer_error_register` ile fatal hatalar.
- Ürün için eklenecekler: WordPress (`do_action('template_redirect')`, `WP::main`), Slim
  (`Slim\Routing\RouteRunner::handle` → `RouteContext`), mysqli, curl (`curl_exec` + `traceparent` enjeksiyonu),
  Guzzle (`GuzzleHttp\Client::transfer`), Predis, Monolog / `error_log` için log korelasyonu (`trace_id` ekleme),
  CLI / queue worker transaction'ları (Laravel Horizon, Symfony Messenger).

### 2.3 Derleme ve dağıtım matrisi

Her PHP minor sürümü ayrı bir `ZEND_MODULE_API_NO` demektir; NTS ve ZTS ABI uyumsuzdur. glibc/musl ve amd64/arm64
de ayrı ikili gerektirir.

- Brief'teki matris: PHP 8.1–8.4 (4) × NTS/ZTS (2) × glibc/musl (2) × amd64/arm64 (2) = **32 `.so` / sürüm**.
- Not: PHP 8.1'in güvenlik desteği 31.12.2025'te bitti; PHP 8.5 Kasım 2025'te çıktı. Gerçekçi hedef
  **8.2–8.5**, yine 32 ikili, her yıl bir minor eklenip biri çıkarılır.
- Dağıtım kanalları: (1) openlog release'inde imzalı tarball (`openlog-php-<v>-php8.3-nts-glibc-arm64.tar.gz`),
  (2) deb/rpm/apk paketleri (Ondřej Surý / Remi / Alpine PHP paket yolları), (3) resmi `php:*` imajları için
  `COPY --from=ghcr.io/…/openlog-php:<v>` katmanı, (4) PIE (PECL'in halefi). PIE Linux'ta çoğunlukla kaynaktan
  derler ve kullanıcıda `phpize` + derleyici ister, bu yüzden ön-derlenmiş ikili ana yol olmalı.
- CI: matris başına derleme, `make test` (phpt), ASan/UBSan'lı debug build, Valgrind ve fuzz (bozuk
  `traceparent`, çok uzun SQL, derin çağrı yığını). Yaklaşık 64+ job / release.

### 2.4 Çökme güvenliği stratejisi

Prototipte uygulananlar:
- Sıcak yolda **heap tahsisi yok**: span tablosu (256), yığın (128) ve string arena'sı (32 KB) modül global'lerinde
  sabit boyutlu. Taşma olursa span düşürülür ve sayacı tutulur, begin/end dengesi hiç bozulmaz.
- Userland'e geri çağrı yok, PHP fonksiyonu çağrılmaz. Sadece `zend_read_property` ile okuma yapılır.
- Gönderim: istek sonunda **tek** `sendto(MSG_DONTWAIT)`. Forwarder yoksa (`ENOENT`/`ECONNREFUSED`) ya da doluysa
  (`EAGAIN`) veri atılır ve istek etkilenmez (**fail-open**).

Üründe ek olarak gerekenler:
- `openlog.enabled` kill-switch ve fleet'ten uzaktan kapatma.
- Sinyal/crash handler ile son çöküş imzasını diske yazıp bir sonraki başlangıçta extension'ı otomatik devre dışı
  bırakmak (watchdog). Kademeli rollout (D-027 dalgaları).
- Fuzz ve 72 saatlik soak testi (risk kaydındaki madde).
- Bilinen tuzak: aynı süreçte başka bir Observer kullanan extension (Xdebug, Blackfire, ddtrace, ext-opentelemetry)
  ile birlikte yükleme test edilmeli.

### 2.5 Süreç içi mi, sidecar mı?

PHP-FPM worker'ı her istekten sonra "boşa" döner. Arka plan thread'i yoktur ve istekler arasında zamanlayıcı
çalışmaz. Süreç içinden HTTP export etmek ya istek süresine eklenir ya da worker'ı meşgul eder. Bu yüzden
**yerel forwarder** önerilir:

- Spike: Go forwarder, unix datagram socket, 1 sn / 2000 span batch, OTLP/HTTP protobuf.
- Ürün: bu forwarder **openlog-infra-agent'a bir modül** olarak eklenmeli. Infra agent zaten OTLP export, disk
  buffer, lisans anahtarı, `host.id` ve self-update (D-027) sağlıyor. Böylece host ↔ servis ilişkisi bedava gelir
  (`host.id` resource attribute). Container kurulumlarında sidecar container ya da paylaşılan volume'daki socket
  kullanılır. Socket yolu: `/run/openlog/php.sock`.
- Datagram boyut sınırı: tek istek ≤ 60 KB. Daha büyük trace'ler için akış (stream socket + ring buffer) M3 işi.

### 2.6 Overhead hedefleri (öneri)

- Tipik Laravel isteğinde p50 artışı ≤ %3, p95 ≤ %5, CPU/istek ≤ %5; worker başına ≤ 2 MB ek RSS.
- Forwarder: 1000 istek/sn'de ≤ %5 tek çekirdek.

### 2.7 Bakım maliyeti tahmini

- MVP (bu belgedeki hook listesi, 4 framework, PDO/mysqli/Redis/curl/Guzzle, exception, log korelasyonu, dağıtık
  trace, CLI/queue, forwarder'ın infra agent'a taşınması, paketleme): **2 kişi × 5–6 ay**.
- New Relic seviyesine yakın derinlik (fonksiyon düzeyi segmentler, slow SQL explain, WordPress hook zamanlaması,
  custom instrumentation API'si): **+2–3 kişi × 9–12 ay**.
- Sürekli bakım: her PHP minor sürümü, framework major sürümleri ve matris CI için **~1–1.5 FTE**.
Bunlar tahmindir, spike ölçümü değildir.

## 3. Seçenek B — OTel PHP extension'ı + paketlenmiş otomatik ölçüm

### 3.1 Kullanıcıya Composer'sız kurulum

Spike'ta uygulanan yol:
1. `pecl install opentelemetry protobuf` (ürün: ön-derlenmiş `.so`'lar, A ile aynı matris ama upstream kodu).
2. `/opt/openlog-php/` altında **kendi vendor'ümüz**: `open-telemetry/sdk`, `exporter-otlp`, `auto-laravel`,
   `auto-symfony`, `auto-pdo`, `nyholm/psr7` ve küçük bir cURL PSR-18 istemcisi (Guzzle/Symfony HttpClient
   taşımamak için).
3. **php-scoper** ile tüm bağımlılıklar `OpenlogVendor\` önekine alınır (963 sınıf, 6 MB).
4. `auto_prepend_file=/opt/openlog-php/prepend.php` SDK'yı `OPENLOG_*` ortam değişkenleriyle kurar.
   Hata olursa istek izlenmeden devam eder.

Uygulama koduna ve composer.json'a dokunulmadı. Ayar `php.ini` / FPM pool `env[]` ile yapılıyor.

### 3.2 İzolasyon: bulunan sorunlar

- **php-scoper protobuf üretilmiş sınıflarını bozar.** İlk denemede export her istekte
  `Couldn't find descriptor` hatasıyla düştü. `ext-protobuf`, mesaj sınıflarını serileştirilmiş descriptor'daki
  PHP namespace'inden bulur. Çözüm: `Opentelemetry\Proto` ve `GPBMetadata` scoping dışında bırakıldı.
  Sonuç: uygulamanın kendisinde farklı sürüm `open-telemetry/gen-otlp-protobuf` varsa **sınıf çakışması** hâlâ
  mümkün. Kalıcı çözüm: OTLP'yi protobuf sınıfları olmadan kodlayan küçük bir encoder (ya da forwarder'a JSON göndermek).
- Hook'lanan framework namespace'leri (`Illuminate`, `Symfony\Component\HttpKernel`…) scoping dışında tutulmalı,
  aksi hâlde otomatik ölçüm hiçbir şeye bağlanmaz. Bu liste framework eklendikçe bakım ister.
- Uygulama zaten OTel SDK kullanıyorsa iki kopya (scoped ve uygulamanınki) iki kez hook kaydeder ve **span'lar
  çift gelir**. Ürün: uygulamanın `vendor/composer/installed.json`'unda `open-telemetry/sdk` görülürse bundle
  kendini kapatmalı.
- `auto_prepend_file` zaten kullanılıyorsa (bazı hosting panelleri) zincirleme gerekir.

### 3.3 Kapsam: OTel contrib bizim ihtiyaçlarımıza göre

| İhtiyaç | B'de durum (spike'ta görülen) |
|---|---|
| Laravel transaction adı | Var: `http.route` = `users/{id}/orders`. Başında `/` yok, backend'de normalize edilmeli. |
| Symfony transaction adı | Var ama **route adı** (`product_show`), şablon değil. APM sözleşmesi `http.route` bekliyorsa bu bir fark. |
| WordPress, Slim | Contrib'de ayrı paketler var (`auto-wordpress`, `auto-slim`). Spike'ta denenmedi. |
| PDO | Var, ama çok ayrıntılı: `PDO::__construct`, `PDO::prepare`, `PDOStatement::execute`, `fetchAll` ayrı span'lar. Laravel QueryWatcher ile birlikte aynı sorgu **iki kez** görünür (`sql SELECT` + `PDO::prepare`/`execute`). |
| Redis (phpredis) | **Yok.** Contrib'de phpredis ölçümü yok. Laravel'in RedisCommand watcher'ı `Redis::enableEvents()` gerektirir; uygulama değişikliği olmadan span gelmedi. |
| HTTP istemcisi | Laravel `Http::` span'ı geldi (`GET`, 200). Guzzle, PSR-18 ve curl için ayrı paketler var. |
| Exception | Var (Laravel ve Symfony, `exception` event'i). |
| Log korelasyonu | Laravel LogWatcher logları OTel log olarak yollar. Monolog'a `trace_id` ekleme için ayrı paket gerekir. |
| Derin fonksiyon izleme | Yok. Her fonksiyon için PHP closure hook'u pahalı olur. |
| Export modeli | İstek sonunda PHP içinden senkron HTTP (BatchSpanProcessor shutdown flush). |

### 3.4 PHP sürüm desteği

`ext-opentelemetry` 8.0+ (güncel sürümler 8.1+), `open-telemetry/sdk` 1.x için PHP ≥ 8.1, `exporter-otlp` 1.4 ve
`auto-pdo`/`auto-curl` için PHP ≥ 8.2. Bu yüzden B pratikte **PHP 8.2+**. 7.x desteği yok, olmayacak da.

## 4. Spike prototiplerinin durumu

Ortam: tek OrbStack VM (arm64, 10 vCPU), PHP 8.3.33 NTS glibc, FPM `pm=static`, opcache açık, MariaDB 11,
Redis 7. Uygulamalar compose projesi `openlog-phpspike` altında çalışır. Telemetri, ortak yerel openlog'a
(compose projesi `openlog`, `host.docker.internal:4318`) gider; spike'ın kendi backend'i yoktur. Aynı VM'de başka
test yığınları da çalıştığı için mutlak sayılar değil, aynı ölçüm setindeki **oranlar** anlamlıdır. Uygulamalar: **Laravel 12** ve
**Symfony 7.4**. Brief Laravel 11 istiyordu, fakat Packagist güvenlik uyarıları tüm Laravel 11.x sürümlerini ve
Symfony skeleton'ın `symfony/cache`/`yaml`/`runtime` 7.3 sürümlerini engelliyor. Bu engeli devre dışı bırakmadık.
Symfony uygulaması FrameworkBundle olmadan gerçek `HttpKernel` + `RouterListener` akışı üzerinde çalışıyor.

Doğrulama (`bench/verify.sh`): her varyanta bilinen bir `traceparent` ile istek atılır, trace ortak openlog'un
Query API'sinden (`GET /api/v1/traces/{id}`) okunur, servisler `GET /api/v1/apm/services` ile listelenir.
Çıktının tamamı `spike/results/verify.txt` içinde. Sekiz trace'in hepsi bulundu. APM'de dört servis
`language=php` ile görünüyor.

**Ortak UI'da açılabilecekler** (http://localhost:8080 → APM):

| Servis | Seçenek | Örnek trace |
|---|---|---|
| `php-laravel-proto-a` | A | `4c2953622b6086caa46723f43306ffb6` (orders), `248af23ea18aa4404dc828b6d64c6a50` (exception) |
| `php-symfony-proto-a` | A | `5ccf0fb55209346e5c06c9513f752f32` (product), `34d987bc1667552af84e4e1779d7f73c` (hata) |
| `php-laravel-proto-b` | B | `47592c78d932044890b6888fc1507c2c` (orders), `f0246e9f051d9781fe24aec88df7af3f` (exception) |
| `php-symfony-proto-b` | B | `4f5e1770894056e617a1146d3a4053fd` (product), `c2ee8f92e27bc813518709069d7bbefd` (exception) |

`php-laravel-base` / `php-symfony-base` enstrümante değildir ve veri göndermez. Not: APM'deki istek ve hata sayıları
benchmark trafiğini de içerir. `php-laravel-proto-a` hata sayısının büyük kısmı §5.2'deki port tükenmesi
sırasında atılan koşulardan gelir.

| Kontrol | A (kendi extension) | B (OTel + bundle) |
|---|---|---|
| Trace openlog'da görünüyor | Evet | Evet |
| Gelen `traceparent`'a bağlanma | Evet (parent = gelen span) | Evet |
| Laravel transaction adı | `GET /users/{id}/orders`, `http.route=/users/{id}/orders` | `GET /users/{id}/orders`, `http.route=users/{id}/orders` (başta `/` yok) |
| Symfony transaction adı | `GET product_show` (route adı, `openlog.transaction.name`) | `GET product_show` (`http.route=product_show`) |
| Eloquent / PDO | Her sorgu için tek `SELECT` client span'ı + `db.query.text` | Eloquent span'ı + `sql SELECT` + ayrı `PDO::prepare` / `execute` / `fetchAll` span'ları (çift sayım) |
| Redis (phpredis) | Evet (`SELECT`, `INCR`, `GET`) | **Hayır** |
| Giden HTTP (Laravel `Http::`) | **Hayır** (curl hook'u yazılmadı) | Evet (client span, `server.address`, status) |
| Servisler arası trace (Laravel → Symfony) | **Hayır** (başlık enjeksiyonu yok) | **Hayır.** Laravel watcher başlık enjekte etmiyor, Guzzle/PSR-18 paketi gerekir. |
| Exception | Laravel: evet (`exception` event + hata durumu). Symfony: yalnızca 500 ve hata durumu, event yok. | Laravel ve Symfony: evet |
| Fatal error | Error observer ile (ayrıca test edilmedi) | Test edilmedi |
| Kod / composer.json değişikliği | Yok | Yok |

Prototip A'nın bilinen eksikleri: curl/Guzzle, mysqli, log korelasyonu, CLI/queue, WordPress/Slim,
SQL maskeleme, ZTS/musl/amd64 derlemeleri (yalnızca NTS glibc arm64 denendi), 60 KB üstü trace'ler.

Spike sırasında düzeltilen hatalar (tekrar etmemek için not): FPM'de `SG(request_info).request_uri` script adını
veriyor, `url.path` için `REQUEST_URI` okunmalı. Scoping, protobuf sınıflarını bozuyor (§3.2).

## 5. Ölçümler

### 5.1 Yöntem

- Uç nokta: Laravel `GET /bench/{id}` (bir Eloquent sorgusu + iki phpredis çağrısı, giden HTTP yok).
- PHP-FPM `pm=static`, Laravel havuzunda 8 worker; PHP container'ı 4 CPU ile sınırlı; opcache açık.
- k6 kapalı döngü, 16 VU. Her ölçüm: FPM yeniden başlatılır, 10 sn ısınma, 30 sn ölçüm. Varyant başına 3 koşu,
  sıra her koşuda değişir (base→A→B, A→B→base, B→base→A).
- CPU: PHP container'ının cgroup `usage_usec` farkı / istek sayısı. A için forwarder CPU'su ayrıca verilir.
- Bellek: ayrı bir 3 koşuluk turda (`bench/mem.sh`), 20 sn yükten sonra Laravel havuzundaki worker'ların ortalama
  `VmRSS` değeri. `smaps_rollup`/PSS container içinde okunamadı, bu yüzden paylaşılan sayfalar dahil RSS verildi.
- A ve B gerçek telemetriyi ortak openlog'a gönderir. Yani export maliyeti ölçüme dahildir.
- Tablo: 3 koşunun medyanı, parantez içinde min–max.

### 5.2 Ölçümü bozan ve düzeltilen iki sorun (atılan ilk sonuçlar)

1. **Ephemeral port tükenmesi:** Laravel her istekte Redis'e yeni TCP bağlantısı açıyordu. Saniyede ~600 isteğin
   üstünde TIME_WAIT soketleri yerel portları bitirdi. Sonuç `Cannot assign requested address` ve HTTP 500 oldu
   (base koşularında %17–25). Bu hızlı 500'ler base ve A'nın RPS'ini şişirdi, daha yavaş olan B ise eşiğin altında
   kaldı. Düzeltme: `REDIS_PERSISTENT=true` (üretimde de olağan) ve PHP container'ında
   `ip_local_port_range`/`tcp_tw_reuse`. Düzeltmeden sonra base 15 sn'de %0 hata ile ~1600 RPS verdi.
   Bu yüzden önceki bütün tur sonuçları atıldı.
2. **nginx'in eski IP'yi önbelleğe alması:** `docker compose restart php-*` container'a yeni IP verebiliyor. Statik
   `fastcgi_pass` 502 döndü. Düzeltme: Docker DNS `resolver` ve değişkenli upstream.

Ürün için ders: overhead ölçümlerini hep "sağlıklı" bir uygulamada (hata oranı %0) yapmak, hata oranını tabloya
yazmak gerekir. Aksi hâlde hata üreten varyant daha hızlı görünür.

### 5.3 Sonuçlar (Laravel `GET /bench/{id}`, 16 VU, 3 koşu)

| metrik (medyan, min–max) | base (ölçümsüz) | A (kendi extension) | B (OTel + bundle) |
|---|---|---|---|
| RPS | 1159 (1080–1425) | 1179 (1047–1343) | 451 (445–462) |
| p50 ms | 12.38 (10.26–12.66) | 12.23 (10.96–12.89) | 32.96 (32.72–33.48) |
| p95 ms | 22.48 (18.06–27.15) | 22.92 (18.28–27.82) | 49.94 (48.58–54.42) |
| p99 ms | 33.75 (24.45–41.53) | 32.44 (27.14–49.35) | 66.59 (61.87–75.77) |
| başarısız istek % | 0.00 | 0.00 | 0.00 |
| PHP CPU ms/istek | 2.955 (2.547–3.133) | 2.951 (2.668–3.217) | 5.688 (5.506–5.764) |
| forwarder CPU ms/istek | — | 0.121 (0.108–0.131) | — |
| VmRSS MB/worker | 29.8 (29.7–30.2) | 30.7 (30.4–31.0) | 34.3 (34.1–34.6) |

Base'e göre fark (medyanlar):
- **A:** RPS +1.7%, p50 −1.2%, p95 +1.9%, PHP CPU/istek −0.1%. Hepsi koşular arası gürültünün (base RPS
  1080–1425) içinde. Yani bu ölçüm düzeyinde **ayırt edilebilir bir overhead yok**. Ek maliyet: forwarder'da istek
  başına 0.12 ms CPU (PHP CPU'sunun ~%4'ü, başka süreçte) ve worker başına +0.9 MB RSS (+%3).
- **B:** RPS **−61%**, p50 **+166%**, p95 +122%, PHP CPU/istek **+92%**, worker başına +4.5 MB RSS (+%15).

Karşılaştırmanın adil olup olmadığı:
- İstek başına span sayısı yakın. A'da forwarder sayaçlarına göre **7.0 span/istek** ölçüldü (server, bağlantı
  kurulumundaki `USE`/`SET`, `SELECT`, Redis komutları). B'de aynı istek şeklinin trace'i ~9 span (sunucu,
  Eloquent, `sql SELECT`, PDO `__construct`/`exec`×2/`prepare`/`execute`/`fetchAll`). Bu B için bir trace'ten
  **tahmindir**, sayılmadı. B'nin Redis span'ı yok. Yani B daha az değil, benzer miktarda veri üretirken daha pahalı.
- B'nin PHP container'ı 30 sn'lik ölçümde ~76 CPU-sn kullandı (4 çekirdeğin ~%63'ü), base ise ~105 CPU-sn (CPU'ya
  dayanmış). B'deki darboğaz yalnız CPU değil: istek sonunda senkron OTLP/HTTP export worker'ı meşgul tutuyor.
  Laravel yanıtı `fastcgi_finish_request()` ile önce gönderse de worker export bitene kadar yeni istek alamıyor. Bu
  kapalı döngü testinde kuyruk olarak gecikmeye yansıyor.
- **Ölçülmeyenler:** düşük yükte (doygunluk altında) gecikme etkisi; B'nin export'unu yerel bir collector'a
  (localhost) yönlendirince kalan maliyet (SDK CPU'su ile ağ beklemesini ayırmak için gerekli); ZTS, musl, amd64;
  Symfony uygulamasında yük; uzun süreli (soak) bellek davranışı.

### 5.4 Hata dayanıklılığı (`bench/crash-test.sh`, her faz 15 sn yük)

| Senaryo | RPS | p50 ms | p95 ms | başarısız % |
|---|---|---|---|---|
| A, forwarder çalışıyor | 970.6 | 14.68 | 28.33 | 0.00 |
| A, forwarder **SIGKILL** ile öldürüldü | 1091.0 | 13.51 | 23.24 | 0.00 |
| A, forwarder yeniden başlatıldı | 1034.1 | 14.20 | 25.17 | 0.00 |
| B, ingest erişilebilir | 441.3 | 34.00 | 50.28 | 0.00 |
| B, ingest **kara deliğe** yönlendirildi (yanıt vermeyen IP) | **16.0** | **1022.36** | **1032.97** | 0.00 |

- A: forwarder yokken uygulama kesintisiz hizmet verdi (**fail-open doğrulandı**). O süredeki span'lar kayboldu,
  bu tasarım gereği. Yeniden başlatınca hiçbir PHP ayarı değiştirmeden veri akışı sürdü: forwarder yeniden
  başladıktan sonra 15 475 datagram, 108 325 span, 0 export hatası.
- B: backend erişilemez olunca **kapasite %96 düştü**, her istek ~1 sn bekledi. Bu bizim bundle'daki kısa
  zaman aşımlarıyla (bağlantı 500 ms, 1 yeniden deneme) ölçüldü. OTel exporter'ın varsayılan zaman aşımı (10 sn)
  ile durum daha kötü olur. Yani **B, openlog backend'inin kesintisini müşteri uygulamasına taşıyor**. Doğrudan
  ingest'e export eden bir B ürün olarak kabul edilemez, export yerel bir süreçten geçmeli.

## 6. Güvenlik, lisans, güncelleme

### 6.1 Güvenlik
- İki yol da uygulama sürecinin içinde kod çalıştırır. B'de bu PHP kodudur (uygulamanın yetkisiyle, bizim bundle'ın
  tedarik zinciri: ~20 Composer paketi). A'da C kodudur (bellek hatası = uygulama çöküşü ya da açık).
- SQL metni span'a girer. Üründe literal maskeleme (`?` ile değiştirme) varsayılan olmalı. A'da bu forwarder'da
  yapılabilir, B'de PHP'de yapılır. Laravel/Eloquent sorguları zaten parametreli geldi; ham `PDO::exec` metinleri
  (`SET NAMES …`) literal içerir.
- Lisans anahtarı A'da PHP sürecine hiç girmez (forwarder / infra agent'ta kalır). B'de anahtar PHP'nin ortamında
  durur ve `phpinfo()` ile sızabilir. Bu, A mimarisinin (ya da B + yerel forwarder'ın) bir artısı.
- Unix socket izinleri: `0777` spike içindir. Üründe `www-data` grubuna `0660`.

### 6.2 Lisans (D-004: agent'lar Apache-2.0)
- A: kodu tamamen bizim, Apache-2.0. PHP header'ları PHP License 3.01 ile gelir; extension'ların farklı lisansla
  dağıtılması yaygın (New Relic Apache-2.0, Datadog Apache-2.0/BSD). Rust yolunda `ext-php-rs` MIT/Apache-2.0.
- B: `ext-opentelemetry`, OTel SDK ve contrib Apache-2.0. `ext-protobuf` / `google/protobuf` BSD-3-Clause.
  `nyholm/psr7`, `ramsey/uuid`, `brick/math`, `php-http/discovery` MIT. php-scoper MIT (sadece build zamanı).
  Hepsi Apache-2.0 dağıtımla uyumlu. NOTICE dosyasında listelenmeleri gerekir.

### 6.3 Fleet üzerinden güncelleme (D-025, D-027)
- Her ürün sürümü için: `openlog-php-<v>-<phpver>-<nts|zts>-<libc>-<arch>` (`.so` + B için bundle tarball),
  aynı imzalı release manifest'inde.
- Infra agent, host'ta PHP'yi keşif sonucundan (`apm_hint: {"language":"php"}`) bilir ve php-fpm ikilisinden
  `php -i` ile API sürümü, ZTS ve ini dizinini çıkarır.
- Kurulum: `/opt/openlog/php/versions/<v>/`, `current` symlink'i ve `conf.d/90-openlog.ini` bu yola işaret eder.
  **`.so` asla yerinde üzerine yazılmaz** (mmap edilmiş dosya değişirse çalışan worker çöker). Yeni sürüm yeni
  yolda durur. Opcache `validate_timestamps=0` olan sistemlerde de yeni yol bayat bytecode sorununu önler.
- Self-test: `php -n -d extension=<yeni>.so -m` ve `php -d auto_prepend_file=… -r 'exit(0);'`, sonra
  `php-fpm -t`, sonra **graceful reload** (`kill -USR2 <master>` / `systemctl reload php8.3-fpm`). Worker'lar
  ellerindeki isteği bitirip yenilenir.
- Onay: reload sonrası N dakika FPM ayakta mı, 5xx oranı arttı mı, forwarder'a veri geliyor mu? Değilse symlink
  geri alınır ve tekrar reload edilir (D-027 otomatik geri dönüş). Container kurulumları kendini güncellemez;
  imaj etiketi yükseltilir.

## 7. Öneri

### 7.1 Karar önerisi

Spike, plandaki varsayılanı ("kısa vadede B, A M3'te") **korumayı**, ama iki şartla güncellemeyi öneriyor:

1. **B, "preview" olarak çıkabilir, ama doğrudan ingest'e değil yerel forwarder'a export ederek.** Ölçülen iki
   sorun var: saturasyonda %61 kapasite kaybı ve backend kesintisinde %96 çöküş. İkincisi mimari bir hata ve yerel
   forwarder ile giderilir. Birincisi kısmen giderilir: ağ beklemesi kalkar, SDK'nın PHP CPU'su kalır. Ne kadarının
   kalacağı **ölçülmedi**, preview'dan önce ölçülmeli.
2. **A, M3'ün başında ciddi bir iş olarak başlamalı.** Spike'ın en önemli bulgusu şu: Observer API + sabit
   bellek + tek non-blocking datagram modeli bu testte **ölçülebilir overhead üretmedi** ve forwarder ölümüne
   dayanıklı. A'nın asıl riski performans değil; çökme güvenliği, derleme matrisi ve kapsam genişliği. Bunlar da
   mühendislik süresiyle yönetilebilir riskler.

Yerel forwarder iki yolun ortak parçasıdır: A'nın datagram alıcısı ve B'nin OTLP hedefi. Önce bunun infra agent'a
modül olarak eklenmesi, hangi yol seçilirse seçilsin boşa gitmeyecek iştir.

### 7.2 Aşamalı plan

| Aşama | Zaman | İş | Çıkış kriteri |
|---|---|---|---|
| **P0 — ortak temel** | M2, 3–4 hafta (infra ekibi, 1 kişi) | Infra agent'a "PHP forwarder" modülü: unix datagram (A formatı) + localhost OTLP/HTTP alıcı (B için), batch, disk buffer, `host.id` | Forwarder öldürülünce PHP etkilenmiyor; 1000 istek/sn'de ≤ %5 çekirdek |
| **P1 — B preview** | M2 sonu, 6–8 hafta (1 kişi) | Bundle'ın CI'da derlenmesi ve scoping; export → yerel forwarder; `open-telemetry/sdk` olan uygulamada kendini kapatma; PDO span gürültüsünün azaltılması (framework watcher varsa `auto-pdo` kapalı); head sampling ayarı; Guzzle/PSR-18 ile başlık enjeksiyonu; `pecl` tabanlı kurulum dokümanı; B'yi yerel forwarder ile yeniden ölçme | Laravel + Symfony + WordPress örneklerinde verify matrisi yeşil; overhead tablosu yayımlanmış; README'de "yüksek trafikte önerilmez" notu |
| **P2 — A beta (bayrak arkasında)** | M3, 5–6 ay (2 kişi) | Hibrit tasarım (ince C çekirdek + forwarder), 4 framework, PDO/mysqli/phpredis/Predis/curl/Guzzle, dağıtık trace (enjeksiyon), log korelasyonu, CLI/queue, SQL maskeleme; 8.2–8.5 × NTS/ZTS × glibc/musl × amd64/arm64; ASan CI, fuzz, 72 saat soak | Aşağıdaki geçiş kriterleri |
| **P3 — geçiş** | M4 | A varsayılan; B "OTel uyumluluk modu" olarak kalır (kendi OTel SDK'sını kullanan müşteriler için) | — |

**A'yı varsayılan yapma kriterleri:** (1) verify matrisinde B ile eşit ya da daha iyi kapsam (Laravel, Symfony,
WordPress, Slim; PDO, mysqli, Redis, HTTP istemcisi, exception, servisler arası trace); (2) 3 referans uygulamada
§2.6 overhead hedefleri; (3) ASan'lı CI ve fuzz temiz, 72 saat soak'ta 0 çökme ve bellek artışı yok; (4) en az iki
sürüm döngüsü boyunca beta kullanıcılarında 0 segfault raporu; (5) fleet üzerinden kurulum, güncelleme ve otomatik
geri dönüş (§6.3) uçtan uca test edilmiş.

### 7.3 Toplam efor tahmini (ölçüm değil, tahmin)

- P0: 1 kişi × ~1 ay. P1: 1 kişi × ~2 ay. P2: 2 kişi × 5–6 ay. Sonrasında A için sürekli ~1–1.5 FTE.
- Yalnız B ile kalınırsa: P0 + P1 ve sürekli ~0.5 FTE (bundle, upstream takibi, katkılar).

### 7.4 Riskler

| Risk | Yol | Azaltma |
|---|---|---|
| Extension hatası müşteri uygulamasını çökertir | A | Hibrit tasarım (C yüzeyi küçük), ASan/fuzz/soak, kill-switch, kademeli rollout, crash imzasıyla otomatik devre dışı |
| Derleme matrisi ve PHP sürüm takibi ekibi yer | A | Matris CI'nın P2'nin ilk ayında kurulması; sürüm başına tek imzalı release |
| Saturasyonda kapasite kaybı (ölçülen %61) | B | Yerel forwarder, sampling, span sayısını azaltma; yeniden ölçüm |
| Backend kesintisinin uygulamaya yansıması (ölçülen %96) | B | Yerel forwarder zorunlu |
| Uygulamanın kendi Composer bağımlılıklarıyla çakışma (protobuf sınıfları, çift OTel SDK) | B | Scoping istisnaları, çift SDK tespiti, protobuf'suz encoder |
| Upstream contrib'in kapsam/isimlendirme farkları (Redis yok, Symfony route adı, `http.route` başında `/` yok) | B | Backend'de normalizasyon + upstream PR |
| Laravel/Symfony sürümlerinin Packagist güvenlik uyarılarıyla kurulamaması | ikisi | Desteklenen framework sürümlerini upstream destek takvimine bağlamak |

## 8. Ürün sahibine sorular

1. **Hedef kitle ve derinlik:** İlk müşteri profili hangisi? "Laravel/Symfony uygulamasında servis listesi, route
   bazında RED, DB sorguları, hatalar" yeterliyse B uzun süre işi görür. New Relic seviyesinde fonksiyon düzeyi
   segmentler, WordPress hook zamanlaması ve slow SQL explain şartsa A kaçınılmaz.
2. **PHP sürüm tabanı:** Destek matrisi 8.2–8.5 mi olsun? PHP 8.1'in güvenlik desteği bitti, 7.4 hâlâ yaygın.
   7.x istenirse B elenir, A'nın maliyeti de ciddi artar (Observer API yok, `zend_execute_ex` ile hook gerekir).
3. **Kurulum modeli:** Preview için `pecl install` + tek satır ini kabul edilebilir mi, yoksa ilk günden
   ön-derlenmiş `.so` matrisi ve infra agent üzerinden otomatik kurulum mu bekleniyor? İkincisi M2 kapsamını büyütür.
4. **Infra agent'ın rolü:** PHP verisinin yerel forwarder'ı olarak infra agent'a bir modül eklenmesi (unix socket,
   `host.id` ilişkisi, disk buffer) onaylanıyor mu? Bu, `agents/infra` ekibine iş çıkarır. B'de de export'u
   PHP'den çıkarmak için aynı yol kullanılabilir.
5. **Fleet'in PHP'ye dokunması:** Infra agent'ın `php.ini`/`conf.d` yazması ve PHP-FPM'i graceful reload etmesi
   kabul edilebilir mi, yoksa yalnızca "önerilen komutu göster" mi olmalı? Bu, D-027'nin "kendi kendini günceller"
   ilkesinin uygulama süreçlerine genişletilmesi demek.
6. **Ekip:** A için PHP çekirdeği / C deneyimli (ya da Rust + Zend öğrenmeye hazır) 2 kişilik bir ekip M3'te
   ayrılabilir mi? Ayrılamıyorsa öneri B'de kalmak ve upstream OTel'e katkı vermektir.
7. **Upstream katkı:** B'deki eksikleri (phpredis ölçümü, Symfony route şablonu, PDO span gürültüsü) kendi bundle'ımızda
   yamamak mı, OpenTelemetry PHP contrib'e PR olarak göndermek mi tercih edilir? İkincisi yavaş ama bakım yükünü
   azaltır.
8. **Laravel 11:** Brief Laravel 11 istiyordu, spike Laravel 12 ile yapıldı (11.x Packagist güvenlik uyarılarıyla
   kurulamıyor). Laravel 11 desteği ürün hedefi mi, yoksa desteklenen framework sürümleri upstream destek takvimini mi
   izlemeli?
