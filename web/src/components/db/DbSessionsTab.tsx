// The latest session sample (db-monitoring.md §5 sessions): blocking chains first — the head session holds the
// locks, its children wait for it — then every other session that was running something.
import { useQuery } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { useTranslation } from "react-i18next";
import { dbSessionsQuery, type DbSession } from "@/api/db";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { blockingForest, type SessionNode } from "@/lib/db";
import { formatDateTime, formatValue } from "@/lib/format";

function SessionLine({ s, onOpenQuery }: { s: DbSession; onOpenQuery: (fp: string) => void }) {
  const { t, i18n } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-mono font-medium">#{s.session_id}</span>
        <Badge variant="muted">{s.state || "–"}</Badge>
        {s.wait_type ? (
          <Badge variant={s.wait_type.toLowerCase() === "lock" ? "destructive" : "warning"}>
            {s.wait_type}
            {s.wait_event ? `: ${s.wait_event}` : ""}
          </Badge>
        ) : (
          <Badge variant="success">CPU</Badge>
        )}
        <span className="text-muted-foreground tabular-nums">{formatValue(s.duration_ms, "ms", i18n.resolvedLanguage)}</span>
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

export function DbSessionsTab({ instance, onOpenQuery }: { instance: string; onOpenQuery: (fp: string) => void }) {
  const { t, i18n } = useTranslation();
  const q = useQuery(dbSessionsQuery(instance));
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data.sampled_at) return <EmptyState>{t("db.sessions.none")}</EmptyState>;
  const { roots, others } = blockingForest(q.data.sessions);
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">{t("db.sessions.sampledAt", { time: formatDateTime(Date.parse(q.data.sampled_at), i18n.resolvedLanguage ?? "en") })}</p>
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
            <h3>{t("db.sessions.active", { count: others.length })}</h3>
          </CardTitle>
        </CardHeader>
        <CardContent>
          {others.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("db.sessions.noneOther")}</p>
          ) : (
            <Table mobile="stack">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("db.columns.session")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {others.map((s) => (
                  <TableRow key={s.session_id}>
                    <TableCell>
                      <SessionLine s={s} onOpenQuery={onOpenQuery} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
