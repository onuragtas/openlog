import { useQuery } from "@tanstack/react-query";
import { Link, Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { operatorMeQuery } from "@/api/operator";
import { can, type Permission } from "@/api/roles";
import { PageHeader } from "@/components/AppShell";
import { ReadOnlyNotice } from "@/components/ReadOnly";

const TABS = [
  { to: "/settings/profile", label: "settings.tabs.profile", permission: null },
  { to: "/settings/organization", label: "settings.tabs.organization", permission: null },
  { to: "/settings/members", label: "settings.tabs.members", permission: null },
  { to: "/settings/license-keys", label: "settings.tabs.licenseKeys", permission: "license_keys.list" },
  { to: "/settings/api-keys", label: "settings.tabs.apiKeys", permission: "api_keys.list" },
  { to: "/settings/security", label: "settings.tabs.security", permission: null },
  { to: "/settings/sso", label: "sso.tab", permission: "org.update" },
  { to: "/settings/audit-log", label: "settings.tabs.auditLog", permission: "audit.read" },
  { to: "/settings/apm-sampling", label: "settings.tabs.tailSampling", permission: null },
  { to: "/settings/usage", label: "settings.tabs.usage", permission: null },
] as const satisfies readonly { to: string; label: string; permission: Permission | null }[];

const TAB_CLASS =
  "-mb-px border-b-2 border-transparent px-3 py-2 text-sm whitespace-nowrap text-muted-foreground pointer-coarse:py-2.5 hover:text-foreground data-[status=active]:border-primary data-[status=active]:font-medium data-[status=active]:text-foreground";

export function SettingsLayout() {
  const { t } = useTranslation();
  const me = useMe().data;
  const role = me?.role ?? null;
  // Status page incidents: openlog operators only (D-108).
  const op = useQuery({ ...operatorMeQuery(), enabled: !!me?.user }).data;
  return (
    <div className="mx-auto w-full max-w-6xl">
      <PageHeader
        title={t("settings.title")}
        subtitle={me?.organization && role ? t("settings.subtitle", { org: me.organization.name, role: t(`settings.roles.${role}`) }) : undefined}
      />
      <nav aria-label={t("settings.tabs.label")} className="mb-4 flex gap-1 overflow-x-auto border-b [scrollbar-width:none]">
        {TABS.filter((tab) => tab.permission === null || can(role, tab.permission)).map((tab) => (
          <Link
            key={tab.to}
            to={tab.to}
            className="-mb-px border-b-2 border-transparent px-3 py-2 text-sm whitespace-nowrap text-muted-foreground pointer-coarse:py-2.5 hover:text-foreground data-[status=active]:border-primary data-[status=active]:font-medium data-[status=active]:text-foreground"
          >
            {t(tab.label)}
          </Link>
        ))}
        {op?.operator && (
          <Link to="/settings/status-page" className={TAB_CLASS}>
            {t("statusPage.tab")}
          </Link>
        )}
      </nav>
      <ReadOnlyNotice className="mb-4" />
      <Outlet />
    </div>
  );
}
