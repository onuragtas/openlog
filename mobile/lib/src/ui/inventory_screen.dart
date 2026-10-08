// Inventory search: which hosts have this thing on them.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'sections_screen.dart';

/// The categories the web offers, in its order, with its words. Mobile and web
/// have to name the same thing the same way or the two read as two products.
const inventoryCategories = <String>[
  'os',
  'hardware',
  'package',
  'process',
  'listening_port',
  'systemd_unit',
  'kernel_module',
  'network_interface',
  'mount',
  'user',
  'launchd_service',
  'windows_service',
  'discovered_service',
];

String inventoryCategoryLabel(L l, String category) => switch (category) {
  'os' => l.invOs,
  'hardware' => l.invHardware,
  'package' => l.invPackage,
  'process' => l.invProcess,
  'listening_port' => l.invListeningPort,
  'systemd_unit' => l.invSystemdUnit,
  'kernel_module' => l.invKernelModule,
  'network_interface' => l.invNetworkInterface,
  'mount' => l.invMount,
  'user' => l.invUser,
  'launchd_service' => l.invLaunchdService,
  'windows_service' => l.invWindowsService,
  'discovered_service' => l.invDiscoveredService,
  // The API accepts any string, so an installation can report a category this
  // build has never heard of. Its own name beats "unknown".
  _ => category,
};

class InventoryBody extends StatelessWidget {
  const InventoryBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final c = sections.inventory;

    return SectionBody<InventorySearchItem>(
      session: session,
      controller: c,
      searchKey: 'inventory-search',
      searchHint: (l) => l.inventorySearch,
      active: active,
      emptyTitle: (l) => l.inventoryEmpty,
      header: ListenableBuilder(
        listenable: c,
        builder: (context, _) => _CategoryPicker(
          category: c.category,
          onChanged: (v) {
            c.category = v;
            c.refresh();
          },
        ),
      ),
      card: (context, item) => _ItemRow(item: item),
    );
  }
}

/// The category is part of the question, not a filter on the answer: the
/// server requires it and there is no "all", so it sits above the list rather
/// than beside the search box.
class _CategoryPicker extends StatelessWidget {
  const _CategoryPicker({required this.category, required this.onChanged});

  final String category;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: DropdownButtonFormField<String>(
        key: const Key('inventory-category'),
        initialValue: category,
        isDense: true,
        decoration: InputDecoration(
          labelText: l.inventoryCategory,
          border: const OutlineInputBorder(),
          isDense: true,
        ),
        items: [
          for (final c in inventoryCategories)
            DropdownMenuItem(
              value: c,
              child: Text(inventoryCategoryLabel(l, c)),
            ),
        ],
        onChanged: (v) {
          if (v != null) onChanged(v);
        },
      ),
    );
  }
}

class _ItemRow extends StatelessWidget {
  const _ItemRow({required this.item});

  final InventorySearchItem item;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final detail = _summarise(item.data);

    return Padding(
      key: Key('inv-${item.hostId}-${item.key}'),
      padding: const EdgeInsets.symmetric(vertical: 8, horizontal: 4),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  item.key,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              if (detail != null) ...[
                const SizedBox(width: 8),
                Text(
                  detail,
                  style: theme.textTheme.bodySmall?.copyWith(
                    fontWeight: FontWeight.w600,
                  ),
                ),
              ],
            ],
          ),
          const SizedBox(height: 2),
          Text(
            // The host name is omitted when the host is no longer in the
            // hosts table; its id still says which machine this was.
            item.hostName ?? item.hostId,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      ),
    );
  }
}

/// The one field worth putting on a phone row, if the body has it.
///
/// The body is whatever the category defines -- the contract says so and does
/// not type it -- so this reads the few keys that mean "which one of these is
/// it" and otherwise shows nothing rather than guessing.
String? _summarise(Object? data) {
  if (data is! Map) return null;
  for (final key in const ['version', 'state', 'port', 'value']) {
    final v = data[key];
    if (v != null && '$v'.isNotEmpty) return '$v';
  }
  return null;
}
