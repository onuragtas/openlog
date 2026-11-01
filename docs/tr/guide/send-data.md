# Entegrasyon seç

Görmek istediğin şeyden başla. Aşağıdaki her kılavuzun kendi hızlı başlangıcı var.

## Neyi izliyorsun?

| Görmek istediğin | Kullanacağın | Anahtar |
|---|---|---|
| Bir makinenin CPU, bellek, disk, log ve servisleri | [Altyapı ajanı](/tr/integrations/infra) | Lisans |
| Bir sürecin neden CPU kullandığı, süreci değiştirmeden | Altyapı ajanıyla gelen [tüm makine profilleyici](/tr/integrations/ebpf) | Lisans |
| Backend'deki yavaş istekler ve hatalar | Bir [APM ajanı](#backend-apm) | Lisans |
| Web sitesinde sayfa hızı, hatalar ve oturumlar | [Tarayıcı SDK'sı](/tr/integrations/browser) | Tarayıcı |
| Mobil uygulamada ekranlar, hatalar ve event'ler | [iOS](/tr/integrations/ios), [Android](/tr/integrations/android) veya [Flutter](/tr/integrations/flutter) | Mobil |
| Unity oyununda crash'ler, event'ler ve oyuncular | [Unity SDK'sı](/tr/integrations/unity) | Mobil |
| Mobil uygulamada crash'ler, sürüm bazında | Mobil SDK üzerinde [crash raporlama](./crash-reporting.md) | Mobil |
| Günlük kullanıcılar, retention ve funnel | Tarayıcı veya mobil SDK üzerinde [ürün analitiği](./product-analytics.md) | Tarayıcı veya mobil |
| Bir Kubernetes kümesi | [Kubernetes](/tr/operations/kubernetes) | Lisans |

## Backend APM

| Dil | Kılavuz | Kod değişikliği | Kurulum |
|---|---|---|---|
| Go | [Go ajanı](/tr/integrations/go) | Başlangıçta birkaç satır | `go get github.com/onuragtas/openlog/agents/go` |
| Node.js | [Node.js ajanı](/tr/integrations/node) | Yok | `npm install openlog-node` |
| Python | [Python ajanı](/tr/integrations/python) | Yok | `pip install openlog-agent` |
| Java | [Java ajanı](/tr/integrations/java) | Yok, `-javaagent` olarak çalışır | Jar'ı sürümden indir |
| .NET | [.NET ajanı](/tr/integrations/dotnet) | Kılavuza bak | `dotnet add package OpenLog.Agent` |
| PHP | [PHP eklentisi](/tr/integrations/php) | Yok, C eklentisidir | Kılavuza bak |

### Zaten OpenTelemetry mi kullanıyorsun?

Herhangi bir OpenTelemetry SDK'sı veya Collector, openlog ajanı olmadan openlog'a veri gönderebilir:

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://<sunucu>:4318
export OTEL_EXPORTER_OTLP_HEADERS=openlog-license-key=<olk_ anahtarın>
```

openlog ajanları buna ek olarak framework'leri otomatik izler ve servisi çalıştığı makineye bağlar.
[Semantic conventions](/tr/contracts/semantic-conventions) sayfası openlog'un tanıdığı alanları listeler.

## Parçaları birbirine bağla

Entegrasyonlar birlikte çalışır. Kurmaya değer bağlantılar şunlar:

- **Tarayıcıdan backend'e.** Tarayıcı SDK'sı aynı origin'e giden isteklere W3C `traceparent` başlığı ekler, böylece tek bir trace
  tıklamadan veritabanına kadar uzanır. Başka origin'lere giden istekler için
  [tarayıcı kılavuzunda](/tr/integrations/browser#options) anlatılan `propagateTraceHeaders` gerekir.
- **Uygulamadan makineye.** Altyapı ajanı da çalışan bir makinede APM ajanı çalıştırmak, servisi makineye bağlar. Her ajan
  kılavuzunda "Host linking" bölümü var.

## Veri görünmüyor mu?

1. **Adresi kontrol et.** Ajanlar arayüzün olduğu `8080`'e değil, `4318` portuna gönderir (`http://<sunucu>:4318`).
2. **Anahtar türünü kontrol et.** `olk_…` anahtarı sunuculara, `olb_…` anahtarı sayfalara ve uygulamalara aittir. Bkz.
   [hangi anahtar nereye](./quickstart.md#hangi-anahtar-nereye).
3. **Tarayıcıda origin'i kontrol et.** Sayfanın origin'i anahtarın izin listesinde olmalı, yoksa istekler reddedilir.
4. **Mobilde uygulama kimliğini kontrol et.** İzin listesinde olmalı, yoksa istekler `403` ve `app_not_allowed` döner.
5. **Bir dakika bekle.** Makineler ve servisler yaklaşık bu kadar sürede görünür.
