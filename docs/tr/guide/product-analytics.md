# Ürün analitiği

Uygulamanı kaç kişinin kullandığını, geri gelip gelmediklerini, ne yaptıklarını ve nereden geldiklerini gör: DAU, WAU, MAU, yeni
kullanıcılar, retention, funnel, event'ler ve trafik kaynakları. Google Analytics veya Firebase Analytics kullandıysan aynı
konuları kapsar, üstelik kendi sunucunda çalışır.

[Tarayıcı](/tr/integrations/browser), [iOS](/tr/integrations/ios), [Android](/tr/integrations/android) ve
[Flutter](/tr/integrations/flutter) ve [Unity](/tr/integrations/unity) SDK'ları üzerinde çalışır, başka bir şey kurman gerekmez.

Sonuçlar uygulama bazında, **Browser** altındaki **Analytics** sekmesinde görünür.

::: tip Hangi adres?
Örnekler barındırılan servisi kullanır: `https://apm.resoft.org`. openlog'u kendin çalıştırıyorsan kendi sunucunun adresini yaz.
:::

## 1. Anahtarı kontrol et

**Settings → Browser keys** sayfasını aç. Her anahtarın bir **Analytics** ayarı var:

| Mod | Ne saklar | Ne verir |
|---|---|---|
| **Visitors** | Cihaz başına kalıcı, rastgele bir kimlik | Bu sayfadaki her şey |
| **Sessions** | Event'ler ve kaynak bilgisi, kimse ziyaretler arasında tanınmaz | Event'ler, kaynaklar ve oturum funnel'ları. DAU ve retention yok |
| **Off** | Normal izlemenin ötesinde hiçbir şey | Bunların hiçbiri |

Yeni anahtarlar **Visitors** ile başlar. **Bu özellikten önce oluşturulan anahtarlar sen değiştirene kadar Off kalır**, yani openlog'u
güncellemek kendiliğinden veri toplamaya başlatmaz.

## 2. SDK'yı başlat

### Tarayıcı

Başka bir şey gerekmez:

```js
import { init } from 'openlog-browser';

const openlog = init({ key: 'olb_…', endpoint: 'https://apm.resoft.org' });
```

### iOS

Başka bir şey gerekmez. Kimlik `UserDefaults` içinde tutulur.

```swift
let openlog = try OpenLog.start(Options(
    key: "olb_…",
    endpoint: "https://apm.resoft.org",
    appID: Bundle.main.bundleIdentifier ?? ""
))
```

### Android

Kimliğin tutulacağı özel bir klasör ver:

```kotlin
val openlog = OpenLog.start(
    Options(
        key = "olb_…",
        endpoint = "https://apm.resoft.org",
        appId = BuildConfig.APPLICATION_ID,
        visitorDirectory = context.filesDir.path,
    )
)
```

### Flutter

Özel bir klasör ver, örneğin `path_provider` paketinden:

```dart
final dir = await getApplicationSupportDirectory();

final openlog = Openlog.init(OpenlogOptions(
  key: 'olb_…',
  endpoint: 'https://apm.resoft.org',
  appId: 'com.example.shop',
  visitorDirectory: dir.path,
));
```

::: warning Android ve Flutter `visitorDirectory` ister
Bu olmadan SDK ziyaretçi kimliği göndermez; DAU, MAU ve retention boş kalır. Her açılışta yeni kimlik üretmeye geçmez, çünkü bu
her açılışı yeni bir kişi olarak sayardı.
:::

## 3. Önemli olanı kaydet

Web'de sayfa görüntülemeleri otomatik kaydedilir. Mobilde kullanıcı gezindikçe `recordScreen` çağır. Önem verdiğin eylemlere isim ver:

```js
openlog.recordEvent('checkout_started', { plan: 'pro' });
```

Az sayıda sabit isim kullan, değişen bilgileri parametreye koy. Her anahtar, her ingest sunucusunda en fazla 500 farklı event ismi
saklar, fazlası `(other)` olarak sayılır.

## 4. Rıza konusunda karar ver

::: danger Bu karar sana ait
**Visitors** modunda SDK ziyaretçinin cihazında kalıcı bir kimlik tutar ve onu ziyaretler arasında tanır. Sen ve ziyaretçilerin
nerede olduğunuza bağlı olarak bu, KVKK, GDPR veya ePrivacy kuralları gereği rıza gerektirebilir. openlog bu kararı senin yerine
veremez. Emin değilsen hukuki destek al.
:::

Önce sorman gerekiyorsa toplamayı kapalı başlat, kabul edince aç:

```js
const openlog = init({
  key: 'olb_…',
  endpoint: 'https://apm.resoft.org',
  analyticsCollection: false,
});

// Rıza penceresinde:
openlog.setAnalyticsCollection(true);
```

Aynı seçenek ve çağrı iOS, Android ve Flutter'da da var.

- Toplamayı kapatmak **kimliği cihazdan siler ve bu tercih hatırlanır**, yani sayfa yenilenince kişiye yeni kimlik verilmez.
- Toplamayı kapatmak yalnızca toplamayı durdurur. **Daha önce gönderilenleri silmez.**
- `identify()` bunlardan ayrıdır. Opak bir hesap kimliği gönder, asla e-posta adresi veya isim gönderme.

### Bir kişinin verisini silmek

Silme talebini yanıtlamak için backend'inden admin API anahtarıyla bunu çağır. Kimliği SDK'daki `visitorId()`'den al, `identify()`
kullanıyorsan kendi hesap kimliğini kullan:

```sh
curl -X POST https://apm.resoft.org/api/v1/analytics/erase \
  -H "Authorization: Bearer <api anahtarı>" -H "Content-Type: application/json" \
  -d '{"app": "shop-web", "visitor_id": "fedcba9876543210fedcba9876543210"}'
```

`202` ve bir kimlik döner, `GET /api/v1/analytics/erase/{id}` durumu bildirir. Silme kısa süre sonra arka planda gerçekleşir. Span'lerdeki
ham kimliği bu silmez: o, 7 günlük trace saklama süresiyle birlikte kendiliğinden silinir.

## Önceki dönemle karşılaştır

**Analytics** sekmesinde 7, 28 veya 90 günlük bir dönem var. Her toplam ve listelerin her satırı, ondan önceki aynı sayıda güne göre ne kadar değiştiğini sayı ve yüzde olarak gösterir: `+200 (+%20,0)`.

- **Tamamlanmış günler, dünle biten, UTC.** Sabahları bugün daha yeni başlamıştır; onu saymak her sabah bütün sayıları düşüyor gösterirdi.
- **`(yeni)`** önceki sayının sıfır olduğu anlamına gelir, çünkü sıfırdan artışın yüzdesi olmaz. `–` ise önceki sayının bilinemediği anlamına gelir: 90 günde, önceki 90 gün event'lerin saklanma süresini aşar, ama kullanıcılar yine karşılaştırılır.
- **Şimdi `0` görünen ve düşen bir satır,** tamamen durmuş bir şeydir.
- Uygulama önceki dönem içinde veri göndermeye başladıysa sekme bunu söyler, çünkü artış yalnızca bundan kaynaklanıyor olabilir.

Aynı sayılar API'de de var: `GET /api/v1/analytics/compare?app=&days=`.

## Kitleni gör

**Analytics** sekmesindeki **Kitle** tablosu oturumları ve kişileri ülkeye, cihaz türüne, cihaz modeline, işletim sistemine, tarayıcıya ve uygulama sürümüne göre
gösterir. Cihaz türü, işletim sistemi, tarayıcı ve sürüm kurulum gerektirmez, cihaz modeli ise mobil SDK'lardan gelir.

**Ülke için sunucunda tek bir ayar gerekir,** çünkü openlog konum verisiyle birlikte gelmez. Önünde Cloudflare gibi bir CDN varsa yazdığı başlığın adını ver:

```sh
OPENLOG_RUM_GEO_HEADER=CF-IPCountry
```

Yoksa bir ülke veritabanı ver. [DB-IP'nin ücretsiz ülke veritabanı](https://db-ip.com/db/download/ip-to-country-lite) çalışır ve veriyi gösterdiğin yerde DB-IP'ye bağlantı
vermeni ister. GeoLite2-Country de çalışır.

```sh
# .env dosyanda. Compose bu klasörü `geoip` biriminde tutar.
OPENLOG_RUM_GEOIP_DB=/var/lib/openlog/geoip/country.mmdb
# Ingest'in önünde nginx veya yük dengeleyici varsa, yoksa her ziyaretçi onun adresinden gelmiş görünür:
OPENLOG_RUM_TRUSTED_PROXIES=10.0.0.0/8
```

Ayarı ekledikten sonra bir kez yeniden başlat. Sonrasında yeni sürüm çıktığında dosyayı değiştirmen yeterli. **Yalnızca ülke saklanır.** Adres istek işlenirken kullanılır; saklanmaz, loglanmaz, span'e yazılmaz.
O an için yine de kişisel veridir, gizlilik politikanda bu tür işlemleri listeliyorsan belirt. Ülkeler bu ayarı yaptığın andan itibaren görünmeye başlar, önceki oturumlar bilinmiyor kalır.

## Sayılar nasıl çalışır

- **Kullanıcı, cihazdır.** Telefon ve laptop kullanan biri iki kişi sayılır. Hesaplar cihazlar arasında birleştirilmez.
- **WAU ve MAU kayan penceredir.** O tarihte biten 7 ve 30 günde aktif olan kişileri sayar.
- **Büyük sayılar tahminidir.** Birkaç bin kişinin üzerinde DAU, WAU ve MAU yaklaşık %1 hata payına sahiptir. Altında kesindir.
- **Stickiness**, DAU bölü MAU'dur.
- **Retention, kişileri ilk görüldükleri zamana göre gruplar** ve sonraki her gün, hafta veya ay aktif olanların oranını gösterir.
- **İlk gün herkes yenidir.** Geçmişe dönük doldurma yoktur, bu yüzden bir anahtarın ilk günlerinde yalnızca yeni kullanıcılar görünür.
- **Örnekleme oturuma değil kişiye göredir,** yani bir kişi örneklemin ya içinde ya dışında kalır. Sayılar örnekleme oranına göre
  ölçeklenir. Oranı değiştirirsen retention bunu belirtir, çünkü düşüşün bir kısmı artık ürünü değil örneklemi terk eden kişilerdir.

## Veri ne kadar saklanır

| Veri | Süre |
|---|---|
| Event'ler, sayfa görüntülemeleri ve parametreler | 90 gün |
| İlk görülme tarihleri ve kişi başına günlük aktivite | 13 ay |
| Span'lerdeki ham ziyaretçi ve kullanıcı kimlikleri | 7 gün |

Bu süreler sabittir. Analitik tabloları kimliğin kendisini değil özetini (hash) tutar. Bu takma adlı (pseudonymous) veridir ve
yine de kişisel veri sayılır.

## API

| Uç nokta | Cevabı |
|---|---|
| `GET /api/v1/analytics/activity` | Gün bazında DAU, WAU, MAU, yeni kullanıcılar ve stickiness |
| `GET /api/v1/analytics/retention` | Gün, hafta veya ay bazında cohort'lar |
| `GET /api/v1/analytics/overview` | Gün bazında sayfa görüntüleme, event ve oturum |
| `GET /api/v1/analytics/events` | Event isimleri veya bir parametrenin değerleri |
| `GET /api/v1/analytics/acquisition` | Referrer veya UTM değerine göre oturumlar |
| `GET /api/v1/analytics/funnel` | Oturum veya kişi bazında sıralı adımlar |

Ayrıntılar [API referansında](/tr/contracts/api), tasarım ve gerekçeleri [kontratta](/tr/contracts/analytics).

## Kapsam dışı

- Push bildirimleri, remote config ve A/B testi
- Session replay
- Bölge veya şehir. Yalnızca ülke çözülür
- Cihaz adları: model, cihazın bildirdiği gibi görünür, örneğin `iPhone15,2`
- Mobilde kurulum kaynağı takibi. UTM ve referrer yalnızca web'de var
