// The openlog mobile console (docs/plan/11-mobile-console.md).
//
// Phase 1: pick an installation, sign in or create an account against it, and
// act in one of the person's organizations. The alerts, services and logs the
// app exists for come next, on top of this.
import 'package:flutter/material.dart';

import 'l10n/app_localizations.dart';
import 'src/alerts.dart';
import 'src/api/client.dart';
import 'src/session.dart';
import 'src/storage/token_store.dart';
import 'src/ui/alerts_screen.dart';
import 'src/ui/server_screen.dart';
import 'src/ui/sign_in_screen.dart';

void main() => runApp(OpenlogApp(store: SecureTokenStore()));

class OpenlogApp extends StatefulWidget {
  const OpenlogApp({super.key, required this.store, this.session, this.alerts});

  /// Where the device token is kept between launches.
  final TokenStore store;

  /// Injected by tests, which cannot reach Keychain or a real server.
  final SessionController? session;

  /// Injected by tests so the alerts screen can be driven without a server.
  final AlertsController? alerts;

  @override
  State<OpenlogApp> createState() => _OpenlogAppState();
}

class _OpenlogAppState extends State<OpenlogApp> {
  late final SessionController _session;
  late final bool _ownsSession;

  @override
  void initState() {
    super.initState();
    _ownsSession = widget.session == null;
    _session = widget.session ?? SessionController(store: widget.store);
    // Before the first frame decides anything: a stored token has to be checked
    // against the server, because it can have been revoked from the web.
    _session.restore();
  }

  AlertsController? _alerts;
  OpenlogClient? _alertsClient;

  /// One alerts controller per signed-in client. Keyed on the client rather
  /// than kept for the app's life: signing out and back in, or changing server,
  /// must not leave the previous installation's incidents on screen.
  AlertsController? _alertsFor(OpenlogClient? client) {
    if (widget.alerts != null) return widget.alerts;
    if (client == null) return null;
    if (!identical(_alertsClient, client)) {
      _alerts?.dispose();
      _alerts = AlertsController(client);
      _alertsClient = client;
    }
    return _alerts;
  }

  @override
  void dispose() {
    _alerts?.dispose();
    if (_ownsSession) _session.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    const seed = Color(0xFF2F6FEB);
    return MaterialApp(
      onGenerateTitle: (context) => L.of(context).appTitle,
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      theme: ThemeData(colorSchemeSeed: seed, useMaterial3: true),
      darkTheme: ThemeData(
        colorSchemeSeed: seed,
        brightness: Brightness.dark,
        useMaterial3: true,
      ),
      home: ListenableBuilder(
        listenable: _session,
        builder: (context, _) {
          switch (_session.stage) {
            case SessionStage.restoring:
              return const Scaffold(
                body: Center(child: CircularProgressIndicator()),
              );
            case SessionStage.needsServer:
              return ServerScreen(session: _session);
            case SessionStage.needsSignIn:
              return SignInScreen(session: _session);
            case SessionStage.signedIn:
              final alerts = _alertsFor(_session.client);
              if (alerts == null) {
                // Signed in with no client is not a state the controller
                // produces; rendering a spinner is better than crashing if it
                // ever becomes one.
                return const Scaffold(
                  body: Center(child: CircularProgressIndicator()),
                );
              }
              return AlertsScreen(session: _session, alerts: alerts);
          }
        },
      ),
    );
  }
}
