import { describe, expect, it } from "vitest";
import {
  apmTargetForLanguage,
  buildInstallCommands,
  DEFAULT_OPTIONS,
  findTarget,
  INSTALL_TARGETS,
  LICENSE_KEY_PLACEHOLDER,
  maskKey,
  shQuote,
  targetNeedsKey,
  yamlQuote,
  pythonVersion,
  type AgentPackageInfo,
  type InstallOptions,
  type OnboardingInfo,
  type TargetId,
} from "./install-commands";

const KEY = "olk_0123456789abcdef0123";
const INFO: OnboardingInfo = {
  otlp_http: { url: "https://ingest.example.com:4318/" },
  otlp_grpc: { url: "https://ingest.example.com:4317" },
  agent_version: "0.9.1",
  cors_enabled: false,
  features: { fleet_php_install: true },
};

const REL = "https://github.com/onuragtas/openlog/releases/download";
const pkgInfo = (registry: AgentPackageInfo["registry"], name: string, version: string, file: string): AgentPackageInfo => ({
  name,
  version,
  registry,
  registry_url: `https://registry.example/${name}`,
  release_asset_url: `${REL}/v0.9.1/${file}`,
  release_asset_sha256_url: `${REL}/v0.9.1/${file}.sha256`,
});
const packages = (registry: AgentPackageInfo["registry"]): Partial<OnboardingInfo> => ({
  agent_packages: {
    node: pkgInfo(registry, "@openlog/node", "0.9.1", "openlog-node-0.9.1.tgz"),
    python: pkgInfo(registry, "openlog-agent", "0.9.1", "openlog_agent-0.9.1-py3-none-any.whl"),
    dotnet: pkgInfo(registry, "OpenLog.Agent", "0.9.1", "OpenLog.Agent.0.9.1.nupkg"),
  },
});
/** The server found agent_version on npm, PyPI and nuget.org. */
const onRegistry = packages("available");

const opts = (patch: Partial<InstallOptions> = {}): InstallOptions => ({ ...DEFAULT_OPTIONS, licenseKey: KEY, ...patch });
const build = (target: TargetId, patch: Partial<InstallOptions> = {}, info: Partial<OnboardingInfo> = {}) =>
  buildInstallCommands(target, opts(patch), { ...INFO, ...info });
const block = (target: TargetId, label: string, patch: Partial<InstallOptions> = {}, info: Partial<OnboardingInfo> = {}) => {
  const b = build(target, patch, info).blocks.find((x) => x.id === label);
  if (!b) throw new Error(`${target}: no block ${label}`);
  return b;
};

describe("shell and YAML quoting", () => {
  it("leaves safe words alone and single-quotes everything else", () => {
    expect(shQuote("olk_abc-123.x")).toBe("olk_abc-123.x");
    expect(shQuote("https://ingest.example.com:4318")).toBe("https://ingest.example.com:4318");
    expect(shQuote("")).toBe("''");
    expect(shQuote("a b")).toBe("'a b'");
    expect(shQuote("$(id)")).toBe("'$(id)'");
    expect(shQuote("it's")).toBe("'it'\\''s'");
    expect(shQuote(LICENSE_KEY_PLACEHOLDER)).toBe("'<LICENSE_KEY>'");
    expect(shQuote("~/x")).toBe("'~/x'");
  });

  it("quotes YAML scalars as JSON strings", () => {
    expect(yamlQuote('/var/log/a "b"/*.log')).toBe('"/var/log/a \\"b\\"/*.log"');
  });

  it("masks the key for display only", () => {
    expect(maskKey(`--license-key ${KEY} x ${KEY}`, KEY)).toBe("--license-key olk_•••••••••••• x olk_••••••••••••");
    expect(maskKey("x '<LICENSE_KEY>'", LICENSE_KEY_PLACEHOLDER)).toBe("x '<LICENSE_KEY>'");
    expect(maskKey("short abc", "abc")).toBe("short ••••••••••••");
  });
});

describe("infrastructure", () => {
  it("Linux: install.sh with the license key and the endpoint", () => {
    const r = build("linux");
    expect(r.blocks.map((b) => b.id)).toEqual(["install", "verify"]);
    expect(r.blocks[0]!.code).toBe(
      "curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh | sudo sh -s -- \\\n" +
        `  --license-key ${KEY} \\\n` +
        "  --endpoint https://ingest.example.com:4318",
    );
    expect(r.blocks[0]!.containsKey).toBe(true);
    expect(r.blocks[1]!.code).toBe("systemctl status openlog-infra-agent --no-pager\njournalctl -u openlog-infra-agent -f");
    expect(r.notes).toEqual(["distroAuto", "dockerGroup"]);
  });

  it("Linux: package method, beta channel, docker opt-out and a placeholder key", () => {
    const r = build("linux", { licenseKey: "  ", distro: "rpm", channel: "beta", dockerAccess: false });
    expect(r.blocks[0]!.code).toBe(
      "curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh | sudo sh -s -- \\\n" +
        "  --license-key '<LICENSE_KEY>' \\\n" +
        "  --endpoint https://ingest.example.com:4318 \\\n" +
        "  --method rpm \\\n" +
        "  --channel beta \\\n" +
        "  --no-docker-access",
    );
    expect(r.blocks[0]!.containsKey).toBe(false);
    expect(r.notes).toEqual(["placeholderKey", "archAuto"]);
  });

  it("Docker: container agent with a quoted custom key and the release tag", () => {
    const custom = "k$y&;|x-0123456789";
    const b = block("docker", "dockerRun", { licenseKey: custom });
    expect(b.code).toBe(
      [
        "docker run -d --name openlog-infra-agent --restart unless-stopped",
        "--pid=host --network=host",
        "-v /:/host:ro -v openlog-agent-state:/var/lib/openlog-infra-agent",
        "--cap-add SYS_PTRACE --cap-add DAC_READ_SEARCH",
        "-e OPENLOG_LICENSE_KEY='k$y&;|x-0123456789'",
        "-e OPENLOG_ENDPOINT=https://ingest.example.com:4318",
        "ghcr.io/onuragtas/openlog-infra-agent:0.9.1",
      ].join(" \\\n  "),
    );
    expect(b.containsKey).toBe(true);
    expect(block("docker", "dockerRun", {}, { agent_version: null }).code).toMatch(/openlog-infra-agent:latest$/);
  });

  it("Kubernetes: secret, chart from the release tag and escaped Helm values", () => {
    const r = build("kubernetes", { clusterName: "prod,eu", environment: "production" });
    expect(r.blocks.map((b) => b.id)).toEqual(["secret", "download", "helmInstall", "verify"]);
    expect(r.blocks[0]!.code).toBe(
      "kubectl create namespace openlog-agent\n" +
        "kubectl label namespace openlog-agent pod-security.kubernetes.io/enforce=privileged\n" +
        `kubectl -n openlog-agent create secret generic openlog-license --from-literal=license-key=${KEY}`,
    );
    expect(r.blocks[1]!.code).toBe("git clone --depth 1 --branch v0.9.1 https://github.com/onuragtas/openlog.git openlog");
    expect(r.blocks[2]!.code).toBe(
      "helm install openlog-agent ./openlog/deploy/helm/openlog-agent -n openlog-agent \\\n" +
        "  --set 'clusterName=prod\\,eu' \\\n" +
        "  --set endpoint=https://ingest.example.com:4318 \\\n" +
        "  --set existingSecret.name=openlog-license \\\n" +
        "  --set-string extraAttributes.env=production",
    );
    // The key goes into the Secret only, never into helm arguments.
    expect(r.blocks[2]!.containsKey).toBe(false);
    expect(block("kubernetes", "helmInstall", { clusterName: "" }).code).toContain("--set clusterName=my-cluster");
    expect(block("kubernetes", "download", {}, { agent_version: null }).code).toContain("--branch master");
  });
});

describe("APM agents", () => {
  it("Go: module, Start with the service name and environment variables", () => {
    const r = build("apm/go", { serviceName: ' check "out" ', environment: "production" });
    expect(r.blocks.map((b) => b.id)).toEqual(["install", "code", "run"]);
    expect(r.blocks[0]!.code).toBe("go get github.com/onuragtas/openlog/agents/go");
    expect(r.blocks[1]!.code).toContain('openlog.Start(context.Background(), openlog.WithServiceName("check out"))');
    expect(r.blocks[2]!.code).toBe(
      `export OPENLOG_LICENSE_KEY=${KEY}\n` +
        "export OPENLOG_ENDPOINT=https://ingest.example.com:4318\n" +
        "export OPENLOG_SERVICE_NAME='check out'\n" +
        "export OPENLOG_ENVIRONMENT=production\n" +
        "go run .",
    );
  });

  it("Node.js: CommonJS --require and ES modules --import", () => {
    expect(block("apm/node", "install", {}, onRegistry).code).toBe("npm install @openlog/node");
    expect(block("apm/node", "run", { serviceName: "checkout" }).code).toMatch(/export OPENLOG_SERVICE_NAME=checkout\nnode --require @openlog\/node\/register server\.js$/);
    const esm = build("apm/node", { nodeModules: "esm" });
    expect(esm.blocks.find((b) => b.id === "run")!.code).toMatch(/\nnode --import @openlog\/node\/register server\.mjs$/);
    expect(esm.notes).toContain("nodeEsm");
  });

  it("Python: openlog-agent with openlog-instrument", () => {
    expect(block("apm/python", "install", {}, onRegistry).code).toBe("pip install openlog-agent");
    expect(block("apm/python", "run").code).toMatch(/\nopenlog-instrument python app\.py$/);
    expect(block("apm/python", "run", { pythonLauncher: "gunicorn" }).code).toMatch(/\nopenlog-instrument gunicorn -w 4 -b 0\.0\.0\.0:8000 myproject\.wsgi:application$/);
  });

  it("Java: release jar with checksum, or a container image", () => {
    expect(block("apm/java", "download").code).toBe(
      "curl -fsSLO https://github.com/onuragtas/openlog/releases/download/v0.9.1/openlog-javaagent-0.9.1.jar\n" +
        "curl -fsSLO https://github.com/onuragtas/openlog/releases/download/v0.9.1/openlog-javaagent-0.9.1.jar.sha256\n" +
        "sha256sum -c openlog-javaagent-0.9.1.jar.sha256\n" +
        "sudo install -D -m 0644 openlog-javaagent-0.9.1.jar /opt/openlog/openlog-javaagent.jar",
    );
    expect(block("apm/java", "run").code).toMatch(/\njava -javaagent:\/opt\/openlog\/openlog-javaagent\.jar -jar app\.jar$/);
    const unknown = build("apm/java", {}, { agent_version: null });
    expect(unknown.blocks[0]!.code).toContain("/download/vX.Y.Z/openlog-javaagent-X.Y.Z.jar");
    expect(unknown.notes).toContain("versionUnknown");
    const docker = build("apm/java", { javaMode: "docker", serviceName: "orders" });
    expect(docker.blocks.map((b) => b.id)).toEqual(["dockerfile", "dockerRun"]);
    expect(docker.blocks[0]!.code).toBe(
      "ADD https://github.com/onuragtas/openlog/releases/download/v0.9.1/openlog-javaagent-0.9.1.jar /opt/openlog/openlog-javaagent.jar\n" +
        'ENV JAVA_TOOL_OPTIONS="-javaagent:/opt/openlog/openlog-javaagent.jar"\n' +
        'ENV OPENLOG_SERVICE_NAME="orders"',
    );
    // The key is passed at run time, not baked into the image.
    expect(docker.blocks[0]!.containsKey).toBe(false);
    expect(docker.blocks[1]!.containsKey).toBe(true);
  });

  it(".NET: OpenLog.Agent with AddOpenLog or OpenLogAgent.Start", () => {
    expect(block("apm/dotnet", "install", {}, onRegistry).code).toBe("dotnet add package OpenLog.Agent");
    expect(build("apm/dotnet", {}, onRegistry).blocks.map((b) => b.id)).toEqual(["install", "code", "run"]);
    expect(block("apm/dotnet", "code").code).toContain("builder.Services.AddOpenLog();");
    expect(block("apm/dotnet", "code", { dotnetApp: "console", serviceName: "invoice-job" }).code).toContain('OpenLogAgent.Start(o => o.ServiceName = "invoice-job")');
  });

  describe("language agent package source", () => {
    it("registry commands only when the server found the version on the registry", () => {
      for (const target of ["apm/node", "apm/python", "apm/dotnet"] as const) {
        const r = build(target, {}, onRegistry);
        expect(r.notes).toContain("packageRegistry");
        expect(r.notes).not.toContain("packageRelease");
        expect(r.blocks.map((b) => b.code).join("\n")).not.toContain("releases/download");
      }
    });

    it("GitHub release assets when the registry does not have the version", () => {
      const missing = packages("missing");
      expect(block("apm/node", "install", {}, missing).code).toBe(`npm install ${REL}/v0.9.1/openlog-node-0.9.1.tgz`);
      expect(block("apm/python", "install", {}, missing).code).toBe(`pip install ${REL}/v0.9.1/openlog_agent-0.9.1-py3-none-any.whl`);
      const dotnet = build("apm/dotnet", {}, missing);
      expect(dotnet.blocks.map((b) => b.id)).toEqual(["download", "install", "code", "run"]);
      expect(dotnet.blocks[0]!.code).toBe(
        'mkdir -p "$HOME/.openlog/nuget"\n' +
          `(cd "$HOME/.openlog/nuget" && curl -fsSLO ${REL}/v0.9.1/OpenLog.Agent.0.9.1.nupkg && curl -fsSLO ${REL}/v0.9.1/OpenLog.Agent.0.9.1.nupkg.sha256 && sha256sum -c OpenLog.Agent.0.9.1.nupkg.sha256)`,
      );
      expect(dotnet.blocks[1]!.code).toBe('dotnet nuget add source "$HOME/.openlog/nuget" -n openlog-local\ndotnet add package OpenLog.Agent --version 0.9.1');
      expect(dotnet.notes).toEqual(expect.arrayContaining(["packageRelease", "dotnetLocalSource"]));
      for (const target of ["apm/node", "apm/python", "apm/dotnet"] as const) {
        expect(build(target, {}, missing).notes).not.toContain("packageRegistry");
      }
    });

    it("unknown availability or an older server without agent_packages: GitHub release, derived URLs", () => {
      for (const info of [packages("unknown"), {}]) {
        expect(block("apm/node", "install", {}, info).code).toBe(`npm install ${REL}/v0.9.1/openlog-node-0.9.1.tgz`);
        expect(build("apm/python", {}, info).notes).toContain("packageReleaseUnchecked");
      }
    });

    it("pre-releases use the PEP 440 wheel name; dev builds use X.Y.Z", () => {
      expect(pythonVersion("1.2.0-beta.3")).toBe("1.2.0b3");
      expect(pythonVersion("1.2.0-rc.1")).toBe("1.2.0rc1");
      expect(pythonVersion("1.2.0")).toBe("1.2.0");
      expect(block("apm/python", "install", {}, { agent_version: "1.2.0-beta.3" }).code).toBe(`pip install ${REL}/v1.2.0-beta.3/openlog_agent-1.2.0b3-py3-none-any.whl`);
      const dev = build("apm/dotnet", {}, { agent_version: null, ...packages("available") });
      expect(dev.blocks[1]!.code).toContain("dotnet add package OpenLog.Agent --version X.Y.Z");
      expect(dev.blocks[0]!.code).toContain(`${REL}/vX.Y.Z/OpenLog.Agent.X.Y.Z.nupkg`);
      expect(dev.notes).toEqual(expect.arrayContaining(["versionUnknown", "packageReleaseUnchecked"]));
    });
  });

  it("PHP: release package + openlog-php-install, or the fleet", () => {
    const pkg = build("apm/php", { phpPackage: "rpm", arch: "arm64", serviceName: "shop" });
    expect(pkg.blocks.map((b) => b.id)).toEqual(["packageInstall", "enablePhp", "phpSettings"]);
    expect(pkg.blocks[0]!.code).toBe(
      "curl -fsSLO https://github.com/onuragtas/openlog/releases/download/v0.9.1/openlog-php-agent_0.9.1_linux_arm64.rpm\n" +
        "sudo dnf install ./openlog-php-agent_0.9.1_linux_arm64.rpm",
    );
    expect(pkg.blocks[1]!.code).toBe("sudo openlog-php-install status\nsudo openlog-php-install install --reload");
    expect(pkg.blocks[2]!.code).toContain('openlog.service_name = "shop"');
    // PHP spans go through the infra agent: no license key in PHP commands.
    expect(pkg.notes).not.toContain("placeholderKey");
    expect(pkg.notes).toContain("phpNeedsInfraAgent");
    expect(block("apm/php", "packageInstall", { phpPackage: "deb" }).code).toMatch(/\nsudo apt-get install \.\/openlog-php-agent_0\.9\.1_linux_amd64\.deb$/);

    const fleet = build("apm/php", { phpMode: "fleet" });
    expect(fleet.blocks.map((b) => b.id)).toEqual(["fleetConfig", "restart", "phpSettings"]);
    expect(fleet.blocks[0]!.code).toContain("php_agent:\n  mode: auto\n  reload: graceful");
    expect(fleet.notes).toContain("phpFleetPage");
    expect(build("apm/php", { phpMode: "fleet" }, { features: { fleet_php_install: false } }).notes).toContain("phpFleetUnavailable");
  });
});

describe("logs", () => {
  it("host files and journald through the infra agent", () => {
    const r = build("logs/host", { logPath: '/var/log/my "app"/*.log', journald: true });
    expect(r.blocks.map((b) => b.id)).toEqual(["agentConfig", "journalAccess", "restart"]);
    expect(r.blocks[0]!.code).toBe(
      "# /etc/openlog-infra-agent/config.yaml\nlogs:\n  enabled: true\n  files:\n" + '    - path: "/var/log/my \\"app\\"/*.log"\n' + "  journald:\n    enabled: true",
    );
    expect(r.blocks[1]!.code).toBe("sudo usermod -aG systemd-journal openlog-agent");
    expect(r.notes).not.toContain("placeholderKey");
  });

  it("container logs", () => {
    const r = build("logs/containers");
    expect(r.blocks.map((b) => b.id)).toEqual(["agentConfig", "containerLabels", "restart"]);
    expect(r.blocks[1]!.code).toContain("docker run --label openlog.logs=false");
  });

  it("browser OTLP/JSON logs: CORS setting only when not configured", () => {
    const r = build("logs/browser", { serviceName: "web-shop", browserOrigin: "https://shop.example.com" });
    expect(r.blocks.map((b) => b.id)).toEqual(["serverCors", "sender"]);
    expect(r.blocks[0]!.code).toContain("OPENLOG_INGEST_CORS_ALLOWED_ORIGINS=https://shop.example.com");
    expect(r.blocks[1]!.code).toContain('const OPENLOG_LOGS_URL = "https://ingest.example.com:4318/v1/logs";');
    expect(r.blocks[1]!.code).toContain(`const OPENLOG_LICENSE_KEY = "${KEY}";`);
    expect(r.blocks[1]!.code).toContain('"openlog-license-key": OPENLOG_LICENSE_KEY');
    expect(r.blocks[1]!.code).toContain('{ key: "service.name", value: { stringValue: "web-shop" } }');
    const configured = build("logs/browser", {}, { cors_enabled: true });
    expect(configured.blocks.map((b) => b.id)).toEqual(["sender"]);
    expect(configured.notes).toContain("corsConfigured");
  });

  it("OpenTelemetry logs export", () => {
    const b = block("logs/otel", "run", { otelLanguage: "python", serviceName: "api" });
    expect(b.code).toContain("export OTEL_LOGS_EXPORTER=otlp");
    expect(b.code).toContain("export OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED=true");
  });
});

describe("OpenTelemetry", () => {
  it("SDK environment: HTTP endpoint and percent-encoded header values", () => {
    const r = build("otel/sdk", { otelLanguage: "node", serviceName: "checkout", environment: "prod eu", licenseKey: "abc=def,ghi%0123456789" });
    expect(r.blocks.map((b) => b.id)).toEqual(["otelInstall", "run"]);
    expect(r.blocks[1]!.code).toBe(
      "export OTEL_EXPORTER_OTLP_ENDPOINT=https://ingest.example.com:4318\n" +
        "export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf\n" +
        "export OTEL_EXPORTER_OTLP_HEADERS=openlog-license-key=abc%3Ddef%2Cghi%250123456789\n" +
        "export OTEL_SERVICE_NAME=checkout\n" +
        "export OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=prod%20eu\n" +
        "node --require @opentelemetry/auto-instrumentations-node/register app.js",
    );
  });

  it("SDK environment: gRPC endpoint", () => {
    const b = block("otel/sdk", "run", { otelLanguage: "java", protocol: "grpc" });
    expect(b.code).toContain("export OTEL_EXPORTER_OTLP_ENDPOINT=https://ingest.example.com:4317\nexport OTEL_EXPORTER_OTLP_PROTOCOL=grpc");
    expect(b.code).toMatch(/\njava -javaagent:\.\/opentelemetry-javaagent\.jar -jar app\.jar$/);
  });

  it("Collector: exporter reads the key from the environment", () => {
    const http = build("otel/collector");
    expect(http.blocks.map((b) => b.id)).toEqual(["collectorEnv", "collectorConfig", "collectorRun"]);
    expect(http.blocks[0]!.code).toBe(`export OPENLOG_LICENSE_KEY=${KEY}`);
    expect(http.blocks[1]!.code).toContain('  otlphttp/openlog:\n    endpoint: "https://ingest.example.com:4318"\n    compression: gzip\n    headers:\n      openlog-license-key: ${env:OPENLOG_LICENSE_KEY}');
    expect(http.blocks[1]!.code).toContain("      exporters: [otlphttp/openlog]");
    expect(http.blocks[1]!.containsKey).toBe(false);

    const grpc = build("otel/collector", { protocol: "grpc" }, { otlp_grpc: { url: "http://10.0.0.5:4317" } });
    expect(grpc.blocks[1]!.code).toContain('  otlp/openlog:\n    endpoint: "10.0.0.5:4317"\n    tls:\n      insecure: true');
    expect(grpc.notes).toContain("grpcInsecure");
  });
});

describe("integrations", () => {
  it("MySQL: least-privilege user, password file and agent config", () => {
    const r = build("integrations/mysql");
    expect(r.blocks.map((b) => b.id)).toEqual(["sqlUser", "passwordFile", "agentConfig", "restart"]);
    expect(r.blocks[0]!.code).toContain("GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'openlog'@'localhost';");
    expect(r.blocks[1]!.code).toBe(
      "sudo install -m 0600 -o openlog-agent -g openlog-agent /dev/null /etc/openlog-infra-agent/mysql.password\nsudoedit /etc/openlog-infra-agent/mysql.password",
    );
    expect(r.blocks.some((b) => b.containsKey)).toBe(false);
    expect(r.notes).not.toContain("placeholderKey");
  });

  it("PostgreSQL, Redis and nginx", () => {
    expect(block("integrations/postgresql", "sqlUser").code).toContain("GRANT pg_monitor TO openlog;");
    expect(block("integrations/redis", "redisAcl").code).toBe("redis-cli ACL SETUSER openlog on '><password>' +info +ping");
    expect(block("integrations/nginx", "stubStatus").code).toContain("location = /nginx_status {\n    stub_status;");
  });
});

describe("every target", () => {
  it("never puts the license key into a URL and marks blocks that contain it", () => {
    for (const t of INSTALL_TARGETS) {
      for (const patch of [{}, { protocol: "grpc" as const }, { phpMode: "fleet" as const }, { javaMode: "docker" as const }]) {
        const r = build(t.id, patch);
        expect(r.blocks.length, t.id).toBeGreaterThan(0);
        expect(new Set(r.blocks.map((b) => b.id)).size, `${t.id}: unique block ids`).toBe(r.blocks.length);
        for (const b of r.blocks) {
          for (const url of b.code.match(/https?:\/\/[^\s"'`)]+/g) ?? []) expect(url, `${t.id}/${b.id}`).not.toContain(KEY);
          expect(b.containsKey, `${t.id}/${b.id}`).toBe(b.code.includes(KEY));
        }
      }
    }
  });

  it("placeholder note only for cards that send with their own key", () => {
    for (const t of INSTALL_TARGETS) {
      const r = buildInstallCommands(t.id, { ...DEFAULT_OPTIONS }, INFO);
      expect(r.notes.includes("placeholderKey"), t.id).toBe(targetNeedsKey(t.id));
    }
  });

  it("finds targets from deep links and discovery hints", () => {
    expect(findTarget("apm/node")?.group).toBe("apm");
    expect(findTarget("/linux/")?.id).toBe("linux");
    expect(findTarget("nope")).toBeUndefined();
    expect(apmTargetForLanguage("nodejs")).toBe("apm/node");
    expect(apmTargetForLanguage("PHP")).toBe("apm/php");
    expect(apmTargetForLanguage("jvm")).toBe("apm/java");
    expect(apmTargetForLanguage("dotnet")).toBe("apm/dotnet");
    expect(apmTargetForLanguage("python")).toBe("apm/python");
    expect(apmTargetForLanguage("ruby")).toBeUndefined();
  });
});
