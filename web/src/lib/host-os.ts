// Operating system specific commands, paths and configuration examples of the infra agent (D-104, D-112):
// agents/infra/README.md "Install on macOS", "Install on Windows" and "Configuration differences". Every host-scoped
// card picks its text from the host's `os.type`; an unknown or missing value keeps the Linux text.

/** `os.type` values the infra agent reports (semantic-conventions §1). */
export type HostOs = "linux" | "darwin" | "windows";
export const HOST_OSES: readonly HostOs[] = ["linux", "darwin", "windows"];

/** Shell of the commands shown for an OS (the `lang` of a command block). */
export type ShellLang = "sh" | "powershell";

export interface OsCommand {
  code: string;
  lang: ShellLang;
}

/** Normalizes `os.type` (or a similar OS name); anything unknown is Linux, the pre-D-104 behaviour. */
export function hostOs(osType: string | null | undefined): HostOs {
  const v = (osType ?? "").trim().toLowerCase();
  if (v === "darwin" || v === "macos" || v === "osx") return "darwin";
  if (v === "windows" || v === "win32") return "windows";
  return "linux";
}

/** The `os.type` of a host's resource attributes. */
export function hostOsOf(host: { resource_attributes?: Record<string, string> | null } | null | undefined): HostOs {
  return hostOs(host?.resource_attributes?.["os.type"]);
}

export const SHELL_LANG: Record<HostOs, ShellLang> = { linux: "sh", darwin: "sh", windows: "powershell" };

export const AGENT_CONFIG_PATH: Record<HostOs, string> = {
  linux: "/etc/openlog-infra-agent/config.yaml",
  darwin: "/etc/openlog-infra-agent/config.yaml",
  windows: "C:\\ProgramData\\openlog\\infra-agent\\config.yaml",
};

/** Directory of the configuration (password files for integrations live next to it). */
export const AGENT_CONFIG_DIR: Record<HostOs, string> = {
  linux: "/etc/openlog-infra-agent",
  darwin: "/etc/openlog-infra-agent",
  windows: "C:\\ProgramData\\openlog\\infra-agent",
};

const RESTART: Record<HostOs, string> = {
  linux: "sudo systemctl restart openlog-infra-agent",
  darwin: "sudo launchctl kickstart -k system/org.openlog.infra-agent",
  windows: "Restart-Service openlog-infra-agent",
};

/** Restart of the agent service (systemd, launchd, Windows service in an elevated PowerShell). */
export function agentRestart(os: HostOs): OsCommand {
  return { code: RESTART[os], lang: SHELL_LANG[os] };
}

const AGENT_LOG: Record<HostOs, string> = {
  linux: "journalctl -u openlog-infra-agent -e",
  darwin: "tail -f /var/log/openlog-infra-agent.log",
  windows: "Get-Content C:\\ProgramData\\openlog\\infra-agent\\logs\\openlog-infra-agent.log -Tail 50 -Wait",
};

/** Where the agent's own log is read. */
export function agentLog(os: HostOs): OsCommand {
  return { code: AGENT_LOG[os], lang: SHELL_LANG[os] };
}

const SELF_TEST: Record<HostOs, string> = {
  linux: "sudo openlog-infra-agent -self-test -config /etc/openlog-infra-agent/config.yaml",
  darwin: "sudo openlog-infra-agent -self-test -config /etc/openlog-infra-agent/config.yaml",
  windows: "& 'C:\\Program Files\\openlog\\infra-agent\\current\\openlog-infra-agent.exe' -self-test -config C:\\ProgramData\\openlog\\infra-agent\\config.yaml",
};

/** Configuration check of the installed agent. */
export function agentSelfTest(os: HostOs): OsCommand {
  return { code: SELF_TEST[os], lang: SHELL_LANG[os] };
}

/** Example log file glob per OS: a generic application log on Linux, Homebrew service logs on macOS, IIS on Windows. */
export const DEFAULT_LOG_PATH: Record<HostOs, string> = {
  linux: "/var/log/myapp/*.log",
  darwin: "/opt/homebrew/var/log/nginx/*.log",
  windows: "C:\\inetpub\\logs\\LogFiles\\W3SVC1\\*.log",
};

/** YAML scalar of a path: Windows paths single-quoted (backslashes stay literal), everything else double-quoted. */
export function yamlPath(value: string): string {
  if (value.includes("\\") && !value.includes("'") && !/[\p{Cc}]/u.test(value)) return `'${value}'`;
  return JSON.stringify(value);
}

/** The system log input of an OS: journald, the macOS unified log or the Windows Event Log. */
export const SYSTEM_LOG_INPUT: Record<HostOs, "journald" | "unified_log" | "windows_event_log"> = {
  linux: "journald",
  darwin: "unified_log",
  windows: "windows_event_log",
};

/** YAML lines (under `logs:`) that turn on the OS's system log input. */
export function systemLogYaml(os: HostOs): string[] {
  switch (os) {
    case "darwin":
      return ["  unified_log:", "    enabled: true", "    level: default", `    predicate: 'subsystem == "com.example.app" OR process == "nginx"'`];
    case "windows":
      return [
        "  windows_event_log:",
        "    enabled: true",
        "    channels:",
        "      - { name: System, levels: [critical, error, warning] }",
        "      - { name: Application, levels: [critical, error, warning] }",
      ];
    default:
      return ["  journald:", "    enabled: true"];
  }
}

/** A `logs` section of config.yaml with one file glob and, optionally, the OS's system log. */
export function hostLogsYaml(os: HostOs, logPath: string, systemLog: boolean): string {
  const lines = [`# ${AGENT_CONFIG_PATH[os]}`, "logs:", "  enabled: true", "  files:", `    - path: ${yamlPath(logPath.trim() || DEFAULT_LOG_PATH[os])}`];
  if (systemLog) lines.push(...systemLogYaml(os));
  return lines.join("\n");
}

/** Host detail tabs that exist for an OS: containers are collected on Linux and (Docker inventory) macOS, not on Windows. */
export function hostHasContainers(osType: string | null | undefined): boolean {
  return hostOs(osType) !== "windows";
}

/** PHP forwarder (and with it the PHP socket access notice): Linux and macOS; disabled on Windows. */
export function hostHasPhpForwarder(os: HostOs): boolean {
  return os !== "windows";
}

/** Manual grant of the PHP socket group: usermod + systemctl reload on Linux, dseditgroup + Homebrew PHP restart on macOS. */
export function phpAccessManualCommand(os: HostOs, group: string, users: string[], units: string[]): string {
  if (os === "darwin") {
    return [...users.map((u) => `sudo dseditgroup -o edit -a ${u} -t user ${group}`), ...(units.length ? ["brew services restart php"] : [])].join(" && \\\n  ");
  }
  return [...users.map((u) => `sudo usermod -aG ${group} ${u}`), ...(units.length ? [`sudo systemctl reload ${units.join(" ")}`] : [])].join(" && \\\n  ");
}

/** Fleet installation of the PHP agent: Linux only. */
export function hostHasPhpInstall(os: HostOs): boolean {
  return os === "linux";
}
