// Saved views, as a sheet.
//
// The web keeps this in a popover next to the page title: the list of views
// on top, a name and a visibility at the bottom to keep the current one. The
// same contents here, in the one shape a phone has for "a list and a small
// form": a bottom sheet, tall enough to type in.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../saved_views.dart';
import '../session.dart';
import 'failure_text.dart';

/// Opens the sheet. [state] is read when somebody saves, so it is the state
/// of the moment they pressed the button rather than of the moment the sheet
/// opened; [onApply] is told which view to show.
Future<void> openSavedViews(
  BuildContext context, {
  required SessionController session,
  required SavedViewsController controller,
  required Map<String, Object?> Function({Map<String, Object?> keep}) state,
  required void Function(SavedView view) onApply,
}) {
  if (!controller.loaded && !controller.loading) controller.load();
  return showModalBottomSheet<void>(
    context: context,
    isScrollControlled: true,
    useSafeArea: true,
    builder: (context) => _SavedViewsSheet(
      session: session,
      controller: controller,
      state: state,
      onApply: onApply,
    ),
  );
}

/// The button that opens the sheet, with the applied view's name on it.
///
/// Hidden entirely when this installation keeps no views: the endpoints are
/// PostgreSQL-only and answer 404 otherwise, and the web hides the button for
/// the same reason. Asking about them is left until the section is actually
/// looked at, like every other request on these screens.
class SavedViewsBar extends StatefulWidget {
  const SavedViewsBar({
    super.key,
    required this.session,
    required this.controller,
    required this.state,
    required this.onApply,
    required this.active,
  });

  final SessionController session;
  final SavedViewsController controller;
  final Map<String, Object?> Function({Map<String, Object?> keep}) state;
  final void Function(SavedView view) onApply;

  /// Whether the section this bar belongs to is the one on screen.
  final bool active;

  @override
  State<SavedViewsBar> createState() => _SavedViewsBarState();
}

class _SavedViewsBarState extends State<SavedViewsBar> {
  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(SavedViewsBar old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  void _loadIfVisible() {
    final c = widget.controller;
    if (!widget.active || c.loaded || c.loading) return;
    WidgetsBinding.instance.addPostFrameCallback((_) => c.load());
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.unavailable) return const SizedBox.shrink();
        final active = c.views.where((v) => v.id == c.activeId).firstOrNull;
        // A chip, and no padding of its own: it sits in the same row as
        // the conditions and the table options. It had a line to itself,
        // and four such lines left the log list reading through a slot.
        return ActionChip(
          key: const Key('saved-views'),
          avatar: const Icon(Icons.bookmark_border, size: 16),
          onPressed: () => openSavedViews(
            context,
            session: widget.session,
            controller: c,
            state: widget.state,
            onApply: widget.onApply,
          ),
          label: Text(
            active?.name ?? l.savedViewsButton,
            overflow: TextOverflow.ellipsis,
          ),
        );
      },
    );
  }
}

class _SavedViewsSheet extends StatefulWidget {
  const _SavedViewsSheet({
    required this.session,
    required this.controller,
    required this.state,
    required this.onApply,
  });

  final SessionController session;
  final SavedViewsController controller;
  final Map<String, Object?> Function({Map<String, Object?> keep}) state;
  final void Function(SavedView view) onApply;

  @override
  State<_SavedViewsSheet> createState() => _SavedViewsSheetState();
}

class _SavedViewsSheetState extends State<_SavedViewsSheet> {
  final _name = TextEditingController();
  String _visibility = 'private';

  /// Which row is asking "delete it?". One at a time, and the question is
  /// the button itself rather than a dialog on top of a sheet.
  String? _confirming;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  Future<void> _create() async {
    final name = _name.text.trim();
    // Nothing to save under no name. The button is disabled for this, but
    // the keyboard's own action reaches here too.
    if (name.isEmpty) return;
    final messenger = ScaffoldMessenger.of(context);
    final kept = L.of(context).savedViewKept(name);
    final view = await widget.controller.create(
      name: name,
      visibility: _visibility,
      state: widget.state(),
    );
    if (view == null || !mounted) return;
    _name.clear();
    // No apply: the state was read off this screen a moment ago, so
    // applying it would change nothing and the second message would
    // push "saved" off the screen before it was read.
    Navigator.of(context).pop();
    // The sheet closing is not an answer. A view that reached the server
    // and came back says so; one that did not keeps the sheet open with
    // the reason on it.
    messenger.showSnackBar(
      SnackBar(key: const Key('view-saved'), content: Text(kept)),
    );
  }

  Future<void> _overwrite(SavedView view) async {
    // The view's own state is passed back in, so the columns and the time
    // range a browser set survive being saved over from here.
    await widget.controller.overwrite(view, widget.state(keep: view.state));
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => Padding(
        // The keyboard is up while the name is being typed, and the form is
        // at the bottom of the sheet.
        padding: EdgeInsets.only(
          left: 16,
          right: 16,
          top: 16,
          bottom: MediaQuery.viewInsetsOf(context).bottom + 16,
        ),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(l.savedViewsTitle, style: theme.textTheme.titleMedium),
            const SizedBox(height: 2),
            Text(
              l.savedViewsAbout,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            FailureBanner(
              failure: c.failure,
              baseUrl: widget.session.baseUrl ?? '',
            ),
            const SizedBox(height: 8),
            if (c.loading && c.views.isEmpty)
              const Padding(
                padding: EdgeInsets.symmetric(vertical: 24),
                child: Center(child: CircularProgressIndicator()),
              )
            else if (c.views.isEmpty)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 12),
                child: Text(
                  l.savedViewsEmpty,
                  key: const Key('views-empty'),
                  style: theme.textTheme.bodyMedium?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              )
            else
              Flexible(
                child: ListView.separated(
                  shrinkWrap: true,
                  itemCount: c.views.length,
                  separatorBuilder: (context, _) => const Divider(height: 1),
                  itemBuilder: (context, i) => _row(context, l, c.views[i]),
                ),
              ),
            const Divider(height: 24),
            TextField(
              key: const Key('view-name'),
              controller: _name,
              maxLength: 200,
              textInputAction: TextInputAction.done,
              onSubmitted: (_) => _create(),
              // The save button follows the name, so the sheet rebuilds as
              // it is typed.
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                labelText: l.savedViewName,
                hintText: l.savedViewNameHint,
                border: const OutlineInputBorder(),
                isDense: true,
                counterText: '',
              ),
            ),
            const SizedBox(height: 8),
            // A row of its own: "Organizasyon" next to a button broke across
            // two lines inside its own segment.
            SizedBox(
              width: double.infinity,
              child: SegmentedButton<String>(
                key: const Key('view-visibility'),
                showSelectedIcon: false,
                segments: [
                  ButtonSegment(
                    value: 'private',
                    label: Text(l.savedViewPrivate),
                  ),
                  ButtonSegment(value: 'org', label: Text(l.savedViewOrg)),
                ],
                selected: {_visibility},
                onSelectionChanged: (s) =>
                    setState(() => _visibility = s.first),
              ),
            ),
            const SizedBox(height: 10),
            Align(
              alignment: Alignment.centerRight,
              child: FilledButton(
                key: const Key('view-save'),
                // Empty name, no save -- and the button shows that rather
                // than swallowing the tap, which is how a save that never
                // happened looks exactly like one that failed.
                onPressed: c.saving || _name.text.trim().isEmpty
                    ? null
                    : _create,
                child: Text(l.savedViewSave),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _row(BuildContext context, L l, SavedView view) {
    final theme = Theme.of(context);
    final c = widget.controller;
    final active = view.id == c.activeId;
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return InkWell(
      key: Key('view-${view.id}'),
      onTap: () {
        c.activate(view.id);
        widget.onApply(view);
        Navigator.of(context).pop();
      },
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 10),
        child: Row(
          children: [
            if (active)
              Padding(
                padding: const EdgeInsets.only(right: 6),
                child: Icon(
                  Icons.check,
                  size: 18,
                  color: theme.colorScheme.primary,
                ),
              ),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    view.name,
                    style: theme.textTheme.bodyLarge,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                  Text(
                    [
                      view.visibility == SavedViewVisibility.org
                          ? l.savedViewOrg
                          : l.savedViewPrivate,
                      // Whose view it is. On an org-wide list this is the
                      // difference between two views with similar names.
                      if (view.createdByEmail.isNotEmpty) view.createdByEmail,
                    ].join(' · '),
                    style: muted,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                ],
              ),
            ),
            // Only what the server would allow: the creator, or an admin for
            // an org-wide view. The others get a row they can apply and
            // nothing that answers 403.
            if (view.canEdit) ...[
              IconButton(
                key: Key('view-overwrite-${view.id}'),
                tooltip: l.savedViewOverwriteHint(view.name),
                onPressed: c.saving ? null : () => _overwrite(view),
                icon: const Icon(Icons.save_outlined),
              ),
              if (_confirming == view.id)
                TextButton(
                  key: Key('view-delete-confirm-${view.id}'),
                  onPressed: c.saving
                      ? null
                      : () async {
                          await c.remove(view.id);
                          if (mounted) setState(() => _confirming = null);
                        },
                  child: Text(
                    l.savedViewDeleteConfirm,
                    style: TextStyle(color: theme.colorScheme.error),
                  ),
                )
              else
                IconButton(
                  key: Key('view-delete-${view.id}'),
                  tooltip: l.savedViewDelete,
                  onPressed: () => setState(() => _confirming = view.id),
                  icon: const Icon(Icons.delete_outline),
                ),
            ],
          ],
        ),
      ),
    );
  }
}

/// Says what applying a view did: its name, what it could not show, or
/// that it carried nothing this screen can act on.
///
/// Silence is the one thing this must not do. A view whose conditions are
/// already on screen changes nothing, and without a word that is
/// indistinguishable from a tap that did not register.
void reportAppliedView(BuildContext context, SavedView view, ViewState state) {
  final l = L.of(context);
  final lines = [
    if (state.empty)
      l.savedViewNothingToApply
    else
      l.savedViewApplied(view.name),
    if (state.hiddenGroups > 0) l.savedViewGroupsIgnored(state.hiddenGroups),
    if (state.allSpans) l.savedViewAllSpans,
  ];
  ScaffoldMessenger.of(context).showSnackBar(
    SnackBar(
      key: const Key('view-applied'),
      content: Text(lines.join('\n')),
      duration: Duration(seconds: lines.length > 1 ? 6 : 3),
    ),
  );
}
