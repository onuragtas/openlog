import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

/** Styled native <select>: fully keyboard/screen-reader accessible without JS. */
export function NativeSelect({ className, ...props }: ComponentProps<"select">) {
  return (
    <select
      data-slot="native-select"
      className={cn(
        "h-9 rounded-md border border-input bg-background px-2 py-1 text-sm shadow-xs disabled:cursor-not-allowed disabled:opacity-50 pointer-coarse:h-10 pointer-coarse:text-base",
        className,
      )}
      {...props}
    />
  );
}
