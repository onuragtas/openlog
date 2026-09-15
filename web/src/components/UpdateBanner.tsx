import { useQuery } from "@tanstack/react-query";
import { RefreshCw, X } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { atLeast } from "@/api/roles";
import { versionQuery } from "@/api/version";
import { useUpdatedServerVersion } from "@/lib/server-version";

export const DISMISSED_UPDATE_STORAGE = "openlog.updateDismissed";

function readDismissed(): string | null {
  try {
    return localStorage.getItem(DISMISSED_UPDATE_STORAGE);
  } catch {
    return null;
  }
}

function writeDismissed(version: string): void {
  try {
    localStorage.setItem(DISMISSED_UPDATE_STORAGE, version);
  } catch {
    // storage unavailable: the banner is hidden until the next page load
  }
}

const bar = "flex flex-wrap items-center gap-x-3 gap-y-1 border-b px-4 py-2 text-sm";

/**
 * Two notices above the page content:
 * - "openlog was updated — reload" for everyone, when API responses report a different backend
 *   version than the one this page was loaded from;
 * - "openlog X.Y.Z is available — release notes", dismissible per version, for those who may act on it: the users
 *   GET /api/v1/version grants update_requests.can_request (superadmins when OPENLOG_SIGNUP_ENABLED=true, admins and
 *   owners otherwise); without the request channel (update_requests null, static auth mode) admins and owners.
 */
export function UpdateBanner() {
  const { t } = useTranslation();
  const me = useMe().data;
  const isAdmin = atLeast(me?.role ?? null, "admin");
  // Every signed-in user loads the version: a superadmin may have any role in the current organization.
  const { data } = useQuery({ ...versionQuery(), enabled: !!me });
  const updatedTo = useUpdatedServerVersion();
  const [dismissed, setDismissed] = useState(readDismissed);

  if (updatedTo) {
    return (
      <div role="status" className={`${bar} bg-primary/10`}>
        <RefreshCw className="size-4 shrink-0" aria-hidden="true" />
        <span>{t("update.updated", { version: updatedTo })}</span>
        <button
          type="button"
          onClick={() => window.location.reload()}
          className="rounded-md bg-primary px-2.5 py-1 text-xs font-medium text-primary-foreground hover:bg-primary/90"
        >
          {t("update.reload")}
        </button>
      </div>
    );
  }
  const latest = data?.latest_available;
  const requests = data?.update_requests;
  const operator = requests ? requests.can_request : isAdmin;
  if (!operator || !latest || dismissed === latest.version) return null;
  return (
    <div role="status" className={`${bar} bg-muted`}>
      <span>{t("update.available", { version: latest.version })}</span>
      {latest.notes_url && (
        <a href={latest.notes_url} target="_blank" rel="noopener noreferrer" className="font-medium underline underline-offset-2">
          {t("update.releaseNotes")}
        </a>
      )}
      <button
        type="button"
        aria-label={t("update.dismiss")}
        title={t("update.dismiss")}
        onClick={() => {
          writeDismissed(latest.version);
          setDismissed(latest.version);
        }}
        className="ml-auto rounded-md p-1 text-muted-foreground hover:bg-accent hover:text-accent-foreground"
      >
        <X className="size-4" aria-hidden="true" />
      </button>
    </div>
  );
}
