import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { DURATION_UNITS, joinDuration, splitDuration, type DurationUnit } from "@/lib/alerts";
import { cn } from "@/lib/utils";
import { describedBy } from "./field-utils";

/** Label + control + hint/error; controls use describedBy() from field-utils.ts. */
export function Field({ id, label, hint, error, children, className }: { id: string; label: string; hint?: string; error?: string; children: ReactNode; className?: string }) {
  return (
    <div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error ? (
        <p id={`${id}-error`} className="text-xs text-destructive-text">
          {error}
        </p>
      ) : (
        hint && (
          <p id={`${id}-hint`} className="text-xs text-muted-foreground">
            {hint}
          </p>
        )
      )}
    </div>
  );
}

/** Number + unit (seconds, minutes, hours, days) editor for a duration in seconds. */
export function DurationField({
  id,
  label,
  seconds,
  onChange,
  error,
  hint,
  disabled,
}: {
  id: string;
  label: string;
  seconds: number;
  onChange: (seconds: number) => void;
  error?: string;
  hint?: string;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  const [unit, setUnit] = useState<DurationUnit>(() => splitDuration(seconds).unit);
  const [text, setText] = useState(() => String(splitDuration(seconds).value));
  const [synced, setSynced] = useState(seconds);
  // Follow external changes (type switch, prefill) without fighting the user's typing: adjust state while rendering.
  if (seconds !== synced) {
    setSynced(seconds);
    if (joinDuration(Number(text || 0), unit) !== seconds) {
      const s = splitDuration(seconds);
      setUnit(s.unit);
      setText(String(s.value));
    }
  }
  const emit = (value: string, u: DurationUnit) => {
    const next = joinDuration(Number(value || 0), u);
    setSynced(next);
    onChange(next);
  };
  return (
    <Field id={id} label={label} hint={hint} error={error}>
      <div className="flex gap-2">
        <Input
          id={id}
          type="number"
          min={0}
          step="any"
          inputMode="decimal"
          value={text}
          disabled={disabled}
          className="w-28"
          onChange={(e) => {
            setText(e.target.value);
            emit(e.target.value, unit);
          }}
          {...describedBy(id, error, hint)}
        />
        <NativeSelect
          aria-label={t("alerts.editor.unitOf", { field: label })}
          value={unit}
          disabled={disabled}
          onChange={(e) => {
            const u = e.target.value as DurationUnit;
            setUnit(u);
            emit(text, u);
          }}
        >
          {(Object.keys(DURATION_UNITS) as DurationUnit[]).map((u) => (
            <option key={u} value={u}>
              {t(`alerts.editor.units.${u}`)}
            </option>
          ))}
        </NativeSelect>
      </div>
    </Field>
  );
}

export function Section({ title, children, description }: { title: string; children: ReactNode; description?: string }) {
  const id = `section-${title.replace(/\W+/g, "-").toLowerCase()}`;
  return (
    <section aria-labelledby={id} className="flex flex-col gap-4 rounded-xl border bg-card p-4">
      <div>
        <h2 id={id} className="text-base font-semibold">
          {title}
        </h2>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      {children}
    </section>
  );
}
