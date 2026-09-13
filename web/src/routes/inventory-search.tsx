import { useQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { inventorySearchQuery } from "@/api/queries";
import { PageHeader } from "@/components/AppShell";
import { InventoryTable } from "@/components/InventoryTable";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { translateOptional } from "@/i18n/dynamic";
import { KNOWN_CATEGORIES } from "@/lib/inventory";

const route = getRouteApi("/app/inventory");

interface Draft {
  category: string;
  q: string;
}

function SearchForm({ id, initial, onSubmit }: { id: string; initial: Draft; onSubmit: (d: Draft) => void }) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(initial);
  return (
    <form
      role="search"
      className="flex flex-wrap items-end gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit(draft);
      }}
    >
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-cat`}>{t("inventorySearch.categoryLabel")}</Label>
        <NativeSelect id={`${id}-cat`} value={draft.category} onChange={(e) => setDraft((d) => ({ ...d, category: e.target.value }))}>
          {KNOWN_CATEGORIES.map((c) => (
            <option key={c} value={c}>
              {translateOptional(`inventory.categories.${c}`, c)}
            </option>
          ))}
        </NativeSelect>
      </div>
      <div className="flex min-w-48 flex-1 flex-col gap-1.5 sm:max-w-sm">
        <Label htmlFor={`${id}-q`}>{t("inventorySearch.queryLabel")}</Label>
        <Input id={`${id}-q`} type="search" value={draft.q} placeholder={t("inventorySearch.queryPlaceholder")} onChange={(e) => setDraft((d) => ({ ...d, q: e.target.value }))} />
      </div>
      <Button type="submit">
        <Search aria-hidden="true" />
        {t("inventorySearch.submit")}
      </Button>
    </form>
  );
}

export function InventorySearchPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/inventory" });
  const id = useId();
  const category = search.category ?? "";
  const q = search.q ?? "";
  const query = useQuery(inventorySearchQuery(category, q));
  const hostCount = new Set((query.data ?? []).map((i) => i.host_id)).size;

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("inventorySearch.title")} subtitle={t("inventorySearch.subtitle")} />
      <SearchForm
        key={`${category}\n${q}`}
        id={id}
        initial={{ category: category || "package", q }}
        onSubmit={(d) => void navigate({ search: (prev) => ({ ...prev, category: d.category, q: d.q || undefined }) })}
      />
      {!category ? (
        <EmptyState>{t("inventorySearch.prompt")}</EmptyState>
      ) : query.isPending ? (
        <LoadingState />
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : query.data.length === 0 ? (
        <EmptyState>{t("inventorySearch.empty")}</EmptyState>
      ) : (
        <>
          <p className="text-xs text-muted-foreground" aria-live="polite">
            {t("common.results", { count: query.data.length })} · {t("inventorySearch.hostsMatched", { count: hostCount })}
          </p>
          <InventoryTable items={query.data} showHost showCategory={false} />
        </>
      )}
    </div>
  );
}
