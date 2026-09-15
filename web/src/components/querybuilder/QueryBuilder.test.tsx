import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { FilterState } from "@/api/explorer";
import { QueryBuilder } from "@/components/querybuilder/QueryBuilder";
import { server } from "@/mocks/server";

function setup(initial: FilterState = { filters: [], groups: [], q: "" }) {
  server.use(
    http.get("*/api/v1/fields/keys", () =>
      HttpResponse.json({
        keys: [
          { key: "service.name", name: "service.name", source: "field", type: "string", count: null, cardinality: null },
          { key: "attributes.http.route", name: "http.route", source: "attribute", type: "string", count: 12, cardinality: 3 },
          { key: "attributes.http.response.status_code", name: "http.response.status_code", source: "attribute", type: "number", count: 12, cardinality: 2 },
        ],
        sampled: false,
      }),
    ),
    http.get("*/api/v1/fields/values", ({ request }) => {
      const key = new URL(request.url).searchParams.get("key");
      return HttpResponse.json({ key, type: "string", values: [{ value: "/cart", count: 7 }, { value: "/checkout", count: 5 }], sampled: false });
    }),
  );
  const onChange = vi.fn();
  function Harness() {
    const [value, setValue] = useState(initial);
    return (
      <QueryBuilder
        signal="logs"
        range={{ range: "1h" }}
        value={value}
        onChange={(v) => {
          onChange(v);
          setValue(v);
        }}
      />
    );
  }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <Harness />
    </QueryClientProvider>,
  );
  return { onChange, user: userEvent.setup() };
}

describe("QueryBuilder", () => {
  it("parses typed conditions into chips and other text into a body search", async () => {
    const { onChange, user } = setup();
    const input = screen.getByRole("combobox", { name: "Filters" });
    await user.type(input, "severity_text IN (ERROR, WARN){Enter}");
    expect(onChange).toHaveBeenLastCalledWith({ filters: [{ key: "severity_text", op: "in", values: ["ERROR", "WARN"] }], groups: [], q: "" });
    expect(screen.getByRole("button", { name: "Edit condition severity_text IN (ERROR, WARN)" })).toBeInTheDocument();
    await user.type(screen.getByRole("combobox", { name: "Filters" }), "connection reset");
    await user.click(await screen.findByRole("option", { name: /Search log bodies for “connection reset”/ }));
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ q: "connection reset" }));
  });

  it("builds a condition from key, operator and value suggestions", async () => {
    const { onChange, user } = setup();
    const input = screen.getByRole("combobox", { name: "Filters" });
    await user.type(input, "route");
    const listbox = await screen.findByRole("listbox");
    await user.click(await within(listbox).findByRole("option", { name: /attributes\.http\.route/ }));
    // String keys offer text operators but no numeric comparisons.
    expect(await screen.findByRole("option", { name: /^CONTAINS/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /^>=/ })).not.toBeInTheDocument();
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("option", { name: /\/checkout/ }));
    expect(onChange).toHaveBeenLastCalledWith({ filters: [{ key: "attributes.http.route", op: "=", value: "/checkout" }], groups: [], q: "" });
    expect(input).toHaveAttribute("aria-expanded", "true");
  });

  it("edits the last chip with Backspace and removes chips", async () => {
    const { onChange, user } = setup({ filters: [{ key: "service.name", op: "=", value: "api" }, { key: "k8s.pod.name", op: "exists" }], groups: [], q: "" });
    const input = screen.getByRole("combobox", { name: "Filters" });
    await user.click(input);
    await user.keyboard("{Backspace}");
    expect(screen.queryByRole("button", { name: "Edit condition k8s.pod.name EXISTS" })).not.toBeInTheDocument();
    expect(screen.getByTestId("filter-draft")).toHaveTextContent("k8s.pod.name");
    await user.keyboard("{Escape}{Escape}{Escape}");
    await user.click(screen.getByRole("button", { name: "Remove condition service.name = api" }));
    expect(onChange).toHaveBeenLastCalledWith({ filters: [{ key: "k8s.pod.name", op: "exists" }], groups: [], q: "" });
  });

  it("adds OR groups", async () => {
    const { onChange, user } = setup({ filters: [{ key: "service.name", op: "=", value: "api" }], groups: [], q: "" });
    await user.click(screen.getByRole("button", { name: "OR" }));
    const lane = screen.getByRole("combobox", { name: "OR group 2" });
    expect(lane).toHaveFocus();
    await user.type(lane, "http.response.status_code >= 500{Enter}");
    expect(onChange).toHaveBeenLastCalledWith({
      filters: [],
      groups: [[{ key: "service.name", op: "=", value: "api" }], [{ key: "http.response.status_code", op: ">=", value: 500 }]],
      q: "",
    });
    await user.click(screen.getByRole("button", { name: "Clear all" }));
    expect(onChange).toHaveBeenLastCalledWith({ filters: [], groups: [], q: "" });
  });
});
