import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";

/** Two-step destructive button: the first click asks for confirmation. */
export function ConfirmButton({ label, confirmLabel, onConfirm, pending = false }: { label: string; confirmLabel: string; onConfirm: () => void; pending?: boolean }) {
  const { t } = useTranslation();
  const [armed, setArmed] = useState(false);
  if (!armed) {
    return (
      <Button type="button" variant="outline" size="sm" disabled={pending} onClick={() => setArmed(true)}>
        {label}
      </Button>
    );
  }
  return (
    <span className="inline-flex gap-1">
      <Button
        type="button"
        variant="destructive"
        size="sm"
        disabled={pending}
        onClick={() => {
          setArmed(false);
          onConfirm();
        }}
      >
        {confirmLabel}
      </Button>
      <Button type="button" variant="ghost" size="sm" onClick={() => setArmed(false)}>
        {t("common.cancel")}
      </Button>
    </span>
  );
}
