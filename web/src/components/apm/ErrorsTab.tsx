// Errors tab of a service page: error inbox (components/apm/errors) plus the selected group's detail.
import { useQuery } from "@tanstack/react-query";
import { apmServiceQuery } from "@/api/apm";
import type { ServiceScope } from "@/lib/apm";
import type { RangeSpec } from "@/lib/time";
import { ErrorGroupPanel } from "./errors/ErrorGroupPanel";
import { ErrorInbox, type ErrorInboxFilterValues } from "./errors/ErrorInbox";

export interface ErrorsTabProps {
  scope: ServiceScope;
  range: RangeSpec;
  selected?: string;
  onSelect: (groupId: string | undefined) => void;
  filters: ErrorInboxFilterValues;
  onFilters: (patch: Partial<ErrorInboxFilterValues>) => void;
  onOpenTransaction?: (name: string) => void;
}

export function ErrorsTab({ scope, range, selected, onSelect, filters, onFilters, onOpenTransaction }: ErrorsTabProps) {
  const service = useQuery(apmServiceQuery(scope));
  const defaultVersion = service.data?.instances[0]?.version || undefined;
  return (
    <div className="flex flex-col gap-4">
      {selected && (
        <ErrorGroupPanel scope={scope} range={range} groupId={selected} onClose={() => onSelect(undefined)} onOpenTransaction={onOpenTransaction} defaultVersion={defaultVersion} />
      )}
      <ErrorInbox scope={scope} range={range} filters={filters} onFilters={onFilters} selected={selected} onOpen={(g) => onSelect(g.group_id)} defaultVersion={defaultVersion} />
    </div>
  );
}
