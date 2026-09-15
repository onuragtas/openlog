import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { FleetPHPAccess } from "@/api/fleet";
import { PHPAccessNotice } from "./PHPAccessNotice";

const ACCESS: FleetPHPAccess = {
  socket_group: "openlog-php",
  group: "openlog-php",
  group_exists: true,
  agent_member: true,
  grants: "auto",
  pools: [
    { pool: "www", php_version: "8.2", user: "www-data", unit: "php8.2-fpm.service", access: "ok" },
    { pool: "shop", php_version: "8.3", user: "shop", unit: "php8.3-fpm.service", access: "missing" },
  ],
} as FleetPHPAccess;

describe("PHPAccessNotice per host OS", () => {
  it("Linux (and unknown OS): systemctl restart and usermod", () => {
    const { unmount } = render(<PHPAccessNotice access={ACCESS} os="linux" />);
    expect(screen.getByTestId("php-access-restart")).toHaveTextContent("sudo systemctl restart openlog-infra-agent");
    expect(screen.getByTestId("php-access-restart")).toHaveAttribute("data-lang", "sh");
    expect(screen.getByTestId("php-access-manual").textContent).toBe("sudo usermod -aG openlog-php shop && \\\n  sudo systemctl reload php8.3-fpm.service");
    unmount();
    render(<PHPAccessNotice access={ACCESS} />);
    expect(screen.getByTestId("php-access-restart")).toHaveTextContent("sudo systemctl restart openlog-infra-agent");
  });

  it("macOS: launchctl kickstart and dseditgroup, no systemctl or usermod", () => {
    render(<PHPAccessNotice access={ACCESS} os="darwin" />);
    expect(screen.getByTestId("php-access-restart")).toHaveTextContent("sudo launchctl kickstart -k system/org.openlog.infra-agent");
    const manual = screen.getByTestId("php-access-manual").textContent ?? "";
    expect(manual).toBe("sudo dseditgroup -o edit -a shop -t user openlog-php && \\\n  brew services restart php");
    expect(screen.getByTestId("php-access").textContent).not.toMatch(/systemctl|usermod/);
  });

  it("Windows: hidden (no PHP forwarder)", () => {
    const { container } = render(<PHPAccessNotice access={ACCESS} os="windows" />);
    expect(container).toBeEmptyDOMElement();
  });
});
