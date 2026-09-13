# 01 — Vizyon ve Kapsam

## Vizyon

Tek bir agent kurulumuyla bir makinede **ne var ne yok** görünür olsun; uygulama, altyapı, log ve alarmlar tek bir üründe birleşsin.
Ürün açık kaynak olsun, küçük bir ekip tek komutla kurabilsin, büyük bir şirket veya SaaS olarak yüz binlerce host'a ölçeklenebilsin.

## Hedef kullanıcılar

- **SaaS müşterileri:** Hesap açar, agent kurar, veri gönderir.
- **Self-hosted kullanıcılar:** Kendi altyapısında `single` (tek makine) veya `cluster` (Kubernetes) profiliyle kurar.
- **Katkıcılar:** Keşif kuralı, entegrasyon ve agent katkısı yapan topluluk.

## Kapsam (ürün genelinde)

| Alan | İçerik |
|---|---|
| Altyapı izleme | Host metrikleri, process'ler, container'lar, envanter, **otomatik keşif** |
| APM | Trace, transaction, hata gruplama, DB sorgu analizi, service map |
| Log yönetimi | Toplama, arama, trace ile ilişkilendirme |
| Alarm | Kural motoru, incident, bildirim kanalları (Slack, e-posta, webhook) |
| Sorgu & dashboard | NRQL benzeri sorgu dili, özel dashboard'lar |
| Agent'lar | Linux infra agent (ilk), ardından Go, PHP, Node.js, Java, Python, .NET |

## Kapsam dışı (şimdilik)

- Windows ve macOS infra agent (Linux sonrası)
- Real User Monitoring (tarayıcı) ve mobil
- Synthetic monitoring
- Kendi wire protokolümüz (OTLP kullanılır)

## Başarı ölçütleri

- Agent kurulumundan ilk verinin ekranda görünmesine kadar **< 2 dakika**.
- Agent host üzerinde **< %1 CPU**, **< 50 MB RSS**.
- Ingest ve processor kopya sayısı 2 katına çıktığında işlenen veri miktarı **≥ 1,8 kat** artar.
- Tek bir bileşen (broker, replica, pod) kaybında **veri kaybı yok**.
