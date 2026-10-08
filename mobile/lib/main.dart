// The openlog mobile console (docs/plan/11-mobile-console.md).
//
// Phase 1: pick an installation, sign in or create an account against it, and
// act in one of the person's organizations. The alerts, services and logs the
// app exists for come next, on top of this.
import 'package:flutter/material.dart';

import 'l10n/app_localizations.dart';
import 'src/api/client.dart';
import 'src/sections.dart';
import 'src/session.dart';
import 'src/storage/token_store.dart';
import 'src/ui/app_shell.dart';
import 'src/ui/server_screen.dart';
import 'src/ui/theme.dart';
import 'src/ui/sign_in_screen.dart';

void main() => runApp(OpenlogApp(store: SecureTokenStore()));

class OpenlogApp extends StatefulWidget {
  const OpenlogApp({
    super.key,
    required this.store,
    this.session,
    this.sections,
  });

  /// Where the device token is kept between launches.
  final TokenStore store;

  /// Injected by tests, which cannot reach Keychain or a real server.
  final SessionController? session;
  final Sections? sections;

  @override
  State<OpenlogApp> createState() => _OpenlogAppState();
}

class _OpenlogAppState extends State<OpenlogApp> {
  late final SessionController _session;
  late final bool _ownsSession;

  Sections? _sections;
  OpenlogClient? _sectionsClient;

  @override
  void initState() {
    super.initState();
    _ownsSession = widget.session == null;
    _session = widget.session ?? SessionController(store: widget.store);
    // Before the first frame decides anything: a stored token has to be
    // checked against the server, because it can have been revoked from the
    // web.
    _session.restore();
  }

  /// One set of section controllers per signed-in client. Keyed on the client
  /// rather than kept for the app's life: signing out and back in, or changing
  /// server, must not leave the previous installation's rows on screen.
  Sections? _sectionsFor(OpenlogClient? client) {
    if (widget.sections != null) return widget.sections;
    if (client == null) return null;
    if (!identical(_sectionsClient, client)) {
      _sections?.dispose();
      _sections = Sections(client: client);
      _sectionsClient = client;
    }
    return _sections;
  }

  @override
  void dispose() {
    _sections?.dispose();
    if (_ownsSession) _session.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      onGenerateTitle: (context) => L.of(context).appTitle,
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      // The web app's tokens, not a generated palette: see src/ui/theme.dart.
      theme: openlogTheme(Brightness.light),
      darkTheme: openlogTheme(Brightness.dark),
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
              final sections = _sectionsFor(_session.client);
              if (sections == null) {
                // Signed in with no client is not a state the controller
                // produces; a spinner beats crashing if it ever becomes one.
                return const Scaffold(
                  body: Center(child: CircularProgressIndicator()),
                );
              }
              return AppShell(session: _session, sections: sections);
          }
        },
      ),
    );
  }
}
