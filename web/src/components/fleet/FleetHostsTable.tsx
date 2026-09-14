// Virtualized agents table (TanStack Virtual) with filters and per-host hold/pin actions.
// Narrow containers (phones) show one labelled card per host instead of the 8-column grid.
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { Search } from "lucide-react";
import { useId, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  clearHostOverride,
  clearHostPHPAgentMode,
  fleetHostsQuery,
  setHostOverride,
  setHostPHPAgentMode,
  type FleetHost,
  type FleetHostFilter,
  type FleetPHPAgentMode,
} from "@/api/fleet";
import { DateTimeText, FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { translateOptional } from "@/i18n/dynamic";
import { isVersion, statusTone, updateStateTone } from "@/lib/fleet";
import { isCompact, useElementWidth } from "@/lib/media";
import { cn } from "@/lib/utils";

const STATES = ["idle", "downloading", "verifying", "staged", "restarting", "confirming", "succeeded", "failed", "rolled_back"] as const;

const GRID =
  "minmax(9rem,1.4fr) minmax(7rem,0.9fr) minmax(6rem,0.7fr) minmax(9rem,1.3fr) minmax(9rem,1.2fr) minmax(11rem,1.3fr) minmax(6rem,0.8fr) minmax(6rem,0.7fr) minmax(8rem,auto)";
/** The grid needs 75rem; below this container width rows become cards. */
const COMPACT_BELOW = 768;

type PHPTone = "default" | "secondary" | "outline" | "warning" | "destructive";

function phpStatusTone(status: FleetHost["php_agent"]["status"]): PHPTone {
  switch (status) {
    case "offer":
      return "secondary";
    case "up_to_date":
      return "default";
    case "already_failed":
    case "not_capable":
      return "warning";
    default:
      return "outline";
  }
}

function phpStateTone(state: string): PHPTone {
  if (state === "failed" || state === "rolled_back") return "destructive";
  if (state === "applied") return "default";
  return "secondary";
}

export function FleetHostsTable({
  filter,
  onFilterChange,
  versions,
  canManage,
  height = "32rem",
}: {
  filter: FleetHostFilter;
  onFilterChange: (f: FleetHostFilter) => void;
  versions: string[];
  canManage: boolean;
  height?: string;
}) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const hosts = useInfiniteQuery(fleetHostsQuery(filter));
  const rows = useMemo(() => hosts.data?.pages.flatMap((p) => p.hosts) ?? [], [hosts.data]);
  const scrollRef = useRef<HTMLDivElement>(null);
  const [measureRef, width] = useElementWidth<HTMLDivElement>();
  const compact = isCompact(width, COMPACT_BELOW);
  const [pinning, setPinning] = useState<{ hostId: string; version: string } | null>(null);

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["fleet"] });
  const hold = useMutation({ mutationFn: (hostId: string) => setHostOverride(hostId, "hold"), onSettled: invalidate });
  const pin = useMutation({
    mutationFn: (p: { hostId: string; version: string }) => setHostOverride(p.hostId, "pin", p.version),
    onSuccess: () => setPinning(null),
    onSettled: invalidate,
  });
  const clear = useMutation({ mutationFn: clearHostOverride, onSettled: invalidate });
  const phpMode = useMutation({
    mutationFn: async (p: { hostId: string; mode: FleetPHPAgentMode | "" }) => {
      if (p.mode === "") await clearHostPHPAgentMode(p.hostId);
      else await setHostPHPAgentMode(p.hostId, p.mode);
    },
    onSettled: invalidate,
  });

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual is not compiler-compatible yet
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => (compact ? 230 : 60),
    overscan: compact ? 4 : 10,
    // The layout is part of the key: switching remounts rows so their new heights are measured.
    getItemKey: (i) => `${compact ? "c" : "w"}|${rows[i]!.host_id}`,
  });

  const header = [
    t("fleet.hosts.columns.host"),
    t("fleet.hosts.columns.version"),
    t("fleet.hosts.columns.install"),
    t("fleet.hosts.columns.update"),
    t("fleet.hosts.columns.status"),
    t("fleet.hosts.columns.php"),
    t("fleet.hosts.columns.override"),
    t("fleet.hosts.columns.lastSync"),
    t("fleet.hosts.columns.actions"),
  ];

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <div className="relative w-full sm:w-72">
          <label htmlFor={`${id}-q`} className="sr-only">
            {t("fleet.hosts.search")}
          </label>
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
          <Input
            id={`${id}-q`}
            type="search"
            className="pl-8"
            placeholder={t("fleet.hosts.searchPlaceholder")}
            value={filter.q ?? ""}
            onChange={(e) => onFilterChange({ ...filter, q: e.target.value || undefined })}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <label htmlFor={`${id}-version`} className="text-xs font-medium">
            {t("fleet.hosts.version")}
          </label>
          <NativeSelect id={`${id}-version`} value={filter.version ?? ""} onChange={(e) => onFilterChange({ ...filter, version: e.target.value || undefined })}>
            <option value="">{t("fleet.hosts.allVersions")}</option>
            {versions.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <label htmlFor={`${id}-state`} className="text-xs font-medium">
            {t("fleet.hosts.state")}
          </label>
          <NativeSelect id={`${id}-state`} value={filter.state ?? ""} onChange={(e) => onFilterChange({ ...filter, state: e.target.value || undefined })}>
            <option value="">{t("fleet.hosts.allStates")}</option>
            {STATES.map((s) => (
              <option key={s} value={s}>
                {t(`fleet.updateState.${s}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
        {hosts.data && <p className="ml-auto text-xs text-muted-foreground" aria-live="polite">{t("fleet.hosts.count", { count: rows.length })}</p>}
      </div>
      <FormError error={hold.error ?? pin.error ?? clear.error ?? phpMode.error} />

      <div ref={measureRef} className="rounded-xl border bg-card text-sm">
        {hosts.isPending ? (
          <LoadingState />
        ) : hosts.isError ? (
          <ErrorState error={hosts.error} onRetry={() => void hosts.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState>{filter.q || filter.version || filter.state ? t("fleet.hosts.noMatch") : t("fleet.hosts.empty")}</EmptyState>
        ) : (
          <div role="table" aria-label={t("fleet.hosts.title")} aria-rowcount={rows.length + 1} className={compact ? undefined : "overflow-x-auto"}>
            <div className={compact ? undefined : "min-w-[75rem]"}>
              <div role="rowgroup" className={compact ? "sr-only" : "border-b"}>
                <div role="row" className="grid items-center px-2" style={{ gridTemplateColumns: GRID }}>
                  {header.map((h, i) => (
                    <div role="columnheader" key={h} className="h-9 content-center px-2 text-xs font-medium text-muted-foreground">
                      {i === header.length - 1 && !canManage ? <span className="sr-only">{h}</span> : h}
                    </div>
                  ))}
                </div>
              </div>
              <div
                ref={scrollRef}
                role="rowgroup"
                className="overflow-y-auto overscroll-contain"
                style={{ maxHeight: compact ? "70dvh" : height }}
                data-testid="fleet-hosts-scroll"
              >
                <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
                  {virtualizer.getVirtualItems().map((vi) => {
                    const h = rows[vi.index]!;
                    return (
                      <div
                        key={vi.key}
                        data-index={vi.index}
                        ref={virtualizer.measureElement}
                        role="row"
                        aria-rowindex={vi.index + 2}
                        data-testid="fleet-host-row"
                        className={cn(
                          "absolute top-0 left-0 grid w-full border-b border-border/60 hover:bg-muted/40",
                          compact ? "grid-cols-2 gap-x-3 gap-y-2 p-3" : "items-center px-2",
                        )}
                        style={{ transform: `translateY(${vi.start}px)`, gridTemplateColumns: compact ? undefined : GRID }}
                      >
                        <HostCells
                          host={h}
                          canManage={canManage}
                          labels={compact ? header : undefined}
                          pinning={pinning?.hostId === h.host_id ? pinning : null}
                          busy={hold.isPending || pin.isPending || clear.isPending}
                          phpBusy={phpMode.isPending}
                          onPHPMode={(mode) => phpMode.mutate({ hostId: h.host_id, mode })}
                          onHold={() => hold.mutate(h.host_id)}
                          onClear={() => clear.mutate(h.host_id)}
                          onPinStart={() => setPinning({ hostId: h.host_id, version: h.override?.version ?? h.agent.version })}
                          onPinChange={(version) => setPinning({ hostId: h.host_id, version })}
                          onPinCancel={() => setPinning(null)}
                          onPinSave={(version) => pin.mutate({ hostId: h.host_id, version })}
                        />
                      </div>
                    );
                  })}
                </div>
              </div>
            </div>
          </div>
        )}
      </div>
      {hosts.hasNextPage && (
        <div>
          <Button type="button" variant="outline" size="sm" disabled={hosts.isFetchingNextPage} onClick={() => void hosts.fetchNextPage()}>
            {t("fleet.hosts.loadMore")}
          </Button>
        </div>
      )}
    </div>
  );
}

function HostCells({
  host: h,
  canManage,
  labels,
  pinning,
  busy,
  phpBusy,
  onPHPMode,
  onHold,
  onClear,
  onPinStart,
  onPinChange,
  onPinCancel,
  onPinSave,
}: {
  host: FleetHost;
  canManage: boolean;
  /** Column names shown inside each cell (card layout). */
  labels?: string[];
  pinning: { version: string } | null;
  busy: boolean;
  phpBusy: boolean;
  onPHPMode: (mode: FleetPHPAgentMode | "") => void;
  onHold: () => void;
  onClear: () => void;
  onPinStart: () => void;
  onPinChange: (v: string) => void;
  onPinCancel: () => void;
  onPinSave: (v: string) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const name = h.host_name || h.host_id;
  const card = !!labels;
  const cell = card ? "flex min-w-0 flex-col items-start gap-0.5 text-xs" : "flex min-h-12 min-w-0 flex-col justify-center gap-0.5 px-2 py-1 text-xs";
  const label = (i: number): ReactNode =>
    card ? (
      <span aria-hidden="true" className="text-[11px] text-muted-foreground">
        {labels[i]}
      </span>
    ) : null;
  const buttonSize = card ? "" : "h-7 px-2";
  return (
    <>
      <div role="cell" className={cn(cell, card && "col-span-2")}>
        <span className={cn("max-w-full truncate font-medium", card && "text-sm")} title={name}>
          {name}
        </span>
        <span className="max-w-full truncate font-mono text-[11px] text-muted-foreground" title={h.host_id}>
          {h.agent.os}/{h.agent.arch}
        </span>
      </div>
      <div role="cell" className={cell}>
        {label(1)}
        <span className="font-mono">{h.agent.version || t("common.unknown")}</span>
        <span className="flex flex-wrap gap-1">
          {!h.supported && <Badge variant="destructive">{t("fleet.summary.unsupported")}</Badge>}
          {h.supported && h.outdated && <Badge variant="outline">{t("fleet.summary.outdated")}</Badge>}
        </span>
      </div>
      <div role="cell" className={cell}>
        {label(2)}
        <span>{translateOptional(`fleet.installMethods.${h.agent.install_method || "unknown"}`, h.agent.install_method)}</span>
        {!h.agent.update_capable && <Badge variant="warning">{t("fleet.hosts.notCapable")}</Badge>}
      </div>
      <div role="cell" className={cell}>
        {label(3)}
        <span className="flex flex-wrap items-center gap-1">
          <Badge variant={updateStateTone(h.update.state)}>{translateOptional(`fleet.updateState.${h.update.state}`, h.update.state)}</Badge>
          {h.update.to_version && h.update.state !== "idle" && <span className="font-mono text-[11px]">→ {h.update.to_version}</span>}
        </span>
        {h.update.error && (
          <span className="max-w-full truncate text-destructive-text" title={h.update.error}>
            {t("fleet.hosts.error", { message: h.update.error })}
          </span>
        )}
      </div>
      <div role="cell" className={cell}>
        {label(4)}
        <Badge variant={statusTone(h.status)} className="max-w-full">
          <span className="truncate">{translateOptional(`fleet.status.${h.status}`, h.status)}</span>
        </Badge>
        {h.status_target && <span className="font-mono text-[11px] text-muted-foreground">{h.status_target}</span>}
      </div>
      <div role="cell" className={cn(cell, card && "col-span-2")} data-testid="fleet-host-php">
        {label(5)}
        <PHPCell host={h} canManage={canManage} busy={phpBusy} onMode={onPHPMode} name={name} />
      </div>
      <div role="cell" className={cn(cell, card && !h.override && "hidden")}>
        {h.override && label(6)}
        {h.override?.action === "hold" && <Badge variant="warning">{t("fleet.hosts.held")}</Badge>}
        {h.override?.action === "pin" && <Badge variant="secondary">{t("fleet.hosts.pinnedTo", { version: h.override.version ?? "" })}</Badge>}
      </div>
      <div role="cell" className={cell}>
        {label(7)}
        <DateTimeText value={h.last_sync_at} relative />
      </div>
      <div role="cell" className={cn(cell, card ? "col-span-2" : "items-end", card && !canManage && "hidden")} aria-label={t("fleet.hosts.actionsFor", { host: name })}>
        {canManage &&
          (pinning ? (
            <form
              className="flex flex-wrap items-center gap-1"
              onSubmit={(e) => {
                e.preventDefault();
                if (isVersion(pinning.version)) onPinSave(pinning.version.trim());
              }}
            >
              <label htmlFor={`${id}-pin`} className="sr-only">
                {t("fleet.hosts.pinVersion", { host: name })}
              </label>
              <Input
                id={`${id}-pin`}
                className={card ? "w-28" : "h-7 w-20 px-1.5 text-xs"}
                autoFocus
                value={pinning.version}
                aria-invalid={!isVersion(pinning.version)}
                onChange={(e) => onPinChange(e.target.value)}
              />
              <Button type="submit" size="sm" className={buttonSize} disabled={busy || !isVersion(pinning.version)}>
                {t("fleet.hosts.pinSave")}
              </Button>
              <Button type="button" size="sm" variant="ghost" className={buttonSize} onClick={onPinCancel}>
                {t("common.cancel")}
              </Button>
            </form>
          ) : (
            <span className={cn("flex flex-wrap gap-1", !card && "justify-end")}>
              {h.override ? (
                <Button type="button" size="sm" variant="outline" className={buttonSize} disabled={busy} onClick={onClear}>
                  {t("fleet.hosts.clear")}
                </Button>
              ) : (
                <>
                  <Button type="button" size="sm" variant="outline" className={buttonSize} disabled={busy} onClick={onHold}>
                    {t("fleet.hosts.hold")}
                  </Button>
                  <Button type="button" size="sm" variant="outline" className={buttonSize} disabled={busy} onClick={onPinStart}>
                    {t("fleet.hosts.pin")}
                  </Button>
                </>
              )}
            </span>
          ))}
      </div>
    </>
  );
}

/** PHP runtimes, installed PHP agent, last operation, decision and the per-host mode (php-agent.md §7.3). */
function PHPCell({
  host: h,
  canManage,
  busy,
  onMode,
  name,
}: {
  host: FleetHost;
  canManage: boolean;
  busy: boolean;
  onMode: (mode: FleetPHPAgentMode | "") => void;
  name: string;
}) {
  const { t } = useTranslation();
  const id = useId();
  const p = h.php_agent;
  if (!p.reported) {
    return <span className="text-muted-foreground">{t("fleet.hosts.phpNotReported")}</span>;
  }
  const runtimeTitle = p.runtimes
    .map((r) => {
      const state = r.excluded ? t("fleet.hosts.phpExcluded") : !r.supported ? t("fleet.hosts.phpUnsupported") : r.loaded ? t("fleet.hosts.phpLoaded") : t("fleet.hosts.phpNotLoaded");
      return `${t("fleet.hosts.phpRuntime", { bin: r.bin, version: r.version })} (${state})`;
    })
    .join("\n");
  return (
    <>
      <span className="flex flex-wrap items-center gap-1">
        {p.runtimes.length === 0 ? (
          <span className="text-muted-foreground">{t("fleet.hosts.phpNone")}</span>
        ) : (
          <span title={runtimeTitle} className="underline decoration-dotted underline-offset-2">
            {t("fleet.hosts.phpRuntimes", { count: p.runtimes.length })}
          </span>
        )}
        {p.version && <span className="font-mono text-[11px]">{t("fleet.hosts.phpInstalled", { version: p.version })}</span>}
      </span>
      <span className="flex flex-wrap items-center gap-1">
        <Badge variant={phpStatusTone(p.status)} className="max-w-full">
          <span className="truncate">
            {p.status === "offer" ? t("fleet.phpStatus.offer", { version: p.status_target ?? "" }) : translateOptional(`fleet.phpStatus.${p.status}`, p.status)}
          </span>
        </Badge>
        {p.update && p.update.state !== "applied" && (
          <Badge variant={phpStateTone(p.update.state)} title={p.update.error || undefined} className="max-w-full">
            <span className="truncate">
              {translateOptional(`fleet.phpState.${p.update.state}`, p.update.state)} {p.update.version}
            </span>
          </Badge>
        )}
      </span>
      {p.update?.error && (
        <span className="w-full max-w-full truncate text-destructive-text" title={p.update.error}>
          {t("fleet.hosts.error", { message: p.update.error })}
        </span>
      )}
      {canManage && (
        <>
          <label htmlFor={`${id}-php`} className="sr-only">
            {t("fleet.hosts.phpModeFor", { host: name })}
          </label>
          <NativeSelect
            id={`${id}-php`}
            className="h-7 w-full max-w-full min-w-0 text-xs"
            value={p.override?.mode ?? ""}
            disabled={busy}
            onChange={(e) => onMode(e.target.value as FleetPHPAgentMode | "")}
          >
            <option value="">{t("fleet.hosts.phpFollowPolicy")}</option>
            <option value="auto">{t("fleet.hosts.phpForce.auto")}</option>
            <option value="manual">{t("fleet.hosts.phpForce.manual")}</option>
            <option value="off">{t("fleet.hosts.phpForce.off")}</option>
          </NativeSelect>
        </>
      )}
    </>
  );
}
