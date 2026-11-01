# Tail-based sampling {#tail-based-sampling}

openlog hangi trace'lerin tutulacağına trace'ler tamamlandıktan **sonra** karar verebilir: her hatalı ve yavaş trace'i tut, seçtiğin
servisleri/route'ları/attribute'ları tut, geri kalanı olasılıksal olarak örnekle ve bir tenant'ın span hızını sınırla; bu sırada APM
sayıları (istekler, hatalar, throughput, gecikme yüzdelikleri) yansız kalır. Tasarım: D-075 / D-076
(`docs/plan/02-decisions.md`), kontratlar: `docs/contracts/apm.md` §4.2, `kafka.md`, `config.md`.

Tail sampling **varsayılan olarak kapalıdır**. Kapalı olması hiçbir davranış değişikliği olmadığı anlamına gelir: ingest her export
isteği için bir kayıt üretir, processor ham traces topic'ini tüketir ve hiçbir sampler çalışmaz.

## Nasıl çalışır {#how-it-works}

```
agents ──OTLP──▶ openlog-ingest ──▶ <prefix>.otlp.traces.v1          (key <tenant>/<trace id>, one record per trace)
                                            │
                                            ▼  consumer group openlog-sampler
                                     openlog-sampler  ── buffer per trace, wait, apply tenant policy
                                            │
                                            ▼  kept spans, tracestate ot=th rewritten
                                   <prefix>.otlp.traces.sampled.v1
                                            │
                                            ▼  consumer group openlog-processor
                                     openlog-processor ──▶ ClickHouse spans / apm_*
```

1. **Partition'lama.** Tail sampling açıkken ingest her trace export isteğini trace id'ye göre böler ve her kaydın anahtarını
   `<tenant>/<hex trace id>` yapar, böylece bir trace'in her span'i aynı partition'a ve dolayısıyla aynı sampler instance'ına düşer.
   Metrikler ve loglar değişmez.
2. **Buffer'lama.** Sampler span'leri (tenant, trace id) başına tutar. Bir trace için karar, ilk span'i geldikten
   `OPENLOG_TAILSAMPLING_DECISION_WAIT` (varsayılan 30 sn) sonra verilir. Bellek sınırları trace'ler için **erken** karar verdirir ("hemen karar ver",
   görülmemiş span asla atılmaz): `MAX_TRACES` (önce en eski trace), `MAX_SPANS_PER_TRACE` (o trace), `MAX_BUFFERED_BYTES` (önce en eski).
3. **Policy.** Tenant başına (Settings → APM sampling, PostgreSQL `tail_sampling_policies` içinde saklanır), yoksa
   `OPENLOG_TAILSAMPLING_DEFAULT_POLICY`, o da yoksa her şeyi tut. Kurallar **sırayla değerlendirilir; ilk eşleşen kazanır**
   ve tutma oranını verir; hiçbir kurala uymayan trace'ler `baseline_ratio` kullanır.
4. **Ağırlıklar.** Tutulan span'ler bileşik olasılık `head × tail` için `ot=th:<T>` tracestate'i alır. Processor zaten
   `th` değerini `sample_weight = 1/p` değerine çevirir (apm.md §4), bu yüzden her APM aggregate'i örneklenmemiş trafiğin Horvitz–Thompson
   tahminidir. Oran 1 ile tutulan trace'lerin span'leri bayt bayt değişmeden iletilir.
5. **Geç gelen span'ler.** Kararlar `OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL` (varsayılan 10 dk) boyunca trace başına önbelleklenir.
   Trace'i için karar verildikten sonra gelen span o karara uyar (ve aynı ağırlığı alır). TTL'den sonra veya bir rebalance sonrası başka
   bir instance'ta geç gelen span yeni bir buffer'lanmış trace başlatır ve kendi başına karar alır.
6. **Commit.** Ham topic'in offset'leri yalnızca hâlâ buffer'lanmış bir span'i veya üretilmemiş tutulan bir span'i olan en eski kayda
   kadar commit edilir.

## Policy referansı {#policy-reference}

```json
{
  "enabled": true,
  "baseline_ratio": 0.1,
  "max_spans_per_second": 20000,
  "rules": [
    { "name": "health",   "type": "route",     "route": "/health*", "ratio": 0 },
    { "name": "errors",   "type": "error" },
    { "name": "slow",     "type": "latency",   "threshold_ms": 2000 },
    { "name": "slow-db",  "type": "latency",   "threshold_ms": 500, "service": "orders-db" },
    { "name": "payments", "type": "service",   "services": ["payments"], "ratio": 0.5 },
    { "name": "vip",      "type": "attribute", "key": "customer.tier", "value": "gold" }
  ]
}
```

| Tip | Ne zaman eşleşir | Alanlar |
|---|---|---|
| `error` | herhangi bir span'in durumu `ERROR` | `service` (isteğe bağlı) |
| `latency` | trace süresi (ilk başlangıç → son bitiş) ≥ `threshold_ms`; `service` ile: o servisin bir span'i ≥ eşik | `threshold_ms`, `service` |
| `service` | herhangi bir span'in `service.name` değeri `services` içinde | `services` |
| `route` | herhangi bir span'in `http.route` değeri (route yoksa span adı) `route` glob'uyla eşleşir; sondaki `*` önek eşleşmesidir | `route`, `service` |
| `attribute` | herhangi bir span'de veya resource'unda `key` (ayarlıysa `value` ile) var | `key`, `value`, `service` |

Her kuralın isteğe bağlı bir `ratio` değeri vardır (tutma olasılığı, varsayılan 1). `ratio: 0` olan bir kural eşleşen trace'leri atar;
onu hata kuralının **önüne** ancak bunların hatalarını da gerçekten atmak istiyorsan koy.

`max_spans_per_second` (0 = sınırsız) **sampler instance'ı başına** uygulanır: sampler tenant'ın beklenen tutulan span hızını tahmin eder
ve aşıldığında her trace'in tutma olasılığını `limit / rate` ile düşürür. Bu düşüş ağırlıklara yansıtılır, yani sayılar
yansız kalır; hız sınırına takılan trace'lere hatalar da dahildir. N sampler replica'sıyla tenant'ın etkin sınırı yaklaşık N × değerdir.

`enabled: false`, policy'yi saklar ama her şeyi tutar.

### Önizleme {#preview}

Settings → APM sampling → **Preview** (`POST /api/v1/apm/sampling/preview`), düzenlenen ve kaydedilmemiş policy'yi son bir saatte
saklanan trace'lere (en fazla 20 000, trace id'lerinin hash örneklemi) uygular ve kural başına tutulan trace ve span oranını gösterir.
Saklanan trace'ler düzeltilmiş sayılarıyla hesaba katılır, yani zaten örneklenmiş olsalar bile oranlar özgün trafiği ifade eder.
Hız sınırı simüle edilmez.

## Etkinleştirme {#enabling}

Bayrak **ingest, processor ve sampler'da aynı** olmalıdır (allinone: tek süreç).

| Kurulum | Adımlar |
|---|---|
| Compose (`single`) | `.env` içinde `OPENLOG_TAILSAMPLING_ENABLED=true`, `docker compose up -d`. allinone sampler'ı süreç içinde çalıştırır. |
| Helm | `tailSampling.enabled: true` (değişkeni ingest ve processor'da ayarlar ve `openlog-sampler`'ı HPA'sıyla kurar). |

Açmak veya kapatmak, kısa bir karışık dönemi olan rolling bir değişikliktir:

- **Açma.** Sampled topic zaten vardır (bu sürümden beri `openlog-migrate` oluşturur). Önce sampler'ı, sonra processor'ı, sonra ingest'i
  rollout et. Processor geçene kadar span'ler ClickHouse'a birkaç saniye iki kez ulaşır (ham yol +
  sampled yol); ingest geçene kadar eski çok trace'li kayıtlar yine sampler tarafından bölünür (span'leri farklı trace'lere
  anahtarlanmış isteklerle gönderilen bir trace instance'lar arasında bölünebilir ve parçalar halinde karar alabilir; error/latency
  kuralları o zaman trace'in yalnızca bir kısmını tutabilir; olasılıksal baseline yine tutarlıdır, aşağıya bak).
- **Kapatma.** Önce ingest ve processor'ı rollout et (ham topic'e dönerler; processor grubu son commit ettiği ham offset'ten devam eder,
  bu eski olabilir → açıldığından beri yeniden okuma ve kopyalar veya ham topic retention'ı içindeki veri).
  Bunu önlemek için kapatmadan önce processor grubunun ham traces offset'lerini sona al:
  `kafka-consumer-groups.sh --group openlog-processor --topic openlog.otlp.traces.v1 --reset-offsets --to-latest --execute`
  (processor'lar durdurulmuşken). Lag'i 0'a ulaştıktan sonra sampler'ı durdur.

## Boyutlandırma {#sizing}

Bellek ≈ `MAX_BUFFERED_BYTES` (varsayılan 512 MiB, protobuf span baytları) + karar önbelleği (girdi başına ~150 B × 500 000 ≈ 75 MiB)
+ çözülmüş kayıt ek yükü. Trace'ler karar bekleme süresi boyunca tutulur, yani buffer `span rate × wait` kadar span tutar:
örn. 20 000 span/sn × 30 sn ≈ 600 000 span ≈ 300–600 MiB. Container bellek limitini ≥ 1,5 × `MAX_BUFFERED_BYTES` yap.
`openlog_tailsampling_evictions_total{reason="max_bytes"|"max_traces"}` artıyorsa replica ekle (ham traces topic'inin partition
sayısına kadar) veya bekleme süresini kısalt.

## Yatay ölçekleme ve rebalance'lar {#horizontal-scaling-and-rebalances}

- Ham traces topic'inin consumer lag'ine göre ölçekle (`openlog_tailsampling_consumer_lag_records`); replica ≤ partition.
  Helm: sampler HPA'sı kutudan çıktığı gibi CPU'ya göre ölçeklenir (resource metrics API gerekir, örn. metrics-server).
  `sampler.autoscaling.lag.enabled`, Kubernetes'in kendi başına sunamadığı bir `External` metrik ekler:
  `openlog_tailsampling_consumer_lag_records` değerini sampler pod'u başına toplanmış olarak sunan bir external metrics adapter'ı
  (prometheus-adapter, KEDA'nın Prometheus scaler'ı) kur; yoksa HPA `FailedGetExternalMetric` raporlar ve yalnızca CPU'ya göre ölçeklemeyi sürdürür.
- kind üzerinde doğrulandı (2026-09-14, `deploy/helm/test/kind-validate.sh` adımı `sampling`, operators modu, 3 partition): sampler
  Deployment 2/2, PDB `maxUnavailable: 1`, `cpu: 3%/70%` okuyan CPU HPA'sı (metrics-server 3.13.1). loadgen 2 dk boyunca 100 span/sn
  (trace başına 5 span, %2 hatalı trace), varsayılan policy `baseline_ratio: 0.1` + bir `error` kuralıyla: 12 000 span için karar verildi
  (1 355 tutuldu + 10 645 atıldı, hepsi ClickHouse'ta), baseline 2 352 trace'in 223'ünü tuttu (%9,5), `errors` 48 hatalı trace'in hepsini tuttu;
  ağırlıklar baseline trace'leri için 10, hatalı trace'ler için 1 (`sum(sample_weight)` 11 390, gönderilen 12 000).
  Lag HPA'sı denenmedi (adapter yok).
- **Geri alınan (revoked) partition'lar için hemen karar verilir**: revoke callback'inde sampler o partition'lardan span'i olan her trace
  için karar verir, tutulan span'leri üretir ve commit eder, sonra partition'ları bırakır. Böyle bir trace'in sonradan gelen span'lerini yeni
  sahip yeni bir trace olarak işler (geç span durumu). Yani bir rebalance o anda devam eden trace'leri iki karara böler; trace id'ye
  göre tutarlı baseline, olasılıksal örneklenen trace'lerde iki parçayı uzlaştırır, kural tabanlı kararlar farklı olabilir.
- **Kaybedilen partition'lar** (session timeout) için aynı şekilde karar verilir ve üretilir, ama commit başarısız olur: yeni sahip bu
  kayıtları yeniden okur → o aralık için ClickHouse'ta kopyalar oluşur.
- **Kapanış** (SIGTERM) 25 sn içinde her şey için karar verir, üretir ve commit eder;
  `terminationGracePeriodSeconds` ≥ 40 ayarla.

### Olasılıksal kararın tutarlılığı {#consistency-of-the-probabilistic-decision}

Tutarlı head sampler'a sahip trace'lerde (tracestate `ot=th`) sampler, trace'in 56 bitlik rastgeleliği
`R` (tracestate `ot=rv`, yoksa trace id'nin son 7 baytı) `head × tail` eşiğinden büyük veya eşitse trace'i tutar. Head sampling'in
tuttuğu varsayılırsa (`R ≥ T(head)`) bu tam olarak `tail` olasılığıyla olur ve her instance, yeniden başlatma veya
yeniden teslimat aynı oran için aynı sonuca ulaşır. `ot=th` olmayan trace'ler (head sampling yok veya yalnızca
`sampling.ratio`) bunun yerine trace id'nin hash'ini kullanır, bu yüzden trace-id-ratio head sampler ile ilişkili olmazlar.

## Teslimat semantiği (D-076) {#delivery-semantics-d-076}

Pipeline'ın geri kalanı gibi at-least-once: tutulan span'ler ham offset'ler commit edilmeden **önce** üretilir (acks=all, idempotent
producer). Sampler üretim ile commit arasında çökerse, commit başarısız olursa, partition'lar kaybedilirse veya kısmi bir hatadan sonra
üretim yeniden denenirse ClickHouse'ta kopyalar olabilir; processor'ın tekilleştirme token'ları bunları kapsamaz (sampled topic'in offset
aralığı başınadır). Kafka transaction'ları (read_committed processor + transactional sampler) bu kopyaları kaldırırdı ama seçilmedi: bkz. D-076.

## Metrikler {#metrics}

| Metrik | Anlamı |
|---|---|
| `openlog_tailsampling_traces_buffered`, `_spans_buffered`, `_buffered_bytes` | mevcut buffer |
| `openlog_tailsampling_decisions_total{policy, decision}` | ilk eşleşen kurala (`baseline`) ve `kept`/`dropped` değerine göre kararlar |
| `openlog_tailsampling_spans_total{decision}` | tutulan/atılan span'ler (geç span'ler dahil) |
| `openlog_tailsampling_late_spans_total{decision}` | önbelleklenmiş bir karara uyan span'ler |
| `openlog_tailsampling_evictions_total{reason}` | erken kararlar: `max_traces`, `max_spans`, `max_bytes`, `revoked`, `shutdown` |
| `openlog_tailsampling_rate_limited_traces_total` | hız sınırı yüzünden olasılığı düşürülen trace'ler |
| `openlog_tailsampling_decision_delay_seconds` | ilk span → karar |
| `openlog_tailsampling_decision_cache_entries` | hatırlanan kararlar |
| `openlog_tailsampling_consumer_lag_records{topic, partition}` | bu üyenin ham traces partition'larının lag'i (HPA) |
| `openlog_tailsampling_records_rejected_total{reason}` | çözülemeyen kayıtlar, geçersiz id'li span'ler |
| `openlog_tailsampling_produce_failures_total` | başarısız üretim denemeleri (yeniden denenir) |
| `openlog_tailsampling_policy_load_failures_total` | başarısız PostgreSQL policy yeniden yüklemeleri (önceki policy'ler kalır) |

İşe yarar sorgular:

```promql
# tutulan span payı
sum(rate(openlog_tailsampling_spans_total{decision="kept"}[5m])) / sum(rate(openlog_tailsampling_spans_total[5m]))
# bellek dolduğu için erken karar verilen trace'ler
sum by (reason) (rate(openlog_tailsampling_evictions_total{reason=~"max_.*"}[5m]))
```

## Sorun giderme {#troubleshooting}

| Belirti | Neden / çözüm |
|---|---|
| açtıktan sonra arayüzde trace yok | processor geçmemiş (`OPENLOG_TAILSAMPLING_ENABLED` onda eksik) veya çalışan sampler yok: `openlog-sampler` grubunun lag'ini kontrol et |
| birkaç saniye yinelenen span'ler | açma sırasında karışık rollout (yukarıya bak) |
| hatalı trace'ler kısmen saklanmış | trace karar bekleme süresinden uzun, `MAX_SPANS_PER_TRACE` doldu veya bir rebalance onu kesti; bekleme süresini / limiti yükselt |
| açtıktan sonra APM throughput'u değişti | düzeltilmiş sayılar tahmindir; küçük baseline oranlarında düşük trafikli transaction'lar gürültülü olur. `baseline_ratio` değerini yükselt veya onlar için kural ekle |
| policy değişikliği uygulanmadı | sampler'lar her `OPENLOG_TAILSAMPLING_POLICY_REFRESH` (30 sn) değerinde yeniden yükler |
