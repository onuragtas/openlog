# Runbook: k3d üzerinde openlog `cluster` profili {#runbook-openlog-cluster-profile-on-k3d}

Strimzi Kafka (KRaft, 3 broker), Altinity ClickHouse (2 shard x 1 replica, Keeper x3), her openlog servisinden birer replica ve ingest'in
önünde Envoy ile `cluster` profilini çalıştıran, 3 agent'lı yerel bir Kubernetes kümesi. Yük testleri (ingest/processor 1 -> 2 -> 4) ve chaos
testleri için kullan (bkz. [04-cluster.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/04-cluster.md)).

Chart: `values-dev.yaml` ile [`deploy/helm/openlog`](https://github.com/onuragtas/openlog/blob/master/deploy/helm/openlog).
k3d yerine kind kullanmak için: [kind-dev-cluster.md](/tr/operations/kind-dev-cluster) (1-4. bölümler farklı, gerisi ortak).
Ölçülen sonuçlar: [benchmarks/2026-09-13-kind.md](https://github.com/onuragtas/openlog/blob/master/docs/operations/benchmarks/2026-09-13-kind.md).

## 0. Ön koşullar {#_0-prerequisites}

- Kendisine en az **8 CPU / 12 GiB RAM** ayrılmış Docker (Kafka x3 + ClickHouse x2 + Keeper x3 + servisler).
- `k3d` >= 5.6, `kubectl`, `helm` >= 3.14, `jq`.
- Reponun bir checkout'u. Aşağıdaki komutlar repo kökünden çalışır.

## 1. Kümeyi oluştur {#_1-create-the-cluster}

```bash
k3d cluster create openlog \
  --agents 3 \
  --k3s-arg "--disable=traefik@server:0" \
  --k3s-node-label "topology.kubernetes.io/zone=zone-a@agent:0" \
  --k3s-node-label "topology.kubernetes.io/zone=zone-b@agent:1" \
  --k3s-node-label "topology.kubernetes.io/zone=zone-c@agent:2" \
  --wait

kubectl get nodes -o wide
```

Traefik kapalı: geliştirme kurulumu servislere port-forward ile ulaşır ve OTLP dengeleme için Envoy kullanır. k3s, `local-path` depolamayla
(`values-dev.yaml`'ın kullandığı varsayılan StorageClass) ve metrics-server ile (HPA'lar için gerekli) gelir.

İsteğe bağlı: üç agent'ın üç worker node gibi davranması için iş yüklerini server node'undan uzak tut:

```bash
kubectl taint node k3d-openlog-server-0 node-role.kubernetes.io/control-plane=:NoSchedule
```

## 2. Operator'ları kur {#_2-install-the-operators}

İki operator da chart'ın içinde gelmez.

```bash
# Strimzi cluster operator (KRaft; 1.2.0 ile test edildi - Strimzi >= 1.0, chart varsayılanı kafka.strimzi.apiVersion=kafka.strimzi.io/v1 gerektirir)
helm install strimzi oci://quay.io/strimzi-helm/strimzi-kafka-operator --version 1.2.0 \
  -n strimzi --create-namespace \
  --set watchAnyNamespace=true \
  --wait

# Altinity clickhouse-operator (0.27.3 ile test edildi; chart >= 0.27, watchNamespaces ayarlanmazsa yalnızca kendi namespace'ini izler)
helm repo add altinity https://docs.altinity.com/clickhouse-operator/
helm repo update
helm install clickhouse-operator altinity/altinity-clickhouse-operator --version 0.27.3 \
  -n clickhouse-operator --create-namespace --set "watchNamespaces={openlog}" --wait

kubectl get crd | grep -E 'strimzi|altinity'
```

Beklenen CRD'ler arasında `kafkas.kafka.strimzi.io`, `kafkanodepools.kafka.strimzi.io`,
`kafkatopics.kafka.strimzi.io`, `clickhouseinstallations.clickhouse.altinity.com` ve
`clickhousekeeperinstallations.clickhouse-keeper.altinity.com` var.

Helm yerine düz manifest tercih edersen:
`kubectl create -f 'https://strimzi.io/install/latest?namespace=strimzi' -n strimzi` (sonra `openlog` namespace'ini izlemesine izin ver,
bkz. Strimzi dokümanları) ve
`kubectl apply -f https://raw.githubusercontent.com/Altinity/clickhouse-operator/master/deploy/operator/clickhouse-operator-install-bundle.yaml`.

## 3. İmajı derle ve içe aktar {#_3-build-and-import-the-image}

```bash
docker build -t ghcr.io/onuragtas/openlog:dev .
k3d image import ghcr.io/onuragtas/openlog:dev -c openlog
```

`values-dev.yaml`, `pullPolicy: IfNotPresent` ile `tag: dev` kullanır, yani içe aktarılan imaj kullanılır. Yeniden derledikten sonra tekrar
içe aktar ve `kubectl -n openlog rollout restart deploy` çalıştır.

## 4. openlog'u kur {#_4-install-openlog}

```bash
helm install openlog deploy/helm/openlog \
  -n openlog --create-namespace \
  -f deploy/helm/openlog/values-dev.yaml \
  --timeout 30m
```

`--wait` **ekleme** (operators modunda migrate hook'u post-install çalışır, bkz. chart README'si). Başka bir terminalde:

```bash
kubectl -n openlog get kafka,kafkanodepool,kafkatopic,chk,chi
kubectl -n openlog get pods -o wide -w
```

Ayağa kalkma sırası: Keeper (3 pod `chk-openlog-keeper-0-*`), ClickHouse (`chi-openlog-openlog-0-0-0`,
`chi-openlog-openlog-1-0-0`), Kafka (`openlog-dual-0..2`) ve entity operator, KafkaTopic'ler `Ready`, ardından `openlog-migrate` Job'u tamamlanır
ve `helm install` döner. Genellikle 5-10 dakika sürer.

Doğrula:

```bash
kubectl -n openlog get kafkatopic -o custom-columns=NAME:.spec.topicName,PARTS:.spec.partitions,RF:.spec.replicas,READY:.status.conditions[0].type
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q "SHOW TABLES FROM openlog"
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q \
  "SELECT cluster, shard_num, replica_num, host_name FROM system.clusters WHERE cluster='openlog'"
kubectl -n openlog get deploy
```

Migrate Job'u başarısız olursa: `kubectl -n openlog logs job/openlog-migrate` (Job başarısızlıktan sonra durur), düzelt, sonra
`helm upgrade openlog deploy/helm/openlog -n openlog -f deploy/helm/openlog/values-dev.yaml --timeout 30m`.

## 5. Port-forward {#_5-port-forward}

```bash
kubectl -n openlog port-forward svc/openlog-envoy 4317:4317 4318:4318 &   # Envoy üzerinden OTLP
kubectl -n openlog port-forward svc/openlog-api 8080:8080 &
kubectl -n openlog port-forward deploy/openlog-kafka-exporter 9308:9404 &   # consumer lag (Strimzi exporter)

# values-dev.yaml, owner'ı admin@openlog.local / openlog-dev-password olan `dev` tenant'ını ve
# dev-license-key ingest key'ini hazırlar. Sorgu API'si ingest key'ini değil, bir oturumu veya
# salt okunur bir API key'ini (Settings → API keys) kabul eder:
curl -s -c /tmp/openlog.jar -H 'Content-Type: application/json' \
  -d '{"email":"admin@openlog.local","password":"openlog-dev-password"}' http://localhost:8080/api/v1/auth/login >/dev/null
curl -s -b /tmp/openlog.jar http://localhost:8080/api/v1/hosts | jq .
```
Arayüzü aynı hesapla kullanmak için <http://localhost:8080> adresini aç.

Strimzi 1.x, Kafka exporter için **Service oluşturmaz** ve metrikleri **9404** portunda sunar (`deploy/<kafka>-kafka-exporter`), port-forward'ın
Deployment'a yapılmasının nedeni budur. Exporter, henüz commit edilmiş offset'i olmayan partition'lar için `kafka_consumergroup_lag` = -1 bildirir.
Toplarken negatif değerleri yok say.

## 6. openlog-loadgen ile yük üret {#_6-generate-load-with-openlog-loadgen}

Port-forward darboğaz olmasın diye yük üreticiyi **kümenin içinde** çalıştır. `openlog-loadgen` aynı imajla gelir
(`/usr/local/bin/openlog-loadgen`). Host metrikleri, envanter snapshot'ları, loglar ve span'ler için OTLP/HTTP (protobuf) gönderir.

| Flag | Varsayılan | Anlamı |
|---|---|---|
| `-endpoint` | `http://localhost:4318` | openlog-ingest'in (veya Envoy'un) OTLP/HTTP temel URL'si |
| `-license-key` | `$OPENLOG_LICENSE_KEY` | zorunlu |
| `-hosts` | `10` | simüle edilen host sayısı |
| `-host-prefix` | `loadgen` | host adı öneki |
| `-interval` | `10s` | host metrikleri aralığı |
| `-logs-per-sec` / `-spans-per-sec` | `100` / `100` | tüm host'lar genelinde hızlar |
| `-inventory-interval` | `10m` | host başına envanter snapshot aralığı |
| `-duration` | `0` | bu süre sonunda dur (0 = kesilene kadar) |
| `-concurrency` | `8` | eşzamanlı HTTP isteği sayısı |
| `-gzip` | `true` | istek gövdelerini gzip'le |

```bash
kubectl -n openlog run loadgen --restart=Never --image=ghcr.io/onuragtas/openlog:dev \
  --image-pull-policy=IfNotPresent \
  --command -- /usr/local/bin/openlog-loadgen \
    -endpoint=http://openlog-envoy.openlog.svc:4318 \
    -license-key=dev-license-key \
    -hosts=200 -logs-per-sec=20000 -spans-per-sec=20000 \
    -concurrency=32 -duration=10m

kubectl -n openlog logs -f loadgen
```

Loadgen periyodik olarak bir durum satırı yazdırır:
`requests=… errors=… | datapoints=… logs=… spans=… inventory_records=…`. datapoints, logs ve spans sayaçları yalnızca ingest'in HTTP 200 ile
cevapladığı istekleri sayar, bu yüzden son satır aşağıdaki karşılaştırmalar için **accepted** sayılarını verir. `errors`, reddedilen veya
başarısız olan istekleri sayar (örneğin Kafka arızası sırasında 503). Loadgen bunları yeniden denemez, yani "accepted" kapsamına girmezler. Pod sen
logları okumadan biterse `kubectl -n openlog logs loadgen | tail -1` son satırı yine gösterir. Başka bir çalıştırmadan önce
`kubectl -n openlog delete pod loadgen` ile sil.

## 7. Ölçekle ve throughput'u izle {#_7-scale-and-observe-throughput}

Ölçeklerken şunları izle (Prometheus veya admin portlarında düz `curl`):

```bash
# topic başına consumer lag (toplam)
curl -s localhost:9308/metrics | grep '^kafka_consumergroup_lag{' | grep openlog-processor \
  | awk '{split($0,a,"topic=\""); split(a[2],b,"\""); s[b[1]]+=$NF} END {for (t in s) print t, s[t]}'

# saniyede saklanan satır (10 sn arayla iki kez çalıştır)
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q \
  "SELECT 'logs', count() FROM openlog.logs UNION ALL SELECT 'spans', count() FROM openlog.spans UNION ALL SELECT 'metrics', count() FROM openlog.metrics"

# pod başına CPU
kubectl -n openlog top pods
```

Adımlar (her birini ~3 dakika tut, 1 processor ile lag büyüyene kadar loadgen hızını artır):

```bash
kubectl -n openlog scale deploy/openlog-processor --replicas=2
kubectl -n openlog scale deploy/openlog-processor --replicas=4
kubectl -n openlog scale deploy/openlog-ingest --replicas=2
kubectl -n openlog scale deploy/openlog-ingest --replicas=4

# consumer group ataması (üye başına partition)
kubectl -n openlog exec openlog-dual-0 -c kafka -- /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server localhost:9092 --describe --group openlog-processor
```

Beklenen:

- Processor: lag büyümeyi bırakır ve ClickHouse satır/sn değeri, ClickHouse veya partition sayısı (dev'de 12) sınır olana kadar kabaca doğrusal
  artar. 12'den fazla processor replica'sı boşta kalır.
- Ingest: Envoy ile yeni ingest pod'ları, loadgen keep-alive bağlantılarını yeniden kullansa da ~5 sn içinde (DNS yenilemesi) istek alır. Karşılaştırmak
  için loadgen'i `http://openlog-ingest.openlog.svc:4318` adresine yönelt: kube-proxy bağlantıları dengeler, bu yüzden yeni pod'lar loadgen'in mevcut
  bağlantılarından çok az şey alır veya hiç almaz. Uzun ömürlü OTLP/gRPC istemcileri (SDK'lar, altyapı ajanı) bundan daha da çok etkilenir, Envoy'un
  orada olmasının nedeni de budur.
- Chart HPA'ları dev'de kapalıdır, `kubectl scale` yeterlidir. HPA'yı test etmek için:
  `helm upgrade ... --set processor.autoscaling.enabled=true --set processor.autoscaling.maxReplicas=12`.

## 8. Chaos testleri {#_8-chaos-tests}

Her testi sabit yük altında çalıştır. Her birinden önce `produced` (loadgen sayaçları) ve `stored` (ClickHouse sayıları) değerlerini not et. Loadgen
bittikten sonra consumer lag 0'a inene kadar bekle, sonra karşılaştır.

```bash
cat > /tmp/openlog-counts.sql <<'EOF'
SELECT 'logs'  AS t, count() AS rows FROM openlog.logs
UNION ALL SELECT 'spans', count() FROM openlog.spans
UNION ALL SELECT 'spans_unique', uniqExact(trace_id, span_id) FROM openlog.spans
UNION ALL SELECT 'metrics', count() FROM openlog.metrics
EOF
kubectl -n openlog exec -i chi-openlog-openlog-0-0-0 -- clickhouse-client --multiquery < /tmp/openlog-counts.sql
```

Temiz bir çalıştırma için boş tablolardan başla:
`kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q "TRUNCATE TABLE openlog.logs_local ON CLUSTER openlog"`
(`spans_local`, `metrics_local` ve kontrol ediyorsan rollup/index tabloları için tekrarla).

Kabul kriteri (at-least-once): her sinyal için `stored >= accepted`, yani ingest'in onayladığı hiçbir şeyin **kaybolmaması**. Rebalance'lardan sonra küçük
çiftlemelere (`spans - spans_unique > 0`) izin var
([04-cluster.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/04-cluster.md), açık madde 2). Arıza sırasında 503 (+ `Retry-After`) ile
reddedilen istekler "accepted" değildir ve istemci tarafından yeniden denenmiş olmaları gerekir.

### 8.1 Bir Kafka broker'ını öldür {#_8-1-kill-a-kafka-broker}

```bash
kubectl -n openlog delete pod openlog-dual-1
kubectl -n openlog get pods -w -l strimzi.io/cluster=openlog
```

RF=3 ve `min.insync.replicas=2` ile producer'lar (`acks=all`) 3 replica'nın 2'siyle başarılı olmaya devam eder, partition liderliği saniyeler içinde
taşınır. Ingest'te kısa bir gecikme sıçraması ve muhtemelen bir consumer group rebalance'ı bekle. Pod geri geldikten sonra under-replicated partition'ların
0'a döndüğünü kontrol et:

```bash
kubectl -n openlog exec openlog-dual-0 -c kafka -- /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 --describe --under-replicated-partitions
```

Daha sert çeşidi: iki broker'ı sil. Produce'lar tek replica ile onaylanmak yerine başarısız olmalıdır (ingest'ten `503`/`UNAVAILABLE`).

### 8.2 Bir ClickHouse pod'unu öldür {#_8-2-kill-a-clickhouse-pod}

```bash
kubectl -n openlog delete pod chi-openlog-openlog-1-0-0
```

Dev yerleşimi 2 shard x **1** replica'dır, bu yüzden pod yeniden başlayana kadar shard 1 kullanılamaz. Processor'ın Distributed tablolara insert'leri
(`distributed_foreground_insert=1`) başarısız olur, offset'ler commit edilmez, lag büyür ve pod geri geldikten sonra processor aynı batch'leri aynı
`insert_deduplication_token` ile yeniden dener: kayıp yok, yeniden denemelerden çiftleme yok. Bu sırada o shard'a dokunan API sorguları başarısız olur.
Replica durumunu (okuma ve yazma devam eder) test etmek için `--set clickhouse.altinity.replicas=2` ile upgrade et (~2 GiB daha RAM gerekir) ve bir
replica'yı sil.

### 8.3 Bir processor pod'unu öldür {#_8-3-kill-a-processor-pod}

```bash
kubectl -n openlog scale deploy/openlog-processor --replicas=3
POD=$(kubectl -n openlog get pod -l app.kubernetes.io/component=processor -o name | head -1)
kubectl -n openlog delete "$POD"                             # düzgün kapanma: flush + commit
# daha sert: düzgün flush yok
POD=$(kubectl -n openlog get pod -l app.kubernetes.io/component=processor -o name | head -1)
kubectl -n openlog delete "$POD" --grace-period=0 --force
```

Düzgün silme: pod `terminationGracePeriodSeconds` içinde flush ve commit yapar. Zorla silme: commit edilmemiş kayıtları rebalance'tan sonra başka bir üye
yeniden tüketir. Batch sınırları farklıysa satırlar çiftlenebilir (`spans - spans_unique` olarak görünür), asla kaybolmaz.

### 8.4 Bir ingest pod'unu / Envoy'u öldür {#_8-4-kill-an-ingest-pod-envoy}

```bash
kubectl -n openlog delete pod -l app.kubernetes.io/component=ingest
```

O pod üzerindeki devam eden istekler başarısız olur ve istemci yeniden dener, onaylanmış veri zaten Kafka'dadır. Envoy health check'leri (`/readyz`) ve
outlier detection endpoint'i çıkarır.

## 9. Kaldırma {#_9-teardown}

```bash
helm uninstall openlog -n openlog
kubectl -n openlog delete secret openlog-credentials   # resource-policy tarafından tutulur
kubectl delete ns openlog
k3d cluster delete openlog
```
