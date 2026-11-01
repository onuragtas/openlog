# Runbook: Kubernetes'i openlog ile izlemek {#runbook-monitoring-kubernetes-with-openlog}

`deploy/helm/openlog-agent` chart'ı (Apache-2.0) openlog infra ajanını bir Kubernetes cluster'ında çalıştırır ve OTLP'yi bir
openlog ingest endpoint'ine (cluster içinde veya dışında) gönderir. Veri modeli: [semantic-conventions.md §7](/contracts/semantic-conventions);
kararlar D-070, D-071; API: [api.md](/contracts/api) "Kubernetes".

| İş yükü | Ne yapar | Host erişimi |
|---|---|---|
| `DaemonSet <release>-openlog-agent-node` (her node) | node'un host metrikleri, süreçleri, envanteri ve keşfi; her container'ın pod, workload ve label attribute'larıyla cgroup metrikleri; kubelet `/stats/summary` pod/container/node metrikleri; `/var/log/pods` üzerinden container stdout/stderr | host kök dizini salt okunur, `hostPID`, `hostNetwork`, yalnızca `DAC_READ_SEARCH` + `SYS_PTRACE` ile root |
| `Deployment <release>-openlog-agent-cluster` (1–2 replica, biri aktif) | kube-state metrikleri (node'lar, pod'lar, workload'lar, job'lar, HPA'lar, namespace'ler, kotalar), log olarak Kubernetes event'leri | yok (root değil, salt okunur kök dosya sistemi, tüm capability'ler kaldırılmış) |

## 1. Kurulum {#_1-install}

Önkoşullar: Kubernetes >= 1.27, Helm >= 3.14, bir openlog ingest lisans anahtarı ve ajan imajı
(`ghcr.io/onuragtas/openlog-infra-agent:<version>`; yayımlanmış bir imaj olana kadar `make -C agents/infra docker` ile derleyip
kendi registry'ne push et).

```bash
kubectl create namespace openlog-agent
# Node ajanı host erişimi ister: namespace "privileged" Pod Security Standard'ına izin vermeli.
kubectl label namespace openlog-agent pod-security.kubernetes.io/enforce=privileged
kubectl -n openlog-agent create secret generic openlog-license --from-literal=license-key='<LICENSE KEY>'

helm install openlog-agent deploy/helm/openlog-agent -n openlog-agent \
  --set clusterName=prod-eu-1 \
  --set endpoint=https://ingest.openlog.example:4318 \
  --set existingSecret.name=openlog-license
```

Depo checkout'u yerine yayımlanmış chart (chart `version` = `appVersion` = openlog sürümü; ayrıca her GitHub release'ine
`openlog-agent-<version>.tgz` olarak eklenir, sha256 imzalı manifest'tedir):

```bash
helm install openlog-agent oci://ghcr.io/onuragtas/charts/openlog-agent --version 0.4.0 -n openlog-agent \
  --set clusterName=prod-eu-1 \
  --set endpoint=https://ingest.openlog.example:4318 \
  --set existingSecret.name=openlog-license
```

Doğrula:

```bash
kubectl -n openlog-agent rollout status ds/openlog-agent-openlog-agent-node
kubectl -n openlog-agent logs ds/openlog-agent-openlog-agent-node | grep 'kubernetes node mode'
kubectl -n openlog-agent get lease openlog-agent-openlog-agent-cluster -o jsonpath='{.spec.holderIdentity}{"\n"}'
```

Yaklaşık bir dakika içinde cluster, node'ları (host sayfalarına bağlı), workload'ları ve pod'ları arayüzde **Kubernetes** altında
görünür; container sayfaları pod ve workload adlarını, pod sayfaları da container loglarını ve event'leri gösterir.

Önemli değerler (hepsi `values.yaml` içinde):

| Değer | Varsayılan | Notlar |
|---|---|---|
| `clusterName` | — (zorunlu) | `k8s.cluster.name`; cluster kimliği `kube-system` namespace uid'sidir (`k8s.cluster.uid`), bu yüzden yeniden adlandırınca geçmiş korunur |
| `endpoint`, `existingSecret.name` / `licenseKey` | — (zorunlu) | `licenseKey` değeri chart'ın yönettiği bir Secret'ta saklanır |
| `node.kubelet.insecureSkipVerify` | `true` | kubeadm/kind'da kubelet serving sertifikaları self-signed'dır; cluster CA'sı tarafından imzalanıyorsa (`serverTLSBootstrap: true`) `false` yap |
| `node.containerLogs.enabled` | `true` | `/var/log/pods/<ns>_<pod>_<uid>/<container>/<n>.log` dosyalarını izler (CRI formatı); `node.config.logs.containers.exclude` ile veya container runtime'da pod annotation'ı gerektirmeyen `openlog.logs=false` label'ıyla hariç tut |
| `node.tolerations` | `[{operator: Exists}]` | taint'li (control-plane) node'larda da çalışır |
| `node.stateHostPath` | `/var/lib/openlog-infra-agent` | disk buffer'ı ve log konumları pod yeniden başlatmalarından sonra korunur |
| `node.config`, `cluster.config` | `{}` | herhangi bir ajan yapılandırma anahtarı ([config.example.yaml](https://github.com/onuragtas/openlog/blob/master/agents/infra/packaging/config.example.yaml)), üretilen yapılandırmanın üzerine birleştirilir |
| `cluster.replicas` | `1` | `2`, ~15 sn'lik failover sağlar (Lease); yalnızca lease sahibi listeler ve gönderir |
| `cluster.interval` | `30s` | cluster metrik aralığı |
| `cluster.events` | `true` | log olarak Kubernetes event'leri (`event_name = k8s.event`) |

Yükseltme: aynı values ile `helm upgrade`. Container'lar kendilerini asla güncellemez (`update.enabled: false`), bunun yerine imaj
tag'ini değiştir. Kaldırma: `helm uninstall openlog-agent -n openlog-agent` (host path durum klasörü node'larda kalır).

## 2. RBAC {#_2-rbac}

Leader election Lease'i dışında her şey salt okunurdur. İki ServiceAccount var:

| ServiceAccount | ClusterRole kuralları | Neden |
|---|---|---|
| `<fullname>-node` | `pods` get/list/watch | kendi node'unun pod metadata'sı (`fieldSelector=spec.nodeName`) |
| | `nodes/stats` get | kubelet `/stats/summary` (kubelet SubjectAccessReview ile yetkilendirir) |
| | `namespaces` get, yalnızca `kube-system` | `k8s.cluster.uid` |
| `<fullname>-cluster` | `nodes`, `pods`, `namespaces`, `resourcequotas`, `events` get/list/watch | kube-state metrikleri, event'ler |
| | `apps` deployments/statefulsets/daemonsets/replicasets, `batch` jobs/cronjobs, `autoscaling` HPAs get/list/watch | workload'lar ve kesin sahipleri |
| | release namespace'inde Role: `leases` create; get/update yalnızca chart'ın Lease'i için | leader election |

Hiçbir kural Secret'lara, ConfigMap'lere, `nodes/proxy` (kubelet exec/logs) erişimi veya Lease dışında yazma yetkisi vermez. Cluster
collector'ın listeleyemediği türler (403) bir uyarıyla atlanır, yani daraltılmış bir ClusterRole da çalışır (node ve pod'lar zorunlu).

## 3. Güvenlik duruşu {#_3-security-posture}

- **Node ajanının yetkileri.** DaemonSet host kök dizinini salt okunur bağlar (`/host`, `mountPropagation: HostToContainer`)
  ve `hostPID` ile `hostNetwork` kullanarak uid 0 ile çalışır; `DAC_READ_SEARCH` (container logları ve diğer kullanıcıların
  `/proc/<pid>/*` dosyaları gibi yalnızca root'un okuyabildiği dosyaları okumak için) ve `SYS_PTRACE` (süreç başına I/O sayaçları ve executable bağlantıları) dışında tüm capability'ler kaldırılır,
  `allowPrivilegeEscalation: false`, salt okunur kök dosya sistemi ve `RuntimeDefault` seccomp profili kullanılır. **`privileged` değildir.**
  Host kök dizinine okuma erişimi, o node'daki pod'lara bağlı her Secret'ı okumakla eşdeğerdir
  (`/var/lib/kubelet/pods/*/volumes`), bu yüzden ajan namespace'ine `kube-system` gibi davran: DaemonSet'e kimin exec yapabileceğini veya
  onu kimin değiştirebileceğini kısıtla. `hostNetwork` ve `hostPID` kapatılabilir (`node.hostNetwork`, `node.hostPID`), ama
  bedeli yanlış host ağ sayaçları, dinleyen portlar ve süreç metrikleridir.
- **CRI socket'i.** Container runtime socket'ine host kök dizini bağlaması üzerinden erişilebilir (yalnızca CRI list/status çağrıları; ajan
  asla container oluşturmaz, içine exec yapmaz veya durdurmaz, ama socket'in kendisi buna izin verirdi; imajın scratch tabanlı olmasının ve
  shell çalıştırmamasının nedeni budur).
- **Kubelet TLS'i.** `node.kubelet.insecureSkipVerify: true` ile (varsayılan) ServiceAccount token'ı, kubelet sertifikası doğrulanmadan
  node'un kendi `status.hostIP:10250` adresine gönderilir. Token yalnızca yukarıdaki salt okunur kuralları verir.
  Mümkün olan yerde kubelet serving certificate bootstrap'ı aç ve bunu `false` yap.
- **Token'lar.** Projected ServiceAccount token'ı her dakika yeniden okunur (rotasyon). Lisans anahtarı bir Secret'tan
  ortam değişkeni olarak gelir.
- **Veri.** Pod label'ları yalnızca `kubernetes.label_allowlist` içinde listeliyse gönderilir (varsayılan: `app` ve
  önerilen `app.kubernetes.io/*` label'ları). Container logları ajanın secret maskelemesinden geçer (`logs.mask_secrets`).
  Event mesajları olduğu gibi gönderilir (16 KiB'de kesilir).
- **API sunucusu yükü.** Node ajanları yalnızca kendi node'larının pod'larını izler (her biri bir watch, 5 dakikada bir relist). Cluster
  collector her 30 sn'de bir `resourceVersion=0` ile listeler (API sunucusu watch cache'inden sunulur, etcd okuması yok) ve bir
  events watch tutar.

## 4. Sorun giderme {#_4-troubleshooting}

| Belirti | Kontrol et |
|---|---|
| Pod'lar `violates PodSecurity "baseline"` ile pending | namespace'i `pod-security.kubernetes.io/enforce=privileged` ile etiketle (yalnızca node ajanı için) |
| Node ajanı loglarında `kubelet metrics disabled` veya HTTP 401/403 | `nodes/stats` RBAC'i; kubelet `--authorization-mode=Webhook`; bazı yönetilen cluster'larda kubelet portu firewall arkasındadır: `node.config.kubernetes.kubelet.endpoint` ayarla |
| kubelet'ten `x509: certificate signed by unknown authority` | `node.kubelet.insecureSkipVerify=true` ayarla veya kubelet serving sertifikalarını bootstrap et |
| Container metriklerinde container adı/pod attribute'u yok | node ajanı logunda `pod list failed` (RBAC) veya CRI socket yolu `containers.cri_sockets` içinde değil (varsayılanlar: containerd, k3s containerd, CRI-O) |
| Cluster metriği yok | `kubectl get lease`: bir sahip olmalı; cluster pod loglarında `k8s.cluster.uid` (`namespaces/kube-system` get gerektirir) |
| Failover sonrası yinelenen event'ler | beklenen davranış: yeni leader son 5 dakika içinde güncellenen event'leri yeniden gönderir |

kind üzerinde yerel test (backend yok, veriyi bir OTLP yakalama sunucusu alır): [`agents/infra/test/k8s/run.sh`](https://github.com/onuragtas/openlog/blob/master/agents/infra/test/k8s/run.sh).
