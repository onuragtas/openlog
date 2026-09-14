package api

import (
	"net/http"

	"github.com/onuragtas/openlog/internal/auth"
)

// verifyEmail confirms an e-mail address: POST /api/v1/auth/verify-email {"token"} → 204 (public).
func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := s.accounts.VerifyEmail(r.Context(), in.Token, s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// resendVerification e-mails a new verification link to the caller: POST /api/v1/auth/verify-email/resend → 204.
func (s *Server) resendVerification(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.accounts.ResendVerification(r.Context(), p, s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// resendInvitation issues a new link for an invitation (also an expired one) and e-mails it when e-mail is
// enabled: POST /api/v1/invitations/{id}/resend → 200 {"invitation", "token", "email_sent"}.
func (s *Server) resendInvitation(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	inv, token, sent, err := s.accounts.ResendInvitation(r.Context(), p, r.PathValue("id"), s.accounts.Meta(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitation": invitationResponse(inv, s.now()), "token": token, "email_sent": sent})
	return nil
}
