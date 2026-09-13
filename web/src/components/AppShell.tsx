import { useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState, useSearch } from "@tanstack/react-router";
import { Activity, Bell, Boxes, LogOut, Rocket, ScrollText, Server, Settings } from "lucide-react";
import { useTranslation } from "react-i18next";
import { logout } from "@/api/account";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { OrgSwitcher } from "@/components/settings/OrgSwitcher";
import { ThemeToggle } from "@/components/ThemeToggle";
import { TimeRangePicker } from "@/components/TimeRangePicker";
import { Button } from "@/components/ui/button";
import { UpdateBanner } from "@/components/UpdateBanner";
import type { RangeSpec } from "@/lib/time";

const NAV = [
  { to: "/hosts", icon: Server, label: "nav.hosts" },
  { to: "/apm", icon: Activity, label: "nav.apm" },
  { to: "/logs", icon: ScrollText, label: "nav.logs" },
  { to: "/inventory", icon: Boxes, label: "nav.inventorySearch" },
  { to: "/fleet", icon: Rocket, label: "nav.fleet" },
  { to: "/alerts", icon: Bell, label: "nav.alerts" },
  { to: "/settings", icon: Settings, label: "nav.settings" },
] as const;

/** Range picker bound to the URL (`range` / `from` / `to` on every route). */
export function UrlTimeRangePicker() {
  const search = useSearch({ strict: false }) as RangeSpec;
  const navigate = useNavigate();
  return (
    <TimeRangePicker
      value={{ range: search.range, from: search.from, to: search.to }}
      onChange={(spec) =>
        void navigate({
          to: ".",
          search: (prev: Record<string, unknown>) => ({ ...prev, range: spec.range, from: spec.from, to: spec.to }),
          replace: false,
        } as never)
      }
    />
  );
}

export function AppShell() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const showRange = !pathname.startsWith("/traces/") && !pathname.startsWith("/settings") && pathname !== "/inventory" && pathname !== "/hosts" && pathname !== "/fleet" && !pathname.startsWith("/alerts");

  return (
    <div className="flex h-full min-h-0">
      <a href="#main" className="sr-only z-50 rounded bg-primary px-3 py-2 text-primary-foreground focus:not-sr-only focus:absolute focus:top-2 focus:left-2">
        {t("app.skipToContent")}
      </a>
      <aside className="flex w-14 shrink-0 flex-col border-r bg-sidebar lg:w-56">
        <div className="flex h-14 items-center gap-2 border-b px-3">
          <img src="/favicon.svg" alt="" className="size-7" />
          <span className="hidden text-base font-semibold tracking-tight lg:inline">{t("app.name")}</span>
        </div>
        <nav aria-label={t("nav.main")} className="flex flex-1 flex-col gap-1 p-2">
          {NAV.map(({ to, icon: Icon, label }) => (
            <Link
              key={to}
              to={to}
              search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never}
              title={t(label)}
              className="flex items-center gap-3 rounded-md px-2.5 py-2 text-sm text-muted-foreground hover:bg-accent hover:text-accent-foreground data-[status=active]:bg-accent data-[status=active]:font-medium data-[status=active]:text-accent-foreground"
            >
              <Icon className="size-4 shrink-0" aria-hidden="true" />
              <span className="sr-only lg:not-sr-only">{t(label)}</span>
            </Link>
          ))}
        </nav>
        <div className="border-t p-2">
          <button
            type="button"
            onClick={() => {
              void logout()
                .catch(() => undefined)
                .finally(() => {
                  queryClient.clear();
                  void navigate({ to: "/login" });
                });
            }}
            title={t("nav.signOut")}
            className="flex w-full items-center gap-3 rounded-md px-2.5 py-2 text-sm text-muted-foreground hover:bg-accent hover:text-accent-foreground"
          >
            <LogOut className="size-4 shrink-0" aria-hidden="true" />
            <span className="sr-only lg:not-sr-only">{t("nav.signOut")}</span>
          </button>
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center justify-end gap-2 border-b px-4">
          <OrgSwitcher className="mr-auto" />
          {showRange && <UrlTimeRangePicker />}
          <LanguageSwitch />
          <ThemeToggle />
        </header>
        <UpdateBanner />
        <main id="main" tabIndex={-1} className="min-h-0 flex-1 overflow-auto p-4 md:p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: React.ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="text-sm text-muted-foreground">{subtitle}</p>}
      </div>
      {actions}
    </div>
  );
}

export { Button };
