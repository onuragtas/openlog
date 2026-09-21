package rum

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Resolve decides which allowlist bounds a key, and until now nothing executed that decision: the only
// coverage was internal/ingest's fake, which *reimplements* the branch rather than calling it. Two
// mechanisms that are supposed to agree, with only one of them tested, is how they drift.
//
// The store fake ignores the hashes and answers with one configured key. That is deliberate: what is under
// test is what Resolve does once a key is found, not the lookup or the cache.

type fakeKeyStore struct {
	key   Key
	found bool
}

func (f *fakeKeyStore) LookupBrowserKey(context.Context, [][]byte) (Key, error) {
	if !f.found {
		return Key{}, ErrUnknownKey
	}
	return f.key, nil
}

func (f *fakeKeyStore) TouchBrowserKeys(context.Context, []string, time.Time) error { return nil }

func keysFor(k Key) *Keys {
	return NewKeys(&fakeKeyStore{key: k, found: true}, CacheOptions{})
}

func browserKey() Key {
	return Key{KeyID: "k-browser", TenantID: "tenant-a", ServiceName: "shop-web", Kind: KindBrowser,
		Origins: []string{"https://shop.example.com"}, RateLimitPerMinute: 6000, SampleRate: 1}
}

func mobileKey() Key {
	return Key{KeyID: "k-mobile", TenantID: "tenant-a", ServiceName: "shop-android", Kind: KindMobile,
		AppIDs: []string{"com.example.shop"}, RateLimitPerMinute: 6000, SampleRate: 1}
}

func TestResolveChecksTheAllowlistOfTheKeysKind(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		key   Key
		scope Scope
		want  error
	}{
		"browser key from its origin":  {browserKey(), Scope{Origin: "https://shop.example.com"}, nil},
		"browser key from elsewhere":   {browserKey(), Scope{Origin: "https://evil.example.com"}, ErrOriginNotAllowed},
		"browser key without origin":   {browserKey(), Scope{}, ErrOriginNotAllowed},
		"mobile key from its app":      {mobileKey(), Scope{AppID: "com.example.shop"}, nil},
		"mobile key from another app":  {mobileKey(), Scope{AppID: "com.example.other"}, ErrAppNotAllowed},
		"mobile key without an app id": {mobileKey(), Scope{}, ErrAppNotAllowed},

		// The two scopes are not interchangeable, and this is the pair that catches a branch falling
		// through: a mobile key presented with a perfectly good origin is still a mobile key, and an
		// application id says nothing about a browser key.
		"mobile key with only an origin":  {mobileKey(), Scope{Origin: "https://shop.example.com"}, ErrAppNotAllowed},
		"browser key with only an app id": {browserKey(), Scope{AppID: "com.example.shop"}, ErrOriginNotAllowed},

		// Rows written before 0098_mobile_keys have no kind and must keep meaning what they meant.
		"key stored without a kind": {
			Key{KeyID: "k-old", TenantID: "tenant-a", Origins: []string{"https://shop.example.com"}},
			Scope{Origin: "https://shop.example.com"}, nil,
		},
	} {
		got, err := keysFor(tc.key).Resolve(ctx, "olb_whatever", tc.scope)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
			continue
		}
		if tc.want == nil && got.KeyID != tc.key.KeyID {
			t.Errorf("%s: resolved %q, want %q", name, got.KeyID, tc.key.KeyID)
		}
	}
}

func TestResolveRefusesAnEmptyValue(t *testing.T) {
	if _, err := keysFor(browserKey()).Resolve(context.Background(), "", Scope{Origin: "https://shop.example.com"}); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("empty key value: %v", err)
	}
}

func TestResolveReportsAnUnknownKey(t *testing.T) {
	k := NewKeys(&fakeKeyStore{}, CacheOptions{})
	if _, err := k.Resolve(context.Background(), "olb_nope", Scope{Origin: "https://shop.example.com"}); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}
}
