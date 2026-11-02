// Who uses the application and whether they come back (analytics.md §6).
//
// These read over days and months, so the window picked for the other screens
// does not apply: an hour of DAU means nothing. The rules here are the web's
// (RumAnalyticsTab.tsx), and its test's example values are in analytics_test:
// a change is a count and a share, a rise from nothing has no share, and an
// unknown previous number is a dash rather than a zero.
//
// Each part is its own endpoint and fails on its own. A server older than one
// of them answers 404, and that part says so plainly: the rest still stand.
import 'package:flutter/foundation.dart';
import 'package:intl/intl.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'country_names.dart';
import 'list_controller.dart';
import 'session.dart';

/// The periods the web offers, in days. It opens on 28.
const analyticsPeriods = [7, 28, 90];
const analyticsGrains = ['day', 'week', 'month'];
const analyticsSourceBys = ['referrer', 'source', 'medium', 'campaign'];
const analyticsAudienceBys = [
  'country',
  'device',
  'model',
  'os',
  'browser',
  'version',
];

/// Columns of the retention table. Beyond this nobody reads it.
const analyticsMaxPeriods = 12;

/// How far back each retention grain reads: enough cohorts to compare.
const _retentionDays = {'day': 30, 'week': 180, 'month': 365};

/// A count as the web writes it: every digit and the reader's separators,
/// since these numbers are read against each other. Counts are extrapolated
/// by the sampling weight, so they can have decimals.
String formatCount(double v, String locale) {
  if (!v.isFinite) return '–';
  final format = NumberFormat.decimalPattern(locale)
    ..maximumFractionDigits = v.abs() < 10 ? 2 : 1;
  return format.format(v);
}

/// A share of a whole, such as a retention cell: whole percents.
String formatShare(double share) => '${(share * 100).round()}%';

/// How far a number moved in percent. One decimal, because a whole number
/// would show a 0.4% move as nothing having happened.
String formatChangeRate(double rate, String locale) {
  final format = NumberFormat.percentPattern(locale)..maximumFractionDigits = 1;
  return format.format(rate);
}

/// Which way a number went, which picks the arrow and the colour.
enum ChangeDirection { unknown, none, up, down }

/// How far a number moved, in words: the count and the share.
class AnalyticsChange {
  const AnalyticsChange(this.direction, this.text);

  /// What the web's `Delta` says for [change] and [rate].
  ///
  /// A null [change] is a previous number nobody can know: a dash. A null
  /// [rate] beside a real change is a rise from nothing, which has no share.
  factory AnalyticsChange.of(
    double? change,
    double? rate, {
    required String locale,
    required String noChange,
    required String fromNothing,
    String Function(double)? format,
  }) {
    if (change == null) {
      return const AnalyticsChange(ChangeDirection.unknown, '–');
    }
    if (change == 0) return AnalyticsChange(ChangeDirection.none, noChange);
    final up = change > 0;
    final sign = up ? '+' : '−';
    final amount = (format ?? (n) => formatCount(n, locale))(change.abs());
    final share = rate == null
        ? fromNothing
        : '($sign${formatChangeRate(rate.abs(), locale)})';
    return AnalyticsChange(
      up ? ChangeDirection.up : ChangeDirection.down,
      '$sign$amount $share',
    );
  }

  final ChangeDirection direction;
  final String text;
}

/// One named thing and its counts: an event, a source, a country.
@immutable
class BreakdownRow {
  const BreakdownRow(this.name, this.values, [this.previous = const []]);

  final String name;
  final List<double> values;

  /// The same counts a period ago. A null one was not sent, which is an older
  /// server or a period that is not kept, and shows no change at all.
  final List<double?> previous;

  double? previousOf(int i) => i < previous.length ? previous[i] : null;

  /// The change of value [i] against the previous period, or null when there
  /// is nothing to compare with. Worked out here because the server sends only
  /// the two numbers, the same way the web works it out.
  ({double change, double? rate})? changeOf(int i) {
    final before = previousOf(i);
    if (before == null) return null;
    final change = values[i] - before;
    return (change: change, rate: before == 0 ? null : change / before);
  }
}

/// Orders names as the web's collator does: numbers by value, and neither case
/// nor accents make a difference.
int compareNames(String a, String b) {
  final x = _fold(a);
  final y = _fold(b);
  var i = 0;
  var j = 0;
  while (i < x.length && j < y.length) {
    if (_isDigit(x.codeUnitAt(i)) && _isDigit(y.codeUnitAt(j))) {
      final startI = i;
      final startJ = j;
      while (i < x.length && _isDigit(x.codeUnitAt(i))) {
        i++;
      }
      while (j < y.length && _isDigit(y.codeUnitAt(j))) {
        j++;
      }
      final m = BigInt.parse(x.substring(startI, i));
      final n = BigInt.parse(y.substring(startJ, j));
      final c = m.compareTo(n);
      if (c != 0) return c;
      continue;
    }
    final c = x.codeUnitAt(i).compareTo(y.codeUnitAt(j));
    if (c != 0) return c;
    i++;
    j++;
  }
  return (x.length - i).compareTo(y.length - j);
}

bool _isDigit(int unit) => unit >= 0x30 && unit <= 0x39;

const _accents = {
  'à': 'a',
  'á': 'a',
  'â': 'a',
  'ã': 'a',
  'ä': 'a',
  'å': 'a',
  'ç': 'c',
  'è': 'e',
  'é': 'e',
  'ê': 'e',
  'ë': 'e',
  'ğ': 'g',
  'ì': 'i',
  'í': 'i',
  'î': 'i',
  'ï': 'i',
  'ı': 'i',
  'ñ': 'n',
  'ò': 'o',
  'ó': 'o',
  'ô': 'o',
  'õ': 'o',
  'ö': 'o',
  'ş': 's',
  'ù': 'u',
  'ú': 'u',
  'û': 'u',
  'ü': 'u',
  'ý': 'y',
  'ÿ': 'y',
};

String _fold(String s) => s
    .replaceAll('İ', 'i')
    .toLowerCase()
    .split('')
    .map((c) => _accents[c] ?? c)
    .join();

/// A list held whole and cut here, as on the web.
///
/// A list with a comparison is two lists merged after they are read: the server
/// takes the top 200 of each period and adds what only the previous one had. So
/// a page cut on the server would need both read again. Holding them all also
/// means a row that stopped altogether sorts with the rest at its zero and stays
/// reachable, which is the largest fall there is.
class BreakdownList extends PagedListController<BreakdownRow> {
  BreakdownList() : super(sort: 'value0', ascendingSorts: const {'name'});

  List<BreakdownRow> _rows = const [];

  @override
  String get forbiddenKind => 'sectionForbidden';

  /// Another dimension or period is another list, and its first page is the
  /// one that exists. The order the person picked stays.
  Future<void> setRows(List<BreakdownRow> rows) {
    _rows = rows;
    return refresh();
  }

  List<BreakdownRow> get _sorted {
    final column = sort.startsWith('value')
        ? int.parse(sort.substring('value'.length))
        : -1;
    final sign = dir == 'asc' ? 1 : -1;
    // List.sort is not stable and the web's is: the index settles a tie.
    final indexed = _rows.indexed.toList();
    indexed.sort((a, b) {
      final c = column < 0
          ? compareNames(a.$2.name, b.$2.name)
          : a.$2.values[column].compareTo(b.$2.values[column]);
      return c != 0 ? sign * c : a.$1.compareTo(b.$1);
    });
    return [for (final e in indexed) e.$2];
  }

  @override
  Future<ListPage<BreakdownRow>> fetchPage(int offset) async {
    final sorted = _sorted;
    final end = offset + limit;
    return ListPage(
      sorted.sublist(
        offset.clamp(0, sorted.length),
        end.clamp(0, sorted.length),
      ),
      hasMore: end < sorted.length,
    );
  }
}

/// One endpoint's answer, and the ways of not having one.
class AnalyticsPart<T> {
  T? value;
  SessionFailure? failure;

  /// This server does not have the endpoint: it answered 404.
  bool tooOld = false;

  /// Bumped by every load, so a slow answer to an old question is dropped.
  int _generation = 0;

  bool get pending => value == null && failure == null && !tooOld;
}

/// Everything on the analytics tab.
class AnalyticsController extends ChangeNotifier {
  AnalyticsController(this._client, this.app, {DateTime Function()? now})
    : _now = now ?? DateTime.now;

  final OpenlogClient _client;
  final String app;
  final DateTime Function() _now;

  int days = 28;
  String grain = 'week';
  String sourceBy = 'referrer';
  String audienceBy = 'country';

  final activity = AnalyticsPart<AnalyticsActivityResponse>();
  final compare = AnalyticsPart<AnalyticsCompareResponse>();
  final retention = AnalyticsPart<AnalyticsRetentionResponse>();
  final events = AnalyticsPart<AnalyticsEventList>();
  final acquisition = AnalyticsPart<AnalyticsAcquisitionResponse>();
  final audience = AnalyticsPart<AnalyticsAudienceResponse>();

  final eventRows = BreakdownList();
  final sourceRows = BreakdownList();
  final audienceRows = BreakdownList();

  String _language = 'en';
  String _direct = '(direct)';
  String _unknown = '(unknown)';
  bool _disposed = false;

  /// The words a row's name can be: they are sorted by, so they are part of
  /// the rows rather than of how the rows are drawn.
  void setLabels({
    required String language,
    required String direct,
    required String unknown,
  }) {
    if (language == _language && direct == _direct && unknown == _unknown) {
      return;
    }
    _language = language;
    _direct = direct;
    _unknown = unknown;
    if (acquisition.value != null) _fillSources();
    if (audience.value != null) _fillAudience();
  }

  /// True when nobody reported a country: a sentence says what to set.
  bool get noCountryRecorded {
    final rows = audience.value?.rows;
    return audienceBy == 'country' &&
        rows != null &&
        rows.isNotEmpty &&
        rows.every((r) => r.value.isEmpty);
  }

  /// How many period columns the retention table has.
  int get retentionColumns {
    var most = -1;
    for (final c in retention.value?.cohorts ?? const <AnalyticsCohort>[]) {
      for (final p in c.periods) {
        if (p.period > most) most = p.period;
      }
    }
    return most + 1 > analyticsMaxPeriods ? analyticsMaxPeriods : most + 1;
  }

  Future<void> refresh() => Future.wait([
    _loadActivity(),
    _loadCompare(),
    _loadRetention(),
    _loadEvents(),
    _loadAcquisition(),
    _loadAudience(),
  ]);

  Future<void> setDays(int value) {
    if (value == days) return Future.value();
    days = value;
    // The same lists over another period: the old rows stay until the new
    // ones arrive, under headings that still describe them.
    return Future.wait([
      _loadActivity(),
      _loadCompare(),
      _loadEvents(),
      _loadAcquisition(),
      _loadAudience(),
    ]);
  }

  Future<void> setGrain(String value) {
    if (value == grain) return Future.value();
    grain = value;
    // Another grain is another table: its rows under the old heading would
    // read as an answer to the new question.
    _clear(retention);
    return _loadRetention();
  }

  Future<void> setSourceBy(String value) {
    if (value == sourceBy) return Future.value();
    sourceBy = value;
    _clear(acquisition);
    return _loadAcquisition();
  }

  Future<void> setAudienceBy(String value) {
    if (value == audienceBy) return Future.value();
    audienceBy = value;
    _clear(audience);
    return _loadAudience();
  }

  void _clear(AnalyticsPart<dynamic> part) {
    part
      ..value = null
      ..failure = null
      ..tooOld = false;
    _notify();
  }

  Future<void> _loadActivity() =>
      _load(activity, () => _client.analyticsActivity(app, days: days));

  Future<void> _loadCompare() =>
      _load(compare, () => _client.analyticsCompare(app, days: days));

  Future<void> _loadRetention() {
    final since = _now().subtract(Duration(days: _retentionDays[grain] ?? 180));
    return _load(
      retention,
      () => _client.analyticsRetention(
        app,
        grain: grain,
        from: since.millisecondsSinceEpoch,
      ),
    );
  }

  Future<void> _loadEvents() => _load(
    events,
    () => _client.analyticsEvents(app, days: days),
    then: _fillEvents,
  );

  Future<void> _loadAcquisition() => _load(
    acquisition,
    () => _client.analyticsAcquisition(app, by: sourceBy, days: days),
    then: _fillSources,
  );

  Future<void> _loadAudience() => _load(
    audience,
    () => _client.analyticsAudience(app, by: audienceBy, days: days),
    then: _fillAudience,
  );

  void _fillEvents() {
    eventRows.setRows([
      for (final e in events.value?.events ?? const <AnalyticsEventRow>[])
        BreakdownRow(
          e.name,
          [e.count, e.sessions, e.users],
          [e.previousCount, e.previousSessions, e.previousUsers],
        ),
    ]);
  }

  void _fillSources() {
    sourceRows.setRows([
      for (final s in acquisition.value?.sources ?? const <AnalyticsSource>[])
        BreakdownRow(
          // An empty source is a direct visit, and it has to say so rather
          // than show a blank row.
          s.value.isEmpty ? _direct : s.value,
          [s.sessions, s.users],
          [s.previousSessions, s.previousUsers],
        ),
    ]);
  }

  void _fillAudience() {
    audienceRows.setRows([
      for (final r in audience.value?.rows ?? const <AnalyticsSource>[])
        BreakdownRow(
          r.value.isEmpty
              ? _unknown
              : audienceBy == 'country'
              ? countryName(r.value, _language)
              : r.value,
          [r.sessions, r.users],
          [r.previousSessions, r.previousUsers],
        ),
    ]);
  }

  Future<void> _load<T>(
    AnalyticsPart<T> part,
    Future<T> Function() read, {
    VoidCallback? then,
  }) async {
    final generation = ++part._generation;
    bool stale() => _disposed || generation != part._generation;
    try {
      final value = await read();
      if (stale()) return;
      part
        ..value = value
        ..failure = null
        ..tooOld = false;
      then?.call();
    } on ApiUnreachable {
      if (!stale()) part.failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      if (stale()) return;
      if (e.status == 404) {
        part
          ..tooOld = true
          ..value = null
          ..failure = null;
      } else {
        part.failure = e.status == 403
            ? const SessionFailure('sectionForbidden', '')
            : SessionFailure('unexpected', e.message);
      }
    } on ApiShapeError catch (e) {
      // A field this build needs and the server did not send takes this part
      // down and nothing else.
      if (!stale()) part.failure = SessionFailure('unexpected', e.toString());
    } finally {
      if (generation == part._generation) _notify();
    }
  }

  void _notify() {
    if (!_disposed) notifyListeners();
  }

  @override
  void dispose() {
    _disposed = true;
    eventRows.dispose();
    sourceRows.dispose();
    audienceRows.dispose();
    super.dispose();
  }
}
