// Alarmlar, as one section with the web's six tabs.
//
// The web has a single /alerts page with Olaylar, Kurallar, Şablonlar,
// Kanallar, Yönlendirme and Susturmalar inside it. This app used to put four
// of those in the drawer as sections of their own, which made the same
// product read as a different one: the drawer is the map people learn, and
// a map with four extra rooms is a different building.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../sections.dart';
import '../session.dart';
import 'alerts_screen.dart';
import 'channels_screen.dart';
import 'mutes_screen.dart';
import 'routes_screen.dart';
import 'rules_screen.dart';
import 'templates_screen.dart';

/// Which tab is which, in the web's order. The shell needs it to know what
/// its refresh button should reload.
const alertsTabCount = 6;

class AlertsSection extends StatefulWidget {
  const AlertsSection({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
    required this.onTab,
  });

  final SessionController session;
  final Sections sections;

  /// Whether the alerts section is the drawer's current choice.
  final bool active;

  /// Tells the shell which tab is open, so its refresh button reloads that
  /// one rather than all six.
  final ValueChanged<int> onTab;

  @override
  State<AlertsSection> createState() => _AlertsSectionState();
}

class _AlertsSectionState extends State<AlertsSection>
    with SingleTickerProviderStateMixin {
  late final TabController _tabs;

  @override
  void initState() {
    super.initState();
    _tabs = TabController(length: alertsTabCount, vsync: this)
      ..addListener(() {
        if (!_tabs.indexIsChanging) widget.onTab(_tabs.index);
      });
  }

  @override
  void dispose() {
    _tabs.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final s = widget.sections;
    final session = widget.session;
    // Each tab is built by TabBarView whether it is looked at or not, so
    // "active" has to mean the section is open *and* this is its tab --
    // otherwise opening Alarmlar would ask the server six questions.
    bool on(int i) => widget.active && _tabs.index == i;

    return Column(
      children: [
        TabBar(
          controller: _tabs,
          isScrollable: true,
          tabAlignment: TabAlignment.start,
          tabs: [
            Tab(key: const Key('alerts-tab-incidents'), text: l.navIncidents),
            Tab(key: const Key('alerts-tab-rules'), text: l.navRules),
            Tab(key: const Key('alerts-tab-templates'), text: l.navTemplates),
            Tab(key: const Key('alerts-tab-channels'), text: l.navChannels),
            Tab(key: const Key('alerts-tab-routing'), text: l.navRoutes),
            Tab(key: const Key('alerts-tab-mutes'), text: l.navMutes),
          ],
        ),
        Expanded(
          child: TabBarView(
            controller: _tabs,
            children: [
              AlertsBody(
                key: const Key('alerts-body'),
                session: session,
                sections: s,
                alerts: s.alerts,
              ),
              RulesBody(session: session, sections: s, active: on(1)),
              TemplatesBody(session: session, sections: s, active: on(2)),
              ChannelsBody(session: session, sections: s, active: on(3)),
              RoutesBody(session: session, sections: s, active: on(4)),
              MutesBody(session: session, sections: s, active: on(5)),
            ],
          ),
        ),
      ],
    );
  }
}
