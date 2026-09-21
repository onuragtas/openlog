package api

import (
	"context"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/jobs"
)

// The job monitoring reads answered 500 for every request. The handlers return the query layer's error as
// it is, so the endpoint could not even build its SQL — nothing to do with ClickHouse or with the data.
// These call the two queries directly, because a handler test only ever shows "internal error".
func TestJobRunQueriesBuild(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	to := time.Unix(1757757600, 0).UTC()
	from := to.Add(-24 * time.Hour)

	if _, err := jobs.History(context.Background(), sc, "m1", from, to, 50); err != nil {
		t.Errorf("History: %v", err)
	}
	if _, err := jobs.Summaries(context.Background(), sc, "m1", from, to); err != nil {
		t.Errorf("Summaries: %v", err)
	}
}
