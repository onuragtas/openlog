// CodeMirror 6 setup for OQL, loaded lazily by OqlEditor (this module and @codemirror/* stay out of the main chunk).
// Highlighting reuses the tokenizer of lib/oql.ts; completion reads the schema snapshot the React component keeps
// up to date (GET /api/v1/query/schema); diagnostics come from POST /api/v1/query/validate.
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap, type Completion, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { bracketMatching, HighlightStyle, StreamLanguage, syntaxHighlighting } from "@codemirror/language";
import { setDiagnostics, type Diagnostic } from "@codemirror/lint";
import { Compartment, EditorState, Prec } from "@codemirror/state";
import { EditorView, keymap, placeholder as placeholderExt } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import type { OqlSchema } from "@/api/oql";
import { completionContext, matchToken, OQL_EVENT_TYPES, OQL_FUNCTIONS, OQL_KEYWORDS, type OqlTokenType } from "@/lib/oql";

const TOKEN_NAMES: Record<OqlTokenType, string | null> = {
  keyword: "oqlKeyword",
  function: "oqlFunction",
  unit: "oqlUnit",
  identifier: "oqlAttribute",
  backtick: "oqlBacktick",
  string: "oqlString",
  number: "oqlNumber",
  variable: "oqlVariable",
  comment: "oqlComment",
  operator: "oqlOperator",
  punctuation: "oqlPunctuation",
  whitespace: null,
  invalid: "oqlInvalid",
};

const oqlLanguage = StreamLanguage.define<null>({
  name: "oql",
  startState: () => null,
  token(stream) {
    const { type, end } = matchToken(stream.string, stream.pos);
    stream.pos = Math.max(end, stream.pos + 1);
    return TOKEN_NAMES[type];
  },
  tokenTable: {
    oqlKeyword: tags.keyword,
    oqlFunction: tags.function(tags.variableName),
    oqlUnit: tags.unit,
    oqlAttribute: tags.propertyName,
    oqlBacktick: tags.special(tags.propertyName),
    oqlString: tags.string,
    oqlNumber: tags.number,
    oqlVariable: tags.special(tags.variableName),
    oqlComment: tags.lineComment,
    oqlOperator: tags.operator,
    oqlPunctuation: tags.punctuation,
    oqlInvalid: tags.invalid,
  },
});

function highlight(dark: boolean) {
  const c = dark
    ? { kw: "#a78bfa", fn: "#60a5fa", str: "#4ade80", num: "#fbbf24", com: "#9ca3af", v: "#f472b6", bt: "#22d3ee", bad: "#f87171", op: "#cbd5e1" }
    : { kw: "#7c3aed", fn: "#2563eb", str: "#15803d", num: "#b45309", com: "#6b7280", v: "#be185d", bt: "#0e7490", bad: "#dc2626", op: "#475569" };
  return HighlightStyle.define([
    { tag: tags.keyword, color: c.kw, fontWeight: "600" },
    { tag: tags.function(tags.variableName), color: c.fn },
    { tag: [tags.string], color: c.str },
    { tag: [tags.number, tags.unit], color: c.num },
    { tag: tags.lineComment, color: c.com, fontStyle: "italic" },
    { tag: tags.special(tags.variableName), color: c.v, fontWeight: "600" },
    { tag: tags.special(tags.propertyName), color: c.bt },
    { tag: [tags.operator, tags.punctuation], color: c.op },
    { tag: tags.invalid, color: c.bad, textDecoration: "underline wavy" },
  ]);
}

function theme(dark: boolean, minHeight: string) {
  return EditorView.theme(
    {
      "&": {
        fontSize: "13px",
        backgroundColor: "var(--background)",
        color: "var(--foreground)",
        border: "1px solid var(--input)",
        borderRadius: "calc(var(--radius) - 2px)",
      },
      "&.cm-focused": { outline: "2px solid var(--ring)", outlineOffset: "1px" },
      ".cm-scroller": { fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", maxHeight: "20rem", overflow: "auto" },
      ".cm-content": { minHeight, padding: "6px 0", caretColor: "var(--foreground)" },
      ".cm-line": { padding: "0 10px" },
      ".cm-placeholder": { color: "var(--muted-foreground)" },
      "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": { backgroundColor: dark ? "rgba(96,165,250,0.3)" : "rgba(37,99,235,0.18)" },
      ".cm-cursor": { borderLeftColor: "var(--foreground)" },
      ".cm-tooltip": { backgroundColor: "var(--card)", color: "var(--card-foreground)", border: "1px solid var(--border)", borderRadius: "6px" },
      ".cm-tooltip-autocomplete > ul > li[aria-selected]": { backgroundColor: "var(--accent)", color: "var(--accent-foreground)" },
      ".cm-completionDetail": { color: "var(--muted-foreground)", fontStyle: "normal", marginLeft: "0.75em" },
      ".cm-diagnostic-error": { borderLeftColor: "var(--destructive)" },
    },
    { dark },
  );
}

export interface SchemaSnapshot {
  base: OqlSchema | undefined;
  eventType: string | null;
  /** Schema fetched with ?event_type= (attribute keys, metric names). */
  detail: OqlSchema | undefined;
}

const IDENT = /^[A-Za-z_][A-Za-z0-9_.]*$/;

/** Completion options for a position (exported for reuse; pure given the snapshot). */
export function completionOptions(text: string, pos: number, explicit: boolean, s: SchemaSnapshot): CompletionResult | null {
  const c = completionContext(text, pos);
  if (!c || (!explicit && c.prefix === "")) return null;
  let options: Completion[] = [];
  switch (c.kind) {
    case "eventType":
      options = (s.base?.event_types.map((e) => ({ name: e.name, description: e.description })) ?? OQL_EVENT_TYPES.map((name) => ({ name, description: "" }))).map((e) => ({
        label: e.name,
        type: "class",
        detail: e.description,
      }));
      break;
    case "function":
      options = [
        ...(s.base?.functions ?? OQL_FUNCTIONS.map((name) => ({ name, signature: `${name}()`, description: "" }))).map((f) => ({
          label: f.name,
          apply: `${f.name}(`,
          type: "function",
          detail: f.signature,
          info: f.description || undefined,
        })),
        { label: "FROM", type: "keyword" },
      ];
      break;
    case "attribute": {
      const et = s.base?.event_types.find((e) => e.name === s.eventType);
      const attrs: Completion[] = (et?.attributes ?? []).map((a) => ({ label: a.name, type: "property", detail: a.type }));
      const keys: Completion[] = (s.detail?.attribute_keys ?? []).map((k) => ({ label: k, apply: IDENT.test(k) ? k : `attributes['${k.replace(/'/g, "\\'")}']`, type: "variable", detail: "attributes" }));
      const res: Completion[] = (s.detail?.resource_keys ?? []).map((k) => ({ label: `resource.${k}`, apply: IDENT.test(k) ? `resource.${k}` : `resource['${k}']`, type: "variable", detail: "resource" }));
      options = [...attrs, ...keys, ...res];
      if (s.eventType === "Metric" && /metricName\s*(=|!=|<>|IN\s*\()\s*$/i.test(text.slice(0, c.from))) {
        options = (s.detail?.metric_names ?? []).map((m) => ({ label: m, apply: `'${m}'`, type: "constant", detail: "metric" }));
      }
      break;
    }
    case "keyword":
      options = [...OQL_KEYWORDS.filter((k) => k !== "WITH").map((k) => ({ label: k, type: "keyword" })), { label: "COMPARE WITH", type: "keyword" }];
      break;
  }
  return { from: c.from, options, validFor: IDENT };
}

export interface OqlEditorController {
  setValue(value: string): void;
  setDark(dark: boolean): void;
  setDiagnostics(list: Diagnostic[]): void;
  focus(): void;
  destroy(): void;
}

export interface CreateOqlEditorOptions {
  parent: HTMLElement;
  doc: string;
  dark: boolean;
  label: string;
  id?: string;
  describedBy?: string;
  placeholder?: string;
  minHeight: string;
  onChange: (value: string) => void;
  onRun: () => void;
  getSchema: () => SchemaSnapshot;
}

export function createOqlEditor(o: CreateOqlEditorOptions): OqlEditorController {
  const themeSlot = new Compartment();
  let external = false;
  let dark = o.dark;
  const attrs: Record<string, string> = { "aria-label": o.label, "aria-multiline": "true", spellcheck: "false", autocapitalize: "off", autocorrect: "off" };
  if (o.id) attrs.id = o.id;
  if (o.describedBy) attrs["aria-describedby"] = o.describedBy;
  const view = new EditorView({
    parent: o.parent,
    state: EditorState.create({
      doc: o.doc,
      extensions: [
        Prec.highest(keymap.of([{ key: "Mod-Enter", run: () => (o.onRun(), true) }])),
        history(),
        closeBrackets(),
        bracketMatching(),
        oqlLanguage,
        autocompletion({ override: [(ctx: CompletionContext) => completionOptions(ctx.state.doc.toString(), ctx.pos, ctx.explicit, o.getSchema())], icons: false }),
        keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, ...completionKeymap]),
        EditorView.lineWrapping,
        EditorView.contentAttributes.of(attrs),
        o.placeholder ? placeholderExt(o.placeholder) : [],
        themeSlot.of([theme(dark, o.minHeight), syntaxHighlighting(highlight(dark))]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged && !external) o.onChange(u.state.doc.toString());
        }),
      ],
    }),
  });
  return {
    setValue(value) {
      const cur = view.state.doc.toString();
      if (cur === value) return;
      external = true;
      try {
        view.dispatch({ changes: { from: 0, to: cur.length, insert: value } });
      } finally {
        external = false;
      }
    },
    setDark(next) {
      if (next === dark) return;
      dark = next;
      view.dispatch({ effects: themeSlot.reconfigure([theme(dark, o.minHeight), syntaxHighlighting(highlight(dark))]) });
    },
    setDiagnostics(list) {
      const len = view.state.doc.length;
      const clamped = list.map((d) => ({ ...d, from: Math.min(d.from, len), to: Math.min(Math.max(d.to, d.from), len) }));
      view.dispatch(setDiagnostics(view.state, clamped));
    },
    focus: () => view.focus(),
    destroy: () => view.destroy(),
  };
}
