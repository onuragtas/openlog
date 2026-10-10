// The flame graph of one profile.
//
// The web's layout model, kept: every row is a stack depth and every block a
// frame as wide as its share of the value below it, so a wide block is where
// the time -- or the memory -- went. Blocks are laid out in fractions of the
// width, which is what lets the same picture work on a phone at all.
//
// What a phone changes is how you read it. A 360-point screen cannot show a
// frame that is a thousandth of the total, so tapping a block re-roots the
// graph on it; the web does the same, but there it is a convenience and here
// it is the only way in. A row says how many of its blocks were too narrow
// to draw, because a graph that silently leaves frames out would be a graph
// that lies about where the time went.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import 'profiles_screen.dart' show formatProfileValue;

/// Below this many logical pixels a block is neither readable nor tappable,
/// so it is counted instead of drawn.
const _minBlock = 3.0;

/// One block: the frame, how deep it sits and where it starts.
class FlameRow {
  const FlameRow(this.node, this.depth, this.left, this.width);

  final FlameNode node;
  final int depth;

  /// Fractions of the whole, left edge and width.
  final double left;
  final double width;
}

/// Lays a tree out left to right: a child is as wide a share of its parent
/// as its value is of the parent's value.
void flatten(
  FlameNode node,
  int depth,
  double left,
  double width,
  List<FlameRow> out,
) {
  out.add(FlameRow(node, depth, left, width));
  final total = node.value == 0 ? 1 : node.value;
  var x = left;
  for (final child in node.children ?? const <FlameNode>[]) {
    final w = child.value / total * width;
    flatten(child, depth + 1, x, w, out);
    x += w;
  }
}

/// A warm hue per frame name, the web's own: the same function keeps its
/// colour across reloads and zooms, which is what makes a flame graph
/// readable at a glance.
Color flameColor(String name) {
  var h = 0;
  for (final unit in name.codeUnits) {
    h = (h * 31 + unit) % 360;
  }
  return HSLColor.fromAHSL(1, (h % 55).toDouble(), 0.85, 0.62).toColor();
}

class FlameView extends StatefulWidget {
  const FlameView({super.key, required this.flame, required this.unit});

  final FlameNode flame;
  final String unit;

  @override
  State<FlameView> createState() => _FlameViewState();
}

class _FlameViewState extends State<FlameView> {
  /// The frames zoomed into, outermost first, so going back is one step
  /// rather than back to the top.
  final _path = <FlameNode>[];

  static const _rowHeight = 22.0;

  @override
  void didUpdateWidget(FlameView old) {
    super.didUpdateWidget(old);
    // A reload is a different tree; the frame zoomed into may not be in it.
    if (!identical(old.flame, widget.flame)) _path.clear();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final root = _path.isEmpty ? widget.flame : _path.last;

    if (widget.flame.value == 0) {
      return Padding(
        padding: const EdgeInsets.symmetric(vertical: 32),
        child: Text(
          l.flameEmpty,
          key: const Key('flame-empty'),
          textAlign: TextAlign.center,
          style: theme.textTheme.bodyMedium?.copyWith(
            color: theme.colorScheme.onSurfaceVariant,
          ),
        ),
      );
    }

    final rows = <FlameRow>[];
    flatten(root, 0, 0, 1, rows);
    final depth = rows.fold<int>(0, (d, r) => r.depth > d ? r.depth : d) + 1;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Expanded(
              child: Text(
                l.flameZoomHint,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ),
            if (_path.isNotEmpty)
              TextButton(
                key: const Key('flame-back'),
                onPressed: () => setState(_path.removeLast),
                child: Text(l.flameBack),
              ),
          ],
        ),
        const SizedBox(height: 4),
        // What is being looked at, and what share of the whole profile it
        // is. Zoomed in, the blocks below are shares of this frame, and
        // without this line there would be nothing saying so.
        Text(
          '${root.name} · ${formatProfileValue(root.value, widget.unit)}'
          '${_path.isEmpty ? '' : ' · ${l.flameShareOfAll(_share(root))}'}',
          key: const Key('flame-root'),
          style: theme.textTheme.bodyMedium,
        ),
        const SizedBox(height: 8),
        LayoutBuilder(
          builder: (context, constraints) {
            final width = constraints.maxWidth;
            final shown = [
              for (final r in rows)
                if (r.width * width >= _minBlock) r,
            ];
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                SizedBox(
                  height: depth * _rowHeight,
                  child: Stack(
                    children: [
                      for (final r in shown)
                        Positioned(
                          left: r.left * width,
                          top: r.depth * _rowHeight,
                          width: r.width * width,
                          height: _rowHeight - 1,
                          child: _Block(
                            row: r,
                            unit: widget.unit,
                            share: root.value == 0
                                ? 0
                                : r.node.value / root.value,
                            onTap: r.node.children?.isEmpty ?? true
                                ? null
                                : () => setState(() => _path.add(r.node)),
                          ),
                        ),
                    ],
                  ),
                ),
                if (shown.length < rows.length)
                  Padding(
                    padding: const EdgeInsets.only(top: 8),
                    child: Text(
                      l.flameTooNarrow(rows.length - shown.length),
                      key: const Key('flame-narrow'),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  ),
              ],
            );
          },
        ),
      ],
    );
  }

  /// A frame's share of the whole profile, as a rounded percentage.
  String _share(FlameNode node) {
    final total = widget.flame.value;
    if (total == 0) return '0';
    return (node.value / total * 100).toStringAsFixed(1);
  }
}

class _Block extends StatelessWidget {
  const _Block({
    required this.row,
    required this.unit,
    required this.share,
    required this.onTap,
  });

  final FlameRow row;
  final String unit;
  final double share;

  /// Null for a frame that called nothing: zooming into it would show one
  /// block and nothing under it.
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final label =
        '${row.node.name} · ${formatProfileValue(row.node.value, unit)} · '
        '${(share * 100).toStringAsFixed(1)}%';

    return Tooltip(
      // The whole label, for the block too narrow to carry its own name --
      // which, on a phone, is most of them.
      message: label,
      triggerMode: TooltipTriggerMode.longPress,
      child: GestureDetector(
        onTap: onTap,
        child: Container(
          decoration: BoxDecoration(
            color: flameColor(row.node.name),
            borderRadius: BorderRadius.circular(2),
            border: Border.all(
              color: Theme.of(context).colorScheme.surface,
              width: 0.5,
            ),
          ),
          padding: const EdgeInsets.symmetric(horizontal: 3),
          alignment: Alignment.centerLeft,
          child: Text(
            row.node.name,
            maxLines: 1,
            overflow: TextOverflow.clip,
            softWrap: false,
            // Black on the warm band, always: these colours are light and
            // the app's own text colour is white in dark mode.
            style: const TextStyle(fontSize: 11, color: Color(0xDD000000)),
            semanticsLabel: '$label${onTap == null ? '' : ' · ${l.flameZoom}'}',
          ),
        ),
      ),
    );
  }
}
