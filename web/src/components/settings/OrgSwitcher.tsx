import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { Badge } from "@/components/ui/badge";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";

/**
 * Current organization (a select for users in several organizations), the
 * caller's role and email. Switching stores the choice (X-Openlog-Org-Id),
 * drops every cached query and reloads the route data for the new tenant.
 */
export function OrgSwitcher({ className }: { className?: string }) {
  const { t } = useTranslation();
  const id = useId();
  const me = useMe().data;
  const queryClient = useQueryClient();
  const router = useRouter();
  if (!me?.organization) return <div className={className} />;
  const current = me.organization;

  return (
    <div className={cn("flex min-w-0 items-center gap-2", className)}>
      {me.organizations.length > 1 ? (
        <>
          <label htmlFor={id} className="sr-only">
            {t("settings.switchOrg")}
          </label>
          <NativeSelect
            id={id}
            value={current.id}
            className="h-8 max-w-32 min-w-0 text-xs sm:max-w-48"
            onChange={(e) => {
              setSelectedOrg(e.target.value);
              queryClient.clear();
              void router.navigate({ to: "/hosts" }).then(() => router.invalidate());
            }}
          >
            {me.organizations.map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
          </NativeSelect>
        </>
      ) : (
        <span className="truncate text-sm font-medium" title={current.tenant_id}>
          {current.name}
        </span>
      )}
      {me.role && (
        <Badge variant="muted" className="hidden sm:inline-flex">
          {t(`settings.roles.${me.role}`)}
        </Badge>
      )}
      {me.user && <span className="hidden truncate text-xs text-muted-foreground lg:inline">{me.user.email}</span>}
    </div>
  );
}
