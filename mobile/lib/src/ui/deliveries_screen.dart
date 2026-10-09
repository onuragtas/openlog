// The delivery log: what was sent, where, and whether it arrived.
//
// Opened from the channels screen -- for one channel, or for all of them.
// The one question it answers is the one asked after an incident nobody woke
// up for: did the page actually go out, and if not, what did the receiver
// say.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../sections.dart';
import '../session.dart';
import 'delivery.dart';
import 'failure_text.dart';

/// The filter chips, in the order they are worth looking at: everything
/// first, then the two that mean something went wrong, then the rest.
const deliveryStatusFilters = <String>[
  '',
  'failed',
  'pending',
  'delivered',
  'suppressed',
];

class DeliveriesScreen extends StatefulWidget {
  const DeliveriesScreen({
    super.key,
    required this.session,
    required this.controller,
    this.channelName = '',
  });

  final SessionController session;
  final AlertDeliveriesController controller;

  /// Shown in the title when the log is about one channel.
  final String channelName;

  @override
  State<DeliveriesScreen> createState() => _DeliveriesScreenState();
}

class _DeliveriesScreenState extends State<DeliveriesScreen> {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback(
      (_) => widget.controller.refresh(),
    );
  }

  @override
  void dispose() {
    // Made for this screen, so it goes with it; the section controllers in
    // `Sections` are the ones that outlive their screen.
    widget.controller.dispose();
    super.dispose();
  }

  String _label(L l, String status) => switch (status) {
    'failed' => l.deliveryFailed,
    'pending' => l.deliveryPending,
    'delivered' => l.deliveryDelivered,
    'suppressed' => l.deliverySuppressed,
    _ => l.deliveriesAll,
  };

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.controller;
    final theme = Theme.of(context);

    return Scaffold(
      appBar: AppBar(
        title: Text(
          widget.channelName.isEmpty
              ? l.deliveriesTitle
              : l.deliveriesFor(widget.channelName),
        ),
        actions: [
          IconButton(
            key: const Key('deliveries-refresh'),
            tooltip: l.refresh,
            onPressed: c.refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      body: ListenableBuilder(
        listenable: c,
        builder: (context, _) => Column(
          children: [
            SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              padding: const EdgeInsets.fromLTRB(12, 10, 12, 4),
              child: Row(
                children: [
                  for (final status in deliveryStatusFilters)
                    Padding(
                      padding: const EdgeInsets.only(right: 8),
                      child: ChoiceChip(
                        key: Key(
                          'deliveries-filter-${status.isEmpty ? 'all' : status}',
                        ),
                        label: Text(_label(l, status)),
                        selected: c.status == status,
                        // The server does the filtering, so a chip is a new
                        // request rather than a view of what is already here.
                        onSelected: (_) {
                          if (c.status == status) return;
                          c.status = status;
                          c.refresh();
                        },
                      ),
                    ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
            ),
            Expanded(
              child: c.loadingFirst
                  ? const Center(child: CircularProgressIndicator())
                  : c.items.isEmpty
                  ? Center(
                      child: Text(
                        l.deliveriesEmpty,
                        key: const Key('deliveries-empty'),
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    )
                  : RefreshIndicator(
                      onRefresh: c.refresh,
                      // Separated rather than stacked: these rows are two to
                      // six lines each and without a line between them an
                      // attempt reads as belonging to the notification below.
                      child: ListView.separated(
                        padding: const EdgeInsets.fromLTRB(16, 8, 16, 28),
                        itemCount: c.items.length,
                        separatorBuilder: (context, _) =>
                            const Divider(height: 1),
                        itemBuilder: (context, i) => Padding(
                          padding: const EdgeInsets.only(top: 10),
                          child: DeliveryTile(
                            key: Key('delivery-${c.items[i].id}'),
                            delivery: c.items[i],
                            detailed: true,
                            showChannel: widget.channelName.isEmpty,
                          ),
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

/// Opens the log, for one channel or for all of them.
Future<void> openDeliveries(
  BuildContext context, {
  required SessionController session,
  required Sections sections,
  String channelId = '',
  String channelName = '',
}) => Navigator.of(context).push(
  MaterialPageRoute<void>(
    builder: (_) => DeliveriesScreen(
      session: session,
      controller: sections.deliveries(channelId),
      channelName: channelName,
    ),
  ),
);
