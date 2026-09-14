import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { QueryConsole } from "@/components/oql/QueryConsole";

const route = getRouteApi("/app/query");

/** Query console (docs/contracts/oql.md): the query text lives in the URL so it can be shared and reloaded. */
export function QueryPage() {
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/query" });
  return (
    <QueryConsole
      query={search.q}
      view={search.view}
      range={{ range: search.range, from: search.from, to: search.to }}
      onSearchChange={(patch) => void navigate({ search: (prev) => ({ ...prev, ...patch, view: (patch.view ?? prev.view) === "chart" ? undefined : (patch.view ?? prev.view) }), replace: patch.q === undefined })}
    />
  );
}
