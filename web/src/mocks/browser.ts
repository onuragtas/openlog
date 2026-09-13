import { setupWorker } from "msw/browser";
import { mockAuth } from "./account";
import { handlers } from "./handlers";

// Keep the emulated session across reloads (and let Playwright end it).
mockAuth.persistIn(window.sessionStorage);

export const worker = setupWorker(...handlers);
