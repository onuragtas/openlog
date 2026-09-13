import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";

/** Two-step button: the first click asks for confirmation (like settings/ConfirmButton, any tone). */
export function ConfirmAction({
  label,
  confirmLabel,
  onConfirm,
  pending = false,
  disabled = false,
  destructive = false,
}: {
  label: string;
  confirmLabel: string;
  onConfirm: () => void;
  pending?: boolean;
  disabled?: boolean;
  destructive?: boolean;
}) {
  const { t } = useTranslation();
  const [armed, setArmed] = useState(false);
  if (!armed) {
    return (
      <Button type="button" variant="outline" size="sm" disabled={pending || disabled} onClick={() => setArmed(true)}>
        {label}
      </Button>
    );
  }
  return (
    <span className="inline-flex gap-1">
      <Button
        type="button"
        variant={destructive ? "destructive" : "default"}
        size="sm"
        disabled={pending}
        autoFocus
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
