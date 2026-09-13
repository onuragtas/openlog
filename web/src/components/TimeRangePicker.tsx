import { CalendarClock } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { useIsBelowLg } from "@/lib/media";
import {
  DEFAULT_RANGE,
  fromDateTimeLocal,
  isCustomRange,
  PRESET_RANGES,
  resolveRange,
  toDateTimeLocal,
  type RangeSpec,
} from "@/lib/time";
import { cn } from "@/lib/utils";

/**
 * Preset buttons and a custom from/to form. Below `md` the presets collapse into a native select
 * and the custom range opens in a bottom sheet with native date-time inputs.
 */
export function TimeRangePicker({ value, onChange, className }: { value: RangeSpec; onChange: (spec: RangeSpec) => void; className?: string }) {
  const { t } = useTranslation();
  const id = useId();
  // Phones and tablets: a select plus a bottom sheet (the preset buttons do not fit the top bar).
  const mobile = useIsBelowLg();
  const custom = isCustomRange(value);
  const active = custom ? "custom" : (value.range ?? DEFAULT_RANGE);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState({ from: "", to: "" });
  const [invalid, setInvalid] = useState(false);

  const openCustom = (toggle = true) => {
    const r = resolveRange(value, Date.now());
    setDraft({ from: toDateTimeLocal(r.from), to: toDateTimeLocal(r.to) });
    setInvalid(false);
    setOpen((o) => (toggle ? !o : true));
  };

  const apply = (e: React.FormEvent) => {
    e.preventDefault();
    const f = fromDateTimeLocal(draft.from);
    const to = fromDateTimeLocal(draft.to);
    if (f === null || to === null || f >= to) {
      setInvalid(true);
      return;
    }
    onChange({ from: String(f), to: String(to) });
    setOpen(false);
  };

  const form = (
    <form
      id={`${id}-custom`}
      onSubmit={apply}
      onKeyDown={(e) => e.key === "Escape" && setOpen(false)}
      className={cn("flex flex-col gap-3", mobile ? "p-4" : "absolute right-0 z-30 mt-2 w-72 rounded-lg border bg-card p-3 shadow-lg")}
    >
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-from`}>{t("range.from")}</Label>
        <Input id={`${id}-from`} type="datetime-local" value={draft.from} autoFocus={!mobile} aria-invalid={invalid} onChange={(e) => setDraft((d) => ({ ...d, from: e.target.value }))} />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-to`}>{t("range.to")}</Label>
        <Input id={`${id}-to`} type="datetime-local" value={draft.to} aria-invalid={invalid} onChange={(e) => setDraft((d) => ({ ...d, to: e.target.value }))} />
      </div>
      {invalid && (
        <p role="alert" className="text-xs text-destructive">
          {t("range.invalid")}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)}>
          {t("common.cancel")}
        </Button>
        <Button type="submit" size="sm">
          {t("common.apply")}
        </Button>
      </div>
    </form>
  );

  return (
    <div className={cn("relative flex min-w-0 items-center gap-1", className)}>
      <label htmlFor={`${id}-select`} className="sr-only">
        {t("range.label")}
      </label>
      <NativeSelect
        id={`${id}-select`}
        value={active}
        className="h-9 w-[7.5rem] lg:hidden"
        onChange={(e) => {
          const v = e.target.value;
          if (v === "custom") openCustom(false);
          else {
            setOpen(false);
            onChange({ range: v as (typeof PRESET_RANGES)[number] });
          }
        }}
      >
        {PRESET_RANGES.map((r) => (
          <option key={r} value={r}>
            {t(`range.${r}`)}
          </option>
        ))}
        <option value="custom">{t("range.custom")}</option>
      </NativeSelect>
      {custom && (
        <Button type="button" variant="ghost" size="icon" className="lg:hidden" aria-label={t("range.editCustom")} aria-expanded={open} onClick={() => openCustom(false)}>
          <CalendarClock aria-hidden="true" />
        </Button>
      )}
      <div role="group" aria-label={t("range.label")} className="hidden rounded-md border bg-background p-0.5 whitespace-nowrap lg:inline-flex">
        {PRESET_RANGES.map((r) => (
          <button
            key={r}
            type="button"
            aria-pressed={active === r}
            onClick={() => {
              setOpen(false);
              onChange({ range: r });
            }}
            className={cn(
              "rounded px-2 py-1 text-xs font-medium text-muted-foreground hover:text-foreground pointer-coarse:px-2.5 pointer-coarse:py-2.5",
              active === r && "bg-primary text-primary-foreground hover:text-primary-foreground",
            )}
          >
            {t(`range.${r}`)}
          </button>
        ))}
        <button
          type="button"
          aria-pressed={active === "custom"}
          aria-expanded={open}
          aria-controls={`${id}-custom`}
          onClick={() => openCustom()}
          className={cn(
            "inline-flex items-center gap-1 rounded px-2 py-1 text-xs font-medium text-muted-foreground hover:text-foreground pointer-coarse:px-2.5 pointer-coarse:py-2.5",
            active === "custom" && "bg-primary text-primary-foreground hover:text-primary-foreground",
          )}
        >
          <CalendarClock className="size-3.5" aria-hidden="true" />
          {t("range.custom")}
        </button>
      </div>
      {mobile ? (
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="bottom" title={t("range.custom")} closeLabel={t("common.close")}>
            {form}
          </SheetContent>
        </Sheet>
      ) : (
        open && <div className="absolute top-full right-0">{form}</div>
      )}
    </div>
  );
}
