# Sözleşme: Tüm host CPU profiling'i (eBPF, v1) {#contract-whole-host-cpu-profiling-ebpf-v1}

Kararlar: D-148 (D-149 ile geçersiz kılındı), D-149. Bileşen: `agents/ebpf`. İlgili: [profiles.md](/tr/contracts/profiles) (bunun ürettiği profiles sinyali),
[semantic-conventions.md](/tr/contracts/semantic-conventions) §2 (süreç attribute'ları).

**Bu ayrı bir bileşendir, altyapı ajanının bir modu değil.** Bilerek kurulur, altyapı ajanının bilerek sahip olmadığı yetkilerle çalışır
ve onu hiç kurmayan bir operatör, openlog'un bugün belgelediği güvenlik duruşunu aynen korur.

## 1. Ne yapar, ne yapmaz {#_1-what-it-does-and-what-it-does-not}

**Tüm makinenin** CPU'sunu örnekler (instrument edilmiş olsun olmasın her süreç) ve sonucu, dil ajanlarının zaten kullandığı endpoint'e OTLP
profile olarak gönderir. SDK'lı bir Go servisi kendini profile eder; bir PHP-FPM pool'u, bir veritabanı, bir cron job ve bir kernel thread'i etmez,
flame graph'a böyle kavuşurlar.

eBPF auto-instrumentation **değildir**. Span üretmez, servis kenarı oluşturmaz ve istek payload'larını okumaz. `docs/features.md` hâlâ
auto-instrumentation'ı yapılmamış olarak listeler ve bu sözleşme bunu değiştirmez.

Burada bilerek olmayan diğer şeyler: off-CPU profiling, bellek ve allocation profiling ve her türlü istek başına tracing.
Her biri kendi tasarımını ister ve bunu kurmak hiçbirini iddia etmez.

## 2. Neden ayrı bir bileşen {#_2-why-it-is-a-separate-component}

Altyapı ajanının Linux sandbox'ı bu işi yasaklar ve sandbox bir kaza değil bir sözdür:

- `packaging/systemd/openlog-infra-agent.service` `User=openlog-agent` olarak, başka hiçbir şey olmadan
  `AmbientCapabilities=CAP_DAC_READ_SEARCH CAP_SYS_PTRACE` ile, ayrıca
  `SystemCallFilter=~@privileged @resources @mount @reboot @swap @raw-io @module @debug @cpu-emulation`,
  `MemoryDenyWriteExecute=yes` ve `ProtectKernelTunables/Modules/Logs=yes` ile çalışır. `bpf(2)` ve
  `perf_event_open(2)` bu listeyle filtrelenir.
- `docs/operations/kubernetes.md`, DaemonSet duruşunu garanti olarak belirtir: "**It is not `privileged`**",
  `bpf(2)`'yi engelleyen `RuntimeDefault` seccomp ile ve ikisi dışında tüm capability'ler atılmış olarak.

İkisinden birini gevşetmek, profiling'i hiç istemeyenler dahil her kurulumun duruşunu zayıflatırdı. Bu yüzden yetkiler kendi bileşeninde durur ve
altyapı ajanına dokunulmaz.

**Varsayılan olarak kurulur** (D-149), başlangıçtaki halinden bir değişiklik: bileşen, istenecek bir şey değil standart donanımdır, çünkü kimsenin
profile etmediği bir host, yavaş sürecinin açıklaması olmayan bir host'tur. Değişmeyen şey ayrı olmasıdır: kendi paketi, kendi hesabı ve kendi unit'i,
yani `--no-ebpf-profiler` makineyi tam olduğu gibi bırakır ve capability'ler ajana sessizce miras kalmak yerine kurulum sırasında açıkça söylenir.

## 3. Yetkiler ve kernel gereksinimleri {#_3-privileges-and-kernel-requirements}

| | |
|---|---|
| Capability'ler | `CAP_BPF` ve `CAP_PERFMON` (kernel ≥ 5.8). Bunların olmadığı yerde `CAP_SYS_ADMIN` kabul edilir, varken istenmez |
| `perf_event_paranoid` | `CAP_PERFMON` ile ≤ 2 yeterli; bileşen değeri okur ve anlaşılmaz şekilde başarısız olmak yerine hangi ayarın engellediğini söyler |
| Kernel | ≥ 5.8. BTF ve kernel header'ları gerekmez: program C'den derlenmez, çalışma anında instruction'lardan oluşturulur, yani karşılanacak CO-RE relocation yok |
| Mount'lar | `/sys/fs/bpf` **gerekmez**, pinning yok. Süreç kimliği için `/proc` okunur |

**Clang yok, cgo yok.** BPF programı `cilium/ebpf/asm` ile derlenir (saf Go; tek bağımlılığı altyapı ajanının zaten taşıdığı
`golang.org/x/sys`). `bpf2go`, birkaç düzine instruction'lık bir program üretmek için `CGO_ENABLED=0` ile altı os/arch hedefi gönderen bir repository'nin
build'ine clang sokardı. Bunun yerine instruction'lar açıkça yazılır ve bu tercihin bedeli programın küçük kalmak zorunda olmasıdır; onu küçük tutmanın
dürüstçe savunulabilmesinin sebebi de budur.

Bir gereksinim eksikse bileşen neyin eksik olduğunu loglar ve başlangıçta çıkar. Sağlıklı görünürken hiçbir şey örneklemeyen bir hale asla düşmez.

**Exit 78 (`EX_CONFIG`), bir operatörün müdahale etmesi gerektiği anlamına gelir:** yanlış yapılandırma veya örneklenmeyi reddeden bir kernel. Hata yalnızca sysctl'i
değil engelleyen ayarı belirtir: `/proc/sys/kernel/perf_event_paranoid` değerini okur, sebebin bu değer olup olmadığını ve değilse başka nereye bakılacağını
söyler (unit'in capability'leri veya capability'ler ne derse desin perf event'lerinin kullanılamadığı bir guest). Unit `RestartPreventExitStatus=78` ayarlar,
yani systemd her on saniyede yeniden başlatmak yerine durur: yeniden denemek kernel politikasını değiştiremez ve bir host'ta `perf_event_paranoid=4` karşısında
704'lük restart sayacı görüldü. Başlangıç sınırı (5 dakikada 5 başlatma), 78 bildirmeyen hatalar için güvencedir. Ayarı değiştirdikten sonra servisi yeniden başlat.

## 4. Bir örnek nasıl servise dönüşür {#_4-how-a-sample-becomes-a-service}

Sinyali kullanılabilir yapan kısım bu ve buna zevk değil depolama modeli karar verir.
`schema/clickhouse/0095_profiles.sql` `cityHash64(tenant_id, service_name)` ile shard'lar ve
`(tenant_id, service_name, profile_type, timestamp)` ile sıralar, çünkü "flame graph her zaman tek pencerede tek servistir". Okuma API'si de aynı fikirde:
`GET /api/v1/profiles/flame` `service`'i **zorunlu kılar** ve `host` yalnızca daraltan bir filtredir. Host alıp profile dönen bir okuma yolu yok.

Bu yüzden örnekler tek, ayrışmamış bir host profile'ı olarak gönderilmek yerine **süreç başına paylaştırılır**:

| Süreç | `service.name` şu olur |
|---|---|
| altyapı ajanının bulduğu bir servis | **discovery kural id'si** (`redis`, `postgresql`, `nginx`), süreç metriklerinin `openlog.discovery.id` olarak taşıdığı değerin aynısı |
| başka herhangi bir şey | binary'nin kendi adı (`sshd`, `containerd`), `process.executable.name`'in yaptığı gibi |
| bir kernel thread'i | thread başına bir sahte servis yerine bir kez `kernel` |

**Bulunan isimler buraya nasıl gelir?** Eşleme altyapı ajanının belleğindedir ve her turda yeniden kurulur, yani doğrudan okunamaz. Bu yüzden altyapı ajanı,
dil ajanları için `host-id`'yi zaten yayınladığı gibi (semantic-conventions §1) bunu runtime dizinine yayınlar: atomik yazılan, ajan çalışmıyorken olmayan ve
asla zorunlu olmayan, isteğe bağlı bir dosya.

```
/run/openlog-infra-agent/services
exe<TAB>/usr/bin/redis-server<TAB>redis
container<TAB>3f2a1b9c…<TAB>redis
```

Anahtar **pid değil, çalıştırılabilir yol ve container id'dir**. Pid'ler yayınlar arasında değişir, yani pid anahtarlı bir dosya yazıldıktan saniyeler sonra yanlış
olurdu; çalıştırılabilir yol ve container id, adlandırdıkları şey var olduğu sürece sabittir. Profiler bir örneğin pid'ini `/proc` üzerinden kendisi bunlara çözer.

Türünü tanımadığı bir satır reddedilmek yerine atlanır, yani yeni bir şey yayınlamayı öğrenen altyapı ajanı, onu okumayı henüz öğrenmemiş profiler'ı bozmaz.

Dosya yoksa (altyapı ajanı kurulu değil veya daha eski) her süreç binary adına düşer. Bu tek bir ekrandaki isimleri bozar, profiling'i bozmaz.

Ortaya çıkan her isim için, her biri `host.id` ve [semantic-conventions.md](/tr/contracts/semantic-conventions) §2'nin süreç attribute'larını taşıyan bir OTLP profile üretilir.
`service.name` olmadan tek profile göndermek reddedildi: Kafka partition anahtarı `<tenant_id>/<service.name>`'dir, yani fleet'teki her host tek partition'a düşer ve flame graph
ekranının seçecek bir şeyi kalmazdı.

Açıkça söylemeye değer sonuç: bu ekranda "service", *bir servis veya bir binary* demektir. Bu, yalnızca instrument edilmiş süreçlerin kendini profile edebildiği zamanki anlamın genişlemesidir.

## 5. Payload {#_5-the-payload}

Go ajanının gönderdiğiyle aynı ([profiles.md](/tr/contracts/profiles)), çünkü aynı sinyal:

```
POST <endpoint>/v1/profiles
openlog-license-key: olk_…
content-type: application/x-protobuf        (gzip optional)
```

`profile_type` `cpu`, `unit` `nanoseconds`'dır. Örneklenen bir stack, örnek sayısı sampling periyoduyla çarpılarak değere çevrilir, yani sayı Go ajanındaki anlamını taşır:
o stack üzerindeki CPU nanosaniyesi.

Yeniden denemeler ingest sözleşmesini izler (`Retry-After` ile 429, 502, 503, 504) ve **sınırlıdır**: bir profile zaten geçmiş bir pencereyi ölçer, yani sonraki pencereden öteye
yeniden denemek son profile'ı kurtarmak yerine sonrakine mal olur.

Hiçbir şeyin çalışmadığı bir aralık hiç istek üretmez.

## 6. Sınırlar {#_6-limits}

| | |
|---|---|
| Sampling frekansı | Varsayılan CPU başına 99 Hz. 100 yerine 99, böylece sampling zamanlayıcı güdümlü işle aynı adıma düşmez |
| Profile aralığı | 60 sn, diğer üreticilerle aynı |
| Stack derinliği | 127 frame, BPF stack map'in kendi sınırı; daha derin stack'ler kökten kırpılır ve işaretlenir |
| Aralık başına farklı stack | 20 000, üstünde en küçükleri atılır; flame graph onları zaten çizemez |
| Aralık başına servis | 256, yani istek başına yeni binary fork eden bir makine tek host'u binlerce seriye çeviremez |
| Kaynak bütçesi | sampler tek çekirdeğin yüzde birkaçıyla sınırlıdır; aşmak frekansı düşürür ve bunu söyler |

## 7. Symbolication {#_7-symbolication}

Native stack'ler, binary taşıyorsa fonksiyon adlarına çözülür (ELF sembol tablosu ve Go binary'leri için Go `pclntab`). Strip edilmiş binary adres verir ve frame atılmak yerine
`<binary>+0x<offset>` olarak raporlanır: CPU'yu hangi binary'nin yaktığını bilmek yine de bir cevaptır.

Yorumlanan runtime'lar (Python, PHP, Ruby, JVM) uygulamanın değil **yorumlayıcının** frame'lerini gösterir. Bu gerçek bir sınırlamadır, açılacak bir bug değil: runtime başına
unwinding gerekir ve bu kendi sözleşmesidir. Olduğu yerde dil ajanının kendi profiler'ı daha iyi cevaptır.

## 8. Güncel tutmak {#_8-keeping-it-current}

`install.sh` paketi kurar; o andan itibaren bileşeni ajanın kendi sürümünde **altyapı ajanı** tutar (D-149), PHP ve Java ajanı kurulumlarıyla aynı bileşen yöneticisini kullanarak.
`-reconcile` **kullanmaz**: o adım bilerek çevrimdışıdır (HTTP yok, güven anahtarı yok, imzalı manifest yok), yani sürüm alıp doğrulayamaz.

`config.yaml`:

```yaml
ebpf_profiler:
  mode: auto                     # auto = kur/yükselt; manual = yalnızca raporla; off = ajanın kurduğunu kaldır
  version: agent                 # agent = ajanın kendi sürümü, ya da örneğin 0.9.1
  remote_config: true            # Fleet sayfasının mode/version değerleri bunların yerine geçer
  health_check_after: 2m         # geçişten sonra: servisin çalıştığını kontrol et, çalışmıyorsa rollback yap
  install_root: /opt/openlog/ebpf-profiler
```

**Ajanın yapmadığı bir kuruluma asla dokunulmaz.** Ajanın kendi install root'u bir işaretçi dosya taşır; onsuz bir kök `dpkg`/`rpm`'e veya bir kişiye aittir, `unmanaged` olarak raporlanır
ve paket yöneticisine bırakılır. Aynı sebeple `/usr/lib/systemd/system` içindeki paketli bir unit, üzerine yazılmak yerine kurulumu durdurur: ajanın kendi unit'i, tarball kurulumlarının
kendininkini tuttuğu `/etc/systemd/system`'e gider.

El sıkışma yetki sınırı boyunca bölünür, çünkü ajan yetkisiz çalışır:

| Adım | Kim | Ne |
|---|---|---|
| karar, indirme, doğrulama, hazırlama | ajan | imza, sürüm, platform artifact'i ve rollback floor, `<state_dir>/ebpf-profiler/staged/<v>/` içine |
| istek | ajan | `request.json`, sonra servis yöneticisi yeniden başlatsın diye çıkar |
| kurulum | yetkili pre-start adımı (`-apply`) | **her şeyi yeniden** doğrular, root'a ait olarak `install_root/versions/<v>/` içine açar, `current`'ı geçirir, unit'i kurar ve etkinleştirir |
| sonuç | yetkili adım | ajanın install root'unda `ebpf-profiler-status.json`, sonraki başlatmada geri okunur |

`state_dir` altındaki her şeyi ajan kullanıcısı yazar, yani yetkili adım için güvenilmezdir: imzayı yeniden doğrular, arşivi boyutunu ve sha256'sını kendisinin kontrol ettiği root'a ait
bir dosyaya kopyalar ve açılan ağacı yalnızca root'a aitse kabul eder.

Geçişten `health_check_after` sonra ajan, `current`'ın o sürümü gösterdiğini, binary'sinin ve manifest'inin yerinde olduğunu ve servisin çalıştığını doğrular; aksi halde önceki sürüme rollback ister,
o sürümün diskte tutulmasının sebebi de bu. Rollback edilmiş bir sürüm asla otomatik yeniden denenmez, başarısız olan da bir saat içinde denenmez. İmzalı sha256 bu noktada **yeniden kontrol edilmez**:
açılmış binary'yi değil tarball'ı kapsar ve yetkili adım açmadan önce doğrulamıştır.

Sync, iki yönde de bir `ebpf_profiler` bölümü taşır: host modu, kaynağı, install root'un sahibini, geçerli ve hedef sürümü, servisin çalışıp çalışmadığını ve son işlemi raporlar; backend fleet'in
`mode`, `target_version` değerleri ve imzalı manifest ile cevap verebilir. Bileşen hiç kurulamadığında (Linux değil, container veya dev build, güvenilen anahtar yok veya yetkili pre-start adımı olmayan
bir başlatma) host sessizce başarısız olmak yerine sebebiyle birlikte `capable: false` raporlar.

## 9. Hatalar {#_9-errors}

`401` bilinmeyen veya eksik lisans anahtarı · `413` gövde çok büyük · `429` rate limit, `Retry-After` ile ·
`503` sonra tekrar dene. Gövdeler `{"error": {"code", "message"}}` biçimindedir.

Asla yeniden denenmez: `401` ve `413`. Sonraki denemede hiçbir şey farklı olmaz.
