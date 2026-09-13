# openlog — Plan Belgeleri

openlog, sıfırdan yazılan, New Relic benzeri, açık kaynak bir gözlemlenebilirlik (observability) platformudur.
Aynı kod tabanı hem SaaS olarak hem de müşterinin kendi altyapısında (self-hosted) çalışır ve **yüke göre yatayda ölçeklenen bir cluster** olarak tasarlanmıştır.

| Belge | İçerik |
|---|---|
| [01-vision-scope.md](01-vision-scope.md) | Vizyon, hedef kullanıcı, kapsam ve kapsam dışı |
| [02-decisions.md](02-decisions.md) | Alınan kararlar ve gerekçeleri (karar günlüğü) |
| [03-architecture.md](03-architecture.md) | Genel mimari, bileşenler, veri akışı |
| [04-cluster.md](04-cluster.md) | Cluster ve ölçekleme tasarımı, kurulum profilleri |
| [05-infra-agent.md](05-infra-agent.md) | Infra agent: metrikler, envanter, otomatik keşif |
| [06-roadmap.md](06-roadmap.md) | Kilometre taşları (M0–M4) ve kabul kriterleri |
| [07-teams.md](07-teams.md) | Ekip yapısı ve sorumluluklar |
| [08-risks.md](08-risks.md) | Riskler ve önlemler |
| [10-m2.md](10-m2.md) | M2: alarm, entegrasyonlar, APM, Go agent, PHP agent tasarımı |
| [09-releases-updates.md](09-releases-updates.md) | Tek ürün sürümü, imzalı release'ler, agent ve backend otomatik güncelleme |

Ekiplerin birbirinden bağımsız çalışabilmesi için bağlayıcı sözleşmeler ayrı tutulur:

| Sözleşme | İçerik |
|---|---|
| [../contracts/semantic-conventions.md](../contracts/semantic-conventions.md) | Resource attribute'ları, metrik isimleri, envanter/keşif olayları |
| [../contracts/kafka.md](../contracts/kafka.md) | Topic'ler, mesaj formatı, partition anahtarları |
| [../contracts/config.md](../contracts/config.md) | Servislerin ortam değişkenleri ve portları |
| [../contracts/api.md](../contracts/api.md) | Query API (M0 uç noktaları) |
| [../../schema/clickhouse/](../../schema/clickhouse/) | ClickHouse şeması |

Bir sözleşmeyi değiştiren her PR, bu belgeleri de aynı PR'da güncellemek zorundadır.
