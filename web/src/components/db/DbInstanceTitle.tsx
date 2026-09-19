import { useQuery } from "@tanstack/react-query";
import { dbInstancesQuery } from "@/api/db";
import { instanceLabel } from "@/lib/db";
import type { RangeSpec } from "@/lib/time";

/** The instance's server address as the page heading; the id (service.instance.id) stays as the title. */
export function DbInstanceTitle({ instance, range }: { instance: string; range: RangeSpec }) {
  const q = useQuery(dbInstancesQuery(range));
  const found = q.data?.find((i) => i.instance === instance);
  return <span title={instance}>{found ? instanceLabel(found) : instance}</span>;
}
