# openlog sürümü çıkarmak {#releasing-openlog}

openlog sürümlerinin nasıl derlendiği, imzalandığı ve yayınlandığı, ayrıca testler için yerelde imzalı bir sürümün nasıl
üretildiği. Bağlayıcı kurallar: [docs/contracts/releases-updates.md](/tr/contracts/releases-updates);
gerekçe: [docs/plan/09-releases-updates.md](https://github.com/onuragtas/openlog/blob/master/docs/plan/09-releases-updates.md) (D-025…D-029).

Her sürümün backend, image, Helm chart ve ajanlar için **tek bir ürün sürümü** vardır (`vX.Y.Z`, ön sürümler `vX.Y.Z-beta.N`
olarak `beta` kanalına gider). Sürüm manifest'i Ed25519 ile imzalanır. Ajanlar ve backend yalnızca içlerine derlenmiş public
key'lere güvenir, yani **imzalama anahtarı otomatik güncellemenin güven köküdür**: onu elinde tutan herkes her ajana kod
gönderebilir.

## Araçlar {#tooling}

| Ne | Nerede |
|---|---|
| `openlog-release` CLI (`keygen`, `build-manifest`, `sign`, `build-index`, `verify`, `archive`) | `cmd/openlog-release`, `libs/release` kullanır |
| Build hedefleri | kök `Makefile` (`release-*`, `shellcheck`, `actionlint`, `helm-lint`, `package-test`, `install-test`) |
| deb/rpm | `packaging/nfpm/infra-agent.yaml`, `packaging/scripts/*.sh` (nfpm, sürümü sabitlenmiş `goreleaser/nfpm` image'ında çalışır) |
| Kurulum betikleri | `scripts/install.sh` (ajan) ve `scripts/install-server.sh` (Compose sunucusu), her sürüme eklenir |
| CI | `.github/workflows/ci.yml` (imzalı bir release dry run dahil), `.github/workflows/release.yml` |

### Sürümün içeriği (`dist/v<version>/`) {#release-contents-dist-v-version}

| Dosya | Manifest |
|---|---|
| `openlog-infra-agent_<v>_linux_{amd64,arm64}.tar.gz` (tek üst dizin: binary, `LICENSE`, `README.md`, `packaging/`) | `artifacts[]` `component=infra-agent`, `format=tar.gz` |
| `openlog-infra-agent_<v>_linux_{amd64,arm64}.{deb,rpm}` | `format=deb` / `rpm` |
| `openlog-infra-agent_<v>_darwin_{amd64,arm64}.tar.gz` ve `.pkg` (`infra-agent-macos` job'u; pkg: `agents/infra/packaging/macos/build-pkg.sh`) | `format=tar.gz` / `pkg` |
| `openlog-infra-agent_<v>_windows_{amd64,arm64}.zip` ve `.msi` (`infra-agent-windows` job'u; MSI: `agents/infra/packaging/windows/build-msi.ps1`, arm64 MSI derlenir ama kurulum testi yapılmaz) | `format=zip` / `msi` |
| `openlog_<v>_linux_{amd64,arm64}.tar.gz` (tüm backend binary'leri) | `component=backend` |
| `openlog-php-agent_<v>_linux_{amd64,arm64}.{tar.gz,deb,rpm,apk}` (PHP ajanı: 36 modül, `openlog-php-install`; [php-agent.md](/tr/contracts/php-agent) §7.1) | `component=php-agent`, `format=tar.gz` / `deb` / `rpm` / `apk` |
| `openlog-javaagent-<v>.jar` + `.sha256` (Java ajanı, [aşağıda](#java-agent-jar)) | `component=java-agent`, `os`/`arch` `any`, `format=jar` (`.sha256` manifest'te yok) |
| `openlog-node-<v>.tgz` + `.sha256` (`openlog-node` paketinin `npm pack` çıktısı, [aşağıda](#language-agent-packages-github-release)) | `component=node-agent`, `os`/`arch` `any`, `format=tgz` |
| `openlog_agent-<pep440 v>-py3-none-any.whl` + `.sha256` (Python ajanı wheel'i) | `component=python-agent`, `os`/`arch` `any`, `format=whl` |
| `openlog_agent-<pep440 v>.tar.gz` + `.sha256` (Python ajanı sdist'i) | manifest'te yok |
| `OpenLog.Agent.<v>.nupkg` + `.sha256` (.NET ajanı) | `component=dotnet-agent`, `os`/`arch` `any`, `format=nupkg` |
| `openlog-<v>.tgz` (Helm chart `deploy/helm/openlog`, `version` = `appVersion` = `<v>`) | `helm_charts.openlog` (ve `helm_chart`) |
| `openlog-agent-<v>.tgz` (Helm chart `deploy/helm/openlog-agent`, `version` = `appVersion` = `<v>`) | `helm_charts.openlog-agent` |
| `openlog-compose-<v>.tar.gz` (git'te izlenen `deploy/compose`, `x-openlog-compose-version` damgalı; `make release-compose`) | `component=compose`, `os`/`arch` `any`, `format=tar.gz` |
| `ghcr.io/onuragtas/openlog:<v>` (dosya değil) | `images.openlog` = `…@sha256:<digest>` |
| `manifest.json`, `manifest.json.sig` | imzalı |
| `index.json`, `index.json.sig` | imzalı; tüm sürümler, en yenisi başta |
| `install.sh` | manifest'te yok (bootstrap, bkz. [install.sh güven modeli](#install-sh)) |
| `install-server.sh` | manifest'te yok (sunucu bootstrap'ı, bkz. [install-server.sh](#install-server-sh)) |

PHP ajanı artifact'lerini `agents/php/packaging/build-artifacts.sh VERSION DIR` derler (Docker; çalıştırma başına bir mimari).
`release.yml` bunu `php-agent-artifacts` job'larında, native `ubuntu-24.04` (amd64) ve `ubuntu-24.04-arm` (arm64) runner'larında
36 modülün tamamıyla çalıştırır (her biri yaklaşık 30–45 dk, image build'iyle paralel), deb/rpm/apk kurulum testini yapar
(`agents/php/packaging/test.sh`) ve dosyaları `release` job'una verir; o da `make release-local`'dan önce bunları
`dist/v<version>/` içine indirir. CI release dry run'ı yalnızca `8.2-nts-glibc` ve `8.3-nts-musl` hedeflerini deb ve apk ile derler
(yaklaşık 8 dk). Yerelde: `make release-local`'dan önce `make release-php-agent VERSION=… [PHP_TARGETS="8.2-nts-glibc"]
[PHP_PACKAGES=""]`; bu olmadan yerel sürümde PHP ajanı olmaz.

`build-manifest` artifact'leri bu dosya adlarından bulur; bilinmeyen dosyalar yok sayılır. `compatibility` alanını
`oldest_supported_agent = X.(Y-2).0` ve `rollback_floor = X.(Y-1).0` ile doldurur; bunu
`RELEASE_COMPAT="min_upgrade_from=0.3.0 rollback_floor=0.2.0"` ile ezebilirsin (boş değer sınırı kaldırır).
`migrations`, `migrations/postgres` ve `schema/clickhouse` içinden hesaplanır (en yüksek numara, ilk satırı
`-- openlog:phase contract` olan dosyalar).

Sürüm enjeksiyonu (`make build` ve image dahil tüm build'ler): `-X
github.com/onuragtas/openlog/internal/version.{Version,Commit,Date}`, `-X
github.com/onuragtas/openlog/internal/release.trustedKeys=$OPENLOG_RELEASE_PUBLIC_KEYS`; ajan aynılarını
`github.com/onuragtas/openlog/agents/infra/internal/{version,release}` altında alır. Her binary sürümünü `-version` ile yazdırır.

## Tek seferlik kurulum (repository sahibi) {#one-time-setup-repository-owner}

1. **Sürüm anahtarını çevrimdışı üret** (güvenilir bir makine, tercihen bağlı olmayan, asla CI değil):

   ```sh
   go build -o /tmp/openlog-release ./cmd/openlog-release
   /tmp/openlog-release keygen > openlog-release-key-1.env   # bu dosyayı çevrimdışı tut
   ```

   Çıktı:

   ```
   # openlog release key 3f0c9a…
   OPENLOG_RELEASE_KEY_ID=3f0c9a1b2c3d4e5f
   OPENLOG_RELEASE_PUBLIC_KEYS=<base64 public key>
   OPENLOG_RELEASE_SIGNING_KEY=<base64 seed>      # SECRET
   ```

   Seed'i bir parola yöneticisinde veya donanım destekli bir kasada sakla (iki kişi kurtarabilmeli). Kaybetmek anahtar
   rotasyonu demektir; sızdırmak ise **acil** rotasyon demektir (aşağıda).

2. **GitHub → Settings → Secrets and variables → Actions:**
   - *Secret* `OPENLOG_RELEASE_SIGNING_KEY` = seed. Onu `release` workflow'uyla sınırla (önerilen: zorunlu reviewer'lı ve `v*`
     tag korumalı bir `release` environment'ına koy ve `release` job'una `environment: release` ekle).
   - *Variable* `OPENLOG_RELEASE_PUBLIC_KEYS` = public key (iki anahtar aktifken virgülle ayrılır).
3. **Settings → Actions → General → Workflow permissions:** read and write (release job'u sürüm oluşturur; image job'u
   `packages: write` ile GHCR'a push eder).
4. İlk image push'undan sonra GHCR paketi `openlog`'u **public** yap (Packages → openlog → Package settings), böylece pull
   kimlik bilgisi olmadan çalışır. Aynısı ilk `helm-oci` çalışmasından sonra `charts/openlog` ve `charts/openlog-agent` için de
   geçerlidir (isteğe bağlı, bkz. [Helm chart'ları](#helm-charts)).
5. `v*` tag'lerini koru (Settings → Rules → Tag rulesets), böylece yalnızca maintainer'lar sürüm çıkarabilir.

6. **npm (Node.js ajanı `openlog-node`, isteğe bağlı):** paket scope'suzdur, yani npm organizasyonu gerekmez. npmjs.com'da
   paketler için read ve write izinli bir *granular access token* oluştur (`openlog-node` daha yokken yapılacak ilk publish için
   tüm paketlere ver; sonra `openlog-node` ile sınırlanabilir; 2FA zorunlu hesaplarda token'ın 2FA'yı atlamasına izin verilmeli).
   Repository *secret*'ı `NPM_TOKEN` olarak sakla. Secret olmadan `release.yml` npm job'unu uyarıyla atlar ve geri kalan her şey
   yayınlanır. Provenance (`npm publish --provenance`) public GitHub repository'si ve job'un `id-token: write` iznini ister (zaten
   ayarlı). Token'ı süresi dolmadan yenile (granular token'ların son kullanma tarihi vardır).

7. **PyPI (Python ajanı `openlog-agent`, isteğe bağlı):** yayınlama PyPI *trusted publishing* (OIDC) kullanır, yani token
   saklanmaz. pypi.org'da (2FA'lı hesap) → *Your projects* → *Publishing* → *Add a new pending publisher* (GitHub): PyPI proje adı
   `openlog-agent`, owner `onuragtas`, repository `openlog`, workflow adı `release.yml`, environment adı `pypi`. İlk başarılı
   publish, pending publisher'ı projenin trusted publisher'ına çevirir ve projeyi oluşturur (ortak maintainer'ları orada ekle).
   GitHub'da `pypi` environment'ını oluştur (Settings → Environments; isteğe bağlı: zorunlu reviewer'lar, deployment tag'leri `v*`)
   ve repository *variable*'ı `PYPI_PUBLISH` = `true` ayarla. Variable olmadan `release.yml` PyPI job'unu uyarıyla atlar ve geri
   kalan her şey yayınlanır. (`openlog` adı PyPI'da alakasız bir projeye ait, `openlog-agent` olması bundan.)
8. **NuGet (.NET ajanı `OpenLog.Agent`, isteğe bağlı):** nuget.org'a giriş yap ve bir API key oluştur (Account → API Keys):
   scope *Push new packages and package versions*, glob pattern `OpenLog.Agent` ve en fazla 365 gün son kullanma süresi (ilk push
   paketi o hesap altında oluşturur; istersen `OpenLog.` ID önek rezervasyonu talep et). Repository *secret*'ı `NUGET_API_KEY`
   olarak sakla. Secret olmadan `release.yml` NuGet job'unu uyarıyla atlar ve geri kalan her şey yayınlanır. Anahtarı süresi
   dolmadan yenile.

`openlog-release-key-1.env` dosyasını 2. adımdan sonra çevrimiçi her makineden sil.

## Sürüm çıkarmak {#cutting-a-release}

Sürümler süreklidir: `ci.yml` çalışması yeşil olan her `master` push'u yayınlanır.

1. `ci.yml` job'u `release-tag` diğer her job'dan sonra çalışır (hiçbiri başarısız olmamış veya iptal edilmemiş olmalı; path
   filtresiyle atlanan job'lar sorun değil). Commit zaten tag'liyse, `master` ilerlemişse (o yeni push'un çalışması yayınlar) veya
   commit mesajında `[skip release]` varsa hiçbir şey yapmaz.
2. Sonraki sürüm: en yüksek stable tag `vX.Y.Z` artı bir patch. Commit mesajındaki `[release minor]` veya `[release major]` bunun
   yerine minor veya major sürümü artırır.
3. Go ajan modülleri o sürüm için hazırlanmamışsa `scripts/go-agent-release.sh prepare` çalıştırır ve
   `release: prepare X.Y.Z (Go agent modules)` commit'ini `github-actions[bot]` olarak `master`'a push eder. `GITHUB_TOKEN` ile
   yapılan push yeni bir `ci.yml` çalışması başlatmaz, yani bu döngüye girmez.
4. Annotated `vX.Y.Z` tag'ini Go modül tag'leriyle birlikte push eder (tek atomik push, `master` hâlâ o commit'teyken) ve
   `release.yml`'i üzerinde `workflow_dispatch` ile (`GITHUB_TOKEN` ile push edilen tag kendiliğinden workflow başlatmaz) ve
   `ci_tested=true` girdisiyle başlatır.

`ci_tested=true` (`RUN_TESTS=0`) ile `release.yml` yalnızca derler, imzalar ve yayınlar: Java, Node.js, Python ve .NET unit
testleri, deb/rpm/apk kurulum testi, PHP deb/apk kurulum testi ve Windows MSI kurulum testi aynı commit'in `ci.yml` çalışmasında
zaten geçmiştir ("release dry run", "infra-agent (Windows)"). PHP rpm kurulum testi ile Windows ve macOS release binary'lerinin
`-version`/`-once` smoke testleri yine çalışır. Elle push edilen tag her testi çalıştırır. `node-agent-npm` yeniden paketlemek
yerine `node-agent-package` tarafından derlenen tarball'ı (release asset'i) yayınlar.

Gereksinimler: *Settings → Actions → General → Workflow permissions* *Read and write* izni vermeli; `master` / `v*` üzerindeki
branch koruması veya tag ruleset'leri GitHub Actions'ın push etmesine izin vermeli. Senin tarafında başka bir şey gerekmez.

Ön sürümler ve elle sürümler, tag'i kendin push ederek hâlâ çalışır (örneğin bir beta; elle tag, sonraki otomatik sürümün en yüksek
*stable* tag'den devam etmesini sağlar):

```sh
make release-prepare VERSION=0.5.0-beta.1 && git commit -am "release: 0.5.0-beta.1" && git push
git tag -s v0.5.0-beta.1 -m "openlog 0.5.0-beta.1" && git push origin v0.5.0-beta.1   # beta kanalı, GitHub pre-release
```

Hazırlık push'unun da otomatik yayınlanmaması için commit mesajına `[skip release]` ekle.

Ardından `release.yml`:

1. `prepare` – tag'den sürüm/kanal; anahtar variable'ı/secret'ı eksikse erken başarısız olur.
2. `go-agent-modules` – tag'lenen commit hazır değilse sürümü (hiçbir şey derlenmeden önce) başarısız yapar:
   `scripts/go-agent-release.sh check` ve `verify` (bkz. [Go ajan modülleri](#go-agent-modules)).
3. `image` – `linux/amd64,linux/arm64` için sürüm + derlenmiş anahtarlarla derler, `ghcr.io/onuragtas/openlog:<v>` push eder (stable
   için `:latest` de), digest'i çıktı verir. Paralel olarak `php-agent-artifacts` PHP ajanı artifact'lerini ve `java-agent-jar`
   Java ajanı jar'ını derler (bkz. [Java ajanı jar'ı](#java-agent-jar)); ikisi de workflow artifact'i olarak `release`'e verilir.
4. `release` – `make web`, GitHub API ile `manifest.json`'u olan önceki sürümleri toplar,
   `make release-local VERSION=<v> RELEASE_IMAGE=ghcr.io/onuragtas/openlog@<digest> …`
   (derleme → imza → `verify --check-artifacts` → tüm sürümler üzerinden index → imza → doğrulama), amd64 deb/rpm'i container'larda
   kurar, GitHub release'i taslak olarak oluşturur, her şeyi yükler ve yayınlar (stable: *latest* olarak işaretlenir; beta:
   pre-release). Beta için `index.json(.sig)` en son stable sürüme de yeniden yüklenir, çünkü
   `releases/latest/download/index.json` (varsayılan index URL'i) en son *stable* sürümü sunar.

5. `go-agent-tags` – GitHub release yayınlandıktan sonra, `release-tag` daha önce push etmediyse Go modül tag'lerini tag'lenen
   commit üzerine push eder (atomik push; aşağıya bak), sonra `proxy.golang.org`'dan bunları ister (mümkün olduğunca).

6. `node-agent-npm` – GitHub release yayınlandıktan sonra `node-agent-package` tarafından derlenen `openlog-node-<v>.tgz` dosyasını
   (release asset'i) npm'e `openlog-node@<v>` olarak yayınlar (bkz. [Node.js ajan paketi](#node-js-agent-package)).

- `python-agent-pypi` – GitHub release yayınlandıktan sonra `python-agent-package` tarafından derlenen wheel ve sdist'i (release
  asset'leri) trusted publishing ile PyPI'a `openlog-agent==<v>` olarak yayınlar (bkz.
  [Python ajan paketi](#python-agent-package)).

7. `dotnet-agent-nuget` – GitHub release yayınlandıktan sonra `dotnet-agent-package` tarafından derlenen
   `OpenLog.Agent.<v>.nupkg` dosyasını (release asset'i, `.snupkg` ile birlikte) nuget.org'a push eder (bkz. [.NET ajan paketi](#net-agent-package)).

Üç registry job'u isteğe bağlıdır. `node-agent-package`, `python-agent-package` ve `dotnet-agent-package` her sürümde `release`'den
önce çalışır (testler dahil) ve dosyaları her zaman GitHub release'e eklenir, yani ajanlar npm, PyPI veya nuget.org olmadan da
kurulur ([Dil ajanı paketleri](#language-agent-packages-github-release)).

- `helm-oci` – isteğe bağlı (repository variable'ı `HELM_OCI_PUSH` = `true`, yoksa uyarıyla atlanır): GitHub release yayınlandıktan
  sonra oradan `openlog-<v>.tgz` ve `openlog-agent-<v>.tgz` dosyalarını indirir, sha256'larını imzalı `manifest.json` ile karşılaştırır
  ve `oci://ghcr.io/onuragtas/charts` adresine push eder (bkz. [Helm chart'ları](#helm-charts)).

Herhangi bir adım başarısız olursa hiçbir şey yayınlanmaz; başarısız bir taslak silinebilir ve tag yeniden push edilebilir.
Başarısız bir `go-agent-tags` job'u doğrudan yeniden çalıştırılabilir: commit'i zaten gösteren tag'ler atlanır.

### Go ajan modülleri {#go-agent-modules}

Go ajanı (`agents/go`) bir Go modülleri kümesidir, bu yüzden ürün tag'iyle **aynı commit üzerindeki Go modül tag'leriyle** yayınlanır
(D-025). Go kütüphanelerini kullanıcının build'i derler (`-ldflags` yok) ve bir modül tag'i, dosyaları sürümü zaten söyleyen bir
commit'i göstermelidir. Yani sürüm artışı tag'lemeden **önce** yapılan normal, incelenmiş bir commit'tir; CI commit oluşturmaz.

`make release-prepare VERSION=X.Y.Z` (`scripts/go-agent-release.sh prepare`):

1. `make -C agents/go set-version` → `agents/go/version.go` içinde `const Version = "X.Y.Z"`
   (`telemetry.distro.version`);
2. `agents/go/**/go.mod` içindeki her repo içi modül require'ı (bugün: `instrumentation/chi` → core, `examples` → core + grpc) →
   `vX.Y.Z` (`go mod edit -require`). `replace` direktifleri kalır: yalnızca ana modüle uygulanırlar (bu repository'deki geliştirme
   ve CI) ve kullanıcılar onları yok sayar, yani `go.sum` değişmez ve ağaç çevrimdışı derlenmeye devam eder;
3. `check` çalıştırır. Yeniden çalıştırmak idempotent'tir. `>= 2.0.0` sürümler reddedilir (modül yollarının `/v2` son eki alması
   gerekirdi).

Kontroller:

| Kontrol | Nerede | Ne zaman başarısız olur |
|---|---|---|
| `scripts/go-agent-release.sh check X.Y.Z` (`make go-agent-release-check`) | `release.yml` `go-agent-modules` | `version.go` ≠ tag, repo içi bir require ≠ `vX.Y.Z` veya bir require'ın yerel `replace`'i yoksa |
| `scripts/go-agent-release.sh verify [X.Y.Z]` (`make go-agent-verify`) | `release.yml` `go-agent-modules`, `ci.yml` `go-agent` | repo içi modülleri require eden bir modül `replace` direktifleri **olmadan** derlenmiyorsa |

`verify`, her `agents/go` modülünü o sürümde içeren bir `GOPROXY=file://` dizini yazar; bir tag'in proxy zip'i gibi git'te izlenen
dosyalardan oluşturulur (`scripts/gomodproxy`, iç içe modüller hariç). Sonra her bağımlı modülü kopyalar, `replace` direktiflerini
siler ve tek kullanımlık bir `GOMODCACHE` içinde `GOFLAGS=-mod=mod GOPROXY=file://<local proxy>,file://<module cache>/cache/download,off`
ile `go build ./...` çalıştırır. Bu bilerek birebir `GOPROXY=off` değildir: yayınlanmamış repo içi sürümler yalnızca yerel proxy'de
bulunur ve üçüncü parti modüller, `go mod download`'un önceden doldurduğu modül cache'inden salt okunur gelir. Ağdan hiçbir şey
çekilmez ve gerçek modül cache'ine yayınlanmamış sürümler asla girmez. Argüman verilmezse sürüm, require'ların kullandığı sürümdür,
böylece ci.yml job'u sürümler arasında master'ı da kapsar.

Tag'ler (`release.yml` `go-agent-tags`, liste `scripts/go-agent-release.sh tags X.Y.Z` çıktısından):
`$GITHUB_SHA` üzerinde `agents/go/vX.Y.Z` ve `agents/go/instrumentation/{grpc,chi,gin,echo}/vX.Y.Z`. `examples` tag'lenmez. Sürekli
sürümler bunları ürün tag'iyle birlikte push eder (`ci.yml` `release-tag`): `GITHUB_TOKEN` ile daha sonra push edilirse GitHub,
commit'inde `master`'da zaten bulunan workflow değişiklikleri olmayan bir tag'i reddeder ("refusing to allow a GitHub App to create
or update workflow"). Elle push edilen tag için bunlar ancak sürüm yayınlandıktan sonra push edilir, çünkü Go modül proxy'si bir sürümü
sonsuza kadar cache'ler ve modül tag'i asla yer değiştirmemelidir. Başka bir commit'te zaten var olan tag job'u başarısız yapar ve
asla taşınmaz. Ön sürümler de tag alır (`agents/go/v0.5.0-beta.1`). Go bunları pre-release sürüm sayar ve `@latest` yok sayar.
`GITHUB_TOKEN` ile push edilen tag'ler workflow tetiklemez. Tag ruleset'leri `agents/**` kapsıyorsa GitHub Actions'ın bu tag'leri
oluşturmasına izin ver.

Kullananlar: `go get github.com/onuragtas/openlog/agents/go@vX.Y.Z` (+ `…/instrumentation/<name>@vX.Y.Z`).

Sürümler arasında `agents/go/version.go` son hazırlanan sürümü tutar, yani `go get …@master` pseudo-version'ları onu
`telemetry.distro.version` olarak bildirir.

### Dil ajanı paketleri (GitHub release) {#language-agent-packages-github-release}

Aşağıdaki registry job'larının kimlik bilgileri olsun olmasın, her sürüm Node.js, Python ve .NET ajan paketlerini GitHub release'e
ekler, böylece kullanıcılar projenin npm, PyPI veya nuget.org hesaplarına asla bağımlı kalmaz:

| Asset | Derleyen (`release.yml` job'u, betik) | Manifest | Registry olmadan kurulum |
|---|---|---|---|
| `openlog-node-<v>.tgz` + `.sha256` | `node-agent-package`: `agents/node/scripts/release-pack.sh` (`npm version`, `npm ci`, `npm test`, `npm pack`) | `node-agent` / `tgz` | `npm install https://github.com/onuragtas/openlog/releases/download/v<v>/openlog-node-<v>.tgz` |
| `openlog_agent-<pep440 v>-py3-none-any.whl` + sdist `openlog_agent-<pep440 v>.tar.gz`, her biri + `.sha256` | `python-agent-package`: `agents/python/scripts/release-dist.sh` (PEP 440 sürümü, testler, `python -m build`, `twine check --strict`) | `python-agent` / `whl` (sdist: manifest'te yok) | `pip install https://github.com/onuragtas/openlog/releases/download/v<v>/openlog_agent-<pep440 v>-py3-none-any.whl` |
| `OpenLog.Agent.<v>.nupkg` + `.sha256` | `dotnet-agent-package`: `agents/dotnet/scripts/release-nupkg.sh` (unit testler, `dotnet pack`) | `dotnet-agent` / `nupkg` | bir klasöre indir, `sha256sum -c`, `dotnet nuget add source <folder> -n openlog-local`, `dotnet add package OpenLog.Agent --version <v>` (`--source` yok: restore'u klasörle sınırlar ve OpenTelemetry bağımlılıklarını kaybettirir) |

Üç job `image` ile paralel, `release`'den önce çalışır; o da workflow artifact'lerini `dist/v<v>/` içine indirir. `build-manifest`
tgz, whl ve nupkg'yi listeler (`os`/`arch` `any`), yani Java jar'ı gibi imzalanır ve `verify --check-artifacts` ile kontrol edilirler.
`.snupkg` sembol paketi NuGet push'u için workflow artifact'i olarak kalır (`dotnet-agent-symbols`). Registry job'ları tam olarak bu
dosyaları (`sha256sum -c` sonrası) yayınlar, yani registry ve release aynı byte'ları sunar.

Add data sayfası (`web/src/lib/install-commands.ts`) yalnızca `GET /api/v1/onboarding` `agent_packages.<lang>.registry = available`
bildirdiğinde registry'den kurar (API npm, PyPI ve nuget.org flat container'a sorar, 3 sn timeout, bir saat cache'lenir; erişilemeyen
registry'ler `unknown` olur), aksi halde bu release asset'lerinden kurar.

Yerelde: `make release-local VERSION=X.Y.Z …` öncesinde `make release-language-agents VERSION=X.Y.Z` (node:22-alpine, python:3.12-slim,
dotnet/sdk:8.0; `RUN_TESTS=0` testleri atlar; `NODE_BUILD=local` / `PY_BUILD=local` / `DOTNET_BUILD=local` host araçlarını kullanır).
CI job'u `release dry run` üç paketi derler ve dry-run manifest'inde değillerse başarısız olur.

### Node.js ajan paketi {#node-js-agent-package}

Node.js ajanı (`agents/node`) npm'e ürün sürümünde `openlog-node` olarak yayınlanır (D-025, D-062). 0.1.20'ye kadar `@openlog/node`
idi: npm adı `openlog` (kullanıcılar ve organizasyonlar tek namespace paylaşır) alınmış, bu yüzden paket scope'suz; npm organizasyonu
gerekmez, `NPM_TOKEN` sahibi yayınlar. OpenTelemetry scope adları `@openlog/node/runtime` ve `@openlog/node/console` değişmedi (saklanan
telemetri eşleşmeye devam eder). Go modüllerinden farklı olarak **hazırlık commit'i gerekmez**: npm derlenmiş dosyaları yayınlar.
`node-agent-package` `agents/node/scripts/release-pack.sh X.Y.Z` çalıştırır (`npm version X.Y.Z --no-git-tag-version`, `src/version.ts`
içine yazılır ve `telemetry.distro.version` olarak bildirilir; `npm ci`; `RUN_TESTS=0` değilse `npm test`; `npm pack`) ve
`node-agent-npm` (`release` job'undan sonra) tam olarak o tarball'ı, release asset'ini,
`npm publish --access public --provenance --tag <latest|beta>` ile yayınlar (`vX.Y.Z-beta.N` → dist-tag `beta`, böylece
`npm install openlog-node` en son stable sürüme çözülmeye devam eder). Zaten yayınlanmış bir sürüm atlanır (npm sürümleri değiştirilemez,
yani job'u yeniden çalıştırmak güvenlidir; bozuk bir sürüm `npm deprecate openlog-node@X.Y.Z "<reason>"` ile deprecate edilmeli ve sonraki
sürümde düzeltilmelidir). Sürümler arasında `agents/node/package.json` son yayınlanan sürümü tutar.

Kurulum: [tek seferlik kurulum](#one-time-setup-repository-owner) 6. adım (`NPM_TOKEN`). Paket içeriğinin yerel kontrolü:
`make -C agents/node build`, sonra `agents/node` içinde `npm pack --dry-run` (CI job'u da aynısını yapar).

### Python ajan paketi {#python-agent-package}

Python ajanı (`agents/python`, D-073) PyPI'a ürün sürümünde `openlog-agent` olarak yayınlanır (D-025). Hazırlık commit'i gerekmez:
`release.yml` job'u `python-agent-pypi` `release` job'undan sonra çalışır ve
- tag sürümünü PEP 440'a çevirir (`X.Y.Z-beta.N` → `X.Y.ZbN`, `-rc.N` → `rcN`; pip ön sürümleri yalnızca `--pre` ile kurar, yani
  `pip install openlog-agent` en son stable sürüme çözülmeye devam eder) ve `src/openlog_agent/version.py` içine yazar
  (`telemetry.distro.version` olarak bildirilir);
- ajanı test gereksinimleriyle Python 3.12 üzerine kurar, unit ve uçtan uca testleri çalıştırır, saf Python wheel ve sdist'i derler
  (`python -m build`) ve `twine check --strict` ile kontrol eder;
- `pypa/gh-action-pypi-publish` ile trusted publishing kullanarak yayınlar: job `id-token: write` ile GitHub environment'ı `pypi`
  içinde çalışır, PyPI OIDC token'ını kısa ömürlü bir upload token'ına çevirir, hiçbir secret saklanmaz. PyPI attestation'larını
  (PEP 740) action üretir.

Zaten yayınlanmış bir sürüm atlanır (PyPI sürümleri ve dosya adları değiştirilemez, yani yeniden çalıştırmak güvenlidir; bozuk bir sürüm
pypi.org'da *yank* edilir ve sonraki sürümde düzeltilir). Repository variable'ı `PYPI_PUBLISH=true` olmadan job yalnızca uyarır. Sürümler
arasında `agents/python/src/openlog_agent/version.py` son yayınlanan sürümü tutar.

Kurulum: [tek seferlik kurulum](#one-time-setup-repository-owner) 7. adım (PyPI'da pending trusted publisher, `pypi` environment'ı,
`PYPI_PUBLISH` variable'ı). Paketin yerel kontrolü: `make -C agents/python build` (wheel ve sdist `agents/python/dist/` içinde); CI job'u
`python-agent` her değişiklikte bunları derler ve kontrol eder.

### Java ajanı jar'ı {#java-agent-jar}

Java ajanı (`agents/java`, D-072) `openlog-javaagent-X.Y.Z.jar` artı `openlog-javaagent-X.Y.Z.jar.sha256` (`sha256sum` biçiminde) olarak
yayınlanır ve imzalı `manifest.json` içinde bileşen `java-agent`, format `jar`, `os`/`arch` `any` olarak listelenir
([releases-updates.md §2](/tr/contracts/releases-updates)). Hazırlık commit'i gerekmez. `release.yml` job'u `java-agent-jar`, `image` ve
`php-agent-artifacts` ile paralel, `release`'den önce çalışır:
- `JAVA_BUILD=local agents/java/scripts/release-jar.sh X.Y.Z dist/java`, `./gradlew -Pversion=X.Y.Z :extension:test :agentJar :agentJarChecksum`
  ile derler (sürüm `telemetry.distro.version` ve jar manifest'indeki `Openlog-Javaagent-Version` özelliği olur) ve `.sha256`'yı kontrol eder;
- iki dosyayı `java-agent` workflow artifact'i olarak yükler. `release` job'u bunları `make release-local`'dan önce `dist/v<v>/` içine
  indirir, böylece `build-manifest` jar'ın hash'ini alır, jar diğer her şeyle imzalanır, `verify --check-artifacts` kontrol eder ve iki dosya
  GitHub release ile yayınlanır. Jar sabit zaman damgaları ve dosya sırasıyla, yeniden üretilebilir şekilde derlenir.

`agents/java/gradle.properties` sürümler arasında `version=0.0.0-dev` tutar. Altyapı ajanının güncelleme hattı `java-agent` bileşenini yok
sayar (yalnızca `infra-agent` kurar); manifest kaydı, kullanıcıların ve araçların indirilen bir jar'ı gevşek `.sha256` dosyası yerine imzalı
sürüme göre doğrulamasını sağlar. CI job'u `release dry run` jar'ı aynı şekilde derler ve dry-run manifest'inde değilse başarısız olur. Maven
Central'a yayınlama (`io.github.onuragtas.openlog:openlog-javaagent`: Sonatype namespace'i, imzalama anahtarı, `maven-publish`) ileride yapılacak.

Jar ile yerel sürüm: `make release-php-agent` gibi, `make release-local VERSION=X.Y.Z …` öncesinde `make release-java-agent VERSION=X.Y.Z`
(`eclipse-temurin:21-jdk` içinde Gradle; `JAVA_BUILD=local` host JDK'sını kullanır).

### .NET ajan paketi {#net-agent-package}

.NET ajanı (`agents/dotnet`) nuget.org'a ürün sürümünde `OpenLog.Agent` olarak yayınlanır (D-025, D-074). Hazırlık commit'i gerekmez:
`release.yml` (`dotnet-agent-nuget`, `release` job'undan sonra) .NET 8 ve 9 SDK'larını kurar, unit testleri ve
`dotnet pack -p:OpenLogVersion=X.Y.Z` çalıştırır (paket sürümü, assembly informational version ve `telemetry.distro.version`), sonra
`dotnet nuget push --skip-duplicate` çalıştırır (`.snupkg` sembol paketi de onunla push edilir). `vX.Y.Z-beta.N` NuGet prerelease olur, böylece
`dotnet add package OpenLog.Agent` en son stable sürüme çözülmeye devam eder. NuGet sürümleri değiştirilemez: yeniden çalıştırma var olan sürümü
atlar, bozuk bir sürüm nuget.org'da unlist veya deprecate edilir ve sonraki sürümde düzeltilir. Sürümler arasında
`agents/dotnet/Directory.Build.props` son yayınlanan sürümü tutar.

Kurulum: [tek seferlik kurulum](#one-time-setup-repository-owner) 8. adım (`NUGET_API_KEY`). Paketin yerel kontrolü:
`make -C agents/dotnet pack VERSION=X.Y.Z` (CI job'u `dotnet-agent` her çalışmada paketler).

### Helm chart'ları {#helm-charts}

`make release-helm` (`release-local`'ın parçası) `deploy/helm/openlog` ve `deploy/helm/openlog-agent` dizinlerini
`helm package --version <v> --app-version <v>` ile `dist/v<v>/openlog-<v>.tgz` ve `openlog-agent-<v>.tgz` olarak paketler; repository'deki
`Chart.yaml` yer tutucu sürümünü korur. Yerelde `helm` yoksa sürümü sabitlenmiş `HELM_IMAGE` (`alpine/helm`) container'ını çalıştırır; CI helm'i
`azure/setup-helm` ile kurar ve `RELEASE_REQUIRE_HELM=1` ayarlar. `build-manifest` ikisini de imzalı manifest'e yazar (`helm_charts`, sha256;
`helm_chart` `openlog` chart'ı olarak kalır, [releases-updates.md](/tr/contracts/releases-updates) §2), `verify --check-artifacts` kontrol eder ve
`release.yml` bunları `dist/v<v>/` içindeki diğer tüm dosyalarla GitHub release'e yükler.

OCI registry (isteğe bağlı): repository *variable*'ı `HELM_OCI_PUSH` = `true` ayarla. `helm-oci` job'u bu durumda yayınlanmış sürümden alınan ve
manifest'iyle kontrol edilen iki chart'ı, job'un `GITHUB_TOKEN`'ı ile (`packages: write`; workflow izinleri
[tek seferlik kurulum](#one-time-setup-repository-owner) 3. adımdaki gibi) `oci://ghcr.io/onuragtas/charts` adresine push eder. Yeni GHCR paketleri
private'tır: ilk push'tan sonra `charts/openlog` ve `charts/openlog-agent` paketlerini public yap. Variable olmadan job yalnızca uyarır. Yeniden
çalıştırma aynı byte'ları tekrar push eder (OCI tag'lerinin üzerine yazılır).

```sh
helm install openlog oci://ghcr.io/onuragtas/charts/openlog --version 0.4.0 -n openlog --create-namespace -f my-values.yaml
helm install openlog-agent oci://ghcr.io/onuragtas/charts/openlog-agent --version 0.4.0 -n openlog-agent -f agent-values.yaml
# veya release asset'inden
helm install openlog https://github.com/onuragtas/openlog/releases/download/v0.4.0/openlog-0.4.0.tgz -n openlog -f my-values.yaml
```

## Bir sürümü doğrulamak {#verifying-a-release}

```sh
go build -o bin/openlog-release ./cmd/openlog-release
V=0.4.0; B=https://github.com/onuragtas/openlog/releases/download/v$V
mkdir -p /tmp/rel && cd /tmp/rel
for f in manifest.json manifest.json.sig index.json index.json.sig openlog-infra-agent_${V}_linux_amd64.tar.gz; do curl -fsSLO "$B/$f"; done
openlog-release verify --keys "<public key(s), comma-separated, or a key file>" manifest.json
openlog-release verify --keys "<…>" index.json
# manifest.json'un yanında bulunan her artifact'in boyutu/sha256'sı kontrol edilir; tam kontrol için hepsini indir
openlog-release verify --keys "<…>" --check-artifacts manifest.json
```

Sağlam bir sürümün çıktısı:

```
OK signature manifest.json (key 3f0c9a1b2c3d4e5f)
OK manifest 0.4.0 channel=stable artifacts=10
OK artifact openlog_0.4.0_linux_amd64.tar.gz 5e1d…
…
OK helm chart openlog-0.4.0.tgz 9a7c…
OK helm chart openlog-agent-0.4.0.tgz 41be…
```

Bir binary'nin hangi sürüm ve anahtarlarla derlendiğini kontrol et: `openlog-infra-agent -version`, `openlog-api -version`.

## Anahtar rotasyonu (iki aktif anahtar) {#key-rotation-two-active-keys}

Binary'ler, **herhangi bir** imza satırı **herhangi bir** derlenmiş anahtarla doğrulanıyorsa manifest'i kabul eder (en fazla 2 aktif anahtar).
Rotasyon, kurulu ajanları güvendikleri anahtarsız asla bırakmamalıdır:

1. İkinci anahtarı çevrimdışı üret (`openlog-release keygen`).
2. `OPENLOG_RELEASE_PUBLIC_KEYS=<key1>,<key2>` ayarla, `OPENLOG_RELEASE_SIGNING_KEY_2=<seed2>` secret'ını ekle ve ikinci anahtarı
   `release-public-keys.txt` dosyasına ekle (backend image'ının kaynaktan derlemeleri için varsayılan). Sürümler artık iki anahtarla imzalanır
   (`manifest.json.sig` iki satırdır) ve binary'ler ikisine de güvenir.
3. Sürüm çıkar ve fleet ikinci anahtara güvenen bir sürüme geçene kadar bekle (Fleet arayüzündeki sürüm dağılımı; rotasyonun başladığı sürümden eski
   ajanlar yalnızca birinci anahtara güvenir).
4. Değiştir: `OPENLOG_RELEASE_SIGNING_KEY=<seed2>`, `OPENLOG_RELEASE_SIGNING_KEY_2`'yi sil. Bir sürüm daha
   `OPENLOG_RELEASE_PUBLIC_KEYS=<key1>,<key2>` olarak tut, sonra yalnızca `<key2>` yap.
5. Birinci seed'i yok et.

**Ele geçirilmiş anahtar:** secret'ı hemen kaldır ve yukarıdaki gibi rotasyon yap, ama mümkün olduğunca beklemeyi atla: *iki* anahtarla imzalanmış
(yalnızca ele geçirilmiş anahtara güvenen ajanlara ulaşmak için o anahtar hâlâ gereklidir) ve yalnızca yeni anahtara güvenen bir sürüm yayınla, sonra eski
anahtarı bırak. Yalnızca eski anahtara güvenen bir sürümün ötesine hiç güncellenmemiş ajanların yeniden kurulması gerekir (`install.sh` veya paket).
Olayı duyur; fleet taşınana kadar fleet politikalarında `mode=off` düşün.

## Yerel imzalı sürümler (testler) {#local-signed-releases-tests}

Ajan self-update, fleet, updater ve e2e testleri kullanır. **Test anahtarları tek kullanımlıktır ve gerçek bir sürüm için asla kullanılmamalı,
gerçek bir sürüm onlara asla güvenmemelidir.**

```sh
# 1. dist/testkeys/ içinde tek kullanımlık anahtar çifti (git'te yok sayılır; yeniden çalıştırmada korunur)
make release-testkeys
#    dist/testkeys/public.key   base64 public key (geçerli bir trusted-keys dosyası da olur)
#    dist/testkeys/signing.key  base64 seed
#    dist/testkeys/release.env  ikisi de, KEY=value

# 2. imzalı sürüm; binary'ler dist/testkeys/public.key anahtarına güvenir
make release-local VERSION=0.9.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://127.0.0.1:18090
make release-local VERSION=0.10.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://127.0.0.1:18090

# 3. dist/ dizinini sun (index http://127.0.0.1:18090/index.json adresinde, dosyalar …/v0.10.0/<name> altında)
make release-serve        # dist/ içinde python3 -m http.server 18090
```

`RELEASE_TESTKEYS` olmadan eşdeğeri:

```sh
make release-local VERSION=0.9.0 \
  OPENLOG_RELEASE_SIGNING_KEY="$(cat dist/testkeys/signing.key)" \
  OPENLOG_RELEASE_PUBLIC_KEYS="$(cat dist/testkeys/public.key)" \
  RELEASE_BASE_URL=http://127.0.0.1:18090
```

Notlar:

- `RELEASE_BASE_URL` **sürümlerin kök adresidir**: artifact'ler `<root>/v<version>/<name>`, manifest'ler
  `<root>/v<version>/manifest.json` altındadır. *Tüketicinin* gördüğü host'u seç: Docker Desktop/OrbStack üzerindeki container'lar
  `http://host.docker.internal:18090` kullanır (Linux'ta `--add-host host.docker.internal:host-gateway` ekle); compose servisleri `dist/`
  dizinini kendileri sunabilir.
- `dist/index.json` her `dist/v*/manifest.json` dosyasını listeler (hepsi geçerli anahtarla doğrulanmalıdır; anahtarları yeniden ürettikten sonra
  `make release-clean` çalıştır). `dist/v<version>/index.json` bir kopyadır.
- Backend `Dockerfile`, build arg boşsa `release-public-keys.txt` içindeki resmi anahtarları kullanır. `OPENLOG_RELEASE_PUBLIC_KEYS` **olmadan**
  derlenen diğer binary'ler (`make build`, ajanlar) hiçbir şeye güvenmez; testler bunun yerine anahtar dosyasını verebilir: backend
  `OPENLOG_RELEASE_TRUSTED_KEYS_FILE=dist/testkeys/public.key`, ajan `release.trusted_keys_file`.
- **Geçerli imzalı ama bozuk sürüm** (otomatik rollback testleri): `VERSION=0.11.0` derle,
  `dist/v0.11.0/openlog-infra-agent_0.11.0_linux_<arch>.tar.gz` dosyasını bozuk bir arşivle (aynı düzende) değiştir, sonra manifest ve index'i yeniden
  oluşturup imzala:

  ```sh
  export OPENLOG_RELEASE_SIGNING_KEY="$(cat dist/testkeys/signing.key)"
  rm -f dist/v0.11.0/manifest.json.sig
  bin/openlog-release build-manifest --version 0.11.0 --dist dist/v0.11.0 --base-url http://127.0.0.1:18090/v0.11.0
  bin/openlog-release sign dist/v0.11.0/manifest.json
  make release-index VERSION=0.11.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://127.0.0.1:18090
  ```

  **Kurcalanmış artifact** (reddedilmelidir): tarball'ı `build-manifest`'i yeniden çalıştırmadan değiştir.
  **Bozuk imza** (reddedilmelidir): imzaladıktan sonra `manifest.json` içinde bir byte değiştir.
- İşe yarar değişkenler: `RELEASE_ARCHES=amd64` (daha hızlı), `HELM=/path/to/helm` (helm yoksa chart'lar sürümü sabitlenmiş `HELM_IMAGE` container'ında
  paketlenir; yalnızca Docker da yoksa uyarıyla atlanır), `NFPM=nfpm` (Docker yerine yerel nfpm), `RELEASE_CHANNEL`,
  `RELEASE_COMPAT`, `RELEASE_IMAGE`, `RELEASE_INDEX_ARGS="--entry 0.8.0=https://…/manifest.json"`.
- `make web` daha önce çalışmadıysa backend tarball'ları yer tutucu arayüzü gömer.
- Kontroller: `make package-test VERSION=0.9.0` (debian:12'de deb, rockylinux/rockylinux:9'da rpm, Docker host'unun mimarisi),
  `packaging/test/install.sh dist 0.9.0 0.10.0-beta.1` (debian:12, ubuntu:24.04, rockylinux/rockylinux:9, alpine üzerinde install.sh; index URL'leri için
  `RELEASE_BASE_URL=http://host.docker.internal:18090` ile derlenmiş bir sürüm gerekir).

## deb / rpm düzeni {#deb-rpm-layout}

| Yol | Sahip | Notlar |
|---|---|---|
| `/opt/openlog/infra-agent/versions/<v>/{openlog-infra-agent,LICENSE,README.md}` | `openlog-agent` | paket dosyaları; ajan diğer sürümleri kendisi ekler/temizler |
| `/opt/openlog/infra-agent/versions/<v>/manifest.json(.sig)` | `openlog-agent` | `<v>` sürümünün imzalı manifest'i (ajan `rollback_floor` değerini buradan okur). Paketlerde yalnızca ajan tarball'larını listeler, çünkü yayınlanan `manifest.json` paketlerin kendi sha256'sını içerir; sürüm, uyumluluk, image'lar ve `released_at` aynıdır. `install.sh` tarball kurulumları yayınlanan manifest'i saklar. |
| `/opt/openlog/infra-agent/current -> versions/<v>` | `openlog-agent` | postinstall tarafından oluşturulur (geçici symlink + `mv -T`) |
| `/usr/bin/openlog-infra-agent -> /opt/openlog/infra-agent/current/openlog-infra-agent` | root | paket symlink'i |
| `/usr/lib/systemd/system/openlog-infra-agent.service` | root | `agents/infra/packaging/systemd/` içinden |
| `/etc/openlog-infra-agent/config.yaml` | `root:openlog-agent 0640` | conffile (`config|noreplace`), üzerine asla yazılmaz |
| `/var/lib/openlog-infra-agent` | `openlog-agent 0750` | durum (`update-state.json`, buffer, host id) |

Maintainer betikleri (`packaging/scripts`, deb ve rpm ortak):

- **preinstall** `openlog-agent` sistem kullanıcısını/grubunu oluşturur.
- **postinstall** **çalışan sürüm daha yeni değilse** `current`'ı paketteki sürüme geçirir (ajan kendini paketin ötesine güncellemiş olabilir); ilk
  kurulumda unit'i etkinleştirir ve lisans anahtarı ayarlıysa başlatır; güncellemede `current` değiştiyse `try-restart` yapar.
- **preremove** (yalnızca kaldırmada) servisi durdurur ve devre dışı bırakır.
- **postremove** kaldırma: `current` ve tüm `versions/` dizinlerini siler (kendi indirilenler dahil); config ve durum kalır. **purge** (deb):
  `/etc/openlog-infra-agent`, `/var/lib/openlog-infra-agent` ve `/opt/openlog` dizinlerini de siler. Hesap korunur. rpm'de purge yok:
  `rm -rf /etc/openlog-infra-agent /var/lib/openlog-infra-agent`.

Paket güncellemesi önceki paketin `versions/<old>/` dosyalarını siler, yani paketle kurulmuş bir ajan rollback hedefi bir sonraki paket
güncellemesinde kaybolur (kendi indirilen sürümler etkilenmez).

## MSI ve macOS pkg (D-113) {#msi-and-macos-pkg-d-113}

`release.yml` tarafından native runner'larda derlenir (`infra-agent-windows`, `infra-agent-macos`) ve her `ci.yml` çalışmasında tek kullanımlık test
anahtarlarıyla (`infra-agent (Windows)`: `-ExpectManifest` ile amd64 MSI kurulum testi, yalnızca arm64 MSI derlemesi, uçtan uca self-update;
`infra-agent (macOS)`: pkg derlemesi, `test-pkg.sh`, uçtan uca self-update).

- **Gömülü manifest.** İki paket de kural 5 (rollback_floor) için `versions/<v>/manifest.json(.sig)` kurar. Onu
  `agents/infra/packaging/embedded-manifest.sh VERSION OUT_DIR ARCHIVE…` derler (yalnızca zip veya tar.gz arşivleri, Makefile'ın manifest argümanları
  `--image` olmadan, `OPENLOG_RELEASE_SIGNING_KEY` (+ `_2`) ile imzalanır ve `--check-artifacts` ile doğrulanır). Bu yüzden imzalama anahtarı iki native
  job'un "embedded manifest" adımlarında da kullanılabilir. Gömülü bir manifest yayınlanandan `artifacts` ve `images` dışında herhangi bir şeyde
  farklıysa `release` job'u başarısız olur.
- **MSI.** `build-msi.ps1 -Version V -Arch amd64|arm64 -Exe … -SourceDir … -Out … -ManifestDir …` (WiX 5: `-arch x64|arm64`, util custom action'ları
  `Wix4UtilCA_X64`/`Wix4UtilCA_A64`; exe'nin PE makine tipi `-Arch` ile eşleşmelidir). Authenticode imzalama `WINDOWS_SIGNING_CERT_*` secret'larıyla yapılır.
  arm64 MSI'ın kurulum testi `windows-latest` (x64) üzerinde yapılamaz.
- **pkg.** `build-pkg.sh VERSION ARCH STAGE_DIR OUT.pkg [MANIFEST_DIR]` (pkgbuild bileşen paketi + productbuild distribution, ARCH için
  `hostArchitectures`, macOS 12+). Payload `/opt/openlog/infra-agent/versions/<v>/` (root:wheel). Postinstall betiği ajanı yalnızca
  `/etc/openlog-infra-agent/config.yaml` varsa veya kimlik bilgileri verilmişse yapılandırır, sonra `-reconcile -reconcile-context package` çalıştırır ve
  lisans anahtarı ayarlıysa LaunchDaemon'u (yeniden) başlatır. Kimlik bilgileri: `sudo OPENLOG_LICENSE_KEY=… OPENLOG_ENDPOINT=… installer -pkg …` güvenilir
  değildir (sudo ve installer ortamı süzer), bu yüzden bunları root'a ait, group/other tarafından yazılamayan `/tmp/openlog-infra-agent.env` dosyasına yaz
  (satırlar `OPENLOG_LICENSE_KEY=…`, `OPENLOG_ENDPOINT=…`; okunduktan sonra silinir) veya önce yapılandırmayı oluştur. İmzalama
  `APPLE_DEVELOPER_ID_INSTALLER_CERT_P12_BASE64` / `APPLE_DEVELOPER_ID_INSTALLER_CERT_PASSWORD` secret'larıyla yapılır; `APPLE_NOTARY_*` secret'larıyla imzalı pkg
  notarize ve staple edilir. Bunlar olmadan pkg imzasızdır (uyarı): `installer -pkg` çalışır, Finder/Gatekeeper çift tıklamayı reddeder.

```sh
sudo sh -c 'umask 077; printf "OPENLOG_LICENSE_KEY=KEY\nOPENLOG_ENDPOINT=https://ingest.example.com:4318\n" > /tmp/openlog-infra-agent.env'
sudo installer -pkg openlog-infra-agent_<v>_darwin_arm64.pkg -target /
# kaldırma
sudo openlog-infra-agent -uninstall-service && sudo rm -rf /opt/openlog/infra-agent /usr/local/bin/openlog-infra-agent && sudo pkgutil --forget org.openlog.infra-agent
```

## install.sh {#install-sh}

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
  sudo sh -s -- --license-key KEY --endpoint https://ingest.example.com:4318
```

Flag'ler: `--license-key`, `--endpoint`, `--version` (varsayılan: `index.json`'daki kanalın en yenisi), `--channel stable|beta`,
`--method auto|deb|rpm|tarball` (auto: Debian/Ubuntu'da deb, RHEL benzerleri/SUSE/Amazon Linux'ta rpm, başka yerde tarball, örneğin Alpine), `--base-url`
(mirror veya yerel sürüm kökü; env `OPENLOG_RELEASE_BASE_URL`), `--index-url`, `--no-start`. Ortam değişkeni karşılıkları:
`OPENLOG_LICENSE_KEY`, `OPENLOG_ENDPOINT`, `OPENLOG_VERSION`, `OPENLOG_CHANNEL`, `OPENLOG_INSTALL_METHOD`.

**Güven modeli.** Kurulum betiğinin kendisi, `index.json` ve `manifest.json` **HTTPS** üzerinden alınır (bootstrap, her `curl | sh` gibi TLS'e ve GitHub'a
güvenir). İndirilen paket manifest'teki **boyut ve sha256** ile kontrol edilir; betik Ed25519 imzasını doğrulamaz (POSIX sh'ta Ed25519 yok). **Sonraki** her
güncellemeyi ajan uygular, sürüm değiştirmeden önce manifest imzasını derlenmiş anahtarlarıyla ve artifact hash'ini doğrular. Tamamen doğrulanmış bir
bootstrap isteyen kullanıcılar sürüm dosyalarını indirir ve paketi kurmadan önce `openlog-release verify --check-artifacts` çalıştırır.

Yeniden çalıştırmak idempotent'tir: aynı sürüm → yalnızca config/servis güncellenir; daha yeni sürüm → güncelleme; kendini istenen sürümün ötesine
güncellemiş bir ajan, `--version` açıkça verilmedikçe korunur. systemd olmadan (container'lar, Alpine/OpenRC) ajan kurulur ve yapılandırılır ama yerel servis
yöneticisi tarafından başlatılmalıdır.

## install-server.sh {#install-server-sh}

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install-server.sh |
  sudo sh -s -- --email you@example.com
```

`single` Compose profilini `/opt/openlog-server` altına `OPENLOG_IMAGE=ghcr.io/onuragtas/openlog:<v>` ile ve `openlog-updater`'ı `auto` modunda kurar (flag'ler:
README "Quick install"). `make release-local` onu `install.sh`'ın yanına kopyalar; `install.sh` gibi manifest'te yoktur.

Compose dosyaları `openlog-compose-<v>.tar.gz` release asset'inden gelir (manifest bileşeni `compose`, git'te izlenen `deploy/compose` üzerinden
`make release-compose` ile `x-openlog-compose-version` damgalanarak derlenir): betik sürümün `manifest.json` dosyasını okur (index kaydındaki `manifest_url`, yoksa
`…/releases/download/v<v>/manifest.json`), asset'i `url` değerinden indirir ve `sha256`'yı kontrol eder. Asset'i olmayan bir sürüm (0.1.22'den eski), tag'in
kaynak arşivindeki `deploy/compose/` dizinine döner (`https://github.com/onuragtas/openlog/archive/refs/tags/v<v>.tar.gz`). `--bundle-url` onu bir mirror'a yöneltir
(`openlog-compose-<v>/` veya `deploy/compose/` içeren bir `tar.gz`). Güven: bu bootstrap için HTTPS (manifest imzasını shell betiği kontrol etmez); `openlog-updater`
imzalı manifest'i doğrular ve dosyaları kurulu sürümde tutar (`.bundle-version`, docs/operations/upgrading.md "Compose files"). `make release-prepare` de repository'de
`x-openlog-compose-version` damgalar ve `go-agent-release.sh check` bunu zorunlu kılar, yani tag'in bir clone'u da sürümünü bildirir. Compose dosyasının `build:` bölümü
kullanılmaz (release image'ı çekilir, asla derlenmez).

`.env` içindeki port ayarları `HOST:PORT` olabilir (`OPENLOG_ADMIN_PORT=127.0.0.1:9464`): hazırlık ve port kontrolleri bağlanılan adrese bağlanır, URL'ler yalnızca
portu kullanır. `packaging/test/install-server-ports.sh` bu yardımcıları Docker olmadan kontrol eder.

Test: `packaging/test/install-server.sh [VERSION]` betiği yetkili bir `docker:28-dind` container'ında yayınlanmış sürüme karşı çalıştırır (temiz kurulum, giriş,
idempotent yeniden çalıştırma, en son sürüme güncelleme, reddedilen sürüm düşürme). İnternet erişimi gerekir ve CI'ın parçası değildir.
