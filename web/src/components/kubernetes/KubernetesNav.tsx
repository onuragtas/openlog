import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

const SECTIONS = [
  { to: "/kubernetes", key: "overview" },
  { to: "/kubernetes/workloads", key: "workloads" },
  { to: "/kubernetes/pods", key: "pods" },
  { to: "/kubernetes/nodes", key: "nodes" },
] as const;

/** Section links of the Kubernetes screens; the selected cluster and time range travel along. */
export function KubernetesNav({ active }: { active: (typeof SECTIONS)[number]["key"] }) {
  const { t } = useTranslation();
  return (
    <nav aria-label={t("kubernetes.nav.label")} className="-mx-1 mb-4 overflow-x-auto">
      <ul className="flex min-w-max gap-1 border-b px-1">
        {SECTIONS.map((s) => (
          <li key={s.key}>
            <Link
              to={s.to}
              search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to, cluster: prev.cluster }) as never}
              aria-current={active === s.key ? "page" : undefined}
              className={cn(
                "-mb-px inline-flex min-h-10 items-center border-b-2 px-3 text-sm",
                active === s.key ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              {t(`kubernetes.nav.${s.key}`)}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}
