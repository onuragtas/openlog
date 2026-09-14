package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/auth"
)

// Background refresh of IdP documents (D-088). A leader-elected job re-fetches the OIDC discovery document and JWKS
// before the one hour provider cache expires, and the SAML IdP metadata of connections with a metadata URL before
// its cacheDuration/validUntil runs out (certificate rollover). Results are stored on the connection (health shown
// in the UI, sso_connections.idp_*); OIDC documents are reused by every pod (oidc.go cachedProvider). Refreshed
// SAML metadata replaces the stored copy without changing config_version when the IdP entity ID is unchanged.

const (
	refreshTick        = time.Minute
	refreshBatch       = 50
	oidcRefreshEvery   = 30 * time.Minute
	samlRefreshDefault = 6 * time.Hour
	samlRefreshMin     = 15 * time.Minute
	samlRefreshMax     = 24 * time.Hour
	refreshBackoffMin  = 5 * time.Minute
	refreshBackoffMax  = time.Hour
	// certWarning and validUntilWarning mark a connection's health "warning" ahead of expiry.
	certWarning       = 30 * 24 * time.Hour
	validUntilWarning = 7 * 24 * time.Hour
	// refreshErrorAfter consecutive failures mark the health "error".
	refreshErrorAfter = 3
)

type refreshMetrics struct {
	runs     *prometheus.CounterVec
	duration *prometheus.HistogramVec
	failing  prometheus.Gauge
}

func registerCollector[C prometheus.Collector](reg prometheus.Registerer, c C) C {
	if reg == nil {
		return c
	}
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(C); ok {
				return existing
			}
		}
	}
	return c
}

func newRefreshMetrics(reg prometheus.Registerer) *refreshMetrics {
	return &refreshMetrics{
		runs: registerCollector(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_sso_idp_refresh_total", Help: "Background refreshes of IdP discovery, JWKS and SAML metadata by protocol and result (ok, error).",
		}, []string{"protocol", "result"})),
		duration: registerCollector(reg, prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "openlog_sso_idp_refresh_duration_seconds", Help: "Duration of a background IdP refresh.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"protocol"})),
		failing: registerCollector(reg, prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openlog_sso_idp_refresh_failing_connections", Help: "Enabled SSO connections whose last IdP refresh failed.",
		})),
	}
}

// RefreshJob returns the leader task that refreshes IdP documents before they expire; reg may be nil.
func (s *Service) RefreshJob(reg prometheus.Registerer) func(ctx context.Context) {
	s.metrics = newRefreshMetrics(reg)
	return func(ctx context.Context) {
		t := time.NewTicker(refreshTick)
		defer t.Stop()
		for {
			s.RunRefresh(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}
}

// RunRefresh refreshes the connections that are due (one pass).
func (s *Service) RunRefresh(ctx context.Context) {
	cs, err := s.store.ListRefreshDue(ctx, s.now(), refreshBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("sso refresh: cannot list connections", "err", err)
		}
		return
	}
	for _, c := range cs {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.refresh(ctx, c); err != nil {
			s.log.Warn("sso refresh failed", "connection_id", c.ID, "org_id", c.OrgID, "protocol", c.Protocol, "err", err)
		}
	}
	if s.metrics != nil {
		if n, err := s.store.CountRefreshFailing(ctx); err == nil {
			s.metrics.failing.Set(float64(n))
		}
	}
}

// RefreshNow refreshes a connection's IdP documents immediately (admin+).
func (s *Service) RefreshNow(ctx context.Context, p *auth.Principal, id string, meta auth.ClientMeta) (Connection, error) {
	c, err := s.GetConnection(ctx, p, id)
	if err != nil {
		return Connection{}, err
	}
	if err := s.allowIP(ctx, "refresh-"+c.ID, "", 30); err != nil {
		return Connection{}, err
	}
	_, rerr := s.refresh(ctx, c)
	s.auth.Audit(ctx, p, meta, "sso.connection.refresh", "sso_connection", c.ID, map[string]any{"ok": rerr == nil})
	out, err := s.store.GetConnectionByID(ctx, c.ID)
	if err != nil {
		return Connection{}, s.fail(err)
	}
	return out, nil
}

// refresh fetches and validates the IdP documents of c and records the result.
func (s *Service) refresh(ctx context.Context, c Connection) (Connection, error) {
	start := s.now()
	rctx, cancel := context.WithTimeout(ctx, 2*s.cfg.HTTPTimeout)
	defer cancel()
	var cache *IdPCache
	var next time.Duration
	var err error
	switch c.Protocol {
	case ProtocolOIDC:
		cache, next, err = s.refreshOIDC(rctx, c)
	case ProtocolSAML:
		cache, next, err = s.refreshSAML(rctx, c)
	default:
		err = errors.New("unknown protocol")
	}
	now := s.now()
	if s.metrics != nil {
		result := "ok"
		if err != nil {
			result = "error"
		}
		s.metrics.runs.WithLabelValues(string(c.Protocol), result).Inc()
		s.metrics.duration.WithLabelValues(string(c.Protocol)).Observe(now.Sub(start).Seconds())
	}
	msg := ""
	if err != nil {
		msg = truncate(err.Error(), 1000)
		next = min(refreshBackoffMin<<min(c.Refresh.Failures, 6), refreshBackoffMax)
	}
	if rerr := s.store.RecordRefresh(ctx, c.ID, err == nil, msg, cache, now.Add(next), now); rerr != nil && !errors.Is(rerr, auth.ErrNotFound) {
		return c, rerr
	}
	if err == nil && c.Protocol == ProtocolOIDC {
		s.forgetProvider(c.ID) // rebuild from the refreshed documents
	}
	return c, err
}

// refreshOIDC fetches and validates discovery and JWKS.
func (s *Service) refreshOIDC(ctx context.Context, c Connection) (*IdPCache, time.Duration, error) {
	if c.OIDC == nil {
		return nil, 0, errors.New("not an OIDC connection")
	}
	if _, err := ParseIdPURL(c.OIDC.Issuer, s.cfg.AllowPrivateNetworks); err != nil {
		return nil, 0, fmt.Errorf("issuer: %w", err)
	}
	disc, err := fetch(ctx, s.client, strings.TrimSuffix(c.OIDC.Issuer, "/")+"/.well-known/openid-configuration")
	if err != nil {
		return nil, 0, fmt.Errorf("discovery: %w", err)
	}
	var meta oidcMetadata
	if err := json.Unmarshal(disc, &meta); err != nil {
		return nil, 0, fmt.Errorf("discovery: %w", err)
	}
	if meta.Issuer != c.OIDC.Issuer {
		return nil, 0, fmt.Errorf("discovery: issuer %q does not match %q", truncate(meta.Issuer, 200), c.OIDC.Issuer)
	}
	if meta.AuthURL == "" || meta.TokenURL == "" || meta.JWKSURL == "" {
		return nil, 0, errors.New("discovery: authorization_endpoint, token_endpoint or jwks_uri missing")
	}
	if len(meta.Algs) > 0 && !slices.ContainsFunc(meta.Algs, func(a string) bool { return slices.Contains(oidcAlgs, a) }) {
		return nil, 0, fmt.Errorf("the provider signs ID tokens only with unsupported algorithms %v", meta.Algs)
	}
	ju, err := ParseIdPURL(meta.JWKSURL, s.cfg.AllowPrivateNetworks)
	if err != nil {
		return nil, 0, fmt.Errorf("jwks_uri: %w", err)
	}
	jwks, err := fetch(ctx, s.client, ju.String())
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: %w", err)
	}
	keys, err := parseJWKS(jwks)
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: %w", err)
	}
	if len(keys) == 0 {
		return nil, 0, errors.New("jwks: no signature keys")
	}
	now := s.now()
	return &IdPCache{FetchedAt: &now, Issuer: c.OIDC.Issuer, OIDCDiscovery: disc, OIDCJWKS: jwks}, oidcRefreshEvery, nil
}

// refreshSAML re-fetches the metadata URL (or re-validates pasted metadata) and stores changed metadata the trust
// settings accept (metadatatrust.go); other changes wait for an administrator.
func (s *Service) refreshSAML(ctx context.Context, c Connection) (*IdPCache, time.Duration, error) {
	if c.SAML == nil {
		return nil, 0, errors.New("not a SAML connection")
	}
	now := s.now()
	if c.SAML.IdPMetadataURL == "" {
		md, info, err := idpMetadataInfo([]byte(c.SAML.IdPMetadataXML), now)
		if err != nil {
			return nil, 0, err
		}
		if err := checkMetadataTimes(md, info, now); err != nil {
			return nil, 0, err
		}
		cache, next := samlSchedule(md, now)
		return cache, next, nil
	}
	u, err := ParseIdPURL(c.SAML.IdPMetadataURL, s.cfg.AllowPrivateNetworks)
	if err != nil {
		return nil, 0, fmt.Errorf("idp_metadata_url: %w", err)
	}
	data, err := fetch(ctx, s.client, u.String())
	if err != nil {
		return nil, 0, fmt.Errorf("cannot fetch the IdP metadata: %w", err)
	}
	rm, err := s.checkRefreshedMetadata(c, data, now)
	if rm != nil && rm.pending != nil {
		s.recordPendingMetadata(ctx, c, rm.pending)
		return nil, 0, err
	}
	if err != nil {
		return nil, 0, err
	}
	// auth.ErrNotFound: saved meanwhile, the next refresh uses the new settings.
	if err := s.applyRefreshedMetadata(ctx, c, data, rm, false); err != nil && !errors.Is(err, auth.ErrNotFound) {
		return nil, 0, err
	}
	cache, next := samlSchedule(rm.md, now)
	return cache, next, nil
}

// Health is the refresh health of a connection shown in the UI.
type Health struct {
	Status             string // ok, warning, error, unknown
	Message            string
	CheckedAt          *time.Time
	NextAt             *time.Time
	Failures           int
	MetadataValidUntil *time.Time
}

// ConnectionHealth summarizes the refresh state and certificate/metadata expiry of c at the service clock.
func (s *Service) ConnectionHealth(c Connection) Health {
	now := s.now()
	h := Health{Status: "unknown", CheckedAt: c.Refresh.RefreshedAt, NextAt: c.Refresh.NextAt, Failures: c.Refresh.Failures,
		MetadataValidUntil: c.Refresh.Cache.SAMLValidUntil}
	warn := func(msg string) {
		if h.Status == "ok" || h.Status == "unknown" {
			h.Status, h.Message = "warning", msg
		}
	}
	switch {
	case c.Refresh.OK == nil:
	case *c.Refresh.OK:
		h.Status = "ok"
	case c.Refresh.Failures >= refreshErrorAfter:
		h.Status, h.Message = "error", c.Refresh.Error
	default:
		h.Status, h.Message = "warning", c.Refresh.Error
	}
	if c.SAML != nil {
		if p := c.SAML.PendingMetadata; p != nil {
			warn("an IdP metadata change awaits confirmation (" + p.Reason + ")")
		}
		if t, err := time.Parse(time.RFC3339, c.SAML.IdPCertNotAfter); err == nil {
			switch {
			case !t.After(now):
				h.Status, h.Message = "error", "the IdP signing certificate expired on "+c.SAML.IdPCertNotAfter
			case t.Before(now.Add(certWarning)):
				warn("the IdP signing certificate expires on " + c.SAML.IdPCertNotAfter)
			}
		}
		if vu := c.Refresh.Cache.SAMLValidUntil; vu != nil && vu.Before(now.Add(validUntilWarning)) && h.Status != "error" {
			warn("the IdP metadata is valid until " + vu.UTC().Format(time.RFC3339))
		}
	}
	return h
}
