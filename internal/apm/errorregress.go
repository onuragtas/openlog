package apm

import (
	"context"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// GroupOccurrences are the ClickHouse facts regression detection needs for one group.
type GroupOccurrences struct {
	LastSeen time.Time
	Versions []VersionOccurrence
}

// LoadRegressionInputs reads, for resolved groups, the newest occurrence per group and per service.version
// (apm_error_groups, apm_error_group_dims) and the first span of every version of their services
// (apm_service_versions_1m). Keys of firstSeen are services.
func LoadRegressionInputs(ctx context.Context, sc *query.Scope, states []ErrorGroupState) (map[uint64]GroupOccurrences, map[ServiceKey]map[string]time.Time, error) {
	occ := map[uint64]GroupOccurrences{}
	first := map[ServiceKey]map[string]time.Time{}
	var ids, services []string
	seen := map[string]bool{}
	for _, st := range states {
		if st.EffectiveStatus() != StatusResolved {
			continue
		}
		ids = append(ids, strconv.FormatUint(st.GroupID, 10))
		if !seen[st.Key.Name] {
			seen[st.Key.Name] = true
			services = append(services, st.Key.Name)
		}
	}
	if len(ids) == 0 {
		return occ, first, nil
	}
	gq := sc.From(query.ApmErrorGroups).Columns("error_group_id", "max(last_seen) AS m_last").
		Where("has({rg_ids:Array(String)}, toString(error_group_id))").Param("rg_ids", ids).GroupBy("error_group_id")
	rows, err := sc.Query(ctx, gq)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id uint64
		var last time.Time
		if err := rows.Scan(&id, &last); err != nil {
			rows.Close()
			return nil, nil, err
		}
		o := occ[id]
		o.LastSeen = last
		occ[id] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	dq := sc.From(query.ApmErrorGroupDims).Columns("error_group_id", "value", "max(last_seen) AS m_last").
		Where("dim = 'version'").Where("has({rg_ids:Array(String)}, toString(error_group_id))").Param("rg_ids", ids).
		GroupBy("error_group_id", "value").Limit(100000)
	rows, err = sc.Query(ctx, dq)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id uint64
		var v VersionOccurrence
		if err := rows.Scan(&id, &v.Version, &v.LastSeen); err != nil {
			rows.Close()
			return nil, nil, err
		}
		o := occ[id]
		o.Versions = append(o.Versions, v)
		occ[id] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	vq := sc.From(query.ApmServiceVersions1m).Columns("service_name", "service_namespace", "deployment_environment", "service_version", "min(first_seen) AS m_first").
		Where("has({rg_services:Array(String)}, service_name)").Param("rg_services", services).
		GroupBy("service_name", "service_namespace", "deployment_environment", "service_version").Limit(100000)
	rows, err = sc.Query(ctx, vq)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k ServiceKey
		var version string
		var t time.Time
		if err := rows.Scan(&k.Name, &k.Namespace, &k.Environment, &version, &t); err != nil {
			return nil, nil, err
		}
		if first[k] == nil {
			first[k] = map[string]time.Time{}
		}
		first[k][version] = t
	}
	return occ, first, rows.Err()
}

// CheckRegressions detects regressions of the resolved states (DetectRegression) and, when store is not nil,
// persists them (MarkRegressed). It returns the states with regressed groups reopened and the reopened group ids.
func CheckRegressions(ctx context.Context, sc *query.Scope, store ErrorStateStore, orgID string, states []ErrorGroupState) ([]ErrorGroupState, map[uint64]Regression, error) {
	reopened := map[uint64]Regression{}
	occ, first, err := LoadRegressionInputs(ctx, sc, states)
	if err != nil || len(occ) == 0 {
		return states, reopened, err
	}
	out := append([]ErrorGroupState(nil), states...)
	for i, st := range out {
		if st.EffectiveStatus() != StatusResolved {
			continue
		}
		o := occ[st.GroupID]
		r, ok := DetectRegression(st, o.LastSeen, o.Versions, first[st.Key])
		if !ok {
			continue
		}
		if store != nil {
			changed, err := store.MarkRegressed(ctx, orgID, st.ErrorGroupRef, r)
			if err != nil {
				return states, reopened, err
			}
			if !changed {
				continue // changed concurrently; the next read sees the stored state
			}
		}
		at := r.At
		out[i].Status, out[i].ResolvedAt, out[i].ResolvedInVersion = StatusUnresolved, nil, ""
		out[i].RegressedAt, out[i].RegressionCount = &at, st.RegressionCount+1
		reopened[st.GroupID] = r
	}
	return out, reopened, nil
}
