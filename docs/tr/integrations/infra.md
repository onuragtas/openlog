# openlog-infra-agent {#openlog-infra-agent}

Açık kaynak bir gözlemlenebilirlik platformu olan [openlog](https://github.com/onuragtas/openlog)'un makine ajanı.
**Üzerinde çalıştığı makinede ne olduğunu** bulur, izler ve her şeyi OTLP/HTTP üzerinden gönderir.
Ortama özel kod içermez. Her şeyi genel toplayıcılar ve **veri odaklı bir YAML discovery kural kataloğu** tanır.

Platformlar: Linux, macOS ve Windows (amd64 ve arm64; bkz. [macOS ve Windows](#macos-and-windows)). Lisans: Apache-2.0.

## Neler toplar {#what-it-collects}

**Metrikler** (her `interval`'de, OTel `hostmetrics` adlarıyla, bkz. `openlog/docs/contracts/semantic-conventions.md` §2):
CPU süresi ve kullanımı, mantıksal CPU sayısı, load average, bellek ve paging kullanımı, dosya sistemi kullanımı ve doluluk oranı,
disk I/O ve operasyonları, ağ I/O'su, paketler, hatalar ve düşenler, uptime ve duruma göre süreç sayıları.
Dosya sistemleri bind mount edilmiş dosyaları hariç tutar (Docker'ın `/etc/hosts`, `/etc/hostname`, `/etc/resolv.conf` dosyaları).

**Donanım sensörleri** (`sensors`, Linux): `/sys/class/hwmon` üzerinden `system.hardware.temperature`, `.fan.speed`, `.voltage`,
`.current` ve `.power` (`sensors(1)`'in gösterdiği değerlerin aynısı); kernel'in high ve critical eşikleri de kendi serileri olarak gelir.
hwmon olmayan bir makine hata yerine hiçbir şey raporlamaz; 0 rpm'deki bir fan raporlanır, çünkü duran fan en önemli okumadır.

**Süreç metrikleri** (`process_metrics`): CPU'ya göre ilk 20 ile belleğe göre ilk 20 sürecin birleşimi için `process.cpu.utilization`,
`process.memory.usage` (RSS), `process.memory.virtual`, `process.threads` ve `process.open_file_descriptors`; `process.pid`,
`process.executable.name`, `process.executable.path`, `process.owner` ve `openlog.discovery.id` (sürecin ait olduğu bulunan servisin
kural id'si) ile. Yalnızca bu süreçler seri üretir; seriler PID'lerle birlikte değişir.

**Container metrikleri** (`containers`, yalnızca cgroup v2): `container.cpu.time`, `container.cpu.utilization`, `container.memory.usage`
(current − inactive_file), `container.memory.limit`, `container.blockio.io`, `container.blockio.operations` ve `container.network.io`
(host ağı kullanan container'lar için değil); `container.id`, `container.name`, `container.image.name`, `container.image.tags`,
`container.runtime` ve container label'larından `docker.compose.project`, `docker.compose.service` ve
`k8s.pod.name`/`k8s.namespace.name`/`k8s.container.name` ile.
Container'lar `/sys/fs/cgroup` gezilerek 64 hex id'li dizinlerden bulunur (Docker, containerd/CRI, CRI-O, Podman); adlar, imajlar ve label'lar
Docker Engine API'den, containerd/CRI-O için (Kubernetes node'ları) CRI'dan (`containers.cri_sockets`) gelir. `openlog.container.status` (değer 1,
`openlog.container.state`, `openlog.container.health`, `openlog.container.started_at`) ve `container.restarts`, çalışan veya son 24 saat içinde
durmuş her Docker container'ını raporlar (en fazla 500), böylece durmuş container'lar openlog'un Containers sayfasında görünür kalır; başlangıç
zamanı, yeniden başlatma sayısı ve sağlık bilgisi `GET /containers/{id}/json` üzerinden gelir (yeni container'lar, durum değişiklikleri, çalışan
container başına dakikada bir).
Kendi telemetrisini de gönderir: `openlog.agent.export.items`, `openlog.agent.buffer.usage`, `openlog.agent.collector.duration`,
`openlog.agent.permission_denied` ve `openlog.agent.collection.interval` (saniye cinsinden geçerli metrik aralığı, CPU bütçesi kontrolü geri
çekildiğinde yükselir), ayrıca `openlog.agent.update.state{state}` ve `openlog.agent.update.attempts{result}` (bkz. [Kendini güncelleme](#self-update)).

**Envanter**, OTLP log kayıtlarından oluşan tam bir snapshot olarak gönderilir (`event.name=openlog.inventory.item`, ardından
`openlog.inventory.snapshot`). Snapshot'lar başlangıçta, her `inventory_interval`'de ve ucuz bir değişiklik parmak izi değiştiğinde
gider (paket veritabanı mtime'ı, systemd unit dizinleri, dinlenen port kümesi).

| Kategori | Kaynak |
|---|---|
| `os` | `/etc/os-release`, `/proc/sys/kernel/*`, `/proc/stat` |
| `hardware` (`cpu`, `memory`, `dmi`) | `/proc/cpuinfo`, `/proc/meminfo`, `/sys/class/dmi/id` (seri numaraları yok) |
| `kernel_module` | `/proc/modules` |
| `package` | dpkg `/var/lib/dpkg/status`, apk `/lib/apk/db/installed`, rpm `rpmdb.sqlite` (doğrudan okunur, aşağıya bak) |
| `systemd_unit` | `/etc`, `/run`, `/usr/local/lib`, `/usr/lib`, `/lib` içindeki unit dosyaları; `*.wants` linklerinden enabled durumu; mask'ler. D-Bus sistem veriyoluna (makine kökü altında `/run/dbus/system_bus_socket`) erişilebiliyorsa: active/sub durumu, active since, yeniden başlatmalar, bellek/CPU (accounting ile), yüklü template instance'ları ve transient servisler; aksi halde yalnızca dosya verisi. Ayrıcalık veya unit değişikliği gerekmez (systemd bu okumalara her kullanıcı için izin verir; sıkılaştırılmış unit `AF_UNIX`'e izin verir) |
| `listening_port` | `/proc/net/{tcp,tcp6,udp,udp6}` + `/proc/<pid>/fd` üzerinden soket inode → PID; anahtar `tcp:0.0.0.0:22`, IPv6 köşeli parantezli `tcp:[::]:22` |
| `process` | `/proc/<pid>/{stat,exe,cmdline,status,cgroup}`, executable'a göre gruplanır; `command` = argv0 basename (örn. `/usr/bin/redis-check-rdb` için `redis-server`) |
| `user` | `/etc/passwd` (ad, uid, gid, home, shell) |
| `network_interface` | `/sys/class/net` + arayüz adresleri |
| `mount` | `mountinfo` |
| `container` | `containers.docker_socket` üzerinden Docker Engine API `GET /containers/json?all=1` (id, ad, imaj, imaj id, durum, sağlık, oluşturulma, başlama/bitiş, yeniden başlatma sayısı, çıkış kodu, değeri ≤ 256 bayt label'lar, portlar); containerd/CRI-O container'ları `containers.cri_sockets` üzerinden CRI `runtime.v1` API'si ile (`ListContainers`, `ContainerStatus`) (`runtime` `containerd`/`cri-o`, Kubernetes pod/namespace/container label'ları, yeniden başlatma sayısı annotation'ı, log yolu), Docker'ınkilerle birleştirilir |
| `discovered_service` | discovery motoru (aşağıda); `command` ve kuralın `log_paths` değerini içerir |

Secret'lar komut satırlarında ve `ExecStart` içinde maskelenir:
- `--password=***` ve benzeri secret flag'leri
- secret'a benzeyen anahtarlar için `TOKEN=***`; `file`, `path` veya `dir` ile biten anahtarlar hariç (`--keyfile=/etc/x.pem` okunur kalır)
- `scheme://user:***@`
- bitişik `-p***`, ama yalnızca MySQL/MariaDB istemcileri için (`mysql`, `mysqldump`, `mariadb*`, …), böylece `ssh -p22` ve `docker run -p 8080:80` okunur kalır
Ortam değişkenleri ve dosya içerikleri asla toplanmaz (yapılandırdığın log dosyaları hariç, aşağıda).

rpm: `/var/lib/rpm/rpmdb.sqlite` (veya `/usr/lib/sysimage/rpm/rpmdb.sqlite`), küçük, yerleşik, salt okunur bir SQLite okuyucuyla okunur
(cgo yok, üçüncü parti kod yok; commit edilmiş WAL frame'leri dahil) ve rpm header blob'ları doğrudan ayrıştırılır. Bu RHEL/Rocky/Alma 9+,
Fedora 33+ ve CentOS Stream 9'u kapsar. Daha eski Berkeley DB (`/var/lib/rpm/Packages`, RHEL 7/8) ve ndb (SUSE) veritabanları, ajanın erişebildiği
bir `rpm` binary'si varsa `rpm -qa --queryformat …` çalıştırılarak okunur (`host.root_path` altında `--root` ile); scratch container imajında
bu binary yok, bu yüzden o makinelerde container imajı hiç rpm paketi raporlamaz.

Docker: Docker soketi genelde `root:docker 0660` olur. Paketler `openlog-agent`'ı varsayılan olarak `docker` grubuna ekler (bkz.
[Docker erişimi](#docker-access)). Erişim yoksa hata `openlog.agent.permission_denied{collector="containers"}` içinde sayılır, container
metrikleri yine cgroup'lardan gelir (adlar/imajlar olmadan) ve entegrasyonlar container port ve adreslerini bunun yerine `/proc/<pid>/net` ve
`docker-proxy` üzerinden çıkarır.

## Docker erişimi {#docker-access}

deb/rpm paketleri ve `scripts/install.sh`, grup varsa `openlog-agent`'ı `docker` grubuna ekler, böylece ajan Docker Engine API'yi
okuyabilir (container adları, imajlar, durumlar, container'lardaki nginx, Redis, … servislerine ulaşmak için kullanılan port ve IP'ler ve json-file
loglar okunamıyorsa container log akışları). Mevcut üyelere dokunulmaz. Yeni üyelik ancak yeniden başlatmadan sonra geçerli olur; kurulum
araçları çalışan bir servisi bir kez yeniden başlatır.

**Docker grubu üyeliği root'a denktir:** `openlog-agent` kullanıcısını kontrol eden herkes Docker'ı, dolayısıyla makineyi kontrol edebilir.

Devre dışı bırakmak için (bunlardan herhangi biri; dosya ayrıca sonraki yükseltmelerin ve yeniden çalıştırmaların tekrar eklemesini önler):

```sh
curl -fsSL …/install.sh | sudo sh -s -- --no-docker-access …   # devre dışı bırakma dosyasını oluşturur
sudo env OPENLOG_AGENT_DOCKER_ACCESS=0 apt-get install ./openlog-infra-agent.deb   # veya dnf/rpm; dosyayı da oluşturur
sudo mkdir -p /etc/openlog-infra-agent && sudo touch /etc/openlog-infra-agent/no-docker-access   # kurulumdan önce
```

Düz `sudo VAR=… apt-get …` yalnızca sudoers politikası değişken ayarlamaya izin veriyorsa çalışır; `sudo env VAR=… …` her zaman çalışır.

Kurulu bir makinede geri almak için:

```sh
sudo gpasswd -d openlog-agent docker && sudo touch /etc/openlog-infra-agent/no-docker-access && sudo systemctl restart openlog-infra-agent
```

Sonradan yeniden açmak için dosyayı sil ve kurulumu yeniden çalıştır (veya `usermod -aG docker openlog-agent` ve yeniden başlat). systemd
unit'inin sıkılaştırması soketi engellemez (`AF_UNIX`'e izin verilir; `ProtectSystem=strict` sokete bağlanmayı önlemez).
Container imajı (`packaging/container.yaml`) etkilenmez: root olarak çalışır ve sokete `/host` altına mount edilen makine kökü üzerinden ulaşır.

## Entegrasyonlar {#integrations}

Bulunan servisler metrik entegrasyonları alır (kontrat: `semantic-conventions.md` §6; metrik adları OpenTelemetry Collector receiver'larınınkidir,
yani OTel Collector verisi aynı panellere oturur):

| Entegrasyon | Kurallar | Kaynak | Kimlik bilgileri | Metrikler |
|---|---|---|---|---|
| `nginx` | nginx | `stub_status`, bulunan portlarda yoklanır (`/nginx_status`, `/stub_status`, `/basic_status`, `/status`, `/server_status`; önce http, sonra https; sertifikalar yalnızca loopback'te doğrulanmaz) | yok | `nginx.*` |
| `redis` | redis | TCP, TLS veya unix soket üzerinden `INFO` | isteğe bağlı `password` / ACL `username` | `redis.*` |
| `mysql` | mysql, mariadb | global status, `performance_schema` io bekleyişleri (top-N), replica durumu | zorunlu | `mysql.*` |
| `postgresql` | postgresql | `pg_stat_database`, `pg_stat_bgwriter`/`pg_stat_checkpointer`, `pg_stat_replication`, `pg_locks`, top-N `pg_stat_user_tables`/`indexes` | zorunlu | `postgresql.*` |
| `docker` | docker | Docker Engine API erişilebilirliği (kendi metriği yok: `container.*` cgroup'lardan gelir, OTel'de motor düzeyinde `docker.*` adları yok) | soket erişimi | — |
| `mssql` | mssql | `sys.dm_os_performance_counters` (bağlantılar, batch request'ler, deadlock'lar, lock bekleyişleri, buffer cache hit ratio, page life expectancy), `sys.master_files` boyutları, top-N `sys.dm_os_wait_stats`; her işletim sistemi, `endpoint` ile uzak sunucular da | zorunlu (SQL Server authentication) | `sqlserver.*` |
| `iis` | iis | WMI üzerinden Windows performans sayaçları (site başına Web Service: bağlantılar, metoda göre istekler, gönderilen/alınan bayt ve dosyalar, not-found hataları; uygulama havuzu durumu) | yok (yalnızca Windows) | `iis.*` |
| `apache` | apache-httpd | makinece okunur biçimiyle `mod_status`, bulunan portlarda yoklanır (`/server-status?auto`, `/status?auto`, `/apache-status?auto`, `/httpd-status?auto`) | yok | `apache.*` |
| `memcached` | memcached | metin protokolü üzerinden `stats` (SASL sunucuları: `needs_configuration`) | yok | `memcached.*` |
| `haproxy` | haproxy | CSV istatistikleri: stats sayfası (`/;csv`, `/stats;csv`, …) veya runtime soketinin `show stat` komutu; frontend, backend ve sunucu başına bir resource (en fazla 500) | yok | `haproxy.*` |
| `php-fpm` | php-fpm | PHP-FPM yapılandırmasının her havuzu (`/etc/php/*/fpm/pool.d/*.conf`, `/etc/php-fpm.d/*.conf`, Remi, cPanel, Plesk içindeki `listen` ve `pm.status_path`), her status sayfası kendi soketinde FastCGI üzerinden okunur, havuz başına bir resource; başkaları başarısız olsa da cevap veren havuzlar tutulur (`partial`), hiçbiri cevap vermezse neden (`pm.status_path` yok veya ajanın bağlanamadığı 0660 soket) `needs_configuration` olur ve çözüm hint'te yazar | yok | `phpfpm.*` |
| `rabbitmq` | rabbitmq | 15672 üzerinde management API (`/api/overview`, `/api/nodes`, `/api/queues`, en fazla 500 kuyruk) | broker isterse zorunlu (HTTP 401) | `rabbitmq.*` |
| `elasticsearch` | elasticsearch, opensearch | 9200 üzerinde REST API: `/_cluster/health` ve yerel node'un `/_nodes/_local/stats` çıktısı | security açıksa zorunlu (HTTP 401) | `elasticsearch.*`, `jvm.*` |
| `jvm` | jvm | herhangi bir JVM'in `java.lang` MBean'leri, Jolokia üzerinden tek bir toplu istekte okunur (heap, GC, thread'ler, sınıflar, CPU) | yalnızca Jolokia köprüsü isterse | `jvm.*` |
| `kafka` | kafka | aynı köprü üzerinden broker'ın kendi MBean'leri: throughput, partition'lar, controller, istek gecikmesi | yalnızca köprü isterse | `kafka.*` |
| `mongodb` | mongodb | `serverStatus`, `listDatabases`, `dbStats` (en fazla 32 veritabanı) ve replica set üyesinde `replSetGetStatus`; bağlantı doğrudandır, yani sayılar bu sürecin sayılarıdır | zorunlu (`clusterMonitor`) | `mongodb.*` |

Nasıl çalışır: discovery, kuralında entegrasyon id'si olan bir servis bulduğunda bir entegrasyon instance'ı başlar, servis kaybolduğunda durur.
Her instance kendi goroutine'inde çalışır (`integrations.interval`, varsayılan 30 sn; `integrations.timeout` 10 sn; aynı anda en fazla
`max_concurrent`; hatalardan sonra 5 dk'ya kadar üstel backoff; panic'ler sınırlandırılır). Endpoint'ler servisin dinlediği portlardan
(wildcard → loopback), bilinen unix soketlerden, yayımlanmış container portlarından ve container IP'lerinden (Docker API'den veya soket erişimi yoksa
container'ın `/proc/<pid>/net/{tcp,tcp6,fib_trie}` dosyalarından ve `docker-proxy` komut satırlarından) ve son çare olarak `127.0.0.1` üzerindeki
varsayılan porttan gelir; ilk cevap veren endpoint kullanılır. Her instance kendi OTLP resource'udur: makine resource'u + `openlog.discovery.id`,
`openlog.discovery.instance`, `openlog.integration.id`, `service.instance.id`, `server.address`, `server.port`. Kendi telemetrisi:
`openlog.agent.integration.{collections,errors,duration}{integration}`.

`discovered_service.integration` gerçek durumu raporlar: `enabled` (son toplama başarılı, `endpoint` dolu; kısmi toplamalar için `error`),
`needs_configuration` (kimlik bilgileri eksik veya parola yapılandırılmadan reddedildi, stub_status yok, sokete erişilemiyor; `hint` bir
`config.yaml` parçası içerir), `error` (temizlenmiş neden, örn. `authentication failed: Access denied for user 'openlog'@'10.0.0.5' (using password: YES)`),
`not_available` (gerçekleme yok, kapalı). Durum değişikliği yeni bir envanter snapshot'ı tetikler (ilk sonuçlardan sonra en fazla dakikada bir).
`-once` her entegrasyonu bir kez çalıştırır, yani `openlog-infra-agent -once | jq '.discovered_services[].integration'` servis kartının ne diyeceğini gösterir.

**Arayüzden yapılandır (SSH gerekmez):** bir makinenin entegrasyon panelinde `needs_configuration`/`error` durumları bir form (endpoint, kullanıcı adı,
parola, veritabanı, etkin) ve "bu makinede kapat" anahtarı gösterir. Ayarlar organizasyon başına saklanır (tüm makineler veya tek makine; parolalar
`OPENLOG_SECRETS_KEY` ile şifrelenir, bir daha gösterilmez) ve ajana bir sonraki sync ile ulaşır (varsayılan olarak ≤ 5 dk). Ajan bunları
`config.yaml` üzerine birleştirir (alan bazında uzaktaki kazanır), son alınan seti `/var/lib/openlog-infra-agent/integrations-remote.json` (0600)
içinde tutar ve yeniden başlatmadan uygular; `journalctl -u openlog-infra-agent | grep "remote integration config"` uygulanan revizyonu gösterir.
Entegrasyonları yalnızca bu dosyadan yönetmek için `integrations.remote_config: false` ayarla (arayüz o makine için uzaktan yapılandırmayı kapalı gösterir).

Kimlik bilgileri: `password: env:NAME` (örn. bir systemd `EnvironmentFile=` üzerinden), `password: file:/etc/openlog-infra-agent/mysql.password` (mod 0600,
sahibi ajan kullanıcısı) veya düz değer (uyarı yazılır). Her bağlantıda okunurlar, asla loglanmazlar, envanterin, durumların veya metriklerin parçası
olmazlar; parolalar ve DSN userinfo hata mesajlarında maskelenir.

```yaml
integrations:
  redis:
    password: env:OPENLOG_REDIS_PASSWORD          # yalnızca requirepass/ACL kullanılıyorsa
  mysql:
    username: openlog
    password: file:/etc/openlog-infra-agent/mysql.password
    instances:                                     # eşleşen servisler için geçersiz kılmalar (port, endpoint, unit, container, instance)
      - match: { container: legacy-mariadb }
        username: monitor
        password: env:OPENLOG_LEGACY_MYSQL_PASSWORD
  postgresql:
    username: openlog
    password: env:OPENLOG_POSTGRESQL_PASSWORD
    exclude_databases: [rdsadmin]
  nginx:
    instances:
      - match: { port: 8080 }
        endpoint: http://127.0.0.1:8080/nginx_status
  rabbitmq:
    username: openlog                              # rabbitmqctl set_user_tags openlog monitoring
    password: env:OPENLOG_RABBITMQ_PASSWORD
  elasticsearch:
    username: openlog                              # yerleşik monitoring_user rolü
    password: env:OPENLOG_ELASTICSEARCH_PASSWORD
    tls: { enabled: true, ca_file: /etc/elasticsearch/certs/http_ca.crt }
  jvm:
    # JVM'in başlatma komutunda Jolokia ajanı olmalı; openlog yalnızca okur:
    #   java -javaagent:/opt/jolokia/jolokia-agent-jvm.jar=port=8778,host=127.0.0.1 -jar app.jar
    # endpoint: http://127.0.0.1:8778/jolokia   # yalnızca alışılmış port veya yolda değilse
  kafka:
    # KAFKA_OPTS="-javaagent:/opt/jolokia/jolokia-agent-jvm.jar=port=8778,host=127.0.0.1"
    # endpoint: http://127.0.0.1:8778/jolokia
  mongodb:
    username: openlog                              # db.createUser(... roles: [{role: "clusterMonitor", db: "admin"}])
    password: env:OPENLOG_MONGODB_PASSWORD
    # database: admin                              # izlenecek veritabanı değil, kimlik doğrulama kaynağı
  haproxy:
    instances:
      - match: { unit: haproxy.service }
        endpoint: unix:/run/haproxy/admin.sock     # veya http://127.0.0.1:8404/;csv
```

En az yetkili izleme kullanıcıları:

```sql
-- MySQL 8 / MariaDB 10.5+ (MariaDB ≥ 10.5.9: replica durumunu okumak için REPLICATION CLIENT yerine REPLICA MONITOR kullan)
-- Docker container'ındaki bir sunucuya ajan Docker ağı üzerinden bağlanır: 'openlog'@'%' (veya bridge alt ağı) kullan.
CREATE USER 'openlog'@'localhost' IDENTIFIED BY '<password>' WITH MAX_USER_CONNECTIONS 3;
GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'openlog'@'localhost';
GRANT SELECT ON performance_schema.* TO 'openlog'@'localhost';
```

```sql
-- PostgreSQL 10+
CREATE ROLE openlog WITH LOGIN PASSWORD '<password>' CONNECTION LIMIT 3;
GRANT pg_monitor TO openlog;          -- replication lag dahil pg_stat_*, pg_database_size; veritabanlarında CONNECT varsayılan olarak PUBLIC'e verilidir
```

```
# nginx: stub_status (ajan bunu bulunan herhangi bir portta bulur; nginx yapılandırmasını asla değiştirmez)
location = /nginx_status {
    stub_status;
    allow 127.0.0.1;
    allow 172.16.0.0/12;   # Docker ağları: yayımlanan portlar bridge gateway'den gelir
    deny all;
}
```

```
# Redis 6+ ACL kullanıcısı (yalnızca requirepass/ACL ile)
ACL SETUSER openlog on >'<password>' +info +ping
```

```sql
-- SQL Server 2016+ (mixed-mode authentication). SQL Server 2022: VIEW SERVER STATE yerine VIEW SERVER PERFORMANCE STATE yeterlidir.
CREATE LOGIN openlog WITH PASSWORD = '<password>', CHECK_POLICY = ON;
GRANT VIEW SERVER STATE TO openlog;
GRANT VIEW ANY DEFINITION TO openlog;   -- veritabanı boyutları (sys.master_files)
```

Bağımlılıklar: `github.com/go-sql-driver/mysql` (MPL-2.0, kütüphane olarak değiştirilmeden kullanılır), `filippo.io/edwards25519` (BSD-3-Clause),
`github.com/jackc/pgx/v5`, `pgpassfile`, `pgservicefile` (MIT), `golang.org/x/text` (BSD-3-Clause), `github.com/microsoft/go-mssqldb`,
`github.com/golang-sql/civil`, `github.com/golang-sql/sqlexp` (BSD-3-Clause), `github.com/google/uuid`, `github.com/shopspring/decimal` (MIT),
`golang.org/x/crypto` (BSD-3-Clause), `github.com/yusufpapurcu/wmi`, `github.com/go-ole/go-ole` (MIT).

## Veritabanı sorgu performansı {#database-query-performance}

Veritabanı entegrasyonu başına açıldığında ajan **veritabanı sunucusunda neyin çalıştığını** da raporlar: statement
istatistikleri, oturumlar ve bekleyişleri, execution plan'lar (`docs/contracts/db-monitoring.md`). openlog'da
**Databases** altında görünür, bir statement onu gönderen APM servislerine bağlanır.

```yaml
# /etc/openlog-infra-agent/config.yaml
integrations:
  postgresql:
    username: openlog
    password: file:/etc/openlog-infra-agent/postgresql.password
    query_stats:
      enabled: true          # statement'lar, oturumlar ve plan'lar
      top_n: 20              # toplama başına gönderilen statement, aralıkta harcanan süreye göre
      sessions: true         # boşta olmayan oturumları her sample_interval'de örnekle
      sample_interval: 10s
      explain: true          # en üstteki statement'ların execution plan'larını yakala
      explain_interval: 1h
```

Aynı `query_stats` bloğu `mysql` ve `mssql` için de çalışır. Her sunucunun ihtiyacı:

| | Statement'lar | Oturumlar ve bloklama | Plan'lar |
|---|---|---|---|
| PostgreSQL | `pg_stat_statements` (`shared_preload_libraries`, `CREATE EXTENSION`), `pg_monitor` yetkili rol | `pg_stat_activity`, `pg_blocking_pids()` | `GRANT pg_read_all_data` (yalnızca okuma statement'ları; PostgreSQL 16+ parametreli olanları da planlar) |
| MySQL / MariaDB | statement digest'leriyle `performance_schema` açık | `performance_schema.threads`, `data_lock_waits` (MySQL 8) | şemalarda `SELECT`; yalnızca okuma statement'ları |
| SQL Server | `VIEW SERVER STATE` | `sys.dm_exec_requests` | plan cache'ten, ek izin gerekmez |

Statement metni makineden asla değerleriyle çıkmaz: literal'ler göndermeden önce `?` ile değiştirilir, execution plan'lar da
temizlenir (MySQL koşulları, SQL Server parametre değerleri). Ajanın kendi izleme statement'ları hariç tutulur. Oturumlar varsayılan
olarak 10 sn'de bir örneklenir; bunu kapatmak için `sessions: false`, plan'lar için `explain: false` ayarla.

## Prometheus ve OpenMetrics endpoint'leri {#prometheus-and-openmetrics-endpoints}

Ajan, Prometheus metin biçimini veya OpenMetrics'i konuşan her endpoint'i scrape eder (node_exporter, veritabanı ve broker exporter'ları,
uygulamanın kendi `/metrics`'i) ve örnekleri OTLP metrik olarak gönderir, böylece diğer tüm metrikler gibi Metrics Explorer, OQL, dashboard'lar ve
alarmlarda görünürler (semantic-conventions §6.9). Hedef eklemenin üç yolu var:

```yaml
# config.yaml: statik hedefler
prometheus:
  targets:
    - url: http://127.0.0.1:9100/metrics
      job: node
```

```yaml
# docker-compose.yml: container label'larla katılır (ajanın Docker erişimi gerekir, yukarıya bak)
services:
  api:
    labels:
      prometheus.io/scrape: "true"
      prometheus.io/port: "9090"        # container tam olarak bir port tanımlıyorsa isteğe bağlı
      prometheus.io/path: /metrics      # varsayılan
```

```yaml
# Kubernetes (node modu): alışılmış pod annotation'ları
metadata:
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "8080"
```

Her hedef ayrıca `up`, `scrape_duration_seconds` ve `scrape_samples_scraped` alır, böylece cevap vermeyi bırakan bir exporter için alarm kurabilirsin
(`SELECT latest(up) FROM Metric FACET service.name`). `sample_limit`'ten fazla örnek veya `body_limit_bytes`'tan büyük gövde sunan bir hedef bütünüyle
reddedilir ve `up = 0` raporlar; ihtiyacın olmayan aileleri atmak için `metrics.exclude` kullan (`go_*`, `process_*`). Counter'lar Prometheus adlarını
korur (`http_requests_total`), histogram ve summary'ler OTLP histogram ve summary olur, OpenMetrics exemplar'ları bir grafiği trace'lerine bağlar.

## Loglar {#logs}

Loglar düz OTLP LogRecord olarak gönderilir (asla envanter event'i olarak değil). Alanlar ve attribute'lar için bkz. `semantic-conventions.md` §4.

**Dosyalar** (`logs.files`): mutlak glob pattern'leri (`filepath.Match` sözdizimi; `**` yok), isteğe bağlı `exclude`, `multiline_start` ve `attributes`.
- Dosyalar device + inode ve ilk 1 KiB'lerinin parmak izi ile tanınır (inode yeniden kullanımına karşı). Glob'lar 10 sn'de bir yeniden açılır.
- Rename + create ile rotasyon: eski dosya, rename sonrası 5 sn boşta kalana kadar okunmaya devam eder, yeni dosya baştan okunur.
  copytruncate: okuma offset'inin altına küçülen bir dosya 0'dan yeniden okunur (kopya ile bir sonraki yoklamamız arasında yazılan satırlar kaybolabilir;
  tek bir yoklama içinde kırpma ve ardından eski offset'in ötesine yazma algılanamaz).
- Offset'ler `<state_dir>/logs-state.json` içinde saklanır (geçici dosya + fsync + rename, 5 sn'de bir ve kapanışta) ve yalnızca exporter batch'i
  sonuçlandırdıktan sonra (gönderildi, disk tamponuna yazıldı veya atıldı) ilerler, yani teslimat at-least-once'tır. Ajan kapalıyken rotasyona uğramış
  bir dosya (bilinen yol, yeni inode) baştan okunur. İlk başlangıçta var olan dosyalar `logs.start_at`'e uyar (varsayılan `end`); sonradan çıkan dosyalar baştan okunur.
- Satırlar: sondaki `\r` silinir, geçersiz UTF-8 değiştirilir, `max_line_bytes`'ta kesilir (satırın geri kalanı atlanır, `openlog.log.truncated=true`).
  Yeni satırı olmayan bir satır 5 sn boşta kaldıktan sonra, bekleyen çok satırlı bir kayıt 2 sn boşta kaldıktan sonra yayımlanır.
- `rate_limit_lines` (dosya başına, saniye başına) ve gönderim birikimi (disk tamponu boş değil veya bellek kuyruğu yarı dolu) okumayı duraklatır: veri
  dosyada kalır, hiçbir şey atılmaz, ama duraklamadayken rotasyona uğrayıp silinen bir dosya kaybolur. En fazla 512 dosya takip edilir.

**journald** (`logs.journald`): `journalctl --output=export --follow` çalıştırır (`host.root_path` altında `--root`, `--unit`, `--priority` ile) ve
export biçimini ayrıştırır (metin ve binary alanlar). Son sonuçlanan kaydın cursor'ı saklanır ve yeniden başlatmada `--after-cursor` olarak verilir; alt süreç
backoff ile yeniden başlatılır. `PRIORITY` → severity, `_SYSTEMD_UNIT`, `_PID`, `_COMM`, `SYSLOG_IDENTIFIER` → attribute'lar. `journalctl` ajanın
erişebileceği yerde olmalı (scratch container imajında yok) ve ajan kullanıcısının journal erişimi gerekir (`systemd-journal`/`adm` grubu veya `CAP_DAC_READ_SEARCH`).

**Discovery odaklı**: kurallar `logs: [{path: "/var/log/nginx/*.log"}]` tanımlayabilir (nginx, Apache, MySQL, MariaDB, PostgreSQL ve Redis tanımlar). Bulunan
servisler bunları `log_paths` olarak raporlar; `logs.auto_from_discovery: true` ile `openlog.discovery.id=<rule_id>` ile takip edilirler. Yolu bulunan bir
glob ile eşleşen yapılandırılmış bir dosya da discovery id'sini alır.

**Container'lar** (`logs.containers`, `containers.enabled` ile varsayılan olarak açık): Docker ve containerd/CRI-O container'larının stdout/stderr çıktısı,
container başına bir OTLP resource (makine attribute'ları + `container.id`, `container.name`, imaj, `docker.compose.project/service`), kayıtlar
`openlog.log.source=container`, `log.iostream=stdout|stderr`, Docker'ın zaman damgası ve `trace_id`/`traceId` alanı olan JSON gövdeler için trace ve span id
taşır (böylece trace sayfası onları bulur). Bkz. `semantic-conventions.md` §4.1.
- `source: auto` (varsayılan), ajan açabiliyorsa json-file logunu okur (`/var/lib/docker/containers/<id>/<id>-json.log`, `root:root 0640`; systemd unit'i
  `CAP_DAC_READ_SEARCH` verir, bu yüzden paket kurulumları dosyaları okur), dosya offset'leri, rotasyon (`-json.log.1`) ve 16 KiB parçalı mesaj birleştirmeyle;
  aksi halde Docker soketi üzerinden (docker grubu) `GET /containers/{id}/logs?follow=1&timestamps=1` akışını okur, bu da `local` ve `journald` log sürücülerini
  kapsar ve son teslim edilen kayıttan sonra devam eder. `source: file` / `api` tek bir yolu zorlar.
- Çalışan container'lar okunur (önce çalışanlar, en fazla `max_containers`, varsayılan 100); durmuş container'lar logları sonuna kadar okunana dek.
  `rate_limit_lines` container başınadır (varsayılan 1000/sn, backpressure). İlk başlangıçta çalışan container'lar `logs.start_at`'e uyar.
- `include` / `exclude` ile seç (`name`, `image`, `compose_project`, `compose_service`, `label` glob'ları) veya container'a `openlog.logs=false` label'ı ver.
- Okunamayan sürücüler (`none`, dual logging'siz uzak sürücüler) backoff ile yeniden denenir ve bir kez loglanır.
- containerd/CRI-O container'ları: CRI log dosyası (`/var/log/pods/…/<n>.log`, CRI biçimi, `P` parçalı satırlar birleştirilir) json-file logu gibi takip edilir;
  onlar için API akışı yoktur.
- Pattern'siz stack trace'ler: `logs.join_continuations: true`, girintili / `at …` / `Caused by:` / `... N more` satırlarını önceki kayda ekler. Varsayılan
  olarak kapalı: her kaydı bir sonraki satıra kadar geciktirir (~1 sn'ye kadar) ve stdout ile stderr'in sırasını değiştirebilir.
- Çok satırlı: container'a `openlog.logs.multiline='^\d{4}-\d{2}-\d{2}'` label'ı ver veya bir `include` öğesinde `multiline_start` ayarla (label kazanır;
  include listesinin yine her container'ı seçmesi için `- name: "*"` ekle). Satırlar `logs.files` gibi akış başına (stdout/stderr) gruplanır:
  bir sonraki başlangıç satırında veya 2 sn boşta kalınca gönderilir, `max_line_bytes`'ta kesilir.

**Maskeleme**: log gövdeleri kullanıcı verisidir ve `logs.mask_secrets: true` olmadıkça maskelenmez (komut satırlarıyla aynı pattern'ler, MySQL `-p` kuralı hariç).

## PHP forwarder {#php-forwarder}

openlog PHP eklentisi (`openlog.so`, `agents/php`) asla ağla konuşmaz: istek başına bir JSON datagram'ı `/run/openlog-infra-agent/php.sock` soketine yazar,
ajanın `php_forwarder` modülü de span'leri OTLP'ye çevirip metrik ve loglarla aynı gönderim kuyruğu, disk tamponu ve retry'lar üzerinden gönderir
(`/v1/traces`). Backend kesintisi PHP isteklerini asla etkilemez; span'ler tamponlanır ve sonra teslim edilir. Kontrat: `openlog/docs/contracts/php-agent.md` §1, §2, §6.

- **Etkinleştirme**: discovery PHP bulduğu sürece otomatik (bir `php-fpm` servisi, mod_php'li Apache veya `/usr/bin`, `/usr/local/bin`, `/bin` içinde bir
  `php`/`phpX.Y` binary'si), her envanter snapshot'ında yeniden değerlendirilir; `php_forwarder.enabled: true|false` önceliklidir.
  Otomatik etkinleştirme `inventory.enabled` ve `discovery.enabled` gerektirir.
- **Soket**: dizin `0755` (systemd `RuntimeDirectory=openlog-infra-agent`), soket `0660`, grubu `socket_group`
  (otomatik: **`openlog-php`**; yalnızca o grup yoksa `www-data`, `nginx`, `apache`, `php-fpm` içinden var olan ilki).
  Önceki çalıştırmadan kalan soket değiştirilir; eklenti bağlantısız datagram gönderir, bu yüzden yeniden başlatmalar ve kendini güncellemeler PHP reload
  gerektirmez. `socket_mode: "0666"` her yerel kullanıcının göndermesine izin verir.
- **PHP soket erişimi (`openlog-php`)**: her servis başlangıcında (unit'in root pre-start adımı), paket kurulum/yükseltmesinde ve `install.sh` çalıştırmasında
  `-reconcile`, `openlog-php` sistem grubunu oluşturur, `openlog-agent`'ı ekler (soketin grubunu ayarlayabilmesi için) ve bulduğu her PHP-FPM havuz
  kullanıcısını ekler (`/etc/php/*/fpm/pool.d/*.conf`, `/etc/php-fpm.d/*.conf`, Remi, cPanel `ea-php*`, Plesk içindeki `user =`; HestiaCP, cPanel, Plesk,
  ISPConfig'in site başına kullanıcıları), Apache veya nginx kuruluysa `www-data`/`apache`/`nginx` da. Root ve yerel olmayan hesaplar atlanır; üyeler asla
  çıkarılmaz. Yalnızca kullanıcıları eklenen PHP-FPM servisleri (ve Apache) nazikçe reload edilir (`systemctl reload php8.2-fpm`, …; asla yeniden
  başlatılmaz), böylece yeni worker'lar grubu alır. Kurulum araçları neyin değiştiğini yazdırır. Bilerek ayrı bir gruptur: `openlog-agent` grubu, lisans
  anahtarını içeren `config.yaml`'ı okuyabilir ve asla PHP kullanıcılarına verilmemelidir.
- **Sonradan eklenen siteler**: ajan, kullanıcısı gönderemeyen havuzları raporlar (log uyarısı, Fleet sync `php_access`, makine → Services PHP-FPM kartında
  çözümle birlikte). `sudo systemctl restart openlog-infra-agent` ile uygula veya elle:
  `sudo usermod -aG openlog-php <pool user> && sudo systemctl reload php<version>-fpm`.
- **Otomatik yetkilendirmeden çıkmak** için (grup ve ajan üyeliği yine kurulur): `php_forwarder.grant_pool_users: false`,
  `sudo touch /etc/openlog-infra-agent/no-php-access`, `install.sh --no-php-access` veya `sudo env OPENLOG_AGENT_PHP_ACCESS=0 apt-get
  install …` (ikisi de dosyayı oluşturur). Bir yetkiyi geri almak için: `sudo gpasswd -d <user> openlog-php`.
- **Grup sorunları** bir kez loglanır: `openlog-php` (veya açıkça verilen `socket_group`) üyeliği eksikse uyarı olarak,
  `openlog.agent.permission_denied{collector="php_forwarder"}` içinde sayılır; yedek grup kullanılıyorsa (`openlog-php` yok, örneğin container'da veya dev
  derlemesinde) info seviyesinde.
- **Soket dosyasını paylaşamayan container'lar**: `udp_listen: 127.0.0.1:18127` ve `openlog.transport=udp://127.0.0.1:18127`.
- **Girdi** sıkı doğrulanır (60 000 baytlık datagram'lar, küçük harfli hex ID'ler, ≤ 128 attribute, ≤ 4 KiB string) ve bozuk mesajlar atılır. Bölünmüş
  mesajlar (pid, trace_id) başına yeniden birleştirilir; `reassembly_timeout` sonrasında `openlog.php.incomplete=true` ile gönderilir. Eklenti `host.*`, `os.*` veya
  `openlog.*` resource attribute'larını ayarlayamaz: forwarder ajanınkileri ekler, böylece PHP servisleri bu makineye bağlanır (`host.id`).
- **Durum**: bulunan `php-fpm` servisleri `apm_hint.status` olarak `active` (son 10 dakikada span alındı) veya `not_installed` raporlar. Kendi telemetrisi:
  `openlog.agent.php.messages{result}`, `.spans`, `.reassembly_timeouts`, `.pending_traces`.

## Çalıştırma {#running}

Modül aynı repodaki `libs/release`'e bağlıdır (`replace … => ../../libs/release`), bu yüzden tam bir checkout'tan derle.

```sh
make build-linux             # dist/openlog-infra-agent-linux-{amd64,arm64}
make test vet
make docker                  # build context repo köküdür

# Sürüm, commit, derleme tarihi, kurulum yöntemi ve güncelleme yeteneği.
openlog-infra-agent -version

# Debug: bir tur topla ve OTLP payload'ını JSON olarak yazdır; hiçbir şey gönderilmez.
sudo openlog-infra-agent -once | jq '.discovered_services'

# Gömülü kuralları ve rules_dir'i doğrula.
openlog-infra-agent -validate-rules -config /etc/openlog-infra-agent/config.yaml

# Yapılandırmayı ayrıştır, her toplayıcıyı göndermeden bir kez çalıştır, state_dir yazılabilir mi kontrol et (exit 0/1).
openlog-infra-agent -self-test -config /etc/openlog-infra-agent/config.yaml

# Servis olarak çalıştır (tarball yerleşimi; deb/rpm paketleri aynı yerleşimi kurar). Kurulum kökünün sahibi root'tur.
V=0.4.0
sudo install -d -m 0755 /opt/openlog/infra-agent /opt/openlog/infra-agent/versions
sudo install -D -m 0755 dist/openlog-infra-agent-linux-amd64 /opt/openlog/infra-agent/versions/$V/openlog-infra-agent
sudo install -m 0644 manifest.json manifest.json.sig /opt/openlog/infra-agent/versions/$V/   # sürümden
sudo ln -sfn versions/$V /opt/openlog/infra-agent/current
sudo install -D -m 0640 packaging/config.example.yaml /etc/openlog-infra-agent/config.yaml
# openlog-agent'ı oluşturur, systemd unit'ini kurar, sahipliği ayarlar, docker grubu (bkz. "Docker erişimi").
sudo /opt/openlog/infra-agent/current/openlog-infra-agent -reconcile -config /etc/openlog-infra-agent/config.yaml
sudo systemctl daemon-reload && sudo systemctl enable --now openlog-infra-agent
```

systemd unit'i `openlog-agent` olarak, diğer kullanıcıların `/proc/<pid>/exe` ve fd linklerini okumak için gereken `CAP_DAC_READ_SEARCH` ve `CAP_SYS_PTRACE`
yetkileriyle çalışır. Okuyamadığı her şey atlanır ve `openlog.agent.permission_denied` içinde sayılır.
`Restart=always` kullanır (ajan bir güncellemeye geçmek için status 0 ile çıkar). Ajan yalnızca `/var/lib/openlog-infra-agent` dizinine yazabilir; güncellemeleri
unit'in ayrıcalıklı pre-start adımı (`ExecStartPre=-+… -apply`, root) kurar.

## macOS ve Windows {#macos-and-windows}

D-104'ten beri ajan macOS (amd64, arm64) ve Windows (amd64, arm64) üzerinde de çalışır. Aynı binary yerleşimi, yapılandırma şeması, metrik adları ve envanter
gövdeleri kullanılır; toplayıcılar procfs yerine yerel API'leri okur. Kontratlar: `semantic-conventions.md` (§1–§4'te platform notları) ve
`releases-updates.md` §3 "macOS and Windows services".

| Özellik | Linux | macOS | Windows |
|---|---|---|---|
| Makine metrikleri (CPU, bellek, paging, dosya sistemi, disk, ağ, uptime, süreç sayıları) | ✅ procfs/sysfs | ✅ gopsutil | ✅ gopsutil (load average yok) |
| Süreç metrikleri (top N) | ✅ | ✅ | ✅ (`open_file_descriptors` = handle'lar) |
| Envanter: OS, donanım, paketler, süreçler, kullanıcılar, arayüzler, mount'lar, dinlenen portlar | ✅ | ✅ `sw_vers`/sysctl, pkgutil receipts + Homebrew + `/Applications`, `dscacheutil`, `lsof` | ✅ registry, Programs and Features, ProfileList, GetExtendedTcpTable |
| Servis yöneticisi envanteri | `systemd_unit` | `launchd_service` | `windows_service` |
| Kernel modülleri | ✅ | yok | yok |
| Discovery + entegrasyonlar (nginx, Redis, MySQL/MariaDB, PostgreSQL, SQL Server) | ✅ | ✅ (Homebrew servisleri ve log yolları) | ✅ (süreç/servis adları; ayrıca performans sayaçlarıyla IIS) |
| Container'lar | ✅ Docker/CRI envanteri + cgroup metrikleri + loglar | yalnızca `docker.sock` üzerinden Docker envanteri | yok |
| Loglar: dosyalar | ✅ | ✅ | ✅ (`FILE_SHARE_DELETE` ile açılır) |
| Loglar: sistem | journald | unified log (`logs.unified_log`) | Event Log (`logs.windows_event_log`) |
| PHP forwarder / PHP ajanı kurulumu | ✅ | forwarder ✅ (`/var/run/openlog-infra-agent/php.sock`, grup `_www`), kurulum yok | yok (kapalı) |
| Kubernetes | ✅ | yok | yok |
| Servis | systemd, `openlog-agent` kullanıcısı | LaunchDaemon, root | Windows servisi, LocalSystem |
| Kendini güncelleme (imzalı manifest'ler, self-test, rollback) | ✅ `ExecStartPre=+ -apply` | ✅ servis sürecinde | ✅ servis sürecinde |
| Paketler | tar.gz, deb, rpm | tar.gz (`install.sh`), pkg | zip (`install.ps1`), MSI (amd64, arm64) |

### İzinler {#permissions}

- **macOS root olarak çalışır.** launchd'de ayrıcalıklı pre-start kancası yoktur: root süreci hazırlanan güncellemeleri kendisi doğrular ve kurar; her sürecin
  executable/argümanlarını, dinlenen port sahiplerini (`lsof`) ve sistem launchd domain'ini okumak da zaten root gerektirir. Yapılandırma root 0600, durum dizini
  root 0700'dür. Özel bir `_openlog` hesabı ikinci bir root yardımcı daemon'u gerektirirdi; gerçeklenmedi.
- **Windows LocalSystem olarak çalışır.** Security event log, tam süreç ve port bilgisi, SCM ve `C:\Program Files` altına güncelleme kurmak için gerekir.
  `LocalService` desteklenmez (güncellemeler ve envanterin bir kısmı başarısız olur). Servis SID türü `unrestricted`'tır;
  `C:\ProgramData\openlog\infra-agent` (lisans anahtarlı yapılandırma, durum, loglar) `-configure`/`-reconcile`, kurulum araçları ve MSI tarafından SYSTEM ve
  Administrators ile sınırlandırılır.

### macOS'a kurulum {#install-on-macos}

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
  sudo sh -s -- --license-key KEY --endpoint https://ingest.example.com:4318

sudo launchctl print system/org.openlog.infra-agent     # durum
tail -f /var/log/openlog-infra-agent.log                 # JSON log (newsyslog ile döndürülür)
sudo openlog-infra-agent -once | jq '.discovered_services'
```

Veya kurulum paketiyle (kimlik bilgileri, postinstall script'inin okuyup sildiği root'a ait bir dosyada; bkz.
`docs/operations/releasing.md` "MSI and macOS pkg"):

```sh
sudo sh -c 'umask 077; printf "OPENLOG_LICENSE_KEY=KEY\nOPENLOG_ENDPOINT=https://ingest.example.com:4318\n" > /tmp/openlog-infra-agent.env'
sudo installer -pkg openlog-infra-agent_<v>_darwin_arm64.pkg -target /
```

Yollar: `/opt/openlog/infra-agent/{versions/<v>,current}`, `/etc/openlog-infra-agent/config.yaml`, `/var/lib/openlog-infra-agent`,
`/Library/LaunchDaemons/org.openlog.infra-agent.plist`, `/usr/local/bin/openlog-infra-agent`.
`curl` ile indirilenler quarantine attribute'u taşımaz, bu yüzden imzasız binary çalışır; CI'da imzalanan ve notarize edilen sürümler için
`.github/workflows/release.yml` içinde listelenen Apple secret'ları gerekir.

Kaldırma:

```sh
sudo /opt/openlog/infra-agent/current/openlog-infra-agent -uninstall-service
sudo rm -rf /opt/openlog/infra-agent /usr/local/bin/openlog-infra-agent /etc/newsyslog.d/openlog-infra-agent.conf
sudo rm -rf /etc/openlog-infra-agent /var/lib/openlog-infra-agent /var/log/openlog-infra-agent.log*   # yapılandırma ve durum
sudo pkgutil --forget org.openlog.infra-agent                                                       # pkg kurulumları
```

### Windows'a kurulum {#install-on-windows}

Administrator olarak PowerShell:

```powershell
& ([scriptblock]::Create((Invoke-RestMethod https://github.com/onuragtas/openlog/releases/latest/download/install.ps1))) `
  -LicenseKey KEY -Endpoint https://ingest.example.com:4318

# veya MSI (amd64; ARM üzerinde Windows için arm64)
msiexec /i openlog-infra-agent_<v>_windows_amd64.msi LICENSE_KEY="KEY" ENDPOINT="https://ingest.example.com:4318" /qn

Get-Service openlog-infra-agent
Get-Content C:\ProgramData\openlog\infra-agent\logs\openlog-infra-agent.log -Tail 20 -Wait
& 'C:\Program Files\openlog\infra-agent\current\openlog-infra-agent.exe' -once
```

Yollar: `C:\Program Files\openlog\infra-agent\{versions\<v>,current}`, `C:\ProgramData\openlog\infra-agent\{config.yaml,state\,logs\,discovery.d\}`.
Kaldırma: `install.ps1 -Uninstall` (yapılandırma ve durumu da silmek için `-Purge` ekle) veya MSI kurulumları için `msiexec /x` / Apps & features.
Yayın workflow'unda Authenticode secret'ları yoksa MSI imzasızdır; SmartScreen uyarabilir.

### Yapılandırma farkları {#configuration-differences}

Varsayılanlar işletim sistemine uyar (`config.DefaultFor`): Windows `containers`, `logs.containers` ve `php_forwarder`'ı kapatır ve
`logs.windows_event_log.channels` değerini önceden doldurur (System ve Application, critical/error/warning; `enabled: false`); macOS CRI soketi kullanmaz ve
`php.sock` dosyasını `/var/run` altına koyar. `-configure` macOS ve Windows'ta en küçük dosyayı yazar (yalnızca `license_key` ve `endpoint`), böylece işletim
sistemi varsayılanları geçerli olur; `packaging/config.example.yaml` her anahtarı Linux yollarıyla belgeler. Yapılandırmadaki yollar POSIX veya Windows yolu
olabilir (`C:\logs\*.log`). Bir işletim sisteminde olmayan girdiler (Linux dışında `logs.journald`, macOS dışında `logs.unified_log`, Windows dışında
`logs.windows_event_log`) yapılandırma uyarısı olarak raporlanır.

```yaml
logs:
  unified_log:                 # macOS
    enabled: true
    level: default             # default | info | debug
    predicate: 'subsystem == "com.example.app" OR process == "nginx"'
  windows_event_log:           # Windows
    enabled: true
    channels:
      - { name: System, levels: [critical, error, warning] }
      - { name: Application, levels: [critical, error, warning] }
      - { name: Security, event_ids: [4624, 4625, 4740] }
      - { name: Microsoft-Windows-PowerShell/Operational, query: "*[System[Level<=3]]" }
```

### macOS ve Windows'ta kendini güncelleme {#self-update-on-macos-and-windows}

Servis süreci, toplamaya başlamadan önce ayrıcalıklı apply adımını kendisi çalıştırır (bkz. `releases-updates.md` §3): hazırlanan sürümün imzasını derlemeye gömülü
anahtarlarıyla yeniden doğrular, arşivi kopyalar ve kontrol eder (macOS'ta `tar.gz`, Windows'ta `zip`), adayı self-test'ten geçirir, `current`'ı değiştirir ve bir kez
çıkar, böylece launchd (`KeepAlive`) veya SCM (recovery actions, exit code 3) yeni binary'yi başlatır. Onaylanmamış bir aday, Linux'taki gibi 3 başlangıç veya 5 dakika
sonra geri alınır. MSI ve pkg, binary'nin yanına kendi sürümlerinin imzalı manifest'ini kurar (D-113), böylece openlog'dan istenen bir rollback paketle kurulmuş bir
sürümden de çalışır. CI bütün döngüyü iki işletim sisteminde de çalıştırır (`test/update/native.sh`, `test/update/native.ps1`: eski sürümü kur, sahte backend üzerinden
yükselt, geri al).


## Kubernetes {#kubernetes}

`deploy/helm/openlog-agent` chart'ı ile kur ([docs/operations/kubernetes.md](/tr/operations/kubernetes)). Pod içinde
(`KUBERNETES_SERVICE_HOST`, `kubernetes.enabled: auto`) ajan iki moddan birinde çalışır (`internal/k8s`, semantic-conventions §7):

- **node** (DaemonSet): makine ajanı ve node'un pod'larını izleme; container metriklerine ve container loglarına (`/var/log/pods`) `k8s.pod.uid`, sahip workload,
  node, cluster ve izin listesindeki pod label'larını ekler, ayrıca kubelet `/stats/summary` metrikleri (`k8s.node.*`, `k8s.pod.*`, `k8s.container.*`).
- **cluster** (`kubernetes.mode: cluster`, Deployment): makine toplama, güncelleme veya ajan sync'i yok; `coordination.k8s.io` Lease sahibi 30 sn'de bir node, pod,
  workload, job, HPA, namespace ve quota listeler (kube-state metrikleri) ve Kubernetes event'lerini log olarak gönderir. `-once` tek bir listeleme yazdırır.

`client-go` yok: ServiceAccount token'ı ile küçük, tipli bir REST istemcisi (list, watch, Lease). OTLP yakalama sunucusuyla kind üzerinde canlı test:
`agents/infra/test/k8s/run.sh`.

## Kendini güncelleme {#self-update}

Kontrat: `openlog/docs/contracts/releases-updates.md` §3. Kendini güncelleme, yeni paketi kurmaya veya `install.sh`'ı yeniden çalıştırmaya denktir: binary'nin yanı sıra
yeni sürümün systemd unit'i, hesabı, docker grubu üyeliği ve dosya sahipliği de uygulanır.

> **0.1.x'ten yükseltme (makine başına bir kez):** ayrıcalıklı pre-start adımından önce kurulan unit'ler ajanın yalnızca binary'sini değiştirmesine izin verir.
> Paket yükseltmesini çalıştır (`apt install --only-upgrade openlog-infra-agent`, `dnf upgrade
> openlog-infra-agent`) veya `install.sh`'ı bir kez yeniden çalıştır. O zamana kadar ajan yalnızca binary güncellemeye devam eder (`versions/`,
> `openlog-agent` tarafından yazılabilir olduğu sürece) ve `update_notice: "unit outdated: …"` raporlar.

Yerleşim:

```
/opt/openlog/infra-agent/                        # root:root 0755; ajan buraya yazamaz
  versions/0.3.0/{openlog-infra-agent, manifest.json, manifest.json.sig, LICENSE, README.md, packaging/}
  versions/0.4.0/…
  current -> versions/0.4.0
  apply-status.json                              # -apply (root) yazar, ajan okur
  reconcile-status.json                          # -reconcile (root) yazar, sync'te "reconcile" olarak raporlanır
/var/lib/openlog-infra-agent/update-state.json   # previous, candidate, attempts, staged_at, confirmed + raporlanan durum
/var/lib/openlog-infra-agent/updates/<v>/        # hazırlanan sürüm: archive.tar.gz, manifest.json, manifest.json.sig
```

- **Staged mod** (`ExecStartPre=-+/opt/openlog/infra-agent/current/openlog-infra-agent -apply` içeren unit): ajan (ayrıcalıksız, sandbox'lı) sürümü doğrular, indirir,
  açar ve self-test'ten geçirir, `updates/<v>/` içinde yalnızca arşivi, manifest'i ve imzayı tutar, durumuna kaydeder ve 0 ile çıkar. Yeniden başlatmada `-apply`, sahibi
  root olan güncel binary'den root olarak çalışır ve durum dizinindeki her şeyi düşmanca kabul eder: imzayı kendisine gömülü anahtarlarla yeniden doğrular (kural 1–5),
  arşivi boyut ve sha256 kontrol ederek kurulum köküne kopyalar (durum dizininden dışarı symlink kaçışı yok, FIFO yok), sahibi root olacak şekilde açar (kural 6), adayın
  `-self-test`'ini `openlog-agent` olarak çalıştırır (kural 7), `current`'ı değiştirir ve yeni binary'nin `-reconcile`'ını çalıştırır.
  Yapılandırma ve `release.trusted_keys_file`, yalnızca root değiştirebiliyor değilse `-apply` tarafından yok sayılır.
- **Reconcile** (`-reconcile`, deb/rpm postinstall ve `install.sh` de çalıştırır): eksikse hesabı oluşturur; kurulum kökünün sahibini root yapar (eski bir ajanın yazdığı
  eski ağaçlar: güncel olan sahibi root olan bir dizine kopyalanır, diğerleri silinir); `/var/lib/openlog-infra-agent` `openlog-agent` 0750; `config.yaml`
  `root:openlog-agent` 0640 (asla yeniden yazılmaz); binary'ye gömülü unit'i kurar (deb/rpm: `/usr/lib/systemd/system`, `/etc/systemd/system` içinde tam bir override
  varsa kurmaz; tarball: `/etc/systemd/system`; drop-in'lere asla dokunulmaz) ve `systemctl daemon-reload` çalıştırır; docker grubu [Docker erişimi](#docker-access)
  bölümündeki gibi. `-apply` sırasında unit değiştiyse ajan başladıktan hemen sonra bir kez çıkar, böylece systemd onu yeni unit altında başlatır (unit içeriği başına yalnızca bir kez).
- **Legacy mod** (pre-start adımı olmayan unit): aşağıdaki eski yalnızca binary geçişi, yalnızca `versions/` ajan tarafından yazılabilir olduğu sürece; `unit outdated` bildirimiyle
  `update_mode: "legacy"` olarak raporlanır.

- **Sync:** ayrı bir goroutine sürüm, commit, kurulum yöntemi, `update_capable`, son güncelleme durumu ve bir config hash'i ile `POST <endpoint>/v1/openlog/agent/sync` gönderir
  (aynı lisans anahtarı header'ı). İlk sync başlangıçtan sonra 10 sn içinde çalışır, sonra sunucudan gelen her `poll_interval_seconds`'ta, [60 sn, 3600 sn] aralığına sıkıştırılarak ve
  ±%10 jitter ile; durum değişiklikleri anında sync tetikler. `404`/`501` yeniden başlatmaya kadar sync'i kapatır.
  Sync ve indirmeler toplamayı veya gönderimi asla bloklamaz.
- **Kurulum yöntemi:** `container` (`/.dockerenv`, `/run/.containerenv`, container cgroup'u veya `OPENLOG_AGENT_CONTAINER=1`; `=0` algılamayı kapatır), `dev` (çalışan binary
  `install_root/versions/<v>/openlog-infra-agent` değil), `deb` (dpkg'nin `openlog-infra-agent.list` dosyası `install_root`'a sahip), `rpm` (rpmdb.sqlite içinde `openlog-infra-agent`
  paketi), aksi halde `tarball`. `update_capable` için tarball/deb/rpm, `update.enabled`, güvenilen sürüm anahtarları, bir `current` symlink'i ve ya `-apply`'ın bu başlangıç için
  çalışmış olması (aynı systemd `$INVOCATION_ID`: staged mod) ya da yazılabilir bir kurulum kökü (legacy mod) gerekir. `-version` paket sürümü dahil bunların hepsini yazdırır.
- **Doğrulama** (kontrat sırasıyla; kural 8, kural 1–5'ten hemen sonra, herhangi bir şey indirilmeden önce kontrol edilir):
  1. güvenilen bir anahtardan manifest imzası (derlemeye gömülü anahtarlar + `release.trusted_keys_file`; yoksa → `no trusted release keys`);
  2. `product`, `schema`, `version == target_version`;
  3. bir `infra-agent` `linux/<arch>` `tar.gz` artifact'ı;
  4. yükseltme: çalışandan yeni ve çalışan ≥ `min_upgrade_from`;
  5. rollback: çalışandan eski ve çalışan sürümün saklanan `manifest.json` dosyasındaki `rollback_floor`'dan ≥ (yoksa → reddedilir);
  6. `versions/.<v>.partial` konumuna indirme (≤ 256 MiB, 10 dk zaman aşımı, boyut + akış halinde sha256 kontrolü), mutlak yolları, `..`, symlink'leri, hard link'leri, device
     dosyalarını, `openlog-infra-agent_<v>_linux_<arch>/` dışındaki girdileri ve toplamda 512 MiB'den fazlasını reddeden açma;
  7. `<new binary> -self-test -config <config>` 30 sn içinde 0 ile çıkar;
  8. güncelleme yapabilir değilse → `not update capable`.
  Ingest makinesinden (backend mirror) indirme lisans anahtarını taşır; diğer makineler onu asla görmez.
- **Hazırlama/geçiş (legacy mod):** doğrulanan ağaç ve `manifest.json`/`.sig`, `versions/<v>/` olur, `{previous, candidate, attempts: 0, staged_at}` durumu yazılır, `current`
  atomik olarak değiştirilir (geçici symlink + rename) ve ajan kuyruğunu boşalttıktan sonra status 0 ile çıkar.
  Staged modda bunu bir sonraki başlangıçta `-apply` root olarak yapar (yukarıya bak).
- **Başlangıç kontrolü:** staged mod: `-apply`, onaylanmamış bir adayın başlangıçlarını kendi root sahipli durumunda sayar ve 4. başlangıçta, geçişten 5 dakika sonra veya çalışan
  adayın watchdog'u isteyince, *kendisinin* kaydettiği önceki sürüme geri döner (sahibi root olan bir dizin; ajanın durum dosyası hedefi seçemez); ajan `rolled_back` raporlar.
  Legacy mod: aynı kontrol `main` içinde ilk iş olarak çalışır ve `current`'ı kendisi değiştirir.
- **Onay:** adayın ilk başarılı OTLP gönderimi `confirmed` değerini ayarlar, `succeeded` raporlar; `versions/` güncel + önceki sürüme budanır (artı deb/rpm paketinin sahip olduğu
  sürüm), staged modda bunu bir sonraki başlangıçta `-apply` yapar.
- Başarısız bir talimat aynı action/version/rollout için 1 saat yeniden denenmez; rollback olmuş bir talimat asla denenmez. `not_before` beklenir ve `deadline`'ı geçmiş talimatlar yok
  sayılır (hazırlama sırasında dolan deadline denemeyi başarısız yapar).

Derleme zamanı anahtarları: `make build-linux OPENLOG_RELEASE_PUBLIC_KEYS=<b64>,<b64>`. Uçtan uca senaryo
[`test/update`](https://github.com/onuragtas/openlog/blob/master/agents/infra/test/update) içinde.

Container'da makine kökünü `/host` altına mount et, PID ve ağ namespace'lerini paylaş:

```sh
docker run -d --pid=host --network=host -v /:/host:ro \
  --cap-add SYS_PTRACE --cap-add DAC_READ_SEARCH \
  -e OPENLOG_LICENSE_KEY=... -e OPENLOG_ENDPOINT=https://ingest.example:4318 openlog/openlog-infra-agent
```

## PHP ajanı kurulumu {#php-agent-installation}

Kontrat: `openlog/docs/contracts/php-agent.md` §7.3. Ajan, makinenin PHP runtime'larını (`php`, `php-cgi`, `php-fpm` binary'leri: sürüm, ABI, `openlog.so` etkin ve yüklü mü) her
sync'te raporlar, PHP ajanını (`/opt/openlog/php-agent`) kurabilir, yükseltebilir ve kaldırabilir; varsayılan olarak yalnızca raporlar (`mode: manual`). Fleet sayfası modu tüm
makineler (güncelleme politikası) veya makine başına ayarlar; `config.yaml` backend olmadan çalışır:

```yaml
php_agent:
  mode: auto               # off | manual (varsayılan) | auto
  version: agent           # agent (= bu ajanın sürümü, varsayılan) veya örn. 0.9.1
  reload: graceful         # none (varsayılan: PHP-FPM modülü bir sonraki reload'unda yükler) | graceful
  exclude_bins: [/usr/bin/php7.4]
  remote_config: true      # fleet ayarları bunların yerine geçer (varsayılan)
  health_check_after: 5m
```

- Kurulum, güncel unit'in ayrıcalıklı pre-start adımını (`ExecStartPre=+… -apply`) gerektirir: ajan imzalı sürümü `/var/lib/openlog-infra-agent/php-agent/` içine indirip doğrular,
  bir kez çıkar, `-apply` yeniden doğrular, sahibi root olacak şekilde kurar, `openlog-php-install install` çalıştırır ve `/opt/openlog/infra-agent/php-agent-status.json` yazar.
  `health_check_after` sonrasında ajan PHP'nin modülü yüklediğini, reload edilen unit'lerin aktif olduğunu ve PHP servislerinin hâlâ veri gönderdiğini kontrol eder; aksi halde bir sonraki
  başlangıç önceki sürüme döner (veya kaldırır).
- `openlog-php-agent` deb/rpm/apk paketinden veya elle yapılan kurulumlar raporlanır (`managed_by: package` / `manual`) ve asla değiştirilmez. Fleet kendi kurulumunu
  `/opt/openlog/php-agent/.managed-by-openlog-infra-agent` ile işaretler.
- `mode: off` bir fleet kurulumunu kaldırır (ini dosyaları ve `/opt/openlog/php-agent`).
- Kendi telemetrisi: `openlog.agent.php_agent.operations{operation, result}`; makine `openlog.php_agent.version` taşır.
- PHP-FPM worker'larının span'leri ajana `php.sock` üzerinden ulaşır (grubu ve modu için bkz. "PHP forwarder").

## Java ajanı güncellemeleri {#java-agent-updates}

Kontrat: `openlog/docs/contracts/java-agent.md` §2 (D-123). Ajan, openlog Java ajanını yükleyen JVM'leri raporlar (komut satırında veya `JAVA_TOOL_OPTIONS`/`JDK_JAVA_OPTIONS` içinde
`-javaagent:`: pid, ajan yolu, JVM'in gerçekten yüklediği sürüm, yeniden başlatılması gerekip gerekmediği) ve sabit bir jar yolunu güncel tutabilir. Uygulamayı bir kez yapılandır, örneğin
PM2 ecosystem dosyasında veya systemd unit'inde:

```sh
JAVA_TOOL_OPTIONS=-javaagent:/opt/openlog/openlog-javaagent.jar
```

```yaml
java_agent:
  mode: auto                # off | manual (varsayılan: yalnızca JVM'leri raporla) | auto
  version: agent            # agent (= bu ajanın sürümü, varsayılan) veya örn. 0.9.1
  remote_config: true       # Fleet sayfasının mode/version değerleri bunların yerine geçer (varsayılan)
  health_check_after: 2m
  install_root: /opt/openlog/java-agent          # versions/<v>/openlog-javaagent.jar, current
  link_path: /opt/openlog/openlog-javaagent.jar  # Windows: C:\Program Files\openlog\openlog-javaagent.jar
```

- Jar, kendini güncelleme gibi indirilip doğrulanır (imzalı manifest, sha256), sahibi root olacak şekilde `versions/<v>/` içine kurulur ve `link_path` atomik olarak ona çevrilir (yeni bir
  symlink eskisinin üzerine rename edilir). Jar asla yerinde değiştirilmez: çalışan JVM'ler başladıkları sürümde kalır ve `restart_pending` olarak raporlanır; yeni sürümü yüklemek için
  uygulamayı yeniden başlat. Ajan bir JVM'i asla yeniden başlatmaz veya ona sinyal göndermez.
- Linux: kurulum, PHP ajanı gibi ayrıcalıklı pre-start adımını (`-apply`) gerektirir (ajan bir kez çıkar). macOS ve Windows: ayrıcalıklı servis süreci doğrudan uygular.
- Ajanın oluşturmadığı bir `link_path`'e (örn. oraya elle kopyalanmış bir jar) asla dokunulmaz: rapor `unmanaged` der. Sahiplenmek için `-javaagent`'ı
  `/opt/openlog/java-agent/current/openlog-javaagent.jar` adresine yönelt veya dosyayı başka yere taşı (`sudo mv /opt/openlog/openlog-javaagent.jar /opt/openlog/openlog-javaagent.jar.manual`;
  çalışan JVM'ler etkilenmez), böylece ajan linki 5 dakika içinde (veya `systemctl restart openlog-infra-agent` ile) oluşturur.
- Windows, bir JVM'in açık tuttuğu jar'ları kilitler, bu yüzden `link_path`'teki kopya ancak o uygulamalar durduğunda değişir (30 sn'de bir yeniden denenir);
  `-javaagent:C:\Program Files\openlog\java-agent\current\openlog-javaagent.jar` bu beklemeden güncellenir.
- `mode: off`, çalışan hiçbir JVM kullanmadığında yönetilen jar ve linki kaldırır. Hâlâ o yolla yapılandırılmış uygulamalar sonrasında başlamaz.
- Rollback: kurulan jar doğrulamadan geçemezse otomatik; aksi halde `java_agent.version` değerini (Fleet politikası) önceki sürüme sabitle.

## Yapılandırma {#configuration}

Bkz. [`packaging/config.example.yaml`](https://github.com/onuragtas/openlog/blob/master/agents/infra/packaging/config.example.yaml). `OPENLOG_LICENSE_KEY` ve `OPENLOG_ENDPOINT` dosyayı geçersiz kılar.
Bilinmeyen anahtarlar reddedilir.

Güvenilirlik:
- Toplama ve gönderim birbirinden ayrıdır. Toplayıcılar kendi sabit takvimlerinde çalışır ve yalnızca payload'ları kuyruğa koyar, böylece yavaş veya erişilemeyen bir ingest asla bir örneği geciktirmez veya atlatmaz.
- Sınırlı bir bellek içi kuyruğu (64 payload / 16 MiB) tek bir exporter goroutine'i boşaltır.
- Payload'lar gzip ile sıkıştırılır ve `openlog-license-key` header'ıyla `<endpoint>/v1/metrics`, `/v1/logs` ve (PHP forwarder) `/v1/traces` adreslerine gönderilir.
- Ağ hatalarında, 429 veya 503'te exporter üstel backoff ve jitter ile yeniden dener, `Retry-After`'a uyar.
- Bellek kuyruğu dolunca veya bir gönderim başarısız olmaya devam edince disk tamponuna taşar. Tampon en fazla `buffer.max_bytes` tutar; dolunca en eski payload'lar atılır ve `openlog.agent.export.items{outcome="dropped"}` içinde sayılır.
- Her payload kuyruğa girerken bir sıra numarası alır ve exporter, bellekte veya diskte beklemesine bakmadan her zaman bekleyen en düşük numaralıyı gönderir. Böylece teslimat yeniden başlatmalar ve kesintiler boyunca FIFO kalır.
- Envanter snapshot'ları en fazla `export.max_request_bytes` büyüklüğünde isteklere bölünür; snapshot tamamlandı kaydı her zaman tüm öğe kayıtlarından sonra gönderilir.
- SIGTERM/SIGINT'te ajan toplamayı durdurur ve kuyruğu en fazla 5 sn boyunca boşaltmayı dener. Hâlâ bekleyen her şey, son anda iptal edilen gönderim dahil, disk tamponuna yazılır ve bir sonraki başlangıçta yeniden oynatılır.
- Toplamanın kendisi (ağı beklemek değil) kalıcı bir pencere boyunca bir CPU'nun %1'inden fazlasını kullanırsa metrik aralığı 60 sn'ye kadar ikiye katlanır ve `openlog.agent.collection.interval` olarak raporlanır.

## Discovery kuralları {#discovery-rules}

Her kural bir YAML dosyasıdır. Katalog [`rules/`](https://github.com/onuragtas/openlog/blob/master/agents/infra/rules) dizininden gömülür. `discovery.rules_dir` içindeki dosyalar yeni kural ekler,
orada aynı `id`'ye sahip bir kural gömülü olanın yerine geçer. **Yeni bir servisi desteklemek yalnızca yeni bir kural gerektirir; Go kodu gerekmez.**

```yaml
id: redis                   # [a-z0-9][a-z0-9_.-]*, benzersiz
name: Redis
category: database          # serbest küçük harf: web, proxy, database, cache, search, queue, container, runtime, system, …
match:                      # herhangi bir eşleştirici kuralı aday yapar; her girdinin tam olarak bir türü vardır
  - process: { exe_basename: ["redis-server"] }          # ayrıca exe_basename_regex, cmdline_regex (verilen tüm alanlar eşleşmeli)
  - systemd_unit: { name_regex: "^redis(-server)?(@.*)?\\.service$" }
  - listening_port: { port: 6379, process_exe_basename: ["redis-server"] }  # isteğe bağlı protocol: tcp|udp
  - package: { name: ["redis-server", "redis"] }         # veya name_regex
  - container: { image_regex: "(^|/)redis(:|$)" }        # container envanteri geldiğinde değerlendirilir (M1)
version:                    # sırayla denenir; binary'ler asla çalıştırılmaz
  - package: ["redis-server", "redis"]                   # dağıtım sürümü normalize edilir: 5:7.0.15-1build2 → 7.0.15
  - package_regex: "^redis[0-9]*$"
  - cmdline_regex: "redis-server.*v=(\\S+)"              # ilk yakalama grubu
endpoints:
  - from: listening_port
integration:                # yazılmazsa → not_available
  id: redis
  auto_enable: true         # ve karşılanmamış requires yoksa → enabled
  requires: []              # örn. ["credentials"]; karşılanmamışsa (M0'da her zaman) → needs_configuration
apm_hint: null              # veya { language: php, agent: openlog-agent-php }
logs:                       # bilinen log glob'ları → log_paths; logs.auto_from_discovery ile takip edilir
  - path: "/var/log/redis/*.log"
```

Eşleşmeler nasıl servis olur (`discovered_service`, anahtar `<rule_id>:<instance>`). Instance şunlardan ilk bulunandır:
executable yolu → systemd unit → container id → paket anahtarı → port anahtarı.
1. Bir `process` eşleştiricisiyle eşleşen süreçler executable yoluna göre gruplanır: çözümlenmiş `exe` linki veya link okunamıyorsa mutlak argv[0].
   Container içindeki süreçler bunun yerine container id'ye göre gruplanır (her redis container'ı `/usr/local/bin/redis-server` çalıştırır).
   exe'si okunamayan çocuklar eşleşen parent'larının instance'ına katılır.
2. Eşleşen bir `listening_port`, sahibi sürecin instance'ına katılır.
3. Eşleşen bir `systemd_unit`, süreçleri içinde çalışan instance'larla birleşir.
   Görünür hiçbir şey çalıştırmayan bir unit mevcut instance'lara katılır; aksi halde kendi instance'ı olur (template unit'ler `foo@.service` yok sayılır).
4. Container'lar imaja göre eşleşir (Docker container envanteri). Süreçleri zaten bir instance'a katılmış bir container onunla birleşir; aksi halde container id instance olur.
5. Kuralın paket eşleştiricilerinde veya sürüm kaynaklarında adı geçen kurulu paketler bağlanır. Başka bir şey bulunamayan eşleşen bir sunucu paketi, paket anahtarlı bir instance üretir.
6. Bilinen sahibi olmayan portlar mevcut instance'lara katılır; port anahtarı ancak hiç instance yoksa instance olur.

Kural eklemek için `rules/<id>.yaml` yaz (veya `rules_dir` içine bırak). `internal/discovery/testdata/cases/` altına bir case ekle,
sonra `go test ./internal/discovery` çalıştır. Pozitif case'i olmayan her gömülü kural için test başarısız olur.

Başlangıç kataloğu: nginx, Apache httpd, HAProxy, Caddy, MySQL, MariaDB, PostgreSQL, MongoDB, Redis, Memcached,
Elasticsearch, OpenSearch, ClickHouse, RabbitMQ, Kafka, Docker, containerd, kubelet, PHP-FPM, Node.js, JVM,
Gunicorn, uWSGI, Uvicorn, .NET, sshd, cron, chrony, ntpd.

## Dizin yapısı {#layout}

```
cmd/openlog-infra-agent   flags: -config -version -once -validate-rules -self-test -apply -reconcile [-reconcile-context]
                          -configure [-license-key -endpoint] -verify-release <manifest> [-artifact] -uninstall-service
                          (Windows: runs as a service when started by the SCM)
internal/config        YAML config, env overrides, validation
internal/version       build version, commit, date (ldflags)
internal/release       trusted release keys (ldflags + release.trusted_keys_file)
internal/update        sync client, install detection, manifest verification, download/extract, stage/confirm/rollback,
                       privileged apply (-apply) and reconcile (-reconcile)
internal/hostfs        root-path aware file access (+ statfs, linux only)
internal/osutil        open flags, file identity, delete-sharing opens (Windows)
internal/osinfo        macOS/Windows OS version (sw_vers, sysctl, registry)
internal/plist         binary and XML property list decoder (macOS receipts, bundles, launchd)
internal/procfs        pure procfs parsers
internal/resource      host.id chain and resource attributes
internal/metrics       host metric collectors + self-telemetry
internal/inventory     inventory collectors and snapshot encoding
internal/mask          secret masking
internal/discovery     rule schema, loader, engine, service index (process → rule id)
internal/containers    Docker Engine API client, cgroup v2 container lookup
internal/logs          file tailing, journald export reader, offsets/cursor state
internal/rpmdb         read-only SQLite reader + rpm header parser
internal/exporter      OTLP/HTTP client, retry, request splitting
internal/buffer        on-disk FIFO retry buffer
internal/agent         scheduler, change fingerprint, resource budget
rules/                 embedded discovery catalog
packaging/             systemd unit and launchd plist (embedded by packaging.go), example config, windows/ (WiX MSI, build and install test scripts)
test/update            self-update end-to-end scenario (Debian 12 container)
```
