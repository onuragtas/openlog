# Sözleşme: Sorgu ve yönetim API'si (M1) {#contract-query-and-management-api-m1}

Temel yol `/api/v1`. Yanıtlar JSON'dır. Zamanlar RFC3339 veya unix milisaniye olarak kabul edilir, nanosaniyeli RFC3339 (UTC) olarak döner.
Makinenin okuyabileceği spec: [openapi.yaml](https://github.com/onuragtas/openlog/blob/master/docs/contracts/openapi.yaml). Veri modeli: [postgres.md](/tr/contracts/postgres).

Hatalar: HTTP durum kodu + `{"error": {"code": "invalid_argument", "message": "…"}}`. Kodlar: `invalid_argument` (400),
`unauthenticated` (401), `permission_denied` (403), `not_found` (404), `already_exists` (409),
`failed_precondition` (409), `resource_exhausted` (429, `Retry-After` ile), `internal` (500),
`unavailable` (503, `Retry-After` ile; mesajı veritabanı şemasının build'in gerisinde kaldığını söylüyorsa bir backend düşmemiş, bir migration atlanmış veya başarısız olmuştur: `openlog-migrate` çalıştırıp yeniden dene, bkz. [releases-updates.md](/tr/contracts/releases-updates) §6), `storage_unavailable` (503, `Retry-After` ve `"retryable": true` ile: bir
telemetri sorgusu storage'dan veri okuyamadı, genelde tiered storage sonrası S3'teki part'lar söz konusudur; sonra yeniden dene veya daha
yakın bir aralığı sorgula, [config.md](/tr/contracts/config) "ClickHouse read-only user and per-tenant query limits"), `timeout` (504).

## Listeler {#lists}

Bir sayfaya sığmayacak kadar büyüyebilen listeler `sort`, `dir` (`asc` veya `desc`), `limit` ve `offset` alır ve `has_more` ile yanıt verir. Sıralama sunucuda yapılır, çünkü tarayıcı yalnızca kendisine gönderilen sayfayı sıralayabilir. `sort` o listenin belgelediği adlardan biridir, başka her şey `400` döner: ad bir sütun seçer ve asla bir sorgu ifadesine ulaşmaz. `dir` verilmezse her sütunun doğal bir yönü vardır. Toplam sayı yoktur, çünkü gruplanmış bir listeyi saymak listenin tamamını okumak demektir: `has_more`, bir sayfadan bir satır fazla istenerek öğrenilir. `offset` 100000'de durur, çünkü ClickHouse ondan öncekilerin hepsini okuyup atar, o noktadan sonra filtre daha iyi bir araçtır. Loglar ve trace'ler ise okunurken değiştikleri için `cursor` ile sayfalanır. Yeni veri geldiğinde bir satır sayfalar arasında tekrar edebilir veya atlanabilir. Konsol, bir listenin sıralamasını ve sayfasını adres çubuğunda tek bir `list` parametresinde tutar, böylece bağlantı aynı görünümü açar.

## Kimlik doğrulama {#authentication}

`OPENLOG_AUTH_MODE=postgres` (varsayılan). Aşağıdaki herkese açık endpoint'ler dışında her endpoint şunlardan birini ister:

| Kimlik bilgisi | Nasıl | Kimin yerine çalışır | Notlar |
|---|---|---|---|
| Oturum | `POST /auth/login`'den gelen `openlog_session` cookie'si | kullanıcının seçili organizasyondaki rolü | `HttpOnly`, `SameSite=Strict`, `Path=/api`, `Secure` (`OPENLOG_COOKIE_SECURE=false` değilse). Sunucu tarafında PostgreSQL'de durur; `OPENLOG_SESSION_TTL` dolunca veya `OPENLOG_SESSION_IDLE_TIMEOUT` kadar hareketsizlikten sonra, çıkışta, iptalde veya parola değişince (diğer oturumlar) biter |
| API anahtarı | `Authorization: Bearer ola_…` | anahtarın organizasyonundaki kendi rolü: `viewer` (varsayılan), `member` veya `admin` (D-133) | Aşağıdaki [Roller](#roles) bölümünde rolün izin verdiği her şey: telemetri, organizasyon yapılandırması, alerting, dashboard'lar, kayıtlı görünümler. Kimlik ve kimlik bilgileri (üyeler, davetler, lisans anahtarları, API anahtarları), kurulumun makineleri (fleet, backend güncellemeleri) ve kullanıcının kendi hesabıyla ilgili her şey, rol ne olursa olsun `403` döner. İptal anında geçerli olur |
| Cihaz oturumu | `POST /auth/device`'tan gelen `Authorization: Bearer olm_…` | cookie oturumu gibi giriş yapmış kişi ve rolü | Mobil konsol için ([11-mobile-console.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/11-mobile-console.md) §3.2). Aynı kullanıcı, aynı rol, aynı iptal: `GET /sessions` içinde `kind: device` ile listelenir ve orada iptal edilir. Ama yalnızca kullanıcıya özel her işlem, rol ne olursa olsun `403` döner: üyeler, davetler, lisans/API/tarayıcı anahtarları, source map'ler, fleet, backend güncellemeleri, sorgu limitleri, disk alanı ve kendi hesabını yönetme. `POST /auth/logout` serbesttir, çünkü çıkış yapmak yalnızca erişimi kaldırır. `X-CSRF-Token` gerekmez. `OPENLOG_DEVICE_SESSION_TTL` dolunca veya `OPENLOG_DEVICE_SESSION_IDLE_TIMEOUT` kadar hareketsizlikten sonra biter; ikisi de tarayıcınınkinden uzundur, çünkü telefon bir alert geldiğinde açılır, bu da genelde uzun bir sessizlikten sonra olur |

Ingest lisans anahtarları (`olk_…`) API kimlik bilgisi **değildir**, API anahtarları da ingest kimlik bilgisi değildir.

`openlog-mcp` ([mcp.md](/tr/operations/mcp), D-126) bir API anahtarı istemcisidir: aşağıdaki telemetri okumalarını
yapay zekâ araçlarına Model Context Protocol araçları olarak sunar, kendine ait endpoint veya kimlik bilgisi eklemez.

**CSRF.** `GET`/`HEAD`/`OPTIONS` dışında bir metot kullanan, cookie ile kimliği doğrulanmış istek, oturumun token'ını
`X-CSRF-Token` içinde göndermelidir (`/auth/login`, `/auth/signup`, `/invitations/accept` ve
`GET /auth/me` bunu `csrf_token` olarak döner); aksi halde `403 permission_denied`. Bearer istekleri buna ihtiyaç duymaz. JSON gövdeli endpoint'ler
`Content-Type: application/json` ister (yoksa `400`), bu da kimlik doğrulaması gerektirmeyen giriş
endpoint'lerini siteler arası form gönderimlerine karşı korur. API asla CORS header'ı göndermez.

**Organizasyon.** Her telemetri sorgusunun tenant'ı **yalnızca** kimliği doğrulanmış principal'dan alınır ve sorgu katmanı
tarafından eklenir; hiçbir endpoint tenant parametresi kabul etmez. Birden çok organizasyona üye bir kullanıcı,
istek başına `X-Openlog-Org-Id: <organization id>` header'ı ile birini seçer; header yoksa kullanıcının en eski
üyeliği kullanılır. Kullanıcının üyesi olmadığı bir organizasyonu gösteren header → `403`. API anahtarlarında header
isteğe bağlıdır ve anahtarın organizasyonuyla eşleşmelidir. Hiç üyeliği olmayan, giriş yapmış bir kullanıcı telemetri endpoint'lerinde `403` alır.

**Rate limit.** Başarısız parola kontrolleri (giriş, var olan bir kullanıcının daveti kabul etmesi, parola değişikliği)
PostgreSQL'de (e-posta, istemci IP'si) başına sayılır; `OPENLOG_LOGIN_WINDOW` içinde `OPENLOG_LOGIN_MAX_FAILURES` kadar başarısızlıktan sonra
istek, parola doğru olsa bile `429 resource_exhausted` alır. İstemci IP'si TCP karşı ucudur; karşı uç `OPENLOG_API_TRUSTED_PROXIES` içindeyse
`X-Forwarded-For`'daki en sağdaki güvenilmeyen adımdır. Bilinmeyen e-postalar ve
yanlış parolalar aynı sürede aynı `401`'i döner.

`OPENLOG_AUTH_MODE=static` (geliştirme/testler): `OPENLOG_LICENSE_KEYS` içindeki lisans anahtarları sorgu
endpoint'lerinde (`openlog-license-key` header'ı veya `Authorization: Bearer`) `viewer` olarak kimlik doğrular; telemetri endpoint'leri dışında yalnızca `GET /auth/config` ve
`GET /auth/me` vardır (aşağıdaki diğer tüm yollar → `404`).

## Roller {#roles}

Rolü hem üyelik **hem de** API anahtarı taşır (D-133) ve ikisine de tek bir matris karar verir: her handler
tek kapıdan, `auth.Allow`'dan (`internal/auth/roles.go`) geçer, başka hiçbir şey bir principal'ın türünü
veya rolünü karşılaştırmaz. Aşağıdaki **Anahtar** sütunu, rolü o satıra yeten bir API anahtarının işlemi yapıp yapamayacağını söyler; orada boş hücre,
işlemin anahtarın rolü ne olursa olsun giriş yapmış bir kullanıcı gerektirdiği anlamına gelir.

| İşlem | viewer | member | admin | owner | Anahtar |
|---|:-:|:-:|:-:|:-:|:-:|
| Telemetri, `GET /orgs/current`, `GET /members`, `GET /fleet/*`, `GET /onboarding` | ✓ | ✓ | ✓ | ✓ | ✓ |
| Kendi oturumları, organizasyondan ayrılma, kendi hesabı (çıkış, parola, dil, kişisel veri dışa aktarımı) | ✓ | ✓ | ✓ | ✓ | |
| `GET /license-keys`, `GET /api-keys` | | ✓ | ✓ | ✓ | ✓ |
| `POST /api-keys`, kendi API anahtarlarını iptal etme | | ✓ | ✓ | ✓ | |
| Alerting okumaları (`GET /alerts/*`) ve kural önizlemesi | ✓ | ✓ | ✓ | ✓ | ✓ |
| Alert kuralı ve mute oluşturma, **kendi** kural ve mute'larını değiştirme/silme, incident'ları onaylama/çözme/not ekleme | | ✓ | ✓ | ✓ | ✓ |
| APM hata gelen kutusu: durum/atanan kişi değiştirme (`PATCH /apm/errors/groups`), yorum yazma, **kendi** yorumlarını silme | | ✓ | ✓ | ✓ | ✓ |
| Herhangi bir hata grubu yorumunu silme | | | ✓ | ✓ | ✓ |
| Service level objective oluşturma, değiştirme ve silme (`/slos`) | | ✓ | ✓ | ✓ | ✓ |
| `GET /integrations/settings` | ✓ | ✓ | ✓ | ✓ | ✓ |
| OQL (`POST /query`, `POST /query/validate`, `GET /query/schema`), dashboard okuma (özel olanlar: yalnızca oluşturan), dışa aktarma | ✓ | ✓ | ✓ | ✓ | ✓ |
| Dashboard oluşturma, içe aktarma ve çoğaltma; **kendi** dashboard'larını değiştirme/silme | | ✓ | ✓ | ✓ | ✓ |
| Özel olmayan herhangi bir dashboard'u değiştirme/silme | | | ✓ | ✓ | ✓ |
| `PATCH /orgs/current` (APM Apdex ayarları ve tail sampling politikası dahil), `GET /audit-log`, entegrasyon ayarı değişiklikleri, herhangi bir alert kuralı veya mute, alert kanalları ve test gönderimleri | | | ✓ | ✓ | ✓ |
| Davetler, lisans anahtarı oluşturma/iptal etme, herhangi bir API anahtarını iptal etme, `viewer` üstünde API anahtarı oluşturma, rol değiştirme/üye çıkarma (owner'lar hariç), fleet değişiklikleri (`PUT /fleet/policy`, host override'ları, duraklatma/sürdürme, hemen dağıtma, rollback), `POST /version/check` ve `POST /version/update` (yalnızca giriş yapmış kullanıcılar; `OPENLOG_SIGNUP_ENABLED=true` ile, rolleri ne olursa olsun yalnızca superadmin'ler) | | | ✓ | ✓ | |
| Owner rolü verme veya kaldırma, owner davet etme, owner çıkarma, organizasyonun sorgu limitleri | | | | ✓ | |

Bir organizasyonda her zaman en az bir owner kalır (`409 failed_precondition`).

`owner` bir API anahtarı rolü değildir: yalnızca owner'a özel işlemler hesap ve organizasyon yaşam döngüsüyle ilgilidir ve
insanda kalır. Bir anahtar hiçbir şeyin sahibi de olmaz: anahtarla oluşturulan dashboard, kayıtlı görünüm, alert kuralı veya mute'un
oluşturanı yoktur, yani sonradan yalnızca admin'ler (veya `admin` anahtarı) değiştirebilir; oluşturduğu şeyleri yönetmesi gereken
otomasyon `admin` anahtarı kullanmalıdır. Retler hangi kuralın engellediğini söyler: **Anahtar** işareti olmayan satır için `this operation requires a signed-in user; API
keys cannot perform it`, rolü yetmeyen anahtar için `this API key's role (viewer) does not allow this
operation` ve giriş yapmış kullanıcı için `your role (viewer) does not allow this operation`.

## Kimlik doğrulama endpoint'leri {#auth-endpoints}

### `GET /api/v1/auth/config` (herkese açık) {#get-api-v1-auth-config-public}
`{"mode": "postgres", "signup_enabled": false, "password_min_length": 8, "email_enabled": false,
"email_verification_required": false, "captcha": null | {"provider": "turnstile|hcaptcha", "site_key"}}`.
`email_enabled`: davet ve doğrulama e-postaları gönderilir (SMTP ve `OPENLOG_PUBLIC_URL` yapılandırılmış).
`captcha` yalnızca kayıt açıkken gelir.

### `POST /api/v1/auth/login` (herkese açık) {#post-api-v1-auth-login-public}
Gövde `{"email": "…", "password": "…"}`. `200` oturum cookie'sini ayarlar ve **Me** nesnesini döner. Yanlış her
e-posta/parola kombinasyonu için `401`, rate limit'e takılınca `429`.

### `POST /api/v1/auth/signup` (herkese açık, `OPENLOG_SIGNUP_ENABLED=true`) {#post-api-v1-auth-signup-public-openlog-signup-enabled-true}
Gövde `{"email", "password", "name", "organization_name", "captcha_token"?}` → `201` + cookie + **Me**. Sahibi yeni kullanıcı olan
yeni bir organizasyon oluşturur (tenant id üretilir). Kapalıysa `403`, e-posta varsa `409`.
Kötüye kullanım koruması (D-045), bu sırayla: geçersiz girdi için `400` (parola politikası 8–256 karakter, e-postayla aynı
olamaz); `OPENLOG_SIGNUP_RATE_WINDOW` içinde istemci IP'si başına `OPENLOG_SIGNUP_MAX_PER_IP` veya
adres başına `OPENLOG_SIGNUP_MAX_PER_EMAIL` denemeden sonra `429 resource_exhausted`; CAPTCHA sağlayıcısı varken
`captcha_token` eksik veya reddedilmişse `400` (sağlayıcıya ulaşılamazsa `503`); engellenmiş e-posta
alan adı için `400`. `email_verification_required` ile yeni kullanıcı giriş yapmış olur ama `user.email_verified` `false`'tur ve
doğrulama e-postası gönderilir (gönderimin başarısız olması kaydı başarısız yapmaz; yeniden gönderme aşağıda). Doğrulanana kadar
`POST /license-keys`, `POST /api-keys`, `POST /invitations` ve davet yeniden gönderimleri `403 permission_denied` döner
("confirm your e-mail address first"); geri kalan her şey çalışır.

### `POST /api/v1/auth/verify-email` (herkese açık) `{"token"}` {#post-api-v1-auth-verify-email-public-token}
Doğrulama bağlantısındaki token (`/verify-email#token=olv_…`) → `204`; geçersiz, kullanılmış veya süresi dolmuşsa
(`OPENLOG_EMAIL_VERIFICATION_TTL`) `404`. Audit: `user.email_verified`.

### `POST /api/v1/auth/verify-email/resend` {#post-api-v1-auth-verify-email-resend}
Giriş yapmış, doğrulanmamış kullanıcı → `204` (yeni bağlantı; önceki bağlantılar süreleri dolana kadar geçerli kalır). Zaten
doğrulanmışsa veya e-posta yapılandırılmamışsa `409`, saatte 5 e-postadan sonra `429`, e-posta gönderilemezse `503`.

### `GET /api/v1/auth/me` {#get-api-v1-auth-me}
**Me** nesnesi:
```json
{"auth": "session", "user": {"id": "…", "email": "ada@example.com", "name": "Ada", "email_verified": true, "language": "auto"},
 "api_key": null, "organization": {"id": "…", "tenant_id": "default", "name": "Default"}, "role": "owner",
 "organizations": [{"id": "…", "tenant_id": "default", "name": "Default", "role": "owner"}],
 "csrf_token": "…"}
```
API anahtarlarında: `"auth": "api_key"`, `user`/`csrf_token` `null`, `organizations: []`, `api_key: {"id", "name", "role"}`
(isteğin kimliğini doğrulayan anahtar) ve `role` = o anahtarın rolü: `viewer`, `member` veya `admin` (D-133).
Oturumlarda `api_key` `null`'dır.
`user.language` kullanıcının dil tercihidir: `"auto"` (tarayıcınınki) veya `"en"`/`"tr"` (D-095).

### `PATCH /api/v1/auth/me` `{"language": "auto" | "en" | "tr"}` {#patch-api-v1-auth-me-language-auto-en-tr}
Giriş yapmış kullanıcılar web arayüzü ve kendilerine gönderilen e-postalar için kendi dillerini ayarlar → `200` **Me**. Web arayüzü tarayıcının dili yerine
bu dilde açılır (Settings → Profile; tercih ayarlıysa üst menüdeki dil değiştirici de onu günceller).
`"auto"` tercihi kaldırır ve isteğin `Accept-Language` değerini hesabın saklanan dili olarak tutar.
Diğer değerler → `400`; API anahtarları → `403`. Audit: `user.language_change` (`details.from`/`to`, organizasyon yok).

### `POST /api/v1/auth/logout` {#post-api-v1-auth-logout}
Oturumu iptal eder ve cookie'yi temizler → `204`.

### `POST /api/v1/auth/password` {#post-api-v1-auth-password}
Gövde `{"current_password", "new_password"}` → `204`; kullanıcının diğer oturumlarını iptal eder. Mevcut parola yanlışsa → `403`.

## Organizasyon ve üyeler {#organization-and-members}

### `GET /api/v1/orgs/current` · `PATCH /api/v1/orgs/current` `{"name"?, "language"?}` {#get-api-v1-orgs-current-·-patch-api-v1-orgs-current-name-language}
`{"id", "tenant_id", "name", "role", "created_at", "language"}` (`role` = çağıranın rolü). `language` organizasyonun
varsayılan e-posta dilidir: `""` (yok), `"en"` veya `"tr"` (D-095). `PATCH` (admin, owner) en az bir alan ister; audit:
`org.rename`, `org.language_change` (`details.from`/`to`).

### `GET /api/v1/members` {#get-api-v1-members}
`{"members": [{"user_id", "email", "name", "role", "joined_at"}]}`

### `PATCH /api/v1/members/{user_id}` `{"role"}` · `DELETE /api/v1/members/{user_id}` {#patch-api-v1-members-user-id-role-·-delete-api-v1-members-user-id}
`204`. Kullanıcı kendini her zaman çıkarabilir (ayrılma).

Değişiklikler **her API pod'unda anında** geçerli olur (D-046): oturumlar cache'lenmez, her istek oturumu ve seçili organizasyonun
üyelik/rol bilgisini PostgreSQL'den okur. Bu yüzden çıkarılan kullanıcının o organizasyonla yaptığı sonraki istek
`403` alır (`X-Openlog-Org-Id` yoksa sıradaki en eski üyeliği kullanılır, o da yoksa hiçbiri), rolü düşürülen kullanıcı da
hemen yeni rolle çalışır. Bir üyeyi çıkarmak, o organizasyonda oluşturduğu API anahtarlarını da iptal eder
(`member.remove` audit olayında `details.revoked_api_keys`). Oturumları diğer
organizasyonları için geçerli kalır. Üyelikle aynı transaction içinde (SCIM ile devre dışı bırakma ve silmede de) adresi
organizasyonun zamanlanmış rapor alıcı listelerinden çıkarılır; alıcısı kalmayan rapor devre dışı kalır
(D-096). Audit: `dashboard.report.recipient_remove` (`details.user_id`, `details.reports`, `details.disabled_reports`;
`details.via = "scim"`) ve `member.remove` üzerinde `details.report_recipient_removed` (sayı).

## Davetler {#invitations}

Davet eden, kabul bağlantısı `/invite#token=<token>` için her zaman tek kullanımlık bir token alır (web arayüzü; token URL
fragment'ında kalır). E-posta yapılandırılmışsa (`GET /auth/config` → `email_enabled`) bağlantı davet edilene
`OPENLOG_SMTP_FROM` adresinden e-postayla da gönderilir; yanıttaki `email_sent` bunun başarılı olup olmadığını söyler (başarısız veya rate limit'e takılan
e-posta isteği asla başarısız yapmaz). Limitler: saatte organizasyon başına 100 ve davet edilen adres başına 5 davet e-postası.

**E-posta dili.** İşlemsel e-postalar (davet, kayıt doğrulama, SSO alan adı doğrulama, kullanım
bildirimleri) `internal/mail/templates` içinden düz metin + HTML olarak İngilizce veya Türkçe üretilir (D-085, D-095).
Dil, şunlardan ilk bulunandır: (1) alıcının dil tercihi (`PATCH /auth/me`, adres bir
kullanıcıya aitse), (2) organizasyonun varsayılan dili (`PATCH /orgs/current`; yalnızca organizasyon e-postaları: davetler, alan adı
doğrulama, kullanım bildirimleri), (3) saklanan istek dili, yani oluşturan isteğin `Accept-Language` başlığının en çok tercih ettiği desteklenen dil
(`q` değerleri dikkate alınır): davetlerde davet edeninki (yeniden gönderimde korunur, sonra yeniden gönderen admin'inki), doğrulama bağlantıları ve alan adı doğrulamada mevcut isteğinki, kullanım bildirimlerinde hesabın oluşturulma anındaki dili
(`users.locale`, postgres.md) ve (4) İngilizce. Doğrulama bağlantıları
organizasyon e-postası değildir (tercih, istek dili, hesap dili). Zamanlanmış dashboard raporları raporun
kendi `language` değerini kullanır.

### `GET /api/v1/invitations?include_expired=` · `POST /api/v1/invitations` `{"email", "role"}` · `DELETE /api/v1/invitations/{id}` {#get-api-v1-invitations-include-expired-·-post-api-v1-invitations-email-role-·-delete-api-v1-invitations-id}
Liste: `{"invitations": [{"id", "email", "role", "invited_by_email", "created_at", "expires_at", "expired", "last_sent_at", "send_count"}]}`: bekleyen davetler; `include_expired=true` ile kabul veya iptal edilmemiş süresi dolmuş olanlar da.
Oluşturma → `201 {"invitation": {…}, "token": "oli_…", "email_sent": true}` (**token bir kez gösterilir**); e-posta
zaten üyeyse veya bekleyen daveti varsa `409`. Doğrulanmamış kayıtlar → `403`.

### `POST /api/v1/invitations/{id}/resend` {#post-api-v1-invitations-id-resend}
Kabul veya iptal edilmemiş (süresi dolmuş da olabilir) bir davet için yeni token üretir, geçerliliğini yeniden başlatır
(`OPENLOG_INVITATION_TTL`) ve e-posta yapılandırılmışsa e-postayla gönderir → `200 {"invitation", "token", "email_sent"}`.
Önceki bağlantı çalışmaz olur. Kabul edilmiş, iptal edilmiş veya bilinmiyorsa `404`; owner davetlerini yalnızca owner'lar yeniden gönderir.
Audit: `invitation.resend`.

### `POST /api/v1/invitations/lookup` (herkese açık) `{"token"}` {#post-api-v1-invitations-lookup-public-token}
`{"organization_name", "email", "role", "expires_at", "user_exists"}`; geçersiz, kullanılmış, iptal edilmiş veya süresi dolmuşsa `404`.

### `POST /api/v1/invitations/accept` (herkese açık) `{"token", "password", "name"}` {#post-api-v1-invitations-accept-public-token-password-name}
Yeni kullanıcı: hesabı `password` ile oluşturur (politika: 8–256 karakter). Var olan kullanıcı (`user_exists`): `password`
mevcut parolası olmalıdır. Üyeliği ekler, giriş yapar (cookie) ve kullanıcının varsayılan
organizasyonuyla **Me** döner (geçiş için `X-Openlog-Org-Id` gönder).

## Ingest lisans anahtarları {#ingest-license-keys}

### `GET /api/v1/license-keys` {#get-api-v1-license-keys}
`{"license_keys": [{"id", "name", "prefix": "olk_1a2b3c4d", "custom": false, "created_by_email", "created_at", "last_used_at", "revoked_at"}]}`
(iptal edilmiş anahtarlar dahil). `last_used_at` en fazla dakikada bir güncellenir ve ingest pod'u başına bir dakikaya kadar geride kalabilir.

### `POST /api/v1/license-keys` `{"name", "key"?}` {#post-api-v1-license-keys-name-key}
`key` olmadan: `201 {"license_key": {…, "custom": false}, "key": "olk_…"}`. **Anahtar yalnızca bu yanıtta gösterilir**.

`key` ile var olan bir değer **içe aktarılır**, böylece onu zaten kullanan göndericiler (örneğin başka bir backend için
yapılandırılmış `x-api-key` header'ı) yeniden deploy edilmeden çalışmaya devam eder:
- Baştaki ve sondaki boşluklar kırpılır; değer boşluk, tırnak
  veya ters bölü içermeyen 16–256 karakterlik yazdırılabilir ASCII olmalıdır (`[\x21-\x7e]` eksi `"`, `'`, `\`), böylece HTTP header'larına, gRPC metadata'sına ve `install.sh`'a sığar.
  Aksi halde `400 invalid_argument` (boş `key` de reddedilir; anahtar üretmek için onu hiç gönderme).
- Yalnızca `sha256(key)` ve değerin en fazla yarısını açığa çıkaran bir görüntüleme öneki saklanır.
- **Herhangi bir** organizasyonun aktif veya iptal edilmiş olarak kullandığı değer `409 already_exists` ile başarısız olur; yanıt
  hangi organizasyonda olduğunu söylemez.
- `201 {"license_key": {…, "custom": true}, "key": null}` (çağıran değeri zaten biliyor). Audit:
  `details.custom = true` ile `license_key.create`.

### `DELETE /api/v1/license-keys/{id}` {#delete-api-v1-license-keys-id}
İptal eder → `204` (idempotent). Ingest pod'ları cache kayıtlarının süresi dolana kadar, yani en fazla
`OPENLOG_AUTH_CACHE_TTL` (varsayılan 60 sn) boyunca anahtarı kabul etmeye devam eder.

## Tarayıcı anahtarları {#browser-keys}

RUM tarayıcı SDK'sının public anahtarları ([rum.md](/tr/contracts/rum) §3, D-136). Yalnızca PostgreSQL auth modunda. Okumalar
member ister (API anahtarlarına izin var); oluşturma, düzenleme ve iptal, diğer tüm kimlik bilgisi işlemleri gibi **giriş yapmış** bir admin veya owner ister. Audit: `browser_key.{create,update,revoke}`.

**Tarayıcı anahtarı bir sır değildir.** Web sayfasının içinde gelir, yani sayfayı açabilen herkeste vardır.
Ingest lisans anahtarından ayrı bir kimlik bilgisi türüdür (`olk_` yerine `olb_`, kendi tablosu) ve
tam olarak bir endpoint'te kimlik doğrular: `POST /v1/rum`; bu endpoint'in payload'ları anahtarın söylemesine izin verilen şeye göre
yeniden yazılır. Onu sınırlayan gizlilik değil yetenektir: origin allowlist'i, rate limit, zorunlu
uygulama adı ve iptal. Tam tehdit modeli, yani kopyalanan bir anahtarın neleri yapıp neleri yapamayacağı,
[rum.md §3.5](/tr/contracts/rum) içindedir.

**Değer okunabilir.** Buradaki diğer tüm kimlik bilgilerinin aksine tarayıcı anahtarı, yalnızca oluşturulurken bir kez değil,
yukarıdaki okumalarda da (`key`) tam olarak döner. Bu bir kolaylık istisnası değil: değer web sayfasının içinde gelir,
yani sayfayı açabilen herkeste zaten vardır ve tekrar göstermeyi reddetmek yalnızca operatörü internetin zaten bildiği
bir anahtarı döndürmeye zorluyordu. Lisans anahtarları, API anahtarları ve SSO client secret'ları gerçek
sırlardır, yalnızca hash olarak kalır ve yine tam bir kez gösterilir. Bu saklama başlamadan önce oluşturulan tarayıcı anahtarları
`key: ""` döner: düz metinleri hiç tutulmadı ve geri getirilemez, yani bunlar yalnızca döndürülebilir.
Değer audit log'da asla görünmez, orada önek kaydedilir.

**İki tür.** `kind`, `browser` (varsayılan, türü olmayan anahtar da budur) veya `mobile`'dır. Tarayıcı anahtarı
`origins` taşır, `app_ids` taşımaz; mobil anahtar `app_ids` (`openlog-app-id` header'ında gönderilen Android paket adları ve iOS bundle
identifier'ları) taşır, `origins` taşımaz. Türe ait olmayan allowlist'i göndermek `400`'dür, allowlist'siz `mobile` anahtar da öyle. İkisi **eşit derecede güçlü değildir**: tarayıcı
`Origin`'i taklit edemez, uygulama ise kendi tanımlayıcısını kendisi bildirir, yani `app_ids` bir programı durdurmaktan çok
gelişigüzel yeniden kullanımı daraltır ([rum.md §3.6](/tr/contracts/rum)).

### `GET /api/v1/browser-keys` {#get-api-v1-browser-keys}
`{"browser_keys": [{"id", "name", "prefix": "olb_1a2b3c4d", "service_name", "environment", "origins": [],
"rate_limit_per_minute", "sample_rate", "analytics_mode", "created_by_email", "created_at", "updated_at", "last_used_at",
"revoked_at"}]}` (iptal edilmiş anahtarlar dahil). Değerin kendisi okumada asla dönmez.

### `POST /api/v1/browser-keys` {#post-api-v1-browser-keys}
Gövde: `{"name" (1–200), "service_name" (1–512), "environment"? (≤ 256), "origins" (1–50),
"rate_limit_per_minute"? (default 6000, 60–10000000), "sample_rate"? (default 1, 0 < x ≤ 1),
"analytics_mode"? (`off`, `sessions` or `visitors`; default `visitors`, [analytics.md](analytics.md) §1.1)}`. `PUT`'ta
gönderilmeyen `analytics_mode` mevcut değeri korur.

`201 {"browser_key": {…}, "key": "olb_…"}`: değer burada döner, başka hiçbir yerde dönmez.

`origins`, anahtarın kullanılabileceği origin'lerdir, tarayıcının gönderdiği biçimde: tam
(`https://app.example.com`, şemanın varsayılanı değilse port dahil) veya alt alan adı joker karakteri
(`https://*.example.com`, her derinlikteki alt alan adını kapsar). **Boş liste reddedilir** (`400`): allowlist'siz anahtar
her web sitesinden veri kabul eder ve boş bir alan güvensiz ayar olmamalıdır. `*` da aynı nedenle
reddedilir. Değer her zaman üretilir; tarayıcı anahtarları içe aktarılamaz.

### `PUT /api/v1/browser-keys/{id}` {#put-api-v1-browser-keys-id}
Aynı alanların tamamen değiştirilmesi → `200`. İptal edilmiş bir anahtar düzenlenerek asla hizmete geri alınmaz (`404`): değeri
eski sayfayı yüklemiş tarayıcılarda hâlâ cache'li olabilir.

### `DELETE /api/v1/browser-keys/{id}` {#delete-api-v1-browser-keys-id}
İptal eder → `204` (idempotent). Ingest pod'ları anahtarı en fazla `OPENLOG_AUTH_CACHE_TTL`
(varsayılan 60 sn) boyunca kabul etmeye devam eder, zaten yüklenmiş sayfalar da yeniden yüklenene kadar göndermeyi sürdürür. Değer
kalıcı olarak kullanılamaz kalır.

## Source map'ler {#source-maps}

Tarayıcı uygulamalarının stack'lerini un-minify eden belgeler ([rum.md](/tr/contracts/rum) §8). Yalnızca PostgreSQL auth modunda
ve `OPENLOG_SOURCE_MAPS_ENABLED` açıksa; aksi halde bunlar `404` döner. Okumalar member ister
(API anahtarlarına izin var); yükleme ve silme **giriş yapmış** bir admin veya owner ister.

**Map'in anahtarı sürüm değil, bundle dosya adıdır.** RUM span'i build tanımlayıcısı taşımaz: ingest,
resource'u `service.version` içermeyen bir allowlist'ten yeniden oluşturur ve tarayıcı anahtarının da
böyle bir alanı yoktur ([rum.md](/tr/contracts/rum) §3.3). Stack'in taşıdığı şey ise dosya adının içindeki içerik hash'idir
(`main.3f2a1b9c.js`), bu da tek bir build'i tam olarak tanımlar; hata fingerprint'i, gruplar deploy'lar arasında sabit kalsın diye bunu grup anahtarından çıkarır,
ama saklanan stack'te olduğu gibi durur. Yani bir deploy map'lerini yeni hash'li adlarla yükler
ve hiçbir şeye hangi sürümün güncel olduğunu söylemek gerekmez.

Symbolication **okuma anında**, `GET /api/v1/apm/services/{service}/errors/{group_id}` içinde yapılır:
`apm_error_groups` güncelleme yolu olmayan bir materialized view'dur, yani çözülmüş stack asla geri yazılmaz.
Bir frame ya çözülür ya çözülmez; map'i olmayan uygulama, storage'ın döndüremediği map veya
üçüncü parti bundle'dan gelen frame o satırı geldiği gibi bırakır.

### `GET /api/v1/source-maps` {#get-api-v1-source-maps}
`{"source_maps": [{"id", "app", "script", "size_bytes", "sha256", "created_by_email", "created_at",
"updated_at"}]}`, en yenisi başta. Belgenin kendisi asla geri sunulmaz.

### `POST /api/v1/source-maps?app=&script=` {#post-api-v1-source-maps-app-script}
**Gövde olarak ham source map belgesi** (`Content-Type` incelenmez), en fazla 32 MiB. Bu, üründe binary gövde alan
tek endpoint'tir: JSON içinde base64, bir build'in ürettiği dosya için üçte bir fazla byte demek olurdu.

`app` tarayıcı uygulamasıdır (tarayıcı anahtarının `service_name` değeri, 1–512). `script`, stack frame'in taşıdığı şekliyle üretilen dosya
adıdır: `main.3f2a1b9c.js`, dizin, query string veya fragment olmadan; yol kırpılmak yerine reddedilir (`400`), çünkü `assets/main.js` yükleyip sonra `main.js` ile eşleştirmek çalışmış gibi görünürdü.

`201 {"source_map": {…}}`. Bir script'i yeniden yüklemek, onun için tutulan map'i aynı id'yi koruyarak **değiştirir**.
Belge saklanmadan önce ayrıştırılır: Source Map v3 olmayan, `sections` (index map'leri) kullanan
veya hiçbir eşleme çıkmayan map, yükleyen kişi hâlâ düzeltebilecekken reddedilir (`400`).

### `DELETE /api/v1/source-maps/{id}` {#delete-api-v1-source-maps-id}
`204` veya organizasyonda böyle bir map yoksa `404`. Satırı ve saklanan belgeyi siler.

## Crash sembolleri {#crash-symbols}

Bir mobil crash'in stack'ini yeniden kaynak koda çeviren şey ([mobile-crash.md](/tr/contracts/mobile-crash) §3). Source map'lerin aksine bunlar API
anahtarı kabul eder, çünkü çağıran bir build makinesidir. Source map'lerin storage'ını ve `OPENLOG_SOURCE_MAPS_ENABLED` ayarını paylaşırlar.

### `GET /api/v1/symbols` {#get-api-v1-symbols}
Member ve üstü. `{"symbols": [{"id", "app", "kind", "key", "size_bytes", "sha256", "created_at", "updated_at"}]}`, en yenisi başta.

### `POST /api/v1/symbols?app=&kind=&key=` {#post-api-v1-symbols-app-kind-key}
Admin veya owner. Gövde dosyanın kendisidir: `kind=r8` ve `key` olarak sürümle bir R8 mapping'i veya `kind=native` ve `key` olarak image id ile
`openlog-symbols`'ün ürettiği bir tablo, gzip olabilir, en fazla 128 MiB. Saklanmadan önce ayrıştırılır; okunamayan
veya hiç fonksiyonu olmayan dosya `400`'dür. Aynı key için yeniden yükleme dosyayı değiştirir ve id'yi korur. `201 {"symbol": {…}}`. Audit:
`symbols.upload`.

### `DELETE /api/v1/symbols/{id}` {#delete-api-v1-symbols-id}
Admin veya owner. `204` veya organizasyonda böyle bir dosya yoksa `404`. Audit: `symbols.delete`.

## API anahtarları {#api-keys}

### `GET /api/v1/api-keys` {#get-api-v1-api-keys}
`{"api_keys": [{"id", "name", "prefix": "ola_…", "role": "viewer", "scope": "read", "created_by_user_id", "created_by_email", "created_at", "last_used_at", "expires_at", "revoked_at"}]}`
`role` anahtarın neler yapabileceğidir (`viewer`, `member` veya `admin`; [Roller](#roles), D-133). `scope` bundan türetilir
ve eski istemciler için tutulur: viewer anahtarı için `read`, member veya admin anahtarı için `write`.

### `POST /api/v1/api-keys` `{"name", "role"?, "expires_at"?}` {#post-api-v1-api-keys-name-role-expires-at}
`201 {"api_key": {…}, "key": "ola_…"}`: **bir kez gösterilir**. `expires_at` (isteğe bağlı) gelecekte olmalıdır.
`role`, `viewer` (yoksa varsayılan budur, böylece eski istemciler salt okunur anahtar oluşturmaya devam eder), `member` veya
`admin` olur; başka her şey → `400 invalid_argument` ("role must be one of viewer, member, admin"). `viewer` üstündeki anahtar
admin veya owner ister ve kimse kendi rolünden daha yetenekli bir anahtar oluşturamaz
(`403 permission_denied`). `api_key.create` audit kaydı `details.role` taşır.

### `DELETE /api/v1/api-keys/{id}` {#delete-api-v1-api-keys-id}
İptal eder → `204`; anında geçerlidir.

## Oturumlar ve audit log {#sessions-and-audit-log}

### `GET /api/v1/sessions` · `DELETE /api/v1/sessions/{id}` {#get-api-v1-sessions-·-delete-api-v1-sessions-id}
Çağıranın aktif oturumları: `{"sessions": [{"id", "created_at", "last_seen_at", "expires_at", "ip", "user_agent", "current"}]}`.
Mevcut oturumu iptal etmek cookie'yi de temizler.

### `GET /api/v1/audit-log?limit=&actor=&action=&from=&to=&cursor=` {#get-api-v1-audit-log-limit-actor-action-from-to-cursor}
En yenisi başta (varsayılan 100, en fazla 500): `{"events": [{"id", "actor_email", "actor_api_key": {"id", "name"} | null, "action", "target_type", "target_id", "details", "ip", "created_at"}], "next_cursor": "…" | null}`.
API anahtarıyla yapılan değişikliğin `actor_email` değeri boştur ve anahtarı `actor_api_key` içinde adıyla belirtir (bunun için
kullanıcı uydurulmaz; veritabanında `actor_user_id` NULL'dır, D-133); giriş yapmış kullanıcıda `actor_api_key` `null`'dır.
`actor`: aktörün e-postasında veya anahtar adında büyük/küçük harf duyarsız alt dize; `action`: önek (`member.` veya `member.remove`);
`from` (dahil) / `to` (hariç): RFC3339 veya unix ms; `cursor`: aynı filtrelerle önceki sayfanın `next_cursor` değeri
(sayfa dolu değilse `null`). Geçersiz değerler → `400`. Web arayüzünde Settings → Audit log.
Eylemler: bkz. [postgres.md](/tr/contracts/postgres#audit_log).

## Tek oturum açma {#single-sign-on}

Organizasyon başına OIDC ve SAML 2.0 ile giriş (D-077, D-088, D-089; operasyon kılavuzu [sso.md](/tr/operations/sso), kod
`internal/sso`). Postgres auth modu, `OPENLOG_SSO_ENABLED=true` (varsayılan) ve `OPENLOG_PUBLIC_URL` gerekir;
`GET /auth/config` `sso_enabled` bildirir.

**Model.** Bir organizasyonun en fazla 10 bağlantısı (`oidc` veya `saml`) olur ve e-posta alan adlarını sahiplenir. En eski bağlantı
**varsayılan bağlantıdır**; doğrulanmış bir alan adı girişleri kendi `connection_id`'sine (`PUT /sso/domains/{id}`) veya,
ayarlanmamışsa, varsayılan bağlantıya yönlendirir ve bir bağlantı yalnızca kendisine yönlendirilen alan adlarının kimliklerini kabul eder. Alan adı
DNS TXT kaydı `_openlog-verification.<domain>` = `openlog-domain-verification=<token>` ile veya
`admin|administrator|hostmaster|postmaster|webmaster@<domain>` adresine e-postayla gönderilen bağlantıyla doğrulanır; doğrulanmış bir alan adı tek bir
organizasyona aittir. **Yalnızca e-posta adresi, bağlantının organizasyonuna ait doğrulanmış bir alan adında olan kimlikler
kabul edilir** (giriş, test ve SCIM), böylece bir organizasyon admin'inin IdP'si başka bir şirketin kullanıcısı olarak giriş yapamaz.

**Giriş.** `POST /auth/sso/start`, girişe özel bir bağlama cookie'si ayarlar (`openlog_sso_<12 hex>`, HttpOnly,
SameSite=Lax, Path=/api/v1/sso, 10 dk; sunucu yalnızca SHA-256'sını saklar) ve IdP URL'sini döner.
- OIDC: PKCE S256, `state` (tek kullanımlık, 10 dk) ve `nonce` ile authorization code flow; discovery
  issuer ile eşleşmelidir; ID token imzası sağlayıcının JWKS'si ile (cache'lenir, saatte bir ve bilinmeyen `kid` görülünce yeniden çekilir), yalnızca RS/PS/ES256–512 ve EdDSA algoritmaları, `aud` client ID'yi içerir (birden çok audience varsa `azp` zorunlu), `exp`
  ve `iat` `OPENLOG_SSO_CLOCK_SKEW` ile. Bağlantı kapatmadıkça `email_verified=true` zorunludur.
  ID token'da olmayan e-posta/gruplar UserInfo'dan okunur (aynı `sub`). IdP-initiated OIDC desteklenmez.
- SAML: HTTP-Redirect AuthnRequest (isteğe bağlı imzalı), yanıt ACS'de HTTP-POST ile. İmza kontrollerinden önce
  belge, DTD içermeyen tek bir `samlp:Response` olmalı ve herhangi bir yerinde tam olarak bir `Assertion`/`EncryptedAssertion` bulunmalıdır
  (XML signature wrapping). Yanıt veya assertion, bir IdP metadata sertifikasının attığı tam olarak bir enveloped imza taşımalıdır
  (RSA/ECDSA SHA-256/384/512; SHA-1 reddedilir; crewjam/saml + goxmldsig, doğrulanan öğe =
  ayrıştırılan öğe). Zorunlular: issuer = IdP entity ID, `Destination`/`Recipient` = ACS URL, SP entity ID ile bir `AudienceRestriction`,
  bearer `SubjectConfirmation`, `AuthnStatement`, `InResponseTo` = bekleyen
  istek (SP-initiated), clock skew ile zaman koşulları. Assertion ID'leri süreleri dolana kadar saklanır (replay →
  `replay`). ACS doğrulanmış kimliği girişe kaydeder ve (303) `GET /sso/saml/complete?state=` adresine yönlendirir; orası bağlama cookie'sini ister (siteler arası POST SameSite
  cookie'lerini taşımaz). IdP-initiated yanıtlar (`InResponseTo` yok) yalnızca bağlantı izin veriyorsa kabul edilir; RelayState'leri
  `relay_state_allowlist` içinden biri olmalıdır (yoksa `/`).
- Tamamlama: JIT, kullanıcıyı (parolasız, e-postası doğrulanmış) ve eşlenen rol veya
  `default_role` ile üyeliği oluşturur; JIT kapalıysa (`not_member`) veya SCIM kullanıcıyı devre dışı bıraktıysa (`deprovisioned`) oluşturmaz. Rol eşlemeleri
  ve grup claim'i/attribute'u varsa owner olmayan roller her girişte eşitlenir. Oturum, parola oturumu gibi, üstüne `auth_method` (`oidc`/`saml`), organizasyon ve bağlantı ile oluşturulur; ömrü
  `OPENLOG_SESSION_TTL` ile `session_max_age_seconds` değerlerinin küçüğüdür.
- Her adım `303` döner: girişin arayüz yoluna (`redirect`, göreli, asla `/api/…` değil) veya
  `expired`, `invalid_request`, `idp_error`, `invalid_response`, `replay`, `disabled`,
  `email_missing`, `email_not_verified`, `domain_not_verified`, `not_member`, `deprovisioned`, `account_disabled`,
  `unavailable` kodlarından biriyle `/login?sso_error=<code>` adresine. Test girişleri `/settings/sso?sso_test=ok|failed` adresine döner. `Referrer-Policy: no-referrer`.

**Oturumlar.** Bir SSO oturumu her istekte yalnızca kendi organizasyonunda (başka `X-Openlog-Org-Id` → `403`),
yalnızca onu oluşturan bağlantı hâlâ var ve etkinken (yoksa `401`) ve yalnızca azami
oturum yaşına kadar (`401`) işlem yapabilir. `POST /auth/logout` yereldir; single logout IdP oturumunu da bitirir.

**Single logout (D-088).** Her SSO oturumunun IdP oturumu girişte kaydedilir (SAML NameID, format,
qualifier'lar ve `SessionIndex`; OIDC `sub`, `sid` ve client secret'lar gibi mühürlenmiş ID token); kaydedilemezse giriş başarısız olur
(`unavailable`). `POST /auth/sso/logout` ("Sign out everywhere") önce çağıranın
oturumunu ve kullanıcının aynı bağlantıdaki diğer oturumlarını iptal eder, cookie'yi temizler ve tarayıcının
IdP oturumunu bitireceği yeri döner:
- SAML (`SingleLogoutService` içeren IdP metadata'sı): SP anahtarıyla imzalanmış bir `LogoutRequest` (NameID, SessionIndex),
  RSA-SHA256 query imzalı HTTP-Redirect binding (`redirect_url`) veya enveloped imzalı HTTP-POST
  (`post`). IdP `/sso/saml/{connection_id}/slo` adresinde yanıt verir; `LogoutResponse` bir IdP
  metadata sertifikasıyla imzalanmış olmalı, issuer = IdP entity ID, `Destination` (varsa) = SLO URL, güncel bir `IssueInstant`
  ve `InResponseTo` = istek olmalıdır (RelayState tek kullanımlık bir state'tir, `OPENLOG_SSO_LOGIN_TTL`) →
  `303 <redirect>?sso_logout=ok`, aksi halde `?sso_logout=partial`.
- IdP-initiated: `/sso/saml/{connection_id}/slo` adresine gelen `LogoutRequest` (iki binding'den biri) imzalı olmalı (RSA/ECDSA SHA-256/384/512 ile query imzası
  veya tam olarak bir enveloped imza; SHA-1 reddedilir), IdP entity ID'den gelmeli,
  `Destination` = SLO URL, `IssueInstant` 5 dk artı clock skew içinde olmalı ve `NotOnOrAfter` geçmemiş olmalıdır; ID'si
  replay cache'inde tutulur (`replay`). Bir `NameID` veya `EncryptedID` (SP anahtarıyla çözülür) taşır. Bağlantının
  o NameID'ye sahip aktif oturumları (IdP `SessionIndex` değerleri gönderdiyse yalnızca adı geçenler)
  iptal edilir ve SP anahtarıyla imzalanmış bir `LogoutResponse` (`Success` veya iptal başarısız olduysa `Responder`) IdP'nin
  SingleLogoutService'ine gider (HTTP-Redirect için `303`, HTTP-POST için hash'i sabitlenmiş
  `Content-Security-Policy` içeren, kendini gönderen bir form). Geçersiz istekler yanıtsız ve hiçbir şey iptal etmeden
  `/login?sso_error=invalid_request` adresinde biter.
- OIDC (`end_session_endpoint` içeren discovery): `redirect_url` = `id_token_hint`,
  `client_id`, `post_logout_redirect_uri` = `/api/v1/sso/oidc/logout/callback` (IdP'de kaydet) ve `state` ile end session endpoint'i;
  callback tarayıcıyı `<redirect>?sso_logout=ok` adresine gönderir.
- `redirect`, `/login` veya bağlantının `logout_redirect_allowlist` yollarından biri olmalıdır (yoksa `/login`). IdP
  logout endpoint'i yoksa yalnızca openlog oturumları biter (`redirect_url` ve `post` null).

**Tarayıcı gidiş dönüşü olmadan kimlik sağlayıcıdan çıkış (D-098).** Bağlantı başına (URI'ler, adreslenen bağlantının
`service_provider` alanında; reddedilen istekler `sso.logout_failed` olarak audit'lenir ve istemci adresi başına 10 dk'da 60 ile
sınırlıdır, geçerli olanlar `via` ile `sso.logout` olarak audit'lenir):
- SAML SOAP binding `POST /sso/saml/{connection_id}/slo/soap` (SP metadata'sında da var): tam olarak bir enveloped imzalı
  tam olarak bir `LogoutRequest` içeren SOAP 1.1 envelope, yukarıdaki front-channel isteği gibi kontrol edilir (`Destination`
  isteğe bağlı: SOAP veya SLO URL); yanıt SOAP gövdesinde imzalı `LogoutResponse` ile `200`, aksi halde ayrıntısız `500` SOAP
  fault (`via` `idp_soap`).
- OIDC Back-Channel Logout 1.0 `POST /sso/oidc/{connection_id}/backchannel-logout` (form `logout_token`): sağlayıcının JWKS'sindeki bir anahtarla
  imzalanmış JWT (asimetrik algoritmalar), `iss` = issuer, `aud` ∋ client ID, `iat`
  5 dk artı clock skew içinde, `exp` (varsa) geçmemiş, `http://schemas.openid.net/event/backchannel-logout` içeren `events`, `nonce` yok, bir `jti` (replay cache, `oidc-logout:<jti>`) ve
  `sub` ve/veya `sid`. `sid` ile o IdP oturumunun oturumları biter (ikisi de verilmişse `sub`'ınkiler de), `sub` ile
  yalnızca öznenin o bağlantıdaki tüm oturumları → `200`; reddedilirse → `400 {"error": "invalid_request",
  "error_description"}`; oturumlar bitirilemezse `503 temporarily_unavailable` (`via` `oidc_backchannel`).
- OIDC Front-Channel Logout 1.0 `GET /sso/oidc/{connection_id}/frontchannel-logout?iss=…&sid=…` ("session required" ile
  kaydet): `iss` issuer olmalıdır ve `sid` zorunludur (`SameSite=Strict` oturum cookie'si
  sağlayıcının iframe'ine asla ulaşmaz); `sid` oturumları biter. `Cache-Control: no-cache,
  no-store` ve `Content-Security-Policy … frame-ancestors <issuer origin>` ile boş bir HTML sayfası (`X-Frame-Options` yok);
  `iss`/`sid` yoksa veya başka bir issuer ise `400` (`via` `oidc_frontchannel`).

**IdP metadata güveni (D-098).** `idp_metadata_url` ile SAML bağlantısı kaydetmek bir güven kararı ister:
`metadata_signing_certificate_pem` ile imzalanmış metadata (verilmezse: değişmemiş URL'nin sabitlenmiş sertifikaları; sertifikasız imzalı metadata
imzalayanını sabitler, `saml.metadata_signing_certificates` ile karşılaştır) veya imzasız metadata için `allow_unsigned_metadata`
(yoksa `400`). İmzalı metadata, kökünde tam olarak bir enveloped imza taşımalıdır
(`ID`'li `EntityDescriptor`/`EntitiesDescriptor`, referans `#ID`, RSA/ECDSA SHA-256+, şu an geçerli sabitlenmiş bir sertifika),
böylece sarmalanmış veya değiştirilmiş belgeler reddedilir.

**Sahiplenilen alan adları (D-089).** Doğrulanmış alan adı **etkin** bir bağlantıya yönlenen adres parola hesabı
oluşturmaz: `POST /auth/signup` `409 failed_precondition` döner ("… uses single sign-on of the organization
…"; `/auth/sso/discover` onu adıyla söyler), `POST /invitations/accept` da o
organizasyonun davetleri için ve, bağlantıda `allow_external_invitations` (varsayılan true) yoksa, diğer organizasyonların davetleri için aynı `409`'u döner.
`POST /invitations/lookup` `sso` döner (`required`, `organization_name`, `connection_name`, `protocol`,
`same_organization`), böylece arayüz parola formu yerine "Continue with SSO" sunar. SSO girişi, bağlantının organizasyonundaki
bekleyen daveti davet edilen rolle kabul eder (JIT kapalıyken de).

**Arka planda yenileme (D-088).** API leader'ı, etkin her bağlantı için, OIDC discovery belgesini ve JWKS'yi
30 dk'da bir (`sso_connections.idp_cache` içinde saklanır ve her pod tarafından en fazla bir saat kullanılır, bilinmeyen key id'leri yine
JWKS'yi çeker), `idp_metadata_url` SAML metadata'sını ise `cacheDuration`/kalan `validUntil` süresinin yarısında (15 dk –
24 sa, varsayılan 6 sa) yeniden çeker. Aynı entity ID ile değişen metadata, yeni bir `config_version` olmadan saklanan kopyanın yerine
(audit `sso.connection.metadata_refresh`) yalnızca sabitlenmiş bir metadata imzalama sertifikasıyla imzalıysa geçer; başka imzalama sertifikaları veya endpoint'ler içeren imzasız
metadata ya da sabitlenmemiş bir sertifikayla imzalanmış metadata yenilemeyi başarısız yapar ve bir yönetici
`POST /sso/connections/{id}/metadata/accept` ile onaylayana kadar `saml.pending_metadata` olarak tutulur (`reason` `changed`/`signer_changed`, audit
`sso.connection.metadata_pending`)
(audit `sso.connection.metadata_accept`; yeni imzalayan sabitlenir). Sabitlenmiş sertifikası olan metadata'da geçersiz veya eksik imza, değişmiş entity ID, süresi dolmuş metadata veya süresi dolmuş imzalama sertifikası yenilemeyi başarısız yapar. Başarısızlıklarda bekleme 5 dk → 1 sa arasında artar. `connection.health`, `ok`, `warning` (1–2 başarısızlık, 30 gün içinde dolacak
sertifika, 7 günden az geçerli metadata), `error` (3 başarısızlık, süresi dolmuş sertifika) veya `unknown` bildirir.
Metrikler: `openlog_sso_idp_refresh_total{protocol,result}`, `openlog_sso_idp_refresh_duration_seconds{protocol}`,
`openlog_sso_idp_refresh_failing_connections`.

**Zorunlu kılma.** Zorunlu kılma bir bağlantı ayarıdır ve doğrulanmış e-posta alan adı o bağlantıya yönlenen üyelere uygulanır.
`enforce` ile bu üyelerin parola oturumları organizasyonda işlem yapamaz (`403 permission_denied` "this organization requires single sign-on"), break-glass owner'lar
(bağlantının `break_glass_user_ids` listesindeki owner'lar) hariç. Doğru parolayla `POST /auth/login`, kullanıcının
tüm organizasyonları SSO gerektiriyorsa aynı `403`'ü döner (başka organizasyonu olan kullanıcı yine giriş yapar, ama zorunlu kılan
organizasyona erişemez). Zorunlu kılmayı açmak (owner) şunları ister: mevcut `config_version` değeri test girişinden geçmiş etkin bir bağlantı,
ona yönlendirilmiş doğrulanmış bir alan adı, en az bir break-glass owner ve çağıranın erişimini koruması
(break-glass owner veya bu bağlantının SSO oturumu) → aksi halde `409`. Zorunluyken bağlantı devre dışı bırakılamaz
veya silinemez, son doğrulanmış alan adı da kaldırılamaz veya başka yere yönlendirilemez (`409`).

| Endpoint | Kim | Notlar |
|---|---|---|
| `POST /auth/sso/discover` `{"email"}` | herkese açık | Yönlendirilen bağlantının `{"sso", "organization_name", "connection_name", "protocol", "enforced"}` bilgisi; yalnızca bir alan adının SSO kullanıp kullanmadığını açığa çıkarır. IP başına 10 dk'da 60 |
| `POST /auth/sso/start` `{"email", "redirect"?}` | herkese açık | `{"redirect_url"}` + bağlama cookie'si; alan adı için etkin bağlantı yoksa `404`; IdP'ye ulaşılamıyorsa `503`. IP başına 10 dk'da 30 |
| `GET /sso/oidc/callback` · `POST /sso/saml/{connection_id}/acs` · `GET /sso/saml/complete` | herkese açık | Yukarıya bak; her zaman `303` |
| `GET /sso/saml/{connection_id}/metadata` | herkese açık | SP metadata'sı (`application/samlmetadata+xml`); URL, SP entity ID'dir. ACS'yi, SLO servisini (iki binding), imzalama sertifikasını ve kabul edilen şifreleme algoritmalarını listeler |
| `GET\|POST /sso/saml/{connection_id}/slo` · `GET /sso/oidc/logout/callback` | herkese açık | Single logout, yukarıya bak; `303` veya HTML formu |
| `POST /sso/saml/{connection_id}/slo/soap` · `POST /sso/oidc/{connection_id}/backchannel-logout` · `GET /sso/oidc/{connection_id}/frontchannel-logout` | herkese açık (kimlik sağlayıcı) | IdP'den çıkış, yukarıya bak (D-098) |
| `GET /auth/sso/session` | giriş yapmış kullanıcı | `{"sso", "protocol", "connection_id", "connection_name", "idp_logout"}` |
| `POST /auth/sso/logout` `{"redirect"?}` | giriş yapmış kullanıcı | `{"protocol", "redirect_url", "post": {"url", "fields"}\|null, "revoked_sessions"}`; oturum cookie'sini temizler |
| `GET /sso/connections` · `POST /sso/connections` `SSOConnectionInput` | admin, owner | SSOState `{"available", "secrets_encrypted", "scim_enabled", "email_verification_available", "domain_email_local_parts", "service_provider": {"oidc_redirect_uri", "oidc_post_logout_redirect_uri", "oidc_backchannel_logout_uri", "oidc_frontchannel_logout_uri", "scim_base_url", "saml_entity_id", "saml_acs_url", "saml_slo_url", "saml_slo_soap_url", "saml_metadata_url", "saml_certificate_pem"}, "connection": SSOConnection\|null, "connections": [SSOConnection]}`; `POST` → yeni bağlantıyla `201`; organizasyon başına ≤ 10 |
| `GET\|PUT\|DELETE /sso/connections/{id}` · `POST /sso/connections/{id}/test` · `POST /sso/connections/{id}/test/start` · `PUT /sso/connections/{id}/enforcement` · `GET\|PUT /sso/connections/{id}/role-mappings` | admin, owner (zorunlu kılma: owner) | Aşağıdaki tek bağlantılı endpoint'ler gibi, tek bir bağlantı için; `service_provider` onun SAML değerlerini tutar. `DELETE` oturumlarını bitirir ve alan adlarını varsayılan bağlantıya yönlendirir |
| `POST /sso/connections/{id}/refresh` | admin, owner | Hemen yenile; yeni `health` ile SSOState; bağlantı başına 10 dk'da 30 |
| `POST /sso/connections/{id}/metadata/accept` `{"digest"}` | admin, owner | `saml.pending_metadata` değerini onaylar (metadata yeniden çekilir ve hâlâ o değişikliği taşımalıdır); SSOState; o bekleyen değişiklik yoksa `409`; yenileme limitini paylaşır |
| `GET /sso/connection` · `PUT /sso/connection` · `DELETE /sso/connection` | admin, owner | **Varsayılan** bağlantı üzerinde tek bağlantılı API (D-077); yoksa `PUT` onu oluşturur. `client_secret` verilmezse = korunur, `""` = kaldırılır; SAML metadata'sı o anda `idp_metadata_url` adresinden çekilir (veya `idp_metadata_xml` yapıştırılır); her kayıt `config_version` değerini artırır. SSOConnection şunları ekler: `default`, `logout_redirect_allowlist`, `allow_external_invitations`, `saml.idp_slo_url`, `saml.metadata_signing_certificates` (parmak izleri), `saml.allow_unsigned_metadata`, `saml.pending_metadata` ve `health`; SSOConnectionInput ise `saml.metadata_signing_certificate_pem` ve `saml.allow_unsigned_metadata` ekler (D-098) |
| `POST /sso/connection/test` | admin, owner | Sunucu tarafı kontroller `{"ok", "checks": [{"name", "ok", "message"}]}` |
| `POST /sso/connection/test/start` | admin, owner | `{"redirect_url"}`; IdP'de yalnızca `last_test` kaydeden gerçek bir giriş (e-posta, gruplar, ortaya çıkan rol) |
| `PUT /sso/enforcement` `{"enforce", "break_glass_user_ids"}` | owner | Yukarıdaki güvenceler |
| `GET /sso/role-mappings` · `PUT /sso/role-mappings` `{"mappings": [{"group", "role"}]}` | admin, owner | Organizasyon geneli eşlemeler (SCIM ve kendi eşlemesi olmayan bağlantılardan girişler); `{"mappings", "connection_id"}`; `role` ∈ admin, member, viewer; ≤ 200; eşleşen en yüksek rol kazanır |
| `GET /sso/domains` · `POST /sso/domains` `{"domain"}` · `PUT /sso/domains/{id}` `{"connection_id"}` · `DELETE /sso/domains/{id}` | admin, owner | Organizasyon başına ≤ 20; eklemek için onaylanmış e-posta adresi gerekir; `connection_id` null = varsayılan bağlantı |
| `POST /sso/domains/{id}/verify` `{"method": "dns_txt"\|"email", "email_local_part"?}` | admin, owner | TXT kaydı yoksa veya alan adını başka bir organizasyon doğruladıysa `409`; e-posta: alan adı başına 10 dk'da 5, bağlantı 24 sa geçerli |
| `POST /sso/domains/verify-email` `{"token"}` | herkese açık | `/sso/verify-domain#token=oldv_…` bağlantısının token'ı |
| `GET /scim/tokens` · `POST /scim/tokens` `{"name", "expires_at"?}` · `DELETE /scim/tokens/{id}` | admin, owner | `POST` `{"token", "secret"}` değerini bir kez döner (`ols_` + 48 hex); ≤ 20 aktif |

Audit eylemleri: `sso.connection.create|update|delete|test|test_start|test_login|refresh|metadata_refresh|metadata_pending|metadata_accept`,
`sso.enforcement.update`, `sso.role_mappings.update`, `sso.domain.add|verify|remove|assign|verification_email`,
`sso.login`, `sso.login_failed` (neden), `sso.logout` (`via` `user`/`idp`/`idp_soap`/`oidc_backchannel`/`oidc_frontchannel`, iptal edilen oturumlar), `sso.logout_complete`,
`sso.logout_failed`, `user.login_refused` (parolayla giriş zorunlu kılma yüzünden reddedildi), `via`
`sso` ile `invitation.accept`, `via` `sso_jit`, `sso_groups`, `scim`, `scim_groups` ile `member.add` / `member.role_change` ve aşağıdaki SCIM
eylemleri.

## SCIM {#scim}

**`/api/scim/v2`** adresinde
`Authorization: Bearer ols_…` ile SCIM 2.0 provisioning (RFC 7643/7644 alt kümesi, D-078, kod `internal/scim`); SCIM token'ı yalnızca bu yollarda kimlik doğrular, API anahtarları ve oturumlar doğrulamaz.
`OPENLOG_SCIM_ENABLED=true` (varsayılan). Yanıtlar `application/scim+json`'dur; hatalar SCIM hata şemasını kullanır
(`status`, `scimType` `invalidFilter`, `invalidValue`, `invalidSyntax`, `invalidPath`, `noTarget`, `uniqueness`,
`mutability`, `tooMany`).

| Kaynak | İşlemler |
|---|---|
| `/ServiceProviderConfig`, `/ResourceTypes`, `/Schemas` | GET (patch ve filter destekli; bulk, sort, ETag, parola değişikliği yok) |
| `/Users` | GET (`filter=userName eq "…"` \| `externalId eq "…"`, `startIndex`, `count` ≤ 500), POST |
| `/Users/{id}` | GET, PUT, PATCH (`add`/`replace`/`remove`; yollar `active`, `userName`, `externalId`, `displayName`, `name`, `name.givenName`, `name.familyName`, `emails`, `emails[primary eq true].value`, `emails[type eq "work"].value`; yolsuz nesne; diğer attribute'lar kabul edilir ve yok sayılır), DELETE |
| `/Groups` | GET (`filter=displayName eq "…"` \| `externalId eq "…"`, `excludedAttributes=members`), POST |
| `/Groups/{id}` | GET, PUT, PATCH (`displayName`, `externalId`, `members` add/replace/remove, `members[value eq "<id>"]` remove), DELETE |

Anlam: bir SCIM kullanıcısı, birincil e-postası (veya
`userName`) olan openlog hesabının organizasyondaki üyeliğidir; adres organizasyonun **doğrulanmış bir alan adında** olmalıdır (aksi halde `400 invalidValue`); `id`,
openlog kullanıcı id'sidir. Aktif kullanıcı oluşturmak üyeliği ekler (adres yeniyse hesabı oluşturur,
rol SCIM gruplarından veya bağlantının `default_role` değerinden gelir, bağlantı yoksa viewer). `active=false` (`"False"`
string'leri de) veya DELETE üyeliği hemen kaldırır, kullanıcının organizasyona bağlı SSO oturumlarını
ve orada oluşturduğu API anahtarlarını iptal eder; diğer oturumlar sonraki isteklerinde organizasyonu kaybeder. Devre dışı
bırakılan kullanıcı JIT girişiyle yeniden eklenmez. Owner'lar devre dışı bırakılamaz veya silinemez (`400 mutability`) ve
rolleri asla değişmez. **E-posta değişikliği (D-089):** PUT veya PATCH'teki `emails` içindeki birincil (veya ilk) adres,
ya da önceki `userName` hesabın adresiyse değişen `userName`, şu koşullarla openlog hesabının
adresi olur: geçerliyse (`400 invalidValue`), organizasyonun doğrulanmış bir alan adındaysa (`400 invalidValue`), başka bir hesap
kullanmıyorsa (`409 uniqueness`), mevcut adres organizasyonun doğrulanmış bir alan adındaysa ve kullanıcı
owner değilse (`400 mutability`); audit: `scim.user.email_change` (`from`, `to`). Grup üyeleri
provision edilmiş kullanıcılar olmalıdır; grup değişiklikleri üyelerin rollerini rol eşlemeleri üzerinden yeniden hesaplar.
Audit eylemleri: `scim.user.create|update|activate|deactivate|delete|email_change`, `scim.group.create|update|delete`
(aktör `scim:<token name>`), `scim.token.create|revoke`.

## Sürüm {#version}

### `GET /api/v1/version` (kimliği doğrulanmış her principal, iki auth modu da) {#get-api-v1-version-any-authenticated-principal-both-auth-modes}
`{"version", "commit", "date", "latest_available": {"version", "notes_url", "checked_at"} | null,
"update_check": "enabled|disabled|failed", "updater": {…} | null}`. `latest_available`, yanıt veren pod'dan yeniyse `OPENLOG_UPDATE_CHANNEL` kanalının en yeni doğrulanmış
sürümüdür. `updater`, `openlog-updater`'ın durum belgesidir (`engine`, `mode`, `state`: `off|error|up_to_date|available|waiting_for_maintenance_window|updating|succeeded|failed|rolled_back|rollback_failed`,
`current_version`, `target_version`, `steps[]`, `failed_versions[]`, `history[]`, …; bkz. openapi `UpdaterStatus`).
`updater.message` İngilizcedir; sabit mesajlarda `updater.message_code` (`mode_off`, `update_available`,
`update_available_notify`, `waiting_for_window`, `updated`, `rolled_back`, `rollback_failed`, `failed_before_change`,
`up_to_date_newest`, `up_to_date_no_eligible`) ve `updater.message_params` (string'ler: `version`, `from`, `to`,
`channel`, `details`) istemcilerin çevirmesini sağlar; ikisi de eski updater'ların belgelerinde ve serbest metinde yoktur
(istemciler o zaman `message` gösterir). `update_requests.latest`, `message` değeri sabit bir updater mesajıysa aynı `message_code`/`message_params` değerlerini
taşır (ekli İngilizce hata için artı `error`).
`update_requests` (static modda null): `{"can_request", "updater_listening", "updater_polled_at", "latest": UpdateRequest | null}`;
`can_request` = çağıran aşağıdaki iki endpoint'i kullanabilir; `latest.requested_by_email` yalnızca `can_request` ise gelir.
`updater.notices[]`, `updater_outdated` / `updater_outdated_bundle` / `updater_outdated_kubernetes`
`{updater_version, running_version}` ve `updater_self_update_failed` `{version, reason}` taşıyabilir (D-120); `updater.updater_version`
updater'ın kendi sürümü, `updater.self_update` son self-update denemesidir. `updater_version` içermeyen bir belge için
(0.1.26'dan eski updater) API kendisi `updater_version: "< <running version>"` ile `updater_outdated` (Compose) veya `updater_outdated_kubernetes` ekler.
`updater.maintenance_window` (D-121), updater'ın `OPENLOG_UPDATER_MAINTENANCE_WINDOW` değeridir:
`{"spec": "sat,sun 02:00-05:00; mon-fri 03:00-04:00", "windows": [MaintenanceWindow], "open_now": false, "next_open_at": "2026-09-19T02:00:00Z"}`
(UTC; normalize edilmiş `spec`, `""` = `open_now: true` ile her zaman; pencere yoksa veya hiç kapanmıyorsa `next_open_at` yok).
API, `open_now`/`next_open_at` değerlerini yanıt anında yeniden hesaplar; alan eski updater'ların belgelerinde yoktur.
Kubernetes'te updater'ın ne zaman çalışacağına chart'ın CronJob zamanlaması karar verir.
Her API yanıtı (hatalar ve arayüz dahil) `X-Openlog-Version` taşır.

### `POST /api/v1/version/check` (admin, owner; postgres auth modu) {#post-api-v1-version-check-admin-owner-postgres-auth-mode}
"Check now": `openlog-updater` için `action=check` güncelleme isteğini kuyruğa alır ve API sürüm kontrolünü
hemen çalıştırır (`OPENLOG_UPDATE_CHECK=enabled`), sonra `GET /version` gövdesiyle `200` döner. Tüm kurulum için 30 sn'de en fazla bir kontrol
isteği: `Retry-After` (saniye) ile `429 resource_exhausted`. Audit:
`update.check_requested` (hedef `update_request`). API anahtarları ve daha düşük roller → `403`.
`OPENLOG_SIGNUP_ENABLED=true` ile organizasyon admin'leri ve owner'lar sunucu operatörü değildir: mevcut organizasyondaki rolleri ne olursa olsun yalnızca superadmin'ler (doğrulanmış e-postası `OPENLOG_SUPERADMIN_EMAILS` içinde olan oturum kullanıcıları)
çağırabilir; diğer herkes → `403`. Superadmin isteğinin audit ayrıntıları `"superadmin": true` taşır.

### `POST /api/v1/version/update` `{"target_version", "ignore_maintenance_window"?: false}` (admin, owner) {#post-api-v1-version-update-target-version-ignore-maintenance-window-false-admin-owner}
"Update now": kuyruğa alınan `UpdateRequest` ile `202` `{"id", "action": "apply", "target_version",
"ignore_maintenance_window", "state": "pending|running|done|failed|expired", "message", "requested_by_email",
"requested_at", "picked_at", "finished_at"}`. Updater sürümü `notify` modunda da kurar, yalnızca hâlâ
`target_version` seçiyorsa, bakım penceresi dışında ise yalnızca `ignore_maintenance_window: true` ile
(releases-updates.md §5.1). Geçersiz sürüm için `400`; sürüm yanıt veren pod'dan yeni değilse, hiçbir updater bildirmiyorsa, `OPENLOG_UPDATER_MODE=off` ise, bir güncelleme çalışıyorsa veya açık bir apply isteği varsa `409 failed_precondition`; önceki apply isteğinden sonraki 30 sn içinde `429`. Audit: `update.apply_requested` (`details.from`, `to`,
`ignore_maintenance_window`, `engine`). İlerleme: `GET /version` değerini yokla (`update_requests.latest`, `updater.state`,
`updater.steps`); Compose updater istekleri `OPENLOG_UPDATER_REQUEST_POLL` (10 sn) içinde alır, Kubernetes
CronJob'u sonraki çalışmasında.

## Onboarding {#onboarding}

### `GET /api/v1/onboarding` (organizasyonu olan her principal, iki auth modu da) {#get-api-v1-onboarding-any-principal-with-an-organization-both-auth-modes}
Web arayüzünün **Add data** sayfasının (`/add-data`) girdileri; sayfa kopyala-yapıştır kurulum komutlarını tarayıcıda oluşturur.
`Cache-Control: no-store` ile `200`; kimlik doğrulanmamışsa `401`, organizasyon yoksa `403`. Sır yoktur: lisans anahtarı değerleri
yalnızca `POST /api/v1/license-keys` tarafından bir kez döner ve kullanıcının sayfaya yapıştırdığı anahtar sunucuya asla ulaşmaz.

```json
{"ui_url": {"url": "https://openlog.example.com", "source": "configured"},
 "otlp_http": {"url": "https://openlog.example.com:4318", "source": "derived_public_url"},
 "otlp_grpc": {"url": "https://openlog.example.com:4317", "source": "derived_public_url"},
 "server_version": "0.9.1", "agent_version": "0.9.2", "release_channel": "stable",
 "cors_enabled": false, "cors_allowed_origins": [], "auth_mode": "postgres",
 "organization": {"id": "…", "tenant_id": "default", "name": "Default"}, "role": "admin",
 "features": {"license_keys": true, "can_create_license_keys": true, "can_list_license_keys": true,
              "fleet_php_install": true, "tail_sampling": false},
 "agent_packages": {
   "node": {"name": "openlog-node", "version": "0.9.2", "registry": "missing",
            "registry_url": "https://www.npmjs.com/package/openlog-node/v/0.9.2",
            "release_asset_url": "https://github.com/onuragtas/openlog/releases/download/v0.9.2/openlog-node-0.9.2.tgz",
            "release_asset_sha256_url": "https://github.com/onuragtas/openlog/releases/download/v0.9.2/openlog-node-0.9.2.tgz.sha256"},
   "python": {"name": "openlog-agent", "version": "0.9.2", "registry": "unknown", …},
   "dotnet": {"name": "OpenLog.Agent", "version": "0.9.2", "registry": "available", …}}}
```

- `source`: `configured` (`OPENLOG_PUBLIC_URL`, `OPENLOG_INGEST_PUBLIC_URL`, `OPENLOG_INGEST_PUBLIC_GRPC_URL`),
  `derived_public_url` (`OPENLOG_PUBLIC_URL` şeması ve host'u, port 4318/4317), `derived_ingest_url` (gRPC:
  `OPENLOG_INGEST_PUBLIC_URL` host'u, port 4317) veya `derived_request` (tarayıcının kullandığı şema ve host; düzgün biçimli
  `X-Forwarded-Proto`/`X-Forwarded-Host` önceliklidir). Arayüz türetilen endpoint'leri kullanıcıya onaylatır.
- `agent_version`: sürüm kontrolü bulduysa `release_channel` kanalının en yeni doğrulanmış sürümü, yoksa
  sunucunun kendi sürümü; geliştirme build'lerinde `null` (komutlar o zaman `latest` indirme URL'leri kullanır).
- `cors_enabled` / `cors_allowed_origins`: bu API pod'unun `OPENLOG_INGEST_CORS_ALLOWED_ORIGINS` değeri (tarayıcıdan OTLP/JSON loglar).
- `features.can_create_license_keys`: postgres modunda giriş yapmış admin veya owner; `can_list_license_keys`:
  member veya üstü, API anahtarları da; `fleet_php_install`: fleet endpoint'leri var (php-agent.md §7.3); `tail_sampling`:
  `OPENLOG_TAILSAMPLING_ENABLED`.

## Host'lar {#hosts}

### `GET /api/v1/hosts?q=&sort=&dir=&limit=&offset=&usage=` {#get-api-v1-hosts-q-sort-dir-limit-offset-usage}
`sort`: `name` (varsayılan), `os`, `agent`, `last_seen`, `cpu`, `memory`, `disk`, `load`. `q`, adda, id'de, işletim sisteminde, mimaride, ajan sürümünde veya bir `key=value` resource attribute'unda hepsi geçmesi gereken kelimelerdir. Kullanım değeri olmayan host, her iki yönde de sona düşer. Liste Go'da sıralanır ve aranır, çünkü kullanım başka bir tabloda durur ve attribute'lar bir map'tir, yani ikisi de `OPENLOG_API_MAX_ROWS` host'a kadar eksiksizdir. Yanıt `has_more` taşır (bkz. [Listeler](#lists)).

Son 24 saatte görülen host'lar, `host_name`'e göre sıralı, her biri şu an ne kadar yoğun olduğuyla birlikte.
```json
{"hosts": [{"host_id": "…", "host_name": "web-1", "os_description": "Ubuntu 24.04 LTS", "arch": "amd64",
            "agent_version": "0.1.0", "last_seen": "2026-09-13T10:00:00Z", "resource_attributes": {"env": "prod"},
            "usage": {"cpu": 0.34, "memory": 0.71, "disk": 0.88, "load1": 2.4, "load_per_cpu": 0.6}}]}
```

`usage`, host'un kendi grafiklerinin çizdiği metriklerin son 5 dakikalık ortalamasıdır
(idle payı çıkarılmış `system.cpu.utilization`, `used` durumundaki `system.memory.utilization`,
`system.filesystem.utilization`, `system.cpu.load_average.1m`) ve tüm sayfa için **tek** sorguda
hesaplanır. İçindeki iki tercih bilinçlidir: `disk`, mountpoint'lerin ortalaması değil **en dolu**
dosya sistemidir, çünkü ortalama dolmak üzere olan tek bölümü gizler; ve host pencerede böyle bir metrik göndermediyse her alan `0` değil `null` olur,
çünkü bildirimi kesen bir ajan boşta bir makine gibi okunmamalıdır. `usage=false` özeti dışarıda bırakır.

### `GET /api/v1/hosts/{host_id}` {#get-api-v1-hosts-host-id}
Tek host nesnesi (aynı şekil) veya `404`.

Her `/hosts/{host_id}/…` endpoint'i (`metrics`, `inventory`, `services`) önce host'un çağıranın organizasyonunda host kaydı olduğunu kontrol eder,
yoksa `404 not_found` (`"host not found"`) döner. Başka bir organizasyonun host'u bilinmeyen bir host'tan ayırt edilemez.
Parametre hataları (`400`) bu kontrolden önce bildirilir.

## Container'lar {#containers}

Altyapı ajanının bildirdiği container'lar: cgroup metrikleri, `openlog.container.status` ve `container.restarts` veri noktaları
(semantic-conventions.md §2 "Container metrics"), `containers` tablosunda tutulur (0009_containers, son
veri noktasından sonra 30 gün). Container `container_id` (64 hex) ile tanımlanır; son veri noktası
5 dakikadan eskiyse `reporting` false olur. Telemetri izinleri (her rol, API anahtarları da); her sorgu tenant kapsamındadır. 64 hex karakter olmayan `container_id` yol
parametreleri → `400 invalid_argument`.

**Container nesnesi** (`Container`):
```json
{"container_id": "3f4e…", "name": "openlog-agents-orders-1", "image_name": "openlog-apmdemo/orders", "image_tags": ["1"],
 "runtime": "docker", "host_id": "…", "host_name": "docker-desktop", "compose_project": "openlog-agents",
 "compose_service": "orders", "k8s_pod_name": "", "k8s_namespace_name": "", "k8s_container_name": "",
 "state": "running", "health": "healthy", "started_at": "2026-09-14T09:00:00.5Z", "restart_count": 0,
 "first_seen": "…", "last_seen": "…", "reporting": true,
 "cpu_utilization": 0.012, "memory_usage": 18350080, "memory_limit": 8233447424,
 "cpu_sparkline": [[1757757600000, 0.011]], "memory_sparkline": [[1757757600000, 18350080]]}
```
`state`, Docker durumudur (`running`, `paused`, `restarting`, `exited`, `created`, `dead`) veya ajan hiç göndermiyorsa `""`
(Docker API erişimi yok); `health`, `healthy`/`unhealthy`/`starting` veya `""`'dir; bilinmiyorsa `started_at` `null` olur.
`cpu_utilization` (host'un 0..1'i), `memory_usage` ve `memory_limit` (byte; limitsizse host belleği) aralığın son
bucket'ıdır, veri noktası yoksa `null`.

### `GET /api/v1/containers?host_id=&compose_project=&compose_service=&state=&q=&from=&to=&limit=&sort=&dir=&offset=` {#get-api-v1-containers-host-id-compose-project-compose-service-state-q-from-to-limit-sort-dir-offset}
`[from, to]` aralığında (varsayılan son bir saat) verisi olan container'lar, aksi istenmedikçe compose project, compose service ve ada göre sıralı:
`{"containers": [Container…], "total": 12, "step": "120s", "has_more": false}`. `total` sayfadan önceki eşleşmeleri sayar; sparkline'larda
`step` genişliğinde ≈ 30 bucket vardır. `sort`: `name`, `state` (durumun okunduğu şekilde, yani `not reporting` ve `unhealthy` kendi gruplarıdır), `image`, `compose` (servis, sonra project), `host`, `last_seen`; tek başına `dir` `name`'e göre sıralar; bkz. [Listeler](#lists). Liste 10000 satıra kadar okunur, sonra Go'da filtrelenip sıralanır ve sayfaya kesilir, yani sırası ve sayfaları ancak o kadarına kadar eksiksizdir. CPU ve bellek metriklerden gelir, yalnızca sayfanın satırları için okunur, bu yüzden bunlara göre sıralanamaz. `compose_project`/`compose_service` tam eşleşmedir, var ama boş = onsuz container'lar;
`state` yukarıdaki durumlardan biri veya `unknown`'dır (aksi halde `400`); `q` (≤ 256 byte), boşlukla ayrılmış her
terimi büyük/küçük harf duyarsız olarak id, ad, image, tag'ler, host, compose ve Kubernetes adlarıyla eşleştirir.

### `GET /api/v1/containers/groups?host_id=&state=&q=&from=&to=` {#get-api-v1-containers-groups-host-id-state-q-from-to}
Aynı container'lar, compose project ve servise göre gruplanmış. `running`, `running` durumundaki bildirim yapan container'ları sayar;
`cpu_utilization` ve `memory_usage`, bildirim yapan container'ların son değerlerini toplar (veri yoksa `null`;
`OPENLOG_API_MAX_ROWS` container üstünde hesaplanmaz).
```json
{"projects": [{"compose_project": "openlog-agents", "host_ids": ["…"], "containers": 9, "running": 9,
  "services": [{"compose_service": "orders", "containers": 1, "running": 1, "cpu_utilization": 0.01, "memory_usage": 18350080}]}]}
```

### `GET /api/v1/containers/{container_id}?from=&to=` {#get-api-v1-containers-container-id-from-to}
Container'ın son kaydı (en son görüldüğü host) ve aralık üzerindeki istatistikleri, artı `attributes` (son durum noktasının veri noktası
attribute'ları) veya `404`.

### `GET /api/v1/containers/{container_id}/timeseries?from=&to=&step=` {#get-api-v1-containers-container-id-timeseries-from-to-step}
Ham veri noktalarından grafik serileri (`step`: Go süresi ≥ `10s`, varsayılan ≈ 300 nokta; 30 gün saklama), bilinmeyen container için
`404`. Gauge'ların bucket başına ortalaması alınır; `network_*` ve `blockio_*`, kümülatif
sayaçların seri başına saniyelik oranlarıdır (sıfırlanmalar 0'a sabitlenir), seriler üzerinden toplanır.
```json
{"container_id": "…", "host_id": "…", "step": "20s", "from": 1757757600000, "to": 1757761200000,
 "series": {"cpu_utilization": [[1757757600000, 0.01]], "memory_usage": [], "memory_limit": [], "network_receive": [],
            "network_transmit": [], "blockio_read": [], "blockio_write": []}}
```

### `GET /api/v1/containers/{container_id}/services?from=&to=` {#get-api-v1-containers-container-id-services-from-to}
Span resource'larında bu `container.id` geçen APM servisleri (`apm_service_containers`, apm.md §1), aralık üzerindeki RED nesnesiyle:
`{"services": [{"service_name", "service_namespace", "environment", "first_seen", "last_seen", "apdex_t_ms", …ApmRed}]}`.

Container logları: `GET /api/v1/logs?container_id=…` (bkz. Loglar).

## Maliyetler {#costs}

Altyapı maliyet tahminleri ([cost.md](/tr/contracts/cost), D-134): her host, ajanının bildirdiği bulut instance bilgilerinden
(semantic-conventions.md §1) repository'deki statik bir fiyat tablosuna göre fiyatlanır ve bu fiyat, 1 dakikalık rollup'ın CPU ve belleğiyle host üzerindeki
servislere ve container'lara bölünür. Telemetri izinleri (her rol,
API anahtarları da); her sorgu tenant kapsamındadır. `OPENLOG_COST_ENABLED` ile etkinleşir (varsayılan açık); kapalıyken bu
endpoint'ler yoktur (`404 not_found`, `"no such endpoint"`).

**Bu sayılar tahmindir, fatura verisi değildir.** Committed-use ve savings plan indirimlerini, kurumsal
anlaşmaları, kredileri, vergileri, lisansları, storage'ı, ağ çıkışını ve desteği hesaba katmazlar. Her yanıt bir `pricing` nesnesi taşır
(`{"version", "updated", "currency", "note", "estimated": true, "override_file"?}`), böylece hiçbir sayı
uyarısız gösterilmez; arayüz notu gösterir. Bunları servisleri karşılaştırmak ve boşta kapasite bulmak için kullan, asla
fatura mutabakatı için kullanma.

**Host maliyeti nasıl bölünür** (formül ve varsayımları cost.md §3'te). İstenen aralık boyunca her host
`price per hour × hours it reported` kadar katkı yapar ve bu toplam, her zaman ona eşit olan dört bucket'a ayrılır:

| Bucket | Anlamı |
|---|---|
| `services` | bir APM servisine bağlı container'lar (`apm_service_containers`, apm.md §1) |
| `unallocated` | openlog'un gördüğü ama bir servise bağlayamadığı container'lar |
| `unattributed` | hiçbir container'ın açıklamadığı host kullanımı, örneğin doğrudan makinede çalışan bir veritabanı |
| `idle` | kimsenin hiç kullanmadığı kapasite |

Idle servislere **asla** dağıtılmaz: boşta bir makine yuvarlama hatası değil, bulgunun kendisidir. Instance
tipi bilinmeyen host vCPU ve GB başına fiyatlanır; ne instance bilgisi ne kapasitesi olan host
`"priced": false` ile bildirilir ve `unpriced_hosts` içinde sayılır, asla maliyetsiz sayılmaz.

**Özet nesnesi** (`CostSummary`): `{"currency", "total", "services", "unallocated", "unattributed", "idle",
"idle_share", "per_hour", "hosts", "priced_hosts", "unpriced_hosts", "host_hours"}`. `per_hour` çalışma hızıdır
(`total / host_hours`), `idle_share` ise `idle / total`'dır.

**Host maliyet nesnesi** (`CostHost`): host'un kimliği ve instance bilgileri (`host_id`, `host_name`, `provider`,
`instance_type`, `region`, `zone`, `lifecycle`, `vcpus`, `memory_bytes`), `hours`, çözümlenmiş `price`
(`{"usd_per_hour", "source": "table"|"override"|"fallback"|"none", "note"?, "region_multiplier"}`), dört bucket,
`used_share`/`idle_share`, `oversubscribed` (container'ların payları host'un kullanımını aştı ve sığacak şekilde
ölçeklendi; host toplamı tam kalır) ve `priced`.

### `GET /api/v1/costs/summary?from=&to=` {#get-api-v1-costs-summary-from-to}
`{"summary": CostSummary, "pricing": {…}, "from": 1757757600000, "to": 1757761200000}`.

### `GET /api/v1/costs/hosts?from=&to=&sort=&dir=&limit=&offset=` {#get-api-v1-costs-hosts-from-to-sort-dir-limit-offset}
`sort`: `cost` (varsayılan), `idle`, `host`, `instance`. Fiyatı olmayan host'un maliyeti ve idle payı yoktur, her iki yönde de sona düşer. Yanıt `has_more` taşır (bkz. [Listeler](#lists)).

Host'lar maliyete göre sıralı, en pahalısı başta: `{"hosts": [CostHost…], "total": 12, "summary": CostSummary, "pricing": {…}}`.
`total`, `limit` öncesindeki host'ları sayar.

### `GET /api/v1/costs/services?from=&to=&sort=&dir=&limit=&offset=` {#get-api-v1-costs-services-from-to-sort-dir-limit-offset}
`sort`: `cost` (varsayılan), `containers`, `service`, `environment`. Yanıt `has_more` taşır (bkz. [Listeler](#lists)).

`{"services": [{"service_name", "service_namespace", "environment", "total", "hosts": ["…"], "containers": 3}…],
"total": 8, "summary": CostSummary, "pricing": {…}}`, maliyete göre sıralı. İki ortamdaki aynı servis adı
iki satırdır.

### `GET /api/v1/costs/containers?host_id=&from=&to=&limit=` {#get-api-v1-costs-containers-host-id-from-to-limit}
`{"containers": [{"container_id", "container_name", "host_id", "host_name", "service_name", "total", "cpu_share",
"memory_share", "share"}…], "total": 40, "summary": CostSummary, "pricing": {…}}`, maliyete göre sıralı. `share`, container'ın
host'undan tuttuğu orandır; bağlı servis yoksa `service_name` `""`'dir.

### `GET /api/v1/costs/hosts/{host_id}?from=&to=` {#get-api-v1-costs-hosts-host-id-from-to}
Tek host'un maliyet kartı veya host'un çağıranın organizasyonunda kaydı yoksa `404`:
`{"host": CostHost, "services": [CostService…], "containers": [CostContainer…], "pricing": {…}, "from", "to"}`.

### `GET /api/v1/costs/trend?from=&to=&step=` {#get-api-v1-costs-trend-from-to-step}
Zaman içinde çalışma hızı: `{"step": "3600s", "points": [{"t": 1757757600000, "total": 0.42, "idle": 0.21}…], "pricing": {…},
"from", "to"}`. `step` en az `1m` olan bir Go süresidir (varsayılan ≈ 400 nokta, en fazla 400 bucket); her bucket yalnızca
kendi dakikaları ve kullanımıyla fiyatlanır.

### `GET /api/v1/costs/prices` {#get-api-v1-costs-prices}
Geçerli fiyat tablosu, yani operatörün override'ı birleştirilmiş yerleşik tablo; böylece bir düzeltme container'ın
dosya sistemini okumadan doğrulanabilir: `{"version", "updated", "currency", "note", "reference_regions",
"sources", "instances", "region_multipliers", "fallback", "override_file"?, "overridden_keys"?}`.

## Kubernetes {#kubernetes}

Altyapı ajanının node ve cluster modunda bildirdiği Kubernetes cluster'ları, node'ları, workload'ları, pod'ları ve olayları
(semantic-conventions.md §7), `k8s_clusters`, `k8s_nodes`, `k8s_workloads` ve `k8s_pods` entity tablolarında tutulur
(şema 0040–0042, son veri noktasından sonra 30 gün). Telemetri izinleri (her rol, API anahtarları da); her sorgu
tenant kapsamındadır. Her endpoint `from`/`to` kabul eder (varsayılan son bir saat). Zaman damgaları RFC3339 string'dir, sparkline'lar
`[[unix_ms, value], …]`, null olabilen sayılar veri yoksa `null`'dır. String filtreler ve yol parçaları en fazla 256 byte olur
(aksi halde `400 invalid_argument`).

- Cluster `cluster_uid` (resource `k8s.cluster.uid`, `kube-system`'in uid'i) ile tanımlanır ve `cluster_name` ile adlandırılır.
- `reporting`: nesnenin son veri noktası en fazla 5 dakikalıktır (cluster ajanı aralığı 30 sn).
- **Güncel nesneler**: bir cluster'ın sayıları (node'lar, pod'lar, workload'lar) ile bir node veya workload'un pod'ları yalnızca
  son noktası üst nesnenin son noktasından en fazla 2 dakika eski olan nesneleri içerir, böylece silinen nesneler
  bir toplamadan sonra düşer ve bildirimi kesen cluster son bilinen sayılarını korur.
- Workload `health`: bildirim yapmıyorsa `unknown`; Deployment/StatefulSet/DaemonSet/ReplicaSet
  `available >= desired` ise `healthy` (desired 0 → healthy), `available == 0 < desired` ise `unavailable`, aksi halde `degraded`; Job
  `updated` (başarısız) > 0 ve `available` (başarılı) < `desired` ise `degraded`, aksi halde `healthy`; CronJob `healthy`.
- Pod `status`: ayarlıysa `reason` (örneğin `CrashLoopBackOff`, `Evicted`), aksi halde `phase`.
- Kullanım metrikleri: `cpu_usage` (çekirdek) ve `memory_working_set` (byte) node ajanlarının kubelet metriklerinden gelir
  (`k8s.node.*`, `k8s.pod.*`, `k8s.container.*`, resource `k8s.cluster.name` ve attribute'larla eşleştirilir), allocatable
  kaynaklar ve request/limit'ler cluster ajanından gelir (`cluster_uid` ile eşleştirilir). "Son değer",
  `[from, to]` içindeki en yeni noktadır; node veya pod'lar üzerinden toplamlar yalnızca en yeniden en fazla 2 dakika uzaktaki değerleri içerir.

**Cluster nesnesi** (`KubernetesCluster`):
```json
{"cluster_uid": "5b1c…", "cluster_name": "prod", "version": "v1.31.2", "first_seen": "…", "last_seen": "…", "reporting": true,
 "nodes": 3, "nodes_ready": 3, "pods": {"Pending": 0, "Running": 41, "Succeeded": 2, "Failed": 0, "Unknown": 0},
 "pods_not_ready": 1, "workloads": 18, "workloads_unhealthy": 1, "namespaces": ["default", "kube-system", "shop"]}
```
`pods_not_ready`, hazır olmayan güncel `Running` pod'ları sayar; `workloads_unhealthy`, `degraded` ve
`unavailable` workload'ları sayar; `namespaces`, aralıkta pod'u veya workload'u olan her namespace'i listeler.

### `GET /api/v1/kubernetes/clusters?from=&to=` {#get-api-v1-kubernetes-clusters-from-to}
Aralıkta verisi olan cluster'lar, ada göre sıralı: `{"clusters": [KubernetesCluster…]}`.

### `GET /api/v1/kubernetes/clusters/{cluster_uid}?from=&to=` {#get-api-v1-kubernetes-clusters-cluster-uid-from-to}
Bir cluster (saklama süresi içinde herhangi bir zamanda) veya `404`, şunlarla: `workloads_by_kind` (güncel workload'lar üzerinden, türe göre `[{"kind", "total", "healthy", "degraded",
"unavailable", "unknown"}]`), `warning_events` (aralıktaki son 20 `Warning` olayı,
en yenisi başta, aşağıdaki Event nesneleri), `cpu_usage` / `memory_working_set` (node'lar üzerinden son `k8s.node.cpu.usage` /
`k8s.node.memory.working_set` toplamı) ve `allocatable_cpu` / `allocatable_memory` (son
`k8s.node.allocatable_*` toplamı).

### `GET /api/v1/kubernetes/nodes?cluster_uid=&q=&from=&to=&limit=&sort=&dir=&offset=` {#get-api-v1-kubernetes-nodes-cluster-uid-q-from-to-limit-sort-dir-offset}
Aralıkta görülen node'lar, aksi istenmedikçe cluster adı ve node adına göre sıralı: `{"nodes": [KubernetesNode…], "total": 3, "has_more": false}` (`total` sayfadan önceki).
`sort`: `name`, `status` (en kötüden başlar), `kubelet`, `last_seen`, `cluster`; tek başına `dir` `name`'e göre sıralar; bkz. [Listeler](#lists). Liste 10000 satıra kadar okunur, sonra Go'da filtrelenip sıralanır ve sayfaya kesilir, yani sırası ve sayfaları ancak o kadarına kadar eksiksizdir. Kullanım, pod sayıları ve host yalnızca sayfanın satırları için okunur, bu yüzden bunlara göre sıralanamaz.
```json
{"cluster_uid": "…", "cluster_name": "prod", "node_name": "worker-1", "node_uid": "…", "ready": "true", "unschedulable": false,
 "roles": ["worker"], "kubelet_version": "v1.31.2", "os_image": "Ubuntu 24.04 LTS", "container_runtime": "containerd://1.7.22",
 "internal_ip": "10.0.0.11", "created_at": "2026-01-01T00:00:00Z", "allocatable_cpu": 4, "allocatable_memory": 16364216320,
 "allocatable_pods": 110, "cpu_usage": 0.82, "memory_working_set": 5242880000, "pods": 23, "host_id": "…", "host_name": "worker-1",
 "first_seen": "…", "last_seen": "…", "reporting": true, "conditions": [{"condition": "Ready", "status": "true"}]}
```
`ready`, `true`, `false` veya `unknown`'dır; `pods` node üzerindeki güncel `Running` ve `Pending` pod'ları sayar; `host_id` /
`host_name`, resource attribute'ları node'un `k8s.cluster.name` ve `k8s.node.name` değerlerini taşıyan host'tur (node ajanı bildirmiyorsa
`null`); `conditions` son `k8s.node.condition` değerleridir (`true`/`false`/`unknown`, ada göre). `q`,
her terimi node adı, uid, cluster adı, roller, IP, kubelet sürümü ve OS image ile eşleştirir.

### `GET /api/v1/kubernetes/workloads?cluster_uid=&namespace=&kind=&health=&q=&from=&to=&limit=&sort=&dir=&offset=` {#get-api-v1-kubernetes-workloads-cluster-uid-namespace-kind-health-q-from-to-limit-sort-dir-offset}
Aralıkta görülen workload'lar, aksi istenmedikçe cluster adı, namespace, tür ve ada göre sıralı:
`{"workloads": [KubernetesWorkload…], "total": 18, "step": "120s", "has_more": false}`. `sort`: `name`, `namespace`, `kind`, `health` (en kötüden başlar), `replicas` (hazır olanlar; CronJob'da yoktur ve sona düşer), `cluster`; tek başına `dir` `name`'e göre sıralar; bkz. [Listeler](#lists). Liste 10000 satıra kadar okunur, sonra Go'da filtrelenip sıralanır ve sayfaya kesilir, yani sırası ve sayfaları ancak o kadarına kadar eksiksizdir. Kullanım, sparkline'lar, `pods` ve `restarts` yalnızca sayfanın satırları için okunur, bu yüzden bunlara göre sıralanamaz. `kind` ∈ `Deployment`, `StatefulSet`, `DaemonSet`,
`Job`, `CronJob`, `ReplicaSet`, `Pod` (cluster ajanı `Pod` workload'u bildirmez, yani hiçbir şeyle eşleşmez); `health` ∈
`healthy`, `degraded`, `unavailable`, `unknown`; diğer değerler → `400`.
```json
{"cluster_uid": "…", "cluster_name": "prod", "namespace": "shop", "kind": "Deployment", "name": "orders", "uid": "…",
 "desired": 3, "ready": 2, "available": 2, "updated": 3, "health": "degraded", "pods": 3, "restarts": 7,
 "cpu_usage": 0.12, "memory_working_set": 314572800, "cpu_sparkline": [[1757757600000, 0.1]], "memory_sparkline": [],
 "created_at": "2026-09-01T10:00:00Z", "first_seen": "…", "last_seen": "…", "reporting": true}
```
`desired`/`ready`/`available`/`updated` türe göre semantic-conventions §7.4'teki gibidir; `pods` ve `restarts` (`openlog.k8s.pod.restarts` toplamı) workload'un güncel pod'ları üzerindendir; `cpu_usage` / `memory_working_set`, pod'larının son
`k8s.pod.cpu.usage` / `k8s.pod.memory.working_set` değerlerini toplar, sparkline'lar pod başına bucket ortalamalarını toplar (`step` genişliğinde ≈ 30 bucket).

### `GET /api/v1/kubernetes/workloads/{cluster_uid}/{namespace}/{kind}/{name}?from=&to=` {#get-api-v1-kubernetes-workloads-cluster-uid-namespace-kind-name-from-to}
Bir workload (saklama süresi içinde herhangi bir zamanda) veya `404`, artı `pod_list` (workload'un aralıkta görülen Pod nesneleri, en fazla
`OPENLOG_API_MAX_ROWS`), `hpa` (onu hedefleyen HorizontalPodAutoscaler'ın `{"name", "min_replicas", "max_replicas", "current_replicas", "desired_replicas"}`
bilgisi, son `k8s.hpa.*` değerleri veya `null`) ve `attributes` (son `openlog.k8s.workload.status` noktasının veri noktası
attribute'ları). Bilinmeyen `kind` → `400`.

### `GET /api/v1/kubernetes/workloads/{cluster_uid}/{namespace}/{kind}/{name}/timeseries?from=&to=&step=` {#get-api-v1-kubernetes-workloads-cluster-uid-namespace-kind-name-timeseries-from-to-step}
`{"step": "20s", "from": 1757757600000, "to": 1757761200000, "series": {"cpu_usage": [], "memory_working_set": [],
"ready": [], "desired": [], "restarts": []}}` (`step` container'lardaki gibi; bilinmeyen workload için `404`). `cpu_usage` ve
`memory_working_set` pod başına bucket ortalamalarını toplar; `ready`/`desired` bucket başına son durum değerleridir;
`restarts`, bucket başına pod başına son restart sayısını toplar.

### `GET /api/v1/kubernetes/pods?cluster_uid=&namespace=&node=&workload_kind=&workload_name=&phase=&q=&from=&to=&limit=&sort=&dir=&offset=` {#get-api-v1-kubernetes-pods-cluster-uid-namespace-node-workload-kind-workload-name-phase-q-from-to-limit-sort-dir-offset}
Aralıkta görülen pod'lar, aksi istenmedikçe cluster adı, namespace ve pod adına göre sıralı: `{"pods": [KubernetesPod…], "total": 41, "has_more": false}`.
`sort`: `name`, `namespace`, `status` (durumun okunduğu şekilde, yani `not reporting` kendi grubudur), `restarts`, `workload`, `node`, `ip` (adrese göre), `age` (en eskiden başlar), `cluster`; tek başına `dir` `name`'e göre sıralar; bkz. [Listeler](#lists). Liste 10000 satıra kadar okunur, sonra Go'da filtrelenip sıralanır ve sayfaya kesilir, yani sırası ve sayfaları ancak o kadarına kadar eksiksizdir. Kullanım yalnızca sayfanın satırları için okunur, bu yüzden ona göre sıralanamaz.
`phase` ∈ `Pending`, `Running`, `Succeeded`, `Failed`, `Unknown`; `workload_kind` yukarıdaki `kind` gibi; diğer değerler → `400`.
```json
{"cluster_uid": "…", "cluster_name": "prod", "namespace": "shop", "pod_name": "orders-7d9c-x2k", "pod_uid": "…",
 "node_name": "worker-1", "workload_kind": "Deployment", "workload_name": "orders", "phase": "Running", "ready": false,
 "reason": "CrashLoopBackOff", "status": "CrashLoopBackOff", "restarts": 7, "pod_ip": "10.244.1.17", "qos_class": "Burstable",
 "created_at": "…", "started_at": "…", "cpu_usage": 0.01, "memory_working_set": 52428800,
 "first_seen": "…", "last_seen": "…", "reporting": true}
```

### `GET /api/v1/kubernetes/pods/{pod_uid}?from=&to=` {#get-api-v1-kubernetes-pods-pod-uid-from-to}
Bir pod (saklama süresi içinde herhangi bir zamanda) veya `404`, artı:
- `containers`: `openlog.k8s.pod.containers` içinden `[{"name", "container_id", "image", "ready", "restarts", "state", "reason", "known", "host_id", "cpu_usage",
  "memory_working_set", "cpu_request", "cpu_limit", "memory_request", "memory_limit"}]`;
  container `containers` tablosundaysa `known` true olur (ve `host_id` ayarlanır, bkz. Container'lar); kullanım son
  `k8s.container.*` kubelet metriklerinden, request/limit'ler cluster ajanından gelir (ayarlı değilse `null`).
- `labels`: bilinen container'larının son durum noktalarındaki `k8s.pod.label.*` attribute'ları (önek kaldırılmış).
- `services`: span'leri container id'lerinden birini taşıyan `[{"service_name", "service_namespace", "deployment_environment"}]` (`apm_service_containers`).
- `host_id` / `host_name`: node'un host'u veya `null`.

### `GET /api/v1/kubernetes/pods/{pod_uid}/timeseries?from=&to=&step=` {#get-api-v1-kubernetes-pods-pod-uid-timeseries-from-to-step}
`{"step", "from", "to", "series": {"cpu_usage": [], "memory_working_set": [], "network_receive": [], "network_transmit": [], "restarts": []}}`
(bilinmeyen pod için `404`): kubelet gauge'larının bucket başına ortalaması, `network_*` `k8s.pod.network.io`'nun saniyelik oranları
(sıfırlanmalar 0'a sabitlenir), `restarts` bucket başına son `openlog.k8s.pod.restarts`.

### `GET /api/v1/kubernetes/events?cluster_uid=&namespace=&type=&object_kind=&object_name=&object_uid=&reason=&from=&to=&limit=` {#get-api-v1-kubernetes-events-cluster-uid-namespace-type-object-kind-object-name-object-uid-reason-from-to-limit}
Aralıktaki Kubernetes olayları (`event_name` `k8s.event` olan log kayıtları, semantic-conventions §7.5), en yenisi başta:
`{"events": [KubernetesEvent…]}`. Bir olayın güncellemeleri (aynı `k8s.event.uid`) son kayıt olarak bir kez döner.
`type` ∈ `Normal`, `Warning` (aksi halde `400`); `limit` varsayılan 100, en fazla 1000.
```json
{"timestamp": "…", "type": "Warning", "reason": "BackOff", "message": "Back-off restarting failed container app",
 "count": 12, "namespace": "shop", "object_kind": "Pod", "object_name": "orders-7d9c-x2k", "object_uid": "…",
 "source": "kubelet", "cluster_uid": "…", "cluster_name": "prod"}
```

### `GET /api/v1/kubernetes/pods/{pod_uid}/events?from=&to=&type=&reason=&limit=` {#get-api-v1-kubernetes-pods-pod-uid-events-from-to-type-reason-limit}
İlgili nesnesi pod olan olaylar (`object_uid` = `pod_uid`; pod'un container olayları aynı uid'i taşır).

### `GET /api/v1/apm/services/{service_name}/kubernetes?namespace=&environment=&from=&to=` {#get-api-v1-apm-services-service-name-kubernetes-namespace-environment-from-to}
Bir APM servisinin aralıkta görülen pod'ları: `openlog.k8s.pod.containers` değeri `from`'dan beri servise bağlı bir container id içeren pod'lar
(`apm_service_containers`) veya servisin span'lerinin aralıkta resource `k8s.pod.uid` olarak taşıdığı uid'e sahip pod'lar.
`{"pods": [{"cluster_uid", "cluster_name", "namespace", "pod_name", "pod_uid", "workload_kind", "workload_name",
"node_name", "phase", "ready", "reporting"}]}`, cluster, namespace ve pod adına göre sıralı (en fazla 1000).

Pod logları: `GET /api/v1/logs?k8s_pod_uid=…` (bkz. Loglar).

## Metrikler {#metrics}

### `GET /api/v1/metrics/names?host_id=&from=&to=` {#get-api-v1-metrics-names-host-id-from-to}
`{"names": [{"name": "system.cpu.utilization", "type": "gauge", "unit": "1"}]}`

### `GET /api/v1/hosts/{host_id}/metrics?name=&from=&to=&step=&agg=&group_by=` {#get-api-v1-hosts-host-id-metrics-name-from-to-step-agg-group-by}

| Parametre | Varsayılan | Notlar |
|---|---|---|
| `name` | zorunlu | metrik adı |
| `step` | otomatik (≈ 300 nokta) | Go süresi, en az `10s` |
| `agg` | gauge'lar için `avg`, monotonic sum'lar için `rate`, non-monotonic sum'lar için `last` | `avg`, `min`, `max`, `sum`, `last`, `rate` |
| `group_by` | tüm attribute'lar | virgülle ayrılmış attribute anahtarları; seriler bu anahtarlara göre birleştirilir. `resource.<key>` (izin verilen anahtarlar aşağıda) bir resource attribute'una göre gruplar, `attributes` içinde `resource.<key>` olarak bildirilir |
| `resource.<key>` | — | tam eşleşmeli resource attribute filtresi (AND'lenir, anahtar başına bir değer, en fazla 4) |

`rate` = seri başına, step başına saniyelik artış (sayaç sıfırlanmaları 0'a sabitlenir), sonra birleştirilen seriler üzerinden toplanır.
6 saatten uzun aralıklar 1 dakikalık rollup tablosundan okur; `resource.*` filtresi veya gruplaması olan istekler hariç
(rollup'ta resource attribute'ları yoktur), onlar ham veri noktalarını okur (30 gün saklama).

İzin verilen `resource.<key>` anahtarları: entegrasyon instance kimliği, PostgreSQL varlıkları ve IIS siteleri/uygulama havuzları
(semantic-conventions §6.1, §6.5, §6.8): `openlog.discovery.id`, `openlog.discovery.instance`, `openlog.integration.id`,
`service.instance.id`, `server.address`, `server.port`, `postgresql.database.name`, `postgresql.table.name`,
`postgresql.index.name`, pg_stat_statements sorgu resource'ları için `postgresql.queryid`, `postgresql.rolname`,
`db.query.text` ve `iis.site`, `iis.application_pool`. Başka herhangi bir anahtar, boş veya
tekrarlanan değer ya da 1024 byte'tan uzun değer → `400 invalid_argument` (host kontrolünden önce bildirilir). Değerler
bağlı sorgu parametreleridir. Entegrasyon panelleri tek bir instance'ı
`?resource.openlog.discovery.id=redis&resource.openlog.discovery.instance=/usr/bin/redis-server` ile seçer.

```json
{"metric": {"name": "system.cpu.utilization", "type": "gauge", "unit": "1"}, "step": "60s",
 "series": [{"attributes": {"cpu.mode": "user"}, "points": [[1757757600000, 0.12]]}]}
```

### `GET /api/v1/metrics?from=&to=&q=&limit=` (Metrics Explorer, D-119) {#get-api-v1-metrics-from-to-q-limit-metrics-explorer-d-119}
Aralıkta veri noktası olan her metrik, herhangi bir resource'tan (yalnızca altyapı ajanı host'larından değil). 6 saate kadarki aralıklar ham veri
noktalarını okur; daha uzun aralıklar son 6 saatin ham noktalarına 1 dakikalık rollup'ı (gauge ve sum'lar; description veya temporality yok) ekler.
`q`: metrik adında **veya** onu gönderen herhangi bir serviste büyük/küçük harf duyarsız alt dize; `limit` varsayılan 1000, en fazla
5000 (`truncated`).
```json
{"metrics": [{"name": "http.server.request.duration", "type": "histogram", "unit": "s", "description": "…",
              "temporality": "cumulative", "monotonic": false, "last_seen": "…", "series": 42, "services": ["checkout"]}],
 "truncated": false}
```

### `GET /api/v1/metrics/{name}?from=&to=` {#get-api-v1-metrics-name-from-to}
Tek metriğin `MetricInfo` değeri, artı `attribute_keys` ve `resource_keys` (`GET /api/v1/fields/keys` gibi, en fazla 500),
`aggregations` ve `default_aggregation`. Ad `/` içerebilir. Aralıkta veri noktası yoksa → `404`.

### `POST /api/v1/metrics/query` `{"metric", "from"?, "to"?, "filters"?, "groups"?, "aggregation"?, "group_by"?, "step"?, "limit"?}` {#post-api-v1-metrics-query-metric-from-to-filters-groups-aggregation-group-by-step-limit}
[Alanlar](#fields) filtre koşullarıyla tek metriğin zaman serisi (metrik anahtarları: `metric.name`, `metric.type`,
`unit`, `service.name`, `host.id`, `host.name`, `scope.name`, `value`, `attributes.<k>`, `resource.<k>`, yalın anahtarlar).
`group_by`: en fazla 5 anahtar; her serinin `attributes` değeri her anahtarın kanonik biçimini (takma adlar çözülür, `attr.k` → `attributes.k`; yalın anahtarlar yalın kalır) değerine eşler. `step`: Go süresi ≥ 10s, varsayılan
≈ 300 nokta (en fazla 11000 nokta). `limit`: seri, varsayılan 50, en fazla 200 (`truncated`).

| Tür | Agregasyonlar (ilki = varsayılan) | Hesaplama |
|---|---|---|
| gauge | `avg`, `min`, `max`, `sum`, `last`, `count` | seri başına step başına, sonra grup üzerinden (`last` = serilerin son değerlerinin toplamı) |
| monotonic sum | `rate`, `increase`, `sum`, `last` | cumulative: seri başına ardışık step'ler arasındaki artış (sıfırlanmadan sonra yeni değer), `rate` = artış / saniye; delta: noktaların toplamı (`rate` / step) |
| non-monotonic sum | `last`, `avg`, `min`, `max`, `sum`, `count` | gauge'lar gibi |
| histogram, exponential_histogram | `p95`, `p50`, `p75`, `p90`, `p99`, `avg`, `count`, `sum`, `rate` | seri başına bucket sayılarının step başına artışı (cumulative) veya toplamı (delta), grup başına birleştirilir (başka bucket düzenine sahip seriler atlanır); kantiller bucket içinde doğrusal interpolasyonla (sınırı pozitifse ilk bucket 0'dan başlar, taşma bucket'ı = en büyük sınır); `avg` = toplam / sayı, `rate` = sayı / saniye |
| summary | `avg`, `p50`, `p75`, `p90`, `p95`, `p99`, `count`, `sum` | kantiller: o seviyenin saklanan kantil değerleri, grubun serileri üzerinden ortalaması alınır (veride olmayan seviyeler nokta vermez); count/sum step başına artış |

6 saatten uzun aralıklar, her filtre ve `group_by` anahtarı orada mevcutsa gauge ve sum'ları 1 dakikalık rollup'tan okur
(`attributes.<k>`, `metric.name`, `metric.type`, `unit`, `service.name`, `host.id`); aksi halde ham veri noktalarını
(30 gün saklama). Histogram ve summary'ler her zaman ham veri noktalarını okur, en fazla 200000 (seri, step) satır
(`truncated`). Bilinmeyen metrik → boş `series` ve `metric.type` `""` ile `200`; tür için mevcut olmayan
agregasyon → `400`.
```json
{"metric": {"name": "http.server.request.duration", "type": "histogram", "unit": "s", "temporality": "cumulative", "monotonic": false},
 "aggregation": "p95", "step": "60s", "truncated": false,
 "series": [{"attributes": {"resource.service.name": "checkout"}, "points": [[1757757600000, 0.182]]}]}
```

### `POST /api/v1/metrics/exemplars` `{"metric", "from"?, "to"?, "filters"?, "groups"?, "limit"?}` {#post-api-v1-metrics-exemplars-metric-from-to-filters-groups-limit}
Bir metriğin veri noktalarının arkasındaki trace'ler (D-130), böylece grafikteki bir sıçrama gerçek bir trace olarak açılır. OTLP veri noktaları
exemplar taşır (trace id, span id, tek ölçüm, zaman damgası ve SDK'nın metriğin kendi attribute'larından çıkardığı
attribute'lar), processor da bunları veri noktalarının tanımlayıcı sütunlarıyla `metric_exemplars` içinde saklar (şema `0092_metric_exemplars`).
Bu yüzden `filters` ve `groups` tam olarak `POST /api/v1/metrics/query` koşullarıdır (aynı anahtarlar), yani exemplar'lar her zaman grafiğin çizdiği seriye aittir.

Sonuçlar ilk *n* değil, **aralığa yayılmıştır**: aralık `limit` bucket'a bölünür (her biri en az 10s)
ve her bucket'ın en büyük değerli exemplar'ı, zaman sırasıyla döner. `limit` varsayılan 50, en fazla 500. `total`,
aralıktaki eşleşen her exemplar'ı sayar, yalnızca dönenleri değil; `truncated` daha fazlası olduğunu bildirir.

Saklama: exemplar'lar 30 günlük metrik saklamasını değil, **trace** saklamasını izler (varsayılan 7 gün), çünkü exemplar
bir trace'e işaretçidir ve daha uzun saklama artık var olmayan trace'lere bağlanırdı. Trace id'si olmayan exemplar'lar
saklanmaz, OTLP summary veri noktaları da exemplar taşımaz, yani summary metriği asla exemplar döndürmez.
```json
{"exemplars": [{"timestamp": "2026-09-17T11:42:13.512000000Z", "value": 7.5,
                "trace_id": "5b8efff798038103d269b633813fc60c", "span_id": "eee19b7ec3c1b174",
                "service_name": "checkout", "attributes": {"http.route": "/pay"},
                "filtered_attributes": {"http.status_code": "500"}}],
 "total": 342, "truncated": true}
```

## Envanter ve keşif {#inventory-and-discovery}

### `GET /api/v1/hosts/{host_id}/inventory?category=` {#get-api-v1-hosts-host-id-inventory-category}
Host'un son **tamamlanmış** snapshot'ının öğeleri. Henüz tamamlanmış snapshot'ı olmayan bilinen host, `snapshot_id: ""` ve boş `items` ile `200` döner.
```json
{"snapshot_id": "…", "snapshot_time": "…", "items": [{"category": "package", "key": "dpkg:openssl", "data": {"name": "openssl", "version": "3.0.13"}}]}
```

### `GET /api/v1/hosts/{host_id}/services` {#get-api-v1-hosts-host-id-services}
`category=discovered_service` kısayolu; `data`, keşfedilen servis gövdesidir.

### `GET /api/v1/inventory/search?category=&q=&limit=` {#get-api-v1-inventory-search-category-q-limit}
Tüm host'ların son tamamlanmış snapshot'ları üzerinde: `category` (zorunlu) içinde key'i `q` içeren öğeler (büyük/küçük harf duyarsız).
`{"items": [{"host_id": "…", "host_name": "…", "category": "package", "key": "dpkg:openssl", "data": {…}}], "has_more": false}`. `has_more`, `limit`in geçirdiğinden fazla öğenin eşleştiğini söyler. Bu listede `offset` yoktur.

## Loglar {#logs}

### `GET /api/v1/logs?host_id=&service=&container_id=&compose_project=&compose_service=&k8s_pod_uid=&q=&severity_min=&trace_id=&span_id=&transaction=&transaction_service=&attr.<key>=&from=&to=&limit=&cursor=` {#get-api-v1-logs-host-id-service-container-id-compose-project-compose-service-k8s-pod-uid-q-severity-min-trace-id-span-id-transaction-transaction-service-attr-key-from-to-limit-cursor}
Envanter olaylarını (`event.name` değeri `openlog.inventory.` ile başlayanlar) hariç tutar. `q`, body üzerinde büyük/küçük harf duyarsız alt dize eşleşmesidir. En yenisi başta.

**Sayfalama.** Satırlar (`timestamp`, satır anahtarı) azalan sıralıdır; satır anahtarı, satırın içeriğinin hash'idir
(gözlem zamanı, host, servis, severity, trace/span id'leri, body, attribute'lar). Dolu bir sayfa
`next_cursor` döner (opak string, yoksa `null`); sonraki (daha eski) sayfayı aynı filtreler, `from` ve `to` artı
`cursor=<next_cursor>` ile iste. Cursor son satırın zaman damgasını (nanosaniye), satır anahtarını ve tam olarak o konumdaki
kaç satırın zaten döndüğünü tutar, böylece aynı nanosaniyedeki satırlar, birebir aynı olsalar bile, tam bir kez döner.
Geçersiz cursor → `400`. `to` değerini kaydırarak sayfalayan istemciler hâlâ çalışır (her yanıt yine en yenisi başta gelir).

`span_id` tek bir span ile sınırlar (küçük harfe çevrilir). `transaction` + `transaction_service` (ikisi birlikte zorunlu, aksi halde `400`)
aralıkta o transaction'ın entry span'i olan trace'lerle sınırlar (en fazla 10000 trace; APM
"logs of this transaction").

`attr.<key>=<value>`, log kaydı attribute'u üzerinde tam eşleşme filtresidir (AND'lenir, anahtar başına bir değer, sorgu
parametresi olarak bağlanır). İzin verilen anahtarlar, altyapı ajanının ayarladığı attribute'lar (semantic-conventions §4):
`openlog.log.source` (`file` / `journald` / `container`), `log.file.path`, `log.file.name`, `openlog.discovery.id`,
`openlog.systemd.unit`, `openlog.syslog.identifier`, `log.iostream` (`stdout` / `stderr`). Başka herhangi bir `attr.*` anahtarı, boş veya tekrarlanan değer ya da
1024 byte'tan uzun değer → `400 invalid_argument`. Ajan log kayıtlarının `service_name` değeri boştur, bu yüzden host log görünümleri
`host_id` artı bu filtreleri kullanır, örneğin `?host_id=…&attr.openlog.discovery.id=nginx&attr.log.file.path=/var/log/nginx/error.log`.
`container_id`, `compose_project` ve `compose_service`, container loglarının `container.id`
(küçük harfe çevrilir), `docker.compose.project` ve `docker.compose.service` resource attribute'ları üzerinde tam eşleşmedir (semantic-conventions §4).
`k8s_pod_uid`, Kubernetes container loglarının `k8s.pod.uid` resource attribute'u üzerinde tam eşleşmedir (semantic-conventions §7.2).
```json
{"logs": [{"timestamp": "…", "severity_text": "ERROR", "severity_number": 17, "body": "…", "host_id": "…",
           "service_name": "…", "trace_id": "…", "span_id": "…", "attributes": {}, "resource_attributes": {}}],
 "next_cursor": "eyJ2IjoxLCJ0IjoxNzU3NzU3NjAwMDAwMDAwMDAwLCJrIjoiNDIiLCJuIjoxfQ"}
```

### `POST /api/v1/logs/query` `{"from"?, "to"?, "filters"?, "groups"?, "q"?, "transaction"?, "transaction_service"?, "order"?, "limit"?, "cursor"?, "columns"?, "include_record"?}` {#post-api-v1-logs-query-from-to-filters-groups-q-transaction-transaction-service-order-limit-cursor-columns-include-record}
[Alanlar](#fields) filtre koşullarıyla Logs Explorer'ın yapılandırılmış araması (D-118). `from`/`to`: RFC3339 veya
unix ms (varsayılan son bir saat); `q`: büyük/küçük harf duyarsız body alt dizesi (≤ 1024 byte); `order`: `desc` (varsayılan) veya
`asc`; `limit` varsayılan 100, `OPENLOG_API_MAX_ROWS` ile sınırlı. Sayfalama `GET /api/v1/logs` gibidir: aynı gövdeyi
`cursor` = `next_cursor` ile gönder (diğer sıranın cursor'ı → `400`). `columns` (≤ 50 anahtar) `fields` içinde
string olarak döner, kayıtta olmayan anahtarlar atlanır; `include_record`, `attributes` ve `resource_attributes` ekler.
`transaction` + `transaction_service` (ikisi birden veya hiçbiri, aksi halde `400`), `GET /api/v1/logs` gibi tek bir APM transaction'ının trace'leriyle
sınırlar (D-122). Envanter olayları hariçtir. Bilinmeyen gövde alanları → `400`.
```json
{"rows": [{"id": "1757757600123456789-9f3a…", "timestamp": "…", "observed_timestamp": "…", "severity_text": "ERROR",
           "severity_number": 17, "body": "…", "service_name": "checkout", "host_id": "…", "host_name": "…",
           "trace_id": "…", "span_id": "…", "fields": {"attributes.http.route": "/api/orders", "resource.k8s.pod.name": "checkout-7d9"},
           "attributes": {}, "resource_attributes": {}}],
 "next_cursor": null}
```
`id`, bir listelemede satırı tanımlar (zaman damgası ve satır anahtarı, birebir aynı satırlar için son ekle).

### `POST /api/v1/logs/aggregate` `{"from"?, "to"?, "filters"?, "groups"?, "q"?, "transaction"?, "transaction_service"?, "step"?, "group_by"?, "limit"?}` {#post-api-v1-logs-aggregate-from-to-filters-groups-q-transaction-transaction-service-step-group-by-limit}
Aynı koşullar için zaman bucket'ı başına kayıt sayıları (Logs Explorer histogramı). `step`: Go süresi ≥ 1s (en fazla 10000
bucket); varsayılan, en fazla 120 bucket veren 1s, 2s, 5s, 10s, 15s, 30s, 1m, 2m, 5m, 10m, 15m, 30m, 1h, 2h, 3h, 6h, 12h, 1d, 7d değerlerinin en küçüğü. `group_by` (filtrelenebilir bir anahtar) sayıları en sık `limit` (varsayılan 10, en fazla 50)
değere, sıklığa göre sıralı olarak böler, artı kalan tüm değerler için bir `other: true` serisi; eksik map anahtarı `""` olarak gruplanır.
Kaydı olmayan bucket'lar atlanır.
```json
{"step": "60s", "total": 1520,
 "series": [{"group": "ERROR", "other": false, "total": 120, "points": [[1757757600000, 4]]},
            {"group": "", "other": true, "total": 30, "points": [[1757757600000, 1]]}]}
```

### `POST /api/v1/logs/patterns` `{"from"?, "to"?, "filters"?, "groups"?, "q"?, "transaction"?, "transaction_service"?, "limit"?}` {#post-api-v1-logs-patterns-from-to-filters-groups-q-transaction-transaction-service-limit}
Aynı koşullarla eşleşen kayıtların ardındaki farklı mesajlar (Logs Explorer "Patterns", D-128): processor
her log body'sinin değişken kısımlarını maskeleyerek bir şablon çıkarır (`user <*> logged in from <*>`) ve şablonu ve
id'sini kayda yazar, yani bu endpoint `POST /api/v1/logs/query` ile aynı filtreler üzerinde bir `GROUP BY`'dır. Pattern'ler
kayıt sayısına göre sıralıdır, `limit` varsayılan 50, en fazla 500 (`truncated` daha fazlası olduğunu bildirir).

`pattern_id`, **string olarak** 64 bit bir id'dir (JSON sayıları 2^53 üstünde hassasiyet kaybeder); aynı zamanda filtre anahtarıdır, yani
`POST /api/v1/logs/query` üzerinde `{"key": "pattern_id", "op": "=", "value": "<id>"}` tam olarak bir
pattern'in kayıtlarını listeler. `count / total`, pattern'in eşleşen kayıtlardaki payıdır. `severity` kayıtları
OpenTelemetry severity aralığına göre sayar ve `sample` pattern'in aralıktaki en yeni kaydıdır.

`unclassified`, pattern'i olmayan eşleşen kayıtları sayar: boş body veya
`0091_log_patterns` migration'ından önce saklanmış kayıt (backfill yoktur; logların TTL'i 14 gündür). Bunlar sayılır, asla listelenmez, çünkü
şablonları yoktur.

Tek kısıtı zaman aralığı olan **ve** aralığı en az 6 saat olan istek saatlik
rollup'tan (`log_patterns_1h`) yanıtlanır ve `rollup: true` bildirir: sayılar o zaman aralığın tam saatlerini kapsar, örnek
yalnızca body, servis ve zamanını taşır ve `unclassified` `0`'dır (rollup pattern'siz kayıt saklamaz).
Her filtreli istek ve her daha kısa aralık ham tabloyu tam olarak okur, yani filtre eklemek sayıların anlamını asla değiştirmez.
```json
{"patterns": [{"pattern_id": "10748583442455546363", "template": "user <*> logged in from <*>", "count": 40012,
               "severity": {"unspecified": 0, "trace": 0, "debug": 0, "info": 39990, "warn": 0, "error": 22, "fatal": 0},
               "max_severity_number": 17, "services": ["checkout"],
               "first_seen": "…", "last_seen": "…",
               "sample": {"timestamp": "…", "body": "user 4711 logged in from 10.0.0.3", "service_name": "checkout",
                          "severity_text": "INFO", "severity_number": 9, "trace_id": ""}}],
 "total": 64512, "unclassified": 12, "rollup": false, "truncated": false}
```

## Alanlar {#fields}

Sorgu oluşturucuların attribute keşfi (Logs ve Metrics Explorer, D-118). Her rol ve API anahtarları (`telemetry.read`),
her telemetri endpoint'i gibi tenant kapsamlı; organizasyon sorgu limitleri geçerlidir (`422`/`429`), timeout'lar `504`'tür.

**Anahtarlar.** Filtreler, sütunlar ve `group_by` veriyi tek bir anahtar söz dizimiyle adlandırır:

| Anahtar | Okuduğu |
|---|---|
| üst düzey alan | loglar: `timestamp`\*, `observed_timestamp`\*, `body`, `severity_text` (takma adı `severity`), `severity_number`, `service.name`, `host.id`, `host.name`, `trace_id`, `span_id`, `trace_flags`, `event.name`, `scope.name`; trace'ler: `timestamp`\*, `name`, `kind`, `status_code`, `status_message`, `service.name`, `service.namespace`, `deployment.environment`, `host.id`, `trace_id`, `span_id`, `parent_span_id`, `duration_ns`, `duration_ms` (sayı), `http.status_code` (sayı), `is_entry` (bool), `error` (bool), `transaction.name`, `transaction.type`, `db.system`, `peer.name`, `scope.name` (apm.md'nin APM sütunları; aynı adlı span attribute'larını gölgelerler, onlar için `attributes.<k>` kullan); metrikler: `metric.name`, `metric.type`, `unit`, `service.name`, `host.id`, `host.name`, `scope.name`, `value` |
| `attributes.<k>` (veya `attr.<k>`) | kayıt / span / veri noktası attribute'u `k` |
| `resource.<k>` | resource attribute'u `k` |
| `body.<k>[.<k2>…]` | loglar: JSON body'de o yoldaki değer (string'ler tırnaksız, diğer JSON değerleri metin olarak; yol öğeleri `[A-Za-z0-9_-@$:]`, en fazla 8) |
| başka herhangi bir anahtar | varsa kayıt attribute'u, yoksa resource attribute'u |

\* yalnızca görüntüleme (zaman aralığı onları filtreler). Anahtarlar kontrol karakteri içermeyen 1–256 byte'tır; `tenant_id` reddedilir.
Anahtarlar ve değerler her zaman bağlı sorgu parametreleridir.

### `GET /api/v1/fields/keys?signal=logs|metrics|traces&from=&to=&q=&metric=&limit=` {#get-api-v1-fields-keys-signal-logs-metrics-traces-from-to-q-metric-limit}
Önce üst düzey alanlar (`source: field`, `count`/`cardinality` null), sonra saatlik anahtar index'i `attribute_keys`'ten sıklığa göre sıralı
attribute ve resource anahtarları (ClickHouse `0080_attribute_keys`: ham tablolardaki materialized view'lar
tenant, sinyal, metrik, saat ve anahtar başına kayıtları, yaklaşık farklı değer sayısıyla ve `type` değerini veren
sayısal / boolean değer payıyla sayar). Index'te aralık için satır yoksa (migration'dan önce yazılmış veri),
anahtarlar aralığın son saatindeki en fazla 20000 kayıttan gelir ve `sampled` true olur. `signal=logs`
ayrıca JSON body'lerin üst düzey anahtarlarını döner (`source: body`, body'si
`{` ile başlayan, son 15 dakikadaki en fazla 2000 kayıttan). `q`: anahtarda büyük/küçük harf duyarsız alt dize; `metric` (yalnızca metrikler): o metriğin anahtarları; `limit`
varsayılan 200, en fazla 1000.
```json
{"keys": [{"key": "service.name", "name": "service.name", "source": "field", "type": "string", "count": null, "cardinality": null},
          {"key": "attributes.http.status_code", "name": "http.status_code", "source": "attribute", "type": "number", "count": 1520, "cardinality": 7},
          {"key": "resource.k8s.pod.name", "name": "k8s.pod.name", "source": "resource", "type": "string", "count": 1520, "cardinality": 12}],
 "sampled": false}
```

### `GET /api/v1/fields/values?signal=&key=&from=&to=&q=&metric=&filters=&groups=&body_q=&transaction=&transaction_service=&root_only=&limit=` {#get-api-v1-fields-values-signal-key-from-to-q-metric-filters-groups-body-q-transaction-transaction-service-root-only-limit}
`key` için sayılarıyla en sık değerler, aralıktaki en fazla 100000 eşleşen kayıttan (örnek dolduysa `sampled: true`,
sayılar o zaman görecelidir). Map ve JSON body anahtarları yalnızca anahtara sahip kayıtları sayar.
`filters`: filtre koşullarının URL kodlu JSON dizisi (aşağıda), `groups`: koşul dizilerinin URL kodlu JSON dizisi
(AND gruplarının OR'u); `key`'in kendisi üzerindeki koşullar yok sayılır, böylece diğer değerler görünür kalır. `q`: değerde büyük/küçük harf duyarsız
alt dize. Explorer'ın diğer koşulları (D-122): `body_q` (loglar: büyük/küçük harf duyarsız body alt dizesi),
`transaction` + `transaction_service` (loglar, `POST /logs/query` gibi), `root_only=true` (trace'ler). `total`, sayılan
kayıt sayısıdır (yalnızca dönenler değil, her değer), yani `count / total` bir değerin payıdır ("top values"). Dönen her değer sayı olarak ayrıştırılıyorsa `type`
`number`'dır. `limit` varsayılan 50, en fazla 1000.
`{"key": "attributes.http.route", "type": "string", "values": [{"value": "/api/orders", "count": 912}], "total": 1520, "sampled": false}`

**Filtre koşulları** (aşağıdaki POST endpoint'lerinin `filters`, `groups` alanları): `{"key", "op", "value"? , "values"?}`.

| `op` | Anlamı |
|---|---|
| `=`, `!=` | eşitlik (string; sayısal üst düzey alanlarda sayısal). Eksik map anahtarı `""` olarak okunur |
| `in`, `not_in` | `values`: 1–100 |
| `contains`, `not_contains` | büyük/küçük harf duyarsız alt dize; `%`, `_` ve `\` düz karakterdir (OQL `CONTAINS` ile aynı, D-122) |
| `like`, `not_like` | SQL `LIKE` (`%`, `_`; büyük/küçük harf duyarlı) |
| `regex`, `not_regex` | RE2 (ClickHouse `match`), en fazla 512 byte, doğrulanır |
| `exists`, `not_exists` | map / JSON anahtarı var; üst düzey string alan boş değil; değer almaz |
| `>`, `>=`, `<`, `<=` | sayısal; sayı olmayan attribute değerleri asla eşleşmez |

Değerler en fazla 1024 byte'lık string, sayı veya boolean'dır (`trace_id`/`span_id` değerleri küçük harfe çevrilir).
`filters` AND'lenir; `groups`, AND gruplarının OR'udur (`[[a, b], [c]]` = `(a AND b) OR c`) ve `filters` ile AND'lenir; boş
grup her şeyle eşleşir. En fazla 50 koşul ve 10 grup. Geçersiz anahtar, op veya değerler → `400`.

## Kayıtlı görünümler {#saved-views}

Adlandırılmış Logs/Metrics Explorer durumları (PostgreSQL `saved_views`, `0080_saved_views`; D-118). Yalnızca PostgreSQL auth modunda
(aksi halde `404`). Okumalar: her rol ve API anahtarları; organizasyon geneli görünümler ve çağıranın kendi özel görünümleri (admin'ler
oluşturanı silinmiş özel görünümleri de görür). Yazmalar: member ve üstü, API anahtarları da (anahtar görünüm sahibi olmaz, D-133); başka bir kullanıcının
görünümünü değiştirmek veya silmek admin/owner ve organizasyon geneli (veya sahipsiz) görünüm ister. Organizasyon başına en fazla 500 görünüm (`409`).
Audit: `saved_view.{create,update,delete}`.

### `GET /api/v1/saved-views?signal=` · `POST /api/v1/saved-views` · `GET|PUT|DELETE /api/v1/saved-views/{id}` {#get-api-v1-saved-views-signal-·-post-api-v1-saved-views-·-get-put-delete-api-v1-saved-views-id}
POST/PUT gövdesi: `{"signal": "logs"|"metrics"|"traces", "name" (1–200 characters), "description"? (≤ 2000),
"visibility": "private"|"org", "state": {…}}`; `state`, verildiği gibi saklanan en fazla 32 KiB'lık bir JSON nesnesidir (web arayüzü
içinde `filters`, `groups`, `q`, `columns`, `order`, `group_by` ve zaman aralığını tutar).
```json
{"id": "…", "signal": "logs", "name": "Checkout errors", "description": "", "visibility": "org",
 "state": {"filters": [{"key": "service.name", "op": "=", "value": "checkout"}], "columns": ["body"]},
 "created_by_user_id": "…", "created_by_email": "ada@example.com", "can_edit": true, "created_at": "…", "updated_at": "…"}
```

## Trace'ler {#traces}

### `POST /api/v1/traces/query` `{"from"?, "to"?, "filters"?, "groups"?, "root_only"?, "order"?, "sort"?, "limit"?, "cursor"?, "columns"?, "include_record"?}` {#post-api-v1-traces-query-from-to-filters-groups-root-only-order-sort-limit-cursor-columns-include-record}
[Alanlar](#fields) filtre koşullarıyla (sinyal
`traces`) Traces Explorer'ın yapılandırılmış span araması (D-122). `root_only`: parent span id'si olmayan span'ler. `order`, `limit`, `cursor`, `columns`, `include_record`,
`POST /api/v1/logs/query` gibidir (satırlar zaman damgasına göre sıralı, eşitlikte trace ve span id'sine göre). `sort`: `timestamp` (varsayılan) veya
`duration`: tek sayfada en yavaş `limit` span (`order` desc, cursor yok, `next_cursor` null). Organizasyon sorgu limitleriyle tenant kapsamlı
(`422`/`429`, `504`). Bir satırı `GET /traces/{trace_id}` ile aç.
```json
{"rows": [{"id": "1757757600123456789-7a1f…", "timestamp": "…", "trace_id": "…", "span_id": "…", "parent_span_id": "",
           "name": "GET /orders/{id}", "kind": "server", "status_code": "error", "status_message": "",
           "service_name": "checkout", "host_id": "…", "duration_ns": 412000000, "duration_ms": 412, "is_entry": true,
           "is_error": true, "http_status_code": 500, "transaction_name": "GET /orders/{id}",
           "fields": {"attributes.http.route": "/orders/{id}"}, "attributes": {}, "resource_attributes": {}}],
 "next_cursor": "…"}
```

### `POST /api/v1/traces/aggregate` `{"from"?, "to"?, "filters"?, "groups"?, "root_only"?, "step"?, "group_by"?, "limit"?}` {#post-api-v1-traces-aggregate-from-to-filters-groups-root-only-step-group-by-limit}
Bucket başına span sayıları (`step`, `group_by`, `limit`, `series` tam olarak `POST /api/v1/logs/aggregate` gibi), artı
eşleşen tüm span'lerin bucket başına milisaniye cinsinden süre yüzdelikleri (`latency`, `group_by` ile bölünmez; ClickHouse
`quantiles`, büyük bucket'larda yaklaşık). Span'i olmayan bucket'lar atlanır.
```json
{"step": "60s", "total": 1520, "series": [{"group": "checkout", "other": false, "total": 900, "points": [[1757757600000, 15]]}],
 "latency": {"p50": [[1757757600000, 12.5]], "p95": [[1757757600000, 180]], "p99": [[1757757600000, 410.2]]}}
```

### `GET /api/v1/traces/{trace_id}` {#get-api-v1-traces-trace-id}
Trace'in tüm span'leri başlangıç zamanına göre sıralı veya `404`.
```json
{"trace_id": "…", "spans": [{"span_id": "…", "parent_span_id": "…", "name": "GET /users", "kind": "server",
  "service_name": "…", "start": "…", "duration_ns": 1234567, "status_code": "ok", "status_message": "",
  "attributes": {}, "resource_attributes": {}, "events": [{"timestamp": "…", "name": "…", "attributes": {}}]}]}
```

## APM {#apm}

Trace'lerden türetilen uygulama performans verisi. Tanımlar (transaction'lar, hata grupları, sampling ağırlıkları,
latency histogramı, Apdex, servis haritası birleştirme kuralları) [apm.md](/tr/contracts/apm) içindedir; bu bölüm endpoint'leri listeler.
`PUT …/settings` dışında telemetri izinleri (her rol, API anahtarları da).

Ortak parametreler: `from`/`to` (varsayılan son bir saat), `step` (Go süresi ≥ `60s`, varsayılan ≈ 60 nokta, tam dakikalara
yukarı yuvarlanır; 60s altında `400`). `/apm/services/{service_name}/…` üzerinde: `namespace`, `environment`; verilmezse servisin her
namespace/ortamı, verilirse (boş da olsa) tam eşleşme. Veri dakika başınadır: ilk bucket
dakikaya kesilmiş `from` ile başlar. Süreler milisaniyedir; `throughput` dakika başına istek (veya çağrı) sayısıdır;
`error_rate` ve `apdex` 0..1'dir; istek yoksa `avg_ms`, `p50_ms`, `p95_ms`, `p99_ms`, `apdex` `null` olur. Tüm
sayılar sampling olasılığıyla ağırlıklıdır (apm.md §4).

**RED nesnesi** (`ApmRed`): `{"requests", "throughput", "errors", "error_rate", "avg_ms", "p50_ms", "p95_ms", "p99_ms", "apdex"}`.

### `GET /api/v1/apm/services?from=&to=&step=&namespace=&environment=&q=&sort=&dir=&limit=&offset=` {#get-api-v1-apm-services-from-to-step-namespace-environment-q-sort-dir-limit-offset}
`from`'dan beri span'i olan servisler (`q`: büyük/küçük harf duyarsız kelimeler, her biri ad, namespace, ortam, dil veya sürümde bir yerde bulunur), ada göre sıralı. `sort` = `name` (varsayılan) | `environment` | `throughput` | `error_rate` | `p95` | `apdex` | `last_seen`, bkz. [Listeler](#lists). `limit` yoksa yanıt, ad sırasıyla `OPENLOG_API_MAX_ROWS` kadar her servistir. Sıralama, arama ve sayfa bu okumadan sonra gerçekleşir, yani ancak o kadarına kadar eksiksizdir. `environments`, `environment`, `q` ve sayfanın satırları neye daraltmış olursa olsun görülen her ortamı listeler:
```json
{"step": "60s", "services": [{"service_name": "orders", "service_namespace": "shop", "environment": "prod",
  "language": "go", "version": "1.4.2", "last_seen": "…", "apdex_t_ms": 500, "requests": 5400, "throughput": 90,
  "errors": 27, "error_rate": 0.005, "avg_ms": 41.2, "p50_ms": 18.3, "p95_ms": 120.4, "p99_ms": 480.0, "apdex": 0.97,
  "sparkline": [[1757757600000, 88.0]]}]}
```

### `GET /api/v1/apm/agents?from=&to=&service=&namespace=&environment=&upgrade=` {#get-api-v1-apm-agents-from-to-service-namespace-environment-upgrade}
`from`'dan beri her servisi bildiren dil ajanları ve sürümlerinin en yeni sürümle karşılaştırması (D-124,
[agent-updates.md](/tr/operations/agent-updates)). Dil ajanları uygulama bağımlılıklarıdır ve kendilerini asla
güncellemez; `upgrade` komutu taşır. Ayrı bir endpoint (`GET /apm/services` alanı değil), böylece servis
listesi ucuz kalır ve registry kontrolleri yalnızca komutların gösterildiği yerde çalışır; `upgrade=false` onları atlar.
```json
{"release": {"catalog": "ok", "channel": "stable", "latest": "0.1.31", "oldest_supported": "0.1.20", "notes_url": "…"},
 "services": [{"service_name": "web", "service_namespace": "", "environment": "prod", "status": "unsupported",
   "agents": [{"kind": "node", "distro_name": "openlog", "sdk_name": "opentelemetry", "sdk_language": "nodejs",
     "status": "unsupported", "instances": 4, "last_seen": "…", "versions_truncated": false, "instrumentation_modules": [],
     "versions": [{"version": "0.1.31", "status": "ok", "instances": 1, "spans": 120, "last_seen": "…"},
                  {"version": "0.1.9", "status": "unsupported", "instances": 3, "spans": 900, "last_seen": "…"}],
     "upgrade": {"package": "openlog-node", "version": "0.1.31", "command": "npm install openlog-node@0.1.31", "lang": "sh",
       "registry": "available", "registry_url": "…", "release_asset_url": "…", "docs_url": "…", "docs_section": "", "notes": []}}]}]}
```
- **Algılama** (span resource'unun saatlik ClickHouse agregatı `apm_agent_versions_1h`, 30 gün, backfill yok):
  `telemetry.distro.name` `openlog` + `telemetry.sdk.language` → `kind` `node`, `python`, `java`, `dotnet`, `go`
  (diğer diller için `unknown`); `openlog-php` → `php`; başka her şey → `third_party` (sürümler
  `telemetry.sdk.version`'dır, asla karşılaştırılmaz). `instances`: farklı `service.instance.id`, yoksa `host.id` (yaklaşık;
  sürüm değiştiren instance ikisinde de sayılır). Sürümler en yenisi başta, en fazla 20 (`versions_truncated`).
  `instrumentation_modules` (Go): span scope'larından tanınan `gin`, `echo`, `grpc`.
- **Karşılaştırma**: `release.latest`, sürüm kataloğundan organizasyonun fleet kanalının (fleet yönetimi yoksa `stable`) en yeni doğrulanmış
  sürümüdür; `release.oldest_supported`, bu backend'in sürüm manifest'indeki, yoksa son sürümdeki `compatibility.oldest_supported_agent` değeridir. `status`: alt sınırın altında `unsupported`, en yeninin altında `outdated`,
  aksi halde `ok`; geliştirme build'leri (`0.0.0…`), SemVer olmayan sürümler ve `release.catalog`
  `disabled` (static auth modu) veya `unavailable` (çevrimdışı, henüz çekilmedi; `latest` null) olduğunda `unknown`. Python PEP 440
  ön sürümleri (`0.2.0rc1`) SemVer olarak karşılaştırılır. Ajan ve servis `status` değeri = en kötü sürüm durumu.
- **Upgrade** (`third_party`, `unknown` ve `latest` yoksa null): `node` `npm install openlog-node@X`, `python`
  `pip install -U "openlog-agent==X"` (PEP 440), `dotnet` `dotnet add package OpenLog.Agent --version X`; her biri
  yalnızca `registry` `available` ise (onboarding kontrolü) registry'den, aksi halde release asset'inden (`notes`
  `registry_fallback`); `go` `go get github.com/onuragtas/openlog/agents/go@vX` + algılanan instrumentation modülleri ve
  `go mod tidy` (`go_modules`); `java` sürümün jar indirme URL'si (`java_fleet_auto`, `docs_section`
  java-agent.md'deki "Distribution and updates"); `php` sürümün `.deb` (amd64) dosyası (`php_fleet_auto`: `php_agent.mode: auto` ile fleet
  kurulumu).

### `GET /api/v1/apm/services/{service_name}` {#get-api-v1-apm-services-service-name}
Servisin saklama süresi içinde verisi yoksa `404`.
```json
{"service_name": "orders", "apdex_t_ms": 500, "apdex_t_default": true,
 "instances": [{"service_namespace": "shop", "environment": "prod", "first_seen": "…", "last_seen": "…", "version": "1.4.2",
                "language": "go", "sdk_name": "opentelemetry", "resource_attributes": {"host.id": "…"}}],
 "hosts": [{"host_id": "…", "host_name": "web-1", "first_seen": "…", "last_seen": "…", "known": true}]}
```
`known`: host'un host kaydı var (`GET /hosts/{host_id}` çalışır).

### `GET /api/v1/apm/services/{service_name}/overview?transaction=&type=` {#get-api-v1-apm-services-service-name-overview-transaction-type}
`{"step": "60s", "apdex_t_ms": 500, "totals": ApmRed, "series": [{"t": 1757757600000, …ApmRed}]}`; `transaction`/`type`
tek bir transaction ile sınırlar. İsteği olmayan bucket'lar atlanır.

### `GET /api/v1/apm/services/{service_name}/transactions?sort=&dir=&type=&limit=&offset=` {#get-api-v1-apm-services-service-name-transactions-sort-dir-type-limit-offset}
`sort` = `time` (varsayılan: en çok zaman tüketen) | `throughput` | `avg` | `slowest` (p95) | `errors` | `error_rate` | `apdex` | `name`; `limit` varsayılan 100, en fazla 1000, bkz. [Listeler](#lists). p95 ve Apdex histogramlardan çıkar, bu yüzden sunucu en çok zaman tüketen `OPENLOG_API_MAX_ROWS` transaction'ı okur, onları sıralar ve sayfayı keser: bir aralıkta bundan fazlası varsa sıra yalnızca onlara kadar eksiksizdir. `time_share`, okunan tüm transaction'lar üzerindendir, yani bir satır her sayfada aynı şeyi söyler.
`{"apdex_t_ms": 500, "has_more": false, "transactions": [{"transaction_type": "web", "transaction_name": "GET /orders/{id}", "time_consumed_ms", "time_share", "max_ms", …ApmRed}]}`

### `GET /api/v1/apm/services/{service_name}/transaction?name=&type=` {#get-api-v1-apm-services-service-name-transaction-name-type}
`name` zorunludur.
```json
{"transaction_name": "GET /orders/{id}", "transaction_type": "", "step": "60s", "apdex_t_ms": 500, "totals": ApmRed,
 "max_ms": 1840.2, "series": [ApmPoint], "histogram": [{"from_ms": 17.4, "to_ms": 19.0, "count": 42}],
 "slowest": [{"trace_id", "span_id", "timestamp", "duration_ms", "is_error", "http_status_code", "service_name", "transaction_name"}]}
```
`histogram` boş olmayan latency bucket'larını listeler (apm.md §4.1); `slowest`, aralıktaki en yavaş 10 entry span'i.

### `GET /api/v1/apm/services/{service_name}/errors?limit=&offset=&status=&assignee=&q=&sort=&dir=` · `GET /api/v1/apm/errors?service=&namespace=&environment=&…` {#get-api-v1-apm-services-service-name-errors-limit-offset-status-assignee-q-sort-dir-·-get-api-v1-apm-errors-service-namespace-environment}
Hata gelen kutusu ([apm.md](/tr/contracts/apm) §3.4): aralıkta oluşumu olan gruplar (ve resolved/ignored/assignee
filtrelerinde, workflow durumu eşleşen gruplar), en sık olan başta (`limit` varsayılan 50, en fazla 500). İkinci biçim
tüm servisleri kapsar. `status` = `all` (varsayılan) veya virgülle ayrılmış `unresolved`, `resolved`, `ignored` listesi;
`assignee` = `any` (varsayılan), `none`, `me` veya bir kullanıcı id'si; `q` tür/mesaj/servis/span adında alt dize (≤ 256
byte); `sort` = `count` (varsayılan), `last_seen`, `first_seen`, `type`, `service`, `status`, `assignee`, `comments`, bkz. [Listeler](#lists). Yanıt şunları ekler: duruma göre `counts` (`assignee`
ve `q` sonrası), `truncated` (adaylar sınırlandı veya başka sayfa var), `capped`, `has_more` ve `workflow` (PostgreSQL yoksa `false`: her grup unresolved, değişiklik yok). Resolved
gruplar her okumada regresyon için kontrol edilir (ve yeniden açılır). Workflow alanları PostgreSQL'de durur, bu yüzden sunucu aralıkta en sık görülen 2000 grubu ve workflow deposundan en fazla 500 grubu toplar, onları sıralar ve sayfayı keser: `capped` bunlardan ilkine ulaşıldığını söyler, onu aşan grup hiçbir sıralamada veya sayfada yer almaz.
```json
{"step": "60s", "groups": [{"group_id": "9f3c2a71d4b84e0f", "error_type": "*errors.errorString",
  "message": "order <n>: inventory shard <n> unavailable", "count": 12, "total_count": 480, "first_seen": "…",
  "last_seen": "…", "last_trace_id": "…", "last_span_name": "GET /orders/{id}", "sparkline": [[1757757600000, 2]]}]}
```
`count` aralıktakidir, `total_count`, `first_seen`, `last_seen` saklama süresi boyuncadır. Her grupta ayrıca
`service_name`, `service_namespace`, `environment` ve workflow alanları `status`, `assignee`
(`{"user_id", "email", "name"}` veya `null`), `resolved_at`, `resolved_in_version`, `resolved_by_email`,
`regressed_at`, `regression_count`, `comment_count`, `updated_at`, `updated_by_email` bulunur.

### `GET /api/v1/apm/services/{service_name}/errors/{group_id}` {#get-api-v1-apm-services-service-name-errors-group-id}
`group_id` 16 hex basamak değilse `400`, bilinmeyen grup için `404`. Yukarıdaki grup alanları artı `last_message` (ham),
`stacktrace` (en yeni örnek), `last_span_id`, `series` (`[[ms, count]]`) ve `samples` (aralıktaki en yeni 20 hata span'i:
`{"trace_id", "span_id", "timestamp", "span_name", "transaction_name", "duration_ms", "message", "version", "host_id"}`),
yukarıdaki workflow alanları, `affected` (`{"versions", "hosts", "containers", "transactions"}`, her biri saklama süresi boyunca ilk 20
`{"value", "name", "count", "first_seen", "last_seen"}`; `name` = host veya container adı),
`comments` ve `activity` (grubun audit olayları `{"action", "actor_email", "details", "created_at"}`, en yenisi başta,
en fazla 50) ve `workflow`.

### `PATCH /api/v1/apm/errors/groups` `{"group_ids": [...], "status"?, "assignee_user_id"?, "resolved_in_version"?}` {#patch-api-v1-apm-errors-groups-group-ids-status-assignee-user-id-resolved-in-version}
Member, admin veya owner, böyle bir role sahip API anahtarları da (viewer'lar için `403`; oturumlarda her zamanki gibi CSRF; static auth modunda `404`). 1–500
grup id'si (16 hex basamak; bilinmeyen id'ler → `404`); en az bir değişiklik. `status` `unresolved`/`resolved`/`ignored`;
`assignee_user_id` bir üyenin id'si (aksi halde `400`) veya atamayı kaldırmak için `""`; `resolved_in_version` (≤ 256 byte) yalnızca
`status: "resolved"` ile. `{"groups": [{"group_id", "service_name", "service_namespace", "environment", …workflow fields}]}` döner.
Değişen grup başına `apm.error_group.update` audit olayı.

### `GET /api/v1/apm/errors/groups/{group_id}/comments` · `POST …/comments` `{"body"}` · `DELETE …/comments/{comment_id}` {#get-api-v1-apm-errors-groups-group-id-comments-·-post-comments-body-·-delete-comments-comment-id}
`{"comments": [{"id", "author_user_id", "author_email", "author_name", "body", "created_at"}]}`, en eskisi başta. `POST`
(member+, gövde 1–4000 byte, yorumla `201`; audit `apm.error_group.comment`); `DELETE` yazar veya bir
admin/owner tarafından (`204`, aksi halde `404`; audit `apm.error_group.comment_delete`).

### `GET /api/v1/apm/services/{service_name}/deployments?from=&to=&gap=` {#get-api-v1-apm-services-service-name-deployments-from-to-gap}
Aralıktaki `service.version` değişiklikleri ([apm.md](/tr/contracts/apm) §12), en eskisi başta; `gap` 5m–24h (varsayılan 30m):
`{"gap_seconds": 1800, "deployments": [{"timestamp", "t": 1757757600000, "service_namespace", "environment", "version": "1.4.3", "previous_version": "1.4.2", "initial": false, "rollback": false}]}`

### `GET /api/v1/apm/services/{service_name}/deployments/compare?at=&window=` {#get-api-v1-apm-services-service-name-deployments-compare-at-window}
`at` zorunlu (RFC3339 veya ms, dakikaya kesilir, geçmişte değilse `400`); `window` 5m–24h (varsayılan 30m).
`{"at", "window_seconds", "apdex_t_ms", "before": {"from", "to", …ApmRed}, "after": {"from", "to", …ApmRed}, "new_error_groups": [{"group_id", "error_type", "message", "first_seen", "total_count"}]}`
(`after` en geç şimdi biter; yeni gruplar = ilk kez `after` içinde görülen, toplam sayıya göre ilk 20).

### `GET /api/v1/apm/services/{service_name}/databases?sort=&dir=&db_system=&limit=&offset=` {#get-api-v1-apm-services-service-name-databases-sort-dir-db-system-limit-offset}
`sort` = `time` (varsayılan) | `calls` | `slowest` (ort.) | `errors` | `error_rate` | `max` | `statement` | `database`, bkz. [Listeler](#lists). `time_share` sayfa üzerindendir.
`{"has_more": false, "queries": [{"db_system": "postgresql", "db_name": "orders", "db_operation": "SELECT", "statement": "SELECT … WHERE id = ?", "calls", "throughput", "errors", "error_rate", "avg_ms", "p95_ms", "max_ms", "time_consumed_ms", "time_share"}]}`

### `GET /api/v1/apm/db-queries?sort=&q=&service=&db_system=&environment=&limit=` {#get-api-v1-apm-db-queries-sort-q-service-db-system-environment-limit}
Aynı satırlar tüm servisler genelinde, her birinde `"services": ["orders", "billing"]` ile: veritabanı sunucusuna bağlantı olmadığında **Databases** ekranının
gösterdiği şey. `q`, ifadenin bir kısmıyla eşleşir.

### `GET /api/v1/apm/db-queries/plans?statement=&db_system=&service=` {#get-api-v1-apm-db-queries-plans-statement-db-system-service}
`{"plans": [{"plan", "service", "generic", "captured_at", "trace_id"}]}`: bir ajanın bu normalleştirilmiş ifade için yakaladığı planlar, en yenisi başta, en fazla 5
([apm.md](/tr/contracts/apm) §7.1). Bir ajanda explain açık değilse boştur.

### `GET /api/v1/apm/services/{service_name}/hosts` {#get-api-v1-apm-services-service-name-hosts}
`{"hosts": [...]}` (şekil servis nesnesindeki gibi).

### `GET /api/v1/apm/services/{service_name}/containers?from=` {#get-api-v1-apm-services-service-name-containers-from}
Span resource'ları `from`'dan beri servis için `container.id` taşıyan container'lar (apm.md §1), en yenisi başta. `known`:
container'ın altyapı ajanı verisi var (Container'lar); `state`, `reporting` ve son `cpu_utilization`, `memory_usage`,
`memory_limit` yalnızca o zaman ayarlanır.
`{"containers": [{"container_id", "name", "host_id", "host_name", "first_seen", "last_seen", "known", "state", "reporting", "cpu_utilization", "memory_usage", "memory_limit"}]}`

### `GET /api/v1/apm/services/{service_name}/settings` · `PUT …/settings` `{"apdex_t_ms": 300}` {#get-api-v1-apm-services-service-name-settings-·-put-settings-apdex-t-ms-300}
`{"service_name", "service_namespace", "environment", "apdex_t_ms", "is_default", "updated_at", "updated_by_email"}`.
Anahtar (`service_name`, `namespace`, `environment` sorgu parametreleri, eksik = `''`) değeridir; namespace ve
ortamı boş bir satır, daha özel bir satır yoksa hepsine uygulanır. `PUT` admin veya owner ister, o role sahip API anahtarı
dahil (aksi halde `403`; oturumlarda her zamanki gibi CSRF; `PATCH /orgs/current` ile aynı izindir),
`apdex_t_ms` 1..600000 arası tam sayı olmalıdır (`400`), `apm.service_settings.update` audit olayını yazar;
`OPENLOG_AUTH_MODE=static` ile kullanılamaz (`404`, `GET` varsayılanı döner).

### `GET /api/v1/apm/sampling` · `PUT /api/v1/apm/sampling` `{"policy": {…}, "version": 3}` {#get-api-v1-apm-sampling-·-put-api-v1-apm-sampling-policy-version-3}
Organizasyonun tail sampling politikası ([apm.md](/tr/contracts/apm) §4.2, D-075):
`{"enabled": true, "policy": {"enabled", "baseline_ratio", "max_spans_per_second", "rules": [{"name", "type", "ratio", …}]}, "is_default", "version", "updated_at", "updated_by_email"}`.
`enabled`, API'nin `OPENLOG_TAILSAMPLING_ENABLED` değeridir. Hiçbir şey saklı değilse `is_default` (sürüm 0).
`PUT` admin veya owner (o role sahip API anahtarı dahil; `403`; `PATCH /orgs/current` ile aynı izin), geçerli bir politika (`400`: bilinmeyen alanlar, 0..1 dışında oranlar, yinelenen/ayrılmış kural adları, eksik kural alanları) ve düzenlenen sürümü ister (saklanan sürüm farklıysa `409 conflict`). `apm.tail_sampling.update` audit olayını yazar ve saklanan durumu döner. `OPENLOG_AUTH_MODE=static` ile kullanılamaz (`404`; `GET` hepsini tutan varsayılanı döner).

### `POST /api/v1/apm/sampling/preview` `{"policy": {…}, "window_minutes": 60}` {#post-api-v1-apm-sampling-preview-policy-window-minutes-60}
Bir politikanın son `window_minutes` (1..1440, varsayılan 60) içinde saklanan trace'lerin ne kadarını tutacağını tahmin eder. Trace id'lerinin hash örneği olarak en fazla 20 000 trace incelenir; her biri düzeltilmiş sayısı kadar ağırlık taşır:
`{"window_minutes", "traces_examined", "sampled_fraction", "estimated_traces", "kept_trace_ratio", "kept_span_ratio", "rules": [{"name", "matched_trace_ratio", "kept_trace_ratio"}]}`
(son kayıt `baseline`'dır). Rate limit simüle edilmez. Telemetriye okuma erişimi gerekir.

### `GET /api/v1/apm/hosts/{host_id}/services?from=` {#get-api-v1-apm-hosts-host-id-services-from}
Resource'ları `from`'dan (varsayılan 24 saat önce) beri `host.id` taşıyan servisler:
`{"services": [{"service_name", "service_namespace", "environment", "first_seen", "last_seen"}]}`. Bilinmeyen host'lar → boş liste.

### `GET /api/v1/apm/map/path?service=&transaction=&namespace=&environment=&from=&to=` {#get-api-v1-apm-map-path-service-transaction-namespace-environment-from-to}
`service` ve `transaction` zorunlu. `{"trace_count": 50, "nodes": ["service:frontend|shop|prod", …], "edges": ["service:frontend|shop|prod->db:postgresql/orders", …]}`:
transaction'ın en fazla 50 trace'inin kullandığı `GET /apm/map` id'leri ([apm.md](/tr/contracts/apm) §5.1).

### `GET /api/v1/apm/map?service=&namespace=&environment=` {#get-api-v1-apm-map-service-namespace-environment}
```json
{"nodes": [{"id": "service:orders|shop|prod", "type": "service", "name": "orders", "service_namespace": "shop",
            "environment": "prod", "requests", "throughput", "error_rate", "avg_ms", "p95_ms", "apdex", "host_count": 2, "container_count": 3},
           {"id": "db:postgresql/orders", "type": "db", "name": "postgresql/orders", "service_namespace": "", "environment": "", …}],
 "edges": [{"id": "service:frontend|shop|prod->service:orders|shop|prod", "source": "…", "target": "…", "target_type": "service",
            "calls", "throughput", "errors", "error_rate", "avg_ms", "p95_ms"}]}
```
Node `type` ∈ `service`, `db`, `external`, `messaging`; bağımlılık node'unun RED değeri gelen kenarlarının toplamıdır.
`host_count`/`container_count`: `from`'dan beri bir servis node'una bağlı host ve container'lar (bağımlılıklar için 0).
`service` olmadan `namespace`/`environment` tüm haritayı filtreler (kaynaklar ve trace ile bağlı hedefler). `service`,
haritayı o servise dokunan kenarlarla sınırlar.

### `GET /api/v1/apm/traces?service=&namespace=&environment=&transaction=&type=&min_duration_ms=&max_duration_ms=&error=&attr.<key>=&sort=&dir=&limit=&offset=` {#get-api-v1-apm-traces-service-namespace-environment-transaction-type-min-duration-ms-max-duration-ms-error-attr-key-sort-dir-limit-offset}
Aralıktaki entry span'ler (`sort` = `timestamp` (varsayılan, en yenisi başta) | `duration`; `limit` varsayılan 50, en fazla 500). `POST /traces/query` gibi `cursor` ile değil `offset` ile sayfalanır, yani iki sayfa arasında gelen bir span geri kalanı bir kaydırır. Transaction adı veya durum burada sıralanamaz.
`service` olmadan trace başına bir satır. `error` = `true`|`false`. `attr.<key>=<value>`: span attribute'unda tam eşleşme (en fazla 10;
anahtar ≤ 256 byte, tek değer ≤ 1024 byte; aksi halde `400`). Sonuçları `GET /traces/{trace_id}` ile, ilişkili
logları `GET /logs?trace_id=` ile aç.
`{"has_more": false, "traces": [{"trace_id", "span_id", "timestamp", "duration_ms", "is_error", "http_status_code", "service_name", "service_namespace", "environment", "transaction_type", "transaction_name"}]}`

## Fleet (ajan güncellemeleri) {#fleet-agent-updates}

Çağıranın organizasyonunun ajan güncelleme politikası, rollout'ları ve ajanları ([releases-updates.md](/tr/contracts/releases-updates)
§3–§4, tablolar: [postgres.md](/tr/contracts/postgres#fleet-agent-updates-0002_fleet)). Okumalar her rolü kabul eder (API anahtarları da);
değişiklikler giriş yapmış admin veya owner ister (aksi halde `403`, her zamanki gibi CSRF).
`OPENLOG_AUTH_MODE=static` ile kullanılamaz (`404`). Değişiklikler ingest'e `OPENLOG_FLEET_POLICY_CACHE_TTL` içinde, rollout
controller'ına `OPENLOG_FLEET_CONTROLLER_INTERVAL` içinde ulaşır. Her değişiklik audit log'a yazılır (`fleet.*`).

### `GET /api/v1/fleet/policy` · `PUT /api/v1/fleet/policy` {#get-api-v1-fleet-policy-·-put-api-v1-fleet-policy}
```json
{"mode": "auto", "channel": "stable", "target": "latest", "pinned_version": null, "waves": [10, 50, 100],
 "wave_soak_minutes": 60, "halt_failure_rate": 0.05,
 "maintenance_windows": [{"days": ["sat", "sun"], "start": "02:00", "end": "05:00"}],
 "php_agent": {"mode": "manual", "version": "agent", "reload": "none", "exclude_bins": [], "changed_at": null},
 "java_agent": {"mode": "manual", "version": "agent", "changed_at": null},
 "is_default": false, "updated_at": "…", "updated_by_email": "…"}
```
`php_agent`, PHP ajanının altyapı ajanı üzerinden kurulumunu kontrol eder ([php-agent.md](/tr/contracts/php-agent) §7.3): `mode`
`off` (fleet kurulumları kaldırılır) · `manual` (varsayılan: yalnızca runtime envanteri) · `auto` (desteklenen bir PHP runtime'ı bulunan yerde kur ve güncelle); `version` `agent` (her host, altyapı ajanı sürümünün PHP ajanını alır)
veya SemVer sürüm (katalog yüklüyse doğrulanmış sürüm olmalıdır); `reload` `none` · `graceful` (değişiklikten sonra PHP-FPM /
Apache reload); `exclude_bins` glob'lar (en fazla 50). Host'lar, `changed_at` (bölüm değişince sunucu ayarlar) ile hedefin
yayın zamanından geç olanından başlayarak politikanın `waves` ve
`wave_soak_minutes` değerlerini ve bakım pencerelerini izler. `php_agent` içermeyen `PUT` saklanan bölümü korur.

`java_agent`, `openlog-javaagent.jar` dosyasını altyapı ajanı üzerinden güncel tutar ([java-agent.md](/tr/contracts/java-agent) §2):
`mode` `off` (fleet kurulumları, hiçbir JVM kullanmadığında kaldırılır) · `manual` (varsayılan: yalnızca JVM envanteri) · `auto`
(jar'ı `link_path` arkasında kur ve güncelle); `version` `agent` veya SemVer sürüm (katalog yüklüyse doğrulanmış sürüm; rollback alt sınırı içinde daha eski bir sürüme sabitlemek, fleet'in emrettiği rollback'tir). Wave'ler, soak süresi
ve bakım pencereleri `php_agent` gibi uygulanır. `java_agent` içermeyen `PUT` saklanan bölümü korur.

`PUT` politika alanlarını alır ve saklanan politikayı döner. `400 invalid_argument`: bilinmeyen mode/channel/target,
1..100 içinde kesin artan olmayan veya 100'de bitmeyen `waves` (1–20 wave), 0..43200 dışında `wave_soak_minutes`,
0..1 dışında `halt_failure_rate`, hatalı pencereler (`days` ∈ `mon`…`sun`, boş = her gün; `HH:MM`, `end`
`24:00` olabilir, `end <= start` gece yarısını aşar; en fazla 50), `pinned_version` olmadan `target=pinned` veya doğrulanmış
sürüm olmayan sabitlenmiş sürüm (sürüm kataloğu yüklüyse kontrol edilir). `pinned_version` diğer hedeflerde
yok sayılır (null); baştaki `v` kaldırılır.

### `GET /api/v1/fleet/summary` {#get-api-v1-fleet-summary}
`OPENLOG_FLEET_HOST_STALE_AFTER` içinde eşitlenen ajanlar üzerinden sayılar (`total_hosts` hepsini sayar):
```json
{"total_hosts": 52, "active_hosts": 50, "update_capable": 46,
 "not_update_capable": [{"reason": "container", "hosts": 4}], "outdated": 30, "unsupported": 2,
 "in_progress": 3, "failed": 1, "held": 1, "pinned": 0,
 "versions": [{"version": "0.4.0", "hosts": 20, "latest": true, "outdated": false, "supported": true}],
 "latest": {"stable": {"version": "0.4.0", "channel": "stable", "released_at": "…", "notes_url": "…"}, "beta": null},
 "target": {"version": "0.4.0", …} | null, "oldest_supported_version": "0.2.0", "policy_mode": "auto",
 "update_available": true, "stale_after_seconds": 86400,
 "catalog": {"status": "ok|pending|error|disabled", "source": "…", "checked_at": "…", "last_success_at": "…",
             "error": "", "releases": 7, "warnings": []},
 "current_rollout": Rollout | null}
```
- `not_update_capable.reason`, ajanın `install_method` değeridir (`container`, `dev`, …; boşsa `unknown`).
- `outdated`: çalışan sürüm politika hedefinin altında (`target=patch`: minor'ının en yeni patch'inin altında).
  `unsupported`: bu backend'in sürümündeki (veya bu sürüm katalogda yoksa en yeni stable sürümdeki)
  `compatibility.oldest_supported_agent` altında; ayrıştırılamayan sürümler desteklenmez sayılır.
- `target`, `target=patch` için veya hedef katalogda yoksa null'dır. `update_available` = `outdated > 0`
  (arayüz `notify` modunda gösterir).

### `GET /api/v1/fleet/hosts?version=&state=&q=&limit=&cursor=` {#get-api-v1-fleet-hosts-version-state-q-limit-cursor}
Host adına, sonra id'ye göre sıralı; `limit` varsayılan 100, en fazla 1000; `next_cursor` (opak) veya null. `version`
bildirilen sürümle tam eşleşir, `state` bildirilen güncelleme durumuyla, `q` host adı veya id'de alt dizeyle.
```json
{"hosts": [{"host_id": "…", "host_name": "web-1",
  "agent": {"name": "openlog-infra-agent", "version": "0.3.0", "commit": "…", "os": "linux", "arch": "amd64",
            "install_method": "tarball", "update_capable": true},
  "update": {"state": "failed", "from_version": "0.3.0", "to_version": "0.4.0", "error": "self-test failed", "changed_at": "…"},
  "first_seen_at": "…", "last_sync_at": "…", "rollout_id": "…" | null,
  "override": {"action": "pin", "version": "0.3.1", "updated_at": "…"} | null,
  "outdated": true, "supported": true, "status": "already_failed", "status_target": "0.4.0" | null,
  "php_agent": {"reported": true, "mode": "auto", "agent_mode": "auto", "source": "remote", "capable": true, "reason": "",
    "managed_by": "fleet", "version": "0.4.0" | null,
    "runtimes": [{"bin": "/usr/sbin/php-fpm8.2", "version": "8.2.29", "api": "20220829", "zts": false, "debug": false,
      "libc": "glibc", "scan_dir": "/etc/php/8.2/fpm/conf.d", "module": "20220829-nts-glibc", "supported": true,
      "enabled": true, "loaded": true, "excluded": false}],
    "update": {"operation": "upgrade", "version": "0.4.0", "state": "applied", "error": "", "changed_at": "…"} | null,
    "override": {"mode": "auto", "updated_at": "…"} | null, "status": "up_to_date", "status_target": "0.4.0" | null},
  "java_agent": {"reported": true, "mode": "auto", "agent_mode": "auto", "source": "remote", "capable": true, "reason": "",
    "managed": true, "version": "0.4.0" | null, "state": "restart_pending", "detail": "1 JVM(s) still run an older Java agent: …",
    "link_path": "/opt/openlog/openlog-javaagent.jar", "link_state": "managed",
    "jvms": [{"pid": 4242, "name": "java", "command": "java -jar shop.jar", "agent_path": "/opt/openlog/openlog-javaagent.jar",
      "loaded_version": "0.3.0", "managed": true, "restart_pending": true, "started_at": "…", "container": false}],
    "update": {"operation": "upgrade", "version": "0.4.0", "state": "applied", "error": "", "changed_at": "…"} | null,
    "override": {"mode": "auto", "updated_at": "…"} | null, "status": "up_to_date", "status_target": "0.4.0" | null}}],
 "next_cursor": null}
```
`php_agent.status`, PHP ajanı kararıdır (sync yanıtıyla aynı girdiler): `offer` (sonraki sync'inde `status_target` kurar),
`up_to_date`, `mode_off`, `manual`, `not_reported` (ajan PHP runtime'larını bildiremeyecek kadar eski),
`not_capable` (yetkili pre-start adımı yok, container, sürüm anahtarı yok), `managed_elsewhere` (deb/rpm/apk paketi veya
elle kurulmuş: asla değiştirilmez), `no_php`, `invalid_version`, `no_catalog`, `target_unavailable`,
`no_artifact`, `already_failed` (host'ta geri alındı), `not_in_wave`, `outside_window`. `update.state`:
`downloading`, `restarting`, `confirming` (sağlık kontrolü bekliyor), `applied`, `failed`, `rolled_back`, `uninstalled`.
`status`, host için şu anki güncelleme kararıdır: `offer` (sonraki sync'inde `status_target` alacak),
`not_in_wave`, `outside_window`, `hold`, `not_capable`, `up_to_date`, `already_failed`, `incompatible` (sürümünden yükseltme
yolu yok / rollback alt sınırının altında), `no_artifact` (platform), `rollout_paused`, `rollout_halted`,
`rollout_outdated`, `not_in_rollout`, `no_rollout`, `notify_only`, `mode_off`, `no_catalog`, `target_unavailable`,
`invalid_version`.

### `PUT /api/v1/fleet/hosts/{host_id}/override` `{"action": "hold"|"pin", "version"}` · `DELETE …/override` {#put-api-v1-fleet-hosts-host-id-override-action-hold-pin-version-·-delete-override}
`PUT` `{"action", "version", "updated_at"}` döner; hiç sync yapmamış host için `404`; başka action veya
doğrulanmış sürüm olmayan pin sürümü için `400`. `DELETE` idempotent'tir (`204`). Sabitlenmiş host, wave'lerden bağımsız olarak, bakım pencereleri içinde sürümüne geçer
(yükseltme veya rollback alt sınırına kadar rollback) ve fleet rollout'larının parçası olmaz.

### `PUT /api/v1/fleet/hosts/{host_id}/php-agent` `{"mode": "off"|"manual"|"auto"}` · `DELETE …/php-agent` {#put-api-v1-fleet-hosts-host-id-php-agent-mode-off-manual-auto-·-delete-php-agent}
Politikanın `php_agent.mode` değerinden bağımsız olarak tek bir host'ta PHP ajanını etkinleştirir (`auto`) veya devre dışı bırakır (`off`, fleet kurulumunu kaldırır); `auto` override wave'leri atlar (bakım pencereleri yine geçerlidir). `PUT`
`{"mode", "updated_at"}` döner; hiç sync yapmamış host için `404`; başka mode için `400`. `DELETE` idempotent'tir (`204`)
ve host'u politikaya döndürür. Audit: `fleet.php_agent_override.set` (`details.mode`) /
`fleet.php_agent_override.delete`.

`java_agent.state`, host'un bildirdiği şeydir: `installed`, `staged` (indirme veya devir sürüyor),
`restart_pending` (JVM'ler hâlâ eski sürümü çalıştırıyor; yenisini yüklemek için o uygulamaları yeniden başlat; Windows:
link kopyası kilitli), `unmanaged` (`link_path`, altyapı ajanının kurmadığı bir jar; `detail` nasıl devralınacağını açıklar), `error`, `not_found`; bildirilmemişse boş. `java_agent.status` karardır (sync yanıtıyla aynı girdiler): `offer`, `up_to_date`, `mode_off`, `manual`, `not_reported`, `not_capable`, `invalid_version`,
`no_catalog`, `target_unavailable`, `no_artifact`, `already_failed`, `not_in_wave`, `outside_window`.

### `PUT /api/v1/fleet/hosts/{host_id}/java-agent` `{"mode": "off"|"manual"|"auto"}` · `DELETE …/java-agent` {#put-api-v1-fleet-hosts-host-id-java-agent-mode-off-manual-auto-·-delete-java-agent}
Politikanın `java_agent.mode` değeri yerine host'un Java ajanı modu; `auto` override wave'leri atlar (bakım
pencereleri yine geçerlidir). `PUT` `{"mode", "updated_at"}` döner; hiç sync yapmamış host için `404`; başka
mode için `400`. `DELETE` idempotent'tir (`204`). Audit: `fleet.java_agent_override.set` (`details.mode`) /
`fleet.java_agent_override.delete`.

### `GET /api/v1/fleet/rollouts?limit=` {#get-api-v1-fleet-rollouts-limit}
En yenisi başta (varsayılan 20, en fazla 100): `{"rollouts": [Rollout]}`.
```json
{"id": "…", "action": "upgrade", "from_version": "0.3.0", "to_version": "0.4.0", "targets": {},
 "waves": [10, 50, 100], "current_wave": 1, "wave_percent": 50, "wave_started_at": "…", "next_wave_at": "…" | null,
 "wave_soak_minutes": 60, "halt_failure_rate": 0.05, "state": "halted",
 "state_reason": "failure rate 4/30 reached the halt threshold 5%",
 "counters": {"pending": 20, "attempted": 30, "succeeded": 26, "failed": 3, "rolled_back": 1},
 "created_by_email": null, "created_at": "…", "updated_at": "…", "ended_at": null}
```
`target=patch` rollout'larında `to_version` null'dır (minor başına `targets`). `created_by_email` null = rollout
controller'ı oluşturdu. `next_wave_at`, daha fazla wave'i olan aktif rollout için ayarlanır (başarısızlık oranı izin verirse wave o zaman ilerler).

### `POST /api/v1/fleet/rollouts/{id}/pause` · `POST /api/v1/fleet/rollouts/{id}/resume` {#post-api-v1-fleet-rollouts-id-pause-·-post-api-v1-fleet-rollouts-id-resume}
Rollout'u döner. Pause: yalnızca `active` (aksi halde `409 failed_precondition`). Resume: `paused` veya `halted`; mevcut wave'in
soak süresi yeniden başlar ve durdurulmuş rollout için şimdiye kadarki başarısızlıklar onaylanır (artık durdurma eşiğine
sayılmaz). Bilinmeyen id'ler için `404`.

### `POST /api/v1/fleet/rollouts/{id}/deploy-now` {#post-api-v1-fleet-rollouts-id-deploy-now}
"Deploy now": son wave'ine (%100) taşınmış rollout'u döner, `wave_started_at` = şimdi, `next_wave_at` null.
Yalnızca son wave'inde olmayan `active` rollout (aksi halde `409 failed_precondition`). Durdurma eşiği ve
politikanın bakım pencereleri yine geçerlidir; ajanlar güncellemeyi sonraki sync'lerinde alır ve wave bekleyen
ajanlara her `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` (60 sn) sync yapmaları söylenir. Audit: `fleet.rollout.deploy_now`
(`details.from_wave`, `wave`, `percent`).

### `POST /api/v1/fleet/rollback` `{"to_version"}` {#post-api-v1-fleet-rollback-to-version}
Mevcut upgrade rollout'unun hedefinden (veya çalışan en yeni sürümden) `to_version`'a inen, politikanın wave'leriyle bir rollback rollout'u oluşturur (`201`, Rollout); açık rollout'un yerini alır. `400`: doğrulanmış sürüm değil veya o sürümden
düşük değil. `409`: katalog kullanılamıyor, hiçbir ajan daha yeni sürüm çalıştırmıyor veya `to_version`, geri alınan sürümün
`rollback_floor` değerinin altında. Controller, geri alınan sürüme yeni bir upgrade başlatmaz; daha yeni bir sürüm başlatır.

## Entegrasyon ayarları {#integration-settings}

Altyapı ajanı entegrasyonlarının (`nginx`, `redis`, `mysql`, `postgresql`, `docker`, `mssql`, `iis`) endpoint'leri ve kimlik bilgileri, çağıranın organizasyonundaki tüm host'lar
veya tek bir host için, ajanlara sync üzerinden iletilir ([releases-updates.md](/tr/contracts/releases-updates)
§3, tablo: [postgres.md](/tr/contracts/postgres#integration-settings-0008_integration_settings)). İzinler Fleet ile aynı: okumalar
her rolü kabul eder (API anahtarları da); değişiklikler admin veya owner ister, o role sahip API anahtarı dahil (aksi halde `403`, oturumlarda her zamanki gibi CSRF).
`OPENLOG_AUTH_MODE=static` ile kullanılamaz (`404`). Değişiklikler ajanlara `OPENLOG_FLEET_POLICY_CACHE_TTL` artı bir sync aralığı içinde ulaşır.
Her değişiklik audit'lenir (`integration_setting.*`). `host_id`, ajanın host id'sidir (`/hosts` ve
`/fleet/hosts` içindeki `host_id`, `host.id` resource attribute'u).

IntegrationSetting:
```json
{"id": "…", "host_id": "…" | null, "integration": "redis",
 "match": {"port": 6380 | null, "container": "", "endpoint": "", "instance": ""},
 "enabled": true, "endpoint": "127.0.0.1:6380", "username": "default", "password_set": true,
 "database": "", "databases": [], "created_at": "…", "updated_at": "…", "updated_by_email": "admin@example.com"}
```
Parola yalnızca yazılabilir: asla döndürülmez (`password_set` saklı olup olmadığını söyler) ve loglanmaz.

### `GET /api/v1/integrations/settings?host_id=` {#get-api-v1-integrations-settings-host-id}
`host_id` olmadan: organizasyonun her ayarı (önce tüm host'ların ayarları, sonra host id'ye göre). `host_id` ile:
o host'a uygulanan ayarlar, ajanın uyguladığı sırayla (tüm host'ların ayarları, sonra host'unkiler; önce
match'siz olanlar; sonra oluşturulma zamanına göre) ve host'un revizyonları:
```json
{"items": [IntegrationSetting],
 "host": {"host_id": "h1", "revision": "sha256:…", "applied_revision": "sha256:…", "applied_at": "…",
          "remote_config_disabled": false} | null}
```
- `revision`: sync'in şu an bu host'a gönderdiği revizyon. `applied_revision`: ajanın son
  sync'inde bildirdiği revizyon (`""` = henüz yok veya host hiç sync yapmadı); `applied_at`: o sync'in zamanı (`applied_revision`
  `""` ise null). `applied_revision == revision` olduğunda ajan güncel ayarları çalıştırır. Kaydedilen değişiklik
  ajanın sonraki sync'ine kadar bekler.
- `remote_config_disabled`: ajan `"disabled"` bildirdi (uzak yapılandırma kendi config dosyasında kapalı).
- 256 byte'ı aşan `host_id` için `400`.

### `POST /api/v1/integrations/settings` · `PUT /api/v1/integrations/settings/{id}` · `DELETE …/{id}` {#post-api-v1-integrations-settings-·-put-api-v1-integrations-settings-id-·-delete-id}
```json
{"host_id": "h1" | null, "integration": "postgresql",
 "match": {"port": 5432, "container": "", "endpoint": "", "instance": ""},
 "enabled": true, "endpoint": "127.0.0.1:5432", "username": "monitor", "password": "…" | null,
 "database": "postgres", "databases": ["app"]}
```
`POST` → `201` IntegrationSetting; `PUT` → `200` (sır olmayan her alanı değiştirir: gönderilmeyen alanlar boş olur,
`enabled` varsayılan olarak true); `DELETE` → `204`. Yalnızca `integration` zorunludur; `host_id` null veya `""` = tüm host'lar.
`password`: gönderilmez veya null olursa `PUT`'ta saklanan parola korunur (`POST`'ta yok), `""` onu temizler, bir değer onu değiştirir;
parolasız bir entegrasyona geçmek onu temizler. Bilinmeyen id için `404`.

- `400 invalid_argument`: bilinmeyen entegrasyon; entegrasyonun kullanmadığı boş olmayan bir alan (nginx: `endpoint`;
  redis, mysql: `endpoint`, `username`, `password`; postgresql: ayrıca `database`, `databases`; docker: yalnızca `enabled` ve
  `match`); host içeren `http(s)` URL olmayan nginx `endpoint`; `host:port` veya
  `unix:/absolute/path` olmayan diğer endpoint'ler; 1–65535 dışında `match.port`; 512 byte'ı aşan veya kontrol karakteri içeren string;
  64'ten fazla `databases` veya boş kayıt; 256 byte'ı aşan `host_id`.
- `409 already_exists`: başka bir ayar aynı host kapsamına, entegrasyona ve match'e sahip.
- `409 failed_precondition`: parola verildi ama `OPENLOG_SECRETS_KEY` yapılandırılmamış.

## Bulut bağlantıları {#cloud-connections}

Çağıranın organizasyonunun yönetilen bulut servislerinin (AWS, Azure, GCP) metrikleri (PostgreSQL
`cloud_connections`, `0092_cloud_connections`; D-135). Yalnızca PostgreSQL auth modunda (aksi halde `404`). Okumalar: her
rol ve API anahtarları. Değişiklikler: admin'ler ve owner'lar, admin rolüne sahip API anahtarı dahil (aksi halde `403`), çünkü bir
bağlantı bulut kimlik bilgilerini saklar ve sağlayıcıda para harcar. Organizasyon başına en fazla 50 bağlantı
(`409`). Audit: `cloud_connection.{create,update,delete}`.

Bir bağlantı tek bir bulut hesabını, kapsanacak **scope**'ları (AWS bölgeleri, Azure abonelikleri, GCP projeleri)
ve toplanacak **servisleri** adlandırır. API **leader**'ı her scope'u kendi zamanlamasıyla yoklar; alma işlemi
zamanlama satırını aynı ifadede ilerletir, yani bir scope aralık başına asla iki kez yoklanmaz, çünkü burada yinelenen yoklama
sağlayıcıda yinelenen faturadır. Bir scope'un başarısız olması diğerlerini asla durdurmaz: her biri kendi durumu, hatası ve backoff'u olan
kendi zamanlama satırıdır.

**Kimlik bilgileri yalnızca yazılabilir.** Şifreli saklanır (`OPENLOG_SECRETS_KEY` ile AES-256-GCM, AAD
`org_id/id`, entegrasyon ayarlarıyla aynı mekanizma) ve yanıtta, logda veya audit
ayrıntısında asla görünmez; `credentials_set` saklı olup olmadığını, `credentials_key_id` hangi anahtarın şifrelediğini söyler.
`OPENLOG_SECRETS_KEY` ayarlı değilken kimlik bilgisi kaydetmek `409 failed_precondition`'dır ve saklı
kimlik bilgisi olmayan bağlantı asla yoklanmaz.

**Toplanan veri noktaları sıradan metriklerdir.** Her değer, processor'ın OTLP veri noktaları için hesapladığı aynı sütunlar
ve aynı `series_id` ile `metrics` içine yazılır, böylece Metrics Explorer, metrik
alert kuralları ([alerting.md](/tr/contracts/alerting) §2.2) ve dashboard'lar bunları yeni bir yol olmadan kullanır, `metrics_1m` de
395 gün tutar ([apm.md](/tr/contracts/apm) §8). Adlandırma ve attribute'lar:
[semantic-conventions.md](/tr/contracts/semantic-conventions) §9.

**Korkuluklar.** Bu sağlayıcı API'leri istek başına faturalanır ve sert rate limit uygular, bu yüzden tek scope'un tek yoklaması
`max_metrics_per_poll` ve `max_api_calls_per_poll` ile sınırlıdır; throttling üstel
backoff ile yeniden denenir (sağlayıcı verirse onun `Retry-After` değeri); reddedilen kimlik bilgileri yeniden **denenmez**; ve
başarısız olmaya devam eden scope, 30 dakikaya kadar üstel olarak daha seyrek yoklanır
([config.md](/tr/contracts/config#cloud-connections-api-allinone-d-135)). Bir sınıra ulaşan veya bazı servisleri veya kaynakları okuyamayan yoklama asla `ok` değil, `partial` olarak kaydedilir.

### `GET /api/v1/cloud/providers` {#get-api-v1-cloud-providers}
Bağlantı formunun oluşturulduğu katalog: bir scope'un ne dendiği, istenecek kimlik bilgisi alanları
(`secret` alanlar parola girdisidir ve asla önceden doldurulmaz) ve toplanabilecek servisler.
`OPENLOG_SECRETS_KEY` ayarlı değilse `secrets_configured` false olur, bu durumda kimlik bilgileri kaydedilemez.
```json
{"providers": [{"id": "aws", "scope_label": "region",
  "credentials": [{"key": "access_key_id", "required": true, "secret": false},
                  {"key": "secret_access_key", "required": true, "secret": true},
                  {"key": "session_token", "required": false, "secret": true}],
  "services": [{"id": "rds", "metrics": 8}, {"id": "s3", "metrics": 2}, {"id": "lambda", "metrics": 5}]}],
 "secrets_configured": true, "test_supported": true}
```
Servisler: AWS `rds`, `s3`, `lambda`, `sqs`, `dynamodb`, `elb`, `elasticache`; Azure `azure_sql`,
`azure_storage`, `azure_vm`, `azure_functions`, `azure_cosmos`; GCP `cloud_sql`, `gcs`, `cloud_functions`,
`pubsub`, `gce`.

### `GET /api/v1/cloud/connections` · `POST` · `GET|PUT|DELETE /api/v1/cloud/connections/{id}` {#get-api-v1-cloud-connections-·-post-·-get-put-delete-api-v1-cloud-connections-id}
POST/PUT gövdesi: `{"name" (1–200), "provider": "aws"|"azure"|"gcp", "ingest_mode"?: "poll"|"push" (default
`poll`), "enabled"? (default true), "scopes" (1–50; letters, digits, `-`, `_`, `.`), "services" (1–50 ids of
the provider's catalog), "poll_interval_seconds"? (default 300, 60–86400), "max_metrics_per_poll"? (default
5000, 100–200000), "max_api_calls_per_poll"? (default 200, 1–5000), "credentials"? (the provider's fields;
omitted on `PUT` keeps the stored ones, and is required when the provider changes)}`.

`status`, scope başına zamanlama satırıdır: son yoklama (doğrudan PostgreSQL'den) ve sonrakinin ne zaman olduğu.
`ingest_mode` bugün `poll`'dur; `push`, sağlayıcı tarafı teslimat için ayrılmıştır, saklanır ama toplanmaz,
böylece bir bağlantı onu migration olmadan kazanabilir.
```json
{"connections": [{"id": "…", "name": "Production AWS", "provider": "aws", "ingest_mode": "poll",
  "enabled": true, "scopes": ["eu-central-1", "us-east-1"], "services": ["rds", "lambda"],
  "poll_interval_seconds": 300, "max_metrics_per_poll": 5000, "max_api_calls_per_poll": 200,
  "credentials_set": true, "credentials_key_id": "a1b2c3d4",
  "created_by_email": "ada@example.com", "updated_by_email": "ada@example.com",
  "created_at": "…", "updated_at": "…",
  "status": [{"scope": "eu-central-1", "next_run_at": "…", "last_run_at": "…", "last_status": "ok",
              "last_error": "", "last_metrics": 412, "last_api_calls": 6, "last_duration_ms": 1830.4,
              "consecutive_errors": 0}]}],
 "secrets_configured": true, "test_supported": true}
```
- `400 invalid_argument`: bilinmeyen sağlayıcı veya servis, başka sağlayıcının servisi, boş veya
  aşırı büyük scope listesi, yukarıdaki sınırların dışında değer veya sağlayıcının kullanmadığı bir kimlik bilgisi alanı
  (alan yolu mesajdadır, örneğin `credentials.tenant_id`).
- `409 failed_precondition`: bağlantı limiti veya `OPENLOG_SECRETS_KEY` yapılandırılmamış.

### `POST /api/v1/cloud/connections/test` {#post-api-v1-cloud-connections-test}
`{"connection_id"?, "provider"?, "scope"?, "credentials"?}`: sağlayıcıya tek bir ucuz çağrı yapar ve hiçbir şey
saklamaz. `credentials` ile forma yazılanı test eder; tek başına `connection_id` ile
saklı kimlik bilgilerini test eder, böylece düzenleme sırasında sırrı yeniden göndermek gerekmez (`scope` o zaman varsayılan olarak bağlantının
ilk scope'udur). Sağlayıcının reddettiği kimlik bilgisi **normal bir sonuçtur**: `ok: false` ve sağlayıcının
mesajıyla `200`, böylece form bunu satır içinde gösterir. Yalnızca hatalı biçimli istek (`400`), bilinmeyen bağlantı
(`404`) veya eksik secrets anahtarı (`409`) hata durumudur.
```json
{"ok": false, "error": "aws: not authorized to perform cloudwatch:ListMetrics"}
```

### `GET /api/v1/cloud/connections/{id}/runs?scope=&limit=` {#get-api-v1-cloud-connections-id-runs-scope-limit}
Son yoklamalar, en yenisi başta (bağlantı başına en fazla 200 tutulur): her scope'un ne topladığı, kaç
sağlayıcı isteğine mal olduğu, sağlayıcının kaç kez throttle ettiği ve servis başına neyin başarısız olduğu.
```json
{"connection": {…}, "runs": [
  {"id": 4711, "scope": "eu-central-1", "started_at": "…", "duration_ms": 1830.4, "status": "partial",
   "metrics": 412, "api_calls": 6, "throttled": 1,
   "error": "s3: 1 of 3 resources could not be read: AccessDenied: no permission",
   "services": [{"service": "rds", "metrics": 412, "error": ""},
                {"service": "s3", "metrics": 0, "error": "1 of 3 resources could not be read: AccessDenied: no permission"}]}]}
```

## Service level objective'ler {#service-level-objectives}

APM servislerinin hedefleri, error budget'ları ve burn rate'leri (PostgreSQL `slos`, `0087_slo`; anlam ve matematik:
[slo.md](/tr/contracts/slo), D-125). Yalnızca PostgreSQL auth modunda (aksi halde `404`). Okumalar: her rol ve API anahtarları
(telemetri). Yazmalar: member ve üstü, API anahtarları da; organizasyon başına en fazla 200 SLO (`409`). Audit:
`slo.{create,update,delete}`. Budget'lar sorgu anında `apm_transactions_1m`'den hesaplanır, yani
her zamanki telemetri hataları (`422`/`429` sorgu limitleri, `504` timeout) geçerlidir.

### `GET /api/v1/slos?status=` · `POST /api/v1/slos` · `GET|PUT|DELETE /api/v1/slos/{id}` {#get-api-v1-slos-status-·-post-api-v1-slos-·-get-put-delete-api-v1-slos-id}
POST/PUT gövdesi: `{"name" (1–200), "description"? (≤ 2000), "service_name", "service_namespace"?,
"environment"?, "sli_type": "availability"|"latency", "latency_threshold_ms"? (latency only, 1–600000),
"objective" (percent, 50 ≤ x < 100), "window_days": 7|28|30}`. `service_namespace`/`environment`, `null`
(her namespace/ortam, birleştirilmiş) veya tam bir değerdir, `""` da olabilir (apm.md §1).
`status=false` değilse liste, her SLO'nun kayan penceresi üzerinden (son tamamlanmış dakikada biten) budget'ı ekler;
en fazla 50 SLO için hesaplanır (`status_truncated`).
```json
{"slos": [{"id": "…", "name": "Checkout availability", "description": "", "service_name": "checkout",
  "service_namespace": null, "environment": "prod", "sli_type": "availability", "latency_threshold_ms": null,
  "objective": 99.9, "window_days": 28, "created_by_email": "ada@example.com", "updated_by_email": "ada@example.com",
  "created_at": "…", "updated_at": "…",
  "status": {"from": "…", "to": "…", "window_days": 28,
    "budget": {"requests": 1200000, "good": 1198800, "bad": 1200, "sli": 0.999, "budget_requests": 1200,
               "budget_consumed": 1200, "budget_remaining": 0, "remaining_ratio": 0, "burn_rate": 1, "met": true}}}],
 "status_truncated": false}
```

### `GET /api/v1/slos/{id}/results?step=` {#get-api-v1-slos-id-results-step}
Kayan pencere üzerinden budget, `slo_burn` varsayılanlarının burn rate pencereleri (1 sa/5 dk üzerinden 14.4×,
6 sa/30 dk üzerinden 6×) ve burndown serisi (`step` ≥ `60s`, varsayılan ≈ 60 nokta, tam dakikalara yukarı yuvarlanır).
Bir noktanın `remaining_ratio` değeri, o bucket'tan sonra **pencerenin** budget'ından kalan paydır, yani son nokta
`status.budget.remaining_ratio`'ya eşittir; bir noktanın `sli` ve `burn_rate` değerleri bucket'ın kendisinindir (istek yoksa
null).
```json
{"slo": {…}, "status": {…}, "step": "600s",
 "burn": [{"name": "fast", "factor": 14.4, "long_seconds": 3600, "short_seconds": 300,
           "long": {…budget…}, "short": {…budget…}, "rate": 21.6, "ratio": 1.5, "breaching": true},
          {"name": "slow", "factor": 6, "long_seconds": 21600, "short_seconds": 1800, "long": {…}, "short": {…},
           "rate": 3.1, "ratio": 0.52, "breaching": false}],
 "series": [{"t": 1757757600000, "requests": 5400, "good": 5397, "bad": 3, "sli": 0.99944,
             "burn_rate": 0.55, "remaining_ratio": 0.92}]}
```

## Gerçek kullanıcı izleme {#real-user-monitoring}

openlog tarayıcı SDK'sının topladığı tarayıcı telemetrisi ([rum.md](/tr/contracts/rum), D-136): sayfa görüntülemeleri, Core Web
Vitals, oturumlar ve trace bağlantıları. Okumalar telemetri okumasıdır (her rol, API anahtarları da) ve
`0094_rum` rollup'larından geçer. Anahtar yönetimi: [Tarayıcı anahtarları](#browser-keys).

**Tarayıcı hataları burada değil.** JavaScript hatası, tarayıcı uygulamasının servisinin bir hata span'idir, yani
zaten aynı fingerprint, gruplama, atama ve çözüm durumuna sahip bir APM hata grubudur: arayüz
`GET /api/v1/apm/errors?service=<app>` adresine bağlanır. Paralel bir gelen kutusu, tek hatayı çözmek için iki yer demek olurdu.
Aynı nedenle tarayıcı uygulaması **APM altında bir servis olarak görünür**: hatalarıyla ama transaction,
throughput veya Apdex olmadan, çünkü sayfa görüntülemeleri bilerek entry span değildir.

### `GET /api/v1/rum/apps?from=&to=&sort=&dir=&limit=&offset=` {#get-api-v1-rum-apps-from-to-sort-dir-limit-offset}
`{"apps": [{"app", "environment", "views", "sessions", "errors", "last_seen"}], "has_more"}`. `sort`: `views` (varsayılan), `sessions`, `errors`,
`last_seen`, `app`, `environment`. `limit` yoksa `OPENLOG_API_MAX_ROWS` kadar her uygulamadır.

### `GET /api/v1/rum/overview?app=&environment=&from=&to=&step=` {#get-api-v1-rum-overview-app-environment-from-to-step}
Beş vital, sayfa görüntüleme serisi ve toplamlar:
```json
{"from": "…", "to": "…", "step": "60s",
 "vitals": [{"name": "lcp", "unit": "ms", "count": 1240, "p50": 1800, "p75": 2400, "p95": 3900, "avg": 2010,
             "good": 0.78, "needs_improvement": 0.17, "poor": 0.05, "rating": "good",
             "good_threshold": 2500, "poor_threshold": 4000}],
 "points": [{"t": 1757757600000, "views": 42, "avg_ms": 1210}],
 "totals": {"views": 1240, "sessions": 610, "errors": 12, "avg_ms": 1180}}
```
Ölçüm yoksa yüzdelikler `null`'dır. `rating`, **p75**'i (Core Web Vitals değerlendirmesinin tanımlandığı yüzdelik)
yayınlanmış eşiklere göre puanlar ([rum.md](/tr/contracts/rum) §2.1); bunlar ayar değil sabittir. Beş vital de
her zaman döner, yani hiçbirini bildirmeyen tarayıcı kaybolmak yerine "veri yok" olarak görünür.

### `GET /api/v1/rum/pages?app=&environment=&sort=&dir=&limit=&offset=&from=&to=` {#get-api-v1-rum-pages-app-environment-sort-dir-limit-offset-from-to}
`{"pages": [{"route", "views", "avg_ms", "p50_ms", "p75_ms", "p95_ms", "max_ms", "ttfb_avg_ms", "lcp_p75",
"errors"}], "has_more"}`. `errors` span'lerden gelir, yani 7 günlük trace saklamasına kadar uzanır, görüntülemeler ise 30 güne. `sort`, `views` (varsayılan), `slowest` (APM'in "most time
consuming" gibi tüketilen toplam süreye göre), `avg`, `ttfb` veya `route`'tur. Yüzdeliklere ve `lcp_p75`'e göre sıralanamaz: sayfa
okunduktan sonra hesaplanırlar. `limit` ≤ 1000. `route` normalleştirilmiş rotadır ([rum.md](/tr/contracts/rum) §4), asla ham URL değildir: query
string ve fragment storage'a asla ulaşmaz.

### `GET /api/v1/rum/vitals?app=&environment=&route=&from=&to=` {#get-api-v1-rum-vitals-app-environment-route-from-to}
`{"from", "to", "vitals": [ … as above … ]}`, isteğe bağlı olarak tek bir rota için.

### `GET /api/v1/rum/sessions?app=&environment=&sort=&dir=&limit=&offset=&from=&to=` {#get-api-v1-rum-sessions-app-environment-sort-dir-limit-offset-from-to}
`{"sessions": [{"session_id", "app", "environment", "started_at", "ended_at", "duration_ms", "page_views",
"errors", "entry_route", "exit_route", "device_type", "browser_name", "browser_version", "os_name",
"trace_id"}], "has_more"}`, en yenisi başta. `sort`: `last_seen` (varsayılan), `started`, `duration`, `page_views`, `errors`, `entry`, `browser`.
`user_id` ile liste, o kişinin aralıktaki en yeni 2000 oturumudur ve sayfalar orada durur. Oturum bir kişi değil, bir ziyarettir ([rum.md](/tr/contracts/rum) §1.1).

### `GET /api/v1/rum/sessions/{session_id}?from=&to=` {#get-api-v1-rum-sessions-session-id-from-to}
`{"session": {…}, "events": [{"timestamp", "event", "name", "route", "duration_ms", "trace_id", "span_id",
"error_group_id", "status_code"}]}`. `event`, `page_view`, `vital`, `error` veya `resource`'tur;
`error_group_id` bir hatayı APM hata grubuna bağlar. Zaman çizelgesi saklanan span'lerden okunur, yani
**trace** saklamasıyla sınırlıdır: daha eski bir oturumun özeti ve `trace_id` değeri hâlâ vardır ama olayları yoktur.
Oturum bilinmiyorsa `404`.

### `GET /api/v1/rum/releases?app=&environment=&from=&to=&gap=&sort=&dir=&limit=&offset=` {#get-api-v1-rum-releases-app-environment-from-to-gap-sort-dir-limit-offset}
`{"releases": [{"version", "sessions", "error_sessions", "crashed_sessions", "capturing_sessions", "error_free_rate",
"crash_free_rate", "session_crash_free_rate", "first_seen", "last_seen"}], "deployments": […], "has_more"}`. `sort`, `releases` listesini sayfalar: `last_seen` (varsayılan),
`first_seen`, `version` (sayılarına göre, yani 1.10, 1.9'dan sonra gelir), `crash_free` (null oran her iki yönde de sondadır), `error_free`, `sessions`, `error_sessions`.
`limit` varsayılan 200, en fazla 1000. `deployments` her sayfada hepsini içerir
([rum.md](/tr/contracts/rum) §2.7, [mobile-crash.md](/tr/contracts/mobile-crash) §5). Oturumlar 30 güne uzanan saatlik bir rollup'tan gelir, yani
aralık ilk tam saatinden başlar.

`session_crash_free_rate`, crash bildirebilen build'ler arasında crash'siz oturumların payıdır ve böyle build'i olmayan sürüm için
`null`'dır: %100, kimsenin ölçmediği bir şeyi iddia etmek olurdu. `error_free_rate` her hatayı sayar ve oturum yoksa `1`'dir.
`crash_free_rate`, `error_free_rate` ile aynı sayıdır: crash'leri hiç ölçmedi ve eski konsollar için bir sürüm daha kalır.

### `GET /api/v1/rum/crashes?app=&environment=&from=&to=` {#get-api-v1-rum-crashes-app-environment-from-to}
`{"crashes": [{"group_id", "type", "message", "kind", "count", "sessions", "last_version", "last_seen", "stack"}]}`, en
sık olan başta ([mobile-crash.md](/tr/contracts/mobile-crash) §6). Stack span üzerinde durduğu için 7 günlük trace saklamasıyla sınırlıdır.
`group_id`, atandığı ve çözüldüğü APM gelen kutusundaki hata grubudur.

### `GET /api/v1/analytics/activity?app=&environment=&from=&to=` {#get-api-v1-analytics-activity-app-environment-from-to}
`{"days": [{"date", "dau", "wau", "mau", "new_users", "stickiness"}], "collecting"}` ([analytics.md](/tr/contracts/analytics) §6).
Aralık varsayılan 30 gün, en fazla 92'dir. Sayılar sampling ağırlığıyla ekstrapole edilir, yani tam sayı olmayabilirler.
Aktörü olmayan uygulama `collecting: false` döner.

### `GET /api/v1/analytics/retention?app=&environment=&grain=&from=&to=` {#get-api-v1-analytics-retention-app-environment-grain-from-to}
`{"grain", "cohorts": [{"cohort", "size", "periods": [{"period", "users", "rate"}]}], "collecting",
"sampling_changed"}`. `grain`, `day`, `week` (varsayılan) veya `month`'tur; 0. dönem cohort'un kendisidir. Aralık en fazla
395 gündür. `sampling_changed`, düşüşün bir kısmının üründen değil, kişilerin örneklemden çıkmasından geldiği anlamına gelir.

### `POST /api/v1/analytics/erase` `{"app", "visitor_id" | "user_id"}` {#post-api-v1-analytics-erase-app-visitor-id-user-id}
Bir kişinin analytics verisinin silinmesini kuyruğa alır ([analytics.md](/tr/contracts/analytics) §7). Admin rolü, API anahtarlarına izin var.
`202 {"id", "app", "status": "pending", "requested_at", "completed_at": null, "note"}`. Kimlik audit log'a yazılmaz.

### `GET /api/v1/analytics/erase/{id}` {#get-api-v1-analytics-erase-id}
Aynı nesne. Silmeler arka planda uygulayan ClickHouse'a devredilince `status` `submitted` olur.
Ham span'lerdeki tanımlayıcının süresi 7 günlük trace saklamasıyla dolar.

### `GET /api/v1/analytics/overview?app=&environment=&from=&to=` {#get-api-v1-analytics-overview-app-environment-from-to}
`{"days": [{"date", "page_views", "events", "sessions"}], "totals": {"page_views", "events", "sessions"}, "collecting"}`.
Aşağıdaki üçü gibi en fazla 90 gün.

### `GET /api/v1/analytics/events?app=&environment=&name=&param=&from=&to=` {#get-api-v1-analytics-events-app-environment-name-param-from-to}
`{"events": [{"name", "count", "sessions", "users"}]}`, en sık olan başta, en fazla 200. Yukarıdaki listelerin aksine bu üç analytics listesinin (events,
audience, acquisition) sayfası yoktur: `days` ile yalnızca önceki dönemde olan satır sona eklenir, yani bir liste dönem başına en fazla 200 satır tutar ve istemci
elindekini sıralar ve sayfalar. `name` ve `param`
birlikte verilirse her satırdaki `name`, o olayın o parametresinin bir değeridir. `(other)`, anahtarın farklı olay adı sınırını
aşanları tutar ([analytics.md](/tr/contracts/analytics) §4). Anahtar `visitors` modunda değilse `users` 0'dır.

### `GET /api/v1/analytics/compare?app=&environment=&days=` {#get-api-v1-analytics-compare-app-environment-days}
`{"days", "current": {"from", "to"}, "previous": {"from", "to"}, "previous_partial", "collecting", "metrics": {"users": {"current", "previous", "change", "change_rate"}, …}}`. Metrikler: `users`, `new_users`, `avg_daily_users`, `dau`, `wau`, `mau`, `stickiness`, `sessions`, `page_views`, `events`. `days` 1–90, varsayılan 28 ([analytics.md](/tr/contracts/analytics) §6.1).

### `GET /api/v1/analytics/audience?app=&environment=&by=&days=&from=&to=` {#get-api-v1-analytics-audience-app-environment-by-days-from-to}
`{"by", "rows": [{"value", "sessions", "users"}]}`, `days` verilirse `previous_sessions` ve `previous_users` ile, en çok oturum başta, en fazla 200 satır ve 90 gün. `by`, `country` (varsayılan), `device`, `model`, `os`,
`browser` veya `version`'dır. Oturum ilk olayının altında bir kez sayılır. Boş `value`, kimsenin bildirmediği anlamına gelir: `country` bir header veya veritabanı ister
([rum.md](/tr/contracts/rum) §3.7), `model` yalnızca mobil SDK'lardan gelir.

### `GET /api/v1/analytics/acquisition?app=&environment=&by=&from=&to=` {#get-api-v1-analytics-acquisition-app-environment-by-from-to}
`{"by", "sources": [{"value", "sessions", "users"}]}`. `by`, `referrer` (varsayılan), `source`, `medium` veya `campaign`'dir.
Boş `value` doğrudan ziyarettir.

### `GET /api/v1/analytics/funnel?app=&environment=&step=&step=&by=&window=&from=&to=` {#get-api-v1-analytics-funnel-app-environment-step-step-by-window-from-to}
`{"steps": [{"step", "sessions", "rate"}], "by", "window_seconds"}`. Aşağıdaki funnel gibi, ama 90 güne kadar ve bir adım
rota da olabilir. `by`, `session` (varsayılan, `window` 30m, en fazla 24h) veya `user`'dır (`window` 7d, en fazla 90d); ikincisi
bir kişiyi oturumlar arasında izler, o zaman adımdaki `sessions` kişileri sayar.

### `GET /api/v1/rum/funnel?app=&environment=&step=&step=&window=&from=&to=` {#get-api-v1-rum-funnel-app-environment-step-step-window-from-to}
`{"steps": [{"step", "sessions", "rate"}], "window_seconds"}` ([rum.md](/tr/contracts/rum) §2.8). Her biri özel bir olay adı olan 2 ile 6 arası
`step` parametresi; `window` (varsayılan `30m`), bir oturumun ilk adımdan son adıma ulaşmak için ne kadar süresi olduğudur.

**Adımlar sıralıdır.** `sessions`, bir noktada bu adımı yapanların sayısı değil, pencere içinde sırayla bu adıma *ve ondan önceki her adıma* ulaşanların
sayısıdır. `rate` ilk adıma görelidir. Ham span'ler üzerindeki her okuma gibi 7 günlük trace saklamasıyla sınırlıdır.

`session.user_id` ve `session.country` **yalnızca burada** doldurulur ([rum.md](/tr/contracts/rum) §3.7). İkisi de oturum rollup'ında değil
span'lerde durur, bu yüzden yukarıdaki liste, rollup'ı okuduğu için, satır başına ikinci bir sorgu çalıştırmak yerine bunları boş döner.
`user_id`, oturumun bildirdiği son kimliktir, çünkü bir
ziyaret yarı yolda giriş yapabilir; `country`, sunucunun güvenilen proxy header'ından çözdüğü ISO 3166-1 alpha-2 kodudur, hiçbiri çözmediyse boştur.
Oturumun span'lerinin süresi trace saklamasıyla dolunca, özet kalsa bile ikisi de boş olur.

## Metrik korelasyonu {#metric-correlation}

### `GET /api/v1/metrics/correlate?from=&to=&baseline_from=&baseline_to=&host_id=&metric=&limit=` {#get-api-v1-metrics-correlate-from-to-baseline-from-baseline-to-host-id-metric-limit}
Hangi serilerin bir pencerede ondan öncekine göre farklı davrandığı (D-146). Bu, her incident'ın başladığı
ve hiçbir grafiğin yanıtlamadığı sorudur: dashboard, yazarının önemli olacağını beklediği şeyi gösterir,
bu kesintiyi açıklayan metrik ise kimsenin oraya koymadığıdır.

Karşılaştırma bilerek basittir ve kişi sonucu kontrol edebilsin diye iki yarısı da döner:

| | |
|---|---|
| window | her serinin `from`–`to` üzerinden ortalaması (en fazla 6 saat; daha uzunu "incident sırasında" anlamını yitirir) |
| baseline | `baseline_from`–`baseline_to` üzerinden ortalama, varsayılan olarak ondan önceki dört pencere uzunluğu: normalin ne olduğunu bilecek kadar uzun, aynı sistem olacak kadar yakın |
| score | farkın **baseline'ın kendi standart sapmasının** kaç katı olduğu, böylece her zaman 100 oynayan seri yine 100 oynadı diye öne çıkmaz |
| direction | `up` veya `down`, güçten ayrı bildirilir: uçurumdan düşen bir istek oranı, sıçrayan kadar ilginçtir |

Bir serinin puanlanması için her iki tarafta en az üç adet bir dakikalık bucket gerekir; pencere sırasında ortaya çıkan serinin
karşılaştıracak baseline'ı yoktur. Paydanın alt sınırı baseline büyüklüğünün yüzde biridir, bu da
yuvarlama hatası kadar oynayan tamamen düz bir serinin iki katına çıkan bir seriyi geçmesini önler; hiç oynamayan seri ise 0 puan alır ve dışarıda bırakılır: "burada bir şey olmadı" bir
korelasyon değildir.

```json
{"from": "…", "to": "…", "baseline_from": "…", "baseline_to": "…", "series_compared": 4182,
 "correlations": [{"metric_name": "system.disk.io", "series_id": "12278…", "host_id": "…",
   "attributes": {"disk.io.direction": "read", "device": "nvme0n1"}, "unit": "By",
   "baseline_mean": 1048576, "window_mean": 83886080, "change_ratio": 79, "score": 41.2,
   "direction": "up", "points": 30}]}
```

1 dakikalık rollup'ı okur, yani yanıt seri sayısı ne olursa olsun tek sorguya mal olur ve diğer her metrik okuması gibi
çağıranın organizasyonuyla sınırlıdır.

## Güvenlik açıkları {#vulnerabilities}

Çağıranın host'larında bulunan açıklı paketler (ClickHouse `host_vulnerabilities`, 90 gün;
advisory kataloğu PostgreSQL `0097_vulnerabilities` içinde; D-142). Katalog durumu için yalnızca PostgreSQL auth modunda
(aksi halde `404`). Yalnızca okuma: bulguyu eşleştirici üretir, kişinin bu konuda yaptığı şey ise
host'ta olur.

Envanter, M1'den beri kurulu her paketi ve sürümünü biliyor. Eksik olan, cümlenin diğer yarısıydı (bu sürümlerin
hangilerinin açıklı olduğu bilindiği) ve o yarı bir feed'dir. openlog,
host'larının gerçekten çalıştırdığı ekosistemlerin OSV dışa aktarımlarını (osv.dev) indirir ve API **leader**'ı
her host'un paketlerini bunlarla eşleştirir:

| Adım | Ne demek |
|---|---|
| ekosistem | host başına işletim sistemi öğesinden ve paket yöneticisinden türetilir: `Debian:12`, `Ubuntu:22.04`, `Alpine:v3.19`, `Rocky Linux:9`. Sürüm numarası bunun parçasıdır, çünkü aynı paket farklı dağıtım sürümlerinde farklı sürümlerde yamalanır |
| sürüm | ekosistemin kendi şemasında karşılaştırılır: dpkg'nin `epoch:upstream-revision`'ı, rpm'in `rpmvercmp`'si, apk'nın son ekleri, başka yerde SemVer. `1.2.3-4ubuntu0.2 < 1.2.3-4ubuntu0.3`, string'lerle değil dpkg ile ilgili bir gerçektir |
| bulgu | (host, advisory, paket) başına bir satır. Host'taki sürümü ve onu düzelten sürümü taşır |
| çözüm | paketi artık açıklı bulmayan sonraki eşleştirme çalışması `resolved_at` ayarlar; `first_seen` kalır, çünkü henüz kimsenin yamalamadığı bir açık hakkındaki soru "ne zamandan beri"dir |

Bir dağıtımın advisory'si **dağıtımın** düzeltilmiş sürümünü taşır, "1.2.3-4+deb12u2
yamalı" sorusunu yanıtlanabilir kılan da budur; upstream feed backport edilen her düzeltmeyi hâlâ açıklı olarak bildirirdi.

Feed URL'si yapılandırılabilir ([config.md](/tr/contracts/config#vulnerabilities-api-allinone-d-142)): mirror indirmeyi ağ içinde tutar,
boş değer ise air-gapped kurulum için indirmeyi tamamen kapatır; böyle bir kurulum kataloğu kendisi doldurur ve eşleştirmeyi yine alır.

### `GET /api/v1/vulnerabilities?severity=&host_id=&sort=&dir=&limit=&offset=` {#get-api-v1-vulnerabilities-severity-host-id-sort-dir-limit-offset}
`sort`: `severity` (varsayılan, en ciddi başta, sonra en çok host), `advisory`, `score`, `hosts`. `limit` varsayılan olarak 1000'dir ve orada durur. `severity_counts` sayfanın değil tüm listenindir ve yanıt `has_more` taşır (bkz. [Listeler](#lists)).

Açık bulgular advisory'ye göre gruplanmış, en ciddi başta ve bir severity içinde en çok host'u etkileyen
advisory başta. `severity_counts`, başlığın gösterdiği fleet özetidir.
```json
{"vulnerabilities": [{"vuln_id": "DSA-5600-1", "cve": "CVE-2026-0001", "severity": "critical", "score": 9.8,
  "summary": "buffer overflow in libexample", "hosts": 12, "packages": ["libexample"],
  "first_seen": "…", "last_seen": "…"}],
 "severity_counts": {"critical": 1, "high": 4, "medium": 9, "low": 2, "none": 0}}
```

### `GET /api/v1/vulnerabilities/{id}?limit=&sort=&dir=&offset=` · `GET /api/v1/hosts/{host_id}/vulnerabilities?limit=&sort=&dir=&offset=` {#get-api-v1-vulnerabilities-id-limit-sort-dir-offset-·-get-api-v1-hosts-host-id-vulnerabilities-limit-sort-dir-offset}
Tek bir advisory'nin veya tek bir host'un bulguları: `host_name`, `package`, kurulu `version` ve onu çözen
`fixed_in` (feed düzeltme bilmiyorsa `""`; en önemli durum budur). `sort`: `severity` (varsayılan, en ciddi başta, sonra pakete göre), `score`, `advisory`, `package`, `host`, `version` (metin olarak), `ecosystem`. `limit` varsayılan olarak 1000'dir ve orada durur; daha büyüğü reddedilmez, buna kesilir. Yanıt `has_more` taşır (bkz. [Listeler](#lists)).

Advisory ayrıca tüm açık bulguları üzerinden ve sayfadan bağımsız olarak `advisory`, `{"cve", "severity", "score", "summary", "findings", "without_fix"}` döner: herhangi bir host'taki en ciddi severity ve en yüksek skor. Açık bulgusu olmayan advisory `404`'tür. Sonuncuyu aşan sayfa `findings` olmadan `200`'dür, çünkü advisory hâlâ vardır.

### `GET /api/v1/vulnerabilities/catalog/status` {#get-api-v1-vulnerabilities-catalog-status}
Kataloğun ne tuttuğu ve her ekosistemin en son ne zaman eşitlendiği. Boş listeyi hiç eşitlenmemiş bir feed'den
ayıran şey budur: `{"vulnerabilities": 61234, "affected_ranges": 98211, "ecosystems": 3,
"last_synced_at": "…", "sources": [{"source": "osv", "ecosystem": "Debian:12", "synced_at": "…",
"count": 41022, "error": ""}]}`.

## Job izleme {#job-monitoring}

Çağıranın organizasyonunun cron ve heartbeat monitörleri (PostgreSQL `job_monitors` + `job_monitor_state`,
`0096_job_monitors`; sonuçlanan çalışmalar ClickHouse `job_runs` içinde, 90 gün; D-141). Yalnızca PostgreSQL auth modunda
(aksi halde `404`). Okumalar: her rol ve API anahtarları. Yazmalar: member ve üstü, API anahtarları da; organizasyon başına en fazla 200
monitör (`409`). Audit: `job_monitor.{create,update,delete,rotate_token}`.

Bu, metrik hattının üretemeyeceği sinyaldir, çünkü **olmayan** bir şeyle ilgilidir.
Çalışmayı bırakan bir yedekleme betiği hiçbir şey göndermez (log yok, span yok, sıfıra düşen metrik yok)
ve "veri yok", "host yeniden kuruluyor"dan ayırt edilemez. Monitör zamanlamayı önceden
yazar, böylece sessizlik bir gerçeğe dönüşür.

| `kind` | Zamanlama | Sonraki çalışma ne zaman beklenir |
|---|---|---|
| `cron` | `time_zone` içinde okunan `cron` (beş alan veya makro) | ifadenin sonraki gerçekleşmesinde |
| `interval` | `interval_seconds` | son bildirimden o kadar sonra |

Bir çalışma, onu bitiren ping ile veya beklenen zamanın `grace_seconds` içinde hiçbir şey gelmezse API **leader**'ının süpürücüsüyle sonuçlanır
(`missed` veya bir start ping'i gelmiş ve bitiş izlememişse `overrun`). Alma işlemi durum satırını aynı ifadede ilerletir, yani kaçırılan bir çalışma,
leader'lık pod'lar arasında taşınırken bile bir kez sonuçlanır.

Sonuçlanan her çalışma gauge veri noktaları olarak da yazılır, böylece **metrik alert kuralları** ([alerting.md](/tr/contracts/alerting)
§2.2) ve dashboard'lar bir job'u diğer sinyaller gibi izler:

| Metrik | Birim | Değer |
|---|---|---|
| `jobs.run.success` | `1` | başarılı çalışma için `1`, başarısız veya kaçırılan için `0` |
| `jobs.run.missed` | `1` | hiç bildirmeyen çalışma için `1`; `max over 10m > 0`, çalışmayı bırakan job için alert'tir |
| `jobs.run.late` | `s` | Çalışmanın beklenen zamandan kaç saniye sonra sonuçlandığı; başarısız olmaya başlamadan önce "gecelik yedek her hafta daha geç bitiyor" diyen sayı |
| `jobs.run.duration` | `ms` | Çalışmanın ne kadar sürdüğü, **yalnızca** openlog iki ucunu da gördüyse |

Hepsi `job.id`, `job.name` ve `job.status` attribute'larını taşır.

### `GET /api/v1/jobs/monitors?summary=` · `POST /api/v1/jobs/monitors` · `GET|PUT|DELETE /api/v1/jobs/monitors/{id}` · `POST /api/v1/jobs/monitors/{id}/rotate` {#get-api-v1-jobs-monitors-summary-·-post-api-v1-jobs-monitors-·-get-put-delete-api-v1-jobs-monitors-id-·-post-api-v1-jobs-monitors-id-rotate}
POST/PUT gövdesi: `{"name" (1–200), "description"? (≤ 1000), "kind"?: `cron`|`interval` (default `cron`),
"cron" (cron only: five fields or @yearly/@monthly/@weekly/@daily/@hourly), "time_zone"? (cron only: an IANA
name, default UTC), "interval_seconds" (interval only: 60–7776000), "grace_seconds"? (default 300, 0–86400),
"enabled"? (default true), "tags"? (≤ 10)}`. Yanıt `ping_url` ve `state` taşır; `rotate` yeni token üretir
ve eski URL hemen çalışmaz olur.

```json
{"monitors": [{"id": "…", "name": "Nightly backup", "kind": "cron", "cron": "0 3 * * *",
  "time_zone": "Europe/Istanbul", "interval_seconds": 0, "grace_seconds": 900, "enabled": true,
  "tags": ["backup"], "ping_url": "https://openlog.example.com/api/v1/jobs/ping/olj_…",
  "state": {"status": "success", "last_ping_at": "…", "last_started_at": "…", "last_finished_at": "…",
            "last_duration_ms": 148000, "last_exit_code": 0, "last_message": "42 GB written",
            "expected_at": "…", "consecutive_failures": 0, "late": false},
  "summary": {"runs": 7, "failures": 0, "missed": 0, "avg_ms": 151000, "max_ms": 162000, "last_at": "…"}}]}
```

### `GET /api/v1/jobs/monitors/{id}/runs?from=&to=&limit=` {#get-api-v1-jobs-monitors-id-runs-from-to-limit}
Sonuçlanan çalışmalar, en yenisi başta (`status`, `started_at`, `duration_ms`, `exit_code`, `late_seconds`,
`message`, `source`), aralık üzerinden aynı `summary` ile. Aralık en fazla 90 gündür, çalışma geçmişinin
saklama süresi.

### `GET|POST /api/v1/jobs/ping/{token}[/start|/fail]` {#get-post-api-v1-jobs-ping-token-start-fail}
**Kimlik doğrulaması yok.** Çağıran, openlog kimlik bilgisi olmayan bir makinedeki crontab satırıdır; URL'deki
token monitörü tanımlar. `GET`, `curl` ve `wget`'in varsayılan olarak gönderdiğidir; `POST` job'un
çıktısını (≤ 4 KiB) taşıyabilir ve `?exit=<code>` çıkış durumunu bildirir: sıfır olmayan kod, olay nasıl
yazılmış olursa olsun başarısızlıktır. `?msg=`, gövde göndermek zahmetliyse çıktıdır. Yanıt düz metindir
(`ok success next expected …`), çünkü çağıranın onunla yapabileceği şey loga yazdırmaktır.

```sh
# bir crontab kaydının tüm bildirim satırı
0 3 * * * /usr/local/bin/backup.sh; curl -fsS -m 10 --retry 3 "$OPENLOG_PING/$?" >/dev/null

# start ping'i ile, böylece overrun (başladı, hiç bitmedi) görünür olur
0 3 * * * curl -fsS "$OPENLOG_PING/start" >/dev/null; /usr/local/bin/backup.sh 2>&1 | tail -c 4000 | curl -fsS --data-binary @- "$OPENLOG_PING/$?" >/dev/null
```

**Tehdit modeli.** Ping URL'si doğası gereği herkese açıktır: crontab'larda, CI yapılandırmasında ve
süreç listelerinde durur ve openlog'da gizlenmek yerine gösterilir, çünkü kimsenin yeniden okuyamadığı URL'yi kimse
düzeltemez. Kopyasının yapabileceği şey **o tek monitörün** çalışmasını bildirmektir: olmayan bir başarıyı bildirmek (sonucu, job'un hiç ping atmamasıyla aynıdır) veya başarısızlık bildirip gürültü çıkarmak.
Hiçbir şey okuyamaz, başka bir sinyal yazamaz veya başka bir monitörü adlandıramaz. Sızan URL
`rotate` ile değiştirilir. Yanlış token ve silinmiş monitör aynı `404`'ü döner, yani bir yoklama hangi
token'ların var olduğunu öğrenemez.

## Sentetik izleme {#synthetic-monitoring}

Çağıranın organizasyonunun zamanlanmış dışarıdan içeriye kontrolleri (PostgreSQL `synthetic_checks`,
`0090_synthetics`; çalışmalar ClickHouse `synthetic_runs` içinde, 30 gün; D-132, D-140). Dört tür, her biri için bir satır ve
konum başına bir zamanlama:

| `type` | Adresler | Ne zaman başarılı |
|---|---|---|
| `http` | `url` | durum `expected_status` içinden biriyse ve body doğrulaması tutuyorsa |
| `tcp` | `target` (`host:port`) | bağlantı kabul edilirse (socket'e hiçbir şey yazılmaz) |
| `dns` | `target` (bir ad) + `dns_record_type` | ad çözülür ve `dns_expected` içindeki her yanıt kayıtlar arasındaysa |
| `tls` | `target` (`host:port`) | handshake tamamlanır, zincir ve ad doğrulanır ve sertifika `tls_warning_days` içinde dolmuyorsa |

Bir kontrol tam olarak tek bir şeyi adresler: `http` kontrolünün `url`'si olur `target`'ı olmaz, diğer her türün
`target`'ı olur `url`'si olmaz; diğer türlerin alanları boş saklanır ve veritabanı bunu kısıt olarak
yineler. `tls` kontrolü sistem köklerine artı `OPENLOG_SYNTHETICS_CA_FILE` değerine göre doğrular;
bilerek kontrol başına "doğrulamayı atla" yoktur, çünkü doğrulamayan tls kontrolü hiçbir şey kontrol etmez. Yalnızca PostgreSQL auth modunda
(aksi halde `404`). Okumalar: her rol ve API anahtarları (telemetri). Yazmalar: member ve üstü, API anahtarları da; organizasyon başına en fazla
100 kontrol (`409`). Audit: `synthetic_check.{create,update,delete}`.

API **leader**'ı zamanı gelen kontrolleri kontrol, aralık ve konum başına bir çalışma olarak yürütür; alma işlemi
zamanlama satırını aynı ifadede ilerletir, yani leader'lık pod'lar arasında taşınırken bile bir kontrol iki kez çalışmaz. Kontrol,
sunucunun bir üye adına yaptığı istektir, bu yüzden korunur: `OPENLOG_SYNTHETICS_ALLOW_PRIVATE_NETWORKS` yoksa yalnızca public adresler (SSRF koruması, DNS çözümlemesinden sonra bağlantı başına kontrol edilir),
yönlendirme sınırı ve yanıt boyutu sınırı ([config.md](/tr/contracts/config#synthetic-monitoring-api-allinone-d-132)).

Her çalışma ayrıca iki gauge veri noktası olarak yazılır, böylece **metrik alert kuralları** ([alerting.md](/tr/contracts/alerting)
§2.2) ve dashboard'lar bir kontrolü diğer metrikler gibi izler ve 1 dakikalık rollup onu 395 gün tutar
([apm.md](/tr/contracts/apm) §8):

| Metrik | Birim | Değer |
|---|---|---|
| `synthetics.check.success` | `1` | başarılı çalışma için `1`, başarısız için `0`; bir pencere üzerinden `avg`, uptime oranıdır |
| `synthetics.check.duration` | `ms` | Toplam çalışma süresi, başarısız çalışmada da kaydedilir |
| `synthetics.certificate.expiry` | `d` | Çalışmanın gördüğü sertifikada kalan gün, süresi dolunca negatif. `tls` kontrolleri **ve** zaten aynı handshake'i tamamlayan `https` kontrolleri için üretilir, yani tek kural ("14'ün altında") kontrol edilen her endpoint'in süre sonunu kapsar |

`check.id`, `check.name`, `check.type` ve `location` attribute'larını taşırlar; ayrıca http kontrolünde `url.full` ve
`http.request.method`, diğer türlerde `server.address`, yanıt geldiyse `http.response.status_code`
ve çalışma başarısızsa `error.kind` (`dns`, `connect`, `tls`, `certificate`,
`record`, `timeout`, `blocked`, `redirect`, `status`, `assertion`, `body`, `request`).

### `GET /api/v1/synthetics/checks?summary=&from=&to=` · `POST /api/v1/synthetics/checks` · `GET|PUT|DELETE /api/v1/synthetics/checks/{id}` {#get-api-v1-synthetics-checks-summary-from-to-·-post-api-v1-synthetics-checks-·-get-put-delete-api-v1-synthetics-checks-id}
POST/PUT gövdesi: `{"name" (1–200), "type"?: `http`|`tcp`|`dns`|`tls` (default `http`), "enabled"? (default
true), "url" (http only: http(s), ≤ 2048, no credentials or fragment), "target" (tcp and tls: `host:port`
with an explicit port; dns: the name to resolve; ≤ 512), "dns_record_type"? (dns only: `A` (default),
`AAAA`, `CNAME`, `MX`, `NS`, `TXT`), "dns_expected"? (dns only: ≤ 10 answers, empty = any answer),
"tls_warning_days"? (tls only: default 14, 0–365; 0 = only an expired certificate fails), "method"? (default `GET`; `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`,
`OPTIONS`), "headers"? (≤ 20; `Host`, `Content-Length`, `Connection`, `Transfer-Encoding` and `Upgrade` are
rejected), "body"? (≤ 65536, only with a method that sends one), "expected_status"? (default `[200]`, ≤ 10
codes of 100–599), "assertion_type"?: "none"|"contains"|"not_contains"|"json_path", "assertion_path"?
(`json_path` only: a dotted path such as `data.items.0.status`), "assertion_value"?, "timeout_ms"? (default
10000, 500–60000, not longer than the interval), "interval_seconds"? (default 300, 5–86400; the scheduler
claims due rows once a second, so 5 s is the floor), "locations"? (default `["local"]`)}`. `location` `local`,
openlog sunucusunun kendisidir; yanıtın `locations` alanı bu kurulumun sunduklarını listeler.

`status`, konum başına zamanlama satırıdır: son sonuç (güncel durum, doğrudan PostgreSQL'den)
ve sonraki çalışmanın ne zaman olduğu. `summary=false` değilse liste, aralık üzerinden (`from`/`to`, varsayılan son 24 sa)
kontrol başına `summary` ekler; aralıkta çalışma yoksa `uptime` ve yüzdelikler `null`'dır.
```json
{"checks": [{"id": "…", "name": "Checkout health", "type": "http", "enabled": true,
  "url": "https://shop.example.com/health", "method": "GET", "headers": {}, "body": "",
  "expected_status": [200], "assertion_type": "contains", "assertion_path": "", "assertion_value": "ok",
  "target": "", "dns_record_type": "", "dns_expected": [], "tls_warning_days": 0,
  "timeout_ms": 10000, "interval_seconds": 300, "locations": ["local"],
  "created_by_email": "ada@example.com", "updated_by_email": "ada@example.com",
  "created_at": "…", "updated_at": "…",
  "status": [{"location": "local", "next_run_at": "…", "last_run_at": "…", "last_success": true,
              "last_status_code": 200, "last_duration_ms": 123.5, "last_error_kind": "", "last_error": ""}],
  "summary": {"from": "…", "to": "…", "step": "60s", "runs": 288, "failures": 3, "uptime": 98.96,
              "avg_ms": 120.4, "p50_ms": 110, "p95_ms": 240, "p99_ms": 480,
              "points": [{"t": 1757757600000, "runs": 12, "failures": 0, "uptime": 100, "p95_ms": 130}]}}],
 "locations": ["local"]}
```

### `GET /api/v1/synthetics/checks/{id}/results?from=&to=&step=&failures=` {#get-api-v1-synthetics-checks-id-results-from-to-step-failures}
Aralık üzerinden uptime ve latency yüzdelikleri (`from`/`to`, varsayılan son 24 sa; `step` ≥ `60s`, varsayılan
≈ 60 nokta, tam dakikalara yukarı yuvarlanır), arkalarındaki seri ve en son başarısız çalışmalar
(`failures`, varsayılan 20, en fazla 200; `0` onları atlar).
```json
{"check": {…}, "summary": {…as above…},
 "failures": [{"timestamp": "…", "location": "local", "status_code": 503, "error_kind": "status",
               "error": "HTTP 503, expected 200", "duration_ms": 87.2}]}
```

## Veritabanı sorgu performansı {#database-query-performance}

Altyapı ajanlarının izlediği veritabanı sunucularında neyin çalıştığı (**[db-monitoring.md](/tr/contracts/db-monitoring)**, D-138):
aralık başına ifade istatistikleri, bekleme ve bloklama zincirleriyle örneklenen oturumlar ve yürütme
planları. Telemetri okumaları (her rol, API anahtarları da). Bir instance `service.instance.id` ile
(`instance=`), bir ifade fingerprint'iyle adreslenir. `from`/`to` varsayılan olarak son bir saattir.

### `GET /api/v1/db/instances?from=&to=&sort=&dir=&limit=&offset=` {#get-api-v1-db-instances-from-to-sort-dir-limit-offset}
```json
{"instances": [{"instance": "db1.internal:5432", "db_system": "postgresql", "host_id": "h-1", "host_name": "db1",
                "server_address": "db1.internal", "server_port": 5432, "calls": 423200, "throughput": 117.6,
                "total_time_ms": 1000000, "avg_ms": 2.36, "statements": 312, "errors": 0,
                "avg_active_sessions": 3.4, "top_wait": "Lock", "last_seen": "…"}]}
```
`avg_active_sessions`, aralığın örneklerinin örnekleme anlarına bölümüdür (oturum örneklemesi yoksa `null`);
örneklerin çoğu beklemiyorsa `top_wait` `CPU`'dur. `sort`: `time` (varsayılan), `calls`, `avg`, `aas`, `errors`, `statements`, `instance`,
`host`, `wait`, `last_seen`; eksik değer her iki yönde de sondadır. `limit` yoksa her instance'tır. İfade
istatistikleri ve oturum örnekleri birleştirildikten sonra sıralanır; her biri 500 instance'a kadar okunur, yani ancak o kadarına kadar eksiksizdir. `has_more` başka sayfa olup olmadığını söyler.

### `GET /api/v1/db/queries?instance=&from=&to=&sort=&dir=&db=&q=&limit=&offset=` {#get-api-v1-db-queries-instance-from-to-sort-dir-db-q-limit-offset}
`sort`: `time` (varsayılan), `calls`, `avg`, `rows`, `rows_per_call`, `errors`, `reads`, `statement`; `q`, ifadede büyük/küçük harf duyarsız alt dizedir;
`limit` ≤ 1000. Yanıtta `has_more` vardır ve `time_share` sayfanın değil tüm instance'ındır.
```json
{"total_time_ms": 1000000,
 "queries": [{"fingerprint": "696841490555676800", "query_id": "-4242", "text": "UPDATE orders SET amount = ? WHERE id = ?",
              "db_names": ["shop"], "calls": 1200, "throughput": 0.33, "total_time_ms": 540000, "avg_ms": 450,
              "time_share": 0.54, "rows": 1200, "rows_per_call": 1, "rows_examined": 0, "errors": 0,
              "no_index_used": 0, "blocks_hit": 4800, "blocks_read": 24, "cache_hit_ratio": 0.995}]}
```

### `GET /api/v1/db/queries/{fingerprint}?instance=&from=&to=&step=` {#get-api-v1-db-queries-fingerprint-instance-from-to-step}
Tek bir ifade: toplamları, serisi (`step` ≥ 60 s), her farklı yürütme planı (en yenisi başta, `is_current`,
`plan_change`), örneklerinin bekleme olayları ve onu çalıştıran APM servisleri (birleştirme normalleştirilmiş metinle yapılır,
db-monitoring.md §4.1). İfade aralıkta instance'ta çalışmadıysa `404 not_found`.
```json
{"from": "…", "to": "…", "step": "60s", "db_system": "postgresql", "query": {…as above…},
 "points": [{"t": 1757757600000, "calls": 20, "throughput": 0.33, "total_time_ms": 42, "avg_ms": 2.1, "rows": 20}],
 "plans": [{"plan_hash": "9066c2e27543bd21", "format": "json", "plan": "[{\"Plan\":…}]", "total_cost": 8.3,
            "db_name": "shop", "first_seen": "…", "last_seen": "…", "captures": 2, "is_current": true, "plan_change": false}],
 "waits": [{"type": "Lock", "event": "transactionid", "samples": 90, "share": 0.9}],
 "callers": [{"service_name": "checkout", "environment": "prod", "calls": 12000, "avg_ms": 3.1, "errors": 0}]}
```

### `GET /api/v1/db/activity?instance=&from=&to=&step=` {#get-api-v1-db-activity-instance-from-to-step}
Step başına bekleme türü başına ortalama aktif oturum, en üst bekleme olayları ve en çok örneği olan ifadeler.
```json
{"from": "…", "to": "…", "step": "60s",
 "series": [{"wait_type": "Lock", "points": [[1757757600000, 2.4]]}],
 "waits": [{"type": "Lock", "event": "transactionid", "samples": 1445, "share": 0.62}],
 "top_queries": [{"fingerprint": "…", "text": "UPDATE …", "samples": 1200, "avg_active_sessions": 2.1,
                  "top_wait": "Lock:transactionid"}]}
```

### `GET /api/v1/db/activity/waits?instance=&from=&to=&sort=&dir=&limit=&offset=` {#get-api-v1-db-activity-waits-instance-from-to-sort-dir-limit-offset}
Aralığın her bekleme olayı (`/db/activity` ilk 20'yi tutar): `{"waits": [{"type", "event", "samples", "share"}], "has_more"}`. `sort`: `samples`
(varsayılan), `type`, `event`. `share`, instance'ın aralıktaki tüm örnekleri üzerindendir; `/db/activity` içinde ise 20'si üzerindendir.

### `GET /api/v1/db/activity/queries?instance=&from=&to=&sort=&dir=&limit=&offset=` {#get-api-v1-db-activity-queries-instance-from-to-sort-dir-limit-offset}
Örneklerin yakaladığı her ifade (`/db/activity` ilk 20'yi tutar): `{"queries": [{"fingerprint", "text", "samples", "avg_active_sessions",
"top_wait"}], "has_more"}`. `sort`: `samples` (varsayılan), `statement`, `wait`.

### `GET /api/v1/db/sessions?instance=&at=` {#get-api-v1-db-sessions-instance-at}
`at` (unix ms, varsayılan şimdi) anında veya öncesinde, son 5 dakika içindeki en son örnekleme anı. `blocks`,
bu oturumu doğrudan veya bir zincir üzerinden bekleyen oturumları sayar. Sayfası yoktur, çünkü bloklama zinciri ancak örneğin tamamı üzerinden çizilebilir:
en uzun çalışan 1000 oturumu tutar ve örnekte daha fazlası varsa `has_more` true olur.
```json
{"sampled_at": "…",
 "sessions": [{"session_id": "4700", "state": "idle in transaction", "wait_type": "Client", "wait_event": "ClientRead",
               "db_name": "shop", "user": "app", "application": "checkout", "client_address": "10.0.0.7",
               "duration_ms": 61000, "fingerprint": "", "text": "", "blocking_session_ids": [], "blocks": 2}]}
```

### `GET /api/v1/db/lookup?db_system=&statement=&from=&to=` {#get-api-v1-db-lookup-db-system-statement-from-to}
İstatistikleri normalleştirilmiş bir ifade tutan instance'lar: APM veritabanı çağrısından sunucu görünümüne bağlantı.
```json
{"matches": [{"instance": "db1.internal:5432", "fingerprint": "…", "host_name": "db1", "calls": 967, "avg_ms": 0.2}]}
```

## Sürekli profiling {#continuous-profiling}

Yavaş bir span'in *içinde* hangi fonksiyonun zaman harcadığı ([profiles.md](/tr/contracts/profiles)). Okumalar telemetri okumasıdır
(her rol, API anahtarları da) ve call stack'in ingest sırasında zaten açıldığı `profiles` (`0095_profiles`) üzerinden geçer;
yani flame graph beş join değil, tek agregasyondur.

Değerlerin toplandığı her yerde **profil türü zorunludur** ve bilerek `cpu` varsayılanı yoktur:
nanosaniye ve byte toplanamaz, yani belirtilmemiş tür üzerinden çizilen grafik birimsiz bir sayı olurdu. Her
yanıt profilin kendi `ValueType` değerinin bildirdiği `unit`'i yineler, yani grafik "ns"yi asla sabit kodlamaz.

### `GET /api/v1/profiles/services?from=&to=&sort=&dir=&limit=&offset=` {#get-api-v1-profiles-services-from-to-sort-dir-limit-offset}
`sort`: `samples` (varsayılan), `total`, `last_seen`, `service`, `environment`, `type`. `limit` yoksa `OPENLOG_API_MAX_ROWS` kadar her şeyle yanıt verir ve yanıt `has_more` taşır (bkz. [Listeler](#lists)).

`{"services": [{"service", "environment", "type", "unit", "samples", "total", "last_seen"}]}`.

Servis, ortam ve profil türü başına bir kayıt; çağırana diğer iki endpoint'in hangi türleri kabul edeceğini söyleyen şey budur.

### `GET /api/v1/profiles/flame?service=&type=&environment=&host=&limit=&from=&to=` {#get-api-v1-profiles-flame-service-type-environment-host-limit-from-to}
```json
{"unit": "nanoseconds", "type": "cpu",
 "flame": {"name": "all", "value": 4200000,
           "children": [{"name": "main", "value": 4200000,
                         "children": [{"name": "handleRequest", "value": 3100000}]}]}}
```
Flame graph'ın çizdiği ağaç, kök başta. Aynı stack'ler ClickHouse'ta katlanır; `limit` farklı stack'leri
değere göre sıralı olarak 20000 ile sınırlar, yani sondan düşenler en dar dilimlerdir.

### `GET /api/v1/profiles/functions?service=&type=&environment=&host=&limit=&from=&to=` {#get-api-v1-profiles-functions-service-type-environment-host-limit-from-to}
`{"unit", "type", "total", "functions": [{"function", "self", "samples"}]}`.

**Self time**'a göre sıralı fonksiyonlar: en içteki frame'e atfedilen değer, yani bir profile sorulan ilk soru.
`total`, pencerenin değil dönen satırların toplamıdır, yani yüzde, çağıranın görebildiği bir şeyin yüzdesidir.

## Alerting {#alerting}

Çağıranın organizasyonunun kuralları, incident'ları, bildirim kanalları, yönlendirme kuralları, mute pencereleri ve teslimat logu. Anlam
(kural türleri, değerlendirme, durum makinesi, bildirimler, payload'lar, sırlar): [alerting.md](/tr/contracts/alerting); şekiller:
[openapi.yaml](https://github.com/onuragtas/openlog/blob/master/docs/contracts/openapi.yaml) tag `alerts`; tablolar: [postgres.md](/tr/contracts/postgres#alerting-0004_alerting). Okumalar her rolü kabul eder
(API anahtarları da); yazmalar [Roller](#roles) bölümündeki rolü ister, rolü izin veren API anahtarı da yazabilir (oturumlarda her zamanki gibi CSRF): member'lar kural ve
mute oluşturur, yalnızca kendi oluşturduklarını değiştirir (aksi halde `403 permission_denied`), incident'lar üzerinde çalışır; admin'ler ve owner'lar
her şeyi değiştirir ve kanalları yönetir. `OPENLOG_AUTH_MODE=static` ile kullanılamaz (`404`). Her yazma audit'lenir
(`alert.*`, alerting.md §7).

| Endpoint | Notlar |
|---|---|
| `GET /api/v1/alerts/rule-types` | `{"types": [{"type", "available", "reason"}]}` |
| `GET /api/v1/alerts/rules?type=&enabled=` · `POST /api/v1/alerts/rules` | ada göre sıralı liste; oluşturma → `201` Rule. `OPENLOG_ALERT_MAX_RULES_PER_ORG` sınırında `409` |
| `GET /api/v1/alerts/rules/{id}` | Rule + `series` (ok olmayan seri durumları) |
| `PUT /api/v1/alerts/rules/{id}` · `DELETE …` | tam değiştirme (isteğe bağlı `version` → eskiyse `409`); `type`/`condition` değiştirmek açık incident'ları çözer (`rule_changed`); silme onları çözer (`rule_deleted`), incident'lar tutulur |
| `POST /api/v1/alerts/rules/{id}/enable` · `…/disable` | disable açık incident'ları çözer (`rule_disabled`) |
| `POST /api/v1/alerts/rules/preview` `{"rule": RuleInput, "hours": 1..24}` | kaydedilmemiş tanımı son saatler üzerinden tenant kapsamlı sorgu katmanıyla değerlendirir (viewer): `{"from", "to", "step_seconds", "operator", "threshold", "recovery_threshold", "unit", "series": [{"key", "labels", "points": [[ms, value\|null]], "transitions": [{"at", "state", "value"}], "incidents": [{"opened_at", "resolved_at", "peak"}]}], "truncated", "approximate"}` |
| `GET /api/v1/alerts/rules/{id}/evaluations?from=&to=` | ClickHouse'tan değerlendirme geçmişi (alerting.md §3.6; varsayılan son 24 sa, ≤ 30 gün, ≤ 500 bucket): `{"from", "to", "step_seconds", "evaluations": [{"at", "firing_series", "evaluations", "errors", "duration_ms", "max_duration_ms"}], "series": [{"series_key", "labels", "points": [[ms, value\|null, state]]}], "truncated"}` |
| `GET /api/v1/alerts/incidents?state=open,acknowledged&rule_id=&severity=&limit=&cursor=` | en yenisi başta (varsayılan 50, en fazla 500), `next_cursor` (son sayfada `null`; sonraki için `cursor` olarak ver), `counts {open, acknowledged, resolved (7 d)}` |
| `GET /api/v1/alerts/incidents/{id}` | Incident + `events` (zaman çizelgesi) + `deliveries` |
| `POST /api/v1/alerts/incidents/{id}/acknowledge` · `…/resolve` `{"note"?}` · `…/notes` `{"text"}` | ack idempotent'tir (çözülmüşse `409`; yeniden bildirimleri durdurur) ve incident'ın PagerDuty/Opsgenie kanalları için `acknowledged` bildirimini kuyruğa alır (alerting.md §5.1); resolve çözüm bildirimlerini kuyruğa alır; note → `201` olay |
| `GET /api/v1/alerts/channels` · `POST` · `GET/PUT/DELETE /api/v1/alerts/channels/{id}` | türler `slack`·`email`·`webhook`·`teams`·`pagerduty`·`opsgenie`; sırlar yalnızca yazılabilir (`url`, `hmac_secret`, `smtp_password`, `routing_key`, `api_key`; `secret_hints` maskeli; gönderilmeyen sır alanları saklanan değerleri korur; `generated_secrets` oluştururken bir kez); liste `secrets_configured` taşır. `OPENLOG_SECRETS_KEY` yoksa `409` |
| `POST /api/v1/alerts/channels/{id}/test` | eşzamanlı test gönderimi: `{"success", "status_code", "error", "duration_ms", "notification_id"}` (kanal varsa her zaman `200`). PagerDuty ve Opsgenie testleri alert'i açıp hemen kapatır |
| `GET /api/v1/alerts/routing-rules` · `POST` · `GET/PUT/DELETE /api/v1/alerts/routing-rules/{id}` | sıralı yönlendirme kuralları (alerting.md §5.6): `{name, position, enabled, is_default, channel_ids, match {severities, services, rule_types, labels, time_window}}`; okumalar her rol, yazmalar admin/owner. İkinci varsayılan rota için veya 100 kuralda `409` |
| `POST /api/v1/alerts/routing-rules/reorder` `{"ids": [...]}` | admin/owner: değerlendirme sırasını ayarlar; `ids` organizasyonun her yönlendirme kuralını tam bir kez listelemelidir (aksi halde `400`) → yeniden sıralanmış liste |
| `GET /api/v1/alerts/mutes?include_expired=` · `POST` · `PUT/DELETE /api/v1/alerts/mutes/{id}` | `starts_at`/`ends_at` (≤ 90 gün), `rule_ids`, label `matchers`; `active` hesaplanır. Yinelenen: `schedule {timezone, days\|rrule, start_time, end_time, from, until}` (alerting.md §5.2); yanıtlar o zaman `starts_at`/`ends_at` içinde mevcut veya sonraki gerçekleşmeyi taşır |
| `POST /api/v1/alerts/mutes/preview` `{"schedule"}` | her rol: kaydedilmemiş zamanlamayı doğrular, `{"occurrences": [{"starts_at", "ends_at"}]}` (≤ 5, istisnalar uygulanmış). Mute yanıtları aynı listeyi `upcoming` olarak taşır; zamanlamalar `FREQ=MONTHLY` kuralları, `exdates` ve `holiday_calendar_ids` kabul eder (alerting.md §5.2) |
| `GET /api/v1/alerts/holiday-calendars` · `POST` · `GET/PUT/DELETE /api/v1/alerts/holiday-calendars/{id}` | adlandırılmış tatil tarihi kümeleri `{name, description, dates: ["YYYY-MM-DD" \| "MM-DD"]}` (≤ 1000), `mute_count` ile; yazmalar admin/owner; mute'lar referans verirken silme → `409` |
| `GET /api/v1/alerts/templates?category=&integration=` | önerilen kural şablonları (alerting.md §2.8): `{"templates": [{"id", "category", "integration", "rule_type", "severity", "metric", "reference_metric", "name": {en, tr}, "description", "params": [...]}]}` |
| `POST /api/v1/alerts/templates/{id}/render` `{"params", "language", "name", "channel_ids"}` | viewer (oran eşikleri için telemetri okur): `{"rule": RuleInput, "reference": {"metric", "value", "ratio"} \| null}`; geçersiz parametreler için `400`, bilinmeyen şablon için `404`, oran referansı eksik veya 0 ise `409` |
| `GET /api/v1/alerts/deliveries?channel_id=&incident_id=&status=&limit=` | teslimat logu en yenisi başta (varsayılan 100, en fazla 500), `attempt_log` ile |

## Sorgu dili (OQL) {#query-language-oql}

Dil: [oql.md](/tr/contracts/oql). Her rol ve API anahtarları (`telemetry.read`); tenant, her telemetri endpoint'inde olduğu gibi principal'dan gelir.
İki auth modunda da kullanılabilir. Yanıtlar `Cache-Control: no-store` taşır.

### `POST /api/v1/query` `{"query", "from"?, "to"?, "variables"?}` {#post-api-v1-query-query-from-to-variables}
`from`/`to` (RFC3339 veya unix ms, ikisi birden veya hiçbiri) `SINCE`/`UNTIL` değerlerini ezer (dashboard'ların zaman seçicisi). `variables`:
`{"name": "value" | ["v1", "v2"]}` (oql.md §3). Sonuç:
```json
{"kind": "single" | "facets" | "timeseries" | "histogram", "event_type": "Log",
 "columns": [{"name": "count(*)", "function": "count", "type": "number" | "string"}],
 "facets": ["service.name"],
 "rows": [{"facets": ["api"], "values": [42, null]}],
 "series": [{"facets": ["api"], "column": 0, "points": [[1757757600000, 42], [1757757660000, null]]}],
 "buckets": [{"from": 0, "to": 25, "count": 17}],
 "compare": {"offset_seconds": 86400, "rows": [], "series": [], "buckets": []} | null,
 "metadata": {"from": "…", "to": "…", "bucket_seconds": 60 | null, "rollup": false, "table": "logs",
              "rows_read": 123456, "bytes_read": 7890123, "elapsed_ms": 35, "queries": 1, "facet_limit": 10,
              "truncated": false, "warnings": ["attribute http.route is read from attributes['http.route']"]}}
```
- `single`: `FACET`/`TIMESERIES` yok, facet'siz tek satır. `facets`: grup başına bir satır (ilk sütuna göre azalan sıralı;
  `truncated` = `LIMIT`'ten fazla grup). `timeseries`: grup × sütun başına bir seri, noktalar
  aralığın her bucket'ı için `[bucket start ms, value]`. `histogram`: yalnızca `buckets`. Kullanılmayan diziler `[]`'dir.
- `400 invalid_argument`: söz dizimi/doğrulama hatası (mesaj `line L, column C: …`), aralık veya boyut limitleri (oql.md §5).
- `422`/`429`/`504`: organizasyon sorgu limitleri ve timeout (diğer telemetri endpoint'leri gibi).

### `POST /api/v1/query/validate` `{"query", "variables"?}` {#post-api-v1-query-validate-query-variables}
ClickHouse'u okumadan ayrıştırır ve planlar (geçersiz sorgular için de `200`):
```json
{"valid": false, "event_type": "Log" | null, "kind": "facets" | null, "variables": ["host"],
 "errors": [{"message": "unknown attribute \"sevrity\" for Log", "offset": 30, "length": 7, "line": 1, "column": 31}],
 "warnings": [{"message": "…", "offset": 0, "length": 0, "line": 1, "column": 1}]}
```

### `GET /api/v1/query/schema?event_type=` {#get-api-v1-query-schema-event-type}
Attribute'larıyla olay türleri (`name`, `type`, `aliases`, `rollup`), `maps` (`attributes`, `resource`) ve
`max_range_seconds`; fonksiyonlar (`name`, `signature`, `description`); anahtar kelimeler. `event_type` ile ayrıca son saatin en sık
map anahtarları (`attribute_keys`, `resource_keys`, her biri ≤ 200) ve `Metric` için `metric_names` (≤ 500).

## Dashboard'lar {#dashboards}

OQL widget'larından özel dashboard'lar (yalnızca `OPENLOG_AUTH_MODE=postgres`; aksi halde `404`). Tablo: [postgres.md](/tr/contracts/postgres#dashboards-0020_dashboards).
Okumalar: her rol ve API anahtarları; `visibility: "private"` dashboard'ları yalnızca oluşturanı görür (oluşturanın hesabı silinince admin'ler de). Yazmalar [Roller](#roles) bölümündeki rolü ister, API anahtarları dahil (oturumlarda her zamanki gibi
CSRF): member'lar dashboard oluşturur, içe aktarır, çoğaltır ve kendilerininkini değiştirir/siler; admin'ler ve owner'lar
herhangi bir `org` dashboard'unu değiştirir/siler (aksi halde `403`). API anahtarıyla oluşturulan dashboard'un oluşturanı yoktur, yani sonradan yalnızca
admin'ler (veya `admin` anahtarı) değiştirebilir (D-133). Her
yazma audit'lenir (`dashboard.{create,update,delete,duplicate,import}`, hedef `dashboard`).

Dashboard:
```json
{"id": "…", "name": "Checkout", "description": "", "visibility": "org" | "private", "version": 3,
 "variables": [{"name": "host", "label": "Host", "type": "query" | "list" | "text",
                "query": "SELECT count(*) FROM Log FACET host.name LIMIT 100", "values": [], "default": [],
                "multi": true, "include_all": true}],
 "pages": [{"id": "…", "name": "Overview", "widgets": [
   {"id": "…", "title": "Errors", "visualization": "line" | "area" | "bar" | "table" | "billboard" | "pie" | "heatmap" | "markdown",
    "layout": {"x": 0, "y": 0, "w": 6, "h": 3}, "query": "SELECT count(*) FROM Log WHERE host.name IN ({{host}}) TIMESERIES",
    "markdown": "", "unit": "" | "number" | "percent" | "bytes" | "bytesPerSec" | "ms" | "s",
    "thresholds": [{"value": 100, "severity": "warning" | "critical"}], "options": {"stacked": false, "legend": true}}]}],
 "created_by_user_id": "…" | null, "created_by_email": "…", "created_at": "…", "updated_at": "…", "can_edit": true}
```
Doğrulama (`400`): ad 1–200 karakter, açıklama ≤ 2000; 1–20 sayfa (boş liste "Page 1" adlı tek sayfa olur),
sayfa adları 1–100; sayfa başına ≤ 100 widget; 12 sütunlu ızgarada düzen (`0 ≤ x`, `1 ≤ w ≤ 12`, `x + w ≤ 12`, `0 ≤ y ≤ 10000`,
`1 ≤ h ≤ 50`); markdown olmayan widget'lar ayrıştırılıp doğrulanan bir sorgu ister (oql.md; değişkenler ayarsız olabilir), markdown
widget'lar ≤ 20 000 karakter; benzersiz adlı ≤ 10 değişken (`[A-Za-z_][A-Za-z0-9_]{0,63}`), `query` değişkenleri
`FACET` içeren bir OQL sorgusu ister (değerler = ilk facet), ≤ 200 `values`; widget başına ≤ 10 eşik. Girdi aynı dashboard'un geçerli bir id'sini taşıyorsa widget ve sayfa id'leri
korunur, aksi halde yeni id'ler atanır.

### `GET /api/v1/dashboards?q=` {#get-api-v1-dashboards-q}
`{"dashboards": [{"id", "name", "description", "visibility", "page_count", "widget_count", "created_by_email",
"updated_at", "can_edit"}]}`, ada göre; `q` ad alt dizesine göre filtreler (büyük/küçük harf duyarsız).

### `POST /api/v1/dashboards` · `GET /api/v1/dashboards/{id}` · `PUT /api/v1/dashboards/{id}` · `DELETE /api/v1/dashboards/{id}` {#post-api-v1-dashboards-·-get-api-v1-dashboards-id-·-put-api-v1-dashboards-id-·-delete-api-v1-dashboards-id}
Girdi: `{"name", "description", "visibility", "variables", "pages", "version"}` (`PUT`'ta `version` zorunlu: okunan
sürüm; dashboard bu arada değiştiyse `409 failed_precondition`). `POST` → `201` Dashboard, `PUT`
→ `200` Dashboard (sürüm + 1, tüm belge değiştirilir), `DELETE` → `204`. Bilinmeyen veya görünmeyen id → `404`.

### `POST /api/v1/dashboards/{id}/widgets` `{"page_id"?, "widget"}` {#post-api-v1-dashboards-id-widgets-page-id-widget}
Sayfanın mevcut widget'larının altına bir widget ekler (varsayılan: ilk sayfa; `layout.y` yeniden hesaplanır) → `200`
Dashboard. `PUT` ile aynı izin.

### `POST /api/v1/dashboards/{id}/duplicate` `{"name"?}` {#post-api-v1-dashboards-id-duplicate-name}
Okunabilir bir dashboard'u çağıranın yeni dashboard'u olarak kopyalar (varsayılan ad `"<name> (copy)"`, aynı görünürlük) → `201`.

### `GET /api/v1/dashboards/{id}/export` · `POST /api/v1/dashboards/import` {#get-api-v1-dashboards-id-export-·-post-api-v1-dashboards-import}
Dışa aktarma: `{"openlog_dashboard": 1, "name", "description", "variables", "pages": [{"name", "widgets": [widget without
id]}]}`. İçe aktarma aynı belgeyi alır (artı isteğe bağlı `"visibility"`), `POST` gibi doğrular ve `201`
Dashboard ile yanıt verir; bilinmeyen `openlog_dashboard` sürümleri → `400`.

### Widget'lar arası filtreler {#cross-widget-filters}
Bir widget'ta facet değerine tıklamak (tablo satırı, çubuk, pasta dilimi, billboard, heatmap satırı veya grafik gösterge kaydı)
`attribute = value` dashboard filtresi ekler (bir attribute'un birden çok değeri ayrı filtrelerdir ve hepsi eşleşmelidir). Web arayüzü
filtreleri URL'de (`filters`) tutar ve her widget sorgusuyla `POST /api/v1/query` olarak gönderir:
`"filters": [{"attribute": "host.name", "value": "web-1", "event_type": "Log"}]` (≤ 10). Her filtre, yalnızca widget'ın olay türünde attribute varsa
plan düzeyinde (bağlı parametre) widget sorgusunun `WHERE` koşuluyla AND'lenir; atlanan
filtreler `metadata.ignored_filters` içinde listelenir ([oql.md](/tr/contracts/oql) §7). Filtreler dashboard ile kaydedilmez ve
paylaşım bağlantılarına veya raporlara uygulanmaz.

### Sürüm geçmişi: `GET /api/v1/dashboards/{id}/versions` · `GET …/versions/{version}` · `POST …/versions/{version}/restore` {#version-history-get-api-v1-dashboards-id-versions-·-get-versions-version-·-post-versions-version-restore}
Her kayıt (oluşturma, `PUT`, widget ekleme, geri yükleme) tüm belgeyi yeni bir sürüm olarak saklar; bir dashboard'un en yeni 50 sürümü
tutulur (D-086). Okumalar: dashboard'u okuyabilen herkes.
Liste: `{"versions": [{"version", "author_user_id" | null, "author_email", "created_at", "restored_from" | null,
"page_count", "widget_count"}], "current_version", "can_restore"}`, en yenisi başta.
Tek sürüm: liste kaydı artı `"document": {"name", "description", "visibility", "variables", "pages"}` (sayfa ve
widget id'leriyle), `"current_version"`, `"previous_version"` (bir önceki saklı sürüm veya `null`), `"changes"` (önceki sürümden buna fark,
yoksa `null`) ve `"differences_from_current"` (güncel belgeden bu sürüme fark, yani geri yüklemenin neyi değiştirdiği). Fark: `{"name", "description", "visibility", "variables": bool,
"pages_added" | "pages_removed" | "pages_renamed": [{"id", "name"}], "widgets_added" | "widgets_removed" |
"widgets_changed": [{"id", "title", "page", "fields"?: ["title" | "visualization" | "layout" | "query" | "markdown" |
"unit" | "thresholds" | "options" | "page"]}]}` (sayfa ve widget'lar id ile eşleştirilir; `page` sayfa adıdır).
Geri yükleme `{"version"}` (okunan güncel sürüm; değiştiyse `409`) saklı belgeyi `restored_from` ile yeni
sürüm olarak kaydeder → `200` Dashboard. `PUT` ile aynı izin; dashboard güncel görünürlüğünü korur; saklı sürümün sayfa
ve widget id'leri korunur; artık doğrulanmayan saklı sorgu → `400`. Bilinmeyen veya budanmış sürüm
→ `404`. Audit: `dashboard.restore`. Eski sürümdeki API pod'larının kayıtları (rolling upgrade) geçmişte yer almaz.

### Paylaşım ayarları: `GET /api/v1/dashboards/settings` · `PUT /api/v1/dashboards/settings` `{"share_links_enabled", "report_domains"?}` {#sharing-settings-get-api-v1-dashboards-settings-·-put-api-v1-dashboards-settings-share-links-enabled-report-domains}
Organizasyon geneli: `{"share_links_enabled": false, "report_domains": [], "updated_at" | null, "can_edit"}`. Paylaşım bağlantıları
varsayılan olarak kapalıdır. `report_domains` (≤ 20 alan adı, küçük harfe çevrilir, baştaki `@` atılır), zamanlanmış raporların
üyeler dışında gönderilebileceği e-posta alan adlarıdır. Okumalar: her rol ve API anahtarları; `PUT`: giriş yapmış admin'ler ve owner'lar
(aksi halde `403`). Paylaşım bağlantılarını kapatmak var olan tüm bağlantıları hemen durdurur (tutulurlar ve yeniden açılınca,
süresi dolmamış veya iptal edilmemişse, tekrar çalışır). Audit: `dashboard.settings.update`.

### Paylaşım bağlantıları: `GET /api/v1/dashboards/{id}/shares` · `POST /api/v1/dashboards/{id}/shares` · `DELETE /api/v1/dashboards/{id}/shares/{share_id}` {#share-links-get-api-v1-dashboards-id-shares-·-post-api-v1-dashboards-id-shares-·-delete-api-v1-dashboards-id-shares-share-id}
Tek bir dashboard'a salt okunur public bağlantılar (D-087). Oluşturma: dashboard'un editörleri (`PUT` ile aynı); listeleme ve iptal: editörler
ve dashboard'u okuyabilen admin/owner'lar. `POST` `{"label"?, "expires_at", "range"}` veya `{"label"?, "expires_at",
"from", "to"}`, isteğe bağlı `"variables": {"name": ["v", …]}`:
- `expires_at` zorunludur, 5 dakika ile 90 gün sonrası arası (bağlantılar uzatılamaz; yenisini oluştur);
- `range`, `<n>m`, `<n>h` veya `<n>d` biçiminde göreli aralıktır (1 dakika – 31 gün, her isteğin zamanında biter); `from`/`to`
  sabit aralıktır (≤ 31 gün);
- `variables` değişken değerlerini kilitler (dashboard değişkenlerinin adları; `"*"` veya değer yok = All); görüntüleyenler değiştiremez;
- dashboard başına ≤ 20 aktif bağlantı; etiket ≤ 100 karakter; paylaşım bağlantıları kapalıysa → `409 failed_precondition`.

→ `201` `{"id", "label", "range" | null, "from" | null, "to" | null, "variables", "created_by_email", "created_at",
"expires_at", "revoked_at" | null, "last_used_at" | null, "use_count", "active", "token", "path"}`. Token (`olds_` + 43
base64url karakter, 256 rastgele bit) yalnızca bu yanıtta döner; yalnızca SHA-256 hash'i saklanır. `path`,
`/shared/dashboards/{token}` web arayüzü sayfasıdır. `GET` `{"shares": [same objects without token and path],
"share_links_enabled"}` döner, en yenisi başta; `DELETE` iptal eder (idempotent) → `200` share. `last_used_at` ve `use_count`
bağlantı başına en fazla dakikada bir güncellenir. Audit: `dashboard.share.create`, `dashboard.share.revoke` ve
bir bağlantı bir saatten uzun süre kullanılmadıktan sonra kullanıldığında `dashboard.share.access` (aktör yok, istemci IP'si).

Bağlantı şu durumlarda çalışmaz olur: süresi dolduğunda veya iptal edildiğinde, organizasyon için paylaşım bağlantıları kapatıldığında, oluşturanı artık
organizasyon üyesi olmadığında veya dashboard silindiğinde.

### Herkese açık paylaşım bağlantısı endpoint'leri (kimlik doğrulaması yok) {#public-share-link-endpoints-no-authentication}
`GET /api/v1/public/dashboards/{token}` → `{"name", "description", "pages": [{"id", "name", "widgets": [{"id", "title",
"visualization", "layout", "markdown", "unit", "thresholds", "options"}]}], "variables": [{"name", "label", "values"}],
"time_range": {"range" | null, "from" | null, "to" | null}, "expires_at"}`. Sorgu metni, sürüm, oluşturan, e-posta
adresleri, dashboard veya organizasyon id'leri ya da adları dönmez.
`GET /api/v1/public/dashboards/{token}/widgets/{widget_id}/result` → widget'ın saklı sorgusunun sonucu (`POST /api/v1/query` şeklinde),
bağlantının zaman aralığı ve kilitli değişkenleri için, sahip organizasyonun tenant kapsamı,
sorgu limitleri ve timeout'u ile çalıştırılır; `metadata.table`, `rows_read` ve `bytes_read` boşaltılır ve `warnings` boştur. İstek
parametre almaz: istemciler başka sorgu çalıştıramaz veya aralığı, değişkenleri ya da filtreleri değiştiremez. Markdown veya bilinmeyen
widget → `404`; artık doğrulanmayan saklı sorgu → `422`.
Bilinmeyen, süresi dolmuş, iptal edilmiş veya kapatılmış bağlantılar → tüm durumlar için tek mesajla `404 not_found`. Rate limit'ler (cluster geneli,
D-096): istemci IP'si başına dakikada 600 istek, bağlantı başına 1200 ve bir IP bir dakika içinde 30 başarısız arama yaptığında o IP'nin her
isteği reddedilir → `Retry-After` ile `429 resource_exhausted`. Pencereler kayar (önceki dakika, geçmemiş payıyla ağırlıklı
+ mevcut dakika) ve tüm API pod'ları arasında PostgreSQL üzerinden paylaşılır (`rate_limit_counters`,
postgres.md): her pod limitten uzak istekleri yerelde karara bağlar ve sayılarını saniyede bir yazar, yazılmamış payı kalan payın 1/8'ine ulaşınca
eşzamanlı yazar (tam uygulama); yani 8 pod'a kadar
limitler aşılmaz, ötesinde en fazla (pods/8 − 1) × kalan pay kadar aşılır. Başka pod'da sayılan başarısız aramalar yaklaşık bir saniye içinde görülür.
PostgreSQL'e ulaşılamıyorsa her pod yeniden denemeden önce 5 saniye kendi başına sayar (limitler o zaman pod başına uygulanır); static auth modu (PostgreSQL yok) her zaman pod başına sayar. Her yanıt
`Cache-Control: no-store`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'; base-uri 'none';
form-action 'none'`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex, nofollow`,
`X-Content-Type-Options: nosniff` ve `Cross-Origin-Resource-Policy: same-origin` taşır; CORS header'ı yoktur. Web arayüzü sayfası
`/shared/dashboards/{token}`, `Referrer-Policy: no-referrer` ve `X-Robots-Tag: noindex, nofollow` ile sunulur ve
dashboard'u navigasyonsuz, salt okunur gösterir.

### Zamanlanmış raporlar: `GET /api/v1/dashboards/{id}/reports` · `POST` · `PUT …/reports/{report_id}` · `DELETE …/reports/{report_id}` {#scheduled-reports-get-api-v1-dashboards-id-reports-·-post-·-put-reports-report-id-·-delete-reports-report-id}
Bir dashboard'un e-posta özetleri (D-087). Oluşturma: dashboard'un editörleri; listeleme, değiştirme ve silme: editörler ve
dashboard'u okuyabilen admin/owner'lar. Girdi: `{"name"?, "frequency": "daily" | "weekly", "weekday"?: 0–6 (0 = Sunday;
weekly), "hour": 0–23, "minute"?: 0–59, "timezone"?: IANA time zone (default `"UTC"`), "recipients": ["…"] (1–20),
"language"?: "en" | "tr", "range"?: relative range (default `"24h"` daily, `"7d"` weekly), "variables"?: {…},
"enabled"?: true}`. Alıcılar organizasyon üyesi olmalı veya `report_domains` içinde olmalıdır; `private`
dashboard'un raporları yalnızca oluşturanına gider (aksi halde `400`). Dashboard başına ≤ 10 rapor. → `201`/`200` `{"id", "name", "frequency",
"weekday", "hour", "minute", "timezone", "recipients", "language", "range", "variables", "enabled", "created_by_email",
"created_at", "updated_at", "next_run_at" | null, "last_run": {"period", "status": "running" | "sent" | "partial" |
"failed" | "skipped", "error", "recipients", "started_at", "finished_at" | null} | null}`; `DELETE` → `204`. Audit:
`dashboard.report.{create,update,delete}`.

Gönderim (API leader'ı, `dashboard-reports` job'u, dakikada bir kontrol edilir): raporun en son zamanlanmış yerel saati son 6 saat içinde ve rapor oluşturulduktan
veya son değiştirildikten sonra geçtiyse rapor zamanı gelmiştir. Dönem (yerel tarih), herhangi bir şey gönderilmeden önce
`dashboard_report_runs` içinde alınır, yani bir dönem leader değişimlerinde bile en fazla bir kez gönderilir (gönderim sırasındaki crash
yeniden denenmez). Bir üyeyi çıkarmak adresini alıcı listelerinden hemen kaldırır (D-096). Alıcılar gönderimden önce yeniden kontrol edilir (ayrılan üyeler ve kaldırılan alan adları
atlanır). En fazla 50 widget'ın saklı sorguları (markdown widget'lar atlanır) sunucu tarafında
`[scheduled time − range, scheduled time)` için organizasyonun tenant kapsamı, sorgu limitleri ve raporun değişkenleriyle çalışır.
E-posta (konu, metin ve HTML raporun dilinde) tek değerleri, facet tablolarını ve zaman serisi başına ortalama/min/maks/son değeri
basit HTML tabloları olarak (widget başına ≤ 20 satır) o dönemin dashboard bağlantısıyla gösterir
(`OPENLOG_PUBLIC_URL`). `OPENLOG_SMTP_HOST` yoksa çalışmalar `failed` olarak kaydedilir.
PNG grafik görselleri (D-097, [operations/reports.md](/tr/operations/reports)): `OPENLOG_RENDERER_URL` ile leader rapor dönemi için bir render token'ı imzalar
ve `openlog-renderer`'dan (ayrı image, headless Chromium) web uygulamasının `/print/dashboard` yazdırma görünümünün görsellerini ister. Görsel alan widget'lar tabloları yerine satır içi `image/png` parçaları olarak gösterilir
(`multipart/related`, `Content-ID: <widget-N@openlog>`, her biri ≤ 1 MiB, e-posta başına ≤ 10 MiB);
düz metin parçası her zaman tabloları taşır. Renderer hatası veya timeout (`OPENLOG_RENDERER_TIMEOUT`), sorgusu
başarısız olan widget veya sınırları aşan görsel tabloya döner; çalışma yine `sent` olur.

### Rapor yazdırma görünümü: `GET /api/v1/render/dashboard` · `GET /api/v1/render/dashboard/widgets/{widget_id}/result` {#report-print-view-get-api-v1-render-dashboard-·-get-api-v1-render-dashboard-widgets-widget-id-result}
`openlog-renderer` için iç endpoint'ler; yalnızca `OPENLOG_RENDERER_URL` ile vardır. Kimlik doğrulama:
yalnızca `Authorization: Bearer olrt_…` render token'ı (oturumlar, API anahtarları ve paylaşım token'ları reddedilir; render token'ını da
diğer tüm endpoint'ler reddeder). Token `olrt_` + base64url(JSON claims) + `.` + base64url(HMAC-SHA256) biçimindedir,
`OPENLOG_KEY_HASH_SECRET`'tan (yoksa `OPENLOG_SECRETS_KEY`) türetilen anahtarla imzalanır, ≤ 10 dakika geçerlidir (job 5 kullanır),
tek organizasyona ve tenant'ına, tek dashboard'a, tek etkin zamanlanmış rapora ve rapor dönemine bağlıdır. Yanıtlar
paylaşım bağlantısı endpoint'lerinin yanıtlarıdır (`time_range.from`/`to` = rapor dönemi, `expires_at` = token süresi olan `SharedDashboard`;
sansürlenmiş OQL sonucu), aynı header'larla. Geçersiz, süresi dolmuş veya sahte token → `401 unauthenticated`; silinmiş
dashboard, silinmiş veya devre dışı rapor → `404`.

## Kullanım ve planlar {#usage-and-plans}

Kullanım ölçümü, plan limitleri, kota durumu ve faturalama ([usage.md](/tr/contracts/usage), D-079–D-081). Okumalar: organizasyonun her rolü
ve API anahtarları. Dışa aktarma: admin, owner. Plan atama: yalnızca superadmin'ler (`OPENLOG_SUPERADMIN_EMAILS`, doğrulanmış
e-posta adresli oturum kullanıcısı; organizasyon üyeliği gerekmez). `period`, `current` (varsayılan),
`previous` veya `YYYY-MM`'dir (UTC takvim ayı); başka her şey → `400`. Static auth modunda varsayılan plan geçerlidir ve
admin ile webhook endpoint'leri yoktur.

### `GET /api/v1/usage?period=` {#get-api-v1-usage-period}
`{"organization": {"id","name","tenant_id"}, "period": {"id","start","end","data_until"}, "saas_mode", "plan":
{"id","name","description","limits","enforcement"}, "plan_assigned", "usage": {"signals": [{"signal","items","bytes",
"ingest_bytes","ingest_requests"}], "ingest_bytes", "hosts", "containers", "services", "active_hosts", "query":
{"queries","failed","read_rows","read_bytes","cpu_seconds","memory_bytes"}}, "stored": [{"signal","retention_days",
"bytes","compressed_bytes"}], "limits": [{"metric","used","limit","percent","level"}], "level", "ingest_blocked",
"projection": {"ingest_bytes","ingest_percent"?}, "can_manage_plan", "billing_enabled"}`. `plan` geçerli plandır
(override'lar uygulanmış); `limits` istek anında değerlendirilir (`metric` `ingest_bytes`, `hosts`, `users`; `limit` 0 =
sınırsız; `level` `ok`, `warning`, `exceeded`). `stored`, her sinyalin geçerli saklama süresini şu ana kadar kapsar.

### `GET /api/v1/usage/daily?period=` {#get-api-v1-usage-daily-period}
`{"period", "days": [{"day": "YYYY-MM-DD", "signals": […], "ingest_bytes", "hosts", "containers", "services", "query"}]}`: kullanımı olan günler, artan sırada.

### `GET /api/v1/usage/top?period=&by=service|host&limit=` {#get-api-v1-usage-top-period-by-service-host-limit}
`{"period", "by", "entries": [{"key", "items", "bytes", "bytes_by_signal": {"traces","logs","metrics"}}]}`, saklanan byte'a göre
sıralı; `limit` 1–100 (varsayılan 10).

### `GET /api/v1/usage/export?period=&format=csv|json` {#get-api-v1-usage-export-period-format-csv-json}
Admin, owner. Ek dosya `openlog-usage-<tenant>-<period>.<format>`. CSV (`text/csv`): `tenant_id,period,day,metric,value`
uzun biçim (usage.md §9); JSON: `{"organization", "plan_id", "period", "totals", "days"}`.

### `GET /api/v1/usage/status` {#get-api-v1-usage-status}
Saklanan son değerlendirme (dakikada bir, API leader'ı): `{"level", "ingest_blocked", "metrics": [QuotaMetric], "saas_mode",
"plan_id"?, "evaluated_at"?, "period_start"?}`; ilk değerlendirmeden önce `{"level": "ok", "metrics": []}`. Arayüz
banner'ı kullanır.

### `GET /api/v1/plans` {#get-api-v1-plans}
`{"plans": [Plan], "default"}`: yapılandırılmış katalog. Her plan `trial_days` (`0` = deneme yok) ve
`trial_fallback_plan` (denemesi bitince atanan plan; denemesiz planlarda `""`) taşır.

### `GET /api/v1/usage/query-limits` · `PUT /api/v1/usage/query-limits` · `DELETE /api/v1/usage/query-limits` {#get-api-v1-usage-query-limits-·-put-api-v1-usage-query-limits-·-delete-api-v1-usage-query-limits}
Organizasyonun ClickHouse sorgu limitleri ve her birinin nereden geldiği ([usage.md](/tr/contracts/usage) §4.5; postgres auth modu).
Okuma: her rol ve API anahtarları. `PUT`/`DELETE`: superadmin'ler; owner'lar (oturum) yalnızca `OPENLOG_SAAS_MODE` kapalıyken,
aksi halde `403`. Yanıt `{"defaults", "plan", "organization": {"max_memory_usage","max_rows_to_read","max_bytes_to_read",
"updated_at","updated_by"} | null, "environment": {…} | null, "effective", "sources": {"max_memory_usage": "default"|"plan"|
"organization"|"environment", …}, "can_manage", "refresh_seconds"}`; değer nesneleri üç ayarı taşır (byte, satır,
byte; `0` = openlog ayarlamadı). `PUT` gövdesi `{"max_memory_usage"?, "max_rows_to_read"?, "max_bytes_to_read"?}`
organizasyon ayarını değiştirir: sayı (`0`–`2^62`) değeri ayarlar, `null`/yok devralır; negatif → `400`; hepsi yok =
`DELETE`. İkisi de yeni durumla yanıt verir; diğer pod'lar `refresh_seconds` içinde uygular. Audit: `query_limits.update` /
`query_limits.delete`.

### `GET /api/v1/admin/orgs/{org}/plan` · `PUT /api/v1/admin/orgs/{org}/plan` {#get-api-v1-admin-orgs-org-plan-·-put-api-v1-admin-orgs-org-plan}
Superadmin. `{org}`, organizasyon id'si veya tenant id'sidir (bilinmiyorsa `404`). Yanıt `{"organization", "plan_id",
"assigned", "overrides", "billing": {"provider","customer_id","subscription_id"}, "note", "updated_at", "updated_by",
"effective": Plan}`. `PUT` gövdesi `{"plan_id", "overrides"?, "billing"?, "note"?}`: bilinmeyen `plan_id`, geçersiz override'lar veya
en uzun plan saklamasından uzun `retention_days` override'ı → `400`. Verilmeyen `overrides`/`billing`/`note`
saklanan değerleri korur. Audit: `plan.update`.

### `POST /api/v1/billing/webhooks/{provider}` {#post-api-v1-billing-webhooks-provider}
Herkese açık; imzayı yapılandırılmış sağlayıcı (`OPENLOG_BILLING_PROVIDER`) doğrular. Başka sağlayıcı veya sağlayıcı yok → `404`;
geçersiz imza veya eşlenmemiş plan referansı → `400`. `200 {"received": true, "changed"}`.

### Ingest: tenant kotası (`429`) {#ingest-tenant-quota-429}
SaaS modunda OTLP ingest, organizasyonun aylık ingest kotası bittiğinde veya ingest rate limit'i aşıldığında
`google.rpc.Status` mesajıyla `429` + `Retry-After` (gRPC `RESOURCE_EXHAUSTED` + `RetryInfo`) döner
(usage.md §4.4).

### Ingest: askıya alınmış organizasyon (`403`) ve host limiti (`429`) {#ingest-suspended-organization-403-and-host-limit-429}
SaaS modunda (D-105) `google.rpc.Status` gövdesi bir `google.rpc.ErrorInfo` ayrıntısı taşır (`domain` `openlog`):

- Askıya alınmış organizasyon: HTTP `403` / gRPC `PERMISSION_DENIED`, reason `org_suspended`, mesaj
  `organization suspended: ingest is disabled …`. Tasarım gereği yeniden denenmez (`Retry-After` yok).
- Silinmesi zamanlanmış veya silinmiş bir organizasyonun lisans anahtarı (yalnızca SaaS değil, tüm modlar; D-115): HTTP `403` / gRPC
  `PERMISSION_DENIED`, reason `org_deleted`, mesaj `organization deleted: this license key belonged to an organization
  that is scheduled for deletion or deleted; ingest is disabled`. Bunu yalnızca silme işleminin kendisinin iptal ettiği anahtarlar alır
  (`license_key_tombstones`), bekleme süresi boyunca ve temizlikten sonra 365 gün; kullanıcının iptal ettiği anahtarlar, bilinmeyen ve
  eksik anahtarlar `401` / `UNAUTHENTICATED` kalır. Bilinmeyen anahtar gibi cache'lenir: iptalden sonra anahtar
  negatif cache TTL'i (10 sn) içinde yeniden çalışır. D-115'ten eski pod'lar `401` döner.
- Plan host limiti (`limits.hosts`): tüm resource'ları limit ötesindeki `host.id` değerlerini taşıyan istek HTTP
  `429` + `Retry-After: 300` / gRPC `RESOURCE_EXHAUSTED` + `RetryInfo`, reason `quota_exceeded`, mesaj
  `quota_exceeded: host limit of N hosts …` alır. İstek kabul edilen host'lar da taşıyorsa yalnızca
  reddedilen host'ların resource'ları düşürülür ve yanıt OTLP partial success ile `200` olur (`rejected_*` sayıları ve aynı
  mesaj). Bugün veya dün aktif olan host'lar her zaman bildirmeye devam eder; `host.id` içermeyen resource'lar sınırlanmaz.

## SaaS operasyonları {#saas-operations}

Operatör konsolu, organizasyon yaşam döngüsü ve destek erişimi (D-105, D-106; operatör kılavuzu
[saas.md](/tr/operations/saas) §8–§11). Operatörler superadmin'lerdir (`OPENLOG_SUPERADMIN_EMAILS`, doğrulanmış
adresli oturum kullanıcıları); diğer her çağıran `GET /operator/me` dışında `/api/v1/operator/*` üzerinde `403 permission_denied` alır.
Her operatör eylemi bir `reason` (3–1000 karakter) ister ve aktör olarak operatörle **organizasyonun** audit log'una bir audit olayı
yazar. `{org}`, organizasyon id'si veya tenant id'sidir. `OPENLOG_AUTH_MODE=postgres` ile kullanılabilir;
zorunlu kılma (askıya alma, limitler, denemeler, işaretler) ayrıca `OPENLOG_SAAS_MODE=true` ister.

### `GET /api/v1/operator/me` {#get-api-v1-operator-me}
Her oturum. `{"operator": bool, "saas_mode": bool}` (arayüz konsolu yalnızca operatörlere gösterir).

### `GET /api/v1/operator/orgs?q=&plan=&state=&sort=&dir=&limit=&offset=` {#get-api-v1-operator-orgs-q-plan-state-sort-dir-limit-offset}
`q`: ad, tenant id, organizasyon id veya üye e-postası alt dizesi. `plan`: geçerli plan id. `state`: `active`,
`suspended`, `trial`, `flagged`. `sort`: `created` (varsayılan, en yenisi başta), `name`, `ingest`, `members`, `last_ingest`. `dir`: `asc` veya `desc`, yoksa sütunun kendi yönü.
`limit` 1–200 (50). Yanıt `{"organizations": [OperatorOrg], "total", "saas_mode"}`; `id`, `tenant_id`, `name`,
`created_at`, `plan_id`, `plan_assigned`, `state`, `suspended_at`, `suspend_reason`, `trial_plan_id`, `trial_ends_at`,
`support_access_until`, `members`, `active_hosts` ve `ingest_bytes` / `quota_level` (mevcut dönemin son kota değerlendirmesi),
`last_ingest_at` (son lisans anahtarı kullanımı), `open_flags` ile.

### `GET /api/v1/operator/orgs/{org}` {#get-api-v1-operator-orgs-org}
`{"organization": OperatorOrg + {"member_list": [{user_id,email,name,role,joined_at,last_login_at,email_verified,disabled}] (≤ 500),
"pending_invitations", "keys": {license_keys_active, license_keys_revoked, api_keys_active, scim_tokens_active,
last_ingest_at}, "sso_connections": [{id,protocol,name,enabled,enforce,jit_enabled,last_test_ok}], "verified_domains",
"flags", "support_sessions", "support_access_granted_by"}, "plan": OrgPlan, "quota": UsageStatus|null, "lifecycle",
"audit": [50 newest events, without IP addresses], "usage": {"period", "days", "available"}, "saas_mode"}`. Asla sır,
anahtar değeri, SSO yapılandırması veya IdP metadata'sı içermez. ClickHouse okunamıyorsa `usage.available=false`.

### Eylemler {#actions}
Hepsi `{"reason"}` ile `POST` (deneme: aşağıya bak):

| Yol | Etkisi | Audit |
|---|---|---|
| `/operator/orgs/{org}/suspend` | Askıya alır (idempotent). Ingest `403 org_suspended`; üyelerin değişiklik yapan API istekleri, giriş/çıkış, profil, oturumlar, destek erişimi ve veri dışa aktarma/silme dışında `403 org_suspended`; sorgular çalışmaya devam eder | `org.suspend` |
| `/operator/orgs/{org}/unsuspend` | Askıyı kaldırır; askıda değilse `409` | `org.unsuspend` |
| `/operator/orgs/{org}/trial` | Gövde `{"plan_id"?, "days"? (1–365) \| "ends_at"?, "reason"}`. Devam eden deneme yoksa: `plan_id` atar ve denemesini başlatır (`days`/`ends_at`, varsayılan planın `trial_days` değeri). Varsa: bitişini taşır (`days` mevcut bitişe eklenir) | `plan.update`, `trial.start` / `trial.extend` |
| `/operator/orgs/{org}/reset-quota-notifications` | Eşik e-postaları yeniden gönderilebilsin diye mevcut dönemin `usage_notifications` kayıtlarını siler; `{"deleted","period"}` | `quota.notifications_reset` |
| `/operator/orgs/{org}/force-logout` | Üyelerin her aktif oturumunu (tüm organizasyonlarında) iptal eder, operatörünkini değil; `{"sessions_revoked"}` | `org.force_logout` |
| `/operator/orgs/{org}/resend-verification` | Doğrulanmamış her owner'a yeni doğrulama e-postası; yoksa `409`; `{"sent_to"}` | `org.owner_verification_resend` |

### Destek oturumları {#support-sessions}
- `POST /api/v1/operator/orgs/{org}/support-sessions` `{"reason"}` → `201 SupportSession` `{id, org_id, org_name,
  tenant_id, operator_email, reason, started_at, expires_at, ended_at}`. Bir owner'ın verdiği destek erişimi gerekir
  (`403 support_access_required`); `OPENLOG_SAAS_SUPPORT_SESSION_TTL` kadar sürer, izni asla aşmaz. Audit:
  `support.session_start`.
- Açıkken operatörün arayüzü API isteklerinde `X-Openlog-Support-Session: <id>` gönderir: istek o organizasyonun
  **viewer**'ı olarak çalışır. Yalnızca operatörün kendi oturumu için (id, kullanıcıya ve oturuma bağlıdır);
  sorgu/önizleme dışındaki değişiklik yapan istekler → `403 support_read_only`; bitmiş/süresi dolmuş oturum veya iptal edilmiş erişim
  → `403 support_session_ended`. Her farklı istek `support.request` olarak audit'lenir (`method`, `path`; API pod'u başına yol başına en fazla
  dakikada bir). `/api/v1/operator/*` header'ı yok sayar.
- `POST /api/v1/operator/support-sessions/{id}/views` `{"path"}` → `204`, audit `support.page_view` (arayüz gezintisi).
- `GET /api/v1/operator/support-sessions` → operatörün açık oturumları; `DELETE …/{id}` birini bitirir (`support.session_end`).

### Kötüye kullanım işaretleri {#abuse-flags}
`GET /api/v1/operator/flags?status=open|dismissed|actioned|all&limit=` → `{"flags": [{id, org_id, org_name, tenant_id,
kind (ingest_spike|new_org_hosts|ingest_source_ips), status, details, occurrences, first_seen_at, last_seen_at,
auto_suspended, resolved_by_email, resolved_at, resolution_note, org_suspended}]}`.
`POST /api/v1/operator/flags/{id}/resolve` `{"status": "dismissed"|"actioned", "note"}` → işaret; açık değilse `404`.
Audit: `abuse_flag.resolve`.

### Organizasyon tarafı {#organization-side}
- `GET /api/v1/orgs/current/saas` (her üye, oturum) → `{"saas_mode", "suspended", "trial": {plan_id, plan_name,
  ends_at, fallback_plan_id}|null, "support_access": {until, granted_at}|null, "support_session": {id, operator_email,
  expires_at, org_name}|null (only inside a support view), "can_manage_support_access"}`.
- `PUT /api/v1/orgs/current/support-access` `{"duration": "24h"|"7d"}` (owner'lar, kendi oturumu, destek görünümünde değil) →
  yukarıdaki durum; audit `support_access.grant`. `DELETE` onu iptal eder ve açık destek oturumlarını bitirir
  (`support_access.revoke`).

### Plan kullanıcı limiti {#plan-users-limit}
SaaS modunda geçerli `limits.users` değerini aşan davet oluşturma (üyeler + bekleyen davetler), davet kabul etme, SSO just-in-time provisioning ve
SCIM provisioning başarısız olur: API plan, limit ve sayıyla `403 quota_exceeded` döner;
SSO girişi `?sso_error=user_limit` ile yönlendirir; SCIM `403` döner.

## Veri dışa aktarma ve silme {#data-export-and-deletion}

Veri sahibi talepleri (KVKK/GDPR; D-107, [saas.md](/tr/operations/saas) §12). Postgres auth modu; indirme bağlantısı dışında her endpoint
giriş yapmış kullanıcı ister (API anahtarları `403` ile reddedilir).

Yıkıcı işlemler yeniden kimlik doğrular: `password` kullanıcının mevcut parolası olmalıdır (15 dakikada 5 yanlış deneme,
sonra `429`); parolası olmayan kullanıcılar (tek oturum açma) son 10 dakika içinde oluşturulmuş bir oturum kullanmalıdır,
aksi halde `403` ("sign in again with single sign-on, then confirm within 10 minutes").

### `GET /api/v1/account/privacy` {#get-api-v1-account-privacy}
`{"has_password", "data_export_enabled", "org_deletion_grace_seconds", "reauth_max_age_seconds", "org_deletions": [OrgDeletion]}`: `org_deletions`, çağıranın owner olduğu organizasyonların zamanlanmış veya çalışan silmeleridir (bu
organizasyonlar artık seçilemez, yani owner'lar onları burada görür ve iptal eder).

### `GET /api/v1/account/data-exports` · `POST /api/v1/account/data-exports` {#get-api-v1-account-data-exports-·-post-api-v1-account-data-exports}
Çağıranın kişisel dışa aktarımları (en yeni 20) / yenisini isteme (`202 {"export": DataExport}`; biri bekliyor veya çalışıyorsa
`409 failed_precondition`, 24 saatte 5'ten sonra `429`).

### `POST /api/v1/account/delete` `{"confirm_email", "password"?}` {#post-api-v1-account-delete-confirm-email-password}
Çağıranın hesabını siler (`204`, oturum cookie'si temizlenir). `confirm_email` çağıranın adresi değilse `400`;
çağıran bir organizasyonun tek owner'ıysa `409 failed_precondition` (mesaj onları adıyla söyler). Eski adrese onay
e-postası; takma adla kurulum düzeyinde audit `user.delete`.

### `GET /api/v1/data-exports` · `POST /api/v1/data-exports` `{"from"?, "to"?, "signals"?: ["logs", "traces", "metrics"]}` (owner) {#get-api-v1-data-exports-·-post-api-v1-data-exports-from-to-signals-logs-traces-metrics-owner}
Organizasyon dışa aktarımları (en yeni 50) / yenisini isteme: `signals` olmadan yalnızca PostgreSQL verisi; sinyallerle `from`/`to`
zorunludur ve aralık `OPENLOG_DATA_EXPORT_MAX_RANGE` değerini aşmamalıdır (`400`). `202 {"export": DataExport}`, kişisel dışa aktarımlarla aynı
`409`/`429`. Audit: `data_export.request`.

`DataExport`: `id`, `kind` (`organization`/`user`), `status` (`pending`, `running`, `completed`, `failed`, `expired`),
`signals`, `from`, `to`, `requested_by`, `size_bytes`, `telemetry_rows`, `truncated`, `error`, `created_at`,
`started_at`, `completed_at`, `expires_at`, `download_available`.

### `GET /api/v1/data-exports/{id}` · `GET /api/v1/data-exports/{id}/download` {#get-api-v1-data-exports-id-·-get-api-v1-data-exports-id-download}
Tek dışa aktarım / ZIP arşivi (`application/zip`, `Content-Disposition: attachment`). Dışa aktarımın organizasyonunun owner'larına
(mevcut organizasyon) ve kişisel dışa aktarımın sahibine görünür; başka her şey `404`'tür. Audit: organizasyon dışa aktarımları için
`data_export.download`.

### `GET /api/v1/data-exports/download?token=` (herkese açık) {#get-api-v1-data-exports-download-token-public}
E-postayla gönderilen bağlantı üzerinden arşiv, `expires_at` zamanına kadar (bilinmeyen, süresi dolmuş veya silinmiş arşivler için `404`). Yanıtlar
`Cache-Control: no-store` ve `Referrer-Policy: no-referrer` taşır.

### `POST /api/v1/orgs/current/deletion` `{"confirm_name", "password"?}` (owner) {#post-api-v1-orgs-current-deletion-confirm-name-password-owner}
Mevcut organizasyonun silinmesini zamanlar (`202 {"deletion": OrgDeletion}`): `confirm_name` organizasyon adına eşit olmalıdır (`400`),
zaten zamanlanmışsa `409 already_exists`. Üyeler erişimi hemen kaybeder, lisans ve
SCIM anahtarları iptal edilir; kesin silme `OPENLOG_ORG_DELETION_GRACE` sonrasında. Audit: `org.deletion_schedule`; owner'lara
e-posta gider.

`OrgDeletion`: `id`, `organization_id` (silinince null), `organization_name`, `tenant_id`, `status` (`scheduled`,
`cancelled`, `deleting`, `completed`), `initiator` (`owner`/`operator`), `requested_at`, `purge_after`, `cancelled_at`,
`started_at`, `completed_at`, `cancellable`, `certificate_id`; operatör görünümleri `reason`, `requested_by_email`,
`last_error` ekler.

### `POST /api/v1/org-deletions/{id}/cancel` (owner) {#post-api-v1-org-deletions-id-cancel-owner}
Çağıranın sahip olduğu organizasyonun `scheduled` silmesini iptal eder (`200 {"deletion"}`); diğer id'ler için `404`, bir
operatör zamanladıysa `403`, `deleting` olduktan sonra `409`. Zamanlamanın iptal ettiği anahtarlar yeniden çalışır. Audit:
`org.deletion_cancel`.

### `POST /api/v1/admin/orgs/{org}/deletion` `{"reason", "immediate"?: false}` (superadmin) {#post-api-v1-admin-orgs-org-deletion-reason-immediate-false-superadmin}
`{org}`, organizasyon id'si veya tenant id'sidir. Nedenle (1–1000 karakter) silme zamanlar; `immediate`,
`purge_after` değerini şimdiye ayarlar. Owner'lar bilgilendirilir ve iptal edemez. Kurulum düzeyinde audit: `admin.org_deletion_schedule`.

### `GET /api/v1/admin/org-deletions?limit=` · `POST /api/v1/admin/org-deletions/{id}/cancel` (superadmin) {#get-api-v1-admin-org-deletions-limit-·-post-api-v1-admin-org-deletions-id-cancel-superadmin}
Tüm silmeler, en yenisi başta (varsayılan 100, en fazla 500) / zamanlanmış birini iptal etme (audit `admin.org_deletion_cancel`).

### `GET /api/v1/admin/deletion-certificates?subject_hash=&limit=` (superadmin) {#get-api-v1-admin-deletion-certificates-subject-hash-limit-superadmin}
`{"certificates": [{"id", "subject_type", "subject_hash", "initiator", "requested_at", "grace_ended_at", "started_at",
"completed_at", "postgres_rows", "clickhouse_rows", "verified"}]}`; `subject_hash` = tenant id veya user id'nin hex sha256'sı.

## Durum sayfası {#status-page}

Herkese açık durum sayfası (D-108, [saas.md](/tr/operations/saas) §13); yalnızca `OPENLOG_STATUS_PAGE_ENABLED` ile (aksi halde `404`).

### `GET /api/v1/status` (herkese açık) {#get-api-v1-status-public}
`Cache-Control: public, max-age=30`, `Access-Control-Allow-Origin: *`. PostgreSQL okunamıyorsa `503 unavailable`.

```json
{"status": "operational", "checked_at": "…",
 "components": [{"id": "ingest", "status": "operational", "uptime_90d": 99.98,
                 "days": [{"date": "2026-06-17", "status": "no_data", "uptime": null}, …]}],
 "incidents": [StatusIncident], "maintenance": [StatusIncident], "history": [StatusIncident]}
```

Durumlar: `operational`, `degraded`, `partial_outage`, `major_outage`, `maintenance`, `unknown` (5 dakika içinde öz kontrol yok).
Bileşenler `ingest`, `query_api`, `alerting`, `processing`; `days` 90 kayıt içerir (en eskisi başta, durum
`operational`/`degraded`/`outage`/`no_data`). `incidents`: açık incident'lar; `maintenance`: zamanlanmış veya çalışan pencereler;
`history`: 14 gün içinde çözülenler/tamamlananlar.

`StatusIncident`: `id`, `kind` (`incident`/`maintenance`), `title`, `status` (incident: `investigating`, `identified`,
`monitoring`, `resolved`; maintenance: `scheduled`, `in_progress`, `completed`), `impact` (`none`, `minor`, `major`,
`critical`), `components`, `starts_at`, `ends_at`, `created_at`, `updated_at`, `updates` (en yenisi başta: `id`, `status`,
`message`, `created_at`).

### `GET /api/v1/admin/status/incidents?limit=` · `POST /api/v1/admin/status/incidents` (superadmin) {#get-api-v1-admin-status-incidents-limit-·-post-api-v1-admin-status-incidents-superadmin}
Liste (`{"incidents", "components"}`) / oluşturma `{"kind", "title", "status", "impact"?, "components"?, "starts_at"?,
"ends_at"?, "message"?}` (`201 {"incident"}`; geçersiz kind/status birleşimi, bilinmeyen bileşen, boş
başlık veya `starts_at` öncesi `ends_at` için `400`). Audit: `status.incident_create` (kurulum düzeyi).

### `PATCH /api/v1/admin/status/incidents/{id}` · `POST …/{id}/updates` `{"status", "message"}` · `DELETE …/{id}` (superadmin) {#patch-api-v1-admin-status-incidents-id-·-post-id-updates-status-message-·-delete-id-superadmin}
Başlık, durum, etki, bileşenler, `starts_at`, `ends_at` düzenleme (`""` temizler; `kind` değişmez) / zaman çizelgesi kaydı
ekleyip durumuna geçme (`resolved`/`completed`, `ends_at` ayarlar) / silme (`204`). Audit: `status.incident_update`,
`status.incident_delete`.

## Uç durumlar (makinenin okuyabileceği spec: [openapi.yaml](https://github.com/onuragtas/openlog/blob/master/docs/contracts/openapi.yaml)) {#edge-cases-machine-readable-spec-openapi-yaml}

- Tamamlanmış snapshot'ı olmayan envanter: `snapshot_id: ""`, `snapshot_time: null`, boş `items`.
- Bilinmeyen host (çağıranın organizasyonunda host kaydı yok, başka organizasyonun host'u dahil): `GET /hosts/{host_id}`, `/inventory`, `/services` ve `/metrics` üzerinde `404 not_found`. Bilinen host'ta bilinmeyen **metrik**, boş `series` ve `metric.type`/`unit` = `""` ile `200`'dür.
- `/metrics/names?host_id=` ve `/logs?host_id=` host araması değil filtredir: bilinmeyen host boş liste verir (`200`).
- Allowlist dışındaki `attr.*` log filtreleri → `400` (bkz. Loglar).
- Host, hosts tablosunda yoksa envanter araması `host_name` alanını atlar.
- Hatalı biçimli `trace_id` (32 hex karakter değil) → `400 invalid_argument`.
- Bilinmeyen `/api/*` yolları → kimlik doğrulamadan önce `404`.
- `severity_min`, OTel severity adlarını (`TRACE`…`FATAL`, artı `WARN` takma adı olarak `WARNING`) veya sayı kabul eder.
- `/metrics/names` `limit` almaz; `OPENLOG_API_MAX_ROWS` ile sınırlıdır.
- 6 saatten uzun aralıklarda `step` tam dakikalara yukarı yuvarlanır (rollup çözünürlüğü).
- `/api` dışındaki yollar `OPENLOG_API_UI_ENABLED=true` iken gömülü arayüzü sunar.
- Her yönetim yanıtı `Cache-Control: no-store` taşır.
