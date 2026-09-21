import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { HostUsage } from "@/api/types";
import { formatLoad, formatShare, HostUsageCells, UsageBar, usageLevel } from "./UsageBar";

describe("host usage", () => {
  it("colours late, not early: working is not failing", () => {
    expect(usageLevel(0.0)).toBe("ok");
    expect(usageLevel(0.79)).toBe("ok");
    expect(usageLevel(0.8)).toBe("warning");
    expect(usageLevel(0.89)).toBe("warning");
    expect(usageLevel(0.9)).toBe("critical");
    // A host that reported nothing has no level at all, which is not the same as 0 %.
    expect(usageLevel(null)).toBe("none");
    expect(usageLevel(undefined)).toBe("none");
    expect(usageLevel(Number.NaN)).toBe("none");
  });

  it("formats a share and a load the way the row reads them", () => {
    expect(formatShare(0.715, "en")).toBe("72%");
    expect(formatShare(0, "en")).toBe("0%");
    expect(formatShare(null)).toBe("–");
    expect(formatLoad(2.4, "en")).toBe("2.4");
    expect(formatLoad(null)).toBe("–");
  });

  it("names the bar for a screen reader and shows the number in text", () => {
    render(<UsageBar label="CPU" share={0.34} />);
    const bar = screen.getByTestId("usage-bar");
    expect(within(bar).getByRole("img", { name: "CPU: 34%" })).toBeInTheDocument();
    expect(within(bar).getByText("34%")).toBeInTheDocument();
  });

  it("says why a host has no figures instead of drawing three empty bars", () => {
    const empty: HostUsage = { cpu: null, memory: null, disk: null, load1: null, load_per_cpu: null };
    render(<HostUsageCells usage={empty} />);
    expect(screen.getByText("No metric in the last 5 minutes")).toBeInTheDocument();
    expect(screen.queryByTestId("usage-bar")).not.toBeInTheDocument();
  });

  it("draws one bar per resource when the host is reporting", () => {
    const usage: HostUsage = { cpu: 0.34, memory: 0.71, disk: 0.88, load1: 2.4, load_per_cpu: 0.6 };
    render(<HostUsageCells usage={usage} />);
    expect(screen.getAllByTestId("usage-bar")).toHaveLength(3);
    expect(screen.getByText("88%")).toBeInTheDocument();
  });
});
