// Source maps of browser applications (docs/contracts/rum.md §8). Uploading one is what turns
// "at n (main.3f2a1b9c.js:1:842)" in the error inbox into "at greet (src/app.ts:5:3)".
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileCode2, Loader2, Upload } from "lucide-react";
import { useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { deleteSourceMap, MAX_SOURCE_MAP_BYTES, scriptFromMapName, sourceMapsQuery, uploadSourceMap, type SourceMap } from "@/api/sourceMaps";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatBytes } from "@/lib/format";
import { usePermissions } from "@/lib/org-writable";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";

export function SourceMapsSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const perms = usePermissions();
  const canManage = perms.writable && perms.can("source_maps.manage");
  const maps = useQuery(sourceMapsQuery());
  const fileInput = useRef<HTMLInputElement>(null);
  const [app, setApp] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [tooLarge, setTooLarge] = useState(false);

  const invalidate = () => void qc.invalidateQueries({ queryKey: sourceMapsQuery().queryKey });
  const upload = useMutation({
    mutationFn: (v: { app: string; file: File }) => uploadSourceMap(v.app, scriptFromMapName(v.file.name), v.file),
    onSuccess: () => {
      setFile(null);
      if (fileInput.current) fileInput.current.value = "";
      invalidate();
    },
  });
  const remove = useMutation({ mutationFn: (mapID: string) => deleteSourceMap(mapID), onSettled: invalidate });

  // The script a map belongs to is derived from the file name, so the upload form can show it before sending
  // and nobody has to learn that "main.js.map" is stored under "main.js".
  const script = file ? scriptFromMapName(file.name) : "";
  const ready = canManage && app.trim() !== "" && file !== null && !tooLarge;

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("settings.sourceMaps.title")} description={t("settings.sourceMaps.description")}>
        {canManage && (
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (ready && file) upload.mutate({ app: app.trim(), file });
            }}
          >
            <div className="flex flex-wrap items-end gap-2">
              <div className="flex min-w-48 flex-1 flex-col gap-1.5">
                <Label htmlFor={`${id}-app`}>{t("settings.sourceMaps.application")}</Label>
                <Input id={`${id}-app`} value={app} maxLength={512} placeholder={t("settings.sourceMaps.applicationPlaceholder")} onChange={(e) => setApp(e.target.value)} />
              </div>
              <div className="flex min-w-60 flex-[2] flex-col gap-1.5">
                <Label htmlFor={`${id}-file`}>{t("settings.sourceMaps.file")}</Label>
                <Input
                  id={`${id}-file`}
                  ref={fileInput}
                  type="file"
                  accept=".map,application/json"
                  className="file:mr-3 file:rounded file:border-0 file:bg-muted file:px-2 file:py-1 file:text-sm"
                  onChange={(e) => {
                    const f = e.target.files?.[0] ?? null;
                    setFile(f);
                    setTooLarge(!!f && f.size > MAX_SOURCE_MAP_BYTES);
                  }}
                />
              </div>
              <Button type="submit" disabled={!ready || upload.isPending}>
                {upload.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Upload aria-hidden="true" />}
                {t("settings.sourceMaps.upload")}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {script ? t("settings.sourceMaps.willStoreAs", { script }) : t("settings.sourceMaps.hint")}
            </p>
            {tooLarge && <p className="text-xs text-destructive-text">{t("settings.sourceMaps.tooLarge", { size: formatBytes(MAX_SOURCE_MAP_BYTES) })}</p>}
          </form>
        )}
        <FormError error={upload.error ?? remove.error} />
      </SettingsSection>

      <div className="rounded-xl border bg-card">
        {maps.isPending ? (
          <LoadingState />
        ) : maps.isError ? (
          <ErrorState error={maps.error} onRetry={() => void maps.refetch()} />
        ) : maps.data.length === 0 ? (
          <EmptyState icon={<FileCode2 className="size-5" aria-hidden="true" />}>
            <p>{t("settings.sourceMaps.empty")}</p>
            <p className="mt-1 text-xs">{t("settings.sourceMaps.emptyHint")}</p>
          </EmptyState>
        ) : (
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("settings.sourceMaps.columns.script")}</TableHead>
                <TableHead>{t("settings.sourceMaps.application")}</TableHead>
                <TableHead className="hidden md:table-cell text-right">{t("settings.sourceMaps.columns.size")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("settings.columns.createdBy")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.sourceMaps.columns.uploaded")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("settings.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {maps.data.map((m: SourceMap) => (
                <TableRow key={m.id}>
                  <TableCell className="font-mono text-xs break-all">{m.script}</TableCell>
                  <TableCell label={t("settings.sourceMaps.application")}>{m.app}</TableCell>
                  <TableCell label={t("settings.sourceMaps.columns.size")} className="hidden md:table-cell text-right tabular-nums">
                    {formatBytes(m.size_bytes, 0)}
                  </TableCell>
                  <TableCell label={t("settings.columns.createdBy")} className="hidden break-all lg:table-cell">
                    {m.created_by_email || "–"}
                  </TableCell>
                  <TableCell label={t("settings.sourceMaps.columns.uploaded")} className="hidden md:table-cell">
                    <DateTimeText value={m.updated_at} relative />
                  </TableCell>
                  <TableCell className="text-right">
                    {canManage && (
                      <ConfirmButton
                        label={t("settings.sourceMaps.delete")}
                        confirmLabel={t("settings.sourceMaps.confirmDelete")}
                        pending={remove.isPending && remove.variables === m.id}
                        onConfirm={() => remove.mutate(m.id)}
                      />
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
      <p className="text-xs text-muted-foreground">{t("settings.sourceMaps.note")}</p>
    </div>
  );
}
