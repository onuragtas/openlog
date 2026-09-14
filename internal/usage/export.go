package usage

import (
	"encoding/csv"
	"io"
	"strconv"
)

// ExportMetric is one (day, metric, value) row of the long-format CSV export.
type ExportMetric struct {
	Day    string
	Metric string
	Value  float64
}

// ExportRows flattens days into long-format rows: <signal>.items, <signal>.bytes, <signal>.ingest_bytes,
// <signal>.ingest_requests, ingest_bytes, hosts, containers, services, queries, query_failed, query_read_rows,
// query_read_bytes, query_cpu_seconds (docs/contracts/usage.md "Export").
func ExportRows(days []Day) []ExportMetric {
	var out []ExportMetric
	for _, d := range days {
		add := func(m string, v float64) { out = append(out, ExportMetric{Day: d.Day, Metric: m, Value: v}) }
		for _, s := range d.Signals {
			add(s.Signal+".items", float64(s.Items))
			add(s.Signal+".bytes", float64(s.Bytes))
			add(s.Signal+".ingest_bytes", float64(s.IngestBytes))
			add(s.Signal+".ingest_requests", float64(s.IngestRequests))
		}
		add("ingest_bytes", float64(d.IngestBytes))
		add("hosts", float64(d.Hosts))
		add("containers", float64(d.Containers))
		add("services", float64(d.Services))
		add("queries", float64(d.Query.Queries))
		add("query_failed", float64(d.Query.Failed))
		add("query_read_rows", float64(d.Query.ReadRows))
		add("query_read_bytes", float64(d.Query.ReadBytes))
		add("query_cpu_seconds", d.Query.CPUSeconds)
	}
	return out
}

// WriteCSV writes the header "tenant_id,period,day,metric,value" and one line per ExportRows row.
func WriteCSV(w io.Writer, tenantID, period string, days []Day) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"tenant_id", "period", "day", "metric", "value"}); err != nil {
		return err
	}
	for _, r := range ExportRows(days) {
		if err := cw.Write([]string{tenantID, period, r.Day, r.Metric, strconv.FormatFloat(r.Value, 'f', -1, 64)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
