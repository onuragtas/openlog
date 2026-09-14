package renderer

import (
	"strings"
	"testing"
	"time"
)

func testClaims() Claims {
	return Claims{OrgID: "org-1", TenantID: "tenant-1", DashboardID: "11111111-1111-1111-1111-111111111111",
		ReportID: "22222222-2222-2222-2222-222222222222", From: 1000, To: 2000}
}

func TestReportToken(t *testing.T) {
	key := KeyFromSecret("server-secret-server-secret-server-secret")
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	tok, err := NewReportToken(key, testClaims(), now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) > maxTokenLen {
		t.Fatalf("token %q", tok)
	}
	c, err := VerifyToken(key, tok, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if c.OrgID != "org-1" || c.TenantID != "tenant-1" || c.ReportID != testClaims().ReportID || c.From != 1000 || c.To != 2000 {
		t.Errorf("claims %+v", c)
	}

	// Expired, other key, the renderer's shared secret as key, tampered payload or signature.
	if _, err := VerifyToken(key, tok, now.Add(5*time.Minute)); err == nil {
		t.Error("expired token accepted")
	}
	if _, err := VerifyToken(KeyFromSecret("other-secret-other-secret-other-secret"), tok, now); err == nil {
		t.Error("token of another key accepted")
	}
	body, sig, _ := strings.Cut(strings.TrimPrefix(tok, TokenPrefix), ".")
	forged := Claims{OrgID: "org-2", TenantID: "tenant-2", DashboardID: "d", ReportID: "r", From: 1, To: 2, Expires: now.Add(time.Minute).Unix(), Purpose: purposeReport}
	other, _ := NewReportToken(KeyFromSecret("attacker-secret-attacker-secret-attacker"), forged, now, time.Minute)
	otherBody, _, _ := strings.Cut(strings.TrimPrefix(other, TokenPrefix), ".")
	for name, bad := range map[string]string{
		"payload swap":   TokenPrefix + otherBody + "." + sig,
		"signature flip": TokenPrefix + body + "." + strings.Repeat("A", 43),
		"no prefix":      strings.TrimPrefix(tok, TokenPrefix),
		"share token":    "olds_" + strings.Repeat("a", 43),
		"too long":       TokenPrefix + strings.Repeat("a", maxTokenLen) + "." + sig,
		"empty":          "",
	} {
		if _, err := VerifyToken(key, bad, now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A lifetime beyond MaxTokenTTL is capped at issue and refused at verification.
	long, _ := NewReportToken(key, testClaims(), now, time.Hour)
	if _, err := VerifyToken(key, long, now.Add(MaxTokenTTL+time.Second)); err == nil {
		t.Error("ttl not capped")
	}
	if _, err := NewReportToken(nil, testClaims(), now, time.Minute); err == nil {
		t.Error("token without key")
	}
	incomplete := testClaims()
	incomplete.TenantID = ""
	if _, err := NewReportToken(key, incomplete, now, time.Minute); err == nil {
		t.Error("token without tenant")
	}
	if KeyFromSecret("  ") != nil {
		t.Error("key from empty secret")
	}
}
