import { Search } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";

import { SYSTEM_LOG_INPUT, type HostOs } from "@/lib/host-os";
import { SEVERITIES } from "@/lib/severity";

/** File path placeholder per OS (Linux keeps the translated nginx example). */
const DEFAULT_LOG_PATH_EXAMPLE: Record<HostOs, string | undefined> = {
  linux: undefined,
  darwin: "/opt/homebrew/var/log/nginx/access.log",
  windows: "C:\\inetpub\\logs\\LogFiles\\W3SVC1\\u_ex260915.log",
};

export interface LogFilterValues {
  q: string;
  severity: string;
  service: string;
  host: string;
  /** Agent log source filters (showSourceFilters): openlog.log.source, log.file.path, openlog.discovery.id, openlog.systemd.unit */
  source?: string;
  file?: string;
  discovery?: string;
  unit?: string;
}

interface LogFiltersProps {
  value: LogFilterValues;
  onApply: (v: LogFilterValues) => void;
  showHost?: boolean;
  showService?: boolean;
  /** Source, file path, discovered service and systemd unit fields (agent logs have no service.name). */
  showSourceFilters?: boolean;
  /**
   * OS of the host whose logs are filtered: the system log source (journald, unified log, Event Log) and the systemd
   * unit field follow it. Omitted: the Linux sources.
   */
  os?: HostOs;
}

const SYSTEM_SOURCE_LABEL: Record<HostOs, "logs.sourceJournald" | "logs.sourceUnifiedLog" | "logs.sourceWindowsEventLog"> = {
  linux: "logs.sourceJournald",
  darwin: "logs.sourceUnifiedLog",
  windows: "logs.sourceWindowsEventLog",
};

/** Filter form; values are applied (to the URL) on submit. The draft resets when the URL values change. */
export function LogFilters(props: LogFiltersProps) {
  return <LogFiltersForm key={JSON.stringify(props.value)} {...props} />;
}

function LogFiltersForm({ value, onApply, showHost = true, showService = true, showSourceFilters = false, os = "linux" }: LogFiltersProps) {
  const { t } = useTranslation();
  const id = useId();
  const [draft, setDraft] = useState(value);

  const set = (k: keyof LogFilterValues) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setDraft((d) => ({ ...d, [k]: e.target.value }));

  return (
    <form
      role="search"
      aria-label={t("logs.filters")}
      onSubmit={(e) => {
        e.preventDefault();
        onApply(draft);
      }}
      className="flex flex-wrap items-end gap-3"
    >
      <div className="flex min-w-48 flex-1 flex-col gap-1.5">
        <Label htmlFor={`${id}-q`}>{t("logs.queryLabel")}</Label>
        <Input id={`${id}-q`} type="search" value={draft.q} placeholder={t("logs.queryPlaceholder")} onChange={set("q")} />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-sev`}>{t("logs.severityLabel")}</Label>
        <NativeSelect id={`${id}-sev`} value={draft.severity} onChange={set("severity")}>
          <option value="">{t("severity.any")}</option>
          {SEVERITIES.map((s) => (
            <option key={s} value={s}>
              {t(`severity.${s}`)}
            </option>
          ))}
        </NativeSelect>
      </div>
      {showSourceFilters && (
        <>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-src`}>{t("logs.sourceLabel")}</Label>
            <NativeSelect id={`${id}-src`} value={draft.source ?? ""} onChange={set("source")}>
              <option value="">{t("logs.sourceAny")}</option>
              <option value="file">{t("logs.sourceFile")}</option>
              <option value={SYSTEM_LOG_INPUT[os]}>{t(SYSTEM_SOURCE_LABEL[os])}</option>
            </NativeSelect>
          </div>
          <div className="flex w-full flex-col gap-1.5 sm:w-64">
            <Label htmlFor={`${id}-file`}>{t("logs.fileLabel")}</Label>
            <Input id={`${id}-file`} value={draft.file ?? ""} placeholder={DEFAULT_LOG_PATH_EXAMPLE[os]} onChange={set("file")} className="font-mono" />
          </div>
          <div className="flex w-full flex-col gap-1.5 sm:w-40">
            <Label htmlFor={`${id}-disc`}>{t("logs.discoveryLabel")}</Label>
            <Input id={`${id}-disc`} value={draft.discovery ?? ""} placeholder={t("logs.discoveryPlaceholder")} onChange={set("discovery")} />
          </div>
          {/* systemd units exist on Linux only; a unit already in the URL stays editable. */}
          {(os === "linux" || !!draft.unit) && (
            <div className="flex w-full flex-col gap-1.5 sm:w-44">
              <Label htmlFor={`${id}-unit`}>{t("logs.unitLabel")}</Label>
              <Input id={`${id}-unit`} value={draft.unit ?? ""} placeholder={t("logs.unitPlaceholder")} onChange={set("unit")} className="font-mono" />
            </div>
          )}
        </>
      )}
      {showService && (
        <div className="flex w-full flex-col gap-1.5 sm:w-40">
          <Label htmlFor={`${id}-svc`}>{t("logs.serviceLabel")}</Label>
          <Input id={`${id}-svc`} value={draft.service} placeholder={t("logs.servicePlaceholder")} onChange={set("service")} />
        </div>
      )}
      {showHost && (
        <div className="flex w-full flex-col gap-1.5 sm:w-56">
          <Label htmlFor={`${id}-host`}>{t("logs.hostLabel")}</Label>
          <Input id={`${id}-host`} value={draft.host} placeholder={t("logs.hostPlaceholder")} onChange={set("host")} className="font-mono" />
        </div>
      )}
      <Button type="submit">
        <Search aria-hidden="true" />
        {t("logs.submit")}
      </Button>
    </form>
  );
}
