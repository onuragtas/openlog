# Single sign-on and SCIM provisioning

openlog organizations can sign in through their identity provider (IdP) with **OpenID Connect** or **SAML 2.0**,
claim their e-mail domains, map IdP groups to roles, **enforce** single sign-on, and let the IdP **provision**
members with **SCIM 2.0**. Contracts: [api.md](../contracts/api.md) "Single sign-on" and "SCIM",
[postgres.md](../contracts/postgres.md) "Single sign-on and SCIM", [config.md](../contracts/config.md); decisions
D-077 (SSO) and D-078 (SCIM).

Everything is configured per organization under **Settings → Single sign-on** (administrators; enforcement: owners).

## 1. Server prerequisites

| Setting | Why |
|---|---|
| `OPENLOG_PUBLIC_URL=https://openlog.example.com` | Redirect URI, SAML entity ID/ACS URL and SCIM base URL are derived from it. Without it the page shows "Single sign-on needs OPENLOG_PUBLIC_URL". |
| `OPENLOG_SSO_SECRET_KEY` (≥ 32 bytes, `openssl rand -base64 48`) | Encrypts OIDC client secrets and SAML SP private keys (AES-256-GCM). Falls back to a key derived from `OPENLOG_KEY_HASH_SECRET`; without either they are stored unencrypted (warning). Set the same value on every api pod. |
| `OPENLOG_COOKIE_SECURE=true` (default) | Session and sign-in binding cookies need HTTPS in production. |
| `OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS` | Unset = allowed unless `OPENLOG_SIGNUP_ENABLED=true`. When false, IdP URLs must be `https` and resolve to public addresses (SSRF protection for SaaS). Self-hosted IdPs on an internal network (Keycloak in the cluster) need `true`. |
| `OPENLOG_SMTP_*` | Optional: verify domains by e-mail instead of DNS. |

The api must reach the IdP (OIDC discovery, JWKS, token endpoint; SAML metadata URL) through
`OPENLOG_SSO_HTTP_TIMEOUT` (10 s). Browsers reach the IdP directly; the IdP never calls openlog except SCIM.

**Key rotation.** Set the new value as `OPENLOG_SSO_SECRET_KEY` and the old one as
`OPENLOG_SSO_SECRET_KEY_PREVIOUS` on all pods, then re-save every connection (Settings → Single sign-on → Save;
for OIDC enter the client secret again) and drop `_PREVIOUS`.

## 2. Verify your e-mail domain

Only users whose address is in a **verified domain of the organization** can sign in through its connection or be
provisioned by SCIM. This prevents an organization admin from configuring an IdP that asserts someone else's
address (account takeover). A domain can be verified by one organization only.

1. **Add domain** (e.g. `example.com`; subdomains are separate domains).
2. Either publish the TXT record shown on the page and click **Check DNS**:

   ```
   _openlog-verification.example.com.  TXT  "openlog-domain-verification=4f0c…"
   ```

   or (with SMTP) **Send e-mail** to `admin@`, `administrator@`, `hostmaster@`, `postmaster@` or `webmaster@` the
   domain; the link verifies it (24 h).

The TXT record can be removed after verification. Removing the domain in openlog stops SSO sign-ins from it.

## 3. Create the connection

The wizard has four steps: **Protocol → Service provider → Identity provider → Users and roles**. Save once with
the IdP values, run **Check configuration**, then **Test sign-in** (signs you in at the IdP and shows the e-mail,
groups and role openlog would use — no session or member is created).

Values openlog shows for your IdP:

| | OIDC | SAML |
|---|---|---|
| Redirect / ACS URL | `https://openlog.example.com/api/v1/sso/oidc/callback` | `https://openlog.example.com/api/v1/sso/saml/<connection id>/acs` |
| Entity ID / audience | client ID | `https://openlog.example.com/api/v1/sso/saml/<connection id>/metadata` (also the metadata URL) |
| Sign-in initiated by | openlog (authorization code + PKCE S256, state, nonce) | openlog (HTTP-Redirect AuthnRequest), optionally IdP |
| NameID | – | e-mail address (or unspecified with an `email` attribute) |
| Signing | RS/PS/ES 256–512, EdDSA (never `none`/HMAC) | RSA/ECDSA SHA-256/384/512 (SHA-1 refused); assertions or response must be signed |

The SAML entity ID and ACS URL are generated when a SAML connection is first saved (paste the IdP metadata URL or
XML, save, then copy the SP values or give the IdP the metadata URL).

Attributes (defaults work for most IdPs; override on the last step):

| | OIDC claim | SAML attribute |
|---|---|---|
| E-mail | `email` (+ `email_verified=true` required unless turned off) | `email`, `mail`, `emailaddress`, the `…/claims/emailaddress` URI, or an e-mail NameID |
| Name | `name` or `given_name family_name` | `name`, `displayName`, `firstName lastName` |
| Groups | `groups` | `groups`, or `http://schemas.microsoft.com/ws/2008/06/identity/claims/groups` |

### Okta

- **OIDC:** Applications → Create App Integration → OIDC, Web Application. Sign-in redirect URI = openlog's
  redirect URI; grant type Authorization Code. Assign users. In openlog: issuer `https://<your>.okta.com` (or the
  authorization server issuer, e.g. `https://<your>.okta.com/oauth2/default`), client ID and secret. Groups: add a
  `groups` claim (Sign On → OpenID Connect ID Token → Groups claim filter, e.g. *Matches regex* `.*`) and the scope
  `groups` if your authorization server requires it.
- **SAML:** Create App Integration → SAML 2.0. Single sign-on URL = ACS URL, Audience URI = entity ID, Name ID
  format EmailAddress, Application username Email. Attribute statement `email` → `user.email`; group attribute
  statement `groups` (filter). Copy "Metadata URL" into openlog.
- **SCIM:** use a SCIM-enabled app (App Integration Wizard → SAML 2.0 or OIDC + Provisioning → SCIM). SCIM
  connector base URL = openlog's SCIM base URL, unique identifier `userName`, actions Push New Users, Push Profile
  Updates, Push Groups; authentication "HTTP Header" with the SCIM token (`ols_…`).

### Microsoft Entra ID (Azure AD)

- **OIDC:** App registrations → New registration, Web redirect URI = openlog's redirect URI. Certificates & secrets
  → new client secret. Token configuration → add optional claim `email` (ID token) and a **groups claim** (Security
  groups; emits group object IDs — map those IDs in openlog, or "Group ID" → *sAMAccountName* for synced groups).
  Issuer: `https://login.microsoftonline.com/<tenant id>/v2.0`. Entra ID does not send `email_verified`; for Entra
  either uncheck "Require email_verified" (your domain is verified in Entra) or set the e-mail attribute to
  `preferred_username`/`upn` when that is the user's address.
- **SAML:** Enterprise applications → New application → Create your own → Non-gallery. Single sign-on → SAML:
  Identifier = entity ID, Reply URL = ACS URL. Attributes: keep `emailaddress` (user.mail) and add a group claim.
  Copy "App Federation Metadata Url" into openlog.
- **SCIM:** the enterprise application → Provisioning → Automatic; Tenant URL = SCIM base URL, Secret Token = SCIM
  token. Entra sends `PATCH active "False"` on unassignment — openlog removes the membership immediately.

### Google Workspace

Google's OIDC does not include groups; use **SAML** for group-based roles. Admin console → Apps → Web and mobile
apps → Add custom SAML app. ACS URL and Entity ID from openlog, Name ID format EMAIL, Name ID *Basic Information >
Primary email*. Attribute mapping: *Primary email* → `email`; Group membership → `groups`. Download the IdP
metadata and paste it (or its XML) into openlog; turn the app on for the organizational units. Google Workspace has
no SCIM push to custom apps.

### Keycloak

- **OIDC:** Clients → Create client (OpenID Connect), Client authentication ON, Standard flow ON, Valid redirect
  URIs = openlog's redirect URI; PKCE method S256 (Advanced). Client scopes → dedicated scope → Add mapper *Group
  Membership*, token claim name `groups`, *Full group path* OFF. Issuer
  `https://keycloak.example.com/realms/<realm>`.
- **SAML:** Clients → Import client, upload openlog's SP metadata (or Realm settings → *client-description
  converter* over the admin API). Name ID format `email`, Force name ID format ON, Sign assertions ON, Encrypt
  assertions OFF, Client signature required OFF (unless "Sign authentication requests" is on in openlog). Mappers:
  *User Property* `email` → attribute `email`; *Group list* → attribute `groups`, full path OFF. IdP metadata URL:
  `https://keycloak.example.com/realms/<realm>/protocol/saml/descriptor`. IdP-initiated: set *IDP-Initiated SSO URL
  name* and *relay state* (e.g. `/hosts`) on the client and allow IdP-initiated sign-in with that path in openlog.

The end-to-end test in `test/sso` runs both flows against Keycloak 26 (see
`test/integration/sso/docker-compose.yml`).

## 4. Users and roles

- **Just-in-time provisioning** (default on): the first sign-in of a verified-domain user creates the openlog
  account (no password) and the membership with the mapped role, else the **default role** (viewer).
- **Role mappings** (group → admin/member/viewer): the highest matching role wins. When mappings exist and the IdP
  sends a groups claim/attribute, non-owner roles are synchronized on every sign-in (and on SCIM group changes).
  Owners are never changed by SSO or SCIM.
- **Maximum session length** forces users back to the IdP after 1 h – 7 days (otherwise `OPENLOG_SESSION_TTL`).
- SSO sessions act **only in the organization they signed in to**; they end when the connection is disabled,
  deleted or replaced. Logout is local (the IdP session is not ended).
- IdP-initiated SAML is off by default. It cannot be bound to the browser (login CSRF), so enable it only if users
  need the IdP app launcher, and list the allowed start pages (RelayState).

## 5. Enforce single sign-on

Owners can require SSO. openlog refuses to turn enforcement on until:

1. the connection is enabled,
2. a **test sign-in of the current settings** succeeded (every save needs a new test),
3. at least one domain is verified,
4. at least one **break-glass owner** is selected — these owners keep password sign-in if the IdP fails,
5. you keep access yourself (you are a break-glass owner or signed in through this connection).

With enforcement on, members whose e-mail domain is verified cannot use password sessions in the organization
(existing ones lose access on their next request; password sign-in answers "requires single sign-on" when all
their organizations enforce SSO). Members outside the verified domains (contractors with other addresses) are not
affected. The connection cannot be disabled or deleted, and the last verified domain not removed, while enforced.

**Lockout recovery** (IdP down, no break-glass owner can sign in): an operator can turn enforcement off in
PostgreSQL:

```sql
UPDATE sso_connections SET enforce = false WHERE org_id = (SELECT id FROM organizations WHERE tenant_id = '<tenant>');
```

## 6. SCIM provisioning

Create a token under **SCIM provisioning** (`ols_…`, shown once, hashed like API keys) and configure the IdP with
the SCIM base URL `https://openlog.example.com/api/scim/v2`. Supported: `/Users` and `/Groups` (GET with
`filter=userName eq "…"` / `externalId eq "…"` / `displayName eq "…"`, `startIndex`, `count`; POST; PUT; PATCH with
`add`/`replace`/`remove`, path-less values and `members[value eq "…"]`; DELETE), `/ServiceProviderConfig`,
`/ResourceTypes`, `/Schemas`. No bulk, sort or ETags.

- Creating an active user adds the membership (and the account when the address is new; addresses must be in a
  verified domain). Changing the e-mail address of a provisioned user is not supported (deprovision and provision).
- `active=false` or DELETE removes the membership **immediately**, revokes the user's SSO sessions of the
  organization and the API keys they created there; a deprovisioned user is not re-added by JIT sign-in until the
  IdP reactivates them. Owners cannot be deprovisioned through SCIM.
- SCIM group membership + role mappings set roles (recomputed when mappings change).

## 7. Troubleshooting

Failed sign-ins return to `/login?sso_error=<code>` and are recorded in the audit log (`sso.login_failed` with the
reason; the details are only visible to administrators). Successful ones log `sso.login`.

| Code | Meaning / fix |
|---|---|
| `expired` | Sign-in older than `OPENLOG_SSO_LOGIN_TTL`, used twice, started in another browser (cookies blocked?), or the settings changed meanwhile |
| `idp_error` | The IdP returned an error (user not assigned to the app, consent denied) |
| `invalid_response` | Signature, issuer, audience, recipient, time or nonce check failed — compare the SP values, IdP certificate, clock (`OPENLOG_SSO_CLOCK_SKEW`) |
| `replay` | The same SAML assertion was posted twice |
| `email_missing` / `email_not_verified` | Adjust the e-mail attribute, or "Require email_verified" (OIDC) |
| `domain_not_verified` | The asserted address is not in a verified domain of this organization |
| `not_member` | Just-in-time provisioning is off and the user is not a member |
| `deprovisioned` | SCIM deactivated the user |
| `disabled` | The connection is disabled or was deleted |

**Check configuration** shows discovery/JWKS/PKCE (OIDC) or metadata/certificate expiry (SAML) problems directly.
