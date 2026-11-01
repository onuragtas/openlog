# openlog PHP ajan eklentisi (`openlog.so`) {#openlog-php-agent-extension-openlog-so}

Lisans: Apache-2.0. Kontrat: [docs/contracts/php-agent.md](/tr/contracts/php-agent) (wire format v1),
APM semantiği: [docs/contracts/apm.md](/tr/contracts/apm). Kararlar D-035…D-038.

```
PHP worker (openlog.so) --unix datagram, JSON--> openlog-infra-agent (php_forwarder) --OTLP/HTTP--> openlog
```

Eklenti asla ağla konuşmaz, asla bloklamaz ve asla bir isteği başarısız etmez: forwarder soketi yoksa, doluysa veya yavaşsa mesaj
atılır ve sayılır. PHP'de lisans anahtarı veya endpoint yapılandırılmaz.

## Desteklenen runtime'lar {#supported-runtimes}

| PHP | Hook katmanı |
|---|---|
| 7.1 – 7.4 | `zend_execute_ex` (userland) + `zend_execute_internal` (dahili fonksiyonlar), önceki işleyiciler zincirlenir |
| 8.0 – 8.1 | Observer API (userland) + `zend_execute_internal` (8.0/8.1 observer'ı dahili çağrıları görmez) |
| 8.2 – 8.4 | Userland ve dahili fonksiyonlar için Observer API |

NTS ve ZTS, glibc ve musl, amd64 ve arm64 (CI: `php-ext` işi; yerelde `build/run-matrix.sh`). SAPI'ler: PHP-FPM,
Apache mod_php (`apache2handler`), CGI/FastCGI, CLI.

### Test edilen matris (`build/run-matrix.sh`, arm64, 2026-09-13) {#tested-matrix-build-run-matrix-sh-arm64-2026-09-13}

| İmaj | phpt başarılı / başarısız / atlanan | Atlananlar |
|---|---|---|
| `php:7.1-cli` … `php:7.3-cli` | 25 / 0 / 1 | fiber'lar (8.1+) |
| `php:7.4-cli`, `php:8.0-cli` | 24 / 0 / 2 | fiber'lar; pgsql (bullseye arm64 mirror'ında libpq-dev yok) |
| `php:8.1-cli` … `php:8.4-cli` | 26 / 0 / 0 | — |
| `php:8.3-zts` | 26 / 0 / 0 | — |
| `php:7.4-zts` | 15 / 0 / 11 | web SAPI testleri (imajda `php-cgi` yok), fiber'lar, pgsql |
| `php:7.4-cli-alpine` / `php:8.3-cli-alpine` | 25 / 0 / 1 · 26 / 0 / 0 | fiber'lar (7.4) |

Testler (`tests/*.phpt`, her mesajı forwarder'ın kurallarına göre doğrulayan bir alt PHP süreci + unix datagram alıcısı): CLI/web
transaction'ları, traceparent (geçerli, örneklenmemiş, 8 bozuk varyant), örnekleme, fail-open (soket yok, geçersiz transport, hiç
okumayan alıcı), bölme (2001 span), span sınırı, tracer (eşik, hızlı çağrılar, sınırlar, 3000 derinlikte özyineleme), generator'lar,
fiber'lar, hatalar (yakalanmamış, işleyici, fatal, 4 KiB kırpma, exit), PDO, SQL sanitize, mysqli, pgsql, phpredis, Predis, curl,
curl_multi, stream'ler (context geri yükleme ve opcache dahil), Laravel, Symfony, Slim, WordPress, CodeIgniter, Yii (gerçek adlara
sahip stub sınıflar), süreç başına birden çok istek (php-cgi -T), log korelasyonu. Gerçek framework sürümleri `agents/php/demo` ile
sınanır.

### Ek yük (2026-09-14, PHP 8.3 NTS, başka iş yükleriyle paylaşılan arm64 VM; D-057) {#overhead-2026-09-14-php-8-3-nts-arm64-vm-shared-with-other-workloads-d-057}

`before` = D-057 öncesi eklenti kaynağı, `after` = güncel hali; `off` = tracer kapalı, `on` = varsayılanlar,
`lean` = `openlog.userland_hooks=0` (+ tracer kapalı).

**İstek başına komut sayısı** (callgrind, `bench/ext/micro/run.sh`, `php-cgi -T`, deterministik):

| Uygulama | base | before off | after off | before on | after on | lean |
|---|---|---|---|---|---|---|
| düz PHP (57 PDO SQLite statement'ı, ~2k çağrı) | 2.59 M | +1.12 M (+43 %) | +0.58 M (+23 %) | +1.16 M | +0.63 M | +0.35 M (+13 %) |
| Laravel 12 `GET /health` (~18k çağrı) | 7.26 M | +1.78 M (+24.5 %) | +1.79 M | +1.80 M | +1.81 M | +0.27 M (+3.7 %) |

Laravel maliyetinin kaynağı: motorun Observer API'si. Bir fcall observer kaydedildiğinde, gözlemlemediği fonksiyonlar için bile, 8.0–8.3'te
userland *ve* dahili çağrı başına ~100 komut (8.4: 50–65), artı istek başına `zend_init_internal_run_time_cache` (8.2/8.3). Eklenti bunu
ancak observer'ı kaydetmeyerek (lean modu) kaldırabilir. Kendi kodumuz yalnızca span yoğun isteklerde asıl maliyetti (JSON kaçışlama,
SQL sanitize, UTF-8 temizleme); o kısım yarıya indi.

**HTTP** (`bench/ext/http/run.sh`: 4 sabitlenmiş CPU üzerinde PHP-FPM 8 statik worker, nginx, wrk 16 bağlantı, forwarder yerine
datagram sink, 15 sn'lik 5 iç içe tur). Medyanlar, p25–p75 (min–max):

| Laravel `GET /bench/{id}` (Eloquent + 2 Redis) | RPS | PHP CPU µs/istek | base'e göre Δ CPU |
|---|---|---|---|
| base | 1068 (1002–1172, 946–1218) | 2909 (2899–2923) | — |
| before, tracer kapalı | 826 (810–868) | 3364 (3161–3562) | +455 µs (+15.6 %) |
| before, tracer açık | 898 (752–902) | 3348 (3323–3510) | +439 µs (+15.1 %) |
| after, tracer kapalı | 893 (879–1005) | 3309 (3191–3375) | +400 µs (+13.8 %) |
| after, tracer açık | 1044 (986–1118) | 3106 (3042–3229) | +197 µs (+6.8 %) |
| after, lean | 1008 (988–1062) | 3032 (3007–3137) | +123 µs (+4.2 %) |

| düz PHP (57 statement; daha önceki çalıştırma, örnekleyici warm-up değişikliğinden önce) | RPS | PHP CPU µs/istek | Δ CPU |
|---|---|---|---|
| base | 10428 (10258–10907) | 295 (289–303) | — |
| before off / on | 7880 / 7482 | 431 / 463 | +136 / +168 µs |
| after off / on / lean | 7652 / 7610 / 8160 | 400 / 409 / 394 | +105 / +114 / +99 µs |

**Bütçe (tracer kapalı ≤ %3 / açık ≤ %7) tam modda hâlâ tutturulamıyor.** Bu VM'de turlar arası RPS farkı ±%10–20 (makineyi başka
ajanların container'ları yüklüyordu), bu yüzden ~%10'un altındaki RPS farkları anlamlı değil; istek başına PHP CPU kararlı sinyaldir.
Laravel çalıştırmasında "after, on"ın "after, off"tan düşük CPU ölçmesi bu gürültünün içindedir (komut sayıları aynı). Lean modu komut
sayısı (+%3.7) ve CPU (+%4.2) açısından bütçe içinde ama framework hook'larından vazgeçer. RPS bütçesini doğrulamak için hâlâ
ayrılmış bir benchmark makinesi gerekiyor.

**Soak** (`bench/ext/soak/run.sh`, 10 dk, recycle olmadan 4 FPM worker, tracer açık, eşik 50 ms, exception'lar ve yavaş trace'ler dahil
Laravel karışımı + düz uygulama): 332 k istek, değiştirilen worker yok, FPM logunda sinyal yok, ilk çeyrekten sonra worker başına RSS
−376…−420 KiB (büyüme yok): PASS.

## Derleme ve kurulum {#build-and-install}

Paketler (D-059, kontrat §7): `openlog-php-agent_<v>_linux_<arch>.deb|.rpm|.apk`, her PHP ABI'si için `openlog.so` içerir
(7.1–8.4 × NTS/ZTS × glibc/musl; glibc modülleri glibc ≥ 2.28 ister, yani RHEL/Alma/Rocky 8+, Debian 10+, Ubuntu 20.04+) ve bulunan her
PHP runtime'ı için etkinleştirir (`php`, `php-cgi`, `php-fpm`; Debian `mods-available`, RHEL/Remi, Alpine, `/usr/local`). PHP-FPM / Apache
yeniden başlatılmaz; reload et veya:

```sh
openlog-php-install status            # runtime'lar, ABI, etkin/yüklü (araçlar için --json)
openlog-php-install install --reload  # (yeniden) etkinleştir ve php-fpm/apache unit'lerini reload et
openlog-php-install uninstall
```

`openlog-php-agent_<v>_linux_<arch>.tar.gz` tarball'ı, paket yöneticisi olmayan makineler veya imajlar için aynı ağacı içerir
(`modules/<api>-<nts|zts>-<libc>/openlog.so`, `bin/openlog-php-install`); sürüm derlemeleri:
`agents/php/packaging/build-artifacts.sh <version> <out>` (paket kurulum testi: `agents/php/packaging/test.sh`).
Altyapı ajanı üzerinden fleet kurulumu kontratta (§7.3) tanımlanır.

Kaynaktan:

```sh
cd agents/php/ext
phpize
./configure --enable-openlog
make -j"$(nproc)"
make install            # veya modules/openlog.so dosyasını `php-config --extension-dir` içine kopyala
echo 'extension=openlog.so' > "$(php-config --ini-dir 2>/dev/null || echo /usr/local/etc/php/conf.d)/90-openlog.ini"
php -m | grep openlog
```

Docker (resmi imajlar): derlemeyi imajına ekle, örneğin:

```dockerfile
FROM php:8.3-fpm
COPY agents/php/ext /usr/src/openlog-ext
RUN cd /usr/src/openlog-ext && phpize && ./configure --enable-openlog && make -j"$(nproc)" && make install \
 && echo 'extension=openlog.so' > /usr/local/etc/php/conf.d/90-openlog.ini
```

Forwarder, altyapı ajanının `php_forwarder` modülüdür (`agents/infra`, PHP bulunduğunda otomatik etkinleşir). Container'larda
`/run/openlog-infra-agent` dizinini (volume) PHP container'ı ile ajan arasında paylaş veya UDP yedeğini kullan
(`openlog.transport=udp://127.0.0.1:18127`).

Geliştirme yardımcıları (kaynağı geçici bir dizine kopyalar, resmi imajın içinde derler):

| Script | Ne yapar |
|---|---|
| `build/dev-build.sh 8.3` | yalnızca derler (`8.3`, `7.4-zts`, `8.3-cli-alpine`, …) |
| `build/test-one.sh 8.3 [tests/…]` | derler + phpt (veri deposu testleri atlanır) |
| `build/run-matrix.sh` | tam matris: 7.1…8.4 NTS glibc + 7.4/8.3 ZTS ve musl, MariaDB/PostgreSQL/Redis container'larıyla (ağ `openlog-php-test`) |
| `VERSIONS="" EXTRA="8.3-asan" build/run-matrix.sh` | kaynaktan derlenmiş AddressSanitizer + UBSan debug PHP (`build/Dockerfile.asan`), aynı sanitizer'larla openlog.so; herhangi bir sanitizer raporu başarısız sayılır (`make -C agents/php ext-asan`) |
| `VERSIONS="" EXTRA="8.3-xdebug 8.3-ddtrace 8.3-newrelic 8.3-jit 8.3-swoole" build/run-matrix.sh` | başka bir ajan / debugger / JIT / Swoole yüklüyken tam test paketi (`build/Dockerfile.compat`, pecl, GitHub, download.newrelic.com üzerinden indirir) |
| `VERSIONS="" EXTRA="8.4-frankenphp" build/run-matrix.sh` | resmi FrankenPHP imajında (ZTS PHP 8.4) tam test paketi, worker ve klasik modda gerçek bir FrankenPHP sunucusu dahil (`tests/044-frankenphp.phpt`) |
| `fuzz/run.sh` | `src/ol_text.c` üzerinde libFuzzer + ASan/UBSan (`FUZZ_SECONDS`, varsayılan 60) |
| `../bench/ext/micro/run.sh` | istek başına maliyet: `php-cgi -T` zamanlaması + callgrind komut sayısı, düz PHP ve Laravel; A/B için `SRC_A=<older ext>` |
| `../bench/ext/http/run.sh` | PHP-FPM + nginx + wrk: RPS, gecikme, istek başına PHP CPU; base / off / on / lean (+ A/B) |
| `../bench/ext/soak/run.sh` | uzun ömürlü 4 FPM worker üzerinde `MINUTES` süre karışık yük; worker RSS büyümesinde veya crash'te başarısız olur |
| `../packaging/build-artifacts.sh` | makinenin mimarisi için sürüm tarball'ı + deb/rpm/apk |

## Ayarlar (`php.ini`) {#settings-php-ini}

| Ayar | Varsayılan | Kapsam | Notlar |
|---|---|---|---|
| `openlog.enabled` | `1` | system | `0`: hiçbir hook kurulmaz |
| `openlog.service_name` | boş → `php-app` (web SAPI'leri) / `php-cli` (CLI) | all | `OPENLOG_SERVICE_NAME` ortam değişkeni geçersiz kılar (FPM havuzu `env[OPENLOG_SERVICE_NAME]`) |
| `openlog.service_namespace`, `openlog.service_version`, `openlog.environment` | boş | all | `OPENLOG_SERVICE_NAMESPACE`, `OPENLOG_SERVICE_VERSION`, `OPENLOG_ENVIRONMENT` ortam değişkenleri geçersiz kılar |
| `openlog.transport` | `unix:///run/openlog-infra-agent/php.sock` | system | `unix:///path` veya `udp://127.0.0.1:18127` (sayısal adres) |
| `openlog.sampling_ratio` | `1.0` | all | head sampling; gelen örneklenmiş bir `traceparent` her zaman kaydedilir, örneklenmemiş olan asla |
| `openlog.capture_query_text` | `sanitized` | all | `raw`, `off` |
| `openlog.transaction_tracer.enabled` | `1` | system, perdir | fonksiyon düzeyinde segmentler |
| `openlog.transaction_tracer.threshold_ms` | `10` | all | transaction en az bu kadar sürdüğünde veya başarısız olduğunda segmentler gönderilir |
| `openlog.transaction_tracer.max_segments` | `2000` | all | |
| `openlog.transaction_tracer.min_segment_ms` | `1` | all | warm-up sonrası stack örnekleme aralığı; bundan kısa çağrılar yalnızca bir örnek denk gelirse görünür |
| `openlog.transaction_tracer.warmup_ms` | `100` | all | istek başında daha seyrek warm-up aralığının ne kadar süre kullanılacağı; `0` warm-up'ı kapatır (ilk komuttan itibaren her `min_segment_ms`'de örnekler). Düşürmek kısa isteklerin (10–100 ms) daha çok fonksiyon segmenti göstermesini sağlar, CPU maliyeti artar |
| `openlog.transaction_tracer.warmup_segment_ms` | `10` | all | warm-up penceresindeki örnekleme aralığı; asla `min_segment_ms`'den sık olmaz (altındaki değerler ve `0`, warm-up yok demektir). Düşürmenin bedeli `warmup_ms` ile aynıdır |
| `openlog.transaction_tracer.max_memory_kb` | `4096` | all | |
| `openlog.log_level` | `warning` | all | `off`, `error`, `warning`, `info`, `debug`; PHP hata logu, süreç başına dakikada en fazla 10 satır |
| `openlog.userland_hooks` | `1` | system | `0` = lean modu: fcall observer (PHP 8) / `zend_execute_ex` override (7.x) yok, böylece motorun çağrı başına observer maliyeti kalkar; yalnızca dahili fonksiyonlar enstrümante edilir (PDO, mysqli, pgsql, phpredis, curl, stream'ler, sleep). Kaybedilenler: framework route adları, framework'ün raporladığı exception'lar, Predis, uzun ömürlü worker transaction'ları; yakalanmamış exception'lar fatal error'dan kaydedilir. Bkz. "Ek yük" |

PHP fonksiyonları (log korelasyonu, özel adlandırma): `openlog\trace_id(): string`, `openlog\span_id(): string`,
`openlog\traceparent(): string`, `openlog\set_transaction_name(string $name): bool`, `openlog\is_sampled(): bool`,
`openlog\stats(): array` (süreç sayaçları: requests, messages, datagrams, send_errors, dropped_messages,
dropped_spans, failopen). `phpinfo()` hook katmanını ve sayaçları gösterir.

## Neler kaydedilir {#what-is-recorded}

**Transaction** (kök span): web istekleri kind 2'dir (`http.request.method`, `url.path`, `url.scheme`, `server.address`, `server.port`,
`client.address`, `user_agent.original`, `network.protocol.version`, `http.response.status_code`, `http.route`); CLI script'leri
kind 1'dir, `php <script>` olarak adlandırılır, `openlog.php.cli=true` taşır (0 değilse `process.exit.code` da). Ad `METHOD route`
biçimindedir, route aşağıdakilerden ilk bulunandır:

| Öncelik | Kaynak |
|---|---|
| 1000 | `openlog\set_transaction_name()` |
| 50 | Laravel `Router::runRoute` → `Route::$uri` (`/users/{id}/orders`); Symfony `ControllerResolver::getController` → `_route_params`'tan yeniden kurulan path şablonu (`/api/products/{id}`, route adı `openlog.php.route_name` içinde); Slim 3 `Slim\Route::run` / Slim 4 `Slim\Routing\Route::run` → `$pattern`; CodeIgniter 3 `CI_Router::_set_routing` → `/dir/class/method`; CodeIgniter 4 `Router::handle` → `matchedRoute` pattern'i; Yii 2 `yii\web\Application::runAction` → `/controller/action` |
| 45 / 40 | WordPress REST: `WP_REST_Server::match_request_to_handler` route regex'i (`/wp-json/wp/v2/posts/{id}`), yoksa normalize edilmiş REST path'i |
| 30 | WordPress şablonu: `apply_filters('template_include')` → `template/single.php` |
| — | düz PHP: apm.md §2.2'ye göre normalize edilmiş path (`/orders/{id}/items/{hex}`), `http.route` yok |

**Veri depoları** (kind 3): PDO (`exec`, `query`, `PDOStatement::execute`; `prepare` yalnızca başarısız olduğunda; DSN →
`server.address`, `server.port`, `db.namespace`), prosedürel ve OO mysqli (`query`, `real_query`, `multi_query`, `execute_query`, prepared
statement'lar; bağlantı attribute'ları `mysqli_connect`/`new mysqli`/`real_connect`, `select_db` üzerinden), pgsql (`pg_query`,
`pg_query_params`, `pg_prepare` + `pg_execute`, varsayılan bağlantı), phpredis (bağlantı/seçenek yardımcıları dışındaki her `Redis`
komutu; `select`, `db.namespace` değerini günceller), Predis (`Client::executeCommand`). Attribute'lar: `db.system.name` (`mysql`,
`postgresql`, `sqlite`, `redis`, …), `db.operation.name`, `db.collection.name`, `db.query.text`; ad `OPERATION table`. Sanitize işlemi
string, sayısal ve hex literal'leri `?` ile değiştirir, yorumları siler ve boşlukları daraltır; placeholder'lar kalır. Redis argümanları
`raw` olmadıkça `?` olur.

**Giden HTTP** (kind 3): `curl_exec`, `curl_multi_add_handle` … `curl_multi_remove_handle`, `http(s)://` üzerinde `file_get_contents` /
`fopen`. Guzzle (sync `CurlHandler`, `CurlMultiHandler`, `StreamHandler`), Laravel `Http::` ve Symfony HttpClient bunlar üzerinden
kapsanır; Guzzle 7 async isteklerde (promise'ler, `Pool`) her istek, `curl_multi_add_handle`'dan `curl_multi_remove_handle`'a kadar süren
ve kendi `traceparent`'ını taşıyan tek bir client span'idir (`tests/043-guzzle-async.phpt`, gerçek kütüphane: `build/test-guzzle.sh`).
`traceparent` (+ `tracestate`) enjekte edilir, örneklenmemiş isteklerde de (bayraklar `00`), meğer ki uygulama zaten bir `traceparent`
header'ı ayarlamış olsun. Attribute'lar: `http.request.method`, `url.full` (kimlik bilgileri çıkarılır), `server.address`, `server.port`,
`http.response.status_code`, `error.type`.

**Hatalar**: yakalanmamış exception'lar (`set_exception_handler` ile işlense de) ve fatal error'lar → kök `status=2` + `exception` event'i
(`exception.type`, `exception.message`, PHP trace biçiminde `exception.stacktrace`). Yakalanan exception'lar yalnızca Laravel
(`Handler::report`/`reportThrowable`, dahili dont-report listesine uyarak) veya Symfony (`HttpKernel::handleThrowable`, 500'ün altındaki
HTTP exception'ları yok sayılır) raporladığında. Enstrümante edilen çağrıların içindeki exception'lar client span'ini işaretler.

**Transaction tracer** (stack örnekleme): PHP süreci başına bir örnekleyici thread (istekler arasında boşta) her `min_segment_ms`'de
`EG(vm_interrupt)` ayarlar. Bir isteğin ilk `warmup_ms` süresinde (varsayılan 100 ms) bunun yerine her `warmup_segment_ms`'de
(varsayılan 10 ms) ayarlar, çünkü fonksiyon trace'leri yalnızca yavaş veya başarısız istekler için gönderilir ve milisaniyede bir uyanmak
hızlı isteklere ölçülebilir bir maliyet getiriyordu (7 ms'lik bir Laravel isteğinde ~150 µs PHP CPU). Bir sonraki güvenli noktada userland
çağrı stack'i gezilir ve bir segment ağacında birleştirilir (kind 1, `Class::method`, `code.function.name`, `code.namespace`,
`code.file.path`, `code.line.number`, `openlog.php.segment=function`, `openlog.php.samples`). Enstrümante edilen her span başında ve
sonunda bir örnek alır, böylece bloklayan DB/HTTP/sleep süresi doğru fonksiyonlara yazılır ve span'in parent'ı en içteki fonksiyon
segmenti olur. `sleep`/`usleep`/`time_nanosleep` dahili span olarak görünür. Çağrı başına maliyet yoktur (her çağrıyı gözlemlemek
~60 ns/çağrı, framework isteği başına > 1 ms ölçüldü). Aralıktan kısa çağrılar yalnızca bir örnek denk gelirse görünür; aynı frame'den
aynı fonksiyonun arada örnek olmayan ardışık çağrıları tek segment oluşturur; derinlik en dıştaki 256 frame ile sınırlıdır. Segmentler
yalnızca transaction yavaş veya başarısız olduğunda gönderilir. Bu yüzden kısa istekler (10–100 ms) varsayılan olarak az fonksiyon
segmenti gösterir; `warmup_ms=0` (veya daha küçük bir `warmup_segment_ms`) onları baştan itibaren `min_segment_ms` ile örnekler ve çok
daha fazlasını gösterir, karşılığında istek başına CPU maliyeti artar.

**Uzun ömürlü worker'lar** (D-058, `src/inst_workers.c`): istek başına bir transaction, web isteği gibi adlandırılır ve attribute alır;
şunlar için: Laravel Octane (`Laravel\Octane\Worker::handle`; status `SwooleClient`/`RoadRunnerClient::respond`'dan), RoadRunner
(`Spiral\RoadRunner\Http\HttpWorker::waitRequest` … `respond`/`respondStream`; PSR7Worker ve RoadRunner üzerinde Octane'i kapsar),
Swoole/OpenSwoole HTTP sunucuları (`Server::on('request', …)` ile kaydedilen callable veya `Coroutine\Http\Server::handle()` …
`Response::end`/`redirect`/`sendfile`, status `Response::status`'tan) ve FrankenPHP worker modu (`frankenphp_handle_request()`'e geçirilen
callable isteğin kendisidir: çağrı → dönüş; istek attribute'ları FrankenPHP'nin istek başına sıfırladığı `$_SERVER`'dan, status callback
döndüğünde `http_response_code()`'dan, `display_errors` kapalıyken kaçan bir exception için 500, yani FrankenPHP'nin verdiği cevap, ve
exception event'i; istek içinde `exit()` ve N istekten sonra worker yeniden başlatmaları çalışır; worker script'inin açılışı ve beklemesi
asla gönderilmez, `FRANKENPHP_WORKER` üzerinden algılanır). FrankenPHP klasik modu özel bir şey gerektirmez (normal SAPI isteği,
`sapi_module.name` `frankenphp`). Worker sürecinin kendi CLI transaction'ı ilk istekte atılır, istekler arasında hiçbir şey kaydedilmez
veya taşınmaz, bağlantı attribute'ları (DSN, host, veritabanı) istekler boyunca korunur. Bir süreçte istekler çakıştığında (Swoole
coroutine'leri) asla karıştırılmazlar: birden fazlası sürerken child span'ler, route adları ve fonksiyon tracer'ı hepsi için durur ve her
istek yalnızca kök span'iyle kaydedilir (`openlog.php.concurrent=true`); giden `traceparent` header'ları ve `openlog\traceparent()`,
handler'ı geçerli coroutine'in stack'inde olan isteği kullanır, öyle bir istek yoksa hiçbir şey kullanmaz. Kapsam dışı: lean modu
(istek callable'ları gözlemlenen userland fonksiyonlarıdır), `frankenphp_handle_request()` / `Server::on()`'a geçirilmeden önce zaten
çalışmış bir istek callable'ı. Testler: `tests/041-workers.phpt` (Octane ve RoadRunner sınıfları), `tests/042-swoole.phpt` (gerçek Swoole
sunucusu, 4 eşzamanlı istek; imaj `8.3-swoole`), `tests/044-frankenphp.phpt` (gerçek FrankenPHP 1.x sunucusu, yeniden başlatma ve `exit()`
ile worker modu, klasik mod; tag `8.4-frankenphp`, resmi `dunglas/frankenphp` imajı).

## Mimari {#architecture}

- `src/ol_hooks.c`: MINIT'te kurulan hook kaydı (küçük harfli `class::method`, `class::*`, `function`); arama sonucu her `zend_function`
  için `reserved[]` slotunda tamsayı olarak önbelleğe alınır, böylece ilk çağrıdan sonra dağıtım hash'lemeden O(1) olur
  (`opcache.protect_memory=1` iken hash'e düşer). Yukarıdaki tablodaki gibi iki backend.
- `src/ol_core.c`: istek başına durum: sabit parçalarda node tablosu (≤ 4096 span + tracer segmenti), frame stack'i (fiber başına ayrı
  stack'ler), sınırlı arena (64 KiB parçalar; taban 8 MiB + `max_memory_kb`), istekler arasında korunur. İşleyiciler yalnızca motor
  verisini okur (argümanlar, `$this`, `__get` olmadan property'ler); ajanın çağırdığı PHP fonksiyonları yalnızca
  `curl_setopt`/`curl_getinfo`/`curl_errno`/`curl_error` ve Predis `getId()`'dir, exception'lar bastırılır.
- `src/ol_text.c`: her span'in yolundaki PHP'den bağımsız metin kodu: JSON yazıcı, UTF-8 temizleme/kırpma, SQL sanitize ve operasyon
  çıkarma, path/route normalizasyonu, `traceparent` ayrıştırma (tablo tabanlı karakter sınıfları, 8 baytlık taramalar; `fuzz/run.sh` ile
  fuzz edilir). Sorgu analizi istek başına sorgu metnine göre önbelleğe alınır (`ol_util.c`).
- `src/inst_workers.c`: uzun ömürlü worker'ların transaction'ları (Octane, RoadRunner, Swoole) ve eşzamanlılık kuralları.
- `src/ol_json.c`: mesaj kodlama; 60 000 bayttan büyük mesajlar bölünür (aynı `pid` + `trace_id`, `seq` 0…255, son parçada `last`,
  `resource`/`sampling_ratio`/`function_trace` tekrarlanır). Her string UTF-8 temizlenmiş ve ≤ 4 KiB, span başına ≤ 64 attribute. Bağlı
  olmayan bir soket üzerinde `sendto(MSG_DONTWAIT)`; `EAGAIN` durumunda bölünmüş bir mesaj en fazla ~2 ms yeniden deneyebilir (kernel'in
  datagram kuyruğu kısadır), asla daha fazla değil.
- `src/ol_context.c`: W3C `traceparent`/`tracestate` (katı ayrıştırma; bozuksa → yeni trace), örnekleme (oran < 1 iken `tracestate`'e
  `ot=th:` eşiği eklenir), kök span, `container.id`.
- Fail-open: dahili bir tutarsızlık, isteğin geri kalanı için enstrümantasyonu kapatır ve bir kez loglar (hız sınırlı); gönderim hataları
  sayılır ve bir sonraki mesajın kök span'inde raporlanır (`openlog.php.send_errors`, `openlog.php.dropped_messages`).

## Diğer eklentilerle uyumluluk {#compatibility-with-other-extensions}

- **Xdebug, New Relic, Datadog, Blackfire, ext-opentelemetry**: PHP 8'de her Observer bağımsızdır. PHP 7.x'te ve 8.0/8.1'de dahili
  fonksiyonlar için eklenti `zend_execute_ex` / `zend_execute_internal` / `zend_error_cb` yerine geçer ve önceden kurulu işleyiciyi her
  zaman çağırır, böylece sonra yüklenen eklenti önceki eklentiyi sarar. Aynı anda iki APM ajanı çalıştırmak ek yükü ikiye katlar ve ikisi
  de curl'e `traceparent` enjekte eder; ikinci ajan birincinin header'ını görür ve korur. Production'da önerilmez.
- **Opcache / JIT**: desteklenir. 7.x'te `zend_execute_ex` override'ı her userland çağrısını C'de özyinelemeli yapar (Xdebug/New Relic ile
  aynı); çok derin özyineleme daha büyük bir C stack'i gerektirir.

Birlikte test edilenler (diğer eklenti önce yüklenmiş halde tam phpt paketi, PHP 8.3 NTS, arm64, 2026-09-14; `build/run-matrix.sh`
tag'leri `8.3-xdebug`, `-ddtrace`, `-newrelic`, `-jit`, `-swoole`):

| Birlikte yüklenen | phpt | Notlar |
|---|---|---|
| Xdebug 3.4 `xdebug.mode=coverage` | 28 / 0 / 1 | `develop,coverage`: 23 / 6. Altısı da Xdebug davranışıdır, crash yok, openlog verisi değişmez: `var_dump()` bir `file:line` öneki alır (5 test) ve `xdebug.max_nesting_level=512`, 3000 derinlikteki özyineleme testini keser |
| Datadog ddtrace (en son `datadog-setup.php`, CLI tracing açık) | 26 / 2 / 1 | ddtrace, openlog'un curl'e enjekte ettiği `traceparent` header'ını değiştirir (son tracer kazanır); ddtrace `Predis\Client::executeCommand`'ı yeniden dağıtır, bu yüzden openlog iç içe tekrarlanan Predis span'leri kaydeder. Test sırasında düzeltildi: ddtrace'in closure'ları (`ZEND_ACC_CLOSURE` olmadan) `redis::*` ile eşleşiyordu |
| New Relic PHP ajanı (en son, daemon çalışmıyor) | 28 / 0 / 1 | — |
| OPcache JIT (`opcache.jit=tracing`, CLI ve CGI) | 28 / 0 / 1 | — |
| Swoole 6 | 3 / 0 / 0 (worker, lean, Swoole testleri) | `tests/042-swoole.phpt`: 4 eşzamanlı coroutine isteği |

## Sağlamlık {#robustness}

- **AddressSanitizer + UBSan** (`VERSIONS="" EXTRA="8.3-asan" build/run-matrix.sh`, CI işi `php-ext (8.3-asan)`): `--enable-address-sanitizer --enable-undefined-sanitizer` ile
  kaynaktan derlenmiş PHP 8.3 debug, aynı flag'lerle derlenmiş openlog.so ve phpredis, `USE_ZEND_ALLOC=0`,
  sızıntı algılama açık; raporlar dosyalara yazılır ve çalıştırmayı başarısız yapar. Sonuç: 28 başarılı / 0 başarısız / 1 atlanan (Swoole),
  0 sanitizer raporu (imaj derlemesi ~3 dk).
- **Fuzzing** (`fuzz/run.sh`, CI işi `php-ext-fuzz`, 120 sn): `src/ol_text.c` üzerinde libFuzzer + ASan/UBSan: UTF-8 temizleme/kırpma, JSON
  string yazıcı (çözülen çıktı girdiye eşit olmalı; kapasitesi çok küçük bir yazıcı kendi tamponunun içinde durmalı), sayılar, SQL sanitize
  ve operasyon çıkarma, path/route normalizasyonu, traceparent ayrıştırma/biçimlendirme gidiş dönüşü. 60 sn'lik yerel çalıştırma: 6.7 M
  çalıştırma, 12 405 corpus birimi, bulgu yok.
- **Soak** (`../bench/ext/soak/run.sh`): bkz. "Ek yük".
- **Swoole / RoadRunner / Octane / FrankenPHP worker modu**: işlenen istek başına bir transaction, bkz. "Uzun ömürlü worker'lar".

## Sorun giderme {#troubleshooting}

| Belirti | Kontrol et |
|---|---|
| Veri yok | `php -m \| grep openlog`; `php -i \| grep openlog`; soket var ve PHP kullanıcısı yazabiliyor (`ls -l /run/openlog-infra-agent/php.sock`); `openlog\stats()['send_errors']` |
| `send_errors` artıyor | forwarder çalışmıyor / yol yanlış / izinler (soket grubu, `php_forwarder.socket_mode`); container'lar soket dizinini paylaşmalı |
| Yanlış transaction adları | framework algılanmadı: `openlog\set_transaction_name()` kullan; düz PHP normalize edilmiş path'ler kullanır |
| Trace'ler kesiliyor | kök span'de `dropped_spans`: > 4096 span, `max_segments` veya `max_memory_kb` sınırına ulaşıldı |
| Fonksiyon segmenti yok | istek `threshold_ms`'den hızlı ve başarısız olmadı; tracer kapalı; istek örneklenmemiş |
| Ek yük endişesi | `openlog.transaction_tracer.enabled=0`, daha düşük `openlog.sampling_ratio` |
| Crash şüphesi | `openlog.enabled=0` eklentiyi kaldırmadan tüm hook'ları kapatır |
