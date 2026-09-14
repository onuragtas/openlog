// "Highlight transaction" control of the service map: pick a service (optional) and one of its transactions;
// the path (GET /apm/map/path) is queried by the page and passed to ServiceMap.
import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { apmTransactionsQuery, type ApmMapPath } from "@/api/apm";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import type { RangeSpec } from "@/lib/time";

export interface MapPathControlProps {
  range: RangeSpec;
  /** service choices; omitted = fixed `service` (service page) */
  serviceOptions?: string[];
  service: string;
  transaction: string;
  namespace?: string;
  environment?: string;
  onChange: (v: { service: string; transaction: string }) => void;
  path?: ApmMapPath;
  loading?: boolean;
}

export function MapPathControl({ range, serviceOptions, service, transaction, namespace, environment, onChange, path, loading }: MapPathControlProps) {
  const { t } = useTranslation();
  const ids = { svc: useId(), txn: useId() };
  const txns = useQuery({ ...apmTransactionsQuery({ service, namespace, environment }, range, "throughput", 100), enabled: service !== "" });
  const names = [...new Set((txns.data?.transactions ?? []).map((x) => x.transaction_name))];
  if (transaction && !names.includes(transaction)) names.unshift(transaction);

  return (
    <div role="group" aria-label={t("apm.map.highlight")} className="flex flex-wrap items-center gap-2 text-sm" data-testid="map-path-control">
      <span className="font-medium">{t("apm.map.highlight")}</span>
      {serviceOptions && (
        <>
          <label htmlFor={ids.svc} className="sr-only">
            {t("apm.map.highlightService")}
          </label>
          <NativeSelect id={ids.svc} value={service} className="max-w-[12rem]" onChange={(e) => onChange({ service: e.target.value, transaction: "" })}>
            <option value="">{t("apm.map.chooseService")}</option>
            {serviceOptions.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </NativeSelect>
        </>
      )}
      <label htmlFor={ids.txn} className="sr-only">
        {t("apm.map.highlightTransaction")}
      </label>
      <NativeSelect id={ids.txn} value={transaction} disabled={!service} className="max-w-full sm:max-w-[18rem]" onChange={(e) => onChange({ service, transaction: e.target.value })}>
        <option value="">{t("apm.map.chooseTransaction")}</option>
        {names.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </NativeSelect>
      {transaction && (
        <>
          <span className="text-xs text-muted-foreground" aria-live="polite" data-testid="map-path-note">
            {loading ? t("common.loading") : path ? (path.trace_count > 0 ? t("apm.map.pathTraces", { count: path.trace_count }) : t("apm.map.pathEmpty")) : null}
          </span>
          <Button variant="ghost" size="sm" onClick={() => onChange({ service: serviceOptions ? service : service, transaction: "" })}>
            <X aria-hidden="true" />
            {t("apm.map.clearHighlight")}
          </Button>
        </>
      )}
    </div>
  );
}
