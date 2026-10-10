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
  String get navRules => 'Kurallar';

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
  String get serviceTabMap => 'Harita';

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

  @override
  String get navTemplates => 'Şablonlar';

  @override
  String get navIncidents => 'Olaylar';

  @override
  String get serviceTabTransactions => 'İşlemler';

  @override
  String get serviceTabDatabases => 'Veritabanları';

  @override
  String get transactionsEmpty => 'Bu aralıkta işlem yok.';

  @override
  String get serviceDatabasesEmpty => 'Bu aralıkta veritabanı sorgusu yok.';

  @override
  String transactionsShare(String percent) {
    return 'sürenin %$percent\'i';
  }

  @override
  String get sortTimeConsumed => 'Harcanan süre';

  @override
  String get sortThroughput => 'Hacim';

  @override
  String get sortCalls => 'Çağrı';

  @override
  String get sortSlowest => 'En yavaş';

  @override
  String get sortErrors => 'Hata';

  @override
  String get deploymentsTitle => 'Dağıtımlar';

  @override
  String get deploymentsEmpty => 'Bu aralıkta sürüm değişikliği yok.';

  @override
  String get deploymentsRollback => 'geri alma';

  @override
  String get deploymentsFirst => 'ilk sürüm';

  @override
  String deploymentsWindow(int minutes) {
    return 'Öncesi ve sonrası $minutes dakika';
  }

  @override
  String deploymentsNewErrors(int count) {
    return 'Bu dağıtımdan sonra ilk kez görülen $count hata grubu';
  }

  @override
  String get serviceLatencyChart => 'Gecikme (p95)';

  @override
  String get serviceApdexChart => 'Apdex';

  @override
  String get serviceTopTransactions => 'Harcanan süreye göre en üst işlemler';

  @override
  String get serviceAllTransactions => 'Tüm işlemler';

  @override
  String get serviceHosts => 'Sunucular';

  @override
  String get serviceContainers => 'Konteynerler';

  @override
  String get servicePods => 'Pod\'lar';

  @override
  String get apdexTitle => 'Apdex eşiği';

  @override
  String get apdexExplain =>
      'Bu süreye kadar süren istekler memnun sayılır; dört katına kadar olanlar yarı memnun.';

  @override
  String get apdexThreshold => 'Milisaniye';

  @override
  String apdexDefault(int ms) {
    return 'Apdex $ms ms (varsayılan)';
  }

  @override
  String apdexSet(int ms) {
    return 'Apdex $ms ms';
  }

  @override
  String get apdexUnavailable => 'Bu kurulumda servis ayarı saklanamıyor.';

  @override
  String get serviceUnknownHost =>
      'Bunun ajan verisi yok; açılacak bir sayfası yok.';

  @override
  String get errorsLastMessage => 'Son mesaj';

  @override
  String get errorsStacktrace => 'Yığın izi';

  @override
  String get errorsNoStack => 'Bu grup için yığın izi yok.';

  @override
  String errorsSymbolicated(int count) {
    return '$count çerçeve source map ile çözüldü.';
  }

  @override
  String get errorsAffected => 'Etkilenenler';

  @override
  String get errorsAffectedVersions => 'Sürümler';

  @override
  String get errorsAffectedHosts => 'Sunucular';

  @override
  String get errorsAffectedContainers => 'Konteynerler';

  @override
  String get errorsAffectedTransactions => 'İşlemler';

  @override
  String get errorsSamples => 'Örnek istekler';

  @override
  String get errorsNoSamples => 'Saklanmış örnek istek yok.';

  @override
  String get errorsActivity => 'Geçmiş';

  @override
  String get hostApmServices => 'İz gönderen servisler';

  @override
  String get settingsProfile => 'Profil';

  @override
  String get settingsSecurity => 'Güvenlik';

  @override
  String get profileLanguage => 'Dil';

  @override
  String get profileLanguageHint =>
      'Sunucunun yazdığı dil: alarm e-postaları ve üretilen kural adları. Uygulamanın dili telefonu izler.';

  @override
  String get profileLanguageAuto => 'Otomatik';

  @override
  String get securityPassword => 'Parola';

  @override
  String get securityPasswordHint =>
      'Parolayı değiştirmek diğer oturumlarınızı kapatır; bu cihaz açık kalır.';

  @override
  String get securityCurrentPassword => 'Şimdiki parola';

  @override
  String get securityNewPassword => 'Yeni parola';

  @override
  String securityMinLength(int count) {
    return 'En az $count karakter.';
  }

  @override
  String get securityChangePassword => 'Parolayı değiştir';

  @override
  String get securityPasswordChanged =>
      'Parola değişti; diğer oturumlar kapatıldı.';

  @override
  String get settingsMembers => 'Üyeler';

  @override
  String get membersTitle => 'Üyeler';

  @override
  String get membersRole => 'Rol';

  @override
  String get membersYou => 'siz';

  @override
  String membersJoined(String when) {
    return 'katıldı $when';
  }

  @override
  String get membersRemove => 'Çıkar';

  @override
  String get membersRemoveTitle => 'Üye çıkarılsın mı?';

  @override
  String membersRemoveBody(String email) {
    return '$email bu organizasyona erişemez olacak.';
  }

  @override
  String get membersLeave => 'Ayrıl';

  @override
  String get membersLeaveTitle => 'Organizasyondan ayrılınsın mı?';

  @override
  String membersLeaveBody(String org) {
    return '$org erişiminiz kalkar; geri dönmek için yeni bir davet gerekir.';
  }

  @override
  String get membersForbidden => 'Rolünüz üyeleri yönetmeye yetmiyor.';

  @override
  String get invitationsTitle => 'Davetler';

  @override
  String get invitationsEmpty => 'Bekleyen davet yok.';

  @override
  String get invitationsNew => 'Yeni davet';

  @override
  String get invitationsEmail => 'E-posta';

  @override
  String get invitationsSend => 'Davet gönder';

  @override
  String get invitationsResend => 'Yenile';

  @override
  String get invitationsRevoke => 'Daveti iptal et';

  @override
  String get invitationsExpired => 'süresi doldu';

  @override
  String invitationsExpires(String when) {
    return 'biter $when';
  }

  @override
  String invitationsSentTo(String email) {
    return '$email adresine davet e-postası gönderildi.';
  }

  @override
  String invitationsNotSent(String email) {
    return '$email için davet oluşturuldu ama e-posta gönderilemedi; bağlantıyı kendiniz iletin.';
  }

  @override
  String get invitationsTokenOnce => 'Bu kod yalnızca bir kez gösterilir.';

  @override
  String get invitationsDone => 'Tamam';

  @override
  String invitationsExpiredAt(String when) {
    return 'bitti $when';
  }

  @override
  String get settingsLicenseKeys => 'Lisans anahtarları';

  @override
  String get settingsApiKeys => 'API anahtarları';

  @override
  String get settingsBrowserKeys => 'Tarayıcı anahtarları';

  @override
  String get keysName => 'Ad';

  @override
  String get keysCreate => 'Oluştur';

  @override
  String keysCreated(String name) {
    return '$name oluşturuldu.';
  }

  @override
  String get keysShownOnce => 'Bu değer yalnızca bir kez gösterilir.';

  @override
  String get keysImported =>
      'Değer dışarıdan verildiği için gösterilecek bir şey yok.';

  @override
  String get keysRevoke => 'İptal et';

  @override
  String get keysRevoked => 'iptal edildi';

  @override
  String keysRevokeTitle(String name) {
    return '$name iptal edilsin mi?';
  }

  @override
  String keysLastUsed(String when) {
    return 'son kullanım $when';
  }

  @override
  String get keysNeverUsed => 'hiç kullanılmadı';

  @override
  String keysExpires(String when) {
    return 'biter $when';
  }

  @override
  String get keysForbidden => 'Rolünüz bu anahtarları yönetmeye yetmiyor.';

  @override
  String get licenseKeysEmpty => 'Lisans anahtarı yok.';

  @override
  String get licenseKeysNew => 'Yeni lisans anahtarı';

  @override
  String get licenseKeysRevokeBody =>
      'Bu anahtarla veri gönderen ajanlar durur. Sunucu, yetki önbelleği dolana kadar kısa bir süre kabul etmeye devam edebilir.';

  @override
  String get apiKeysEmpty => 'API anahtarı yok.';

  @override
  String get apiKeysNew => 'Yeni API anahtarı';

  @override
  String get apiKeysViewerOnly =>
      'Yazma yetkisi olan anahtarı yalnızca yöneticiler oluşturabilir.';

  @override
  String get apiKeysRevokeBody =>
      'Bu anahtarı kullanan betikler hemen çalışmaz olur.';

  @override
  String get browserKeysEmpty => 'Tarayıcı anahtarı yok.';

  @override
  String get browserKeysNew => 'Yeni tarayıcı anahtarı';

  @override
  String get browserKeysService => 'Servis adı';

  @override
  String get browserKeysKindBrowser => 'Tarayıcı';

  @override
  String get browserKeysKindMobile => 'Mobil';

  @override
  String get browserKeysOriginsLabel => 'İzinli adresler';

  @override
  String get browserKeysOriginsHint =>
      'Her satıra bir tane: https://app.example.com';

  @override
  String get browserKeysAppIds => 'İzinli uygulama kimlikleri';

  @override
  String get browserKeysAppIdsHint => 'Her satıra bir tane: com.example.shop';

  @override
  String browserKeysOrigins(int count) {
    return '$count adres';
  }

  @override
  String browserKeysApps(int count) {
    return '$count uygulama';
  }

  @override
  String get browserKeysRevokeBody =>
      'Bu anahtarla veri gönderen sayfalar ve uygulamalar durur.';

  @override
  String get settingsSourceMaps => 'Source map\'ler';

  @override
  String get settingsAuditLog => 'Denetim kaydı';

  @override
  String get sourceMapsEmpty => 'Kayıtlı source map yok.';

  @override
  String get sourceMapsUploadElsewhere =>
      'Yükleme, dosyanın bulunduğu yerden yapılır: derleme makinesi ya da CI.';

  @override
  String sourceMapsSize(String kb) {
    return '$kb KB';
  }

  @override
  String get sourceMapsDeleteBody =>
      'Bu dosyanın ait olduğu sürümün tarayıcı yığınları bir daha çözülmez.';

  @override
  String get auditActor => 'Kim';

  @override
  String get auditAction => 'İşlem';

  @override
  String get auditActionHint => 'Önek: member. ya da member.remove';

  @override
  String get auditEmpty => 'Bu süzgeçle kayıt yok.';

  @override
  String get auditMore => 'Daha eskiler';

  @override
  String get auditEnd => 'Kaydın sonu.';

  @override
  String get auditUnknownActor => 'bilinmiyor';

  @override
  String get auditWithKey => 'API anahtarıyla';

  @override
  String get auditForbidden => 'Rolünüz denetim kaydını okumaya yetmiyor.';

  @override
  String get settingsOrganization => 'Organizasyon';

  @override
  String get orgName => 'Ad';

  @override
  String get orgRename => 'Adı değiştir';

  @override
  String get orgCreated => 'Oluşturuldu';

  @override
  String get orgTenantId => 'Tenant kimliği';

  @override
  String get orgId => 'Organizasyon kimliği';

  @override
  String get orgLanguage => 'Organizasyon dili';

  @override
  String get orgLanguageHint =>
      'Kendi dilini seçmemiş kişilere sunucunun yazdığı dil.';

  @override
  String get orgLanguageNone => 'Seçilmedi';

  @override
  String get orgForbidden => 'Rolünüz organizasyonu değiştirmeye yetmiyor.';

  @override
  String get settingsSampling => 'APM örnekleme';

  @override
  String get settingsUsage => 'Kullanım ve plan';

  @override
  String get usageCurrent => 'Bu dönem';

  @override
  String get usagePrevious => 'Önceki dönem';

  @override
  String get usageSaas => 'SaaS';

  @override
  String get usageSelfHosted => 'Kendi sunucunuzda';

  @override
  String get usagePlanDefault => 'Plan atanmadı; katalog varsayılanı geçerli.';

  @override
  String get usageBlocked => 'Veri alımı durduruldu: plan sınırı aşıldı.';

  @override
  String get usageIngest => 'Alınan veri';

  @override
  String get usageHosts => 'Sunucular';

  @override
  String get usageUsers => 'Kullanıcılar';

  @override
  String get usageContainers => 'Konteynerler';

  @override
  String get usageServices => 'Servisler';

  @override
  String get usageQueries => 'Sorgular';

  @override
  String get usageStored => 'Saklanan (sıkıştırılmış)';

  @override
  String get usageUnlimited => 'Sınırsız';

  @override
  String usageProjected(String value) {
    return 'Dönem sonu tahmini: $value';
  }

  @override
  String usageProjectedPercent(String value, int percent) {
    return 'Dönem sonu tahmini: $value (sınırın %$percent\'i)';
  }

  @override
  String get usageBySignal => 'Sinyale göre';

  @override
  String get usageRetention => 'Saklama';

  @override
  String usageDays(int count) {
    return '$count gün';
  }

  @override
  String get usageForbidden => 'Rolünüz kullanımı görmeye yetmiyor.';

  @override
  String get settingsStorage => 'Depolama';

  @override
  String get storageNotMeasured => 'Diskler henüz ölçülmedi.';

  @override
  String storageLevels(int warn, int high) {
    return 'Uyarı %$warn, yüksek %$high.';
  }

  @override
  String storageFree(String free, String total) {
    return '$free boş / $total';
  }

  @override
  String get storageBroken => 'Disk okunamıyor.';

  @override
  String get storageForbidden => 'Rolünüz disk durumunu görmeye yetmiyor.';

  @override
  String get settingsSso => 'SSO';

  @override
  String get ssoUnavailable =>
      'SSO kullanılamıyor: sunucunun genel adresi (OPENLOG_PUBLIC_URL) ayarlı değil.';

  @override
  String get ssoSecretsPlain =>
      'Sırlar şifrelenmeden saklanıyor; sunucuda bir anahtar tanımlayın.';

  @override
  String get ssoConnections => 'Bağlantılar';

  @override
  String get ssoNoConnections => 'Tanımlı SSO bağlantısı yok.';

  @override
  String get ssoEditOnWeb =>
      'Bağlantı oluşturmak ve düzenlemek webde: metadata adresi, istemci sırrı ve sertifika yapıştırmak gerekiyor.';

  @override
  String get ssoTestOk => 'Sunucu tarafı denetimler geçti.';

  @override
  String ssoTestFailed(String checks) {
    return 'Başarısız denetimler: $checks';
  }

  @override
  String get ssoEnforce => 'SSO zorunlu';

  @override
  String get ssoEnforceHint =>
      'Açıkken herkes kimlik sağlayıcısından giriş yapar.';

  @override
  String ssoBreakGlass(int count) {
    return '$count kişi parolayla girebilir.';
  }

  @override
  String get ssoNoBreakGlass => 'Parolayla girebilecek kimse tanımlı değil.';

  @override
  String get ssoDomains => 'Alan adları';

  @override
  String get ssoDomainsHint =>
      'Bu alan adlarındaki e-postalar SSO ile giriş yapar.';

  @override
  String get ssoDomainAdd => 'Alan adı ekle';

  @override
  String get ssoVerified => 'doğrulandı';

  @override
  String get ssoUnverified => 'doğrulanmadı';

  @override
  String get ssoVerifyDns => 'DNS ile doğrula';

  @override
  String get ssoVerifyEmail => 'E-posta ile doğrula';

  @override
  String get ssoRoleMappings => 'Grup eşlemeleri';

  @override
  String get ssoRoleMappingsHint =>
      'Kimlik sağlayıcısındaki grup, buradaki rol olur. Sahip rolü eşlemeyle verilmez.';

  @override
  String get ssoGroup => 'Grup';

  @override
  String get ssoScimTokens => 'SCIM anahtarları';

  @override
  String get ssoNoScimTokens => 'SCIM anahtarı yok.';

  @override
  String get ssoForbidden => 'Rolünüz SSO ayarlarını görmeye yetmiyor.';

  @override
  String get ssoTest => 'Bağlantıyı dene';

  @override
  String get ssoHealthOk => 'Sağlıklı';

  @override
  String get ssoHealthWarning => 'Uyarı';

  @override
  String get ssoHealthError => 'Hata';

  @override
  String get ssoHealthUnknown => 'Henüz denetlenmedi';

  @override
  String get privacySsoReauth =>
      'Bu hesabın parolası yok; sunucu yakın zamanlı bir SSO girişi istiyor.';

  @override
  String get privacyDelete => 'Sil';

  @override
  String privacyUnverified(String email) {
    return '$email adresi doğrulanmadı.';
  }

  @override
  String get privacyResend => 'Doğrulama e-postasını yeniden gönder';

  @override
  String get privacyVerificationSent => 'Doğrulama e-postası gönderildi.';

  @override
  String get privacyExport => 'Verilerimin kopyası';

  @override
  String get privacyExportHint =>
      'Hazırlanınca webden indirilir; arşiv açmak telefonun işi değil.';

  @override
  String get privacyExportRequest => 'Kopya iste';

  @override
  String get privacyExportQueued => 'İstek alındı; hazırlanınca e-posta gelir.';

  @override
  String privacyOrgDeletion(String org, String when) {
    return '$org silinmek üzere: $when kalıcı olarak gidiyor.';
  }

  @override
  String get privacyCancelDeletion => 'Silmeyi iptal et';

  @override
  String get privacyDangerous => 'Geri alınamaz';

  @override
  String get privacyDeleteOrg => 'Organizasyonu sil';

  @override
  String get privacyDeleteOrgTitle => 'Organizasyonu sil';

  @override
  String privacyDeleteOrgBody(String org, int days) {
    return '$org ve içindeki her şey $days gün sonra kalıcı olarak silinir. O zamana kadar iptal edebilirsiniz. Onaylamak için adını yazın.';
  }

  @override
  String get privacyDeleteAccount => 'Hesabımı sil';

  @override
  String get privacyDeleteAccountTitle => 'Hesabı sil';

  @override
  String get privacyDeleteAccountBody =>
      'Hesabınız ve kişisel verileriniz silinir. Onaylamak için e-posta adresinizi yazın.';

  @override
  String get alreadyVerified => 'Bu adres zaten doğrulanmış.';

  @override
  String get reauthNeeded =>
      'Kimliğinizi doğrulayın: parola yanlış ya da SSO oturumu çok eski.';

  @override
  String get filterAdd => 'Süzgeç';

  @override
  String get filterPickKey => 'Alan seç';

  @override
  String get filterSearchKey => 'Alan ara';

  @override
  String get filterSearchValue => 'Değer ara';

  @override
  String get filterBack => 'Alanlara dön';

  @override
  String get filterExists => 'Yalnızca var olanlar';

  @override
  String get filterApply => 'Uygula';

  @override
  String filterRecords(int count) {
    return '$count kayıt';
  }

  @override
  String filterDistinct(int count) {
    return '$count farklı değer';
  }

  @override
  String get filterEmptyValue => '(boş)';

  @override
  String get filterSampled =>
      'Örneklemden sayıldı; en sık görülenler listeleniyor.';

  @override
  String get logsTabRecords => 'Kayıtlar';

  @override
  String get logsTabPatterns => 'Desenler';

  @override
  String get patternsEmpty => 'Bu süzgeçle desen yok.';

  @override
  String patternsTotal(int count) {
    return '$count kayıt';
  }

  @override
  String patternsUnclassified(int count) {
    return '$count deseni olmayan';
  }

  @override
  String get patternsRollup => 'saatlik özetten';

  @override
  String get patternsTruncated => 'Desenlerin tamamı değil; süzgeci daraltın.';

  @override
  String patternsErrors(int count) {
    return '$count hata';
  }

  @override
  String volumeTotal(int count, String step) {
    return '$count kayıt · $step aralıklarla';
  }

  @override
  String volumeP95(String ms) {
    return 'p95 $ms ms';
  }

  @override
  String get metricExemplars => 'Örnek izler';

  @override
  String get metricExemplarsLoad => 'Bu metriğin arkasındaki izleri getir';

  @override
  String metricExemplarsFailed(String message) {
    return 'Örnek izler alınamadı: $message';
  }

  @override
  String get metricExemplarsTruncated =>
      'Hepsi değil; aralıkta daha fazlası var.';

  @override
  String get correlationsTitle => 'Bu sırada değişenler';

  @override
  String get correlationsAbout =>
      'Olayın penceresinde, öncesine göre farklı davranan seriler.';

  @override
  String get correlationsLoad => 'Hesapla';

  @override
  String correlationsFailed(String message) {
    return 'Hesaplanamadı: $message';
  }

  @override
  String get correlationsNoRatio => 'oran yok';

  @override
  String correlationsMeans(String baseline, String window, String score) {
    return 'önce $baseline → sonra $window · skor $score';
  }

  @override
  String get savedViewsButton => 'Görünümler';

  @override
  String get savedViewsTitle => 'Kayıtlı görünümler';

  @override
  String get savedViewsEmpty =>
      'Henüz kayıtlı görünüm yok. Aşağıya bir ad yazıp ekrandaki filtreleri kaydedin.';

  @override
  String get savedViewsAbout =>
      'Filtreler bir adın altında saklanır; aynı görünüm tarayıcıda da açılır.';

  @override
  String get savedViewPrivate => 'Özel';

  @override
  String get savedViewOrg => 'Organizasyon';

  @override
  String get savedViewVisibility => 'Kimler görebilir';

  @override
  String get savedViewName => 'Ad';

  @override
  String get savedViewNameHint => 'ör. Ödeme hataları';

  @override
  String get savedViewSave => 'Görünümü kaydet';

  @override
  String get savedViewOverwrite => 'Üzerine yaz';

  @override
  String savedViewOverwriteHint(String name) {
    return 'Ekrandaki filtreleri $name görünümüne kaydet';
  }

  @override
  String get savedViewDelete => 'Sil';

  @override
  String get savedViewDeleteConfirm => 'Silinsin mi?';

  @override
  String savedViewApplied(String name) {
    return '$name uygulandı';
  }

  @override
  String savedViewGroupsIgnored(int count) {
    return 'Bu görünümün $count VEYA grubu telefonda gösterilemiyor; liste yalnızca VE koşullarıyla süzüldü.';
  }

  @override
  String get savedViewAllSpans =>
      'Bu görünüm tüm span\'leri istiyor; telefon her izin yalnızca kök span\'ini listeler.';

  @override
  String get viewsFull =>
      'Organizasyonun kayıtlı görünüm sayısı sınırda (500). Kaydetmek için önce birini silin.';

  @override
  String get viewGone => 'Bu görünüm artık yok; listeyi yenileyin.';

  @override
  String get wizardTitle => 'Sorgu oluştur';

  @override
  String get wizardSubtitle =>
      'Neye bakmak istediğinizi seçin; OQL sizin için yazılır.';

  @override
  String get wizardDataType => 'Şuna bak';

  @override
  String get wizardTypeLog => 'Loglar';

  @override
  String get wizardTypeSpan => 'Span\'ler (tüm trace adımları)';

  @override
  String get wizardTypeTransaction => 'Transaction\'lar (istekler)';

  @override
  String get wizardTypeMetric => 'Metrikler';

  @override
  String get wizardTypeHost => 'Sunucular';

  @override
  String get wizardTypeContainer => 'Konteynerler';

  @override
  String get wizardMeasure => 'Göster';

  @override
  String get wizardMeasureCount => 'Kaç tane';

  @override
  String get wizardMeasureAverage => 'Ortalaması';

  @override
  String get wizardMeasureSum => 'Toplamı';

  @override
  String get wizardMeasureMin => 'En küçüğü';

  @override
  String get wizardMeasureMax => 'En büyüğü';

  @override
  String get wizardMeasureUnique => 'Farklı değer sayısı';

  @override
  String get wizardMeasureMedian => 'Ortanca değeri';

  @override
  String get wizardMeasurePercentile => 'Yüzdelikleri (50, 95, 99)';

  @override
  String get wizardMeasureLatest => 'En son değeri';

  @override
  String get wizardAttribute => 'Alan';

  @override
  String get wizardChooseAttribute => 'Bir alan seçin';

  @override
  String get wizardMetric => 'Metrik';

  @override
  String get wizardChooseMetric => 'Bir metrik seçin';

  @override
  String get wizardFilters => 'Yalnızca şu durumda';

  @override
  String get wizardAddFilter => 'Koşul ekle';

  @override
  String get wizardRemoveFilter => 'Koşulu kaldır';

  @override
  String get wizardFilterKey => 'Alan';

  @override
  String get wizardFilterOp => 'Koşul';

  @override
  String get wizardFilterValue => 'Değer';

  @override
  String get wizardOpEq => 'eşittir';

  @override
  String get wizardOpNeq => 'eşit değildir';

  @override
  String get wizardOpContains => 'içerir';

  @override
  String get wizardOpLike => 'kalıba uyar (% joker)';

  @override
  String get wizardOpGt => 'büyüktür';

  @override
  String get wizardOpGte => 'en az';

  @override
  String get wizardOpLt => 'küçüktür';

  @override
  String get wizardOpLte => 'en fazla';

  @override
  String get wizardOpNull => 'boştur';

  @override
  String get wizardOpNotNull => 'boş değildir';

  @override
  String get wizardGroupBy => 'Şuna göre ayır';

  @override
  String get wizardAddGroupBy => 'Ayırma ekle';

  @override
  String get wizardGroupByHint =>
      'Her değer için ayrı bir satır, örneğin her servis için.';

  @override
  String get wizardLimit => 'İlk';

  @override
  String get wizardTimeseries => 'Zamana göre göster';

  @override
  String get wizardRun => 'Sorguyu çalıştır';

  @override
  String get wizardReset => 'Baştan başla';

  @override
  String get wizardMissingAttribute => 'Ölçülecek alanı seçin.';

  @override
  String get wizardMissingMetric => 'Bir metrik seçin.';

  @override
  String get wizardNoOptions => 'Bu aralıkta gösterilecek bir şey yok.';

  @override
  String get queryProblems => 'Sorgu sorunları';

  @override
  String queryProblem(int line, int column, String message) {
    return 'Satır $line, sütun $column: $message';
  }

  @override
  String get queryError => 'Hata';

  @override
  String get queryWarning => 'Uyarı';

  @override
  String get queryExamples => 'Örnekler';

  @override
  String get queryExamplesHint =>
      'Birine dokununca çalışır; sonra düzenleyebilirsiniz.';

  @override
  String get queryExampleLogsBySeverity => 'Önem düzeyine göre log sayısı';

  @override
  String get queryExampleErrorsByService => 'Servis bazında hata logları';

  @override
  String get queryExampleSlowTransactions =>
      'En yavaş transaction\'lar (p50, p95)';

  @override
  String get queryExampleCpuByHost => 'Sunucu bazında CPU kullanımı';

  @override
  String get queryExampleDurationHistogram => 'İstek sürelerinin dağılımı';

  @override
  String get queryExampleTrafficVsYesterday => 'Trafiğin dünle karşılaştırması';

  @override
  String get profileTabFlame => 'Alev grafiği';

  @override
  String get profileTabFunctions => 'Fonksiyonlar';

  @override
  String get flameEmpty => 'Bu aralıkta örnek yok.';

  @override
  String get flameZoomHint =>
      'Yakınlaşmak için bir çerçeveye dokunun; adı için basılı tutun.';

  @override
  String get flameBack => 'Bir üste dön';

  @override
  String get flameZoom => 'yakınlaş';

  @override
  String flameShareOfAll(String share) {
    return 'profilin %$share\'i';
  }

  @override
  String flameTooNarrow(int count) {
    return '$count çerçeve bu genişlikte çizilemeyecek kadar dar; görmek için üstündeki bloğa dokunun.';
  }

  @override
  String savedViewKept(String name) {
    return '“$name” kaydedildi';
  }
}
