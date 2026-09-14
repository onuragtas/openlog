import { useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState, useSearch } from "@tanstack/react-router";
import { Activity, Bell, Boxes, Container, LayoutDashboard, LogOut, Menu, MoreVertical, Plug, Rocket, ScrollText, SearchCode, Server, Settings, Ship, X } from "lucide-react";
import { Popover } from "radix-ui";
import { useTranslation } from "react-i18next";
import { logout, useMe } from "@/api/account";
import { EmailVerificationBanner } from "@/components/EmailVerificationBanner";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { OrgSwitcher } from "@/components/settings/OrgSwitcher";
import { UsageBanner } from "@/components/settings/UsageBanner";
import { ThemeToggle } from "@/components/ThemeToggle";
import { TimeRangePicker } from "@/components/TimeRangePicker";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { UpdateBanner } from "@/components/UpdateBanner";
import { useNavDrawer } from "@/lib/nav-drawer";
import type { RangeSpec } from "@/lib/time";

const NAV = [
  { to: "/hosts", icon: Server, label: "nav.hosts" },
  { to: "/containers", icon: Container, label: "nav.containers" },
  { to: "/kubernetes", icon: Ship, label: "nav.kubernetes" },
  { to: "/integrations", icon: Plug, label: "nav.integrations" },
  { to: "/apm", icon: Activity, label: "nav.apm" },
  { to: "/logs", icon: ScrollText, label: "nav.logs" },
  { to: "/query", icon: SearchCode, label: "nav.query" },
  { to: "/dashboards", icon: LayoutDashboard, label: "nav.dashboards" },
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

const itemClass =
  "flex min-h-10 items-center gap-3 rounded-md px-2.5 py-2 text-sm text-muted-foreground hover:bg-accent hover:text-accent-foreground data-[status=active]:bg-accent data-[status=active]:font-medium data-[status=active]:text-accent-foreground";

/** Logo, navigation and sign-out; rendered in the desktop sidebar and in the mobile drawer. */
function SidebarContent({ onNavigate, closeButton }: { onNavigate?: () => void; closeButton?: React.ReactNode }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  return (
    <>
      <div className="flex h-14 shrink-0 items-center gap-2 border-b px-3">
        <img src="/favicon.svg" alt="" className="size-7" />
        <span className="mr-auto text-base font-semibold tracking-tight">{t("app.name")}</span>
        {closeButton}
      </div>
      <nav aria-label={t("nav.main")} className="flex flex-1 flex-col gap-1 overflow-y-auto p-2">
        {NAV.map(({ to, icon: Icon, label }) => (
          <Link
            key={to}
            to={to}
            search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never}
            onClick={onNavigate}
            className={itemClass}
          >
            <Icon className="size-4 shrink-0" aria-hidden="true" />
            <span>{t(label)}</span>
          </Link>
        ))}
      </nav>
      <div className="border-t p-2">
        <button
          type="button"
          onClick={() => {
            onNavigate?.();
            void logout()
              .catch(() => undefined)
              .finally(() => {
                queryClient.clear();
                void navigate({ to: "/login" });
              });
          }}
          className={`${itemClass} w-full`}
        >
          <LogOut className="size-4 shrink-0" aria-hidden="true" />
          <span>{t("nav.signOut")}</span>
        </button>
      </div>
    </>
  );
}

/** Phone top-bar overflow menu: account, language and theme. */
function MoreMenu() {
  const { t } = useTranslation();
  const me = useMe().data;
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <Button variant="ghost" size="icon" aria-label={t("app.moreOptions")} className="md:hidden">
          <MoreVertical aria-hidden="true" />
        </Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="end"
          sideOffset={6}
          collisionPadding={8}
          className="z-50 flex w-64 max-w-[calc(100vw-1rem)] flex-col gap-3 rounded-lg border bg-card p-3 text-sm text-card-foreground shadow-lg"
        >
          {me?.user && (
            <div className="flex min-w-0 flex-col gap-1">
              <span className="truncate text-muted-foreground">{me.user.email}</span>
              {me.role && (
                <Badge variant="muted" className="w-fit">
                  {t(`settings.roles.${me.role}`)}
                </Badge>
              )}
            </div>
          )}
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground">{t("language.label")}</span>
            <LanguageSwitch />
          </div>
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground">{t("theme.label")}</span>
            <ThemeToggle />
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

export function AppShell() {
  const { t } = useTranslation();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const [drawerOpen, setDrawerOpen] = useNavDrawer(pathname);
  const showRange = !pathname.startsWith("/traces/") && !pathname.startsWith("/settings") && pathname !== "/inventory" && pathname !== "/hosts" && pathname !== "/fleet" && !pathname.startsWith("/alerts");

  return (
    <div className="flex h-full min-h-0">
      <a href="#main" className="sr-only z-50 rounded bg-primary px-3 py-2 text-primary-foreground focus:not-sr-only focus:absolute focus:top-2 focus:left-2">
        {t("app.skipToContent")}
      </a>
      <aside className="hidden w-56 shrink-0 flex-col border-r bg-sidebar lg:flex">
        <SidebarContent />
      </aside>
      <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
        <SheetContent side="left" title={t("nav.main")} showHeader={false} className="bg-sidebar lg:hidden" overlayClassName="lg:hidden" id="nav-drawer">
          <SidebarContent
            onNavigate={() => setDrawerOpen(false)}
            closeButton={
              <Button variant="ghost" size="icon" aria-label={t("nav.closeMenu")} onClick={() => setDrawerOpen(false)}>
                <X aria-hidden="true" />
              </Button>
            }
          />
        </SheetContent>
      </Sheet>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-1.5 border-b px-2 sm:gap-2 sm:px-4">
          <Button
            variant="ghost"
            size="icon"
            className="shrink-0 lg:hidden"
            aria-label={t("nav.openMenu")}
            aria-expanded={drawerOpen}
            aria-controls="nav-drawer"
            onClick={() => setDrawerOpen(true)}
          >
            <Menu aria-hidden="true" />
          </Button>
          <OrgSwitcher className="mr-auto" />
          {showRange && <UrlTimeRangePicker />}
          <div className="hidden items-center gap-2 md:flex">
            <LanguageSwitch />
            <ThemeToggle />
          </div>
          <MoreMenu />
        </header>
        <UpdateBanner />
        <EmailVerificationBanner />
        <UsageBanner />
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
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight break-words">{title}</h1>
        {subtitle && <p className="text-sm text-muted-foreground">{subtitle}</p>}
      </div>
      {actions && <div className="flex w-full min-w-0 flex-wrap items-center gap-2 sm:w-auto">{actions}</div>}
    </div>
  );
}

export { Button };
