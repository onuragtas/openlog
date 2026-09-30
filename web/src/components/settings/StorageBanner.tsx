import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { HardDrive } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { diskSpaceQuery } from "@/api/diskspace";
import { can } from "@/api/roles";
import { cn } from "@/lib/utils";

/** Shown when a ClickHouse disk has reached one of the levels it is reported at (GET /api/v1/storage/disk). */
export function StorageBanner() {
  const { t } = useTranslation();
  const me = useMe().data;
  // Admins only. The endpoint refuses anyone else, and a banner that 403s on every page load is worse than none.
  const enabled = !!me?.organization && can(me?.role ?? null, "disk_space.read");
  const data = useQuery({ ...diskSpaceQuery(), enabled }).data;
  const worst = data?.worst;
  if (!data || !worst || worst.level <= 0) return null;

  const critical = worst.level >= data.effective.high_percent;
  return (
    <div
      role="status"
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-2 text-sm",
        critical ? "border-destructive/60 bg-destructive/10" : "border-warning/60 bg-warning/10",
      )}
    >
      <HardDrive className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1 break-words">
        {t(critical ? "storage.banner.critical" : "storage.banner.warning", {
          host: worst.host,
          disk: worst.disk,
          percent: Math.round(worst.used_percent),
        })}
      </span>
      <Link to="/settings/storage" className="font-medium underline underline-offset-4">
        {t("storage.banner.details")}
      </Link>
    </div>
  );
}
