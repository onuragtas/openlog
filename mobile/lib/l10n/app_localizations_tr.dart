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
  String get alertsAlreadyResolved => 'Bu alarm onaylanmadan önce çözüldü.';

  @override
  String get alertsAcknowledge => 'Onayla';

  @override
  String alertsAcknowledgedBy(String email) {
    return '$email onayladı';
  }

  @override
  String get alertsAcknowledgedUnknown => 'Onaylandı';

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
}
