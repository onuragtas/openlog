# Disk alanı {#disk-space}

openlog'un ClickHouse veri diskinin dolmaması için neler yaptığı ve mevcut bir sunucuyu yükselttikten sonra operatörün yapması gereken tek işlem.

Telemetri retention'ının kendisi burada anlatılmıyor: her telemetri tablosu sahibi `openlog-migrate` olan bir TTL taşır (sınıflar ve taşıma TTL'leri için
bkz. [tiered-storage.md](/tr/operations/tiered-storage)). Bu sayfa telemetri *olmayan* alanla ilgili.

## ClickHouse'un kendi sistem log tabloları {#clickhouse-s-own-system-log-tables}

ClickHouse, veriyle aynı diske kendine ait bir düzine log tablosu yazar ve varsayılan olarak hiçbirinin süresi dolmaz. `metric_log` ve
`asynchronous_metric_log` her saniye bir satır alır, `processors_profile_log` ise sorgu *operatörü* başına bir satır. Sessiz bir kurulumda, hata ayıklamaya
yardım etmeleri gereken telemetrinin boyunu aşarlar: ölçülen bir sunucuda 36 KiB log ve 26 KiB span'e karşılık 4.2 GiB `processors_profile_log` ve
1.9 GiB `query_views_log` vardı.

openlog bunlar için sınırlarla gelir:

- Compose: [`deploy/compose/clickhouse/openlog-system-logs.xml`](https://github.com/onuragtas/openlog/blob/master/deploy/compose/clickhouse/openlog-system-logs.xml),
  koşulsuz olarak `config.d/` içine mount edilir.
- Helm (`dependencies.mode: operators`): aynı XML, ClickHouseInstallation içinde `config.d/openlog-system-logs.xml` olarak.
- Helm `external` modu: hiçbir şey uygulanmaz, aynı dosyayı kendi sunucularına koy.

On iki tablo sınırlanır: `query_log`, `part_log` ve `error_log` 14 gün, `crash_log` 90 gün, hacimli olanlar (`trace_log`, `metric_log`,
`asynchronous_metric_log`, `text_log`, `query_thread_log`, `processors_profile_log`, `query_views_log`, `query_metric_log`) 3 gün tutar.

`query_log` ve `part_log` bilerek daha uzundur: kullanım toplayıcısı, tenant sorgu hesaplamasını `system.query_log`'dan `usage_queries_1h` içine okur,
`openlog-admin storage status` ise son 24 saatin başarısız part taşımalarını `system.part_log`'dan okur. Bunları toplama penceresinin altına kısaltmak
kullanım verisini sessizce kaybettirir. `crash_log` çok küçüktür ve geçmişini isteyeceğin tek tablodur.

Yalnızca ClickHouse'un varsayılan olarak açtığı tablolar listelenmiştir. Bazı log tablolarında bir config bölümünün varlığı onları *açan* şeydir (bilinen
örnek `text_log`), bu yüzden kapalı olan birinin adını yazmak onu sınırlamak yerine hacim yaratır.

Sunucu log *dosyaları* aynı dosyada sınırlanır (`100M` × 3). Resmi imaj aksi halde log türü başına 10 × 1000M'ye kadar, veri diskinde tutar.

### `partition_by` neden `ttl` ile birlikte ayarlanıyor {#why-partition-by-is-set-together-with-ttl}

Tek başına TTL yetmez ve bunu yanlış yapmak hiçbir şey yapmamaktan daha kötüdür. Sistem tablolarının varsayılan partition'lanması aylıktır ve ClickHouse
bütün part'ların süresini doldurur, bu yüzden partition aralığından kısa bir TTL, ayın tamamının satır düzeyinde yeniden yazılmasına dönüşür. Bin civarı
kolonu olan `metric_log`'da bu merge belleği tüketir, başarısız olur ve bir çekirdeği %100'de tutarak sonsuza kadar yeniden dener.

Günlük partition'larla (`partition_by: event_date`) her süre dolumu bir partition düşürmeye dönüşür: yeniden yazma yok, merge baskısı yok.

`engine` bilerek ayarlanmaz. `partition_by`/`ttl`'nin eşlikçisi değil alternatifidir ve `ORDER BY`'ı tablo tablo yazmak, ClickHouse'un sahip olduğu ve
sürümler arasında değiştirdiği yapıyı sabitlemek olurdu.

## Mevcut bir sunucuyu yükselttikten sonra: yeniden adlandırılan tabloları düşür {#after-upgrading-an-existing-server-drop-the-renamed-tables}

Var olan bir sistem tablosunun `partition_by` değerini değiştirmek onu yeniden yazmaz. ClickHouse şunu loglar:

```
Existing table system.query_log for system log has obsolete or different structure
```

Tabloyu `system.query_log_0` olarak yeniden adlandırır ve yeni yapılandırmayla taze bir tane oluşturur. **Yeniden adlandırılan tablolar tüm satırlarını korur
ve onlara TTL uygulanmaz**, yani sen düşürene kadar alan geri kazanılmaz. Yeni yapılandırmayla ilk yeniden başlatmadan sonra bunu bir kez yap:

```sql
SELECT name, formatReadableSize(total_bytes) AS size
FROM system.tables
WHERE database = 'system' AND match(name, '_log_[0-9]+$')
ORDER BY total_bytes DESC;
```

sonra listelenen her ad için:

```sql
DROP TABLE system.query_log_0;
```

Sayısal sonekli tablolar her zaman kalıntıdır: ya bu değişikliğin ya da bir sistem tablosunun kolonlarını değiştiren daha eski bir ClickHouse yükseltmesinin.
Onları okuyan hiçbir şey yok.

## Diskler ne kadar dolu {#how-full-the-disks-are}

Api leader'ı 5 dakikada bir ClickHouse'a yerel disklerinin ne kadar dolu olduğunu sorar (`system.disks`, replica başına bir satır) ve sonucu üç şekilde
yayınlar:

| Nerede | Ne |
|---|---|
| `/metrics` üzerinde `openlog_clickhouse_disk_used_ratio{host,disk}` | kullanılan oran, 0..1, alarm kurulacak seri budur |
| `openlog_clickhouse_disk_free_bytes`, `openlog_clickhouse_disk_total_bytes` | arkasındaki ham sayılar |
| `openlog-admin storage status` | disk başına bir `USED` kolonu |

Yalnızca yerel diskler ölçülür. Object storage diski ve önündeki cache için ClickHouse `free_space`/`total_space` değerini yer tutucu olarak bildirir, bu
yüzden orada bir yüzde yalan olurdu. O satırlar `-` yazdırır.

Yüzde, ClickHouse'un `free_space` değerinden türetilir ve bu değer ext4'te root için ayrılmış blokları dışarıda bırakır. O bloklar ClickHouse'un kullanımına
açık değildir, bu yüzden kullanılmış sayılır. Aksi halde sayı, sunucunun gerçekte hissettiği baskının birkaç puan altında kalırdı.

Bir küme, ortalaması kadar değil, en dolu replica'sı kadar doludur: ortalama, part kabul etmeyi bırakmak üzere olan tek node'u gizler.

### Eşikler {#thresholds}

**%80**'e ulaşan disk uyarı, **%90**'a ulaşan disk hata olarak; host, disk, yüzde, boş ve toplam baytlarla birlikte loglanır. 80, retention'ı kısaltmanın
hâlâ işe yarayacağı kadar erkendir. 90'da bir merge geçici alan yokluğundan başarısız olabilir ve başarısız merge, ingest'i yavaşlatmak yerine durdurur.

`openlog_clickhouse_disk_reported_level{host,disk}`, diskin şu anda bildirildiği seviyeyi taşır. Hepsinin altındaysa `0` olur.

Her seviye her kontrolde değil, **bir kez** bildirilir. Bir seviyenin yeniden devreye girmesi için diskin, bildirildiği seviyenin **5 puan** altına inmesi
gerekir. Böylece %80'in iki yanında gezinen bir disk beş dakikada bir bildirim üretmez ve yalnızca bir puan düşen disk toparlanmış sayılmaz. Seviyeler
snapshot ile birlikte saklanır, yani yeniden başlatma veya leader değişimi yerinden oynamamış bir diski yeniden bildirmez.

## Disk dolarken en eski günü düşürmek {#dropping-the-oldest-day-when-the-disk-fills}

Tablo TTL'leri verinin ne kadar *eski* olabileceğini sınırlar. Ne kadar *çok* olabileceğini sınırlayamazlar: ingest'i ikiye katlarsan aynı retention, diski
hâlâ TTL'i içinde olan veriyle doldurur ve ClickHouse'ta bunu bırakacak hiçbir şey yoktur.

Bunun cevabı shedding'dir ve **sen açana kadar kapalıdır**. Telemetri siler ve geri alınamaz.

| Ayar | Yerleşik | Ne yapar |
|---|---|---|
| `shed_enabled` | **kapalı** | Bu kapalıyken hiçbir şey düşürülmez |
| `shed_start_percent` | 90 | Düşürme bu değerde veya üstünde başlar |
| `shed_stop_percent` | 85 | Disk bunun altına indiğinde düşürme durur |
| `shed_min_partitions` | 3 | Disk ne kadar dolu olursa olsun her tablo en az bu kadar gün tutar |
| `shed_max_drops_per_run` | 20 | Tek bir tur bundan fazla gün düşürmez |

`shed_start_percent` kritik bildirim seviyesinin altına ayarlanamaz: veri, aynı sayfanın hâlâ sağlıklı dediği bir yüzdede kaybolmamalıdır. API de veritabanı
da bunu reddeder.

### Sırayla neden vazgeçilir {#what-is-given-up-in-order}

Bayt başına en az değerli olan önce, böylece baskı altındaki disk, olanların kaydını kaybetmeden önce hata ayıklama ayrıntısını kaybeder:

1. profiller
2. metrik exemplar'ları
3. trace'ler: `spans` ve `trace_index` **birlikte**, çünkü olmayan satırlara işaret eden bir indeks, hiç indeks olmamasından kötüdür
4. veritabanı oturum örnekleri
5. loglar
6. veritabanı sorgu istatistikleri ve planları
7. ham metrikler

**Hiçbir baskıda düşürülmeyenler:** `metrics_1m` (uzun vadeli hafıza, üstelik aylık partition'lı, yani tek düşürme bütün bir ayı götürürdü), `usage_*`
(faturalama), `apm_*` rollup'ları (geldikleri span'lerin süresi dolunca geriye kalan) ve tüm envanter, alarm, RUM ve zafiyet tabloları.

### Nasıl düşürür {#how-it-drops}

`ALTER TABLE … ON CLUSTER … DROP PARTITION ID '<YYYYMMDD>'`, en eski gün önce, tahmin durma seviyesine ulaşana kadar her seferinde bir gün.

Satırlar yerine bütün partition'ın düşürülmesi bilinçli. `ALTER … DELETE` bir mutation'dır: part'ları yeniden yazar, yenilerini yazmak için boş alan gerekir,
yani disk en doluyken en pahalı halindedir. Partition düşürmek alanı anında boşaltır ve düşürülebilen her tablo günlük partition'lıdır.

### Açmadan önce neleri götüreceğini görmek {#seeing-what-it-would-take-before-turning-it-on}

```
openlog-admin storage clean            # önizleme: hiçbir şey silmez
openlog-admin storage clean --apply    # listelenen günleri siler
```

Önizleme, shedding kapalıyken bile açıkmış gibi plan yapar, çünkü kapalıyken sorulan soru da budur. En dolu diski, kullandığı seviyeleri ve nereden geldiklerini,
vazgeçeceği her gün için bir satır yazdırır:

```
disk clickhouse/default: 93% used, 20.4 GiB free of 291.7 GiB
levels (stored settings): shedding off, start 90%, stop 85%, keep 3 days per table, at most 20 days per run

UNIT      DAY       TABLES                         SIZE
profiles  20260901  profiles_local                 2.1 GiB
traces    20260901  spans_local,trace_index_local  9.4 GiB

2 days, 11.5 GiB: nothing was deleted. Pass --apply to delete them.
automatic shedding is off: this is what it would give up if you turned it on.
```

`--apply`, otomatik shedding kapalıyken bile çalışır: zamanlanmış iş değil, terminaldeki bir kişiden gelen açık bir talimattır. Aynı audit satırlarını yazar ve
aynı alt sınırlara ve üst limitlere uyar.

Seviyeler PostgreSQL'den okunamadığında komut bunu söyler ve yerleşik olanlara döner, çünkü yanlış seviyelerle yapılan bir önizleme hiç önizleme olmamasından
kötüdür.

### Geride ne bırakır {#what-it-leaves-behind}

Düşürülen her gün; günü, tabloları ve baytları belirten bir uyarı olarak loglanır, `openlog_clickhouse_disk_days_dropped_total{unit}` ve
`openlog_clickhouse_disk_dropped_bytes_total` içinde sayılır ve `system:disk-space` aktörüyle `disk_space.partition_dropped` olarak `audit_log`'a yazılır.

Disk seviyeyi geçmişse ve vazgeçilebilecek hiçbir şey yoksa (her tablo alt sınırında veya yalnızca korunan tablolar kalmış), bu sessizce geçilmez, hata olarak
loglanır. O noktada yalnızca sen bir şey yapabilirsin.

## Yüklenen source map'ler {#uploaded-source-maps}

Bir RUM source map'i bir kez yüklenir ve bir tarayıcı stack'inin minify'dan çıkarılması gerektiğinde okunur, bu yüzden ait olduğu deploy'dan uzun yaşamalıdır.

Compose bunları `/var/lib/openlog/sourcemaps` yoluna mount edilen `source-maps` volume'unda tutar. Eskiden varsayılan olarak container'ın `/tmp` klasöründeydiler
ve her yeniden oluşturmada kayboluyorlardı. Üstelik bir veri export'unun aksine kaybolan map yeniden üretilemez: biri tekrar yükleyene kadar stack'ler minify
edilmiş kalır.

Helm, veri export'larında olduğu gibi yolu bilerek api pod'unun `/tmp` emptyDir'ında bırakır: ReadWriteOnce bir PVC de replica'lar arasında paylaşılamazdı ve bir
pod'a düşen yükleme, başka bir pod'daki sembolleştirmeye görünmez olurdu. Bunun yerine `OPENLOG_SOURCE_MAPS_S3_URL` ile object storage'ı göster. Katmanlı depolama
açıkken bu değer `OPENLOG_S3_ENDPOINT`'ten otomatik türetilir.

Kapladıkları alan `(organization, application, script)` başına sınırlıdır: yeniden yükleme hem satırın hem nesnenin yerine geçer. Bundler dosya adlarını hash'liyorsa
(`app.a1b2c3.js`) zaman içinde sınırlı değildir, çünkü o zaman her derleme yeni bir script'tir.

Bu yüzden api leader'ı, `OPENLOG_SOURCE_MAPS_RETENTION_DAYS` boyunca (varsayılan **90**, `0` sonsuza kadar tutar) kimsenin yüklemediği map'leri, belge ve indeks
satırıyla birlikte, saatlik geçiş başına en fazla 500 tane olacak şekilde siler.

Yaş bir vekil ölçüttür ve bunu söylemeye değer: bir map'i işe yaramaz yapan, ait olduğu derlemeyi artık hiçbir tarayıcının çalıştırmamasıdır ve buradaki hiçbir şey
bunu bilemez. Kuralın doğru yaptığı ise diğer durumdur: adları *hash'lemeyen* bir bundler her deploy'da aynı script'i yeniden yükler, bu da satırı korur ve zaman
damgasını günceller, yani hâlâ gönderilen bir map asla bayat görünmez. Temizliğin ilk yüklemeyi değil son yüklemeyi okumasının nedeni budur.

## Diski ne kullanıyor {#what-is-using-the-disk}

`openlog-admin storage status`, replica başına her disk için boş ve toplam alanı, ayrıca volume başına her tablo için baytları ve en eski ile en yeni partition'ı yazdırır.
Doğrudan ClickHouse'tan hızlı bir cevap için:

```sql
SELECT database, table, formatReadableSize(sum(bytes_on_disk)) AS size
FROM system.parts
WHERE active
GROUP BY database, table
ORDER BY sum(bytes_on_disk) DESC
LIMIT 20;
```
