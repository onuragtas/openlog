# 11 — Mobil Konsol (iOS + Android)

Nöbetçi mühendisin cebindeki openlog: alarmı telefondan görmek, servisin sağlığına bakmak ve logu
aramak. Flutter ile tek kod tabanı, iki platform.

Bu belge **tasarımı** anlatır, henüz alınmış kararları değil. Bağlı olduğu kararlar D-133 (API anahtarı
rolleri) ve D-136 (gerçek kullanıcı izleme). Kendisinin önerdiği iki şey — cihaz oturumu (§3.2) ve push
rölesi (§6) — kabul edildiğinde [02-decisions.md](02-decisions.md)'ye kendi D numaralarıyla girer; o satırlar
yazılmadan bu belge bir öneridir.

## 1. Kapsam

**Mobil konsol**, openlog'u telefondan *izlemek* için. Karıştırılmaması gereken şey, zaten yapılmış olan
**mobil SDK**'dır: `agents/swift`, `agents/kotlin` ve `agents/dart`, kullanıcının kendi mobil uygulamasından
openlog'a telemetri *gönderir* ve [../contracts/mobile-agent.md](../contracts/mobile-agent.md) sözleşmesini
uygular. Bu belge onların hiçbirine dokunmaz.

Kapsam dışı, bilerek:

- **Telemetri gönderme.** Konsol uygulaması bir ajan değil. Kendi çökmelerini bildirmek isterse
  `agents/dart`'ı kullanır, ki bu da onu sözleşmenin bir müşterisi yapar, parçası değil.
- **Yapılandırma yönetimi.** Alarm kuralı yazmak, dashboard düzenlemek, üye davet etmek masaüstü işleri.
  Telefonda ekran alanı bunları dürüstçe göstermeye yetmez ve yanlış dokunuşun bedeli yüksektir.
- **Dashboard düzenleme.** Görüntüleme §5'te var, düzenleme yok.

## 2. Sunucuya bağlanma

Mağazadan inen tek bir derleme, herkesin kendi sunucusuna bağlanır. Bu, ürünün en belirleyici mobil
farkıdır ve ilk ekranı o belirler.

- **Adres alanı, varsayılanı dolu: `https://apm.resoft.org`.** Barındırılan kurulumun adresi budur ve
  açılışta alanda yazıyor olur; kendi sunucusunda çalıştıran onu siler ve kendisininkini yazar. Varsayılan
  bir kolaylıktır, gizli bir zorunluluk değil — her kurulum eşit şekilde desteklenir.
- **Varsayılan bir derleme sabitidir, yani değiştirmek mağaza sürümü ister.** Adres uygulamanın içine
  gömülür; `apm.resoft.org` bir gün taşınırsa eski derlemeler onu bulamaz. Bunun bedelini ödememek için
  adres alanı her zaman düzenlenebilir kalır ve uygulama varsayılana dair başka hiçbir şey varsaymaz —
  sertifika sabitleme (certificate pinning) yok, bu adrese özel kod yolu yok. Barındırılan kurulum,
  uygulamanın gözünde yalnızca alanı önceden doldurulmuş bir self-hosted kurulumdur.
- **Adresin doğrulanması `GET /api/v1/auth/config` ile olur.** Bu uç nokta kimlik doğrulaması istemez
  (`security: []`) ve iki işi birden yapar: adresin gerçekten bir openlog sunucusu olduğunu kanıtlar ve
  giriş ekranının şeklini söyler.

`AuthConfig` ([openapi.yaml](../contracts/openapi.yaml) `AuthConfig`) uygulamanın bilmesi gereken her şeyi
taşır: `signup_enabled` (kayıt düğmesi gösterilsin mi), `password_min_length` (istemci tarafı doğrulama),
`email_enabled` ve `email_verification_required` (kayıttan sonra ne olacak), `captcha` (sağlayıcı ve
`site_key`), `sso_enabled`, `mode`.

Uygulama bu değerleri **varsaymaz, sorar.** Kayıt düğmesini kapalı bir sunucuda göstermek ya da 12 karakter
isteyen bir sunucuya 8 karakter yollamak, sunucunun yapılandırmasını istemciye kopyalamaktan doğan
hatalardır; tek kaynak sunucudur.

- **Birden çok sunucu.** Bir kullanıcının hem şirket sunucusu hem kendi kurulumu olabilir. Adres listesi
  saklanır ve aralarında geçiş yapılır; her adresin kendi oturumu vardır. Oturum paylaşmak yanlış olurdu —
  bunlar farklı kurulumlardır, aynı hesabın iki görünümü değil.

## 3. Kimlik: kayıt, giriş ve cihaz oturumu

**Bu, Faz 1'de çözülür.** Mobil kodun geri kalanı buna dayandığı için sonraya bırakmak, sonradan her
ekranı yeniden elden geçirmek demektir.

### 3.1 Mevcut oturum mobilde neden yetmiyor

Bugünkü oturum tarayıcı için tasarlanmış ve mobilde üç yerden kanıyor:

| Gerçek | Nerede | Mobildeki sonucu |
|---|---|---|
| `openlog_session` çerezi, HttpOnly, SameSite=Strict, Path=/api | `POST /api/v1/auth/login` | Native istemcide çerez kavramı yapay; saklama ve gönderme elle kurulur |
| `SessionTTL` **7 gün mutlak** | [internal/auth/service.go](../../internal/auth/service.go) `defaults()` | Haftada bir zorunlu yeniden giriş |
| `OPENLOG_SESSION_IDLE_TIMEOUT` varsayılan **24 saat** | [internal/config/config.go](../../internal/config/config.go) | Uygulamayı bir gün açmayan atılır |
| Mutasyonlar `X-CSRF-Token` ister | [internal/auth/principal.go](../../internal/auth/principal.go) | Çerez olmayan bir istemci için gereksiz tören |

24 saatlik boşta kalma süresi tek başına yeterli gerekçe: bir nöbet uygulaması, alarm geldiğinde açılır —
yani tam da uzun süre açılmadıktan sonra. O an giriş ekranı görmek, uygulamanın var olma sebebini ortadan
kaldırır.

API anahtarları (`ola_…`) bu boşluğu dolduramaz: organizasyon düzeyinde paylaşılan bir sırdır, sabit bir
rolle gelir ve kullanıcının kimliğini taşımaz. Bir kullanıcının telefonuna organizasyon anahtarı koymak,
telefon kaybolduğunda iptal edilecek şeyin yanlış şey olması demektir.

### 3.2 Cihaz oturumu — **yazıldı**

`POST /api/v1/auth/device`, e-posta ve parolayı `olm_…` bearer token'ına çeviriyor. Mobil kod yazılmadan
önce bitti, çünkü her ekran buna dayanıyor.

- **Bearer, çerez değil.** CSRF bir çerez problemidir, `Authorization` başlığında gelen bir istekte
  koruyacağı bir şey yok; o yüzden `/auth/me` cihaz oturumunda `csrf_token` döndürmüyor. Buna rağmen satırda
  bir CSRF token'ı **saklanıyor**: boş bir değer, herhangi bir yol oraya cihaz principal'ıyla ulaşırsa boş bir
  `X-CSRF-Token` başlığıyla eşit karşılaştırılırdı.
- **Kendi ömrü.** `OPENLOG_DEVICE_SESSION_TTL` (90 gün) ve `OPENLOG_DEVICE_SESSION_IDLE_TIMEOUT` (30 gün),
  tarayıcının 7 gün / 24 saatinden ayrı. Boşta kalma süresi TTL'i aşarsa yapılandırma reddediliyor — asla
  kapanamayacak bir pencere, sessizce hiçbir şey yapmayan bir ayardır.
- **Ayrı tablo değil, `sessions` üzerinde bir `kind` kolonu** (`0102_device_sessions`). Cihaz oturumu *bir*
  kullanıcı oturumudur: aynı kullanıcı, aynı rol, aynı iptal. Ayırmak ikinci bir arama yolu, ikinci bir iptal
  yolu ve yarısını unutan bir "her yerden çıkış" demekti. Bu sayede `GET /api/v1/sessions` telefonu
  tarayıcının yanında listeliyor ve `DELETE /api/v1/sessions/{id}` onu çıkış yaptırıyor — ikisi de
  değişmeden. Ayarlar → Güvenlik'te telefon kendi adıyla ve "Mobil uygulama" etiketiyle görünüyor.
- **Kısıtlama matristen geliyor, elle yazılmış bir listeden değil.** `auth.Allow`, `UserOnly` işaretli her
  eylemi oturum olmayan principal'lara reddediyordu; cihaz da oturum olmadığı için matrise tek satır
  dokunmadan üyeler, davetler, lisans/API/tarayıcı anahtarları, source map'ler, fleet, backend güncellemesi,
  sorgu limitleri, disk alanı ve hesap yönetiminin tamamı kapandı. Test bunu uç nokta listesiyle değil
  **matrisin tamamı üzerinden** doğruluyor, böylece sonradan eklenen bir `UserOnly` eylem eklendiği gün
  kapsama giriyor.
- **Okumak ayrı bir soru.** Üye listesini ya da anahtarların adlarını ve öneklerini okumak role bağlı, o
  yüzden telefon da okuyabiliyor — aynı rolde bir API anahtarı da okuyabildiği için telefon bununla fazladan
  yetki kazanmıyor. Yasak olan, o listelerdeki şeyleri **üretmek ya da iptal etmek**.
- **Çıkış yapmak serbest.** `POST /auth/logout` cihaza açık, çünkü çıkış yalnızca erişimi azaltır: reddetmek,
  endişelenen birinin ilk uzandığı düğmeyi erişilemez kılar ve oturumu açık bırakır. Bu tek kural matrisle
  ifade edilemiyordu (bir API anahtarı her kontrolü geçer ve sonra iptal edecek oturumu yoktur), o yüzden
  yerinde kontrol ediliyor.
- **Token çerez olarak kabul edilmiyor.** Cihaz token'ı `openlog_session` çerezinde gönderilse çerez yolu onu
  tarayıcı oturumu sanıp bütün bu kısıtlamanın etrafından dolaşırdı. Önek bunu kazara ulaşılmaz kılıyor,
  kontrol ise bilerek imkânsız.
- **Önek `olm_`, `old_` değil.** Akla gelen harf `d`'ydi ama `old_`, hem `olds_` (dashboard paylaşımı) hem
  `oldv_` (domain doğrulama) öneklerinin başlangıcı; bearer token'ı önekle yönlendirmek onları da yakalardı.

### 3.3 Kayıt

Kayıt uygulamanın içinde olur, `POST /api/v1/auth/signup`. Akışın şekli `AuthConfig`'ten gelir:

1. `signup_enabled: false` → kayıt düğmesi hiç gösterilmez. Kapalı bir sunucuda düğmeyi gösterip 403
   almak, kullanıcıya kendi sunucusunun kuralını hata olarak sunmaktır.
2. **Kayıt bir organizasyon kurar.** `SignupRequest` `organization_name`'i zorunlu tutuyor, yani mobilde
   "kaydol" düğmesi yeni kullanıcı değil, yeni *kurulum sahibi* üretir. Davet edilen kullanıcının yolu bu
   değildir — o, davet bağlantısıyla gelir (`POST /api/v1/invitations/lookup` ve `…/accept`, ikisi de
   kimlik doğrulamasız). İkisini aynı ekranda karıştırmamak gerekir: ekranın adı "openlog'a kaydol" değil,
   "yeni organizasyon oluştur" olmalı.

   **Davet kabulü mobilde ilk sürümde yok.** Davet e-postasındaki bağlantı web'i açar, kullanıcı daveti
   orada kabul eder ve sonra telefondan giriş yapar. Mobilde kabul etmek, e-posta bağlantısını uygulamaya
   yönlendiren bir universal link / app link kurulumu demektir; Faz 1'e değmez ve olmadığında kimse
   engellenmez.
3. `password_min_length` → istemcide doğrulanır, ama sunucunun cevabı son söz. Şemanın kendi tabanı 8
   karakter; sunucu daha fazlasını isteyebilir, daha azını isteyemez.
4. `email_verification_required: true` → kayıttan sonra "e-postanı doğrula" ekranı;
   `POST /api/v1/auth/verify-email/resend` ile yeniden gönderim. `email_enabled: false` olan bir sunucuda
   doğrulama e-postası hiç gitmeyeceği için bu ekran gösterilmez.

### 3.4 Captcha — yazılmıyor, karşılanıyor

Captcha varsayılan olarak kapalıdır: `OPENLOG_SIGNUP_CAPTCHA_PROVIDER` boş bırakıldığında hiç devreye
girmez ([../contracts/config.md](../contracts/config.md)). `apm.resoft.org`'da da ayarlı değil, dolayısıyla
uygulamanın en çok kullanılacak yolunda captcha yok ve **Faz 1'e captcha işi girmiyor.**

Yine de `AuthConfig.captcha` dolu gelebilir — kendi sunucusunu çalıştıran biri açabilir, ya da ileride
`apm.resoft.org`'da açılabilir. O durumda uygulama **kayıt widget'ını kendi içinde göstermez**: uygulama
içi kayıt kapanır ve kullanıcı o sunucunun web kayıt sayfasına yönlendirilir, sonra telefondan giriş yapar.

Alternatifi widget'ı bir WebView'de açmaktı. Sağlayıcıların ikisi de (`turnstile`, `hcaptcha`) web
bileşeni ve Turnstile'ın native mobil desteği yok, yani WebView tek yoldu. Bugün kimsenin kullanmadığı bir
yol için uygulamaya WebView bağımlılığı eklemek, taşınacak ama çalışmayacak kod demek. Gerçekten ihtiyaç
duyan bir kurulum çıkarsa o zaman yazılır.

Girişte captcha hiç yok: `captcha_token` yalnızca `SignupRequest`'te var, `LoginRequest`'te yok.

### 3.5 SSO — Faz 1'de değil

`POST /api/v1/auth/sso/start` bir `redirect_url` döndürüyor, ama akış `openlog_sso_<id>` çerezine bağlı
(HttpOnly, SameSite=Lax, 10 dakika) ve `redirect` alanı **göreli bir UI yolu** bekliyor. Mobil için
eksikler: custom scheme geri çağrısı (`openlog://auth/callback`), tek kullanımlık kod ve cihaz token'ına
takas.

Faz 1'e konmuyor çünkü sunucu tarafında kendi tasarımını istiyor ve e-posta/şifre girişi olmadan hiçbir
şey test edilemez. SSO zorunlu tutulan kurumsal kurulumlar bu yüzden ilk sürümde uygulamayı kullanamaz; bu,
bilerek kabul edilen bir eksik.

### 3.6 Organizasyon seçimi

`Me` yanıtı `organizations` listesini ve `organization` (geçerli olan) alanını taşır; seçim
`X-Openlog-Org-Id` başlığıyla yapılır. Birden çok organizasyonda olan kullanıcı için üst çubukta bir
seçici; tek organizasyonu olanda hiç gösterilmez. Seçim cihazda saklanır, çünkü nöbetteki kişi her açılışta
aynı organizasyona bakar.

## 4. Flutter

### 4.1 Tip üretimi ve CI bekçisi

Web, OpenAPI'den `web/src/api/schema.gen.ts` üretiyor ve sözleşme değişince `tsc` kırılıyor. Flutter'da bu
güvenlik ağı doğrudan kurulamaz, yerine konması gerekir:

**Yazıldı.** `mobile/tool/gen_api.dart`, `docs/contracts/openapi.yaml`'dan `lib/src/api/schema.g.dart`
üretiyor; `--check` kipi CI'da sözleşmeyle dosya ayrışmışsa düşüyor (`web`'de `npm run gen:api`'nin yaptığı
işin aynısı).

Hazır üreticileri önce denedim, ikisi de olmadı:

- **swagger_parser 1.45.0** bu spec'te çöküyor: `type 'List<Object?>' is not a subtype of type
  'Map<String, dynamic>' in type cast`, 0 dosya. Sebep spec'in OpenAPI **3.1** olması — `type: [string,
  "null"]` 108 yerde geçiyor, parser ise `type`'ı string sanıyor.
- **openapi-generator**'ın Dart hedefleri bu işe JVM istiyor ve 3.1'i deneysel sayıyor.

İkisi de ayrıca uygulamanın ~10 uç nokta ayrıştırdığı yerde 278'inin tamamını üretirdi. Ve bu spec'in dolu
olduğu yapılarda sessizce `dynamic`'e düşen bir üretici, bekçinin **sağlamadığı** bir güvenliği sağlıyormuş
gibi gösterir — ki bu, bekçinin hiç olmamasından kötüdür.

Bu yüzden üretici bize ait: spec'in kullandığı yapıları (`$ref`, null dallı `oneOf`, `type: [x, "null"]`,
gömülü nesneler, enum, dizi, map, `Timestamp`) biliyor ve tanımadığı bir şeyde **yüksek sesle duruyor**.
Nitekim ilk koşuda `Role`'de durdu; doğrusu bir dalı `dynamic`'e açmak değil, üreticiye o yapıyı öğretmekti.

İçindeki iki karar:

- **Bilinmeyen enum değeri `unknown` oluyor, istisna atmıyor.** Mağazadaki uygulama sunucuyla aynı anda
  güncellenmediği için, sunucuya eklenen bir rol eski telefonlarda çalışan bir ekranı düşürmemeli.
- **Eksik ya da yanlış tipte zorunlu alan, yolunu söyleyerek hata veriyor**
  (`DeviceSession.me.organizations[0].name`). Üç ekran sonraki "unexpected null", bir sözleşme kırığının
  alakasız bir hata raporuna dönüşme biçimidir.

Hata gövdeleri bilinçli olarak **üretilmiyor**: 502'yi bir proxy HTML olarak döndürebilir, captive portal
her şeyi yanıtlayabilir. Sözleşmenin söz veremediği tek şekil orası, o yüzden `client.dart` onu toleranslı
ayrıştırıyor ve "ulaşılamıyor" ile "sunucu reddetti"yi ayrı tutuyor — kişiye söylenecek şey farklı.

### 4.2 i18n

Web'de `en.ts` tip kaynağı, `tr.ts` ise `Messages` olarak tipli; eksik anahtar `tsc`'yi kırıyor. Mobilde
sözlükleri çatallamamak için bu dosyaları ARB'ye çeviren bir betik yazılır. Çeviriyi iki yerde ayrı
sürdürmek, ikisinin de eksik kalmasıyla sonuçlanır.

**Köprü yazılmadı, ve gerekçesi bir ölçüm.** Web sözlükleri **4985 satır**; uygulama Faz 1'de **56** metin
gösteriyor. Onlardan üretmek, bir avuç anahtardaki çatallanmayı önlemek için hiçbir ekranın okumadığı binlerce
anahtarı taşımak olurdu.

Bunun yerine ARB dosyaları uygulamanın kendisinin, ama **örtüşen her metinde web'in Türkçesi birebir
kullanılıyor** ("openlog'a giriş yap", "E-posta veya parola hatalı.") — iki ürün aynı Türkçeyi konuşsun diye.
Gerçek risk olan "bir dilde çevrilmiş, diğerinde unutulmuş anahtar" ise doğrudan yakalanıyor:
`tool/check_l10n.dart`, anahtar kümeleri ayrıştığında **ve** aynı anahtar iki dosyada farklı yer tutucu
kullandığında (`{min}`'e karşı `{sayi}`) düşüyor. gen-l10n bunu yalnızca uyarıp İngilizceye düşüyor, ki o da
kullanıcının CI'dan önce gördüğü "Türkçe ekranda İngilizce kelime" demek.

Uygulama web'de zaten var olan ekranlara (alarmlar, servisler, loglar) büyüdüğünde yeniden değerlendirilmeli:
örtüşme orada bir giriş formundakinden çok daha büyük olacak.

### 4.3 Grafikler

Grafikler web'in kullandığı aynı JSON'dan (`/api/v1/metrics/query`) Flutter tarafında çizilir.

`internal/renderer` sunucuda PNG üretiyor ama mobil için kestirme değil: zamanlanmış rapor e-postaları için
yazılmış (D-097), headless Chrome çalıştırıyor ve render token'ı tek bir raporun tek bir dashboard'una
bağlı. Mobil için kullanmak, raporlara bağlı bir mekanizmayı amacının dışına çekmek olurdu.

## 5. Ekranlar ve fazlar

| Faz | İçerik | Çıktı |
|---|---|---|
| **0** ✅ | `mobile/` iskeleti, Dart tip üretimi + CI bekçisi, API istemcisi, 22 test | Sözleşmeye bağlı temel |
| **1** ✅ | Sunucu adresi (§2), kayıt ve giriş (§3.3), **cihaz oturumu (§3.2)**, organizasyon seçimi | Uygulama bağlanıyor, kalıcı oturum açıyor |
| **2** ✅ | **Alarmlar:** açık/onaylanmış liste, önem rozetleri, onaylama, çekmecede hesap | Uygulamanın var olma sebebi |
| **3** ✅ | **Servis sağlığı:** APM servis listesi (RED), en kötü üstte, sunucu tarafı arama | Alarmdan sonra bakılan ilk yer |
| **4** ✅ | Loglar: gövde araması, önem filtresi (varsayılan WARN), son kayıtlar | Teşhis |
| **5** ✅ | Pano listesi ve görüntüleme (salt-okuma), **sonuç türüne göre** render | Tamamlayıcı |
| **6** ⛔ | **Push bildirim (§6)** | Kod değil, karar ve kimlik bilgisi bekliyor |

Faz 2 erken ve tam yapıldı: uygulamanın var olma sebebi o.

**Faz 5 hakkında bir not.** "En düşük getirili" değerlendirmesi doğruydu ve kapsam o yüzden daraltıldı.
Sözleşme sekiz görselleştirme sunuyor (`line, area, bar, table, billboard, pie, heatmap, markdown`) ama
sonuç yalnızca dört *türde* geliyor (`single, facets, timeseries, histogram`). Telefonda ayırt edici olan
türlerdir: bir `facets` sonucu, pano ona ister pasta ister çubuk desin, sıralı bir listedir — ve 390 puanlık
ekranda sıralı liste hem okunur hem kesindir, beş dilimli pasta ise ikisi de değildir. Bu yüzden widget'lar
**türe göre** çiziliyor: tek sayı, sparkline, oranlı sıralı liste. Histogram ve tanınmayan tür "en iyi web'de
okunur" diyor — boş görünen bir kart, bozuk görünen bir karttır.

Sparkline elle çiziliyor (`CustomPainter`): eksen, gösterge, ipucu ve etkileşim içermeyen tek bir çoklu çizgi
için bir grafik kütüphanesi bağımlılık, lisans ve sürüm yükü demekti.

## 6. Push bildirim — kod eksik değil, karar eksik

**Durum: başlanmadı, ve başlanamaz.** Faz 0–5 yazıldı; bu fazı bloke eden şey yazılacak kod değil, aşağıdaki
üç şeyin sahibinin vermesi gereken kararlar ve kimlik bilgileri. Bunlar gelmeden yazılacak her şey, var
olmayan bir protokole karşı spekülatif kod olurdu.

**Gereken, sırayla:**

1. **Bir karar: röle mi, yoksa push yok mu?** Aşağıdaki üç yoldan hangisi. Röle seçilirse proje bir sunucu
   işletmeyi üstlenmiş olur.
2. **Apple Developer hesabı ve bir APNs anahtarı**, uygulama kimliğine bağlı. Mağaza derlemesi projenin
   kimliğiyle imzalandığı için bu anahtar da projenindir — bir operatör kendi Firebase'iyle bu uygulamaya
   push gönderemez.
3. **Bir Firebase/FCM projesi** (Android tarafı için) ve rölenin çalışacağı yer.
4. **Gizlilik beyanının güncellenmesi**: bugün uygulama yalnızca kullanıcının kendi sunucusuna bağlanıyor ve
   projeye hiçbir veri akmıyor. Röle bunu değiştirir, az da olsa.

**Yazılmayanlar, bilerek:** sunucuya yeni bir `AlertChannelType` eklemedim ve uygulamaya bildirim
bağımlılığı koymadım. Protokolü belli olmayan bir röleye karşı kanal yazmak, taşınacak ama çalışmayacak kod
demekti.

**"ntfy köprüsü" neden tek başına yetmiyor:** mevcut `webhook` kanalı bugün herhangi bir URL'ye gönderebilir,
yani sunucu tarafı hazır. Ama uygulamanın içinden bir konuya abone olmak arka planda çalışmaz — iOS'ta bir
Dart SSE bağlantısı uygulama kapalıyken yaşamaz. Bu yolun gerçek hali, kullanıcının **ntfy'nin kendi
uygulamasını** kurup abone olmasıdır; o da bizim yazacağımız bir şey değil, bir kurulum talimatıdır.


Bugün altyapı yok: `AlertChannelType` enum'u `slack, email, webhook, teams, pagerduty, opsgenie`.

Self-hosted olmanın sert sonucu: mağazadaki uygulama **projenin** uygulama kimliğiyle imzalı, dolayısıyla
APNs/FCM kimlik bilgileri de projenindir. Bir operatörün kendi sunucusundan kendi Firebase'iyle bu
uygulamaya push göndermesi mümkün değildir — kendi derlemesini yayınlaması gerekir.

Üç yol:

1. **Mevcut `webhook` kanalı + ntfy benzeri bir servis.** Sunucuda sıfır değişiklik, bugün çalışır.
   Operatör bir webhook kanalı tanımlar, uygulama o konuya abone olur. Faz 6 beklerken erken değer almanın
   yolu; kalıcı çözüm değil, ama hiç bildirim almamaktan iyidir.
2. **Proje tarafından işletilen röle** (hedef çözüm). openlog sunucusu → röle → APNs/FCM. Tek mağaza
   derlemesiyle çalışır. Gizliliği korumanın yolu, röleden **yalnızca opak bir uyandırma** geçirmektir —
   "bir alarmın var" ve bir kimlik; içeriği uygulama kullanıcının kendi sunucusundan çeker. Röle alarm
   metnini, servis adını, eşik değerini görmez. Bedeli: proje olarak sunucu işletmek.
3. **Yoklama + yerel bildirim.** iOS arka plan uyandırması dakikalarla saatler arasında değişir. Nöbet için
   yeterli değil; bu yüzden yol olarak sayılmıyor, sadece neden sayılmadığı yazılıyor.

Push'un sona bırakılması bilinçli: önce uygulamanın gösterecek bir şeyi olması gerekir. Faz 2–5 bittiğinde
kullanıcı alarmı Slack'ten duyup uygulamayı açar, ki bu zaten bugünkü akıştır.

## 6b. Web ile aynı deneyim

Faz 0–5 çalışan bir uygulama verdi ama *openlog gibi* görünmüyordu. Bu bölüm onu kapatıyor.

**Renkler ölçülerek alındı, benzetilerek değil.** `web/src/index.css`'teki OKLCH belirteçleri standart OKLab
matrisleriyle sRGB'ye çevrilip `mobile/lib/src/ui/theme.dart`'a birebir taşındı. İlk ortaya çıkan şey
uygulamanın baştan beri **yanlış renkte** olduğuydu: openlog teal (`#00736A`), tohumlanmış bir
`ColorScheme` ise mavi üretiyordu. Artık tohum yok.

**Bileşenler web'in biçiminde:** kart `rounded-xl border bg-card shadow-xs` (12 piksel, kenarlıklı,
gölgesiz — Material'ın varsayılanının tersi), rozet ise anlamsal rengin %15–20 tonu artı ayrı bir metin
belirteci, ki index.css bunları WCAG AA için zaten ayrı tutuyor.

**Gezinme de web'in:** web kenar çubuğunu `lg` altında soldan açılan bir Sheet'e indiriyor, yani çekmece
mobil icadı değil. Aynı düz liste, aynı sıra, aynı etiketler. Uygulamanın sahip olduğu on üç bölüm web'in
yerlerinde duruyor; sahip olmadıkları listelenmiyor — hiçbir yere gitmeyen on satır da web'in deneyimi
değil.

İki yerde bilerek ayrıldık: uygulamanın **nerede açıldığı** (liste sırası web'in, ama nöbet uygulaması yanan
şeyle açılır) ve **bölümlerin ne zaman yüklendiği** (web her sayfayı gezinince yükler; burada `IndexedStack`
hepsini kuruyor ama her bölüm ilk bakıldığında yükleniyor — on üç ekran kurmak ucuz, henüz kimsenin sormadığı
on üç soruyu sunucuya sormak değil).

## 7. Depo yerleşimi ve sürüm hattı

Monorepo'da `mobile/` dizini, `web/` ve `agents/` ile tutarlıdır ve tip üretimi bekçisini (§4.1) mümkün
kılar — ayrı depoda sözleşme ile istemci aynı PR'da değişemez.

**Dikkat edilmesi gereken çakışma:** sürüm hattı master'a her push ile dönüyor (`ci.yml` → `v*` etiketi →
`release.yml`). Mobil uygulama bu hatta girerse her sunucu sürümünde sürümü artar, ki mağaza sürümleri için
yanlıştır. Mobil uygulamanın **kendi sürüm şeması ve yol filtreli kendi iş akışı** olmalı: sunucu günde
birkaç kez sürüm kesebilir, mağaza kesemez.

## 8. Riskler ve açık kararlar

- **Derin bağlantılar `apm.resoft.org`'dan sunulmak zorunda.** §3.3 (davet kabulü) ve §3.5 (SSO geri
  çağrısı) ilk sürümde yok, ama yapıldıklarında universal link / app link kurulumu gerekir: Apple için
  `/.well-known/apple-app-site-association`, Android için `/.well-known/assetlinks.json`, ikisi de o
  alan adından. Bunlar yalnızca barındırılan kurulumda çalışır — kendi sunucusunu çalıştıran bir kullanıcı
  için davet bağlantısı uygulamayı açmaz. O yüzden ikisinin de tasarımı, derin bağlantı *olmadan* çalışan
  bir yedek yola sahip olmalı (kodu elle yapıştırmak gibi); aksi halde self-hosted kurulumlar sessizce
  ikinci sınıf olur.
- ~~**Cihaz oturumu sunucu işi.**~~ Yapıldı (§3.2): `olm_` bearer token'ı, `sessions.kind`, 90/30 gün,
  matris üzerinden kısıtlama, Ayarlar → Güvenlik'te "Mobil uygulama" etiketi. Mobil ekranlar bundan sonra
  gelebilir. Not: yetki matrisine **hiç** satır eklenmedi, ki beklediğimden iyisi — `UserOnly` zaten
  "kimlik, kimlik bilgileri, kurulum makineleri" kümesini tam olarak ifade ediyordu.
- **Cihazda çalışıyor.** iOS ve Android derlemeleri elle yapıldı ve uygulama bir iPhone'a kuruldu
  (`org.resoft.openlogMobile`). CI hâlâ iki platformu da derlemiyor, yani native taraf her sürümde değil
  yalnızca elle doğrulanıyor.
- **Mağaza yayını.** App Store ve Play, uygulama kimliği, imzalama sertifikaları ve gizlilik beyanı demek.
  Self-hosted bir ürün için gizlilik beyanı özellikle dikkat ister: uygulama *kullanıcının* sunucusuna
  bağlanır, veri projeye akmaz — bunu beyanda doğru anlatmak gerekir. Push rölesi (§6) bu tabloyu
  değiştirir ve beyanın güncellenmesini gerektirir.
- **SSO zorunlu kurulumlar ilk sürümde dışarıda** (§3.5).
- **`apm.resoft.org`'da captcha açılırsa uygulama içi kayıt kapanır** (§3.4). Bugün kapalı olduğu için
  Faz 1'de captcha işi yok; açılması bir hata üretmez ama kayıt akışı sessizce web'e taşınır. Spam
  nedeniyle açmak gerekirse bunun mobil kayıt oranına etkisi önceden bilinmeli.

## 9. Web ile birebir eşitlik: ölçülmüş fark ve çalışma listesi

Sahibin kararı: mobil ve web **aynı** özellikleri sunmalı. Daha önce "telefona ait değil" diye webde
bırakılan yönetim işlemleri de buna dahil; yıkıcı olanlarda onay kutusu kalır, çünkü bu bir özellik farkı
değil, dokunmatik bir cihazda doğru davranıştır.

Fark tahminle değil ölçülerek çıkarıldı: webin `web/src` içinden çağırdığı `/api/v1/...` yolları ile
mobilin `mobile/lib/src/api/client.dart` içinden çağırdıkları karşılaştırıldı, sonra sözleşmede gerçekten
var olanlara indirgendi (regex artıkları elendi). **119 uç** mobilde yok. Bunların ~20'si imzalı konsolun
parçası değil ve listede değil: `operator/*` (ayrı süper-yönetici konsolu, ayrı giriş), `admin/*`,
`render/*` (e-posta için sunucu tarafı PNG), SSO yönlendirme uçları (`sso/oidc/callback`,
`sso/saml/complete`) ve tarayıcıya özgü giriş akışları.

Kalanlar, konu konu. Her satır bitince işaretlenir.

### 9.1 Alarmlar
- [x] Susturmalar (`alerts/mutes`): liste, süreli oluşturma, bitirme. Tekrarlayan takvim
      düzenleyicisi (`mutes/preview`, `AlertMuteScheduleInput`) hâlâ webde; okunuyor, yazılmıyor.
- [x] Yönlendirme kuralları (`alerts/routing-rules`, `.../reorder`): sıralı liste, sürükleyerek
      yeniden sıralama, açma/kapama. Koşul düzenleyicisi (`AlertRouteMatch` yazma) hâlâ webde.
- [x] Tatil takvimleri (`alerts/holiday-calendars`): susturmalar ekranından açılan liste,
      oluşturma, düzenleme, silme; tarihler metin olarak (`YYYY-AA-GG` ve her yıl için `AA-GG`).
- [ ] Kural oluşturma/düzenleme (`alerts/rule-types`, `alerts/rules/preview`, `alerts/templates`)
- [x] Teslimat günlüğü (`alerts/deliveries`): kanallar ekranından, tümü ya da tek kanal için;
      durum süzgeci sunucuda, her denemenin kodu ve süresiyle. (Webde bu uç için sorgu tanımlı
      ama hiçbir ekran çağırmıyor; web yalnızca olayın kendi teslimatlarını gösteriyor.)

### 9.2 APM
- [ ] Hata gelen kutusu kendi bölümü olarak (`apm/errors`, `apm/errors/groups`)
- [ ] Servis haritası (`apm/map`, `apm/map/path`)
- [ ] Ajanlar (`apm/agents`)
- [ ] Örnekleme ayarları (`apm/sampling`, `.../preview`)
- [ ] Servis izleri (`apm/traces`)

### 9.3 Organizasyon ve hesap
- [ ] Üyeler (`members`), davetler (`invitations`, `.../accept`, `.../lookup`)
- [ ] API anahtarları (`api-keys`), lisans anahtarları (`license-keys`), tarayıcı anahtarları (`browser-keys`)
- [ ] Denetim günlüğü (`audit-log`)
- [ ] Organizasyon ayarları (`orgs/current`, `.../saas`, `.../support-access`, `.../deletion`)
- [ ] SSO yönetimi (`sso/connection(s)`, `sso/domains`, `sso/enforcement`, `sso/role-mappings`, `scim/tokens`)
- [ ] Hesap: parola değiştirme (`auth/password`), e-posta doğrulama (`auth/verify-email`),
      veri dışa aktarma ve hesap silme (`account/*`, `data-exports`)

### 9.4 Keşif ekranlarının derinliği
- [ ] Filtre kurucusunun sözlüğü (`fields/keys`, `fields/values`) — log, iz ve metrik filtrelerinin temeli
- [ ] Log gelişmiş sorgu ve desenler (`logs/query`, `logs/aggregate`, `logs/patterns`)
- [ ] İz toplulaştırma (`traces/aggregate`)
- [ ] Metrik adları, korelasyon, exemplar (`metrics/names`, `metrics/correlate`, `metrics/exemplars`)
- [ ] Kayıtlı görünümler (`saved-views`)
- [ ] OQL şema ve doğrulama (`query/schema`, `query/validate`)
- [ ] Alev grafiği (`profiles/flame`)

### 9.5 Altyapı
- [ ] Kubernetes: kümeler, düğümler, iş yükleri, olaylar (`kubernetes/clusters|nodes|workloads|events`)
- [ ] Veritabanı: etkinlik, sorgular, oturumlar, arama (`db/activity|queries|sessions|lookup`)
- [ ] Konteyner grupları (`containers/groups`)
- [ ] Maliyet: özet, servisler, konteynerler, eğilim, fiyatlar (`costs/*`)
- [ ] Bulut bağlantıları (`cloud/providers`, `cloud/connections`, `.../test`)
- [ ] Filo politikası ve dağıtımlar (`fleet/policy`, `fleet/rollouts`, `fleet/rollback`)
- [ ] Entegrasyon ayarlarını düzenleme (`integrations/settings` yazma)
- [ ] Depolama (`storage/disk`, `.../settings`)

### 9.6 RUM
- [ ] Sayfalar, vitals ayrıntısı, oturumlar, sürümler, huni (`rum/pages|vitals|sessions|releases|funnel`)
- [ ] Kaynak haritaları (`source-maps`)

### 9.7 Kullanım, sürüm, panolar
- [ ] Kullanım ve kotalar (`usage`, `.../daily|top|status|query-limits|export`), planlar (`plans`)
- [ ] Sürüm ve güncelleme (`version`, `.../check`, `.../update`), durum (`status`)
- [ ] Pano içe aktarma ve ayarları (`dashboards/import`, `dashboards/settings`)
- [ ] Güvenlik açığı kataloğu durumu (`vulnerabilities/catalog/status`)

### Yöntem notu

Bu liste uç bazında; bir satır bir ekran demek değil. Ölçüm tekrarlanabilir: webin ve mobilin çağırdığı
yolları çıkarıp sözleşmedekilerle kesiştirmek yeter. Bitirme ölçütü de bu — tahmin değil, aynı ölçümün
boş dönmesi.
