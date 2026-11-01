# Sözleşme: Altyapı maliyeti (v1) {#contract-infrastructure-cost-v1}

openlog'un "bu host `eu-central-1`'de bir `m5.large`" bilgisini "bu servis sana ayda 420 $'a mal oluyor ve o CPU'nun %60'ı boşta" bilgisine
nasıl çevirdiği. `internal/cost` (fiyat tablosu ve paylaştırma), `internal/api/cost.go` (endpoint'ler) ve ajanın instance bilgisi toplaması
(`agents/infra/internal/resource/cloud.go`) tarafından uygulanır.

> **Bu sayılar tahmindir, faturalama verisi değil.** openlog bir bulut faturalama API'siyle asla konuşmaz. Host'un hangi makine olduğunu
> bilir ve bu repository'deki bir tablodan aldığı liste fiyatıyla çarpar. Tahminin görmezden geldiği her şeyi §2 sayar.
> Servisleri karşılaştırmak ve boşta kapasite bulmak için kullan, asla fatura mutabakatı için değil.

## 1. Instance bilgileri nereden gelir {#_1-where-the-instance-facts-come-from}

Altyapı ajanı instance metadata servisine **başlangıçta bir kez** sorar ve cevabı her payload'a resource attribute olarak ekler
(semantic-conventions.md §1 "Cloud instance facts"): `cloud.provider`, `cloud.platform`,
`cloud.region`, `cloud.availability_zone`, `cloud.account.id`, `host.type` (instance tipi) ve
`openlog.host.lifecycle` (`on-demand`, `spot`, `preemptible`).

| Sağlayıcı | Endpoint | Alanlar |
|---|---|---|
| AWS | `169.254.169.254/latest/dynamic/instance-identity/document` (önce IMDSv2 token'ı, IMDSv1'e düşer), `/latest/meta-data/instance-life-cycle` | `instanceType`, `region`, `availabilityZone`, `accountId`, spot/on-demand |
| GCP | `169.254.169.254/computeMetadata/v1/instance/{machine-type,zone,scheduling/*}` (`Metadata-Flavor: Google`) | makine tipi, zone (region türetilir), project id, `provisioning-model` / `preemptible` |
| Azure | `169.254.169.254/metadata/instance?api-version=2021-02-01` (`Metadata: true`) | `vmSize`, `location`, `zone`, `subscriptionId`, `priority` |

Tüm yoklama **1,5 sn**, tek istek **500 ms** ile sınırlıdır. Her zaman link-local IP kullanır (asla DNS değil, yani
`metadata.google.internal`'ın çözülmediği bir host hiçbir şey beklemez) ve ortamdan gelen HTTP proxy'yi asla kullanmaz, yani bir metadata
isteği makineden çıkamaz. DMI sistem üreticisi (`/sys/class/dmi/id/sys_vendor`) yalnızca hangi sağlayıcıya önce sorulacağını belirler; yanlış
veya eksik ipucu bir yoklama fazlasına mal olur, tespiti asla engellemez.

**Bulutta olmayan bir host** link-local adreste reddedilen bir bağlantı alır veya en kötü ihtimalle paketleri düşüren bir firewall arkasında
süre sınırına takılır. Sonra bu attribute'ların hiçbirini taşımaz ve başlangıcı başka türlü etkilenmez: hata yok, yeniden deneme döngüsü yok,
tekrarlı yoklama yok. Böyle bir host vCPU/GB başına fiyatlanır (§2) veya fiyatsız olarak raporlanır; asla bedava raporlanmaz.
`host.cloud_metadata: off` yoklamayı tamamen atlar.

## 2. Fiyat tablosu {#_2-the-price-table}

`internal/cost/prices.json` binary'ye derlenir. Sağlayıcı başına yaygın instance ailelerinin saatlik on-demand ve spot fiyatını, vCPU/GB başına
bir fallback ücretini ve referans region'a (`us-east-1`, `us-central1`, `eastus`) göre region başına bir çarpanı tutar. Kendi `version`,
`updated` tarihini, `currency` değerini, sayıların okunduğu `sources` listesini ve API'nin her yanıtla döndüğü bir `note` taşır.

**Fiyatlar nedir:** Linux instance'ları için yaklaşık public liste fiyatları, `updated` tarihinde elle toplanmış.

**Neyi görmezden gelirler:** bunların her biri gerçek bir faturayı, çoğu zaman epey, farklı yapar:

- committed-use indirimleri, reserved instance'lar, savings plan'lar ve kurumsal anlaşmalar;
- kredi, ücretsiz katman, promosyon ve pazarlıkla belirlenen fiyatlar;
- vergiler ve kur dönüşümü;
- işletim sistemi ve yazılım lisansları (Windows, RHEL, marketplace image'ları);
- **depolama, snapshot'lar, network egress, load balancer'lar, yönetilen servisler**: yalnızca compute instance fiyatlanır;
- saniye başına faturalama hassasiyeti, asgari faturalama süreleri ve stop/start davranışı;
- çarpan listesinde olmayan region'lar (referans region ücretinden fiyatlanır);
- sürekli değişen spot fiyatları: tablonun spot sütunu tipik bir değerdir, ödediğin fiyat değil.

Yani bir fiyat *"buna benzer bir makinenin liste fiyatı yaklaşık bu kadar"* der ve aynı fleet üzerindeki iki servisin karşılaştırması mutlak
sayıdan çok daha güvenilirdir.

### Çözümleme sırası {#resolution-order}

1. sağlayıcı için birebir instance tipi; operatörün ezmesi tanımlıyorsa oradan (`source: "override"`), yoksa yerleşik tablodan (`source: "table"`);
2. aksi halde sağlayıcının vCPU/GB başına fallback ücreti veya bilinmeyen sağlayıcı için `default` ücreti, host'un ölçülen kapasitesine
   uygulanır (`source: "fallback"`);
3. aksi halde **fiyat yok** (`source: "none"`): host `"priced": false` ile raporlanır ve `unpriced_hosts` içinde sayılır, asla bedavaymış gibi değil.

Region çarpanı 1. ve 2. durumda uygulanır. Spot veya preemptible makinede spot sütunu kullanılır; kaydın spot fiyatı yoksa on-demand ücret
kullanılır ve sonucun `note` alanı tahminin fazla yüksek olduğunu söyler. Fallback ücreti bir on-demand ücrettir ve spot makineler için bunu söyler.

### Operatör ezmesi {#operator-override}

`OPENLOG_COST_PRICES_FILE`, başlangıçta yerleşik tablonun üzerine birleştirilen bir JSON dosyasını gösterir, yani fiyat bir sürüm beklemeden
düzeltilebilir. Yalnızca dosyanın belirttiği değişir (tek instance tipi, tek sağlayıcının fallback ücreti, tek sağlayıcının region çarpanları),
yani tek makine için yapılan düzeltme tablonun geri kalanını düşürmez:

```json
{
  "updated": "2026-10-01",
  "instances": {"aws": {"m5.large": {"on_demand": 0.0712, "spot": 0.0301}}},
  "fallback": {"aws": {"vcpu_hour": 0.028, "gb_hour": 0.004}},
  "region_multipliers": {"aws": {"eu-central-1": 1.09}}
}
```

Bilinmeyen bir anahtar, geçersiz JSON veya okunamayan bir dosya **api'yi durdurur**: operatörün değiştirmek istediği sayılarla sessizce
fiyatlamak, başarısız bir başlangıçtan daha kötü olurdu. `GET /api/v1/costs/prices` geçerli tabloyu `override_file` ve `overridden_keys` ile
döner, yani bir düzeltme arayüzden doğrulanabilir.

## 3. Paylaştırma {#_3-attribution}

Tamamı openlog'un zaten sakladığı veri üzerinde saf aritmetiktir (`internal/cost/attribute.go`); faturadan hiçbir şey çıkarılmaz. İstenen
aralıkta, *h* host'u için:

```
host cost      = price per hour × hours h reported
workload share = 0.5 × (workload core-hours / host core-hours)
               + 0.5 × (workload byte-hours / host byte-hours)
host usage     = 0.5 × (used cores / vCPUs) + 0.5 × (used bytes / total bytes)
```

ve host'un maliyeti, toplamı her zaman ona tam eşit olan dört kovaya ayrılır:

| Kova | Nedir |
|---|---|
| `services` | bir APM servisine bağlı container'ların Σ payları |
| `unallocated` | bağlı servisi olmayan container'ların Σ payları |
| `unattributed` | `host usage − Σ container shares`: hiçbir container'ın açıklamadığı gerçek iş |
| `idle` | `1 − host usage`: kimsenin kullanmadığı kapasite |

### Girdiler {#inputs}

| Büyüklük | Kaynak |
|---|---|
| host vCPU'ları, bellek | `system.cpu.logical.count`, `system.memory.limit` (aralıktaki tepe) |
| host kullanımı | `system.cpu.utilization` (`1 − idle`, olmazsa idle dışı modların toplamı) ve `system.memory.usage{used}` |
| raporlanan saatler | host'un host başına serisinin farklı bir dakikalık bucket'ları, aralıkla sınırlı |
| container CPU, bellek | `container.cpu.utilization` (tüm host'un bir kesri → çekirdek) ve `container.memory.usage` |
| container → servis | `apm_service_containers` (apm.md §1), en yeni bağlantı kazanır |

Tamamı 395 gün tutulan 1 dakikalık rollup `metrics_1m` üzerinden okunur, yani maliyet aralığı 30 günlük ham metrik retention'ıyla sınırlı değildir.

### Açıkça belirtilen varsayımlar {#assumptions-stated-honestly}

- **CPU ve bellek eşit ağırlıktadır (0.5 / 0.5).** Her bölüşüm keyfidir. Yalnızca CPU'yu ağırlıklandırmak belleği yoğun servisleri bedava
  gösterir, tersi de geçerlidir; eşit ağırlıklar en az şaşırtan seçimdir ve yukarıdaki formül herkesin aritmetiği elle tekrarlamasını sağlar.
- **Host, var olduğu saatler için değil raporladığı saatler için para tutar.** Aralığın yarısında raporlamış bir host'a yarısı yazılır. openlog
  gözlediğini fiyatlar; ayakta ama sessiz bir makine ona görünmez, silinmiş bir makinenin maliyeti son veri noktasında durur.
- **Kullanım, aralık üzerinde ortalamadır.** 23 saat boşta durup bir saat bir CPU'yu dolduran servis, gün boyu bir CPU'nun 1/24'ünü kullanan
  servisle aynı payı alır. Daha kısa aralıklar farkı gösterir.
- **`container.cpu.utilization` tüm host'un bir kesridir**, yani bir container'ın çekirdekleri o kesir × host'un vCPU'larıdır. vCPU sayısı
  bilinmeyen bir host'ta container yalnızca bellekle katkı yapar.
- **Request ve limit'ler kullanılmaz**, yalnızca gerçek kullanım. 8 GB ayıran ve 1 GB kullanan servise kullandığı yazılır; ayrılmış ama kullanılmamış
  kapasite host'unda idle olarak görünür, operatörün müdahale edebileceği yer de orasıdır.
- **Yalnızca container'lar paylaştırılır.** Container dışındaki iş (makinedeki bir veritabanı, cron job, kernel) bir servise bağlanamaz, bu yüzden
  sessizce paylaştırılmak veya yanlışlıkla idle sayılmak yerine görünür şekilde `unattributed`'a düşer.
- **Aşırı abonelik gizlenmez, ölçeklenir.** Container payları host'un kullanımını aşarsa (çift sayım, sampling gürültüsü, aralık içinde host
  değiştiren container) sığacak şekilde küçültülür ve host `oversubscribed` işaretlenir. Host'un toplamı birebir kalır ve idle asla negatif olmaz.
- **Idle asla yeniden dağıtılmaz.** Host düzeyinde (`idle`, `idle_share`) ve fleet düzeyinde kendi sayısı olarak raporlanır. Boşta maliyeti servislere
  yaymak her servisi daha pahalı gösterir ve doğrudan aksiyon alınabilecek tek bulguyu gizlerdi.
- **Instance bilgisi ve kapasitesi olmayan bir host'un fiyatı yoktur.** 0 katkı yapmak yerine `unpriced_hosts` içinde sayılır, yani fleet toplamı
  asla sessizce düşük kalmaz.

## 4. API ve arayüz {#_4-api-and-ui}

Endpoint'ler, yanıt biçimleri ve durum kodları: [api.md](/tr/contracts/api) "Costs". Arayüzün Costs görünümü (`/costs`) toplamı, saatlik run rate'i,
idle payını, aralıktaki trendi ve servise ve host'a göre dökümü gösterir; host detay sayfası tek bir host'un maliyetini ve dağılımını gösterir.
İkisi de fiyat tablosunun `note` değerini sayıların yanında gösterir.

## 5. Bilerek burada olmayanlar {#_5-what-is-deliberately-not-here}

- **ClickHouse migration'ı yok.** Her girdi zaten `metrics_1m`, `hosts`, `containers` ve `apm_service_containers` içinde var; maliyete özel bir rollup
  yeni bilgi değil performans optimizasyonu olurdu ve büyük bir fleet sorgunun fazla yavaş olduğunu gösterene kadar şema değişikliğine değmez.
- **Bulut faturalama entegrasyonu yok.** Gerçek bir faturayı okumak (Cost and Usage Report, Cloud Billing export) farklı kimlik bilgileri ve farklı
  güven modeli olan ayrı bir özelliktir. Var olduğunda kaynak olarak fiyat tablosunun *yerini alacak* ve bu sözleşmenin bölüşümü (§3) bir faturanın
  servislere paylaştırılma yöntemi olarak kalacak.
- **Tenant başına fiyat tablosu yok.** Tablo bir operatör ayarıdır, organizasyon ayarı değil.
