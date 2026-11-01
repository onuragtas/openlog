# openlog mobil SDK'sı (Kotlin/JVM) {#openlog-mobile-sdk-kotlin-jvm}

Bir Android veya JVM uygulamasından ekranlar, hatalar, özel event'ler ve kimlik, OTLP/JSON üzerinden. [mobile-agent.md](/tr/contracts/mobile-agent)
kontratını uygular, fazlasını değil: kontrat o dokümandır, bu onun istemcilerinden biridir ve Dart ile Swift istemcileri de aynısını uygular.

**Düz Kotlin/JVM, Android kütüphanesi değil.** HTTP, zamanlayıcılar ve JSON gerekir, bunların hiçbiri Android API'si değildir. Bir
`com.android.library` projesi, kullanmadığı Android SDK'sını kodu test etmek için CI'a sokar ve hiçbir kazanç olmadan onu JVM servislerinin ve
masaüstü istemcilerinin dışında bırakırdı. Android'den başka herhangi bir JVM bağımlılığı gibi kullan.

**Bağımlılık yok**, JSON için bile: `HttpURLConnection` ve paketin içindeki küçük bir yazıcı yeterli. Bu, uygulamanın çıkarmayı seçemeyeceği tek
kütüphanedir, bu yüzden ona başka bir kütüphanenin sürümünü dayatmamalı: ne OkHttp ne de kotlinx-serialization.

## Hızlı başlangıç {#quick-start}

```kotlin
val openlog = OpenLog.start(
    Options(
        key = "olb_…",                    // mobil anahtar, Settings → Browser keys altından
        endpoint = "https://apm.resoft.org",
        appId = BuildConfig.APPLICATION_ID,   // anahtarın uygulama izin listesinde olmalı
        serviceVersion = BuildConfig.VERSION_NAME,
        deviceManufacturer = Build.MANUFACTURER,
        deviceModel = Build.MODEL,
        osVersion = Build.VERSION.RELEASE,
    )
)

openlog.recordScreen("/cart", isColdStart = true)
openlog.recordEvent("checkout_started", mapOf("plan" to "pro"))
openlog.recordTiming("cart_priced", 42)
openlog.identify("acct_8f3a2b")           // çıkış yapınca ""
```

`start`, sessizce işe yaramayan bir SDK döndürmek yerine `ConfigException` fırlatır: sunucunun reddedeceği seçenekler burada, geliştiricinin
göreceği yerde reddedilir.

## Uygulama kapanırken gönderim {#flushing-when-the-app-goes-away}

Platforma özel tek şey *ne zaman* gönderileceğidir. Android bir süreci öldürmeden önce haber vermez, bu yüzden kaybolma ihtimali en yüksek olan
son batch'tir:

```kotlin
class App : Application() {
    override fun onCreate() {
        super.onCreate()
        registerActivityLifecycleCallbacks(object : ActivityLifecycleCallbacks {
            override fun onActivityStopped(activity: Activity) = openlog.onAppBackgrounded()
            // diğer callback'ler boş kalabilir
        })
    }
}
```

`androidx.lifecycle` ile `ProcessLifecycleOwner`'ı `ON_STOP` için dinlemek aynı işi tek satırda yapar ve activity başına değil, uygulama başına
bir kez tetiklenir.

Ekranlar için `recordScreen`'i navigasyonunun rota adını zaten bildiği yerde çağır. Değerleri yerleştirilmiş path'i değil, navigator'ının bildiği
rotayı (`/orders/:id`) gönder: openlog gelen her şeyi normalize eder ama hangi parçanın id olduğunu yalnızca navigator'ın bilir.

## Anahtar herkese açık, bunda sorun yok {#the-key-is-public-—-and-that-is-fine}

Mobil anahtar APK'nın içinde gelir, yani uygulamayı indiren herkeste vardır. Buradaki hiçbir şey anahtarın gizli olmasına dayanmaz: anahtar tek
bir endpoint'e yetki verir, her veri sunucu tarafında anahtarın söylemesine izin verilen şeye göre yeniden yazılır, hız sınırı anahtar başınadır ve
iptal edildiğinde bir dakika içinde geçerli olur.

Uygulama id'sini **uygulama kendisi beyan eder**. Bir derlemeden alınan anahtarın gelişigüzel yeniden kullanılmasını zorlaştırır; ikinci bir
kimlik doğrulama faktörü değildir ve bu SDK öyleymiş gibi davranmaz. Tehdit modelinin tamamı [rum.md §3.5](/tr/contracts/rum) içinde.

## Seçenekler {#options}

| | Varsayılan | |
|---|---|---|
| `key`, `endpoint`, `appId` | — | Zorunlu. `appId` anahtarın izin listesinde olmalı, yoksa her istek `403` alır |
| `serviceVersion` | `""` | Derleme. `service.name` anahtardan gelir; bu yalnızca onun içindeki bir derlemeyi etiketler ve release health bununla gruplar |
| `deviceModel`, `deviceManufacturer`, `osName`, `osVersion` | `""` / `"Android"` | Platformun kendisi hakkında bildirdikleri |
| `sampleRate` | `1.0` | `GET /v1/rum/config` operatörün değeriyle cevap verene kadar kullanılır; cevap gelince o geçerli olur |
| `maxBatchSize` | `32` | Erken gönderimden önce tamponda tutulan span sayısı |
| `flushIntervalMs` | `5000` | Boş olmayan kuyruğun ne sıklıkla gönderileceği |
| `debug` | `false` | SDK'nın yaptıklarını loglar |
| `analyticsCollection` | `true` | DAU, MAU ve retention için bir kişiyi açılışlar arasında tanır. Bunun için kalıcı bir id saklar ve bu onay gerektirebilir: [Ürün analitiğine](/tr/guide/product-analytics) bak |
| `captureCrashes`, `crashDirectory` | `false`, yok | Yakalanmamış JVM exception'larını bir sonraki açılışta raporlar. İkisi de gerekir. [Crash raporlamaya](/tr/guide/crash-reporting) bak |
| `androidContext` | yok | `Context` nesnen. `captureCrashes` ile birlikte Android 11 ve sonrasında NDK crash'lerini, ANR'leri ve ön planda bellek yetersizliğini de raporlar. |
| `visitorDirectory` | yok | Uygulamaya özel bir klasör, örneğin `context.filesDir.path`. **Bu olmadan ziyaretçi id'si gönderilmez** ve DAU, MAU ve retention boş kalır |

## Örnekleme ve oturumlar {#sampling-and-sessions}

Oturum bir kişi değil, bir ziyarettir: 32 rastgele karakter, cihaz veya kullanıcıyla ilgili hiçbir şeyden türetilmez, 30 dakika hareketsizlikten
sonra sona erer ve en fazla 4 saat sürer.

Örnekleme event başına değil, **oturum başına bir kez** kararlaştırılır: yarım oturum daha ucuz bir oturum değil, okunamayan bir oturumdur.
Örneklemin dışında kalan oturum hiçbir şey göndermez. Saklanan her span'in ağırlığı sunucu tarafında anahtardan uygulanır, yani bir uygulama bir
oran beyan ederek kendi trafiğini şişiremez.

## Neleri yapmaz {#what-it-does-not-do}

**NDK crash'leri ve ANR'ler için `androidContext` gerekir.** `captureCrashes` açıkken yakalanmamış JVM exception'ları her JVM'de raporlanır.
`Context`'ini de verirsen NDK crash'leri, ANR'ler ve ön plandaki bellek yetersizliği kapanmaları Android 11 ve sonrasında Android'in kaydettiklerinden
raporlanır: [Crash raporlamaya](/tr/guide/crash-reporting) bak. Bu, sistemin kaydını reflection ile okur ve o sınıfların yerine geçen taklitlerle test
edilmiştir, henüz gerçek bir Android uygulamasında değil.

**Stack'ler için sembollerin gerekir.** R8 mapping'ini ve native kütüphanelerini release derlemesinde yükle:
[Stack'lerini oku](/tr/guide/crash-reporting#read-your-stacks) bölümüne bak.

**Attestation yok.** Uygulama izin listesinin ileride güçlendirileceği yol Play Integrity'dir ve bilerek öyle bir iddiada bulunulmuyor.

## Geliştirme {#development}

```sh
make test     # ./gradlew test — wrapper Gradle sürümünü sabitler, önceden kurulacak bir şey yok
make build
```

Kütüphane, derlemeyi hangi JDK çalıştırıyorsa çalıştırsın **Java 11 bytecode'u** hedefler: Android'in yükleyebildiği budur ve daha yeni bir hedef,
artifact'ı hedeflendiği yerde kullanılamaz hale getirirdi.
