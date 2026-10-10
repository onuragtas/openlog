// Kubernetes: what the four lists ask for, and how a state is read.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/kubernetes.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

Map<String, Object?> cluster(
  String uid, {
  String name = 'prod',
  bool reporting = true,
  List<String> namespaces = const ['default', 'kube-system'],
}) => {
  'cluster_uid': uid,
  'cluster_name': name,
  'version': 'v1.31.2',
  'first_seen': '2026-10-01T09:00:00.000000000Z',
  'last_seen': '2026-10-10T09:00:00.000000000Z',
  'reporting': reporting,
  'nodes': 3,
  'nodes_ready': 2,
  'pods': {
    'Pending': 1,
    'Running': 9,
    'Succeeded': 2,
    'Failed': 1,
    'Unknown': 0,
  },
  'pods_not_ready': 2,
  'workloads': 7,
  'workloads_unhealthy': 1,
  'namespaces': namespaces,
};

Map<String, Object?> clusterDetail(String uid) => {
  ...cluster(uid),
  'workloads_by_kind': [
    {
      'kind': 'Deployment',
      'total': 5,
      'healthy': 4,
      'degraded': 1,
      'unavailable': 0,
      'unknown': 0,
    },
  ],
  'warning_events': [
    {
      'timestamp': '2026-10-10T08:59:00.000000000Z',
      'type': 'Warning',
      'reason': 'BackOff',
      'message': 'Back-off restarting failed container',
      'count': 12,
      'namespace': 'default',
      'object_kind': 'Pod',
      'object_name': 'checkout-7d9',
      'object_uid': 'pod-1',
      'source': 'kubelet',
      'cluster_uid': uid,
      'cluster_name': 'prod',
    },
  ],
  'cpu_usage': 1.5,
  'memory_working_set': 2147483648,
  'allocatable_cpu': 6.0,
  'allocatable_memory': 8589934592,
};

Map<String, Object?> node(String name) => {
  'cluster_uid': 'c1',
  'cluster_name': 'prod',
  'node_name': name,
  'node_uid': 'uid-$name',
  'ready': 'true',
  'unschedulable': false,
  'roles': ['control-plane'],
  'kubelet_version': 'v1.31.2',
  'os_image': 'Talos',
  'container_runtime': 'containerd',
  'internal_ip': '10.0.0.4',
  'created_at': null,
  'allocatable_cpu': 4.0,
  'allocatable_memory': 8589934592,
  'allocatable_pods': 110,
  'cpu_usage': 0.5,
  'memory_working_set': 1073741824,
  'pods': 12,
  'host_id': null,
  'host_name': null,
  'first_seen': '2026-10-01T09:00:00.000000000Z',
  'last_seen': '2026-10-10T09:00:00.000000000Z',
  'reporting': true,
  'conditions': [
    {'condition': 'Ready', 'status': 'true'},
    {'condition': 'MemoryPressure', 'status': 'true'},
  ],
};

Map<String, Object?> workload(String name) => {
  'cluster_uid': 'c1',
  'cluster_name': 'prod',
  'namespace': 'default',
  'kind': 'Deployment',
  'name': name,
  'uid': 'w-$name',
  'desired': 3,
  'ready': 2,
  'available': 2,
  'updated': 3,
  'health': 'degraded',
  'pods': 3,
  'restarts': 4,
  'cpu_usage': 0.25,
  'memory_working_set': 536870912,
  'cpu_sparkline': [
    [1760000000000, 0.2],
    [1760000060000, 0.3],
  ],
  'memory_sparkline': <Object>[],
  'created_at': null,
  'first_seen': '2026-10-01T09:00:00.000000000Z',
  'last_seen': '2026-10-10T09:00:00.000000000Z',
  'reporting': true,
};

Map<String, Object?> pod(String name, {String phase = 'Running'}) => {
  'cluster_uid': 'c1',
  'cluster_name': 'prod',
  'namespace': 'default',
  'pod_name': name,
  'pod_uid': 'p-$name',
  'node_name': 'node-a',
  'workload_kind': 'Deployment',
  'workload_name': 'checkout',
  'phase': phase,
  'ready': phase == 'Running',
  'reason': '',
  'status': phase,
  'restarts': 0,
  'pod_ip': '10.1.2.3',
  'qos_class': 'Burstable',
  'created_at': null,
  'started_at': null,
  'cpu_usage': 0.1,
  'memory_working_set': 134217728,
  'first_seen': '2026-10-01T09:00:00.000000000Z',
  'last_seen': '2026-10-10T09:00:00.000000000Z',
  'reporting': true,
};

void main() {
  group('reading a state the way the web reads it', () {
    test('CPU is milli below one core', () {
      expect(formatCores(null), '—');
      expect(formatCores(0.25), '250m');
      expect(formatCores(0.0004), '<1m');
      expect(formatCores(1.5), '1.5');
      expect(formatCores(1.25), '1.25');
      expect(formatCores(2), '2');
    });

    test('a ratio needs both halves', () {
      expect(usageRatio(1, 4), 0.25);
      expect(usageRatio(1, null), isNull);
      expect(usageRatio(1, 0), isNull);
    });

    test('a finished pod is not a broken pod', () {
      final finished = KubernetesPod.fromJson({
        ...pod('job-1', phase: 'Succeeded'),
        'reason': 'Completed',
      });
      expect(podStatus(finished).level, 'muted');
      expect(podStatus(finished).notReporting, isFalse);

      final crash = KubernetesPod.fromJson({
        ...pod('checkout-1'),
        'reason': 'CrashLoopBackOff',
        'status': 'CrashLoopBackOff',
      });
      expect(podStatus(crash).level, 'bad');
      expect(podStatus(crash).label, 'CrashLoopBackOff');

      // A pod that stopped reporting while it was running is not Running
      // any more; saying so is the whole point.
      final gone = KubernetesPod.fromJson({
        ...pod('checkout-2'),
        'reporting': false,
      });
      expect(podStatus(gone).notReporting, isTrue);

      final notReady = KubernetesPod.fromJson({
        ...pod('checkout-3'),
        'ready': false,
      });
      expect(podStatus(notReady).notReady, isTrue);
      expect(podStatus(notReady).level, 'warn');
    });

    test('a node says what is wrong besides Ready', () {
      final n = KubernetesNode.fromJson(node('node-a'));
      expect(nodeStatus(n), 'ready');
      expect(nodeProblems(n), ['MemoryPressure']);

      final down = KubernetesNode.fromJson({
        ...node('node-b'),
        'reporting': false,
      });
      expect(nodeStatus(down), 'notReporting');
    });

    test('every phase counts towards the cluster total', () {
      final c = KubernetesCluster.fromJson(cluster('c1'));
      expect(totalPods(c.pods), 13);
    });
  });

  test('the scope keeps one cluster for every tab', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {
        'clusters': [
          cluster('c1', name: 'prod'),
          cluster('c2', name: 'staging', namespaces: ['default', 'test']),
        ],
      }),
    );
    addTearDown(server.stop);
    final scope = KubernetesScope(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await scope.load();

    // Nothing chosen: the overview is about the first one, as the web's is.
    expect(scope.cluster?.clusterUid, 'c1');
    // Every namespace of every cluster, once, sorted.
    expect(scope.namespaces, ['default', 'kube-system', 'test']);
    expect(scope.choose('c2'), isTrue);
    expect(scope.namespaces, ['default', 'test']);
    // The same choice twice is not a reason to reload four lists.
    expect(scope.choose('c2'), isFalse);
  });

  test(
    'a cluster that stopped reporting stops filtering from behind',
    () async {
      var call = 0;
      final server = await FakeServer.start((req, seen) {
        call++;
        writeJson(req, 200, {
          'clusters': call == 1
              ? [cluster('c1'), cluster('c2')]
              : [cluster('c1')],
        });
      });
      addTearDown(server.stop);
      final scope = KubernetesScope(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await scope.load();
      scope.choose('c2');
      await scope.load();

      expect(scope.clusterUid, '');
    },
  );

  test('the lists send the cluster and the filters', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/nodes')) {
        writeJson(req, 200, {
          'nodes': [node('node-a')],
          'total': 4,
        });
      } else if (seen.path.endsWith('/workloads')) {
        writeJson(req, 200, {
          'workloads': [workload('checkout')],
          'total': 9,
          'step': '120s',
        });
      } else {
        writeJson(req, 200, {
          'pods': [pod('checkout-7d9')],
          'total': 2,
        });
      }
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';

    final nodes = KubernetesNodesController(client)
      ..clusterUid = 'c1'
      ..query = 'node';
    await nodes.refresh();
    final workloads = KubernetesWorkloadsController(client)
      ..clusterUid = 'c1'
      ..namespace = 'default'
      ..kind = 'Deployment'
      ..health = 'degraded';
    await workloads.refresh();
    final pods = PodsController(client)
      ..clusterUid = 'c1'
      ..namespace = 'default'
      ..phase = 'Running'
      ..node = 'node-a';
    await pods.refresh();

    expect(
      Uri.decodeQueryComponent(server.requests[0].query),
      'cluster_uid=c1&q=node',
    );
    expect(
      Uri.decodeQueryComponent(server.requests[1].query),
      'cluster_uid=c1&namespace=default&kind=Deployment&health=degraded',
    );
    expect(
      Uri.decodeQueryComponent(server.requests[2].query),
      'cluster_uid=c1&namespace=default&node=node-a&phase=Running',
    );
    // What was cut off is worth knowing: the list is not the whole truth.
    expect(nodes.total, 4);
    expect(workloads.total, 9);
    expect(pods.total, 2);
  });

  test(
    'the cluster screen refuses to show one cluster under another name',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/c2')) {
          writeJson(req, 404, {
            'error': {'code': 'not_found', 'message': 'no such cluster'},
          });
        } else {
          writeJson(req, 200, clusterDetail('c1'));
        }
      });
      addTearDown(server.stop);
      final c = KubernetesClusterController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.load('c1');
      expect(c.detail?.clusterUid, 'c1');
      expect(c.detail?.warningEvents.single.reason, 'BackOff');

      await c.load('c2');
      expect(c.detail, isNull);
      expect(c.uid, '');
      expect(c.failure?.kind, 'detailGone');
    },
  );

  test('events are the warnings unless asked otherwise', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(req, 200, {'events': <Object>[]}),
    );
    addTearDown(server.stop);
    final c = KubernetesEventsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    )..clusterUid = 'c1';

    await c.refresh();

    expect(
      Uri.decodeQueryComponent(server.requests.single.query),
      'limit=100&cluster_uid=c1&type=Warning',
    );
  });
}
