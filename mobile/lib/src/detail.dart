// One record, loaded because someone tapped a row.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'discovery.dart';
import 'session.dart';

/// A single record behind a detail screen.
///
/// `ListController`'s sibling, and it differs where detail screens differ. This
/// screen was reached by a tap, so a 404 is its own failure rather than an empty
/// page: the row that was tapped can describe something the server has since
/// purged, and "it is gone" is the one thing the person needs to hear.
abstract class DetailController<T> extends ChangeNotifier {
  T? value;

  /// True only for the first load, so a pull-to-refresh keeps what is on screen.
  bool loadingFirst = false;
  bool loaded = false;
  SessionFailure? failure;

  Future<T> fetch();

  /// What a 403 means here, named so the person knows it is a permission and
  /// not an outage.
  String get forbiddenKind;

  /// A screen whose status codes do not mean what they usually do says so
  /// here. Costs is the case: with OPENLOG_COST_ENABLED=false the server does
  /// not register the routes at all, so its 404 means "this installation does
  /// not estimate cost", not "that record is gone".
  SessionFailure? kindForStatus(int status) => null;

  Future<void> refresh() async {
    if (!loaded) {
      loadingFirst = true;
      notifyListeners();
    }
    try {
      value = await fetch();
      failure = null;
      loaded = true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure =
          kindForStatus(e.status) ??
          switch (e.status) {
            403 => SessionFailure(forbiddenKind, ''),
            404 => const SessionFailure('detailGone', ''),
            _ => SessionFailure('unexpected', e.message),
          };
    } finally {
      loadingFirst = false;
      notifyListeners();
    }
  }
}

/// One incident: its timeline, what was delivered, and what an on-call person
/// can do about it from a phone.
class IncidentController extends DetailController<AlertIncidentDetail> {
  IncidentController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  /// Which action is in flight, so the screen disables only that button.
  String? busy;

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<AlertIncidentDetail> fetch() => _client.incident(id);

  /// The service this incident is about, when the rule was about a service.
  ///
  /// APM conditions label their series with `service.name`
  /// (internal/alert/cond_apm.go), and that label is the whole reason this app
  /// can get from "what is firing" to "what is wrong" in one tap.
  String? get serviceName {
    final name = value?.labels['service.name'];
    return name == null || name.isEmpty ? null : name;
  }

  /// Everything except the `alert.` bookkeeping the rule engine adds, which is
  /// what the web shows too -- the person wants the labels they chose.
  Map<String, String> get labels {
    final all = value?.labels ?? const <String, String>{};
    return {
      for (final e in all.entries)
        if (!e.key.startsWith('alert.')) e.key: e.value,
    };
  }

  Future<void> acknowledge() =>
      _act('acknowledge', () => _client.acknowledgeIncident(id));

  Future<void> resolve({String note = ''}) =>
      _act('resolve', () => _client.resolveIncident(id, note: note));

  Future<void> addNote(String text) =>
      _act('note', () => _client.addIncidentNote(id, text));

  /// Runs [action] then reloads, so the timeline shows what the server did
  /// rather than this app's guess at it.
  Future<void> _act(String which, Future<void> Function() action) async {
    busy = which;
    failure = null;
    notifyListeners();
    try {
      await action();
      await refresh();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      // 409 is the race worth naming: it resolved between this screen being
      // drawn and the button being pressed. Reload first and report after,
      // because a successful refresh() clears `failure` and would throw the
      // message away, leaving a screen that silently changed under them.
      final reported = e.status == 409
          ? const SessionFailure('alreadyResolved', '')
          : SessionFailure('unexpected', e.message);
      if (e.status == 409) await refresh();
      failure = reported;
    } finally {
      busy = null;
      notifyListeners();
    }
  }
}

/// One service's golden signals: the numbers that say whether the alert is
/// still true, and the series that says how it got there.
class ServiceOverviewController extends DetailController<ApmOverview> {
  ServiceOverviewController(this._client, this.serviceName);

  final OpenlogClient _client;
  final String serviceName;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<ApmOverview> fetch() => _client.serviceOverview(serviceName);
}

/// One service's error inbox: what is breaking, worst first.
class ServiceErrorsController extends DetailController<ApmErrorInbox> {
  ServiceErrorsController(this._client, this.serviceName);

  final OpenlogClient _client;
  final String serviceName;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<ApmErrorInbox> fetch() => _client.serviceErrors(serviceName);

  /// Unresolved first, then by how often it happened in the window. The server
  /// sorts by its own default; a phone screen is read from the top and rarely
  /// scrolled, so what is still broken has to be there.
  List<ApmErrorGroup> get groups {
    final all = [...?value?.groups];
    all.sort((a, b) {
      final openA = a.status == ApmErrorStatus.unresolved ? 0 : 1;
      final openB = b.status == ApmErrorStatus.unresolved ? 0 : 1;
      if (openA != openB) return openA - openB;
      return b.count.compareTo(a.count);
    });
    return all;
  }
}

/// A span placed in the tree and on the timeline.
class SpanRow {
  const SpanRow({
    required this.span,
    required this.depth,
    required this.offset,
    required this.width,
  });

  final Span span;

  /// How many ancestors it has, for the indent.
  final int depth;

  /// Where it starts and how much of the trace it covers, both 0..1, so the
  /// bar can be drawn without the screen knowing anything about time.
  final double offset;
  final double width;
}

/// One trace, flattened into rows a list can draw.
class TraceController extends DetailController<Trace> {
  TraceController(this._client, this.traceId);

  final OpenlogClient _client;
  final String traceId;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<Trace> fetch() => _client.trace(traceId);

  /// Depth-first, children ordered by start: the order a person reads a trace.
  ///
  /// A root is a span whose parent is not in the answer, which covers both the
  /// real root (empty parent_span_id) and a trace that arrived incomplete --
  /// those spans would otherwise be dropped silently, which is the worst
  /// possible way to show a trace that is missing its middle.
  List<SpanRow> get rows {
    final spans = value?.spans ?? const <Span>[];
    if (spans.isEmpty) return const [];

    final byId = {for (final s in spans) s.spanId: s};
    final children = <String, List<Span>>{};
    final roots = <Span>[];
    for (final s in spans) {
      if (s.parentSpanId.isEmpty || !byId.containsKey(s.parentSpanId)) {
        roots.add(s);
      } else {
        (children[s.parentSpanId] ??= []).add(s);
      }
    }
    int byStart(Span a, Span b) => a.start.compareTo(b.start);
    roots.sort(byStart);
    for (final list in children.values) {
      list.sort(byStart);
    }

    // The trace's own window, from the earliest start to the latest end, so a
    // bar's width means "this share of the request" rather than "this share of
    // the root span", which is wrong whenever a child outlives its parent.
    final startUs = spans
        .map((s) => s.start.microsecondsSinceEpoch)
        .reduce((a, b) => a < b ? a : b);
    final endUs = spans
        .map((s) => s.start.microsecondsSinceEpoch + s.durationNs ~/ 1000)
        .reduce((a, b) => a > b ? a : b);
    final total = (endUs - startUs).toDouble();

    final out = <SpanRow>[];
    void walk(Span s, int depth) {
      final from = (s.start.microsecondsSinceEpoch - startUs).toDouble();
      final width = (s.durationNs / 1000).toDouble();
      out.add(
        SpanRow(
          span: s,
          depth: depth,
          // A trace whose spans all share one instant is not a division by
          // zero; it is a full-width bar, which is what instant means here.
          offset: total <= 0 ? 0 : from / total,
          width: total <= 0 ? 1 : (width / total).clamp(0.0, 1.0),
        ),
      );
      for (final c in children[s.spanId] ?? const <Span>[]) {
        walk(c, depth + 1);
      }
    }

    for (final r in roots) {
      walk(r, 0);
    }
    return out;
  }
}

/// One metric: its metadata and a series drawn with the aggregation the
/// server says is the right default for its type.
///
/// Two requests, in order, because the second depends on the first: which
/// aggregations mean anything is a property of the metric, so asking for a
/// series before reading the metadata would mean this app guessing.
class MetricController extends DetailController<MetricDetail> {
  MetricController(this._client, this.name);

  final OpenlogClient _client;
  final String name;

  MetricQueryResponse? series;

  /// Why the chart is missing while the metadata is on screen. A metric can
  /// describe itself and still have nothing to draw, and an empty space where
  /// a chart should be says nothing about which happened.
  String? seriesError;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<MetricDetail> fetch() async {
    final detail = await _client.metric(name);
    seriesError = null;
    series = null;
    try {
      series = await _client.metricSeries(
        name,
        aggregation: detail.defaultAggregation,
      );
    } on ApiUnreachable {
      rethrow;
    } on ApiException catch (e) {
      // The metadata came back, so the screen is worth showing; only the chart
      // is missing, and saying why beats failing the whole screen.
      seriesError = e.message;
    }
    return detail;
  }

  /// The first series' values, for the sparkline. One line: a metric can have
  /// fifty series and a phone can show one of them honestly or fifty of them
  /// as a smear.
  List<double> get values => [
    for (final p
        in series?.series.firstOrNull?.points ?? const <List<double>>[])
      if (p.length > 1) p[1],
  ];
}

/// One browser application: its Core Web Vitals and page views.
class RumOverviewController extends DetailController<RumOverview> {
  RumOverviewController(this._client, this.app);

  final OpenlogClient _client;
  final String app;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<RumOverview> fetch() => _client.rumOverview(app);

  /// Page views per bucket, for the sparkline.
  List<double> get views => [
    for (final p in value?.points ?? const <RumOverviewPointsItem>[]) p.views,
  ];
}

/// What the fleet costs: the summary, the hosts behind it, and where the
/// prices came from.
///
/// One request, because `/costs/hosts` answers all three at once -- and on a
/// phone the summary without the hosts is a number nobody can act on.
class CostsController extends DetailController<CostHostPage> {
  CostsController(this._client);

  final OpenlogClient _client;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  SessionFailure? kindForStatus(int status) => status == 404
      // With OPENLOG_COST_ENABLED=false the server never registers these
      // routes, so its 404 says the installation does not estimate cost at
      // all -- not that something went missing.
      ? const SessionFailure('costsOff', '')
      : null;

  @override
  Future<CostHostPage> fetch() => _client.costHosts();
}

/// The functions of one profile, ranked by self time.
class ProfileFunctionsController extends DetailController<ProfileFunctionPage> {
  ProfileFunctionsController(
    this._client, {
    required this.service,
    required this.type,
    required this.environment,
  });

  final OpenlogClient _client;
  final String service;
  final String type;
  final String environment;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<ProfileFunctionPage> fetch() => _client.profileFunctions(
    service: service,
    type: type,
    environment: environment,
  );

  /// A function's share of the rows on screen. The server says `total` is the
  /// sum of what it returned, not of the window, so this is a share of what
  /// the person can see -- which is the only share that can be checked.
  double shareOf(ProfileFunction f) {
    final total = value?.total ?? 0;
    return total <= 0 ? 0 : f.self / total;
  }
}

/// Where to send data: the endpoints, the pinned release and what this
/// installation supports.
class OnboardingController extends DetailController<Onboarding> {
  OnboardingController(this._client);

  final OpenlogClient _client;

  @override
  String get forbiddenKind => 'onboardingForbidden';

  @override
  Future<Onboarding> fetch() => _client.onboarding();
}

/// One host: what it is, how loaded it is, and what runs on it.
///
/// Two requests in one fetch. The second is the reason to open the screen at
/// all -- a host row already says the name and the load, and "what is actually
/// running here" is the thing the list cannot show.
class HostController extends DetailController<Host> {
  HostController(this._client, this.hostId);

  final OpenlogClient _client;
  final String hostId;

  /// The discovered services, worst integration status first, reusing the
  /// ordering the Integrations section uses so the two agree.
  List<IntegrationInstance> services = const [];

  /// Why the service list is missing while the host is on screen. A host can
  /// be in the hosts table and have no inventory snapshot yet.
  String? servicesError;

  /// The APM services whose spans came from this host -- a different list
  /// from the discovered ones above: that one is what the infra agent found
  /// running, this one is what sent traces.
  List<ApmHostService> apmServices = const [];

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<Host> fetch() async {
    final host = await _client.host(hostId);
    servicesError = null;
    services = const [];
    try {
      final snapshot = await _client.hostServices(hostId);
      services = [
        for (final item in snapshot.items)
          ?instanceOf(
            hostId: hostId,
            hostName: host.hostName,
            key: item.key,
            data: item.data,
          ),
      ]..sort(compareInstances);
    } on ApiUnreachable {
      rethrow;
    } on ApiException catch (e) {
      // The host itself came back, so the screen is worth showing.
      servicesError = e.message;
    }
    apmServices = const [];
    try {
      apmServices = (await _client.apmHostServices(hostId)).services;
    } on ApiUnreachable {
      rethrow;
    } on ApiException {
      // A host with no traces is the common case, not a problem worth a
      // message: the section simply does not appear.
    }
    return host;
  }
}

/// One container: what it is, and what it has been doing.
class ContainerController extends DetailController<ContainerDetail> {
  ContainerController(this._client, this.containerId);

  final OpenlogClient _client;
  final String containerId;

  ContainerTimeseries? series;

  /// Why the charts are missing while the container is on screen. A container
  /// in the list can have no samples in the window -- one that exited an hour
  /// ago is still in the 30-day retention.
  String? seriesError;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<ContainerDetail> fetch() async {
    final detail = await _client.container(containerId);
    seriesError = null;
    series = null;
    try {
      series = await _client.containerTimeseries(containerId);
    } on ApiUnreachable {
      rethrow;
    } on ApiException catch (e) {
      seriesError = e.message;
    }
    return detail;
  }

  /// The value column of a `[unix ms, value]` series.
  static List<double> values(List<List<double>> points) => [
    for (final p in points)
      if (p.length > 1) p[1],
  ];

  /// Memory as a share of the limit, where there is a limit. A container with
  /// no limit has no share -- it can use the host's memory, and plotting
  /// bytes against nothing would invent a ceiling.
  List<double> get memoryShare {
    final usage = series?.series.memoryUsage ?? const <List<double>>[];
    final limit = series?.series.memoryLimit ?? const <List<double>>[];
    final out = <double>[];
    for (var i = 0; i < usage.length && i < limit.length; i++) {
      if (usage[i].length < 2 || limit[i].length < 2) continue;
      final max = limit[i][1];
      if (max <= 0) continue;
      out.add(usage[i][1] / max);
    }
    return out;
  }
}

/// One pod: what it is, what it has been doing, and -- when it is not doing
/// it -- why.
///
/// Three requests. The events are the reason this screen exists: a pod that
/// will not start says nothing through its metrics, and everything through
/// `BackOff` and `FailedScheduling`.
class PodController extends DetailController<KubernetesPodDetail> {
  PodController(this._client, this.podUid);

  final OpenlogClient _client;
  final String podUid;

  KubernetesPodTimeseries? series;
  List<KubernetesEvent> events = const [];

  /// Kept apart so one missing piece does not hide the others.
  String? seriesError;
  String? eventsError;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<KubernetesPodDetail> fetch() async {
    final pod = await _client.pod(podUid);
    seriesError = null;
    eventsError = null;
    series = null;
    events = const [];
    try {
      series = await _client.podTimeseries(podUid);
    } on ApiUnreachable {
      rethrow;
    } on ApiException catch (e) {
      seriesError = e.message;
    }
    try {
      events = (await _client.podEvents(podUid)).events;
    } on ApiUnreachable {
      rethrow;
    } on ApiException catch (e) {
      eventsError = e.message;
    }
    return pod;
  }

  /// Warnings before anything else, newest first within each. A pod with
  /// thirty Normal events and one Warning is a pod with one problem, and the
  /// list has to open on it.
  List<KubernetesEvent> get sortedEvents {
    final out = [...events];
    out.sort((a, b) {
      final warn =
          (b.type == 'Warning' ? 1 : 0) - (a.type == 'Warning' ? 1 : 0);
      if (warn != 0) return warn;
      return b.timestamp.compareTo(a.timestamp);
    });
    return out;
  }
}

/// The person's own sessions, and the one write that belongs on a phone:
/// ending one of them.
///
/// This is the screen for the phone left in a taxi. Everything else about an
/// account -- members, API keys, roles -- is administration, and a pocket is
/// where those get tapped by accident.
class SessionsController extends DetailController<SessionPage> {
  SessionsController(this._client);

  final OpenlogClient _client;

  /// Which session is being ended, so only that row is busy.
  String? revoking;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<SessionPage> fetch() => _client.sessions();

  /// This device first, then the rest by when they were last used. The one
  /// the person is holding is the one they need to recognise to rule out.
  List<Session> get sessions {
    final out = [...?value?.sessions];
    out.sort((a, b) {
      if (a.current != b.current) return a.current ? -1 : 1;
      return b.lastSeenAt.compareTo(a.lastSeenAt);
    });
    return out;
  }

  /// Ends [id], then reloads so the list is the server's answer.
  ///
  /// The current session is not offered here: ending it is signing out, which
  /// has its own button and leaves the app in a state it knows how to be in.
  Future<void> revoke(String id) async {
    revoking = id;
    failure = null;
    notifyListeners();
    try {
      await _client.revokeSession(id);
      await refresh();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      // 404: it ended on its own between the list being drawn and the button
      // being pressed, which is not an error worth a red banner -- reload and
      // let the row disappear.
      if (e.status == 404) {
        await refresh();
      } else {
        failure = SessionFailure('unexpected', e.message);
      }
    } finally {
      revoking = null;
      notifyListeners();
    }
  }
}
