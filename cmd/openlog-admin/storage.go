package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// storageCommand implements `openlog-admin storage status` (docs/operations/tiered-storage.md): storage policy, disks,
// bytes per table per volume summed over the replicas, pending and running moves, and the TTL changes openlog-migrate
// would still apply. It uses the OPENLOG_CLICKHOUSE_* (writer) and OPENLOG_STORAGE_* variables.
func storageCommand(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "status" {
		return errors.New("usage: openlog-admin storage status [--json]")
	}
	fs := flag.NewFlagSet("storage status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	conn, err := clickhouse.Open(ctx, clickhouse.OptionsFromConfig(cfg.Common))
	if err != nil {
		return err
	}
	defer conn.Close()
	st, err := migrate.ReadStorageStatus(ctx, conn, cfg.ClickHouseCluster, cfg.Storage.Policy)
	if err != nil {
		return err
	}
	ttlOpts, planErr := app.TTLOptions(cfg) // includes per-tenant retention table TTLs (D-081)
	var plan migrate.TTLPlan
	if planErr == nil {
		plan, planErr = migrate.PlanTableTTLs(ctx, conn, ttlOpts)
	}
	if *asJSON {
		out := struct {
			migrate.StorageStatus
			TieringEnabled bool     `json:"tiering_enabled"`
			PendingDDL     []string `json:"pending_ddl"`
			PlanError      string   `json:"plan_error,omitempty"`
		}{StorageStatus: st, TieringEnabled: cfg.Storage.TieringEnabled, PendingDDL: []string{}}
		for _, s := range plan.Steps {
			out.PendingDDL = append(out.PendingDDL, s.SQL)
		}
		if planErr != nil {
			out.PlanError = planErr.Error()
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	printStorageStatus(stdout, st, cfg, plan, planErr)
	return nil
}

func printStorageStatus(w io.Writer, st migrate.StorageStatus, cfg config.Config, plan migrate.TTLPlan, planErr error) {
	tiering := "disabled"
	if cfg.Storage.TieringEnabled {
		tiering = "enabled"
	}
	var vols []string
	for _, v := range st.Volumes {
		vols = append(vols, fmt.Sprintf("%s[%s]", v.Name, strings.Join(v.Disks, ",")))
	}
	if len(vols) == 0 {
		vols = []string{"not defined on this server"}
	}
	fmt.Fprintf(w, "cluster %s, tiering %s, storage policy %s: %s\n\n", st.Cluster, tiering, st.Policy, strings.Join(vols, " -> "))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tDISK\tTYPE\tREMOTE\tBROKEN\tFREE\tTOTAL\tCACHE")
	for _, d := range st.Disks {
		free, total := humanBytes(d.FreeBytes), humanBytes(d.TotalBytes)
		if d.Remote {
			free, total = "-", "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%t\t%s\t%s\t%s\n", d.Host, d.Name, d.Type, d.Remote, d.Broken, free, total, d.CachePath)
	}
	_ = tw.Flush()
	fmt.Fprintln(w)

	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TABLE\tCLASS\tVOLUME\tDISK\tREPLICAS\tPARTS\tROWS\tSIZE\tPARTITIONS\tOVERDUE MOVES\tPOLICY")
	var overdue uint64
	for _, t := range st.Tables {
		vol := t.Volume
		if vol == "" {
			vol = "-"
		}
		due := "-"
		if t.OverdueParts > 0 {
			due = fmt.Sprintf("%d parts, %s", t.OverdueParts, humanBytes(t.OverdueBytes))
			overdue += t.OverdueParts
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s..%s\t%s\t%s\n", t.Table, t.Class, vol, t.Disk, t.Replicas, t.Parts, t.Rows,
			humanBytes(t.Bytes), t.OldestPartition, t.NewestPartition, due, strings.Join(t.Policies, ","))
	}
	_ = tw.Flush()
	fmt.Fprintln(w)

	fmt.Fprintf(w, "moves in progress: %d, parts past their move TTL still on the hot volume: %d\n", len(st.Moves), overdue)
	for _, m := range st.Moves {
		fmt.Fprintf(w, "  %s %s %s -> %s (%s, %.0fs)\n", m.Host, m.Table, m.Part, m.TargetDisk, humanBytes(m.Bytes), m.Elapsed)
	}
	switch {
	case st.PartLogError != "":
		fmt.Fprintf(w, "failed moves (24h): unknown (system.part_log: %s)\n", firstLine(st.PartLogError))
	case len(st.FailedMoves) == 0:
		fmt.Fprintln(w, "failed moves (24h): 0")
	default:
		fmt.Fprintln(w, "failed moves (24h):")
		for _, f := range st.FailedMoves {
			fmt.Fprintf(w, "  %s %s: %d, last %s: %s\n", f.Host, f.Table, f.Count, f.Last.UTC().Format("2006-01-02 15:04:05"), firstLine(f.LastError))
		}
	}
	if len(st.Detached) == 0 {
		fmt.Fprintln(w, "detached parts: 0")
	} else {
		fmt.Fprintln(w, "detached parts (system.detached_parts; `ignored` = empty leftovers of dropped parts, broken* = investigate):")
		for _, d := range st.Detached {
			fmt.Fprintf(w, "  %s %s reason=%s disk=%s: %d\n", d.Host, d.Table, d.Reason, d.Disk, d.Count)
		}
	}
	switch {
	case planErr != nil:
		fmt.Fprintf(w, "pending openlog-migrate changes: unknown (%v)\n", planErr)
	case len(plan.Steps) == 0:
		fmt.Fprintln(w, "pending openlog-migrate changes: none")
	default:
		fmt.Fprintln(w, "pending openlog-migrate changes (applied by the next openlog-migrate run):")
		for _, s := range plan.Steps {
			fmt.Fprintf(w, "  %s\n", s.SQL)
		}
	}
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
