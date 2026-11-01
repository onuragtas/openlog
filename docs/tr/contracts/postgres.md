# Sözleşme: PostgreSQL şeması (M1) {#contract-postgresql-schema-m1}

PostgreSQL 16+ tenancy ve erişim kontrolünü tutar: organizasyonlar (= tenant'lar), kullanıcılar, üyelikler ve
roller, ingest lisans anahtarları, API key'leri, session'lar, davetler, audit log ve giriş rate limiting'i.
Telemetri ClickHouse'da kalır; tek bağlantı, her ClickHouse tablosunun ilk `ORDER BY` sütunu olan
`organizations.tenant_id`'dir.

Servisler onu yalnızca `OPENLOG_AUTH_MODE=postgres` modunda kullanır (varsayılan; bkz. [config.md](/tr/contracts/config)).
`OPENLOG_AUTH_MODE=static` (geliştirme ve testler) PostgreSQL'e asla bağlanmaz.

## Migration'lar {#migrations}

- Dosyalar: `migrations/postgres/NNNN_name.sql`, binary'lere gömülü (`migrations.Postgres`).
- Uygulayanlar: `openlog-migrate` (ClickHouse şemasından önce), başlangıçta `openlog-allinone`
  (`OPENLOG_MIGRATE_ON_START`) ve her `openlog-admin` komutu.
- Her dosya tek bir transaction'da çalışır ve `schema_migrations (version, name, applied_at)` tablosuna kaydedilir.
  Eşzamanlı migrator'lar `pg_advisory_lock(0x6f70656e6c6f67)` ile sıraya sokulur.
- Değişiklikler yalnızca ileri yönlüdür ve önceki sürümle geriye dönük uyumludur (tablo/sütun eklenir,
  aynı sürümde asla yeniden adlandırılmaz veya silinmez), böylece bir rollout sırasında iki sürümün pod'ları birlikte çalışabilir.
- Her dosyanın ilk satırı `-- openlog:phase expand` veya `-- openlog:phase contract` olur
  (docs/contracts/releases-updates.md §6). Contract dosyaları ayrıca baştaki yorum bloğunda
  `-- openlog:requires-all-at-least <version>` bildirir ve yalnızca `component_heartbeats` içindeki (son 5 dakika) her canlı
  instance ile migrator en az o sürümdeyse çalışır; atlanan contract migration'ları kaydedilmeden kalır ve sonraki
  çalıştırmada yeniden denenir, bu yüzden sonraki expand migration'larının ön koşulu olmamalıdırlar.
  `openlog-migrate -plan` kararı gösterir. Aynı kurallar, aynı PostgreSQL tablosuyla kapılanan
  `schema/clickhouse` için de geçerlidir.

## Secret'lar {#secrets}

Erişim sağlayan hiçbir şey düz metin olarak saklanmaz:

| Secret | Biçim | Saklanma şekli |
|---|---|---|
| Ingest lisans anahtarı | `olk_` + 48 hex karakter (192 rastgele bit) veya içe aktarılmış değer (16–256 yazdırılabilir ASCII karakter, bkz. [api.md](/tr/contracts/api#post-apiv1license-keys-name-key)) | `license_keys.key_hash = HMAC-SHA256(OPENLOG_KEY_HASH_SECRET, key)` veya secret yoksa `sha256(key)`; `key_prefix` = ilk 12 karakter (içe aktarılmış değerin en fazla yarısı) |
| API key | `ola_` + 48 hex karakter | `api_keys.key_hash`, lisans anahtarları gibi hash'lenir; `key_prefix` = ilk 12 karakter |
| Davet token'ı | `oli_` + 48 hex karakter | `invitations.token_hash = sha256(token)` |
| E-posta doğrulama token'ı | `olv_` + 48 hex karakter | `email_verifications.token_hash = sha256(token)` |
| Session token'ı (cookie) | 32 rastgele bayt, base64url | `sessions.token_hash = sha256(token)` |
| Cihaz session token'ı (bearer) | `olm_` + 48 hex karakter | `sessions.token_hash = sha256(token)`, aynı sütun; `kind = 'device'` (`0102_device_sessions`). `Authorization` içinde gönderildiği ve sızan bir tanesinin tanınabilmesi gerektiği için öneklidir; cookie olan değildir, çünkü hiçbir yere yapıştırılmaz |
| SCIM token'ı | `ols_` + 48 hex karakter | `scim_tokens.key_hash`, API key'leri gibi hash'lenir (D-044); `key_prefix` = ilk 12 karakter |
| SSO giriş durumu / binding cookie'si | her biri 32 rastgele bayt, base64url | `sso_login_states.state_hash` / `binding_hash = sha256(…)`; PKCE verifier ve nonce 10 dakika saklanır |
| Alan adı doğrulama e-posta token'ı | `oldv_` + 48 hex karakter | `sso_domains.email_token_hash = sha256(token)` |
| Dashboard paylaşım bağlantısı token'ı | `olds_` + 43 base64url karakter (256 rastgele bit) | `dashboard_shares.token_hash = sha256(token)` (`0054_dashboard_shares`, D-087) |
| OIDC client secret, SAML SP private key | IdP'nin verdiği / üretilmiş RSA 2048 | `sso_connections.secret_enc` / `sp_key_enc` = `OPENLOG_SSO_SECRET_KEY`'den gelen anahtarla AES-256-GCM (yoksa `OPENLOG_KEY_HASH_SECRET`'tan türetilir; `_PREVIOUS` değerleri şifre çözer), ek veri = amaç + bağlantı id'si. Biçim `0x01 ‖ key id (4) ‖ nonce ‖ ciphertext`; hiç anahtar yoksa `0x00 ‖ plaintext` (uyarı) |
| Parola | kullanıcının seçtiği, 8–256 karakter | `users.password_hash` = argon2id PHC dizgisi (`m=19456,t=2,p=1`, 16 baytlık salt, 32 baytlık anahtar) |

Anahtarlar yüksek entropili rastgele değerlerdir, bu yüzden salt'sız bir hash yeterlidir ve indeksli aramaya imkân verir.
Anahtarlar bir kez, onları oluşturan yanıtta gösterilir.

**Sunucu tarafı key hash secret'ı (D-044).** `OPENLOG_KEY_HASH_SECRET` ile lisans ve API key'lerinin saklanan hash'i
bir HMAC olur, yani tek başına bir veritabanı dump'ı (muhtemelen düşük entropili) içe aktarılmış değerlerin çevrimdışı tahminine imkân vermez.
Arama, her aday hash'i tek sorguda gönderir (geçerli secret ile HMAC,
`OPENLOG_KEY_HASH_SECRET_PREVIOUS` ile HMAC, düz SHA-256) ve tercih sırasına göre sıralar
(`key_hash = ANY($1) ORDER BY array_position($1, key_hash)`); sonraki bir adayla bulunan satır aynı statement içinde
geçerli hash'e yeniden yazılır (veri değiştiren CTE, o hash zaten varsa atlanır, yalnızca aktif anahtarlar).
Ingest bunu yalnızca cache miss'te yapar, yani sıcak yol değişmez. İçe aktarılmış veya bootstrap anahtar oluşturmak da
eski biçimdeki hash'i var olan bir değeri (aktif veya iptal edilmiş) reddeder. İptal edilmiş satırlar eski hash'lerini korur. Rotasyon:
yeni secret'ı ve eskisini `_PREVIOUS` olarak tüm servislerde aynı anda ayarla; her aktif anahtarın
`last_used_at` değeri rotasyondan sonraya geçtiğinde `_PREVIOUS`'u kaldır. Session, davet ve doğrulama token'ları SHA-256 kalır (rastgele,
kısa ömürlü, operatör seçimi değil). Operatörün seçtiği anahtarlar (Settings → License keys altından içe aktarılan veya
`openlog-admin bootstrap`'e (`OPENLOG_BOOTSTRAP_LICENSE_KEY`) verilen) aynı şekilde hash'lenir; gösterilen önekleri anahtarın
en fazla yarısını açığa çıkarır. İçe aktarılan değerin kendisi de yüksek entropili olmalıdır (örneğin var olan 32 karakterlik rastgele bir anahtar). Bootstrap anahtarları aynı karakter
kurallarına uyar ama 8 karaktere kadar kısa olabilir, böylece `dev-license-key` gibi geliştirme varsayılanları çalışmaya devam eder.

## Tablolar {#tables}

### `organizations` {#organizations}
| Sütun | Tür | Notlar |
|---|---|---|
| `id` | uuid PK | API tarafından kullanılır (`X-Openlog-Org-Id`) |
| `tenant_id` | text UNIQUE | `^[a-z0-9][a-z0-9_-]{0,62}$`; ClickHouse tenant anahtarı; değiştirilemez. Kayıtta `t` + 20 hex karakter olarak üretilir, bootstrap/`create-owner` için operatör seçer |
| `name` | text | 1–200 karakter |
| `created_at`, `updated_at` | timestamptz | |
| `locale` | text | `0060_language_preferences`: varsayılan e-posta dili (`''` = yok, `en`, `tr`; CHECK), admin/owner'lar ayarlar; alıcının tercihi olmadığında organizasyon e-postalarında kullanılır (D-095) |

### `users` {#users}
| Sütun | Tür | Notlar |
|---|---|---|
| `id` | uuid PK | |
| `email` | text UNIQUE | küçük harfle saklanır (CHECK) |
| `name` | text | görünen ad, ≤ 200 karakter |
| `password_hash` | text NULL | NULL = parolayla giriş yok (SSO için ayrılmış, M4) |
| `created_at`, `updated_at`, `last_login_at` | timestamptz | |
| `disabled_at` | timestamptz NULL | devre dışı kullanıcılar giriş yapamaz; session'ları çalışmayı bırakır |
| `email_verified_at` | timestamptz NULL | `0011_email_verification`: yalnızca adresini doğrulamamış self-service kayıtlar için NULL (`OPENLOG_SIGNUP_REQUIRE_VERIFICATION`); bunlar lisans/API key oluşturamaz veya davet edemez. Var olan satırlar `created_at` ile dolduruldu; sütun varsayılanı `now()`, eski binary'lerin oluşturduğu kullanıcıları doğrulanmış tutar |
| `locale` | text | `0050_email_locale`: kullanıcı oluşturulduğunda tercih edilen e-posta dili (kayıt, davet kabulü; `Accept-Language` desteklenen bir dile indirgenir: `en`, `tr`), `''` = İngilizce. Owner'lara kullanım bildirimlerinde kullanılır. `locale_explicit` ile birlikte kullanıcının seçtiği dildir (Settings → Profile) |
| `locale_explicit` | boolean | `0060_language_preferences`: `true` = `locale` kullanıcının tercihidir (web arayüzü ve kullanıcıya e-postalar); `false` = otomatik (D-095) |

### `email_verifications` (`0011_email_verification`) {#email-verifications-0011-email-verification}
`id`, `user_id`, `email`, `token_hash` (UNIQUE), `created_at`, `expires_at` (`OPENLOG_EMAIL_VERIFICATION_TTL`),
`used_at`, `locale` (`0050_email_locale`: bağlantıyı taşıyan e-postanın dili). Bağlantıyı açmak `used_at` ve `users.email_verified_at` değerlerini tek transaction'da ayarlar (yalnızca
kullanıcının hâlâ o e-postası varsa). Her yeniden gönderim yeni bir satır oluşturur; eski kullanılmamış bağlantılar süreleri dolana kadar geçerli kalır. Süresi
dolmuş veya bir günden uzun süre önce kullanılmış satırlar saatlik auth temizliğiyle silinir.

### `memberships` {#memberships}
`(org_id, user_id)` PK, `role` ∈ `owner`, `admin`, `member`, `viewer`, `created_at`. Bir kullanıcının varsayılan
organizasyonu en eski üyeliğidir. Her organizasyonda en az bir owner kalır: son owner'ı düşürmek veya
kaldırmak başarısız olur (owner satırları `FOR UPDATE` ile kilitlenir, yani eşzamanlı düşürmeler
ikisi birden başarılı olamaz).

### `license_keys` (ingest) {#license-keys-ingest}
`id`, `org_id`, `name`, `key_prefix`, `key_hash` (UNIQUE, 32 bayt), `custom` (boolean, `0007_license_key_custom`;
true = operatörün seçtiği değer, içe aktarılmış veya bootstrap; migration'dan önceki satırlar
`key_prefix NOT LIKE 'olk\_%'` üzerinden dolduruldu), `created_by` (kullanıcı, kullanıcı silindikten sonra NULL), `created_at`,
`last_used_at`, `revoked_at`, `revoked_by`. Unique `key_hash`, bir değeri yalnızca tek bir organizasyonun kullanabilmesini sağlar
ve iptal edilmiş bir değer bir daha asla içe aktarılamaz.
Arama: `WHERE key_hash = $1 AND revoked_at IS NULL`. İptal bir soft delete'tir (satır audit izi için ve
hash'i kullanılamaz tutmak için kalır). `last_used_at`, ingest tarafından asenkron yazılır, pod başına anahtar başına
dakikada en fazla bir kez.

### `api_keys` {#api-keys}
`license_keys` gibi, artı `role`, `scope` ve `expires_at` (NULL = asla). `role`
(`0091_api_key_roles`, D-133) `viewer`, `member` veya `admin` olur (`owner` bir key rolü değildir: yalnızca owner'ın
yapabildiği işlemler hesap ve organizasyon yaşam döngüsüdür ve bir insanda kalır); migration'dan önce var olan her satır
`viewer` aldı, yani hiçbir anahtar bundan izin kazanmadı. `scope` (`read`,
`write`) rolden türetilir ve eski istemciler için tutulur. API key'leri yalnızca Query API'de kimlik doğrular
ve rolleriyle hareket eder ([api.md](/tr/contracts/api#roles)); ingest'te asla kimlik doğrulamazlar, ingest
lisans anahtarları da API'de asla kimlik doğrulamaz.

### `sessions` {#sessions}
`id`, `user_id`, `token_hash` (UNIQUE), `csrf_token`, `created_at`, `last_seen_at`, `expires_at`,
`revoked_at`, `ip`, `user_agent` ve `0102_device_sessions`'tan itibaren `kind` (`browser` | `device`, varsayılan
`browser`) ile `device_name` (en fazla 100 karakter, `kind = 'device'` değilse boş). Bir session iptal edilmemişken,
`now < expires_at` ve `now - last_seen_at < ` boşta kalma süresi sağlandığı sürece geçerlidir; iki sınır browser session'ı için
`OPENLOG_SESSION_TTL`/`OPENLOG_SESSION_IDLE_TIMEOUT`'tan, device session'ı için
`OPENLOG_DEVICE_SESSION_TTL`/`OPENLOG_DEVICE_SESSION_IDLE_TIMEOUT`'tan gelir. `last_seen_at`
dakikada en fazla bir kez güncellenir. API, süresi dolmuş veya bir günden uzun süre önce iptal edilmiş session'ları siler
(saatlik, her API pod'undan; idempotent).

Device session'ı bilerek browser session'ından bir tablo satırı kadar uzaktır: aynı kullanıcı, aynı
üyelik rolü, aynı iptal; böylece `GET /api/v1/sessions` bir telefonu browser'ın yanında listeler ve
`DELETE /api/v1/sessions/{id}` onun oturumunu kapatır. `auth.Allow`, bir cihaza yalnızca kullanıcıya özel her işlemi reddetmek için
`kind`'ı okur ([api.md](/tr/contracts/api), [11-mobile-console.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/11-mobile-console.md) §3.2).

### `invitations` {#invitations}
`id`, `org_id`, `email`, `role`, `token_hash` (UNIQUE), `invited_by`, `created_at`, `expires_at`
(`OPENLOG_INVITATION_TTL`), `accepted_at`, `accepted_by`, `revoked_at`. `(org_id, email)` başına en fazla bir bekleyen davet
(kısmi unique index); süresi dolmuş bekleyen davet, yenisi oluşturulurken iptal edilir.
Kabul etmek daveti işaretler ve üyeliği tek transaction'da ekler. `last_sent_at` ve
`send_count` (`0010_invitation_email`) davet e-postalarını kaydeder. Yeniden gönderim, kabul edilmemiş ve iptal edilmemiş bir davetin (süresi dolduktan sonra da)
`token_hash` ve `expires_at` değerlerini değiştirir, yani önceki bağlantı çalışmayı bırakır.
`locale` (`0050_email_locale`) davet oluşturulurken davet edenin dilidir; yeniden gönderilen e-postalar onu korur
(`''`: yeniden gönderen admin'in dili).

### `audit_log` {#audit-log}
`id` (identity), `org_id` (giriş gibi kullanıcı düzeyi olaylar için NULL), `actor_user_id`, `actor_email`,
`actor_api_key_id` (`REFERENCES api_keys ON DELETE SET NULL`) ve `actor_api_key_name` (`0091_api_key_roles`,
D-133), `action`, `target_type`, `target_id`, `details` (jsonb), `ip`, `created_at`.

API key ile yapılan bir değişiklikte `actor_user_id` NULL ve `actor_email` boştur; onun için kullanıcı uydurulmaz:
aktör anahtarın kendisidir, id'siyle ve o andaki adıyla (ad join edilmek yerine kopyalanır, böylece
satır, anahtar yeniden adlandırıldıktan veya silindikten sonra da anlamını korur).

| Aksiyon | Hedef |
|---|---|
| `org.create`, `org.rename`, `org.language_change` (`details.from`/`to`), `bootstrap` | organizasyon |
| `member.role_change` (`details.from`/`to`), `member.remove` (`details.role`, kaldırılan kullanıcının API key'leri iptal edildiğinde `details.revoked_api_keys`, `details.report_recipient_removed`) | kullanıcı |
| `dashboard.report.recipient_remove` (`details.user_id`, `details.reports`, `details.disabled_reports`, `details.via`; rapor alıcı listelerini değiştiren bir üye kaldırmasıyla birlikte yazılır, D-096) | kullanıcı |
| `user.language_change` (`details.from`/`to`; organizasyon yok) | kullanıcı |
| `invitation.create`, `invitation.resend` (`details.email_sent`), `invitation.revoke`, `invitation.accept` | davet |
| `user.email_verified`, `user.verification_resend` (organizasyon yok) | kullanıcı |
| `license_key.create`, `license_key.revoke` | license_key (`details.name`, `details.prefix`; içe aktarılmış değer için `details.custom = true`) |
| `api_key.create` (`details.name`, `details.prefix`, `details.role`), `api_key.revoke` | api_key |
| `user.login`, `user.logout`, `user.password_change`, `user.password_reset`, `session.revoke` | session / kullanıcı |
| `fleet.policy.update` (`details.from`/`to`) | politika (organizasyon id'si) |
| `fleet.host_override.set` (`details.action`/`version`), `fleet.host_override.delete` | agent_host |
| `fleet.rollout.pause`, `fleet.rollout.resume`, `fleet.rollout.deploy_now` (`details.from_wave`/`wave`), `fleet.rollback` (`details.from_version`/`to_version`) | rollout |
| `update.check_requested`, `update.apply_requested` (`details.from`/`to`/`ignore_maintenance_window`/`engine`) | update_request |
| `fleet.rollout.create`, `fleet.rollout.advance`, `fleet.rollout.halt`, `fleet.rollout.complete`, `fleet.rollout.supersede` (aktör e-postası `openlog-controller`, kullanıcı yok) | rollout |
| `integration_setting.create`, `integration_setting.update`, `integration_setting.delete` (`details.integration`/`host_id`/`changed`/`password_changed`) | integration_setting |
| `apm.error_group.update` (`details.service_name`/`service_namespace`/`environment`, `status` ve `assignee_user_id` from/to, `resolved_in_version`), `apm.error_group.comment`, `apm.error_group.comment_delete` (`details.comment_id`) | apm_error_group (hedef id = 16 hex basamaklı grup id'si) |
| `apm.error_group.regressed` (aktör e-postası `openlog-apm`, kullanıcı yok; `details.resolved_at`/`resolved_in_version`/`occurrence_at`/`version`) | apm_error_group |

Audit yazmaları best effort'tur: başarısız bir yazma loglanır (`cannot write audit log`) ve işlemi başarısız
yapmaz.

### `rate_limit_counters` (`0061_rate_limit_counters`) {#rate-limit-counters-0061-rate-limit-counters}
UNLOGGED. `key` (bytea, 16 bayt = sha256(purpose, subject); istemci IP'leri ve token'lar saklanmaz), `bucket`
(timestamptz, dakika başlangıcı), `n`; PK `(key, bucket)`, `bucket` üzerinde index. Public dashboard paylaşım bağlantısı endpoint'lerinin cluster çapında
kayan pencere sayaçları (api.md "Public share link endpoints", `internal/ratelimit`, D-096): her api pod'u
sayılarını saniyede bir (anahtarlar sıralı, statement başına ≤ 1000) veya limite yakınken istek başına tek bir
`INSERT … SELECT FROM unnest(…) ON CONFLICT (key, bucket) DO UPDATE SET n = n + excluded.n` ile ekler ve geçerli ile önceki bucket'ı
aynı statement içinde okur. Api leader'ı (`rate-limit-prune` görevi) önceki dakikadan eski bucket'ları her
dakika siler. UNLOGGED olduğu için sayaçlar crash veya failover sonrası sıfırdan başlar ve standby'larda yoktur.

### `login_failures` {#login-failures}
`key_hash = sha256(lower(email) + "|" + client IP)`, `attempted_at`. Giriş (ve davet kabulü ile parola değişikliği sırasındaki
parola kontrolleri), `OPENLOG_LOGIN_WINDOW` içinde `OPENLOG_LOGIN_MAX_FAILURES` başarısızlıktan sonra
`429 resource_exhausted` ile reddedilir; başarılı giriş sayacı
temizler. Tüm API pod'ları paylaşır. Bir günden eski satırlar saatlik silinir.
Aynı tablo diğer API rate limit sayaçlarını da tutar, `key_hash = sha256(purpose, subject)` ile: istemci IP'si ve e-posta başına
kayıt denemeleri (`OPENLOG_SIGNUP_MAX_PER_IP`/`_PER_EMAIL`), organizasyon ve davet edilen adres başına davet
e-postaları ve kullanıcı başına doğrulama e-postaları (pencereler ≤ 24 sa).

Listeleme (`GET /audit-log`), `actor_email` veya `actor_api_key_name` alt dizgisine, `action` önekine (`starts_with`) ve zamana göre filtreler, en yeni
önce, `(created_at, id)` üzerinde keyset cursor ile; `audit_log_org_idx` karşılar.

## Single sign-on ve SCIM (`0030_sso`, `0056_sso_connections_slo`, `0063_sso_sessions_index`) {#single-sign-on-and-scim-0030-sso-0056-sso-connections-slo-0063-sso-sessions-index}

OIDC/SAML bağlantıları, sahiplenilen alan adları, rol eşlemeleri, giriş ve çıkış durumları, SAML replay cache'i, single
logout session verileri ve SCIM provisioning ([api.md](/tr/contracts/api#single-sign-on), D-077, D-078, D-088, D-089, kod
`internal/sso`, `internal/scim`, store `internal/store/postgres/sso.go`).

### `sso_connections` {#sso-connections}
`id` (PK), `org_id` (FK organizations; index `(org_id, created_at, id)`; `0056` UNIQUE'i kaldırdı: en fazla 10
bağlantı, api zorunlu kılar; en eskisi **varsayılan bağlantıdır**), `protocol` (`oidc`·`saml`),
`name`, `enabled`, `config` (jsonb `{"oidc": {issuer, client_id, scopes, require_email_verified}}` veya
`{"saml": {idp_metadata_url, idp_metadata_xml, idp_entity_id, idp_sso_url, idp_certificates, idp_cert_not_after,
allow_idp_initiated, relay_state_allowlist, sign_authn_requests, sp_certificate_pem, idp_slo_url, idp_slo_binding,
metadata_signing_certificates, allow_unsigned_metadata, pending_metadata}}` artı `logout_redirect_allowlist`; D-098'in
metadata güven alanları (pin'lenmiş PEM sertifikaları, imzasız seçimi ve onay bekleyen yenilenmiş değişiklik
`{digest, reason, detected_at, idp_certificates, idp_sso_url, idp_slo_url, signer_certificate}`) migration
gerektirmez: eski api binary'leri onları yok sayar ve imzasız metadata'yı eskisi gibi yeniler), `secret_enc`, `sp_key_enc`
(mühürlü, bkz. "Secret'lar"), `email_attribute`, `name_attribute`, `groups_attribute` (boş = varsayılanlar), `jit_enabled`,
`default_role` (admin·member·viewer), `session_max_age_seconds` (0 = session TTL), `enforce`
(CHECK: yalnızca `enabled` iken), `break_glass_user_ids uuid[]`, `config_version` (her ayar kaydında artar),
`tested_version` (o sürümün başarılı test girişiyle ayarlanır), `last_test_at`, `last_test_ok`,
`last_test_error`, `last_test_details` (jsonb: email, groups, role), `created_by`, `updated_by`, timestamp'ler;
`0056`: `allow_external_invitations` (varsayılan true, sahiplenilen alan adı davetleri), arka plan yenileme durumu
`idp_refreshed_at`, `idp_refresh_ok` (NULL = hiç), `idp_refresh_error`, `idp_refresh_failures` (ardışık),
`idp_next_refresh_at` (kısmi index `WHERE enabled`), `idp_cache` (son başarılı fetch'in jsonb `{fetched_at, issuer, oidc_discovery,
oidc_jwks, saml_valid_until}` değeri; yalnızca yenileme yazar, ayar kaydı asla yazmaz).
Yenilenen SAML metadata'sı `WHERE config_version = <read version>` ile `config.saml` içine yazılır ve sürümü
değiştirmez. Session politikası, session'ın bağlantısını (`enabled`, `session_max_age_seconds`) ve
kullanıcının doğrulanmış e-posta alan adının yönlendirdiği bağlantıyı (`enabled AND enforce`, `break_glass_user_ids`), session ile kimlik doğrulanan istek başına **tek**
statement'ta okur (primary key, `sso_domains (org_id, domain)`, `sso_connections_org_idx`).

### `sso_domains` {#sso-domains}
`id`, `org_id`, `domain` (küçük harf), `dns_token` (public TXT değeri), `email_token_hash`/`email_address`/
`email_expires_at` (bekleyen e-posta doğrulaması, 24 sa), `verified_at`, `verification_method` (`dns_txt`·`email`),
`last_checked_at`, `created_by`, `created_at`, `connection_id` (`0056`, FK `sso_connections` **ON DELETE SET NULL**;
NULL = varsayılan bağlantı). UNIQUE `(org_id, domain)`; **kısmi UNIQUE `(domain) WHERE verified_at IS NOT
NULL`**: bir alan adını yalnızca tek bir organizasyon doğrular (ikinci doğrulama → `already_exists`). Başka bir organizasyonun
bağlantı id'sini store reddeder.

### `sso_role_mappings` {#sso-role-mappings}
`org_id`, `connection_id` (`0056`, FK, cascade; NULL = organizasyon geneli), `group_name`, `role` (admin·member·viewer;
owner'lar asla eşlenmez), `created_at`. UNIQUE `(org_id, coalesce(connection_id, 00000000-…), group_name)` (eski
PK'nin yerini alır). Organizasyon geneli eşlemeleri SCIM grupları ve kendi eşlemesi olmayan bağlantılar kullanır; her kapsam
tek transaction'da bütün olarak değiştirilir.

### `sso_login_states` {#sso-login-states}
Süren girişler: `state_hash` (UNIQUE), `binding_hash` (IdP tarafından başlatılan SAML için NULL), `org_id`, `connection_id`
(FK, cascade), `purpose` (`login`·`test`·`logout`), `idp_initiated`, `nonce`, `pkce_verifier`, `saml_request_id`
(AuthnRequest veya SP tarafından başlatılan logout'un LogoutRequest'i),
`redirect_to`, `actor_user_id` (test), `config_version` (giriş sırasındaki bir ayar değişikliği onu geçersiz kılar), `result`
(SAML ACS ile tamamlama arasındaki jsonb doğrulanmış kimlik), `created_at`, `expires_at` (`OPENLOG_SSO_LOGIN_TTL`;
IdP tarafından başlatılan 1 dk), `consumed_at`. Tüketim tek bir `UPDATE … WHERE consumed_at IS NULL AND expires_at > now
RETURNING` ile yapılır, yani bir durum tüm pod'larda bir kez tamamlanır; `result` yalnızca NULL iken yazılır.

### `sso_saml_assertions` {#sso-saml-assertions}
PK `(connection_id, assertion_id)`, `expires_at` (en geç `NotOnOrAfter` + saat kayması, en az 10 dk). `INSERT … ON
CONFLICT DO UPDATE … WHERE expires_at < now()`: bir id süresi dolana kadar bir kez kabul edilir (pod'lar arası replay
koruması).

### `sso_sessions` (`0056`) {#sso-sessions-0056}
Bir SSO session'ının IdP session'ı: `session_id` (PK, FK `sessions`, cascade), `connection_id` (FK, cascade), `org_id`,
`user_id`, `subject` (SAML NameID değeri / OIDC `sub`), `name_id_format`, `name_qualifier`, `sp_name_qualifier`,
`session_index` (SAML SessionIndex / OIDC `sid`), `id_token_enc` (`id_token_hint` için OIDC ID token'ı, client secret'lar gibi
AAD `oidc-id-token:<session id>` ile mühürlü), `created_at`. Index `(connection_id, subject)`: bir IdP
LogoutRequest'i, bağlantının NameID ile aktif session'larını listeler (`sessions` ile join: iptal edilmemiş, süresi dolmamış);
index `user_id`; `0063`: `sid` ile OIDC back-channel/front-channel logout için kısmi index `(connection_id, session_index) WHERE session_index <> ''`
(D-098). LogoutRequest ID'lerinin ve OIDC logout token `jti`'lerinin satırları
replay cache'i paylaşır (`sso_saml_assertions`, `slo:<id>`, `oidc-logout:<jti>`).

### `scim_tokens` {#scim-tokens}
`api_keys` gibi: `id`, `org_id`, `name`, `key_prefix`, `key_hash` (UNIQUE, D-044'e göre HMAC/SHA-256; aramada yeniden hash'lenir),
`created_by`, `created_at`, `last_used_at` (≤ 1 yazma/dk), `expires_at`, `revoked_at`.

### `scim_users` {#scim-users}
PK `(org_id, user_id)` (FK users, cascade), `user_name` (organizasyon başına UNIQUE, büyük/küçük harf duyarsız), `external_id`
(boş değilse organizasyon başına UNIQUE), `active`, `display_name`, `given_name`, `family_name`, timestamp'ler. SCIM kullanıcıyı
devre dışı bıraktığında (üyelik kaldırılır) satır kalır, bu da JIT girişin onları yeniden eklemesini engeller.

### `scim_groups`, `scim_group_members` {#scim-groups-scim-group-members}
`scim_groups`: `id`, `org_id`, `display_name` (organizasyon başına UNIQUE, büyük/küçük harf duyarsız), `external_id`, timestamp'ler.
`scim_group_members`: PK `(group_id, user_id)`, `user_id` üzerinde index; bir SCIM kullanıcısını silmek onu
organizasyonun gruplarından çıkarır.

### `sessions` (`0030_sso` ile eklenen sütunlar) {#sessions-columns-added-by-0030-sso}
`auth_method` (`password`·`oidc`·`saml`, varsayılan `password`), `org_id` (FK organizations, cascade; SSO session'ları yalnızca
bu organizasyonda hareket eder), `sso_connection_id` (FK `sso_connections`, **ON DELETE CASCADE**: bir bağlantıyı silmek
session'larını siler). Eski api binary'leri parola session'larını varsayılanlarla insert eder.

Temizlik (api, 10 dakikada bir, idempotent): bir saatten uzun süre önce süresi dolmuş giriş ve çıkış durumları, süresi dolmuş assertion
ve LogoutRequest id'leri, süresi dolmuş alan adı e-posta token'ları. `users.email` SCIM tarafından değiştirilir (`SetUserEmail`, UNIQUE →
`already_exists`).

Karışık sürümler (`0056`, expand): eski bir api organizasyon başına bir bağlantı okur (aldığı ilk satır),
bağlantıya özel rol eşlemelerini organizasyon geneli sayar (kendi değiştirmesi onları siler) ve `sso_sessions`
satırları oluşturmaz, yani rollout bitene kadar session'ları bir IdP LogoutRequest'iyle sonlandırılamaz.

## Fleet (ajan güncellemeleri, `0002_fleet`) {#fleet-agent-updates-0002-fleet}

Ajan güncelleme politikası, host istisnaları, rollout'lar ve ingest ile senkronize olan ajanlar
([releases-updates.md](/tr/contracts/releases-updates) §3–§4, API: [api.md](/tr/contracts/api#fleet-agent-updates), kod: `internal/fleet`).

### `agent_update_policies` {#agent-update-policies}
`org_id` (PK, FK organizations), `mode` (`off`·`notify`·`auto`), `channel` (`stable`·`beta`), `target`
(`latest`·`patch`·`pinned`), `pinned_version` (text, `target = pinned` iken zorunlu), `waves` (int[], kesin artan,
100'de biten yüzdeler), `wave_soak_minutes` (0–43200), `halt_failure_rate` (0–1),
`maintenance_windows` (jsonb `[{"days": ["mon",…], "start": "HH:MM", "end": "HH:MM"}]`, UTC; `[]` = her zaman;
`end <= start` gece yarısını aşar), `updated_at`, `updated_by`, `php_agent` (`0045_php_agent_fleet`, jsonb
`{"mode": "off|manual|auto", "version": "agent|<semver>", "reload": "none|graceful", "exclude_bins": [], "changed_at"}`;
`{}` = varsayılanlar: `manual`, `agent`, `none`). **Satır yok = varsayılan politika** (`auto`, `stable`, `latest`,
`[10,50,100]`, 60 dk, 0.05, pencere yok, PHP ajanı `manual`). Doğrulamayı API yapar. `java_agent`
(`0086_java_agent_fleet`, jsonb `{"mode": "off|manual|auto", "version": "agent|<semver>", "changed_at"}`; `{}` =
varsayılanlar `manual`, `agent`; java-agent.md §2.6).

### `agent_java_host_overrides` (`0086_java_agent_fleet`) {#agent-java-host-overrides-0086-java-agent-fleet}
`(org_id, host_id)` PK, `mode` (`off`·`manual`·`auto`: politikanınki yerine host'un Java ajanı modu; `auto`
override Java ajanı wave'lerini atlar), `updated_at`, `updated_by`. Host kaybolduğunda satırlar tutulur.

### `agent_php_host_overrides` (`0045_php_agent_fleet`) {#agent-php-host-overrides-0045-php-agent-fleet}
`(org_id, host_id)` PK, `mode` (`off`·`manual`·`auto`: politikanınki yerine host'un PHP ajanı modu; `auto`
override PHP ajanı wave'lerini atlar), `updated_at`, `updated_by`. Host kaybolduğunda satırlar tutulur.

### `agent_update_host_overrides` {#agent-update-host-overrides}
`(org_id, host_id)` PK, `action` (`hold` = asla güncelleme; `pin` = bu host'u fleet rollout'larının dışında, yukarı veya aşağı, `version`'a taşı),
`version`, `updated_at`, `updated_by`. Host kaybolduğunda satırlar tutulur.

### `agent_rollouts` {#agent-rollouts}
`id`, `org_id`, `action` (`upgrade`·`rollback`), `from_version` (bilgi amaçlı: en yaygın çalışan sürüm veya
geri alınan sürüm), `to_version` (`target = patch` için NULL; minor başına hedefleri
`targets` jsonb `{"0.3": "0.3.4"}` içindedir), `waves`/`wave_soak_minutes`/`halt_failure_rate` (oluşturulurken politikadan
kopyalanır), `current_wave` (`waves` içinde index), `wave_started_at`, `state`, `state_reason`, `hosts_pending`,
`hosts_attempted`, `hosts_succeeded`, `hosts_failed`, `hosts_rolled_back` (controller yeniler),
`ack_attempted`/`ack_failed` (durdurulmuş bir rollout'un admin tarafından sürdürülmesiyle onaylanan sayılar; halt
oranından hariçtir), `created_by` (NULL = controller), `created_at`, `updated_at`, `ended_at`.

- Durumlar: `active` → `paused` (admin) / `halted` (hata oranı) → `active` (admin sürdürür); `completed` (bekleyen host
  yok); `superseded` (daha yeni bir rollout ile değiştirildi veya artık politikanın hedefi değil). Kısmi bir unique index
  **organizasyon başına bir açık (`active`, `paused`, `halted`) rollout**'a izin verir; rollout oluşturmak açık olanı aynı
  transaction'da supersede eder.
- Bir organizasyonun **geçerli rollout**'u, `superseded` olmayan en yeni rollout'udur. Sync onun güncellemesini
  geçerli wave'deki host'lara verir (`fnv32a(host_id + ":" + id) % 100 < waves[current_wave]`); `completed`
  bir rollout her host'a hizmet eder (sonradan kurulan ajanlar yetişir).
- Host sınıflandırması (controller, `OPENLOG_FLEET_HOST_STALE_AFTER` içinde sync olmuş host'lar): *succeeded* = bu rollout'un sunduğu ve
  hedefini çalıştıran; *failed*/*rolled back* = bu rollout'un sunduğu ve rollout başladığından beri o durumu raporlayan
  (bu rollout tarafından yeniden sunulmaz); *in progress* attempted ve pending sayılır;
  *pending* = wave'ler ve bakım pencereleri yok sayıldığında güncellemeyi alacak olan (hold ve pin'lenmiş host'lar hariç).

### `agent_hosts` {#agent-hosts}
`(org_id, host_id)` PK, `host_name`, `agent_name`, `agent_version`, `agent_commit`, `agent_os`, `agent_arch`,
`install_method`, `update_capable`, `update_state`, `update_from`, `update_to`, `update_error`,
`update_changed_at`, `config_hash`, `first_seen_at`, `last_sync_at`, `rollout_id` (bu host'a güncelleme sunan son rollout;
soft referans, sonraki sync'ler asla temizlemez), `integrations_config_revision` (`0008_integration_settings`;
ajanın son sync'inde bildirdiği uzak entegrasyon config revizyonu: `''` = yok, `disabled` = host'ta uzak config
kapalı), `php_agent` (`0045_php_agent_fleet`, jsonb: son sync isteğinin `php_agent` bölümü
(PHP runtime'ları, kurulu PHP ajanı sürümü, `managed_by`, son işlem), ingest tarafından sınırlanır; ajan
bildirmezse NULL), `php_access` (`0064_php_access`, jsonb: son sync isteğinin `php_access` bölümü (socket
grubu, PHP-FPM pool'ları ve kullanıcılarının `php.sock`'a yazıp yazamayacağı, php-agent.md §1), en fazla 512 pool; ajan
bildirmezse NULL), `java_agent` (`0086_java_agent_fleet`, jsonb: son sync isteğinin `java_agent` bölümü
(yönetilen jar sürümü, link durumu, yüklü sürüm ve `restart_pending` ile JVM'ler, son işlem), en fazla 64
JVM; ajan bildirmezse NULL).

**Yalnızca ingest tarafından, asenkron** yazılır: sync istekleri bellekten yanıtlanır; raporlar pod başına kuyruğa alınır
(host başına birleştirilir, `OPENLOG_FLEET_REPORT_QUEUE_SIZE` ile sınırlıdır, dolunca en eski rapor düşer:
`openlog_fleet_host_reports_dropped_total`) ve her saniye en fazla 200 satırlık statement'larla upsert edilir
(`INSERT … SELECT FROM unnest(…) JOIN organizations ON tenant_id … ON CONFLICT DO UPDATE`). Başarısız bir batch
atılır; ajanlar sonraki sync'lerinde yeniden raporlar.

### Erişim deseni ve leader {#access-pattern-and-leader}
- Ingest, tenant ve pod başına en fazla `OPENLOG_FLEET_POLICY_CACHE_TTL` (≤ 30 sn, singleflight) başına bir kez şunları okur:
  `tenant_id` ile organizasyon, politikası, override'ları, PHP ajanı override'ları, geçerli rollout ve entegrasyon ayarları. PostgreSQL erişilemezken
  cache'teki durum 5 dakika sunulur; cache'te olmayan organizasyonlar güncelleme almaz (sync yine `200` yanıtlar).
- Rollout controller'ı api leader'ında (session advisory lock `postgres.LeaderLock`, update
  check ile paylaşılır) her `OPENLOG_FLEET_CONTROLLER_INTERVAL` aralığında çalışır: ajanı olan organizasyon başına politikayı,
  override'ları, geçerli rollout'u ve yakın zamanda sync olmuş `agent_hosts` satırlarını okur, sonra sayaçları ve durumu
  koşullu yazmalarla günceller (`WHERE state = <expected>`), böylece eşzamanlı admin aksiyonları kazanır.

## Entegrasyon ayarları (`0008_integration_settings`) {#integration-settings-0008-integration-settings}

### `integration_settings` {#integration-settings}
Arayüzde düzenlenen ve ajan sync'i ile iletilen altyapı ajanı entegrasyon ayarları ([releases-updates.md](/tr/contracts/releases-updates)
§3, API: [api.md](/tr/contracts/api#integration-settings), kod: `internal/intsettings`).

| Sütun | Tür | Notlar |
|---|---|---|
| `id` | uuid PK | API üretir (parola AAD'sinin parçasıdır) |
| `org_id` | uuid FK organizations | cascade |
| `host_id` | text NULL | ajan `host_id`'si (1–256 bayt); NULL = organizasyonun her host'u |
| `integration` | text | `nginx`·`redis`·`mysql`·`postgresql`·`docker` |
| `match_port` | int NULL | 1–65535 |
| `match_container`, `match_endpoint`, `match_instance` | text, varsayılan `''` | bulunan servis seçicileri (≤ 512 bayt, API doğrular); hepsi boş = her instance |
| `enabled` | boolean | varsayılan true |
| `endpoint`, `username`, `database` | text, varsayılan `''` | entegrasyonun kullanmadığı alanlar boş kalır |
| `password_enc` | text, varsayılan `''` | `OPENLOG_SECRETS_KEY` ile parolanın `ol1:<key id>:<AES-256-GCM>` değeri, AAD `org_id/id`; `''` = parola yok |
| `databases` | text[], varsayılan `{}` | ≤ 64 girdi |
| `created_at`, `updated_at` | timestamptz | |
| `updated_by` | uuid NULL | kullanıcı (silmede SET NULL) |

Unique index `integration_settings_scope_uniq (org_id, coalesce(host_id, ''), integration, coalesce(match_port, 0),
match_container, match_endpoint, match_instance)`. Audit: `integration_setting.create`, `integration_setting.update`,
`integration_setting.delete` (hedef `integration_setting` = id; `details.integration`, `host_id`, `changed` (alan
adları), `password_changed`; güncellemede değiştiklerinde `previous_host_id`/`previous_integration` de). Parolalar yanıtlarda, loglarda veya audit detaylarında asla
görünmez. Ingest onları yalnızca sync'i yanıtlamak için çözer. `openlog-alert rotate-secrets` bu satırları henüz
yeniden şifrelemez, bu yüzden her parola yeniden kaydedilene kadar önceki anahtarı `OPENLOG_SECRETS_KEY_PREVIOUS` içinde
tut.

## APM (`0005_apm`) {#apm-0005-apm}

### `apm_service_settings` {#apm-service-settings}
Servis başına APM ayarları ([apm.md](/tr/contracts/apm) §4). Primary key (`org_id`, `service_name`, `service_namespace`,
`deployment_environment`); `apdex_t_ms` 1..600000; `updated_at`, `updated_by` (kullanıcı, nullable). Namespace ve environment'ı boş bir
satır, daha spesifik bir satır yoksa servisin her namespace/environment'ına uygulanır;
satırı olmayan servisler `OPENLOG_APM_DEFAULT_APDEX_T` kullanır. `PUT /api/v1/apm/services/{service_name}/settings` tarafından,
audit olayı `apm.service_settings.update` (hedef `apm_service` = servis adı,
`details.service_namespace`/`environment`/`apdex_t_ms`/`previous_apdex_t_ms`) ile birlikte tek transaction'da yazılır.

### `tail_sampling_policies` (`0040_tail_sampling`) {#tail-sampling-policies-0040-tail-sampling}
Organizasyon başına bir tail sampling politikası ([apm.md](/tr/contracts/apm) §4.2, D-075). Primary key `org_id`; `policy` jsonb nesnesi
(api doğrular: `internal/tailsampling.ParsePolicy`), `version` (1'den başlar, kayıt başına +1; `PUT
/api/v1/apm/sampling` düzenlenen sürümü gönderir ve uyuşmazlıkta 409 alır), `updated_at`, `updated_by` (kullanıcı, nullable).
Audit olayı `apm.tail_sampling.update` (hedef `tail_sampling_policy` = org id,
`details.version`/`enabled`/`baseline_ratio`/`max_spans_per_second`/`rules` sayısı) ile birlikte tek transaction'da yazılır.
`openlog-sampler` tüm satırları `organizations.tenant_id` ile join ederek her `OPENLOG_TAILSAMPLING_POLICY_REFRESH` aralığında okur;
geçersiz belgeler atlanır (varsayılan politika geçerli olur).

## SLO'lar (`0087_slo`) {#slos-0087-slo}

### `slos` {#slos}
APM servislerinin service level objective'leri ([slo.md](/tr/contracts/slo), [api.md](/tr/contracts/api#service-level-objectives), D-125).
`id` (uuid), `org_id` (cascade), `name` (1–200), `description` (≤ 2000), hedef servis
(`service_name` 1–512; `service_namespace` ve `deployment_environment` nullable: NULL = her
namespace/environment, `''` = tam olarak attribute'u olmayan servisler), `sli_type`
(`availability`/`latency`), `latency_threshold_ms` (1–600000, tam olarak `latency` için ayarlı, CHECK), `objective`
(yüzde, 50 ≤ x < 100), `window_days` (7, 28 veya 30), `created_by`/`updated_by` (`ON DELETE SET NULL`),
`created_at`, `updated_at`. Index'ler: (`org_id`, `lower(name)`), (`org_id`, `service_name`). Organizasyon başına en fazla 200 satır
(insert statement'ında kontrol edilir). `/api/v1/slos` tarafından, audit olayı
`slo.create`/`slo.update`/`slo.delete` (hedef tür `slo`) ile birlikte tek transaction'da yazılır. Budget'lar **saklanmaz**:
sorgu anında `apm_transactions_1m`'den hesaplanır.

Migration ayrıca `alert_rules_type_check`'i `slo_burn` ile genişletir (alerting.md §2.11); bu tür kurallar silinmiş bir SLO'nun
`slo_id`'sini korur ve o zaman değerlendirme hatası raporlar.

## Zafiyet kataloğu (`0097_vulnerabilities`) {#vulnerability-catalog-0097-vulnerabilities}

`vulnerabilities` ve `vulnerability_affected`, openlog'un kurulu paketleri karşılaştırdığı advisory'leri,
`vulnerability_sync` ise ekosistem başına son sync'i tutar. Bu veritabanındaki neredeyse her şeyin aksine
kataloğun **`org_id`'si yoktur**: OSV advisory'leri public veridir, her tenant için aynıdır ve
organizasyon başına bir kopya, yanıtta hiçbir fark yaratmadan onlarca megabaytı tenant sayısıyla çarpar. Bulgular (hangi
host'un hangi zafiyetli sürümü çalıştırdığı) tenant verisidir ve ClickHouse'da yaşar
(`host_vulnerabilities`, D-142).

## Job izleme (`0096_job_monitors`) {#job-monitoring-0096-job-monitors}

`job_monitors` tanımdır (ad, takvim türü, cron ifadesi ve saat dilimi veya raporlama
aralığı, grace period, tag'ler) artı işin çağırdığı URL'deki değer olan `ping_token`. `job_monitor_state`
monitör başına bir satırdır: son ping ne dedi ve sonraki çalıştırma ne zaman bekleniyor. Sweeper vadesi geçmiş satırları
`FOR UPDATE ... SKIP LOCKED` ile üstlenir ve `expected_at`'i aynı statement içinde ilerletir, yani kaçırılan bir çalıştırma
bir kez sonuçlandırılır (D-141). Çalıştırmaların kendisi ClickHouse'da yaşar (`job_runs`, 90 gün).

Token hash'lenmeden olduğu gibi saklanır: URL bir crontab'da durur ve yeniden okunabilir kalması gerekir,
yetkilendirdiği şey ise tek bir monitörün kendi durumudur; tehdit modeli [api.md](/tr/contracts/api#job-monitoring) içindedir.

## Synthetic izleme (`0090_synthetics`) {#synthetic-monitoring-0090-synthetics}

`0095_synthetic_check_types` ile genişletildi (D-140): `type` `http`, `tcp`, `dns` ve `tls` kabul eder ve
HTTP olmayan türler `dns_record_type`, `dns_expected` ve `tls_warning_days` ile `target` kullanır (`host:port` veya bir dns check'inin
çözdüğü ad). `synthetic_checks_target_per_type`, bir satırın tek bir türü tanımlamasını sağlayan kısıttır:
`http` check'inde `url` vardır ve `target` boştur, diğer her türde tersi.

### `synthetic_checks` {#synthetic-checks}
Takvimli, dışarıdan içeriye HTTP check'leri ([api.md](/tr/contracts/api#synthetic-monitoring), D-132). `id` (uuid), `org_id`
(cascade), `name` (1–200), `type` (`http`; yeni check türleri CHECK'e eklenir), `enabled`,
`url` (1–2048), `method` (CHECK, yedi verb), `headers` (jsonb nesnesi, ≤ 20 girdi), `body` (≤ 65536),
`expected_status` (`integer[]`, 1–10 kod), `assertion_type`
(`none`/`contains`/`not_contains`/`json_path`), `assertion_path` ve `assertion_value` (her biri ≤ 1024),
`timeout_ms` (500–60000), `interval_seconds` (30–86400), `locations` (`text[]`, 1–10; yerleşik `local` =
openlog sunucusu), `created_by`/`updated_by` (`ON DELETE SET NULL`), `created_at`, `updated_at`. Tablo
CHECK'i `timeout_ms <= interval_seconds * 1000` koşulunu korur, böylece yavaş bir hedef çalıştırmaları biriktiremez. Index:
(`org_id`, `lower(name)`). Organizasyon başına en fazla 100 satır (insert statement'ında kontrol edilir). `/api/v1/synthetics/checks` tarafından,
audit olayı `synthetic_check.create`/`update`/`delete` (hedef tür `synthetic_check`) ile birlikte tek transaction'da yazılır.

### `synthetic_check_schedule` {#synthetic-check-schedule}
Check ve konum başına bir satır, primary key (`check_id`, `location`), `check_id` `synthetic_checks`'ten
cascade olur. `next_run_at` (indeksli) çalıştırmanın vadesidir; `last_run_at`, `last_success` (ilk çalıştırmaya kadar
NULL), `last_status_code`, `last_duration_ms`, `last_error_kind` ve `last_error` son sonuçtur; check listesi bunu
ClickHouse'u sorgulamadan gösterir. Api leader'ı vadesi gelen satırları `FOR UPDATE ... SKIP LOCKED` ile üstlenir ve
`next_run_at`'i check'in aralığı kadar **aynı statement içinde** ilerletir, böylece iki scheduler (leadership devri) aynı check'i asla iki kez çalıştırmaz. Konum eklemek
migration gerektirmez: `synthetic_checks.locations` içinde bir değer artı burada bir satırdır. Çalıştırmaların kendisi burada
**saklanmaz**; ClickHouse `synthetic_runs` tablosuna (30 gün) gider ve metrik data point'leri olarak yansıtılır.

## Cloud bağlantıları (`0092_cloud_connections`) {#cloud-connections-0092-cloud-connections}

### `cloud_connections` {#cloud-connections}
Bir organizasyonun openlog'a metrik okuttuğu tek bir cloud hesabı ([api.md](/tr/contracts/api#cloud-connections), D-135).
`id` (uuid, credential AAD'sinin parçası olduğu için API üretir), `org_id` (cascade), `name`
(1–200), `provider` (CHECK `aws`/`azure`/`gcp`), `ingest_mode` (CHECK `poll`/`push`, varsayılan `poll`),
`enabled`, `credentials_enc`, `credentials_key_id`, `scopes` (`text[]`, 1–50: AWS region'ları, Azure
subscription'ları, GCP projeleri), `services` (`text[]`, 1–50; bilinen değerler CHECK değil
`internal/cloudconnect` içinde bir listedir, yani yeni servis migration gerektirmez), `poll_interval_seconds`
(60–86400), `max_metrics_per_poll` (100–200000), `max_api_calls_per_poll` (1–5000),
`created_by`/`updated_by` (`ON DELETE SET NULL`), `created_at`, `updated_at`. Index: (`org_id`,
`lower(name)`). Organizasyon başına en fazla 50 satır (insert statement'ında kontrol edilir). `/api/v1/cloud/connections` tarafından,
audit olayı `cloud_connection.create`/`update`/`delete`
(hedef tür `cloud_connection`) ile birlikte tek transaction'da yazılır.

`credentials_enc`, sağlayıcıya özel JSON credential belgesinin `OPENLOG_SECRETS_KEY` ile
`ol1:<key id>:<AES-256-GCM>` değeridir, AAD `org_id/id`; `integration_settings.password_enc` ile aynı mekanizma. Liste ve detay
sorguları onu asla seçmez (yalnızca poller'ın ve test aksiyonunun kullandığı credential okuması seçer),
API asla döndürmez ve asla log satırının veya audit detayının parçası olmaz; audit yalnızca
`credentials_changed` kaydeder. `openlog-alert rotate-secrets` bu satırları henüz yeniden şifrelemez, bu yüzden her bağlantı yeniden kaydedilene kadar önceki
anahtarı `OPENLOG_SECRETS_KEY_PREVIOUS` içinde tut. `ingest_mode` `push`
sağlayıcı tarafı teslimat için ayrılmıştır: böyle bir satır credential'larını, scope'larını ve servislerini korur ve basitçe
takvim satırı yoktur, yani sonradan push eklemek migration gerektirmez.

### `cloud_connection_schedule` {#cloud-connection-schedule}
Bağlantı ve scope başına bir satır, primary key (`connection_id`, `scope`), `cloud_connections`'tan cascade olur.
`next_run_at` (indeksli) poll'un vadesidir; `last_run_at`, `last_status` (ilk poll'a kadar NULL, sonra
`ok`/`partial`/`error`), `last_error`, `last_metrics`, `last_api_calls`, `last_duration_ms` son
sonuçtur; bağlantı listesi bunu çalıştırma geçmişini okumadan gösterir. `consecutive_errors`, başarısız bir scope'un
üstel backoff'unu yönetir (hatasız her poll 0'a sıfırlar). Api leader'ı vadesi gelen satırları
`FOR UPDATE ... SKIP LOCKED` ile üstlenir ve `next_run_at`'i bağlantının aralığı kadar **aynı
statement içinde** ilerletir, böylece iki scheduler (leadership devri) aynı scope'u asla iki kez poll etmez; bu da burada
sağlayıcının aynı poll için asla iki kez fatura etmediği anlamına gelir. Yalnızca credential'ı saklı, etkin `poll` bağlantılarının satırları
üstlenilir. Scope başına bir satır, hataları izole eden şeydir de: throttle edilen veya
yanlış yapılandırılmış bir region kendi durumunu ve backoff'unu taşır ve diğerlerini asla durdurmaz.

### `cloud_collection_runs` {#cloud-collection-runs}
Durum rozetinin arkasındaki dürüst geçmiş için son poll'lar: `connection_id` (cascade), `scope`,
`started_at`, `duration_ms`, `status` (CHECK `ok`/`partial`/`error`), `metrics` (yazılan data point'ler),
`api_calls` (yapılan sağlayıcı istekleri, sağlayıcının faturaladığı şey), `throttled`, `error` ve `services` (bir
JSON dizisi `[{"service","metrics","error"}]`, böylece bir hata ait olduğu servisi söyler). Index
(`connection_id`, `started_at DESC`). API bağlantı başına en yeni 200 satırı tutar ve geri kalanını yazmayla
aynı transaction'da temizler, yani tablo bir retention job'u olmadan küçük kalır. Metriklerin kendisi burada
**değildir**: diğer tüm data point'ler gibi ClickHouse `metrics` tablosuna gider.

## APM hata iş akışı (`0025_apm_error_workflow`) {#apm-error-workflow-0025-apm-error-workflow}

Hata gelen kutusu durumu ([apm.md](/tr/contracts/apm) §3.4). Satırı olmayan bir grup çözülmemiş ve atanmamıştır.

### `apm_error_group_states` {#apm-error-group-states}
Primary key (`org_id`, `group_id`); `group_id` = 16 küçük harf hex basamaklı ClickHouse hata grubu id'si (servisi zaten
kodlar); filtreleme için `service_name`, `service_namespace`, `deployment_environment`; `status`
(`unresolved`/`resolved`/`ignored`), `assignee_user_id` (üye; `ON DELETE SET NULL`), `resolved_at` (tam olarak
resolved iken ayarlı, CHECK), `resolved_in_version` (≤ 256 bayt, yalnızca resolved iken, CHECK), `resolved_by`,
`regressed_at`, `regression_count`, `created_at`, `updated_at`, `updated_by` (otomatik yeniden açmalarda NULL). Index'ler:
(`org_id`, `status`, `updated_at DESC`), atanmışken (`org_id`, `assignee_user_id`), (`org_id`, servis üçlüsü).
`PATCH /api/v1/apm/errors/groups` (grup başına `SELECT … FOR UPDATE` altında upsert, audit olayı aynı
transaction'da; atanan kişinin `memberships` satırı olmalıdır) ve regresyon tespiti (koşullu
`UPDATE … WHERE status = 'resolved' AND resolved_at < occurrence`, audit `apm.error_group.regressed`) tarafından yazılır.

### `apm_error_group_comments` {#apm-error-group-comments}
`id` (uuid), `org_id`, `group_id`, `author_user_id` (`ON DELETE SET NULL`), `author_email`, `body` (1–4000),
`created_at`; index (`org_id`, `group_id`, `created_at`). Yazarı veya bir admin/owner siler.

Migration ayrıca bir grubun etkinliği için `audit_log_target_idx` (`org_id`, `target_type`, `target_id`, `created_at DESC`) ekler ve
`alert_rules_type_check`'i `apm_error` ile genişletir (ve `oql`, böylece paralel expand
migration'larının sırası önemli olmaz).

## Dashboard'lar (`0020_dashboards`) {#dashboards-0020-dashboards}

OQL widget'larından oluşan özel dashboard'lar ([api.md](/tr/contracts/api#dashboards), [oql.md](/tr/contracts/oql), kod: `internal/dashboard`, D-064).

| Tablo | Anahtar | İçerik |
|---|---|---|
| `dashboards` | `id` | `org_id` (cascade), `name` (1–200), `description` (≤ 2000), `visibility` (`org`·`private`), `variables` (jsonb: `[{name, label, type, query, values, default, multi, include_all}]`), `version` (her kayıtta +1), `created_by`/`updated_by` (SET NULL), timestamp'ler. Index `(org_id, lower(name))` |
| `dashboard_pages` | `id` | `dashboard_id` (cascade), `position` (dashboard başına unique), `name` (1–100) |
| `dashboard_widgets` | `id` | `dashboard_id`, `page_id` (ikisi de cascade), `position` (sayfa başına unique), `title` (≤ 200), `visualization` (`line`·`area`·`bar`·`table`·`billboard`·`pie`·`heatmap`·`markdown`), `x`/`y`/`w`/`h` (12 sütunlu grid: `0 ≤ x ≤ 11`, `1 ≤ w ≤ 12`, `x + w ≤ 12`, `0 ≤ y ≤ 10000`, `1 ≤ h ≤ 50`), `query` (≤ 8192), `markdown` (≤ 20000), `unit`, `thresholds` (jsonb `[{value, severity}]`), `options` (jsonb `{stacked, legend}`) |

Kayıt tüm belgeyi tek transaction'da değiştirir: dashboard satırı `FOR UPDATE` ile kilitlenir, `version`'ı
istemcinin okuduğu sürümle karşılaştırılır (uyuşmazlık → `409`), sonra sayfalar (ve cascade ile widget'lar) silinir ve korunan id'lerle
yeniden insert edilir. Tek dashboard okumaları repeatable-read, salt okunur bir transaction kullanır. Görünürlük SQL'de filtrelenir
(`visibility = 'org' OR created_by = viewer OR (created_by IS NULL AND admin)`). Audit: `dashboard.{create,update,delete,duplicate,import}`.
`0021_alert_oql`, `alert_rules.type`'a `oql` ekler ([alerting.md](/tr/contracts/alerting) §2.10).

Sürüm geçmişi, paylaşım ve zamanlanmış raporlar (`0053_dashboard_versions`, `0054_dashboard_shares`,
`0055_dashboard_reports`; [api.md](/tr/contracts/api#dashboards), D-086, D-087):

| Tablo | Anahtar | İçerik |
|---|---|---|
| `dashboard_versions` | `(dashboard_id, version)` | `dashboard_id` (cascade), `document` (sayfa ve widget id'leriyle jsonb `{name, description, visibility, variables, pages}`), `author_id` (SET NULL), `restored_from`, `created_at`. Dashboard başına en yeni 50 tutulur (kaydeden transaction'da temizlenir); 0053 var olan her dashboard'un geçerli sürümünü doldurur |
| `dashboard_org_settings` | `org_id` | `shares_enabled` (varsayılan false), `report_domains` (text[] ≤ 20), `updated_by` (SET NULL), `updated_at`. Satır yok = varsayılanlar |
| `dashboard_shares` | `id` | `org_id`, `dashboard_id` (ikisi de cascade), `token_hash` (token'ın SHA-256'sı, unique), `label` (≤ 100), `time_range` (göreli) veya `range_from`/`range_to` (sabit; CHECK tam olarak biri), `variables` (jsonb), `created_by`/`revoked_by` (SET NULL), `created_at`, `expires_at`, `revoked_at`, `last_used_at`, `use_count`. Index `(dashboard_id, created_at DESC)` |
| `dashboard_reports` | `id` | `org_id`, `dashboard_id` (ikisi de cascade), `name` (≤ 100), `frequency` (`daily`·`weekly`), `weekday` (0 = Pazar), `hour`, `minute`, `timezone`, `recipients` (text[] ≤ 20; `0062_report_recipients_cleanup` boş listeye izin verir, API 1–20 ister), `language` (`en`·`tr`), `time_range`, `variables` (jsonb), `enabled`, `created_by` (SET NULL), timestamp'ler. Etkin olanlar üzerinde kısmi index. Bir üyeyi kaldırmak (`internal/store/postgres` `RemoveMemberWithCleanup`, API ve SCIM) üyeliğin transaction'ında kullanıcının adresinin `array_remove`'unu çalıştırır ve boş kalan raporları devre dışı bırakır (D-096) |
| `dashboard_report_runs` | `(report_id, period)` | `report_id` (cascade), `period` (yerel tarih `YYYY-MM-DD`), `status` (`running`·`sent`·`partial`·`failed`·`skipped`), `error` (≤ 1000), `recipients` (gönderilen sayısı), `started_at`, `finished_at`; raporun yeni bir çalıştırması üstlenildiğinde 90 günden eski çalıştırmalar silinir |

Her kayıt, sürüm satırını belgeyle aynı transaction'da insert eder. Paylaşım bağlantısı araması organizasyonu
(tenant id), `dashboard_org_settings.shares_enabled` ve oluşturanın üyeliğini join eder; `last_used_at`/`use_count`
bağlantı başına dakikada en fazla bir kez güncellenir. Paylaşım bağlantısı veya rapor oluşturmak, dashboard başına limitleri uygulamak için dashboard satırını `FOR UPDATE` ile kilitler.
Rapor job'u göndermeden önce `(report_id, period)`'u `INSERT … ON CONFLICT DO NOTHING` ile üstlenir.
Audit: `dashboard.{restore,settings.update,share.create,share.revoke,share.access,report.create,report.update,report.delete}`.

## Alerting (`0004_alerting`, `0088_alert_routing`) {#alerting-0004-alerting-0088-alert-routing}

Kurallar, değerlendirme durumu, incident'lar, kanallar, routing kuralları, mute'lar ve bildirim outbox'ı ([alerting.md](/tr/contracts/alerting), API:
[api.md](/tr/contracts/api#alerting), kod: `internal/alert/pgstore_*.go`).

| Tablo | Anahtar | İçerik |
|---|---|---|
| `alert_rules` | `id` | `org_id`, `name`, `description`, `type` (`0006_alerting_m2`'den beri `apm_no_data` izinli), `severity`, `enabled`, `interval_seconds`, `for_seconds`, `recovery_for_seconds`, `condition` (jsonb, doğrulanmış kanonik biçim), `renotify_interval_seconds`, `flapping` (jsonb), `runbook_url`, `labels` (jsonb), `version` (her değişiklikte +1), `created_by`, `updated_by`, timestamp'ler |
| `alert_rule_channels` | (`rule_id`, `channel_id`) | kural → kanal (ikisi de cascade) |
| `alert_channels` | `id` | `org_id`, `name`, `type` (`slack`·`email`·`webhook`·`teams`, artı `0088_alert_routing`'den beri `pagerduty`·`opsgenie`), `enabled`, `config` (jsonb, gizli olmayan: e-posta alıcıları/SMTP override'ı, `pagerduty` ve `opsgenie` bölümleri), `secrets` (secret JSON'un `ol1:<key id>:<AES-256-GCM>` değeri: `url`, `hmac_secret`, `smtp_password`, `routing_key`, `api_key`; AAD `org_id/channel_id`), `secrets_key_id`, `secret_hints` (maskeli), `created_by`, timestamp'ler |
| `alert_evaluators` | `instance_id` | evaluator heartbeat'leri (`last_seen`, veritabanı saati); 1 saatten eski satırlar temizlenir |
| `alert_rule_leases` | `rule_id` | `org_id`, `owner` (`''` = boşta), `lease_until` (`-infinity` = bırakılmış), `claimed_at`, `next_eval_at`, `last_eval_end`, `last_evaluated_at`, `last_result`, `last_error`, `last_duration_ms`. Kural başına bir satır (kuralla oluşturulur; eksik satırları evaluator'lar ekler) |
| `alert_series_state` | (`rule_id`, `series_key`) | ok olmayan seriler (ve flapping geçmişi olan ok seriler): `labels`, `state`, `pending_since`, `firing_since`, `recovering_since`, `last_value`, `last_seen_at`, `incident_id`, `transitions` (timestamptz[] ≤ 10), `last_notified_at`, `renotify_count` |
| `alert_incidents` | `id` | `org_id`, `rule_id` (silmede SET NULL), `rule_name`/`rule_type`/`severity` snapshot'ı, `series_key`, `labels`, `summary`, `state` (`open`·`acknowledged`·`resolved`), `value`, `last_value`, `threshold`, `channel_ids` (açılışta bildirilen uuid[]), `flapping`, `opened_at`, `acknowledged_at/by`, `resolved_at/by`, `resolve_reason`. Kısmi unique index `alert_incidents_open_uniq (rule_id, series_key) WHERE state <> 'resolved'` |
| `alert_incident_events` | `id` (identity) | zaman çizelgesi: `incident_id`, `at`, `kind`, `actor_user_id`, `actor_email`, `message` (≤ 4000), `details` |
| `alert_mutes` | `id` | `org_id`, `name`, `comment`, `starts_at`, `ends_at`, `rule_ids` (uuid[], boş = tümü), `matchers` (jsonb), `schedule` (jsonb, `0006_alerting_m2`; NULL = tek seferlik; tekrarlayan mute'lar geçerli veya sonraki oluşumu `starts_at`/`ends_at` içinde tutar, dispatcher'lar ileri alır), `created_by`, timestamp'ler |
| `alert_holiday_calendars` | `id` | `0015_alert_holiday_calendars`: `org_id`, `name` (organizasyon başına unique), `description`, `dates` (text[], `YYYY-MM-DD` veya `MM-DD`), `created_by`, timestamp'ler; `alert_mutes.schedule->holiday_calendar_ids` üzerinden id ile referans verilir (FK yok: referans varken silme reddedilir) |
| `alert_routing_rules` | `id` | `0088_alert_routing`: sıralı routing kuralları (alerting.md §5.6): `org_id`, `name`, `"position"` (0 ilk), `enabled`, `is_default` (kısmi unique index `alert_routing_rules_default_uniq (org_id) WHERE is_default`), `match` (jsonb: severity'ler, servisler, kural türleri, label eşleştiricileri, yerel zaman penceresi), `created_by`, timestamp'ler |
| `alert_routing_rule_channels` | (`route_id`, `channel_id`) | routing kuralı → kanal (ikisi de cascade) |
| `alert_notifications` | `id` (+ sıralama için `seq` identity) | outbox: `org_id`, `incident_id`, `rule_id`, `channel_id` (SET NULL), `channel_type`, `kind` (`opened`·`resolved`·`renotify`·`test`, artı `0088_alert_routing`'den beri `acknowledged`), `idempotency_key` (UNIQUE), `payload` (jsonb olay), `status` (`pending`·`sending`·`delivered`·`failed`·`suppressed`), `attempts`, `next_attempt_at`, `claimed_by`, `claimed_until`, `muted_logged`, `last_error`, `created_at`, `finished_at`. 30 günden eski bitmiş satırlar temizlenir |
| `alert_delivery_attempts` | `id` (identity) | teslimat logu: `notification_id` (cascade), `attempt`, `instance_id`, `started_at`, `duration_ms`, `success`, `status_code`, `error` (≤ 2000) |

**Eşzamanlılık.** Lease'ler `SELECT … FOR UPDATE SKIP LOCKED` ile üstlenilir ve sahibi tarafından yenilenir; her değerlendirme
commit'i kuralın lease satırını `FOR UPDATE` ile kilitler ve seri durumunu, incident'ları, olayları ve outbox satırlarını aynı transaction'da yazmadan önce owner, süre, `last_eval_end` ve kural `version`'ını
(`FOR SHARE`) kontrol eder. Bir kural veya incident üzerindeki API değişiklikleri önce aynı lease satırını kilitler (aynı kilit sırası), yani
değerlendirmelerle sıraya girer. Dispatcher'lar outbox satırlarını `FOR UPDATE SKIP LOCKED` ile üstlenir ve koşullu olarak bitirir (`status = 'sending' AND claimed_by = me`);
süresi dolmuş üstlenmeler `pending`'e döner. Audit: `alert.rule.*`, `alert.channel.*`, `alert.mute.*`,
`alert.routing_rule.{create,update,delete,reorder}`, `alert.holiday_calendar.*`,
`alert.incident.{acknowledge,resolve}` (detaylar: adlar, `condition_changed`, test gönderimlerinin `success` değeri).

## Sürümler ve güncellemeler (`0003_component_heartbeats`) {#versions-and-updates-0003-component-heartbeats}

### `component_heartbeats` {#component-heartbeats}
`(component, instance_id)` primary key, `version`, `started_at`, `last_seen` (veritabanı saati). Her ingest, processor, api ve allinone süreci
30 sn'de bir upsert eder (`component` = binary adı, `instance_id` = host adı +
rastgele sonek, yani yeniden başlatma yeni bir satırdır); düzgün kapanışta silinir; 24 saat görülmeyen satırlar temizlenir.
`openlog-migrate` okur (canlı = `last_seen` 5 dakika içinde). `last_seen` üzerinde index.

### `system_state` {#system-state}
`key` primary key, `value jsonb`, `updated_at`. Küçük, cluster çapında belgeler:

| key | Yazan | İçerik |
|---|---|---|
| `update_check` | api leader'ı (advisory lock `postgres.LeaderLock` = `0x6f6c2d6c656164`, diğer leader job'larıyla paylaşılır) | `{channel, checked_at, last_success_at, error, latest: {version, notes_url, released_at}}` |
| `updater` | `openlog-updater` | GET /api/v1/version'ın `UpdaterStatus` değeri |
| `updater_poll` | Compose `openlog-updater`, 30 sn'de bir | `{engine, mode, poll_seconds, polled_at}` (`update_requests.updater_listening`) |

### `update_requests` (`0009_update_requests`) {#update-requests-0009-update-requests}
Arayüzden `openlog-updater`'a "Check now" / "Update now" (releases-updates.md §5.1, D-041). `id` uuid, `action`
(`check|apply`), `target_version`, `ignore_maintenance_window`, `state` (`pending|running|done|failed|expired`),
`message` (≤ 2000), `org_id` ve `requested_by` (silmede SET NULL), `requested_by_email`, `requested_at`,
`picked_at`, `finished_at`. Index'ler: `requested_at DESC`; updater'ın poll'u için kısmi `(requested_at) WHERE state = 'pending'`.
Insert'ler `pg_advisory_xact_lock(0x6f6c2d7570647271)` ile sıraya sokulur (rate limit: aksiyon başına 30 sn'de bir istek; bir açık
`apply`). Tablo küçük kalır (admin aksiyonu başına birkaç satır); satırlar temizlenmez.

Updater olayları ayrıca `audit_log`'a `org_id NULL`, `actor_email = 'openlog-updater'`,
`action` `updater.update_{available,started,succeeded,failed,rolled_back,rollback_failed}`,
`target_type = 'openlog_release'`, `target_id` = hedef sürüm ile yazılır.

## Kullanım, planlar ve faturalama (`0035_usage_plans`) {#usage-plans-and-billing-0035-usage-plans}

Plan tanımları yapılandırmadır (`OPENLOG_PLANS`, [usage.md](/tr/contracts/usage) §4); bu tablolar atamaları ve
kota değerlendirmesi, bildirimler, faturalama push'ları ve tenant başına retention kayıtlarını tutar (D-079–D-081).

### `org_plans` {#org-plans}
Açık bir ataması olan organizasyon başına bir satır (yoksa = `OPENLOG_DEFAULT_PLAN`): `plan_id` (katalog id'si; bilinmeyen id'ler
varsayılan plana döner), `overrides` jsonb (kısmi limitler, `quota.Overrides`), `billing_provider`,
`billing_customer_id`, `billing_subscription_id` (bağlanana kadar boş; provider + customer üzerinde kısmi index),
`note`, `updated_by`, `updated_at`. Yalnızca superadmin'ler (`PUT /api/v1/admin/orgs/{org}/plan`) ve faturalama
webhook'ları yazar, her değişiklik `plan.update` audit aksiyonuyla (hedef `organization`).

### `org_query_limits` (`0051_org_query_limits`) {#org-query-limits-0051-org-query-limits}
ClickHouse sorgu limitlerinin organizasyon katmanı ([usage.md](/tr/contracts/usage) §4.5). PK `org_id` (cascade):
`max_memory_usage`, `max_rows_to_read`, `max_bytes_to_read` (bigint `>= 0`; NULL = plandan/varsayılanlardan devral, `0` = openlog tarafından
ayarlanmadı), `updated_by`, `updated_at`. Satır yok = hiçbir şey ayarlı değil; her değeri temizlemek satırı siler.
`PUT`/`DELETE /api/v1/usage/query-limits` üzerinden `query_limits.update` / `query_limits.delete` audit aksiyonlarıyla yazılır; her api ve alert
pod'u 30 sn'de bir okur.

### `tenant_quota_status` {#tenant-quota-status}
Tenant başına son değerlendirme (PK `tenant_id`), api leader'ı her `OPENLOG_USAGE_EVALUATION_INTERVAL` aralığında upsert eder:
`org_id`, `plan_id`, `period_start`, `level` (`ok`/`warning`/`exceeded`), `ingest_bytes`, `ingest_limit_bytes`,
`ingest_blocked`, `rate_bytes_per_second`, `burst_bytes`, `metrics` jsonb, `evaluated_at`. Her ingest pod'u son bir saat içinde değerlendirilmiş,
bloklanmış veya rate limit'li satırları okur (`OPENLOG_QUOTA_REFRESH_INTERVAL`); arayüz banner'ı
organizasyonunun satırını okur. Silinen organizasyonların satırları cascade olur.

### `usage_notifications` {#usage-notifications}
PK `(org_id, period_start, metric, threshold)`: insert, bir faturalama dönemi için bir owner e-postasını üstlenir; e-posta gönderilemezse
bırakılır (silinir), gönderimden sonra `recipients`/`sent_at` güncellenir.

### `billing_usage_pushes` {#billing-usage-pushes}
PK `idempotency_key` (`openlog:<org_id>:<day>:<metric>`, sağlayıcıya da gönderilir): `provider`, `metric`, `day`,
`quantity`, `status` (`pending`/`pushed`/`failed`), `attempts`, `error`. Başarısız bir kayıt 5 denemeye kadar yeniden üstlenilir; 15 dakikadan eski
bekleyen bir üstlenme devralınır.

### `usage_retention_mutations` {#usage-retention-mutations}
PK `(table_name, partition_id, tenants_hash)`: tenant başına retention job'unun gönderdiği ClickHouse `ALTER … DELETE IN PARTITION`
mutation'ları (`retention_days`, `tenants`, `submitted_at`); aynı tenant kümesi için 24 saat içinde
yeniden gönderilmez; 30 günden eski satırlar temizlenir.

## SaaS operasyonları (`0065_saas_operator`) {#saas-operations-0065-saas-operator}

Operatör konsolu, organizasyon yaşam döngüsü, destek erişimi, abuse işaretleri ve katı host limitleri (D-105, D-106,
[saas.md](/tr/operations/saas) §8–§11). Tüm tablolar organizasyonlarıyla birlikte cascade olur. Eski binary'ler onları yok sayar.

### `org_saas_state` {#org-saas-state}
PK `org_id`; satır yok = aktif, trial yok, destek erişimi yok. Askıya alma: `suspended_at`, `suspended_by`, `suspend_reason`
(her ingest pod'u her `OPENLOG_QUOTA_REFRESH_INTERVAL` aralığında, her api pod'u 15 sn'de bir okur). Trial: `trial_plan_id`,
`trial_started_at`, `trial_ends_at`, `trial_ended_at` (leader yedek planı atadığında ayarlar; çalışan trial'lar üzerinde kısmi
index). Owner'ın verdiği destek erişimi: `support_access_until`, `support_access_granted_by`,
`support_access_granted_at`. `updated_at`.

### `trial_notifications` {#trial-notifications}
PK `(org_id, trial_ends_at, days_left)`: adım başına bir trial e-postasının üstlenilmesi (`days_left` 7/3/1, `0` = bitti); e-posta
gönderilemezse bırakılır. Trial uzatmak `trial_ends_at`'i değiştirir, yani hatırlatmalar yeni bitiş için yeniden gönderilir.

### `support_sessions` {#support-sessions}
`id`, `org_id`, `operator_user_id`, `operator_email`, `auth_session_id` (operatörün `sessions` satırı, cascade: çıkış
yapmak destek görünümünü bitirir), `reason`, `started_at`, `expires_at`, `ended_at`. Index `(org_id, started_at DESC)`.

### `abuse_flags` {#abuse-flags}
`id` identity, `org_id`, `kind` (`ingest_spike`, `new_org_hosts`, `ingest_source_ips`), `status`
(`open`/`dismissed`/`actioned`), `details` jsonb, `occurrences`, `first_seen_at`, `last_seen_at`, `auto_suspended`,
`resolved_by`, `resolved_by_email`, `resolved_at`, `resolution_note`. Unique kısmi index: `(org_id, kind)` başına bir açık
işaret; dedektör satır eklemek yerine onu yeniler.

### `tenant_host_limits`, `tenant_known_hosts` {#tenant-host-limits-tenant-known-hosts}
Geçerli planı host sınırlayan tenant'lar için api leader'ı yazar (`OPENLOG_SAAS_HOST_SYNC_INTERVAL`):
`tenant_host_limits` (PK `tenant_id` → `organizations.tenant_id`): `host_limit`, `active_hosts`, `evaluated_at` (bir saatten eski
satırlar uygulanmaz); `tenant_known_hosts` (PK `(tenant_id, host_id)`): bugün veya dün aktif host'ların `last_seen` tarihi
(eski satırlar silinir). Ingest pod'ları ikisini de yükler.

### `ingest_source_counts` {#ingest-source-counts}
PK `(tenant_id, hour, instance)`: `distinct_ips`, ingest instance'ı ve saat başına kimliği doğrulanmış OTLP isteklerinin farklı istemci adresi sayısı
(adreslerin kendisi asla saklanmaz), `updated_at`. Abuse dedektörü okur (instance'lar arasında
maksimum); 48 saatten eski satırlar silinir.

## Veri sahibi talepleri (`0070_data_subject_requests`) {#data-subject-requests-0070-data-subject-requests}

Veri dışa aktarımları, organizasyon soft silme ve hard silme kayıtları, silme sertifikaları (D-107,
[saas.md](/tr/operations/saas) §12).

### `organizations.deleted_at` {#organizations-deleted-at}
Bir owner veya operatör organizasyonun silinmesini zamanladığında ayarlanır, iptal temizler. Ayarlıyken store
organizasyonu üyeliklerden gizler (session'lar sonraki istekte erişimi kaybeder), organizasyonun API key'leri kimlik
doğrulamaz, lisans anahtarlarını ingest bulamaz, alert kuralları değerlendirme için üstlenilmez ve zamanlanmış raporlar
gönderilmez. Null olmayan değerler üzerinde kısmi index.

### `data_exports` {#data-exports}
Dışa aktarma job'ları. `kind` `organization` (owner'lar; `org_id` cascade, `user_id` = talep eden, SET NULL) veya `user` (kişisel;
`user_id` = özne). `status` `pending` → `running` → `completed` | `failed`; `expires_at`
geçtiğinde `completed` → `expired` (arşiv silinir, `object_key` ve `download_token_hash` temizlenir). `signals` (`logs`, `traces`,
`metrics`), `range_from`/`range_to`, `locale` (e-posta), `storage` (`local`/`s3`), `object_key`
(`exports/<kind>/<org or user id>/<id>.zip`), `size_bytes`, `telemetry_rows`, `truncated`, `manifest` jsonb (arşivin
manifest.json'u), `download_token_hash` (e-postayla gönderilen bağlantı token'ının sha256'sı, unique), `attempts`, `error`
(talep eden için güvenli), `created_at`, `started_at`, `heartbeat_at`, `completed_at`, `expires_at`. Api leader'ı
en eski bekleyen satırı `FOR UPDATE SKIP LOCKED` ile üstlenir; 5 dakika heartbeat'siz çalışan satır yeniden denenir
(3 deneme). Oluşturma, organizasyon/kullanıcı başına `pg_advisory_xact_lock(hashtext('data_export:…'))` ile sıraya sokulur:
aynı anda bir bekleyen veya çalışan dışa aktarma ve 24 saatte en fazla 5. Başarısız ve süresi dolmuş satırlar 90 gün sonra temizlenir.

### `org_deletions` {#org-deletions}
Zamanlama başına bir satır: `org_id` (organizasyon gidince SET NULL), `tenant_id`, `status` `scheduled` →
`cancelled` | `deleting` → `completed`, `initiator` (`owner`/`operator`), `reason` (operatörler), `requested_by`,
`requested_by_email`, `notify` jsonb (tamamlanma e-postası için organizasyon adı ve owner adresleri),
`revoked_license_key_ids` / `revoked_scim_token_ids` (zamanlamanın iptal ettiği ve iptalin geri yüklediği anahtarlar),
`requested_at`, `purge_after` (= requested_at + grace), `cancelled_at`, `started_at`, `completed_at`, `progress` jsonb
(önceki ClickHouse satır sayıları, tablo ve partition başına gönderilen mutation'lar, kalan satırlar, `verified`), `attempts`,
`last_error`, `certificate_id`. Unique kısmi index: organizasyon başına bir `scheduled`/`deleting` satırı. Silme
tamamlandığında `requested_by`, `requested_by_email`, `reason` ve `notify` temizlenir. Leader `purge_after`'dan sonra başlattığında silme `deleting` olur (artık
iptal edilemez).

### `license_key_tombstones` (`0075_license_key_tombstones`) {#license-key-tombstones-0075-license-key-tombstones}
Organizasyon silmesiyle iptal edilen lisans anahtarları; böylece ingest `401` yerine `403 org_deleted` yanıtlar (D-115):
`key_hash` (primary key; zamanlama anındaki `license_keys.key_hash`, bir hash, kişisel veri yok), `reason`
(`org_deleted`), `org_deletion_id` (foreign key yok; `org_id` sütunu değil, yani purge satırı korur), `created_at`,
`expires_at`. Zamanlama, iptal ettiği anahtarlar için yazar (kullanıcıların daha önce iptal ettiği anahtarlar için yazılmaz), iptal
siler, purge `expires_at` = purge zamanı + 365 gün verir; purge süresi dolmuş satırları da siler. Lisans
anahtarı araması ona yalnızca aday hash ile eşleşen aktif anahtar yoksa bakar (D-044 adayları, `license_keys` ile aynı) ve
o hash silinmemiş bir organizasyonun anahtarına aitken yok sayar.

### `deletion_certificates` {#deletion-certificates}
Tamamlanmış bir hard silmenin kişisel veri içermeyen kanıtı, süresiz tutulur: `subject_type` (`organization`/`user`),
`subject_hash` (tenant id veya user id'nin hex sha256'sı), `initiator` (`owner`/`operator`/`self`), `requested_at`,
`grace_ended_at`, `started_at`, `completed_at`, `postgres_rows` jsonb (tablo başına silinen veya pseudonimleştirilen satırlar; organizasyonlar
için ayrıca `users_deleted`: hiçbir organizasyonu kalmayan hesaplar), `clickhouse_rows` jsonb (silmeden önce ClickHouse tablosu başına tenant'ın
satırları), `verified` (her ClickHouse tablosu yeniden sayıldı, sıfır satır). Superadmin'ler listeler
(`GET /api/v1/admin/deletion-certificates`).

### Hesap silme {#account-deletion}
`users` satırını tek transaction'da `DELETE` eder (cascade: üyelikler, session'lar, `sso_sessions`, `sso_login_states`,
`email_verifications`, `scim_users`, `scim_group_members`), adrese yapılmış `invitations`'ı, kullanıcının oluşturduğu API key'lerini ve
kişisel `data_exports`'u siler, adresi `dashboard_reports.recipients` (alıcısı kalmayan raporlar
devre dışı bırakılır) ve `sso_domains.email_address`'ten kaldırır, tutulan kayıtlardaki kullanıcıyı rastgele
sabit bir pseudonimle değiştirir: `deleted-user-<10 hex>`: `audit_log` (`actor_email`, `actor_user_id` NULL, `ip` temizlenir, `user` hedeflerinin
`target_id`'si, `details` içindeki adres ve id geçişleri), `alert_incident_events.actor_email`,
`apm_error_group_comments.author_email`, `update_requests.requested_by_email`, `org_deletions.requested_by_email`.
Kullanıcı, silinmesi zamanlanmamış bir organizasyonun tek owner'ıyken reddedilir.

## Durum sayfası (`0071_status_page`) {#status-page-0071-status-page}

Public durum sayfası verisi (D-108); tenant verisi yok.

### `status_incidents` {#status-incidents}
`kind` (`incident`/`maintenance`), `title`, `status` (incident: `investigating`, `identified`, `monitoring`,
`resolved`; maintenance: `scheduled`, `in_progress`, `completed`), `impact` (`none`, `minor`, `major`, `critical`),
`components` text[] (`ingest`, `query_api`, `alerting`, `processing`), `starts_at`, `ends_at` (bakım sonu veya
çözülme zamanı, `resolved`/`completed` olunca ayarlanır), `created_by` (SET NULL), `created_at`, `updated_at`. Superadmin'ler yazar
(`org_id NULL` ile audit aksiyonları `status.incident_create`, `status.incident_update`, `status.incident_delete`).

### `status_incident_updates` {#status-incident-updates}
Zaman çizelgesi girdileri: `incident_id` (cascade), `status`, `message` (1–5000 karakter), `created_at`.

### `status_checks_daily` {#status-checks-daily}
PK `(component, day)`: `checks`, `operational` (çalışıyor veya bakımda), `degraded`, `outage` (kısmi veya
büyük kesinti). Api leader'ı bileşen başına dakikada bir check ekler; uptime = `(checks - outage) / checks`. 400 günden eski
satırlar temizlenir. En güncel snapshot, `system_state` belgesi `status_page`'dir
(`checked_at`, `components`, `processing_lag_seconds`).

## Kayıtlı görünümler (`0080_saved_views`) {#saved-views-0080-saved-views}

Kayıtlı Logs/Metrics Explorer görünümleri ([api.md](/tr/contracts/api#saved-views), kod: `internal/savedview`, D-118).

| Tablo | Anahtar | İçerik |
|---|---|---|
| `saved_views` | `id` | `org_id` (cascade), `signal` (`logs`·`metrics`·`traces`), `name` (1–200), `description` (≤ 2000), `visibility` (`private`·`org`), `state` (jsonb nesnesi ≤ 64 KiB; API ≤ 32 KiB kabul eder: filtreler, gruplar, sütunlar, zaman aralığı …), `created_by` (SET NULL), `created_at`, `updated_at`. Index `(org_id, signal, lower(name))` |

Görünürlük SQL'de filtrelenir (`visibility = 'org' OR created_by = viewer OR (created_by IS NULL AND admin)`). Organizasyon başına
500 görünüm limiti insert statement'ında kontrol edilir (`INSERT … SELECT … WHERE count < 500`; eşzamanlı oluşturmalar
birkaç satır aşabilir). Audit: `saved_view.{create,update,delete}`.

## Boyutlandırma ve operasyon {#sizing-and-operations}

- Bağlantılar: her ingest ve api pod'u en fazla `OPENLOG_POSTGRES_MAX_CONNS` (varsayılan 10) bağlantılık bir pool açar.
  Ingest PostgreSQL'i yalnızca lisans anahtarı cache miss'lerinde sorgular, yani kararlı durum yükü yaklaşık
  `(distinct keys × pods) / OPENLOG_AUTH_CACHE_TTL` indeksli aramadır.
- API, kimliği doğrulanmış istek başına 2–3 indeksli sorgu yapar (session veya API key, üyelik).
- Yedekler: PostgreSQL erişim kontrolünün kayıt sistemidir; yedeğini al (CloudNativePG: `Cluster` üzerinde
  `backup` yapılandır). Onu kaybetmek telemetriyi kaybettirmez ama tüm anahtarların ve kullanıcıların yeniden oluşturulması gerekir.
