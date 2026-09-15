import { describe, expect, it } from "vitest";
import {
  AGENT_CONFIG_PATH,
  agentLog,
  agentRestart,
  agentSelfTest,
  hostHasContainers,
  hostHasPhpForwarder,
  hostHasPhpInstall,
  hostLogsYaml,
  hostOs,
  hostOsOf,
  yamlPath,
} from "./host-os";

describe("host OS helpers (D-112)", () => {
  it("normalizes os.type; unknown and missing values are Linux", () => {
    expect(hostOs("linux")).toBe("linux");
    expect(hostOs("darwin")).toBe("darwin");
    expect(hostOs("macOS")).toBe("darwin");
    expect(hostOs("windows")).toBe("windows");
    expect(hostOs("freebsd")).toBe("linux");
    expect(hostOs(undefined)).toBe("linux");
    expect(hostOsOf({ resource_attributes: { "os.type": "windows" } })).toBe("windows");
    expect(hostOsOf(null)).toBe("linux");
  });

  it("restart, agent log and self-test per OS with the shell language", () => {
    expect(agentRestart("linux")).toEqual({ code: "sudo systemctl restart openlog-infra-agent", lang: "sh" });
    expect(agentRestart("darwin")).toEqual({ code: "sudo launchctl kickstart -k system/org.openlog.infra-agent", lang: "sh" });
    expect(agentRestart("windows")).toEqual({ code: "Restart-Service openlog-infra-agent", lang: "powershell" });
    expect(agentLog("linux").code).toBe("journalctl -u openlog-infra-agent -e");
    expect(agentLog("darwin").code).toBe("tail -f /var/log/openlog-infra-agent.log");
    expect(agentLog("windows").code).toContain("C:\\ProgramData\\openlog\\infra-agent\\logs\\openlog-infra-agent.log");
    expect(agentSelfTest("windows").code).toContain("-config C:\\ProgramData\\openlog\\infra-agent\\config.yaml");
    expect(AGENT_CONFIG_PATH.darwin).toBe("/etc/openlog-infra-agent/config.yaml");
  });

  it("logs section: journald on Linux, unified log predicate on macOS, Event Log channels on Windows", () => {
    expect(hostLogsYaml("linux", "/var/log/app/*.log", true)).toBe(
      '# /etc/openlog-infra-agent/config.yaml\nlogs:\n  enabled: true\n  files:\n    - path: "/var/log/app/*.log"\n  journald:\n    enabled: true',
    );
    const mac = hostLogsYaml("darwin", "", true);
    expect(mac).toContain('    - path: "/opt/homebrew/var/log/nginx/*.log"');
    expect(mac).toContain("  unified_log:\n    enabled: true\n    level: default\n    predicate: 'subsystem == \"com.example.app\" OR process == \"nginx\"'");
    expect(mac).not.toContain("journald");
    const win = hostLogsYaml("windows", "", true);
    expect(win.split("\n")[0]).toBe("# C:\\ProgramData\\openlog\\infra-agent\\config.yaml");
    expect(win).toContain("    - path: 'C:\\inetpub\\logs\\LogFiles\\W3SVC1\\*.log'");
    expect(win).toContain("  windows_event_log:\n    enabled: true\n    channels:\n      - { name: System, levels: [critical, error, warning] }");
    expect(hostLogsYaml("windows", "C:\\logs\\*.log", false)).not.toContain("windows_event_log");
    expect(yamlPath("it's\\x")).toBe('"it\'s\\\\x"');
  });

  it("features per OS: containers everywhere but Windows, PHP forwarder not on Windows, PHP install Linux only", () => {
    expect([hostHasContainers("linux"), hostHasContainers("darwin"), hostHasContainers("windows"), hostHasContainers(undefined)]).toEqual([true, true, false, true]);
    expect([hostHasPhpForwarder("linux"), hostHasPhpForwarder("darwin"), hostHasPhpForwarder("windows")]).toEqual([true, true, false]);
    expect([hostHasPhpInstall("linux"), hostHasPhpInstall("darwin"), hostHasPhpInstall("windows")]).toEqual([true, false, false]);
  });
});
