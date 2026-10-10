// The filter builder: what a picked condition becomes on the wire.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/fields.dart';

import 'fake_server.dart';

void main() {
  test('one value is =, several are one in', () {
    const one = Filter(key: 'service.name', op: '=', values: ['checkout']);
    const many = Filter(
      key: 'service.name',
      op: 'in',
      values: ['checkout', 'cart'],
    );

    // `=`, not `eq`: the server checks the operator against the contract's
    // list and answers 400 for anything else.
    expect(one.toJson(), {
      'key': 'service.name',
      'op': '=',
      'value': 'checkout',
    });
    // One condition rather than two the server has to OR back together.
    expect(many.toJson(), {
      'key': 'service.name',
      'op': 'in',
      'values': ['checkout', 'cart'],
    });
  });

  test('exists carries no value at all', () {
    const f = Filter(key: 'error.stack', op: 'exists');
    expect(f.toJson(), {'key': 'error.stack', 'op': 'exists'});
  });

  test('no filters means no parameter', () {
    expect(encodeFilters(const []), '');
    expect(
      jsonDecode(
        encodeFilters(const [
          Filter(key: 'k', op: '=', values: ['v']),
        ]),
      ),
      [
        {'key': 'k', 'op': '=', 'value': 'v'},
      ],
    );
  });

  test(
    'the dictionary is asked per signal, and says when it sampled',
    () async {
      final queries = <String>[];
      final server = await FakeServer.start((req, seen) {
        queries.add(Uri.decodeQueryComponent(seen.query));
        if (seen.path.endsWith('/values')) {
          writeJson(req, 200, {
            'key': 'service.name',
            'type': 'string',
            'values': [
              {'value': 'checkout', 'count': 412},
            ],
            'total': 412,
            'sampled': true,
          });
        } else {
          writeJson(req, 200, {
            'keys': [
              {
                'key': 'service.name',
                'name': 'name',
                'source': 'field',
                'type': 'string',
                'count': null,
                'cardinality': 7,
              },
            ],
            'sampled': false,
          });
        }
      });
      addTearDown(server.stop);
      final c = FieldsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        signal: 'logs',
      );
      addTearDown(c.dispose);

      await c.loadKeys(q: 'service');
      expect(queries.single, contains('signal=logs'));
      expect(queries.single, contains('q=service'));
      expect(c.keys.single.key, 'service.name');
      expect(c.keysSampled, isFalse);

      await c.loadValues('service.name');
      expect(queries.last, contains('key=service.name'));
      expect(c.values.single.count, 412);
      // The server counted a sample: the list is the most frequent, not all
      // of them, and the screen says so.
      expect(c.valuesSampled, isTrue);
    },
  );

  test('picking a key clears the values of the previous one', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/values')) {
        writeJson(req, 200, {
          'key': 'k',
          'type': 'string',
          'values': [
            {'value': 'v', 'count': 1},
          ],
          'total': 1,
          'sampled': false,
        });
      } else {
        writeJson(req, 200, {'keys': <Object>[], 'sampled': false});
      }
    });
    addTearDown(server.stop);
    final c = FieldsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      signal: 'traces',
    );
    addTearDown(c.dispose);

    await c.loadValues('k');
    expect(c.values, hasLength(1));

    await c.loadKeys();
    // Going back to the keys must not leave the old key's values behind,
    // or the sheet would offer them under the next key.
    expect(c.values, isEmpty);
    expect(c.key, isEmpty);
  });
}
