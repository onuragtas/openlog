# openlog mobil SDK'sı (Swift) {#openlog-mobile-sdk-swift}

Apple platformundaki bir uygulamadan ekranlar, hatalar, özel event'ler ve kimlik, OTLP/JSON üzerinden. [mobile-agent.md](/contracts/mobile-agent)
kontratını uygular, fazlasını değil: kontrat o dokümandır, bu onun istemcilerinden biridir ve Dart ile Kotlin istemcileri de aynısını uygular.

**Bağımlılık yok.** URLSession, zamanlayıcılar ve JSON gerekir, hepsi Foundation'da var. Bu, uygulamanın çıkarmayı seçemeyeceği tek kütüphanedir,
bu yüzden ona asla başka bir kütüphanenin sürümünü dayatmamalı.

## Hızlı başlangıç {#quick-start}

```swift
import OpenLog

let openlog = try OpenLog.start(Options(
    key: "olb_…",                        // mobil anahtar, Settings → Browser keys altından
    endpoint: "https://apm.resoft.org",
    appID: Bundle.main.bundleIdentifier ?? "",   // anahtarın uygulama izin listesinde olmalı
    serviceVersion: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "",
    deviceModel: "iPhone15,2",
    osName: "iOS",
    osVersion: UIDevice.current.systemVersion
))

openlog.record(screen: "/cart", isColdStart: true)
openlog.record(event: "checkout_started", params: ["plan": "pro"])
openlog.record(timing: "cart_priced", milliseconds: 42)
openlog.identify("acct_8f3a2b")          // çıkış yapınca ""
```

`start`, sessizce işe yaramayan bir SDK döndürmek yerine hata fırlatır: sunucunun reddedeceği seçenekler burada, geliştiricinin göreceği yerde
reddedilir.

## Uygulama kapanırken gönderim {#flushing-when-the-app-goes-away}

Platforma özel tek şey *ne zaman* gönderileceğidir. iOS bir süreci öldürmeden önce haber vermez, bu yüzden kaybolma ihtimali en yüksek olan son
batch'tir.

UIKit:

```swift
NotificationCenter.default.addObserver(
    forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: nil
) { _ in openlog.onAppBackgrounded() }
```

SwiftUI:

```swift
@Environment(\.scenePhase) private var scenePhase
// …
.onChange(of: scenePhase) { phase in
    if phase == .background { openlog.onAppBackgrounded() }
}
```

Ekranlar için `record(screen:)`'i navigasyonunun rota adını zaten bildiği yerde çağır. Değerleri yerleştirilmiş path'i değil, router'ının bildiği
rotayı (`/orders/:id`) gönder: openlog gelen her şeyi normalize eder ama hangi parçanın id olduğunu yalnızca router'ın bilir.

## Anahtar herkese açık, bunda sorun yok {#the-key-is-public-—-and-that-is-fine}

Mobil anahtar uygulama binary'sinin içinde gelir, yani uygulamayı indiren herkeste vardır. Buradaki hiçbir şey anahtarın gizli olmasına
dayanmaz: anahtar tek bir endpoint'e yetki verir, her veri sunucu tarafında anahtarın söylemesine izin verilen şeye göre yeniden yazılır, hız
sınırı anahtar başınadır ve iptal edildiğinde bir dakika içinde geçerli olur.

Uygulama id'sini **uygulama kendisi beyan eder**. Bir derlemeden alınan anahtarın gelişigüzel yeniden kullanılmasını zorlaştırır; ikinci bir
kimlik doğrulama faktörü değildir ve bu SDK öyleymiş gibi davranmaz. Tehdit modelinin tamamı [rum.md §3.5](/contracts/rum) içinde.

## Seçenekler {#options}

| | Varsayılan | |
|---|---|---|
| `key`, `endpoint`, `appID` | — | Zorunlu. `appID` anahtarın izin listesinde olmalı, yoksa her istek `403` alır |
| `serviceVersion` | `""` | Derleme. `service.name` anahtardan gelir; bu yalnızca onun içindeki bir derlemeyi etiketler ve release health bununla gruplar |
| `deviceModel`, `deviceManufacturer`, `osName`, `osVersion` | `""` / `"Apple"` | Platformun kendisi hakkında bildirdikleri |
| `sampleRate` | `1` | `GET /v1/rum/config` operatörün değeriyle cevap verene kadar kullanılır; cevap gelince o geçerli olur |
| `maxBatchSize` | `32` | Erken gönderimden önce tamponda tutulan span sayısı |
| `flushInterval` | `5` (saniye) | Boş olmayan kuyruğun ne sıklıkla gönderileceği |
| `debug` | `false` | SDK'nın yaptıklarını loglar |
| `analyticsCollection` | `true` | DAU, MAU ve retention için bir kişiyi açılışlar arasında tanır. Bunun için kalıcı bir id saklar ve bu onay gerektirebilir: [Ürün analitiğine](/tr/guide/product-analytics) bak |
| `captureCrashes`, `crashDirectory` | `false`, Application Support | Crash'leri sonraki bir açılışta raporlar. [Crash raporlamaya](/tr/guide/crash-reporting) bak |
| `visitorDefaults` | `.standard` | Ziyaretçi id'sinin saklandığı yer. `nil` verirsen ne saklanır ne gönderilir |

## Örnekleme ve oturumlar {#sampling-and-sessions}

Oturum bir kişi değil, bir ziyarettir: 32 rastgele karakter, cihaz veya kullanıcıyla ilgili hiçbir şeyden türetilmez, 30 dakika hareketsizlikten
sonra sona erer ve en fazla 4 saat sürer.

Örnekleme event başına değil, **oturum başına bir kez** kararlaştırılır: yarım oturum daha ucuz bir oturum değil, okunamayan bir oturumdur.
Örneklemin dışında kalan oturum hiçbir şey göndermez. Saklanan her span'in ağırlığı sunucu tarafında anahtardan uygulanır, yani bir uygulama bir
oran beyan ederek kendi trafiğini şişiremez.

## Neleri yapmaz {#what-it-does-not-do}

**Bellek yetersizliği raporu yok.** `captureCrashes` açıkken exception'lar, sinyaller ve trap'ler raporlanır: [Crash raporlamaya](/tr/guide/crash-reporting) bak.

**Native stack'leri okumak için dSYM'lerini yükle,** bunu release derlemesinin bir adımı yap: bkz. [Stack'lerini oku](/tr/guide/crash-reporting#read-your-stacks). Swift fonksiyon adları mangled biçimde kalır.

**Attestation yok.** Uygulama izin listesinin ileride güçlendirileceği yol App Attest'tir ve bilerek öyle bir iddiada bulunulmuyor.

## Geliştirme {#development}

```sh
make build
make test     # macOS'ta çalışır; kütüphane iOS, tvOS ve watchOS'u da hedefler
```
