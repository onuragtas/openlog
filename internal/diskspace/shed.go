package diskspace

import "sort"

// Deciding what to delete when a disk fills.
//
// Table TTLs bound how old the data gets; they cannot bound how much of it there is. When the ingest rate outruns
// the retention, the disk fills with data that is still inside its TTL, and nothing in ClickHouse will give it up.
// This plans which day to give up instead.
//
// A whole day-partition is dropped rather than rows deleted. ALTER ... DELETE is a mutation: it rewrites parts,
// which needs free space to write the new ones, so it is at its most expensive exactly when the disk is at its
// fullest. DROP PARTITION frees the space immediately, and every table here is partitioned by day.

// Partition is one day of one table, as system.parts reports it.
type Partition struct {
	Table string `json:"table"`
	// ID is the partition id. For the daily toDate() partitions of every sheddable table it is "YYYYMMDD", so
	// sorting the strings sorts the days.
	ID    string `json:"partition"`
	Bytes uint64 `json:"bytes"`
}

// shedUnit is a set of tables that give up the same day together.
type shedUnit struct {
	Name   string
	Tables []string
}

// ShedOrder is the order data is given up in: least valuable per byte first, so a disk under pressure loses
// debugging detail before it loses the record of what happened.
//
// What is absent matters as much as what is here. metrics_1m_local is the long-term memory of the installation
// and is partitioned by month, so one drop would take a whole month of it. usage_* is billing. The apm_* rollups
// are what survives when the spans they came from expire. None of them are ever dropped by this.
var ShedOrder = []shedUnit{
	// Profiles are the widest rows openlog stores and the least often read.
	{"profiles", []string{"profiles_local"}},
	// Exemplars are pointers from a metric into a trace: useful, and worthless once the trace is gone anyway.
	{"metric exemplars", []string{"metric_exemplars_local"}},
	// trace_index points into spans. One outliving the other leaves an index into rows that are gone, so the two
	// give up the same day together.
	{"traces", []string{"spans_local", "trace_index_local"}},
	{"database session samples", []string{"db_session_samples_local"}},
	{"logs", []string{"logs_local"}},
	{"database query statistics", []string{"db_query_stats_local", "db_query_plans_local"}},
	// Raw metrics last of the lot: metrics_1m keeps the shape of history, but only the raw table can answer at
	// full resolution, and an alert that reads it stops working the moment its day is gone.
	{"raw metrics", []string{"metrics_local"}},
}

// ShedTables is every table Plan may drop from, for a caller that needs to ask ClickHouse about exactly those.
func ShedTables() []string {
	var out []string
	for _, u := range ShedOrder {
		out = append(out, u.Tables...)
	}
	return out
}

// Drop is one day of one unit, to be removed from all of the unit's tables.
type Drop struct {
	Unit      string   `json:"unit"`
	Partition string   `json:"partition"`
	Tables    []string `json:"tables"`
	Bytes     uint64   `json:"bytes"`
}

// Plan decides what to drop so that disk falls below the stop level, and returns the drops in the order they
// should be applied. It returns nothing at all unless shedding is enabled and the disk is at or above the start
// level: every other outcome of this function deletes data.
//
// parts is what ClickHouse reports for the sheddable tables on this disk's replica.
func Plan(disk Disk, parts []Partition, eff Effective) []Drop {
	if !eff.ShedEnabled || disk.Total == 0 {
		return nil
	}
	if disk.UsedPercent() < float64(eff.ShedStart) {
		return nil
	}
	used := disk.Total - min(disk.Free, disk.Total)
	target := disk.Total / 100 * uint64(eff.ShedStop)
	if used <= target {
		return nil
	}
	need := used - target

	// Days per table, oldest first, and how many each table still has: the floor is per table, so dropping from
	// one unit must not be allowed to take a table below it on a later pass through this loop.
	bytes := map[string]map[string]uint64{} // table -> partition -> bytes
	days := map[string][]string{}           // table -> partition ids, ascending
	left := map[string]int{}                // table -> partitions not yet planned for removal
	for _, p := range parts {
		if bytes[p.Table] == nil {
			bytes[p.Table] = map[string]uint64{}
		}
		if _, seen := bytes[p.Table][p.ID]; !seen {
			days[p.Table] = append(days[p.Table], p.ID)
			left[p.Table]++
		}
		bytes[p.Table][p.ID] += p.Bytes
	}
	for t := range days {
		sort.Strings(days[t])
	}

	var plan []Drop
	var freed uint64
	for _, unit := range ShedOrder {
		// Every day any table of the unit has, oldest first.
		seen := map[string]bool{}
		var dates []string
		for _, t := range unit.Tables {
			for _, d := range days[t] {
				if !seen[d] {
					seen[d] = true
					dates = append(dates, d)
				}
			}
		}
		sort.Strings(dates)
		for _, date := range dates {
			if freed >= need || len(plan) >= eff.ShedMaxDropsPerRun {
				return plan
			}
			var tables []string
			var size uint64
			floor := false
			for _, t := range unit.Tables {
				if _, ok := bytes[t][date]; !ok {
					continue // this table has no data for that day
				}
				if left[t]-1 < eff.ShedMinPartitions {
					floor = true
					break
				}
				tables = append(tables, t)
				size += bytes[t][date]
			}
			// All or nothing among the tables that have this day: dropping spans without the trace_index of the
			// same day would leave an index into rows that are gone. A table with no data for that day is not
			// part of the bargain, because there is nothing of it to dangle.
			if floor || len(tables) == 0 {
				continue
			}
			for _, t := range tables {
				left[t]--
			}
			plan = append(plan, Drop{Unit: unit.Name, Partition: date, Tables: tables, Bytes: size})
			freed += size
		}
	}
	return plan
}

// PlanBytes is how much the plan expects to free.
func PlanBytes(plan []Drop) uint64 {
	var n uint64
	for _, d := range plan {
		n += d.Bytes
	}
	return n
}
