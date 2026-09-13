package memstore_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/authtest"
	"github.com/onuragtas/openlog/internal/auth/memstore"
)

func TestServiceConformance(t *testing.T) {
	authtest.Run(t, func(*testing.T) auth.Store { return memstore.New() })
}

func TestStoreOutage(t *testing.T) {
	st := memstore.New()
	e := authtest.NewEnv(t, st, auth.Config{})
	_, owner := e.Bootstrap("down")
	st.SetErr(errors.New("connection refused"))
	_, err := e.Auth(http.MethodGet, owner)
	if !errors.Is(err, auth.ErrUnavailable) {
		t.Fatalf("auth during outage: %v, want unavailable", err)
	}
	_, err = e.Svc.Login(context.Background(), e.Email("owner-down"), authtest.Password, e.Meta)
	if !errors.Is(err, auth.ErrUnavailable) {
		t.Fatalf("login during outage: %v", err)
	}
	st.SetErr(nil)
	if _, err := e.Auth(http.MethodGet, owner); err != nil {
		t.Fatalf("after outage: %v", err)
	}
}

func TestDisabledUser(t *testing.T) {
	st := memstore.New()
	e := authtest.NewEnv(t, st, auth.Config{})
	_, owner := e.Bootstrap("dis")
	st.DisableUser(owner.P.UserID, time.Now())
	if _, err := e.Auth(http.MethodGet, owner); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled user session: %v", err)
	}
	if _, err := e.Svc.Login(context.Background(), e.Email("owner-dis"), authtest.Password, e.Meta); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled user login: %v", err)
	}
}
