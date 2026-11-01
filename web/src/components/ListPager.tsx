// The foot of a paged list. There is no "of N": counting a grouped list on the server means reading all of it (api.md "Lists").
import { ChevronLeft, ChevronRight } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { formatNumber } from "@/lib/format";
import { PAGE_SIZES, type ListControls } from "@/lib/list";

export function ListPager<S extends string>({ list, count, hasMore }: { list: Pick<ListControls<S>, "state" | "setOffset" | "setLimit">; count: number; hasMore: boolean }) {
  const { t, i18n } = useTranslation();
  const id = useId();
  const { limit, offset } = list.state;
  // A single page that is not full has nothing to page through, and the controls would only be noise.
  if (offset === 0 && !hasMore && count < PAGE_SIZES[0]!) return null;
  return (
    <nav aria-label={t("common.perPage")} className="flex flex-wrap items-center justify-end gap-3 border-t px-3 py-2 text-xs text-muted-foreground" data-testid="list-pager">
      <div className="flex items-center gap-2">
        <Label htmlFor={id} className="text-xs">{t("common.perPage")}</Label>
        <NativeSelect id={id} className="h-7 w-20" value={String(limit)} onChange={(e) => list.setLimit(Number(e.target.value))}>
          {PAGE_SIZES.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </NativeSelect>
      </div>
      <span className="tabular-nums">
        {t("common.pageRange", { from: formatNumber(count === 0 ? 0 : offset + 1, i18n.language), to: formatNumber(offset + count, i18n.language) })}
      </span>
      <div className="flex items-center gap-1">
        <Button type="button" variant="outline" size="sm" className="h-7 px-2" disabled={offset === 0} onClick={() => list.setOffset(offset - limit)}>
          <ChevronLeft className="size-3.5" aria-hidden="true" />
          {t("common.previousPage")}
        </Button>
        <Button type="button" variant="outline" size="sm" className="h-7 px-2" disabled={!hasMore} onClick={() => list.setOffset(offset + limit)}>
          {t("common.nextPage")}
          <ChevronRight className="size-3.5" aria-hidden="true" />
        </Button>
      </div>
    </nav>
  );
}
