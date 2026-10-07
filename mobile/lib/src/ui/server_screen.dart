// The first screen, and the one that makes openlog's self-hosting visible:
// which installation is this app talking to.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/client.dart';
import '../session.dart';
import 'failure_text.dart';

class ServerScreen extends StatefulWidget {
  const ServerScreen({super.key, required this.session});

  final SessionController session;

  @override
  State<ServerScreen> createState() => _ServerScreenState();
}

class _ServerScreenState extends State<ServerScreen> {
  // Pre-filled with the hosted installation. Anyone running their own replaces
  // it, which is why this is a text field and not a choice between two buttons.
  final _address = TextEditingController(text: defaultBaseUrl);

  @override
  void dispose() {
    _address.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final session = widget.session;

    return Scaffold(
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 420),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Text(l.serverTitle, style: text.headlineSmall),
                  const SizedBox(height: 8),
                  Text(l.serverDescription, style: text.bodyMedium),
                  const SizedBox(height: 24),
                  TextField(
                    key: const Key('server-address'),
                    controller: _address,
                    autocorrect: false,
                    enableSuggestions: false,
                    keyboardType: TextInputType.url,
                    textInputAction: TextInputAction.go,
                    onSubmitted: (_) => _submit(),
                    decoration: InputDecoration(
                      labelText: l.serverAddressLabel,
                      border: const OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 16),
                  FilledButton(
                    key: const Key('server-continue'),
                    onPressed: session.busy ? null : _submit,
                    child: Text(
                      session.busy ? l.serverChecking : l.serverContinue,
                    ),
                  ),
                  FailureBanner(
                    failure: session.failure,
                    baseUrl: _address.text.trim(),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }

  void _submit() => widget.session.useServer(_address.text);
}
