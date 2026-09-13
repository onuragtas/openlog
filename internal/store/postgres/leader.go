package postgres

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaderLock is the session advisory lock held by the single leader among all openlog-api pods
// (docs/contracts/releases-updates.md §4, §5). Background jobs that must run once per cluster
// (update check, fleet rollout transitions) register with one Leader per process.
const LeaderLock int64 = 0x6f6c2d6c656164 // "ol-lead"

// Leader runs registered tasks while this process holds LeaderLock. The lock lives on a
// dedicated connection taken out of the pool; if that connection breaks the tasks are cancelled
// and PostgreSQL releases the lock, so another pod takes over.
type Leader struct {
	pool  *pgxpool.Pool
	log   *slog.Logger
	mu    sync.Mutex
	tasks []leaderTask
	held  atomic.Bool

	// RetryInterval is how often a follower tries to take the lock; PingInterval how often the
	// leader verifies its connection.
	RetryInterval time.Duration
	PingInterval  time.Duration
}

type leaderTask struct {
	name string
	fn   func(ctx context.Context)
}

// NewLeader creates a leader elector on pool.
func NewLeader(pool *pgxpool.Pool, log *slog.Logger) *Leader {
	return &Leader{pool: pool, log: log, RetryInterval: 15 * time.Second, PingInterval: 10 * time.Second}
}

// Add registers a task. fn runs while leadership is held and must return when ctx is done. Add
// must be called before Run.
func (l *Leader) Add(name string, fn func(ctx context.Context)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tasks = append(l.tasks, leaderTask{name, fn})
}

// IsLeader reports whether this process currently holds the lock.
func (l *Leader) IsLeader() bool { return l.held.Load() }

// Run campaigns for leadership until ctx is done.
func (l *Leader) Run(ctx context.Context) {
	l.mu.Lock()
	tasks := append([]leaderTask(nil), l.tasks...)
	l.mu.Unlock()
	if len(tasks) == 0 {
		return
	}
	for {
		if err := l.lead(ctx, tasks); err != nil && ctx.Err() == nil {
			l.log.Debug("leader election", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.RetryInterval):
		}
	}
}

var errNotLeader = errors.New("lock held by another instance")

func (l *Leader) lead(ctx context.Context, tasks []leaderTask) error {
	actx, cancel := context.WithTimeout(ctx, 10*time.Second)
	pc, err := l.pool.Acquire(actx)
	cancel()
	if err != nil {
		return err
	}
	var got bool
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = pc.QueryRow(qctx, "SELECT pg_try_advisory_lock($1)", LeaderLock).Scan(&got)
	cancel()
	if err != nil || !got {
		pc.Release()
		if err == nil {
			err = errNotLeader
		}
		return err
	}
	// The session owns the lock: never hand this connection back to the pool.
	conn := pc.Hijack()
	defer closeConn(conn)
	l.held.Store(true)
	defer l.held.Store(false)
	l.log.Info("acquired leadership", "tasks", len(tasks))

	lctx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.fn(lctx)
		}()
	}
	tick := time.NewTicker(l.PingInterval)
	defer tick.Stop()
	for lctx.Err() == nil {
		select {
		case <-lctx.Done():
		case <-tick.C:
			pctx, cancel := context.WithTimeout(lctx, 5*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil && ctx.Err() == nil {
				l.log.Warn("lost leadership connection", "err", err)
				stop()
			}
		}
	}
	stop()
	wg.Wait()
	l.log.Info("released leadership")
	return nil
}

func closeConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", LeaderLock)
	_ = conn.Close(ctx)
}
