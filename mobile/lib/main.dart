// The openlog mobile console (docs/plan/11-mobile-console.md).
//
// Phase 0 is the foundation under this screen, not the screen: the data classes
// in lib/src/api/schema.g.dart are generated from docs/contracts/openapi.yaml and
// CI fails when they and the contract disagree, and lib/src/api/client.dart
// speaks to one installation over HTTPS. The sign-in flow (§2, §3) is next, and
// arrives together with the Turkish and English dictionaries, which is why there
// is deliberately almost no user-facing wording here yet.
import 'package:flutter/material.dart';

import 'src/api/client.dart';

void main() => runApp(const OpenlogApp());

class OpenlogApp extends StatelessWidget {
  const OpenlogApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'openlog',
      theme: ThemeData(
        colorSchemeSeed: const Color(0xFF2F6FEB),
        useMaterial3: true,
      ),
      darkTheme: ThemeData(
        colorSchemeSeed: const Color(0xFF2F6FEB),
        brightness: Brightness.dark,
        useMaterial3: true,
      ),
      home: const GroundworkPage(),
    );
  }
}

/// Placeholder home, replaced by the server-address screen in the next phase.
class GroundworkPage extends StatelessWidget {
  const GroundworkPage({super.key});

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      body: SafeArea(
        child: Center(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                Text('openlog', style: text.headlineMedium),
                const SizedBox(height: 8),
                Text(
                  defaultBaseUrl,
                  key: const Key('default-server'),
                  style: text.bodyMedium?.copyWith(fontFamily: 'monospace'),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
