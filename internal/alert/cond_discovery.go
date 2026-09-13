package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

type discoveryType struct{}

func (discoveryType) Name() string              { return TypeDiscovery }
func (discoveryType) Available() bool           { return true }
func (discoveryType) UnavailableReason() string { return "" }
func (discoveryType) DefaultInterval() int      { return 300 }

// DiscoveryCondition is a discovery event condition (§2.5).
type DiscoveryCondition struct {
	Event           string   `json:"event"`
	Filters         []Filter `json:"filters"`
	Match           string   `json:"match"`
	WindowSeconds   int      `json:"window_seconds"`
	LookbackSeconds int      `json:"lookback_seconds"`
}

func (discoveryType) Parse(raw json.RawMessage) (Condition, error) {
	var c DiscoveryCondition
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	switch c.Event {
	case "service_disappeared", "port_opened":
	default:
		return nil, invalid("event", "must be service_disappeared or port_opened")
	}
	if len(c.Match) > maxValueBytes {
		return nil, invalid("match", "at most %d bytes", maxValueBytes)
	}
	if err := validateWindow("window_seconds", &c.WindowSeconds, 900, 300, 86400); err != nil {
		return nil, err
	}
	if c.LookbackSeconds == 0 {
		c.LookbackSeconds = 86400
	}
	if c.LookbackSeconds <= c.WindowSeconds || c.LookbackSeconds > 604800 {
		return nil, invalid("lookback_seconds", "must be greater than window_seconds and at most 604800")
	}
	var err error
	if c.Filters, err = validateFilters(c.Filters, hostTableCols); err != nil {
		return nil, err
	}
	return c, nil
}

func (c DiscoveryCondition) Judge() Judge { return Judge{Operator: "gte", Threshold: 1, Recovery: 1} }
func (c DiscoveryCondition) Window() time.Duration {
	return time.Duration(c.WindowSeconds) * time.Second
}
func (c DiscoveryCondition) IgnoresFor() bool { return true }
func (c DiscoveryCondition) Missing() string  { return "expire" }

func (c DiscoveryCondition) category() string {
	if c.Event == "port_opened" {
		return "listening_port"
	}
	return "discovered_service"
}

func (c DiscoveryCondition) itemLabel() string {
	if c.Event == "port_opened" {
		return "port"
	}
	return "service"
}

func (c DiscoveryCondition) Summary(s Sample, _ string) string {
	host := s.Labels["host.name"]
	if host == "" {
		host = s.Labels["host.id"]
	}
	if c.Event == "port_opened" {
		return fmt.Sprintf("new listening port %s on %s", s.Labels["port"], host)
	}
	return fmt.Sprintf("service %s disappeared from %s", s.Labels["service"], host)
}

// hostFilter restricts q's host_id to hosts matching the filters (host name and resource attributes live in the
// hosts table).
func (c DiscoveryCondition) hostFilter(sc *query.Scope, q *query.Select) {
	if len(c.Filters) == 0 {
		return
	}
	sub := sc.From(query.Hosts).Columns("host_id")
	applyFilters(sub, c.Filters, hostTableCols, "h")
	q.WhereIn("host_id", sub)
}

// itemKey is one (host, item key) pair with its presence per bucket.
type itemKey struct {
	host, key string
}

// fetch returns, per (host, item) and per host, which buckets [0, n) contain snapshots of the category.
func (c DiscoveryCondition) fetch(ctx context.Context, sc *query.Scope, origin time.Time, step time.Duration, n int, lim Limits) (map[itemKey][]bool, map[string][]bool, error) {
	lim = lim.withDefaults()
	q := sc.From(query.InventoryItems)
	b := bucketExpr(q, "snapshot_time", origin, step)
	q.Columns("host_id", "item_key", b+" AS bk").
		Where("category = {category:String}").Param("category", c.category()).
		GroupBy("host_id", "item_key", "bk")
	tsRange(q, "snapshot_time", origin, origin.Add(time.Duration(n)*step))
	if c.Match != "" {
		q.Where("positionCaseInsensitiveUTF8(item_key, {item_match:String}) > 0").Param("item_match", c.Match)
	}
	c.hostFilter(sc, q)
	q.Limit(lim.MaxRows + 1)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	items := map[itemKey][]bool{}
	count := 0
	for rows.Next() {
		count++
		if count > lim.MaxRows {
			rows.Close()
			return nil, nil, &LimitError{Msg: fmt.Sprintf("the condition matches more than %d inventory buckets; add filters or match", lim.MaxRows)}
		}
		var (
			host, key string
			bk        int64
		)
		if err := rows.Scan(&host, &key, &bk); err != nil {
			rows.Close()
			return nil, nil, err
		}
		k := itemKey{host, key}
		if items[k] == nil {
			items[k] = make([]bool, n)
		}
		if bk >= 0 && bk < int64(n) {
			items[k][bk] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}

	// Snapshot presence per host (any item of the category), used to tell a silent host from a removed item.
	hq := sc.From(query.InventoryItems)
	hb := bucketExpr(hq, "snapshot_time", origin, step)
	hq.Columns("host_id", hb+" AS bk").
		Where("category = {category:String}").Param("category", c.category()).
		GroupBy("host_id", "bk")
	tsRange(hq, "snapshot_time", origin, origin.Add(time.Duration(n)*step))
	c.hostFilter(sc, hq)
	hq.Limit(lim.MaxRows + 1)
	hrows, err := sc.Query(ctx, hq)
	if err != nil {
		return nil, nil, err
	}
	defer hrows.Close()
	hosts := map[string][]bool{}
	for hrows.Next() {
		var (
			host string
			bk   int64
		)
		if err := hrows.Scan(&host, &bk); err != nil {
			return nil, nil, err
		}
		if hosts[host] == nil {
			hosts[host] = make([]bool, n)
		}
		if bk >= 0 && bk < int64(n) {
			hosts[host][bk] = true
		}
	}
	return items, hosts, hrows.Err()
}

func anyIn(bs []bool, lo, hi int) bool {
	for i := max(lo, 0); i < hi && i < len(bs); i++ {
		if bs[i] {
			return true
		}
	}
	return false
}

// eventValue decides the event at one end: buckets [aLo, bLo) are the earlier period A, [bLo, bHi) the recent
// window B. ok is false when the series does not exist at that end (no data in A or B).
func (c DiscoveryCondition) eventValue(item, host []bool, aLo, bLo, bHi int) (float64, bool) {
	inA, inB := anyIn(item, aLo, bLo), anyIn(item, bLo, bHi)
	if !inA && !inB {
		return 0, false
	}
	if c.Event == "port_opened" {
		if inB && !inA && anyIn(host, aLo, bLo) {
			return 1, true
		}
		return 0, true
	}
	if inA && !inB && anyIn(host, bLo, bHi) {
		return 1, true
	}
	return 0, true
}

func (c DiscoveryCondition) hostNames(ctx context.Context, sc *query.Scope, ids []string) (map[string]string, error) {
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	q := sc.From(query.Hosts).Columns("host_id", "argMax(host_name, last_seen)").
		Where("has({host_ids:Array(String)}, host_id)").Param("host_ids", ids).GroupBy("host_id")
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

func (c DiscoveryCondition) labels(k itemKey, names map[string]string) (string, map[string]string) {
	labels := map[string]string{"host.id": k.host, "host.name": names[k.host], c.itemLabel(): k.key}
	ds := []dim{{label: "host.id"}, {label: c.itemLabel()}}
	return seriesKey(ds, []string{k.host, k.key}), labels
}

func (c DiscoveryCondition) layout(step time.Duration) (l, w int) {
	return int(ceilDiv(int64(c.LookbackSeconds)*int64(time.Second), int64(step))), int(ceilDiv(int64(c.WindowSeconds)*int64(time.Second), int64(step)))
}

func (c DiscoveryCondition) Evaluate(ctx context.Context, sc *query.Scope, end time.Time, lim Limits) (*EvalResult, error) {
	lim = lim.withDefaults()
	// Two buckets: A = [end-lookback, end-window), B = [end-window, end). The origin bucket is sized so that
	// bucket 1 starts exactly at end-window.
	w := c.Window()
	lb := time.Duration(c.LookbackSeconds) * time.Second
	items, hosts, err := c.fetchTwo(ctx, sc, end, lb, w, lim)
	if err != nil {
		return nil, err
	}
	return c.samples(ctx, sc, items, hosts, lim, func(item, host []bool) (float64, bool) {
		return c.eventValue(item, host, 0, 1, 2)
	})
}

// fetchTwo reads presence in A and B with two bucket widths by querying A and B as separate single buckets.
func (c DiscoveryCondition) fetchTwo(ctx context.Context, sc *query.Scope, end time.Time, lb, w time.Duration, lim Limits) (map[itemKey][]bool, map[string][]bool, error) {
	aItems, aHosts, err := c.fetch(ctx, sc, end.Add(-lb), lb-w, 1, lim)
	if err != nil {
		return nil, nil, err
	}
	bItems, bHosts, err := c.fetch(ctx, sc, end.Add(-w), w, 1, lim)
	if err != nil {
		return nil, nil, err
	}
	items := map[itemKey][]bool{}
	for k, v := range aItems {
		items[k] = []bool{v[0], false}
	}
	for k, v := range bItems {
		if items[k] == nil {
			items[k] = []bool{false, false}
		}
		items[k][1] = v[0]
	}
	hosts := map[string][]bool{}
	for h, v := range aHosts {
		hosts[h] = []bool{v[0], false}
	}
	for h, v := range bHosts {
		if hosts[h] == nil {
			hosts[h] = []bool{false, false}
		}
		hosts[h][1] = v[0]
	}
	return items, hosts, nil
}

func (c DiscoveryCondition) samples(ctx context.Context, sc *query.Scope, items map[itemKey][]bool, hosts map[string][]bool, lim Limits,
	value func(item, host []bool) (float64, bool)) (*EvalResult, error) {
	res := &EvalResult{Unit: ""}
	var ids []string
	seenHost := map[string]bool{}
	type kv struct {
		k itemKey
		v float64
	}
	var found []kv
	for k, pres := range items {
		v, ok := value(pres, hosts[k.host])
		if !ok {
			continue
		}
		found = append(found, kv{k, v})
		if !seenHost[k.host] {
			seenHost[k.host] = true
			ids = append(ids, k.host)
		}
	}
	if len(found) > lim.MaxSeries*20 {
		return nil, &LimitError{Msg: fmt.Sprintf("too many inventory items (%d); add filters or match", len(found))}
	}
	names, err := c.hostNames(ctx, sc, ids)
	if err != nil {
		return nil, err
	}
	for _, f := range found {
		key, labels := c.labels(f.k, names)
		res.Samples = append(res.Samples, Sample{Key: key, Labels: labels, Value: f.v})
	}
	sort.Slice(res.Samples, func(i, j int) bool { return res.Samples[i].Key < res.Samples[j].Key })
	return res, nil
}

func (c DiscoveryCondition) Range(ctx context.Context, sc *query.Scope, from, to time.Time, step time.Duration, lim Limits) (*RangeResult, error) {
	lim = lim.withDefaults()
	ends := rangeEnds(from, to, step)
	l, w := c.layout(step)
	origin := from.Add(-time.Duration(l) * step)
	items, hosts, err := c.fetch(ctx, sc, origin, step, l+len(ends), lim)
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := map[string]bool{}
	for k := range items {
		if !seen[k.host] {
			seen[k.host] = true
			ids = append(ids, k.host)
		}
	}
	names, err := c.hostNames(ctx, sc, ids)
	if err != nil {
		return nil, err
	}
	res := &RangeResult{Ends: ends, Approximate: true}
	for k, pres := range items {
		key, labels := c.labels(k, names)
		rs := RangeSeries{Key: key, Labels: labels, Values: nanSlice(len(ends))}
		any := false
		for i := range ends {
			bHi := l + i + 1
			bLo := bHi - w
			aLo := i + 1
			if v, ok := c.eventValue(pres, hosts[k.host], aLo, bLo, bHi); ok {
				rs.Values[i] = v
				any = true
			}
		}
		if any {
			res.Series = append(res.Series, rs)
		}
	}
	sortSeries(res.Series)
	if len(res.Series) > lim.MaxSeries {
		res.Series, res.Truncated = res.Series[:lim.MaxSeries], true
	}
	return res, nil
}
