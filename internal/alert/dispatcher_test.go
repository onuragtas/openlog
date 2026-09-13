package alert

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/alert/notify"
	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// memOutbox emulates the outbox queries (each method atomic, like SKIP LOCKED statements).
type memOutbox struct {
	mu        sync.Mutex
	now       time.Time
	seq       int
	rows      map[string]*memRow
	channels  map[string]*Channel
	incidents map[string]*Incident
	mutes     []Mute
	attempts  []Attempt
	events    []IncidentEvent
}

type memRow struct {
	n            Notification
	seq          int
	claimedBy    string
	claimedUntil time.Time
	mutedLogged  bool
}

func newMemOutbox() *memOutbox {
	return &memOutbox{now: t0, rows: map[string]*memRow{}, channels: map[string]*Channel{}, incidents: map[string]*Incident{}}
}

func (s *memOutbox) clock() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

func (s *memOutbox) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

func (s *memOutbox) add(n Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.n.IdempotencyKey == n.IdempotencyKey {
			return // ON CONFLICT DO NOTHING
		}
	}
	s.seq++
	n.Status, n.CreatedAt, n.NextAttemptAt = StatusPending, s.now, s.now
	s.rows[n.ID] = &memRow{n: n, seq: s.seq}
}

func (s *memOutbox) RequeueExpired(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := 0
	for _, r := range s.rows {
		if r.n.Status == StatusSending && !r.claimedUntil.After(s.now) {
			r.n.Status, r.claimedBy = StatusPending, ""
			c++
		}
	}
	return c, nil
}

func (s *memOutbox) ClaimDeliveries(_ context.Context, instance string, n int, ttl time.Duration) ([]*Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []*memRow
	for _, r := range s.rows {
		if r.n.Status != StatusPending || r.n.NextAttemptAt.After(s.now) {
			continue
		}
		blocked := false
		for _, o := range s.rows {
			if o.n.IncidentID == r.n.IncidentID && o.n.ChannelID == r.n.ChannelID && o.seq < r.seq &&
				(o.n.Status == StatusPending || o.n.Status == StatusSending) {
				blocked = true
			}
		}
		if !blocked {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].seq < due[j].seq })
	var out []*Delivery
	for _, r := range due {
		if len(out) == n {
			break
		}
		r.n.Status, r.claimedBy, r.claimedUntil = StatusSending, instance, s.now.Add(ttl)
		r.n.Attempts++
		d := &Delivery{Notification: r.n, Channel: s.channels[r.n.ChannelID], MutedLogged: r.mutedLogged}
		if inc := s.incidents[r.n.IncidentID]; inc != nil {
			c := *inc
			d.Incident = &c
		}
		for _, o := range s.rows {
			if o.n.IncidentID == r.n.IncidentID && o.n.ChannelID == r.n.ChannelID && o.n.Kind == KindOpened {
				d.OpenedStatus = o.n.Status
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *memOutbox) FinishDelivery(_ context.Context, instance string, d *Delivery, out DeliveryOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[d.ID]
	if out.Attempt != nil {
		s.attempts = append(s.attempts, *out.Attempt)
	}
	if r.n.Status != StatusSending || r.claimedBy != instance {
		return errors.New("claim lost")
	}
	r.n.Status, r.n.LastError, r.mutedLogged, r.claimedBy = out.Status, out.Error, out.MutedLogged, ""
	if out.Status == StatusPending {
		r.n.NextAttemptAt = out.NextAttemptAt
	}
	if out.Event != nil {
		s.events = append(s.events, *out.Event)
	}
	return nil
}

func (s *memOutbox) ActiveMutes(_ context.Context, orgID string, at time.Time) ([]Mute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Mute
	for _, m := range s.mutes {
		if m.OrgID == orgID && m.Active(at) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *memOutbox) PendingCount(context.Context) (int, error)     { return 0, nil }
func (s *memOutbox) Prune(context.Context, time.Time) (int, error) { return 0, nil }
func (s *memOutbox) status(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id].n.Status
}
func (s *memOutbox) row(id string) Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id].n
}
func (s *memOutbox) setIncidentState(id, state string) {
	s.mu.Lock()
	s.incidents[id].State = state
	s.mu.Unlock()
}
func (s *memOutbox) addMute(m Mute) { s.mu.Lock(); s.mutes = append(s.mutes, m); s.mu.Unlock() }
func (s *memOutbox) eventKinds() (k []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		k = append(k, e.Kind)
	}
	return
}

// fakeSender counts deliveries per idempotency key.
type fakeSender struct {
	mu     sync.Mutex
	sent   map[string]int
	fail   func(key string, n int) notify.Result
	before func() // called before recording (simulates a crash window)
}

func (f *fakeSender) Send(_ context.Context, _ notify.Target, ev notify.Event, _ string) notify.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sent == nil {
		f.sent = map[string]int{}
	}
	if f.fail != nil {
		if r := f.fail(ev.IdempotencyKey, f.sent[ev.IdempotencyKey]); !r.OK() {
			return r
		}
	}
	f.sent[ev.IdempotencyKey]++
	return notify.Result{StatusCode: 200}
}

func (f *fakeSender) count(key string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.sent[key] }

func testKeyring(t *testing.T) *secrets.Keyring {
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func seedOutbox(t *testing.T, kr *secrets.Keyring, s *memOutbox, incidents int) []string {
	ch := &Channel{ID: "ch-1", OrgID: "org-1", Name: "ops-webhook", Type: notify.TypeWebhook, Enabled: true}
	var err error
	if ch.Secrets, _, err = EncryptSecrets(kr, "org-1", "ch-1", map[string]string{"url": "https://hooks.example/x"}); err != nil {
		t.Fatal(err)
	}
	s.channels["ch-1"] = ch
	var ids []string
	for i := 0; i < incidents; i++ {
		inc := &Incident{ID: fmt.Sprintf("inc-%d", i), OrgID: "org-1", RuleID: "rule-1", State: IncidentOpen, Labels: map[string]string{"host.name": "web-1"}}
		s.incidents[inc.ID] = inc
		payload, _ := json.Marshal(notify.Event{Event: notify.EventOpened, IdempotencyKey: IdempotencyKey(inc.ID, KindOpened, "", "ch-1")})
		id := fmt.Sprintf("n-%d", i)
		s.add(Notification{ID: id, OrgID: "org-1", IncidentID: inc.ID, RuleID: "rule-1", ChannelID: "ch-1", ChannelType: notify.TypeWebhook,
			Kind: KindOpened, IdempotencyKey: IdempotencyKey(inc.ID, KindOpened, "", "ch-1"), Payload: payload})
		ids = append(ids, id)
	}
	return ids
}

func newTestDispatcher(s *memOutbox, sender Sender, kr *secrets.Keyring, name string) *Dispatcher {
	return NewDispatcher(s, sender, kr, DispatcherOptions{Instance: name, Workers: 3, MaxAttempts: 4, Now: s.clock})
}

func TestDispatchersDeliverEachNotificationOnce(t *testing.T) {
	kr := testKeyring(t)
	s := newMemOutbox()
	ids := seedOutbox(t, kr, s, 60)
	sender := &fakeSender{}
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c"} {
		d := newTestDispatcher(s, sender, kr, name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := d.RunOnce(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	for i, id := range ids {
		key := IdempotencyKey(fmt.Sprintf("inc-%d", i), KindOpened, "", "ch-1")
		if s.status(id) != StatusDelivered || sender.count(key) != 1 {
			t.Fatalf("%s: status %s, sent %d times", id, s.status(id), sender.count(key))
		}
	}
}

func TestDispatcherPodDeathRequeuesWithoutLoss(t *testing.T) {
	kr := testKeyring(t)
	s := newMemOutbox()
	ids := seedOutbox(t, kr, s, 1)
	sender := &fakeSender{}
	dead := newTestDispatcher(s, sender, kr, "dead")
	// The dead pod claims the row and is killed before delivering (no Finish).
	claimed, _ := s.ClaimDeliveries(context.Background(), "dead", 10, dead.ClaimTTL())
	if len(claimed) != 1 {
		t.Fatalf("claimed %d", len(claimed))
	}
	alive := newTestDispatcher(s, sender, kr, "alive")
	if n, _ := alive.RunOnce(context.Background()); n != 0 {
		t.Fatal("a claimed row was taken over before its claim expired")
	}
	s.advance(dead.ClaimTTL() + time.Second)
	if n, _ := alive.RunOnce(context.Background()); n != 1 {
		t.Fatal("expired claim not requeued")
	}
	key := IdempotencyKey("inc-0", KindOpened, "", "ch-1")
	if s.status(ids[0]) != StatusDelivered || sender.count(key) != 1 {
		t.Fatalf("after takeover: %s sent=%d", s.status(ids[0]), sender.count(key))
	}
	// The dead pod's late outcome is rejected (its claim is gone): no double bookkeeping.
	if err := s.FinishDelivery(context.Background(), "dead", claimed[0], DeliveryOutcome{Status: StatusFailed}); err == nil {
		t.Fatal("stale claim accepted")
	}
	if s.status(ids[0]) != StatusDelivered {
		t.Fatal("stale outcome overwrote delivered status")
	}
}

func TestDispatcherRetryBackoffAndGiveUp(t *testing.T) {
	kr := testKeyring(t)
	s := newMemOutbox()
	ids := seedOutbox(t, kr, s, 2)
	sender := &fakeSender{fail: func(key string, _ int) notify.Result {
		if key == IdempotencyKey("inc-1", KindOpened, "", "ch-1") {
			return notify.Result{StatusCode: 404, Err: errors.New("HTTP 404")}
		}
		return notify.Result{StatusCode: 503, Err: errors.New("HTTP 503"), Retryable: true}
	}}
	d := newTestDispatcher(s, sender, kr, "a")
	var waits []time.Duration
	for i := 0; i < 10; i++ {
		before := s.clock()
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if s.status(ids[0]) == StatusPending {
			waits = append(waits, s.row(ids[0]).NextAttemptAt.Sub(before))
			s.advance(s.row(ids[0]).NextAttemptAt.Sub(before))
		}
	}
	if s.status(ids[1]) != StatusFailed || s.row(ids[1]).Attempts != 1 {
		t.Fatalf("permanent failure retried: %+v", s.row(ids[1]))
	}
	if s.status(ids[0]) != StatusFailed || s.row(ids[0]).Attempts != 4 {
		t.Fatalf("retryable failure: %+v", s.row(ids[0]))
	}
	if len(waits) != 3 || waits[0] < 8*time.Second || waits[1] < 24*time.Second || waits[2] < 48*time.Second {
		t.Fatalf("backoff %v", waits)
	}
	kinds := s.eventKinds()
	if len(kinds) != 2 || kinds[0] != EventNotificationFailed {
		t.Errorf("timeline %v", kinds)
	}
	if len(s.attempts) != 5 {
		t.Errorf("delivery log has %d attempts, want 5", len(s.attempts))
	}
}

func TestDispatcherOrderingSuppressionAndMutes(t *testing.T) {
	kr := testKeyring(t)
	s := newMemOutbox()
	seedOutbox(t, kr, s, 1)
	fail := true
	sender := &fakeSender{fail: func(string, int) notify.Result {
		if fail {
			return notify.Result{Err: errors.New("down"), Retryable: true}
		}
		return notify.Result{StatusCode: 200}
	}}
	resolved, _ := json.Marshal(notify.Event{Event: notify.EventResolved})
	s.add(Notification{ID: "n-resolve", OrgID: "org-1", IncidentID: "inc-0", ChannelID: "ch-1", ChannelType: "webhook", Kind: KindResolved,
		IdempotencyKey: IdempotencyKey("inc-0", KindResolved, "", "ch-1"), Payload: resolved})
	d := newTestDispatcher(s, sender, kr, "a")
	// While the opening notification is retrying, the resolve row is not claimed.
	d.RunOnce(context.Background())
	if s.status("n-resolve") != StatusPending || s.row("n-resolve").Attempts != 0 {
		t.Fatal("resolve claimed before the opening notification finished")
	}
	// Opening gives up → resolve is suppressed, never sent alone.
	for i := 0; i < 6; i++ {
		s.advance(time.Hour)
		d.RunOnce(context.Background())
	}
	if s.status("n-0") != StatusFailed || s.status("n-resolve") != StatusSuppressed || sender.count(IdempotencyKey("inc-0", KindResolved, "", "ch-1")) != 0 {
		t.Fatalf("opened=%s resolve=%s", s.status("n-0"), s.status("n-resolve"))
	}

	// Mutes postpone the opening notification until the mute ends; resolved meanwhile → suppressed.
	s2 := newMemOutbox()
	seedOutbox(t, kr, s2, 1)
	s2.addMute(Mute{ID: "m1", OrgID: "org-1", Name: "maintenance", StartsAt: t0.Add(-time.Minute), EndsAt: t0.Add(time.Hour),
		Matchers: []MuteMatcher{{Label: "host.name", Op: "eq", Value: "web-1"}}})
	fail = false
	d2 := newTestDispatcher(s2, sender, kr, "a")
	d2.RunOnce(context.Background())
	if r := s2.row("n-0"); r.Status != StatusPending || !r.NextAttemptAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("muted opening: %+v", r)
	}
	s2.setIncidentState("inc-0", IncidentResolved)
	s2.advance(time.Hour)
	d2.RunOnce(context.Background())
	if s2.status("n-0") != StatusSuppressed || sender.count(IdempotencyKey("inc-0", KindOpened, "", "ch-1")) != 0 {
		t.Fatalf("resolved during mute: %s", s2.status("n-0"))
	}
	if kinds := s2.eventKinds(); len(kinds) != 2 || kinds[0] != EventNotificationMuted || kinds[1] != EventNotificationSuppressed {
		t.Errorf("mute timeline %v", kinds)
	}
}

func TestDispatcherRenotifyAndDeletedChannel(t *testing.T) {
	kr := testKeyring(t)
	s := newMemOutbox()
	seedOutbox(t, kr, s, 1)
	sender := &fakeSender{}
	d := newTestDispatcher(s, sender, kr, "a")
	d.RunOnce(context.Background())
	renotify, _ := json.Marshal(notify.Event{Event: notify.EventRenotify})
	s.add(Notification{ID: "n-re", OrgID: "org-1", IncidentID: "inc-0", ChannelID: "ch-1", ChannelType: "webhook", Kind: KindRenotify,
		IdempotencyKey: IdempotencyKey("inc-0", KindRenotify, "1", "ch-1"), Payload: renotify})
	s.setIncidentState("inc-0", IncidentAcknowledged)
	d.RunOnce(context.Background())
	if s.status("n-re") != StatusSuppressed {
		t.Fatalf("renotify of acknowledged incident: %s", s.status("n-re"))
	}
	s.add(Notification{ID: "n-gone", OrgID: "org-1", IncidentID: "inc-0", ChannelID: "ch-deleted", ChannelType: "slack", Kind: KindOpened,
		IdempotencyKey: "inc-0:opened:ch-deleted", Payload: renotify})
	d.RunOnce(context.Background())
	if s.status("n-gone") != StatusFailed {
		t.Fatalf("deleted channel: %s", s.status("n-gone"))
	}
}
