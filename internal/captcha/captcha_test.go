package captcha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerify(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = map[string]string{"secret": r.PostForm.Get("secret"), "response": r.PostForm.Get("response"),
			"remoteip": r.PostForm.Get("remoteip"), "sitekey": r.PostForm.Get("sitekey")}
		switch r.PostForm.Get("response") {
		case "good":
			_, _ = w.Write([]byte(`{"success":true}`))
		case "misconfigured":
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-secret"]}`))
		case "down":
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
		}
	}))
	defer srv.Close()

	v, err := New(HCaptcha, "s3cret", "site", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := v.Verify(context.Background(), "good", "203.0.113.5"); !ok || err != nil {
		t.Fatalf("good token: %v %v", ok, err)
	}
	if got["secret"] != "s3cret" || got["remoteip"] != "203.0.113.5" || got["sitekey"] != "site" {
		t.Fatalf("form %v", got)
	}
	if ok, err := v.Verify(context.Background(), "bad", ""); ok || err != nil {
		t.Fatalf("bad token: %v %v", ok, err)
	}
	if _, err := v.Verify(context.Background(), "misconfigured", ""); err == nil {
		t.Fatal("secret error must be reported as an error")
	}
	if _, err := v.Verify(context.Background(), "down", ""); err == nil {
		t.Fatal("HTTP error must be reported as an error")
	}

	tv, _ := New(Turnstile, "s", "k", srv.URL, nil)
	if ok, _ := tv.Verify(context.Background(), "good", ""); !ok || got["sitekey"] != "" {
		t.Fatalf("turnstile: %v form %v", ok, got)
	}
	for _, bad := range [][3]string{{"recaptcha", "s", "k"}, {Turnstile, "", "k"}, {Turnstile, "s", ""}} {
		if _, err := New(bad[0], bad[1], bad[2], "", nil); err == nil {
			t.Errorf("New(%v) accepted", bad)
		}
	}
}
