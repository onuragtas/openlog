import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { KubernetesEvent } from "@/api/kubernetes";
import { EmptyState } from "@/components/StateViews";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { rangeOnly, WORKLOAD_KINDS } from "@/lib/kubernetes";
import { parseTimeParam } from "@/lib/time";
import { EventTypeBadge } from "./K8sBadges";

/** Involved object: pods link to the pod page, workloads to the workload page. */
function EventObject({ e }: { e: KubernetesEvent }) {
  const label = (
    <>
      <span className="text-muted-foreground">{e.object_kind}</span> {e.object_name}
    </>
  );
  if (e.object_kind === "Pod" && e.object_uid) {
    return (
      <Link to="/kubernetes/pods/$podUid" params={{ podUid: e.object_uid }} search={rangeOnly} className="hover:underline">
        {label}
      </Link>
    );
  }
  if ((WORKLOAD_KINDS as readonly string[]).includes(e.object_kind) && e.namespace && e.cluster_uid) {
    return (
      <Link
        to="/kubernetes/workloads/$clusterUid/$namespace/$kind/$name"
        params={{ clusterUid: e.cluster_uid, namespace: e.namespace, kind: e.object_kind, name: e.object_name }}
        search={rangeOnly}
        className="hover:underline"
      >
        {label}
      </Link>
    );
  }
  return <span>{label}</span>;
}

/** Kubernetes events, newest first (semantic-conventions §7.5). */
export function EventList({ events, showObject = true, emptyText }: { events: KubernetesEvent[]; showObject?: boolean; emptyText?: string }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  if (events.length === 0) return <EmptyState>{emptyText ?? t("kubernetes.events.empty")}</EmptyState>;
  return (
    <Table mobile="stack" data-testid="k8s-events">
      <TableHeader>
        <TableRow>
          <TableHead>{t("kubernetes.events.columns.time")}</TableHead>
          <TableHead>{t("kubernetes.events.columns.type")}</TableHead>
          <TableHead>{t("kubernetes.events.columns.reason")}</TableHead>
          {showObject && <TableHead>{t("kubernetes.events.columns.object")}</TableHead>}
          <TableHead>{t("kubernetes.events.columns.message")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((e, i) => {
          const ms = parseTimeParam(e.timestamp) ?? 0;
          return (
            <TableRow key={`${e.timestamp}/${e.object_uid}/${e.reason}/${i}`}>
              <TableCell className="text-xs whitespace-nowrap">
                <time dateTime={e.timestamp} title={formatDateTime(ms, locale)}>
                  {formatRelative(ms, now, locale)}
                </time>
              </TableCell>
              <TableCell className="max-md:w-auto">
                <EventTypeBadge type={e.type} />
              </TableCell>
              <TableCell className="text-xs font-medium">
                {e.reason}
                {e.count > 1 && <span className="ml-1 text-muted-foreground">{t("kubernetes.events.count", { count: e.count })}</span>}
              </TableCell>
              {showObject && (
                <TableCell label={t("kubernetes.events.columns.object")} className="text-xs">
                  <EventObject e={e} />
                  {e.namespace && <div className="text-muted-foreground">{e.namespace}</div>}
                </TableCell>
              )}
              <TableCell label={t("kubernetes.events.columns.message")} className="min-w-64 text-xs max-md:min-w-0 max-md:whitespace-normal">
                {e.message}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
