package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/diskspace"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// cleanCommand implements `openlog-admin storage clean [--apply] [--json]`
// (docs/operations/disk-space.md): what automatic shedding would give up, and optionally giving it up now.
//
// Preview is the default and --apply is the only way to delete anything, because the question an operator asks
// before turning shedding on is "what would it take from me", and that question must be answerable without risk.
//
// The preview plans as though shedding were on even when the stored settings have it off: that is the point of
// asking. --apply, by contrast, is an explicit instruction from a person at a terminal, so it runs regardless of
// the switch that governs the automatic job.
func cleanCommand(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("storage clean", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "drop the partitions; without it nothing is deleted")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	conn, err := clickhouse.Open(ctx, clickhouse.OptionsFromConfig(cfg.Common))
	if err != nil {
		return err
	}
	defer conn.Close()

	// The levels come from the settings row when PostgreSQL is reachable. Falling back to the built-in ones is
	// better than refusing, but the output has to say which it used: a preview against the wrong levels is worse
	// than no preview.
	eff, levels := diskspace.Defaults(), "built-in defaults"
	var record func(context.Context, diskspace.Drop) error
	if pool, perr := app.OpenPostgres(ctx, cfg, "openlog-admin"); perr != nil {
		levels = fmt.Sprintf("built-in defaults (PostgreSQL unreachable: %v)", perr)
	} else {
		defer pool.Close()
		store := diskspace.PGStore{Pool: pool}
		record = store.RecordDrop
		if s, found, gerr := store.Get(ctx); gerr != nil {
			levels = fmt.Sprintf("built-in defaults (cannot read the settings: %v)", gerr)
		} else if found {
			eff, levels = s.Resolve(), "stored settings"
		}
	}

	snap, err := (&diskspace.Checker{CH: conn, Cluster: cfg.ClickHouseCluster}).Read(ctx)
	if err != nil {
		return err
	}
	disk, found := snap.Fullest()
	if !found {
		return fmt.Errorf("no local ClickHouse disk answered")
	}
	shed := &diskspace.Shedder{
		CH: conn, Cluster: cfg.ClickHouseCluster, Database: cfg.ClickHouseDatabase, DryRun: !*apply, Record: record,
	}
	parts, err := shed.Partitions(ctx, disk)
	if err != nil {
		return err
	}
	// Forced on: the preview answers "what would shedding take", which is exactly the question asked while it is
	// still off.
	planned := eff
	planned.ShedEnabled = true
	plan := diskspace.Plan(disk, parts, planned)

	var done []diskspace.Drop
	if *apply && len(plan) > 0 {
		if done, err = shed.Apply(ctx, plan); err != nil {
			printClean(stdout, disk, eff, levels, plan, done, *apply, *asJSON)
			return err
		}
	}
	printClean(stdout, disk, eff, levels, plan, done, *apply, *asJSON)
	return nil
}

func printClean(w io.Writer, disk diskspace.Disk, eff diskspace.Effective, levels string,
	plan, done []diskspace.Drop, applied, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Disk    diskspace.Disk      `json:"disk"`
			Used    float64             `json:"used_percent"`
			Levels  diskspace.Effective `json:"levels"`
			Source  string              `json:"levels_from"`
			Applied bool                `json:"applied"`
			Plan    []diskspace.Drop    `json:"plan"`
			Done    []diskspace.Drop    `json:"dropped"`
			Bytes   uint64              `json:"plan_bytes"`
		}{disk, disk.UsedPercent(), eff, levels, applied, plan, done, diskspace.PlanBytes(plan)})
		return
	}
	fmt.Fprintf(w, "disk %s/%s: %.0f%% used, %s free of %s\n", disk.Host, disk.Name, disk.UsedPercent(),
		humanBytes(disk.Free), humanBytes(disk.Total))
	state := "on"
	if !eff.ShedEnabled {
		state = "off"
	}
	fmt.Fprintf(w, "levels (%s): shedding %s, start %d%%, stop %d%%, keep %d days per table, at most %d days per run\n\n",
		levels, state, eff.ShedStart, eff.ShedStop, eff.ShedMinPartitions, eff.ShedMaxDropsPerRun)

	if len(plan) == 0 {
		if disk.UsedPercent() < float64(eff.ShedStart) {
			fmt.Fprintf(w, "nothing to do: the disk is below the start level (%.0f%% < %d%%)\n",
				disk.UsedPercent(), eff.ShedStart)
			return
		}
		fmt.Fprintln(w, "nothing can be given up: every table is at its floor, or only protected tables are left")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "UNIT\tDAY\tTABLES\tSIZE")
	for _, d := range plan {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.Unit, d.Partition, strings.Join(d.Tables, ","), humanBytes(d.Bytes))
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	switch {
	case !applied:
		fmt.Fprintf(w, "%d days, %s: nothing was deleted. Pass --apply to delete them.\n",
			len(plan), humanBytes(diskspace.PlanBytes(plan)))
		if !eff.ShedEnabled {
			fmt.Fprintln(w, "automatic shedding is off: this is what it would give up if you turned it on.")
		}
	default:
		fmt.Fprintf(w, "deleted %d of %d days, %s freed. This cannot be undone.\n",
			len(done), len(plan), humanBytes(diskspace.PlanBytes(done)))
	}
}
