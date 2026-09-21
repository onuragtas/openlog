// Install commands of the "Add data" page (routes/add-data.tsx). Everything here is pure and unit tested
// (install-commands.test.ts): flags, variable names and URLs follow the agents' own documentation
// (scripts/install.sh, agents/*/README.md, deploy/helm/openlog-agent, docs/contracts/php-agent.md §7).
// The license key only ever appears in command text shown to the user; never in a URL.

import { AGENT_CONFIG_DIR, AGENT_CONFIG_PATH, agentRestart, DEFAULT_LOG_PATH, hostLogsYaml, HOST_OSES, SHELL_LANG, yamlPath, type HostOs } from "./host-os";

export const TARGET_IDS = [
  "linux",
  "macos",
  "windows",
  "docker",
  "kubernetes",
  "apm/go",
  "apm/node",
  "apm/python",
  "apm/java",
  "apm/dotnet",
  "apm/php",
  "logs/host",
  "logs/containers",
  "logs/browser",
  "logs/otel",
  "otel/sdk",
  "otel/collector",
  "integrations/nginx",
  "integrations/apache",
  "integrations/redis",
  "integrations/memcached",
  "integrations/mysql",
  "integrations/postgresql",
  "integrations/mongodb",
  "integrations/mssql",
  "integrations/iis",
  "integrations/haproxy",
  "integrations/rabbitmq",
  "integrations/elasticsearch",
  "integrations/prometheus",
] as const;
export type TargetId = (typeof TARGET_IDS)[number];

export const TARGET_GROUPS = ["infrastructure", "apm", "logs", "opentelemetry", "integrations"] as const;
export type TargetGroup = (typeof TARGET_GROUPS)[number];

/** What the verification step waits for. */
export type VerifyKind = "host" | "kubernetes" | "apm" | "otel" | "logs" | "integration" | "prometheus";

export type OptionKey =
  | "hostName"
  | "distro"
  | "channel"
  | "dockerAccess"
  | "clusterName"
  | "serviceName"
  | "environment"
  | "nodeModules"
  | "pythonLauncher"
  | "javaMode"
  | "dotnetApp"
  | "phpMode"
  | "phpPackage"
  | "arch"
  | "otelLanguage"
  | "protocol"
  | "hostOs"
  | "logPath"
  | "journald"
  | "browserOrigin";

export interface InstallTarget {
  id: TargetId;
  group: TargetGroup;
  verify: VerifyKind;
  /** Options shown in the options step, in order. */
  options: OptionKey[];
  /** Integration id for integration targets (lib/integrations.ts ids). */
  integration?: "nginx" | "apache" | "redis" | "memcached" | "mysql" | "postgresql" | "mongodb" | "mssql" | "iis" | "haproxy" | "rabbitmq" | "elasticsearch";
  /** Documentation in the repository. */
  docs: string;
  /** Cards of which one must be set up first (e.g. the infra agent for PHP and host logs); several = any of them. */
  requires?: readonly TargetId[];
}

/** The infra agent host cards: features that work on every host OS require one of them. */
const INFRA_HOSTS: readonly TargetId[] = ["linux", "macos", "windows"];

const REPO = "https://github.com/onuragtas/openlog";
const RELEASES = `${REPO}/releases`;
const blob = (path: string) => `${REPO}/blob/master/${path}`;

export const INSTALL_TARGETS: readonly InstallTarget[] = [
  { id: "linux", group: "infrastructure", verify: "host", options: ["distro", "channel", "dockerAccess", "hostName"], docs: blob("agents/infra/README.md") },
  { id: "macos", group: "infrastructure", verify: "host", options: ["channel", "hostName"], docs: blob("agents/infra/README.md") },
  { id: "windows", group: "infrastructure", verify: "host", options: ["channel", "hostName"], docs: blob("agents/infra/README.md") },
  { id: "docker", group: "infrastructure", verify: "host", options: ["hostName"], docs: blob("agents/infra/README.md") },
  { id: "kubernetes", group: "infrastructure", verify: "kubernetes", options: ["clusterName", "environment"], docs: blob("docs/operations/kubernetes.md") },
  { id: "apm/go", group: "apm", verify: "apm", options: ["serviceName", "environment"], docs: blob("agents/go/README.md") },
  { id: "apm/node", group: "apm", verify: "apm", options: ["serviceName", "environment", "nodeModules"], docs: blob("agents/node/README.md") },
  { id: "apm/python", group: "apm", verify: "apm", options: ["serviceName", "environment", "pythonLauncher"], docs: blob("agents/python/README.md") },
  { id: "apm/java", group: "apm", verify: "apm", options: ["serviceName", "environment", "javaMode"], docs: blob("agents/java/README.md") },
  { id: "apm/dotnet", group: "apm", verify: "apm", options: ["serviceName", "environment", "dotnetApp"], docs: blob("agents/dotnet/README.md") },
  { id: "apm/php", group: "apm", verify: "apm", options: ["serviceName", "environment", "phpMode", "phpPackage", "arch"], docs: blob("docs/contracts/php-agent.md"), requires: ["linux"] },
  { id: "logs/host", group: "logs", verify: "logs", options: ["hostOs", "logPath", "journald"], docs: blob("agents/infra/README.md#logs"), requires: INFRA_HOSTS },
  { id: "logs/containers", group: "logs", verify: "logs", options: [], docs: blob("agents/infra/README.md#logs"), requires: ["linux"] },
  { id: "logs/browser", group: "logs", verify: "logs", options: ["serviceName", "environment", "browserOrigin"], docs: blob("docs/contracts/config.md") },
  { id: "logs/otel", group: "logs", verify: "logs", options: ["serviceName", "environment", "otelLanguage", "protocol"], docs: blob("README.md") },
  { id: "otel/sdk", group: "opentelemetry", verify: "apm", options: ["serviceName", "environment", "otelLanguage", "protocol"], docs: blob("README.md") },
  { id: "otel/collector", group: "opentelemetry", verify: "otel", options: ["protocol"], docs: blob("README.md") },
  { id: "integrations/nginx", group: "integrations", verify: "integration", integration: "nginx", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/apache", group: "integrations", verify: "integration", integration: "apache", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/redis", group: "integrations", verify: "integration", integration: "redis", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/memcached", group: "integrations", verify: "integration", integration: "memcached", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/mysql", group: "integrations", verify: "integration", integration: "mysql", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/postgresql", group: "integrations", verify: "integration", integration: "postgresql", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/mongodb", group: "integrations", verify: "integration", integration: "mongodb", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  // SQL Server over TDS from any OS (also a remote server); IIS through Windows performance counters only.
  { id: "integrations/mssql", group: "integrations", verify: "integration", integration: "mssql", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/iis", group: "integrations", verify: "integration", integration: "iis", options: [], docs: blob("agents/infra/README.md#integrations"), requires: ["windows"] },
  { id: "integrations/haproxy", group: "integrations", verify: "integration", integration: "haproxy", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/rabbitmq", group: "integrations", verify: "integration", integration: "rabbitmq", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  { id: "integrations/elasticsearch", group: "integrations", verify: "integration", integration: "elasticsearch", options: ["hostOs"], docs: blob("agents/infra/README.md#integrations"), requires: INFRA_HOSTS },
  {
    id: "integrations/prometheus",
    group: "integrations",
    verify: "prometheus",
    options: ["hostOs"],
    docs: blob("agents/infra/README.md#prometheus-and-openmetrics-endpoints"),
    requires: [...INFRA_HOSTS, "docker", "kubernetes"],
  },
];

export function findTarget(id: string | undefined): InstallTarget | undefined {
  const clean = (id ?? "").replace(/^\/+|\/+$/g, "");
  return INSTALL_TARGETS.find((t) => t.id === clean);
}

/**
 * Product each APM card installs. Discovery hints carry identifiers such as `openlog-agent-php`
 * (semantic-conventions §3.4); these are the names users install.
 */
export const AGENT_PRODUCTS: Partial<Record<TargetId, string>> = {
  "apm/go": "github.com/onuragtas/openlog/agents/go",
  "apm/node": "openlog-node",
  "apm/python": "openlog-agent",
  "apm/java": "openlog-javaagent",
  "apm/dotnet": "OpenLog.Agent",
  "apm/php": "openlog-php-agent",
};

/** Maps a discovery `apm_hint.language` (agents/infra/rules/*.yaml) to its Add data card. */
export function apmTargetForLanguage(language: string | undefined | null): TargetId | undefined {
  switch ((language ?? "").toLowerCase()) {
    case "php":
      return "apm/php";
    case "node":
    case "nodejs":
      return "apm/node";
    case "python":
      return "apm/python";
    case "java":
    case "jvm":
      return "apm/java";
    case "dotnet":
      return "apm/dotnet";
    case "go":
      return "apm/go";
    default:
      return undefined;
  }
}

export interface InstallOptions {
  /** Value inserted for the license key; empty = placeholder. Held in memory only. */
  licenseKey: string;
  hostName: string;
  distro: "auto" | "deb" | "rpm" | "tarball";
  channel: "stable" | "beta";
  dockerAccess: boolean;
  clusterName: string;
  serviceName: string;
  environment: string;
  nodeModules: "commonjs" | "esm";
  pythonLauncher: "python" | "gunicorn" | "uvicorn" | "django" | "celery";
  javaMode: "jvm" | "docker";
  dotnetApp: "aspnet" | "console";
  phpMode: "fleet" | "package";
  phpPackage: "deb" | "rpm" | "apk";
  arch: "amd64" | "arm64";
  otelLanguage: "node" | "python" | "java" | "dotnet" | "go" | "other";
  protocol: "http" | "grpc";
  /** OS of the host the infra agent runs on (host logs and integration cards; prefilled from the host's os.type). */
  hostOs: HostOs;
  logPath: string;
  /** Also read the host's system log: journald (Linux), unified log (macOS) or Event Log (Windows). */
  journald: boolean;
  browserOrigin: string;
}

export const DEFAULT_OPTIONS: InstallOptions = {
  licenseKey: "",
  hostName: "",
  distro: "auto",
  channel: "stable",
  dockerAccess: true,
  clusterName: "",
  serviceName: "",
  environment: "",
  nodeModules: "commonjs",
  pythonLauncher: "python",
  javaMode: "jvm",
  dotnetApp: "aspnet",
  phpMode: "package",
  phpPackage: "deb",
  arch: "amd64",
  otelLanguage: "node",
  protocol: "http",
  hostOs: "linux",
  logPath: DEFAULT_LOG_PATH.linux,
  journald: false,
  browserOrigin: "",
};

/** The parts of GET /api/v1/onboarding the commands use. */
export interface OnboardingInfo {
  otlp_http: { url: string };
  otlp_grpc: { url: string };
  agent_version: string | null;
  cors_enabled: boolean;
  features?: { fleet_php_install?: boolean };
  /** Registry availability and GitHub release assets of the language agent packages (absent on older servers). */
  agent_packages?: { node: AgentPackageInfo; python: AgentPackageInfo; dotnet: AgentPackageInfo } | null;
}

/** GET /api/v1/onboarding agent_packages.<lang>. */
export interface AgentPackageInfo {
  name: string;
  version: string;
  /** available: the registry serves agent_version; missing: it does not; unknown: not checked or unreachable. */
  registry: "available" | "missing" | "unknown";
  registry_url: string;
  release_asset_url: string;
  release_asset_sha256_url: string;
}

export type BlockLang = "sh" | "powershell" | "yaml" | "go" | "js" | "csharp" | "dockerfile" | "sql" | "nginx" | "ini";

export type BlockLabel =
  | "install"
  | "msiInstall"
  | "download"
  | "packageInstall"
  | "enablePhp"
  | "phpSettings"
  | "fleetConfig"
  | "secret"
  | "helmInstall"
  | "code"
  | "environment"
  | "run"
  | "dockerfile"
  | "dockerRun"
  | "agentConfig"
  | "journalAccess"
  | "containerLabels"
  | "restart"
  | "serverCors"
  | "sender"
  | "otelInstall"
  | "collectorEnv"
  | "collectorConfig"
  | "collectorRun"
  | "stubStatus"
  | "modStatus"
  | "statsSection"
  | "managementPlugin"
  | "brokerUser"
  | "esUser"
  | "mongoUser"
  | "redisAcl"
  | "sqlUser"
  | "passwordFile"
  | "iisCheck"
  | "scrapeTargets"
  | "scrapeLabels"
  | "scrapeAnnotations"
  | "verify";

export interface CommandBlock {
  /** Unique within one result. */
  id: string;
  label: BlockLabel;
  lang: BlockLang;
  code: string;
  /** The block contains the real license key (masked in the UI until revealed). */
  containsKey: boolean;
}

export type NoteKey =
  | "placeholderKey"
  | "versionUnknown"
  | "packageRegistry"
  | "packageRelease"
  | "packageReleaseUnchecked"
  | "dotnetLocalSource"
  | "distroAuto"
  | "archAuto"
  | "macosService"
  | "windowsService"
  | "windowsMsi"
  | "windowsElevated"
  | "macosRoot"
  | "homebrewLogs"
  | "iisLogs"
  | "dockerGroup"
  | "dockerImageTag"
  | "helmChartSource"
  | "helmPodSecurity"
  | "goStart"
  | "nodeEsm"
  | "pythonModules"
  | "javaChecksum"
  | "javaDockerKey"
  | "javaFleet"
  | "dotnetRuntime"
  | "phpNeedsInfraAgent"
  | "phpFleetPage"
  | "phpFleetUnavailable"
  | "phpIniFile"
  | "mergeConfig"
  | "journaldGroup"
  | "containerLogsDefault"
  | "corsRequired"
  | "corsConfigured"
  | "browserKeyPublic"
  | "otelGoSdk"
  | "otelOtherSdk"
  | "otelLogsBridge"
  | "collectorPorts"
  | "grpcInsecure"
  | "integrationUi"
  | "integrationAuto"
  | "passwordPlaceholder"
  | "redisAclOptional"
  | "mssqlAuth"
  | "mssqlRemote"
  | "iisNoCredentials"
  | "iisDiscovery"
  | "apacheAuto"
  | "memcachedAuto"
  | "haproxySocket"
  | "rabbitmqGuest"
  | "esSecurity"
  | "mongoAuthSource"
  | "prometheusDiscovery"
  | "prometheusLimits";

export interface InstallCommands {
  blocks: CommandBlock[];
  notes: NoteKey[];
}

export const LICENSE_KEY_PLACEHOLDER = "<LICENSE_KEY>";
const DEFAULT_SERVICE = "my-service";
const DEFAULT_CLUSTER = "my-cluster";
const INFRA_IMAGE = "ghcr.io/onuragtas/openlog-infra-agent";
const INSTALL_SH = `${RELEASES}/latest/download/install.sh`;
/** Published next to install.sh in every release (scripts/install.ps1). */
const INSTALL_PS1 = `${RELEASES}/latest/download/install.ps1`;
const CONT = " \\\n  ";

const SHELL_SAFE = /^[A-Za-z0-9_@%+=:,./-]+$/;

/** POSIX shell quoting: safe words stay as they are, everything else is single-quoted. */
export function shQuote(value: string): string {
  if (value === "") return "''";
  return SHELL_SAFE.test(value) ? value : `'${value.replace(/'/g, `'\\''`)}'`;
}

/**
 * PowerShell verbatim string: always single-quoted, quotes doubled. PowerShell also treats the typographic single
 * quotes (U+2018..U+201B) as quote characters, so those are doubled as well.
 */
export function psQuote(value: string): string {
  return `'${value.replace(/['‘’‚‛]/g, "$&$&")}'`;
}

/** Value of an msiexec PROPERTY="value" argument typed in PowerShell: backtick-escapes what a double-quoted string expands. */
function msiValue(value: string): string {
  return `"${value.replace(/[`$"“”„]/g, "`$&")}"`;
}

/** YAML double-quoted scalar (a JSON string is one). */
export function yamlQuote(value: string): string {
  return JSON.stringify(value);
}

/** String literal for Go, JavaScript and C# (JSON escaping is valid in all three for text input). */
const codeString = (value: string) => JSON.stringify(value);

/** Helm --set value: commas and backslashes are Helm syntax. */
const helmValue = (value: string) => value.replace(/\\/g, "\\\\").replace(/,/g, "\\,");

/** Strips characters that no install target can carry safely in names (quotes, backslashes, control characters). */
export function cleanName(value: string): string {
  return value.replace(/[\p{Cc}"'`\\]/gu, "").trim();
}

/** Display form of text containing the key: the key replaced by a masked form. Copying always uses the real text. */
export function maskKey(text: string, key: string): string {
  if (!key || key === LICENSE_KEY_PLACEHOLDER) return text;
  const masked = key.length > 12 ? `${key.slice(0, 4)}${"•".repeat(12)}` : "•".repeat(12);
  return text.split(key).join(masked);
}

const stripSlash = (u: string) => u.replace(/\/+$/, "");

interface Ctx {
  o: InstallOptions;
  key: string;
  hasKey: boolean;
  http: string;
  grpc: string;
  version: string | null;
  info: OnboardingInfo;
  service: string;
  env: string;
  blocks: CommandBlock[];
  notes: NoteKey[];
}

function add(c: Ctx, label: BlockLabel, lang: BlockLang, code: string) {
  const n = c.blocks.filter((b) => b.label === label).length;
  c.blocks.push({ id: n === 0 ? label : `${label}-${n + 1}`, label, lang, code, containsKey: c.hasKey && code.includes(c.key) });
}

function note(c: Ctx, key: NoteKey) {
  if (!c.notes.includes(key)) c.notes.push(key);
}

/** `export NAME=value` lines of the openlog agents' shared configuration (agents/go/README.md "Configuration"). */
function openlogEnv(c: Ctx, extra: [string, string][] = []): string {
  const lines: [string, string][] = [
    ["OPENLOG_LICENSE_KEY", c.key],
    ["OPENLOG_ENDPOINT", c.http],
    ["OPENLOG_SERVICE_NAME", c.service],
  ];
  if (c.env) lines.push(["OPENLOG_ENVIRONMENT", c.env]);
  return [...lines, ...extra].map(([k, v]) => `export ${k}=${shQuote(v)}`).join("\n");
}

function releaseAsset(c: Ctx, file: (v: string) => string): string {
  const v = c.version ?? "X.Y.Z";
  return `${RELEASES}/download/v${v}/${file(v)}`;
}

// ---- infrastructure ----

function linux(c: Ctx) {
  const args = [`--license-key ${shQuote(c.key)}`, `--endpoint ${shQuote(c.http)}`];
  if (c.o.distro !== "auto") args.push(`--method ${c.o.distro}`);
  if (c.o.channel === "beta") args.push("--channel beta");
  if (!c.o.dockerAccess) args.push("--no-docker-access");
  add(c, "install", "sh", `curl -fsSL ${INSTALL_SH} | sudo sh -s --${CONT}${args.join(CONT)}`);
  add(c, "verify", "sh", "systemctl status openlog-infra-agent --no-pager\njournalctl -u openlog-infra-agent -f");
  note(c, c.o.distro === "auto" ? "distroAuto" : "archAuto");
  if (c.o.dockerAccess) note(c, "dockerGroup");
}

/** macOS: the same install.sh as Linux installs the launchd service org.openlog.infra-agent. */
function macos(c: Ctx) {
  const args = [`--license-key ${shQuote(c.key)}`, `--endpoint ${shQuote(c.http)}`];
  if (c.o.channel === "beta") args.push("--channel beta");
  add(c, "install", "sh", `curl -fsSL ${INSTALL_SH} | sudo sh -s --${CONT}${args.join(CONT)}`);
  add(c, "verify", "sh", "sudo launchctl print system/org.openlog.infra-agent\ntail -f /var/log/openlog-infra-agent.log");
  note(c, "archAuto");
  note(c, "macosService");
}

/** Windows (PowerShell as Administrator): install.ps1, or the amd64 MSI package. */
function windows(c: Ctx) {
  const args = [`-LicenseKey ${psQuote(c.key)}`, `-Endpoint ${psQuote(c.http)}`];
  if (c.o.channel === "beta") args.push("-Channel beta");
  add(c, "install", "powershell", `& ([scriptblock]::Create((Invoke-RestMethod ${psQuote(INSTALL_PS1)}))) ${args.join(" ")}`);
  const file = `openlog-infra-agent_${c.version ?? "X.Y.Z"}_windows_amd64.msi`;
  const url = releaseAsset(c, (v) => `openlog-infra-agent_${v}_windows_amd64.msi`);
  add(
    c,
    "msiInstall",
    "powershell",
    [
      "# Alternative to the script: the MSI package (amd64 only)",
      `Invoke-WebRequest -Uri ${psQuote(url)} -OutFile ${psQuote(file)}`,
      `msiexec /i ${file} LICENSE_KEY=${msiValue(c.key)} ENDPOINT=${msiValue(c.http)} /qn`,
    ].join("\n"),
  );
  add(c, "verify", "powershell", "Get-Service openlog-infra-agent");
  note(c, "windowsService");
  note(c, "windowsMsi");
  if (!c.version) note(c, "versionUnknown");
}

function docker(c: Ctx) {
  const tag = c.version ?? "latest";
  const lines = [
    "docker run -d --name openlog-infra-agent --restart unless-stopped",
    "--pid=host --network=host",
    "-v /:/host:ro -v openlog-agent-state:/var/lib/openlog-infra-agent",
    "--cap-add SYS_PTRACE --cap-add DAC_READ_SEARCH",
    `-e OPENLOG_LICENSE_KEY=${shQuote(c.key)}`,
    `-e OPENLOG_ENDPOINT=${shQuote(c.http)}`,
    `${INFRA_IMAGE}:${tag}`,
  ];
  add(c, "dockerRun", "sh", lines.join(CONT));
  add(c, "verify", "sh", "docker logs -f openlog-infra-agent");
  note(c, "dockerImageTag");
}

function kubernetes(c: Ctx) {
  const cluster = cleanName(c.o.clusterName) || DEFAULT_CLUSTER;
  add(
    c,
    "secret",
    "sh",
    [
      "kubectl create namespace openlog-agent",
      "kubectl label namespace openlog-agent pod-security.kubernetes.io/enforce=privileged",
      `kubectl -n openlog-agent create secret generic openlog-license --from-literal=license-key=${shQuote(c.key)}`,
    ].join("\n"),
  );
  const ref = c.version ? `v${c.version}` : "master";
  add(c, "download", "sh", `git clone --depth 1 --branch ${ref} ${REPO}.git openlog`);
  const sets = [
    `--set ${shQuote(`clusterName=${helmValue(cluster)}`)}`,
    `--set ${shQuote(`endpoint=${helmValue(c.http)}`)}`,
    "--set existingSecret.name=openlog-license",
  ];
  if (c.env) sets.push(`--set-string ${shQuote(`extraAttributes.env=${helmValue(c.env)}`)}`);
  add(c, "helmInstall", "sh", ["helm install openlog-agent ./openlog/deploy/helm/openlog-agent -n openlog-agent", ...sets].join(CONT));
  add(
    c,
    "verify",
    "sh",
    [
      "kubectl -n openlog-agent rollout status ds/openlog-agent-openlog-agent-node",
      "kubectl -n openlog-agent logs ds/openlog-agent-openlog-agent-node | grep 'kubernetes node mode'",
    ].join("\n"),
  );
  note(c, "helmPodSecurity");
  note(c, "helmChartSource");
}

// ---- APM ----

function apmGo(c: Ctx) {
  add(c, "install", "sh", "go get github.com/onuragtas/openlog/agents/go");
  add(
    c,
    "code",
    "go",
    [
      'import openlog "github.com/onuragtas/openlog/agents/go"',
      "",
      "func main() {",
      `\tshutdown, err := openlog.Start(context.Background(), openlog.WithServiceName(${codeString(c.service)}))`,
      "\tif err != nil {",
      "\t\tlog.Fatal(err)",
      "\t}",
      "\tdefer shutdown(context.Background()) // flushes buffered telemetry",
      "\t// ...",
      "}",
    ].join("\n"),
  );
  add(c, "run", "sh", `${openlogEnv(c)}\ngo run .`);
  note(c, "goStart");
}

/** Python packages carry the PEP 440 form of the product version (libs/release PythonVersion): 1.2.0-beta.3 → 1.2.0b3. */
export function pythonVersion(version: string): string {
  const m = /^(\d+\.\d+\.\d+)-(alpha|beta|rc)\.(\d+)$/.exec(version);
  if (!m) return version;
  const short = { alpha: "a", beta: "b", rc: "rc" }[m[2] as "alpha" | "beta" | "rc"];
  return `${m[1]}${short}${m[3]}`;
}

type PackageLang = "node" | "python" | "dotnet";

/** Package files attached to every GitHub release (docs/operations/releasing.md "Language agent packages"). */
const PACKAGE_FILES: Record<PackageLang, (v: string) => string> = {
  node: (v) => `openlog-node-${v}.tgz`,
  python: (v) => `openlog_agent-${pythonVersion(v)}-py3-none-any.whl`,
  dotnet: (v) => `OpenLog.Agent.${v}.nupkg`,
};

/**
 * Where a language agent installs from: the registry only when the server found this release there
 * (GET /api/v1/onboarding agent_packages.<lang>.registry = available); otherwise the file attached to the GitHub
 * release, which exists for every release even when registry publishing is not configured.
 */
function agentPackage(c: Ctx, lang: PackageLang): { registry: true } | { registry: false; version: string; file: string; url: string } {
  const info = c.info.agent_packages?.[lang];
  if (c.version && info?.registry === "available") {
    note(c, "packageRegistry");
    return { registry: true };
  }
  const version = c.version ?? "X.Y.Z";
  const url = c.version && info?.release_asset_url ? info.release_asset_url : `${RELEASES}/download/v${version}/${PACKAGE_FILES[lang](version)}`;
  const file = url.slice(url.lastIndexOf("/") + 1);
  if (!c.version) note(c, "versionUnknown");
  note(c, info?.registry === "missing" ? "packageRelease" : "packageReleaseUnchecked");
  return { registry: false, version, file, url };
}

function apmNode(c: Ctx) {
  const pkg = agentPackage(c, "node");
  add(c, "install", "sh", pkg.registry ? "npm install openlog-node" : `npm install ${pkg.url}`);
  const run = c.o.nodeModules === "esm" ? "node --import openlog-node/register server.mjs" : "node --require openlog-node/register server.js";
  add(c, "run", "sh", `${openlogEnv(c)}\n${run}`);
  if (c.o.nodeModules === "esm") note(c, "nodeEsm");
}

const PYTHON_RUN: Record<InstallOptions["pythonLauncher"], string> = {
  python: "openlog-instrument python app.py",
  gunicorn: "openlog-instrument gunicorn -w 4 -b 0.0.0.0:8000 myproject.wsgi:application",
  uvicorn: "openlog-instrument uvicorn main:app --host 0.0.0.0 --workers 4",
  django: "DJANGO_SETTINGS_MODULE=myproject.settings openlog-instrument python manage.py runserver --noreload",
  celery: "openlog-instrument celery -A myproject worker --concurrency 8",
};

function apmPython(c: Ctx) {
  const pkg = agentPackage(c, "python");
  add(c, "install", "sh", pkg.registry ? "pip install openlog-agent" : `pip install ${pkg.url}`);
  add(c, "run", "sh", `${openlogEnv(c)}\n${PYTHON_RUN[c.o.pythonLauncher]}`);
  if (c.o.pythonLauncher !== "python") note(c, "pythonModules");
}

function apmJava(c: Ctx) {
  const jar = releaseAsset(c, (v) => `openlog-javaagent-${v}.jar`);
  const file = `openlog-javaagent-${c.version ?? "X.Y.Z"}.jar`;
  if (c.o.javaMode === "docker") {
    add(
      c,
      "dockerfile",
      "dockerfile",
      [
        `ADD ${jar} /opt/openlog/openlog-javaagent.jar`,
        'ENV JAVA_TOOL_OPTIONS="-javaagent:/opt/openlog/openlog-javaagent.jar"',
        `ENV OPENLOG_SERVICE_NAME=${codeString(c.service)}`,
        ...(c.env ? [`ENV OPENLOG_ENVIRONMENT=${codeString(c.env)}`] : []),
      ].join("\n"),
    );
    add(c, "dockerRun", "sh", ["docker run -d", `-e OPENLOG_LICENSE_KEY=${shQuote(c.key)}`, `-e OPENLOG_ENDPOINT=${shQuote(c.http)}`, "my-java-app"].join(CONT));
    note(c, "javaChecksum");
    note(c, "javaDockerKey");
  } else {
    add(
      c,
      "download",
      "sh",
      [
        `curl -fsSLO ${jar}`,
        `curl -fsSLO ${jar}.sha256`,
        `sha256sum -c ${file}.sha256`,
        `sudo install -D -m 0644 ${file} /opt/openlog/openlog-javaagent.jar`,
      ].join("\n"),
    );
    add(c, "run", "sh", `${openlogEnv(c)}\njava -javaagent:/opt/openlog/openlog-javaagent.jar -jar app.jar`);
    note(c, "javaFleet");
  }
  if (!c.version) note(c, "versionUnknown");
}

function apmDotnet(c: Ctx) {
  const pkg = agentPackage(c, "dotnet");
  if (pkg.registry) {
    add(c, "install", "sh", "dotnet add package OpenLog.Agent");
  } else {
    // A local folder source: `dotnet add package --source` would restrict the restore to it and lose the
    // OpenTelemetry dependencies from nuget.org, so the folder is added to the NuGet configuration instead.
    const dir = '"$HOME/.openlog/nuget"';
    add(
      c,
      "download",
      "sh",
      [`mkdir -p ${dir}`, `(cd ${dir} && curl -fsSLO ${pkg.url} && curl -fsSLO ${pkg.url}.sha256 && sha256sum -c ${pkg.file}.sha256)`].join("\n"),
    );
    add(c, "install", "sh", [`dotnet nuget add source ${dir} -n openlog-local`, `dotnet add package OpenLog.Agent --version ${pkg.version}`].join("\n"));
    note(c, "dotnetLocalSource");
  }
  const code =
    c.o.dotnetApp === "console"
      ? ["using OpenLog.Agent;", "", `using var agent = OpenLogAgent.Start(o => o.ServiceName = ${codeString(c.service)}); // flushes on Dispose`].join("\n")
      : ["var builder = WebApplication.CreateBuilder(args);", "builder.Services.AddOpenLog(); // OPENLOG_* variables configure it", "var app = builder.Build();"].join("\n");
  add(c, "code", "csharp", code);
  add(c, "run", "sh", `${openlogEnv(c)}\ndotnet run`);
  note(c, "dotnetRuntime");
}

function apmPhp(c: Ctx) {
  note(c, "phpNeedsInfraAgent");
  if (c.o.phpMode === "fleet") {
    if (c.info.features?.fleet_php_install === false) note(c, "phpFleetUnavailable");
    else note(c, "phpFleetPage");
    add(c, "fleetConfig", "yaml", ["# /etc/openlog-infra-agent/config.yaml (php_agent section)", "php_agent:", "  mode: auto", "  reload: graceful"].join("\n"));
    add(c, "restart", "sh", "sudo systemctl restart openlog-infra-agent");
    note(c, "mergeConfig");
  } else {
    const pkg = c.o.phpPackage;
    const url = releaseAsset(c, (v) => `openlog-php-agent_${v}_linux_${c.o.arch}.${pkg}`);
    const file = `openlog-php-agent_${c.version ?? "X.Y.Z"}_linux_${c.o.arch}.${pkg}`;
    const installer = pkg === "deb" ? `sudo apt-get install ./${file}` : pkg === "rpm" ? `sudo dnf install ./${file}` : `sudo apk add --allow-untrusted ./${file}`;
    add(c, "packageInstall", "sh", `curl -fsSLO ${url}\n${installer}`);
    add(c, "enablePhp", "sh", "sudo openlog-php-install status\nsudo openlog-php-install install --reload");
    if (!c.version) note(c, "versionUnknown");
  }
  add(
    c,
    "phpSettings",
    "ini",
    ["; e.g. /etc/php/8.3/fpm/conf.d/91-openlog-service.ini", `openlog.service_name = ${codeString(c.service)}`, ...(c.env ? [`openlog.environment = ${codeString(c.env)}`] : [])].join("\n"),
  );
  note(c, "phpIniFile");
}

// ---- logs ----

/** The host OS of a card; values other than the three agent OSes fall back to Linux. */
function osOf(c: Ctx): HostOs {
  return HOST_OSES.includes(c.o.hostOs) ? c.o.hostOs : "linux";
}

/** Restart of the infra agent on the card's host OS, with the notes the OS needs. */
function restartAgent(c: Ctx, os: HostOs) {
  const r = agentRestart(os);
  add(c, "restart", r.lang, r.code);
  if (os === "windows") note(c, "windowsElevated");
}

function logsHost(c: Ctx) {
  const os = osOf(c);
  // The log path option starts with the Linux example; another OS gets its own example unless the user typed a path.
  const typed = c.o.logPath.trim();
  const path = !typed || (os !== "linux" && typed === DEFAULT_LOG_PATH.linux) ? DEFAULT_LOG_PATH[os] : typed;
  add(c, "agentConfig", "yaml", hostLogsYaml(os, path, c.o.journald));
  if (c.o.journald && os === "linux") {
    add(c, "journalAccess", "sh", "sudo usermod -aG systemd-journal openlog-agent");
    note(c, "journaldGroup");
  }
  restartAgent(c, os);
  note(c, "mergeConfig");
  if (os === "darwin") {
    note(c, "macosRoot");
    if (path === DEFAULT_LOG_PATH.darwin) note(c, "homebrewLogs");
  }
  if (os === "windows" && path === DEFAULT_LOG_PATH.windows) note(c, "iisLogs");
}

function logsContainers(c: Ctx) {
  add(c, "agentConfig", "yaml", ["# /etc/openlog-infra-agent/config.yaml", "containers:", "  enabled: true", "logs:", "  containers:", "    enabled: true"].join("\n"));
  add(
    c,
    "containerLabels",
    "sh",
    ["# no logs from one container", "docker run --label openlog.logs=false …", "# group stack traces: a record starts at a matching line", "docker run --label 'openlog.logs.multiline=^\\d{4}-\\d{2}-\\d{2}' …"].join("\n"),
  );
  add(c, "restart", "sh", "sudo systemctl restart openlog-infra-agent");
  note(c, "containerLogsDefault");
  note(c, "mergeConfig");
}

function logsBrowser(c: Ctx) {
  const origin = c.o.browserOrigin.trim() || "https://app.example.com";
  if (!c.info.cors_enabled) {
    add(c, "serverCors", "sh", [`# openlog server environment (e.g. /opt/openlog-server/.env), then restart openlog-ingest`, `OPENLOG_INGEST_CORS_ALLOWED_ORIGINS=${shQuote(origin)}`].join("\n"));
    note(c, "corsRequired");
  } else {
    note(c, "corsConfigured");
  }
  const resource = [`{ key: "service.name", value: { stringValue: ${codeString(c.service)} } }`];
  if (c.env) resource.push(`{ key: "deployment.environment.name", value: { stringValue: ${codeString(c.env)} } }`);
  add(
    c,
    "sender",
    "js",
    [
      `const OPENLOG_LOGS_URL = ${codeString(`${c.http}/v1/logs`)};`,
      `const OPENLOG_LICENSE_KEY = ${codeString(c.key)}; // use a dedicated key: everything in a browser is public`,
      "",
      "export function sendLog(severityText, message, attributes = {}) {",
      "  return fetch(OPENLOG_LOGS_URL, {",
      '    method: "POST",',
      "    keepalive: true,",
      '    headers: { "Content-Type": "application/json", "openlog-license-key": OPENLOG_LICENSE_KEY },',
      "    body: JSON.stringify({",
      "      resourceLogs: [{",
      `        resource: { attributes: [${resource.join(", ")}] },`,
      "        scopeLogs: [{ logRecords: [{",
      "          timeUnixNano: `${Date.now()}000000`,",
      "          severityText,",
      "          body: { stringValue: String(message) },",
      "          attributes: Object.entries(attributes).map(([key, v]) => ({ key, value: { stringValue: String(v) } })),",
      "        }] }],",
      "      }],",
      "    }),",
      "  });",
      "}",
      "",
      'sendLog("INFO", "page loaded", { "url.path": location.pathname });',
    ].join("\n"),
  );
  note(c, "browserKeyPublic");
}

// ---- OpenTelemetry ----

function otelEnv(c: Ctx, logs: boolean): string {
  const grpc = c.o.protocol === "grpc";
  const header = c.hasKey ? encodeURIComponent(c.key) : c.key;
  const lines: [string, string][] = [
    ["OTEL_EXPORTER_OTLP_ENDPOINT", grpc ? c.grpc : c.http],
    ["OTEL_EXPORTER_OTLP_PROTOCOL", grpc ? "grpc" : "http/protobuf"],
    ["OTEL_EXPORTER_OTLP_HEADERS", `openlog-license-key=${header}`],
    ["OTEL_SERVICE_NAME", c.service],
  ];
  if (c.env) lines.push(["OTEL_RESOURCE_ATTRIBUTES", `deployment.environment.name=${encodeURIComponent(c.env)}`]);
  if (logs) {
    lines.push(["OTEL_LOGS_EXPORTER", "otlp"]);
    if (c.o.otelLanguage === "python") lines.push(["OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED", "true"]);
  }
  return lines.map(([k, v]) => `export ${k}=${shQuote(v)}`).join("\n");
}

function otelSdk(c: Ctx, logs: boolean) {
  const env = otelEnv(c, logs);
  switch (c.o.otelLanguage) {
    case "node":
      add(c, "otelInstall", "sh", "npm install --save @opentelemetry/api @opentelemetry/auto-instrumentations-node");
      add(c, "run", "sh", `${env}\nnode --require @opentelemetry/auto-instrumentations-node/register app.js`);
      break;
    case "python":
      add(c, "otelInstall", "sh", "pip install opentelemetry-distro opentelemetry-exporter-otlp\nopentelemetry-bootstrap -a install");
      add(c, "run", "sh", `${env}\nopentelemetry-instrument python app.py`);
      break;
    case "java":
      add(c, "otelInstall", "sh", "curl -fsSLO https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/latest/download/opentelemetry-javaagent.jar");
      add(c, "run", "sh", `${env}\njava -javaagent:./opentelemetry-javaagent.jar -jar app.jar`);
      break;
    case "dotnet":
      add(
        c,
        "otelInstall",
        "sh",
        "curl -fsSLO https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/latest/download/otel-dotnet-auto-install.sh\nsh ./otel-dotnet-auto-install.sh",
      );
      add(c, "run", "sh", `${env}\n. $HOME/.otel-dotnet-auto/instrument.sh\ndotnet MyApp.dll`);
      break;
    case "go":
      add(c, "environment", "sh", env);
      note(c, "otelGoSdk");
      break;
    default:
      add(c, "environment", "sh", env);
      note(c, "otelOtherSdk");
  }
  if (logs) note(c, "otelLogsBridge");
}

function collector(c: Ctx) {
  add(c, "collectorEnv", "sh", `export OPENLOG_LICENSE_KEY=${shQuote(c.key)}`);
  const grpc = c.o.protocol === "grpc";
  let exporterName: string;
  let exporter: string[];
  if (grpc) {
    let hostPort = c.grpc;
    let insecure = false;
    try {
      const u = new URL(c.grpc);
      insecure = u.protocol === "http:";
      // URL drops the scheme's default port (https://host:443 → host), but the gRPC exporter needs host:port.
      hostPort = u.hostname ? `${u.host}${u.port ? "" : insecure ? ":80" : ":443"}` : c.grpc;
    } catch {
      // keep the configured value
    }
    exporterName = "otlp/openlog";
    exporter = ["  otlp/openlog:", `    endpoint: ${yamlQuote(hostPort)}`, ...(insecure ? ["    tls:", "      insecure: true"] : [])];
    if (insecure) note(c, "grpcInsecure");
  } else {
    exporterName = "otlphttp/openlog";
    exporter = ["  otlphttp/openlog:", `    endpoint: ${yamlQuote(c.http)}`, "    compression: gzip"];
  }
  const pipeline = (signal: string) => [`    ${signal}:`, "      receivers: [otlp]", "      processors: [batch]", `      exporters: [${exporterName}]`];
  add(
    c,
    "collectorConfig",
    "yaml",
    [
      "# openlog-collector.yaml",
      "receivers:",
      "  otlp:",
      "    protocols:",
      "      grpc:",
      "        endpoint: 0.0.0.0:4317",
      "      http:",
      "        endpoint: 0.0.0.0:4318",
      "",
      "processors:",
      "  batch: {}",
      "",
      "exporters:",
      ...exporter,
      "    headers:",
      "      openlog-license-key: ${env:OPENLOG_LICENSE_KEY}",
      "",
      "service:",
      "  pipelines:",
      ...pipeline("traces"),
      ...pipeline("metrics"),
      ...pipeline("logs"),
    ].join("\n"),
  );
  add(c, "collectorRun", "sh", "otelcol-contrib --config openlog-collector.yaml");
  note(c, "collectorPorts");
}

// ---- integrations ----

/** A password file only the agent can read: the openlog-agent user on Linux, root on macOS, SYSTEM and Administrators on Windows. */
function passwordFile(os: HostOs, name: string): string {
  const file = os === "windows" ? `${AGENT_CONFIG_DIR.windows}\\${name}.password` : `${AGENT_CONFIG_DIR[os]}/${name}.password`;
  switch (os) {
    case "darwin":
      return [`sudo install -m 0600 -o root -g wheel /dev/null ${file}`, `sudo -e ${file}`].join("\n");
    case "windows":
      return [
        `$f = ${psQuote(file)}`,
        "Set-Content -Path $f -Value '<password>' -NoNewline",
        "# SYSTEM and Administrators only (well-known SIDs work on every Windows language)",
        "icacls $f /inheritance:r /grant:r '*S-1-5-18:(F)' '*S-1-5-32-544:(F)'",
      ].join("\n");
    default:
      return [`sudo install -m 0600 -o openlog-agent -g openlog-agent /dev/null ${file}`, `sudoedit ${file}`].join("\n");
  }
}

/** `integrations.<name>` with a username and a password file. */
function integrationConfig(os: HostOs, name: string): string {
  const file = os === "windows" ? `${AGENT_CONFIG_DIR.windows}\\${name}.password` : `${AGENT_CONFIG_DIR[os]}/${name}.password`;
  return [`# ${AGENT_CONFIG_PATH[os]}`, "integrations:", `  ${name}:`, "    username: openlog", `    password: ${os === "windows" ? yamlPath(`file:${file}`) : `file:${file}`}`].join("\n");
}

const APACHE_RELOAD: Record<HostOs, string> = {
  linux: "sudo apachectl configtest && sudo systemctl reload apache2 || sudo systemctl reload httpd",
  darwin: "sudo apachectl configtest && sudo apachectl graceful",
  windows: "httpd.exe -t; if ($LASTEXITCODE -eq 0) { Restart-Service -Name Apache2.4 }",
};

const HAPROXY_RELOAD: Record<HostOs, string> = {
  linux: "sudo haproxy -c -f /etc/haproxy/haproxy.cfg && sudo systemctl reload haproxy",
  darwin: "haproxy -c -f /usr/local/etc/haproxy/haproxy.cfg && brew services restart haproxy",
  windows: "haproxy.exe -c -f haproxy.cfg",
};

const NGINX_RELOAD: Record<HostOs, string> = {
  linux: "sudo nginx -t && sudo systemctl reload nginx",
  darwin: "nginx -t && nginx -s reload",
  windows: "nginx -t; if ($LASTEXITCODE -eq 0) { nginx -s reload }",
};

/** `integrations.<name>` with a username, a password file and an optional endpoint line (a remote SQL Server). */
function integrationConfigWithEndpoint(os: HostOs, name: string, username: string, endpointComment: string): string {
  const file = os === "windows" ? `${AGENT_CONFIG_DIR.windows}\\${name}.password` : `${AGENT_CONFIG_DIR[os]}/${name}.password`;
  return [
    `# ${AGENT_CONFIG_PATH[os]}`,
    "integrations:",
    `  ${name}:`,
    `    username: ${username}`,
    `    password: ${os === "windows" ? yamlPath(`file:${file}`) : `file:${file}`}`,
    `    # ${endpointComment}`,
  ].join("\n");
}

/** Checks on Windows that W3SVC runs and the Web Service counters are readable (what the iis integration reads through WMI). */
const IIS_CHECK = [
  "Get-Service W3SVC",
  "Get-CimInstance Win32_PerfRawData_W3SVC_WebService | Where-Object Name -ne '_Total' | Select-Object Name, CurrentConnections",
  "# No rows: re-register the performance counters (lodctr /R) and restart W3SVC",
].join("\n");

function integration(c: Ctx, id: NonNullable<InstallTarget["integration"]>) {
  if (id === "iis") {
    // Windows only, no credentials: the host OS option does not apply.
    add(c, "iisCheck", "powershell", IIS_CHECK);
    note(c, "iisNoCredentials");
    note(c, "iisDiscovery");
    return;
  }
  const os = osOf(c);
  const shell = SHELL_LANG[os];
  note(c, "integrationUi");
  switch (id) {
    case "nginx":
      add(
        c,
        "stubStatus",
        "nginx",
        [
          "# nginx: stub_status (the agent finds it on any discovered port; it never edits nginx configuration)",
          "location = /nginx_status {",
          "    stub_status;",
          "    allow 127.0.0.1;",
          "    allow 172.16.0.0/12;   # Docker networks: published ports arrive from the bridge gateway",
          "    deny all;",
          "}",
        ].join("\n"),
      );
      add(c, "restart", shell, NGINX_RELOAD[os]);
      note(c, "integrationAuto");
      break;
    case "apache":
      add(
        c,
        "modStatus",
        "nginx",
        [
          "# Apache: mod_status (the agent probes /server-status?auto on the discovered ports and never edits the configuration)",
          "# Debian/Ubuntu: a2enmod status; RHEL: the module is built in",
          "ExtendedStatus On",
          "<Location /server-status>",
          "    SetHandler server-status",
          "    Require local",
          "</Location>",
        ].join("\n"),
      );
      add(c, "restart", shell, APACHE_RELOAD[os]);
      note(c, "apacheAuto");
      break;
    case "memcached":
      add(c, "agentConfig", "yaml", [`# ${AGENT_CONFIG_PATH[os]}`, "integrations:", "  memcached:", "    # endpoint: 127.0.0.1:11211   # only when the server is not on this host", "    enabled: true"].join("\n"));
      note(c, "memcachedAuto");
      break;
    case "haproxy":
      add(
        c,
        "statsSection",
        "ini",
        [
          "# /etc/haproxy/haproxy.cfg — a stats page on localhost, or the runtime socket below",
          "frontend stats",
          "    bind 127.0.0.1:8404",
          "    stats enable",
          "    stats uri /",
          "",
          "# Or the runtime API, readable by the agent's user:",
          "global",
          "    stats socket /run/haproxy/admin.sock mode 660 group openlog-agent level operator",
        ].join("\n"),
      );
      add(c, "restart", shell, HAPROXY_RELOAD[os]);
      note(c, "haproxySocket");
      break;
    case "rabbitmq":
      add(c, "managementPlugin", shell, "sudo rabbitmq-plugins enable rabbitmq_management");
      add(
        c,
        "brokerUser",
        shell,
        [
          "sudo rabbitmqctl add_user openlog '<password>'",
          "sudo rabbitmqctl set_user_tags openlog monitoring",
          'sudo rabbitmqctl set_permissions -p / openlog "" "" ".*"   # read only',
        ].join("\n"),
      );
      add(c, "passwordFile", shell, passwordFile(os, "rabbitmq"));
      add(c, "agentConfig", "yaml", integrationConfigWithEndpoint(os, "rabbitmq", "openlog", "endpoint: http://127.0.0.1:15672   # only when the management API is elsewhere"));
      restartAgent(c, os);
      note(c, "passwordPlaceholder");
      note(c, "rabbitmqGuest");
      break;
    case "mongodb":
      add(
        c,
        "mongoUser",
        "sh",
        [
          "# In mongosh, as a user that may create users:",
          "use admin",
          `db.createUser({user: "openlog", pwd: "<password>", roles: [`,
          `  {role: "clusterMonitor", db: "admin"}, {role: "read", db: "local"}]})`,
        ].join("\n"),
      );
      add(c, "passwordFile", shell, passwordFile(os, "mongodb"));
      add(c, "agentConfig", "yaml", integrationConfigWithEndpoint(os, "mongodb", "openlog", "database: admin   # the authentication source, not a database to monitor"));
      restartAgent(c, os);
      note(c, "passwordPlaceholder");
      note(c, "mongoAuthSource");
      break;
    case "elasticsearch":
      add(
        c,
        "esUser",
        "sh",
        [
          "# Elasticsearch 8 with security on: a read-only monitoring user (OpenSearch: create it the same way)",
          "curl -u elastic:<elastic-password> -X POST https://127.0.0.1:9200/_security/user/openlog \\",
          "  -H 'Content-Type: application/json' \\",
          '  -d \'{"password":"<password>","roles":["monitoring_user"]}\'',
        ].join("\n"),
      );
      add(c, "passwordFile", shell, passwordFile(os, "elasticsearch"));
      add(c, "agentConfig", "yaml", integrationConfigWithEndpoint(os, "elasticsearch", "openlog", "endpoint: https://127.0.0.1:9200   # with TLS: tls: { enabled: true, ca_file: /etc/elasticsearch/certs/http_ca.crt }"));
      restartAgent(c, os);
      note(c, "passwordPlaceholder");
      note(c, "esSecurity");
      break;
    case "redis":
      add(c, "redisAcl", shell, "redis-cli ACL SETUSER openlog on '><password>' +info +ping");
      add(c, "passwordFile", shell, passwordFile(os, "redis"));
      add(c, "agentConfig", "yaml", integrationConfig(os, "redis"));
      restartAgent(c, os);
      note(c, "redisAclOptional");
      note(c, "passwordPlaceholder");
      break;
    case "mysql":
      add(
        c,
        "sqlUser",
        "sql",
        [
          "-- MySQL 8 / MariaDB 10.5+ (MariaDB >= 10.5.9: REPLICA MONITOR instead of REPLICATION CLIENT)",
          "-- For a server in a Docker container use 'openlog'@'%' (or the bridge subnet).",
          "CREATE USER 'openlog'@'localhost' IDENTIFIED BY '<password>' WITH MAX_USER_CONNECTIONS 3;",
          "GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'openlog'@'localhost';",
          "GRANT SELECT ON performance_schema.* TO 'openlog'@'localhost';",
        ].join("\n"),
      );
      add(c, "passwordFile", shell, passwordFile(os, "mysql"));
      add(c, "agentConfig", "yaml", integrationConfig(os, "mysql"));
      restartAgent(c, os);
      note(c, "passwordPlaceholder");
      break;
    case "postgresql":
      add(
        c,
        "sqlUser",
        "sql",
        ["-- PostgreSQL 10+", "CREATE ROLE openlog WITH LOGIN PASSWORD '<password>' CONNECTION LIMIT 3;", "GRANT pg_monitor TO openlog;"].join("\n"),
      );
      add(c, "passwordFile", shell, passwordFile(os, "postgresql"));
      add(c, "agentConfig", "yaml", integrationConfig(os, "postgresql"));
      restartAgent(c, os);
      note(c, "passwordPlaceholder");
      break;
    case "mssql":
      add(
        c,
        "sqlUser",
        "sql",
        [
          "-- SQL Server 2016+ with mixed-mode (SQL Server) authentication; run in master.",
          "-- SQL Server 2022: VIEW SERVER PERFORMANCE STATE is enough instead of VIEW SERVER STATE.",
          "CREATE LOGIN openlog_monitor WITH PASSWORD = '<password>', CHECK_POLICY = ON;",
          "GRANT VIEW SERVER STATE TO openlog_monitor;",
          "GRANT VIEW ANY DEFINITION TO openlog_monitor;   -- database sizes (sys.master_files)",
        ].join("\n"),
      );
      add(c, "passwordFile", shell, passwordFile(os, "mssql"));
      add(c, "agentConfig", "yaml", integrationConfigWithEndpoint(os, "mssql", "openlog_monitor", "endpoint: sql.example.internal:1433   # only for a remote server; local instances are discovered"));
      restartAgent(c, os);
      note(c, "passwordPlaceholder");
      note(c, "mssqlAuth");
      note(c, "mssqlRemote");
      break;
  }
  note(c, "mergeConfig");
  if (os === "darwin") note(c, "macosRoot");
}

/** Prometheus/OpenMetrics scraping (semantic-conventions §6.9): static targets, container labels, pod annotations. */
function prometheus(c: Ctx) {
  const os = osOf(c);
  add(
    c,
    "scrapeTargets",
    "yaml",
    [
      `# ${AGENT_CONFIG_PATH[os]}`,
      "prometheus:",
      "  targets:",
      "    - url: http://127.0.0.1:9100/metrics   # e.g. node_exporter",
      "      job: node                           # becomes service.name",
      "    # - url: https://app.internal:8443/metrics",
      "    #   job: app",
      `    #   bearer_token: ${os === "windows" ? yamlPath(`file:${AGENT_CONFIG_DIR.windows}\\app.token`) : `file:${AGENT_CONFIG_DIR[os]}/app.token`}`,
    ].join("\n"),
  );
  restartAgent(c, os);
  add(
    c,
    "scrapeLabels",
    "yaml",
    ["# docker-compose.yml: no agent change needed", "services:", "  api:", "    labels:", '      prometheus.io/scrape: "true"', '      prometheus.io/port: "9090"', "      prometheus.io/path: /metrics"].join("\n"),
  );
  add(
    c,
    "scrapeAnnotations",
    "yaml",
    ["# Kubernetes pod template (node agent of the openlog-agent chart)", "metadata:", "  annotations:", '    prometheus.io/scrape: "true"', '    prometheus.io/port: "8080"'].join("\n"),
  );
  note(c, "prometheusDiscovery");
  note(c, "prometheusLimits");
  note(c, "mergeConfig");
}

/**
 * Commands for one Add data card. Pure: the same inputs give the same text. Values are shell-quoted, YAML-quoted or
 * code-string-escaped where they are inserted; the license key never appears in a URL.
 */
export function buildInstallCommands(target: TargetId, options: InstallOptions, onboarding: OnboardingInfo): InstallCommands {
  const key = options.licenseKey.trim() || LICENSE_KEY_PLACEHOLDER;
  const c: Ctx = {
    o: options,
    key,
    hasKey: key !== LICENSE_KEY_PLACEHOLDER,
    http: stripSlash(onboarding.otlp_http.url),
    grpc: stripSlash(onboarding.otlp_grpc.url),
    version: onboarding.agent_version,
    info: onboarding,
    service: cleanName(options.serviceName) || DEFAULT_SERVICE,
    env: cleanName(options.environment),
    blocks: [],
    notes: [],
  };
  const def = findTarget(target);
  if (!def) return { blocks: [], notes: [] };
  switch (target) {
    case "linux":
      linux(c);
      break;
    case "macos":
      macos(c);
      break;
    case "windows":
      windows(c);
      break;
    case "docker":
      docker(c);
      break;
    case "kubernetes":
      kubernetes(c);
      break;
    case "apm/go":
      apmGo(c);
      break;
    case "apm/node":
      apmNode(c);
      break;
    case "apm/python":
      apmPython(c);
      break;
    case "apm/java":
      apmJava(c);
      break;
    case "apm/dotnet":
      apmDotnet(c);
      break;
    case "apm/php":
      apmPhp(c);
      break;
    case "logs/host":
      logsHost(c);
      break;
    case "logs/containers":
      logsContainers(c);
      break;
    case "logs/browser":
      logsBrowser(c);
      break;
    case "logs/otel":
      otelSdk(c, true);
      break;
    case "otel/sdk":
      otelSdk(c, false);
      break;
    case "otel/collector":
      collector(c);
      break;
    case "integrations/prometheus":
      prometheus(c);
      break;
    default:
      if (def.integration) integration(c, def.integration);
  }
  const usesKey = def.verify !== "integration" && def.verify !== "prometheus" && target !== "logs/host" && target !== "logs/containers" && !(target === "apm/php");
  if (!c.hasKey && usesKey) c.notes.unshift("placeholderKey");
  return { blocks: c.blocks, notes: c.notes };
}

/** Whether a card's commands need a license key at all (host log and integration cards reuse the infra agent's). */
export function targetNeedsKey(target: TargetId): boolean {
  const def = findTarget(target);
  if (!def) return false;
  return def.verify !== "integration" && def.verify !== "prometheus" && target !== "logs/host" && target !== "logs/containers" && target !== "apm/php";
}

/** The service name the verification step looks for (PHP defaults to php-app when the ini setting is not used). */
export function expectedServiceName(options: InstallOptions): string {
  return cleanName(options.serviceName) || DEFAULT_SERVICE;
}
