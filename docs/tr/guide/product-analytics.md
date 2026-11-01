# Ürün analitiği

Uygulamanı kaç kişinin kullandığını, geri gelip gelmediklerini, ne yaptıklarını ve nereden geldiklerini gör: DAU, WAU, MAU, yeni
kullanıcılar, retention, funnel, event'ler ve trafik kaynakları. Google Analytics veya Firebase Analytics kullandıysan aynı
konuları kapsar, üstelik kendi sunucunda çalışır.

[Tarayıcı](/integrations/browser), [iOS](/integrations/ios), [Android](/integrations/android) ve
[Flutter](/integrations/flutter) ve [Unity](/integrations/unity) SDK'ları üzerinde çalışır, başka bir şey kurman gerekmez.

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

Ayrıntılar [API referansında](/contracts/api), tasarım ve gerekçeleri [kontratta](/contracts/analytics) (İngilizce).

## Kapsam dışı

- Push bildirimleri, remote config ve A/B testi
- Session replay
- Bölge veya şehir. Ülke bilgisi için ingest'in önünde `OPENLOG_RUM_GEO_HEADER` ile tanımlı bir CDN veya proxy gerekir
- Mobilde kurulum kaynağı takibi. UTM ve referrer yalnızca web'de var
