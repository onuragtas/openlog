# Crash raporlama

Mobil uygulamanda neyin, hangi sürümde çöktüğünü ve kaç oturumu etkilediğini gör. [iOS](/integrations/ios),
[Android](/integrations/android) ve [Flutter](/integrations/flutter) SDK'ları üzerinde çalışır.

Sonuçlar uygulama bazında **Browser** altında görünür: **Crashes** sekmesi neyin çöktüğünü listeler, **Releases** sekmesi her sürümün
crash-free oranını gösterir. Bir crash aynı zamanda normal bir hatadır, bu yüzden atama ve çözme işlemleri APM hata kutusunda yapılır.

Yakalama **varsayılan olarak kapalıdır**, çünkü sürecine genel bir işleyici kurar. Önceden kurulu bir işleyici çalışmaya devam
eder: her zaman ardından çağrılır, yani başka bir crash aracıyla yan yana çalışabilir.

## Neler kapsanıyor

| Platform | Raporlanan | Henüz raporlanmayan |
|---|---|---|
| iOS, macOS | Yakalanmamış Objective-C exception'ları. iOS 14 ve macOS 12 ve sonrasında MetricKit ile sinyaller, Mach exception'ları ve Swift trap'leri | Bellek yetersizliği |
| Android | Yakalanmamış JVM exception'ları | **NDK crash'leri ve ANR'ler** |
| Flutter | Aşağıdaki gibi bağladığın yakalanmamış Dart hataları | Flutter motorunun veya bir eklentinin crash'i, ana uygulamada iOS veya Android SDK'sı da çalışmıyorsa |

::: warning Stack'ler henüz okunur hale getirilmiyor
Release derlemeleri stack'leri cihazın gönderdiği haliyle raporlar: Android'de karıştırılmış isimler, iOS'ta adresler. R8 mapping ve
dSYM yükleme henüz yok. Aynı nedenle crash'ler stack'e göre değil yalnızca tür ve mesaja göre gruplanır, böylece yeni bir derleme
her grubu yeniden açmaz. Aynı tür ve mesaja sahip farklı crash'ler, örneğin düz bir `SIGSEGV`, tek grupta toplanır.
:::

## Kurulum

::: tip Hangi adres?
Örnekler barındırılan servisi kullanır: `https://apm.resoft.org`. openlog'u kendin çalıştırıyorsan kendi sunucunun adresini yaz.
:::

### iOS

```swift
let openlog = try OpenLog.start(Options(
    key: "olb_…",
    endpoint: "https://apm.resoft.org",
    appID: Bundle.main.bundleIdentifier ?? "",
    serviceVersion: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "",
    captureCrashes: true
))
```

Sistem MetricKit raporlarını sonraki bir açılışta iletir, gecikme iOS sürümüne göre değişir. Rapor oturumu belirtmez, bu yüzden SDK
derleme eşleşiyorsa raporu son açılışın bittiği oturuma bağlar. Eşleşmiyorsa oturumu bilinmiyor olarak işaretler.

### Android

```kotlin
val openlog = OpenLog.start(
    Options(
        key = "olb_…",
        endpoint = "https://apm.resoft.org",
        appId = BuildConfig.APPLICATION_ID,
        serviceVersion = BuildConfig.VERSION_NAME,
        captureCrashes = true,
        crashDirectory = context.filesDir.path,
    )
)
```

`crashDirectory` olmadan hiçbir şey yakalanmaz. Başlangıçtaki crash'leri de görmek için SDK'yı erken, `Application.onCreate` içinde başlat.

### Flutter

SDK saf Dart'tır ve Flutter'ın işleyicilerini kendisi kuramaz, bu yüzden bağla:

```dart
final openlog = Openlog.init(OpenlogOptions(
  key: 'olb_…',
  endpoint: 'https://apm.resoft.org',
  appId: 'com.example.shop',
  serviceVersion: '4.2.1',
  captureCrashes: true,
));

FlutterError.onError = (details) {
  FlutterError.presentError(details);
  openlog.recordFatal(details.exception, stack: details.stack);
};
PlatformDispatcher.instance.onError = (error, stack) {
  openlog.recordFatal(error, stack: stack);
  return true;
};
```

## Bir crash sunucuya nasıl ulaşır

1. Çöken süreç tek bir dosya yazar ve başka bir şey yapmaz. Ağa dokunmaz.
2. Sonraki açılış dosyayı gönderir ve sunucu kesin cevap verince siler. Bağlantı yoksa veya sunucu beklemeni söylerse dosya bir
   sonraki açılışa kalır.
3. Crash kendi zamanını, oturumunu ve sürümünü korur. 1.0'daki bir crash, 1.1'e güncellendikten sonra raporlansa da 1.0'a yazılır.

Sınırlar:

- Bir cihazda en fazla 10 rapor bekler. Crash döngüsünde ilkleri tutulur.
- Sunucuya ulaştığında 7 günden eski olan rapor atılır, çünkü saklanan aralığın dışına düşerdi.
- Örneklemin dışında kalan oturum hiçbir şey göndermez, crash dahil. Crash-free oranı doğru kalır, çünkü oturumlar ve crash'ler
  birlikte örneklenir.

## Crash-free oranı

**Releases** sekmesi her sürüm için iki sayı gösterir:

| Sütun | Anlamı |
|---|---|
| **Crash-free** | Crash yaşamayan oturumların oranı. **Sürüm crash raporlayamıyorsa `—` görünür**, çünkü %100 kimsenin ölçmediği bir şeyi iddia ederdi |
| **Error-free** | Hiç hata olmayan oturumların oranı, kodunun yakaladığı hatalar dahil |

Oturum sayıları ve oranlar 30 gün geriye gider, openlog'un bu sürümüne geçtiğin andan itibaren birikmeye başlar.

## Alarm kur

Crash'ler span olduğu için `oql` alarm kuralı yeni bir kural türü gerektirmeden onları izleyebilir:

```sql
SELECT count(*) FROM Span WHERE openlog.rum.crash.kind != ''
```

## API

`GET /api/v1/rum/crashes?app=` crash'leri gruplara göre listeler, `GET /api/v1/rum/releases?app=` oranları döndürür.
[API referansına](/contracts/api) bak. Tasarım [kontratta](/contracts/mobile-crash) (İngilizce).

Veriyi SDK olmadan kendin gönderiyorsan [mobil protokole](/contracts/mobile-agent) bak.
