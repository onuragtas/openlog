// The app starts and offers the default address. Thin on purpose: the screens
// worth testing arrive with the sign-in flow.
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/main.dart';
import 'package:openlog_mobile/src/api/client.dart';

void main() {
  testWidgets('the app starts and shows the address it offers first', (
    tester,
  ) async {
    await tester.pumpWidget(const OpenlogApp());

    expect(find.text('openlog'), findsOneWidget);
    // Self-hosted: this is a pre-filled box, so the default has to be visible
    // rather than buried in a constant nobody reads.
    expect(find.byKey(const Key('default-server')), findsOneWidget);
    expect(find.text(defaultBaseUrl), findsOneWidget);
    expect(defaultBaseUrl, 'https://apm.resoft.org');
  });
}
