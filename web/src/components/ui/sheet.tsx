// Sheet: a Radix Dialog sliding in from an edge (focus trap, Esc and overlay close, scroll lock).
// Used for the navigation drawer and for dialogs that become bottom sheets on phones.
import { X } from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/utils";

export const Sheet = DialogPrimitive.Root;
export const SheetTrigger = DialogPrimitive.Trigger;
export const SheetClose = DialogPrimitive.Close;

const SIDES = {
  left: "inset-y-0 left-0 h-full w-72 max-w-[85vw] border-r",
  right: "inset-y-0 right-0 h-full w-80 max-w-[90vw] border-l",
  bottom: "inset-x-0 bottom-0 max-h-[85dvh] rounded-t-2xl border-t pb-[env(safe-area-inset-bottom)]",
} as const;

export interface SheetContentProps extends ComponentProps<typeof DialogPrimitive.Content> {
  side?: keyof typeof SIDES;
  /** Accessible title (required by the dialog pattern). */
  title: string;
  /** Show the title as a header row with a close button (default true). */
  showHeader?: boolean;
  closeLabel?: string;
  overlayClassName?: string;
  children: ReactNode;
}

export function SheetContent({ side = "bottom", title, showHeader = true, closeLabel, overlayClassName, className, children, ...props }: SheetContentProps) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay data-slot="sheet-overlay" className={cn("sheet-overlay fixed inset-0 z-40 bg-black/50", overlayClassName)} />
      <DialogPrimitive.Content
        data-slot="sheet"
        data-side={side}
        aria-describedby={undefined}
        className={cn("sheet fixed z-50 flex flex-col bg-card text-card-foreground shadow-xl outline-none", SIDES[side], className)}
        {...props}
      >
        {showHeader ? (
          <div className="flex min-h-14 shrink-0 items-center justify-between gap-2 border-b py-2 pr-2 pl-4">
            <DialogPrimitive.Title className="min-w-0 truncate text-base font-semibold">{title}</DialogPrimitive.Title>
            <DialogPrimitive.Close
              aria-label={closeLabel}
              className="inline-flex size-10 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground"
            >
              <X className="size-5" aria-hidden="true" />
            </DialogPrimitive.Close>
          </div>
        ) : (
          <DialogPrimitive.Title className="sr-only">{title}</DialogPrimitive.Title>
        )}
        {children}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}
