// Which traces are kept, and which are thrown away.
//
// The policy is a list of rules tried in order, the first match deciding the
// keep ratio, and a baseline for everything that matches none -- so the order
// is part of the answer, exactly as with alert routing.
//
// Editing happens on a copy. Nothing reaches the server until the save
// button, because a sampling policy applied halfway is a policy nobody
// chose: turning the rate limit down before adding the rule that protects
// the errors would throw away the errors in between.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

/// The rule types, in the contract's order.
const samplingRuleTypes = <TailSamplingRuleType>[
  TailSamplingRuleType.error,
  TailSamplingRuleType.latency,
  TailSamplingRuleType.service,
  TailSamplingRuleType.route,
  TailSamplingRuleType.attribute,
];

class SamplingController extends ChangeNotifier {
  SamplingController(this.client);

  final OpenlogClient client;

  /// What the server has. Null until it has been read.
  TailSamplingPolicyState? state;

  /// What is being edited. Equal to the server's until something is changed.
  TailSamplingPolicy? draft;

  TailSamplingPreview? preview;

  bool loading = false;
  bool busy = false;
  SessionFailure? failure;

  /// True when the draft differs from what the server has, which is what the
  /// save button waits for.
  bool get dirty {
    final server = state?.policy;
    final d = draft;
    if (server == null || d == null) return false;
    return !_samePolicy(server, d);
  }

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      final s = await client.tailSampling();
      state = s;
      draft = s.policy;
      preview = null;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// Replaces the draft. Every edit goes through here so the preview that
  /// belongs to the old draft is dropped rather than left looking current.
  void edit(TailSamplingPolicy next) {
    draft = next;
    preview = null;
    notifyListeners();
  }

  Future<void> estimate({int windowMinutes = 60}) async {
    final d = draft;
    if (d == null) return;
    busy = true;
    failure = null;
    notifyListeners();
    try {
      preview = await client.previewTailSampling(
        d,
        windowMinutes: windowMinutes,
      );
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> save() async {
    final d = draft;
    final s = state;
    if (d == null || s == null) return;
    busy = true;
    failure = null;
    notifyListeners();
    try {
      final saved = await client.putTailSampling(d, version: s.version);
      state = saved;
      draft = saved.policy;
      preview = null;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      // 409: somebody else saved while this was being edited. The draft is
      // kept -- it is work somebody did -- and the message says to reload.
      failure = e.status == 409
          ? const SessionFailure('samplingConflict', '')
          : _failureOf(e);
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  SessionFailure _failureOf(ApiException e) => switch (e.status) {
    403 => const SessionFailure('servicesForbidden', ''),
    // 404 in static auth mode: there is no store to put a policy in.
    404 => const SessionFailure('samplingUnavailable', ''),
    _ => SessionFailure('unexpected', e.message),
  };
}

bool _samePolicy(TailSamplingPolicy a, TailSamplingPolicy b) =>
    a.enabled == b.enabled &&
    a.baselineRatio == b.baselineRatio &&
    a.maxSpansPerSecond == b.maxSpansPerSecond &&
    a.rules.length == b.rules.length &&
    [for (var i = 0; i < a.rules.length; i++) _sameRule(a.rules[i], b.rules[i])]
        .every((same) => same);

bool _sameRule(TailSamplingRule a, TailSamplingRule b) =>
    a.name == b.name &&
    a.type == b.type &&
    a.ratio == b.ratio &&
    a.thresholdMs == b.thresholdMs &&
    a.service == b.service &&
    a.route == b.route &&
    a.key == b.key &&
    a.value == b.value &&
    _sameList(a.services, b.services);

bool _sameList(List<String>? a, List<String>? b) {
  if (a == null || b == null) return (a == null) == (b == null);
  if (a.length != b.length) return false;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return false;
  }
  return true;
}

/// A policy with one rule replaced, added, removed or moved.
///
/// Copies rather than mutation: `TailSamplingRule` is immutable, and the
/// preview has to be able to tell the old draft from the new one.
TailSamplingPolicy withRules(
  TailSamplingPolicy p,
  List<TailSamplingRule> rules,
) => TailSamplingPolicy(
  enabled: p.enabled,
  baselineRatio: p.baselineRatio,
  maxSpansPerSecond: p.maxSpansPerSecond,
  rules: rules,
);

/// Moves the rule at [from] to [to]. The order is the policy: the first
/// matching rule decides, so moving one changes what the later ones see.
List<TailSamplingRule> movedRules(
  List<TailSamplingRule> rules,
  int from,
  int to,
) {
  final next = [...rules];
  final moved = next.removeAt(from);
  next.insert(to > from ? to - 1 : to, moved);
  return next;
}
