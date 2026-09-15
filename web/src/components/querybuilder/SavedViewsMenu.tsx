// Saved explorer views (api.md "Saved views", PostgreSQL auth mode): apply, save the current state as a new view,
// overwrite or delete views the caller may edit. Hidden when the API is unavailable (404).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bookmark, Loader2, Save, Trash2 } from "lucide-react";
import { Popover } from "radix-ui";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { createSavedView, deleteSavedView, isSavedViewsUnavailable, savedViewsQuery, updateSavedView, type SavedView } from "@/api/explorer";
import { WriteGuard } from "@/components/ReadOnly";
import { ErrorState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";

export interface SavedViewsMenuProps {
  signal: SavedView["signal"];
  /** Explorer state to store (read when saving). */
  getState: () => Record<string, unknown>;
  activeId?: string;
  onApply: (view: SavedView) => void;
}

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

export function SavedViewsMenu({ signal, getState, activeId, onApply }: SavedViewsMenuProps) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const list = useQuery(savedViewsQuery(signal));
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [visibility, setVisibility] = useState<"private" | "org">("private");
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: savedViewsQuery(signal).queryKey });
  const create = useMutation({
    mutationFn: () => createSavedView({ signal, name: name.trim(), visibility, state: getState() }),
    onSuccess: (view) => {
      setName("");
      void invalidate();
      onApply(view);
      setOpen(false);
    },
  });
  const overwrite = useMutation({
    mutationFn: (v: SavedView) => updateSavedView(v.id, { signal, name: v.name, description: v.description, visibility: v.visibility, state: getState() }),
    onSuccess: () => void invalidate(),
  });
  const remove = useMutation({
    mutationFn: (id: string) => deleteSavedView(id),
    onSuccess: () => {
      setConfirmDelete(null);
      void invalidate();
    },
  });

  if (list.isError && isSavedViewsUnavailable(list.error)) return null;
  const views = list.data ?? [];
  const active = views.find((v) => v.id === activeId);
  const mutationError = create.error ?? overwrite.error ?? remove.error;

  return (
    <Popover.Root
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        setConfirmDelete(null);
      }}
    >
      <Popover.Trigger asChild>
        <Button type="button" variant="outline" size="sm" className="max-w-full min-w-0">
          <Bookmark aria-hidden="true" />
          <span className="truncate">{active ? active.name : t("savedViews.button")}</span>
        </Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 flex w-[min(24rem,calc(100vw-1rem))] flex-col gap-2 rounded-lg border bg-card p-2 text-card-foreground shadow-lg">
          <h2 className="px-1 text-sm font-semibold">{t("savedViews.title")}</h2>
          {list.isPending ? (
            <p className="px-1 py-2 text-xs text-muted-foreground" role="status">
              {t("common.loading")}
            </p>
          ) : list.isError ? (
            <ErrorState error={list.error} onRetry={() => void list.refetch()} className="py-3" />
          ) : views.length === 0 ? (
            <p className="px-1 py-2 text-xs text-muted-foreground">{t("savedViews.empty")}</p>
          ) : (
            <ul className="flex max-h-72 flex-col gap-0.5 overflow-y-auto" aria-label={t("savedViews.title")}>
              {views.map((v) => (
                <li key={v.id} className={cn("flex items-center gap-1 rounded-md", v.id === activeId && "bg-accent/60")}>
                  <button
                    type="button"
                    className="flex min-w-0 flex-1 flex-col items-start rounded-md px-2 py-1.5 text-left hover:bg-accent pointer-coarse:py-2.5"
                    aria-current={v.id === activeId ? "true" : undefined}
                    onClick={() => {
                      onApply(v);
                      setOpen(false);
                    }}
                  >
                    <span className="flex max-w-full items-center gap-1.5">
                      <span className="truncate text-sm font-medium">{v.name}</span>
                      <Badge variant={v.visibility === "org" ? "secondary" : "muted"}>{t(`savedViews.visibility.${v.visibility}`)}</Badge>
                    </span>
                    {v.created_by_email && <span className="max-w-full truncate text-xs text-muted-foreground">{v.created_by_email}</span>}
                  </button>
                  {v.can_edit && (
                    <WriteGuard className="shrink-0">
                      <Button type="button" variant="ghost" size="icon" className="size-8" aria-label={t("savedViews.overwrite", { name: v.name })} title={t("savedViews.overwriteHint")} disabled={overwrite.isPending} onClick={() => overwrite.mutate(v)}>
                        {overwrite.isPending && overwrite.variables?.id === v.id ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Save aria-hidden="true" />}
                      </Button>
                      {confirmDelete === v.id ? (
                        <Button type="button" variant="destructive" size="sm" onClick={() => remove.mutate(v.id)} disabled={remove.isPending}>
                          {t("savedViews.confirmDelete")}
                        </Button>
                      ) : (
                        <Button type="button" variant="ghost" size="icon" className="size-8" aria-label={t("savedViews.delete", { name: v.name })} onClick={() => setConfirmDelete(v.id)}>
                          <Trash2 aria-hidden="true" />
                        </Button>
                      )}
                    </WriteGuard>
                  )}
                </li>
              ))}
            </ul>
          )}
          {mutationError !== null && (
            <p role="alert" className="px-1 text-xs text-destructive-text">
              {errorText(mutationError)}
            </p>
          )}
          <WriteGuard block>
            <form
              className="flex flex-col gap-2 border-t pt-2"
              onSubmit={(e) => {
                e.preventDefault();
                if (name.trim()) create.mutate();
              }}
            >
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${uid}-name`}>{t("savedViews.name")}</Label>
                <Input id={`${uid}-name`} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} placeholder={t("savedViews.namePlaceholder")} />
              </div>
              <div className="flex items-end gap-2">
                <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                  <Label htmlFor={`${uid}-vis`}>{t("savedViews.visibilityLabel")}</Label>
                  <NativeSelect id={`${uid}-vis`} value={visibility} onChange={(e) => setVisibility(e.target.value === "org" ? "org" : "private")}>
                    <option value="private">{t("savedViews.visibility.private")}</option>
                    <option value="org">{t("savedViews.visibility.org")}</option>
                  </NativeSelect>
                </div>
                <Button type="submit" size="sm" disabled={!name.trim() || create.isPending}>
                  {create.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
                  {t("savedViews.save")}
                </Button>
              </div>
            </form>
          </WriteGuard>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
