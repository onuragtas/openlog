# MCP sunucusu (`openlog-mcp`) {#mcp-server-openlog-mcp}

`openlog-mcp`, yapay zekâ araçlarının (Claude Code, Cursor ve
[Model Context Protocol](https://modelcontextprotocol.io) konuşan başka her şey) bir openlog kurulumunu sorgulamasını sağlar: OQL
çalıştırmak, APM servislerine bakmak, log kayıtlarını okumak, alarm olaylarını ve SLO error budget'larını kontrol etmek. Her araç
**salt okunurdur** (D-126).

Sunucu, veritabanlarının değil query API'nin önyüzüdür. Her araç çağrısı, **senin API key'ini** taşıyan bir veya iki
`/api/v1` isteğidir, bu yüzden API'nin uyguladığı her şey aynen geçerlidir: anahtar kendi rolüyle kimlik doğrular, her ClickHouse
sorgusunun tenant'ı yalnızca o anahtardan gelir ve sorgular, organizasyonun limitleriyle salt okunur ClickHouse kullanıcısı olarak
çalışır ([api.md](/contracts/api#authentication), [config.md](/contracts/config)). Sürecin kendisi ClickHouse, PostgreSQL veya Kafka
erişimi istemez ve hiçbir şey saklamaz.

## 1. API key {#_1-an-api-key}

Web arayüzünde **Settings → API keys** altından veya `openlog-admin bootstrap` ile
(`OPENLOG_BOOTSTRAP_API_KEY`) bir tane oluştur. Anahtar `ola_…` biçimindedir ve yalnızca bir kez gösterilir.

Bir API key tek bir organizasyona aittir ve bir rol taşır: `viewer` (varsayılan), `member` veya
`admin` ([api.md "Roles"](/contracts/api#roles), D-133). Buradaki her araç yalnızca okuma endpoint'lerini çağırır, yani
`viewer` anahtarı yeterlidir: MCP sunucusuna fazlasını verme. Ingest lisans anahtarları
(`olk_…`) API key **değildir** ve burada çalışmaz.

## 2. Transport'lar {#_2-transports}

| Transport | Kimin için | Anahtar |
|---|---|---|
| `stdio` (varsayılan) | binary'yi kendisi başlatan yerel bir CLI veya editör | `OPENLOG_MCP_API_KEY` |
| `http` (`/mcp` üzerinde streamable HTTP) | paylaşılan veya uzak bir sunucu | her isteğin `Authorization: Bearer ola_…` başlığı, yoksa `OPENLOG_MCP_API_KEY` |

`http` modunda o oturum için çağıranın kendi anahtarı kullanılır, böylece tek bir kurulum birden fazla organizasyona
karıştırmadan hizmet verir; anahtarsız ve yapılandırılmış yedeği olmayan bir istek, oturum başlamadan `401` ile reddedilir.
API'de olduğu gibi önüne TLS koy (binary düz HTTP sunar).

Değişkenler: [config.md → `openlog-mcp`](/contracts/config#openlog-mcp).

```sh
# yerel, bu makinedeki bir stack'e karşı
OPENLOG_MCP_API_KEY=ola_… openlog-mcp

# uzak, :8092'de dinler, anahtarlar istek başına
OPENLOG_MCP_TRANSPORT=http OPENLOG_MCP_API_URL=https://openlog.example.com openlog-mcp
```

`-transport http` ile her openlog binary'sinin admin sunucusu da sunulur
(`OPENLOG_ADMIN_ADDR`, `:9464`): `/healthz`, `/readyz` (openlog API'nin yanıt verdiğini ve MCP portunun dinlediğini kontrol eder) ve
`openlog_mcp_tool_calls_total{tool,result}` ile `openlog_mcp_tool_duration_seconds{tool}` içeren `/metrics`. `stdio`
transport'u listener başlatmaz ve iki modda da loglar stderr'e gider (stdout protokolü taşır).

## 3. Araçlar {#_3-tools}

Hepsi salt okunur, hepsi anahtarın organizasyonuyla sınırlı.

| Araç | Argümanlar | Yanıtladığı |
|---|---|---|
| `run_oql` | `query`, `from`/`to`, `variables` | bir OQL aggregate'i: tek satır, facet'ler, zaman serisi veya histogram ([oql.md](/contracts/oql)) |
| `oql_schema` | `event_type` | event tipleri, attribute'lar, fonksiyonlar, anahtar kelimeler; `event_type` ile son bir saatin canlı attribute anahtarları da |
| `list_services` | `from`/`to`, `namespace`, `environment`, `q` | RED metrikleriyle APM servisleri (istekler, throughput, hatalar, hata oranı, gecikme yüzdelikleri, Apdex) |
| `service_summary` | `service`, `from`/`to`, `namespace`, `environment`, `transaction`, `limit` | bir servisin RED toplamları ve serileri, ayrıca harcanan süreye göre transaction'ları |
| `list_incidents` | `state`, `rule_id`, `severity`, `limit`, `cursor` | alarm olayları, en yenisi önce, duruma göre sayılarla |
| `get_incident` | `id` | zaman çizelgesi ve bildirim teslimatlarıyla bir olay |
| `search_logs` | `from`/`to`, `q`, `filters`, `order`, `limit`, `columns`, `cursor` | tek tek log kayıtları ([api.md “Fields”](/contracts/api#fields) filtre koşulları) |
| `list_slos` | `status` | kayan pencere üzerindeki error budget'larıyla SLO'lar |
| `slo_status` | `id`, `step` | bir SLO'nun budget'ı, çok pencereli burn rate'leri ve burndown serisi ([slo.md](/contracts/slo)) |

Aralıklar varsayılan olarak son bir saattir. `OPENLOG_MCP_MAX_ROWS` (100) her `limit` için üst sınır koyar, böylece bir model
bağlamına sınırsız sonuç çekemez. Organizasyonun sorgu limitleri yine geçerlidir: fazla pahalı bir sorgu `resource_exhausted`,
yavaş bir sorgu `timeout` olarak döner; aralığı daralt veya filtre ekle.

`list_slos` ve `slo_status`, PostgreSQL kimlik doğrulamasıyla çalışan bir openlog ister
(`OPENLOG_AUTH_MODE=postgres`); `static` ile endpoint'ler gibi `404` yanıtı verirler.

## 4. Claude Code {#_4-claude-code}

Proje başına, deponun `.mcp.json` dosyasında (veya `claude mcp add` ile):

```json
{
  "mcpServers": {
    "openlog": {
      "command": "openlog-mcp",
      "env": {
        "OPENLOG_MCP_API_URL": "https://openlog.example.com",
        "OPENLOG_MCP_API_KEY": "ola_…"
      }
    }
  }
}
```

Bunun yerine uzak bir sunucu:

```json
{
  "mcpServers": {
    "openlog": {
      "type": "http",
      "url": "https://openlog-mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ola_…" }
    }
  }
}
```

Bağlanınca Claude Code'daki `/mcp` sunucuyu ve araçlarını listeler.

## 5. Cursor {#_5-cursor}

Projedeki `.cursor/mcp.json` (veya tüm projeler için `~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "openlog": {
      "command": "openlog-mcp",
      "env": {
        "OPENLOG_MCP_API_URL": "https://openlog.example.com",
        "OPENLOG_MCP_API_KEY": "ola_…"
      }
    }
  }
}
```

```json
{
  "mcpServers": {
    "openlog": {
      "url": "https://openlog-mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ola_…" }
    }
  }
}
```

Settings → MCP sunucuyu gösterir ve tek tek araçları açmanı sağlar.

## 6. Birden fazla organizasyondaki kullanıcılar {#_6-users-in-several-organizations}

Bir API key tek bir organizasyona aittir, bu yüzden hiçbir şey seçmen gerekmez. Birden fazla organizasyona üye bir kullanıcının
anahtarı `OPENLOG_MCP_ORG_ID` (stdio) veya `X-Openlog-Org-Id` başlığı (http) ile sabitlenebilir; bu değer anahtarın organizasyonunu
belirtmelidir, yoksa API `403` yanıtı verir.

## 7. Sorun giderme {#_7-troubleshooting}

| Belirti | Neden |
|---|---|
| `openlog rejected the API key` | yanlış, iptal edilmiş veya süresi dolmuş anahtar ya da ingest lisans anahtarı (`olk_…`) |
| `openlog refused the request` | anahtarın rolü endpoint'e izin vermiyor veya organizasyonda bu özellik yok (SLO'lar postgres auth modu ister) |
| `cannot reach the openlog API at …` | `OPENLOG_MCP_API_URL` yanlış veya ulaşılamıyor; bu **temel** URL'dir, `/api/v1` olmadan |
| `resource_exhausted` / `timeout` | sorgu organizasyonun ClickHouse limitlerine takıldı: daha kısa aralık, daha fazla filtre |
| istemci hiç araç göstermiyor | sürecin stderr çıktısına bak; `http` ile `curl -sS -H 'Authorization: Bearer ola_…' https://…/mcp` `401` dönmemeli |
