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

/// The rest of the usage screen: the shape of the period, who is storing
/// the most, where the organization stands against its quota, and the
/// limits its queries run under.
///
/// One controller rather than four: they are four requests of one screen and
/// four separate loading states on a phone would be four separate spinners
/// in a column.
class UsageDetailController extends ChangeNotifier {
  UsageDetailController(this.client);

  final OpenlogClient client;

  UsageDaily? daily;
  UsageTop? top;
  UsageStatus? status;
  OrgQueryLimits? limits;

  /// `current`, `previous`, or a `YYYY-MM` month -- kept with the overview's.
  String period = 'current';

  /// `service` or `host`.
  String by = 'service';

  bool loading = false;
  bool loaded = false;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      // In parallel: four sequential requests on a phone network is four
      // round trips for one screen.
      final results = await Future.wait([
        client.usageDaily(period: period),
        client.usageTop(period: period, by: by),
        client.usageStatus(),
        // Only PostgreSQL auth mode has query limits; a 404 there is "this
        // installation has none", not a failure of the screen.
        client.queryLimits().then<Object?>((v) => v).catchError((Object e) {
          if (e is ApiException && (e.status == 404 || e.status == 403)) {
            return null;
          }
          throw e;
        }),
      ]);
      daily = results[0] as UsageDaily;
      top = results[1] as UsageTop;
      status = results[2] as UsageStatus;
      limits = results[3] as OrgQueryLimits?;
      loaded = true;
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
