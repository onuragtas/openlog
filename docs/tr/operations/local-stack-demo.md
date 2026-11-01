# Yerel stack demosu: Docker ile tüm sistem {#local-stack-demo-the-whole-system-with-docker}

M1'in kapanış senaryosu ([yol haritası](https://github.com/onuragtas/openlog/blob/master/docs/plan/06-roadmap.md), "Tüm sistemin Docker ile ayağa kalkması"), tek
makinede tekrarlanabilir biçimde: imzalı yerel release'ler, varsayılan portlarda `deploy/compose` stack'i, ajanı `scripts/install.sh` ile
kuran iki Debian 12 "sunucu", fleet ile ajan otomatik güncellemesi, `openlog-updater` ile backend'in kendini güncellemesi,
metrik kaybetmeden backend'i yeniden başlatma ve M2 için aynı stack'e trace gönderen APM demo servisleri
(`test/apmdemo`) ile webhook teslimatlarıyla tetiklenip çözülen bir alarm kuralı.

```sh
make stack-demo                                   # tüm aşamalar (imaj cache'leri sıcakken yaklaşık 15 dakika)
make stack-demo STACKDEMO_ARGS="--screenshots"    # ayrıca Playwright ekran görüntüleri stack-01 … stack-18
test/stackdemo/run.sh --help                      # aşamalar ve değişkenler
```

Stack sonunda ayakta kalır: <http://localhost:8080>, `admin@openlog.local` / `openlog-dev-password`
(`deploy/compose/.env` dosyasından). Her şeyi durdurup silmek için:

```sh
docker compose -p openlog --project-directory deploy/compose -f deploy/compose/docker-compose.yml \
  -f test/stackdemo/docker-compose.stackdemo.yml -f test/apmdemo/docker-compose.apmdemo.yml \
  --profile updater down -v --remove-orphans
rm -rf deploy/compose/releases deploy/compose/backups
```

APM demosu, demo bittikten sonra da yük üretmeye devam eder (~8 istek/sn); yalnızca onu
`… stop frontend orders catalog apmdemo-loadgen redis orders-db` ile durdur (aynı `docker compose` seçenekleri).

Gerekenler: Compose v2.17+ ile Docker (4 CPU / 6 GB boş rahat yeter), Go 1.26, `make`, `jq`, `curl`, `rsync`;
`--screenshots` için Node ≥ 20.19 (`web/`, `$TMPDIR/openlog-stackdemo-web` altına kopyalanır, çünkü node araçları iCloud ile
eşitlenen checkout'ların içinde çalışmaz). Host portları: 4317, 4318, 8080, 9464 (openlog), 127.0.0.1:5432/8123/9000/9092
(bağımlılıklar), 127.0.0.1:18090 (release sunucusu; 18080 e2e API portudur), 127.0.0.1:5001 (registry; macOS'ta 5000 AirPlay'dir),
127.0.0.1:18091 (alarm webhook alıcısı), 18093 (APM demo frontend'i). `deploy/compose/.env.example` içindeki değişkenlerle ve
`STACKDEMO_RELEASES_PORT` / `STACKDEMO_REGISTRY_PORT` / `STACKDEMO_ALERT_RECEIVER_PORT` /
`STACKDEMO_APM_FRONTEND_PORT` ile değiştirebilirsin.
Çıktı (süreler, ekran görüntüleri): `$STACKDEMO_OUT` (varsayılan `$TMPDIR/openlog-stackdemo`).

**Yalnızca test anahtarları.** Release'ler `dist/testkeys` ile imzalanır (`make release-testkeys`, git'e girmez) ve imajlar ile
ajanlar bu anahtara güvenecek şekilde derlenir. Bu derlemeleri asla yayınlama.

## Neler çalışıyor {#what-is-running}

| Servis (compose projesi `openlog`) | Kaynak | Rol |
|---|---|---|
| `postgres`, `kafka`, `clickhouse`, `bootstrap`, `openlog` | `deploy/compose/docker-compose.yml` | `single` profili; `openlog` = ingest + processor + api + arayüz |
| `openlog-updater` | aynı dosya, profil `updater` | backend'in kendini güncellemesi (`notify`, sonra `auto`) |
| `releases` | `test/stackdemo/docker-compose.stackdemo.yml` | `deploy/compose/releases` klasörünü `http://releases` adresinde sunan nginx (install.sh, index, manifest'ler, artifact'ler) |
| `registry` | aynı | `registry:2`; imzalı manifest'ler backend imajlarına digest ile başvurur (`localhost:5001/openlog@sha256:…`) |
| `host-services` (`demo-web-1`) | aynı, `test/stackdemo/host` | nginx, redis, postgresql içeren Debian 12; ajan install.sh ile kurulur |
| `host-plain` (`demo-plain-1`) | aynı | servissiz Debian 12 |
| `alert-receiver` | aynı, `test/alertproof/receiver` | `alert` aşamasının webhook alıcısı: `X-Openlog-Signature` imzasını doğrular (secret `STACKDEMO_ALERT_HMAC_SECRET`), istekleri kaydeder (`GET /requests`) |
| `frontend`, `orders`, `catalog`, `redis`, `orders-db`, `apmdemo-loadgen` | `test/apmdemo/docker-compose.apmdemo.yml` (`apm` aşaması) | Stack'in lisans anahtarını kullanan, ~8 istek/sn yük alan, OpenTelemetry ile enstrümante edilmiş Node/Go/PHP servisleri; `--no-deps` ile başlatılır, bu yüzden `openlog` için apmdemo override'ı uygulanmaz |

`deploy/compose/releases`, `openlog` ve `openlog-updater` içine `/releases` yoluna salt okunur bağlanır (temel compose
dosyası bunu herkes için yapar: air-gapped mirror ve güvenilen anahtar dosyaları buraya konur). Demo `.env` dosyası şunları ayarlar:

```sh
OPENLOG_IMAGE=openlog:0.9.0-stack
OPENLOG_UPDATE_CHECK_INTERVAL=1m                 # varsayılan 24h; banner demo içinde tepki vermeli
OPENLOG_RELEASE_INDEX_URL=http://releases/index.json
OPENLOG_RELEASE_TRUSTED_KEYS_FILE=/releases/trusted-keys.txt
OPENLOG_RELEASE_MIRROR_DIR=/releases             # fleet kataloğu mirror'ı doğrudan okur
OPENLOG_RELEASE_SERVE_MIRROR=true                # ajanlar ingest'ten indirir (/v1/openlog/releases/…)
OPENLOG_RELEASE_CATALOG_REFRESH=20s
OPENLOG_FLEET_SYNC_INTERVAL=60s                  # ajanlar ≥ 60 sn'ye sınırlar
OPENLOG_FLEET_CONTROLLER_INTERVAL=10s
OPENLOG_UPDATER_MODE=notify                      # backend aşamasında auto'ya geçer
OPENLOG_UPDATER_INTERVAL=30s
OPENLOG_UPDATER_HEALTH_TIMEOUT=3m
```

## Aşamalar {#phases}

`test/stackdemo/run.sh [--screenshots] [phase…]`; aşama verilmezse hepsi bu sırayla çalışır. Her aşama çalışan bir stack'e
karşı tek başına yeniden çalıştırılabilir.

| Aşama | Ne olur | Kontroller / çıktı |
|---|---|---|
| `clean` | `openlog` projesinde `down -v`, `deploy/compose/releases`, `backups` kaldırılır | |
| `env` | `deploy/compose/.env` = `.env.example` + yukarıdaki ayarlar | |
| `release` | `make release-testkeys`; 0.9.0 ve 0.9.1 için: `make docker VERSION=… IMAGE=openlog:<v>-stack RELEASE_TESTKEYS=1`, registry'ye push, `make release-local VERSION=… RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://releases RELEASE_ARCHES=<docker arch> RELEASE_IMAGE=<digest>` | her release'in `OK signature`, `OK artifact` satırları |
| `up` | mirror = `dist/v0.9.0` + **yalnızca 0.9.0** üzerinde index (0.9.1 yayınlanmamış kalır), `docker compose up -d --wait` | başlama süresi, `docker compose ps`, `"version":"0.9.0"` ile `/readyz` |
| `agents` | her host'ta: `curl -fsSL http://releases/install.sh \| sh -s -- --license-key dev-license-key --endpoint http://openlog:4318 --base-url http://releases --method tarball`; yapılandırmada anahtar olunca host'un supervisor döngüsü (systemd yerine) ajanı başlatır; sonra `demo-web-1` üzerinde `logs.auto_from_discovery: true` ve ajanın yeniden başlatılması | kurulumdan host'un `GET /api/v1/hosts` içinde görünmesine kadar saniye (hedef < 2 dk) |
| `policy` | `PUT /api/v1/fleet/policy` mod `notify`, dalgalar `[50,100]`, soak 1 dk; ekran görüntüleri stack-01 … stack-14 | |
| `publish` | mirror `v0.9.1` alır, index 0.9.0 + 0.9.1 üzerinden yeniden oluşturulur ve yeniden imzalanır | `GET /api/v1/version` `latest_available` = 0.9.1 (banner, stack-15), fleet kataloğu 0.9.1'i listeler, updater `available` |
| `fleet` | policy `auto` | 10 sn'de bir satır: rollout durumu, dalga, sayaçlar, sürümler (stack-16); iki host da 0.9.1'de (stack-17); her host'ta `current -> versions/0.9.1`, `-version`, `update-state.json`; ingest `openlog_release_mirror_requests_total` |
| `backend` | `.env` içinde `OPENLOG_UPDATER_MODE=auto`, `docker compose up -d openlog-updater` | `/readyz` 0.9.1, updater adımları backup → pull → migrate → recreate → health → cleanup → contract-migrate, `backups/*.dump`, `OPENLOG_IMAGE` digest'e yeniden yazılır, host'lar ve metrikler hâlâ yerinde (stack-18) |
| `restart` | `docker compose restart postgres kafka clickhouse openlog openlog-updater` | kesinti süresi, yeniden başlatma penceresinde host başına `system.uptime` örnekleri ve en büyük boşluk (10 sn aralık → ~20 sn üzerinde boşluk yoksa hiçbir şey kaybolmamış demektir), outcome'a göre `openlog.agent.export.items` (`buffered` arttı, `dropped` 0) |
| `apm` | `docker compose … -f test/apmdemo/docker-compose.apmdemo.yml up -d --build --no-deps --wait redis orders-db orders catalog frontend apmdemo-loadgen` (`APMDEMO_LICENSE_KEY` = stack'in anahtarı) | `GET /api/v1/apm/services` frontend, orders ve catalog'u listeleyene, `GET /api/v1/apm/map` trace ile bağlanan frontend→orders ve frontend→catalog kenarlarını içerene (edge-linking job'ı, 1 dk) ve hata grupları görünene kadar bekler (her biri ≤ 10 dk); servisleri (rpm, hata oranı, p95, Apdex), kenarları ve hata gruplarını yazdırır (stack-19, stack-20) |
| `alert` | `alert-receiver` ayağa kalkar; `stackdemo webhook` webhook kanalı (HMAC) + test gönderimi; `demo-plain-1` meşgul CPU'su üzerinde `metric_threshold` kuralı (Σ idle olmayan `cpu.mode`, 1 dk ortalama, aralık 10 sn, flapping kapalı, eşik = clamp(mevcut yük + 0.35, 0.5, 0.92), toparlanma 0.15 aşağıda); olay açılana kadar `host-plain`'in her çekirdeğinde meşgul döngüler çalışır, sonra durdurulur | olay açık, alıcıda imzalı `incident.opened`, olay çözüldü (`recovered`), imzalı `incident.resolved`, `GET /api/v1/alerts/rules/{id}/evaluations` içinde tetiklenme noktaları; 30 sn sonra olay için **tam olarak bir** açılış ve bir çözülme teslimatı (aksi halde başarısız olur); teslimat logu; kural sonunda devre dışı bırakılır (stack-21, stack-22). Yeniden çalıştırmalar önceki demo kuralını ve kanalını siler |

Ekran görüntüleri (`--screenshots`, Türkçe arayüz, 1440×900, `$STACKDEMO_OUT/shots`):

| Dosya | Gösterdiği |
|---|---|
| `stack-01-login` | giriş sayfası |
| `stack-02-hosts` | iki host |
| `stack-03-host-overview` | `demo-web-1` grafikleri |
| `stack-04-services` | keşfedilen nginx, Redis, PostgreSQL |
| `stack-05-inventory` | host envanteri |
| `stack-06-host-logs` | keşfedilen servise göre filtrelenmiş nginx log satırları |
| `stack-07-logs` | log explorer |
| `stack-08-inventory-search` | iki host'ta `openssl` paketi |
| `stack-09-settings-license-keys`, `stack-10-settings-members` | ayarlar |
| `stack-11-fleet-overview`, `stack-12-fleet-policy` | fleet: iki ajan da 0.9.0 ve güncellenebilir, policy |
| `stack-13-version` | Settings → Organization: sürüm, release kontrolü, updater |
| `stack-14-dark` | karanlık modda host overview |
| `stack-15-update-banner` | admin için "openlog 0.9.1 yayınlandı" |
| `stack-16-fleet-rollout`, `stack-17-fleet-done` | devam eden rollout, iki ajan da 0.9.1'de |
| `stack-18-after-backend-update` | 0.9.1 sürümü ve updater'ın adımları |
| `stack-19-apm-services`, `stack-20-apm-map` | demonun APM servisleri, servis haritası |
| `stack-21-alert-incident`, `stack-22-alert-evaluation-history` | açık CPU olayı, değerlendirme geçmişiyle kural |

## Elle yapmak {#doing-it-by-hand}

Script yalnızca belgelenmiş komutları art arda çalıştırır; temel olanlar şunlar:

```sh
make release-testkeys
make docker VERSION=0.9.0 IMAGE=openlog:0.9.0-stack RELEASE_TESTKEYS=1
make release-local VERSION=0.9.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://releases RELEASE_ARCHES=arm64 \
  RELEASE_IMAGE=localhost:5001/openlog@sha256:…

# mirror'a yayınla: dist/v<version> klasörünü kopyala, sonra mirror'daki içerik üzerinden index oluştur
bin/openlog-release build-index --out deploy/compose/releases/index.json --base-url http://releases \
  --keys "$(cat dist/testkeys/public.key)" deploy/compose/releases/v*/manifest.json
OPENLOG_RELEASE_SIGNING_KEY="$(cat dist/testkeys/signing.key)" \
  bin/openlog-release sign --key-env OPENLOG_RELEASE_SIGNING_KEY deploy/compose/releases/index.json

DC="docker compose -p openlog --project-directory deploy/compose -f deploy/compose/docker-compose.yml -f test/stackdemo/docker-compose.stackdemo.yml --profile updater"
$DC up -d --wait
$DC exec host-plain sh -c 'curl -fsSL http://releases/install.sh | sh -s -- --license-key dev-license-key --endpoint http://openlog:4318 --base-url http://releases --method tarball'
$DC logs -f host-plain                         # supervisor: "starting versions/0.9.0", ajan çıktısı
$DC exec host-plain sh -c 'readlink /opt/openlog/infra-agent/current; cat /var/lib/openlog-infra-agent/update-state.json'
```

`OPENLOG_AGENT_CONTAINER=0` (iki host'ta da ayarlı) container içindeki tarball düzenini güncellenebilir yapar; o olmadan
ajan container'ı algılar ve `update_capable: false` raporlar.

## Sorun giderme {#troubleshooting}

| Belirti | Kontrol et |
|---|---|
| `release`, `docker push` adımında başarısız | registry portu dolu (`STACKDEMO_REGISTRY_PORT`); macOS'ta 5000 AirPlay'dir |
| `publish` sonrası banner yok | `.env` içinde `OPENLOG_UPDATE_CHECK_INTERVAL`; `docker compose logs openlog \| grep "update check"` |
| fleet kataloğu `error` | `GET /api/v1/fleet/summary` `.catalog`; mirror'daki her değişiklikten sonra index yeniden imzalanmalı |
| ajanlar 0.9.0'da kalıyor | `GET /api/v1/fleet/hosts` `.update`; host logu (`$DC logs host-plain`): `update` mesajları; policy `mode` |
| `up` hemen sonrasında updater `error: current version …` | geçici: updater `openlog`'dan önce başlar, bir dakika içinde yeniden dener |
| backend güncellemesi `rolled_back` | `GET /api/v1/version` `.updater.steps`; `$DC logs openlog-updater` |
| `apm` haritayı bekliyor | kenarlar ilk trace'lerden sonra iki bağlama çalıştırması ister (`OPENLOG_APM_LINK_INTERVAL`, `_DELAY`); `docker compose logs openlog \| grep "apm edge"` |
| `alert` hiç tetiklenmiyor | makine meşgul: yazdırılan baseline/eşik; arayüzde kuralın değerlendirme geçmişi (`last_error`, değerler) |
| `alert` teslimat sayıları ≠ 1 | `curl 127.0.0.1:18091/requests \| jq`, `GET /api/v1/alerts/deliveries?incident_id=…` (başarısız ilk deneme yeniden denenir, yine tek kabul edilen teslimattır) |
