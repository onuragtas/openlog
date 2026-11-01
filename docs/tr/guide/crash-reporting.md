# Crash raporlama

Mobil uygulamanda neyin, hangi sürümde çöktüğünü ve kaç oturumu etkilediğini gör. [iOS](/tr/integrations/ios),
[Android](/tr/integrations/android) ve [Flutter](/tr/integrations/flutter) SDK'ları üzerinde çalışır.

Sonuçlar uygulama bazında **Browser** altında görünür: **Crashes** sekmesi neyin çöktüğünü listeler, **Releases** sekmesi her sürümün
crash-free oranını gösterir. Bir crash aynı zamanda normal bir hatadır, bu yüzden atama ve çözme işlemleri APM hata kutusunda yapılır.

Yakalama **varsayılan olarak kapalıdır**, çünkü sürecine genel bir işleyici kurar. Önceden kurulu bir işleyici çalışmaya devam
eder: her zaman ardından çağrılır, yani başka bir crash aracıyla yan yana çalışabilir.

## Neler kapsanıyor

| Platform | Raporlanan | Henüz raporlanmayan |
|---|---|---|
| iOS, macOS | Yakalanmamış Objective-C exception'ları. iOS 14 ve macOS 12 ve sonrasında MetricKit ile sinyaller, Mach exception'ları ve Swift trap'leri | Bellek yetersizliği |
| Android | Yakalanmamış JVM exception'ları. `androidContext` ile Android 11 ve sonrasında NDK crash'leri, ANR'ler ve ön planda bellek yetersizliği, Android 12'den itibaren native stack ile | Android 11 altında yalnızca JVM exception'ları |
| Unity | Android 11 ve sonrasında native crash, ANR ve bellek yetersizliği, iOS 14 ve sonrasında native crash. Bkz. [Unity kılavuzu](/tr/integrations/unity) | C# exception'ları crash değil hata sayılır |
| Flutter | Aşağıdaki gibi bağladığın yakalanmamış Dart hataları | Flutter motorunun veya bir eklentinin crash'i, ana uygulamada iOS veya Android SDK'sı da çalışmıyorsa |

Release derlemelerinin stack'leri karıştırılmış isimlerle veya adres olarak gelir. Okumak için [sembollerini yükle](#read-your-stacks).

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
        androidContext = context, // NDK crash'leri, ANR'ler ve bellek yetersizliği
    )
)
```

`androidContext` verilmezse yalnızca JVM exception'ları yakalanır. Diğerlerini Android kendisi kaydeder, bu yüzden bir sonraki
açılışta raporlanırlar.

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

## Stack'lerini oku {#read-your-stacks}

Release derlemesi Android'de `a.b.c`, native kodda ise bir kütüphane ve offset raporlar. `openlog-symbols` bunları fonksiyon, dosya ve
satır adlarına çeviren dosyaları yükler. Release derlemenin son adımı olarak ekle: semboller tek bir derlemeye aittir ve sonradan
yeniden üretilemez.

**Admin rolünde bir API key gerekir.** **Ayarlar → API keys** altında oluştur ve CI'ın secret'larına koy: yalnızca bir kez gösterilir.
Bu, uygulamandaki `olb_…` anahtarı değildir; o herkese açıktır ve yalnızca veri gönderebilir.

```sh
# Go gerekir. Bir kez derle, binary'yi CI araçlarının yanında tut.
git clone --depth 1 https://github.com/onuragtas/openlog && (cd openlog && go build -o ../openlog-symbols ./cmd/openlog-symbols)

export OPENLOG_API_KEY=ola_…

# iOS: derlemenin dSYM'leri
./openlog-symbols upload -endpoint https://apm.resoft.org -app uygulamam build/MyApp.xcarchive/dSYMs

# Android ve Unity: native kütüphaneler veya Unity'nin symbols.zip dosyasının açıldığı klasör
./openlog-symbols upload -endpoint https://apm.resoft.org -app uygulamam app/build/intermediates/merged_native_libs

# Android: R8 mapping'i, uygulamanın raporladığı sürümle aynı olmalı
./openlog-symbols upload -endpoint https://apm.resoft.org -app uygulamam -version 4.2.1 app/build/outputs/mapping/release/mapping.txt
```

`-app`, mobil anahtarındaki uygulama adıdır. Komutu bütün bir klasöre yöneltebilirsin: binary'leri kendisi bulur, aynı kütüphane hem
soyulmuş hem sembollü olarak varsa iyi olanı yükler. Her binary önce küçük bir tabloya indirilir, örneğin 75 MB'lık bir
`libil2cpp.so` yaklaşık 3 MB olarak yüklenir. Neyin gönderileceğini görmek için `-dry-run` ekle.

**Unity'de** **Build Settings → Create symbols.zip** ayarını *Debugging* yap ve yüklemeden önce zip'i APK'nın yanına aç.

**Semboller stack okunurken uygulanır,** yani sonradan yüklemek daha önce gelmiş crash'leri de düzeltir. Native kütüphane build
id'siyle, mapping sürümüyle eşleşir; aynısı için tekrar yüklemek öncekinin yerine geçer. **Crashes** sekmesi hangi kütüphanelerinin
hâlâ sembolsüz olduğunu söyler.

**Crash'ler nasıl gruplanır?** Semboller bir frame'i adlandırdığında crash'ler türlerine ve o fonksiyona göre gruplanır, böylece yeni
derleme onları yeniden açmaz. O zamana kadar tür ve mesaja göre gruplanırlar ve aynı mesajlı farklı crash'ler, örneğin düz bir
`SIGSEGV`, tek grupta toplanır. Hata kutusu her zaman tür ve mesaja göre gruplar, çünkü veri gelirken sembol okuyamaz.

::: warning Kapsam dışı olanlar
Flutter'ın `--split-debug-info` sembolleri desteklenmiyor, bu yüzden release derlemenin Dart stack'i karışık kalır. Swift fonksiyon
adları mangled biçimde görünür. Sembol dosyaları `DELETE /api/v1/symbols/{id}` ile silene kadar saklanır.
:::

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
[API referansına](/tr/contracts/api) bak. Tasarım [kontratta](/tr/contracts/mobile-crash).

Veriyi SDK olmadan kendin gönderiyorsan [mobil protokole](/tr/contracts/mobile-agent) bak.
