// The signed-in app: the web's sections, the web's order, behind the web's
// drawer.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../alerts.dart';
import '../dashboards.dart';
import '../logs.dart';
import '../services.dart';
import '../session.dart';
import 'alerts_screen.dart';
import 'dashboards_screen.dart';
import 'logs_screen.dart';
import 'nav_drawer.dart';
import 'services_screen.dart';
import 'settings_screen.dart';

class AppShell extends StatefulWidget {
  const AppShell({
    super.key,
    required this.session,
    required this.alerts,
    required this.services,
    required this.logs,
    required this.dashboards,
  });

  final SessionController session;
  final AlertsController alerts;
  final ServicesController services;
  final LogsController logs;
  final DashboardsController dashboards;

  @override
  State<AppShell> createState() => _AppShellState();
}

class _AppShellState extends State<AppShell> {
  /// Alerts, not the first entry. The order of the list is the web's; where
  /// the app opens is this app's own answer, and an on-call app opens on what
  /// is firing.
  int _tab = 3;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final titles = [
      l.navApm,
      l.navLogs,
      l.navDashboards,
      l.alertsTitle,
      l.navSettings,
    ];
    final refreshers = <VoidCallback?>[
      widget.services.refresh,
      widget.logs.refresh,
      widget.dashboards.refresh,
      widget.alerts.refresh,
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
        onSignOut: widget.session.signOut,
        signOutEnabled: !widget.session.busy,
      ),
      // IndexedStack, not a switch: moving between sections must not reload
      // the list or lose where the person had scrolled to, which on an on-call
      // screen is the difference between checking two things and losing one.
      body: IndexedStack(
        index: _tab,
        children: [
          ServicesBody(session: widget.session, services: widget.services),
          LogsBody(session: widget.session, logs: widget.logs),
          DashboardsBody(
            session: widget.session,
            dashboards: widget.dashboards,
          ),
          AlertsBody(session: widget.session, alerts: widget.alerts),
          SettingsBody(session: widget.session),
        ],
      ),
    );
  }
}
