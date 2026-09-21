import 'package:openlog/src/session.dart';
import 'package:test/test.dart';

void main() {
  test('a quiet session expires and a new one begins', () {
    var now = DateTime.utc(2026, 1, 1, 12);
    final s = SessionState(1, now: () => now);
    final first = s.id;

    now = now.add(const Duration(minutes: 29));
    expect(s.touch(), isFalse, reason: 'still the same visit');
    expect(s.id, first);

    now = now.add(const Duration(minutes: 31));
    expect(s.touch(), isTrue, reason: 'nothing happened for over half an hour');
    expect(s.id, isNot(first));
  });

  test('a busy session still ends at the cap', () {
    var now = DateTime.utc(2026, 1, 1, 12);
    final s = SessionState(1, now: () => now);
    final first = s.id;
    // Active the whole time: the idle timeout never fires, and without the cap this would be one session
    // for as long as the screen stays open.
    for (var i = 0; i < 24; i++) {
      now = now.add(const Duration(minutes: 10));
      s.touch();
    }
    expect(s.id, isNot(first));
  });

  test('a new screen is a new trace, in the same session', () {
    final s = SessionState(1);
    final session = s.id;
    final trace = s.traceId;
    s.newScreen();
    expect(s.traceId, isNot(trace));
    expect(s.id, session);
  });

  test('sampling is decided once, for the whole session', () {
    expect(SessionState(1).shouldSend, isTrue);
    expect(SessionState(0).shouldSend, isFalse);
  });
}
