# Sürekli profiling {#continuous-profiling}

Sürekli profiling, diğer sinyallerin cevaplayamadığı bir soruyu cevaplar: yavaş span'in *içinde* zamanı hangi fonksiyon harcadı? Bir
trace, `GET /orders` isteğinin 900 ms sürdüğünü ve bunun 40 ms'sinin veritabanında geçtiğini söyler; bir profile, kalan 860 ms'nin
`renderOrders` içinden çağrılan `json.Marshal`'a gittiğini söyler.

openlog profile'ları pprof olarak değil **OTLP profiles** olarak taşır. Sinyal upstream'de hâlâ `v1development` durumunda ve bu bilinçli
bir tercihti: pprof kararlı ama Go'ya özgü modeliyle Go'nun formatıdır. OTLP profiles ise ekosistemin geri kalanının yöneldiği format,
OTel resource attribute'larını doğal olarak taşır (yani bir profile, eşleme tablosu olmadan ait olduğu APM servisinin yanına düşer) ve
kararlı hale gelecek. Bu tercihin maliyeti sınırlı: wire tiplerini bilen tek paket `internal/profiles`.

## 1. Ingest {#_1-ingest}

| | |
|---|---|
| HTTP | `POST /v1/profiles`, `application/x-protobuf` veya `application/json`, isteğe bağlı gzip |
| gRPC | `opentelemetry.proto.collector.profiles.v1development.ProfilesService/Export` |
| Kafka topic'i | `<prefix>.otlp.profiles.v1` ([kafka.md](/tr/contracts/kafka)) |
| Partition anahtarı | sahip olan ilk resource'un `<tenant_id>/<service.name>` değeri |

Profile'lar `internal/ingest` içinde (`profiles.go`) `httpExport`'un bir kolu olarak değil kendi yollarından geçer. Sebep tercih değil
bir tip: OTLP profiles isteği bir pdata wrapper'ıdır ve `newRequest`, `prepare` ile `partialSuccess`'in üzerine kurulu olduğu
`proto.Message`'ı uygulamaz.

Bu yolun farklı **yapmadığı** şey, önemli olan her şeydir: lisans anahtarı çözümleme, SaaS organizasyon kapısı, gövde sınırı, tenant
kotası ve produce aynı sırayla aynı çağrılardır. Profile, diğer sinyallerin geçtiği bir kontrolü atlatmanın yolu asla değildir.

Payload Kafka'ya **byte byte** ulaşır. OTLP/JSON ingest'te bir kez protobuf'a çevrilir (processor encoding'i tahmin etmek zorunda
kalmasın diye), ama protobuf payload tam geldiği gibi üretilir: kararsız bir wire formatını yeniden encode etmek, byte'ların ne anlama
geldiğine spesifikasyon yerine openlog'un karar vermesi demek olurdu.

Hiç profile taşımayan bir payload kabul edilir ve hiçbir şey üretmez: süreci boşta olan bir ajanın raporlayacak bir şeyi yoktur ve hata
dönmek onu sonsuza kadar yeniden denetirdi.

**Üreticiler.** İki ajan profile üretir ve varsayılanlarının farklı olmasının yazmaya değer bir sebebi var. Tüm makine profilleyicisi üçüncüsüdür ve kendi kontratı vardır ([ebpf-profiler.md](/tr/contracts/ebpf-profiler)).

**Go ajanı** (`agents/go`) `OPENLOG_PROFILE_INTERVAL` boyunca sürecin CPU profile'ını alır ve buraya gönderir, **varsayılan olarak
açıktır**: kimsenin profile etmediği bir servis, yavaş span'i cevapsız kalan bir servistir ve sampling maliyeti tek çekirdeğin
yüzde birkaçıdır.

`OPENLOG_PROFILING=false` onu kapatır ve bu anahtar süs değil: Go süreç başına bir CPU profile'ına izin verir, yani bu çalışırken
`net/http/pprof`'un kendi `/debug/pprof/profile` adresi "cpu profiling already in use" cevabını verir. O adrese ihtiyacı olan bir süreç
profiling'i kapatır; anahtar olmasaydı tek çıkış yolu SDK'yı bırakmak olurdu.

HTTP protokolü gerekir, çünkü yayınlanmış OTLP profiles modülleri gRPC servis stub'ları olmadan yalnızca mesaj tiplerini içerir, yani
bağlanılacak üretilmiş bir profiles istemcisi yok. Go runtime'ının pprof çıktısından dönüşüm ajanda yapılır
(`agents/go/internal/otlpprofiles`): pprof aynı örnekler üzerinde iki sample tipi tanımlar (`samples/count` ve `cpu/nanoseconds`), bir OTLP
profile ise tek tip tanımlar. Bu yüzden her biri kendi profile'ı olur ve "tek profile, tek birim" kuralı doğru kalır.

**Node.js ajanı** (`agents/node`) aynısını inspector protokolü üzerinden V8'in sampling profiler'ıyla yapar, dönüşümü
`agents/node/src/profiles/` içinde gerçekleştirir ve **varsayılan olarak kapalıdır**: `OPENLOG_PROFILING=true` açar. Node'da süreç başına
tek profile kilidi yok, yani Go ajanının anahtar ihtiyacının sebebi burada geçerli değil, ama varsayılanın sebebi tersine dönmüş halde
geçerli. Bu özellik zaten kurulumları olan bir ajana geldi ve bir güncelleme, operatörün sakladığı ve ödediği şeyi sessizce katlamamalı.
Para tutan bir sinyal, biri istediğinde başlar.

## 2. Depolama {#_2-storage}

`schema/clickhouse/0095_profiles.sql`. Örnek başına bir satır: çözülmüş çağrı yığını ve üzerinde ölçülen değer.

| Sütun | Notlar |
|---|---|
| `stack` | `Array(LowCardinality(String))`, **önce kök**, flame graph'ın okuduğu gibi |
| `leaf` | en içteki frame, türetilmek yerine saklanır |
| `profile_type`, `unit` | profile'ın kendi `ValueType` değerinden (`cpu`/`nanoseconds`, `alloc_space`/`bytes`) |
| `value` | örneğin `unit` cinsinden ölçümü |
| `duration_ns` | profile'ın kapsadığı duvar saati süresi, yani bir oran için ikinci sorgu gerekmez |

**Stack neden wire'daki indeksler olarak saklanmak yerine açılıyor?** OTLP profiles bir sözlük artı indekslerdir (sample → stack →
location → line → function → string). Bu, payload için doğru, column store için yanlış şekildir: böyle tutmak her flame graph'ı beş
join yapardı. Sözlük küçüktür, bu yüzden açma processor'da bir kez yapılır (`internal/profiles.FromOTLP`) ve flame graph
`SELECT stack, sum(value) GROUP BY stack` haline gelir.

`leaf` hesaplanmak yerine saklanır, çünkü "fonksiyona göre self time" bir profile'a sorulan ilk sorudur ve `GROUP BY` içindeki
`arrayElement(stack, -1)` index'i boşa çıkarırdı.

`cityHash64(tenant_id, service_name)` ile shard'lanır: flame graph her zaman tek pencerede tek servistir, yani servisin örnekleri tek
shard'da kalır ve okuma tek shard'dan yapılır. Host'a göre shard'lamak her servisi cluster'a yayar ve her flame graph'ı bir fan-out yapardı.

**Sınırlar** (`internal/profiles`): stack başına `MaxFrames = 128` (daha derin stack'ler zamanın geçtiği *en içteki* frame'lerini
tutar), frame adı başına `MaxFrameBytes = 512` (atılmaz, kırpılır, yani frame yine gruplanır), attribute başına `MaxAttrBytes = 1024`,
payload başına `MaxRowsPerRequest = 200 000`.

**Parça parça değil, tümüyle atılır.** Açılamayan bir payload (sample tipi olmayan bir profile, satır sınırını aşan bir istek) tümüyle
reddedilir. Flame graph'ın gösterdiği yüzdeler elindeki örneklerin yüzdeleridir, yani yarım profile daha küçük bir doğru değil, yanlış bir doğrudur.

## 3. Retention {#_3-retention}

Profile'lar openlog'un sakladığı en geniş satırlardır: tek süreçten bir dakikalık CPU örneği binlerce stack eder. Şema TTL'i **7 gündür**
ve `traces` sınıfını değil kendi depolama sınıflarını (`profiles`) taşırlar, çünkü tenant başına retention (D-081) bir sınıfı bütün olarak
genişletir: trace retention'ını uzatan bir tenant, profiling faturasını sessizce katlamamalı. Tenant başına retention, `profiles_local`
için de diğer ham tablolar gibi geçerlidir ([usage.md](/tr/contracts/usage)).

## 4. Ölçümleme {#_4-metering}

`usage_signals_1h`, `profiles_local` üzerindeki bir materialized view ile (`0097_usage_profiles.sql`) saklanan örnekleri ve tahmini
sıkıştırılmamış byte'ları sayar; `usage_ingest_1h`, processor'ın tükettiği OTLP byte'larını sayar. Yani faturalama saklanan satırları diğer her
sinyal gibi sayar: ingest'in kabul edip processor'ın attığı payload faturalanmaz. Yalnızca profile gönderen bir servis de aktif servis
sayılır, yani entity sayımlarında görünmez kalmaz.

## 5. Okumalar {#_5-reads}

Tüm okumalar tenant kapsamlı sorgu katmanından geçer; parametreler ve yanıt gövdeleri için [api.md](/tr/contracts/api)'ye bak.

| Endpoint | Cevapladığı |
|---|---|
| `GET /api/v1/profiles/services` | pencerede neyin profile edildiği ve **her servisin hangi profile tiplerine sahip olduğu** |
| `GET /api/v1/profiles/flame` | tek servis ve tip için flame graph ağacı |
| `GET /api/v1/profiles/functions` | self time'a göre sıralı fonksiyonlar |

Değerlerin toplandığı her yerde **profile tipi zorunludur** ve bilerek `cpu` varsayılanı verilmez. Nanosaniye ve byte toplanmaz: tipi
belirtilmemiş bir grafik, birimi olmayan bir sayı olurdu. Hangi tiplerin var olduğunu çağırana services endpoint'i söyler ve her yanıt
profile'ın kendi `ValueType` değerinin bildirdiği `unit`'i tekrarlar, yani bir grafiğin "ns"yi koda gömmesi gerekmez.

Aynı stack'ler Go'da değil ClickHouse'da katlanır; `stack`'in sütun olmasının tüm sebebi bu. Flame graph, değere göre sıralı 20 000 farklı
stack ile sınırlıdır, yani sondan düşenler en ince şeritlerdir ve flame graph onlara zaten bir piksel bile çizemezdi.
