import { useIsFetching, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, RefreshCw, Timer } from "lucide-react";
import { DropdownMenu, Tooltip } from "radix-ui";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { REFRESH_INTERVALS, refreshActiveQueries, refreshingFilters, type RefreshInterval } from "@/lib/auto-refresh";
import { cn } from "@/lib/utils";

const itemClass =
  "flex min-h-9 cursor-default items-center gap-2 rounded px-2 py-1.5 text-sm outline-none select-none data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground pointer-coarse:min-h-11";

/**
 * "Refresh now" plus the auto-refresh interval menu. `disabled` (absolute range) keeps manual refresh and explains in a
 * tooltip why the interval cannot be chosen. Below `sm` both live in one icon menu.
 */
export function AutoRefreshControl({
  value,
  onChange,
  disabled = false,
  className,
}: {
  value: RefreshInterval | undefined;
  onChange: (v: RefreshInterval | undefined) => void;
  disabled?: boolean;
  className?: string;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const hintId = useId();
  const fetching = useIsFetching(refreshingFilters) > 0;
  const active = !!value && !disabled;
  const refreshNow = () => void refreshActiveQueries(client);
  const label = value ? t(`autoRefresh.intervals.${value}`) : t("autoRefresh.off");
  const triggerLabel = disabled ? t("autoRefresh.label") : value ? t("autoRefresh.every", { interval: label }) : t("autoRefresh.labelOff");

  const intervals = (
    <DropdownMenu.RadioGroup value={value ?? "off"} onValueChange={(v) => onChange(v === "off" ? undefined : (v as RefreshInterval))}>
      {(["off", ...REFRESH_INTERVALS] as const).map((v) => (
        <DropdownMenu.RadioItem key={v} value={v} disabled={disabled && v !== "off"} className={cn(itemClass, "pl-7 data-[disabled]:opacity-50")}>
          <DropdownMenu.ItemIndicator className="absolute left-2 inline-flex">
            <Check className="size-4" aria-hidden="true" />
          </DropdownMenu.ItemIndicator>
          {v === "off" ? t("autoRefresh.off") : t(`autoRefresh.intervals.${v}`)}
        </DropdownMenu.RadioItem>
      ))}
    </DropdownMenu.RadioGroup>
  );
  const content = (children: React.ReactNode) => (
    <DropdownMenu.Portal>
      <DropdownMenu.Content align="end" sideOffset={6} collisionPadding={8} className="z-50 min-w-40 rounded-lg border bg-card p-1 text-card-foreground shadow-lg [&_[role=menuitemradio]]:relative">
        {children}
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  );
  const dot = active && <span className="absolute top-1.5 right-1.5 size-2 rounded-full bg-primary" aria-hidden="true" />;

  return (
    <Tooltip.Provider delayDuration={200}>
      <div className={cn("flex shrink-0 items-center", className)}>
        <span id={hintId} className="sr-only">
          {disabled ? t("autoRefresh.absoluteDisabled") : ""}
        </span>

        {/* Phones: one icon menu with "refresh now" and the intervals. */}
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild>
            <Button variant="ghost" size="icon" className="relative sm:hidden" aria-label={triggerLabel} aria-describedby={disabled ? hintId : undefined}>
              {fetching ? <RefreshCw className="animate-spin motion-reduce:animate-none" aria-hidden="true" /> : <Timer aria-hidden="true" />}
              {dot}
            </Button>
          </DropdownMenu.Trigger>
          {content(
            <>
              <DropdownMenu.Item className={itemClass} onSelect={refreshNow}>
                <RefreshCw className="size-4" aria-hidden="true" />
                {t("autoRefresh.refreshNow")}
              </DropdownMenu.Item>
              <DropdownMenu.Separator className="my-1 h-px bg-border" />
              <DropdownMenu.Label className="px-2 py-1 text-xs text-muted-foreground">{disabled ? t("autoRefresh.absoluteDisabled") : t("autoRefresh.label")}</DropdownMenu.Label>
              {intervals}
            </>,
          )}
        </DropdownMenu.Root>

        {/* sm and up: refresh button + interval menu as one segmented control. */}
        <div className="hidden items-center rounded-md border bg-background sm:inline-flex">
          <Button type="button" variant="ghost" size="icon" className="size-8 rounded-r-none" aria-label={t("autoRefresh.refreshNow")} title={t("autoRefresh.refreshNow")} onClick={refreshNow}>
            <RefreshCw className={cn(fetching && "animate-spin motion-reduce:animate-none")} aria-hidden="true" />
          </Button>
          <Tooltip.Root>
            <DropdownMenu.Root>
              <Tooltip.Trigger asChild>
                <DropdownMenu.Trigger asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className={cn("relative h-8 gap-1 rounded-l-none border-l px-2 text-xs", active ? "text-primary" : "text-muted-foreground", disabled && "opacity-60")}
                    aria-label={triggerLabel}
                    aria-describedby={disabled ? hintId : undefined}
                  >
                    {active && <span className="size-1.5 rounded-full bg-primary" aria-hidden="true" />}
                    <span>{label}</span>
                    <ChevronDown className="size-3.5" aria-hidden="true" />
                  </Button>
                </DropdownMenu.Trigger>
              </Tooltip.Trigger>
              {content(
                <>
                  <DropdownMenu.Label className="px-2 py-1 text-xs text-muted-foreground">{t("autoRefresh.label")}</DropdownMenu.Label>
                  {intervals}
                </>,
              )}
            </DropdownMenu.Root>
            {disabled && (
              <Tooltip.Portal>
                <Tooltip.Content sideOffset={6} collisionPadding={8} className="z-50 max-w-64 rounded-md border bg-card px-2 py-1 text-xs text-card-foreground shadow-md">
                  {t("autoRefresh.absoluteDisabled")}
                </Tooltip.Content>
              </Tooltip.Portal>
            )}
          </Tooltip.Root>
        </div>
      </div>
    </Tooltip.Provider>
  );
}
