// What every list screen in this app does the same way.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart' show ApiShapeError;
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
    } on ApiShapeError catch (e) {
      // A field this build needs and the server does not send. Said where
      // the list is rather than thrown past it, which would leave a screen
      // that looks empty and an error nobody sees.
      failure = SessionFailure('unexpected', e.toString());
    } finally {
      loadingFirst = false;
      notifyListeners();
    }
  }
}

/// One page of a list the server sorts and cuts (api.md "Lists").
class ListPage<T> {
  const ListPage(this.items, {this.hasMore = false});

  final List<T> items;
  final bool hasMore;
}

/// A list too long to fetch at once, with the same sort and pages as the web.
///
/// A phone scrolls rather than turns pages, so a next page is added to the
/// rows on screen. An older server ignores `offset`, but it does not send
/// `has_more` either, so there is never a next page to fetch from it.
abstract class PagedListController<T> extends ListController<T> {
  PagedListController({
    required this.sort,
    this.dir = 'desc',
    this.limit = 50,
    this.ascendingSorts = const {},
  });

  String sort;
  String dir;
  final int limit;

  /// Sorts that read best from the start of the alphabet when first picked.
  /// Every other one starts with the largest, as on the web.
  final Set<String> ascendingSorts;

  bool hasMore = false;
  bool loadingMore = false;

  /// True when the last attempt at a next page failed. The rows already here
  /// stay, and the row at the foot of the list offers another try.
  bool loadMoreFailed = false;

  /// Bumped by every load from the top, so a next page that comes back after
  /// the list was re-sorted is thrown away rather than added to the wrong list.
  int _generation = 0;

  /// Reads the page starting at [offset].
  Future<ListPage<T>> fetchPage(int offset);

  @override
  Future<List<T>> fetch() async {
    _generation++;
    final page = await fetchPage(0);
    hasMore = page.hasMore;
    loadMoreFailed = false;
    return page.items;
  }

  /// Picks a column. The column already picked turns around.
  Future<void> setSort(String column) {
    if (column == sort) {
      dir = dir == 'asc' ? 'desc' : 'asc';
    } else {
      sort = column;
      dir = ascendingSorts.contains(column) ? 'asc' : 'desc';
    }
    return refresh();
  }

  Future<void> loadMore() async {
    if (!hasMore || loadingMore) return;
    final generation = _generation;
    loadingMore = true;
    loadMoreFailed = false;
    notifyListeners();
    try {
      final page = await fetchPage(items.length);
      if (generation != _generation) return;
      items = [...items, ...page.items];
      hasMore = page.hasMore;
    } on ApiUnreachable {
      loadMoreFailed = true;
    } on ApiException {
      loadMoreFailed = true;
    } on ApiShapeError {
      loadMoreFailed = true;
    } finally {
      loadingMore = false;
      notifyListeners();
    }
  }
}
