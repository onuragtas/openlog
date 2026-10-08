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
  int _tab = 11;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final s = widget.sections;
    final session = widget.session;

    final titles = [
      l.navHosts,
      l.navContainers,
      l.navKubernetes,
      l.navApm,
      l.navDatabases,
      l.navSlos,
      l.navSynthetics,
      l.navJobs,
      l.navVulnerabilities,
      l.navLogs,
      l.navDashboards,
      l.alertsTitle,
      l.navSettings,
    ];
    final refreshers = <VoidCallback?>[
      s.hosts.refresh,
      s.containers.refresh,
      s.pods.refresh,
      s.services.refresh,
      s.databases.refresh,
      s.slos.refresh,
      s.synthetics.refresh,
      s.jobs.refresh,
      s.vulnerabilities.refresh,
      s.logs.refresh,
      s.dashboards.refresh,
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
      body: IndexedStack(
        index: _tab,
        children: [
          SectionBody(
            session: session,
            controller: s.hosts,
            searchKey: 'hosts-search',
            active: _tab == 0,
            emptyTitle: (l) => l.hostsEmpty,
            card: hostCard,
          ),
          SectionBody(
            session: session,
            controller: s.containers,
            searchKey: 'containers-search',
            active: _tab == 1,
            emptyTitle: (l) => l.containersEmpty,
            card: containerCard,
          ),
          SectionBody(
            session: session,
            controller: s.pods,
            searchKey: 'pods-search',
            active: _tab == 2,
            emptyTitle: (l) => l.podsEmpty,
            card: podCard,
          ),
          ServicesBody(session: session, services: s.services),
          SectionBody(
            session: session,
            controller: s.databases,
            searchKey: 'databases-search',
            active: _tab == 4,
            emptyTitle: (l) => l.databasesEmpty,
            card: dbCard,
          ),
          SectionBody(
            session: session,
            controller: s.slos,
            searchKey: 'slos-search',
            active: _tab == 5,
            emptyTitle: (l) => l.slosEmpty,
            card: sloCard,
          ),
          SectionBody(
            session: session,
            controller: s.synthetics,
            searchKey: 'synthetics-search',
            active: _tab == 6,
            emptyTitle: (l) => l.syntheticsEmpty,
            card: syntheticCard,
          ),
          SectionBody(
            session: session,
            controller: s.jobs,
            searchKey: 'jobs-search',
            active: _tab == 7,
            emptyTitle: (l) => l.jobsEmpty,
            card: jobCard,
          ),
          SectionBody(
            session: session,
            controller: s.vulnerabilities,
            searchKey: 'vulnerabilities-search',
            active: _tab == 8,
            emptyTitle: (l) => l.vulnerabilitiesEmpty,
            card: vulnCard,
          ),
          LogsBody(session: session, logs: s.logs),
          DashboardsBody(session: session, dashboards: s.dashboards),
          AlertsBody(session: session, alerts: s.alerts),
          SettingsBody(session: session),
        ],
      ),
    );
  }
}
