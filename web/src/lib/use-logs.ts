import { useInfiniteQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { logsInfiniteQuery, type LogsRequest } from "@/api/queries";
import { mergeLogPages } from "./logs";

/** Paged log listing: merged, de-duplicated rows plus "load older" controls. */
export function useLogPages(request: LogsRequest) {
  const query = useInfiniteQuery(logsInfiniteQuery(request));
  const logs = useMemo(() => (query.data ? mergeLogPages(query.data.pages.map((p) => p.logs)) : undefined), [query.data]);
  return { query, logs };
}
