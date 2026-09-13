import { useQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { inventoryQuery } from "@/api/queries";
import { InventoryTable } from "@/components/InventoryTable";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Input } from "@/components/ui/input";
import { translateOptional } from "@/i18n/dynamic";
import { countByCategory, filterInventory } from "@/lib/inventory";
import { cn } from "@/lib/utils";

const route = getRouteApi("/app/hosts/$hostId");

export function HostInventoryTab({ hostId }: { hostId: string }) {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/hosts/$hostId" });
  const id = useId();
  const query = useQuery(inventoryQuery(hostId));
  const category = search.category ?? "";
  const q = search.iq ?? "";

  const items = useMemo(() => query.data?.items ?? [], [query.data]);
  const counts = useMemo(() => countByCategory(items), [items]);
  const filtered = useMemo(() => filterInventory(items, category, q), [items, category, q]);

  if (query.isPending) return <LoadingState />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  if (!query.data.snapshot_id) return <EmptyState>{t("services.noSnapshot")}</EmptyState>;
  if (items.length === 0) return <EmptyState>{t("inventory.empty")}</EmptyState>;

  const tabs = [{ category: "", count: items.length }, ...counts];

  return (
    <section className="flex flex-col gap-3" aria-label={t("inventory.title")}>
      <div role="group" aria-label={t("inventory.categoriesLabel")} className="flex flex-wrap gap-1">
        {tabs.map((c) => (
          <button
            key={c.category || "all"}
            type="button"
            aria-pressed={category === c.category}
            onClick={() => void navigate({ search: (prev) => ({ ...prev, category: c.category || undefined }), replace: true })}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs font-medium hover:bg-accent",
              category === c.category && "border-primary bg-primary text-primary-foreground hover:bg-primary/90",
            )}
          >
            {c.category === "" ? t("inventory.allCategories") : translateOptional(`inventory.categories.${c.category}`, c.category)}
            <span className={cn("rounded px-1 font-mono text-[10px]", category === c.category ? "bg-primary-foreground/20" : "bg-muted")}>{c.count}</span>
          </button>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full sm:w-80">
          <label htmlFor={id} className="sr-only">
            {t("inventory.searchLabel")}
          </label>
          <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
          <Input
            id={id}
            type="search"
            className="pl-8"
            placeholder={t("inventory.searchPlaceholder")}
            value={q}
            onChange={(e) => void navigate({ search: (prev) => ({ ...prev, iq: e.target.value || undefined }), replace: true })}
          />
        </div>
        <span className="text-xs text-muted-foreground" aria-live="polite">
          {t("inventory.count", { shown: filtered.length, total: items.length })}
        </span>
      </div>
      {filtered.length === 0 ? <EmptyState>{t("inventory.noMatch")}</EmptyState> : <InventoryTable items={filtered} showCategory={category === ""} />}
    </section>
  );
}
