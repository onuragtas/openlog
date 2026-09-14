import { useQuery } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { Popover } from "radix-ui";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { DashboardVariable } from "@/api/dashboards";
import { oqlQuery } from "@/api/oql";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { isAll, selectedValues, variableOptions, type VarValues } from "@/lib/dashboards";
import type { RangeSpec } from "@/lib/time";

export interface VariablesBarProps {
  variables: DashboardVariable[];
  vars: VarValues | undefined;
  range: RangeSpec;
  onChange: (vars: VarValues) => void;
}

/** One control per dashboard variable; selections live in the URL (`vars`). */
export function VariablesBar({ variables, vars, range, onChange }: VariablesBarProps) {
  const { t } = useTranslation();
  if (variables.length === 0) return null;
  const set = (name: string, values: string[]) => onChange({ ...(vars ?? {}), [name]: values });
  return (
    <div role="group" aria-label={t("dashboards.variables.bar")} className="flex flex-wrap items-end gap-3" data-testid="variables-bar">
      {variables.map((v) => (
        <VariableControl key={v.name} variable={v} values={selectedValues(v, vars)} range={range} onChange={(values) => set(v.name, values)} />
      ))}
    </div>
  );
}

function VariableControl({ variable: v, values, range, onChange }: { variable: DashboardVariable; values: string[]; range: RangeSpec; onChange: (values: string[]) => void }) {
  const { t } = useTranslation();
  const id = useId();
  const label = v.label || v.name;
  const q = useQuery({ ...oqlQuery({ query: v.query, range }), enabled: v.type === "query" && v.query.trim() !== "" });
  const options = v.type === "query" ? variableOptions(q.data?.rows ?? []) : v.values;
  const all = isAll(values);
  const extra = values.filter((x) => x !== "*" && !options.includes(x));
  const choices = [...options, ...extra];

  if (v.type === "text") {
    return (
      <div className="flex min-w-0 flex-col gap-1">
        <label htmlFor={id} className="text-xs font-medium text-muted-foreground">
          {label}
        </label>
        <TextVariable id={id} value={values[0] === "*" ? "" : (values[0] ?? "")} onCommit={(s) => onChange(s ? [s] : [])} />
      </div>
    );
  }

  if (!v.multi) {
    return (
      <div className="flex min-w-0 flex-col gap-1">
        <label htmlFor={id} className="text-xs font-medium text-muted-foreground">
          {label}
        </label>
        <NativeSelect id={id} className="max-w-60" value={all ? "*" : values[0]} onChange={(e) => onChange([e.target.value])}>
          {(v.include_all || all) && <option value="*">{t("common.all")}</option>}
          {choices.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </NativeSelect>
      </div>
    );
  }

  const summary = all ? t("common.all") : values.length === 1 ? values[0] : t("dashboards.variables.selected", { count: values.length });
  const toggle = (o: string) => {
    const cur = all ? [] : values;
    const next = cur.includes(o) ? cur.filter((x) => x !== o) : [...cur, o];
    onChange(next.length === 0 && v.include_all ? ["*"] : next);
  };
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span id={id} className="text-xs font-medium text-muted-foreground">
        {label}
      </span>
      <Popover.Root>
        <Popover.Trigger asChild>
          <Button variant="outline" className="max-w-60 justify-between font-normal" aria-labelledby={`${id} ${id}-value`}>
            <span id={`${id}-value`} className="truncate">
              {summary}
            </span>
            <ChevronDown aria-hidden="true" />
          </Button>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content align="start" sideOffset={4} collisionPadding={8} className="z-50 flex max-h-72 w-64 max-w-[calc(100vw-1rem)] flex-col gap-0.5 overflow-y-auto rounded-lg border bg-card p-2 text-sm shadow-lg">
            <div role="group" aria-label={label} className="flex flex-col gap-0.5">
              {v.include_all && (
                <label className="flex min-h-8 items-center gap-2 rounded px-1 hover:bg-accent">
                  <input type="checkbox" checked={all} onChange={() => onChange(["*"])} />
                  {t("common.all")}
                </label>
              )}
              {q.isPending && v.type === "query" ? <span className="px-1 text-muted-foreground">{t("common.loading")}</span> : null}
              {choices.map((o) => (
                <label key={o} className="flex min-h-8 items-center gap-2 rounded px-1 hover:bg-accent">
                  <input type="checkbox" checked={!all && values.includes(o)} onChange={() => toggle(o)} />
                  <span className="truncate">{o}</span>
                </label>
              ))}
            </div>
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
    </div>
  );
}

function TextVariable({ id, value, onCommit }: { id: string; value: string; onCommit: (v: string) => void }) {
  const [text, setText] = useState(value);
  const [synced, setSynced] = useState(value);
  if (value !== synced) {
    setSynced(value);
    setText(value);
  }
  return (
    <Input
      id={id}
      className="w-48"
      value={text}
      onChange={(e) => setText(e.target.value)}
      onBlur={() => text !== value && onCommit(text.trim())}
      onKeyDown={(e) => {
        if (e.key === "Enter") onCommit(text.trim());
      }}
    />
  );
}
