package tenant

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestParseStatic(t *testing.T) {
	s, err := ParseStatic(" key1=tenant1, key2 = tenant2 ,,")
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("len = %d", s.Len())
	}
	for key, want := range map[string]string{"key1": "tenant1", "key2": "tenant2"} {
		got, err := s.Resolve(context.Background(), key)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v", key, got, err)
		}
	}
	for _, bad := range []string{"", "key3", "key", "key1 "} {
		if _, err := s.Resolve(context.Background(), bad); !errors.Is(err, ErrUnknownKey) {
			t.Errorf("Resolve(%q) err = %v", bad, err)
		}
	}
}

func TestParseStaticErrors(t *testing.T) {
	for _, spec := range []string{"novalue", "=t", "k=", "k=a,k=b"} {
		if _, err := ParseStatic(spec); err == nil {
			t.Errorf("ParseStatic(%q) expected error", spec)
		}
	}
	s, err := ParseStatic("")
	if err != nil || s.Len() != 0 {
		t.Errorf("empty spec: %v %d", err, s.Len())
	}
}

func TestKeyFromHTTP(t *testing.T) {
	cases := []struct {
		h    http.Header
		want string
	}{
		{http.Header{"Openlog-License-Key": {"abc"}}, "abc"},
		{http.Header{"Authorization": {"Bearer xyz"}}, "xyz"},
		{http.Header{"Authorization": {"bearer  xyz "}}, "xyz"},
		{http.Header{"Authorization": {"Basic xyz"}}, ""},
		{http.Header{"Openlog-License-Key": {"abc"}, "Authorization": {"Bearer xyz"}}, "abc"},
		{http.Header{}, ""},
	}
	for _, c := range cases {
		if got := KeyFromHTTP(c.h); got != c.want {
			t.Errorf("KeyFromHTTP(%v) = %q, want %q", c.h, got, c.want)
		}
	}
}
