// Sign in, and create an account on a server that allows it.
import 'dart:io' show Platform;

import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import 'failure_text.dart';

/// A starting point for the device name. The person can change it, and should:
/// it is what they will pick this phone out by in their session list on the web.
String defaultDeviceName() {
  if (Platform.isIOS) return 'iPhone';
  if (Platform.isAndroid) return 'Android';
  return 'openlog mobile';
}

class SignInScreen extends StatefulWidget {
  const SignInScreen({super.key, required this.session});

  final SessionController session;

  @override
  State<SignInScreen> createState() => _SignInScreenState();
}

class _SignInScreenState extends State<SignInScreen> {
  final _email = TextEditingController();
  final _password = TextEditingController();
  final _device = TextEditingController(text: defaultDeviceName());
  final _name = TextEditingController();
  final _org = TextEditingController();

  bool _creating = false;
  String? _localError;

  @override
  void dispose() {
    for (final c in [_email, _password, _device, _name, _org]) {
      c.dispose();
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final session = widget.session;
    final config = session.authConfig;
    final baseUrl = session.baseUrl ?? '';

    // A server with no user accounts cannot be signed in to at all, so say that
    // instead of offering a form that can only fail.
    final staticMode = config?.mode == AuthConfigMode.static;

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
                  Text(
                    _creating ? l.signUpTitle : l.signInTitle,
                    style: text.headlineSmall,
                  ),
                  const SizedBox(height: 8),
                  Text(
                    _creating ? l.signUpDescription : l.signInDescription,
                    style: text.bodyMedium,
                  ),
                  const SizedBox(height: 4),
                  Text(
                    baseUrl,
                    style: text.bodySmall?.copyWith(
                      color: Theme.of(context).colorScheme.onSurfaceVariant,
                    ),
                  ),
                  const SizedBox(height: 20),
                  if (staticMode)
                    Text(l.signInStaticMode, style: text.bodyMedium)
                  else ...[
                    if (_creating) ...[
                      _field(_name, l.nameLabel, key: 'signup-name'),
                      const SizedBox(height: 12),
                      _field(
                        _org,
                        l.orgLabel,
                        key: 'signup-org',
                        hint: l.orgHint,
                      ),
                      const SizedBox(height: 12),
                    ],
                    _field(
                      _email,
                      l.emailLabel,
                      key: 'email',
                      hint: l.emailHint,
                      keyboard: TextInputType.emailAddress,
                    ),
                    const SizedBox(height: 12),
                    _field(
                      _password,
                      l.passwordLabel,
                      key: 'password',
                      obscure: true,
                    ),
                    if (_creating && config != null) ...[
                      const SizedBox(height: 6),
                      Text(
                        l.passwordHintMin(config.passwordMinLength),
                        style: text.bodySmall,
                      ),
                    ],
                    const SizedBox(height: 12),
                    _field(_device, l.deviceNameLabel, key: 'device-name'),
                    const SizedBox(height: 6),
                    Text(l.deviceNameHelp, style: text.bodySmall),
                    if (_creating &&
                        (config?.emailVerificationRequired ?? false)) ...[
                      const SizedBox(height: 10),
                      Text(l.signUpVerificationNote, style: text.bodySmall),
                    ],
                    const SizedBox(height: 20),
                    FilledButton(
                      key: const Key('submit'),
                      onPressed: session.busy ? null : _submit,
                      child: Text(_label(l, session.busy)),
                    ),
                    if (_localError != null) ...[
                      const SizedBox(height: 12),
                      Text(
                        _localError!,
                        style: TextStyle(
                          color: Theme.of(context).colorScheme.error,
                        ),
                      ),
                    ],
                    FailureBanner(failure: session.failure, baseUrl: baseUrl),
                    const SizedBox(height: 8),
                    // Only offered when the server offers it: showing it on a
                    // server with sign-up closed turns its own rule into an error.
                    if (config?.signupEnabled ?? false)
                      // Wrap, not Row: the prompt and the link together are
                      // wider than a narrow phone in Turkish, and a Row answers
                      // that with an overflow stripe instead of a second line.
                      Wrap(
                        alignment: WrapAlignment.center,
                        crossAxisAlignment: WrapCrossAlignment.center,
                        children: [
                          Text(_creating ? l.haveAccount : l.signUpPrompt),
                          TextButton(
                            key: const Key('toggle-signup'),
                            onPressed: () => setState(() {
                              _creating = !_creating;
                              _localError = null;
                            }),
                            child: Text(_creating ? l.toSignIn : l.signUpLink),
                          ),
                        ],
                      ),
                  ],
                  TextButton(
                    key: const Key('change-server'),
                    onPressed: session.forgetServer,
                    child: Text(l.serverChange),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _field(
    TextEditingController controller,
    String label, {
    required String key,
    String? hint,
    bool obscure = false,
    TextInputType? keyboard,
  }) {
    return TextField(
      key: Key(key),
      controller: controller,
      obscureText: obscure,
      autocorrect: false,
      enableSuggestions: false,
      keyboardType: keyboard,
      decoration: InputDecoration(
        labelText: label,
        hintText: hint,
        border: const OutlineInputBorder(),
      ),
    );
  }

  String _label(L l, bool busy) {
    if (_creating) return busy ? l.signUpSubmitting : l.signUpSubmit;
    return busy ? l.signInChecking : l.signInSubmit;
  }

  void _submit() {
    final l = L.of(context);
    final email = _email.text.trim();
    final password = _password.text;
    final device = _device.text.trim();

    // Checked here rather than by sending an empty form: a round trip to be
    // told the e-mail box is empty is a worse answer than an immediate one.
    if (email.isEmpty || password.isEmpty) {
      setState(() => _localError = l.signInRequired);
      return;
    }
    if (device.isEmpty) {
      setState(() => _localError = l.deviceNameRequired);
      return;
    }
    if (_creating && _org.text.trim().isEmpty) {
      setState(() => _localError = l.signUpRequired);
      return;
    }
    setState(() => _localError = null);

    if (_creating) {
      widget.session.signUp(
        email: email,
        password: password,
        name: _name.text.trim(),
        organizationName: _org.text.trim(),
        deviceName: device,
      );
    } else {
      widget.session.signIn(
        email: email,
        password: password,
        deviceName: device,
      );
    }
  }
}
