package k8s

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// Leader election timing (§7.4): a lease is valid for LeaseDuration after its renew time; the holder renews every
// RenewEvery and gives up leadership when it could not renew within RenewDeadline.
const (
	LeaseDuration = 15 * time.Second
	RenewEvery    = 5 * time.Second
	RenewDeadline = 10 * time.Second
)

// Elector acquires and renews a coordination.k8s.io/v1 Lease.
type Elector struct {
	Client    *Client
	Namespace string
	Name      string
	Identity  string
	Log       *slog.Logger
	Now       func() time.Time

	lastRenew time.Time
	leading   bool
}

func (e *Elector) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Elector) path() string {
	return "/apis/coordination.k8s.io/v1/namespaces/" + url.PathEscape(e.Namespace) + "/leases/" + url.PathEscape(e.Name)
}

// TryAcquireOrRenew performs one election step and reports whether this identity holds the lease afterwards.
func (e *Elector) TryAcquireOrRenew(ctx context.Context) (bool, error) {
	now := e.now()
	ctx, cancel := context.WithTimeout(ctx, RenewDeadline/2)
	defer cancel()
	dur := int(LeaseDuration / time.Second)
	var cur Lease
	err := e.Client.Get(ctx, e.path(), nil, &cur)
	switch {
	case IsStatus(err, http.StatusNotFound):
		id, t, zero := e.Identity, MicroTime{now}, 0
		l := Lease{APIVersion: "coordination.k8s.io/v1", Kind: "Lease", Metadata: LeaseMeta{Name: e.Name, Namespace: e.Namespace},
			Spec: LeaseSpecs{HolderIdentity: &id, LeaseDurationSeconds: &dur, AcquireTime: &t, RenewTime: &t, LeaseTransitions: &zero}}
		path := "/apis/coordination.k8s.io/v1/namespaces/" + url.PathEscape(e.Namespace) + "/leases"
		if err := e.Client.Do(ctx, http.MethodPost, path, nil, l, nil); err != nil {
			if IsStatus(err, http.StatusConflict) {
				return false, nil // someone else created it first
			}
			return false, err
		}
		return true, nil
	case err != nil:
		return false, err
	}
	holder := ""
	if cur.Spec.HolderIdentity != nil {
		holder = *cur.Spec.HolderIdentity
	}
	leaseDur := LeaseDuration
	if cur.Spec.LeaseDurationSeconds != nil && *cur.Spec.LeaseDurationSeconds > 0 {
		leaseDur = time.Duration(*cur.Spec.LeaseDurationSeconds) * time.Second
	}
	var renewed time.Time
	if cur.Spec.RenewTime != nil {
		renewed = cur.Spec.RenewTime.Time
	}
	if holder != "" && holder != e.Identity && now.Before(renewed.Add(leaseDur)) {
		return false, nil // held by another live replica
	}
	t := MicroTime{now}
	spec := cur.Spec
	id := e.Identity
	spec.HolderIdentity, spec.LeaseDurationSeconds, spec.RenewTime = &id, &dur, &t
	if holder != e.Identity {
		transitions := 0
		if spec.LeaseTransitions != nil {
			transitions = *spec.LeaseTransitions
		}
		transitions++
		spec.LeaseTransitions, spec.AcquireTime = &transitions, &t
	}
	upd := Lease{APIVersion: "coordination.k8s.io/v1", Kind: "Lease",
		Metadata: LeaseMeta{Name: e.Name, Namespace: e.Namespace, ResourceVersion: cur.Metadata.ResourceVersion}, Spec: spec}
	if err := e.Client.Do(ctx, http.MethodPut, e.path(), nil, upd, nil); err != nil {
		if IsStatus(err, http.StatusConflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Run keeps electing until ctx ends. onStart runs with a context that is cancelled when leadership is lost; Run
// waits for onStart to return before trying again. The lease is released (holder cleared) on shutdown.
func (e *Elector) Run(ctx context.Context, onStart func(ctx context.Context)) {
	var (
		leadCancel context.CancelFunc
		leadDone   chan struct{}
	)
	stopLeading := func(reason string) {
		if leadCancel == nil {
			return
		}
		leadCancel()
		<-leadDone
		leadCancel, leadDone = nil, nil
		if e.Log != nil {
			e.Log.Warn("kubernetes cluster collection: leadership lost", "reason", reason)
		}
	}
	ticker := time.NewTicker(RenewEvery)
	defer ticker.Stop()
	for {
		ok, err := e.TryAcquireOrRenew(ctx)
		now := e.now()
		switch {
		case ok:
			e.lastRenew = now
			if leadCancel == nil {
				if e.Log != nil {
					e.Log.Info("kubernetes cluster collection: became leader", "lease", e.Namespace+"/"+e.Name, "identity", e.Identity)
				}
				lctx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				leadCancel, leadDone = cancel, done
				go func() {
					defer close(done)
					onStart(lctx)
				}()
			}
		case err != nil && leadCancel != nil && now.Sub(e.lastRenew) < RenewDeadline:
			// transient error: keep leading until the renew deadline
			if e.Log != nil {
				e.Log.Debug("lease renew failed", "error", err)
			}
		default:
			if err != nil && e.Log != nil && !errors.Is(err, context.Canceled) {
				e.Log.Warn("lease election failed", "error", err)
			}
			stopLeading("lease not renewed")
		}
		select {
		case <-ctx.Done():
			wasLeading := leadCancel != nil
			if leadCancel != nil {
				leadCancel()
				<-leadDone
			}
			if wasLeading {
				e.release()
			}
			return
		case <-ticker.C:
		}
	}
}

// release clears the holder so another replica takes over without waiting for the lease to expire.
func (e *Elector) release() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var cur Lease
	if err := e.Client.Get(ctx, e.path(), nil, &cur); err != nil {
		return
	}
	if cur.Spec.HolderIdentity == nil || *cur.Spec.HolderIdentity != e.Identity {
		return
	}
	empty, one := "", 1
	past := MicroTime{e.now().Add(-LeaseDuration)}
	cur.Spec.HolderIdentity, cur.Spec.LeaseDurationSeconds, cur.Spec.RenewTime = &empty, &one, &past
	upd := Lease{APIVersion: "coordination.k8s.io/v1", Kind: "Lease",
		Metadata: LeaseMeta{Name: e.Name, Namespace: e.Namespace, ResourceVersion: cur.Metadata.ResourceVersion}, Spec: cur.Spec}
	_ = e.Client.Do(ctx, http.MethodPut, e.path(), nil, upd, nil)
}
