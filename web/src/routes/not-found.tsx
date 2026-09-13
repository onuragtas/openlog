import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { EmptyState } from "@/components/StateViews";

export function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <EmptyState className="h-full">
      <p className="mb-2 font-medium text-foreground">{t("common.notFound")}</p>
      <Link to="/hosts" className="text-primary underline-offset-4 hover:underline">
        {t("nav.hosts")}
      </Link>
    </EmptyState>
  );
}
