# openlog'u güncelleme (backend) {#upgrading-openlog-backend}

Backend'in (ingest, processor, api, allinone, migration'lar) Docker Compose ve Kubernetes üzerinde, elle veya
`openlog-updater` ile bir sürümden diğerine nasıl geçtiği. Ajanlar kendilerini ayrıca günceller
(docs/contracts/releases-updates.md §3); backend güncellemesi ajanları asla bozmaz (backend `N`, `N`, `N-1` ve `N-2`
ajanlarını kabul eder).

Arka plan: [plan §5](https://github.com/onuragtas/openlog/blob/master/docs/plan/09-releases-updates.md), [kontrat §5–§6](/contracts/releases-updates), D-025…D-029.

## 1. Uyumluluk kuralları {#_1-compatibility-rules}

| Kural | Senin için anlamı |
|---|---|
| Tek ürün sürümü | Image, chart, arayüz ve ajanlar aynı `X.Y.Z` sürümünü paylaşır. Her binary bunu bildirir: `-version`, `/readyz` gövdesi (`"version"`), `GET /api/v1/version` ve her ingest, api ve admin cevabındaki `X-Openlog-Version` header'ı (gRPC: `x-openlog-version` header metadata'sı). |
| `N` ve `N+1` birlikte çalışır | Rolling update sırasında iki sürümün pod'ları aynı Kafka, ClickHouse ve PostgreSQL'i paylaşır. Bu, CI'da test edilir (`make mixed-version`). |
| Migration'lar `expand` veya `contract` olur | `migrations/postgres` ve `schema/clickhouse` altındaki her dosya `-- openlog:phase expand` veya `-- openlog:phase contract` ile başlar. Expand değişiklikleri (yeni tablolar, nullable kolonlar) önceki sürüm için güvenlidir ve yeni kod başlamadan **önce** çalışır. Contract değişiklikleri (silme, yeniden adlandırma) `-- openlog:requires-all-at-least <version>` bildirir ve yalnızca daha eski bir instance ayakta değilken çalışır. |
| Güncelleme yolu | Bir sürümün manifest'i `compatibility.min_upgrade_from` şartı koyabilir; daha eski kurulumların araya bir sürüm alarak güncellemesi gerekir. `openlog-updater` bu ara sürümü kendisi seçer. |
| Sürüm düşürme | Yalnızca manifest'in `rollback_floor` değerine kadar izinlidir, çünkü expand migration'lar yerinde kalır; yeni sürümün contract migration'ları çalışmış olabilir, bu yüzden gerçek bir sürüm düşürme için yedekten dönmeyi tercih et. |

### Contract migration'lar nasıl denetlenir {#how-contract-migrations-are-gated}

`OPENLOG_POSTGRES_DSN` ayarlı her ingest, processor, api ve allinone süreci 30 saniyede bir `component_heartbeats`
tablosuna bir satır upsert eder (component, instance id, version, last_seen) ve düzgün kapanırken bu satırı siler.
`openlog-migrate`:

- bekleyen her `expand` migration'ı uygular;
- bir `contract` migration'ı yalnızca kendi sürümü ve son 5 dakikada görülen **her** instance gereken sürüme `>=` ise
  uygular; değilse nedenini loglar ve atlar, sonraki çalıştırma yeniden dener (updater başarılı bir güncellemeden sonra
  migrate'i tekrar çalıştırır);
- okunamayan heartbeat'leri (PostgreSQL yok, tablo yok) "bilinmiyor" sayar ve atlar.

```sh
openlog-migrate -plan              # neyin neden çalışacağını gösterir, hiçbir şeyi değiştirmez
openlog-migrate                    # uygular
openlog-migrate -force-contract    # eski instance'ların engellediği contract migration'ları da uygular (onları bozar)
```

```
live instances (seen within 5m0s): 2
  openlog-allinone  openlog-n-1-3f2a9c1e  0.9.0  last seen 2026-09-13T10:01:12Z
  openlog-api       api-7d9c-0b61fe22     0.9.1  last seen 2026-09-13T10:01:20Z

DATABASE    VERSION  NAME                   PHASE               ACTION   REASON
postgres    0003     component_heartbeats   expand              applied
postgres    0004     drop_legacy_column     contract (>= 0.9.1) skip     openlog-allinone openlog-n-1-3f2a9c1e runs 0.9.0 < 0.9.1
```

PostgreSQL'i olmayan süreçler (DSN'siz `OPENLOG_AUTH_MODE=static`) heartbeat gönderemez, bu yüzden orada contract
migration'lar zorlanana kadar atlanır. Contract migration yazarken: sonraki hiçbir expand migration ona ihtiyaç duymamalı
ve gereken sürümü, silinen nesneyi artık kullanmayan ilk sürüm olmalı.

## 2. Sürüm kontrolü ve arayüz {#_2-release-check-and-ui}

Api lideri (tek pod, PostgreSQL advisory lock) imzalı sürüm index'ini günde en fazla bir kez çeker (hata olursa 6 saat
sonra yeniden dener), index'i ve `OPENLOG_UPDATE_CHANNEL` kanalının en yeni manifest'ini doğrular ve sonucu PostgreSQL'e
yazar. Her pod bunu `GET /api/v1/version` ile sunar:

```json
{"version": "0.9.0", "commit": "abc123", "date": "2026-09-01T12:00:00Z",
 "latest_available": {"version": "0.9.1", "notes_url": "https://github.com/onuragtas/openlog/releases/tag/v0.9.1", "checked_at": "2026-09-13T03:00:00Z"},
 "update_check": "enabled", "updater": {"engine": "compose", "mode": "notify", "state": "available", "target_version": "0.9.1"}}
```

- Güncelleme isteyebilenler (`update_requests.can_request`: `OPENLOG_SIGNUP_ENABLED=true` kurulumlarda superadmin'ler,
  diğerlerinde admin'ler ve owner'lar; istek kanalı yoksa admin'ler ve owner'lar) "openlog 0.9.1 is available — Release
  notes" banner'ını görür (tarayıcıda sürüm başına kapatılır).
- **Settings → Organization → Version and updates** altında admin'ler ve owner'lar için iki düğme var. Çok tenant'lı
  kurulumlarda (`OPENLOG_SIGNUP_ENABLED=true`) kendi kaydolan organizasyonların owner ve admin'leri sunucuyu işletenler
  değildir: orada düğmeleri yalnızca superadmin'ler görür (`OPENLOG_SUPERADMIN_EMAILS`, doğrulanmış e-postayla giriş
  yapmış), giriş yaptıkları organizasyondaki rolleri ne olursa olsun; diğer herkes `403` alır (superadmin isteğinin audit
  kaydında `superadmin: true` bulunur). Bölümün kendisi, updater durumu ve bildirimlerle birlikte, her üyeye görünür.
  - **Check now** sürüm kontrolünü hemen çalıştırır ve `openlog-updater`'dan da kontrol etmesini ister (30 saniyede en
    fazla bir kez); günlük kontrolü veya `OPENLOG_UPDATER_INTERVAL` süresini beklemen gerekmez.
  - **Update now** (daha yeni bir sürüm biliniyor ve bir updater durum bildiriyorsa görünür) updater'dan o sürümü hemen
    kurmasını ister; bir admin onayladığı için `notify` modunda da. Onay penceresinde "Install now, also outside the
    maintenance window" kutucuğu vardır; işaretlenmezse istek `OPENLOG_UPDATER_MAINTENANCE_WINDOW` dışında başarısız olur.
    Bölüm, updater'ın bakım penceresini UTC ve tarayıcı saatiyle bir sonraki açılışıyla birlikte gösterir (pencere yoksa
    "any time"; Kubernetes'te updater'ın ne zaman çalışacağına CronJob zamanlaması karar verir);
    `waiting_for_maintenance_window` durumunda bir sonraki açılış durumun yanında görünür. Sayfa isteğin durumunu ve
    updater'ın adımlarını (backup, pull, migrate, recreate, health, rolled back/succeeded) gösterir, `openlog` yeniden
    oluşturulurken verisini korur ("The server is restarting… reconnecting") ve yeni sürüm cevap verince yeniden yüklemeyi
    önerir.
  - Compose updater'ı istekleri `OPENLOG_UPDATER_REQUEST_POLL` (10 sn) içinde alır. Updater isteklere bakmıyorsa (durmuş
    veya bu özellikten eski bir updater container'ı) sayfa bunu söyler: container'ı
    `docker compose --profile updater up -d openlog-updater` ile yeniden oluştur. Hiçbir updater'ın almadığı istekler 15
    dakika sonra zaman aşımına uğrar. Kubernetes'te CronJob istekleri bir sonraki çalışmasında işler (§4).
  - Mekanizma ve audit eylemleri: [releases-updates.md §5.1](/contracts/releases-updates), D-041.
- Fleet (ajanlar): aktif bir rollout'taki **Deploy now**, kalan dalga bekleme sürelerini atlar (bir anda %100). Dalga
  bekleyen ajanlar her `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` (60 sn) aralığında eşitlenir, yani yaklaşık bir dakika içinde
  başlarlar; politikanın bakım pencereleri ve durdurma eşiği yine geçerlidir.
- API cevapları, açık sayfayı sunan backend'den farklı bir `X-Openlog-Version` taşıdığında herkes "openlog was updated to
  X — Reload" mesajını görür (güncellemeden sonra eski lazy-load arayüz parçaları artık yoktur).
- `OPENLOG_UPDATE_CHECK=disabled` kontrolü kapatır; `OPENLOG_RELEASE_INDEX_URL` bir mirror'ı gösterir (air-gapped:
  `index.json`, manifest'ler ve `.sig` dosyalarını herhangi bir HTTP sunucusuna kopyala; imzalar yine doğrulanır). Güvenilir
  anahtarı olmayan build'ler `update_check: failed` ("no trusted release keys") bildirir.

## 3. Docker Compose {#_3-docker-compose}

### Elle {#manual}

```sh
cd deploy/compose
docker compose exec -T postgres pg_dump -U openlog -d openlog -Fc > backup-$(date +%F).dump   # yedek
sed -i.bak 's|^OPENLOG_IMAGE=.*|OPENLOG_IMAGE=ghcr.io/onuragtas/openlog:0.9.1|' .env
docker compose pull openlog
docker compose run --rm --entrypoint openlog-migrate openlog          # önce expand migration'lar
docker compose up -d --wait openlog                                    # allinone açılışta da migrate eder
curl -s localhost:9464/readyz                                          # {"version":"0.9.1",...}
```

Geri almak için: önceki `OPENLOG_IMAGE` değerini yaz ve `docker compose up -d openlog` çalıştır (expand migration'lar
önceki sürümle uyumludur).

### Otomatik: `openlog-updater` {#automatic-openlog-updater}

```sh
docker compose --profile updater up -d openlog-updater
docker compose logs -f openlog-updater
```

| Değişken | Varsayılan | |
|---|---|---|
| `OPENLOG_UPDATER_MODE` | `notify` | `off`, `notify` ("available" kaydeder, bir kez audit'e yazar, arayüzde gösterir), `auto` |
| `OPENLOG_UPDATE_CHANNEL` | `stable` | `beta` stable sürümleri de görür |
| `OPENLOG_RELEASE_INDEX_URL` | GitHub'daki en son `index.json` | mirror URL'i |
| `OPENLOG_RELEASE_TRUSTED_KEYS_FILE` | – | ek public key'ler (release image'larında resmi anahtarlar derlenmiş olarak bulunur) |
| `OPENLOG_UPDATER_INTERVAL` | `1h` | kontrol aralığı |
| `OPENLOG_UPDATER_REQUEST_POLL` | `10s` | arayüzden gelen "Check now" / "Update now" isteklerinin PostgreSQL'den (`update_requests`) ne sıklıkla okunacağı |
| `OPENLOG_UPDATER_MAINTENANCE_WINDOW` | – (her zaman) | UTC, örneğin `sat,sun 02:00-05:00; mon-fri 03:00-03:30` (`*` = her gün; `end <= start` gece yarısını geçer) |
| `OPENLOG_UPDATER_HEALTH_TIMEOUT` | `5m` | yeni sürümün hazır hale gelmek için ne kadar süresi olduğu |
| `OPENLOG_UPDATER_SERVICES` | `openlog` | yeniden oluşturulacak compose servisleri |
| `OPENLOG_UPDATER_HEALTH_URLS` | `http://openlog:9464/readyz` | hedef sürümle 200 dönmeli |
| `OPENLOG_UPDATER_POSTGRES_SERVICE` / `_PGDUMP_USER` / `_PGDUMP_DATABASE` | `postgres` / `openlog` / `openlog` | yedeğin kaynağı |
| `OPENLOG_UPDATER_BACKUP_DIR` / `_BACKUP_KEEP` | `./backups` (`/backups` altına mount edilir) / `5` | `pg_dump -Fc` dosyaları `openlog-<UTC time>-<from version>.dump` |
| `OPENLOG_UPDATER_ENV_FILE` | `/compose/.env` | yeniden oluşturulan container'ların ayarları ([aşağıda](#settings-from-env)); doğrulanmış bir güncellemeden sonra `OPENLOG_IMAGE=` yeniden yazılır |
| `OPENLOG_UPDATER_COMPOSE_DIR` | `OPENLOG_UPDATER_ENV_FILE` dosyasının dizini (`/compose`) | updater içine mount edilmiş haliyle compose proje dizini |
| `OPENLOG_UPDATER_COMPOSE_SYNC` | `auto` | `auto`: install-server.sh kurulumları kurulan her sürümün doğrulanmış compose dosyalarını alır ([aşağıda](#compose-files)); `off`: compose dosyaları asla değişmez |
| `OPENLOG_UPDATER_SELF_UPDATE` | `auto` | başarılı bir güncellemeden sonra updater kendi container'ını kurulan image ile değiştirir ([aşağıda](#updater-self-update)): `auto` install-server.sh kurulumları (`.bundle-version`), `on` her Compose kurulumu, `off` asla (`updater_outdated*` bildirimi kalır) |
| `OPENLOG_UPDATER_IMAGE_REPOSITORY` | – | manifest digest'ini bir mirror repository'den çeker |
| `OPENLOG_UPDATER_COMPOSE_PROJECT` | updater container'ından algılanır | |

Her çalışmada: stack'in bildirdiği sürümü okur (`/readyz`), index'i doğrular, kanalda daha yeni olan, daha önce başarısız
olmamış, image'ı bulunan ve `min_upgrade_from` şartına uyan en yeni sürümü seçer. `auto` modunda, bakım penceresi içinde:

1. **backup** — postgres container'ında `docker exec` ile `/backups` içine `pg_dump -Fc`; en yeni N tanesi tutulur. Yedek başarısız olursa güncelleme iptal edilir.
2. **pull** — image, imzalı manifest'teki **digest ile** çekilir (altından bir tag değiştirilemez).
3. **compose-bundle** (yalnızca install-server.sh kurulumları) — imzalı manifest'te listelenen `openlog-compose-<v>.tar.gz` indirilir, boyutu ve sha256'sı kontrol edilir, `.bundle-staging/` içine açılır (yalnızca normal dosyalar, `.env` yok). Burada bir hata, hiçbir şey değişmeden güncellemeyi durdurur ([Compose dosyaları](#compose-files)).
4. **migrate** — yeni image'dan tek seferlik bir container, çalışan `openlog` container'ının ağı ve (yenilenmiş, [aşağıda](#settings-from-env)) ortamıyla `openlog-migrate` çalıştırır. Yalnızca expand (ve uygun durumdaki contract) migration'lar çalışır, yani bu adım başarısız olsa da çalışan sürüm çalışmaya devam eder.
5. **recreate** — her servis container'ı için: durdurulur (kendi stop timeout'u ile), `<name>-pre-update` olarak yeniden adlandırılır, aynı config, label, mount, port binding, restart policy, ağ ve alias'larla yeni bir container oluşturulur, image değiştirilir ve ortam `.env` ile compose dosyalarından yenilenir, sonra başlatılır. Adımın ayrıntısı yenilenen değişkenlerin adlarını listeler (değerlerini asla).
6. **health** — her health URL'i `OPENLOG_UPDATER_HEALTH_TIMEOUT` dolmadan hedef sürümle 200 dönmelidir; restart policy'si olmadan kapanan bir container anında başarısız sayılır.
7. **success** (`cleanup` adımı) — `-pre-update` container'ları silinir, hazırlanan compose bundle kurulur, `.env` içindeki `OPENLOG_IMAGE` yeniden yazılır, sonra (`contract-migrate` adımı) eski container'ları bekleyen contract migration'lar için `openlog-migrate` yeniden çalıştırılır.
8. **4. adımdan sonra hata** — yeni container'lar silinir, öncekiler adlarına geri döndürülüp başlatılır, önceki sürümü bildirene kadar beklenir: durum `rolled_back` (veya `rollback_failed`). Hazırlanan compose bundle silinir; compose dosyalarına hiç dokunulmamıştır. Sürüm `failed_versions` içine yazılır ve tekrar denenmez; daha yeni bir sürüm denenir.
9. **self-update** (yalnızca başarıdan sonra, sonuç ve arayüz isteği kaydedildikten sonra; hata veya rollback'ten sonra asla) — kurulan sürümden eski bir updater kendi container'ını o sürümün image'ı ile değiştirir ([Updater'ın kendini güncellemesi](#updater-self-update)). Burada bir hata, güncellemenin sonucunu değiştirmez.

Durum `/backups/updater-status.json` dosyasında ve PostgreSQL'de tutulur (`GET /api/v1/version` içinde `updater` olarak
görünür); olaylar audit log'a gider (`updater.update_started`, `…_succeeded`, `…_rolled_back`, `…_failed`, `…_available`,
`updater.self_update_started` / `…_succeeded` / `…_failed`, actor `openlog-updater`). Doküman ayrıca `updater_version`
(updater'ın kendi sürümü) ve `self_update` (son self-update denemesi) alanlarını taşır. Updater güncellemenin ortasında
ölürse, bir sonraki açılışında `-pre-update` container'ını bulur ve geri yükler (yarım kalmış bir compose dosyası
değişimini de, aşağıya bak).

### `.env` ayarları {#settings-from-env}

Eski bir compose dosyasından oluşturulan container'da o dosyanın listelemediği ayarlar yoktur (örneğin `.env` içinde
`OPENLOG_SAAS_MODE` var ama compose dosyası bu ayardan eski: image güncellendi, ayar container'a hiç ulaşmadı). Bu yüzden
updater, yeniden oluşturduğu her container'ın ortamını `.env` ve container'ın oluşturulduğu compose dosyalarından yeniler
(`com.docker.compose.project.config_files` ve `working_dir` label'ları, `/compose` altına eşlenir; install-server.sh
kurulumlarında yeni bundle'ın `docker-compose.yml` dosyası), öncelik sırası `docker compose up` ile aynıdır:

| # | Değişken | Yeniden oluşturulan container'daki değer |
|---|---|---|
| 1 | servisin `environment` bölümünde, başvurduğu her değişken `.env` içinde ayarlı (veya literal) | compose gibi hesaplanır: `.env` değeriyle `${VAR:-default}`, literal'ler yazıldığı gibi (compose literal'i `.env`'i ezer) |
| 2 | servisin `environment` bölümünde, başvurulan değişken `.env` içinde **yok** | container'ın mevcut değeri (container oluşturulurken shell'den veya başka bir env dosyasından gelmiştir) |
| 3 | servisin `environment` bölümünde yok, `.env` içinde ayarlı | compose dosyaları kurulu sürümden eskiyse (`x-openlog-compose-version` / `.bundle-version` değerleri; yoksa eski sayılır) **ve** değişkene hiç başvurmuyorsa ayarlanır: bu dosyalardan yeni olan ayarlar. Image, `OPENLOG_UPDATER_*`, dinleme adresi (`*_ADDR`) veya paketle gelen bağımlılıkların TLS/SASL değişkenleri asla ayarlanmaz. Güncel compose dosyalarında bu tür değişkenler bilerek geçirilmez. |
| 4 | container'daki diğer her şey (`docker-compose.override.yml` değeri, …) | korunur |

Değerler compose'un `.env` kurallarıyla okunur (tırnaklar, `export`, `#` yorumları, `${VAR}` referansları). Compose
dosyaları okunamazsa (proje dizini dışında bir dosya, YAML hatası, `extends` kullanan bir servis) yalnızca `.env`'deki,
container'da olmayan veya boş olan `OPENLOG_*` değişkenleri eklenir. Okunamayan veya ayrıştırılamayan bir `.env` güncellemeyi
asla engellemez: ortam olduğu gibi bırakılır ve `recreate` adımı bunu söyler. `docker compose up` çalışırken export edilmiş
shell değişkenlerini updater göremez: ayarları `.env` içinde tut.

Compose dosyası her backend ayarını açıkça geçirir (`x-openlog-settings` anchor'ı, `internal/config/envcoverage_test.go`
bunu zorunlu kılar); bilerek `env_file: .env` içermez, çünkü bu diğer servislerin PostgreSQL, bootstrap ve S3 kimlik
bilgilerini de `openlog`'a verirdi (ve veri export'larını tiered storage bucket'ına geçirirdi).

### Compose dosyaları {#compose-files}

**install-server.sh kurulumları** (`/opt/openlog-server/.bundle-version` varsa) çalışan sürümde tutulur:

- Her sürümde `openlog-compose-<v>.tar.gz` asset'i bulunur (manifest bileşeni `compose`,
  [releases-updates.md §2](/contracts/releases-updates)): `docker-compose.yml` (`x-openlog-compose-version: "<v>"` ile),
  `.env.example`, `clickhouse/`, `README.md`.
- `migrate`'ten önce doğrulanıp hazırlanır ve yalnızca yeni sürüm sağlıklı olduktan sonra kurulur: değiştireceği dosyalar
  (ve `.bundle-version`) `.bundle-previous/` içine kopyalanır, `.bundle-swap.json` günlüğü yazılır, her dosya rename ile
  yerine konur, `.env`'de olmayan ayarlar yeni `.env.example` dosyasından eklenir (install-server.sh ile aynı kural:
  yalnızca aktif satırlar, kimlik bilgileri veya `OPENLOG_IMAGE` asla; `.env` 0600 modunu korur) ve `.bundle-version` en son
  yazılır. Herhangi bir hatada önceki dosyalar `.bundle-previous/` içinden geri yüklenir ve hata `cleanup` adımında görünür;
  updater açılışta günlüğü bulursa da onları geri yükler.
- Compose dosyalarını elle geri almak: `cp -R /opt/openlog-server/.bundle-previous/. /opt/openlog-server/`
  (dosyaları ve `.bundle-version`'ı geri yükler; `.env`'e eklenen ayarlar kalır, onlar varsayılandır).
- Container'lar Docker API üzerinden yeniden oluşturulur, yani yalnızca image ve ortam değişir. Updater'ın uygulamadığı
  değişiklikler (yeni veya değişen volume'lar, portlar, ağlar, yeni servisler ya da `clickhouse/*.xml` gibi değişen bir
  mount edilmiş dosya), servis compose tarafından yeniden oluşturulana (`com.docker.compose.config-hash` değişene) veya mount
  edilmiş dosyalarda yeniden başlatılana kadar Settings → Organization → Version and updates ekranında ve updater log'unda
  `compose_changes_pending` bildirimi olarak görünür ("… re-run install-server.sh or docker compose up -d").
  install-server.sh'ı yeniden çalıştırmak bunları uygular (yeni dosyalarla `docker compose up -d`). Updater compose CLI'ını
  kendisi çalıştırmaz: bunun için image'da compose eklentisi ve projenin host'taki yolunda mount edilmesi gerekirdi (compose
  göreli bind mount'ları host üzerinde çözer) ve updater'ın kendi container'ını da yeniden oluştururdu.
- Asset'i olmayan bir sürüm (0.1.22'den eski) veya `OPENLOG_UPDATER_COMPOSE_SYNC=off`: dosyalar kalır, adım nedenini söyler
  ve `compose_outdated_bundle` bildirimi install-server.sh'ı yeniden çalıştırmanı ister.

**Diğer kurulumlar** (git clone, kendi dizinin): updater dosyaları asla değiştirmez. Compose dosyasının
`x-openlog-compose-version` değeri (yoksa: updater'ın sürümünden eski bir dosya) çalışan sürümden eskiyse bir uyarı loglar
ve `compose_outdated` bildirimini gösterir: "the compose files (0.1.21) are older than the running version 0.1.22: …
update them (git checkout v0.1.22, then docker compose up -d) or migrate to install-server.sh". `.env`'e eklenen ayarlar
yeniden oluşturulan container'lara yine ulaşır (yukarıdaki kural 3), ama volume'lar ve yeni servisler dosyalar güncellenene
veya [install-server.sh'a geçene](#migrating-from-a-git-clone-installation) kadar ulaşmaz.

Bu davranışı getiren ilk güncellemeyi hâlâ şu an çalıştırdığın updater container'ı yapar: 0.1.26'dan eski bir updater ne
kendini günceller ne de eski olduğunu bildirir (api onun için `updater_outdated` bildirimini ekler). `install-server.sh`'ı
bir kez çalıştır (veya yeni dosyalarla `docker compose --profile updater up -d openlog-updater`), böylece sonraki
güncellemeleri güncel updater yapar ve o da kendini güncel tutar ([aşağıda](#updater-self-update)).

### Bildirimler {#notices}

Settings → Organization → Version and updates ekranında (çevrilmiş olarak), `GET /api/v1/version` içinde
(`updater.notices[]`) ve updater log'unda uyarı olarak görünür; her çalışmada yenilenir.

| Kod | Anlamı | Çözüm |
|---|---|---|
| `compose_outdated` | git clone / kendi dizinin compose dosyaları çalışan sürümden eski | `git checkout v<running>` + `docker compose up -d` veya install-server.sh'a geç |
| `compose_outdated_bundle` | install-server.sh compose dosyaları çalışan sürümden eski (sürümde bundle yok, `OPENLOG_UPDATER_COMPOSE_SYNC=off`, başarısız kurulum) | install-server.sh'ı yeniden çalıştır |
| `compose_changes_pending` | updater'ın uygulayamadığı compose değişiklikleri (volume'lar, portlar, yeni servisler, mount edilmiş dosyalar) | install-server.sh'ı yeniden çalıştır veya `docker compose up -d` |
| `updater_outdated_bundle` | openlog-updater container'ı (install-server.sh kurulumu) çalışan sürümden eski: self-update kapalı, başarısız veya henüz mümkün değil | install-server.sh'ı yeniden çalıştır |
| `updater_outdated` | diğer Compose kurulumları için aynısı; ayrıca api, bunu bildiremeyen 0.1.26'dan eski updater'ların durum dokümanlarına da ekler (`updater_version: "< <running>"`) | compose dizininde `docker compose --profile updater up -d openlog-updater` (git clone) veya install-server.sh'ı yeniden çalıştır |
| `updater_outdated_kubernetes` | updater CronJob'u, güncellediği Deployment'lardan eski bir image çalıştırıyor | `helm upgrade … --set image.tag=<running version>` (§4) |
| `updater_self_update_failed` | `version` sürümüne self-update başarısız oldu; önceki updater çalışmaya devam eder ve o sürüm için tekrar denenmez | `reason` alanını ve updater log'unu oku; install-server.sh'ı yeniden çalıştır (veya `docker compose --profile updater up -d openlog-updater`) |

### Updater'ın kendini güncellemesi {#updater-self-update}

`openlog-updater` container'ı, oluşturulduğunda `.env`'in seçtiği image'ı çalıştırır (`OPENLOG_UPDATER_IMAGE`, varsayılan
`OPENLOG_IMAGE`). Yardım almasa bu image'da sonsuza kadar kalırdı, yani updater'daki iyileştirmeler bir kuruluma hiç
ulaşmazdı. Bu yüzden `V` sürümüne başarılı bir güncellemeden sonra (health check geçti, compose dosyaları kuruldu, arayüz
isteği bitti) `V`'den eski bir updater kendi container'ını `V`'nin image'ı ile değiştirir: güncellemenin zaten imzalı
digest'iyle çektiği image. `OPENLOG_UPDATER_SELF_UPDATE=auto` (varsayılan) bunu install-server.sh kurulumlarında yapar; `on`
git clone veya kendi dizinin için de (container, `openlog` gibi Docker API üzerinden yeniden oluşturulur, yani compose
dosyana gerek yoktur, ama container'da olmayan bir `docker-compose.override.yml` ayarı olduğu gibi kalır); `off` asla (yalnızca
bildirim). Kubernetes CronJob'u chart'ın image'ını çalıştırır ve kendini asla güncellemez.

Container adı (örneğin `openlog-openlog-updater-1`) sahiplik jetonudur: Docker'da rename atomiktir ve adlar benzersizdir,
yani onu yalnızca bir container tutabilir ve stack üzerinde yalnızca onu tutan işlem yapar.

1. Eski updater yedek dizinine (iki container'a da mount edilir) `updater-handover.json` yazar, `.env` bir image'a sabitliyorsa
   `.env` içindeki `OPENLOG_UPDATER_IMAGE` değerini yeniden yazar (updater `OPENLOG_IMAGE` değerini izliyorsa yazmaz, onu
   güncelleme zaten yazmıştır) ve kendi container'ından `<name>-self-update-new` oluşturur: aynı label'lar
   (`com.docker.compose.*` dahil, yani compose onu sahiplenmeye devam eder), mount'lar, ağlar, kullanıcı ve entrypoint, her
   yeniden oluşturulan container gibi `.env` ve compose dosyalarından yenilenen ortam, yeni image ve **restart policy `no`**.
2. Yeni updater başlar, kendi container id'sinin olduğu işaret dosyasını bulur ve bir self-test çalıştırır: beklenen sürümü
   bildiriyor mu, Docker'a ulaşıyor mu, durum dosyasını ve compose dizinini okuyor mu. `updater-handover-ready.json` yazar ve
   bekler; başka hiçbir şey yapmaz.
3. Eski updater bu dosyayı en fazla 2 dakika bekler. Yeni container başlamazsa, kapanırsa, self-test'i geçemezse veya sessiz
   kalırsa eski updater onu siler, `.env`'i geri yükler, işaret dosyasını siler, `self-update` adımını başarısız olarak ve
   `updater_self_update_failed` bildirimini kaydeder ve çalışmaya devam eder. Self-update `V` için tekrar denenmez (sonraki
   sürüm yeniden dener).
4. Başarı durumunda eski updater kendini `<name>-self-update-old` olarak adlandırır ve bir daha işlem yapmaz.
5. Yeni updater asıl adı alır, eski restart policy'yi (`unless-stopped`) kendine uygular (commit budur), eski container'ı
   durdurup siler, işaret dosyasını siler, `self-update` adımını ok olarak işaretler (Settings'te "Updater self-update"
   görünür) ve normal döngüsüne başlar (ilk çalıştırma hemen).

Log'da bakman gereken satırlar: `openlog-updater replaces its own container…`, `the new updater passed its self-test`, `handed
over to the new updater container`, sonra yeni container'da `self-update: took over the container name` ve
`openlog-updater replaced its own container`.

**Crash güvenliği.** Her updater, işlem yapmadan önce açılışta işaret dosyasını okur:

| Kesilme anı | Host veya daemon yeniden başlatıldıktan sonra | Sonuç |
|---|---|---|
| eski updater kendini yeniden adlandırmadan önce | yalnızca eski container yeniden başlar (yeninin restart policy'si `no`); ad hâlâ onda | yeni container'ı siler, `.env`'i geri yükler: başarısız, eski updater çalışır |
| rename'den sonra, yeni updater adı almadan önce | yalnızca eskisi yeniden başlar (adı `…-self-update-old`); yenisi durmuş haldedir | durmuş, commit edilmemiş yeni container'ı siler ve adını geri alır: başarısız, eski updater çalışır |
| yeni updater adı aldı, henüz commit etmedi | aynısı: yeni container `no` policy'si ile durmuş haldedir | yukarıdaki gibi |
| commit'ten sonra | ikisi de yeniden başlar; eskisi commit edilmiş yeni bir updater görür ve yalnızca bekler | yenisi eski container'ı siler: başarılı |
| yeni updater takılır | — | handover 3 dakikalık olduğunda, ad onda değilse kapanır; sonra eskisi adı geri alır |
| handover sırasında `docker compose up -d openlog-updater` | ad, compose'un oluşturduğu container'dadır | iki handover container'ını da siler ve işaret dosyasını siler |

Eski updater adı yalnızca yeni container yoksa ya da durmuş **ve** restart policy'si hâlâ `no` ise geri alır: bu policy'deki
durmuş bir container bir daha asla işlem yapamaz, commit edilmiş olana da asla dokunulmaz. Yani herhangi bir anda en fazla bir
updater işlem yapar ve her yeniden başlatmadan sonra tam olarak biri kalır.

**Kapatmak için:** `.env` içine `OPENLOG_UPDATER_SELF_UPDATE=off` yaz, sonra `docker compose --profile updater up -d openlog-updater`
(veya install-server.sh'ı yeniden çalıştır).

**Elle kurtarma** (gerekmemeli): `docker ps -a --filter name=self-update` kalıntıları listeler. `<project>-openlog-updater-1`
adlı container'ı tut (birini `docker rename` ile geri adlandır), diğerlerini `docker rm -f` ile sil,
`backups/updater-handover.json` ve `backups/updater-handover-ready.json` dosyalarını sil ve install-server.sh'ı yeniden çalıştır
(veya `docker compose --profile updater up -d openlog-updater`).

### Git clone kurulumundan geçiş {#migrating-from-a-git-clone-installation}

Bir clone'dan başlatılmış stack (`/opt/openlog/deploy/compose`, proje `openlog`) veri kaybetmeden install-server.sh düzenine
geçer: adlandırılmış volume'lar (`openlog_postgres-data`, `openlog_clickhouse-data`, `openlog_kafka-data`, …) dizine değil
proje adına aittir.

```sh
sudo mkdir -p /opt/openlog-server/releases
sudo cp /opt/openlog/deploy/compose/.env /opt/openlog-server/.env && sudo chmod 600 /opt/openlog-server/.env
sudo cp /opt/openlog/deploy/compose/releases/plans.json /opt/openlog-server/releases/ 2>/dev/null || true   # plan kataloğu kullanıyorsan
# isteğe bağlı: updater'ın dump'larını koru
sudo cp -R /opt/openlog/deploy/compose/backups /opt/openlog-server/ 2>/dev/null || true
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install-server.sh |
  sudo sh -s -- --project openlog --dir /opt/openlog-server
docker compose -p openlog -f /opt/openlog-server/docker-compose.yml --env-file /opt/openlog-server/.env ps
```

Kurulum betiği kopyalanan `.env`'i korur (secret'lar asla yeniden üretilmez; eksik ayarlar eklenir), çalışan veya daha yeni
sürümün compose dosyalarını yazar, container'ları aynı projede bu dosyalardan yeniden oluşturur ve updater'ı yeni dizinden
başlatır. `docker-compose.override.yml` içindeki ayarlar önce `.env`'e taşınmalıdır (install-server.sh ve updater yalnızca
`docker-compose.yml` ve `.env` kullanır; veri export'larının `data-exports` volume'u vardır, yani onlar için override
gerekmez). Arayüzü ve `ps` çıktısını kontrol et (tüm servisler sağlıklı), sonra eski dizini sil
(`sudo rm -rf /opt/openlog`; asla `docker compose down -v` çalıştırma, volume'ları siler).

**Neden `docker compose up` yerine Docker Engine API.** Updater `/var/run/docker.sock` ile düz HTTP üzerinden konuşur ve
container'ları kendi inspect verilerinden yeniden oluşturur. Bunun için image içinde compose CLI veya compose dosyası, host ile
aynı proje yolu (compose göreli bind mount'ları host yoluna göre çözer) ve stack başlatıldığında shell'inde olan değişkenlerin
bir kopyası gerekmez; önceki container rollback hedefi olarak bozulmadan kalır, bu da rollback'i ikinci bir deployment yerine bir
rename yapar. Bedeli compose dosyasından sapmadır, updater bunu `.env` içindeki `OPENLOG_IMAGE` değerini yeniden yazarak kapatır:
sonraki bir `docker compose up -d` yeni sürümü korur (saklanan config hash artık eşleşmediği için container'ı bir kez yeniden
oluşturabilir). Eski image'dan gelen ayarlar (`ENV`, `LABEL`, entrypoint) atılır, böylece yeni image'ın varsayılanları geçerli
olur.

**Güvenlik.** Docker socket'i root'a eşdeğerdir; updater kendi container'ında root olarak çalışır. Yalnızca digest'i güvenilir
bir anahtarla imzalanmış bir manifest'te bulunan image'lar çalıştırılır. Registry kimlik bilgileri desteklenmez (public
image'lar veya kimlik doğrulamasız erişilebilen bir mirror).

**Otomatik güncellenenler:** `OPENLOG_UPDATER_SERVICES` içindeki servisler (`openlog`), `.env` içindeki `OPENLOG_IMAGE`,
install-server.sh kurulumlarının compose dosyaları ve, 0.1.26'dan beri `OPENLOG_UPDATER_SELF_UPDATE` ile, `openlog-updater`
container'ının kendisi ([Updater'ın kendini güncellemesi](#updater-self-update)).

**Otomatik güncellenmeyenler:** `bootstrap`, `loadgen`, `openlog-renderer`, üçüncü parti image'lar (Kafka, ClickHouse,
PostgreSQL), `OPENLOG_UPDATER_SELF_UPDATE=off` iken updater container'ı (veya install-server.sh kurulumları dışında `auto`:
`updater_outdated` bildirimi) ve 0.1.26'dan eski updater container'ları (install-server.sh'ı veya
`docker compose --profile updater up -d openlog-updater` komutunu bir kez çalıştır).

## 4. Kubernetes (Helm) {#_4-kubernetes-helm}

### Önerilen: GitOps {#recommended-gitops}

Chart ve image sürümünü Git'te sabitle, Argo CD veya Flux uygulasın; chart'ın `pre-upgrade` migrate hook'u Deployment'lar
dönmeden önce yeni image ile `openlog-migrate` çalıştırır (önce expand). Yeni sürümler için `GET /api/v1/version` değerini
(veya arayüz banner'ını) izle, örneğin chart sürümünde Renovate ile.

```yaml
# Argo CD Application (alıntı)
source:
  repoURL: https://github.com/onuragtas/openlog
  path: deploy/helm/openlog
  targetRevision: v0.9.1
  helm: {valueFiles: [values-production.yaml], parameters: [{name: image.tag, value: "0.9.1"}]}
```

### Elle {#manual-1}

```sh
helm upgrade openlog deploy/helm/openlog -n observability -f values.yaml --set image.tag=0.9.1 --wait
kubectl -n observability logs job/openlog-migrate
```

Hook sırasında atlanan contract migration'ları (eski pod'lar hâlâ ayakta) bir sonraki migrate çalıştırması uygular (sonraki
güncelleme veya `kubectl -n observability create job --from=cronjob/…` / elle bir `openlog-migrate` Job'u). Önce PostgreSQL'i
yedekle (CloudNativePG: `spec.backup`; harici: `pg_dump`).

### İsteğe bağlı: updater CronJob'u {#optional-updater-cronjob}

```yaml
updater:
  enabled: true
  mode: auto                     # notify | auto
  schedule: "17 3 * * *"
  maintenanceWindow: "sat,sun 02:00-05:00"
```

`openlog-updater -k8s -once` (küme içi service account, düz REST, client-go yok):

1. index ve manifest'i doğrular ve hedefi seçer (Compose ile aynı kurallar);
2. api Deployment'ının pod ayarlarından (service account, env, security context) yeni image, `openlog-migrate` ve `OPENLOG_MIGRATE_SKIP_KAFKA=true` ile bir `<release>-api-updater-migrate-*` Job'u oluşturur ve bekler;
3. ingest, processor ve api Deployment'larının image'ını patch'ler (strategic merge patch, normal rolling update) ve her rollout tamamlanana kadar bekler;
4. api hedef sürümü bildirene kadar bekler;
5. 3–4. adımlarda hata olursa önceki image'ları geri patch'ler ve o rollout'u bekler (`rolled_back`).

RBAC (release namespace'inde Role): üç Deployment üzerinde ada göre `get`,`patch`; Job'larda `create`,`get`; pod'larda
`get`,`list` ve pods/log üzerinde `get`. Durum PostgreSQL'e (`GET /api/v1/version`) ve audit log'a gider. Kubernetes'te yedek
adımı yoktur: CloudNativePG yedeklerini veya yönetilen veritabanının snapshot'larını ayarla.

Arayüzdeki "Check now" / "Update now": CronJob'un istekleri dinleyecek uzun ömürlü bir süreci yoktur, bu yüzden her çalışma önce
bir önceki çalışmadan beri kuyruğa giren istekleri işler (`apply` isteği `notify` modunda da kurar). Bir isteği hemen işlemek için
CronJob'tan bir çalıştırma başlat:

```sh
kubectl -n <namespace> create job --from=cronjob/<fullname>-updater openlog-updater-now-$(date +%s)
```

Dikkat: updater Deployment'ları Helm dışından değiştirir. Sonraki `helm upgrade`, image'ı yine `image.tag` değerinden ayarlar;
bunu çalışan sürüme ayarla (veya GitOps kullan, orada updater `notify` modunda kalmalı). CronJob'un kendisi chart'ın `image.tag`
değerini çalıştırmaya devam eder, yani Deployment'ları güncelledikten sonra bir sonraki
`helm upgrade --set image.tag=<running version>` komutuna kadar `updater_outdated_kubernetes` bildirimini verir.

## 5. Yedekler ve geri yükleme {#_5-backups-and-restore}

Compose yedekleri PostgreSQL veritabanının (tenancy, kullanıcılar, anahtarlar, fleet durumu) custom-format dump'larıdır.
Telemetri, güncellemelerin asla silmediği ClickHouse ve Kafka volume'larındadır; expand migration'lar yalnızca ekleme yapar.

```sh
cd deploy/compose
docker compose stop openlog
docker compose exec -T postgres pg_restore -U openlog -d openlog --clean --if-exists < backups/openlog-20260913T030000Z-0.9.0.dump
sed -i.bak 's|^OPENLOG_IMAGE=.*|OPENLOG_IMAGE=<previous image>|' .env
docker compose up -d openlog
```

Güncellemeden önce alınmış bir dump'ı geri yüklemek, o zamanki `schema_migrations` tablosunu da geri yükler, yani yeni sürümün
migration'ları sonraki güncellemede yeniden çalışır.

## 6. Sorun giderme {#_6-troubleshooting}

| Belirti | Kontrol |
|---|---|
| `update_check: failed` | `GET /api/v1/version`; api liderinin `update check failed` log'ları (GitHub'a ağ erişimi, mirror URL'i, kendi derlediğin image'larda "no trusted release keys" → `OPENLOG_RELEASE_TRUSTED_KEYS_FILE`). |
| Updater `state: error` | `error` alanı: mevcut sürüm okunamıyor (`OPENLOG_UPDATER_HEALTH_URLS`), index/manifest imzası, Docker socket'i mount edilmemiş. Başarısız bir çalışma `min(OPENLOG_UPDATER_INTERVAL, 1m)` sonra tekrar denenir, yani `docker compose up`'ın hemen ardından görülen hata (`lookup openlog … no such host`: updater `openlog`'dan önce başlar) bir dakika içinde geçer. |
| `waiting_for_maintenance_window` | `OPENLOG_UPDATER_MAINTENANCE_WINDOW` UTC'dir; Settings → Version and updates (`updater.maintenance_window.next_open_at`) bir sonraki açılışı gösterir. Daha erken kurmak için: "Install now, also outside the maintenance window" ile **Update now**. |
| `up_to_date` ama daha yeni bir sürüm var | `message` atlanan sürümleri listeler: daha önce başarısız olmuş (`/backups/updater-status.json` dosyasını silip `DELETE FROM system_state WHERE key='updater'` çalıştırarak `failed_versions` listesinden çıkar), `min_upgrade_from`, eksik image. |
| `rolled_back` | `steps[]` başarısız adımı gösterir; `docker compose logs openlog-updater` başarısız container'ın log'unun sonunu içerir. Önceki sürüm çalışmaya devam eder. |
| `rollback_failed` | Elle işlem gerekir: `docker ps -a`; `<name>-pre-update` container'ını adını geri verdikten sonra başlat veya yedekten geri yükle. |
| `.env`'deki bir ayarın etkisi yok | `docker inspect <container> --format '{{json .Config.Env}}'`; `compose_outdated` / `compose_outdated_bundle` bildirimi, compose dosyalarının ayardan eski olduğu anlamına gelir: onları güncelle (install-server.sh veya `git checkout v<running version>` + `docker compose up -d`). Updater'ın sonraki çalışması, başvurulmayan ayarları ekler ([.env ayarları](#settings-from-env)). |
| `compose_changes_pending` | install-server.sh'ı yeniden çalıştır (veya `docker compose -p openlog -f … --env-file … up -d`); değişen bir mount edilmiş dosya için adı geçen servisi yeniden başlatmak yeterlidir. |
| `compose-bundle` adımı başarısız | `openlog-compose-<v>.tar.gz` indirmesi veya sha256 (ağ, asset'i olmayan mirror: `OPENLOG_UPDATER_COMPOSE_SYNC=off` ve her sürüm için install-server.sh'ı yeniden çalıştır). Hiçbir şey değişmedi. |
| `cleanup` adımı: compose dosyaları geri yüklendi | Güncellemenin kendisi başarılı oldu; dosyalar önceki sürümde kaldı (`compose_outdated_bundle` bildirimi): install-server.sh'ı yeniden çalıştır. |
| `OPENLOG_SIGNUP_ENABLED=true` iken "Check now" / "Update now" yok | Onları yalnızca superadmin'ler görür: e-posta, `openlog` servisinin `OPENLOG_SUPERADMIN_EMAILS` değerinde olmalı ve doğrulanmış olmalı. |
| `updater_outdated*` | Updater container'ı çalışan sürümden eski ([Bildirimler](#notices)); `docker inspect <project>-openlog-updater-1 --format '{{.Config.Image}}'`. |
| `self-update` adımı başarısız / `updater_self_update_failed` | `reason` ve `docker compose logs openlog-updater` (yeni container'ın log'unun sonu dahildir). Önceki updater çalışmaya devam eder; nedeni düzelt ve install-server.sh'ı yeniden çalıştır. |
| `…-self-update-new` / `…-self-update-old` container'ları kalıyor | Bir handover sürüyor (~3 dakikaya kadar) veya elle müdahaleden kalmış: [elle kurtarma](#updater-self-update). |
| Contract migration hiç çalışmıyor | `openlog-migrate -plan` eski instance'ı söyler; onu durdur veya düzgün kapanmadan öldüyse 5 dakika bekle. |
| Arayüz sürekli yeniden yüklemeyi istiyor | Bir load balancer bazı istekleri hâlâ eski bir pod'a gönderiyor; rollout'u bitir. |
| Kabul testleri | `make mixed-version` (N ve N+1 yan yana, contract denetimi), `make updater-acceptance` (test expand + contract migration'larıyla Compose 0.9.0 → 0.9.1 → bozuk 0.9.2 geri alınır; aşağıya bak). |

### `make updater-acceptance` neyi kanıtlıyor {#what-make-updater-acceptance-proves}

`test/autoupdate` (compose projesi `openlog-updtest`, host portları 25xxx, yerel bir registry ve tek kullanımlık bir anahtarla
imzalanmış bir sürüm index'i) gerçek `deploy/compose` stack'ini ve `openlog-updater`'ı `auto` modunda çalıştırır:

1. **0.9.0 → 0.9.1**: her adım `ok` (backup → pull → migrate → recreate → health → cleanup → contract-migrate); test expand
   migration'ı 9001, migrate adımında çalışır, contract migration 9002 (`requires-all-at-least 0.9.1`) yalnızca eski container
   durduktan sonra contract-migrate adımında çalışır; host'lar, log/span/metrik satır sayıları, log ve APM sorguları, `pg_dump`
   yedeği ve yeniden yazılan `OPENLOG_IMAGE` kontrol edilir.
   **Self-update** (`OPENLOG_UPDATER_SELF_UPDATE=on`; `UPDTEST_FROM_IMAGE` veya `UPDTEST_UPDATER=to` ile atlanır): 0.9.0 updater'ı
   bir 0.9.1 container'ına devreder: `self_update.state=succeeded`, `self-update` adımı ok, `updater_version` 0.9.1,
   `openlog-updtest-openlog-updater-1` 0.9.1 digest'ini `unless-stopped` restart policy'si ve compose servis label'ıyla çalıştırır,
   hiçbir `-self-update-` container'ı kalmaz; ve 2. adımı bu yeni updater yapar.
2. **Bozuk 0.9.2** (allinone'u hiç hazır olmayan, bir expand migration'ı, 9003, fazla olan gerçek bir build): migrate adımı
   9003'ü uygular, health başarısız olur (`not healthy within …: connection refused`), container'lar 0.9.1'e geri alınır
   (`rolled_back`, `failed_versions` ve audit log'a yazılır). **Expand migration uygulanmış olarak kalır**: 0.9.1 aynı satırları
   sunar, sorgulara cevap verir ve onunla yeni veri ingest eder; expand migration'ların önceki sürümle uyumlu kalması gerekmesinin
   nedeni budur. Sonraki kontrol `up_to_date` ile `no eligible release newer than 0.9.1: 0.9.2: failed
   before on this installation` gösterir, Settings → Organization → Version and updates de bunu gösterir.

`UPDTEST_FROM_IMAGE=ghcr.io/onuragtas/openlog:<published version>` 0.9.0 yerine yayınlanmış bir sürümden başlatır (güncellemeyi onun
kendi `openlog-updater`'ı yapar; `UPDTEST_UPDATER=to` yenisini kullanır), böylece güncelleme o sürümden beri eklenen her gerçek
migration'ı da uygular. `UPDTEST_KEEP=1` stack'i korur.

2026-09-14'te ölçüldü (`UPDTEST_FROM_IMAGE=ghcr.io/onuragtas/openlog:0.1.9`, 212 sn): yayınlanmış 0.1.9 updater'ı çalışma ağacı
build'ini kurdu, migrate adımı yeni container başlamadan önce PostgreSQL 0010…0056 (16 migration), ClickHouse 0020…0050 (7) ve test
migration'larını uyguladı, her adım `ok` oldu, host'lar ve telemetri satırları korundu; ardından bozuk sürüm yukarıdaki gibi geri
alındı. Varsayılan çalıştırma (ağaçtan derlenen 0.9.0) 240 sn sürdü.
