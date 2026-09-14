import { Link } from "@tanstack/react-router";
import { Activity, Plug, ScrollText, Search, Server, Waypoints, type LucideIcon } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { EmptyState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { INSTALL_TARGETS, TARGET_GROUPS, type TargetGroup } from "@/lib/install-commands";
import { tDynamic } from "@/lib/onboarding-key";

const GROUP_ICONS: Record<TargetGroup, LucideIcon> = {
  infrastructure: Server,
  apm: Activity,
  logs: ScrollText,
  opentelemetry: Waypoints,
  integrations: Plug,
};

/** Cards of every data source, grouped like New Relic's "Add data" catalog. */
export function AddDataCatalog() {
  const { t } = useTranslation();
  const id = useId();
  const [q, setQ] = useState("");
  const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
  const items = INSTALL_TARGETS.map((target) => ({
    target,
    title: tDynamic(t, `addData.targets.${target.id}.title`),
    description: tDynamic(t, `addData.targets.${target.id}.description`),
  })).filter(({ target, title, description }) => {
    const text = `${title} ${description} ${target.id} ${tDynamic(t, `addData.groups.${target.group}`)}`.toLowerCase();
    return terms.every((term) => text.includes(term));
  });

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div className="relative w-full sm:w-80">
        <label htmlFor={id} className="sr-only">
          {t("addData.searchLabel")}
        </label>
        <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
        <Input id={id} type="search" className="pl-8" placeholder={t("addData.searchPlaceholder")} value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      {items.length === 0 && <EmptyState>{t("addData.noMatch", { q })}</EmptyState>}
      {TARGET_GROUPS.map((group) => {
        const cards = items.filter((i) => i.target.group === group);
        if (cards.length === 0) return null;
        const Icon = GROUP_ICONS[group];
        return (
          <section key={group} aria-labelledby={`${id}-${group}`} className="flex min-w-0 flex-col gap-3">
            <div className="flex min-w-0 items-center gap-2">
              <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <h2 id={`${id}-${group}`} className="text-base font-semibold">
                {tDynamic(t, `addData.groups.${group}`)}
              </h2>
              <span className="min-w-0 truncate text-xs text-muted-foreground">{tDynamic(t, `addData.groupHints.${group}`)}</span>
            </div>
            <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {cards.map(({ target, title, description }) => (
                <li key={target.id} className="min-w-0">
                  <Link
                    to="/add-data/$"
                    params={{ _splat: target.id }}
                    data-testid="add-data-card"
                    className="flex h-full min-w-0 flex-col gap-1 rounded-xl border bg-card p-4 transition-colors hover:border-primary hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                  >
                    <span className="font-medium">{title}</span>
                    <span className="text-sm text-muted-foreground">{description}</span>
                    {target.requires && (
                      <Badge variant="muted" className="mt-2 max-w-full whitespace-normal">
                        {t("addData.requires", { name: tDynamic(t, `addData.targets.${target.requires}.title`) })}
                      </Badge>
                    )}
                  </Link>
                </li>
              ))}
            </ul>
          </section>
        );
      })}
    </div>
  );
}
