package sso

import "github.com/onuragtas/openlog/internal/auth"

// Auth returns the auth service (SCIM uses its plan users limit, SaaS mode).
func (s *Service) Auth() *auth.Service { return s.auth }
