import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { operatorMeQuery } from "@/api/operator";
import {
  addStatusIncidentUpdate,
  createStatusIncident,
  deleteStatusIncident,
  IMPACTS,
  INCIDENT_STATUSES,
  STATUS_COMPONENTS,
  statusIncidentsQuery,
  type StatusIncident,
} from "@/api/statusPage";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";

type Kind = keyof typeof INCIDENT_STATUSES;
const TEXTAREA = "min-h-20 w-full rounded-md border bg-transparent px-3 py-2 text-sm";

/** Settings → Status page: incidents and maintenance windows of the public status page (superadmins; D-108). */
export function StatusPageSettings() {
  const { t } = useTranslation();
  const op = useQuery(operatorMeQuery());
  if (op.isPending) return <LoadingState />;
  if (!op.data?.operator) return <p className="text-sm text-muted-foreground">{t("statusPage.operatorsOnly")}</p>;
  return (
    <div className="flex flex-col gap-4">
      <CreateIncident />
      <IncidentList />
    </div>
  );
}

function CreateIncident() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const [kind, setKind] = useState<Kind>("incident");
  const [title, setTitle] = useState("");
  const [status, setStatus] = useState<string>(INCIDENT_STATUSES.incident[0]);
  const [impact, setImpact] = useState<(typeof IMPACTS)[number]>("minor");
  const [components, setComponents] = useState<string[]>([]);
  const [startsAt, setStartsAt] = useState("");
  const [endsAt, setEndsAt] = useState("");
  const [message, setMessage] = useState("");
  const create = useMutation({
    mutationFn: () =>
      createStatusIncident({
        kind, title: title.trim(), status, impact, components: components as (typeof STATUS_COMPONENTS)[number][], message: message.trim() || undefined,
        starts_at: startsAt ? new Date(startsAt).toISOString() : undefined, ends_at: endsAt ? new Date(endsAt).toISOString() : undefined,
      }),
    onSuccess: () => {
      setTitle("");
      setMessage("");
      setComponents([]);
      void qc.invalidateQueries({ queryKey: statusIncidentsQuery().queryKey });
    },
  });
  return (
    <SettingsSection title={t("statusPage.adminTitle")} description={t("statusPage.adminDescription")}>
      <a href="/status" target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-sm text-primary underline-offset-4 hover:underline">
        {t("statusPage.viewPublic")}
        <ExternalLink className="size-3.5" aria-hidden="true" />
      </a>
      <form
        className="grid grid-cols-1 gap-3 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <label htmlFor={`${id}-kind`} className="flex flex-col gap-1 text-sm">
          {t("statusPage.fieldKind")}
          <NativeSelect
            id={`${id}-kind`}
            className="h-9 text-sm"
            value={kind}
            onChange={(e) => {
              const k = e.target.value as Kind;
              setKind(k);
              setStatus(INCIDENT_STATUSES[k][0]);
            }}
          >
            <option value="incident">{t("statusPage.kind.incident")}</option>
            <option value="maintenance">{t("statusPage.kind.maintenance")}</option>
          </NativeSelect>
        </label>
        <label htmlFor={`${id}-title`} className="flex flex-col gap-1 text-sm">
          {t("statusPage.fieldTitle")}
          <Input id={`${id}-title`} value={title} maxLength={200} required onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label htmlFor={`${id}-status`} className="flex flex-col gap-1 text-sm">
          {t("statusPage.fieldStatus")}
          <NativeSelect id={`${id}-status`} className="h-9 text-sm" value={status} onChange={(e) => setStatus(e.target.value)}>
            {INCIDENT_STATUSES[kind].map((s) => (
              <option key={s} value={s}>
                {t(`statusPage.incidentStatus.${s}`)}
              </option>
            ))}
          </NativeSelect>
        </label>
        <label htmlFor={`${id}-impact`} className="flex flex-col gap-1 text-sm">
          {t("statusPage.fieldImpact")}
          <NativeSelect id={`${id}-impact`} className="h-9 text-sm" value={impact} onChange={(e) => setImpact(e.target.value as (typeof IMPACTS)[number])}>
            {IMPACTS.map((i) => (
              <option key={i} value={i}>
                {t(`statusPage.impact.${i}`)}
              </option>
            ))}
          </NativeSelect>
        </label>
        <fieldset className="flex flex-wrap gap-x-4 gap-y-2 sm:col-span-2">
          <legend className="mb-1 text-sm">{t("statusPage.fieldComponents")}</legend>
          {STATUS_COMPONENTS.map((c) => (
            <label key={c} className="inline-flex items-center gap-2 text-sm">
              <input type="checkbox" checked={components.includes(c)} onChange={(e) => setComponents((cur) => (e.target.checked ? [...cur, c] : cur.filter((x) => x !== c)))} />
              {t(`statusPage.components.${c}`)}
            </label>
          ))}
        </fieldset>
        <label htmlFor={`${id}-start`} className="flex flex-col gap-1 text-sm">
          {t("statusPage.fieldStart")}
          <Input id={`${id}-start`} type="datetime-local" value={startsAt} onChange={(e) => setStartsAt(e.target.value)} />
        </label>
        <label htmlFor={`${id}-end`} className="flex flex-col gap-1 text-sm">
          {t("statusPage.fieldEnd")}
          <Input id={`${id}-end`} type="datetime-local" value={endsAt} onChange={(e) => setEndsAt(e.target.value)} />
        </label>
        <label htmlFor={`${id}-message`} className="flex flex-col gap-1 text-sm sm:col-span-2">
          {t("statusPage.fieldMessage")}
          <textarea id={`${id}-message`} className={TEXTAREA} maxLength={5000} value={message} onChange={(e) => setMessage(e.target.value)} />
        </label>
        <div className="sm:col-span-2">
          <Button type="submit" size="sm" disabled={create.isPending || title.trim() === ""}>
            {t("statusPage.create")}
          </Button>
          <FormError error={create.error} />
        </div>
      </form>
    </SettingsSection>
  );
}

function IncidentList() {
  const { t } = useTranslation();
  const list = useQuery(statusIncidentsQuery());
  if (list.isPending) return <LoadingState />;
  if (list.isError) return <ErrorState error={list.error} onRetry={() => void list.refetch()} />;
  if (list.data.length === 0) return <p className="text-sm text-muted-foreground">{t("statusPage.empty")}</p>;
  return (
    <ul className="flex flex-col gap-3">
      {list.data.map((inc) => (
        <IncidentItem key={inc.id} incident={inc} />
      ))}
    </ul>
  );
}

function IncidentItem({ incident }: { incident: StatusIncident }) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const statuses = INCIDENT_STATUSES[incident.kind];
  const [status, setStatus] = useState<string>(incident.status);
  const [message, setMessage] = useState("");
  const refresh = () => void qc.invalidateQueries({ queryKey: statusIncidentsQuery().queryKey });
  const update = useMutation({
    mutationFn: () => addStatusIncidentUpdate(incident.id, status, message.trim()),
    onSuccess: () => {
      setMessage("");
      refresh();
    },
  });
  const remove = useMutation({ mutationFn: () => deleteStatusIncident(incident.id), onSuccess: refresh });
  const finished = incident.status === "resolved" || incident.status === "completed";
  return (
    <li className="rounded-xl border bg-card p-4 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={incident.kind === "maintenance" ? "secondary" : "warning"}>{t(`statusPage.kind.${incident.kind}`)}</Badge>
        <span className="font-medium">{incident.title}</span>
        <Badge variant={finished ? "success" : "outline"}>{t(`statusPage.incidentStatus.${incident.status}`)}</Badge>
        <span className="text-xs text-muted-foreground">{t(`statusPage.impact.${incident.impact}`)}</span>
        <span className="ml-auto">
          <ConfirmButton label={t("statusPage.delete")} confirmLabel={t("statusPage.confirmDelete")} pending={remove.isPending} onConfirm={() => remove.mutate()} />
        </span>
      </div>
      <p className="mt-1 text-xs text-muted-foreground">
        <DateTimeText value={incident.starts_at} />
        {incident.components.length > 0 && ` · ${incident.components.map((c) => t(`statusPage.components.${c as (typeof STATUS_COMPONENTS)[number]}`)).join(", ")}`}
      </p>
      {incident.updates.length > 0 && (
        <ol className="mt-2 flex flex-col gap-1 border-l pl-3">
          {incident.updates.slice(0, 5).map((u) => (
            <li key={u.id}>
              <span className="font-medium">{t(`statusPage.incidentStatus.${u.status as StatusIncident["status"]}`)}</span> — {u.message}{" "}
              <span className="text-xs text-muted-foreground">
                <DateTimeText value={u.created_at} relative />
              </span>
            </li>
          ))}
        </ol>
      )}
      {!finished && (
        <form
          className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            if (message.trim() !== "") update.mutate();
          }}
        >
          <label htmlFor={`${id}-status`} className="flex flex-col gap-1">
            {t("statusPage.fieldStatus")}
            <NativeSelect id={`${id}-status`} className="h-9 text-sm" value={status} onChange={(e) => setStatus(e.target.value)}>
              {statuses.map((s) => (
                <option key={s} value={s}>
                  {t(`statusPage.incidentStatus.${s}`)}
                </option>
              ))}
            </NativeSelect>
          </label>
          <label htmlFor={`${id}-message`} className="flex flex-1 flex-col gap-1">
            {t("statusPage.fieldMessage")}
            <Input id={`${id}-message`} value={message} maxLength={5000} onChange={(e) => setMessage(e.target.value)} />
          </label>
          <Button type="submit" size="sm" disabled={update.isPending || message.trim() === ""}>
            {t("statusPage.postUpdate")}
          </Button>
        </form>
      )}
      <FormError error={update.error ?? remove.error} />
    </li>
  );
}
