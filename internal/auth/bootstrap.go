package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/onuragtas/openlog/internal/tenant"
)

var tenantIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidTenantID reports whether id is a valid tenant id (the ClickHouse key).
func ValidTenantID(id string) bool { return tenantIDRe.MatchString(id) }

// NewTenantID returns a random tenant id ("t" + 20 hex characters).
func NewTenantID() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "t" + hex.EncodeToString(b), nil
}

// MinProvidedKeyLen is the minimum length of operator-chosen keys in
// BootstrapSpec (generated keys are always 52 characters). It is lower than
// MinCustomKeyLen so existing development keys such as "dev-license-key"
// (15 characters) keep working; the character rules of ValidateCustomKey apply.
const MinProvidedKeyLen = 8

// BootstrapSpec describes the first organization for self-hosted/dev installs
// (OPENLOG_BOOTSTRAP_*). Everything is idempotent: existing objects are kept.
type BootstrapSpec struct {
	TenantID      string // required
	OrgName       string // used when the organization is created (default: TenantID)
	OwnerEmail    string // optional; ensured as owner
	OwnerPassword string // required only when the owner user does not exist yet
	OwnerName     string
	LicenseKey    string // optional plaintext ingest key to ensure
	APIKey        string // optional plaintext API key to ensure
}

// BootstrapResult reports what Bootstrap created.
type BootstrapResult struct {
	Org               Organization
	CreatedOrg        bool
	CreatedUser       bool
	AddedOwner        bool
	CreatedLicenseKey bool
	CreatedAPIKey     bool
}

// Bootstrap ensures the organization, owner membership and keys in spec.
// It never changes an existing user's password, and it fails if a provided
// key already belongs to another organization or was revoked.
func (s *Service) Bootstrap(ctx context.Context, spec BootstrapSpec) (BootstrapResult, error) {
	var res BootstrapResult
	if !ValidTenantID(spec.TenantID) {
		return res, fmt.Errorf("invalid tenant id %q (want ^[a-z0-9][a-z0-9_-]{0,62}$)", spec.TenantID)
	}
	orgName := spec.OrgName
	if orgName == "" {
		orgName = spec.TenantID
	}
	orgName, err := cleanName(orgName, "organization name", true)
	if err != nil {
		return res, err
	}
	email := NormalizeEmail(spec.OwnerEmail)
	if email != "" && !validEmail(email) {
		return res, fmt.Errorf("invalid owner email %q", spec.OwnerEmail)
	}
	now := s.now()

	var owner User
	haveOwner := false
	if email != "" {
		owner, err = s.store.GetUserByEmail(ctx, email)
		switch {
		case err == nil:
			haveOwner = true
		case errors.Is(err, ErrNotFound):
			if spec.OwnerPassword == "" {
				return res, fmt.Errorf("owner %s does not exist: a password is required to create it", email)
			}
			if err := ValidatePassword(spec.OwnerPassword); err != nil {
				return res, err
			}
			hash, err := HashPassword(spec.OwnerPassword)
			if err != nil {
				return res, err
			}
			name, err := cleanName(spec.OwnerName, "owner name", false)
			if err != nil {
				return res, err
			}
			owner = User{Email: email, Name: name, PasswordHash: hash, CreatedAt: now}
		default:
			return res, err
		}
	}

	// Reject keys owned by another organization before creating anything.
	if spec.LicenseKey != "" {
		if err := ValidateCustomKey(spec.LicenseKey, MinProvidedKeyLen); err != nil {
			return res, fmt.Errorf("bootstrap license key: %s", err.(*Error).Message)
		}
		info, err := s.store.LookupLicenseKey(ctx, HashSecret(spec.LicenseKey))
		if err == nil && info.TenantID != spec.TenantID {
			return res, errors.New("the bootstrap license key already belongs to another organization")
		} else if err != nil && !errors.Is(err, tenant.ErrUnknownKey) {
			return res, err
		}
	}
	if spec.APIKey != "" {
		if len(spec.APIKey) < MinProvidedKeyLen {
			return res, fmt.Errorf("API key must be at least %d characters", MinProvidedKeyLen)
		}
		_, keyOrg, err := s.store.LookupAPIKey(ctx, HashSecret(spec.APIKey))
		if err == nil && keyOrg.TenantID != spec.TenantID {
			return res, errors.New("the bootstrap API key already belongs to another organization")
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return res, err
		}
	}

	org, err := s.store.GetOrganizationByTenant(ctx, spec.TenantID)
	switch {
	case err == nil:
		if email != "" {
			if !haveOwner {
				if err := s.store.CreateUser(ctx, &owner); err != nil {
					return res, err
				}
				res.CreatedUser = true
			}
			if _, err := s.store.GetMembership(ctx, org.ID, owner.ID); errors.Is(err, ErrNotFound) {
				if err := s.store.AddMember(ctx, org.ID, owner.ID, RoleOwner); err != nil {
					return res, err
				}
				res.AddedOwner = true
			} else if err != nil {
				return res, err
			}
		}
	case errors.Is(err, ErrNotFound):
		if email == "" {
			return res, errors.New("an owner email is required to create the organization")
		}
		org = Organization{TenantID: spec.TenantID, Name: orgName, CreatedAt: now}
		res.CreatedUser = !haveOwner
		if err := s.store.CreateOrganization(ctx, &org, &owner); err != nil {
			return res, err
		}
		res.CreatedOrg, res.AddedOwner = true, true
	default:
		return res, err
	}
	res.Org = org

	if spec.LicenseKey != "" {
		info, err := s.store.LookupLicenseKey(ctx, HashSecret(spec.LicenseKey))
		switch {
		case err == nil:
			if info.TenantID != org.TenantID {
				return res, errors.New("the bootstrap license key already belongs to another organization")
			}
		case errors.Is(err, tenant.ErrUnknownKey):
			k := LicenseKey{OrgID: org.ID, Name: "bootstrap", Prefix: DisplayPrefix(spec.LicenseKey), Hash: HashSecret(spec.LicenseKey), Custom: true, CreatedBy: owner.ID, CreatedAt: now}
			if err := s.store.CreateLicenseKey(ctx, &k); err != nil {
				if errors.Is(err, ErrAlreadyExists) {
					return res, errors.New("the bootstrap license key exists but was revoked; choose another key")
				}
				return res, err
			}
			res.CreatedLicenseKey = true
		default:
			return res, err
		}
	}
	if spec.APIKey != "" {
		if len(spec.APIKey) < MinProvidedKeyLen {
			return res, fmt.Errorf("API key must be at least %d characters", MinProvidedKeyLen)
		}
		k, keyOrg, err := s.store.LookupAPIKey(ctx, HashSecret(spec.APIKey))
		switch {
		case err == nil:
			if keyOrg.ID != org.ID {
				return res, errors.New("the bootstrap API key already belongs to another organization")
			}
			if k.RevokedAt != nil {
				return res, errors.New("the bootstrap API key was revoked; choose another key")
			}
		case errors.Is(err, ErrNotFound):
			nk := APIKey{OrgID: org.ID, Name: "bootstrap", Prefix: DisplayPrefix(spec.APIKey), Hash: HashSecret(spec.APIKey), Scope: "read", CreatedBy: owner.ID, CreatedAt: now}
			if err := s.store.CreateAPIKey(ctx, &nk); err != nil {
				return res, err
			}
			res.CreatedAPIKey = true
		default:
			return res, err
		}
	}
	if res.CreatedOrg || res.CreatedUser || res.AddedOwner || res.CreatedLicenseKey || res.CreatedAPIKey {
		s.audit(ctx, org.ID, owner.ID, owner.Email, ClientMeta{}, "bootstrap", "organization", org.ID, map[string]any{
			"created_org": res.CreatedOrg, "created_user": res.CreatedUser, "added_owner": res.AddedOwner,
			"created_license_key": res.CreatedLicenseKey, "created_api_key": res.CreatedAPIKey,
		})
	}
	return res, nil
}

// ResetPassword sets a user's password and revokes all their sessions (openlog-admin).
func (s *Service) ResetPassword(ctx context.Context, email, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	u, err := s.store.GetUserByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if err := s.store.SetUserPassword(ctx, u.ID, hash); err != nil {
		return err
	}
	if err := s.store.RevokeUserSessions(ctx, u.ID, "", s.now()); err != nil {
		return err
	}
	s.audit(ctx, "", u.ID, u.Email, ClientMeta{}, "user.password_reset", "user", u.ID, map[string]any{"via": "openlog-admin"})
	return nil
}
