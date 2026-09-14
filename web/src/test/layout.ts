// jsdom has no layout: give scroll containers a viewport, elements a size and
// a ResizeObserver that reports it, so virtualized lists and charts render as in a browser.
import { vi } from "vitest";

export interface LayoutStub {
  viewportHeight: number;
  width: number;
  /** measured height of every element (virtual rows) */
  itemHeight: number;
}

export function stubLayout({ viewportHeight, width, itemHeight }: LayoutStub) {
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(viewportHeight);
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(width);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    bottom: itemHeight,
    right: width,
    width,
    height: itemHeight,
    toJSON: () => ({}),
  });
  const observers: FakeResizeObserver[] = [];
  class FakeResizeObserver {
    private readonly els = new Set<Element>();
    constructor(private readonly cb: ResizeObserverCallback) {
      observers.push(this);
    }
    observe(el: Element) {
      this.els.add(el);
      this.fire(width);
    }
    unobserve(el: Element) {
      this.els.delete(el);
    }
    disconnect() {
      this.els.clear();
    }
    fire(w: number) {
      const entries = [...this.els].map((target) => ({ target, contentRect: { width: w, height: itemHeight } }) as unknown as ResizeObserverEntry);
      if (entries.length) this.cb(entries, this as unknown as ResizeObserver);
    }
  }
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  return {
    /** Reports a new width to every connected observer. */
    resize: (w: number) => observers.forEach((o) => o.fire(w)),
    /** Observers that still watch at least one element. */
    connected: () => observers.filter((o) => (o as unknown as { els: Set<Element> }).els.size > 0).length,
  };
}
