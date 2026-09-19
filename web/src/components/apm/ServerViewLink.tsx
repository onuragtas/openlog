// From an APM database call to the server-side view of the same statement (db-monitoring.md §4.1): the lookup runs
// only when asked, and opens the statement on the instance where it cost the most.
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { DatabaseZap } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { dbLookupQuery } from "@/api/db";
import { Button } from "@/components/ui/button";
import type { RangeSpec } from "@/lib/time";

export function ServerViewLink({ dbSystem, statement, range }: { dbSystem: string; statement: string; range: RangeSpec }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [asked, setAsked] = useState(false);
  const q = useQuery(dbLookupQuery({ dbSystem, statement, range, enabled: asked }));
  const match = q.data?.[0];
  useEffect(() => {
    if (asked && match) {
      void navigate({ to: "/databases/query", search: { ...range, instance: match.instance, fp: match.fingerprint } as never });
    }
  }, [asked, match, navigate, range]);
  if (asked && q.isSuccess && !match) {
    return <span className="text-xs text-muted-foreground">{t("apm.databases.notMonitored")}</span>;
  }
  return (
    <Button type="button" variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={() => setAsked(true)} disabled={asked && q.isFetching} data-testid="db-server-view">
      <DatabaseZap className="size-3.5" aria-hidden="true" />
      {t("apm.databases.serverView")}
    </Button>
  );
}
