# Single sign-on ve SCIM provisioning {#single-sign-on-and-scim-provisioning}

openlog organizasyonları kimlik sağlayıcıları (IdP) üzerinden **OpenID Connect** veya **SAML 2.0** ile giriş yapabilir, e-posta alan adlarını
sahiplenebilir, IdP gruplarını rollere eşleyebilir, single sign-on'u **zorunlu kılabilir** ve IdP'nin üyeleri **SCIM 2.0** ile **provision** etmesini
sağlayabilir. Kontratlar: [api.md](/contracts/api) "Single sign-on" ve "SCIM", [postgres.md](/contracts/postgres) "Single sign-on and SCIM",
[config.md](/contracts/config); kararlar D-077 (SSO), D-078 (SCIM), D-088 (birden fazla bağlantı, single logout, şifreli assertion'lar, metadata yenileme)
ve D-089 (sahiplenilen alan adları, SCIM e-posta değişiklikleri).

Her şey organizasyon başına **Settings → Single sign-on** altında ayarlanır (yöneticiler; zorunlu kılma: owner'lar).

## 1. Sunucu ön koşulları {#_1-server-prerequisites}

| Ayar | Neden |
|---|---|
| `OPENLOG_PUBLIC_URL=https://openlog.example.com` | Redirect URI, SAML entity ID/ACS URL ve SCIM base URL bundan türetilir. Bu olmadan sayfa "Single sign-on needs OPENLOG_PUBLIC_URL" gösterir. |
| `OPENLOG_SSO_SECRET_KEY` (≥ 32 byte, `openssl rand -base64 48`) | OIDC client secret'larını ve SAML SP private key'lerini şifreler (AES-256-GCM). Yoksa `OPENLOG_KEY_HASH_SECRET` değerinden türetilen bir anahtara döner; ikisi de yoksa şifresiz saklanırlar (uyarı). Her api pod'unda aynı değeri ayarla. |
| `OPENLOG_COOKIE_SECURE=true` (varsayılan) | Oturum ve giriş bağlama cookie'leri production'da HTTPS ister. |
| `OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS` | Ayarlanmamışsa `OPENLOG_SIGNUP_ENABLED=true` olmadıkça izinlidir. False iken IdP URL'leri `https` olmalı ve public adreslere çözülmelidir (SaaS için SSRF koruması). İç ağdaki kendi IdP'lerin (kümedeki Keycloak) için `true` gerekir. |
| `OPENLOG_SMTP_*` | İsteğe bağlı: alan adlarını DNS yerine e-postayla doğrula. |

Api'nin IdP'ye `OPENLOG_SSO_HTTP_TIMEOUT` (10 sn) içinde ulaşması gerekir (OIDC discovery, JWKS, token endpoint; SAML metadata URL). Tarayıcılar IdP'ye doğrudan
ulaşır; IdP, SCIM dışında openlog'u asla çağırmaz (single logout tarayıcı üzerinden çalışır, bkz. bölüm 5).

**Anahtar rotasyonu.** Yeni değeri `OPENLOG_SSO_SECRET_KEY`, eskisini `OPENLOG_SSO_SECRET_KEY_PREVIOUS` olarak tüm pod'larda ayarla, sonra her bağlantıyı yeniden
kaydet (Settings → Single sign-on → Save; OIDC için client secret'ı yeniden gir) ve `_PREVIOUS`'u kaldır.

## 2. E-posta alan adını doğrula {#_2-verify-your-e-mail-domain}

Yalnızca adresi **organizasyonun doğrulanmış bir alan adında** olan kullanıcılar onun bağlantısıyla giriş yapabilir veya SCIM ile provision edilebilir. Bu, bir
organizasyon admin'inin başkasının adresini iddia eden bir IdP ayarlamasını (hesap ele geçirme) önler. Bir alan adını yalnızca bir organizasyon doğrulayabilir.

1. **Add domain** (örneğin `example.com`; alt alan adları ayrı alan adlarıdır).
2. Ya sayfada gösterilen TXT kaydını yayınla ve **Check DNS**'e tıkla:

   ```
   _openlog-verification.example.com.  TXT  "openlog-domain-verification=4f0c…"
   ```

   ya da (SMTP ile) alan adının `admin@`, `administrator@`, `hostmaster@`, `postmaster@` veya `webmaster@` adresine **Send e-mail**; bağlantı alan adını doğrular
   (24 saat).

TXT kaydı doğrulamadan sonra kaldırılabilir. Alan adını openlog'dan kaldırmak o alan adından SSO girişlerini durdurur.

## 3. Bağlantıyı oluştur {#_3-create-the-connection}

Bir organizasyonun en fazla **10 bağlantısı** olabilir (örneğin Entra ID'de ana şirket, Okta'da bir yan kuruluş). İlk bağlantı **varsayılan bağlantıdır**. Her
doğrulanmış alan adı, **Connection** sütununda seçilen bağlantıyla giriş yapar (varsayılan: varsayılan bağlantı); bir bağlantı yalnızca kendisine yönlendirilen alan
adlarının kimliklerini kabul eder, yani bir IdP başka bir IdP'nin alan adındaki kullanıcılar olarak giriş yapamaz. **Add connection** yeni bir bağlantı için sihirbazı açar;
her bağlantı ayrı ayrı devre dışı bırakılabilir, yenilenebilir veya silinebilir (silmek alan adlarını varsayılan bağlantıya geri yönlendirir ve oturumlarını bitirir).

Sihirbazın dört adımı var: **Protocol → Service provider → Identity provider → Users and roles**. IdP değerleriyle bir kez kaydet, **Check configuration**'ı çalıştır, sonra
**Test sign-in** (seni IdP'de giriş yaptırır ve openlog'un kullanacağı e-posta, grup ve rolü gösterir; oturum veya üye oluşturulmaz).

openlog'un IdP'n için gösterdiği değerler:

| | OIDC | SAML |
|---|---|---|
| Redirect / ACS URL | `https://openlog.example.com/api/v1/sso/oidc/callback` | `https://openlog.example.com/api/v1/sso/saml/<connection id>/acs` |
| Logout URL | post logout redirect URI `https://openlog.example.com/api/v1/sso/oidc/logout/callback` | single logout service `https://openlog.example.com/api/v1/sso/saml/<connection id>/slo` (HTTP-Redirect ve HTTP-POST, metadata'da) |
| IdP'den logout (back-channel) | back-channel logout URI `https://openlog.example.com/api/v1/sso/oidc/<connection id>/backchannel-logout` (session/`sid` zorunlu) | SOAP single logout service `https://openlog.example.com/api/v1/sso/saml/<connection id>/slo/soap` (metadata'da) |
| IdP'den logout (front-channel) | front-channel logout URI `https://openlog.example.com/api/v1/sso/oidc/<connection id>/frontchannel-logout` (session zorunlu) | yukarıdaki single logout service |
| Entity ID / audience | client ID | `https://openlog.example.com/api/v1/sso/saml/<connection id>/metadata` (aynı zamanda metadata URL'i) |
| Girişi başlatan | openlog (authorization code + PKCE S256, state, nonce) | openlog (HTTP-Redirect AuthnRequest), isteğe bağlı IdP |
| NameID | – | e-posta adresi (veya `email` özelliğiyle unspecified) |
| İmzalama | RS/PS/ES 256–512, EdDSA (asla `none`/HMAC) | RSA/ECDSA SHA-256/384/512 (SHA-1 reddedilir); assertion'lar veya response imzalı olmalı; logout mesajları iki yönde de imzalıdır |
| Şifreleme | – | SP sertifikasıyla isteğe bağlı şifreli assertion'lar: AES-256/192/128-GCM veya -CBC, RSA-OAEP (2001 MGF1P veya MGF1-SHA-256/… ile xmlenc 1.1); RSA PKCS#1 v1.5 ve 3DES reddedilir |

SAML entity ID ve ACS URL, bir SAML bağlantısı ilk kaydedildiğinde üretilir (IdP metadata URL'ini veya XML'i yapıştır, kaydet, sonra SP değerlerini kopyala veya IdP'ye metadata
URL'ini ver). OIDC back-channel ve front-channel logout URI'leri de bağlantı id'sini içerir ve ilk kayıttan sonra görünür.

**Metadata URL güveni.** IdP metadata URL'i ile openlog'un o URL'in sonradan sunacaklarına güvenip güvenmeyeceğini bilmesi gerekir (bölüm 7). Kaydederken birini seç:
- **İmzalı metadata** (Entra ID federation metadata, ADFS, Shibboleth/federasyonlar): IdP'nin *metadata signing certificate*'ini (PEM) yapıştır. Boş bırakırsan ve metadata
  imzalıysa openlog metadata'yı imzalayan sertifikayı sabitler; IdP değerlerinin altında gösterilen parmak izini IdP'nin yayınladığıyla karşılaştır.
- **İmzasız metadata** (Keycloak descriptor'ı, Okta, Google): *Allow unsigned metadata* kutusunu işaretle. Yeni imzalama sertifikaları veya endpoint'ler içeren yenilenmiş metadata
  bu durumda onayını bekler.

Bu seçim yapılmadan imzasız bir metadata URL'iyle veya girilen sertifikayla doğrulanmayan metadata ile kaydetmek reddedilir.

Özellikler (varsayılanlar çoğu IdP için çalışır; son adımda ezebilirsin):

| | OIDC claim | SAML attribute |
|---|---|---|
| E-posta | `email` (kapatılmadıkça + `email_verified=true` zorunlu) | `email`, `mail`, `emailaddress`, `…/claims/emailaddress` URI'si veya e-posta NameID |
| Ad | `name` veya `given_name family_name` | `name`, `displayName`, `firstName lastName` |
| Gruplar | `groups` | `groups` veya `http://schemas.microsoft.com/ws/2008/06/identity/claims/groups` |

### Okta {#okta}

- **OIDC:** Applications → Create App Integration → OIDC, Web Application. Sign-in redirect URI = openlog'un redirect URI'si; grant type Authorization Code. Kullanıcıları ata.
  openlog'da: issuer `https://<your>.okta.com` (veya authorization server issuer'ı, örneğin `https://<your>.okta.com/oauth2/default`), client ID ve secret. Gruplar: bir `groups`
  claim'i ekle (Sign On → OpenID Connect ID Token → Groups claim filter, örneğin *Matches regex* `.*`) ve authorization server'ın istiyorsa `groups` scope'unu ekle.
- **SAML:** Create App Integration → SAML 2.0. Single sign-on URL = ACS URL, Audience URI = entity ID, Name ID format EmailAddress, Application username Email. Attribute statement
  `email` → `user.email`; group attribute statement `groups` (filter). "Metadata URL" değerini openlog'a kopyala ve *Allow unsigned metadata* kutusunu işaretle (Okta'nın metadata URL'i
  imzalı değildir).
- **Logout:** SAML uygulamaları: *Enable Single Logout*, Single Logout URL = openlog'un single logout service'i, SP Issuer = entity ID ve imza sertifikası olarak openlog'un SP sertifikası
  (front-channel; Okta SOAP endpoint'i çağırmaz). OIDC uygulamaları: Okta, app integration'ların back-channel veya front-channel logout URI'lerini çağırmaz; openlog'un
  "Sign out everywhere" seçeneği (RP-initiated logout, sign-out redirect URI) yine Okta oturumunu bitirir. Okta org'un uygulama için OIDC back-channel logout sunuyorsa back-channel logout
  URI'sini session (`sid`) seçeneğiyle kaydet.
- **SCIM:** SCIM destekli bir uygulama kullan (App Integration Wizard → SAML 2.0 veya OIDC + Provisioning → SCIM). SCIM connector base URL = openlog'un SCIM base URL'i, unique identifier
  `userName`, eylemler Push New Users, Push Profile Updates, Push Groups; kimlik doğrulama SCIM token'ıyla (`ols_…`) "HTTP Header".

### Microsoft Entra ID (Azure AD) {#microsoft-entra-id-azure-ad}

- **OIDC:** App registrations → New registration, Web redirect URI = openlog'un redirect URI'si. Certificates & secrets → yeni client secret. Token configuration → isteğe bağlı `email` claim'i
  (ID token) ve bir **groups claim** ekle (Security groups; grup object ID'lerini yayar, bu ID'leri openlog'da eşle veya senkronize gruplar için "Group ID" → *sAMAccountName*). Issuer:
  `https://login.microsoftonline.com/<tenant id>/v2.0`. Entra ID `email_verified` göndermez; Entra için ya "Require email_verified" işaretini kaldır (alan adın Entra'da doğrulanmıştır) ya da
  kullanıcının adresiyse e-posta özelliğini `preferred_username`/`upn` yap.
- **SAML:** Enterprise applications → New application → Create your own → Non-gallery. Single sign-on → SAML: Identifier = entity ID, Reply URL = ACS URL. Attributes: `emailaddress` (user.mail)
  kalsın ve bir group claim ekle. "App Federation Metadata Url" değerini openlog'a kopyala; uygulamanın SAML imzalama sertifikasıyla imzalıdır, openlog kaydederken onu sabitler (thumbprint'i
  karşılaştır). Entra bu sertifikayı değiştirdiğinde metadata yenisiyle imzalanır: değişikliği bağlantının health bölümünde onayla (bölüm 7). Logout URL (isteğe bağlı) = openlog'un single logout
  service'i (front-channel; Entra SOAP logout isteği göndermez).
- **Logout (OIDC):** Authentication → *Front-channel logout URL* = openlog'un front-channel logout URI'si. openlog yalnızca `iss` ve `sid` içeren isteklerde işlem yapar (isteğe bağlı ID token
  claim'i `sid`'i ekle); bunlar olmayan isteklere `400` döner ve oturumları yalnızca "Sign out everywhere" bitirir. Entra ID OIDC back-channel logout token'ı göndermez.
- **SCIM:** enterprise application → Provisioning → Automatic; Tenant URL = SCIM base URL, Secret Token = SCIM token. Entra atama kaldırıldığında `PATCH active "False"` gönderir; openlog üyeliği
  hemen kaldırır.

### Google Workspace {#google-workspace}

Google'ın OIDC'si grupları içermez; grup tabanlı roller için **SAML** kullan. Admin console → Apps → Web and mobile apps → Add custom SAML app. ACS URL ve Entity ID openlog'dan, Name ID format EMAIL,
Name ID *Basic Information > Primary email*. Attribute mapping: *Primary email* → `email`; Group membership → `groups`. IdP metadata'sını indir ve openlog'a yapıştır (veya XML'ini); uygulamayı
organizational unit'ler için aç. Google Workspace'in özel uygulamalara SCIM push'u yoktur.

### Keycloak {#keycloak}

- **OIDC:** Clients → Create client (OpenID Connect), Client authentication ON, Standard flow ON, Valid redirect URIs = openlog'un redirect URI'si, Valid post logout redirect URIs = openlog'un post
  logout redirect URI'si (veya `+`); PKCE method S256 (Advanced). Client scopes → dedicated scope → Add mapper *Group Membership*, token claim name `groups`, *Full group path* OFF. Issuer
  `https://keycloak.example.com/realms/<realm>`.
- **SAML:** Clients → Import client, openlog'un SP metadata'sını yükle (veya admin API üzerinden Realm settings → *client-description converter*). Name ID format `email`, Force name ID format ON,
  Sign assertions ON, Encrypt assertions ON veya OFF (Keycloak'un varsayılanı AES-256-GCM/RSA-OAEP çalışır), openlog'daki "Sign authentication requests" ile birlikte Client signature required ON (SP
  sertifikası metadata'dan gelir), Logout service redirect/POST binding URL = openlog'un single logout service'i olacak şekilde Front channel logout ON. Mapper'lar: *User Property* `email` → attribute
  `email`; *Group list* → attribute `groups`, full path OFF. IdP metadata URL'i: `https://keycloak.example.com/realms/<realm>/protocol/saml/descriptor` (imzasız: *Allow unsigned metadata* kutusunu
  işaretle). IdP-initiated: client üzerinde *IDP-Initiated SSO URL name* ve *relay state* (örneğin `/hosts`) ayarla ve openlog'da o yolla IdP-initiated girişe izin ver.
- **Keycloak'tan logout (OIDC):** client → Settings → *Logout settings*: ya *Front channel logout* ON, *Front-channel logout URL* = openlog'un front-channel logout URI'si ve *Front-channel logout session
  required* ON (`frontchannel.logout.url`, `frontchannel.logout.session.required` özellikleri), ya da *Front channel logout* OFF, *Backchannel logout URL* = openlog'un back-channel logout URI'si ve
  *Backchannel logout session required* ON (`backchannel.logout.url`, `backchannel.logout.session.required`). Back-channel, Keycloak'ta bir yönetici veya oturum süresi dolması tarafından tarayıcısız
  bitirilen oturumları da kapsar; openlog'un Keycloak'tan erişilebilir olması gerekir.
- **Keycloak'tan logout (SAML, SOAP):** *Front channel logout* OFF ve Advanced → *Logout Service SOAP Binding URL* = openlog'un SOAP single logout service'i (`saml_single_logout_service_url_soap`).
  Keycloak bu durumda imzalı LogoutRequest'i sunucudan sunucuya gönderir.

`test/sso` içindeki uçtan uca test iki akışı da Keycloak 26'ya karşı çalıştırır: bir OIDC varsayılan bağlantı ve alan adına göre yönlendirilen şifreli assertion'lı bir SAML bağlantısı, iki yönde single
logout, OIDC RP-initiated logout ve (`OPENLOG_TEST_CALLBACK_HOST=host.docker.internal` ile) Keycloak'ın başlattığı OIDC back-channel ve front-channel logout ile SAML SOAP logout (bkz.
`test/integration/sso/docker-compose.yml`).

## 4. Kullanıcılar ve roller {#_4-users-and-roles}

- **Just-in-time provisioning** (varsayılan açık): doğrulanmış alan adındaki bir kullanıcının ilk girişi openlog hesabını (parolasız) ve eşlenen rolle, yoksa **varsayılan rolle** (viewer) üyeliği
  oluşturur.
- **Rol eşlemeleri** (grup → admin/member/viewer): eşleşen en yüksek rol kazanır. Eşleme varsa ve IdP bir groups claim/attribute gönderiyorsa, owner olmayan roller her girişte (ve SCIM grup
  değişikliklerinde) eşitlenir. Owner'ları SSO veya SCIM asla değiştirmez. Eşlemeler **organizasyon genelinde** (SCIM ve her bağlantı kullanır) veya **bağlantı başına** olur (kendi eşlemeleri olan bir
  bağlantı girişte yalnızca onları kullanır).
- **Davetler:** davet edilen adres daveti kabul etmek için SSO ile giriş yapar (davet edilen rolle, just-in-time provisioning kapalıyken de).
- **Maksimum oturum süresi** kullanıcıları 1 saat – 7 gün sonra IdP'ye geri gönderir (yoksa `OPENLOG_SESSION_TTL`).
- SSO oturumları **yalnızca giriş yaptıkları organizasyonda** işlem yapar; bağlantıları devre dışı bırakıldığında veya silindiğinde biter. **Sign out** yereldir; kullanıcı menüsündeki **Sign out
  everywhere (IdP)** IdP oturumunu da bitirir (bölüm 5).
- IdP-initiated SAML varsayılan olarak kapalıdır. Tarayıcıya bağlanamaz (login CSRF), bu yüzden yalnızca kullanıcıların IdP uygulama başlatıcısına ihtiyacı varsa aç ve izin verilen başlangıç sayfalarını
  (RelayState) listele.

## 5. Single logout {#_5-single-logout}

**Sign out everywhere (IdP)** (kullanıcı menüsü, yalnızca SSO oturumları) openlog oturumunu, kullanıcının aynı bağlantıdaki diğer openlog oturumlarını ve ardından kimlik sağlayıcıdaki oturumu bitirir:

- **SAML:** openlog, IdP'nin metadata'sındaki SingleLogoutService'e bir LogoutRequest gönderir (girişin NameID ve SessionIndex değerleri, SP anahtarıyla imzalı); IdP openlog'un single logout service'ine
  cevap verir ve tarayıcı "You are signed out of openlog and your identity provider" (`?sso_logout=ok`) mesajıyla veya IdP onaylamazsa tarayıcıyı kapatma ipucuyla (`?sso_logout=partial`) giriş sayfasına
  döner. IdP metadata'sında SingleLogoutService yoksa yalnızca openlog oturumları biter.
- **IdP-initiated SAML logout:** kullanıcı IdP'de (veya aynı IdP oturumundaki başka bir uygulamada) çıkış yaptığında IdP, tarayıcı üzerinden (front-channel) single logout service'e bir LogoutRequest gönderir.
  openlog bunu yalnızca IdP sertifikasıyla imzalı, bu bağlantıya adreslenmiş, güncel ve tekrar oynatılmamışsa kabul eder, o NameID'nin (ve SessionIndex'in) oturumlarını bitirir ve imzalı bir LogoutResponse
  ile cevap verir.
- **SAML SOAP back-channel logout:** oturumları tarayıcısız bitiren bir IdP (yönetici logout'u, oturum süresinin dolması) LogoutRequest'i SOAP single logout service'e post eder. Aynı kontroller geçerlidir
  (IdP sertifikasıyla imza, issuer, varsa destination = SOAP veya SLO URL'i, yaş, replay); openlog SOAP gövdesinde imzalı bir LogoutResponse ile veya ayrıntısız bir SOAP fault ile cevap verir.
- **OIDC:** openlog, `id_token_hint` ve post logout redirect URI ile sağlayıcının `end_session_endpoint` adresine yönlendirir; bu URI'yi sağlayıcıda kaydet (Keycloak "Valid post logout redirect URIs", Okta
  "Sign-out redirect URIs").
- **OIDC back-channel logout (sağlayıcıdan):** sağlayıcı bağlantının back-channel logout URI'sine imzalı bir `logout_token` post eder. openlog bunu sağlayıcının JWKS'i ile doğrular (issuer, audience = client ID,
  5 dakika içinde üretilmiş, back-channel logout olayı, nonce yok, `jti` daha önce görülmemiş) ve `sid`'inin oturumlarını bitirir; token'da `sid` yoksa bağlantıdaki `sub`'ının tüm oturumlarını. URI'yi
  "session required" ile kaydet.
- **OIDC front-channel logout (sağlayıcıdan):** sağlayıcının logout sayfası bağlantının front-channel logout URI'sini `iss` ve `sid` ile gizli bir iframe'de yükler; openlog o `sid`'in oturumlarını bitirir. Sayfa
  yalnızca issuer'ın origin'i tarafından frame'lenebilir. openlog'un oturum cookie'si `SameSite=Strict` olduğundan o iframe'e asla ulaşmaz, bu yüzden `sid`'siz istekler reddedilir; sağlayıcı sunuyorsa back-channel
  logout'u tercih et (tarayıcı üçüncü parti iframe'leri engellediğinde de çalışır).
- **Dönüş yolu:** `/login` veya sihirbazın son adımında "Logout redirect paths" altında listelenen bir yol.

Her logout audit'e yazılır (`via` `user`, `idp`, `idp_soap`, `oidc_backchannel` veya `oidc_frontchannel` ve iptal edilen oturum sayısıyla `sso.logout`); reddedilen IdP istekleri `sso.logout_failed` olarak
(istemci adresi başına 10 dakikada 60, sonra kontrol edilmeden reddedilir).

## 6. Single sign-on'u zorunlu kıl {#_6-enforce-single-sign-on}

Owner'lar SSO'yu **bağlantı başına** zorunlu kılabilir: zorunluluk, doğrulanmış e-posta alan adı o bağlantıyla giriş yapan üyelere uygulanır. openlog şunlar olana kadar zorunluluğu açmayı reddeder:

1. bağlantı etkin,
2. **mevcut ayarların test girişi** başarılı olmuş (her kayıt yeni bir test ister),
3. en az bir doğrulanmış alan adı bu bağlantıyla giriş yapıyor,
4. en az bir **break-glass owner** seçilmiş; bu owner'lar IdP arızalanırsa parolayla girişi korur,
5. sen de erişimini koruyorsun (break-glass owner'sın veya bu bağlantıyla giriş yaptın).

Zorunluluk açıkken e-posta alan adı doğrulanmış üyeler organizasyonda parola oturumu kullanamaz (mevcut oturumlar bir sonraki isteklerinde erişimi kaybeder; tüm organizasyonları SSO zorunlu kılıyorsa parolayla giriş
"requires single sign-on" cevabını verir). Doğrulanmış alan adları dışındaki üyeler (başka adresleri olan yükleniciler) etkilenmez. Zorunluyken bağlantı devre dışı bırakılamaz veya silinemez, son doğrulanmış alan adı
da kaldırılamaz veya başka bir bağlantıya taşınamaz.

**Sahiplenilen alan adları.** Doğrulanmış bir alan adı etkin bir bağlantıyla giriş yapıyorsa o alan adının adresleri parola hesabı oluşturamaz: self-servis kayıt **Continue with SSO** ile birlikte "<organization> signs
in with single sign-on for this e-mail domain" gösterir ve o organizasyonun davetleri SSO ile giriş yapılarak kabul edilir. *Başka* bir organizasyonun böyle bir adrese daveti, yalnızca bağlantı için "Allow invitations to
other organizations with a password" açıkken parolayla kabul edilir (varsayılan açık).

**Kilitlenmeden kurtulma** (IdP çalışmıyor, hiçbir break-glass owner giriş yapamıyor): bir operatör PostgreSQL'de zorunluluğu kapatabilir:

```sql
UPDATE sso_connections SET enforce = false WHERE org_id = (SELECT id FROM organizations WHERE tenant_id = '<tenant>');
```

## 7. IdP metadata yenileme ve bağlantı sağlığı {#_7-idp-metadata-refresh-and-connection-health}

Api lideri, etkin bağlantıların IdP dokümanlarını arka planda yeniler: OIDC discovery ve JWKS 30 dakikada bir (her api pod'u yenilenmiş kopyayı bir saate kadar kullanır, yani anahtar rotasyonu veya yavaş bir discovery
endpoint'i girişleri geciktirmez), SAML metadata'sı metadata URL'inden `cacheDuration` veya kalan `validUntil` süresinin yarısında (15 dakika ile 24 saat arasında, ikisi de yoksa 6 saat). Yapıştırılan metadata yalnızca
yeniden doğrulanır. Yenilenen SAML metadata'sı yapılandırma sürümünü asla değiştirmez (test girişi geçerli kalır); neyi değiştirebileceği metadata güvenine bağlıdır (bölüm 3):

- **İmzalı metadata:** dokümanın kökünde, sabitlenmiş sertifikayla, tam olarak bir XML imzası olmalıdır (SHA-256 veya daha güçlü, tüm dokümana referans veren). Yeni IdP imzalama sertifikaları ve SSO/SLO URL'leri otomatik
  uygulanır (`sso.connection.metadata_refresh`). Eksik veya geçersiz imza (kurcalanmış veya "wrapped" dokümanlar) yenilemeyi başarısız yapar ve hiçbir şeyi değiştirmez. **Başka bir sertifikayla** imzalı metadata (IdP metadata
  imzalama anahtarını değiştirmiş) da yenilemeyi başarısız yapar ve yeni sertifikanın parmak iziyle bekleyen değişiklik olarak gösterilir.
- **İmzasız metadata:** aynı imzalama sertifikaları ve endpoint'ler sessizce yenilenir; **yeni imzalama sertifikaları veya endpoint'ler** uygulanmak yerine bekleyen değişiklik olarak gösterilir, aksi halde metadata URL'inin
  cevabını değiştirebilen herkes girişleri ele geçirebilirdi.

Bekleyen bir değişiklik (`sso.connection.metadata_pending`) bağlantıyı son kabul edilen metadata'da tutar; health nedenini gösterir. **Confirm change** metadata'yı yeniden çeker, hâlâ tam olarak o değişikliği içeriyorsa uygular
ve yeni metadata imzalama sertifikasını sabitler (`sso.connection.metadata_accept`). Onaylamadan önce sertifikaları IdP'nle doğrula. Değişen entity ID, süresi dolmuş metadata veya süresi dolmuş imzalama sertifikası önceden
olduğu gibi bildirilir; yeni bir entity ID'yi kabul etmek için bağlantıyı kaydet.

Her bağlantı sağlığını son kontrolle birlikte gösterir: **OK**, **Warning** (bir veya iki başarısız yenileme, 30 gün içinde süresi dolan sertifika, 7 günden az geçerli metadata), **Error** (art arda üç başarısız yenileme, süresi
dolmuş sertifika) veya **Unknown** (henüz yenilenmedi); **Refresh now** yenilemeyi hemen çalıştırır. Başarısız yenilemeler 5, 10, 20, 40 dakika sonra, sonra saatlik yeniden denenir. Metrikler:
`openlog_sso_idp_refresh_total{protocol,result}`, `openlog_sso_idp_refresh_duration_seconds{protocol}`, `openlog_sso_idp_refresh_failing_connections` (0'ın üstünde kalırsa alarm kur).

## 8. SCIM provisioning {#_8-scim-provisioning}

**SCIM provisioning** altında bir token oluştur (`ols_…`, bir kez gösterilir, API key'ler gibi hash'lenir) ve IdP'yi SCIM base URL `https://openlog.example.com/api/scim/v2` ile ayarla. Desteklenenler: `/Users` ve `/Groups` (GET,
`filter=userName eq "…"` / `externalId eq "…"` / `displayName eq "…"`, `startIndex`, `count` ile; POST; PUT; `add`/`replace`/`remove`, path'siz değerler ve `members[value eq "…"]` ile PATCH; DELETE), `/ServiceProviderConfig`,
`/ResourceTypes`, `/Schemas`. Bulk, sort veya ETag yok.

- Aktif bir kullanıcı oluşturmak üyeliği ekler (adres yeniyse hesabı da; adresler doğrulanmış bir alan adında olmalı).
- **E-posta değişiklikleri:** `emails` içinde yeni bir birincil adres (PUT, PATCH `emails`, `emails[primary eq true].value`, `emails[type eq "work"].value`) veya eski `userName` adres iken yeni bir `userName`, eski ve yeni adres
  organizasyonun doğrulanmış alan adlarındaysa, yeni adresi başka bir hesap kullanmıyorsa (`409 uniqueness`) ve kullanıcı owner değilse (`400 mutability`) openlog hesabının adresini değiştirir. `scim.user.email_change` olarak
  audit'e yazılır.
- `active=false` veya DELETE üyeliği **hemen** kaldırır, kullanıcının organizasyondaki SSO oturumlarını ve orada oluşturduğu API key'leri iptal eder; deprovision edilen bir kullanıcı, IdP yeniden etkinleştirene kadar JIT girişiyle
  yeniden eklenmez. Owner'lar SCIM üzerinden deprovision edilemez.
- SCIM grup üyeliği + rol eşlemeleri rolleri belirler (eşlemeler değişince yeniden hesaplanır).

## 9. Sorun giderme {#_9-troubleshooting}

Başarısız girişler `/login?sso_error=<code>` adresine döner ve audit log'a yazılır (nedeniyle `sso.login_failed`; ayrıntıları yalnızca yöneticiler görür). Başarılı olanlar `sso.login` loglar.

| Kod | Anlamı / çözümü |
|---|---|
| `expired` | Giriş `OPENLOG_SSO_LOGIN_TTL` süresinden eski, iki kez kullanılmış, başka bir tarayıcıda başlatılmış (cookie'ler engelli mi?) veya bu arada ayarlar değişmiş |
| `idp_error` | IdP bir hata döndürdü (kullanıcı uygulamaya atanmamış, onay reddedilmiş) |
| `invalid_response` | İmza, issuer, audience, recipient, zaman veya nonce kontrolü başarısız; SP değerlerini, IdP sertifikasını, saati karşılaştır (`OPENLOG_SSO_CLOCK_SKEW`) |
| `replay` | Aynı SAML assertion iki kez post edildi |
| `email_missing` / `email_not_verified` | E-posta özelliğini veya "Require email_verified" (OIDC) ayarını düzelt |
| `domain_not_verified` | İddia edilen adres bu organizasyonun doğrulanmış bir alan adında değil |
| `not_member` | Just-in-time provisioning kapalı ve kullanıcı üye değil |
| `deprovisioned` | SCIM kullanıcıyı devre dışı bıraktı |
| `disabled` | Bağlantı devre dışı veya silinmiş |
| bağlantı ekledikten sonra `domain_not_verified` | Alan adı başka bir bağlantıyla giriş yapıyor: **Connection** sütununu kontrol et |
| IdP logout'undan sonra `/login` adresinde `invalid_request` | IdP LogoutRequest imzasız, bu bağlantının SLO URL'ine adreslenmemiş, çok eski veya SHA-1 ile imzalı; IdP'nin imzalama ve front-channel logout ayarlarını kontrol et |
| `?sso_logout=partial` | IdP logout'u onaylamadı (imzasız veya başarısız LogoutResponse); openlog oturumları yine de bitti |
| IdP'de logout openlog'u girişli bırakıyor | Back-channel/front-channel logout kayıtlı değil, openlog IdP'den erişilemiyor (back-channel), front-channel isteğinde `sid` yok ("session required" aç) veya tarayıcı üçüncü parti iframe'leri engelliyor; audit log'da `sso.logout_failed` kaydına bak |
| `via` `oidc_backchannel` ile `sso.logout_failed` | Logout token bir kontrolden geçemedi (issuer, audience = client ID, süresi dolmuş veya 5 dakikadan eski; saatleri karşılaştır, tekrar kullanılmış `jti`, eksik olay) |
| Health: "not pinned" / "changed … confirm" | IdP metadata'sı değişti (yeni metadata imzalama sertifikası veya imzasız metadata'da yeni sertifikalar/endpoint'ler): IdP ile doğrula ve **Confirm change** |
| Health: "signature is not valid" / "not signed" | Bir imzalama sertifikası sabitliyken metadata URL'i değiştirilmiş, imzasız veya yanlış imzalı metadata döndürdü; hiçbir şey uygulanmadı |

**Check configuration**, discovery/JWKS/PKCE (OIDC) veya metadata/sertifika süresi (SAML) sorunlarını doğrudan gösterir.
