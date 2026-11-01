# Katmanlı depolama: yerel disk → (warm disk) → S3 {#tiered-storage-local-disk-→-warm-disk-→-s3}

Eski telemetri, aynı tablolar ve API üzerinden sorgulanabilir kalarak ClickHouse veri diskinden daha ucuz bir depolamaya taşınır. Retention
(verinin ne zaman silindiği) değişmez. Kararlar: D-066 (tasarım), D-067 (TTL sahipliği).

```
insert ──► volume `default` (hot, local SSD) ──TTL age──► volume `warm` (optional local disk) ──TTL age──► volume `cold` (S3 + local read cache)
                                                                                                         └─ delete at the retention
```

## Nasıl çalışır {#how-it-works}

- **ClickHouse sunucu yapılandırması** `openlog_tiered` storage policy'sini tanımlar
  ([storage-tiered.xml](https://github.com/onuragtas/openlog/blob/master/deploy/compose/clickhouse/storage-tiered.xml)):
  - `default` volume'u: her insert'ün düştüğü `default` diski (`/var/lib/clickhouse`);
  - `warm` volume'u (isteğe bağlı, [storage-tiered-warm.xml](https://github.com/onuragtas/openlog/blob/master/deploy/compose/clickhouse/storage-tiered-warm.xml)):
    ikinci bir yerel disk;
  - `cold` volume'u: filesystem cache diski `openlog_s3_cache` üzerinden okunan (`OPENLOG_S3_CACHE_MAX_SIZE` ile sınırlı) `openlog_s3` diski
    (`object_storage`, `s3`, metadata yerel diskte).
- **openlog-migrate** (ve `OPENLOG_MIGRATE_ON_START` ile `openlog-allinone`), şema migration'larından sonra,
  `OPENLOG_STORAGE_TIERING_ENABLED=true` ise:
  1. **her replica'nın** policy'yi `default`, `cold` (ve warm yaşı ayarlıysa `warm`) volume'larıyla tanımladığını kontrol eder;
  2. taşıma alan ve henüz tüm replica'larda policy üzerinde olmayan her tablo için `ALTER TABLE openlog.<table>_local ON CLUSTER … MODIFY SETTING storage_policy = 'openlog_tiered',
     materialize_ttl_recalculate_only = 1` çalıştırır;
  3. `ALTER TABLE … ON CLUSTER … MODIFY TTL <time> + INTERVAL <warm> DAY TO VOLUME 'warm', <time> + INTERVAL <cold> DAY
     TO VOLUME 'cold', <time> + INTERVAL <retention> DAY` çalıştırır ve uygulanan TTL'i `openlog.table_settings` içine kaydeder
     (`ttl:<table>`).

  ClickHouse sonra mevcut part'ların TTL bilgisini yeniden hesaplar (`MATERIALIZE TTL`, yalnızca yeniden hesaplama, part'lar yeniden
  yazılmaz) ve arka plan mover'ı en yeni satırı yaştan eski olan part'ları taşır. Her adım idempotent'tir: ikinci çalıştırma hiçbir şey
  değiştirmez, başarısız bir çalıştırmayı sonraki tamamlar. `openlog-migrate -plan` bekleyen ALTER'ları yazdırır.
- **Insert'ler asla S3'e yazmaz:** volume'larda `perform_ttl_move_on_insert=false` vardır, bu yüzden taşıma yaşından eski satırlar (ajan
  birikintisi) hot volume'a düşer ve sonra taşınır.
- **Okumalar** değişmez: Distributed tablolar ve API part'ları nerede olurlarsa olsunlar okur. Cold part'lar S3'ten aralıklar halinde
  çekilir ve cache'te tutulur.
- **Silmeler** retention dolduğunda cold part'ları S3'ten de kaldırır (`ttl_only_drop_parts` olan tablolarda bütün part'lar, süreleri
  dolduktan en fazla `merge_with_ttl_timeout`, varsayılan olarak 4 saat, sonra).
- **Hot disk emniyet supabı:** `move_factor 0.1`, hot volume'da %10'dan az boş alan kaldığında en büyük part'ları erkenden sonraki
  volume'a taşır.

### Tablo sınıfları {#table-classes}

| Sınıf | Tablolar (`_local`) | Retention | S3'e taşıma (varsayılan) | Değişkenler |
|---|---|---|---|---|
| `metrics` | `metrics` | 30 g | 7 g | `OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS`, `…_WARM_AFTER_DAYS_METRICS` |
| `metrics_1m` | `metrics_1m` | 395 g | 30 g | `…_METRICS_1M` |
| `logs` | `logs` | 14 g | 3 g | `…_LOGS` |
| `traces` | `spans`, `trace_index` | 7 g | 3 g | `…_TRACES` |
| `apm` | `apm_transactions_1m`, `apm_service_edges_1m`, `apm_service_links_1m`, `apm_db_queries_1m`, `apm_errors_1m`, `apm_error_groups`, `apm_services`, `apm_service_hosts` | `OPENLOG_APM_RETENTION_DAYS` (30) | 7 g | `…_APM` |
| `alerts` | `alert_evaluations` | 30 g | 7 g | `…_ALERTS` |

Taşıma yaşı `0` ise veya retention'ın altında değilse o sınıf için taşıma yapılmaz (ve sınıfın tabloları `default` policy'de kalır). Küçük
varlık tabloları ve kuyruklar (`hosts`, `inventory_*`, `containers`, `apm_service_containers`, `apm_relink_queue`, `apm_error_group_dims`,
`apm_service_versions_1m`, `table_settings`) yerel diskte kalır.

## Tasarım tercihleri (D-066) {#design-choices-d-066}

- **`s3_plain_rewritable` değil, yerel metadata + filesystem cache ile `s3` object storage diski:** plain diskler metadata'yı bucket'ta
  tutar ve tek yazarlı / paylaşımlı katalog kurulumları içindir. Telemetri tablolarının ihtiyaç duyduğu her MergeTree işlemini (mutation
  ve taşımaların kullandığı hard link'ler) desteklemezler. Yerel metadata ile S3'teki bir part; merge, mutation, TTL ve replikasyon
  açısından yerel bir part gibi davranır.
- **Zero-copy replikasyon kapalı kalır** (`allow_remote_fs_zero_copy_replication = 0`, ClickHouse varsayılanı). Her replica kendi kopyasını
  kendi prefix'i altına yükler ve ona sahiptir (`…/{shard}/{replica}/`). Bu, S3 baytlarını replica sayısıyla çarpar, ama zero-copy
  replikasyon upstream'de production'a hazır değildir (merge, mutation ve replica kaybı etrafında bilinen veri kaybı ve sahipsiz nesne
  hataları var) ve replica'ların yaşam döngülerini Keeper kilitleriyle birbirine bağlardı.
- **Taşımalar yalnızca TTL ile**, tablo sınıfı başına ve retention'dan bağımsız yapılır. Bütün TTL ifadesi (retention + taşımalar)
  openlog'undur, böylece ikisi birbirinin üzerine asla yazmaz (D-067).
- **`skip_access_check=true`:** sunucu S3 erişilemezken de başlar (cold part okumaları başarısız olur, aşağıya bak). Bu yüzden yanlış kimlik
  bilgileri başlangıç hatası olarak değil, başarısız taşımalar olarak görünür: etkinleştirdikten sonra `openlog-admin storage status`
  ile kontrol et.

## Etkinleştir {#enable-it}

### Docker Compose (`deploy/compose`) {#docker-compose-deploy-compose}

1. `.env`:
   ```sh
   OPENLOG_CLICKHOUSE_STORAGE_CONFIG=./clickhouse/storage-tiered.xml
   OPENLOG_STORAGE_TIERING_ENABLED=true
   OPENLOG_S3_ENDPOINT=https://my-bucket.s3.eu-central-1.amazonaws.com/openlog/ch-1/
   OPENLOG_S3_REGION=eu-central-1
   OPENLOG_S3_ACCESS_KEY_ID=AKIA...            # veya boş + OPENLOG_S3_USE_ENVIRONMENT_CREDENTIALS=true (instance profile)
   OPENLOG_S3_SECRET_ACCESS_KEY=...
   OPENLOG_S3_CACHE_MAX_SIZE=20Gi
   # isteğe bağlı: OPENLOG_STORAGE_COLD_AFTER_DAYS_LOGS=5 ...
   ```
   Bucket yerine yerel test: `COMPOSE_PROFILES=tiered` MinIO'yu başlatır (bucket `openlog-cold`) ve S3 varsayılanları onu gösterir. Bu MinIO
   donmuş `bitnamilegacy` arşividir, çünkü MinIO topluluk imajını ve binary'lerini anonim istemcilere sunmayı bıraktı. uid 1001 olarak
   çalışır, bu yüzden o değişiklikten önce oluşturulmuş bir stack'in volume'unu bir kez devretmesi gerekir:
   `docker run --rm -v openlog_minio-data:/d alpine chown -R 1001:0 /d`.
2. `docker compose up -d --wait` (`clickhouse`'u yeni yapılandırmayla, migration'ları taşımaları uygulayan `openlog`'u da yeniden oluşturur).
   Kontrol: `docker compose exec openlog openlog-admin storage status`.

Dosyadan kimlik bilgileri: `clickhouse/storage-tiered-credentials.xml.example` dosyasını kopyala, doldur, `chmod 0640` yap,
`OPENLOG_CLICKHOUSE_S3_CREDENTIALS_FILE=/path/to/it` ayarla (`openlog-storage.xml`'den sonra sıralanması gereken
`config.d/zz-openlog-s3-credentials.xml` olarak mount edilir). Warm volume: `OPENLOG_CLICKHOUSE_STORAGE_WARM_CONFIG=./clickhouse/storage-tiered-warm.xml`
(Docker volume'u `clickhouse-warm`; oraya başka bir diski bind-mount et) ve `OPENLOG_STORAGE_WARM_AFTER_DAYS_<CLASS>`.

### Helm, `dependencies.mode: operators` {#helm-dependencies-mode-operators}

```yaml
clickhouse:
  tieredStorage:
    enabled: true
    coldAfterDays: {logs: 3, traces: 3, metrics: 7}
    s3:
      endpoint: https://my-bucket.s3.eu-central-1.amazonaws.com/openlog/{shard}/{replica}/
      region: eu-central-1
      credentialsSecret: {name: openlog-s3}          # anahtarlar: access-key-id, secret-access-key
      # bunun yerine IRSA: credentialsSecret.name "", useEnvironmentCredentials: true, serviceAccountName: clickhouse-s3
      cacheMaxSize: 50Gi
      diskSettings: {}                                # örn. server_side_encryption_kms_key_id
    warm: {enabled: false}
```

Chart, storage policy'yi ClickHouseInstallation'a (`config.d/openlog-storage.xml`, değerler container ortamından), kimlik bilgilerini Secret'tan,
cache'i veri volume'una (PVC'yi buna göre boyutlandır) ve `warm.enabled` ile replica başına ikinci bir PVC ekler. Operator yeni yapılandırma
için ClickHouse pod'larını yeniden başlatır.

Mevcut bir release'te etkinleştirmek **iki** `helm upgrade` ister: migrate Job'u bir pre-upgrade hook'tur ve ClickHouseInstallation policy'yi
almadan önce çalışır, bu yüzden ilk upgrade'de chart migrate için `OPENLOG_STORAGE_TIERING_ENABLED=false` tutar (canlı CHI'ye bakar).
`kubectl get chi` `Completed` gösterene kadar bekle, sonra taşımaları uygulamak için aynı `helm upgrade`'i yeniden çalıştır. Bu ele alınmadan
önce migrate hook'u deadline'ına kadar `storage policy "openlog_tiered" is not
usable` ile başarısız oluyor ve CHI değişikliği hiç uygulanmıyordu (kind, 2026-09-14). Sıfırdan kurulum (post-install hook) her şeyi tek seferde
uygular. Bucket yerine küme içi MinIO: `s3.endpoint: http://minio.<ns>.svc:9000/<bucket>/{shard}/{replica}/`,
`region: us-east-1` (`deploy/helm/test/kind-validate.sh` betiği, `tiered` adımı; henüz uçtan uca doğrulanmadı). `openlog-admin storage status` herhangi bir api pod'unda çalışır:
`kubectl exec deploy/<release>-api -- openlog-admin storage status`.

### Harici ClickHouse (Helm `external` modu, kendi sunucuların) {#external-clickhouse-helm-external-mode-own-servers}

`storage-tiered.xml` (ve isteğe bağlı olarak `storage-tiered-warm.xml`) dosyasını **her** sunucunun `config.d/` klasörüne koy,
`OPENLOG_S3_*` / `OPENLOG_WARM_PATH` değişkenlerini sunucunun ortamına ekle (veya `from_env` özniteliklerini değerlerle değiştir), endpoint'te
`{shard}/{replica}` makrolarını kullan, sunucuları tek tek yeniden başlat, sonra `clickhouse.tieredStorage.enabled: true` (veya openlog-migrate
için `OPENLOG_STORAGE_TIERING_ENABLED=true`) ayarla. İlk volume'un **adı `default` olmalı ve `default` diskini içermelidir**: ClickHouse, bir tablonun
policy'sini mevcut (`default`) policy'sindeki bir volume'u içermeyen bir policy ile değiştirmeyi reddeder (`New storage policy … shall contain volumes of the old storage
policy`). Bir replica'da yapılandırma eksikse openlog-migrate `storage policy "openlog_tiered" is not usable … <host>: policy not defined` ile durur.

## Değiştir veya kapat {#change-or-disable}

- **Başka yaşlar:** değişkenleri değiştir ve migrate'i çalıştır, yalnızca değişen tablolar ALTER edilir. Yaş büyüdüğünde zaten S3'te olan part'lar
  geri taşınmaz.
- **Kapatma** (`OPENLOG_STORAGE_TIERING_ENABLED=false`): migrate taşıma ifadelerini kaldırır. Tablolar policy'yi **korur** (part'lar S3'te olabilirken
  ClickHouse `default`'a geri dönemez) ve S3'teki part'lar süreleri dolana kadar orada okunabilir kalır. Veriyi yerel disklere geri getirmek için
  (yer gerekir):
  `ALTER TABLE openlog.logs_local ON CLUSTER openlog MOVE PARTITION '2026-09-10' TO VOLUME 'default'`. `cold` üzerinde herhangi bir part olduğu sürece S3
  yapılandırmasını sunucularda tut (`openlog-admin storage status`).
- **APM retention'ı** (`OPENLOG_APM_RETENTION_DAYS`) aynı adımda uygulanır ve taşımaları korur.

## İzle {#observe}

`openlog-admin storage status [--json]` (OPENLOG_CLICKHOUSE_* yazma kimlik bilgileri, bekleyen değişiklikler için OPENLOG_STORAGE_*) tüm replica'ları
okur:

```
cluster openlog, tiering enabled, storage policy openlog_tiered: default[default] -> warm[openlog_warm] -> cold[openlog_s3]

HOST        DISK              TYPE           REMOTE  BROKEN  USED  FREE      TOTAL     CACHE
clickhouse  default           Local          false   false   57%   37.4 GiB  87.1 GiB
clickhouse  openlog_s3        ObjectStorage  true    false   -     -         -
clickhouse  openlog_s3_cache  ObjectStorage  true    false   -     -         -         /var/lib/clickhouse/openlog_s3_cache/
clickhouse  openlog_warm      Local          false   false   57%   37.4 GiB  87.1 GiB

TABLE       CLASS  VOLUME   DISK          REPLICAS  PARTS  ROWS  SIZE      PARTITIONS              OVERDUE MOVES  POLICY
logs_local  logs   default  default       1         2      525   4.7 KiB   2026-09-13..2026-09-14  -              openlog_tiered
logs_local  logs   cold     openlog_s3    1         7      3467  16.9 KiB  2026-09-04..2026-09-10  -              openlog_tiered
logs_local  logs   warm     openlog_warm  1         2      1008  4.9 KiB   2026-09-11..2026-09-12  -              openlog_tiered

moves in progress: 0, parts past their move TTL still on the hot volume: 0
failed moves (24h): 0
detached parts: 0
pending openlog-migrate changes: none
```

(`COMPOSE_PROFILES=tiered` ile Compose stack'i, warm volume, `OPENLOG_STORAGE_WARM_AFTER_DAYS_LOGS=1`, 5 gün sonra cold.)

- **SIZE** replica'lar üzerinden toplanır (S3'teki baytlar replica başına bir kez faturalanır).
- **OVERDUE MOVES:** hot volume'da olup taşıma TTL'i dolmuş part'lar. Sayının büyümesi taşımaların başarısız olduğu (`system.part_log`'dan gelen
  *failed moves*'a bak) veya mover'ın geride kaldığı (`background_move_pool_size`, varsayılan 8) anlamına gelir.
- Dashboard'lar için ClickHouse metrikleri (`system.metrics` / `system.events`, ClickHouse Prometheus endpoint'i yayınlar):
  `FilesystemCacheSize`, `CachedReadBufferReadFromCacheBytes` / `…FromSourceBytes` (cache isabet oranı), `S3ReadRequestsErrors`,
  `S3WriteRequestsErrors`, `DiskS3PutObject`, `BackgroundMovePoolTask`; çalışan taşımalar için `system.moves`.

## Maliyet {#cost}

- **Depolama:** sıkıştırılmış part baytları × replica (zero-copy yok). Metrikler ve loglar ClickHouse'ta 5–15× sıkışır, bu yüzden S3 ham hacmin
  küçük bir kısmını tutar.
- **İstekler:** her part birkaç nesnedir (kolon dosyası başına bir tane + küçük metadata dosyaları; testte replica başına ~0.9 MiB, 634 nesneydi).
  Taşımalar ve cold volume'daki merge'ler PUT yapar, okumalar aralıklı GET yapar (cache azaltır), silmeler AWS'de ücretsizdir. Daha az ve daha büyük
  part'ları tercih et: processor'ın batch varsayılanlarını koru.
- **Cold volume'daki merge'ler** part'ları indirir ve yeniden yükler. Günlük partition'ların part'ları genellikle taşıma yaşına ulaşmadan tamamen
  merge edilmiş olur.
- **Bucket ayarları:** lifecycle expiration veya transition olmasın (nesneleri ClickHouse kendisi siler; Glacier/Deep Archive nesneleri okunamaz,
  S3 Standard-IA 30 günlük minimum ve geri çağırma ücreti alır). Versioning kapalı olsun veya kısa bir noncurrent-version expiration kullan, yoksa
  silinen part'lar maliyet üretmeye devam eder.
- **Cache:** yerel diskte `OPENLOG_S3_CACHE_MAX_SIZE` (LRU ile boşaltılır). Boyutunu dashboard'ların ve alarmların taşıma yaşının ötesinde okuduğu
  veriye göre belirle.

## Yedekler ve geri yükleme {#backups-and-restore}

- `/var/lib/clickhouse`'un **disk veya volume snapshot'ı** cold part'lara yalnızca *referansları* içerir. Yalnızca başvurulan nesneler hâlâ varken
  doğru geri yüklenir. Merge'ler veya TTL silmeleri onları kaldırdıktan sonra geri yüklenen cold part'lar bozuk olur. Tiering açıkken yalnızca disk
  snapshot'larına güvenme.
- **`BACKUP … ON CLUSTER … TO S3(…)`** (veya object disk destekli `clickhouse-backup`) kullan: cold part'ların verisini de kopyalar (kaynak ve hedef
  aynı S3 servisindeyse sunucu tarafı kopyalama), shard başına bir kopya.
- `TIEREDTEST_BACKUP=1 make tieredtest` ile prova edildi (2 shard × 2 replica, MinIO, part'lar `default`, `warm` ve `cold` üzerinde): test aşaması 132 cold
  part'ı 2.8 sn'de yedekliyor ve 3.3 sn'de geri yüklüyor. Ağır host yükü altındaki bir stack'te elle yapılan çalıştırma (≈370 000 satır, 328 part'ın
  146'sı S3'te) 37 sn ve 63 sn sürdü:

  ```sql
  -- ayrı bir bucket veya prefix, asla bir replica'nın veri prefix'i değil
  BACKUP DATABASE openlog ON CLUSTER 'openlog'
    TO S3('https://backup-bucket.s3.eu-central-1.amazonaws.com/openlog/2026-09-14/', '<key id>', '<secret>');
  SELECT name, status, error, total_size FROM system.backups ORDER BY start_time DESC LIMIT 1;   -- BACKUP_CREATED

  -- geri yükleme (openlog durdurulmuş: docker compose stop openlog veya api/ingest/processor'ı 0'a ölçekle)
  DROP DATABASE openlog ON CLUSTER 'openlog' SYNC;
  RESTORE DATABASE openlog ON CLUSTER 'openlog'
    FROM S3('https://backup-bucket.s3.eu-central-1.amazonaws.com/openlog/2026-09-14/', '<key id>', '<secret>');
  SYSTEM SYNC REPLICA ON CLUSTER 'openlog' openlog.logs_local;
  ```

  Sonra tablo başına satırları (`SELECT table, sum(rows) FROM clusterAllReplicas('openlog', system.parts) WHERE database =
  'openlog' AND active AND hostName() IN (<one replica per shard>) GROUP BY table`) yedekten önceki aynı sorguyla karşılaştır ve
  `openlog-admin storage status`'a bak. Bu karşılaştırma yalnızca düz `ReplicatedMergeTree` tablolarında geçerlidir: Summing/Replacing rollup'ları
  (`usage_signals_1h`, `usage_entities_1d`, `schema_migrations`, `table_settings`) geri yükleme sırasında merge olur ve aynı içerikle daha az part satırı
  gösterir (testte `usage_entities_1d_local` içinde 1 670 → 1 103 satır). Onların yerine `sum(items)` / farklı key'leri karşılaştır. Provada ortaya
  çıkan uyarılar:
  - **Geri yüklenen part'lar, cold olanlar dahil, hot `default` volume'una düşer.** Tablolar `openlog_tiered` policy'sini ve TTL'lerini korur, bu yüzden
    mover eski part'ları yeniden `warm`/`cold`'a gönderir. Geri yükleme sırasında hot disklerde tüm veritabanına yer olması gerekir (veya tablo tablo
    geri yükle).
  - **Keeper yolları sabittir** (`/clickhouse/tables/{shard}/openlog/<table>`): çalışan kümeye `RESTORE` için önce veritabanını `SYNC` ile düşürmek gerekir.
    Yanına geri yüklemek (başka bir veritabanı adıyla) bu yollarda çakışır. Provayı kendi Keeper'ı olan ayrı bir kümede yap.
  - Bir S3 kesintisinin hemen ardından (testteki MinIO durdurup başlatma) veri diskinin okuma, taşıma ve silme işlemleri toparlanmış olsa da
    `BACKUP … TO S3` bir süre başarısız olmaya devam etti: yedeği açarken (`BackupWriterS3::fileExists`) veya ilk metadata nesnelerini yazarken boş mesajlı
    `Code: 499 … S3_ERROR`. Boşta bir host'ta 23 sn içindeki üç deneme başarısız oldu, aynı ifade aynı kümede dakikalar sonra başarılı oldu ve öncesinde kesinti
    olmayan her yedek ilk seferde başarılı oldu. Yedekleri bir S3 olayının hemen sonrasına zamanlama, başarısız bir yedeği sonra yeniden dene,
    `system.backups` içindeki `status`/`error` değerlerine bak ve yalnızca durumu `BACKUP_CREATED` olan bir yedekten geri yükle. (Bu yüzden testin
    `backup_restore` aşaması kesinti aşamalarından önce çalışır.)
  - Geri yüklemeden sonra openlog-migrate'in TTL adımı hiçbir şey yapmaz (`openlog.table_settings` tablolarla birlikte geri yüklenir).
- İki kümeyi (veya geri yüklenmiş bir kopyayı) asla aynı prefix'e yöneltme, yeni bir prefix/bucket'a geri yükle. Yerel diski kaybolan bir replica, sağlıklı
  eşinden çekerek yeniden kurulur (çekilen part'lar kendi prefix'i altına yeniden yüklenir). Eski halinin nesneleri sahipsiz kalır: replica yeniden
  senkron olduktan sonra o prefix'i sil veya yeni replica'ya yeni bir prefix ver.
- PostgreSQL yedekleri (`openlog-updater`, CloudNativePG) bundan etkilenmez.

## Arıza durumları {#failure-modes}

`make tieredtest` ile ölçüldü (ClickHouse çalışırken MinIO durduruldu):

| Durum | Etkisi |
|---|---|
| S3 erişilemez, insert'ler | **Etkilenmez** (hot volume), taşıma yaşından eski satırlar dahil (testte 1 000 satır için 270 ms) |
| S3 erişilemez, yalnızca hot veri üzerindeki sorgular | Partition pruning cold part'ları atlıyorsa etkilenmez (testte 14 ms) |
| S3 erişilemez, cold part'lara dokunan sorgular | `Code: 499 … S3_ERROR` (`This error happened for S3 disk …`) ile başarısız olur. API `503 storage_unavailable` (`"retryable": true`, `Retry-After`) döner ve arayüz genel bir hata yerine "archive storage cannot be read right now" gösterir. Bağlantı reddi saniyeler içinde başarısız olur, erişilemeyen bir endpoint ise S3 istemcisinin yeniden denemeleri bitene kadar (dakikalar) bekletebilir. API sorgularını `OPENLOG_API_QUERY_TIMEOUT` (`504 timeout`), alarm değerlendirmelerini `OPENLOG_ALERT_QUERY_TIMEOUT` keser (değerlendirme hatası, `no data` kuralları tetiklenebilir) |
| S3 erişilemez, taşımalar / cold part merge'leri / cold part'ların TTL silmeleri | Başarısız olur ve yeniden denenir (`failed moves (24h)`). Part'lar dolmaya devam eden hot volume'da kalır: *FREE* ve *OVERDUE MOVES* değerlerini izle |
| S3 erişilemezken ClickHouse yeniden başlatma | Sunucu başlar (`skip_access_check`). Yakın zamanda düşürülmüş part'ların boş kalıntıları `ignored_*` olarak detach edilebilir (`detached parts`). Satır taşımazlar ve `ALTER TABLE … DROP DETACHED PART '…' SETTINGS allow_drop_detached = 1` ile kaldırılabilirler |
| S3 geri geldi | Okumalar, taşımalar ve silmeler müdahalesiz devam eder. Testte toparlanma sonrası satır sayıları ve her shard'ın iki replica'sı aynıydı |
| Yanlış kimlik bilgileri / bucket policy | Başlangıç başarılı olur, taşımalar `Access Denied` ile başarısız olur (`failed moves`), veri hot'ta kalır |
| Bucket veya prefix silindi | O replica için cold part'lar kaybolur, shard'ın diğer replica'sında hâlâ kendi kopyası vardır. Replica'yı yeniden kur (yerel tablo verisini sil ve çekmesine izin ver) |

Prefix üzerinde gereken en az IAM izinleri: `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, `s3:ListBucket`
(SSE-KMS ile ayrıca `kms:GenerateDataKey`, `kms:Decrypt`).

## Güvenlik {#security}

- Statik key'ler: ortam (`OPENLOG_S3_ACCESS_KEY_ID` / `_SECRET_ACCESS_KEY`), bir kimlik bilgisi XML dosyası veya Kubernetes Secret'ı (Helm). ClickHouse,
  key'ler dahil birleştirilmiş yapılandırmayı `/var/lib/clickhouse/preprocessed_configs/config.xml` dosyasına yazar (mod 0640, kullanıcı clickhouse).
- IAM rolleri: key'ler boşken `OPENLOG_S3_USE_ENVIRONMENT_CREDENTIALS=true`, AWS varsayılan zincirini kullanır (`AWS_*` ortamı, web identity / IRSA, EKS Pod
  Identity, instance profile).
- Şifreleme: bucket varsayılan şifrelemesi (SSE-S3) hiçbir şey gerektirmez. SSE-KMS / SSE-C için:
  [storage-tiered-sse.xml.example](https://github.com/onuragtas/openlog/blob/master/deploy/compose/clickhouse/storage-tiered-sse.xml.example) (Helm:
  `tieredStorage.s3.diskSettings`). HTTPS endpoint'leri kullan.

## Test {#test}

`make tieredtest` (`test/e2e/tieredtest`, compose projesi `openlog-tiered`, ~6 dk): MinIO ve değiştirilmemiş `storage-tiered.xml` + `storage-tiered-warm.xml`
kullanan 2 shard × 2 replica'lık bir küme, her replica farklı bir kimlik bilgisi kaynağıyla (ortam key'leri, `AWS_*` ile `use_environment_credentials`,
kimlik bilgisi dosyası, region'sız). Şemayı uygular, Distributed tablolar üzerinden eski zaman damgalı metrik, log, span ve APM satırları ekler ve şunları
kontrol eder: tiering kapalıyken hiçbir şey olmaması; etkinleştirmenin her replica'da tam olarak yönetilen tabloları değiştirmesi ve ikinci çalıştırmanın hiçbir
şey değiştirmemesi; part'ların dört replica'nın hepsinde `warm` ve `cold`'a ulaşması ve nesnelerin her `{shard}/{replica}` prefix'i altında görünmesi;
cache'i silip tüm sunucuları yeniden başlattıktan sonra Distributed tablolar üzerinden sayıların değişmemesi; APM retention'ını düşürmenin cold part'ları silmesi;
bir S3 kesintisi (insert'ler, hot ve cold sorgular, sunucu yeniden başlatma) ve toparlanma; kapatmanın taşımaları kaldırıp veriyi koruması. `TIEREDTEST_KEEP=1`
stack'i çalışır bırakır. `TIEREDTEST_BACKUP=1` kapatmadan önce `backup_restore` aşamasını ekler ([Yedekler ve geri yükleme](#backups-and-restore)): MinIO bucket'ı
`openlog-backup` içine `BACKUP DATABASE openlog ON CLUSTER
… TO S3` (kesinti aşamalarından sonra yeniden denenir), `DROP DATABASE … SYNC`, `RESTORE`, ardından düz tabloların part satırları ve Summing/Replacing
rollup'larının mantıksal kontrolleri, Distributed sayıları, `warm`/`cold`'a geri taşımalar ve hiçbir şey yapmayan bir TTL adımı.
