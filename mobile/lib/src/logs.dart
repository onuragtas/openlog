// Recent log records, newest first.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';

class LogsController extends ListController<LogRecord> {
  LogsController(this._client);

  final OpenlogClient _client;

  /// Case-insensitive substring of the body.
  String query = '';

  /// `WARN` by default rather than everything: a phone screen holds about ten
  /// lines, and the ten most recent lines of an information-level stream are
  /// almost never the ten that matter.
  String severityMin = 'WARN';

  @override
  String get forbiddenKind => 'logsForbidden';

  @override
  Future<List<LogRecord>> fetch() async {
    final page = await _client.logs(q: query.trim(), severityMin: severityMin);
    return page.logs;
  }
}
