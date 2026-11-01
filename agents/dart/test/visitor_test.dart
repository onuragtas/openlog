import 'dart:io';

import 'package:openlog/openlog.dart';
import 'package:openlog/src/session.dart';
import 'package:openlog/src/visitor.dart';
import 'package:test/test.dart';

void main() {
  late Directory dir;
  File file() => File('${dir.path}/openlog_visitor');
  final idShape = RegExp(r'^[0-9a-f]{32}$');

  setUp(() => dir = Directory.systemTemp.createTempSync('openlog_visitor'));
  tearDown(() {
    dir.deleteSync(recursive: true);
    Openlog.reset();
  });

  test('leaves nothing on the device until the server confirms visitors mode', () {
    final v = Visitor(enabled: true, directory: dir.path);
    expect(v.id, matches(idShape));
    expect(file().existsSync(), isFalse);
    v.confirm();
    expect(file().existsSync(), isTrue);
  });

  test('has no id without a directory, because a new one per launch would count every launch as a person', () {
    final v = Visitor(enabled: true);
    expect(v.id, '');
    v.confirm();
    v.setEnabled(true);
    expect(v.id, '');
  });

  test('recognises the same visitor on the next launch', () {
    final a = Visitor(enabled: true, directory: dir.path)..confirm();
    expect(Visitor(enabled: true, directory: dir.path).id, a.id);
  });

  test('removes what is stored when the server says the key does not recognise visitors', () {
    Visitor(enabled: true, directory: dir.path).confirm();
    final b = Visitor(enabled: true, directory: dir.path)..discard();
    expect(b.id, '');
    expect(file().existsSync(), isFalse);
  });

  test('keeps a withdrawal across launches and through a server answer', () {
    final a = Visitor(enabled: true, directory: dir.path)..confirm();
    final before = a.id;
    a.setEnabled(false);
    expect(file().readAsStringSync(), isNot(contains(before)));

    final reload = Visitor(enabled: true, directory: dir.path);
    expect(reload.id, '', reason: 'the default must not undo a person saying no');
    reload
      ..confirm()
      ..discard();
    expect(Visitor(enabled: true, directory: dir.path).id, '');
  });

  test('gives a new id when collection is enabled again', () {
    final a = Visitor(enabled: true, directory: dir.path)..confirm();
    final before = a.id;
    a
      ..setEnabled(false)
      ..setEnabled(true);
    expect(a.id, matches(idShape));
    expect(a.id, isNot(before));
    expect(Visitor(enabled: true, directory: dir.path).id, a.id);
  });

  test('keeps the earlier id when an application that asks first gets consent', () {
    final consented = Visitor(enabled: true, directory: dir.path)..confirm();
    final v = Visitor(enabled: false, directory: dir.path);
    expect(v.id, '');
    v
      ..confirm()
      ..setEnabled(true);
    expect(v.id, consented.id);
  });

  test('rotates the id after 13 months', () {
    var now = DateTime.utc(2026, 1, 1);
    final a = Visitor(enabled: true, directory: dir.path, now: () => now)..confirm();
    now = now.add(const Duration(days: 394));
    expect(Visitor(enabled: true, directory: dir.path, now: () => now).id, a.id);
    now = now.add(const Duration(days: 2));
    expect(Visitor(enabled: true, directory: dir.path, now: () => now).id, isNot(a.id));
  });

  test('survives a corrupt file and a directory it cannot write', () {
    file().writeAsStringSync('nonsense');
    expect(Visitor(enabled: true, directory: dir.path).id, matches(idShape));
    final v = Visitor(enabled: true, directory: '${dir.path}/does/not/exist')
      ..confirm()
      ..setEnabled(false);
    expect(v.id, '');
  });

  test('matches the vector every SDK is tested against', () {
    // analytics.md §2. A different value here means one person is sampled differently per platform.
    expect(visitorRoll('fedcba9876543210fedcba9876543210'), 777805509 / 4294967296);
  });

  test('decides sampling by the visitor and not by the session', () {
    const id = 'fedcba9876543210fedcba9876543210';
    final place = visitorRoll(id);
    for (var i = 0; i < 20; i++) {
      expect(SessionState(place + 0.01, roll: () => visitorRoll(id)).sampled, isTrue);
      expect(SessionState(place - 0.01, roll: () => visitorRoll(id)).sampled, isFalse);
    }
  });

  group('Openlog', () {
    Future<void> settle() => Future<void>.delayed(Duration.zero);

    Openlog start(String mode, {bool collection = true}) => Openlog.init(
          OpenlogOptions(
            key: 'olb_x',
            endpoint: 'https://ingest.example.com',
            appId: 'com.example',
            analyticsCollection: collection,
            visitorDirectory: dir.path,
          ),
          send: (url, headers, body) async => 200,
          fetchConfig: (url, headers) async => {'analytics_mode': mode},
        );

    test('names the application when it reads the config, or a mobile key refuses it', () async {
      Map<String, String>? sent;
      Openlog.init(
        OpenlogOptions(key: 'olb_x', endpoint: 'https://ingest.example.com', appId: 'com.example'),
        send: (url, headers, body) async => 200,
        fetchConfig: (url, headers) async {
          sent = headers;
          return null;
        },
      );
      await settle();
      expect(sent?['openlog-app-id'], 'com.example');
    });

    test('stores the id on a visitors key', () async {
      final sdk = start('visitors');
      await settle();
      expect(sdk.visitorId, matches(idShape));
      expect(file().existsSync(), isTrue);
    });

    test('forgets the id on a key that is off', () async {
      final sdk = start('off');
      await settle();
      expect(sdk.visitorId, '');
      expect(file().existsSync(), isFalse);
    });

    test('setAnalyticsCollection is what a consent dialog calls', () async {
      final sdk = start('visitors', collection: false);
      await settle();
      expect(sdk.visitorId, '');
      sdk.setAnalyticsCollection(true);
      expect(sdk.visitorId, matches(idShape));
      expect(file().existsSync(), isTrue);
      sdk.setAnalyticsCollection(false);
      expect(sdk.visitorId, '');
    });
  });
}
