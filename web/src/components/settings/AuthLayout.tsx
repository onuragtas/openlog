import type { ReactNode } from "react";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { ThemeToggle } from "@/components/ThemeToggle";
import { Card } from "@/components/ui/card";

/** Centered card layout for the unauthenticated screens (sign-in, invitation). */
export function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-full flex-col">
      <header className="flex justify-end gap-2 p-4">
        <LanguageSwitch />
        <ThemeToggle />
      </header>
      <main className="flex flex-1 items-center justify-center p-4">
        <Card className="w-full max-w-md">{children}</Card>
      </main>
    </div>
  );
}
