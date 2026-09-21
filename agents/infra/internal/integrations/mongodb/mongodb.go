// Package mongodb implements the MongoDB integration: `serverStatus`, `listDatabases`, `dbStats` and, on a
// replica set member, `replSetGetStatus`, emitting the metrics of the OpenTelemetry Collector
// mongodbreceiver (semantic-conventions §6.15).
//
// The official driver is used rather than a hand-written client, unlike the small text formats this agent
// parses itself (D-137): MongoDB's wire protocol needs BSON and SCRAM, and the repository already carries
// real drivers for every other database integration (pgx, go-sql-driver/mysql, go-mssqldb). A hand-written
// SCRAM implementation is the wrong place to save a dependency.
package mongodb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// DefaultPort is the mongod/mongos port.
const DefaultPort = 27017

// MaxDatabases bounds the databases whose dbStats are collected: a database is a set of metric series, and
// an installation that creates one per tenant has thousands.
const MaxDatabases = 32

// defaultAuthSource is where MongoDB keeps users unless the deployment says otherwise.
const defaultAuthSource = "admin"

// Integration is the MongoDB integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationMongoDB }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: DefaultPort}
}

// Hint implements integrations.Integration.
func (Integration) Hint(*integrations.Instance) string {
	return `# MongoDB needs a read-only monitoring user; the agent never creates users itself. In mongosh:
#   use admin
#   db.createUser({user: "openlog", pwd: "<password>", roles: [
#     {role: "clusterMonitor", db: "admin"}, {role: "read", db: "local"}]})
# Then enter the user and password in openlog (host → Integrations → MongoDB), or in config.yaml:
integrations:
  mongodb:
    username: openlog
    password: env:OPENLOG_MONGODB_PASSWORD   # or file:/etc/openlog-infra-agent/mongodb.password
    # database: admin                        # the authentication source, when users live elsewhere
    # endpoint: 127.0.0.1:27017`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	if ep.Network != "tcp" {
		return nil, fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
	return &collector{inst: inst, ep: ep}, nil
}

type collector struct {
	inst   *integrations.Instance
	ep     integrations.Endpoint
	client *mongo.Client
}

func (c *collector) Close() {
	if c.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.client.Disconnect(ctx)
		c.client = nil
	}
}

// connect opens the client on first use. The connection is direct: the agent runs next to this mongod and
// is asking about *this* process, so following the topology to a primary elsewhere would report another
// machine's numbers under this host's name.
func (c *collector) connect(ctx context.Context) error {
	if c.client != nil {
		return nil
	}
	password, err := c.inst.Password()
	if err != nil {
		return integrations.NeedsConfiguration("password: "+err.Error(), false)
	}
	opts := options.Client().
		SetHosts([]string{c.ep.Address}).
		SetDirect(true).
		SetAppName("openlog-infra-agent").
		SetConnectTimeout(c.inst.Timeout).
		SetServerSelectionTimeout(c.inst.Timeout).
		SetTimeout(c.inst.Timeout).
		// One connection is enough for a collection every 30 seconds, and a monitoring agent must not be
		// the reason a server runs out of them.
		SetMaxPoolSize(2)
	if u := c.inst.Settings.Username; u != "" || password != "" {
		source := strings.TrimSpace(c.inst.Settings.Database)
		if source == "" {
			source = defaultAuthSource
		}
		opts.SetAuth(options.Credential{Username: u, Password: password, AuthSource: source})
	}
	if t := c.inst.Settings.TLS; t != nil && t.Enabled {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: t.InsecureSkipVerify, ServerName: t.ServerName} //nolint:gosec // the operator's own setting
		if t.CAFile != "" {
			pem, err := os.ReadFile(t.CAFile)
			if err != nil {
				return integrations.NeedsConfiguration("tls.ca_file: "+err.Error(), true)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return integrations.NeedsConfiguration("tls.ca_file "+t.CAFile+": no PEM certificate", true)
			}
			cfg.RootCAs = pool
		}
		opts.SetTLSConfig(cfg)
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	if err := client.Ping(ctx, readpref.PrimaryPreferred()); err != nil {
		_ = client.Disconnect(context.Background())
		return classify(err)
	}
	c.client = client
	return nil
}

// classify maps a driver error to the status the framework expects.
func classify(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Authentication failed"), strings.Contains(msg, "auth error"),
		strings.Contains(msg, "not authorized"), strings.Contains(msg, "requires authentication"):
		return integrations.NeedsConfiguration("authentication failed: check the user, password and authentication database", false)
	case strings.Contains(msg, "server selection error"), strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"), strings.Contains(msg, "i/o timeout"):
		return fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	return err
}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	if err := c.connect(ctx); err != nil {
		return err
	}
	admin := c.client.Database(defaultAuthSource)

	var status bson.M
	if err := admin.RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&status); err != nil {
		c.Close() // a failed command may mean the connection is gone; the next collection reconnects
		return classify(err)
	}
	RecordServerStatus(b, status)

	var partial []string
	dbs, err := c.databases(ctx)
	if err != nil {
		partial = append(partial, "listDatabases: "+err.Error())
	} else {
		RecordDatabases(b, dbs)
		for _, name := range c.wanted(dbs) {
			var stats bson.M
			if err := c.client.Database(name).RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&stats); err != nil {
				partial = append(partial, "dbStats "+name+": "+err.Error())
				continue
			}
			RecordDBStats(b, name, stats)
		}
	}

	// replSetGetStatus fails with "not running with --replSet" on a standalone server, which is not a
	// problem to report: most MongoDB servers people monitor are standalone.
	var repl bson.M
	if err := admin.RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Decode(&repl); err == nil {
		RecordReplicaSet(b, repl)
	}

	if len(partial) > 0 {
		return integrations.Partial(fmt.Errorf("%s", strings.Join(partial, "; ")))
	}
	return nil
}

// database is one entry of listDatabases.
type database struct {
	Name  string
	Size  int64
	Empty bool
}

func (c *collector) databases(ctx context.Context) ([]database, error) {
	var res bson.M
	if err := c.client.Database(defaultAuthSource).RunCommand(ctx, bson.D{{Key: "listDatabases", Value: 1}}).Decode(&res); err != nil {
		return nil, err
	}
	list, _ := res["databases"].(bson.A)
	out := make([]database, 0, len(list))
	for _, item := range list {
		doc, ok := item.(bson.M)
		if !ok {
			continue
		}
		name, _ := doc["name"].(string)
		if name == "" {
			continue
		}
		empty, _ := doc["empty"].(bool)
		out = append(out, database{Name: name, Size: int64(numberOf(doc["sizeOnDisk"])), Empty: empty})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// wanted returns the databases whose dbStats are collected: the configured allow list, or every database
// except the ones excluded, bounded by MaxDatabases.
func (c *collector) wanted(dbs []database) []string {
	allow := map[string]bool{}
	for _, name := range c.inst.Settings.Databases {
		allow[name] = true
	}
	exclude := map[string]bool{}
	for _, name := range c.inst.Settings.ExcludeDatabases {
		exclude[name] = true
	}
	out := make([]string, 0, min(len(dbs), MaxDatabases))
	for _, db := range dbs {
		if len(out) >= MaxDatabases {
			break
		}
		switch {
		case len(allow) > 0 && !allow[db.Name]:
			continue
		case exclude[db.Name]:
			continue
		}
		out = append(out, db.Name)
	}
	return out
}

// RecordServerStatus emits the server-wide metrics of a serverStatus document.
func RecordServerStatus(b *integrations.Batch, st bson.M) {
	s := b.Resource()
	if v, ok := st["version"].(string); ok && v != "" {
		b.SetResourceAttr(otlputil.Str("db.version", v))
	}
	if v, ok := st["process"].(string); ok && v != "" {
		// mongod or mongos: a router reports no storage numbers, and saying which it is explains why.
		b.SetResourceAttr(otlputil.Str("mongodb.process", v))
	}
	if uptime, ok := number(st, "uptime"); ok {
		s.SumInt("mongodb.uptime", "s", true, int64(uptime))
		b.SetStartTime(b.Now().Add(-time.Duration(uptime) * time.Second))
	}

	for _, kv := range []struct{ field, state string }{
		{"current", "current"}, {"available", "available"}, {"active", "active"},
	} {
		if v, ok := number(st, "connections."+kv.field); ok {
			s.SumInt("mongodb.connection.count", "{connections}", false, int64(v), otlputil.Str("type", kv.state))
		}
	}
	for _, kv := range []struct{ path, typ string }{{"mem.resident", "resident"}, {"mem.virtual", "virtual"}} {
		if v, ok := number(st, kv.path); ok {
			// mem.* is reported in mebibytes; the metric is bytes, like every other size in openlog.
			s.SumInt("mongodb.memory.usage", "By", false, int64(v)*1024*1024, otlputil.Str("type", kv.typ))
		}
	}
	for _, op := range []string{"insert", "query", "update", "delete", "getmore", "command"} {
		if v, ok := number(st, "opcounters."+op); ok {
			s.SumInt("mongodb.operation.count", "{operations}", true, int64(v), otlputil.Str("operation", op))
		}
	}
	for _, op := range []string{"insert", "query", "update", "delete"} {
		if v, ok := number(st, "metrics.document."+documentField(op)); ok {
			s.SumInt("mongodb.document.operation.count", "{documents}", true, int64(v), otlputil.Str("operation", op))
		}
	}
	for _, op := range []string{"reads", "writes", "commands"} {
		if v, ok := number(st, "opLatencies."+op+".latency"); ok {
			s.SumInt("mongodb.operation.latency.time", "us", true, int64(v), otlputil.Str("operation", latencyOperation(op)))
		}
	}
	for _, kv := range []struct{ path, name, unit string }{
		{"network.bytesIn", "mongodb.network.io.receive", "By"},
		{"network.bytesOut", "mongodb.network.io.transmit", "By"},
		{"network.numRequests", "mongodb.network.request.count", "{requests}"},
	} {
		if v, ok := number(st, kv.path); ok {
			s.SumInt(kv.name, kv.unit, true, int64(v))
		}
	}
	if v, ok := number(st, "globalLock.totalTime"); ok {
		s.SumInt("mongodb.global_lock.time", "ms", true, int64(v)/1000)
	}
	for _, kv := range []struct{ path, typ string }{
		{"metrics.cursor.open.total", "open"}, {"metrics.cursor.open.noTimeout", "no_timeout"},
	} {
		if v, ok := number(st, kv.path); ok {
			s.SumInt("mongodb.cursor.count", "{cursors}", false, int64(v), otlputil.Str("type", kv.typ))
		}
	}
	if v, ok := number(st, "metrics.cursor.timedOut"); ok {
		s.SumInt("mongodb.cursor.timeout.count", "{cursors}", true, int64(v))
	}
	// WiredTiger's cache: the hit share is what tells a working set that fits memory from one that does not.
	if v, ok := number(st, "wiredTiger.cache.pages read into cache"); ok {
		s.SumInt("mongodb.cache.operations", "{operations}", true, int64(v), otlputil.Str("type", "miss"))
	}
	if req, ok := number(st, "wiredTiger.cache.pages requested from the cache"); ok {
		miss, _ := number(st, "wiredTiger.cache.pages read into cache")
		if hits := req - miss; hits >= 0 {
			s.SumInt("mongodb.cache.operations", "{operations}", true, int64(hits), otlputil.Str("type", "hit"))
		}
	}
	if v, ok := number(st, "logicalSessionRecordCache.activeSessionsCount"); ok {
		s.SumInt("mongodb.session.count", "{sessions}", false, int64(v))
	}
	if v, ok := number(st, "asserts.regular"); ok {
		s.SumInt("mongodb.asserts", "{asserts}", true, int64(v), otlputil.Str("type", "regular"))
	}
	if v, ok := number(st, "asserts.warning"); ok {
		s.SumInt("mongodb.asserts", "{asserts}", true, int64(v), otlputil.Str("type", "warning"))
	}
}

// documentField maps an operation to the serverStatus field that counts the documents it touched.
func documentField(op string) string {
	switch op {
	case "insert":
		return "inserted"
	case "query":
		return "returned"
	case "update":
		return "updated"
	}
	return "deleted"
}

// latencyOperation maps the opLatencies key to the receiver's attribute value.
func latencyOperation(key string) string {
	switch key {
	case "reads":
		return "read"
	case "writes":
		return "write"
	}
	return "command"
}

// RecordDatabases emits the counts and sizes of listDatabases.
func RecordDatabases(b *integrations.Batch, dbs []database) {
	s := b.Resource()
	s.SumInt("mongodb.database.count", "{databases}", false, int64(len(dbs)))
}

// RecordDBStats emits the per-database metrics of a dbStats document.
func RecordDBStats(b *integrations.Batch, name string, st bson.M) {
	s := b.Resource(otlputil.Str("db.namespace", name))
	intField := func(path, metric, unit string, monotonic bool, attrs ...*commonpb.KeyValue) {
		if v, ok := number(st, path); ok {
			s.SumInt(metric, unit, monotonic, int64(v), attrs...)
		}
	}
	intField("collections", "mongodb.collection.count", "{collections}", false)
	intField("dataSize", "mongodb.data.size", "By", false)
	intField("storageSize", "mongodb.storage.size", "By", false)
	intField("indexSize", "mongodb.index.size", "By", false)
	intField("indexes", "mongodb.index.count", "{indexes}", false)
	intField("objects", "mongodb.object.count", "{objects}", false)
	intField("views", "mongodb.view.count", "{views}", false)
}

// RecordReplicaSet emits what replSetGetStatus says about this member: its state and how far behind the
// primary it is, which is the number that matters on a secondary.
func RecordReplicaSet(b *integrations.Batch, st bson.M) {
	setName, _ := st["set"].(string)
	members, _ := st["members"].(bson.A)
	var self, primary bson.M
	for _, item := range members {
		m, ok := item.(bson.M)
		if !ok {
			continue
		}
		if isSelf, _ := m["self"].(bool); isSelf {
			self = m
		}
		if state, _ := number(m, "state"); state == 1 {
			primary = m
		}
	}
	if self == nil {
		return
	}
	attrs := []*commonpb.KeyValue{}
	if setName != "" {
		b.SetResourceAttr(otlputil.Str("mongodb.replica_set.name", setName))
	}
	if name, _ := self["stateStr"].(string); name != "" {
		attrs = append(attrs, otlputil.Str("state", strings.ToLower(name)))
	}
	s := b.Resource()
	s.GaugeInt("mongodb.replica_set.member", "{status}", 1, attrs...)
	s.SumInt("mongodb.replica_set.members", "{members}", false, int64(len(members)))
	// The lag is the difference between the primary's last applied operation and this member's; without a
	// primary in the answer there is nothing to be behind, and reporting 0 would be a lie.
	if primary != nil && self != nil {
		if p, okP := optimeDate(primary); okP {
			if s2, okS := optimeDate(self); okS {
				s.GaugeInt("mongodb.replica_set.lag", "s", int64(p.Sub(s2).Seconds()))
			}
		}
	}
}

func optimeDate(m bson.M) (time.Time, bool) {
	switch v := m["optimeDate"].(type) {
	case time.Time:
		return v, true
	case bson.DateTime:
		return v.Time(), true
	}
	return time.Time{}, false
}

// number reads a numeric field at a dotted path ("connections.current"). BSON numbers arrive as int32,
// int64 or double depending on the field and the server version, so every read goes through here.
func number(doc bson.M, path string) (float64, bool) {
	cur := any(doc)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(bson.M)
		if !ok {
			return 0, false
		}
		cur, ok = m[seg]
		if !ok {
			return 0, false
		}
	}
	return numberOf(cur), isNumber(cur)
}

func numberOf(v any) float64 {
	switch n := v.(type) {
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	case bson.Decimal128:
		if f, err := parseDecimal(n); err == nil {
			return f
		}
	}
	return 0
}

func isNumber(v any) bool {
	switch v.(type) {
	case int32, int64, float64, bson.Decimal128:
		return true
	}
	return false
}

func parseDecimal(d bson.Decimal128) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(d.String(), "%g", &f)
	return f, err
}
