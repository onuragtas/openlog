package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/fleet"
)

type batchWriter struct {
	mu      sync.Mutex
	batches [][]fleet.HostRecord
	err     error
}

func (w *batchWriter) UpsertHosts(_ context.Context, recs []fleet.HostRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.batches = append(w.batches, append([]fleet.HostRecord(nil), recs...))
	return nil
}

func report(host, rollout string) fleet.HostRecord {
	return fleet.HostRecord{TenantID: "t", Report: fleet.HostReport{HostID: host}, RolloutID: rollout}
}

func TestRecorderDropsOldestAndCoalesces(t *testing.T) {
	w := &batchWriter{}
	reg := prometheus.NewRegistry()
	r := fleet.NewRecorder(w, fleet.RecorderOptions{MaxPending: 3, BatchSize: 2, Registerer: reg})
	r.Record(report("h1", "r1"))
	r.Record(report("h2", ""))
	r.Record(report("h1", "")) // coalesced: keeps its place and the offered rollout id
	r.Record(report("h3", ""))
	r.Record(report("h4", "")) // queue full: h1 (oldest) is dropped
	if r.Len() != 3 {
		t.Fatalf("len = %d", r.Len())
	}
	r.Flush(context.Background())
	var got []string
	for _, b := range w.batches {
		if len(b) > 2 {
			t.Errorf("batch of %d", len(b))
		}
		for _, rec := range b {
			got = append(got, rec.Report.HostID)
		}
	}
	if fmt.Sprint(got) != "[h2 h3 h4]" {
		t.Errorf("written = %v", got)
	}
	metrics, _ := reg.Gather()
	found := false
	for _, mf := range metrics {
		if mf.GetName() == "openlog_fleet_host_reports_dropped_total" && mf.GetMetric()[0].GetCounter().GetValue() == 1 {
			found = true
		}
	}
	if !found {
		t.Error("dropped report not counted")
	}

	w2 := &batchWriter{}
	r2 := fleet.NewRecorder(w2, fleet.RecorderOptions{})
	r2.Record(report("h1", "r1"))
	r2.Record(report("h1", ""))
	r2.Flush(context.Background())
	if len(w2.batches) != 1 || w2.batches[0][0].RolloutID != "r1" {
		t.Errorf("coalesced record lost the rollout id: %+v", w2.batches)
	}
}

func TestRecorderFailedBatchIsDropped(t *testing.T) {
	w := &batchWriter{err: errors.New("postgres down")}
	r := fleet.NewRecorder(w, fleet.RecorderOptions{})
	r.Record(report("h1", ""))
	r.Flush(context.Background())
	if r.Len() != 0 {
		t.Fatal("failed batch kept in the queue")
	}
}

func TestRecorderRunFlushesOnBatchAndShutdown(t *testing.T) {
	w := &batchWriter{}
	r := fleet.NewRecorder(w, fleet.RecorderOptions{BatchSize: 2, FlushInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	r.Record(report("h1", ""))
	r.Record(report("h2", "")) // full batch wakes the writer
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		n := len(w.batches)
		w.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("full batch not written")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.Record(report("h3", ""))
	cancel()
	<-done
	if len(w.batches) != 2 {
		t.Errorf("final flush missing: %d batches", len(w.batches))
	}
}

type countingLoader struct {
	calls atomic.Int32
	err   atomic.Value
	delay time.Duration
}

func (l *countingLoader) LoadOrgState(_ context.Context, tenant string) (fleet.OrgState, error) {
	l.calls.Add(1)
	time.Sleep(l.delay)
	if e, ok := l.err.Load().(error); ok && e != nil {
		return fleet.OrgState{}, e
	}
	if tenant == "unknown" {
		return fleet.OrgState{}, fleet.ErrNotFound
	}
	return fleet.OrgState{OrgID: "org-" + tenant, Policy: fleet.DefaultPolicy()}, nil
}

func TestStateCache(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	l := &countingLoader{delay: 20 * time.Millisecond}
	c := fleet.NewStateCache(l, fleet.StateCacheOptions{TTL: 10 * time.Second, MaxStale: time.Minute, RetryAfter: 5 * time.Second,
		Now: func() time.Time { return now }})
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st, found, ok := c.Get(ctx, "t1"); !ok || !found || st.OrgID != "org-t1" {
				t.Errorf("get = %+v %v %v", st, found, ok)
			}
		}()
	}
	wg.Wait()
	if n := l.calls.Load(); n != 1 {
		t.Fatalf("concurrent misses made %d loads", n)
	}
	now = now.Add(9 * time.Second)
	c.Get(ctx, "t1")
	if l.calls.Load() != 1 {
		t.Fatal("reloaded before TTL")
	}
	now = now.Add(2 * time.Second)
	c.Get(ctx, "t1")
	if l.calls.Load() != 2 {
		t.Fatal("not reloaded after TTL")
	}

	if _, found, ok := c.Get(ctx, "unknown"); !ok || found {
		t.Errorf("unknown tenant: found=%v ok=%v", found, ok)
	}

	// Store down: stale entry served within MaxStale, unknown tenants unavailable.
	l.err.Store(errors.New("down"))
	now = now.Add(15 * time.Second)
	if _, _, ok := c.Get(ctx, "t1"); !ok {
		t.Error("stale entry not served")
	}
	calls := l.calls.Load()
	c.Get(ctx, "t1")
	if l.calls.Load() != calls {
		t.Error("retried before RetryAfter")
	}
	if _, _, ok := c.Get(ctx, "t2"); ok {
		t.Error("uncached tenant served while the store is down")
	}
	now = now.Add(2 * time.Minute)
	if _, _, ok := c.Get(ctx, "t1"); ok {
		t.Error("served past MaxStale")
	}
}
