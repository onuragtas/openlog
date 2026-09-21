package api

import (
	"context"
	"time"

	"github.com/onuragtas/openlog/internal/jobs"
)

// fakeJobStore is enough of jobs.Store to register the job monitoring routes.
//
// It exists because those routes are gated on the dependency: jobRoutes returns early when s.jobs is nil, so
// a test server without one registers nothing and the route-coverage walk cannot see them at all. That is
// how an endpoint reaches production having never had its query built — the state GET /api/v1/vulnerabilities
// was in when it shipped broken — and jobs is one of nineteen route groups in the same position.
//
// Only Get and List matter here: the read handlers call them and then build the ClickHouse queries, which is
// where a query that cannot be built shows itself.
type fakeJobStore struct{}

func (fakeJobStore) List(context.Context, string) ([]jobs.Monitor, error) {
	return []jobs.Monitor{testJobMonitor()}, nil
}

func (fakeJobStore) Get(context.Context, string, string) (*jobs.Monitor, error) {
	m := testJobMonitor()
	return &m, nil
}

func (fakeJobStore) Create(context.Context, string, jobs.Input, jobs.Actor) (*jobs.Monitor, error) {
	m := testJobMonitor()
	return &m, nil
}

func (fakeJobStore) Update(context.Context, string, string, jobs.Input, jobs.Actor) (*jobs.Monitor, error) {
	m := testJobMonitor()
	return &m, nil
}

func (fakeJobStore) Delete(context.Context, string, string, jobs.Actor) error { return nil }

func (fakeJobStore) Rotate(context.Context, string, string, jobs.Actor) (*jobs.Monitor, error) {
	m := testJobMonitor()
	return &m, nil
}

func testJobMonitor() jobs.Monitor {
	at := time.Unix(1757757600, 0).UTC()
	return jobs.Monitor{
		ID:    "11111111-1111-1111-1111-111111111111",
		OrgID: "org-1",
		Input: jobs.Input{
			Name: "nightly-backup", Kind: jobs.KindInterval,
			IntervalSeconds: 3600, GraceSeconds: 300, Enabled: true,
		},
		Token:     "tok_test",
		CreatedAt: at,
		UpdatedAt: at,
	}
}
