// The Kubernetes section: the web's four tabs, one cluster between them.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/kubernetes.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/kubernetes_screen.dart';

import 'kubernetes_test.dart' as data;

final _client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');

class ScriptedScope extends KubernetesScope {
  ScriptedScope({List<Map<String, Object?>>? clusters}) : super(_client) {
    this.clusters = [
      for (final c in clusters ?? [data.cluster('c1')])
        KubernetesCluster.fromJson(c),
    ];
    loaded = true;
  }

  @override
  Future<void> load() async {}
}

class ScriptedCluster extends KubernetesClusterController {
  ScriptedCluster() : super(_client) {
    detail = KubernetesClusterDetail.fromJson(data.clusterDetail('c1'));
    uid = 'c1';
  }

  final loads = <String>[];

  @override
  Future<void> load(String clusterUid) async {
    loads.add(clusterUid);
    detail = KubernetesClusterDetail.fromJson(data.clusterDetail(clusterUid));
    uid = clusterUid;
    notifyListeners();
  }
}

class ScriptedNodes extends KubernetesNodesController {
  ScriptedNodes() : super(_client) {
    items = [KubernetesNode.fromJson(data.node('node-a'))];
    loaded = true;
  }

  int refreshes = 0;

  @override
  Future<void> refresh() async {
    refreshes++;
    notifyListeners();
  }
}

class ScriptedWorkloads extends KubernetesWorkloadsController {
  ScriptedWorkloads() : super(_client) {
    items = [KubernetesWorkload.fromJson(data.workload('checkout'))];
    loaded = true;
  }

  int refreshes = 0;

  @override
  Future<void> refresh() async {
    refreshes++;
    notifyListeners();
  }
}

class ScriptedPods extends PodsController {
  ScriptedPods() : super(_client) {
    items = [KubernetesPod.fromJson(data.pod('checkout-7d9'))];
    loaded = true;
  }

  int refreshes = 0;

  @override
  Future<void> refresh() async {
    refreshes++;
    notifyListeners();
  }
}

Future<Sections> pump(
  WidgetTester tester, {
  List<Map<String, Object?>>? clusters,
  ScriptedCluster? cluster,
  ScriptedNodes? nodes,
  ScriptedWorkloads? workloads,
  ScriptedPods? pods,
}) async {
  final sections = Sections(
    client: _client,
    k8s: ScriptedScope(clusters: clusters),
    k8sCluster: cluster ?? ScriptedCluster(),
    k8sNodes: nodes ?? ScriptedNodes(),
    k8sWorkloads: workloads ?? ScriptedWorkloads(),
    pods: pods ?? ScriptedPods(),
  );
  addTearDown(sections.dispose);
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('tr'),
      home: Scaffold(
        body: KubernetesBody(
          session: SessionController(store: MemoryTokenStore()),
          sections: sections,
          active: true,
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return sections;
}

void main() {
  testWidgets('the section has the web\'s four tabs, in the web\'s order', (
    tester,
  ) async {
    await pump(tester);

    expect(
      [for (final t in tester.widgetList<Tab>(find.byType(Tab))) t.text],
      ['Genel bakış', 'İş yükleri', "Pod'lar", 'Düğümler'],
    );
  });

  testWidgets('the overview is the cluster, its warnings and its nodes', (
    tester,
  ) async {
    await pump(tester);

    expect(find.byKey(const Key('k8s-tile-nodes')), findsOneWidget);
    expect(find.text('2/3'), findsOneWidget);
    // Thirteen pods across every phase, not the nine that are running.
    expect(find.text('13'), findsOneWidget);
    // The warnings and the nodes are below the tiles on a phone screen.
    await tester.drag(
      find.byKey(const Key('k8s-tile-nodes')),
      const Offset(0, -400),
    );
    await tester.pumpAndSettle();
    expect(find.text('BackOff ×12'), findsOneWidget);
    await tester.drag(find.text('BackOff ×12'), const Offset(0, -400));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('k8s-node-uid-node-a')), findsOneWidget);
    // A node under memory pressure says so: it is not in the Ready
    // condition and it is why nothing schedules there.
    expect(find.text('MemoryPressure'), findsOneWidget);
    // A node under memory pressure says so: it is not in the Ready
    // condition and it is why nothing schedules there.
    expect(find.text('MemoryPressure'), findsOneWidget);
  });

  testWidgets('one cluster is the question for every tab', (tester) async {
    final cluster = ScriptedCluster();
    final nodes = ScriptedNodes();
    final workloads = ScriptedWorkloads();
    final pods = ScriptedPods();
    await pump(
      tester,
      clusters: [
        data.cluster('c1'),
        data.cluster('c2', name: 'staging'),
      ],
      cluster: cluster,
      nodes: nodes,
      workloads: workloads,
      pods: pods,
    );

    final before = (nodes.refreshes, workloads.refreshes, pods.refreshes);
    await tester.tap(find.byKey(const Key('k8s-cluster')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('staging').last);
    await tester.pumpAndSettle();

    // Every list that has been looked at is asked again; the cluster
    // screen follows too, or the tabs would be about two clusters.
    expect(cluster.loads.last, 'c2');
    expect(nodes.clusterUid, 'c2');
    expect(workloads.clusterUid, 'c2');
    expect(pods.clusterUid, 'c2');
    expect((
      nodes.refreshes,
      workloads.refreshes,
      pods.refreshes,
    ), isNot(before));
  });

  testWidgets('a filter is a chip that clears itself', (tester) async {
    final workloads = ScriptedWorkloads();
    await pump(tester, workloads: workloads);

    await tester.tap(find.byKey(const Key('k8s-tab-workloads')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('k8s-workload-namespace')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('pick-kube-system')));
    await tester.pumpAndSettle();

    expect(workloads.namespace, 'kube-system');
    expect(find.text('kube-system'), findsOneWidget);

    // The chip now clears the filter, which is the web's "Hepsi" option.
    // Chosen, the chip carries one icon and it is the one that clears it.
    await tester.tap(
      find
          .descendant(
            of: find.byKey(const Key('k8s-workload-namespace')),
            matching: find.byType(Icon),
          )
          .last,
    );
    await tester.pumpAndSettle();
    expect(workloads.namespace, '');
  });

  testWidgets('a workload opens its own pods, not the cluster\'s', (
    tester,
  ) async {
    final sections = await pump(tester);
    await tester.tap(find.byKey(const Key('k8s-tab-workloads')));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('k8s-workload-c1-default-checkout')));
    await tester.pumpAndSettle();

    // A screen of its own, with a list of its own: going back must not
    // leave the tab showing one workload's pods.
    expect(find.text('checkout'), findsWidgets);
    expect(sections.pods.workloadName, isEmpty);
  });
}
