import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Copy, Download, LayoutDashboard, Plus, Upload } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  createDashboard,
  dashboardsQuery,
  deleteDashboard,
  duplicateDashboard,
  exportDashboard,
  importDashboard,
  isDashboardsUnavailable,
  type DashboardSummary,
  type DashboardVisibility,
} from "@/api/dashboards";
import { PageHeader } from "@/components/AppShell";
import { ConfirmButton } from "@/components/settings/ConfirmButton";
import { DateTimeText, FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { downloadJson, exportFileName, parseImport } from "@/lib/dashboards";
import { useIsMobile } from "@/lib/media";
import { ReadOnlyNotice } from "@/components/ReadOnly";
import { usePermissions } from "@/lib/org-writable";
import { useDebounced } from "@/lib/use-debounced";

export interface DashboardsListProps {
  q?: string;
  onSearchChange: (q: string | undefined) => void;
  onOpenDashboard: (id: string) => void;
}

export function DashboardsList({ q = "", onSearchChange, onOpenDashboard }: DashboardsListProps) {
  const { t } = useTranslation();
  const mobile = useIsMobile();
  const queryClient = useQueryClient();
  const [text, setText] = useState(q);
  const debounced = useDebounced(text, 250);
  const list = useQuery(dashboardsQuery(debounced.trim()));
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  const perms = usePermissions();
  const canCreate = perms.canWriteAs("member");

  useEffect(() => {
    if (debounced.trim() !== q) onSearchChange(debounced.trim() || undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["dashboards"] });
  const remove = useMutation({ mutationFn: deleteDashboard, onSuccess: invalidate });
  const duplicate = useMutation({ mutationFn: (id: string) => duplicateDashboard(id), onSuccess: invalidate });
  const doExport = useMutation({ mutationFn: exportDashboard, onSuccess: (doc) => downloadJson(exportFileName(doc.name), doc) });

  if (list.isError && isDashboardsUnavailable(list.error)) {
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={t("dashboards.title")} subtitle={t("dashboards.subtitle")} />
        <EmptyState icon={<LayoutDashboard className="size-5" aria-hidden="true" />}>{t("dashboards.unavailable")}</EmptyState>
      </div>
    );
  }

  const rows = list.data ?? [];
  const actions = (d: DashboardSummary) => (
    <div className="flex flex-wrap items-center gap-1">
      {canCreate && (
        <Button variant="ghost" size="sm" disabled={duplicate.isPending} onClick={() => duplicate.mutate(d.id)} aria-label={t("dashboards.duplicateNamed", { name: d.name })}>
          <Copy aria-hidden="true" />
          <span className="max-md:sr-only">{t("dashboards.duplicate")}</span>
        </Button>
      )}
      <Button variant="ghost" size="sm" onClick={() => doExport.mutate(d.id)} aria-label={t("dashboards.exportNamed", { name: d.name })}>
        <Download aria-hidden="true" />
        <span className="max-md:sr-only">{t("dashboards.export")}</span>
      </Button>
      {d.can_edit && perms.writable && <ConfirmButton label={t("dashboards.delete")} confirmLabel={t("dashboards.confirmDelete")} pending={remove.isPending} onConfirm={() => remove.mutate(d.id)} />}
    </div>
  );
  const nameLink = (d: DashboardSummary) => (
    <Link
      to="/dashboards/$dashboardId"
      params={{ dashboardId: d.id }}
      search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never}
      className="font-medium text-primary hover:underline"
    >
      {d.name}
    </Link>
  );

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title={t("dashboards.title")}
        subtitle={t("dashboards.subtitle")}
        actions={
          canCreate && (
            <>
              <Button onClick={() => setCreating(true)}>
                <Plus aria-hidden="true" />
                {t("dashboards.create")}
              </Button>
              <Button variant="outline" onClick={() => setImporting(true)}>
                <Upload aria-hidden="true" />
                {t("dashboards.import")}
              </Button>
            </>
          )
        }
      />
      <ReadOnlyNotice />
      <Input type="search" className="max-w-sm" aria-label={t("dashboards.search")} placeholder={t("dashboards.search")} value={text} onChange={(e) => setText(e.target.value)} />
      <FormError error={remove.error ?? duplicate.error ?? doExport.error} />
      {list.isPending ? (
        <LoadingState />
      ) : list.isError ? (
        <ErrorState error={list.error} onRetry={() => void list.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState icon={<LayoutDashboard className="size-5" aria-hidden="true" />}>{debounced ? t("dashboards.noMatches") : t("dashboards.empty")}</EmptyState>
      ) : mobile ? (
        <ul className="flex flex-col gap-2" data-testid="dashboard-cards">
          {rows.map((d) => (
            <li key={d.id} className="flex min-w-0 flex-col gap-1.5 rounded-xl border bg-card p-3" data-testid="dashboard-row">
              <div className="flex min-w-0 items-center justify-between gap-2">
                <span className="min-w-0 truncate">{nameLink(d)}</span>
                <Badge variant={d.visibility === "private" ? "outline" : "muted"}>{t(`dashboards.visibility.${d.visibility}`)}</Badge>
              </div>
              {d.description && <p className="text-sm text-muted-foreground">{d.description}</p>}
              <p className="text-xs text-muted-foreground">
                {t("dashboards.counts", { pages: d.page_count, widgets: d.widget_count })} · <DateTimeText value={d.updated_at} relative />
              </p>
              {actions(d)}
            </li>
          ))}
        </ul>
      ) : (
        <div className="rounded-xl border bg-card">
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("dashboards.columns.name")}</TableHead>
                <TableHead>{t("dashboards.columns.visibility")}</TableHead>
                <TableHead>{t("dashboards.columns.widgets")}</TableHead>
                <TableHead>{t("dashboards.columns.owner")}</TableHead>
                <TableHead>{t("dashboards.columns.updated")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("dashboards.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((d) => (
                <TableRow key={d.id} data-testid="dashboard-row">
                  <TableCell className="max-w-96">
                    {nameLink(d)}
                    {d.description && <p className="truncate text-xs text-muted-foreground">{d.description}</p>}
                  </TableCell>
                  <TableCell className="max-md:w-auto">
                    <Badge variant={d.visibility === "private" ? "outline" : "muted"}>{t(`dashboards.visibility.${d.visibility}`)}</Badge>
                  </TableCell>
                  <TableCell label={t("dashboards.columns.widgets")} className="text-sm">
                    {t("dashboards.counts", { pages: d.page_count, widgets: d.widget_count })}
                  </TableCell>
                  <TableCell label={t("dashboards.columns.owner")} className="text-sm">
                    {d.created_by_email}
                  </TableCell>
                  <TableCell label={t("dashboards.columns.updated")} className="text-sm">
                    <DateTimeText value={d.updated_at} relative />
                  </TableCell>
                  <TableCell className="max-md:w-auto">{actions(d)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      <CreateDashboardSheet open={creating} onOpenChange={setCreating} onCreated={(id) => (invalidate(), onOpenDashboard(id))} />
      <ImportDashboardSheet open={importing} onOpenChange={setImporting} onImported={(id) => (invalidate(), onOpenDashboard(id))} />
    </div>
  );
}

function CreateDashboardSheet({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: (id: string) => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [visibility, setVisibility] = useState<DashboardVisibility>("org");
  const create = useMutation({
    mutationFn: () => createDashboard({ name: name.trim(), description, visibility, pages: [{ name: t("dashboards.defaultPage"), widgets: [] }] }),
    onSuccess: (d) => {
      onOpenChange(false);
      setName("");
      setDescription("");
      onCreated(d.id);
    },
  });
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" title={t("dashboards.createTitle")} closeLabel={t("common.close")} className="w-full max-w-full sm:max-w-md">
        <form
          className="flex flex-col gap-4 overflow-y-auto p-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate();
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-name`}>{t("dashboards.fields.name")}</Label>
            <Input id={`${uid}-name`} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} required />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-desc`}>{t("dashboards.fields.description")}</Label>
            <textarea id={`${uid}-desc`} rows={3} maxLength={2000} value={description} onChange={(e) => setDescription(e.target.value)} className="rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs" />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-vis`}>{t("dashboards.fields.visibility")}</Label>
            <NativeSelect id={`${uid}-vis`} value={visibility} onChange={(e) => setVisibility(e.target.value as DashboardVisibility)}>
              <option value="org">{t("dashboards.visibility.org")}</option>
              <option value="private">{t("dashboards.visibility.private")}</option>
            </NativeSelect>
          </div>
          <FormError error={create.error} />
          <div className="flex gap-2">
            <Button type="submit" disabled={!name.trim() || create.isPending}>
              {t("dashboards.create")}
            </Button>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

function ImportDashboardSheet({ open, onOpenChange, onImported }: { open: boolean; onOpenChange: (o: boolean) => void; onImported: (id: string) => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const [text, setText] = useState("");
  const [parseError, setParseError] = useState<string | null>(null);
  const run = useMutation({
    mutationFn: importDashboard,
    onSuccess: (d) => {
      onOpenChange(false);
      setText("");
      onImported(d.id);
    },
  });
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" title={t("dashboards.importTitle")} closeLabel={t("common.close")} className="w-full max-w-full sm:max-w-lg">
        <form
          className="flex flex-col gap-4 overflow-y-auto p-4"
          onSubmit={(e) => {
            e.preventDefault();
            const parsed = parseImport(text);
            if (!parsed.ok) {
              setParseError(t(parsed.error === "json" ? "dashboards.importErrors.json" : "dashboards.importErrors.format"));
              return;
            }
            setParseError(null);
            run.mutate(parsed.doc);
          }}
        >
          <p className="text-sm text-muted-foreground">{t("dashboards.importHint")}</p>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-file`}>{t("dashboards.importFile")}</Label>
            <input
              id={`${uid}-file`}
              type="file"
              accept=".json,application/json"
              className="text-sm file:mr-3 file:rounded-md file:border file:border-input file:bg-background file:px-3 file:py-1.5"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) void f.text().then(setText);
              }}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-json`}>{t("dashboards.importJson")}</Label>
            <textarea id={`${uid}-json`} rows={12} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} className="rounded-md border border-input bg-background px-3 py-2 font-mono text-xs shadow-xs" />
          </div>
          {parseError && (
            <p role="alert" className="text-sm text-destructive">
              {parseError}
            </p>
          )}
          <FormError error={run.error} />
          <div className="flex gap-2">
            <Button type="submit" disabled={!text.trim() || run.isPending}>
              {t("dashboards.import")}
            </Button>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}
