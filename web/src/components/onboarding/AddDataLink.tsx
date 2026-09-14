import { Link } from "@tanstack/react-router";
import { PlusCircle } from "lucide-react";
import { buttonVariants } from "@/components/ui/button";
import type { TargetId } from "@/lib/install-commands";
import { cn } from "@/lib/utils";

/** Call to action of an empty state: opens the matching Add data card (or the catalog). */
export function AddDataLink({ target, label, className }: { target?: TargetId; label: string; className?: string }) {
  const cls = cn(buttonVariants({ size: "sm" }), "mt-3", className);
  return target ? (
    <Link to="/add-data/$" params={{ _splat: target }} className={cls} data-testid="add-data-link">
      <PlusCircle aria-hidden="true" />
      {label}
    </Link>
  ) : (
    <Link to="/add-data" className={cls} data-testid="add-data-link">
      <PlusCircle aria-hidden="true" />
      {label}
    </Link>
  );
}
