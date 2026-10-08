// The web's sidebar, as a phone drawer.
//
// The web collapses the same flat list into a left Sheet below `lg`, so this
// is not a mobile-only invention: it is the same navigation, the same order
// and the same labels. Sections the app does not have yet are left out rather
// than listed as dead ends -- a drawer where four of nine entries do nothing
// is not the web's experience either.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import 'theme.dart';

/// One destination, in the order the web lists it.
class NavItem {
  const NavItem(this.icon, this.label);

  final IconData icon;
  final String Function(L) label;
}

/// Order taken from web/src/components/AppShell.tsx. The web lists
/// twenty-three; these are the ones this app has, in the same places.
final navItems = <NavItem>[
  NavItem(Icons.dns_outlined, (l) => l.navHosts),
  NavItem(Icons.inventory_2_outlined, (l) => l.navContainers),
  NavItem(Icons.account_balance_wallet_outlined, (l) => l.navCosts),
  NavItem(Icons.hub_outlined, (l) => l.navKubernetes),
  NavItem(Icons.monitor_heart_outlined, (l) => l.navApm),
  NavItem(Icons.devices_outlined, (l) => l.navRum),
  NavItem(Icons.storage_outlined, (l) => l.navDatabases),
  NavItem(Icons.track_changes_outlined, (l) => l.navSlos),
  NavItem(Icons.radar_outlined, (l) => l.navSynthetics),
  NavItem(Icons.event_repeat_outlined, (l) => l.navJobs),
  NavItem(Icons.gpp_maybe_outlined, (l) => l.navVulnerabilities),
  NavItem(Icons.article_outlined, (l) => l.navLogs),
  NavItem(Icons.account_tree_outlined, (l) => l.navTraces),
  NavItem(Icons.show_chart_outlined, (l) => l.navMetrics),
  NavItem(Icons.manage_search_outlined, (l) => l.navQuery),
  NavItem(Icons.dashboard_outlined, (l) => l.navDashboards),
  NavItem(Icons.inventory_outlined, (l) => l.navInventory),
  NavItem(Icons.notifications_outlined, (l) => l.navAlerts),
  NavItem(Icons.settings_outlined, (l) => l.navSettings),
];

class NavDrawer extends StatelessWidget {
  const NavDrawer({
    super.key,
    required this.selected,
    required this.onSelect,
    required this.onSignOut,
    required this.signOutEnabled,
  });

  final int selected;
  final ValueChanged<int> onSelect;
  final VoidCallback onSignOut;
  final bool signOutEnabled;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = colorsOf(context);
    final text = Theme.of(context).textTheme;

    return Drawer(
      width: 288,
      child: SafeArea(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 20, 20, 12),
              child: Text(
                'openlog',
                style: text.titleLarge?.copyWith(
                  color: c.primary,
                  fontWeight: FontWeight.w700,
                ),
              ),
            ),
            Expanded(
              child: ListView(
                padding: const EdgeInsets.symmetric(horizontal: 10),
                children: [
                  for (var i = 0; i < navItems.length; i++)
                    _Item(
                      item: navItems[i],
                      active: i == selected,
                      onTap: () {
                        Navigator.of(context).pop();
                        onSelect(i);
                      },
                    ),
                ],
              ),
            ),
            Divider(color: c.border, height: 1),
            Padding(
              padding: const EdgeInsets.all(10),
              child: TextButton.icon(
                key: const Key('sign-out'),
                onPressed: signOutEnabled ? onSignOut : null,
                icon: const Icon(Icons.logout, size: 18),
                label: Align(
                  alignment: Alignment.centerLeft,
                  child: Text(l.homeSignOut),
                ),
                style: TextButton.styleFrom(
                  foregroundColor: c.foreground,
                  alignment: Alignment.centerLeft,
                  padding: const EdgeInsets.symmetric(
                    horizontal: 12,
                    vertical: 12,
                  ),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Item extends StatelessWidget {
  const _Item({required this.item, required this.active, required this.onTap});

  final NavItem item;
  final bool active;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = colorsOf(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Material(
        color: active ? c.accent : Colors.transparent,
        borderRadius: BorderRadius.circular(Radii.md),
        child: InkWell(
          key: Key('nav-${item.label(l)}'),
          borderRadius: BorderRadius.circular(Radii.md),
          onTap: onTap,
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 11),
            child: Row(
              children: [
                Icon(
                  item.icon,
                  size: 18,
                  color: active ? c.accentForeground : c.mutedForeground,
                ),
                const SizedBox(width: 12),
                // Expanded, because the label is translated: "Güvenlik
                // açıkları" and "Sentetik izleme" overflow a fixed row where
                // "Vulnerabilities" and "Synthetics" fit, and an overflow
                // stripe is a bug nobody sees until the app is in Turkish.
                Expanded(
                  child: Text(
                    item.label(l),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      color: active ? c.accentForeground : c.foreground,
                      fontWeight: active ? FontWeight.w600 : FontWeight.w400,
                    ),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
