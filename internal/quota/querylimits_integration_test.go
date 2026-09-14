//go:build integration

package quota_test

import (
	"context"
	"testing"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/quota"
)

func TestOrgQueryLimitsStore(t *testing.T) {
	pool := pgPool(t)
	ctx := context.Background()
	st := quota.PGStore{Pool: pool}
	orgID, ownerID := createOrg(t, pool, "qlimits")

	if _, found, err := st.GetOrgQueryLimits(ctx, orgID); err != nil || found {
		t.Fatalf("empty: found=%v err=%v", found, err)
	}
	mem, rows := int64(4<<30), int64(0)
	actor := quota.Actor{UserID: ownerID, Email: "qlimits@example.com", IP: "192.0.2.1"}
	if err := st.PutOrgQueryLimits(ctx, orgID, quota.OrgQueryLimits{MaxMemoryUsage: &mem, MaxRowsToRead: &rows}, actor); err != nil {
		t.Fatal(err)
	}
	got, found, err := st.GetOrgQueryLimits(ctx, orgID)
	if err != nil || !found || *got.MaxMemoryUsage != mem || *got.MaxRowsToRead != 0 || got.MaxBytesToRead != nil || got.UpdatedByEmail != "qlimits@example.com" {
		t.Fatalf("get: %+v found=%v err=%v", got, found, err)
	}
	all, err := st.ListOrgQueryLimits(ctx)
	if err != nil || all["qlimits"].MaxMemoryUsage == nil {
		t.Fatalf("list: %v %v", all, err)
	}

	// The resolver sees the stored layer.
	r := &quota.QueryLimitsResolver{Query: config.Query{Defaults: config.QueryLimits{MaxMemoryUsage: 1, MaxRowsToRead: 2, MaxBytesToRead: 3}}, Store: st}
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if l := r.Limits("qlimits"); l.MaxMemoryUsage != mem || l.MaxRowsToRead != 0 || l.MaxBytesToRead != 3 {
		t.Errorf("resolver: %+v", l)
	}

	if err := st.PutOrgQueryLimits(ctx, orgID, quota.OrgQueryLimits{}, actor); err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.GetOrgQueryLimits(ctx, orgID); err != nil || found {
		t.Fatalf("after clear: found=%v err=%v", found, err)
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id = $1::uuid AND action IN ('query_limits.update', 'query_limits.delete')`, orgID).Scan(&audits); err != nil || audits != 2 {
		t.Errorf("audit events: %d %v", audits, err)
	}
	if err := st.PutOrgQueryLimits(ctx, "00000000-0000-0000-0000-000000000000", quota.OrgQueryLimits{MaxRowsToRead: &rows}, actor); err != quota.ErrOrgNotFound {
		t.Errorf("unknown org: %v", err)
	}
}
