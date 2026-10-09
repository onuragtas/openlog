// The signed-in app: the web's sections, the web's order, behind the web's
// drawer.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../sections.dart';
import '../session.dart';
import 'alerts_screen.dart';
import 'dashboards_screen.dart';
import 'logs_screen.dart';
import 'nav_drawer.dart';
import 'pod_screen.dart';
import 'profiles_screen.dart';
import 'query_screen.dart';
import 'rules_screen.dart';
import 'add_data_screen.dart';
import 'channels_screen.dart';
import 'container_screen.dart';
import 'costs_screen.dart';
import 'fleet_screen.dart';
import 'host_screen.dart';
import 'integrations_screen.dart';
import 'inventory_screen.dart';
import 'metrics_screen.dart';
import 'rum_screen.dart';
import 'traces_screen.dart';
import 'sections_screen.dart';
import 'services_screen.dart';
import 'settings_screen.dart';

class AppShell extends StatefulWidget {
  const AppShell({super.key, required this.session, required this.sections});

  final SessionController session;
  final Sections sections;

  @override
  State<AppShell> createState() => _AppShellState();
}

class _AppShellState extends State<AppShell> {
  /// Alerts, which is the last-but-one entry. The order of the list is the
  /// web's; where the app opens is this app's own answer, and an on-call app
  /// opens on what is firing.
  ///
  /// Counted from the end rather than written as a number: this was 11, then
  /// 12, 13, 14 as sections were added in the middle, and each time it was one
  /// more chance to open the app on the wrong screen.
  int _tab = navItems.length - 2;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final s = widget.sections;
    final session = widget.session;

    final titles = [
      l.navAddData,
      l.navHosts,
      l.navContainers,
      l.navCosts,
      l.navKubernetes,
      l.navIntegrations,
      l.navApm,
      l.navRum,
      l.navProfiles,
      l.navDatabases,
      l.navSlos,
      l.navSynthetics,
      l.navJobs,
      l.navVulnerabilities,
      l.navLogs,
      l.navTraces,
      l.navMetrics,
      l.navQuery,
      l.navDashboards,
      l.navInventory,
      l.navFleet,
      l.navRules,
      l.navChannels,
      l.alertsTitle,
      l.navSettings,
    ];
    final refreshers = <VoidCallback?>[
      s.onboarding.refresh,
      s.hosts.refresh,
      s.containers.refresh,
      s.costs.refresh,
      s.pods.refresh,
      s.integrations.refresh,
      s.services.refresh,
      s.rum.refresh,
      s.profiles.refresh,
      s.databases.refresh,
      s.slos.refresh,
      s.synthetics.refresh,
      s.jobs.refresh,
      s.vulnerabilities.refresh,
      s.logs.refresh,
      s.traces.refresh,
      s.metrics.refresh,
      null, // The console has nothing to refresh until a query is run.
      s.dashboards.refresh,
      s.inventory.refresh,
      s.fleet.refresh,
      s.rules.refresh,
      s.channels.refresh,
      s.alerts.refresh,
      null, // Settings reads what the session already knows.
    ];

    return Scaffold(
      appBar: AppBar(
        title: Text(titles[_tab]),
        actions: [
          if (refreshers[_tab] != null)
            IconButton(
              key: const Key('refresh'),
              tooltip: l.refresh,
              onPressed: refreshers[_tab],
              icon: const Icon(Icons.refresh),
            ),
        ],
      ),
      drawer: NavDrawer(
        selected: _tab,
        onSelect: (i) => setState(() => _tab = i),
        onSignOut: session.signOut,
        signOutEnabled: !session.busy,
      ),
      // IndexedStack, not a switch: moving between sections must not reload
      // the list or lose where the person had scrolled to, which on an on-call
      // screen is the difference between checking two things and losing one.
      //
      // Every child is built, but each section loads the first time it is
      // looked at rather than when the app opens: building thirteen screens is
      // cheap, asking the server thirteen questions nobody has yet is not.
      body: IndexedStack(index: _tab, children: _bodies(session, s)),
    );
  }

  /// The bodies, in the drawer's order.
  ///
  /// Each one is told whether it is the visible tab by its own position in
  /// this list rather than by a number written next to it. Those numbers were
  /// here, and inserting Browser in the middle silently moved every section
  /// after it off its own index -- which does not crash, it just makes a
  /// section load when a different one is looked at.
  List<Widget> _bodies(SessionController session, Sections s) {
    final out = <Widget>[];
    void add(Widget Function(bool active) build) =>
        out.add(build(_tab == out.length));

    add(
      (active) => AddDataBody(
        key: const Key('add-data-body'),
        session: session,
        onboarding: s.onboarding,
        active: active,
      ),
    );
    add(
      (active) => SectionBody(
        session: session,
        controller: s.hosts,
        searchKey: 'hosts-search',
        active: active,
        emptyTitle: (l) => l.hostsEmpty,
        card: (context, h) => hostCard(
          context,
          h,
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => HostScreen(
                session: session,
                sections: s,
                hostId: h.hostId,
                hostName: h.hostName.isEmpty ? h.hostId : h.hostName,
              ),
            ),
          ),
        ),
      ),
    );
    add(
      (active) => SectionBody(
        session: session,
        controller: s.containers,
        searchKey: 'containers-search',
        active: active,
        emptyTitle: (l) => l.containersEmpty,
        card: (context, x) => containerCard(
          context,
          x,
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => ContainerScreen(
                session: session,
                sections: s,
                containerId: x.containerId,
                name: x.name.isEmpty ? x.containerId : x.name,
              ),
            ),
          ),
        ),
      ),
    );
    add(
      (active) => CostsBody(
        key: const Key('costs-body'),
        session: session,
        costs: s.costs,
        active: active,
      ),
    );
    add(
      (active) => SectionBody(
        session: session,
        controller: s.pods,
        searchKey: 'pods-search',
        active: active,
        emptyTitle: (l) => l.podsEmpty,
        card: (context, p) => podCard(
          context,
          p,
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => PodScreen(
                session: session,
                sections: s,
                podUid: p.podUid,
                podName: p.podName,
              ),
            ),
          ),
        ),
      ),
    );
    add(
      (active) =>
          IntegrationsBody(session: session, sections: s, active: active),
    );
    add(
      (_) => ServicesBody(session: session, sections: s, services: s.services),
    );
    add(
      (active) => RumBody(
        key: const Key('rum-body'),
        session: session,
        sections: s,
        active: active,
      ),
    );
    add(
      (active) => ProfilesBody(session: session, sections: s, active: active),
    );
    add(
      (active) => SectionBody(
        session: session,
        controller: s.databases,
        searchKey: 'databases-search',
        active: active,
        emptyTitle: (l) => l.databasesEmpty,
        card: dbCard,
      ),
    );
    add(
      (active) => SectionBody(
        session: session,
        controller: s.slos,
        searchKey: 'slos-search',
        active: active,
        emptyTitle: (l) => l.slosEmpty,
        card: sloCard,
      ),
    );
    add(
      (active) => SectionBody(
        session: session,
        controller: s.synthetics,
        searchKey: 'synthetics-search',
        active: active,
        emptyTitle: (l) => l.syntheticsEmpty,
        card: syntheticCard,
      ),
    );
    add(
      (active) => SectionBody(
        session: session,
        controller: s.jobs,
        searchKey: 'jobs-search',
        active: active,
        emptyTitle: (l) => l.jobsEmpty,
        card: jobCard,
      ),
    );
    add(
      (active) => SectionBody(
        session: session,
        controller: s.vulnerabilities,
        searchKey: 'vulnerabilities-search',
        active: active,
        emptyTitle: (l) => l.vulnerabilitiesEmpty,
        card: vulnCard,
      ),
    );
    add((_) => LogsBody(session: session, logs: s.logs));
    add((active) => TracesBody(session: session, sections: s, active: active));
    add((active) => MetricsBody(session: session, sections: s, active: active));
    add((_) => QueryBody(session: session, query: s.query));
    add((_) => DashboardsBody(session: session, dashboards: s.dashboards));
    add(
      (active) => InventoryBody(session: session, sections: s, active: active),
    );
    add((active) => FleetBody(session: session, sections: s, active: active));
    add((active) => RulesBody(session: session, sections: s, active: active));
    add(
      (active) => ChannelsBody(session: session, sections: s, active: active),
    );
    add(
      (_) => AlertsBody(
        key: const Key('alerts-body'),
        session: session,
        sections: s,
        alerts: s.alerts,
      ),
    );
    add((_) => SettingsBody(session: session));
    return out;
  }
}
