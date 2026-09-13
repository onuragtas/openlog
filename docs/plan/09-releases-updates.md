# 09 — Sürümler ve Otomatik Güncelleme

Hedef: openlog'un her parçası **tek bir ürün sürümü** taşır ve kurulduğu her yerde **kendini güvenle günceller**.
Güncelleme; imzalı, kademeli, geri alınabilir ve kesintisiz olmalıdır. Binlerce makinedeki agent'ları elle güncellemek kabul edilemez.

## 1. Sürümleme (D-025)

- **Tek sürüm:** backend servisleri, gömülü UI, Helm chart, container imajı ve tüm agent'lar aynı SemVer sürümüyle (`vX.Y.Z`) birlikte yayınlanır. Kullanıcı tek bir "openlog sürümü" görür.
- **Kanallar:** `stable`, `beta`. Pre-release etiketleri (`v0.4.0-beta.1`) yalnızca `beta` kanalına girer.
- **Uyumluluk kuralı:**
  - Backend `N`, agent'ların son **3 minor** sürümünü kabul eder (`N`, `N-1`, `N-2`). Daha eski agent'lar veri göndermeye devam eder ama UI'da "desteklenmiyor" olarak işaretlenir.
  - Agent, kendi sürümünden yeni bir backend ile her zaman çalışır (OTLP + ileriye uyumlu sözleşmeler).
  - Backend `N` ve `N+1` rolling update sırasında **aynı anda** çalışabilmelidir (§5).
- Her binary sürümünü `-version`, `/api/v1/version`, `/readyz` ve her HTTP yanıtındaki `X-Openlog-Version` header'ı ile bildirir. UI, backend sürümü değişince kullanıcıya yenileme önerir.

## 2. Release ve imza (D-026)

Her release GitHub Releases'a (`github.com/onuragtas/openlog`) şunları koyar:

| Artifact | İçerik |
|---|---|
| `manifest.json` + `manifest.json.sig` | Sürüm, kanal, tüm artifact'ların URL/sha256/boyut bilgisi, imaj digest'leri, uyumluluk alanları |
| Agent arşivleri | `openlog-infra-agent_<v>_linux_<arch>.tar.gz` |
| Paketler | `.deb`, `.rpm` |
| Container imajı | `ghcr.io/onuragtas/openlog:<v>` (digest manifest'te) |
| Helm chart | `openlog-<v>.tgz` |
| `install.sh` | Dağıtımı algılar, paketi kurar, license key ile servisi başlatır |

- **İmza:** manifest Ed25519 ile imzalanır. Açık anahtarlar agent'a ve backend'e gömülüdür. Anahtar rotasyonu için aynı anda **2 geçerli anahtar** desteklenir.
- Artifact'lar manifest'teki sha256 ile doğrulanır, yani manifest imzası tüm zinciri korur.
- **Sunucu güvenilmez kabul edilir:** backend ele geçirilse bile agent'a yalnızca imzalı sürümlerden birini seçtirebilir. İmzasız ya da uyumsuz bir binary kurulamaz. Sürüm düşürme yalnızca açık "geri al" komutuyla ve manifest'in izin verdiği alt sınıra kadar yapılabilir.
- **Kapalı ağlar (air-gapped):** `openlog-api` release'leri kendi üzerinden sunabilir (mirror). Agent indirmeyi yine aynı imza zinciriyle doğrular.

## 3. Agent otomatik güncelleme (D-027)

### Akış

1. Agent periyodik olarak ingest'e `POST /v1/openlog/agent/sync` gönderir (aynı license key, aynı endpoint). Gönderdiği bilgiler: sürüm, kurulum yöntemi, güncelleme yeteneği, son güncelleme sonucu.
2. Backend organizasyonun **güncelleme politikasına** bakar ve hedef sürümü, imzalı manifest'i ve indirme adresini döner. Politika hedef belirlemiyorsa (ör. kademeli dağıtımda host henüz sırada değilse) yanıt "güncelleme yok" olur.
3. Agent manifest imzasını ve sürüm kurallarını doğrular, ardından arşivi indirir ve sha256'yı kontrol eder.
4. Yeni binary `versions/<v>/` altına açılır ve `-self-test` ile denenir (config okuma, toplayıcıların kuru çalışması).
5. `current` symlink'i atomik olarak yeni sürüme çevrilir ve agent çıkar. systemd yeni sürümü başlatır.
6. Yeni sürüm başarılı bir veri gönderimi yapınca güncellemeyi **onaylar** ve sonucu bir sonraki sync'te raporlar.
7. **Otomatik geri alma:** Yeni sürüm 3 başlatma denemesinde ya da 5 dakika içinde onay veremezse agent önceki sürüme döner ve hatayı raporlar.

### Dizin yapısı

```
/opt/openlog/infra-agent/
  versions/0.3.0/openlog-infra-agent
  versions/0.4.0/openlog-infra-agent
  current -> versions/0.4.0
/var/lib/openlog-infra-agent/update-state.json   # previous, candidate, attempts, confirmed
```

systemd unit `ExecStart=/opt/openlog/infra-agent/current/openlog-infra-agent`, `Restart=always`. deb/rpm paketleri de aynı yapıya kurar; paket yöneticisiyle yapılan güncelleme de symlink'i çevirir. Son 2 sürüm diskte tutulur.

### Kurulum yöntemine göre

| Yöntem | Kendini günceller mi | Not |
|---|---|---|
| tarball / `install.sh` | Evet | |
| deb / rpm | Evet | Paket sürümü ile çalışan sürüm farklı olabilir; `openlog-infra-agent -version` ikisini de gösterir |
| Container / DaemonSet | Hayır (`update_capable=false`) | UI yeni imaj etiketini önerir; Helm/DaemonSet güncellemesi yapılır |

## 4. Filo güncelleme politikası (D-028)

Organizasyon başına (host bazında istisnalarla):

| Ayar | Değerler |
|---|---|
| `mode` | `off` · `notify` (yalnızca UI'da gösterir) · `auto` |
| `target` | `latest` (kanaldaki son sürüm) · `patch` (mevcut minor'ın son yaması) · `pinned:<sürüm>` |
| `channel` | `stable` · `beta` |
| `rollout` | Kademeli dalgalar: ör. %1 → %10 → %50 → %100. Host'un dalgası `hash(host.id)` ile belirlenir. Bir dalga, başarısızlık oranı eşiğin altındaysa (varsayılan %5) bekleme süresinden sonra otomatik ilerler. |
| `halt_on_failure_rate` | Aşılırsa dağıtım durur ve UI'da uyarı çıkar |
| `maintenance_window` | Gün ve saat aralıkları (UTC) |
| Host istisnası | `hold` (güncelleme dışı) veya `pin` |
| Filo komutları | Dağıtımı duraklat/devam et, bir sürüme geri al |

Varsayılan politika: yeni organizasyonlarda `mode=auto`, `target=latest`, `channel=stable`, dalgalar `10/50/100`, bekleme süresi 1 saat.

## 5. Backend güncelleme (D-029)

- **SaaS:** CI/CD sürekli dağıtım yapar; rolling update ve aynı anda iki sürüm kuralı (§1) geçerlidir.
- **Rolling update güvenliği:**
  - Migration'lar **expand/contract** olarak etiketlenir. `expand` (yeni kolon/tablo) sürüm `N+1` dağıtılmadan önce çalışır.
  - `contract` (eski kolonu kaldırma) en erken bir sonraki sürümde, tüm pod'lar yeni sürümdeyken çalışır.
  - `openlog-migrate` bu kuralı zorlar. CI'da "`N` ve `N+1` aynı anda" testi koşar.
- **Self-hosted, sürüm kontrolü:** Backend imzalı release index'ini günde bir kontrol eder. Bu kontrol kapatılabilir, mirror desteklenir. UI'da "yeni sürüm var" bandı ve sürüm notları gösterilir.
- **Self-hosted, Compose otomatik güncelleme:** Opsiyonel `openlog-updater` servisi `notify` ya da `auto` modunda çalışır. Adımları:
  1. İmzalı manifest'teki imaj digest'ini çeker.
  2. PostgreSQL yedeği alır (`pg_dump`).
  3. `expand` migration'larını çalıştırır.
  4. Servisleri yeni imajla yeniden başlatır.
  5. Sağlık kontrolü başarısız olursa önceki imaja döner.
- **Self-hosted, Kubernetes:**
  - **Önerilen:** GitOps (Argo CD/Flux) ile Helm chart sürümünü takip etmek.
  - **Alternatif:** Chart'ın opsiyonel updater CronJob'u. Dar RBAC ile önce migrate Job'unu, ardından Deployment imajlarını günceller (rolling). Başarısız olursa `helm rollback` eşdeğerini uygular.
- Backend güncellemesi, uyumluluk kuralı gereği agent'ları bozmaz. Agent'lar backend'den sonra kendi politikalarıyla güncellenir.

## 6. Gözlemlenebilirlik

- `hosts` tablosunda agent sürümü zaten var. Sync ile ayrıca kurulum yöntemi, güncelleme durumu ve son hata tutulur.
- UI **Filo** ekranı şunları gösterir: sürüm dağılımı, dağıtım dalgaları ve ilerleyişi, başarısız host'lar ve hata mesajları, politika düzenleme, duraklat/geri al.
- Metrikler: `openlog_agent_updates_total{result}`, `openlog_fleet_hosts{version}`, updater olayları audit log'a yazılır.

## 7. Kabul kriterleri

- Linux container'da agent `v1 → v2` otomatik güncellenir. Bozuk `v3` kendiliğinden `v2`'ye geri döner. İmzası geçersiz manifest ve sha256'sı tutmayan arşiv reddedilir. Downgrade saldırısı reddedilir.
- Politika testleri: %10 dalgaya düşen host'lar güncellenir, diğerleri güncellenmez. Bakım penceresi dışında güncelleme olmaz. Başarısızlık eşiği aşılınca dağıtım durur.
- Compose kurulumu `vA → vB` otomatik güncellenir: migration çalışır, veri korunur, bozuk imajda geri döner.
- `N` ve `N+1` backend aynı anda çalışırken uçtan uca test yeşil kalır.
