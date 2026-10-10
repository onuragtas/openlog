// ignore: unused_import
import 'package:intl/intl.dart' as intl;
import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for Turkish (`tr`).
class LTr extends L {
  LTr([String locale = 'tr']) : super(locale);

  @override
  String get appTitle => 'openlog';

  @override
  String get serverTitle => 'openlog\'a bağlan';

  @override
  String get serverDescription =>
      'openlog kendi sunucunuzda çalışır. Aşağıdaki adres barındırılan kurulumdur — kendi sunucunuzu çalıştırıyorsanız onunla değiştirin.';

  @override
  String get serverAddressLabel => 'Sunucu adresi';

  @override
  String get serverContinue => 'Devam et';

  @override
  String get serverChecking => 'Kontrol ediliyor…';

  @override
  String get serverInvalidAddress => 'Bu bir adrese benzemiyor.';

  @override
  String serverUnreachable(String address) {
    return '$address adresine ulaşılamıyor. Adresi ve bağlantınızı kontrol edin.';
  }

  @override
  String get serverNotOpenlog =>
      'Bu adres yanıt verdi ama bir openlog sunucusu gibi değil.';

  @override
  String get serverChange => 'Sunucuyu değiştir';

  @override
  String get signInTitle => 'openlog\'a giriş yap';

  @override
  String get signInDescription =>
      'openlog hesabınızın e-posta adresini ve parolasını kullanın.';

  @override
  String get emailLabel => 'E-posta';

  @override
  String get emailHint => 'siz@ornek.com';

  @override
  String get passwordLabel => 'Parola';

  @override
  String get signInSubmit => 'Giriş yap';

  @override
  String get signInChecking => 'Giriş yapılıyor…';

  @override
  String get signInInvalid => 'E-posta veya parola hatalı.';

  @override
  String get signInRateLimited =>
      'Çok fazla başarısız deneme. Birkaç dakika bekleyip tekrar deneyin.';

  @override
  String get signInRequired => 'E-posta ve parola gerekli.';

  @override
  String get signInStaticMode =>
      'Bu sunucu OPENLOG_AUTH_MODE=static ile çalışıyor ve kullanıcı hesabı yok.';

  @override
  String get deviceNameLabel => 'Cihaz adı';

  @override
  String get deviceNameHelp =>
      'Oturum listenizde görünür; bu cihazı diğerlerinden ayırmanıza ve çıkış yapmanıza yarar.';

  @override
  String get deviceNameRequired => 'Cihaz adı gerekli.';

  @override
  String get signUpPrompt => 'Hesabınız yok mu?';

  @override
  String get signUpLink => 'Hesap oluştur';

  @override
  String get signUpTitle => 'openlog hesabınızı oluşturun';

  @override
  String get signUpDescription =>
      'Yeni bir organizasyon başlatın. Sahibi siz olursunuz ve ekibinizi davet edebilirsiniz.';

  @override
  String get nameLabel => 'Adınız';

  @override
  String get orgLabel => 'Organizasyon adı';

  @override
  String get orgHint => 'ör. Acme A.Ş.';

  @override
  String passwordHintMin(int min) {
    return 'En az $min karakter, e-posta adresiniz olamaz.';
  }

  @override
  String get signUpSubmit => 'Hesap oluştur';

  @override
  String get signUpSubmitting => 'Hesap oluşturuluyor…';

  @override
  String get signUpRequired => 'Organizasyon adı, e-posta ve parola gerekli.';

  @override
  String get signUpDisabled =>
      'Bu sunucuda kayıt kapalı. Organizasyonunuzun bir yöneticisinden davet isteyin.';

  @override
  String get signUpEmailTaken =>
      'Bu e-posta adresiyle bir hesap zaten var. Giriş yapın.';

  @override
  String get signUpRateLimited =>
      'Çok fazla kayıt denemesi. Daha sonra tekrar deneyin.';

  @override
  String signUpOnWeb(String address) {
    return 'Bu sunucu kayıt için CAPTCHA istiyor ve uygulama onu gösteremiyor. Hesabı bir tarayıcıda $address adresinden oluşturup buradan giriş yapın.';
  }

  @override
  String get signUpVerificationNote =>
      'Adresinizi doğrulamanız için e-postanıza bir bağlantı göndereceğiz.';

  @override
  String get haveAccount => 'Zaten hesabınız var mı?';

  @override
  String get toSignIn => 'Giriş yap';

  @override
  String homeSignedInAs(String email) {
    return '$email olarak giriş yapıldı';
  }

  @override
  String get homeOrganization => 'Organizasyon';

  @override
  String get homeRole => 'Rol';

  @override
  String get homeSignOut => 'Çıkış yap';

  @override
  String get homeNextPhase =>
      'Alarmlar, servisler ve loglar bir sonraki fazda geliyor.';

  @override
  String get homeSwitchOrganization => 'Organizasyon değiştir';

  @override
  String get roleOwner => 'Sahip';

  @override
  String get roleAdmin => 'Yönetici';

  @override
  String get roleMember => 'Üye';

  @override
  String get roleViewer => 'Görüntüleyici';

  @override
  String get roleUnknown => 'Bilinmeyen rol';

  @override
  String get retry => 'Tekrar dene';

  @override
  String get cancel => 'İptal';

  @override
  String errorUnexpected(String detail) {
    return 'Bir şeyler ters gitti: $detail';
  }

  @override
  String get alertsTitle => 'Alarmlar';

  @override
  String get alertsEmpty => 'Açık alarm yok.';

  @override
  String get alertsEmptyHint => 'Bir kural tetiklendiği anda burada görünür.';

  @override
  String get alertsForbidden => 'Rolünüz alarmları görmeye izin vermiyor.';

  @override
  String get alertsAlreadyResolved => 'Bu alarm üstlenilmeden önce çözüldü.';

  @override
  String get alertsAcknowledge => 'Üstlen';

  @override
  String alertsAcknowledgedBy(String email) {
    return '$email üstlendi';
  }

  @override
  String get alertsAcknowledgedUnknown => 'Üstlenildi';

  @override
  String alertsCounts(int open, int acknowledged) {
    return '$open açık, $acknowledged onaylanmış';
  }

  @override
  String alertsResolvedRecently(int count) {
    return 'Son 7 günde $count çözüldü';
  }

  @override
  String get alertsMuted => 'Susturulmuş';

  @override
  String get alertsFlapping => 'Kararsız';

  @override
  String alertsOpened(String when) {
    return '$when açıldı';
  }

  @override
  String get severityCritical => 'Kritik';

  @override
  String get severityWarning => 'Uyarı';

  @override
  String get severityInfo => 'Bilgi';

  @override
  String get severityUnknown => 'Bilinmeyen önem';

  @override
  String get refresh => 'Yenile';

  @override
  String get justNow => 'az önce';

  @override
  String minutesAgo(int count) {
    return '$count dk önce';
  }

  @override
  String hoursAgo(int count) {
    return '$count sa önce';
  }

  @override
  String daysAgo(int count) {
    return '$count gün önce';
  }

  @override
  String get navAlerts => 'Alarmlar';

  @override
  String get navLogs => 'Loglar';

  @override
  String get servicesEmpty => 'Son bir saatte hiçbir servis bildirim yapmadı.';

  @override
  String get servicesSearch => 'Servis ara';

  @override
  String get servicesForbidden => 'Rolünüz servisleri görmeye izin vermiyor.';

  @override
  String get svcThroughput => 'ist/dk';

  @override
  String get svcErrorRate => 'hata';

  @override
  String get svcP95 => 'p95';

  @override
  String get svcApdex => 'Apdex';

  @override
  String get svcNoData => '—';

  @override
  String get logsEmpty => 'Eşleşen log kaydı yok.';

  @override
  String get logsSearch => 'Mesaj içinde ara';

  @override
  String get logsForbidden => 'Rolünüz logları görmeye izin vermiyor.';

  @override
  String get logsSeverity => 'Önem';

  @override
  String get logsSeverityAll => 'Hepsi';

  @override
  String get logsNoService => 'servis yok';

  @override
  String get navDashboards => 'Panolar';

  @override
  String get dashboardsEmpty => 'Henüz pano yok.';

  @override
  String get dashboardsSearch => 'Pano ara';

  @override
  String get dashboardsForbidden => 'Rolünüz panoları görmeye izin vermiyor.';

  @override
  String dashboardWidgets(int count, int pages) {
    return '$pages sayfada $count bileşen';
  }

  @override
  String get dashboardNoQuery => 'Çalıştırılacak sorgu yok';

  @override
  String get dashboardWidgetFailed => 'Bu bileşenin sorgusu yanıt vermedi.';

  @override
  String get dashboardOnWeb => 'En iyi web\'de okunur';

  @override
  String get dashboardNoData => 'Veri yok';

  @override
  String get dashboardLoading => 'Sorgular çalışıyor…';

  @override
  String get navApm => 'APM';

  @override
  String get navSettings => 'Ayarlar';

  @override
  String get navMain => 'Ana gezinme';

  @override
  String get settingsAccount => 'Hesap';

  @override
  String get closeMenu => 'Menüyü kapat';

  @override
  String get navHosts => 'Sunucular';

  @override
  String get navContainers => 'Konteynerler';

  @override
  String get navKubernetes => 'Kubernetes';

  @override
  String get navDatabases => 'Veritabanları';

  @override
  String get navSlos => 'SLO\'lar';

  @override
  String get navSynthetics => 'Sentetik izleme';

  @override
  String get navJobs => 'İş izleme';

  @override
  String get navVulnerabilities => 'Güvenlik açıkları';

  @override
  String get sectionForbidden => 'Rolünüz bu bölümü görmeye izin vermiyor.';

  @override
  String get sectionSearch => 'Ara';

  @override
  String get hostsEmpty => 'Bildirim yapan sunucu yok.';

  @override
  String get containersEmpty => 'Bildirim yapan konteyner yok.';

  @override
  String get podsEmpty => 'Pod bulunamadı.';

  @override
  String get databasesEmpty => 'Bildirim yapan veritabanı örneği yok.';

  @override
  String get slosEmpty => 'Tanımlı SLO yok.';

  @override
  String get syntheticsEmpty => 'Tanımlı sentetik kontrol yok.';

  @override
  String get jobsEmpty => 'Tanımlı iş izleyici yok.';

  @override
  String get vulnerabilitiesEmpty => 'Güvenlik açığı bulunamadı.';

  @override
  String get statCpu => 'CPU';

  @override
  String get statMemory => 'Bellek';

  @override
  String get statDisk => 'Disk';

  @override
  String get statRestarts => 'yeniden başlatma';

  @override
  String get statUptime => 'çalışma';

  @override
  String get statRuns => 'koşu';

  @override
  String get statFailures => 'hata';

  @override
  String get statBudget => 'kalan bütçe';

  @override
  String get statObjective => 'hedef';

  @override
  String get statScore => 'skor';

  @override
  String get statHosts => 'sunucu';

  @override
  String get statCalls => 'çağrı';

  @override
  String get stateDisabled => 'Kapalı';

  @override
  String get stateNotReporting => 'Bildirim yok';

  @override
  String get stateReady => 'Hazır';

  @override
  String get sloMet => 'Karşılandı';

  @override
  String get sloBreached => 'Aşıldı';

  @override
  String get detailGone => 'Bu kayıt artık sunucuda yok.';

  @override
  String get incidentTimeline => 'Zaman çizelgesi';

  @override
  String get incidentNoEvents => 'Bu olayla ilgili henüz bir şey olmadı.';

  @override
  String get incidentDeliveries => 'Bildirimler';

  @override
  String get incidentNoDeliveries => 'Bu olay için bir bildirim gönderilmedi.';

  @override
  String get incidentLabels => 'Etiketler';

  @override
  String get incidentValue => 'Değer';

  @override
  String get incidentThreshold => 'Eşik';

  @override
  String incidentOpenService(String name) {
    return '$name servisini aç';
  }

  @override
  String get incidentResolve => 'Çöz';

  @override
  String get incidentResolveHint =>
      'Eşiği aşmaya devam eden bir seri yeni bir olay açar; bu bir susturma değildir.';

  @override
  String incidentResolvedBy(String email) {
    return '$email çözdü';
  }

  @override
  String incidentResolved(String when) {
    return '$when çözüldü';
  }

  @override
  String get incidentNote => 'Not ekle';

  @override
  String get incidentNoteHint => 'Ne buldunuz';

  @override
  String get incidentNoteSend => 'Gönder';

  @override
  String get incidentEventOpened => 'Açıldı';

  @override
  String get incidentEventFlapping => 'Kararsız';

  @override
  String get incidentEventAcknowledged => 'Üstlenildi';

  @override
  String get incidentEventNote => 'Not';

  @override
  String get incidentEventRenotified => 'Yeniden bildirildi';

  @override
  String get incidentEventResolved => 'Çözüldü';

  @override
  String get incidentEventDelivered => 'Bildirim iletildi';

  @override
  String get incidentEventFailed => 'Bildirim başarısız';

  @override
  String get incidentEventSuppressed => 'Bildirim bastırıldı';

  @override
  String get incidentEventMuted => 'Bildirim sessize alındı';

  @override
  String get incidentEventUnknown => 'Uygulamanın tanımadığı olay';

  @override
  String get serviceSignals => 'Altın sinyaller';

  @override
  String get serviceRequests => 'İstekler';

  @override
  String get serviceErrors => 'Hatalar';

  @override
  String get serviceLatency => 'Gecikme';

  @override
  String get serviceNoData => 'Bu servis bu aralıkta hiç rapor etmedi.';

  @override
  String get serviceThroughputChart => 'Dakikadaki istek';

  @override
  String get serviceErrorRateChart => 'Hata oranı';

  @override
  String get deliveryPending => 'Beklemede';

  @override
  String get deliverySending => 'Gönderiliyor';

  @override
  String get deliveryDelivered => 'İletildi';

  @override
  String get deliveryFailed => 'Başarısız';

  @override
  String get deliverySuppressed => 'Bastırıldı';

  @override
  String get deliveryUnknown => 'Bilinmeyen durum';

  @override
  String deliveryAttempts(int count) {
    return '$count deneme';
  }

  @override
  String get serviceTabOverview => 'Genel bakış';

  @override
  String get serviceTabErrors => 'Hatalar';

  @override
  String get errorsEmpty => 'Bu süzgeçle hata yok.';

  @override
  String errorsOccurrences(int count) {
    return '$count kez';
  }

  @override
  String errorsLastSeen(String when) {
    return 'son $when';
  }

  @override
  String get errorStatusUnresolved => 'Çözülmedi';

  @override
  String get errorStatusResolved => 'Çözüldü';

  @override
  String get errorStatusIgnored => 'Yoksayıldı';

  @override
  String get errorStatusUnknown => 'Bilinmeyen durum';

  @override
  String get errorsNoTrace => 'Bu hata için iz saklanmamış.';

  @override
  String get errorsTruncated =>
      'Eşleşen grupların tamamı değil; süzgeci daraltın.';

  @override
  String get traceTitle => 'İz';

  @override
  String traceSpans(int count) {
    return '$count span';
  }

  @override
  String get traceEmpty => 'Bu izde span yok.';

  @override
  String get traceRoot => 'kök';

  @override
  String get navQuery => 'Sorgu';

  @override
  String get queryHint => 'SELECT count(*) FROM logs SINCE 1 hour ago';

  @override
  String get queryRun => 'Çalıştır';

  @override
  String get queryEmpty => 'Bir sorgu yazıp Çalıştır\'a basın.';

  @override
  String get queryForbidden => 'Rolünüz sorgu çalıştırmaya izin vermiyor.';

  @override
  String queryRejected(String detail) {
    return 'Sunucu bunu çalıştırmadı: $detail';
  }

  @override
  String queryMeta(int rows, int ms) {
    return '$rows satır okundu, $ms ms';
  }

  @override
  String get queryRecent => 'Son sorgular';

  @override
  String get queryTruncated => 'Yanıt sunucunun sınırıyla kısaldı.';

  @override
  String get navTraces => 'İzler';

  @override
  String get tracesEmpty => 'Bu aralıkta izlenmiş istek yok.';

  @override
  String get tracesNewest => 'En yeni';

  @override
  String get tracesSlowest => 'En yavaş';

  @override
  String get tracesError => 'hata';

  @override
  String get navMetrics => 'Metrikler';

  @override
  String get metricsEmpty => 'Bu aralıkta rapor eden metrik yok.';

  @override
  String metricSeriesCount(int count) {
    return '$count seri';
  }

  @override
  String metricNoChart(String detail) {
    return 'Seri çizilemedi: $detail';
  }

  @override
  String get metricNoPoints => 'Bu metriğin bu aralıkta noktası yok.';

  @override
  String get metricAttributes => 'Öznitelikler';

  @override
  String get metricResourceKeys => 'Kaynak anahtarları';

  @override
  String get metricTruncated => 'Grafiğin gösterdiğinden fazla seri var.';

  @override
  String metricOneSeries(int count) {
    return '$count serinin biri.';
  }

  @override
  String get navRum => 'Tarayıcı';

  @override
  String get rumEmpty => 'Bu aralıkta rapor eden tarayıcı uygulaması yok.';

  @override
  String get rumViews => 'Sayfa görüntüleme';

  @override
  String get rumSessions => 'Oturum';

  @override
  String get rumErrors => 'Hata';

  @override
  String get rumAvgLoad => 'Ortalama yükleme';

  @override
  String get rumVitals => 'Temel Web Verileri';

  @override
  String get rumVitalGood => 'iyi';

  @override
  String get rumVitalNeedsImprovement => 'iyileştirilmeli';

  @override
  String get rumVitalPoor => 'kötü';

  @override
  String get rumVitalNoData => 'ölçüm yok';

  @override
  String rumVitalShare(int percent) {
    return '%$percent iyi';
  }

  @override
  String get rumNoPoints => 'Bu aralıkta sayfa görüntüleme yok.';

  @override
  String get navCosts => 'Maliyet';

  @override
  String get costsOff => 'Bu kurulum maliyet tahmini yapmıyor.';

  @override
  String get costsTotal => 'Toplam';

  @override
  String get costsPerHour => 'Saatlik';

  @override
  String get costsIdle => 'Atıl';

  @override
  String get costsHosts => 'Sunucu';

  @override
  String costsEstimate(String updated, String note) {
    return 'Fatura değil, fiyat tablosundan tahmin. Fiyatlar $updated tarihinde toplandı. $note';
  }

  @override
  String costsUnpriced(int count) {
    return '$count sunucunun fiyatı yok.';
  }

  @override
  String get costsHostsTitle => 'En pahalı sunucular';

  @override
  String costsUsed(int percent) {
    return '%$percent kullanımda';
  }

  @override
  String get costsNoPrice => 'fiyat yok';

  @override
  String costsMoreHosts(int count) {
    return 've $count tane daha';
  }

  @override
  String get navInventory => 'Envanter arama';

  @override
  String get inventoryEmpty => 'Bu kategoride eşleşen yok.';

  @override
  String get inventoryHint =>
      'Tüm sunucularda öğe bulun — ör. hangi sunucularda openssl var?';

  @override
  String get inventoryCategory => 'Kategori';

  @override
  String get inventorySearch => 'Anahtar içerir';

  @override
  String get invOs => 'İşletim sistemi';

  @override
  String get invHardware => 'Donanım';

  @override
  String get invPackage => 'Paketler';

  @override
  String get invProcess => 'Süreçler';

  @override
  String get invListeningPort => 'Dinlenen portlar';

  @override
  String get invSystemdUnit => 'systemd birimleri';

  @override
  String get invKernelModule => 'Çekirdek modülleri';

  @override
  String get invNetworkInterface => 'Ağ arayüzleri';

  @override
  String get invMount => 'Bağlama noktaları';

  @override
  String get invUser => 'Kullanıcılar';

  @override
  String get invLaunchdService => 'launchd servisleri';

  @override
  String get invWindowsService => 'Windows servisleri';

  @override
  String get invDiscoveredService => 'Keşfedilen servisler';

  @override
  String get navFleet => 'Filo';

  @override
  String get fleetEmpty => 'Rapor eden ajan yok.';

  @override
  String get fleetAgents => 'Ajan';

  @override
  String get fleetOutdated => 'Eski';

  @override
  String get fleetInProgress => 'Güncelleniyor';

  @override
  String get fleetFailed => 'Başarısız';

  @override
  String fleetLatest(String version) {
    return 'En yeni $version';
  }

  @override
  String get fleetNoCatalog =>
      'Sürüm kataloğu yok; sunucu en yenisinin hangisi olduğunu bilemiyor.';

  @override
  String get fleetReadOnly =>
      'Burada salt okunur. Dağıtım ve politika webden değiştirilir.';

  @override
  String get fleetUnsupported => 'desteklenmiyor';

  @override
  String fleetStatePrefix(String state) {
    return 'güncelleme $state';
  }

  @override
  String get fleetStateIdle => 'boşta';

  @override
  String get fleetStateDownloading => 'indiriliyor';

  @override
  String get fleetStateVerifying => 'doğrulanıyor';

  @override
  String get fleetStateStaged => 'hazır';

  @override
  String get fleetStateRestarting => 'yeniden başlıyor';

  @override
  String get fleetStateConfirming => 'onaylanıyor';

  @override
  String get fleetStateSucceeded => 'başarılı';

  @override
  String get fleetStateFailed => 'başarısız';

  @override
  String get fleetStateRolledBack => 'geri alındı';

  @override
  String get navIntegrations => 'Entegrasyonlar';

  @override
  String get integrationsEmpty => 'Ajanlar bir entegrasyon keşfetmedi.';

  @override
  String get integrationsSearch => 'Entegrasyon ara';

  @override
  String get integEnabled => 'topluyor';

  @override
  String get integNeedsConfig => 'yapılandırma gerek';

  @override
  String get integError => 'hata';

  @override
  String get integNotAvailable => 'kullanılamıyor';

  @override
  String integCounts(int enabled, int needs, int error) {
    return '$enabled topluyor, $needs yapılandırma bekliyor, $error hatalı';
  }

  @override
  String get integConfigureOnWeb =>
      'Yapılandırma webden yapılır; bu ekran ajanların bildirdiğini okur.';

  @override
  String get navProfiles => 'Profilleme';

  @override
  String get profilesEmpty => 'Bu aralıkta profillenen bir şey yok.';

  @override
  String get profilesSearch => 'Servis ve tür ara';

  @override
  String profileSamples(int count) {
    return '$count örnek';
  }

  @override
  String get profileFunctions => 'Kendi süresine göre';

  @override
  String get profileNoFunctions =>
      'Bu profilin yükünü taşıyan bir fonksiyon yok.';

  @override
  String profileShare(String percent) {
    return '%$percent';
  }

  @override
  String get profileOfShown =>
      'Oranlar gösterilen satırlara göre, tüm aralığa göre değil.';

  @override
  String get navAddData => 'Veri ekle';

  @override
  String get addDataOtlpHttp => 'HTTP üzerinden OTLP';

  @override
  String get addDataOtlpGrpc => 'gRPC üzerinden OTLP';

  @override
  String get addDataCopied => 'Kopyalandı';

  @override
  String get addDataVersions => 'Sürümler';

  @override
  String addDataServer(String version) {
    return 'Sunucu $version';
  }

  @override
  String addDataAgent(String version, String channel) {
    return 'Ajanlar $version sürümüne sabitli ($channel)';
  }

  @override
  String get addDataAgentDev =>
      'Geliştirme derlemesi; komutlar bir sürüme sabitlemiyor.';

  @override
  String get addDataBrowser => 'Tarayıcı verisi';

  @override
  String addDataCorsOn(String origins) {
    return 'İzin verilenler: $origins';
  }

  @override
  String get addDataCorsOff =>
      'Yapılandırılmamış, yani tarayıcı bu sunucuya gönderemez.';

  @override
  String get addDataSourcesOnWeb =>
      'Veri kaynakları ve kurulum komutları webde; bu ekranda bir şeyi bu sunucuya yöneltmek için gerekenler var.';

  @override
  String get addDataEndpointDerived =>
      'Yapılandırılmamış, türetilmiş — dışarıdan erişilebildiğini doğrulayın.';

  @override
  String get onboardingForbidden => 'Rolünüz bunu görmeye izin vermiyor.';

  @override
  String get hostRuns => 'Burada koşanlar';

  @override
  String get hostNoServices =>
      'Ajan bu sunucu için henüz bir anlık görüntü bildirmedi.';

  @override
  String hostServicesFailed(String detail) {
    return 'Burada ne koştuğu okunamadı: $detail';
  }

  @override
  String get hostNothingFound => 'Ajan tanıdığı bir şey bulamadı.';

  @override
  String get hostCpu => 'İşlemci';

  @override
  String get hostMemory => 'Bellek';

  @override
  String get hostDisk => 'Disk';

  @override
  String get hostLoad => 'Yük';

  @override
  String hostAgent(String version) {
    return 'Ajan $version';
  }

  @override
  String get hostAttributes => 'Kaynak öznitelikleri';

  @override
  String get containerCpu => 'İşlemci';

  @override
  String get containerMemory => 'Bellek';

  @override
  String get containerNetwork => 'Ağ';

  @override
  String get containerDisk => 'Blok G/Ç';

  @override
  String get containerNoSeries => 'Bu aralıkta örnek alınmamış.';

  @override
  String containerSeriesFailed(String detail) {
    return 'Grafikler okunamadı: $detail';
  }

  @override
  String get containerNoLimit =>
      'Bellek sınırı yok, gösterilecek bir oran da yok.';

  @override
  String containerRestarts(int count) {
    return '$count yeniden başlatma';
  }

  @override
  String get containerImage => 'İmaj';

  @override
  String get containerAttributes => 'Öznitelikler';

  @override
  String containerOn(String host) {
    return '$host üzerinde';
  }

  @override
  String get containerRxTx => 'gelen / giden';

  @override
  String get containerReadWrite => 'okuma / yazma';

  @override
  String get podContainers => 'Konteynerler';

  @override
  String get podEvents => 'Olaylar';

  @override
  String get podNoEvents => 'Bu pod hakkında olay yok.';

  @override
  String podEventsFailed(String detail) {
    return 'Olaylar okunamadı: $detail';
  }

  @override
  String get podLabels => 'Etiketler';

  @override
  String get podServices => 'Servisler';

  @override
  String get podRestartsLabel => 'Yeniden başlatma';

  @override
  String get podNode => 'Düğüm';

  @override
  String get podReady => 'hazır';

  @override
  String get podNotReady => 'hazır değil';

  @override
  String podEventCount(int count) {
    return '$count×';
  }

  @override
  String get podCpu => 'İşlemci';

  @override
  String get podMemory => 'Çalışma kümesi';

  @override
  String get navRules => 'Alarm kuralları';

  @override
  String get rulesEmpty => 'Alarm kuralı yok.';

  @override
  String get rulesSearch => 'Kural ara';

  @override
  String get ruleFiring => 'tetikte';

  @override
  String get rulePending => 'bekliyor';

  @override
  String get ruleOk => 'normal';

  @override
  String get ruleError => 'hata';

  @override
  String get ruleDisabled => 'kapalı';

  @override
  String ruleOpenIncidents(int count) {
    return '$count açık';
  }

  @override
  String ruleEvery(int seconds) {
    return '$seconds sn\'de bir';
  }

  @override
  String get ruleDisableTitle => 'Bu kural kapatılsın mı?';

  @override
  String ruleDisableBody(int count) {
    return 'Değerlendirmeyi bırakacak, ve $count açık olayı da çözülecek. Şu an haber verdiği ne varsa takip edilmeyi bırakır.';
  }

  @override
  String get ruleDisableBodyNone =>
      'Biri yeniden açana kadar değerlendirmeyi bırakacak.';

  @override
  String get ruleDisableConfirm => 'Kapat';

  @override
  String get ruleEnableTitle => 'Bu kural yeniden açılsın mı?';

  @override
  String get ruleEnableConfirm => 'Aç';

  @override
  String get ruleCancel => 'Vazgeç';

  @override
  String get ruleEditOnWeb =>
      'Eşikler, koşullar ve kanallar webden düzenlenir.';

  @override
  String get navChannels => 'Kanallar';

  @override
  String get channelsEmpty => 'Bildirim kanalı yok.';

  @override
  String get channelsSearch => 'Kanal ara';

  @override
  String get channelsNoSecrets =>
      'Bu kurulumda sır anahtarı yok; kanallar saklanamaz ve denenemez.';

  @override
  String get channelTest => 'Test gönder';

  @override
  String channelTestOk(int ms) {
    return '$ms ms\'de ulaştı';
  }

  @override
  String channelTestFailedCode(int code, String error) {
    return '$code ile reddedildi: $error';
  }

  @override
  String channelTestFailed(String error) {
    return 'Ulaşmadı: $error';
  }

  @override
  String channelLastDelivery(String when) {
    return 'son $when';
  }

  @override
  String get channelOff => 'kapalı';

  @override
  String get channelEditOnWeb =>
      'Kanallar webden oluşturulur ve düzenlenir; burada birinin hâlâ çalıştığını deneyebilirsiniz.';

  @override
  String get sessionsTitle => 'Açık oturumlar';

  @override
  String get sessionsThisDevice => 'bu cihaz';

  @override
  String get sessionsBrowser => 'tarayıcı';

  @override
  String sessionsLastSeen(String when) {
    return 'son kullanım $when';
  }

  @override
  String sessionsExpires(String when) {
    return '$when doluyor';
  }

  @override
  String get sessionsEnd => 'Sonlandır';

  @override
  String get sessionsEndTitle => 'Bu oturum sonlandırılsın mı?';

  @override
  String get sessionsEndBody =>
      'Orada açık olan ne varsa çıkış yapmış olur. Artık sizde olmayan bir cihazsa yapılacak şey budur.';

  @override
  String get sessionsNone => 'Başka oturum yok.';

  @override
  String sessionsFailed(String detail) {
    return 'Oturumlar okunamadı: $detail';
  }

  @override
  String get logsService => 'Servis';

  @override
  String get logsScopedTrace => 'Bu isteğin logları';

  @override
  String get logsScopedPod => 'Bu pod\'un logları';

  @override
  String get logsScopedContainer => 'Bu konteynerin logları';

  @override
  String get logsScopedAll =>
      'Her seviye; bir isteğin logları zaten onun hepsidir.';

  @override
  String get logsOpenForTrace => 'Loglar';

  @override
  String get logsEmptyScoped => 'Buna dair bir log yok.';

  @override
  String get navMutes => 'Susturmalar';

  @override
  String get mutesEmpty => 'Susturulmuş bir şey yok.';

  @override
  String get mutesSearch => 'Susturma ara';

  @override
  String get mutesActive => 'şu an susturuyor';

  @override
  String mutesUpcoming(String when) {
    return '$when başlıyor';
  }

  @override
  String mutesUntil(String when) {
    return '$when kadar';
  }

  @override
  String get mutesEnd => 'Şimdi bitir';

  @override
  String get mutesEndTitle => 'Bu susturma bitirilsin mi?';

  @override
  String get mutesEndBody =>
      'Bunun susturduğu ne varsa alarmlar hemen yeniden çalışmaya başlar.';

  @override
  String get mutesNew => 'Alarmları sustur';

  @override
  String get mutesNewName => 'Neden';

  @override
  String get mutesNewNameHint => 'ör. checkout dağıtımı';

  @override
  String get mutesFor => 'süre';

  @override
  String get mutesCreate => 'Sustur';

  @override
  String get mutesAllRules => 'her kural';

  @override
  String mutesSomeRules(int count) {
    return '$count kural';
  }

  @override
  String get mutesRecurring => 'tekrarlayan; webden düzenlenir';

  @override
  String get mutesDuration30m => '30 dk';

  @override
  String get mutesDuration1h => '1 saat';

  @override
  String get mutesDuration2h => '2 saat';

  @override
  String get mutesDuration4h => '4 saat';

  @override
  String get rightNow => 'birazdan';

  @override
  String inMinutes(int count) {
    return '$count dk sonra';
  }

  @override
  String inHours(int count) {
    return '$count sa sonra';
  }

  @override
  String inDays(int count) {
    return '$count gün sonra';
  }

  @override
  String get navRoutes => 'Yönlendirme';

  @override
  String get routesEmpty =>
      'Yönlendirme kuralı yok; her olay kuralın kendi kanallarına gider.';

  @override
  String get routesOrder =>
      'Bu sırayla denenir; ilk eşleşen kazanır. Sıralamak için sürükleyin.';

  @override
  String get routesDefault => 'varsayılan';

  @override
  String get routesOff => 'kapalı';

  @override
  String routesChannels(int count) {
    return '$count kanal';
  }

  @override
  String get routesMatchAll => 'her şeye uyar';

  @override
  String routesSeverities(String list) {
    return 'önem $list';
  }

  @override
  String routesServices(String list) {
    return 'servis $list';
  }

  @override
  String routesTypes(String list) {
    return 'tür $list';
  }

  @override
  String routesLabels(int count) {
    return '$count etiket koşulu';
  }

  @override
  String routesWindow(String start, String end, String tz) {
    return '$start–$end $tz';
  }

  @override
  String get routesEditOnWeb => 'Koşullar ve kanallar webden düzenlenir.';

  @override
  String get calendarsTitle => 'Tatil takvimleri';

  @override
  String get calendarsAbout =>
      'Tekrarlayan susturmaların atladığı adlandırılmış tarih listeleri, örneğin resmi tatiller.';

  @override
  String get calendarsEmpty => 'Tatil takvimi yok.';

  @override
  String get calendarsNew => 'Yeni tatil takvimi';

  @override
  String get calendarsName => 'Ad';

  @override
  String get calendarsDescription => 'Açıklama';

  @override
  String get calendarsDates => 'Tarihler';

  @override
  String get calendarsDatesHint =>
      'Her satıra bir tane: tek tarih için YYYY-AA-GG, her yıl için AA-GG.';

  @override
  String calendarsInvalid(String list) {
    return 'Geçersiz tarihler: $list';
  }

  @override
  String calendarsCount(int count) {
    return '$count tarih';
  }

  @override
  String calendarsSummary(int count, int mutes) {
    return '$count tarih, $mutes susturmada kullanılıyor';
  }

  @override
  String get calendarsCreate => 'Oluştur';

  @override
  String get calendarsSave => 'Kaydet';

  @override
  String get calendarsEdit => 'Düzenle';

  @override
  String get calendarsDelete => 'Sil';

  @override
  String calendarsDeleteTitle(String name) {
    return '$name silinsin mi?';
  }

  @override
  String get calendarsDeleteBody =>
      'Takvim silinir; onu kullanan susturma yok.';

  @override
  String calendarsDeleteInUse(int count) {
    return '$count susturma bu takvimi kullanıyor; sunucu onlar varken silmeyi reddeder.';
  }

  @override
  String get mutesCalendars => 'Tatil takvimleri';

  @override
  String get deliveriesTitle => 'Teslimat günlüğü';

  @override
  String deliveriesFor(String channel) {
    return '$channel teslimatları';
  }

  @override
  String get deliveriesEmpty => 'Bu filtreyle teslimat yok.';

  @override
  String get deliveriesAll => 'Hepsi';

  @override
  String get deliveriesOpen => 'Teslimat günlüğü';

  @override
  String deliveryForRule(String rule) {
    return 'kural: $rule';
  }

  @override
  String deliveryAttempt(int attempt, String status, int ms) {
    return '$attempt. deneme: $status, $ms ms';
  }

  @override
  String get deliveryAttemptOk => 'başarılı';

  @override
  String get deliveryAttemptFailed => 'başarısız';

  @override
  String get templatesTitle => 'Yeni kural';

  @override
  String get templatesAll => 'Hepsi';

  @override
  String get templatesHosts => 'Sunucular';

  @override
  String get templatesContainers => 'Konteynerler';

  @override
  String get templatesApm => 'APM';

  @override
  String get templatesIntegrations => 'Entegrasyonlar';

  @override
  String get templatesKubernetes => 'Kubernetes';

  @override
  String get templatesEmpty => 'Bu kategoride hazır kural yok.';

  @override
  String get templatesSetUp => 'Kur';

  @override
  String get templatesPreview => 'Önizle';

  @override
  String get templatesCreate => 'Kuralı oluştur';

  @override
  String templatesCreated(String name) {
    return '$name oluşturuldu.';
  }

  @override
  String templatesWouldFire(int count) {
    return 'Son 6 saatte $count kez tetiklenirdi.';
  }

  @override
  String get templatesNoData => 'Son 6 saatte bu kuralın bakacağı veri yok.';

  @override
  String templatesThreshold(String value) {
    return 'Eşik: $value';
  }

  @override
  String templatesOneSeries(int count) {
    return '$count seriden en çok tetikleneni çizildi.';
  }

  @override
  String templatesReference(String metric, String value) {
    return '$metric şu an $value; eşik bunun oranı olarak hesaplandı.';
  }

  @override
  String get templatesRequired => 'Zorunlu.';

  @override
  String get templatesNumber => 'Bir sayı girin.';

  @override
  String templatesRange(String min, String max) {
    return '$min ile $max arasında olmalı.';
  }

  @override
  String get templatesSeconds => 'sn';

  @override
  String get templatesPerSecond => '/sn';

  @override
  String get rulesNew => 'Yeni kural';

  @override
  String templatesUnavailable(String list) {
    return 'Kullanılamayan kural türleri: $list';
  }

  @override
  String templatesPercent(String value) {
    return '%$value';
  }

  @override
  String get navErrors => 'Hatalar';

  @override
  String get errorsSearch => 'Hata ara';

  @override
  String get errorsSort => 'Sırala';

  @override
  String get errorsSortCount => 'Adede göre';

  @override
  String get errorsSortLastSeen => 'Son görülmeye göre';

  @override
  String get errorsSortFirstSeen => 'İlk görülmeye göre';

  @override
  String get errorsAll => 'Hepsi';

  @override
  String get errorsUnresolved => 'Açık';

  @override
  String get errorsResolved => 'Çözüldü';

  @override
  String get errorsIgnored => 'Yoksayıldı';

  @override
  String get errorsRegressed => 'Geri geldi';

  @override
  String get errorsNoWorkflow =>
      'Bu kurulumda hata iş akışı yok (PostgreSQL gerekir); her grup açık görünür.';

  @override
  String errorsCount(int count) {
    return '$count kez';
  }

  @override
  String errorsCountOfTotal(int count, int total) {
    return 'Bu aralıkta $count, saklama süresi boyunca $total kez';
  }

  @override
  String errorsFirstSeen(String when) {
    return 'ilk $when';
  }

  @override
  String errorsResolvedIn(String version) {
    return '$version sürümünde çözüldü';
  }

  @override
  String errorsRegressions(int count) {
    return '$count kez geri geldi';
  }

  @override
  String get errorsLastTrace => 'Son izi aç';

  @override
  String get errorsResolve => 'Çözüldü';

  @override
  String get errorsResolveInVersion => 'Sürümde çözüldü…';

  @override
  String get errorsVersion => 'Sürüm';

  @override
  String get errorsVersionHint =>
      'Bu sürümden sonra yeniden görülürse grup kendiliğinden açılır.';

  @override
  String get errorsIgnore => 'Yoksay';

  @override
  String get errorsReopen => 'Yeniden aç';

  @override
  String get errorsComments => 'Yorumlar';

  @override
  String get errorsNoComments => 'Yorum yok.';

  @override
  String get errorsComment => 'Yorum';

  @override
  String get errorsCommentTooLong => 'Yorum çok uzun.';

  @override
  String get errorsSend => 'Gönder';

  @override
  String get errorsDeleteComment => 'Yorumu sil';

  @override
  String get agentsTitle => 'Ajan sürümleri';

  @override
  String get agentsSearch => 'Servis, ajan ya da sürüm ara';

  @override
  String get agentsEmpty => 'Bu süzgeçle ajan yok.';

  @override
  String get agentsOutdatedOnly => 'Yalnızca geride olanlar';

  @override
  String agentsLatest(String version, String channel) {
    return 'En yeni sürüm $version ($channel)';
  }

  @override
  String get agentsNoCatalog =>
      'Sürüm kataloğu okunamadı; durumlar bilinmiyor.';

  @override
  String get agentsOk => 'güncel';

  @override
  String get agentsOutdated => 'eski';

  @override
  String get agentsUnsupported => 'desteklenmiyor';

  @override
  String get agentsThirdParty => 'üçüncü taraf';

  @override
  String get agentsUnknown => 'bilinmiyor';

  @override
  String agentsInstances(int count) {
    return '$count örnek';
  }

  @override
  String get servicesAgents => 'Ajan sürümleri';

  @override
  String get tracesFilters => 'Süzgeçler';

  @override
  String get tracesTransaction => 'İşlem';

  @override
  String get tracesMinMs => 'En az ms';

  @override
  String get tracesMaxMs => 'En çok ms';

  @override
  String get tracesAttributes => 'Öznitelikler';

  @override
  String get tracesAttributesHint =>
      'anahtar=değer, boşlukla ayrılmış; en fazla 10 tane.';

  @override
  String get tracesErrorsOnly => 'Yalnızca hatalılar';

  @override
  String get tracesSearch => 'Ara';

  @override
  String get serviceTabTraces => 'İzler';

  @override
  String get samplingTitle => 'Örnekleme';

  @override
  String get samplingOffHere =>
      'Bu sunucuda kuyruk örnekleme kapalı; ilke saklanır ama uygulanmaz.';

  @override
  String get samplingDefault =>
      'Kayıtlı ilke yok; sunucunun varsayılanı gösteriliyor.';

  @override
  String samplingUpdated(String when, String email) {
    return '$when, $email tarafından güncellendi';
  }

  @override
  String get samplingEnabled => 'İlke etkin';

  @override
  String get samplingEnabledHint =>
      'Kapalıyken her iz saklanır; ilke durur ama hiçbir şeyi elemez.';

  @override
  String get samplingBaseline => 'Taban oran';

  @override
  String get samplingBaselineHint =>
      'Hiçbir kurala uymayan izlerden saklanan oran.';

  @override
  String get samplingMaxSpans => 'Saniyede en çok span';

  @override
  String get samplingMaxSpansHint =>
      'Örnekleyici örneği başına; 0 sınırsız demek.';

  @override
  String get samplingPercentRange => '0 ile 100 arasında bir yüzde girin.';

  @override
  String get samplingRules => 'Kurallar';

  @override
  String get samplingOrder =>
      'Sırayla denenir; ilk uyan kuralın oranı geçerli olur. Sıralamak için sürükleyin.';

  @override
  String get samplingNoRules => 'Kural yok; her iz taban orana tabi.';

  @override
  String get samplingAddRule => 'Kural ekle';

  @override
  String get samplingEditRule => 'Kuralı düzenle';

  @override
  String get samplingRuleName => 'Ad';

  @override
  String get samplingRuleType => 'Tür';

  @override
  String get samplingRuleRatio => 'Saklama oranı';

  @override
  String get samplingRuleThreshold => 'Eşik (ms)';

  @override
  String get samplingRuleServices => 'Servisler';

  @override
  String get samplingRuleServicesHint => 'Virgülle ayrılmış.';

  @override
  String get samplingRuleService => 'Servis (isteğe bağlı)';

  @override
  String get samplingRuleServiceHint =>
      'Doldurulursa kural yalnızca bu servisin span\'lerine bakar.';

  @override
  String get samplingRuleRoute => 'Rota';

  @override
  String get samplingRuleRouteHint =>
      'http.route üzerinde glob; sonda * önek eşleşmesi.';

  @override
  String get samplingRuleKey => 'Öznitelik anahtarı';

  @override
  String get samplingRuleValue => 'Değer (isteğe bağlı)';

  @override
  String get samplingRuleValueHint =>
      'Boş bırakılırsa anahtarın var olması yeter.';

  @override
  String get samplingTypeError => 'hatalı izler';

  @override
  String get samplingTypeLatency => 'yavaş izler';

  @override
  String get samplingTypeService => 'servise göre';

  @override
  String get samplingTypeRoute => 'rotaya göre';

  @override
  String get samplingTypeAttribute => 'özniteliğe göre';

  @override
  String samplingOverMs(int ms) {
    return '$ms ms üstü';
  }

  @override
  String samplingKeepRatio(String percent) {
    return '%$percent saklanır';
  }

  @override
  String samplingMatched(String percent) {
    return 'son tahminde izlerin %$percent\'i uydu';
  }

  @override
  String get samplingEstimate => 'Tahmin et';

  @override
  String get samplingSave => 'Kaydet';

  @override
  String samplingKeeps(String traces, String spans) {
    return 'İzlerin %$traces\'i, span\'lerin %$spans\'i saklanırdı.';
  }

  @override
  String samplingExamined(int count, int minutes) {
    return 'Son $minutes dakikadaki $count iz incelendi.';
  }

  @override
  String get samplingNoRateLimit => 'Hız sınırı bu tahmine dahil değil.';

  @override
  String get samplingConflict =>
      'Siz düzenlerken başkası kaydetti; yeniden yükleyip tekrar deneyin.';

  @override
  String get samplingUnavailable =>
      'Bu kurulumda örnekleme ilkesi saklanamıyor.';

  @override
  String get servicesSampling => 'Örnekleme';

  @override
  String get serviceTabMap => 'Bağımlılıklar';

  @override
  String get mapEmpty =>
      'Bu aralıkta bu servise giden ya da bu servisten çıkan çağrı yok.';

  @override
  String get mapIncoming => 'Bunu çağıranlar';

  @override
  String get mapIncomingHint => 'Bu servise istek gönderen servisler.';

  @override
  String get mapOutgoing => 'Bunun çağırdıkları';

  @override
  String get mapOutgoingHint =>
      'Bu servisin bağımlı olduğu servisler, veritabanları ve dış adresler.';

  @override
  String get mapKindDb => 'veritabanı';

  @override
  String get mapKindExternal => 'dış';

  @override
  String get mapKindMessaging => 'kuyruk';

  @override
  String mapCalls(int count) {
    return '$count çağrı';
  }

  @override
  String mapErrorRate(String percent) {
    return '%$percent hata';
  }

  @override
  String mapP95(String ms) {
    return 'p95 $ms ms';
  }

  @override
  String mapAvg(String ms) {
    return 'ort $ms ms';
  }

  @override
  String get mapOnPath => 'bu yolda';

  @override
  String mapPathOf(String transaction, int count) {
    return '$transaction işleminin $count izinden geçtiği bağımlılıklar işaretli.';
  }

  @override
  String get mapPathClear => 'İşlem işaretini kaldır';

  @override
  String get mapShowPath => 'Bu işlemin yolunu göster';
}
