# Dil ajanlarını güncel tutmak {#keeping-language-agents-up-to-date}

Dil ajanları, yani Java (`openlog-javaagent` jar), Node.js (npm `openlog-node`), Python (PyPI `openlog-agent`),
.NET (NuGet `OpenLog.Agent`), Go (`github.com/onuragtas/openlog/agents/go/...` modülleri) ve PHP eklentisi, ürünle aynı
sürüm numarasını paylaşır (D-025). Fleet yönetiminin yerinde güncellediği infra ajanının aksine çoğu birer
**uygulama bağımlılığıdır**: lockfile'larda sabitlenir (`package-lock.json`, `poetry.lock`, `packages.lock.json`,
`go.sum`) ve CI tarafından imajlara derlenir. Bu yüzden openlog onları asla kendisi güncellemez, çünkü sessiz bir güncelleme
kimsenin incelemediği bir derlemeyi değiştirirdi (D-124).

## Eski ajanları görmek {#seeing-outdated-agents}

- **APM → Services**: bir ajan `outdated` veya `unsupported` ise servis adının yanında rozet çıkar, tooltip çalışan ve en
  son sürümü gösterir.
- **Service overview**: instance sayısına göre sürümleri, en son release'i ve yükseltme komutunu içeren bir bildirim
  (en son sürüm başına, tarayıcı başına kapatılabilir).
- **APM → Agent versions** (`/apm/agents`): her servis × ajan × sürüm kombinasyonu durumuyla birlikte, ayrıca "outdated only"
  filtresi ve CSV dışa aktarma.
- API: `GET /api/v1/apm/agents` ([api.md](/contracts/api) "APM").

Durumlar: `outdated` = organizasyonun fleet kanalındaki doğrulanmış en yeni release'ten eski (Fleet → policy
channel; fleet yönetimi yoksa `stable`), `unsupported` = release manifest'indeki `compatibility.oldest_supported_agent`
değerinden eski, `unknown` = release kataloğu yok (static auth modu, release mirror'ı olmayan çevrimdışı kurulumlar, katalog
henüz çekilmemiş) veya geliştirme derlemesi, `third_party` = düz bir OpenTelemetry SDK'sı (karşılaştırma yapılmaz).

Sürümler span resource'undan gelir (`telemetry.distro.name`, `telemetry.distro.version`, `telemetry.sdk.*`) ve saatlik
toplanır (ClickHouse `apm_agent_versions_1h`, 30 gün). Bir servis, ilk span'leri 0090 migration'ına sahip bir backend
tarafından ingest edildikten sonra görünür; geriye dönük doldurma yoktur.

## Yükseltme komutları {#upgrade-commands}

Arayüzdeki komut en son release'i sabitler. npm, PyPI ve nuget.org'da bulunup bulunmadığı Add data sayfasındaki gibi
kontrol edilir; registry o sürümü sunmuyorsa (veya registry'ye ulaşılamıyorsa) komut bunun yerine GitHub release'ine ekli
dosyayı kurar.

| Ajan | Komut |
|---|---|
| Node.js | `npm install openlog-node@X.Y.Z` |
| Python | `pip install -U "openlog-agent==X.Y.Z"` (pre-release'ler için PEP 440: `X.Y.Zrc1`) |
| .NET | `dotnet add package OpenLog.Agent --version X.Y.Z` |
| Go | `go get github.com/onuragtas/openlog/agents/go@vX.Y.Z` ve kullandığın her enstrümantasyon modülü (`.../instrumentation/gin@vX.Y.Z`; gin, echo ve grpc span scope'larından algılanır, chi algılanmaz), ardından `go mod tidy` |
| Java | `openlog-javaagent-X.Y.Z.jar` dosyasını release'ten indir; infra ajanının yönettiği host'larda jar `java_agent.mode: auto` ile güncel tutulabilir ([java-agent.md](/contracts/java-agent) "Distribution and updates") |
| PHP | fleet kurulumu (infra ajanı yapılandırmasında `php_agent.mode: auto`) infra ajanının sürümünü izler; paket kurulumlarında release'teki yeni `.deb`/`.rpm`/`.apk` paketini kur ([php-agent.md](/contracts/php-agent) §7) |

## Bağımlılık güncellemelerini otomatikleştirmek {#automating-dependency-updates}

Bağımlılık botun pull request açsın, böylece yükseltmeler diğer bağımlılıklar gibi inceleme ve CI'dan geçsin.

### Renovate {#renovate}

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

Beta kanalındaysan ilk kurala `"ignoreUnstable": false` ekle; stable kullanıyorsan varsayılan kalsın (pre-release'ler
atlanır).

### Dependabot {#dependabot}

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

Dependabot'un `allow` ayarı bu girdileri openlog paketleriyle sınırlar; aynı girdinin her bağımlılığı güncellemesini
istiyorsan onu kaldır.

### Java jar'ı ve PHP eklentisi {#java-jar-and-php-extension}

İki bot da Dockerfile içinde indirilen bir jar'ı veya PHP eklenti paketini takip etmez. Seçeneklerin:

- **İmajlarda Java**: sürümü tek bir yerde sabitle (`ARG OPENLOG_JAVAAGENT_VERSION=X.Y.Z` ve
  `ADD https://github.com/onuragtas/openlog/releases/download/v${OPENLOG_JAVAAGENT_VERSION}/openlog-javaagent-${OPENLOG_JAVAAGENT_VERSION}.jar ...`)
  ve Renovate'in regex manager'ı onu GitHub release'lerinden güncellesin (`datasourceTemplate: github-releases`,
  `depNameTemplate: onuragtas/openlog`, `extractVersionTemplate: ^v(?<version>.*)$`).
- **Infra ajanlı host'larda Java**: `java_agent.mode: auto` ([java-agent.md](/contracts/java-agent)
  "Distribution and updates").
- **PHP**: fleet kurulumunu tercih et (`php_agent.mode: auto`), fleet yönetiminin güncellediği infra ajanıyla birlikte
  ilerler; paket kurulumları işletim sisteminin paket sürecini izler.
