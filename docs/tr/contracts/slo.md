# Sözleşme: Service level objective'ler (D-125) {#contract-service-level-objectives-d-125}

Bir **SLO**, tek bir APM servisinin ne kadar güvenilir olması gerektiğini söyler: bir SLI (availability veya latency), bir hedef
yüzde ve kayan bir pencere. Bundan **error budget** (hedefin hâlâ izin verdiği kötü istek sayısı) ve **burn rate** (bu bütçenin ne kadar
hızlı harcandığı) çıkar. `slo_burn` alert kuralı bunları çok pencereli, çok burn rate'li koşullarla izler.

Bu dosya PostgreSQL tablosu `slos` (`migrations/postgres/0087_slo.sql`,
[postgres.md](/tr/contracts/postgres#slos-0087-slo)), bütçe hesabı (`internal/slo`), API (`/api/v1/slos/*`,
[api.md](/tr/contracts/api#service-level-objectives)) ve alert kuralı tipi `slo_burn`
([alerting.md](/tr/contracts/alerting) §2.11) için bağlayıcıdır. Her şey APM rollup'ı `apm_transactions_1m` üzerinden **sorgu anında**
hesaplanır ([apm.md](/tr/contracts/apm) §4, §8); önceden hiçbir şey hesaplanmaz ve yeni bir telemetri tablosuna yazılmaz.

## 1. Nesneler {#_1-objects}

| Alan | Tip | Notlar |
|---|---|---|
| `name` | string | 1–200 karakter |
| `description` | string | ≤ 2000 karakter |
| `service_name` | string | zorunlu, birebir `service.name` (apm.md §1) |
| `service_namespace`, `environment` | string \| null | `null` = tüm namespace/environment'lar (toplu), string (`""` de dahil) = birebir eşleşme, APM alert koşullarındaki gibi |
| `sli_type` | enum | `availability` · `latency` |
| `latency_threshold_ms` | integer | yalnızca `latency`: 1–600000, orada zorunlu, `availability` için reddedilir |
| `objective` | number | yüzde olarak hedef, `50 ≤ objective < 100` (örneğin `99.9`) |
| `window_days` | enum | kayan pencere: `7`, `28` veya `30` gün |

Audit metadata'sı: `created_by`/`created_at` ve `updated_by`/`updated_at` (API e-posta adreslerini döner). Organizasyon başına
en fazla **200 SLO** (`409 failed_precondition`). Doğrulama hataları alan yoluyla birlikte
`400 invalid_argument` olur (`objective: must be at least 50 and below 100 (percent)`).
Her yazma audit log'a girer (`slo.create`, `slo.update`, `slo.delete`, hedef tipi `slo`) ve değişiklikle aynı transaction'da yazılır.

Aynı servisi birden fazla SLO hedefleyebilir (örneğin availability ve latency veya environment başına bir tane).
SLO yalnızca bir tanımdır: tek başına hiçbir şey tetiklemez, `slo_burn` kuralının girdisidir (§3).

## 2. Bütçe hesabı (`internal/slo`) {#_2-budget-math-internal-slo}

Tüm sayımlar `apm_transactions_1m` tablosundan gelir ve sampling olasılığıyla **ağırlıklandırılır** (apm.md §4):
`requests` entry span'lerin Σ ağırlığıdır, `errors` başarısız olanların Σ ağırlığı. Okumalar her zaman `GROUP BY` ile yeniden
toplar (sharding doğruluğu, apm.md §8) ve tamamlanmamış geçerli dakika okunmaz: aralıklar son tamamlanmış dakikada biter.

| Büyüklük | Tanım |
|---|---|
| `good` (availability) | `requests − errors`, `[0, requests]` aralığına sıkıştırılmış |
| `good` (latency) | süresi ≤ `latency_threshold_ms` olan isteklerin ağırlığı, saklanan `duration_hist` bucket'larından (apm.md §4.1, `apm.HistCountLE`), **asla** `avg_ms`'den değil; hatalar çıkarılmaz (hızlı bir hata, istek sayısında olduğu gibi latency SLI için de iyidir) |
| `bad` | `requests − good` |
| `sli` | `good / requests` (istek yoksa değer yok) |
| `budget_requests` | `(1 − objective) × requests`: hedefin aralıkta izin verdiği kötü istekler |
| `budget_consumed` | `bad`; `budget_remaining` = `budget_requests − bad` (hedef kaçırılınca negatif) |
| `remaining_ratio` | `1 − bad / budget_requests` (istek yoksa değer yok); 1 = dokunulmamış, 0 = tükenmiş, < 0 = kaçırılmış |
| `burn_rate` | `(bad / requests) / (1 − objective)`: 1, bütçeyi tam pencere boyunca harcar; 14,4, 30 günlük bütçenin %2'sini bir saatte harcar |
| `met` | `sli ≥ objective` (isteksiz aralık tutturulmuş sayılır: hiçbir şey başarısız olmadı) |

**Eşik hassasiyeti.** Latency SLI, tamamen eşiğin altında kalan histogram bucket'larını ve eşiği içeren bucket'ın doğrusal bir payını
sayar. Yani sayılan ağırlık, span başına birebir sayımdan en fazla bir bucket genişliği (≤ %9,1, apm.md §4.1) uzaktadır ve span'lerin
nasıl örneklendiğinden bağımsızdır.

**Bucket'lama birebirdir.** Komşu bucket'ların toplamları ve `sumMap` histogramları kayıpsız birleşir, yani bir aralığın bütçesi ister tek
bucket ister 43 200 dakika olarak okunsun aynıdır. Bu yüzden okumalar adımı hesaptan değil aralıktan seçer (API'nin olağan `step` kuralları).

**Seri (burndown).** Arayüz serisinde adım başına bir nokta vardır: bucket'ın kendi `requests`, `good`, `bad`, `sli` ve `burn_rate` değerleri,
artı `remaining_ratio`, yani **pencerenin** bütçesinden o bucket'tan sonra kalan pay (tüm pencerenin bütçesine karşı kümülatif kötü
istekler). Bu yüzden son nokta pencerenin `remaining_ratio` değerine eşittir ve boş bucket'lar çizgiyi düz tutar.

**Pencere kenarları.** `[end − w, end)` penceresi tam olarak tamamen içinde kalan bucket'ları içerir; son hariçtir. Burn pencereleri
1 dakikalık bucket kullanır, yani tam dakika sayısı olan her pencere birebirdir.

**Retention.** Rollup `OPENLOG_APM_RETENTION_DAYS` kadar tutulur (varsayılan 30, apm.md §8). 30 günlük pencere tam bu sınırdadır:
pencerenin en eski dakikaları süresi dolup silinmiş olabilir, bu da bütçeyi ayın tamamının değil görünen verinin bütçesi yapar.

## 3. Çok pencereli burn rate alerting (`slo_burn`) {#_3-multi-window-burn-rate-alerting-slo-burn}

`slo_burn` kural tipi ([alerting.md](/tr/contracts/alerting) §2.11), bir SLO'yu çok pencereli, çok burn rate'li yöntemle (Google SRE workbook)
değerlendirir: bir pencere çifti, bütçe **uzun** penceresinde en az `factor` kat fazla hızlı yanıyorsa *ve* son **kısa** penceresinde de
yanmaya devam ediyorsa alert verir. Böylece çoktan durmuş bir patlama tetiklemez ve çözülen bir olay çabuk kapanır.

Varsayılanlar (kural başına ayarlanabilir, 1–4 pencere):

| Pencere | Faktör | Uzun | Kısa | Ne için alert verir |
|---|---|---|---|---|
| `fast` | 14,4× | 1 sa | 5 dk | bir saatte 30 günlük bütçenin %2'si |
| `slow` | 6× | 6 sa | 30 dk | altı saatte 30 günlük bütçenin %5'i |

- Seri değeri `max over the windows of min(burn(long), burn(short)) / factor` olur, yani bir pencerenin faktöründe tam olarak 1'dir ve
  kuralın karşılaştırması her zaman `gte 1`'dir (yapılandırmayı faktörler taşır). Değeri üreten pencere `slo.window` etiketidir.
- Uzun penceresinde `min_requests` değerinden az ağırlıklı istek olan veya hiç isteği olmayan pencerenin değeri yoktur; hiçbir
  pencerenin değeri yoksa değerlendirmenin de **değeri yoktur** (eksik veri `keep`: durum değişmez).
- Kural başına bir seri (anahtar `slo.id=<uuid>`), etiketler `slo.id`, `slo.name`, `slo.window`, `service.name` ve SLO sabitliyorsa
  `service.namespace` ile `environment`.
- Değerlendirme PostgreSQL ister (tanım için); o olmadan kural değerlendirme hatası bildirir, SLO'su silinmiş kurallar da öyle.
  Bir SLO'yu silmek kurallarını silmez.
- Olaylar, bildirimler, mute'lar, flapping ve değerlendirme geçmişi normal alerting yollarıdır
  (alerting.md §3–§6); `slo_burn` kendine ait bir mekanizma eklemez.

## 4. API {#_4-api}

`GET /api/v1/slos`, `POST /api/v1/slos`, `GET|PUT|DELETE /api/v1/slos/{id}` ve
`GET /api/v1/slos/{id}/results`: biçimler ve roller [api.md](/tr/contracts/api#service-level-objectives) ve
[openapi.yaml](https://github.com/onuragtas/openlog/blob/master/docs/contracts/openapi.yaml) içinde (tag `slos`). Okumalar telemetri okumasıdır (her rol, API anahtarları da);
yazmalar member veya üstü rol ister, o role sahip API anahtarı da olur (D-133). `OPENLOG_AUTH_MODE=static` ile kullanılamaz (`404`).

## 5. Bu sürümde yok {#_5-not-in-this-version}

SLO başına burn penceresi varsayılanları (kurallar kendininkini taşır), takvime hizalı pencereler (yalnızca kayan pencereler), metrik veya
log üzerinde SLO'lar (yalnızca APM transaction'ları), kalan pay üzerinden bütçe alert'leri (yalnızca burn rate'ler) ve önceden hesaplanmış
bütçeler (her okuma rollup'ı toplar).
