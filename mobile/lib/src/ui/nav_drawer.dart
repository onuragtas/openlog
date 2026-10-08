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

/// Order taken from web/src/components/AppShell.tsx: apm, logs, dashboards,
/// alerts, settings.
final navItems = <NavItem>[
  NavItem(Icons.monitor_heart_outlined, (l) => l.navApm),
  NavItem(Icons.article_outlined, (l) => l.navLogs),
  NavItem(Icons.dashboard_outlined, (l) => l.navDashboards),
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
      width: 272,
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
                Text(
                  item.label(l),
                  style: TextStyle(
                    color: active ? c.accentForeground : c.foreground,
                    fontWeight: active ? FontWeight.w600 : FontWeight.w400,
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
