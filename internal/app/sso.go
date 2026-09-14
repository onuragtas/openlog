package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/sso"
	"github.com/onuragtas/openlog/internal/store/postgres"
)

// startSSO enables single sign-on, domain verification and SCIM on the api (D-077, D-078): the SSO service, the
// session policy of the auth service and the cleanup of expired sign-in states. No-op in static auth mode or with
// OPENLOG_SSO_ENABLED=false.
func startSSO(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, svc *auth.Service, srv *api.Server, log *slog.Logger) {
	s := cfg.API.Auth.SSO
	if !s.Enabled || pool == nil || svc == nil {
		return
	}
	box := sso.NewSecretBox(s.SecretKey, s.SecretKeyPrevious, cfg.KeyHash.Secret, cfg.KeyHash.SecretPrevious)
	if !box.Encrypted() {
		log.Warn("neither OPENLOG_SSO_SECRET_KEY nor OPENLOG_KEY_HASH_SECRET is set: OIDC client secrets and SAML SP keys are stored unencrypted")
	}
	if cfg.Alert.PublicURL == "" {
		log.Info("OPENLOG_PUBLIC_URL is not set: single sign-on is unavailable (redirect and SAML URLs are derived from it)")
	}
	allowPrivate := cfg.SSOAllowPrivateNetworks()
	if allowPrivate && cfg.API.Auth.SignupEnabled {
		log.Warn("OPENLOG_SSO_ALLOW_PRIVATE_NETWORKS=true with sign-up enabled: organization admins can make the api request internal addresses")
	}
	store := postgres.NewSSOStore(pool)
	ssoSvc := sso.NewService(svc, store, sso.Config{
		PublicURL: cfg.Alert.PublicURL, CookieSecure: cfg.API.Auth.CookieSecure, LoginTTL: s.LoginTTL, ClockSkew: s.ClockSkew,
		AllowPrivateNetworks: allowPrivate, HTTPTimeout: s.HTTPTimeout, SecretBox: box, KeyHasher: KeyHasher(cfg),
		Mailer: svc.Config().Mailer, SCIMEnabled: s.SCIMEnabled,
	}, log.With("component", "sso"))
	svc.SetSessionPolicy(ssoSvc.Policy())
	srv.SetSSO(ssoSvc)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if err := store.Cleanup(ctx, now); err != nil && ctx.Err() == nil {
					log.Warn("sso cleanup failed", "err", err)
				}
			}
		}
	}()
}
