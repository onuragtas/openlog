# Sözleşme: Sürümler, imzalı manifest'ler ve ajan güncellemeleri (v1) {#contract-releases-signed-manifests-and-agent-updates-v1}

Plan ve gerekçe: [../plan/09-releases-updates.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/09-releases-updates.md). Bu doküman sürüm araçları, ajanlar ve backend için bağlayıcıdır.

## 1. Sürümler {#_1-versions}

- Her bileşen için tek bir SemVer 2.0 ürün sürümü, git tag'i `v<version>` (örneğin `v0.4.0`, `v0.5.0-beta.1`).
- Build bunu `-ldflags "-X github.com/onuragtas/openlog/internal/version.Version=<version> -X …Commit=<sha> -X …Date=<RFC3339>"` ile enjekte eder (ajan: `github.com/onuragtas/openlog/agents/infra/internal/version`). Dev build'leri `0.0.0-dev+<commit>` bildirir.
- Ön sürümler `beta` kanalına aittir; düz sürümler `stable`'a (ve `beta`'da da görünür).
- Karşılaştırmalar SemVer önceliğini kullanır. Build metadata'sı yok sayılır.

## 2. Sürüm manifest'i {#_2-release-manifest}

`manifest.json` (UTF-8, BOM yok), ham byte'lar olarak imzalanır:

```json
{
  "schema": 1,
  "product": "openlog",
  "version": "0.4.0",
  "channel": "stable",
  "released_at": "2026-10-01T12:00:00Z",
  "notes_url": "https://github.com/onuragtas/openlog/releases/tag/v0.4.0",
  "compatibility": {
    "min_backend_for_agent": "0.2.0",
    "oldest_supported_agent": "0.2.0",
    "min_upgrade_from": "0.3.0",
    "rollback_floor": "0.3.0"
  },
  "artifacts": [
    {"component": "infra-agent", "os": "linux", "arch": "amd64", "format": "tar.gz",
     "name": "openlog-infra-agent_0.4.0_linux_amd64.tar.gz",
     "url": "https://github.com/onuragtas/openlog/releases/download/v0.4.0/openlog-infra-agent_0.4.0_linux_amd64.tar.gz",
     "sha256": "…64 hex…", "size": 4123456}
  ],
  "images": {"openlog": "ghcr.io/onuragtas/openlog@sha256:…"},
  "helm_chart": {"name": "openlog-0.4.0.tgz", "url": "…", "sha256": "…"},
  "helm_charts": {"openlog": {"name": "openlog-0.4.0.tgz", "url": "…", "sha256": "…"},
                  "openlog-agent": {"name": "openlog-agent-0.4.0.tgz", "url": "…", "sha256": "…"}},
  "migrations": {"postgres": {"latest": 3, "contract_pending": []}, "clickhouse": {"latest": 7, "contract_pending": []}}
}
```

- `component` değerleri: `infra-agent`, `backend`, `php-agent` (php-agent.md §7.1: her PHP ABI'nin modülleri ve `openlog-php-install` ile
  `openlog-php-agent_<v>_linux_<arch>.tar.gz`, artı `.deb`/`.rpm`/`.apk`), `java-agent`
  (`openlog-javaagent-<v>.jar`, platformdan bağımsız: `os` ve `arch` `any`), `node-agent`
  (`openlog-node-<v>.tgz`, `openlog-node`'un `npm pack` çıktısı), `python-agent` (`openlog_agent-<pep440 v>-py3-none-any.whl`)
  ve `dotnet-agent` (`OpenLog.Agent.<v>.nupkg`), üçü de `os`/`arch` `any` ile, ve `compose`
  (`openlog-compose-<v>.tar.gz`, `os`/`arch` `any`, `format` `tar.gz`: `deploy/compose`'un git'te izlenen dosyalarıyla tek üst dizin `openlog-compose-<v>/`:
  `x-openlog-compose-version: "<v>"` damgalı `docker-compose.yml`,
  `.env.example`, `clickhouse/`, `README.md`; yalnızca normal dosyalar ve dizinler, asla `.env`, `backups/` veya
  `releases/` değil; install-server.sh kurar ve `openlog-updater` senkron tutar, D-111). `format`: `tar.gz`, `deb`,
  `rpm`, `apk`, `jar`, `tgz`, `whl`, `nupkg`. Tüketiciler bilmedikleri bileşen ve formatları yok sayar, yani yenileri `schema`'yı değiştirmez.
- Helm chart'ları: `helm_charts`, chart adını (`openlog`, `openlog-agent`) paketlenmiş chart `<chart>-<version>.tgz`'ye eşler
  (chart `version` = `appVersion` = sürüm); `name`, `url` ve `sha256` zorunludur.
  `helm_chart` korunur ve `helm_charts`'tan eski tüketiciler için her zaman `helm_charts.openlog`'a eşittir. İkisi de
  isteğe bağlıdır (helm olmadan derlenen sürümde ikisi de olmaz). `helm_charts` `schema` değişikliği olmadan eklendi: manifest'ler bilinmeyen alanları
  reddetmeden decode edilir, yani eski binary'ler böyle manifest'leri değişiklik olmadan doğrular ve kullanır (imza yeni alan dahil ham byte'ları kapsar).
- Tarball düzeni: `openlog-infra-agent`, `LICENSE`, `README.md`, `packaging/` içeren tek üst dizin `openlog-infra-agent_<v>_linux_<arch>/`.
- `compatibility.min_upgrade_from`: bundan eski ajan/backend'ler bir ara sürümden geçerek yükselmelidir.
- `compatibility.rollback_floor`: bu sürümden rollback'in hedefleyebileceği en düşük sürüm.

Platform başına altyapı ajanı artifact'leri (D-104): `linux` amd64/arm64 `tar.gz`, `deb`, `rpm`; `darwin` amd64/arm64 `tar.gz` ve `pkg`; `windows` amd64/arm64 `zip` ve `msi`. Arşivler tek üst dizin `openlog-infra-agent_<v>_<os>_<arch>/` içerir (binary `openlog-infra-agent`, Windows'ta `openlog-infra-agent.exe`). Self-update Linux ve macOS'ta `tar.gz`, Windows'ta `zip` kullanır (kural 3 o formatı arar); `deb`, `rpm`, `msi` ve `pkg` yalnızca kurulum programları içindir. Tüketiciler bilmedikleri formatları yok sayar.


### İmza {#signature}

- `manifest.json.sig`: bir veya daha fazla `openlog-sig-v1 <key_id> <base64 ed25519 signature of manifest.json bytes>` satırı içeren metin dosyası.
- `key_id` = ham 32 byte public key'in SHA-256'sının ilk 8 byte'ı, küçük harf hex (16 karakter).
- **Herhangi bir** satır güvenilen bir anahtarla doğrulanırsa manifest geçerlidir. Doğrulama kodu: Go modülü `github.com/onuragtas/openlog/libs/release` (Apache-2.0, ajanlar, backend ve sürüm araçları kullanır).
- **Güvenilen anahtarlar** (public, gizli değil) build sırasında derlenir: `-X <module>/internal/release.trustedKeys=<base64>,<base64>` (backend `github.com/onuragtas/openlog/internal/release`, ajan `github.com/onuragtas/openlog/agents/infra/internal/release`). CI bunları repository variable'ı `OPENLOG_RELEASE_PUBLIC_KEYS`'ten alır. En fazla 2 aktif anahtar.
- Testler ve kendi derlenen dağıtımlar için ek bir anahtar dosyası (satır başına bir base64 anahtar) yapılandırılabilir: backend `OPENLOG_RELEASE_TRUSTED_KEYS_FILE`, ajan `release.trusted_keys_file`. **Derlenmiş anahtarı ve anahtar dosyası olmayan binary'ler güncelleme uygulayamaz ve duyuramaz** (sync yine çalışır; güncelleme istekleri `error="no trusted release keys"` ile `failed` olarak raporlanır).
- Private key asla repo'da yaşamaz. CI onu `OPENLOG_RELEASE_SIGNING_KEY` secret'ından okur (base64 32 byte seed). Anahtarlar `openlog-release keygen` ile üretilir; dev/test anahtarları gerçek sürümlerde asla kullanılmamalıdır.

### Index {#index}

`index.json` + `index.json.sig` (aynı imza biçimi) kanal başına sürümleri listeler, en yenisi başta:

```json
{"schema": 1, "product": "openlog", "generated_at": "…",
 "channels": {"stable": [{"version": "0.4.0", "manifest_url": "…/v0.4.0/manifest.json"}],
              "beta":   [{"version": "0.5.0-beta.1", "manifest_url": "…"}]}}
```

Varsayılan konum: `https://github.com/onuragtas/openlog/releases/latest/download/index.json` (her sürüme eklenir, yani `latest` her zaman en yenisini sunar). `OPENLOG_RELEASE_INDEX_URL` ile ayarlanabilir. Index yalnızca bir ipucudur. Her manifest kendi başına doğrulanır.

## 3. Ajan ↔ backend: `POST /v1/openlog/agent/sync` {#_3-agent-↔-backend-post-v1-openlog-agent-sync}

OTLP HTTP portunda `openlog-ingest` sunar, OTLP ile aynı kimlik doğrulama (`openlog-license-key` / Bearer). JSON.

### İstek {#request}

```json
{
  "host_id": "…", "host_name": "web-1",
  "agent": {"name": "openlog-infra-agent", "version": "0.3.0", "commit": "abc123",
            "os": "linux", "arch": "amd64",
            "install_method": "tarball|deb|rpm|container|dev",
            "update_capable": true},
  "update": {"state": "idle|downloading|verifying|staged|restarting|confirming|succeeded|failed|rolled_back",
             "from_version": "0.2.0", "to_version": "0.3.0", "error": "", "changed_at": "…"},
  "config_hash": "sha256:…",
  "integrations_config_revision": "sha256:…"
}
```

`integrations_config_revision`: ajanın uyguladığı uzak entegrasyon yapılandırmasının revision'ı (`""` = yok;
ajan yapılandırmasında `integrations.remote_config: false` ise `"disabled"`). Host üzerinde saklanır (`agent_hosts`, en fazla
128 byte) ve `GET /api/v1/integrations/settings?host_id=` tarafından `applied_revision` olarak gösterilir.

### Yanıt {#response}

```json
{
  "poll_interval_seconds": 300,
  "server_version": "0.4.0",
  "update": null,
  "integrations_config": null
}
```

Veya politika bu host'u güncelleme için seçtiğinde:

```json
{
  "poll_interval_seconds": 300,
  "server_version": "0.4.0",
  "update": {
    "action": "upgrade|rollback",
    "target_version": "0.4.0",
    "manifest": "<base64 of manifest.json bytes>",
    "signature": "<contents of manifest.json.sig>",
    "download_url": "https://github.com/…/openlog-infra-agent_0.4.0_linux_amd64.tar.gz",
    "rollout_id": "…",
    "not_before": "2026-10-01T02:00:00Z",
    "deadline": "2026-10-01T04:00:00Z"
  }
}
```

- `download_url` sürüm kaynağını veya bir backend mirror'ını (ingest üzerinde `/v1/openlog/releases/<version>/<name>`) gösterebilir. URL ne olursa olsun ajan **imzalı manifest'in** sha256 ve boyutuyla doğrular.
- `poll_interval_seconds`: sunucu kontrollü, ajan [60, 3600] aralığına sıkıştırır, ±%10 jitter. Ingest
  `OPENLOG_FLEET_SYNC_INTERVAL` (300 sn) döner; aktif bir rollout'un sonraki bir dalgada güncelleyeceği host'lara (karar `not_in_wave`)
  `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` (60 sn) döner, böylece sonraki dalga veya "Deploy now"
  (`POST /api/v1/fleet/rollouts/{id}/deploy-now`) onlara yaklaşık bir dakika artı politika cache TTL'i içinde ulaşır.
- Bilinmeyen alanlar iki tarafta da yok sayılır. Eski backend'den `404` veya `501` → ajan yeniden başlayana kadar sync'i kapatır ve bir kez loglar.

#### Uzak entegrasyon yapılandırması {#remote-integration-config}

Host'un geçerli entegrasyon ayarlarının (`/api/v1/integrations/settings` altında düzenlenir, `integration_settings` içinde saklanır) revision'ı isteğin
`integrations_config_revision` değerinden farklıysa cevap onları taşır:

```json
"integrations_config": {
  "revision": "sha256:4f1c…",
  "items": [
    {"integration": "redis", "enabled": true, "endpoint": "127.0.0.1:6379", "username": "default",
     "password": "<plaintext>", "database": "", "databases": []},
    {"integration": "nginx", "match": {"port": 8080, "container": "", "endpoint": "", "instance": ""},
     "enabled": true, "endpoint": "http://127.0.0.1:8080/nginx_status", "username": "", "password": "",
     "database": "", "databases": []}
  ]
}
```

- `null` (veya eski sunuculardan yokluk) = mevcut yapılandırmayı koru. Revision değişmediğinde, `OPENLOG_AUTH_MODE=static` ile, PostgreSQL kullanılamazken ve cache'te bir şey yokken veya
  bir parola çözülemediğinde de `null`'dır (ingest secret'lar olmadan uyarı loglar; `openlog_agent_sync_integrations_config_total{result="error"}`).
- `items` eksiksiz uzak yapılandırmadır (öncekinin yerine geçer; `[]` = uzak ayar yok) ve uygulama sırasındadır: önce tüm host'lar için ayarlar, sonra bu host için ayarlar; her grup içinde önce `match` olmayan ayarlar; eşitlikte
  oluşturma zamanı. Ayar her instance'a uygulanıyorsa `match` çıkarılır; ayarlanmamışsa `port` çıkarılır. Bir entegrasyonun kullanmadığı alanlar boştur.
- Revision = `"sha256:"` + her parolası saklanan ciphertext'iyle değiştirilmiş JSON `items` dizisinin hex sha256'sı, yani yeni parola dahil her değişiklikte değişir (yeni anahtarla yeniden şifrelemek de değiştirir).
- Ajan item'ları uygular, revision ile saklar (mod 0600) ve sonraki her sync'te revision'ı bildirir. `"disabled"` asla eşleşmez, yani böyle ajanlar nesneyi her sync'te alır ve yok sayar.
- Parolalar, organizasyonun lisans anahtarıyla doğrulanmış ajanlara (TLS) ingest bağlantısı içinde düz metin olarak gönderilir; saklanırken PostgreSQL'de `OPENLOG_SECRETS_KEY` ile şifrelenir, bu yüzden ingest'in de buna ihtiyacı var.
- Değişiklikler bir ajana `OPENLOG_FLEET_POLICY_CACHE_TTL` artı bir poll aralığı içinde ulaşır.

#### PHP ajanı {#php-agent}

Ajanlar PHP runtime'larını ve PHP ajanı kurulumlarını istekte bildirir (`php_agent`, php-agent.md §7.3 madde 8;
ingest sınırlar: 64 runtime, kırpılmış string'ler) ve ingest host'un fleet ayarlarını döner (politika deposu yoksa null):

```json
"php_agent": {"mode": "auto", "version": "agent", "reload": "graceful", "exclude_bins": [],
  "target_version": "0.4.0", "manifest": "<base64 of manifest.json>", "signature": "…",
  "download_url": "https://…/openlog-php-agent_0.4.0_linux_amd64.tar.gz", "reason": "offer"}
```

- `mode` host'un ezmesi veya politikanınkidir; `target_version` `reason=offer` (manifest, imza ve indirme URL'i ile, açıksa backend mirror'ı) ve `reason=up_to_date` (onlarsız) ile ayarlanır; aksi halde `""`'dir ve
  ajan kurulu olanı tutar. Karar sebepleri: api.md "Fleet" (`php_agent.status`).
- `php_agent.remote_config: false` olmadıkça ajan bu ayarları `config.yaml` yerine uygular ve manifest'i bir güncelleme talimatı gibi doğrular (imza, şema, sürüm, platformu için bir `php-agent` `tar.gz` artifact'i,
  sürüm düşürmede kurulu PHP ajanının rollback floor'u).

#### Java ajanı {#java-agent}

Altyapı ajanı, host'ta `openlog-javaagent.jar`'ı güncel tutmak için `java-agent` jar artifact'ini (os/arch `any`) kullanır
([java-agent.md](/tr/contracts/java-agent) §2, D-123). Ajanlar JVM'leri ve yönetilen jar'ı istekte bildirir (`java_agent`,
java-agent.md §2.5; ingest sınırlar: 64 JVM) ve ingest host'un fleet ayarlarını döner (politika deposu yoksa null):

```json
"java_agent": {"mode": "auto", "version": "agent", "target_version": "0.4.0", "manifest": "<base64 of manifest.json>",
  "signature": "…", "download_url": "https://…/openlog-javaagent-0.4.0.jar", "reason": "offer"}
```

- `target_version` `reason=offer` (manifest, imza ve indirme URL'i ile, açıksa backend mirror'ı) ve `reason=up_to_date` ile ayarlanır; aksi halde `""`'dir ve ajan kurulu olanı tutar.
- `java_agent.remote_config: false` olmadıkça ajan bu ayarları `config.yaml` yerine uygular ve manifest'i bir güncelleme talimatı gibi doğrular (imza, şema, sürüm, bir `java-agent` `jar` artifact'i,
  sürüm düşürmede kurulu jar'ın rollback floor'u).

### Ajan doğrulama kuralları (hepsi geçmeli, yoksa güncelleme reddedilir ve `failed` olarak raporlanır) {#agent-verification-rules-all-must-pass-or-the-update-is-rejected-and-reported-as-failed}

1. Güvenilen bir anahtarla geçerli imza.
2. `manifest.product == "openlog"`, `schema == 1`, `manifest.version == target_version`.
3. `component=infra-agent`, kendi `os`/`arch` ve `format=tar.gz` için bir artifact var.
4. `action=upgrade`: `target_version > current` ve `current >= compatibility.min_upgrade_from`.
5. `action=rollback`: `target_version < current` ve `target_version >= rollback_floor`; floor **şu anda çalışan** sürümün manifest'inden alınır (kendi kurulum/güncellemesinden diskte tutulur; bilinmiyorsa → reddet).
6. İndirilen boyut ve sha256 eşleşir. Arşiv açma mutlak yolları, `..`, symlink'leri ve beklenen dizin dışındaki dosyaları reddeder.
7. `<new binary> -self-test -config <config>` 30 sn içinde 0 ile çıkar.
8. `update_capable=false` (container, dev) → asla uygulama; istenirse `error="not update capable"` ile `failed` raporla. Bu kural (ve trusted-keys kontrolü) kural 5'ten hemen sonra, herhangi bir indirme veya disk yazmadan **önce** değerlendirilir.

Ek ajan davranışı:
- Container tespiti: `/.dockerenv`, `/run/.containerenv`, container cgroup'u veya `OPENLOG_AGENT_CONTAINER=1`; `OPENLOG_AGENT_CONTAINER=0` tespiti kapatır (container içinde tarball düzenini çalıştıran test kurulumları için).
- `update_capable` ayrıca `update.enabled`, güvenilen anahtarlar, var olan bir `current` symlink'i ve ya bu başlatma için çalışmış yetkili pre-start adımını (staged modu) ya da ajanın yazabildiği bir install root'u (legacy modu) ister. deb/rpm için paketin adı `openlog-infra-agent` olmalıdır.
- İsteğe bağlı istek alanları (eski backend'ler yok sayar): `agent.update_mode` (`staged` | `legacy` | yok), `agent.update_notice` (operatör eylemi, örneğin unit pre-start adımından eskiyse `"unit outdated: …"`) ve son reconcile'dan `reconcile` `{version, at, unit_changed, docker: added|member|opt_out|no_group|error, php_access: added|member|opt_out|error, error}`.
- `php_access` (isteğe bağlı; host'ta PHP-FPM pool dosyası yoksa bulunmaz): `{socket_group, group, group_exists, agent_member, grants: auto|opted_out, pools: [{pool, php_version, user, unit, access: ok|missing|opted_out|unsupported_user}]}`: hangi PHP-FPM pool'larının `php.sock`'a gönderebileceği (php-agent.md §1), yetkisiz ajan tarafından her sync'te `/etc/group`, `/etc/passwd` ve pool dosyalarından değerlendirilir (en fazla 512 pool). `missing` pool'lara sonraki reconcile'da (servis yeniden başlatma) izin verilir. Backend son raporu host başına saklar (`agent_hosts.php_access`) ve `FleetHost.php_access` olarak döner.
- Kurulum programları (deb/rpm/`install.sh`) `manifest.json` ve `manifest.json.sig` dosyalarını `versions/<v>/` içine koyar; onlar olmadan o sürümden rollback reddedilir (floor bilinmiyor). Bir deb/rpm yayınlanan manifest'i içeremez (paketin kendi sha256'sını listeler), bu yüzden paketler aynı sürüm için aynı anahtarla imzalanmış, yalnızca ajan tarball'larını listeleyen ikinci bir manifest gömer; `version`, `channel`, `released_at`, `compatibility` ve `images` yayınlananla aynıdır. `install.sh` tarball kurulumları yayınlanan manifest'i saklar.
- Aynı action/sürüm/rollout ile başarısız olan talimat 1 sa boyunca yeniden denenmez; rollback edilmiş bir sürüm asla otomatik yeniden denenmez.
- İndirmeler lisans anahtarını yalnızca URL host'u ingest endpoint host'una eşitse gönderir.
- Sınırlama: staged modunda başlatma denemelerini `-apply` sayar, yani `main`'i erken çöken bir aday, adayın kendi `-apply`'ı çalıştığı sürece (geçişten hemen sonra başarıyla çalıştı) yine rollback olur. Binary'si hiç başlayamayan bir aday restart döngüsüne girer (`StartLimitIntervalSec=0`) ve sonraki sürümü veya elle müdahaleyi bekler.

### Güncelleme durum makinesi ve rollback {#update-state-machine-and-rollback}

Durum dosyası `<state_dir>/update-state.json`: `{previous, candidate, attempts, staged_at, confirmed}`, artı staged modunda `{staged, confirmed_version, confirmed_at, rollback_request, rollback_reason}`.

Staged modu (yetkili pre-start adımı olan unit, aşağıya bak):
- **Stage (ajan, yetkisiz):** kural 1-8'den sonra sandbox içinde indir, aç ve self-test yap; `archive.tar.gz`, `manifest.json`, `manifest.json.sig` dosyalarını `<state_dir>/updates/<v>/` içinde tut; `{staged: v, candidate: v, previous, staged_at}` durumunu yaz; 0 ile çık.
- **Apply (root, sonraki başlatma):** yeniden doğrula ve kur (aşağıya bak), adayı `{version, previous, switched_at, attempts: 1}` olarak root'a ait `apply-status.json` içine kaydet, `current`'ı geçir, yeni binary'nin `-reconcile`'ını çalıştır.
- **Başlangıç:** `-apply` her başlatmada kayıtlı adayının `attempts` değerini artırır (değişen unit'in yol açtığı tek restart için değil). `attempts > 3`, `now - switched_at > 5 min` ise veya çalışan aday `rollback_request` ayarladıysa (watchdog'u: 5 dk içinde onay yok), `current`'ı kayıtlı `previous`'a geri alır (root'a ait bir ağaç olmalı) ve onun `-reconcile`'ını çalıştırır. Ajan başlatmasının sonucunu (`$INVOCATION_ID`'li `apply-status.json`: `switched`, `rejected`, `rolled_back`, `rollback_failed`) raporlanan duruma eşler.
- **Onay:** adayda ilk başarılı OTLP export'tan sonra `confirmed`, `confirmed_version`, `confirmed_at` ayarla, `succeeded` raporla. Sonraki `-apply` onayı yalnızca `confirmed_at` kendi `switched_at`'inden sonraysa kabul eder, adayı unutur ve current + previous (+ deb/rpm paket sürümü) dışındaki sürümleri temizler.

Legacy modu (pre-start adımı olmayan unit; yalnızca `versions/` ajanın yazabildiği sürece):
- **Stage:** `<install_root>/versions/<v>/` içine aç, self-test yap, `{candidate, previous, attempts: 0}` durumunu yaz, `current` symlink'ini atomik değiştir (geçici symlink oluştur + rename), 0 koduyla çık (systemd yeniden başlatır).
- **Başlangıç (`main`'de ilk iş):** `candidate == running version` ve `confirmed` değilse `attempts`'ı artır. `attempts > 3` veya `now - staged_at > 5 min` ise: `current`'ı `previous`'a geri al, durumu `rolled_back` yap, çık (previous ile yeniden başla).
- **Onay:** adayda ilk başarılı OTLP export'tan sonra `confirmed: true` ayarla, `succeeded` raporla, current + previous dışındaki sürümleri temizle.
- `install_root` varsayılanı `/opt/openlog/infra-agent`. Bu düzene sahip olmayan legacy kurulumlar, kurulum programı/paket taşıyana kadar `update_capable=false` bildirir.

### Yetkili apply ve reconcile {#privileged-apply-and-reconcile}

Self-update, yeni paketi kurmaya veya `install.sh`'ı yeniden çalıştırmaya denk olmalı: unit değişiklikleri, grup üyelikleri, sahiplik ve gelecekteki kurulum adımları self-update olmuş host'lara ulaşmalı. Ajan `openlog-agent` olarak çalışır ve root olamamalı, yani bunları kendisi kuramaz ve root, ajan kullanıcısının yazmış olabileceği hiçbir şeyi asla çalıştırmamalı.

- **Sahiplik:** `<install_root>`, `versions/`, her sürüm ağacı ve `current` `root:root`'tur, başkaları yazamaz; unit ajana orada yazma hakkı vermez (`ReadWritePaths=/var/lib/openlog-infra-agent`). `state_dir` `openlog-agent` 0750; `config.yaml` `root:openlog-agent` 0640.
- **Unit:** `ExecStartPre=-+/opt/openlog/infra-agent/current/openlog-infra-agent -apply -config /etc/openlog-infra-agent/config.yaml` (`+`: root, `User=` ve sandbox olmadan; `-`: başlatmayı asla bloke etmez), `TimeoutStartSec=180`. Çalıştırılan binary, root'a ait geçerli sürümdür.
- **`-apply`** (her zaman 0 ile çıkar; ağ yok; versions düzeni dışında veya root değilken no-op): (1) düzeni güvenli hale getir: install root ve `versions/` root'a ait 0755; root'a ait olmayan, başkalarının yazabildiği, symlink veya özel dosya içeren sürüm ağacı güvenilmezdir: geçerli olan (yalnızca dizinler ve normal dosyalar, `os.Root` üzerinden) root'a ait bir ağaca kopyalanır, diğerleri silinir; güvenilmez durum dosyaları silinir. (2) Aday kontrolü ve rollback (yukarıda). (3) `state_dir` altındaki her dosya düşmanca sayılarak staged güncelleme: sürüm adı SemVer olmalı, action `upgrade`/`rollback`; dosyalar `os.Root(state_dir)` üzerinden açılır (symlink'ler kaçamaz), boyut sınırları içinde normal dosya olmalı (FIFO değil) (manifest 1 MiB, imza 64 KiB, arşiv = imzalı boyut); bu binary'ye derlenmiş anahtarlar ve kendi sürüm/manifest'iyle kural 1-5; arşiv `versions/.<v>.partial` içine kopyalanır ve boyut + sha256 kopya üzerinde kontrol edilir (digest bildirilmez); root olarak kural 6 ile açma; açılan ağaç güvenilir olmalı; adayın `openlog-agent` olarak `-self-test`'i (uid/gid `/etc/passwd`'den, ek grup yok; kural 7); `versions/<v>` olarak rename; geçiş. Bir staged güncelleme bir kez işlenir (`handled_staged_at`). (4) Başlayan sürümün `-reconcile`'ı (geçiş veya rollback sonrası re-exec, yani yeni sürümün kodu çalışır). Yapılandırma ve `release.trusted_keys_file` yalnızca kendileri ve tüm üst dizinleri yalnızca root tarafından değiştirilebiliyorsa kullanılır; aksi halde varsayılanlar ve derlenmiş anahtarlar.
- **`-reconcile`** (`-apply`, deb/rpm postinstall ve `install.sh` çalıştırır; idempotent; kurulum programları için `restart-required` yazdırır): eksikse hesabı oluştur; düzeni güvenli hale getir; state dizini ve config sahiplik/modlarını düzelt (config içeriği asla yazılmaz); binary'ye gömülü unit'i (`agents/infra/packaging/systemd/openlog-infra-agent.service`, paketlerin içerdiği dosya) içeriği farklıysa kur: deb/rpm: paketli `/usr/lib/systemd/system` yolu (paket yükseltmesi onun yerine geçer ve postinstall'ı geçerli, muhtemelen daha yeni binary ile yeniden reconcile eder; daha yeni self-update edilmiş sürüm çalışırken `dpkg --verify`/`rpm -V` onu değişmiş bildirebilir), `/etc/systemd/system/openlog-infra-agent.service` onu tamamen eziyorsa atlanır; tarball: `/etc/systemd/system`; drop-in'lere asla dokunulmaz; sonra `systemctl daemon-reload`; docker grup üyeliği (D-040 kuralları ve opt-out'lar); PHP socket grubu (D-103, php-agent.md §1): `openlog-php` sistem grubunu oluştur ve `openlog-agent` ekle, sonra `php_forwarder.grant_pool_users: false` (root'a ait config), `/etc/openlog-infra-agent/no-php-access` veya `OPENLOG_AGENT_PHP_ACCESS=0` (o dosyaya kaydedilir) olmadıkça, var olan her root olmayan PHP-FPM pool kullanıcısını ve Apache/nginx varsa `www-data`/`apache`/`nginx` ekle; üyeler asla çıkarılmaz; yalnızca kullanıcıları yeni eklenen aktif PHP-FPM (ve Apache) servisleri `systemctl reload` alır (asla restart); tarball kurulumları için `/usr/bin` symlink'i. Sonuç: root'a ait `<install_root>/reconcile-status.json` (`php_access: {group, agent, grants, added, failed, reloaded, not_active}`), `reconcile` olarak raporlanır. Kurulum programları ne yaptığını yazdırır (adım stderr'e loglar). Sonradan oluşturulan yeni pool'lara sonraki `systemctl restart openlog-infra-agent`'ta izin verilir; timer yok.
- **Yeni unit için restart:** `-apply`'ın reconcile'ı unit'i değiştirdiğinde, o invocation'da başlatılan ajan çalışmadan önce bir kez 0 ile çıkar, böylece systemd onu yeni tanımla yeniden başlatır. Döngü koruması: sonraki başlatmada yeniden yazılan aynı unit içeriği için tekrar olmaz. Yeni ek gruplar restart gerektirmez (ana süreç `-apply`'dan sonra oluşturulur ve systemd `User=`'ın gruplarını her exec için `initgroups` ile çözer, yani pre-start adımının eklediği `docker`/`openlog-php` üyelikleri aynı başlatmanın ajanına uygulanır; `package`/`install` bağlamları bunun yerine `restart-required` yazdırır).
- **Mevcut kurulumlar (bootstrap):** bu adımdan önceki unit'lerde `ExecStartPre` yok; ajan `-apply`'ın kendi başlatması için çalışmadığını tespit eder (`apply-status.json` `invocation_id` ≠ `$INVOCATION_ID`), `versions/` yazılabilir olduğu sürece legacy yalnızca-binary güncellemeyi sürdürür ve `update_notice: "unit outdated: …"` ile `update_mode: legacy` bildirir. Bir paket yükseltmesi veya `install.sh` çalıştırması yeni unit'i kurar ve düzeni güvenli hale getirir. Legacy self-update'in yazdığı geçerli sürüme kurulum programları güvenmez: postinstall paket sürümüne geçer, `install.sh` çalışan sürümü sürüm kaynağından yeniden kurar.
- **Kalan güven:** bootstrap, root'un onu ilk çalıştırdığı anda geçerli olan binary'ye güvenir (ajan kullanıcısı taşınmadan önce ele geçirilmiş bir host bununla kurtulmaz). Docker grup üyeliği root'a denk kalır (D-040).

### macOS ve Windows servisleri (D-104) {#macos-and-windows-services-d-104}

launchd ve Windows Service Control Manager'ın yetkili pre-start kancası yok, bu yüzden ajan orada kendisi yetkili çalışır ve aynı apply adımını servis sürecinin başında uygular:

- **Kimlik ve düzen:** macOS: `/opt/openlog/infra-agent/current/openlog-infra-agent`'ı root olarak çalıştıran LaunchDaemon `/Library/LaunchDaemons/org.openlog.infra-agent.plist` (`KeepAlive`, ortam `OPENLOG_SERVICE_MANAGER=launchd`); install root, config (`/etc/openlog-infra-agent/config.yaml`, root 0600) ve state dizini (`/var/lib/openlog-infra-agent`, root 0700) Linux'taki gibi; log `/var/log/openlog-infra-agent.log` (newsyslog). Windows: LocalSystem olarak `openlog-infra-agent` servisi, otomatik (gecikmeli) başlatma, ImagePath `"C:\Program Files\openlog\infra-agent\current\openlog-infra-agent.exe" -config "C:\ProgramData\openlog\infra-agent\config.yaml"`, kurtarma: 5 sn/10 sn/30 sn sonra yeniden başlat, crash dışı hatalarda da; `current` bir directory symbolic link'tir; `C:\ProgramData\openlog\infra-agent` (config, `state\`, `logs\`) yalnızca SYSTEM ve Administrators'a izin veren korumalı bir ACL'e sahiptir.
- **Güven kontrolleri:** POSIX sahiplik/mod kontrolleri macOS'ta değişmez (root:wheel). Windows bunları ACL kontrolleriyle değiştirir: güvenilir bir dosya veya sürüm ağacı LocalSystem, Administrators veya TrustedInstaller'a aittir ve DACL'i başka kimseye yazma, silme veya izin hakkı vermez; üst dizinler başka principal'ların girdi oluşturmasına izin verebilir (C:\ ve C:\ProgramData verir) ama silmesine, yeniden adlandırmasına veya izinlerini değiştirmesine izin vermez.
- **Başlatma:** servis süreci (launchd: `OPENLOG_SERVICE_MANAGER=launchd` ve euid 0; Windows: SCM başlatır) bir invocation id üretir, `INVOCATION_ID` ayarlar ve çalışan (güvenilir) binary'ye derlenmiş anahtarlarla `Apply` çalıştırır: aday kontrolü ve rollback, kural 1–7 ile staged güncelleme (self-test servis kimliğiyle çalışır), başlayan sürümün `reconcile`'ı. Başka sürüme geçtiyse veya rollback yaptıysa bir kez çıkar (Windows: servise özgü çıkış kodu 3, kurtarma eylemleri yeniden başlatır; launchd: `KeepAlive`); sonraki başlatma yeni binary'nin ilk denemesidir (`attempts` orada sayılır) ve taşınan apply sonucunu bildirir (`apply-status.json` `carried: true`). Staged modu, ilk başarılı export'tan sonra onay, `ConfirmWindow`, `MaxStartAttempts` ve rollback isteği yukarıdaki gibidir.
- **`-reconcile`:** macOS: root'a ait düzen, state dizini ve config sahipliği, LaunchDaemon (değişen tanım `restart-required` yazdırır; kurulum programları `bootout`/`bootstrap` yapar; apply bağlamında sonraki bootstrap'ta uygulanır), `/usr/local/bin/openlog-infra-agent` linki, `/etc/newsyslog.d/openlog-infra-agent.conf`. Windows: servis oluşturma veya güncelleme (çalışan servisin tanımı değiştiğinde `restart-required`), kurtarma eylemleri, korumalı ACL'ler; `package` (MSI) bağlamı, daha yeni self-update edilmiş sürüm geçerli değilse `current`'ı kendi sürümüne yöneltir. İki işletim sisteminde de hesap veya grup oluşturulmaz.
- **Kurulum yöntemleri:** `tarball` (macOS `install.sh` ve `.pkg`), `zip` (`install.ps1`), `msi` (registry `HKLM\SOFTWARE\openlog\infra-agent` `InstallMethod=msi`). **Gömülü manifest'ler (D-113):** MSI (amd64, arm64) ve macOS pkg yayınlanan `manifest.json`'u içeremez (kendi digest'lerini listeler), bu yüzden deb/rpm gibi `versions/<v>/` içine aynı sürümün, sürüm anahtar(lar)ıyla imzalanmış ve yalnızca zip (Windows) veya tar.gz (macOS) arşivlerini listeleyen ikinci bir manifest'ini kurarlar; `schema`, `product`, `version`, `channel`, `released_at`, `notes_url`, `compatibility` ve `migrations` yayınlanan manifest ile aynıdır (release job'u kontrol eder), `images` çıkarılır. `-reconcile -reconcile-context package` (MSI custom action, pkg postinstall) onu binary'nin anahtarlarıyla doğrular ve eksik veya geçersizse not düşer (D-113'ten önce derlenen MSI'lar: böyle bir sürümden backend'in istediği rollback, self-update manifest'iyle bir sürüm kurana kadar kural 5'te başarısız olur). Onaylanmamış adayın otomatik rollback'i buna asla ihtiyaç duymaz. macOS'ta `package` bağlamı da, daha yeni self-update edilmiş sürüm geçerli değilse `current`'ı paketin sürümüne yöneltir.
- **Kurulum programı doğrulaması:** `install.sh` ve `install.ps1`, boyut ve sha256'yı HTTPS üzerinden alınan manifest'e göre kontrol eder; güvenilir bir ajan zaten kuruluysa onu değiştirmeden önce ayrıca `<current binary> -verify-release manifest.json -artifact <archive>` çalıştırırlar (derlenmiş anahtarlarla Ed25519 imzası).


## 4. Fleet güncelleme politikası (backend) {#_4-fleet-update-policy-backend}

PostgreSQL'de saklanır (bkz. [postgres.md](/tr/contracts/postgres), tablolar `agent_update_policies`, `agent_update_host_overrides`, `agent_rollouts`, `agent_hosts`).

| Alan | Tip | Varsayılan |
|---|---|---|
| `mode` | `off` · `notify` · `auto` | `auto` |
| `channel` | `stable` · `beta` | `stable` |
| `target` | `latest` · `patch` · `pinned` | `latest` |
| `pinned_version` | SemVer veya null | null |
| `waves` | int yüzdeler, kesin artan, son = 100 | `[10, 50, 100]` |
| `wave_soak_minutes` | int | 60 |
| `halt_failure_rate` | 0..1 | 0.05 |
| `maintenance_windows` | `{days: ["mon",…], start: "02:00", end: "05:00"}` UTC listesi; boş = her zaman | `[]` |

- **Dalga üyeliği:** `bucket = fnv32a(host_id + ":" + rollout_id) % 100`. `bucket < waves[current_wave]` olduğunda host uygundur.
- **Dalga ilerlemesi:** dalga başladıktan sonra `wave_soak_minutes` geçtiğinde ve bu rollout'u deneyen host'lar arasında `failed + rolled_back < halt_failure_rate × attempted` olduğunda (oran değerlendirilmeden önce en az 3 deneme; daha azıyla yalnızca soak'a göre ilerler).
- **Durdurma:** hata oranı aşılır → rollout `halted`, yeni host'lar güncellemeyi almaz, zaten güncellenmiş host'lar üzerinde kalır. Admin devam ettirebilir veya rollback emredebilir (önceki sürüme rollback rollout'u, aynı dalgalar).
- **Host ezmeleri:** `hold` (asla güncelleme) veya bir sürüme `pin`.
- Ingest sync handler'ı politikayı ve rollout durumunu lisans anahtarlarıyla aynı pod-local cache kalıbıyla okur (TTL ≤ 30 sn). Rollout durum geçişlerini (ilerleme, durdurma) tek bir leader hesaplar: bir `openlog-api` pod'unun tuttuğu Postgres advisory lock.

## 5. Backend sürüm endpoint'leri {#_5-backend-version-endpoints}

- ingest ve api'nin her HTTP yanıtı: `X-Openlog-Version: <version>`.
- `GET /api/v1/version` (kimliği doğrulanmış, her rol) → `{"version", "commit", "date", "latest_available": {"version", "notes_url", "checked_at"} | null, "update_check": "enabled|disabled|failed"}`.
- Backend index'i en fazla 24 saatte bir kontrol eder (`OPENLOG_UPDATE_CHECK=enabled|disabled`, `OPENLOG_RELEASE_INDEX_URL`) ve imzaları doğrular. Yalnızca leader (aynı advisory lock).
  Index'i ve `OPENLOG_UPDATE_CHANNEL`'ın en yeni kaydının manifest'ini doğrular (`notes_url` için) ve sonucu PostgreSQL'de saklar (`system_state`, anahtar `update_check`); başarısız kontrol 6 sa sonra yeniden denenir ve
  son iyi sonucu korur. Her pod `latest_available`'ı yalnızca o sürüm kendi sürümünden yeniyse bildirir.
- Eklemeli: yanıtta ayrıca `"updater": {…} | null` var, `openlog-updater`'ın durumu
  (docs/operations/upgrading.md, openapi `UpdaterStatus`). `/readyz` gövdeleri `"version"` içerir; ingest'in gRPC yanıtları `x-openlog-version` header metadata'sı taşır.
- Eklemeli: `"update_requests": {"can_request", "updater_listening", "updater_polled_at", "latest"} | null` (§5.1).
- Eklemeli (D-111): `updater.notices[]` `{code, message, params}` (`compose_outdated`, `compose_outdated_bundle`
  `{files_version, running_version}`, `compose_changes_pending` `{version, services}`) ve `updater.compose_changes`
  (docs/operations/upgrading.md "Compose files"). Arayüz bildirimleri koda göre çevirerek gösterir.
- Eklemeli (D-120): `updater.updater_version` (updater'ın kendi sürümü), `updater.self_update`
  `{target_version, from_version, state: running|succeeded|failed, error, started_at, finished_at}`, `self-update` adımı ve bildirimler `updater_outdated` (Compose, install-server.sh ile kurulmamış),
  `updater_outdated_bundle` (install-server.sh), `updater_outdated_kubernetes` `{updater_version, running_version}` ve
  `updater_self_update_failed` `{version, reason}`. `updater_version` olmayan dokümanlar 0.1.26'dan eski updater'lardan gelir; api onlara `updater_outdated` / `updater_outdated_kubernetes` (`updater_version: "< <running>"`) ekler.
- Eklemeli (D-121): `updater.maintenance_window` `{spec, windows[], open_now, next_open_at?}` (normalize edilmiş
  `OPENLOG_UPDATER_MAINTENANCE_WINDOW`, UTC; `spec: ""` = her zaman). api cevap verirken `open_now` / `next_open_at` değerlerini yeniden değerlendirir; eski updater'ların dokümanlarında yoktur.
  Compose updater'ı başarılı bir güncellemeden sonra kendi container'ını değiştirir (`OPENLOG_UPDATER_SELF_UPDATE`,
  docs/operations/upgrading.md "Updater self-update"); Kubernetes CronJob'u chart image'ını çalıştırır ve bunu asla yapmaz.

### 5.1 Güncelleme istekleri ("Check now" / "Update now", D-041) {#_5-1-update-requests-check-now-update-now-d-041}

Arayüz `openlog-updater` ile doğrudan konuşmaz. İstek kanalı PostgreSQL tablosu `update_requests`'tir
(migration `0009`, docs/contracts/postgres.md); iki updater motoru da durum dokümanları için kullandıkları DSN ile ona zaten ulaşır:

- `POST /api/v1/version/check` (admin/owner; `OPENLOG_SIGNUP_ENABLED=true` ile yalnızca organizasyon rolü ne olursa olsun
  `OPENLOG_SUPERADMIN_EMAILS` superadmin'leri, D-120) `action=check` ekler ve api sürüm kontrolünü senkron çalıştırır (herhangi bir pod; `update_check` içinde saklanır, yani leader'ın 24 saatlik takvimi yeniden başlar).
  `POST /api/v1/version/update {"target_version", "ignore_maintenance_window"}` `action=apply` ekler. İkisi de tüm kurulum için action başına 30 sn'de bir istekle sınırlıdır (`429` + `Retry-After`; transaction düzeyinde
  advisory lock pod'ları sıralar); biri `pending`/`running` iken ikinci `apply` → `409`.
- **Compose updater'ı:** her `OPENLOG_UPDATER_REQUEST_POLL`'da (10 sn) yoklar: 15 dk'dan eski `pending` satırların süresini doldurur
  (`expired`), en eski `pending` satırı sahiplenir (`UPDATE … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED)` → `running`), işler ve `done`/`failed` + `message` saklar. Her 30 sn'de `system_state` anahtarı `updater_poll`
  `{engine, mode, poll_seconds, polled_at}` yazar; api, `polled_at` en fazla max(90 s, 3 polls) eskiyse `updater_listening` bildirir. Başlangıçta `running` kalmış satırları `failed` işaretler (kesintiye uğramış).
- **Kubernetes CronJob'u** (`-k8s -once`): önce tüm `pending` satırları işler (işlenen istek kontrolü içerir), yani istekler bir sonraki planlı çalıştırmada uygulanır; `updater_poll` asla yazmaz.
- `check` = normal bir çalıştırma (mod kuralları değişmez). `apply` = hemen kur, `notify` modunda da: yalnızca updater'ın seçtiği sürüm hâlâ `target_version` ise (aksi halde `failed`, "check again");
  `OPENLOG_UPDATER_MAINTENANCE_WINDOW` dışında yalnızca `ignore_maintenance_window` ile; `OPENLOG_UPDATER_MODE=off` → `failed`.
  `failed_versions` içindeki bir sürüm açık bir istekle yeniden denenebilir. Güncellemenin kendisi normal
  backup → pull → migrate → recreate → health → rollback akışıdır; audit `updater.update_started` `request_id` ve `requested_by` taşır. api `update.check_requested` / `update.apply_requested` audit eder.
- Arayüz, istek açıkken veya `updater.state = updating` iken `GET /api/v1/version`'ı her 2 sn'de yoklar, api container'ı yeniden oluşturulurken son cevabı korur ("server is restarting…") ve yeni
  `X-Openlog-Version` ile yeniden yüklenir.

## 6. Rolling update için migration'lar {#_6-migrations-for-rolling-updates}

- Her PostgreSQL ve ClickHouse migration dosyası ilk satırında bir faz bildirir: `-- openlog:phase expand` veya `-- openlog:phase contract`.
- `openlog-migrate` (varsayılan) tüm `expand` migration'larını ve yalnızca `-- openlog:requires-all-at-least <version>` şartı `component_heartbeats` tablosuna kayıtlı (pod'lar sürüm + last_seen bildirir) çalışan her bileşen sürümüyle karşılanan `contract` migration'larını uygular. Bilinmiyorsa → atla ve logla.
- CI job'u `mixed-version`: `N` sürümünü deploy eder, `N+1` için migrate çalıştırır, `N` ve `N+1` servislerini birlikte çalıştırır, e2e suite'i yürütür.
