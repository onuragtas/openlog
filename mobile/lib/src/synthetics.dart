// One synthetic check, loaded because somebody tapped a row in the list.
//
// The list already carries the name, the target and the last outcome; what it
// cannot carry is the period's summary, the schedule of every location and the
// runs that failed -- which is exactly what gets asked for when a check is red.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';

class SyntheticDetailController extends DetailController<SyntheticResults> {
  SyntheticDetailController(this._client, this.id);

  final OpenlogClient _client;
  final String id;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<SyntheticResults> fetch() => _client.syntheticResults(id);
}
