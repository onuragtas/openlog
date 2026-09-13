package apm

import (
	"testing"
	"time"
)

func TestIsConnectionSpan(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]string
		want  bool
	}{
		{"sql.connector.connect", map[string]string{"db.system": "postgresql"}, true},
		{"sql.conn.reset_session", map[string]string{"db.system": "postgresql"}, true},
		{"sql.conn.ping", map[string]string{"db.system": "mysql"}, true},
		{"db.connect", map[string]string{"db.system.name": "postgresql"}, true},
		{"pg.connect", map[string]string{"db.system": "postgresql"}, true},
		{"pg-pool.connect", map[string]string{"db.system": "postgresql"}, true},
		{"mysql.connection", map[string]string{"db.system": "mysql"}, true},
		{"anything", map[string]string{"db.system": "postgresql", "db.operation.name": "CONNECT"}, true},
		// Statements are queries, whatever the span is called.
		{"sql.conn.query", map[string]string{"db.system": "postgresql", "db.statement": "SELECT 1"}, false},
		{"sql.connector.connect", map[string]string{"db.system": "postgresql", "db.query.text": "SELECT 1"}, false},
		// Queries and transaction control without statement text stay DB calls.
		{"SELECT orders", map[string]string{"db.system": "postgresql"}, false},
		{"sql.conn.begin_tx", map[string]string{"db.system": "postgresql"}, false},
		{"GET", map[string]string{"db.system": "redis"}, false},
		{"connections.list", map[string]string{"db.system": "mongodb"}, false},
	}
	for _, c := range cases {
		if got := IsConnectionSpan(c.name, c.attrs); got != c.want {
			t.Errorf("IsConnectionSpan(%q, %v) = %v, want %v", c.name, c.attrs, got, c.want)
		}
	}

	// Derive: no DB columns (not a database query), the service map edge stays.
	d := Derive(&Input{Resource: map[string]string{"service.name": "orders"}, Kind: KindClient, Name: "sql.connector.connect",
		Attributes: map[string]string{"db.system": "postgresql", "db.name": "orders"}})
	if d.DBSystem != "" || d.DBStatementNormalized != "" || d.DBOperation != "" || d.PeerType != PeerDB || d.PeerName != "postgresql/orders" {
		t.Errorf("connection span derived %+v", d)
	}
	d = Derive(&Input{Resource: map[string]string{"service.name": "orders"}, Kind: KindClient, Name: "sql.conn.query",
		Attributes: map[string]string{"db.system": "postgresql", "db.statement": "SELECT * FROM orders WHERE id = 1"}})
	if d.DBSystem != "postgresql" || d.DBStatementNormalized != "SELECT * FROM orders WHERE id = ?" {
		t.Errorf("query span derived %+v", d)
	}
}

func TestCatchUpDue(t *testing.T) {
	at := 3 * time.Hour
	day := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	if _, _, ok := CatchUpDue(day.Add(2*time.Hour), at, time.Time{}); ok {
		t.Error("due before the configured time")
	}
	from, to, ok := CatchUpDue(day.Add(3*time.Hour+time.Minute), at, time.Time{})
	if !ok || !from.Equal(day.Add(-24*time.Hour)) || !to.Equal(day) {
		t.Errorf("due: %s %s %v", from, to, ok)
	}
	if _, _, ok := CatchUpDue(day.Add(4*time.Hour), at, day.Add(-24*time.Hour)); ok {
		t.Error("due again after the day was caught up")
	}
	if _, _, ok := CatchUpDue(day.Add(9*time.Hour+time.Minute), at, time.Time{}); ok {
		t.Error("due after the catch-up window")
	}
}
