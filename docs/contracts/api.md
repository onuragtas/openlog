# Contract: Query and management API (M1)

Base path `/api/v1`. JSON responses. Times accepted as RFC3339 or unix milliseconds; returned as RFC3339 with nanoseconds (UTC).
Machine-readable spec: [openapi.yaml](openapi.yaml). Data model: [postgres.md](postgres.md).

Errors: HTTP status + `{"error": {"code": "invalid_argument", "message": "…"}}`. Codes: `invalid_argument` (400),
`unauthenticated` (401), `permission_denied` (403), `not_found` (404), `already_exists` (409),
`failed_precondition` (409), `resource_exhausted` (429, with `Retry-After`), `internal` (500),
`unavailable` (503, with `Retry-After`), `storage_unavailable` (503, with `Retry-After` and `"retryable": true`: a
telemetry query could not read data from storage, typically parts on S3 after tiered storage; retry later or query a
more recent range, [config.md](config.md) "ClickHouse read-only user and per-tenant query limits"), `timeout` (504).

## Authentication

`OPENLOG_AUTH_MODE=postgres` (default). Every endpoint except the public ones below needs one of:

| Credential | How | Acts as | Notes |
|---|---|---|---|
| Session | cookie `openlog_session` from `POST /auth/login` | the user's role in the selected organization | `HttpOnly`, `SameSite=Strict`, `Path=/api`, `Secure` (unless `OPENLOG_COOKIE_SECURE=false`). Server-side in PostgreSQL; ends after `OPENLOG_SESSION_TTL` or `OPENLOG_SESSION_IDLE_TIMEOUT` of inactivity, on logout, on revocation, or when the password changes (other sessions) |
| API key | `Authorization: Bearer ola_…` | `viewer` of the key's organization | Read-only: telemetry endpoints, `GET /auth/me`, `GET /orgs/current`. Every other management endpoint answers `403`. Revocation is immediate |

Ingest license keys (`olk_…`) are **not** API credentials, and API keys are not ingest credentials.

**CSRF.** A cookie-authenticated request with a method other than `GET`/`HEAD`/`OPTIONS` must send the session's
token in `X-CSRF-Token` (returned as `csrf_token` by `/auth/login`, `/auth/signup`, `/invitations/accept` and
`GET /auth/me`); otherwise `403 permission_denied`. Bearer requests do not need it. Endpoints with a JSON body
require `Content-Type: application/json` (`400` otherwise), which also protects the unauthenticated sign-in
endpoints against cross-site form posts. The API never sends CORS headers.

**Organization.** The tenant of every telemetry query is taken **only** from the authenticated principal and
injected by the query layer; no endpoint accepts a tenant parameter. A user who belongs to several organizations
selects one per request with the header `X-Openlog-Org-Id: <organization id>`; without it the user's oldest
membership is used. A header naming an organization the user is not a member of → `403`. For API keys the header
is optional and must match the key's organization. A signed-in user without any membership gets `403` on
telemetry endpoints.

**Rate limiting.** Failed password checks (sign-in, invitation acceptance by an existing user, password change)
are counted per (email, client IP) in PostgreSQL; after `OPENLOG_LOGIN_MAX_FAILURES` within `OPENLOG_LOGIN_WINDOW`
the request gets `429 resource_exhausted`, even with the right password. The client IP is the TCP peer, or the
right-most untrusted `X-Forwarded-For` hop when the peer is in `OPENLOG_API_TRUSTED_PROXIES`. Unknown emails and
wrong passwords return the same `401` with equal timing.

`OPENLOG_AUTH_MODE=static` (development/tests): the license keys of `OPENLOG_LICENSE_KEYS` authenticate the query
endpoints (`openlog-license-key` header or `Authorization: Bearer`) as `viewer`; only `GET /auth/config` and
`GET /auth/me` exist besides the telemetry endpoints (all other paths below → `404`).

## Roles

| Operation | viewer | member | admin | owner |
|---|:-:|:-:|:-:|:-:|
| Telemetry, `GET /orgs/current`, `GET /members`, `GET /fleet/*`, `GET /onboarding`, own sessions, leave organization | ✓ | ✓ | ✓ | ✓ |
| `GET /license-keys`, `GET /api-keys`, `POST /api-keys`, revoke own API keys | | ✓ | ✓ | ✓ |
| Alerting reads (`GET /alerts/*`) and rule preview | ✓ | ✓ | ✓ | ✓ |
| Create alert rules and mutes, change/delete **own** rules and mutes, acknowledge/resolve/annotate incidents | | ✓ | ✓ | ✓ |
| APM error inbox: change status/assignee (`PATCH /apm/errors/groups`), comment, delete **own** comments (signed-in users only) | | ✓ | ✓ | ✓ |
| Delete any error group comment | | | ✓ | ✓ |
| `GET /integrations/settings` | ✓ | ✓ | ✓ | ✓ |
| OQL (`POST /query`, `POST /query/validate`, `GET /query/schema`), read dashboards (private ones: creator only), export | ✓ | ✓ | ✓ | ✓ |
| Create, import and duplicate dashboards; change/delete **own** dashboards | | ✓ | ✓ | ✓ |
| Change/delete any non-private dashboard | | | ✓ | ✓ |
| `PATCH /orgs/current`, invitations, create/revoke license keys, revoke any API key, change roles/remove members (not owners), `GET /audit-log`, fleet changes (`PUT /fleet/policy`, host overrides, pause/resume, deploy now, rollback), integration setting changes, any alert rule or mute, alert channels and test sends, `POST /version/check` and `POST /version/update` (signed-in users only; refused for everyone when `OPENLOG_SIGNUP_ENABLED=true`) | | | ✓ | ✓ |
| Grant or remove the owner role, invite owners, remove owners | | | | ✓ |

An organization always keeps at least one owner (`409 failed_precondition`).

## Auth endpoints

### `GET /api/v1/auth/config` (public)
`{"mode": "postgres", "signup_enabled": false, "password_min_length": 8, "email_enabled": false,
"email_verification_required": false, "captcha": null | {"provider": "turnstile|hcaptcha", "site_key"}}`.
`email_enabled`: invitation and verification e-mails are sent (SMTP and `OPENLOG_PUBLIC_URL` configured).
`captcha` only while sign-up is enabled.

### `POST /api/v1/auth/login` (public)
Body `{"email": "…", "password": "…"}`. `200` sets the session cookie and returns the **Me** object. `401` for any
wrong email/password combination, `429` when rate limited.

### `POST /api/v1/auth/signup` (public, `OPENLOG_SIGNUP_ENABLED=true`)
Body `{"email", "password", "name", "organization_name", "captcha_token"?}` → `201` + cookie + **Me**. Creates a new
organization (generated tenant id) owned by the new user. `403` when disabled, `409` when the email exists.
Abuse protection (D-045), in this order: `400` for invalid input (password policy 8–256 characters, not equal to
the e-mail); `429 resource_exhausted` after `OPENLOG_SIGNUP_MAX_PER_IP` attempts per client IP or
`OPENLOG_SIGNUP_MAX_PER_EMAIL` per address within `OPENLOG_SIGNUP_RATE_WINDOW`; with a CAPTCHA provider `400` when
`captcha_token` is missing or rejected (`503` when the provider cannot be reached); `400` for a blocked e-mail
domain. With `email_verification_required` the new user is signed in but `user.email_verified` is `false` and a
verification e-mail is sent (a failed send does not fail the sign-up; resend below). Until verified,
`POST /license-keys`, `POST /api-keys`, `POST /invitations` and invitation resends answer `403 permission_denied`
("confirm your e-mail address first"); everything else works.

### `POST /api/v1/auth/verify-email` (public) `{"token"}`
Token from the verification link (`/verify-email#token=olv_…`) → `204`; `404` when invalid, used or expired
(`OPENLOG_EMAIL_VERIFICATION_TTL`). Audit `user.email_verified`.

### `POST /api/v1/auth/verify-email/resend`
Signed-in, unverified user → `204` (new link; earlier links stay valid until they expire). `409` when already
verified or e-mail is not configured, `429` after 5 e-mails per hour, `503` when the e-mail cannot be sent.

### `GET /api/v1/auth/me`
**Me** object:
```json
{"auth": "session", "user": {"id": "…", "email": "ada@example.com", "name": "Ada", "email_verified": true, "language": "auto"},
 "organization": {"id": "…", "tenant_id": "default", "name": "Default"}, "role": "owner",
 "organizations": [{"id": "…", "tenant_id": "default", "name": "Default", "role": "owner"}],
 "csrf_token": "…"}
```
For API keys: `"auth": "api_key"`, `user`/`csrf_token` `null`, `organizations: []`, `role: "viewer"`.
`user.language` is the user's language preference: `"auto"` (the browser's) or `"en"`/`"tr"` (D-095).

### `PATCH /api/v1/auth/me` `{"language": "auto" | "en" | "tr"}`
Signed-in users set their own language for the web UI and for e-mails sent to them → `200` **Me**. The web UI starts in
that language instead of the browser's (Settings → Profile; with a preference set, the header language switch updates
it too). `"auto"` removes the preference and keeps the request's `Accept-Language` as the account's stored language.
Other values → `400`; API keys → `403`. Audit `user.language_change` (`details.from`/`to`, no organization).

### `POST /api/v1/auth/logout`
Revokes the session and clears the cookie → `204`.

### `POST /api/v1/auth/password`
Body `{"current_password", "new_password"}` → `204`; revokes the user's other sessions. Wrong current password → `403`.

## Organization and members

### `GET /api/v1/orgs/current` · `PATCH /api/v1/orgs/current` `{"name"?, "language"?}`
`{"id", "tenant_id", "name", "role", "created_at", "language"}` (`role` = caller's role). `language` is the organization's
default e-mail language: `""` (none), `"en"` or `"tr"` (D-095). `PATCH` (admin, owner) needs at least one field; audit
`org.rename`, `org.language_change` (`details.from`/`to`).

### `GET /api/v1/members`
`{"members": [{"user_id", "email", "name", "role", "joined_at"}]}`

### `PATCH /api/v1/members/{user_id}` `{"role"}` · `DELETE /api/v1/members/{user_id}`
`204`. A user may always remove themselves (leave).

Changes take effect **immediately on every API pod** (D-046): sessions are not cached — every request reads the
session and the membership/role of the selected organization from PostgreSQL — so the removed user's next request
with that organization gets `403` (without `X-Openlog-Org-Id` their next oldest membership is used, or none), and a
demoted user acts with the new role at once. Removing a member also revokes the API keys they created in that
organization (`details.revoked_api_keys` in the `member.remove` audit event). Their sessions stay valid for their
other organizations. In the same transaction as the membership (also for SCIM deactivation and deletion) their address
is removed from the organization's scheduled report recipient lists; a report left without recipients is disabled
(D-096). Audit `dashboard.report.recipient_remove` (`details.user_id`, `details.reports`, `details.disabled_reports`;
`details.via = "scim"`) and `details.report_recipient_removed` (count) on `member.remove`.

## Invitations

The inviter always gets a one-time token for the accept link `/invite#token=<token>` (web UI; the token stays in the
URL fragment). When e-mail is configured (`GET /auth/config` → `email_enabled`) the link is also e-mailed to the
invitee from `OPENLOG_SMTP_FROM`; `email_sent` in the response says whether that succeeded (a failed or rate-limited
e-mail never fails the request). Limits: 100 invitation e-mails per organization and 5 per invited address per hour.

**E-mail language.** Transactional e-mails (invitation, sign-up verification, SSO domain verification, usage
notifications) are rendered from `internal/mail/templates` as plain text + HTML in English or Turkish (D-085, D-095).
The language is the first of: (1) the recipient's language preference (`PATCH /auth/me`, when the address belongs to a
user), (2) the organization's default language (`PATCH /orgs/current`; organization e-mails only: invitations, domain
verification, usage notifications), (3) the stored request language — the supported language the `Accept-Language` of
the creating request prefers most (`q` values honoured): the inviter's for invitations (kept on resend, then the
resending admin's), the current request's for verification links and domain verification, and the account's language
at creation for usage notifications (`users.locale`, postgres.md) — and (4) English. Verification links are not
organization e-mails (preference, request language, account language). Scheduled dashboard reports use the report's
own `language`.

### `GET /api/v1/invitations?include_expired=` · `POST /api/v1/invitations` `{"email", "role"}` · `DELETE /api/v1/invitations/{id}`
List: `{"invitations": [{"id", "email", "role", "invited_by_email", "created_at", "expires_at", "expired", "last_sent_at", "send_count"}]}`
— pending invitations; with `include_expired=true` also expired ones that were neither accepted nor revoked.
Create → `201 {"invitation": {…}, "token": "oli_…", "email_sent": true}` (**token shown once**); `409` if the email
is already a member or has a pending invitation. Unverified sign-ups → `403`.

### `POST /api/v1/invitations/{id}/resend`
Issues a new token for an invitation that is neither accepted nor revoked (also an expired one), restarts its
validity (`OPENLOG_INVITATION_TTL`) and e-mails it when e-mail is configured → `200 {"invitation", "token", "email_sent"}`.
The previous link stops working. `404` when accepted, revoked or unknown; only owners resend owner invitations.
Audit `invitation.resend`.

### `POST /api/v1/invitations/lookup` (public) `{"token"}`
`{"organization_name", "email", "role", "expires_at", "user_exists"}`; `404` when invalid, used, revoked or expired.

### `POST /api/v1/invitations/accept` (public) `{"token", "password", "name"}`
New user: creates the account with `password` (policy: 8–256 characters). Existing user (`user_exists`): `password`
must be their current password. Adds the membership, signs in (cookie) and returns **Me** with the user's default
organization (send `X-Openlog-Org-Id` to switch).

## Ingest license keys

### `GET /api/v1/license-keys`
`{"license_keys": [{"id", "name", "prefix": "olk_1a2b3c4d", "custom": false, "created_by_email", "created_at", "last_used_at", "revoked_at"}]}`
(revoked keys included). `last_used_at` is updated at most once a minute and may lag by up to a minute per ingest pod.

### `POST /api/v1/license-keys` `{"name", "key"?}`
Without `key`: `201 {"license_key": {…, "custom": false}, "key": "olk_…"}` — **the key is shown only in this response**.

With `key` an existing value is **imported**, so senders that already use it (e.g. an `x-api-key` header configured
for another backend) keep working without being redeployed:
- Surrounding whitespace is trimmed; the value must be 16–256 characters of printable ASCII without spaces, quotes
  or backslashes (`[\x21-\x7e]` minus `"`, `'`, `\`), so it fits HTTP headers, gRPC metadata and `install.sh`.
  Otherwise `400 invalid_argument` (an empty `key` is also rejected; omit it to generate a key).
- Only `sha256(key)` and a display prefix revealing at most half of the value are stored.
- A value already used by **any** organization, active or revoked, fails with `409 already_exists`; the response
  does not say which organization has it.
- `201 {"license_key": {…, "custom": true}, "key": null}` (the caller already knows the value). Audit:
  `license_key.create` with `details.custom = true`.

### `DELETE /api/v1/license-keys/{id}`
Revokes → `204` (idempotent). Ingest pods keep accepting the key until their cache entry expires, i.e. for up to
`OPENLOG_AUTH_CACHE_TTL` (default 60 s).

## API keys

### `GET /api/v1/api-keys`
`{"api_keys": [{"id", "name", "prefix": "ola_…", "scope": "read", "created_by_user_id", "created_by_email", "created_at", "last_used_at", "expires_at", "revoked_at"}]}`

### `POST /api/v1/api-keys` `{"name", "expires_at"?}`
`201 {"api_key": {…}, "key": "ola_…"}` — **shown once**. `expires_at` (optional) must be in the future.

### `DELETE /api/v1/api-keys/{id}`
Revokes → `204`; effective immediately.

## Sessions and audit log

### `GET /api/v1/sessions` · `DELETE /api/v1/sessions/{id}`
The caller's active sessions: `{"sessions": [{"id", "created_at", "last_seen_at", "expires_at", "ip", "user_agent", "current"}]}`.
Revoking the current session also clears the cookie.

### `GET /api/v1/audit-log?limit=&actor=&action=&from=&to=&cursor=`
Newest first (default 100, max 500): `{"events": [{"id", "actor_email", "action", "target_type", "target_id", "details", "ip", "created_at"}], "next_cursor": "…" | null}`.
`actor`: case-insensitive substring of the actor e-mail; `action`: prefix (`member.` or `member.remove`);
`from` (inclusive) / `to` (exclusive): RFC3339 or unix ms; `cursor`: `next_cursor` of the previous page with the same
filters (`null` when the page was not full). Invalid values → `400`. Settings → Audit log in the web UI.
Actions: see [postgres.md](postgres.md#audit_log).

## Single sign-on

OIDC and SAML 2.0 sign-in per organization (D-077, D-088, D-089; operations guide [sso.md](../operations/sso.md), code
`internal/sso`). Postgres auth mode, `OPENLOG_SSO_ENABLED=true` (default) and `OPENLOG_PUBLIC_URL` required;
`GET /auth/config` reports `sso_enabled`.

**Model.** An organization has up to 10 connections (`oidc` or `saml`) and claims e-mail domains. The oldest connection
is the **default connection**; a verified domain routes sign-ins to its `connection_id` (`PUT /sso/domains/{id}`) or,
when unset, to the default connection, and a connection accepts only identities of the domains routed to it. A domain is
verified by a DNS TXT record `_openlog-verification.<domain>` = `openlog-domain-verification=<token>` or by a link
e-mailed to `admin|administrator|hostmaster|postmaster|webmaster@<domain>`; a verified domain belongs to one
organization. **Only identities whose e-mail address is in a verified domain of the connection's organization are
accepted** (sign-in, test and SCIM), so an organization admin's IdP cannot sign in as another company's user.

**Sign-in.** `POST /auth/sso/start` sets a per-sign-in binding cookie (`openlog_sso_<12 hex>`, HttpOnly,
SameSite=Lax, Path=/api/v1/sso, 10 min; the server stores only its SHA-256) and returns the IdP URL.
- OIDC: authorization code flow with PKCE S256, `state` (single use, 10 min) and `nonce`; discovery must match the
  issuer; ID token signature with the provider's JWKS (cached, refetched hourly and on an unknown `kid`), algorithms
  RS/PS/ES256–512 and EdDSA only, `aud` contains the client ID (`azp` required with several audiences), `exp`
  and `iat` with `OPENLOG_SSO_CLOCK_SKEW`. `email_verified=true` is required unless the connection turns it off.
  E-mail/groups missing from the ID token are read from UserInfo (same `sub`). IdP-initiated OIDC is not supported.
- SAML: HTTP-Redirect AuthnRequest (optionally signed), response at the ACS by HTTP-POST. Before signature checks the
  document must be a single `samlp:Response` without DTD with exactly one `Assertion`/`EncryptedAssertion` anywhere
  (XML signature wrapping). The response or the assertion must carry exactly one enveloped signature by an IdP
  metadata certificate (RSA/ECDSA SHA-256/384/512; SHA-1 refused; crewjam/saml + goxmldsig, validated element =
  parsed element). Required: issuer = IdP entity ID, `Destination`/`Recipient` = ACS URL, an `AudienceRestriction`
  with the SP entity ID, a bearer `SubjectConfirmation`, an `AuthnStatement`, `InResponseTo` = the outstanding
  request (SP-initiated), time conditions with the clock skew. Assertion IDs are stored until they expire (replay →
  `replay`). The ACS stores the verified identity on the sign-in and redirects (303) to
  `GET /sso/saml/complete?state=`, which requires the binding cookie (a cross-site POST does not carry SameSite
  cookies). IdP-initiated responses (no `InResponseTo`) are accepted only when the connection allows it; their
  RelayState must be one of `relay_state_allowlist` (else `/`).
- Completion: JIT creates the user (no password, e-mail verified) and the membership with the mapped role or
  `default_role` unless JIT is off (`not_member`) or SCIM deactivated the user (`deprovisioned`). With role mappings
  and a groups claim/attribute, non-owner roles are synchronized at every sign-in. The session is created like a
  password session plus `auth_method` (`oidc`/`saml`), the organization and the connection; its lifetime is the
  minimum of `OPENLOG_SESSION_TTL` and `session_max_age_seconds`.
- Every step answers `303`: to the UI path of the sign-in (`redirect`, relative, never `/api/…`), or to
  `/login?sso_error=<code>` with `expired`, `invalid_request`, `idp_error`, `invalid_response`, `replay`, `disabled`,
  `email_missing`, `email_not_verified`, `domain_not_verified`, `not_member`, `deprovisioned`, `account_disabled`,
  `unavailable`. Test sign-ins return to `/settings/sso?sso_test=ok|failed`. `Referrer-Policy: no-referrer`.

**Sessions.** On every request an SSO session may act only in its organization (other `X-Openlog-Org-Id` → `403`),
only while the connection that created it still exists and is enabled (else `401`), and only until the maximum
session age (`401`). `POST /auth/logout` is local; single logout ends the IdP session as well.

**Single logout (D-088).** The IdP session of every SSO session is recorded at sign-in (SAML NameID, format,
qualifiers and `SessionIndex`; OIDC `sub`, `sid` and the ID token sealed like client secrets); a sign-in fails
(`unavailable`) when it cannot be recorded. `POST /auth/sso/logout` ("Sign out everywhere") revokes the caller's
session and the user's other sessions of the same connection first, clears the cookie, and returns where the browser
ends the IdP session:
- SAML (IdP metadata with a `SingleLogoutService`): a `LogoutRequest` (NameID, SessionIndex) signed with the SP key —
  HTTP-Redirect binding with a RSA-SHA256 query signature (`redirect_url`) or HTTP-POST with an enveloped signature
  (`post`). The IdP answers at `/sso/saml/{connection_id}/slo`; the `LogoutResponse` must be signed by an IdP
  metadata certificate, with issuer = IdP entity ID, `Destination` (when present) = SLO URL, a current `IssueInstant`
  and `InResponseTo` = the request (RelayState is a single-use state, `OPENLOG_SSO_LOGIN_TTL`) →
  `303 <redirect>?sso_logout=ok`, otherwise `?sso_logout=partial`.
- IdP-initiated: a `LogoutRequest` at `/sso/saml/{connection_id}/slo` (either binding) must be signed (query signature
  with RSA/ECDSA SHA-256/384/512, or exactly one enveloped signature; SHA-1 refused), come from the IdP entity ID with
  `Destination` = SLO URL, `IssueInstant` within 5 min plus clock skew and `NotOnOrAfter` not passed; its ID is kept in
  the replay cache (`replay`). It carries a `NameID` or an `EncryptedID` (decrypted with the SP key). The connection's
  active sessions with that NameID — restricted to the named `SessionIndex` values when the IdP sends any — are
  revoked, and a `LogoutResponse` signed with the SP key (`Success`, or `Responder` when a revocation failed) goes to the
  IdP's SingleLogoutService (`303` for HTTP-Redirect, an auto-submitting form with a hash-pinned
  `Content-Security-Policy` for HTTP-POST). Invalid requests end at `/login?sso_error=invalid_request` without a
  response and without revoking anything.
- OIDC (discovery with `end_session_endpoint`): `redirect_url` = the end session endpoint with `id_token_hint`,
  `client_id`, `post_logout_redirect_uri` = `/api/v1/sso/oidc/logout/callback` (register it at the IdP) and `state`;
  the callback sends the browser to `<redirect>?sso_logout=ok`.
- `redirect` must be `/login` or one of the connection's `logout_redirect_allowlist` paths (else `/login`). Without an
  IdP logout endpoint only the openlog sessions end (`redirect_url` and `post` null).

**Logout from the identity provider without the browser round trip (D-098).** Per connection (URIs in
`service_provider` of the addressed connection; refused requests are audited as `sso.logout_failed` and limited to 60
per client address per 10 min, valid ones are audited as `sso.logout` with `via`):
- SAML SOAP binding `POST /sso/saml/{connection_id}/slo/soap` (also in the SP metadata): a SOAP 1.1 envelope with
  exactly one `LogoutRequest` with one enveloped signature, checked like the front-channel request above (`Destination`
  optional: the SOAP or the SLO URL); answer `200` with a signed `LogoutResponse` in the SOAP body, else a `500` SOAP
  fault without details (`via` `idp_soap`).
- OIDC Back-Channel Logout 1.0 `POST /sso/oidc/{connection_id}/backchannel-logout` (form `logout_token`): a JWT
  signed with a key of the provider's JWKS (asymmetric algorithms), `iss` = issuer, `aud` ∋ client ID, `iat` within
  5 min plus clock skew, `exp` (when present) not passed, `events` with
  `http://schemas.openid.net/event/backchannel-logout`, no `nonce`, a `jti` (replay cache, `oidc-logout:<jti>`) and
  `sub` and/or `sid`. With `sid` the sessions of that IdP session end (and of `sub` when both are given), with `sub`
  only every session of the subject on the connection → `200`; refused → `400 {"error": "invalid_request",
  "error_description"}`; `503 temporarily_unavailable` when sessions could not be ended (`via` `oidc_backchannel`).
- OIDC Front-Channel Logout 1.0 `GET /sso/oidc/{connection_id}/frontchannel-logout?iss=…&sid=…` (register it with
  "session required"): `iss` must be the issuer and `sid` is required (the `SameSite=Strict` session cookie never
  reaches the provider's iframe); the sessions of `sid` end. An empty HTML page with `Cache-Control: no-cache,
  no-store` and `Content-Security-Policy … frame-ancestors <issuer origin>` (no `X-Frame-Options`); `400` without
  `iss`/`sid` or with another issuer (`via` `oidc_frontchannel`).

**IdP metadata trust (D-098).** Saving a SAML connection with `idp_metadata_url` needs a trust decision: metadata
signed by `metadata_signing_certificate_pem` (omitted: the pinned certificates of an unchanged URL; signed metadata
without a certificate pins its signer — compare `saml.metadata_signing_certificates`), or `allow_unsigned_metadata`
for unsigned metadata (else `400`). Signed metadata must carry exactly one enveloped signature on its root
(`EntityDescriptor`/`EntitiesDescriptor` with an `ID`, reference `#ID`, RSA/ECDSA SHA-256+, a pinned certificate valid
now), so wrapped or altered documents are refused.

**Claimed domains (D-089).** An address whose verified domain routes to an **enabled** connection does not create
password accounts: `POST /auth/signup` answers `409 failed_precondition` ("… uses single sign-on of the organization
…"; `/auth/sso/discover` names it), and `POST /invitations/accept` answers the same `409` for invitations of that
organization and — unless the connection has `allow_external_invitations` (default true) — of other organizations.
`POST /invitations/lookup` returns `sso` (`required`, `organization_name`, `connection_name`, `protocol`,
`same_organization`) so the UI offers "Continue with SSO" instead of the password form. An SSO sign-in accepts a
pending invitation of the connection's organization with the invited role (also with JIT off).

**Background refresh (D-088).** The api leader re-fetches, per enabled connection, the OIDC discovery document and JWKS
every 30 min (stored in `sso_connections.idp_cache` and used by every pod for up to an hour, unknown key ids still fetch
the JWKS) and the SAML metadata of `idp_metadata_url` at half its `cacheDuration`/remaining `validUntil` (15 min –
24 h, default 6 h). Changed metadata with the same entity ID replaces the stored copy without a new `config_version`
(audit `sso.connection.metadata_refresh`) only when it is signed by a pinned metadata signing certificate; unsigned
metadata with other signing certificates or endpoints, or metadata signed by a certificate that is not pinned, fails
the refresh and is kept as `saml.pending_metadata` (`reason` `changed`/`signer_changed`, audit
`sso.connection.metadata_pending`) until an administrator confirms it with `POST /sso/connections/{id}/metadata/accept`
(audit `sso.connection.metadata_accept`; a new signer is pinned). An invalid or missing signature on metadata with a
pinned certificate, a changed entity ID, expired metadata or an expired signing certificate fails the refresh. Failures back off 5 min → 1 h. `connection.health` reports `ok`, `warning` (1–2 failures, certificate
expiring within 30 days, metadata valid for less than 7 days), `error` (3 failures, expired certificate) or `unknown`.
Metrics: `openlog_sso_idp_refresh_total{protocol,result}`, `openlog_sso_idp_refresh_duration_seconds{protocol}`,
`openlog_sso_idp_refresh_failing_connections`.

**Enforcement.** Enforcement is a setting of a connection and applies to members whose verified e-mail domain routes to
it. With `enforce`, password sessions of those members cannot act in the organization (`403 permission_denied` "this organization requires single sign-on"), except break-glass owners
(owners listed in the connection's `break_glass_user_ids`). `POST /auth/login` with a correct password answers the same `403` when
every organization of the user requires SSO (a user in another organization still signs in, without access to the
enforcing one). Turning enforcement on (owner) requires an enabled connection whose current `config_version`
passed a test sign-in, a verified domain routed to it, at least one break-glass owner, and that the caller keeps access
(break-glass owner or SSO session of this connection) → else `409`. While enforced the connection cannot be
disabled or deleted, and its last verified domain can be neither removed nor routed elsewhere (`409`).

| Endpoint | Who | Notes |
|---|---|---|
| `POST /auth/sso/discover` `{"email"}` | public | `{"sso", "organization_name", "connection_name", "protocol", "enforced"}` of the routed connection; reveals only whether a domain uses SSO. 60 per IP per 10 min |
| `POST /auth/sso/start` `{"email", "redirect"?}` | public | `{"redirect_url"}` + binding cookie; `404` without an enabled connection for the domain; `503` IdP unreachable. 30 per IP per 10 min |
| `GET /sso/oidc/callback` · `POST /sso/saml/{connection_id}/acs` · `GET /sso/saml/complete` | public | See above; always `303` |
| `GET /sso/saml/{connection_id}/metadata` | public | SP metadata (`application/samlmetadata+xml`); the URL is the SP entity ID. Lists the ACS, the SLO service (both bindings), the signing certificate and the accepted encryption algorithms |
| `GET\|POST /sso/saml/{connection_id}/slo` · `GET /sso/oidc/logout/callback` | public | Single logout, see above; `303` or an HTML form |
| `POST /sso/saml/{connection_id}/slo/soap` · `POST /sso/oidc/{connection_id}/backchannel-logout` · `GET /sso/oidc/{connection_id}/frontchannel-logout` | public (identity provider) | Logout from the IdP, see above (D-098) |
| `GET /auth/sso/session` | signed-in user | `{"sso", "protocol", "connection_id", "connection_name", "idp_logout"}` |
| `POST /auth/sso/logout` `{"redirect"?}` | signed-in user | `{"protocol", "redirect_url", "post": {"url", "fields"}\|null, "revoked_sessions"}`; clears the session cookie |
| `GET /sso/connections` · `POST /sso/connections` `SSOConnectionInput` | admin, owner | SSOState `{"available", "secrets_encrypted", "scim_enabled", "email_verification_available", "domain_email_local_parts", "service_provider": {"oidc_redirect_uri", "oidc_post_logout_redirect_uri", "oidc_backchannel_logout_uri", "oidc_frontchannel_logout_uri", "scim_base_url", "saml_entity_id", "saml_acs_url", "saml_slo_url", "saml_slo_soap_url", "saml_metadata_url", "saml_certificate_pem"}, "connection": SSOConnection\|null, "connections": [SSOConnection]}`; `POST` → `201` with the new connection; ≤ 10 per organization |
| `GET\|PUT\|DELETE /sso/connections/{id}` · `POST /sso/connections/{id}/test` · `POST /sso/connections/{id}/test/start` · `PUT /sso/connections/{id}/enforcement` · `GET\|PUT /sso/connections/{id}/role-mappings` | admin, owner (enforcement: owner) | As the single-connection endpoints below, for one connection; `service_provider` holds its SAML values. `DELETE` ends its sessions and routes its domains to the default connection |
| `POST /sso/connections/{id}/refresh` | admin, owner | Refresh now; SSOState with the new `health`; 30 per connection per 10 min |
| `POST /sso/connections/{id}/metadata/accept` `{"digest"}` | admin, owner | Confirms `saml.pending_metadata` (the metadata is fetched again and must still carry that change); SSOState; `409` without that pending change; shares the refresh limit |
| `GET /sso/connection` · `PUT /sso/connection` · `DELETE /sso/connection` | admin, owner | Single-connection API (D-077) on the **default** connection; `PUT` creates it when there is none. `client_secret` omitted = keep, `""` = remove; SAML metadata is fetched from `idp_metadata_url` now (or pasted `idp_metadata_xml`); each save increments `config_version`. SSOConnection adds `default`, `logout_redirect_allowlist`, `allow_external_invitations`, `saml.idp_slo_url`, `saml.metadata_signing_certificates` (fingerprints), `saml.allow_unsigned_metadata`, `saml.pending_metadata` and `health`; SSOConnectionInput `saml.metadata_signing_certificate_pem` and `saml.allow_unsigned_metadata` (D-098) |
| `POST /sso/connection/test` | admin, owner | Server-side checks `{"ok", "checks": [{"name", "ok", "message"}]}` |
| `POST /sso/connection/test/start` | admin, owner | `{"redirect_url"}`; a real sign-in at the IdP that only records `last_test` (email, groups, resulting role) |
| `PUT /sso/enforcement` `{"enforce", "break_glass_user_ids"}` | owner | Safeguards above |
| `GET /sso/role-mappings` · `PUT /sso/role-mappings` `{"mappings": [{"group", "role"}]}` | admin, owner | Organization-wide mappings (SCIM, and sign-ins through connections without own mappings); `{"mappings", "connection_id"}`; `role` ∈ admin, member, viewer; ≤ 200; highest matching role wins |
| `GET /sso/domains` · `POST /sso/domains` `{"domain"}` · `PUT /sso/domains/{id}` `{"connection_id"}` · `DELETE /sso/domains/{id}` | admin, owner | ≤ 20 per organization; adding needs a confirmed e-mail address; `connection_id` null = default connection |
| `POST /sso/domains/{id}/verify` `{"method": "dns_txt"\|"email", "email_local_part"?}` | admin, owner | `409` when the TXT record is missing or another organization verified the domain; e-mail: 5 per domain per 10 min, link valid 24 h |
| `POST /sso/domains/verify-email` `{"token"}` | public | Token of the link `/sso/verify-domain#token=oldv_…` |
| `GET /scim/tokens` · `POST /scim/tokens` `{"name", "expires_at"?}` · `DELETE /scim/tokens/{id}` | admin, owner | `POST` returns `{"token", "secret"}` once (`ols_` + 48 hex); ≤ 20 active |

Audit actions: `sso.connection.create|update|delete|test|test_start|test_login|refresh|metadata_refresh|metadata_pending|metadata_accept`,
`sso.enforcement.update`, `sso.role_mappings.update`, `sso.domain.add|verify|remove|assign|verification_email`,
`sso.login`, `sso.login_failed` (reason), `sso.logout` (`via` `user`/`idp`/`idp_soap`/`oidc_backchannel`/`oidc_frontchannel`, revoked sessions), `sso.logout_complete`,
`sso.logout_failed`, `user.login_refused` (password sign-in refused by enforcement), `invitation.accept` with `via`
`sso`, `member.add` / `member.role_change` with `via` `sso_jit`, `sso_groups`, `scim`, `scim_groups`, and the SCIM
actions below.

## SCIM

SCIM 2.0 provisioning (RFC 7643/7644 subset, D-078, code `internal/scim`) at **`/api/scim/v2`** with
`Authorization: Bearer ols_…` (a SCIM token authenticates only these paths; API keys and sessions do not).
`OPENLOG_SCIM_ENABLED=true` (default). Responses are `application/scim+json`; errors use the SCIM error schema
(`status`, `scimType` `invalidFilter`, `invalidValue`, `invalidSyntax`, `invalidPath`, `noTarget`, `uniqueness`,
`mutability`, `tooMany`).

| Resource | Operations |
|---|---|
| `/ServiceProviderConfig`, `/ResourceTypes`, `/Schemas` | GET (patch and filter supported; no bulk, sort, ETag, password change) |
| `/Users` | GET (`filter=userName eq "…"` \| `externalId eq "…"`, `startIndex`, `count` ≤ 500), POST |
| `/Users/{id}` | GET, PUT, PATCH (`add`/`replace`/`remove`; paths `active`, `userName`, `externalId`, `displayName`, `name`, `name.givenName`, `name.familyName`, `emails`, `emails[primary eq true].value`, `emails[type eq "work"].value`; path-less object; other attributes accepted and ignored), DELETE |
| `/Groups` | GET (`filter=displayName eq "…"` \| `externalId eq "…"`, `excludedAttributes=members`), POST |
| `/Groups/{id}` | GET, PUT, PATCH (`displayName`, `externalId`, `members` add/replace/remove, `members[value eq "<id>"]` remove), DELETE |

Semantics: a SCIM user is the organization's membership of the openlog account with the primary e-mail (or
`userName`), which must be in a **verified domain** of the organization (`400 invalidValue` otherwise); the `id` is
the openlog user id. Creating an active user adds the membership (creating the account when the address is new,
role from SCIM groups or the connection's `default_role`, viewer without a connection). `active=false` (also the
strings `"False"`) or DELETE removes the membership immediately, revokes the user's SSO sessions bound to the
organization and the API keys they created in it; other sessions lose the organization on their next request. A
deactivated user is not re-added by JIT sign-in. Owners cannot be deactivated or deleted (`400 mutability`) and
their role is never changed. **E-mail change (D-089):** the primary (or first) address of `emails` in a PUT or PATCH —
or a changed `userName` when the previous `userName` was the account's address — becomes the openlog account's
address when it is valid (`400 invalidValue`), in a verified domain of the organization (`400 invalidValue`), not used
by another account (`409 uniqueness`), the current address is in a verified domain of the organization and the user
is not an owner (`400 mutability`); audit `scim.user.email_change` (`from`, `to`). Group members must be
provisioned users; group changes recompute the members' roles through the role mappings.
Audit actions: `scim.user.create|update|activate|deactivate|delete|email_change`, `scim.group.create|update|delete`
(actor `scim:<token name>`), `scim.token.create|revoke`.

## Version

### `GET /api/v1/version` (any authenticated principal, both auth modes)
`{"version", "commit", "date", "latest_available": {"version", "notes_url", "checked_at"} | null,
"update_check": "enabled|disabled|failed", "updater": {…} | null}`. `latest_available` is the newest verified
release of `OPENLOG_UPDATE_CHANNEL` when it is newer than the answering pod. `updater` is the status document of
`openlog-updater` (`engine`, `mode`, `state`: `off|error|up_to_date|available|waiting_for_maintenance_window|updating|succeeded|failed|rolled_back|rollback_failed`,
`current_version`, `target_version`, `steps[]`, `failed_versions[]`, `history[]`, …; see openapi `UpdaterStatus`).
`updater.message` is English; for fixed messages `updater.message_code` (`mode_off`, `update_available`,
`update_available_notify`, `waiting_for_window`, `updated`, `rolled_back`, `rollback_failed`, `failed_before_change`,
`up_to_date_newest`, `up_to_date_no_eligible`) and `updater.message_params` (strings: `version`, `from`, `to`,
`channel`, `details`) let clients translate it; both are absent in documents of older updaters and for free text
(clients show `message` then). `update_requests.latest` carries the same `message_code`/`message_params` when its
`message` is a fixed updater message (plus `error` for an appended English error).
`update_requests` (null in static mode): `{"can_request", "updater_listening", "updater_polled_at", "latest": UpdateRequest | null}`;
`can_request` = the caller may use the two endpoints below; `latest.requested_by_email` only when `can_request`.
Every API response (including errors and the UI) carries `X-Openlog-Version`.

### `POST /api/v1/version/check` (admin, owner; postgres auth mode)
"Check now": queues an update request `action=check` for `openlog-updater` and runs the api release check
immediately (`OPENLOG_UPDATE_CHECK=enabled`), then returns `200` with the `GET /version` body. At most one check
request per 30 s for the whole installation: `429 resource_exhausted` with `Retry-After` (seconds). Audit
`update.check_requested` (target `update_request`). API keys, lower roles, and every caller when
`OPENLOG_SIGNUP_ENABLED=true` (organization admins are not server operators) → `403`.

### `POST /api/v1/version/update` `{"target_version", "ignore_maintenance_window"?: false}` (admin, owner)
"Update now": `202` with the queued `UpdateRequest` `{"id", "action": "apply", "target_version",
"ignore_maintenance_window", "state": "pending|running|done|failed|expired", "message", "requested_by_email",
"requested_at", "picked_at", "finished_at"}`. The updater installs the release also in `notify` mode, only when it
still selects `target_version`, and outside its maintenance window only with `ignore_maintenance_window: true`
(releases-updates.md §5.1). `400` invalid version; `409 failed_precondition` when the version is not newer than the
answering pod, no updater reports, `OPENLOG_UPDATER_MODE=off`, an update is running or an apply request is already
open; `429` within 30 s of the previous apply request. Audit `update.apply_requested` (`details.from`, `to`,
`ignore_maintenance_window`, `engine`). Progress: poll `GET /version` (`update_requests.latest`, `updater.state`,
`updater.steps`); the Compose updater picks requests up within `OPENLOG_UPDATER_REQUEST_POLL` (10 s), the Kubernetes
CronJob at its next run.

## Onboarding

### `GET /api/v1/onboarding` (any principal with an organization, both auth modes)
Inputs of the web UI's **Add data** page (`/add-data`), which builds copy-paste install commands in the browser.
`200` with `Cache-Control: no-store`; `401` unauthenticated, `403` without an organization. No secrets: license key values
are only returned once by `POST /api/v1/license-keys`, and a key the user pastes into the page never reaches the server.

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
   "node": {"name": "@openlog/node", "version": "0.9.2", "registry": "missing",
            "registry_url": "https://www.npmjs.com/package/@openlog/node/v/0.9.2",
            "release_asset_url": "https://github.com/onuragtas/openlog/releases/download/v0.9.2/openlog-node-0.9.2.tgz",
            "release_asset_sha256_url": "https://github.com/onuragtas/openlog/releases/download/v0.9.2/openlog-node-0.9.2.tgz.sha256"},
   "python": {"name": "openlog-agent", "version": "0.9.2", "registry": "unknown", …},
   "dotnet": {"name": "OpenLog.Agent", "version": "0.9.2", "registry": "available", …}}}
```

- `source`: `configured` (`OPENLOG_PUBLIC_URL`, `OPENLOG_INGEST_PUBLIC_URL`, `OPENLOG_INGEST_PUBLIC_GRPC_URL`),
  `derived_public_url` (scheme and host of `OPENLOG_PUBLIC_URL` with port 4318/4317), `derived_ingest_url` (gRPC: host of
  `OPENLOG_INGEST_PUBLIC_URL` with port 4317) or `derived_request` (scheme and host the browser used; a well-formed
  `X-Forwarded-Proto`/`X-Forwarded-Host` wins). The UI asks the user to confirm derived endpoints.
- `agent_version`: the newest verified release of `release_channel` when the release check found one, else the
  server's own release version; `null` for development builds (commands then use `latest` download URLs).
- `cors_enabled` / `cors_allowed_origins`: `OPENLOG_INGEST_CORS_ALLOWED_ORIGINS` of this api pod (browser OTLP/JSON logs).
- `features.can_create_license_keys`: signed-in admin or owner in postgres mode; `can_list_license_keys`: signed-in
  member or higher; `fleet_php_install`: fleet endpoints exist (php-agent.md §7.3); `tail_sampling`:
  `OPENLOG_TAILSAMPLING_ENABLED`.

## Hosts

### `GET /api/v1/hosts?limit=`
Hosts seen in the last 24h, ordered by `host_name`.
```json
{"hosts": [{"host_id": "…", "host_name": "web-1", "os_description": "Ubuntu 24.04 LTS", "arch": "amd64",
            "agent_version": "0.1.0", "last_seen": "2026-09-13T10:00:00Z", "resource_attributes": {"env": "prod"}}]}
```

### `GET /api/v1/hosts/{host_id}`
Single host object (same shape) or `404`.

Every `/hosts/{host_id}/…` endpoint (`metrics`, `inventory`, `services`) first checks that the host has a host record
in the caller's organization and returns `404 not_found` (`"host not found"`) otherwise. A host of another
organization is indistinguishable from an unknown one. Parameter errors (`400`) are reported before this check.

## Containers

Containers reported by the infra agent: cgroup metrics, `openlog.container.status` and `container.restarts` data points
(semantic-conventions.md §2 "Container metrics"), kept in the `containers` table (0009_containers, 30 days after the last
data point). A container is identified by `container_id` (64 hex); `reporting` is false when its last data point is older
than 5 minutes. Telemetry permissions (any role, API keys too); every query is tenant-scoped. `container_id` path
parameters that are not 64 hex characters → `400 invalid_argument`.

**Container object** (`Container`):
```json
{"container_id": "3f4e…", "name": "openlog-agents-orders-1", "image_name": "openlog-apmdemo/orders", "image_tags": ["1"],
 "runtime": "docker", "host_id": "…", "host_name": "docker-desktop", "compose_project": "openlog-agents",
 "compose_service": "orders", "k8s_pod_name": "", "k8s_namespace_name": "", "k8s_container_name": "",
 "state": "running", "health": "healthy", "started_at": "2026-09-14T09:00:00.5Z", "restart_count": 0,
 "first_seen": "…", "last_seen": "…", "reporting": true,
 "cpu_utilization": 0.012, "memory_usage": 18350080, "memory_limit": 8233447424,
 "cpu_sparkline": [[1757757600000, 0.011]], "memory_sparkline": [[1757757600000, 18350080]]}
```
`state` is the Docker state (`running`, `paused`, `restarting`, `exited`, `created`, `dead`) or `""` when the agent sends
none (no Docker API access); `health` is `healthy`/`unhealthy`/`starting` or `""`; `started_at` is `null` when unknown.
`cpu_utilization` (0..1 of the host), `memory_usage` and `memory_limit` (bytes; host memory when unlimited) are the latest
bucket of the range, `null` without data points.

### `GET /api/v1/containers?host_id=&compose_project=&compose_service=&state=&q=&from=&to=&limit=`
Containers with data in `[from, to]` (default last hour), ordered by compose project, compose service and name:
`{"containers": [Container…], "total": 12, "step": "120s"}`. `total` counts matches before `limit`; sparklines have
≈ 30 buckets of `step`. `compose_project`/`compose_service` are exact matches, present but empty = containers without
one; `state` is one of the states above or `unknown` (`400` otherwise); `q` (≤ 256 bytes) matches every whitespace-separated
term, case-insensitive, against id, name, image, tags, host, compose and Kubernetes names.

### `GET /api/v1/containers/groups?host_id=&state=&q=&from=&to=`
The same containers grouped by compose project and service. `running` counts reporting containers in state `running`;
`cpu_utilization` and `memory_usage` sum the latest values of reporting containers (`null` without data; not computed
above `OPENLOG_API_MAX_ROWS` containers).
```json
{"projects": [{"compose_project": "openlog-agents", "host_ids": ["…"], "containers": 9, "running": 9,
  "services": [{"compose_service": "orders", "containers": 1, "running": 1, "cpu_utilization": 0.01, "memory_usage": 18350080}]}]}
```

### `GET /api/v1/containers/{container_id}?from=&to=`
The container's latest record (the host it was seen on last) with stats over the range, plus `attributes` (data point
attributes of its latest status point), or `404`.

### `GET /api/v1/containers/{container_id}/timeseries?from=&to=&step=`
Chart series from raw data points (`step`: Go duration ≥ `10s`, default ≈ 300 points; 30-day retention), `404` for an
unknown container. Gauges are averaged per bucket; `network_*` and `blockio_*` are per-second rates of the cumulative
counters per series (resets clamp to 0), summed over series.
```json
{"container_id": "…", "host_id": "…", "step": "20s", "from": 1757757600000, "to": 1757761200000,
 "series": {"cpu_utilization": [[1757757600000, 0.01]], "memory_usage": [], "memory_limit": [], "network_receive": [],
            "network_transmit": [], "blockio_read": [], "blockio_write": []}}
```

### `GET /api/v1/containers/{container_id}/services?from=&to=`
APM services whose span resources carried this `container.id` (`apm_service_containers`, apm.md §1), with the RED object
over the range: `{"services": [{"service_name", "service_namespace", "environment", "first_seen", "last_seen", "apdex_t_ms", …ApmRed}]}`.

Container logs: `GET /api/v1/logs?container_id=…` (see Logs).

## Kubernetes

Kubernetes clusters, nodes, workloads, pods and events reported by the infra agent in node and cluster mode
(semantic-conventions.md §7), kept in the entity tables `k8s_clusters`, `k8s_nodes`, `k8s_workloads` and `k8s_pods`
(schema 0040–0042, 30 days after the last data point). Telemetry permissions (any role, API keys too); every query is
tenant-scoped. Every endpoint accepts `from`/`to` (default last hour). Timestamps are RFC3339 strings, sparklines
`[[unix_ms, value], …]`, nullable numbers are `null` without data. String filters and path segments are at most 256 bytes
(`400 invalid_argument` otherwise).

- A cluster is identified by `cluster_uid` (resource `k8s.cluster.uid`, uid of `kube-system`) and named by `cluster_name`.
- `reporting`: the object's last data point is at most 5 minutes old (cluster agent interval 30 s).
- **Current objects**: counts of a cluster (nodes, pods, workloads) and the pods of a node or workload include only
  objects whose last point is at most 2 minutes older than the parent's last point, so deleted objects drop out after
  one collection and a cluster that stopped reporting keeps its last known counts.
- Workload `health`: `unknown` when not reporting; Deployment/StatefulSet/DaemonSet/ReplicaSet `healthy` when
  `available >= desired` (desired 0 → healthy), `unavailable` when `available == 0 < desired`, else `degraded`; Job
  `degraded` when `updated` (failed) > 0 and `available` (succeeded) < `desired`, else `healthy`; CronJob `healthy`.
- Pod `status`: `reason` (e.g. `CrashLoopBackOff`, `Evicted`) when set, else `phase`.
- Usage metrics: `cpu_usage` (cores) and `memory_working_set` (bytes) come from the node agents' kubelet metrics
  (`k8s.node.*`, `k8s.pod.*`, `k8s.container.*`, matched by resource `k8s.cluster.name` and attributes), allocatable
  resources and requests/limits from the cluster agent (matched by `cluster_uid`). "Latest" is the newest point in
  `[from, to]`; sums over nodes or pods include only values within 2 minutes of the newest one.

**Cluster object** (`KubernetesCluster`):
```json
{"cluster_uid": "5b1c…", "cluster_name": "prod", "version": "v1.31.2", "first_seen": "…", "last_seen": "…", "reporting": true,
 "nodes": 3, "nodes_ready": 3, "pods": {"Pending": 0, "Running": 41, "Succeeded": 2, "Failed": 0, "Unknown": 0},
 "pods_not_ready": 1, "workloads": 18, "workloads_unhealthy": 1, "namespaces": ["default", "kube-system", "shop"]}
```
`pods_not_ready` counts current `Running` pods that are not ready; `workloads_unhealthy` counts `degraded` and
`unavailable` workloads; `namespaces` lists every namespace with pods or workloads in the range.

### `GET /api/v1/kubernetes/clusters?from=&to=`
Clusters with data in the range, ordered by name: `{"clusters": [KubernetesCluster…]}`.

### `GET /api/v1/kubernetes/clusters/{cluster_uid}?from=&to=`
A cluster (any time within retention) or `404`, with `workloads_by_kind` (`[{"kind", "total", "healthy", "degraded",
"unavailable", "unknown"}]` over current workloads, by kind), `warning_events` (the latest 20 `Warning` events in the range,
newest first, Event objects below), `cpu_usage` / `memory_working_set` (sum of the latest `k8s.node.cpu.usage` /
`k8s.node.memory.working_set` over the nodes) and `allocatable_cpu` / `allocatable_memory` (sum of the latest
`k8s.node.allocatable_*`).

### `GET /api/v1/kubernetes/nodes?cluster_uid=&q=&from=&to=&limit=`
Nodes seen in the range, ordered by cluster name and node name: `{"nodes": [KubernetesNode…], "total": 3}` (`total` before `limit`).
```json
{"cluster_uid": "…", "cluster_name": "prod", "node_name": "worker-1", "node_uid": "…", "ready": "true", "unschedulable": false,
 "roles": ["worker"], "kubelet_version": "v1.31.2", "os_image": "Ubuntu 24.04 LTS", "container_runtime": "containerd://1.7.22",
 "internal_ip": "10.0.0.11", "created_at": "2026-01-01T00:00:00Z", "allocatable_cpu": 4, "allocatable_memory": 16364216320,
 "allocatable_pods": 110, "cpu_usage": 0.82, "memory_working_set": 5242880000, "pods": 23, "host_id": "…", "host_name": "worker-1",
 "first_seen": "…", "last_seen": "…", "reporting": true, "conditions": [{"condition": "Ready", "status": "true"}]}
```
`ready` is `true`, `false` or `unknown`; `pods` counts current `Running` and `Pending` pods on the node; `host_id` /
`host_name` are the host whose resource attributes carry `k8s.cluster.name` and `k8s.node.name` of the node (`null` when
no node agent reports); `conditions` are the latest `k8s.node.condition` values (`true`/`false`/`unknown`, by name). `q`
matches every term against node name, uid, cluster name, roles, IP, kubelet version and OS image.

### `GET /api/v1/kubernetes/workloads?cluster_uid=&namespace=&kind=&health=&q=&from=&to=&limit=`
Workloads seen in the range, ordered by cluster name, namespace, kind and name:
`{"workloads": [KubernetesWorkload…], "total": 18, "step": "120s"}`. `kind` ∈ `Deployment`, `StatefulSet`, `DaemonSet`,
`Job`, `CronJob`, `ReplicaSet`, `Pod` (the cluster agent reports no `Pod` workloads, so it matches nothing); `health` ∈
`healthy`, `degraded`, `unavailable`, `unknown`; other values → `400`.
```json
{"cluster_uid": "…", "cluster_name": "prod", "namespace": "shop", "kind": "Deployment", "name": "orders", "uid": "…",
 "desired": 3, "ready": 2, "available": 2, "updated": 3, "health": "degraded", "pods": 3, "restarts": 7,
 "cpu_usage": 0.12, "memory_working_set": 314572800, "cpu_sparkline": [[1757757600000, 0.1]], "memory_sparkline": [],
 "created_at": "2026-09-01T10:00:00Z", "first_seen": "…", "last_seen": "…", "reporting": true}
```
`desired`/`ready`/`available`/`updated` per kind as in semantic-conventions §7.4; `pods` and `restarts` (sum of
`openlog.k8s.pod.restarts`) over the current pods of the workload; `cpu_usage` / `memory_working_set` sum the latest
`k8s.pod.cpu.usage` / `k8s.pod.memory.working_set` of its pods, sparklines sum the per-pod bucket averages (≈ 30 buckets of `step`).

### `GET /api/v1/kubernetes/workloads/{cluster_uid}/{namespace}/{kind}/{name}?from=&to=`
A workload (any time within retention) or `404`, plus `pod_list` (Pod objects of the workload seen in the range, at
most `OPENLOG_API_MAX_ROWS`), `hpa` (`{"name", "min_replicas", "max_replicas", "current_replicas", "desired_replicas"}`
of the HorizontalPodAutoscaler targeting it, latest `k8s.hpa.*` values, or `null`) and `attributes` (data point
attributes of its latest `openlog.k8s.workload.status` point). An unknown `kind` → `400`.

### `GET /api/v1/kubernetes/workloads/{cluster_uid}/{namespace}/{kind}/{name}/timeseries?from=&to=&step=`
`{"step": "20s", "from": 1757757600000, "to": 1757761200000, "series": {"cpu_usage": [], "memory_working_set": [],
"ready": [], "desired": [], "restarts": []}}` (`step` as for containers; `404` for an unknown workload). `cpu_usage` and
`memory_working_set` sum the per-pod bucket averages; `ready`/`desired` are the latest status values per bucket;
`restarts` sums the latest restart count per pod per bucket.

### `GET /api/v1/kubernetes/pods?cluster_uid=&namespace=&node=&workload_kind=&workload_name=&phase=&q=&from=&to=&limit=`
Pods seen in the range, ordered by cluster name, namespace and pod name: `{"pods": [KubernetesPod…], "total": 41}`.
`phase` ∈ `Pending`, `Running`, `Succeeded`, `Failed`, `Unknown`; `workload_kind` as `kind` above; other values → `400`.
```json
{"cluster_uid": "…", "cluster_name": "prod", "namespace": "shop", "pod_name": "orders-7d9c-x2k", "pod_uid": "…",
 "node_name": "worker-1", "workload_kind": "Deployment", "workload_name": "orders", "phase": "Running", "ready": false,
 "reason": "CrashLoopBackOff", "status": "CrashLoopBackOff", "restarts": 7, "pod_ip": "10.244.1.17", "qos_class": "Burstable",
 "created_at": "…", "started_at": "…", "cpu_usage": 0.01, "memory_working_set": 52428800,
 "first_seen": "…", "last_seen": "…", "reporting": true}
```

### `GET /api/v1/kubernetes/pods/{pod_uid}?from=&to=`
A pod (any time within retention) or `404`, plus:
- `containers`: `[{"name", "container_id", "image", "ready", "restarts", "state", "reason", "known", "host_id", "cpu_usage",
  "memory_working_set", "cpu_request", "cpu_limit", "memory_request", "memory_limit"}]` from `openlog.k8s.pod.containers`;
  `known` is true (and `host_id` set) when the container is in the `containers` table (see Containers); usage from the
  latest `k8s.container.*` kubelet metrics, requests/limits from the cluster agent (`null` when unset).
- `labels`: `k8s.pod.label.*` attributes (prefix removed) of the latest status points of its known containers.
- `services`: `[{"service_name", "service_namespace", "deployment_environment"}]` whose spans carried one of its container ids (`apm_service_containers`).
- `host_id` / `host_name`: the node's host, or `null`.

### `GET /api/v1/kubernetes/pods/{pod_uid}/timeseries?from=&to=&step=`
`{"step", "from", "to", "series": {"cpu_usage": [], "memory_working_set": [], "network_receive": [], "network_transmit": [], "restarts": []}}`
(`404` for an unknown pod): kubelet gauges averaged per bucket, `network_*` per-second rates of `k8s.pod.network.io`
(resets clamp to 0), `restarts` the latest `openlog.k8s.pod.restarts` per bucket.

### `GET /api/v1/kubernetes/events?cluster_uid=&namespace=&type=&object_kind=&object_name=&object_uid=&reason=&from=&to=&limit=`
Kubernetes events (log records with `event_name` `k8s.event`, semantic-conventions §7.5) in the range, newest first:
`{"events": [KubernetesEvent…]}`. Updates of one event (same `k8s.event.uid`) are returned once, as the latest record.
`type` ∈ `Normal`, `Warning` (`400` otherwise); `limit` default 100, at most 1000.
```json
{"timestamp": "…", "type": "Warning", "reason": "BackOff", "message": "Back-off restarting failed container app",
 "count": 12, "namespace": "shop", "object_kind": "Pod", "object_name": "orders-7d9c-x2k", "object_uid": "…",
 "source": "kubelet", "cluster_uid": "…", "cluster_name": "prod"}
```

### `GET /api/v1/kubernetes/pods/{pod_uid}/events?from=&to=&type=&reason=&limit=`
The events whose involved object is the pod (`object_uid` = `pod_uid`; container events of the pod carry the same uid).

### `GET /api/v1/apm/services/{service_name}/kubernetes?namespace=&environment=&from=&to=`
Pods of an APM service seen in the range: pods whose `openlog.k8s.pod.containers` contain a container id linked to the
service since `from` (`apm_service_containers`), or whose uid spans of the service carried as resource `k8s.pod.uid` in the
range. `{"pods": [{"cluster_uid", "cluster_name", "namespace", "pod_name", "pod_uid", "workload_kind", "workload_name",
"node_name", "phase", "ready", "reporting"}]}`, ordered by cluster, namespace and pod name (at most 1000).

Pod logs: `GET /api/v1/logs?k8s_pod_uid=…` (see Logs).

## Metrics

### `GET /api/v1/metrics/names?host_id=&from=&to=`
`{"names": [{"name": "system.cpu.utilization", "type": "gauge", "unit": "1"}]}`

### `GET /api/v1/hosts/{host_id}/metrics?name=&from=&to=&step=&agg=&group_by=`

| Param | Default | Notes |
|---|---|---|
| `name` | required | metric name |
| `step` | auto (≈ 300 points) | Go duration, min `10s` |
| `agg` | `avg` for gauges, `rate` for monotonic sums, `last` for non-monotonic sums | `avg`, `min`, `max`, `sum`, `last`, `rate` |
| `group_by` | all attributes | comma-separated attribute keys; series are merged by these keys. `resource.<key>` (allowed keys below) groups by a resource attribute, reported as `resource.<key>` in `attributes` |
| `resource.<key>` | — | exact-match resource attribute filter (AND-ed, one value per key, at most 4) |

`rate` = per-second increase per series per step (counter resets clamp to 0), then summed across merged series.
Ranges longer than 6h read from the 1-minute rollup table, except requests with `resource.*` filters or groupings
(the rollup has no resource attributes), which read raw data points (30-day retention).

Allowed `resource.<key>` keys — integration instance identity and PostgreSQL entities (semantic-conventions §6.1, §6.5):
`openlog.discovery.id`, `openlog.discovery.instance`, `openlog.integration.id`, `service.instance.id`, `server.address`,
`server.port`, `postgresql.database.name`, `postgresql.table.name`, `postgresql.index.name`, and for pg_stat_statements
query resources `postgresql.queryid`, `postgresql.rolname`, `db.query.text`. Any other key, an empty or
repeated value, or a value longer than 1024 bytes → `400 invalid_argument` (reported before the host check). Values are
bound query parameters. Integration panels select one instance with
`?resource.openlog.discovery.id=redis&resource.openlog.discovery.instance=/usr/bin/redis-server`.

```json
{"metric": {"name": "system.cpu.utilization", "type": "gauge", "unit": "1"}, "step": "60s",
 "series": [{"attributes": {"cpu.mode": "user"}, "points": [[1757757600000, 0.12]]}]}
```

## Inventory and discovery

### `GET /api/v1/hosts/{host_id}/inventory?category=`
Items of the host's latest **complete** snapshot. A known host without a complete snapshot yet returns `200` with `snapshot_id: ""` and empty `items`.
```json
{"snapshot_id": "…", "snapshot_time": "…", "items": [{"category": "package", "key": "dpkg:openssl", "data": {"name": "openssl", "version": "3.0.13"}}]}
```

### `GET /api/v1/hosts/{host_id}/services`
Shortcut for `category=discovered_service`; `data` is the discovered service body.

### `GET /api/v1/inventory/search?category=&q=&limit=`
Across all hosts' latest complete snapshots: items in `category` (required) whose key contains `q` (case-insensitive).
`{"items": [{"host_id": "…", "host_name": "…", "category": "package", "key": "dpkg:openssl", "data": {…}}]}`

## Logs

### `GET /api/v1/logs?host_id=&service=&container_id=&compose_project=&compose_service=&k8s_pod_uid=&q=&severity_min=&trace_id=&span_id=&transaction=&transaction_service=&attr.<key>=&from=&to=&limit=&cursor=`
Excludes inventory events (`event.name` starting with `openlog.inventory.`). `q` is a case-insensitive substring match on body. Newest first.

**Paging.** Rows are ordered by (`timestamp`, row key) descending; the row key is a hash of the row's content
(observed timestamp, host, service, severity, trace/span ids, body, attributes). A full page returns
`next_cursor` (opaque string, else `null`); request the next (older) page with the same filters, `from` and `to` plus
`cursor=<next_cursor>`. The cursor holds the last row's timestamp (nanoseconds), row key and how many rows with that
exact position were already returned, so rows in the same nanosecond — even identical ones — are returned exactly once.
An invalid cursor → `400`. Clients that page by moving `to` still work (every response is still newest first).

`span_id` restricts to one span (lower-cased). `transaction` + `transaction_service` (both required together, `400`
otherwise) restrict to the traces with an entry span of that transaction in the range (at most 10000 traces; APM
"logs of this transaction").

`attr.<key>=<value>` is an exact-match filter on a log record attribute (AND-ed, one value per key, bound as a query
parameter). Allowed keys — the attributes the infra agent sets (semantic-conventions §4):
`openlog.log.source` (`file` / `journald` / `container`), `log.file.path`, `log.file.name`, `openlog.discovery.id`,
`openlog.systemd.unit`, `openlog.syslog.identifier`, `log.iostream` (`stdout` / `stderr`). Any other `attr.*` key, an empty or repeated value, or a value
longer than 1024 bytes → `400 invalid_argument`. Agent log records have an empty `service_name`, so host log views use
`host_id` plus these filters, e.g. `?host_id=…&attr.openlog.discovery.id=nginx&attr.log.file.path=/var/log/nginx/error.log`.
`container_id`, `compose_project` and `compose_service` are exact matches on the resource attributes `container.id`
(lower-cased), `docker.compose.project` and `docker.compose.service` of container logs (semantic-conventions §4).
`k8s_pod_uid` is an exact match on the resource attribute `k8s.pod.uid` of Kubernetes container logs (semantic-conventions §7.2).
```json
{"logs": [{"timestamp": "…", "severity_text": "ERROR", "severity_number": 17, "body": "…", "host_id": "…",
           "service_name": "…", "trace_id": "…", "span_id": "…", "attributes": {}, "resource_attributes": {}}],
 "next_cursor": "eyJ2IjoxLCJ0IjoxNzU3NzU3NjAwMDAwMDAwMDAwLCJrIjoiNDIiLCJuIjoxfQ"}
```

## Traces

### `GET /api/v1/traces/{trace_id}`
All spans of the trace ordered by start time, or `404`.
```json
{"trace_id": "…", "spans": [{"span_id": "…", "parent_span_id": "…", "name": "GET /users", "kind": "server",
  "service_name": "…", "start": "…", "duration_ns": 1234567, "status_code": "ok", "status_message": "",
  "attributes": {}, "resource_attributes": {}, "events": [{"timestamp": "…", "name": "…", "attributes": {}}]}]}
```

## APM

Application performance data derived from traces. Definitions (transactions, error groups, sampling weights,
latency histogram, Apdex, service map merge rules) are in [apm.md](apm.md); this section lists the endpoints.
Telemetry permissions (any role, API keys too) except `PUT …/settings`.

Common parameters: `from`/`to` (default last hour), `step` (Go duration ≥ `60s`, default ≈ 60 points, rounded up to
whole minutes; `400` below 60s). On `/apm/services/{service_name}/…`: `namespace`, `environment` — omitted = every
namespace/environment of the service, present (also empty) = exact match. Data is per minute: the first bucket starts
at `from` truncated to the minute. Durations are milliseconds; `throughput` is requests (or calls) per minute;
`error_rate` and `apdex` are 0..1; `avg_ms`, `p50_ms`, `p95_ms`, `p99_ms`, `apdex` are `null` without requests. All
counts are weighted by the sampling probability (apm.md §4).

**RED object** (`ApmRed`): `{"requests", "throughput", "errors", "error_rate", "avg_ms", "p50_ms", "p95_ms", "p99_ms", "apdex"}`.

### `GET /api/v1/apm/services?from=&to=&step=&namespace=&environment=&q=`
Services with spans since `from` (`q`: case-insensitive substring of the name), ordered by name:
```json
{"step": "60s", "services": [{"service_name": "orders", "service_namespace": "shop", "environment": "prod",
  "language": "go", "version": "1.4.2", "last_seen": "…", "apdex_t_ms": 500, "requests": 5400, "throughput": 90,
  "errors": 27, "error_rate": 0.005, "avg_ms": 41.2, "p50_ms": 18.3, "p95_ms": 120.4, "p99_ms": 480.0, "apdex": 0.97,
  "sparkline": [[1757757600000, 88.0]]}]}
```

### `GET /api/v1/apm/services/{service_name}`
`404` when the service has no data within retention.
```json
{"service_name": "orders", "apdex_t_ms": 500, "apdex_t_default": true,
 "instances": [{"service_namespace": "shop", "environment": "prod", "first_seen": "…", "last_seen": "…", "version": "1.4.2",
                "language": "go", "sdk_name": "opentelemetry", "resource_attributes": {"host.id": "…"}}],
 "hosts": [{"host_id": "…", "host_name": "web-1", "first_seen": "…", "last_seen": "…", "known": true}]}
```
`known`: the host has a host record (`GET /hosts/{host_id}` works).

### `GET /api/v1/apm/services/{service_name}/overview?transaction=&type=`
`{"step": "60s", "apdex_t_ms": 500, "totals": ApmRed, "series": [{"t": 1757757600000, …ApmRed}]}`; `transaction`/`type`
restrict to one transaction. Buckets without requests are omitted.

### `GET /api/v1/apm/services/{service_name}/transactions?sort=&type=&limit=`
`sort` = `time` (default: most time consumed) | `throughput` | `slowest` (p95) | `errors`; `limit` default 100, max 1000.
`{"apdex_t_ms": 500, "transactions": [{"transaction_type": "web", "transaction_name": "GET /orders/{id}", "time_consumed_ms", "time_share", "max_ms", …ApmRed}]}`

### `GET /api/v1/apm/services/{service_name}/transaction?name=&type=`
`name` required.
```json
{"transaction_name": "GET /orders/{id}", "transaction_type": "", "step": "60s", "apdex_t_ms": 500, "totals": ApmRed,
 "max_ms": 1840.2, "series": [ApmPoint], "histogram": [{"from_ms": 17.4, "to_ms": 19.0, "count": 42}],
 "slowest": [{"trace_id", "span_id", "timestamp", "duration_ms", "is_error", "http_status_code", "service_name", "transaction_name"}]}
```
`histogram` lists the non-empty latency buckets (apm.md §4.1); `slowest` the 10 slowest entry spans in the range.

### `GET /api/v1/apm/services/{service_name}/errors?limit=&status=&assignee=&q=&sort=` · `GET /api/v1/apm/errors?service=&namespace=&environment=&…`
Error inbox ([apm.md](apm.md) §3.4): groups with occurrences in the range (and, for resolved/ignored/assignee
filters, groups with a matching workflow state), most frequent first (`limit` default 50, max 500). The second form
spans every service. `status` = `all` (default) or a comma-separated list of `unresolved`, `resolved`, `ignored`;
`assignee` = `any` (default), `none`, `me`, or a user id; `q` substring of type/message/service/span name (≤ 256
bytes); `sort` = `count` (default), `last_seen`, `first_seen`. Response adds `counts` per status (after `assignee`
and `q`), `truncated` and `workflow` (`false` without PostgreSQL: every group unresolved, no mutations). Resolved
groups are checked for regressions (and reopened) on every read.
```json
{"step": "60s", "groups": [{"group_id": "9f3c2a71d4b84e0f", "error_type": "*errors.errorString",
  "message": "order <n>: inventory shard <n> unavailable", "count": 12, "total_count": 480, "first_seen": "…",
  "last_seen": "…", "last_trace_id": "…", "last_span_name": "GET /orders/{id}", "sparkline": [[1757757600000, 2]]}]}
```
`count` is in the range, `total_count`, `first_seen`, `last_seen` over retention. Every group also has
`service_name`, `service_namespace`, `environment` and the workflow fields `status`, `assignee`
(`{"user_id", "email", "name"}` or `null`), `resolved_at`, `resolved_in_version`, `resolved_by_email`,
`regressed_at`, `regression_count`, `comment_count`, `updated_at`, `updated_by_email`.

### `GET /api/v1/apm/services/{service_name}/errors/{group_id}`
`400` unless `group_id` is 16 hex digits, `404` for an unknown group. Group fields above plus `last_message` (raw),
`stacktrace` (newest sample), `last_span_id`, `series` (`[[ms, count]]`) and `samples` (newest 20 error spans in the
range: `{"trace_id", "span_id", "timestamp", "span_name", "transaction_name", "duration_ms", "message", "version", "host_id"}`),
the workflow fields above, `affected` (`{"versions", "hosts", "containers", "transactions"}`, each the top 20
`{"value", "name", "count", "first_seen", "last_seen"}` over retention; `name` = host or container name),
`comments` and `activity` (the group's audit events `{"action", "actor_email", "details", "created_at"}`, newest first,
at most 50) and `workflow`.

### `PATCH /api/v1/apm/errors/groups` `{"group_ids": [...], "status"?, "assignee_user_id"?, "resolved_in_version"?}`
Signed-in member, admin or owner (`403` for viewers and API keys; CSRF as usual; `404` in static auth mode). 1–500
group ids (16 hex digits; unknown ids → `404`); at least one change. `status` `unresolved`/`resolved`/`ignored`;
`assignee_user_id` a member's id (`400` otherwise) or `""` to unassign; `resolved_in_version` (≤ 256 bytes) only with
`status: "resolved"`. Returns `{"groups": [{"group_id", "service_name", "service_namespace", "environment", …workflow fields}]}`.
Audit event `apm.error_group.update` per changed group.

### `GET /api/v1/apm/errors/groups/{group_id}/comments` · `POST …/comments` `{"body"}` · `DELETE …/comments/{comment_id}`
`{"comments": [{"id", "author_user_id", "author_email", "author_name", "body", "created_at"}]}`, oldest first. `POST`
(member+, body 1–4000 bytes, `201` with the comment; audit `apm.error_group.comment`); `DELETE` by the author or an
admin/owner (`204`, else `404`; audit `apm.error_group.comment_delete`).

### `GET /api/v1/apm/services/{service_name}/deployments?from=&to=&gap=`
`service.version` changes in the range ([apm.md](apm.md) §12), oldest first; `gap` 5m–24h (default 30m):
`{"gap_seconds": 1800, "deployments": [{"timestamp", "t": 1757757600000, "service_namespace", "environment", "version": "1.4.3", "previous_version": "1.4.2", "initial": false, "rollback": false}]}`

### `GET /api/v1/apm/services/{service_name}/deployments/compare?at=&window=`
`at` required (RFC3339 or ms, truncated to the minute, `400` unless in the past); `window` 5m–24h (default 30m).
`{"at", "window_seconds", "apdex_t_ms", "before": {"from", "to", …ApmRed}, "after": {"from", "to", …ApmRed}, "new_error_groups": [{"group_id", "error_type", "message", "first_seen", "total_count"}]}`
(`after` ends at now at the latest; new groups = first seen in `after`, top 20 by total count).

### `GET /api/v1/apm/services/{service_name}/databases?sort=&db_system=&limit=`
`sort` = `time` (default) | `calls` | `slowest` (avg) | `errors`.
`{"queries": [{"db_system": "postgresql", "db_name": "orders", "db_operation": "SELECT", "statement": "SELECT … WHERE id = ?", "calls", "throughput", "errors", "error_rate", "avg_ms", "p95_ms", "max_ms", "time_consumed_ms", "time_share"}]}`

### `GET /api/v1/apm/services/{service_name}/hosts`
`{"hosts": [...]}` (shape as in the service object).

### `GET /api/v1/apm/services/{service_name}/containers?from=`
Containers whose span resources carried `container.id` for the service since `from` (apm.md §1), newest first. `known`:
the container has infra agent data (Containers); `state`, `reporting` and the latest `cpu_utilization`, `memory_usage`,
`memory_limit` are set only then.
`{"containers": [{"container_id", "name", "host_id", "host_name", "first_seen", "last_seen", "known", "state", "reporting", "cpu_utilization", "memory_usage", "memory_limit"}]}`

### `GET /api/v1/apm/services/{service_name}/settings` · `PUT …/settings` `{"apdex_t_ms": 300}`
`{"service_name", "service_namespace", "environment", "apdex_t_ms", "is_default", "updated_at", "updated_by_email"}`.
The key is (`service_name`, `namespace`, `environment` query parameters, missing = `''`); a row with empty namespace and
environment applies to all of them unless a more specific row exists. `PUT` needs a signed-in admin or owner (`403`
otherwise; CSRF as usual), `apdex_t_ms` an integer 1..600000 (`400`), writes the audit event
`apm.service_settings.update`; not available with `OPENLOG_AUTH_MODE=static` (`404`, `GET` returns the default).

### `GET /api/v1/apm/sampling` · `PUT /api/v1/apm/sampling` `{"policy": {…}, "version": 3}`
The organization's tail sampling policy ([apm.md](apm.md) §4.2, D-075):
`{"enabled": true, "policy": {"enabled", "baseline_ratio", "max_spans_per_second", "rules": [{"name", "type", "ratio", …}]}, "is_default", "version", "updated_at", "updated_by_email"}`.
`enabled` is `OPENLOG_TAILSAMPLING_ENABLED` of the api. `is_default` (version 0) when nothing is stored.
`PUT` requires a signed-in admin or owner (`403`), a valid policy (`400`: unknown fields, ratios outside 0..1, duplicate/reserved rule names, missing rule fields), and the version that was edited (`409 conflict` when the stored version differs). It writes the audit event `apm.tail_sampling.update` and returns the stored state. Not available with `OPENLOG_AUTH_MODE=static` (`404`; `GET` returns the keep-all default).

### `POST /api/v1/apm/sampling/preview` `{"policy": {…}, "window_minutes": 60}`
Estimates what a policy would keep of the traces stored in the last `window_minutes` (1..1440, default 60). At most 20 000 traces are examined, as a hash sample of trace ids; each weighs its adjusted count:
`{"window_minutes", "traces_examined", "sampled_fraction", "estimated_traces", "kept_trace_ratio", "kept_span_ratio", "rules": [{"name", "matched_trace_ratio", "kept_trace_ratio"}]}`
(the last entry is `baseline`). The rate limit is not simulated. Needs read access to telemetry.

### `GET /api/v1/apm/hosts/{host_id}/services?from=`
Services whose resources carried `host.id` since `from` (default 24h ago):
`{"services": [{"service_name", "service_namespace", "environment", "first_seen", "last_seen"}]}`. Unknown hosts → empty list.

### `GET /api/v1/apm/map/path?service=&transaction=&namespace=&environment=&from=&to=`
`service` and `transaction` required. `{"trace_count": 50, "nodes": ["service:frontend|shop|prod", …], "edges": ["service:frontend|shop|prod->db:postgresql/orders", …]}`:
ids of `GET /apm/map` used by up to 50 traces of the transaction ([apm.md](apm.md) §5.1).

### `GET /api/v1/apm/map?service=&namespace=&environment=`
```json
{"nodes": [{"id": "service:orders|shop|prod", "type": "service", "name": "orders", "service_namespace": "shop",
            "environment": "prod", "requests", "throughput", "error_rate", "avg_ms", "p95_ms", "apdex", "host_count": 2, "container_count": 3},
           {"id": "db:postgresql/orders", "type": "db", "name": "postgresql/orders", "service_namespace": "", "environment": "", …}],
 "edges": [{"id": "service:frontend|shop|prod->service:orders|shop|prod", "source": "…", "target": "…", "target_type": "service",
            "calls", "throughput", "errors", "error_rate", "avg_ms", "p95_ms"}]}
```
Node `type` ∈ `service`, `db`, `external`, `messaging`; dependency node RED is the sum of its incoming edges.
`host_count`/`container_count`: hosts and containers linked to a service node since `from` (0 for dependencies).
Without `service`, `namespace`/`environment` filter the whole map (sources and trace-linked targets). `service`
restricts the map to edges touching that service.

### `GET /api/v1/apm/traces?service=&namespace=&environment=&transaction=&type=&min_duration_ms=&max_duration_ms=&error=&attr.<key>=&sort=&limit=`
Entry spans in the range (`sort` = `timestamp` (default, newest first) | `duration`; `limit` default 50, max 500). Without
`service`, one row per trace. `error` = `true`|`false`. `attr.<key>=<value>`: exact match on a span attribute (at most 10;
key ≤ 256 bytes, one value ≤ 1024 bytes; `400` otherwise). Open results with `GET /traces/{trace_id}` and correlated
logs with `GET /logs?trace_id=`.
`{"traces": [{"trace_id", "span_id", "timestamp", "duration_ms", "is_error", "http_status_code", "service_name", "service_namespace", "environment", "transaction_type", "transaction_name"}]}`

## Fleet (agent updates)

Agent update policy, rollouts and agents of the caller's organization ([releases-updates.md](releases-updates.md)
§3–§4, tables: [postgres.md](postgres.md#fleet-agent-updates-0002_fleet)). Reads need any role (API keys too);
changes need a signed-in admin or owner (`403` otherwise, CSRF as usual). Not available with
`OPENLOG_AUTH_MODE=static` (`404`). Changes reach ingest within `OPENLOG_FLEET_POLICY_CACHE_TTL` and the rollout
controller within `OPENLOG_FLEET_CONTROLLER_INTERVAL`. Every change is written to the audit log (`fleet.*`).

### `GET /api/v1/fleet/policy` · `PUT /api/v1/fleet/policy`
```json
{"mode": "auto", "channel": "stable", "target": "latest", "pinned_version": null, "waves": [10, 50, 100],
 "wave_soak_minutes": 60, "halt_failure_rate": 0.05,
 "maintenance_windows": [{"days": ["sat", "sun"], "start": "02:00", "end": "05:00"}],
 "php_agent": {"mode": "manual", "version": "agent", "reload": "none", "exclude_bins": [], "changed_at": null},
 "is_default": false, "updated_at": "…", "updated_by_email": "…"}
```
`php_agent` controls installing the PHP agent through the infra agent ([php-agent.md](php-agent.md) §7.3): `mode`
`off` (fleet installations are removed) · `manual` (default: runtime inventory only) · `auto` (install and upgrade
where a supported PHP runtime is found); `version` `agent` (each host gets the PHP agent of its infra agent version)
or a SemVer version (must be a verified release when the catalog is loaded); `reload` `none` · `graceful` (PHP-FPM /
Apache reload after a change); `exclude_bins` globs (at most 50). Hosts follow the policy's `waves` and
`wave_soak_minutes` starting at the later of `changed_at` (set by the server when the section changes) and the
target's release time, and its maintenance windows. A `PUT` without `php_agent` keeps the stored section.

`PUT` takes the policy fields and returns the stored policy. `400 invalid_argument`: unknown mode/channel/target,
`waves` not strictly increasing in 1..100 or not ending at 100 (1–20 waves), `wave_soak_minutes` outside 0..43200,
`halt_failure_rate` outside 0..1, bad windows (`days` ∈ `mon`…`sun`, empty = every day; `HH:MM`, `end` may be
`24:00`, `end <= start` spans midnight; at most 50), `target=pinned` without `pinned_version`, or a pinned version
that is not a verified release (checked when the release catalog is loaded). `pinned_version` is ignored (null)
for other targets; a leading `v` is removed.

### `GET /api/v1/fleet/summary`
Counts over agents that synced within `OPENLOG_FLEET_HOST_STALE_AFTER` (`total_hosts` counts all):
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
- `not_update_capable.reason` is the agent's `install_method` (`container`, `dev`, …; `unknown` when empty).
- `outdated`: running version below the policy target (`target=patch`: below the newest patch of its minor).
  `unsupported`: below `compatibility.oldest_supported_agent` of this backend's release (or of the newest stable
  release when this version is not in the catalog); unparsable versions are unsupported.
- `target` is null for `target=patch` or when the target is not in the catalog. `update_available` = `outdated > 0`
  (shown by the UI in `notify` mode).

### `GET /api/v1/fleet/hosts?version=&state=&q=&limit=&cursor=`
Ordered by host name then id; `limit` default 100, max 1000; `next_cursor` (opaque) or null. `version` matches
the reported version exactly, `state` the reported update state, `q` a substring of host name or id.
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
    "override": {"mode": "auto", "updated_at": "…"} | null, "status": "up_to_date", "status_target": "0.4.0" | null}}],
 "next_cursor": null}
```
`php_agent.status` is the PHP agent decision (same inputs as the sync response): `offer` (installs `status_target`
at its next sync), `up_to_date`, `mode_off`, `manual`, `not_reported` (agent too old to report PHP runtimes),
`not_capable` (no privileged pre-start step, container, no release keys), `managed_elsewhere` (installed by a
deb/rpm/apk package or by hand: never changed), `no_php`, `invalid_version`, `no_catalog`, `target_unavailable`,
`no_artifact`, `already_failed` (rolled back on the host), `not_in_wave`, `outside_window`. `update.state`:
`downloading`, `restarting`, `confirming` (health check pending), `applied`, `failed`, `rolled_back`, `uninstalled`.
`status` is the update decision for the host right now: `offer` (will get `status_target` on its next sync),
`not_in_wave`, `outside_window`, `hold`, `not_capable`, `up_to_date`, `already_failed`, `incompatible` (no upgrade
path from its version / below the rollback floor), `no_artifact` (platform), `rollout_paused`, `rollout_halted`,
`rollout_outdated`, `not_in_rollout`, `no_rollout`, `notify_only`, `mode_off`, `no_catalog`, `target_unavailable`,
`invalid_version`.

### `PUT /api/v1/fleet/hosts/{host_id}/override` `{"action": "hold"|"pin", "version"}` · `DELETE …/override`
`PUT` returns `{"action", "version", "updated_at"}`; `404` for a host that never synced; `400` for another action or
a pin version that is not a verified release. `DELETE` is idempotent (`204`). A pinned host moves to its version
(upgrade or, down to the rollback floor, rollback) regardless of waves, within maintenance windows, and is not part
of fleet rollouts.

### `PUT /api/v1/fleet/hosts/{host_id}/php-agent` `{"mode": "off"|"manual"|"auto"}` · `DELETE …/php-agent`
Enables (`auto`) or disables (`off`, removes a fleet installation) the PHP agent on one host regardless of the
policy's `php_agent.mode`; an `auto` override skips waves (maintenance windows still apply). `PUT` returns
`{"mode", "updated_at"}`; `404` for a host that never synced; `400` for another mode. `DELETE` is idempotent (`204`)
and returns the host to the policy. Audit `fleet.php_agent_override.set` (`details.mode`) /
`fleet.php_agent_override.delete`.

### `GET /api/v1/fleet/rollouts?limit=`
Newest first (default 20, max 100): `{"rollouts": [Rollout]}`.
```json
{"id": "…", "action": "upgrade", "from_version": "0.3.0", "to_version": "0.4.0", "targets": {},
 "waves": [10, 50, 100], "current_wave": 1, "wave_percent": 50, "wave_started_at": "…", "next_wave_at": "…" | null,
 "wave_soak_minutes": 60, "halt_failure_rate": 0.05, "state": "halted",
 "state_reason": "failure rate 4/30 reached the halt threshold 5%",
 "counters": {"pending": 20, "attempted": 30, "succeeded": 26, "failed": 3, "rolled_back": 1},
 "created_by_email": null, "created_at": "…", "updated_at": "…", "ended_at": null}
```
`to_version` is null for `target=patch` rollouts (per-minor `targets`). `created_by_email` null = created by the
rollout controller. `next_wave_at` is set for an active rollout that has further waves (the wave advances then if
the failure rate allows).

### `POST /api/v1/fleet/rollouts/{id}/pause` · `POST /api/v1/fleet/rollouts/{id}/resume`
Return the rollout. Pause: only `active` (`409 failed_precondition` otherwise). Resume: `paused` or `halted`; the
soak time of the current wave restarts, and for a halted rollout the failures so far are acknowledged (no longer
counted towards the halt threshold). `404` for unknown ids.

### `POST /api/v1/fleet/rollouts/{id}/deploy-now`
"Deploy now": returns the rollout moved to its last wave (100 %), `wave_started_at` = now, `next_wave_at` null.
Only an `active` rollout that is not in its last wave (`409 failed_precondition` otherwise). The halt threshold and
the policy's maintenance windows still apply; agents get the update at their next sync, and agents waiting for a
wave are told to sync every `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` (60 s). Audit `fleet.rollout.deploy_now`
(`details.from_wave`, `wave`, `percent`).

### `POST /api/v1/fleet/rollback` `{"to_version"}`
Creates a rollback rollout (`201`, Rollout) from the current upgrade rollout's target (or the newest running version)
down to `to_version`, with the policy's waves; it supersedes the open rollout. `400`: not a verified release or not
lower than that version. `409`: catalog unavailable, no agent runs a newer version, or `to_version` is below the
`rollback_floor` of the version rolled back from. The controller does not start a new upgrade to the version rolled
back from; a newer release does.

## Integration settings

Endpoints and credentials of infra agent integrations (`nginx`, `redis`, `mysql`, `postgresql`, `docker`) for all hosts
of the caller's organization or for one host, delivered to agents through sync ([releases-updates.md](releases-updates.md)
§3, table: [postgres.md](postgres.md#integration-settings-0008_integration_settings)). Same permissions as Fleet: reads
need any role (API keys too); changes need a signed-in admin or owner (`403` otherwise, CSRF as usual). Not available with
`OPENLOG_AUTH_MODE=static` (`404`). Changes reach agents within `OPENLOG_FLEET_POLICY_CACHE_TTL` plus one sync interval.
Every change is audited (`integration_setting.*`). `host_id` is the agent's host id (the `host_id` of `/hosts` and
`/fleet/hosts`, the `host.id` resource attribute).

IntegrationSetting:
```json
{"id": "…", "host_id": "…" | null, "integration": "redis",
 "match": {"port": 6380 | null, "container": "", "endpoint": "", "instance": ""},
 "enabled": true, "endpoint": "127.0.0.1:6380", "username": "default", "password_set": true,
 "database": "", "databases": [], "created_at": "…", "updated_at": "…", "updated_by_email": "admin@example.com"}
```
The password is write-only: it is never returned (`password_set` tells whether one is stored) or logged.

### `GET /api/v1/integrations/settings?host_id=`
Without `host_id`: every setting of the organization (all-hosts settings first, then by host id). With `host_id`: the
settings that apply to that host, in the order the agent applies them (all-hosts settings, then the host's; without a
match first; then by creation time), and the host's revisions:
```json
{"items": [IntegrationSetting],
 "host": {"host_id": "h1", "revision": "sha256:…", "applied_revision": "sha256:…", "applied_at": "…",
          "remote_config_disabled": false} | null}
```
- `revision`: the revision sync sends this host now. `applied_revision`: the revision the agent reported in its last
  sync (`""` = none yet, or the host never synced); `applied_at`: that sync's time (null when `applied_revision` is
  `""`). The agent runs the current settings when `applied_revision == revision`. A saved change is pending until the
  agent's next sync.
- `remote_config_disabled`: the agent reported `"disabled"` (remote config turned off in its config file).
- `400` for a `host_id` over 256 bytes.

### `POST /api/v1/integrations/settings` · `PUT /api/v1/integrations/settings/{id}` · `DELETE …/{id}`
```json
{"host_id": "h1" | null, "integration": "postgresql",
 "match": {"port": 5432, "container": "", "endpoint": "", "instance": ""},
 "enabled": true, "endpoint": "127.0.0.1:5432", "username": "monitor", "password": "…" | null,
 "database": "postgres", "databases": ["app"]}
```
`POST` → `201` IntegrationSetting; `PUT` → `200` (replaces every non-secret field: omitted fields become empty,
`enabled` defaults to true); `DELETE` → `204`. Only `integration` is required; `host_id` null or `""` = all hosts.
`password`: omitted or null keeps the stored password on `PUT` (none on `POST`), `""` clears it, a value replaces it;
changing to an integration without passwords clears it. `404` for an unknown id.

- `400 invalid_argument`: unknown integration; a non-empty field the integration does not use (nginx: `endpoint`;
  redis, mysql: `endpoint`, `username`, `password`; postgresql: also `database`, `databases`; docker: only `enabled` and
  `match`); nginx `endpoint` not an `http(s)` URL with a host; other endpoints not `host:port` or
  `unix:/absolute/path`; `match.port` outside 1–65535; a string over 512 bytes or with control characters;
  more than 64 `databases` or an empty entry; `host_id` over 256 bytes.
- `409 already_exists`: another setting has the same host scope, integration and match.
- `409 failed_precondition`: a password was given but `OPENLOG_SECRETS_KEY` is not configured.

## Alerting

Rules, incidents, notification channels, mute windows and the delivery log of the caller's organization. Semantics
(rule types, evaluation, state machine, notifications, payloads, secrets): [alerting.md](alerting.md); shapes:
[openapi.yaml](openapi.yaml) tag `alerts`; tables: [postgres.md](postgres.md#alerting-0004_alerting). Reads need any role
(API keys too); writes need a signed-in user (CSRF as usual) with the role in [Roles](#roles): members create rules and
mutes and change only those they created (`403 permission_denied` otherwise), work on incidents; admins and owners change
everything and manage channels. Not available with `OPENLOG_AUTH_MODE=static` (`404`). Every write is audited
(`alert.*`, alerting.md §7).

| Endpoint | Notes |
|---|---|
| `GET /api/v1/alerts/rule-types` | `{"types": [{"type", "available", "reason"}]}` |
| `GET /api/v1/alerts/rules?type=&enabled=` · `POST /api/v1/alerts/rules` | list ordered by name; create → `201` Rule. `409` at `OPENLOG_ALERT_MAX_RULES_PER_ORG` |
| `GET /api/v1/alerts/rules/{id}` | Rule + `series` (non-ok series states) |
| `PUT /api/v1/alerts/rules/{id}` · `DELETE …` | full replacement (optional `version` → `409` when stale); changing `type`/`condition` resolves open incidents (`rule_changed`); delete resolves them (`rule_deleted`), incidents are kept |
| `POST /api/v1/alerts/rules/{id}/enable` · `…/disable` | disable resolves open incidents (`rule_disabled`) |
| `POST /api/v1/alerts/rules/preview` `{"rule": RuleInput, "hours": 1..24}` | evaluates the unsaved definition over the last hours through the tenant-scoped query layer (viewer): `{"from", "to", "step_seconds", "operator", "threshold", "recovery_threshold", "unit", "series": [{"key", "labels", "points": [[ms, value\|null]], "transitions": [{"at", "state", "value"}], "incidents": [{"opened_at", "resolved_at", "peak"}]}], "truncated", "approximate"}` |
| `GET /api/v1/alerts/rules/{id}/evaluations?from=&to=` | evaluation history from ClickHouse (alerting.md §3.6; default last 24 h, ≤ 30 days, ≤ 500 buckets): `{"from", "to", "step_seconds", "evaluations": [{"at", "firing_series", "evaluations", "errors", "duration_ms", "max_duration_ms"}], "series": [{"series_key", "labels", "points": [[ms, value\|null, state]]}], "truncated"}` |
| `GET /api/v1/alerts/incidents?state=open,acknowledged&rule_id=&severity=&limit=&cursor=` | newest first (default 50, max 500), `next_cursor`, `counts {open, acknowledged, resolved (7 d)}` |
| `GET /api/v1/alerts/incidents/{id}` | Incident + `events` (timeline) + `deliveries` |
| `POST /api/v1/alerts/incidents/{id}/acknowledge` · `…/resolve` `{"note"?}` · `…/notes` `{"text"}` | ack is idempotent (`409` when resolved; stops re-notifications); resolve enqueues resolve notifications; note → `201` event |
| `GET /api/v1/alerts/channels` · `POST` · `GET/PUT/DELETE /api/v1/alerts/channels/{id}` | secrets are write-only (`secret_hints` masked; omitted secret fields keep stored values; `generated_secrets` once on create); list carries `secrets_configured`. `409` without `OPENLOG_SECRETS_KEY` |
| `POST /api/v1/alerts/channels/{id}/test` | synchronous test send: `{"success", "status_code", "error", "duration_ms", "notification_id"}` (always `200` when the channel exists) |
| `GET /api/v1/alerts/mutes?include_expired=` · `POST` · `PUT/DELETE /api/v1/alerts/mutes/{id}` | `starts_at`/`ends_at` (≤ 90 days), `rule_ids`, label `matchers`; `active` computed. Recurring: `schedule {timezone, days\|rrule, start_time, end_time, from, until}` (alerting.md §5.2); responses then carry the current or next occurrence in `starts_at`/`ends_at` |
| `POST /api/v1/alerts/mutes/preview` `{"schedule"}` | any role: validates an unsaved schedule, `{"occurrences": [{"starts_at", "ends_at"}]}` (≤ 5, exceptions applied). Mute responses carry the same list as `upcoming`; schedules accept `FREQ=MONTHLY` rules, `exdates` and `holiday_calendar_ids` (alerting.md §5.2) |
| `GET /api/v1/alerts/holiday-calendars` · `POST` · `GET/PUT/DELETE /api/v1/alerts/holiday-calendars/{id}` | named holiday date sets `{name, description, dates: ["YYYY-MM-DD" \| "MM-DD"]}` (≤ 1000) with `mute_count`; writes admin/owner; delete → `409` while mutes reference it |
| `GET /api/v1/alerts/templates?category=&integration=` | recommended rule templates (alerting.md §2.8): `{"templates": [{"id", "category", "integration", "rule_type", "severity", "metric", "reference_metric", "name": {en, tr}, "description", "params": [...]}]}` |
| `POST /api/v1/alerts/templates/{id}/render` `{"params", "language", "name", "channel_ids"}` | viewer (reads telemetry for ratio thresholds): `{"rule": RuleInput, "reference": {"metric", "value", "ratio"} \| null}`; `400` for invalid params, `404` unknown template, `409` when a ratio reference is missing or 0 |
| `GET /api/v1/alerts/deliveries?channel_id=&incident_id=&status=&limit=` | delivery log newest first (default 100, max 500) with `attempt_log` |

## Query language (OQL)

Language: [oql.md](oql.md). Any role and API keys (`telemetry.read`); the tenant comes from the principal as for every
telemetry endpoint. Available in both auth modes. Responses carry `Cache-Control: no-store`.

### `POST /api/v1/query` `{"query", "from"?, "to"?, "variables"?}`
`from`/`to` (RFC3339 or unix ms, both or neither) override `SINCE`/`UNTIL` (dashboards' time picker). `variables`:
`{"name": "value" | ["v1", "v2"]}` (§3 of oql.md). Result:
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
- `single`: no `FACET`/`TIMESERIES`, one row without facets. `facets`: one row per group (ordered by the first column,
  descending; `truncated` = more groups than `LIMIT`). `timeseries`: one series per group × column, points
  `[bucket start ms, value]` for every bucket of the range. `histogram`: `buckets` only. Unused arrays are `[]`.
- `400 invalid_argument`: syntax/validation error (message `line L, column C: …`), range or size limits (oql.md §5).
- `422`/`429`/`504`: organization query limits and timeout (as other telemetry endpoints).

### `POST /api/v1/query/validate` `{"query", "variables"?}`
Parses and plans without reading ClickHouse (`200` also for invalid queries):
```json
{"valid": false, "event_type": "Log" | null, "kind": "facets" | null, "variables": ["host"],
 "errors": [{"message": "unknown attribute \"sevrity\" for Log", "offset": 30, "length": 7, "line": 1, "column": 31}],
 "warnings": [{"message": "…", "offset": 0, "length": 0, "line": 1, "column": 1}]}
```

### `GET /api/v1/query/schema?event_type=`
Event types with their attributes (`name`, `type`, `aliases`, `rollup`), `maps` (`attributes`, `resource`) and
`max_range_seconds`; functions (`name`, `signature`, `description`); keywords. With `event_type`, also the most frequent
map keys of the last hour (`attribute_keys`, `resource_keys`, ≤ 200 each) and, for `Metric`, `metric_names` (≤ 500).

## Dashboards

Custom dashboards of OQL widgets (`OPENLOG_AUTH_MODE=postgres` only; `404` otherwise). Table: [postgres.md](postgres.md#dashboards-0020_dashboards).
Reads: any role and API keys; `visibility: "private"` dashboards are visible only to their creator (and to admins once
the creator's account is deleted). Writes need a signed-in user (CSRF as usual): members create, import and duplicate
dashboards and change/delete their own; admins and owners change/delete any `org` dashboard (`403` otherwise). Every
write is audited (`dashboard.{create,update,delete,duplicate,import}`, target `dashboard`).

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
Validation (`400`): name 1–200 characters, description ≤ 2000; 1–20 pages (an empty list becomes one page "Page 1"),
page names 1–100; ≤ 100 widgets per page; layout on a 12-column grid (`0 ≤ x`, `1 ≤ w ≤ 12`, `x + w ≤ 12`, `0 ≤ y ≤ 10000`,
`1 ≤ h ≤ 50`); non-markdown widgets need a query that parses and validates (oql.md; variables may be unset), markdown
widgets ≤ 20 000 characters; ≤ 10 variables with unique names (`[A-Za-z_][A-Za-z0-9_]{0,63}`), `query` variables need an
OQL query with `FACET` (values = first facet), ≤ 200 `values`; ≤ 10 thresholds per widget. Widget and page ids are kept when
the input carries a valid id of the same dashboard, otherwise new ids are assigned.

### `GET /api/v1/dashboards?q=`
`{"dashboards": [{"id", "name", "description", "visibility", "page_count", "widget_count", "created_by_email",
"updated_at", "can_edit"}]}`, by name; `q` filters by name substring (case-insensitive).

### `POST /api/v1/dashboards` · `GET /api/v1/dashboards/{id}` · `PUT /api/v1/dashboards/{id}` · `DELETE /api/v1/dashboards/{id}`
Input: `{"name", "description", "visibility", "variables", "pages", "version"}` (`version` required on `PUT`: the
version that was read; `409 failed_precondition` when the dashboard changed meanwhile). `POST` → `201` Dashboard, `PUT`
→ `200` Dashboard (version + 1, whole document replaced), `DELETE` → `204`. Unknown or invisible id → `404`.

### `POST /api/v1/dashboards/{id}/widgets` `{"page_id"?, "widget"}`
Appends one widget below the existing ones of the page (default: first page; `layout.y` is recomputed) → `200`
Dashboard. Same permission as `PUT`.

### `POST /api/v1/dashboards/{id}/duplicate` `{"name"?}`
Copies a readable dashboard as a new dashboard of the caller (default name `"<name> (copy)"`, same visibility) → `201`.

### `GET /api/v1/dashboards/{id}/export` · `POST /api/v1/dashboards/import`
Export: `{"openlog_dashboard": 1, "name", "description", "variables", "pages": [{"name", "widgets": [widget without
id]}]}`. Import takes the same document (plus optional `"visibility"`), validates it like `POST` and answers `201`
Dashboard; unknown `openlog_dashboard` versions → `400`.

### Cross-widget filters
Clicking a facet value in a widget (table row, bar, pie slice, billboard, heatmap row or chart legend entry) adds a
dashboard filter `attribute = value` (several values of one attribute are separate filters and all must match). The web UI
keeps the filters in the URL (`filters`) and sends them with every widget query as `POST /api/v1/query`
`"filters": [{"attribute": "host.name", "value": "web-1", "event_type": "Log"}]` (≤ 10). Each filter is ANDed with the
widget query's `WHERE` at plan level (bound parameter) only where the widget's event type has the attribute; skipped
filters are listed in `metadata.ignored_filters` ([oql.md](oql.md) §7). Filters are not saved with the dashboard and do
not apply to share links or reports.

### Version history: `GET /api/v1/dashboards/{id}/versions` · `GET …/versions/{version}` · `POST …/versions/{version}/restore`
Every save (create, `PUT`, add widget, restore) stores the whole document as a new version; the newest 50 versions of a
dashboard are kept (D-086). Reads: whoever can read the dashboard.
List: `{"versions": [{"version", "author_user_id" | null, "author_email", "created_at", "restored_from" | null,
"page_count", "widget_count"}], "current_version", "can_restore"}`, newest first.
One version: the list entry plus `"document": {"name", "description", "visibility", "variables", "pages"}` (with page and
widget ids), `"current_version"`, `"previous_version"` (the next older stored version, or `null`), `"changes"` (diff from
the previous version to this one, `null` without one) and `"differences_from_current"` (diff from the current document to
this version, i.e. what a restore changes). Diff: `{"name", "description", "visibility", "variables": bool,
"pages_added" | "pages_removed" | "pages_renamed": [{"id", "name"}], "widgets_added" | "widgets_removed" |
"widgets_changed": [{"id", "title", "page", "fields"?: ["title" | "visualization" | "layout" | "query" | "markdown" |
"unit" | "thresholds" | "options" | "page"]}]}` (pages and widgets matched by id; `page` is the page name).
Restore `{"version"}` (the current version that was read; `409` when it changed) saves the stored document as a new
version with `restored_from` → `200` Dashboard. Same permission as `PUT`; the dashboard keeps its current visibility; page
and widget ids of the stored version are kept; a stored query that no longer validates → `400`. Unknown or pruned version
→ `404`. Audit: `dashboard.restore`. Saves by api pods of an older release (rolling upgrade) are missing from the history.

### Sharing settings: `GET /api/v1/dashboards/settings` · `PUT /api/v1/dashboards/settings` `{"share_links_enabled", "report_domains"?}`
Organization-wide: `{"share_links_enabled": false, "report_domains": [], "updated_at" | null, "can_edit"}`. Share links are
off by default. `report_domains` (≤ 20 domain names, lower-cased, a leading `@` is dropped) are the e-mail domains
scheduled reports may be sent to besides members. Reads: every role and API keys; `PUT`: signed-in admins and owners
(`403` otherwise). Disabling share links stops all existing links immediately (they are kept and work again when
re-enabled, unless expired or revoked). Audit: `dashboard.settings.update`.

### Share links: `GET /api/v1/dashboards/{id}/shares` · `POST /api/v1/dashboards/{id}/shares` · `DELETE /api/v1/dashboards/{id}/shares/{share_id}`
Read-only public links to one dashboard (D-087). Create: editors of the dashboard (same as `PUT`); list and revoke: editors
and admins/owners who can read the dashboard. `POST` `{"label"?, "expires_at", "range"}` or `{"label"?, "expires_at",
"from", "to"}`, optional `"variables": {"name": ["v", …]}`:
- `expires_at` is required, 5 minutes to 90 days ahead (links cannot be extended; create a new one);
- `range` is a relative range `<n>m`, `<n>h` or `<n>d` (1 minute – 31 days, ending at the time of each request); `from`/`to`
  is a fixed range (≤ 31 days);
- `variables` lock variable values (names of the dashboard's variables; `"*"` or no value = All); viewers cannot change them;
- ≤ 20 active links per dashboard; label ≤ 100 characters; share links disabled → `409 failed_precondition`.

→ `201` `{"id", "label", "range" | null, "from" | null, "to" | null, "variables", "created_by_email", "created_at",
"expires_at", "revoked_at" | null, "last_used_at" | null, "use_count", "active", "token", "path"}`. The token (`olds_` + 43
base64url characters, 256 random bits) is returned only in this response; only its SHA-256 hash is stored. `path` is the
web UI page `/shared/dashboards/{token}`. `GET` returns `{"shares": [same objects without token and path],
"share_links_enabled"}`, newest first; `DELETE` revokes (idempotent) → `200` share. `last_used_at` and `use_count` are
updated at most once a minute per link. Audit: `dashboard.share.create`, `dashboard.share.revoke`, and
`dashboard.share.access` (no actor, client IP) when a link is used after more than an hour without use.

A link stops working when it expires or is revoked, share links are disabled for the organization, its creator is no
longer a member of the organization, or the dashboard is deleted.

### Public share link endpoints (no authentication)
`GET /api/v1/public/dashboards/{token}` → `{"name", "description", "pages": [{"id", "name", "widgets": [{"id", "title",
"visualization", "layout", "markdown", "unit", "thresholds", "options"}]}], "variables": [{"name", "label", "values"}],
"time_range": {"range" | null, "from" | null, "to" | null}, "expires_at"}`. No query text, version, creator, e-mail
addresses, dashboard or organization ids or names are returned.
`GET /api/v1/public/dashboards/{token}/widgets/{widget_id}/result` → the result of the widget's stored query (shape of
`POST /api/v1/query`) for the link's time range and locked variables, run with the owning organization's tenant scope,
query limits and timeout; `metadata.table`, `rows_read` and `bytes_read` are blanked and `warnings` is empty. The request
takes no parameters: clients cannot run other queries or change the range, variables or filters. Markdown or unknown
widget → `404`; a stored query that no longer validates → `422`.
Unknown, expired, revoked or disabled links → `404 not_found` with one message for all cases. Rate limits (cluster-wide,
D-096): 600 requests per minute per client IP, 1200 per link, and once an IP has 30 failed lookups within a minute every
request of that IP is refused → `429 resource_exhausted` with `Retry-After`. The windows slide (previous minute weighted
by its unelapsed share + current minute) and are shared by all api pods through PostgreSQL (`rate_limit_counters`,
postgres.md): each pod decides requests far from a limit locally and writes its counts once a second, and writes
synchronously (exact enforcement) once its unwritten share reaches 1/8 of the remaining headroom — so with up to 8 pods
the limits are not exceeded, beyond that by at most (pods/8 − 1) × the remaining headroom. Failed lookups counted on
another pod are seen within about a second. When PostgreSQL is unreachable each pod counts on its own for 5 seconds
before retrying (limits then apply per pod); static auth mode (no PostgreSQL) always counts per pod. Every response carries
`Cache-Control: no-store`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'; base-uri 'none';
form-action 'none'`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex, nofollow`,
`X-Content-Type-Options: nosniff` and `Cross-Origin-Resource-Policy: same-origin`; no CORS headers. The web UI page
`/shared/dashboards/{token}` is served with `Referrer-Policy: no-referrer` and `X-Robots-Tag: noindex, nofollow` and shows
the dashboard read-only without navigation.

### Scheduled reports: `GET /api/v1/dashboards/{id}/reports` · `POST` · `PUT …/reports/{report_id}` · `DELETE …/reports/{report_id}`
E-mail summaries of a dashboard (D-087). Create: editors of the dashboard; list, change and delete: editors and
admins/owners who can read the dashboard. Input: `{"name"?, "frequency": "daily" | "weekly", "weekday"?: 0–6 (0 = Sunday;
weekly), "hour": 0–23, "minute"?: 0–59, "timezone"?: IANA time zone (default `"UTC"`), "recipients": ["…"] (1–20),
"language"?: "en" | "tr", "range"?: relative range (default `"24h"` daily, `"7d"` weekly), "variables"?: {…},
"enabled"?: true}`. Recipients must be members of the organization or in its `report_domains`; reports of a `private`
dashboard only go to its creator (`400` otherwise). ≤ 10 reports per dashboard. → `201`/`200` `{"id", "name", "frequency",
"weekday", "hour", "minute", "timezone", "recipients", "language", "range", "variables", "enabled", "created_by_email",
"created_at", "updated_at", "next_run_at" | null, "last_run": {"period", "status": "running" | "sent" | "partial" |
"failed" | "skipped", "error", "recipients", "started_at", "finished_at" | null} | null}`; `DELETE` → `204`. Audit:
`dashboard.report.{create,update,delete}`.

Sending (api leader, job `dashboard-reports`, checked every minute): a report is due when its latest scheduled local time
passed within the last 6 hours and after the report was created or last changed. The period (local date) is claimed in
`dashboard_report_runs` before anything is sent, so a period is sent at most once, also across leader changes (a crash
while sending is not retried). Removing a member removes their address from recipient lists at once (D-096). Recipients are checked again before sending (members who left and removed domains are
skipped). The stored queries of up to 50 widgets (markdown widgets are skipped) run server-side for
`[scheduled time − range, scheduled time)` with the organization's tenant scope, query limits and the report's variables.
The e-mail (subject, text and HTML in the report's language) shows single values, facet tables, and average/min/max/last
per timeseries series as simple HTML tables (≤ 20 rows per widget) with a link to the dashboard for that period
(`OPENLOG_PUBLIC_URL`). Without `OPENLOG_SMTP_HOST` runs are recorded as `failed`.
PNG chart images (D-097, [operations/reports.md](../operations/reports.md)): with `OPENLOG_RENDERER_URL` the leader signs
a render token for the report period and asks `openlog-renderer` (separate image, headless Chromium) for the images of
the web app's print view `/print/dashboard`. Widgets that got an image are shown as inline `image/png` parts
(`multipart/related`, `Content-ID: <widget-N@openlog>`, ≤ 1 MiB each, ≤ 10 MiB per e-mail) instead of their table; the
plain text part always carries the tables. A renderer error or timeout (`OPENLOG_RENDERER_TIMEOUT`), a widget whose query
failed or an image over the caps falls back to the table; the run is still `sent`.

### Report print view: `GET /api/v1/render/dashboard` · `GET /api/v1/render/dashboard/widgets/{widget_id}/result`
Internal endpoints for `openlog-renderer`; they exist only with `OPENLOG_RENDERER_URL`. Authentication:
`Authorization: Bearer olrt_…` render token only (sessions, API keys and share tokens are refused; the render token is
refused by every other endpoint). A token is `olrt_` + base64url(JSON claims) + `.` + base64url(HMAC-SHA256), signed with
a key derived from `OPENLOG_KEY_HASH_SECRET` (else `OPENLOG_SECRETS_KEY`), valid ≤ 10 minutes (the job uses 5), bound to
one organization and its tenant, one dashboard, one enabled scheduled report and the report period. Responses are those of
the share link endpoints (`SharedDashboard` with `time_range.from`/`to` = the report period, `expires_at` = token expiry;
the redacted OQL result) with the same headers. Invalid, expired or forged token → `401 unauthenticated`; deleted
dashboard, deleted or disabled report → `404`.

## Usage and plans

Usage metering, plan limits, quota status and billing ([usage.md](usage.md), D-079–D-081). Reads: every role of the
organization and API keys. Export: admin, owner. Plan assignment: superadmins only (`OPENLOG_SUPERADMIN_EMAILS`, a
session user with a verified e-mail address; organization membership not required). `period` is `current` (default),
`previous` or `YYYY-MM` (UTC calendar month); anything else → `400`. In static auth mode the default plan applies and
the admin and webhook endpoints do not exist.

### `GET /api/v1/usage?period=`
`{"organization": {"id","name","tenant_id"}, "period": {"id","start","end","data_until"}, "saas_mode", "plan":
{"id","name","description","limits","enforcement"}, "plan_assigned", "usage": {"signals": [{"signal","items","bytes",
"ingest_bytes","ingest_requests"}], "ingest_bytes", "hosts", "containers", "services", "active_hosts", "query":
{"queries","failed","read_rows","read_bytes","cpu_seconds","memory_bytes"}}, "stored": [{"signal","retention_days",
"bytes","compressed_bytes"}], "limits": [{"metric","used","limit","percent","level"}], "level", "ingest_blocked",
"projection": {"ingest_bytes","ingest_percent"?}, "can_manage_plan", "billing_enabled"}`. `plan` is the effective plan
(overrides applied); `limits` is evaluated at request time (`metric` `ingest_bytes`, `hosts`, `users`; `limit` 0 =
unlimited; `level` `ok`, `warning`, `exceeded`). `stored` covers each signal's effective retention up to now.

### `GET /api/v1/usage/daily?period=`
`{"period", "days": [{"day": "YYYY-MM-DD", "signals": […], "ingest_bytes", "hosts", "containers", "services", "query"}]}`
— days with any usage, ascending.

### `GET /api/v1/usage/top?period=&by=service|host&limit=`
`{"period", "by", "entries": [{"key", "items", "bytes", "bytes_by_signal": {"traces","logs","metrics"}}]}` ordered by
stored bytes; `limit` 1–100 (default 10).

### `GET /api/v1/usage/export?period=&format=csv|json`
Admin, owner. Attachment `openlog-usage-<tenant>-<period>.<format>`. CSV (`text/csv`): `tenant_id,period,day,metric,value`
long format (usage.md §9); JSON: `{"organization", "plan_id", "period", "totals", "days"}`.

### `GET /api/v1/usage/status`
Latest stored evaluation (every minute, api leader): `{"level", "ingest_blocked", "metrics": [QuotaMetric], "saas_mode",
"plan_id"?, "evaluated_at"?, "period_start"?}`; `{"level": "ok", "metrics": []}` before the first evaluation. Used by the
UI banner.

### `GET /api/v1/plans`
`{"plans": [Plan], "default"}` — the configured catalog.

### `GET /api/v1/usage/query-limits` · `PUT /api/v1/usage/query-limits` · `DELETE /api/v1/usage/query-limits`
ClickHouse query limits of the organization and where each comes from ([usage.md](usage.md) §4.5; postgres auth mode).
Read: every role and API keys. `PUT`/`DELETE`: superadmins; owners (session) only when `OPENLOG_SAAS_MODE` is off —
otherwise `403`. Response `{"defaults", "plan", "organization": {"max_memory_usage","max_rows_to_read","max_bytes_to_read",
"updated_at","updated_by"} | null, "environment": {…} | null, "effective", "sources": {"max_memory_usage": "default"|"plan"|
"organization"|"environment", …}, "can_manage", "refresh_seconds"}`; value objects carry the three settings (bytes, rows,
bytes; `0` = not set by openlog). `PUT` body `{"max_memory_usage"?, "max_rows_to_read"?, "max_bytes_to_read"?}` replaces
the organization setting: a number (`0`–`2^62`) sets the value, `null`/absent inherits; negative → `400`; all absent =
`DELETE`. Both answer the new state; other pods apply it within `refresh_seconds`. Audit `query_limits.update` /
`query_limits.delete`.

### `GET /api/v1/admin/orgs/{org}/plan` · `PUT /api/v1/admin/orgs/{org}/plan`
Superadmin. `{org}` is the organization id or tenant id (`404` when unknown). Response `{"organization", "plan_id",
"assigned", "overrides", "billing": {"provider","customer_id","subscription_id"}, "note", "updated_at", "updated_by",
"effective": Plan}`. `PUT` body `{"plan_id", "overrides"?, "billing"?, "note"?}`: unknown `plan_id`, invalid overrides or a
`retention_days` override longer than the longest plan retention → `400`. Absent `overrides`/`billing`/`note` keep the
stored values. Audit `plan.update`.

### `POST /api/v1/billing/webhooks/{provider}`
Public; the configured provider (`OPENLOG_BILLING_PROVIDER`) verifies the signature. Another or no provider → `404`;
invalid signature or an unmapped plan reference → `400`. `200 {"received": true, "changed"}`.

### Ingest: tenant quota (`429`)
In SaaS mode OTLP ingest answers `429` + `Retry-After` (gRPC `RESOURCE_EXHAUSTED` + `RetryInfo`) with a
`google.rpc.Status` message when the organization's monthly ingest quota is used up or its ingest rate limit is exceeded
(usage.md §4.4).

### Ingest: suspended organization (`403`) and host limit (`429`)
In SaaS mode (D-105) the `google.rpc.Status` body carries a `google.rpc.ErrorInfo` detail (`domain` `openlog`):

- Suspended organization: HTTP `403` / gRPC `PERMISSION_DENIED`, reason `org_suspended`, message
  `organization suspended: ingest is disabled …`. Not retryable by design (no `Retry-After`).
- Plan host limit (`limits.hosts`): a request whose resources all carry `host.id` values beyond the limit gets HTTP
  `429` + `Retry-After: 300` / gRPC `RESOURCE_EXHAUSTED` + `RetryInfo`, reason `quota_exceeded`, message
  `quota_exceeded: host limit of N hosts …`. When the request also carries admitted hosts, only the resources of the
  rejected hosts are dropped and the response is `200` with OTLP partial success (`rejected_*` counts and the same
  message). Hosts active today or yesterday always keep reporting; resources without `host.id` are not limited.

## SaaS operations

Operator console, organization lifecycle and support access (D-105, D-106; operator guide
[saas.md](../operations/saas.md) §8–§11). Operators are superadmins (`OPENLOG_SUPERADMIN_EMAILS`, session users with a
verified address); every other caller gets `403 permission_denied` on `/api/v1/operator/*` except `GET /operator/me`.
Every operator action needs a `reason` (3–1000 characters) and writes an audit event with the operator as actor into the
**organization's** audit log. `{org}` is the organization id or tenant id. Available with `OPENLOG_AUTH_MODE=postgres`;
enforcement (suspension, limits, trials, flags) additionally needs `OPENLOG_SAAS_MODE=true`.

### `GET /api/v1/operator/me`
Any session. `{"operator": bool, "saas_mode": bool}` (the UI shows the console only for operators).

### `GET /api/v1/operator/orgs?q=&plan=&state=&sort=&limit=&offset=`
`q`: name, tenant id, organization id or member e-mail substring. `plan`: effective plan id. `state`: `active`,
`suspended`, `trial`, `flagged`. `sort`: `created` (default, newest first), `name`, `ingest`, `members`, `last_ingest`.
`limit` 1–200 (50). Response `{"organizations": [OperatorOrg], "total", "saas_mode"}` with `id`, `tenant_id`, `name`,
`created_at`, `plan_id`, `plan_assigned`, `state`, `suspended_at`, `suspend_reason`, `trial_plan_id`, `trial_ends_at`,
`support_access_until`, `members`, `active_hosts` and `ingest_bytes` / `quota_level` (latest quota evaluation of the
current period), `last_ingest_at` (latest license key use), `open_flags`.

### `GET /api/v1/operator/orgs/{org}`
`{"organization": OperatorOrg + {"member_list": [{user_id,email,name,role,joined_at,last_login_at,email_verified,disabled}] (≤ 500),
"pending_invitations", "keys": {license_keys_active, license_keys_revoked, api_keys_active, scim_tokens_active,
last_ingest_at}, "sso_connections": [{id,protocol,name,enabled,enforce,jit_enabled,last_test_ok}], "verified_domains",
"flags", "support_sessions", "support_access_granted_by"}, "plan": OrgPlan, "quota": UsageStatus|null, "lifecycle",
"audit": [50 newest events, without IP addresses], "usage": {"period", "days", "available"}, "saas_mode"}`. Never secrets,
key values, SSO configuration or IdP metadata. `usage.available=false` when ClickHouse cannot be read.

### Actions
All `POST` with `{"reason"}` (trial: see below):

| Path | Effect | Audit |
|---|---|---|
| `/operator/orgs/{org}/suspend` | Suspend (idempotent). Ingest `403 org_suspended`; members' mutating API requests `403 org_suspended` except sign-in/out, profile, sessions, support access and data export/deletion; queries keep working | `org.suspend` |
| `/operator/orgs/{org}/unsuspend` | Lift the suspension; `409` when not suspended | `org.unsuspend` |
| `/operator/orgs/{org}/trial` | Body `{"plan_id"?, "days"? (1–365) \| "ends_at"?, "reason"}`. Without a running trial: assigns `plan_id` and starts its trial (`days`/`ends_at`, default the plan's `trial_days`). With one: moves its end (`days` are added to the current end) | `plan.update`, `trial.start` / `trial.extend` |
| `/operator/orgs/{org}/reset-quota-notifications` | Deletes the current period's `usage_notifications` so threshold e-mails can be sent again; `{"deleted","period"}` | `quota.notifications_reset` |
| `/operator/orgs/{org}/force-logout` | Revokes every active session of the members (all their organizations), not the operator's; `{"sessions_revoked"}` | `org.force_logout` |
| `/operator/orgs/{org}/resend-verification` | New verification e-mail to every unverified owner; `409` when none; `{"sent_to"}` | `org.owner_verification_resend` |

### Support sessions
- `POST /api/v1/operator/orgs/{org}/support-sessions` `{"reason"}` → `201 SupportSession` `{id, org_id, org_name,
  tenant_id, operator_email, reason, started_at, expires_at, ended_at}`. Requires support access granted by an owner
  (`403 support_access_required`); lasts `OPENLOG_SAAS_SUPPORT_SESSION_TTL`, never beyond the grant. Audit
  `support.session_start`.
- While open, the operator's UI sends `X-Openlog-Support-Session: <id>` on its API requests: the request acts as a
  **viewer** of that organization. Only for the operator's own session (the id is bound to user and session);
  mutating requests other than queries/previews → `403 support_read_only`; an ended/expired session or revoked access
  → `403 support_session_ended`. Every distinct request is audited as `support.request` (`method`, `path`; at most once a
  minute per path per api pod). `/api/v1/operator/*` ignores the header.
- `POST /api/v1/operator/support-sessions/{id}/views` `{"path"}` → `204`, audit `support.page_view` (UI navigation).
- `GET /api/v1/operator/support-sessions` → the operator's open sessions; `DELETE …/{id}` ends one (`support.session_end`).

### Abuse flags
`GET /api/v1/operator/flags?status=open|dismissed|actioned|all&limit=` → `{"flags": [{id, org_id, org_name, tenant_id,
kind (ingest_spike|new_org_hosts|ingest_source_ips), status, details, occurrences, first_seen_at, last_seen_at,
auto_suspended, resolved_by_email, resolved_at, resolution_note, org_suspended}]}`.
`POST /api/v1/operator/flags/{id}/resolve` `{"status": "dismissed"|"actioned", "note"}` → the flag; `404` when not open.
Audit `abuse_flag.resolve`.

### Organization side
- `GET /api/v1/orgs/current/saas` (any member, session) → `{"saas_mode", "suspended", "trial": {plan_id, plan_name,
  ends_at, fallback_plan_id}|null, "support_access": {until, granted_at}|null, "support_session": {id, operator_email,
  expires_at, org_name}|null (only inside a support view), "can_manage_support_access"}`.
- `PUT /api/v1/orgs/current/support-access` `{"duration": "24h"|"7d"}` (owners, own session, not in a support view) →
  the state above; audit `support_access.grant`. `DELETE` revokes it and ends open support sessions
  (`support_access.revoke`).

### Plan users limit
In SaaS mode creating an invitation (members + pending invitations), accepting one, SSO just-in-time provisioning and
SCIM provisioning beyond the effective `limits.users` fail: API `403 quota_exceeded` with the plan, limit and count;
SSO sign-in redirects with `?sso_error=user_limit`; SCIM answers `403`.

## Data export and deletion

Data subject requests (KVKK/GDPR; D-107, [saas.md](../operations/saas.md) §12). Postgres auth mode; every endpoint
except the download link needs a signed-in user (API keys are refused with `403`).

Destructive operations re-authenticate: `password` must be the user's current password (5 wrong attempts per
15 minutes, then `429`); users without a password (single sign-on) must use a session created within the last
10 minutes, otherwise `403` ("sign in again with single sign-on, then confirm within 10 minutes").

### `GET /api/v1/account/privacy`
`{"has_password", "data_export_enabled", "org_deletion_grace_seconds", "reauth_max_age_seconds", "org_deletions": [OrgDeletion]}`
— `org_deletions` are the scheduled or running deletions of organizations where the caller is an owner (those
organizations are no longer selectable, so this is where owners see and cancel them).

### `GET /api/v1/account/data-exports` · `POST /api/v1/account/data-exports`
Personal exports of the caller (newest 20) / request one (`202 {"export": DataExport}`; `409 failed_precondition` while
one is pending or running, `429` after 5 within 24 hours).

### `POST /api/v1/account/delete` `{"confirm_email", "password"?}`
Deletes the caller's account (`204`, session cookie cleared). `400` when `confirm_email` is not the caller's address;
`409 failed_precondition` when the caller is the only owner of an organization (message names them). Confirmation
e-mail to the old address; installation-level audit `user.delete` with the pseudonym.

### `GET /api/v1/data-exports` · `POST /api/v1/data-exports` `{"from"?, "to"?, "signals"?: ["logs", "traces", "metrics"]}` (owner)
Organization exports (newest 50) / request one: without `signals` only PostgreSQL data; with signals `from`/`to` are
required and the range must not exceed `OPENLOG_DATA_EXPORT_MAX_RANGE` (`400`). `202 {"export": DataExport}`, same
`409`/`429` as personal exports. Audit `data_export.request`.

`DataExport`: `id`, `kind` (`organization`/`user`), `status` (`pending`, `running`, `completed`, `failed`, `expired`),
`signals`, `from`, `to`, `requested_by`, `size_bytes`, `telemetry_rows`, `truncated`, `error`, `created_at`,
`started_at`, `completed_at`, `expires_at`, `download_available`.

### `GET /api/v1/data-exports/{id}` · `GET /api/v1/data-exports/{id}/download`
One export / its ZIP archive (`application/zip`, `Content-Disposition: attachment`). Visible to owners of the export's
organization (current organization) and to the subject of a personal export; anything else is `404`. Audit
`data_export.download` for organization exports.

### `GET /api/v1/data-exports/download?token=` (public)
The archive through the e-mailed link until `expires_at` (`404` for unknown, expired or deleted archives). Responses
carry `Cache-Control: no-store` and `Referrer-Policy: no-referrer`.

### `POST /api/v1/orgs/current/deletion` `{"confirm_name", "password"?}` (owner)
Schedules the current organization's deletion (`202 {"deletion": OrgDeletion}`): `confirm_name` must equal the
organization name (`400`), `409 already_exists` when already scheduled. Members lose access immediately, license and
SCIM keys are revoked; hard deletion after `OPENLOG_ORG_DELETION_GRACE`. Audit `org.deletion_schedule`; owners are
e-mailed.

`OrgDeletion`: `id`, `organization_id` (null once deleted), `organization_name`, `tenant_id`, `status` (`scheduled`,
`cancelled`, `deleting`, `completed`), `initiator` (`owner`/`operator`), `requested_at`, `purge_after`, `cancelled_at`,
`started_at`, `completed_at`, `cancellable`, `certificate_id`; operator views add `reason`, `requested_by_email`,
`last_error`.

### `POST /api/v1/org-deletions/{id}/cancel` (owner)
Cancels a `scheduled` deletion of an organization the caller owns (`200 {"deletion"}`); `404` for other ids, `403` when
an operator scheduled it, `409` once it is `deleting`. Keys revoked by the scheduling work again. Audit
`org.deletion_cancel`.

### `POST /api/v1/admin/orgs/{org}/deletion` `{"reason", "immediate"?: false}` (superadmin)
`{org}` is an organization id or tenant id. Schedules a deletion with a reason (1–1000 characters); `immediate` sets
`purge_after` to now. Owners are informed and cannot cancel. Installation-level audit `admin.org_deletion_schedule`.

### `GET /api/v1/admin/org-deletions?limit=` · `POST /api/v1/admin/org-deletions/{id}/cancel` (superadmin)
All deletions, newest first (default 100, max 500) / cancel a scheduled one (audit `admin.org_deletion_cancel`).

### `GET /api/v1/admin/deletion-certificates?subject_hash=&limit=` (superadmin)
`{"certificates": [{"id", "subject_type", "subject_hash", "initiator", "requested_at", "grace_ended_at", "started_at",
"completed_at", "postgres_rows", "clickhouse_rows", "verified"}]}`; `subject_hash` = hex sha256 of a tenant id or user id.

## Status page

Public status page (D-108, [saas.md](../operations/saas.md) §13); only with `OPENLOG_STATUS_PAGE_ENABLED` (else `404`).

### `GET /api/v1/status` (public)
`Cache-Control: public, max-age=30`, `Access-Control-Allow-Origin: *`. `503 unavailable` when PostgreSQL cannot be read.

```json
{"status": "operational", "checked_at": "…",
 "components": [{"id": "ingest", "status": "operational", "uptime_90d": 99.98,
                 "days": [{"date": "2026-06-17", "status": "no_data", "uptime": null}, …]}],
 "incidents": [StatusIncident], "maintenance": [StatusIncident], "history": [StatusIncident]}
```

Statuses: `operational`, `degraded`, `partial_outage`, `major_outage`, `maintenance`, `unknown` (no self-check within
5 minutes). Components `ingest`, `query_api`, `alerting`, `processing`; `days` has 90 entries (oldest first, status
`operational`/`degraded`/`outage`/`no_data`). `incidents`: open incidents; `maintenance`: scheduled or running windows;
`history`: resolved/completed within 14 days.

`StatusIncident`: `id`, `kind` (`incident`/`maintenance`), `title`, `status` (incident: `investigating`, `identified`,
`monitoring`, `resolved`; maintenance: `scheduled`, `in_progress`, `completed`), `impact` (`none`, `minor`, `major`,
`critical`), `components`, `starts_at`, `ends_at`, `created_at`, `updated_at`, `updates` (newest first: `id`, `status`,
`message`, `created_at`).

### `GET /api/v1/admin/status/incidents?limit=` · `POST /api/v1/admin/status/incidents` (superadmin)
List (`{"incidents", "components"}`) / create `{"kind", "title", "status", "impact"?, "components"?, "starts_at"?,
"ends_at"?, "message"?}` (`201 {"incident"}`; `400` for an invalid kind/status combination, unknown component, empty
title or `ends_at` before `starts_at`). Audit `status.incident_create` (installation level).

### `PATCH /api/v1/admin/status/incidents/{id}` · `POST …/{id}/updates` `{"status", "message"}` · `DELETE …/{id}` (superadmin)
Edit title, status, impact, components, `starts_at`, `ends_at` (`""` clears; `kind` is immutable) / append a timeline
entry and move to its status (`resolved`/`completed` set `ends_at`) / delete (`204`). Audit `status.incident_update`,
`status.incident_delete`.

## Edge cases (machine-readable spec: [openapi.yaml](openapi.yaml))

- Inventory without a complete snapshot: `snapshot_id: ""`, `snapshot_time: null`, empty `items`.
- Unknown host (no host record in the caller's organization, including another organization's host): `404 not_found` on `GET /hosts/{host_id}`, `/inventory`, `/services` and `/metrics`. An unknown **metric** on a known host is `200` with empty `series` and `metric.type`/`unit` = `""`.
- `/metrics/names?host_id=` and `/logs?host_id=` are filters, not host lookups: an unknown host gives empty lists (`200`).
- `attr.*` log filters outside the allowlist → `400` (see Logs).
- Inventory search omits `host_name` when the host is not in the hosts table.
- Malformed `trace_id` (not 32 hex chars) → `400 invalid_argument`.
- Unknown `/api/*` routes → `404` before authentication.
- `severity_min` accepts OTel severity names (`TRACE`…`FATAL`, plus `WARNING` as alias of `WARN`) or a number.
- `/metrics/names` has no `limit`; capped at `OPENLOG_API_MAX_ROWS`.
- For ranges longer than 6h, `step` is rounded up to whole minutes (rollup resolution).
- Non-`/api` paths serve the embedded UI when `OPENLOG_API_UI_ENABLED=true`.
- Every management response carries `Cache-Control: no-store`.
