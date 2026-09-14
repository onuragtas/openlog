package templates

import (
	"strings"
	"testing"
	"time"
)

func TestPrivacyTemplates(t *testing.T) {
	at := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	for _, lang := range Supported {
		msgs := map[string]Message{
			"export-org":  DataExport(lang, DataExportData{Kind: "organization", OrgName: "Acme <b>", Size: "1.2 MiB", Truncated: true, ExpiresAt: at, Link: "https://o.test/api/v1/data-exports/download?token=x"}),
			"export-user": DataExport(lang, DataExportData{Kind: "user", Size: "4 KiB", ExpiresAt: at, Link: "https://o.test/d"}),
			"scheduled":   OrgDeletion(lang, OrgDeletionData{Stage: StageScheduled, OrgName: "Acme", PurgeAt: at, Link: "https://o.test/settings/profile"}),
			"operator":    OrgDeletion(lang, OrgDeletionData{Stage: StageScheduled, OrgName: "Acme", PurgeAt: at, Operator: true}),
			"cancelled":   OrgDeletion(lang, OrgDeletionData{Stage: StageCancelled, OrgName: "Acme"}),
			"completed":   OrgDeletion(lang, OrgDeletionData{Stage: StageCompleted, OrgName: "Acme"}),
			"account":     AccountDeletion(lang, AccountDeletionData{DeletedAt: at}),
		}
		for name, m := range msgs {
			if !strings.HasPrefix(m.Subject, "[openlog] ") || strings.ContainsAny(m.Subject, "\r\n") {
				t.Errorf("%s.%s subject %q", name, lang, m.Subject)
			}
			if strings.Contains(m.Text, "<no value>") || strings.Contains(m.HTML, "<no value>") || strings.Contains(m.HTML, "<b>") {
				t.Errorf("%s.%s: %q / %q", name, lang, m.Text, m.HTML)
			}
			if !strings.Contains(m.HTML, `<html lang="`+lang+`">`) {
				t.Errorf("%s.%s html lang: %q", name, lang, m.HTML)
			}
		}
		if !strings.Contains(msgs["export-org"].Text, "2026-09-21 08:00 UTC") || !strings.Contains(msgs["export-org"].Text, "manifest.json") {
			t.Errorf("export-org.%s text %q", lang, msgs["export-org"].Text)
		}
		if strings.Contains(msgs["operator"].Text, "Settings") || strings.Contains(msgs["operator"].Text, "Ayarlar") {
			t.Errorf("operator deletion must not offer self-service cancellation: %q", msgs["operator"].Text)
		}
	}
	if s := OrgDeletion("tr", OrgDeletionData{Stage: StageCompleted, OrgName: "Acme"}).Subject; s != "[openlog] Acme organizasyonu silindi" {
		t.Errorf("turkish subject %q", s)
	}
}
