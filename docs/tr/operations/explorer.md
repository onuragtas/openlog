# Logs, Traces ve Metrics Explorer {#logs-traces-and-metrics-explorer}

**Logs** (`/logs`), **Traces** (`/traces`) ve **Metrics** (`/metrics`) sayfaları, organizasyonun herhangi bir log kaydını, span'ini ve OTLP
metriğini aynı sorgu oluşturucuyla arar (D-118, D-119, D-122). API:
[api.md "Fields"](/contracts/api#fields), "Logs", "Traces", "Metrics", [“Saved views”](/contracts/api#saved-views).

## Zaman aralığı ve otomatik yenileme {#time-range-and-auto-refresh}

Üst çubuktaki zaman aralığı seçici (15 dk … 7 gün veya özel başlangıç/bitiş) zamana yayılan veri gösteren her sayfaya
uygulanır: explorer'lar, APM, host ve container detayı, Kubernetes, sorgu konsolu ve dashboard'lar. Yanında şunlar var:

- **Refresh now** (dairesel ok) mevcut sayfanın verisini hemen yeniden yükler; göreli bir aralıkta ("last 1 hour")
  pencere şimdiki zamana kayar. Veri yüklenirken döner.
- **Auto-refresh** menüsü: *Off*, 5s, 10s, 30s, 1m, 5m veya 15m. Etkin aralık düğmede bir noktayla görünür.
  Telefonlarda ikisi tek bir ikon menüsünde toplanır.

Aralık, `refresh` URL parametresidir (`?range=1h&refresh=30s`), yani kopyalanan bir bağlantı da otomatik yenilenir ve sayfalar
arasında gezerken korunur. Son seçimin tarayıcıda hatırlanır ve bağlantıda `refresh` yoksa kullanılır.
Otomatik yenileme yalnızca göreli aralıklarda çalışır; özel bir başlangıç/bitiş aralığı kaymaz, bu yüzden orada menü devre dışıdır
(Refresh now yine çalışır). Tarayıcı sekmesi gizliyken durur, geri döndüğünde bir aralık geçmişse bir kez yeniler ve önceki yenileme
hâlâ yükleniyorsa o turu atlar. Zaman aralığı seçicisi olmayan sayfalar (Hosts listesi, Inventory, Fleet, Alerts, Settings, trace
detayı) kendi periyodik güncellemelerini sürdürür.

## Sorgu oluşturucu {#query-builder}

Filtre çubuğuna yazarak önce bir **anahtar**, sonra bir **operatör**, sonra bir **değer** seç; öneriler seçili zaman
aralığının verisinden gelir.

| Anahtar | Örnek | Okuduğu yer |
|---|---|---|
| üst düzey alan | `service.name`, `severity_text`, `severity_number`, `host.name`, `trace_id`, `body` | kaydın kendi sütunları |
| `attributes.<key>` | `attributes.http.route` | log kaydının veya data point'in attribute'u |
| `resource.<key>` | `resource.k8s.pod.name` | resource attribute'u (SDK, collector, infra ajanı) |
| `body.<path>` | `body.user.id` | JSON log gövdesinin içindeki bir değer |
| yalın anahtar | `http.route` | kaydın attribute'u, o yoksa resource attribute'u |

Operatörler: `=`, `!=`, `IN`, `NOT IN`, `contains` / `not contains` (büyük/küçük harf duyarsız alt dize; `%` ve `_` sıradan
karakterdir), `like` / `not like` (büyük/küçük harf duyarlı, `%` ve `_` joker karakterdir), `regex` / `not regex` (RE2), `exists` / `not exists` ve sayılar için `>`, `>=`, `<`, `<=`.
`service.name = checkout`, `severity_text IN (ERROR, WARN)` veya `http.status_code >= 500` gibi metinleri doğrudan yapıştırabilir veya yazabilirsin;
geri kalan her şey log gövdesinde arama yapar. **+ OR** yeni bir grup başlatır: bir grubun içindeki koşulların hepsi eşleşmelidir, gruplardan
herhangi biri eşleşebilir. Her filtre, sütun ve zaman aralığı URL'dedir, yani kopyalanan bir bağlantı aynı görünümü açar.

Sınırlar: 50 koşul, 10 OR grubu, `IN` başına 100 değer, değer başına 1024 bayt. Organizasyonun sorgu limitleri
(Settings → Usage) her explorer sorgusuna uygulanır.

## Logs Explorer {#logs-explorer}

- **Histogram**: zaman bucket'ı başına log hacmi, severity'ye veya seçtiğin herhangi bir anahtara göre yığılmış (ilk 10 değer artı "other").
  Zaman aralığını yakınlaştırmak için üzerinde sürükle.
- **Sütunlar**: herhangi bir anahtarı sütun olarak ekle (sütun seçici veya kayıt detaylarındaki "Add as column"), sıralamak için sürükle,
  boyutlandırmak için başlık kenarını sürükle, kaldır; zaman damgası ilk sırada kalır. Sütun kümesi URL'de tutulur ve tarayıcıda
  hatırlanır. Gövde kaydırmasını ve satır yoğunluğunu tablo araç çubuğundan değiştir.
- **Kayıt detayları**: her alan, alan başına işlemlerle birlikte tablo veya JSON olarak görünür: değere göre filtrele (`=`), hariç tut
  (`!=`), sütun olarak ekle/kaldır, kopyala. Trace id'si olan kayıtlar trace'e bağlanır.
- **Hücre menüsü**: her tablo hücresinde (zaman hariç) o değer için aynı işlemlerin olduğu bir menü vardır: filtreye ekle, hariç tut,
  sütunu ekle veya kaldır, kopyala.
- **Top values**: yan panel, mevcut sorgu için (filtreler, OR grupları, gövde araması ve bağlam) seçilen anahtarın en sık 10 değerini
  sayıları ve anahtara sahip kayıtlar içindeki paylarıyla listeler; bir değere tıklayarak ona göre filtrele veya onu hariç tut.
  Seçilen anahtarın kendisine ait koşullar uygulanmaz, böylece diğer değerleri görünür kalır.
- **Saved views**: mevcut filtreleri, sütunları ve zaman aralığını özel veya organizasyon genelinde bir görünüm olarak kaydet (member ve
  üstü; admin'ler başkalarının organizasyon genelindeki görünümlerini değiştirebilir). Saved views PostgreSQL auth modu gerektirir.
- Host, container ve Kubernetes pod'larının **log sekmeleri**, her sorgunun, histogramın ve önerinin içerdiği kilitli bir bağlam koşuluyla
  (`host.id`, `resource.container.id`, `resource.k8s.pod.uid`) explorer'ı gömer;
  "Open in Logs Explorer", koşulu sıradan ve kaldırılabilir bir chip olarak `/logs` üzerinde sürdürür. `lq`, `severity`, `lsrc`, `lfile`, `ldisc`, `lunit` veya `stream` içeren eski
  bağlantılar karşılık gelen chip'lerle açılır.
- **APM bağlantıları**: "logs of this transaction" kaldırılabilir bir *transaction* chip'i gösterir (entry span'i o transaction olan
  trace'lerin logları, eskisi gibi); bir trace'in veya span'in "related logs" bağlantıları `trace_id` / `span_id` chip'lerine dönüşür.

## Traces Explorer {#traces-explorer}

Her span'i arar (yalnızca APM entry span'lerini değil). Üst düzey anahtarlar: `name`, `kind`, `status_code`, `service.name`,
`duration_ms`, `http.status_code`, `error`, `is_entry`, `transaction.name`, `trace_id`, … artı `attributes.<key>` ve
`resource.<key>` (span'lerde `http.status_code`, `db.system` ve `peer.name` attribute'lardan türetilen APM sütunlarını okur;
ham attribute için `attributes.http.status_code` kullan).

- **Root spans only** parent'ı olmayan span'leri tutar (tam trace'lerde trace başına bir tane).
- **Grafikler**: bucket başına span sayısı (isteğe bağlı olarak bir anahtara göre bölünmüş) ve eşleşen span'lerin p50 / p95 / p99 süresi;
  zaman aralığını yakınlaştırmak için sürükle.
- **Tablo**: aynı sütunlar, sıralama, boyutlandırma, hücre menüsü ve "Slowest first" sırası (aralığın en yavaş span'leri,
  tek sayfa). Span detayları için zamana veya satıra tıkla; trace id waterfall'u açar.
- **Saved views** ve URL durumu Logs Explorer'daki gibi çalışır (`signal: traces`).

## Metrics Explorer {#metrics-explorer}

Metrik listesi aralıkta alınan her metriği gösterir (infra ajanından, APM ajanlarından, OpenTelemetry SDK'larından veya bir
collector'dan), tip, birim, temporality, açıklama, seri sayısı ve raporlayan servislerle birlikte. Bir metrik seç, filtre
ekle, bir aggregation ve group-by anahtarları seç:

| Metrik tipi | Aggregation'lar |
|---|---|
| gauge | avg, min, max, sum, last, count |
| monotonic sum (counter) | rate (saniye başına), increase, sum, last |
| non-monotonic sum (up-down counter) | last, avg, min, max, sum, count |
| histogram, exponential histogram | p50, p75, p90, p95, p99 (bucket içinde interpolasyonlu), avg, count, sum, rate |
| summary | p50…p99 (SDK'nın raporladığı quantile'lar), avg, count, sum |

Birden fazla sorgu (A, B, …) `A / B * 100` gibi bir formülle birlikte çizilebilir. **Add to dashboard**, en yakın karşılık gelen
sorguyla bir OQL widget'ı oluşturur; OQL'in ifade edemediği aggregation'lar (örneğin histogram yüzdelikleri) düğmeyi devre dışı
bırakır.

## Nasıl çalışır, maliyeti nedir {#how-it-works-and-what-it-costs}

- **Anahtar önerileri** saatlik `attribute_keys` anahtar indeksini okur (ClickHouse `0080_attribute_keys`): logs, spans ve metrics
  tablolarındaki materialized view'lar tenant, saat ve anahtar başına anahtara sahip kayıtları, kaç değerin sayı veya boolean olduğunu (anahtar
  tipi) ve yaklaşık benzersiz değer sayısını sayar. Attribute değerleri saklanmaz. Bu view'lar her insert'e küçük bir iş yükü ekler
  (eklenen her block'un attribute başına bir satırı, block içinde gruplanmış). Yükseltmeden önce yazılan veri indekslenmez; böyle aralıklarda API son
  saatin en fazla 20000 kaydından örnekler. JSON gövde anahtarları son 15 dakikanın en fazla 2000 kaydından örneklenir.
- **Değer önerileri** ve "top values", aralıktaki en fazla 100000 eşleşen kaydı okur.
- **Log araması**, logs tablosunun sıralama anahtarını (tenant, service, host, time) ve `body` üzerindeki `contains` için gövde token
  indeksini kullanır; attribute filtreleri zaman aralığını tarar, bu yüzden en hızlısı dar aralıklar ve `service.name` filtresidir.
- **6 saatten uzun metrik sorguları**, tüm filtreler ve group-by anahtarları data point attribute'u, `host.id` veya `service.name` ise
  gauge ve sum'lar için 1 dakikalık rollup'ı okur; resource attribute filtreleri ham data point'leri okur (30 gün
  saklama). Histogram ve summary'ler her zaman ham data point'leri okur (en fazla 200000 seri × adım satırı); en fazla 200
  seri döner.

## Sürüm notları (bu sürüm) {#release-notes-this-version}

- Yeni **Traces Explorer** (`/traces`, `POST /api/v1/traces/query`, `POST /api/v1/traces/aggregate`).
- Host, container ve pod log sekmeleri ile APM/trace log bağlantıları Logs Explorer'ı kullanır. `GET /api/v1/logs` API istemcileri için
  değişmedi.
- **`contains` anlamı (D-122).** Explorer'ların `contains` işlemi zaten büyük/küçük harf duyarsızdı. OQL'de artık tam olarak aynı anlama gelen
  `attr [NOT] CONTAINS 'text'` var ve "Add to dashboard" bunu üretir. Daha önce `LIKE '%text%'` ile oluşturulmuş dashboard'lar ve alarm
  kuralları (bu sürümden önce "Add to dashboard" ile üretilen sorgular dahil) **değiştirilmez**: büyük/küçük harf duyarlı kalırlar ve metin içindeki `%` / `_` karakterlerini
  joker olarak değerlendirirler. Explorer davranışını almak için `x LIKE '%text%'` ifadesini `x CONTAINS 'text'` ile değiştir.
- "Add to dashboard", yalın anahtar filtresini (ör. `pool = 'heap'`) yalnızca attribute olarak değil, explorer'daki anlamıyla "attribute,
  o yoksa resource attribute" olarak yazar. Öneksiz group-by anahtarları hâlâ attribute üzerinden facet oluşturur.

## Yükseltmeden sonra metrik tipleri {#metric-types-after-the-upgrade}

Önceki sürümler exponential histogram'ları scale, offset, negatif ve sıfır bucket'ları olmadan saklıyor, summary'lerin
quantile'larını atıyordu. Bu sürümden itibaren processor, exponential histogram bucket'larını açık sınırlar olarak ve
summary quantile'larını `metrics.quantiles` / `metrics.quantile_values` içinde saklar (ClickHouse `0081_metrics_summary_quantiles`) ve
"no recorded value" işaretli data point'leri atar (`no_recorded_value` nedeniyle `openlog_processor_items_dropped_total` içinde sayılır).
Bu yüzden exponential histogram ve summary yüzdelikleri yalnızca yükseltmeden sonra alınan veride vardır; count, sum ve ortalama
eski veride de çalışır.
