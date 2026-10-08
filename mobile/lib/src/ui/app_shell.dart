// The signed-in app: three lists, one account drawer.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../alerts.dart';
import '../logs.dart';
import '../services.dart';
import '../session.dart';
import 'account_drawer.dart';
import 'alerts_screen.dart';
import 'logs_screen.dart';
import 'services_screen.dart';

class AppShell extends StatefulWidget {
  const AppShell({
    super.key,
    required this.session,
    required this.alerts,
    required this.services,
    required this.logs,
  });

  final SessionController session;
  final AlertsController alerts;
  final ServicesController services;
  final LogsController logs;

  @override
  State<AppShell> createState() => _AppShellState();
}

class _AppShellState extends State<AppShell> {
  int _tab = 0;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final titles = [l.alertsTitle, l.navServices, l.navLogs];

    return Scaffold(
      appBar: AppBar(
        title: Text(titles[_tab]),
        actions: [
          IconButton(
            key: const Key('refresh'),
            tooltip: l.refresh,
            onPressed: switch (_tab) {
              0 => widget.alerts.refresh,
              1 => widget.services.refresh,
              _ => widget.logs.refresh,
            },
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      drawer: AccountDrawer(session: widget.session),
      // IndexedStack, not a switch: moving between tabs must not reload the
      // list or lose where the person had scrolled to, which on an on-call
      // screen is the difference between checking two things and losing one.
      body: SafeArea(
        child: IndexedStack(
          index: _tab,
          children: [
            AlertsBody(session: widget.session, alerts: widget.alerts),
            ServicesBody(session: widget.session, services: widget.services),
            LogsBody(session: widget.session, logs: widget.logs),
          ],
        ),
      ),
      bottomNavigationBar: NavigationBar(
        selectedIndex: _tab,
        onDestinationSelected: (i) => setState(() => _tab = i),
        destinations: [
          NavigationDestination(
            key: const Key('tab-alerts'),
            icon: const Icon(Icons.notifications_outlined),
            selectedIcon: const Icon(Icons.notifications),
            label: l.navAlerts,
          ),
          NavigationDestination(
            key: const Key('tab-services'),
            icon: const Icon(Icons.lan_outlined),
            selectedIcon: const Icon(Icons.lan),
            label: l.navServices,
          ),
          NavigationDestination(
            key: const Key('tab-logs'),
            icon: const Icon(Icons.article_outlined),
            selectedIcon: const Icon(Icons.article),
            label: l.navLogs,
          ),
        ],
      ),
    );
  }
}
