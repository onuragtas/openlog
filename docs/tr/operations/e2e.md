# Uçtan uca test: infra ajanı → backend {#end-to-end-test-infra-agent-→-backend}

`test/e2e`, gerçek `openlog-infra-agent`'ı Debian 12 container'larında gerçek backend'e karşı çalıştırır
(compose `single` profili: Kafka, ClickHouse, `openlog-allinone`). Her şeyi Query API üzerinden (`docs/contracts/api.md`),
API'nin göstermediği yerlerde de doğrudan ClickHouse'tan doğrular.

Bu test takımlarından hangisini CI'ın nerede çalıştırdığı (`ci.yml`, `long-tests.yml`): [ci.md](/tr/operations/ci).

## Çalıştırma {#run}

Gerekenler: Compose v2.17+ (`additional_contexts`) ile Docker, Go 1.26, yaklaşık 4 GB boş bellek.

```sh
make e2e
# şununla aynı
go test -tags e2e -v -count=1 -timeout 45m ./test/e2e
```

Bir çalıştırma yaklaşık 10 dakika sürer (imaj derleme hariç): ~2 dk veri toplama, 60 sn backend kesintisi artı toparlanma,
30 sn Kafka kesintisi artı toparlanma. Stack, test başarısız olsa da sonunda `docker compose down -v` ile kaldırılır.

| Değişken | Etkisi |
|---|---|
| `E2E_KEEP=1` | testten sonra stack'i ayakta tutar (hata ayıklamak için) |
| `E2E_SKIP_UP=1` | çalışan bir stack'i yeniden kullanır (önceki çalıştırmanın `E2E_KEEP=1` ile bıraktığı) |
| `E2E_SKIP_BUILD=1` | imajları yeniden derlemez |
| `E2E_SKIP_OUTAGE=1` | backend ve Kafka kesintisi aşamalarını atlar |
| `E2E_DOCKER=0` | hedefte özel dockerd çalışmaz (`containers` aşaması atlanır) |
| `E2E_UI=1` | stack'e karşı Playwright arayüz aşamasını çalıştırır (bkz. [UI aşaması](#ui-phase)) |
| `E2E_UI_WORKDIR` | arayüz aşaması için `web/` klasörünün kopyalanacağı yer (varsayılan `$TMPDIR/openlog-e2e-ui`) |

Compose projesi, kaydırılmış host portlarıyla `openlog-e2e`'dir (`test/e2e/e2e.env`), bu yüzden `make compose-up` stack'inin
yanında çalışabilir:

| Port | Servis |
|---|---|
| 14318 | OTLP/HTTP |
| 18080 | Query API |
| 19464 | Admin |
| 18123 | ClickHouse HTTP (kullanıcı/parola `openlog`) |
| 15432 | PostgreSQL (kullanıcı/parola `openlog`) |

Elle kontrol (depo kökünden):

```sh
E2E="docker compose -p openlog-e2e -f deploy/compose/docker-compose.yml -f test/e2e/docker-compose.e2e.yml --env-file test/e2e/e2e.env"
$E2E up -d --build --wait
$E2E down -v --remove-orphans
```

## Neler çalışır {#what-runs}

`test/e2e/docker-compose.e2e.yml`, temel compose dosyasına iki host ekler. İki imaj da `test/e2e/target/Dockerfile`
dosyasından derlenir, ajan `agents/infra` altından derlenir (sürüm `0.1.0-e2e`):

- **target** (`e2e-target`, machine-id `0e2e…0001`) nginx, redis-server ve postgresql 15 çalıştırır. Ayrıca
  `mysql -uapp -pE2eSecretPw1 --password=E2eSecretPw2 DB_TOKEN=E2eSecretPw3 mysql://app:E2eSecretPw4@db.internal:3306/app` olarak başlatılan sahte bir `/usr/local/bin/mysql` de çalışır.
  IPv6 açıktır, bu yüzden nginx `[::]:80` portunu da dinler. `/data` altına bir docker volume bağlanır.
  Ajanı log da izler (`e2e.source=logs.files` kullanıcı attribute'uyla `logs.files: /var/log/nginx/access.log`,
  `auto_from_discovery: true`, `start_at: beginning`) ve belleğe göre ilk 50 sürecin metriklerini raporlar.
  `E2E_DOCKER=1` ile (varsayılan) target `privileged` olur ve **kendi** dockerd'sini çalıştırır (`docker:dind` imajından
  kopyalanan statik binary'ler; `--bridge=none --iptables=false`, `target-docker` volume'ünde vfs depolama, socket grubu
  `openlog-agent`), içinde tek bir `e2e-sleeper` container'ı olur (`openlog-e2e/sleeper:1` imajı, statik bir binary'den
  içe aktarılır, registry erişimi yok, `--network none`). Entrypoint, target'ın süreçlerini `/sys/fs/cgroup/init` altına
  taşır, böylece iç içe container'lar gerçek bir host'taki gibi `/sys/fs/cgroup/docker/<id>` alır. Bu daemon host'un Docker'ına
  asla dokunmaz. `docker:dind` *sidecar'ı* kullanılmadı: onun container'larının cgroup'ları target'ın cgroup namespace'inden
  görünmez, bu yüzden ajan container envanterini raporlayabilir ama `container.*` metriklerini raporlayamazdı.
- **plain** (`e2e-plain`, machine-id `0e2e…0002`) yalnızca `ca-certificates`/`openssl` ve ajan içeren Debian 12'dir.

Ajan, systemd unit'inin çalıştırdığı gibi çalışır: ambient `CAP_SYS_PTRACE` + `CAP_DAC_READ_SEARCH` ile yetkisiz
`openlog-agent` kullanıcısı olarak (`setpriv`, bkz. `test/e2e/target/entrypoint.sh`). Ayarları
`interval: 10s`, `inventory_interval: 2m`, ek attribute'lar `env=e2e`, `e2e.role=<target|plain>` ve `e2e-license-key`
lisans anahtarıyla (tenant `e2e-tenant`) `http://openlog:4318` endpoint'idir.

Tenant yönetimi PostgreSQL'e dayanır (`OPENLOG_AUTH_MODE=postgres`). Temel compose'daki `bootstrap` servisi
`e2e-tenant` organizasyonunu oluşturur (sahibi `owner@e2e.test`, ingest anahtarı `e2e-license-key`, salt okunur API key
`ola_e2e-api-key`); override'daki `bootstrap-other` ise `e2e-other-tenant` organizasyonunu oluşturur (ingest anahtarı `e2e-other-key`,
API key `ola_e2e-other-api-key`). Test API'yi API key'lerle sorgular (`Authorization: Bearer`) ve OTLP
denemelerini ingest anahtarıyla gönderir.

## Neleri kontrol eder {#what-it-checks}

| Aşama | Doğrulamalar |
|---|---|
| `wait_for_data` | iki host'ta da tam bir snapshot ve ≥ 60 sn metrik var (en fazla 4 dk) |
| `hosts` | tam olarak 2 host; host_name, `os_description`, arch (= Docker sunucu mimarisi), `agent_version`, `openlog.agent.name`, entity tipi, ek attribute'lar; `GET /hosts/{id}` |
| `metrics` | `cpu.mode`'a göre `system.cpu.utilization`: 8 modun hepsi, değerler [0,1] aralığında, modların toplamı ≈ 1; duruma göre `system.memory.usage` toplamı `system.memory.limit` değerine eşit (±%1); filesystem kullanımı `/data` içerir, utilization [0,1] aralığında; disk io/operations (read, write) ve network io (receive, transmit, `eth0`, `lo` yok) için `rate`; iki host'ta da `openlog.agent.collection.interval` = 10; `openlog.agent.export.items{outcome=sent}` monoton ve artıyor |
| `inventory` | en son tam snapshot'ta os, hardware (cpu, memory), package, systemd_unit, listening_port, process, user, mount, network_interface var; > 100 dpkg paketi (target); doğru `process_name` ile 80/6379/5432 portları; `tcp:0.0.0.0:80` ve `tcp:[::]:80`; her IPv6 anahtarı köşeli parantezli; ClickHouse'taki snapshot satırının `item_count` değeri = API'nin döndürdüğü öğe sayısı |
| `services` | target: nginx (`enabled`), redis (`enabled`), postgresql (`needs_configuration`), her biri dpkg paketiyle eşleşen sürüm, bağlı paket ve portuyla. Plain: web/veritabanı servisi yok |
| `masking` | sahte mysql `cmdline` değerinde `-p***`, `--password=***`, `DB_TOKEN=***` ve `mysql://app:***@…` var, hiçbir secret yok. ClickHouse `inventory_items` ve `logs` tablolarında `E2eSecret` içeren satır yok; pozitif kontrol maskelenmiş biçimi bulur |
| `inventory_search` | `category=package&q=openssl` iki host'u da host adlarıyla döndürür; `category` zorunludur |
| `logs_exclude_inventory` | `/api/v1/logs` envanter olayı döndürmez; `logs` tablosunda da yoktur |
| `tenant_isolation` | diğer anahtar host da arama öğesi de görmez; `/hosts/{target}`, `/inventory`, `/services`, `/metrics` diğer organizasyon için ve (kendi anahtarınla) bilinmeyen host id için `404 not_found` döner; `attr.*` filtreli loglar diğer anahtar için boştur; geçersiz anahtar ve ingest anahtarı 401 alır |
| `agent_logs` | eksik nginx yollarına 100 istek (`/e2e-log/<run>/a/<i>`), ardından logrotate tarzı rotasyon (`mv access.log access.log.1 && nginx -s reopen`), ardından 100 istek daha (`…/b/<i>`). Her isteğin tam olarak bir benzersiz access ve bir error kaydı vardır (rotasyonda kayıp yok; kopyalar loglanır, teslimat at-least-once'tır). Tüm kayıtlar: `host_id`, boş `service_name`, `openlog.log.source=file`, `openlog.discovery.id=nginx`. Access kayıtlarında `e2e.source` (logs.files) ve `access.log` (b aşaması) veya `access.log`/`access.log.1` (a aşaması) değerinde `log.file.path` bulunur; error kayıtlarında `log.file.path=/var/log/nginx/error.log`, severity `ERROR` bulunur, `e2e.source` bulunmaz (yalnızca `auto_from_discovery` ile izlenir). Sunucu tarafı `attr.*` filtreleri aynı sayıları döndürür (yola göre error.log, nginx+file; redis ve journald 0 verir; izin listesinde olmayan anahtar 400 alır). ClickHouse: benzersiz (istek, dosya) satırları = 400 |
| `agent_log_rotation_unread` | okunmamış satırlarla zorunlu rotasyon: `/var/log/e2e-rotate/*.log` (logs.files). İlk satırın kaydı geldikten sonra tek bir exec 10 000 satır ekler (`/e2e-rotate/<run>/a/<i>`), `app.log` dosyasını `app.log.1` olarak yeniden adlandırır ve yeni bir `app.log` dosyasına 2 000 satır yazar (`…/b/<i>`). `rate_limit_lines` 2000/sn iken ve mv, patlamanın başlangıcından sonraki 2 sn içinde olduğunda (doğrulanır) rotasyon anında binlerce satır okunmamıştır ve yalnızca yeniden adlandırılan dosyada vardır. ClickHouse: 12 001 satırın her biri tam olarak bir kez, çok satırlı kayıt yok, hepsi `e2e.source=rotation` ile; ajan ardından `app.log.1` dosyasını kapatır (açık descriptor kalmaz) |
| `agent_journald` | target, `systemd-journald`'ı bağımsız çalıştırır (systemd PID 1 yok); `systemd-cat -p err` ve `logger -p user.warning` ile 20 girdi (`/e2e-journal/<run>/<i>`). Ajan (journalctl, `logs.journald.enabled`) her birini iletir: `openlog.log.source=journald`, `openlog.syslog.identifier=openlog-e2e`, severity `ERROR`/`WARN`, `host_id`, timestamp; `attr.openlog.syslog.identifier` filtresi 20 kaydın tamamını döndürür |
| `process_metrics` | `process.executable.name,openlog.discovery.id` ile gruplanmış `process.memory.usage`: `nginx` → `nginx`, `redis-server` → `redis`, değer > 0, birim `By` |
| `containers` | (`E2E_DOCKER=1`) running durumunda `e2e-sleeper` / `openlog-e2e/sleeper` için bir `container` envanter öğesi; `container.name=e2e-sleeper` ile `container.memory.usage` (> 0) ve `container.cpu.time` serileri |
| `ui` | (`E2E_UI=1`) gömülü arayüze karşı Playwright, aşağıya bak |
| `resilience_backend_outage` | 60 sn boyunca `docker compose stop openlog`. 20. saniyede 6390 portunda ikinci bir redis başlatılır, bu port kümesini değiştirir ve kesinti sırasında bir envanter snapshot'ını zorlar. Yeniden başlatmadan sonra: o snapshot (`tcp:127.0.0.1:6390` ile) en son tam snapshot olur; `system.uptime` bucket'ları iki host'ta da kesinti aralığını ≤ 2 aralıklık boşluklarla kapsar; `outcome=buffered` artar; `outcome=dropped` 0 kalır |
| `kafka_outage_503` | 30 sn boyunca `docker compose stop kafka`. Küçük bir OTLP/HTTP isteği sayısal `Retry-After` ile `503` alır. Kafka döndükten sonra, hiçbir şey yeniden başlatılmadan: ingest 200 döner, iki host da yeni noktalar alır ve `outcome=sent` artar. Kafka kesintisi sırasındaki örnekleme boşlukları doğrulanmaz, loglanır: ajan tek döngüde toplayıp gönderir, bu yüzden yavaş bir gönderim sonraki örneği geciktirir |

## UI aşaması {#ui-phase}

`E2E_UI=1 make e2e`, `agent_logs` aşamasından sonra `web/e2e-stack/stack.spec.ts` dosyasını (yapılandırma `web/playwright.stack.config.ts`)
e2e stack'inin gömülü arayüzüne karşı çalıştırır (`http://127.0.0.1:18080`, `openlog:e2e` imajına derlenmiştir).
`test/e2e/ui_test.go`, `web/` klasörünü `E2E_UI_WORKDIR` altına kopyalar (node araçları iCloud ile eşitlenen checkout'ların içinde çalışmaz),
`npm ci` komutunu yalnızca `package-lock.json` değiştiyse çalıştırır, `npx playwright install chromium` çalıştırır, sonra test takımını başlatır.
Node ≥ 20.19 gerekir. Başarısız testlerin trace'leri ve ekran görüntüleri: `$E2E_UI_WORKDIR/test-results-stack`.

Test bootstrap sahibi olarak giriş yapar (`test/e2e/e2e.env` dosyasından `OPENLOG_BOOTSTRAP_OWNER_EMAIL` / `_PASSWORD`) ve
şunları kontrol eder: host listesi → `e2e-target` → overview grafikleri çizilir (canvas sayısı > 0); services sekmesi nginx, Redis,
PostgreSQL gösterir; host logs sekmesi bu çalıştırmanın nginx satırlarını gösterir ve *Discovered service* + *File path* filtreleri
(`ldisc`, `lfile` → `attr.openlog.discovery.id`, `attr.log.file.path`) bunu error.log satırlarına daraltır; bilinmeyen bir host id
"Host not found" gösterir; `package` / `openssl` envanter araması iki host'u da listeler; Settings → License keys,
`bootstrap` anahtarını önekiyle listeler.

Ayakta tutulan bir stack'e karşı: `E2E_SKIP_UP=1 E2E_KEEP=1 E2E_UI=1 go test -tags e2e -v -count=1 -run 'TestE2E/(wait_for_data|agent_logs|ui)$' ./test/e2e`.

## TLS entegrasyon testi {#tls-integration-test}

`make tlstest` (`go test -tags tlstest -v -count=1 -timeout 30m ./test/e2e/tlstest`),
[config.md](/tr/contracts/config#tls-and-sasl-all-services-that-use-the-dependency) içindeki TLS/SASL ayarlarını uçtan uca kanıtlar.
`crypto/x509` ile bir CA, sunucu/istemci sertifikaları ve ilgisiz bir CA üretir (`internal/testutil/testcerts`, `/tmp` altında yeni bir
klasöre yazılır, hiçbir şey commit edilmez) ve `openlog-tlstest` compose projesini başlatır
(`test/e2e/tlstest/docker-compose.yml`, çalışma ağacından derlenen `openlog:tlstest` imajı):

| Servis | TLS kurulumu | Host portları |
|---|---|---|
| `kafka` | KRaft, istemci listener'ları SSL (mTLS zorunlu) ve SASL_SSL (PLAIN, SCRAM-SHA-256/512; SCRAM kullanıcıları storage format sırasında eklenir), düz metin istemci listener'ı yok | 26093 (SSL), 26094 (SASL_SSL) |
| `ch1`, `ch2` | 2 shard; native protokol yalnızca 9440'ta (`tcp_port` kaldırılmış), istemci sertifikası zorunlu, `remote_servers` içinde `<secure>1</secure>` | 26440/26441 (native TLS), 26123/26124 (HTTP) |
| `postgres` | `ssl=on` | 26432 |
| `openlog`, `bootstrap`, `loadgen` | SASL_SSL SCRAM-SHA-512, ClickHouse mTLS (doğrudan shard insert'leri), PostgreSQL `verify-full` ile allinone | 26080 (API), 26318, 26464 |
| `badca-*` (profil `badca`) | PostgreSQL / ClickHouse / Kafka için ilgisiz CA kullanan api / processor / ingest | 26466 (ingest admin) |

| Test | Doğrulamalar |
|---|---|
| `TestKafkaClients` | `queue.ClientOptions` istemcileri: mTLS, SASL_SSL PLAIN, SCRAM-SHA-256, SCRAM-SHA-512 openlog topic'lerini listeler; `insecure_skip_verify` ilgisiz bir CA ile çalışır; yanlış CA → `certificate signed by unknown authority`; yanlış SCRAM parolası → `SASL_AUTHENTICATION_FAILED`; istemci sertifikasız bağlantı ve düz metin reddedilir |
| `TestClickHouseClients` | mTLS bağlanır ve 2 shard görür; `SERVER_NAME` override; yanlış CA, ad uyuşmazlığı, istemci sertifikasının olmaması ve düz metin reddedilir |
| `TestPostgresClients` | `verify-full` + `OPENLOG_POSTGRES_TLS_CA_FILE`, `pg_stat_ssl.ssl = true` ile bağlanır; yanlış CA reddedilir |
| `TestDataFlowsOverTLS` | loadgen host'ları ve logları API'den görünür; `metrics_local` satırları **iki** shard'da da var; `system.query_log` içinde `openlog` kullanıcısının her native sorgusu `is_secure`; `openlog-processor` consumer group'u Stable; metrics topic'inde kayıt var; tüm `openlog-*` PostgreSQL oturumları TLS kullanıyor |
| `TestWrongCAFailsClearly` | `badca-postgres` ve `badca-clickhouse`, `x509: certificate signed by unknown authority` ile `waiting for …` loglar; `badca-kafka`'nın `/readyz` yanıtı 200 değildir ve bu hatayı adıyla söyler |

`TLSTEST_KEEP=1` stack'i ayakta tutar, `TLSTEST_SKIP_BUILD=1` `openlog:tlstest` imajını yeniden kullanır, `TLSTEST_WORKDIR` sertifika
klasörünü sabitler (`TLSTEST_KEEP=1` ile yeniden bağlanmak için gerekir). Stack sonunda `down -v` ile kaldırılır.

## Tiered storage testi {#tiered-storage-test}

`make tieredtest` (`test/e2e/tieredtest`, compose projesi `openlog-tiered`, host portları 29000/29123, openlog imajı yok),
değiştirilmemiş `deploy/compose/clickhouse/storage-tiered.xml` ve `storage-tiered-warm.xml` dosyalarıyla MinIO ve 2 shard × 2 replica bir
ClickHouse cluster'ı çalıştırır, şemayı ve `migrate.ApplyTableTTLs` adımını süreç içinde uygular; ardından her replica'da warm ve
S3'e taşımaları, cache temizliği ve tüm sunucuların yeniden başlatılmasından sonra Distributed tablolar üzerinden okumaları, cold
part'ların silinmesini, S3 kesintisini (insert'ler, hot/cold sorgular, sunucu yeniden başlatma) ve toparlanmayı, son olarak da devre
dışı bırakmayı kontrol eder. Neyi doğruladığı ve ölçülen hata davranışı: [tiered-storage.md](/tr/operations/tiered-storage#test). `TIEREDTEST_KEEP=1` stack'i ayakta tutar.
`TIEREDTEST_BACKUP=1`, devre dışı bırakmadan önce `backup_restore` aşamasını ekler: part'lar `warm`/`cold` üzerindeyken MinIO'daki
`openlog-backup` bucket'ına `BACKUP DATABASE openlog ON CLUSTER … TO S3`, `DROP DATABASE … SYNC`, `RESTORE`, ardından düz tablo
başına satır sayıları ve rollup'ların mantıksal kontrolleri, Distributed sayıları, `warm`/`cold`'a geri taşımalar ve etkisiz bir TTL adımı
([Yedekler ve geri yükleme](/tr/operations/tiered-storage#backups-and-restore)). Birkaç dakika ekler ve aşırı yüklü olmayan bir host
gerektirir (yoksa Keeper timeout'ları olur).

## Hata ayıklama {#debugging}

Stack'i ayakta tut: `E2E_KEEP=1 make e2e`, sonra `E2E_SKIP_UP=1 E2E_KEEP=1 go test -tags e2e -v -count=1 -run 'TestE2E/(wait_for_data|masking)' ./test/e2e` ile tekrarla.
`wait_for_data` önce çalışmalıdır, unutma. Bir kesinti aşamasından sonra 6390'daki ek redis, stack yeniden oluşturulana kadar ayakta kalır.
Test başarısız olunca `openlog`, `target` ve `plain` loglarının sonunu yazdırır.

```sh
$E2E ps
$E2E logs -f openlog                      # ingest/processor/api (JSON loglar)
$E2E logs target plain | grep -v '"level":"INFO"'   # ajan uyarıları: export failed; buffering, permission denied
$E2E exec target openlog-infra-agent -once -config /etc/openlog-infra-agent/config.yaml | jq .discovered_services
$E2E exec target ss -ltnp                 # gerçekte ne dinliyor

KEY='Authorization: Bearer ola_e2e-api-key'
curl -s -H "$KEY" localhost:18080/api/v1/hosts | jq
curl -s -H "$KEY" "localhost:18080/api/v1/hosts/0e2e0000000000000000000000000001/services" | jq '.items[].data | {rule_id, version, integration}'
curl -s -H "$KEY" "localhost:18080/api/v1/hosts/0e2e0000000000000000000000000001/metrics?name=openlog.agent.export.items&agg=last&group_by=outcome&step=10s" | jq
```

ClickHouse (HTTP portuna `curl` ile veya `$E2E exec clickhouse clickhouse-client -u openlog --password openlog`):

```sql
-- host başına snapshot'lar, en yenisi önce (item_count ile saklanan öğeler)
SELECT s.host_id, s.snapshot_id, s.snapshot_time, s.item_count, i.stored
FROM openlog.inventory_snapshots AS s
LEFT JOIN (SELECT host_id, snapshot_id, uniqExact(category, item_key) AS stored
           FROM openlog.inventory_items GROUP BY host_id, snapshot_id) AS i
  ON s.host_id = i.host_id AND s.snapshot_id = i.snapshot_id
ORDER BY s.host_id, s.snapshot_time DESC;

-- host başına metrik tazeliği
SELECT host_name, max(timestamp), count() FROM openlog.metrics WHERE timestamp > now() - INTERVAL 10 MINUTE GROUP BY host_name;

-- kesinti çevresindeki örnek zaman damgaları (boşluk = kaybolan veya henüz yeniden gönderilmemiş veri)
SELECT timestamp FROM openlog.metrics WHERE metric_name = 'system.uptime' AND host_name = 'e2e-target' ORDER BY timestamp DESC LIMIT 40;

-- secret sızıntısı kontrolü
SELECT count() FROM openlog.inventory_items WHERE positionCaseInsensitive(data, 'E2eSecret') > 0;
```

```sh
curl -s -u openlog:openlog 'localhost:18123/?query=SELECT+host_name,max(timestamp)+FROM+openlog.metrics+GROUP+BY+host_name'
curl -s localhost:19464/metrics | grep -E 'openlog_(ingest|processor)_'   # ingest/processor sayaçları
```
