// What every list screen in this app does the same way.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'session.dart';

/// A screen's worth of rows, loaded from one installation.
///
/// The behaviour worth sharing is all about failure: a refresh that fails keeps
/// the rows already on screen rather than blanking what the person was reading,
/// only the first load covers the screen with a spinner, and a 403 is reported
/// as "you may not see this" rather than as an empty list, which looks
/// identical and means the opposite.
abstract class ListController<T> extends ChangeNotifier {
  List<T> items = const [];

  /// True only for the first load.
  bool loadingFirst = false;
  bool loaded = false;
  SessionFailure? failure;

  /// Reads one page. Subclasses also stash whatever else the answer carried.
  Future<List<T>> fetch();

  /// What a 403 means on this screen, named so the person knows it is a
  /// permission and not an outage.
  String get forbiddenKind;

  Future<void> refresh() async {
    if (!loaded) {
      loadingFirst = true;
      notifyListeners();
    }
    try {
      items = await fetch();
      failure = null;
      loaded = true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? SessionFailure(forbiddenKind, '')
          : SessionFailure('unexpected', e.message);
    } finally {
      loadingFirst = false;
      notifyListeners();
    }
  }
}
