# 06 — Yol Haritası

Süreler kalabalık ekip ve paralel çalışma varsayımıyla verilmiştir. Kilometre taşları kapsamla tanımlanır; tarih taahhüdü değildir.

## M0 — Sözleşmeler ve iskelet (2–3 hafta)

Amaç: Ekiplerin birbirini beklemeden çalışabileceği sözleşmeler ve uçtan uca çalışan en ince dilim.

- [x] Plan ve sözleşme belgeleri (`docs/plan`, `docs/contracts`)
- [x] ClickHouse şeması: metrics, logs, spans, hosts, inventory (Replicated + Distributed) ve `openlog-migrate`
- [x] `openlog-ingest`: OTLP HTTP + gRPC, license key → tenant, Kafka producer
- [x] `openlog-processor`: Kafka consumer group, OTLP → satır, envanter/keşif yönlendirmesi, batch insert
- [x] `openlog-api`: host listesi, host metrikleri, envanter, keşfedilen servisler, log arama, trace getirme
- [x] `openlog-allinone`, `openlog-loadgen`
- [x] `agents/infra`: sistem metrikleri, envanter (os, hardware, package, systemd_unit, listening_port, process, user, network_interface, mount, kernel_module), kural tabanlı keşif motoru + 29 başlangıç kuralı, OTLP/HTTP exporter, disk buffer
- [x] `deploy/compose` (`single` profil, uçtan uca doğrulandı)
- [x] `deploy/helm/openlog` (`cluster` profil) — kind (1+3 node) üzerinde Strimzi + Altinity operator'larıyla kuruldu, 7 hata düzeltildi
- [x] Agent'ın gerçek backend'e veri gönderdiği uçtan uca test (`make e2e`, kesinti ve 503 senaryoları dahil)
- [x] kind üzerinde ölçekleme ve chaos deneyi — ~118k kayıt/s kabul, tüm kesintilerde veri kaybı 0; processor ölçeklemesi ×1,4'te durdu (D-018). Ayrıntı: `docs/operations/benchmarks/2026-09-13-kind.md`

**Kabul kriteri:** Temiz bir Linux makinede agent kurulur; 2 dakika içinde API'den host'un metrikleri, envanteri ve keşfedilen servisleri sorgulanabilir. Aynı kurulum Helm ile 3 node cluster'da çalışır.

## M1 — Uçtan uca host izleme, v0.1 public release (6–8 hafta)

- UI: host listesi, host detayı (metrik grafikleri), envanter, keşfedilen servisler, log arama
- PostgreSQL: organizasyon, kullanıcı, roller, license/API key, oturum, davet, audit log (statik key'lerin yerine) ✅ — kalan: davet e-postası, kayıt (signup) arayüzü + kötüye kullanım koruması, üyelikten çıkarılan kullanıcının oturumlarının anında iptali, audit log ekranı, key hash'lerinde sunucu tarafı gizli anahtar (HMAC), CNPG Postgres'in gerçek cluster'da denenmesi
- Agent: process ve container metrikleri ✅, log toplama (dosya + journald) ✅, rpm envanteri ✅, container envanteri (Docker) ✅, D-Bus systemd ❌ (M2'ye kaydı), keşif kataloğunun tamamı
- Ölçekleme ve chaos testleri CI'da; 04-cluster.md'deki açık konular karara bağlanır
- Paketleme: deb/rpm/install.sh, imzalı release'ler
- Güvenlik: Kafka (TLS/mTLS + SASL PLAIN/SCRAM), ClickHouse (TLS), PostgreSQL (TLS) ✅ (`make tlstest`); kalan: API için salt okunur ClickHouse kullanıcısı, tenant başına sorgu bellek limitleri, sertifika yenilemede yeniden başlatmadan yükleme, Altinity operator modunda ClickHouse sunucu TLS'i
- UI iskeleti (`web/`): React + TypeScript + Vite (D-017) — M0'da kuruldu; M1'de gerçek backend ile doğrulama, route bazlı code-splitting (tek 656 kB bundle), log listesi ve waterfall için sanallaştırma + sayfalama, grafiklerin yeniden oluşturulmak yerine güncellenmesi
- API: bilinmeyen host için tüm host uç noktalarında `404` ✅; host log sekmesinde kaynak/dosya/servis/unit filtreleri ✅
- `make e2e` genişletildi: agent logları (rotasyon dahil), process metrikleri, container envanteri/metrikleri, gerçek backend'e karşı Playwright arayüz testi (`E2E_UI=1`) ✅; kalan: rotasyon anında okunmamış satırların eski dosyadan okunmasının zorlanması, journald uçtan uca
- Processor: doğrudan shard insert (D-018) ✅; tekrar gönderimde aynı sınırları üreten chunk'lar ✅; consumer lag metrikleri + alarm kuralları ✅; retry'da `metrics_1m`/`trace_index` çift sayım hatası düzeltildi ✅
- Ingest/processor `/readyz` topic kontrolü (D-019) ✅
- Çok node'lu ortamda 3+ ingest ve 4+ processor ile ölçüm tekrarı (direct vs distributed)
- Agent: Debian'da `redis-server → redis-check-rdb` gibi symlink'lerde kullanıcıya gösterilecek servis adı/instance (sözleşme şu an çözülmüş exe yolunu istiyor)

- Sürümler ve otomatik güncelleme (09, D-025–D-029) ✅: `libs/release` (imza/manifest), `openlog-release` + imzalı yerel release, deb/rpm + `install.sh`, GitHub Actions (CI + release; GitHub'da henüz çalışmadı), agent kendini güncelleme + otomatik geri dönüş, filo politikası/dalgalar/durdurma + Filo ekranı, backend sürüm kontrolü + UI bandı, expand/contract migration + N/N+1 testi, Compose updater (yedek → migrate → yeniden oluştur → geri dönüş), Helm updater CronJob (gerçek cluster'da denenmedi)

### Ara adım — Tüm sistemin Docker ile ayağa kalkması (M1 kapanışı) ✅ 2026-09-13

Sonuç: `make stack-demo` sıfırdan 9,5 dk'da geçti (`docs/operations/local-stack-demo.md`). Agent kurulumundan ilk veriye 4–5 sn; agent'lar 0.9.0 → 0.9.1 kademeli güncellendi (129 sn, 2/2 başarılı); backend compose updater ile 0.9.1'e 7 sn'de güncellendi, veri korundu; tam yeniden başlatmada veri kaybı yok. Kalan: bozuk imajda geri dönüş bu senaryoda denenmedi (`make updater-acceptance` kapsıyor), gerçek migration içeren sürüm geçişi, Helm chart'ın release'e girmesi, updater mesajlarının Türkçeleştirilmesi, compose değişikliği sonrası `e2e`/`autoupdate`/`mixedversion` testlerinin tekrar koşulması.

Otomatik güncelleme, filo, TLS ve M1 kalanları birleştirildikten sonra, sıfır durumdan tek komutla:
- `deploy/compose` ile PostgreSQL, Kafka, ClickHouse, openlog (ingest + processor + api + UI), bootstrap, `openlog-updater` ve gerçek bir `openlog-infra-agent` host'u ayağa kalkar; tüm healthcheck'ler yeşil.
- Tarayıcıda: e-posta ile giriş → host listesi → grafikler, servisler, envanter, loglar → Ayarlar → Filo ekranı (agent sürümü, politika) → sürüm bilgisi.
- Agent sıfırdan kurulum → ilk veri < 2 dk; imzalı yerel release ile agent otomatik güncellemesi ekranda görülür.
- Ekran görüntüleri ve adım adım çıktı raporlanır; bulunan sorunlar düzeltilir.

## M2 — Alarm, entegrasyonlar, APM başlangıcı (paralel, ~6 hafta)

- `openlog-alert`: eşik kuralları, incident, Slack/e-posta/webhook
- Agent entegrasyonları: nginx, Redis, MySQL, PostgreSQL, Docker (keşif ile otomatik açılma)
- APM backend: span → transaction türetme, RED metrikleri, Apdex, servis listesi, trace waterfall (OTel SDK ile)
- Go ve PHP agent geliştirmelerinin başlaması

## M3 — APM GA ve sorgu dili

- Go, PHP, Node.js agent'ları; hata gelen kutusu, service map, log ↔ trace geçişi
- NRQL benzeri sorgu dili, özel dashboard'lar
- Katmanlı saklama (S3)

## M4 — SaaS lansmanı

- Multi-tenant production, organizasyon/rol/SSO, kota ve faturalama
- Kubernetes entegrasyonu (DaemonSet, kube-state), Java, Python, .NET agent'ları
- Tail-based sampling
