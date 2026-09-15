// Read-only organization controls (lib/org-writable.ts): disabled mutating controls with an explanation and a page notice.
import { Lock } from "lucide-react";
import { useId, type ReactNode } from "react";
import { useOrgWritable } from "@/lib/org-writable";
import { cn } from "@/lib/utils";

/**
 * Wraps mutating controls (buttons, forms): in a read-only organization they are disabled through a disabled fieldset
 * and the wrapper explains why (tooltip and screen reader description).
 */
export function WriteGuard({ children, className, block = false }: { children: ReactNode; className?: string; block?: boolean }) {
  const { writable, reason } = useOrgWritable();
  const id = useId();
  if (writable) return <>{children}</>;
  const Wrapper = block ? "div" : "span";
  return (
    <Wrapper className={cn(block ? "block" : "inline-flex", "cursor-not-allowed", className)} title={reason ?? undefined} data-testid="read-only-guard">
      <fieldset disabled aria-describedby={`${id}-reason`} className="contents">
        {children}
      </fieldset>
      <span id={`${id}-reason`} className="sr-only">
        {reason}
      </span>
    </Wrapper>
  );
}

/** Notice at the top of a page whose changes are hidden or disabled in a read-only organization. */
export function ReadOnlyNotice({ className }: { className?: string }) {
  const { writable, reason } = useOrgWritable();
  if (writable) return null;
  return (
    <p role="note" data-testid="read-only-notice" className={cn("flex items-start gap-2 rounded-md border border-warning/60 bg-warning/10 px-3 py-2 text-sm", className)}>
      <Lock className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 break-words">{reason}</span>
    </p>
  );
}
