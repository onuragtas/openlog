# 05 — Infra Agent (`openlog-infra-agent`)

Lisans: Apache-2.0 · Dil: Go · Platform: Linux (x86_64, arm64) · Konum: `agents/infra` (modül `github.com/onuragtas/openlog/agents/infra`)

Agent'ın görevi: kurulduğu makinede **ne var ne yok** bulmak, izlemek ve bunu OTLP ile göndermek.
Belirli bir müşteriye veya ortama özel kod içermez; tüm tanıma işi **genel toplayıcılar + veri tabanlı keşif kuralları** ile yapılır.

## 1. Üç katman

| Katman | Soru | Çıktı |
|---|---|---|
| **Metrikler** | Makine nasıl çalışıyor? | OTLP metrics (CPU, bellek, disk, ağ, process, container) |
| **Envanter** | Makinede ne var? | OTLP log olayları (`event.name=openlog.inventory.item`) |
| **Keşif** | Bunlar ne işe yarıyor, nasıl izlenir? | `discovered_service` kategorisinde envanter öğeleri + otomatik entegrasyon önerisi |

## 2. Metrikler

OTel `hostmetrics` isimlendirmesi kullanılır; tam liste [semantic-conventions.md](../contracts/semantic-conventions.md)'de.

- **Sistem:** CPU zamanı ve kullanımı, load average, bellek, swap, dosya sistemi, disk I/O, ağ I/O/hata/drop, uptime, process sayıları
- **Process (M1, uygulandı):** CPU'ya göre ilk N ile RSS'e göre ilk N process'in birleşimi (varsayılan 20 + 20); PID değişince seri değişir
- **Container (M1, uygulandı):** yalnızca cgroup v2 üzerinden CPU, bellek, blok I/O, ağ (Docker, containerd, CRI-O, Podman cgroup dizinleri); cgroup v1 host'larda container metriği yok
- Varsayılan toplama aralığı 10 sn; sayaçlar cumulative, kullanım oranları (utilization) agent'ta hesaplanan gauge

## 3. Envanter — "makinede ne var"

Agent başlangıçta, her saat ve değişiklik algıladığında **tam anlık görüntü (snapshot)** gönderir.
Her öğe bir kategori ve bu kategori içinde benzersiz bir anahtar taşır.

| Kategori | Kaynak | Anahtar |
|---|---|---|
| `os` | `/etc/os-release`, `uname` | sabit `os` |
| `hardware` | `/proc/cpuinfo`, `/proc/meminfo`, `/sys/class/dmi/id` | `cpu`, `memory`, `dmi` |
| `kernel_module` | `/proc/modules` | modül adı |
| `package` | dpkg (`/var/lib/dpkg/status`), rpm (`rpmdb.sqlite` doğrudan okunur, cgo/bağımlılık yok; eski BDB/ndb için `rpm` CLI), apk (`/lib/apk/db/installed`) | `<yönetici>:<paket>` |
| `systemd_unit` | `/etc/systemd`, `/lib/systemd`, `/run/systemd` (D-Bus ile aktif durum: yapılmadı, M2) | unit adı |
| `listening_port` | `/proc/net/tcp`, `/proc/net/tcp6`, `/proc/net/udp*` + inode→pid eşleşmesi | `<proto>:<adres>:<port>` |
| `process` | `/proc/<pid>/{comm,cmdline,exe,status}` | `<exe yolu>` (aynı binary'nin kopyaları tek öğe, sayı attribute'ta) |
| `container` | Docker Engine API (unix socket; containerd/CRI-O/Podman: M2) | container id |
| `user` | `/etc/passwd` (yalnızca isim/uid/shell) | kullanıcı adı |
| `network_interface` | `/sys/class/net` | arayüz adı |
| `mount` | `/proc/self/mountinfo` | mount noktası |

Sunucu tarafında bir host'un güncel envanteri, o host'un **son snapshot'ındaki** öğelerdir. Önceki snapshot'ta olup yenisinde olmayan öğe "kaldırıldı" kabul edilir.
Böylece "hangi host'larda openssl 3.0.2 var?" veya "bu makineye dün ne kuruldu?" gibi sorular sorgulanabilir.

**Hassas veri kuralı:** Ortam değişkenleri, komut satırındaki parola benzeri argümanlar (`--password=`, `-p…`, `*_TOKEN=` vb.) ve dosya içerikleri **gönderilmez**. Komut satırı agent'ta maskelenir.

## 4. Otomatik keşif — "bunlar ne"

Keşif motoru envanter öğelerini **kural kataloğu** ile eşleştirir. Katalog kod değil, **veridir** (YAML); agent'a gömülü gelir, `/etc/openlog-infra-agent/discovery.d/` ile genişletilebilir ve ileride sunucudan güncellenir.

### Kural örneği

```yaml
id: redis
name: Redis
category: database
match:                      # herhangi biri eşleşirse aday
  - process: { exe_basename: ["redis-server"] }
  - systemd_unit: { name_regex: "^redis(-server)?(@.*)?\\.service$" }
  - container: { image_regex: "(^|/)redis(:|$)" }
  - listening_port: { port: 6379, process_exe_basename: ["redis-server"] }
version:                    # sırayla denenir; binary çalıştırılmaz
  - package: ["redis-server", "redis"]
  - cmdline_regex: "redis-server.*v=(\\S+)"
endpoints:
  - from: listening_port    # keşfedilen servisin dinlediği portlar
integration:
  id: redis
  auto_enable: true         # kimlik bilgisi gerektirmeyen durumlarda
  requires: []              # ör. ["credentials"] → kullanıcıya "yapılandır" olarak gösterilir
apm_hint: null
```

Uygulama çalışma ortamları da keşfedilir ve UI'da **APM agent kurulum önerisi** olarak gösterilir:

```yaml
id: php-fpm
name: PHP-FPM
category: runtime
match:
  - process: { exe_basename_regex: "^php-fpm[0-9.]*$" }
version:
  - cmdline_regex: "php-fpm: master process \\((\\S+)\\)"
  - package_regex: "^php[0-9.]*-fpm$"
apm_hint: { language: php, agent: openlog-agent-php }
```

### Başlangıç kataloğu (M1 hedefi)

| Kategori | Servisler |
|---|---|
| Web / proxy | nginx, Apache httpd, HAProxy, Caddy, Traefik, Envoy |
| Veritabanı | MySQL, MariaDB, PostgreSQL, MongoDB, Redis, Memcached, Elasticsearch/OpenSearch, ClickHouse, Cassandra |
| Kuyruk | RabbitMQ, Kafka, NATS, ActiveMQ |
| Container / orkestrasyon | Docker, containerd, Podman, kubelet, k3s |
| Çalışma ortamı (APM önerisi) | PHP-FPM, Node.js, JVM (Java), Python (gunicorn/uwsgi/uvicorn), .NET, Go binary'leri |
| Sistem | sshd, cron, systemd-journald, chrony/ntpd, fail2ban |

### Keşif çıktısı

Her keşfedilen servis için: `openlog.discovery.id`, ad, kategori, sürüm, eşleşen kaynaklar, process'ler, dinlenen portlar, entegrasyon durumu (`enabled` / `needs_configuration` / `not_available`), APM önerisi.

### Entegrasyonlar (M2)

Keşfedilen servisler için metrik toplayıcılar: kimlik bilgisi gerekmeyenler otomatik açılır (nginx `stub_status` bulunursa, Redis `INFO`, Docker socket); gerekenler UI'da "yapılandır" olarak listelenir.
Entegrasyonlar agent içinde modül olarak durur; aynı kural katalogu hangi entegrasyonun çalışacağını belirler.

## 5. Loglar (M1, uygulandı)

- **Dosya:** glob + `exclude`, multiline başlangıç deseni, rename ve copytruncate rotasyonu, cihaz+inode+ilk 1 KiB hash ile dosya kimliği, satır boyu limiti, dosya başına hız limiti (fazlası dosyada bekler, atılmaz), `start_at` (baştan/sondan)
- **journald:** `journalctl --output=export --follow` alt süreci, kaydedilen cursor ile devam; PRIORITY → severity
- **Teslim garantisi:** en az bir kez; offset'ler yalnızca batch gönderildikten ya da disk buffer'a yazıldıktan sonra ilerler. Gönderim birikmişken okuma durur (log seli metrik ve envanteri buffer'dan itemez).
- **Keşiften log:** kurallar `logs:` glob'ları tanımlayabilir (nginx, apache, mysql, mariadb, postgresql, redis); `logs.auto_from_discovery: true` ise otomatik tail edilir (varsayılan kapalı)
- **Maskeleme:** loglar kullanıcı verisidir; `logs.mask_secrets` ile isteğe bağlı (varsayılan kapalı)
- Keşfedilen servise ait process'ler container içindeyse **her container ayrı instance** olur (iki redis container'ı tek servis olarak birleşmez)
- Bilinen açıklar: containerd/CRI-O/Podman envanteri, container stdout logları, `**` glob, copytruncate'te kopya ile sonraki okuma arasındaki kısa yarış

## 6. Güvenilirlik

- **Disk buffer:** Gönderilemeyen batch'ler diske yazılır (varsayılan limit 256 MiB, dolunca en eski veri atılır ve bu durum kendi metriğiyle raporlanır).
- **Retry:** `429`/`503`/ağ hatasında exponential backoff + jitter; `Retry-After` header'ına uyulur.
- **Kaynak bütçesi:** < %1 CPU, < 50 MB RSS; aşılırsa toplama aralığı otomatik uzar ve bu durum raporlanır.
- **Self-telemetry:** `openlog.agent.*` metrikleri (gönderilen/atılan veri, buffer doluluğu, toplayıcı süreleri).

## 7. Yetkiler

- Varsayılan: `openlog-agent` kullanıcısı + gerekli capability'ler (`CAP_DAC_READ_SEARCH`, `CAP_SYS_PTRACE` — başka kullanıcıların `/proc/<pid>/exe` ve soket inode'larını okumak için).
- Root olmadan çalıştırıldığında erişilemeyen öğeler atlanır ve kapsam eksikliği `openlog.agent.permission_denied` sayacıyla raporlanır.
- Container içinde çalışırken host dosya sistemi `/host` altına bağlanır; `host.root_path` ayarı ile tüm yollar buna göre çözülür.

## 8. Konfigürasyon

```yaml
# /etc/openlog-infra-agent/config.yaml
license_key: "…"                 # ya da OPENLOG_LICENSE_KEY
endpoint: "https://ingest.openlog.example:4318"   # ya da OPENLOG_ENDPOINT
interval: 10s
inventory_interval: 1h
host:
  root_path: /                   # container içinde /host
  extra_attributes: { env: prod, team: payments }
collectors:
  cpu: true
  memory: true
  load: true
  filesystem: true
  disk: true
  network: true
  uptime: true
  processes: true
inventory:
  enabled: true
discovery:
  enabled: true
  rules_dir: /etc/openlog-infra-agent/discovery.d
buffer:
  dir: /var/lib/openlog-infra-agent/buffer
  max_bytes: 268435456
```

## 9. Paketleme

`.deb`, `.rpm`, statik binary tarball, Docker imajı, `install.sh` (dağıtımı algılar, paket deposunu ekler, servisi başlatır), systemd unit, Kubernetes DaemonSet (M4).
