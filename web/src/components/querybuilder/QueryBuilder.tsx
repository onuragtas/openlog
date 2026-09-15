// SigNoz-style filter bar for the explorer API (api.md "Fields"): type a key (suggestions from GET /fields/keys), pick an
// operator valid for its type, pick values (GET /fields/values with the other filters) and Enter commits a chip. Typed or
// pasted text such as `severity_text IN (ERROR, WARN)` is parsed into chips (lib/querybuilder.ts); other text becomes a
// body search. Chips are AND-ed within a lane; "+ OR" adds an alternative lane. Phones get the editor in a bottom sheet.
import { useQuery } from "@tanstack/react-query";
import { Filter, Loader2, Plus, Search, X } from "lucide-react";
import { useId, useRef, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { fieldKeysQuery, fieldValuesQuery, type FieldKey, type FieldSignal, type FieldSource, type FieldType, type FilterOp, type FilterState, type QueryFilter } from "@/api/explorer";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import {
  addOrGroup,
  conditionCount,
  filterValues,
  formatFilter,
  isFilterStateEmpty,
  isMultiValueOp,
  isNoValueOp,
  makeFilter,
  normalizeFilterState,
  OP_TEXT,
  opsForType,
  parseFilterText,
  QB_LIMITS,
} from "@/lib/querybuilder";
import { useIsMobile } from "@/lib/media";
import type { RangeSpec } from "@/lib/time";
import { useDebounced } from "@/lib/use-debounced";
import { cn } from "@/lib/utils";

export interface QueryBuilderProps {
  signal: FieldSignal;
  /** signal "metrics": suggestions for this metric only */
  metric?: string;
  value: FilterState;
  onChange: (value: FilterState) => void;
  /** Range of the key and value suggestions. */
  range: RangeSpec;
  /** Unparseable text becomes a body search (default: logs only). */
  allowText?: boolean;
  className?: string;
}

const OP_I18N = { "=": "eq", "!=": "neq", in: "in", not_in: "not_in", contains: "contains", not_contains: "not_contains", like: "like", not_like: "not_like", regex: "regex", not_regex: "not_regex", exists: "exists", not_exists: "not_exists", ">": "gt", ">=": "gte", "<": "lt", "<=": "lte" } as const satisfies Record<FilterOp, string>;
const SOURCE_RANK: Record<FieldSource, number> = { field: 0, attribute: 1, resource: 2, body: 3 };
const KEY_TEXT = /^[^\s=!<>(),"'`~]+$/;

/** Type badge of a key (suggestions, column picker). */
export function FieldTypeBadge({ type }: { type: FieldType }) {
  const { t } = useTranslation();
  return (
    <Badge variant="outline" className="px-1 py-0 font-mono text-[10px] text-muted-foreground uppercase">
      {t(`queryBuilder.types.${type}`)}
    </Badge>
  );
}

export function QueryBuilder(props: QueryBuilderProps) {
  const { t } = useTranslation();
  const mobile = useIsMobile();
  const [open, setOpen] = useState(false);
  if (!mobile) return <FilterEditor {...props} />;
  const count = conditionCount(props.value) + (props.value.q.trim() ? 1 : 0);
  return (
    <div className={props.className}>
      <Button type="button" variant="outline" className="w-full justify-start" aria-haspopup="dialog" onClick={() => setOpen(true)}>
        <Filter aria-hidden="true" />
        {count > 0 ? t("queryBuilder.filtersCount", { count }) : t("queryBuilder.addFilter")}
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="bottom" title={t("queryBuilder.title")} closeLabel={t("common.close")}>
          <div className="min-h-[50dvh] overflow-y-auto p-4">
            <FilterEditor {...props} className={undefined} />
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}

type LaneId = "filters" | "new" | number;

function FilterEditor({ signal, metric, value, onChange, range, allowText = signal === "logs", className }: QueryBuilderProps) {
  const { t } = useTranslation();
  const [orDraft, setOrDraft] = useState(false);
  const total = conditionCount(value);
  const remaining = QB_LIMITS.conditions - total;

  const lanes: { id: LaneId; conds: QueryFilter[] }[] = [];
  if (value.groups.length === 0 || value.filters.length > 0) lanes.push({ id: "filters", conds: value.filters });
  value.groups.forEach((g, i) => lanes.push({ id: i, conds: g }));
  if (orDraft) lanes.push({ id: "new", conds: [] });

  const update = (lane: LaneId, conds: QueryFilter[], q?: string) => {
    const base = { ...value, q: q ?? value.q };
    if (lane === "filters") onChange(normalizeFilterState({ ...base, filters: conds }));
    else if (lane === "new") {
      setOrDraft(false);
      onChange(addOrGroup(base, conds));
    } else onChange(normalizeFilterState({ ...base, groups: value.groups.map((g, i) => (i === lane ? conds : g)) }));
  };

  const canOr = !orDraft && total > 0 && value.groups.length < QB_LIMITS.groups && remaining > 0;
  const firstGroup = lanes.findIndex((l) => typeof l.id === "number" || l.id === "new");

  return (
    <div role="search" aria-label={t("queryBuilder.title")} className={cn("flex min-w-0 flex-col gap-1.5", className)} data-testid="query-builder">
      {lanes.map((lane, i) => (
        <div key={String(lane.id)} className="flex min-w-0 flex-col gap-1.5">
          {i > 0 && (
            <span className="pl-1 text-xs font-semibold text-muted-foreground" aria-hidden="true">
              {i === firstGroup && lanes[0]!.id === "filters" && value.groups.length > 0 ? t("queryBuilder.andOneOf") : t("queryBuilder.or")}
            </span>
          )}
          <Lane
            ctx={{ signal, metric, range, allowText, filters: value.filters }}
            label={lane.id === "filters" ? t("queryBuilder.title") : t("queryBuilder.orGroup", { n: typeof lane.id === "number" ? lane.id + 1 : Math.max(2, value.groups.length + 1) })}
            conds={lane.conds}
            q={lane.id === "filters" || (i === 0 && allowText) ? value.q : undefined}
            remaining={remaining}
            autoFocus={lane.id === "new"}
            onChange={(conds, q) => update(lane.id, conds, q)}
            onCancelEmpty={lane.id === "new" ? () => setOrDraft(false) : undefined}
          />
        </div>
      ))}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" variant="ghost" size="sm" disabled={!canOr} onClick={() => setOrDraft(true)} title={t("queryBuilder.addOrHint")}>
          <Plus aria-hidden="true" />
          {t("queryBuilder.addOr")}
        </Button>
        {!isFilterStateEmpty(value) && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => {
              setOrDraft(false);
              onChange({ filters: [], groups: [], q: "" });
            }}
          >
            <X aria-hidden="true" />
            {t("queryBuilder.clearAll")}
          </Button>
        )}
        {remaining <= 10 && (
          <span className="text-xs text-muted-foreground" role="status">
            {t("queryBuilder.conditionsLeft", { count: Math.max(0, remaining), max: QB_LIMITS.conditions })}
          </span>
        )}
      </div>
    </div>
  );
}

interface SuggestContext {
  signal: FieldSignal;
  metric?: string;
  range: RangeSpec;
  allowText: boolean;
  /** AND-ed filters sent with value suggestions. */
  filters: QueryFilter[];
}

type Editing = { index: number; filter: QueryFilter } | { q: string };

function Lane({
  ctx,
  label,
  conds,
  q,
  remaining,
  autoFocus,
  onChange,
  onCancelEmpty,
}: {
  ctx: SuggestContext;
  label: string;
  conds: QueryFilter[];
  /** body search shown in this lane (undefined: not this lane) */
  q?: string;
  remaining: number;
  autoFocus?: boolean;
  onChange: (conds: QueryFilter[], q?: string) => void;
  onCancelEmpty?: () => void;
}) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState<Editing | null>(null);
  const [editSeq, setEditSeq] = useState(0);
  const edit = (e: Editing | null) => {
    setEditing(e);
    setEditSeq((n) => n + 1);
  };
  const editIndex = editing && "index" in editing ? editing.index : -1;
  const editingQ = editing !== null && "q" in editing;

  const commit = (filters: QueryFilter[]) => {
    const next = [...conds];
    if (editIndex >= 0) next.splice(editIndex, 1, ...filters);
    else next.push(...filters);
    onChange(next, editingQ ? "" : undefined);
    edit(null);
  };

  return (
    <div role="group" aria-label={label} className="flex min-w-0 flex-wrap items-center gap-1 rounded-md border border-input bg-background px-1.5 py-1 shadow-xs focus-within:ring-2 focus-within:ring-ring/40">
      {q && !editingQ && (
        <span className="inline-flex max-w-full min-w-0 items-center rounded-md border bg-muted/60 text-xs">
          <button type="button" className="inline-flex min-w-0 items-center gap-1 px-2 py-1 pointer-coarse:py-2" onClick={() => edit({ q })} aria-label={t("queryBuilder.editSearch", { text: q })}>
            <Search className="size-3 shrink-0 text-muted-foreground" aria-hidden="true" />
            <span className="truncate font-mono">“{q}”</span>
          </button>
          <button type="button" className="rounded px-1 py-1 text-muted-foreground hover:text-foreground pointer-coarse:px-2 pointer-coarse:py-2" onClick={() => onChange(conds, "")} aria-label={t("queryBuilder.removeSearch", { text: q })}>
            <X className="size-3" aria-hidden="true" />
          </button>
        </span>
      )}
      {conds.map((f, i) => (i === editIndex ? null : <Chip key={`${i}|${formatFilter(f)}`} filter={f} onEdit={() => edit({ index: i, filter: f })} onRemove={() => onChange(conds.filter((_, j) => j !== i))} />))}
      <ChipInput
        key={editSeq}
        ctx={ctx}
        editing={editing}
        remaining={remaining + (editIndex >= 0 ? 1 : 0)}
        autoFocus={autoFocus || editing !== null}
        ariaLabel={label}
        placeholder={conds.length === 0 && !q ? (ctx.allowText ? t("queryBuilder.placeholderLogs") : t("queryBuilder.placeholder")) : undefined}
        onCommit={commit}
        onText={(text) => {
          onChange(conds, text);
          edit(null);
        }}
        onCancel={() => {
          if (editing) edit(null);
          else if (conds.length === 0) onCancelEmpty?.();
        }}
        onBackspaceEmpty={() => {
          if (conds.length > 0) edit({ index: conds.length - 1, filter: conds[conds.length - 1]! });
          else if (q) edit({ q });
        }}
      />
    </div>
  );
}

function Chip({ filter, onEdit, onRemove }: { filter: QueryFilter; onEdit: () => void; onRemove: () => void }) {
  const { t } = useTranslation();
  const text = formatFilter(filter);
  const values = filterValues(filter);
  return (
    <span className="inline-flex max-w-full min-w-0 items-center rounded-md border bg-muted/60 text-xs" data-testid="filter-chip">
      <button type="button" className="inline-flex min-w-0 items-center gap-1 px-2 py-1 font-mono pointer-coarse:py-2" onClick={onEdit} title={text} aria-label={t("queryBuilder.editCondition", { condition: text })}>
        <span className="truncate text-muted-foreground">{filter.key}</span>
        <span className="shrink-0 font-semibold text-primary">{OP_TEXT[filter.op]}</span>
        {values.length > 0 && <span className="truncate">{isMultiValueOp(filter.op) ? `(${values.join(", ")})` : values[0]}</span>}
      </button>
      <button type="button" className="rounded px-1 py-1 text-muted-foreground hover:text-foreground pointer-coarse:px-2 pointer-coarse:py-2" onClick={onRemove} aria-label={t("queryBuilder.removeCondition", { condition: text })}>
        <X className="size-3" aria-hidden="true" />
      </button>
    </span>
  );
}

interface Draft {
  key: string;
  type?: FieldType;
  op: FilterOp | null;
  values: string[];
}

type Option =
  | { kind: "parsed"; filters: QueryFilter[] }
  | { kind: "key"; key: FieldKey }
  | { kind: "typedKey"; key: string }
  | { kind: "text"; q: string }
  | { kind: "op"; op: FilterOp }
  | { kind: "value"; value: string; count?: number }
  | { kind: "apply" };

function draftOf(editing: Editing | null): { draft: Draft | null; text: string } {
  if (!editing) return { draft: null, text: "" };
  if ("q" in editing) return { draft: null, text: editing.q };
  const f = editing.filter;
  if (isMultiValueOp(f.op)) return { draft: { key: f.key, op: f.op, values: filterValues(f) }, text: "" };
  if (isNoValueOp(f.op)) return { draft: { key: f.key, op: null, values: [] }, text: "" };
  return { draft: { key: f.key, op: f.op, values: [] }, text: filterValues(f)[0] ?? "" };
}

function ChipInput({
  ctx,
  editing,
  remaining,
  autoFocus,
  ariaLabel,
  placeholder,
  onCommit,
  onText,
  onCancel,
  onBackspaceEmpty,
}: {
  ctx: SuggestContext;
  editing: Editing | null;
  remaining: number;
  autoFocus?: boolean;
  ariaLabel: string;
  placeholder?: string;
  onCommit: (filters: QueryFilter[]) => void;
  onText: (q: string) => void;
  onCancel: () => void;
  onBackspaceEmpty: () => void;
}) {
  const { t } = useTranslation();
  const listId = useId();
  const containerRef = useRef<HTMLDivElement>(null);
  const [draft, setDraft] = useState<Draft | null>(() => draftOf(editing).draft);
  const [text, setText] = useState(() => draftOf(editing).text);
  const [open, setOpen] = useState(editing !== null);
  const [active, setActive] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const debounced = useDebounced(text.trim(), 200);
  const stage = !draft ? "key" : !draft.op ? "op" : "value";

  const keys = useQuery({ ...fieldKeysQuery({ signal: ctx.signal, range: ctx.range, metric: ctx.metric, q: stage === "key" ? debounced : "", limit: 200 }), enabled: open });
  const keyType = draft ? (draft.type ?? keys.data?.keys.find((k) => k.key === draft.key)?.type) : undefined;
  const values = useQuery(
    fieldValuesQuery({
      signal: ctx.signal,
      key: draft?.key ?? "",
      range: ctx.range,
      metric: ctx.metric,
      q: debounced,
      filters: ctx.filters.filter((f) => f.key !== draft?.key),
      enabled: open && stage === "value",
    }),
  );

  const lower = text.trim().toLowerCase();
  const options: Option[] = [];
  if (stage === "key") {
    const parsed = text.trim() ? parseFilterText(text) : null;
    if (parsed?.kind === "filters") options.push({ kind: "parsed", filters: parsed.filters });
    const matching = (keys.data?.keys ?? [])
      .filter((k) => k.key.toLowerCase().includes(lower))
      .sort((a, b) => SOURCE_RANK[a.source] - SOURCE_RANK[b.source])
      .slice(0, 80);
    options.push(...matching.map((key): Option => ({ kind: "key", key })));
    if (text.trim() && KEY_TEXT.test(text.trim()) && !matching.some((k) => k.key === text.trim())) options.push({ kind: "typedKey", key: text.trim() });
    if (parsed?.kind === "text" && ctx.allowText) options.push({ kind: "text", q: parsed.q });
  } else if (stage === "op" && draft) {
    const parsed = text.trim() && !draft.key.includes("`") ? parseFilterText(`\`${draft.key}\` ${text}`) : null;
    if (parsed?.kind === "filters") options.push({ kind: "parsed", filters: parsed.filters });
    options.push(
      ...opsForType(keyType)
        .filter((op) => !lower || OP_TEXT[op].toLowerCase().startsWith(lower) || op.startsWith(lower))
        .map((op): Option => ({ kind: "op", op })),
    );
  } else if (draft?.op) {
    if (isMultiValueOp(draft.op) && draft.values.length > 0) options.push({ kind: "apply" });
    const suggested = values.data?.values ?? [];
    options.push(...suggested.map((v): Option => ({ kind: "value", value: v.value, count: v.count })));
    if (text.trim() && !suggested.some((v) => v.value === text.trim())) options.push({ kind: "value", value: text.trim() });
  }
  const activeIndex = Math.min(active, options.length - 1);
  const loading = stage === "key" ? keys.isFetching && !keys.data : stage === "value" ? values.isFetching && !values.data : false;

  const reset = () => {
    setDraft(null);
    setText("");
    setActive(0);
    setError(null);
  };

  const commit = (filters: QueryFilter[]) => {
    if (filters.length > remaining) {
      setError(t("queryBuilder.limit", { max: QB_LIMITS.conditions }));
      return;
    }
    onCommit(filters);
    reset();
  };

  const choose = (o: Option) => {
    setError(null);
    setActive(0);
    switch (o.kind) {
      case "parsed":
        return commit(o.filters);
      case "key":
        setDraft({ key: o.key.key, type: o.key.type, op: null, values: [] });
        return setText("");
      case "typedKey":
        setDraft({ key: o.key, op: null, values: [] });
        return setText("");
      case "text":
        onText(o.q);
        return reset();
      case "op":
        if (!draft) return;
        if (isNoValueOp(o.op)) return commit([{ key: draft.key, op: o.op }]);
        setDraft({ ...draft, op: o.op, values: [] });
        return setText("");
      case "value":
        if (!draft?.op) return;
        if (isMultiValueOp(draft.op)) {
          const has = draft.values.includes(o.value);
          if (!has && draft.values.length >= QB_LIMITS.values) return setError(t("queryBuilder.valuesLimit", { max: QB_LIMITS.values }));
          setDraft({ ...draft, values: has ? draft.values.filter((v) => v !== o.value) : [...draft.values, o.value] });
          return setText("");
        }
        return commit([makeFilter(draft.key, draft.op, [o.value])]);
      case "apply":
        if (draft?.op) commit([makeFilter(draft.key, draft.op, draft.values)]);
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) return setOpen(true);
      if (options.length === 0) return;
      const d = e.key === "ArrowDown" ? 1 : -1;
      setActive((activeIndex + d + options.length) % options.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const o = open ? options[activeIndex] : undefined;
      if (o) return choose(o);
      if (stage === "key" && text.trim()) {
        const parsed = parseFilterText(text);
        if (parsed?.kind === "filters") commit(parsed.filters);
        else if (parsed && ctx.allowText) {
          onText(parsed.q);
          reset();
        } else setError(t("queryBuilder.notACondition"));
      } else if (stage === "value" && draft?.op && isMultiValueOp(draft.op) && draft.values.length > 0) {
        commit([makeFilter(draft.key, draft.op, draft.values)]);
      }
      setOpen(true);
    } else if (e.key === "Backspace" && text === "") {
      if (stage === "value" && draft) {
        e.preventDefault();
        if (draft.values.length > 0) setDraft({ ...draft, values: draft.values.slice(0, -1) });
        else setDraft({ ...draft, op: null });
      } else if (stage === "op" && draft) {
        e.preventDefault();
        setText(draft.key);
        setDraft(null);
      } else if (stage === "key") {
        onBackspaceEmpty();
      }
    } else if (e.key === "Escape") {
      if (open) setOpen(false);
      else if (draft || text) reset();
      else onCancel();
    }
  };

  const hint =
    stage === "key"
      ? t("queryBuilder.hintKey")
      : stage === "op"
        ? t("queryBuilder.hintOp", { name: draft?.key ?? "" })
        : draft?.op && isMultiValueOp(draft.op)
          ? t("queryBuilder.hintValues")
          : t("queryBuilder.hintValue");

  let lastSource: FieldSource | null = null;
  return (
    <div
      ref={containerRef}
      className="relative flex min-w-[min(14rem,100%)] flex-1 flex-wrap items-center gap-1"
      onBlur={(e) => {
        if (!containerRef.current?.contains(e.relatedTarget as Node | null)) {
          setOpen(false);
          if (editing && !draft && !text) onCancel();
        }
      }}
    >
      {draft && (
        <span className="inline-flex max-w-full min-w-0 items-center gap-1 rounded-md border border-dashed border-primary/60 px-2 py-1 font-mono text-xs" data-testid="filter-draft">
          <span className="truncate text-muted-foreground">{draft.key}</span>
          {draft.op && <span className="font-semibold text-primary">{OP_TEXT[draft.op]}</span>}
          {draft.values.length > 0 && <span className="truncate">({draft.values.join(", ")})</span>}
        </span>
      )}
      <input
        role="combobox"
        aria-label={ariaLabel}
        aria-expanded={open}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open && activeIndex >= 0 ? `${listId}-${activeIndex}` : undefined}
        aria-invalid={error ? true : undefined}
        // Focus follows the chip being edited or the OR lane just added.
        autoFocus={autoFocus}
        value={text}
        placeholder={draft ? undefined : placeholder}
        spellCheck={false}
        autoCapitalize="off"
        autoComplete="off"
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
        onChange={(e) => {
          setText(e.target.value);
          setActive(0);
          setError(null);
          setOpen(true);
        }}
        onKeyDown={onKeyDown}
        className="h-7 min-w-24 flex-1 bg-transparent px-1 font-mono text-sm outline-none placeholder:font-sans placeholder:text-muted-foreground pointer-coarse:h-9 pointer-coarse:text-base"
      />
      {open && (
        <div className="absolute top-full left-0 z-30 mt-1.5 w-[min(30rem,calc(100vw-2.5rem))] overflow-hidden rounded-lg border bg-card text-card-foreground shadow-lg">
          <div className="flex items-center justify-between gap-2 border-b px-3 py-1.5 text-xs text-muted-foreground">
            <span className="truncate">{hint}</span>
            {loading && <Loader2 className="size-3.5 shrink-0 animate-spin" aria-label={t("common.loading")} />}
          </div>
          {error && (
            <p role="alert" className="border-b px-3 py-1.5 text-xs text-destructive-text">
              {error}
            </p>
          )}
          <ul id={listId} role="listbox" aria-label={hint} aria-multiselectable={draft?.op && isMultiValueOp(draft.op) ? true : undefined} className="max-h-72 overflow-y-auto overscroll-contain p-1">
            {options.map((o, i) => {
              const header = o.kind === "key" && o.key.source !== lastSource ? o.key.source : null;
              if (o.kind === "key") lastSource = o.key.source;
              const selected = o.kind === "value" && !!draft?.op && isMultiValueOp(draft.op) && draft.values.includes(o.value);
              return (
                <OptionRow
                  key={`${o.kind}|${i}`}
                  id={`${listId}-${i}`}
                  option={o}
                  header={header ? t(`queryBuilder.sources.${header}`) : null}
                  active={i === activeIndex}
                  selected={selected}
                  draft={draft}
                  onChoose={() => choose(o)}
                  onHover={() => setActive(i)}
                />
              );
            })}
            {options.length === 0 && !loading && <li className="px-2 py-2 text-xs text-muted-foreground">{stage === "value" ? t("queryBuilder.noValues") : t("queryBuilder.noSuggestions")}</li>}
          </ul>
          {(keys.data?.sampled || values.data?.sampled) && <p className="border-t px-3 py-1 text-[11px] text-muted-foreground">{t("queryBuilder.sampled")}</p>}
        </div>
      )}
    </div>
  );
}

function OptionRow({ id, option: o, header, active, selected, draft, onChoose, onHover }: { id: string; option: Option; header: string | null; active: boolean; selected: boolean; draft: Draft | null; onChoose: () => void; onHover: () => void }) {
  const { t, i18n } = useTranslation();
  const cls = cn("flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm pointer-coarse:py-2.5", active && "bg-accent text-accent-foreground");
  let body: React.ReactNode;
  switch (o.kind) {
    case "parsed":
      body = (
        <>
          <Filter className="size-3.5 shrink-0 text-primary" aria-hidden="true" />
          <span className="shrink-0 text-xs text-muted-foreground">{t("queryBuilder.applyCondition")}</span>
          <span className="truncate font-mono text-xs">{o.filters.map(formatFilter).join(" AND ")}</span>
        </>
      );
      break;
    case "key":
      body = (
        <>
          <span className="min-w-0 flex-1 truncate font-mono text-xs">{o.key.key}</span>
          {o.key.count !== null && <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{o.key.count.toLocaleString(i18n.resolvedLanguage)}</span>}
          <FieldTypeBadge type={o.key.type} />
        </>
      );
      break;
    case "typedKey":
      body = <span className="truncate text-xs">{t("queryBuilder.useKey", { key: o.key })}</span>;
      break;
    case "text":
      body = (
        <>
          <Search className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
          <span className="truncate text-xs">{t("queryBuilder.searchText", { text: o.q })}</span>
        </>
      );
      break;
    case "op":
      body = (
        <>
          <span className="w-24 shrink-0 font-mono text-xs font-semibold">{OP_TEXT[o.op]}</span>
          <span className="truncate text-xs text-muted-foreground">{t(`queryBuilder.ops.${OP_I18N[o.op]}`)}</span>
        </>
      );
      break;
    case "value": {
      const multi = !!draft?.op && isMultiValueOp(draft.op);
      body = (
        <>
          {multi && <span aria-hidden="true" className={cn("inline-flex size-3.5 shrink-0 items-center justify-center rounded-sm border", selected && "border-primary bg-primary text-primary-foreground")}>{selected ? "✓" : ""}</span>}
          <span className="min-w-0 flex-1 truncate font-mono text-xs">{o.value === "" ? <span className="text-muted-foreground italic">{t("queryBuilder.emptyValue")}</span> : o.value}</span>
          {o.count !== undefined && <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{o.count.toLocaleString(i18n.resolvedLanguage)}</span>}
        </>
      );
      break;
    }
    case "apply":
      body = <span className="text-xs font-medium text-primary">{t("queryBuilder.applyValues", { count: draft?.values.length ?? 0 })}</span>;
  }
  return (
    <>
      {header && (
        <li role="presentation" className="px-2 pt-2 pb-0.5 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
          {header}
        </li>
      )}
      <li
        id={id}
        role="option"
        aria-selected={o.kind === "value" && !!draft?.op && isMultiValueOp(draft.op) ? selected : active}
        className={cls}
        onMouseDown={(e) => e.preventDefault()}
        onMouseMove={active ? undefined : onHover}
        onClick={onChoose}
      >
        {body}
      </li>
    </>
  );
}
