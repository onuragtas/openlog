/** A touch-opened tooltip closes after this long without touches. */
export const TOUCH_TOOLTIP_IDLE_MS = 4000;
/** Compatibility mouse events a browser emits this soon after a touch are ignored. */
export const TOUCH_MOUSE_SUPPRESS_MS = 800;
/** A touch that moves less than this is a tap, not a drag. */
export const TOUCH_TAP_SLOP_PX = 8;

/**
 * State of a chart tooltip opened by touch (pure; the uPlot plugin wires DOM events and timers to it).
 * Tapping a point opens it, tapping the same point again closes it; a horizontal drag moves it; tapping
 * outside, scrolling and TOUCH_TOOLTIP_IDLE_MS without touches close it.
 */
export class TouchTooltipState {
  /** Data index the tooltip is open at, or null. */
  openIdx: number | null = null;
  private lastTouch = Number.NEGATIVE_INFINITY;
  private downX = 0;

  /** Touch down at data index `idx` (x in px). Returns whether the tooltip is open afterwards. */
  down(idx: number | null, x: number, now: number): boolean {
    this.lastTouch = now;
    this.downX = x;
    this.openIdx = idx === null || idx === this.openIdx ? null : idx;
    return this.openIdx !== null;
  }

  /** Touch move while pressed. Returns the new open index, or undefined when the move is within the tap slop. */
  move(idx: number | null, x: number, now: number): number | null | undefined {
    this.lastTouch = now;
    // Finger jitter during a tap must not reopen a tooltip the tap just closed.
    if (Math.abs(x - this.downX) < TOUCH_TAP_SLOP_PX) return undefined;
    this.openIdx = idx;
    return idx;
  }

  /** Closes the tooltip (outside tap, scroll, idle). Returns whether it was open. */
  close(): boolean {
    const was = this.openIdx !== null;
    this.openIdx = null;
    return was;
  }

  /** Whether a compatibility mouse event at `now` should be ignored. */
  suppressMouse(now: number): boolean {
    return now - this.lastTouch < TOUCH_MOUSE_SUPPRESS_MS;
  }

  /** Whether the idle timeout elapsed at `now`. */
  idle(now: number): boolean {
    return now - this.lastTouch >= TOUCH_TOOLTIP_IDLE_MS;
  }
}

export interface TooltipPlacementInput {
  /** Cursor position inside the plot area (px). */
  x: number;
  y: number;
  /** Tooltip size (px). */
  width: number;
  height: number;
  /** Plot area size (px). */
  areaWidth: number;
  areaHeight: number;
  /** Extra room left of the plot area the tooltip may use (e.g. the y axis), in px. */
  leftRoom?: number;
  gap?: number;
}

/**
 * Tooltip position relative to the plot area: right of the cursor, flipped to the left when it
 * would overflow, then clamped so it stays inside the chart (and so inside the viewport) even
 * when the tooltip is wider than the plot on a phone.
 */
export function placeTooltip({ x, y, width, height, areaWidth, areaHeight, leftRoom = 0, gap = 12 }: TooltipPlacementInput): { left: number; top: number } {
  let left = x + gap;
  if (left + width > areaWidth) left = x - gap - width;
  left = Math.max(leftRoom > 0 ? -leftRoom : 0, Math.min(left, areaWidth - width));
  let top = Math.max(0, y - 10);
  if (top + height > areaHeight) top = Math.max(0, areaHeight - height);
  return { left: Math.round(left), top: Math.round(top) };
}
