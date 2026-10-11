// The browser screens beyond the overview: the routes of an application, the
// visits it had, one visit's timeline, the versions it ran and a funnel over
// its own custom events.
//
// Every one of these is a separate endpoint rather than a slice of what the
// overview already has: a route's p75 is not derivable from the
// application's, and a session's events live in the spans.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';
import 'list_controller.dart';
import 'session.dart';

/// The routes of one application, by the measure the screen picked.
class RumPagesController extends ListController<RumPage> {
  RumPagesController(this._client, this.app);

  final OpenlogClient _client;
  final String app;

  /// `views`, `slowest` (total time consumed) or `avg`, as the web offers.
  String sort = 'views';

  /// Which kind of visitor the list is about: a device, a browser, or all.
  String deviceType = '';
  String browser = '';

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<RumPage>> fetch() async => (await _client.rumPages(
    app,
    sort: sort,
    deviceType: deviceType,
    browser: browser,
  )).pages;
}

/// The five vitals of one route. Its own controller because it is its own
/// query: the page list carries only LCP, and a route's other four vitals
/// are not in it at all.
class RumRouteVitalsController extends DetailController<RumVitalList> {
  RumRouteVitalsController(
    this._client,
    this.app,
    this.route, {
    this.deviceType = '',
  });

  final OpenlogClient _client;
  final String app;
  final String route;

  /// The device the list was narrowed to, carried into this route's own
  /// vitals. No browser: the rollup behind them keeps no such column.
  final String deviceType;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<RumVitalList> fetch() =>
      _client.rumVitals(app, route: route, deviceType: deviceType);
}

/// Visits of one application, newest first.
class RumSessionsController extends ListController<RumSession> {
  RumSessionsController(this._client, this.app);

  final OpenlogClient _client;
  final String app;

  /// Which kind of visitor, and whose visits.
  String deviceType = '';
  String browser = '';

  /// One identity (rum.md §3.7), set by opening a session and asking for
  /// the rest of that person's visits.
  String userId = '';

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<RumSession>> fetch() async => (await _client.rumSessions(
    app,
    deviceType: deviceType,
    browser: browser,
    userId: userId,
  )).sessions;
}

/// One visit: its summary and the events it produced.
class RumSessionController extends DetailController<RumSessionDetail> {
  RumSessionController(this._client, this.sessionId);

  final OpenlogClient _client;
  final String sessionId;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<RumSessionDetail> fetch() => _client.rumSession(sessionId);
}

/// The versions of one application and the deployments that introduced them.
class RumReleasesController extends DetailController<RumReleaseList> {
  RumReleasesController(this._client, this.app);

  final OpenlogClient _client;
  final String app;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<RumReleaseList> fetch() => _client.rumReleases(app);
}

/// A funnel over the application's own custom events.
///
/// Nothing is asked until there are two steps: the endpoint requires them and
/// would answer 400, which on a screen where the steps are typed would be an
/// error message per keystroke.
class RumFunnelController extends ChangeNotifier {
  RumFunnelController(this._client, this.app);

  final OpenlogClient _client;
  final String app;

  List<String> steps = const [];
  String window = '30m';

  RumFunnelResponse? value;
  bool loading = false;
  SessionFailure? failure;

  bool get ready => steps.length >= 2;

  Future<void> run({List<String>? steps, String? window}) async {
    if (steps != null) this.steps = steps;
    if (window != null) this.window = window;
    if (!ready) {
      value = null;
      failure = null;
      notifyListeners();
      return;
    }
    loading = true;
    failure = null;
    notifyListeners();
    try {
      value = await _client.rumFunnel(
        app,
        steps: this.steps,
        funnelWindow: this.window,
      );
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('sectionForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      loading = false;
      notifyListeners();
    }
  }
}
