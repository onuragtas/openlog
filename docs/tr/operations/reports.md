# Zamanlanmış rapor grafik görselleri (`openlog-renderer`) {#scheduled-report-chart-images-openlog-renderer}

Zamanlanmış dashboard raporları (api.md "Scheduled reports", D-087) HTML tablolarla e-postalanır. İsteğe bağlı
`openlog-renderer` servisiyle her widget bunun yerine PNG görsel olarak gösterilir (D-097). Renderer ayrı bir imajdır
(`ghcr.io/onuragtas/openlog-renderer:<version>`, Alpine + Chromium, açılmış haliyle ≈ 780 MB), böylece ana imaj küçük kalır.
Yapılandırma: [config.md "Report chart images"](/contracts/config#report-chart-images-openlog-renderer-api-allinone-d-097).

## Rapor görsellerini nasıl alır {#how-a-report-gets-its-images}

```
api leader (report job)                      openlog-renderer                          any api pod
  │ due report: run widget queries (tables)
  │ sign render token (5 min, report+period)
  ├── POST /v1/render ─────────────────────▶ │ Bearer OPENLOG_RENDERER_TOKEN
  │   {path:/print/dashboard, token}         │ start Chromium (fresh profile)
  │                                          ├── GET <UI origin>/print/dashboard#token=… ─▶ web UI
  │                                          ├── GET /api/v1/render/dashboard (Bearer olrt_…) ─▶ verify token,
  │                                          ├── GET …/widgets/{id}/result ───────────────────▶ run stored query
  │                                          │ wait for data-render-state=ready
  │                                          │ capture each [data-render-id] as PNG, kill Chromium
  │ ◀──────────────── {images, errors} ───────┤
  │ embed PNGs (cid:), tables for the rest
  └── SMTP
```

- Job önce widget'ları kendisi sorgular (tablolar, düz metin bölümü), sonra renderer'dan ister; tarayıcı onları render endpoint'leri
  üzerinden yeniden sorgular. Dönem sabit olduğu için ikisi de aynı veriyi gösterir.
- Herhangi bir hata (renderer kapalı, timeout, `429`, geçersiz yanıt) → e-posta yalnızca tablolarla gönderilir; widget başına,
  başarısız bir sorgu veya sınırları aşan bir görsel (her biri 1 MiB, e-posta başına 10 MiB, 2000 px yükseklik) tablosunu korur.
- Loglar: api leader'da `dashboard report images` (sayı, atılan, bayt, süre) veya `dashboard report images unavailable, sending
  tables`; renderer'da `rendered` / `render failed` (yalnızca yol; token asla loglanmaz).

## Güvenlik modeli {#security-model}

**Render API'si (api → renderer).** Yalnızca dahili: `OPENLOG_RENDERER_ADDR` (8090) üzerinde `POST /v1/render`, paylaşılan secret
`OPENLOG_RENDERER_TOKEN` ile doğrulanır (sabit zamanlı karşılaştırma), isteğe bağlı TLS ve mTLS
(`OPENLOG_RENDERER_TLS_*`). Asla ingress, gateway veya yayınlanmış port üzerinden açma: Helm chart'ı, NetworkPolicy'si yalnızca
api pod'larına izin veren bir ClusterIP Service oluşturur; Compose profili port yayınlamaz. İstek yalnızca web uygulamasının bir
print route'unu (`/print/<segment>`; şema, host, query veya nokta segmenti olmadan) ve token biçimli bir dizgeyi adlandırabilir;
origin renderer'ın kendi `OPENLOG_RENDERER_UI_ORIGIN` değeriyle sabittir, yani çağıran taraf tarayıcıya rastgele URL açtıramaz.
İstek gövdeleri ≤ 8 KiB'dir, bilinmeyen alanlar reddedilir.

**Render token'ları (tarayıcı → api).** Print görünümü session, API key veya paylaşım bağlantısıyla değil, render token'ıyla kimlik
doğrular:

- `olrt_` + claim'ler + HMAC-SHA256. Anahtar, renderer'ın asla almadığı `OPENLOG_KEY_HASH_SECRET` (veya `OPENLOG_SECRETS_KEY`) değerinden
  türetilir, bu yüzden ele geçirilmiş bir renderer token üretemez; yalnızca kendisine verilen token'ları süreleri dolana kadar
  yeniden oynatabilir.
- Ömür ≤ 10 dakika (5 dakika verilir). Tek bir organizasyona ve tenant'ına, tek bir dashboard'a, hâlâ var olması ve etkin olması gereken
  tek bir zamanlanmış rapora ve rapor dönemine bağlıdır. Sorgular dashboard'un saklanan sorgularıdır; raporun değişkenleri,
  organizasyonun tenant kapsamı ve sorgu limitleriyle çalışır; istek OQL, değişken veya zaman aralığı taşımaz.
- Yalnızca `GET /api/v1/render/dashboard` ve `…/widgets/{id}/result` kabul eder; diğer her endpoint reddeder.
  Yanıtlar paylaşım bağlantıları gibi redakte edilir (sorgu metni, organizasyon/kullanıcı kimlikleri veya çalışma istatistikleri yok),
  `no-store`, kısıtlayıcı CSP.
- Token, sunuculara gönderilmeyen ve access loglarına yazılmayan URL fragment'ında (`#token=`) taşınır; sayfa onu adres çubuğundan
  kaldırır ve yalnızca `Authorization` başlığında gönderir. `/print/*` sayfaları
  `Referrer-Policy: no-referrer` ve `X-Robots-Tag: noindex` ile sunulur.
- Paylaşım bağlantısı değildir: asla saklanmaz, listelenmez, kullanıcılara gösterilmez veya render endpoint'leri dışında kullanılamaz ve
  organizasyonda paylaşım bağlantıları kapalı olsa bile çalışır (alıcıları rapor sahibi zaten seçmiştir).

**Tarayıcı yalıtımı.**

- Render başına taze geçici profille (sonra silinir) bir Chromium süreci, `OPENLOG_RENDERER_RENDER_TIMEOUT` ile öldürülür; aynı anda en
  fazla `OPENLOG_RENDERER_MAX_CONCURRENCY` tane çalışır, diğer istekler `OPENLOG_RENDERER_QUEUE_TIMEOUT` kadar bekler ve sonra `429` alır.
  V8 heap'i renderer süreci başına 512 MiB ile sınırlıdır, tarayıcı başına bir renderer süreci vardır; eklentiler, sync, arka plan ağı,
  bileşen güncellemeleri, crash raporlama ve ping'ler kapalıdır.
- Ağ: Chromium içindeki DNS yalnızca UI origin'inin host'unu çözer (`--host-resolver-rules`), proxy yoktur ve sayfanın her
  HTTP(S) isteği yakalanır (DevTools Fetch domain) ve tam olarak UI origin'ini hedeflemiyorsa başarısız edilir.
  Kurulum katmanı: Helm NetworkPolicy çıkışı yalnızca 8080'deki api pod'larına ve DNS'e izin verir; Compose, renderer'ı yalnızca `openlog`
  ile paylaşılan bir `internal: true` ağa bağlar (internet yok, Kafka/ClickHouse/PostgreSQL yok). Kendi kurulumlarında eşdeğer bir
  kısıtlama tut: WebSocket'ler ve IP-literal bağlantılar Fetch yakalamasının kapsamında değildir.
- Sandbox: Chromium'un sandbox'ı varsayılan olarak açıktır (`OPENLOG_RENDERER_CHROMIUM_NO_SANDBOX=false`). Yetkisiz user
  namespace'ler ister, Docker'ın ve Kubernetes'in varsayılan seccomp profilleri (`RuntimeDefault`) bunları reddeder, bu yüzden Compose profili ve
  Helm chart'ı `true` ayarlar ve bunun yerine container'ı sıkılaştırır: root olmayan (UID 10001 / chart `podSecurityContext`),
  boyutu sınırlı `/tmp` ve bellek tabanlı `/dev/shm` ile salt okunur kök dosya sistemi, tüm capability'ler kaldırılmış,
  `no-new-privileges`, service account token'ı yok, bellek ve PID limitleri. Yalnızca openlog UI origin'i yüklenir, yani
  renderer asla üçüncü taraf içerik işlemez. Sandbox'ı korumak için container'ı user namespace'lere izin veren bir seccomp profiliyle
  (örneğin Chromium'un `chrome.json` profili) çalıştır ve değişkeni `false` yap.
- Renderer veritabanı kimlik bilgisi, lisans veya API key tutmaz: yalnızca `OPENLOG_RENDERER_TOKEN` ve isteğe bağlı TLS dosyaları.

## Kurulum {#deploy}

**Compose.** `.env` içinde: `OPENLOG_RENDERER_URL=http://openlog-renderer:8090`, `OPENLOG_RENDERER_TOKEN=$(openssl rand -hex 32)`
ve `OPENLOG_KEY_HASH_SECRET` (veya `OPENLOG_SECRETS_KEY`). Sonra
`docker compose --profile renderer up -d --build openlog-renderer` çalıştır ve `openlog`'u yeniden oluştur (`docker compose up -d openlog`).

**Helm.** `renderer.enabled=true`, `renderer.token` (veya `renderer.existingSecret`) ve `auth.keyHashSecret` veya
`alert.secretsKey`. Chart, api pod'larında `OPENLOG_RENDERER_URL/_TOKEN/_TIMEOUT` ayarlar ve renderer
Deployment, ClusterIP Service ve NetworkPolicy oluşturur (NetworkPolicy uygulayan bir CNI gerekir; DNS çıkışı `k8s-app: kube-dns` label'lı
CoreDNS pod'larını varsayar, değilse `renderer.networkPolicy.extraEgress` ekle). mTLS: `renderer.tls.secretName`
(`tls.crt`, `tls.key`, `ca.crt` içeren Secret, renderer ve api pod'larına bağlanır; sertifika renderer Service adını kapsamalı ve
istemci kimlik doğrulamasına izin vermeli). Kaynaklar: iki eşzamanlı render için request 250m/512Mi, limit 1.5Gi;
`maxConcurrency` ile bellek limitini de yükselt.

**Boyutlandırma.** Raporları tek bir api leader, biri bittikten sonra diğerini göndererek yollar, bu yüzden eşzamanlılığı 2 olan tek bir
renderer replica'sı çoğu kuruluma yeter.

## Sorun giderme {#troubleshooting}

| Belirti (api leader logu) | Neden |
|---|---|
| `renderer: HTTP 401` | `OPENLOG_RENDERER_TOKEN` api ve renderer'da farklı |
| `renderer: HTTP 429` | tüm render yuvaları kuyruk timeout'undan uzun süre dolu |
| `renderer: HTTP 502: the page could not be rendered` | renderer logunda `render failed`: UI origin'ine ulaşılamıyor, sayfa hata bildirdi (örn. render token reddedildi: `OPENLOG_KEY_HASH_SECRET` api pod'ları arasında farklı veya saatler dakikalarca farklı), Chromium çöktü (bellek limiti, `/dev/shm`) |
| `renderer: HTTP 502: the render timed out` | yavaş widget sorguları; `OPENLOG_RENDERER_RENDER_TIMEOUT` değerini (ve onun üzerinde `OPENLOG_RENDERER_TIMEOUT` değerini) yükselt |
| görseller `dropped` > 0 | yakalamalar boyut sınırlarını aşıyor; widget'lar tablo olarak gönderilir |
| renderer başlamıyor: `No usable sandbox` | sandbox açık ama seccomp profili user namespace'leri reddediyor; yukarıdaki "Sandbox" bölümüne bak |
