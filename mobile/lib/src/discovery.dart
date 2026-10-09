// What the agents discovered, as both screens that show it understand it.
//
// Its own file because two screens read the same thing from two endpoints:
// the Integrations section from the inventory search, a host from its own
// snapshot. They have to agree about what a discovered service is and which
// one matters most, or the same machine reads differently depending on how
// you arrived at it.
import 'api/schema.g.dart';

/// One discovered integration instance, with the host it runs on.
class IntegrationInstance {
  const IntegrationInstance({
    required this.hostId,
    required this.hostName,
    required this.key,
    required this.service,
  });

  final String hostId;
  final String hostName;

  /// The inventory key, which is the executable path the agent matched.
  final String key;
  final DiscoveredService service;

  DiscoveredServiceIntegrationStatus get status =>
      service.integration?.status ??
      DiscoveredServiceIntegrationStatus.notAvailable;

  /// nginx, redis, mysql … or, when the agent did not recognise one, the
  /// discovery rule's own name.
  String get name =>
      service.integration?.id ?? service.name ?? service.ruleId ?? key;
}

/// One inventory item as an instance, or null when there is nothing to show.
///
/// Takes the pieces rather than an item, because the search endpoint and a
/// host's snapshot return two different shapes of the same thing.
IntegrationInstance? instanceOf({
  required String hostId,
  required String hostName,
  required String key,
  required Object? data,
}) {
  // A non-JSON body comes back as a string; the contract says so, and there
  // is nothing to show for one.
  if (data is! Map) return null;
  return IntegrationInstance(
    hostId: hostId,
    hostName: hostName,
    key: key,
    service: DiscoveredService.fromJson(data),
  );
}

/// What is wrong first, then what needs a hand, then the rest: the two that
/// want doing are the reason anybody opens either screen.
int compareInstances(IntegrationInstance a, IntegrationInstance b) {
  int rank(IntegrationInstance i) => switch (i.status) {
    DiscoveredServiceIntegrationStatus.error => 0,
    DiscoveredServiceIntegrationStatus.needsConfiguration => 1,
    DiscoveredServiceIntegrationStatus.enabled => 2,
    _ => 3,
  };
  final byStatus = rank(a) - rank(b);
  if (byStatus != 0) return byStatus;
  final byName = a.name.compareTo(b.name);
  return byName != 0 ? byName : a.hostName.compareTo(b.hostName);
}
