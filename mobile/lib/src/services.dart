// Service health: where to look once an alert says something is wrong.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';

class ServicesController extends ListController<ApmService> {
  ServicesController(this._client);

  final OpenlogClient _client;

  /// The search box. Empty means every service with spans in the range.
  String query = '';

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmService>> fetch() async {
    final page = await _client.services(q: query.trim());
    // Worst first: on a phone the list is read from the top and rarely
    // scrolled, so the service that is actually broken has to be there.
    final list = [...page.services]
      ..sort((a, b) {
        final byError = b.errorRate.compareTo(a.errorRate);
        if (byError != 0) return byError;
        return b.throughput.compareTo(a.throughput);
      });
    return list;
  }
}
