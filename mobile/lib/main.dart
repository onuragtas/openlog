// The openlog mobile console (docs/plan/11-mobile-console.md).
//
// Phase 1: pick an installation, sign in or create an account against it, and
// act in one of the person's organizations. The alerts, services and logs the
// app exists for come next, on top of this.
import 'package:flutter/material.dart';

import 'l10n/app_localizations.dart';
import 'src/session.dart';
import 'src/storage/token_store.dart';
import 'src/ui/home_screen.dart';
import 'src/ui/server_screen.dart';
import 'src/ui/sign_in_screen.dart';

void main() => runApp(OpenlogApp(store: SecureTokenStore()));

class OpenlogApp extends StatefulWidget {
  const OpenlogApp({super.key, required this.store, this.session});

  /// Where the device token is kept between launches.
  final TokenStore store;

  /// Injected by tests, which cannot reach Keychain or a real server.
  final SessionController? session;

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

  @override
  void dispose() {
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
              return HomeScreen(session: _session);
          }
        },
      ),
    );
  }
}
