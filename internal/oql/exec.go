package oql

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/api/query"
)

// ResultColumn describes a result column.
type ResultColumn struct {
	Name     string `json:"name"`
	Function string `json:"function"`
	Type     string `json:"type"`
}

// Row is one row of a single or facets result.
type Row struct {
	Facets []string `json:"facets"`
	Values []any    `json:"values"`
}

// Point is [bucket start ms, value|null].
type Point [2]any

// Series is one timeseries (group × column).
type Series struct {
	Facets []string `json:"facets"`
	Column int      `json:"column"`
	Points []Point  `json:"points"`
}

// HistogramBucket is one histogram bucket.
type HistogramBucket struct {
	From  float64 `json:"from"`
	To    float64 `json:"to"`
	Count float64 `json:"count"`
}

// Compare is the COMPARE WITH part of a result.
type Compare struct {
	OffsetSeconds int64             `json:"offset_seconds"`
	Rows          []Row             `json:"rows"`
	Series        []Series          `json:"series"`
	Buckets       []HistogramBucket `json:"buckets"`
}

// Metadata describes the execution.
type Metadata struct {
	From          time.Time `json:"-"`
	To            time.Time `json:"-"`
	BucketSeconds *int64    `json:"bucket_seconds"`
	Rollup        bool      `json:"rollup"`
	Table         string    `json:"table"`
	RowsRead      uint64    `json:"rows_read"`
	BytesRead     uint64    `json:"bytes_read"`
	ElapsedMs     int64     `json:"elapsed_ms"`
	Queries       int       `json:"queries"`
	FacetLimit    int       `json:"facet_limit"`
	Truncated     bool      `json:"truncated"`
	Warnings      []string  `json:"warnings"`
}

// Result is the outcome of Execute.
type Result struct {
	Kind      Kind              `json:"kind"`
	EventType string            `json:"event_type"`
	Columns   []ResultColumn    `json:"columns"`
	Facets    []string          `json:"facets"`
	Rows      []Row             `json:"rows"`
	Series    []Series          `json:"series"`
	Buckets   []HistogramBucket `json:"buckets"`
	Compare   *Compare          `json:"compare"`
	Metadata  Metadata          `json:"metadata"`
}

type stats struct{ rows, bytes atomic.Uint64 }

func (s *stats) ctx(ctx context.Context) context.Context {
	return ch.Context(ctx, ch.WithProgress(func(p *ch.Progress) {
		s.rows.Add(p.Rows)
		s.bytes.Add(p.Bytes)
	}))
}

// Execute runs the plan with sc (bound to the caller's tenant).
func Execute(ctx context.Context, sc *query.Scope, p *Plan) (*Result, error) {
	start := time.Now()
	st := &stats{}
	res := &Result{Kind: p.Kind, EventType: p.Event.name, Facets: p.FacetNames(), Rows: []Row{}, Series: []Series{}, Buckets: []HistogramBucket{}}
	for _, c := range p.Columns {
		res.Columns = append(res.Columns, ResultColumn{Name: c.Name, Function: c.Function, Type: c.Type.String()})
	}
	res.Metadata = Metadata{From: p.From, To: p.To, Rollup: p.Rollup, Table: p.Table(), FacetLimit: p.Limit, Warnings: []string{}}
	if p.Kind == KindTimeseries {
		bs := int64(p.Bucket / time.Second)
		res.Metadata.BucketSeconds = &bs
	}
	for _, w := range p.Warnings {
		res.Metadata.Warnings = append(res.Metadata.Warnings, w.Msg)
	}
	part, truncated, err := p.run(st.ctx(ctx), sc, p.From, p.To, 0)
	if err != nil {
		return nil, err
	}
	res.Rows, res.Series, res.Buckets = part.rows, part.series, part.buckets
	res.Metadata.Truncated = truncated
	res.Metadata.Queries = 1
	if p.Compare > 0 {
		prev, _, err := p.run(st.ctx(ctx), sc, p.From.Add(-p.Compare), p.To.Add(-p.Compare), p.Compare)
		if err != nil {
			return nil, err
		}
		res.Compare = &Compare{OffsetSeconds: int64(p.Compare / time.Second), Rows: prev.rows, Series: prev.series, Buckets: prev.buckets}
		res.Metadata.Queries = 2
	}
	res.Metadata.RowsRead, res.Metadata.BytesRead = st.rows.Load(), st.bytes.Load()
	res.Metadata.ElapsedMs = time.Since(start).Milliseconds()
	return res, nil
}

type partial struct {
	rows    []Row
	series  []Series
	buckets []HistogramBucket
}

// run executes the main statement for [from, to); shift moves timeseries points forward (COMPARE WITH).
func (p *Plan) run(ctx context.Context, sc *query.Scope, from, to time.Time, shift time.Duration) (*partial, bool, error) {
	q, err := p.build(sc, from, to)
	if err != nil {
		return nil, false, err
	}
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := &partial{rows: []Row{}, series: []Series{}, buckets: []HistogramBucket{}}
	nf := len(p.Facets)
	if p.Kind == KindSingle || p.Kind == KindHistogram {
		nf = 0
	}
	facets := make([]string, nf)
	var bucket int64
	nums := make([]*float64, len(p.Columns))
	strs := make([]string, len(p.Columns))
	dest := make([]any, 0, nf+1+len(p.Columns))
	for i := range facets {
		dest = append(dest, &facets[i])
	}
	if p.Kind == KindTimeseries || p.Kind == KindHistogram {
		dest = append(dest, &bucket)
	}
	for i, c := range p.Columns {
		if c.Type == TString {
			dest = append(dest, &strs[i])
		} else {
			dest = append(dest, &nums[i])
		}
	}
	value := func(i int) any {
		if p.Columns[i].Type == TString {
			return strs[i]
		}
		if nums[i] == nil {
			return nil
		}
		return finiteOrNil(*nums[i])
	}

	type group struct {
		facets []string
		points [][]any // column → bucket index → value
		total  float64
	}
	var (
		groups     = map[string]*group{}
		order      []string
		bucketsIdx map[int64]int
		starts     []int64
		truncated  bool
	)
	if p.Kind == KindTimeseries {
		bucketsIdx = map[int64]int{}
		step := p.Bucket.Milliseconds()
		for t := alignDown(from, p.Bucket).UnixMilli(); t < to.UnixMilli(); t += step {
			bucketsIdx[t] = len(starts)
			starts = append(starts, t)
		}
	}
	if p.Kind == KindHistogram {
		w := p.hist.Ceiling / float64(p.hist.Buckets)
		for i := 0; i < p.hist.Buckets; i++ {
			out.buckets = append(out.buckets, HistogramBucket{From: float64(i) * w, To: float64(i+1) * w})
		}
	}
	for rows.Next() {
		for i := range nums {
			nums[i] = nil
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, false, err
		}
		switch p.Kind {
		case KindSingle:
			r := Row{Facets: []string{}, Values: make([]any, len(p.Columns))}
			for i := range p.Columns {
				r.Values[i] = value(i)
			}
			out.rows = append(out.rows, r)
		case KindFacets:
			if len(out.rows) == p.Limit {
				truncated = true
				continue
			}
			r := Row{Facets: append([]string(nil), facets...), Values: make([]any, len(p.Columns))}
			for i := range p.Columns {
				r.Values[i] = value(i)
			}
			out.rows = append(out.rows, r)
		case KindHistogram:
			if bucket >= 0 && bucket < int64(len(out.buckets)) && nums[0] != nil {
				out.buckets[bucket].Count = *nums[0]
			}
		case KindTimeseries:
			idx, ok := bucketsIdx[bucket]
			if !ok {
				continue
			}
			key := strings.Join(facets, "\x00")
			g, ok := groups[key]
			if !ok {
				g = &group{facets: append([]string(nil), facets...), points: make([][]any, len(p.Columns))}
				for i, c := range p.Columns {
					g.points[i] = make([]any, len(starts))
					if c.zeroFill {
						for j := range g.points[i] {
							g.points[i][j] = float64(0)
						}
					}
				}
				groups[key] = g
				order = append(order, key)
			}
			for i := range p.Columns {
				v := value(i)
				if v == nil && p.Columns[i].zeroFill {
					v = float64(0)
				}
				g.points[i][idx] = v
				if f, ok := v.(float64); ok && i == 0 && !math.IsNaN(f) {
					g.total += f
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if p.Kind == KindTimeseries {
		if len(p.Facets) == 0 && len(groups) == 0 {
			// No data: one empty series per column.
			g := &group{facets: []string{}, points: make([][]any, len(p.Columns))}
			for i, c := range p.Columns {
				g.points[i] = make([]any, len(starts))
				if c.zeroFill {
					for j := range g.points[i] {
						g.points[i][j] = float64(0)
					}
				}
			}
			groups[""] = g
			order = append(order, "")
		}
		sort.SliceStable(order, func(i, j int) bool {
			gi, gj := groups[order[i]], groups[order[j]]
			if gi.total != gj.total {
				return gi.total > gj.total
			}
			return order[i] < order[j]
		})
		shiftMs := shift.Milliseconds()
		for _, key := range order {
			g := groups[key]
			for ci := range p.Columns {
				s := Series{Facets: g.facets, Column: ci, Points: make([]Point, len(starts))}
				if s.Facets == nil {
					s.Facets = []string{}
				}
				for bi, t := range starts {
					s.Points[bi] = Point{t + shiftMs, g.points[ci][bi]}
				}
				out.series = append(out.series, s)
			}
		}
	}
	return out, truncated, nil
}

// WindowSeries is one series of ExecuteWindows: Values[i] is the value of window i (NaN = none).
type WindowSeries struct {
	Facets []string
	Values []float64
}

// ErrTooManyRows is returned by ExecuteWindows when the result exceeds maxRows.
type ErrTooManyRows struct{ Max int }

func (e *ErrTooManyRows) Error() string {
	return "the query matches more than " + strconv.Itoa(e.Max) + " series windows; add filters"
}

// ExecuteWindows evaluates a single/facets plan with one number column over the windows [e_j - window, e_j),
// e_j = origin + j·step for j = 1..n (alert previews). Raw data is always read.
func ExecuteWindows(ctx context.Context, sc *query.Scope, p *Plan, origin time.Time, step, window time.Duration, n, maxRows int) ([]WindowSeries, error) {
	q, k, err := p.buildWindows(sc, origin, step, window, n, maxRows)
	if err != nil {
		return nil, err
	}
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nf := len(p.Facets)
	facets := make([]string, nf)
	var ei uint64
	var v *float64
	dest := make([]any, 0, nf+2)
	for i := range facets {
		dest = append(dest, &facets[i])
	}
	dest = append(dest, &ei, &v)
	zero := len(p.Columns) > 0 && p.Columns[0].zeroFill
	byKey := map[string]*WindowSeries{}
	var order []string
	count := 0
	newSeries := func(fs []string) *WindowSeries {
		ws := &WindowSeries{Facets: fs, Values: make([]float64, n)}
		for i := range ws.Values {
			ws.Values[i] = math.NaN()
			if zero {
				ws.Values[i] = 0
			}
		}
		return ws
	}
	for rows.Next() {
		v = nil
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		count++
		if count > maxRows {
			return nil, &ErrTooManyRows{Max: maxRows}
		}
		j := int(ei) - k - 1 // window index 0..n-1
		if j < 0 || j >= n {
			continue
		}
		key := strings.Join(facets, "\x00")
		ws, ok := byKey[key]
		if !ok {
			ws = newSeries(append([]string{}, facets...))
			byKey[key] = ws
			order = append(order, key)
		}
		if v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) {
			ws.Values[j] = *v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if nf == 0 && len(order) == 0 && zero {
		byKey[""] = newSeries([]string{})
		order = append(order, "")
	}
	out := make([]WindowSeries, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out, nil
}
