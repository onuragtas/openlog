package tenant

import (
	"net/http"
	"testing"
)

func TestKeyFromHTTPAPIKeyAlias(t *testing.T) {
	cases := []struct {
		h    http.Header
		want string
	}{
		{http.Header{"X-Api-Key": {" k1 "}}, "k1"},
		{http.Header{"X-Api-Key": {"k1"}, "Authorization": {"Bearer k2"}}, "k1"},
		{http.Header{"Openlog-License-Key": {"k0"}, "X-Api-Key": {"k1"}}, "k0"},
		{http.Header{"X-Api-Key": {"  "}, "Authorization": {"Bearer k2"}}, "k2"},
	}
	for _, c := range cases {
		if got := KeyFromHTTP(c.h); got != c.want {
			t.Errorf("KeyFromHTTP(%v) = %q, want %q", c.h, got, c.want)
		}
	}
	// gRPC metadata keys are lower case.
	md := map[string]string{"x-api-key": "k3"}
	if got := KeyFromValues(func(n string) string { return md[n] }); got != "k3" {
		t.Errorf("KeyFromValues(metadata) = %q", got)
	}
}
