package apm

import (
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/orders/42/items/5f0e8c3a9b2d4e6f?x=1":       "/orders/{id}/items/{hex}",
		"/users/3fa85f64-5717-4562-b3fc-2c963f66afa6": "/users/{uuid}",
		"/u/john@example.com/profile":                 "/u/{email}/profile",
		"/t/eyJhbGciOiJIUzI1NiJ9abc":                  "/t/{token}",
		"/api/v2/health":                              "/api/v2/health",
		"/blob/abc12345":                              "/blob/{hex}",
		"/blob/deadbeef":                              "/blob/deadbeef",
		"":                                            "/",
		"//a//b/#frag":                                "/a/b",
		"a/b/c/d/e/f/g/h/i/j":                         "/a/b/c/d/e/f/g/h/…",
		"/orders/-7":                                  "/orders/{id}",
		"/files/report-2026-final":                    "/files/report-2026-final",
	}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTransactionName(t *testing.T) {
	cases := []struct {
		name     string
		attrs    map[string]string
		typ, txn string
	}{
		{"x", map[string]string{"http.request.method": "get", "http.route": "/orders/{id}"}, TxWeb, "GET /orders/{id}"},
		{"x", map[string]string{"http.method": "POST", "http.target": "/orders/77?x=1"}, TxWeb, "POST /orders/{id}"},
		{"x", map[string]string{"http.request.method": "FOO", "url.path": "/a"}, TxWeb, "_OTHER /a"},
		{"x", map[string]string{"http.method": "GET", "http.url": "http://h:1/products/12"}, TxWeb, "GET /products/{id}"},
		{"GET", map[string]string{"http.method": "GET"}, TxWeb, "GET"},
		{"x", map[string]string{"rpc.system": "grpc", "rpc.service": "pkg.Orders", "rpc.method": "Get"}, TxRPC, "pkg.Orders/Get"},
		{"x", map[string]string{"messaging.system": "kafka", "messaging.operation": "receive", "messaging.destination.name": "orders"}, TxMessaging, "receive orders"},
		{"x", map[string]string{"messaging.system": "rabbitmq", "messaging.destination.name": "reply-3fa85f64-5717-4562-b3fc-2c963f66afa6"}, TxMessaging, "process {token}"},
		{"cron job", map[string]string{}, TxOther, "cron job"},
		{strings.Repeat("é", 200), map[string]string{}, TxOther, strings.Repeat("é", 128)},
	}
	for _, c := range cases {
		typ, txn := TransactionName(c.name, c.attrs)
		if typ != c.typ || txn != c.txn {
			t.Errorf("TransactionName(%q, %v) = %q %q, want %q %q", c.name, c.attrs, typ, txn, c.typ, c.txn)
		}
	}
}

func TestPeer(t *testing.T) {
	cases := []struct {
		attrs     map[string]string
		typ, name string
	}{
		{map[string]string{"peer.service": "billing", "server.address": "x"}, PeerService, "billing"},
		{map[string]string{"db.system": "postgresql", "db.name": "orders", "server.address": "db"}, PeerDB, "postgresql/orders"},
		{map[string]string{"db.system.name": "redis"}, PeerDB, "redis"},
		{map[string]string{"messaging.system": "kafka", "messaging.destination.name": "orders"}, PeerMessaging, "kafka/orders"},
		{map[string]string{"server.address": "orders", "server.port": "8080"}, PeerExternal, "orders:8080"},
		{map[string]string{"net.peer.name": "catalog", "net.peer.port": "80"}, PeerExternal, "catalog"},
		{map[string]string{"url.full": "https://api.stripe.com/v1/charges"}, PeerExternal, "api.stripe.com"},
		{map[string]string{"http.url": "http://10.0.0.1:9200/_bulk"}, PeerExternal, "10.0.0.1:9200"},
		{map[string]string{}, "", ""},
	}
	for _, c := range cases {
		if typ, name := Peer(c.attrs); typ != c.typ || name != c.name {
			t.Errorf("Peer(%v) = %q %q, want %q %q", c.attrs, typ, name, c.typ, c.name)
		}
	}
}

func TestIsEntry(t *testing.T) {
	cases := []struct {
		kind, parent string
		flags        uint32
		local, want  bool
	}{
		{KindServer, "", 0, false, true},
		{KindConsumer, "", 0, false, true},
		{KindServer, "p", 0, true, false},
		{KindServer, "p", 0, false, true},
		{KindServer, "p", flagHasIsRemote | flagIsRemote, true, true},
		{KindServer, "p", flagHasIsRemote, false, false},
		{KindClient, "", 0, false, false},
		{"internal", "", 0, false, false},
	}
	for _, c := range cases {
		if got := IsEntry(c.kind, c.parent, c.flags, c.local); got != c.want {
			t.Errorf("IsEntry(%+v) = %v", c, got)
		}
	}
}

func TestNormalizeStatement(t *testing.T) {
	cases := []struct{ system, in, want string }{
		{"postgresql", "SELECT id, status FROM orders WHERE customer_id = 42 AND status IN ('new', 'paid') ORDER BY created_at DESC LIMIT 10",
			"SELECT id, status FROM orders WHERE customer_id = ? AND status IN (?) ORDER BY created_at DESC LIMIT ?"},
		{"postgresql", "INSERT INTO orders (customer_id, product_id, quantity, status) VALUES ($1, $2, $3, 'new') RETURNING id",
			"INSERT INTO orders (customer_id, product_id, quantity, status) VALUES (?, ?, ?, ?) RETURNING id"},
		{"mysql", "insert into t values (1,'a'),(2,'b'), (3, 'c')", "insert into t values (?,?)"},
		{"postgresql", "SELECT /* hint */ a FROM t -- trailing\nWHERE x = 'it''s'", "SELECT a FROM t WHERE x = ?"},
		{"postgresql", "SELECT col1 FROM t2 WHERE a=1.5e3 AND b=0x1F", "SELECT col1 FROM t2 WHERE a=? AND b=?"},
		{"postgresql", "SELECT * FROM t WHERE a = :name AND b = @p1 AND c = ? AND d::text = 'x'", "SELECT * FROM t WHERE a = ? AND b = ? AND c = ? AND d::text = ?"},
		{"postgresql", "SELECT $$abc$$, $tag$x;y$tag$, E'a\\'b'", "SELECT ?, ?, ?"},
		{"postgresql", "UPDATE users SET active = true WHERE \"Order\".\"id\" = 5", "UPDATE users SET active = ? WHERE \"Order\".\"id\" = ?"},
		{"postgresql", "SELECT pg_sleep(1.2 + random() * 0.6), count(*) FROM orders", "SELECT pg_sleep(? + random() * ?), count(*) FROM orders"},
		{"postgresql", "  SELECT\n\t1  ", "SELECT ?"},
		{"redis", "GET product:12", "GET ?"},
		{"redis", "setex product:1 60 {\"id\":1}", "SETEX ?"},
		{"redis", "PING", "PING"},
		{"postgresql", "", ""},
	}
	for _, c := range cases {
		if got := NormalizeStatement(c.system, c.in); got != c.want {
			t.Errorf("NormalizeStatement(%q, %q)\n got %q\nwant %q", c.system, c.in, got, c.want)
		}
	}
	long := "SELECT " + strings.Repeat("a_column, ", 400) + "x FROM t"
	if got := NormalizeStatement("postgresql", long); len(got) > MaxStatement || !strings.HasSuffix(got, "…") {
		t.Errorf("long statement: len %d suffix %q", len(got), got[len(got)-8:])
	}
}

func TestNormalizeMessage(t *testing.T) {
	cases := map[string]string{
		"order 34: inventory shard 2 unavailable":                        "order <n>: inventory shard <n> unavailable",
		"Product 13 is discontinued":                                     "Product <n> is discontinued",
		"user 'bob' not found (id 3fa85f64-5717-4562-b3fc-2c963f66afa6)": "user '?' not found (id <uuid>)",
		"can't connect to 10.0.0.5:5432":                                 "can't connect to <ip>",
		"hash deadbeef12 mismatch in shard4 utf8":                        "hash <hex> mismatch in shard4 utf8",
		"Cannot read properties of undefined (reading 'price')":          "Cannot read properties of undefined (reading '?')",
		"mail to ada@example.com failed after 1.5s":                      "mail to <email> failed after <n>s",
		"  multi \n  line  ":                                             "multi line",
	}
	for in, want := range cases {
		if got := NormalizeMessage(in); got != want {
			t.Errorf("NormalizeMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTopFrame(t *testing.T) {
	goStack := "goroutine 42 [running]:\nruntime/debug.Stack()\n\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e\n" +
		"go.opentelemetry.io/otel/sdk/trace.recordStackTrace()\n\t/go/pkg/mod/go.opentelemetry.io/otel/sdk@v1.38.0/trace/span.go:596 +0x1c\n" +
		"main.loadInventory(0xc0001, 0x11)\n\t/app/main.go:88 +0x2a\n" +
		"net/http.HandlerFunc.ServeHTTP(...)\n\t/usr/local/go/src/net/http/server.go:2294\n"
	nodeStack := "TypeError: Cannot read properties of undefined (reading 'price')\n" +
		"    at /app/node_modules/express/lib/router/layer.js:95:5\n" +
		"    at flaky (/app/server.js:42:17)\n" +
		"    at Layer.handle [as handle_request] (/app/node_modules/express/lib/router/layer.js:95:5)"
	javaStyle := "RuntimeException: Product 13 is discontinued\n\tat Slim.App.handle(/app/vendor/slim/slim/Slim/App.php:208)\n\tat App\\Catalog.find(/app/src/Catalog.php:25)\n"
	phpStack := "#0 /app/vendor/slim/slim/Slim/Handlers/Strategies/RequestResponse.php(38): App\\Catalog->find(13)\n#1 /app/src/Routes.php(20): App\\Catalog->show()\n#2 {main}"
	pyStack := "Traceback (most recent call last):\n  File \"/app/app.py\", line 9, in handler\n  File \"/usr/lib/python3/site-packages/lib.py\", line 1, in inner\nValueError: bad"
	cases := map[string]string{
		goStack:          "main.loadInventory@/app/main.go",
		nodeStack:        "flaky@/app/server.js",
		javaStyle:        "App\\Catalog.find@/app/src/Catalog.php",
		phpStack:         "App\\Catalog->show@/app/src/Routes.php",
		pyStack:          "handler@/app/app.py",
		"":               "",
		"no frames here": "",
		"    at /app/node_modules/x/index.js:1:1": "<anonymous>@/app/node_modules/x/index.js",
	}
	for in, want := range cases {
		if got := TopFrame(in); got != want {
			t.Errorf("TopFrame(%.40q…) = %q, want %q", in, got, want)
		}
	}
	// Line numbers do not change the frame.
	if TopFrame(strings.ReplaceAll(goStack, "main.go:88", "main.go:91")) != TopFrame(goStack) {
		t.Error("frame depends on the line number")
	}
}

func exceptionInput(msg, stack string) *Input {
	return &Input{
		Resource:   map[string]string{"service.name": "orders", "service.namespace": "shop", "deployment.environment.name": "prod"},
		Attributes: map[string]string{"http.request.method": "GET", "http.route": "/orders/{id}", "http.response.status_code": "500"},
		Kind:       KindServer, StatusCode: StatusError, Name: "GET /orders/{id}",
		EventsName: []string{"log", "exception"},
		EventsAttributes: []map[string]string{{}, {"exception.type": "*fmt.wrapError", "exception.message": msg,
			"exception.stacktrace": "main.loadInventory()\n\t/app/main.go:" + stack + " +0x1\n"}},
	}
}

func TestDeriveServerError(t *testing.T) {
	a := Derive(exceptionInput("order 34: inventory shard 2 unavailable", "88"))
	b := Derive(exceptionInput("order 51: inventory shard 3 unavailable", "90"))
	if !a.IsEntry || a.TransactionType != TxWeb || a.TransactionName != "GET /orders/{id}" || !a.IsError || a.HTTPStatusCode != 500 {
		t.Errorf("derived %+v", a)
	}
	if a.ServiceNamespace != "shop" || a.Environment != "prod" || a.SampleWeight != 1 {
		t.Errorf("identity %+v", a)
	}
	if a.ErrorGroupID == 0 || a.ErrorGroupID != b.ErrorGroupID || a.ErrorType != "*fmt.wrapError" || a.ErrorMessage != "order <n>: inventory shard <n> unavailable" {
		t.Errorf("error grouping: %+v vs %+v", a, b)
	}
	other := exceptionInput("order 34: inventory shard 2 unavailable", "88")
	other.EventsAttributes[1]["exception.stacktrace"] = "main.other()\n\t/app/other.go:1 +0x1\n"
	if Derive(other).ErrorGroupID == a.ErrorGroupID {
		t.Error("different top frame, same group")
	}
	noExc := exceptionInput("", "")
	noExc.EventsName, noExc.EventsAttributes, noExc.StatusCode = nil, nil, "unset"
	d := Derive(noExc)
	if !d.IsError || d.ErrorType != "HTTP 500" {
		t.Errorf("HTTP 500 without exception: %+v", d)
	}
	if id, ok := ParseGroupID(GroupIDString(d.ErrorGroupID)); !ok || id != d.ErrorGroupID {
		t.Errorf("group id round trip %x", d.ErrorGroupID)
	}
}

func TestDeriveClientDB(t *testing.T) {
	d := Derive(&Input{
		Resource: map[string]string{"service.name": "orders"},
		Attributes: map[string]string{"db.system": "postgresql", "db.name": "orders", "db.statement": "SELECT * FROM orders WHERE id = 7",
			"server.address": "orders-db"},
		Kind: KindClient, Name: "SELECT orders", ParentSpanID: "abc", TraceState: "ot=th:8",
	})
	if d.IsEntry || d.IsError || d.PeerType != PeerDB || d.PeerName != "postgresql/orders" || d.DBSystem != "postgresql" ||
		d.DBName != "orders" || d.DBOperation != "SELECT" || d.DBStatementNormalized != "SELECT * FROM orders WHERE id = ?" || d.SampleWeight != 2 {
		t.Errorf("derived %+v", d)
	}
	// Client 4xx without error status is not an error; no service: no APM fields besides identity.
	c := Derive(&Input{Resource: map[string]string{}, Attributes: map[string]string{"http.response.status_code": "404", "server.address": "x"}, Kind: KindClient})
	if c.IsError || c.PeerType != "" || c.IsEntry {
		t.Errorf("no service: %+v", c)
	}
	s := Derive(&Input{Resource: map[string]string{"service.name": "a"}, Attributes: map[string]string{"http.response.status_code": "404"}, Kind: KindServer})
	if s.IsError {
		t.Errorf("server 404 is an error: %+v", s)
	}
}

func TestSampleWeight(t *testing.T) {
	cases := []struct {
		ts        string
		span, res map[string]string
		want      float64
	}{
		{"", nil, nil, 1},
		{"ot=th:8", nil, nil, 2},
		{"ot=th:0", nil, nil, 1},
		{"vendor=x,ot=rv:abcdef01234567;th:c", nil, nil, 4},
		{"ot=p:2", nil, nil, 4},
		{"ot=p:63", nil, nil, 0},
		{"", map[string]string{"sampling.ratio": "0.1"}, nil, 10},
		{"", nil, map[string]string{"sampling.ratio": "0.25"}, 4},
		{"ot=th:zz", map[string]string{"sampling.ratio": "nope"}, nil, 1},
		{"", map[string]string{"sampling.ratio": "1e-9"}, nil, 1e6},
	}
	for _, c := range cases {
		if got := SampleWeight(c.ts, c.span, c.res); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("SampleWeight(%q, %v, %v) = %v, want %v", c.ts, c.span, c.res, got, c.want)
		}
	}
}

func TestBucketBounds(t *testing.T) {
	fixed := map[uint64]int16{1_000_000: 0, 2_000_000: 8, 1_500_000: 5, 500: HistMinBucket, 1 << 62: HistMaxBucket}
	for ns, want := range fixed {
		if got := Bucket(ns); got != want {
			t.Errorf("Bucket(%d) = %d, want %d", ns, got, want)
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		ns := uint64(math.Exp(r.Float64()*25)) + 1001
		b := Bucket(ns)
		ms := float64(ns) / 1e6
		if b > HistMinBucket && b < HistMaxBucket && !(BucketLowerMs(b) < ms*(1+1e-12) && ms <= BucketUpperMs(b)*(1+1e-12)) {
			t.Fatalf("duration %v ms in bucket %d (%v, %v]", ms, b, BucketLowerMs(b), BucketUpperMs(b))
		}
	}
}

func TestHistQuantileAccuracy(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	var durations []float64
	m := map[int16]float64{}
	for range 20000 {
		ms := math.Exp(r.NormFloat64()*1.2 + 3) // lognormal around 20 ms
		durations = append(durations, ms)
		m[Bucket(uint64(ms*1e6))]++
	}
	var keys []int16
	var vals []float64
	for k, v := range m {
		keys = append(keys, k)
		vals = append(vals, v)
	}
	h := NewHist(keys, vals)
	sort.Float64s(durations)
	for _, q := range []float64{0.5, 0.9, 0.95, 0.99} {
		exact := durations[int(math.Ceil(q*float64(len(durations))))-1]
		got := HistQuantile(h, q)
		if rel := math.Abs(got-exact) / exact; rel > 0.045 {
			t.Errorf("q%.2f: hist %.3f exact %.3f (%.2f%%)", q, got, exact, rel*100)
		}
	}
	if !math.IsNaN(HistQuantile(Hist{}, 0.5)) {
		t.Error("empty histogram quantile")
	}
	if merged := h.Add(h); merged.Total() != 2*h.Total() || HistQuantile(merged, 0.5) != HistQuantile(h, 0.5) {
		t.Error("merge changes quantiles")
	}
}

func TestHistApdex(t *testing.T) {
	ok := NewHist([]int16{Bucket(3000e6), Bucket(100e6), Bucket(1000e6)}, []float64{10, 60, 20})
	apdex, sat, tol, has := HistApdex(100, ok, 500)
	if !has || sat != 60 || tol != 20 || math.Abs(apdex-0.7) > 1e-9 {
		t.Errorf("apdex %v satisfied %v tolerating %v", apdex, sat, tol)
	}
	if _, _, _, has := HistApdex(0, Hist{}, 500); has {
		t.Error("apdex without requests")
	}
	// T inside a bucket: linear share.
	one := NewHist([]int16{8}, []float64{100}) // (1.917, 2] ms
	if c := HistCountLE(one, BucketLowerMs(8)+(BucketUpperMs(8)-BucketLowerMs(8))/2); math.Abs(c-50) > 1e-9 {
		t.Errorf("half bucket = %v", c)
	}
}
