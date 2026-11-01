# Sözleşme: PHP ajanı ↔ altyapı ajanı forwarder'ı (v1) {#contract-php-agent-↔-infra-agent-forwarder-v1}

Kararlar: D-035 (kendi C eklentisi), D-036 (PHP 7.1+), D-037 (fonksiyon düzeyinde trace'ler dahil derinlik), D-038 (forwarder
altyapı ajanında). Arka plan ve ölçümler: `agents/php/docs/decision.md`. APM semantiği (transaction'lar, adlandırma, DB
normalizasyonu, hatalar, örnekleme): [apm.md](/tr/contracts/apm).

```
PHP worker (openlog.so) --unix datagram, JSON--> openlog-infra-agent (php_forwarder module) --OTLP/HTTP--> ingest
```

Eklenti asla ağla konuşmaz, asla bloklamaz ve isteği asla başarısız yapmaz: socket yoksa, doluysa veya
yavaşsa mesaj atılır ve sayılır.

## 1. Taşıma {#_1-transport}

- **Unix datagram socket** (`SOCK_DGRAM`), varsayılan yol `/run/openlog-infra-agent/php.sock`.
  - Forwarder oluşturur (dizin `0755`; socket modu `0660`, grup `php_forwarder.socket_group`'tan, varsayılan
    **`openlog-php`**; `0666` yalnızca açıkça yapılandırılırsa). Yalnızca `openlog-php` yoksa (ayrıcalıklı
    adım hiç çalışmadıysa: container, dev build) forwarder `www-data`, `nginx`,
    `apache`, `php-fpm` içinden var olan ilkine döner ve yedek grubun neden uygulanamadığını info seviyesinde bir kez loglar.
  - **Socket grubu `openlog-php` (D-103).** Tek amacı `php.sock`'a yazma izni olan özel bir sistem grubu.
    Bilerek `openlog-agent` grubu **değildir**: `/etc/openlog-infra-agent/config.yaml`
    `root:openlog-agent 0640`'tır ve lisans anahtarını tutar, yani PHP kodu o gruba asla girmemeli. Ayrıcalıklı
    reconcile adımı (`openlog-infra-agent -reconcile`: her servis başlangıcında unit pre-start adımı, deb/rpm postinstall,
    `install.sh`; releases-updates.md §3) grubu oluşturur ve `openlog-agent`'ı ona ekler (böylece ajan socket'i `chown` edebilir;
    pre-start adımıyla üyelik hemen ardından başlayan ajana uygulanır).
  - **Otomatik izinler.** Hosting panelleri her sitenin PHP-FPM pool'unu kendi kullanıcısıyla çalıştırır (HestiaCP, cPanel, Plesk,
    ISPConfig), bu yüzden aynı adım, devre dışı bırakılmadıkça şunları ekler:
    - pool dosyalarındaki bir `[pool]` bölümünün her `user =` değeri (`[global]` değil; `;`/`#` yorumları, tırnaklar ve `$pool` işlenir; `include`
      direktifleri izlenmez; başka değişken içeren değerler atlanır); pool dosyaları:
      `/etc/php/*/fpm/pool.d/*.conf` (Debian/Ubuntu, HestiaCP), `/etc/php-fpm.d/*.conf` (RHEL),
      `/etc/opt/remi/php*/php-fpm.d/*.conf` (Remi), `/opt/cpanel/ea-php*/root/etc/php-fpm.d/*.conf` (cPanel),
      `/opt/plesk/php/*/etc/php-fpm.d/*.conf` (Plesk) ve `/etc/php*/php-fpm.d/*.conf` (Alpine);
    - Apache veya nginx unit'i ya da süreci varsa `www-data`, `apache` ve `nginx` (mod_php).

    Yalnızca var olan yerel hesaplar (`/etc/passwd`) eklenir, asla root veya uid 0, asla yeniden ajan kullanıcısı; üyeler
    asla çıkarılmaz. Sonra yalnızca pool kullanıcıları yeni eklenen PHP-FPM servisleri ve kullanıcısı eklendiyse
    Apache, aktifse nazik bir `systemctl reload` alır (`php<v>-fpm`, `php-fpm`, `php<NN>-php-fpm`,
    `ea-php<NN>-php-fpm`, `plesk-php<NN>-fpm`, `apache2`/`httpd`; pool dizininden türetilir; asla restart değil). Reload
    yeterlidir: FPM master kendini yeniden çalıştırır ve pool kullanıcısı için `initgroups` çağıran yeni worker'lar fork eder.
  - Sonradan oluşturulan **yeni siteler** bir sonraki servis başlangıcında (`systemctl restart openlog-infra-agent`) veya
    elle izin alır: `usermod -aG openlog-php <user> && systemctl reload php<v>-fpm`. Ayrıcalıksız ajan grupları
    değiştiremez; her pool'u ve erişimini agent sync'te bildirir (`php_access`, releases-updates.md §3), pool'lar gönderemiyorsa
    uyarı loglar ve host'un Services sekmesi bu pool'ları iki düzeltmeyle birlikte gösterir.
  - **Devre dışı bırakma:** `php_forwarder.grant_pool_users: false` (yalnızca root'a ait yapılandırmada dikkate alınır),
    `/etc/openlog-infra-agent/no-php-access` dosyası veya kurulumda `OPENLOG_AGENT_PHP_ACCESS=0` / `install.sh --no-php-access`
    (o dosyaya kaydedilir). Grup ve ajanın üyeliği yine de kurulur, yani elle izinler çalışır.
  - Güvenlik: `openlog-php` üyeleri ajana span gönderebilir (host için APM verisi enjekte edebilirler), başka bir şey
    yapamaz; bu, host'un PHP worker'larının zaten ihtiyaç duyduğu güvenin aynısıdır.
  - Eklenti bağlı olmayan bir socket'te `sendto(..., MSG_DONTWAIT | MSG_NOSIGNAL)` ile gönderir, böylece yeniden başlatılan forwarder
    (yeni inode) yeniden bağlanmadan alınır.
- **Datagram boyutu:** en fazla **60 000 byte**. Daha büyük mesajlar bölünür (§2.3).
- Socket dosyasını paylaşamayan container'lar için isteğe bağlı yedek: UDP `127.0.0.1:18127`
  (`openlog.transport=udp://127.0.0.1:18127` / `php_forwarder.udp_listen`). Aynı payload.

## 2. Mesaj {#_2-message}

Datagram başına bir JSON nesnesi (UTF-8); tüm ID'ler küçük harf hex; zamanlar unix nanosaniye (tamsayı).

```json
{
  "v": 1,
  "pid": 4711,
  "trace_id": "4c2953622b6086caa46723f43306ffb6",
  "seq": 0,
  "last": true,
  "resource": {
    "service.name": "shop-api",
    "service.namespace": "shop",
    "service.version": "1.4.2",
    "deployment.environment.name": "prod",
    "process.runtime.name": "php",
    "process.runtime.version": "8.3.11",
    "php.sapi": "fpm-fcgi",
    "telemetry.distro.name": "openlog-php",
    "telemetry.distro.version": "0.9.1"
  },
  "sampling_ratio": 1.0,
  "function_trace": false,
  "dropped_spans": 0,
  "spans": [
    {
      "id": "5ccf0fb55209346e",
      "parent": "",
      "name": "GET /users/{id}/orders",
      "kind": 2,
      "start": 1789302480000000000,
      "dur": 12345678,
      "status": 0,
      "status_msg": "",
      "attrs": {"http.request.method": "GET", "http.route": "/users/{id}/orders", "http.response.status_code": 200},
      "events": [{"name": "exception", "time": 1789302480010000000,
                  "attrs": {"exception.type": "RuntimeException", "exception.message": "…", "exception.stacktrace": "…"}}]
    }
  ]
}
```

### 2.1 Alanlar {#_2-1-fields}

| Alan | Kurallar |
|---|---|
| `v` | `1`. Bilinmeyen sürümleri forwarder atar ve sayar. |
| `pid` | Worker PID; `trace_id` ile birlikte bir mesaj dizisini tanımlar. |
| `trace_id` | 32 hex. Gelen `traceparent` header'ı varsa ondan sürdürülür, yoksa rastgele. |
| `resource` | Yalnızca string değerler. Yalnızca yukarıda listelenen anahtarlar artı `service.instance.id`, `container.id`, `k8s.*`. Forwarder altyapı ajanının kendi resource'undan host attribute'larını (`host.id`, `host.name`, `os.*`, `openlog.agent.*`) **ekler** ve eklentinin bunları ezmesine asla izin vermez. |
| `sampling_ratio` | Uygulanan head sampling oranı (0 < r ≤ 1). Root span'de `sampling.ratio` olarak gönderilir (apm.md §sampling). Örneklenmeyen istekler hiçbir şey göndermez. |
| `function_trace` | Mesaj fonksiyon segmentleri içeriyorsa `true` (§3). |
| `dropped_spans` | Bu trace için eklenti limitleriyle atılan span'ler; forwarder root span'de `openlog.php.dropped_spans` ayarlar. |
| `spans[].kind` | OTLP `SpanKind` numaraları: 1 internal, 2 server, 3 client, 4 producer, 5 consumer. |
| `spans[].status` | 0 unset, 1 ok, 2 error. |
| `spans[].attrs` | Değerler: string, integer, float, boolean veya bunların dizileri. Semantic convention'lar apm.md / OTel'e göre (HTTP, DB, exception'lar, `code.*`). |

### 2.2 Span içeriği (eklentinin sorumlulukları) {#_2-2-span-content-extension-responsibilities}

- **Transaction (root, kind 2):** biliniyorsa framework'ün route şablonunu kullanan `METHOD route` adı (Laravel, Symfony,
  Slim, WordPress, CodeIgniter 3/4, Yii 2, düz PHP → apm.md'ye göre normalize script/yol). CLI script'leri: kind 1, ad
  `php <script>`, `openlog.php.cli=true` ile.
- **Datastore'lar (kind 3):** PDO, mysqli, pgsql fonksiyonları, phpredis, Predis; `db.system.name`, `db.namespace`,
  `server.address`/`server.port`, `openlog.capture_query_text=raw|off` değilse apm.md'ye göre temizlenmiş `db.query.text`.
- **Giden HTTP (kind 3):** curl (`curl_exec`, `curl_multi_*`), stream'ler (http(s) üzerinde `file_get_contents`), Guzzle;
  giden isteğe `traceparent` (+ `tracestate`) enjekte eder.
- **Hatalar:** yakalanmamış exception'lar ve fatal hatalar → root span `status=2` + `exception` event'i; yakalanan exception'lar yalnızca
  framework handler'larıyla bildirildiğinde (Laravel `report()`, Symfony kernel exception).
- **Log korelasyonu (isteğe bağlı):** log processor'ları için PHP fonksiyonları `openlog\trace_id()`, `openlog\span_id()` (segment olmayan en içteki
  span) ve `openlog\traceparent()`; `openlog\set_transaction_name()` adı ezer.
- **Uzun süre çalışan worker'lar (D-058):** Laravel Octane (`Worker::handle`, status client'ın `respond`'undan), RoadRunner
  (`Spiral\RoadRunner\Http\HttpWorker::waitRequest` … `respond`/`respondStream`, ayrıca PSR7Worker altında ve RoadRunner üzerinde Octane),
  Swoole/OpenSwoole HTTP sunucuları (`Server::on('request')` ile kaydedilen callable veya
  `Coroutine\Http\Server::handle` … `Response::end`/`redirect`/`sendfile`, status `Response::status`'ten) ve
  FrankenPHP worker modu (`frankenphp_handle_request()`'e verilen callable: çağrısı transaction'ı başlatır,
  dönüşü bitirir; metot, yol, host, istemci, user agent, `traceparent`, FrankenPHP'nin zaten sıfırladığı istek başına `$_SERVER` /
  `SG(request_info)` içinden gelir, status callback dönerken `http_response_code`'dan, callback'ten kaçan
  exception kaydedilir; istekteki `exit()` ve worker yeniden başlatmaları işlenir, worker
  script'inin kendi çalışması asla gönderilmez) tarafından işlenen her istek için bir transaction (kind 2, web isteğiyle aynı adlandırma ve attribute'lar). FrankenPHP klasik modu normal bir SAPI isteğidir. Bilinen FrankenPHP sorunu (eklentiden değil):
  v1.12.7'ye kadar Caddy, `frankenphp.Init()` istek kanalını oluşturmadan bağlantı kabul eder ve
  o pencerede gelen istek asla yanıtlanmaz ve sunucunun durmasını engeller (php/frankenphp#2612, 1.12.7'den sonra düzeltildi);
  PHP başlangıcını yavaşlatan eklentiler (openlog ~10 ms ekler) pencereyi genişletir. Trafiği yalnızca
  `FrankenPHP started` log satırından sonra gönder (`tests/044-frankenphp.phpt` böyle yapar). Worker
  sürecinin kendi CLI transaction'ı ilk istek başlayınca atılır; istekler arasında hiçbir şey kaydedilmez ve
  header iletilmez; bağlantı attribute'ları istekler boyunca kalır. Tek süreçte örtüşen istekler (Swoole
  coroutine'leri) asla karışmaz: birden fazlası sürerken child span'ler, route adları ve function tracer
  hepsi için durur, her istek yine de `openlog.php.concurrent=true` ile root span'ini alır ve giden
  `traceparent` header'ları (ve `openlog\traceparent()`) handler'ı güncel coroutine'in call stack'inde olan isteğin context'ini taşır;
  handler yoksa hiçbir şey taşımaz. Kapsam dışı: `openlog.userland_hooks=0` (istek
  callable'ları gözlemlenen userland fonksiyonlardır), `frankenphp_handle_request()` / `Server::on()`'a verilmeden önce
  çalışmış bir istek callable'ı.

### 2.3 Bölme {#_2-3-splitting}

JSON 60 000 byte'ı aşarsa span'ler aynı `pid` + `trace_id` ile datagram'lara bölünür, `seq` 0'dan artar,
`last: true` yalnızca sonuncusundadır; `resource`, `sampling_ratio`, `function_trace` her parçada tekrarlanır.
Nesne içindeki alan sırası önemli değildir (eklenti `seq`, `last` ve `dropped_spans` değerlerini
`spans`'ten sonra yazar). Çekirdeğin datagram kuyruğu kısadır (`net.unix.max_dgram_qlen`), bu yüzden bölünmüş mesajlarda eklenti
`EAGAIN` alınca bir parçayı mesaj başına en fazla ~2 ms yeniden deneyebilir, asla daha uzun değil.
Forwarder (`pid`, `trace_id`) başına yeniden birleştirir ve `last` gelince veya **5 sn** sonra export eder; eksik trace'ler
root span'de (root yoksa her span'de) `openlog.php.incomplete=true` alır.

## 3. Fonksiyon düzeyinde trace'ler (transaction tracer) {#_3-function-level-traces-transaction-tracer}

- Eklenti, örneklenen her istek için userland fonksiyon çağrılarının **sınırlı bir segment ağacını** yalnızca bellekte kaydeder.
  Ağaç, her `min_segment_ms`'de ve her instrumente edilen span'in başında ve sonunda alınan **wall-clock stack örneklerinden** kurulur
  (her çağrıyı gözlemlemek çağrı başına ~60 ns'ye, framework isteğinde > 1 ms'ye mal olur; ölçüldü).
- Yalnızca transaction süresi ≥ `openlog.transaction_tracer.threshold_ms` (varsayılan **10 ms**) ise veya
  transaction hatayla bittiyse **gönderilir**; aksi halde istek sonunda atılır.
- Limitler: `openlog.transaction_tracer.max_segments` (varsayılan **2000**), `min_segment_ms` (varsayılan **1 ms**; warm-up
  penceresinden sonraki örnekleme aralığı, bir isteğin ilk `warmup_ms` (varsayılan
  **100 ms**) süresinde `warmup_segment_ms` (varsayılan **10 ms**) (D-057); `warmup_ms=0` warm-up'ı kapatır ve baştan itibaren her `min_segment_ms`'de
  örnekler, böylece kısa istekler (10–100 ms) daha yüksek CPU maliyetiyle daha fazla fonksiyon segmenti gösterir; `min_segment_ms`'den küçük bir
  `warmup_segment_ms` daha kaba değildir ve warm-up yok demektir; aralıktan kısa çağrılar yalnızca bir örnek
  denk gelirse görünür, aynı frame'den arada örnek olmadan yapılan aynı fonksiyonun ardışık çağrıları tek segment olur), bellek sınırı `openlog.transaction_tracer.max_memory_kb`
  (varsayılan **4096**), örneklenen çağrı derinliği 256 (en dıştaki frame'ler). Limite ulaşmak kaydı durdurur ve
  `dropped_spans`'i artırır.
- Segment span'leri: kind 1, ad `Class::method` veya `function`, attribute'lar `code.function.name`, `code.namespace`,
  `code.file.path`, `code.line.number`, `openlog.php.segment="function"`, `openlog.php.samples` (segmentin dayandığı örnek
  sayısı; başlangıç/bitiş ± yarım aralıktır).
- Dahili fonksiyonlar yalnızca instrumente edilen kümedeyse trace edilir (datastore'lar, HTTP, `sleep`/`usleep`, uzak stream'lerde `file_*`);
  fonksiyon segmenti olarak değil, kendi client/internal span'leri olarak görünürler.
- Overhead bütçesi (CI'daki benchmark'larla zorlanır): tracer kapalı ≤ %3 RPS; açık (eşik 500 ms, 0.1.36'ya kadarki varsayılan) referans Laravel uygulamasında ≤ %7 RPS.
  Ölçülen durum ve yöntem: `agents/php/ext/README.md` "Overhead" (D-057). PHP 8'de
  Observer API'nin kendisi, herhangi bir fcall observer kaydedildiğinde observer ne yaparsa yapsın her fonksiyon çağrısına 8.0–8.3'te ~100 CPU komutu,
  8.4'te ~50–65 maliyet getirir; `openlog.userland_hooks=0` bunu önler (§4).

## 4. Eklenti ayarları (`php.ini`) {#_4-extension-settings-php-ini}

| Ayar | Varsayılan |
|---|---|
| `openlog.enabled` | `1` |
| `openlog.service_name` | boş → `php-app` (web SAPI'leri) veya `php-cli` (CLI); env `OPENLOG_SERVICE_NAME` ezer |
| `openlog.service_namespace`, `openlog.service_version`, `openlog.environment` | boş; env `OPENLOG_*` ezer |
| `openlog.transport` | `unix:///run/openlog-infra-agent/php.sock` |
| `openlog.sampling_ratio` | `1.0` (parent-based: gelen örneklenmiş `traceparent` her zaman kaydedilir) |
| `openlog.capture_query_text` | `sanitized` (`raw`, `off`) |
| `openlog.transaction_tracer.enabled` | `1` |
| `openlog.transaction_tracer.threshold_ms` | `10` |
| `openlog.transaction_tracer.max_segments` | `2000` |
| `openlog.transaction_tracer.min_segment_ms` | `1` |
| `openlog.transaction_tracer.warmup_ms` | `100` (istek başında kaba örnekleme penceresi; `0`: warm-up yok) |
| `openlog.transaction_tracer.warmup_segment_ms` | `10` (warm-up sırasında örnekleme aralığı; asla `min_segment_ms`'den ince değil) |
| `openlog.transaction_tracer.max_memory_kb` | `4096` |
| `openlog.log_level` | `warning` (PHP error log'a, rate limit'li) |
| `openlog.userland_hooks` | `1`. `0` (system): yalın mod; fcall observer / `zend_execute_ex` override yok, yalnızca dahili fonksiyonlar instrumente edilir (datastore'lar, HTTP client'ları, sleep); framework route adları, framework'ün bildirdiği exception'lar, Predis, uzun süre çalışan worker transaction'ları yok; yakalanmamış exception'lar fatal hatadan kaydedilir (`exception.type` = `E_ERROR`) |

Eklentinin öz metrikleri, yalnızca sıfır değilse her mesajın root span attribute'larında taşınır
(`openlog.php.send_errors`, bu worker'ın önceki mesajından beri `openlog.php.dropped_messages`).

## 5. Runtime desteği (D-036) {#_5-runtime-support-d-036}

| PHP | Hook katmanı |
|---|---|
| 7.1 – 7.4 | `zend_execute_ex` / `zend_execute_internal` değişimi (önceki handler'ları zincirler) |
| 8.0 + | Observer API (`zend_observer_fcall_register`) |

Build hedefleri: her minor sürüm 7.1…8.4 (8.5 çıkınca) × NTS/ZTS × glibc/musl × amd64/arm64 (§7.1: glibc
modülleri en fazla glibc 2.28 ister). Eklenti açık kalarak başarısız olmalıdır: her dahili hata instrumentasyonu isteğin geri kalanı için
(uzun süre çalışan worker'da: worker isteğinin) kapatır ve bir kez loglar.

## 6. Forwarder (altyapı ajanı modülü `php_forwarder`) {#_6-forwarder-infra-agent-module-php-forwarder}

| Yapılandırma | Varsayılan |
|---|---|
| `php_forwarder.enabled` | discovery bir PHP runtime bulursa (php-fpm, mod_php, php CLI binary) `true`, aksi halde `false`; açık değer kazanır |
| `php_forwarder.socket` | `/run/openlog-infra-agent/php.sock` (boş = unix listener yok; bu durumda `udp_listen` zorunlu) |
| `php_forwarder.socket_group` | auto = `openlog-php`, yoksa yedek gruplar (bkz. §1); `auto`, bir grup adı veya sayısal gid |
| `php_forwarder.grant_pool_users` | `true`: ayrıcalıklı reconcile adımı PHP-FPM pool kullanıcılarını ve Apache/nginx kullanıcısını `openlog-php`'ye ekler (§1); `false` devre dışı bırakır (yalnızca root'a ait yapılandırma) |
| `php_forwarder.socket_mode` | `"0660"`; `"0666"`, §1'deki açık tercih (her yerel kullanıcı gönderebilir) |
| `php_forwarder.udp_listen` | boş (kapalı) |
| `php_forwarder.max_pending_traces` | `10000` |
| `php_forwarder.reassembly_timeout` | `5s` |

- Mesajları OTLP span'lerine çevirir (farklı resource başına bir ResourceSpans) ve ajanın var olan export
  hattına (kuyruk, disk buffer, yeniden denemeler, `Retry-After`) `/v1/traces` payload'ları olarak verir; PHP verisi backend kesintilerinden
  host metrikleri gibi kurtulur. Span'ler en fazla 1 sn ve istek başına `export.max_request_bytes`'ın yarısına kadar batch'lenir.
- Girdiyi doğrular (boyutlar, hex ID'ler, değer türleri, span başına attribute sayısı ≤ 128, string değerler ≤ 4 KiB) ve
  bozuk mesajları atar; `resource` host anahtarlarına asla güvenmez. Doğrulama detayları (v1):
  - 60 000 byte'tan uzun datagram, geçersiz JSON veya sondaki veri → `malformed`; `v` ≠ 1 olan geçerli JSON →
    `unsupported_version`; bilinmeyen alanlar yok sayılır (eklemeler v1 içinde uyumlu kalır);
  - ID'ler tam uzunlukta küçük harf hex ve tamamı sıfır olmayan değerler olmalı; span id'leri mesaj içinde tekil; `pid` > 0;
    `seq` 0…255; `sampling_ratio` yoksa = 1; `kind` 1…5, `status` 0…2, `start` > 0;
  - attribute anahtarları 1…256 byte; span ve event başına ≤ 128 attribute; her string değer (attribute'lar, dizi
    elemanları, resource değerleri, adlar, `status_msg`) ≤ 4 KiB; eklenti kırpmalıdır, forwarder kırpmaz;
    diziler iç içe olamaz ve homojen olmalıdır (karışık integer ve float'lar float olur); `null` ve nesneler
    geçersizdir;
  - tek bir geçersiz span tüm mesajı düşürür.
- Resource: izin verilen eklenti anahtarları (§2.1) tutulur, diğer her şey sessizce atılır; forwarder
  `telemetry.sdk.language=php` ve altyapı ajanının `host.*`, `os.*` ve `openlog.agent.*` attribute'larını ekler (
  `openlog.entity.type` veya `host.extra_attributes` değil).
- Span flag'leri: parent'ı aynı trace mesajında olan span `SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE` alır (yerel parent);
  tam bir trace'te parent'ı içinde olmayan span (`traceparent`'tan sürdürülen) ayrıca
  `SPAN_FLAGS_CONTEXT_IS_REMOTE` alır, böylece APM giriş tespiti (apm.md §2) span'lerin nasıl batch'lendiğine bağlı olmaz.
- Root span: parent'ı olmayan span; yoksa tam bir trace için parent'ı trace'te olmayan en erken span;
  eksik bir trace için yalnızca tek böyle span. `dropped_spans`, parçalar üzerindeki en büyük değerdir.
- Yeniden birleştirme limitleri: `max_pending_traces` ve 64 MiB bekleyen datagram; ikisinden biri aşılınca en eski bekleyen trace
  çıkarılır (parçaları `dropped` sayılır), yinelenen `seq` veya `last`'ten sonraki parça `dropped` olur. Ajan kapanırken
  veya modül kapatılınca bekleyen trace'ler eksik olarak export edilir.
- Öz telemetri: `openlog.agent.php.messages{result=accepted|malformed|unsupported_version|dropped}`,
  `openlog.agent.php.spans`, `openlog.agent.php.reassembly_timeouts`, `openlog.agent.php.pending_traces`
  (semantic-conventions §2 ajan öz telemetrisi). `accepted`, mesajın trace'i export edildiğinde sayılır.
- Discovery: `php-fpm` kuralının `apm_hint`'i eyleme dönüşebilir hâle gelir: arayüz `openlog.so`'nun yüklü olup olmadığını gösterir
  (forwarder'ın son 10 dakikada o host/servisten veri almasıyla tespit edilir).
  Ajan, forwarder son 10 dakikada geçerli bir mesaj kabul ettiyse `apm_hint.status` = `active`, aksi halde
  `not_installed` ayarlar (semantic-conventions §3.4), bunu ipucu `openlog-agent-php` adlandıran her bulunan servis için yapar;
  durum değişikliği bir envanter snapshot'ı tetikler.
- İzinler: ajan `openlog-agent` olarak çalışır; socket'in PHP grubuna `chgrp` edilmesi yalnızca o kullanıcı
  grubun üyesiyse çalışır (örneğin systemd drop-in `SupplementaryGroups=www-data`). Aksi halde socket ajanın grubunda kalır,
  uyarı loglanır ve `openlog.agent.permission_denied{collector=php_forwarder}` artırılır.

## 7. Dağıtım (D-059) {#_7-distribution-d-059}

### 7.1 Release artifact'leri {#_7-1-release-artifacts}

Mimari başına (amd64, arm64) `agents/php/packaging/build-artifacts.sh VERSION OUT_DIR` ile derlenir ve
her ürün sürümüyle aynı imzalı `manifest.json` içinde yayınlanır (releases-updates.md §2):

| Dosya | İçerik |
|---|---|
| `openlog-php-agent_<v>_linux_<arch>.tar.gz` | tek üst dizin `openlog-php-agent_<v>_linux_<arch>/`: `modules/<api>-<nts\|zts>-<glibc\|musl>/openlog.so`, `modules.txt` (modül başına bir satır: anahtar, derlendiği PHP sürümü, sha256), `bin/openlog-php-install`, `VERSION`, `ARCH`, `LICENSE`, `README.md` |
| `….deb`, `….rpm` | ağaç `/opt/openlog/php-agent/versions/<v>/` içinde (root'a ait), `current` → `versions/<v>`, `/usr/bin/openlog-php-install`; postinstall `openlog-php-install install` çalıştırır (restart yok), preremove `uninstall` çalıştırır |
| `….apk` | Alpine için aynısı |

- Modül anahtarı: `ZEND_MODULE_API_NO` (`php -i` "PHP Extension"), thread safety, C kütüphanesi. PHP 7.1–8.4 × NTS/ZTS ×
  glibc/musl = mimari başına 36 modül. PHP'nin debug build'leri desteklenmez.
- glibc modülleri AlmaLinux 8'de, resmi image'ın PHP kaynağından yapılandırılan header'lara karşı derlenir ve
  2.28'den yeni bir glibc sembol sürümüne referans vermemelidir (aksi halde build başarısız olur): RHEL/Alma/Rocky 8+, Debian 10+,
  Ubuntu 20.04+, Amazon Linux 2023. musl modülleri resmi Alpine image'larında, desteklenen bir dağıtımın getirdiği en eski
  PHP patch'ine sabitlenerek derlenir (Alpine 3.20 PHP 8.3.15 taşır, yani 8.3 musl `php:8.3.15`'e karşı derlenir):
  PHP'nin inline `smart_str` yardımcıları patch sürümüne göre farklı bir export edilmiş fonksiyon çağırır, yani en yeni
  8.3'e karşı derlenen modül 8.3.15'te relocate olmaz. Her modül, ABI'sinin resmi PHP
  image'ında yükleme testinden geçer.
- Manifest kayıtları: `{"component": "php-agent", "os": "linux", "arch": "<arch>", "format": "tar.gz" | "deb" | "rpm" |
  "apk"}`.

### 7.2 PHP runtime'larını etkinleştirmek (`openlog-php-install`) {#_7-2-enabling-php-runtimes-openlog-php-install}

`openlog-php-install [install|uninstall|status] [--php BIN]… [--dry-run] [--reload] [--json]`, POSIX sh, root olarak çalışır.

- Runtime'lar: olağan yerlerdeki `php`, `php-cgi`, `php-fpm` binary'leri (Debian/Ubuntu `php<v>`, `php-fpm<v>`; RHEL ve
  Remi `/opt/remi/php*/root`; Alpine `php<vv>`; resmi image'ların `/usr/local`'i; `/opt/php*`) veya `--php BIN`,
  gerçek yola göre tekilleştirilir. Her biri için: `<bin> -i` → API numarası, thread safety, debug build, ini scan dizini.
- Etkinleştirme: ilk satırı `; managed by openlog-php-install` olan ve modülü
  sabit bağlantı üzerinden mutlak yolla yükleyen `<scan dir>/90-openlog.ini`: `extension=/opt/openlog/php-agent/current/modules/<key>/openlog.so` (bir
  yükseltme yalnızca `current`'ı değiştirir; PHP'nin `extension_dir`'ine hiçbir şey kopyalanmaz). Debian/Ubuntu:
  `/etc/php/<v>/mods-available/openlog.ini` artı her SAPI'nin `conf.d`'sinde (cli, fpm, apache2,
  cgi) `90-openlog.ini` bağlantıları. İşaret satırı olmayan dosyalar asla yazılmaz veya silinmez; ayarlar başka bir ini dosyasına gider.
- Fail safe: yazdıktan sonra `<bin> -m`, `openlog`'u listelemelidir; aksi halde dosya (ve bağlantılar) yine kaldırılır ve
  çıkış durumu 1 olur. openlog'u zaten başka bir ini dosyasıyla yükleyen, eşleşen modülü veya scan
  dizini olmayan runtime'a dokunulmaz ve bildirilir.
- `--reload` olmadıkça restart yok (çalışan `php*fpm*`, `httpd*`, `apache2*` unit'lerinin systemd `reload`'u; OpenRC reload).
- `status --json`: satır başına bir nesne: `bin`, `version`, `api`, `zts`, `debug`, `libc`, `scan_dir`, `module` (anahtar),
  `supported` (modül var), `enabled` (yönetilen ini var), `loaded` (`-m` openlog'u listeler).
- `uninstall`, artık var olmayan runtime'larınkiler dahil her yönetilen ini dosyasını ve bağlantıyı kaldırır.

### 7.3 Altyapı ajanı üzerinden fleet kurulumu (D-082, D-083) {#_7-3-fleet-installation-through-the-infra-agent-d-082-d-083}

Altyapı ajanı, kendi güncellemelerinin release hattını kullanarak PHP ajanını kurar, yükseltir ve kaldırır
(releases-updates.md §3, D-042). Kod: `agents/infra/internal/phpagent`; backend: `internal/fleet` (politika bölümü,
karar), API: api.md "Fleet". Ajan kullanıcısı kurulum köküne veya PHP ini dizinlerine asla yazmaz; tüm değişiklikler
unit'in ayrıcalıklı pre-start adımı `-apply`'da (root) olur. Ajan bir tane ister ve systemd'nin yeniden
başlatması için bir kez çıkar (D-082: ikinci bir ayrıcalıklı giriş noktası yok; PHP ajanı değişiklikleri nadirdir, boşluk birkaç saniyelik
telemetridir ve yeniden başlatma sırasında gelen PHP span'leri kaybolur).

1. **Yapılandırma** `config.yaml` içinde `php_agent`: `mode: off | manual | auto` (varsayılan `manual`: yalnızca envanter;
   `auto`: desteklenen bir PHP runtime bulunan yerde kur ve yükselt; `off`: fleet kurulumunu kaldır), `version:
   agent | <semver>` (varsayılan `agent`: altyapı ajanının kendi sürümü), `reload: none | graceful` (varsayılan `none`),
   `exclude_bins` (glob'lar, en fazla 50), `remote_config` (varsayılan `true`), `health_check_after` (varsayılan `5m`, en az
   `10s`), `install_root` (varsayılan `/opt/openlog/php-agent`). Fleet mode, version, reload, exclude_bins ve
   çözülmüş hedefi sync üzerinden gönderir (`php_agent`, releases-updates.md §3 "PHP agent"); `remote_config: true` ile bunlar
   yerel değerlerin yerini alır (`<state_dir>/php-agent/remote.json` içinde kalıcı). Ayrıcalıklı adım yalnızca
   root'a ait yapılandırmayı okur (install root; o dosya `mode: off` ve
   `remote_config: false` diyorsa kurulumları reddeder).
2. **Envanter** (her sync'te bildirilir; değişiklik bir sync tetikler): kuruluysa `<install_root>/current`'tan `openlog-php-install status --json` alanları,
   aksi halde ajan yükleyicinin binary konumları için `<bin> -i` / `<bin> -m`'i kendisi çalıştırır
   (§7.2; `supported` = PHP 7.1–8.4 API numarası ve debug build değil), 5 dakikada bir yeniden okunur, artı
   `excluded`, kurulu `version` (`current` → `versions/<v>`) ve `managed_by`: `fleet` (root'a ait işaret
   `.managed-by-openlog-infra-agent` var), `package` (`dpkg-query -S`/`dpkg -S`/`rpm -qf`/`apk info -W` kurulum köküne
   sahip), `manual` (diğer her şey) veya `none`. `package` ve `manual` köklerine asla dokunulmaz; ayrıcalıklı adım
   işareti olmayan bir kök için her isteği reddeder.
3. **Yetenek**: yalnızca ayrıcalıklı pre-start adımı bu başlangıç için çalıştıysa (aynı
   çağrının `apply-status.json`'ı), binary versions düzenindeyse ve release anahtarları varsa (container'larda veya dev build'lerde değil);
   aksi halde raporda `capable=false` ve bir `reason` olur.
4. **Hazırlama** (ayrıcalıksız, `mode=auto`, hedef ≠ kurulu, desteklenen ve hariç tutulmamış bir runtime, süren self-update
   yok): hedefin imzalı manifest'i fleet'ten (`php_agent.manifest`) veya yerelde
   `version: agent` ile, arşivi listeliyorsa altyapı ajanı binary'sinin yanında tutulan manifest'ten gelir (deb/rpm/MSI/pkg paketlerine
   gömülü manifest listelemez: böyle host'lar hata kaydetmeden fleet'i bekler).
   Doğrulama: güvenilen anahtarla imza,
   ürün/şema, manifest sürümü = hedef, `linux/<arch>` için bir `php-agent` `tar.gz` artifact'i ve sürüm düşürmede
   hedef ≥ kurulu sürümün manifest'indeki `rollback_floor`. İndirme (imzalı manifest'in boyutu ve sha256'sı;
   lisans anahtarı yalnızca ingest host'u için) ve host'un en az bir runtime'ı için
   `bin/openlog-php-install` ve `modules/<key>/openlog.so` içermesi gereken deneme açması; sonra `<state_dir>/php-agent/staged/<v>/{archive.tar.gz,
   manifest.json,manifest.json.sig}` ve `<state_dir>/php-agent/request.json` `{id, action: install, version, reload,
   exclude_bins, at}` (hepsi güvenilmez) ve çıkış. Yeniden başlatmadan önceki hata `failed` olarak bildirilir ve
   1 saat sonra yeniden denenir.
5. **Ayrıcalıklı kurulum** (`-apply`, ajanın kendi güncelleme adımından sonra, istek id'si başına bir kez): her şey boyut limitleriyle
   `os.Root(state_dir)` üzerinden okunur; root'a ait binary'ye derlenmiş anahtarlarla yeniden doğrulama;
   arşiv `versions/.<v>.partial` içine kopyalanır ve orada kontrol edilir; root olarak açma (mutlak yol, `..`,
   symlink, link veya aygıt yok, dosyalar yalnızca 0644/0755) manifest ve imzayla birlikte root'a ait bir ağaç olması gereken
   `versions/<v>/` içine; işaret yazılır; `current` atomik olarak değiştirilir; `versions/<v>/bin/openlog-php-install install`
   (`exclude_bins` ayarlıysa kendi `status --json`'ının hariç tutulmamış root'a ait runtime'ları için `--php <bin>`,
   `reload: graceful` için `--reload`: yükleyici çalışan `php*fpm*`, `httpd*`, `apache2*` unit'lerini reload eder; bunlar
   kaydedilir). Başarı = etkin her runtime openlog'u yükler ve en az biri vardır. Aksi halde `current` önceki sürüme
   döner (yeniden kurulur) ve yeni dizin silinir (`rolled_back`) veya önceki sürüm yoksa
   yükleyicinin `uninstall`'u çalışır ve kurulum kökü silinir (`failed`). Başarıda yeni
   ve önceki dışındaki eski sürümler silinir. Sonuç, root'a ait `<infra install root>/php-agent-status.json` dosyasıdır:
   `{request_id, at, action, operation, result: applied|failed|rolled_back|uninstalled|rejected, target, version,
   previous, error, runtimes, units}`; `rejected` = bir doğrulama veya sahiplik kontrolü başarısız oldu, hiçbir şey değişmedi.
6. **Sağlık kontrolü** (ajan, `applied`'den `health_check_after` sonra): `current`'ın `status --json`'ı hariç tutulmamış
   etkin her runtime'ın yüklü olduğunu ve en az birinin olduğunu gösterir, kaydedilen her unit `active`'tir ve kurulum istendiğinde
   veri gönderen PHP servisleri (`apm_hint.status=active`, §6) hâlâ gönderir. Başarısızlıkta ajan o sürümün
   `rollback`'ini ister (süren self-update'ten sonra); `-apply` kayıtlı önceki sürüme döner ve
   onu yeniden kurar veya `uninstall` çalıştırıp kurulum kökünü siler. Geri alınan sürüm otomatik olarak asla
   yeniden kurulmaz.
7. **Kaldırma**: `managed_by=fleet` ile `mode: off`, `uninstall` ister: yükleyici yönetilen her ini dosyasını kaldırır
   (`reload: graceful` ile `--reload`) ve kurulum kökü silinir. Başarısız kaldırma 1 saat sonra yeniden denenir.
8. **Rapor** (sync isteğinin `php_agent`'ı): `mode`, `source` (`local` | `remote`), `capable`, `reason`,
   `managed_by`, `version`, `runtimes[]`, `update{operation: install|upgrade|rollback|uninstall, version, state:
   downloading|restarting|confirming|applied|failed|rolled_back|uninstalled, error, changed_at}`.
9. **Öz telemetri**: `openlog.agent.php_agent.operations{operation=install|upgrade|rollback|uninstall,
   result=success|failure}` (install/upgrade sağlık kontrolünden sonra sayılır; `-apply`'ın geri aldığı başarısız kurulum
   kurulumun başarısızlığı, sağlık geri alması `rollback` olarak sayılır); host resource'u
   `openlog.php_agent.version` taşır (başlangıçta okunur; altyapı ajanının her değişikliği onu yeniden başlatır).
10. **Fleet politikası** (backend, D-083): güncelleme politikasının `php_agent` bölümü `{mode (default manual), version (default
    agent), reload, exclude_bins, changed_at}` ve host başına mode ezmeleri (`agent_php_host_overrides`). `auto`'daki bir host,
    raporu capable ise, başka yerde yönetilmiyorsa, desteklenen bir
    runtime'ı varsa, hedef kendi platformu için `php-agent` artifact'i olan doğrulanmış bir sürümse, o sürümü geri almadıysa,
    güncel dalgadaysa (`fnv32a(host_id + ":php-agent:" + target) % 100`, `changed_at` ile sürüm zamanının sonrakinden beri dalga başına
    `wave_soak_minutes` sonra ulaşılan politika dalgasının altında; host başına `auto` override dalgaları atlar)
    ve bir bakım penceresi açıksa `target_version` + imzalı manifest alır.
