# Terraform provider'ı (`terraform-provider-openlog`) {#terraform-provider-terraform-provider-openlog}

Provider, bir openlog organizasyonunun **yapılandırmasını** kod olarak yönetir: alarm kuralları, bildirim kanalları, yönlendirme
kuralları, service level objective'ler ve dashboard'lar (D-131). Belgelenmiş HTTP API'nin (`/api/v1`, [api.md](/contracts/api)) bir
istemcisidir ve başka hiçbir şeye ihtiyaç duymaz: veritabanı erişimi de, sunucuda bir dosya da gerekmez.

Kaynak kodu: bu repodaki `terraform/`. Ajanlar ve SDK'lar gibi Apache-2.0 altında kendi başına bir Go modülüdür
(`github.com/onuragtas/openlog/terraform`, D-004), böylece plugin framework'ünün bağımlılık ağacı AGPL-3.0'lı sunucu binary'lerine
hiç ulaşmaz.

**Yönetmedikleri:** telemetri, incident'lar, mute pencereleri, tatil takvimleri, paylaşım linkleri, zamanlanmış raporlar, API key'ler,
üyeler ve davetler. Incident'lar ve mute'lar istenen durumdan çok operasyonel durumdur, geri kalanı bilinçli bir ilk kapsam
tercihidir (§7).

## 1. Yazma yetkisi olan bir kimlik bilgisi {#_1-a-credential-that-may-write}

**İlk kontrol etmen gereken ön koşul bu.** Bir openlog API key'inin (`ola_…`) kendi rolü vardır: `viewer` (varsayılan), `member` veya
`admin` ([api.md "Roles"](/contracts/api#roles), D-133). Provider'a rolü yazmaya izin veren bir key gerekir. **`admin`** kullan: key hiçbir
şeye sahip olmaz, bu yüzden `member` key'iyle oluşturulan bir kuralın, SLO'nun veya dashboard'un oluşturanı olmaz ve aynı key onu sonradan
değiştiremez. `admin` key'i organizasyondaki her şeyi yönetir, kendi kaynaklarına sahip çıkan bir provider'ın yaptığı da budur.

Key'i oturum açmış bir admin veya owner oluşturur (bir key asla key oluşturmaz). Web arayüzünde Settings → API keys bir `viewer` key'i
oluşturur. Rolü seçmek için API'yi admin veya owner oturumuyla çağır:

```sh
curl -sS -X POST https://openlog.example.com/api/v1/api-keys \
  -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" -b "openlog_session=$SESSION" \
  -d '{"name": "terraform", "role": "admin"}'
```

`viewer` üstündeki bir `role` için admin veya owner gerekir ve çağıranın kendi rolünü asla aşamaz (`403`). Bilinmeyen rol `400` döner.
Yanıt key'i bir kez gösterir: provider'ın `OPENLOG_API_KEY` değeri budur (§3).

**Salt okunur (`viewer`) key yine de başarısız olur:** `plan` çalışır, çünkü okumaya izin vardır, ama ilk `apply` şununla durur:

```
Error: Could not create the alert rule: the API key may not write

  this API key's role (viewer) does not allow this operation

  The openlog API accepts writes from a credential with write permission; a read-only API key is
  refused here. See docs/operations/terraform.md.
```

Provider'da session cookie varsayan hiçbir şey yoktur: [mcp.md](/tr/operations/mcp) ile tamamen aynı şekilde, `Authorization: Bearer`
ve `X-Openlog-Org-Id` ile kimlik doğrular.

Kanallar için sunucuda ayrıca `OPENLOG_SECRETS_KEY` gerekir. O olmadan API kanal yazmalarını `409 failed_precondition` ile reddeder
(alerting.md §5.4).

## 2. Derleyip yerelde kullanmak {#_2-building-and-using-it-locally}

Provider bir registry'de yayınlanmıyor. Derle ve bir dev override ile Terraform'u binary'ye yönlendir:

```sh
cd terraform
go build -o ~/.terraform.d/plugins/terraform-provider-openlog ./cmd/terraform-provider-openlog
```

`~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "onuragtas/openlog" = "/Users/you/.terraform.d/plugins"
  }
  direct {}
}
```

Dev override ile Terraform bu provider için `terraform init` adımını atlar ve her komutta bir uyarı yazdırır. Bu beklenen davranıştır.

## 3. Provider bloğu {#_3-the-provider-block}

```hcl
terraform {
  required_providers {
    openlog = {
      source = "onuragtas/openlog"
    }
  }
}

provider "openlog" {
  endpoint = "https://openlog.example.com" # veya OPENLOG_ENDPOINT
  # api_key OPENLOG_API_KEY'den okunur; yapılandırmanın dışında tut
  # org_id  = "…"                          # veya OPENLOG_ORG_ID, yalnızca birden çok organizasyonda
  # timeout_seconds = 30
}
```

| Öznitelik | Ortam değişkeni | Notlar |
|---|---|---|
| `endpoint` | `OPENLOG_ENDPOINT` | `/api/v1` **olmadan** temel URL; sondaki `/api/v1` kırpılır |
| `api_key` | `OPENLOG_API_KEY` | Hassas. Ortam değişkenini tercih et: yapılandırmaya yazılan key state dosyasına geçer |
| `org_id` | `OPENLOG_ORG_ID` | `X-Openlog-Org-Id`; yalnızca kimlik bilgisi birden çok organizasyona aitse gerekir ve key'in organizasyonuyla eşleşmelidir |
| `timeout_seconds` | — | Tek API isteği, 1–3600, varsayılan 30 |

## 4. Çalışan bir örnek {#_4-a-worked-example}

Bir Slack kanalı, ona bildirim gönderen bir metrik eşik kuralı ve checkout servisi için bir availability SLO'su:

```hcl
resource "openlog_notification_channel" "oncall_slack" {
  name = "on-call Slack"
  type = "slack"

  secrets = {
    url = var.slack_webhook_url # hassas; plan'da asla yazdırılmaz
  }
}

resource "openlog_alert_rule" "host_cpu_high" {
  name        = "Host CPU high"
  description = "CPU above 90 % for five minutes, one incident per host."
  type        = "metric_threshold"
  severity    = "critical"

  interval_seconds = 60
  for_seconds      = 300

  condition = jsonencode({
    metric             = "system.cpu.utilization"
    aggregation        = "avg"
    window_seconds     = 300
    group_by           = ["host"]
    operator           = "gt"
    threshold          = 0.9
    recovery_threshold = 0.8
    filters = [
      { field = "attr.cpu.mode", op = "not_in", values = ["idle"] },
    ]
  })

  channel_ids = [openlog_notification_channel.oncall_slack.id]
  labels      = { team = "infra" }
}

resource "openlog_slo" "checkout_availability" {
  name         = "Checkout availability"
  service_name = "checkout"
  environment  = "prod"
  sli_type     = "availability"
  objective    = 99.9
  window_days  = 28
}

# Bu SLO için burn-rate alarmı, slo_burn türünde sıradan bir alarm kuralıdır.
resource "openlog_alert_rule" "checkout_budget_burn" {
  name     = "Checkout error budget burning"
  type     = "slo_burn"
  severity = "critical"

  condition = jsonencode({
    slo_id = openlog_slo.checkout_availability.id
  })

  channel_ids = [openlog_notification_channel.oncall_slack.id]
}
```

`terraform apply`, ardından yeniden `terraform plan`: ikinci plan boştur. Bir `condition` değişikliğinden sonra bunu kontrol etmeye değer,
çünkü §6 tam olarak bununla ilgili.

## 5. Kaynaklar ve data source'lar {#_5-resources-and-data-sources}

| Kaynak | Yönettiği | API |
|---|---|---|
| `openlog_alert_rule` | on türden herhangi birinde bir alarm kuralı | `/api/v1/alerts/rules` |
| `openlog_notification_channel` | Slack, e-posta, webhook, Teams, PagerDuty, Opsgenie | `/api/v1/alerts/channels` |
| `openlog_alert_routing_rule` | bir incident açıldığında hangi kanallara ulaşacağı | `/api/v1/alerts/routing-rules` |
| `openlog_slo` | bir service level objective | `/api/v1/slos` |
| `openlog_dashboard` | bütün bir dashboard belgesi (sayfalar, widget'lar, değişkenler) | `/api/v1/dashboards` |

| Data source | Aradığı |
|---|---|
| `openlog_alert_channel` | `id` veya `name` ile bir kanal (maskelenmiş `secret_hints`, asla secret'lar değil) |
| `openlog_dashboard` | `id` veya `name` ile bir dashboard; `document` değeri saklanan `{variables, pages}` JSON'udur |

Her kaynak `terraform import <address> <id>` destekler, id API'nin ve web arayüzü URL'sinin gösterdiği id'dir. Bir kanalın secret'ları
import edilemez, çünkü openlog onları asla döndürmez. Bu yüzden bir kanalı import ettikten sonra yapılandırma `secrets` değerini yeniden
taşımalıdır ve sonraki apply onları tekrar gönderir.

### `condition` JSON'dur {#condition-is-json}

On kural türünün her birinin kendine ait bir condition yapısı vardır (alerting.md §2.2–§2.12), bu yüzden `condition` on ayrı tipli blok
yerine `jsonencode({…})` ile yazılan bir JSON belgesidir. API onu alan alan doğrular ve sorunlu yolu bildirir, provider da bunu
`condition` özniteliğine bağlar:

```
Error: Invalid value

  with openlog_alert_rule.host_cpu_high,
  on main.tf line 21, in resource "openlog_alert_rule" "host_cpu_high":
  21:   condition = jsonencode({

condition.threshold: required
```

openlog'un sakladığı tam belge, doldurduğu her varsayılanla birlikte, hesaplanan `condition_effective` özniteliğidir. Üç satırlık bir
condition'ın gerçekte ne anlama geldiğini görmenin en hızlı yolu budur.

## 6. Drift, varsayılanlar ve eşzamanlı değişiklikler {#_6-drift-defaults-and-concurrent-changes}

**Kararı okumalar verir.** Her create ve update'ten sonra provider plan'ı değil, API'nin cevabını saklar. Bu yüzden sunucu tarafındaki
normalizasyon sessizce drift etmek yerine sonraki plan'da diff olarak görünür.

**Terraform dışında silinenler.** `404` alan bir okuma kaynağı state'ten çıkarır ve sonraki plan onu yeniden oluşturur. Zaten olmayan
bir şeyi silmek hata değildir.

**Varsayılanlı condition'lar sürekli diff üretmez.** openlog bir condition'ı ayrıştırır ve eksiksiz olarak geri yansıtır: üç alanlı bir
condition on alanla döner. Bu cevabı olduğu gibi saklamak her plan'da diff gösterirdi, yapılandırmayı saklamak ise gerçek drift'i gizlerdi.
Provider, openlog **yapılandırmanın belirlediği her alanda** hemfikir olduğu sürece yapılandırılan belgeyi tutar, bunlardan biri farklı
olduğu anda onu openlog'un belgesiyle değiştirir ki diff'in çıkması gereken an da tam olarak budur. Yapılandırmaya alan eklemek normal bir
diff'tir, saklanan belgenin geri kalanı `condition_effective` içinde görünür kalır.

**İyimser eşzamanlılık.** Alarm kuralları ve dashboard'lar, openlog'un her değişiklikte artırdığı bir `version` taşır. Provider update ile
birlikte son okuduğu sürümü gönderir, böylece bu arada biri web arayüzünde düzenlediyse kural sessizce üzerine yazılmak yerine reddedilir:

```
Error: Could not update the alert rule: the object changed in openlog

  conflicting change; reload and try again

  Someone changed this object (or a limit was reached) since Terraform last read it. Run
  'terraform refresh' (or plan again) and re-apply; this provider never overwrites a newer version blindly.
```

`terraform apply -refresh-only` (veya refresh yapan sıradan bir plan) yeni sürümü alır, sonra değişiklik uygulanır. Aynı mesaj API'nin
diğer `409`'larını da kapsar: ikinci bir varsayılan yönlendirme kuralı, bir organizasyonun 1000. kuralı, 201. SLO, `OPENLOG_SECRETS_KEY`
olmadan kanal yazma. Çünkü çözümleri aynıdır: yeniden yazmadan önce orada ne olduğuna bak.

**Yönlendirme kurallarının sürümü yoktur.** Küçük, sıralı bir listedirler ve openlog onları bütün olarak değiştirir. Bir yönlendirme
kuralındaki eşzamanlı düzenlemenin üzerine sonraki apply yazar. Sıra `position` değerinden gelir, sıra önemliyse her route'a kendi
numarasını ver.

**Sayfa ve widget id'leri.** openlog bunları kendisi atar ve bir yazma onları taşıyorsa korur, bu yüzden provider son okuduğu id'leri geri
gönderir: bir widget'ı düzenlemek kimliğini ve dashboard'un sürüm geçmişini korur. Listenin ortasına sayfa eklemek sonraki sayfaların id'lerini
kaydırır. Bu zararsızdır ama plan'da görünür.

**openlog'un yeniden yazdığını provider gizlemez.** Bilmeye değer iki durum var, çünkü ikisi de sessiz bir değişiklik yerine apply sırasında
hata olarak çıkar: `discovery` veya `apm_error` kuralındaki `for_seconds` (bu türler onu yok sayar ve openlog 0 saklar) ve `enabled = false`
yanındaki bir `flapping` bloğunun üç sayısı (openlog kendi varsayılanlarını saklar). İkincisi doğrulama sırasında, bunu söyleyen bir mesajla
yakalanır.

## 7. Secret'lar {#_7-secrets}

Kanal secret'ları (Slack veya webhook URL'si, HMAC secret'ı, SMTP parolası, PagerDuty routing key'i, Opsgenie API key'i) yalnızca
yazılabilir. openlog bunları PostgreSQL'e ulaşmadan önce AES-256-GCM ile şifreler ve asla döndürmez. Okuma bunun yerine maskelenmiş
`secret_hints` (`••••••  (set)`) ile cevap verir.

Provider'da:

- `secrets` **hassas** olarak işaretlidir, bu yüzden plan değerin kendisini değil `(sensitive value)` yazdırır;
- okuma `secrets`'a asla dokunmaz: karşılaştıracak bir şey yoktur, bu yüzden web arayüzünde döndürülen bir secret Terraform'a görünmez,
  onun yerine burada döndür;
- `generated_secrets` (hiç verilmediğinde webhook'un `hmac_secret` değeri) de hassastır ve yalnızca create cevabında döner;
- yine de değerler, her Terraform secret'ı gibi **state**'e yazılır, bu yüzden state erişimi kısıtlı, şifreli bir backend'de durmalıdır.

## 8. Testler {#_8-tests}

`cd terraform && go test ./...` birim testleri çalıştırır: API istemcisini bir `httptest` fake'ine karşı (auth header'ları, hata yapısı,
alan yolu, 404 ve 409 işleme) ve şema eşlemesini iki yönde. Acceptance testleri canlı bir openlog yerine aynı fake API'yi kullanır ve `TF_ACC=1`
ile bir `terraform` binary'si yoksa atlanır, yani normal test çalıştırması ikisini de gerektirmez.

CI modülü `.github/workflows/ci.yml` dosyasının `go` job'unda çalıştırır ([ci.md](/tr/operations/ci)): `libs/release` ve `agents/infra` ile
yan yana `gofmt`, `go vet` ve `go test`.
