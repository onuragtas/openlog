// The flame graph: the layout maths, and what the screen does with a tree
// too wide for a phone.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/ui/flame_view.dart';

FlameNode node(String name, int value, [List<FlameNode>? children]) =>
    FlameNode(name: name, value: value, children: children);

Future<void> pump(WidgetTester tester, FlameNode flame, {String unit = ''}) =>
    tester.pumpWidget(
      MaterialApp(
        localizationsDelegates: L.localizationsDelegates,
        supportedLocales: L.supportedLocales,
        locale: const Locale('tr'),
        home: Scaffold(
          body: SingleChildScrollView(
            child: SizedBox(
              width: 360,
              child: FlameView(flame: flame, unit: unit),
            ),
          ),
        ),
      ),
    );

void main() {
  test('a child is as wide a share of its parent as its value', () {
    final tree = node('all', 100, [
      node('a', 75, [node('a1', 50)]),
      node('b', 25),
    ]);
    final rows = <FlameRow>[];
    flatten(tree, 0, 0, 1, rows);

    expect([for (final r in rows) r.node.name], ['all', 'a', 'a1', 'b']);
    expect([for (final r in rows) r.depth], [0, 1, 2, 1]);
    // `a1` is two thirds of `a`, which is three quarters of everything.
    expect(rows[2].left, closeTo(0, 0.0001));
    expect(rows[2].width, closeTo(0.5, 0.0001));
    // `b` starts where `a` ends.
    expect(rows[3].left, closeTo(0.75, 0.0001));
    expect(rows[3].width, closeTo(0.25, 0.0001));
  });

  test('a frame keeps its colour wherever it appears', () {
    expect(flameColor('runtime.mallocgc'), flameColor('runtime.mallocgc'));
    expect(flameColor('a'), isNot(flameColor('b')));
    // The warm band a flame graph is drawn in: red through yellow.
    expect(HSLColor.fromColor(flameColor('anything')).hue, lessThan(60));
  });

  testWidgets('an empty profile says so rather than drawing nothing', (
    tester,
  ) async {
    await pump(tester, node('all', 0));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('flame-empty')), findsOneWidget);
  });

  testWidgets('tapping a frame re-roots the graph, and back goes up one', (
    tester,
  ) async {
    final tree = node('all', 100000, [
      node('serve', 80000, [node('db.query', 60000)]),
      node('gc', 20000),
    ]);
    await pump(tester, tree, unit: 'nanoseconds');
    await tester.pumpAndSettle();

    expect(find.text('all · 100 µs'), findsOneWidget);
    expect(find.byKey(const Key('flame-back')), findsNothing);

    await tester.tap(find.text('serve'));
    await tester.pumpAndSettle();

    // Zoomed in, the line above says what is being looked at and how much
    // of the whole profile it is -- without it the blocks below would read
    // as the whole thing.
    expect(find.text('serve · 80 µs · profilin %80.0\'i'), findsOneWidget);

    await tester.tap(find.byKey(const Key('flame-back')));
    await tester.pumpAndSettle();
    expect(find.text('all · 100 µs'), findsOneWidget);
  });

  testWidgets('frames too narrow to draw are counted, not dropped quietly', (
    tester,
  ) async {
    // Three hundred siblings of one unit each: at 360 points a block is a
    // pixel wide, which is neither readable nor tappable.
    final tree = node('all', 300, [
      for (var i = 0; i < 300; i++) node('fn$i', 1),
    ]);
    await pump(tester, tree);
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('flame-narrow')), findsOneWidget);
    expect(find.textContaining('300 çerçeve bu genişlikte'), findsOneWidget);
  });
}
