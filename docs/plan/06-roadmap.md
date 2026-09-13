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
  - Manuel güncelleme (D-041): Ayarlar → Organizasyon → "Sürüm ve güncellemeler" sayfasında "Şimdi kontrol et" (api kontrolü + updater'a istek, 30 sn hız sınırı) ve "Şimdi güncelle" (`update_requests` tablosu, Compose updater 10 sn'de alır, `notify` modunda da uygular, bakım penceresi dışı için açık onay; sayfa api yeniden başlarken bağlantıyı kaybetmeden izler). Filo sayfasında "Şimdi dağıt" (dalga beklemelerini atlar; dalga bekleyen agent'lar 60 sn'de bir sync yapar). Kubernetes CronJob istekleri bir sonraki çalışmasında işler.

### Ara adım — Tüm sistemin Docker ile ayağa kalkması (M1 kapanışı) ✅ 2026-09-13

Sonuç: `make stack-demo` sıfırdan 9,5 dk'da geçti (`docs/operations/local-stack-demo.md`). Agent kurulumundan ilk veriye 4–5 sn; agent'lar 0.9.0 → 0.9.1 kademeli güncellendi (129 sn, 2/2 başarılı); backend compose updater ile 0.9.1'e 7 sn'de güncellendi, veri korundu; tam yeniden başlatmada veri kaybı yok. Kalan: bozuk imajda geri dönüş bu senaryoda denenmedi (`make updater-acceptance` kapsıyor), gerçek migration içeren sürüm geçişi, Helm chart'ın release'e girmesi, updater mesajlarının Türkçeleştirilmesi, compose değişikliği sonrası `e2e`/`autoupdate`/`mixedversion` testlerinin tekrar koşulması.

Otomatik güncelleme, filo, TLS ve M1 kalanları birleştirildikten sonra, sıfır durumdan tek komutla:
- `deploy/compose` ile PostgreSQL, Kafka, ClickHouse, openlog (ingest + processor + api + UI), bootstrap, `openlog-updater` ve gerçek bir `openlog-infra-agent` host'u ayağa kalkar; tüm healthcheck'ler yeşil.
- Tarayıcıda: e-posta ile giriş → host listesi → grafikler, servisler, envanter, loglar → Ayarlar → Filo ekranı (agent sürümü, politika) → sürüm bilgisi.
- Agent sıfırdan kurulum → ilk veri < 2 dk; imzalı yerel release ile agent otomatik güncellemesi ekranda görülür.
- Ekran görüntüleri ve adım adım çıktı raporlanır; bulunan sorunlar düzeltilir.

## M2 — Alarm, entegrasyonlar, APM başlangıcı (paralel, ~6 hafta)

- `openlog-alert`: eşik kuralları, incident, Slack/e-posta/webhook
  - ✅ (D-030, `docs/contracts/alerting.md`): metrik/log/veri yok/keşif/APM kural tipleri, lease ile kural paylaşımı (lease sahibi öldürülünce 22,8 sn'de devir, tek incident), outbox + idempotent teslim, Slack/e-posta (STARTTLS)/HMAC imzalı webhook/Teams, susturma, önizleme grafiği, "bu metrikten alarm oluştur" kısayolu; e2e'de tek açılış + tek kapanış bildirimi doğrulandı.
  - ✅ `apm_no_data` kuralı ("APM servisi raporlamayı bıraktı", §2.7); değerlendirme özetleri ClickHouse `alert_evaluations`'a (doğrudan shard insert, kural başına tek shard, 30 gün TTL) + `GET /alerts/rules/{id}/evaluations` + kural ekranında geçmiş grafiği (§3.6); tekrarlayan susturma (gün + saat + saat dilimi veya `FREQ=WEEKLY;BYDAY=…`, DST testli, eski sürümlerle uyumlu, §5.2). Kalan: aylık/tatil istisnalı takvimler
- Agent entegrasyonları: nginx, Redis, MySQL, PostgreSQL, Docker (keşif ile otomatik açılma)
  - ✅ (D-031): keşifle yaşam döngüsü, uç nokta türetme, `env:`/`file:` kimlik bilgileri, gerçek durum (`enabled`/`needs_configuration`/`error` + ipucu), OTel Collector receiver isimleriyle nginx (4), Redis (28), MySQL/MariaDB (28), PostgreSQL (21) metrik; gerçek container'larda doğru/eksik/yanlış parola senaryoları doğrulandı. Docker: OTel'de motor seviyesi metrik adı olmadığı için yalnızca erişilebilirlik.
  - ✅ SSH'sız yapılandırma: docker.sock erişimi olmayan konteyner servislerinde uç nokta türetme (`/proc/<pid>/net` + `docker-proxy`, son çare loopback varsayılan port), nginx stub_status yolu hatırlanır ve https doğrulamasız deneme yalnızca loopback'te; UI'dan host/organizasyon bazında entegrasyon ayarı (D-039, parola şifreli, agent sync ile revizyonlu teslim, agent yeniden başlamadan uygular, `integrations.remote_config: false` ile kapatılır); paketler `openlog-agent`'ı varsayılan olarak `docker` grubuna ekler (D-040, `--no-docker-access` / `OPENLOG_AGENT_DOCKER_ACCESS=0`). Kalan: gerçek sunucuda (deb + Docker'da nginx/redis) uçtan uca doğrulama.
  - Güvenlik düzeltmesi: komut satırı maskeleme `--requirepass`/`--masterauth` gibi sonu parola kelimesiyle biten bayrakları kaçırıyordu → düzeltildi, test + sözleşme §3.5 güncellendi.
  - Kalan: entegrasyon panelleri ve önerilen alarmlar (UI), MySQL io-wait metriklerinin gerçek veride doğrulanması, nginx Plus/VTS, Redis cluster, `pg_stat_statements`
- APM backend: span → transaction türetme, RED metrikleri, Apdex, servis listesi, trace waterfall (OTel SDK ile)
  - ✅ (D-032, `docs/contracts/apm.md`): transaction/hata/örnekleme ağırlığı tanımları, `apm_*` ClickHouse tabloları + MV'ler (2 shard'da doğrulandı), shard başına edge eşleştirme işi, servis listesi/genel bakış/transaction/hata gelen kutusu/DB sorguları/servis haritası/trace arama + trace logları, host ↔ servis bağlantısı, Apdex T ayarı. `test/apmdemo` (Node + Go + PHP) ile API sayıları ham span'larla birebir (percentiller <%9,1). Processor `hosts` ezme hatası düzeltildi.
  - ✅ `make stack-demo`'ya APM ve alarm fazları eklendi; geç span'lar için ayarlanabilir lookback (≤24 sa) + önceki günü gece toplu yeniden eşleştiren catch-up (`openlog_apm_link_late_calls_total`); `OPENLOG_APM_RETENTION_DAYS` (migrate `MODIFY TTL ON CLUSTER`, 2 shard/3 replikada doğrulandı); bağlantı yönetimi span'ları (`sql.connector.connect`, `sql.conn.reset_session` …) DB sorgularından çıkarıldı (apm.md §7). Kalan: catch-up sonrasından da geç gelen span'lar
- Go ve PHP agent geliştirmelerinin başlaması
  - Go agent (`agents/go`, D-033) ✅: `openlog.Start`, HTTP/SQL/slog/gRPC/chi/gin/echo, runtime metrikleri, infra agent ile aynı `host.id`, canlı ortamda span/log/metrik doğrulandı. Örnekleme oranı `tracestate` (`ot=th`) ile servisler arası taşınıyor ✅ (paylaşılan openlog'da %25 oranla 400 istek → 432 ağırlıklı, +0,9 sd); Go modül tag'leri release'e bağlandı ✅ (`make release-prepare`, `release.yml` kontrol + tag job'ları; GitHub'da henüz koşmadı). Kalan: `rv` yazma/W3C random bayrağı, container içindeki uygulamalarda host eşleşmesi
  - Backend hatası (Go agent testi buldu): processor uygulama kaynaklarından da `hosts` satırı yazıyordu → ✅ düzeltildi, yalnızca `openlog.entity.type=host` kaynakları yazıyor
  - PHP agent (D-035–D-038): karşılaştırmalı ölçüm yapıldı (`agents/php/docs/decision.md`); karar: kendi C extension'ımız, PHP 7.1+, fonksiyon seviyesi ayrıntı, forwarder infra agent içinde. Infra agent `php_forwarder` modülü ✅ (unix datagram, doğrulama, parça birleştirme, host bağlantısı, kesintide disk buffer; paylaşılan openlog'da `php-shop` servisi ve trace'leri görüldü). Extension faz 1 ✅ (`agents/php/ext`): 8.2+ Observer API, 8.0/8.1 Observer + `zend_execute_internal`, 7.x `zend_execute_ex`; Laravel/Symfony/Slim/WordPress/CodeIgniter/Yii 2/düz PHP, PDO/mysqli/pgsql/phpredis/Predis, curl/curl_multi/streams (+ `traceparent`), hatalar, örnekleme tabanlı fonksiyon izleyici; phpt matrisi 7.1–8.4 NTS + 8.3 ZTS + musl geçti; 5 demo uygulama paylaşılan openlog'da. **Ek yük hedefi tutmadı:** paylaşılan VM'de −%15 (izleyici kapalı) / −%10,7 (açık), hedef ≤%3 / ≤%7 — ayrılmış benchmark makinesinde yeniden ölçüm ve istek başı optimizasyon gerekli. Kalan: ASan/fuzz/soak, diğer extension'larla (Xdebug/NR/Datadog) birlikte çalışma testi, Guzzle async, Octane/Swoole, paketleme ve filo ile dağıtım (M3).
- Konteynerler (D-043) ✅: infra agent konteyner durumu/sağlığı/yeniden başlatma sayısı ve Docker Compose / Kubernetes adları (`openlog.container.status`, `container.restarts`, `docker.compose.*`, `k8s.*`), durmuş konteynerler 24 sa görünür; ClickHouse `containers` + `apm_service_containers` MV'leri (schema 0009), `logs` için `container.id`/compose servisi indeksleri; `/api/v1/containers*` + servis ↔ konteyner uç noktaları; UI'da Konteynerler sayfası (compose servisine göre gruplama, filtreler, mobilde kartlar), detay (CPU/bellek/ağ/disk grafikleri, APM servisleri, loglar, nitelikler), host sayfasında Konteynerler sekmesi, APM servis sayfasında konteynerler. Konteyner logları otomatik (`logs.containers`): json-file dosyası (rotasyon, parça birleştirme, ofset) veya Docker API akışı (çoklanmış akış, `since` ile devam), JSON gövdeden trace_id/span_id. `test/localagents`: demo uygulamalar `container.id` gönderir, `docker-host` agent Docker motorunu izler. Kalan: containerd/CRI-O meta verisi (CRI), Kubernetes pod görünümü, konteyner loglarında çok satırlı gruplama, servis haritasında konteyner sayısı
- Entegrasyon panelleri ve önerilen alarm şablonları (UI) — geliştiriliyor

## M3 — APM GA ve sorgu dili

- Go, PHP, Node.js agent'ları; hata gelen kutusu, service map, log ↔ trace geçişi
- NRQL benzeri sorgu dili, özel dashboard'lar
- Katmanlı saklama (S3)

## M4 — SaaS lansmanı

- Multi-tenant production, organizasyon/rol/SSO, kota ve faturalama
- Kubernetes entegrasyonu (DaemonSet, kube-state), Java, Python, .NET agent'ları
- Tail-based sampling
