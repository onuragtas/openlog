# Sözleşme: Ürün analitiği (D-150) {#contract-product-analytics-d-150}

**Durum: tamamlandı.** Uç noktalar için §6'ya bak.

Uygulamayı kim kullanıyor, nereden geldiler, ne yapıyorlar ve geri dönüyorlar mı: web ve mobil uygulamalardan, 7 günlük trace retention'ından
daha uzun süre boyunca.

**Analitik de paralel bir yığın değildir.** SDK'lar [rum.md](/tr/contracts/rum)'nin OTLP span'lerini göndermeye devam eder; ikinci bir endpoint
ve ikinci bir SDK yok. Yeni olan kalıcı bir kimlik (§1), giriş sayfası görüntülemesinde biraz daha fazla kaynak bilgisi (§3) ve span'lerden uzun
yaşayan dört tablo (§5).

**Bilerek burada değil:** push mesajlaşma, remote configuration, A/B testi, session replay, bölge veya şehir.

## 1. Kimlik {#_1-identity}

### 1.1 Varsayılan olarak açık, Google Analytics ve Firebase gibi {#_1-1-on-by-default-like-google-analytics-and-firebase}

DAU, MAU, yeni kullanıcılar ve retention, birinin ziyaretler arasında tanınmasını gerektirir, bu yüzden **yeni anahtar ve yeni SDK için toplama
varsayılan olarak açıktır**, Google Analytics ve Firebase Analytics'te olduğu gibi.

**Bu, o uygulamalar için `rum.md` §1.1'deki gerekçeden vazgeçmek demektir.** RUM'u izin banner'ı olmadan toplanabilir yapan şey, oturumun bir kişi
değil bir ziyaret olmasıydı. Kalıcı bir ziyaretçi id'siyle bu artık geçerli değil ve izne ihtiyacı olan uygulamanın izin istemesi gerekir.
**İhtiyacı olup olmadığı uygulamanın sorusu olarak kalır**, rum.md §3.7'deki `identify()` gibi, ve cevaplamak için gerekene sahip: aşağıdaki
`analyticsCollection`'a bak.

| Anahtar | Kim | Varsayılan | Neden |
|---|---|---|---|
| `browser_keys.analytics_mode` | operatör | **yeni** anahtarda `visitors`; bu özellikten önce var olan anahtarda `off` | Bir güncelleme, sahibi hiç karar vermemiş bir uygulama için kişisel veri toplamaya başlamamalı |
| `init` sırasında `analyticsCollection` | uygulama | `true` | `false`, önce sorması gereken uygulama için ziyaretçi id'si olmadan başlar |
| `setAnalyticsCollection(enabled)` | uygulama, çalışma anında | | Firebase'in `setAnalyticsCollectionEnabled`'ının karşılığı, izin diyaloğundan çağrılır |

| `analytics_mode` | Saklanan | Cevapladığı |
|---|---|---|
| `off` | yeni bir şey yok; bugünkü davranış | RUM olduğu gibi |
| `sessions` | yalnızca §5.1, actor olmadan | event hacmi, kaynak bilgisi ve 7 günün ötesinde oturum funnel'ları |
| `visitors` | §5'in tümü | DAU, WAU, MAU, yeni ve geri dönen, retention, kullanıcı funnel'ları |

Uygulamada toplama kapalıyken, `visitors` modundaki anahtar o ziyaretçi için `sessions` gibi davranır.

### 1.2 Ziyaretçi id'si {#_1-2-the-visitor-id}

`openlog.rum.visitor.id`: 32 rastgele hex karakter, asla cihazdan veya kişiden türetilmez.

- **Web:** `localStorage`, bir origin'in sekmeleri arasında paylaşılır. Cookie yok: cookie uygulamanın kendi sunucularına gider ve burada hiçbir
  şeyin buna ihtiyacı yok. Depolama kullanılamıyorsa ziyaretçi id'si olmadan devam edilir.
- **Mobil:** Apple platformlarında `UserDefaults`; Kotlin ve Dart'ta uygulamanın verdiği `visitorDirectory` içinde bir dosya, çünkü bu SDK'ların
  dizin bulacak platform API'si yok. **Depolama yoksa id gönderilmez**; her açılışta yenisini göndermek her açılışı yeni kişi sayardı. `device.id`
  reddedilmeye devam eder (mobile-agent.md §3.2).
- **Ömür:** oluşturulduktan 13 ay sonra yenilenir.
- `setAnalyticsCollection(false)` saklanan id'yi siler ve göndermeyi hemen durdurur. Yeniden açmak yeni bir id oluşturur, yani vazgeçmiş bir ziyaretçi
  döndüğünde tanınmaz.

Sunucu, **anahtar `visitors` modunda değilse attribute'u atar**, yani sırf gönderildi diye gelemez.
`session.id` gibi doğrulanır: 32 hex karakter dışındaki her şey kaldırılır, span tutulur.

**Id cihaza ancak sunucu söyledikten sonra ulaşır.** `GET /v1/rum/config` `analytics_mode` döner. `visitors` cevabı gelene kadar id bellekte yaşar,
yani operatörün analitiği kapalı bıraktığı bir anahtar, ziyaretçinin tarayıcısında hiçbir şey bırakmaz ve daha önce saklanmış id, cevap değiştiğinde silinir.
Cevaptan önce gönderilen sayfa görüntülemesi yine bellekteki id'yi taşır, yani yeni ziyaretçinin ilk sayfa görüntülemesi kaybolmaz.

**Vazgeçme hatırlanır.** `setAnalyticsCollection(false)` bu gerçeği saklar, çünkü aksi halde sonraki yükleme, az önce hayır demiş birine yeni id verirdi.
Bunu yalnızca açıkça çağrılan `setAnalyticsCollection(true)` geri alır, `analyticsCollection`'ın varsayılanı asla.

### 1.3 Actor {#_1-3-the-actor}

Tablolar bir **actor** sayar: varsa ziyaretçi id'si, yoksa `user.id`.

**Ziyaretçi id'si varsa bir cihaz bir actor'dür**, oturum açtıktan sonra bile, yani telefonu ve laptop'u olan bir kişi iki kez sayılır. Bu v1'in
belirtilen sınırıdır. Kimlikleri geriye dönük birleştirmek çok daha büyük bir tasarımdır ve yanlış birleştirme çift sayımdan kötüdür.

Uzun ömürlü tablolara yalnızca hash ulaşır (§5):

```
actor_hash = sipHash64(tenant_id, actor)
```

Bu **anonimleştirme değil, takma adlandırmadır**; tablolar kişisel veri olarak kalır ve §7 onlara uygulanır.

## 2. Ziyaretçi varken sampling değişir {#_2-sampling-changes-when-there-is-a-visitor}

Bugün bir oturum bağımsız olarak örneklenir (rum.md §1.1). Retention için bu yanlıştır: %10 oranda, sadık bir kullanıcı on ziyaretten dokuzunda
kaybolmuş gibi görünürdü. Bu yüzden ziyaretçi id'si varsa:

```
sampled = fnv1a32(visitor_id) / 2^32 < sample_rate
```

Sampling **ziyaretçi başına bir kez** kararlaştırılır. Tüm istemciler aynı fonksiyonu uygulamalı, burada belirtilmesinin sebebi de bu: id'nin ASCII
byte'ları üzerinde FNV-1a; `fedcba9876543210fedcba9876543210` hash'i `777805509` olur.
Ziyaretçi id'si yoksa kural oturum başına bir kez olarak kalır.

Bir anahtarın oranını kohort ortasında düşürmek, o andan itibaren yine bazı ziyaretçileri düşürür; retention yanıtı bunu gizlemek yerine oranın
aralığında değiştiğini bildirir (§6).

## 3. Kaynak bilgisi {#_3-attribution}

Tarayıcı SDK'sı tarafından **yalnızca oturumun ilk sayfa görüntülemesinde** gönderilir:

| Attribute | Değer |
|---|---|
| `openlog.rum.referrer.host` | `document.referrer` host'u, doğrudan veya aynı site için boş. **Yalnızca host**: path'ler kişisel veri taşır |
| `openlog.rum.utm.source`, `.medium`, `.campaign` | Giriş URL'inden, her biri en fazla 128 byte |

SDK'nın okuduğu tek query parametreleri bunlardır. `url.full` query string'i atmaya devam eder (rum.md §2.4).
`utm_term` ve `utm_content` toplanmaz: serbest metnin düştüğü ikisi onlardır.

Mobil v1 bunların hiçbirini göndermez. Kurulum kaynağı mağazalara bağlıdır ve ayrı bir tasarımdır.

## 4. Sanitize kuralları {#_4-sanitize-rules}

Hepsi `internal/rum.Sanitize` içinde, rum.md §3.3'ü izleyerek:

| Kural | Neden |
|---|---|
| `openlog.analytics.mode` her span'de anahtardan **zorlanır** | §5'in view'leri buna göre filtreler, yani bir sayfa kendini dahil edemez |
| Ziyaretçi id'si §1.2'deki gibi işlenir | Kimlik, anahtarın kararıdır |
| Referrer ve UTM yalnızca `page_view`'de, sınırlı olarak kabul edilir | Serbest bir attribute deposu değil |
| **Anahtar başına en fazla 500 farklı event adı; ötesinde ad `(other)` olur** | Aşağıya bak |

**Event adı sınırı yeni ve gerekli.** `rum.md` §2.5 adları sınırsız bırakabiliyordu, çünkü rollup'ları yoktu. Artık public bir anahtardan 90 günlük
bir tabloya ulaşıyorlar, yani kopyalanmış bir anahtar aksi halde sınırsız ad üretebilirdi ("a page cannot mint rollup keys", §3.3). Analitiği açık
anahtarlara uygulanır. Küme, rate limit gibi ingest pod'u başına tutulur, yani *p* pod ile tavan en fazla *p* × 500'dür; bu gizlenmez, açıkça belirtilir. Sıfırın
üstündeki bir `(other)` sayısı arayüzde gösterilir.

## 5. Depolama {#_5-storage}

ClickHouse `0102_analytics.sql`, PostgreSQL `0104_analytics_mode.sql`. Üç tablonun hepsi `spans_local` üzerindeki, zorlanan mod attribute'una göre
filtrelenen materialized view'ler tarafından yazılır, yani `off` modundaki anahtarın maliyeti sıfırdır.

**View'ler yeni oluşturulmalı.** `rum_sessions` değiştirilemez (rum.md §3.7) ve burada hiçbir şey ona dokunmaz.

### 5.1 `analytics_events`: 90 gün {#_5-1-analytics-events-—-90-days}

`page_view` veya `custom` span'i başına ağırlığıyla bir satır:

`tenant_id, app, deployment_environment, timestamp, kind, name, route, session_id, actor_hash, weight, version,
country, device_type, os_name, browser_name, referrer_host, utm_source, utm_medium, utm_campaign, params`

`name` event adıdır veya sayfa görüntülemesi için route'tur. `params`, mevcut `openlog.rum.custom.param.*` değerlerini tutan
`Map(LowCardinality(String), String)`'dir. Güne göre partition'lanır, `ORDER BY (tenant_id, app, deployment_environment,
timestamp, name)`, `actor_hash` üzerinde bloom filter ile.

### 5.2 `analytics_actors`: son görülmeden 13 ay sonrasına kadar {#_5-2-analytics-actors-—-13-months-after-last-seen}

Actor başına bir satır: `first_seen` (min), `last_seen` (max), `first_version`, `first_country`, `first_referrer_host`,
`first_utm_source`. "Yeni"yi gerçekten yeni yapan budur: `first_seen` §5.1'in 90 gününden uzun yaşamalı, yoksa üç ay sonra dönen herkes yeni kullanıcı sayılırdı.

### 5.3 `analytics_actor_days`: 13 ay {#_5-3-analytics-actor-days-—-13-months}

`tenant_id, app, deployment_environment, day, actor_hash, weight (max), events (sum)`. Retention bunu okur, event'leri asla; yani 12 aylık bir kohort
grafiği 12 aylık event taramaz.

### 5.4 `analytics_device_models_1d`: 90 gün {#_5-4-analytics-device-models-1d-—-90-days}

`tenant_id, app, deployment_environment, day, model, sessions, actors, weight`, resource'un `device.model.identifier` değerinden, yani yalnızca mobil SDK'lar.
§5.1'de model yok ve view'i değiştirilemez. `sessions` ve `actors` `uniqCombined64` sketch'leridir ve kişisel veri tutmaz, yani silmenin
(§7) burada bulacağı bir şey yok. Birkaç binin üstünde tahmindirler.

### 5.5 Retention sınıfı {#_5-5-retention-class}

`internal/migrate.TTLTables` içinde **sabit** retention'lı yeni bir `analytics` depolama sınıfı, `rum`'un kendi sınıfına sahip olmasıyla aynı sebepten
(rum.md §5): başka bir sınıfı ödünç almak, tenant başına bir ayarın kişisel verinin ne kadar tutulduğunu sessizce uzatmasına izin verirdi.
`OPENLOG_STORAGE_COLD_AFTER_DAYS_ANALYTICS` varsayılan olarak 30'dur.

Bir actor'ün satırları shard'lara yayılır, çünkü view'ler yerel yazar ve span'ler trace'e göre shard'lanır.
Bu yüzden okumalar Distributed tablo üzerinden yeniden toplar (apm.md §8). Retention tablolar arası join gerektirmez: bir
actor'ün kohortu §5.3'teki ilk satırıdır ve o tablo aynı 13 ayı tutar.

**2 milyon actor ve 10 milyon actor-gün ile ölçüldü:** retention 0,16 sn'de cevap verir. Aktivite okuması önce her actor-günü 30 güne yayıyordu; bu
birebirdi ve belleği tüketti, bu yüzden artık gün başına bir `uniqCombined64` sketch birleştiriyor.
Yani birkaç bin kişinin üstünde DAU, WAU ve MAU **yaklaşık %1 içinde tahmindir**, altında birebirdir.

## 6. API {#_6-api}

Taban `/api/v1/analytics`, tenant kapsamlı, viewer rolü, API anahtarlarına izin var. Hepsi `app=&environment=&from=&to=` alır.
OQL `AnalyticsEvent` event tipini kazanır, bu da aşağıdaki her soruyu `oql` kural tipiyle alert'lenebilir yapar.

| Endpoint | İçerik | Gerekli |
|---|---|---|
| `GET /overview` | oturumlar, event'ler, kullanıcılar, yeni kullanıcılar, seri | kullanıcılar: `visitors` |
| `GET /events` | sayı, kullanıcı ve trend ile event adları; `?name=` tek bir parametreye göre döküm ekler | |
| `GET /acquisition` | referrer host, UTM source, medium, campaign'e göre oturumlar ve kullanıcılar | |
| `GET /audience?by=country\|device\|model\|os\|browser\|version` | ne üzerinde ve nerede çalıştıklarına göre oturumlar ve kullanıcılar; bir oturum ilk event'i altında bir kez sayılır | |
| `GET /compare?days=` | bir dönem ve öncekinin her sayısı, değişimle (§6.1) | kişiler: `visitors` |
| `GET /retention?grain=day\|week\|month` | `first_seen`'e göre kohortlar, dönem başına geri dönen yüzdesi | `visitors` |
| `GET /funnel?step=&step=&window=&by=session\|user` | rum.md §2.8'deki gibi, §5.1 üzerinde, 90 güne kadar | `by=user`: `visitors` |
| `GET /activity` | gün başına DAU, WAU, MAU, yeni kullanıcılar ve stickiness | `visitors` |

### 6.1 Önceki döneme karşı {#_6-1-against-the-previous-period}

`GET /compare?days=` her sayıyı bir dönem ve aynı uzunluktaki öncesi için `change` ve `change_rate` ile döner. `/activity`, `/events`, `/acquisition` ve `/audience` üzerindeki `days` aynı dönemi okur
ve üç liste bu durumda `previous_*` da taşır.

- **Dünle biten tam günler, UTC.** Bugün sabah daha yeni başlamıştır, yani onu saymak her sabah her sayıyı düşüyormuş gibi gösterirdi. Google Analytics de varsayılan aralıklarının dışında bugünü aynı sebeple bırakır.
- **Bilinemediği yerde `previous` null'dır.** `analytics_events` 90 gün tutulur, yani `days` 45'in üzerindeyken oturumlar, sayfa görüntülemeleri ve event'lerin önceki sayısı yoktur; sıfıra düşüş gösterilmez. Kişiler 13 ay tutulur ve her zaman önceki sayıları vardır.
- **`previous` 0 ise `change_rate` null'dır,** çünkü yokluktan yükselişin yüzdesi olmaz. Ekran "new" der.
- **`previous_partial`**, uygulamanın önceki dönemin başından itibaren kayıtlı kişisi olmadığını söyler, yani yükseliş yalnızca SDK'nın o zamandan beri kurulmuş olması anlamına gelebilir. Ekranlar bunu sayıların yanında söyler.
- **Yalnızca önceki dönemde olan bir satır sıfırlarla eklenir,** çünkü tamamen duran şey olabilecek en büyük düşüştür. Listeler dönem başına 200 satırda kesilir, yani bunun ötesinde bir satır yalnızca ilk 200'den çıkmışken yeni veya gitmiş gibi okunabilir.
- DAU, WAU ve MAU, her dönemin son gününde oldukları haliyle karşılaştırılır. Stickiness oran olarak karşılaştırılır, yani `change` değerini puan olarak oku.
- Birkaç bin kişinin üstünde sayılar yaklaşık %1 içinde tahmindir, yani bu boyuttaki küçük bir değişim gürültü olabilir.

`visitors` gerektiren bir endpoint, aralıkta actor'ü olmayan uygulamada `collecting: false` cevabı verir, böylece arayüz neyin açılacağını söyleyebilir: boş bir
retention grafiği "kimse geri gelmedi" diye okunur. Veriden karar verilir, çünkü buradaki okuma yalnızca ClickHouse'a bağlıdır. Anahtarın oranı aralıkta değiştiyse retention `sampling_changed` içerir.

**Durum:** bunların hepsi yapıldı.

`/rum/funnel` olduğu gibi kalır. Arayüz uygulama başına bir **Analytics** bölümü ekler; RUM sekmelerinin yerini almaz.

## 7. Silme {#_7-erasure}

`{"app", "visitor_id" | "user_id"}` ile `POST /api/v1/analytics/erase`, admin rolü (API anahtarlarına izin var, çünkü çağıran genelde bir backend'dir), audit log'a id olmadan yazılır.
`202 {"id", "status": "pending", …}` cevabı verir ve `GET /api/v1/analytics/erase/{id}` durumu bildirir:

1. İstek PostgreSQL'de kuyruğa alınır, çünkü API ClickHouse'u salt okunur kullanıcıyla okur.
2. Bir leader görevi id'yi §1.3'teki gibi hash'ler ve o uygulamayla sınırlı `ALTER TABLE … DELETE` ile §5 tablolarından siler. İfade yalnızca
   hash'i taşır, çünkü metni `system.mutations` ve query log'da kalır.
3. İstek tamamlanınca kuyruktaki id temizlenir.
4. `spans` üzerindeki ham id, 7 günlük trace retention'ıyla süresi dolup gider ve bu yanıtta belirtilir.

Silmeler mutation'dır, yani **asenkrondur**: `submitted`, ClickHouse'a teslim edildi demektir ve yanıt bunu söyler. Bir uygulamanın ziyaretçilerinin veri sahibi talepleri,
bugün hesapları ve organizasyonları kapsayan [saas.md](/tr/operations/saas) §12 için yeni bir durumdur ve o doküman bir bölüm kazanır.

**Önemli bir sınır:** `setAnalyticsCollection(false)` toplamayı durdurur ama hiçbir şey silmez, çünkü public bir anahtar üzerinde bir istemciye silme yetkisi güvenilemez.
Uygulamanın backend'i yukarıdaki endpoint'i çağırmak zorundadır.

## 8. Mevcut sözleşmelerdeki değişiklikler {#_8-changes-to-existing-contracts}

- **rum.md §1.1:** buradaki §1 ve §2'ye işaret eder.
- **rum.md §2.5:** "there is no rollup" `off` için doğru kalır, ad sınırının olmaması da öyle: `off` modundaki anahtar bu özellik yokmuş gibi davranır.
- **rum.md §2.8:** retention'ın burada olmadığını söyleyen paragraf bu dokümana bir linkle değiştirilir.
- **rum.md §3.3 ve semantic-conventions.md §10:** yeni attribute'lar.
- **mobile-agent.md §3.1:** ziyaretçi id'si. **§4:** ziyaretçi düzeyinde sampling.

## 9. Açık sorular {#_9-open-questions}

1. **`first_seen` geriye dönük doldurulur mu?** Öneri: hayır. Bir anahtarın açıldığı gün herkes yeni kullanıcıdır ve arayüz o tarihi işaretler, çünkü geriye dönük
   doldurma için elde olan span'ler zaten yalnızca 7 gün geriye gider.

## 10. Karara bağlananlar {#_10-decided}

- **Faturalama.** §5 satırları ücretsizdir: zaten span olarak sayılan verinin kopyasıdırlar ve ölçülen bir sinyal değildirler.
- **Retention.** §5.1 için 90 gün, §5.2 ve §5.3 için 13 ay, §5.5'in anlattığı gibi sabit.
- **Kimlik.** Yeni anahtarlar ve yeni SDK'lar için varsayılan açık, mevcut anahtarlarda operatör açana kadar kapalı (§1.1).
