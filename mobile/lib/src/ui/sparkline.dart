// A line, drawn by hand.
//
// CustomPainter rather than a charting package: this draws one polyline with
// no axes, legend, tooltip or interaction, and a chart library would be a
// dependency, a licence and an upgrade treadmill for forty lines of code.
import 'package:flutter/material.dart';

class Sparkline extends StatelessWidget {
  const Sparkline({super.key, required this.values, required this.color});

  final List<double> values;
  final Color color;

  @override
  Widget build(BuildContext context) => CustomPaint(
    painter: _SparklinePainter(values, color),
    size: Size.infinite,
  );
}

class _SparklinePainter extends CustomPainter {
  _SparklinePainter(this.values, this.color);

  final List<double> values;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    if (values.length < 2 || size.width <= 0 || size.height <= 0) return;

    var min = values.first;
    var max = values.first;
    for (final v in values) {
      if (v < min) min = v;
      if (v > max) max = v;
    }
    // A flat line is drawn through the middle rather than at the bottom: a
    // constant series is not the same as a series at zero.
    final span = max - min;
    final path = Path();
    for (var i = 0; i < values.length; i++) {
      final x = size.width * i / (values.length - 1);
      final t = span == 0 ? 0.5 : (values[i] - min) / span;
      final y = size.height * (1 - t);
      i == 0 ? path.moveTo(x, y) : path.lineTo(x, y);
    }

    canvas.drawPath(
      path,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 2
        ..strokeJoin = StrokeJoin.round
        ..strokeCap = StrokeCap.round
        ..color = color,
    );
  }

  @override
  bool shouldRepaint(_SparklinePainter old) =>
      old.color != color || !identical(old.values, values);
}
