package processor

import (
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/otlputil"
)

// Database query performance monitoring (schema 0096_db_monitoring, docs/contracts/db-monitoring.md, D-138): the
// infra agent's database integrations send statement statistics, session samples and execution plans as OTLP log
// records; AddLogs routes them here instead of into logs.

// Event names of the database integrations.
const (
	EventDBQueryStats    = "openlog.db.query_stats"
	EventDBSessionSample = "openlog.db.session_sample"
	EventDBQueryPlan     = "openlog.db.query_plan"
)

// Tables of 0096_db_monitoring.
const (
	TableDBQueryStats     = "db_query_stats"
	TableDBSessionSamples = "db_session_samples"
	TableDBQueryPlans     = "db_query_plans"
)

// Record attributes (db-monitoring.md §2). db.system.name, db.namespace and db.query.text are the OTel database
// semantic conventions; the rest are openlog's.
const (
	attrDBSystemName    = "db.system.name"
	attrDBSystem        = "db.system" // older OTel name, accepted
	attrDBNamespace     = "db.namespace"
	attrDBQueryText     = "db.query.text"
	attrDBUser          = "openlog.db.user"
	attrDBQueryID       = "openlog.db.query.id"
	attrDBInterval      = "openlog.db.interval_seconds"
	attrDBCalls         = "openlog.db.calls"
	attrDBTotalTimeMs   = "openlog.db.total_time_ms"
	attrDBRows          = "openlog.db.rows"
	attrDBRowsExamined  = "openlog.db.rows_examined"
	attrDBErrors        = "openlog.db.errors"
	attrDBNoIndexUsed   = "openlog.db.no_index_used"
	attrDBBlocksHit     = "openlog.db.blocks_hit"
	attrDBBlocksRead    = "openlog.db.blocks_read"
	attrDBSessionID     = "openlog.db.session.id"
	attrDBSessionState  = "openlog.db.session.state"
	attrDBWaitType      = "openlog.db.wait.type"
	attrDBWaitEvent     = "openlog.db.wait.event"
	attrDBDurationMs    = "openlog.db.duration_ms"
	attrDBBlockingIDs   = "openlog.db.blocking_session_ids"
	attrDBApplication   = "openlog.db.application"
	attrClientAddress   = "client.address"
	attrDBPlanHash      = "openlog.db.plan.hash"
	attrDBPlanFormat    = "openlog.db.plan.format"
	attrDBPlanCost      = "openlog.db.plan.cost"
	attrServiceInstance = "service.instance.id"
	attrServerAddress   = "server.address"
	attrServerPort      = "server.port"
)

// Bounds of what one record may store. Plans beyond MaxDBPlanBytes are dropped rather than truncated: half a JSON
// or XML document cannot be rendered.
const (
	MaxDBPlanBytes       = 512 << 10
	maxDBShortValue      = 256
	maxDBBlockingIDs     = 64
	maxDBIntervalSeconds = 24 * 3600
)

// DBQueryStatRow is one statement's activity over one collection interval (deltas).
type DBQueryStatRow struct {
	TenantID        string
	Timestamp       time.Time
	IntervalSeconds uint32
	HostID          string
	HostName        string
	DBSystem        string
	Instance        string
	ServerAddress   string
	ServerPort      uint16
	DBName          string
	DBUser          string
	QueryID         string
	QueryText       string
	Fingerprint     uint64
	Calls           uint64
	TotalTimeMs     float64
	Rows            uint64
	RowsExamined    uint64
	Errors          uint64
	NoIndexUsed     uint64
	BlocksHit       uint64
	BlocksRead      uint64
}

// Values implements row.
func (r *DBQueryStatRow) Values() []any {
	return []any{r.TenantID, r.Timestamp, r.IntervalSeconds, r.HostID, r.HostName, r.DBSystem, r.Instance, r.ServerAddress,
		r.ServerPort, r.DBName, r.DBUser, r.QueryID, r.QueryText, r.Fingerprint, r.Calls, r.TotalTimeMs, r.Rows, r.RowsExamined,
		r.Errors, r.NoIndexUsed, r.BlocksHit, r.BlocksRead}
}

// DBSessionSampleRow is one non-idle session at one sampling instant.
type DBSessionSampleRow struct {
	TenantID           string
	Timestamp          time.Time
	HostID             string
	HostName           string
	DBSystem           string
	Instance           string
	DBName             string
	DBUser             string
	SessionID          string
	State              string
	WaitEventType      string
	WaitEvent          string
	QueryID            string
	QueryText          string
	Fingerprint        uint64
	DurationMs         float64
	BlockingSessionIDs []string
	Application        string
	ClientAddress      string
}

// Values implements row.
func (r *DBSessionSampleRow) Values() []any {
	return []any{r.TenantID, r.Timestamp, r.HostID, r.HostName, r.DBSystem, r.Instance, r.DBName, r.DBUser, r.SessionID,
		r.State, r.WaitEventType, r.WaitEvent, r.QueryID, r.QueryText, r.Fingerprint, r.DurationMs, r.BlockingSessionIDs,
		r.Application, r.ClientAddress}
}

// DBQueryPlanRow is one capture of a statement's execution plan.
type DBQueryPlanRow struct {
	TenantID    string
	CapturedAt  time.Time
	Instance    string
	Fingerprint uint64
	PlanHash    string
	DBSystem    string
	HostID      string
	DBName      string
	QueryText   string
	PlanFormat  string
	Plan        string
	TotalCost   float64
}

// Values implements row.
func (r *DBQueryPlanRow) Values() []any {
	return []any{r.TenantID, r.CapturedAt, r.Instance, r.Fingerprint, r.PlanHash, r.DBSystem, r.HostID, r.DBName, r.QueryText,
		r.PlanFormat, r.Plan, r.TotalCost}
}

// DBFingerprint is the compact key of a normalized statement (FNV-1a 64). The API and the UI address a statement
// by it; correlation with APM compares the normalized text itself.
func DBFingerprint(normalized string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(normalized))
	return h.Sum64()
}

// NormalizeDBQuery is the one canonical form of a statement: apm.NormalizeStatement, which APM applies to
// db.statement of client spans. Agents only redact literals, so their text normalizes to what APM stores.
func NormalizeDBQuery(system, text string) string { return apm.NormalizeStatement(system, text) }

// dbIdentity is what every database record shares: the instance and the statement.
type dbIdentity struct {
	system, instance, dbName, user, queryID, text string
	fingerprint                                   uint64
}

func (r *Rows) dbIdentity(ri resourceInfo, attrs []*commonpb.KeyValue, needText bool) (dbIdentity, bool) {
	id := dbIdentity{
		system:   clip(firstNonEmpty(otlputil.AttrString(attrs, attrDBSystemName), otlputil.AttrString(attrs, attrDBSystem)), maxDBShortValue),
		instance: clip(ri.attrs[attrServiceInstance], 512),
		dbName:   clip(otlputil.AttrString(attrs, attrDBNamespace), maxDBShortValue),
		user:     clip(otlputil.AttrString(attrs, attrDBUser), maxDBShortValue),
		queryID:  clip(otlputil.AttrString(attrs, attrDBQueryID), maxDBShortValue),
	}
	if id.system == "" || id.instance == "" {
		return id, false
	}
	id.text = NormalizeDBQuery(id.system, otlputil.AttrString(attrs, attrDBQueryText))
	if id.text != "" {
		id.fingerprint = DBFingerprint(id.text)
	} else if needText {
		return id, false
	}
	return id, true
}

func (r *Rows) addDBQueryStats(tenant string, ri resourceInfo, ts time.Time, attrs []*commonpb.KeyValue) {
	id, ok := r.dbIdentity(ri, attrs, true)
	if !ok {
		r.Dropped["invalid_db_query_stats"]++
		return
	}
	iv := attrUint(attrs, attrDBInterval)
	if iv == 0 || iv > maxDBIntervalSeconds {
		r.Dropped["invalid_db_query_stats"]++
		return
	}
	var port uint64
	if p, err := strconv.ParseUint(ri.attrs[attrServerPort], 10, 16); err == nil {
		port = p
	}
	r.DBQueryStats = append(r.DBQueryStats, DBQueryStatRow{
		TenantID: tenant, Timestamp: ts, IntervalSeconds: uint32(iv), HostID: ri.hostID, HostName: ri.hostName,
		DBSystem: id.system, Instance: id.instance, ServerAddress: clip(ri.attrs[attrServerAddress], maxDBShortValue),
		ServerPort: uint16(port), DBName: id.dbName, DBUser: id.user, QueryID: id.queryID, QueryText: id.text,
		Fingerprint: id.fingerprint,
		Calls:       attrUint(attrs, attrDBCalls), TotalTimeMs: attrNonNegFloat(attrs, attrDBTotalTimeMs),
		Rows: attrUint(attrs, attrDBRows), RowsExamined: attrUint(attrs, attrDBRowsExamined), Errors: attrUint(attrs, attrDBErrors),
		NoIndexUsed: attrUint(attrs, attrDBNoIndexUsed), BlocksHit: attrUint(attrs, attrDBBlocksHit), BlocksRead: attrUint(attrs, attrDBBlocksRead),
	})
}

func (r *Rows) addDBSessionSample(tenant string, ri resourceInfo, ts time.Time, attrs []*commonpb.KeyValue) {
	id, ok := r.dbIdentity(ri, attrs, false)
	sid := clip(otlputil.AttrString(attrs, attrDBSessionID), maxDBShortValue)
	if !ok || sid == "" {
		r.Dropped["invalid_db_session_sample"]++
		return
	}
	var blocking []string
	if v := otlputil.FindAttr(attrs, attrDBBlockingIDs); v != nil {
		for _, e := range v.GetArrayValue().GetValues() {
			if len(blocking) >= maxDBBlockingIDs {
				break
			}
			if s := clip(otlputil.AnyValueString(e), maxDBShortValue); s != "" {
				blocking = append(blocking, s)
			}
		}
	}
	if blocking == nil {
		blocking = []string{}
	}
	r.DBSessionSamples = append(r.DBSessionSamples, DBSessionSampleRow{
		TenantID: tenant, Timestamp: ts, HostID: ri.hostID, HostName: ri.hostName, DBSystem: id.system, Instance: id.instance,
		DBName: id.dbName, DBUser: id.user, SessionID: sid,
		State:         clip(otlputil.AttrString(attrs, attrDBSessionState), maxDBShortValue),
		WaitEventType: clip(otlputil.AttrString(attrs, attrDBWaitType), maxDBShortValue),
		WaitEvent:     clip(otlputil.AttrString(attrs, attrDBWaitEvent), maxDBShortValue),
		QueryID:       id.queryID, QueryText: id.text, Fingerprint: id.fingerprint,
		DurationMs:         attrNonNegFloat(attrs, attrDBDurationMs),
		BlockingSessionIDs: blocking,
		Application:        clip(otlputil.AttrString(attrs, attrDBApplication), maxDBShortValue),
		ClientAddress:      clip(otlputil.AttrString(attrs, attrClientAddress), maxDBShortValue),
	})
}

func (r *Rows) addDBQueryPlan(tenant string, ri resourceInfo, ts time.Time, attrs []*commonpb.KeyValue, body string) {
	id, ok := r.dbIdentity(ri, attrs, true)
	hash := clip(otlputil.AttrString(attrs, attrDBPlanHash), 128)
	format := otlputil.AttrString(attrs, attrDBPlanFormat)
	if !ok || hash == "" || body == "" || (format != "json" && format != "xml") {
		r.Dropped["invalid_db_query_plan"]++
		return
	}
	if len(body) > MaxDBPlanBytes {
		r.Dropped["db_query_plan_too_large"]++
		return
	}
	r.DBQueryPlans = append(r.DBQueryPlans, DBQueryPlanRow{
		TenantID: tenant, Instance: id.instance, Fingerprint: id.fingerprint, PlanHash: hash, DBSystem: id.system,
		HostID: ri.hostID, DBName: id.dbName, QueryText: id.text, PlanFormat: format, Plan: body,
		TotalCost: attrNonNegFloat(attrs, attrDBPlanCost), CapturedAt: ts,
	})
}

// attrUint reads a non-negative integer attribute (int, double or decimal string); anything else is 0.
func attrUint(attrs []*commonpb.KeyValue, key string) uint64 {
	v := otlputil.FindAttr(attrs, key)
	if v == nil {
		return 0
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_IntValue:
		if x.IntValue > 0 {
			return uint64(x.IntValue)
		}
	case *commonpb.AnyValue_DoubleValue:
		if x.DoubleValue > 0 && x.DoubleValue < math.MaxUint64 {
			return uint64(x.DoubleValue)
		}
	case *commonpb.AnyValue_StringValue:
		if n, err := strconv.ParseUint(x.StringValue, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// attrNonNegFloat reads a finite, non-negative number attribute; anything else is 0.
func attrNonNegFloat(attrs []*commonpb.KeyValue, key string) float64 {
	v := otlputil.FindAttr(attrs, key)
	if v == nil {
		return 0
	}
	var f float64
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_DoubleValue:
		f = x.DoubleValue
	case *commonpb.AnyValue_IntValue:
		f = float64(x.IntValue)
	case *commonpb.AnyValue_StringValue:
		f, _ = strconv.ParseFloat(x.StringValue, 64)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return f
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}

// clip bounds a value on a UTF-8 boundary.
func clip(s string, n int) string { return apm.Truncate(s, n) }
