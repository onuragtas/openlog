// The public status page of this installation.
//
// Its own controller rather than a field of the settings ones: it needs no
// authentication, 404s where the page is turned off, and that 404 means
// "this installation publishes none" rather than "something is broken".
import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';
import 'session.dart';

class StatusPageController extends DetailController<StatusPage> {
  StatusPageController(this._client);

  final OpenlogClient _client;

  /// True when the installation does not publish a status page at all
  /// (OPENLOG_STATUS_PAGE_ENABLED): a 404 here is a setting, not an outage,
  /// and the two must not look the same.
  bool unavailable = false;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  SessionFailure? kindForStatus(int status) {
    if (status != 404) return null;
    unavailable = true;
    return null;
  }

  @override
  Future<StatusPage> fetch() async {
    final page = await _client.statusPage();
    unavailable = false;
    return page;
  }
}
