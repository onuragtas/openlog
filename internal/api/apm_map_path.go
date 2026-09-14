package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
)

// Service map GA (apm.md §5.1): host/container counts per service node and the path of a transaction.

// mapNodeCounts sets host_count and container_count of service nodes (links seen since from).
func (s *Server) mapNodeCounts(ctx context.Context, sc *query.Scope, from time.Time, nodes []mapNodeJSON) error {
	idCols := []string{"service_name", "service_namespace", "deployment_environment"}
	counts := func(t query.Table, col string) (map[apm.ServiceKey]int, error) {
		q := sc.From(t).Columns(cols(idCols, []string{"uniqExact(" + col + ") AS m_n"})...).
			Where("last_seen >= fromUnixTimestamp64Nano({t_seen:Int64})").Param("t_seen", from.UnixNano()).
			GroupBy(idCols...).Limit(s.cfg.MaxRows)
		rows, err := sc.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[apm.ServiceKey]int{}
		for rows.Next() {
			var k apm.ServiceKey
			var n uint64
			if err := rows.Scan(&k.Name, &k.Namespace, &k.Environment, &n); err != nil {
				return nil, err
			}
			out[k] = int(n)
		}
		return out, rows.Err()
	}
	hasService := false
	for _, n := range nodes {
		hasService = hasService || n.Type == apm.PeerService
	}
	if !hasService {
		return nil
	}
	hosts, err := counts(query.ApmServiceHosts, "host_id")
	if err != nil {
		return err
	}
	containers, err := counts(query.ApmServiceContainers, "container_id")
	if err != nil {
		return err
	}
	for i := range nodes {
		if nodes[i].Type != apm.PeerService {
			continue
		}
		k := apm.ServiceKey{Name: nodes[i].Name, Namespace: nodes[i].ServiceNamespace, Environment: nodes[i].Environment}
		nodes[i].HostCount, nodes[i].ContainerCount = hosts[k], containers[k]
	}
	return nil
}

// pathSpan is one span of a sampled trace.
type pathSpan struct {
	traceID, spanID, parentID, kind, peerType, peerName string
	key                                                 apm.ServiceKey
}

// tracePath returns the service map node and edge ids (ids as in GET /apm/map) that the spans of the sampled
// traces use: attribute edges of client/producer spans and client → server/consumer edges between services.
func tracePath(spans []pathSpan) (nodes, edges []string, traces int) {
	byID := map[string]pathSpan{}
	names := map[string]map[string][]apm.ServiceKey{} // trace -> service name -> keys
	traceSet := map[string]bool{}
	for _, sp := range spans {
		byID[sp.traceID+"|"+sp.spanID] = sp
		traceSet[sp.traceID] = true
		if sp.key.Name == "" {
			continue
		}
		if names[sp.traceID] == nil {
			names[sp.traceID] = map[string][]apm.ServiceKey{}
		}
		names[sp.traceID][sp.key.Name] = append(names[sp.traceID][sp.key.Name], sp.key)
	}
	nodeSet, edgeSet := map[string]bool{}, map[string]bool{}
	for _, sp := range spans {
		if sp.key.Name == "" {
			continue
		}
		src := serviceNodeID(sp.key)
		nodeSet[src] = true
		switch sp.kind {
		case "client", "producer":
			if sp.peerType == "" {
				continue
			}
			var tgt string
			if sp.peerType == apm.PeerService {
				target := apm.ServiceKey{Name: sp.peerName, Namespace: sp.key.Namespace, Environment: sp.key.Environment}
				if keys := names[sp.traceID][sp.peerName]; len(keys) > 0 {
					target = keys[0]
				}
				tgt = serviceNodeID(target)
			} else {
				tgt = depNodeID(sp.peerType, sp.peerName)
			}
			nodeSet[tgt] = true
			edgeSet[src+"->"+tgt] = true
		case "server", "consumer":
			if sp.parentID == "" {
				continue
			}
			parent, ok := byID[sp.traceID+"|"+sp.parentID]
			if !ok || parent.key.Name == "" || parent.key == sp.key || (parent.kind != "client" && parent.kind != "producer") {
				continue
			}
			edgeSet[serviceNodeID(parent.key)+"->"+src] = true
		}
	}
	for id := range nodeSet {
		nodes = append(nodes, id)
	}
	for id := range edgeSet {
		edges = append(edges, id)
	}
	sort.Strings(nodes)
	sort.Strings(edges)
	if nodes == nil {
		nodes = []string{}
	}
	if edges == nil {
		edges = []string{}
	}
	return nodes, edges, len(traceSet)
}

const mapPathTraces = 50

func (s *Server) apmMapPath(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	qp := r.URL.Query()
	name, txn := qp.Get("service"), qp.Get("transaction")
	if name == "" || txn == "" || len(name) > maxServiceNameBytes || len(txn) > 4096 {
		return badRequest("service and transaction are required")
	}
	f := svcFilter{name: name, ns: optionalParam(qp, "namespace"), env: optionalParam(qp, "environment")}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	traces := f.apply(spanRange(sc.From(query.Spans).Columns("trace_id"), from, to)).
		Where("is_entry AND transaction_name = {txn:String}").Param("txn", txn).GroupBy("trace_id").Limit(mapPathTraces)
	// Spans of a sampled trace may start before its entry span or end after the range.
	q := sc.From(query.Spans).Columns("trace_id", "span_id", "parent_span_id", "toString(kind)", "peer_type", "peer_name",
		"service_name", "service_namespace", "deployment_environment").
		Where("timestamp >= fromUnixTimestamp64Nano({p_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({p_to:Int64})").
		Param("p_from", from.Add(-10*time.Minute).UnixNano()).Param("p_to", to.Add(10*time.Minute).UnixNano()).
		WhereIn("trace_id", traces).Limit(50000)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	var spans []pathSpan
	for rows.Next() {
		var sp pathSpan
		if err := rows.Scan(&sp.traceID, &sp.spanID, &sp.parentID, &sp.kind, &sp.peerType, &sp.peerName, &sp.key.Name, &sp.key.Namespace, &sp.key.Environment); err != nil {
			return err
		}
		spans = append(spans, sp)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	nodes, edges, n := tracePath(spans)
	writeJSON(w, http.StatusOK, map[string]any{"trace_count": n, "nodes": nodes, "edges": edges})
	return nil
}
