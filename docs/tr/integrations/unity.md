# openlog Unity SDK'sı {#openlog-unity-sdk}

Apache-2.0 · `io.github.onuragtas.openlog` paketi · iOS ve Android · bağımlılık yok

Unity oyunları için crash raporlama, ürün analitiği ve hata izleme: native crash'ler, ANR'ler, C# exception'ları, sahneler, özel event'ler, DAU,
retention ve her sürümün crash-free oranı.

[Mobil protokolü](/contracts/mobile-agent) uygular; Swift, Kotlin ve Dart SDK'ları da aynı protokolün diğer istemcileridir. IL2CPP ve yüksek
managed stripping ile Unity 6'da test edildi. Reflection kullanmaz, bu yüzden `link.xml` gerektirmez.

## Hızlı başlangıç {#quick-start}

Paketi **Window → Package Manager → + → Install package from git URL** ile ekle:

```
https://github.com/onuragtas/openlog.git?path=agents/unity
```

**Settings → Browser keys** altında, izin listesinde paket adın ve bundle identifier'ın olan bir mobil anahtar oluştur. Sonra oyun başlarken
SDK'yı başlat:

```csharp
using OpenLogUnity;
using UnityEngine;

public static class Telemetry
{
    [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.BeforeSceneLoad)]
    private static void Start()
    {
        OpenLog.Init(new OpenLogOptions
        {
            Key = "olb_…",
            Endpoint = "https://apm.resoft.org",
            CaptureCrashes = true,
        });
    }
}
```

openlog'u kendin çalıştırıyorsan endpoint olarak kendi sunucunu yaz. Kurulumun tamamı bu: yüklenen her sahne ekran olarak, exception'lar hata
olarak raporlanır, sürüm ve uygulama id'si de player ayarlarından gelir.

Editörden hiçbir şey gönderilmez, yani oyun üzerinde çalışman oyuncu sayılmaz. Orada denemek için `EnableInEditor` ayarla.

## Önemli olanı kaydet {#record-what-matters}

```csharp
OpenLog.RecordEvent("level_completed", new Dictionary<string, object> { ["level"] = 12, ["stars"] = 3 });
OpenLog.RecordTiming("level_loaded", 840);
OpenLog.Identify("acct_8f3a2b");   // kendi opak id'in, asla e-posta adresi veya isim değil; "" temizler

try { Save(); }
catch (Exception e) { OpenLog.RecordError(e); }
```

Küçük ve sabit bir event adı kümesi kullan, değişen şeyleri parametrelere koy. Sonuçlar konsolda **Browser** altında, oyununun **Analytics** ve
**Crashes** sekmelerinde görünür. [Ürün analitiğine](/tr/guide/product-analytics) bak.

## Seçenekler {#options}

| | Varsayılan | |
|---|---|---|
| `Key`, `Endpoint` | yok | Zorunlu. `olk_…` lisans anahtarı reddedilir: o gizlidir, bu ise oyunun içinde gelir |
| `CaptureCrashes` | `false` | Native crash'leri, ANR'leri ve bellek yetersizliği kapanmalarını bir sonraki açılışta raporlar |
| `CaptureExceptions` | `true` | Unity'nin logladığı exception'ları hata olarak raporlar |
| `TrackScenes` | `true` | Tek sahne olarak yüklenen her sahne için bir ekran raporlar |
| `AnalyticsCollection` | `true` | DAU, MAU ve retention için bir oyuncuyu açılışlar arasında tanır. Bunun için kalıcı bir id saklar ve bu onay gerektirebilir |
| `SampleRate` | `1` | `GET /v1/rum/config` operatörün değeriyle cevap verene kadar kullanılır; cevap gelince o geçerli olur |
| `FlushIntervalMs` | `5000` | Boş olmayan kuyruğun ne sıklıkla gönderileceği |
| `EnableInEditor` | `false` | Editörden de gönderir |

## Onay {#consent}

`AnalyticsCollection` açıkken SDK cihazda kalıcı bir id tutar; bunun oyuncularının onayını gerektirip gerektirmediği senin kararın. Gerektiriyorsa
kapalı başlat ve onay penceresinden aç:

```csharp
OpenLog.Init(new OpenLogOptions { Key = "olb_…", Endpoint = "https://apm.resoft.org", AnalyticsCollection = false });

OpenLog.SetAnalyticsCollection(true);
```

Kapatmak id'yi cihazdan siler ve açılışlar arasında hatırlanır. Daha önce gönderilmiş olanı silmez:
[bunun nasıl yapılacağına bak](/tr/guide/product-analytics).

## Crash'ler {#crashes}

| | Android | iOS |
|---|---|---|
| Native crash | ✅ Android 11'den itibaren, Android 12'den itibaren stack ile | ✅ iOS 14'ten itibaren, MetricKit üzerinden, stack ile |
| ANR | ✅ Android 11'den itibaren, thread dump ile | Hayır |
| Bellek yetersizliği | ✅ Android 11'den itibaren, ön planda | Hayır |
| C# exception | Hata olarak raporlanır | Hata olarak raporlanır |

**C# exception'ı crash sayılmaz.** Unity ondan sonra oyunu çalıştırmaya devam eder, yani saymak crash-free oranını anlamsız hale getirirdi. Bir hata
gerçekten oyuncu için oyunu bitiriyorsa bunu belirt:

```csharp
catch (SaveCorruptedException e) { OpenLog.RecordFatal(e); }
```

**Her karede fırlatılan aynı exception 10 saniyede bir gönderilir,** yani `Update` içindeki tek bir satır hız sınırını veya oyuncularının veri
kotasını tüketemez.

**Raporlar bir sonraki açılışta gelir,** çünkü çöken süreç hiçbir şey yapmaz: işletim sistemi crash'i kaydeder, SDK da sonradan okur. Bu yüzden sinyal
işleyici kurulmaz ve Unity'ninki veya başka bir SDK'nınki çalışmaya devam eder. Ne zaman ileteceğine MetricKit karar verir ve eski iOS sürümlerinde
bu bir sonraki açılıştan sonra olabilir.

**Native stack'leri okumak için sembollerini** release derlemesinin bir parçası olarak yükle: [Stack'lerini oku](/tr/guide/crash-reporting#read-your-stacks) bölümüne bak.

**Ne kadar test edildi?** IL2CPP ile Android 16 emülatöründe gerçek bir native crash, bir abort ve bir ANR uçtan uca raporlandı. iOS 26'lı gerçek bir iPhone'da da MetricKit üzerinden gerçek bir native crash ve bir abort raporlandı. Native stack'ler, Android'de `symbols.zip`, iOS'ta dSYM'ler yüklendikten sonra fonksiyon adlarıyla okundu. O iPhone'da raporlar sonraki bir iki açılışta geldi, ama Apple bir süre garanti etmiyor. Bellek yetersizliği ANR ile aynı yolu kullanıyor ama tetiklenmedi. Raporun telefondan sunucuya ağ üzerinden teslimi de denenmedi: rapor cihazdan okunup sunucuya elle gönderildi.

## Kapsam dışı {#not-covered}

WebGL, konsollar ve standalone derlemeler: paket orada derlenir ama crash yakalama hiçbir şey yapmaz ve test edilmemiştir.

## Geliştirme {#development}

Çekirdekte Unity'den hiçbir şey yok ve testleri editör lisansı olmadan çalışır:

```sh
cd 'agents/unity/Tests~/dotnet' && dotnet test
```

Paket bir projenin `manifest.json` dosyasında `testables` altında listelendiğinde Unity Test Runner'da da çalışırlar. İşletim sistemiyle konuşan kısımlar
(`Runtime/NativeCrashes.cs`, `Plugins/iOS`) yalnızca cihazda veya emülatörde çalışır.
