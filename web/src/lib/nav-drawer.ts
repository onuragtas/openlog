import { useState } from "react";

/**
 * Open state of the off-canvas navigation drawer. It closes whenever the location changes
 * (link taps, back/forward), adjusting state during render instead of in an effect.
 */
export function useNavDrawer(pathname: string): [boolean, (open: boolean) => void] {
  const [state, setState] = useState({ open: false, pathname });
  if (state.pathname !== pathname) setState({ open: false, pathname });
  const open = state.pathname === pathname && state.open;
  return [open, (next: boolean) => setState({ open: next, pathname })];
}
