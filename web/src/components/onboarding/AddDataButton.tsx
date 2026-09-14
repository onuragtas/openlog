import { Link } from "@tanstack/react-router";
import { PlusCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { buttonVariants } from "@/components/ui/button";

/** Prominent "Add data" button of the top bar (icon only on phones). */
export function AddDataButton() {
  const { t } = useTranslation();
  return (
    <Link to="/add-data" className={buttonVariants({ size: "sm", className: "shrink-0" })} aria-label={t("addData.nav")} data-testid="add-data-button">
      <PlusCircle aria-hidden="true" />
      <span className="hidden sm:inline">{t("addData.nav")}</span>
    </Link>
  );
}
