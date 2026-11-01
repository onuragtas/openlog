// The latest session sample (db-monitoring.md §5 sessions): blocking chains first — the head session holds the
// locks, its children wait for it — then every other session that was running something.
//
// The sample is read whole and sorted here, because a chain can only be drawn over all of it. The server gives at most its 1000 longest running
// sessions, so a sort reaches only those, and the screen says when there were more.
import { useQuery } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { dbSessionsQuery, type DbSession } from "@/api/db";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { SortableHead, Table, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { blockingForest, type SessionNode } from "@/lib/db";
import { formatDateTime, formatValue } from "@/lib/format";
import { useLocalList } from "@/lib/list";

function WaitBadge({ s }: { s: DbSession }) {
  return s.wait_type ? (
    <Badge variant={s.wait_type.toLowerCase() === "lock" ? "destructive" : "warning"}>
      {s.wait_type}
      {s.wait_event ? `: ${s.wait_event}` : ""}
    </Badge>
  ) : (
    <Badge variant="success">CPU</Badge>
  );
}

function SessionLine({ s, onOpenQuery, inColumns = false }: { s: DbSession; onOpenQuery: (fp: string) => void; inColumns?: boolean }) {
  const { t, i18n } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-mono font-medium">#{s.session_id}</span>
        {!inColumns && (
          <>
            <Badge variant="muted">{s.state || "–"}</Badge>
            <WaitBadge s={s} />
            <span className="text-muted-foreground tabular-nums">{formatValue(s.duration_ms, "ms", i18n.resolvedLanguage)}</span>
          </>
        )}
        {s.blocks > 0 && (
          <span className="inline-flex items-center gap-1 font-medium text-destructive-text">
            <Lock className="size-3.5" aria-hidden="true" />
            {t("db.sessions.blocks", { count: s.blocks })}
          </span>
        )}
        <span className="text-xs text-muted-foreground">{[s.user, s.db_name, s.application, s.client_address].filter(Boolean).join(" · ")}</span>
      </div>
      {s.text &&
        (s.fingerprint ? (
          <button type="button" className="truncate text-left font-mono text-xs text-primary hover:underline" onClick={() => onOpenQuery(s.fingerprint)}>
            {s.text}
          </button>
        ) : (
          <span className="truncate font-mono text-xs text-muted-foreground">{s.text}</span>
        ))}
    </div>
  );
}

function Tree({ node, depth, onOpenQuery }: { node: SessionNode; depth: number; onOpenQuery: (fp: string) => void }) {
  return (
    <li className="flex flex-col gap-2" data-testid="db-blocking-node" data-session={node.session.session_id}>
      <div style={{ paddingLeft: `${depth * 1.25}rem` }} className={depth > 0 ? "border-l-2 border-destructive/40" : ""}>
        <div className={depth > 0 ? "pl-3" : ""}>
          <SessionLine s={node.session} onOpenQuery={onOpenQuery} />
        </div>
      </div>
      {node.children.length > 0 && (
        <ul className="flex flex-col gap-2">
          {node.children.map((c) => (
            <Tree key={c.session.session_id} node={c} depth={depth + 1} onOpenQuery={onOpenQuery} />
          ))}
        </ul>
      )}
    </li>
  );
}

type OtherSort = "session" | "state" | "wait" | "duration";

const OTHER_SORTS: Record<OtherSort, (s: DbSession) => string | number> = {
  session: (s) => s.session_id,
  state: (s) => s.state,
  wait: (s) => s.wait_type || "CPU",
  duration: (s) => s.duration_ms,
};

export function DbSessionsTab({ instance, onOpenQuery }: { instance: string; onOpenQuery: (fp: string) => void }) {
  const { t, i18n } = useTranslation();
  const q = useQuery(dbSessionsQuery(instance));
  const forest = useMemo(() => blockingForest(q.data?.sessions ?? []), [q.data?.sessions]);
  const others = useLocalList(forest.others, OTHER_SORTS, "duration", "desc");
  const { toFirstPage } = others;
  const past = others.state.offset > 0 && others.state.offset >= others.total;
  // This refreshes every few seconds and sessions end, so the page being read can stop existing.
  useEffect(() => {
    if (past) toFirstPage();
  }, [past, toFirstPage]);
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data.sampled_at) return <EmptyState>{t("db.sessions.none")}</EmptyState>;
  const { roots } = forest;
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">{t("db.sessions.sampledAt", { time: formatDateTime(Date.parse(q.data.sampled_at), i18n.resolvedLanguage ?? "en") })}</p>
      {q.data.has_more && <p className="text-sm text-muted-foreground" data-testid="db-sessions-cut">{t("db.sessions.cut", { n: q.data.sessions.length })}</p>}
      <Card className="gap-2">
        <CardHeader>
          <CardTitle>
            <h3>{t("db.sessions.blocking")}</h3>
          </CardTitle>
        </CardHeader>
        <CardContent>
          {roots.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("db.sessions.noBlocking")}</p>
          ) : (
            <ul className="flex flex-col gap-3" data-testid="db-blocking">
              {roots.map((r) => (
                <Tree key={r.session.session_id} node={r} depth={0} onOpenQuery={onOpenQuery} />
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
      <Card className="gap-2">
        <CardHeader>
          <CardTitle>
            <h3>{t("db.sessions.active", { count: others.total })}</h3>
          </CardTitle>
        </CardHeader>
        <CardContent>
          {others.total === 0 ? (
            <p className="text-sm text-muted-foreground">{t("db.sessions.noneOther")}</p>
          ) : (
            <>
              <Table mobile="stack" data-testid="db-other-sessions">
                <TableHeader>
                  <TableRow>
                    <SortableHead column="session" defaultDir="asc" {...others.head}>{t("db.columns.session")}</SortableHead>
                    <SortableHead column="state" defaultDir="asc" {...others.head}>{t("db.columns.state")}</SortableHead>
                    <SortableHead column="wait" defaultDir="asc" {...others.head}>{t("db.columns.waitType")}</SortableHead>
                    <SortableHead column="duration" className="text-right" {...others.head}>{t("db.columns.duration")}</SortableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {others.visible.map((s) => (
                    <TableRow key={s.session_id} data-testid="db-other-session">
                      <TableCell className="max-w-xl">
                        <SessionLine s={s} onOpenQuery={onOpenQuery} inColumns />
                      </TableCell>
                      <TableCell label={t("db.columns.state")} className="max-md:w-auto">
                        <Badge variant="muted">{s.state || "–"}</Badge>
                      </TableCell>
                      <TableCell label={t("db.columns.waitType")} className="max-md:w-auto">
                        <WaitBadge s={s} />
                      </TableCell>
                      <TableCell label={t("db.columns.duration")} className="text-right text-muted-foreground tabular-nums">
                        {formatValue(s.duration_ms, "ms", i18n.resolvedLanguage)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              <ListPager list={others} count={others.visible.length} hasMore={others.hasMore} />
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
