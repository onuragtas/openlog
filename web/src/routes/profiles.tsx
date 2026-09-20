// Continuous profiling screen (/profiles): what has been profiled, and for one service and profile type its
// flame graph and the functions ranked by self time (docs/contracts/profiles.md §5).
//
// Service *and* profile type live in the URL, like the range does on every other screen, so a link keeps what
// it was opened with. The type is part of the selection rather than a default because nanoseconds and bytes
// do not add up: the services list is what offers the types a service actually has.
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Flame } from "lucide-react";
import { useTranslation } from "react-i18next";
import { profileFlameQuery, profileFunctionsQuery, profileServicesQuery, type ProfileService } from "@/api/profiles";
import { PageHeader } from "@/components/AppShell";
import { FlameGraph } from "@/components/profiles/FlameGraph";
import { FunctionsTable } from "@/components/profiles/FunctionsTable";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatRelative } from "@/lib/format";
import { formatValue } from "@/lib/profile-format";
import { useNow } from "@/lib/hooks";
import { apiTimeMs } from "@/lib/rum";
import type { ProfilesSearch } from "@/router";

const routeApi = getRouteApi("/app/profiles");

const rangeOf = (s: { range?: string; from?: string; to?: string }) => ({ range: s.range, from: s.from, to: s.to });

export function ProfilesPage() {
  const { t } = useTranslation();
  const search = routeApi.useSearch();

  const selected = Boolean(search.service && search.type);

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("profiles.title")} subtitle={t("profiles.subtitle")} />
      {selected ? <ProfileDetail search={search} /> : <ProfileServices search={search} />}
    </div>
  );
}

/** The services table: one row per service, environment and profile type, which is the type selector. */
function ProfileServices({ search }: { search: ProfilesSearch }) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const now = useNow();
  const services = useQuery(profileServicesQuery(rangeOf(search)));

  if (services.isPending) return <LoadingState />;
  if (services.isError) return <ErrorState error={services.error} onRetry={() => void services.refetch()} />;
  if (services.data.length === 0)
    return (
      <EmptyState icon={<Flame className="size-5" aria-hidden="true" />}>
        <p>{t("profiles.empty")}</p>
        <p className="mt-1 text-xs">{t("profiles.emptyHint")}</p>
      </EmptyState>
    );

  const open = (s: ProfileService) =>
    void navigate({
      to: "/profiles",
      search: (prev: Record<string, unknown>) =>
        ({ ...rangeOf(prev as ProfilesSearch), service: s.service, type: s.type, env: s.environment || undefined }) as never,
    });

  const when = (value: string) => {
    const ms = apiTimeMs(value);
    return ms === null ? "–" : formatRelative(ms, now, i18n.language);
  };

  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">{t("profiles.typeHint")}</p>
      <Table mobile="stack" data-testid="profile-services">
        <TableHeader>
          <TableRow>
            <TableHead>{t("profiles.services.columns.service")}</TableHead>
            <TableHead className="hidden sm:table-cell">{t("profiles.services.columns.environment")}</TableHead>
            <TableHead>{t("profiles.services.columns.type")}</TableHead>
            <TableHead className="text-right">{t("profiles.services.columns.total")}</TableHead>
            <TableHead className="hidden text-right md:table-cell">{t("profiles.services.columns.samples")}</TableHead>
            <TableHead className="hidden md:table-cell">{t("profiles.services.columns.lastSeen")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {services.data.map((s) => (
            <TableRow key={`${s.service}-${s.environment}-${s.type}`} className="cursor-pointer" onClick={() => open(s)}>
              <TableCell className="font-medium">{s.service}</TableCell>
              <TableCell className="hidden sm:table-cell">{s.environment || "–"}</TableCell>
              <TableCell className="font-mono text-xs">{s.type}</TableCell>
              <TableCell className="text-right tabular-nums">{formatValue(s.total, s.unit, i18n.language)}</TableCell>
              <TableCell className="hidden text-right tabular-nums md:table-cell">
                {new Intl.NumberFormat(i18n.language).format(s.samples)}
              </TableCell>
              <TableCell className="hidden md:table-cell">{when(s.last_seen)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function ProfileDetail({ search }: { search: ProfilesSearch }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const range = rangeOf(search);
  const service = search.service ?? "";
  const type = search.type ?? "";

  const flame = useQuery(profileFlameQuery(range, service, type, search.env));
  const functions = useQuery(profileFunctionsQuery(range, service, type, search.env));

  const back = () =>
    void navigate({
      to: "/profiles",
      search: (prev: Record<string, unknown>) => rangeOf(prev as ProfilesSearch) as never,
    });

  const tab = search.tab ?? "flame";

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="ghost" size="sm" onClick={back}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("profiles.title")}
        </Button>
        <span className="font-medium">{service}</span>
        <span className="font-mono text-xs text-muted-foreground">{type}</span>
        {search.env && <span className="text-xs text-muted-foreground">· {search.env}</span>}
      </div>

      <Tabs
        value={tab}
        onValueChange={(v) =>
          void navigate({
            to: "/profiles",
            search: (prev: Record<string, unknown>) => ({ ...(prev as ProfilesSearch), tab: v as ProfilesSearch["tab"] }) as never,
          })
        }
      >
        <TabsList>
          <TabsTrigger value="flame">{t("profiles.tabs.flame")}</TabsTrigger>
          <TabsTrigger value="functions">{t("profiles.tabs.functions")}</TabsTrigger>
        </TabsList>

        <TabsContent value="flame">
          {flame.isPending ? (
            <LoadingState />
          ) : flame.isError ? (
            <ErrorState error={flame.error} onRetry={() => void flame.refetch()} />
          ) : (
            <FlameGraph flame={flame.data.flame} unit={flame.data.unit} />
          )}
        </TabsContent>

        <TabsContent value="functions">
          {functions.isPending ? (
            <LoadingState />
          ) : functions.isError ? (
            <ErrorState error={functions.error} onRetry={() => void functions.refetch()} />
          ) : (
            <FunctionsTable functions={functions.data.functions} total={functions.data.total} unit={functions.data.unit} />
          )}
        </TabsContent>
      </Tabs>
    </div>
  );
}
