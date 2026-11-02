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
class RumPagesController extends PagedListController<RumPage> {
  RumPagesController(this._client, this.app)
    : super(sort: 'views', ascendingSorts: const {'route'});

  final OpenlogClient _client;
  final String app;

  /// Which kind of visitor the list is about: a device, a browser, or all.
  String deviceType = '';
  String browser = '';

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<ListPage<RumPage>> fetchPage(int offset) async {
    final page = await _client.rumPages(
      app,
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
      deviceType: deviceType,
      browser: browser,
    );
    return ListPage(page.pages, hasMore: page.hasMore ?? false);
  }
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

/// Visits of one application, newest first, as on the web.
class RumSessionsController extends PagedListController<RumSession> {
  RumSessionsController(this._client, this.app)
    : super(sort: 'started', ascendingSorts: const {'entry', 'browser'});

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
  Future<ListPage<RumSession>> fetchPage(int offset) async {
    final page = await _client.rumSessions(
      app,
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
      deviceType: deviceType,
      browser: browser,
      userId: userId,
    );
    return ListPage(page.sessions, hasMore: page.hasMore ?? false);
  }
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
///
/// The versions are the rows. The deployments come with every answer and are
/// not paged, so the first page's stay while later pages add versions.
class RumReleasesController extends PagedListController<RumRelease> {
  RumReleasesController(this._client, this.app) : super(sort: 'last_seen');

  final OpenlogClient _client;
  final String app;

  List<ApmDeployment> deployments = const [];

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<ListPage<RumRelease>> fetchPage(int offset) async {
    final page = await _client.rumReleases(
      app,
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
    );
    if (offset == 0) deployments = page.deployments;
    return ListPage(page.releases, hasMore: page.hasMore ?? false);
  }
}

/// What crashed a mobile application.
///
/// The endpoint is newer than the app's oldest servers. [serverTooOld] keeps
/// "this server cannot say" apart from "nothing crashed", which an empty list
/// would claim, and a 404 here is not a failure worth a red banner.
class RumCrashesController extends ListController<RumCrash> {
  RumCrashesController(this._client, this.app);

  final OpenlogClient _client;
  final String app;

  bool serverTooOld = false;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<RumCrash>> fetch() async {
    try {
      final crashes = (await _client.rumCrashes(app)).crashes;
      serverTooOld = false;
      return crashes;
    } on ApiException catch (e) {
      if (e.status != 404) rethrow;
      serverTooOld = true;
      return const [];
    }
  }
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
