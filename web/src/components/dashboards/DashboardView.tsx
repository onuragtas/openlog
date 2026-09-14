import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, Copy, Download, Pencil, Plus, Settings } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { ApiError } from "@/api/client";
import {
  dashboardQuery,
  duplicateDashboard,
  exportDashboard,
  isDashboardsUnavailable,
  updateDashboard,
  type Dashboard,
  type DashboardWidget,
} from "@/api/dashboards";
import { atLeast } from "@/api/roles";
import { FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  applyLayout,
  dashboardToInput,
  downloadJson,
  duplicateWidget,
  exportFileName,
  layoutChanged,
  newWidget,
  removeWidget,
  requestVariables,
  upsertWidget,
  type GridItemLayout,
  type VarValues,
} from "@/lib/dashboards";
import type { RangeSpec } from "@/lib/time";
import { DashboardGrid } from "./DashboardGrid";
import { DashboardSettings } from "./DashboardSettings";
import { VariablesBar } from "./VariablesBar";
import { WidgetCard } from "./WidgetCard";
import { WidgetEditor } from "./WidgetEditor";

export interface DashboardViewSearch {
  page?: string;
  vars?: VarValues;
  edit?: boolean;
}

export interface DashboardViewProps {
  dashboardId: string;
  search: DashboardViewSearch;
  range: RangeSpec;
  onSearchChange: (patch: DashboardViewSearch) => void;
  onOpenDashboard?: (id: string) => void;
}

const LAYOUT_SAVE_DELAY = 800;

export function DashboardView({ dashboardId, search, range, onSearchChange, onOpenDashboard }: DashboardViewProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const me = useMe().data;
  const q = useQuery(dashboardQuery(dashboardId));
  const [local, setLocal] = useState<Dashboard | null>(null);
  const [conflict, setConflict] = useState(false);
  const [editing, setEditing] = useState<{ widget: DashboardWidget; isNew: boolean } | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const doc = local ?? q.data;
  const latest = useRef(doc);
  useEffect(() => {
    latest.current = doc;
  });
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const save = useMutation({
    mutationFn: (next: Dashboard) => updateDashboard(next.id, dashboardToInput(next)),
    onSuccess: (d) => {
      queryClient.setQueryData(dashboardQuery(d.id).queryKey, d);
      void queryClient.invalidateQueries({ queryKey: ["dashboards", "list"] });
      setLocal(null);
      setEditing(null);
      setSettingsOpen(false);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 409) setConflict(true);
    },
  });
  const persist = useCallback((next: Dashboard) => {
    setLocal(next);
    save.mutate(next);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => () => clearTimeout(timer.current), []);

  const duplicate = useMutation({ mutationFn: () => duplicateDashboard(dashboardId), onSuccess: (d) => onOpenDashboard?.(d.id) });
  const doExport = useMutation({ mutationFn: () => exportDashboard(dashboardId), onSuccess: (docExport) => downloadJson(exportFileName(docExport.name), docExport) });

  const variables = useMemo(() => requestVariables(doc?.variables ?? [], search.vars), [doc?.variables, search.vars]);

  if (q.isPending) return <LoadingState />;
  if (q.isError || !doc) {
    if (isDashboardsUnavailable(q.error)) {
      return (
        <EmptyState>
          <p className="mb-2 font-medium text-foreground">{t("dashboards.notFound")}</p>
          <Link to="/dashboards" className="text-primary hover:underline">
            {t("dashboards.back")}
          </Link>
        </EmptyState>
      );
    }
    return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  }

  const canEdit = doc.can_edit;
  const edit = !!search.edit && canEdit;
  const page = doc.pages.find((p) => p.id === search.page) ?? doc.pages[0]!;

  const onLayoutChange = (layout: GridItemLayout[]) => {
    if (!edit || !layoutChanged(page.widgets, layout)) return;
    setLocal(applyLayout(doc, page.id, layout));
    clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      const cur = latest.current;
      if (cur) save.mutate(applyLayout(cur, page.id, layout));
    }, LAYOUT_SAVE_DELAY);
  };

  const reload = () => {
    clearTimeout(timer.current);
    setLocal(null);
    setConflict(false);
    save.reset();
    void q.refetch();
  };

  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="dashboard-view">
      <div className="flex flex-col gap-2">
        <Link to="/dashboards" search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never} className="inline-flex w-fit items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="size-3" aria-hidden="true" />
          {t("dashboards.back")}
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="text-xl font-semibold tracking-tight break-words">{doc.name}</h1>
              <Badge variant={doc.visibility === "private" ? "outline" : "muted"}>{t(`dashboards.visibility.${doc.visibility}`)}</Badge>
              {save.isPending && <span className="text-xs text-muted-foreground" role="status">{t("dashboards.saving")}</span>}
            </div>
            {doc.description && <p className="text-sm text-muted-foreground">{doc.description}</p>}
          </div>
          <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto">
            {canEdit && (
              <Button variant={edit ? "secondary" : "outline"} aria-pressed={edit} onClick={() => onSearchChange({ edit: edit ? undefined : true })}>
                <Pencil aria-hidden="true" />
                {edit ? t("dashboards.doneEditing") : t("dashboards.edit")}
              </Button>
            )}
            {canEdit && (
              <Button variant="outline" onClick={() => setEditing({ widget: newWidget("line", page.widgets), isNew: true })}>
                <Plus aria-hidden="true" />
                {t("dashboards.addWidget")}
              </Button>
            )}
            {canEdit && (
              <Button variant="outline" size="icon" aria-label={t("dashboards.settings.title")} onClick={() => setSettingsOpen(true)}>
                <Settings aria-hidden="true" />
              </Button>
            )}
            {atLeast(me?.role, "member") && (
              <Button variant="outline" size="icon" aria-label={t("dashboards.duplicateNamed", { name: doc.name })} title={t("dashboards.duplicate")} disabled={duplicate.isPending} onClick={() => duplicate.mutate()}>
                <Copy aria-hidden="true" />
              </Button>
            )}
            <Button variant="outline" size="icon" aria-label={t("dashboards.exportNamed", { name: doc.name })} title={t("dashboards.export")} disabled={doExport.isPending} onClick={() => doExport.mutate()}>
              <Download aria-hidden="true" />
            </Button>
          </div>
        </div>
        <FormError error={duplicate.error ?? doExport.error} />
      </div>

      {conflict ? (
        <div role="alert" className="flex flex-wrap items-center gap-3 rounded-lg border border-warning/60 bg-warning/10 px-3 py-2 text-sm" data-testid="dashboard-conflict">
          <span>{t("dashboards.conflict")}</span>
          <Button size="sm" variant="outline" onClick={reload}>
            {t("dashboards.reload")}
          </Button>
        </div>
      ) : (
        !editing && !settingsOpen && save.error && <FormError error={save.error} />
      )}

      <VariablesBar variables={doc.variables} vars={search.vars} range={range} onChange={(vars) => onSearchChange({ vars })} />

      {doc.pages.length > 1 && (
        <Tabs value={page.id} onValueChange={(id) => onSearchChange({ page: id === doc.pages[0]!.id ? undefined : id })}>
          <TabsList aria-label={t("dashboards.pages")}>
            {doc.pages.map((p) => (
              <TabsTrigger key={p.id} value={p.id}>
                {p.name}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      )}

      {edit && <p className="hidden text-xs text-muted-foreground lg:block">{t("dashboards.editHint")}</p>}

      {page.widgets.length === 0 ? (
        <EmptyState>
          <p>{t("dashboards.emptyPage")}</p>
          {canEdit && (
            <Button className="mt-2" variant="outline" onClick={() => setEditing({ widget: newWidget("line", page.widgets), isNew: true })}>
              <Plus aria-hidden="true" />
              {t("dashboards.addWidget")}
            </Button>
          )}
        </EmptyState>
      ) : (
        <DashboardGrid
          key={page.id}
          widgets={page.widgets}
          edit={edit}
          onLayoutChange={onLayoutChange}
          renderWidget={(w, mode) => (
            <WidgetCard
              widget={w}
              range={range}
              variables={variables}
              edit={edit}
              fill={mode === "grid"}
              onEdit={() => setEditing({ widget: w, isNew: false })}
              onDuplicate={() => persist(duplicateWidget(doc, page.id, w.id, t("dashboards.copySuffix")))}
              onDelete={() => persist(removeWidget(doc, page.id, w.id))}
            />
          )}
        />
      )}

      <WidgetEditor
        widget={editing?.widget ?? null}
        isNew={editing?.isNew ?? false}
        range={range}
        variables={variables}
        saving={save.isPending}
        error={editing ? save.error : null}
        onOpenChange={(open) => {
          if (!open) {
            setEditing(null);
            save.reset();
          }
        }}
        onSave={(w) => persist(upsertWidget(latest.current ?? doc, page.id, w))}
      />
      <DashboardSettings
        dashboard={settingsOpen ? doc : null}
        saving={save.isPending}
        error={settingsOpen ? save.error : null}
        onOpenChange={(open) => {
          setSettingsOpen(open);
          if (!open) save.reset();
        }}
        onSave={(next) => {
          persist(next);
          if (!next.pages.some((p) => p.id === search.page)) onSearchChange({ page: undefined });
        }}
      />
    </div>
  );
}
