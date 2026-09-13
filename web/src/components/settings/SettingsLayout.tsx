import { Link, Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { can, type Permission } from "@/api/roles";
import { PageHeader } from "@/components/AppShell";

const TABS = [
  { to: "/settings/organization", label: "settings.tabs.organization", permission: null },
  { to: "/settings/members", label: "settings.tabs.members", permission: null },
  { to: "/settings/license-keys", label: "settings.tabs.licenseKeys", permission: "license_keys.list" },
  { to: "/settings/api-keys", label: "settings.tabs.apiKeys", permission: "api_keys.list" },
  { to: "/settings/security", label: "settings.tabs.security", permission: null },
] as const satisfies readonly { to: string; label: string; permission: Permission | null }[];

export function SettingsLayout() {
  const { t } = useTranslation();
  const me = useMe().data;
  const role = me?.role ?? null;
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
      </nav>
      <Outlet />
    </div>
  );
}
