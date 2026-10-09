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
  String get errorsEmpty => 'Bu aralıkta hiçbir şey hata vermedi.';

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
  String get errorsTruncated => 'Daha fazlası var; bu listenin başı.';

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
}
