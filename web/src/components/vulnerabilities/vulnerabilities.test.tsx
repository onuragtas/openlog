import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { HOST_IDS } from "@/mocks/fixtures";
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
