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
