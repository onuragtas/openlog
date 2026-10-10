// How much arrived, and when.
//
// The chart above the web's log and trace explorers. One controller for
// both, because the two endpoints answer the same shape -- counts per
// bucket -- and traces add the percentiles of what was counted.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'fields.dart';
import 'session.dart';

class VolumeController extends ChangeNotifier {
  VolumeController(this.client, {required this.signal});

  final OpenlogClient client;

  /// `logs` or `traces`.
  final String signal;

  /// Counts per bucket, in the order the server returned them.
  List<double> counts = const [];

  /// p95 of span duration per bucket; empty for logs, which have none.
  List<double> p95 = const [];

  int total = 0;
  String step = '';

  bool loading = false;
  SessionFailure? failure;

  Future<void> load({String q = '', List<Filter> filters = const []}) async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      final wire = [for (final f in filters) f.toJson()];
      if (signal == 'traces') {
        final page = await client.traceVolume(q: q, filters: wire);
        total = page.total;
        step = page.step;
        counts = _points(page.series);
        p95 = [
          for (final p in page.latency.p95)
            if (p.length > 1) p[1],
        ];
      } else {
        final page = await client.logVolume(q: q, filters: wire);
        total = page.total;
        step = page.step;
        counts = _points(page.series);
        p95 = const [];
      }
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? SessionFailure(
              signal == 'traces' ? 'sectionForbidden' : 'logsForbidden',
              '',
            )
          : SessionFailure('unexpected', e.message);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// The buckets of every series added together.
  ///
  /// Without a `group_by` there is one series; with one there are several,
  /// and a chart of only the first would be a chart of part of the answer.
  List<double> _points(List<LogsAggregateSeries> series) {
    final out = <double>[];
    for (final s in series) {
      for (var i = 0; i < s.points.length; i++) {
        final point = s.points[i];
        if (point.length < 2) continue;
        if (i < out.length) {
          out[i] += point[1];
        } else {
          out.add(point[1]);
        }
      }
    }
    return out;
  }
}
