package api

import (
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/rum"
)

// The funnel is the first query in the codebase with a sub-select over the raw spans, and every RUM
// attribute key it names starts with "openlog." — the token the tenant guard refuses anywhere in a
// fragment. A handler test would not catch that, because it runs against an empty result set.
func TestRUMFunnelQueryBuilds(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	q := rumFunnelSelect(sc, rumFilter{app: "shop-web"}, []string{"viewed_cart", "checkout_started"}, 30*time.Minute, now.Add(-time.Hour), now)
	sql, params, err := q.Build()
	if err != nil {
		t.Fatalf("funnel query does not build: %v", err)
	}
	if params["a_custom"] != rum.AttrCustomName || params["a_session"] != rum.AttrSessionID {
		t.Errorf("attribute keys are not bound as parameters: %v", params)
	}
	if params["s0"] != "viewed_cart" || params["s1"] != "checkout_started" {
		t.Errorf("steps are not bound as parameters: %v", params)
	}
	if strings.Contains(sql, rum.AttrCustomName) || strings.Contains(sql, "viewed_cart") {
		t.Errorf("a key or a step is embedded in the statement: %s", sql)
	}
	// Ordered, not five independent counts: a session that checked out before seeing the cart has not
	// completed the funnel, and only windowFunnel knows that.
	if !strings.Contains(sql, "windowFunnel") {
		t.Errorf("the funnel is not ordered: %s", sql)
	}
	// The tenant predicate belongs on the innermost read of the table, and the sub-select is what carries
	// it: without this the outer query would be unscoped.
	if !strings.Contains(sql, "tenant_id = {tenant_id:String}") {
		t.Errorf("the sub-select lost the tenant predicate: %s", sql)
	}
}
