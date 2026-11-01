import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { HOST_IDS } from "@/mocks/fixtures";
import { server } from "@/mocks/server";
import { advisoryLabel, formatScore, packagesText, upgradeText, withoutFix } from "@/lib/vulnerabilities";
import { HostVulnerabilities } from "./HostVulnerabilities";
import { VulnerabilitiesList } from "./VulnerabilitiesList";
import { VulnerabilityDetail } from "./VulnerabilityDetail";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("Vulnerabilities", () => {
  it("asks the server for another order and keeps the counters of the whole list", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<VulnerabilitiesList onOpen={vi.fn()} />);
    await screen.findAllByTestId("vulnerability-row");
    const scores = () => screen.getAllByTestId("vulnerability-row").map((r) => Number(within(r).getAllByRole("cell")[2]!.textContent));
    const total = screen.getByRole("button", { name: /^All/ }).textContent;

    await user.click(screen.getByRole("button", { name: "CVSS" }));
    await vi.waitFor(() => {
      const s = scores();
      expect(s).toEqual([...s].sort((a, b) => b - a));
    });
    expect(screen.getByRole("columnheader", { name: "CVSS" })).toHaveAttribute("aria-sort", "descending");
    expect(screen.getByRole("button", { name: /^All/ }).textContent).toBe(total);
  });

  it("groups the findings by advisory, most serious first", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    renderWithClient(<VulnerabilitiesList onOpen={onOpen} />);

    const list = await screen.findByTestId("vulnerabilities-list");
    const rows = within(list).getAllByTestId("vulnerability-row");
    expect(rows).toHaveLength(3);
    // The critical advisory is first and says how many hosts it affects — the number a person works down.
    expect(within(rows[0]!).getByText("DSA-5600-1 · CVE-2026-0001")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("9.8")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("2")).toBeInTheDocument();

    await user.click(within(rows[0]!).getByText("DSA-5600-1 · CVE-2026-0001"));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ vuln_id: "DSA-5600-1" }));
  });

  it("says when the catalog was synced, so an empty list can be read", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VulnerabilitiesList onOpen={vi.fn()} />);
    await screen.findByTestId("vulnerabilities-list");
    expect(screen.getByText(/Catalog synced/)).toBeInTheDocument();
  });

  it("shows the hosts of one advisory with what upgrading means", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VulnerabilityDetail id="DSA-5600-1" />);

    const table = await screen.findByTestId("vulnerability-hosts");
    expect(within(table).getByText("web-1.shop.internal")).toBeInTheDocument();
    expect(within(table).getAllByText("1.2.3-4+deb12u1 → 1.2.3-4+deb12u2")).toHaveLength(2);
  });

  it("calls out an advisory with no fix", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VulnerabilityDetail id="CVE-2026-0042" />);
    await screen.findByTestId("vulnerability-detail");
    expect(screen.getByText(/no fix for yet/)).toBeInTheDocument();
  });

  it("lists the findings of one host", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<HostVulnerabilities hostId={HOST_IDS.web} />);

    const table = await screen.findByTestId("host-vulnerabilities");
    expect(within(table).getByText("DSA-5600-1 · CVE-2026-0001")).toBeInTheDocument();
    expect(within(table).getByText("openssl")).toBeInTheDocument();
    // A finding without a fixed version shows the installed one alone rather than an empty arrow.
    expect(within(table).getByText("3.0.11-1~deb12u2")).toBeInTheDocument();
  });

  it("translates the severity labels", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("tr");
    renderWithClient(<VulnerabilitiesList onOpen={vi.fn()} />);
    const list = await screen.findByTestId("vulnerabilities-list");
    expect(within(list).getAllByText("Kritik").length).toBeGreaterThan(0);
    expect(within(list).getByText("Duyuru")).toBeInTheDocument();
  });
});

describe("the findings lists", () => {
  const finding = { host_id: "h1", host_name: "web-1", vuln_id: "CVE-1", cve: "CVE-1", severity: "low", score: 1, ecosystem: "Debian:12", package: "curl", version: "1.0", fixed_in: "1.1", summary: "the row's summary", first_seen: "", last_seen: "" };
  const advisory = { cve: "CVE-1", severity: "critical", score: 9.8, summary: "the advisory's summary", findings: 60, without_fix: 7 };

  it("sends a heading and Next of an advisory to the server, with a header that does not come from the rows", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const seen: URLSearchParams[] = [];
    server.use(
      http.get("*/api/v1/vulnerabilities/:id", ({ request }) => {
        const p = new URL(request.url).searchParams;
        seen.push(p);
        // Past the last page the server answers 200 with nothing in it, which must read as an empty page and not as "not found".
        return HttpResponse.json(p.get("offset") === "0" ? { vuln_id: "CVE-1", findings: [finding], advisory, has_more: true } : { vuln_id: "CVE-1", findings: [], advisory, has_more: false });
      }),
    );
    const last = () => seen[seen.length - 1]!;
    renderWithClient(<VulnerabilityDetail id="CVE-1" />);
    const table = await screen.findByTestId("vulnerability-hosts");
    expect(screen.getByText("the advisory's summary")).toBeInTheDocument();
    expect(screen.getByText("Critical")).toBeInTheDocument();
    expect(screen.getByText(/7 hosts run a version/)).toBeInTheDocument();
    expect(screen.queryByText("the row's summary")).not.toBeInTheDocument();

    await user.click(within(table).getByRole("button", { name: "Package" }));
    await waitFor(() => expect(last().get("sort")).toBe("package"));
    expect(last().get("dir")).toBe("asc");

    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(last().get("offset")).toBe("50"));
    await waitFor(() => expect(within(screen.getByTestId("vulnerability-hosts")).queryByText("web-1")).not.toBeInTheDocument());
    expect(screen.getByText("the advisory's summary")).toBeInTheDocument();
    expect(screen.getByText("Critical")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Previous" })).toBeEnabled();
  });

  it("goes by the first row on a server older than the advisory summary", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    server.use(http.get("*/api/v1/vulnerabilities/:id", () => HttpResponse.json({ vuln_id: "CVE-1", findings: [{ ...finding, fixed_in: "" }] })));
    renderWithClient(<VulnerabilityDetail id="CVE-1" />);
    await screen.findByTestId("vulnerability-hosts");
    expect(screen.getByText("the row's summary")).toBeInTheDocument();
    expect(screen.getByText(/1 host runs a version/)).toBeInTheDocument();
  });

  it("sends a heading and Next of a host to the server, and an empty later page is not an empty host", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const seen: URLSearchParams[] = [];
    server.use(
      http.get("*/api/v1/hosts/:hostId/vulnerabilities", ({ request }) => {
        const p = new URL(request.url).searchParams;
        seen.push(p);
        return HttpResponse.json(p.get("offset") === "0" ? { host_id: "h1", findings: [finding], has_more: true } : { host_id: "h1", findings: [], has_more: false });
      }),
    );
    const last = () => seen[seen.length - 1]!;
    renderWithClient(<HostVulnerabilities hostId="h1" />);
    const table = await screen.findByTestId("host-vulnerabilities");
    await user.click(within(table).getByRole("button", { name: "CVSS" }));
    await waitFor(() => expect(last().get("sort")).toBe("score"));
    expect(last().get("dir")).toBe("desc");

    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(last().get("offset")).toBe("50"));
    expect(await screen.findByRole("button", { name: "Previous" })).toBeEnabled();
    expect(screen.queryByText(i18n.t("vulnerabilities.host.empty"))).not.toBeInTheDocument();
  });
});

describe("vulnerability helpers", () => {
  it("formats what the tables show", () => {
    expect(formatScore(9.8)).toBe("9.8");
    expect(formatScore(0)).toBe("–");
    expect(formatScore(null)).toBe("–");
    expect(advisoryLabel({ vuln_id: "CVE-1", cve: "CVE-1" })).toBe("CVE-1");
    expect(advisoryLabel({ vuln_id: "DSA-1", cve: "CVE-1" })).toBe("DSA-1 · CVE-1");
    expect(upgradeText({ version: "1.0", fixed_in: "1.1" })).toBe("1.0 → 1.1");
    expect(upgradeText({ version: "1.0", fixed_in: "" })).toBe("1.0");
    expect(packagesText(["a", "b"], 3)).toBe("a, b");
    expect(packagesText(["a", "b", "c", "d"], 3)).toBe("a, b, c +1");
  });

  it("finds the advisories that cannot be resolved by upgrading", () => {
    const findings = [{ fixed_in: "1.1" }, { fixed_in: "" }] as unknown as Parameters<typeof withoutFix>[0];
    expect(withoutFix(findings)).toHaveLength(1);
  });
});
