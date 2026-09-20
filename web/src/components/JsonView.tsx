import { cn } from "@/lib/utils";

/** Read-only pretty-printed JSON. (A CodeMirror viewer can replace this later.) */
export function JsonView({ value, className, label }: { value: unknown; className?: string; label?: string }) {
  const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  return (
    <pre
      aria-label={label}
      tabIndex={0}
      className={cn("max-h-80 overflow-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all", className)}
    >
      {text}
    </pre>
  );
}

export function AttributeTable({ attributes, caption }: { attributes: Record<string, string>; caption: string }) {
  const entries = Object.entries(attributes).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return null;
  return (
    // A long attribute key cannot wrap (it is one token), so the table gets its own scroll container rather
    // than widening the page it sits in.
    <table className="block w-full overflow-x-auto text-xs md:table">
      <caption className="mb-1 text-left text-xs font-semibold text-muted-foreground">{caption}</caption>
      <tbody>
        {entries.map(([k, v]) => (
          <tr key={k} className="border-b last:border-0">
            <th scope="row" className="py-1 pr-3 text-left align-top font-mono font-normal whitespace-nowrap text-muted-foreground">
              {k}
            </th>
            <td className="py-1 font-mono break-all">{v}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
