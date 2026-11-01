# Sözleşme: Mobil uygulamalar (v1) {#contract-mobile-applications-v1}

Kararlar: D-136 (real user monitoring). Sunucu: `internal/rum`, `internal/ingest/rum.go`. İlgili:
[rum.md](/tr/contracts/rum) (tüm RUM tasarımı), [semantic-conventions.md](/tr/contracts/semantic-conventions) §10 (bunun mobil için
yeniden ifade ettiği attribute kaydı).

**Bu sözleşmedir, SDK değil.** Bir mobil uygulama openlog ile OTLP/JSON taşıyan düz HTTP üzerinden konuşur ve her SDK (Flutter paketi,
native bir Android veya iOS SDK'sı ya da JSON'u kendisi gönderen bir uygulama) tam olarak aşağıdakini uygular ve bunun için fazladan bir
şey almaz. Önce bunu yazmak, üç istemcinin üç ayrı dil konuşmasını önleyen şeydir.

[rum.md](/tr/contracts/rum) içindeki her şey aynen geçerlidir: payload, anahtarın söylemesine izin verilen şeye göre sunucu tarafında
yeniden yazılır ve bu dokümanın listelemediği bir attribute saklanmak yerine atılır.

## 1. Anahtar bir mobil anahtardır {#_1-the-key-is-a-mobile-key}

Mobil uygulamanın `Origin`'i yoktur, yani tarayıcı anahtarını sınırlayan origin allowlist'i onu sınırlayamaz. **Uygulama allowlist'i** ile
kapsamlandırılmış `kind: "mobile"` bir anahtar gerekir: Android paket adları ve iOS bundle identifier'ları
([rum.md](/tr/contracts/rum) §3.6, anahtar başına en fazla 50).

```
POST /v1/rum
openlog-browser-key: olb_…
openlog-app-id: com.example.shop
content-type: application/json
```

Anahtar header'ı yerine `k` query parametresi de kullanılabilir (değer yapısı gereği public'tir; bu, header ayarlayamayan transport'lar için
var). `openlog-app-id` **mobil anahtar için zorunludur**: uygulama bildirmeyen bir istek hiçbir allowlist ile eşleşmez, çünkü boş alan izin
veren alan olmamalı.

**Uygulama id'si beyana dayalıdır ve openlog onu doğrulayamaz.** Bir build'den alınan anahtarın gelişigüzel yeniden kullanımını daraltır;
ikinci bir kimlik doğrulama faktörü değildir ve bu doküman öyleymiş gibi davranmayacak. Anahtarı asıl sınırlayan, kopyalanmış bir tarayıcı
anahtarını sınırlayan şeydir: tek endpoint, sunucu tarafında yeniden yazılan her payload, anahtar başına rate limit ve auth cache TTL'i içinde iptal.

Burada tarayıcı anahtarı veya eşleşen app id'si olmayan mobil anahtar sunmak `app_not_allowed` ile `403` döner. Bilinmeyen veya iptal
edilmiş anahtar `401` döner.

## 2. Yapılandırma {#_2-configuration}

```
GET /v1/rum/config      → {"service_name", "environment", "sample_rate", "analytics_mode"}
openlog-browser-key: olb_…
openlog-app-id: com.example.shop
```

Uygulama id'si burada da zorunlu: anahtar iki endpoint'te de aynı şekilde yetkilendirilir.

Başlangıçta oku ve döndüğü değeri kullan. `sample_rate` operatörün değeridir, anahtar üzerinde ayarlanır, yani bir uygulamanın hacmi sürüm
çıkarmadan kısılabilir. Cevap gelene kadar (veya başarısız olursa) derlenmiş varsayılanla raporla; başlangıcı asla buna bağlı olarak bloke etme.

## 3. Payload {#_3-the-payload}

Bir OTLP/JSON `ExportTraceServiceRequest`: tek `resourceSpans` kaydı, onun `resource.attributes` değeri ve
`scopeSpans[].spans[]`. Normal content type `application/json`'dur; aksi halde preflight yapacak transport'lar için `text/plain` de kabul edilir.

Üç tür attribute var ve aradaki fark isimlendirme değil güvenlik modelidir:

- **Zorlanan:** sunucu bunları anahtardan veya istekten yazar, gönderilenin üzerine yazar:
  `service.name`, `deployment.environment.name`, `openlog.entity.type`, `geo.country.iso_code`,
  `sampling.ratio`.
- **Atanan:** gönderilenden türetilir, yani gönderen kendi bucket'larını seçemez:
  `openlog.rum.route` (normalize edilmiş), `openlog.rum.vital.rating`, `device.type`.
- **Kabul edilen:** gönderildiği gibi alınır, sınırlıdır: aşağıdaki diğer her şey.

### 3.1 Her span {#_3-1-every-span}

| Attribute | Zorunlu | Değer |
|---|---|---|
| `openlog.rum.event` | evet | `page_view`, `vital`, `error`, `resource` veya `custom`. Bunu taşımayan span RUM değildir ve atılır |
| `session.id` | evet | 32 hex karakter. Bunu taşımayan span bir yere bağlanamaz ve atılır |
| `openlog.rum.page_view.id` | hayır | 16 hex karakter, bir ekranın span'lerini birbirine bağlar |
| `openlog.rum.route` | hayır | Ekran. Navigator'ının bildiği route'u gönder (`/orders/:id`); openlog ne gelirse normalize eder |
| `user.id` | hayır | Kişi için kendi tanımlayıcın, oturum açmada ayarlanır ([rum.md](/tr/contracts/rum) §3.7). **E-posta adresi veya isim değil, opak ve sabit bir id gönder.** openlog saklar ve asla yorumlamaz |
| `openlog.rum.visitor.id` | hayır | Cihazda tutulan 32 rastgele hex karakter; DAU, MAU ve retention bunu sayar ([analytics.md](/tr/contracts/analytics) §1.2). Anahtar `visitors` modunda değilse atılır. Yalnızca §2'deki `analytics_mode` `visitors` dediğinde sakla ve her açılışta yenisini asla gönderme: bu her açılışı yeni bir kişi olarak sayar |

Bir **ekran**, `page_view` span'idir: kişinin açtığı her ekran için bir tane gönder; uygulama içi gezinme için
`openlog.rum.page_view.kind = "route_change"`, cold start için `"load"` ile.

### 3.2 Uygulamanın kendisi hakkında söyledikleri (resource attribute'ları) {#_3-2-what-the-application-says-about-itself-resource-attributes}

| Attribute | Değer |
|---|---|
| `service.version` | Build. `service.name` anahtardan gelir, bu gelmez; bu bilinçli: isim verinin kime ait olduğuna karar verir, sürüm yalnızca içindeki bir build'i etiketler |
| `device.model.identifier` | `iPhone15,2`, `SM-S911B` |
| `device.manufacturer` | `Apple`, `Samsung` |
| `os.name`, `os.version` | `iOS` / `Android` ve sürüm |

`device.id` **kabul edilmez**. Kalıcı bir cihaz tanımlayıcısı gözden kaçmış bir detay değil, gizlilik kararıdır ve sırf gönderildi diye
gelmemeli. Tarayıcıya özgü attribute'lar (`browser.*`, `user_agent.original`) burada anlamsızdır ve atılır.

## 4. Oturumlar ve sampling {#_4-sessions-and-sampling}

Oturum bir kişi değil bir ziyarettir ([rum.md](/tr/contracts/rum) §1.1): 32 rastgele hex karakter, asla kullanıcıdan veya cihazdan
türetilmez. 30 dakika hareketsizlikten sonra yenisini başlat ve 4 saatle sınırla.

**Event başına değil, oturum başına bir kez örnekle.** Yarım oturum daha ucuz bir oturum değil, okunamayan bir oturumdur. Sampling dışı
kalan oturum hiçbir şey göndermez. Payload'a sampling oranı koyma: sunucu ağırlığı anahtardan damgalar, yani ağırlıklı sayımlar bir istemcinin
iddiasıyla şişirilemez.

**Ziyaretçi id'si varsa bunun yerine ziyaretçi başına bir kez örnekle** ([analytics.md](/tr/contracts/analytics) §2), yoksa sadık bir kullanıcı,
sampling dışı kalan her ziyarette ayrılmış gibi görünür.

## 5. Sınırlar {#_5-limits}

| | |
|---|---|
| İstek gövdesi | 512 KiB (üstünde `413`) |
| İstek başına span | 1000 |
| Span başına attribute | 64 |
| Attribute değeri | 2048 byte (`url.full` 1024, `user.id` 128, route 256) |
| Span adı | 256 byte |
| Hata mesajı / stack | 1024 / 16384 byte |
| Custom event adı / birimi | 128 / 32 byte |
| Event başına custom parametre | 16, anahtarlar `[a-z0-9_.-]` 64 byte'a kadar, `openlog.rum.custom.param.` altında |
| Oran | Anahtar başına, operatör ayarlar (`Retry-After` ile `429`) |

Fazla uzun değerler reddedilmez, kırpılır; fazla attribute'lar atılır. Toplu halde, bir zamanlayıcıyla ve uygulama arka plana geçerken gönder:
bir oturumun son batch'i kaybolma ihtimali en yüksek olandır.

## 6. Hatalar {#_6-errors}

`401` bilinmeyen veya eksik anahtar · `403` `app_not_allowed` · `413` gövde çok büyük · `429` rate limit, `Retry-After` ile ·
`503` kimlik doğrulama geçici olarak kullanılamıyor, sonra tekrar dene. Gövdeler
`{"error": {"code", "message"}}` biçimindedir.

`429` ve `503`'ü backoff ile yeniden dene; `401` veya `403`'ü asla yeniden deneme: sonraki denemede hiçbir şey değişmez.

## 7. Henüz bu sözleşmede yok {#_7-not-in-this-contract-yet}

- **Crash raporlama** [mobile-crash.md](/tr/contracts/mobile-crash) içinde.
- **Symbolication.** Tarayıcı stack'leri yüklenen source map'lerle ([rum.md](/tr/contracts/rum) §8), mobil stack'ler yüklenen R8 mapping'leri ve native sembollerle ([mobile-crash.md](/tr/contracts/mobile-crash) §3) okunur hale gelir.
- **Attestation.** §1 için yükseltme yolu Play Integrity ve App Attest'tir, burada bilerek iddia edilmiyor.
