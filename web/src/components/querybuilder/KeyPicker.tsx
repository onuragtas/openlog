// Searchable key list in a popover (GET /fields/keys, or a given key list): column picker, histogram group-by and
// metric group-by. The input is a combobox; arrows move, Enter picks, a typed key that is not listed can be used too.
import { useQuery } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { Popover } from "radix-ui";
import { useId, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { fieldKeysQuery, type FieldKey, type FieldSignal } from "@/api/explorer";
import { Button } from "@/components/ui/button";
import type { RangeSpec } from "@/lib/time";
import { useDebounced } from "@/lib/use-debounced";
import { cn } from "@/lib/utils";
import { FieldTypeBadge } from "./QueryBuilder";

export interface KeyPickerProps {
  signal: FieldSignal;
  range: RangeSpec;
  metric?: string;
  /** Fixed keys (e.g. a metric's attribute keys) instead of querying GET /fields/keys. */
  keys?: FieldKey[];
  /** Trigger content and accessible name. */
  children: ReactNode;
  label: string;
  /** Keys shown as checked. */
  selected?: readonly string[];
  onSelect: (key: string) => void;
  /** Keep the list open after picking (multi-select). */
  multiple?: boolean;
  disabled?: boolean;
  className?: string;
}

export function KeyPicker({ children, label, className, disabled, ...props }: KeyPickerProps) {
  const [open, setOpen] = useState(false);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <Button type="button" variant="outline" size="sm" className={className} aria-label={label} title={label} disabled={disabled}>
          {children}
        </Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="start" sideOffset={4} collisionPadding={8} className="z-50 w-[min(24rem,calc(100vw-1rem))] rounded-lg border bg-card p-2 text-card-foreground shadow-lg">
          {open && <KeySearch {...props} label={label} onPicked={() => !props.multiple && setOpen(false)} />}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function KeySearch({ signal, range, metric, keys: fixed, label, selected = [], onSelect, onPicked }: Omit<KeyPickerProps, "children" | "className" | "disabled"> & { onPicked: () => void }) {
  const { t, i18n } = useTranslation();
  const listId = useId();
  const [text, setText] = useState("");
  const [active, setActive] = useState(0);
  const q = useDebounced(text.trim(), 200);
  const remote = useQuery({ ...fieldKeysQuery({ signal, range, metric, q, limit: 200 }), enabled: !fixed });
  const lower = text.trim().toLowerCase();
  const all = fixed ?? remote.data?.keys ?? [];
  const options: { key: string; field?: FieldKey }[] = all.filter((k) => k.key.toLowerCase().includes(lower)).slice(0, 100).map((k) => ({ key: k.key, field: k }));
  if (text.trim() && /^[^\s=!<>(),"'`~]+$/.test(text.trim()) && !options.some((o) => o.key === text.trim())) options.push({ key: text.trim() });
  const activeIndex = Math.min(active, options.length - 1);

  const pick = (key: string) => {
    onSelect(key);
    onPicked();
  };

  return (
    <div className="flex flex-col gap-1.5">
      <input
        role="combobox"
        aria-label={label}
        aria-expanded="true"
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={activeIndex >= 0 ? `${listId}-${activeIndex}` : undefined}
        value={text}
        placeholder={t("queryBuilder.searchKeys")}
        spellCheck={false}
        autoCapitalize="off"
        autoComplete="off"
        onChange={(e) => {
          setText(e.target.value);
          setActive(0);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            if (options.length) setActive((activeIndex + (e.key === "ArrowDown" ? 1 : -1) + options.length) % options.length);
          } else if (e.key === "Enter") {
            e.preventDefault();
            const o = options[activeIndex];
            if (o) pick(o.key);
          }
        }}
        className="h-8 w-full rounded-md border border-input bg-background px-2 font-mono text-sm outline-none placeholder:font-sans focus-visible:ring-2 focus-visible:ring-ring/40 pointer-coarse:h-10 pointer-coarse:text-base"
      />
      <ul id={listId} role="listbox" aria-label={label} aria-multiselectable={selected.length > 0 || undefined} className="max-h-72 overflow-y-auto overscroll-contain">
        {options.map((o, i) => {
          const checked = selected.includes(o.key);
          return (
            <li
              key={o.key}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={checked}
              onMouseDown={(e) => e.preventDefault()}
              onMouseMove={i === activeIndex ? undefined : () => setActive(i)}
              onClick={() => pick(o.key)}
              className={cn("flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 pointer-coarse:py-2.5", i === activeIndex && "bg-accent text-accent-foreground")}
            >
              <Check className={cn("size-3.5 shrink-0", checked ? "text-primary" : "invisible")} aria-hidden="true" />
              {o.field ? (
                <>
                  <span className="min-w-0 flex-1 truncate font-mono text-xs">{o.key}</span>
                  {o.field.count !== null && <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{o.field.count.toLocaleString(i18n.resolvedLanguage)}</span>}
                  <FieldTypeBadge type={o.field.type} />
                </>
              ) : (
                <span className="truncate text-xs">{t("queryBuilder.useKey", { key: o.key })}</span>
              )}
            </li>
          );
        })}
        {options.length === 0 && <li className="px-2 py-2 text-xs text-muted-foreground">{!fixed && remote.isFetching ? t("common.loading") : t("queryBuilder.noSuggestions")}</li>}
      </ul>
    </div>
  );
}
