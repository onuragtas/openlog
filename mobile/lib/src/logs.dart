// Recent log records, newest first.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'fields.dart';
import 'list_controller.dart';
import 'log_fields.dart';

class LogsController extends ListController<LogQueryRow> {
  LogsController(
    this._client, {
    this.traceId = '',
    this.podUid = '',
    this.containerId = '',
  });

  final OpenlogClient _client;

  /// Case-insensitive substring of the body.
  String query = '';

  /// `WARN` by default rather than everything: a phone screen holds about ten
  /// lines, and the ten most recent lines of an information-level stream are
  /// almost never the ten that matter.
  String severityMin = 'WARN';

  /// What the filter builder added. Sent as the server's `filters`
  /// parameter, which is a JSON array of conditions.
  List<Filter> filters = const [];

  /// Which columns the rows are shown with, as the web keeps them: the
  /// first four are its defaults, anything else is picked from the
  /// dictionary and comes back in each row's `fields`.
  List<String> columns = [...defaultLogColumns];

  /// Oldest first instead of newest first, which is the web's other
  /// order. It is a question about a window, not a sort of the page:
  /// "the first errors after the deploy" is the top of the ascending
  /// list, not the bottom of the descending one.
  bool oldestFirst = false;

  /// Narrow to one service. Typed, because the screen cannot know the names
  /// without asking for them and this is a filter, not a picker.
  String service = '';

  /// Fixed by whoever opened the screen, not by the person reading it.
  ///
  /// A controller made for one request, one pod or one container answers only
  /// about that, and the screen says so instead of pretending the box is
  /// empty. This is what makes "the logs of this" reachable from a trace or a
  /// pod, which is the part a phone is worst at doing by typing.
  final String traceId;
  final String podUid;
  final String containerId;

  /// True when this list is about one thing rather than the whole stream.
  bool get scoped =>
      traceId.isNotEmpty || podUid.isNotEmpty || containerId.isNotEmpty;

  @override
  String get forbiddenKind => 'logsForbidden';

  @override
  Future<List<LogQueryRow>> fetch() async {
    final page = await _client.logs(
      q: query.trim(),
      filters: _conditions(),
      columns: requestColumns(columns),
      oldestFirst: oldestFirst,
    );
    return page.rows;
  }

  /// Everything this screen asks, as conditions.
  ///
  /// The explorer endpoint has no `service`, no `severity_min` and no
  /// `trace_id` of its own -- it has one list of conditions, and the named
  /// parameters of `GET /logs` are each a condition underneath. Writing
  /// them out here is what makes the service box, the severity button and
  /// a saved view's chips the same kind of thing, which is what they are
  /// in the browser too.
  ///
  /// The keys are the server's canonical ones (`internal/querybuilder`):
  /// `service.name`, `trace_id`, and the resource attributes a container's
  /// and a pod's logs carry.
  List<Map<String, Object?>> _conditions() => [
    for (final f in filters) f.toJson(),
    if (service.trim().isNotEmpty)
      {'key': 'service.name', 'op': '=', 'value': service.trim()},
    // A request's logs are all of them: narrowing one trace to WARN is how
    // you miss the line that explains it.
    if (!scoped && severityNumbers[severityMin] != null)
      {
        'key': 'severity_number',
        'op': '>=',
        'value': severityNumbers[severityMin],
      },
    if (traceId.isNotEmpty)
      {'key': 'trace_id', 'op': '=', 'value': traceId.toLowerCase()},
    if (podUid.isNotEmpty)
      {'key': 'resource.k8s.pod.uid', 'op': '=', 'value': podUid},
    if (containerId.isNotEmpty)
      {
        'key': 'resource.container.id',
        'op': '=',
        'value': containerId.toLowerCase(),
      },
  ];
}

/// What is being said in the logs, rather than what was said at 10:04.
///
/// The processor masks the variable parts of every body into a template, so
/// a thousand lines a minute become a few dozen patterns. The pattern id is
/// itself a filter key, which is how one pattern's records are listed.
class LogPatternsController extends ListController<LogPattern> {
  LogPatternsController(this.client);

  final OpenlogClient client;

  /// Shared with the log list: the same search box and the same filters,
  /// because these are two views of one question.
  String query = '';
  List<Filter> filters = const [];

  /// Matching records over every pattern, not only the ones returned.
  int total = 0;

  /// Records with no pattern at all: an empty body, or a record stored
  /// before patterns existed. Saying it keeps the counts honest.
  int unclassified = 0;

  /// The answer came from the hourly rollup, so the counts cover whole
  /// hours rather than the exact range.
  bool rollup = false;
  bool truncated = false;

  @override
  String get forbiddenKind => 'logsForbidden';

  @override
  Future<List<LogPattern>> fetch() async {
    final page = await client.logPatterns(
      q: query.trim(),
      filters: [for (final f in filters) f.toJson()],
    );
    total = page.total;
    unclassified = page.unclassified;
    rollup = page.rollup;
    truncated = page.truncated;
    return page.patterns;
  }
}
