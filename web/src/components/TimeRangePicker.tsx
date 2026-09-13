import { CalendarClock } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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

export function TimeRangePicker({ value, onChange, className }: { value: RangeSpec; onChange: (spec: RangeSpec) => void; className?: string }) {
  const { t } = useTranslation();
  const id = useId();
  const custom = isCustomRange(value);
  const active = custom ? "custom" : (value.range ?? DEFAULT_RANGE);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState({ from: "", to: "" });
  const [invalid, setInvalid] = useState(false);

  const openCustom = () => {
    const r = resolveRange(value, Date.now());
    setDraft({ from: toDateTimeLocal(r.from), to: toDateTimeLocal(r.to) });
    setInvalid(false);
    setOpen((o) => !o);
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

  return (
    <div className={cn("relative", className)}>
      <div role="group" aria-label={t("range.label")} className="inline-flex rounded-md border bg-background p-0.5">
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
              "rounded px-2 py-1 text-xs font-medium text-muted-foreground hover:text-foreground",
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
          onClick={openCustom}
          className={cn(
            "inline-flex items-center gap-1 rounded px-2 py-1 text-xs font-medium text-muted-foreground hover:text-foreground",
            active === "custom" && "bg-primary text-primary-foreground hover:text-primary-foreground",
          )}
        >
          <CalendarClock className="size-3.5" aria-hidden="true" />
          {t("range.custom")}
        </button>
      </div>
      {open && (
        <form
          id={`${id}-custom`}
          onSubmit={apply}
          onKeyDown={(e) => e.key === "Escape" && setOpen(false)}
          className="absolute right-0 z-30 mt-2 flex w-72 flex-col gap-3 rounded-lg border bg-card p-3 shadow-lg"
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-from`}>{t("range.from")}</Label>
            <Input id={`${id}-from`} type="datetime-local" value={draft.from} autoFocus aria-invalid={invalid} onChange={(e) => setDraft((d) => ({ ...d, from: e.target.value }))} />
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
      )}
    </div>
  );
}
