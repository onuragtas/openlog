import { useMutation } from "@tanstack/react-query";
import { useId, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";

/** Operator action with confirmation: the first click opens a form that requires a reason (audit log). */
export function ReasonAction({
  label,
  destructive = false,
  disabled = false,
  hint,
  children,
  onConfirm,
}: {
  label: string;
  destructive?: boolean;
  disabled?: boolean;
  hint?: string;
  children?: ReactNode;
  onConfirm: (reason: string) => Promise<unknown>;
}) {
  const { t } = useTranslation();
  const id = useId();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [done, setDone] = useState(false);
  const run = useMutation({
    mutationFn: () => onConfirm(reason.trim()),
    onSuccess: () => {
      setOpen(false);
      setReason("");
      setDone(true);
    },
  });
  if (!open) {
    return (
      <span className="inline-flex items-center gap-2">
        <Button
          type="button"
          size="sm"
          variant={destructive ? "destructive" : "outline"}
          disabled={disabled}
          title={disabled ? hint : undefined}
          onClick={() => {
            setDone(false);
            setOpen(true);
          }}
        >
          {label}
        </Button>
        {done && (
          <span role="status" className="text-xs text-muted-foreground">
            {t("operator.actions.done")}
          </span>
        )}
      </span>
    );
  }
  return (
    <form
      aria-label={label}
      className="flex w-full flex-col gap-2 rounded-lg border bg-card p-3"
      onSubmit={(e) => {
        e.preventDefault();
        run.mutate();
      }}
    >
      <span className="text-sm font-medium">{label}</span>
      {children}
      <label htmlFor={`${id}-reason`} className="text-xs text-muted-foreground">
        {t("operator.actions.reason")}
      </label>
      <textarea
        id={`${id}-reason`}
        required
        minLength={3}
        maxLength={1000}
        rows={2}
        autoFocus
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm pointer-coarse:text-base"
      />
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" variant={destructive ? "destructive" : "default"} disabled={run.isPending || reason.trim().length < 3}>
          {t("operator.actions.confirm")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={() => {
            setOpen(false);
            run.reset();
          }}
        >
          {t("common.cancel")}
        </Button>
      </div>
      <FormError error={run.error} />
    </form>
  );
}
