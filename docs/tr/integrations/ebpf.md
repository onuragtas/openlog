# openlog eBPF profiler {#openlog-ebpf-profiler}

Tüm makineyi kapsayan CPU profili: makinedeki her süreç, enstrümante edilmiş olsun olmasın, örneklenir ve OTLP profili olarak
gönderilir. [ebpf-profiler.md](/contracts/ebpf-profiler) dokümanını uygular: kontrat o dokümandır, burası onun gerçeklemesi.

**Ayrı bir bileşendir, altyapı ajanının parçası değildir.** Altyapı ajanının sandbox'ı bu işi bilerek yasaklar: systemd unit'i
`bpf(2)` ve `perf_event_open(2)` çağrılarını filtreler, yalnızca `CAP_DAC_READ_SEARCH` ve `CAP_SYS_PTRACE` yetkilerini taşır,
Kubernetes DaemonSet duruşu da ayrıcalıklı olmadığına dair bir garanti olarak yayımlanmıştır. Bunu hiç kurmayan bir operatör o
duruşu olduğu gibi korur (D-148).

**Yalnızca Linux**, üstelik derleme açısından değil işlev açısından: buradaki her paket darwin ve windows'ta da derlenir ve vet'ten
geçer, örnekleyici bir build tag'in arkasında durur. Hiç çalışmayacağı yerde derlenmeyen bir bileşen, bütün repoda çalışmayı
zorlaştırır.

## Durum {#status}

Aşağıdaki "Kurulum" bölümünün anlattığı gibi gerçek bir makinede çalıştı. Var olan ve test edilenler:

- `internal/otlpprofiles`: birleştirilmiş stack'lerden, ortak sözlüğüyle birlikte bir OTLP profil payload'ı üretir.
- `internal/attribute`: bir örneğin hangi servisin altına yazılacağını belirler. Altyapı ajanının yayımladığı discovery haritası
  varsa onu, yoksa binary'nin adını, kernel thread'leri için `kernel` kullanır.
- `internal/export`: lisans anahtarıyla, gzip ile `/v1/profiles` adresine gönderir. Retry'ler sınırlıdır, böylece ölü bir ingest
  bir sonraki profilin yerini almak yerine onu yalnızca geciktirir.
- `internal/aggregate`: bir aralığın ham örneklerini servis başına gruplar ve kontratın koyduğu her sınırı uygular: 127 frame
  (kesildiği işaretlenir), 20 000 stack ve 256 servis. En yoğun olanı değil en sessiz olanı atar.
- `internal/config`: ayarları ortamdan okur, uygulayamayacağı şeyi tahmin etmek yerine reddeder: lisans anahtarı yoksa, örnekleme
  frekansı 1..1000 dışındaysa, aralık bir saniyenin altındaysa.
- `internal/profiler`: bunları birbirine bağlayan aralık döngüsü: örnekle, grupla, dönüştür, gönder. Kernel'e dokunan her çağrı
  `Sampler` arayüzünün arkasındadır; döngüyü eBPF hiç çalıştıramayan bir makinede de test edilebilir tutan da budur.
- `internal/version` ve komutun kendisi: `-version`, bu makinenin örneklenip örneklenemeyeceğini söyleyen `-self-test` ve bir
  ingest'e yöneltmeden önce neyin saklanacağını yazdıran `-once`.

- `internal/sampler` (tesisat değil, program): BPF programı `cilium/ebpf/asm` ile elle yazılır, map'leri de hemen yanında tanımlanır.
  Bunların hiçbirini oluşturmak için kernel gerekmez, bu yüzden tag'siz bir dosyada durur ve testler şeklini burada doğrular: iki
  stack de intern edilmiş, yalnızca bilinen map'lere başvuruluyor, anahtar kodlaması map tanımıyla bayt bayt eşleşiyor.

- `internal/sampler` (Linux tesisatı): yapılandırılan frekansta CPU başına bir perf event, `PERF_EVENT_IOC_SET_BPF` ile bağlanan
  program ve her aralıkta boşaltılıp temizlenen map'ler. Kernel'in yazdıklarının üzerindeki aritmetik tag'sizdir ve burada test
  edilir: CPU başına bir sayacı CPU'lar boyunca toplamak ve sabit genişlikli bir stack dizisinin son frame'den sonra bıraktığı
  sıfır dolgusunu kırpmak.

Binary linux/amd64, linux/arm64, darwin ve windows için derlenir. Linux dışında `sampler.New`, hiçbir şey üretmeyen bir örnekleyici
döndürmek yerine reddeder.

**Kernel tarafındaki kodun CI'ı yok.** İki Linux mimarisi için derleniyor ve saf kısımları test ediliyor, ama bu repoda ayrıcalıklı bir Linux işi yok ve geliştirme Mac'i BPF yükleyemiyor, bu yüzden bu koddaki bir değişiklik ancak gerçek bir makinede kanıtlanır. `New` orada ya çalışır ya da hangi yetkinin veya `perf_event_paranoid` ayarının engel olduğunu tam olarak söyler.

Bileşenin tamamının yazılış biçimini de bu belirler: kernel'e dokunmayan her şey bir arayüzün arkasında durur ve testlerin
çalışabildiği yerde test edilir, kernel'e dokunan kısım ise okunabilecek kadar küçük tutulur. Buradaki bir iddia çalıştırılıp
görülmediyse, yeşil bir test ima etmek yerine bunu açıkça söyler.

- `internal/symbol`: bir adresi adlandırır. O adreste `/proc/<pid>/maps`'e göre hangi dosyanın olduğu, dosyanın neresine denk geldiği
  (PT_LOAD başlıklarıyla, çünkü semboller sanal adres taşır, maps ise offset verir) ve sembol tablosunun ona ne dediği. Soyulmuş bir
  binary `app+0x1010` cevabını verir, yok olmuş bir süreç çıplak adresi; hiçbir şey uydurulmaz. Testin içinde bayt bayt oluşturulan
  bir ELF'e karşı test edilir, çünkü bu repo binary fixture taşımaz.

Her pencere için yeni bir symbolizer oluşturulur. Önbellekleri pid'e göre tutulur ve pid'ler yeniden kullanılır: penceresinden uzun
yaşayan bir önbellek, yeni bir programın adreslerine o pid'de daha önce ne varsa onun sembollerini takar. Bu, tamamen akla yatkın
görünen bir şekilde yanlıştır.

## Kurulum {#installing-it}

`packaging/systemd/openlog-ebpf-profiler.service` unit'in kendisidir ve bunun ayrı bir bileşen olmasının da tek nedenidir.
`CAP_BPF`, `CAP_PERFMON`, `CAP_DAC_READ_SEARCH` ve `CAP_SYS_PTRACE` ister, root değil; altyapı ajanının unit'inin filtrelediği
`bpf(2)` ve `perf_event_open(2)` çağrılarına da izin verir. O filtreyi buraya kopyalamak, başlayıp hiçbir zaman örnekleyemeyen bir
profiler çıkarmak demek olurdu; bu bileşen de tam olarak o hatayı dışlamak için yazıldı.

`packaging/openlog-ebpf-profiler.env.example` yapılandırmadır, yalnızca ortam değişkenlerinden oluşur.

Sürüm, linux/amd64 ve linux/arm64 için bir .deb ve bir .rpm derler, `install.sh` de varsayılan olarak ajanın yanına kurar:

```sh
curl -fsSL .../install.sh | sudo sh -s -- --license-key KEY --endpoint URL
curl -fsSL .../install.sh | sudo sh -s -- --license-key KEY --endpoint URL --no-ebpf-profiler   # profiler olmadan
```

Altyapı ajanında olmayan yetkileri taşıdığı için ayrı bir paket ve ayrı bir servis olarak kalır, kurulum da çıkarken nelerin
verildiğini söyler. Ajanla aynı imzalı sürüm manifest'ine karşı doğrulanır: boyut ve SHA-256.

Çalışamayacağı yerde (Linux değilse veya kurulum yöntemi tarball ise) bir gerekçeyle atlanır ve ajan her zamanki gibi kurulur.
`--with-ebpf-profiler` ile açıkça istersen aynı koşullar hataya dönüşür: bunu adıyla isteyen biri, sessizce hiçbir şey yapılmamasını
değil, hata almayı hak eder.

**Helm chart yok.** Kubernetes için henüz bir DaemonSet yok.

İlk kez 2026-09-22'de bir Debian makinede çalıştı:
nfpm her sürümde paketleri derliyor, unit systemd tarafından yüklendi ve BPF programı kernel verifier'ından geçti. Sonuncusunun
kanıtı hatanın düştüğü yer: New() map'leri oluşturur, bağlar ve perf event'leri açmadan *önce* ebpf.NewProgram çağırır; makine de
programın yüklenmesinden sonraki adımda, `perf event on cpu 0 … permission denied` ile hata verdi. O makinenin
`kernel.perf_event_paranoid` değeri 4'tü (mainline'ın en fazla 2 olan sınırının üstünde bir Debian/Ubuntu seviyesi); değer
düşürülünce profiler örnekledi ve profilleri profiling ekranına ulaştı.

İlk makinenin ortaya çıkardığı ve o zamandan beri düzeltilen diğer şeyler: sembol önbelleği sınırsızdı ve süreç, unit'in MemoryMax
sınırı altında OOM ile öldürüldü (internal/symbol'de artık dosya başına ve toplam bir bütçe var); operatörün düzeltmesi gereken bir
hata da sonsuza kadar on saniyede bir yeniden deneniyordu (exit 78 ve RestartPreventExitStatus).

## clang yok, cgo yok {#no-clang-no-cgo}

BPF programı `bpf2go` ile C'den üretilmek yerine `cilium/ebpf/asm` ile yazılır. Bu repo `CGO_ENABLED=0` ile altı os/arch hedefi
çıkarıyor ve birkaç düzine komut üretmek için clang'i bu derlemeye sokmak yanlış bir takas olurdu. Bedeli, programın küçük kalmak
zorunda olması.

## Geliştirme {#development}

```sh
go test ./...
go vet ./...
```
