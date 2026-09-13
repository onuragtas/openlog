import { act, render, renderHook, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { TimeRangePicker } from "@/components/TimeRangePicker";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { useNavDrawer } from "@/lib/nav-drawer";

describe("useNavDrawer", () => {
  it("opens, closes and closes on navigation", () => {
    const { result, rerender } = renderHook(({ path }) => useNavDrawer(path), { initialProps: { path: "/hosts" } });
    expect(result.current[0]).toBe(false);
    act(() => result.current[1](true));
    expect(result.current[0]).toBe(true);
    rerender({ path: "/logs" });
    expect(result.current[0]).toBe(false);
    act(() => result.current[1](true));
    expect(result.current[0]).toBe(true);
    act(() => result.current[1](false));
    expect(result.current[0]).toBe(false);
  });
});

describe("Sheet", () => {
  it("traps focus inside and closes on Escape", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    render(
      <>
        <button type="button">outside</button>
        <Sheet open onOpenChange={onOpenChange}>
          <SheetContent side="left" title="Main navigation" closeLabel="Close menu">
            <a href="#a">Hosts</a>
          </SheetContent>
        </Sheet>
      </>,
    );
    const dialog = screen.getByRole("dialog", { name: "Main navigation" });
    expect(dialog).toContainElement(document.activeElement as HTMLElement);
    await user.tab();
    await user.tab();
    await user.tab();
    expect(dialog).toContainElement(document.activeElement as HTMLElement);
    await user.keyboard("{Escape}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});

describe("TimeRangePicker", () => {
  it("offers presets and a custom range in a select for small screens", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<TimeRangePicker value={{ range: "1h" }} onChange={onChange} />);
    const select = screen.getByRole("combobox", { name: "Time range" });
    expect(select).toHaveValue("1h");
    await user.selectOptions(select, "24h");
    expect(onChange).toHaveBeenCalledWith({ range: "24h" });
    await user.selectOptions(select, "custom");
    expect(screen.getByLabelText("From")).toHaveAttribute("type", "datetime-local");
    expect(onChange).toHaveBeenCalledTimes(1);
  });
});
