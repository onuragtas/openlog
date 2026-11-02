// The names of the sort columns of the infrastructure lists, and each list's
// columns in the order of the web's table.
//
// One word per column rather than one per list: "Name" sorted by here and
// "Ad" on the next screen would otherwise be two chances to disagree. These are
// the columns the web offers, which is fewer than the contract accepts on some
// lists.
import '../../l10n/app_localizations.dart';

const hostSorts = [
  'name',
  'os',
  'cpu',
  'memory',
  'disk',
  'load',
  'agent',
  'last_seen',
];
const containerSorts = [
  'name',
  'state',
  'image',
  'compose',
  'host',
  'last_seen',
];
const podSorts = [
  'name',
  'status',
  'restarts',
  'workload',
  'node',
  'ip',
  'age',
  'cluster',
];
const nodeSorts = ['name', 'status', 'kubelet', 'last_seen', 'cluster'];
const workloadSorts = ['name', 'kind', 'health', 'replicas', 'cluster'];
const vulnerabilitySorts = ['severity', 'advisory', 'score', 'hosts'];
const advisoryFindingSorts = [
  'severity',
  'host',
  'package',
  'version',
  'ecosystem',
];
const hostFindingSorts = [
  'severity',
  'advisory',
  'score',
  'package',
  'version',
];
const costHostSorts = ['cost', 'host', 'instance', 'idle'];
const costServiceSorts = ['cost', 'service', 'environment', 'containers'];
const profileSorts = [
  'samples',
  'service',
  'environment',
  'type',
  'total',
  'last_seen',
];

String infraSortLabel(L l, String column) => switch (column) {
  'name' => l.infraSortName,
  'os' => l.infraSortOs,
  'cpu' => l.infraSortCpu,
  'memory' => l.infraSortMemory,
  'disk' => l.infraSortDisk,
  'load' => l.infraSortLoad,
  'agent' => l.infraSortAgent,
  'last_seen' => l.infraSortLastSeen,
  'state' => l.infraSortState,
  'image' => l.infraSortImage,
  'compose' => l.infraSortCompose,
  'host' => l.infraSortHost,
  'status' => l.infraSortStatus,
  'restarts' => l.infraSortRestarts,
  'workload' => l.infraSortWorkload,
  'node' => l.infraSortNode,
  'ip' => l.infraSortIp,
  'age' => l.infraSortAge,
  'cluster' => l.infraSortCluster,
  'kind' => l.infraSortKind,
  'health' => l.infraSortHealth,
  'replicas' => l.infraSortReplicas,
  'kubelet' => l.infraSortKubelet,
  'instance' => l.infraSortInstance,
  'cost' => l.infraSortCost,
  'idle' => l.infraSortIdle,
  'service' => l.infraSortService,
  'environment' => l.infraSortEnvironment,
  'containers' => l.infraSortContainers,
  'type' => l.infraSortType,
  'total' => l.infraSortTotal,
  'samples' => l.infraSortSamples,
  'advisory' => l.infraSortAdvisory,
  'severity' => l.infraSortSeverity,
  'score' => l.infraSortScore,
  'hosts' => l.infraSortHosts,
  'package' => l.infraSortPackage,
  'version' => l.infraSortVersion,
  'ecosystem' => l.infraSortEcosystem,
  _ => column,
};
