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

/// The entry spans of one service, with the filters the web's traces tab has.
///
/// A controller per service screen, like the overview and the inbox: the
/// filters belong to that visit.
class ServiceTracesController extends ListController<ApmTraceResult> {
  ServiceTracesController(this.client, this.serviceName);

  final OpenlogClient client;
  final String serviceName;

  String transaction = '';
  String minDurationMs = '';
  String maxDurationMs = '';
  bool errorsOnly = false;

  /// 'timestamp' (newest first) or 'duration' (slowest first).
  String sort = 'timestamp';

  /// `attr.<key>=<value>` filters, as typed. Parsed rather than sent raw so
  /// a typo becomes an empty filter here instead of a 400 there.
  Map<String, String> attributes = const {};

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmTraceResult>> fetch() async => (await client.apmTraces(
    service: serviceName,
    transaction: transaction,
    minDurationMs: minDurationMs,
    maxDurationMs: maxDurationMs,
    errorsOnly: errorsOnly,
    sort: sort,
    attributes: attributes,
  )).traces;
}

/// `k=v k2=v2` as typed into one field, as the server's `attr.` parameters.
///
/// Pairs without a key or without a value are dropped rather than sent: the
/// server answers 400 for `attr.=x`, and a half-typed filter is not a filter
/// anybody meant. At most ten, which is the server's limit.
Map<String, String> parseAttributeFilter(String text) {
  final out = <String, String>{};
  for (final part in text.split(RegExp(r'[\s,]+'))) {
    final i = part.indexOf('=');
    if (i <= 0 || i == part.length - 1) continue;
    out[part.substring(0, i)] = part.substring(i + 1);
    if (out.length == 10) break;
  }
  return out;
}
