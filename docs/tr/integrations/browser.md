# openlog tarayıcı SDK'sı {#openlog-browser-sdk}

Apache-2.0 · `openlog-browser` paketi · bağımlılık yok · gzip'li yaklaşık 10 KB

Web uygulamaları için gerçek kullanıcı izleme: Core Web Vitals, sayfa yükleme süreleri, SPA rota değişiklikleri, yakalanmamış
JavaScript hataları ve backend trace'lerine devam eden `fetch`/`XHR` izleme.

Kontrat: [docs/contracts/rum.md](/tr/contracts/rum).

## Hızlı başlangıç {#quick-start}

```sh
npm install openlog-browser
```

```js
import { init } from 'openlog-browser';

init({
  key: 'olb_…',                                   // tarayıcı anahtarı, Add data → Browser altından
  endpoint: 'https://apm.resoft.org',
});
```

Kurulumun tamamı bu. İlk sayfa yüklemesinden itibaren Navigation Timing, LCP, INP, CLS, FCP ve TTFB ile sayfa görüntülemelerini,
yakalanmamış hataları ve işlenmemiş rejection'ları, ayrıca her `fetch`/`XHR` çağrısını raporlar. Aynı origin'e giden isteklere W3C
`traceparent` başlığı eklenir, böylece yavaş bir sayfa onu yavaşlatan backend trace'i olarak açılır.

## Anahtar herkese açık, bunda sorun yok {#the-key-is-public-—-and-that-is-fine}

`key` sayfanın içinde gelir. Sayfayı açabilen herkes onu okuyabilir. Bu bir sızıntı değil, tarayıcı izleme böyle çalışır ve openlog
buna göre tasarlandı:

- tarayıcı anahtarı **tek bir endpoint'e** (`POST /v1/rum`) erişim verir, başka hiçbir şeye değil: log, metrik veya rastgele trace
  yazamaz ve bir ingest lisans anahtarı değildir;
- yalnızca anahtarda **listelediğin origin'lerden** çalışır;
- her veri **sunucu tarafında yeniden yazılarak** anahtarın ait olduğu uygulamaya bağlanır, bu yüzden kopyalanan bir anahtar başka bir
  servisin sayfasına veri koyamaz, makine gibi görünemez veya trafik sayılarını şişiremez;
- kendi **hız sınırı** vardır ve iptal edildiğinde bir dakika içinde geçerli olur.

Buraya asla `olk_…` ingest lisans anahtarı koyma. SDK onu reddeder, çünkü o anahtar *gerçekten* gizlidir.

Tehdit modelinin tamamı, yani anahtarını kopyalayan birinin neler yapıp neler yapamayacağı ve nasıl karşılık vereceğin,
[rum.md §3.5](/tr/contracts/rum) içinde.

## Özel event'ler ve süreler {#custom-events-and-timings}

Yukarıdaki her şey otomatik. Yalnızca uygulamanın bildiği bir şeyi kaydetmek istediğinde:

```js
const openlog = init({ key: 'olb_…', endpoint: 'https://apm.resoft.org' });

openlog.recordEvent('checkout_started', { plan: 'pro', step: 2 });
openlog.recordTiming('cart_priced', 42, { currency: 'try' });
```

Parametreler `openlog.rum.custom.param.<key>` altında saklanır, yani openlog'un tanımladığı bir alanla asla çakışmazlar. Event başına
en fazla 16 tane olabilir; anahtarlar küçük harfli `[a-z0-9_.-]`, değerler metin olarak saklanır. Bunun dışındakileri SDK atar, çünkü
sunucu da atardı.

Span olarak saklanırlar ve kendilerine ait bir rollup'ları yoktur, çünkü uygulamanın neyi saydığını, ne anlama geldiğini bilmeden sunucu
önceden toplayamaz. Bu yüzden onları OQL ile okursun:

```sql
SELECT count(*) FROM Span WHERE openlog.rum.custom.name = 'checkout_started' FACET openlog.rum.route
```

API bir ad ve, süre için, bir sayı alır. Rastgele attribute'ları bilerek almaz: sunucu, bir tarayıcı anahtarının neyi yazabileceğine dair
bir izin listesi tutar ([rum.md §3.3](/tr/contracts/rum)), yani başka her şey burada kabul edilip orada atılırdı.

## Seçenekler {#options}

| Seçenek | Varsayılan | Ne yapar |
|---|---|---|
| `key` | — | Tarayıcı anahtarı (`olb_…`). Zorunlu. |
| `endpoint` | — | openlog ingest'in OTLP/HTTP temel adresi. Zorunlu. |
| `serviceName`, `environment` | anahtardan | Bilgi amaçlıdır; sunucu anahtarın değerlerini kullanır. |
| `sampleRate` | `1` | Tutulan **oturumların** oranı. Anahtarın sunucudaki değeri alındıktan sonra o geçerli olur. |
| `propagateTraceHeaders` | `'same-origin'` | `traceparent` nereye eklenir. Aşağıya bak. |
| `maxBatchSize` | `32` | Erken gönderimden önce tamponda tutulan event sayısı. |
| `flushIntervalMs` | `5000` | Boş olmayan kuyruğun ne sıklıkla gönderileceği. |
| `captureVitals` | `true` | LCP, INP, CLS, FCP, TTFB. |
| `capturePageViews` | `true` | Doküman yüklemeleri ve SPA rota değişiklikleri. |
| `captureErrors` | `true` | `error` ve `unhandledrejection`. |
| `captureConsoleErrors` | `false` | `console.error(...)` çağrılarını da raporlar. Kapalı, çünkü gürültülüdür ve çoğu zaman bilerek yazılmıştır. |
| `captureRequests` | `true` | `fetch` ve `XMLHttpRequest` span'leri. |
| `debug` | `false` | SDK'nın yaptıklarını loglar. |
| `analyticsCollection` | `true` | DAU, MAU ve retention için bir ziyaretçiyi ziyaretler arasında tanır. Bunun için `localStorage`'da kalıcı bir id saklar ve bu onay gerektirebilir: [Ürün analitiğine](/tr/guide/product-analytics) bak. `false` geçir ve kullanıcılar kabul edince `setAnalyticsCollection(true)` çağır. |

### Başka bir origin'deki kendi API'ni izlemek {#tracing-your-own-api-on-another-origin}

Varsayılan `'same-origin'`, çünkü farklı origin'e giden bir isteğe başlık eklemek tarayıcının o istek için **preflight** yapmasına yol açar
(fazladan bir gidiş dönüş ve o sunucu başlığa izin vermiyorsa kesin hata) ve trace id'lerini sunucuyu işleten kişiye gönderir. Başka bir
host'taki kendi API'ni izlemek için onu listele:

```js
init({
  key: 'olb_…',
  endpoint: 'https://apm.resoft.org',
  propagateTraceHeaders: ['https://api.example.com', /^https:\/\/.*\.internal\.example\.com/],
});
```

O backend'in CORS yapılandırması `traceparent` istek başlığına izin vermeli.

## Örnekleme {#sampling}

Örnekleme event başına değil, **oturum başına bir kez** kararlaştırılır: yarım oturum daha ucuz bir oturum değil, okunamayan bir oturumdur.
Vitals'ı atılmış bir sayfa görüntülemesi sana hiçbir şey söylemez. Örneklemin dışında kalan oturum hiçbir şey göndermez. openlog saklanan
sayıları anahtarın örnekleme oranıyla ölçekler, yani arayüzdeki sayılar tutulan verinin değil, gerçek trafiğin tahminidir.

Oranı anahtarda değiştir (Settings → Browser keys), sayfalar yeniden deploy gerekmeden alır.

## Neler toplar {#what-it-collects}

Sayfa görüntülemeleri, web vitals, hatalar ve istekler, URL'nin **yalnızca path kısmıyla**: query string ve fragment gönderilmeden önce
atılır, çünkü uygulamalar token'ları ve kişisel verileri tam oraya koyar.

Statik dosya süreleri (script'ler, stil dosyaları, görseller, fontlar) mevcuttur ama **varsayılan olarak kapalıdır**:
`captureResources: true` bunu açar ve her sayfa görüntülemesinin yalnızca en yavaş birkaç dosyası gönderilir. Kapalı olmasının nedeni,
bir sayfanın yaptığı istekten çok daha fazla dosya yüklemesidir; bunu açmak bir kurulumun sakladığı ve faturalandırıldığı veriyi katlar,
bu da güncellemeyle kendiliğinden gelecek bir şey değildir. Oturum id'si rastgeledir, sekme başınadır ve süresi dolar; ziyaretçiyle ilgili
hiçbir şeyden türetilmez, bu yüzden kimseyi ziyaretler arasında tanımlayamaz. Bunu yapan, `analyticsCollection` arkasındaki ziyaretçi id'sidir ve ayrı bir şeydir.

Ayrıntılar ve attribute listesinin tamamı: [rum.md §2](/tr/contracts/rum),
[semantic-conventions.md §10](/tr/contracts/semantic-conventions).

## API {#api}

```js
const rum = init({ … });
rum.recordError(err);   // kendin yakaladığın bir hatayı raporla
rum.flush();            // tamponda olanı şimdi gönder
rum.sessionId();        // geçerli oturum id'si (örneklenmediyse "")
rum.shutdown();         // tüm listener ve patch'leri kaldır, gönder
```

`init()` asla exception fırlatmaz: yapılandırma hatası loglanır ve sayfa izleme olmadan çalışır. İki kez çağırırsan ilk örneği döndürür.

## Tarayıcı desteği {#browser-support}

`PerformanceObserver` olan her tarayıcı (güncel sürümlerin tümü). Tarayıcının desteklemediği vitals raporlanmaz, o kadar: INP için
Chromium 96+ gerekir, diğerleri de aynı şekilde devre dışı kalır. SDK sunucu tarafı render sırasında hiçbir şey yapmaz.

## Geliştirme {#development}

```sh
make install     # veya NODE=local npm ci
make typecheck
make test        # minimal bir DOM taklidine karşı node:test paketleri
make lint
make build
make size        # scripts/size.mjs içindeki bütçeye göre gzip'li boyut
```
