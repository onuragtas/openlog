# Hızlı başlangıç

Bu kılavuz seni sıfırdan ekranda veri görmeye götürür: openlog çalışan bir sunucu, ona veri gönderen bir makine ve trace
gönderen bir uygulama. Her şeyin tek makinede Docker Compose ile çalıştığı `single` profilini kullanır. Kubernetes için
[Helm chart'ına](https://github.com/onuragtas/openlog/blob/master/deploy/helm/openlog/README.md) bak.

## 1. Sunucuyu kur

Docker Engine ve Compose v2 eklentisi olan bir Linux sunucu gerekir. Docker yoksa komuta `--install-docker` ekle.

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install-server.sh |
  sudo sh -s -- --email sen@example.com
```

Kurulum bitince arayüzün adresini, **lisans anahtarını** (`olk_…`) ve ajan kurma komutunu yazdırır. Lisans anahtarını sakla,
ajanlar veri göndermek için onu kullanır.

`http://<sunucu>:8080` adresini aç ve verdiğin e-postayla giriş yap. Parola belirlemediysen kurulum bir tane üretip bir kez
yazdırmıştır.

::: tip Gerisi isteğe bağlı
Kurulumun tüm seçenekleri, alan adı ve sürüm kanalı dahil, [README'de](https://github.com/onuragtas/openlog/blob/master/README.md) anlatılıyor.
:::

## 2. Portları aç

| Port | Ne işe yarar | Kimin erişmesi gerekir |
|---|---|---|
| `8080` | Arayüz ve API | Kullanıcıların |
| `4318` | OTLP/HTTP, ajanların veri gönderdiği yer | İzlenen makineler ve uygulamalar |
| `4317` | OTLP/gRPC | gRPC kullanan makineler ve uygulamalar |
| `9464` | Prometheus metrikleri ve sağlık kontrolleri | Yalnızca iç ağ |

Denemenin ötesinde bir kullanım için `8080` ve `4318`'in önüne Caddy, nginx veya Traefik gibi bir ters proxy koy,
`OPENLOG_COOKIE_SECURE=true` ayarla ve ajanlara `https://` adresleri ver.

## 3. Bir makineyi izle

İzlemek istediğin makinede şunu çalıştır:

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
  sudo sh -s -- --license-key <olk_ anahtarın> --endpoint http://<sunucu>:4318
```

openlog sunucusunun kendisinde adres olarak `http://127.0.0.1:4318` kullan.

Makine yaklaşık bir dakika içinde **Hosts** altında metrikleri, envanteri ve bulunan servisleriyle görünür.

## 4. Bir uygulamayı izle

Dilini [entegrasyonlardan](./send-data.md) seç. Örneğin Node.js ajanı kod değişikliği gerektirmez:

```sh
npm install openlog-node
```

Sonra uygulamayı ajanla başlatmak için [hızlı başlangıcını](/integrations/node#quick-start) izle.

Uygulaman zaten OpenTelemetry kullanıyorsa openlog ajanına gerek yok, sunucuyu göstermen yeterli:

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://<sunucu>:4318
export OTEL_EXPORTER_OTLP_HEADERS=openlog-license-key=<olk_ anahtarın>
```

Uygulaman trafik almaya başlayınca **APM** altında bir servis olarak görünür.

## Arayüze yaptır

Üst menüden **Add data** sayfasını aç veya `http://<sunucu>:8080/add-data` adresine git. Makineler, Docker, Kubernetes, tüm APM
ajanları, loglar ve OpenTelemetry için komutları sunucunun adresi dolu olarak hazırlar. Kurulum için lisans anahtarı
oluşturabilir ve veri gelene kadar bekler.

## Hangi anahtar nereye

İki tür anahtar var ve en sık yapılan hata bunları karıştırmak.

| | Lisans anahtarı `olk_…` | Tarayıcı veya mobil anahtar `olb_…` |
|---|---|---|
| **Kullanan** | Altyapı ajanı, APM ajanları, OpenTelemetry | Tarayıcı ve mobil SDK'lar |
| **Gizli mi?** | **Evet.** Asla web sayfasına veya uygulamaya koyma | Hayır. Sayfanda veya uygulamanda yer alır, yetkisi tasarım gereği sınırlıdır |
| **Nereden oluşturulur** | Add data veya Settings | Settings → Browser keys |

## Sonra

- [Bir web sitesini veya mobil uygulamayı izle](/integrations/browser)
- [DAU, retention ve funnel ölç](./product-analytics.md)
- [Crash'leri raporla](./crash-reporting.md)
- [Ürünün neler yaptığına ekran ekran bak](/features) (İngilizce)
- [openlog'u güncel tut](/operations/upgrading) (İngilizce)
