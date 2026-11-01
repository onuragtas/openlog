# Sözleşme: Semantic Conventions (v1) {#contract-semantic-conventions-v1}

Ajanlar ile backend arasındaki bağlayıcı sözleşme. Bir OpenTelemetry semantic convention'ı varsa openlog ona uyar; openlog'a özel adlar `openlog.` önekini kullanır.

## 1. Resource attribute'ları {#_1-resource-attributes}

### Altyapı ajanı (gönderdiği her OTLP resource'u) {#infra-agent-every-otlp-resource-it-sends}

| Attribute | Zorunlu | Kaynak / değer |
|---|---|---|
| `host.id` | evet | `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid` → ajanın durum dizininde saklanan üretilmiş UUID. Çalışan ajan çözülen id'yi `/run/openlog-infra-agent/host-id` dosyasında yayınlar (`<id>\n`, mod 0644, yalnızca dizin varsa, örneğin systemd `RuntimeDirectory`); APM ajanları önce onu okur, dizini salt okunur mount eden container'lardan da. macOS: `IOPlatformUUID`; Windows: `MachineGuid` (`HKLM\SOFTWARE\Microsoft\Cryptography`; sysprep'siz klonlanan image'lar bunu paylaşır); ikisi de durum dizininde saklanan UUID'ye döner; yayınlanan `host-id` dosyası yalnızca Linux'tadır (D-104) |
| `host.name` | evet | kernel hostname'i |
| `host.arch` | evet | `amd64`, `arm64`, … (OTel değerleri) |
| `os.type` | evet | `linux`, `darwin` (macOS), `windows` (D-104) |
| `os.name` | hayır | `/etc/os-release` `ID`; `macos` / `windows` |
| `os.version` | hayır | `/etc/os-release` `VERSION_ID`; macOS `sw_vers -productVersion`; Windows registry'den `<major>.<minor>.<build>` |
| `os.description` | hayır | `/etc/os-release` `PRETTY_NAME`; örneğin `macOS 15.5 (24F74)`, `Windows Server 2022 Datacenter 21H2 (build 20348.2340)` |
| `openlog.os.kernel_release` | hayır | `uname -r` karşılığı (`/proc/sys/kernel/osrelease`) |
| `cloud.provider` | hayır | `aws`, `gcp`, `azure`: yanıt veren instance metadata servisi (aşağıda "Cloud instance bilgileri"ne bak) |
| `cloud.platform` | hayır | `aws_ec2`, `gcp_compute_engine`, `azure_vm` |
| `cloud.region` | hayır | `eu-central-1`, `europe-west1`, `westeurope`; sağlayıcı yalnızca zone raporluyorsa zone'dan türetilir |
| `cloud.availability_zone` | hayır | `eu-central-1a`, `europe-west1-b`, `westeurope-2` (Azure: `<location>-<zone>`) |
| `cloud.account.id` | hayır | AWS account id, GCP project id, Azure subscription id |
| `host.type` | hayır | instance türü: `m5.large`, `n2-standard-4`, `Standard_D4s_v5` |
| `openlog.host.lifecycle` | hayır | `on-demand`, `spot` veya `preemptible` (GCP'nin eski preemptible VM'leri) |
| `openlog.entity.type` | evet | `host` |
| `openlog.agent.name` | evet | `openlog-infra-agent` |
| `openlog.agent.version` | evet | ajanın build sürümü |
| *kullanıcının ek attribute'ları* | hayır | yapılandırmadan `host.extra_attributes`, olduğu gibi gönderilir |

**Cloud instance bilgileri.** Ajan, instance metadata servisine **başlangıçta bir kez** sorar (`host.cloud_metadata: auto`,
§5) ve yanıtı her payload'a ekler; backend makineyi buna göre fiyatlar ([cost.md](/tr/contracts/cost)). Yoklama toplamda 1,5 sn ve istek
başına 500 ms ile sınırlıdır, link-local `169.254.169.254` adresini kullanır (asla DNS, asla HTTP proxy değil) ve önce DMI sistem
üreticisinin işaret ettiği sağlayıcıya sorar (IMDSv1'e dönüşlü AWS IMDSv2, GCP
`computeMetadata/v1`, Azure `metadata/instance`). Cloud'da **olmayan** bir makine bağlantı reddi alır, en kötü ihtimalle
paketleri düşüren bir firewall arkasında süre sınırına takılır: bu attribute'ların hiçbirini taşımaz, ajan başlangıçta hiçbir şey
loglamaz ve geri kalan her şey değişmeden devam eder. Bunlar olmayan host'lar vCPU/GB başına fiyatlanır veya fiyatsız raporlanır,
asla ücretsiz olarak değil. Bilgiler ajan çalışırken yeniden okunmaz; instance türü yalnızca bir stop ve start ile değişir, o da
ajanı zaten yeniden başlatır.

### Backend'in çıkardıkları {#backend-extraction}

Processor bu sütunları resource attribute'larından çıkarır; eksik değerler `''` olur:

| Sütun | Attribute |
|---|---|
| `host_id` | `host.id` |
| `host_name` | `host.name` |
| `service_name` | `service.name` |

`tenant_id` attribute'lardan **asla** alınmaz; ingest'in ayarladığı Kafka header'ından gelir.

## 2. Host metrikleri (altyapı ajanı) {#_2-host-metrics-infra-agent}

Counter'lar **kümülatiftir**; kullanım değerlerini ajan iki örnek arasında hesaplar ve gauge olarak gönderir. M0 tüm CPU'ların toplamını gönderir (CPU başına seri yok).

| Metrik | Tür | Birim | Attribute'lar |
|---|---|---|---|
| `system.cpu.time` | Sum, cumulative, monotonic | `s` | `cpu.mode` = `user`,`nice`,`system`,`idle`,`iowait`,`interrupt`,`softirq`,`steal` |
| `system.cpu.utilization` | Gauge | `1` | `cpu.mode` (aynı değerler; 0..1) |
| `system.cpu.logical.count` | Sum, non-monotonic | `{cpu}` | — |
| `system.cpu.load_average.1m` / `.5m` / `.15m` | Gauge | `{thread}` | — |
| `system.memory.usage` | Sum, non-monotonic | `By` | `system.memory.state` = `used`,`free`,`cached`,`buffers` |
| `system.memory.limit` | Sum, non-monotonic | `By` | — |
| `system.memory.utilization` | Gauge | `1` | `system.memory.state` |
| `system.paging.usage` | Sum, non-monotonic | `By` | `system.paging.state` = `used`,`free` |
| `system.filesystem.usage` | Sum, non-monotonic | `By` | `system.device`, `system.filesystem.mountpoint`, `system.filesystem.type`, `system.filesystem.state` = `used`,`free`,`reserved` |
| `system.filesystem.utilization` | Gauge | `1` | `system.device`, `system.filesystem.mountpoint`, `system.filesystem.type` |
| `system.disk.io` | Sum, cumulative, monotonic | `By` | `system.device`, `disk.io.direction` = `read`,`write` |
| `system.disk.operations` | Sum, cumulative, monotonic | `{operation}` | `system.device`, `disk.io.direction` |
| `system.network.io` | Sum, cumulative, monotonic | `By` | `network.interface.name`, `network.io.direction` = `receive`,`transmit` |
| `system.network.packets` | Sum, cumulative, monotonic | `{packet}` | `network.interface.name`, `network.io.direction` |
| `system.network.errors` | Sum, cumulative, monotonic | `{error}` | `network.interface.name`, `network.io.direction` |
| `system.network.dropped` | Sum, cumulative, monotonic | `{packet}` | `network.interface.name`, `network.io.direction` |
| `system.uptime` | Gauge | `s` | — |
| `system.process.count` | Sum, non-monotonic | `{process}` | `process.status` = `running`,`sleeping`,`disk_sleep`,`stopped`,`zombie`,`idle`,`other` |

`system.memory.usage{used}` = `MemTotal − MemFree − Buffers − Cached − SReclaimable` (hepsi `/proc/meminfo`'dan).
`SReclaimable`, `cached` içinde sayılır (`cached` = `Cached + SReclaimable`), yani dört durumun toplamı `MemTotal`'dır.

Şu türlerdeki dosya sistemleri varsayılan olarak hariçtir: `proc, sysfs, devtmpfs, devpts, tmpfs, cgroup, cgroup2, overlay, squashfs, securityfs, debugfs, tracefs, pstore, bpf, mqueue, hugetlbfs, configfs, fusectl, autofs, binfmt_misc, nsfs, rpc_pipefs, ramfs`.
Dizin olmayan mount noktaları hariçtir (Docker'ın `/etc/hosts`, `/etc/hostname`, `/etc/resolv.conf` gibi bind mount edilmiş dosyaları).
Ajanın kendi `state_dir` veya `buffer.dir` dizini üzerindeki bir mount, raporlanan başka bir mount aynı cihaza sahipse (`system.device` ve major:minor) hariç tutulur; aynı cihazdaki diğer volume'lar (örneğin `/data`) korunur.
Varsayılan olarak hariç tutulan disk cihazları: `loop*`, `ram*`; partition'lar dahildir. Varsayılan olarak hariç tutulan ağ arayüzleri: `lo`.

### Platform notları (macOS ve Windows, D-104) {#platform-notes-macos-and-windows-d-104}

Her işletim sisteminden aynı metrik adları, birimler ve attribute'lar gönderilir; ajan macOS veya Windows'ta `host.root_path: /` ile çalıştığında değerler native API'lerden (gopsutil v4) gelir. Bir platformda olmayan durumlar 0 olarak raporlanır, böylece her host'ta aynı seriler bulunur:

| Metrik | macOS | Windows |
|---|---|---|
| `system.cpu.time` / `utilization` | `host_statistics` modları; `iowait`, `softirq`, `steal` = 0 | `GetSystemTimes`; `nice`, `iowait`, `softirq`, `steal` = 0 |
| `system.cpu.load_average.*` | `getloadavg` | gönderilmez (load average yok) |
| `system.memory.usage` | `free` = boş sayfalar, `cached` = inactive, `used` = geri kalan (active + wired + compressed), `buffers` = 0 | `free` = available (standby listesini içerir), `used` = total − available, `cached` = `buffers` = 0 |
| `system.filesystem.*` | mount edilmiş volume'lar; `devfs`/`autofs`/`nullfs` ve mühürlü sistem volume'unun `/System/Volumes` altındaki yardımcı mount'ları (`/System/Volumes/Data` hariç) atlanır; `system.device` = `/dev/diskNsM` | sürücü harfleri; `system.device` = `C:` |
| `system.disk.*` | tüm disk başına IOKit | volume başına `IOCTL_DISK_PERFORMANCE` (disk performans sayaçları kapalıyken olmayabilir) |
| `system.network.*` | `lo0` atlanır | loopback pseudo-arayüzü atlanır; `network.interface.name` adaptör adıdır |
| `system.process.count` | kernel `p_stat` (macOS çoğu süreci `running` raporlar) | her süreç `running` (Windows'ta çalışma durumu yok) |
| `process.*` | `process.owner` = kullanıcı adı | `process.owner` = `DOMAIN\user`; `process.open_file_descriptors` = handle sayısı |

Container metrikleri (cgroup'lar) yalnızca Linux'ta vardır.


### Süreç metrikleri (altyapı ajanı, `process_metrics`) {#process-metrics-infra-agent-process-metrics}

CPU kullanımına göre en üstteki `process_metrics.top_n_cpu` süreç (varsayılan 20, yalnızca kullanımı > 0 olanlar)
ile RSS'e göre en üstteki `process_metrics.top_n_memory` sürecin (varsayılan 20) birleşimi için süreç başına seriler. Kernel thread'leri atlanır. Yalnızca bu süreçler seri üretir; yeniden başlayan
bir süreç yeni bir `process.pid` alır, yani seriler PID'lerle birlikte değişir (örnek başına top-N birleşimiyle sınırlıdır).

| Metrik | Tür | Birim | Notlar |
|---|---|---|---|
| `process.cpu.utilization` | Gauge | `1` | (utime+stime delta) / geçen süre / mantıksal CPU'lar; tüm host'un 0..1'i; bir sürecin ilk örneğinde gönderilmez |
| `process.memory.usage` | Sum, non-monotonic | `By` | RSS |
| `process.memory.virtual` | Sum, non-monotonic | `By` | VSZ |
| `process.threads` | Sum, non-monotonic | `{thread}` | |
| `process.open_file_descriptors` | Sum, non-monotonic | `{count}` | `/proc/<pid>/fd` okunamıyorsa atlanır |

Data point attribute'ları (resource, §1'in host resource'u olarak kalır):

| Attribute | Değer |
|---|---|
| `process.pid` | int |
| `process.executable.name` | `comm` (`/proc/<pid>/stat`) |
| `process.executable.path` | `/proc/<pid>/exe` hedefi; okunamıyorsa atlanır |
| `process.owner` | gerçek UID'nin kullanıcı adı (`/etc/passwd`), yoksa string olarak sayısal UID |
| `openlog.discovery.id` | sürecin ait olduğu bulunan servisin `rule_id`'si (PID, executable instance, sonra container id ile; `runtime` olmayan bir servis `runtime` olana üstün gelir); yoksa atlanır |

### Container metrikleri (altyapı ajanı, `containers`) {#container-metrics-infra-agent-containers}

`/sys/fs/cgroup` altında 64-hex container id'siyle adlandırılmış her dizin için cgroup v2'den okunur (`docker-<id>.scope`, `docker/<id>`,
`cri-containerd-<id>.scope`, `crio-<id>.scope`, `libpod-<id>.scope`). cgroup v1 host'ları container metriği göndermez.

| Metrik | Tür | Birim | Kaynak / attribute'lar |
|---|---|---|---|
| `container.cpu.time` | Sum, cumulative, monotonic | `s` | `cpu.stat` `usage_usec` |
| `container.cpu.utilization` | Gauge | `1` | kullanım deltası / geçen süre / host'un mantıksal CPU'ları (host'un 0..1'i) |
| `container.memory.usage` | Sum, non-monotonic | `By` | `memory.current − inactive_file` (`docker stats` gibi) |
| `container.memory.limit` | Sum, non-monotonic | `By` | `memory.max`; sınırsızsa host `MemTotal` |
| `container.blockio.io` | Sum, cumulative, monotonic | `By` | cihazlar üzerinden toplanmış `io.stat` rbytes/wbytes; `disk.io.direction` = `read`,`write` |
| `container.blockio.operations` | Sum, cumulative, monotonic | `{operation}` | `io.stat` rios/wios; `disk.io.direction` |
| `container.network.io` | Sum, cumulative, monotonic | `By` | bir container sürecinin `/proc/<pid>/net/dev` dosyası, `lo` olmayan arayüzler toplanır; `network.io.direction` = `receive`,`transmit`; host network namespace'indeki container'lar için atlanır |
| `container.restarts` | Sum, cumulative, monotonic | `{restart}` | Docker `RestartCount` (inspect), CRI annotation'ı `io.kubernetes.container.restartCount`; yalnızca container metadata'sı varsa |
| `openlog.container.status` | Gauge | `1` | container başına değer 1; `openlog.container.state` = Docker durumu (`running`,`paused`,`restarting`,`exited`,`created`,`dead`); `openlog.container.health` = `healthy`,`unhealthy`,`starting` (yalnızca healthcheck varsa); `openlog.container.started_at` = RFC3339 başlangıç zamanı (inspect) |

Her container metriğinin data point attribute'ları: `container.id` (her zaman), Docker veya CRI metadata'sı varsa `container.name`, `container.image.name`, `container.image.tags`
(string dizisi; örneğin `["1.25"]`), biliniyorsa `container.runtime` (`docker`, `containerd`, `cri-o`, `podman`)
ve container label'larından (yalnızca Docker metadata'sı): `docker.compose.project` (`com.docker.compose.project`), `docker.compose.service`
(`com.docker.compose.service`), `k8s.pod.name` (`io.kubernetes.pod.name`), `k8s.namespace.name` (`io.kubernetes.pod.namespace`),
`k8s.container.name` (`io.kubernetes.container.name`; cri-dockerd üzerinden Docker'da Kubernetes; containerd/CRI-O host'larında ad yoktur, aşağıya bak).
Kümülatif container counter'ları başlangıç zamanı taşımaz.

`openlog.container.status`, Docker Engine API'nin listelediği, çalışan, paused veya restarting olan ya da son 24 saatte durmuş, başlamış
veya oluşturulmuş her container için ve Docker metadata'sı olmayan her container cgroup'u için (durum
`running`, health yok) her metrik aralığında gönderilir; örnek başına en fazla 500 container, önce çalışanlar. Yani durmuş container'lar (durumlarıyla)
durduktan sonraki 24 saate kadar görünür kalır. `GET /containers/{id}/json` (başlangıç zamanı, restart sayısı, health, log kaynağı) yeni container'lar için,
durum değişikliklerinde ve çalışan container başına en fazla dakikada bir, listeleme başına en fazla 64 kez çalışır. Docker socket'i (veya izni) olmadan ajan
çalışan container'lar için cgroup metriklerini ve durumu yine gönderir, yalnızca `container.id` anahtarıyla; containerd, CRI-O ve Podman container'ları
asla zenginleştirilmez (ad, image veya label yok).

**Container entity'si (backend).** `containers_mv` materialized view'ı (şema 0009), `openlog.container.status`, `container.restarts` ve `container.cpu.time`
noktalarından (tenant, `host.id`, `container.id`) başına bir satır tutar: ilk/son görülme, host adı, yukarıdaki kimlik attribute'ları
ve son status noktasının durumu, health'i ve başlangıç zamanı (API: [api.md](/tr/contracts/api) "Containers"). Son noktadan 30 gün sonra.

### Ajanın kendi telemetrisi {#agent-self-telemetry}

| Metrik | Tür | Birim | Attribute'lar |
|---|---|---|---|
| `openlog.agent.export.items` | Sum, cumulative, monotonic | `{item}` | `signal` = `metrics`,`logs`,`traces` (`traces` yalnızca PHP forwarder başladıktan sonra; item'lar = span'ler); `outcome` = `sent`,`buffered`,`dropped` |
| `openlog.agent.buffer.usage` | Sum, non-monotonic | `By` | — |
| `openlog.agent.collector.duration` | Gauge | `s` | `collector` |
| `openlog.agent.permission_denied` | Sum, cumulative, monotonic | `{error}` | `collector` |
| `openlog.agent.collection.interval` | Gauge | `s` | — (geçerli metrik aralığı; CPU bütçesi kontrolü geri çekildiğinde yapılandırılan değerin üstüne çıkar) |
| `openlog.agent.update.state` | Gauge | `1` | `state` = `idle`,`downloading`,`verifying`,`staged`,`restarting`,`confirming`,`succeeded`,`failed`,`rolled_back` (bir nokta, değer 1, attribute = geçerli self-update durumu; bkz. releases-updates.md §3) |
| `openlog.agent.update.attempts` | Sum, cumulative, monotonic | `{attempt}` | `result` = `succeeded`,`failed`,`rolled_back` (ajan başlangıcından beri; başlangıçta yapılan rollback sonraki süreç tarafından sayılır) |
| `openlog.agent.integration.collections` | Sum, cumulative, monotonic | `{collection}` | `integration` = entegrasyon id'si (§6); her instance'ın her toplama denemesi |
| `openlog.agent.integration.errors` | Sum, cumulative, monotonic | `{error}` | `integration`; başarısız toplamalar (`error` veya `needs_configuration` durumu; kısmi toplamalar sayılmaz) |
| `openlog.agent.integration.duration` | Gauge | `s` | `integration`; entegrasyonun herhangi bir instance'ının son toplama süresi |
| `openlog.agent.php.messages` | Sum, cumulative, monotonic | `{message}` | `result` = `accepted`,`malformed`,`unsupported_version`,`dropped` (PHP forwarder datagram'ları, php-agent.md §6; forwarder başladıktan sonra üretilir) |
| `openlog.agent.php.spans` | Sum, cumulative, monotonic | `{span}` | — (export hattına verilen span'ler) |
| `openlog.agent.php.reassembly_timeouts` | Sum, cumulative, monotonic | `{trace}` | — (`reassembly_timeout` sonrası eksik export edilen bölünmüş trace'ler) |
| `openlog.agent.php.pending_traces` | Gauge | `{trace}` | — (eksik parçaları bekleyen trace'ler) |
### 2.9 Donanım sensörleri (`sensors`, Linux, D-145) {#_2-9-hardware-sensors-sensors-linux-d-145}

Kaynak: `/sys/class/hwmon`, `sensors(1)`'in okuduğu kernel arayüzü. Yalnızca Linux; macOS ve Windows'ta
collector her aralıkta başarısız olmak yerine kümenin parçası değildir, hwmon olmayan bir makine (bir
container, sensör geçirilmemiş bir VM) hata yerine hiçbir şey raporlamaz.

Bu, ajanın topladığı diğer her şeyin altındaki katmandır: termal olarak kısılmış bir CPU diğer her
metrikte nedeni söylenmeden "daha yavaş" olarak görünür, durmuş bir fan ise makine durana kadar
hiçbir şekilde görünmez.

| Metrik | Tür | Birim | Attribute'lar | hwmon dosyası |
|---|---|---|---|---|
| `system.hardware.temperature` | Gauge, double | `Cel` | `hw.chip`, `hw.sensor`, `hw.label` | `temp<n>_input` (millidegrees → °C) |
| `system.hardware.fan.speed` | Gauge, double | `rpm` | `hw.chip`, `hw.sensor`, `hw.label` | `fan<n>_input` |
| `system.hardware.voltage` | Gauge, double | `V` | `hw.chip`, `hw.sensor`, `hw.label` | `in<n>_input` (mV → V) |
| `system.hardware.current` | Gauge, double | `A` | `hw.chip`, `hw.sensor`, `hw.label` | `curr<n>_input` (mA → A) |
| `system.hardware.power` | Gauge, double | `W` | `hw.chip`, `hw.sensor`, `hw.label` | `power<n>_input` (µW → W) |
| `<metric>.limit` | Gauge, double | okumanın birimi | okumanın attribute'ları artı `hw.limit` = `high`, `critical` | `*_max`, `*_crit` |

`hw.chip`, çipin `name` dosyasıdır (`coretemp`, `nct6798`), `hw.sensor` okumanın kendi dosya önekidir
(`temp1`), `hw.label` ise kernel yayınlıyorsa etiketidir (`Package id 0`, `Core 0`); etiket olmadan
bir okuma kimsenin yerini bulamadığı bir sayıdır.

Okumaları güvenilir kılan üç kural var: eşik, okumanın bir alanı değil kendi serisidir, çünkü "ne kadar sıcak" ve
"ne kadar ısınabilir" iki ayrı bilgidir ve alarmın ikisine de ihtiyacı vardır; hiçbir makinenin üretemeyeceği bir değer
(bağlantısı kesilmiş sensörden −274 °C) atılır, **0 rpm'deki fan ise tutulur**, çünkü durmuş fan listedeki en önemli okumadır;
ve toplama başına en fazla `collectors.max_sensors` (200) okuma saklanır, yani yüzlerce sensörlü bir kasa
sınırlı sayıda seridir.

## 3. Envanter ve discovery (OTLP logları) {#_3-inventory-and-discovery-otlp-logs}

Envanter ve discovery sonuçları, host metrikleriyle aynı resource içinde, logs sinyali üzerinden OTLP **LogRecord**'ları olarak gönderilir.

### 3.1 Öğe kaydı {#_3-1-item-record}

| Alan | Değer |
|---|---|
| `time_unix_nano` | snapshot zamanı (bir snapshot'ın her kaydı için aynı) |
| `event.name` attribute'u | `openlog.inventory.item` |
| `openlog.inventory.snapshot_id` attribute'u | snapshot başına opak, benzersiz string (örneğin UUIDv7) |
| `openlog.inventory.category` attribute'u | aşağıdaki tabloya bak |
| `openlog.inventory.key` attribute'u | (host, snapshot, category) içinde benzersiz |
| `body` | kategorinin alanlarını içeren bir JSON nesnesi barındıran **string** |

### 3.2 Snapshot tamamlanma kaydı {#_3-2-snapshot-complete-record}

Bir snapshot'ın tüm öğelerinden **sonra** gönderilir (öğeler birden çok OTLP isteğine yayılabilir). Backend bir snapshot'ı ancak bu kayıt geldiğinde güncel sayar.

| Alan | Değer |
|---|---|
| `event.name` attribute'u | `openlog.inventory.snapshot` |
| `openlog.inventory.snapshot_id` attribute'u | aynı id |
| `openlog.inventory.item_count` attribute'u | int, öğe kaydı sayısı |
| `body` | boş |

### 3.3 Kategoriler ve JSON body alanları {#_3-3-categories-and-json-body-fields}

Bilinmeyen alanlar tüketiciler tarafından yok sayılmalıdır; eksik alanlara izin verilir.

| Kategori | Anahtar | Body alanları |
|---|---|---|
| `os` | `os` | `id`, `name`, `version_id`, `pretty_name`, `kernel_release`, `kernel_version`, `arch`, `hostname`, `boot_time` (RFC3339) |
| `hardware` | `cpu` | `vendor`, `model`, `logical_cores`, `physical_cores`, `sockets`, `mhz` |
| `hardware` | `memory` | `total_bytes`, `swap_total_bytes` |
| `hardware` | `dmi` | `sys_vendor`, `product_name`, `product_version`, `bios_vendor`, `bios_version` (seri numarası yok) |
| `kernel_module` | modül adı | `name`, `size_bytes`, `state` |
| `package` | `<manager>:<name>` | `manager` (`dpkg`,`rpm`,`apk`,`pkgutil`,`homebrew`,`app`,`windows`), `name`, `version`, `arch` |
| `systemd_unit` | unit adı | `name`, `type` (`service`,`socket`,`timer`,…), `path`, `enabled_state` (`enabled`,`disabled`,`static`,`masked`,`unknown`; yalnızca D-Bus üzerinden bilinen unit'ler için `generated`,`transient`), `description`, `exec_start` (maskeli). D-Bus üzerinden çalışma durumu (`org.freedesktop.systemd1`, host kökü altında sistem bus socket'i `/run/dbus/system_bus_socket`; bus erişilemezse veya unit yüklü değilse hepsi atlanır): `load_state`, `active_state` (`active`,`inactive`,`failed`,`activating`,…), `sub_state` (`running`,`dead`,`exited`,…), `active_since` (RFC3339, `ActiveEnterTimestamp`; `inactive`/`failed` için değil), yalnızca servisler: `restarts` (`NRestarts`, 0 olabilir), `memory_bytes` (`MemoryCurrent`), `cpu_usage_ns` (`CPUUsageNSec`; accounting kapalıysa bellek/CPU atlanır). Kendi unit dosyası olmayan yüklü servisler (`foo@bar.service` şablon instance'ları, `path`/`exec_start` şablondan gelir, üretilmiş ve transient servisler) de öğedir; device'lar, scope'lar ve dosyasız diğer unit'ler değildir. Durum snapshot anındakidir (durum değişikliklerinde ek snapshot yok) |
| `launchd_service` | job label'ı | macOS (D-104): `label`, `pid` (çalışan job'lar), `last_exit_status`, `program` (plist'in `Program`/`ProgramArguments[0]` değerinden), `path` (plist), `domain` (ajan root çalışıyorsa `system`, değilse `user`). `launchctl list`'teki yüklü job'lar artı `/Library/LaunchDaemons`'taki yüklenmemiş daemon'lar; launch başına `application.*` GUI job'ları atlanır |
| `windows_service` | servis adı | Windows (D-104): `name`, `display_name`, `state` (`running`,`stopped`,`start_pending`,`stop_pending`,`paused`,…), `start_type` (`automatic`,`automatic_delayed`,`manual`,`disabled`,`boot`,`system`), `pid`, `binary_path` (maskeli), `account` |
| `listening_port` | `<proto>:<address>:<port>`; IPv6 adresleri köşeli parantez içinde (`tcp:[::]:22`, `tcp:0.0.0.0:22`) | `protocol` (`tcp`,`udp`), `family` (4,6), `address`, `port`, `pid`, `process_name`, `process_exe` |
| `process` | exe yolu (exe okunamıyorsa `comm:<name>`) | `exe`, `name`, `command` (ilk instance'ın argv0 basename'i, örneğin exe `/usr/bin/redis-check-rdb` iken `redis-server`), `cmdline` (maskeli, ≤1024 karakter, ilk instance), `count`, `pids` (≤20), `uids` (farklı), `start_time` (en erken, RFC3339), `systemd_unit`, `container_id` |
| `user` | kullanıcı adı | `name`, `uid`, `gid`, `home`, `shell` |
| `network_interface` | arayüz adı | `name`, `mac`, `mtu`, `operstate`, `addresses` |
| `mount` | mount noktası | `mountpoint`, `device`, `fs_type`, `options` |
| `container` | container id (64 hex) | `id`, `name`, `runtime` (`docker`; CRI üzerinden listelenen container'lar için `containerd`, `cri-o`; bu durumda `name` Kubernetes container adıdır, `ports` boştur ve `restart_count` kubelet annotation'ı `io.kubernetes.container.restartCount`'tan gelir), `image` (raporlandığı gibi, örneğin `nginx:1.25` veya `sha256:…`), `image_id`, `state` (`running`,`exited`,`paused`,`created`,…), `health` (`healthy`,`unhealthy`,`starting`; healthcheck yoksa atlanır), `created` (RFC3339), `started_at` / `finished_at` (RFC3339; inspect edilene kadar veya hiç başlamamış/durmamışsa atlanır), `restart_count`, `exit_code` (durmuş container'lar, 0 ise atlanır), `labels` (nesne; en fazla 100 anahtar, değerler 256 bayta kırpılır), `ports` (`{ip, private_port, public_port, protocol}` dizisi) |
| `discovered_service` | `<rule_id>:<instance>` | bkz. 3.4 |

Platform notları (D-104): macOS ve Windows host'ları `kernel_module`, `systemd_unit` veya (Windows) `container` öğesi göndermez. Oradaki `package` yöneticileri `pkgutil` (installer makbuzları), `homebrew` (formula ve cask'ler), `app` (`/Applications` içindeki bundle'lar) ve `windows`'tur (Programs and Features: 64 bit ve 32 bit Uninstall registry anahtarları, sistem bileşenleri ve güncellemeler atlanır; `arch` `amd64`/`arm64`/`386`). `os`: `id` `macos`/`windows`, `version_id` örneğin `15.5` / `10.0.20348`, `pretty_name` örneğin `macOS 15.5 (24F74)` / `Windows Server 2022 Datacenter 21H2 (build 20348.2340)`. Windows'ta `user` profili olan hesapları listeler (`uid` = relative identifier, `gid` 0, `home` = profil yolu); Windows'ta `process.uids` boştur. `discovered_service` body'leri `services` (launchd label'ları veya Windows servis adları) taşıyabilir ve `matched_by` `service` içerebilir; discovery kuralları bir Windows süreç adını `.exe` olmadan eşleştirir.

### 3.4 `discovered_service` body'si {#_3-4-discovered-service-body}

```json
{
  "rule_id": "redis",
  "name": "Redis",
  "category": "database",
  "instance": "/usr/bin/redis-server",
  "command": "redis-server",
  "version": "7.2.4",
  "matched_by": ["process", "systemd_unit", "listening_port"],
  "pids": [812],
  "ports": [{"protocol": "tcp", "address": "127.0.0.1", "port": 6379}],
  "systemd_units": ["redis-server.service"],
  "packages": ["dpkg:redis-server"],
  "container_ids": [],
  "integration": {"id": "redis", "status": "enabled"},
  "apm_hint": null,
  "log_paths": ["/var/log/redis/*.log"]
}
```

`integration` nesnesi (M2, çalışan entegrasyonu yansıtır, §6):

| Alan | Varlığı | Anlamı |
|---|---|---|
| `id` | kural bir entegrasyon bildirdiğinde | entegrasyon id'si (`nginx`, `redis`, `mysql`, `postgresql`, `docker`, …) |
| `status` | her zaman | `enabled`, `needs_configuration`, `error`, `not_available` |
| `error` | isteğe bağlı | temizlenmiş sebep, ≤ 256 bayt, asla kimlik bilgisi içermez (aşağıya bak) |
| `hint` | isteğe bağlı, `needs_configuration` ile | instance'ı yapılandıran çok satırlı `config.yaml` parçası (YAML, `#` yorumları); arayüz olduğu gibi gösterir |
| `endpoint` | isteğe bağlı | son başarılı toplamanın endpoint'i: `host:port`, `unix:<path>` veya nginx status URL'i; asla kimlik bilgisi içermez |

Status değerleri:
- `not_available`: kuralın entegrasyonu yoktur (`{"status": "not_available"}`, `id` yok); ajanda `id` için implementasyon yoktur (`hint` açıklar);
  entegrasyonlar devre dışıdır (`integrations.enabled` / `integrations.<id>.enabled` false veya instance override'ı `enabled: false`; hangisi olduğunu `error` söyler);
  ya da `integrations.max_instances` dolmuştur.
- `needs_configuration`: kural `requires: ["credentials"]` der ve ne `username` ne `password` yapılandırılmıştır (bağlantı denenmez);
  sunucu yapılandırılmamış kimlik doğrulama ister (Redis `NOAUTH`, parolasız MySQL `1045`, parolasız PostgreSQL `28P01`/`28000`);
  yapılandırılmış bir `env:`/`file:` secret'ı boş veya eksiktir; endpoint türetilemez; nginx'in bulunan hiçbir portta `stub_status` sayfası yoktur
  (veya `auto_enable: false`); Docker socket'ine erişilemez. `error` sebebi anlatır; `hint` ayarlıdır.
- `error`: son toplama başka bir sebeple başarısız oldu: yanlış kimlik bilgileri (`authentication failed: …`), erişilemeyen endpoint'ler
  (`no endpoint reachable (127.0.0.1:6379): …`), temel sorguda eksik yetkiler, protokol hataları, timeout'lar.
- `enabled`: son toplama başarılı oldu. **Kısmi** bir toplamada `error` yine bulunabilir (örneğin `replica status: permission denied: …`):
  metrikler gönderildi, isteğe bağlı bir sorgu başarısız oldu. Yeni bir instance'ın ilk toplamasından önce (başlangıçtan sonraki ilk snapshot) status
  `endpoint`'siz `enabled`'dır; her instance'ın ilk sonucu oluşur oluşmaz yeni bir snapshot gönderilir, sonrasında bir status, hata veya endpoint değiştiğinde
  dakikada en fazla bir kez (normal snapshot tetikleyicilerine ek olarak).

`error` temizleme: yapılandırılmış secret değerleri `***` ile değiştirilir, URL userinfo (`scheme://user:***@`), DSN userinfo (`user:***@tcp(`),
`password=…`/`password: …` ve §3.5 desenleri maskelenir, boşluklar daraltılır, metin 256 baytta kesilir. Kullanıcı adları, host'lar ve
sunucu mesajları (örneğin `Access denied for user 'openlog'@'172.18.0.1' (using password: YES)`) korunur.

`-once` yazdırmadan önce her entegrasyonun bir toplamasını çalıştırır, yani `discovered_services` çıktısı gerçek durumları gösterir.

`apm_hint`, varsa: `{"language": "php", "agent": "openlog-agent-php", "status": "not_installed"}`. `status` (M2, `agent` = `openlog-agent-php` için ajan
ayarlar): ajanın PHP forwarder'ı son 10 dakikada geçerli span almışsa `active`, değilse `not_installed`
(php-agent.md §6). Status değişikliği bir envanter snapshot'ı tetikler. Kurallar `status` ayarlayamaz.

`command`, servisin ana sürecinin argv0 basename'idir (parent'ı servisin parçası olmayan en düşük PID; setproctitle soneki `:` kaldırılır),
örneğin executable'ın `redis-check-rdb`'ye çözüldüğü Debian'da `redis-server`. Yalnızca görünen addır; `instance` çözülmüş executable'ı korur. Servisin süreci yoksa atlanır.

`display_instance`, multi-call veya symlink'li executable'lar için kullanıcıya gösterilen instance'tır: `instance` bir executable yolu olduğunda, basename'i
executable'ınkinden farklı olduğunda (`/usr/bin/redis-check-rdb`) ve `comm` ona eşit olduğunda (ilk 15 bayt; yani setproctitle başlığı değil
çalıştırılan ad) ana sürecin mutlak argv0'ı (`/usr/bin/redis-server`). Aksi halde atlanır (container instance'ları, göreli argv0, aynı ad). Arayüzler adı `command`,
yolu `display_instance`, yoksa `instance` olarak gösterir. Yalnızca görüntüleme içindir: `instance`, öğe anahtarı, `openlog.discovery.instance` (§6) ve entegrasyon
join'leri çözülmüş executable'ı korur, böylece var olan entity'ler (örneğin Debian'da `redis:/usr/bin/redis-check-rdb`) ajan güncellemesinden sonra anahtarlarını ve
metrik serilerini korur; eski ajanlar alanı basitçe atlar. Aynı kuralın eşleştirdiği bir binary'nin farklı çağrılma adları yine tek instance paylaşır ve
`process` öğeleri çözülmüş executable ile anahtarlanmaya devam eder.

`log_paths`, kuralın bildirdiği (`logs:`) log dosyası glob'larını listeler; her zaman vardır, boş olabilir. `logs.auto_from_discovery` true iken tail edilirler.

`instance` eşleşmeleri gruplar, ilk bulunan: servisin executable yolu (çözülmüş `/proc/<pid>/exe` hedefi; okunamıyorsa mutlak bir argv0), systemd unit adı, container id, paket anahtarı (`dpkg:redis-server`), listening port anahtarı.
İstisna: container içinde çalışan süreçler (`/proc/<pid>/cgroup`'tan container id) container'a göre gruplanır ve instance container id'sidir, çünkü container'lar executable yollarını paylaşır; eşleşen bir `container` envanter öğesi o instance'a katılır.
Exe'si okunamayan ama parent'ı bir instance'a ait olan süreçler o instance'a katılır.

### 3.5 Maskeleme kuralları (ajan tarafı, zorunlu) {#_3-5-masking-rules-agent-side-mandatory}

`cmdline` ve `exec_start` içinde değerler şunlar için `***` ile değiştirilir:
- `(?i)--?[A-Za-z0-9_.\-]*?(password|passwd|pass|pwd|secret|token|api[-_]?key|auth|credentials?)(=|\s+)\S+` ile eşleşen argümanlar: flag adı bir secret sözcüğüyle **bitmelidir**, yani `--requirepass x`, `--masterauth x` (Redis), `--db-password x` ve `-keypass x` maskelenir, `--passive yes`, `--auth-mode md5` ve `--token-file /path` okunabilir kalır
- doğrudan bitişik `-p<value>`: **yalnızca** executable basename'i `^(mysql|mysqldump|mysqladmin|mysqlimport|mysqlcheck|mysqlpump|mariadb.*)$` ile eşleştiğinde (yani `ssh -p22` ve `docker -p 8080:80` okunabilir kalır)
- anahtarı `(?i)(pass|secret|token|key|auth)` ile eşleşen `KEY=value` çiftleri, `file`, `path` veya `dir` ile biten anahtarlar **hariç** (örneğin `--keyfile=/etc/x.pem` okunabilir kalır)
- URL userinfo: `scheme://user:pass@` → `scheme://user:***@`

Ortam değişkenleri ve dosya içerikleri asla toplanmaz (log dosyaları yalnızca yapılandırıldığında toplanır, bkz. §4).

## 4. Log kayıtları (altyapı ajanı) {#_4-log-records-infra-agent}

Log satırları, logs sinyali üzerinden §1'in host resource'unda sıradan OTLP LogRecord'ları olarak gönderilir (container logları: bkz. §4.1). Asla `event.name` veya `openlog.inventory.*` attribute'u taşımazlar.

| Alan | Dosyalar | journald |
|---|---|---|
| `time_unix_nano` | ayarlanmaz (0) | `__REALTIME_TIMESTAMP` |
| `observed_time_unix_nano` | okunma zamanı | okunma zamanı |
| `severity_number` / `severity_text` | `logs.parse_severity` açıkken sezgisel (aşağıda); aksi halde ayarlanmaz | `PRIORITY`'den |
| `body` | string: tek satır veya `\n` ile birleştirilmiş tek çok satırlı kayıt; sondaki `\r` kaldırılır; geçersiz UTF-8, U+FFFD ile değiştirilir; en fazla `logs.max_line_bytes` | string `MESSAGE`, aynı limitler |

Attribute'lar:

| Attribute | Dosyalar | journald |
|---|---|---|
| `openlog.log.source` | `file` | `journald` |
| `log.file.path` | host yolu (`host.root_path` olmadan) | — |
| `log.file.name` | basename | — |
| `openlog.discovery.id` | yol, bulunan bir servisin `log_paths` glob'uyla eşleştiğinde `rule_id` | — |
| `openlog.systemd.unit` | — | `_SYSTEMD_UNIT` |
| `process.pid` | — | `_PID` (int) |
| `process.command` | — | `_COMM` |
| `openlog.syslog.identifier` | — | `SYSLOG_IDENTIFIER` |
| `openlog.log.truncated` | body `logs.max_line_bytes`'ta kesildiğinde `true` (bool) | aynı |
| *kullanıcı attribute'ları* | `logs.files[].attributes` | — |

`severity_text`, kanonik `TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR`, `FATAL` adlarını kullanır.

journald `PRIORITY` → `severity_number`: 0 emerg → 24 (FATAL4), 1 alert → 23 (FATAL3), 2 crit → 21 (FATAL), 3 err → 17 (ERROR), 4 warning → 13 (WARN), 5 notice → 10 (INFO2), 6 info → 9 (INFO), 7 debug → 5 (DEBUG).

Dosya severity sezgisi: ilk 128 bayttaki ilk seviye sözcüğü; boşluk, `[ ] " ' = : ( < | ,` ile ayrılır (`/` değil, yani URL yolları eşleşmez):
`emerg`/`emergency` → 24, `alert` → 23, `panic` → 22, `fatal`/`crit`/`critical` → 21, `err`/`error` → 17, `warn`/`warning` → 13, `notice` → 10, `info` → 9, `debug` → 5, `trace` → 1 (büyük/küçük harf duyarsız).
Örnekler: nginx `[error]`, Apache `[core:warn]`, PostgreSQL `FATAL:`, MySQL `[Warning]`, logfmt `level=info`, JSON `"level":"debug"`.

Maskeleme: log body'leri kullanıcı verisidir ve varsayılan olarak **maskelenmez**. `logs.mask_secrets: true` ile §3.5 desenleri (secret flag'leri, secret `KEY=value` token'ları, URL userinfo) body'lere uygulanır; MySQL `-p<value>` kuralı uygulanmaz (program bilinmiyor). JSON tarzı `"password": "x"` kapsanmaz.

Teslimat at-least-once'tır: dosya offset'leri ve journal cursor'ı, batch gönderildikten veya disk buffer'ına yazıldıktan sonra commit edilir, yani bir crash birkaç saniyeye kadar kaydı yeniden gönderebilir.

### 4.2 macOS unified log ve Windows Event Log (D-104) {#_4-2-macos-unified-log-and-windows-event-log-d-104}

| Alan / attribute | `logs.unified_log` (macOS) | `logs.windows_event_log` (Windows) |
|---|---|---|
| kaynak | `log stream --style ndjson --level <level> [--predicate <p>]`, backoff ile yeniden başlatılır; yalnızca canlı (kesinti sonrası backfill yok) | `event_ids`/`levels` (veya `query`) XPath sorgusuyla kanal başına `EvtSubscribe`; teslim edilen batch ile commit edilen bookmark'tan sonra sürer, yoksa `logs.start_at`'ten (`end` = gelecek olaylar, `beginning` = en eski kayıt) |
| `time_unix_nano` | `timestamp` | `System/TimeCreated/@SystemTime` |
| `severity_number` / `severity_text` | `messageType`: `Fault` → 21 FATAL, `Error` → 17 ERROR, `Default` → 10 INFO, `Info` → 9 INFO, `Debug` → 5 DEBUG | `Level`: 1 critical → 21 FATAL, 2 error → 17 ERROR, 3 warning → 13 WARN, 4 information ve 0 → 9 INFO, 5 verbose → 5 DEBUG |
| `body` | `eventMessage` (journald ile aynı limitler ve maskeleme) | render edilmiş mesaj (`EvtFormatMessage`); publisher metadata'sı yoksa `name=value` çiftleri olarak olay verisi |
| `openlog.log.source` | `unified_log` | `windows_event_log` |
| diğer attribute'lar | `openlog.macos.subsystem`, `openlog.macos.category`, `process.pid`, `process.executable.path`, `process.command` | `openlog.windows.event_log.channel`, `openlog.windows.event.id` (int), `openlog.windows.event.provider`, `openlog.windows.event.record_id` (int), `process.pid` |

Unified log'un yalnızca `logEvent` girdileri gönderilir (activity veya signpost yok). `Security` kanalını okumak LocalSystem veya Event Log Readers grubu gerektirir (servis LocalSystem olarak çalışır). Log dosyaları her işletim sisteminde tail edilir; Windows'ta rotasyon çalışmaya devam etsin diye `FILE_SHARE_DELETE` ile açılırlar ve dosya kimliği volume seri numarası artı dosya indeksidir.


### 4.1 Container logları (`logs.containers`) {#_4-1-container-logs-logs-containers}

Docker ve CRI (containerd, CRI-O) container'larının stdout/stderr'i, otomatik toplanır (varsayılan açık; `containers.enabled` gerektirir). Her container'ın kayıtları,
resource'u §1'in host resource'u artı §2 "Container metrikleri"nin container kimlik attribute'ları olan
ayrı bir `ResourceLogs`'tur (`container.id`, `container.name`, `container.image.name`, `container.image.tags`, `container.runtime`, `docker.compose.project`,
`docker.compose.service`, `k8s.*`).

| Alan | Değer |
|---|---|
| `time_unix_nano` | mesajın Docker zamanı |
| `observed_time_unix_nano` | okunma zamanı |
| `body` | sondaki yeni satır olmadan mesaj; Docker'ın 16 KiB'lık parçalara böldüğü mesajlar birleştirilir; dosyalarla aynı limitler, severity sezgisi ve maskeleme |
| `trace_id` / `span_id` | 32-hex `trace_id`, `traceId`, `traceID`, `trace.id`, `otelTraceID` veya `TraceId` alanı (hepsi sıfır değil) ve 16-hex `span_id`/`spanId`/`span.id`/`otelSpanID` alanı olan JSON nesne body'sinden (≤ 16 KiB) |
| `openlog.log.source` attribute'u | `container` |
| `log.iostream` attribute'u | `stdout` veya `stderr` (json-file girdisi olmayan satırlarda yok) |
| `openlog.log.truncated` attribute'u | dosyalardaki gibi |

Container başına kaynaklar (`logs.containers.source: auto`): ajan açabiliyorsa json-file log (`GET /containers/{id}/json`'dan `LogPath`, örneğin
`/var/lib/docker/containers/<id>/<id>-json.log`); dosyalar `0710` bir dizinde `root:root 0640` olduğundan bu
`CAP_DAC_READ_SEARCH` (systemd unit'i verir) veya root gerektirir; aksi halde Docker Engine API
`GET /containers/{id}/logs?follow=1&stdout=1&stderr=1&timestamps=1&since=…` (docker grubu). API ayrıca `local` ve `journald`
driver'larını ve Docker ≥ 20.10 dual logging ile çoğu uzak driver'ı sunar; okunamayan driver'lar (`none` veya dual logging'i
kapalı uzak driver'lar) backoff ile yeniden denenir (30 sn … 5 dk) ve bir kez loglanır. `source: file` / `api` tek bir yolu zorlar. API erişimi olmadan ajan
`logs.containers.docker_containers_dir/<id>/` dizinini listeler ve okunabiliyorsa `config.v2.json` okur (ad, image, label'lar, durum).

Seçim: çalışan, paused ve restarting container'lar, artı logu sonuna kadar okunana kadar durmuş container'lar (ajan başladıktan sonra
durmuşlardır veya kayıtlı bir konum vardır); önce çalışanlar, en fazla `max_containers`. Container label'ı `openlog.logs=false`
bir container'ı devre dışı bırakır; `exclude` sonra `include` (boş = hepsi), `name`, `image` (referans, tag'siz ad veya son yol parçası),
`compose_project`, `compose_service` ve `label` (`key` veya `key=value`) üzerinde `filepath.Match` glob'larıyla eşleşir, bir öğenin verilen tüm alanları eşleşmelidir.

Konumlar: json-file loglar §4'ün dosya offset'lerini kullanır (`<id>-json.log` → `.1` rotasyonu izlenir; ajan rotasyon sırasında kapalıysa
yeni dosyadan önce `<id>-json.log.1`'in geri kalanı okunur). İlk listelemede çalışan container'lar `logs.start_at`'ten başlar; sonradan
görülen container'lar baştan okunur, ajan başlangıcından eski (`start_at: end`) veya zaten okunmuş mesajlar atlanır. API stream'leri
container başına son teslim edilen kaydın Docker zamanını durum dosyasına kaydeder ve ondan sonra sürer. Container başına rate limit
`rate_limit_lines` (backpressure: dosya okunmamış veriyi tutar, stream bloklanır).

CRI container'ları (`container.runtime` `containerd` / `cri-o`, `containers.cri_sockets` üzerinden listelenir): log dosyası `ContainerStatus`'un mutlak
`log_path`'idir (kubelet: `/var/log/pods/<ns>_<pod>_<uid>/<container>/<restart>.log`, `CAP_DAC_READ_SEARCH` veya root gerektirir),
CRI log biçiminde `<RFC3339Nano> <stdout|stderr> <F|P> <message>`; `P` (partial) satırlar aynı stream'in sonraki satırlarıyla `F` satırına kadar
birleştirilir, zaman ilk parçanınkidir. API'ye dönüş yoktur (`source: api` onlar için hiçbir şey okumaz);
biçime uymayan satırlar `log.iostream` olmadan olduğu gibi gönderilir. Ajan kapalıyken olan rotasyon izlenmez
(kubelet'in `<n>.log.<timestamp>` dosyaları okunmaz).

Çok satırlı gruplama: başlangıç deseni olmadan `logs.join_continuations` (varsayılan `false`) bir stack trace'i şekline göre gruplar: boşlukla,
`at …`, `Caused by:` veya `... N more` ile başlayan satır önceki kaydı sürdürür, diğer her satır onu bitirir. Varsayılan olarak kapalıdır,
çünkü bir kaydı sonraki satıra kadar tutmak teslimata bir saniyeye kadar gecikme ekler ve bir container'ın stdout'unu
stderr'ine göre yeniden sıralayabilir; yapılandırılmış bir desen de aynı bedeli bilerek öder.

Çok satırlı gruplama: bir container'ın satırları, başlangıç deseni ayarlıysa çok satırlı kayıtlara gruplanır: container label'ı
`openlog.logs.multiline=<regex>` (kazanır), yoksa eşleşen ilk `include` öğesinin `multiline_start` değeri. Dosyalardaki gibi desenle
eşleşen satır bir kayıt başlatır ve sonraki eşleşmeyen satırlar `\n` ile eklenir (ilk başlangıç satırından önceki satırlar tek başına gönderilir).
Gruplama stream başınadır (stdout ve stderr ayrı), Docker/CRI kısmi mesajları birleştirildikten sonra; kayıt, stream'inin sonraki başlangıç
satırı geldiğinde, stream'inde 2 sn yeni satır olmadığında veya log bittiğinde gönderilir; en fazla `logs.max_line_bytes`
(`openlog.log.truncated=true`). `time_unix_nano` ilk satırın zamanıdır; kayıtlı konum (dosya offset'i / stream zamanı) gönderilmemiş bir kaydı asla
geçmez. Geçersiz bir label deseni o container için gruplamayı kapatır (bir kez loglanır).

## 5. Altyapı ajanı yapılandırma anahtarları (M1 eklemeleri) {#_5-infra-agent-configuration-keys-m1-additions}

| Anahtar | Varsayılan | Anlamı |
|---|---|---|
| `process_metrics.enabled` | `true` | süreç başına metrikler (§2) |
| `process_metrics.top_n_cpu` / `top_n_memory` | `20` / `20` | top-N boyutları; birleşim raporlanır |
| `host.cloud_metadata` | `auto` | `auto` = §1'in cloud instance bilgileri için başlangıçta bir kez instance metadata servisine sor; `off` = asla yoklama |
| `containers.enabled` | `true` | container envanteri ve metrikleri |
| `containers.docker_socket` | `/var/run/docker.sock` | `host.root_path` altında host yolu; yedek olarak `/run/docker.sock` denenir |
| `containers.cri_sockets` | `[/run/containerd/containerd.sock, /run/k3s/containerd/containerd.sock, /var/run/crio/crio.sock]` | containerd / CRI-O'nun CRI (`runtime.v1`) socket'leri, `host.root_path` altında host yolları (`/var/run/…`, `/run/…` olarak da denenir); yanıt veren her socket'in container'ları Docker'ınkilere eklenir (id ile birleşim, Docker kazanır); Docker çalışırken CRI hataları yok sayılır; CRI servisi olmayan socket'e 5 dk boyunca tekrar sorulmaz; `[]` kapatır |
| `logs.enabled` | `true` | ana şalter; dosyalar, journald veya otomatik discovery yapılandırılmadıkça hiçbir şey okunmaz |
| `logs.files[]` | `[]` | `path` (mutlak glob, `filepath.Match` sözdizimi, `**` yok), `exclude` (yol ve basename ile eşleştirilen glob'lar), `multiline_start` (kaydın ilk satırının regex'i), `attributes` (map) |
| `logs.auto_from_discovery` | `false` | bulunan servislerin `log_paths` değerlerini tail et |
| `logs.journald.enabled` | `false` | `journalctl -o export -f` çalıştır |
| `logs.journald.journalctl_path` / `units` / `priority` | `journalctl` / `[]` / `""` | binary, `--unit` filtreleri, `--priority` (emerg..debug veya 0..7) |
| `logs.start_at` | `end` | başlangıçta ilk görülen dosyaların (ve kayıtlı cursor'ı olmayan journald'ın) başladığı yer: `end` veya `beginning`; sonradan beliren dosyalar her zaman baştan okunur |
| `logs.max_line_bytes` | `65536` | daha uzun satırlar/kayıtlar kesilir, satırın geri kalanı atlanır |
| `logs.rate_limit_lines` | `2000` | dosya başına saniyede satır (0 = sınırsız); fazlası dosyada kalır (backpressure, düşme yok) |
| `logs.parse_severity` | `true` | dosya severity sezgisi |
| `logs.mask_secrets` | `false` | log body'lerine §3.5 maskelemesini uygula |
| `logs.poll_interval` | `1s` | dosya poll ve batch flush aralığı |
| `logs.containers.enabled` | `true` | container logları (§4.1); `containers.enabled` gerektirir |
| `logs.containers.source` | `auto` | `auto`, `file` (yalnızca json-file loglar) veya `api` (yalnızca Docker Engine API) |
| `logs.containers.max_containers` | `100` | aynı anda okunan container'lar (1–1000) |
| `logs.containers.rate_limit_lines` | `1000` | container başına saniyede satır (0 = sınırsız) |
| `logs.containers.include[]` / `exclude[]` | `[]` | `{name, image, compose_project, compose_service, label}` glob'ları; `openlog.logs=false` label'ı her zaman hariç tutar; `include` öğeleri `multiline_start` ayarlayabilir (regex; `openlog.logs.multiline` label'ı kazanır, §4.1); include listesi container seçer, yani diğerlerini toplamaya devam etmek için `{name: "*"}` ekle |
| `logs.containers.docker_containers_dir` | `/var/lib/docker/containers` | Docker API erişimi olmadan json-file loglar (`host.root_path` altında host yolu) |

## 6. Entegrasyonlar (altyapı ajanı, M2, D-031) {#_6-integrations-infra-agent-m2-d-031}

Entegrasyonlar bulunan servisler için servis metrikleri toplar (§3.4). Kuralı implemente edilmiş bir `integration.id` bildiren her `discovered_service` için bir instance
vardır; servis bulunduğunda başlar, kaybolduğunda durur (envanter snapshot sıklığı, host change
fingerprint'i). Her instance kendi takvimiyle toplar (`integrations.interval`, varsayılan 30 sn, ilk toplama 2 sn içinde), toplama başına
timeout (`integrations.timeout`, 10 sn), aynı anda en fazla `integrations.max_concurrent` toplama ve başarısızlıklardan sonra üstel backoff
(interval · 2ⁿ, en fazla 5 dk). Her instance'ın son örneği sonraki metrik payload'ıyla gönderilir. Metrik adları, türleri, birimleri ve attribute anahtarları
OpenTelemetry Collector contrib receiver'larınınkidir (`nginxreceiver`, `redisreceiver`, `mysqlreceiver`, `postgresqlreceiver`, `main`'deki
`metadata.yaml` dosyalarına göre kontrol edildi, 2026-09-13; `sqlserverreceiver` ve `iisreceiver` 2026-09-15'te), yani bu receiver'lara sahip bir OTel Collector'ın
topladığı veri aynı panellerle eşleşir.
Receiver'da varsayılan olarak kapalı olup openlog'un ürettiği metrikler *(OTel'de opt-in)* olarak işaretlidir.

### 6.1 Resource {#_6-1-resource}

Her instance (ve §6.5 PostgreSQL'in her entity'si) ayrı bir OTLP `ResourceMetrics`'tir. Attribute'ları §1'in tüm host attribute'ları, ardından:

| Attribute | Değer |
|---|---|
| `openlog.discovery.id` | bulunan servisin `rule_id`'si (örneğin `mariadb`) |
| `openlog.discovery.instance` | bulunan servisin `instance`'ı; `<openlog.discovery.id>:<openlog.discovery.instance>`, `discovered_service` anahtarıdır (§3.4) |
| `openlog.integration.id` | `nginx`, `redis`, `mysql`, `postgresql`, `mssql`, `iis` |
| `service.instance.id` | endpoint'in `host:port` değeri; loopback host'lar `host.name` ile değiştirilir (`web-1:6379`); unix socket'ler: `<host.name>:<socket path>`. MySQL bunu receiver'ın UUIDv5'iyle ezer (§6.4) |
| `server.address` | endpoint host'u (dial edildiği gibi IP, örneğin `127.0.0.1`, `172.18.0.5`) veya unix socket'ler için socket yolu |
| `server.port` | int, endpoint portu (unix socket'lerde yok) |
| entegrasyon attribute'ları | entegrasyon bölümlerine bak (`redis.version`, `mysql.instance.endpoint`, `postgresql.database.name`, …) |

Instrumentation scope: `name` = `openlog-infra-agent/integrations/<id>`, `version` = ajan sürümü.
Kümülatif sum'lar Redis ve MySQL için `start_time_unix_nano` = sunucu başlangıcı (now − uptime) taşır; nginx ve PostgreSQL sum'ları başlangıç zamanı taşımaz.
Resource `service.name` **ayarlamaz** (backend `service_name` sütunu entegrasyon metrikleri için boş kalır).
Arayüz panelleri için join: `host.id` + `openlog.discovery.id` + `openlog.discovery.instance` (servis kartı) veya `openlog.integration.id` (panel türü).

Endpoint türetme (sırayla, en fazla 8 aday, ilk yanıt veren kullanılır ve sonrasında tercih edilir):
1. yapılandırmadaki `endpoint` (tek aday);
2. servisin TCP listening portları (önce entegrasyonun varsayılan portu; `0.0.0.0` → `127.0.0.1`, `::` → `::1`; protokol dışı portlar atlanır: MySQL 33060/33062, Redis 16379);
3. `host.root_path` altında var olan bilinen unix socket'ler (Redis `/run/redis/redis-server.sock`, …; MySQL `/run/mysqld/mysqld.sock`, `/var/lib/mysql/mysql.sock`, …; PostgreSQL `/var/run/postgresql/.s.PGSQL.5432`, …);
4. container servisleri: `127.0.0.1:<public port>` üzerinde yayınlanan portlar, sonra container'ın private portları ve varsayılan portla container IP adresleri (Docker `NetworkSettings`).
   Docker Engine API olmadan (socket yok veya `permission denied`) ajan aynı veriyi host'tan türetir: süreç cgroup'larında bulunan her container id'si için
   container'ın network namespace'ini `/proc/<pid>/net/tcp{,6}` (container süreçlerinin tuttuğu listening TCP portları; loopback'e bağlı socket'ler atlanır) ve
   `/proc/<pid>/net/fib_trie` (yerel, loopback olmayan IPv4 adresleri) üzerinden okur ve yayınlanan portları
   `docker-proxy -host-ip … -host-port … -container-ip … -container-port …` komut satırlarından eşler. Host network namespace'indeki container'lar atlanır
   (portları host listening portlarıdır, 2. adım);
5. yalnızca hiç aday bulunamazsa: `127.0.0.1` üzerinde entegrasyonun varsayılan portu (nginx 80, Redis 6379, MySQL 3306, PostgreSQL 5432).

### 6.2 Yapılandırma anahtarları {#_6-2-configuration-keys}

| Anahtar | Varsayılan | Anlamı |
|---|---|---|
| `integrations.enabled` | `true` | ana şalter (`discovery.enabled` gerektirir) |
| `integrations.interval` / `timeout` | `30s` / `10s` | varsayılan toplama aralığı (≥ 5 sn) ve toplama başına timeout (≤ aralık) |
| `integrations.max_concurrent` / `max_instances` | `4` / `32` | eşzamanlılık limiti; instance limiti (sonraki instance'lar: `not_available`) |
| `integrations.<id>.enabled` | `true` | entegrasyon başına (`nginx`, `apache`, `redis`, `memcached`, `mysql`, `postgresql`, `mongodb`, `docker`, `mssql`, `iis`, `haproxy`, `rabbitmq`, `elasticsearch`, `jvm`, `kafka`) |
| `integrations.<id>.interval` | — | varsayılan aralığı ezer |
| `integrations.<id>.endpoint` | — | `host:port`, `unix:/path` (redis, mysql, postgresql, memcached; haproxy: runtime socket'i) veya `http(s)://…` URL'i (nginx: stub_status sayfası, NGINX Plus API `/api/` veya `/api/<n>` ya da VTS JSON sayfası, biçim tespit edilir, §6.3; apache: mod_status sayfası §6.10; haproxy: stats sayfası §6.12; rabbitmq: management API §6.13; elasticsearch: REST API §6.14) |
| `integrations.<id>.username` / `password` | — | redis, mysql, postgresql, mssql (SQL Server kimlik doğrulaması; Windows kimlik doğrulaması desteklenmez), rabbitmq, elasticsearch (HTTP basic auth). `password`: `env:NAME`, `file:/abs/path` (sondaki yeni satır kaldırılır; her bağlantıda yeniden okunur) veya literal (başlangıç uyarısı) |
| `integrations.<id>.tls` | — | `{enabled, insecure_skip_verify, ca_file, server_name}`; `tls` olmadan PostgreSQL TCP üzerinde `sslmode=prefer` kullanır |
| `integrations.postgresql.database` | `postgres` | ilk veritabanı |
| `integrations.mongodb.database` | `admin` | kimlik doğrulama kaynağı, izlenecek veritabanı değil (§6.15) |
| `integrations.mongodb.databases` / `exclude_databases` | `[]` | hangi veritabanlarının `dbStats` alacağı (en fazla 32) |
| `integrations.postgresql.databases` / `exclude_databases` | `[]` | veritabanı izin/ret listeleri (varsayılan: `datallowconn` olan her template olmayan veritabanı, en fazla 32) |
| `integrations.{mysql,postgresql}.top_n_tables` | `50` / `20` | cardinality koruması: en büyük tablolar/index'ler (PostgreSQL, veritabanı başına) veya en çok io wait süresi olan tablolar/index'ler (MySQL) |
| `integrations.mssql.top_n_tables` | `10` | `sqlserver.os.wait.duration` içinde toplam bekleme süresi en çok olan wait türleri (§6.7) |
| `integrations.postgresql.query_stats` | `{enabled: false, top_n: 20, min_calls: 0}` | opt-in `pg_stat_statements` en üst statement'lar (§6.5): toplam çalışma süresine göre `top_n` 0..100 (0 = 20), `min_calls` daha az çağrısı olan statement'ları atlar. Yalnızca `config.yaml` (uzak entegrasyon yapılandırmasının parçası değil) |
| `integrations.<id>.instances[]` | `[]` | verilen **tüm** `match` alanlarıyla eşleşen servisler için override'lar: `port` (listening, private veya published), `endpoint` (türetilen aday), `unit`, `container` (ad veya ≥ 12 karakterlik id öneki), `instance`; artı yukarıdaki herhangi bir ayar ve `enabled` |
| `integrations.remote_config` | `true` | openlog arayüzünde yapılandırılan entegrasyon ayarlarını uygula (ajan sync'iyle iletilir, [releases-updates.md](/tr/contracts/releases-updates) §3, D-039). `false`: yok sayılır; ajan `disabled` revizyonunu bildirir |

Bilinmeyen anahtarlar ve bir entegrasyonun desteklemediği ayarlar (örneğin `integrations.nginx.password`) yapılandırma hatasıdır. Kimlik bilgileri asla
loglanmaz, envantere, durumlara veya metriklere asla dahil edilmez ve ajan tarafından backend'e asla gönderilmez.

**Uzak entegrasyon yapılandırması** (D-039): sync yanıtının `integrations_config` öğeleri sırayla `config.yaml` üzerine birleştirilir; boş olmayan her alanda uzak değerler
kazanır. `match` içermeyen bir öğe entegrasyon varsayılanlarını değiştirir (`enabled` dahil `integrations.<id>.*`); `match` içeren bir öğe
(`port`, `container`, `endpoint`, `instance`) aynı match'e sahip `config.yaml` instance'ını günceller veya yapılandırılmış
instance'lardan önce eklenir. Uzak parolalar literal değerlerdir (asla `env:`/`file:` referansı değil). Geçersiz öğeler (örneğin URL olmayan bir nginx endpoint'i) uyarıyla
atlanır. Son alınan yapılandırma `<state_dir>/integrations-remote.json` dosyasına kaydedilir (mod 0600, parola içerir) ve bir sonraki
başlangıçta backend yanıtlamadan önce uygulanır. Değişiklikler yeniden başlatma olmadan uygulanır: endpoint'i, kimlik bilgileri, veritabanı ayarları veya enabled durumu
değişen instance'lar yeniden başlatılır. Toplama başına 20 000 data point'lik kesin bir üst sınır geçerlidir;
düşürülen noktalar toplamayı kısmi yapar (`error`: `cardinality guard: N data points dropped`).

### 6.3 nginx (`stub_status`, NGINX Plus API, VTS) {#_6-3-nginx-stub-status-nginx-plus-api-vts}

Kaynaklar: `ngx_http_stub_status_module` sayfası, NGINX Plus API (`api` direktifi) veya nginx-module-vts JSON sayfası. `auto_enable: true`
(nginx kuralı) ile ve yapılandırılmış `endpoint` yoksa, bu yollar her aday endpoint üzerinde `http`, sonra `https` ile (sertifikalar yalnızca loopback
endpoint'lerde doğrulanmaz) şu sırayla yoklanır: `/api/` (Plus), `/status/format/json`, `/vts/format/json`, `/vts_status/format/json` (VTS),
`/nginx_status`, `/stub_status`, `/basic_status`, `/status`, `/server_status` (stub_status). Gövdesi şu biçimlerden biri olarak tespit edilen ilk `200` sayfası
kullanılır: API sürümlerinin JSON dizisi (Plus kökü, en yüksek sürüm kullanılır) veya `connections` ve `http` içeren endpoint adlarının dizisi
(sürümlü Plus kökü, örneğin yapılandırılmış `…/api/9`), yalnızca `<api>/connections` yanıt verirse kabul edilir; `connections` ve
`serverZones` içeren bir JSON nesnesi (VTS); bir `stub_status` metin sayfası. Yapılandırılmış bir `endpoint` URL'i aynı şekilde tespit edilir. Bulunan URL instance başına
hatırlanır ve instance bir hatadan sonra yeniden yokladığında önce denenir (instance backoff'uyla). Hiçbir aday sayfa sunmuyorsa status,
stub_status'un etkin olmadığını söyleyen ve nginx `location` parçalarını (stub_status, Plus `api`, VTS) gösteren bir hint ile `needs_configuration` olur;
ajan nginx yapılandırmasını asla düzenlemez. Kimlik bilgisi yok.

Resource attribute'u (openlog eklentisi): `nginx.status.source` = `stub_status`, `plus` veya `vts`.

| Metrik | Tür | Birim | Attribute'lar |
|---|---|---|---|
| `nginx.requests` | Sum, cumulative, monotonic, int | `{requests}` | — |
| `nginx.connections_accepted` | Sum, cumulative, monotonic, int | `{connections}` | — |
| `nginx.connections_handled` | Sum, cumulative, monotonic, int | `{connections}` | — |
| `nginx.connections_current` | Sum, cumulative, non-monotonic, int | `{connections}` | `state` = `active`, `reading`, `writing`, `waiting` |

Kaynağa göre temel metrikler: stub_status ve VTS (`connections` nesnesi) yukarıdakilerin hepsini sağlar. Plus: `nginx.requests` = `http/requests.total`,
`connections_accepted` = `connections.accepted`, `connections_handled` = `accepted − dropped`, `connections_current` yalnızca `state` = `active`
(`active + idle`, çünkü stub_status boştaki keep-alive bağlantıları sayar) ve `waiting` (`idle`); Plus'ta reading/writing durumları yoktur.

Zone ve upstream metrikleri (yalnızca Plus ve VTS; birimleri dahil NGINX ajanının `nginxplusreceiver`'ına göre adlandırılmış openlog eklentileri, `v3`'teki `metadata.yaml`,
2026-09-14). Plus `/api/<n>/connections`, `/http/requests` (zorunlu), `/http/server_zones` ve `/http/upstreams` okur
(son ikisinin başarısızlığı toplamayı kısmi yapar). VTS: `*` toplam zone'u hariç `serverZones`, `upstreamZones`
(`::nogroups` dahil). Cardinality: en çok isteği olan 50 server zone ve 100 upstream peer.

| Metrik | Tür | Birim | Attribute'lar | Kaynaklar |
|---|---|---|---|---|
| `nginx.http.requests` | Sum, cumulative, monotonic, int | `requests` | `nginx.zone.name`, `nginx.zone.type` = `SERVER` | Plus (`requests`), VTS (`requestCounter`) |
| `nginx.http.response.status` | Sum, cumulative, monotonic, int | `responses` | `nginx.status_range` = `1xx`…`5xx`, `nginx.zone.name`, `nginx.zone.type` | Plus, VTS (`responses`) |
| `nginx.http.upstream.peer.requests` | Sum, cumulative, monotonic, int | `requests` | `nginx.peer.address`, `nginx.peer.name`, `nginx.upstream.name`, `nginx.zone.name` (yalnızca Plus) | Plus, VTS (`server` ad ve adrestir) |
| `nginx.http.upstream.peer.responses` | Sum, cumulative, monotonic, int | `responses` | peer attribute'ları + `nginx.status_range` | Plus, VTS |
| `nginx.http.upstream.peer.fails` | Sum, cumulative, monotonic, int | `attempts` | peer attribute'ları | Plus |
| `nginx.http.upstream.peer.state` | Gauge, int | `is_deployed` | peer attribute'ları + `nginx.peer.state` = `UP`, `DOWN`, `DRAINING`, `UNAVAILABLE`, `UNHEALTHY`, `CHECKING` (geçerli durum için değer 1) | Plus |

5xx oranı alarmı: `rate(nginx.http.response.status{nginx.status_range="5xx"})` / `rate(sum by zone of nginx.http.response.status)`.

### 6.4 Redis (`INFO`) ve MySQL/MariaDB {#_6-4-redis-info-and-mysql-mariadb}

**Redis** — TCP, TLS veya unix socket üzerinden `INFO` (varsayılan bölümler); yapılandırılmışsa `AUTH <password>` veya `AUTH <username> <password>`.
Gereksinim: `requirepass` olmadan hiçbir şey; ACL'lerle kullanıcının `+info` izni olmalıdır (örneğin `ACL SETUSER openlog on >… +info +ping`).
Resource attribute'u: `redis.version` (`redis_version`, yoksa `unknown`).

| Metrik | Tür | Birim | Attribute'lar | INFO alanı |
|---|---|---|---|---|
| `redis.clients.connected` | Sum, non-monotonic, int | `{client}` | — | `connected_clients` |
| `redis.clients.blocked` | Sum, non-monotonic, int | `{client}` | — | `blocked_clients` |
| `redis.clients.max_input_buffer` | Gauge, int | `By` | — | `client_recent_max_input_buffer` |
| `redis.clients.max_output_buffer` | Gauge, int | `By` | — | `client_recent_max_output_buffer` |
| `redis.commands` | Gauge, int | `{ops}/s` | — | `instantaneous_ops_per_sec` |
| `redis.commands.processed` | Sum, monotonic, int | `{command}` | — | `total_commands_processed` |
| `redis.connections.received` | Sum, monotonic, int | `{connection}` | — | `total_connections_received` |
| `redis.connections.rejected` | Sum, monotonic, int | `{connection}` | — | `rejected_connections` |
| `redis.cpu.time` | Sum, monotonic, double | `s` | `state` = `sys`, `sys_children`, `sys_main_thread`, `user`, `user_children`, `user_main_thread` | `used_cpu_*` |
| `redis.db.keys` | Gauge, int | `{key}` | `db` (`"0"`, `"1"`, …) | `db<N>: keys` |
| `redis.db.expires` | Gauge, int | `{key}` | `db` | `db<N>: expires` |
| `redis.db.avg_ttl` | Gauge, int | `ms` | `db` | `db<N>: avg_ttl` |
| `redis.keys.evicted` | Sum, monotonic, int | `{key}` | — | `evicted_keys` |
| `redis.keys.expired` | Sum, monotonic, int | `{event}` | — | `expired_keys` |
| `redis.keyspace.hits` | Sum, monotonic, int | `{hit}` | — | `keyspace_hits` |
| `redis.keyspace.misses` | Sum, monotonic, int | `{miss}` | — | `keyspace_misses` |
| `redis.latest_fork` | Gauge, int | `us` | — | `latest_fork_usec` |
| `redis.memory.used` / `.peak` / `.rss` / `.lua` | Gauge, int | `By` | — | `used_memory`, `used_memory_peak`, `used_memory_rss`, `used_memory_lua` |
| `redis.memory.fragmentation_ratio` | Gauge, double | `1` | — | `mem_fragmentation_ratio` |
| `redis.maxmemory` *(OTel'de opt-in)* | Gauge, int | `By` | — | `maxmemory` |
| `redis.net.input` / `redis.net.output` | Sum, monotonic, int | `By` | — | `total_net_input_bytes`, `total_net_output_bytes` |
| `redis.rdb.changes_since_last_save` | Sum, non-monotonic, int | `{change}` | — | `rdb_changes_since_last_save` |
| `redis.replication.offset` | Gauge, int | `By` | — | `master_repl_offset` |
| `redis.replication.backlog_first_byte_offset` | Gauge, int | `By` | — | `repl_backlog_first_byte_offset` |
| `redis.replication.replica_offset` *(OTel'de opt-in)* | Gauge, int | `By` | — | `slave_repl_offset` (replikalar) |
| `redis.role` *(OTel'de opt-in)* | Sum, non-monotonic, int | `{role}` | `role` = `primary`, `replica` (değer 1) | `role` |
| `redis.slaves.connected` | Sum, non-monotonic, int | `{replica}` | — | `connected_slaves` |
| `redis.uptime` | Sum, monotonic, int | `s` | — | `uptime_in_seconds` |

**Redis Cluster** — `INFO` `cluster_enabled:1` raporladığında ajan ayrıca `CLUSTER INFO` ve `CLUSTER NODES` çalıştırır (ACL: `+cluster|info +cluster|nodes`;
bunlar olmadan INFO metrikleri korunur ve toplama hint ile kısmi olur). Her cluster node'u kendi instance'ıdır, yani yukarıdaki INFO metrikleri
node başınadır. Resource attribute'u: `redis.cluster.node.id` (`CLUSTER NODES`'un `myself` satırının id'si). Adlar, türler ve birimler
redisreceiver'ın `metadata.yaml` dosyasındakilerdir (hepsi *OTel'de opt-in*; receiver'ın kendisi yalnızca `redis.cluster.cluster_enabled`'ı doldurur). Üretilmez:
`redis.cluster.uptime` / `redis.cluster.node.uptime` (birimi `s` olarak tanımlı ama anlamı cluster/node epoch'u).

| Metrik | Tür | Birim | Attribute'lar | Alan |
|---|---|---|---|---|
| `redis.cluster.cluster_enabled` | Gauge, int | `1` | — | INFO `cluster_enabled` (her sunucu, `0` veya `1`) |
| `redis.cluster.state` | Gauge, int | `{state}` | `cluster_state` = `ok` (değer 1), `fail` (değer 0) | `cluster_state` |
| `redis.cluster.slots_assigned` / `slots_ok` / `slots_pfail` / `slots_fail` | Gauge, int | `{slot}` | — | `cluster_slots_assigned`, `_ok`, `_pfail`, `_fail` |
| `redis.cluster.known_nodes` | Gauge, int | `{node}` | — | `cluster_known_nodes` |
| `redis.cluster.node.count` | Gauge, int | `{node}` | — | `cluster_size` (slot sunan master'lar) |
| `redis.cluster.stats_messages_sent` / `stats_messages_received` | Sum, monotonic, int | `{message}` | — | `cluster_stats_messages_sent`, `_received` |
| `redis.cluster.links_buffer_limit_exceeded.count` | Sum, monotonic, int | `{count}` | — | `total_cluster_links_buffer_limit_exceeded` |

**MySQL / MariaDB** (`mysql` ve `mariadb` kuralları, entegrasyon id'si `mysql`) — native protokol, `mysql_native_password` ve
`caching_sha2_password` (TLS olmadan RSA anahtar değişimi). Sorgular: `SHOW GLOBAL STATUS`, `SELECT @@innodb_buffer_pool_size`,
top-N `performance_schema.table_io_waits_summary_by_table` / `…_by_index_usage` (`SUM_TIMER_WAIT` ile sıralı), `SHOW REPLICA STATUS`
(eski sunucularda `SHOW SLAVE STATUS`). Gereken yetkiler: `PROCESS`, `REPLICATION CLIENT` (MariaDB ≥ 10.5.9: replika durumu için `REPLICA MONITOR` veya `SLAVE MONITOR`),
`SELECT ON performance_schema.*`. Eksik performance_schema/replika yetkileri toplamayı kısmi yapar;
`performance_schema = OFF` da öyle (MariaDB varsayılanı; io wait tabloları vardır ama boş kalır), etkinleştirmek için hint ile. io wait'ler: `mysql`,
`performance_schema`, `information_schema`, `sys` şemaları hariç; eşitlikler şema, tablo (, index) ile sıralanır; sayılar `COUNT_*`'tan, süreler
işlem başına `FLOOR(SUM_TIMER_*/1000)` (pikosaniye → ns). mysqlreceiver'ın index sorgusu `FETCH, INSERT, UPDATE, DELETE` seçer ama onları
`delete, fetch, insert, update` olarak tarar; openlog doğru işlem label'larını üretir.
Resource attribute'ları: `mysql.instance.endpoint` (endpoint görüntü biçimi, örneğin `127.0.0.1:3306`, `unix:/run/mysqld/mysqld.sock`);
`service.instance.id` = §6.1 `host:port` değerinin UUIDv5'i (namespace `4d63009a-8d0f-11ee-aad7-4c796ed8e320`), mysqlreceiver gibi.

Tüm MySQL metrikleri int'tir. Attribute anahtarları receiver'ın `name_override` değerleridir.

| Metrik | Tür | Birim | Attribute'lar (değerler) |
|---|---|---|---|
| `mysql.buffer_pool.data_pages` | Sum, non-monotonic | `1` | `status` = `dirty`, `clean` (pages_data − pages_dirty) |
| `mysql.buffer_pool.limit` | Sum, non-monotonic | `By` | — |
| `mysql.buffer_pool.operations` | Sum, monotonic | `1` | `operation` = `read_ahead_rnd`, `read_ahead`, `read_ahead_evicted`, `read_requests`, `reads`, `wait_free`, `write_requests` |
| `mysql.buffer_pool.page_flushes` | Sum, monotonic | `1` | — |
| `mysql.buffer_pool.pages` | Sum, non-monotonic | `1` | `kind` = `data`, `free`, `misc`, `total` (aralık dışındaysa `misc` atlanır) |
| `mysql.buffer_pool.usage` | Sum, non-monotonic | `By` | `status` = `dirty`, `clean` |
| `mysql.commands` *(OTel'de opt-in)* | Sum, monotonic | `1` | `command` = `alter_table`, `create_index`, `create_table`, `delete`, `delete_multi`, `insert`, `optimize`, `select`, `update`, `update_multi` |
| `mysql.connection.count` *(OTel'de opt-in)* | Sum, monotonic | `1` | — (`Connections`) |
| `mysql.connection.errors` *(OTel'de opt-in)* | Sum, monotonic | `1` | `error` = `accept`, `internal`, `max_connections`, `peer_address`, `select`, `tcpwrap`, `aborted`, `aborted_clients`, `locked` |
| `mysql.double_writes` | Sum, monotonic | `1` | `kind` = `pages_written`, `writes` |
| `mysql.handlers` | Sum, monotonic | `1` | `kind` = `commit`, `delete`, `discover`, `external_lock`, `mrr_init`, `prepare`, `read_first`, `read_key`, `read_last`, `read_next`, `read_prev`, `read_rnd`, `read_rnd_next`, `rollback`, `savepoint`, `savepoint_rollback`, `update`, `write` |
| `mysql.index.io.wait.count` | Sum, monotonic | `1` | `operation` = `delete`, `fetch`, `insert`, `update`; `table`, `schema`, `index` (index yoksa `NONE`) |
| `mysql.index.io.wait.time` | Sum, monotonic | `ns` | aynı |
| `mysql.locks` | Sum, monotonic | `1` | `kind` = `immediate`, `waited` |
| `mysql.log_operations` | Sum, monotonic | `1` | `operation` = `waits`, `write_requests`, `writes`, `fsyncs` |
| `mysql.max_used_connections` *(OTel'de opt-in)* | Sum, non-monotonic | `1` | — |
| `mysql.mysqlx_connections` | Sum, monotonic | `1` | `status` = `accepted`, `closed`, `rejected` (yalnızca MySQL) |
| `mysql.opened_resources` | Sum, monotonic | `1` | `kind` = `file`, `table`, `table_definition` |
| `mysql.operations` | Sum, monotonic | `1` | `operation` = `fsyncs`, `reads`, `writes` |
| `mysql.page_operations` | Sum, monotonic | `1` | `operation` = `created`, `read`, `written` |
| `mysql.prepared_statements` | Sum, monotonic | `1` | `command` = `execute`, `close`, `fetch`, `prepare`, `reset`, `send_long_data` |
| `mysql.query.count` / `mysql.query.client.count` / `mysql.query.slow.count` *(OTel'de opt-in)* | Sum, monotonic | `1` | — (`Queries`, `Questions`, `Slow_queries`) |
| `mysql.replica.time_behind_source` *(OTel'de opt-in)* | Sum, non-monotonic | `s` | — (yalnızca replikalar; `NULL` iken yok) |
| `mysql.replica.sql_delay` *(OTel'de opt-in)* | Sum, non-monotonic | `s` | — (yalnızca replikalar) |
| `mysql.row_locks` | Sum, monotonic | `1` | `kind` = `waits`, `time` (`Innodb_row_lock_time`, raporlandığı gibi ms) |
| `mysql.row_operations` | Sum, monotonic | `1` | `operation` = `deleted`, `inserted`, `read`, `updated` |
| `mysql.sorts` | Sum, monotonic | `1` | `kind` = `merge_passes`, `range`, `rows`, `scan` |
| `mysql.table.io.wait.count` | Sum, monotonic | `1` | `operation`, `table`, `schema` |
| `mysql.table.io.wait.time` | Sum, monotonic | `ns` | `operation`, `table`, `schema` |
| `mysql.threads` | Sum, non-monotonic | `1` | `kind` = `cached`, `connected`, `created`, `running` |
| `mysql.tmp_resources` | Sum, monotonic | `1` | `resource` = `disk_tables`, `files`, `tables` |
| `mysql.uptime` | Sum, monotonic | `s` | — |

### 6.5 PostgreSQL (`pg_stat_*`) {#_6-5-postgresql-pg-stat}

pgx protokol istemcisi (`sslmode=prefer`, simple query protocol, `application_name=openlog-infra-agent`). `database` ile tek bağlantı tutulur;
veritabanı başına bağlantılar her toplama için açılır ve kapatılır. Gereksinim: `GRANT pg_monitor TO openlog` (veya `pg_stat_*`
view'larında `SELECT` artı replikasyon gecikmesi için `pg_read_all_stats`) ve toplanan veritabanlarında `CONNECT`. Başarısız veritabanı başına sorgular
toplamayı kısmi yapar. Sorgular postgresqlreceiver'ı izler (verilmiş `AccessExclusiveLock` tutan ilişkiler atlanır). Resource'lar receiver'ın
varsayılan modunu izler (`separateSchemaAttr` ve `useOTelSemconv` feature gate'leri kapalı): sunucu metrikleri instance resource'unda (yalnızca §6.1), sonra
veritabanı, tablo ve index başına birer resource, şu ek attribute'larla:

| Resource | Ek attribute'lar |
|---|---|
| instance | — |
| veritabanı | `postgresql.database.name` |
| tablo | `postgresql.database.name`, `postgresql.table.name` = `<schema>.<table>` |
| indeks | `postgresql.database.name`, `postgresql.table.name` = `<table>` (şema yok, receiver gibi), `postgresql.index.name` |
| sorgu (opt-in `query_stats`) | `postgresql.database.name`, `postgresql.queryid`, `postgresql.rolname`, `db.query.text` |

| Metrik | Resource | Tür | Birim | Attribute'lar |
|---|---|---|---|---|
| `postgresql.bgwriter.buffers.allocated` | instance | Sum, monotonic, int | `{buffers}` | — |
| `postgresql.bgwriter.buffers.writes` | instance | Sum, monotonic, int | `{buffers}` | `source` = `bgwriter`, `checkpoints`, `backend`, `backend_fsync` (son ikisi yalnızca PostgreSQL 17 öncesi) |
| `postgresql.bgwriter.checkpoint.count` | instance | Sum, monotonic, int | `{checkpoints}` | `type` = `requested`, `scheduled` |
| `postgresql.bgwriter.duration` | instance | Sum, monotonic, double | `ms` | `type` = `write`, `sync` |
| `postgresql.bgwriter.maxwritten` | instance | Sum, monotonic, int | `1` | — |
| `postgresql.connection.max` | instance | Gauge, int | `{connections}` | — |
| `postgresql.database.count` | instance | Sum, non-monotonic, int | `{databases}` | — (toplanan veritabanları) |
| `postgresql.replication.data_delay` | instance | Gauge, int | `By` | `replication_client` (istemci adresi veya `unix`) |
| `postgresql.wal.lag` | instance | Gauge, int | `s` | `operation` = `write`, `flush`, `replay`; `replication_client` (bilinmezken atlanır) |
| `postgresql.wal.age` | instance | Gauge, int | `s` | — (yalnızca WAL arşivleme bir segment arşivlediyse) |
| `postgresql.backends` | veritabanı | Sum, non-monotonic, int | `1` | — |
| `postgresql.commits` / `postgresql.rollbacks` | veritabanı | Sum, monotonic, int | `1` | — |
| `postgresql.db_size` | veritabanı | Sum, non-monotonic, int | `By` | — |
| `postgresql.deadlocks` *(OTel'de opt-in)* | veritabanı | Sum, monotonic, int | `{deadlock}` | — |
| `postgresql.database.locks` *(OTel'de opt-in)* | veritabanı | Gauge, int | `{lock}` | `relation` (ilişki dışı kilitler için `''`), `mode`, `lock_type`; sayıya göre en üst 100 satır |
| `postgresql.table.count` | veritabanı | Sum, non-monotonic, int | `{table}` | — |
| `postgresql.rows` | tablo | Sum, non-monotonic, int | `1` | `state` = `live`, `dead` |
| `postgresql.operations` | tablo | Sum, monotonic, int | `1` | `operation` = `ins`, `upd`, `del`, `hot_upd` |
| `postgresql.table.size` | tablo | Sum, non-monotonic, int | `By` | — |
| `postgresql.table.vacuum.count` | tablo | Sum, monotonic, int | `{vacuum}` | — |
| `postgresql.blocks_read` | tablo | Sum, monotonic, int | `1` | `source` = `heap_read`, `heap_hit`, `idx_read`, `idx_hit`, `toast_read`, `toast_hit`, `tidx_read`, `tidx_hit` |
| `postgresql.index.scans` | indeks | Sum, monotonic, int | `{scans}` | — |
| `postgresql.index.size` | indeks | Gauge, int | `By` | — |

Cardinality: en fazla 32 veritabanı, veritabanı başına en büyük `top_n_tables` (varsayılan 20) tablo ve index, veritabanı başına 100 kilit satırı.

**En üst statement'lar** (`integrations.postgresql.query_stats.enabled: true`, openlog eklentisi). postgresqlreceiver `pg_stat_statements`'ı yalnızca
`db.server.top_query` log olayları olarak raporlar; openlog metrik üretir ve o olayın attribute anahtarlarını yeniden kullanır. `database` bağlantısında sorgulanır:
extension orada kurulu olmalı (`SELECT extversion FROM pg_extension`), sunucuda `shared_preload_libraries = 'pg_stat_statements'` gerekir ve
diğer rollerin statement'ları için `pg_read_all_stats` gerekir (`pg_monitor`'a dahil). Toplanan veritabanlarının `calls ≥ max(1, min_calls)` olan ve
`total_exec_time`'a göre (extension sürümü < 1.8, PostgreSQL < 13 için `total_time`) en üst `top_n` (varsayılan 20, en fazla 100) `pg_stat_statements` satırı;
`queryid` + veritabanı + rol başına bir resource (`pg_stat_statements.track = all`'dan gelen yinelenen ilk satırı korur).
`db.query.text`, PostgreSQL'in normalize ettiği statement'tır (`$1` yer tutucuları); boşluklar daraltılır, string literal'ler (`'…'`, `E'…'`,
`$$…$$`) `'?'`/`$$?$$` ile değiştirilir (`CREATE ROLE … PASSWORD '…'` gibi utility statement'larını PostgreSQL normalize etmez) ve
1024 bayta kesilir. Eksik extension, `shared_preload_libraries` hatası (SQLSTATE 55000), izin hatası veya `<insufficient privilege>` olarak gösterilen satırlar
toplamayı hint ile kısmi yapar (`CREATE EXTENSION pg_stat_statements`, `shared_preload_libraries`, `pg_read_all_stats`); diğer metrikler
etkilenmez.

| Metrik | Resource | Tür | Birim | Attribute'lar | Sütun |
|---|---|---|---|---|---|
| `postgresql.query.calls` | sorgu | Sum, monotonic, int | `{call}` | — | `calls` |
| `postgresql.query.rows` | sorgu | Sum, monotonic, int | `{row}` | — | `rows` |
| `postgresql.query.total_exec_time` | sorgu | Sum, monotonic, double | `ms` | — | `total_exec_time` (ortalama = Δtotal_exec_time / Δcalls) |
| `postgresql.query.shared_blocks` | sorgu | Sum, monotonic, int | `{block}` | `source` = `hit`, `read` | `shared_blks_hit`, `shared_blks_read` |

### 6.6 Docker Engine {#_6-6-docker-engine}

Entegrasyon id'si `docker` (kural `docker`) **metrik göndermez**: OTel `docker_stats` receiver'ı yalnızca ajanın zaten
cgroup v2'den gönderdiği `container.*` metriklerini tanımlar (§2 container metrikleri) ve motor düzeyinde OTel `docker.*` adları yoktur. Entegrasyon servis kartı için Docker Engine
API durumunu raporlar: `containers.docker_socket` üzerinde `GET /containers/json` başarılı olursa `enabled`, permission
denied durumunda `needs_configuration` (hint: root'a eşdeğer docker grubu üyeliği), socket yoksa veya API başarısızsa `error`,
`containers.enabled` false ise `not_available`. Durum yalnızca Docker Engine API'yi yansıtır: yalnızca bir CRI runtime'ın (containerd, CRI-O) yanıt verdiği host'larda
container listeleme başarılı olur ama entegrasyon `error` raporlar (`docker socket not found`).

### 6.7 Microsoft SQL Server (`mssql`, D-113) {#_6-7-microsoft-sql-server-mssql-d-113}

Entegrasyon id'si `mssql` (kural `mssql`: Windows servisi `MSSQLSERVER`/`MSSQL$<instance>`, `sqlservr`, port 1433, `mssql/server` container'ları, Linux'ta
`mssql-server` paketi). Her işletim sisteminde `github.com/microsoft/go-mssqldb` ile (`agents/infra/go.mod` içinde sabitli) TCP üzerinden TDS: uzak bir sunucu
(Azure SQL Managed Instance, ajansız bir Linux host) `integrations.mssql.instances[].endpoint` ile herhangi bir ajandan izlenir. Unix
socket'ler atlanır (TDS endpoint'i yok). Yalnızca SQL Server kimlik doğrulaması (`username`/`password`, uzak yapılandırma D-039: endpoint, username, password);
kimlik bilgisi yoksa status `needs_configuration` olur (kural `credentials` ister), parolasız başarısız login (18456)
`needs_configuration`, parolayla `error: authentication failed`. Bağlantı: veritabanı `master`, `app name=openlog-infra-agent`,
`encrypt=false` (yalnızca login paketi, SQL Server varsayılanı) veya `tls.enabled` ile `encrypt=true`; `TrustServerCertificate` =
`tls.insecure_skip_verify`, `certificate` = `tls.ca_file`, `hostNameInCertificate` = `tls.server_name`. Gereken izinler: `VIEW SERVER STATE`
(SQL Server 2022: `VIEW SERVER PERFORMANCE STATE`) ve veritabanı boyutları için `VIEW ANY DEFINITION`; boyutlar veya wait'ler için eksik izin
toplamayı kısmi yapar.

Resource attribute'ları: `sqlserver.version` (`SERVERPROPERTY('ProductVersion')`), `sqlserver.instance.name` (`@@SERVICENAME`). Kümülatif sum'lar
`start_time_unix_nano` = `sqlserver_start_time` taşır. Kaynaklar: `sys.dm_os_performance_counters` (nesne öneki `SQLServer:` / `MSSQL$<instance>:`
kaldırılır; instance başına sayaçlar `_Total` kullanır), `sys.master_files`, `sys.dm_os_wait_stats`. `.rate` gauge'ları, aynı bağlantının iki toplaması arasındaki kümülatif
`/sec` sayaçlarının saniye başına deltalarıdır (ilk toplamada veya sayaç sıfırlandıktan sonra yok).

| Metrik | Tür | Birim | Attribute'lar | Kaynak |
|---|---|---|---|---|
| `sqlserver.user.connection.count` | Gauge, int | `{connections}` | — | General Statistics `User Connections` |
| `sqlserver.processes.blocked` *(OTel'de opt-in)* | Gauge, int | `{processes}` | — | General Statistics `Processes blocked` |
| `sqlserver.batch.request.rate` | Gauge, double | `{requests}/s` | — | SQL Statistics `Batch Requests/sec` |
| `sqlserver.batch.sql_compilation.rate` | Gauge, double | `{compilations}/s` | — | SQL Statistics `SQL Compilations/sec` |
| `sqlserver.batch.sql_recompilation.rate` | Gauge, double | `{compilations}/s` | — | SQL Statistics `SQL Re-Compilations/sec` |
| `sqlserver.deadlock.rate` *(OTel'de opt-in)* | Gauge, double | `{deadlocks}/s` | — | Locks `Number of Deadlocks/sec` (`_Total`) |
| `sqlserver.deadlock.count` *(openlog, OTel'de yok)* | Sum, monotonic, int | `{deadlocks}` | — | aynı sayaç, kümülatif (toplamalar arasında deadlock kaybolmaz) |
| `sqlserver.lock.wait.rate` | Gauge, double | `{requests}/s` | — | Locks `Lock Waits/sec` (`_Total`) |
| `sqlserver.transaction.rate` | Gauge, double | `{transactions}/s` | — | Databases `Transactions/sec` (`_Total`) |
| `sqlserver.page.split.rate` | Gauge, double | `{pages}/s` | — | Access Methods `Page Splits/sec` |
| `sqlserver.page.buffer_cache.hit_ratio` | Gauge, double | `%` | — | Buffer Manager `Buffer cache hit ratio` / `… base` × 100 |
| `sqlserver.page.life_expectancy` | Gauge, int | `s` | `performance_counter.object_name` = `Buffer Manager` | Buffer Manager `Page life expectancy` |
| `sqlserver.database.size` *(openlog, OTel'de yok)* | Sum, non-monotonic, int | `By` | `sqlserver.database.name`, `file_type` (`rows`, `log`, `filestream`, `fulltext`) | `sys.master_files` sayfa × 8192 |
| `sqlserver.os.wait.duration` *(OTel'de opt-in)* | Sum, monotonic, double | `s` | `wait.type`, `wait.category` | `sys.dm_os_wait_stats.wait_time_ms` / 1000, toplam bekleme süresine göre en üst `top_n_tables` (10) wait türü, idle/background wait'ler hariç; kategori Query Store'daki gibi (`Lock`, `Buffer IO`, `Buffer Latch`, `Latch`, `Tran Log IO`, `Network IO`, `Parallelism`, `CPU`, `Memory`, `Preemptive`, `Other Disk IO`, `Replication`, `Other`) |

### 6.8 Microsoft IIS (`iis`, D-113) {#_6-8-microsoft-iis-iis-d-113}

Entegrasyon id'si `iis` (kural `iis`: Windows servisi `W3SVC`, `w3wp` worker süreçleri), yalnızca Windows; diğer işletim sistemlerinde status `not_available`.
Endpoint yok, kimlik bilgisi yok (`auto_enable`). Servis süreci (LocalSystem) ham performans sayacı sınıflarını WMI üzerinden okur
(`SELECT * FROM Win32_PerfRawData_W3SVC_WebService` ve `Win32_PerfRawData_APPPOOLCountersProvider_APPPOOLWAS`); ham değerler `…/sec` sayaçlarının arkasındaki kümülatif
sayılardır. `_Total` instance'ları atlanır. Status: Web Service sınıfı okunamıyorsa veya site instance'ı yoksa `error`
(W3SVC durmuş, sayaçlar kayıtlı değil: `lodctr /R`); eksik application pool sayaçları veya Windows sürümünün bilmediği özellikler
toplamayı kısmi yapar.

Resource'lar: site başına bir (`iis.site`) ve application pool başına bir (`iis.application_pool`), her biri §6.1'in instance attribute'larıyla.

| Metrik | Tür | Birim | Attribute'lar | Sayaç (aksi belirtilmedikçe site başına) |
|---|---|---|---|---|
| `iis.connection.active` | Sum, non-monotonic, int | `{connections}` | — | `CurrentConnections` |
| `iis.connection.anonymous` | Sum, monotonic, int | `{connections}` | — | `TotalAnonymousUsers` |
| `iis.connection.attempt.count` | Sum, monotonic, int | `{attempts}` | — | `TotalConnectionAttemptsallinstances` |
| `iis.network.blocked` | Sum, monotonic, int | `By` | — | `TotalBlockedBandwidthBytes` |
| `iis.network.io` | Sum, monotonic, int | `By` | `direction` = `sent`, `received` | `TotalBytesSent`, `TotalBytesReceived` |
| `iis.network.file.count` | Sum, monotonic, int | `{files}` | `direction` = `sent`, `received` | `TotalFilesSent`, `TotalFilesReceived` |
| `iis.request.count` | Sum, monotonic, int | `{requests}` | `request` = `delete`, `get`, `head`, `options`, `post`, `put`, `trace` | `Total<Method>Requests` |
| `iis.request.not_found.count` *(openlog, OTel'de yok)* | Sum, monotonic, int | `{requests}` | — | `TotalNotFoundErrors` |
| `iis.application_pool.state` | Gauge, int | `{state}` | — (resource `iis.application_pool`) | `CurrentApplicationPoolState`: 1 Uninitialized, 2 Initialized, 3 Running, 4 Disabling, 5 Disabled, 6 Shutdown Pending, 7 Delete Pending |

Toplanmaz: `iis.uptime`, `iis.application_pool.uptime` (geçen süre sayaçları performans sayacı zaman tabanı gerektirir), `iis.request.queue.*`,
`iis.request.rejected`, `iis.thread.active` (HTTP Service Request Queues / W3SVC_W3WP sınıfları).

### 6.8b Veritabanı sorgu performansı (`query_stats`, D-138) {#_6-8b-database-query-performance-query-stats-d-138}

Veritabanı entegrasyonu başına açıldığında (`integrations.<postgresql|mysql|mssql>.query_stats.enabled`) ajan,
statement istatistiklerini, session örneklerini ve execution plan'ları OTLP **log kayıtları** olarak gönderir; processor bunları
`logs` yerine kendi tablolarına yönlendirir. Kayıtlar, attribute'ları, her sunucunun ihtiyaçları ve API,
[db-monitoring.md](/tr/contracts/db-monitoring) sözleşmesidir; §6.4–6.7'nin sunucu metrikleri bundan etkilenmez.

### 6.9 Prometheus ve OpenMetrics endpoint'leri (`prometheus`, D-137) {#_6-9-prometheus-and-openmetrics-endpoints-prometheus-d-137}

Discovery'ye bağlı değildir: altyapı ajanı (a) `config.yaml`'ın statik `prometheus.targets[]` hedeflerini, (b) `prometheus.io/scrape: "true"` label'lı çalışan container'ları
(`prometheus.containers`, varsayılan açık) ve (c) Kubernetes node modunda node'un `prometheus.io/scrape: "true"` annotation'lı `Running` pod'larını
(`prometheus.kubernetes_pods`, varsayılan açık; bu durumda bir pod'un container'ları label'larından scrape edilmez, yani bir pod asla iki kez scrape edilmez) scrape eder. Opt-in anahtarları, label ve annotation'larda aynı: `prometheus.io/port` (varsayılan: bildirilen tek TCP
portu; hiç veya birden çok bildirilmişse hedef uyarıyla atlanır), `prometheus.io/path` (varsayılan `/metrics`, query taşıyabilir),
`prometheus.io/scheme` (`http` varsayılan, `https`), `prometheus.io/job`. Adres: pod IP'si, container'ın ilk ağ adresi veya
kendi adresi olmayan container için `127.0.0.1:<published port>`. Hedefler her `prometheus.interval` aralığında yenilenir; statik hedefler
önce gelir ve en fazla `prometheus.max_targets` (64) scrape edilir.

Scrape: `Accept: application/openmetrics-text;version=1.0.0;q=0.75,text/plain;version=0.0.4;q=0.5,*/*;q=0.1` ile `GET`,
`X-Prometheus-Scrape-Timeout-Seconds`, gzip, yalnızca aynı host'a yönlendirmeler, statik hedefler için bearer token veya basic auth ve TLS (`ca_file`, `server_name`,
`insecure_skip_verify`). Gövde `body_limit_bytes`'ı (32 MiB) aşarsa, hedef `sample_limit`'ten (20 000) fazla örnek sunarsa veya
exposition parse edilemezse tüm scrape reddedilir (kısmi family yok). Text format 0.0.4 ve OpenMetrics
1.0 (tırnak içindeki UTF-8 metrik adları dahil) ajanın kendisi tarafından parse edilir (`internal/promscrape`); `prometheus.metrics.include`/`exclude`
(family adları üzerinde glob'lar) dönüşümden önce family'leri düşürür.

Dönüşüm (OTel Collector `prometheus` receiver'ının eşlemesi):

| Exposition | OTLP | Notlar |
|---|---|---|
| counter | Sum, cumulative, monotonic, double | ad = örnek adı (`http_requests_total`) |
| gauge, unknown/untyped, info (`x_info` = 1), stateset (durum başına bir nokta) | Gauge, double | gaugehistogram: `x_bucket`, `x_gsum`, `x_gcount` gauge'ları |
| histogram | Histogram, cumulative | kümülatif `le` bucket'ları bucket başına sayılara dönüşür, sonuncusu `(max, +Inf]`; `count` `_count`'tan (yoksa `+Inf` bucket'ı) |
| summary | Summary | `quantile` label'ı → quantile değerleri |

Her label bir data point attribute'u olur; `le` ve `quantile` tüketilir. Örnek timestamp'leri korunur (text: ms, OpenMetrics: s). Kümülatif noktaların başlangıç
zamanı: sunuluyorsa `_created`, yoksa seriyi gören ilk scrape; öncekinden düşük bir değer sıfırlanmadır
ve önceki scrape'te yeni bir dönem başlatır. OpenMetrics exemplar'ları OTLP exemplar'ları olur (`trace_id`/`span_id` label'ları → trace ve span id, diğer
label'lar filtered attribute'lar), her exemplar gibi saklanır (§8, D-130). UNIT `seconds`/`bytes`/`ratio`/… UCUM'a eşlenir (`s`, `By`, `1`).

Resource (scrape başına bir): §1'in host resource'u, sonra hedefin `labels` değerleri (statik) veya container attribute'ları (`container.id`,
`container.name`, `container.image.name`, §7.2'nin Kubernetes pod attribute'ları) ya da pod attribute'ları (§7.2), sonra `service.name` = job (statik: `job`
veya URL host'u; container: `prometheus.io/job`, Compose servisi veya container adı; pod: `prometheus.io/job` veya
`<namespace>/<workload>`), `service.instance.id` = `host:port`, `server.address`, `server.port`, `url.scheme`, `openlog.integration.id` =
`prometheus`, `openlog.scrape.source` = `static`/`container`/`pod`. Başarılı olsun olmasın her scrape, Prometheus sağlık gauge'larını ekler: `up`
(1/0), `scrape_duration_seconds` ve `scrape_samples_scraped`; böylece erişilemeyen bir exporter görünür ve alarm verilebilir olur (`up == 0`). Ajanın
kendi telemetrisi scrape'leri `openlog.agent.integration.*{integration="prometheus"}` olarak sayar.

### 6.10 Apache HTTP Server (`apache`, D-139) {#_6-10-apache-http-server-apache-d-139}

Kaynak: makine tarafından okunabilir biçimde `mod_status` sayfası (`?auto`). Entegrasyon id'si `apache` (kural `apache-httpd`), `auto_enable: true`: yapılandırılmış
`endpoint` yoksa `/server-status?auto`, `/status?auto`, `/apache-status?auto`, `/httpd-status?auto` yolları her aday
endpoint üzerinde `http`, sonra `https` ile yoklanır (sertifikalar yalnızca loopback endpoint'lerde doğrulanmaz). `Total Accesses` ve `Uptime` taşıyan ilk sayfa
instance'ın sayfasıdır ve collector yeniden başlatmalarında hatırlanır. Böyle bir sayfa olmadan HTTP yanıtlayan port veya `mod_status`'un
sunmadığı sayfa hint ile `needs_configuration` olur; yanıt vermeyen port sonraki adaydır. Kimlik bilgisi yok: kimlik doğrulama arkasındaki sayfa
`integrations.apache.endpoint` ile yapılandırılır. İstek sayılarını, trafiği, CPU'yu ve scoreboard'u ekleyen `ExtendedStatus On`'dur (çoğu dağıtımda `mod_status` yüklüyken
varsayılan); o olmadan yalnızca worker'lar ve bağlantılar raporlanır.

Resource: instance başına bir (§6.1) artı `apache.server.version` (örneğin `Apache/2.4.58 (Unix)`). Counter'lar sunucunun başlangıcından itibaren kümülatiftir,
başlangıç zamanını `Uptime` verir.

| Metrik | Tür | Birim | Attribute'lar | mod_status alanı |
|---|---|---|---|---|
| `apache.uptime` | Sum, monotonic, int | `s` | — | `Uptime` |
| `apache.requests` | Sum, monotonic, int | `{requests}` | — | `Total Accesses` |
| `apache.traffic` | Sum, monotonic, int | `By` | — | `Total kBytes` × 1024 |
| `apache.request.time` | Sum, monotonic, int | `ms` | — | `Total Duration` (2.4.37+) |
| `apache.workers` | Sum, non-monotonic, int | `{workers}` | `state` = `busy`, `idle` | `BusyWorkers`, `IdleWorkers` |
| `apache.current_connections` | Sum, non-monotonic, int | `{connections}` | — | `ConnsTotal` (event/worker MPM) |
| `apache.connections.async` | Sum, non-monotonic, int | `{connections}` | `state` = `writing`, `keep-alive`, `closing` | `ConnsAsync*` |
| `apache.scoreboard` | Sum, non-monotonic, int | `{workers}` | `state` = `waiting`, `starting`, `reading`, `sending`, `keepalive`, `dnslookup`, `closing`, `logging`, `finishing`, `idle_cleanup`, `open`, `unknown` | `Scoreboard`, worker slot'u başına bir karakter |
| `apache.cpu.load` | Gauge, double | `%` | — | `CPULoad` |
| `apache.cpu.time` | Sum, monotonic, double | `s` | `level` = `self`, `children`; `mode` = `user`, `system` | `CPUUser`, `CPUSystem`, `CPUChildren*` |
| `apache.load.1` / `.5` / `.15` | Gauge, double | `%` | — | `Load1`, `Load5`, `Load15` (2.4.31+) |

### 6.11 Memcached (`memcached`, D-139) {#_6-11-memcached-memcached-d-139}

Kaynak: bulunan porttaki (11211) text protokolünün `stats` komutu, `auto_enable: true`, kimlik bilgisi yok. SASL
kimlik doğrulamalı bir sunucu `CLIENT_ERROR` yanıtlar ve `needs_configuration` olarak raporlanır: ajan Memcached'de kimlik doğrulamaz. Counter'lar sunucunun
başlangıcından itibaren kümülatiftir (`uptime` başlangıç zamanını verir).

| Metrik | Tür | Birim | Attribute'lar | `stats` alanı |
|---|---|---|---|---|
| `memcached.uptime` | Sum, monotonic, int | `s` | — | `uptime` |
| `memcached.bytes` | Sum, non-monotonic, int | `By` | — | `bytes` |
| `memcached.connections.current` | Sum, non-monotonic, int | `{connections}` | — | `curr_connections` |
| `memcached.connections.total` | Sum, monotonic, int | `{connections}` | — | `total_connections` |
| `memcached.current_items` | Sum, non-monotonic, int | `{items}` | — | `curr_items` |
| `memcached.evictions` | Sum, monotonic, int | `{evictions}` | — | `evictions` |
| `memcached.threads` | Sum, non-monotonic, int | `{threads}` | — | `threads` |
| `memcached.network` | Sum, monotonic, int | `By` | `direction` = `sent`, `received` | `bytes_written`, `bytes_read` |
| `memcached.commands` | Sum, monotonic, int | `{commands}` | `command` = `get`, `set`, `flush`, `touch` | `cmd_*` |
| `memcached.operations` | Sum, monotonic, int | `{operations}` | `operation` = `get`, `increment`, `decrement`, `delete`; `type` = `hit`, `miss` | `*_hits`, `*_misses` |
| `memcached.operation_hit_ratio` | Gauge, double | `%` | `operation` | türetilir: hits / (hits + misses) |
| `memcached.cpu.usage` | Sum, monotonic, double | `s` | `state` = `user`, `system` | `rusage_user`, `rusage_system` |

### 6.12 HAProxy (`haproxy`, D-139) {#_6-12-haproxy-haproxy-d-139}

Kaynak: CSV istatistikleri; HTTP üzerinden stats sayfasından (`;csv`) veya runtime API socket'inden (`show stat`) okunur. `auto_enable: true`:
`/;csv`, `/stats;csv`, `/haproxy?stats;csv`, `/haproxy_stats;csv` yolları aday endpoint'lerde (varsayılan port 8404) yoklanır ve
bilinen runtime socket'ler (`/run/haproxy/admin.sock`, `/var/run/haproxy/admin.sock`, `/run/haproxy.sock`, `/var/lib/haproxy/stats`) endpoint olarak
denenir; yanıt veren kaynak hatırlanır. Socket en az `operator` seviyesi ve ajanın kullanıcısının içinde olduğu bir grup gerektirir. Yapılandırılmış bir
`endpoint`, ya `http(s)://…` stats URL'i ya da `unix:/path`'tir. Kimlik bilgisi yok.

Resource'lar: CSV satırı başına bir (frontend, backend veya sunucularından biri); `haproxy.proxy.name` (`pxname`), `haproxy.service.name` (`svname`)
ve `haproxy.proxy.type` = `frontend`, `backend`, `server`, `listener` ile. Toplama başına en fazla 500 satır saklanır (`MaxProxies`); ötesinde
toplama kısmidir, yani yoğun bir load balancer'ın cardinality'si backend'de sürpriz değil, yapılandırmasının bir özelliğidir.

| Metrik | Tür | Birim | Attribute'lar | CSV sütunu |
|---|---|---|---|---|
| `haproxy.status` | Gauge, int (1) | `{status}` | `state` = `up`, `down`, `open`, `maint`, `drain`, `nolb`, … | `status` (ilk sözcük; `UP 2/3` → `up`) |
| `haproxy.sessions.count` | Sum, monotonic, int | `{sessions}` | — | `stot` |
| `haproxy.sessions.current` / `.limit` | Sum, non-monotonic, int | `{sessions}` | — | `scur`, `slim` |
| `haproxy.sessions.rate` | Gauge, int | `{sessions}/s` | — | `rate` |
| `haproxy.connections.total` | Sum, monotonic, int | `{connections}` | — | `conn_tot` |
| `haproxy.connections.rate` | Gauge, int | `{connections}/s` | — | `conn_rate` |
| `haproxy.connections.denied` / `.errors` | Sum, monotonic, int | `{connections}` | — | `dcon`, `econ` |
| `haproxy.requests.total` | Sum, monotonic, int | `{requests}` | — | `req_tot` |
| `haproxy.requests.rate` | Gauge, int | `{requests}/s` | — | `req_rate` |
| `haproxy.requests.denied` / `.errors` | Sum, monotonic, int | `{requests}` | — | `dreq`, `ereq` |
| `haproxy.responses.count` | Sum, monotonic, int | `{responses}` | `status_code` = `1xx`…`5xx`, `other` | `hrsp_*` |
| `haproxy.responses.errors` | Sum, monotonic, int | `{responses}` | — | `eresp` |
| `haproxy.bytes` | Sum, monotonic, int | `By` | `direction` = `received`, `sent` | `bin`, `bout` |
| `haproxy.server.retries` / `.redispatches` | Sum, monotonic, int | `{retries}` / `{redispatches}` | — | `wretr`, `wredis` |
| `haproxy.health_check.failures` | Sum, monotonic, int | `{checks}` | — | `chkfail` |
| `haproxy.queue.current` | Gauge, int | `{requests}` | — | `qcur` |
| `haproxy.queue.time`, `.connect.time`, `.response.time`, `.session.time` | Gauge, int | `ms` | — | `qtime`, `ctime`, `rtime`, `ttime` (son 1024 istek) |
| `haproxy.servers` | Gauge, int | `{servers}` | `state` = `active`, `backup` | `act`, `bck` |

Boş bir sütun hiçbir şey üretmez: frontend'in kuyruk veya backend zamanlamaları yoktur ve yokluk sıfır olarak okunmamalıdır.

### 6.13 RabbitMQ (`rabbitmq`, D-139) {#_6-13-rabbitmq-rabbitmq-d-139}

Kaynak: 15672 portunda management eklentisinin HTTP API'si (`/api/overview`, `/api/nodes`, `/api/queues`); discovery AMQP portunu (5672) bulur, bu yüzden
`endpoint` aksini söylemedikçe entegrasyon management portuna geçer. `requires: []` ile `auto_enable: true`: kimlik bilgisi gerekip gerekmediğini broker'ın kendisi
yanıtlar (HTTP 401 → hint ile `needs_configuration`), yani anonim veya farklı güvenlikli bir broker'ın login istediği varsayılmaz.
Önerilen kullanıcı salt okunur izinli, `monitoring` tag'lidir. Kuyruk listesi kullandığı sütunları ve en fazla 500
kuyruğu ister (`MaxQueues`); fazlası kısmi toplamadır, overview başarılıyken node veya kuyruk listesi başarısız olan broker da öyle.

Resource'lar: `rabbitmq.version` ve `rabbitmq.cluster.name` ile instance resource'u (§6.1); node başına bir (`rabbitmq.node.name`); kuyruk başına bir
(`rabbitmq.queue.name`, `rabbitmq.vhost.name`, `rabbitmq.node.name`). Mesaj ve consumer metrikleri kuyruğundur, OTel
`rabbitmqreceiver`'daki gibi: broker'ın kendi toplamları aynı adlarla **üretilmez**, çünkü bir instance'ı toplamak o zaman her mesajı
iki kez sayardı: bir kuyruğunda, bir toplamda.

| Metrik | Tür | Birim | Attribute'lar | Resource / API alanı |
|---|---|---|---|---|
| `rabbitmq.connection.count`, `.channel.count`, `.exchange.count`, `.queue.count` | Sum, non-monotonic, int | `{connections}`, `{channels}`, `{exchanges}`, `{queues}` | — | instance, `/api/overview` `object_totals` |
| `rabbitmq.message.current` | Sum, non-monotonic, int | `{messages}` | `state` = `ready`, `unacknowledged` | kuyruk |
| `rabbitmq.message.published`, `.delivered`, `.acknowledged`, `.redelivered`, `.dropped` | Sum, monotonic, int | `{messages}` | — | kuyruk, `message_stats` (`delivered` = `deliver` + `deliver_get`, `dropped` = yönlendirilemeyen) |
| `rabbitmq.consumer.count` | Sum, non-monotonic, int | `{consumers}` | — | kuyruk |
| `rabbitmq.queue.state` | Gauge, int (1) | `{status}` | `state` = `running`, `idle`, `flow`, … | kuyruk |
| `rabbitmq.node.up` | Gauge, int | `{status}` | `type` = `disc`, `ram` | node, `running` |
| `rabbitmq.node.memory.used` / `.limit` | Sum, non-monotonic, int | `By` | — | node |
| `rabbitmq.node.disk.free` / `.free_limit` | Sum, non-monotonic, int | `By` | — | node |
| `rabbitmq.node.file_descriptors.used` / `.limit`, `.sockets.used` / `.limit`, `.processes.used` / `.limit` | Sum, non-monotonic, int | `{file_descriptors}`, `{sockets}`, `{processes}` | — | node |
| `rabbitmq.node.run_queue` | Gauge, int | `{processes}` | — | node |
| `rabbitmq.node.uptime` | Sum, monotonic, int | `s` | — | node (ms / 1000) |
| `rabbitmq.node.alarm` | Gauge, int (0/1) | `{status}` | `kind` = `memory`, `disk` | node; alarm, broker'ın publish kabul etmeyi bırakma sebebidir |
| `rabbitmq.node.partitions` | Gauge, int | `{partitions}` | — | node, görülen network partition'ları |

### 6.14 Elasticsearch ve OpenSearch (`elasticsearch`, D-139) {#_6-14-elasticsearch-and-opensearch-elasticsearch-d-139}

Kaynak: 9200 portunda REST API: `/` (sürüm ve dağıtım), `/_cluster/health` ve `/_nodes/_local/stats`. Yalnızca yerel node'un istatistikleri
okunur: her node'da bir ajan çalışır ve her node'a diğerlerini sormak aynı serileri cluster boyutuyla çarpardı.
Transport portu (9300) asla endpoint olarak sunulmaz. `requires: []` ile `auto_enable: true`: güvenliği kapalı bir cluster kimlik bilgisi
gerektirmez, güvenli olanı HTTP 401 yanıtlar ve bu hint ile `needs_configuration` olur (`monitoring_user` yerleşik salt okunur
roldür). OpenSearch discovery kuralı bu entegrasyonu kullanır; ikisini `elasticsearch.distribution` ayırır. Erişilebilir cluster, okunamayan node
istatistikleri: kısmi, cluster sağlığı korunur.

Resource'lar: `elasticsearch.cluster.name`, `elasticsearch.version` ve `elasticsearch.distribution` (`elasticsearch`, `opensearch`)
ile instance resource'u (§6.1); node başına bir (`elasticsearch.node.name`).

| Metrik | Tür | Birim | Attribute'lar | API alanı |
|---|---|---|---|---|
| `elasticsearch.cluster.health` | Gauge, int | `{status}` | `status` = `green`, `yellow`, `red` | `status`, grafik ve alarm kullanabilsin diye 0, 1, 2 olarak |
| `elasticsearch.cluster.nodes` / `.data_nodes` | Sum, non-monotonic, int | `{nodes}` | — | `number_of_nodes`, `number_of_data_nodes` |
| `elasticsearch.cluster.shards` | Sum, non-monotonic, int | `{shards}` | `state` = `active`, `active_primary`, `relocating`, `initializing`, `unassigned`, `delayed_unassigned` | health shard sayıları |
| `elasticsearch.cluster.pending_tasks` | Sum, non-monotonic, int | `{tasks}` | — | `number_of_pending_tasks` |
| `elasticsearch.cluster.in_flight_fetch` | Gauge, int | `ms` | — | `task_max_waiting_in_queue_millis` |
| `elasticsearch.node.documents` | Sum, non-monotonic, int | `{documents}` | `state` = `active`, `deleted` | `indices.docs` |
| `elasticsearch.node.disk.usage` | Sum, non-monotonic, int | `By` | — | `indices.store.size_in_bytes` |
| `elasticsearch.node.operations.completed` / `.time` | Sum, monotonic, int | `{operations}` / `ms` | `operation` = `index`, `query`, `fetch`, `merge` | `indices.indexing`, `.search`, `.merges` |
| `elasticsearch.node.operations.failed` | Sum, monotonic, int | `{operations}` | `operation` = `index` | `indices.indexing.index_failed` |
| `elasticsearch.node.translog.operations` / `.size` | Sum, non-monotonic, int | `{operations}` / `By` | — | `indices.translog` |
| `elasticsearch.node.cache.memory.usage`, `.evictions` | Sum (non-monotonic / monotonic), int | `By` / `{evictions}` | `cache_name` = `query`, `fielddata` | `indices.query_cache`, `.fielddata` |
| `elasticsearch.node.cache.count` | Sum, monotonic, int | `{hits}` | `type` = `hit`, `miss` | `indices.query_cache` |
| `elasticsearch.node.thread_pool.threads`, `.threads.active`, `.tasks.queued`, `.tasks.rejected`, `.tasks.finished` | Sum (rejected ve finished için monotonic), int | `{threads}` / `{tasks}` | `thread_pool_name` | `thread_pool.*`, en fazla 32 pool |
| `elasticsearch.node.open_files` | Sum, non-monotonic, int | `{files}` | — | `process.open_file_descriptors` |
| `elasticsearch.node.cpu.usage` | Gauge, int | `%` | — | `process.cpu.percent` |
| `elasticsearch.node.fs.disk.free` / `.total` | Sum, non-monotonic, int | `By` | — | `fs.total` |
| `elasticsearch.breaker.tripped` | Sum, monotonic, int | `{breaks}` | `circuit_breaker_name` | `breakers.*.tripped` |
| `elasticsearch.breaker.memory.estimated` / `.limit` | Sum, non-monotonic, int | `By` | `circuit_breaker_name` | `breakers.*` |
| `jvm.memory.heap.used` / `.max`, `.nonheap.used` | Sum, non-monotonic, int | `By` | — | `jvm.mem` |
| `jvm.memory.heap.utilization` | Gauge, int | `%` | — | `jvm.mem.heap_used_percent` |
| `jvm.threads.count` | Sum, non-monotonic, int | `{threads}` | — | `jvm.threads.count` |
| `jvm.gc.collections.count` / `.elapsed` | Sum, monotonic, int | `{collections}` / `ms` | `name` = `young`, `old` | `jvm.gc.collectors` |

### 6.15 MongoDB (`mongodb`, D-143) {#_6-15-mongodb-mongodb-d-143}

Kaynaklar: `serverStatus` (sunucu), `listDatabases` ve `dbStats` (veritabanı başına) ve replica set
üyesinde `replSetGetStatus`. Entegrasyon id'si `mongodb` (kural `mongodb`), varsayılan port 27017, kimlik bilgisi
zorunlu: MongoDB güvenli bir kurulumda kimliği doğrulanmamış istemciye işe yarar hiçbir şey yanıtlamaz ve
izleme kullanıcısı yerleşik `clusterMonitor` rolüdür.

Bağlantı **doğrudandır** (`directConnection`): ajan bu `mongod`'un yanında çalışır ve *bu* süreci sorar, yani
topolojiyi izleyip başka yerdeki bir primary'ye gitmek başka bir makinenin sayılarını bu host'un adıyla raporlardı.
`integrations.mongodb.database` kimlik doğrulama kaynağıdır (varsayılan `admin`), izlenecek veritabanı değil;
`databases` / `exclude_databases` hangilerinin `dbStats` alacağını sınırlar, en fazla 32
(`MaxDatabases`), çünkü veritabanı bir seri kümesidir ve tenant başına bir veritabanı olan kurulumda
binlerce vardır.

Resource'lar: `db.version`, `mongodb.process` (`mongod` veya `mongos`; router depolama sayıları
raporlamaz ve hangisi olduğunu söylemek nedenini açıklar) ve replica set'te
`mongodb.replica_set.name` ile instance başına bir (§6.1); veritabanı başına bir (`db.namespace`). Counter'lar sunucunun
başlangıcından itibaren kümülatiftir, başlangıç zamanını `uptime` verir.

| Metrik | Tür | Birim | Attribute'lar | serverStatus / dbStats alanı |
|---|---|---|---|---|
| `mongodb.uptime` | Sum, monotonic, int | `s` | — | `uptime` |
| `mongodb.connection.count` | Sum, non-monotonic, int | `{connections}` | `type` = `current`, `available`, `active` | `connections.*` |
| `mongodb.memory.usage` | Sum, non-monotonic, int | `By` | `type` = `resident`, `virtual` | `mem.*` (MiB → bayt) |
| `mongodb.operation.count` | Sum, monotonic, int | `{operations}` | `operation` = `insert`, `query`, `update`, `delete`, `getmore`, `command` | `opcounters.*` |
| `mongodb.document.operation.count` | Sum, monotonic, int | `{documents}` | `operation` = `insert`, `query`, `update`, `delete` | `metrics.document.*` |
| `mongodb.operation.latency.time` | Sum, monotonic, int | `us` | `operation` = `read`, `write`, `command` | `opLatencies.*.latency` |
| `mongodb.network.io.receive` / `.transmit` | Sum, monotonic, int | `By` | — | `network.bytesIn`, `network.bytesOut` |
| `mongodb.network.request.count` | Sum, monotonic, int | `{requests}` | — | `network.numRequests` |
| `mongodb.global_lock.time` | Sum, monotonic, int | `ms` | — | `globalLock.totalTime` (µs → ms) |
| `mongodb.cursor.count` | Sum, non-monotonic, int | `{cursors}` | `type` = `open`, `no_timeout` | `metrics.cursor.open.*` |
| `mongodb.cursor.timeout.count` | Sum, monotonic, int | `{cursors}` | — | `metrics.cursor.timedOut` |
| `mongodb.cache.operations` | Sum, monotonic, int | `{operations}` | `type` = `hit`, `miss` | WiredTiger'ın istenen sayfaları eksi okunan sayfaları, ve okunan sayfaları |
| `mongodb.session.count` | Sum, non-monotonic, int | `{sessions}` | — | `logicalSessionRecordCache.activeSessionsCount` |
| `mongodb.asserts` | Sum, monotonic, int | `{asserts}` | `type` = `regular`, `warning` | `asserts.*` |
| `mongodb.database.count` | Sum, non-monotonic, int | `{databases}` | — | `listDatabases` |
| `mongodb.collection.count`, `.index.count`, `.object.count`, `.view.count` | Sum, non-monotonic, int | `{collections}`, `{indexes}`, `{objects}`, `{views}` | — (resource `db.namespace`) | `dbStats` |
| `mongodb.data.size`, `.storage.size`, `.index.size` | Sum, non-monotonic, int | `By` | — (resource `db.namespace`) | `dbStats` |
| `mongodb.replica_set.member` | Gauge, int (1) | `{status}` | `state` = `primary`, `secondary`, … | `replSetGetStatus` |
| `mongodb.replica_set.members` | Sum, non-monotonic, int | `{members}` | — | `replSetGetStatus` |
| `mongodb.replica_set.lag` | Gauge, int | `s` | — | primary'nin `optimeDate` değeri eksi bu üyeninki |

`replSetGetStatus` standalone sunucuda başarısız olur (`not running with --replSet`), bu sorun olarak
raporlanmaz: izlenen MongoDB sunucularının çoğu standalone'dur. Lag **yalnızca** yanıt bir primary
adlandırdığında üretilir; o olmadan geride kalınacak bir şey yoktur ve 0 raporlamak yalan olurdu.

### 6.16 Java runtime'ları (`jvm`, D-144) {#_6-16-java-runtimes-jvm-d-144}

Kaynak: Jolokia sunan herhangi bir JVM'in `java.lang` MBean'leri, toplama başına **tek bir bulk POST** olarak okunur
(`internal/integrations/jolokia`). Entegrasyon id'si `jvm` (kural `jvm`: herhangi bir `java` süreci), `requires: []` ile `auto_enable: true`:
köprünün orada olup olmadığını JVM'in kendisi yanıtlar. `/jolokia`,
`/actuator/jolokia`, `/jmx` ve `/api/jolokia` yolları aday endpoint'lerde ve 8778 portunda (bağımsız Jolokia ajanının
kendi portu) yoklanır; `/version` yanıtlayan hatırlanır. Jolokia yoksa veya
köprünün erişim politikası `java.lang`'ı gizliyorsa hint ile `needs_configuration` olur.

Neden doğrudan JMX değil Jolokia: MBean'lere JMX üzerinden ulaşmak Java RMI artı nesne serileştirmesi demektir;
olgun bir Go implementasyonu olmayan, bir Java sürümünde bozulan binlerce satır. Jolokia aynı
MBean'leri tek HTTP isteğine çevirir ve maliyet, operatörün bir kez eklediği bir `-javaagent` satırına taşınır; orada
görünür ve geri alınabilirdir (D-144).

Resource: instance başına bir (§6.1) artı `jvm.version` (`Runtime.VmVersion`). Counter'lar JVM'in başlangıcından itibaren
kümülatiftir, başlangıç zamanını `Runtime.Uptime` verir.

| Metrik | Tür | Birim | Attribute'lar | MBean attribute'u |
|---|---|---|---|---|
| `jvm.uptime` | Sum, monotonic, int | `ms` | — | `Runtime.Uptime` |
| `jvm.memory.used` / `.committed` / `.limit` | Sum, non-monotonic, int | `By` | `jvm.memory.type` = `heap`, `non_heap` | `Memory.{Heap,NonHeap}MemoryUsage` |
| `jvm.memory.pool.used` / `.limit` | Sum, non-monotonic, int | `By` | `jvm.memory.pool.name` | `MemoryPool.Usage` (pattern okuması) |
| `jvm.thread.count` | Sum, non-monotonic, int | `{threads}` | `jvm.thread.daemon` = `true`, `false` | `Threading.{Thread,DaemonThread}Count` |
| `jvm.thread.peak` | Sum, non-monotonic, int | `{threads}` | — | `Threading.PeakThreadCount` |
| `jvm.class.count` | Sum, non-monotonic, int | `{classes}` | — | `ClassLoading.LoadedClassCount` |
| `jvm.class.unloaded` | Sum, monotonic, int | `{classes}` | — | `ClassLoading.UnloadedClassCount` |
| `jvm.gc.collections` | Sum, monotonic, int | `{collections}` | `jvm.gc.name` | `GarbageCollector.CollectionCount` (pattern okuması) |
| `jvm.gc.duration` | Sum, monotonic, int | `ms` | `jvm.gc.name` | `GarbageCollector.CollectionTime` |
| `jvm.cpu.recent_utilization` | Gauge, double | `1` | — | `OperatingSystem.ProcessCpuLoad` |
| `jvm.system.cpu.utilization` | Gauge, double | `1` | — | `OperatingSystem.SystemCpuLoad` |
| `jvm.file_descriptor.count` / `.limit` | Sum, non-monotonic, int | `{file_descriptors}` | — | `OperatingSystem.{Open,Max}FileDescriptorCount` |

Üç modelleme kuralı: `-1` maksimum (limitsiz pool) limit olarak **üretilmez**; `-1` CPU payı
(ikinci örneğinden önce MXBean) hiç üretilmez; thread sayısı da toplam artı alt küme olarak üretilmek yerine
daemon ve non-daemon olarak bölünür, böylece serileri toplamak daemon'ları iki kez saymak yerine
toplamı verir.

### 6.17 Apache Kafka (`kafka`, D-144) {#_6-17-apache-kafka-kafka-d-144}

Kaynak: broker'ın kendi MBean'leri, aynı Jolokia köprüsü üzerinden tek bulk okumayla. Entegrasyon id'si `kafka`
(kural `kafka`), `auto_enable: true`. Discovery broker'ın listener portunu (9092/9093/9094) bulur; köprü
başka yerdedir, bu yüzden `endpoint` aksini söylemedikçe endpoint Jolokia'nın 8778'ine geçer.

Aynı sürecin JVM metrikleri burada **tekrarlanmaz**: broker bir JVM'dir, `jvm` `java.lang`'ı okur
ve ikisi de aynı endpoint'e bağlanır. Bunun eklediği, süreci broker yapan şeydir.

| Metrik | Tür | Birim | Attribute'lar | MBean |
|---|---|---|---|---|
| `kafka.messages.in` | Sum, monotonic, int | `{messages}` | — | `BrokerTopicMetrics.MessagesInPerSec.Count` |
| `kafka.network.io` | Sum, monotonic, int | `By` | `direction` = `in`, `out` | `BrokerTopicMetrics.Bytes{In,Out}PerSec.Count` |
| `kafka.request.count` / `.failed` | Sum, monotonic, int | `{requests}` | `type` = `fetch`, `produce` | `BrokerTopicMetrics.Total*/Failed*RequestsPerSec` |
| `kafka.request.total` | Sum, monotonic, int | `{requests}` | `type` (istek adı) | `RequestMetrics.RequestsPerSec` (pattern okuması) |
| `kafka.request.time.avg` | Gauge, double | `ms` | `type` (istek adı) | `RequestMetrics.TotalTimeMs.Mean` |
| `kafka.request.queue` / `kafka.response.queue` | Sum, non-monotonic, int | `{requests}` / `{responses}` | — | `RequestChannel.{Request,Response}QueueSize` |
| `kafka.request.handler.busy` | Gauge, double | `1` | — | `1 −` `KafkaRequestHandlerPool.RequestHandlerAvgIdlePercent` |
| `kafka.network.processor.busy` | Gauge, double | `1` | — | `1 −` `SocketServer.NetworkProcessorAvgIdlePercent` |
| `kafka.partition.count` / `.global` | Sum, non-monotonic, int | `{partitions}` | — | `ReplicaManager.PartitionCount`, `KafkaController.GlobalPartitionCount` |
| `kafka.partition.under_replicated` | Sum, non-monotonic, int | `{partitions}` | — | `ReplicaManager.UnderReplicatedPartitions` |
| `kafka.partition.under_min_isr` | Sum, non-monotonic, int | `{partitions}` | — | `ReplicaManager.UnderMinIsrPartitionCount` |
| `kafka.partition.offline` | Sum, non-monotonic, int | `{partitions}` | — | `KafkaController.OfflinePartitionsCount` |
| `kafka.leader.count` | Sum, non-monotonic, int | `{partitions}` | — | `ReplicaManager.LeaderCount` |
| `kafka.leader.election.count` / `.unclean` | Sum, monotonic, int | `{elections}` | — | `ControllerStats.*` |
| `kafka.isr.operation.count` | Sum, monotonic, int | `{operations}` | `operation` = `shrink`, `expand` | `ReplicaManager.Isr{Shrinks,Expands}PerSec` |
| `kafka.controller.active.count` | Sum, non-monotonic, int | `{controllers}` | — | `KafkaController.ActiveControllerCount` |
| `kafka.topic.count` | Sum, non-monotonic, int | `{topics}` | — | `KafkaController.GlobalTopicCount` |
| `kafka.purgatory.size` | Sum, non-monotonic, int | `{operations}` | `operation` = `produce`, `fetch` | `DelayedOperationPurgatory.PurgatorySize` |
| `kafka.logs.flush.count` | Sum, monotonic, int | `{flushes}` | — | `LogFlushStats.LogFlushRateAndTimeMs.Count` |
| `kafka.broker.state` | Gauge, int | `{state}` | — | `KafkaServer.BrokerState` (3 = çalışıyor) |

`*Rate` olanlar yerine `Count` attribute'u okunur: broker'ın kendi penceresinde hesapladığı oran
yeniden toplanamaz, monoton bir toplam ise toplanabilir. `kafka.request.handler.busy`, broker'ın kendi
boşta kalma payını tersine çevirir, çünkü operatörün sorduğu soru "ne kadar meşgul"dür.

### 6.18 PHP-FPM (`php-fpm`, D-147) {#_6-18-php-fpm-php-fpm-d-147}

Kaynak: PHP-FPM yapılandırmasının **her pool'u**, her biri HTTP değil **FastCGI** üzerinden, kendi listening socket'inde okunur.
Pool'lar, bilinen socket yollarının listesinden değil pool dosyalarından gelir (`/etc/php/*/fpm/pool.d/*.conf`, `/etc/php-fpm.d/*.conf` ve Remi, cPanel
ve Plesk düzenleri; PHP ajanının erişim kontrolünün kullandığı okuyucunun aynısı): site başına bir pool çalıştıran host'ta (HestiaCP, cPanel, Plesk) sitelerin adını taşıyan, hiçbir
sabit listenin sayamayacağı onlarca socket vardır ve her pool kendi resource'udur, çünkü bir pool'un kuyruğu diğerininkiyle ilgili hiçbir şey söylemez.
`listen` adresi (socket yolu, `host:port` veya loopback'te yalın port), `pm.status_path` sayfayı verir; status yolu belirtmeyen
pool için `QUERY_STRING=json` ile `SCRIPT_NAME` olarak `/status`, `/fpm-status`, `/php-fpm-status`, `/php_status`, `/fpm_status` yoklanır
ve yanıt veren hatırlanır. Pool dosyaları okunamayan bir host (yalnızca socket'i mount eden bir container)
discovery'nin türettiği tek endpoint'e döner, aday olarak bilinen socket yollarıyla; yapılandırılmış bir `endpoint` (`unix:/path` veya `host:port`) bu yedeği seçer. PHP-FPM FastCGI konuşur,
yani HTTP üzerinden `/status`'a ulaşmak önündeki web sunucusunun o yolu proxy'lemesini gerektirirdi; pool'un kendi socket'i
yalnızca PHP-FPM'in kendi ayarı olan `pm.status_path`'i gerektirir. `requires: []` ile `auto_enable: true`: status sayfası
kimlik bilgisi gerektirmez.

Yanıt veren pool'lar diğerleri vermese de kaydedilir, yani kapalı veya yapılandırılmamış bir pool diğerlerine mal olmaz;
toplama bu durumda `partial` olur ve neyin başarısız olduğunu söyler. **Hiçbir** pool yanıt vermezse iki sonuç, erişilemeyen endpoint değil
yapılandırma yanıtıdır ve düzeltmesiyle `needs_configuration` raporlanır: ajanın bağlanamadığı bir socket
(ajan `openlog-agent` olarak çalışırken pool socket'leri web sunucusunun kullanıcısına ait `0660`'tır, yani
`usermod -aG www-data openlog-agent` tüm pool'ları bir kerede, `listen.acl_users = openlog-agent` ise tek tek kapsar)
ve her yolda 404 yanıtlayan veya stderr'e yazan, `pm.status_path`'i olmayan bir pool. İkisi de pool'un
kapalı olduğu anlamına gelmez.

Pool başına bir resource, `phpfpm.pool.name` (`pool`, status sayfasının raporladığı gibi) ve `phpfpm.process_manager`
(`process manager`: `static`, `dynamic`, `ondemand`) ile. Kümülatif sum'lar başlangıç zamanı **taşımaz**: tek batch,
her biri master'ı fork ettiğinde başlamış birkaç pool tutar, yani batch çapında bir başlangıç çoğunu başka bir
pool'unkiyle etiketlerdi (nginx ve PostgreSQL sum'ları da taşımaz, §6.1).

| Metrik | Tür | Birim | Attribute'lar | status alanı |
|---|---|---|---|---|
| `phpfpm.uptime` | Sum, monotonic, int | `s` | — | `start since` |
| `phpfpm.connections.accepted` | Sum, monotonic, int | `{connections}` | — | `accepted conn` |
| `phpfpm.requests.slow` | Sum, monotonic, int | `{requests}` | — | `slow requests` (`request_slowlog_timeout` gerektirir) |
| `phpfpm.max_children_reached` | Sum, monotonic, int | `{events}` | — | `max children reached` |
| `phpfpm.listen_queue.current` | Gauge, int | `{requests}` | — | `listen queue` |
| `phpfpm.listen_queue.max` | Gauge, int | `{requests}` | — | `max listen queue` (başlangıçtan beri en yüksek değer) |
| `phpfpm.listen_queue.limit` | Gauge, int | `{requests}` | — | `listen queue len`; 0 ise atlanır, böylece backlog ayarı olmayan socket sıfır limit gibi okunmaz |
| `phpfpm.processes.current` | Sum, non-monotonic, int | `{processes}` | `state` = `idle`, `active` | `idle processes`, `active processes` |
| `phpfpm.processes.max_active` | Gauge, int | `{processes}` | — | `max active processes` |

`total processes` hiçbir şey üretmez: `idle + active`'tir ve backend'in toplayabildiği bir değer saklamaya değer bir seri değildir.
`?full`'un süreç başına detayı toplanmaz: `pm.max_requests` ile yenilenen worker başına bir seri, bilgiden çok
churn'dür.

## 7. Kubernetes (altyapı ajanı, M4, D-070, D-071) {#_7-kubernetes-infra-agent-m4-d-070-d-071}

Altyapı ajanı Kubernetes'te `deploy/helm/openlog-agent` chart'ından iki modda çalışır ([operations/kubernetes.md](/tr/operations/kubernetes)):
**node** (DaemonSet, node başına bir: host, container, kubelet metrikleri ve container logları) ve **cluster** (leader seçimli tek
Deployment replikası: kube-state metrikleri ve Kubernetes olayları). Nesne kimliği, container metrikleri gibi (§2) ajan başına tek resource üzerinde **data point / log kaydı attribute'ları**
olarak gönderilir, böylece bir node'un her Kubernetes serisi node'un shard'ına düşer ve alarm
kuralları onu `attr.*` ile filtreler. Adlar OTel `kubeletstats` ve `k8s_cluster` receiver'larını izler; yalnızca openlog'a özel adlar `openlog.k8s.` kullanır.

### 7.1 Tespit ve resource {#_7-1-detection-and-resource}

Kubernetes modu, `kubernetes.enabled` `true` olduğunda veya `auto` (varsayılan) iken `KUBERNETES_SERVICE_HOST` ayarlıysa açıktır. API server'a
cluster içinden ulaşılır (`https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT`, ServiceAccount token'ı ve CA).

| Attribute | Node modu (host resource'u, §1) | Cluster modu resource'u |
|---|---|---|
| `k8s.cluster.name` | `kubernetes.cluster_name` (chart `clusterName`) | aynı |
| `k8s.cluster.uid` | `kube-system` namespace'inin uid'si (okunamazsa atlanır) | aynı (zorunlu: cluster ajanı o olmadan başlamaz) |
| `k8s.node.name` | `kubernetes.node_name` (downward API `spec.nodeName`) | — |
| `openlog.entity.type` | `host` | `k8s_cluster` |
| `openlog.agent.mode` | `node` | `cluster` |
| `host.id`, `host.name`, OS attribute'ları | §1'deki gibi | **gönderilmez** (`host_id` = `''`; cluster ajanı host satırı yazmaz) |
| `openlog.agent.name`, `openlog.agent.version` | §1'deki gibi | aynı |

### 7.2 Container metrikleri ve loglarında pod metadata'sı (node modu) {#_7-2-pod-metadata-on-container-metrics-and-logs-node-mode}

Node ajanı kendi node'unun pod'larını izler (`GET /api/v1/pods?fieldSelector=spec.nodeName=<node>`, list + watch, her
`kubernetes.pod_resync` (5m) aralığında ve `410 Gone` sonrası yeniden liste). CRI'dan (`io.kubernetes.*` label'ları) veya Docker'dan listelenen container'lar
`container.id` ile (pod `status.containerStatuses[].containerID`), yoksa pod uid + container adı ile eşleştirilir. Bu attribute'lar, biliniyorsa, her
container metrik data point'ine (§2) ve container log kayıtlarının resource'una (§4.1) eklenir:

| Attribute | Kaynak |
|---|---|
| `k8s.pod.name`, `k8s.namespace.name`, `k8s.container.name` | CRI label'ları `io.kubernetes.pod.name`, `io.kubernetes.pod.namespace`, `io.kubernetes.container.name` (artık containerd ve CRI-O için de), yoksa pod |
| `k8s.pod.uid` | CRI label'ı `io.kubernetes.pod.uid`, yoksa pod |
| `k8s.node.name`, `k8s.cluster.name` | ajan yapılandırması |
| `k8s.replicaset.name`, `k8s.deployment.name` | controller owner `ReplicaSet`; deployment, `-<pod-template-hash>` soneki olmadan ReplicaSet adıdır |
| `k8s.statefulset.name`, `k8s.daemonset.name`, `k8s.job.name` | o türdeki controller owner |
| `k8s.cronjob.name` | adı `<cronjob>-<digits>` olan owner `Job` (CronJob'un oluşturduğu job'lar) |
| `openlog.k8s.workload.kind`, `openlog.k8s.workload.name` | en üst owner: `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob`, `Job`, `ReplicaSet` (deployment yok) veya `Pod` (yalın pod, ad = pod adı) |
| `k8s.pod.label.<key>` | `kubernetes.label_allowlist` içindeki pod label'ları (varsayılan `app`, `app.kubernetes.io/name`, `app.kubernetes.io/instance`, `app.kubernetes.io/version`, `app.kubernetes.io/component`) |

CRI container'larının log dosyaları `/var/log/pods/<namespace>_<pod>_<pod-uid>/<container>/<restart>.log` olur (CRI log biçimi, §4.1); runtime
metadata'sı yoksa ajan namespace, pod adı, pod uid ve container adını bu yoldan alır.

### 7.3 Kubelet metrikleri (node modu) {#_7-3-kubelet-metrics-node-mode}

Her metrik aralığında `GET https://<kubernetes.kubelet.endpoint>/stats/summary` (varsayılan `https://$OPENLOG_K8S_HOST_IP:10250`, ServiceAccount bearer token'ı,
RBAC `nodes/stats get`). Pod metriklerinin attribute'ları: `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`,
`k8s.node.name` artı §7.2'nin owner attribute'ları; container metrikleri `k8s.container.name` ve `container.id` ekler (pod watch'tan);
node metrikleri `k8s.node.name` taşır. Kümülatif counter'lar başlangıç zamanı taşımaz (container metrikleri gibi, §2).

| Metrik | Tür | Birim | Summary alanı |
|---|---|---|---|
| `k8s.node.cpu.usage` | Gauge | `{cpu}` | `node.cpu.usageNanoCores` / 1e9 |
| `k8s.node.cpu.time` | Sum, cumulative, monotonic | `s` | `node.cpu.usageCoreNanoSeconds` / 1e9 |
| `k8s.node.memory.usage` · `k8s.node.memory.working_set` · `k8s.node.memory.rss` · `k8s.node.memory.available` | Gauge | `By` | `node.memory.*` |
| `k8s.node.network.io` | Sum, cumulative, monotonic | `By` | `node.network.interfaces[]` rx/tx toplanır; `network.io.direction` |
| `k8s.node.filesystem.usage` · `.capacity` · `.available` | Gauge | `By` | `node.fs` |
| `k8s.pod.cpu.usage` | Gauge | `{cpu}` | `pods[].cpu.usageNanoCores` / 1e9 |
| `k8s.pod.cpu.time` | Sum, cumulative, monotonic | `s` | `pods[].cpu.usageCoreNanoSeconds` / 1e9 |
| `k8s.pod.memory.usage` · `k8s.pod.memory.working_set` · `k8s.pod.memory.rss` | Gauge | `By` | `pods[].memory.*` |
| `k8s.pod.network.io` | Sum, cumulative, monotonic | `By` | `pods[].network.interfaces[]` toplanır; `network.io.direction` = `receive`,`transmit` |
| `k8s.pod.network.errors` | Sum, cumulative, monotonic | `{error}` | rx/tx hataları; `network.io.direction` |
| `k8s.pod.filesystem.usage` · `.capacity` · `.available` | Gauge | `By` | `pods[].ephemeral-storage` |
| `k8s.container.cpu.usage` | Gauge | `{cpu}` | `pods[].containers[].cpu.usageNanoCores` / 1e9 |
| `k8s.container.cpu.time` | Sum, cumulative, monotonic | `s` | `containers[].cpu.usageCoreNanoSeconds` / 1e9 |
| `k8s.container.memory.usage` · `k8s.container.memory.working_set` · `k8s.container.memory.rss` | Gauge | `By` | `containers[].memory.*` |
| `k8s.container.filesystem.usage` | Gauge | `By` | `containers[].rootfs.usedBytes` + `logs.usedBytes` |

OTel `kubeletstats` receiver'ı container metriklerini `container.*` olarak adlandırır; openlog `k8s.container.*` kullanır, çünkü `container.*` adları
zaten §2'nin cgroup metrikleridir (ikisi de gönderilir; hafifçe farklıdırlar: cgroup `container.memory.usage` `inactive_file`'ı hariç tutar, kubelet
`working_set` de hariç tutar ama `usage` tutmaz).

### 7.4 Cluster metrikleri (cluster modu) {#_7-4-cluster-metrics-cluster-mode}

Her `kubernetes.cluster.interval` (30s) aralığında `resourceVersion=0` ile (API server watch cache'inden sunulur) listelenir, yalnızca Lease sahibi tarafından
(ajan namespace'inde `coordination.k8s.io` Lease `kubernetes.cluster.lease_name`, 15s lease, 5s'de bir yenileme). Attribute kümeleri:
*pod* = `k8s.namespace.name`, `k8s.pod.name`, `k8s.pod.uid`, `k8s.node.name` + owner attribute'ları (§7.2, ReplicaSet'ler
ve Job'lar üzerinden birebir çözülür); *container* = *pod* + `k8s.container.name`, `container.id` (runtime öneki kaldırılır; container başlamadan önce boş),
`container.image.name`, `container.image.tags`; *workload* = `k8s.namespace.name`, `openlog.k8s.workload.kind`, `openlog.k8s.workload.name`,
`openlog.k8s.workload.uid` + türün name/uid attribute'u (`k8s.deployment.name`/`k8s.deployment.uid`, …).

| Metrik | Tür | Birim | Attribute'lar / değer |
|---|---|---|---|
| `openlog.k8s.cluster.status` | Gauge | `1` | değer 1; `openlog.k8s.cluster.version` (API server `gitVersion`), `openlog.k8s.cluster.nodes`, `openlog.k8s.cluster.namespaces` (string olarak sayılar) |
| `openlog.k8s.node.status` | Gauge | `1` | değer 1; `k8s.node.name`, `k8s.node.uid`, `openlog.k8s.node.ready` (`true`,`false`,`unknown`), `openlog.k8s.node.unschedulable` (`true`,`false`), `openlog.k8s.node.roles` (virgülle ayrılmış `node-role.kubernetes.io/*`), `openlog.k8s.node.kubelet_version`, `openlog.k8s.node.os_image`, `openlog.k8s.node.container_runtime`, `openlog.k8s.node.internal_ip`, `openlog.k8s.node.created_at` (RFC3339) |
| `k8s.node.condition` | Gauge | `1` | `k8s.node.name`, `condition` (`Ready`, `MemoryPressure`, `DiskPressure`, `PIDPressure`, `NetworkUnavailable`); 1 true, 0 false, -1 unknown |
| `k8s.node.allocatable_cpu` · `k8s.node.allocatable_memory` · `k8s.node.allocatable_pods` · `k8s.node.allocatable_ephemeral_storage` | Gauge | `{cpu}` · `By` · `{pod}` · `By` | `k8s.node.name` |
| `openlog.k8s.pod.status` | Gauge | `1` | değer 1; *pod* + `openlog.k8s.pod.phase` (`Pending`,`Running`,`Succeeded`,`Failed`,`Unknown`), `openlog.k8s.pod.ready` (`true`,`false`), `openlog.k8s.pod.reason` (ilk bulunan: pod `status.reason` örneğin `Evicted`, container waiting sebebi örneğin `CrashLoopBackOff`/`ImagePullBackOff`, terminated sebebi örneğin `OOMKilled`/`Error`; yoksa boş), `openlog.k8s.pod.restarts` (container'lar üzerinden toplam), `openlog.k8s.pod.ip`, `openlog.k8s.pod.qos_class`, `openlog.k8s.pod.created_at`, `openlog.k8s.pod.started_at`, `openlog.k8s.pod.containers` (JSON dizisi `[{"name","container_id","image","ready","restarts","state","reason"}]`, init container'lar hariç, en fazla 4 KiB) |
| `k8s.pod.phase` | Gauge | `1` | *pod*; 1 Pending, 2 Running, 3 Succeeded, 4 Failed, 5 Unknown |
| `k8s.container.restarts` | Gauge | `{restart}` | *container*; `restartCount` |
| `k8s.container.ready` | Gauge | `1` | *container*; 1 hazır, 0 değil |
| `k8s.container.cpu_request` · `k8s.container.cpu_limit` | Gauge | `{cpu}` | *container*; yalnızca ayarlıysa |
| `k8s.container.memory_request` · `k8s.container.memory_limit` | Gauge | `By` | *container*; yalnızca ayarlıysa |
| `openlog.k8s.workload.status` | Gauge | `1` | değer 1; *workload* + `openlog.k8s.workload.desired`, `.ready`, `.available`, `.updated` (string olarak tamsayılar; türler için aşağıya bak), `openlog.k8s.workload.created_at` |
| `openlog.k8s.workload.unavailable` | Gauge | `{pod}` | *workload*; max(desired − available, 0) (Job'lar: başarısız pod'lar; CronJob'lar: 0) |
| `k8s.deployment.desired` · `k8s.deployment.available` | Gauge | `{pod}` | *workload* (Deployment) |
| `k8s.statefulset.desired_pods` · `.current_pods` · `.ready_pods` · `.updated_pods` | Gauge | `{pod}` | *workload* (StatefulSet) |
| `k8s.daemonset.desired_scheduled_nodes` · `.current_scheduled_nodes` · `.ready_nodes` · `.misscheduled_nodes` | Gauge | `{node}` | *workload* (DaemonSet) |
| `k8s.job.active_pods` · `.failed_pods` · `.successful_pods` · `.desired_successful_pods` · `.max_parallel_pods` | Gauge | `{pod}` | *workload* (Job; desired/max yalnızca ayarlıysa) |
| `k8s.cronjob.active_jobs` | Gauge | `{job}` | *workload* (CronJob) |
| `k8s.hpa.current_replicas` · `.desired_replicas` · `.min_replicas` · `.max_replicas` | Gauge | `{pod}` | `k8s.namespace.name`, `k8s.hpa.name`, `k8s.hpa.uid`, `k8s.hpa.scaletargetref.kind`, `k8s.hpa.scaletargetref.name` (`autoscaling/v2`) |
| `k8s.namespace.phase` | Gauge | `1` | `k8s.namespace.name`; 1 Active, 0 Terminating |
| `k8s.resource_quota.hard_limit` · `k8s.resource_quota.used` | Gauge | `{resource}` | `k8s.namespace.name`, `k8s.resource_quota.name`, `k8s.resource_quota.uid`, `resource` (örneğin `limits.cpu`; miktarlar core / bayt / sayı cinsinden) |

Workload status değerleri: Deployment desired = `spec.replicas`, ready = `status.readyReplicas`, available = `status.availableReplicas`,
updated = `status.updatedReplicas`; StatefulSet desired = `spec.replicas`, ready = `readyReplicas`, available = `availableReplicas`,
updated = `updatedReplicas`; DaemonSet desired = `desiredNumberScheduled`, ready = `numberReady`, available = `numberAvailable`,
updated = `updatedNumberScheduled`; Job desired = `spec.completions` (ayarlı değilse 1), ready = `status.ready`, available = `succeeded`,
updated = `failed`; CronJob desired = 0, ready = aktif job'lar, available = 0, updated = 0 (`openlog.k8s.workload.suspended` = `true`/`false`).
Bir Deployment'a ait ReplicaSet'ler ve bir CronJob'a ait Job'lar workload olarak raporlanmaz.

Aralık başına en fazla `kubernetes.cluster.max_pods` (10000) pod ve 5000 workload raporlanır; ajan kestiğinde uyarı loglar.

### 7.5 Kubernetes olayları (cluster modu, OTLP logları) {#_7-5-kubernetes-events-cluster-mode-otlp-logs}

Leader `GET /api/v1/events`'i izler (core/v1, tüm namespace'ler; `410 Gone`'da yeniden liste) ve her yeni veya güncellenmiş olayı
(uid ve `count`/`series.count` ile) bir kez gönderir. Başlangıçta 5 dakikadan eski olaylar atlanır. Log kaydı: `event_name` = `k8s.event`,
`body` = `message`, timestamp = `series.lastObservedTime` → `lastTimestamp` → `eventTime` → `firstTimestamp`, severity `Warning` türü için `WARN` (13),
aksi halde `INFO` (9). Resource = §7.1'in cluster resource'u.

| Log attribute'u | Kaynak |
|---|---|
| `k8s.event.name`, `k8s.event.uid` | olay metadata'sı |
| `k8s.event.reason`, `k8s.event.type` (`Normal`,`Warning`), `k8s.event.action`, `k8s.event.count` (int) | olay |
| `k8s.event.source` | `source.component` / `reportingController` |
| `k8s.namespace.name` | `involvedObject.namespace` (yoksa olay namespace'i) |
| `k8s.object.kind`, `k8s.object.name`, `k8s.object.uid`, `k8s.object.fieldpath` | `involvedObject` |
| `k8s.pod.name`, `k8s.pod.uid` | yalnızca `Pod` nesneleri için |
| `k8s.node.name` | `Node` nesneleri; `Pod` nesneleri için ayarlıysa `source.host` |

### 7.6 Backend entity'leri {#_7-6-backend-entities}

`metrics_local` üzerindeki materialized view'lar (şema 0040–0044, `containers_mv` gibi) tenant başına entity satırları tutar: `k8s_clusters`
(`openlog.k8s.cluster.status`'tan, anahtar `k8s.cluster.uid`), `k8s_nodes` (`openlog.k8s.node.status`), `k8s_workloads`
(`openlog.k8s.workload.status`) ve `k8s_pods` (`openlog.k8s.pod.status`), son noktadan 30 gün sonrasına kadar; artı `logs_local` üzerinde
`resource_attributes['k8s.pod.uid']` ve `attributes['k8s.object.uid']` için bloom filter skip index'leri. Bir node, host'una
`k8s.cluster.name` + `k8s.node.name` host resource attribute'ları üzerinden bağlanır; bir pod container'lara (`containers.container_id`)
`openlog.k8s.pod.containers[].container_id` üzerinden, APM servislerine `apm_service_containers` üzerinden bağlanır (API: [api.md](/tr/contracts/api) "Kubernetes").

## 8. Metrik data point'leri (depolama, tüm resource'lar) {#_8-metric-data-points-storage-all-resources}

Her OTLP metriği, resource'u ne olursa olsun (altyapı ajanı, APM ajanları, OpenTelemetry SDK'ları veya collector'lar) `metrics` tablosunda saklanır (data point başına bir satır);
`service_name`, `host_id` ve `host_name`, varsa `service.name`, `host.id` ve `host.name` resource attribute'larından kopyalanır
(yoksa boş). Tür başına (processor `AddMetrics`, D-119):

| OTLP türü | `metric_type` | Saklanan |
|---|---|---|
| Gauge | `gauge` | `value` |
| Sum | `sum` | `value`, `temporality` (`delta`/`cumulative`), `is_monotonic` |
| Histogram | `histogram` | `count`, `sum`, `value` = ortalama, `explicit_bounds`, `bucket_counts` (len(bounds)+1), `temporality` |
| ExponentialHistogram | `exponential_histogram` | `count`, `sum`, `value` = ortalama, `temporality`; bucket'lar `explicit_bounds`/`bucket_counts`'a dönüştürülür: her işaretin i indeksi (base^i, base^(i+1)] büyüklüklerini kapsar, base = 2^(2^-scale), sıfır bucket'ı [-zero_threshold, zero_threshold]; boşluklar boş bucket olur; -10..20 dışındaki scale'ler veya 1024'ten fazla bucket hiç bucket saklamaz |
| Summary | `summary` | `count`, `sum`, `value` = ortalama, `temporality` = `cumulative`, `quantiles` + `quantile_values` (0081) |

`FLAG_NO_RECORDED_VALUE` taşıyan data point'ler atılır (sebep `no_recorded_value`). D-119 öncesi yazılan satırlarda
exponential histogram `bucket_counts` yalnızca pozitif bucket'ları içerir (sınır yok) ve summary'ler quantile'sızdır.
`metrics_1m` yalnızca gauge ve sum'ları rollup yapar. Saatlik anahtar index'i `attribute_keys` (0080, D-118),
`logs_local`, `spans_local` ve `metrics_local`'ın attribute ve resource attribute map'lerinden doldurulur (metrik adı başına).

## 9. Cloud servis metrikleri (cloud bağlantıları) {#_9-cloud-service-metrics-cloud-connections}

Backend'in bir **cloud bağlantısı** için cloud sağlayıcı API'lerinden topladığı data point'ler
([api.md](/tr/contracts/api#cloud-connections), D-135). Bir ajanın OTLP data point'leriyle tamamen aynı şekilde `metrics` tablosuna
yazılırlar (aynı sütunlar, aynı `series_id` hesabı), böylece Metrics Explorer, metrik alarm kuralları ve
dashboard'lar yönetilen bir veritabanına diğer kaynaklar gibi davranır. Ajan yoktur ve OTLP üzerinden hiçbir şey gönderilmez;
satırları doğrudan `internal/cloudconnect` yazar.

**Metrik adı:** `cloud.<provider>.<service>.<metric>`; burada `<provider>` `aws`, `azure` veya `gcp`,
`<service>` bağlantının servis id'si (`rds`, `s3`, `lambda`, `azure_sql`, `cloud_sql`, …) ve
`<metric>` sağlayıcının kendi metrik adının snake_case'e çevrilmiş halidir: ayraçlar `_` olur, camel ve
kısaltma sınırları bölünür, yani `CPUUtilization` → `cpu_utilization`, `HTTPCode_Target_5XX_Count` →
`http_code_target_5xx_count`, `Percentage CPU` → `percentage_cpu`, `cpu/utilization` → `cpu_utilization`.
Sağlayıcının özgün yazımı data point üzerinde `cloud.metric.name` olarak korunur, yani dönüşümde hiçbir şey
kaybolmaz.

Her nokta `temporality` `unspecified` olan bir **gauge**'dur: sağlayıcı pencereyi zaten toplamıştır, yani
openlog'un sakladığı o timestamp'teki değerdir ve toplama, openlog'un uydurmak zorunda kalacağı bir OTLP temporality'si
olarak değil attribute olarak taşınır. Gauge'lar `metrics_1m` ile rollup yapılır, yani cloud metrikleri
diğer tüm metrikler gibi 395 gün tutulur ([apm.md](/tr/contracts/apm) §8).

### Resource attribute'ları {#resource-attributes}

| Attribute | Zorunlu | Değer |
|---|---|---|
| `cloud.provider` | evet | `aws`, `gcp`, `azure` (§1 aynı değerleri kullanır) |
| `cloud.platform` | evet | `aws_rds`, `aws_s3`, `aws_lambda`, `azure_sql`, `gcp_cloud_sql`, …: katalog servisi başına bir |
| `cloud.region` | hayır | `eu-central-1`; Azure için resource'un location'ı, GCP için resource'un `location`/`zone` label'ı |
| `cloud.account.id` | hayır | AWS account id (poll başına bir kez STS'ten okunur), Azure subscription id, GCP project id |
| `cloud.resource.id` | hayır | Sağlayıcının benzersiz id'si: Azure resource id veya GCP resource adı. CloudWatch bunu döndürmez, yani AWS noktaları yalnızca adı taşır |
| `cloud.resource.name` | hayır | Tekil resource: DB instance tanımlayıcısı, bucket, function, kuyruk |
| `openlog.entity.type` | evet | `cloud_resource` |
| `openlog.cloud.connection.id` | evet | Noktayı toplayan bağlantı |
| `openlog.cloud.connection.name` | evet | Görünen adı, böylece bir grafik join olmadan hesaba göre gruplanabilir |
| `openlog.cloud.service` | evet | Katalog servis id'si (`rds`, `azure_sql`, `cloud_sql`, …) |

`service_name`, `host_id` ve `host_name` boştur: yönetilen bir servis host değildir ve APM
servisi de değildir. Arayüzde gruplama yukarıdaki `cloud.*` resource attribute'larına göredir.

### Data point attribute'ları {#data-point-attributes}

| Attribute | Zorunlu | Değer |
|---|---|---|
| `cloud.metric.name` | evet | Sağlayıcının kendi metrik adı (`CPUUtilization`, `cpu_percent`, `cpu/utilization`) |
| `cloud.metric.stat` | hayır | Değerin temsil ettiği toplama: `Average`, `Sum`, `Maximum`, `Total` |
| `cloud.aws.dimension.<name>` | hayır | Kalan her CloudWatch dimension'ı, adı metrik adı gibi dönüştürülür |
| `cloud.azure.dimension.<name>` | hayır | Her Azure Monitor metadata değeri |
| `cloud.gcp.resource.<label>` | hayır | Kalan her monitored-resource label'ı (`project_id`, `location` ve `zone` zaten resource attribute'u oldu) |
| `cloud.gcp.metric.<label>` | hayır | Her Cloud Monitoring metrik label'ı |

Servis eklemek, yapılandırma değil, bu sözleşme altında bir adla `internal/cloudconnect` içinde kod değişikliğidir:
bir organizasyonun üzerine alarm ve dashboard kurabileceği metrik adlarını sabitleyen katalogdur.

## 10. Real user monitoring (tarayıcı SDK'sı) {#_10-real-user-monitoring-browser-sdk}

Tarayıcı SDK'sının `POST /v1/rum`'a gönderdiği span'ler ([rum.md](/tr/contracts/rum), D-136). Sıradan traces topic'iyle gelir
ve `spans` tablosuna düşerler, yani sonraki aşamalarda RUM'a özel bir yol yoktur; ama üzerlerinde ne bulunabileceği
sayfaya uyacağı güvenilen bir convention değil, **kapalı bir listedir**. `rum.Sanitize` resource'u
tarayıcı anahtarından yeniden kurar ve yalnızca aşağıdaki span attribute'larını tutar; diğer her şey Kafka'ya ulaşmadan
atılır. Bu bölümün rehber gibi değil sözleşme gibi okunmasının sebebi budur: burada olmayan bir attribute
hayatta kalmaz.

Üç tür attribute görünür ve aradaki fark bir adlandırma detayı değil, güvenlik modelidir:

- **Forced (zorlanan)**: sunucu tarafından anahtardan veya istekten yazılır, sayfanın gönderdiğinin üzerine yazar.
  Kopyalanmış bir anahtar bunları başka bir şey olduğunu iddia etmek için kullanamaz.
- **Assigned (atanan)**: sunucu tarafından sayfanın gönderdiğinden türetilir (URL'den normalize edilen route, değerinden
  yeniden hesaplanan vital rating'i), yani sayfa kendi verisinin nasıl bucket'lanacağına karar veremez.
- **Accepted (kabul edilen)**: payload'dan alınır ve sınırlanır. Yalnızca sayfanın kendisini anlatan bilgiler.

### Resource attribute'ları {#resource-attributes-1}

Her istek için yeniden kurulur, sayfanın gönderdiğiyle asla birleştirilmez.

| Attribute | Tür | Değer |
|---|---|---|
| `service.name` | forced | Tarayıcı anahtarının bağlı olduğu uygulama. Asla payload'dan değil: kopyalanmış bir anahtarın backend servisinin adıyla yazmasını engelleyen budur |
| `deployment.environment.name` | forced | Varsa anahtarın environment'ı |
| `openlog.entity.type` | forced | `browser_app`, böylece arayüz ikinci bir tablo olmadan RUM uygulamasını host'tan veya deploy edilmiş servisten ayırır |
| `telemetry.sdk.name` | forced | `openlog-browser` |
| `telemetry.sdk.language` | forced | `webjs` |
| `telemetry.sdk.version` | accepted | Sayfanın raporladığı SDK sürümü, 64 baytla sınırlı. Bilgi amaçlı |
| `browser.brands`, `browser.platform`, `browser.mobile`, `browser.language` | accepted | User-Agent Client Hints, tarayıcının raporladığı gibi |
| `user_agent.original` | accepted | Tam User-Agent dizgisi |
| `os.name`, `os.version` | accepted | Tarayıcının veya mobil platformun raporladığı gibi |
| `service.version` | accepted | Uygulama build'i (mobile-agent.md). `service.name` anahtardan zorlanır, bu zorlanmaz ve bu bilerek böyledir: ad verinin kime ait olduğuna karar verir ve iddia edilebilir olmamalıdır, sürüm ise yalnızca o uygulama içinde bir build'i etiketler; bu konuda yalan söyleyen bir anahtar yalnızca kendi release health'ini kirletir |
| `device.model.identifier`, `device.manufacturer` | accepted | Mobil uygulamanın kendini raporladığı şekliyle cihaz. Tarayıcı ikisini de göndermez |

### Span attribute'ları {#span-attributes}

| Attribute | Tür | Değer |
|---|---|---|
| `openlog.rum.event` | accepted | `page_view`, `vital`, `error`, `resource` veya `custom`. Bunu taşımayan span RUM değildir ve atılır |
| `session.id` | accepted | 32 hex karakter, ziyaret (rum.md §1.1). Bunu taşımayan span atfedilemez ve atılır |
| `openlog.rum.page_view.id` | accepted | Bir page view'ın span'lerini birbirine bağlayan 16 hex karakter |
| `openlog.rum.route` | assigned | SDK'nın route'undan veya URL'den, APM transaction adıyla aynı segment kurallarıyla normalize edilir (rum.md §4). Sayfa kendi rollup anahtarını seçemez |
| `openlog.rum.page_view.kind` | assigned | `load` veya `route_change`; başka her şey `load` olur |
| `openlog.rum.vital.name` | accepted | `lcp`, `cls`, `inp`, `fcp`, `ttfb`. Bilinmeyen vital span'i düşürür |
| `openlog.rum.vital.value` | accepted | Ölçüm, `0`–`1e9` |
| `openlog.rum.vital.rating` | assigned | `good`/`needs_improvement`/`poor`, değerden yeniden hesaplanır: sayfa kendini puanlayamaz |
| `openlog.rum.error.source` | accepted | `error`, `unhandledrejection` veya `console`; başka her şey `error` olur |
| `openlog.rum.custom.name` | accepted | Uygulamanın kendi olay adı. Custom olay için zorunlu |
| `openlog.rum.custom.value`, `…custom.unit` | accepted | Bir zamanlama için isteğe bağlı sayı ve birimi |
| `openlog.rum.custom.param.<key>` | accepted | Olay başına en fazla 16 uygulama tanımlı parametre, anahtarlar `[a-z0-9_.-]`, metin olarak saklanır. Sınırdan önce sıralanır, yani hangi 16'sının kaldığı map sırasına bağlı değildir |
| `device.type` | assigned | `mobile`, `tablet` veya `desktop`, sunucu tarafında türetilir |
| `url.path`, `url.full`, `url.domain` | accepted | Sayfanın kendi URL'i, query ve fragment zaten atılmış |
| `openlog.rum.timing.*` | accepted | Milisaniye cinsinden Navigation Timing fazları: `ttfb_ms`, `dns_ms`, `connect_ms`, `tls_ms`, `response_ms`, `dom_interactive_ms`, `dom_content_loaded_ms`, `load_event_ms` |
| `user.id` | accepted | Uygulamanın kişi için kendi tanımlayıcısı, `identify()` ile ayarlanır ve 128 baytla sınırlıdır. **openlog bunu asla türetmez ve opak bir hesap anahtarını e-posta adresinden ayıramaz**; rum.md §3.7 operatörden birincisini ister |
| `openlog.rum.crash.kind`, `.id`, `.session_unknown` | accepted | Bir `error` span'inde. Kind kapalı bir listeden gelir ([mobile-crash.md](/tr/contracts/mobile-crash) §1) |
| `openlog.rum.visitor.id` | accepted | 32 hex karakter, rastgele kalıcı bir id. Yalnızca anahtar `visitors` modundaysa tutulur ([analytics.md](/tr/contracts/analytics) §1.2) |
| `openlog.analytics.mode` | forced | `sessions` veya `visitors`, anahtardan. `off` modunda yok. Analytics view'ları buna göre filtreler |
| `openlog.rum.referrer.host`, `openlog.rum.utm.source`, `.medium`, `.campaign` | accepted | Analytics'i açık bir anahtarın `page_view`'ında. Referrer yalın host'tur, diğerleri 128 baytla sınırlıdır |
| `geo.country.iso_code` | forced | ISO 3166-1 alpha-2, `OPENLOG_RUM_GEO_HEADER`'ın adlandırdığı header'dan okunur ve sunucu tarafından yazılır. Sayfanın gönderdiği değer atılır: sayfanın gönderebildiği ülke, sayfanın uydurabildiği ülkedir. Güvenilen bir proxy çözmediyse boş; **ziyaretçi adresi asla saklanmaz** |
| `http.request.method`, `http.response.status_code`, `server.address`, `error.type` | accepted | Bir `resource` span'inde, böylece tarayıcı fetch/XHR'ı diğer HTTP client span'leri gibi görünür |
| `openlog.rum.resource.initiator` | accepted | Asset'i yükleyen: `script`, `link`, `img`, `css`, `font`, …; tarayıcının kendi `initiatorType` değeri |
| `openlog.rum.resource.transfer_bytes` | accepted | Ağ üzerinden baytlar, cache yanıtladıysa `0` |
| `openlog.rum.resource.encoded_bytes` | accepted | Aktarılsın ya da aktarılmasın, resource'un encode edilmiş bayt sayısı |
| `openlog.rum.resource.cached` | accepted | Tarayıcı aktarım olmadan sunduysa `true`. Bu olmadan 0 ms'lik bir asset çok hızlı olandan ayırt edilemez |

`sampling.ratio` payload'dan değil tarayıcı anahtarından damgalanır, yani ağırlıklı sayılar kendi örnekleme oranını
iddia eden bir sayfa tarafından şişirilemez (rum.md §3.3).
