// Recent log records, newest first.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'fields.dart';
import 'list_controller.dart';

class LogsController extends ListController<LogRecord> {
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
  Future<List<LogRecord>> fetch() async {
    final page = await _client.logs(
      q: query.trim(),
      // A request's logs are all of them: narrowing one trace to WARN is how
      // you miss the line that explains it.
      severityMin: scoped ? '' : severityMin,
      service: service.trim(),
      traceId: traceId,
      podUid: podUid,
      containerId: containerId,
      filters: encodeFilters(filters),
    );
    return page.logs;
  }
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
