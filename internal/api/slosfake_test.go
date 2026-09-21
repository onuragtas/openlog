package api

import (
	"context"
	"time"

	"github.com/onuragtas/openlog/internal/slo"
)

// Fakes for the two route groups closest in shape to job monitoring: a five-method store, a setter taking it,
// and three read endpoints of which the last runs a ClickHouse query. That last one is where jobs broke, so
// it is the reason these exist — a route that is never registered is a route whose SQL nothing has built.

type fakeSLOStore struct{}

func (fakeSLOStore) List(context.Context, string) ([]slo.SLO, error) {
	return []slo.SLO{testSLO()}, nil
}

func (fakeSLOStore) Get(context.Context, string, string) (*slo.SLO, error) {
	s := testSLO()
	return &s, nil
}

func (fakeSLOStore) Create(context.Context, string, slo.Input, slo.Actor) (*slo.SLO, error) {
	s := testSLO()
	return &s, nil
}

func (fakeSLOStore) Update(context.Context, string, string, slo.Input, slo.Actor) (*slo.SLO, error) {
	s := testSLO()
	return &s, nil
}

func (fakeSLOStore) Delete(context.Context, string, string, slo.Actor) error { return nil }

func testSLO() slo.SLO {
	at := time.Unix(1757757600, 0).UTC()
	return slo.SLO{
		ID:    "22222222-2222-2222-2222-222222222222",
		OrgID: "org-1",
		Input: slo.Input{
			Name: "checkout availability", ServiceName: "orders",
			SLIType: "availability", Objective: 99.9, WindowDays: 30,
		},
		CreatedAt: at, UpdatedAt: at,
	}
}
