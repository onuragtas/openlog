import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { operatorMeQuery } from "@/api/operator";
import { useSupportSessionId } from "./useSupportSession";

/** Navigation entry of the operator console: only for operators, hidden inside a support view. */
export function OperatorNavLink({ className, onNavigate }: { className?: string; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const me = useMe().data;
  const inSupport = useSupportSessionId() !== null;
  const op = useQuery({ ...operatorMeQuery(), enabled: !!me?.user && !inSupport }).data;
  if (!op?.operator || inSupport) return null;
  return (
    <Link to="/operator" onClick={onNavigate} className={className}>
      <ShieldCheck className="size-4 shrink-0" aria-hidden="true" />
      <span>{t("nav.operator")}</span>
    </Link>
  );
}
