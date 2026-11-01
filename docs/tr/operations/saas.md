# openlog'u SaaS olarak işletmek {#operating-openlog-as-saas}

openlog'u birçok ödeme yapan organizasyon için çalıştırmanın yolu: planlar, kotalar, kullanım, tenant başına retention ve
faturalama entegrasyonu. Kontrat [docs/contracts/usage.md](/tr/contracts/usage); kararlar D-079–D-081.

Kendi sunucunda çalışan kurulumların bunların hiçbirine ihtiyacı yok: kullanım ölçümü her zaman açıktır ve Settings → Usage &
plan sayıları yerleşik `unlimited` planına göre gösterir; hiçbir şey asla reddedilmez.

## 1. Kontrol listesi {#_1-checklist}

| Ayar | Neden |
|---|---|
| `OPENLOG_AUTH_MODE=postgres` | SaaS modu için gerekli |
| **ingest, api ve migrate** üzerinde `OPENLOG_SAAS_MODE=true` (allinone: bir kez) | sert kota uygulaması, tenant başına retention varsayılanı |
| **api ve migrate** üzerinde `OPENLOG_PLANS_FILE=/etc/openlog/plans.json` (veya `OPENLOG_PLANS`) | plan kataloğu; migrate'in tablo TTL'leri için gerekir (§4) |
| `OPENLOG_DEFAULT_PLAN=free` | yeni kayıtların planı |
| api üzerinde `OPENLOG_SUPERADMIN_EMAILS=ops@example.com,…` | plan atayabilecekler |
| api üzerinde `OPENLOG_SMTP_*`, `OPENLOG_PUBLIC_URL` | %80 / %100'de owner e-postaları |
| `OPENLOG_SIGNUP_ENABLED=true`, `OPENLOG_KEY_HASH_SECRET`, kayıt koruması | çok tenant'lı kayıt (config.md) |
| `OPENLOG_CLICKHOUSE_READ_USER` | yazma yetkisi olmayan tenant sorguları (D-047) |
| `OPENLOG_BILLING_PROVIDER` | bir sağlayıcı uygulanana kadar `none` (§6) |
| api üzerinde `OPENLOG_SAAS_SIGNUP_TRIAL_PLAN=pro` (`trial_days` olan plan) | yeni kayıtlar için otomatik deneme (§9) |
| api üzerinde `OPENLOG_SAAS_AUTO_SUSPEND` (varsayılan `false`) | yalnızca işaretlenen organizasyonlar insan müdahalesi olmadan askıya alınacaksa (§10) |

Operatör konsolunu, askıya almayı ve host limitlerini yayına almadan önce PostgreSQL migration `0065_saas_operator`'ı uygula.

Yeni binary'leri yayına almadan önce ClickHouse şeması `0050_usage` ve PostgreSQL migration `0035_usage_plans`'ı openlog-migrate
ile uygula (ikisi de yalnızca expand; eski binary'ler onları yok sayar).

## 2. Planlar {#_2-plans}

Kataloğu JSON olarak yaz (biçim ve alanlar: usage.md §4.1). Fiyatları faturalama sağlayıcısında tut; openlog'un yalnızca limitlere
ve `billing.plan_ref` değerine ihtiyacı var.

```json
{
  "default": "free",
  "plans": [
    {"id": "free", "name": "Free",
     "limits": {"ingest_gb_month": 100, "hosts": 5, "users": 3,
                "retention_days": {"logs": 7, "traces": 7, "metrics": 30},
                "ingest_bytes_per_second": 2097152, "ingest_burst_bytes": 20971520},
     "enforcement": {"hard_ingest_limit": true, "grace_percent": 10}},
    {"id": "pro", "name": "Pro",
     "limits": {"ingest_gb_month": 1000, "retention_days": {"logs": 30, "traces": 14, "metrics": 90},
                "query": {"max_memory_usage": 8589934592}},
     "enforcement": {"hard_ingest_limit": true, "grace_percent": 20},
     "billing": {"plan_ref": "price_pro_monthly"}},
    {"id": "enterprise", "name": "Enterprise", "limits": {"retention_days": {"logs": 90}}}
  ]
}
```

Katalog açılışta okunur: değiştirmek için api'yi yeniden dağıt (ingest'in buna ihtiyacı yok, ingest değerlendirilmiş durumu
PostgreSQL'den okur). Organizasyonların kullandığı bir plan id'sini kaldırmak onları varsayılan plana düşürür.

### Plan ve override atama {#assigning-plans-and-overrides}

Superadmin'ler organizasyona geçmişken Settings → Usage & plan → "Plan assignment (operator)" ekranını veya API'yi kullanır:

```sh
curl -s -b cookies.txt -H 'X-CSRF-Token: …' -H 'Content-Type: application/json' \
  -X PUT https://openlog.example.com/api/v1/admin/orgs/acme/plan \
  -d '{"plan_id":"pro","overrides":{"ingest_gb_month":2500,"hosts":400},"note":"contract #42"}'
```

`{org}` organizasyon id'si veya tenant id'sidir. Her değişiklik organizasyonun audit log'una yazılır (`plan.update`).

## 3. Kotalar ve uygulanması {#_3-quotas-and-enforcement}

- Api lideri her dakika her organizasyonu değerlendirir ve sonucu `tenant_quota_status` tablosuna yazar.
- Ingest pod'ları bu tabloyu 30 saniyede bir yeniden yükler. `ingest_gb_month × (1 + grace_percent/100)` aşıldığında
  `hard_ingest_limit` olan plan `Retry-After: 300` ile `429` / `RESOURCE_EXHAUSTED` alır; ajanlar denemeye devam eder ve plan
  yükseltilince (yaklaşık 1,5 dakika içinde) veya ay dönünce devam eder.
- Rate limit'ler pod başınadır (`rate / live ingest pods`). `openlog_quota_ingest_pods` değerinin ingest replica sayınla
  eşleştiğini kontrol et; bağlantıları eşit dağıtmayan bir load balancer varsa (uzun ömürlü gRPC bağlantıları)
  `OPENLOG_QUOTA_INGEST_PODS` değerini açıkça ayarla veya L7 gRPC dengeleme kullan.
- PostgreSQL 15 dakikadan uzun süre erişilemezse ingest fail-open davranır.

### Runbook: bir tenant'ın engelini hemen kaldır {#runbook-unblock-a-tenant-immediately}

1. Limiti yükselt: `{"plan_id": "<current>", "overrides": {"ingest_gb_month": <higher>}}` ile `PUT /api/v1/admin/orgs/<tenant>/plan`
   (veya `"hard_ingest_limit": false`).
2. Sonraki değerlendirmeyi (`OPENLOG_USAGE_EVALUATION_INTERVAL`, 1m) ve ingest yenilemesini (30s) bekle.
3. Organizasyon olarak `GET /api/v1/usage/status` çağrısının `"ingest_blocked": false` gösterdiğini ve
   `openlog_quota_ingest_rejected_total{reason="quota_exceeded"}` değerinin artmayı bıraktığını doğrula.

### Runbook: reddedilen tenant'ları bul {#runbook-find-the-tenants-being-rejected}

```sql
-- PostgreSQL
SELECT tenant_id, plan_id, level, ingest_bytes / 1073741824.0 AS gib, ingest_limit_bytes / 1073741824.0 AS limit_gib, evaluated_at
FROM tenant_quota_status WHERE ingest_blocked OR level <> 'ok' ORDER BY level DESC, ingest_bytes DESC;
```

## 4. Tenant başına retention {#_4-per-tenant-retention}

`OPENLOG_QUOTA_RETENTION_ENABLED=true` ile (SaaS modunda varsayılan):

1. openlog-migrate ham tabloların silme TTL'sini her sinyal (log, trace, metrik) için en uzun plan retention'ına yükseltir.
   Uygulamadan önce `ALTER TABLE … MODIFY TTL` ifadelerini görmek için `openlog-migrate -plan` çalıştır; `openlog-admin storage status`
   bekleyen TTL değişikliklerini gösterir. **Diski planla:** en uzun plan retention'ı artık silme job'u kaldırana kadar her tenant'ın
   ham verisine uygulanır.
2. Api lideri daha kısa retention'lı tenant'lar için partition kapsamlı
   `ALTER TABLE … DELETE IN PARTITION ID … WHERE tenant_id IN (…)` mutation'ları gönderir (saatlik çalıştırma başına en fazla
   `OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS`).

Mutation'ları izle:

```sql
SELECT table, command, create_time, parts_to_do, is_done, latest_fail_reason
FROM clusterAllReplicas('openlog', system.mutations) WHERE database = 'openlog' AND NOT is_done;
```

Uzun bir mutation birikmesi, çok fazla farklı retention veya bir plan düşürmenin yetişme sürecine işaret eder; çalıştırma başına limiti
yalnızca merge'ler yetişiyorsa yükselt. Özelliği sonradan kapatmak, migrate onsuz çalışana kadar genişletilmiş TTL'yi yerinde bırakır.

## 5. Kullanım verisi {#_5-usage-data}

- Ölçüm tabloları 400 gün saklanır (ClickHouse'ta `usage_*`, usage.md §2).
- Sorgu hesaplaması `system.query_log` üzerinden gelir: `query_log`'u her sunucuda en az 4 saat retention ile açık tut ve yazan
  kullanıcıya okuma izni ver.
- Export'lar: `GET /api/v1/usage/export?period=2026-09&format=csv` (organizasyon admin'leri/owner'ları), sağlayıcı faturalarını
  mutabakat için fatura dönemi kaydıdır.

## 6. Faturalama sağlayıcısı {#_6-billing-provider}

Sağlayıcı henüz seçilmedi. `internal/billing` openlog'un neye ihtiyacı olduğunu tanımlar; `OPENLOG_BILLING_PROVIDER=noop` günlük push'u ve
muhasebesini (`billing_usage_pushes`) harici çağrı yapmadan çalıştırır.

### Sağlayıcı eklemek (Stripe, iyzico, Paddle, …) {#adding-a-provider-stripe-iyzico-paddle}

1. `billing.Provider`'ı `internal/billing/<provider>` içinde uygula (sağlayıcının secret key'i `OPENLOG_BILLING_<PROVIDER>_*`
   değişkenlerinden gelen bir HTTP client; asla repository'de değil):
   - `EnsureCustomer`: organizasyon için müşteriyi oluştur/bul (id'sini `PUT /api/v1/admin/orgs/{org}/plan`
     `billing.customer_id` ile sakla veya kayıt/checkout akışından çağır).
   - `PushUsage`: bir gün için `ingest_gb`, `hosts`, `users` bildir ve `UsageRecord.IdempotencyKey` değerini sağlayıcının idempotency
     key'i olarak geçir (Stripe: meter event `identifier`; Paddle: transaction/adjustment custom data; iyzico'da metered billing yok,
     fatura tutarını export'tan hesapla ve `ErrNotSupported` dön).
   - `ParseWebhook`: imza header'ını webhook secret'ıyla doğrula ve sağlayıcı olaylarını `subscription.updated` (`PlanRef` = price/plan
     id ile), `subscription.canceled`, `payment.failed`, `invoice.paid` olaylarına eşle.
2. Adı `billing.New` ve `config.validateUsage` içine kaydet (`OPENLOG_BILLING_PROVIDER`).
3. Sağlayıcının price/plan id'lerini kataloğa `billing.plan_ref` olarak yaz.
4. Sağlayıcının webhook'unu `https://<api>/api/v1/billing/webhooks/<provider>` adresine yönelt.
5. Arayüze bir checkout/customer-portal bağlantısı ekle (uygulanmadı; doğal yeri Settings → Usage & plan).

## 7. Alarm kurulacak metrikler {#_7-metrics-to-alert-on}

| Metrik | Ne zaman alarm |
|---|---|
| `openlog_quota_evaluations_total{result="error"}` | 10 dakika boyunca artıyorsa (limitler bayat; engellenen tenant'lar bir saate kadar engelli kalır) |
| `openlog_quota_ingest_rejected_total` | ani sıçramalar (büyük bir müşteri kotasına ulaştı) |
| `openlog_quota_ingest_pods` | ingest replica sayısından farklıysa |
| `openlog_processor_rows_inserted_total{table="usage_ingest_1h"}` | diğer tablolara yazılırken sıfırsa (ölçüm bozuk) |
| `openlog_saas_ingest_rejected_total{reason="org_suspended"\|"host_limit"}` | yeri olması gereken bir müşterinin host limiti reddedilmeleri (bayat host eşitlemesi) |

## 8. Operatör konsolu (D-105) {#_8-operator-console-d-105}

Superadmin'ler (`OPENLOG_SUPERADMIN_EMAILS`, doğrulanmış adresle giriş yapmış) navigasyonda **Operator** görür (`/operator`); başka kimse
görmez ve API `403` döner. Konsolun üç sekmesi var:

- **Organizations**: ada, tenant id'sine, organizasyon id'sine veya üye e-postasına göre ara; geçerli plana ve duruma (active, trial,
  suspended, flagged) göre filtrele; oluşturma, ad, bu dönemki ingest, üye sayısı veya son ingest'e göre sırala. Sayılar PostgreSQL'den
  gelir (son kota değerlendirmesi, lisans anahtarının `last_used_at` değeri), yani liste çok tenant'la da hızlı kalır.
- **Organization detail**: üyeler (rol, son giriş, doğrulanmış), plan ve override'lar (mevcut plan editörü), deneme (plan seçici yalnızca
  `trial_days` olan planları listeler, `GET /api/v1/plans` üzerinden), organizasyon silme (§12), geçerli dönemin kullanım grafiği, kota durumu,
  en yeni 50 audit olayı, SSO bağlantıları (protokol, etkin, zorunlu, JIT, son test), lisans anahtarı/API key/SCIM token sayıları ve doğrulanmış
  alan adları, flag'ler ve destek oturumları. Hiçbir secret, anahtar değeri, SSO yapılandırması veya IP adresi gösterilmez.
- **Flagged**: dismiss / actioned (not zorunlu) seçenekleri ve organizasyon bağlantılarıyla açık kötüye kullanım flag'leri (§10).

Her işlem onay ve neden ister; neden, müşterinin admin'lerinin okuyabildiği organizasyon audit log'una yazılır:

| İşlem | Etkisi |
|---|---|
| Suspend | `OPENLOG_QUOTA_REFRESH_INTERVAL` içinde ingest `403 org_suspended`; arayüz banner gösterir ve salt okunur olur (dashboard, alarm, fleet, entegrasyon ve ayarların oluşturma/düzenleme/silme işlemleri gizlenir veya açıklamayla devre dışı kalır; `403 org_suspended` dönen bir mutation durumu anında yeniden çeker); üyelerin değişiklikleri reddedilir (`403 org_suspended`) ama giriş yapabilir, sorgulayabilir ve verilerini export edip silebilirler |
| Unsuspend | işlemi yapan api pod'unda hemen, diğerlerinde 15 saniye içinde normale döner |
| Change plan / overrides | `PUT /api/v1/admin/orgs/{org}/plan` (§2) |
| Start / extend trial | §9 |
| Reset quota notifications | geçerli dönemin %80/%100 e-postaları yeniden gönderilebilir (örneğin limit yükseltildikten sonra) |
| Force logout | üyelerin her aktif oturumu iptal edilir (diğer organizasyonlarındakiler de); API key'ler çalışmaya devam eder, gerekirse organizasyon olarak iptal et |
| Resend verification | adresi onaylanmamış her owner'a yeni bir doğrulama e-postası |

### Runbook: kötüye kullanım yapan bir organizasyonu askıya al {#runbook-suspend-an-abusive-organization}

1. Organizasyonu **Flagged** üzerinden aç veya ara; kullanımı, son audit kayıtlarını ve flag'leri kontrol et.
2. Ticket'ı belirten bir nedenle **Suspend** yap. Ingest yaklaşık 30 saniye içinde durur;
   `openlog_saas_ingest_rejected_total{reason="org_suspended"}` değerinin arttığını doğrula.
3. Kimlik bilgileri ele geçirilmiş olabilirse **Force logout** yap; owner'dan lisans anahtarlarını yenilemesini iste.
4. Flag'i ticket numarasıyla **actioned** olarak işaretle.

## 9. Denemeler (D-106) {#_9-trials-d-106}

Denenebilecek planlara `trial_days` (ve isteğe bağlı `trial_fallback_plan`) ekle:

```json
{"id": "pro", "name": "Pro", "trial_days": 14, "trial_fallback_plan": "free", "limits": {"ingest_gb_month": 1000}}
```

- **Operatör**: organization detail → *Start trial* (plan, gün) planı atar ve bitişi kaydeder; *Extend trial* bitişi ileri alır (günler eklenir).
  Audit `plan.update`, `trial.start`, `trial.extend`.
- **Kayıt**: `OPENLOG_SAAS_SIGNUP_TRIAL_PLAN=pro` ile, api bu ayarla ilk çalıştıktan sonra oluşturulan ve plan ataması olmayan organizasyonlar
  `OPENLOG_SAAS_LIFECYCLE_INTERVAL` içinde denemeye başlar (o zamana kadar varsayılan plandadırlar). Mevcut organizasyonlar otomatik olarak asla
  denemeye alınmaz.
- **E-postalar**: owner'lar bitişten `OPENLOG_SAAS_TRIAL_NOTIFY_DAYS` (7, 3, 1) gün önce TR/EN hatırlatma ve bittiğinde bir bildirim alır
  (`OPENLOG_SMTP_*` gerekir; SMTP olmadan denemeler yine biter). Gönderilemeyen hatırlatma sonraki çalıştırmada yeniden denenir; denemeyi uzatmak
  hatırlatmaları yeniden kurar.
- **Bitiş**: bir operatör deneme sırasında başka bir plan atamadıysa lider `trial_ended_at` ayarlar ve fallback planı atar (override'lar ve faturalama
  id'leri kalır). Audit `trial.end` (actor `system:trial`). Settings → Usage & plan banner'ı süren denemeyi gösterir.

Runbook: *müşteri deneme sırasında ödeme yaptı*: ücretli planı `PUT /api/v1/admin/orgs/{org}/plan` ile ata; deneme yine tarihinde biter ama atanan
planı korur.

## 10. Kötüye kullanım tespiti (D-106) {#_10-abuse-detection-d-106}

Api lideri her `OPENLOG_SAAS_ABUSE_INTERVAL` (10 dk) aralığında her organizasyonu değerlendirir ve flag kaldırır:

| Tür | Koşul (varsayılanlar) |
|---|---|
| `ingest_spike` | önceki veya geçerli saatin ingest'i > `OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER` (10) × `ingest_gb_month / 730`; ingest limiti olmayan planlar atlanır |
| `new_org_hosts` | `OPENLOG_SAAS_ABUSE_NEW_ORG_DAYS` (7) günden genç ve `OPENLOG_SAAS_ABUSE_NEW_ORG_HOSTS` (50) üstü aktif host'u olan organizasyon |
| `ingest_source_ips` | tek ingest instance'ında bir saat içinde `OPENLOG_SAAS_ABUSE_SOURCE_IPS` (200) üstü farklı istemci adresinden kullanılan lisans anahtarları |

- Organizasyon ve tür başına bir açık flag; tekrar eden tespitler `occurrences`, `details` ve `last_seen_at` değerlerini günceller.
- İstemci adreslerini her ingest pod'u bellekte sayar; yalnızca saatlik sayı yazılır (`ingest_source_counts`, 48 saat). Ingest karşı taraf adresini
  görür: istemci adreslerini korumayan bir load balancer arkasında (PROXY protokolü / L4 pass-through yok) sayı, balancer adreslerinin sayısıdır ve bu
  flag hiç tetiklenmez. Sayılar instance başınadır, yani N pod arasında istek düzeyinde dengelemede değer bir alt sınırdır.
- `OPENLOG_SAAS_AUTO_SUSPEND=true` olmadıkça hiçbir şey askıya alınmaz; olduğunda **yeni** bir flag organizasyonunu askıya alır (audit `org.suspend`,
  `system:abuse-detector` tarafından, flag `auto_suspended`). Otomatik askıya almaları her gün gözden geçir: eşikler kaba (büyük bir fleet ekleyen müşteri
  `new_org_hosts`'u tetikler).
- Mevcut kayıt koruması (IP/e-posta başına rate limit, CAPTCHA, geçici alan adı engel listesi, e-posta doğrulama; D-044–D-046) ilk savunma hattı olarak
  kalır; tespit, oradan geçeni yakalar.

## 11. Destek erişimi (D-105) {#_11-support-access-d-105}

openlog personeli müşteri kimlik bilgilerini asla kullanmaz. Bir **owner**, Settings → Organization → *openlog support access* altından desteğe 24 saat
veya 7 gün izin verir (audit `support_access.grant`) ve istediği zaman iptal edebilir (`support_access.revoke`, açık destek görünümlerini de bitirir).

Erişim verilmişken operatör organizasyonu açar → bir nedenle *Open support view* (audit `support.session_start`). Tarayıcı organizasyonun salt okunur bir
görünümüne geçer; kırmızı bir banner organizasyonu, kalan süreyi ve *Exit support view* seçeneğini gösterir:

- Operatör **viewer** olarak davranır: dashboard'lar, host'lar, loglar, trace'ler, alarmlar vb. görünür; viewer'ın olağan self-servis işlemleri dahil her
  değişiklik reddedilir (`403 support_read_only`). Sorgular ve önizlemeler çalışır.
- Görünüm operatörün kendi oturumuna bağlıdır ve `OPENLOG_SAAS_SUPPORT_SESSION_TTL` (2 saat) sonra, izin bittiğinde veya iptal edildiğinde ya da operatör
  çıkış yaptığında sona erer.
- Operatörün açtığı her sayfa (`support.page_view`) ve her farklı API isteği (`support.request`, method ve path) organizasyonun audit log'una yazılır, yani
  müşteri desteğin tam olarak neye baktığını görür.

Operatörler için güvenlik notları: `OPENLOG_SUPERADMIN_EMAILS` listesini kısa tut, mümkünse bu hesaplar için MFA'lı SSO zorunlu kıl ve operatör konsoluna
production veritabanı erişimi gibi davran.

## 12. Veri sahibi talepleri (KVKK/GDPR; D-107) {#_12-data-subject-requests-kvkk-gdpr-d-107}

Müşteriler haklarını kendileri kullanır; operatörler yalnızca kötüye kullanımda veya müşteri giriş yapamadığında devreye girer. Aşağıdakilerin hepsi her
postgres modlu kurulumda çalışır (`OPENLOG_DATA_EXPORT_*`, `OPENLOG_ORG_DELETION_GRACE`, config.md "Data subject requests and status page"). E-postalar
için `OPENLOG_SMTP_*` ve `OPENLOG_PUBLIC_URL` gerekir.

### Veri taşınabilirliği: export'lar {#data-portability-exports}

| Talep | Kim | Nerede | İçerik |
|---|---|---|---|
| Organizasyon export'u | owner'lar | Settings → Organization → *Data export* (`POST /api/v1/data-exports`) | `organization/*.json`: organizasyon, üyeler, davetler, lisans/API key metadata'sı, dashboard'lar, raporlar, alarm kuralları, kanallar (secret yok; maskelenmiş ipuçları), susturmalar, olaylar, SSO bağlantıları/alan adları/rol eşlemeleri/SCIM token metadata'sı (client secret veya anahtar yok), plan ve limitler, audit log; isteğe bağlı olarak bir zaman aralığının telemetrisi (log, trace, metrik) `telemetry/<signal>/<day>-NNNN.ndjson.gz` olarak |
| Kişisel export | her kullanıcı | Settings → Profile → *Your data* (`POST /api/v1/account/data-exports`) | `user/*.json`: profil, üyelikler, oturum metadata'sı (IP, user agent, zamanlar; token yok), oluşturulan API key'ler, adrese gelen davetler, SCIM kimlikleri, SSO oturumları, yorumlar, kullanıcının yaptığı veya onunla ilgili audit kayıtları |

- Api lideri aynı anda bir export oluşturur: `manifest.json` (dosyalar, satır sayıları, limitler, `truncated`, sinyal başına `complete_until`) içeren bir ZIP
  arşivi. Telemetri ClickHouse'tan gün gün `max_threads=2` ve `OPENLOG_DATA_EXPORT_ROWS_PER_SECOND` ile aktarılır; `OPENLOG_DATA_EXPORT_MAX_ROWS` veya
  `OPENLOG_DATA_EXPORT_MAX_BYTES` sınırında düzgünce durur (müşteri kalanını daha kısa aralıkla ister). Aralık `OPENLOG_DATA_EXPORT_MAX_RANGE` ile sınırlıdır.
  Organizasyon/kullanıcı başına bir bekleyen export, 24 saatte en fazla 5.
- Depolama: S3 (`OPENLOG_DATA_EXPORT_S3_URL` veya `openlog-exports/` altında tiered storage bucket'ı) ya da yerel dizin (`OPENLOG_DATA_EXPORT_LOCAL_PATH`; tek api
  pod'u veya paylaşılan volume). Güvenlik ağı olarak bucket önekine `OPENLOG_DATA_EXPORT_TTL` süresinden biraz uzun bir lifecycle kuralı ver.
- S3 kimlik bilgileri: statik anahtarlar veya (`OPENLOG_DATA_EXPORT_S3_CREDENTIALS=auto`, anahtar yoksa varsayılan) bir IAM rolü (D-116): EKS IRSA (api service
  account'una `eks.amazonaws.com/role-arn` annotation'ı ekle), EKS Pod Identity, ECS task role veya EC2 instance profile (IMDSv2; EKS node'larında hop limit pod'ların
  ulaşmasına izin vermeli). Rolün export öneki üzerinde `s3:PutObject`, `s3:GetObject` ve `s3:DeleteObject` izni olmalı. Credentials dosyaları, SSO ve
  `credential_process` okunmaz.
- Talep eden, `OPENLOG_DATA_EXPORT_TTL` süresine kadar geçerli bir indirme bağlantısı içeren e-posta alır (`/api/v1/data-exports/download?token=…`, 256-bit rastgele
  token, yalnızca hash'i saklanır); owner'lar ayarlar sayfasından da indirebilir. Arşivler süreleri dolunca silinir. Audit: `data_export.request`,
  `data_export.download` (organizasyon), `user.data_export_request` (kurulum düzeyi).

### Silme: hesap silme {#erasure-account-deletion}

Settings → Profile → *Delete account* (`POST /api/v1/account/delete`): kullanıcı e-posta adresini ve parolasını yazar (parolası olmayan kullanıcılar, yani single
sign-on kullananlar, yeniden giriş yapıp 10 dakika içinde onaylar). Kullanıcı bir organizasyonun tek owner'ıysa reddedilir; önce başka bir owner ekler veya organizasyonu
siler. Tek bir transaction'da hesap, oturumları, SSO/SCIM kimlikleri, oluşturduğu API key'ler, davetler ve kişisel export'lar kaldırılır, adres rapor alıcı listelerinden
çıkar ve audit/olay/yorum kayıtları ad, adres ve IP yerine rastgele bir takma ad (`deleted-user-<hex>`) tutar (ayrıntılar: postgres.md "Account deletion"). Eski adrese
bir onay e-postası gider; bir silme sertifikası (`subject_type=user`, kullanıcı id'sinin sha256'sı) kaydedilir.

### Silme: organizasyon silme {#erasure-organization-deletion}

1. Bir owner Settings → Organization → *Delete organization* ekranını açar, organizasyon adını yazar ve parolayla (veya yakın zamanlı bir SSO girişiyle) onaylar.
   Organizasyon hemen soft-delete edilir: üyeler erişimi kaybeder, API key'ler ve lisans anahtarları çalışmayı bırakır (ingest iptal edilen anahtarlara `org_deleted`
   nedeniyle `403` döner, purge'den sonra 365 gün daha, böylece çalışır halde kalan ajanlar net bir hata gösterir; D-115), SCIM token'ları iptal edilir, alarmlar artık
   değerlendirilmez ve raporlar artık gönderilmez. Owner'lar e-posta alır.
2. `OPENLOG_ORG_DELETION_GRACE` (varsayılan 7 gün) süresince herhangi bir owner Settings → Profile altından iptal edebilir; silmenin iptal ettiği anahtarlar yeniden çalışır.
3. Bekleme süresinden sonra api lideri tenant'ın satırlarını **`tenant_id` kolonu olan her ClickHouse tablosundan** (ham sinyaller, rollup'lar, envanterler, container'lar,
   Kubernetes, `usage_*`) partition kapsamlı `ALTER TABLE … ON CLUSTER … DELETE IN PARTITION ID … WHERE tenant_id = …` mutation'larıyla siler (çalıştırma başına ≤ 50),
   bunları bekler, yeniden sayar ve sıfır satır şart koşar. S3 üzerindeki part'lar (tiered storage) S3'te yeniden yazılır ve eski nesneler eski part'larla birlikte kaldırılır.
   Sonra tek bir PostgreSQL transaction'ı organizasyonu (`org_id` değerine sahip tüm satırlar) ve artık başka bir organizasyona ait olmayan hesapları (hesap silmedeki gibi) siler
   ve silme sertifikasını kaydeder (`subject_type=organization`, tenant id'sinin sha256'sı, tablo başına satır sayıları, zaman damgaları, `verified`). Owner'lar tamamlanma
   e-postası alır; silme satırı kişisel veri tutmaz.

Operatörler kötüye kullanım yapan organizasyonları `POST /api/v1/admin/orgs/{org}/deletion` `{"reason", "immediate"?}` ile siler (superadmin; `immediate` bekleme süresini atlar;
owner'lar bilgilendirilir ama iptal edemez; kurulum düzeyinde audit `admin.org_deletion_schedule`). `GET /api/v1/admin/org-deletions` ilerlemeyi ve `last_error`'ı gösterir;
`POST /api/v1/admin/org-deletions/{id}/cancel` bekleme süresinde iptal eder. Operatör konsolunun organization detail ekranında *Organization deletion* altında aynı kontroller
var: zamanlama (neden zorunlu; *Delete immediately* ayrıca tenant id'sinin yazılmasını ister), purge zamanı, başlatan, neden ve son hatayla bekleyen silme, iptal, önceki talepler
ve tenant'ın silme sertifikaları (tenant id'sinin sha256'sıyla eşleşir).

Çalışan bir silmeyi izle:

```sql
SELECT table, partition_id, command, parts_to_do, is_done, latest_fail_reason
FROM clusterAllReplicas('openlog', system.mutations) WHERE database = 'openlog' AND NOT is_done AND position(command, '<tenant_id>') > 0;
```

### E-postayla gelen talepler için prosedür {#procedure-for-requests-that-arrive-by-e-mail}

1. Talep edenin hesabı kontrol ettiğini veya organizasyonun owner'ı olduğunu doğrula (kayıtlı adresten cevap veya giriş yaptırma). Talebi tarihiyle ticket sistemine kaydet (KVKK:
   30 gün içinde cevap; GDPR: bir ay).
2. Erişim/taşınabilirlik: export düğmelerini kullanmalarını iste; giriş yapamıyorlarsa ancak kimlik doğrulamasından sonra operatör olarak giriş yap ve bir owner'ın destek erişimini
   kullan (§11), asla onların kimlik bilgilerini değil.
3. Hesap silme: kendileri siler; giriş yapamıyorlarsa girişi kapat ve organizasyonlarında kalan bir owner üzerinden zamanla. Organizasyon silme: bir owner siler veya bir operatör,
   `reason` olarak ticket referansıyla admin endpoint'ini kullanır.
4. Onay olarak `GET /api/v1/admin/deletion-certificates?subject_hash=<sha256>` çıktısındaki sertifika id'sini ve `subject_hash` değerini gönder.

### Hemen silinmeyenler ve sertifikaların saklanması {#what-is-not-deleted-immediately-and-retention-of-certificates}

- **Kafka**: ham OTLP kayıtları `OPENLOG_KAFKA_RETENTION_MS` (varsayılan 24 saat) dolana kadar kalır; bekleme süresini Kafka retention'ından uzun tut ki tenant'ın yolda olan hiçbir
  kaydı ClickHouse silmesinden sonra yazılmasın.
- **Yedekler** (`pg_dump`, ClickHouse yedekleri, S3 versioning): silinen veri, yedekler dönene kadar yedeklerde kalır; yedek saklama süresini gizlilik bildiriminde belirt ve silinmiş
  bir tenant'ı yedekten geri yükleme.
- **Detach edilmiş ClickHouse part'ları** (elle `DETACH`): job bunları görmez; alışılmadık bir kümeyi sertifikalandırmadan önce `system.detached_parts` tablosunu kontrol et.
- **Operatör kayıtları**: kurulum düzeyindeki audit kayıtları (`admin.*`, takma adla `user.delete`) ve sertifikalar uyum kanıtı olarak saklanır; sertifikalar yalnızca hash, sayı ve
  zaman damgası içerir. Silmeyi gösterebilmen gerektiği sürece sakla (yasal zamanaşımı süresini öneriyoruz, örneğin Türk ticaret hukukunda 10 yıl), sonra `deletion_certificates`
  satırlarını elle sil.

## 13. Durum sayfası (D-108) {#_13-status-page-d-108}

`OPENLOG_STATUS_PAGE_ENABLED` (varsayılan: `OPENLOG_SAAS_MODE`) herkese açık `/status` sayfasını ve `GET /api/v1/status` adresini sunar (kimlik doğrulama yok,
`Cache-Control: public, max-age=30`, CORS `*`). Hiçbir tenant verisi göstermez.

- **Bileşenler**, api liderinin dakikalık öz kontrollerinden gelir: `ingest` (`component_heartbeats` içindeki canlı ingest/allinone instance'ları), `query_api` (PostgreSQL ve ClickHouse
  erişilebilir), `alerting` (canlı değerlendiriciler; etkin kuralların %10'undan fazlası 5 dakikadan fazla gecikmişse degraded), `processing` (canlı processor'lar; saklanan en yeni metrik
  10 dakikadan eskiyse degraded, 30 dakikadan sonra partial outage). 5 dakikadan eski bir snapshot `unknown` gösterir.
- **Uptime**: `status_checks_daily` üzerinden bileşen başına 90 gün (herhangi bir kontrol partial veya major outage bildirdiyse o gün kesinti günüdür; uptime = kesintisiz kontroller /
  kontroller).
- **Olaylar ve bakım**: superadmin'ler bunları Settings → Status page altından (veya `/api/v1/admin/status/incidents`) yönetir: bileşenler, etki ve ilk mesajla oluştur, zaman çizelgesi
  güncellemeleri ekle, çöz. Aktif bakım pencereleri etkilenen bileşenleri `maintenance` olarak gösterir; açık olaylar genel durumu etkiye göre yükseltir (minor → degraded, major → partial
  outage, critical → major outage). Çözülen kayıtlar sayfada 14 gün kalır.
- Sayfayı proxy'n üzerinden ayrı bir host adına koy (`status.example.com` → `/status`) ve tam kesintiler için küme dışında statik bir yedek tut: sayfanın kendisini openlog sunar.
