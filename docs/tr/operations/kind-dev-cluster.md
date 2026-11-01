# Runbook: kind üzerinde openlog cluster profili {#runbook-openlog-cluster-profile-on-kind}

[k3d runbook'uyla](/tr/operations/k3d-dev-cluster) aynı topoloji (Strimzi Kafka KRaft x3, Altinity ClickHouse
2 shard x 1 replica + Keeper x3, her openlog servisinden bir replica, ingest önünde Envoy), bu kez
1 control-plane + 3 worker ile [kind](https://kind.sigs.k8s.io/) üzerinde. k3d runbook'unun 5-9. bölümleri
(port-forward, loadgen, ölçekleme, chaos, teardown) aşağıdaki notlar dışında aynen geçerlidir.
İlk çalıştırmanın sonuçları: [benchmarks/2026-09-13-kind.md](https://github.com/onuragtas/openlog/blob/master/docs/operations/benchmarks/2026-09-13-kind.md).

Test edilen sürümler (2026-09-13): kind v0.33.0 (node imajı `kindest/node:v1.37.0`), kubectl v1.37.0,
Helm v4.3.0, Strimzi operator chart 1.2.0 (Kafka 4.3.1), Altinity clickhouse-operator chart 0.27.3,
metrics-server chart 3.14.0, ClickHouse/Keeper 25.8, Envoy v1.35.3, OrbStack üzerinde (10 CPU, 16 GiB).

## 0. Önkoşullar {#_0-prerequisites}

- >= 8 CPU / 12 GiB RAM ile Docker (stack boşta ~5 GiB, yük altında ~7 GiB kullanır).
- `kind` >= 0.33, node imajıyla arasında en fazla bir minor sürüm fark olan `kubectl` (burada v1.37), `helm` >= 3.14, `jq`.

## 1. Cluster'ı oluştur {#_1-create-the-cluster}

```bash
kind create cluster --config deploy/kind/cluster.yaml --wait 5m
kubectl config use-context kind-openlog-dev
kubectl get nodes -L topology.kubernetes.io/zone
```

[`deploy/kind/cluster.yaml`](https://github.com/onuragtas/openlog/blob/master/deploy/kind/cluster.yaml) her worker'a bir zone etiketi verir. k3d'den
farkları:

| | k3d | kind |
|---|---|---|
| Varsayılan StorageClass | `local-path` | `standard` (aynı rancher local-path provisioner) |
| metrics-server | yerleşik | **kurulu değil** - `kubectl top` ve HPA'lar için gerekir |
| İmaj aktarma | `k3d image import` | `kind load docker-image` |
| Control-plane node'u | yalnızca sen taint edersen taint'li | normal pod'lar için `NoSchedule` taint'li |

`values-dev.yaml` cluster'ın varsayılan StorageClass'ını kullanır (`storageClass: ""`), bu yüzden ikisinde de çalışır.

metrics-server (kind'daki kubelet'ler self-signed sertifika kullanır):

```bash
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
helm install metrics-server metrics-server/metrics-server --version 3.14.0 -n kube-system \
  --set 'args={--kubelet-insecure-tls}' --wait
```

## 2. Operatörleri kur {#_2-install-the-operators}

```bash
# Strimzi >= 1.0 yalnızca kafka.strimzi.io/v1 sunar (chart varsayılanı). 0.46-0.48 için şunu ayarla:
# kafka.strimzi.apiVersion=kafka.strimzi.io/v1beta2.
helm install strimzi oci://quay.io/strimzi-helm/strimzi-kafka-operator --version 1.2.0 \
  -n strimzi --create-namespace --set watchAnyNamespace=true --wait

helm repo add altinity https://docs.altinity.com/clickhouse-operator/
# Chart >= 0.27, watchNamespaces ayarlanmazsa YALNIZCA kendi namespace'ini izler.
helm install clickhouse-operator altinity/altinity-clickhouse-operator --version 0.27.3 \
  -n clickhouse-operator --create-namespace --set 'watchNamespaces={openlog}' --wait
```

`watchNamespaces` eksikse belirtisi şudur: `ClickHouseInstallation` / `ClickHouseKeeperInstallation` kaynakları
`STATUS` olmadan kalır, `chi-*`/`chk-*` pod'ları oluşmaz ve migrate Job'ı sonsuza kadar yeniden dener.

## 3. İmajı derle ve yükle {#_3-build-and-load-the-image}

```bash
docker build -t ghcr.io/onuragtas/openlog:dev .
kind load docker-image ghcr.io/onuragtas/openlog:dev --name openlog-dev
# isteğe bağlı, node'ların içinde pull yapılmasını önler:
kind load docker-image clickhouse/clickhouse-server:25.8 envoyproxy/envoy:v1.35.3 --name openlog-dev
```

Yeniden derlemeden sonra: tekrar yükle, ardından `kubectl -n openlog rollout restart deploy`.

## 4. openlog'u kur {#_4-install-openlog}

```bash
helm install openlog deploy/helm/openlog -n openlog --create-namespace \
  -f deploy/helm/openlog/values-dev.yaml --timeout 30m
```

`--wait` yok (post-install migrate hook'u, chart README'sine bak). Bu dizüstü bilgisayarda tüm stack
(Keeper, ClickHouse, Kafka, topic'ler, migration'lar) `helm install`'dan ~3 dakika sonra Ready oldu.

Doğrula (k3d runbook'undaki kontrollere ek olarak):

```bash
# her shard'da şema (her birinde 20 nesne) ve ON CLUSTER DDL her yerde tamamlandı
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q \
  "SELECT hostName(), count() FROM clusterAllReplicas('openlog', system.tables) WHERE database='openlog' GROUP BY 1"
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q \
  "SELECT hostName(), count(), countIf(status='Finished') FROM clusterAllReplicas('openlog', system.distributed_ddl_queue) GROUP BY 1"
# her servisin hazır olma durumu
for c in ingest processor api; do
  kubectl -n openlog exec deploy/openlog-$c -- wget -qO- http://127.0.0.1:9464/readyz; echo
done
```

## 5-9. Yük, ölçekleme, chaos, teardown {#_5-9-load-scaling-chaos-teardown}

[k3d runbook'unu](/tr/operations/k3d-dev-cluster#_5-port-forward) izle. kind'a özgü notlar:

- Consumer lag: Strimzi 1.x, Kafka exporter için Service oluşturmaz; exporter 9404'ü dinler
  (`kubectl -n openlog port-forward deploy/openlog-kafka-exporter 9308:9404`, sonra
  `curl -s localhost:9308/metrics | grep kafka_consumergroup_lag`; -1 = henüz commit edilmiş offset yok).
  Bir broker pod'unun içinde `kafka-consumer-groups.sh` komutunu döngüyle **çalıştırma**: her çalıştırma broker'ın bellek
  cgroup'unda bir JVM başlatır ve broker'ın kendisini OOM-kill ettirebilir. Tek seferlik çalıştırmak sorun değil.
- loadgen'i Job olarak control-plane node'unda çalıştır (taint'i için toleration +
  `nodeSelector: {kubernetes.io/hostname: openlog-dev-control-plane}`), böylece worker'ların pod'larıyla zamanlama için
  yarışmaz. Yine de tüm kind node'ları aynı Docker VM CPU'larını paylaşır.
- `values-dev.yaml` içinde pod'lar limitsiz çalışır; 10 CPU'lu bir dizüstünde limitsiz tek bir ingest veya
  processor pod'u doyurulamaz. Ölçekleme ölçümleri için onlara sabit bir CPU boyutu ver, örn.
  `kubectl -n openlog set resources deploy/openlog-ingest deploy/openlog-processor --requests=cpu=300m --limits=cpu=300m`.
- Tüm kind node'larının `/var` dizini aynı Docker VM diskidir. Yük yüksekse test için topic retention'ını
  düşür (`kubectl -n openlog patch kafkatopic openlog-otlp-logs-v1 --type merge -p '{"spec":{"config":{"retention.ms":1800000,"segment.bytes":104857600}}}'`, metrics/traces için de aynısı).

### Bu kurulumda görülen tuzaklar {#pitfalls-seen-on-this-setup}

- **Helm 4 + `kubectl scale`:** Helm 4 manifest'leri server-side uygular. `kubectl scale` sonrasında bir sonraki
  `helm upgrade`, `conflict with "kubectl" with subresource "scale" ... .spec.replicas` hatasıyla başarısız olur
  (ve bazı nesneleri güncellenmiş bir `failed` revision bırakır). Replica sayısını values üzerinden ayarla
  veya upgrade'i `--force-conflicts` ile yeniden çalıştır.
- **ClickHouse belleği:** ClickHouse kendini container limitinin %90'ıyla sınırlar ve bu sınırı, mark cache'i (varsayılan 5 GiB)
  de içeren RSS'e göre uygular. 2Gi limit ve saklanan birkaç on milyon satırla RSS sınırda kaldı ve her insert
  `MEMORY_LIMIT_EXCEEDED` ile başarısız oldu. Bu yüzden `values-dev.yaml`, 3Gi ve `mark_cache_size: 268435456` kullanır.
- **Backlog'da processor belleği:** büyük bir consumer lag ile başlayan processor büyük fetch'leri aynı anda çözer;
  50k satırlık log/span batch'leri 1 GiB civarında tepe yapar. `values-dev.yaml` ona 2Gi limit verir.
- **Retention ve disk:** topic retention'ını oluşturabileceğin en uzun backlog'un altına düşürme.
  Processor'lar `retention.ms` değerinden uzun süre takılırsa (örn. ClickHouse kapalıyken), tüketilmemiş kayıtlar
  silinir ve consumer sessizce en eski offset'e döner. `kafka-consumer-groups.sh --describe` (mevcut offset) ile
  `kafka-get-offsets.sh --time -2` (en eski) çıktısını karşılaştırarak kontrol et.
- **Envoy ve pod değişimi:** Envoy ingest pod'larını headless Service üzerinden bulur (`STRICT_DNS`) ve kind'ın
  CoreDNS'i yanıtları 30 sn önbellekler, bu yüzden Envoy silinmiş bir ingest pod'una ~30 sn'ye kadar bağlanmaya devam
  edebilir. Chart bunu 15 sn'lik bir ingest `preStop` sleep'i, bağlantı hatalarında `retry_budget` ile
  route yeniden denemeleri (`connect-failure`, `refused-stream`, `reset-before-request`) ve 2 bağlantı hatasından sonra
  local-origin outlier ejection ile çözer. Bu düzeltmelerden önce, yük altında bir ingest pod'unu silmek ~250, bir ingest
  rollout'u ~380 istemci 503'üne mal oluyordu.
- **`helm uninstall` sonrası yeniden kurulum:** yeniden kurmadan önce `kubectl -n openlog get kafkatopic` boşalana kadar
  bekle; yoksa kurulum topic'siz bitebilir (her OTLP isteği 503 alır). Bu durumda topic'leri yeniden oluşturmak için aynı
  values ile `helm upgrade` çalıştır.
- **Keeper yerleşimi:** chart artık Keeper pod'larını node'lara yayıyor (soft anti-affinity), ama
  local-path volume'ler bir pod'u PV'sinin ilk oluşturulduğu node'a bağlar. Mevcut kurulumlar yerleşimlerini korur;
  taşımak için Keeper PVC'lerini yeniden oluştur.

Teardown:

```bash
helm uninstall openlog -n openlog
kubectl -n openlog delete pvc --all
kubectl -n openlog delete secret openlog-credentials
kind delete cluster --name openlog-dev
```

## Doğrulama çalıştırması: operators modu + CloudNativePG + TLS (2026-09-14) {#validation-run-operators-mode-cloudnativepg-tls-2026-09-14}

Script: [`deploy/helm/test/kind-validate.sh`](https://github.com/onuragtas/openlog/blob/master/deploy/helm/test/kind-validate.sh) (`STEPS=base,failover,tls`,
`KEEP=1` cluster'ı korur). Aşağıdaki adımları içerir; bu adımlar elle şu ortamda çalıştırıldı: kind v0.33.0 (`kindest/node:v1.37.0`,
1 control-plane + 2 worker, cluster `openlog-r-kind`), Helm v4.3.0, Strimzi chart 1.2.0, Altinity operator chart
0.27.3, CloudNativePG chart 0.26.1 (operator 1.27.1), cert-manager v1.21.2, kaynak ağacından derlenip `kind load` ile yüklenen
openlog imajı (`pullPolicy: Never`). Küçük boyutlandırma: Kafka 1 dual node (RF 1, 3 partition), ClickHouse
1 shard x 2 replica, Keeper x3, CNPG 2 instance, servis başına bir replica, Envoy yok.

| Adım | Sonuç |
|---|---|
| Kurulum (`--wait` olmadan) | Keeper, ClickHouse, Kafka, CNPG, migration'lar ve bootstrap `helm install`'dan ~3 dk sonra tamamlandı; iki replica'da da 87 şema nesnesi; `Dev` organizasyonu + sahibi oluşturuldu |
| loadgen (2 dk, 3 host, 50 log/sn + 50 span/sn) | 0 istek hatası; ClickHouse'ta 5950 log / 5950 span / 1520 datapoint; `/api/v1/hosts`, `/api/v1/logs`, `POST /api/v1/query` (session login) veriyle yanıt veriyor |
| CNPG failover (primary'de `kubectl delete pod`) | API 200 yanıtlamayı sürdürdü. Eski primary ~3 dk 12 sn `Terminating` kaldı (smart shutdown, openlog pod'larının havuzlanmış bağlantılarını bekler, CNPG varsayılanı `smartShutdownTimeout` 180 sn), ardından promotion, `/auth/me` üzerinde tek bir 503 ile ~1 sn sürdü. Chart artık `postgres.operator.smartShutdownTimeout: 15` ayarlıyor (yeniden ölçülmedi) |
| ClickHouse sunucu TLS'i, A adımı (`clickhouse.tls.server.secretName`, `verificationMode: strict`) | 9440/8443 dinliyor, `remote_servers` 9440'ta `<secure>1</secure>` ile, rolling restart ~2,5 dk; istemci sertifikasıyla `clickhouse-client --secure` çalışıyor, sertifikasız handshake sıfırlanıyor |
| B adımı (`clickhouse.tls.enabled`, `clientSecret`) | openlog, istemci sertifikasıyla `clickhouse-openlog:9440` üzerinde; loadgen yine 0 hata, satırlar iki katına çıktı; TLS uyarısı yok |
| Keeper + replikasyon TLS'i (D-093, sunucu TLS'iyle varsayılan olarak açık) | ClickHouse, `secure://keeper-openlog.openlog.svc:9281` adresine bağlanıyor; Raft "SSL enabled" (leader + 2 eşitlenmiş follower); 9009 gitti, 9010 dinliyor; replica'lar Keeper'a `scheme: https` / port 9010 ile kaydoluyor ve part'ları indiriyor (hatasız `DownloadPart`), iki replica'da da 17700 log, replikasyon kuyruğu hataları boş |
| Cluster içi MinIO ile tiered storage | **chart hatası bulundu** (aşağıda); uçtan uca çalıştırma tamamlanmadı: Docker VM diski (diğer iş yükleriyle ortak) 2,8 GB boş alana düştü, bu yüzden cluster silindi |
| Tail sampling, ajan chart'ı | çalıştırılmadı (aynı neden); yalnızca `tailSampling.enabled` ile chart render'ı kontrol edildi |

Bu stack'in kaynakları: node container'ları ~6 GiB RSS (worker 3,3 GiB, worker2 1,7 GiB, control-plane 0,9 GiB) ve
~12 GB disk (containerd imajları + local-path volume'ler). Docker VM'in geri kalanı meşgulken control plane'in
controller-manager ve scheduler bileşenleri lease'lerini kaybetti ve birkaç kez yeniden başladı.

Bu çalıştırmada bulunan tuzaklar:

- **Mevcut bir release'te ClickHouse TLS'ini açmak iki upgrade gerektirir.** Migrate Job'ı bir pre-upgrade hook'udur ve CHI değişikliği
  uygulanmadan önce çalışır; aynı upgrade'de `tls.enabled` olursa ClickHouse sunmadan önce 9440'a bağlanmaya çalışırdı.
  A ve B adımları arasında eski düz metin processor/api pod'ları `system.clusters` üzerinden 9440 portunu okur ve doğrudan
  shard bağlantıları handshake'te başarısız olur (`apm edge linking failed ... handshake`); ingest Kafka'ya yazmaya devam eder, bu yüzden
  B adımını CHI `Completed` gösterir göstermez çalıştır.
- **Çalışan bir ensemble'da Keeper TLS'i** Raft'ı karışık bırakır (TLS ve düz metin peer'lar): keeper-0 liveness probe'unu
  3 kez geçemedi ve operatörün StatefulSet beklemesi, diğer ikisini rollout etmeden önce zaman aşımına uğradı (5 dk); ~6 dk quorum yok
  (Replicated insert'ler ve `ON CLUSTER` DDL durur). Bir bakım penceresinde veya kurulum sırasında aç.
- **Mevcut bir release'te tiered storage'ı açmak** kilitlendi: pre-upgrade migrate hook'u deadline'ına kadar
  `storage policy "openlog_tiered" is not usable` ile başarısız oldu, bu yüzden CHI policy'yi hiç almadı. Chart'ta düzeltildi:
  canlı CHI'da policy dosyası yokken migrate tiering'i kapalı tutar; CHI rollout'undan sonra `helm upgrade` komutunu ikinci kez
  çalıştır (docs/operations/tiered-storage.md).
- **`verificationMode: strict` ile `disableInsecure`** denenmedi: operator 0.27.3 ClickHouse'a HTTPS üzerinden ulaşabilir
  (`configs.files.config.yaml.clickhouse.access.scheme/port/rootCASecretRef`) ama strict modun 8443'te istediği istemci sertifikasını
  sunamaz. Düz metin Keeper portu 2181 de kalır (operatör liveness probe'u `ruok`).
- **Altinity operator chart'ını yükseltmek**, `bitnami/kubectl:latest` ile bir CRD pre-upgrade Job'ı çalıştırır; ilk pull'u
  ~2 dk sürdü ve varsayılan `--timeout 5m` doldu. `--timeout 10m` kullan.
- Operatörün host başına ürettiği ClickHouse/Keeper Service'leri headless'tır, bu yüzden ek TLS portları (9281, 9010)
  Service değişikliği gerektirmez; yalnızca CHI'nın ClusterIP Service'i 9440/8443'ü listeler.
