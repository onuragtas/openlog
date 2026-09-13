import { Badge } from "@/components/ui/badge";
import { extraAttributes } from "@/lib/utils";

export function AttributeChips({ attributes, max = 4 }: { attributes: Record<string, string>; max?: number }) {
  const extra = extraAttributes(attributes);
  if (extra.length === 0) return null;
  const shown = extra.slice(0, max);
  const hidden = extra.length - shown.length;
  return (
    <ul className="flex flex-wrap gap-1">
      {shown.map(([k, v]) => (
        <li key={k}>
          <Badge variant="muted" className="font-mono">
            {k}={v}
          </Badge>
        </li>
      ))}
      {hidden > 0 && (
        <li>
          <Badge variant="outline" title={extra.slice(max).map(([k, v]) => `${k}=${v}`).join("\n")}>
            +{hidden}
          </Badge>
        </li>
      )}
    </ul>
  );
}
