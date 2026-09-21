package auth_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
)

// Browser keys are the one credential openlog reads back (0099_browser_key_value), and an exception is only
// as safe as its edges. These hold the two that matter: the value does come back from an ordinary read, and
// it does **not** leak into the audit log — which is a different store, kept longer, and read by people who
// have no business with the key itself.

func TestBrowserKeyValueIsReadableAfterCreation(t *testing.T) {
	e := newKeyEnv(t)
	ctx := context.Background()
	k, secret, err := e.svc.CreateBrowserKey(ctx, e.admin, validInput(), auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if k.Value != secret {
		t.Fatalf("create returned value %q, want the secret it generated", k.Value)
	}

	// The point of the feature: a later read, not only the one-shot creation response.
	got, err := e.svc.GetBrowserKey(ctx, e.admin, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != secret {
		t.Errorf("get returned value %q, want %q", got.Value, secret)
	}
	list, err := e.svc.ListBrowserKeys(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Value != secret {
		t.Errorf("list did not carry the value: %+v", list)
	}
	// The hash is still what the ingest path resolves; the plaintext is additional, never a replacement.
	if len(got.Hash) == 0 {
		t.Error("storing the value replaced the hash")
	}
	if string(got.Hash) == secret {
		t.Error("the hash is the plaintext")
	}
}

// The audit log records that a key was created, not what it is. It is retained differently from the key
// itself and read by people auditing changes, so a credential quoted there turns it into a credential
// store — the reason browserKeyDetails names the prefix instead.
func TestBrowserKeyValueNeverReachesTheAuditLog(t *testing.T) {
	e := newKeyEnv(t)
	ctx := context.Background()
	k, secret, err := e.svc.CreateBrowserKey(ctx, e.admin, validInput(), auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	in := validInput()
	in.Name = "renamed"
	if _, err := e.svc.UpdateBrowserKey(ctx, e.admin, k.ID, in, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RevokeBrowserKey(ctx, e.admin, k.ID, auth.ClientMeta{}); err != nil {
		t.Fatal(err)
	}

	events := e.st.AuditEvents()
	if len(events) == 0 {
		t.Fatal("no audit events recorded, so this proves nothing")
	}
	// Serialised whole rather than field by field: a value added to the details map later would slip past
	// a check that only knew today's fields.
	blob, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Errorf("the key's plaintext appears in the audit log: %s", blob)
	}
	// The prefix is what identifies the key there, and it must still be present or the log names nothing.
	if !strings.Contains(string(blob), k.Prefix) {
		t.Errorf("the audit log does not name the key by its prefix: %s", blob)
	}
}
