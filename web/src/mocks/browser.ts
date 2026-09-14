import { setupWorker } from "msw/browser";
import { mockAuth } from "./account";
import { handlers } from "./handlers";
import { reportMockHost } from "./onboarding";

// Keep the emulated session across reloads (and let Playwright end it).
mockAuth.persistIn(window.sessionStorage);

// dev:mock / Playwright hook: emulate an agent that was just installed (Add data verification step).
(window as unknown as { __openlogMock?: { reportHost: (name: string) => string } }).__openlogMock = { reportHost: reportMockHost };

export const worker = setupWorker(...handlers);
