# Sözleşme: Gerçek kullanıcı izleme (RUM, D-136) {#contract-real-user-monitoring-rum-d-136}

Tarayıcı telemetrisi: openlog tarayıcı SDK'sının (`agents/browser`, paket `openlog-browser`) gönderdiği Core Web Vitals, sayfa yükleme
zamanlaması, SPA route değişiklikleri, JavaScript hataları ve `fetch`/`XHR` çağrıları.

Bu dosya SDK, ingest endpoint'i (`internal/ingest/rum.go`), doğrulama ve sorgu
katmanı (`internal/rum`), ClickHouse şeması (`schema/clickhouse/0094_rum.sql`), tarayıcı anahtarı deposu
(`migrations/postgres/0093_browser_keys.sql`) ve API (`/api/v1/rum/*`, [api.md](/tr/contracts/api#real-user-monitoring)) için bağlayıcıdır.

**RUM paralel bir stack değildir.** SDK OTLP span'leri gönderir, yani bir sayfa görüntüleme, bir vital, bir hata ve bir fetch çağrısı
`spans` tablosunun sıradan satırlarıdır ve zaten var olan trace modelini, hata fingerprint'ini ve hata gelen kutusunu
devralır. Üç materialized view (§5), span'lerin çok yavaş yanıtladığı şeyleri ekler: 30 gün boyunca route başına LCP'nin p75'i,
en yavaş sayfalar, kaç oturum olduğu. RUM'la ilgili başka hiçbir şey yeni bir mekanizma değildir.

## 1. SDK {#_1-the-sdk}

```js
import { init } from 'openlog-browser';
init({ key: 'olb_…', endpoint: 'https://apm.resoft.org' });
```

Bağımlılık yok, ES modülleri, yan etkisiz, gzip'lenmiş ~10 KB, bütçeyi
`agents/browser/scripts/size.mjs` zorunlu kılar. `init()` asla exception fırlatmaz: geçersiz yapılandırma loglanır ve sayfa
izleme olmadan çalışır. İkinci bir `init()` ilk instance'ı döndürür.

### 1.1 Oturumlar ve örnekleme {#_1-1-sessions-and-sampling}

Oturum **bir ziyarettir, bir kişi değil**: `sessionStorage` içinde 32 rastgele hex karakter, 30
dakika hareketsizlikten sonra sona erer ve en fazla 4 saat sürer. Asla ziyaretçiyle ilgili bir şeyden türetilmez, sekme başınadır
ve sekmeyle birlikte kaybolur; yani tek başına birini ziyaretler arasında tanıyamaz, onay banner'ı olmadan
toplanabilir olmasını sağlayan da budur.

**Son cümle, bir uygulama `identify()` çağırdığı anda doğru olmaktan çıkar** (§3.7). Bir hesap id'sine bağlanan oturum, tanımı gereği
bir kişiyi ziyaretler arasında tanır ve bunun onay olmadan yasal olup olmadığı openlog'un değil
uygulamanın sorusudur; openlog ne kimlik türetir ne de varsayılan olarak kimlik ayarlar. Oturum
id'sinin kendisi yukarıda anlatıldığı gibi kalır; kimlik, bir uygulamanın bilerek doldurduğu bir alandır. **Aynısı
`visitors` modundaki bir anahtar için de geçerlidir** ([analytics.md](/tr/contracts/analytics) §1), ki yeni anahtarlar bu modda başlar: kalıcı
ziyaretçi id'si birini ziyaretler arasında tanır ve örnekleme ziyaretçi başına bir kez olur. `sessionStorage` kullanılamıyorsa (gizli modlar, sandbox'lı frame'ler)
bellek içi id'ye düşer; asla exception fırlatmaz.

Örnekleme event başına değil, **oturum başına bir kez** kararlaştırılır. Yarım oturum daha ucuz bir oturum değil,
okunamayan bir oturumdur: vital'leri atılmış bir sayfa görüntüleme sana hiçbir şey söylemez. Örneklemeye girmeyen bir oturum
hiçbir şey göndermez.

Oran sayfadan değil **tarayıcı anahtarından** gelir: SDK onu `GET /v1/rum/config` üzerinden okur ve
ingest eşleşen `sampling.ratio` değerini her span'e damgalar (§3.3), böylece sayımlar doğru ölçeklenir ve bir sayfa
kendi trafiğini şişiremez. Bir admin oranı arayüzden düşürür ve sayfalar deploy olmadan alır.

### 1.2 Teslim {#_1-2-delivery}

Event'ler kuyruğa alınır ve batch dolduğunda (32), bir zamanlayıcıyla (5 sn) ve sayfa kapanırken her zaman flush edilir.
Kuyruk 256 event ile sınırlıdır, en eskiler atılır.

Kapanış flush'ı, tarayıcının belge gittikten sonra bitmesini garanti ettiği tek gönderim olan `navigator.sendBeacon`'ı kullanır.
Beacon yalnızca **CORS-simple** bir content type taşıyabilir, bu yüzden gövde `text/plain` olarak ve anahtar query string'de
gönderilir: `application/json` beacon'ı preflight'a sokardı, preflight da kapanış sırasında tamamlanamaz, yani her ziyaretin
son batch'i kaybolurdu. Yedek olarak `keepalive`'lı `fetch` kullanılır. Ingest gövdeyi her iki durumda da OTLP/JSON olarak ayrıştırır; content type bir
taşıma detayıdır, asla güven sinyali değildir.

Yaşam döngüsü hook'ları `visibilitychange`→hidden ve `pagehide`'dır. `beforeunload` bilerek kullanılmaz:
back/forward cache'i bozar ve mobilde güvenilmez şekilde tetiklenir.

### 1.3 SPA route değişiklikleri {#_1-3-spa-route-changes}

`history.pushState`/`replaceState` yamalanır (orijinaller korunur ve onlara iletilir), `popstate`
ve `hashchange` dinlenir. Route değişikliği **yeni bir sayfa görüntüleme ve yeni bir trace başlatır** ve
güncel vital'leri sonlandırır: bir route'un yol açtığı istekler o route'a aittir, dakikalar önceki belge yüklemesine değil.

## 2. Neler toplanır {#_2-what-is-collected}

### 2.1 Core Web Vitals {#_2-1-core-web-vitals}

| Vital | İyi ≤ | Kötü > | Birim | Notlar |
|---|---|---|---|---|
| `lcp` | 2500 | 4000 | ms | Largest Contentful Paint; son (en büyük) aday kazanır |
| `inp` | 200 | 500 | ms | Interaction to Next Paint |
| `cls` | 0.1 | 0.25 | — | Cumulative Layout Shift: toplam değil, **en kötü patlama** |
| `fcp` | 1800 | 3000 | ms | First Contentful Paint |
| `ttfb` | 800 | 1800 | ms | Time to First Byte |

Eşikler yayımlanmış Core Web Vitals sınırlarıdır ve **ayar değil sabittir**: programın amacı herkesin
aynı çıtaya göre ölçmesidir, çıtayı oynatabilen bir kurulum başka kimse için hiçbir şey ifade etmeyen bir sayı
bildirirdi. Derecelendirme her zaman sunucu tarafında hesaplanır (§3.3).

LCP, INP ve CLS ancak sayfa gizlendiğinde bilinebilir (daha büyük bir element, daha geç bir kayma veya daha yavaş bir
etkileşim her zaman hâlâ olabilir), bu yüzden o zamana kadar tutulur. CLS **0 olsa bile** bildirilir: "bu sayfa
kaymıyor" gerçek bir ölçümdür, atlamak kusursuz bir sayfayı ölçülmemiş bir sayfadan ayırt edilemez yapardı.

**Bilinen basitleştirme.** INP, resmi tanımın 50 etkileşimin üstünde kullandığı aykırı değer düzeltmeli 98. yüzdelik değil, sayfanın
en kötü etkileşim gecikmesidir. Böyle sayfalarda openlog'un INP'si
karamsardır, asla iyimser değildir ve her sayfa için aynı hesaptır, yani route'lar karşılaştırılabilir kalır.
Değiştirmek `agents/browser/src/vitals.ts` içinde sınırlı bir değişikliktir.

### 2.2 Sayfa yükleme zamanlaması {#_2-2-page-load-timing}

Navigation Timing kaydından, milisaniye olarak, `openlog.rum.timing.*` şeklinde: `ttfb_ms`, `dns_ms`,
`connect_ms`, `tls_ms`, `response_ms`, `dom_interactive_ms`, `dom_content_loaded_ms`, `load_event_ms`. Tarayıcının
atladığı bir aşama (sıcak DNS cache, düz http origin, back/forward cache'ten geri yükleme) 0 olarak bildirilmek yerine **dışarıda bırakılır**,
böylece bir aşamanın ortalaması onu gerçekleştiren yüklemelerin ortalaması olur.

Sayfa görüntüleme span'inin süresi `loadEventEnd`'dir veya ziyaretçi daha önce ayrıldıysa yüklemenin geldiği noktadır.

### 2.3 Hatalar {#_2-3-errors}

`error` ve `unhandledrejection` listener'ları (eklenir, uygulamanın kendi `onerror`'unun üzerine asla atanmaz), artı
`captureConsoleErrors` açıkken `console.error`; varsayılan kapalıdır, çünkü gürültülüdür ve çoğu zaman bilinçlidir.
`Error` olmayan bir reject yine de bildirilir. `Error` nesnesi yoksa (cross-origin bir script yalnızca
"Script error." verir), dosya, satır ve sütundan tek bir frame sentezlenir.

Hatalar `exception.type`/`message`/`stacktrace` taşıyan **`exception` adlı span event'leri** olur, APM hata fingerprint'inin
okuduğu da tam olarak budur ([apm.md](/tr/contracts/apm) §3.1). Fingerprint v2 bundle dosya adlarından içerik hash'lerini zaten
çıkarır (`main.3f2a1b9c.js` → `main.js`), yani bir deploy her tarayıcı hata grubunu yeniden açmaz.

### 2.4 İstekler {#_2-4-requests}

Her `fetch` ve `XMLHttpRequest`, `http.request.method`,
`http.response.status_code`, `server.address` ve `url.full` taşıyan bir `client` span olur. URL **yalnızca yolu** tutar: query string ve
fragment gönderilmeden önce atılır, çünkü uygulamaların token ve kişisel veri koyduğu yer orasıdır.

### 2.5 Özel event'ler ve zamanlamalar {#_2-5-custom-events-and-timings}

`recordEvent(name)` ve `recordTiming(name, ms)`, `openlog.rum.event = custom` ile bir span gönderir; span
`openlog.rum.custom.name` ve zamanlama için `openlog.rum.custom.value` ile `openlog.rum.custom.unit` taşır.

Ad zorunludur (128 byte ile sınırlı) ve o olmadan tüm span reddedilir: event'e karşı yazılan her sorgunun gruplandığı
anahtar odur, yani adsız bir event daha küçük bir event değil, hiçbir şeyin soramayacağı bir satırdır. Değer isteğe bağlıdır, çünkü
saymak da zamanlamak kadar geçerlidir; varsa ±10¹² içinde sonlu bir sayı olmalıdır, çünkü ötesi bir ölçüm değil
bug veya saldırıdır. Değersiz bir birim atılır: sayılan bir event'te "ms", hiç yapılmamış bir ölçümü iddia ederdi.

**Rollup yoktur**: bir uygulamanın saydığı şeyi sunucu ne anlama geldiğini bilmeden önceden toplayamaz.
OQL ile okunurlar, örneğin
`SELECT count(*) FROM Span WHERE openlog.rum.custom.name = 'checkout_started' FACET openlog.rum.route`.

**Parametreler.** `recordEvent('checkout_started', { plan: 'pro' })` her parametreyi
`openlog.rum.custom.param.<key>` olarak gönderir. Allowlist kayıtları yerine bir önek, çünkü parametreler uygulamanın
kelime dağarcığıdır ve önceden sıralanamaz; bir namespace ile sınırlamak yine de §3.3 kuralını
korur: bir parametre, openlog'un ileride tanımlayıp yorumlayabileceği bir adı asla işgal edemez.

Event başına en fazla 16 tanesi tutulur, anahtarlar 64 byte'a kadar `[a-z0-9_.-]`'dir ve değerler 512'de kesilir. Bunun
dışındaki bir anahtar kaçışlanmak yerine atılır: onu uygulama seçti ve bir `FACET` içinde yazılamayan bir ad kullanılabilir boyut değildir. Sınır
**anahtarlar sıralandıktan sonra** uygulanır: Go map sırası rastgeledir, yani geldikleri gibi almak her istekte
farklı 16 tanesinin hayatta kalması demek olurdu.

Yalnızca özel event'lere aittirler. Bir sayfa görüntüleme veya vital'de namespace, onu tanımayan allowlist'e takılır,
yani herhangi bir event'e keyfi attribute yazmanın yolu olamaz.

Ürün analitiğini RUM'dan yanıtlanabilir yapan da budur (bir event üzerinde `FACET openlog.rum.custom.param.plan` bir
kırılımdır), üstelik public bir anahtara serbest biçimli attribute deposu vermeden.

### 2.6 Statik asset zamanlaması {#_2-6-static-asset-timing}

`PerformanceObserver`'ın `resource` kayıtlarından script'ler, stylesheet'ler, görseller ve font'lar. **Varsayılan olarak kapalı**
(`captureResources`), açıkken de **oranla değil sınırla** kısıtlı: her sayfa görüntülemenin en yavaş on
asset'i.

Buradaki hacim sorununun dürüst yanıtı bir sınırdır. Oran, sayfanın maliyetini kumara çevirir: aynı
ekran bir ziyaretçi için ucuz, sonraki için pahalıdır ve yavaş yüklemeyi açıklayan asset tam da en yavaş olduğu anda
eksik kalır. Sayfa görüntüleme başına en yavaş on, her zaman aynı maliyettedir ve birinin ekranı açıp bulmak istediklerini her zaman
tutar.

`fetch` ve `XHR`, tarayıcı onları resource olarak listelese de hariç tutulur: istek instrumentasyonu (§2.4) onları
trace id ve zamanlama API'sinin göremediği bir status code ile zaten gönderir, iki kez saymak
oturum zaman çizelgesindeki her isteği ikiye katlardı.

Her asset, `url.full` (her zamanki gibi yalnızca yol),
`openlog.rum.resource.initiator`, `…transfer_bytes`, `…encoded_bytes` ve `…cached` taşıyan bir `resource` span'dir. Sonuncusu
göründüğünden daha önemlidir: o olmadan 0 ms'lik bir asset, ağı hiç geçmemişken imkânsız derecede hızlı bir asset'ten
ayırt edilemez.

Asset'ler sayfa görüntüleme bittiğinde (route değişikliği veya sayfanın gizlenmesi) flush edilir, böylece tek sayfalı bir
uygulama tüm oturumun yüklemesini ilk ekranına yazmaz.

### 2.7 Release sağlığı {#_2-7-release-health}

Bir sürümün nasıl davrandığı ve ne zaman çıktığı: `GET /api/v1/rum/releases?app=&environment=&from=&to=&gap=&sort=&dir=&limit=&offset=`.

**İki yarı da zaten var olanı yeniden kullanır.** Sürümler `apm_service_versions_1m`'den gelir; bunun
materialized view'ı servis adı olan her span'i resource `service.version` değerine göre sayar ve RUM span'inde
ikisi de vardır: ad anahtardan zorlanır, sürümü SDK gönderir. Yani bir mobil sürüm, backend deployment'ını
tespit eden kodun aynısıyla tespit edilir ([apm.md](/tr/contracts/apm) §12), rollback ve
ilk sürüm durumları dahil. Hiçbir yeni şeyin release'in ne olduğunu öğrenmesi gerekmedi.

Oturum yarısı aynı yerden gelemezdi. `rum_sessions`'ın sürüm sütunu yoktur ve bir materialized
view tarafından yazılır, o da değiştirilemez (§3.7). `rum_release_sessions_1h`'i okur; bu tablo, sürüm listesiyle aynı 30 gün boyunca sürüm başına saat başına
oturum sayar. **Bir sürümün oranlarının ne anlama geldiği [mobile-crash.md](/tr/contracts/mobile-crash) §5'tedir.**

### 2.8 Funnel'lar {#_2-8-funnels}

`GET /api/v1/rum/funnel?app=&environment=&step=&step=&window=&from=&to=`: ilk adımı yapan oturumlardan kaçının sonraki
adımların her birine **sırayla** ulaştığı.

Adımlar, bir uygulamanın zaten `recordEvent` ile gönderdiği özel event adlarıdır. Bu yüzden funnel yeni
instrumentasyon gerektirmez: var olan event'lere sorulan bir sorudur, adımların saklı bir tanım değil de sorgu parametresi olmasının
nedeni de budur.

**Bütün mesele sıradır ve bunun adım başına bir sayım olmamasının nedeni de budur.** Her adımı
bağımsız saymak, sepeti görmeden checkout'a ulaşan bir oturumu funnel'ı tamamlamış gibi bildirirdi.
ClickHouse'un `windowFunnel`'ı oturum başına, sırayla ve bir pencere içinde ulaşılan en uzak adımı yanıtlar; dış sorgu
oturumları bu seviyeye göre sayar ve API her seviyeyi öncekilere geri ekler, çünkü bitiren bir oturum
son seviyede bir kez sayılır.

`window` (varsayılan 30 dakika), bir oturumun ilk adımdan son adıma gitmek için ne kadar süresi olduğudur: penceresiz bir funnel
funnel değil, bir ömürdür.

İki limit keşfedilmek yerine açıkça söylenir. 2 ile 6 adım arası: tek adım bir sayımdır, altıdan sonra
yanıt bir rapordur. Ve rollup yerine span okuyan her şey gibi, yalnızca **7 günlük trace retention** kadar
geriye gider (§3.7).

**Retention (kohortlar) burada değil:** oturumlar arası kimlik ve yedi günden fazlasını gerektirir, bu yüzden kendi
tabloları ve kendi sözleşmesi var: [analytics.md](/tr/contracts/analytics).

## 3. Tarayıcı anahtarı {#_3-the-browser-key}

**Tarayıcı anahtarı tanımı gereği public'tir.** Bir web sayfasının içinde gelir; sayfayı açabilen herkeste vardır.
Aşağıdaki her tasarım kararı, aksini varsaymak yerine bunu kabul etmekten çıkar.

Ingest lisans anahtarından **ayrı bir kimlik bilgisi türüdür**, kendi tablosunda, kendi öneki
(`olb_`) ve kendi endpoint'iyle. Lisans anahtarını yeniden kullanmak, HTML'e basılan bir değere her sinyali yazma ve ajan fleet sync'i
yönetme gücü verirdi.

### 3.1 Anahtarın sunulması {#_3-1-presenting-the-key}

`POST /v1/rum` anahtarı `openlog-browser-key` header'ından veya `k` query parametresinden alır. Query
parametresi, `sendBeacon` header ayarlayamadığı için vardır (§1.2). Kimlik bilgisini URL'e koymak normalde
yanlıştır; burada değer zaten public'tir, yani bedeli sızması değil, access log'larda görünmesidir.

### 3.2 Origin allowlist'i {#_3-2-origin-allowlist}

Her anahtar kullanılabileceği origin'leri listeler: tam (`https://app.example.com`, şemanın varsayılanı değilse port dahil)
veya subdomain joker karakteri (`https://*.example.com`, her subdomain derinliğini kapsar). Söz dizimi
`OPENLOG_INGEST_CORS_ALLOWED_ORIGINS` ile aynıdır. Boş liste **reddedilir**: allowlist'siz bir anahtar internetteki
her sayfadan veri kabul eder ve boş bir alan güvensiz ayar olmamalıdır. `*`
de aynı nedenle reddedilir.

`/v1/rum` kendi CORS'unu, sunucu geneli CORS değişkeni yerine anahtarın allowlist'iyle yanıtlar: tarayıcı
anahtarı operatör CORS yapılandırması olmadan çalışır ve onunla sessizce genişlemez de. Preflight anahtar
çözülmeden izin verici şekilde yanıtlanır, çünkü preflight hiçbir şeye yetki vermez: gerçek kontrolü ardından gelen POST yapar
ve sert başarısız olan bir preflight konsolda okunabilir bir 403 yerine açıklanamayan bir ağ hatası olarak görünürdü.

### 3.3 Ingest'in payload'a yaptıkları {#_3-3-what-the-ingest-does-to-a-payload}

**Yeniden yaz, inceleme.** Payload parçalanır ve anahtarın yapılandırması artı bir alan
allowlist'inden yeniden kurulur (`internal/rum.Sanitize`), böylece openlog'un ileride yorumlamayı öğreneceği bir alan zaten
saldırganın kontrolünde olamaz:

| Kural | Neden |
|---|---|
| **Resource yeniden kurulur**: `service.name` ve `deployment.environment.name` anahtardan gelir; payload'dan yalnızca `browser.*`, `user_agent.original`, `os.*` kalır | Kopyalanan anahtar bir backend servisinin adıyla yazamaz ve host↔servis bağını ve SaaS modunda planın host limitini yöneten `host.id`'yi enjekte edemez |
| `browser.name` ve `os.name`, payload göndermediyse span üzerinde kapalı bir listeden **`user_agent.original`'dan türetilir**; mobil resource'un `os.name`'i span'e kopyalanır | Rollup'lar span attribute'larını okur ve bunları anahtar yapar, yani bir sayfa değer uyduramaz |
| Bilinen bir `openlog.rum.event` olmayan span **atılır** | Endpoint genel amaçlı trace yazıcısı olarak kullanılamaz |
| Düzgün biçimli `session.id` olmayan span **atılır** | Kime ait olduğu bilinmeyen satır saklanmaya değmez |
| **Route yeniden normalize edilir** (§4) | Bir sayfa rollup anahtarı uyduramaz |
| **Vital derecelendirmesi yeniden hesaplanır**, bilinmeyen vital'ler ve sayısal olmayan değerler atılır | Derecelendirme good/poor sayaçlarını belirler |
| `tracestate` temizlenir ve `sampling.ratio` anahtardan ayarlanır | Yoksa bir sayfa 10⁶ ağırlık iddia edip arayüzdeki her sayımı katlayabilirdi |
| **Span kind atanır**: `internal` veya istekler için `client` | Sayfa görüntüleme asla APM giriş span'i olmamalı, yoksa tarayıcı uygulaması transaction ve Apdex üretirdi |
| Yalnızca `exception` event'leri kalır, en fazla 4 | Sınırlı depolama |
| Her sayı ve uzunluk sınırlıdır: 1000 span/istek, 64 attribute/span, 2 KiB değer, 16 KiB stack, 512 KiB gövde | Tek bir istek asla pahalı olamaz |

Atılan span'ler yanıtta geri bildirilir (`rejected`, `message`) ve
`openlog_rum_rejected_events_total{reason}` içinde sayılır.

### 3.4 Rate limiting {#_3-4-rate-limiting}

Her anahtar `rate_limit_per_minute` taşır (varsayılan 6000 event, yani dakikada kabaca 750 sayfa görüntüleme). Bu, bir dakikalık
burst'lü, **ingest pod'u başına** bir token bucket'tır: cluster geneli bir sayaç, açık internetten gelen trafiği karşılamak için var olan bir
yolda PostgreSQL gidiş dönüşü demek olurdu. Sonucu gizlenmez, açıkça söylenir: *p* ingest pod'uyla etkin tavan
*p* × yapılandırılan limittir. Limit aşılınca
endpoint `Retry-After` ile `429` yanıtlar.

### 3.5 Tehdit modeli {#_3-5-threat-model}

**Anahtarın bilindiğini varsay.** Sayfa kaynağında, tarayıcı devtools ağ sekmesinde ve bundle'ın her CDN
cache'indedir. Aşağıdakilerin hiçbiri onun gizli olmasına bağlı değildir.

Kopyalanmış anahtarı olan birinin **yapabilecekleri**:

- **O tek uygulama** için sahte RUM verisi göndermek: sahte sayfa görüntülemeler, uydurma vital'ler, uydurma hatalar.
  Bu, o uygulamanın RUM ekranlarını ve hata gelen kutusunu kirletir.
- Anahtarın rate limit'ini harcamak, bucket dolana kadar o uygulamanın gerçek ziyaretçilerinin kapasitesini engellemek.
- Eşleşen bir `Origin` header'ı gönderirse yukarıdakileri her yerden yapmak: **origin allowlist'i bir programa karşı
  savunma değildir.** `Origin` sayfa JavaScript'iyle sahtelenemez, yani allowlist kopyalanmış bir anahtarın başka bir *web sitesinde*
  çalışmasını gerçekten durdurur; `curl` ise canı ne isterse gönderir. Bunu saldırıyı değil, gelişigüzel yeniden kullanımı önleyen bir şey olarak düşün.

Tasarım gereği **yapamayacakları**:

- Log, metrik veya keyfi trace yazmak: anahtar tek bir endpoint'i doğrular ve her span
  RUM olarak doğrulanır (§3.3).
- Başka bir servisin adıyla yazmak, host olarak görünmek veya host kotası tüketmek (§3.3).
- Örnekleme iddialarıyla ağırlıklı sayımları şişirmek (§3.3).
- Herhangi bir şey okumak. Tarayıcı anahtarı yalnızca yazar; API anahtarı değildir, sorgu yapamaz.
- Organizasyonun dışına çıkmak. Tenant anahtardan gelir ve sorgu katmanı her okumayı ona bağlar.
- İptalden sonra `OPENLOG_AUTH_CACHE_TTL`'den (varsayılan 60 sn) uzun süre ayakta kalmak.

**Değer okunabilir ve bu yukarıdaki paragrafla çelişmez, ondan çıkar.** Buradaki hiçbir şey anahtarın gizli olmasına
bağlı olmadığından, openlog onu açık saklar ve ayarlar ekranı tam gösterir:
`GET /api/v1/browser-keys`, `key` döndürür. Operatörün kendi HTML'ine basılı bir değeri tekrar göstermeyi reddetmek, onları yalnızca
internetin zaten sahip olduğu bir anahtarı döndürmeye zorluyordu. openlog'un geri okuduğu tek kimlik bilgisi budur: lisans anahtarları, API anahtarları ve SSO client secret'ları gerçek secret'lardır, yalnızca hash olarak
kalır ve hâlâ tam bir kez gösterilir. Bu saklanmadan önce verilmiş anahtarlar boş `key` döner ve yalnızca döndürülebilir.

**Operatör nasıl yanıt verir.** Anahtarı iptal et (Settings → Browser keys veya `DELETE /api/v1/browser-keys/{id}`)
ve sayfada yenisini ver. İptal soft delete'tir: değer kalıcı olarak kullanılamaz kalır, bu önemlidir
çünkü düz metin eski sayfayı yükleyen tarayıcıların cache'inde hâlâ durur. Ingest pod'ları onu
`OPENLOG_AUTH_CACHE_TTL` içinde kabul etmeyi bırakır, zaten yüklenmiş sayfalar yeniden yüklenene kadar denemeye devam eder. Daha dar bir
yanıt için origin listesini sıkılaştır veya rate limit'i düşür: ikisi de siteye dokunmadan bir sonraki cache
yenilemesinde geçerli olur. Tüm RUM'u hemen durdurmak için `OPENLOG_RUM_ENABLED=false`.

**Bilerek iddia edilmeyen.** Bu tasarım zararı sınırlar; kararlı bir saldırganın tek bir uygulamanın RUM verisine
çöp yazmasını önlemez. Bu, public bir istemciden telemetri kabul etmenin doğasında vardır ve public anahtarla aksini iddia eden
her vendor aynı origin kontrolünü anlatıyordur.

### 3.6 Mobil anahtarlar {#_3-6-mobile-keys}

Bir mobil uygulamanın gerçekte ne gönderdiği (endpoint, header'lar, attribute'lar, limitler)
[mobile-agent.md](/tr/contracts/mobile-agent)'dedir. Bu bölüm yalnızca ona yetki veren anahtarla ilgilidir.

Mobil uygulama bir sitedeki sayfa değil, cihazdaki bir binary'dir ve `Origin`'i yoktur. Bu yüzden bir anahtar
iki **kind**'dan biri olarak verilebilir (`browser_keys.kind`, 0098_mobile_keys):

| Kind | Kapsam | Bildiren | Sahtelenebilir |
|---|---|---|---|
| `browser` | `origins`: tam origin'ler ve subdomain joker karakterleri | tarayıcı, `Origin` header'ında | sayfa JavaScript'iyle değil |
| `mobile` | `app_ids`: Android paket adları ve iOS bundle identifier'ları | uygulama, `openlog-app-id` içinde | **herkes tarafından** |

İkisi birbirini dışlar: bir anahtar yalnızca kendi kind'ının allowlist'ini taşır; bunu
`BrowserKeyInput.normalize`, API'nin istek decode'u ve bir `CHECK` kısıtı zorlar. İkisini de taşıyan bir satır,
kapsamı hangi kontrolün önce çalıştığına bağlı bir anahtar olurdu. 0098_mobile_keys öncesinde saklanmış bir anahtarın
kind'ı boştur ve tarayıcı anahtarıdır; çözücünün varsayılan kolunun hata değil origin kontrolü olmasının nedeni de budur.

**Uygulama allowlist'i origin allowlist'inden daha zayıftır ve bu fark bir detay değildir.** `Origin`'i
tarayıcı ayarlar ve sayfa JavaScript'i sahteleyemez, yani o allowlist kopyalanmış bir anahtarın başka bir *web sitesinde*
çalışmasını gerçekten durdurur. Uygulama tanımlayıcısı ise *kendi beyanıdır*: uygulama kendi paket
adını gönderir, `curl` de canı ne isterse gönderir. Uygulama allowlist'i gelişigüzel yeniden kullanımı daraltır (bir uygulamanın
build'inden alınan anahtar başka bir geliştiricinin uygulamasında kazara çalışmaz) ve bundan fazlasını yapmaz. İkinci bir
kimlik doğrulama faktörü değildir ve öyle anlatılmamalıdır.

Dolayısıyla mobil anahtarı gerçekte sınırlayan, kopyalanmış tarayıcı anahtarını sınırlayanla tamamen aynıdır (§3.5):
tek bir endpoint'i doğrular, her payload sunucu tarafında anahtarın kendi satırından yeniden yazılır, rate limit
anahtar başınadır, iptal `OPENLOG_AUTH_CACHE_TTL` içinde her ingest pod'una ulaşır ve hiçbir şey okuyamaz. Listenin
dışındaki bir uygulama `403 app_not_allowed` ile reddedilir; `origin_not_allowed`'dan ayrı adlandırılır, çünkü
olası neden bir saldırı değil, yeni bir uygulamayı eklemeden yayınlamış bir operatördür. Uygulama id'si bildirmeyen istek
asla eşleşmez: mobil anahtar her zaman içinde geldiği uygulamaları adlandırır, yani hiçbir şey iddia etmemek anahtarın verildiği
trafik değildir ve boş alanı "herhangi biri" saymak güvensiz ayarı kolay ayar yapardı.

**Bilerek iddia edilmeyen.** Buradaki hiçbir şey, çağıranın gerçekten senin uygulaman olduğunu kanıtlamaz. Platform
attestation'ı (Play Integrity, App Attest) yükseltme yoludur ve public anahtarla onu taklit etmek, bunun sahteciliği önlemek yerine
zararı sınırladığını açıkça söylemekten daha kötü olurdu; §3.5'in tarayıcılar için aldığı konumun aynısı.

### 3.7 Kim ve nerede {#_3-7-who-and-where}

İki alan sayfayı değil kişiyi anlatır ve zıt yönlerden gelirler. Tasarım bu yöndür:

| Alan | Ayarlayan | Neden böyle |
|---|---|---|
| `user.id` | uygulama, `identify(userId)` ile | sunucudaki hiçbir şey bunu bilemez |
| `geo.country.iso_code` | sunucu, güvenilir bir proxy'nin header'ından | sayfanın gönderebildiği değer, sayfanın uydurabildiği değerdir |

**Kimlik.** `identify(userId)`, oturumun sonraki her span'ine bir tanımlayıcı kaydeder, böylece "bu hesabın hangi oturumları
oldu ve içlerinde ne bozuldu" soruşturma değil bir sorgu olur. İçine ne konacağı operatörün seçimidir ve
**openlog bunu denetleyemez**: sözleşme opak, kararlı bir id ister (backend'in hesap için zaten kullandığı anahtar);
e-posta adresi, ad veya bir kişiyi doğrudan tanımlayan herhangi bir şey değil. openlog uzunluğu 128 byte ile sınırlar, değeri saklar ve asla
yorumlamaz; hesap id'sini e-posta adresinden ayırt edemez, yani disiplin uygulamada olmak zorundadır. Hiç kimseyi tanımlamayan bir oturum
boş bir değer bile değil, hiç `user.id` taşımaz: "çıkış yapmış" ile "hiçbir şey olarak tanımlanmış"
aynı görünmemelidir.

**Ülke.** İki yoldan biriyle gelir ve ikisinde de **ziyaretçi adresi asla saklanmaz**.

1. Ingest'in önündeki bir CDN'in veya proxy'nin yazdığı, `OPENLOG_RUM_GEO_HEADER` ile adlandırılan **bir header** (Cloudflare arkasında `CF-IPCountry`). Güven
   argümanı kısadır: openlog'un önünde duran her şeye, isteği iletmesi için ne kadar güveniliyorsa o header'ı yazması için de o kadar güvenilebilir.
   openlog'a güvenmesi söylenmemiş bir header, mevcut olsa bile yok sayılır.
2. Operatörün sağladığı, `OPENLOG_RUM_GEOIP_DB` ile adlandırılan, hiçbir header yanıt vermediğinde kullanılan **bir veritabanı**. GeoLite2-Country
   biçiminde bir MMDB dosyasıdır, DB-IP'nin ülke veritabanı da aynı biçimdedir. Adres istek işlenirken bellekte aranır ve span'e yalnızca ülke
   gider. openlog veri dosyası getirmez ve lisans kabul etmez, yani varsayılan kurulum hiçbir şey çözümlemez. Proxy arkasında adres
   `X-Forwarded-For`'dan gelir, yalnızca `OPENLOG_RUM_TRUSTED_PROXIES`'in kefil olduğu kadar inanılır ve sağdan okunur, çünkü istemci sola
   her şeyi yazabilir. Değiştirilen dosya bir dakika içinde alınır, bozuk dosya kullanımdakini bırakır.

İkisi de olmayan bir kurulum ülke kaydetmez; bu, tahmin değil dürüst bir boşluktur. İki ASCII harf olmayan değerler atılır, CDN
yer tutucuları `XX` (yanıt yok) ve `T1` (Tor) de öyle: ikisi de yoksa her coğrafya kırılımının tepesinde ülke kılığında otururdu. Region ve şehir
bilerek çözümlenmez: ürün kararının kullandığı şey ülkedir, şehir ise bir kişiyi daraltır.

**Retention, keşfedilmeden açıkça söyleniyor.** İkisi de oturum rollup'ında değil span'lerde durur, yani ikisi de yalnızca
**7 günlük trace retention** kadar geriye gider, `rum_sessions`'ın 30 günü kadar değil. `rum_sessions_local`
bir materialized view tarafından yazılır ve view değiştirilemez: bunları rollup'a koymak onu düşürüp yeniden oluşturmak demek olurdu,
buradaki hiçbir migration bunu yapamaz, aynı hedefe yönelik ikinci bir view de her satırı iki kez sayardı. Kimliği otuz gün
taşımak kendi tablosunu gerektirir; bu, kişisel veri saklama kararıdır ve bir migration'la varılmak yerine bilerek verilmelidir.

**Sorgulamak.** İkisi de yeni bir şey gerektirmez: `Span` OQL'de keyfi attribute taşır, yani
`SELECT count(*) FROM Span WHERE user.id = 'acct_8f3a2b'` ve
`... FROM Span WHERE openlog.rum.event = 'page_view' FACET geo.country.iso_code` olduğu gibi çalışır ve
OQL'in ifade edebildiği her şey için `oql` alert kuralı türü alert verebilir. `user.id` üzerindeki bloom filter
(0101_rum_identity_geo) bir aramanın tenant'ın her span'ini okumasını önler. Ülke üzerinde bilerek
yok: yaklaşık iki yüz farklı değer, neredeyse her granule'ün neredeyse her ülkeyi içermesi demektir, yani
index işe yarayan bir index'in yazma maliyetini taşır ve neredeyse hiçbir şey atlamaz.

## 4. Route normalizasyonu {#_4-route-normalization}

Rollup'ların kardinalitesi saklanan route'un kardinalitesidir ve girdisi, public anahtarla çalışan bir sayfanın seçtiği bir
URL'dir; yani bu bir kolaylık değil bir sınırdır:

1. SDK bildiği bir route gönderebilir (`openlog.rum.route`); framework router'ı `/orders/:id`'yi bilir ve hiçbir
   inceleme bunu `/orders/8f3a`'dan çıkaramaz.
2. Gelen ne olursa olsun yine de **APM transaction adıyla aynı segment kurallarıyla** normalize edilir
   ([apm.md](/tr/contracts/apm) §2.2): UUID'ler, id'ler, hex, token'lar ve e-posta adresleri yer tutucu olur, 8'den fazla
   segment `/…` şeklinde daralır. Yani aynı URL'in tarayıcı route'u ile backend transaction'ı aynı görünür.
3. Sonuç 256 byte'a kırpılır.

Query string ve fragment her zaman önce atılır.

## 5. Depolama {#_5-storage}

Ham RUM tablosu yok: ham veri span'lerdir (7 günlük trace retention). `spans_local` üzerinde üç aggregating view,
hepsi tenant anahtarlı, her sütunda birleştirilebilir ve her zaman Distributed tablo üzerinden `GROUP BY` ile yeniden toplanır
([apm.md](/tr/contracts/apm) §8'in sharding argümanı aynen geçerlidir).

| Tablo | Anahtar (ORDER BY) | İçerik | TTL |
|---|---|---|---|
| `rum_page_views_1m` | tenant, app, environment, minute, route, kind, device, browser | views, samples, duration sum/max/histogram, sekiz Navigation Timing aşama toplamı | 30 g |
| `rum_vitals_1m` | tenant, app, environment, minute, vital, route, device | count, samples, value sum/max/histogram, good / needs_improvement / poor | 30 g |
| `rum_sessions` | tenant, app, environment, session_id | ilk/son görülme, sayfa görüntülemeler, hatalar, giriş ve çıkış route'u, en yeni trace id, device, browser, OS | last_seen'den 30 g sonra |

TTL **sabit 30 gündür**. Üç tablo `internal/migrate.TTLTables` içinde kendi depolama sınıfları olan `rum`'u oluşturur ve
sabit retention'ı temenni olmaktan çıkarıp gerçek yapan da budur: tenant başına retention (D-081) tüm bir sınıfın silme TTL'ini genişletir,
yani `apm` sınıfını ödünç almak bir tenant ayarının RUM'u 400 güne itmesine izin verirdi, oysa bu tablo hâlâ 30 derdi. `apm` sınıfı bundan yalnızca,
rollup'larının kendi şema TTL'i olmaması ve [apm.md](/tr/contracts/apm) §8'in sekiz `apm_*` tablosu üzerinde tanımlı `OPENLOG_APM_RETENTION_DAYS`'i izlemesi sayesinde kurtulur
(`APMRetentionStatements` yalnızca `openlog.apm_*` ifadeleri üretir); RUM tabloları onların arasında değildir.

Tiered storage taşımaları her zamanki gibi sınıf üzerinden uygulanır: `OPENLOG_STORAGE_COLD_AFTER_DAYS_RUM` (varsayılan 7).

`0094_rum.sql` ayrıca `spans_local` üzerine `attributes['session.id']` için bir bloom filter skip index ekler, böylece
oturum detayı tenant'ın aralığını taramadan oturumun kendi span'lerini okuyabilir; `0043_k8s_logs_indexes` içinde
loglardaki `k8s.pod.uid`'nin gördüğü muamelenin aynısı.

### 5.1 Vital histogram bucket'ları {#_5-1-vital-histogram-buckets}

APM log-lineer şeması (ikinin her kuvveti için 8 bucket, [apm.md](/tr/contracts/apm) §4.1) aşağı doğru
`MinBucket = -160`'a genişletilmiştir, çünkü CLS birimsiz bir skordur ve *iyi* değerleri 0.05 civarındadır, APM ise mikrosaniye altı süreleri yok saymak için
-80'de sıkıştırır. CLS'yi APM tabanında sıkıştırmak neredeyse her iyi ölçümü tek bucket'a koyar ve p75'ini anlamsız yapardı.
Sayfa yükleme süreleri APM bucket'ını aynen korur: onlar da diğer span süreleri gibi milisaniyedir.

```
bucket(v) = clamp(ceil(8 · log2(v)), −160, 200)     v ≤ 0 → −160
```

Referans uygulamalar: `rum.Bucket`, `rum.Hist.Quantile` (`internal/rum/hist.go`); ClickHouse
ifadesi `0094_rum.sql` içindedir ve ikisi `internal/rum/rum_test.go` içinde birbirine karşı kontrol edilir.

`good`/`needs_improvement`/`poor`, histogramdan türetilmek yerine yazma anında sayılır, çünkü
eşikler bucket sınırlarıyla hizalanmaz ve Core Web Vitals değerlendirmesi tam pay üzerinden tanımlıdır.

## 6. Korelasyon {#_6-correlation}

**Tarayıcı → backend.** Her `fetch`/`XHR` span'i sayfa görüntüleme span'inin çocuğudur ve W3C
`traceparent` taşır, böylece backend'in server span'i aynı trace'i sürdürür. Tek bir trace tıklamadan
veritabanına kadar uzanır ve trace görünümü iki yarıyı da gösterir.

Propagation varsayılan olarak **same-origin**'dir, ötesi hedef başına isteğe bağlıdır: header'ı
cross-origin bir isteğe eklemek onu preflight'lı bir isteğe çevirir (fazladan bir gidiş dönüş, sunucu header'a
izin vermiyorsa sert hata) ve trace id'lerini o sunucuyu çalıştıran kişiye verir.

**Backend → tarayıcı.** Sayfa görüntüleme span'i **trace'in köküdür**, yani bir backend trace'i tarayıcı kökenini zaten
adlandırır: `GET /api/v1/traces/{trace_id}` tarayıcı span'lerini sunucu span'leriyle birlikte döndürür ve
oturum id'si onlarda bir attribute'tur. `rum_sessions` oturum başına en yeni trace id'yi tutar, böylece sayfa görüntüleme span'leri
trace retention ile süresi dolduktan sonra bile oturum trace olarak açılır.

Bu ikinci bir join değil, trace modelidir: RUM bir korelasyon mekanizması eklemedi, APM'i çalıştıranı yeniden kullandı.

## 7. API {#_7-api}

Base `/api/v1/rum`, her telemetri okuması gibi tenant kapsamlıdır (viewer rolü, API anahtarlarına izin verilir). Şekiller:
[api.md](/tr/contracts/api#real-user-monitoring), [openapi.yaml](https://github.com/onuragtas/openlog/blob/master/docs/contracts/openapi.yaml) (tag `rum`).

| Endpoint | İçerik |
|---|---|
| `GET /rum/apps?sort=&dir=&limit=&offset=` | görüntüleme, oturum, hata, son görülme ile tarayıcı uygulamaları |
| `GET /rum/overview?app=` | beş vital, sayfa görüntüleme serisi ve toplamlar |
| `GET /rum/pages?app=&sort=views\|slowest\|avg\|ttfb\|route&dir=&limit=&offset=` | görüntüleme ve p50/p75/p95 yükleme süresiyle route'lar |
| `GET /rum/vitals?app=&route=` | vital dağılımı, isteğe bağlı olarak tek route için |
| `GET /rum/sessions?app=&sort=&dir=&limit=&offset=` | oturumlar, aksi istenmedikçe en yeni başta |

**Bir okumayı tek tür ziyaretçiye daraltmak.** `device_type` (`desktop`, `mobile`, `tablet`) ve `browser`,
`/rum/pages` ve `/rum/sessions` tarafından kabul edilir; `/rum/vitals` yalnızca `device_type` alır ve `browser` için
400 yanıtlar. Bu asimetri API'nin tercihi değil, depolamanındır: sayfa görüntüleme rollup'ı device **ve** browser
anahtarlıdır, vitals rollup'ı ise yalnızca route ve device anahtarlıdır (§5). Parametreyi orada yok saymak her tarayıcının
rakamlarını tek bir tarayıcının adı altında gösterirdi, bu da reddetmekten kötüdür.

`/rum/sessions?user_id=` tek bir kimliğin ziyaretlerini listeler (§3.7). Id oturum rollup'ında değil span'lerdedir,
yani bu, rollup'ı o span'lerin taşıdığı oturum id'leriyle daraltır: aynı pencere ve trace retention ile sınırlıdır,
istenmediğinde hiçbir maliyeti yoktur. `identify()` hiç çağırmayan bir uygulama için, ki çoğu öyledir, hiçbir şey yanıtlamaz.
| `GET /rum/sessions/{session_id}` | event zaman çizelgesi ve trace bağlantılarıyla tek oturum |

Anahtar yönetimi `/api/v1/browser-keys`'tir ([api.md](/tr/contracts/api#browser-keys)).

**Bilerek bir RUM hata endpoint'i yok.** Tarayıcı hatası, tarayıcı uygulamasının servisinin bir hata span'idir,
yani aynı fingerprint, gruplama, atama ve çözüm durumuyla zaten APM hata gelen kutusundadır; arayüz
`GET /api/v1/apm/errors?service=<app>`'e bağlantı verir. Paralel bir gelen kutusu, aynı hatayı çözmek için iki yer demek olurdu.

Söylemeye değer bir sonuç: tarayıcı uygulaması **APM altında bir servis olarak görünür** (`apm_services`,
`service.name`'i olan her span'i kaydeder), hatalarla ama transaction, throughput veya Apdex olmadan; sayfa görüntülemeler
tasarım gereği `internal` span'lerdir (§3.3). Bu amaçlanandır: full-stack, tek servis listesi ve tek hata gelen kutusu demektir,
ayrı bir tarayıcı ürünü değil.

## 8. Bu dilimde olmayanlar {#_8-not-in-this-slice}

Alacakları şekille birlikte bilerek sonraya bırakıldı:

- **Source map'ler artık hazır** ([api.md](/tr/contracts/api#source-maps)): map, bundle dosya adı başına yüklenir ve
  okuma anında uygulanır, böylece tarayıcı stack'i geliştiricinin kendi dosyalarında okunur. Anahtarlama §3.3'ten çıkar:
  RUM span'i build tanımlayıcısı taşımaz, ama `main.3f2a1b9c.js` içindeki içerik hash'i tam olarak bir build'i adlandırır
  ve fingerprint bunu grup anahtarından çıkarır, böylece gruplar deploy'lar arasında kararlı kalır.
- **Coğrafya ve ağ.** ~~Bu dilimde yok.~~ Ülke: yapıldı, bir proxy'nin header'ından veya operatörün sağladığı veritabanından. openlog
  kendisi adres çözümlemez: `OPENLOG_RUM_GEO_HEADER` ile adlandırılan header'dan önceden çözülmüş bir ISO 3166-1 alpha-2 kodu okur ve
  **ziyaretçi adresini asla saklamaz** (§3.7). Ayarlı değilse veya onu yazan proxy yoksa
  alan boş kalır. Region ve şehir hâlâ yok, `connection.effectiveType` da öyle.
- **Kullanıcı kimliği.** ~~Bu dilimde yok.~~ Yapıldı: `identify(userId)`, oturumun sonraki span'lerinde `user.id` ayarlar
  (§3.7). Bu, şema değil veri koruma sorusu olarak kalır; yanıtın uygulamanın bilerek doldurduğu ve openlog'un asla türetmediği bir alan
  olmasının ve retention'ın oturum rollup'ının 30 günü değil 7 günlük trace penceresi olmasının nedeni de budur.
- **Statik asset'ler için resource timing.** ~~Bu dilimde yok.~~ Yapıldı, bu maddenin istediği örneklemeyle ve
  beklenenden farklı bir yanıtla: oran yerine **deterministik bir sınır**. `captureResources`
  (varsayılan kapalı) her sayfa görüntülemenin yalnızca en yavaş birkaç asset'ini tutar, böylece aynı sayfa her zaman aynı maliyette olur
  ve hayatta kalan asset'ler birinin bu ekranı açıp bulmak istedikleridir. `fetch`/`XHR` orada hariçtir, çünkü fetch.ts onları
  trace id ve zamanlama API'sinin göremediği bir status code ile zaten bildirir. Varsayılan kapalı kalır, çünkü açmak bir kurulumun
  sakladığı ve faturalandığı şeyi katlar, bu da bir yükseltmeyle devralınacak bir şey değildir.
- **Özel event'ler ve zamanlamalar.** ~~Bu dilimde yok.~~ Yapıldı: `recordEvent(name)` ve
  `recordTiming(name, ms)`, `openlog.rum.custom.name` ve
  zamanlama için `…custom.value` ile `…custom.unit` taşıyan `openlog.rum.event = custom` span'i gönderir. **Kendi rollup'ları yoktur**: bir
  uygulamanın saydığı şeyi sunucu ne anlama geldiğini bilmeden önceden toplayamaz; bu yüzden
  OQL ile okunurlar (`FROM Span WHERE openlog.rum.custom.name = 'checkout_started'`). SDK bir ad, zamanlama için
  bir sayı ve §2.5'in `openlog.rum.custom.param.*` altına aldığı parametreleri kabul eder. Geri kalanı dışarıda kalır, çünkü sunucu attribute allowlist'i tutar ve keyfi attribute kabul eden bir API
  sağlamadığı bir depolamayı vaat ederdi.
- **Vital'lerde alert.** ~~Bu dilimde yok.~~ Yapıldı, üstelik `rum` kural türü olmadan: `RumPageView`,
  `RumVital` ve `RumSession` OQL event türleridir ([oql.md](/tr/contracts/oql)) ve `oql` alert kuralı türü
  her OQL sorgusunu derler; yani `SELECT sum(value.sum) / sum(count) FROM RumVital WHERE vital = 'lcp'` bir alert
  koşuludur. OQL'in bildiği bir event türü eklendiği anda alert verilebilir olur, sinyallerin her birine kural türü vermek yerine
  OQL'e bağlanmasının nedeni budur.
- **Session replay.** Kapsamın epey dışında.
