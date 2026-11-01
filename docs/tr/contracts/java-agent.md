# Sözleşme: Java ajanı dağıtımı ve güncellemeleri (v1) {#contract-java-agent-distribution-and-updates-v1}

Kararlar: D-072 (OpenTelemetry Java ajanı dağıtımı), D-123 (altyapı ajanı üzerinden güncellemeler). Ajan:
[`agents/java/README.md`](https://github.com/onuragtas/openlog/blob/master/agents/java/README.md). Altyapı ajanı kodu: `agents/infra/internal/javaagent`; backend:
`internal/fleet` (politika bölümü, karar), API: [api.md](/tr/contracts/api) "Fleet".

## 1. Sürüm artifact'i {#_1-release-artifact}

Her ürün sürümü `openlog-javaagent-<v>.jar` ve `.sha256` yayınlar ve jar'ı imzalı `manifest.json` içinde listeler
(releases-updates.md §2): `{"component": "java-agent", "os": "any", "arch": "any", "format": "jar"}`.
Jar manifest'i `Premain-Class` ve `Openlog-Javaagent-Version: <v>` taşır.

## 2. Altyapı ajanı üzerinden dağıtım ve güncellemeler (D-123) {#_2-distribution-and-updates-through-the-infra-agent-d-123}

Altyapı ajanı sabit bir jar yolunu güncel tutar; bunu kendi güncellemelerinin (releases-updates.md §3) ve PHP ajanı kurulumunun
(php-agent.md §7.3) sürüm hattı ve güven kurallarıyla yapar. Uygulama bir kez yapılandırılır:

```sh
JAVA_TOOL_OPTIONS=-javaagent:/opt/openlog/openlog-javaagent.jar   # örneğin PM2 ecosystem dosyasında veya systemd unit'inde
```

### 2.1 Yapılandırma {#_2-1-configuration}

`config.yaml` içinde `java_agent`:

| Anahtar | Varsayılan | Anlamı |
|---|---|---|
| `mode` | `manual` | `off`: fleet kurulumunu kaldır (kullanıcının kendi jar'ını asla) · `manual`: yalnızca JVM envanteri, dosyaları asla değiştirmez · `auto`: kur ve yükselt |
| `version` | `agent` | `agent` (altyapı ajanının kendi sürümü) veya bir SemVer sürümü |
| `remote_config` | `true` | fleet'in `mode` değeri ve çözülmüş hedefi (sync `java_agent`) yerel değerlerin yerine geçer (`<state_dir>/java-agent/remote.json` içinde saklanır) |
| `health_check_after` | `2m` | geçiş ile doğrulaması arasındaki süre (en az `10s`) |
| `install_root` | `/opt/openlog/java-agent` (Windows `C:\Program Files\openlog\java-agent`) | `versions/<v>/openlog-javaagent.jar`, `current` → `versions/<v>` |
| `link_path` | `/opt/openlog/openlog-javaagent.jar` (Windows `C:\Program Files\openlog\openlog-javaagent.jar`) | `-javaagent:` içinde kullanılan sabit yol; `.jar` ile bitmeli ve `install_root` dışında olmalı |

Yetkili adım, `install_root` ve `link_path` değerlerini yalnızca root'un değiştirebildiği bir yapılandırma dosyasından alır (aksi halde
varsayılanları kullanır) ve o dosya `mode: off` ile `remote_config: false` diyorsa kurulumları reddeder.

### 2.2 Düzen ve link geçişi {#_2-2-layout-and-link-switch}

- `<install_root>/versions/<v>/{openlog-javaagent.jar (0644), manifest.json, manifest.json.sig}`, dizinler 0755, her şey root'a ait
  (Windows: SYSTEM/Administrators'a ait, başka kimse yazamaz); install root'ta `.managed-by-openlog-infra-agent` işaretçisi;
  `current` → `versions/<v>` (geçici link + rename ile symlink; Windows: directory symbolic link).
- **Bir jar asla yerinde değiştirilmez.** Çalışan JVM'ler açtıkları dosyayı tutar (jar'ı lazy okurlar; içeriğini altlarında değiştirmek
  onları çökertir). Yeni sürüm yeni bir dosyadır; yalnızca isim değiştirilir.
- `link_path`, Linux ve macOS: mutlak `versions/<v>/openlog-javaagent.jar` yoluna symbolic link; aynı dizinde geçici link olarak oluşturulur
  ve eski linkin üzerine rename edilir. Dizin root'a ait olmalı ve başkaları tarafından yazılamamalı.
- `link_path`, Windows: symbolic link oluşturabilmek varsayılmaz, bu yüzden **kopyadır**: `<link_path>.openlog-new` dosyasına yazılır,
  sonra eski kopyanın üzerine rename edilir (`MoveFileEx(MOVEFILE_REPLACE_EXISTING)`); yönetilen olarak tanınması için sha256'sı kaydedilir.
  Windows JVM'leri jar'ları `FILE_SHARE_DELETE` olmadan açar, yani eski kopyayla başlamış bir JVM çalıştığı sürece rename başarısız olur: yeni
  sürüm kurulur ve `current` geçirilir, link `pending` kalır, rapor `restart_pending` der ve ajan her 30 sn'de yeniden dener (yetkili servis
  sürecinde, yeniden başlatma olmadan). O uygulamaları durdurmak geçişin olmasını sağlar; yeniden denemeden önce tekrar başlatmak eski
  kopyayı bir kez daha yükler. Windows'ta kesintisiz güncellemeler için `-javaagent`'ı
  `C:\Program Files\openlog\java-agent\current\openlog-javaagent.jar` yoluna yönelt (directory link, jar'lar açıkken de değiştirilebilir).
- **Yönetilmeyen `link_path`**: altyapı ajanının oluşturmadığı normal bir dosya (veya `install_root` dışına giden bir link), örneğin
  `install -m 0644 … /opt/openlog/openlog-javaagent.jar` ile elle kurulmuş bir jar, asla değiştirilmez veya kaldırılmaz. `install_root` içine
  kurulum yine yapılır; rapor yönlendirmeyle birlikte `unmanaged` der: `-javaagent`'ı `<install_root>/current/openlog-javaagent.jar` yoluna
  yönelt veya dosyayı kenara taşı (`sudo mv
  /opt/openlog/openlog-javaagent.jar /opt/openlog/openlog-javaagent.jar.manual`; çalışan JVM'ler açık kopyalarını tutar), böylece sonraki
  değerlendirme linki oluşturur. Uygulamaları ancak link var olduktan sonra yeniden başlat: `-javaagent` dosyası eksik olan JVM başlamaz.
- Tutulan sürümler: current, bir önceki, çalışan yönetilen bir JVM'in hâlâ kullandığı her sürüm ve (Windows) bekleyen bir kopyanın tuttuğu
  sürüm; daha eskiler kaldırılır.

### 2.3 Akış {#_2-3-flow}

1. **Envanter** (her 5 dk ve her değişiklikten sonra; değişiklik sync tetikler): kurulum durumu (`current`, işaretçi, link durumu) ve
   JVM'ler (§2.4).
2. **Yetenek**: Linux: unit'in yetkili pre-start adımı bu başlatma için çalışmıştır (aynı invocation'ın `apply-status.json` dosyası), binary
   versions düzeninden çalışır ve sürüm anahtarları vardır. macOS (root olarak LaunchDaemon) ve Windows (LocalSystem olarak servis): servis
   süreci yetkilidir (D-104) ve istekleri kendisi uygular. Container'larda veya dev build'lerinde değil; aksi halde `reason` ile `capable=false`.
3. **Hazırlama** (`mode=auto`, hedef ≠ current, daha önce rollback edilmemiş, başarısız hedef 1 sa içinde değil, süren self-update yok):
   imzalı manifest fleet'ten (`java_agent.manifest`) veya yerelde `version: agent` ile, jar'ı listeliyorsa altyapı ajanı binary'sinin yanındaki
   manifest'ten gelir (deb/rpm/MSI/pkg paketlerine gömülü manifest listelemez: bu host'lar hata kaydetmeden fleet'i bekler). Doğrulama:
   güvenilen anahtarla imza, product/schema, manifest sürümü = hedef, bir `java-agent` `jar` artifact'i (`any`/`any`, en fazla 256 MiB) ve
   sürüm düşürmede hedef ≥ kurulu sürümün manifest'indeki `rollback_floor`. İndirme (manifest'in boyutu ve sha256'sı; lisans anahtarı yalnızca
   ingest host'una), jar `Premain-Class` taşımalı ve belirtiyorsa `Openlog-Javaagent-Version` = hedef olmalı.
   Sonuç: `<state_dir>/java-agent/staged/<v>/{openlog-javaagent.jar, manifest.json, manifest.json.sig}` ve
   `request.json` `{id, action: install, version, in_use, at}` (hepsi güvenilmez).
4. **Devir**: Linux: ajan bir kez çıkar; sonraki başlatmanın `-apply` adımı (ajanın kendi güncelleme adımından ve PHP ajanı adımından sonra)
   isteği işler. macOS/Windows: çalışan servis sürecinde uygulanır.
5. **Yetkili kurulum** (istek id'si başına bir kez; `state_dir` altındaki her şey boyut sınırlarıyla `os.Root` üzerinden okunur; root'a ait
   binary'ye derlenmiş anahtarlarla yeniden doğrulama): jar `versions/.<v>.partial/` içine kopyalanır (boyut ve sha256 kopya üzerinde kontrol
   edilir), `<v>` Java ajanı olarak kontrol edilir, manifest ve imzayla saklanır, root'a ait bir ağaç olmalıdır, `versions/<v>` olarak rename edilir;
   işaretçi yazılır; `current` geçirilir; `link_path` geçirilir (§2.2); temizlik yapılır. İmzalı digest'e sahip mevcut güvenilir bir `versions/<v>`
   yeniden kullanılır (tutulan bir sürüme rollback). Sonuç: root'a ait `<infra install root>/java-agent-status.json` `{request_id, at, action,
   operation, result: applied|failed|rolled_back|uninstalled|rejected, target, version, previous, error, link_state,
   link_version, link_sha256, switches[], link_switches[]}` (`switches`: `current` ve `link_path`'in son 4 geçişi zamanıyla, yüklü sürümleri
   tespit etmekte kullanılır); `rejected` = bir doğrulama veya sahiplik kontrolü başarısız oldu, hiçbir şey değişmedi. İşaretçisi olmayan bir
   install root'a asla dokunulmaz.
6. **Doğrulama** (`applied`'dan `health_check_after` sonra): `current` o sürümü gösterir, jar'ı imzalı sürümdür (tutulan manifest'in sha256'sı) ve
   bir Java ajanıdır, yönetilen link de ona çözülür. Başarısızlıkta ajan `rollback` ister: `current` ve `link_path` kayıtlı önceki sürüme döner
   (önceki yoksa kurulu sürüm kalır ve işlem `failed` olur: jar'ı kaldırmak JVM'lerin başlamasını engellerdi). Rollback edilmiş bir sürüm asla
   otomatik olarak yeniden kurulmaz.
   Otomatik tespit edilmeyen: yeni jar *ile* başarısız olan JVM (altyapı ajanı uygulamaları denetlemez ve ajan hatasını uygulamanın çıkışından
   ayıramaz). Fleet, politikanın `java_agent.version` değerini önceki sürüme sabitleyerek rollback yapar (`rollback_floor` içinde bir sürüm
   düşürme; tutulan sürüm dizini yeniden kullanılır) veya operatör `mode: manual` ayarlayıp `-javaagent`'ı bir sürüm dizinine yöneltir.
7. **Kaldırma**: işaretçili `mode: off`, `uninstall` ister. Çalışan yönetilen bir JVM jar'ı kullanırken reddedilir (kullanılan sürümlerle `error`
   olarak raporlanır, her envanterde yeniden değerlendirilir), sonra yönetilen `link_path` ve install root kaldırılır. Hâlâ
   `-javaagent:<link_path>` ile yapılandırılmış uygulamalar bundan sonra başlamaz.
8. **Asla**: bir JVM'i yeniden başlatmak, ona sinyal göndermek veya attach olmak; bir jar'ı yerinde değiştirmek; kullanıcının jar'ına dokunmak.

### 2.4 JVM bulma ve yüklü sürüm {#_2-4-jvm-discovery-and-loaded-version}

- Linux (`/proc`): çalıştırılabilir dosyası veya `argv[0]` değeri `java` olan ya da komut satırı `-javaagent:` içeren süreçler;
  `/proc/<pid>/environ`'dan `JAVA_TOOL_OPTIONS`, `JDK_JAVA_OPTIONS` ve `_JAVA_OPTIONS`; `/proc/<pid>/fd`'den açık `.jar` dosyaları; `stat`'tan
  başlama zamanı; çalışma dizini; cgroup'tan container. Başka kullanıcıların süreçlerinin `environ`, `fd` ve `cwd` değerleri `CAP_SYS_PTRACE`
  ister (unit verir); o olmadan bu alanlar boştur.
- macOS ve Windows: komut satırı, başlama zamanı ve (mümkün olduğunca) ortamı ve çalışma diziniyle `java`/`javaw` süreçleri; açık dosyalar okunmaz.
- Bir JVM, `-javaagent:` yollarından biri (önce komut satırı, sonra değişkenler) `link_path` ise, `install_root` içindeyse veya
  `openlog-javaagent*.jar` adındaysa raporlanır; göreli yollar çalışma dizinine göre çözülür. En fazla 64 JVM. Komut satırları süreç envanteri gibi maskelenir.
- `managed`: yol yönetilen bir `link_path`'tir veya `install_root` içindedir. Container süreçleri asla yönetilmez (yolları container'a aittir).
- `loaded_version`, sırayla: (1) Linux: açık bir `versions/<v>/openlog-javaagent.jar` (ayrıca `(deleted)`) veya yönetilmeyen bir yol için açık
  dosyanın manifest'i; (2) `versions/<v>` belirten bir yol; (3) yönetilen `link_path`/`current`: geçiş geçmişi, yani süreç başladığında yolun
  gösterdiği sürüm; (4) yönetilmeyen: süreç başladıktan sonra dosya değişmediyse jar'ın `Openlog-Javaagent-Version` değeri. Aksi halde `""` (bilinmiyor).
- `restart_pending`: yönetilen, bir sürüm dizinine sabitlenmemiş ve yüklü sürüm `current`'tan farklı; bilinmiyorsa, süreç yol `current`'a
  geçirilmeden önce başlamış.

### 2.5 Rapor {#_2-5-report}

Sync isteğinin `java_agent` alanı: `mode`, `source` (`local`|`remote`), `capable`, `reason`, `managed`,
`current_version`, `target_version`, `status` (`installed` | `staged` (indirme veya devir sürüyor) |
`restart_pending` (JVM'ler eski sürümü çalıştırıyor veya Windows linki kilitli) | `unmanaged` (`link_path` kullanıcının dosyası) |
`error` (son işlem başarısız, kaldırma çalışan JVM'lerce bloke, link yazılamıyor) | `not_found`), `detail`,
`link_path`, `link_state` (`missing`|`managed`|`unmanaged`|`pending`|`error`), `jvms[] {pid, name, command,
agent_path, loaded_version, managed, restart_pending, started_at, container}`, `update {operation:
install|upgrade|rollback|switch|uninstall, version, state: downloading|restarting|confirming|applied|failed|rolled_back|uninstalled,
error, changed_at}`. Ingest bunu sınırlar (64 JVM, kırpılmış string'ler) ve `agent_hosts.java_agent` içinde saklar.

### 2.6 Fleet politikası (backend) {#_2-6-fleet-policy-backend}

Güncelleme politikasının `java_agent` bölümü `{mode (default manual), version (default agent), changed_at}` ve host başına mod ezmeleri
(`agent_java_host_overrides`, `PUT/DELETE /api/v1/fleet/hosts/{host_id}/java-agent`). Sync,
`java_agent {mode, version, target_version, manifest, signature, download_url, reason}` döner. `auto` modundaki bir host hedefi ve imzalı
manifest'ini şu koşullarda alır: raporu capable ise, hedef `java-agent` jar'ı olan doğrulanmış bir sürümse, o sürümü rollback etmemişse,
geçerli dalgadaysa (`fnv32a(host_id + ":java-agent:" + target) % 100`, `changed_at` ve sürüm zamanının sonuncusundan beri dalga başına
`wave_soak_minutes` sonrasında ulaşılan politika dalgasının altında; host başına `auto` ezmesi dalgaları atlar) ve bir bakım penceresi açıksa.
Karar sebepleri: `offer`, `up_to_date`, `mode_off`, `manual`, `not_reported`, `not_capable`, `invalid_version`, `no_catalog`, `target_unavailable`,
`no_artifact`, `already_failed`, `not_in_wave`, `outside_window`.
