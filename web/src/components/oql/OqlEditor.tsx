import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { oqlSchemaQuery, oqlValidateQuery, type OqlEventType, type OqlVariables } from "@/api/oql";
import { canonicalEventType, eventTypeOf, offsetOf } from "@/lib/oql";
import { useTheme } from "@/lib/theme";
import { useDebounced } from "@/lib/use-debounced";
import { cn } from "@/lib/utils";
import type { OqlEditorController, SchemaSnapshot } from "./codemirror";

type CodeMirrorModule = typeof import("./codemirror");

/** jsdom (Vitest) cannot lay out CodeMirror; tests drive the textarea fallback. */
const canLoadCodeMirror = () => typeof navigator === "undefined" || !/jsdom/i.test(navigator.userAgent);

export interface OqlEditorProps {
  value: string;
  onChange: (value: string) => void;
  /** Ctrl/Cmd+Enter */
  onRun?: () => void;
  /** Accessible name of the editor. */
  label: string;
  id?: string;
  /** Variables for validation (dashboards). */
  variables?: OqlVariables;
  /** Validate while typing (debounced) and show diagnostics. */
  validate?: boolean;
  minRows?: number;
  placeholder?: string;
  className?: string;
  /** Extra aria-describedby ids (e.g. a field hint). */
  describedBy?: string;
}

export interface EditorDiagnostic {
  severity: "error" | "warning";
  message: string;
  line: number;
  column: number;
  from: number;
  to: number;
}

/**
 * OQL editor: CodeMirror 6 (lazy chunk) with highlighting, schema-based completion, Ctrl/Cmd+Enter to run and
 * validation diagnostics as underlines and as a list below the editor. Until CodeMirror has loaded — and in jsdom —
 * an accessible textarea with the same behaviour is rendered.
 */
export function OqlEditor({ value, onChange, onRun, label, id, variables, validate = true, minRows = 3, placeholder, className, describedBy }: OqlEditorProps) {
  const { t } = useTranslation();
  const { resolved } = useTheme();
  const uid = useId();
  const listId = `${uid}-diagnostics`;
  const [cm, setCm] = useState<CodeMirrorModule | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<OqlEditorController | null>(null);
  const latest = useRef({ value, onChange, onRun, dark: resolved === "dark" });
  const schemaRef = useRef<SchemaSnapshot>({ base: undefined, eventType: null, detail: undefined });

  useEffect(() => {
    latest.current = { value, onChange, onRun, dark: resolved === "dark" };
  });

  useEffect(() => {
    if (!canLoadCodeMirror()) return;
    let alive = true;
    import("./codemirror")
      .then((m) => alive && setCm(m))
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);

  const debounced = useDebounced(value, 300);
  const validation = useQuery({ ...oqlValidateQuery(debounced, variables), enabled: validate && debounced.trim() !== "" });
  const eventType = canonicalEventType(eventTypeOf(debounced)) as OqlEventType | null;
  const baseSchema = useQuery({ ...oqlSchemaQuery(), enabled: cm !== null });
  const detailSchema = useQuery({ ...oqlSchemaQuery(eventType), enabled: cm !== null && eventType !== null });

  useEffect(() => {
    schemaRef.current = { base: baseSchema.data, eventType, detail: detailSchema.data };
  }, [baseSchema.data, detailSchema.data, eventType]);

  const hasText = debounced.trim() !== "";
  const data = validate && hasText && !validation.isPlaceholderData ? validation.data : undefined;
  const diagnostics = useMemo<EditorDiagnostic[]>(() => {
    if (!data) return [];
    const map = (severity: EditorDiagnostic["severity"]) => (d: { message: string; line: number; column: number; length: number }) => {
      const from = offsetOf(debounced, d.line, d.column);
      return { severity, message: d.message, line: d.line, column: d.column, from, to: from + Math.max(1, d.length) };
    };
    return [...data.errors.map(map("error")), ...data.warnings.map(map("warning"))];
  }, [data, debounced]);

  const labelled = [describedBy, diagnostics.length > 0 ? listId : undefined].filter(Boolean).join(" ") || undefined;

  // Create the CodeMirror view once the module is there.
  useEffect(() => {
    const host = hostRef.current;
    if (!cm || !host) return;
    const editor = cm.createOqlEditor({
      parent: host,
      doc: latest.current.value,
      dark: latest.current.dark,
      label,
      id,
      describedBy: labelled,
      placeholder,
      minHeight: `${minRows * 1.4 + 0.5}rem`,
      onChange: (v) => latest.current.onChange(v),
      onRun: () => latest.current.onRun?.(),
      getSchema: () => schemaRef.current,
    });
    editorRef.current = editor;
    return () => {
      editor.destroy();
      editorRef.current = null;
    };
    // labelled changes are rare (diagnostics appear); recreating would lose focus, so it is left out.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cm, label, id, placeholder, minRows]);

  useEffect(() => editorRef.current?.setValue(value), [value, cm]);
  useEffect(() => editorRef.current?.setDark(resolved === "dark"), [resolved, cm]);
  useEffect(() => {
    editorRef.current?.setDiagnostics(debounced === value ? diagnostics.map((d) => ({ from: d.from, to: d.to, severity: d.severity, message: d.message })) : []);
  }, [diagnostics, debounced, value, cm]);

  const hasErrors = diagnostics.some((d) => d.severity === "error");

  return (
    <div className={cn("flex min-w-0 flex-col", className)} data-testid="oql-editor">
      {cm ? (
        <div ref={hostRef} className="min-w-0" data-testid="oql-codemirror" />
      ) : (
        <textarea
          id={id}
          aria-label={label}
          aria-invalid={hasErrors || undefined}
          aria-describedby={labelled}
          value={value}
          rows={minRows}
          placeholder={placeholder}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
              e.preventDefault();
              onRun?.();
            }
          }}
          className="min-h-20 w-full resize-y rounded-md border border-input bg-background px-3 py-2 font-mono text-[13px] shadow-xs outline-none focus-visible:ring-2 focus-visible:ring-ring aria-invalid:border-destructive pointer-coarse:text-base"
        />
      )}
      {diagnostics.length > 0 && (
        <ul id={listId} aria-label={t("oql.editor.problems")} data-testid="oql-diagnostics" className="mt-1.5 flex flex-col gap-0.5 text-xs">
          {diagnostics.map((d, i) => (
            <li key={i} className={d.severity === "error" ? "text-destructive-text" : "text-warning-text"}>
              <span className="sr-only">{t(d.severity === "error" ? "oql.editor.error" : "oql.editor.warning")}: </span>
              {t("oql.editor.position", { line: d.line, column: d.column, message: d.message })}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
