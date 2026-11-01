# Sözleşme: Mobil crash raporlama (D-151) {#contract-mobile-crash-reporting-d-151}

**Durum: yapıldı,** Flutter sembolleri hariç (§3). Gruplama öneriden farklılaştı: bkz. §4. Kullanıcı kılavuzu: [Crash raporlama](/tr/guide/crash-reporting). İlgili: [rum.md](/tr/contracts/rum) §2.7, [apm.md](/tr/contracts/apm) §3.

**Tek cümlede sorun:** yakalanmamış bir crash, sürecin göndermek için hayatta kaldığı bir `error` span'i değildir, bu yüzden ölmekte olan süreç
veya işletim sistemi tarafından yazılıp sonraki açılışta iletilmesi gerekir.

**Crash yine de bir hata span'idir.** `POST /v1/rum` üzerinden geçer ve aynı gruplama ve çözüm iş akışıyla mevcut hata kutusuna düşer.
`openlog.rum.event` `error` kalır, çünkü `rum_sessions` hataları bu değere göre sayar ve view'i değiştirilemez (rum.md §3.7). Yeni olan, bu
hatanın fatal olduğunu söyleyen tek bir attribute.

## 1. Payload {#_1-the-payload}

mobile-agent.md §3'teki gibi bir hata span'i, artı:

| Attribute | Zorunlu | Değer |
|---|---|---|
| `openlog.rum.crash.kind` | evet | `uncaught_exception`, `signal`, `anr` veya `out_of_memory`. Başka bir şey atılır ve span sıradan bir hata olarak kalır |
| `openlog.rum.crash.id` | evet | 32 rastgele hex karakter, crash kaydedilirken sabitlenir, yani yeniden denenen teslimat tek crash olur |
| `openlog.rum.crash.session_unknown` | hayır | Rapor oturum belirtmediği için oturum bir tahminse `true` |

Teslimatın geç olmasından üç kural çıkar:

- **`session.id`, geçerli oturumun değil, diskten okunan crash olmuş oturumun id'sidir.** Aksi halde crash olan oturum crash-free görünür, masum
  sonraki oturum ise öyle görünmez.
- **`service.version`, crash olan build'in sürümüdür.** Güncellemeden sonra eski sürümden gelen crash yenisinin hanesine yazılmamalı. Resource attribute'ları
  istek başına uygulanır, yani **crash'lenen build başına bir istek**. Bu yüzden `service.version` crash dosyasına yazılmalıdır.
- **Span'in zaman damgası, 7 güne kadar eskiyse gönderildiği gibi tutulur.** Daha eskisi trace retention'ının ötesine düşeceği için `crash_too_old` ile
  atılır. Gelecekteki bir zaman damgası sunucunun zamanıyla değiştirilir.

Sampling dışı kalan oturum, crash dahil hiçbir şey göndermez (mobile-agent.md §4). Crash-free oranı doğru kalır, çünkü oturumlar ve crash'ler
aynı şekilde ağırlıklandırılır. Örneklenmemiş oturumların crash'lerini raporlamak, tüm crash'leri oturumların örneklenmiş bir payına böler ve oranı olduğundan düşük gösterirdi.

## 2. Yakalama {#_2-capture}

Her SDK'da varsayılan olarak kapalıdır (`captureCrashes`), çünkü başkasının sürecine genel bir işleyici kurar.
Önceden kurulu bir işleyici **her zaman zincirlenir, asla değiştirilmez**, yoksa uygulamanın kendi crash aracı sessizce çalışmayı bırakırdı.

| Platform | Mekanizma | Kapsam | v1 |
|---|---|---|---|
| Kotlin/JVM | `Thread.setDefaultUncaughtExceptionHandler` dosya yazar | JVM exception'ları | ✅ |
| Android | `androidContext` ile, sonraki açılışta okunan, reflection ile `ApplicationExitInfo` (API 30+) | native sinyaller, ANR, düşük bellek öldürmeleri | ✅ |
| Swift | Sistemin ilettiği MetricKit `MXCrashDiagnostic` | sinyaller, Mach exception'ları, Swift trap'leri | ✅ |
| Swift | `NSSetUncaughtExceptionHandler` | Objective-C exception'ları, ad ve sebep için | ✅ |
| Unity, Android | Sonraki açılışta okunan, JNI ile `ApplicationExitInfo` (API 30+) | native crash'ler, ANR, ön planda düşük bellek öldürmeleri | ✅ |
| Unity, iOS | Küçük bir Objective-C eklentisi ile MetricKit | sinyaller, Mach exception'ları | ✅ |
| Dart/Flutter | `FlutterError.onError` ve `PlatformDispatcher.onError`'a bağlanan `recordFatal` | Dart exception'ları | ✅ |

**Neden kendi signal handler'ımız değil?** Signal handler yalnızca async-signal-safe fonksiyonlar çağırabilir ve bunu yanlış yapmak crash'i gizler,
daha kötüsü uygulamayı tam düşerken kilitler. MetricKit'te yakalamayı işletim sistemi yapar ve SDK'ya maliyeti yalnızca
`#if canImport(MetricKit)` arkasında bir sistem framework import'udur.

**MetricKit'in bedelleri, açıkça.** iOS 14 veya üstünü ister ve eski sürümlerde teslimat daha geç olabilir. Build numarasını (`CFBundleVersion`) belirtir, raporda geçen sürümü değil, yani ikisi
yalnızca şu an çalışan build için eşleştirilebilir. Daha eski bir build, son oturumun çalıştırdığı sürüm sayılır ve Unity'nin varsayılan `0`'ı gibi hiç değişmeyen bir build numarası eski crash'i yeni sürüme yazar.
Oturum id'si taşımaz, bu yüzden SDK son oturumu ve sürümü diskte tutar ve mümkün olduğunca ekler; eşleştiremediği crash yeni, tek span'lik bir oturumun `session.id` değerini alır ve `openlog.rum.crash.session_unknown` ile işaretlenir.
Yakalanmamış Objective-C exception'ı hem işleyici hem MetricKit tarafından iki kez görülür: oturum bir kez sayılır, hata kutusu iki kez.

**Android düz Kotlin/JVM kalır.** Çekirdek SDK'nın Android bağımlılığı yok ve bu korunmaya değer. Bu yüzden ilk önerilen isteğe bağlı `openlog-android` artifact'i yerine `androidContext` `Any` tipindedir
ve `ApplicationExitInfo` reflection ile okunur: ikinci artifact, unutulacak ikinci bir şey olurdu. O kayıttaki bir Java crash'i atlanır, çünkü işleyici onu zaten raporlamıştır.

**Flutter.** Yakalanmamış bir Dart hatası genellikle süreci öldürmez, yani span diske yazılmak yerine hemen gönderilebilir. Uygulama onu fatal olarak bağladığı
sürece `crash.kind = uncaught_exception` ile gönderilir; SDK saf Dart kalır, yani bağlama belgelenmiş bir kod parçasıdır. Flutter motorunun veya bir
eklentinin crash'i native'dir ve ana uygulama içeriyorsa Swift veya Android SDK'sı tarafından yakalanır.

### 2.1 Crash dosyası {#_2-1-the-crash-file}

- Uygulamanın verdiği dizin (`crashDirectory`): Kotlin ve Dart SDK'larının dizin bulacak platform API'si yok.
- Crash başına bir JSON dosyası: span artı resource attribute'ları, geçici bir ada yazılır ve rename edilir.
- En fazla 10 dosya. Dolunca **en eskiler silinir**. Crash döngüsü cihazı doldurmamalı ve döngünün ilk crash'leri geri kalanların söyleyeceğini söyler.
- Yalnızca `2xx` veya `429` dışı bir `4xx` sonrasında silinir. Yeniden denemeler mobile-agent.md §6'yı izler.
- İşleyici tek bir yazma yapar ve döner. Event kuyruğunu asla flush etmez, ağa asla dokunmaz.

## 3. Symbolication {#_3-symbolication}

Bu olmadan release build'leri `a.b.c()` ve `0x1042f3a8c` raporlar. Source map'leri izler ([api.md](/tr/contracts/api#crash-symbols)): index PostgreSQL'de
(`0106_crash_symbols.sql`), dosyalar aynı depolama ayarları altında `internal/objstore` içinde, okuma anında uygulanır.

| Tür | Girdi | Anahtar | Parse eden |
|---|---|---|---|
| `r8` | `mapping.txt` | uygulama, sürüm | `internal/symbols` |
| `native` | CLI'ın dSYM veya ELF'ten yaptığı bir tablo | uygulama, image id | `internal/symbols` |
| `flutter` | `--split-debug-info` sembolleri | build id | **Yapılmadı** |

**Her SDK'nın yazdığı ve sunucunun okuduğu native frame biçimi:**

```
#00 libil2cpp.so 0123abcd… +0x1a2b Game::Update()
```

Bir image id (Mach-O UUID veya ELF build id, tire ve büyük/küçük harf yok sayılır) ve bir offset, cihaz biliyorsa adıyla birlikte. Binary'nin adında boşluk yok ve yol
rastgele bir kurulum dizini içerir, bu yüzden yalnızca dosya adıdır. Adres anlamsız olurdu, çünkü bir image'ın yüklendiği yer her açılışta değişir.

- **Android, API 31+:** `getTraceInputStream` bir protobuf tombstone'dur (AOSP `tombstone.proto`), bir raporun kullandığı birkaç alan için elle decode edilir, yani hiçbir
  SDK protobuf kütüphanesi taşımaz. Crash olan thread'in `rel_pc` değeri offset'tir ve bir ELF sanal adresidir.
  API 30'da yoktur ve raporun stack'i olmaz.
- **iOS:** MetricKit'in call stack ağacı, attribute edilen thread'e düzleştirilmiş. Kök en içteki frame'dir ve `subFrames` çağıranları tutar.
  `offsetIntoBinaryTextSegment` `__TEXT`'e görelidir, tablo da öyle. iOS 26'daki bir iPhone'dan alınan gerçek raporlarla kontrol edildi. Okunamayan bir ağaç
  geldiği gibi gönderilir.
- **ANR:** sistemin thread dump'ı, metin, `"main"` thread'inden itibaren. Bellek rakamları ve bir event log'uyla açılır, bunlar stack sınırını harcardı. Native satırları
  symbolicate edilmez. Java frame'leri diğerleri gibi retrace edilir.

**Native semboller için neden CLI adımı?** Bir dSYM yüzlerce megabayta çıkabilir ve çoğu, stack'in ihtiyaç duymadığı tip bilgisidir.
`openlog-symbols` onu `debug/macho`, `debug/elf` ve `debug/dwarf` ile okur ve yalnızca adres aralığı → fonksiyon, dosya ve satır bilgisini gzip'li metin tablosu olarak
yükler. İsimler orada demangle edilir, yani sunucuya demangler gerekmez. Swift'in mangling'i kapsanmaz. Bir build dizini bir image'ı strip edilmiş ve edilmemiş olarak
tek id altında iki kez tutar, bu yüzden en çok fonksiyonu olan kopya gönderilir.

**Yükleme yetkileri source map'lerden farklıdır,** bilerek: admin veya owner, API anahtarlarına izin var, çünkü çağıran bir build makinesidir ve birinin giriş yapması gereken
adım atlanan adımdır.

**Yapılmadı:** source map'lerin `OPENLOG_SOURCE_MAPS_RETENTION_DAYS` ayarının aksine sembol dosyalarının retention'ı yok: sahada hâlâ duran bir build, kaç yaşında olursa olsun
sembollerine ihtiyaç duyar. Silinene veya organizasyon silinene kadar kalırlar. Bir Gradle task'ı ve Xcode build phase önerildi ve yazılmadı; kılavuz komutları gösteriyor.

## 4. Gruplama: zor kısım {#_4-grouping-the-hard-part}

Grup id'si hata tipini, mesajı ve en üstteki uygulama içi frame'i **yazma anında** hash'ler (apm.md §3.1), semboller ise okuma anında uygulanır.
Mobil release build'leri frame kısmını bozar: R8 sınıfları build başına farklı yeniden adlandırır ve native frame bir adrestir. Bu yüzden crash'in
fingerprint frame'i boştur (`internal/apm/errors.go` içinde `frame = ""`) ve hata kutusu tipe ve mesaja göre gruplar. Bu, sürüm başına yeni grup üretmez,
ama gruplar geniştir.

**Buradaki öneri sembolleri yazma yolunda okumaktı. Reddedildi.** Processor'ın PostgreSQL ve object storage'a ihtiyacı olurdu, ingestion'ı bloke etme riskiyle, ve geç gelen
semboller hiçbir şeyi yeniden gruplamazdı.

**Bunun yerine yapılan:** `GET /rum/crashes` en fazla 500 farklı stack okur, symbolicate eder ve okurken gruplar:

```
key = type + first function symbols resolved     when symbols resolved a frame
    | error group + the device's name for a frame  otherwise
```

- Sembolleri yüklenmiş bir image uygulamanın kendisinindir, yani `libc.so` içindeki `abort`, `Game::Load`'un önüne geçmez.
- Sembol yokken cihazın adlandırdığı bir frame yalnızca bir grubu ayırır, ikisini asla birleştirmez, yoksa her abort tek crash olurdu.
- Geç gelen semboller eski crash'leri yeniden gruplar; yazma yolu tasarımı bunu yapamazdı.
- **Bedeli:** hata kutusu kaba kalır ve tek bir hata kutusu grubu bu sekmede birkaç crash olarak görünebilir. R8 ile yeniden adlandırılmış bir tipin de sürüm başına bir hata kutusu grubu olur.
  Hata kutusunun detay görünümündeki stack, sembolleri sürüm gerektirmeyen native frame'ler için symbolicate edilir.
- Stack başına 2.000 oturumu geçince oturum sayısı düşük okunur: oturumlar id olarak döner, çünkü bir oturum iki stack'te crash olabilir.

## 5. Crash-free oranı {#_5-crash-free-rate}

**`crash_free_rate` başka bir şey ölçüyordu.** Herhangi bir `error` span'i olan oturumları sayıyordu, yani yakalanmış tek bir exception oturumu "crash-free değil" yapıyordu ve native
crash'ler hiç sayılmıyordu. Yanıt artık bunları ayırıyor:

| Alan | Anlamı | Ne zaman |
|---|---|---|
| `error_free_rate` | `error` span'i olmayan oturumlar | her zaman, oturum yoksa `1` |
| `session_crash_free_rate` | crash'i olmayan yakalama yapan oturumlar | yakalama yapan oturumu olmayan sürüm için **null** |
| `crash_free_rate` | `error_free_rate` ile aynı | bir sürüm daha tutulur |

Null, rum.md §2.7'nin hatalar için seçtiği `1`'in yerini alır, çünkü crash **raporlayamayan** bir sürüm için %100, sürümün değerlendirildiği sayı hakkında yanlış bir beyandır. Web konsolu bunu "—" olarak çizer.

Yakalama sinyali her isteğin resource'unda `openlog.rum.crash.capture = "1"`'dir ve `captureCrashes` açıkken gönderilir.

**Eski isim değiştirilmedi, deprecate edildi,** çünkü mağazadaki mobil konsol bir sunucudan daha yavaş güncellenir ve aynı isim altında anlamı değişen bir sayı, gereksiz bir alandan daha
kötüdür. Mobil konsol bundan sonra onu "Error-free" olarak etiketler. Alanı kaldırmak ve `session_crash_free_rate`'i olması gereken ada taşımak sonraki bir sürümün işi.

**Yeni tablo,** `rum_release_sessions_1h` (`0103_release_sessions.sql`, sınıf `rum`, 30 gün, kişisel veri yok): tenant, uygulama, environment, sürüm ve saat başına: oturumlar, hatalı oturumlar,
crash olan oturumlar ve yakalama yapan oturumlar, `uniqCombined64` sketch'leri olarak. Bu, sürümlerin 30 gün, oturumların 7 gün geriye gittiği rum.md §2.7'deki boşluğu da kapatır. Migration'dan önceki
oturumlar içinde yok ve birkaç binin üstündeki sayılar tahmindir.

## 6. Arayüz {#_6-ui}

- Uygulama başına `GET /api/v1/rum/crashes`'ten beslenen bir **Crashes** sekmesi: oluşma sayısı, etkilenen oturumlar, son sürüm ve en yeni stack ile gruplar. Span'leri okur, yani 7 gün
  geriye gider. İş akışı APM hata kutusunda kalır.
- **Releases** sekmesi crash-free ve error-free oranlarını yan yana gösterir.
- OQL: `SELECT count(*) FROM Span WHERE openlog.rum.crash.kind != ''`, yeni kural tipi olmadan alert'lenebilir.
- Her crash neyin yükleneceğini söyler (`missing_symbols`). Cihazın adlandırdığı bir frame ve `.dylib` sistemindir ve dışarıda bırakılır. MetricKit raporunda iOS framework'leri uygulamayla
  aynı görünür, bu yüzden semboller bir şeyi çözdükten sonra diğer image'lar sistemin sayılır. Uygulamanın sembolsüz ikinci bir framework'ü bu durumda listelenmez, frame'leri yine de adres olarak okunur.
  Hiçbir şey çözülmediyse hepsi listelenir.

## 7. İş sırası {#_7-order-of-work}

1. ~~Payload, sanitize, yeni tablo ve API alanları (§1, §5).~~ Bitti.
2. ~~Dosya teslimatıyla Kotlin, Dart ve Swift'te yakalama (§2).~~ Bitti.
3. ~~CLI ile R8 ve native semboller (§3).~~ Bitti.
4. ~~Gruplama (§4).~~ Okuma anında yapıldı.
5. ~~Android'de NDK crash'leri ve ANR.~~ Bitti. Flutter sembolleri bitmedi.

## 8. Açık sorular {#_8-open-questions}

1. **Breadcrumb verisi kapsamda mı?** Crash öncesi ekranlar ve event'ler 7 gün boyunca oturum zaman çizelgesinde zaten var. Öneri: v1'de ayrı breadcrumb izi yok, oturuma link.
2. **`out_of_memory` iOS'ta dürüst mü?** MetricKit çıkışları sebebe göre `MXAppExitMetric` içinde, oturum başına event olarak değil sayı olarak raporlar. Öneri: aksi kanıtlanana kadar yalnızca Android.
3. **Crash oturumları 30 gün için yeterli mi?** Grup detayı ham span'leri 7 gün okur, `apm_error_groups` ise onlardan uzun yaşar. Bir haftadan eski crash'in hâlâ kullanışlı bir stack ile açılıp açılmadığı
   gerçek bir yüklemeyle kontrol edilmeli.
