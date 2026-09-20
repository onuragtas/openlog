// Functions ranked by self time (docs/contracts/profiles.md §5). Self time is the value attributed to the
// innermost frame, which is why `leaf` is a stored column rather than the last element of the stack array.
//
// The share is of the returned rows, not of the window — the API says so, and a percentage of something the
// reader cannot see would be a worse number than none.
import { useTranslation } from "react-i18next";
import type { ProfileFunction } from "@/api/profiles";
import { EmptyState } from "@/components/StateViews";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatValue } from "@/lib/profile-format";

export function FunctionsTable({ functions, total, unit }: { functions: ProfileFunction[]; total: number; unit: string }) {
  const { t, i18n } = useTranslation();

  if (functions.length === 0) return <EmptyState>{t("profiles.functions.empty")}</EmptyState>;

  const number = new Intl.NumberFormat(i18n.language);

  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">{t("profiles.functions.hint")}</p>
      <Table mobile="stack" data-testid="profile-functions">
        <TableHeader>
          <TableRow>
            <TableHead>{t("profiles.functions.columns.function")}</TableHead>
            <TableHead className="text-right">{t("profiles.functions.columns.self")}</TableHead>
            <TableHead className="text-right">{t("profiles.functions.columns.share")}</TableHead>
            <TableHead className="hidden text-right sm:table-cell">{t("profiles.functions.columns.samples")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {functions.map((f) => (
            <TableRow key={f.function}>
              <TableCell className="font-mono text-xs break-all">{f.function}</TableCell>
              <TableCell className="text-right tabular-nums">{formatValue(f.self, unit, i18n.language)}</TableCell>
              <TableCell className="text-right tabular-nums">{total > 0 ? `${((f.self / total) * 100).toFixed(1)}%` : "–"}</TableCell>
              <TableCell className="hidden text-right tabular-nums sm:table-cell">{number.format(f.samples)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
