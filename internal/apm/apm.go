// Package apm derives application performance data from spans (docs/contracts/apm.md):
// transaction detection and naming, error classification and grouping, service map
// targets, database statement normalization and sampling weights (all pure functions
// used by the processor at insert time), the latency histogram math used by the API
// and alerting, the Apdex settings model and the per-shard edge-linking job.
package apm

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Span kinds and status codes as stored in ClickHouse (spans.kind, spans.status_code).
const (
	KindServer   = "server"
	KindClient   = "client"
	KindProducer = "producer"
	KindConsumer = "consumer"

	StatusError = "error"
)

// Transaction types (apm.md §2.1).
const (
	TxWeb       = "web"
	TxRPC       = "rpc"
	TxMessaging = "messaging"
	TxOther     = "other"
)

// Service map target types (apm.md §5).
const (
	PeerService   = "service"
	PeerDB        = "db"
	PeerMessaging = "messaging"
	PeerExternal  = "external"
)

// OTLP span flags (trace.proto SpanFlags).
const (
	flagHasIsRemote = 0x100
	flagIsRemote    = 0x200
)

// MaxTransactionName bounds transaction_name (bytes).
const MaxTransactionName = 256

// Input is one span as seen by the processor.
type Input struct {
	Resource      map[string]string
	Attributes    map[string]string
	Kind          string
	StatusCode    string
	StatusMessage string
	Name          string
	TraceState    string
	Flags         uint32
	ParentSpanID  string
	// ParentLocalSameService: the parent span is in the same export request and has the same service.name.
	ParentLocalSameService bool
	EventsName             []string
	EventsAttributes       []map[string]string
}

// Derived holds the APM columns of a span (apm.md §8).
type Derived struct {
	ServiceNamespace      string
	Environment           string
	IsEntry               bool
	TransactionType       string
	TransactionName       string
	IsError               bool
	HTTPStatusCode        uint16
	SampleWeight          float64
	PeerType              string
	PeerName              string
	DBSystem              string
	DBName                string
	DBOperation           string
	DBStatementNormalized string
	ErrorGroupID          uint64
	ErrorType             string
	ErrorMessage          string
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Environment returns deployment.environment.name, else deployment.environment.
func Environment(res map[string]string) string {
	return first(res["deployment.environment.name"], res["deployment.environment"])
}

// Derive computes the APM columns of a span. It depends only on its input, so the
// processor's conversion stays deterministic per Kafka record.
func Derive(in *Input) Derived {
	d := Derived{
		ServiceNamespace: in.Resource["service.namespace"],
		Environment:      Environment(in.Resource),
		SampleWeight:     SampleWeight(in.TraceState, in.Attributes, in.Resource),
	}
	d.HTTPStatusCode = httpStatus(in.Attributes)
	d.IsError = in.StatusCode == StatusError || (in.Kind == KindServer && d.HTTPStatusCode >= 500)
	service := in.Resource["service.name"]
	if service == "" {
		return d
	}
	d.IsEntry = IsEntry(in.Kind, in.ParentSpanID, in.Flags, in.ParentLocalSameService)
	if d.IsEntry {
		d.TransactionType, d.TransactionName = TransactionName(in.Name, in.Attributes)
	}
	if in.Kind == KindClient || in.Kind == KindProducer {
		d.PeerType, d.PeerName = Peer(in.Attributes)
	}
	if in.Kind == KindClient {
		if sys := first(in.Attributes["db.system.name"], in.Attributes["db.system"]); sys != "" && !IsConnectionSpan(in.Name, in.Attributes) {
			d.DBSystem = sys
			d.DBName = first(in.Attributes["db.namespace"], in.Attributes["db.name"])
			stmt := first(in.Attributes["db.query.text"], in.Attributes["db.statement"], in.Name)
			d.DBStatementNormalized = NormalizeStatement(sys, stmt)
			d.DBOperation = strings.ToUpper(first(in.Attributes["db.operation.name"], in.Attributes["db.operation"], firstWord(d.DBStatementNormalized)))
		}
	}
	if d.IsError {
		d.ErrorType, d.ErrorMessage, d.ErrorGroupID = ErrorGroup(service, d.ServiceNamespace, d.Environment, in, d.HTTPStatusCode)
	}
	return d
}

// connectionOps are the last words of span names / operations that manage connections rather than run queries.
var connectionOps = map[string]bool{"connect": true, "connection": true, "reconnect": true, "disconnect": true, "close": true,
	"ping": true, "reset_session": true, "resetsession": true, "reset": true, "acquire": true, "release": true, "handshake": true}

// IsConnectionSpan reports a DB client span that manages a connection (apm.md §7): it has no statement
// (db.query.text, db.statement) and its operation (db.operation.name, db.operation, else the span name) ends in a
// connection word, e.g. otelsql sql.connector.connect, sql.conn.reset_session, sql.conn.ping, db.connect, pg.connect.
// Such spans get no DB columns, so they are not listed as database queries.
func IsConnectionSpan(name string, attrs map[string]string) bool {
	if first(attrs["db.query.text"], attrs["db.statement"]) != "" {
		return false
	}
	op := strings.ToLower(strings.TrimSpace(first(attrs["db.operation.name"], attrs["db.operation"], name)))
	if op == "" {
		return false
	}
	if strings.Contains(op, "connector") {
		return true
	}
	last := op
	if i := strings.LastIndexAny(op, ". /:"); i >= 0 {
		last = op[i+1:]
	}
	return connectionOps[last]
}

// IsEntry implements apm.md §2.
func IsEntry(kind, parentSpanID string, flags uint32, parentLocalSameService bool) bool {
	if kind != KindServer && kind != KindConsumer {
		return false
	}
	if parentSpanID == "" {
		return true
	}
	if flags&flagHasIsRemote != 0 {
		return flags&flagIsRemote != 0
	}
	return !parentLocalSameService
}

func httpStatus(attrs map[string]string) uint16 {
	v := first(attrs["http.response.status_code"], attrs["http.status_code"])
	if v == "" {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}

var httpMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "DELETE": true, "CONNECT": true,
	"OPTIONS": true, "TRACE": true, "PATCH": true, "QUERY": true}

// TransactionName returns the transaction type and name of an entry span (apm.md §2.1).
func TransactionName(spanName string, attrs map[string]string) (string, string) {
	if method := first(attrs["http.request.method"], attrs["http.method"]); method != "" {
		m := strings.ToUpper(method)
		if !httpMethods[m] {
			m = "_OTHER"
		}
		route := attrs["http.route"]
		if route == "" {
			if p := first(attrs["url.path"], pathOf(attrs["http.target"]), pathOf(attrs["url.full"]), pathOf(attrs["http.url"])); p != "" {
				route = NormalizePath(p)
			}
		}
		if route == "" {
			return TxWeb, Truncate(spanName, MaxTransactionName)
		}
		return TxWeb, Truncate(m+" "+route, MaxTransactionName)
	}
	if attrs["rpc.system"] != "" {
		svc, method := attrs["rpc.service"], attrs["rpc.method"]
		if svc == "" && method == "" {
			return TxRPC, Truncate(spanName, MaxTransactionName)
		}
		return TxRPC, Truncate(first(svc, spanName)+"/"+first(method, spanName), MaxTransactionName)
	}
	if attrs["messaging.system"] != "" {
		op := first(attrs["messaging.operation.name"], attrs["messaging.operation"], "process")
		dest := first(attrs["messaging.destination.name"], attrs["messaging.destination"])
		if dest == "" {
			return TxMessaging, Truncate(spanName, MaxTransactionName)
		}
		return TxMessaging, Truncate(op+" "+normalizeSegment(dest), MaxTransactionName)
	}
	return TxOther, Truncate(spanName, MaxTransactionName)
}

// Peer returns the service map target of a client or producer span (apm.md §5).
func Peer(attrs map[string]string) (string, string) {
	if v := attrs["peer.service"]; v != "" {
		return PeerService, v
	}
	if sys := first(attrs["db.system.name"], attrs["db.system"]); sys != "" {
		if name := first(attrs["db.namespace"], attrs["db.name"]); name != "" {
			return PeerDB, sys + "/" + name
		}
		return PeerDB, sys
	}
	if ms := attrs["messaging.system"]; ms != "" {
		if dest := first(attrs["messaging.destination.name"], attrs["messaging.destination"]); dest != "" {
			return PeerMessaging, ms + "/" + normalizeSegment(dest)
		}
		return PeerMessaging, ms
	}
	host := first(attrs["server.address"], attrs["net.peer.name"])
	port := first(attrs["server.port"], attrs["net.peer.port"])
	if host == "" {
		if u, err := url.Parse(first(attrs["url.full"], attrs["http.url"])); err == nil && u.Host != "" {
			host, port = u.Hostname(), u.Port()
			if port == "" && u.Scheme == "https" {
				port = "443"
			}
		}
	}
	if host == "" {
		return "", ""
	}
	if port == "" || port == "80" || port == "443" {
		return PeerExternal, host
	}
	return PeerExternal, net.JoinHostPort(host, port)
}

func pathOf(v string) string {
	if v == "" {
		return ""
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil {
			return ""
		}
		return u.Path
	}
	if i := strings.IndexAny(v, "?#"); i >= 0 {
		v = v[:i]
	}
	return v
}

func firstWord(s string) string {
	s = strings.TrimLeft(s, " (")
	end := 0
	for end < len(s) {
		c := s[end]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			break
		}
		end++
	}
	return s[:end]
}

// Truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
