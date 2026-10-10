// What the organization used this billing period, against its plan.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

/// The periods the server names, in the web's order.
const usagePeriods = <String>['current', 'previous'];

class UsageController extends ChangeNotifier {
  UsageController(this.client);

  final OpenlogClient client;

  UsageOverview? overview;

  /// `current`, `previous`, or a `YYYY-MM` month.
  String period = 'current';

  bool loading = false;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      overview = await client.usage(period: period);
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('usageForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      loading = false;
      notifyListeners();
    }
  }
}

/// Bytes as a person reads them.
///
/// Binary multiples, like the web's formatBytes: a gigabyte of ingest is
/// what the plan counts, and switching base between the two would make the
/// same number look different in two places.
String formatBytes(num bytes) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  var value = bytes.toDouble();
  var unit = 0;
  while (value.abs() >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  final shown = value >= 100 || unit == 0
      ? value.toStringAsFixed(0)
      : value.toStringAsFixed(1);
  return '$shown ${units[unit]}';
}

/// A count with thousands separated by a thin space, which both languages
/// read the same way.
String formatCount(num value) {
  final digits = value.round().abs().toString();
  final out = StringBuffer(value < 0 ? '-' : '');
  for (var i = 0; i < digits.length; i++) {
    if (i > 0 && (digits.length - i) % 3 == 0) out.write(' ');
    out.write(digits[i]);
  }
  return out.toString();
}

/// A quota as the screen says it: used, and what of.
///
/// `limit == 0` means unlimited, which is the contract's own convention --
/// not "a limit of zero", which would read as "nothing allowed".
String quotaValue(QuotaMetric m) {
  final used = m.metric == QuotaMetricMetric.ingestBytes
      ? formatBytes(m.used)
      : formatCount(m.used);
  if (m.limit == 0) return used;
  final limit = m.metric == QuotaMetricMetric.ingestBytes
      ? formatBytes(m.limit)
      : formatCount(m.limit);
  return '$used / $limit';
}

/// How full the ClickHouse disks are.
///
/// The disks belong to the operator, not to a tenant: in a multi-tenant
/// install their free space is nobody else's business, which is why this
/// takes an admin to read.
class StorageController extends ChangeNotifier {
  StorageController(this.client);

  final OpenlogClient client;

  DiskSpace? space;
  bool loading = false;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      space = await client.diskSpace();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('storageForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      loading = false;
      notifyListeners();
    }
  }
}
