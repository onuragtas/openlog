# Keeping language agents up to date

The language agents — Java (`openlog-javaagent` jar), Node.js (npm `openlog-node`), Python (PyPI `openlog-agent`),
.NET (NuGet `OpenLog.Agent`), Go (modules `github.com/onuragtas/openlog/agents/go/...`) and the PHP extension — share
the product version (D-025). Unlike the infra agent, which fleet management updates in place, most of them are
**application dependencies**: they are pinned in lockfiles (`package-lock.json`, `poetry.lock`, `packages.lock.json`,
`go.sum`) and built into images by CI. openlog therefore never updates them itself; a silent update would change a
build nobody reviewed (D-124).

## Seeing outdated agents

- **APM → Services**: a badge next to the service name when an agent is `outdated` or `unsupported`; the tooltip
  shows the running and latest version.
- **Service overview**: a notice with the versions per instance count, the latest release and the upgrade command
  (dismissible per latest version, per browser).
- **APM → Agent versions** (`/apm/agents`): every service × agent × version with its status, an "outdated only"
  filter and CSV export.
- API: `GET /api/v1/apm/agents` ([api.md](../contracts/api.md) "APM").

Statuses: `outdated` = older than the newest verified release of the organization's fleet channel (Fleet → policy
channel; `stable` without fleet management), `unsupported` = older than `compatibility.oldest_supported_agent` of the
release manifest, `unknown` = no release catalog (static auth mode, offline installations without a release mirror,
catalog not fetched yet) or a development build, `third_party` = a plain OpenTelemetry SDK (no comparison).

Versions come from the span resource (`telemetry.distro.name`, `telemetry.distro.version`, `telemetry.sdk.*`) and are
aggregated per hour (ClickHouse `apm_agent_versions_1h`, 30 days). A service shows up after its first spans are
ingested by a backend with migration 0090; there is no backfill.

## Upgrade commands

The command in the UI pins the latest release. npm, PyPI and nuget.org availability is checked like on the Add data
page; when the registry does not serve the version (or cannot be reached), the command installs the file attached to
the GitHub release instead.

| Agent | Command |
|---|---|
| Node.js | `npm install openlog-node@X.Y.Z` |
| Python | `pip install -U "openlog-agent==X.Y.Z"` (PEP 440 for pre-releases: `X.Y.Zrc1`) |
| .NET | `dotnet add package OpenLog.Agent --version X.Y.Z` |
| Go | `go get github.com/onuragtas/openlog/agents/go@vX.Y.Z` plus every instrumentation module you use (`.../instrumentation/gin@vX.Y.Z`; gin, echo and grpc are detected from span scopes, chi is not), then `go mod tidy` |
| Java | download `openlog-javaagent-X.Y.Z.jar` from the release; hosts managed by the infra agent can keep the jar current with `java_agent.mode: auto` ([java-agent.md](../contracts/java-agent.md) "Distribution and updates") |
| PHP | fleet installation (`php_agent.mode: auto` in the infra agent configuration) follows the infra agent version; package installations: install the new `.deb`/`.rpm`/`.apk` from the release ([php-agent.md](../contracts/php-agent.md) §7) |

## Automating dependency updates

Let your dependency bot open pull requests so upgrades go through review and CI like any other dependency.

### Renovate

`renovate.json`:

```json
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "extends": ["config:recommended"],
  "packageRules": [
    {
      "description": "openlog language agents: one grouped PR per release",
      "groupName": "openlog agents",
      "matchPackageNames": [
        "openlog-node",
        "openlog-agent",
        "OpenLog.Agent",
        "github.com/onuragtas/openlog/agents/go{/,}**"
      ],
      "matchManagers": ["npm", "pip_requirements", "pep621", "poetry", "nuget", "gomod"]
    },
    {
      "description": "Go: keep the core and instrumentation modules on the same version",
      "matchManagers": ["gomod"],
      "matchPackageNames": ["github.com/onuragtas/openlog/agents/go{/,}**"],
      "postUpdateOptions": ["gomodTidy"]
    }
  ]
}
```

Beta channel users add `"ignoreUnstable": false` to the first rule; stable users keep the default (pre-releases are
skipped).

### Dependabot

`.github/dependabot.yml`:

```yaml
version: 2
updates:
  - package-ecosystem: npm
    directory: /
    schedule: { interval: weekly }
    allow: [{ dependency-name: openlog-node }]
    groups: { openlog: { patterns: ["openlog-node"] } }
  - package-ecosystem: pip
    directory: /
    schedule: { interval: weekly }
    allow: [{ dependency-name: openlog-agent }]
  - package-ecosystem: nuget
    directory: /
    schedule: { interval: weekly }
    allow: [{ dependency-name: OpenLog.Agent }]
  - package-ecosystem: gomod
    directory: /
    schedule: { interval: weekly }
    allow: [{ dependency-name: "github.com/onuragtas/openlog/agents/go*" }]
    groups: { openlog: { patterns: ["github.com/onuragtas/openlog/agents/go*"] } }
```

Dependabot's `allow` limits these entries to the openlog packages; drop it to let the same entry update every
dependency.

### Java jar and PHP extension

Neither bot tracks a jar downloaded in a Dockerfile or a PHP extension package. Options:

- **Java in images**: pin the version in one place (`ARG OPENLOG_JAVAAGENT_VERSION=X.Y.Z` and
  `ADD https://github.com/onuragtas/openlog/releases/download/v${OPENLOG_JAVAAGENT_VERSION}/openlog-javaagent-${OPENLOG_JAVAAGENT_VERSION}.jar ...`)
  and let Renovate's regex manager update it from GitHub releases (`datasourceTemplate: github-releases`,
  `depNameTemplate: onuragtas/openlog`, `extractVersionTemplate: ^v(?<version>.*)$`).
- **Java on hosts with the infra agent**: `java_agent.mode: auto` ([java-agent.md](../contracts/java-agent.md)
  "Distribution and updates").
- **PHP**: prefer the fleet installation (`php_agent.mode: auto`), which moves with the infra agent that fleet
  management updates; package installations follow your OS package process.
