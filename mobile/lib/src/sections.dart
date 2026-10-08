// The sections that are a list of things with a search box.
//
// One file, because they are one shape: fetch a page, keep the rows, let
// ListController handle what happens when the request fails. A section that
// needs more than that -- alerts can be acknowledged, dashboards run queries --
// has a file of its own.
import 'package:flutter/foundation.dart';

import 'alerts.dart';
import 'api/client.dart';
import 'api/schema.g.dart';
import 'dashboards.dart';
import 'list_controller.dart';
import 'logs.dart';
import 'services.dart';

/// Every section's controller, for one signed-in client.
///
/// One object rather than thirteen parameters: the shell needs all of them,
/// the app has to dispose all of them, and a list that long threaded through
/// two widgets is a list that will one day be missing an entry.
class Sections {
  Sections({
    required OpenlogClient client,
    HostsController? hosts,
    ContainersController? containers,
    PodsController? pods,
    ServicesController? services,
    DatabasesController? databases,
    SlosController? slos,
    SyntheticsController? synthetics,
    JobsController? jobs,
    VulnerabilitiesController? vulnerabilities,
    LogsController? logs,
    DashboardsController? dashboards,
    AlertsController? alerts,
  }) : hosts = hosts ?? HostsController(client),
       containers = containers ?? ContainersController(client),
       pods = pods ?? PodsController(client),
       services = services ?? ServicesController(client),
       databases = databases ?? DatabasesController(client),
       slos = slos ?? SlosController(client),
       synthetics = synthetics ?? SyntheticsController(client),
       jobs = jobs ?? JobsController(client),
       vulnerabilities = vulnerabilities ?? VulnerabilitiesController(client),
       logs = logs ?? LogsController(client),
       dashboards = dashboards ?? DashboardsController(client),
       alerts = alerts ?? AlertsController(client);

  final HostsController hosts;
  final ContainersController containers;
  final PodsController pods;
  final ServicesController services;
  final DatabasesController databases;
  final SlosController slos;
  final SyntheticsController synthetics;
  final JobsController jobs;
  final VulnerabilitiesController vulnerabilities;
  final LogsController logs;
  final DashboardsController dashboards;
  final AlertsController alerts;

  /// In the order the drawer lists them, which is the web's order.
  List<ChangeNotifier> get all => [
    hosts,
    containers,
    pods,
    services,
    databases,
    slos,
    synthetics,
    jobs,
    vulnerabilities,
    logs,
    dashboards,
    alerts,
  ];

  void dispose() {
    for (final c in all) {
      c.dispose();
    }
  }
}

/// Everything these sections share: a client, a search box, and a fetch.
abstract class SectionController<T> extends ListController<T> {
  SectionController(this.client);

  final OpenlogClient client;

  /// What is in the search box. Sent to the server rather than filtered here,
  /// so a search is not limited to the rows that fit in the first page.
  String query = '';

  @override
  String get forbiddenKind => 'sectionForbidden';
}

class HostsController extends SectionController<Host> {
  HostsController(super.client);

  @override
  Future<List<Host>> fetch() async => (await client.hosts(q: query)).hosts;
}

class ContainersController extends SectionController<ApiContainer> {
  ContainersController(super.client);

  @override
  Future<List<ApiContainer>> fetch() async =>
      (await client.containers(q: query)).containers;
}

class PodsController extends SectionController<KubernetesPod> {
  PodsController(super.client);

  @override
  Future<List<KubernetesPod>> fetch() async =>
      (await client.pods(q: query)).pods;
}

class SlosController extends SectionController<SloListItem> {
  SlosController(super.client);

  @override
  Future<List<SloListItem>> fetch() async {
    final slos = (await client.slos(q: query)).slos;
    // The ones in trouble first. An SLO list read top-down on a phone has to
    // start with the budget that is nearly gone, not with the alphabet.
    return [...slos]..sort((a, b) {
      final ar = a.status?.budget.remainingRatio ?? 2;
      final br = b.status?.budget.remainingRatio ?? 2;
      return ar.compareTo(br);
    });
  }
}

class SyntheticsController extends SectionController<SyntheticCheckListItem> {
  SyntheticsController(super.client);

  @override
  Future<List<SyntheticCheckListItem>> fetch() async =>
      (await client.synthetics(q: query)).checks;
}

class JobsController extends SectionController<JobMonitor> {
  JobsController(super.client);

  @override
  Future<List<JobMonitor>> fetch() async =>
      (await client.jobMonitors(q: query)).monitors;
}

class VulnerabilitiesController extends SectionController<VulnGroup> {
  VulnerabilitiesController(super.client);

  @override
  Future<List<VulnGroup>> fetch() async =>
      (await client.vulnerabilities(q: query)).vulnerabilities;
}

class DatabasesController extends SectionController<DbInstance> {
  DatabasesController(super.client);

  @override
  Future<List<DbInstance>> fetch() async =>
      (await client.dbInstances(q: query)).instances;
}
