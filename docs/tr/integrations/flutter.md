# openlog mobil SDK'sı (Dart) {#openlog-mobile-sdk-dart}

Bir mobil uygulamadan ekranlar, hatalar, özel event'ler ve kimlik, OTLP/JSON üzerinden. [mobile-agent.md](/contracts/mobile-agent)
kontratını uygular, fazlasını değil: kontrat o dokümandır, bu ise onun istemcilerinden biridir.

**Saf Dart, bağımlılık yok.** HTTP, zamanlayıcılar ve JSON gerekir, hepsi de Dart SDK'sında var. Flutter'a bağımlı olmak onu hiçbir kazanç
olmadan Dart sunucularının ve CLI'ların dışında bırakırdı. `package:http`'ye bağımlı olmak da uygulamanın çıkarmayı seçemeyeceği tek
kütüphaneye bir sürüm kısıtı koyardı.

## Hızlı başlangıç {#quick-start}

```dart
import 'package:openlog/openlog.dart';

final openlog = Openlog.init(OpenlogOptions(
  key: 'olb_…',                       // mobil anahtar, Settings → Browser keys altından
  endpoint: 'https://apm.resoft.org',
  appId: 'com.example.shop',          // anahtarın uygulama izin listesinde olmalı
  serviceVersion: '4.2.1',
  osName: 'Android', osVersion: '15',
  deviceManufacturer: 'Samsung', deviceModel: 'SM-S911B',
));

openlog.recordScreen('/cart', isColdStart: true);
openlog.recordEvent('checkout_started', params: {'plan': 'pro'});
openlog.recordTiming('cart_priced', 42);
openlog.identify('acct_8f3a2b');      // çıkış yapınca ''
```

## Flutter'dan kullanmak {#using-it-from-flutter}

Flutter'ın değiştirdiği tek şey ne zaman gönderileceğidir. Mobil platform bir süreci öldürmeden önce haber vermez, bu yüzden kaybolma ihtimali
en yüksek olan son batch'tir. Uygulama arka plana geçince gönder:

```dart
class _AppLifecycle extends WidgetsBindingObserver {
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.paused) openlog.onAppBackgrounded();
  }
}

WidgetsBinding.instance.addObserver(_AppLifecycle());
```

Ekranlar için `recordScreen`'i bir `NavigatorObserver`'dan veya router'ının rota adını zaten bildiği bir yerden çağır. Değerleri yerleştirilmiş
path'i değil, navigator'ının bildiği rotayı (`/orders/:id`) gönder: openlog gelen her şeyi normalize eder ama hangi parçanın id olduğunu yalnızca
router'ın bilir.

## Anahtar herkese açık, bunda sorun yok {#the-key-is-public-—-and-that-is-fine}

Mobil anahtar uygulama binary'sinin içinde gelir, yani uygulamayı indiren herkeste vardır. Buradaki hiçbir şey anahtarın gizli olmasına
dayanmaz: anahtar tek bir endpoint'e yetki verir, her veri sunucu tarafında anahtarın söylemesine izin verilen şeye göre yeniden yazılır, hız
sınırı anahtar başınadır ve iptal edildiğinde bir dakika içinde geçerli olur.

Uygulama id'sini **uygulama kendisi beyan eder**. Bir derlemeden alınan anahtarın gelişigüzel yeniden kullanılmasını zorlaştırır; ikinci bir
kimlik doğrulama faktörü değildir ve bu SDK öyleymiş gibi davranmaz. Tehdit modelinin tamamı [rum.md §3.5](/contracts/rum) içinde.

## Seçenekler {#options}

| | Varsayılan | |
|---|---|---|
| `key`, `endpoint`, `appId` | — | Zorunlu. `appId` anahtarın izin listesinde olmalı, yoksa her istek `403` alır |
| `serviceVersion` | `''` | Derleme. `service.name` anahtardan gelir; bu yalnızca onun içindeki bir derlemeyi etiketler ve release health bununla gruplar |
| `deviceModel`, `deviceManufacturer`, `osName`, `osVersion` | `''` | Platformun kendisi hakkında bildirdikleri |
| `sampleRate` | `1` | `GET /v1/rum/config` operatörün değeriyle cevap verene kadar kullanılır; cevap gelince o geçerli olur |
| `maxBatchSize` | `32` | Erken gönderimden önce tamponda tutulan span sayısı |
| `flushInterval` | `5s` | Boş olmayan kuyruğun ne sıklıkla gönderileceği |
| `debug` | `false` | SDK'nın yaptıklarını loglar |
| `analyticsCollection` | `true` | DAU, MAU ve retention için bir kişiyi açılışlar arasında tanır. Bunun için kalıcı bir id saklar ve bu onay gerektirebilir. [Ürün analitiğine](/tr/guide/product-analytics) bak |
| `captureCrashes` | `false` | Bu derlemenin yakalanmamış hataları `recordFatal` ile raporladığını belirtir. [Crash raporlamaya](/tr/guide/crash-reporting) bak |
| `visitorDirectory` | yok | Uygulamaya özel bir klasör, örneğin `path_provider`'dan. **Bu olmadan ziyaretçi id'si gönderilmez** ve DAU, MAU ve retention boş kalır |

## Örnekleme ve oturumlar {#sampling-and-sessions}

Oturum bir kişi değil, bir ziyarettir: 32 rastgele karakter, cihaz veya kullanıcıyla ilgili hiçbir şeyden türetilmez, 30 dakika hareketsizlikten
sonra sona erer ve en fazla 4 saat sürer.

Örnekleme event başına değil, **oturum başına bir kez** kararlaştırılır: yarım oturum daha ucuz bir oturum değil, okunamayan bir oturumdur.
Örneklemin dışında kalan oturum hiçbir şey göndermez. Saklanan her span'in ağırlığı sunucu tarafında anahtardan uygulanır, yani bir uygulama bir
oran beyan ederek kendi trafiğini şişiremez.

## Neleri yapmaz {#what-it-does-not-do}

**Native crash yok.** Yakalanmamış Dart hataları `recordFatal` ile raporlanır: [Crash raporlamaya](/tr/guide/crash-reporting) bak. Flutter motorunun veya bir eklentinin crash'i için ana uygulamada Swift veya Kotlin SDK'sı gerekir.

**Dart stack'leri release derlemede karışık kalır.** Flutter'ın `--split-debug-info` sembolleri desteklenmiyor. Uygulamanın native tarafı destekleniyor: ana uygulama iOS veya Android SDK'sını çalıştırıyorsa [dSYM'lerini ve R8 mapping'ini yükle](/tr/guide/crash-reporting#read-your-stacks).

**Attestation yok.** Uygulama izin listesinin ileride güçlendirileceği yol Play Integrity ve App Attest'tir ve bilerek öyle bir iddiada bulunulmuyor.

## Geliştirme {#development}

```sh
make install     # dart pub get, bir container içinde (host SDK'sını kullanmak için DART=local)
make analyze     # typecheck ve lint tek komutta, uyarılar hata sayılır
make test
```
